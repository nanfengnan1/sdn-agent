package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
)

// These fixtures are a public operation/action contract, independent of commands().
type linuxCase struct {
	code    pb.OpCode
	actions []pb.Action
	data    string
}

func linuxCases() []linuxCase {
	return []linuxCase{
		{pb.OpCode_NODE_NETWORK, []pb.Action{pb.Action_SET}, `"network":{"settings":[{"key":"net.ipv4.ip_forward","value":"1"}]}`},
		{pb.OpCode_INTERFACE, []pb.Action{pb.Action_SET}, `"interface":{"name":"eth0","mtu":9000,"mac":"02:00:00:00:00:02","up":true,"addrgenmode":"none"}`},
		{pb.OpCode_ADDRESS, []pb.Action{pb.Action_CREATE, pb.Action_DELETE}, `"address":{"interface":"eth0","prefix":"192.0.2.2/24"}`},
		{pb.OpCode_TENANT, []pb.Action{pb.Action_CREATE, pb.Action_DELETE}, `"tenant":{"name":"blue","table_id":1001}`},
		{pb.OpCode_BD, []pb.Action{pb.Action_CREATE, pb.Action_SET, pb.Action_DELETE}, `"bridge":{"name":"br0","stp":true,"mtu":9000,"mac":"02:00:00:00:00:02","ageing_time":60000}`},
		{pb.OpCode_BD_PORT, []pb.Action{pb.Action_BIND, pb.Action_UNBIND, pb.Action_SET}, `"bridge_port":{"bridge":"br0","port":"eth0","learning":false,"neigh_suppress":true}`},
		{pb.OpCode_BD_TENANT, []pb.Action{pb.Action_BIND, pb.Action_UNBIND}, `"binding":{"bridge":"br0","tenant":"blue"}`},
		{pb.OpCode_VXLAN, []pb.Action{pb.Action_CREATE, pb.Action_DELETE}, `"vxlan":{"name":"vx0","vni":10010,"local":"192.0.2.1","underlay":"eth0"}`},
		{pb.OpCode_STATIC_ROUTE, []pb.Action{pb.Action_CREATE, pb.Action_DELETE}, `"route":{"prefix":"198.51.100.0/24","gateway":"192.0.2.1","interface":"eth0"}`},
	}
}

func linuxOp(t *testing.T, c linuxCase, action pb.Action) *pb.Operation {
	t.Helper()
	o := new(pb.Operation)
	if err := protojson.Unmarshal([]byte(fmt.Sprintf(`{"id":"test","code":"%s","action":"%s",%s}`, c.code, action, c.data)), o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestLinuxOperationValidation(t *testing.T) {
	d := new(Driver)
	covered := map[pb.OpCode]bool{}
	for _, c := range linuxCases() {
		covered[c.code] = true
		t.Run(c.code.String(), func(t *testing.T) {
			for n, name := range pb.Action_name {
				t.Run(name, func(t *testing.T) {
					allowed := false
					for _, a := range c.actions {
						allowed = allowed || a == pb.Action(n)
					}
					if err := d.Validate(linuxOp(t, c, pb.Action(n))); (err == nil) != allowed {
						t.Fatalf("action accepted=%t want=%t: %v", err == nil, allowed, err)
					}
				})
			}
			for _, kind := range []string{"missing_payload", "wrong_payload", "unknown_action"} {
				t.Run(kind, func(t *testing.T) {
					o := linuxOp(t, c, c.actions[0])
					switch kind {
					case "missing_payload":
						o.Data = nil
					case "wrong_payload":
						o.Data = &pb.Operation_Bgp{Bgp: &pb.Bgp{Asn: 65000}}
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
			t.Errorf("missing operation test: %s", c)
		}
	}
	for _, o := range []*pb.Operation{nil, {Code: pb.OpCode(999), Action: pb.Action_SET}} {
		if d.Validate(o) == nil {
			t.Error("unknown/nil operation accepted")
		}
	}
}

func TestLinuxParameterBoundaries(t *testing.T) {
	for _, c := range linuxCases() {
		t.Run(c.code.String(), func(t *testing.T) {
			base := linuxOp(t, c, c.actions[0])
			check := func(label string, valid bool, change func(*pb.Operation)) {
				t.Run(label, func(t *testing.T) {
					o := proto.Clone(base).(*pb.Operation)
					change(o)
					err := new(Driver).Validate(o)
					if (err == nil) != valid {
						t.Fatalf("valid=%t: %v", valid, err)
					}
				})
			}
			switch c.code {
			case pb.OpCode_NODE_NETWORK:
				check("nil_setting", false, func(o *pb.Operation) { o.GetNetwork().Settings = []*pb.Sysctl{nil} })
				check("duplicate", false, func(o *pb.Operation) {
					o.GetNetwork().Settings = append(o.GetNetwork().Settings, o.GetNetwork().Settings[0])
				})
				check("forbidden_key", false, func(o *pb.Operation) { o.GetNetwork().Settings[0].Key = "kernel.core_pattern" })
				check("invalid_value", false, func(o *pb.Operation) { o.GetNetwork().Settings[0].Value = "2" })
				check("rp_filter_2", true, func(o *pb.Operation) {
					o.GetNetwork().Settings[0] = &pb.Sysctl{Key: "net.ipv4.conf.all.rp_filter", Value: "2"}
				})
			case pb.OpCode_INTERFACE, pb.OpCode_BD:
				for _, v := range []uint32{0, 67, 68, 65535, 65536} {
					check(fmt.Sprintf("mtu_%d", v), v >= 68 && v <= 65535, func(o *pb.Operation) {
						if o.Code == pb.OpCode_INTERFACE {
							o.GetInterface().Mtu = proto.Uint32(v)
						} else {
							o.GetBridge().Mtu = proto.Uint32(v)
						}
					})
				}
				for _, v := range []string{"", "bad;name", strings.Repeat("x", 16)} {
					check("name_"+v, false, func(o *pb.Operation) {
						if o.Code == pb.OpCode_INTERFACE {
							o.GetInterface().Name = v
						} else {
							o.GetBridge().Name = v
						}
					})
				}
				check("invalid_mac", false, func(o *pb.Operation) {
					if o.Code == pb.OpCode_INTERFACE {
						o.GetInterface().Mac = proto.String("not-a-mac")
					} else {
						o.GetBridge().Mac = proto.String("not-a-mac")
					}
				})
				if c.code == pb.OpCode_INTERFACE {
					check("invalid_addrgenmode", false, func(o *pb.Operation) { o.GetInterface().Addrgenmode = proto.String("random") })
				}
			case pb.OpCode_ADDRESS:
				check("invalid_prefix", false, func(o *pb.Operation) { o.GetAddress().Prefix = "192.0.2.2/33" })
				check("invalid_interface", false, func(o *pb.Operation) { o.GetAddress().Interface = "--help" })
				check("ipv6_prefix", true, func(o *pb.Operation) { o.GetAddress().Prefix = "2001:db8::1/64" })
			case pb.OpCode_TENANT:
				for _, v := range []uint32{0, 1, 252, 253, 254, 255, 256} {
					check(fmt.Sprintf("table_%d", v), v != 0 && (v < 253 || v > 255), func(o *pb.Operation) { o.GetTenant().TableId = v })
				}
				check("invalid_name", false, func(o *pb.Operation) { o.GetTenant().Name = "a b" })
			case pb.OpCode_BD_PORT:
				check("invalid_bridge", false, func(o *pb.Operation) { o.GetBridgePort().Bridge = "" })
				check("invalid_port", false, func(o *pb.Operation) { o.GetBridgePort().Port = "a;reboot" })
			case pb.OpCode_BD_TENANT:
				check("invalid_bridge", false, func(o *pb.Operation) { o.GetBinding().Bridge = "" })
				check("invalid_tenant", false, func(o *pb.Operation) { o.GetBinding().Tenant = "a b" })
			case pb.OpCode_VXLAN:
				for _, v := range []uint32{0, 1, 16777215, 16777216} {
					check(fmt.Sprintf("vni_%d", v), v > 0 && v <= 16777215, func(o *pb.Operation) { o.GetVxlan().Vni = v })
				}
				for _, v := range []uint32{0, 65535, 65536} {
					check(fmt.Sprintf("port_%d", v), v <= 65535, func(o *pb.Operation) { o.GetVxlan().Port = v })
				}
				check("invalid_local", false, func(o *pb.Operation) { o.GetVxlan().Local = "300.1.1.1" })
				check("invalid_underlay", false, func(o *pb.Operation) { o.GetVxlan().Underlay = "eth0;id" })
				check("invalid_name", false, func(o *pb.Operation) { o.GetVxlan().Name = "" })
			case pb.OpCode_STATIC_ROUTE:
				check("default_route", true, func(o *pb.Operation) { o.GetRoute().Prefix = "default" })
				check("invalid_prefix", false, func(o *pb.Operation) { o.GetRoute().Prefix = "bad" })
				check("invalid_gateway", false, func(o *pb.Operation) { o.GetRoute().Gateway = "bad" })
				check("invalid_interface", false, func(o *pb.Operation) { o.GetRoute().Interface = "" })
			}
		})
	}
}

// Scripted reads are captured-style iproute2 JSON. Writes must match independent
// expected argv; this is a driver contract test, not a kernel/network simulator.
type linuxSnapshot struct {
	links, addresses, routes string
	forward                  string
}
type linuxRunner struct {
	t                *testing.T
	snapshot, target linuxSnapshot
	writes           [][]string
	seen             [][]string
	failAt           int
	readError        error
	afterWrite       map[int]linuxSnapshot
}

var injectedLinuxError = errors.New("injected runner failure")

func (r *linuxRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) > 1 && (args[1] == "-j" || args[0] == "sysctl" && args[1] == "-n") {
		if r.readError != nil {
			return "", r.readError
		}
		switch {
		case reflect.DeepEqual(args, []string{"ip", "-j", "-d", "link", "show"}):
			return r.snapshot.links, nil
		case len(args) >= 4 && args[2] == "address" && args[3] == "show":
			return r.snapshot.addresses, nil
		case len(args) == 6 && args[2] == "route" && args[3] == "show":
			return r.snapshot.routes, nil
		case reflect.DeepEqual(args, []string{"sysctl", "-n", "net.ipv4.ip_forward"}):
			return r.snapshot.forward, nil
		}
	}
	i := len(r.seen)
	r.seen = append(r.seen, append([]string(nil), args...))
	if i >= len(r.writes) || !reflect.DeepEqual(args, r.writes[i]) {
		r.t.Errorf("unexpected write #%d: %q; expected %q", i, args, r.writes)
		return "", errors.New("unexpected command")
	}
	if r.failAt == i+1 {
		return "", injectedLinuxError
	}
	if snapshot, ok := r.afterWrite[i+1]; ok {
		r.snapshot = snapshot
	}
	if len(r.seen) == len(r.writes) {
		r.snapshot = r.target
	}
	return "", nil
}
func commands(lines ...string) [][]string {
	out := make([][]string, len(lines))
	for i, s := range lines {
		out[i] = strings.Fields(s)
	}
	return out
}
func (r *linuxRunner) expect(target linuxSnapshot, writes [][]string) {
	r.target = target
	r.writes = writes
	r.seen = nil
	r.failAt = 0
}
func (r *linuxRunner) complete() {
	r.t.Helper()
	if len(r.seen) != len(r.writes) {
		r.t.Fatalf("executed %d/%d writes", len(r.seen), len(r.writes))
	}
}

const ethBefore = `{"ifname":"eth0","flags":[],"mtu":1500,"address":"02:00:00:00:00:01","inet6_addr_gen_mode":"eui64"}`
const ethAfter = `{"ifname":"eth0","flags":["UP"],"mtu":9000,"address":"02:00:00:00:00:02","inet6_addr_gen_mode":"none"}`
const bridgeBefore = `{"ifname":"br0","flags":[],"mtu":1500,"address":"02:00:00:00:00:01","inet6_addr_gen_mode":"none","linkinfo":{"info_kind":"bridge","info_data":{"stp_state":0,"ageing_time":30000}}}`
const bridgeAfter = `{"ifname":"br0","flags":[],"mtu":9000,"address":"02:00:00:00:00:02","inet6_addr_gen_mode":"none","linkinfo":{"info_kind":"bridge","info_data":{"stp_state":1,"ageing_time":60000}}}`
const vrfBefore = `{"ifname":"blue","flags":[],"mtu":1500,"address":"02:00:00:00:00:01","inet6_addr_gen_mode":"none","linkinfo":{"info_kind":"vrf","info_data":{"table":1001}}}`
const vxlanBefore = `{"ifname":"vx0","flags":[],"mtu":1450,"address":"02:00:00:00:00:01","inet6_addr_gen_mode":"none","linkinfo":{"info_kind":"vxlan","info_data":{"id":10010,"local":"192.0.2.1","port":4789,"learning":false}}}`
const portBound = `{"ifname":"eth0","master":"br0","linkinfo":{"info_slave_data":{"learning":true,"neigh_suppress":false}}}`
const portAfter = `{"ifname":"eth0","master":"br0","linkinfo":{"info_slave_data":{"learning":false,"neigh_suppress":true}}}`
const portFree = `{"ifname":"eth0"}`
const bridgeBound = `{"ifname":"br0","master":"blue","linkinfo":{"info_kind":"bridge"}}`

func lifecycleFixture(c linuxCase, a pb.Action) (linuxSnapshot, linuxSnapshot, [][]string, [][]string) {
	before := linuxSnapshot{links: "[]", addresses: "[]", routes: "[]", forward: "0"}
	after := before
	var apply, undo [][]string
	switch c.code {
	case pb.OpCode_NODE_NETWORK:
		after.forward = "1"
		apply = commands("sysctl -w net.ipv4.ip_forward=1")
		undo = commands("sysctl -w net.ipv4.ip_forward=0")
	case pb.OpCode_INTERFACE:
		before.links = "[" + ethBefore + "]"
		after.links = "[" + ethAfter + "]"
		apply = commands("ip link set dev eth0 mtu 9000", "ip link set dev eth0 address 02:00:00:00:00:02", "ip link set dev eth0 addrgenmode none", "ip link set dev eth0 up")
		undo = commands("ip link set dev eth0 mtu 1500", "ip link set dev eth0 address 02:00:00:00:00:01", "ip link set dev eth0 addrgenmode eui64", "ip link set dev eth0 down")
	case pb.OpCode_ADDRESS:
		before.links = "[" + ethBefore + "]"
		after.links = before.links
		after.addresses = `[{"addr_info":[{"local":"192.0.2.2","prefixlen":24,"scope":"global"}]}]`
		apply = commands("ip address add 192.0.2.2/24 dev eth0")
		undo = commands("ip address del 192.0.2.2/24 dev eth0")
		if a == pb.Action_DELETE {
			before, after = after, before
			apply, undo = undo, apply
		}
	case pb.OpCode_STATIC_ROUTE:
		after.routes = `[{"dst":"198.51.100.0/24","gateway":"192.0.2.1","dev":"eth0","protocol":"static"}]`
		apply = commands("ip route add 198.51.100.0/24 via 192.0.2.1 dev eth0 table 254 proto static")
		undo = commands("ip route del 198.51.100.0/24 via 192.0.2.1 dev eth0 table 254 proto static")
		if a == pb.Action_DELETE {
			before, after = after, before
			apply, undo = undo, apply
		}
	case pb.OpCode_TENANT:
		after.links = "[" + vrfBefore + "]"
		apply = commands("ip link add blue type vrf table 1001")
		undo = commands("ip link del dev blue")
		if a == pb.Action_DELETE {
			before, after = after, before
			apply = undo
			undo = commands("ip link add blue type vrf table 1001", "ip link set dev blue mtu 1500", "ip link set dev blue address 02:00:00:00:00:01", "ip link set dev blue addrgenmode none", "ip link set dev blue down")
		}
	case pb.OpCode_BD:
		after.links = "[" + bridgeAfter + "]"
		apply = commands("ip link add br0 type bridge stp_state 1", "ip link set dev br0 mtu 9000", "ip link set dev br0 address 02:00:00:00:00:02", "ip link set dev br0 type bridge ageing_time 60000")
		undo = commands("ip link del dev br0")
		if a == pb.Action_SET {
			before.links = "[" + bridgeBefore + "]"
			apply = commands("ip link set dev br0 type bridge stp_state 1", "ip link set dev br0 type bridge ageing_time 60000", "ip link set dev br0 mtu 9000", "ip link set dev br0 address 02:00:00:00:00:02")
			undo = commands("ip link set dev br0 type bridge stp_state 0", "ip link set dev br0 type bridge ageing_time 30000", "ip link set dev br0 mtu 1500", "ip link set dev br0 address 02:00:00:00:00:01")
		}
		if a == pb.Action_DELETE {
			before.links = "[" + bridgeBefore + "]"
			after.links = "[]"
			apply = commands("ip link del dev br0")
			undo = commands("ip link add br0 type bridge stp_state 0 ageing_time 30000", "ip link set dev br0 mtu 1500", "ip link set dev br0 address 02:00:00:00:00:01", "ip link set dev br0 addrgenmode none", "ip link set dev br0 down")
		}
	case pb.OpCode_VXLAN:
		after.links = "[" + vxlanBefore + "]"
		apply = commands("ip link add vx0 type vxlan id 10010 local 192.0.2.1 dstport 4789 nolearning dev eth0")
		undo = commands("ip link del dev vx0")
		if a == pb.Action_DELETE {
			before, after = after, before
			apply = undo
			undo = commands("ip link add vx0 type vxlan id 10010 local 192.0.2.1 dstport 4789 nolearning dev eth0", "ip link set dev vx0 mtu 1450", "ip link set dev vx0 address 02:00:00:00:00:01", "ip link set dev vx0 addrgenmode none", "ip link set dev vx0 down")
		}
	case pb.OpCode_BD_PORT:
		before.links = "[" + bridgeBefore + "," + portFree + "]"
		after.links = "[" + bridgeBefore + "," + portAfter + "]"
		apply = commands("ip link set dev eth0 master br0", "ip link set dev eth0 type bridge_slave learning off", "ip link set dev eth0 type bridge_slave neigh_suppress on")
		undo = commands("ip link set dev eth0 nomaster")
		if a == pb.Action_SET || a == pb.Action_UNBIND {
			before.links = "[" + bridgeBefore + "," + portBound + "]"
			undo = commands("ip link set dev eth0 master br0", "ip link set dev eth0 type bridge_slave learning on", "ip link set dev eth0 type bridge_slave neigh_suppress off")
		}
		if a == pb.Action_UNBIND {
			after.links = "[" + bridgeBefore + "," + portFree + "]"
			apply = commands("ip link set dev eth0 nomaster")
		}
	case pb.OpCode_BD_TENANT:
		before.links = "[" + vrfBefore + "," + bridgeBefore + "]"
		after.links = "[" + vrfBefore + "," + bridgeBound + "]"
		apply = commands("ip link set dev br0 master blue")
		undo = commands("ip link set dev br0 nomaster")
		if a == pb.Action_UNBIND {
			before, after = after, before
			apply, undo = undo, apply
		}
	}
	return before, after, apply, undo
}

func TestLinuxOperationLifecycle(t *testing.T) {
	for _, c := range linuxCases() {
		for _, a := range c.actions {
			t.Run(c.code.String()+"/"+a.String(), func(t *testing.T) {
				o := linuxOp(t, c, a)
				before, after, apply, inverse := lifecycleFixture(c, a)
				r := &linuxRunner{t: t, snapshot: before}
				d := &Driver{Runner: r}
				ctx := context.Background()
				u, err := d.Prepare(ctx, o)
				if err != nil {
					t.Fatal(err)
				}
				if len(r.seen) != 0 {
					t.Fatal("Prepare mutated network")
				}
				if err = d.Verify(ctx, o); err == nil {
					t.Fatal("pre-change state passed verification")
				}
				r.expect(after, apply)
				if err = d.Apply(ctx, o); err != nil {
					t.Fatal(err)
				}
				r.complete()
				if err = d.Verify(ctx, o); err != nil {
					t.Fatal(err)
				}
				// Persisted Undo must survive encoding, as it does on agent restart.
				b, err := json.Marshal(u)
				if err != nil {
					t.Fatal(err)
				}
				var saved model.Undo
				if err = json.Unmarshal(b, &saved); err != nil {
					t.Fatal(err)
				}
				r.expect(before, inverse)
				if err = d.Rollback(ctx, o, &saved); err != nil {
					t.Fatal(err)
				}
				r.complete()
				r.expect(before, nil)
				if err = d.Rollback(ctx, o, &saved); err != nil {
					t.Fatal(err)
				}
				r.complete()
			})
		}
	}
}

func TestLinuxExecutionFailures(t *testing.T) {
	for _, c := range linuxCases() {
		for _, a := range c.actions {
			t.Run(c.code.String()+"/"+a.String(), func(t *testing.T) {
				o := linuxOp(t, c, a)
				before, after, apply, _ := lifecycleFixture(c, a)
				ctx := context.Background()
				r := &linuxRunner{t: t, snapshot: before}
				d := &Driver{Runner: r}
				r.readError = injectedLinuxError
				if _, err := d.Prepare(ctx, o); !errors.Is(err, injectedLinuxError) {
					t.Fatalf("Prepare: %v", err)
				}
				if err := d.Apply(ctx, o); !errors.Is(err, injectedLinuxError) {
					t.Fatalf("Apply: %v", err)
				}
				if err := d.Verify(ctx, o); !errors.Is(err, injectedLinuxError) {
					t.Fatalf("Verify: %v", err)
				}
				if len(r.seen) != 0 {
					t.Fatal("read failure caused writes")
				}
				r.readError = nil
				r.expect(after, apply)
				r.failAt = 1
				if err := d.Apply(ctx, o); !errors.Is(err, injectedLinuxError) {
					t.Fatalf("write failure lost: %v", err)
				}
				if len(r.seen) != 1 {
					t.Fatal("continued after failed command")
				}
			})
		}
	}
}

func TestLinuxRejectsUnsafeState(t *testing.T) {
	for _, c := range linuxCases() {
		for _, a := range c.actions {
			if a != pb.Action_CREATE && a != pb.Action_DELETE {
				continue
			}
			t.Run(c.code.String()+"/"+a.String(), func(t *testing.T) {
				before, after, _, _ := lifecycleFixture(c, a)
				// CREATE on an existing object or DELETE on an absent object conflicts.
				_ = before
				r := &linuxRunner{t: t, snapshot: after}
				d := &Driver{Runner: r}
				if _, err := d.Prepare(context.Background(), linuxOp(t, c, a)); err == nil {
					t.Fatal("existence conflict accepted")
				}
				if len(r.seen) > 0 {
					t.Fatal("validation modified state")
				}
			})
		}
	}
	for _, test := range []struct{ name, links, addresses string }{
		{"has_master", `[{"ifname":"br0","master":"blue","linkinfo":{"info_kind":"bridge"}}]`, `[]`},
		{"has_member", "[" + bridgeBefore + "," + portBound + "]", `[]`},
		{"has_address", "[" + bridgeBefore + "]", `[{"addr_info":[{"scope":"global"}]}]`},
		{"wrong_kind", `[{"ifname":"br0","linkinfo":{"info_kind":"dummy"}}]`, `[]`},
		{"malformed_json", `{`, `[]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &linuxRunner{t: t, snapshot: linuxSnapshot{links: test.links, addresses: test.addresses}}
			d := &Driver{Runner: r}
			if _, err := d.Prepare(context.Background(), linuxOp(t, linuxCases()[4], pb.Action_DELETE)); err == nil {
				t.Fatal("unsafe delete accepted")
			}
		})
	}
}

func TestLinuxRollbackFailures(t *testing.T) {
	c := linuxCases()[1]
	o := linuxOp(t, c, pb.Action_SET)
	before, after, _, undo := lifecycleFixture(c, pb.Action_SET)
	ctx := context.Background()
	r := &linuxRunner{t: t, snapshot: before}
	d := &Driver{Runner: r}
	u, err := d.Prepare(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	r.snapshot = after
	r.expect(before, undo)
	r.failAt = 1
	if err = d.Rollback(ctx, o, u); !errors.Is(err, injectedLinuxError) {
		t.Fatalf("lost undo error: %v", err)
	}
	if len(r.seen) != 1 {
		t.Fatal("continued undo after failed command")
	}
	r.expect(after, undo)
	if err = d.Rollback(ctx, o, u); err == nil {
		t.Fatal("successful commands hid failed rollback readback")
	}
	for _, u := range []*model.Undo{nil, {Data: []byte("bad")}} {
		if d.Rollback(ctx, o, u) == nil {
			t.Fatal("invalid undo accepted")
		}
	}
}

func TestLinuxPartialApplyRestoresOriginalValues(t *testing.T) {
	c := linuxCases()[1]
	o := linuxOp(t, c, pb.Action_SET)
	before, after, apply, undo := lifecycleFixture(c, pb.Action_SET)
	partial := before
	partial.links = strings.Replace(before.links, `"mtu":1500`, `"mtu":9000`, 1)
	r := &linuxRunner{t: t, snapshot: before, afterWrite: map[int]linuxSnapshot{1: partial}}
	d := &Driver{Runner: r}
	ctx := context.Background()
	saved, err := d.Prepare(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	r.expect(after, apply)
	r.failAt = 2
	if err = d.Apply(ctx, o); !errors.Is(err, injectedLinuxError) {
		t.Fatalf("expected second-command failure: %v", err)
	}
	if r.snapshot != partial {
		t.Fatal("fixture did not retain partial MTU change")
	}
	r.afterWrite = nil
	r.expect(before, undo)
	if err = d.Rollback(ctx, o, saved); err != nil {
		t.Fatal(err)
	}
	r.complete()
}

func TestLinuxRollbackResumesAfterInverseCreate(t *testing.T) {
	c := linuxCases()[7]
	o := linuxOp(t, c, pb.Action_DELETE)
	before, _, _, undo := lifecycleFixture(c, pb.Action_DELETE)
	r := &linuxRunner{t: t, snapshot: before}
	d := &Driver{Runner: r}
	ctx := context.Background()
	saved, err := d.Prepare(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	// Crash occurred after inverse link creation but before restoring properties.
	r.snapshot = before
	r.snapshot.links = strings.Replace(before.links, `"mtu":1450`, `"mtu":1500`, 1)
	r.expect(before, undo[1:])
	if err = d.Rollback(ctx, o, saved); err != nil {
		t.Fatal(err)
	}
	r.complete()
}

type linuxRunFunc func(context.Context, ...string) (string, error)

func (f linuxRunFunc) Run(ctx context.Context, args ...string) (string, error) {
	return f(ctx, args...)
}

func TestLinuxOptionalSysctlAndReadback(t *testing.T) {
	o := linuxOp(t, linuxCases()[0], pb.Action_SET)
	o.GetNetwork().Settings[0] = &pb.Sysctl{Key: "net.bridge.bridge-nf-call-iptables", Value: "0", Optional: true}
	d := &Driver{Runner: linuxRunFunc(func(_ context.Context, args ...string) (string, error) {
		if !reflect.DeepEqual(args, []string{"sysctl", "-n", "net.bridge.bridge-nf-call-iptables"}) {
			t.Fatalf("optional absent key was written: %q", args)
		}
		return "", injectedLinuxError
	})}
	ctx := context.Background()
	u, err := d.Prepare(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Apply(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err = d.Verify(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err = d.Rollback(ctx, o, u); err != nil {
		t.Fatal(err)
	}
	o.GetNetwork().Settings[0].Optional = false
	if _, err = d.Prepare(ctx, o); !errors.Is(err, injectedLinuxError) {
		t.Fatalf("required sysctl ignored: %v", err)
	}
}

func TestLinuxStaticRouteReadback(t *testing.T) {
	c := linuxCases()[8]
	o := linuxOp(t, c, pb.Action_CREATE)
	o.GetRoute().Prefix = "0.0.0.0/0"
	o.GetRoute().Table = 1001
	for _, tc := range []struct {
		name, json string
		valid      bool
	}{
		{"default_normalization", `[{"dst":"default","gateway":"192.0.2.1","dev":"eth0"}]`, true},
		{"wrong_gateway", `[{"dst":"default","gateway":"192.0.2.99","dev":"eth0"}]`, false},
		{"wrong_device", `[{"dst":"default","gateway":"192.0.2.1","dev":"eth1"}]`, false},
		{"wrong_prefix", `[{"dst":"192.0.2.0/24","gateway":"192.0.2.1","dev":"eth0"}]`, false},
		{"malformed_json", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Driver{Runner: linuxRunFunc(func(_ context.Context, args ...string) (string, error) {
				if !reflect.DeepEqual(args, []string{"ip", "-j", "route", "show", "table", "1001"}) {
					t.Fatalf("wrong route table: %q", args)
				}
				return tc.json, nil
			})}
			if err := d.Verify(context.Background(), o); (err == nil) != tc.valid {
				t.Fatalf("readback valid=%t: %v", tc.valid, err)
			}
		})
	}
}
