package backend_test

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/frr"
	"sdn-agent/internal/adapters/linux"
	"sdn-agent/internal/backend"
)

func TestCapabilitiesCoverProtocolOperations(t *testing.T) {
	r, err := backend.New(&linux.Driver{}, &frr.Driver{})
	if err != nil {
		t.Fatal(err)
	}
	caps := r.Capabilities("node-1")
	covered := map[pb.OpCode]bool{}
	for _, c := range caps.Operations {
		if covered[c.Code] {
			t.Fatalf("duplicate capability %s", c.Code)
		}
		covered[c.Code] = true
		if !c.Rollback || len(c.Backends) == 0 {
			t.Fatalf("incomplete capability %v", c)
		}
		if c.Code == pb.OpCode_TENANT && !reflect.DeepEqual(c.Backends, []string{"linux", "frr"}) {
			t.Fatalf("tenant backends: %v", c.Backends)
		}
	}
	for n, name := range pb.OpCode_name {
		if pb.OpCode(n) == pb.OpCode_NEIGHBOR_KEEPALIVE {
			if covered[pb.OpCode(n)] {
				t.Fatal("Linux must not advertise VPP neighbor keepalive")
			}
			continue
		}
		if n != 0 && !covered[pb.OpCode(n)] {
			t.Errorf("protocol operation %s has no backend", name)
		}
	}
	if caps.NodeId != "node-1" || caps.ProtocolVersion != 1 {
		t.Fatalf("wrong node/protocol: %v", caps)
	}
}

func TestPlanNetworkLifecycleDependencies(t *testing.T) {
	r, err := backend.New(&linux.Driver{}, &frr.Driver{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		steps      []string
	}{
		{"create", `{"operations":[
			{"id":"port","code":"BD_PORT","action":"BIND","depends_on":["vxlan","bridge"],"bridge_port":{"bridge":"br0","port":"vx0"}},
			{"id":"binding","code":"BD_TENANT","action":"BIND","depends_on":["bridge","tenant"],"binding":{"bridge":"br0","tenant":"blue"}},
			{"id":"vxlan","code":"VXLAN","action":"CREATE","depends_on":["binding"],"vxlan":{"name":"vx0","vni":10010,"local":"192.0.2.1"}},
			{"id":"bridge","code":"BD","action":"CREATE","depends_on":["tenant"],"bridge":{"name":"br0"}},
			{"id":"tenant","code":"TENANT","action":"CREATE","tenant":{"name":"blue","table_id":1001}}
		]}`, []string{"tenant/linux", "tenant/frr", "bridge/linux", "binding/linux", "vxlan/linux", "port/linux"}},
		{"delete", `{"operations":[
			{"id":"tenant","code":"TENANT","action":"DELETE","depends_on":["bridge"],"tenant":{"name":"blue","table_id":1001}},
			{"id":"bridge","code":"BD","action":"DELETE","depends_on":["binding"],"bridge":{"name":"br0"}},
			{"id":"binding","code":"BD_TENANT","action":"UNBIND","depends_on":["vxlan"],"binding":{"bridge":"br0","tenant":"blue"}},
			{"id":"vxlan","code":"VXLAN","action":"DELETE","depends_on":["port"],"vxlan":{"name":"vx0","vni":10010,"local":"192.0.2.1"}},
			{"id":"port","code":"BD_PORT","action":"UNBIND","bridge_port":{"bridge":"br0","port":"vx0"}}
		]}`, []string{"port/linux", "vxlan/linux", "binding/linux", "bridge/linux", "tenant/linux", "tenant/frr"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req pb.StageRequest
			if err := protojson.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			plan, err := r.Plan(req.Operations)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, step := range plan {
				ids = append(ids, step.ID)
			}
			if !reflect.DeepEqual(ids, tc.steps) {
				t.Fatalf("dependency/backend ordering: %v want %v", ids, tc.steps)
			}
			// A dependency error must prevent construction of any executable plan.
			req.Operations[0].DependsOn = append(req.Operations[0].DependsOn, "missing")
			if plan, err = r.Plan(req.Operations); err == nil || len(plan) != 0 {
				t.Fatalf("missing dependency yielded executable plan: %v %v", plan, err)
			}
		})
	}
}
