"""Optional real Controller/Web query path for the disposable VPP labs."""

import json
import os
from pathlib import Path
import secrets
import time
import urllib.error
import urllib.parse
import urllib.request


class Stack:
    def __init__(self, run, out, stamp, network, agent_token):
        self.run, self.out, self.stamp = run, Path(out), stamp
        self.names = []
        self.admin, self.viewer = secrets.token_urlsafe(24), secrets.token_urlsafe(24)
        self.controller = stamp + "-controller"
        self.web = stamp + "-web"
        env = self.out / (stamp + "-stack.env")
        env.write_text(
            f"SDN_ADMIN_TOKEN={self.admin}\nSDN_VIEWER_TOKEN={self.viewer}\nSDN_AGENT_TOKEN={agent_token}\n"
        )
        os.chmod(env, 0o600)
        self.env = env
        try:
            for name, image, extra in (
                (
                    self.controller,
                    os.environ.get(
                        "SDN_CONTROLLER_IMAGE", "alpine/sdn-controller:vpp-dev"
                    ),
                    [],
                ),
                (
                    self.web,
                    os.environ.get("SDN_WEB_IMAGE", "alpine/sdn-web:vpp-dev"),
                    ["-p", "127.0.0.1::8080"],
                ),
            ):
                command = (
                    ["-controller", self.controller + ":19090", "-insecure"]
                    if name == self.web
                    else []
                )
                run(
                    "docker",
                    "run",
                    "-d",
                    "--name",
                    name,
                    "--network",
                    network,
                    "--env-file",
                    env,
                    *extra,
                    image,
                    *command,
                )
                self.names.append(name)
            port = (
                run("docker", "port", self.web, "8080/tcp")
                .stdout.splitlines()[0]
                .rsplit(":", 1)[1]
            )
            self.base = "http://127.0.0.1:" + port
            deadline = time.monotonic() + 80
            while time.monotonic() < deadline:
                try:
                    self.request("/desired")
                    break
                except Exception:
                    time.sleep(1)
            else:
                raise RuntimeError("isolated Controller/Web did not start")
            self.access = self.out / "stack-access.json"
            self.access.write_text(
                json.dumps(
                    {
                        "url": "http://10.8.126.103:" + port,
                        "admin": self.admin,
                        "viewer": self.viewer,
                        "stamp": stamp,
                    }
                )
            )
            os.chmod(self.access, 0o600)
        except Exception:
            self.close()
            raise

    def agent_config(self, leaf):
        path = self.out / (leaf + "-agent.conf")
        path.write_text(
            json.dumps(
                {
                    "agent": {
                        "dataplane": "vpp",
                        "insecure": True,
                        "controller": self.controller + ":19090",
                        "endpoint": leaf + ":9090",
                    },
                    "redis": {"enabled": True},
                }
            )
        )
        return path

    def request(self, path, method="GET", body=None, viewer=False):
        req = urllib.request.Request(
            self.base + "/api/v1" + path,
            method=method,
            data=json.dumps(body).encode() if body is not None else None,
            headers={
                "Authorization": "Bearer " + (self.viewer if viewer else self.admin),
                "Content-Type": "application/json",
                "Idempotency-Key": secrets.token_hex(16),
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=35) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode(errors="replace")
            raise RuntimeError(f"{method} {path}: HTTP {exc.code}: {detail}") from exc

    def verify(self, leaves, require_vxlan=True):
        expected = {n["name"] + ":9090" for n in leaves}
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            nodes = self.request("/nodes?pageSize=200").get("items", [])
            if {n["endpoint"] for n in nodes if n["state"] == "RUNNING"} == expected:
                break
            time.sleep(1)
        else:
            raise AssertionError("VPP Agents did not register with Controller")
        saved = {}
        for node in nodes:
            nid = node["nodeId"]
            # Heartbeat revision can lag a just-completed direct Agent commit.
            for attempt in range(30):
                node = self.request(f"/nodes/{nid}")
                try:
                    self.request(
                        f"/nodes/{nid}/adopt",
                        "POST",
                        {
                            "expectedRevision": node["agentRevision"],
                            "allowExisting": True,
                        },
                    )
                    break
                except RuntimeError as exc:
                    if "agent revision conflict" not in str(exc) or attempt == 29:
                        raise
                    time.sleep(1)
            saved[nid] = {}
            for kind in (
                "TENANTS",
                "BRIDGES",
                "PORTS",
                "INTERFACES",
                "ADDRESSES",
                "NEIGHBORS",
                "ROUTES",
                "FDB",
                "VXLAN_TUNNELS",
            ):
                result = self.request(
                    f"/nodes/{nid}/query?kind={kind}&pageSize=200", viewer=True
                )
                assert result["source"] == "vpp", result
                assert result["nodeId"] == nid and result["kind"] == kind, result
                saved[nid][kind] = result
            assert not require_vxlan or any(
                r.get("kind") == "vxlan"
                for r in saved[nid]["INTERFACES"].get("records", [])
            )
            vx = saved[nid]["VXLAN_TUNNELS"].get("records", [])
            assert (vx or not require_vxlan) and all(r["multipoint"] for r in vx), vx
            for family in ("IPV4", "IPV6"):
                query = f"/nodes/{nid}/query?kind=VXLAN_TUNNELS&family={family}"
                rows = self.request(query, viewer=True).get("records", [])
                assert all(r["family"] == family for r in rows)
                self.request(
                    f"/nodes/{nid}/query?kind=ROUTES&family={family}", viewer=True
                )
        (self.out / "stack-query.json").write_text(
            json.dumps({"status": "passed", "nodes": saved}, indent=2)
        )
        access = json.loads(self.access.read_text())
        access["nodeId"] = next(
            (
                node["nodeId"]
                for node in nodes
                if saved[node["nodeId"]]["VXLAN_TUNNELS"].get("records")
            ),
            nodes[0]["nodeId"],
        )
        self.access.write_text(json.dumps(access))
        if os.environ.get("SDN_VPP_BROWSER") == "1":
            done = self.out / (self.stamp + "-browser.json")
            deadline = time.monotonic() + 600
            print("READY real Web browser query acceptance", flush=True)
            while time.monotonic() < deadline:
                if done.exists():
                    result = json.loads(done.read_text())
                    assert result["status"] == "passed", result
                    break
                time.sleep(1)
            else:
                raise AssertionError("browser acceptance did not finish")

    def close(self):
        for name in reversed(self.names):
            self.run("docker", "logs", "--tail", "80", name, check=False)
            self.run("docker", "rm", "-fv", name, check=False)
        self.names.clear()
        if hasattr(self, "env"):
            self.env.unlink(missing_ok=True)
        if hasattr(self, "access"):
            self.access.unlink(missing_ok=True)
