#!/usr/bin/env python3
"""Run the complete reproducible regression and keep command results."""
import json
from pathlib import Path
import subprocess
import sys
import time

ROOT=Path(__file__).resolve().parents[1]
ART=ROOT/"artifacts"

def main():
    ART.mkdir(exist_ok=True)
    commands=[("unit",["go","test","-count=1","-v","./..."]),
              ("runtime",[sys.executable,"-m","unittest","discover","-s","docker","-p","*_test.py","-v"]),
              ("race",["go","test","-race","-count=1","./..."]),
              ("vet",["go","vet","./..."]),
              ("build",["make","build"]),
              ("cleanup",["python3","lab/run.py","--cleanup"]),
              ("evpn",["python3","lab/run.py"])]
    results=[]
    for name,command in commands:
        print("RUN",name,flush=True);start=time.monotonic()
        with (ART/f"regression-{name}.log").open("w") as f:
            p=subprocess.run(command,cwd=ROOT,stdout=f,stderr=subprocess.STDOUT)
        results.append(dict(name=name,command=command,exit_code=p.returncode,seconds=round(time.monotonic()-start,2)))
        (ART/"regression-results.json").write_text(json.dumps(results,indent=2))
        if p.returncode:
            print((ART/f"regression-{name}.log").read_text());raise SystemExit(p.returncode)
        print("PASS",name,flush=True)
    print("All regressions passed; lab retained.",flush=True)

if __name__=="__main__":main()
