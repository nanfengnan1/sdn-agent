package linux

import (
	"context"
	pb "sdn-agent/api/agent/v1"
	"testing"
)

func TestVXLANIPv6Readback(t *testing.T) {
	d := &Driver{Runner: routeOutput(`[{"ifname":"vx0","flags":["UP"],"linkinfo":{"info_kind":"vxlan","info_data":{"id":10010,"local6":"2001:db8::1","port":4789,"learning":false}}}]`)}
	op := &pb.Operation{Code: pb.OpCode_VXLAN, Action: pb.Action_CREATE, Data: &pb.Operation_Vxlan{Vxlan: &pb.Vxlan{Name: "vx0", Vni: 10010, Local: "2001:0db8:0:0::1"}}}
	if err := d.Verify(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	op.GetVxlan().Local = "2001:db8::2"
	if err := d.Verify(context.Background(), op); err == nil {
		t.Fatal("wrong IPv6 VTEP accepted")
	}
}

func TestLargeVXLANAndVRFIDsUseDecimalReadback(t *testing.T) {
	d := &Driver{Runner: routeOutput("[{\"ifname\":\"vx0\",\"linkinfo\":{\"info_kind\":\"vxlan\",\"info_data\":{\"id\":16777002,\"local\":\"192.0.2.1\",\"port\":4789,\"learning\":false}}},{\"ifname\":\"OUTER\",\"linkinfo\":{\"info_kind\":\"vrf\",\"info_data\":{\"table\":4294967294}}}]")}
	for _, op := range []*pb.Operation{
		{Code: pb.OpCode_VXLAN, Action: pb.Action_CREATE, Data: &pb.Operation_Vxlan{Vxlan: &pb.Vxlan{Name: "vx0", Vni: 16777002, Local: "192.0.2.1", Port: 4789}}},
		{Code: pb.OpCode_TENANT, Action: pb.Action_CREATE, Data: &pb.Operation_Tenant{Tenant: &pb.Tenant{Name: "OUTER", TableId: 4294967294}}},
	} {
		if err := d.Verify(context.Background(), op); err != nil {
			t.Fatal(err)
		}
	}
}
