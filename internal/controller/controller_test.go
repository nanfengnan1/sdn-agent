package controller

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/store"
	"testing"
)

func TestIdentityCollisionRetryAndSession(t *testing.T) {
	s, _ := store.NewFile("")
	c := &Controller{Store: s}
	ctx := context.Background()
	a, e := c.Register(ctx, &pb.Registration{ProposedNodeId: "candidate", RegistrationId: "device-a", InstanceId: "boot-a"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Register(ctx, &pb.Registration{ProposedNodeId: "candidate", RegistrationId: "device-b", InstanceId: "boot-b"})
	if e != nil || a.NodeId == b.NodeId {
		t.Fatal(a, b, e)
	}
	again, e := c.Register(ctx, &pb.Registration{ProposedNodeId: "candidate", RegistrationId: "device-b", InstanceId: "boot-c"})
	if e != nil || again.NodeId != b.NodeId {
		t.Fatal(again, e)
	}
	if _, e = c.Heartbeat(ctx, &pb.Heartbeat{NodeId: b.NodeId, InstanceId: "boot-b"}); status.Code(e) != codes.FailedPrecondition {
		t.Fatal(e)
	}
	if _, e = c.Heartbeat(ctx, &pb.Heartbeat{NodeId: b.NodeId, InstanceId: "boot-c"}); e != nil {
		t.Fatal(e)
	}
}
