package linux

import (
	"context"
	"fmt"
	"testing"

	pb "sdn-agent/api/agent/v1"
)

type routeOutput string

func (r routeOutput) Run(context.Context, ...string) (string, error) { return string(r), nil }

func TestRouteReadbackNormalizesIPRoute2Destinations(t *testing.T) {
	for _, tc := range []struct{ prefix, dst, gateway string }{
		{"198.51.100.42/32", "198.51.100.42", "192.0.2.1"},
		{"2001:db8::42/128", "2001:db8::42", "2001:db8::1"},
		{"0.0.0.0/0", "default", "192.0.2.1"},
		{"::/0", "default", "2001:db8::1"},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			d := &Driver{Runner: routeOutput(fmt.Sprintf(`[{"dst":%q,"gateway":%q,"dev":"uplink"}]`, tc.dst, tc.gateway))}
			op := &pb.Operation{Code: pb.OpCode_STATIC_ROUTE, Action: pb.Action_CREATE,
				Data: &pb.Operation_Route{Route: &pb.Route{Prefix: tc.prefix, Gateway: tc.gateway, Interface: "uplink"}}}
			if err := d.Verify(context.Background(), op); err != nil {
				t.Fatal(err)
			}
			op.GetRoute().Gateway = "203.0.113.1"
			if err := d.Verify(context.Background(), op); err == nil {
				t.Fatal("conflicting next-hop accepted")
			}
		})
	}
}
