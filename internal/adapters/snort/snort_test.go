package snort

import (
	"context"
	"encoding/json"
	"errors"
	pb "sdn-agent/api/agent/v1"
	"testing"
)

type fake struct {
	calls []string
	fail  string
}

func (f *fake) Call(_ context.Context, m string, data, out any) error {
	f.calls = append(f.calls, m)
	if m == f.fail {
		return errors.New("reload failed")
	}
	if out != nil {
		return json.Unmarshal([]byte("{\"rules\":{},\"interfaces\":{}}"), out)
	}
	return nil
}
func rule() *pb.Operation {
	return &pb.Operation{Id: "rule", Code: pb.OpCode_IPS_RULE, Action: pb.Action_CREATE, Data: &pb.Operation_IpsRule{IpsRule: &pb.IpsRule{Sid: 1000001, Rule: "drop icmp any any -> any any (sid:1000001;)"}}}
}
func TestTransactionalRuntimeErrorsAndUndo(t *testing.T) {
	f := &fake{}
	d := &Driver{RPC: f}
	ctx := context.Background()
	op := rule()
	undo, e := d.Prepare(ctx, op)
	if e != nil || len(undo.Data) == 0 {
		t.Fatal(undo, e)
	}
	f.fail = "apply"
	if d.Apply(ctx, op) == nil {
		t.Fatal("failed reload was reported as successful")
	}
	if e = d.Rollback(ctx, op, undo); e != nil {
		t.Fatal(e)
	}
	if f.calls[len(f.calls)-1] != "restore" {
		t.Fatal(f.calls)
	}
	f.fail = "verify"
	if d.Verify(ctx, op) == nil {
		t.Fatal("readback failure was hidden")
	}
}
func TestValidationAndCapabilities(t *testing.T) {
	d := &Driver{}
	op := rule()
	op.GetIpsRule().Rule += "\ninclude secret.rules"
	if d.Validate(op) == nil {
		t.Fatal("accepted multiline rule")
	}
	op.Action = pb.Action_DELETE
	op.GetIpsRule().Rule = ""
	if e := d.Validate(op); e != nil {
		t.Fatal(e)
	}
	op.GetIpsRule().Sid = 0
	if d.Validate(op) == nil {
		t.Fatal("accepted zero SID")
	}
	if len(d.QueryCapabilities()) != 2 {
		t.Fatal("missing query capabilities")
	}
	if d.Rollback(context.Background(), op, nil) == nil {
		t.Fatal("missing undo accepted")
	}
}
