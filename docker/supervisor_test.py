"""Process-supervision checks; never start Docker, FRR, or modify host networking."""

import importlib.util
import json
from pathlib import Path
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location(
    "supervisor", Path(__file__).with_name("supervisor.py")
)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class SupervisionTests(unittest.TestCase):
    def setUp(self):
        module.stopping = False

    def test_config_failure_does_not_start_children_or_create_runtime(self):
        with tempfile.TemporaryDirectory() as tmp:
            runtime = Path(tmp) / 'absent' / 'runtime.json'
            with patch.object(module, 'RUNTIME', runtime), patch.object(module, 'validated_config', side_effect=RuntimeError('bad config')), patch.object(module.subprocess, 'Popen') as spawn:
                with self.assertRaises(RuntimeError):
                    module.main(['-config', '/bad.conf'])
                self.assertFalse(runtime.parent.exists())
                spawn.assert_not_called()

    def test_readiness_fails_when_dependency_exits(self):
        dead = Mock()
        dead.poll.return_value = 1
        with self.assertRaisesRegex(RuntimeError, 'required process exited'):
            module.wait_ready(lambda: False, 30, 'Redis', (dead,))

    def test_utility_flags_respect_equals_and_last_value(self):
        self.assertTrue(module.utility_only(['-check-config=true']))
        self.assertTrue(module.utility_only(['--check-config=1']))
        self.assertFalse(module.utility_only(['-check-config=false']))
        self.assertFalse(module.utility_only(['-check-config', '-check-config=false']))
        self.assertTrue(module.utility_only(['-check-config=false', '-check-config=true']))

    def test_redis_rejects_unrelated_data_before_chown(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            (directory / 'important.txt').write_text('do not change ownership')
            with patch.object(module.pwd, 'getpwnam', return_value=Mock(pw_uid=101, pw_gid=101)), patch.object(module.os, 'chown') as chown:
                with self.assertRaisesRegex(RuntimeError, 'unrelated files'):
                    module.prepare_redis({'dataDir': str(directory)})
                chown.assert_not_called()

    def test_shutdown_waits_then_kills_only_if_timeout(self):
        process = Mock()
        process.poll.return_value = None
        module.stop_child(process, 3)
        process.send_signal.assert_called_once_with(signal.SIGTERM)
        process.wait.assert_called_once_with(timeout=3)
        process.kill.assert_not_called()
        process.reset_mock()
        process.wait.side_effect = [subprocess.TimeoutExpired('agent', 3), 0]
        module.stop_child(process, 3)
        process.kill.assert_called_once()

    def test_redis_arguments_do_not_use_shell_interpolation(self):
        settings = {'bind': '127.0.0.1', 'port': 16379, 'dataDir': '/data/with spaces', 'appendOnly': True, 'appendFsync': 'always'}
        command = module.redis_command(settings)
        self.assertIn('/data/with spaces', command)
        self.assertEqual(command[command.index('--appendfsync') + 1], 'always')

    def test_health_uses_effective_port_and_required_dependencies(self):
        state = {'config': {'agent': {'listen': ':19091', 'frr': True}, 'redis': {'enabled': True}}, 'agentPid': 101, 'redisPid': 102}
        with tempfile.TemporaryDirectory() as tmp:
            runtime = Path(tmp) / 'runtime.json'
            runtime.write_text(json.dumps(state))
            with patch.object(module, 'RUNTIME', runtime), patch.object(module, 'process_alive', return_value=True), patch.object(module, 'tcp_ready', return_value=True) as tcp, patch.object(module, 'redis_ready', return_value=True), patch.object(module, 'frr_ready', return_value=False):
                self.assertEqual(module.healthcheck(), 1)
                tcp.assert_called_with(':19091')
            with patch.object(module, 'RUNTIME', runtime), patch.object(module, 'process_alive', return_value=True), patch.object(module, 'tcp_ready', return_value=True), patch.object(module, 'redis_ready', return_value=True), patch.object(module, 'frr_ready', return_value=True):
                self.assertEqual(module.healthcheck(), 0)

    def test_child_failure_stops_agent_before_frr_and_redis(self):
        config = {'agent': {'listen': ':9090', 'frr': True}, 'redis': {'enabled': True, 'startupTimeoutSeconds': 30, 'shutdownTimeoutSeconds': 30}}
        events = []
        redis, agent = Mock(), Mock()
        redis.poll.return_value = 1
        agent.poll.return_value = None
        account = Mock(pw_uid=101, pw_gid=101)
        def stop(child, timeout):
            events.append('agent-stop' if child is agent else 'redis-stop')
        def run(args, **kwargs):
            if args[-1] == 'stop': events.append('frr-stop')
        with tempfile.TemporaryDirectory() as tmp, patch.object(module, 'RUNTIME', Path(tmp) / 'runtime.json'), patch.object(module, 'validated_config', return_value=config), patch.object(module, 'prepare_redis', return_value=account), patch.object(module, 'redis_command', return_value=['redis-server']), patch.object(module, 'prepare_frr'), patch.object(module, 'wait_ready'), patch.object(module, 'write_runtime'), patch.object(module.subprocess, 'Popen', side_effect=[redis, agent]), patch.object(module.subprocess, 'run', side_effect=run), patch.object(module, 'stop_child', side_effect=stop):
            self.assertEqual(module.main(['-config', '/agent.conf']), 1)
        self.assertEqual(events, ['agent-stop', 'frr-stop', 'redis-stop'])
    def test_vpp_failure_stops_agent_before_network_dependencies(self):
        config = {
            "agent": {
                "listen": ":9090",
                "frr": True,
                "dataplane": "vpp",
                "stateDir": "/data",
            },
            "redis": {
                "enabled": True,
                "startupTimeoutSeconds": 30,
                "shutdownTimeoutSeconds": 30,
            },
            "vpp": {
                "startupConfig": "/etc/vpp/startup.conf",
                "apiSocket": "/run/vpp/api.sock",
                "adapterSocket": "/run/sdn-agent/vpp.sock",
                "startupTimeoutSeconds": 60,
            },
        }
        events = []
        redis, vpp, adapter, agent = [Mock() for _ in range(4)]
        for child in (redis, vpp, adapter, agent):
            child.poll.return_value = None
        vpp.poll.return_value = 1

        def stop(child, timeout):
            events.append(
                {
                    id(redis): "redis",
                    id(vpp): "vpp",
                    id(adapter): "adapter",
                    id(agent): "agent",
                }[id(child)]
            )

        def run(args, **kwargs):
            if args[-1] == "stop":
                events.append("frr")

        with tempfile.TemporaryDirectory() as tmp, patch.object(
            module, "RUNTIME", Path(tmp) / "runtime.json"
        ), patch.object(module, "validated_config", return_value=config), patch.object(
            module, "prepare_redis", return_value=Mock(pw_uid=101, pw_gid=101)
        ), patch.object(
            module, "redis_command", return_value=["redis-server"]
        ), patch.object(
            module, "prepare_frr"
        ), patch.object(
            module, "wait_ready"
        ), patch.object(
            module, "write_runtime"
        ), patch.object(
            module.Path, "is_file", return_value=True
        ), patch.object(
            module.Path, "mkdir"
        ), patch.object(
            module.subprocess, "Popen", side_effect=[redis, vpp, adapter, agent]
        ), patch.object(
            module.subprocess, "run", side_effect=run
        ), patch.object(
            module, "stop_child", side_effect=stop
        ):
            self.assertEqual(module.main([]), 1)
        self.assertEqual(events, ["agent", "adapter", "frr", "vpp", "redis"])


if __name__ == "__main__":
    unittest.main()
