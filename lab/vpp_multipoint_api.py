#!/usr/bin/env python3
"""Run inside a disposable Agent/VPP test container, never on a live fabric."""

import subprocess
import sys

sys.path.insert(0, "/usr/local/lib/sdn-agent/vpp")
from adapter import VPP, ALL


def main():
    api = VPP("/run/vpp/api.sock", "/usr/share/vpp/api")
    base = dict(
        src_address="192.0.2.250",
        src_port=4799,
        dst_port=4799,
        encap_vrf_id=0,
        instance=ALL,
        mcast_sw_if_index=ALL,
        decap_next_index=ALL,
        is_l3=False,
    )
    created = []
    try:
        # CLI without dst, native creation before any remote is configured.
        cp = subprocess.run(
            [
                "vppctl",
                "create",
                "vxlan",
                "tunnel",
                "src",
                base["src_address"],
                "vni",
                "16777000",
                "src_port",
                "4799",
                "dst_port",
                "4799",
            ],
            text=True,
            capture_output=True,
            check=True,
        )
        assert "vxlan_tunnel" in cp.stdout, cp.stdout + cp.stderr
        created.append(dict(base, vni=16777000, dst_address="0.0.0.0"))
        _, _, ts, _ = api.snapshot()
        sw = next(i for i, t in ts.items() if t["vni"] == 16777000)
        count = len(ts)
        assert str(ts[sw]["dst_address"]) == "0.0.0.0"
        mac = "02:00:00:aa:00:01"

        def update(remote, bum=False, add=True, address=mac, index=sw):
            api.call(
                "vxlan_mp_remote_add_del",
                sw_if_index=index,
                mac=address,
                remote=remote,
                bum=bum,
                is_add=add,
            )

        def entries():
            return {
                (str(r.mac), str(r.remote), bool(r.bum))
                for r in api.call("vxlan_mp_remote_dump", sw_if_index=sw)
            }

        update("192.0.2.2")
        update("192.0.2.3")
        update("192.0.2.2", add=False)  # delayed old withdrawal
        assert entries() == {(mac, "192.0.2.3", False)}
        for remote in ("192.0.2.2", "192.0.2.3", "192.0.2.3"):
            update(remote, bum=True, address="00:00:00:00:00:00")
        assert len(entries()) == 3
        assert len(api.snapshot()[2]) == count  # no hidden tunnel interfaces
        update("192.0.2.3", bum=True, add=False, address="00:00:00:00:00:00")
        assert (mac, "192.0.2.3", False) in entries()
        for params in (
            dict(remote="224.0.0.1"),
            dict(remote="0.0.0.0"),
            dict(remote="192.0.2.250"),
            dict(remote="192.0.2.2", index=ALL),
            dict(remote="192.0.2.2", address="01:00:00:00:00:01"),
        ):
            try:
                update(**params)
            except RuntimeError:
                pass
            else:
                raise AssertionError(f"invalid remote accepted: {params}")
        # Existing fixed-destination native interfaces still coexist.
        fixed = dict(base, vni=16777001, dst_address="192.0.2.2")
        idx = api.call("vxlan_add_del_tunnel_v3", is_add=True, **fixed).sw_if_index
        created.append(fixed)
        assert str(api.snapshot()[2][idx]["dst_address"]) == "192.0.2.2"
        try:
            update("192.0.2.3", index=idx)
        except RuntimeError:
            pass
        else:
            raise AssertionError("multipoint update accepted on fixed tunnel")
        print(
            "PASS native CLI, single interface, MAC move, stale withdrawal, "
            "BUM independence, input validation and fixed-dst coexistence"
        )
    finally:
        for args in reversed(created):
            api.call("vxlan_add_del_tunnel_v3", is_add=False, **args)
        assert not any(
            t["vni"] in (16777000, 16777001) for t in api.snapshot()[2].values()
        )
        api.client.disconnect()


if __name__ == "__main__":
    main()
