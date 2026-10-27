#!/usr/bin/env python3
"""Prepare/restore one explicitly selected PF for a disposable VF lab.

This changes host PCI bindings. The state file is written before each change;
restore it after the lab, including after a failed VF/PMD probe. No global prune,
unsafe VFIO mode, firmware changes, management NIC changes or PCI wildcard binds.
"""

import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import time

IMAGE = "alpine/sdn-agent:vpp-dev"
SYS = Path("/sys/bus/pci/devices")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def interrupted(signum, _frame):
    raise SystemExit(f"interrupted by signal {signum}; restoring saved host state")


def run(*args, check=True):
    result = subprocess.run(
        list(map(str, args)), text=True, capture_output=True, timeout=120
    )
    if check and result.returncode:
        raise RuntimeError(result.stderr + result.stdout)
    return result.stdout.strip()


def host(code, *args):
    return run(
        "docker",
        "run",
        "--rm",
        "--privileged",
        "--network",
        "host",
        "-v",
        "/sys:/sys:rw",
        "--entrypoint",
        "python3",
        IMAGE,
        "-c",
        code,
        *args,
    )


def host_ip(*args):
    return run(
        "docker",
        "run",
        "--rm",
        "--privileged",
        "--network",
        "host",
        "--entrypoint",
        "ip",
        IMAGE,
        *args,
    )


def host_command(*args):
    return run(
        "docker",
        "run",
        "--rm",
        "--privileged",
        "--pid",
        "host",
        "--network",
        "host",
        "--entrypoint",
        "nsenter",
        IMAGE,
        "-t",
        "1",
        "-m",
        "-u",
        "-i",
        "-n",
        "-p",
        "--",
        *args,
    )


def save(path, state):
    path = Path(path)
    tmp = path.with_suffix(".tmp")
    with tmp.open("w") as stream:
        stream.write(json.dumps(state, indent=2) + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    tmp.replace(path)


def bind(pci, driver):
    require(
        re.fullmatch(r"[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]", pci),
        "invalid explicit PCI address",
    )
    require(driver in ("vfio-pci", "hinic3", "hisdk3"), "unexpected PCI driver")
    host(
        """from pathlib import Path
import sys
p=Path('/sys/bus/pci/devices')/sys.argv[1]; driver=sys.argv[2]
if (p/'driver').exists():
 if (p/'driver').resolve().name==driver:sys.exit(0)
 (p/'driver/unbind').write_text(p.name)
(p/'driver_override').write_text(driver)
Path('/sys/bus/pci/drivers_probe').write_text(p.name)
assert (p/'driver').exists() and (p/'driver').resolve().name==driver, 'PCI probe did not bind '+p.name+' to '+driver
""",
        pci,
        driver,
    )


def prepare(path, pci, owner):
    path = Path(path)
    if path.exists() and json.loads(path.read_text()).get("status") != "restored":
        raise RuntimeError("state already exists; restore the previous lab first")
    p = SYS / pci
    require(p.exists() and not (p / "physfn").exists(), "select a PF, not a VF")
    require((p / "vendor").read_text().strip() == "0x19e5", "expected Huawei PF")
    require(
        int((p / "sriov_numvfs").read_text()) == 0,
        "existing VFs are not owned by this lab",
    )
    require(
        int((p / "sriov_totalvfs").read_text()) >= 3,
        "PF must provide at least three VFs",
    )
    require(
        (p / "driver").resolve().name == "vfio-pci",
        "this lab expects a PF already owned by VFIO/VPP",
    )
    management = json.loads(run("ip", "-j", "route", "get", "10.8.126.1"))[0]
    require(
        Path("/sys/class/net", management["dev"], "device").resolve() != p.resolve(),
        "refusing the management PF",
    )
    container = json.loads(run("docker", "inspect", owner))[0]
    require(container["State"]["Running"], "expected original PF owner to be running")
    group = (p / "iommu_group").resolve().name
    require(
        any(
            d["PathOnHost"] == "/dev/vfio/" + group
            for d in container["HostConfig"]["Devices"]
        ),
        "original container does not own the selected VFIO group",
    )
    require(
        len(list((p / "iommu_group" / "devices").iterdir())) == 1,
        "PF IOMMU group is shared",
    )
    state = dict(
        status="preparing",
        pci=pci,
        originalDriver=(p / "driver").resolve().name,
        originalOverride=(p / "driver_override").read_text().strip(),
        originalAutoprobe=(p / "sriov_drivers_autoprobe").read_text().strip(),
        owner=owner,
        restartOwner=True,
        management=management,
        vfs=[],
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    save(path, state)
    try:
        run("docker", "stop", "-t", "30", owner)
        # Huawei's out-of-tree package registers the PCI driver as hisdk3;
        # hinic3 is its NIC upper-layer module, not the PCI driver name.
        kernel_driver = (
            "hisdk3" if Path("/sys/bus/pci/drivers/hisdk3").exists() else "hinic3"
        )
        bind(pci, kernel_driver)
        for _ in range(30):
            ports = list((p / "net").glob("*"))
            if ports:
                break
            time.sleep(1)
        require(len(ports) == 1, "expected one PF netdev")
        state["netdev"] = ports[0].name
        save(path, state)
        if run("systemctl", "is-active", "NetworkManager", check=False) == "active":
            state["nmManaged"] = run(
                "nmcli", "-g", "GENERAL.NM-MANAGED", "device", "show", ports[0].name
            )
            save(path, state)
            host_command("nmcli", "device", "set", ports[0].name, "managed", "no")
        # VF probes are explicitly controlled; PF stays with hinic3 for mailbox
        # and VLAN/trust management while VF data paths use vfio-pci.
        host(
            """from pathlib import Path
import sys
p=Path('/sys/bus/pci/devices')/sys.argv[1]
(p/'sriov_drivers_autoprobe').write_text('0')
(p/'sriov_numvfs').write_text('3')
""",
            pci,
        )
        host_ip("link", "set", state["netdev"], "up")
        for i, vlan in enumerate((3101, 0, 3102)):
            vf = (p / f"virtfn{i}").resolve()
            require(
                vf.exists() and (vf / "physfn").resolve() == p.resolve(),
                "VF has an unexpected parent",
            )
            group_path = (vf / "iommu_group").resolve()
            require(group_path.exists(), "VF has no IOMMU group")
            members = list((group_path / "devices").iterdir())
            require(len(members) == 1, "VF is not independently isolated")
            mac = f"02:00:00:73:00:{i + 1:02x}"
            row = dict(
                index=i,
                pci=vf.name,
                group=group_path.name,
                mac=mac,
                vlan=vlan,
                role=("leaf1", "spine", "leaf2")[i],
            )
            state["vfs"].append(row)
            save(path, state)
            host_ip("link", "set", "dev", state["netdev"], "vf", str(i), "mac", mac)
            host_ip("link", "set", "dev", state["netdev"], "vf", str(i), "trust", "on")
            host_ip(
                "link", "set", "dev", state["netdev"], "vf", str(i), "spoofchk", "off"
            )
            host_ip(
                "link", "set", "dev", state["netdev"], "vf", str(i), "vlan", str(vlan)
            )
            bind(vf.name, "vfio-pci")
        state["pfInfo"] = json.loads(
            host_ip("-j", "-d", "link", "show", state["netdev"])
        )
        after = json.loads(run("ip", "-j", "route", "get", "10.8.126.1"))[0]
        require(
            after["dev"] == management["dev"]
            and after["prefsrc"] == management["prefsrc"],
            "management route changed during VF preparation",
        )
        state["status"] = "ready"
        save(path, state)
        return state
    except BaseException as error:
        try:
            restore(path)
        except Exception as rollback_error:
            state["status"] = "restore-required"
            state["prepareError"] = str(error)
            state["restoreError"] = str(rollback_error)
            save(path, state)
            raise RuntimeError(
                f"prepare failed: {error}; restore failed: {rollback_error}; use {path}"
            ) from error
        raise


def restore(path):
    path = Path(path)
    state = json.loads(path.read_text())
    if state["status"] == "restored":
        return state
    pci, p = state["pci"], SYS / state["pci"]
    # Call only after all lab VPP processes have released their VF devices.
    # Detaching a VF still open by a process is intentionally not forced here.
    users = run("docker", "ps", "--format", "{{.Names}}")
    if any(name.startswith("sdn-vf-lab-") for name in users.splitlines()):
        raise RuntimeError("remove owned VF lab containers before restoring the PF")
    host(
        """from pathlib import Path
import sys
p=Path('/sys/bus/pci/devices')/sys.argv[1]
if int((p/'sriov_numvfs').read_text()):
 (p/'sriov_numvfs').write_text('0')
(p/'sriov_drivers_autoprobe').write_text(sys.argv[2])
""",
        pci,
        state["originalAutoprobe"],
    )
    if state.get("nmManaged") == "yes" and (p / "net" / state["netdev"]).exists():
        host_command("nmcli", "device", "set", state["netdev"], "managed", "yes")
    bind(pci, state["originalDriver"])
    original = state["originalOverride"]
    host(
        """from pathlib import Path
import sys
(Path('/sys/bus/pci/devices')/sys.argv[1]/'driver_override').write_text('\\n' if sys.argv[2] in ('', '(null)') else sys.argv[2])
""",
        pci,
        original,
    )
    if state["restartOwner"]:
        run("docker", "start", state["owner"])
    state["status"] = "restored"
    save(path, state)
    return state


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "restore"))
    parser.add_argument("state")
    parser.add_argument("--pci", default="0000:3b:00.0")
    parser.add_argument("--owner", default="vpp-26-06")
    args = parser.parse_args()
    result = (
        prepare(args.state, args.pci, args.owner)
        if args.action == "prepare"
        else restore(args.state)
    )
    print(json.dumps(result, indent=2))
