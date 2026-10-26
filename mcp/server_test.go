package sdnmcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	pb "sdn-agent/api/agent/v1"
	sdnmcp "sdn-agent/mcp"
)

const testNode = "mcp-test-node"
const largeRevision = "18446744073709551615"

type recordedRPC struct {
	method  string
	request proto.Message
}

// The fake is behind a real gRPC connection: tests exercise protobuf conversion,
// contexts and stream lifetimes in addition to the MCP wire protocol.
type testAgent struct {
	pb.UnimplementedAgentServer
	mu    sync.Mutex
	calls []recordedRPC
	hooks map[string]func(context.Context, proto.Message) (proto.Message, error)
	watch func(*pb.TaskRequest, grpc.ServerStreamingServer[pb.Task]) error
}

func (a *testAgent) invoke(ctx context.Context, method string, q, fallback proto.Message) (proto.Message, error) {
	a.mu.Lock()
	a.calls = append(a.calls, recordedRPC{method, proto.Clone(q)})
	hook := a.hooks[method]
	a.mu.Unlock()
	if hook != nil {
		return hook(ctx, q)
	}
	return fallback, nil
}

func (a *testAgent) requests(method string) []proto.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []proto.Message
	for _, c := range a.calls {
		if c.method == method {
			out = append(out, proto.Clone(c.request))
		}
	}
	return out
}

func (a *testAgent) writeMethods() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, c := range a.calls {
		switch c.method {
		case "Begin", "Stage", "Commit", "Abort", "RetryRollback":
			out = append(out, c.method)
		}
	}
	return out
}

func (a *testAgent) GetCapabilities(ctx context.Context, q *pb.NodeRequest) (*pb.Capabilities, error) {
	r, err := a.invoke(ctx, "GetCapabilities", q, &pb.Capabilities{NodeId: testNode, ProtocolVersion: 1, Operations: []*pb.Capability{{Code: pb.OpCode_BD, Backends: []string{"linux"}, Rollback: true}}, Queries: []*pb.QueryCapability{{Kind: pb.QueryKind_ROUTES, Families: []pb.AddressFamily{pb.AddressFamily_IPV4, pb.AddressFamily_IPV6}}}})
	if err != nil {
		return nil, err
	}
	return r.(*pb.Capabilities), nil
}
func (a *testAgent) GetState(ctx context.Context, q *pb.NodeRequest) (*pb.State, error) {
	r, err := a.invoke(ctx, "GetState", q, &pb.State{NodeId: testNode, Revision: math.MaxUint64, NodeState: "RUNNING"})
	if err != nil {
		return nil, err
	}
	return r.(*pb.State), nil
}
func (a *testAgent) Query(ctx context.Context, q *pb.QueryRequest) (*pb.QueryResponse, error) {
	record, _ := structpb.NewStruct(map[string]any{"prefix": "2001:db8::/64", "active": false})
	r, err := a.invoke(ctx, "Query", q, &pb.QueryResponse{NodeId: testNode, Kind: q.Kind, Source: "fake-linux", Revision: math.MaxUint64, Records: []*structpb.Struct{record}, NextPageToken: "opaque-next", TotalCount: 2})
	if err != nil {
		return nil, err
	}
	return r.(*pb.QueryResponse), nil
}
func (a *testAgent) VerifyResources(ctx context.Context, q *pb.VerifyResourcesRequest) (*pb.VerifyResourcesResponse, error) {
	r, err := a.invoke(ctx, "VerifyResources", q, &pb.VerifyResourcesResponse{NodeId: testNode, Revision: q.ExpectedRevision, Verified: true})
	if err != nil {
		return nil, err
	}
	return r.(*pb.VerifyResourcesResponse), nil
}
func (a *testAgent) Begin(ctx context.Context, q *pb.BeginRequest) (*pb.Transaction, error) {
	r, err := a.invoke(ctx, "Begin", q, &pb.Transaction{Id: "tx-1", NodeId: testNode, BaseRevision: q.BaseRevision})
	if err != nil {
		return nil, err
	}
	return r.(*pb.Transaction), nil
}
func (a *testAgent) Stage(ctx context.Context, q *pb.StageRequest) (*pb.Transaction, error) {
	r, err := a.invoke(ctx, "Stage", q, &pb.Transaction{Id: q.TransactionId, NodeId: testNode, Operations: q.Operations})
	if err != nil {
		return nil, err
	}
	return r.(*pb.Transaction), nil
}
func (a *testAgent) Commit(ctx context.Context, q *pb.CommitRequest) (*pb.Task, error) {
	r, err := a.invoke(ctx, "Commit", q, &pb.Task{Id: "task-1", TransactionId: q.TransactionId, NodeId: testNode, State: "RUNNING", Sequence: 7})
	if err != nil {
		return nil, err
	}
	return r.(*pb.Task), nil
}
func (a *testAgent) Abort(ctx context.Context, q *pb.AbortRequest) (*pb.Empty, error) {
	r, err := a.invoke(ctx, "Abort", q, &pb.Empty{})
	if err != nil {
		return nil, err
	}
	return r.(*pb.Empty), nil
}
func (a *testAgent) RetryRollback(ctx context.Context, q *pb.RetryRollbackRequest) (*pb.Task, error) {
	r, err := a.invoke(ctx, "RetryRollback", q, &pb.Task{Id: q.TaskId, NodeId: testNode, State: "ROLLING_BACK", Sequence: q.ExpectedSequence, Revision: q.ExpectedRevision})
	if err != nil {
		return nil, err
	}
	return r.(*pb.Task), nil
}
func (a *testAgent) GetTask(ctx context.Context, q *pb.TaskRequest) (*pb.Task, error) {
	r, err := a.invoke(ctx, "GetTask", q, &pb.Task{Id: q.TaskId, NodeId: testNode, State: "SUCCEEDED", Sequence: 9, Revision: math.MaxUint64})
	if err != nil {
		return nil, err
	}
	return r.(*pb.Task), nil
}
func (a *testAgent) WatchTasks(q *pb.TaskRequest, stream grpc.ServerStreamingServer[pb.Task]) error {
	_, _ = a.invoke(stream.Context(), "WatchTasks", q, &pb.Empty{})
	if a.watch != nil {
		return a.watch(q, stream)
	}
	return stream.Send(&pb.Task{Id: q.TaskId, NodeId: testNode, State: "SUCCEEDED", Sequence: 9, Revision: math.MaxUint64})
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	client *sdk.ClientSession
	agent  *testAgent
}

func newFixture(t *testing.T, a *testAgent, opts sdnmcp.Options) *fixture {
	t.Helper()
	if a == nil {
		a = &testAgent{}
	}
	if opts.Timeout == 0 {
		opts.Timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	pb.RegisterAgentServer(gs, a)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { gs.Stop(); _ = lis.Close() })
	conn, err := grpc.NewClient("passthrough:///mcp-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	srv, err := sdnmcp.NewServer(pb.NewAgentClient(conn), opts)
	if err != nil {
		t.Fatal(err)
	}
	ct, st := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "sdn-mcp-test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if cs.InitializeResult() == nil {
		t.Fatal("MCP initialize did not complete")
	}
	return &fixture{t, ctx, cs, a}
}

func (f *fixture) call(name string, args map[string]any) (*sdk.CallToolResult, error) {
	f.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	return f.client.CallTool(f.ctx, &sdk.CallToolParams{Name: name, Arguments: args})
}
func (f *fixture) ok(name string, args map[string]any) map[string]any {
	f.t.Helper()
	r, err := f.call(name, args)
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	if r.IsError {
		f.t.Fatalf("%s isError: %+v", name, r.Content)
	}
	if len(r.Content) == 0 {
		f.t.Fatalf("%s missing text compatibility result", name)
	}
	return object(f.t, r.StructuredContent)
}
func object(t *testing.T, v any) map[string]any {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	if e = json.Unmarshal(b, &out); e != nil || out == nil {
		t.Fatalf("expected object, got %s: %v", b, e)
	}
	return out
}
func rejected(t *testing.T, r *sdk.CallToolResult, err error) {
	t.Helper()
	if err == nil && (r == nil || !r.IsError) {
		t.Fatalf("expected rejection, got %+v", r)
	}
}
func validOperations() []any {
	return []any{map[string]any{"id": "bridge-1", "code": "BD", "action": "CREATE", "bridge": map[string]any{"name": "br-test", "stp": false, "ageing_time": 0}}}
}

func TestMCPDiscoveryReadOnlyAndWriteModes(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			f := newFixture(t, nil, sdnmcp.Options{NodeID: testNode, AllowWrites: write})
			r, err := f.client.ListTools(f.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			read := map[string]bool{"sdn_capabilities": true, "sdn_state": true, "sdn_query": true, "sdn_verify": true, "sdn_task": true, "sdn_watch": true}
			writes := []string{"sdn_begin", "sdn_stage", "sdn_commit", "sdn_abort", "sdn_retry_rollback", "sdn_submit"}
			want := len(read)
			if write {
				want += len(writes)
			}
			if len(r.Tools) != want {
				t.Fatalf("tools=%d want=%d", len(r.Tools), want)
			}
			seen := map[string]bool{}
			for _, tool := range r.Tools {
				seen[tool.Name] = true
				if tool.Description == "" || tool.InputSchema == nil || tool.Annotations == nil {
					t.Fatalf("incomplete discovery: %+v", tool)
				}
				if tool.Annotations.ReadOnlyHint != read[tool.Name] {
					t.Errorf("incorrect read-only hint for %s", tool.Name)
				}
				schema, _ := json.Marshal(tool.InputSchema)
				if strings.Contains(string(schema), `"node_id"`) {
					t.Errorf("tool %s accepts a caller-controlled node", tool.Name)
				}
				if tool.Name == "sdn_query" {
					for _, part := range []string{"EVPN_ROUTES", "IPV6", "page_token"} {
						if !strings.Contains(string(schema), part) {
							t.Errorf("query schema omits %s", part)
						}
					}
				}
				if tool.Name == "sdn_stage" {
					for _, part := range []string{"ospf6_interface", "EVPN_TYPE5", "stp", "ageing_time"} {
						if !strings.Contains(string(schema), part) {
							t.Errorf("stage schema omits %s", part)
						}
					}
				}
			}
			for name := range read {
				if !seen[name] {
					t.Errorf("missing %s", name)
				}
			}
			for _, name := range writes {
				if seen[name] != write {
					t.Errorf("write exposure %s=%v", name, seen[name])
				}
			}
			if !write {
				r, e := f.call("sdn_commit", map[string]any{"transaction_id": "tx-1", "request_id": "write-1"})
				rejected(t, r, e)
				if len(f.agent.writeMethods()) != 0 {
					t.Fatal("read-only server forwarded write")
				}
			}
		})
	}
}

func TestMCPReadToolsPreserveProtobufValues(t *testing.T) {
	f := newFixture(t, nil, sdnmcp.Options{})
	if got := f.ok("sdn_capabilities", nil)["node_id"]; got != testNode {
		t.Fatalf("discovered node=%v", got)
	}
	if got := f.ok("sdn_state", nil)["revision"]; got != largeRevision {
		t.Fatalf("revision lost precision: %T %v", got, got)
	}
	q := f.ok("sdn_query", map[string]any{"kind": "ROUTES", "family": "IPV6", "vrf": "blue", "interface": "eth0", "prefix": "2001:db8::/64", "page_size": 2, "page_token": "opaque-input"})
	if q["source"] != "fake-linux" || q["next_page_token"] != "opaque-next" || q["revision"] != largeRevision {
		t.Fatalf("query metadata changed: %v", q)
	}
	query := f.agent.requests("Query")[0].(*pb.QueryRequest)
	if query.NodeId != testNode || query.Kind != pb.QueryKind_ROUTES || query.Family != pb.AddressFamily_IPV6 || query.PageToken != "opaque-input" || query.PageSize != 2 || query.Vrf != "blue" || query.Interface != "eth0" {
		t.Fatalf("query mismatch: %v", query)
	}
	if got := f.ok("sdn_verify", map[string]any{"expected_revision": largeRevision, "operations": validOperations()})["verified"]; got != true {
		t.Fatalf("verify=%v", got)
	}
	v := f.agent.requests("VerifyResources")[0].(*pb.VerifyResourcesRequest)
	if v.ExpectedRevision != math.MaxUint64 || v.Operations[0].GetBridge().Stp == nil || *v.Operations[0].GetBridge().Stp || v.Operations[0].GetBridge().AgeingTime == nil || *v.Operations[0].GetBridge().AgeingTime != 0 {
		t.Fatalf("optional false/0 or revision lost: %v", v)
	}
	if got := f.ok("sdn_task", map[string]any{"task_id": "task-1"})["revision"]; got != largeRevision {
		t.Fatalf("task revision=%v", got)
	}
	if len(f.agent.writeMethods()) != 0 {
		t.Fatal("read tools called a mutating RPC")
	}
}

func TestMCPTransactionTools(t *testing.T) {
	f := newFixture(t, nil, sdnmcp.Options{NodeID: testNode, AllowWrites: true})
	f.ok("sdn_begin", map[string]any{"base_revision": largeRevision, "request_id": "begin-original"})
	f.ok("sdn_stage", map[string]any{"transaction_id": "tx-1", "operations": validOperations()})
	commit := f.ok("sdn_commit", map[string]any{"transaction_id": "tx-1", "request_id": "commit-original"})
	if commit["state"] != "RUNNING" {
		t.Fatalf("commit did not return accepted task: %v", commit)
	}
	if len(f.agent.requests("WatchTasks")) != 0 || len(f.agent.requests("GetTask")) != 0 {
		t.Fatal("commit waited instead of returning task identity")
	}
	f.ok("sdn_abort", map[string]any{"transaction_id": "other-tx"})
	f.ok("sdn_retry_rollback", map[string]any{"task_id": "task-failed", "request_id": "repair-original", "expected_revision": largeRevision, "expected_sequence": largeRevision})
	begin := f.agent.requests("Begin")[0].(*pb.BeginRequest)
	if begin.BaseRevision != math.MaxUint64 || begin.RequestId != "begin-original" {
		t.Fatalf("Begin changed explicit precondition/key: %v", begin)
	}
	stage := f.agent.requests("Stage")[0].(*pb.StageRequest)
	if stage.TransactionId != "tx-1" || stage.NodeId != testNode || stage.Operations[0].GetBridge().Stp == nil {
		t.Fatalf("Stage=%v", stage)
	}
	c := f.agent.requests("Commit")[0].(*pb.CommitRequest)
	if c.RequestId != "commit-original" {
		t.Fatalf("Commit regenerated key: %v", c)
	}
	retry := f.agent.requests("RetryRollback")[0].(*pb.RetryRollbackRequest)
	if retry.ExpectedSequence != math.MaxUint64 || retry.ExpectedRevision != math.MaxUint64 || retry.RequestId != "repair-original" {
		t.Fatalf("RetryRollback=%v", retry)
	}
	if want := []string{"Begin", "Stage", "Commit", "Abort", "RetryRollback"}; !reflect.DeepEqual(f.agent.writeMethods(), want) {
		t.Fatalf("writes=%v want=%v", f.agent.writeMethods(), want)
	}
}

func TestMCPRejectsInvalidArgumentsBeforeWrites(t *testing.T) {
	cases := []struct {
		name, tool string
		args       map[string]any
	}{
		{"unknown_tool", "run_shell", map[string]any{"command": "id"}},
		{"node_override", "sdn_state", map[string]any{"node_id": "other"}},
		{"token_override", "sdn_state", map[string]any{"token": "not-accepted"}},
		{"query_enum", "sdn_query", map[string]any{"kind": "NOT_A_QUERY"}},
		{"query_page", "sdn_query", map[string]any{"kind": "ROUTES", "page_size": -1}},
		{"missing_key", "sdn_begin", map[string]any{"base_revision": "0"}},
		{"blank_key", "sdn_commit", map[string]any{"transaction_id": "tx-1", "request_id": ""}},
		{"number_revision", "sdn_begin", map[string]any{"base_revision": 9007199254740993, "request_id": "r"}},
		{"overflow_revision", "sdn_begin", map[string]any{"base_revision": "18446744073709551616", "request_id": "r"}},
		{"negative_revision", "sdn_begin", map[string]any{"base_revision": "-1", "request_id": "r"}},
		{"null_revision", "sdn_begin", map[string]any{"base_revision": nil, "request_id": "r"}},
		{"missing_transaction", "sdn_stage", map[string]any{"operations": validOperations()}},
		{"empty_operations", "sdn_submit", map[string]any{"base_revision": "0", "request_id": "r", "operations": []any{}}},
		{"no_payload", "sdn_submit", map[string]any{"base_revision": "0", "request_id": "r", "operations": []any{map[string]any{"id": "x", "code": "BD", "action": "CREATE"}}}},
		{"multiple_payloads", "sdn_stage", map[string]any{"transaction_id": "tx-1", "operations": []any{map[string]any{"id": "x", "code": "BD", "action": "CREATE", "bridge": map[string]any{"name": "br1"}, "tenant": map[string]any{"name": "blue", "table_id": 1001}}}}},
		{"unknown_payload_field", "sdn_submit", map[string]any{"base_revision": "0", "request_id": "r", "operations": []any{map[string]any{"id": "x", "code": "BD", "action": "CREATE", "bridge": map[string]any{"name": "br1", "shell": "ip link"}}}}},
		{"duplicate_operation_id", "sdn_submit", map[string]any{"base_revision": "0", "request_id": "r", "operations": append(validOperations(), validOperations()...)}},
		{"watch_limit", "sdn_watch", map[string]any{"task_id": "task-1", "max_events": 257}},
		{"watch_timeout", "sdn_watch", map[string]any{"task_id": "task-1", "timeout_ms": 0}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, nil, sdnmcp.Options{NodeID: testNode, AllowWrites: true})
			r, e := f.call(tt.tool, tt.args)
			rejected(t, r, e)
			if got := f.agent.writeMethods(); len(got) != 0 {
				t.Fatalf("invalid request reached mutating RPCs: %v", got)
			}
		})
	}
}

func TestMCPSubmitRecoversUnknownCommitUsingOriginalKeys(t *testing.T) {
	var mu sync.Mutex
	var accepted bool
	var staged []*pb.Operation
	a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){}}
	a.hooks["Begin"] = func(_ context.Context, q proto.Message) (proto.Message, error) {
		mu.Lock()
		defer mu.Unlock()
		b := q.(*pb.BeginRequest)
		tx := &pb.Transaction{Id: "tx-stable", NodeId: testNode, BaseRevision: b.BaseRevision, Operations: staged, Frozen: accepted}
		if accepted {
			tx.TaskId = "task-stable"
		}
		return proto.Clone(tx), nil
	}
	a.hooks["Stage"] = func(_ context.Context, q proto.Message) (proto.Message, error) {
		mu.Lock()
		defer mu.Unlock()
		s := q.(*pb.StageRequest)
		staged = s.Operations
		return &pb.Transaction{Id: s.TransactionId, NodeId: testNode, BaseRevision: 42, Operations: staged}, nil
	}
	a.hooks["Commit"] = func(_ context.Context, q proto.Message) (proto.Message, error) {
		mu.Lock()
		defer mu.Unlock()
		accepted = true
		return nil, status.Error(codes.Unavailable, "reply lost after durable acceptance")
	}
	a.hooks["GetTask"] = func(_ context.Context, q proto.Message) (proto.Message, error) {
		return &pb.Task{Id: "task-stable", TransactionId: "tx-stable", NodeId: testNode, State: "RUNNING", Sequence: 3}, nil
	}
	f := newFixture(t, a, sdnmcp.Options{NodeID: testNode, AllowWrites: true})
	args := map[string]any{"base_revision": "42", "request_id": "stable-submit", "operations": validOperations()}
	r, e := f.call("sdn_submit", args)
	if e != nil {
		t.Fatal(e)
	}
	if !r.IsError {
		t.Fatal("lost Commit result was reported as success")
	}
	if data := object(t, r.StructuredContent); data["transaction_id"] != "tx-stable" {
		t.Fatalf("lost recovery handle: %v", data)
	}
	result := f.ok("sdn_submit", args)
	if task := object(t, result["task"]); task["id"] != "task-stable" {
		t.Fatalf("retry did not find accepted task: %v", result)
	}
	if got := a.writeMethods(); !reflect.DeepEqual(got, []string{"Begin", "Stage", "Commit", "Begin"}) {
		t.Fatalf("retry duplicated writes or aborted unknown Commit: %v", got)
	}
	for _, req := range a.requests("Begin") {
		q := req.(*pb.BeginRequest)
		if q.RequestId != "stable-submit/begin" || q.BaseRevision != 42 {
			t.Fatalf("retry changed key/revision: %v", q)
		}
	}
	if q := a.requests("Commit")[0].(*pb.CommitRequest); q.RequestId != "stable-submit/commit" {
		t.Fatalf("unexpected commit key: %v", q)
	}
	if len(a.requests("GetState")) != 0 {
		t.Fatal("submit silently fetched a new revision")
	}
	changed := map[string]any{"base_revision": "42", "request_id": "stable-submit", "operations": []any{map[string]any{"id": "bridge-2", "code": "BD", "action": "CREATE", "bridge": map[string]any{"name": "br-other"}}}}
	r, e = f.call("sdn_submit", changed)
	rejected(t, r, e)
	if len(a.requests("Stage")) != 1 || len(a.requests("Commit")) != 1 {
		t.Fatal("conflicting retry mutated accepted transaction")
	}
}

func TestMCPSubmitStopsAfterStageFailure(t *testing.T) {
	a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){"Stage": func(context.Context, proto.Message) (proto.Message, error) {
		return nil, status.Error(codes.InvalidArgument, "invalid dependency")
	}}}
	f := newFixture(t, a, sdnmcp.Options{NodeID: testNode, AllowWrites: true})
	r, e := f.call("sdn_submit", map[string]any{"base_revision": "0", "request_id": "stage-failed", "operations": validOperations()})
	rejected(t, r, e)
	if got := a.writeMethods(); !reflect.DeepEqual(got, []string{"Begin", "Stage"}) {
		t.Fatalf("stage error side effects: %v", got)
	}
	if r != nil && object(t, r.StructuredContent)["transaction_id"] != "tx-1" {
		t.Fatal("stage error lost transaction ID")
	}
}

func TestMCPWatchBoundsAndRecovery(t *testing.T) {
	t.Run("event_limit", func(t *testing.T) {
		a := &testAgent{watch: func(q *pb.TaskRequest, s grpc.ServerStreamingServer[pb.Task]) error {
			for seq := uint64(4); seq <= 6; seq++ {
				if e := s.Send(&pb.Task{Id: q.TaskId, NodeId: testNode, State: "RUNNING", Sequence: seq}); e != nil {
					return e
				}
			}
			return nil
		}}
		f := newFixture(t, a, sdnmcp.Options{NodeID: testNode})
		r := f.ok("sdn_watch", map[string]any{"task_id": "task-1", "after_sequence": "3", "max_events": 2, "timeout_ms": 500})
		if len(r["events"].([]any)) != 2 || r["after_sequence"] != "5" || r["terminal"] != false || r["truncated"] != true {
			t.Fatalf("bounded result=%v", r)
		}
		if q := a.requests("WatchTasks")[0].(*pb.TaskRequest); q.AfterSequence != 3 {
			t.Fatalf("cursor=%v", q)
		}
	})
	t.Run("terminal", func(t *testing.T) {
		f := newFixture(t, nil, sdnmcp.Options{NodeID: testNode})
		r := f.ok("sdn_watch", map[string]any{"task_id": "task-1"})
		if r["terminal"] != true || r["after_sequence"] != "9" {
			t.Fatalf("terminal result=%v", r)
		}
	})
	t.Run("empty_terminal_resume", func(t *testing.T) {
		a := &testAgent{watch: func(*pb.TaskRequest, grpc.ServerStreamingServer[pb.Task]) error { return nil }}
		f := newFixture(t, a, sdnmcp.Options{NodeID: testNode})
		r := f.ok("sdn_watch", map[string]any{"task_id": "task-1", "after_sequence": "9"})
		if r["terminal"] != true || len(a.requests("GetTask")) != 1 {
			t.Fatalf("EOF not checked: %v", r)
		}
	})
	t.Run("nonterminal_eof", func(t *testing.T) {
		a := &testAgent{watch: func(*pb.TaskRequest, grpc.ServerStreamingServer[pb.Task]) error { return nil }, hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){"GetTask": func(context.Context, proto.Message) (proto.Message, error) {
			return &pb.Task{Id: "task-1", NodeId: testNode, State: "RUNNING", Sequence: 7}, nil
		}}}
		f := newFixture(t, a, sdnmcp.Options{NodeID: testNode})
		r, e := f.call("sdn_watch", map[string]any{"task_id": "task-1"})
		rejected(t, r, e)
	})
	t.Run("timeout_is_partial_not_success", func(t *testing.T) {
		a := &testAgent{watch: func(_ *pb.TaskRequest, s grpc.ServerStreamingServer[pb.Task]) error {
			<-s.Context().Done()
			return s.Context().Err()
		}}
		f := newFixture(t, a, sdnmcp.Options{NodeID: testNode})
		r := f.ok("sdn_watch", map[string]any{"task_id": "task-1", "timeout_ms": 20})
		if r["terminal"] != false || r["timed_out"] != true {
			t.Fatalf("timeout result=%v", r)
		}
	})
}

func TestMCPUpstreamErrorsDeadlinesAndCancellation(t *testing.T) {
	t.Run("structured_grpc_error", func(t *testing.T) {
		a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){"Query": func(context.Context, proto.Message) (proto.Message, error) {
			return nil, status.Error(codes.PermissionDenied, "sensitive upstream text must not become a success")
		}}}
		f := newFixture(t, a, sdnmcp.Options{NodeID: testNode})
		r, e := f.call("sdn_query", map[string]any{"kind": "ROUTES"})
		if e != nil {
			t.Fatal(e)
		}
		if !r.IsError || !strings.Contains(fmt.Sprint(r.StructuredContent), "PermissionDenied") {
			t.Fatalf("gRPC error lost status: %+v", r)
		}
	})
	t.Run("per_call_deadline", func(t *testing.T) {
		done := make(chan struct{})
		a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){"Query": func(ctx context.Context, _ proto.Message) (proto.Message, error) {
			<-ctx.Done()
			close(done)
			return nil, status.FromContextError(ctx.Err()).Err()
		}}}
		f := newFixture(t, a, sdnmcp.Options{NodeID: testNode, Timeout: 50 * time.Millisecond})
		r, e := f.call("sdn_query", map[string]any{"kind": "ROUTES"})
		rejected(t, r, e)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("deadline did not reach gRPC")
		}
	})
	t.Run("client_cancel", func(t *testing.T) {
		started := make(chan struct{})
		done := make(chan struct{})
		a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){"Query": func(ctx context.Context, _ proto.Message) (proto.Message, error) {
			close(started)
			<-ctx.Done()
			close(done)
			return nil, status.FromContextError(ctx.Err()).Err()
		}}}
		f := newFixture(t, a, sdnmcp.Options{NodeID: testNode, Timeout: 5 * time.Second})
		ctx, cancel := context.WithCancel(f.ctx)
		defer cancel()
		returned := make(chan error, 1)
		go func() {
			_, e := f.client.CallTool(ctx, &sdk.CallToolParams{Name: "sdn_query", Arguments: map[string]any{"kind": "ROUTES"}})
			returned <- e
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("upstream did not start")
		}
		cancel()
		select {
		case e := <-returned:
			if e == nil {
				t.Error("canceled call returned no error")
			}
		case <-time.After(time.Second):
			t.Fatal("MCP cancellation did not finish")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("MCP cancellation did not reach gRPC")
		}
	})
}

func TestMCPRejectsMismatchedAgentResponses(t *testing.T) {
	for _, tt := range []struct {
		name, method, tool string
		args               map[string]any
		response           proto.Message
	}{
		{"capabilities_node", "GetCapabilities", "sdn_capabilities", nil, &pb.Capabilities{NodeId: "other-node"}},
		{"state_node", "GetState", "sdn_state", nil, &pb.State{NodeId: "other-node"}},
		{"query_node", "Query", "sdn_query", map[string]any{"kind": "ROUTES"}, &pb.QueryResponse{NodeId: "other-node", Kind: pb.QueryKind_ROUTES}},
		{"query_kind", "Query", "sdn_query", map[string]any{"kind": "ROUTES"}, &pb.QueryResponse{NodeId: testNode, Kind: pb.QueryKind_FDB}},
		{"task_identity", "GetTask", "sdn_task", map[string]any{"task_id": "task-1"}, &pb.Task{Id: "other-task", NodeId: testNode}},
		{"stage_transaction", "Stage", "sdn_stage", map[string]any{"transaction_id": "tx-1", "operations": validOperations()}, &pb.Transaction{Id: "other-tx", NodeId: testNode}},
		{"commit_transaction", "Commit", "sdn_commit", map[string]any{"transaction_id": "tx-1", "request_id": "original"}, &pb.Task{Id: "task-1", TransactionId: "other-tx", NodeId: testNode}},
		{"rollback_task", "RetryRollback", "sdn_retry_rollback", map[string]any{"task_id": "task-1", "request_id": "original", "expected_revision": "0", "expected_sequence": "3"}, &pb.Task{Id: "other-task", NodeId: testNode}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){tt.method: func(context.Context, proto.Message) (proto.Message, error) { return tt.response, nil }}}
			f := newFixture(t, a, sdnmcp.Options{NodeID: testNode, AllowWrites: true})
			r, e := f.call(tt.tool, tt.args)
			if e != nil {
				t.Fatal(e)
			}
			if !r.IsError || !strings.Contains(fmt.Sprint(r.StructuredContent), "DataLoss") {
				t.Fatalf("accepted unrelated response: %+v", r)
			}
		})
	}
}

func TestMCPFailedVerificationPreservesEvidence(t *testing.T) {
	a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){"VerifyResources": func(context.Context, proto.Message) (proto.Message, error) {
		return &pb.VerifyResourcesResponse{NodeId: testNode, Revision: 42, Verified: false, Errors: []string{"br-test is absent"}}, nil
	}}}
	f := newFixture(t, a, sdnmcp.Options{NodeID: testNode})
	r, e := f.call("sdn_verify", map[string]any{"expected_revision": "42", "operations": validOperations()})
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(r.StructuredContent)
	if !r.IsError || !strings.Contains(string(encoded), "FailedPrecondition") || !strings.Contains(string(encoded), "br-test is absent") {
		t.Fatalf("failed verification lost status/evidence: %s", encoded)
	}
}

func TestMCPRejectsDuplicateJSONFields(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"base_revision":"0","base_revision":"1","request_id":"x","operations":[{"id":"bridge-1","code":"BD","action":"CREATE","bridge":{"name":"br-test"}}]}`),
		json.RawMessage(`{"base_revision":"0","request_id":"x","operations":[{"id":"bridge-1","code":"BD","action":"CREATE","bridge":{"name":"br-test","stp":false,"stp":true}}]}`),
	} {
		f := newFixture(t, nil, sdnmcp.Options{NodeID: testNode, AllowWrites: true})
		r, e := f.client.CallTool(f.ctx, &sdk.CallToolParams{Name: "sdn_submit", Arguments: raw})
		rejected(t, r, e)
		if len(f.agent.writeMethods()) != 0 {
			t.Fatal("duplicate fields reached mutating RPC")
		}
	}
}

func TestMCPConcurrentSubmitWaiterCanCancelWithoutSideEffects(t *testing.T) {
	stageStarted := make(chan struct{})
	releaseStage := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStage) }) }
	t.Cleanup(release)
	var mu sync.Mutex
	var committed bool
	var staged []*pb.Operation
	a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){}}
	a.hooks["Begin"] = func(_ context.Context, q proto.Message) (proto.Message, error) {
		mu.Lock()
		defer mu.Unlock()
		b := q.(*pb.BeginRequest)
		tx := &pb.Transaction{Id: "tx-concurrent", NodeId: testNode, BaseRevision: b.BaseRevision, Operations: staged, Frozen: committed}
		if committed {
			tx.TaskId = "task-concurrent"
		}
		return proto.Clone(tx), nil
	}
	a.hooks["Stage"] = func(ctx context.Context, q proto.Message) (proto.Message, error) {
		close(stageStarted)
		select {
		case <-releaseStage:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		mu.Lock()
		defer mu.Unlock()
		s := q.(*pb.StageRequest)
		staged = s.Operations
		return &pb.Transaction{Id: s.TransactionId, NodeId: testNode, BaseRevision: 7, Operations: staged}, nil
	}
	a.hooks["Commit"] = func(context.Context, proto.Message) (proto.Message, error) {
		mu.Lock()
		committed = true
		mu.Unlock()
		return &pb.Task{Id: "task-concurrent", TransactionId: "tx-concurrent", NodeId: testNode, State: "RUNNING"}, nil
	}
	a.hooks["GetTask"] = func(context.Context, proto.Message) (proto.Message, error) {
		return &pb.Task{Id: "task-concurrent", TransactionId: "tx-concurrent", NodeId: testNode, State: "RUNNING"}, nil
	}
	f := newFixture(t, a, sdnmcp.Options{NodeID: testNode, AllowWrites: true})
	args := map[string]any{"base_revision": "7", "request_id": "concurrent-original", "operations": validOperations()}
	type response struct {
		r   *sdk.CallToolResult
		err error
	}
	first := make(chan response, 1)
	go func() { r, e := f.call("sdn_submit", args); first <- response{r, e} }()
	select {
	case <-stageStarted:
	case <-time.After(time.Second):
		t.Fatal("first submit did not reach Stage")
	}
	waitCtx, cancel := context.WithTimeout(f.ctx, 100*time.Millisecond)
	defer cancel()
	r, e := f.client.CallTool(waitCtx, &sdk.CallToolParams{Name: "sdn_submit", Arguments: args})
	rejected(t, r, e)
	if len(a.requests("Begin")) != 1 {
		t.Fatal("concurrent waiter entered Agent before acquiring workflow ownership")
	}
	release()
	select {
	case response := <-first:
		if response.err != nil || response.r.IsError {
			t.Fatalf("original submit failed: %v %+v", response.err, response.r)
		}
	case <-time.After(time.Second):
		t.Fatal("original submit did not finish")
	}
	got := f.ok("sdn_submit", args)
	if task := object(t, got["task"]); task["id"] != "task-concurrent" {
		t.Fatalf("resume lost original task: %v", got)
	}
	if methods := a.writeMethods(); !reflect.DeepEqual(methods, []string{"Begin", "Stage", "Commit", "Begin"}) {
		t.Fatalf("canceled waiter caused writes: %v", methods)
	}
}
