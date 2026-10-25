// Package frr configures an exclusively managed FRR instance through vtysh.
package frr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
)

type Driver struct {
	Runner     backend.Runner
	ReloadPath string
}

func (d *Driver) Name() string { return "frr" }
func (d *Driver) Codes() []pb.OpCode {
	return []pb.OpCode{pb.OpCode_TENANT, pb.OpCode_TENANT_L3VNI, pb.OpCode_OSPF_INSTANCE, pb.OpCode_OSPF_NETWORK, pb.OpCode_OSPF_INTERFACE, pb.OpCode_OSPF6_INSTANCE, pb.OpCode_OSPF6_INTERFACE, pb.OpCode_BGP_INSTANCE, pb.OpCode_BGP_PEER_GROUP, pb.OpCode_BGP_NEIGHBOR, pb.OpCode_BGP_EVPN_PEER, pb.OpCode_BGP_EVPN_GLOBAL, pb.OpCode_EVPN_L2VNI, pb.OpCode_EVPN_TENANT, pb.OpCode_BGP_AF_PEER, pb.OpCode_BGP_NETWORK, pb.OpCode_BGP_REDISTRIBUTE, pb.OpCode_EVPN_TYPE5}
}

var token = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.:-]{0,63}$`)
var rt = regexp.MustCompile(`^([0-9]+|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+):[0-9]+$`)

func safe(s string) error {
	if !token.MatchString(s) {
		return fmt.Errorf("invalid CLI identifier %q", s)
	}
	return nil
}
func ipv4(s string) error {
	a, e := netip.ParseAddr(s)
	if e != nil || !a.Is4() {
		return fmt.Errorf("IPv4 address required: %q", s)
	}
	return nil
}
func (d *Driver) Validate(o *pb.Operation) error {
	if o == nil {
		return fmt.Errorf("nil operation")
	}
	allowed := []pb.Action{pb.Action_CREATE, pb.Action_SET, pb.Action_DELETE}
	switch o.Code {
	case pb.OpCode_TENANT:
		allowed = []pb.Action{pb.Action_CREATE, pb.Action_DELETE}
	case pb.OpCode_TENANT_L3VNI:
		allowed = []pb.Action{pb.Action_SET, pb.Action_BIND, pb.Action_UNBIND}
	case pb.OpCode_OSPF_INTERFACE, pb.OpCode_OSPF6_INTERFACE, pb.OpCode_BGP_AF_PEER, pb.OpCode_EVPN_TYPE5, pb.OpCode_BGP_EVPN_PEER, pb.OpCode_BGP_EVPN_GLOBAL, pb.OpCode_EVPN_TENANT:
		allowed = []pb.Action{pb.Action_SET, pb.Action_DELETE}
	}
	ok := false
	for _, action := range allowed {
		if action == o.Action {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("unsupported action %s for %s", o.Action, o.Code)
	}
	asn := uint32(0)
	vrf := ""
	switch o.Code {
	case pb.OpCode_TENANT:
		x := o.GetTenant()
		if x == nil {
			return fmt.Errorf("tenant required")
		}
		if o.Action != pb.Action_CREATE && o.Action != pb.Action_DELETE {
			return fmt.Errorf("tenant action")
		}
		return safe(x.Name)
	case pb.OpCode_TENANT_L3VNI:
		x := o.GetL3Vni()
		if x == nil {
			return fmt.Errorf("l3vni required")
		}
		if x.Vni == 0 || x.Vni > 16777215 {
			return fmt.Errorf("invalid VNI")
		}
		return safe(x.Tenant)
	case pb.OpCode_OSPF_INSTANCE, pb.OpCode_OSPF6_INSTANCE:
		x := o.GetOspf()
		if x == nil {
			return fmt.Errorf("ospf required")
		}
		if x.Vrf != "" {
			if err := safe(x.Vrf); err != nil {
				return err
			}
		}
		if o.Action == pb.Action_DELETE {
			return nil
		}
		return ipv4(x.RouterId)
	case pb.OpCode_OSPF_NETWORK:
		x := o.GetOspfNetwork()
		if x == nil {
			return fmt.Errorf("ospf_network required")
		}
		p, e := netip.ParsePrefix(x.Prefix)
		if e != nil || !p.Addr().Is4() {
			return fmt.Errorf("OSPFv2 requires IPv4 prefix")
		}
		if x.Vrf != "" {
			if e = safe(x.Vrf); e != nil {
				return e
			}
		}
		return area(x.Area)
	case pb.OpCode_OSPF_INTERFACE:
		x := o.GetOspfInterface()
		if x == nil {
			return fmt.Errorf("ospf_interface required")
		}
		return validateOspfInterface(x.Name, x.Vrf, x.Area, x.NetworkType, x.Cost, x.HelloInterval, x.DeadInterval)
	case pb.OpCode_OSPF6_INTERFACE:
		x := o.GetOspf6Interface()
		if x == nil {
			return fmt.Errorf("ospf6_interface required")
		}
		return validateOspfInterface(x.Name, x.Vrf, x.Area, x.NetworkType, x.Cost, x.HelloInterval, x.DeadInterval)
	case pb.OpCode_BGP_INSTANCE:
		x := o.GetBgp()
		if x == nil {
			return fmt.Errorf("bgp required")
		}
		asn, vrf = x.Asn, x.Vrf
		if o.Action != pb.Action_DELETE {
			if e := ipv4(x.RouterId); e != nil {
				return e
			}
		}
	case pb.OpCode_BGP_PEER_GROUP, pb.OpCode_BGP_NEIGHBOR, pb.OpCode_BGP_EVPN_PEER, pb.OpCode_BGP_AF_PEER:
		x := o.GetPeer()
		if x == nil {
			return fmt.Errorf("peer required")
		}
		asn, vrf = x.Asn, x.Vrf
		for _, s := range []string{x.Name, x.UpdateSource, x.PeerGroup} {
			if s != "" {
				if e := safe(s); e != nil {
					return e
				}
			}
		}
		if x.Name == "" {
			return fmt.Errorf("peer name required")
		}
		if o.Code == pb.OpCode_BGP_NEIGHBOR {
			if _, e := netip.ParseAddr(x.Name); e != nil {
				return e
			}
		}
		if o.Code == pb.OpCode_BGP_AF_PEER && x.Family != pb.AddressFamily_IPV4 && x.Family != pb.AddressFamily_IPV6 {
			return fmt.Errorf("IPv4 or IPv6 address family required")
		}
	case pb.OpCode_BGP_NETWORK:
		x := o.GetBgpNetwork()
		if x == nil {
			return fmt.Errorf("bgp_network required")
		}
		asn, vrf = x.Asn, x.Vrf
		if err := familyPrefix(x.Family, x.Prefix); err != nil {
			return err
		}
	case pb.OpCode_BGP_REDISTRIBUTE:
		x := o.GetBgpRedistribute()
		if x == nil {
			return fmt.Errorf("bgp_redistribute required")
		}
		asn, vrf = x.Asn, x.Vrf
		if x.Family != pb.AddressFamily_IPV4 && x.Family != pb.AddressFamily_IPV6 {
			return fmt.Errorf("IPv4 or IPv6 address family required")
		}
		if x.Protocol != "connected" && x.Protocol != "static" && x.Protocol != "ospf" && x.Protocol != "ospf6" {
			return fmt.Errorf("unsupported redistribution protocol")
		}
		if x.Protocol == "ospf" && x.Family != pb.AddressFamily_IPV4 || x.Protocol == "ospf6" && x.Family != pb.AddressFamily_IPV6 {
			return fmt.Errorf("redistribution protocol/address family mismatch")
		}
	case pb.OpCode_EVPN_TYPE5:
		x := o.GetBgpType5()
		if x == nil {
			return fmt.Errorf("bgp_type5 required")
		}
		asn, vrf = x.Asn, x.Vrf
		if vrf == "" || x.Family != pb.AddressFamily_IPV4 && x.Family != pb.AddressFamily_IPV6 {
			return fmt.Errorf("tenant VRF and IPv4 or IPv6 family required")
		}
		if o.Action != pb.Action_DELETE && x.Enabled == nil {
			return fmt.Errorf("enabled required")
		}
	case pb.OpCode_BGP_EVPN_GLOBAL:
		x := o.GetEvpnGlobal()
		if x == nil {
			return fmt.Errorf("evpn_global required")
		}
		asn = x.Asn
	case pb.OpCode_EVPN_L2VNI, pb.OpCode_EVPN_TENANT:
		x := o.GetEvpn()
		if x == nil {
			return fmt.Errorf("evpn required")
		}
		asn, vrf = x.Asn, x.Vrf
		if o.Code == pb.OpCode_EVPN_L2VNI && (x.Vni == 0 || x.Vni > 16777215) {
			return fmt.Errorf("invalid VNI")
		}
		if o.Code == pb.OpCode_EVPN_TENANT && vrf == "" {
			return fmt.Errorf("tenant VRF required")
		}
		for _, s := range append(append([]string{x.Rd}, x.ImportRts...), x.ExportRts...) {
			if s != "" && !rt.MatchString(s) {
				return fmt.Errorf("invalid RD/RT %q", s)
			}
		}
	default:
		return fmt.Errorf("unsupported op")
	}
	if asn == 0 {
		return fmt.Errorf("ASN required")
	}
	if vrf != "" {
		return safe(vrf)
	}
	return nil
}

type expectation struct {
	Root, AF, VNI, Line string
	Absent              bool
}

func bgp(asn uint32, vrf string) string {
	s := fmt.Sprintf("router bgp %d", asn)
	if vrf != "" {
		s += " vrf " + vrf
	}
	return s
}
func render(o *pb.Operation) ([]string, []expectation) {
	var cmds []string
	var checks []expectation
	root, af, vni := "", "", ""
	del := o.Action == pb.Action_DELETE || o.Action == pb.Action_UNBIND
	enter := func(s string) { root = s; cmds = append(cmds, s) }
	line := func(s string, remove bool) {
		c := s
		if remove {
			c = "no " + s
		}
		cmds = append(cmds, c)
		checks = append(checks, expectation{root, af, vni, s, remove})
	}
	afEnter := func() { af = "address-family l2vpn evpn"; cmds = append(cmds, af) }
	rootOp := func(s string) {
		if del {
			cmds = append(cmds, "no "+s)
		} else {
			cmds = append(cmds, s)
		}
		checks = append(checks, expectation{Root: s, Absent: del})
		root = s
	}
	switch o.Code {
	case pb.OpCode_TENANT:
		rootOp("vrf " + o.GetTenant().Name)
	case pb.OpCode_TENANT_L3VNI:
		x := o.GetL3Vni()
		enter("vrf " + x.Tenant)
		line(fmt.Sprintf("vni %d", x.Vni), del)
	case pb.OpCode_OSPF_INSTANCE:
		x := o.GetOspf()
		rootOp(withVRF("router ospf", x.Vrf))
		if !del {
			line("ospf router-id "+x.RouterId, false)
		}
	case pb.OpCode_OSPF_NETWORK:
		x := o.GetOspfNetwork()
		enter(withVRF("router ospf", x.Vrf))
		line("network "+x.Prefix+" area "+x.Area, del)
	case pb.OpCode_OSPF_INTERFACE:
		x := o.GetOspfInterface()
		enter(withVRF("interface "+x.Name, x.Vrf))
		if x.Area != "" {
			line("ip ospf area "+canonicalArea(x.Area), del)
		}
		if x.Passive != nil {
			line("ip ospf passive", del || !*x.Passive)
		}
		if x.NetworkType != "" {
			line("ip ospf network "+x.NetworkType, del)
		}
		for _, p := range ospfTimers(x.Cost, x.HelloInterval, x.DeadInterval) {
			line("ip ospf "+p, del)
		}
	case pb.OpCode_OSPF6_INSTANCE:
		x := o.GetOspf()
		rootOp(withVRF("router ospf6", x.Vrf))
		if !del {
			line("ospf6 router-id "+x.RouterId, false)
		}
	case pb.OpCode_OSPF6_INTERFACE:
		x := o.GetOspf6Interface()
		enter(withVRF("interface "+x.Name, x.Vrf))
		if x.Area != "" {
			line("ipv6 ospf6 area "+canonicalArea(x.Area), del)
		}
		if x.Passive != nil {
			line("ipv6 ospf6 passive", del || !*x.Passive)
		}
		if x.NetworkType != "" {
			line("ipv6 ospf6 network "+x.NetworkType, del)
		}
		for _, p := range ospfTimers(x.Cost, x.HelloInterval, x.DeadInterval) {
			line("ipv6 ospf6 "+p, del)
		}
	case pb.OpCode_BGP_INSTANCE:
		x := o.GetBgp()
		rootOp(bgp(x.Asn, x.Vrf))
		if !del {
			line("bgp router-id "+x.RouterId, false)
			if x.DefaultIpv4Unicast != nil {
				if *x.DefaultIpv4Unicast {
					cmds = append(cmds, "bgp default ipv4-unicast")
					checks = append(checks, expectation{Root: root, Line: "no bgp default ipv4-unicast", Absent: true})
				} else {
					cmds = append(cmds, "no bgp default ipv4-unicast")
					checks = append(checks, expectation{Root: root, Line: "no bgp default ipv4-unicast"})
				}
			}
		}
	case pb.OpCode_BGP_PEER_GROUP, pb.OpCode_BGP_NEIGHBOR:
		x := o.GetPeer()
		enter(bgp(x.Asn, x.Vrf))
		n := "neighbor " + x.Name
		if del {
			cmds = append(cmds, "no "+n)
			checks = append(checks, expectation{Root: root, Line: n, Absent: true})
			break
		}
		if o.Code == pb.OpCode_BGP_PEER_GROUP {
			line(n+" peer-group", false)
		}
		if x.PeerGroup != "" {
			line(n+" peer-group "+x.PeerGroup, false)
		}
		if x.RemoteAs != 0 {
			line(fmt.Sprintf("%s remote-as %d", n, x.RemoteAs), false)
		}
		if x.UpdateSource != "" {
			line(n+" update-source "+x.UpdateSource, false)
		}
	case pb.OpCode_BGP_EVPN_PEER, pb.OpCode_BGP_AF_PEER:
		x := o.GetPeer()
		enter(bgp(x.Asn, x.Vrf))
		if o.Code == pb.OpCode_BGP_AF_PEER {
			af = "address-family " + familyName(x.Family) + " unicast"
			cmds = append(cmds, af)
		} else {
			afEnter()
		}
		if x.Active != nil {
			line("neighbor "+x.Name+" activate", del || !*x.Active)
		}
		if x.RrClient != nil {
			line("neighbor "+x.Name+" route-reflector-client", del || !*x.RrClient)
		}
	case pb.OpCode_BGP_NETWORK:
		x := o.GetBgpNetwork()
		enter(bgp(x.Asn, x.Vrf))
		af = "address-family " + familyName(x.Family) + " unicast"
		cmds = append(cmds, af)
		p, _ := netip.ParsePrefix(x.Prefix)
		line("network "+p.Masked().String(), del)
	case pb.OpCode_BGP_REDISTRIBUTE:
		x := o.GetBgpRedistribute()
		enter(bgp(x.Asn, x.Vrf))
		af = "address-family " + familyName(x.Family) + " unicast"
		cmds = append(cmds, af)
		line("redistribute "+x.Protocol, del)
	case pb.OpCode_EVPN_TYPE5:
		x := o.GetBgpType5()
		enter(bgp(x.Asn, x.Vrf))
		afEnter()
		line("advertise "+familyName(x.Family)+" unicast", del || !x.GetEnabled())
	case pb.OpCode_BGP_EVPN_GLOBAL:
		x := o.GetEvpnGlobal()
		enter(bgp(x.Asn, ""))
		afEnter()
		if x.AdvertiseAllVni != nil {
			line("advertise-all-vni", del || !*x.AdvertiseAllVni)
		}
		if x.AdvertiseSviIp != nil {
			line("advertise-svi-ip", del || !*x.AdvertiseSviIp)
		}
	case pb.OpCode_EVPN_L2VNI, pb.OpCode_EVPN_TENANT:
		x := o.GetEvpn()
		enter(bgp(x.Asn, x.Vrf))
		afEnter()
		if o.Code == pb.OpCode_EVPN_L2VNI {
			vni = fmt.Sprintf("vni %d", x.Vni)
			if del {
				cmds = append(cmds, "no "+vni)
				checks = append(checks, expectation{Root: root, AF: af, Line: vni, Absent: true})
				break
			}
			cmds = append(cmds, vni)
		}
		if x.Rd != "" {
			line("rd "+x.Rd, del)
		}
		for _, rt := range x.ImportRts {
			line("route-target import "+rt, del)
		}
		for _, rt := range x.ExportRts {
			line("route-target export "+rt, del)
		}
	}
	return cmds, checks
}
func (d *Driver) cli(ctx context.Context, commands ...string) (string, error) {
	args := []string{"vtysh"}
	for _, c := range commands {
		args = append(args, "-c", c)
	}
	out, e := d.Runner.Run(ctx, args...)
	if e != nil {
		return out, e
	}
	for _, l := range strings.Split(out, "\n") {
		s := strings.TrimSpace(l)
		if (strings.HasPrefix(s, "%") && !strings.Contains(s, "No changes")) || strings.HasPrefix(s, "This command is only supported") || strings.HasPrefix(s, "ERROR:") {
			return out, fmt.Errorf("FRR rejected command: %s", s)
		}
	}
	return out, nil
}
func config(out string) string {
	if i := strings.Index(out, "frr version "); i >= 0 {
		out = out[i:]
	}
	return strings.TrimSpace(out) + "\n"
}
func (d *Driver) Prepare(ctx context.Context, o *pb.Operation) (*model.Undo, error) {
	if o.Code == pb.OpCode_TENANT_L3VNI && o.Action != pb.Action_UNBIND {
		if e := d.requireL3Resources(ctx, o.GetL3Vni()); e != nil {
			return nil, e
		}
	}
	out, e := d.cli(ctx, "show running-config")
	if e != nil {
		return nil, e
	}
	return &model.Undo{Files: map[string]string{"frr.conf": config(out)}}, nil
}
func (d *Driver) Apply(ctx context.Context, o *pb.Operation) error {
	cs, _ := render(o)
	commands := append([]string{"configure terminal"}, cs...)
	commands = append(commands, "end")
	if _, e := d.cli(ctx, commands...); e != nil {
		return e
	}
	_, e := d.cli(ctx, "write memory")
	return e
}

// block selects exactly one indentation scope; it never mixes tenant instances.
func block(lines []string, want string) []string {
	for i, l := range lines {
		if strings.TrimSpace(l) != want {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " "))
		end := i + 1
		for end < len(lines) {
			s := lines[end]
			t := strings.TrimSpace(s)
			if t != "" && t != "!" && len(s)-len(strings.TrimLeft(s, " ")) <= indent {
				break
			}
			end++
		}
		return lines[i:end]
	}
	return nil
}
func has(lines []string, want string) bool {
	for _, s := range lines {
		t := strings.TrimSpace(s)
		fields, wanted := strings.Fields(t), strings.Fields(want)
		if len(fields) >= 3 && len(wanted) == 3 && fields[0] == "route-target" && wanted[0] == "route-target" && (fields[1] == wanted[1] || fields[1] == "both") {
			for _, rt := range fields[2:] {
				if rt == wanted[2] {
					return true
				}
			}
		}
		if t == want || strings.HasPrefix(t, want+" ") {
			return true
		}
	}
	return false
}
func (d *Driver) Verify(ctx context.Context, o *pb.Operation) error {
	out, e := d.cli(ctx, "show running-config")
	if e != nil {
		return e
	}
	_, checks := render(o)
	for _, c := range checks {
		ls := block(strings.Split(config(out), "\n"), c.Root)
		present := len(ls) > 0
		if c.Line != "" {
			if c.AF != "" {
				ls = block(ls, c.AF)
			}
			if c.VNI != "" {
				ls = block(ls, c.VNI)
			}
			present = has(ls, c.Line)
			// FRR omits configured default OSPF timers from running-config.
			// Absence means the default only when no value for that timer exists.
			if !present && !c.Absent && (o.Code == pb.OpCode_OSPF_INTERFACE || o.Code == pb.OpCode_OSPF6_INTERFACE) {
				for _, suffix := range []string{"hello-interval 10", "dead-interval 40"} {
					if strings.HasSuffix(c.Line, suffix) {
						base := strings.TrimSuffix(c.Line, " "+strings.Fields(suffix)[1])
						present = len(ls) > 0 && !has(ls, base)
					}
				}
			}
			if o.Code == pb.OpCode_BGP_AF_PEER && o.GetPeer().Family == pb.AddressFamily_IPV4 && strings.HasSuffix(c.Line, " activate") && !present {
				x := o.GetPeer()
				if _, err := netip.ParseAddr(x.Name); err == nil {
					data, err := d.queryJSON(ctx, withVRF("show bgp", x.Vrf)+" neighbors "+x.Name+" json")
					if err != nil {
						return err
					}
					afName := "ipv4Unicast"
					if x.Family == pb.AddressFamily_IPV6 {
						afName = "ipv6Unicast"
					}
					_, present = objectValue(objectValue(data, x.Name), "addressFamilyInfo")[afName]
				}
			}
			// FRR leaves an RT equal to its auto-derived value implicit in
			// running-config. Confirm the effective VNI RTs before rejecting it.
			if !present && !c.Absent && o.Code == pb.OpCode_EVPN_L2VNI && strings.HasPrefix(c.Line, "route-target ") {
				text, err := d.cli(ctx, fmt.Sprintf("show bgp l2vpn evpn vni %d json", o.GetEvpn().Vni))
				if err != nil {
					return err
				}
				var effective struct {
					Import []string `json:"importRts"`
					Export []string `json:"exportRts"`
				}
				if err = json.Unmarshal([]byte(text), &effective); err != nil {
					return err
				}
				fields := strings.Fields(c.Line)
				rts := effective.Import
				if fields[1] == "export" {
					rts = effective.Export
				}
				for _, rt := range rts {
					if rt == fields[2] {
						present = true
					}
				}
			}
		}
		if present == c.Absent {
			return fmt.Errorf("FRR readback failed: root=%s af=%s vni=%s line=%s absent=%t", c.Root, c.AF, c.VNI, c.Line, c.Absent)
		}
	}
	return nil
}
func canonical(cfg string) string {
	var paths []string
	var stack []string
	var levels []int
	for _, l := range strings.Split(config(cfg), "\n") {
		s := strings.TrimSpace(l)
		if s == "" || s == "!" || strings.HasPrefix(s, "exit") || s == "end" || strings.HasPrefix(s, "frr version ") {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		for len(levels) > 0 && levels[len(levels)-1] >= n {
			levels = levels[:len(levels)-1]
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, s)
		levels = append(levels, n)
		paths = append(paths, strings.Join(stack, "/"))
	}
	sort.Strings(paths)
	return strings.Join(paths, "\n")
}
func (d *Driver) Rollback(ctx context.Context, o *pb.Operation, u *model.Undo) error {
	if u == nil || u.Files["frr.conf"] == "" {
		return fmt.Errorf("missing FRR snapshot")
	}
	before := u.Files["frr.conf"]
	now, e := d.cli(ctx, "show running-config")
	if e == nil && canonical(before) == canonical(now) {
		return nil
	}
	// Removing a newly created peer as one object avoids frr-reload removing
	// remote-as before update-source (which makes subsequent CLI removals fail).
	if o.Code == pb.OpCode_BGP_NEIGHBOR || o.Code == pb.OpCode_BGP_PEER_GROUP {
		x := o.GetPeer()
		root := bgp(x.Asn, x.Vrf)
		if !has(block(strings.Split(before, "\n"), root), "neighbor "+x.Name) {
			if _, e = d.cli(ctx, "configure terminal", root, "no neighbor "+x.Name, "end"); e != nil {
				return e
			}
		}
	}
	f, e := os.CreateTemp("", "sdn-frr-*.conf")
	if e != nil {
		return e
	}
	path := f.Name()
	defer os.Remove(path)
	if _, e = f.WriteString(before); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	reload := d.ReloadPath
	if reload == "" {
		reload = "/usr/lib/frr/frr-reload.py"
	}
	_, reloadErr := d.Runner.Run(ctx, reload, "--reload", "--overwrite", path)
	if _, e = d.cli(ctx, "write memory"); e != nil {
		return e
	}
	for i := 0; i < 10; i++ {
		now, e = d.cli(ctx, "show running-config")
		if e == nil && canonical(before) == canonical(now) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("FRR rollback readback differs from snapshot (reload: %v)", reloadErr)
}
func (d *Driver) Observe(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, c := range map[string]string{"evpn": "show bgp l2vpn evpn json", "bgp": "show bgp l2vpn evpn summary json", "vni": "show evpn vni json", "ospf": "show ip ospf neighbor json"} {
		s, e := d.cli(ctx, c)
		if e != nil {
			return out, e
		}
		out[k] = s
	}
	return out, nil
}
