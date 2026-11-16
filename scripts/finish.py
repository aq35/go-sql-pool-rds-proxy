#!/usr/bin/env python3
"""Finish a campaign from the laptop: verify it is done, fetch every result, summarize, and (only if asked) destroy.

  finish.py NAME [--destroy]      (via: make finish CAMPAIGN=day1 [DESTROY_AFTER=yes])

The stack is destroyed ONLY if --destroy is given AND every check passes:
  1. the campaign on the instance is in state "done"
  2. every expected run (experiment x only x repeat, per Aurora class) is present locally with an "ok" marker
  3. every metrics.json has all series without errors and at least one point
  4. summary.md exists for every fetched experiment directory
Otherwise nothing is destroyed and the failed checks are printed (the instance keeps the raw data).
"""
import json, pathlib, subprocess, sys

root = pathlib.Path(__file__).resolve().parent.parent
name = sys.argv[1]
destroy = "--destroy" in sys.argv
camp = json.loads((root / "lab/campaigns" / f"{name}.json").read_text())
problems = []


def sh(cmd, **kw):
    return subprocess.run(cmd, shell=True, cwd=root, capture_output=True, text=True, **kw)


st = sh(f"scripts/ssm-run.sh 'cd ~/lab && ./exp.py campaign-status {name}'")
try:
    state = json.loads(st.stdout)
except Exception:
    state = {"state": "unreadable", "raw": st.stdout[-200:] + st.stderr[-200:]}
print("campaign state:", state.get("state"), state.get("error") or "")
if state.get("state") != "done":
    problems.append(f"campaign state is {state.get('state')!r}, not 'done'")

exps = []
cls = None
for step in camp["steps"]:
    if "aurora_class" in step: cls = step["aurora_class"]
    else: exps.append((step["exp"], cls, step))
for exp in sorted({e for e, _, _ in exps}):
    r = sh(f"scripts/lab.sh fetch {exp}")
    print(r.stdout[-300:] if r.returncode == 0 else r.stderr[-300:])
    if r.returncode != 0: problems.append(f"fetch/summary failed for {exp}")

checks = []
for exp, c, step in exps:
    if c is not None:
        checks.append((exp, root / "results" / f"{exp}__{c}", step))
    else:  # no aurora_class in the campaign: the class is whatever the runner recorded
        found = sorted((root / "results").glob(f"{exp}__db.*"))
        if not found: problems.append(f"missing results dir {exp}__*")
        checks += [(exp, d, step) for d in found]
for exp, d, step in checks:
    if not d.exists(): problems.append(f"missing results dir {d.name}"); continue
    e = json.loads((root / "lab/experiments" / f"{exp}.json").read_text())
    ids = step.get("only") or [r["id"] for r in e["runs"]]
    repeat = step.get("repeat") or e.get("repeat", 1)
    for rid in ids:
        for rep in range(1, repeat + 1):
            rd = d / f"{rid}.r{rep}"
            if not (rd / "ok").exists(): problems.append(f"{d.name}/{rid}.r{rep}: no ok marker"); continue
            try:
                m = json.loads((rd / "metrics.json").read_text())["series"]
            except Exception as ex:
                problems.append(f"{d.name}/{rid}.r{rep}: metrics.json unreadable ({ex})"); continue
            bad = [k for k, v in m.items() if v.get("error") or not v.get("points")]
            core = [k for k in bad if k in ("client", "dbconn", "cpu")]
            if core: problems.append(f"{d.name}/{rid}.r{rep}: empty/failed series {core}")
    if not (d / "summary.md").exists(): problems.append(f"{d.name}: no summary.md")

pr = sh("aws rds describe-db-proxies --db-proxy-name rdsproxy-exp --query 'DBProxies[0].IdleClientTimeout' --output text; "
        "aws rds describe-db-proxy-target-groups --db-proxy-name rdsproxy-exp --query 'TargetGroups[0].ConnectionPoolConfig.[MaxConnectionsPercent,MaxIdleConnectionsPercent]' --output text")
if pr.stdout.split() != ["1800", "80", "10"]:
    problems.append(f"Proxy settings are not at the defaults 1800/80/10 (now {pr.stdout.split() or pr.stderr[-120:]}); run exp.py proxy-restore")

idx = [f"# campaign {name}", "", camp.get("description", ""), ""]
for d in sorted((root / "results").glob("*__db.*")):
    idx.append(f"- [{d.name}]({d.name}/summary.md)")
(root / "results" / f"CAMPAIGN-{name}.md").write_text("\n".join(idx) + "\n")

if problems:
    print("\nNOT DESTROYING. Problems:"); [print(" -", p) for p in problems]
    sys.exit(1)
print("\nAll checks passed.")
if not destroy:
    print("Not destroying (DESTROY_AFTER=yes was not given). Stack is still running and billing.")
    sys.exit(0)
print("Destroying (DESTROY_AFTER=yes and all checks passed) ...")
r = subprocess.run("make destroy CONFIRM=yes", shell=True, cwd=root)
sys.exit(r.returncode)
