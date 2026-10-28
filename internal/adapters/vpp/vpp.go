// Package vpp coordinates Linux control-plane mirrors with the VPP 26.06
// adapter. This driver owns both sides, so Registry must not also load linux.
package vpp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/frr"
	"sdn-agent/internal/adapters/linux"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/model"
)

type RPC interface {
	Call(context.Context, string, any, any) error
}
type Client struct{ HTTP *http.Client }

func NewClient(socket string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
}
func (c *Client) Call(ctx context.Context, method string, data any, out any) error {
	b, e := json.Marshal(data)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", "http://localhost/"+method, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := c.HTTP.Do(req)
	if e != nil {
		return fmt.Errorf("VPP adapter: %w", e)
	}
	defer res.Body.Close()
	b, e = io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if e != nil {
		return e
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("VPP adapter %s: %s", method, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type Driver struct {
	Linux *linux.Driver
	RPC   RPC
}
type undo struct {
	Linux *model.Undo     `json:"linux"`
	VPP   json.RawMessage `json:"vpp"`
}

func (d *Driver) Name() string { return "vpp" }
func (d *Driver) Codes() []pb.OpCode {
	return []pb.OpCode{pb.OpCode_NEIGHBOR_KEEPALIVE, pb.OpCode_NODE_NETWORK, pb.OpCode_INTERFACE, pb.OpCode_ADDRESS, pb.OpCode_TENANT, pb.OpCode_BD, pb.OpCode_BD_PORT, pb.OpCode_BD_TENANT, pb.OpCode_TENANT_L3VNI, pb.OpCode_VXLAN, pb.OpCode_STATIC_ROUTE}
}
func (d *Driver) Features() []string {
	return []string{"dataplane/vpp-26.06", "neighbor-keepalive/v1", "evpn-l2/v1", "evpn-irb/v1", "vxlan-multipoint/v1", "vxlan-dualstack/v1", "vxlan-software-gso/v1", "linux-control-mirror/v1"}
}
func (d *Driver) Validate(o *pb.Operation) error {
	if o.Code == pb.OpCode_NEIGHBOR_KEEPALIVE {
		if o.Action != pb.Action_SET || o.GetNeighborKeepalive() == nil || o.GetNeighborKeepalive().Enabled == nil {
			return fmt.Errorf("VPP neighbor keepalive requires SET and explicit enabled boolean")
		}
		return nil
	}
	if o.Code == pb.OpCode_TENANT_L3VNI {
		return (&frr.Driver{}).Validate(o)
	}
	if e := d.Linux.Validate(o); e != nil {
		return e
	}
	supported := false
	for _, c := range d.Codes() {
		if c == o.Code {
			supported = true
		}
	}
	if !supported {
		return fmt.Errorf("VPP: operation %s is not supported", o.Code)
	}
	if b := o.GetBridge(); b != nil {
		if b.GetStp() {
			return fmt.Errorf("VPP STP is not supported")
		}
		if b.AgeingTime != nil && (b.GetAgeingTime()%60 != 0 || b.GetAgeingTime() > 15300) {
			return fmt.Errorf("VPP MAC ageing must be 0..15300 seconds in multiples of 60")
		}
	}
	return nil
}
func opJSON(o *pb.Operation) (json.RawMessage, error) { b, e := protojson.Marshal(o); return b, e }
func (d *Driver) Prepare(ctx context.Context, o *pb.Operation) (*model.Undo, error) {
	if e := d.Validate(o); e != nil {
		return nil, e
	}
	l := &model.Undo{}
	var e error
	if o.Code != pb.OpCode_TENANT_L3VNI && o.Code != pb.OpCode_NEIGHBOR_KEEPALIVE {
		l, e = d.Linux.Prepare(ctx, o)
	}
	if e != nil {
		return nil, e
	}
	b, e := opJSON(o)
	if e != nil {
		return nil, e
	}
	var before json.RawMessage
	if e = d.RPC.Call(ctx, "prepare", b, &before); e != nil {
		return nil, e
	}
	data, e := json.Marshal(undo{Linux: l, VPP: before})
	return &model.Undo{Data: data}, e
}
func (d *Driver) Apply(ctx context.Context, o *pb.Operation) error {
	if o.Code != pb.OpCode_TENANT_L3VNI && o.Code != pb.OpCode_NEIGHBOR_KEEPALIVE {
		if e := d.Linux.Apply(ctx, o); e != nil {
			return e
		}
	}
	b, e := opJSON(o)
	if e != nil {
		return e
	}
	return d.RPC.Call(ctx, "apply", b, nil)
}
func (d *Driver) Verify(ctx context.Context, o *pb.Operation) error {
	if o.Code != pb.OpCode_TENANT_L3VNI && o.Code != pb.OpCode_NEIGHBOR_KEEPALIVE {
		if e := d.Linux.Verify(ctx, o); e != nil {
			return e
		}
	}
	b, e := opJSON(o)
	if e != nil {
		return e
	}
	return d.RPC.Call(ctx, "verify", b, nil)
}
func (d *Driver) Rollback(ctx context.Context, o *pb.Operation, u *model.Undo) error {
	var before undo
	if u == nil {
		return fmt.Errorf("missing VPP Undo")
	}
	if e := json.Unmarshal(u.Data, &before); e != nil {
		return e
	}
	if before.Linux == nil || len(before.VPP) == 0 {
		return fmt.Errorf("incomplete VPP Undo")
	}
	if e := d.RPC.Call(ctx, "rollback", before.VPP, nil); e != nil {
		return e
	}
	if o.Code != pb.OpCode_TENANT_L3VNI && o.Code != pb.OpCode_NEIGHBOR_KEEPALIVE {
		if e := d.Linux.Rollback(ctx, o, before.Linux); e != nil {
			return e
		}
	}
	b, e := opJSON(o)
	if e != nil {
		return e
	}
	return d.RPC.Call(ctx, "verify_rollback", b, nil)
}
func (d *Driver) Observe(ctx context.Context) (map[string]string, error) {
	var v json.RawMessage
	e := d.RPC.Call(ctx, "status", nil, &v)
	if e == nil {
		var status struct {
			LastError string `json:"lastError"`
			Paused    bool   `json:"paused"`
		}
		if err := json.Unmarshal(v, &status); err != nil {
			e = err
		} else if status.LastError != "" {
			e = fmt.Errorf("VPP synchronization: %s", status.LastError)
		} else if status.Paused {
			e = fmt.Errorf("VPP synchronization awaits transaction recovery")
		}
	}
	return map[string]string{"status": string(v)}, e
}
func (d *Driver) QueryCapabilities() []*pb.QueryCapability {
	var out []*pb.QueryCapability
	for _, k := range []pb.QueryKind{pb.QueryKind_NEIGHBOR_KEEPALIVE_STATUS, pb.QueryKind_TENANTS, pb.QueryKind_BRIDGES, pb.QueryKind_PORTS, pb.QueryKind_INTERFACES, pb.QueryKind_ADDRESSES, pb.QueryKind_NEIGHBORS, pb.QueryKind_ROUTES, pb.QueryKind_FDB, pb.QueryKind_VXLAN_TUNNELS} {
		capability := &pb.QueryCapability{Kind: k}
		if k == pb.QueryKind_NEIGHBOR_KEEPALIVE_STATUS || k == pb.QueryKind_ADDRESSES || k == pb.QueryKind_NEIGHBORS || k == pb.QueryKind_ROUTES || k == pb.QueryKind_VXLAN_TUNNELS {
			capability.Families = []pb.AddressFamily{pb.AddressFamily_IPV4, pb.AddressFamily_IPV6}
		}
		out = append(out, capability)
	}
	return out
}
func (d *Driver) Query(ctx context.Context, q *pb.QueryRequest) ([]map[string]any, error) {
	b, e := protojson.Marshal(q)
	if e != nil {
		return nil, e
	}
	var rows []map[string]any
	e = d.RPC.Call(ctx, "query", json.RawMessage(b), &rows)
	return rows, e
}

// RoutingDriver uses the same FRR control plane with VPP-owned forwarding.
type RoutingDriver struct{ *frr.Driver }

func (d *RoutingDriver) Codes() []pb.OpCode {
	return d.Driver.Codes()
}
func (d *RoutingDriver) Validate(o *pb.Operation) error {
	// Tenant SVIs are isolated control mirrors. Until a BVI punt channel is
	// implemented, accepting protocol sessions on them would claim a path
	// that cannot carry control packets. EVPN tenant policy/Type-5 is supported.
	if p := o.GetPeer(); p != nil && p.Vrf != "" {
		return fmt.Errorf("VPP tenant BGP sessions require a BVI control-packet channel which is not implemented")
	}
	if p := o.GetOspf(); p != nil && p.Vrf != "" {
		return fmt.Errorf("VPP tenant OSPF sessions are not implemented")
	}
	if p := o.GetOspfInterface(); p != nil && p.Vrf != "" {
		return fmt.Errorf("VPP tenant OSPF sessions are not implemented")
	}
	if p := o.GetOspf6Interface(); p != nil && p.Vrf != "" {
		return fmt.Errorf("VPP tenant OSPF6 sessions are not implemented")
	}
	return d.Driver.Validate(o)
}

var _ backend.Driver = (*Driver)(nil)
