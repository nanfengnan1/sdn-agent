#!/usr/bin/env python3
"""Isolated, real Docker EVPN lab. Requires Linux, Docker, Python 3; no pip packages."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
STATE = ROOT / "lab/state"
ART = ROOT / "artifacts"
PREFIX = "sdna-e2e"
LABEL = "sdn-agent.lab=sdna-e2e"
FRR = "quay.io/frrouting/frr:10.0.0"
NET = PREFIX + "-mgmt"

def run(*args, check=True, timeout=120, **kwargs):
    p = subprocess.run([str(x) for x in args], text=True, capture_output=True, timeout=timeout, **kwargs)
    if check and p.returncode:
        raise RuntimeError(f"{' '.join(str(x) for x in args)}\n{p.stdout}\n{p.stderr}")
    return p

def docker(*args, **kw):
    return run("docker", *args, **kw)

def cname(n):
    return PREFIX + "-" + n

def exe(n, *args, **kw):
    return docker("exec", cname(n), *args, **kw)

def cleanup():
    # Only containers carrying our exact ownership label may be removed.
    ids = docker("ps", "-aq", "--filter", "label=" + LABEL).stdout.split()
    if ids:
        docker("rm", "-f", *ids)
    info = docker("network", "inspect", NET, check=False)
    if info.returncode == 0 and json.loads(info.stdout)[0].get("Labels", {}).get("sdn-agent.lab") == PREFIX:
        docker("network", "rm", NET)

def container(n, image, *args, extra=()):
    return docker("run", "-d", "--name", cname(n), "--label", LABEL,
                  "--network", NET, "--network-alias", n, *extra, image, *args).stdout.strip()

def wire(a, an, b, bn, idx):
    pa = docker("inspect", "-f", "{{.State.Pid}}", cname(a)).stdout.strip()
    pb = docker("inspect", "-f", "{{.State.Pid}}", cname(b)).stdout.strip()
    x, y = f"sdna{idx}a", f"sdna{idx}b"
    # Helper has its own network namespace, with host PID visibility for nsenter.
    exe("helper", "ip", "link", "add", x, "type", "veth", "peer", "name", y)
    exe("helper", "ip", "link", "set", x, "netns", pa)
    exe("helper", "ip", "link", "set", y, "netns", pb)
    exe("helper", "nsenter", "-t", pa, "-n", "ip", "link", "set", x, "name", an)
    exe("helper", "nsenter", "-t", pb, "-n", "ip", "link", "set", y, "name", bn)

def build_ops(node):
    ops = []
    def add(code, data, value, action="SET", deps=None):
        ident = f"op{len(ops):03d}"
        # A chain is a valid DAG; explicit dependencies make initial provisioning deterministic.
        dep = ([ops[-1]["id"]] if ops else []) if deps is None else deps
        ops.append(dict(id=ident, code=code, action=action, depends_on=dep, **{data:value}))
    add("NODE_NETWORK", "network", {"settings":[
        dict(key=k,value=v,optional=k.startswith("net.bridge")) for k,v in [
            ("net.ipv4.conf.all.rp_filter","0"),("net.ipv4.conf.default.rp_filter","0"),
            ("net.ipv6.conf.all.disable_ipv6","1"),("net.ipv6.conf.default.disable_ipv6","1"),
            ("net.ipv4.ip_forward","1"),
            ("net.bridge.bridge-nf-call-iptables","0"),("net.bridge.bridge-nf-call-ip6tables","0"),
            ("net.bridge.bridge-nf-call-arptables","0")]]})
    add("INTERFACE","interface",dict(name="lo",up=True))
    leaf = node.startswith("leaf")
    i = int(node[-1]) if leaf else 0
    rid = f"10.255.0.{10+i if leaf else 1}"
    add("ADDRESS","address",dict(interface="lo",prefix=rid+"/32"),"CREATE")
    ports = [("uplink",f"10.0.{i}.2/30")] if leaf else [("swp1","10.0.1.1/30"),("swp2","10.0.2.1/30")]
    for p,ip in ports:
        add("INTERFACE","interface",dict(name=p,mtu=9000,up=True))
        add("ADDRESS","address",dict(interface=p,prefix=ip),"CREATE")
    if leaf:
        seg=i*10; bd=f"br{seg}"; host=f"host{seg}"; l2=10000+seg
        add("INTERFACE","interface",dict(name=host,mtu=1500,up=True))
        add("TENANT","tenant",dict(name="TENANT-A",table_id=1001),"CREATE")
        add("INTERFACE","interface",dict(name="TENANT-A",up=True))
        for bridge,mac in [(bd,f"02:00:00:00:00:{seg}"),("br50000",f"02:00:00:00:ff:{10+i}")]:
            add("BD","bridge",dict(name=bridge,stp=False,mtu=1500,mac=mac),"CREATE")
            add("BD_TENANT","binding",dict(bridge=bridge,tenant="TENANT-A"),"BIND")
            add("INTERFACE","interface",dict(name=bridge,addrgenmode="none",up=True))
        add("ADDRESS","address",dict(interface=bd,prefix=f"192.168.{seg}.254/24"),"CREATE")
        add("BD_PORT","bridge_port",dict(bridge=bd,port=host),"BIND")
        for v,bridge in [(l2,bd),(50000,"br50000")]:
            vx=f"vx{v}"
            add("VXLAN","vxlan",dict(name=vx,vni=v,local=rid,port=4789,learning=False),"CREATE")
            add("BD_PORT","bridge_port",dict(bridge=bridge,port=vx,learning=False,neigh_suppress=True),"BIND")
            add("INTERFACE","interface",dict(name=vx,mtu=1500,addrgenmode="none",up=True))
        add("TENANT_L3VNI","l3vni",dict(tenant="TENANT-A",vni=50000),"BIND")
    add("OSPF_INTERFACE","ospf_interface",dict(name="lo",passive=True))
    for p,_ in ports:
        add("OSPF_INTERFACE","ospf_interface",dict(name=p,network_type="point-to-point"))
    add("OSPF_INSTANCE","ospf",dict(router_id=rid),"CREATE")
    for prefix in [rid+"/32"]+([f"10.0.{i}.0/30"] if leaf else ["10.0.1.0/30","10.0.2.0/30"]):
        add("OSPF_NETWORK","ospf_network",dict(prefix=prefix,area="0"),"CREATE")
    add("BGP_INSTANCE","bgp",dict(asn=65000,router_id=rid,default_ipv4_unicast=False),"CREATE")
    if not leaf:
        add("BGP_PEER_GROUP","peer",dict(asn=65000,name="LEAF",remote_as=65000,update_source="lo"),"CREATE")
        for peer in ["10.255.0.11","10.255.0.12"]:
            add("BGP_NEIGHBOR","peer",dict(asn=65000,name=peer,peer_group="LEAF"),"CREATE")
        add("BGP_EVPN_PEER","peer",dict(asn=65000,name="LEAF",active=True,rr_client=True))
    else:
        add("BGP_NEIGHBOR","peer",dict(asn=65000,name="10.255.0.1",remote_as=65000,update_source="lo"),"CREATE")
        add("BGP_EVPN_PEER","peer",dict(asn=65000,name="10.255.0.1",active=True))
        add("BGP_EVPN_GLOBAL","evpn_global",dict(asn=65000,advertise_all_vni=True,advertise_svi_ip=True))
        add("EVPN_L2VNI","evpn",dict(asn=65000,vni=l2,rd=f"{rid}:{seg}",import_rts=[f"65000:{l2}"],export_rts=[f"65000:{l2}"]))
        add("BGP_INSTANCE","bgp",dict(asn=65000,vrf="TENANT-A",router_id=rid,default_ipv4_unicast=False),"CREATE")
        add("EVPN_TENANT","evpn",dict(asn=65000,vrf="TENANT-A",rd=f"{rid}:50000",import_rts=["65000:1"],export_rts=["65000:1"]))
    return dict(operations=ops)

def ctl(n, command, *args, check=True):
    p = exe(n,"/workspace/bin/sdnctl","-insecure","-addr","127.0.0.1:9090",*args,command,check=check,timeout=240)
    return p

def submit(n, data, suffix="config", check=True):
    path=STATE/f"{n}-{suffix}.json";path.write_text(json.dumps(data,indent=2))
    p=ctl(n,"submit","-file",f"/workspace/lab/state/{path.name}",check=False)
    (ART/f"{n}-{suffix}-result.json").write_text(p.stdout)
    if check and p.returncode:
        raise RuntimeError(f"{n}/{suffix}: {p.stdout}\n{p.stderr}")
    return p

def wait_for(fn, message, seconds=80):
    deadline=time.monotonic()+seconds;last=""
    while time.monotonic()<deadline:
        try:
            if fn(): return
        except Exception as e: last=str(e)
        time.sleep(1)
    raise RuntimeError(f"timeout: {message}\n{last}")

def start_agent(n):
    exe(n,"sh","-c",f"/workspace/bin/sdn-agent -insecure -controller controller:9010 -endpoint {n}:9090 -redis redis:6379 -state-dir /workspace/lab/state/{n}/agent > /workspace/lab/state/{n}/agent.log 2>&1 & echo $! > /workspace/lab/state/{n}/agent.pid")
    wait_for(lambda: ctl(n,"capabilities","-timeout","2s",check=False).returncode==0,f"agent {n}",30)

def setup():
    if docker("ps","-aq","--filter","label="+LABEL).stdout.strip():
        raise RuntimeError("Lab exists. Use --cleanup first (only our labelled containers are removed).")
    STATE.mkdir(parents=True,exist_ok=True);ART.mkdir(exist_ok=True)
    (STATE/"go.mod").write_text("module lab-runtime-state\n\ngo 1.25.0\n")
    token=secrets.token_hex(24);(STATE/"token").write_text(token)
    docker("network","create","--label",LABEL,NET)
    # The lab has a separate AOF directory; existing Redis/services are never touched.
    stamp=str(time.time_ns()); redisdir=STATE/f".redis-{stamp}";redisdir.mkdir()
    container("redis","redis:7.4-alpine","redis-server","--appendonly","yes","--appendfsync","always",extra=("-v",f"{redisdir}:/data"))
    mount=("-v",f"{ROOT}:/workspace","-e",f"SDN_TOKEN={token}")
    container("controller",FRR,"/workspace/bin/mock-controller","-insecure","-state",f"/workspace/lab/state/controller-{stamp}.json",extra=(*mount,"--entrypoint",""))
    container("helper",FRR,"sleep","infinity",extra=("--privileged","--pid=host","--entrypoint",""))
    base_daemons=docker("run","--rm","--entrypoint","cat",FRR,"/etc/frr/daemons").stdout
    for n in ["spine","leaf1","leaf2"]:
        d=STATE/n;d.mkdir(exist_ok=True)
        # New unique state directory per setup avoids accidentally reusing the old journal.
        agentdir=d/"agent"
        if agentdir.exists(): agentdir.rename(d/f"agent-previous-{stamp}")
        daemons=base_daemons.replace("bgpd=no","bgpd=yes").replace("ospfd=no","ospfd=yes")
        conf=d/f"frr-{stamp}";conf.mkdir();os.chmod(conf,0o777)
        (conf/"daemons").write_text(daemons)
        (conf/"frr.conf").write_text(f"frr defaults traditional\nhostname {n}\nlog stdout informational\nservice integrated-vtysh-config\n")
        (conf/"vtysh.conf").write_text("service integrated-vtysh-config\n")
        os.chmod(conf/"frr.conf",0o666)
        container(n,FRR,extra=(*mount,"--privileged","-v",f"{conf}:/etc/frr"))
    for n in ["pc1","pc2"]:
        container(n,FRR,"sleep","infinity",extra=("--privileged","--entrypoint",""))
    wire("spine","swp1","leaf1","uplink",1);wire("spine","swp2","leaf2","uplink",2)
    wire("leaf1","host10","pc1","eth1",3);wire("leaf2","host20","pc2","eth1",4)
    # End hosts are test fixtures; fabric configuration is exclusively sent through agent RPCs.
    for i in [1,2]:
        n=f"pc{i}";seg=i*10
        exe(n,"ip","link","set","eth1","address",f"02:00:00:{seg}:00:{10+i}","mtu","1500","up")
        exe(n,"ip","address","add",f"192.168.{seg}.{10+i}/24","dev","eth1")
        exe(n,"ip","route","replace","192.168.0.0/16","via",f"192.168.{seg}.254","dev","eth1")
        exe(n,"sysctl","-w","net.ipv4.conf.all.rp_filter=0")
    for n in ["spine","leaf1","leaf2"]:
        start_agent(n)
        print(f"Provision {n} through gRPC",flush=True)
        submit(n,build_ops(n))

def verify():
    results=[]
    def passed(name): results.append(dict(name=name,status="PASS"));print("PASS",name,flush=True)
    def established():
        s=json.loads(exe("spine","vtysh","-c","show bgp l2vpn evpn summary json").stdout)
        peers=s.get("peers",s.get("l2VpnEvpn",{}).get("peers",{}))
        return len(peers)==2 and all(p.get("state")=="Established" for p in peers.values())
    wait_for(established,"two EVPN BGP sessions",100);passed("EVPN BGP sessions")
    # Pure Type-2 IRB needs both silent hosts to reveal their IP/MAC locally.
    # Generate ordinary host traffic; this is not a static route/FDB workaround.
    for i in [1,2]:
        wait_for(lambda i=i: exe(f"pc{i}","ping","-c","1","-W","1",f"192.168.{i*10}.254",check=False).returncode==0,f"PC{i} gateway",30)
    passed("host ARP learning on both access networks")
    wait_for(lambda: exe("pc1","ping","-c","2","-W","1","192.168.20.12",check=False).returncode==0,"cross-subnet ping",100)
    passed("PC1 -> PC2 across L2VNI 10010 / L3VNI 50000 / L2VNI 10020")
    assert exe("pc2","ping","-c","3","-W","1","192.168.10.11").returncode==0;passed("PC2 -> PC1")
    routes=exe("spine","vtysh","-c","show bgp l2vpn evpn").stdout
    assert "[3]:" in routes and "[2]:" in routes and "192.168.10.11" in routes and "192.168.20.12" in routes
    (ART/"spine-evpn.txt").write_text(routes);passed("Type-2 MAC/IP and Type-3 IMET at spine")
    for n in ["leaf1","leaf2"]:
        vni=exe(n,"vtysh","-c","show evpn vni").stdout
        assert "50000" in vni and "L3" in vni
        (ART/f"{n}-vni.txt").write_text(vni)
    passed("L3VNI recognised by zebra")
    for n in ["spine","leaf1","leaf2"]:
        (ART/f"{n}-state.json").write_text(ctl(n,"state").stdout)
        (ART/f"{n}-running.conf").write_text(exe(n,"vtysh","-c","show running-config").stdout)
    # Real partial failure: create a bridge, then operate on a nonexistent interface.
    revision=json.loads(ctl("leaf1","state").stdout).get("revision","0")
    p=submit("leaf1",dict(operations=[dict(id="a",code="BD",action="CREATE",bridge=dict(name="br-rollback",stp=False)),dict(id="b",code="INTERFACE",action="SET",depends_on=["a"],interface=dict(name="missing0",up=True))]),"rollback",check=False)
    assert p.returncode!=0 and json.loads(p.stdout)["state"]=="FAILED_ROLLED_BACK",p.stdout+p.stderr
    assert exe("leaf1","ip","link","show","br-rollback",check=False).returncode!=0
    assert json.loads(ctl("leaf1","state").stdout).get("revision","0")==revision
    passed("failed transaction removes bridge and preserves Running revision")
    # Existing MTU is restored, not reset to an arbitrary default.
    p=submit("leaf1",dict(operations=[dict(id="a",code="INTERFACE",action="SET",interface=dict(name="uplink",mtu=8900)),dict(id="b",code="INTERFACE",action="SET",depends_on=["a"],interface=dict(name="missing0",up=True))]),"restore-mtu",check=False)
    assert json.loads(p.stdout)["state"]=="FAILED_ROLLED_BACK",p.stdout+p.stderr
    assert json.loads(exe("leaf1","ip","-j","link","show","uplink").stdout)[0]["mtu"]==9000
    passed("rollback restores pre-existing MTU")
    # FRR compensation against a real running BGP/EVPN instance.
    p=submit("leaf1",dict(operations=[dict(id="a",code="EVPN_L2VNI",action="SET",evpn=dict(asn=65000,vni=10010,rd="10.255.0.11:99")),dict(id="b",code="INTERFACE",action="SET",depends_on=["a"],interface=dict(name="missing0",up=True))]),"restore-frr",check=False)
    assert json.loads(p.stdout)["state"]=="FAILED_ROLLED_BACK",p.stdout+p.stderr
    assert "rd 10.255.0.11:10" in exe("leaf1","vtysh","-c","show running-config").stdout
    passed("FRR RD rollback and readback")
    # Create and remove a separate tenant/BD/VXLAN using reverse dependencies.
    create=[dict(id="a",code="TENANT",action="CREATE",tenant=dict(name="TENANT-T",table_id=1099)),
            dict(id="b",code="BD",action="CREATE",depends_on=["a"],bridge=dict(name="br-test",stp=False)),
            dict(id="c",code="BD_TENANT",action="BIND",depends_on=["b"],binding=dict(bridge="br-test",tenant="TENANT-T")),
            dict(id="d",code="VXLAN",action="CREATE",depends_on=["c"],vxlan=dict(name="vx-test",vni=10999,local="10.255.0.11",port=4789)),
            dict(id="e",code="BD_PORT",action="BIND",depends_on=["d"],bridge_port=dict(bridge="br-test",port="vx-test",learning=False,neigh_suppress=True))]
    submit("leaf1",dict(operations=create),"create-delete-fixture")
    remove=[]
    for original in reversed(create):
        o=dict(original);o["action"]="UNBIND" if original["action"]=="BIND" else "DELETE"
        o["depends_on"]=[remove[-1]["id"]] if remove else [];remove.append(o)
    submit("leaf1",dict(operations=remove),"delete-fixture")
    for name in ["TENANT-T","br-test","vx-test"]:
        assert exe("leaf1","ip","link","show",name,check=False).returncode!=0
    passed("create/delete tenant, bridge, VXLAN and detach dependencies")
    # Restart the Agent process while Linux, FRR and Redis stay alive.
    before=json.loads(ctl("leaf1","state").stdout)
    pid=exe("leaf1","cat","/workspace/lab/state/leaf1/agent.pid").stdout.strip()
    assert pid.isdecimal() and int(pid)>1
    assert exe("leaf1","readlink",f"/proc/{pid}/exe").stdout.strip()=="/workspace/bin/sdn-agent"
    exe("leaf1","kill","-9",pid)
    start_agent("leaf1")
    after=json.loads(ctl("leaf1","state").stdout)
    assert before["node_id"]==after["node_id"] and before["revision"]==after["revision"]
    assert before["instance_id"]!=after["instance_id"]
    passed("agent process restart preserves UUID, Running revision and Redis journal")
    old_task=json.loads((ART/"leaf1-config-result.json").read_text())["id"]
    assert json.loads(ctl("leaf1","task","-task",old_task).stdout)["state"]=="SUCCEEDED"
    passed("task query after process restart")
    docker("stop",cname("controller"))
    wait_for(lambda: json.loads(ctl("leaf1","state").stdout)["node_state"]=="DOWN","management DOWN",20)
    wait_for(lambda: exe("pc1","ping","-c","2","-W","1","192.168.20.12",check=False).returncode==0,"forwarding after compensation",30)
    passed("controller disconnection preserves forwarding")
    docker("start",cname("controller"));wait_for(lambda: json.loads(ctl("leaf1","state").stdout)["node_state"]=="RUNNING","management reconnect",30)
    passed("controller reconnect / identity persistence")
    (ART/"lab-results.json").write_text(json.dumps(results,indent=2))
    print(f"{len(results)} lab checks passed. Lab retained; cleanup: python3 lab/run.py --cleanup",flush=True)

def main():
    p=argparse.ArgumentParser();p.add_argument("--cleanup",action="store_true");p.add_argument("--verify-only",action="store_true");a=p.parse_args()
    if a.cleanup: cleanup();return
    try:
        if not a.verify_only: setup()
        verify()
    except Exception:
        ART.mkdir(exist_ok=True)
        for n in ["spine","leaf1","leaf2"]:
            (ART/f"{n}-docker.log").write_text(docker("logs",cname(n),check=False).stdout)
        raise

if __name__=="__main__": main()
