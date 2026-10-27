"""Validate TAP TCP GSO and packet checksums after physical VF reception."""

import struct
import time


def checksum(data):
    if len(data) % 2:
        data += b"\0"
    value = sum(struct.unpack("!%dH" % (len(data) // 2), data))
    while value >> 16:
        value = (value & 65535) + (value >> 16)
    return (~value) & 65535


def ip_payload(packet):
    if packet[0] >> 4 == 4:
        length, hlen, proto = (
            int.from_bytes(packet[2:4], "big"),
            (packet[0] & 15) * 4,
            packet[9],
        )
        assert checksum(packet[:hlen]) == 0, "IPv4 header checksum"
        body = packet[hlen:length]
        pseudo = packet[12:20] + bytes((0, proto)) + struct.pack("!H", len(body))
    else:
        assert packet[0] >> 4 == 6
        proto, body = packet[6], packet[40 : 40 + int.from_bytes(packet[4:6], "big")]
        while proto in (0, 43, 60):
            length = (body[1] + 1) * 8
            proto, body = body[0], body[length:]
        pseudo = packet[8:40] + struct.pack("!I3xB", len(body), proto)
    return proto, body, pseudo


def ethernet_payload(frame):
    ether_type, offset = int.from_bytes(frame[12:14], "big"), 14
    while ether_type in (0x8100, 0x88A8):
        ether_type, offset = (
            int.from_bytes(frame[offset + 2 : offset + 4], "big"),
            offset + 4,
        )
    return (ether_type, frame[offset:])


def verify(path):
    data = path.read_bytes()
    assert data[:4] in (
        b"\xd4\xc3\xb2\xa1",
        b"\xa1\xb2\xc3\xd4",
    ), "expected standard pcap"
    order = "<" if data[:4] == b"\xd4\xc3\xb2\xa1" else ">"
    offset, counts = 24, {"tcp4": 0, "tcp6": 0, "vxlan": 0}
    while offset + 16 <= len(data):
        _, _, length, _ = struct.unpack(order + "IIII", data[offset : offset + 16])
        frame = data[offset + 16 : offset + 16 + length]
        offset += 16 + length
        ethertype, outer = ethernet_payload(frame)
        if ethertype not in (0x800, 0x86DD):
            continue
        proto, udp, pseudo = ip_payload(outer)
        if proto != 17 or len(udp) < 16 or int.from_bytes(udp[2:4], "big") != 4789:
            continue
        if outer[0] >> 4 == 6 or udp[6:8] != b"\0\0":
            assert (
                udp[6:8] != b"\0\0" and checksum(pseudo + udp) == 0
            ), "VXLAN UDP checksum"
        counts["vxlan"] += 1
        ethertype, inner = ethernet_payload(udp[16:])
        if ethertype not in (0x800, 0x86DD):
            continue
        proto, payload, pseudo = ip_payload(inner)
        if proto == 6:
            assert checksum(pseudo + payload) == 0, "inner TCP checksum"
            counts["tcp" + str(inner[0] >> 4)] += 1
    assert counts["tcp4"] > 100 and counts["tcp6"] > 100, counts
    return counts


def acceptance(lab):
    spine = lab.nodes["spine"]
    lab.cli(
        spine,
        "pcap trace rx max 20000 max-bytes-per-pkt 9000 intfc vf0 file sriov-underlay.pcap",
    )
    try:
        for family in (4, 6):
            for sender, receiver in ((1, 2), (2, 1)):
                address = (
                    f"192.168.{receiver*10}.{10+receiver}"
                    if family == 4
                    else f"2001:db8:{receiver*10}::{10+receiver}"
                )
                port = 24000 + family * 10 + sender
                server = f"""import socket,hashlib
s=socket.socket(socket.AF_INET{'6' if family==6 else ''},socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.settimeout(25)
s.bind(({address!r},{port}));s.listen();c,_=s.accept();c.settimeout(25);h=hashlib.sha256()
while True:
 b=c.recv(65536)
 if not b:break
 h.update(b)
c.sendall(h.hexdigest().encode());c.close();s.close()
"""
                lab.run(
                    "docker",
                    "exec",
                    "-d",
                    lab.pcs[f"leaf{receiver}"],
                    "python3",
                    "-c",
                    server,
                )
                time.sleep(0.4)
                client = f"""import socket,hashlib
b=bytes(range(256))*8192
s=socket.socket(socket.AF_INET{'6' if family==6 else ''},socket.SOCK_STREAM);s.settimeout(20)
s.connect(({address!r},{port}));s.sendall(b);s.shutdown(socket.SHUT_WR)
r=b''
while len(r)<64:
 x=s.recv(64)
 if not x:break
 r+=x
assert r.decode()==hashlib.sha256(b).hexdigest(),r
"""
                lab.de(lab.pcs[f"leaf{sender}"], "python3", "-c", client)
            lab.report["checks"].append(
                f"physical VF inner TCP IPv{family} bidirectional 2 MiB SHA256"
            )
    finally:
        lab.cli(spine, "pcap trace off")
        lab.run(
            "docker",
            "cp",
            spine + ":/tmp/sriov-underlay.pcap",
            lab.out / "spine-vf-rx.pcap",
        )
    lab.report["wireChecksums"] = verify(lab.out / "spine-vf-rx.pcap")
    for leaf in ("leaf1", "leaf2"):
        errors = lab.cli(lab.nodes[leaf], "show errors")
        assert "TCP GSO packets segmented" in errors, errors
    lab.report["checks"].append(
        "TAP GSO segmentation and VF RX outer/inner wire checksums verified"
    )
