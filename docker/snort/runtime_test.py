import importlib.util
from pathlib import Path
import unittest
import tempfile
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("ips_runtime", Path(__file__).with_name("runtime.py"))
ips = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ips)

RULE = 'drop icmp any any -> any any (msg:"block ping"; itype:8; sid:1000001; rev:1;)'


class RulesTest(unittest.TestCase):
    def test_create_replace_delete_and_idempotence(self):
        empty = {"rules": {}, "interfaces": {}}
        create = {"code": "IPS_RULE", "action": "CREATE", "ipsRule": {"sid": 1000001, "rule": RULE}}
        state = ips.updated(empty, create)
        self.assertEqual(ips.updated(state, create), state)
        self.assertEqual(empty["rules"], {})
        replace = {**create, "action": "SET", "ipsRule": {"sid": 1000001, "rule": RULE.replace("itype:8", "itype:0")}}
        changed = ips.updated(state, replace)
        self.assertNotEqual(changed, state)
        self.assertEqual(ips.updated(changed, {**create, "action": "DELETE"}), empty)
        with self.assertRaises(ValueError):
            ips.updated(empty, replace)

    def test_reject_injection_mismatch_and_duplicate_sid(self):
        for text in [RULE + "\ninclude /etc/shadow", 'include "/etc/shadow"',
                     RULE.replace("sid:1000001", "sid:99"),
                     RULE.replace("rev:1", "sid:1000001"),
                     RULE.replace("rev:1", 'luajit:"evil.lua"'),
                     RULE.replace("rev:1", "gid:2")]:
            with self.subTest(text=text), self.assertRaises(ValueError):
                ips.validate_rule(1000001, text)

    def test_message_cannot_forge_sid(self):
        text = 'alert ip any any -> any any (msg:"fake; sid:1000001;"; sid:9;)'
        with self.assertRaises(ValueError):
            ips.validate_rule(1000001, text)
        ips.validate_rule(9, text)

    def test_lua_encodes_quotes_backslashes_and_unicode(self):
        encoded = ips.lua_string('"\\\n中')
        self.assertEqual(encoded, '"' + "\\034\\092\\010\\228\\184\\173" + '"')

    def test_empty_rules_render_a_valid_nonempty_rules_source(self):
        runtime = ips.Runtime.__new__(ips.Runtime)
        runtime.base = "alert_json = {file = true}"
        runtime.base_path = Path("/config/base.lua")
        text = runtime.render({"rules": {}, "interfaces": {}})
        self.assertIn("# No managed IPS rules", text)
        self.assertNotIn('rules = ""', text)

    def test_interface_direction_and_snapshot_shape(self):
        state = {"rules": {}, "interfaces": {}}
        op = {"code": "IPS_INTERFACE", "action": "CREATE",
              "ipsInterface": {"name": "GigabitEthernet3b/0/0", "direction": "input"}}
        after = ips.updated(state, op)
        self.assertEqual(after["interfaces"][op["ipsInterface"]["name"]], "input")
        op["ipsInterface"]["name"] = "tap0; reboot"
        with self.assertRaises(ValueError):
            ips.updated(state, op)
        with self.assertRaises(ValueError):
            ips.normalize({**state, "command": "reboot"})



class BaseConfigTest(unittest.TestCase):
    def runtime(self):
        runtime = ips.Runtime.__new__(ips.Runtime)
        runtime.base = "ips = { variables = { nets = { HOME_NET = '10.0.0.0/8' } } }"
        runtime.base_path = Path("/external/snort.lua")
        runtime.config = {"ips": {"autoReload": True}}
        runtime.state = {"rules": {"1000001": RULE}, "interfaces": {}}
        runtime.base_error = ""
        runtime.base_attempted = runtime.base_candidate = ips.base_hash(runtime.base)
        runtime.loaded = Mock()
        runtime.confd = Mock()
        runtime.ensure_reload = Mock()
        return runtime

    def test_snapshot_merges_rules_and_preserves_base_options(self):
        runtime = self.runtime()
        text = runtime.render(runtime.state)
        self.assertIn(ips.lua_string(runtime.base), text)
        self.assertIn(ips.lua_string(RULE), text)
        self.assertIn("ips.mode = 'inline'", text)
        self.assertIn("ips.rules is managed by Agent", text)
        self.assertIn("ips.include is managed by Agent", text)
        self.assertNotIn("alert_json =", text)  # logger belongs to base config

    def test_reload_failure_restores_accepted_snapshot(self):
        runtime = self.runtime()
        before = runtime.base
        seen = []
        def confd(state):
            seen.append(runtime.base)
            if runtime.base == "bad lua":
                raise RuntimeError("Snort syntax error")
        runtime.confd.side_effect = confd
        with self.assertRaisesRegex(RuntimeError, "Snort syntax error"):
            runtime.reload_base("bad lua")
        self.assertEqual(runtime.base, before)
        self.assertEqual(seen, ["bad lua", before])
        self.assertIn("Snort syntax error", runtime.base_error)
        self.assertEqual(runtime.state["rules"]["1000001"], RULE)

    @patch.object(ips, "generation", return_value=3)
    def test_reload_failure_reports_failed_recovery(self, generation):
        runtime = self.runtime()
        runtime.confd.side_effect = RuntimeError("confd failed")
        with self.assertRaisesRegex(RuntimeError, "recovery failed"):
            runtime.reload_base("new config")

    @patch.object(ips, "generation", return_value=4)
    def test_manual_reload_uses_single_snapshot_and_same_content_is_noop(self, generation):
        runtime = self.runtime()
        with patch.object(ips, "read_base", return_value="new base") as read:
            reply = runtime.reload_base()
        read.assert_called_once_with(runtime.base_path)
        self.assertTrue(reply["reloaded"])
        self.assertEqual(runtime.base, "new base")
        runtime.confd.reset_mock()
        self.assertFalse(runtime.reload_base("new base")["reloaded"])
        runtime.confd.assert_not_called()

    def test_auto_reload_debounces_and_does_not_retry_identical_bad_file(self):
        runtime = self.runtime()
        runtime.confd.side_effect = lambda _: (_ for _ in ()).throw(RuntimeError("bad")) if runtime.base == "bad" else None
        with patch.object(ips, "read_base", return_value="bad"):
            runtime.poll_base()
            runtime.confd.assert_not_called()
            runtime.poll_base()
            self.assertEqual(runtime.confd.call_count, 2)
            runtime.poll_base()
            self.assertEqual(runtime.confd.call_count, 2)
        runtime.config["ips"]["autoReload"] = False
        with patch.object(ips, "read_base") as read:
            runtime.poll_base()
        read.assert_not_called()

    def test_file_restrictions_and_pending_missing_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "base.lua"
            for data in [b"", b"\x00", b"x" * ((1 << 20) + 1), b"\xff"]:
                path.write_bytes(data)
                with self.assertRaises(ValueError):
                    ips.read_base(path)
            path.write_text("-- valid UTF-8 中文")
            self.assertEqual(ips.read_base(path), "-- valid UTF-8 中文")
            runtime = self.runtime()
            runtime.base_path = path
            self.assertTrue(runtime.base_pending())
            path.unlink()
            self.assertTrue(runtime.base_pending())


if __name__ == "__main__":
    unittest.main()
