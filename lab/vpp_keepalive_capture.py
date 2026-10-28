"""Run in a disposable PC namespace: passive raw-socket probe capture."""

import ipaddress
import json
import select
import socket
import time

sock = socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(3))
deadline = time.monotonic() + 42
records = []
while time.monotonic() < deadline:
    if not select.select([sock], [], [], max(0, min(1, deadline - time.monotonic())))[
        0
    ]:
        continue
    packet, address = sock.recvfrom(65535)
    if len(packet) < 42:
        continue
    ethertype = packet[12:14]
    kind = None
    if ethertype == b"\x08\x06" and packet[20:22] == b"\x00\x01":
        src = str(ipaddress.ip_address(packet[28:32]))
        dst = str(ipaddress.ip_address(packet[38:42]))
        target = dst
        kind = "ARP"
    elif (
        len(packet) >= 78
        and ethertype == b"\x86\xdd"
        and packet[20] == 58
        and packet[54] in (135, 136)
    ):
        src = str(ipaddress.ip_address(packet[22:38]))
        dst = str(ipaddress.ip_address(packet[38:54]))
        target = str(ipaddress.ip_address(packet[62:78]))
        kind = "NS" if packet[54] == 135 else "NA"
    if kind and target in ("192.168.20.12", "2001:db8:20::12"):
        records.append(
            {
                "interface": address[0],
                "kind": kind,
                "src": src,
                "dst": dst,
                "target": target,
                "dstMac": ":".join(f"{x:02x}" for x in packet[:6]),
                "unicast": not bool(packet[0] & 1),
                "payloadLength": (
                    int.from_bytes(packet[18:20], "big") if kind == "NA" else None
                ),
                "hex": packet.hex() if kind == "NA" else "",
            }
        )
with open("/tmp/keepalive-wire.json", "w") as stream:
    json.dump(records, stream, indent=2)
