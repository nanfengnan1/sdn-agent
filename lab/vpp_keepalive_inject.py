"""Negative test, disposable PC namespace only: valid NA from a wrong MAC."""

import socket
import sys
import time

frame = bytearray.fromhex(sys.argv[1])
assert frame[12:14] == b"\x86\xdd" and frame[54] == 136
assert int.from_bytes(frame[18:20], "big") == 24  # No TLLA option.
frame[6:12] = bytes.fromhex("020000ffffff")  # Ethernet source mismatch.
sock = socket.socket(socket.AF_PACKET, socket.SOCK_RAW)
sock.bind(("eth0", 0))
for _ in range(60):
    sock.send(frame)
    time.sleep(1)
