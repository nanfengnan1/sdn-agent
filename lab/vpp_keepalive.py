"""Opt-in real dual-stack liveness acceptance used by vpp_irb.py.

SDN_VPP_KEEPALIVE=1 SDN_VPP_STACK=1: actual Controller/Web candidate commits,
native unicast probes, idle aging, independent IP failure, recovery and disable.
No physical NIC or production container is modified.
"""

import json
import time


def verify(lab):
    de, cli, query = (lab[k] for k in ("de", "cli", "query"))
    wait, submit, op = (lab[k] for k in ("wait", "submit", "op"))
    leaves, pcs, results, out = (lab[k] for k in ("leaves", "pcs", "results", "OUT"))
    stack = lab["stack"]
    l1, l2 = [leaf["name"] for leaf in leaves]

    def rows(leaf):
        return query(leaf, "NEIGHBOR_KEEPALIVE_STATUS").get("records", [])

    def policy(leaf):
        return next(r for r in rows(leaf) if r.get("recordType") == "policy")

    def target(leaf, ip):
        return next((r for r in rows(leaf) if r.get("ip") == ip), {})

    for leaf in (l1, l2):
        assert not policy(leaf).get("enabled", False)
    results["keepalive disabled by default"] = True
    for i, leaf in enumerate((l1, l2), 1):
        submit(
            leaf,
            [op("BD", "SET", "bridge", name=f"br{i*10}", ageing_time=120)],
            "keepalive-age" + str(i),
        )

    nodes = []
    if stack:
        stack.verify(leaves)
        nodes = stack.request("/nodes?pageSize=200")["items"]
        for node in nodes:
            assert "neighbor-keepalive/v1" in node["capabilities"]["features"]
            stack.request(
                f'/nodes/{node["nodeId"]}/resources/import',
                "POST",
                {"expectedAgentRevision": node["agentRevision"]},
            )

    def switch(enabled):
        if not stack:
            for i, leaf in enumerate((l1, l2)):
                submit(
                    leaf,
                    [
                        op(
                            "NEIGHBOR_KEEPALIVE",
                            "SET",
                            "neighbor_keepalive",
                            enabled=enabled,
                        )
                    ],
                    f"keepalive-{enabled}-{i}",
                )
            return
        desired = stack.request("/desired")
        change = stack.request(
            "/changes", "POST", {"baseRevision": desired["revision"]}
        )
        for node in nodes:
            change = stack.request(
                f'/changes/{change["id"]}/nodes/{node["nodeId"]}/resources',
                "PUT",
                {
                    "editRevision": change["editRevision"],
                    "operations": [
                        {
                            "code": "NEIGHBOR_KEEPALIVE",
                            "action": "SET",
                            "neighborKeepalive": {"enabled": enabled},
                        }
                    ],
                },
            )
        plan = stack.request(f'/changes/{change["id"]}/plan', "POST")
        job = stack.request(
            f'/changes/{change["id"]}/commit', "POST", {"planId": plan["id"]}
        )
        wait(
            "Controller neighbor keepalive commit",
            lambda: stack.request(f'/jobs/{job["id"]}')["state"] == "SUCCEEDED",
        )
        for node in nodes:
            response = stack.request(
                f'/nodes/{node["nodeId"]}/query?kind=NEIGHBOR_KEEPALIVE_STATUS',
                viewer=True,
            )
            assert response["source"] == "vpp"
            assert any(
                r.get("recordType") == "policy" and bool(r.get("enabled")) == enabled
                for r in response["records"]
            )

    switch(True)
    saved = json.loads(de(l2, "cat", "/var/lib/sdn-agent/vpp.json").stdout)
    assert (
        saved["version"] == 3
        and saved["desired"]["neighborKeepalive"]["enabled"] is True
    )
    results["enabled policy is persisted in adapter state"] = True
    if stack:
        results["Web gateway Controller candidate commits enabled policy"] = True
    wait(
        "native IPv4 and IPv6 targets enrolled",
        lambda: target(l2, "192.168.20.12") and target(l2, "2001:db8:20::12"),
    )
    # Native probes must keep working without the Python adapter scheduling them.
    pid = de(l2, "pgrep", "-f", "^python3 .*vpp/adapter.py").stdout.strip()
    supervisor = de(
        l2, "pgrep", "-f", "^python3 .*sdn-agent/supervisor.py($| -config)"
    ).stdout.strip()
    assert pid.isdecimal() and supervisor.isdecimal()
    before = policy(l2).get("confirmed", 0)
    capture = lab["STAMP"] + "-probe-capture"
    lab["run"](
        "docker",
        "run",
        "-d",
        "--name",
        capture,
        "--network",
        "container:" + pcs[1],
        "--cap-add",
        "NET_RAW",
        "--entrypoint",
        "python3",
        "-v",
        str(lab["ROOT"] / "lab/vpp_keepalive_capture.py") + ":/tmp/capture.py:ro",
        lab["IMAGE"],
        "/tmp/capture.py",
    )
    lab["containers"].append(capture)
    # The production supervisor deliberately shuts down a bundle whose adapter
    # stops responding. Suspend that supervisor too, only in this disposable
    # experiment, to test the native engine independently of that safeguard.
    de(l2, "kill", "-STOP", supervisor)
    de(l2, "kill", "-STOP", pid)
    try:
        time.sleep(45)
    finally:
        de(l2, "kill", "-CONT", pid)
        de(l2, "kill", "-CONT", supervisor)
    (out / "keepalive-after-pause.json").write_text(json.dumps(rows(l2), indent=2))
    lab["run"](
        "docker",
        "cp",
        capture + ":/tmp/keepalive-wire.json",
        out / "keepalive-wire.json",
    )
    wire = json.loads((out / "keepalive-wire.json").read_text())
    for kind in ("ARP", "NS"):
        assert any(
            r["kind"] == kind
            and r["interface"] == "eth0"
            and r["unicast"]
            and r["dstMac"] == "02:00:00:10:00:02"
            for r in wire
        ), wire
    results["ARP and IPv6 NS are unicast on local access port"] = True
    optionless_na = next(
        r for r in wire if r["kind"] == "NA" and r["payloadLength"] == 24
    )
    results["Linux replies with solicited NA without TLLA"] = True
    wait(
        "native probes continue while adapter paused",
        lambda: policy(l2).get("confirmed", 0) > before,
    )
    results["native keepalive independent of Python reconciliation"] = True
    # No PC-originated ping during this period: only replies to native probes.
    # 150 additional seconds exceeds the configured 120-second MAC age.
    timeline = []
    until = time.monotonic() + 150
    while time.monotonic() < until:
        sample = rows(l2)
        timeline.append({"time": time.time(), "records": sample})
        assert not any(
            r.get("state") == "failed"
            and r.get("ip") in ("192.168.20.12", "2001:db8:20::12")
            for r in sample
        ), sample
        time.sleep(5)
    (out / "keepalive-idle.json").write_text(json.dumps(timeline, indent=2))
    assert "192.168.20.12/32" in cli(l1, "show ip fib table 1001")
    assert "2001:db8:20::12/128" in cli(l1, "show ip6 fib table 1001")
    assert policy(l2).get("confirmed", 0) >= before + 4
    results["silent dual-stack PC survives beyond MAC age"] = True
    failed = submit(
        l2,
        [
            op("NEIGHBOR_KEEPALIVE", "SET", "neighbor_keepalive", enabled=False),
            op("BD_PORT", "BIND", "bridge_port", bridge="br20", port="missing0"),
        ],
        "keepalive-rollback",
        check=False,
    )
    assert json.loads(failed.stdout)["state"] == "FAILED_ROLLED_BACK", failed.stdout
    assert policy(l2)["enabled"] is True
    results["failed transaction restores enabled policy through Undo and readback"] = (
        True
    )

    de(pcs[1], "ip", "addr", "del", "192.168.20.12/24", "dev", "eth0")
    wait(
        "IPv4-only failure withdraws Type2",
        lambda: target(l2, "192.168.20.12").get("state") == "failed"
        and "192.168.20.12/32" not in cli(l1, "show ip fib table 1001"),
    )
    assert target(l2, "2001:db8:20::12").get("state") != "failed"
    assert "2001:db8:20::12/128" in cli(l1, "show ip6 fib table 1001")
    assert "02:00:00:10:00:02" in cli(l2, "show l2fib verbose")
    results["failed IPv4 binding does not delete shared MAC or IPv6 binding"] = True
    de(pcs[1], "ip", "addr", "add", "192.168.20.12/24", "dev", "eth0")
    # Removing the last IPv4 address also removes Linux's route via this subnet.
    de(pcs[1], "ip", "route", "replace", "192.168.0.0/16", "via", "192.168.20.254")
    de(pcs[1], "ping", "-c", "2", "192.168.20.254")
    wait(
        "IPv4 recovery after genuine relearning",
        lambda: "192.168.20.12/32" in cli(l1, "show ip fib table 1001"),
    )
    de(pcs[1], "ip", "-6", "addr", "del", "2001:db8:20::12/64", "dev", "eth0")
    injector = lab["STAMP"] + "-na-inject"
    lab["run"](
        "docker",
        "run",
        "-d",
        "--name",
        injector,
        "--network",
        "container:" + pcs[1],
        "--cap-add",
        "NET_RAW",
        "--entrypoint",
        "python3",
        "-v",
        str(lab["ROOT"] / "lab/vpp_keepalive_inject.py") + ":/tmp/inject.py:ro",
        lab["IMAGE"],
        "/tmp/inject.py",
        optionless_na["hex"],
    )
    lab["containers"].append(injector)
    wait(
        "IPv6-only failure withdraws Type2",
        lambda: target(l2, "2001:db8:20::12").get("state") == "failed"
        and "2001:db8:20::12/128" not in cli(l1, "show ip6 fib table 1001"),
    )
    assert "192.168.20.12/32" in cli(l1, "show ip fib table 1001")
    results["failed IPv6 binding does not delete healthy IPv4 binding"] = True
    results["wrong-MAC optionless NA cannot renew an existing binding"] = True
    lab["run"]("docker", "rm", "-fv", injector)
    lab["containers"].remove(injector)
    de(pcs[1], "ip", "-6", "addr", "add", "2001:db8:20::12/64", "dev", "eth0")
    de(
        pcs[1],
        "ip",
        "-6",
        "route",
        "replace",
        "2001:db8::/32",
        "via",
        "2001:db8:20::fe",
    )
    wait(
        "IPv6 relearning",
        lambda: de(
            pcs[1], "ping", "-6", "-c", "2", "-W", "1", "2001:db8:20::fe", check=False
        ).returncode
        == 0,
    )
    wait(
        "IPv6 route recovers",
        lambda: "2001:db8:20::12/128" in cli(l1, "show ip6 fib table 1001"),
    )
    switch(False)
    stopped = policy(l2).get("probes", 0)
    assert "192.168.20.12/32" in cli(l1, "show ip fib table 1001")
    time.sleep(10)
    assert not policy(l2).get("enabled") and policy(l2).get("probes", 0) == stopped
    results["disable stops probes without flushing healthy neighbors"] = True
    de(pcs[0], "ping", "-c", "2", "192.168.20.12")
    de(pcs[0], "ping", "-6", "-c", "2", "2001:db8:20::12")
    results["forwarding recovers and remains usable after switch off"] = True
