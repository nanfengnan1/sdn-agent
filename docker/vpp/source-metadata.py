#!/usr/bin/env python3
"""Reject incompatible source before compilation and record its actual identity."""

import argparse
import hashlib
import json
from pathlib import Path
import subprocess


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args])


def inspect_source(root, component, repository, ref):
    root = Path(root)
    required = {
        "vpp": (
            "extras/sdn-agent/adapter.py",
            "extras/sdn-agent/irb.py",
            "src/vpp-api/python/vpp_papi/__init__.py",
            "src/plugins/vxlan/multipoint.c",
            "src/plugins/vxlan/vxlan.api",
            "src/vnet/l2/l2.api",
            "build/external/packages/dpdk.mk",
        ),
        "frr": (
            "bootstrap.sh",
            "configure.ac",
            "tools/etc/frr/daemons",
            "docker/alpine/docker-start",
        ),
    }[component]
    missing = [name for name in required if not (root / name).is_file()]
    if missing:
        raise ValueError(
            f"{component} source is missing required files: {', '.join(missing)}. Select a committed compatible ref, or use local source mode."
        )
    if component == "vpp":
        for name, markers in {
            "src/plugins/vxlan/vxlan.api": (
                "vxlan_mp_remote_add_del_v2",
                "vxlan_mp_remote_v2_dump",
            ),
            "src/vnet/l2/l2.api": ("l2_egress_flood",),
        }.items():
            text = (root / name).read_text()
            for marker in markers:
                if marker not in text:
                    raise ValueError(
                        f"VPP source lacks required API {marker}; unmodified upstream VPP cannot build the SDN bundle"
                    )
    commit = git(root, "rev-parse", "HEAD").decode().strip()
    # Include untracked source identity: local adaptation files may not yet be committed.
    untracked = (
        git(root, "ls-files", "--others", "--exclude-standard", "-z")
        .decode()
        .split("\0")
    )
    hashes = {
        name: hashlib.sha256((root / name).read_bytes()).hexdigest()
        for name in untracked
        if name and (root / name).is_file()
    }
    dirty = bool(git(root, "status", "--porcelain", "--untracked-files=normal"))
    description = (
        git(root, "describe", "--tags", "--always", "--dirty").decode().strip()
    )
    if component == "vpp":
        git(root, "describe", "--long", "--match", "v*")
    return {
        "component": component,
        "repository": repository,
        "requestedRef": ref,
        "commit": commit,
        "describe": description,
        "dirty": dirty,
        "untrackedSha256": hashes,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("component", choices=("vpp", "frr"))
    parser.add_argument("root", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("repository")
    parser.add_argument("ref")
    args = parser.parse_args()
    result = inspect_source(args.root, args.component, args.repository, args.ref)
    args.output.mkdir(parents=True, exist_ok=True)
    (args.output / "source.json").write_text(json.dumps(result, indent=2) + "\n")
    (args.output / "source-commit").write_text(result["commit"] + "\n")
    (args.output / "source.patch").write_bytes(
        git(args.root, "diff", "--binary", "HEAD")
    )
    print(
        f'{args.component}: {result["describe"]} ({result["commit"]}), dirty={result["dirty"]}'
    )


if __name__ == "__main__":
    main()
