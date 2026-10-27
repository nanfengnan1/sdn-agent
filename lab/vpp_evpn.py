#!/usr/bin/env python3
"""Disposable three-leaf VPP/FRR/Agent multipoint acceptance test.

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
OUT = ROOT / "artifacts" / "vpp" / ("dual-ipv6" if UNDERLAY6 else ".")
OUT.mkdir(parents=True, exist_ok=True)
STAMP = "sdn-vpp-it-" + str(int(time.time()))
IMAGE = os.environ.get("SDN_VPP_IMAGE", "alpine/sdn-agent:vpp-dev")
TOKEN = secrets.token_urlsafe(24)
stack = None
containers = []
networks = []
results = {
    "image": IMAGE,
    "scope": f"L2 EVPN, IPv{6 if UNDERLAY6 else 4} VTEP, dual-stack inner, no IRB",
}
log = (OUT / "integration.log").open("w", buffering=1)


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
        name, "sdnctl", "-insecure", "-timeout", "40s", *args, command, check=check
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
    results["sourceOverrides"] = bool(os.environ.get("SDN_VPP_SOURCE"))
    config = OUT / (STAMP + "-agent.conf")
    config.write_text(
        json.dumps(
            {
                "agent": {"dataplane": "vpp", "insecure": True},
                "redis": {"enabled": True},
            }
        )
    )
    workers = int(os.environ.get("SDN_VPP_WORKERS", "0"))
    if not 0 <= workers <= 16:
        raise ValueError("SDN_VPP_WORKERS must be between 0 and 16")
    worker_config = OUT / (STAMP + "-vpp.conf")
    if workers:
        worker_config.write_text(
            (ROOT / "docker/vpp/startup.conf").read_text()
            + f"\ncpu {{ workers {workers} }}\n"
        )
    for suffix in ("underlay", "access1", "access2", "access3"):
        name = STAMP + "-" + suffix
        netnum = len(networks)
        ipv6 = [
            "--ipv6",
            "--subnet",
            f"fd43:{int(time.time()) % 65536:x}:{netnum}::/64",
        ]
        run("docker", "network", "create", *ipv6, name)
        networks.append(name)
    if os.environ.get("SDN_VPP_STACK") == "1":
        from vpp_stack import Stack

        stack = Stack(run, OUT, STAMP, networks[0], TOKEN)
    leaves = []
    pcs = []
    for i in (1, 2, 3):
        leaf = STAMP + "-leaf" + str(i)
        pc = STAMP + "-pc" + str(i)
        if stack:
            config = stack.agent_config(leaf)
        overrides = []
        if os.environ.get("SDN_VPP_SOURCE"):
            overrides = [
                "-v",
                str(Path(os.environ["SDN_VPP_SOURCE"]) / "extras/sdn-agent")
                + ":/usr/local/lib/sdn-agent/vpp:ro",
            ]
        if workers:
            overrides.extend(["-v", str(worker_config) + ":/etc/vpp/startup.conf:ro"])
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
        if workers:
            threads = cli(leaf, "show threads")
            assert threads.count("vpp_wk_") == workers, threads
            results[f"leaf{i} native workers"] = workers
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
            "alpine:3.22",
            "infinity",
        )
        containers.append(pc)
        pcs.append(pc)
        de(pc, "ip", "addr", "flush", "dev", "eth0")
        de(pc, "ip", "link", "set", "eth0", "address", f"02:00:00:10:00:0{i}")
        de(pc, "ip", "addr", "add", f"10.100.0.{i}/24", "dev", "eth0")
        de(pc, "ip", "-6", "addr", "add", f"2001:db8:100::{i}/64", "dev", "eth0")
    for i, leaf in enumerate(leaves):
        n = leaf["name"]
        peers = [v["ip"] for v in leaves if v != leaf]
        peer = peers[0]
        caps = json.loads(ctl(n, "capabilities").stdout)
        assert "dataplane/vpp-26.06" in caps["features"]
        assert "evpn-irb/v1" in caps["features"]
        assert "vxlan-multipoint/v1" in caps["features"]
        ops = [
            op("INTERFACE", "SET", "interface", name="uplink", up=True),
            op(
                "ADDRESS",
                "CREATE",
                "address",
                interface="uplink",
                prefix=f'{leaf["ip"]}/{leaf["prefix"]}',
            ),
            op(
                "STATIC_ROUTE",
                "CREATE",
                "route",
                prefix="198.51.100.42/32",
                gateway=leaves[(i + 1) % len(leaves)]["router_id"],
                interface="uplink",
            ),
            op("INTERFACE", "SET", "interface", name="access0", up=True),
            op("BD", "CREATE", "bridge", name="br100", stp=False),
            op(
                "INTERFACE",
                "SET",
                "interface",
                name="br100",
                up=True,
                addrgenmode="none",
            ),
            op(
                "VXLAN",
                "CREATE",
                "vxlan",
                name="vx100",
                vni=10100,
                local=leaf["ip"],
                underlay="uplink",
                port=4789,
                learning=False,
            ),
            op("BD_PORT", "BIND", "bridge_port", bridge="br100", port="access0"),
            op(
                "BD_PORT",
                "BIND",
                "bridge_port",
                bridge="br100",
                port="vx100",
                learning=False,
                neigh_suppress=False,
            ),
            op(
                "INTERFACE",
                "SET",
                "interface",
                name="vx100",
                up=True,
                addrgenmode="none",
            ),
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
                advertise_svi_ip=False,
            ),
        ]
        if UNDERLAY6:
            ops.insert(
                2,
                op(
                    "ADDRESS",
                    "CREATE",
                    "address",
                    interface="uplink",
                    prefix=f'{leaf["router_id"]}/{leaf["prefix4"]}',
                ),
            )
        for other in peers[1:]:
            ops.extend(
                [
                    op(
                        "BGP_NEIGHBOR",
                        "CREATE",
                        "peer",
                        asn=65000,
                        name=other,
                        remote_as=65000,
                    ),
                    op(
                        "BGP_EVPN_PEER",
                        "SET",
                        "peer",
                        asn=65000,
                        name=other,
                        active=True,
                    ),
                ]
            )
        submit(n, ops, "leaf" + str(i + 1))
        results["leaf" + str(i + 1) + " Agent configuration transaction"] = True
    l1, l2, l3 = [v["name"] for v in leaves]
    wait(
        "one native source-only interface contains two Type3 destinations",
        lambda: len(rows := query(l1, "VXLAN_TUNNELS").get("records", [])) == 1
        and rows[0].get("multipoint")
        and sum(bool(r["bum"]) for r in rows[0]["remotes"]) == 2,
    )
    wait(
        "inner IPv6 reaches third VTEP",
        lambda: de(
            pcs[0], "ping", "-6", "-c", "2", "-W", "1", "2001:db8:100::3", check=False
        ).returncode
        == 0,
    )
    original_index = query(l1, "VXLAN_TUNNELS")["records"][0]["sw_if_index"]
    wait(
        "same interface reaches third VTEP",
        lambda: de(
            pcs[0], "ping", "-c", "2", "-W", "1", "10.100.0.3", check=False
        ).returncode
        == 0,
    )
    wait(
        "hosts ping through VPP VXLAN",
        lambda: de(
            pcs[0], "ping", "-c", "2", "-W", "1", "10.100.0.2", check=False
        ).returncode
        == 0,
    )
    wait(
        "local VPP MAC advertised as EVPN Type2",
        lambda: "02:00:00:10:00:01"
        in de(l2, "vtysh", "-c", "show bgp l2vpn evpn route type macip").stdout,
    )
    wait(
        "remote Type2 programmed into VPP FDB",
        lambda: any(
            r.get("mac") == "02:00:00:10:00:02" and r.get("origin") == "remote"
            for r in query(l1, "FDB").get("records", [])
        ),
    )
    wait(
        "native MAC map selects two different remote VTEPs",
        lambda: {
            r["remote"]
            for r in query(l1, "VXLAN_TUNNELS")["records"][0]["remotes"]
            if not r["bum"] and r["mac"] in ("02:00:00:10:00:02", "02:00:00:10:00:03")
        }
        == {leaves[1]["ip"], leaves[2]["ip"]},
    )

    if stack:
        stack.verify(leaves)
        results["Web Gateway Controller Agent VPP queries"] = True

    def moved_mac(leaf, origin):
        return any(
            r.get("mac") == "02:00:00:10:00:01" and r.get("origin") == origin
            for r in query(leaf, "FDB").get("records", [])
        )

    def move_pc(source, target):
        run("docker", "network", "disconnect", source, pcs[0])
        run("docker", "network", "connect", target, pcs[0])
        de(pcs[0], "ip", "addr", "flush", "dev", "eth0")
        de(pcs[0], "ip", "link", "set", "eth0", "address", "02:00:00:10:00:01")
        de(pcs[0], "ip", "addr", "add", "10.100.0.1/24", "dev", "eth0")
        de(pcs[0], "ping", "-c", "2", "-W", "1", "10.100.0.254", check=False)

    move_pc(networks[1], networks[2])
    wait(
        "MAC mobility remote to local",
        lambda: moved_mac(l2, "local") and moved_mac(l1, "remote"),
    )
    move_pc(networks[2], networks[1])
    wait(
        "MAC mobility returns to original leaf",
        lambda: moved_mac(l1, "local") and moved_mac(l2, "remote"),
    )
    wait(
        "forwarding after MAC mobility",
        lambda: de(
            pcs[0], "ping", "-c", "2", "-W", "1", "10.100.0.2", check=False
        ).returncode
        == 0,
    )
    results["VPP L2 counters"] = cli(l1, "show errors")
    # Withdraw only Type-3 through BGP; Zebra reinstalls manually deleted FDBs.
    de(
        l2,
        "vtysh",
        "-c",
        "configure terminal",
        "-c",
        "route-map NOIMET deny 10",
        "-c",
        "match evpn route-type multicast",
        "-c",
        "exit",
        "-c",
        "route-map NOIMET permit 20",
        "-c",
        "exit",
        "-c",
        "router bgp 65000",
        "-c",
        "address-family l2vpn evpn",
        "-c",
        "neighbor " + leaves[0]["ip"] + " route-map NOIMET out",
        "-c",
        "end",
        "-c",
        "clear bgp l2vpn evpn " + leaves[0]["ip"] + " soft out",
    )
    wait(
        "Type3 withdrawal prunes only one replication destination",
        lambda: {
            r["remote"]
            for r in query(l1, "VXLAN_TUNNELS")["records"][0]["remotes"]
            if r["bum"]
        }
        == {leaves[2]["ip"]},
    )
    wait(
        "known unicast survives BUM withdrawal",
        lambda: de(
            pcs[0], "ping", "-c", "2", "-W", "1", "10.100.0.2", check=False
        ).returncode
        == 0,
    )
    # Exercise native API validation as well as the positive flood-class path.
    de(
        l1,
        "python3",
        "-c",
        """
import sys
sys.path.insert(0,"/usr/local/lib/sdn-agent/vpp")
from adapter import VPP,ALL
api=VPP("/run/vpp/api.sock","/usr/share/vpp/api")
_,_,tunnels,pairs=api.snapshot()
for index,cls in ((ALL,3),(pairs["uplink"],3),(next(iter(tunnels)),2)):
    try: api.call("l2_egress_flood",sw_if_index=index,is_set=True,flood_class=cls)
    except RuntimeError: pass
    else: raise AssertionError("invalid flood-class request accepted")
api.client.disconnect()
""",
    )
    results["native flood API rejects invalid interface and BVI class"] = True
    run("docker", "cp", ROOT / "lab/vpp_multipoint_api.py", l1 + ":/tmp/mp-test.py")
    de(l1, "python3", "/tmp/mp-test.py")
    results["native multipoint CLI API lifecycle and fixed-dst coexistence"] = True
    submit(
        l1,
        [
            op(
                "STATIC_ROUTE",
                "DELETE",
                "route",
                prefix="198.51.100.42/32",
                gateway=leaves[1]["router_id"],
                interface="uplink",
            )
        ],
        "route-delete",
    )
    results["static route LCP apply and delete verified"] = True
    # Configuration failure after a successful step must remove both mirrors.
    failure = submit(
        l1,
        [
            op("BD", "CREATE", "bridge", name="rollbackbd", stp=False),
            op("BD_PORT", "BIND", "bridge_port", bridge="rollbackbd", port="missing0"),
        ],
        "rollback",
        check=False,
    )
    assert failure.returncode != 0
    assert de(l1, "ip", "link", "show", "rollbackbd", check=False).returncode != 0
    assert not any(
        r.get("name") == "rollbackbd" for r in query(l1, "BRIDGES").get("records", [])
    )
    results["failed transaction rolls back Linux and VPP"] = True
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
    submit(
        l3,
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
        "withdraw-third",
    )
    wait(
        "EVPN withdrawal empties remotes and retains original native interface",
        lambda: len(rows := query(l1, "VXLAN_TUNNELS").get("records", [])) == 1
        and not rows[0]["remotes"]
        and rows[0]["sw_if_index"] == original_index,
    )
    results["status"] = "passed"
except Exception as exc:
    results["status"] = "failed"
    results["error"] = str(exc)
    traceback.print_exc()
finally:
    for n in containers:
        run("docker", "logs", "--tail", "120", n, check=False)
        if "-leaf" in n:
            de(n, "vppctl", "show", "errors", check=False)
            de(n, "vppctl", "show", "bridge-domain", check=False)
            de(n, "vppctl", "show", "l2fib", check=False)
            de(n, "bridge", "-j", "fdb", "show", check=False)
    if stack:
        stack.close()
    if os.environ.get("SDN_VPP_KEEP") != "1":
        for n in reversed(containers):
            run("docker", "rm", "-f", n, check=False)
        for n in reversed(networks):
            run("docker", "network", "rm", n, check=False)
    (OUT / "integration.json").write_text(json.dumps(results, indent=2) + "\n")
    log.close()
print(json.dumps(results, indent=2), flush=True)
if results["status"] != "passed":
    raise SystemExit(1)
