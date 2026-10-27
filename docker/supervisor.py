#!/usr/bin/env python3
"""Supervise the Agent, its local Redis and its dedicated FRR instance."""

import json
import os
from pathlib import Path
import pwd
import re
import shutil
import signal
import socket
import subprocess
import sys
import time

RUNTIME = Path("/run/sdn-agent/runtime.json")
DEFAULT_CONFIG = "/etc/sdn-agent/agent.conf"
FRR_INIT = "/usr/lib/frr/frrinit.sh"
VPP_ADAPTER = "/usr/local/lib/sdn-agent/vpp/adapter.py"
stopping = False


def request_stop(_signum, _frame):
    global stopping
    stopping = True


def effective_args(args):
    if not any(arg in ('-config', '--config') or arg.startswith(('-config=', '--config=')) for arg in args):
        return ['-config', DEFAULT_CONFIG, *args]
    return args


def utility_only(args):
    check = False
    for arg in args:
        name, equal, value = arg.partition('=')
        if name in ('-h', '--help', '-help'):
            return True
        if name in ('-check-config', '--check-config'):
            # Go's last occurrence wins. Invalid values also go directly to Go
            # to receive its error, without starting any services.
            check = not equal or value not in ('0', 'f', 'F', 'FALSE', 'false', 'False')
    return check


def validated_config(args):
    checked = subprocess.run(['sdn-agent', *args, '-check-config'], capture_output=True, text=True, timeout=15)
    if checked.returncode:
        # Go validation does not emit token values; do not echo the input file.
        raise RuntimeError('agent configuration rejected: ' + checked.stderr.strip())
    return json.loads(checked.stdout)


def address(value):
    host, port = value.rsplit(':', 1)
    host = host.strip('[]')
    if host in ('', '0.0.0.0'):
        host = '127.0.0.1'
    elif host == '::':
        host = '::1'
    return host, int(port)


def tcp_ready(value):
    try:
        with socket.create_connection(address(value), timeout=1):
            return True
    except OSError:
        return False


def process_alive(pid, expected):
    try:
        os.kill(pid, 0)
        command = Path(f'/proc/{pid}/comm').read_text().strip()
        return command == expected
    except (OSError, ValueError):
        return False


def redis_ready(settings):
    try:
        proc = subprocess.run(['redis-cli', '-h', settings['bind'], '-p', str(settings['port']), 'PING'],
                              text=True, capture_output=True, timeout=2)
        return proc.returncode == 0 and proc.stdout.strip() == 'PONG'
    except (OSError, subprocess.TimeoutExpired):
        return False


def frr_ready():
    try:
        for name in ('watchfrr', 'zebra', 'bgpd', 'ospfd', 'ospf6d'):
            pid = int(Path(f'/run/frr/{name}.pid').read_text().strip())
            if not process_alive(pid, name):
                return False
        proc = subprocess.run(['vtysh', '-c', 'show version'], stdout=subprocess.DEVNULL,
                              stderr=subprocess.DEVNULL, timeout=4)
        return proc.returncode == 0
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return False


def wait_ready(check, timeout, label, children=()):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if stopping:
            raise InterruptedError('shutdown requested')
        if any(child.poll() is not None for child in children):
            raise RuntimeError(f'{label}: required process exited during startup')
        if check():
            return
        time.sleep(0.1)
    raise RuntimeError(f'{label}: readiness timeout')


def redis_command(settings):
    return ['redis-server', '--bind', settings['bind'], '--port', str(settings['port']),
            '--dir', settings['dataDir'], '--appendonly', 'yes' if settings['appendOnly'] else 'no',
            '--appendfsync', settings['appendFsync'], '--save', '', '--protected-mode', 'yes',
            '--daemonize', 'no', '--logfile', '', '--pidfile', '/run/redis/redis.pid']


def prepare_redis(settings):
    directory = Path(settings['dataDir'])
    if directory.resolve() != directory or directory.is_symlink():
        raise RuntimeError('Redis data directory cannot be a symlink')
    directory.mkdir(parents=True, exist_ok=True)
    account = pwd.getpwnam('redis')
    # Existing standalone Redis volumes may use a different uid. Preserve all
    # files. Only accept a dedicated Redis directory and inspect all entries
    # before changing ownership; never recursively chown arbitrary user paths.
    entries = [directory]
    for item in directory.rglob('*'):
        if item.is_symlink() or (item.is_file() and item.stat().st_nlink != 1):
            raise RuntimeError('Redis data directory contains linked files')
        if item.is_dir():
            if item.parent != directory or item.name != 'appendonlydir':
                raise RuntimeError('Redis data directory contains an unrelated directory')
        elif item.is_file():
            if item.parent not in (directory, directory / 'appendonlydir') or not re.fullmatch(r'(dump\.rdb|appendonly\.aof(?:\.[A-Za-z0-9.-]+)?|temp-[A-Za-z0-9.-]+)', item.name):
                raise RuntimeError('Redis data directory contains unrelated files')
        else:
            raise RuntimeError('Redis data directory contains unsupported filesystem entries')
        entries.append(item)
    for item in entries:
        os.chown(item, account.pw_uid, account.pw_gid, follow_symlinks=False)
    os.chmod(directory, 0o700)
    run_directory = Path('/run/redis')
    run_directory.mkdir(parents=True, exist_ok=True)
    os.chown(run_directory, account.pw_uid, account.pw_gid)
    os.chmod(run_directory, 0o700)
    return account


def prepare_frr():
    Path('/etc/frr').mkdir(parents=True, exist_ok=True)
    Path('/run/frr').mkdir(parents=True, exist_ok=True)
    account = pwd.getpwnam('frr')
    for directory in (Path('/etc/frr'), Path('/run/frr')):
        os.chown(directory, account.pw_uid, account.pw_gid)
    for name in ('daemons', 'frr.conf', 'vtysh.conf'):
        target = Path('/etc/frr') / name
        if not target.exists():
            shutil.copyfile(Path('/usr/share/sdn-agent/frr') / name, target)
        os.chown(target, account.pw_uid, account.pw_gid)
        os.chmod(target, 0o640)
    # A persistent /etc/frr from an earlier release may have ospf6d disabled.
    # These daemons implement the bundle's advertised operations and readiness.
    daemons = Path('/etc/frr/daemons')
    content = daemons.read_text()
    for name in ('bgpd', 'ospfd', 'ospf6d'):
        if re.search(rf'^{name}=', content, re.MULTILINE):
            content = re.sub(rf'^{name}=.*$', f'{name}=yes', content, flags=re.MULTILINE)
        else:
            content += f'\n{name}=yes\n'
    daemons.write_text(content)


def vpp_ready(config):
    try:
        p = subprocess.run(
            [
                "python3",
                VPP_ADAPTER,
                "--socket",
                config["vpp"]["adapterSocket"],
                "--health",
            ],
            capture_output=True,
            timeout=7,
        )
        return p.returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def vpp_api_ready(config):
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
            client.settimeout(1)
            client.connect(config["vpp"]["apiSocket"])
        return True
    except OSError:
        return False


def write_runtime(config, agent, redis):
    data = {'config': config, 'agentPid': agent.pid, 'redisPid': redis.pid if redis else None}
    temp = RUNTIME.with_suffix('.tmp')
    with os.fdopen(os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), 'w') as stream:
        json.dump(data, stream)
    os.replace(temp, RUNTIME)


def stop_child(child, timeout):
    if child is None or child.poll() is not None:
        return
    child.send_signal(signal.SIGTERM)
    try:
        child.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        child.kill()
        child.wait(timeout=10)


def healthcheck():
    try:
        state = json.loads(RUNTIME.read_text())
        config = state["config"]
        if not process_alive(state["agentPid"], "sdn-agent") or not tcp_ready(
            config["agent"]["listen"]
        ):
            return 1
        if config["redis"]["enabled"] and (
            not process_alive(state["redisPid"], "redis-server")
            or not redis_ready(config["redis"])
        ):
            return 1
        if config["agent"]["frr"] and not frr_ready():
            return 1
        if config["agent"].get("dataplane", "linux") == "vpp" and not vpp_ready(config):
            return 1
        return 0
    except (OSError, ValueError, KeyError, TypeError):
        return 1


def main(args):
    if args == ["--healthcheck"]:
        return healthcheck()
    if args and not args[0].startswith("-"):
        os.execvp(args[0], args)
    args = effective_args(args)
    if utility_only(args):
        os.execvp("sdn-agent", ["sdn-agent", *args])
    # Validate before creating directories, starting Redis/FRR, or touching data.
    config = validated_config(args)
    signal.signal(signal.SIGTERM, request_stop)
    signal.signal(signal.SIGINT, request_stop)
    RUNTIME.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(RUNTIME.parent, 0o700)
    RUNTIME.unlink(missing_ok=True)
    redis = agent = vpp = adapter = None
    frr_started = False
    result = 1
    try:
        if config["redis"]["enabled"]:
            account = prepare_redis(config["redis"])
            redis = subprocess.Popen(
                redis_command(config["redis"]),
                user=account.pw_uid,
                group=account.pw_gid,
                extra_groups=[],
            )
            wait_ready(
                lambda: redis_ready(config["redis"]),
                config["redis"]["startupTimeoutSeconds"],
                "Redis",
                (redis,),
            )
        if config["agent"].get("dataplane", "linux") == "vpp":
            settings = config["vpp"]
            if (
                not Path(settings["startupConfig"]).is_file()
                or not Path(VPP_ADAPTER).is_file()
            ):
                raise RuntimeError(
                    "VPP startup configuration/adapter missing; use the VPP bundle image"
                )
            Path("/run/vpp/log").mkdir(parents=True, exist_ok=True)
            vpp = subprocess.Popen(["vpp", "-c", settings["startupConfig"]])
            wait_ready(
                lambda: vpp_api_ready(config),
                settings["startupTimeoutSeconds"],
                "VPP",
                (vpp,),
            )
            adapter = subprocess.Popen(
                [
                    "python3",
                    VPP_ADAPTER,
                    "--socket",
                    settings["adapterSocket"],
                    "--api",
                    settings["apiSocket"],
                    "--state",
                    str(Path(config["agent"]["stateDir"]) / "vpp.json"),
                ]
            )
            wait_ready(
                lambda: vpp_ready(config),
                settings["startupTimeoutSeconds"],
                "VPP adapter",
                (vpp, adapter),
            )
        if config["agent"]["frr"]:
            prepare_frr()
            frr_started = True
            subprocess.run([FRR_INIT, "start"], check=True, timeout=40)
            wait_ready(frr_ready, 30, "FRR", (redis,) if redis else ())
        agent = subprocess.Popen(["sdn-agent", *args])
        children = tuple(
            child for child in (agent, redis, vpp, adapter) if child is not None
        )
        wait_ready(lambda: tcp_ready(config["agent"]["listen"]), 45, "Agent", children)
        write_runtime(config, agent, redis)
        print(
            "sdn-agent bundle ready (dataplane="
            + config["agent"].get("dataplane", "linux")
            + ")",
            flush=True,
        )
        next_probe = time.monotonic()
        while not stopping:
            if any(child.poll() is not None for child in children):
                raise RuntimeError("required Agent/Redis/VPP/adapter process exited")
            if time.monotonic() >= next_probe:
                if config["agent"]["frr"] and not frr_ready():
                    raise RuntimeError("required FRR process is unavailable")
                if redis and not redis_ready(config["redis"]):
                    raise RuntimeError("required Redis service is unavailable")
                if vpp and not vpp_ready(config):
                    raise RuntimeError("required VPP adapter/API is unavailable")
                next_probe = time.monotonic() + 3
            time.sleep(0.2)
        result = 0
    except InterruptedError:
        result = 0
    except Exception as error:
        print(f"sdn-agent bundle failed: {error}", file=sys.stderr, flush=True)
    finally:
        RUNTIME.unlink(missing_ok=True)
        # Keep the journal and network backend available while Agent drains its
        # active task/rollback. Container stop_grace_period must allow this wait.
        stop_child(agent, 120)
        if adapter:
            stop_child(adapter, 15)
        if frr_started:
            try:
                subprocess.run([FRR_INIT, "stop"], check=False, timeout=30)
            except subprocess.TimeoutExpired:
                print("FRR shutdown timed out", file=sys.stderr, flush=True)
        if vpp:
            stop_child(vpp, 30)
        stop_child(redis, config["redis"]["shutdownTimeoutSeconds"])
    return result


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except Exception as error:
        print(f"sdn-agent startup rejected: {error}", file=sys.stderr, flush=True)
        sys.exit(1)
