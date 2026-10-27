#!/usr/bin/env python3
"""Real TAP TCP-GSO -> native multipoint VXLAN, plus wire checksum validation.

Two disposable containers, no physical NIC reconfiguration. Hardware TSO/LRO
performance is NOT inferred from this software fallback acceptance test.
"""

import hashlib
import ipaddress
import json
import os
import secrets
from pathlib import Path
import re
import struct
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "artifacts/vpp/offload"
OUT.mkdir(parents=True, exist_ok=True)
STAMP = "sdn-vpp-offload-" + str(time.time_ns())
IMAGE = "alpine/sdn-agent:vpp-dev"
names = []
report = {"status": "running", "checks": [], "image": IMAGE}
log = (OUT / "test.log").open("w", buffering=1)


def run(*args, check=True):
    cp = subprocess.run(
        [str(x) for x in args], text=True, capture_output=True, timeout=90
    )
    log.write("$ " + " ".join(map(str, args)) + "\n" + cp.stdout + cp.stderr)
    if check and cp.returncode:
        raise RuntimeError(cp.stdout + cp.stderr)
    return cp.stdout


def de(name, *args):
    return run("docker", "exec", name, *args)


def cli(name, line):
    value = de(name, "vppctl", line)
    if any(
        x in value.lower()
        for x in (
            "unknown input",
            "unknown interface",
            "no such file",
            "failed",
            "invalid",
        )
    ):
        raise RuntimeError(value)
    return value


def passed(label):
    report["checks"].append(label)
    print("PASS", label, flush=True)


def checksum(data):
    if len(data) % 2:
        data += b"\0"
    value = sum(struct.unpack("!%dH" % (len(data) // 2), data))
    while value >> 16:
        value = (value & 65535) + (value >> 16)
    return (~value) & 65535


def ip_payload(packet):
    if packet[0] >> 4 == 4:
        length = int.from_bytes(packet[2:4], "big")
        hlen = (packet[0] & 15) * 4
        assert checksum(packet[:hlen]) == 0, "bad IPv4 header checksum"
        proto = packet[9]
        body = packet[hlen:length]
        pseudo = packet[12:20] + bytes((0, proto)) + struct.pack("!H", len(body))
    else:
        assert packet[0] >> 4 == 6
        proto = packet[6]
        body = packet[40 : 40 + int.from_bytes(packet[4:6], "big")]
        while proto in (0, 43, 60):
            length = (body[1] + 1) * 8
            proto, body = body[0], body[length:]
        pseudo = packet[8:40] + struct.pack("!I3xB", len(body), proto)
    return proto, body, pseudo


def verify_pcap(path):
    data = path.read_bytes()
    assert data[:4] in (b"\xd4\xc3\xb2\xa1", b"\xa1\xb2\xc3\xd4")
    order = "<" if data[:4] == b"\xd4\xc3\xb2\xa1" else ">"
    offset, packets, tcp4, tcp6 = 24, 0, 0, 0
    while offset + 16 <= len(data):
        _, _, size, _ = struct.unpack(order + "IIII", data[offset : offset + 16])
        frame = data[offset + 16 : offset + 16 + size]
        offset += 16 + size
        if len(frame) < 14 or frame[12:14] not in (b"\x08\x00", b"\x86\xdd"):
            continue
        outer = frame[14:]
        proto, udp, pseudo = ip_payload(outer)
        if proto != 17 or len(udp) < 16 or int.from_bytes(udp[2:4], "big") != 4799:
            continue
        if outer[0] >> 4 == 6 or udp[6:8] != b"\0\0":
            assert (
                udp[6:8] != b"\0\0" and checksum(pseudo + udp) == 0
            ), "bad outer UDP checksum"
        inner = udp[16:]
        if len(inner) < 14 or inner[12:14] not in (b"\x08\x00", b"\x86\xdd"):
            continue
        inner_ip = inner[14:]
        protocol, payload, pseudo = ip_payload(inner_ip)
        if protocol == 6:
            assert checksum(pseudo + payload) == 0, "bad inner TCP checksum"
            if inner_ip[0] >> 4 == 4:
                tcp4 += 1
            else:
                tcp6 += 1
        packets += 1
    assert tcp4 > 100 and tcp6 > 100, (tcp4, tcp6)
    return dict(packets=packets, tcp4=tcp4, tcp6=tcp6)


try:
    report["imageId"] = run(
        "docker", "image", "inspect", IMAGE, "--format", "{{.Id}}"
    ).strip()
    config = OUT / (STAMP + "-agent.conf")
    config.write_text(
        json.dumps(
            {
                "agent": {"dataplane": "vpp", "insecure": True},
                "redis": {"enabled": True},
            }
        )
    )
    env = OUT / (STAMP + "-token.env")
    env.write_text("SDN_TOKEN=" + secrets.token_urlsafe(24) + "\n")
    os.chmod(env, 0o600)
    run(
        "docker",
        "network",
        "create",
        "--ipv6",
        "--subnet",
        f"fd45:{int(time.time()) % 65536:x}::/64",
        STAMP,
    )
    endpoints = []
    for i in (1, 2):
        name = STAMP + "-" + str(i)
        run(
            "docker",
            "run",
            "-d",
            "--name",
            name,
            "--network",
            STAMP,
            "--cap-add",
            "NET_ADMIN",
            "--cap-add",
            "NET_RAW",
            "--cap-add",
            "SYS_ADMIN",
            "--cap-add",
            "IPC_LOCK",
            "--ulimit",
            "memlock=-1:-1",
            "--device",
            "/dev/net/tun",
            "--device",
            "/dev/vhost-net",
            "--env-file",
            env,
            "-v",
            str(config) + ":/etc/sdn-agent/agent.conf:ro",
            IMAGE,
        )
        names.append(name)
        for _ in range(80):
            ready = run("docker", "exec", name, "vppctl", "show version", check=False)
            if "vpp v" in ready.lower() or "vpp v" in ready or "vpp 26" in ready:
                break
            time.sleep(1)
        else:
            raise RuntimeError(
                "VPP did not start: " + run("docker", "logs", name, check=False)
            )
        info = json.loads(run("docker", "inspect", name))[0]["NetworkSettings"][
            "Networks"
        ][STAMP]
        endpoint = {
            "4": info["IPAddress"],
            "6": info["GlobalIPv6Address"],
            "mac": info["MacAddress"],
        }
        endpoints.append(endpoint)
        cli(name, "create host-interface name eth0 hw-addr " + info["MacAddress"])
        cli(name, "set interface state host-eth0 up")
        de(name, "ip", "addr", "flush", "dev", "eth0")
        # VPP alone owns underlay IP/ND; prevent Linux from answering on eth0.
        de(name, "ip", "link", "set", "dev", "eth0", "addrgenmode", "none")
        cli(
            name,
            f'set interface ip address host-eth0 {endpoint["4"]}/{info["IPPrefixLen"]}',
        )
        cli(
            name,
            f'set interface ip address host-eth0 {endpoint["6"]}/{info["GlobalIPv6PrefixLen"]}',
        )
        run(
            "docker",
            "exec",
            "-d",
            name,
            "sh",
            "-c",
            "exec tcpdump -U -i eth0 -s 0 -w /tmp/underlay.pcap 'udp port 4799' >/tmp/capture.log 2>&1",
        )
    for outer in (4, 6):
        for i, name in enumerate(names, 1):
            peer = endpoints[2 - i][str(outer)]
            cli(name, f"create bridge-domain {outer}")
            tap = cli(
                name,
                f"create tap id {outer} host-if-name gso{outer} host-mtu-size 1400 gso",
            ).strip()
            assert tap.startswith("tap"), tap
            cli(name, f"set interface state {tap} up")
            cli(name, f"set interface l2 bridge {tap} {outer}")
            de(
                name,
                "ip",
                "link",
                "set",
                f"gso{outer}",
                "address",
                f"02:00:00:42:{outer:02x}:{i:02x}",
                "up",
            )
            de(
                name,
                "ip",
                "addr",
                "add",
                f"198.18.{outer}.{i}/24",
                "dev",
                f"gso{outer}",
            )
            de(
                name,
                "ip",
                "-6",
                "addr",
                "add",
                f"2001:db8:{outer}::{i}/64",
                "dev",
                f"gso{outer}",
            )
            vx = cli(
                name,
                f"create vxlan tunnel src {endpoints[i-1][str(outer)]} vni {100+outer} src_port 4799 dst_port 4799",
            ).strip()
            cli(name, f"set interface l2 bridge {vx} {outer} 1")
            cli(name, f"set vxlan remote {vx} remote {peer} bum")
            mac = f"02:00:00:42:{outer:02x}:{3-i:02x}"
            cli(name, f"set vxlan remote {vx} remote {peer} mac {mac}")
            cli(name, f"l2fib add {mac} {outer} {vx}")
        time.sleep(2)
        for inner, extension in ((4, False), (6, False), (6, True)):
            if extension:
                for name in names:
                    cli(name, "trace add tap-input 20")
            for sender, receiver in ((0, 1), (1, 0)):
                port = (
                    24000 + outer * 100 + inner * 10 + sender + (10 if extension else 0)
                )
                address = (
                    f"198.18.{outer}.{receiver+1}"
                    if inner == 4
                    else f"2001:db8:{outer}::{receiver+1}"
                )
                server = f"""import socket,hashlib
s=socket.socket(socket.AF_INET{'6' if inner==6 else ''},socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind(({address!r},{port}));s.listen();c,_=s.accept();h=hashlib.sha256()
while True:
 b=c.recv(65536)
 if not b:break
 h.update(b)
c.sendall(h.hexdigest().encode());c.close();s.close()
"""
                run("docker", "exec", "-d", names[receiver], "python3", "-c", server)
                time.sleep(0.5)
                client = f"""import socket,hashlib
b=bytes(range(256))*8192
s=socket.socket(socket.AF_INET{'6' if inner==6 else ''},socket.SOCK_STREAM);s.settimeout(15)
if {extension!r}:s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_DSTOPTS,bytes([6,0,1,4,0,0,0,0]))
s.connect(({address!r},{port}));s.sendall(b);s.shutdown(socket.SHUT_WR)
r=b''
while len(r)<64:
 x=s.recv(64)
 if not x:break
 r+=x
assert r.decode()==hashlib.sha256(b).hexdigest(),r
"""
                de(names[sender], "python3", "-c", client)
            passed(
                f"outer IPv{outer} inner TCP IPv{inner}{' with destination options' if extension else ''} bidirectional 2 MiB hashes match"
            )
    report["pcap"] = []
    for i, name in enumerate(names):
        errors = de(name, "vppctl", "show errors")
        assert "TCP GSO packets segmented" in errors, errors
        (OUT / f"leaf{i}-errors.txt").write_text(errors)
        de(name, "pkill", "-INT", "tcpdump")
        time.sleep(1)
        pcap = OUT / f"leaf{i}-underlay.pcap"
        run("docker", "cp", name + ":/tmp/underlay.pcap", pcap)
        report["pcap"].append(verify_pcap(pcap))
    passed(
        "GSO actually segmented on both VPP nodes; captured IPv4/IPv6 outer and inner TCP checksums valid"
    )
    # Raw underlay injection tests receive validation after captures were saved.
    # A valid frame must decapsulate; bad checksums must not reach the BD.
    for bypass in (False, True):
        for family in (4, 6):
            probe_vni = 900 + family + 100 * int(bypass)
            probe = cli(
                names[1],
                f"create vxlan tunnel src {endpoints[1][str(family)]} vni {probe_vni} src_port 4798 dst_port 4798",
            ).strip()
            if bypass:
                cli(
                    names[1],
                    f"set interface {'ip' if family == 4 else 'ip6'} vxlan-bypass host-eth0",
                )

            def counters():
                value = de(names[1], "vppctl", "show interface " + probe)
                match = re.search(r"rx packets\s+(\d+)", value)
                return int(match[1]) if match else 0

            before = counters()
            code = f"""import socket,struct,ipaddress,time
def csum(b):
 if len(b)%2:b+=b'\\0'
 s=sum(struct.unpack('!%dH'%(len(b)//2),b))
 while s>>16:s=(s&65535)+(s>>16)
 return (~s)&65535
family={family}
src=ipaddress.ip_address({endpoints[0][str(family)]!r}).packed
dst=ipaddress.ip_address({endpoints[1][str(family)]!r}).packed
eth=bytes.fromhex({endpoints[1]["mac"].replace(":", "")!r}+{endpoints[0]["mac"].replace(":", "")!r})+struct.pack('!H',0x800 if family==4 else 0x86dd)
inner=bytes.fromhex('02000042{family:02x}0202000042{family:02x}0188b5')+b'checksum-test'*4
payload=bytes([8,0,0,0])+struct.pack('!I',{probe_vni}<<8)+inner
udp=struct.pack('!HHHH',53001,4798,8+len(payload),0)+payload
pseudo=src+dst+(struct.pack('!BBH',0,17,len(udp)) if family==4 else struct.pack('!I3xB',len(udp),17))
good=csum(pseudo+udp) or 65535
s=socket.socket(socket.AF_PACKET,socket.SOCK_RAW);s.bind(('eth0',0))
for checksum in (good, 0, good^65535 or 1):
 u=udp[:6]+struct.pack('!H',checksum)+udp[8:]
 if family==4:
  ip=struct.pack('!BBHHHBBH4s4s',0x45,0,20+len(u),0,0,64,17,0,src,dst)
  ip=ip[:10]+struct.pack('!H',csum(ip))+ip[12:]
 else:ip=struct.pack('!IHBB16s16s',6<<28,len(u),17,64,src,dst)
 s.send(eth+ip+u);time.sleep(.1)
"""
            de(names[0], "python3", "-c", code)
            time.sleep(0.5)
            accepted = counters() - before
            assert accepted == (2 if family == 4 else 1), (family, bypass, accepted)
            passed(
                f"outer IPv{family} valid/zero/invalid UDP checksums distinguished (bypass={bypass})"
            )
    report["status"] = "passed"
except Exception as exc:
    report["status"], report["error"] = "failed", str(exc)
    raise
finally:
    for name in names:
        if report["status"] != "passed":
            run("docker", "exec", name, "pkill", "-INT", "tcpdump", check=False)
            time.sleep(0.2)
            run(
                "docker",
                "cp",
                name + ":/tmp/underlay.pcap",
                OUT / (name + "-failure.pcap"),
                check=False,
            )
        run("docker", "logs", "--tail", "60", name, check=False)
        for command in (
            "show errors",
            "show trace",
            "show interface address",
            "show ip6 neighbors",
            "show hardware-interfaces",
        ):
            run("docker", "exec", name, "vppctl", command, check=False)
        run("docker", "rm", "-fv", name, check=False)
    run("docker", "network", "rm", STAMP, check=False)
    if "env" in globals():
        env.unlink(missing_ok=True)
    (OUT / "result.json").write_text(json.dumps(report, indent=2))
