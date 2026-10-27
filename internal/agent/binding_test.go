package agent

import (
	"context"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/linux"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
	"sdn-agent/internal/store"
	"testing"
)

func TestDataplaneBindingCannotReinterpretLegacyResources(t *testing.T) {
	ctx := context.Background()
	s, _ := store.NewFile("")
	r, _ := backend.New(&linux.Driver{})
	if e := s.Atomic(ctx, map[string]any{"running": &model.Running{Operations: []*model.Op{{Operation: &pb.Operation{Code: pb.OpCode_BD}}}}}); e != nil {
		t.Fatal(e)
	}
	if CheckDataplaneBinding(ctx, s, "vpp", r) == nil {
		t.Fatal("legacy Linux journal reinterpreted as VPP")
	}
	if e := CheckDataplaneBinding(ctx, s, "linux", r); e != nil {
		t.Fatal(e)
	}
}
func TestDataplaneBindingChecksRecoveryDependencies(t *testing.T) {
	ctx := context.Background()
	s, _ := store.NewFile("")
	r, _ := backend.New(&linux.Driver{})
	rec := &model.Record{Task: &pb.Task{Id: "partial", State: "ROLLBACK_FAILED"}, Plan: []*model.Step{{Backend: "vpp"}}}
	_ = s.Atomic(ctx, map[string]any{"task/partial": rec})
	if CheckDataplaneBinding(ctx, s, "linux", r) == nil {
		t.Fatal("disabled recovery backend accepted")
	}
}
func TestDataplaneBindingPersistsAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/state.json"
	s, _ := store.NewFile(path)
	r, _ := backend.New(&linux.Driver{})
	ctx := context.Background()
	if e := CheckDataplaneBinding(ctx, s, "linux", r); e != nil {
		t.Fatal(e)
	}
	reopened, _ := store.NewFile(path)
	if CheckDataplaneBinding(ctx, reopened, "vpp", r) == nil {
		t.Fatal("restart bypassed ownership guard")
	}
}
