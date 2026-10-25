package agent

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
	"sdn-agent/internal/store"
)

// RetryRollback durably schedules another Undo attempt. It cannot accept an
// operator assertion that the dataplane is fixed, or replay forward operations.
func (a *Agent) RetryRollback(ctx context.Context, q *pb.RetryRollbackRequest) (*pb.Task, error) {
	if e := a.check(q.NodeId); e != nil {
		return nil, e
	}
	if q.RequestId == "" || q.TaskId == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id and task_id required")
	}
	// An accepted retry can be queried while its Undo is running.
	var existing receipt
	if e := a.Store.Load(ctx, "rollback/"+q.RequestId, &existing); e == nil {
		if existing.Hash != hash(q) {
			return nil, status.Error(codes.AlreadyExists, "request_id conflict")
		}
		return a.GetTask(ctx, &pb.TaskRequest{NodeId: a.NodeID, TaskId: existing.ID})
	} else if !errors.Is(e, store.ErrNotFound) {
		return nil, loadError(e)
	}
	if !a.executionMu.TryLock() {
		return nil, status.Error(codes.Unavailable, "node worker busy; retry same request")
	}
	defer a.executionMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	key := "rollback/" + q.RequestId
	var rc receipt
	if e := a.Store.Load(ctx, key, &rc); e == nil {
		if rc.Hash != hash(q) {
			return nil, status.Error(codes.AlreadyExists, "request_id conflict")
		}
		return a.GetTask(ctx, &pb.TaskRequest{NodeId: a.NodeID, TaskId: rc.ID})
	} else if !errors.Is(e, store.ErrNotFound) {
		return nil, loadError(e)
	}
	var r model.Record
	if e := a.Store.Load(ctx, "task/"+q.TaskId, &r); e != nil {
		return nil, loadError(e)
	}
	if r.Task.State != "ROLLBACK_FAILED" || r.Task.Sequence != q.ExpectedSequence {
		return nil, status.Error(codes.FailedPrecondition, "expected ROLLBACK_FAILED task and current sequence")
	}
	var run model.Running
	if e := a.Store.Load(ctx, "running", &run); e != nil {
		return nil, loadError(e)
	}
	if r.Before == nil || run.Revision != r.Before.Revision || run.Revision != q.ExpectedRevision {
		return nil, status.Error(codes.FailedPrecondition, "running revision changed; rollback requires baseline reconciliation")
	}
	unrestored := false
	for i := len(r.Plan) - 1; i >= 0; i-- {
		s := r.Plan[i]
		if !s.Started {
			continue
		}
		if !s.Restored {
			unrestored = true
		}
		if s.Restored && unrestored {
			return nil, status.Error(codes.FailedPrecondition, "legacy rollback crossed a failed dependency; explicit reconciliation required")
		}
		if !s.Restored && (s.Undo == nil || a.Registry.Driver(s.Backend) == nil) {
			return nil, status.Error(codes.FailedPrecondition, "saved Undo or backend unavailable")
		}
	}
	if !unrestored {
		return nil, status.Error(codes.FailedPrecondition, "no failed Undo to retry")
	}
	r.Task.State = "ROLLING_BACK"
	r.Task.Sequence++
	if e := a.Store.Atomic(ctx, map[string]any{
		"task/" + r.Task.Id: &r,
		fmt.Sprintf("event/%s/%d", r.Task.Id, r.Task.Sequence): r.Task,
		key: receipt{hash(q), r.Task.Id},
	}); e != nil {
		return nil, loadError(e)
	}
	a.kick()
	return r.Task, nil
}
