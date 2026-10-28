#!/usr/bin/env python3
"""Fresh adapter process restores saved nonzero table; old daemon must be paused."""
import sys
sys.path.insert(0, "/usr/local/lib/sdn-agent/vpp")
from adapter import VPP, command, PREFIX
from irb import IRBEngine
api = VPP("/run/vpp/api.sock", "/usr/share/vpp/api")
engine = IRBEngine(api, "/var/lib/sdn-agent/vpp.json")
assert not engine.paused
objects = engine.model["vxlans"]
assert objects and all(x["encapVrfId"] == 100 for x in objects.values())
indices = {r["tag"].removeprefix(PREFIX+"vx/").removesuffix("/mp"): i
           for i, r in api.snapshot()[0].items()
           if r.get("tag", "").startswith(PREFIX+"vx/")}
for name in objects:
    api.call("vxlan_set_encap_vrf", sw_if_index=indices[name], encap_vrf_id=0)
    command("ip", "link", "set", "dev", name, "type", "vxlan", "dev", "lo")
engine.restore_mirrors()
engine.reconcile()
for name in objects:
    engine.verify({"code": "VXLAN", "action": "SET",
                   "vxlan": {"name": name, "encapVrfId": 100}})
    assert api.snapshot()[2][indices[name]]["encap_vrf_id"] == 100
api.client.disconnect()
print("PASS fresh-process persisted-state restoration, Linux mirror and native VPP")
