package frr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
)

type frrCase struct {
	code                pb.OpCode
	actions             []pb.Action
	data                string
	set, remove         []string
	configured, removed string
}

const emptyConfig = "frr version 10\n!\nend\n"
const bgpRoot = "router bgp 65000"
const evpnAF = "address-family l2vpn evpn"

// Config readbacks and expected commands are independent fixtures, not render()
// output. They check scoped semantics as well as exact argv and CLI sequencing.
func frrCases() []frrCase {
	crud := []pb.Action{pb.Action_CREATE, pb.Action_SET, pb.Action_DELETE}
	setDelete := []pb.Action{pb.Action_SET, pb.Action_DELETE}
	return []frrCase{
		{pb.OpCode_OSPF6_INSTANCE, crud, `"ospf":{"router_id":"192.0.2.1"}`,
			[]string{"router ospf6", "ospf6 router-id 192.0.2.1"}, []string{"no router ospf6"}, "router ospf6\n ospf6 router-id 192.0.2.1\nexit\n", ""},
		{pb.OpCode_OSPF6_INTERFACE, setDelete, `"ospf6_interface":{"name":"eth0","area":"0.0.0.0","passive":true,"network_type":"point-to-point","cost":10}`,
			[]string{"interface eth0", "ipv6 ospf6 area 0.0.0.0", "ipv6 ospf6 passive", "ipv6 ospf6 network point-to-point", "ipv6 ospf6 cost 10"}, []string{"interface eth0", "no ipv6 ospf6 area 0.0.0.0", "no ipv6 ospf6 passive", "no ipv6 ospf6 network point-to-point", "no ipv6 ospf6 cost 10"}, "interface eth0\n ipv6 ospf6 area 0.0.0.0\n ipv6 ospf6 passive\n ipv6 ospf6 network point-to-point\n ipv6 ospf6 cost 10\nexit\n", "interface eth0\nexit\n"},
		{pb.OpCode_BGP_AF_PEER, setDelete, `"peer":{"asn":65000,"name":"2001:db8::2","family":"IPV6","active":true}`,
			[]string{bgpRoot, "address-family ipv6 unicast", "neighbor 2001:db8::2 activate"}, []string{bgpRoot, "address-family ipv6 unicast", "no neighbor 2001:db8::2 activate"}, bgpRoot + "\n address-family ipv6 unicast\n  neighbor 2001:db8::2 activate\n exit-address-family\nexit\n", bgpRoot + "\nexit\n"},
		{pb.OpCode_BGP_NETWORK, crud, `"bgp_network":{"asn":65000,"family":"IPV6","prefix":"2001:db8:1::/64"}`,
			[]string{bgpRoot, "address-family ipv6 unicast", "network 2001:db8:1::/64"}, []string{bgpRoot, "address-family ipv6 unicast", "no network 2001:db8:1::/64"}, bgpRoot + "\n address-family ipv6 unicast\n  network 2001:db8:1::/64\n exit-address-family\nexit\n", bgpRoot + "\nexit\n"},
		{pb.OpCode_BGP_REDISTRIBUTE, crud, `"bgp_redistribute":{"asn":65000,"family":"IPV6","protocol":"connected"}`,
			[]string{bgpRoot, "address-family ipv6 unicast", "redistribute connected"}, []string{bgpRoot, "address-family ipv6 unicast", "no redistribute connected"}, bgpRoot + "\n address-family ipv6 unicast\n  redistribute connected\n exit-address-family\nexit\n", bgpRoot + "\nexit\n"},
		{pb.OpCode_EVPN_TYPE5, setDelete, `"bgp_type5":{"asn":65000,"vrf":"blue","family":"IPV6","enabled":true}`,
			[]string{bgpRoot + " vrf blue", evpnAF, "advertise ipv6 unicast"}, []string{bgpRoot + " vrf blue", evpnAF, "no advertise ipv6 unicast"}, bgpRoot + " vrf blue\n " + evpnAF + "\n  advertise ipv6 unicast\n exit-address-family\nexit\n", bgpRoot + " vrf blue\nexit\n"},
		{pb.OpCode_TENANT, []pb.Action{pb.Action_CREATE, pb.Action_DELETE}, `"tenant":{"name":"blue","table_id":1001}`,
			[]string{"vrf blue"}, []string{"no vrf blue"}, "vrf blue\nexit-vrf\n", ""},
		{pb.OpCode_TENANT_L3VNI, []pb.Action{pb.Action_BIND, pb.Action_SET, pb.Action_UNBIND}, `"l3vni":{"tenant":"blue","vni":50000}`,
			[]string{"vrf blue", "vni 50000"}, []string{"vrf blue", "no vni 50000"}, "vrf blue\n vni 50000\nexit-vrf\n", "vrf blue\nexit-vrf\n"},
		{pb.OpCode_OSPF_INSTANCE, crud, `"ospf":{"router_id":"192.0.2.1"}`,
			[]string{"router ospf", "ospf router-id 192.0.2.1"}, []string{"no router ospf"}, "router ospf\n ospf router-id 192.0.2.1\nexit\n", ""},
		{pb.OpCode_OSPF_NETWORK, crud, `"ospf_network":{"prefix":"192.0.2.0/24","area":"0"}`,
			[]string{"router ospf", "network 192.0.2.0/24 area 0"}, []string{"router ospf", "no network 192.0.2.0/24 area 0"}, "router ospf\n network 192.0.2.0/24 area 0\nexit\n", "router ospf\nexit\n"},
		{pb.OpCode_OSPF_INTERFACE, setDelete, `"ospf_interface":{"name":"eth0","passive":true,"network_type":"point-to-point"}`,
			[]string{"interface eth0", "ip ospf passive", "ip ospf network point-to-point"}, []string{"interface eth0", "no ip ospf passive", "no ip ospf network point-to-point"}, "interface eth0\n ip ospf passive\n ip ospf network point-to-point\nexit\n", "interface eth0\nexit\n"},
		{pb.OpCode_BGP_INSTANCE, crud, `"bgp":{"asn":65000,"router_id":"192.0.2.1","default_ipv4_unicast":false}`,
			[]string{bgpRoot, "bgp router-id 192.0.2.1", "no bgp default ipv4-unicast"}, []string{"no " + bgpRoot}, bgpRoot + "\n bgp router-id 192.0.2.1\n no bgp default ipv4-unicast\nexit\n", ""},
		{pb.OpCode_BGP_PEER_GROUP, crud, `"peer":{"asn":65000,"name":"LEAF","remote_as":65001,"update_source":"lo"}`,
			[]string{bgpRoot, "neighbor LEAF peer-group", "neighbor LEAF remote-as 65001", "neighbor LEAF update-source lo"}, []string{bgpRoot, "no neighbor LEAF"}, bgpRoot + "\n neighbor LEAF peer-group\n neighbor LEAF remote-as 65001\n neighbor LEAF update-source lo\nexit\n", bgpRoot + "\nexit\n"},
		{pb.OpCode_BGP_NEIGHBOR, crud, `"peer":{"asn":65000,"name":"192.0.2.2","peer_group":"LEAF"}`,
			[]string{bgpRoot, "neighbor 192.0.2.2 peer-group LEAF"}, []string{bgpRoot, "no neighbor 192.0.2.2"}, bgpRoot + "\n neighbor 192.0.2.2 peer-group LEAF\nexit\n", bgpRoot + "\nexit\n"},
		{pb.OpCode_BGP_EVPN_PEER, setDelete, `"peer":{"asn":65000,"name":"LEAF","active":true,"rr_client":true}`,
			[]string{bgpRoot, evpnAF, "neighbor LEAF activate", "neighbor LEAF route-reflector-client"}, []string{bgpRoot, evpnAF, "no neighbor LEAF activate", "no neighbor LEAF route-reflector-client"}, bgpRoot + "\n " + evpnAF + "\n  neighbor LEAF activate\n  neighbor LEAF route-reflector-client\n exit-address-family\nexit\n", bgpRoot + "\n " + evpnAF + "\n exit-address-family\nexit\n"},
		{pb.OpCode_BGP_EVPN_GLOBAL, setDelete, `"evpn_global":{"asn":65000,"advertise_all_vni":true,"advertise_svi_ip":true}`,
			[]string{bgpRoot, evpnAF, "advertise-all-vni", "advertise-svi-ip"}, []string{bgpRoot, evpnAF, "no advertise-all-vni", "no advertise-svi-ip"}, bgpRoot + "\n " + evpnAF + "\n  advertise-all-vni\n  advertise-svi-ip\n exit-address-family\nexit\n", bgpRoot + "\n " + evpnAF + "\n exit-address-family\nexit\n"},
		{pb.OpCode_EVPN_L2VNI, crud, `"evpn":{"asn":65000,"vni":10010,"rd":"192.0.2.1:10","import_rts":["65000:10"],"export_rts":["65000:10"]}`,
			[]string{bgpRoot, evpnAF, "vni 10010", "rd 192.0.2.1:10", "route-target import 65000:10", "route-target export 65000:10"}, []string{bgpRoot, evpnAF, "no vni 10010"}, bgpRoot + "\n " + evpnAF + "\n  vni 10010\n   rd 192.0.2.1:10\n   route-target both 65000:10\n  exit-vni\n exit-address-family\nexit\n", bgpRoot + "\n " + evpnAF + "\n exit-address-family\nexit\n"},
		{pb.OpCode_EVPN_TENANT, setDelete, `"evpn":{"asn":65000,"vrf":"blue","rd":"192.0.2.1:50000","import_rts":["65000:1"],"export_rts":["65000:1"]}`,
			[]string{bgpRoot + " vrf blue", evpnAF, "rd 192.0.2.1:50000", "route-target import 65000:1", "route-target export 65000:1"}, []string{bgpRoot + " vrf blue", evpnAF, "no rd 192.0.2.1:50000", "no route-target import 65000:1", "no route-target export 65000:1"}, bgpRoot + " vrf blue\n " + evpnAF + "\n  rd 192.0.2.1:50000\n  route-target both 65000:1\n exit-address-family\nexit\n", bgpRoot + " vrf blue\n " + evpnAF + "\n exit-address-family\nexit\n"},
	}
}
func frrOp(t *testing.T, c frrCase, a pb.Action) *pb.Operation {
	t.Helper()
	o := new(pb.Operation)
	if err := protojson.Unmarshal([]byte(fmt.Sprintf(`{"id":"test","code":"%s","action":"%s",%s}`, c.code, a, c.data)), o); err != nil {
		t.Fatal(err)
	}
	return o
}
func TestFRROperationValidation(t *testing.T) {
	d := new(Driver)
	covered := map[pb.OpCode]bool{}
	for _, c := range frrCases() {
		covered[c.code] = true
		t.Run(c.code.String(), func(t *testing.T) {
			for n, name := range pb.Action_name {
				t.Run(name, func(t *testing.T) {
					allowed := false
					for _, a := range c.actions {
						allowed = allowed || a == pb.Action(n)
					}
					err := d.Validate(frrOp(t, c, pb.Action(n)))
					if (err == nil) != allowed {
						t.Fatalf("accepted=%t want=%t: %v", err == nil, allowed, err)
					}
				})
			}
			for _, kind := range []string{"missing_payload", "wrong_payload", "unknown_action"} {
				t.Run(kind, func(t *testing.T) {
					o := frrOp(t, c, c.actions[0])
					switch kind {
					case "missing_payload":
						o.Data = nil
					case "wrong_payload":
						o.Data = &pb.Operation_Route{Route: &pb.Route{}}
					case "unknown_action":
						o.Action = pb.Action(999)
					}
					if d.Validate(o) == nil {
						t.Fatal("invalid operation accepted")
					}
				})
			}
		})
	}
	for _, c := range d.Codes() {
		if !covered[c] {
			t.Errorf("untested operation %s", c)
		}
	}
	t.Run("nil", func(t *testing.T) {
		if d.Validate(nil) == nil {
			t.Fatal("nil operation accepted")
		}
	})
	t.Run("unknown_code", func(t *testing.T) {
		if d.Validate(&pb.Operation{Code: pb.OpCode(999), Action: pb.Action_SET}) == nil {
			t.Fatal("unknown operation accepted")
		}
	})
}

func TestFRRParameterBoundaries(t *testing.T) {
	for _, c := range frrCases() {
		t.Run(c.code.String(), func(t *testing.T) {
			base := frrOp(t, c, c.actions[0])
			check := func(name string, valid bool, change func(*pb.Operation)) {
				t.Run(name, func(t *testing.T) {
					o := proto.Clone(base).(*pb.Operation)
					change(o)
					err := new(Driver).Validate(o)
					if (err == nil) != valid {
						t.Fatalf("valid=%t: %v", valid, err)
					}
				})
			}
			switch c.code {
			case pb.OpCode_TENANT:
				check("empty_name", false, func(o *pb.Operation) { o.GetTenant().Name = "" })
				check("name_64", true, func(o *pb.Operation) { o.GetTenant().Name = strings.Repeat("x", 64) })
				check("name_65", false, func(o *pb.Operation) { o.GetTenant().Name = strings.Repeat("x", 65) })
				check("injection", false, func(o *pb.Operation) { o.GetTenant().Name = "blue\nend" })
			case pb.OpCode_TENANT_L3VNI:
				for _, v := range []uint32{0, 1, 16777215, 16777216} {
					check(fmt.Sprintf("vni_%d", v), v > 0 && v < 16777216, func(o *pb.Operation) { o.GetL3Vni().Vni = v })
				}
				check("invalid_tenant", false, func(o *pb.Operation) { o.GetL3Vni().Tenant = "blue;id" })
			case pb.OpCode_OSPF_INSTANCE:
				for _, s := range []string{"", "bad", "2001:db8::1"} {
					check("router_id_"+s, false, func(o *pb.Operation) { o.GetOspf().RouterId = s })
				}
			case pb.OpCode_OSPF_NETWORK:
				check("invalid_prefix", false, func(o *pb.Operation) { o.GetOspfNetwork().Prefix = "192.0.2.0/33" })
				check("area_ipv4", true, func(o *pb.Operation) { o.GetOspfNetwork().Area = "0.0.0.1" })
				check("invalid_area", false, func(o *pb.Operation) { o.GetOspfNetwork().Area = "bad" })
			case pb.OpCode_OSPF_INTERFACE:
				check("invalid_name", false, func(o *pb.Operation) { o.GetOspfInterface().Name = "eth0\nshutdown" })
				check("broadcast", true, func(o *pb.Operation) { o.GetOspfInterface().NetworkType = "broadcast" })
				check("invalid_network_type", false, func(o *pb.Operation) { o.GetOspfInterface().NetworkType = "unknown" })
			case pb.OpCode_BGP_INSTANCE:
				check("asn_zero", false, func(o *pb.Operation) { o.GetBgp().Asn = 0 })
				check("asn_uint32_max", true, func(o *pb.Operation) { o.GetBgp().Asn = ^uint32(0) })
				check("invalid_router_id", false, func(o *pb.Operation) { o.GetBgp().RouterId = "2001:db8::1" })
				check("delete_without_router_id", true, func(o *pb.Operation) { o.Action = pb.Action_DELETE; o.GetBgp().RouterId = "" })
				check("invalid_vrf", false, func(o *pb.Operation) { o.GetBgp().Vrf = "--bad" })
			case pb.OpCode_BGP_PEER_GROUP, pb.OpCode_BGP_NEIGHBOR, pb.OpCode_BGP_EVPN_PEER:
				check("asn_zero", false, func(o *pb.Operation) { o.GetPeer().Asn = 0 })
				check("empty_name", false, func(o *pb.Operation) { o.GetPeer().Name = "" })
				check("name_injection", false, func(o *pb.Operation) { o.GetPeer().Name = "a\nend" })
				check("source_injection", false, func(o *pb.Operation) { o.GetPeer().UpdateSource = "a b" })
				check("group_injection", false, func(o *pb.Operation) { o.GetPeer().PeerGroup = "a;id" })
				check("vrf_injection", false, func(o *pb.Operation) { o.GetPeer().Vrf = "a\nb" })
				if c.code == pb.OpCode_BGP_NEIGHBOR {
					check("neighbor_ipv6", true, func(o *pb.Operation) { o.GetPeer().Name = "2001:db8::1" })
				}
			case pb.OpCode_BGP_EVPN_GLOBAL:
				check("asn_zero", false, func(o *pb.Operation) { o.GetEvpnGlobal().Asn = 0 })
			case pb.OpCode_EVPN_L2VNI, pb.OpCode_EVPN_TENANT:
				check("asn_zero", false, func(o *pb.Operation) { o.GetEvpn().Asn = 0 })
				check("invalid_rd", false, func(o *pb.Operation) { o.GetEvpn().Rd = "auto;id" })
				check("invalid_import_rt", false, func(o *pb.Operation) { o.GetEvpn().ImportRts = []string{"65000:1\nend"} })
				check("invalid_export_rt", false, func(o *pb.Operation) { o.GetEvpn().ExportRts = []string{"bad"} })
				if c.code == pb.OpCode_EVPN_L2VNI {
					for _, v := range []uint32{0, 1, 16777215, 16777216} {
						check(fmt.Sprintf("vni_%d", v), v > 0 && v < 16777216, func(o *pb.Operation) { o.GetEvpn().Vni = v })
					}
				} else {
					check("missing_vrf", false, func(o *pb.Operation) { o.GetEvpn().Vrf = "" })
					check("invalid_vrf", false, func(o *pb.Operation) { o.GetEvpn().Vrf = "a b" })
				}
			}
		})
	}
}

type frrRunner struct {
	t                     *testing.T
	config, before, after string
	writes, seen          [][]string
	reloadSeen            bool
	failAt                int
	cliOutput             string
	readError             error
	keepWrong             bool
}

var injectedFRRError = errors.New("injected FRR failure")

func vtysh(lines ...string) []string {
	args := []string{"vtysh"}
	for _, l := range lines {
		args = append(args, "-c", l)
	}
	return args
}
func (r *frrRunner) Run(_ context.Context, args ...string) (string, error) {
	if reflect.DeepEqual(args, []string{"ip", "-j", "-d", "link", "show"}) {
		return `[{"ifname":"blue","linkinfo":{"info_kind":"vrf"}},{"ifname":"br50000","master":"blue","linkinfo":{"info_kind":"bridge"}},{"ifname":"vx50000","master":"br50000","linkinfo":{"info_kind":"vxlan","info_data":{"id":50000}}}]`, nil
	}
	if reflect.DeepEqual(args, vtysh("show running-config")) {
		return r.config, r.readError
	}
	if reflect.DeepEqual(args, vtysh("show bgp l2vpn evpn vni 10010 json")) {
		return `{}`, nil
	}
	if len(args) == 4 && args[0] == "test-frr-reload" && args[1] == "--reload" && args[2] == "--overwrite" {
		b, err := os.ReadFile(args[3])
		if err != nil {
			r.t.Fatal(err)
		}
		if string(b) != r.before {
			r.t.Fatalf("reload did not receive original snapshot: %q", b)
		}
		r.reloadSeen = true
		if !r.keepWrong {
			r.config = r.before
		}
		return "", nil
	}
	i := len(r.seen)
	r.seen = append(r.seen, append([]string(nil), args...))
	if i >= len(r.writes) || !reflect.DeepEqual(args, r.writes[i]) {
		r.t.Errorf("unexpected write #%d %q, expected %q", i, args, r.writes)
		return "", errors.New("unexpected write")
	}
	if r.failAt == i+1 {
		return "", injectedFRRError
	}
	if r.cliOutput != "" {
		return r.cliOutput, nil
	}
	if len(r.seen) == len(r.writes) && !r.reloadSeen {
		r.config = r.after
	}
	return "", nil
}
func (r *frrRunner) complete() {
	r.t.Helper()
	if len(r.seen) != len(r.writes) {
		r.t.Fatalf("writes %d/%d", len(r.seen), len(r.writes))
	}
}
func frrFixture(c frrCase, a pb.Action) (string, string, [][]string) {
	before := emptyConfig
	after := "frr version 10\n" + c.configured + "end\n"
	lines := c.set
	if a == pb.Action_DELETE || a == pb.Action_UNBIND {
		before = after
		after = "frr version 10\n" + c.removed + "end\n"
		lines = c.remove
	}
	all := append([]string{"configure terminal"}, lines...)
	all = append(all, "end")
	return before, after, [][]string{vtysh(all...), vtysh("write memory")}
}
func TestFRROperationLifecycle(t *testing.T) {
	for _, c := range frrCases() {
		for _, a := range c.actions {
			t.Run(c.code.String()+"/"+a.String(), func(t *testing.T) {
				o := frrOp(t, c, a)
				before, after, writes := frrFixture(c, a)
				r := &frrRunner{t: t, config: before, before: before, after: after, writes: writes}
				d := &Driver{Runner: r, ReloadPath: "test-frr-reload"}
				ctx := context.Background()
				u, err := d.Prepare(ctx, o)
				if err != nil {
					t.Fatal(err)
				}
				if len(r.seen) != 0 {
					t.Fatal("Prepare changed configuration")
				}
				if err = d.Verify(ctx, o); err == nil {
					t.Fatal("pre-change configuration passed verification")
				}
				if err = d.Apply(ctx, o); err != nil {
					t.Fatal(err)
				}
				r.complete()
				if err = d.Verify(ctx, o); err != nil {
					t.Fatal(err)
				}
				b, err := json.Marshal(u)
				if err != nil {
					t.Fatal(err)
				}
				var saved model.Undo
				if err = json.Unmarshal(b, &saved); err != nil {
					t.Fatal(err)
				}
				r.seen = nil
				r.writes = nil
				if (c.code == pb.OpCode_BGP_NEIGHBOR || c.code == pb.OpCode_BGP_PEER_GROUP) && a != pb.Action_DELETE {
					r.writes = append(r.writes, vtysh("configure terminal", bgpRoot, "no neighbor "+o.GetPeer().Name, "end"))
				}
				r.writes = append(r.writes, vtysh("write memory"))
				if err = d.Rollback(ctx, o, &saved); err != nil {
					t.Fatal(err)
				}
				r.complete()
				if !r.reloadSeen {
					t.Fatal("snapshot was not reloaded")
				}
				r.seen = nil
				r.writes = nil
				r.reloadSeen = false
				if err = d.Rollback(ctx, o, &saved); err != nil {
					t.Fatal(err)
				}
				r.complete()
				if r.reloadSeen {
					t.Fatal("idempotent rollback reloaded again")
				}
			})
		}
	}
}

func TestFRRExecutionFailures(t *testing.T) {
	for _, c := range frrCases() {
		for _, a := range c.actions {
			t.Run(c.code.String()+"/"+a.String(), func(t *testing.T) {
				o := frrOp(t, c, a)
				before, after, writes := frrFixture(c, a)
				r := &frrRunner{t: t, config: before, before: before, after: after, writes: writes, readError: injectedFRRError}
				d := &Driver{Runner: r}
				ctx := context.Background()
				if _, err := d.Prepare(ctx, o); !errors.Is(err, injectedFRRError) {
					t.Fatalf("Prepare error: %v", err)
				}
				if err := d.Verify(ctx, o); !errors.Is(err, injectedFRRError) {
					t.Fatalf("Verify error: %v", err)
				}
				r.readError = nil
				for _, step := range []int{1, 2} {
					r.seen = nil
					r.failAt = step
					if err := d.Apply(ctx, o); !errors.Is(err, injectedFRRError) {
						t.Fatalf("lost command #%d failure: %v", step, err)
					}
					if len(r.seen) != step {
						t.Fatal("continued after failure")
					}
				}
			})
		}
	}
	for _, output := range []string{"% Unknown command", "ERROR: permission denied", "This command is only supported with zebra"} {
		t.Run(output, func(t *testing.T) {
			c := frrCases()[0]
			_, _, writes := frrFixture(c, pb.Action_CREATE)
			r := &frrRunner{t: t, writes: writes, cliOutput: output}
			d := &Driver{Runner: r}
			if d.Apply(context.Background(), frrOp(t, c, pb.Action_CREATE)) == nil {
				t.Fatal("CLI error output accepted")
			}
			if len(r.seen) != 1 {
				t.Fatal("saved rejected config")
			}
		})
	}
}

func TestFRRRollbackRejectsMissingOrIncorrectSnapshot(t *testing.T) {
	c := frrCases()[0]
	o := frrOp(t, c, pb.Action_CREATE)
	ctx := context.Background()
	d := &Driver{}
	for _, u := range []*model.Undo{nil, {Files: map[string]string{}}} {
		if d.Rollback(ctx, o, u) == nil {
			t.Fatal("missing snapshot accepted")
		}
	}
	r := &frrRunner{t: t, config: "frr version 10\nvrf wrong\nexit-vrf\nend\n", before: emptyConfig, writes: [][]string{vtysh("write memory")}, keepWrong: true}
	d = &Driver{Runner: r, ReloadPath: "test-frr-reload"}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := d.Rollback(cancelled, o, &model.Undo{Files: map[string]string{"frr.conf": emptyConfig}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong readback was accepted: %v", err)
	}
}

func TestFRRBooleanDisableAndScope(t *testing.T) {
	for _, c := range frrCases() {
		if c.code != pb.OpCode_OSPF_INTERFACE && c.code != pb.OpCode_BGP_EVPN_PEER && c.code != pb.OpCode_BGP_EVPN_GLOBAL {
			continue
		}
		t.Run(c.code.String(), func(t *testing.T) {
			o := frrOp(t, c, pb.Action_SET)
			switch c.code {
			case pb.OpCode_OSPF_INTERFACE:
				o.GetOspfInterface().Passive = proto.Bool(false)
			case pb.OpCode_BGP_EVPN_PEER:
				o.GetPeer().Active = proto.Bool(false)
				o.GetPeer().RrClient = proto.Bool(false)
			case pb.OpCode_BGP_EVPN_GLOBAL:
				o.GetEvpnGlobal().AdvertiseAllVni = proto.Bool(false)
				o.GetEvpnGlobal().AdvertiseSviIp = proto.Bool(false)
			}
			cfg := "frr version 10\n" + c.removed + "end\n"
			if c.code == pb.OpCode_OSPF_INTERFACE {
				cfg = "frr version 10\ninterface eth0\n ip ospf network point-to-point\nexit\nend\n"
			}
			d := &Driver{Runner: runner{text: cfg}}
			if err := d.Verify(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			d.Runner = runner{text: "frr version 10\n" + c.configured + "end\n"}
			if d.Verify(context.Background(), o) == nil {
				t.Fatal("enabled option accepted as disabled")
			}
		})
	}
}
