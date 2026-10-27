#!/usr/bin/env python3
"""Huawei VF underlay acceptance lab; prepare the PF with vpp_sriov_host.py first.

Only VFs carry Leaf/Spine traffic. Docker networking is management only.
The host preparation state is restored by default, including on test failure.
"""

import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import time
import traceback

from vpp_sriov_host import restore

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "alpine/sdn-agent:vpp-dev"


class Lab:
    def __init__(self, state, out, with_stack=False):
        self.state, self.out = state, out
        self.stamp = "sdn-vf-lab-" + str(time.time_ns())
        self.names = []
        self.nodes = {}
        self.pcs = {}
        self.stack = None
        self.network = None
        self.with_stack = with_stack
        self.token = secrets.token_urlsafe(24)
        self.report = {"status": "running", "checks": [], "host": state, "image": IMAGE}
        out.mkdir(parents=True, exist_ok=True)
        self.log = (out / "test.log").open("w", buffering=1)

    def run(self, *args, check=True, timeout=90):
        cp = subprocess.run(
            list(map(str, args)), text=True, capture_output=True, timeout=timeout
        )
        self.log.write(
            "$ "
            + " ".join(map(str, args)).replace(self.token, "<token>")
            + "\n"
            + cp.stdout
            + cp.stderr
        )
        if check and cp.returncode:
            raise RuntimeError(cp.stdout + cp.stderr)
        return cp

    def de(self, name, *args, **kw):
        return self.run("docker", "exec", name, *args, **kw)

    def cli(self, name, text):
        value = self.de(name, "vppctl", text).stdout
        if any(
            word in value.lower()
            for word in (
                "unknown input",
                "unknown interface",
                "failed",
                "invalid",
                "no such",
            )
        ):
            raise RuntimeError(value)
        return value

    def wait(self, label, fn, seconds=45):
        deadline, last = time.monotonic() + seconds, None
        while time.monotonic() < deadline:
            try:
                last = fn()
                if last:
                    self.report["checks"].append(label)
                    print("PASS", label, flush=True)
                    return last
            except Exception as exc:
                last = str(exc)
            time.sleep(1)
        raise AssertionError(f"{label}: {last}")

    def start(self):
        network = self.stamp + "-mgmt"
        self.run("docker", "network", "create", network)
        self.network = network
        if self.with_stack:
            from vpp_stack import Stack

            self.stack = Stack(self.run, self.out, self.stamp, network, self.token)
        self.report["imageId"] = self.run(
            "docker", "image", "inspect", IMAGE, "--format", "{{.Id}}"
        ).stdout.strip()
        config = self.out / "agent.conf"
        config.write_text(
            json.dumps(
                {
                    "agent": {"dataplane": "vpp", "insecure": True},
                    "redis": {"enabled": True},
                }
            )
        )
        for n, vf in enumerate(self.state["vfs"]):
            role = vf["role"]
            name = self.stamp + "-" + role
            node_config = self.stack.agent_config(name) if self.stack else config
            pc_mount = []
            if role.startswith("leaf"):
                pc = self.stamp + "-pc" + role[-1]
                self.run(
                    "docker",
                    "run",
                    "-d",
                    "--name",
                    pc,
                    "--network",
                    "none",
                    "--cap-add",
                    "NET_ADMIN",
                    "--cap-add",
                    "NET_RAW",
                    "--sysctl",
                    "net.ipv6.conf.all.disable_ipv6=0",
                    "--entrypoint",
                    "sleep",
                    IMAGE,
                    "infinity",
                )
                self.names.append(pc)
                self.pcs[role] = pc
                pid = self.run(
                    "docker", "inspect", "--format", "{{.State.Pid}}", pc
                ).stdout.strip()
                assert pid.isdecimal() and int(pid) > 0
                pc_mount = ["-v", f"/proc/{pid}/ns/net:/run/netns/pc:ro"]
            startup = self.out / (role + "-startup.conf")
            startup.write_text("""unix {
 nodaemon
 runtime-dir /run/vpp
 log /run/vpp/log/vpp.log
 cli-listen /run/vpp/cli.sock
}
memory { main-heap-size 1G main-heap-page-size 1G }
buffers { buffers-per-numa 16384 page-size 1G }
statseg { size 64M page-size default }
socksvr { socket-name /run/vpp/api.sock }
cpu { main-core %d corelist-workers %d }
plugins {
 plugin default { disable }
 plugin dpdk_plugin.so { enable }
 plugin tap_plugin.so { enable }
 plugin linux_cp_plugin.so { enable }
 plugin linux_nl_plugin.so { enable }
 plugin vxlan_plugin.so { enable }
 plugin ping_plugin.so { enable }
}
dpdk {
 file-prefix %s
 dev %s { name vf0 num-rx-queues 1 num-tx-queues 2 num-rx-desc 1024 num-tx-desc 1024 }
}
""" % (18 + n * 4, 20 + n * 4, name, vf["pci"]))
            self.run(
                "docker",
                "run",
                "-d",
                "--name",
                name,
                "--network",
                network,
                "--sysctl",
                "net.ipv4.ip_forward=1",
                "--sysctl",
                "net.ipv6.conf.all.forwarding=1",
                "--sysctl",
                "net.ipv6.conf.all.disable_ipv6=0",
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
                "--device",
                "/dev/vfio/vfio",
                "--device",
                "/dev/vfio/" + vf["group"],
                "-v",
                "/dev/hugepages:/dev/hugepages",
                "-v",
                "/lib/firmware:/lib/firmware:ro",
                "-v",
                str(node_config) + ":/etc/sdn-agent/agent.conf:ro",
                "-v",
                str(startup) + ":/etc/vpp/startup.conf:ro",
                "-e",
                "SDN_TOKEN=" + self.token,
                *pc_mount,
                IMAGE,
            )
            self.names.append(name)
            self.nodes[role] = name
            self.wait(
                role + " bundle healthy",
                lambda: self.de(
                    name, "/usr/local/bin/sdn-agent-healthcheck", check=False
                ).returncode
                == 0,
                90,
            )
            self.cli(name, "set interface state vf0 up")
            self.cli(name, "lcp create vf0 host-if uplink")
            self.cli(name, "lcp lcp-sync on")
            self.de(name, "ip", "link", "set", "uplink", "up")
            self.cli(name, "trace add dpdk-input 40")
        self.report["vfPci"] = {vf["role"]: vf["pci"] for vf in self.state["vfs"]}

    def underlay(self):
        spine = self.nodes["spine"]
        for i in (1, 2):
            leaf = self.nodes["leaf" + str(i)]
            vlan = 3100 + i
            self.cli(spine, f"create sub-interfaces vf0 {vlan}")
            self.cli(spine, f"set interface state vf0.{vlan} up")
            self.cli(spine, f"lcp create vf0.{vlan} host-if up{i}")
            self.de(spine, "ip", "link", "set", "up" + str(i), "up")
            for name, interface, v4, v6 in (
                (leaf, "uplink", f"192.0.2.{(i-1)*2}", f"fd80:{vlan}::2"),
                (spine, "up" + str(i), f"192.0.2.{(i-1)*2+1}", f"fd80:{vlan}::1"),
            ):
                self.de(name, "ip", "addr", "add", v4 + "/31", "dev", interface)
                self.de(
                    name,
                    "ip",
                    "-6",
                    "addr",
                    "add",
                    v6 + "/64",
                    "dev",
                    interface,
                    "nodad",
                )
            self.wait(
                f"leaf{i} VF IPv4 adjacency",
                lambda: self.de(
                    leaf,
                    "ping",
                    "-c",
                    "2",
                    "-W",
                    "1",
                    f"192.0.2.{(i-1)*2+1}",
                    check=False,
                ).returncode
                == 0,
                20,
            )
            self.wait(
                f"leaf{i} VF IPv6 adjacency",
                lambda: self.de(
                    leaf,
                    "ping",
                    "-6",
                    "-c",
                    "2",
                    "-W",
                    "1",
                    f"fd80:{vlan}::1",
                    check=False,
                ).returncode
                == 0,
                20,
            )
            self.de(
                leaf,
                "ip",
                "route",
                "add",
                f"192.0.2.{(2-i)*2}/31",
                "via",
                f"192.0.2.{(i-1)*2+1}",
            )
            self.de(
                leaf,
                "ip",
                "-6",
                "route",
                "add",
                f"fd80:{3103-i}::/64",
                "via",
                f"fd80:{vlan}::1",
            )
        self.wait(
            "Leaf1-Spine-Leaf2 IPv4 routed underlay",
            lambda: self.de(
                self.nodes["leaf1"],
                "ping",
                "-c",
                "3",
                "-W",
                "1",
                "192.0.2.2",
                check=False,
            ).returncode
            == 0,
        )
        self.wait(
            "Leaf1-Spine-Leaf2 IPv6 routed underlay",
            lambda: self.de(
                self.nodes["leaf1"],
                "ping",
                "-6",
                "-c",
                "3",
                "-W",
                "1",
                "fd80:3102::2",
                check=False,
            ).returncode
            == 0,
        )

    def collect(self):
        for role, name in self.nodes.items():
            for label, command in (
                ("hardware", "show hardware-interfaces"),
                ("interfaces", "show interface"),
                ("errors", "show errors"),
                ("lcp", "show lcp"),
                ("ip4", "show ip fib"),
                ("ip6", "show ip6 fib"),
                ("trace", "show trace"),
            ):
                cp = self.de(name, "vppctl", command, check=False)
                (self.out / (role + "-" + label + ".txt")).write_text(
                    cp.stdout + cp.stderr
                )
            cp = self.run("docker", "logs", "--tail", "150", name, check=False)
            (self.out / (role + "-container.log")).write_text(cp.stdout + cp.stderr)

    def cleanup(self):
        for name in reversed(self.names):
            self.run("docker", "rm", "-f", name)
        if self.stack:
            self.stack.close()
        if self.network:
            self.run("docker", "network", "rm", self.network)


def main():
    def interrupted(signum, _frame):
        raise SystemExit(f"interrupted by signal {signum}")

    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--state", type=Path, default=ROOT / "artifacts/vpp/sriov/host-state.json"
    )
    parser.add_argument("--out", type=Path, default=ROOT / "artifacts/vpp/sriov/probe")
    parser.add_argument(
        "--retain-host",
        action="store_true",
        help="keep prepared VFs for another lab run; containers are still removed",
    )
    parser.add_argument(
        "--evpn",
        action="store_true",
        help="also test PC TAPs, EVPN L2 and symmetric IRB",
    )
    parser.add_argument("--vtep-family", choices=("4", "6"), default="4")
    parser.add_argument(
        "--stack",
        action="store_true",
        help="also verify Web Gateway/Controller queries",
    )
    parser.add_argument(
        "--offload",
        action="store_true",
        help="also verify TAP TCP GSO and VF RX packet checksums",
    )
    args = parser.parse_args()
    state = json.loads(args.state.read_text())
    assert state["status"] == "ready", "prepare host first"
    lab = Lab(state, args.out.resolve(), args.stack)
    try:
        lab.start()
        lab.underlay()
        if args.evpn or args.offload:
            from vpp_sriov_evpn import acceptance

            acceptance(lab, int(args.vtep_family))
        if args.offload:
            from vpp_sriov_offload import acceptance as offload_acceptance

            offload_acceptance(lab)
        if lab.stack:
            lab.stack.verify(
                [{"name": name} for name in lab.nodes.values()], require_vxlan=False
            )
            lab.report["checks"].append(
                "Web Gateway/Controller/Agent native VPP queries on three VF nodes"
            )
        lab.report["status"] = "passed"
    except BaseException:
        lab.report["status"] = "failed"
        lab.report["error"] = traceback.format_exc()
        raise
    finally:
        try:
            lab.collect()
        finally:
            try:
                lab.cleanup()
            except BaseException:
                lab.report["status"] = "cleanup-failed"
                lab.report["cleanupError"] = traceback.format_exc()
                raise
            finally:
                try:
                    if not args.retain_host:
                        lab.report["restore"] = restore(args.state)["status"]
                except BaseException:
                    lab.report["status"] = "restore-failed"
                    lab.report["restoreError"] = traceback.format_exc()
                    raise
                finally:
                    (lab.out / "report.json").write_text(
                        json.dumps(lab.report, indent=2) + "\n"
                    )


if __name__ == "__main__":
    main()
