package frr

import (
	"fmt"
	"net/netip"
	pb "sdn-agent/api/agent/v1"
	"strconv"
)

func withVRF(root, vrf string) string {
	if vrf != "" {
		return root + " vrf " + vrf
	}
	return root
}
func familyName(f pb.AddressFamily) string {
	if f == pb.AddressFamily_IPV6 {
		return "ipv6"
	}
	return "ipv4"
}
func familyPrefix(f pb.AddressFamily, s string) error {
	p, e := netip.ParsePrefix(s)
	if e != nil {
		return e
	}
	if f == pb.AddressFamily_IPV4 && p.Addr().Is4() || f == pb.AddressFamily_IPV6 && p.Addr().Is6() {
		return nil
	}
	return fmt.Errorf("prefix/address family mismatch")
}
func area(s string) error {
	if _, e := strconv.ParseUint(s, 10, 32); e == nil {
		return nil
	}
	return ipv4(s)
}
func canonicalArea(s string) string {
	n, e := strconv.ParseUint(s, 10, 32)
	if e == nil {
		return fmt.Sprintf("%d.%d.%d.%d", n>>24, (n>>16)&255, (n>>8)&255, n&255)
	}
	return s
}
func validateOspfInterface(n, v, a, network string, cost, hello, dead *uint32) error {
	if e := safe(n); e != nil {
		return e
	}
	if v != "" {
		if e := safe(v); e != nil {
			return e
		}
	}
	if a != "" {
		if e := area(a); e != nil {
			return e
		}
	}
	if network != "" && network != "point-to-point" && network != "broadcast" {
		return fmt.Errorf("unsupported OSPF network type")
	}
	for _, n := range []*uint32{cost, hello, dead} {
		if n != nil && (*n == 0 || *n > 65535) {
			return fmt.Errorf("OSPF cost/timer must be 1..65535")
		}
	}
	if hello != nil && dead != nil && *dead <= *hello {
		return fmt.Errorf("dead interval must exceed hello interval")
	}
	return nil
}
func ospfTimers(cost, hello, dead *uint32) []string {
	var out []string
	for _, x := range []struct {
		name  string
		value *uint32
	}{{"cost", cost}, {"hello-interval", hello}, {"dead-interval", dead}} {
		if x.value != nil {
			out = append(out, fmt.Sprintf("%s %d", x.name, *x.value))
		}
	}
	return out
}
