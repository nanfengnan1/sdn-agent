// Package snort owns versioned rule sets and VPP IPS interface attachments.
// The local runtime renders through confd and acknowledges a completed reload.
package snort

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/model"
)

type RPC interface {
	Call(context.Context, string, any, any) error
}
type Client struct{ HTTP *http.Client }

func NewClient(socket string) *Client {
	return &Client{&http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
}
func (c *Client) Call(ctx context.Context, method string, data, out any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("IPS runtime: %w", err)
	}
	defer res.Body.Close()
	b, err = io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("IPS %s: %s", method, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type Driver struct{ RPC RPC }

func (d *Driver) Name() string       { return "snort" }
func (d *Driver) Codes() []pb.OpCode { return []pb.OpCode{pb.OpCode_IPS_RULE, pb.OpCode_IPS_INTERFACE} }
func (d *Driver) Features() []string { return []string{"snort-ips/v1"} }

var ifname = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$")

func (d *Driver) Validate(o *pb.Operation) error {
	if o == nil {
		return fmt.Errorf("operation required")
	}
	if o.Action != pb.Action_CREATE && o.Action != pb.Action_SET && o.Action != pb.Action_DELETE {
		return fmt.Errorf("IPS requires CREATE, SET or DELETE")
	}
	switch o.Code {
	case pb.OpCode_IPS_RULE:
		x := o.GetIpsRule()
		if x == nil || x.Sid == 0 {
			return fmt.Errorf("IPS rule requires a nonzero SID")
		}
		if o.Action != pb.Action_DELETE {
			if len(x.Rule) == 0 || len(x.Rule) > 16384 || strings.ContainsAny(x.Rule, "\x00\r\n") {
				return fmt.Errorf("IPS rule must be a single line, at most 16384 bytes")
			}
		}
	case pb.OpCode_IPS_INTERFACE:
		x := o.GetIpsInterface()
		if x == nil || !ifname.MatchString(x.Name) {
			return fmt.Errorf("IPS requires a VPP native interface name")
		}
		if o.Action != pb.Action_DELETE && x.Direction != "input" && x.Direction != "output" && x.Direction != "inout" {
			return fmt.Errorf("IPS direction must be input, output or inout")
		}
	default:
		return fmt.Errorf("unsupported IPS operation")
	}
	return nil
}
func operation(o *pb.Operation) json.RawMessage { b, _ := protojson.Marshal(o); return b }
func (d *Driver) Prepare(ctx context.Context, o *pb.Operation) (*model.Undo, error) {
	if e := d.Validate(o); e != nil {
		return nil, e
	}
	var snapshot json.RawMessage
	if e := d.RPC.Call(ctx, "prepare", operation(o), &snapshot); e != nil {
		return nil, e
	}
	return &model.Undo{Data: snapshot}, nil
}
func (d *Driver) Apply(ctx context.Context, o *pb.Operation) error {
	return d.RPC.Call(ctx, "apply", operation(o), nil)
}
func (d *Driver) Verify(ctx context.Context, o *pb.Operation) error {
	return d.RPC.Call(ctx, "verify", operation(o), nil)
}
func (d *Driver) Rollback(ctx context.Context, _ *pb.Operation, u *model.Undo) error {
	if u == nil || len(u.Data) == 0 {
		return fmt.Errorf("IPS undo missing")
	}
	return d.RPC.Call(ctx, "restore", u.Data, nil)
}
func (d *Driver) Observe(ctx context.Context) (map[string]string, error) {
	var status map[string]any
	e := d.RPC.Call(ctx, "status", nil, &status)
	if e != nil {
		return nil, e
	}
	b, _ := json.Marshal(status)
	return map[string]string{"status": string(b)}, nil
}
func (d *Driver) QueryCapabilities() []*pb.QueryCapability {
	return []*pb.QueryCapability{{Kind: pb.QueryKind_IPS_STATUS}, {Kind: pb.QueryKind_IPS_RULES}}
}
func (d *Driver) Query(ctx context.Context, q *pb.QueryRequest) ([]map[string]any, error) {
	if q.Vrf != "" || q.Bridge != "" || q.Interface != "" || q.Family != pb.AddressFamily_AF_UNSPECIFIED || q.Prefix != "" || q.Neighbor != "" {
		return nil, fmt.Errorf("IPS query does not accept network filters")
	}
	if q.Kind == pb.QueryKind_IPS_STATUS {
		var status map[string]any
		e := d.RPC.Call(ctx, "status", nil, &status)
		return []map[string]any{status}, e
	}
	if q.Kind != pb.QueryKind_IPS_RULES {
		return nil, fmt.Errorf("unsupported IPS query")
	}
	var out []map[string]any
	e := d.RPC.Call(ctx, "rules", nil, &out)
	return out, e
}
