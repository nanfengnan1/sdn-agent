package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
)

func TestRollbackRetrySurvivesRestartAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	d := fake("linux")
	d.failApply, d.failUndo = "b", "b"
	d.values["a"] = "original"
	var log []string
	var logMu sync.Mutex
	d.log, d.logmu = &log, &logMu
	a := newAgent(t, d)
	one := commit(t, a, op("a"), op("b", "a"))
	a.Start()
	failed := waitTask(t, a, one.Id)
	if failed.State != "ROLLBACK_FAILED" {
		t.Fatal(failed)
	}
	a.Close()
	if d.value("a") != "new" {
		t.Fatal("rollback crossed a failed dependency")
	}
	q := &pb.RetryRollbackRequest{NodeId: "node", TaskId: one.Id, RequestId: "repair-1", ExpectedSequence: failed.Sequence, ExpectedRevision: 0}
	q.ExpectedSequence--
	if _, e := a.RetryRollback(ctx, q); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("stale sequence accepted: %v", e)
	}
	q.ExpectedSequence++
	q.ExpectedRevision = 7
	if _, e := a.RetryRollback(ctx, q); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("stale baseline accepted: %v", e)
	}
	q.ExpectedRevision = 0
	d.mu.Lock()
	d.failUndo = ""
	d.mu.Unlock()
	accepted, e := a.RetryRollback(ctx, q)
	if e != nil || accepted.State != "ROLLING_BACK" {
		t.Fatal(accepted, e)
	}
	// No worker runs between intent persistence and this simulated restart.
	restarted, e := New("node", a.Store, a.Registry)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(restarted.Close)
	restarted.Start()
	done := waitTask(t, restarted, one.Id)
	if done.State != "FAILED_ROLLED_BACK" || restarted.Revision(ctx) != 0 || d.value("a") != "original" || d.value("b") != "" {
		t.Fatal(done)
	}
	for _, step := range done.Steps {
		if step.RollbackError != "" {
			t.Fatal("stale rollback error", step)
		}
	}
	replayed, e := restarted.RetryRollback(ctx, q)
	if e != nil || replayed.Sequence != done.Sequence {
		t.Fatal("retry was not idempotent", e)
	}
	var historical pb.Task
	if e = a.Store.Load(ctx, fmt.Sprintf("event/%s/%d", one.Id, failed.Sequence), &historical); e != nil || historical.State != "ROLLBACK_FAILED" {
		t.Fatal("original failure event lost", e)
	}
	logMu.Lock()
	history := strings.Join(log, ",")
	logMu.Unlock()
	if strings.Count(history, "apply/linux/b") != 1 || !strings.HasSuffix(history, "undo/linux/b,undo/linux/a") {
		t.Fatal("forward replay or invalid Undo order", history)
	}
	q.ExpectedRevision++
	if _, e = restarted.RetryRollback(ctx, q); status.Code(e) != codes.AlreadyExists {
		t.Fatal("request payload conflict accepted", e)
	}
	two := commit(t, restarted, op("c"))
	if waitTask(t, restarted, two.Id).State != "SUCCEEDED" {
		t.Fatal("recovered node remained blocked")
	}
}

func TestRollbackRetryRejectsUnsafeLegacyHistory(t *testing.T) {
	a := newAgent(t, fake("linux"))
	ctx := context.Background()
	task := commit(t, a, op("a"), op("b", "a"))
	var record model.Record
	if e := a.Store.Load(ctx, "task/"+task.Id, &record); e != nil {
		t.Fatal(e)
	}
	record.Task.State = "ROLLBACK_FAILED"
	record.Plan[0].Started, record.Plan[0].Restored = true, true
	record.Plan[1].Started = true
	record.Plan[1].Undo = &model.Undo{}
	if e := a.Store.Atomic(ctx, map[string]any{"task/" + task.Id: &record}); e != nil {
		t.Fatal(e)
	}
	_, e := a.RetryRollback(ctx, &pb.RetryRollbackRequest{NodeId: "node", TaskId: task.Id, RequestId: "unsafe", ExpectedSequence: task.Sequence})
	if status.Code(e) != codes.FailedPrecondition || !strings.Contains(e.Error(), "legacy") {
		t.Fatal("unsafe history retried", e)
	}
}
