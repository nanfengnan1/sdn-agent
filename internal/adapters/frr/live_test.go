package frr

import (
	"context"
	"google.golang.org/protobuf/proto"
	"os"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/backend"
	"strings"
	"testing"
	"time"
)

// Opt-in only. The caller must create an exclusive, disposable FRR container.
// Every configuration case is restored from its own readback before returning.
type liveRunner struct{ container string }

func (r liveRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) > 0 && strings.HasSuffix(args[0], "frr-reload.py") {
		path := args[len(args)-1]
		if _, e := (backend.Exec{Timeout: 20 * time.Second}).Run(ctx, "docker", "cp", path, r.container+":"+path); e != nil {
			return "", e
		}
	}
	return (backend.Exec{Timeout: 20 * time.Second}).Run(ctx, append([]string{"docker", "exec", r.container}, args...)...)
}
func TestLiveFRRAddedOperations(t *testing.T) {
	container := os.Getenv("SDN_AGENT_FRR_CONTAINER")
	if container == "" {
		t.Skip("set SDN_AGENT_FRR_CONTAINER to an exclusive disposable container")
	}
	if !strings.HasPrefix(container, "sdn-agent-feature-") {
		t.Fatal("live tests require own sdn-agent-feature-* container")
	}
	ctx := context.Background()
	d := &Driver{Runner: liveRunner{container}}
	for _, tc := range frrCases() {
		if tc.code != pb.OpCode_OSPF6_INSTANCE && tc.code != pb.OpCode_OSPF6_INTERFACE && tc.code != pb.OpCode_BGP_AF_PEER && tc.code != pb.OpCode_BGP_NETWORK && tc.code != pb.OpCode_BGP_REDISTRIBUTE && tc.code != pb.OpCode_EVPN_TYPE5 {
			continue
		}
		t.Run(tc.code.String(), func(t *testing.T) {
			o := frrOp(t, tc, pb.Action_SET)
			switch o.Code {
			case pb.OpCode_OSPF6_INSTANCE:
				o.GetOspf().RouterId = "100.64.0.1"
			case pb.OpCode_OSPF6_INTERFACE:
				o.GetOspf6Interface().Name = "lo"
				o.GetOspf6Interface().Passive = proto.Bool(true)
				o.GetOspf6Interface().Cost = proto.Uint32(17)
				o.GetOspf6Interface().Area = "0.0.0.0"
			case pb.OpCode_BGP_AF_PEER:
				o.GetPeer().Active = proto.Bool(false)
			case pb.OpCode_BGP_NETWORK:
				o.GetBgpNetwork().Prefix = "2001:db8:100::/64"
			case pb.OpCode_BGP_REDISTRIBUTE:
				o.GetBgpRedistribute().Protocol = "static"
			case pb.OpCode_EVPN_TYPE5:
				o.GetBgpType5().Vrf = "BLUE"
				o.GetBgpType5().Enabled = proto.Bool(true)
			}
			undo, e := d.Prepare(ctx, o)
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if e := d.Rollback(ctx, o, undo); e != nil {
					t.Errorf("rollback: %v", e)
				}
			}()
			if e = d.Apply(ctx, o); e != nil {
				t.Fatal(e)
			}
			if e = d.Verify(ctx, o); e != nil {
				t.Fatal(e)
			}
		})
	}
	for _, q := range []*pb.QueryRequest{{Kind: pb.QueryKind_EVPN_VNI}, {Kind: pb.QueryKind_EVPN_ROUTES}, {Kind: pb.QueryKind_BGP_ROUTES, Family: pb.AddressFamily_IPV6}, {Kind: pb.QueryKind_BGP_NEIGHBORS}, {Kind: pb.QueryKind_OSPF_SUMMARY, OspfVersion: 3}, {Kind: pb.QueryKind_OSPF_INTERFACES, OspfVersion: 3}, {Kind: pb.QueryKind_OSPF_NEIGHBORS, OspfVersion: 3}, {Kind: pb.QueryKind_OSPF_DATABASE, OspfVersion: 3}, {Kind: pb.QueryKind_OSPF_ROUTES, OspfVersion: 3}} {
		if _, e := d.Query(ctx, q); e != nil {
			t.Errorf("query %s: %v", q.Kind, e)
		}
	}
}
