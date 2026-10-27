package sdnmcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	pb "sdn-agent/api/agent/v1"
	"sdn-agent/internal/adapters/frr"
	"sdn-agent/internal/adapters/linux"
	"sdn-agent/internal/adapters/vpp"
	"sdn-agent/internal/agent"
	"sdn-agent/internal/backend"
	"sdn-agent/internal/store"
	sdnmcp "sdn-agent/mcp"
)

// Run the real Agent, registry and VPP Go driver, with only the Python/VPP
// service replaced by an HTTP-over-Unix-socket fixture. No host commands run.
type vppAdapterFixture struct {
	mu        sync.Mutex
	queries   []*pb.QueryRequest
	paths     []string
	rows      []map[string]any
	paused    bool
	lastError string
	fail      bool
}

func (a *vppAdapterFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.paths = append(a.paths, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if a.fail {
		http.Error(w, "VPP API unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/query":
		var raw json.RawMessage
		q := &pb.QueryRequest{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := protojson.Unmarshal(raw, q); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a.queries = append(a.queries, q)
		_ = json.NewEncoder(w).Encode(a.rows)
	case "/status":
		_ = json.NewEncoder(w).Encode(map[string]any{"paused": a.paused, "lastError": a.lastError})
	default:
		http.Error(w, "fixture rejects mutation", http.StatusMethodNotAllowed)
	}
}

type noHostCommands struct{}

func (noHostCommands) Run(context.Context, ...string) (string, error) {
	return "", fmt.Errorf("test forbids host commands")
}

func vppFixture(t *testing.T, adapter *vppAdapterFixture, writes, withFRR bool) *fixture {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "adapter.sock")
	lis, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: adapter}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { _ = srv.Close() })
	client := vpp.NewClient(socket)
	t.Cleanup(client.HTTP.CloseIdleConnections)
	drivers := []backend.Driver{&vpp.Driver{Linux: &linux.Driver{Runner: noHostCommands{}}, RPC: client}}
	if withFRR {
		drivers = append(drivers, &vpp.RoutingDriver{Driver: &frr.Driver{Runner: noHostCommands{}}})
	}
	registry, err := backend.New(drivers...)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.NewFile("")
	if err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(testNode, db, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	// No worker is started: staging validates intent without applying it.
	return newServiceFixture(t, a, sdnmcp.Options{NodeID: testNode, AllowWrites: writes})
}

func TestMCPVPPDiscoveryExplainsBackendAndStartupBoundary(t *testing.T) {
	f := vppFixture(t, &vppAdapterFixture{}, true, true)
	caps := f.ok("sdn_capabilities", nil)
	encoded, _ := json.Marshal(caps)
	for _, feature := range []string{"dataplane/vpp-26.06", "vxlan-multipoint/v1", "vxlan-dualstack/v1", "evpn-irb/v1", "TENANT_L3VNI", "EVPN_TYPE5", "VXLAN_TUNNELS", "IPV6"} {
		if !strings.Contains(string(encoded), feature) {
			t.Errorf("actual VPP capability missing %s", feature)
		}
	}
	for _, entry := range caps["operations"].([]any) {
		op := object(t, entry)
		if op["code"] == "BD" && !reflect.DeepEqual(op["backends"], []any{"vpp"}) {
			t.Fatalf("VPP capability advertised a second writer: %v", op)
		}
	}
	instructions := f.client.InitializeResult().Instructions
	for _, part := range []string{"source=vpp", "source=frr", "startup.conf", "no MCP operation", "L3VNI"} {
		if !strings.Contains(instructions, part) {
			t.Errorf("initialization guidance missing %s", part)
		}
	}
	listed, err := f.client.ListTools(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		schema := object(t, tool.InputSchema)
		properties := object(t, schema["properties"])
		if tool.Name == "sdn_query" {
			family := object(t, properties["family"])
			neighbor := object(t, properties["neighbor"])
			if !strings.Contains(family["description"].(string), "outer VTEP") || !strings.Contains(neighbor["description"].(string), "remote VTEP") {
				t.Fatal("VPP address-family/remote-filter semantics missing")
			}
			if _, exists := properties["source"]; exists {
				t.Fatal("caller can select a different backend")
			}
		}
		if tool.Name == "sdn_stage" {
			operation := object(t, object(t, properties["operations"])["items"])
			vxlan := object(t, object(t, operation["properties"])["vxlan"])
			if !strings.Contains(vxlan["description"].(string), "source-only multipoint") {
				t.Fatal("VPP source-only VXLAN semantics missing")
			}
		}
	}
}

func TestMCPVPPQueryThroughAgentAndUnixAdapter(t *testing.T) {
	for _, tc := range []struct{ family, local, remote string }{
		{"IPV4", "192.0.2.1", "192.0.2.2"},
		{"IPV6", "2001:db8::1", "2001:db8::2"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			remotes := []any{
				map[string]any{"remote": tc.remote, "mac": "02:00:00:00:ff:02", "bum": false},
				map[string]any{"remote": tc.remote, "mac": "00:00:00:00:00:00", "bum": true},
			}
			row := map[string]any{"name": "vx50000", "vppName": "vxlan_tunnel0", "swIfIndex": 8, "vni": 50000, "bridge": "br50000", "vrf": "TENANT-A", "local": tc.local, "family": tc.family, "multipoint": true, "remoteCount": 1, "bumCount": 1, "remotes": remotes}
			adapter := &vppAdapterFixture{rows: []map[string]any{row}}
			f := vppFixture(t, adapter, false, true)
			args := map[string]any{"kind": "VXLAN_TUNNELS", "family": tc.family, "vrf": "TENANT-A", "bridge": "br50000", "interface": "vx50000", "neighbor": tc.remote, "page_size": 1}
			out := f.ok("sdn_query", args)
			if out["source"] != "vpp" || out["revision"] != "0" {
				t.Fatalf("incorrect backend/revision: %v", out)
			}
			records := out["records"].([]any)
			if len(records) != 1 || !reflect.DeepEqual(object(t, records[0]), object(t, row)) {
				t.Fatalf("nested VPP record or false BUM flag changed: %v", records)
			}
			adapter.mu.Lock()
			defer adapter.mu.Unlock()
			q := adapter.queries[0]
			if q.NodeId != testNode || q.Family.String() != tc.family || q.Neighbor != tc.remote || q.Bridge != "br50000" || q.Interface != "vx50000" || q.Vrf != "TENANT-A" {
				t.Fatalf("VPP filters changed: %v", q)
			}
		})
	}
}

func TestMCPVPPPaginationUsesAgentFingerprint(t *testing.T) {
	adapter := &vppAdapterFixture{rows: []map[string]any{{"name": "vx10010", "remoteCount": 0, "remotes": []any{}}, {"name": "vx10020", "remoteCount": 0, "remotes": []any{}}}}
	f := vppFixture(t, adapter, false, false)
	args := map[string]any{"kind": "VXLAN_TUNNELS", "page_size": 1}
	first := f.ok("sdn_query", args)
	if first["next_page_token"] == "" {
		t.Fatal("missing real Agent pagination token")
	}
	args["page_token"] = first["next_page_token"]
	second := f.ok("sdn_query", args)
	if second["next_page_token"] != "" || object(t, first["records"].([]any)[0])["name"] == object(t, second["records"].([]any)[0])["name"] {
		t.Fatal("VPP pages repeated or failed to terminate")
	}
	adapter.mu.Lock()
	adapter.rows[0]["remoteCount"] = 1
	adapter.mu.Unlock()
	r, err := f.call("sdn_query", args)
	rejected(t, r, err)
	if r == nil || object(t, r.StructuredContent)["code"] != "Aborted" {
		t.Fatalf("changed VPP data did not invalidate cursor: %v %v", r, err)
	}
}

func TestMCPVPPStagingUsesRealBackendValidation(t *testing.T) {
	adapter := &vppAdapterFixture{}
	f := vppFixture(t, adapter, true, true)
	begin := f.ok("sdn_begin", map[string]any{"base_revision": "0", "request_id": "vpp-irb-candidate"})
	ops := []any{
		map[string]any{"id": "tenant", "code": "TENANT", "action": "CREATE", "tenant": map[string]any{"name": "TENANT-A", "table_id": 1001}},
		map[string]any{"id": "bd", "code": "BD", "action": "CREATE", "bridge": map[string]any{"name": "br50000", "stp": false, "ageing_time": 0, "mac": "02:00:00:00:ff:01"}},
		map[string]any{"id": "binding", "code": "BD_TENANT", "action": "BIND", "depends_on": []any{"tenant", "bd"}, "binding": map[string]any{"bridge": "br50000", "tenant": "TENANT-A"}},
		map[string]any{"id": "vx", "code": "VXLAN", "action": "CREATE", "vxlan": map[string]any{"name": "vx50000", "vni": 50000, "local": "2001:db8::1", "underlay": "uplink", "learning": false}},
		map[string]any{"id": "port", "code": "BD_PORT", "action": "BIND", "depends_on": []any{"binding", "vx"}, "bridge_port": map[string]any{"bridge": "br50000", "port": "vx50000", "learning": false, "neigh_suppress": true}},
		map[string]any{"id": "l3", "code": "TENANT_L3VNI", "action": "BIND", "depends_on": []any{"port"}, "l3vni": map[string]any{"tenant": "TENANT-A", "vni": 50000}},
	}
	staged := f.ok("sdn_stage", map[string]any{"transaction_id": begin["id"], "operations": ops})
	encoded, _ := json.Marshal(staged)
	tx := &pb.Transaction{}
	if err := protojson.Unmarshal(encoded, tx); err != nil {
		t.Fatal(err)
	}
	for _, op := range tx.Operations {
		if b := op.GetBridge(); b != nil && (b.Stp == nil || *b.Stp || b.AgeingTime == nil || *b.AgeingTime != 0) {
			t.Fatal("VPP optional false/zero changed")
		}
		if v := op.GetVxlan(); v != nil && (v.Local != "2001:db8::1" || v.Learning) {
			t.Fatal("source-only IPv6 VXLAN changed")
		}
	}
	for _, bad := range []map[string]any{
		{"id": "unsupported-stp", "code": "BD", "action": "CREATE", "bridge": map[string]any{"name": "br-bad", "stp": true}},
		{"id": "unsupported-tenant-peer", "code": "BGP_NEIGHBOR", "action": "CREATE", "peer": map[string]any{"asn": 65000, "vrf": "TENANT-A", "name": "192.0.2.2", "remote_as": 65001}},
	} {
		r, err := f.call("sdn_stage", map[string]any{"transaction_id": begin["id"], "operations": []any{bad}})
		rejected(t, r, err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.paths) != 0 {
		t.Fatalf("staging executed adapter operations: %v", adapter.paths)
	}
}

func TestMCPVPPUnhealthyObservationsAndUnavailableAPI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adapter *vppAdapterFixture
	}{
		{"recovery", &vppAdapterFixture{paused: true}},
		{"sync", &vppAdapterFixture{lastError: "remote table sync incomplete"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := vppFixture(t, tc.adapter, false, false)
			r, err := f.call("sdn_state", nil)
			rejected(t, r, err)
			if r == nil {
				t.Fatal(err)
			}
			out := object(t, r.StructuredContent)
			if out["code"] != "FailedPrecondition" || out["observation_error"] == "" || object(t, out["observations"])["vpp/status"] == nil || out["revision"] != "0" {
				t.Fatalf("sync failure evidence lost: %v", out)
			}
		})
	}
	f := vppFixture(t, &vppAdapterFixture{fail: true}, false, false)
	r, err := f.call("sdn_query", map[string]any{"kind": "VXLAN_TUNNELS"})
	rejected(t, r, err)
	if r == nil || object(t, r.StructuredContent)["code"] != "FailedPrecondition" {
		t.Fatal("unavailable adapter presented as successful empty table")
	}
}

func TestMCPIncompleteQueriesPreserveEvidence(t *testing.T) {
	for _, source := range []string{"vpp", "frr", "linux"} {
		for _, stale := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stale=%v", source, stale), func(t *testing.T) {
				row, _ := structpb.NewStruct(map[string]any{"name": "evidence"})
				response := &pb.QueryResponse{NodeId: testNode, Kind: pb.QueryKind_INTERFACES, Source: source, Stale: stale, Records: []*structpb.Struct{row}, Revision: 42}
				if !stale {
					response.Error = "incomplete readback"
				}
				a := &testAgent{hooks: map[string]func(context.Context, proto.Message) (proto.Message, error){"Query": func(context.Context, proto.Message) (proto.Message, error) { return response, nil }}}
				f := newFixture(t, a, sdnmcp.Options{NodeID: testNode})
				r, err := f.call("sdn_query", map[string]any{"kind": "INTERFACES"})
				rejected(t, r, err)
				if r == nil {
					t.Fatal(err)
				}
				out := object(t, r.StructuredContent)
				if out["source"] != source || out["revision"] != "42" || len(out["records"].([]any)) != 1 || out["code"] != "FailedPrecondition" || out["stale"] != stale || out["error"] != response.Error {
					t.Fatalf("query evidence lost: %v", out)
				}
			})
		}
	}
}

func TestMCPRejectsStartupAndDataplaneOverrides(t *testing.T) {
	f := newFixture(t, nil, sdnmcp.Options{NodeID: testNode, AllowWrites: true})
	for _, extra := range []map[string]any{
		{"startup_config": "/etc/vpp/startup.conf"},
		{"command": "show vxlan tunnel"},
		{"source": "vpp"},
		{"dataplane": "vpp"},
	} {
		extra["kind"] = "VXLAN_TUNNELS"
		r, err := f.call("sdn_query", extra)
		rejected(t, r, err)
	}
	r, err := f.call("sdn_stage", map[string]any{"transaction_id": "tx-1", "operations": []any{
		map[string]any{"id": "vx", "code": "VXLAN", "action": "CREATE", "vxlan": map[string]any{"name": "vx1", "vni": 10100, "local": "192.0.2.1", "dst": "192.0.2.2"}},
	}})
	rejected(t, r, err)
	if len(f.agent.requests("Query")) != 0 || len(f.agent.writeMethods()) != 0 {
		t.Fatal("unsupported control input reached Agent")
	}
}
