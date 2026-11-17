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
  exp.py recollect EXP [--force]      collect metrics again (only runs found missing; --force: every run), then re-judge

For every run it (1) waits until Proxy DatabaseConnections is back at the floor, so a run never starts on the
leftovers of the previous one, (2) runs `run.sh --scenario=... args`, (3) waits `observe_after_s`,
(4) saves meta.json, client.log and metrics.json (CloudWatch, definitions in metrics.json) in out/EXP/<id>.r<rep>/.
Repetitions rotate the order of the runs, so a time-of-day effect is not tied to one condition.
"""
import argparse, datetime as dt, fcntl, hashlib, json, os, pathlib, re, shutil, subprocess, sys, tarfile, time

HERE = pathlib.Path(__file__).resolve().parent
OUT = HERE / "out"
EXPS = HERE / "experiments"
REQUIRED = ["name", "question", "hypothesis", "reading", "scenario", "runs"]
FLOOR_DEFAULT = 10         # whole-Proxy DatabaseConnections (Sum) a run may start with = the idle pool the Proxy keeps once it has been used:
                           # MaxIdleConnectionsPercent 10% of max_connections 100, 5 per node on 2 nodes (steady for hours with no client;
                           # a never-used Proxy has 0). Above it, connections of the previous run are still there. docs/metrics.md
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


def cw_series(metric, start, end, instance=False, cluster=False, stat=None):
    dim = ({"Name": "DBClusterIdentifier", "Value": os.environ["DB_CLUSTER_ID"]} if cluster
           else {"Name": "DBInstanceIdentifier", "Value": os.environ["WRITER_INSTANCE_ID"]} if instance
           else {"Name": "ProxyName", "Value": proxy_name()})
    q = [{"Id": "a", "MetricStat": {"Metric": {"Namespace": "AWS/RDS", "MetricName": metric["name"],
          "Dimensions": [dim]}, "Period": 60, "Stat": stat or metric["stat"]}, "ReturnData": True}]
    r = subprocess.run(["aws", "cloudwatch", "get-metric-data", "--start-time", iso(start), "--end-time", iso(end),
                        "--metric-data-queries", json.dumps(q), "--scan-by", "TimestampAscending"],
                       capture_output=True, text=True)
    if r.returncode: raise RuntimeError(r.stderr[-300:])
    res = json.loads(r.stdout)["MetricDataResults"][0]
    return [[t, v] for t, v in zip(res["Timestamps"], res["Values"])]


def baseline_met(last, floor):
    """True if the last two 1-minute points exist and are both <= floor."""
    return len(last) == 2 and max(last) <= floor


def wait_baseline(floor):
    """Block until the last two 1-minute DatabaseConnections points of the WHOLE Proxy (statistic Sum) are <= floor. Returns a record for
    meta.json. The floor is the Proxy's idle pool (10 = 5 per node), which it keeps for hours with no client once it has been used: a
    stricter floor (2) was never reached and made every run "baseline_unmet" (2026-11-17). The old check (Maximum of one node <= 6) was
    equivalent for the default settings; Sum is the whole-Proxy value and is what the summaries report."""
    dbm = next(m for m in METRICS["metrics"] if m["key"] == "dbconn")
    t0 = time.time(); last = None
    while time.time() - t0 < BASELINE_TIMEOUT_S:
        try:
            pts = cw_series(dbm, now() - dt.timedelta(minutes=8), now(), stat="Sum")
            last = [v for _, v in pts[-2:]]
            if baseline_met(last, floor):
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
    """Save CloudWatch series (definitions in metrics.json) and per-second Aurora OS CPU. Gauges are also fetched with the other
    statistics (metrics.json "extra_stats"): AWS recommends Sum for the RDS Proxy connection gauges and publishes them per proxy
    instance, so Maximum may show only the busiest instance. Keys: <key> (the configured stat) and <key>__<Stat>."""
    try:
        pts = os_cpu(start - dt.timedelta(seconds=10), end)
        (run_dir / "os_cpu.json").write_text(json.dumps({"points": pts, "error": None}))
    except Exception as e:
        (run_dir / "os_cpu.json").write_text(json.dumps({"points": [], "error": str(e)}))
    data = {}
    for kind, group in (("proxy", METRICS["metrics"]), ("instance", METRICS.get("instance_metrics", [])), ("cluster", METRICS.get("cluster_metrics", []))):
        for m in group:
            stats = [(m["key"], m["stat"])] + [(f'{m["key"]}__{x}', x) for x in (m.get("extra_stats") or []) if x != m["stat"]]
            for key, stat in stats:
                try: data[key] = {"metric": m["name"], "stat": stat, "points": cw_series(m, start, end, kind == "instance", kind == "cluster", stat=stat)}
                except Exception as e: data[key] = {"metric": m["name"], "stat": stat, "error": str(e), "points": []}
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


def proxy_name():
    """The deployed Proxy (PROXY_NAME in /etc/exp.env, from the stack's envId). metrics.json only holds the fallback for old stacks."""
    return os.environ.get("PROXY_NAME") or globals().get("METRICS", {}).get("proxy_name", "unknown")


def proxy_defaults():
    """The settings the stack was deployed with: what a run must leave behind. DebugLogging follows `-c debugLogging` at deploy."""
    return {"IdleClientTimeout": 1800, "MaxConnectionsPercent": 80, "MaxIdleConnectionsPercent": 10,
            "DebugLogging": os.environ.get("PROXY_DEBUG_LOGGING_AT_DEPLOY", "false").lower() == "true"}


RESTORE = OUT / "proxy.restore"


def proxy_read():
    n = proxy_name()
    px = subprocess.run(["aws", "rds", "describe-db-proxies", "--db-proxy-name", n, "--query", "DBProxies[0].[IdleClientTimeout,Status,DebugLogging]", "--output", "text"], capture_output=True, text=True, check=True).stdout.split()
    tg = json.loads(subprocess.run(["aws", "rds", "describe-db-proxy-target-groups", "--db-proxy-name", n, "--query", "TargetGroups[0].[ConnectionPoolConfig,Status]", "--output", "json"], capture_output=True, text=True, check=True).stdout)
    return ({"IdleClientTimeout": int(px[0]), "MaxConnectionsPercent": tg[0]["MaxConnectionsPercent"], "MaxIdleConnectionsPercent": tg[0]["MaxIdleConnectionsPercent"],
             "DebugLogging": px[2].lower() == "true"}, px[1] == "available" and tg[1] == "available")


def proxy_apply(want):
    """Set Proxy settings (subset of proxy_defaults()) and wait until they are in effect and the Proxy is available."""
    n = proxy_name(); cur, _ = proxy_read()
    if all(cur[k] == v for k, v in want.items()): return
    if not RESTORE.exists(): RESTORE.write_text(json.dumps(cur))    # first change in this session remembers the original
    full = dict(cur, **want)
    if "IdleClientTimeout" in want or "DebugLogging" in want:
        subprocess.run(["aws", "rds", "modify-db-proxy", "--db-proxy-name", n, "--idle-client-timeout", str(full["IdleClientTimeout"]),
                        "--debug-logging" if full["DebugLogging"] else "--no-debug-logging"], check=True, capture_output=True)
    if "MaxConnectionsPercent" in want or "MaxIdleConnectionsPercent" in want:
        subprocess.run(["aws", "rds", "modify-db-proxy-target-group", "--db-proxy-name", n, "--target-group-name", "default", "--connection-pool-config",
                        f"MaxConnectionsPercent={full['MaxConnectionsPercent']},MaxIdleConnectionsPercent={full['MaxIdleConnectionsPercent']}"], check=True, capture_output=True)
    for _ in range(120):
        cur, avail = proxy_read()
        if avail and all(cur[k] == v for k, v in want.items()): log("proxy settings in effect:", want); time.sleep(30); return
        time.sleep(5)
    raise RuntimeError(f"proxy settings {want} not in effect after 10 min")


def proxy_restore():
    want = json.loads(RESTORE.read_text()) if RESTORE.exists() else proxy_defaults()
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


def build_info(binary="lab"):
    """What the running `lab` binary was built with: the Go toolchain and the dependency versions (e.g. the MySQL driver). A different
    driver or Go version can change connection handling, so results must say which one they came from."""
    r = subprocess.run(["go", "version", "-m", str(HERE / binary)], capture_output=True, text=True, cwd=HERE)
    lines = r.stdout.splitlines()
    go = lines[0].split(":", 1)[-1].strip() if lines else "unknown"           # "<path>: go1.27.4"
    deps = {}
    for ln in lines[1:]:
        f = ln.split()
        if len(f) >= 3 and f[0] == "dep": deps[f[1]] = f[2]
    return go, deps


def sha256_file(p):
    try: return hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest()
    except OSError: return None


def infra_conditions():
    """Infrastructure conditions of the deployed stack (/etc/exp.env, set at deploy time). They change what is measured, so they go into
    every run's meta.json and into its manifest: a result always says whether TLS was required, which password auth the Proxy used, etc."""
    g = lambda k: os.environ.get(k, "unknown")
    return {"require_tls": g("REQUIRE_TLS"), "proxy_auth": g("PROXY_AUTH"), "env_id": g("ENV_ID"), "proxy_name": proxy_name()}


def run_target(e, r): return r.get("target", e.get("target", "proxy"))      # "proxy" | "cluster" (straight to Aurora, no Proxy)
def run_bin(e, r): return r.get("bin", e.get("bin", "lab"))               # which built binary: "lab" or e.g. "lab-v181" (older MySQL driver)


def manifest_of(e, r, cls, version, binary_sha):
    """Everything that decides what a run measures. A finished run is reused only if this hash is unchanged."""
    body = {"scenario": e["scenario"], "args": e.get("common", []) + r.get("args", []), "secret": r.get("secret", e.get("secret", "admin")),
            "proxy": dict(e.get("proxy", {}), **r.get("proxy", {})), "floor": e.get("floor", FLOOR_DEFAULT), "pre_gap_s": e.get("pre_gap_s", 0),
            "observe_after_s": e.get("observe_after_s", 120), "required_metrics": e.get("required_metrics"), "aurora_class": cls,
            "git": version, "binary_sha256": binary_sha, "infra": infra_conditions(),
            "target": run_target(e, r), "bin": run_bin(e, r)}
    return hashlib.sha256(json.dumps(body, sort_keys=True).encode()).hexdigest()


DEFAULT_REQUIRED = ["client", "dbconn", "cpu"]


# CloudWatch returns NO data points for these gauges while their value has never been above zero (found by the smoke test: a run
# without pinning has an empty `pinned` series with no error). An empty series therefore means 0 and is not a missing measurement;
# only a failed query (an "error" on the series) or a series that was never requested is.
ZERO_IF_EMPTY = {"pinned", "intx", "borrowed"}


def series_missing(m, k):
    """True if the CloudWatch series `k` of a run was not collected. `m` is metrics.json["series"] (None if unreadable)."""
    if m is None or not m.get(k) or m[k].get("error"): return True
    return not m[k].get("points") and k.split("__")[0] not in ZERO_IF_EMPTY


def judge(e, rdir, meta, start, run_end):
    """Validity of one finished run, separate from "completed" (the `ok` marker). Returns {valid, reasons}. A run that is not valid is
    kept (an unmet target or a missing series is still an observation) but is not aggregated as a successful measurement."""
    reasons = []
    rc = meta["rc"]
    if rc == 3: reasons.append("target_not_reached")
    elif rc != 0: reasons.append(f"rc={rc}")
    if meta["baseline"]["unmet"]: reasons.append("baseline_unmet")
    try: m = json.loads((rdir / "metrics.json").read_text())["series"]
    except Exception: m = None
    for k in e.get("required_metrics", DEFAULT_REQUIRED):
        if k == "os_cpu":
            try: o = json.loads((rdir / "os_cpu.json").read_text())
            except Exception: o = {"points": [], "error": "unreadable"}
            span = max(1, int((run_end - start).total_seconds()) - 20)
            n = sum(1 for t, *_ in o.get("points", []) if start + dt.timedelta(seconds=20) <= dt.datetime.fromisoformat(t.replace("Z", "+00:00")) <= run_end)
            if o.get("error") or not o.get("points"): reasons.append("os_cpu_missing")
            elif n < 0.8 * span: reasons.append(f"os_cpu_sparse:{n}/{span}")
        elif series_missing(m, k):
            reasons.append(f"metric_missing:{k}")
    return {"valid": not reasons, "reasons": reasons}


PENDING = "metric_missing:pending"


def defer_reasons(reasons):
    """For an experiment with "collect_later": CloudWatch / Enhanced Monitoring values of the run are not there yet when it ends, so a
    missing series is "pending" (fetched once for the whole experiment, see recollect_experiment), not a failure."""
    out = [PENDING if x.startswith(("metric_missing", "os_cpu")) else x for x in reasons]
    return list(dict.fromkeys(out))


def run_experiment(exp, repeat=0, only=None):
    """Runs the experiment on the CURRENT Aurora class; results go to out/<exp>__<class>/. Returns {"failed": [...], "invalid": [...]}.
    Proxy settings changed by a run are always restored (also on errors / Ctrl-C); a failed restore fails the experiment."""
    res = None
    try:
        res = _run_experiment(exp, repeat, only)
    finally:
        err = None
        if RESTORE.exists():
            try: proxy_restore()
            except Exception as ex:
                err = repr(ex); log("PROXY RESTORE FAILED:", err)
        if err and res is not None: res["failed"].append("proxy-restore:" + err)
        if err or res is not None:
            for d in sorted(OUT.glob(f"{exp}__*")):
                write_status(d, proxy_restore=err or "ok")
    return res


def _run_experiment(exp, repeat=0, only=None):
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
    binary_sha = sha256_file(HERE / "lab")
    (exp_dir / "experiment.json").write_text(json.dumps(e, indent=1, ensure_ascii=False))
    total = len(runs) * repeat; done = 0; failed = []; invalid = []
    write_status(exp_dir, state="running", total=total, done=0, current=None, failed=[], invalid=[], started=iso(now()))
    for rep in range(1, repeat + 1):
        for r in ordered(runs, rep - 1):
            rid = f"{r['id']}.r{rep}"; rdir = exp_dir / rid
            mf = manifest_of(e, r, cls, version, binary_sha)
            if (rdir / "ok").exists():
                old = json.loads((rdir / "meta.json").read_text()) if (rdir / "meta.json").exists() else {}
                v = json.loads((rdir / "valid.json").read_text()) if (rdir / "valid.json").exists() else {"valid": False, "reasons": ["no valid.json"]}
                reusable = old.get("manifest") == mf and (v["valid"] or v["reasons"] == ["target_not_reached"])
                if reusable:
                    done += 1; log("skip", rid, "(done, same manifest)"); continue
                dst = exp_dir / "_superseded" / f"{rid}.{int(time.time())}"      # never overwrite evidence: keep the old run next to the new one
                dst.parent.mkdir(exist_ok=True); shutil.move(str(rdir), str(dst))
                log("superseded", rid, "->", dst.name, "(manifest changed or run was not valid)")
            rdir.mkdir(exist_ok=True)
            write_status(exp_dir, current=rid, done=done)
            need = r.get("infra", {})          # e.g. {"require_tls": "false"}: a run that compares plain TCP only makes sense on such a stack
            have = infra_conditions()
            wrong = [f"{k}: needs {v}, the stack has {have.get(k)}" for k, v in need.items() if str(v).lower() != str(have.get(k)).lower()]
            if wrong:                          # do not run, and do not pretend: keep a record that says why, marked invalid
                log("NOT RUN", rid, "- the deployed infrastructure does not match:", wrong)
                (rdir / "client.log").write_text("not run: " + "; ".join(wrong) + "\n")
                (rdir / "metrics.json").write_text(json.dumps({"window": [], "series": {}}))
                (rdir / "meta.json").write_text(json.dumps({
                    "experiment": a.exp, "aurora_class": cls, "run_id": r["id"], "rep": rep, "args": [], "git": version, "manifest": mf,
                    "start": iso(now()), "run_end": iso(now()), "rc": None, "infra": have, "condition": "not run (infrastructure mismatch)",
                    "baseline": {"unmet": False, "last_points": [], "floor": e.get("floor", FLOOR_DEFAULT)}}, indent=1))
                (rdir / "valid.json").write_text(json.dumps({"valid": False, "reasons": ["infra_mismatch: " + "; ".join(wrong)]}))
                (rdir / "ok").write_text("")
                invalid.append(rid + ":infra_mismatch"); done += 1
                write_status(exp_dir, done=done, failed=failed, invalid=invalid)
                continue
            try:
                want = dict(e.get("proxy", {}), **r.get("proxy", {}))
                proxy_apply(want) if want else proxy_restore() if RESTORE.exists() else None
            except Exception as ex:
                log("proxy setting failed:", ex); failed.append(rid + ":proxy"); continue
            if e.get("wait_writer_available"): ensure_class(cls)       # a previous run may have rebooted the writer: wait until it is available again (+120 s)
            if e.get("pre_gap_s"): time.sleep(e["pre_gap_s"])    # >= 2 min keeps 1-minute CloudWatch points of neighbouring runs apart
            base = wait_baseline(e.get("floor", FLOOR_DEFAULT))
            args = [f"--scenario={e['scenario']}"] + e.get("common", []) + r.get("args", [])
            start = now(); log("START", rid, " ".join(args))
            env = dict(os.environ, SECRET=r.get("secret", e.get("secret", "admin")), TARGET=run_target(e, r))
            if run_bin(e, r) != "lab": env["LAB_BIN"] = run_bin(e, r)
            with open(rdir / "client.log", "w") as f:
                rc = subprocess.run([str(HERE / "run.sh")] + args, stdout=f, stderr=subprocess.STDOUT, env=env, cwd=HERE).returncode
            run_end = now(); log("run finished", rid, "rc", rc)
            time.sleep(e.get("observe_after_s", 120))
            collect(rdir, start - dt.timedelta(minutes=3), now())
            floor = e.get("floor", FLOOR_DEFAULT)
            go_ver, deps = build_info(run_bin(e, r))
            meta = {"experiment": a.exp, "target": run_target(e, r), "bin": run_bin(e, r), "go_version": go_ver, "go_deps": deps, "aurora_class": cls, "run_id": r["id"], "rep": rep, "args": args, "git": version, "binary_sha256": binary_sha,
                    "manifest": mf, "start": iso(start), "run_end": iso(run_end), "rc": rc, "baseline": base,
                    "infra": infra_conditions(), "proxy_settings": proxy_read()[0],     # what the Proxy was set to while this run executed
                    # floor >= 50: the Proxy was NOT waited down to its idle floor, so Proxy->Aurora connections may be left over from the previous run
                    "condition": "warm-proxy (previous connections may remain)" if floor >= 50 else "settled (Proxy connections at the floor)"}
            (rdir / "meta.json").write_text(json.dumps(meta, indent=1))
            j = judge(e, rdir, meta, start, run_end)
            if e.get("collect_later"):
                j = {"valid": False if defer_reasons(j["reasons"]) else True, "reasons": defer_reasons(j["reasons"])}
            (rdir / "valid.json").write_text(json.dumps(j))
            real = [x for x in j["reasons"] if x != PENDING]
            if real: invalid.append(rid + ":" + ",".join(real)); log("INVALID", rid, real)
            if rc != 0 and rc != 3: failed.append(rid)
            (rdir / "ok").write_text("")                    # "completed": the run finished and its data were saved. Validity is valid.json.
            done += 1
            write_status(exp_dir, done=done, failed=failed, invalid=invalid)
    write_status(exp_dir, state="done" if not (failed or invalid) else "done_with_failures", current=None, done=done, finished=iso(now()))
    log("ALL DONE", a.exp, cls, "failed:", failed, "invalid:", invalid)
    return {"failed": failed, "invalid": invalid}


def recollect_experiment(exp_name, force=False):
    """Collect again (CloudWatch + Enhanced Monitoring) for the runs of an experiment, then re-judge them. Returns the invalid run ids that
    remain ("<run>:<reasons>"). Without `force` only runs whose series were found missing are collected. WITH `force` every run is: needed
    for collect_later experiments, because a run's own 1-minute CloudWatch points are not there yet when it ends, yet its series are not
    empty (the window starts 3 minutes before the run and holds the previous run's points), so a missing tail is not detected as missing."""
    e = load_exp(exp_name)
    remaining = []
    for exp_dir in sorted(OUT.glob(f"{exp_name}__*")):
        for rdir in sorted(exp_dir.glob("*.r*")):
            if not (rdir / "meta.json").exists(): continue
            meta = json.loads((rdir / "meta.json").read_text()); start = dt.datetime.fromisoformat(meta["start"].replace("Z", "+00:00"))
            end = dt.datetime.fromisoformat(meta["run_end"].replace("Z", "+00:00"))
            j = json.loads((rdir / "valid.json").read_text()) if (rdir / "valid.json").exists() else {"valid": False, "reasons": ["no valid.json"]}
            if force or any(x.startswith(("metric_missing", "os_cpu")) or x == "no valid.json" for x in j["reasons"]):
                log("recollect", rdir.name)
                collect(rdir, start - dt.timedelta(minutes=3), end + dt.timedelta(minutes=10))
                j = judge(e, rdir, meta, start, end)
                (rdir / "valid.json").write_text(json.dumps(j))
            if not j["valid"]: remaining.append(rdir.name + ":" + ",".join(j["reasons"]))
    return remaining


def cmd_recollect(a):
    take_lock()
    left = recollect_experiment(a.exp, force=a.force)
    log("recollect done;", len(left), "run(s) still invalid", left)


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
    problems = []
    st = {"state": "running", "step": 0, "steps": len(c["steps"]), "started": iso(now()), "error": None}
    sp.write_text(json.dumps(st))
    try:
        for i, step in enumerate(c["steps"]):
            st.update(step=i + 1, current=step, updated=iso(now())); sp.write_text(json.dumps(st))
            if "aurora_class" in step: ensure_class(step["aurora_class"])
            else:
                r = run_experiment(step["exp"], step.get("repeat", 0), step.get("only")) or {"failed": [], "invalid": []}
                if load_exp(step["exp"]).get("collect_later"):
                    log("collect_later:", step["exp"], "- waiting 150 s once for CloudWatch, then collecting every run of the experiment")
                    time.sleep(150)
                    left = recollect_experiment(step["exp"], force=True)
                    r = {"failed": r["failed"], "invalid": left}
                problems += [f'{step["exp"]}:{x}' for x in r["failed"] + r["invalid"]]
        st.update(state="done" if not problems else "done_with_failures", problems=problems, finished=iso(now()))
    except BaseException as ex:
        st.update(state="failed", error=repr(ex), updated=iso(now())); sp.write_text(json.dumps(st)); raise
    sp.write_text(json.dumps(st)); log("CAMPAIGN", st["state"].upper(), a.name, problems)


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
    s0 = sp.add_parser("recollect"); s0.add_argument("exp"); s0.add_argument("--force", action="store_true", help="collect every run again, not only the ones found missing"); s0.set_defaults(f=cmd_recollect)
    for n, f in (("run", cmd_run), ("status", cmd_status), ("pack", cmd_pack)):
        s = sp.add_parser(n); s.add_argument("exp"); s.set_defaults(f=f)
        if n == "run": s.add_argument("--repeat", type=int, default=0); s.add_argument("--only", default="")
    a = ap.parse_args()
    if a.cmd in ("run", "campaign", "campaign-status", "status", "pack", "recollect"): load_env(); OUT.mkdir(exist_ok=True)
    a.f(a)
