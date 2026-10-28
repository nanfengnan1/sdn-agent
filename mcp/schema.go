package sdnmcp

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	pb "sdn-agent/api/agent/v1"
)

// Schemas are derived from the public protocol, not the Go struct JSON tags.
// Protobuf uint64 values deliberately accept only decimal strings here.
type schema = map[string]any

func messageSchema(d protoreflect.MessageDescriptor) schema {
	properties := map[string]any{}
	for i := 0; i < d.Fields().Len(); i++ {
		f := d.Fields().Get(i)
		properties[string(f.Name())] = fieldSchema(f)
	}
	out := schema{"type": "object", "properties": properties, "additionalProperties": false}
	if description := messageDescriptions[d.FullName()]; description != "" {
		out["description"] = description
	}
	if d.FullName() == "sdn.agent.v1.Operation" {
		out["required"] = []string{"id", "code", "action"}
		properties["id"].(schema)["minLength"] = 1
		var variants []any
		for _, item := range operationPayloads {
			variants = append(variants, schema{"properties": schema{"code": schema{"const": item.code.String()}}, "required": []string{item.field}})
		}
		out["oneOf"] = variants
		// Exactly one real oneof payload must be present. protojson performs the
		// same check again while decoding the request.
		var payloads []any
		oneof := d.Oneofs().ByName("data")
		for i := 0; i < oneof.Fields().Len(); i++ {
			payloads = append(payloads, schema{"required": []string{string(oneof.Fields().Get(i).Name())}})
		}
		out["allOf"] = []any{schema{"oneOf": payloads}}
	}
	return out
}

func fieldSchema(f protoreflect.FieldDescriptor) schema {
	if f.IsMap() {
		return schema{"type": "object", "additionalProperties": scalarSchema(f.MapValue())}
	}
	s := scalarSchema(f)
	if description := fieldDescriptions[f.FullName()]; description != "" {
		s["description"] = description
	}
	if f.IsList() {
		return schema{"type": "array", "items": s, "maxItems": 1000}
	}
	return s
}

// Explain backend semantics in tools/list without narrowing the shared protocol
// to one dataplane. Actual support and validation remain authoritative in Agent.
var messageDescriptions = map[protoreflect.FullName]string{
	"sdn.agent.v1.Operation": "Typed network intent for the configured Agent backend. Read capabilities first. Does not accept shell commands, VPP CLI, startup files or dataplane changes.",
	"sdn.agent.v1.Tenant":    "Linux VRF; in VPP mode also owns independent IPv4/IPv6 tenant FIBs. The tenant table ID is distinct from a VNI or BD ID.",
	"sdn.agent.v1.Bridge":    "Linux bridge; in VPP mode maps to an owned BD and, when tenant-bound, a BVI. VPP does not support STP.",
	"sdn.agent.v1.Vxlan":     "EVPN VXLAN object. In VPP mode one source-only multipoint interface per VNI; remote endpoints come from EVPN. No dst, remote or is_l3 input is supported.",
	"sdn.agent.v1.L3Vni":     "Associate a tenant with an existing L3VNI. For VPP symmetric IRB first create the tenant-bound transport BD/BVI, Router MAC and Ethernet VXLAN port; this association alone does not create the forwarding path.",
}

var fieldDescriptions = map[protoreflect.FullName]string{
	"sdn.agent.v1.QueryRequest.kind":         "Select a kind advertised by sdn_capabilities. Network queries use the configured Linux/VPP backend; BGP/EVPN/OSPF queries use FRR.",
	"sdn.agent.v1.QueryRequest.vrf":          "Tenant/VRF name. With VPP, selects the managed tenant FIB or resources bound to that tenant.",
	"sdn.agent.v1.QueryRequest.bridge":       "Managed bridge name, such as br50000. This is not a native numeric VPP BD ID.",
	"sdn.agent.v1.QueryRequest.interface":    "Interface name as returned by a preceding query. VPP interface queries include LCP interfaces, BVIs and VXLAN interfaces.",
	"sdn.agent.v1.QueryRequest.family":       "Use a family advertised for this kind. For VXLAN_TUNNELS, IPV4/IPV6 filters outer VTEP addresses, not inner payload addresses.",
	"sdn.agent.v1.QueryRequest.neighbor":     "IP address filter. For VPP VXLAN_TUNNELS, selects tunnels containing this remote VTEP; remotes remains the complete member list of each matching tunnel.",
	"sdn.agent.v1.QueryRequest.prefix":       "IP prefix filter for supported route/address queries; not a VNI selector.",
	"sdn.agent.v1.QueryRequest.route_type":   "EVPN route type (for example 2, 3 or 5), only with EVPN_ROUTES or BGP_ROUTES family L2VPN_EVPN. Zero means all types.",
	"sdn.agent.v1.QueryRequest.page_token":   "Opaque next_page_token from the same query and unchanged filters. Restart pagination if Agent reports changed data.",
	"sdn.agent.v1.Tenant.table_id":           "Tenant routing table ID; distinct from the underlay FIB, BD ID and VNI.",
	"sdn.agent.v1.Bridge.stp":                "VPP accepts only false or omission; STP is not implemented by the VPP backend.",
	"sdn.agent.v1.Bridge.ageing_time":        "FDB ageing time in seconds. VPP supports zero or multiples of 60, up to 15300; omission preserves the current/default value.",
	"sdn.agent.v1.Bridge.mac":                "MAC for the bridge/BVI. An L3VNI transport BVI uses its Router MAC.",
	"sdn.agent.v1.BridgePort.port":           "Existing managed interface. In VPP mode use the LCP/control interface name or managed VXLAN name, not an assumed sw_if_index.",
	"sdn.agent.v1.BridgePort.learning":       "For EVPN VXLAN ports use false: remote MACs are supplied by the control plane. Optional false is distinct from omission.",
	"sdn.agent.v1.BridgePort.neigh_suppress": "Enable remote ARP/ND suppression when required. This is not active neighbor keepalive.",
	"sdn.agent.v1.Address.interface":         "Interface or bridge name; addressing a tenant-bound bridge configures its VPP BVI gateway in VPP mode.",
	"sdn.agent.v1.Vxlan.local":               "Local IPv4 or IPv6 VTEP address (src). Remote endpoints learned for this VNI must use the same outer address family.",
	"sdn.agent.v1.Vxlan.vni":                 "VXLAN network identifier; distinct from bridge ID and tenant routing table ID.",
	"sdn.agent.v1.Vxlan.underlay":            "Existing Linux underlay interface name; in VPP mode it must map to an LCP pair. This does not configure or bind a PCI device.",
	"sdn.agent.v1.Vxlan.learning":            "Use false for EVPN control-plane MAC learning.",
	"sdn.agent.v1.Peer.vrf":                  "FRR VRF; VPP mode does not support tenant BGP sessions through BVI. Underlay/default-instance EVPN peering remains supported.",
}

func scalarSchema(f protoreflect.FieldDescriptor) schema {
	switch f.Kind() {
	case protoreflect.MessageKind:
		return messageSchema(f.Message())
	case protoreflect.EnumKind:
		var values []string
		for i := 0; i < f.Enum().Values().Len(); i++ {
			v := f.Enum().Values().Get(i)
			if v.Number() == 0 && f.Enum().FullName() != "sdn.agent.v1.AddressFamily" {
				continue
			}
			values = append(values, string(v.Name()))
		}
		return schema{"type": "string", "enum": values}
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return schema{"type": "string", "pattern": "^(0|[1-9][0-9]{0,19})$", "description": "Unsigned 64-bit decimal string; use a string even for zero."}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return schema{"type": "string", "pattern": "^-?(0|[1-9][0-9]{0,18})$"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return schema{"type": "integer", "minimum": 0, "maximum": uint64(1<<32 - 1)}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return schema{"type": "integer", "minimum": -(1 << 31), "maximum": 1<<31 - 1}
	case protoreflect.BoolKind:
		return schema{"type": "boolean"}
	default:
		return schema{"type": "string"}
	}
}

func inputSchema(message proto.Message, required ...string) schema {
	s := messageSchema(message.ProtoReflect().Descriptor())
	p := s["properties"].(map[string]any)
	delete(p, "node_id") // The launch configuration binds one Agent identity.
	if len(required) > 0 {
		s["required"] = required
	}
	for _, name := range []string{"transaction_id", "task_id", "request_id"} {
		if f, ok := p[name].(schema); ok {
			f["minLength"] = 1
			f["maxLength"] = 256
			f["pattern"] = "\\S"
		}
	}
	if f, ok := p["operations"].(schema); ok {
		f["minItems"] = 1
	}
	return s
}

func compileSchema(s schema) (*jsonschema.Resolved, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var decoded jsonschema.Schema
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil, err
	}
	r, err := decoded.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP input schema: %w", err)
	}
	return r, nil
}

// Keep the code-to-oneof contract explicit: many operation names do not match
// their payload name (BD is Bridge, for example). Backend semantics stay in Agent.
var operationPayloads = []struct {
	code  pb.OpCode
	field string
}{
	{pb.OpCode_NEIGHBOR_KEEPALIVE, "neighbor_keepalive"},
	{pb.OpCode_NODE_NETWORK, "network"}, {pb.OpCode_INTERFACE, "interface"},
	{pb.OpCode_ADDRESS, "address"}, {pb.OpCode_TENANT, "tenant"}, {pb.OpCode_BD, "bridge"},
	{pb.OpCode_BD_PORT, "bridge_port"}, {pb.OpCode_BD_TENANT, "binding"},
	{pb.OpCode_VXLAN, "vxlan"}, {pb.OpCode_STATIC_ROUTE, "route"},
	{pb.OpCode_OSPF_INSTANCE, "ospf"}, {pb.OpCode_OSPF_NETWORK, "ospf_network"},
	{pb.OpCode_OSPF_INTERFACE, "ospf_interface"}, {pb.OpCode_OSPF6_INSTANCE, "ospf"},
	{pb.OpCode_OSPF6_INTERFACE, "ospf6_interface"}, {pb.OpCode_BGP_INSTANCE, "bgp"},
	{pb.OpCode_BGP_PEER_GROUP, "peer"}, {pb.OpCode_BGP_NEIGHBOR, "peer"},
	{pb.OpCode_BGP_EVPN_PEER, "peer"}, {pb.OpCode_BGP_EVPN_GLOBAL, "evpn_global"},
	{pb.OpCode_EVPN_L2VNI, "evpn"}, {pb.OpCode_EVPN_TENANT, "evpn"},
	{pb.OpCode_TENANT_L3VNI, "l3vni"}, {pb.OpCode_BGP_AF_PEER, "peer"},
	{pb.OpCode_BGP_NETWORK, "bgp_network"}, {pb.OpCode_BGP_REDISTRIBUTE, "bgp_redistribute"},
	{pb.OpCode_EVPN_TYPE5, "bgp_type5"},
}
