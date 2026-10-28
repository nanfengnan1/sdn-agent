#!/usr/bin/env python3
"""Native outer VRF API/CLI regression; run only in a disposable VPP container."""
import json
import subprocess
import sys
sys.path.insert(0, "/usr/local/lib/sdn-agent/vpp")
from adapter import VPP, ALL, plain


def main():
    api = VPP("/run/vpp/api.sock", "/usr/share/vpp/api")
    def cli(line):
        return subprocess.check_output(["vppctl", line], text=True)
    def fail(fn):
        try:
            fn()
        except RuntimeError:
            return
        raise AssertionError("expected native API rejection")
    def table(index):
        return api.snapshot()[2][index]["encap_vrf_id"]
    results = []
    for ipv6 in (False, True):
        local = "2001:db8::1" if ipv6 else "192.0.2.1"
        remote = "2001:db8::2" if ipv6 else "192.0.2.2"
        zero = "::" if ipv6 else "0.0.0.0"
        for tid in (100, 200):
            api.call("ip_table_add_del", is_add=True,
                     table={"table_id": tid, "is_ip6": ipv6})
        for mp in (True, False):
            args = dict(src_address=local, dst_address=zero if mp else remote,
                        src_port=4799, dst_port=4799, vni=16777001,
                        encap_vrf_id=0, instance=ALL, mcast_sw_if_index=ALL,
                        decap_next_index=ALL, is_l3=False)
            sw = api.call("vxlan_add_del_tunnel_v3", is_add=True, **args).sw_if_index
            twin = dict(args, encap_vrf_id=200)
            twin_sw = api.call("vxlan_add_del_tunnel_v3", is_add=True, **twin).sw_if_index
            api.call("bridge_domain_add_del_v2", is_add=True, bd_id=16777001,
                     flood=True, uu_flood=True, forward=True, learn=False, arp_term=False)
            api.call("sw_interface_set_l2_bridge", rx_sw_if_index=sw, bd_id=16777001,
                     shg=1, port_type=0, enable=True)
            if mp:
                for mac, bum in (("02:00:00:00:00:02", False), ("00:00:00:00:00:00", True)):
                    api.call("vxlan_mp_remote_add_del_v2", sw_if_index=sw,
                             mac=mac, remote=remote, bum=bum, is_add=True)
            def entries():
                return plain(api.call("vxlan_mp_remote_v2_dump", sw_if_index=sw)) if mp else []
            before = entries()
            name = api.snapshot()[0][sw]["interface_name"]
            fail(lambda: api.call("vxlan_set_encap_vrf", sw_if_index=sw, encap_vrf_id=9999))
            assert table(sw) == 0
            cli("set vxlan tunnel " + name + " encap-vrf-id 100")
            assert table(sw) == 100
            api.call("vxlan_set_encap_vrf", sw_if_index=sw, encap_vrf_id=100)
            fail(lambda: api.call("vxlan_set_encap_vrf", sw_if_index=sw, encap_vrf_id=200))
            assert table(sw) == 100
            assert before == entries(), (before, entries())
            bridge = api.snapshot()[1][16777001]
            assert any(x["sw_if_index"] == sw and x["shg"] == 1 for x in bridge["sw_if_details"])
            output = cli("show vxlan tunnel")
            line = next(x for x in output.splitlines() if f"sw-if-idx {sw} " in x)
            assert (" dst " not in line) == mp, line
            # Old key was released: identical original tunnel can be created.
            old_sw = api.call("vxlan_add_del_tunnel_v3", is_add=True, **args).sw_if_index
            api.call("vxlan_add_del_tunnel_v3", is_add=False, **args)
            assert old_sw not in api.snapshot()[2]
            api.call("sw_interface_set_l2_bridge", rx_sw_if_index=sw, enable=False)
            api.call("vxlan_add_del_tunnel_v3", is_add=False, **dict(args, encap_vrf_id=100))
            api.call("vxlan_add_del_tunnel_v3", is_add=False, **twin)
            api.call("bridge_domain_add_del_v2", is_add=False, bd_id=16777001)
            # New key was released on deletion.
            check = dict(args, encap_vrf_id=100)
            api.call("vxlan_add_del_tunnel_v3", is_add=True, **check)
            api.call("vxlan_add_del_tunnel_v3", is_add=False, **check)
            results.append(f"IPv{6 if ipv6 else 4} {'multipoint' if mp else 'fixed'}: CLI/API, conflict, identity, remote/BD preservation, deletion")
        for tid in (100, 200):
            api.call("ip_table_add_del", is_add=False,
                     table={"table_id": tid, "is_ip6": ipv6})
    fail(lambda: api.call("vxlan_set_encap_vrf", sw_if_index=ALL, encap_vrf_id=0))
    api.client.disconnect()
    print(json.dumps({"status": "passed", "cases": results}, indent=2))


if __name__ == "__main__":
    main()
