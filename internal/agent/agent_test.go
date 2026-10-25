package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
	"sdn-agent/internal/store"
	"sdn-agent/internal/transport"
)

type fakeDriver struct {
	mu                              sync.Mutex
	name                            string
	values                          map[string]string
	failApply, failVerify, failUndo string
	log                             *[]string
	logmu                           *sync.Mutex
}

func fake(n string) *fakeDriver          { return &fakeDriver{name: n, values: map[string]string{}} }
func (d *fakeDriver) Name() string       { return d.name }
func (d *fakeDriver) Codes() []pb.OpCode { return []pb.OpCode{pb.OpCode_BD} }
func (d *fakeDriver) Validate(o *pb.Operation) error {
	if o.GetBridge() == nil {
		return fmt.Errorf("bridge data required")
	}
	return nil
}
func (d *fakeDriver) Prepare(ctx context.Context, o *pb.Operation) (*model.Undo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, _ := json.Marshal(d.values[o.Id])
	return &model.Undo{Data: b}, nil
}
func (d *fakeDriver) note(s string) {
	if d.log != nil {
		d.logmu.Lock()
		*d.log = append(*d.log, s)
		d.logmu.Unlock()
	}
}
func (d *fakeDriver) Apply(ctx context.Context, o *pb.Operation) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.values[o.Id] = "new"
	d.note("apply/" + d.name + "/" + o.Id)
	if o.Id == d.failApply {
		return fmt.Errorf("partial apply failure")
	}
	return nil
}
func (d *fakeDriver) Verify(ctx context.Context, o *pb.Operation) error {
	if o.Id == d.failVerify {
		return fmt.Errorf("readback failure")
	}
	return nil
}
func (d *fakeDriver) Rollback(ctx context.Context, o *pb.Operation, u *model.Undo) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.note("undo/" + d.name + "/" + o.Id)
	if o.Id == d.failUndo {
		return fmt.Errorf("undo failure")
	}
	var old string
	if e := json.Unmarshal(u.Data, &old); e != nil {
		return e
	}
	d.values[o.Id] = old
	return nil
}
func (d *fakeDriver) Observe(ctx context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (d *fakeDriver) value(id string) string { d.mu.Lock(); defer d.mu.Unlock(); return d.values[id] }
func op(id string, deps ...string) *pb.Operation {
	return &pb.Operation{Id: id, Code: pb.OpCode_BD, Action: pb.Action_CREATE, DependsOn: deps, Data: &pb.Operation_Bridge{Bridge: &pb.Bridge{Name: id}}}
}
func newAgent(t *testing.T, ds ...backend.Driver) *Agent {
	t.Helper()
	s, e := store.NewFile(filepath.Join(t.TempDir(), "journal.json"))
	if e != nil {
		t.Fatal(e)
	}
	r, e := backend.New(ds...)
	if e != nil {
		t.Fatal(e)
	}
	a, e := New("node", s, r)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	return a
}
func commit(t *testing.T, a *Agent, ops ...*pb.Operation) *pb.Task {
	t.Helper()
	ctx := context.Background()
	tx, e := a.Begin(ctx, &pb.BeginRequest{NodeId: a.NodeID, BaseRevision: a.Revision(ctx), RequestId: model.ID()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Stage(ctx, &pb.StageRequest{NodeId: a.NodeID, TransactionId: tx.Id, Operations: ops}); e != nil {
		t.Fatal(e)
	}
	task, e := a.Commit(ctx, &pb.CommitRequest{NodeId: a.NodeID, TransactionId: tx.Id, RequestId: model.ID()})
	if e != nil {
		t.Fatal(e)
	}
	return task
}
func waitTask(t *testing.T, a *Agent, id string) *pb.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, e := a.GetTask(context.Background(), &pb.TaskRequest{NodeId: a.NodeID, TaskId: id})
		if e != nil {
			t.Fatal(e)
		}
		if model.Terminal(r.State) {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task timeout")
	return nil
}
func TestAllBackendsAndDependencyOrder(t *testing.T) {
	d1, d2 := fake("linux"), fake("frr")
	var log []string
	var mu sync.Mutex
	d1.log, d2.log = &log, &log
	d1.logmu, d2.logmu = &mu, &mu
	a := newAgent(t, d1, d2)
	task := commit(t, a, op("b", "a"), op("a"))
	if d1.value("a") != "" {
		t.Fatal("applied before worker")
	}
	a.Start()
	done := waitTask(t, a, task.Id)
	if done.State != "SUCCEEDED" || done.Revision != 1 {
		t.Fatal(done)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "[apply/linux/a apply/frr/a apply/linux/b apply/frr/b]"
	if fmt.Sprint(log) != want {
		t.Fatalf("%v", log)
	}
}
func TestPartialFailureAndVerifyFailureRollback(t *testing.T) {
	for _, kind := range []string{"apply", "verify"} {
		t.Run(kind, func(t *testing.T) {
			d := fake("linux")
			d.values["a"] = "original"
			if kind == "apply" {
				d.failApply = "b"
			} else {
				d.failVerify = "b"
			}
			a := newAgent(t, d)
			task := commit(t, a, op("a"), op("b", "a"))
			a.Start()
			r := waitTask(t, a, task.Id)
			if r.State != "FAILED_ROLLED_BACK" || d.value("a") != "original" || d.value("b") != "" || a.Revision(context.Background()) != 0 {
				t.Fatal(r)
			}
		})
	}
}
func TestRollbackFailureBlocksLaterTasks(t *testing.T) {
	d := fake("linux")
	d.failApply = "b"
	d.failUndo = "b"
	a := newAgent(t, d)
	one := commit(t, a, op("a"), op("b", "a"))
	two := commit(t, a, op("c"))
	a.Start()
	if r := waitTask(t, a, one.Id); r.State != "ROLLBACK_FAILED" {
		t.Fatal(r)
	}
	time.Sleep(50 * time.Millisecond)
	r, e := a.GetTask(context.Background(), &pb.TaskRequest{NodeId: "node", TaskId: two.Id})
	if e != nil || r.State != "ACCEPTED" || d.value("c") != "" {
		t.Fatal(r, e)
	}
}
func TestRevisionConflictAndCommitIdempotency(t *testing.T) {
	d := fake("linux")
	a := newAgent(t, d)
	one := commit(t, a, op("a"))
	two := commit(t, a, op("b"))
	q := &pb.CommitRequest{NodeId: "node", TransactionId: one.TransactionId, RequestId: "retry"}
	r, e := a.Commit(context.Background(), q)
	if e != nil || r.Id != one.Id {
		t.Fatal(r, e)
	}
	a.Start()
	if waitTask(t, a, one.Id).State != "SUCCEEDED" {
		t.Fatal("first task")
	}
	if waitTask(t, a, two.Id).ErrorCode != "REVISION_CONFLICT" {
		t.Fatal("lost stale revision check")
	}
}
func TestValidationAndNodeIsolation(t *testing.T) {
	a := newAgent(t, fake("linux"))
	ctx := context.Background()
	if _, e := a.Begin(ctx, &pb.BeginRequest{NodeId: "other", RequestId: "r"}); status.Code(e) != codes.PermissionDenied {
		t.Fatal(e)
	}
	tx, e := a.Begin(ctx, &pb.BeginRequest{NodeId: "node", RequestId: "r"})
	if e != nil {
		t.Fatal(e)
	}
	for _, ops := range [][]*pb.Operation{{op("a", "b"), op("b", "a")}, {op("a", "missing")}, {{Id: "x", Code: pb.OpCode_VXLAN, Action: pb.Action_CREATE}}} {
		if _, e = a.Stage(ctx, &pb.StageRequest{NodeId: "node", TransactionId: tx.Id, Operations: ops}); status.Code(e) != codes.InvalidArgument {
			t.Fatalf("expected invalid argument: %v", e)
		}
	}
	if _, e = a.Begin(ctx, &pb.BeginRequest{NodeId: "node", RequestId: "r", BaseRevision: 1}); status.Code(e) != codes.AlreadyExists {
		t.Fatal(e)
	}
	if _, e = a.Abort(ctx, &pb.AbortRequest{NodeId: "node", TransactionId: tx.Id}); e != nil {
		t.Fatal(e)
	}
}
func TestRestartCompensatesPersistedInFlightStep(t *testing.T) {
	d := fake("linux")
	d.values["a"] = "changed-before-crash"
	a := newAgent(t, d)
	o := op("a")
	before, _ := json.Marshal("original")
	task := &pb.Task{Id: "crashed", NodeId: "node", TransactionId: "tx", State: "APPLYING", Sequence: 1, Steps: []*pb.StepResult{{Id: "a/linux", Backend: "linux", OperationId: "a", State: "APPLYING"}}}
	record := &model.Record{Task: task, Before: &model.Running{}, Plan: []*model.Step{{ID: "a/linux", Backend: "linux", Operation: &model.Op{Operation: o}, Undo: &model.Undo{Data: before}, Started: true}}, Created: 1}
	if e := a.Store.Atomic(context.Background(), map[string]any{"task/crashed": record, "event/crashed/1": task}); e != nil {
		t.Fatal(e)
	}
	a.Start()
	r := waitTask(t, a, "crashed")
	if r.State != "FAILED_ROLLED_BACK" || d.value("a") != "original" {
		t.Fatal(r)
	}
}
func TestGRPCAuthenticationEventReplayAndDetachedCommit(t *testing.T) {
	d := fake("linux")
	a := newAgent(t, d)
	token := "test-token-at-least-16"
	s, e := transport.Server(token, "", "", true)
	if e != nil {
		t.Fatal(e)
	}
	pb.RegisterAgentServer(s, a)
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go s.Serve(l)
	t.Cleanup(s.Stop)
	conn, e := transport.Dial(l.Addr().String(), token, "", true)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	client := pb.NewAgentClient(conn)
	bad, e := transport.Dial(l.Addr().String(), "wrong", "", true)
	if e != nil {
		t.Fatal(e)
	}
	defer bad.Close()
	if _, e = pb.NewAgentClient(bad).GetCapabilities(context.Background(), &pb.NodeRequest{}); status.Code(e) != codes.Unauthenticated {
		t.Fatal(e)
	}
	task := commit(t, a, op("a"))
	a.SetNodeState("DOWN")
	a.Start()
	done := waitTask(t, a, task.Id)
	if done.State != "SUCCEEDED" {
		t.Fatal(done)
	}
	stream, e := client.WatchTasks(context.Background(), &pb.TaskRequest{NodeId: "node", TaskId: task.Id, AfterSequence: 1})
	if e != nil {
		t.Fatal(e)
	}
	seq := uint64(1)
	for {
		r, e := stream.Recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		seq++
		if r.Sequence != seq {
			t.Fatal("replay gap")
		}
	}
	if seq != done.Sequence {
		t.Fatal("missing final event")
	}
}
