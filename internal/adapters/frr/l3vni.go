package frr

import (
	"context"
	"encoding/json"
	"fmt"
	pb "sdn-agent/api/agent/v1"
)

// Binding classifies an existing VNI as L3. It never creates Linux resources.
func (d *Driver) requireL3Resources(ctx context.Context, x *pb.L3Vni) error {
	text, e := d.Runner.Run(ctx, "ip", "-j", "-d", "link", "show")
	if e != nil {
		return e
	}
	var links []map[string]any
	if e = json.Unmarshal([]byte(text), &links); e != nil {
		return fmt.Errorf("L3VNI resource readback: %w", e)
	}
	by := map[string]map[string]any{}
	for _, l := range links {
		by[stringValue(l, "ifname")] = l
	}
	tenant := by[x.Tenant]
	if stringValue(objectValue(tenant, "linkinfo"), "info_kind") != "vrf" {
		return fmt.Errorf("L3VNI requires existing Linux VRF %s", x.Tenant)
	}
	for _, l := range links {
		info := objectValue(l, "linkinfo")
		data := objectValue(info, "info_data")
		if stringValue(info, "info_kind") == "vxlan" && data["id"] == float64(x.Vni) {
			bridge := by[stringValue(l, "master")]
			if stringValue(objectValue(bridge, "linkinfo"), "info_kind") == "bridge" && stringValue(bridge, "master") == x.Tenant {
				return nil
			}
		}
	}
	return fmt.Errorf("L3VNI %d requires an existing VXLAN attached to a bridge in VRF %s", x.Vni, x.Tenant)
}
