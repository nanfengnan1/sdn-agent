package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/transport"
)

// Execute the actual main/flag/transport/output/exit path in an isolated process.
// This avoids refactoring production CLI solely for dependency injection.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("SDNCTL_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"sdnctl"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("sdnctl", flag.ExitOnError)
	main()
	os.Exit(0)
}

const cliToken = "command-test-token-local-only"

type cliCall struct {
	name    string
	request proto.Message
}
type cliServer struct {
	pb.UnimplementedAgentServer
	mu          sync.Mutex
	calls       []cliCall
	fail        string
	events      []*pb.Task
	streamError error
	wait        bool
	taskState   string
}

func (s *cliServer) record(name string, q proto.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, cliCall{name, proto.Clone(q)})
	if s.fail == name {
		return status.Error(codes.Unavailable, "injected "+name+" failure")
	}
	return nil
}
func (s *cliServer) GetCapabilities(_ context.Context, q *pb.NodeRequest) (*pb.Capabilities, error) {
	err := s.record("capabilities", q)
	return &pb.Capabilities{NodeId: "node-1", ProtocolVersion: 1}, err
}
func (s *cliServer) GetState(_ context.Context, q *pb.NodeRequest) (*pb.State, error) {
	err := s.record("state", q)
	return &pb.State{NodeId: "node-1", Revision: 7, NodeState: "RUNNING"}, err
}
func (s *cliServer) Begin(_ context.Context, q *pb.BeginRequest) (*pb.Transaction, error) {
	err := s.record("begin", q)
	return &pb.Transaction{Id: "tx-new", NodeId: q.NodeId, BaseRevision: q.BaseRevision}, err
}
func (s *cliServer) Stage(_ context.Context, q *pb.StageRequest) (*pb.Transaction, error) {
	err := s.record("stage", q)
	return &pb.Transaction{Id: q.TransactionId, NodeId: q.NodeId, Operations: q.Operations}, err
}
func (s *cliServer) Commit(_ context.Context, q *pb.CommitRequest) (*pb.Task, error) {
	err := s.record("commit", q)
	return &pb.Task{Id: "task-new", TransactionId: q.TransactionId, State: "ACCEPTED", Sequence: 1}, err
}
func (s *cliServer) Abort(_ context.Context, q *pb.AbortRequest) (*pb.Empty, error) {
	return &pb.Empty{}, s.record("abort", q)
}
func (s *cliServer) RetryRollback(_ context.Context, q *pb.RetryRollbackRequest) (*pb.Task, error) {
	err := s.record("retry-rollback", q)
	return &pb.Task{Id: q.TaskId, State: "ROLLING_BACK", Sequence: q.ExpectedSequence + 1}, err
}
func (s *cliServer) GetTask(_ context.Context, q *pb.TaskRequest) (*pb.Task, error) {
	err := s.record("task", q)
	state := s.taskState
	if state == "" {
		state = "SUCCEEDED"
	}
	return &pb.Task{Id: q.TaskId, State: state, Sequence: 3}, err
}
func (s *cliServer) WatchTasks(q *pb.TaskRequest, stream grpc.ServerStreamingServer[pb.Task]) error {
	if err := s.record("watch", q); err != nil {
		return err
	}
	if s.wait {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	for _, event := range s.events {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	return s.streamError
}
func (s *cliServer) recorded() []cliCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cliCall(nil), s.calls...)
}
func startCLI(t *testing.T, s *cliServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := transport.Server(cliToken, "", "", true)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	pb.RegisterAgentServer(server, s)
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); listener.Close(); <-done })
	return listener.Addr().String()
}
func runCLI(t *testing.T, addr string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	all := []string{"-test.run=^TestCLIProcess$", "--", "-insecure", "-addr", addr, "-timeout", "3s"}
	all = append(all, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], all...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "SDN_TOKEN=") && !strings.HasPrefix(e, "SDNCTL_TEST_PROCESS=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "SDN_TOKEN="+cliToken, "SDNCTL_TEST_PROCESS=1")
	var out, errout bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("CLI did not terminate: %v", ctx.Err())
	}
	return out.String(), errout.String(), err
}
func cliFile(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "operations.json")
	if err := os.WriteFile(p, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

const stageJSON = `{"node_id":"file-node","transaction_id":"file-tx","operations":[{"id":"addr","code":"ADDRESS","action":"CREATE","address":{"interface":"eth0","prefix":"192.0.2.2/24"}}]}`

type cliCase struct {
	command string
	calls   []string
}

func cliCases() []cliCase {
	return []cliCase{
		{"capabilities", []string{"capabilities"}},
		{"state", []string{"capabilities", "state"}},
		{"begin", []string{"capabilities", "state", "begin"}},
		{"stage", []string{"capabilities", "stage"}},
		{"commit", []string{"capabilities", "commit", "watch"}},
		{"submit", []string{"capabilities", "state", "begin", "stage", "commit", "watch"}},
		{"abort", []string{"capabilities", "abort"}},
		{"task", []string{"capabilities", "task"}},
		{"watch", []string{"capabilities", "watch"}},
		{"retry-rollback", []string{"capabilities", "retry-rollback"}},
	}
}
func cliArgs(command, file string) []string {
	a := []string{"-request-id", "request-1", "-node", "requested-node"}
	switch command {
	case "stage", "commit", "abort":
		a = append(a, "-transaction", "tx-existing")
	case "task", "watch", "retry-rollback":
		a = append(a, "-task", "task-existing")
	}
	if command == "stage" || command == "submit" {
		a = append(a, "-file", file)
	}
	if command == "watch" {
		a = append(a, "-after", "11")
	}
	if command == "retry-rollback" {
		a = append(a, "-sequence", "22", "-revision", "7")
	}
	return append(a, command)
}
func successEvents() []*pb.Task {
	return []*pb.Task{{Id: "task-new", State: "ACCEPTED", Sequence: 1}, {Id: "task-new", State: "APPLYING", Sequence: 2}, {Id: "task-new", State: "SUCCEEDED", Sequence: 3, Revision: 8}}
}
func assertCalls(t *testing.T, s *cliServer, want []string) {
	t.Helper()
	var got []string
	for _, c := range s.recorded() {
		got = append(got, c.name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RPC order %v want %v", got, want)
	}
}
func decodeObjects(t *testing.T, out string) []json.RawMessage {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(out))
	var result []json.RawMessage
	for {
		var obj json.RawMessage
		err := d.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid JSON output %q: %v", out, err)
		}
		result = append(result, obj)
	}
	return result
}

func TestCLICommands(t *testing.T) {
	for _, c := range cliCases() {
		t.Run(c.command, func(t *testing.T) {
			s := &cliServer{events: successEvents()}
			addr := startCLI(t, s)
			file := cliFile(t, stageJSON)
			out, stderr, err := runCLI(t, addr, cliArgs(c.command, file)...)
			if err != nil {
				t.Fatalf("%v: %s", err, stderr)
			}
			if stderr != "" {
				t.Fatalf("unexpected stderr: %s", stderr)
			}
			assertCalls(t, s, c.calls)
			for _, call := range s.recorded() {
				switch q := call.request.(type) {
				case *pb.NodeRequest:
					want := "node-1"
					if call.name == "capabilities" {
						want = "requested-node"
					}
					if q.NodeId != want {
						t.Fatalf("node discovery: %v", q)
					}
				case *pb.BeginRequest:
					if !proto.Equal(q, &pb.BeginRequest{NodeId: "node-1", BaseRevision: 7, RequestId: "request-1/begin"}) {
						t.Fatalf("begin arguments: %v", q)
					}
				case *pb.StageRequest:
					var want pb.StageRequest
					if err := protojson.Unmarshal([]byte(stageJSON), &want); err != nil {
						t.Fatal(err)
					}
					want.NodeId = "node-1"
					want.TransactionId = "tx-existing"
					if c.command == "submit" {
						want.TransactionId = "tx-new"
					}
					if !proto.Equal(q, &want) {
						t.Fatalf("stage body/identity: %v", q)
					}
				case *pb.CommitRequest:
					tx := "tx-existing"
					if c.command == "submit" {
						tx = "tx-new"
					}
					if !proto.Equal(q, &pb.CommitRequest{NodeId: "node-1", TransactionId: tx, RequestId: "request-1/commit"}) {
						t.Fatalf("commit arguments: %v", q)
					}
				case *pb.TaskRequest:
					id := "task-existing"
					var after uint64
					if c.command == "commit" || c.command == "submit" {
						id = "task-new"
					}
					if c.command == "watch" {
						after = 11
					}
					if !proto.Equal(q, &pb.TaskRequest{NodeId: "node-1", TaskId: id, AfterSequence: after}) {
						t.Fatalf("task arguments: %v", q)
					}
				case *pb.AbortRequest:
					if !proto.Equal(q, &pb.AbortRequest{NodeId: "node-1", TransactionId: "tx-existing"}) {
						t.Fatalf("abort arguments: %v", q)
					}
				case *pb.RetryRollbackRequest:
					if !proto.Equal(q, &pb.RetryRollbackRequest{NodeId: "node-1", TaskId: "task-existing", RequestId: "request-1", ExpectedSequence: 22, ExpectedRevision: 7}) {
						t.Fatalf("retry guard arguments: %v", q)
					}
				}
			}
			objects := decodeObjects(t, out)
			wantCount := 1
			if c.command == "abort" {
				wantCount = 0
			}
			if c.command == "watch" {
				wantCount = 3
			}
			if len(objects) != wantCount {
				t.Fatalf("JSON objects %d want %d: %s", len(objects), wantCount, out)
			}
			if c.command == "commit" || c.command == "submit" || c.command == "watch" {
				var task pb.Task
				if err := protojson.Unmarshal(objects[len(objects)-1], &task); err != nil {
					t.Fatal(err)
				}
				if task.State != "SUCCEEDED" || task.Sequence != 3 {
					t.Fatalf("terminal output: %v", &task)
				}
			}
		})
	}
}

func TestCLIRPCFailures(t *testing.T) {
	for _, c := range cliCases() {
		for i, fail := range c.calls {
			t.Run(c.command+"/"+fail, func(t *testing.T) {
				s := &cliServer{fail: fail, events: successEvents()}
				addr := startCLI(t, s)
				out, stderr, err := runCLI(t, addr, cliArgs(c.command, cliFile(t, stageJSON))...)
				if err == nil || !strings.Contains(stderr, "injected "+fail+" failure") {
					t.Fatalf("RPC failure lost: err=%v stdout=%q stderr=%q", err, out, stderr)
				}
				assertCalls(t, s, c.calls[:i+1])
			})
		}
	}
}

func TestCLIInputErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"missing_command", nil}, {"unknown_command", []string{"unknown"}}, {"extra_argument", []string{"state", "extra"}},
		{"stage_no_transaction", []string{"-file", "unused", "stage"}}, {"stage_no_file", []string{"-transaction", "tx", "stage"}},
		{"submit_no_file", []string{"submit"}}, {"commit_no_transaction", []string{"commit"}}, {"abort_no_transaction", []string{"abort"}},
		{"task_no_id", []string{"task"}}, {"watch_no_id", []string{"watch"}}, {"retry_no_id", []string{"retry-rollback"}},
		{"negative_sequence", []string{"-sequence", "-1", "retry-rollback"}}, {"bad_timeout", []string{"-timeout", "bad", "state"}},
		{"zero_timeout", []string{"-timeout", "0s", "state"}}, {"unknown_flag", []string{"-nonsense", "state"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &cliServer{}
			addr := startCLI(t, s)
			out, stderr, err := runCLI(t, addr, test.args...)
			if err == nil || stderr == "" {
				t.Fatalf("invalid input accepted: err=%v out=%q stderr=%q", err, out, stderr)
			}
			if len(s.recorded()) > 0 {
				t.Fatalf("invalid CLI input contacted agent: %v", s.recorded())
			}
		})
	}
	for _, contents := range []string{`{`, `{"operations":[{"code":"DO_SHELL"}]}`, `{"unexpected":true}`} {
		t.Run("invalid_file_"+contents, func(t *testing.T) {
			s := &cliServer{}
			addr := startCLI(t, s)
			_, stderr, err := runCLI(t, addr, cliArgs("stage", cliFile(t, contents))...)
			if err == nil || stderr == "" {
				t.Fatal("invalid JSON accepted")
			}
			for _, c := range s.recorded() {
				if c.name == "stage" || c.name == "commit" {
					t.Fatal("invalid JSON was sent")
				}
			}
		})
	}
	t.Run("missing_file", func(t *testing.T) {
		s := &cliServer{}
		addr := startCLI(t, s)
		_, _, err := runCLI(t, addr, cliArgs("stage", filepath.Join(t.TempDir(), "absent.json"))...)
		if err == nil {
			t.Fatal("missing file accepted")
		}
		for _, c := range s.recorded() {
			if c.name == "stage" {
				t.Fatal("missing file was staged")
			}
		}
	})
}

func TestCLIStreamOutcomes(t *testing.T) {
	for _, command := range []string{"watch", "commit", "submit"} {
		for _, state := range []string{"FAILED_NO_CHANGE", "FAILED_ROLLED_BACK", "ROLLBACK_FAILED"} {
			t.Run(command+"/"+state, func(t *testing.T) {
				s := &cliServer{events: []*pb.Task{{Id: "task-new", State: state, Error: "injected apply failure"}}}
				addr := startCLI(t, s)
				out, stderr, err := runCLI(t, addr, cliArgs(command, cliFile(t, stageJSON))...)
				if err == nil || !strings.Contains(stderr, state) || !strings.Contains(out, state) {
					t.Fatalf("failed task exit/output: %v %q %q", err, out, stderr)
				}
			})
		}
		for _, mode := range []string{"empty_eof", "unfinished_eof", "rpc_error", "deadline"} {
			t.Run(command+"/"+mode, func(t *testing.T) {
				s := &cliServer{taskState: "APPLYING"}
				switch mode {
				case "unfinished_eof":
					s.events = []*pb.Task{{Id: "task-new", State: "APPLYING"}}
				case "rpc_error":
					s.streamError = status.Error(codes.DataLoss, "broken stream")
				case "deadline":
					s.wait = true
				}
				addr := startCLI(t, s)
				args := cliArgs(command, cliFile(t, stageJSON))
				if mode == "deadline" {
					args = append([]string{"-timeout", "200ms"}, args...)
				}
				_, stderr, err := runCLI(t, addr, args...)
				if err == nil || stderr == "" {
					t.Fatalf("unfinished task reported success: %v %q", err, stderr)
				}
			})
		}
	}
}

func TestCLIDiscoveryAndGeneratedRequestID(t *testing.T) {
	s := &cliServer{}
	addr := startCLI(t, s)
	_, stderr, err := runCLI(t, addr, "begin")
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	calls := s.recorded()
	if calls[0].request.(*pb.NodeRequest).NodeId != "" {
		t.Fatal("default node was not discovery")
	}
	q := calls[len(calls)-1].request.(*pb.BeginRequest)
	if q.NodeId != "node-1" || !strings.HasSuffix(q.RequestId, "/begin") || len(strings.TrimSuffix(q.RequestId, "/begin")) != 36 {
		t.Fatalf("generated request/identity: %v", q)
	}
}

func TestCLIResumeAtTerminalCursor(t *testing.T) {
	for _, command := range []string{"watch", "commit", "submit"} {
		for _, state := range []string{"SUCCEEDED", "FAILED_ROLLED_BACK"} {
			t.Run(command+"/"+state, func(t *testing.T) {
				s := &cliServer{taskState: state}
				addr := startCLI(t, s)
				args := cliArgs(command, cliFile(t, stageJSON))
				if command == "watch" {
					args = []string{"-task", "task-existing", "-after", "3", "watch"}
				}
				out, stderr, err := runCLI(t, addr, args...)
				if (err == nil) != (state == "SUCCEEDED") {
					t.Fatalf("persisted terminal result: %v %q", err, stderr)
				}
				calls := s.recorded()
				if calls[len(calls)-1].name != "task" {
					t.Fatal("EOF did not query persisted task")
				}
				if command == "watch" && out != "" {
					t.Fatalf("replayed already-consumed event: %s", out)
				}
				if command != "watch" && !strings.Contains(out, state) {
					t.Fatalf("missing final task: %s", out)
				}
			})
		}
	}
	t.Run("query_failure", func(t *testing.T) {
		s := &cliServer{fail: "task"}
		addr := startCLI(t, s)
		_, stderr, err := runCLI(t, addr, "-task", "task-existing", "watch")
		if err == nil || !strings.Contains(stderr, "injected task failure") {
			t.Fatalf("unconfirmed EOF succeeded: %v %s", err, stderr)
		}
	})
}

func TestCLITransportConfigurationErrors(t *testing.T) {
	for _, args := range [][]string{{"-insecure=false", "state"}, {"-ca", "missing-ca.pem", "state"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			s := &cliServer{}
			addr := startCLI(t, s)
			_, stderr, err := runCLI(t, addr, args...)
			if err == nil || stderr == "" {
				t.Fatal("invalid transport config accepted")
			}
			if len(s.recorded()) != 0 {
				t.Fatal("invalid transport reached server")
			}
		})
	}
}
