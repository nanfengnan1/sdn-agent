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
	if f.IsList() {
		return schema{"type": "array", "items": s, "maxItems": 1000}
	}
	return s
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
