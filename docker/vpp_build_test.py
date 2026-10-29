"""Build input and provenance checks: no Docker builds, network or host changes."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

DOCKER = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location(
    "source_metadata", DOCKER / "vpp" / "source-metadata.py"
)
metadata = importlib.util.module_from_spec(spec)
spec.loader.exec_module(metadata)


class BuildInputsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sdn build tests ")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / ".git").mkdir()
        self.log = self.root / "docker.jsonl"
        executable = self.root / "docker"
        executable.write_text(
            f"#!{sys.executable}\n"
            "import json, os, sys\n"
            "with open(os.environ['DOCKER_CALLS'], 'a') as stream:\n"
            "    stream.write(json.dumps(sys.argv[1:]) + '\\n')\n"
            "sys.exit(int(os.environ.get('DOCKER_EXIT', '0')))\n"
        )
        executable.chmod(0o755)
        self.env = {
            key: value
            for key, value in os.environ.items()
            if not key.startswith(
                (
                    "SNORT_",
                    "VPP_",
                    "FRR_",
                    "BUILD_",
                    "ALPINE_",
                    "AGENT_IMAGE",
                    "SOURCE_CACHE_BUST",
                )
            )
        }
        self.env.update(
            PATH=f"{self.root}{os.pathsep}{os.environ['PATH']}",
            DOCKER_CALLS=str(self.log),
        )

    def run_build(self, **settings):
        return subprocess.run(
            ["sh", str(DOCKER / "build-vpp.sh")],
            env={**self.env, **settings},
            capture_output=True,
            text=True,
        )

    def calls(self):
        return (
            [json.loads(line) for line in self.log.read_text().splitlines()]
            if self.log.exists()
            else []
        )

    def test_reuse_needs_no_checkout_and_builds_only_bundle(self):
        result = self.run_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        self.assertEqual(len(calls), 1)
        self.assertIn(str(DOCKER / "Dockerfile.vpp"), calls[0])
        self.assertNotIn("--build-context", calls[0])

    def test_git_refs_and_repositories_forwarded_without_local_context(self):
        result = self.run_build(
            BUILD_BASES="1",
            VPP_REF="abc123",
            FRR_REF="frr-custom",
            VPP_REPOSITORY="https://example.invalid/vpp.git",
            FRR_REPOSITORY="https://example.invalid/frr.git",
            VPP_IMAGE="test/vpp:custom",
            FRR_IMAGE="test/frr:custom",
            AGENT_IMAGE="test/agent:custom",
            SOURCE_CACHE_BUST="refresh-1",
            BUILD_JOBS="3",
            ALPINE_VERSION="3.22",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        snort, frr, vpp, bundle = self.calls()
        for call, component, ref in (
            (frr, "FRR", "frr-custom"),
            (vpp, "VPP", "abc123"),
        ):
            self.assertIn(f"{component}_SOURCE_MODE=git", call)
            self.assertIn(f"{component}_REF={ref}", call)
            self.assertIn(
                f"{component}_REPOSITORY=https://example.invalid/{component.lower()}.git",
                call,
            )
            self.assertIn("SOURCE_CACHE_BUST=refresh-1", call)
            self.assertIn("BUILD_JOBS=3", call)
            self.assertNotIn("--build-context", call)
        self.assertIn("VPP_IMAGE=test/vpp:custom", bundle)
        self.assertIn("FRR_IMAGE=test/frr:custom", bundle)
        self.assertIn("test/agent:custom", bundle)

    def test_default_git_build_selects_sdn_vpp_branch_and_frr_tag(self):
        result = self.run_build(BUILD_BASES="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        snort, frr, vpp, bundle = self.calls()
        self.assertIn("SNORT_REF=3.12.2.0", snort)
        self.assertIn("SNORT_IMAGE=alpine/snort:sdn-base", bundle)
        self.assertIn("FRR_REF=frr-10.7.0", frr)
        self.assertIn("VPP_REF=sdn/v26.06", vpp)
        self.assertIn("VPP_REPOSITORY=https://github.com/nanfengnan1/vpp.git", vpp)
        self.assertIn("FRR_REPOSITORY=https://github.com/nanfengnan1/frr.git", frr)
        self.assertIn("VPP_SOURCE_MODE=git", vpp)
        self.assertNotIn("--build-context", vpp)
        self.assertIn("VPP_IMAGE=alpine/vpp:sdn-base", bundle)

    def test_local_paths_with_spaces_are_single_arguments(self):
        source = self.root / "local checkout"
        source.mkdir()
        (source / ".git").mkdir()
        sentinel = source / "keep.txt"
        sentinel.write_text("uncommitted work")
        result = self.run_build(
            BUILD_BASES="1", VPP_SOURCE=str(source), FRR_SOURCE=str(source)
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        snort, frr, vpp, _ = self.calls()
        for call, component in ((frr, "FRR"), (vpp, "VPP")):
            self.assertIn(f"{component}_SOURCE_MODE=local", call)
            self.assertIn(f"{component.lower()}_source={source}", call)
            self.assertIn(f"{component.lower()}_git={source}/.git", call)
        self.assertEqual(sentinel.read_text(), "uncommitted work")

    def test_mixed_sources_are_independent(self):
        result = self.run_build(
            BUILD_BASES="1", VPP_SOURCE=str(self.root), FRR_REF="frr-10.7.0"
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        snort, frr, vpp, _ = self.calls()
        self.assertIn("FRR_SOURCE_MODE=git", frr)
        self.assertIn("VPP_SOURCE_MODE=local", vpp)

    def test_local_source_without_git_history_fails_before_build(self):
        source = self.root / "archive-only"
        source.mkdir()
        result = self.run_build(BUILD_BASES="1", VPP_SOURCE=str(source))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("full Git checkout", result.stderr)
        self.assertEqual(self.calls(), [])

    def test_invalid_input_fails_before_any_build(self):
        for settings in (
            {"BUILD_BASES": "yes"},
            {"BUILD_JOBS": "00"},
            {"BUILD_JOBS": "-2"},
            {
                "BUILD_BASES": "1",
                "VPP_REF": "main",
                "FRR_SOURCE": str(self.root / "absent"),
            },
            {"BUILD_BASES": "1", "VPP_SOURCE": str(self.root / "absent")},
        ):
            with self.subTest(settings=settings):
                result = self.run_build(**settings)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.calls(), [])

    def test_failed_base_build_does_not_continue(self):
        result = self.run_build(BUILD_BASES="1", VPP_REF="main", DOCKER_EXIT="23")
        self.assertEqual(result.returncode, 23)
        self.assertEqual(len(self.calls()), 1)


class SourceFixture(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sdn source tests ")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.repo = self.root / "origin"
        self.repo.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        files = {
            "extras/sdn-agent/adapter.py": "# fixture\n",
            "extras/sdn-agent/irb.py": "# fixture\n",
            "extras/sdn-agent/keepalive.py": "# fixture\n",
            "src/plugins/neighbor_keepalive/neighbor_keepalive.c": "/* fixture */\n",
            "src/plugins/neighbor_keepalive/neighbor_keepalive.api": "neighbor_keepalive_set neighbor_keepalive_status neighbor_keepalive_dump\n",
            "src/vpp-api/python/vpp_papi/__init__.py": "# fixture\n",
            "src/plugins/vxlan/multipoint.c": "/* fixture */\n",
            "src/plugins/vxlan/vxlan.api": "vxlan_mp_remote_add_del_v2 vxlan_mp_remote_v2_dump vxlan_set_encap_vrf\n",
            "src/vnet/l2/l2.api": "l2_egress_flood\n",
            "src/vnet/ip6-nd/ip6_nd.c": "ip_neighbor_confirm_dp\n",
            "build/external/packages/dpdk.mk": "dpdk_version := 26.03\n",
            "bootstrap.sh": "#!/bin/sh\n",
            "configure.ac": "# fixture\n",
            "tools/etc/frr/daemons": "bgpd=yes\n",
            "docker/alpine/docker-start": "#!/bin/sh\n",
        }
        for name, content in files.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        self.git("add", ".")
        self.git("commit", "-qm", "fixture source")
        self.git("tag", "-a", "v26.06", "-m", "fixture baseline")
        self.first = self.git("rev-parse", "HEAD").strip()
        (self.repo / "next.txt").write_text("second commit")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture next")
        self.head = self.git("rev-parse", "HEAD").strip()
        self.git("branch", "sdn/v26.06")

    def git(self, *args):
        return subprocess.check_output(
            ["git", "-C", str(self.repo), *args], stderr=subprocess.PIPE, text=True
        )

    def fetch(self, ref, destination):
        return subprocess.run(
            [
                "sh",
                str(DOCKER / "vpp/fetch-source.sh"),
                str(self.repo),
                ref,
                str(destination),
            ],
            capture_output=True,
            text=True,
        )


class SourceFetchTests(SourceFixture):
    def test_tag_branch_and_commit_select_actual_requested_revision(self):
        for index, (ref, expected) in enumerate(
            (
                ("v26.06", self.first),
                ("main", self.head),
                ("sdn/v26.06", self.head),
                (self.first, self.first),
            )
        ):
            with self.subTest(ref=ref):
                dest = self.root / f"checkout {index}"
                result = self.fetch(ref, dest)
                self.assertEqual(result.returncode, 0, result.stderr)
                actual = subprocess.check_output(
                    ["git", "-C", str(dest), "rev-parse", "HEAD"], text=True
                ).strip()
                self.assertEqual(actual, expected)
                subprocess.run(
                    ["git", "-C", str(dest), "describe", "--long", "--match", "v*"],
                    check=True,
                    capture_output=True,
                )
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.head)

    def test_unknown_ref_does_not_silently_use_default_branch(self):
        dest = self.root / "bad-ref"
        result = self.fetch("does-not-exist", dest)
        self.assertNotEqual(result.returncode, 0)
        head = subprocess.run(
            ["git", "-C", str(dest), "rev-parse", "--verify", "HEAD"],
            capture_output=True,
        )
        self.assertNotEqual(head.returncode, 0)

    def test_nonempty_destination_is_preserved(self):
        result = self.fetch("main", self.repo)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("destination must be empty", result.stderr)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.head)


class SourceMetadataTests(SourceFixture):
    def test_tracks_actual_commit_and_uncommitted_source(self):
        result = metadata.inspect_source(self.repo, "vpp", "fixture", "main")
        self.assertEqual(result["commit"], self.head)
        self.assertFalse(result["dirty"])
        (self.repo / "new-adaptation.py").write_text("local change")
        (self.repo / "extras/sdn-agent/adapter.py").write_text("# modified\n")
        result = metadata.inspect_source(self.repo, "vpp", "fixture", "label-only")
        self.assertEqual(result["commit"], self.head)
        self.assertTrue(result["dirty"])
        self.assertIn("new-adaptation.py", result["untrackedSha256"])
        self.assertEqual(len(result["untrackedSha256"]["new-adaptation.py"]), 64)

    def test_rejects_source_without_required_api(self):
        (self.repo / "src/plugins/vxlan/vxlan.api").write_text("upstream API only")
        with self.assertRaisesRegex(ValueError, "vxlan_mp_remote_add_del_v2"):
            metadata.inspect_source(self.repo, "vpp", "fixture", "main")

    def test_rejects_source_without_adapter(self):
        (self.repo / "extras/sdn-agent/adapter.py").unlink()
        with self.assertRaisesRegex(ValueError, "missing required files"):
            metadata.inspect_source(self.repo, "vpp", "fixture", "main")

    def test_frr_metadata_does_not_require_vpp_api(self):
        (self.repo / "src/plugins/vxlan/vxlan.api").unlink()
        result = metadata.inspect_source(self.repo, "frr", "fixture", "main")
        self.assertEqual(result["component"], "frr")
        self.assertEqual(result["commit"], self.head)

    def test_cli_writes_provenance_and_patch(self):
        (self.repo / "bootstrap.sh").write_text("#!/bin/sh\n# local fix\n")
        output = self.root / "metadata"
        result = subprocess.run(
            [
                sys.executable,
                str(DOCKER / "vpp/source-metadata.py"),
                "frr",
                str(self.repo),
                str(output),
                "fixture",
                "main",
            ],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            json.loads((output / "source.json").read_text())["commit"], self.head
        )
        self.assertEqual((output / "source-commit").read_text().strip(), self.head)
        self.assertIn("# local fix", (output / "source.patch").read_text())


if __name__ == "__main__":
    unittest.main()
