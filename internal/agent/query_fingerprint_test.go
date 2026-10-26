package agent

import (
	"context"
	"encoding/json"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	pb "sdn-agent/api/agent/v1"
	"testing"
)

type fingerprintDriver struct{ *queryDriver }

func (d *fingerprintDriver) QueryCapabilities() []*pb.QueryCapability {
	return []*pb.QueryCapability{{Kind: pb.QueryKind_INTERFACES}, {Kind: pb.QueryKind_ROUTES}}
}

func TestInterfacePaginationRealTimersDoNotInvalidateOrReorder(t *testing.T) {
	// Captured from iproute2 on the dedicated Linux 6.8 integration leaf.
	body, e := os.ReadFile("testdata/interfaces-linux-6.8.json")
	if e != nil {
		t.Fatal(e)
	}
	var links []map[string]any
	if e = json.Unmarshal(body, &links); e != nil {
		t.Fatal(e)
	}
	var rows []map[string]any
	var bridgeData map[string]any
	var bridgeRecords []map[string]any
	for _, link := range links {
		rows = append(rows, map[string]any{"name": link["ifname"], "details": link})
		if info, ok := link["linkinfo"].(map[string]any); ok && info["info_kind"] == "bridge" {
			bridgeData = info["info_data"].(map[string]any)
			bridgeRecords = append(bridgeRecords, bridgeData)
		}
	}
	if bridgeData == nil || len(rows) < 2 {
		t.Fatal("fixture must contain a bridge and multiple interfaces")
	}
	d := &fingerprintDriver{&queryDriver{fakeDriver: fake("fixture"), rows: rows}}
	a := newAgent(t, d)
	ctx := context.Background()
	q := &pb.QueryRequest{NodeId: a.NodeID, Kind: pb.QueryKind_INTERFACES, PageSize: 1}
	page, e := a.Query(ctx, q)
	if e != nil || page.NextPageToken == "" {
		t.Fatalf("first page %v %v", page, e)
	}
	seen := map[string]bool{page.Records[0].Fields["name"].GetStringValue(): true}
	for page.NextPageToken != "" {
		for _, data := range bridgeRecords {
			data["gc_timer"] = 99999.0
			data["hello_timer"] = 0.9
			data["tcn_timer"] = 0.7
			data["topology_change_timer"] = 1.3
		}
		q.PageToken = page.NextPageToken
		page, e = a.Query(ctx, q)
		if e != nil {
			t.Fatalf("countdown broke continuation: %v", e)
		}
		for _, row := range page.Records {
			name := row.Fields["name"].GetStringValue()
			if seen[name] {
				t.Fatalf("record moved across pages: %s", name)
			}
			seen[name] = true
			if details := row.Fields["details"].GetStructValue(); details != nil {
				if info := details.Fields["linkinfo"].GetStructValue(); info != nil && info.Fields["info_kind"].GetStringValue() == "bridge" {
					data := info.Fields["info_data"].GetStructValue()
					if data.Fields["gc_timer"].GetNumberValue() != 99999 {
						t.Fatal("raw timer removed from returned observation")
					}
				}
			}
		}
	}
	if len(seen) != len(rows) {
		t.Fatalf("missing interfaces: %d/%d", len(seen), len(rows))
	}
	q.PageToken = ""
	page, e = a.Query(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	q.PageToken = page.NextPageToken
	bridgeData["ageing_time"] = float64(12345)
	if _, e = a.Query(ctx, q); status.Code(e) != codes.Aborted {
		t.Fatalf("configuration ageing_time excluded: %v", e)
	}
	q.PageToken = ""
	page, e = a.Query(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	q.PageToken = page.NextPageToken
	d.rows = append(d.rows, map[string]any{"name": "new-interface"})
	if _, e = a.Query(ctx, q); status.Code(e) != codes.Aborted {
		t.Fatalf("interface addition accepted stale token: %v", e)
	}
}

func TestRoutePaginationRejectsChangedPathsAndSet(t *testing.T) {
	d := &fingerprintDriver{&queryDriver{fakeDriver: fake("fixture"), rows: []map[string]any{{"prefix": "192.0.2.0/24", "expires": float64(30), "gateway": "198.51.100.1"}, {"prefix": "2001:db8::/64", "gateway": "2001:db8:1::1"}}}}
	a := newAgent(t, d)
	q := &pb.QueryRequest{NodeId: a.NodeID, Kind: pb.QueryKind_ROUTES, PageSize: 1}
	ctx := context.Background()
	page, e := a.Query(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	q.PageToken = page.NextPageToken
	d.rows[0]["expires"] = float64(29)
	if _, e = a.Query(ctx, q); e != nil {
		t.Fatalf("route expiry countdown invalidated collection: %v", e)
	}
	d.rows[0]["gateway"] = "198.51.100.2"
	if _, e = a.Query(ctx, q); status.Code(e) != codes.Aborted {
		t.Fatalf("route path change accepted: %v", e)
	}
	q.PageToken = ""
	page, e = a.Query(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	q.PageToken = page.NextPageToken
	d.rows = d.rows[:1]
	if _, e = a.Query(ctx, q); status.Code(e) != codes.Aborted {
		t.Fatalf("route withdrawal accepted: %v", e)
	}
}
