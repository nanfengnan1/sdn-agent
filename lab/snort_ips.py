#!/usr/bin/env python3
"""Disposable Redis/confd/Snort/VPP + Controller/Web acceptance.

Runs only uniquely named containers. All TAPs and PC network namespaces are
inside the Agent container. No physical devices or existing fabric are touched.
"""
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import traceback

from vpp_stack import Stack

ROOT = Path(__file__).resolve().parents[1]
STAMP = "sdn-ips-" + str(int(time.time())) + "-" + secrets.token_hex(3)
OUT = Path(os.environ.get("SDN_IPS_OUTPUT", ROOT / "artifacts/ips" / STAMP)).resolve()
OUT.mkdir(parents=True, exist_ok=True)
TOKEN = secrets.token_urlsafe(24)
EXTERNAL = os.environ.get("SDN_IPS_EXTERNAL") == "1"
IMAGE = os.environ.get("SDN_IPS_IMAGE", "alpine/sdn-agent:ips-test")
os.environ.setdefault("SDN_CONTROLLER_IMAGE", "alpine/sdn-controller:ips-test")
os.environ.setdefault("SDN_WEB_IMAGE", "alpine/sdn-web:ips-test")
name, network = STAMP + "-agent", STAMP + "-mgmt"
log = (OUT / "commands.log").open("w", buffering=1)
report = {"image": IMAGE, "checks": {}, "status": "running"}
stack = None
created = False


def run(*args, check=True, timeout=120):
    proc = subprocess.run([str(x) for x in args], text=True, capture_output=True, timeout=timeout)
    redacted = " ".join(str(x) for x in args).replace(TOKEN, "<token>")
    log.write("$ " + redacted + "\n" + proc.stdout.replace(TOKEN, "<token>") + proc.stderr.replace(TOKEN, "<token>") + "\n")
    if check and proc.returncode:
        raise RuntimeError(proc.stdout + proc.stderr)
    return proc


def de(*args, **kwargs):
    return run("docker", "exec", name, *args, **kwargs)


def cli(command):
    text = de("vppctl", "-s", "/run/vpp/cli.sock", command).stdout
    if any(x in text.lower() for x in ("unknown input", "failed", "error:")):
        raise RuntimeError(command + ": " + text)
    return text


def wait(fn, timeout=90):
    deadline, error = time.monotonic() + timeout, None
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except Exception as exc:
            error = exc
        time.sleep(1)
    raise RuntimeError("wait failed: " + str(error))


def check(label, value):
    if not value:
        raise AssertionError(label)
    report["checks"][label] = True
    print("PASS " + label, flush=True)


def topology():
    for number in (1, 2):
        ns = "ips-pc" + str(number)
        de("ip", "netns", "del", ns, check=False)
        de("ip", "netns", "add", ns)
        cli(f"create tap id {number} host-if-name ips-tap{number}")
        cli(f"set interface ip address tap{number} 10.{number}.0.1/24")
        cli(f"set interface ip address tap{number} 2001:db8:{number}::1/64")
        cli(f"set interface state tap{number} up")
        de("ip", "link", "set", "ips-tap" + str(number), "netns", ns)
        de("ip", "-n", ns, "link", "set", "lo", "up")
        de("ip", "-n", ns, "addr", "add", f"10.{number}.0.2/24", "dev", "ips-tap" + str(number))
        de("ip", "-n", ns, "-6", "addr", "add", f"2001:db8:{number}::2/64", "dev", "ips-tap" + str(number), "nodad")
        de("ip", "-n", ns, "link", "set", "ips-tap" + str(number), "up")
        de("ip", "-n", ns, "route", "add", "default", "via", f"10.{number}.0.1")
        de("ip", "-n", ns, "-6", "route", "add", "default", "via", f"2001:db8:{number}::1")


def ping(family=4):
    target = "10.2.0.2" if family == 4 else "2001:db8:2::2"
    return de("ip", "netns", "exec", "ips-pc1", "ping", "-" + str(family), "-c", "3", "-W", "1", target, check=False).returncode == 0


def query(kind):
    return stack.request(f"/nodes/{node_id}/query?kind={kind}", viewer=True).get("records", [])


def edit(operations, succeed=True):
    desired = stack.request("/desired")
    change = stack.request("/changes", "POST", {"baseRevision": desired["revision"], "requestId": secrets.token_hex(16)})
    change = stack.request(f'/changes/{change["id"]}/nodes/{node_id}/resources', "PUT",
                           {"editRevision": change["editRevision"], "operations": operations})
    plan = stack.request(f'/changes/{change["id"]}/plan', "POST")
    job = stack.request(f'/changes/{change["id"]}/commit', "POST",
                        {"planId": plan["id"], "requestId": secrets.token_hex(16)})
    def finished():
        result = stack.request("/jobs/" + job["id"])
        return result if result["state"] in ("SUCCEEDED", "FAILED", "PARTIAL", "ROLLED_BACK", "COMPENSATION_FAILED") else None
    result = wait(finished, 120)
    (OUT / (job["id"] + ".json")).write_text(json.dumps(result, indent=2))
    if succeed and result["state"] != "SUCCEEDED":
        raise AssertionError(result)
    if not succeed and result["state"] == "SUCCEEDED":
        raise AssertionError("invalid rule accepted")
    return result


def rule(sid, text, action="CREATE"):
    return {"code": "IPS_RULE", "action": action, "ipsRule": {"sid": sid, "rule": text}}


def main():
    global stack, created, node_id
    run("docker", "network", "create", network)
    stack = Stack(run, OUT, STAMP, network, TOKEN)
    conf = stack.agent_config(name)
    data = json.loads(conf.read_text())
    data["ips"] = {"enabled": True}
    mounts = []
    if EXTERNAL:
        external = OUT / "snort-config"
        external.mkdir()
        base = external / "snort.lua"
        base_text = ("include '/usr/etc/snort/snort.lua'\n"
                     "ips.variables.nets.HOME_NET = '10.1.0.0/24'\n"
                     "alert_json = {file = true}\n")
        base.write_text(base_text)
        data["ips"].update(configFile="/etc/snort-local/snort.lua", autoReload=False)
        mounts = ["-v", f"{external}:/etc/snort-local:ro"]
    conf.write_text(json.dumps(data))
    env = OUT / "agent.env"
    env.write_text("SDN_TOKEN=" + TOKEN + "\n")
    env.chmod(0o600)
    state = OUT / "state"
    for directory in ("agent", "redis", "frr"):
        (state / directory).mkdir(parents=True)
    run("docker", "run", "-d", "--name", name, "--network", network, "--privileged", "--stop-timeout", "150",
        "--env-file", env, "-v", f"{conf}:/etc/sdn-agent/agent.conf:ro",
        "-v", f"{state / 'agent'}:/var/lib/sdn-agent", "-v", f"{state / 'redis'}:/var/lib/redis",
        "-v", f"{state / 'frr'}:/etc/frr", *mounts, IMAGE)
    created = True
    def registered():
        nodes = stack.request("/nodes?pageSize=100").get("items", [])
        return next((n for n in nodes if n["endpoint"] == name + ":9090" and n["state"] == "RUNNING"), None)
    node = wait(registered, 120)
    node_id = node["nodeId"]
    stack.request(f"/nodes/{node_id}/adopt", "POST", {"expectedRevision": node["agentRevision"], "allowExisting": True})
    stack.request(f"/nodes/{node_id}/resources/import", "POST",
                  {"expectedAgentRevision": node["agentRevision"], "requestId": secrets.token_hex(16)})
    topology()
    native = de("python3", "-c", """from vpp_papi import VPPApiClient
c=VPPApiClient(server_address='/run/vpp/api.sock',apidir='/usr/share/vpp/api',read_timeout=5)
c.connect('ips-query-regression')
try:
 for idx in (1,2147483647):
  reply,rows=c.api.snort_interface_v2_get(cursor=0,sw_if_index=idx)
  assert reply.retval != 0 and not rows
 reply,rows=c.api.snort_client_v2_get(cursor=0,snort_client_index=0xffffffff)
 assert reply.retval == 0 and rows and rows[0].n_instances == 1 and rows[0].mode == 2
finally:
 c.disconnect()
""")
    check("native unbound interface and v2 client query", native.returncode == 0)
    check("IPv4 baseline", wait(lambda: ping(4)))
    check("IPv6 baseline", wait(lambda: ping(6)))
    initial = query("IPS_STATUS")[0]
    check("Redis + confd and real VPP DAQ", initial["backend"] == "redis+confd" and initial["daq"] == "vpp")
    bind = {"code": "IPS_INTERFACE", "action": "CREATE", "ipsInterface": {"name": "tap1", "direction": "input"}}
    edit([bind])
    check("attached routed TAP", query("IPS_STATUS")[0]["attachedInterfaces"] == {"tap1": "input"})
    check("IPv4 pass without rules", ping(4))
    check("IPv6 pass without rules", ping(6))
    block4 = 'drop icmp any any -> any any (msg:"IPS IPv4 acceptance"; itype:8; sid:1000001; rev:1;)'
    block6 = 'drop icmp any any -> any any (msg:"IPS IPv6 acceptance"; itype:128; sid:1000002; rev:1;)'
    if EXTERNAL:
        block4 = block4.replace("icmp any any", "icmp $HOME_NET any")
    edit([rule(1000001, block4)])
    check("IPv4 blocked by Snort", not ping(4))
    check("IPv6 remains reachable", ping(6))
    loaded = query("IPS_STATUS")[0]
    check("live reload keeps Snort PID", loaded["pid"] == initial["pid"])
    check("reload generation advanced", loaded["generation"] > initial["generation"])
    if EXTERNAL:
        old_hash = loaded["baseConfigSHA256"]
        before_pid = loaded["pid"]
        # Atomic rename through a DIRECTORY bind mount must be visible.
        candidate = base.with_suffix(".tmp")
        candidate.write_text(base_text.replace("10.1.0.0/24", "10.99.0.0/24"))
        candidate.replace(base)
        check("external edit remains pending with auto reload disabled",
              query("IPS_STATUS")[0]["baseConfigPending"] and not ping(4))
        result = json.loads(de("sdn-snort-reload").stdout)
        check("manual base reload changes real HOME_NET matching", result["reloaded"] and ping(4))
        status = query("IPS_STATUS")[0]
        check("base reload preserves Snort PID and managed rules",
              status["pid"] == before_pid and status["baseConfigSHA256"] != old_hash and
              {int(x["sid"]) for x in query("IPS_RULES")} == {1000001})
        new_hash = status["baseConfigSHA256"]
        block4 = block4.replace("rev:1", "rev:2")
        edit([rule(1000001, block4, "SET")])
        check("Web rule update preserves accepted external base",
              query("IPS_STATUS")[0]["baseConfigSHA256"] == new_hash and ping(4))
        base.write_text("this is not valid Lua\n")
        result = de("sdn-snort-reload", check=False)
        status = query("IPS_STATUS")[0]
        check("invalid base rejected while previous config keeps forwarding",
              result.returncode != 0 and bool(status["baseConfigError"]) and
              status["baseConfigSHA256"] == new_hash and ping(4))
        base.write_text(base_text + "ips.rules = 'alert icmp any any -> any any (sid:1000001;)'\n")
        result = de("sdn-snort-reload", check=False)
        check("base cannot override Agent-managed rules",
              result.returncode != 0 and query("IPS_STATUS")[0]["baseConfigSHA256"] == new_hash)
        base.write_text(base_text)
        de("sdn-snort-reload")
        check("corrected external base restores protection", not ping(4))
    edit([rule(1000002, block6)])
    check("IPv6 blocked by Snort", not ping(6))
    invalid = 'drop icmp any any -> any any (not_a_real_option; sid:1000003; rev:1;)'
    edit([rule(1000003, invalid)], succeed=False)
    check("invalid rule was rolled back", {int(x["sid"]) for x in query("IPS_RULES")} == {1000001, 1000002})
    check("old IPv4 protection remains", not ping(4))
    edit([rule(1000001, "", "DELETE"), rule(1000002, "", "DELETE"), rule(1000003, "", "DELETE")])
    check("delete rule restores IPv4", ping(4))
    check("delete rule restores IPv6", ping(6))
    edit([rule(1000001, block4)])
    # Recreate hand-built links after restart; rule persistence is independent.
    de("ip", "netns", "del", "ips-pc1")
    de("ip", "netns", "del", "ips-pc2")
    if EXTERNAL:
        data["ips"]["autoReload"] = True
        conf.write_text(json.dumps(data))
    run("docker", "restart", "-t", "150", name, timeout=180)
    wait(lambda: de("sdn-agent-healthcheck", check=False).returncode == 0, 120)
    wait(registered, 120)
    topology()
    wait(lambda: query("IPS_STATUS")[0]["state"] == "RUNNING")
    check("rules persist across container restart", {int(x["sid"]) for x in query("IPS_RULES")} == {1000001})
    check("restored rule blocks IPv4", not ping(4))
    check("restored binding passes IPv6", ping(6))
    if EXTERNAL:
        before = query("IPS_STATUS")[0]
        check("external base survives restart", before["autoReload"] and not before["baseConfigPending"])
        base.write_text(base_text.replace("10.1.0.0/24", "10.99.0.0/24"))
        wait(lambda: query("IPS_STATUS")[0]["baseConfigSHA256"] != before["baseConfigSHA256"])
        check("automatic base reload changes packet verdict without restart",
              ping(4) and query("IPS_STATUS")[0]["pid"] == before["pid"])
        accepted = query("IPS_STATUS")[0]["baseConfigSHA256"]
        base.write_text("not valid Lua")
        wait(lambda: query("IPS_STATUS")[0]["baseConfigError"])
        check("automatic bad edit keeps last good base and rules",
              query("IPS_STATUS")[0]["baseConfigSHA256"] == accepted and ping(4))
        base.write_text(base_text)
        wait(lambda: not query("IPS_STATUS")[0]["baseConfigError"] and not query("IPS_STATUS")[0]["baseConfigPending"])
        check("automatic recovery restores protection", not ping(4))
    edit([rule(1000001, "", "DELETE")])
    check("post-restart delete restores IPv4", ping(4))
    access = json.loads(stack.access.read_text())
    access.update(nodeId=node_id, url=stack.base)
    stack.access.write_text(json.dumps(access))
    if os.environ.get("SDN_IPS_BROWSER") == "1":
        print("READY browser acceptance: " + str(stack.access), flush=True)
        wait(lambda: (OUT / "web-ips.json").exists(), 600)
        browser = json.loads((OUT / "web-ips.json").read_text())
        check("real browser IPS create/delete", browser["status"] == "passed")
    report["status"] = "passed"


try:
    main()
except Exception:
    report["status"] = "failed"
    report["error"] = traceback.format_exc()
    print(report["error"], flush=True)
finally:
    if created:
        logs = run("docker", "logs", "--tail", "180", name, check=False)
        (OUT / "agent.log").write_text(logs.stdout + logs.stderr)
    (OUT / "report.json").write_text(json.dumps(report, indent=2))
    if os.environ.get("SDN_IPS_KEEP") != "1":
        if created:
            run("docker", "rm", "-fv", name, check=False)
        if stack:
            stack.close()
        run("docker", "network", "rm", network, check=False)
        (OUT / "agent.env").unlink(missing_ok=True)
        (OUT / "stack-access.json").unlink(missing_ok=True)
    print(json.dumps(report, indent=2), flush=True)
    log.close()
if report["status"] != "passed":
    raise SystemExit(1)
