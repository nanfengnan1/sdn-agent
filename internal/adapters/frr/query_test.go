package frr

import (
	"context"
	"fmt"
	"os"
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
	return "", fmt.Errorf("unexpected command %s", key)
}
func TestQueryRealFRRFixtures(t *testing.T) {
	for _, tc := range []struct {
		file        string
		request     *pb.QueryRequest
		field, want string
	}{{"evpn-frr-10.2.json", &pb.QueryRequest{Kind: pb.QueryKind_EVPN_ROUTES, RouteType: 3}, "rd", "10.0.0.1:3"}, {"bgp-ipv6-frr-10.2.json", &pb.QueryRequest{Kind: pb.QueryKind_BGP_ROUTES, Family: pb.AddressFamily_IPV6}, "prefix", "2001:db8:1::/64"}, {"ospf6-interface-frr-10.2.json", &pb.QueryRequest{Kind: pb.QueryKind_OSPF_INTERFACES, OspfVersion: 3, Interface: "lo"}, "interface", "lo"}} {
		t.Run(tc.file, func(t *testing.T) {
			body, e := os.ReadFile("testdata/" + tc.file)
			if e != nil {
				t.Fatal(e)
			}
			cmd, e := queryCommand(tc.request)
			if e != nil {
				t.Fatal(e)
			}
			d := &Driver{Runner: queryRunner{"vtysh -c " + cmd: string(body)}}
			rows, e := d.Query(context.Background(), tc.request)
			if e != nil || len(rows) == 0 || rows[0][tc.field] != tc.want {
				t.Fatalf("records %v error %v", rows, e)
			}
		})
	}
}
func TestQueryFailuresAndType5Prefix(t *testing.T) {
	q := &pb.QueryRequest{Kind: pb.QueryKind_BGP_ROUTES, Family: pb.AddressFamily_IPV6}
	cmd, _ := queryCommand(q)
	for _, text := range []string{"", `{"warning":"BGP instance missing"}`, "% Unknown command", `null`} {
		d := &Driver{Runner: queryRunner{"vtysh -c " + cmd: text}}
		if rows, e := d.Query(context.Background(), q); e == nil {
			t.Fatalf("error became empty success %q %v", text, rows)
		}
	}
	q = &pb.QueryRequest{Kind: pb.QueryKind_EVPN_ROUTES, RouteType: 5, Prefix: "2001:db8::/64"}
	cmd, _ = queryCommand(q)
	d := &Driver{Runner: queryRunner{"vtysh -c " + cmd: `{"10.0.0.1:50000":{"[5]:[0]:[64]:[2001:db8::]":{"paths":[{"routeType":5,"ip":"2001:db8::","ipLen":64}]},"[3]:[0]:[32]:[10.0.0.1]":{"paths":[]}}}`}}
	rows, e := d.Query(context.Background(), q)
	if e != nil || len(rows) != 1 || rows[0]["prefix"] != "2001:db8::/64" {
		t.Fatalf("Type5 IPv6 prefix %v %v", rows, e)
	}
}

func TestQueryNeighborFilters(t *testing.T) {
	q := &pb.QueryRequest{Kind: pb.QueryKind_OSPF_NEIGHBORS, Interface: "swp1"}
	cmd, _ := queryCommand(q)
	d := &Driver{Runner: queryRunner{"vtysh -c " + cmd: `{"neighbors":{"10.0.0.1":[{"ifaceName":"swp1:192.0.2.1","nbrState":"Full/-"}],"10.0.0.2":[{"ifaceName":"swp2:192.0.2.5","nbrState":"Full/-"}]}}`}}
	rows, e := d.Query(context.Background(), q)
	if e != nil || len(rows) != 1 || rows[0]["interface"] != "swp1" {
		t.Fatalf("OSPF interface filter %v %v", rows, e)
	}
	q = &pb.QueryRequest{Kind: pb.QueryKind_BGP_NEIGHBORS, Family: pb.AddressFamily_IPV6}
	cmd, _ = queryCommand(q)
	d.Runner = queryRunner{"vtysh -c " + cmd: `{"192.0.2.1":{"addressFamilyInfo":{"ipv6Unicast":{}}},"192.0.2.2":{"addressFamilyInfo":{"ipv4Unicast":{}}}}`}
	rows, e = d.Query(context.Background(), q)
	if e != nil || len(rows) != 1 || rows[0]["neighbor"] != "192.0.2.1" {
		t.Fatalf("BGP NLRI family must not use transport family %v %v", rows, e)
	}
}
