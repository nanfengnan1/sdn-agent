package linux

import (
	"google.golang.org/protobuf/proto"
	pb "sdn-agent/api/agent/v1"
	"testing"
)

func TestTypedValidation(t *testing.T) {
	d := &Driver{}
	bad := []*pb.Operation{{Code: pb.OpCode_INTERFACE, Action: pb.Action_SET}, {Code: pb.OpCode_INTERFACE, Action: pb.Action_SET, Data: &pb.Operation_Interface{Interface: &pb.Interface{Name: "eth0", Mtu: proto.Uint32(1)}}}, {Code: pb.OpCode_VXLAN, Action: pb.Action_CREATE, Data: &pb.Operation_Vxlan{Vxlan: &pb.Vxlan{Name: "vx1", Vni: 1 << 24, Local: "10.0.0.1"}}}, {Code: pb.OpCode_NODE_NETWORK, Action: pb.Action_SET, Data: &pb.Operation_Network{Network: &pb.Network{Settings: []*pb.Sysctl{{Key: "kernel.core_pattern", Value: "1"}}}}}, {Code: pb.OpCode_BD, Action: pb.Action_CREATE, Data: &pb.Operation_Bridge{Bridge: &pb.Bridge{Name: "br0;reboot"}}}}
	for _, o := range bad {
		if e := d.Validate(o); e == nil {
			t.Fatalf("accepted %v", o)
		}
	}
}
