package vpp

import (
	"context"
	"encoding/json"
	"google.golang.org/protobuf/proto"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/linux"
	"testing"
)

func TestEncapVRFSetKeepsExplicitZeroAndUsesAdapterUndo(t *testing.T) {
	rpc := &keepaliveRPC{}
	d := &Driver{Linux: &linux.Driver{}, RPC: rpc}
	op := &pb.Operation{Code: pb.OpCode_VXLAN, Action: pb.Action_SET,
		Data: &pb.Operation_Vxlan{Vxlan: &pb.Vxlan{Name: "vx50000", EncapVrfId: proto.Uint32(0)}}}
	ctx := context.Background()
	saved, err := d.Prepare(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Apply(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = d.Verify(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = d.Rollback(ctx, op, saved); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal([]byte(rpc.payloads[1]), &body); err != nil {
		t.Fatal(err)
	}
	field, ok := body["vxlan"].(map[string]any)["encapVrfId"]
	if !ok || field != float64(0) {
		t.Fatal("explicit default VRF lost", body)
	}
	if err = d.Linux.Validate(op); err == nil {
		t.Fatal("Linux accepted native VPP SET")
	}
}
func TestEncapVRFRejectsAmbiguousSetAndInvalidTables(t *testing.T) {
	d := &Driver{Linux: &linux.Driver{}}
	for _, x := range []*pb.Vxlan{
		{Name: "vx1"},
		{Name: "vx1", EncapVrfId: proto.Uint32(100), Vni: 100},
		{Name: "vx1", EncapVrfId: proto.Uint32(254)},
		{Name: "vx1", EncapVrfId: proto.Uint32(^uint32(0))},
		{Name: "bad name", EncapVrfId: proto.Uint32(100)},
	} {
		if d.Validate(&pb.Operation{Code: pb.OpCode_VXLAN, Action: pb.Action_SET, Data: &pb.Operation_Vxlan{Vxlan: x}}) == nil {
			t.Fatal("accepted invalid SET", x)
		}
	}
	op := &pb.Operation{Code: pb.OpCode_VXLAN, Action: pb.Action_CREATE, Data: &pb.Operation_Vxlan{Vxlan: &pb.Vxlan{Name: "vx1", Vni: 100, Local: "192.0.2.1", EncapVrfId: proto.Uint32(100)}}}
	if err := d.Validate(op); err != nil {
		t.Fatal(err)
	}
	if d.Linux.Validate(op) == nil {
		t.Fatal("Linux silently ignored nonzero encap VRF")
	}
}

type mirrorRestoreRunner struct{ present bool }

func (r *mirrorRestoreRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) > 2 && args[0] == "ip" && args[1] == "link" && args[2] == "add" {
		r.present = true
		return "", nil
	}
	if len(args) > 2 && args[0] == "ip" && args[1] == "-j" && args[2] == "address" {
		return "[]", nil
	}
	if len(args) > 3 && args[0] == "ip" && args[1] == "-j" && args[2] == "-d" {
		if !r.present {
			return "[]", nil
		}
		return "[{\"ifname\":\"vx1\",\"mtu\":1500,\"address\":\"02:00:00:00:00:01\",\"flags\":[\"UP\"],\"linkinfo\":{\"info_kind\":\"vxlan\",\"info_data\":{\"id\":10100,\"local\":\"192.0.2.1\",\"port\":4789,\"learning\":false}}}]", nil
	}
	return "", nil
}

type mirrorRestoreRPC struct {
	keepaliveRPC
	kernel *mirrorRestoreRunner
	t      *testing.T
}

func (r *mirrorRestoreRPC) Call(ctx context.Context, method string, value any, out any) error {
	if method == "rollback" && !r.kernel.present {
		r.t.Fatal("adapter rollback ran before deleted Linux mirror was restored")
	}
	return r.keepaliveRPC.Call(ctx, method, value, out)
}
func TestDeletedVXLANRestoresMirrorBeforeAdapter(t *testing.T) {
	kernel := &mirrorRestoreRunner{present: true}
	rpc := &mirrorRestoreRPC{kernel: kernel, t: t}
	d := &Driver{Linux: &linux.Driver{Runner: kernel}, RPC: rpc}
	op := &pb.Operation{Code: pb.OpCode_VXLAN, Action: pb.Action_DELETE, Data: &pb.Operation_Vxlan{Vxlan: &pb.Vxlan{Name: "vx1", Vni: 10100, Local: "192.0.2.1", Port: 4789}}}
	ctx := context.Background()
	undo, err := d.Prepare(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	kernel.present = false
	if err = d.Rollback(ctx, op, undo); err != nil {
		t.Fatal(err)
	}
	if !kernel.present {
		t.Fatal("deleted mirror not restored")
	}
}
