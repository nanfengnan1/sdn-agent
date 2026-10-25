package main

import (
	"context"
	"flag"
	"fmt"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"io"
	"os"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
	"sdn-agent/internal/transport"
	"strings"
	"time"
)

func printPB(m proto.Message) {
	b, _ := protojson.MarshalOptions{Indent: "  ", UseProtoNames: true}.Marshal(m)
	fmt.Println(string(b))
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:9090", "agent address")
	node := flag.String("node", "", "node UUID (discovered when empty)")
	file := flag.String("file", "", "JSON StageRequest file")
	tx := flag.String("transaction", "", "transaction id")
	task := flag.String("task", "", "task id")
	request := flag.String("request-id", "", "idempotency key")
	after := flag.Uint64("after", 0, "event cursor")
	sequence := flag.Uint64("sequence", 0, "expected task sequence for rollback retry")
	revision := flag.Uint64("revision", 0, "expected Running revision for rollback retry")
	timeout := flag.Duration("timeout", 3*time.Minute, "RPC/workflow timeout")
	ca := flag.String("ca", "", "CA")
	insecure := flag.Bool("insecure", false, "allow lab plaintext")
	kind := flag.String("kind", "", "structured query kind, e.g. TENANTS, ROUTES, EVPN_ROUTES")
	vrf := flag.String("vrf", "", "query VRF")
	bridge := flag.String("bridge", "", "query bridge")
	intf := flag.String("interface", "", "query interface")
	family := flag.String("family", "AF_UNSPECIFIED", "IPV4, IPV6 or L2VPN_EVPN")
	ospfVersion := flag.Uint("ospf-version", 0, "OSPF version 2 or 3")
	pageSize := flag.Uint("page-size", 200, "query records per page (max 1000)")
	pageToken := flag.String("page-token", "", "query continuation token")
	routeType := flag.Uint("route-type", 0, "EVPN route type filter")
	prefix := flag.String("prefix", "", "route prefix filter")
	neighbor := flag.String("neighbor", "", "neighbor address filter")
	flag.Parse()
	command := flag.Arg(0)
	if flag.NArg() != 1 {
		return fmt.Errorf("exactly one command required; place flags before the command")
	}
	switch command {
	case "query", "verify", "capabilities", "state", "begin", "stage", "commit", "submit", "abort", "task", "watch", "retry-rollback":
	default:
		return fmt.Errorf("commands: capabilities state begin stage commit submit abort task watch retry-rollback")
	}
	if *timeout <= 0 {
		return fmt.Errorf("-timeout must be positive")
	}
	if (command == "stage" || command == "commit" || command == "abort") && *tx == "" {
		return fmt.Errorf("-transaction required for %s", command)
	}
	if (command == "task" || command == "watch" || command == "retry-rollback") && *task == "" {
		return fmt.Errorf("-task required for %s", command)
	}
	if (command == "stage" || command == "submit") && *file == "" {
		return fmt.Errorf("-file required for %s", command)
	}
	cc, e := transport.Dial(*addr, os.Getenv("SDN_TOKEN"), *ca, *insecure)
	if e != nil {
		return e
	}
	defer cc.Close()
	c := pb.NewAgentClient(cc)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	caps, e := c.GetCapabilities(ctx, &pb.NodeRequest{NodeId: *node})
	if e != nil {
		return e
	}
	*node = caps.NodeId
	if *request == "" {
		*request = model.ID()
	}
	switch command {
	case "query":
		k, ok := pb.QueryKind_value[strings.ToUpper(*kind)]
		if !ok || k == 0 {
			return fmt.Errorf("valid -kind required")
		}
		f, ok := pb.AddressFamily_value[strings.ToUpper(*family)]
		if !ok {
			return fmt.Errorf("invalid -family")
		}
		if *pageSize > 1000 || *ospfVersion > 3 || *routeType > 5 {
			return fmt.Errorf("invalid query numeric parameter")
		}
		s, e := c.Query(ctx, &pb.QueryRequest{NodeId: *node, Kind: pb.QueryKind(k), Vrf: *vrf, Bridge: *bridge, Interface: *intf, Family: pb.AddressFamily(f), OspfVersion: uint32(*ospfVersion), PageSize: uint32(*pageSize), PageToken: *pageToken, RouteType: uint32(*routeType), Prefix: *prefix, Neighbor: *neighbor})
		if e == nil {
			printPB(s)
		}
		return e
	case "verify":
		b, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		q := &pb.VerifyResourcesRequest{}
		if e = protojson.Unmarshal(b, q); e != nil {
			return e
		}
		q.NodeId = *node
		q.ExpectedRevision = *revision
		s, e := c.VerifyResources(ctx, q)
		if e == nil {
			printPB(s)
			if !s.Verified {
				return fmt.Errorf("resource readback failed")
			}
		}
		return e
	case "capabilities":
		printPB(caps)
		return nil
	case "state":
		s, e := c.GetState(ctx, &pb.NodeRequest{NodeId: *node})
		if e == nil {
			printPB(s)
		}
		return e
	case "task":
		t, e := c.GetTask(ctx, &pb.TaskRequest{NodeId: *node, TaskId: *task})
		if e == nil {
			printPB(t)
		}
		return e
	case "abort":
		_, e := c.Abort(ctx, &pb.AbortRequest{NodeId: *node, TransactionId: *tx})
		return e
	case "retry-rollback":
		t, e := c.RetryRollback(ctx, &pb.RetryRollbackRequest{NodeId: *node, TaskId: *task, RequestId: *request, ExpectedSequence: *sequence, ExpectedRevision: *revision})
		if e == nil {
			printPB(t)
		}
		return e
	case "begin", "submit":
		s, e := c.GetState(ctx, &pb.NodeRequest{NodeId: *node})
		if e != nil {
			return e
		}
		t, e := c.Begin(ctx, &pb.BeginRequest{NodeId: *node, BaseRevision: s.Revision, RequestId: *request + "/begin"})
		if e != nil {
			return e
		}
		*tx = t.Id
		if command == "begin" {
			printPB(t)
			return nil
		}
	case "stage", "commit", "watch":
	default:
		return fmt.Errorf("commands: capabilities state begin stage commit submit abort task watch retry-rollback")
	}
	if command == "stage" || command == "submit" {
		b, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		var q pb.StageRequest
		if e = protojson.Unmarshal(b, &q); e != nil {
			return e
		}
		q.NodeId = *node
		q.TransactionId = *tx
		t, e := c.Stage(ctx, &q)
		if e != nil {
			return e
		}
		if command == "stage" {
			printPB(t)
			return nil
		}
	}
	if command == "commit" || command == "submit" {
		t, e := c.Commit(ctx, &pb.CommitRequest{NodeId: *node, TransactionId: *tx, RequestId: *request + "/commit"})
		if e != nil {
			return e
		}
		*task = t.Id
	}
	stream, e := c.WatchTasks(ctx, &pb.TaskRequest{NodeId: *node, TaskId: *task, AfterSequence: *after})
	if e != nil {
		return e
	}
	for {
		t, e := stream.Recv()
		if e == io.EOF {
			// A resumed watch may already be at the terminal event's cursor.
			// Confirm persisted state before treating an empty stream as success.
			t, e = c.GetTask(ctx, &pb.TaskRequest{NodeId: *node, TaskId: *task})
			if e != nil {
				return fmt.Errorf("watch ended; cannot confirm task state: %w", e)
			}
			if !model.Terminal(t.State) {
				return fmt.Errorf("watch ended before task %s reached a terminal state: %s", t.Id, t.State)
			}
			if command != "watch" {
				printPB(t)
			}
			if t.State != "SUCCEEDED" {
				return fmt.Errorf("task %s: %s: %s", t.Id, t.State, t.Error)
			}
			return nil
		}
		if e != nil {
			return e
		}
		if command == "watch" {
			printPB(t)
		}
		if model.Terminal(t.State) {
			if command != "watch" {
				printPB(t)
			}
			if t.State != "SUCCEEDED" {
				return fmt.Errorf("task %s: %s: %s", t.Id, t.State, t.Error)
			}
			return nil
		}
	}
}
