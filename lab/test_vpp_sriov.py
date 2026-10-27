"""Host ownership/rollback guards. These tests never run Docker or bind PCI."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import vpp_sriov_host as host


class HostSafetyTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / "state.json"

    def write(self, **extra):
        value = dict(
            status="ready",
            pci="0000:3b:00.0",
            originalDriver="vfio-pci",
            originalOverride="(null)",
            originalAutoprobe="1",
            owner="original-vpp",
            restartOwner=True,
            vfs=[],
        )
        value.update(extra)
        self.state.write_text(json.dumps(value))

    def test_invalid_pci_and_driver_never_reach_host(self):
        with patch.object(host, "host") as privileged:
            for address, driver in (
                ("../devices", "vfio-pci"),
                ("0000:3b:00.0", "unrelated-driver"),
            ):
                with self.assertRaises(RuntimeError):
                    host.bind(address, driver)
            privileged.assert_not_called()

    def test_existing_state_requires_restore(self):
        self.write(status="preparing")
        with patch.object(host, "run") as commands:
            with self.assertRaisesRegex(RuntimeError, "restore the previous"):
                host.prepare(self.state, "0000:3b:00.0", "owner")
            commands.assert_not_called()

    def test_existing_vfs_are_not_taken_over(self):
        pf = self.root / "0000:3b:00.0"
        pf.mkdir()
        (pf / "vendor").write_text("0x19e5")
        (pf / "sriov_numvfs").write_text("1")
        with patch.object(host, "SYS", self.root), patch.object(
            host, "run"
        ) as commands:
            with self.assertRaisesRegex(RuntimeError, "existing VFs"):
                host.prepare(self.state, pf.name, "owner")
            commands.assert_not_called()

    def test_restore_refuses_live_vf_containers(self):
        self.write()
        with patch.object(
            host, "run", return_value="sdn-vf-lab-123-leaf1"
        ), patch.object(host, "host") as privileged, patch.object(host, "bind") as bind:
            with self.assertRaisesRegex(RuntimeError, "remove owned VF lab"):
                host.restore(self.state)
            privileged.assert_not_called()
            bind.assert_not_called()

    def test_restored_state_is_idempotent(self):
        self.write(status="restored")
        with patch.object(host, "run") as commands, patch.object(
            host, "host"
        ) as privileged:
            self.assertEqual(host.restore(self.state)["status"], "restored")
            commands.assert_not_called()
            privileged.assert_not_called()

    def test_restore_removes_vfs_before_rebinding_and_restarting(self):
        self.write()
        events = []

        def command(*args, **_kw):
            events.append(args)
            return ""

        with patch.object(host, "run", side_effect=command), patch.object(
            host, "host", side_effect=lambda *args: events.append(("sysfs", *args))
        ), patch.object(
            host, "bind", side_effect=lambda *args: events.append(("bind", *args))
        ):
            host.restore(self.state)
        self.assertIn("sriov_numvfs", events[1][1])
        self.assertEqual(events[2], ("bind", "0000:3b:00.0", "vfio-pci"))
        self.assertEqual(events[-1], ("docker", "start", "original-vpp"))
        self.assertEqual(json.loads(self.state.read_text())["status"], "restored")


if __name__ == "__main__":
    unittest.main()
