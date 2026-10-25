package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
	"sdn-agent/internal/store"
)

type Agent struct {
	pb.UnimplementedAgentServer
	NodeID, InstanceID string
	Store              store.Store
	Registry           *backend.Registry
	mu                 sync.Mutex
	executionMu        sync.Mutex
	nodeState          string
	wake               chan struct{}
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
}

func New(node string, s store.Store, r *backend.Registry) (*Agent, error) {
	a := &Agent{NodeID: node, InstanceID: model.ID(), Store: s, Registry: r, nodeState: "INIT", wake: make(chan struct{}, 1)}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	var run model.Running
	if e := s.Load(a.ctx, "running", &run); errors.Is(e, store.ErrNotFound) {
		if e = s.Atomic(a.ctx, map[string]any{"running": &model.Running{}}); e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	}
	return a, nil
}
func (a *Agent) Start()                { a.wg.Add(1); go a.worker(); a.kick() }
func (a *Agent) Close()                { a.cancel(); a.wg.Wait() }
func (a *Agent) SetNodeState(s string) { a.mu.Lock(); a.nodeState = s; a.mu.Unlock() }
func (a *Agent) Revision(ctx context.Context) uint64 {
	var r model.Running
	if a.Store.Load(ctx, "running", &r) != nil {
		return 0
	}
	return r.Revision
}
func (a *Agent) check(node string) error {
	if node != a.NodeID {
		return status.Error(codes.PermissionDenied, "node_id mismatch")
	}
	return nil
}
func (a *Agent) kick() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
func loadError(e error) error {
	if errors.Is(e, store.ErrNotFound) {
		return status.Error(codes.NotFound, e.Error())
	}
	return status.Error(codes.Internal, e.Error())
}
func hash(p proto.Message) string {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(p)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type receipt struct{ Hash, ID string }

func (a *Agent) Begin(ctx context.Context, q *pb.BeginRequest) (*pb.Transaction, error) {
	if e := a.check(q.NodeId); e != nil {
		return nil, e
	}
	if q.RequestId == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var rec receipt
	key := "begin/" + q.RequestId
	if e := a.Store.Load(ctx, key, &rec); e == nil {
		if rec.Hash != hash(q) {
			return nil, status.Error(codes.AlreadyExists, "request_id reused with different request")
		}
		var t pb.Transaction
		if e = a.Store.Load(ctx, "candidate/"+rec.ID, &t); e != nil {
			return nil, loadError(e)
		}
		return &t, nil
	} else if !errors.Is(e, store.ErrNotFound) {
		return nil, loadError(e)
	}
	var run model.Running
	if e := a.Store.Load(ctx, "running", &run); e != nil {
		return nil, loadError(e)
	}
	if q.BaseRevision != run.Revision {
		return nil, status.Error(codes.FailedPrecondition, "base revision mismatch")
	}
	t := &pb.Transaction{Id: model.ID(), NodeId: a.NodeID, BaseRevision: q.BaseRevision}
	if e := a.Store.Atomic(ctx, map[string]any{"candidate/" + t.Id: t, key: receipt{hash(q), t.Id}}); e != nil {
		return nil, loadError(e)
	}
	return t, nil
}
func (a *Agent) Stage(ctx context.Context, q *pb.StageRequest) (*pb.Transaction, error) {
	if e := a.check(q.NodeId); e != nil {
		return nil, e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var t pb.Transaction
	if e := a.Store.Load(ctx, "candidate/"+q.TransactionId, &t); e != nil {
		return nil, loadError(e)
	}
	if t.Frozen {
		return nil, status.Error(codes.FailedPrecondition, "transaction frozen")
	}
	ops := map[string]*pb.Operation{}
	for _, o := range t.Operations {
		ops[o.Id] = o
	}
	for _, o := range q.Operations {
		if o == nil || o.Id == "" {
			return nil, status.Error(codes.InvalidArgument, "operation id required")
		}
		ops[o.Id] = o
	}
	t.Operations = nil
	for _, o := range ops {
		t.Operations = append(t.Operations, o)
	}
	sort.Slice(t.Operations, func(i, j int) bool { return t.Operations[i].Id < t.Operations[j].Id })
	if _, e := a.Registry.Plan(t.Operations); e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	if e := a.Store.Atomic(ctx, map[string]any{"candidate/" + t.Id: &t}); e != nil {
		return nil, loadError(e)
	}
	return &t, nil
}
func (a *Agent) Commit(ctx context.Context, q *pb.CommitRequest) (*pb.Task, error) {
	if e := a.check(q.NodeId); e != nil {
		return nil, e
	}
	if q.RequestId == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := "commit/" + q.RequestId
	var rc receipt
	if e := a.Store.Load(ctx, key, &rc); e == nil {
		if rc.Hash != hash(q) {
			return nil, status.Error(codes.AlreadyExists, "request_id conflict")
		}
		return a.GetTask(ctx, &pb.TaskRequest{NodeId: a.NodeID, TaskId: rc.ID})
	} else if !errors.Is(e, store.ErrNotFound) {
		return nil, loadError(e)
	}
	var t pb.Transaction
	if e := a.Store.Load(ctx, "candidate/"+q.TransactionId, &t); e != nil {
		return nil, loadError(e)
	}
	if t.Frozen {
		return a.GetTask(ctx, &pb.TaskRequest{NodeId: a.NodeID, TaskId: t.TaskId})
	}
	if len(t.Operations) == 0 {
		return nil, status.Error(codes.InvalidArgument, "empty transaction")
	}
	plan, e := a.Registry.Plan(t.Operations)
	if e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	var run model.Running
	if e = a.Store.Load(ctx, "running", &run); e != nil {
		return nil, loadError(e)
	}
	if run.Revision != t.BaseRevision {
		return nil, status.Error(codes.FailedPrecondition, "base revision mismatch")
	}
	id := model.ID()
	task := &pb.Task{Id: id, NodeId: a.NodeID, TransactionId: t.Id, State: "ACCEPTED", Sequence: 1, Revision: run.Revision}
	for _, s := range plan {
		task.Steps = append(task.Steps, &pb.StepResult{Id: s.ID, Backend: s.Backend, OperationId: s.Operation.Id, State: "PENDING"})
	}
	ordered, _ := model.Order(t.Operations)
	rec := &model.Record{Task: task, Plan: plan, Before: &run, Operations: model.Wrap(ordered), Created: time.Now().UnixNano()}
	t.Frozen = true
	t.TaskId = id
	if e = a.Store.Atomic(ctx, map[string]any{"candidate/" + t.Id: &t, "task/" + id: rec, "event/" + id + "/1": task, key: receipt{hash(q), id}}); e != nil {
		return nil, loadError(e)
	}
	a.kick()
	return task, nil
}
func (a *Agent) Abort(ctx context.Context, q *pb.AbortRequest) (*pb.Empty, error) {
	if e := a.check(q.NodeId); e != nil {
		return nil, e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var t pb.Transaction
	if e := a.Store.Load(ctx, "candidate/"+q.TransactionId, &t); e != nil {
		return nil, loadError(e)
	}
	if t.Frozen {
		return nil, status.Error(codes.FailedPrecondition, "committed transaction cannot be discarded")
	}
	if e := a.Store.Atomic(ctx, map[string]any{"candidate/" + t.Id: nil}); e != nil {
		return nil, loadError(e)
	}
	return &pb.Empty{}, nil
}
func (a *Agent) GetTask(ctx context.Context, q *pb.TaskRequest) (*pb.Task, error) {
	if e := a.check(q.NodeId); e != nil {
		return nil, e
	}
	var r model.Record
	if e := a.Store.Load(ctx, "task/"+q.TaskId, &r); e != nil {
		return nil, loadError(e)
	}
	return r.Task, nil
}
func (a *Agent) WatchTasks(q *pb.TaskRequest, stream grpc.ServerStreamingServer[pb.Task]) error {
	if e := a.check(q.NodeId); e != nil {
		return e
	}
	seq := q.AfterSequence
	for {
		t, e := a.GetTask(stream.Context(), q)
		if e != nil {
			return e
		}
		if seq > t.Sequence {
			return status.Error(codes.InvalidArgument, "cursor ahead of task")
		}
		for seq < t.Sequence {
			seq++
			var event pb.Task
			if e = a.Store.Load(stream.Context(), fmt.Sprintf("event/%s/%d", q.TaskId, seq), &event); e != nil {
				return loadError(e)
			}
			if e = stream.Send(&event); e != nil {
				return e
			}
		}
		if model.Terminal(t.State) {
			return nil
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func (a *Agent) GetCapabilities(ctx context.Context, q *pb.NodeRequest) (*pb.Capabilities, error) {
	if q.NodeId != "" {
		if e := a.check(q.NodeId); e != nil {
			return nil, e
		}
	}
	return a.Registry.Capabilities(a.NodeID), nil
}
func (a *Agent) GetState(ctx context.Context, q *pb.NodeRequest) (*pb.State, error) {
	if e := a.check(q.NodeId); e != nil {
		return nil, e
	}
	var r model.Running
	if e := a.Store.Load(ctx, "running", &r); e != nil {
		return nil, loadError(e)
	}
	b, _ := json.Marshal(r)
	a.mu.Lock()
	state := a.nodeState
	a.mu.Unlock()
	out := &pb.State{NodeId: a.NodeID, InstanceId: a.InstanceID, NodeState: state, Revision: r.Revision, RunningJson: string(b)}
	var observationErr error
	out.Observations, observationErr = a.Registry.Observe(ctx)
	if observationErr != nil {
		out.ObservationError = observationErr.Error()
	}
	if e := a.Store.Atomic(ctx, map[string]any{"operational": out}); e != nil {
		out.ObservationError = e.Error()
	}
	return out, nil
}

func (a *Agent) save(r *model.Record, extra map[string]any) error {
	r.Task.Sequence++
	m := map[string]any{"task/" + r.Task.Id: r, fmt.Sprintf("event/%s/%d", r.Task.Id, r.Task.Sequence): r.Task}
	for k, v := range extra {
		m[k] = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.Store.Atomic(ctx, m)
}
func (a *Agent) worker() {
	defer a.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.wake:
		case <-ticker.C:
		}
		a.workOnce()
	}
}
func (a *Agent) workOnce() {
	a.executionMu.Lock()
	defer a.executionMu.Unlock()
	keys, e := a.Store.Keys(a.ctx, "task/")
	if e != nil {
		return
	}
	var pending []*model.Record
	blocked := false
	for _, k := range keys {
		var r model.Record
		if a.Store.Load(a.ctx, k, &r) != nil {
			return // Unknown journal contents may contain a rollback barrier.
		}
		if r.Task.State == "ROLLBACK_FAILED" {
			blocked = true
		}
		if !model.Terminal(r.Task.State) {
			pending = append(pending, &r)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Created < pending[j].Created })
	if blocked {
		return
	}
	for _, r := range pending {
		if a.ctx.Err() != nil {
			return
		}
		if r.Task.State != "ACCEPTED" {
			cause := r.Task.Error
			if cause == "" {
				cause = "agent restarted during transaction"
			}
			a.rollback(r, errors.New(cause))
		} else {
			a.execute(r)
		}
		if r.Task.State == "ROLLBACK_FAILED" {
			break
		}
	}
}
func (a *Agent) execute(r *model.Record) {
	var run model.Running
	if e := a.Store.Load(a.ctx, "running", &run); e != nil {
		return
	}
	if run.Revision != r.Before.Revision {
		r.Task.State = "FAILED_NO_CHANGE"
		r.Task.ErrorCode = "REVISION_CONFLICT"
		r.Task.Error = "base revision changed before execution"
		a.save(r, nil)
		return
	}
	r.Task.State = "APPLYING"
	if a.save(r, nil) != nil {
		return
	}
	for i, s := range r.Plan {
		d := a.Registry.Driver(s.Backend)
		if d == nil {
			a.rollback(r, fmt.Errorf("missing backend %s", s.Backend))
			return
		}
		u, e := d.Prepare(a.ctx, s.Operation.Operation)
		if e != nil {
			a.rollback(r, e)
			return
		}
		s.Undo = u
		s.Started = true
		r.Task.Steps[i].State = "APPLYING"
		if e = a.save(r, nil); e != nil {
			return
		}
		if e = d.Apply(a.ctx, s.Operation.Operation); e == nil {
			e = d.Verify(a.ctx, s.Operation.Operation)
		}
		if e != nil {
			r.Task.Steps[i].Error = e.Error()
			a.rollback(r, e)
			return
		}
		s.Done = true
		r.Task.Steps[i].State = "SUCCEEDED"
		if e = a.save(r, nil); e != nil {
			return
		}
	}
	run.Revision++
	run.Transactions = append(run.Transactions, r.Task.TransactionId)
	run.Operations = append(run.Operations, r.Operations...)
	r.Task.State = "SUCCEEDED"
	r.Task.Revision = run.Revision
	a.save(r, map[string]any{"running": &run})
}
func (a *Agent) rollback(r *model.Record, cause error) {
	r.Task.State = "ROLLING_BACK"
	r.Task.Error = cause.Error()
	r.Task.ErrorCode = "APPLY_FAILED"
	if a.save(r, nil) != nil {
		return
	}
	changed := false
	var failures []string
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for i := len(r.Plan) - 1; i >= 0; i-- {
		s := r.Plan[i]
		if !s.Started {
			continue
		}
		changed = true
		if s.Restored {
			continue
		}
		d := a.Registry.Driver(s.Backend)
		var e error
		if d == nil {
			e = fmt.Errorf("backend %s missing", s.Backend)
		} else {
			e = d.Rollback(ctx, s.Operation.Operation, s.Undo)
		}
		if e != nil {
			failures = append(failures, e.Error())
			r.Task.Steps[i].RollbackError = e.Error()
			r.Task.Steps[i].State = "ROLLBACK_FAILED"
		} else {
			s.Restored = true
			r.Task.Steps[i].State = "ROLLED_BACK"
			r.Task.Steps[i].RollbackError = ""
		}
		if a.save(r, nil) != nil {
			return
		}
		if e != nil {
			// Earlier Undo may depend on this step being restored first. Stop
			// here so retry resumes the same reverse dependency order.
			break
		}
	}
	if len(failures) > 0 {
		r.Task.State = "ROLLBACK_FAILED"
		r.Task.ErrorCode = "ROLLBACK_FAILED"
	} else if changed {
		r.Task.State = "FAILED_ROLLED_BACK"
	} else {
		r.Task.State = "FAILED_NO_CHANGE"
	}
	a.save(r, nil)
}
