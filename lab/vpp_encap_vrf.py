"""Outer VRF switch acceptance for vpp_irb.py; no host NICs are touched.

Run with SDN_VPP_ENCAP_VRF_ONLY=1 SDN_VPP_STACK=1. Management/BGP stays on
eth0; VPP data underlay is separate. This explicitly prepares test underlays
before submitting VXLAN changes, just as deployment startup configuration must.
"""
import json
import time


def verify(lab):
    de, cli, query = (lab[k] for k in ("de", "cli", "query"))
    wait, submit, op = (lab[k] for k in ("wait", "submit", "op"))
    leaves, pcs, results = (lab[k] for k in ("leaves", "pcs", "results"))
    stack = lab["stack"]
    assert stack, "SDN_VPP_STACK=1 is required for end-to-end outer VRF acceptance"
    names = [x["name"] for x in leaves]
    def vxlans(name):
        return query(name, "VXLAN_TUNNELS")["records"]
    before = {name: {r["name"]: r for r in vxlans(name)} for name in names}
    for i, name in enumerate(names):
        ops = [op("NEIGHBOR_KEEPALIVE", "SET", "neighbor_keepalive", enabled=True)]
        for table in (100, 200):
            ops += [op("TENANT", "CREATE", "tenant", name=f"OUTER{table}", table_id=table),
                    op("INTERFACE", "SET", "interface", name=f"OUTER{table}", up=True)]
        submit(name, ops, f"outer-tables-{i}")
    stack.verify(leaves)
    nodes = stack.request("/nodes?pageSize=200")["items"]
    for node in nodes:
        assert "vxlan-encap-vrf/v1" in node["capabilities"]["features"]
        # Fetch fresh revision after stack verification.
        node.update(stack.request(f'/nodes/{node["nodeId"]}'))
        stack.request(f'/nodes/{node["nodeId"]}/resources/import', "POST",
                      {"expectedAgentRevision": node["agentRevision"]})

    def switch(table):
        desired = stack.request("/desired")
        change = stack.request("/changes", "POST", {"baseRevision": desired["revision"]})
        for node in nodes:
            name = node["endpoint"].rsplit(":", 1)[0]
            change = stack.request(f'/changes/{change["id"]}/nodes/{node["nodeId"]}/resources',
                "PUT", {"editRevision": change["editRevision"], "operations": [
                    {"code": "VXLAN", "action": "SET",
                     "vxlan": {"name": vx, "encapVrfId": table}} for vx in before[name]]})
        plan = stack.request(f'/changes/{change["id"]}/plan', "POST")
        job = stack.request(f'/changes/{change["id"]}/commit', "POST", {"planId": plan["id"]})
        def complete():
            status = stack.request(f'/jobs/{job["id"]}')
            if status["state"] in ("FAILED", "ROLLED_BACK", "ROLLBACK_FAILED"):
                raise AssertionError(status)
            return status["state"] == "SUCCEEDED"
        wait(f"Controller outer VRF {table} commit", complete)
        for node in nodes:
            rows = stack.request(f'/nodes/{node["nodeId"]}/query?kind=VXLAN_TUNNELS', viewer=True)["records"]
            name = node["endpoint"].rsplit(":", 1)[0]
            assert set(r["name"] for r in rows) == set(before[name])
            for row in rows:
                old = before[name][row["name"]]
                assert row["encapVrfId"] == table, row
                for field in ("vppName", "vni", "bridge", "vrf"):
                    assert row.get(field) == old.get(field), (field, old, row)
            state = json.loads(de(name, "cat", "/var/lib/sdn-agent/vpp.json").stdout)
            assert state["version"] == 4
            assert all(x["encapVrfId"] == table for x in state["desired"]["vxlans"].values())

    def prepare_underlay(table):
        for leaf in leaves:
            name = leaf["name"]
            # This test's data interface only; never the management/BGP NIC.
            # LCP mirrors link/addr, but its Linux master handling is not assumed:
            # explicitly configure the matching native interface routing table.
            family = "ip6" if lab["UNDERLAY6"] else "ip"
            address = f'{leaf["ip"]}/{leaf["prefix"]}'
            de(name, "ip", "addr", "del", address, "dev", "uplink")
            time.sleep(0.5)
            de(name, "ip", "link", "set", "uplink", *(
                ("master", f"OUTER{table}") if table else ("nomaster",)))
            cli(name, f"set interface {family} table host-eth1 {table}")
            de(name, "ip", "addr", "add", address, "dev", "uplink")
            # BGP EVPN runs in default VRF. Its next-hop tracking must resolve
            # the VTEP through the real data underlay, without a dummy/blackhole
            # route or bypassing FRR best-path selection.
            remote = next(x["ip"] for x in leaves if x["name"] != name)
            prefix = f"{remote}/{128 if lab['UNDERLAY6'] else 32}"
            command = "ipv6" if lab["UNDERLAY6"] else "ip"
            for old in (100, 200):
                de(name, "vtysh", "-c", "configure terminal", "-c",
                   f"no {command} route {prefix} uplink nexthop-vrf OUTER{old}")
            if table:
                de(name, "vtysh", "-c", "configure terminal", "-c",
                   f"{command} route {prefix} uplink nexthop-vrf OUTER{table}")
            # Reconcile is asynchronous, and Linux NL route notifications can lag.
            wait(f"{name} underlay table {table} prepared",
                 lambda name=name: leaf["ip"] in cli(name, f"show {family} fib table {table}"))

    def traffic(label):
        for src, dst in ((0, 1), (1, 0)):
            for family, address in ((4, f"192.168.{(dst+1)*10}.{11+dst}"),
                                    (6, f"2001:db8:{(dst+1)*10}::{11+dst}")):
                wait(f"{label} PC{src+1}->PC{dst+1} IPv{family}",
                     lambda src=src, family=family, address=address:
                     de(pcs[src], "ping", "-6" if family == 6 else "-4",
                        "-c", "2", "-W", "1", address, check=False).returncode == 0)
    for table in (100, 200, 0):
        prepare_underlay(table)
        switch(table)
        traffic(f"outer VRF {table}")
        if table == 100:
            name = names[0]
            pid = de(name, "pgrep", "-f", "^python3 .*vpp/adapter[.]py .* --api ").stdout.strip()
            assert pid.isdecimal()
            lab["run"]("docker", "cp", lab["ROOT"] / "lab/vpp_encap_vrf_reload.py", name + ":/tmp/reload.py")
            de(name, "kill", "-STOP", pid)
            try:
                de(name, "python3", "/tmp/reload.py")
            finally:
                de(name, "kill", "-CONT", pid)
            traffic("after adapter configuration reload")
            results["fresh adapter restores nonzero outer table from persistent state"] = True
    results["outer VRF changes preserve VXLAN identity and tenant binding"] = True
    results["outer VRF 0 to 100 to 200 to 0 supports dual-stack EVPN forwarding"] = True

    # Actual Agent transactional rollback, after native/mirror SET has executed.
    # The second operation targets a missing LCP port and must fail.
    name = names[0]
    vx = next(iter(before[name]))
    cp = submit(name, [
        op("VXLAN", "SET", "vxlan", name=vx, encap_vrf_id=100),
        op("INTERFACE", "SET", "interface", name="missing0", up=True),
    ], "outer-rollback", check=False)
    assert json.loads(cp.stdout)["state"] == "FAILED_ROLLED_BACK", cp.stdout + cp.stderr
    assert next(r for r in vxlans(name) if r["name"] == vx)["encapVrfId"] == 0
    traffic("after failed transaction rollback")
    results["failed transaction restores old outer VRF and forwarding"] = True

    # A failed delete must restore the Linux mirror before native reconciliation.
    spare = dict(name="vx-spare", vni=16777002, local=leaves[0]["ip"],
                 port=4789, encap_vrf_id=100)
    submit(name, [op("VXLAN", "CREATE", "vxlan", **spare)], "outer-spare-create")
    cp = submit(name, [op("VXLAN", "DELETE", "vxlan", **spare),
                      op("INTERFACE", "SET", "interface", name="missing0", up=True)],
                "outer-delete-rollback", check=False)
    assert json.loads(cp.stdout)["state"] == "FAILED_ROLLED_BACK", cp.stdout
    assert next(r for r in vxlans(name) if r["name"] == "vx-spare")["encapVrfId"] == 100
    submit(name, [op("VXLAN", "DELETE", "vxlan", **spare)], "outer-spare-clean")
    results["failed deletion restores nonzero-table Linux mirror and VPP tunnel"] = True
