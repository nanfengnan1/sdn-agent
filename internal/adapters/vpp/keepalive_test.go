package vpp

import (
	"context"
	"encoding/json"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/linux"
	"sdn-agent/internal/backend"
	"testing"
)

type keepaliveRPC struct {
	methods  []string
	payloads []string
}

func (r *keepaliveRPC) Call(_ context.Context, method string, value any, out any) error {
	r.methods = append(r.methods, method)
	b, _ := json.Marshal(value)
	r.payloads = append(r.payloads, string(b))
	if method == "prepare" {
		*(out.(*json.RawMessage)) = json.RawMessage(`{"neighborKeepalive":{"enabled":false}}`)
	}
	return nil
}
func TestKeepaliveSwitchUsesOnlyVPPAndPreservesFalse(t *testing.T) {
	rpc := &keepaliveRPC{}
	d := &Driver{RPC: rpc} // no Linux driver: this policy must never touch Linux.
	off := false
	op := &pb.Operation{Id: "keepalive", Code: pb.OpCode_NEIGHBOR_KEEPALIVE, Action: pb.Action_SET, Data: &pb.Operation_NeighborKeepalive{NeighborKeepalive: &pb.NeighborKeepalive{Enabled: &off}}}
	ctx := context.Background()
	undo, err := d.Prepare(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() error{func() error { return d.Apply(ctx, op) }, func() error { return d.Verify(ctx, op) }, func() error { return d.Rollback(ctx, op, undo) }} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rpc.payloads[1]), &body); err != nil {
		t.Fatal(err)
	}
	if value, ok := body["neighborKeepalive"].(map[string]any)["enabled"]; !ok || value != false {
		t.Fatal("explicit false lost", body)
	}
	for _, cap := range d.Features() {
		if cap == "neighbor-keepalive/v1" {
			return
		}
	}
	t.Fatal("missing feature capability")
}
func TestKeepaliveRejectsMissingValueDeleteAndLinux(t *testing.T) {
	off := false
	d := &Driver{}
	for _, o := range []*pb.Operation{
		{Code: pb.OpCode_NEIGHBOR_KEEPALIVE, Action: pb.Action_SET},
		{Code: pb.OpCode_NEIGHBOR_KEEPALIVE, Action: pb.Action_SET, Data: &pb.Operation_NeighborKeepalive{NeighborKeepalive: &pb.NeighborKeepalive{}}},
		{Code: pb.OpCode_NEIGHBOR_KEEPALIVE, Action: pb.Action_DELETE, Data: &pb.Operation_NeighborKeepalive{NeighborKeepalive: &pb.NeighborKeepalive{Enabled: &off}}},
	} {
		if d.Validate(o) == nil {
			t.Fatal("invalid switch accepted", o)
		}
	}
	reg, err := backend.New(&linux.Driver{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.Plan([]*pb.Operation{{Id: "keepalive", Code: pb.OpCode_NEIGHBOR_KEEPALIVE, Action: pb.Action_SET, Data: &pb.Operation_NeighborKeepalive{NeighborKeepalive: &pb.NeighborKeepalive{Enabled: &off}}}}); err == nil {
		t.Fatal("Linux accepted VPP policy")
	}
}
