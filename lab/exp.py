#!/usr/bin/env python3
"""Experiment runner. Runs ON the EC2 instance (stdlib only). Usage (normally via `make`):

  exp.py list                         experiments and what they ask
  exp.py check                        validate every experiments/*.json (fields, scenario flags)
  exp.py run EXP [--repeat N] [--only id,id]     results: out/EXP__<aurora class>/
  exp.py campaign NAME                 run lab/campaigns/NAME.json (Aurora spec phases + experiments), resumable
  exp.py campaign-status NAME
  exp.py proxy-restore                 put the Proxy settings back (experiments with a "proxy" block change IdleClientTimeout etc.)
  exp.py status EXP
  exp.py pack EXP                     tar the results of EXP to out/EXP.tgz

For every run it (1) waits until Proxy DatabaseConnections is back at the floor, so a run never starts on the
leftovers of the previous one, (2) runs `run.sh --scenario=... args`, (3) waits `observe_after_s`,
(4) saves meta.json, client.log and metrics.json (CloudWatch, definitions in metrics.json) in out/EXP/<id>.r<rep>/.
Repetitions rotate the order of the runs, so a time-of-day effect is not tied to one condition.
"""
import argparse, datetime as dt, fcntl, json, os, pathlib, re, subprocess, sys, tarfile, time

HERE = pathlib.Path(__file__).resolve().parent
OUT = HERE / "out"
EXPS = HERE / "experiments"
REQUIRED = ["name", "question", "hypothesis", "reading", "scenario", "runs"]
FLOOR_DEFAULT = 6          # Proxy keeps ~5 backend connections with no clients (measured)
BASELINE_TIMEOUT_S = 20 * 60


def now(): return dt.datetime.now(dt.timezone.utc)
def iso(t): return t.strftime("%Y-%m-%dT%H:%M:%SZ")
def log(*a): print(iso(now()), *a, flush=True)


def load_env():
    p = pathlib.Path("/etc/exp.env")
    if p.exists():
        for line in p.read_text().splitlines():
            m = re.match(r"export (\w+)=(.*)", line)
            if m: os.environ[m.group(1)] = m.group(2)


def load_exp(name):
    p = EXPS / f"{name}.json"
    if not p.exists(): sys.exit(f"no experiment {name!r} (exp.py list)")
    return json.loads(p.read_text())


def cw_series(metric, start, end, instance=False, cluster=False):
    dim = ({"Name": "DBClusterIdentifier", "Value": os.environ["DB_CLUSTER_ID"]} if cluster
           else {"Name": "DBInstanceIdentifier", "Value": os.environ["WRITER_INSTANCE_ID"]} if instance
           else {"Name": "ProxyName", "Value": METRICS["proxy_name"]})
    q = [{"Id": "a", "MetricStat": {"Metric": {"Namespace": "AWS/RDS", "MetricName": metric["name"],
          "Dimensions": [dim]}, "Period": 60, "Stat": metric["stat"]}, "ReturnData": True}]
    r = subprocess.run(["aws", "cloudwatch", "get-metric-data", "--start-time", iso(start), "--end-time", iso(end),
                        "--metric-data-queries", json.dumps(q), "--scan-by", "TimestampAscending"],
                       capture_output=True, text=True)
    if r.returncode: raise RuntimeError(r.stderr[-300:])
    res = json.loads(r.stdout)["MetricDataResults"][0]
    return [[t, v] for t, v in zip(res["Timestamps"], res["Values"])]


def wait_baseline(floor):
    """Block until the last two 1-minute DatabaseConnections points are <= floor. Returns a record for meta.json."""
    dbm = next(m for m in METRICS["metrics"] if m["key"] == "dbconn")
    t0 = time.time(); last = None
    while time.time() - t0 < BASELINE_TIMEOUT_S:
        try:
            pts = cw_series(dbm, now() - dt.timedelta(minutes=8), now())
            last = [v for _, v in pts[-2:]]
            if len(last) == 2 and max(last) <= floor:
                return {"floor": floor, "last_points": last, "waited_s": int(time.time() - t0), "unmet": False}
        except Exception as e:
            log("baseline check failed:", e)
        time.sleep(30)
    log("BASELINE_UNMET after", BASELINE_TIMEOUT_S, "s; last points", last)
    return {"floor": floor, "last_points": last, "waited_s": int(time.time() - t0), "unmet": True}


def os_cpu(start, end):
    """Per-second OS CPU (percent) of the Aurora writer from Enhanced Monitoring (log group RDSOSMetrics, one event per second).
    Returns [[iso time, total, user, system, wait], ...]. The 1-minute AWS/RDS CPUUtilization cannot separate runs less than a minute apart."""
    r = subprocess.run(["aws", "rds", "describe-db-instances", "--db-instance-identifier", os.environ["WRITER_INSTANCE_ID"],
                        "--query", "DBInstances[0].DbiResourceId", "--output", "text"], capture_output=True, text=True)
    if r.returncode or not r.stdout.strip(): raise RuntimeError("no DbiResourceId: " + r.stderr[-200:])
    stream = r.stdout.strip()
    ms = lambda t: str(int(t.timestamp() * 1000))
    pts, token = [], None
    for _ in range(200):
        cmd = ["aws", "logs", "get-log-events", "--log-group-name", "RDSOSMetrics", "--log-stream-name", stream,
               "--start-time", ms(start), "--end-time", ms(end), "--start-from-head", "--output", "json"]
        if token: cmd += ["--next-token", token]
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode: raise RuntimeError(r.stderr[-300:])
        d = json.loads(r.stdout)
        for ev in d.get("events", []):
            try:
                m = json.loads(ev["message"]); c = m["cpuUtilization"]
                pts.append([m["timestamp"], c["total"], c.get("user"), c.get("system"), c.get("wait")])
            except Exception: pass
        nxt = d.get("nextForwardToken")
        if not nxt or nxt == token or not d.get("events"): break
        token = nxt
    return pts


def collect(run_dir, start, end):
    try:
        pts = os_cpu(start - dt.timedelta(seconds=10), end)
        (run_dir / "os_cpu.json").write_text(json.dumps({"points": pts, "error": None}))
    except Exception as e:
        (run_dir / "os_cpu.json").write_text(json.dumps({"points": [], "error": str(e)}))
    data = {}
    for kind, group in (("proxy", METRICS["metrics"]), ("instance", METRICS.get("instance_metrics", [])), ("cluster", METRICS.get("cluster_metrics", []))):
        for m in group:
            try: data[m["key"]] = {"metric": m["name"], "stat": m["stat"], "points": cw_series(m, start, end, kind == "instance", kind == "cluster")}
            except Exception as e: data[m["key"]] = {"metric": m["name"], "stat": m["stat"], "error": str(e), "points": []}
    (run_dir / "metrics.json").write_text(json.dumps({"window": [iso(start), iso(end)], "series": data}))


def write_status(exp_dir, **kw):
    p = exp_dir / "status.json"
    cur = json.loads(p.read_text()) if p.exists() else {}
    cur.update(kw, updated=iso(now()))
    p.write_text(json.dumps(cur, indent=1))


def aurora_class():
    r = subprocess.run(["aws", "rds", "describe-db-instances", "--db-instance-identifier", os.environ["WRITER_INSTANCE_ID"],
                        "--query", "DBInstances[0].[DBInstanceClass,DBInstanceStatus]", "--output", "text"], capture_output=True, text=True)
    if r.returncode: raise RuntimeError(r.stderr[-300:])
    cls, status = r.stdout.split()
    return cls, status


def ensure_class(target):
    """Resize the writer to `target` (e.g. db.r6g.xlarge) if needed and wait until it is available."""
    cls, status = aurora_class()
    if cls != target:
        log("RESIZE", cls, "->", target)
        subprocess.run(["aws", "rds", "modify-db-instance", "--db-instance-identifier", os.environ["WRITER_INSTANCE_ID"],
                        "--db-instance-class", target, "--apply-immediately"], check=True, capture_output=True)
        time.sleep(60)
    for _ in range(120):  # up to 60 min
        cls, status = aurora_class()
        if cls == target and status == "available":
            log("writer is", cls, "available; settling 120s"); time.sleep(120); return
        time.sleep(30)
    raise RuntimeError(f"writer did not reach {target} available (now {cls} {status})")


PROXY_DEFAULTS = {"IdleClientTimeout": 1800, "MaxConnectionsPercent": 80, "MaxIdleConnectionsPercent": 10}
RESTORE = OUT / "proxy.restore"


def proxy_read():
    n = METRICS["proxy_name"]
    idle = subprocess.run(["aws", "rds", "describe-db-proxies", "--db-proxy-name", n, "--query", "DBProxies[0].[IdleClientTimeout,Status]", "--output", "text"], capture_output=True, text=True, check=True).stdout.split()
    tg = json.loads(subprocess.run(["aws", "rds", "describe-db-proxy-target-groups", "--db-proxy-name", n, "--query", "TargetGroups[0].[ConnectionPoolConfig,Status]", "--output", "json"], capture_output=True, text=True, check=True).stdout)
    return {"IdleClientTimeout": int(idle[0]), "MaxConnectionsPercent": tg[0]["MaxConnectionsPercent"], "MaxIdleConnectionsPercent": tg[0]["MaxIdleConnectionsPercent"]}, idle[1] == "available" and tg[1] == "available"


def proxy_apply(want):
    """Set Proxy settings (subset of PROXY_DEFAULTS) and wait until they are in effect and the Proxy is available."""
    n = METRICS["proxy_name"]; cur, _ = proxy_read()
    if all(cur[k] == v for k, v in want.items()): return
    if not RESTORE.exists(): RESTORE.write_text(json.dumps(cur))    # first change in this session remembers the original
    full = dict(cur, **want)
    if "IdleClientTimeout" in want:
        subprocess.run(["aws", "rds", "modify-db-proxy", "--db-proxy-name", n, "--idle-client-timeout", str(full["IdleClientTimeout"])], check=True, capture_output=True)
    if "MaxConnectionsPercent" in want or "MaxIdleConnectionsPercent" in want:
        subprocess.run(["aws", "rds", "modify-db-proxy-target-group", "--db-proxy-name", n, "--target-group-name", "default", "--connection-pool-config",
                        f"MaxConnectionsPercent={full['MaxConnectionsPercent']},MaxIdleConnectionsPercent={full['MaxIdleConnectionsPercent']}"], check=True, capture_output=True)
    for _ in range(120):
        cur, avail = proxy_read()
        if avail and all(cur[k] == v for k, v in want.items()): log("proxy settings in effect:", want); time.sleep(30); return
        time.sleep(5)
    raise RuntimeError(f"proxy settings {want} not in effect after 10 min")


def proxy_restore():
    want = json.loads(RESTORE.read_text()) if RESTORE.exists() else PROXY_DEFAULTS
    proxy_apply(want)
    if RESTORE.exists(): RESTORE.unlink()
    log("proxy settings restored:", want)


def ordered(runs, rep):
    k = rep % len(runs)
    return runs[k:] + runs[:k]


LOCK = None


def take_lock():
    global LOCK
    if LOCK: return
    LOCK = open(OUT / ".lock", "w")
    try: fcntl.flock(LOCK, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError: sys.exit("another experiment is running (out/.lock)")


def cmd_run(a):
    take_lock()
    run_experiment(a.exp, a.repeat, a.only.split(",") if a.only else None)


def run_experiment(exp, repeat=0, only=None):
    """Runs the experiment on the CURRENT Aurora class; results go to out/<exp>__<class>/."""
    e = load_exp(exp)
    guess = [k for k, v in e.get("coeff_sources", {}).items() if str(v).startswith("default")]
    if guess and not os.environ.get("ALLOW_DEFAULT_COEFFS"):
        sys.exit(f"{exp}: coefficients {guess} are not measured. Run scripts/derive_coeffs.py + scripts/gen_validation.py first "
                 "(or ALLOW_DEFAULT_COEFFS=1 to run with guesses; the result is then not a validation).")
    runs = e["runs"]
    if only: runs = [r for r in runs if r["id"] in only]
    if not runs: sys.exit(f"no runs selected in {exp}")
    repeat = repeat or e.get("repeat", 1)
    cls, _ = aurora_class()
    exp_dir = OUT / f"{exp}__{cls}"; exp_dir.mkdir(parents=True, exist_ok=True)
    a = argparse.Namespace(exp=exp)
    prev = json.loads((exp_dir / "expected.json").read_text()) if (exp_dir / "expected.json").exists() else {"runs": [], "repeat": 0}
    (exp_dir / "expected.json").write_text(json.dumps({"runs": sorted(set(prev["runs"]) | {r["id"] for r in runs}),
                                                       "repeat": max(prev["repeat"], repeat)}))
    version = (HERE / "VERSION").read_text().strip() if (HERE / "VERSION").exists() else "unknown"
    (exp_dir / "experiment.json").write_text(json.dumps(e, indent=1, ensure_ascii=False))
    total = len(runs) * repeat; done = 0; failed = []
    write_status(exp_dir, state="running", total=total, done=0, current=None, failed=[], started=iso(now()))
    for rep in range(1, repeat + 1):
        for r in ordered(runs, rep - 1):
            rid = f"{r['id']}.r{rep}"; rdir = exp_dir / rid
            if (rdir / "ok").exists():
                done += 1; log("skip", rid, "(done)"); continue
            rdir.mkdir(exist_ok=True)
            write_status(exp_dir, current=rid, done=done)
            try:
                want = dict(e.get("proxy", {}), **r.get("proxy", {}))
                proxy_apply(want) if want else proxy_restore() if RESTORE.exists() else None
            except Exception as ex:
                log("proxy setting failed:", ex); failed.append(rid + ":proxy"); continue
            if e.get("pre_gap_s"): time.sleep(e["pre_gap_s"])    # >= 2 min keeps 1-minute CloudWatch points of neighbouring runs apart
            base = wait_baseline(e.get("floor", FLOOR_DEFAULT))
            args = [f"--scenario={e['scenario']}"] + e.get("common", []) + r.get("args", [])
            start = now(); log("START", rid, " ".join(args))
            env = dict(os.environ, SECRET=r.get("secret", e.get("secret", "admin")))
            with open(rdir / "client.log", "w") as f:
                rc = subprocess.run([str(HERE / "run.sh")] + args, stdout=f, stderr=subprocess.STDOUT, env=env, cwd=HERE).returncode
            run_end = now(); log("run finished", rid, "rc", rc)
            time.sleep(e.get("observe_after_s", 120))
            collect(rdir, start - dt.timedelta(minutes=3), now())
            (rdir / "meta.json").write_text(json.dumps({
                "experiment": a.exp, "aurora_class": cls, "run_id": r["id"], "rep": rep, "args": args, "git": version,
                "start": iso(start), "run_end": iso(run_end), "rc": rc, "baseline": base}, indent=1))
            if rc != 0 and rc != 3: failed.append(rid)
            (rdir / "ok").write_text("")
            done += 1
            write_status(exp_dir, done=done, failed=failed)
    if RESTORE.exists(): proxy_restore()      # a run changed Proxy settings: always leave the Proxy as we found it
    write_status(exp_dir, state="done" if not failed else "done_with_failures", current=None, done=done, finished=iso(now()))
    log("ALL DONE", a.exp, cls)


def cmd_status(a):
    ds = sorted(OUT.glob(f"{a.exp}__*"))
    for d in ds:
        p = d / "status.json"
        print(d.name, p.read_text() if p.exists() else "no status")
    if not ds: print("no status (not started)")


def cmd_pack(a):
    ds = sorted(OUT.glob(f"{a.exp}__*"))
    if not ds: sys.exit(f"no results for {a.exp}")
    with tarfile.open(OUT / f"{a.exp}.tgz", "w:gz") as t:
        for d in ds: t.add(d, arcname=d.name)
    print(OUT / f"{a.exp}.tgz")


CAMPAIGNS = HERE / "campaigns"


def cmd_campaign(a):
    """Run a campaign (lab/campaigns/NAME.json) on this instance: Aurora spec phases and experiments, in order. Resumable."""
    take_lock()
    c = json.loads((CAMPAIGNS / f"{a.name}.json").read_text())
    sp = OUT / f"campaign-{a.name}.json"
    st = {"state": "running", "step": 0, "steps": len(c["steps"]), "started": iso(now()), "error": None}
    sp.write_text(json.dumps(st))
    try:
        for i, step in enumerate(c["steps"]):
            st.update(step=i + 1, current=step, updated=iso(now())); sp.write_text(json.dumps(st))
            if "aurora_class" in step: ensure_class(step["aurora_class"])
            else: run_experiment(step["exp"], step.get("repeat", 0), step.get("only"))
        st.update(state="done", finished=iso(now()))
    except BaseException as ex:
        st.update(state="failed", error=repr(ex), updated=iso(now())); sp.write_text(json.dumps(st)); raise
    sp.write_text(json.dumps(st)); log("CAMPAIGN DONE", a.name)


def cmd_campaign_status(a):
    p = OUT / f"campaign-{a.name}.json"
    print(p.read_text() if p.exists() else json.dumps({"state": "not started"}))


def cmd_proxy_restore(_):
    load_env(); proxy_restore()


def cmd_list(_):
    for p in sorted(EXPS.glob("*.json")):
        e = json.loads(p.read_text())
        print(f"{e['name']:<18} runs={len(e['runs']):<3} {e['question']}")


def cmd_check(_):
    bad = 0
    flags = {}
    binary = HERE / "lab"
    for p in sorted(EXPS.glob("*.json")):
        e = json.loads(p.read_text())
        miss = [k for k in REQUIRED if not e.get(k)]
        if miss: print(f"{p.name}: missing {miss}"); bad += 1; continue
        if e["name"] != p.stem: print(f"{p.name}: name must equal the file name"); bad += 1
        ids = [r["id"] for r in e["runs"]]
        if len(ids) != len(set(ids)): print(f"{p.name}: duplicate run ids"); bad += 1
        if binary.exists():
            sc = e["scenario"]
            if sc not in flags:
                r = subprocess.run([str(binary), f"--scenario={sc}", "-h"], capture_output=True, text=True)
                flags[sc] = set(re.findall(r"^\s+-([\w-]+)", r.stderr + r.stdout, re.M))
            for r in e["runs"]:
                for arg in e.get("common", []) + r.get("args", []):
                    m = re.match(r"--?([\w-]+)", arg)
                    if m and m.group(1) not in flags[sc]:
                        print(f"{p.name}: run {r['id']}: flag --{m.group(1)} is not a flag of scenario {sc}"); bad += 1
    names = {p.stem: json.loads(p.read_text()) for p in EXPS.glob("*.json")}
    for p in sorted(CAMPAIGNS.glob("*.json")):
        c = json.loads(p.read_text())
        for st in c["steps"]:
            if "exp" in st:
                if st["exp"] not in names: print(f"{p.name}: unknown experiment {st['exp']}"); bad += 1; continue
                ids = {r["id"] for r in names[st["exp"]]["runs"]}
                for o in st.get("only", []):
                    if o not in ids: print(f"{p.name}: {st['exp']} has no run {o}"); bad += 1
            elif "aurora_class" not in st: print(f"{p.name}: step needs exp or aurora_class: {st}"); bad += 1
    print("experiments OK" if not bad else f"{bad} problem(s)")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    METRICS = json.loads((HERE / "metrics.json").read_text())
    ap = argparse.ArgumentParser(); sp = ap.add_subparsers(dest="cmd", required=True)
    sp.add_parser("list").set_defaults(f=cmd_list)
    sp.add_parser("check").set_defaults(f=cmd_check)
    sp.add_parser("proxy-restore").set_defaults(f=cmd_proxy_restore)
    for n, f in (("campaign", cmd_campaign), ("campaign-status", cmd_campaign_status)):
        s = sp.add_parser(n); s.add_argument("name"); s.set_defaults(f=f)
    for n, f in (("run", cmd_run), ("status", cmd_status), ("pack", cmd_pack)):
        s = sp.add_parser(n); s.add_argument("exp"); s.set_defaults(f=f)
        if n == "run": s.add_argument("--repeat", type=int, default=0); s.add_argument("--only", default="")
    a = ap.parse_args()
    if a.cmd in ("run", "campaign", "campaign-status", "status", "pack"): load_env(); OUT.mkdir(exist_ok=True)
    a.f(a)
