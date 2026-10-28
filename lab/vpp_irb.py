#!/usr/bin/env python3
"""Disposable two-leaf VPP/FRR/Agent data-plane acceptance test.

No PCI devices, huge pages, host interfaces, existing containers or published
ports. All resource mutations are inside uniquely named test containers.
"""

import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import traceback

ROOT = Path(__file__).resolve().parents[1]
UNDERLAY6 = os.environ.get("SDN_VPP_UNDERLAY") == "6"
OUT = Path(
    os.environ.get(
        "SDN_VPP_OUTPUT",
        ROOT / "artifacts" / "vpp" / ("dual-ipv6" if UNDERLAY6 else "."),
    )
).resolve()
OUT.mkdir(parents=True, exist_ok=True)
STAMP = "sdn-vpp-irb-" + str(int(time.time())) + "-" + secrets.token_hex(3)
# Independent runs need independent IPv6 pools even when started in one second.
ula_id = secrets.token_hex(5)
ULA_PREFIX = f"fd{ula_id[:2]}:{ula_id[2:6]}:{ula_id[6:]}"
IMAGE = os.environ.get("SDN_VPP_IMAGE", "alpine/sdn-agent:vpp-dev")
TOKEN = secrets.token_urlsafe(24)
containers = []
networks = []
stack = None
results = {
    "image": IMAGE,
    "scope": f"EVPN symmetric IRB, IPv{6 if UNDERLAY6 else 4} VTEP, dual-stack inner",
}
log = (OUT / "irb.log").open("w", buffering=1)


class KeepaliveComplete(Exception):
    """Skip unrelated destructive IRB regression cases in the focused run."""


def run(*args, check=True, timeout=90):
    cp = subprocess.run(
        [str(x) for x in args], text=True, capture_output=True, timeout=timeout
    )
    # Never write the ephemeral authentication token into the report.
    log.write(
        "$ "
        + " ".join(str(x).replace(TOKEN, "<test-token>") for x in args)
        + "\n"
        + cp.stdout
        + cp.stderr
        + "\n"
    )
    if check and cp.returncode:
        raise RuntimeError(cp.stdout + cp.stderr)
    return cp


def de(name, *args, check=True):
    return run("docker", "exec", name, *args, check=check)


def cli(name, line):
    return de(name, "vppctl", "-s", "/run/vpp/cli.sock", line).stdout


def ctl(name, command, *args, check=True):
    return de(
        name, "sdnctl", "-insecure", "-timeout", "80s", *args, command, check=check
    )


def query(name, kind):
    return json.loads(ctl(name, "query", "-kind", kind).stdout)


def wait(label, fn, seconds=70):
    until = time.monotonic() + seconds
    last = None
    while time.monotonic() < until:
        try:
            last = fn()
            if last:
                results[label] = True
                print("PASS", label, flush=True)
                return last
        except Exception as exc:
            last = str(exc)
        time.sleep(1)
    raise AssertionError(f"{label}: {last}")


def submit(name, ops, label, check=True):
    path = OUT / (STAMP + "-" + label + ".json")
    previous = None
    for i, op in enumerate(ops):
        op["id"] = str(i)
        if previous is not None:
            op["depends_on"] = [previous]
        previous = str(i)
    path.write_text(json.dumps({"operations": ops}))
    run("docker", "cp", path, name + ":/tmp/operations.json")
    return ctl(name, "submit", "-file", "/tmp/operations.json", check=check)


def op(code, action, key, **data):
    return {"code": code, "action": action, key: data}


def startup(name):
    return de(name, "/usr/local/bin/sdn-agent-healthcheck", check=False).returncode == 0


try:
    results["imageId"] = run(
        "docker", "image", "inspect", IMAGE, "--format", "{{.Id}}"
    ).stdout.strip()
    IMAGE = results[
        "imageId"
    ]  # Pin every test container if a tag is rebuilt concurrently.
    results["sourceOverrides"] = bool(os.environ.get("SDN_VPP_SOURCE"))
    workers = int(os.environ.get("SDN_VPP_WORKERS", "0"))
    if not 0 <= workers <= 16:
        raise ValueError("SDN_VPP_WORKERS must be between 0 and 16")
    worker_config = OUT / (STAMP + "-vpp.conf")
    if workers:
        worker_config.write_text(
            (ROOT / "docker/vpp/startup.conf").read_text()
            + f"\ncpu {{ workers {workers} }}\n"
        )
    results["workers"] = workers
    config = OUT / (STAMP + "-agent.conf")
    config.write_text(
        json.dumps(
            {
                "agent": {"dataplane": "vpp", "insecure": True},
                "redis": {"enabled": True},
            }
        )
    )
    for network_index, suffix in enumerate(("underlay", "access1", "access2")):
        name = STAMP + "-" + suffix
        ipv6 = (
            [
                "--ipv6",
                "--subnet",
                f"{ULA_PREFIX}:{network_index:x}::/64",
            ]
            if network_index or UNDERLAY6
            else []
        )
        run("docker", "network", "create", *ipv6, name)
        networks.append(name)
    if os.environ.get("SDN_VPP_STACK") == "1":
        from vpp_stack import Stack

        stack = Stack(run, OUT, STAMP, networks[0], TOKEN)
    leaves = []
    pcs = []
    for i in (1, 2):
        leaf = STAMP + "-leaf" + str(i)
        pc = STAMP + "-pc" + str(i)
        if stack:
            config = stack.agent_config(leaf)
        overrides = []
        if workers:
            overrides += ["-v", str(worker_config) + ":/etc/vpp/startup.conf:ro"]
        if os.environ.get("SDN_VPP_SOURCE"):
            overrides += [
                "-v",
                str(Path(os.environ["SDN_VPP_SOURCE"]) / "extras/sdn-agent")
                + ":/usr/local/lib/sdn-agent/vpp:ro",
            ]
        run(
            "docker",
            "run",
            "-d",
            "--name",
            leaf,
            "--network",
            networks[0],
            "--cap-add",
            "NET_ADMIN",
            "--cap-add",
            "NET_RAW",
            "--cap-add",
            "SYS_ADMIN",
            "--cap-add",
            "IPC_LOCK",
            "--cap-add",
            "SYS_NICE",
            "--ulimit",
            "memlock=-1:-1",
            "--device",
            "/dev/net/tun",
            "--device",
            "/dev/vhost-net",
            "-e",
            "SDN_TOKEN=" + TOKEN,
            "-v",
            str(config) + ":/etc/sdn-agent/agent.conf:ro",
            *overrides,
            IMAGE,
        )
        containers.append(leaf)
        leaves.append(leaf)
        run("docker", "network", "connect", networks[i], leaf)
        wait("leaf" + str(i) + " bundle healthy", lambda n=leaf: startup(n))
        info = json.loads(run("docker", "inspect", leaf).stdout)[0]
        underlay_info = info["NetworkSettings"]["Networks"][networks[0]]
        router_id = underlay_info["IPAddress"]
        ip = underlay_info["GlobalIPv6Address"] if UNDERLAY6 else router_id
        prefix = (
            underlay_info["GlobalIPv6PrefixLen"]
            if UNDERLAY6
            else underlay_info["IPPrefixLen"]
        )
        for original, mirror in (("eth0", "uplink"), ("eth1", "access0")):
            cli(leaf, "create host-interface name " + original)
            cli(leaf, "set interface state host-" + original + " up")
            cli(leaf, "lcp create host-" + original + " host-if " + mirror)
            de(leaf, "ip", "addr", "flush", "dev", original)
        cli(leaf, "lcp lcp-sync on")
        leaves[-1] = {
            "name": leaf,
            "ip": ip,
            "prefix": prefix,
            "router_id": router_id,
            "prefix4": underlay_info["IPPrefixLen"],
        }
        run(
            "docker",
            "run",
            "-d",
            "--name",
            pc,
            "--network",
            networks[i],
            "--cap-add",
            "NET_ADMIN",
            "--entrypoint",
            "sleep",
            "--sysctl",
            "net.ipv6.conf.all.disable_ipv6=0",
            "--sysctl",
            "net.ipv6.conf.default.disable_ipv6=0",
            "--sysctl",
            "net.ipv6.conf.all.accept_ra=0",
            "--sysctl",
            "net.ipv6.conf.default.accept_ra=0",
            "alpine:3.22",
            "infinity",
        )
        containers.append(pc)
        pcs.append(pc)
        de(pc, "ip", "addr", "flush", "dev", "eth0")
        # Docker's IPv6 gateway must not become a fallback next hop while the
        # emulated Leaf's ND entry is probing. PCs use only the lab gateways.
        de(pc, "ip", "-6", "route", "flush", "default")
        de(pc, "ip", "-6", "addr", "flush", "dev", "eth0", "scope", "global")
        de(pc, "ip", "link", "set", "eth0", "address", f"02:00:00:10:00:0{i}")
        de(pc, "ip", "addr", "add", f"192.168.{i*10}.{10+i}/24", "dev", "eth0")
        de(pc, "ip", "route", "replace", "192.168.0.0/16", "via", f"192.168.{i*10}.254")
    for i, leaf in enumerate(leaves, 1):
        n, peer, seg = leaf["name"], leaves[2 - i]["ip"], i * 10
        ops = [
            op("INTERFACE", "SET", "interface", name="uplink", up=True),
            op(
                "ADDRESS",
                "CREATE",
                "address",
                interface="uplink",
                prefix=f'{leaf["ip"]}/{leaf["prefix"]}',
            ),
            op("INTERFACE", "SET", "interface", name="access0", up=True),
            op("TENANT", "CREATE", "tenant", name="TENANT-A", table_id=1001),
            op("INTERFACE", "SET", "interface", name="TENANT-A", up=True),
        ]
        for bridge, vx, vni, mac in (
            ("br50000", "vx50000", 50000, f"02:00:00:00:ff:0{i}"),
            (f"br{seg}", f"vx{seg}", 10000 + seg, f"02:00:00:00:{seg:02x}:01"),
        ):
            ops += [
                op("BD", "CREATE", "bridge", name=bridge, stp=False, mac=mac),
                op("BD_TENANT", "BIND", "binding", bridge=bridge, tenant="TENANT-A"),
                op(
                    "INTERFACE",
                    "SET",
                    "interface",
                    name=bridge,
                    up=True,
                    addrgenmode="none",
                ),
                op(
                    "VXLAN",
                    "CREATE",
                    "vxlan",
                    name=vx,
                    vni=vni,
                    local=leaf["ip"],
                    underlay="uplink",
                    port=4789,
                    learning=False,
                ),
                op(
                    "BD_PORT",
                    "BIND",
                    "bridge_port",
                    bridge=bridge,
                    port=vx,
                    learning=False,
                    neigh_suppress=True,
                ),
                op(
                    "INTERFACE",
                    "SET",
                    "interface",
                    name=vx,
                    up=True,
                    addrgenmode="none",
                ),
            ]
        ops += [
            op(
                "ADDRESS",
                "CREATE",
                "address",
                interface=f"br{seg}",
                prefix=f"192.168.{seg}.254/24",
            ),
            op(
                "BD_PORT",
                "BIND",
                "bridge_port",
                bridge=f"br{seg}",
                port="access0",
                learning=True,
            ),
            op("TENANT_L3VNI", "BIND", "l3vni", tenant="TENANT-A", vni=50000),
            op(
                "BGP_INSTANCE",
                "CREATE",
                "bgp",
                asn=65000,
                router_id=leaf["router_id"],
                default_ipv4_unicast=False,
            ),
            op("BGP_NEIGHBOR", "CREATE", "peer", asn=65000, name=peer, remote_as=65000),
            op("BGP_EVPN_PEER", "SET", "peer", asn=65000, name=peer, active=True),
            op(
                "BGP_EVPN_GLOBAL",
                "SET",
                "evpn_global",
                asn=65000,
                advertise_all_vni=True,
                advertise_svi_ip=True,
            ),
            op(
                "EVPN_L2VNI",
                "SET",
                "evpn",
                asn=65000,
                vni=10000 + seg,
                rd=f'{leaf["router_id"]}:{seg}',
                import_rts=[f"65000:{10000+seg}"],
                export_rts=[f"65000:{10000+seg}"],
            ),
            op(
                "BGP_INSTANCE",
                "CREATE",
                "bgp",
                asn=65000,
                vrf="TENANT-A",
                router_id=leaf["router_id"],
                default_ipv4_unicast=False,
            ),
            op(
                "EVPN_TENANT",
                "SET",
                "evpn",
                asn=65000,
                vrf="TENANT-A",
                rd=f'{leaf["router_id"]}:50000',
                import_rts=["65000:1"],
                export_rts=["65000:1"],
            ),
        ]
        submit(n, ops, "leaf" + str(i))
        results["leaf" + str(i) + " transaction"] = True
    l1, l2 = [l["name"] for l in leaves]
    for i, pc in enumerate(pcs, 1):
        wait(
            "VPP gateway ARP and ping leaf" + str(i),
            lambda i=i, pc=pc: de(
                pc, "ping", "-c", "2", "-W", "1", f"192.168.{i*10}.254", check=False
            ).returncode
            == 0,
        )
    wait(
        "Type2 MAC/IP imported by remote tenant",
        lambda: "192.168.20.12"
        in de(l1, "ip", "route", "show", "table", "1001").stdout,
    )
    wait(
        "VPP tenant FIB contains EVPN remote host route",
        lambda: "192.168.20.12/32" in cli(l1, "show ip fib table 1001"),
    )
    wait(
        "symmetric IRB leaf1 to leaf2",
        lambda: de(
            pcs[0], "ping", "-c", "3", "-W", "1", "192.168.20.12", check=False
        ).returncode
        == 0,
    )
    wait(
        "symmetric IRB leaf2 to leaf1",
        lambda: de(
            pcs[1], "ping", "-c", "3", "-W", "1", "192.168.10.11", check=False
        ).returncode
        == 0,
    )
    for i, leaf in enumerate(leaves, 1):
        submit(
            leaf["name"],
            [
                op(
                    "ADDRESS",
                    "CREATE",
                    "address",
                    interface=f"br{i*10}",
                    prefix=f"2001:db8:{i*10}::fe/64",
                )
            ],
            "ipv6-gateway" + str(i),
        )
        de(
            pcs[i - 1],
            "ip",
            "-6",
            "addr",
            "add",
            f"2001:db8:{i*10}::{10+i}/64",
            "dev",
            "eth0",
        )
        de(
            pcs[i - 1],
            "ip",
            "-6",
            "route",
            "add",
            "2001:db8::/32",
            "via",
            f"2001:db8:{i*10}::fe",
        )
        wait(
            "IPv6 gateway ND leaf" + str(i),
            lambda i=i: de(
                pcs[i - 1],
                "ping",
                "-6",
                "-c",
                "2",
                "-W",
                "1",
                f"2001:db8:{i*10}::fe",
                check=False,
            ).returncode
            == 0,
        )
    wait(
        "IPv6 Type2 symmetric IRB",
        lambda: de(
            pcs[0], "ping", "-6", "-c", "3", "-W", "1", "2001:db8:20::12", check=False
        ).returncode
        == 0,
    )
    if os.environ.get("SDN_VPP_KEEPALIVE_ONLY") == "1":
        from vpp_keepalive import verify as verify_keepalive

        verify_keepalive(globals())
        results["status"] = "passed"
        raise KeepaliveComplete()
    # Verify the path is VPP even while all Linux mirror addresses remain up.
    bvi = next(
        r["vppName"] for r in query(l1, "INTERFACES")["records"] if r["name"] == "br10"
    )
    adapter_pid = de(l1, "pgrep", "-f", "^python3 .*vpp/adapter.py").stdout.strip()
    assert adapter_pid.isdecimal(), adapter_pid
    de(l1, "kill", "-STOP", adapter_pid)
    try:
        cli(l1, "set interface state " + bvi + " down")
        assert (
            de(
                pcs[0], "ping", "-c", "2", "-W", "1", "192.168.20.12", check=False
            ).returncode
            != 0
        )
        assert (
            "UP"
            in json.loads(de(l1, "ip", "-j", "link", "show", "br10").stdout)[0]["flags"]
        )
        results["VPP BVI down blocks traffic while Linux mirror remains up"] = True
    finally:
        cli(l1, "set interface state " + bvi + " up")
        de(l1, "kill", "-CONT", adapter_pid)
    wait(
        "traffic recovers after VPP BVI restoration",
        lambda: de(
            pcs[0], "ping", "-c", "2", "-W", "1", "192.168.20.12", check=False
        ).returncode
        == 0,
    )
    # Type-5 advertises the subnet. An additional host IP has never been
    # advertised as Type-2 and must first be reached through the prefix route.
    submit(
        l2,
        [
            op(
                "BGP_REDISTRIBUTE",
                "SET",
                "bgp_redistribute",
                asn=65000,
                vrf="TENANT-A",
                family="IPV4",
                protocol="connected",
            ),
            op(
                "EVPN_TYPE5",
                "SET",
                "bgp_type5",
                asn=65000,
                vrf="TENANT-A",
                family="IPV4",
                enabled=True,
            ),
        ],
        "type5",
    )
    wait(
        "Type5 subnet installed in VPP tenant FIB",
        lambda: "192.168.20.0/24" in cli(l1, "show ip fib table 1001"),
    )
    de(pcs[1], "ip", "addr", "add", "192.168.20.13/24", "dev", "eth0")
    wait(
        "Type5 forwards to previously silent destination",
        lambda: de(
            pcs[0], "ping", "-c", "3", "-W", "1", "192.168.20.13", check=False
        ).returncode
        == 0,
    )
    submit(
        l2,
        [
            op(
                "EVPN_TYPE5",
                "SET",
                "bgp_type5",
                asn=65000,
                vrf="TENANT-A",
                family="IPV4",
                enabled=False,
            )
        ],
        "type5-withdraw",
    )
    wait(
        "Type5 withdrawal removes VPP subnet route",
        lambda: "192.168.20.0/24" not in cli(l1, "show ip fib table 1001"),
    )
    submit(
        l2,
        [
            op(
                "BGP_REDISTRIBUTE",
                "SET",
                "bgp_redistribute",
                asn=65000,
                vrf="TENANT-A",
                family="IPV6",
                protocol="connected",
            ),
            op(
                "EVPN_TYPE5",
                "SET",
                "bgp_type5",
                asn=65000,
                vrf="TENANT-A",
                family="IPV6",
                enabled=True,
            ),
        ],
        "type5-ipv6",
    )
    wait(
        "IPv6 Type5 subnet installed in VPP tenant FIB",
        lambda: "2001:db8:20::/64" in cli(l1, "show ip6 fib table 1001"),
    )
    assert "2001:db8:20::13/128" not in cli(l1, "show ip6 fib table 1001")
    de(pcs[1], "ip", "-6", "addr", "add", "2001:db8:20::13/64", "dev", "eth0")
    wait(
        "IPv6 Type5 forwards to previously silent destination",
        lambda: de(
            pcs[0], "ping", "-6", "-c", "3", "-W", "1", "2001:db8:20::13", check=False
        ).returncode
        == 0,
    )
    submit(
        l2,
        [
            op(
                "EVPN_TYPE5",
                "SET",
                "bgp_type5",
                asn=65000,
                vrf="TENANT-A",
                family="IPV6",
                enabled=False,
            )
        ],
        "type5-ipv6-withdraw",
    )
    wait(
        "IPv6 Type5 withdrawal removes VPP subnet route",
        lambda: "2001:db8:20::/64" not in cli(l1, "show ip6 fib table 1001"),
    )
    # Separate VRF with an overlapping gateway; no route import is configured.
    rednet, redpc = STAMP + "-red", STAMP + "-redpc"
    run("docker", "network", "create", rednet)
    networks.append(rednet)
    run("docker", "network", "connect", rednet, l1)
    cli(l1, "create host-interface name eth2")
    cli(l1, "set interface state host-eth2 up")
    cli(l1, "lcp create host-eth2 host-if access1")
    de(l1, "ip", "addr", "flush", "dev", "eth2")
    run(
        "docker",
        "run",
        "-d",
        "--name",
        redpc,
        "--network",
        rednet,
        "--cap-add",
        "NET_ADMIN",
        "--entrypoint",
        "sleep",
        "alpine:3.22",
        "infinity",
    )
    containers.append(redpc)
    de(redpc, "ip", "addr", "flush", "dev", "eth0")
    de(redpc, "ip", "addr", "add", "192.168.10.22/24", "dev", "eth0")
    de(redpc, "ip", "route", "replace", "192.168.0.0/16", "via", "192.168.10.254")
    fixture = [
        op("TENANT", "CREATE", "tenant", name="TENANT-R", table_id=1002),
        op("INTERFACE", "SET", "interface", name="TENANT-R", up=True),
        op("BD", "CREATE", "bridge", name="br-red", mac="02:00:00:00:aa:01"),
        op("BD_TENANT", "BIND", "binding", bridge="br-red", tenant="TENANT-R"),
        op("INTERFACE", "SET", "interface", name="br-red", up=True),
        op(
            "ADDRESS",
            "CREATE",
            "address",
            interface="br-red",
            prefix="192.168.10.254/24",
        ),
        op("INTERFACE", "SET", "interface", name="access1", up=True),
        op(
            "BD_PORT",
            "BIND",
            "bridge_port",
            bridge="br-red",
            port="access1",
            learning=True,
        ),
    ]
    submit(l1, fixture, "red")
    wait(
        "overlapping subnet gateway in separate VPP tenant",
        lambda: de(
            redpc, "ping", "-c", "2", "-W", "1", "192.168.10.254", check=False
        ).returncode
        == 0,
    )
    assert (
        de(redpc, "ping", "-c", "2", "-W", "1", "192.168.20.12", check=False).returncode
        != 0
    )
    assert "192.168.20.12" not in cli(l1, "show ip fib table 1002")
    results["separate tenant cannot reach imported routes in TENANT-A"] = True
    route_failure = submit(
        l1,
        [
            op(
                "STATIC_ROUTE",
                "CREATE",
                "route",
                prefix="198.19.0.0/16",
                gateway="192.168.10.22",
                interface="br-red",
                table=1002,
            ),
            op("BD_PORT", "BIND", "bridge_port", bridge="br-red", port="missing0"),
        ],
        "tenant-route-rollback",
        check=False,
    )
    assert (
        json.loads(route_failure.stdout)["state"] == "FAILED_ROLLED_BACK"
    ), route_failure.stdout
    assert "198.19.0.0/16" not in cli(l1, "show ip fib table 1002")
    results["tenant static route rollback reads the correct FIB"] = True
    # Partial failure after a real BVI/FIB binding and address update.
    failed = submit(
        l1,
        [
            op(
                "ADDRESS",
                "CREATE",
                "address",
                interface="br-red",
                prefix="198.18.0.1/24",
            ),
            op("BD_PORT", "BIND", "bridge_port", bridge="br-red", port="missing0"),
        ],
        "irb-rollback",
        check=False,
    )
    assert json.loads(failed.stdout)["state"] == "FAILED_ROLLED_BACK", failed.stdout
    assert "198.18.0.1/24" not in cli(l1, "show interface address")
    assert "198.18.0.1" not in de(l1, "ip", "-j", "addr", "show", "br-red").stdout
    results["failed transaction rolls back VPP and Linux gateway address"] = True
    submit(
        l1,
        [
            op("BD_PORT", "UNBIND", "bridge_port", bridge="br-red", port="access1"),
            op(
                "ADDRESS",
                "DELETE",
                "address",
                interface="br-red",
                prefix="192.168.10.254/24",
            ),
            op("BD_TENANT", "UNBIND", "binding", bridge="br-red", tenant="TENANT-R"),
            op("BD", "DELETE", "bridge", name="br-red"),
            op("TENANT", "DELETE", "tenant", name="TENANT-R", table_id=1002),
        ],
        "delete-red",
    )
    assert not any(t["name"] == "TENANT-R" for t in query(l1, "TENANTS")["records"])
    assert "TENANT-R" not in cli(l1, "show ip fib")
    results["delete removes tenant FIB BVI BD and mirror filter"] = True
    stable = de(pcs[0], "ping", "-c", "40", "-W", "1", "192.168.20.12")
    assert ", 0% packet loss" in stable.stdout, stable.stdout
    results["40 seconds of forwarding without mirror NUD withdrawal"] = True
    if os.environ.get("SDN_VPP_KEEPALIVE") == "1":
        from vpp_keepalive import verify as verify_keepalive

        verify_keepalive(globals())
    submit(
        l2,
        [
            op(
                "BGP_EVPN_PEER",
                "SET",
                "peer",
                asn=65000,
                name=leaves[0]["ip"],
                active=False,
            )
        ],
        "withdraw",
    )
    wait(
        "EVPN withdrawal removes VPP host route",
        lambda: "192.168.20.12/32" not in cli(l1, "show ip fib table 1001"),
    )
    wait(
        "EVPN withdrawal removes remote mappings but keeps source-only VNIs",
        lambda: bool(rows := query(l1, "VXLAN_TUNNELS").get("records"))
        and all(r.get("multipoint") and not r.get("remotes") for r in rows),
    )
    results["status"] = "passed"
except KeepaliveComplete:
    pass
except Exception as exc:
    results["status"] = "failed"
    results["error"] = str(exc)
    traceback.print_exc()
finally:
    for n in containers:
        run("docker", "logs", "--tail", "80", n, check=False)
        if "-leaf" in n:
            for line in (
                "show errors",
                "show bridge-domain",
                "show l2fib",
                "show l2fib verbose",
                "show ip fib",
                "show ip6 fib",
                "show ip neighbors",
                "show interface address",
            ):
                de(n, "vppctl", line, check=False)
            de(n, "vtysh", "-c", "show bgp l2vpn evpn", check=False)
            de(n, "ip", "-j", "route", "show", "table", "1001", check=False)
            de(n, "ip", "-j", "neigh", "show", check=False)
    if os.environ.get("SDN_VPP_KEEP") != "1":
        if stack:
            stack.close()
        for n in reversed(containers):
            run("docker", "rm", "-f", n, check=False)
        for n in reversed(networks):
            run("docker", "network", "rm", n, check=False)
    (OUT / "irb.json").write_text(json.dumps(results, indent=2) + "\n")
    log.close()
print(json.dumps(results, indent=2), flush=True)
if results["status"] != "passed":
    raise SystemExit(1)
