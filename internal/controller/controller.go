// Package controller is a persistent mock controller for development and labs.
package controller

import (
	"context"
	"errors"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
	"sdn-agent/internal/store"
	"sync"
	"time"
)

type Node struct {
	NodeID, RegistrationID, InstanceID, Endpoint string
	LastSeen                                     int64
	Revision                                     uint64
	Capabilities                                 *pb.Capabilities
}
type Controller struct {
	pb.UnimplementedControllerServer
	Store store.Store
	mu    sync.Mutex
}

func (c *Controller) Register(ctx context.Context, q *pb.Registration) (*pb.Registered, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if q.RegistrationId == "" || q.InstanceId == "" || q.ProposedNodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "identity fields required")
	}
	var n Node
	e := c.Store.Load(ctx, "registration/"+q.RegistrationId, &n)
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return nil, e
	}
	if errors.Is(e, store.ErrNotFound) {
		id := q.ProposedNodeId
		for {
			var existing Node
			e = c.Store.Load(ctx, "node/"+id, &existing)
			if errors.Is(e, store.ErrNotFound) {
				break
			}
			if e != nil {
				return nil, e
			}
			id = model.ID()
		}
		n = Node{NodeID: id, RegistrationID: q.RegistrationId}
	}
	n.InstanceID = q.InstanceId
	n.Endpoint = q.Endpoint
	n.LastSeen = time.Now().Unix()
	n.Capabilities = q.Capabilities
	if e = c.Store.Atomic(ctx, map[string]any{"registration/" + q.RegistrationId: n, "node/" + n.NodeID: n}); e != nil {
		return nil, e
	}
	return &pb.Registered{NodeId: n.NodeID}, nil
}
func (c *Controller) Heartbeat(ctx context.Context, q *pb.Heartbeat) (*pb.Empty, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n Node
	if e := c.Store.Load(ctx, "node/"+q.NodeId, &n); e != nil {
		return nil, status.Error(codes.NotFound, "unregistered node")
	}
	if n.InstanceID != q.InstanceId {
		return nil, status.Error(codes.FailedPrecondition, "superseded instance")
	}
	n.Revision = q.Revision
	n.Endpoint = q.Endpoint
	n.LastSeen = time.Now().Unix()
	if e := c.Store.Atomic(ctx, map[string]any{"node/" + n.NodeID: n}); e != nil {
		return nil, fmt.Errorf("save heartbeat: %w", e)
	}
	return &pb.Empty{}, nil
}
