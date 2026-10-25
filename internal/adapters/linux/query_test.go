package linux

import (
	"context"
	"fmt"
	pb "sdn-agent/api/agent/v1"
	"strings"
	"testing"
)

type queryRunner map[string]string

func (r queryRunner) Run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	if v, ok := r[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("unexpected query %s", key)
}

const linksJSON = `[{"ifindex":10,"ifname":"blue","linkinfo":{"info_kind":"vrf","info_data":{"table":1001}}},{"ifindex":11,"ifname":"br10","master":"blue","linkinfo":{"info_kind":"bridge"}},{"ifindex":12,"ifname":"vx10","master":"br10","linkinfo":{"info_kind":"vxlan","info_data":{"id":10010,"local":"192.0.2.1","port":4789},"info_slave_data":{"neigh_suppress":true}}},{"ifindex":13,"ifname":"orphan","linkinfo":{"info_kind":"bridge"}}]`

func TestQueryLinuxRelationsAndIPv6(t *testing.T) {
	d := &Driver{Runner: queryRunner{"ip -j -d link show": linksJSON, "ip -j -6 route show vrf blue": `[{"dst":"2001:db8::/64","dev":"br10","protocol":"kernel"}]`, "ip -j -6 neigh show": `[{"dst":"2001:db8::2","dev":"br10","lladdr":"02:00:00:00:00:02","state":["REACHABLE"]}]`}}
	rows, e := d.Query(context.Background(), &pb.QueryRequest{Kind: pb.QueryKind_BRIDGES, Vrf: "blue"})
	if e != nil || len(rows) != 1 || rows[0]["name"] != "br10" || len(rows[0]["ports"].([]any)) != 1 {
		t.Fatalf("bridge associations %v %v", rows, e)
	}
	rows, e = d.Query(context.Background(), &pb.QueryRequest{Kind: pb.QueryKind_BRIDGES})
	if e != nil || len(rows) != 2 {
		t.Fatalf("unbound bridge missing %v %v", rows, e)
	}
	for _, kind := range []pb.QueryKind{pb.QueryKind_ROUTES, pb.QueryKind_NEIGHBORS} {
		rows, e = d.Query(context.Background(), &pb.QueryRequest{Kind: kind, Vrf: "blue", Family: pb.AddressFamily_IPV6})
		if e != nil || len(rows) != 1 || rows[0]["vrf"] != "blue" || rows[0]["family"] != "IPV6" {
			t.Fatalf("IPv6 %s: %v %v", kind, rows, e)
		}
	}
	if _, e = d.Query(context.Background(), &pb.QueryRequest{Kind: pb.QueryKind_INTERFACES, Interface: "missing"}); e == nil {
		t.Fatal("missing interface appeared empty/successful")
	}
	d.Runner = queryRunner{"ip -j -d link show": "warning: broken"}
	if _, e = d.Query(context.Background(), &pb.QueryRequest{Kind: pb.QueryKind_TENANTS}); e == nil {
		t.Fatal("malformed backend output accepted")
	}
}
