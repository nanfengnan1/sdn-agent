package frr

import (
	"context"
	pb "sdn-agent/api/agent/v1"
	"testing"
)

type runner struct{ text string }

func TestSuccessfulCommitNotesDoNotHideSubsequentErrors(t *testing.T) {
	for _, tc := range []struct {
		output string
		failed bool
	}{
		{"% Configuration applied with notes:\nL3VNI will become active when BGP starts\n", false},
		{"% Configuration applied with notes:\n% Unknown command\n", true},
		{"% Configuration applied with notes:\nERROR: failed\n", true},
	} {
		d := &Driver{Runner: runner{text: tc.output}}
		_, err := d.cli(context.Background(), "configure terminal")
		if (err != nil) != tc.failed {
			t.Fatalf("output %q: %v", tc.output, err)
		}
	}
}

func (r runner) Run(context.Context, ...string) (string, error) { return r.text, nil }
func TestVerifyScopesTenantAndAddressFamily(t *testing.T) {
	d := &Driver{Runner: runner{text: "frr version 10\nrouter bgp 65000\n address-family l2vpn evpn\n  rd 10.0.0.1:1\n exit-address-family\nexit\nrouter bgp 65000 vrf A\n address-family l2vpn evpn\n  rd 10.0.0.1:2\n exit-address-family\nexit\nend\n"}}
	o := &pb.Operation{Code: pb.OpCode_EVPN_TENANT, Action: pb.Action_SET, Data: &pb.Operation_Evpn{Evpn: &pb.Evpn{Asn: 65000, Vrf: "A", Rd: "10.0.0.1:1"}}}
	if e := d.Verify(context.Background(), o); e == nil {
		t.Fatal("matched RD from wrong BGP instance")
	}
	o.GetEvpn().Rd = "10.0.0.1:2"
	if e := d.Verify(context.Background(), o); e != nil {
		t.Fatal(e)
	}
}
func TestRejectCLIInjectionAndWrongPayload(t *testing.T) {
	d := &Driver{}
	for _, s := range []string{"A\nend\nrouter bgp 1", "A; touch /tmp/x", "--help", "x y"} {
		o := &pb.Operation{Code: pb.OpCode_TENANT, Action: pb.Action_CREATE, Data: &pb.Operation_Tenant{Tenant: &pb.Tenant{Name: s}}}
		if d.Validate(o) == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	if d.Validate(&pb.Operation{Code: pb.OpCode_BGP_INSTANCE, Action: pb.Action_SET}) == nil {
		t.Fatal("missing oneof accepted")
	}
	if d.Validate(&pb.Operation{Code: pb.OpCode_BGP_INSTANCE, Action: pb.Action_BIND, Data: &pb.Operation_Bgp{Bgp: &pb.Bgp{Asn: 65000, RouterId: "10.0.0.1"}}}) == nil {
		t.Fatal("invalid action accepted")
	}
}

func TestMultipleRouteTargetsInOneConfigLine(t *testing.T) {
	d := &Driver{Runner: runner{text: "frr version 10\nrouter bgp 65000 vrf A\n address-family l2vpn evpn\n  route-target both 65000:1 65000:2\n exit-address-family\nexit\nend\n"}}
	o := &pb.Operation{Code: pb.OpCode_EVPN_TENANT, Action: pb.Action_SET, Data: &pb.Operation_Evpn{Evpn: &pb.Evpn{Asn: 65000, Vrf: "A", ImportRts: []string{"65000:1", "65000:2"}, ExportRts: []string{"65000:2"}}}}
	if e := d.Verify(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	o.GetEvpn().ExportRts = []string{"65000:3"}
	if e := d.Verify(context.Background(), o); e == nil {
		t.Fatal("missing RT accepted")
	}
}

type effectiveRunner struct{}

func (effectiveRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) > 2 && args[2] == "show running-config" {
		return "frr version 10\nrouter bgp 65000\n address-family l2vpn evpn\n  vni 10010\n   rd 10.0.0.1:10\n  exit-vni\n exit-address-family\nexit\nend\n", nil
	}
	return `{"vni":10010,"importRts":["65000:10010"],"exportRts":["65000:10010"]}`, nil
}
func TestImplicitAutoRTRequiresEffectiveReadback(t *testing.T) {
	d := &Driver{Runner: effectiveRunner{}}
	o := &pb.Operation{Code: pb.OpCode_EVPN_L2VNI, Action: pb.Action_SET, Data: &pb.Operation_Evpn{Evpn: &pb.Evpn{Asn: 65000, Vni: 10010, Rd: "10.0.0.1:10", ImportRts: []string{"65000:10010"}, ExportRts: []string{"65000:10010"}}}}
	if e := d.Verify(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	o.GetEvpn().ImportRts = []string{"65000:999"}
	if e := d.Verify(context.Background(), o); e == nil {
		t.Fatal("incorrect effective RT accepted")
	}
}
