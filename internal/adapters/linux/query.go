package linux

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	pb "sdn-agent/api/agent/v1"
)

func (d *Driver) QueryCapabilities() []*pb.QueryCapability {
	var out []*pb.QueryCapability
	for _, kind := range []pb.QueryKind{pb.QueryKind_TENANTS, pb.QueryKind_BRIDGES, pb.QueryKind_INTERFACES, pb.QueryKind_ADDRESSES, pb.QueryKind_ROUTES, pb.QueryKind_FDB, pb.QueryKind_NEIGHBORS, pb.QueryKind_VXLAN_TUNNELS, pb.QueryKind_PORTS} {
		out = append(out, &pb.QueryCapability{Kind: kind, Families: []pb.AddressFamily{pb.AddressFamily_IPV4, pb.AddressFamily_IPV6}})
	}
	return out
}
func stringField(m map[string]any, key string) string { s, _ := m[key].(string); return s }
func object(m map[string]any, key string) map[string]any {
	x, _ := m[key].(map[string]any)
	if x == nil {
		return map[string]any{}
	}
	return x
}
func (d *Driver) jsonRows(ctx context.Context, command ...string) ([]map[string]any, error) {
	text, e := d.Runner.Run(ctx, command...)
	if e != nil {
		return nil, e
	}
	var rows []map[string]any
	if e = json.Unmarshal([]byte(text), &rows); e != nil {
		return nil, fmt.Errorf("invalid %s JSON: %w", command[0], e)
	}
	if rows == nil && text != "[]" {
		return nil, fmt.Errorf("%s returned no JSON array", command[0])
	}
	return rows, nil
}
func (d *Driver) Query(ctx context.Context, q *pb.QueryRequest) ([]map[string]any, error) {
	for _, s := range []string{q.Vrf, q.Bridge, q.Interface} {
		if s != "" {
			if e := name(s); e != nil {
				return nil, e
			}
		}
	}
	if q.Family != pb.AddressFamily_AF_UNSPECIFIED && q.Family != pb.AddressFamily_IPV4 && q.Family != pb.AddressFamily_IPV6 {
		return nil, fmt.Errorf("Linux queries require IPv4 or IPv6 family")
	}
	if q.Prefix != "" {
		if _, e := netip.ParsePrefix(q.Prefix); e != nil {
			return nil, e
		}
	}
	if q.Neighbor != "" {
		if _, e := netip.ParseAddr(q.Neighbor); e != nil {
			return nil, e
		}
	}
	links, e := d.jsonRows(ctx, "ip", "-j", "-d", "link", "show")
	if e != nil {
		return nil, e
	}
	by := map[string]map[string]any{}
	ids := map[float64]string{}
	for _, l := range links {
		by[stringField(l, "ifname")] = l
		if n, ok := l["ifindex"].(float64); ok {
			ids[n] = stringField(l, "ifname")
		}
	}
	for _, l := range links {
		if n, ok := l["master"].(float64); ok {
			l["master"] = ids[n]
		}
	}
	kind := func(l map[string]any) string { return stringField(object(l, "linkinfo"), "info_kind") }
	var vrfOf func(string) string
	vrfOf = func(n string) string {
		for i := 0; i < 4; i++ {
			l := by[n]
			if l == nil {
				return ""
			}
			if kind(l) == "vrf" {
				return n
			}
			n = stringField(l, "master")
		}
		return ""
	}
	for key, n := range map[string]string{"vrf": q.Vrf, "bridge": q.Bridge, "interface": q.Interface} {
		if n != "" {
			l := by[n]
			if l == nil {
				return nil, fmt.Errorf("%s %s does not exist", key, n)
			}
			if key != "interface" && kind(l) != map[string]string{"vrf": "vrf", "bridge": "bridge"}[key] {
				return nil, fmt.Errorf("%s is not a %s", n, key)
			}
		}
	}
	selected := func(n string) bool {
		l := by[n]
		if l == nil {
			return false
		}
		if q.Interface != "" && n != q.Interface {
			return false
		}
		if q.Vrf != "" && vrfOf(n) != q.Vrf {
			return false
		}
		if q.Bridge != "" && n != q.Bridge && stringField(l, "master") != q.Bridge {
			return false
		}
		return true
	}
	var out []map[string]any
	switch q.Kind {
	case pb.QueryKind_TENANTS, pb.QueryKind_BRIDGES, pb.QueryKind_INTERFACES, pb.QueryKind_VXLAN_TUNNELS, pb.QueryKind_PORTS:
		for _, l := range links {
			n := stringField(l, "ifname")
			k := kind(l)
			if !selected(n) {
				continue
			}
			if q.Kind == pb.QueryKind_TENANTS && k != "vrf" || q.Kind == pb.QueryKind_BRIDGES && k != "bridge" || q.Kind == pb.QueryKind_VXLAN_TUNNELS && k != "vxlan" {
				continue
			}
			master := stringField(l, "master")
			if q.Kind == pb.QueryKind_PORTS && kind(by[master]) != "bridge" {
				continue
			}
			row := map[string]any{"name": n, "kind": k, "vrf": vrfOf(n), "master": master, "details": l}
			for _, key := range []string{"ifindex", "mtu", "address", "operstate", "flags"} {
				if v, ok := l[key]; ok {
					row[key] = v
				}
			}
			data := object(object(l, "linkinfo"), "info_data")
			if k == "vrf" {
				row["tableId"] = data["table"]
				members := []any{}
				for _, member := range links {
					if stringField(member, "master") == n && kind(member) == "bridge" {
						members = append(members, stringField(member, "ifname"))
					}
				}
				row["bridges"] = members
			}
			if k == "bridge" {
				members := []any{}
				vnis := []any{}
				for _, member := range links {
					if stringField(member, "master") == n {
						members = append(members, stringField(member, "ifname"))
						if kind(member) == "vxlan" {
							vnis = append(vnis, object(object(member, "linkinfo"), "info_data")["id"])
						}
					}
				}
				row["ports"] = members
				row["vnis"] = vnis
			}
			if k == "vxlan" {
				row["bridge"] = master
				for src, dst := range map[string]string{"id": "vni", "local": "local", "remote": "remote", "port": "dstPort", "learning": "learning"} {
					if v, ok := data[src]; ok {
						row[dst] = v
					}
				}
			}
			if kind(by[master]) == "bridge" {
				row["bridge"] = master
				row["neighSuppress"] = object(object(l, "linkinfo"), "info_slave_data")["neigh_suppress"]
			}
			out = append(out, row)
		}
	case pb.QueryKind_ADDRESSES:
		rows, e := d.jsonRows(ctx, "ip", "-j", "address", "show")
		if e != nil {
			return nil, e
		}
		for _, l := range rows {
			n := stringField(l, "ifname")
			if !selected(n) {
				continue
			}
			addresses, _ := l["addr_info"].([]any)
			for _, a := range addresses {
				info, ok := a.(map[string]any)
				if !ok {
					continue
				}
				family := stringField(info, "family")
				if q.Family == pb.AddressFamily_IPV4 && family != "inet" || q.Family == pb.AddressFamily_IPV6 && family != "inet6" {
					continue
				}
				row := map[string]any{"interface": n, "vrf": vrfOf(n), "family": family, "address": info["local"], "prefixLen": info["prefixlen"], "details": info}
				out = append(out, row)
			}
		}
	case pb.QueryKind_FDB:
		rows, e := d.jsonRows(ctx, "bridge", "-j", "fdb", "show")
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			n := stringField(row, "dev")
			if !selected(n) {
				continue
			}
			row["interface"] = n
			row["bridge"] = stringField(by[n], "master")
			row["vrf"] = vrfOf(n)
			out = append(out, row)
		}
	case pb.QueryKind_NEIGHBORS, pb.QueryKind_ROUTES:
		families := []string{"-4", "-6"}
		if q.Family == pb.AddressFamily_IPV4 {
			families = families[:1]
		} else if q.Family == pb.AddressFamily_IPV6 {
			families = families[1:]
		}
		for _, family := range families {
			cmd := []string{"ip", "-j", family}
			if q.Kind == pb.QueryKind_ROUTES {
				cmd = append(cmd, "route", "show")
				if q.Vrf != "" {
					cmd = append(cmd, "vrf", q.Vrf)
				} else {
					cmd = append(cmd, "table", "all")
				}
			} else {
				cmd = append(cmd, "neigh", "show")
			}
			rows, e := d.jsonRows(ctx, cmd...)
			if e != nil {
				return nil, e
			}
			for _, row := range rows {
				n := stringField(row, "dev")
				if n != "" && !selected(n) {
					continue
				}
				if n == "" && (q.Interface != "" || q.Bridge != "") {
					continue
				}
				dst := stringField(row, "dst")
				if q.Kind == pb.QueryKind_ROUTES && q.Prefix != "" {
					p, _ := netip.ParsePrefix(q.Prefix)
					want := p.Masked().String()
					if p.Bits() == 0 {
						want = "default"
					}
					if dst != want {
						continue
					}
				}
				if q.Kind == pb.QueryKind_NEIGHBORS && q.Neighbor != "" && dst != q.Neighbor {
					continue
				}
				row["family"] = map[string]string{"-4": "IPV4", "-6": "IPV6"}[family]
				row["interface"] = n
				row["vrf"] = vrfOf(n)
				if q.Vrf != "" {
					row["vrf"] = q.Vrf
				}
				if q.Kind == pb.QueryKind_ROUTES {
					row["prefix"] = dst
					if _, ok := row["table"]; !ok && q.Vrf != "" {
						row["table"] = object(object(by[q.Vrf], "linkinfo"), "info_data")["table"]
					}
				}
				out = append(out, row)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported Linux query %s", q.Kind)
	}
	return out, nil
}
