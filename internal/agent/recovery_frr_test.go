package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/frr"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
)

type transientFRRFailure struct {
	backend.Driver
	verifyFailure, undoFailure atomic.Bool
}

func (d *transientFRRFailure) Verify(ctx context.Context, op *pb.Operation) error {
	if e := d.Driver.Verify(ctx, op); e != nil {
		return e
	}
	if op.Code == pb.OpCode_BGP_EVPN_GLOBAL && d.verifyFailure.CompareAndSwap(true, false) {
		return fmt.Errorf("injected failure after actual FRR config/readback")
	}
	return nil
}
func (d *transientFRRFailure) Rollback(ctx context.Context, op *pb.Operation, undo *model.Undo) error {
	if d.undoFailure.CompareAndSwap(true, false) {
		return fmt.Errorf("injected transient FRR Undo unavailability")
	}
	return d.Driver.Rollback(ctx, op, undo)
}

// Executed only inside the disposable, network-isolated FRR container created
// by sdn-controller/lab/reliability.py, never against an existing lab node.
func TestRealFRRRollbackRetry(t *testing.T) {
	if os.Getenv("SDNA_REAL_FRR_RECOVERY") != "1" {
		t.Skip("requires disposable FRR recovery fixture")
	}
	ctx := context.Background()
	runner := backend.Exec{}
	before, e := runner.Run(ctx, "vtysh", "-c", "show running-config")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(before, "router bgp ") {
		t.Fatal("fixture must have no existing BGP instance")
	}
	d := &transientFRRFailure{Driver: &frr.Driver{Runner: runner}}
	d.verifyFailure.Store(true)
	d.undoFailure.Store(true)
	a := newAgent(t, d)
	one := commit(t, a,
		&pb.Operation{Id: "bgp", Code: pb.OpCode_BGP_INSTANCE, Action: pb.Action_CREATE, Data: &pb.Operation_Bgp{Bgp: &pb.Bgp{Asn: 65123, RouterId: "192.0.2.123"}}},
		&pb.Operation{Id: "evpn", Code: pb.OpCode_BGP_EVPN_GLOBAL, Action: pb.Action_SET, DependsOn: []string{"bgp"}, Data: &pb.Operation_EvpnGlobal{EvpnGlobal: &pb.EvpnGlobal{Asn: 65123, AdvertiseAllVni: boolPtr(true)}}},
	)
	a.Start()
	failed := waitTask(t, a, one.Id)
	if failed.State != "ROLLBACK_FAILED" {
		t.Fatal(failed)
	}
	a.Close()
	current, e := runner.Run(ctx, "vtysh", "-c", "show running-config")
	if e != nil || !strings.Contains(current, "router bgp 65123") {
		t.Fatal("real config not present before retry", e)
	}
	q := &pb.RetryRollbackRequest{NodeId: a.NodeID, TaskId: one.Id, RequestId: "real-frr-recovery", ExpectedSequence: failed.Sequence}
	if _, e = a.RetryRollback(ctx, q); e != nil {
		t.Fatal(e)
	}
	restarted, e := New(a.NodeID, a.Store, a.Registry)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(restarted.Close)
	restarted.Start()
	done := waitTask(t, restarted, one.Id)
	if done.State != "FAILED_ROLLED_BACK" || restarted.Revision(ctx) != 0 {
		t.Fatal(done)
	}
	after, e := runner.Run(ctx, "vtysh", "-c", "show running-config")
	if e != nil || strings.Contains(after, "router bgp ") {
		t.Fatal("actual FRR baseline not restored", after, e)
	}
}

func boolPtr(v bool) *bool { return &v }
