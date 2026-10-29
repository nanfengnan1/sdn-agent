#!/usr/bin/env python3
"""Node-local Snort3 runtime: Redis -> confd -> validated Lua -> acknowledged reload.

One Unix-socket server serializes changes. Redis stores whole versioned snapshots;
the Agent's durable Undo owns transaction recovery. No user-provided shell/Lua.
"""
import argparse
import copy
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import signal
import socket
import socketserver
import subprocess
import sys
import time

ALL = 0xffffffff
KEY = "/sdn/ips/v1/"
CONTROL = "/run/sdn-agent/snort-control.sock"
RECEIPT = Path("/run/sdn-agent/snort-reload.json")
BOOTSTRAP = Path("/run/sdn-agent/snort-bootstrap")
INSTANCE = "sdn-ips"
stopping = False


def canonical(value):
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":"))


def digest(value):
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def atomic(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    tmp = path.with_suffix(".tmp")
    with tmp.open("w") as stream:
        os.chmod(tmp, 0o600)
        stream.write(value)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(tmp, path)
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def run(args, timeout=40):
    result = subprocess.run(args, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        output = result.stdout + result.stderr
        if args[0] == "snort":
            errors = [line for line in output.splitlines() if line.startswith(("ERROR", "FATAL"))]
            output = "\n".join(errors) or output
        raise RuntimeError(output[-6000:])
    return result.stdout


def read_base(path):
    # A single immutable byte snapshot is used for validation and reload.
    with Path(path).open("rb") as stream:
        data = stream.read((1 << 20) + 1)
    if not data or len(data) > 1 << 20 or b"\x00" in data:
        raise ValueError("Snort base config must be nonempty UTF-8, at most 1 MiB, without NUL")
    return data.decode("utf-8")


def base_hash(text):
    return hashlib.sha256(text.encode()).hexdigest()


def lua_string(value):
    # Encode UTF-8 bytes, not JSON escapes (Lua does not understand JSON \uXXXX).
    return '"' + ''.join(chr(x) if 32 <= x < 127 and x not in (34, 92)
                          else "\\%03d" % x for x in value.encode()) + '"'


def validate_rule(sid, text):
    if type(sid) is not int or not 1 <= sid <= ALL:
        raise ValueError("SID must be 1..4294967295")
    if not isinstance(text, str) or not 0 < len(text.encode()) <= 16384:
        raise ValueError("rule must be 1..16384 bytes")
    if any(ord(c) < 32 or ord(c) == 127 for c in text):
        raise ValueError("one rule per line; control characters are forbidden")
    match = re.fullmatch(r"(alert|drop|reject|pass|block)\s+(tcp|udp|icmp|ip)\s+.+?\((.*)\)\s*", text)
    if not match:
        raise ValueError("expected a complete Snort network rule")
    options = []
    current = ""
    quoted = escaped = False
    for ch in match[3]:
        if escaped:
            escaped = False
            current += " " if quoted else ch
        elif ch == "\\":
            escaped = True
            current += " " if quoted else ch
        elif ch == '"':
            quoted = not quoted
            current += " "
        elif ch == ";" and not quoted:
            options.append(current.strip())
            current = ""
        else:
            current += " " if quoted else ch
    if quoted or escaped or current.strip():
        raise ValueError("invalid rule options or missing trailing semicolon")
    sids = []
    for option in options:
        key, _, value = option.partition(":")
        key = key.strip().lower()
        if key in ("lua", "luajit", "so", "soid", "include"):
            raise ValueError("scripts, includes and shared-object rules are not supported")
        if key == "sid":
            if not re.fullmatch(r"\s*\d+\s*", value):
                raise ValueError("invalid rule SID")
            sids.append(int(value))
        if key == "gid" and value.strip() != "1":
            raise ValueError("only text-rule GID 1 is supported")
    if sids != [sid]:
        raise ValueError("rule must contain exactly one matching SID")
    return text


def normalize(value):
    if not isinstance(value, dict) or set(value) != {"rules", "interfaces"}:
        raise ValueError("invalid IPS snapshot")
    if not isinstance(value["rules"], dict) or not isinstance(value["interfaces"], dict):
        raise ValueError("invalid IPS collections")
    if len(value["rules"]) > 2048 or len(value["interfaces"]) > 256:
        raise ValueError("IPS snapshot exceeds resource limits")
    for sid, rule in value["rules"].items():
        if str(int(sid)) != sid:
            raise ValueError("non-canonical SID")
        validate_rule(int(sid), rule)
    for name, direction in value["interfaces"].items():
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}", name):
            raise ValueError("invalid VPP interface name")
        if direction not in ("input", "output", "inout"):
            raise ValueError("invalid IPS direction")
    return copy.deepcopy(value)


def updated(before, op):
    after = normalize(before)
    code, action = op.get("code"), op.get("action")
    if action not in ("CREATE", "SET", "DELETE"):
        raise ValueError("unsupported IPS action")
    if code == "IPS_RULE":
        obj = op.get("ipsRule", {})
        sid = obj.get("sid")
        if type(sid) is not int or not 1 <= sid <= ALL:
            raise ValueError("nonzero SID required")
        key, items = str(sid), after["rules"]
        value = obj.get("rule")
        if action != "DELETE":
            validate_rule(sid, value)
    elif code == "IPS_INTERFACE":
        obj = op.get("ipsInterface", {})
        key, items = obj.get("name"), after["interfaces"]
        value = obj.get("direction")
        if not isinstance(key, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}", key):
            raise ValueError("invalid VPP interface name")
        if action != "DELETE" and value not in ("input", "output", "inout"):
            raise ValueError("invalid IPS direction")
    else:
        raise ValueError("unsupported IPS operation")
    if action == "DELETE":
        items.pop(key, None)
    else:
        if action == "CREATE" and key in items and items[key] != value:
            raise ValueError("resource exists with different configuration; use SET")
        if action == "SET" and key not in items:
            raise ValueError("resource does not exist; use CREATE")
        items[key] = value
    return normalize(after)


def control(command, marker, timeout=35):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(timeout)
        client.connect(CONTROL)
        client.sendall((command + "\n").encode())
        data = b""
        deadline = time.monotonic() + timeout
        while marker.encode() not in data or b"\n" not in data[data.find(marker.encode()):]:
            if time.monotonic() >= deadline:
                raise RuntimeError("Snort control acknowledgement timed out")
            client.settimeout(max(0.1, deadline - time.monotonic()))
            part = client.recv(65536)
            if not part:
                raise RuntimeError("Snort control connection closed before acknowledgement")
            data += part
            if len(data) > 1 << 20:
                raise RuntimeError("Snort control output exceeded limit")
            if b"reload failed" in data or b"reload pending" in data:
                raise RuntimeError(data.decode(errors="replace")[-4000:])
        return data.decode(errors="replace")


def generation():
    output = control("show_config_generation()", "configuration id:", 5)
    # The numeric response may arrive in a later read; ask control for newline.
    match = re.search(r"configuration id:\s*(\d+)", output)
    if not match:
        raise RuntimeError("invalid Snort configuration generation")
    return int(match[1])


def check_config(path):
    run(["snort", "-T", "-Q", "-c", str(path), "--daq", "vpp", "--daq-dir", "/usr/lib/daq"], 35)


def reload_config(path):
    if BOOTSTRAP.exists():
        return
    before = generation()
    control("reload_config(" + lua_string(str(path)) + ")", "== reload complete", 40)
    after = generation()
    if after <= before:
        raise RuntimeError("Snort did not advance its configuration generation")
    atomic(RECEIPT, canonical({"generation": after, "sha256": hashlib.sha256(Path(path).read_bytes()).hexdigest()}))


class VPP:
    def __init__(self, path):
        from vpp_papi import VPPApiClient
        self.client = VPPApiClient(server_address=path, apidir="/usr/share/vpp/api", read_timeout=5)
        self.client.connect("sdn-agent-snort")
        reply = self.call("snort_instance_create_v2", queue_size=1024, qpairs_per_thread=1,
                          drop_bitmap=(1 << 1) | (1 << 4), drop_on_disconnect=1, name=INSTANCE)
        self.index = reply.instance_index

    def call(self, method, **kwargs):
        value = getattr(self.client.api, method)(**kwargs)
        reply = value[0] if isinstance(value, tuple) and not hasattr(value, "_fields") else value
        if getattr(reply, "retval", 0):
            raise RuntimeError(f"{method}: VPP retval {reply.retval}")
        return value

    def rows(self, name, **kwargs):
        rows, cursor = [], 0
        while cursor != ALL:
            reply, part = self.call(name, cursor=cursor, **kwargs)
            rows.extend(part)
            if not part:
                break
            if reply.cursor == cursor:
                raise RuntimeError("non-advancing VPP cursor")
            cursor = reply.cursor
        return rows

    def interfaces(self):
        return {x.interface_name: x.sw_if_index for x in self.call("sw_interface_dump")}

    def attached(self):
        names = {v: k for k, v in self.interfaces().items()}
        result = {}
        for row in self.rows("snort_interface_v2_get", sw_if_index=ALL):
            inside = row.in_instance_index == self.index
            outside = row.out_instance_index == self.index
            if inside or outside:
                result[names[row.sw_if_index]] = "inout" if inside and outside else "input" if inside else "output"
        return result

    def connected(self):
        instances = self.rows("snort_instance_v2_get", instance_index=self.index)
        if len(instances) != 1 or instances[0].snort_client_index == ALL:
            raise RuntimeError("Snort DAQ is not connected to the VPP instance")
        clients = self.rows("snort_client_v2_get", snort_client_index=ALL)
        if not any(row.snort_client_index == instances[0].snort_client_index
                   and row.mode == 2 and row.n_instances > 0 for row in clients):
            raise RuntimeError("VPP has no inline Snort DAQ worker")

    def binding_call(self, method, index, direction):
        # Native VPP requires an administratively down interface for changes.
        rows = self.call("sw_interface_dump", sw_if_index=index)
        flags = next(x.flags for x in rows if x.sw_if_index == index)
        up = int(flags) & 1
        if up:
            self.call("sw_interface_set_flags", sw_if_index=index, flags=0)
        try:
            self.call(method, instance_index=self.index, sw_if_index=index,
                      snort_dir={"input": 1, "output": 2, "inout": 3}[direction])
        finally:
            if up:
                self.call("sw_interface_set_flags", sw_if_index=index, flags=1)

    def check_target(self, target):
        indexes = self.interfaces()
        for name in target:
            if name not in indexes:
                raise ValueError(f"VPP interface {name} does not exist")
        # This VPP plugin uses IP feature arcs. Plain L2 switching bypasses them.
        for bd in self.call("bridge_domain_dump"):
            for port in bd.sw_if_details:
                if port.sw_if_index != bd.bvi_sw_if_index and port.sw_if_index in [indexes[n] for n in target]:
                    raise ValueError("IPS requires a routed interface/BVI; plain BD member ports bypass IP inspection")

    def reconcile(self, target, allow_missing=False):
        indexes = self.interfaces()
        current = self.attached()
        for name, direction in current.items():
            if target.get(name) != direction:
                self.binding_call("snort_interface_detach_v2", indexes[name], direction)
        for name, direction in target.items():
            if name not in indexes:
                if allow_missing:
                    continue
                raise ValueError(f"VPP interface {name} does not exist")
            if current.get(name) != direction:
                # Refuse takeover of another instance's interface.
                rows = [row for row in self.rows("snort_interface_v2_get", sw_if_index=ALL) if row.sw_if_index == indexes[name]]
                for row in rows:
                    if any(x not in (self.index, 65535) for x in (row.in_instance_index, row.out_instance_index)):
                        raise ValueError(f"VPP interface {name} belongs to another IPS instance")
                self.binding_call("snort_interface_attach", indexes[name], direction)


class Runtime:
    def __init__(self, config):
        import redis
        self.config = config
        self.root = Path(config["agent"]["stateDir"]) / "ips"
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.lua = self.root / "snort.lua"
        self.base_path = Path(config["ips"]["configFile"])
        if self.base_path.resolve() == self.lua.resolve():
            raise ValueError("base config must differ from generated snort.lua")
        self.base = read_base(self.base_path)
        self.base_error = ""
        self.base_candidate = self.base_attempted = base_hash(self.base)
        self.redis = redis.Redis(host=config["redis"]["bind"], port=config["redis"]["port"],
                                 socket_connect_timeout=3, socket_timeout=3, decode_responses=True)
        self.redis.ping()
        self.state = normalize(json.loads(self.redis.get(KEY + "desired") or '{"rules":{},"interfaces":{}}'))
        self.error = ""
        self.snort = None
        self.vpp = None
        self.confdir = Path("/run/sdn-agent/confd")
        (self.confdir / "conf.d").mkdir(parents=True, exist_ok=True, mode=0o700)
        (self.confdir / "templates").mkdir(parents=True, exist_ok=True, mode=0o700)
        source = Path(config["ips"]["templateDir"])
        (self.confdir / "templates/snort.lua.tmpl").write_text((source / "snort.lua.tmpl").read_text())
        # JSON string syntax is also valid for TOML basic string values.
        template = (source / "snort.toml").read_text()
        import shlex
        args = "python3 /usr/local/lib/sdn-agent/snort/runtime.py"
        template = template.replace("@DEST@", json.dumps(str(self.lua)))
        template = template.replace("@CHECK@", json.dumps(args + " check '{{.src}}'"))
        template = template.replace("@RELOAD@", json.dumps(args + " reload " + shlex.quote(str(self.lua))))
        (self.confdir / "conf.d/snort.toml").write_text(template)

    def render(self, state):
        rules = "\n".join(state["rules"][sid] for sid in sorted(state["rules"], key=int)) or "# No managed IPS rules\n"
        # Only this trusted code produces Lua. Uploaded rule text is an escaped string.
        return ("-- SDN IPS revision " + digest(state) + "\n"
                "-- Base config SHA256 " + base_hash(self.base) + "\n"
                "assert(loadstring(" + lua_string(self.base) + ", "
                + lua_string("@" + str(self.base_path)) + "))()\n"
                "ips = ips or {}\n"
                "ips.mode = 'inline'\n"
                "ips.variables = ips.variables or default_variables\n"
                "assert(ips.rules == nil or ips.rules == '', 'ips.rules is managed by Agent')\n"
                "assert(ips.include == nil or ips.include == '', 'ips.include is managed by Agent')\n"
                "ips.rules = " + lua_string(rules) + "\n")

    def confd(self, state):
        self.redis.set(KEY + "render", self.render(state))
        return run(["confd", "-onetime", "-backend", "redis",
                    "-node", f'{self.config["redis"]["bind"]}:{self.config["redis"]["port"]}',
                    "-confdir", str(self.confdir)], 65)

    def start(self):
        BOOTSTRAP.touch(mode=0o600)
        try:
            self.confd(self.state)
        finally:
            BOOTSTRAP.unlink(missing_ok=True)
        self.vpp = VPP(self.config["vpp"]["apiSocket"])
        Path(CONTROL).unlink(missing_ok=True)
        logdir = self.root / "logs"
        logdir.mkdir(exist_ok=True, mode=0o700)
        self.snort = subprocess.Popen(["snort", "-c", str(self.lua), "-Q", "--daq", "vpp",
                                     "--daq-dir", "/usr/lib/daq", "-i", INSTANCE,
                                     "--daq-var", "socket=/run/vpp/snort.sock",
                                     "--control-socket", CONTROL, "-l", str(logdir)])
        deadline = time.monotonic() + 50
        while True:
            if self.snort.poll() is not None:
                raise RuntimeError("Snort exited during startup")
            try:
                gen = generation()
                break
            except (OSError, RuntimeError):
                if time.monotonic() >= deadline:
                    raise
                time.sleep(0.25)
        atomic(RECEIPT, canonical({"generation": gen, "sha256": hashlib.sha256(self.lua.read_bytes()).hexdigest()}))
        # Network resources may be restored asynchronously by the Agent. Status
        # reports missing bindings; the loop attaches them once interfaces exist.
        self.vpp.reconcile(self.state["interfaces"], allow_missing=True)

    def loaded(self):
        if not self.snort or self.snort.poll() is not None:
            raise RuntimeError("Snort is not running")
        self.vpp.connected()
        receipt = json.loads(RECEIPT.read_text())
        if receipt["generation"] != generation() or receipt["sha256"] != hashlib.sha256(self.lua.read_bytes()).hexdigest():
            raise RuntimeError("Snort configuration changed outside Agent control")
        if self.lua.read_text() != self.render(self.state):
            raise RuntimeError("Snort configuration does not match accepted rules")

    def ensure_reload(self):
        # confd can skip identical files after an interrupted reload.
        receipt = json.loads(RECEIPT.read_text())
        if receipt["sha256"] != hashlib.sha256(self.lua.read_bytes()).hexdigest() or receipt["generation"] != generation():
            reload_config(self.lua)

    def base_pending(self):
        try:
            return read_base(self.base_path) != self.base
        except (OSError, ValueError):
            return True

    def reload_base(self, candidate=None):
        before = self.base
        try:
            candidate = read_base(self.base_path) if candidate is None else candidate
            self.loaded()
            self.base_attempted = base_hash(candidate)
            if candidate != before:
                self.base = candidate
                try:
                    self.confd(self.state)
                    self.ensure_reload()
                    self.loaded()
                except Exception as exc:
                    self.base = before
                    try:
                        self.confd(self.state)
                        self.ensure_reload()
                        self.loaded()
                    except Exception as recovery:
                        raise RuntimeError(f"base reload failed: {exc}; recovery failed: {recovery}") from exc
                    raise
            self.base_error = ""
            return {"reloaded": candidate != before, "baseConfigSHA256": base_hash(self.base),
                    "generation": generation()}
        except Exception as exc:
            self.base_error = str(exc)
            raise

    def poll_base(self):
        if not self.config["ips"].get("autoReload", False):
            return
        try:
            candidate = read_base(self.base_path)
            fingerprint = base_hash(candidate)
            # Two matching polls avoid loading an editor's intermediate write.
            if fingerprint != self.base_candidate:
                self.base_candidate = fingerprint
                return
            if candidate == self.base:
                self.base_error = ""
                self.base_attempted = fingerprint
            elif fingerprint != self.base_attempted:
                self.reload_base(candidate)
        except Exception as exc:
            self.base_error = str(exc)

    def status(self):
        self.redis.ping()
        self.loaded()
        attached = self.vpp.attached()
        return {"state": "DEGRADED" if attached != self.state["interfaces"] or self.error or self.base_error else "RUNNING",
                "revision": digest(self.state), "generation": generation(), "pid": self.snort.pid,
                "ruleCount": len(self.state["rules"]), "desiredInterfaces": self.state["interfaces"],
                "attachedInterfaces": attached, "lastError": self.error,
                "baseConfigPath": str(self.base_path), "baseConfigSHA256": base_hash(self.base),
                "baseConfigPending": self.base_pending(), "baseConfigError": self.base_error,
                "autoReload": self.config["ips"].get("autoReload", False),
                "configurationPath": str(self.lua), "logDirectory": str(self.root / "logs"),
                "backend": "redis+confd", "daq": "vpp", "disconnectPolicy": "drop"}

    def apply_state(self, after, recovery=False):
        after = normalize(after)
        before = copy.deepcopy(self.state)
        try:
            if not recovery:
                self.loaded()
            indexes = self.vpp.interfaces()
            if set(after["interfaces"]) - set(indexes):
                raise ValueError("create the VPP interfaces before attaching IPS")
            self.vpp.check_target(after["interfaces"])
            self.confd(after)
            self.ensure_reload()
            self.vpp.reconcile(after["interfaces"])
            self.redis.set(KEY + "desired", canonical(after))
            self.state = after
            self.loaded()
            if self.vpp.attached() != after["interfaces"]:
                raise RuntimeError("VPP IPS attachment readback mismatch")
            self.error = ""
        except Exception as exc:
            self.error = str(exc)
            # Always restore render + accepted state; never mistake failed reload
            # for success merely because the file already has the desired bytes.
            try:
                self.confd(before)
                self.ensure_reload()
                self.vpp.reconcile(before["interfaces"], allow_missing=True)
                self.redis.set(KEY + "desired", canonical(before))
                self.state = before
                self.loaded()
            except Exception as recovery:
                raise RuntimeError(f"IPS apply failed: {exc}; recovery failed: {recovery}") from exc
            raise

    def dispatch(self, method, data):
        if method == "status":
            return self.status()
        if method == "rules":
            self.loaded()
            return [{"sid": int(sid), "rule": rule, "revision": digest(self.state), "loaded": True}
                    for sid, rule in sorted(self.state["rules"].items(), key=lambda x: int(x[0]))]
        if method == "reload-base":
            return self.reload_base()
        if method == "prepare":
            self.loaded()
            updated(self.state, data)
            return self.state
        if method == "verify":
            self.loaded()
            if updated(self.state, data) != self.state or self.vpp.attached() != self.state["interfaces"]:
                raise RuntimeError("IPS desired rule/interface does not match running configuration")
            return {"verified": True}
        if method == "restore":
            self.apply_state(data, recovery=True)
            return {"restored": True}
        if method == "apply":
            self.apply_state(updated(self.state, data))
            return {"applied": True}
        raise ValueError("unknown IPS method")

    def stop(self):
        if self.snort and self.snort.poll() is None:
            self.snort.terminate()
            try:
                self.snort.wait(15)
            except subprocess.TimeoutExpired:
                self.snort.kill()
                self.snort.wait(5)
        if self.vpp:
            self.vpp.client.disconnect()


class Server(socketserver.UnixStreamServer):
    allow_reuse_address = True


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", 0))
            if not 0 < length <= 4 << 20:
                raise ValueError("invalid request size")
            data = json.loads(self.rfile.read(length))
            result = self.server.runtime.dispatch(self.path.lstrip("/"), data)
            code = 200
        except Exception as exc:
            result, code = {"error": str(exc)}, 400
        body = canonical(result).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except BrokenPipeError:
            pass

    def log_message(self, *_):
        pass


def reload_base_cli():
    import http.client
    parser = argparse.ArgumentParser(description="Validate and reload the mounted Snort base configuration")
    parser.add_argument("--socket", default="/run/sdn-agent/ips.sock")
    args = parser.parse_args(sys.argv[2:])
    connection = http.client.HTTPConnection("localhost", timeout=90)
    connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    connection.sock.settimeout(90)
    try:
        connection.sock.connect(args.socket)
        connection.request("POST", "/reload-base", body="null")
        response = connection.getresponse()
        body = response.read().decode()
        if response.status != 200:
            raise RuntimeError(body)
        print(body)
    finally:
        connection.close()


def main():
    if len(sys.argv) >= 2 and sys.argv[1] == "reload-base":
        reload_base_cli()
        return
    if len(sys.argv) == 3 and sys.argv[1] in ("check", "reload"):
        (check_config if sys.argv[1] == "check" else reload_config)(sys.argv[2])
        return
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    args = parser.parse_args()
    config = json.loads(Path(args.config).read_text())
    runtime = Runtime(config)
    def stop(*_):
        global stopping
        stopping = True
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    path = Path(config["ips"]["socket"])
    path.unlink(missing_ok=True)
    try:
        runtime.start()
        with Server(str(path), Handler) as server:
            os.chmod(path, 0o600)
            server.timeout = 0.5
            server.runtime = runtime
            next_reconcile = time.monotonic()
            while not stopping:
                if runtime.snort.poll() is not None:
                    raise RuntimeError("required Snort process exited")
                server.handle_request()
                if time.monotonic() >= next_reconcile:
                    try:
                        runtime.vpp.reconcile(runtime.state["interfaces"], allow_missing=True)
                    except Exception as exc:
                        runtime.error = str(exc)
                    runtime.poll_base()
                    next_reconcile = time.monotonic() + 3
    finally:
        path.unlink(missing_ok=True)
        runtime.stop()


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print("IPS runtime failed: " + str(exc), file=sys.stderr, flush=True)
        sys.exit(1)
