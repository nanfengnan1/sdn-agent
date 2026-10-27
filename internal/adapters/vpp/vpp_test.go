package vpp

import (
	"context"
	"errors"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/frr"
	"sdn-agent/internal/adapters/linux"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
	"strings"
	"testing"
)

type failedRPC struct{}

func (failedRPC) Call(context.Context, string, any, any) error { return errors.New("API disconnected") }
func TestSelectedRegistryAndQueryCapabilities(t *testing.T) {
	d := &Driver{Linux: &linux.Driver{}, RPC: failedRPC{}}
	r, e := backend.New(d, &RoutingDriver{Driver: &frr.Driver{}})
	if e != nil {
		t.Fatal(e)
	}
	c := r.Capabilities("leaf")
	seen := map[pb.OpCode]bool{}
	for _, op := range c.Operations {
		seen[op.Code] = true
		if op.Code == pb.OpCode_BD && (len(op.Backends) != 1 || op.Backends[0] != "vpp") {
			t.Fatalf("duplicate BD writer: %v", op)
		}
	}
	for _, code := range []pb.OpCode{pb.OpCode_TENANT, pb.OpCode_BD_TENANT, pb.OpCode_TENANT_L3VNI, pb.OpCode_EVPN_TYPE5} {
		if !seen[code] {
			t.Fatalf("missing IRB capability %s", code)
		}
	}
	_, source, e := r.Query(context.Background(), &pb.QueryRequest{Kind: pb.QueryKind_FDB})
	if source != "vpp" || e == nil {
		t.Fatalf("query must use VPP and preserve error: %s %v", source, e)
	}
}
func TestUnsupportedForwardingFeaturesRejected(t *testing.T) {
	d := &Driver{Linux: &linux.Driver{}}
	yes := true
	for _, op := range []*pb.Operation{
		{Code: pb.OpCode_BD, Action: pb.Action_CREATE, Data: &pb.Operation_Bridge{Bridge: &pb.Bridge{Name: "br100", Stp: &yes}}},
	} {
		if d.Validate(op) == nil {
			t.Fatalf("unsupported feature accepted: %v", op)
		}
	}
	f := &RoutingDriver{Driver: &frr.Driver{}}
	if e := f.Validate(&pb.Operation{Code: pb.OpCode_BGP_INSTANCE, Action: pb.Action_CREATE, Data: &pb.Operation_Bgp{Bgp: &pb.Bgp{Asn: 65000, Vrf: "blue", RouterId: "192.0.2.1"}}}); e != nil {
		t.Fatalf("tenant BGP rejected: %v", e)
	}
	if e := f.Validate(&pb.Operation{Code: pb.OpCode_BGP_NEIGHBOR, Action: pb.Action_CREATE, Data: &pb.Operation_Peer{Peer: &pb.Peer{Asn: 65000, Vrf: "blue", Name: "192.0.2.2", RemoteAs: 65001}}}); e == nil {
		t.Fatal("tenant protocol session accepted without BVI punt channel")
	}
}
func TestRollbackRejectsIncompleteOrUnavailableUndo(t *testing.T) {
	d := &Driver{Linux: &linux.Driver{}, RPC: failedRPC{}}
	for _, u := range []*model.Undo{nil, {Data: []byte("{}")}, {Data: []byte("bad")}} {
		if d.Rollback(context.Background(), &pb.Operation{}, u) == nil {
			t.Fatal("invalid Undo accepted")
		}
	}
	e := d.Rollback(context.Background(), &pb.Operation{}, &model.Undo{Data: []byte("{\"linux\":{},\"vpp\":{\"bridges\":{},\"ports\":{},\"vxlans\":{}}}")})
	if e == nil || !strings.Contains(e.Error(), "disconnected") {
		t.Fatalf("API failure hidden: %v", e)
	}
}
