#!/usr/bin/env python3
"""Finish a campaign from the laptop: verify it is done, fetch every result, summarize, and (only if asked) destroy.

  finish.py NAME [--destroy]      (via: make finish CAMPAIGN=day1 [DESTROY_AFTER=yes])

Two different questions, kept apart:
  EVIDENCE COMPLETE (gates --destroy): every expected run was fetched with everything needed to judge it later.
  VALID SUCCESS (reported, never deleted): the measurement itself was valid (rc 0, target reached, baseline met, required series present).
The stack is destroyed ONLY if --destroy is given AND the evidence is complete:
  1. the campaign on the instance ended ("done" or "done_with_failures": failures are reported, not hidden)
  2. every expected run (experiment x only x repeat, per Aurora class) is present locally with ok + meta.json (rc, manifest) + valid.json + client.log
  3. every REQUIRED series of the experiment ("required_metrics" in its definition; default client, dbconn, cpu; "os_cpu" = per-second Aurora CPU) is present and non-empty
  4. summary.md exists for every fetched experiment directory
An invalid run (failed, target not reached, baseline unmet) is evidence too: it is kept and listed, and does not block the destroy.
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
if state.get("state") not in ("done", "done_with_failures"):
    problems.append(f"campaign state is {state.get('state')!r}, not 'done' / 'done_with_failures'")
if state.get("problems"): print("campaign reported problems (kept as evidence):", state["problems"])

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
validity = {}
for exp, d, step in checks:
    if not d.exists(): problems.append(f"missing results dir {d.name}"); continue
    e = json.loads((root / "lab/experiments" / f"{exp}.json").read_text())
    ids = step.get("only") or [r["id"] for r in e["runs"]]
    repeat = step.get("repeat") or e.get("repeat", 1)
    required = e.get("required_metrics", ["client", "dbconn", "cpu"])
    for rid in ids:
        for rep in range(1, repeat + 1):
            rd = d / f"{rid}.r{rep}"; tag = f"{d.name}/{rid}.r{rep}"
            if not (rd / "ok").exists(): problems.append(f"{tag}: no ok marker (run did not complete)"); continue
            miss = [f for f in ("meta.json", "valid.json", "client.log", "metrics.json") if not (rd / f).exists()]
            if miss: problems.append(f"{tag}: missing files {miss}"); continue
            meta = json.loads((rd / "meta.json").read_text()); val = json.loads((rd / "valid.json").read_text())
            if "rc" not in meta or "manifest" not in meta: problems.append(f"{tag}: meta.json has no rc / manifest")
            if any(x.startswith("infra_mismatch") for x in val["reasons"]):
                validity.setdefault(exp, []).append((tag, val))   # deliberately not run on this stack; the reason is the evidence
                continue
            try:
                m = json.loads((rd / "metrics.json").read_text())["series"]
            except Exception as ex:
                problems.append(f"{tag}: metrics.json unreadable ({ex})"); continue
            for k in required:
                if k == "os_cpu":
                    try: o = json.loads((rd / "os_cpu.json").read_text())
                    except Exception: o = {"points": [], "error": "missing os_cpu.json"}
                    if o.get("error") or not o.get("points"):
                        problems.append(f"{tag}: required per-second Aurora CPU (os_cpu) missing: {o.get('error')}  -> exp.py recollect {exp} (while the stack is up)")
                elif not m.get(k) or m[k].get("error") or not m[k].get("points"):
                    problems.append(f"{tag}: required series {k!r} empty/failed")
            validity.setdefault(exp, []).append((tag, val))
    if not (d / "summary.md").exists(): problems.append(f"{d.name}: no summary.md")

print("\nVALID SUCCESS (measurement validity; invalid runs are kept as evidence):")
for exp_name, rows in validity.items():
    bad = [(t, v["reasons"]) for t, v in rows if not v["valid"]]
    print(f"  {exp_name}: {len(rows) - len(bad)}/{len(rows)} valid runs")
    for t, r in bad: print(f"    INVALID {t}: {', '.join(r)}")

def stack_output(key):
    r = sh(f"source scripts/lib.sh && out {key}", executable="/bin/bash")
    v = r.stdout.strip()
    return "" if v in ("", "None") else v


# The Proxy name and the deploy-time debug-logging setting come from the stack (envId can change the name); not hard-coded.
proxy_name = stack_output("ProxyName")
if not proxy_name:
    problems.append("the stack has no ProxyName output (is it still deployed?): cannot check that the Proxy settings were restored")
else:
    want_debug = stack_output("DebugLoggingAtDeploy").lower() == "true"
    pr = sh(f"aws rds describe-db-proxies --db-proxy-name {proxy_name} --query 'DBProxies[0].[IdleClientTimeout,DebugLogging]' --output text; "
            f"aws rds describe-db-proxy-target-groups --db-proxy-name {proxy_name} --query 'TargetGroups[0].ConnectionPoolConfig.[MaxConnectionsPercent,MaxIdleConnectionsPercent]' --output text")
    got = pr.stdout.split()
    if len(got) != 4 or got[0] != "1800" or got[1].lower() != str(want_debug).lower() or got[2:] != ["80", "10"]:
        problems.append(f"Proxy settings are not what the stack was deployed with (idle timeout 1800, debug logging {want_debug}, pool 80/10); "
                        f"now {got or pr.stderr[-120:]}; run exp.py proxy-restore")

# Enhanced Monitoring writes to the shared, AWS-managed log group RDSOSMetrics (1-second interval: a lot of log volume). It is not part of
# the stack and is NOT deleted with it. Report what is there; deleting anything stays a human decision.
lg = sh("aws logs describe-log-groups --log-group-name-prefix RDSOSMetrics --query 'logGroups[?logGroupName==`RDSOSMetrics`].[retentionInDays,storedBytes]' --output text")
if lg.stdout.strip():
    ret, stored = (lg.stdout.split() + ["?", "?"])[:2]
    print(f"\nNOTE RDSOSMetrics (shared, not deleted with the stack): retention {ret if ret != 'None' else 'never expires'} days, {stored} bytes stored. "
          "After the stack is destroyed, this stack's log stream remains until its retention ends; "
          "delete it only if nothing else uses this account's Enhanced Monitoring: aws logs delete-log-stream --log-group-name RDSOSMetrics --log-stream-name <DbiResourceId>")

idx = [f"# campaign {name}", "", camp.get("description", ""), ""]
for d in sorted((root / "results").glob("*__db.*")):
    idx.append(f"- [{d.name}]({d.name}/summary.md)")
(root / "results" / f"CAMPAIGN-{name}.md").write_text("\n".join(idx) + "\n")

if problems:
    print("\nNOT DESTROYING. Problems:"); [print(" -", p) for p in problems]
    sys.exit(1)
print("\nEvidence complete (all checks passed).")
if not destroy:
    print("Not destroying (DESTROY_AFTER=yes was not given). Stack is still running and billing.")
    sys.exit(0)
print("Destroying (DESTROY_AFTER=yes and all checks passed) ...")
r = subprocess.run("make destroy CONFIRM=yes", shell=True, cwd=root)
sys.exit(r.returncode)
