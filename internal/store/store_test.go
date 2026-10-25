package store

import (
	"context"
	"errors"
	"path/filepath"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
	"testing"
)

func TestAtomicDurabilityAndOneofRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.json")
	s, e := NewFile(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	r := &model.Record{Operations: []*model.Op{{Operation: &pb.Operation{Id: "vx", Code: pb.OpCode_VXLAN, Data: &pb.Operation_Vxlan{Vxlan: &pb.Vxlan{Vni: 50000, Name: "vx50000"}}}}}}
	if e = s.Atomic(ctx, map[string]any{"journal": r, "remove": "value"}); e != nil {
		t.Fatal(e)
	}
	if e = s.Atomic(ctx, map[string]any{"remove": nil}); e != nil {
		t.Fatal(e)
	}
	reopened, e := NewFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var out model.Record
	if e = reopened.Load(ctx, "journal", &out); e != nil {
		t.Fatal(e)
	}
	if out.Operations[0].GetVxlan().Vni != 50000 {
		t.Fatal("oneof lost")
	}
	var str string
	if e = reopened.Load(ctx, "remove", &str); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}
