package agent

import (
	"context"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
	"testing"
)

type queryDriver struct {
	*fakeDriver
	rows []map[string]any
	err  error
}

func (d *queryDriver) QueryCapabilities() []*pb.QueryCapability {
	return []*pb.QueryCapability{{Kind: pb.QueryKind_BRIDGES}}
}
func (d *queryDriver) Query(context.Context, *pb.QueryRequest) ([]map[string]any, error) {
	return d.rows, d.err
}
func TestQueryPaginationAndReadErrors(t *testing.T) {
	d := &queryDriver{fakeDriver: fake("fixture"), rows: []map[string]any{{"name": "b"}, {"name": "a"}}}
	a := newAgent(t, d)
	ctx := context.Background()
	q := &pb.QueryRequest{NodeId: a.NodeID, Kind: pb.QueryKind_BRIDGES, PageSize: 1}
	first, e := a.Query(ctx, q)
	if e != nil || first.TotalCount != 2 || len(first.Records) != 1 || first.Records[0].Fields["name"].GetStringValue() != "a" || first.NextPageToken == "" {
		t.Fatalf("first page %v %v", first, e)
	}
	q.PageToken = first.NextPageToken
	second, e := a.Query(ctx, q)
	if e != nil || second.NextPageToken != "" || second.Records[0].Fields["name"].GetStringValue() != "b" {
		t.Fatalf("second page %v %v", second, e)
	}
	d.rows[0]["name"] = "c"
	if _, e = a.Query(ctx, q); status.Code(e) != codes.Aborted {
		t.Fatalf("changed data token accepted: %v", e)
	}
	q.PageToken = ""
	d.err = fmt.Errorf("backend unavailable")
	if result, e := a.Query(ctx, q); e == nil || result != nil {
		t.Fatal("backend failure turned into empty success")
	}
	q.NodeId = "wrong"
	if _, e = a.Query(ctx, q); status.Code(e) != codes.PermissionDenied {
		t.Fatal(e)
	}
}
func TestVerifyResourcesRevisionBarrierAndReadOnly(t *testing.T) {
	d := fake("fixture")
	a := newAgent(t, d)
	ctx := context.Background()
	q := &pb.VerifyResourcesRequest{NodeId: a.NodeID, Operations: []*pb.Operation{op("bridge")}}
	r, e := a.VerifyResources(ctx, q)
	if e != nil || !r.Verified || d.value("bridge") != "" {
		t.Fatalf("read-only verification %v %v", r, e)
	}
	q.ExpectedRevision = 1
	if _, e = a.VerifyResources(ctx, q); status.Code(e) != codes.Aborted {
		t.Fatal(e)
	}
	q.ExpectedRevision = 0
	if e = a.Store.Atomic(ctx, map[string]any{"task/pending": &model.Record{Task: &pb.Task{Id: "pending", State: "QUEUED"}}}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.VerifyResources(ctx, q); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("pending task did not block verification", e)
	}
}
