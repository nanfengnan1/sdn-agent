package frr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	pb "sdn-agent/api/agent/v1"
	"sort"
	"strconv"
	"strings"
)

func (d *Driver) QueryCapabilities() []*pb.QueryCapability {
	var out []*pb.QueryCapability
	for _, kind := range []pb.QueryKind{pb.QueryKind_BGP_SUMMARY, pb.QueryKind_BGP_NEIGHBORS, pb.QueryKind_BGP_ROUTES, pb.QueryKind_EVPN_ROUTES, pb.QueryKind_EVPN_VNI} {
		out = append(out, &pb.QueryCapability{Kind: kind, Families: []pb.AddressFamily{pb.AddressFamily_IPV4, pb.AddressFamily_IPV6, pb.AddressFamily_L2VPN_EVPN}})
	}
	for _, kind := range []pb.QueryKind{pb.QueryKind_OSPF_SUMMARY, pb.QueryKind_OSPF_INTERFACES, pb.QueryKind_OSPF_NEIGHBORS, pb.QueryKind_OSPF_DATABASE, pb.QueryKind_OSPF_ROUTES} {
		out = append(out, &pb.QueryCapability{Kind: kind, Families: []pb.AddressFamily{pb.AddressFamily_IPV4, pb.AddressFamily_IPV6}, OspfVersions: []uint32{2, 3}})
	}
	return out
}
func queryCommand(q *pb.QueryRequest) (string, error) {
	if q.Vrf != "" {
		if e := safe(q.Vrf); e != nil {
			return "", e
		}
	}
	if q.Interface != "" {
		if e := safe(q.Interface); e != nil {
			return "", e
		}
	}
	if q.Neighbor != "" {
		if _, e := netip.ParseAddr(q.Neighbor); e != nil {
			return "", e
		}
	}
	if q.Prefix != "" {
		if _, e := netip.ParsePrefix(q.Prefix); e != nil {
			return "", e
		}
	}
	if q.Bridge != "" {
		return "", fmt.Errorf("bridge filter is not supported by FRR query; use EVPN_VNI and its bridge field")
	}
	root := withVRF("show bgp", q.Vrf)
	af := familyName(q.Family) + " unicast"
	if q.Family == pb.AddressFamily_L2VPN_EVPN {
		af = "l2vpn evpn"
	}
	switch q.Kind {
	case pb.QueryKind_BGP_SUMMARY:
		return root + " " + af + " summary json", nil
	case pb.QueryKind_BGP_NEIGHBORS:
		if q.Neighbor != "" {
			return root + " neighbors " + q.Neighbor + " json", nil
		}
		return root + " neighbors json", nil
	case pb.QueryKind_BGP_ROUTES:
		return root + " " + af + " json", nil
	case pb.QueryKind_EVPN_ROUTES:
		if q.Vrf != "" {
			return "", fmt.Errorf("EVPN routes belong to the global EVPN RIB; use EVPN_VNI to select tenant membership")
		}
		return "show bgp l2vpn evpn json", nil
	case pb.QueryKind_EVPN_VNI:
		return "show evpn vni json", nil
	case pb.QueryKind_OSPF_SUMMARY, pb.QueryKind_OSPF_INTERFACES, pb.QueryKind_OSPF_NEIGHBORS, pb.QueryKind_OSPF_DATABASE, pb.QueryKind_OSPF_ROUTES:
		version := q.OspfVersion
		if version == 0 {
			if q.Family == pb.AddressFamily_IPV6 {
				version = 3
			} else {
				version = 2
			}
		}
		if version != 2 && version != 3 {
			return "", fmt.Errorf("ospf_version must be 2 or 3")
		}
		if version == 2 && q.Family == pb.AddressFamily_IPV6 || version == 3 && q.Family == pb.AddressFamily_IPV4 {
			return "", fmt.Errorf("OSPF version/address family mismatch")
		}
		root = "show ip ospf"
		if version == 3 {
			root = "show ipv6 ospf6"
		}
		root = withVRF(root, q.Vrf)
		suffix := map[pb.QueryKind]string{pb.QueryKind_OSPF_INTERFACES: " interface", pb.QueryKind_OSPF_NEIGHBORS: " neighbor", pb.QueryKind_OSPF_DATABASE: " database", pb.QueryKind_OSPF_ROUTES: " route"}[q.Kind]
		return root + suffix + " json", nil
	}
	return "", fmt.Errorf("unsupported FRR query %s", q.Kind)
}
func (d *Driver) queryJSON(ctx context.Context, cmd string) (map[string]any, error) {
	text, e := d.cli(ctx, cmd)
	if e != nil {
		return nil, e
	}
	var value map[string]any
	if e = json.Unmarshal([]byte(text), &value); e != nil {
		return nil, fmt.Errorf("%s returned invalid or unavailable JSON: %w", cmd, e)
	}
	if value == nil {
		return nil, fmt.Errorf("%s returned null JSON", cmd)
	}
	for _, key := range []string{"warning", "error"} {
		if v, ok := value[key]; ok {
			return nil, fmt.Errorf("%s: %v", cmd, v)
		}
	}
	return value, nil
}
func cloneRow(m map[string]any) map[string]any {
	r := make(map[string]any, len(m)+2)
	for k, v := range m {
		r[k] = v
	}
	return r
}
func objectValue(m map[string]any, k string) map[string]any { x, _ := m[k].(map[string]any); return x }
func stringValue(m map[string]any, k string) string         { x, _ := m[k].(string); return x }
func mapRows(m map[string]any, keyName string) []map[string]any {
	var out []map[string]any
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		switch v := m[key].(type) {
		case map[string]any:
			r := cloneRow(v)
			r[keyName] = key
			out = append(out, r)
		case []any:
			for _, item := range v {
				if r, ok := item.(map[string]any); ok {
					row := cloneRow(r)
					row[keyName] = key
					out = append(out, row)
				}
			}
		}
	}
	return out
}
func (d *Driver) Query(ctx context.Context, q *pb.QueryRequest) ([]map[string]any, error) {
	cmd, e := queryCommand(q)
	if e != nil {
		return nil, e
	}
	value, e := d.queryJSON(ctx, cmd)
	if e != nil {
		return nil, e
	}
	var rows []map[string]any
	switch q.Kind {
	case pb.QueryKind_BGP_SUMMARY, pb.QueryKind_OSPF_SUMMARY:
		if len(value) > 0 {
			rows = append(rows, value)
		}
	case pb.QueryKind_BGP_NEIGHBORS:
		rows = mapRows(value, "neighbor")
	case pb.QueryKind_BGP_ROUTES:
		if q.Family == pb.AddressFamily_L2VPN_EVPN {
			rows = evpnRows(value)
		} else {
			rows = mapRows(objectValue(value, "routes"), "prefix")
		}
	case pb.QueryKind_EVPN_ROUTES:
		rows = evpnRows(value)
	case pb.QueryKind_EVPN_VNI:
		// Full VNI output is keyed by VNI in FRR 10.x; each record includes type,
		// tenantVrf and bridge. VRF VNI list adds explicit L3 membership, never guessed.
		rows = mapRows(value, "vniKey")
		vrfs, err := d.queryJSON(ctx, "show vrf vni json")
		if err != nil {
			return nil, err
		}
		if list, ok := vrfs["vrfs"].([]any); ok {
			for _, item := range list {
				if r, ok := item.(map[string]any); ok {
					row := cloneRow(r)
					row["type"] = "L3"
					found := false
					for _, existing := range rows {
						if existing["vni"] == row["vni"] {
							for k, v := range row {
								existing[k] = v
							}
							found = true
							break
						}
					}
					if !found {
						rows = append(rows, row)
					}
				}
			}
		}
	case pb.QueryKind_OSPF_INTERFACES:
		if interfaces := objectValue(value, "interfaces"); interfaces != nil {
			rows = mapRows(interfaces, "interface")
		} else {
			rows = mapRows(value, "interface")
		}
	case pb.QueryKind_OSPF_NEIGHBORS:
		switch n := value["neighbors"].(type) {
		case []any:
			for _, item := range n {
				if r, ok := item.(map[string]any); ok {
					rows = append(rows, r)
				}
			}
		case map[string]any:
			rows = mapRows(n, "neighbor")
		default:
			rows = mapRows(value, "neighbor")
		}
	case pb.QueryKind_OSPF_ROUTES:
		if r := objectValue(value, "routes"); r != nil {
			rows = mapRows(r, "prefix")
		} else {
			rows = mapRows(value, "prefix")
		}
	case pb.QueryKind_OSPF_DATABASE:
		if len(value) > 0 {
			rows = append(rows, value)
		}
	}
	var filtered []map[string]any
	for _, row := range rows {
		if q.Kind == pb.QueryKind_OSPF_NEIGHBORS {
			if n := stringValue(row, "ifaceName"); n != "" {
				row["interface"] = strings.SplitN(n, ":", 2)[0]
			}
			if n := stringValue(row, "interfaceName"); n != "" {
				row["interface"] = n
			}
		}
		if q.Kind == pb.QueryKind_BGP_NEIGHBORS && q.Family != pb.AddressFamily_AF_UNSPECIFIED {
			key := map[pb.AddressFamily]string{pb.AddressFamily_IPV4: "ipv4Unicast", pb.AddressFamily_IPV6: "ipv6Unicast", pb.AddressFamily_L2VPN_EVPN: "l2VpnEvpn"}[q.Family]
			afs := objectValue(row, "addressFamilyInfo")
			if _, ok := afs[key]; !ok {
				continue
			}
		}
		if q.Kind == pb.QueryKind_EVPN_VNI && q.Vrf != "" {
			match := false
			for _, key := range []string{"tenantVrf", "vrf", "vrfName", "name"} {
				if row[key] == q.Vrf {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if q.Interface != "" {
			if !hasInterface(row, q.Interface) {
				continue
			}
		}
		if q.Neighbor != "" && q.Kind != pb.QueryKind_BGP_SUMMARY {
			match := false
			for _, key := range []string{"neighbor", "neighborId", "neighborAddress", "routerId", "peerId"} {
				if row[key] == q.Neighbor {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if q.RouteType > 0 && row["routeType"] != float64(q.RouteType) {
			continue
		}
		if q.Prefix != "" {
			p, _ := netip.ParsePrefix(q.Prefix)
			if row["prefix"] != p.Masked().String() && row["network"] != p.Masked().String() && row["ipPrefix"] != p.Masked().String() {
				continue
			}
		}
		if (q.Kind == pb.QueryKind_EVPN_ROUTES || q.Kind == pb.QueryKind_BGP_ROUTES && q.Family == pb.AddressFamily_L2VPN_EVPN) && (q.Family == pb.AddressFamily_IPV4 || q.Family == pb.AddressFamily_IPV6) {
			match := false
			for _, address := range evpnIPs(row) {
				if parsed, e := netip.ParseAddr(address); e == nil && (q.Family == pb.AddressFamily_IPV4 && parsed.Is4() || q.Family == pb.AddressFamily_IPV6 && parsed.Is6()) {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if q.Vrf != "" && q.Kind != pb.QueryKind_EVPN_VNI {
			row["vrf"] = q.Vrf
		}
		family := q.Family
		if family == pb.AddressFamily_AF_UNSPECIFIED {
			if q.Kind >= pb.QueryKind_OSPF_SUMMARY && q.Kind <= pb.QueryKind_OSPF_ROUTES {
				family = pb.AddressFamily_IPV4
				if q.OspfVersion == 3 {
					family = pb.AddressFamily_IPV6
				}
			} else if q.Kind == pb.QueryKind_BGP_ROUTES || q.Kind == pb.QueryKind_BGP_SUMMARY {
				family = pb.AddressFamily_IPV4
			}
		}
		row["family"] = family.String()
		filtered = append(filtered, row)
	}
	return filtered, nil
}

func hasInterface(row map[string]any, want string) bool {
	for _, key := range []string{"interface", "ifName", "ifname", "interfaceName", "via", "directlyAttachedTo"} {
		if row[key] == want {
			return true
		}
	}
	for _, key := range []string{"nexthops", "paths"} {
		if values, ok := row[key].([]any); ok {
			for _, value := range values {
				if child, ok := value.(map[string]any); ok && hasInterface(child, want) {
					return true
				}
			}
		}
	}
	return false
}
func evpnIPs(row map[string]any) []string {
	var out []string
	if p, e := netip.ParsePrefix(stringValue(row, "prefix")); e == nil {
		out = append(out, p.Addr().String())
	}
	if s := stringValue(row, "ip"); s != "" {
		out = append(out, s)
	}
	if paths, ok := row["paths"].([]any); ok {
		for _, item := range paths {
			if child, ok := item.(map[string]any); ok {
				out = append(out, evpnIPs(child)...)
			}
		}
	}
	return out
}

// EVPN JSON nests RDs, NLRIs and path arrays. Preserve each NLRI with paths and
// typed identity fields so callers can select Type-2/3/5 without parsing CLI.
func evpnRows(value map[string]any) []map[string]any {
	var out []map[string]any
	var walk func(any, string)
	walk = func(v any, rd string) {
		switch x := v.(type) {
		case map[string]any:
			for key, item := range x {
				if strings.HasPrefix(key, "[") {
					typ := 0
					if end := strings.Index(key, "]"); end > 1 {
						typ, _ = strconv.Atoi(key[1:end])
					}
					r := map[string]any{"rd": rd, "nlri": key, "routeType": float64(typ)}
					switch paths := item.(type) {
					case map[string]any:
						for k, v := range paths {
							r[k] = v
						}
					case []any:
						r["paths"] = paths
					default:
						r["details"] = paths
					}
					if typ == 5 {
						parts := strings.Split(key, ":")
						_ = parts // extract bracket-delimited prefix fields, including IPv6 colons.
						fields := []string{}
						rest := key
						for {
							start := strings.Index(rest, "[")
							end := strings.Index(rest, "]")
							if start < 0 || end <= start {
								break
							}
							fields = append(fields, rest[start+1:end])
							rest = rest[end+1:]
						}
						if len(fields) >= 4 {
							r["prefix"] = fields[3] + "/" + fields[2]
						}
					}
					out = append(out, r)
				} else {
					next := rd
					if strings.Count(key, ":") == 1 && !strings.Contains(key, " ") {
						next = key
					}
					walk(item, next)
				}
			}
		case []any:
			for _, item := range x {
				walk(item, rd)
			}
		}
	}
	walk(value, "")
	return out
}
