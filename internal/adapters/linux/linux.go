// Package linux implements the typed operations using iproute2, without a shell.
package linux

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
)

type Driver struct{ Runner backend.Runner }

func (d *Driver) Name() string { return "linux" }
func (d *Driver) Codes() []pb.OpCode {
	return []pb.OpCode{pb.OpCode_NODE_NETWORK, pb.OpCode_INTERFACE, pb.OpCode_ADDRESS, pb.OpCode_TENANT, pb.OpCode_BD, pb.OpCode_BD_PORT, pb.OpCode_BD_TENANT, pb.OpCode_VXLAN, pb.OpCode_STATIC_ROUTE}
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$`)

// iproute2 omits /32 and /128 on host routes and spells /0 as default.
func routePrefix(s string) string {
	if p, err := netip.ParsePrefix(s); err == nil {
		if p.Bits() == 0 {
			return "default"
		}
		return p.Masked().String()
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(ip, ip.BitLen()).String()
	}
	return s
}

func name(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("invalid interface name %q", s)
	}
	return nil
}
func prefix(s string) error { _, e := netip.ParsePrefix(s); return e }
func ip(s string) error     { _, e := netip.ParseAddr(s); return e }
func mac(s string) error {
	v, e := net.ParseMAC(s)
	if e != nil || len(v) != 6 {
		return fmt.Errorf("invalid Ethernet MAC %q", s)
	}
	return nil
}
func validVNI(v uint32) bool { return v > 0 && v <= 16777215 }
func (d *Driver) Validate(o *pb.Operation) error {
	if o == nil {
		return fmt.Errorf("nil operation")
	}
	a := o.Action
	allowed := func(as ...pb.Action) error {
		for _, x := range as {
			if a == x {
				return nil
			}
		}
		return fmt.Errorf("unsupported action %s for %s", a, o.Code)
	}
	switch o.Code {
	case pb.OpCode_NODE_NETWORK:
		if o.GetNetwork() == nil {
			return fmt.Errorf("network data required")
		}
		if e := allowed(pb.Action_SET); e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, s := range o.GetNetwork().Settings {
			if s == nil {
				return fmt.Errorf("nil sysctl")
			}
			if seen[s.Key] {
				return fmt.Errorf("duplicate sysctl")
			}
			seen[s.Key] = true
			if !allowedSysctl(s.Key) {
				return fmt.Errorf("sysctl %s not allowed", s.Key)
			}
			if s.Value != "0" && s.Value != "1" && !(strings.HasSuffix(s.Key, "rp_filter") && s.Value == "2") {
				return fmt.Errorf("invalid sysctl value")
			}
		}
	case pb.OpCode_INTERFACE:
		x := o.GetInterface()
		if x == nil {
			return fmt.Errorf("interface data required")
		}
		if e := allowed(pb.Action_SET); e != nil {
			return e
		}
		if e := name(x.Name); e != nil {
			return e
		}
		if x.Mtu != nil && (*x.Mtu < 68 || *x.Mtu > 65535) {
			return fmt.Errorf("invalid MTU")
		}
		if x.Mac != nil {
			if e := mac(*x.Mac); e != nil {
				return e
			}
		}
		if x.Addrgenmode != nil && *x.Addrgenmode != "none" && *x.Addrgenmode != "eui64" {
			return fmt.Errorf("unsupported address generation mode")
		}
	case pb.OpCode_ADDRESS:
		x := o.GetAddress()
		if x == nil {
			return fmt.Errorf("address data required")
		}
		if e := allowed(pb.Action_CREATE, pb.Action_DELETE); e != nil {
			return e
		}
		if e := name(x.Interface); e != nil {
			return e
		}
		return prefix(x.Prefix)
	case pb.OpCode_TENANT:
		x := o.GetTenant()
		if x == nil {
			return fmt.Errorf("tenant data required")
		}
		if e := allowed(pb.Action_CREATE, pb.Action_DELETE); e != nil {
			return e
		}
		if e := name(x.Name); e != nil {
			return e
		}
		if x.TableId == 0 || x.TableId >= 253 && x.TableId <= 255 {
			return fmt.Errorf("invalid/reserved VRF table")
		}
	case pb.OpCode_BD:
		x := o.GetBridge()
		if x == nil {
			return fmt.Errorf("bridge data required")
		}
		if e := allowed(pb.Action_CREATE, pb.Action_SET, pb.Action_DELETE); e != nil {
			return e
		}
		if e := name(x.Name); e != nil {
			return e
		}
		if x.Mtu != nil && (*x.Mtu < 68 || *x.Mtu > 65535) {
			return fmt.Errorf("invalid MTU")
		}
		if x.Mac != nil {
			return mac(*x.Mac)
		}
	case pb.OpCode_BD_PORT:
		x := o.GetBridgePort()
		if x == nil {
			return fmt.Errorf("bridge_port required")
		}
		if e := allowed(pb.Action_BIND, pb.Action_UNBIND, pb.Action_SET); e != nil {
			return e
		}
		if e := name(x.Bridge); e != nil {
			return e
		}
		return name(x.Port)
	case pb.OpCode_BD_TENANT:
		x := o.GetBinding()
		if x == nil {
			return fmt.Errorf("binding required")
		}
		if e := allowed(pb.Action_BIND, pb.Action_UNBIND); e != nil {
			return e
		}
		if e := name(x.Tenant); e != nil {
			return e
		}
		return name(x.Bridge)
	case pb.OpCode_VXLAN:
		x := o.GetVxlan()
		if x == nil {
			return fmt.Errorf("vxlan required")
		}
		if e := allowed(pb.Action_CREATE, pb.Action_DELETE); e != nil {
			return e
		}
		if e := name(x.Name); e != nil {
			return e
		}
		if !validVNI(x.Vni) || x.Port > 65535 {
			return fmt.Errorf("invalid VXLAN VNI or port")
		}
		if x.Underlay != "" {
			if e := name(x.Underlay); e != nil {
				return e
			}
		}
		return ip(x.Local)
	case pb.OpCode_STATIC_ROUTE:
		x := o.GetRoute()
		if x == nil {
			return fmt.Errorf("route required")
		}
		if e := allowed(pb.Action_CREATE, pb.Action_DELETE); e != nil {
			return e
		}
		if x.Prefix != "default" {
			if e := prefix(x.Prefix); e != nil {
				return e
			}
		}
		if e := ip(x.Gateway); e != nil {
			return e
		}
		if x.Prefix != "default" {
			p, _ := netip.ParsePrefix(x.Prefix)
			g, _ := netip.ParseAddr(x.Gateway)
			if p.Addr().Is4() != g.Is4() {
				return fmt.Errorf("route prefix/gateway family mismatch")
			}
		}
		return name(x.Interface)
	default:
		return fmt.Errorf("unsupported op")
	}
	return nil
}
func allowedSysctl(k string) bool {
	switch k {
	case "net.ipv4.ip_forward", "net.ipv6.conf.all.forwarding", "net.ipv6.conf.default.forwarding", "net.ipv4.conf.all.rp_filter", "net.ipv4.conf.default.rp_filter", "net.ipv6.conf.all.disable_ipv6", "net.ipv6.conf.default.disable_ipv6", "net.bridge.bridge-nf-call-iptables", "net.bridge.bridge-nf-call-ip6tables", "net.bridge.bridge-nf-call-arptables":
		return true
	}
	return false
}

type link struct {
	Name   string   `json:"ifname"`
	Flags  []string `json:"flags"`
	MTU    int      `json:"mtu"`
	MAC    string   `json:"address"`
	Master string   `json:"master"`
	Mode   string   `json:"inet6_addr_gen_mode"`
	Info   struct {
		Kind  string         `json:"info_kind"`
		Data  map[string]any `json:"info_data"`
		Slave map[string]any `json:"info_slave_data"`
	} `json:"linkinfo"`
}

func (d *Driver) links(ctx context.Context) ([]link, error) {
	s, e := d.Runner.Run(ctx, "ip", "-j", "-d", "link", "show")
	if e != nil {
		return nil, e
	}
	var a []link
	e = json.Unmarshal([]byte(s), &a)
	return a, e
}
func find(ls []link, n string) *link {
	for i := range ls {
		if ls[i].Name == n {
			return &ls[i]
		}
	}
	return nil
}
func up(l *link) bool {
	for _, f := range l.Flags {
		if f == "UP" {
			return true
		}
	}
	return false
}
func num(n any) string { return fmt.Sprint(n) }
func on(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
func state(b bool) string {
	if b {
		return "up"
	}
	return "down"
}
func flag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
func linkName(o *pb.Operation) string {
	switch o.Code {
	case pb.OpCode_INTERFACE:
		return o.GetInterface().Name
	case pb.OpCode_ADDRESS:
		return o.GetAddress().Interface
	case pb.OpCode_TENANT:
		return o.GetTenant().Name
	case pb.OpCode_BD:
		return o.GetBridge().Name
	case pb.OpCode_BD_PORT:
		return o.GetBridgePort().Port
	case pb.OpCode_BD_TENANT:
		return o.GetBinding().Bridge
	case pb.OpCode_VXLAN:
		return o.GetVxlan().Name
	}
	return ""
}

type view map[string]string

func (d *Driver) view(ctx context.Context, o *pb.Operation) (view, error) {
	v := view{}
	if o.Code == pb.OpCode_NODE_NETWORK {
		for _, s := range o.GetNetwork().Settings {
			out, e := d.Runner.Run(ctx, "sysctl", "-n", s.Key)
			if e != nil {
				if s.Optional {
					v[s.Key] = "<absent>"
					continue
				}
				return nil, e
			}
			v[s.Key] = strings.TrimSpace(out)
		}
		return v, nil
	}
	if o.Code == pb.OpCode_STATIC_ROUTE {
		x := o.GetRoute()
		table := x.Table
		if table == 0 {
			table = 254
		}
		cmd := []string{"ip", "-j"}
		g, _ := netip.ParseAddr(x.Gateway)
		if g.Is6() {
			cmd = append(cmd, "-6")
		}
		cmd = append(cmd, "route", "show", "table", num(table))
		out, e := d.Runner.Run(ctx, cmd...)
		if e != nil {
			return nil, e
		}
		var rs []map[string]any
		if e = json.Unmarshal([]byte(out), &rs); e != nil {
			return nil, e
		}
		v["exists"] = "false"
		for _, r := range rs {
			p := routePrefix(x.Prefix)
			dst, _ := r["dst"].(string)
			if routePrefix(dst) == p {
				if r["gateway"] != x.Gateway || r["dev"] != x.Interface {
					return nil, fmt.Errorf("conflicting route for %s", p)
				}
				v["exists"] = "true"
			}
		}
		return v, nil
	}
	ls, e := d.links(ctx)
	if e != nil {
		return nil, e
	}
	l := find(ls, linkName(o))
	v["exists"] = "false"
	if l == nil {
		return v, nil
	}
	v["exists"] = "true"
	switch o.Code {
	case pb.OpCode_ADDRESS:
		x := o.GetAddress()
		out, e := d.Runner.Run(ctx, "ip", "-j", "address", "show", "dev", x.Interface)
		if e != nil {
			return nil, e
		}
		var as []struct {
			Addresses []struct {
				Local string `json:"local"`
				Len   int    `json:"prefixlen"`
			} `json:"addr_info"`
		}
		if e = json.Unmarshal([]byte(out), &as); e != nil {
			return nil, e
		}
		v["exists"] = "false"
		p, _ := netip.ParsePrefix(x.Prefix)
		for _, a := range as {
			for _, b := range a.Addresses {
				if b.Local == p.Addr().String() && b.Len == p.Bits() {
					v["exists"] = "true"
				}
			}
		}
	case pb.OpCode_INTERFACE:
		x := o.GetInterface()
		if x.Mtu != nil {
			v["mtu"] = num(l.MTU)
		}
		if x.Mac != nil {
			v["mac"] = l.MAC
		}
		if x.Up != nil {
			v["up"] = strconv.FormatBool(up(l))
		}
		if x.Addrgenmode != nil {
			v["mode"] = l.Mode
		}
	case pb.OpCode_BD_PORT:
		v["master"] = l.Master
		if l.Info.Slave != nil && find(ls, l.Master) != nil && find(ls, l.Master).Info.Kind == "bridge" {
			v["learning"] = num(l.Info.Slave["learning"])
			v["neigh_suppress"] = num(l.Info.Slave["neigh_suppress"])
		}
	case pb.OpCode_BD_TENANT:
		v["master"] = l.Master
	default:
		v["kind"] = l.Info.Kind
		v["mtu"] = num(l.MTU)
		v["mac"] = l.MAC
		v["up"] = strconv.FormatBool(up(l))
		v["master"] = l.Master
		v["mode"] = l.Mode
		switch o.Code {
		case pb.OpCode_TENANT:
			v["table"] = num(l.Info.Data["table"])
		case pb.OpCode_BD:
			v["stp"] = num(l.Info.Data["stp_state"])
			v["ageing"] = num(l.Info.Data["ageing_time"])
		case pb.OpCode_VXLAN:
			for _, k := range []string{"id", "local", "port", "learning"} {
				v[k] = num(l.Info.Data[k])
			}
			if local6, ok := l.Info.Data["local6"]; ok {
				v["local"] = num(local6)
			}
			if address, e := netip.ParseAddr(v["local"]); e == nil {
				v["local"] = address.String()
			}
		}
	}
	return v, nil
}
func setLink(n string, args ...string) []string {
	return append([]string{"ip", "link", "set", "dev", n}, args...)
}
func (d *Driver) commands(ctx context.Context, o *pb.Operation, v view) (apply, undo [][]string, err error) {
	add := func(a []string) { apply = append(apply, a) }
	back := func(a []string) { undo = append(undo, a) }
	exists := v["exists"] == "true"
	n := linkName(o)
	if o.Code == pb.OpCode_NODE_NETWORK {
		for _, s := range o.GetNetwork().Settings {
			if v[s.Key] == "<absent>" {
				continue
			}
			add([]string{"sysctl", "-w", s.Key + "=" + s.Value})
			back([]string{"sysctl", "-w", s.Key + "=" + v[s.Key]})
		}
		return
	}
	if o.Code == pb.OpCode_ADDRESS {
		x := o.GetAddress()
		verb, inv := "add", "del"
		if o.Action == pb.Action_DELETE {
			verb, inv = "del", "add"
		}
		if exists == (o.Action == pb.Action_CREATE) {
			err = fmt.Errorf("address existence conflict")
			return
		}
		add([]string{"ip", "address", verb, x.Prefix, "dev", x.Interface})
		back([]string{"ip", "address", inv, x.Prefix, "dev", x.Interface})
		return
	}
	if o.Code == pb.OpCode_STATIC_ROUTE {
		x := o.GetRoute()
		verb, inv := "add", "del"
		if o.Action == pb.Action_DELETE {
			verb, inv = "del", "add"
		}
		if exists == (o.Action == pb.Action_CREATE) {
			err = fmt.Errorf("route existence conflict")
			return
		}
		table := x.Table
		if table == 0 {
			table = 254
		}
		tail := []string{x.Prefix, "via", x.Gateway, "dev", x.Interface, "table", num(table), "proto", "static"}
		cmd := []string{"ip"}
		g, _ := netip.ParseAddr(x.Gateway)
		if g.Is6() {
			cmd = append(cmd, "-6")
		}
		add(append(append(append([]string{}, cmd...), "route", verb), tail...))
		back(append(append(append([]string{}, cmd...), "route", inv), tail...))
		return
	}
	create := o.Code == pb.OpCode_TENANT || o.Code == pb.OpCode_BD || o.Code == pb.OpCode_VXLAN
	if create && o.Action == pb.Action_CREATE {
		if exists {
			err = fmt.Errorf("interface %s already exists", n)
			return
		}
		var a []string
		switch o.Code {
		case pb.OpCode_TENANT:
			a = []string{"ip", "link", "add", n, "type", "vrf", "table", num(o.GetTenant().TableId)}
		case pb.OpCode_BD:
			a = []string{"ip", "link", "add", n, "type", "bridge"}
			if o.GetBridge().Stp != nil {
				a = append(a, "stp_state", flag(*o.GetBridge().Stp))
			}
		case pb.OpCode_VXLAN:
			x := o.GetVxlan()
			port := x.Port
			if port == 0 {
				port = 4789
			}
			a = []string{"ip", "link", "add", n, "type", "vxlan", "id", num(x.Vni), "local", x.Local, "dstport", num(port)}
			if x.Learning {
				a = append(a, "learning")
			} else {
				a = append(a, "nolearning")
			}
			if x.Underlay != "" {
				a = append(a, "dev", x.Underlay)
			}
		}
		add(a)
		back([]string{"ip", "link", "del", "dev", n})
		if o.Code == pb.OpCode_BD {
			x := o.GetBridge()
			if x.Mtu != nil {
				add(setLink(n, "mtu", num(*x.Mtu)))
			}
			if x.Mac != nil {
				add(setLink(n, "address", *x.Mac))
			}
			if x.AgeingTime != nil {
				add(setLink(n, "type", "bridge", "ageing_time", num(*x.AgeingTime)))
			}
		}
		return
	}
	if !exists {
		err = fmt.Errorf("interface %s absent", n)
		return
	}
	if create {
		kind := map[pb.OpCode]string{pb.OpCode_TENANT: "vrf", pb.OpCode_BD: "bridge", pb.OpCode_VXLAN: "vxlan"}[o.Code]
		if v["kind"] != kind {
			err = fmt.Errorf("%s is not %s", n, kind)
			return
		}
		if o.Action == pb.Action_DELETE {
			if v["master"] != "" {
				err = fmt.Errorf("detach %s from its master before deletion", n)
				return
			}
			ls, e := d.links(ctx)
			if e != nil {
				err = e
				return
			}
			for _, l := range ls {
				if l.Master == n {
					err = fmt.Errorf("detach member %s before deleting %s", l.Name, n)
					return
				}
			}
			out, e := d.Runner.Run(ctx, "ip", "-j", "address", "show", "dev", n)
			if e != nil {
				err = e
				return
			}
			var as []struct {
				Addresses []struct {
					Scope string `json:"scope"`
				} `json:"addr_info"`
			}
			if e = json.Unmarshal([]byte(out), &as); e != nil {
				err = e
				return
			}
			for _, a := range as {
				for _, ip := range a.Addresses {
					if ip.Scope != "link" {
						err = fmt.Errorf("remove addresses before deleting %s", n)
						return
					}
				}
			}
			add([]string{"ip", "link", "del", "dev", n})
			a := []string{"ip", "link", "add", n, "type", kind}
			switch kind {
			case "vrf":
				a = append(a, "table", v["table"])
			case "bridge":
				a = append(a, "stp_state", v["stp"], "ageing_time", v["ageing"])
			case "vxlan":
				a = append(a, "id", v["id"], "local", v["local"], "dstport", v["port"])
				if v["learning"] == "true" {
					a = append(a, "learning")
				} else {
					a = append(a, "nolearning")
				}
				if o.GetVxlan().Underlay != "" {
					a = append(a, "dev", o.GetVxlan().Underlay)
				}
			}
			back(a)
			back(setLink(n, "mtu", v["mtu"]))
			if v["mac"] != "00:00:00:00:00:00" {
				back(setLink(n, "address", v["mac"]))
			}
			if v["mode"] != "" {
				back(setLink(n, "addrgenmode", v["mode"]))
			}
			back(setLink(n, state(v["up"] == "true")))
			return
		}
	}
	switch o.Code {
	case pb.OpCode_INTERFACE:
		x := o.GetInterface()
		if x.Mtu != nil {
			add(setLink(n, "mtu", num(*x.Mtu)))
			back(setLink(n, "mtu", v["mtu"]))
		}
		if x.Mac != nil {
			add(setLink(n, "address", *x.Mac))
			back(setLink(n, "address", v["mac"]))
		}
		if x.Addrgenmode != nil {
			add(setLink(n, "addrgenmode", *x.Addrgenmode))
			if v["mode"] != "" {
				back(setLink(n, "addrgenmode", v["mode"]))
			}
		}
		if x.Up != nil {
			add(setLink(n, state(*x.Up)))
			back(setLink(n, state(v["up"] == "true")))
		}
	case pb.OpCode_BD:
		x := o.GetBridge()
		if x.Stp != nil {
			add(setLink(n, "type", "bridge", "stp_state", flag(*x.Stp)))
			back(setLink(n, "type", "bridge", "stp_state", v["stp"]))
		}
		if x.AgeingTime != nil {
			add(setLink(n, "type", "bridge", "ageing_time", num(*x.AgeingTime)))
			back(setLink(n, "type", "bridge", "ageing_time", v["ageing"]))
		}
		if x.Mtu != nil {
			add(setLink(n, "mtu", num(*x.Mtu)))
			back(setLink(n, "mtu", v["mtu"]))
		}
		if x.Mac != nil {
			add(setLink(n, "address", *x.Mac))
			back(setLink(n, "address", v["mac"]))
		}
	case pb.OpCode_BD_PORT, pb.OpCode_BD_TENANT:
		target, kind := "", "bridge"
		if o.Code == pb.OpCode_BD_PORT {
			target = o.GetBridgePort().Bridge
		} else {
			target = o.GetBinding().Tenant
			kind = "vrf"
		}
		ls, e := d.links(ctx)
		if e != nil {
			err = e
			return
		}
		l := find(ls, target)
		if l == nil || l.Info.Kind != kind {
			err = fmt.Errorf("master %s must be %s", target, kind)
			return
		}
		if o.Code == pb.OpCode_BD_TENANT && find(ls, n).Info.Kind != "bridge" {
			err = fmt.Errorf("tenant member must be a bridge")
			return
		}
		if o.Action == pb.Action_UNBIND {
			if v["master"] != target {
				err = fmt.Errorf("master mismatch")
				return
			}
			add(setLink(n, "nomaster"))
		} else {
			if v["master"] != "" && v["master"] != target {
				err = fmt.Errorf("port already belongs to another master")
				return
			}
			add(setLink(n, "master", target))
			if o.Code == pb.OpCode_BD_PORT {
				x := o.GetBridgePort()
				if x.Learning != nil {
					add(setLink(n, "type", "bridge_slave", "learning", on(*x.Learning)))
				}
				if x.NeighSuppress != nil {
					add(setLink(n, "type", "bridge_slave", "neigh_suppress", on(*x.NeighSuppress)))
				}
			}
		}
		if v["master"] == "" {
			back(setLink(n, "nomaster"))
		} else {
			back(setLink(n, "master", v["master"]))
			for _, k := range []string{"learning", "neigh_suppress"} {
				if val, ok := v[k]; ok {
					back(setLink(n, "type", "bridge_slave", k, on(val == "true")))
				}
			}
		}
	default:
		err = fmt.Errorf("unsupported operation/action")
	}
	return
}
func (d *Driver) Prepare(ctx context.Context, o *pb.Operation) (*model.Undo, error) {
	v, e := d.view(ctx, o)
	if e != nil {
		return nil, e
	}
	_, undo, e := d.commands(ctx, o, v)
	if e != nil {
		return nil, e
	}
	b, _ := json.Marshal(v)
	return &model.Undo{Commands: undo, Data: b}, nil
}
func (d *Driver) Apply(ctx context.Context, o *pb.Operation) error {
	v, e := d.view(ctx, o)
	if e != nil {
		return e
	}
	cs, _, e := d.commands(ctx, o, v)
	if e != nil {
		return e
	}
	for _, c := range cs {
		if _, e = d.Runner.Run(ctx, c...); e != nil {
			return e
		}
	}
	return nil
}
func (d *Driver) Verify(ctx context.Context, o *pb.Operation) error {
	v, e := d.view(ctx, o)
	if e != nil {
		return e
	}
	want := view{}
	switch o.Code {
	case pb.OpCode_NODE_NETWORK:
		for _, s := range o.GetNetwork().Settings {
			if s.Optional && v[s.Key] == "<absent>" {
				continue
			}
			want[s.Key] = s.Value
		}
	case pb.OpCode_ADDRESS, pb.OpCode_STATIC_ROUTE:
		want["exists"] = strconv.FormatBool(o.Action != pb.Action_DELETE)
	case pb.OpCode_INTERFACE:
		x := o.GetInterface()
		want["exists"] = "true"
		if x.Mtu != nil {
			want["mtu"] = num(*x.Mtu)
		}
		if x.Mac != nil {
			m, _ := net.ParseMAC(*x.Mac)
			want["mac"] = m.String()
		}
		if x.Up != nil {
			want["up"] = strconv.FormatBool(*x.Up)
		}
		if x.Addrgenmode != nil {
			want["mode"] = *x.Addrgenmode
		}
	case pb.OpCode_TENANT:
		want["exists"] = strconv.FormatBool(o.Action != pb.Action_DELETE)
		if o.Action != pb.Action_DELETE {
			want["kind"] = "vrf"
			want["table"] = num(o.GetTenant().TableId)
		}
	case pb.OpCode_BD:
		want["exists"] = strconv.FormatBool(o.Action != pb.Action_DELETE)
		if o.Action != pb.Action_DELETE {
			x := o.GetBridge()
			want["kind"] = "bridge"
			if x.Stp != nil {
				want["stp"] = flag(*x.Stp)
			}
			if x.Mtu != nil {
				want["mtu"] = num(*x.Mtu)
			}
			if x.Mac != nil {
				want["mac"] = strings.ToLower(*x.Mac)
			}
			if x.AgeingTime != nil {
				want["ageing"] = num(*x.AgeingTime)
			}
		}
	case pb.OpCode_VXLAN:
		want["exists"] = strconv.FormatBool(o.Action != pb.Action_DELETE)
		if o.Action != pb.Action_DELETE {
			x := o.GetVxlan()
			want["kind"] = "vxlan"
			want["id"] = num(x.Vni)
			want["local"] = x.Local
			if address, e := netip.ParseAddr(x.Local); e == nil {
				want["local"] = address.String()
			}
			port := x.Port
			if port == 0 {
				port = 4789
			}
			want["port"] = num(port)
			want["learning"] = strconv.FormatBool(x.Learning)
		}
	case pb.OpCode_BD_PORT:
		want["exists"] = "true"
		x := o.GetBridgePort()
		if o.Action == pb.Action_UNBIND {
			want["master"] = ""
		} else {
			want["master"] = x.Bridge
			if x.Learning != nil {
				want["learning"] = strconv.FormatBool(*x.Learning)
			}
			if x.NeighSuppress != nil {
				want["neigh_suppress"] = strconv.FormatBool(*x.NeighSuppress)
			}
		}
	case pb.OpCode_BD_TENANT:
		want["exists"] = "true"
		if o.Action == pb.Action_UNBIND {
			want["master"] = ""
		} else {
			want["master"] = o.GetBinding().Tenant
		}
	}
	for k, w := range want {
		if v[k] != w {
			return fmt.Errorf("verification %s: %s=%q, expected %q", o.Id, k, v[k], w)
		}
	}
	return nil
}
func (d *Driver) Rollback(ctx context.Context, o *pb.Operation, u *model.Undo) error {
	if u == nil {
		return fmt.Errorf("missing undo")
	}
	var before view
	if e := json.Unmarshal(u.Data, &before); e != nil {
		return e
	}
	// Empty automatic-MAC bridges can report zero after their last port leaves.
	// Linux rejects an explicit zero MAC; restored ports select the MAC again.
	equal := func(now view) bool {
		if o.Code == pb.OpCode_BD && before["mac"] == "00:00:00:00:00:00" && now["kind"] == "bridge" {
			now["mac"] = before["mac"]
		}
		return reflect.DeepEqual(before, now)
	}
	current, e := d.view(ctx, o)
	if e == nil && equal(current) {
		return nil
	}
	for _, c := range u.Commands {
		// Recovery can resume after an inverse create/delete already succeeded.
		if len(c) >= 5 && c[0] == "ip" && c[1] == "link" && (c[2] == "add" || c[2] == "del") {
			ls, err := d.links(ctx)
			if err != nil {
				return err
			}
			n := c[3]
			if n == "dev" {
				n = c[4]
			}
			l := find(ls, n)
			if c[2] == "del" && l == nil {
				continue
			}
			if c[2] == "add" && l != nil {
				if l.Info.Kind != before["kind"] {
					return fmt.Errorf("rollback object kind conflict for %s", n)
				}
				continue
			}
		}
		if _, e = d.Runner.Run(ctx, c...); e != nil {
			now, readErr := d.view(ctx, o)
			if readErr == nil && equal(now) {
				return nil
			}
			return e
		}
	}
	now, e := d.view(ctx, o)
	if e != nil {
		return e
	}
	if !equal(now) {
		return fmt.Errorf("rollback readback mismatch: before=%v after=%v", before, now)
	}
	return nil
}
func (d *Driver) Observe(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, cmd := range map[string][]string{"links": {"ip", "-j", "-d", "link", "show"}, "addresses": {"ip", "-j", "address", "show"}, "routes": {"ip", "-j", "route", "show", "table", "all"}, "fdb": {"bridge", "-j", "fdb", "show"}, "neighbors": {"ip", "-j", "neigh", "show"}} {
		s, e := d.Runner.Run(ctx, cmd...)
		if e != nil {
			return out, e
		}
		out[k] = s
	}
	return out, nil
}
