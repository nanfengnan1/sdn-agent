"""EVPN and TAP acceptance checks for the prepared physical VF lab."""

import json


def op(code, action, key, **data):
    return {"code": code, "action": action, key: data}


def submit(lab, name, ops, label):
    for i, operation in enumerate(ops):
        operation["id"] = str(i)
        if i:
            operation["depends_on"] = [str(i - 1)]
    path = lab.out / (label + ".json")
    path.write_text(json.dumps({"operations": ops}, indent=2) + "\n")
    lab.run("docker", "cp", path, name + ":/tmp/operations.json")
    lab.de(
        name,
        "sdnctl",
        "-insecure",
        "-timeout",
        "60s",
        "-file",
        "/tmp/operations.json",
        "submit",
    )


def network(bridge, vx, vni, local, mac):
    return [
        op("BD", "CREATE", "bridge", name=bridge, stp=False, mac=mac),
        op("BD_TENANT", "BIND", "binding", bridge=bridge, tenant="TENANT-A"),
        op("INTERFACE", "SET", "interface", name=bridge, up=True, addrgenmode="none"),
        op(
            "VXLAN",
            "CREATE",
            "vxlan",
            name=vx,
            vni=vni,
            local=local,
            underlay="uplink",
            port=4789,
            learning=False,
        ),
        op(
            "BD_PORT",
            "BIND",
            "bridge_port",
            bridge=bridge,
            port=vx,
            learning=False,
            neigh_suppress=True,
        ),
        op("INTERFACE", "SET", "interface", name=vx, up=True, addrgenmode="none"),
    ]


def ping(lab, pc, address, count=2):
    return (
        lab.de(
            pc,
            "ping",
            *(["-6"] if ":" in address else []),
            "-c",
            str(count),
            "-W",
            "1",
            address,
            check=False,
        ).returncode
        == 0
    )


def acceptance(lab, family):
    lab.report["vtepFamily"] = family
    spine = lab.nodes["spine"]
    spine_ops = [
        op(
            "BGP_INSTANCE",
            "CREATE",
            "bgp",
            asn=65000,
            router_id="192.0.2.254",
            default_ipv4_unicast=False,
        )
    ]
    for i in (1, 2):
        peer = f"fd80:{3100+i}::2" if family == 6 else f"192.0.2.{(i-1)*2}"
        spine_ops += [
            op("BGP_NEIGHBOR", "CREATE", "peer", asn=65000, name=peer, remote_as=65000),
            op(
                "BGP_EVPN_PEER",
                "SET",
                "peer",
                asn=65000,
                name=peer,
                active=True,
                rr_client=True,
            ),
        ]
    submit(lab, spine, spine_ops, "spine-bgp")
    for i in (1, 2):
        leaf, pc = lab.nodes[f"leaf{i}"], lab.pcs[f"leaf{i}"]
        for port in (0, 1):
            lab.cli(
                leaf,
                f"create tap id {10+port} host-if-name eth{port} host-ns /run/netns/pc host-mac-addr 02:00:00:10:0{port}:0{i} gso",
            )
            lab.cli(leaf, f"set interface state tap{10+port} up")
            lab.cli(leaf, f"lcp create tap{10+port} host-if access{port}")
            lab.de(pc, "ip", "link", "set", f"eth{port}", "up")
        lab.report["checks"].append(
            f"pc{i} has only VPP TAP access, no Docker data interface"
        )
        links = json.loads(lab.de(pc, "ip", "-j", "link").stdout)
        assert {link["ifname"] for link in links} == {"lo", "eth0", "eth1"}
        seg = i * 10
        local = f"fd80:{3100+i}::2" if family == 6 else f"192.0.2.{(i-1)*2}"
        peer = f"fd80:{3100+i}::1" if family == 6 else f"192.0.2.{(i-1)*2+1}"
        router_id = f"192.0.2.{10+i}"
        ops = [
            op("INTERFACE", "SET", "interface", name="access0", up=True),
            op("INTERFACE", "SET", "interface", name="access1", up=True),
            op("TENANT", "CREATE", "tenant", name="TENANT-A", table_id=1001),
            op("INTERFACE", "SET", "interface", name="TENANT-A", up=True),
        ]
        for bridge, vx, vni, mac in (
            ("br50000", "vx50000", 50000, f"02:00:00:00:ff:0{i}"),
            (f"br{seg}", f"vx{seg}", 10000 + seg, f"02:00:00:00:{seg:02x}:01"),
            ("br100", "vx100", 10100, f"02:00:00:00:64:0{i}"),
        ):
            ops += network(bridge, vx, vni, local, mac)
        ops += [
            op(
                "ADDRESS",
                "CREATE",
                "address",
                interface=f"br{seg}",
                prefix=f"192.168.{seg}.254/24",
            ),
            op(
                "ADDRESS",
                "CREATE",
                "address",
                interface=f"br{seg}",
                prefix=f"2001:db8:{seg}::fe/64",
            ),
            op(
                "BD_PORT",
                "BIND",
                "bridge_port",
                bridge=f"br{seg}",
                port="access0",
                learning=True,
            ),
            op(
                "BD_PORT",
                "BIND",
                "bridge_port",
                bridge="br100",
                port="access1",
                learning=True,
            ),
            op("TENANT_L3VNI", "BIND", "l3vni", tenant="TENANT-A", vni=50000),
            op(
                "BGP_INSTANCE",
                "CREATE",
                "bgp",
                asn=65000,
                router_id=router_id,
                default_ipv4_unicast=False,
            ),
            op("BGP_NEIGHBOR", "CREATE", "peer", asn=65000, name=peer, remote_as=65000),
            op("BGP_EVPN_PEER", "SET", "peer", asn=65000, name=peer, active=True),
            op(
                "BGP_EVPN_GLOBAL",
                "SET",
                "evpn_global",
                asn=65000,
                advertise_all_vni=True,
                advertise_svi_ip=True,
            ),
        ]
        for vni in (10000 + seg, 10100):
            ops.append(
                op(
                    "EVPN_L2VNI",
                    "SET",
                    "evpn",
                    asn=65000,
                    vni=vni,
                    rd=f"{router_id}:{vni}",
                    import_rts=[f"65000:{vni}"],
                    export_rts=[f"65000:{vni}"],
                )
            )
        ops += [
            op(
                "BGP_INSTANCE",
                "CREATE",
                "bgp",
                asn=65000,
                vrf="TENANT-A",
                router_id=router_id,
                default_ipv4_unicast=False,
            ),
            op(
                "EVPN_TENANT",
                "SET",
                "evpn",
                asn=65000,
                vrf="TENANT-A",
                rd=f"{router_id}:50000",
                import_rts=["65000:1"],
                export_rts=["65000:1"],
            ),
        ]
        submit(lab, leaf, ops, f"leaf{i}-evpn")
        for interface, address in (
            ("eth0", f"192.168.{seg}.{10+i}/24"),
            ("eth0", f"2001:db8:{seg}::{10+i}/64"),
            ("eth1", f"198.18.100.{i}/24"),
            ("eth1", f"2001:db8:100::{i}/64"),
        ):
            lab.de(pc, "ip", "addr", "add", address, "dev", interface)
        lab.de(pc, "ip", "route", "add", "192.168.0.0/16", "via", f"192.168.{seg}.254")
        lab.de(
            pc,
            "ip",
            "-6",
            "route",
            "add",
            "2001:db8::/32",
            "via",
            f"2001:db8:{seg}::fe",
        )
        lab.wait(f"PC{i} TAP IPv4 gateway", lambda: ping(lab, pc, f"192.168.{seg}.254"))
        lab.wait(
            f"PC{i} TAP IPv6 gateway", lambda: ping(lab, pc, f"2001:db8:{seg}::fe")
        )
    pc1, pc2 = lab.pcs["leaf1"], lab.pcs["leaf2"]
    l1, l2 = lab.nodes["leaf1"], lab.nodes["leaf2"]
    for label, pc, target in (
        ("L2VNI IPv4 PC1-PC2 via VF Spine", pc1, "198.18.100.2"),
        ("L2VNI IPv6 PC1-PC2 via VF Spine", pc1, "2001:db8:100::2"),
        ("IRB IPv4 PC1-PC2 via VF Spine", pc1, "192.168.20.12"),
        ("IRB IPv4 PC2-PC1 via VF Spine", pc2, "192.168.10.11"),
        ("IRB IPv6 PC1-PC2 via VF Spine", pc1, "2001:db8:20::12"),
        ("IRB IPv6 PC2-PC1 via VF Spine", pc2, "2001:db8:10::11"),
    ):
        lab.wait(label, lambda pc=pc, target=target: ping(lab, pc, target))
    lab.wait(
        "EVPN Type2 remote host in native VPP tenant FIB",
        lambda: "192.168.20.12/32" in lab.cli(l1, "show ip fib table 1001"),
    )
    for name in (l1, l2, spine):
        for command in (
            "show bgp l2vpn evpn summary json",
            "show bgp l2vpn evpn route json",
        ):
            value = lab.de(name, "vtysh", "-c", command).stdout
            (
                lab.out
                / (
                    name.rsplit("-", 1)[-1]
                    + "-"
                    + ("bgp-summary" if "summary" in command else "bgp-routes")
                    + ".json"
                )
            ).write_text(value)
    # Native VF subinterface down must break business traffic while Docker
    # management connectivity and both PC TAPs remain present.
    lab.cli(spine, "set interface state vf0.3102 down")
    try:
        assert not ping(lab, pc1, "198.18.100.2")
        assert not ping(lab, pc1, "192.168.20.12")
        assert not ping(lab, pc1, "2001:db8:20::12")
        lab.report["checks"].append("Spine VF link down blocks L2 and dual-stack IRB")
    finally:
        lab.cli(spine, "set interface state vf0.3102 up")
    lab.wait(
        "EVPN traffic recovers after Spine VF restoration",
        lambda: ping(lab, pc1, "198.18.100.2")
        and ping(lab, pc1, "192.168.20.12")
        and ping(lab, pc1, "2001:db8:20::12"),
    )
    for role in ("leaf1", "leaf2", "spine"):
        for kind in (
            "INTERFACES",
            "ADDRESSES",
            "TENANTS",
            "BRIDGES",
            "FDB",
            "ROUTES",
            "NEIGHBORS",
            "VXLAN_TUNNELS",
            "PORTS",
        ):
            value = lab.de(
                lab.nodes[role], "sdnctl", "-insecure", "-kind", kind, "query"
            ).stdout
            (lab.out / (role + "-query-" + kind.lower() + ".json")).write_text(value)
        lab.report["checks"].append(role + " native Agent queries")
