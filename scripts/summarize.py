#!/usr/bin/env python3
"""Summarize results/<exp>/ (made by `make fetch`) into results/<exp>/summary.md. Usage: summarize.py <exp-dir>"""
import datetime as dt, json, pathlib, re, statistics, sys

d = pathlib.Path(sys.argv[1])
CRIT = json.loads((pathlib.Path(__file__).resolve().parent.parent / "lab/criteria.json").read_text())
ERR_RE = re.compile(r"\b(c\d+ w\d+ (select|hold|gorm)|get_lock|conn|rotate c\d+|lock-holders c\d+ keepalive)[: ]")

exp = json.loads((d / "experiment.json").read_text())
P = lambda s: dt.datetime.fromisoformat(s.replace("Z", "+00:00"))


def series(m, key):
    return [(P(t) if isinstance(t, str) else t, v) for t, v in m["series"].get(key, {}).get("points", [])]


def analyse(rdir):
    meta = json.loads((rdir / "meta.json").read_text())
    m = json.loads((rdir / "metrics.json").read_text())
    start, end = P(meta["start"]), P(meta["run_end"])
    inrun = lambda k: [v for t, v in series(m, k) if start - dt.timedelta(minutes=1) <= t <= end + dt.timedelta(minutes=1)]
    mx = lambda xs: max(xs) if xs else None
    db, cl = series(m, "dbconn"), dict(series(m, "client"))
    floor = meta["baseline"]["floor"]
    settle = next((round((t - end).total_seconds() / 60, 1) for t, v in db if t >= end and v <= floor), None)
    cpu = [v for t, v in series(m, "cpu") if start <= t <= end]
    allowed = mx([v for t, v in series(m, "allowed") if start <= t <= end]) or None
    pct = lambda xs, q: sorted(xs)[min(len(xs) - 1, int(q * len(xs)))] if xs else None
    r = dict(cpu_p50=pct(cpu, 0.5), cpu_p95=pct(cpu, 0.95), cpu_max=mx(cpu), allowed=allowed,
             pin_ratio=(mx(inrun("pinned")) / allowed) if allowed and inrun("pinned") else None,
             borrowlat_max=mx(inrun("borrowlat")), run=meta["run_id"], rep=meta["rep"], rc=meta["rc"], unmet=meta["baseline"]["unmet"], base=max(meta["baseline"]["last_points"] or [0]),
             client=mx(inrun("client")), db=mx(inrun("dbconn")), pinned=mx(inrun("pinned")), borrowed=mx(inrun("borrowed")),
             excess=mx([v - cl.get(t, 0) for t, v in db if start <= t <= end + dt.timedelta(minutes=8)]),
             db_end=db[-1][1] if db else None, settle=settle)
    log = (rdir / "client.log").read_text(errors="replace")
    r["errors"] = sum(1 for l in log.splitlines() if ERR_RE.search(l))
    last = [l for l in log.splitlines() if "client_side_open" in l]
    if last:
        for k in ("hold_avg_ms", "hold_p99_ms", "max_idle_closed", "wait_count"):
            g = re.search(k + r"=([\d.]+)", last[-1]); r[k] = float(g.group(1)) if g else None
    ex = [l for l in log.splitlines() if "EXCEED-STATS" in l]
    if ex:
        g = re.search(r"failed=(\d+)", ex[-1]); r["failed"] = int(g.group(1)) if g else None
    r["status"] = status_of(r, meta)
    return r


def status_of(r, meta):
    """Flags from lab/criteria.json. OK = none fired. Several flags are joined with '+'."""
    f = []
    if r["rc"] not in (0,): f.append("RUN_FAILED")
    if meta["baseline"]["unmet"]: f.append("BASELINE_UNMET")
    if r.get("cpu_max") is not None and r["cpu_max"] > CRIT["cpu_danger_pct"]: f.append("CPU_OVER_%d" % CRIT["cpu_danger_pct"])
    if r.get("pin_ratio") is not None and r["pin_ratio"] > CRIT["pin_saturation_ratio"]: f.append("PIN_SATURATED")
    if (r.get("wait_count") or 0) > CRIT["wait_count_max"]: f.append("WAIT_SPIKE")
    if r.get("errors", 0) > CRIT["error_count_max"]: f.append("ERRORS")
    if r.get("borrowlat_max") is not None and r["borrowlat_max"] > CRIT["borrow_latency_max_us"]: f.append("BORROW_SLOW")
    return "+".join(f) or "OK"


def capacity_summary():
    """Per concurrency level: QPS, p99, Aurora CPU, client CPU (median over repetitions); knees per mix."""
    cls = next((json.loads(m.read_text()).get("aurora_class") for m in d.glob("*.r*/meta.json")), "?")
    out = [f"# {exp['name']} — {cls}", "", f"**問い**: {exp['question']}", "", f"**仮説**: {exp['hypothesis']}", "", f"**読み方**: {exp['reading']}", ""]
    knees = {}
    for rid in [r["id"] for r in exp["runs"]]:
        levels = {}
        for rdir in sorted(d.glob(f"{rid}.r*")):
            if not (rdir / "ok").exists() or not (rdir / "metrics.json").exists(): continue
            m = json.loads((rdir / "metrics.json").read_text())
            cpu = series(m, "cpu")
            for l in (x for x in (rdir / "client.log").read_text(errors="replace").splitlines() if "CAPACITY-LEVEL" in x):
                f = dict(kv.split("=", 1) for kv in l.split("CAPACITY-LEVEL ", 1)[1].split())
                st, en = P(f["start"]), P(f["end"])
                lv = levels.setdefault(int(f["n"]), {"qps": [], "p50": [], "p99": [], "ccpu": [], "acpu": [], "err": 0})
                lv["qps"].append(float(f["qps"])); lv["p50"].append(int(f["p50_us"]) / 1000); lv["p99"].append(int(f["p99_us"]) / 1000)
                lv["ccpu"].append(float(f["client_cpu"])); lv["err"] += int(f["errors"])
                a = [v for t, v in cpu if st.replace(second=0) <= t <= en]
                if a: lv["acpu"].append(max(a))
        if not levels: continue
        med = lambda xs: statistics.median(xs) if xs else None
        rows = {n: {k: (med(v) if isinstance(v, list) else v) for k, v in lv.items()} for n, lv in sorted(levels.items())}
        ok = {n: r for n, r in rows.items() if r["ccpu"] is None or r["ccpu"] <= 70}
        best = max((r["qps"] for r in ok.values()), default=None)
        qknee = min((n for n, r in ok.items() if best and r["qps"] >= 0.9 * best), default=None)
        p99min = min((r["p99"] for r in ok.values()), default=None)
        pknee = max((n for n, r in ok.items() if p99min and r["p99"] <= 2 * p99min), default=None)
        top = rows[max(rows)]      # highest level: what happens when we push beyond the knee
        cpu_at = (top["acpu"] if top["acpu"] is not None else None)
        if cpu_at is None: diag = "判定不能（Aurora CPU が取れていない）"
        elif top["ccpu"] is not None and top["ccpu"] > 70: diag = "負荷生成側が律速（Aurora の限界は測れていない）"
        elif cpu_at >= 80: diag = "Aurora の CPU が限界（80% 以上）→ 重い SQL の改善（インデックス、N+1 の解消）、それでも足りなければスケールアップ"
        elif cpu_at >= 50: diag = "Aurora の CPU は 50〜80%。SQL の改善とスケールアップの両方が候補。ロックや I/O の待ちも疑う"
        else: diag = "Aurora の CPU に余裕（50% 未満）なのに頭打ち → スケールアップでは直らない。接続プール、ロック待ち、トランザクションの持ち方、ネットワークを見る"
        knees[rid] = {"diagnosis": diag, "cpu_at_top_level": cpu_at, "qps_knee": qknee, "p99_knee": pknee, "best_qps": best, "aurora_class": cls}
        out += [f"## mix = {rid}", "", f"**原因の判定**: {diag}（最大水準 N={max(rows)} での Aurora CPU = {'–' if cpu_at is None else '%.0f%%' % cpu_at}）", "", f"**qps_knee = {qknee}**（QPS が最大の 90% に達する最小の N）、**p99_knee = {pknee}**（p99 が最良の 2 倍以内の最大の N）。負荷生成側 CPU が 70% 超の水準は、knee の判定から除外。", "",
                "| N | QPS | p50(ms) | p99(ms) | Aurora CPU max(%) | 負荷生成 CPU(%) | エラー | 備考 |", "|---|---|---|---|---|---|---|---|"]
        for n, r in rows.items():
            note = "負荷生成が律速" if r["ccpu"] is not None and r["ccpu"] > 70 else ""
            acpu = "–" if r["acpu"] is None else "%.0f" % r["acpu"]
            out.append("| %d | %.0f | %.2f | %.2f | %s | %.0f | %d | %s |" % (n, r["qps"], r["p50"], r["p99"], acpu, r["ccpu"], r["err"], note))
        out.append("")
    (d / "knee.json").write_text(json.dumps(knees, indent=1))
    (d / "summary.md").write_text("\n".join(out) + "\n"); print("\n".join(out)); sys.exit(0)


def rotation_summary():
    """Per run: query errors, connection failures, stale/fresh login probes, time until old-credential connections are gone."""
    cls = next((json.loads(m.read_text()).get("aurora_class") for m in d.glob("*.r*/meta.json")), "?")
    out = [f"# {exp['name']} — {cls}", "", f"**問い**: {exp['question']}", "", f"**仮説**: {exp['hypothesis']}", "", f"**読み方**: {exp['reading']}", ""]
    ts = lambda l: dt.datetime.strptime(l[:19], "%Y/%m/%d %H:%M:%S").replace(tzinfo=dt.timezone.utc)
    kv = lambda l, tag: dict(x.split("=", 1) for x in l.split(tag + " ", 1)[1].split() if "=" in x)
    hdr = ["run", "回転回数", "既存接続のエラー(最終)", "接続失敗", "stale ログイン失敗/回", "fresh ログイン失敗/回", "古い接続が消えるまで(秒) 中央値/最大", "old_pool 最大", "old_locks 最終", "Proxy DBConn max", "Pinned max", "判定"]
    rows = []
    for rid in [r["id"] for r in exp["runs"]]:
        for rdir in sorted(d.glob(f"{rid}.r*")):
            if not (rdir / "ok").exists() or not (rdir / "metrics.json").exists(): continue
            lines = (rdir / "client.log").read_text(errors="replace").splitlines()
            rot = [ts(l) for l in lines if "ROTATE-SECRET requested" in l]
            ver = [ts(l) for l in lines if "ROT-VERSION" in l]
            stats = [(ts(l), kv(l, "ROT-STATS")) for l in lines if "ROT-STATS" in l]
            probes = [kv(l, "ROT-PROBE") for l in lines if "ROT-PROBE" in l and "stale=" in l]
            sf = sum(1 for p in probes if p["stale"] != "ok"); ff = sum(1 for p in probes if p["fresh"] != "ok")
            drains = []
            for v in ver:
                after = [t for t, s in stats if t >= v]
                zero = next((t for t, s in stats if t >= v and int(s["old_pool"]) == 0), None)
                if after and zero: drains.append((zero - v).total_seconds())
                elif after: drains.append(float("inf"))
            last = stats[-1][1] if stats else {}
            qerr = last.get("query_err", "-")
            m = json.loads((rdir / "metrics.json").read_text())
            mx = lambda k: max([v for t, v in series(m, k)] or [0])
            flags = []
            if qerr not in ("-", ""): flags.append("既存接続エラー")
            if int(last.get("connect_fail", 0)) > 0: flags.append("接続失敗")
            if rot and sf > 0: flags.append("古い認証情報で新規ログイン不可")
            if drains and any(x == float("inf") for x in drains): flags.append("古い接続が残る")
            if int(last.get("old_locks", 0)) > 0: flags.append("ロック専用が古いまま")
            fm = lambda x: "∞(未排除)" if x == float("inf") else "%.0f" % x
            dr = "–" if not drains else "%s / %s" % (fm(statistics.median(drains)), fm(max(drains)))
            rows.append([rid, len(rot), qerr, last.get("connect_fail", "–"), "%d/%d" % (sf, len(probes)), "%d/%d" % (ff, len(probes)), dr,
                         max([int(s["old_pool"]) for t, s in stats] or [0]), last.get("old_locks", "–"), "%g" % mx("dbconn"), "%g" % mx("pinned"), "+".join(flags) or "問題なし"])
    out += ["| " + " | ".join(hdr) + " |", "|" + "---|" * len(hdr)] + ["| " + " | ".join(str(x) for x in r) + " |" for r in rows]
    out += ["", "注: 「古い接続が消えるまで」は、回転で新しい版が現れてから、その版以外の接続が 0 になるまで(ROT-STATS は 15 秒間隔)。∞ は、実行の終わりまでに 0 にならなかったこと。"]
    (d / "runs.csv").write_text("\n".join([",".join(hdr)] + [",".join(str(x) for x in r) for r in rows]) + "\n")
    (d / "summary.md").write_text("\n".join(out) + "\n"); print("\n".join(out)); sys.exit(0)


def patterns_summary():
    """scenario patterns: every PATTERN-RESULT line is a row; numeric columns are the median over repetitions [min-max]."""
    cls = next((json.loads(m.read_text()).get("aurora_class") for m in d.glob("*.r*/meta.json")), "?")
    out = [f"# {exp['name']} — {cls}", "", f"**問い**: {exp['question']}", "", f"**仮説**: {exp['hypothesis']}", "", f"**読み方**: {exp['reading']}", ""]
    ID_KEYS = {"mode", "test", "variant", "max_open", "rps", "timeout_ms", "warm", "conns", "queries_per_request", "mix", "lifetime_s", "workers", "summary", "trial", "gap_s", "idle_time_s", "query", "pause_s"}
    groups, order, failures = {}, [], []
    for rid in [r["id"] for r in exp["runs"]]:
        for rdir in sorted(d.glob(f"{rid}.r*")):
            if not (rdir / "ok").exists() or not (rdir / "metrics.json").exists(): continue
            meta = json.loads((rdir / "meta.json").read_text())
            # completed (ok) is not valid: only valid runs are aggregated; the others are listed below, never mixed into medians
            if (rdir / "valid.json").exists(): val = json.loads((rdir / "valid.json").read_text())
            else: val = {"valid": meta["rc"] == 0 and not meta["baseline"]["unmet"], "reasons": [] if meta["rc"] == 0 else [f"rc={meta['rc']}"]}   # runs from before valid.json existed
            if not val["valid"]:
                failures.append((rid, meta["rep"], val["reasons"])); continue
            m = json.loads((rdir / "metrics.json").read_text())
            start, end = P(meta["start"]), P(meta["run_end"])
            inrun = lambda k: [v for t, v in series(m, k) if start <= t <= end + dt.timedelta(minutes=1)]
            side = {"proxy_condition": "warm" if "warm-proxy" in meta.get("condition", "") else ("settled" if meta.get("condition") else None)}
            for k in ("client", "dbconn", "pinned", "intx"):   # Maximum (configured) and Sum (AWS-recommended) side by side
                side[f"proxy_{k}_max"] = max(inrun(k) or [0])
                if m["series"].get(f"{k}__Sum", {}).get("points"): side[f"proxy_{k}_sum"] = max(inrun(f"{k}__Sum") or [0])
            # Aurora OS CPU per second (Enhanced Monitoring), cut to this run: skip the first 20 s (warm-up) and stop at the end of the run
            osf = rdir / "os_cpu.json"
            if osf.exists():
                pts = [(P(t), v) for t, v, *_ in json.loads(osf.read_text()).get("points", [])]
                cs = sorted(v for t, v in pts if start + dt.timedelta(seconds=20) <= t <= end)
                if cs: side.update(aurora_cpu_p50=cs[len(cs) // 2], aurora_cpu_max=cs[-1])
            lines = (rdir / "client.log").read_text(errors="replace").splitlines()
            for l in lines:
                if "PATTERN-AURORA " in l:
                    side.update({k: v for k, v in (kv.split("=", 1) for kv in l.split("PATTERN-AURORA ", 1)[1].split() if "=" in kv)})
            for l in lines:
                if "PATTERN-RESULT " not in l: continue
                f = dict(kv.split("=", 1) for kv in l.split("PATTERN-RESULT ", 1)[1].split() if "=" in kv)
                if f.get("trial") is not None: continue          # per-trial lines: the summary line carries the medians
                key = (rid, tuple(sorted((k, v) for k, v in f.items() if k in ID_KEYS)))
                if key not in groups: groups[key] = []; order.append(key)
                groups[key].append(dict(f, **{k: v for k, v in side.items() if v is not None}, rc=meta["rc"], unmet=meta["baseline"]["unmet"]))
    def num(x):
        try: return float(x)
        except Exception: return None
    cols = []
    for k in order:
        for r in groups[k]:
            for c in r:
                if c not in cols and c not in ID_KEYS and c not in ("rc", "unmet", "err_sample", "err"): cols.append(c)
    def cell(rs, c):
        xs = [num(r[c]) for r in rs if c in r and num(r[c]) is not None]
        if not xs:
            cat = sorted({r[c] for r in rs if c in r})
            if not cat: return "–"
            return "/".join(f"{v}×{sum(1 for r in rs if r.get(c) == v)}" for v in cat) if len(cat) > 1 else cat[0]
        f = lambda x: f"{x:g}" if abs(x) < 100 else f"{x:.0f}"
        return f(statistics.median(xs)) if len(xs) == 1 or min(xs) == max(xs) else f"{f(statistics.median(xs))} [{f(min(xs))}–{f(max(xs))}]"
    hdr = ["run", "識別", "n"] + cols
    out += ["値は、繰り返しの中央値 [最小–最大]。", "", "`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。", "", "| " + " | ".join(hdr) + " |", "|" + "---|" * len(hdr)]
    csv = [",".join(hdr)]
    for key in order:
        rid, ident = key; rs = groups[key]
        idt = " ".join(f"{k}={v}" for k, v in ident if k not in ("mode", "summary"))
        row = [rid, idt, str(len(rs))] + [cell(rs, c) for c in cols]
        out.append("| " + " | ".join(row) + " |"); csv.append(",".join(x.replace(",", ";") for x in row))
    errs = sorted({r.get("err_sample") or r.get("err") for k in order for r in groups[k] if (r.get("err_sample") or r.get("err"))})
    errs = [re.sub(r"\d+\.\d+\.\d+\.\d+(:\d+)?", "<ip>", e) for e in errs]     # no addresses in published summaries
    if errs: out += ["", "エラーの例: " + " / ".join(errs[:6])]
    out += ["", "## 無効な run(集計に含めていない。データは保存してある)", ""]
    out += [f"- {rid}.r{rep}: {', '.join(rs) or '理由不明'}" for rid, rep, rs in failures] or ["なし"]
    out += ["", "`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。", "`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。"]
    (d / "runs.csv").write_text("\n".join(csv) + "\n")
    (d / "summary.md").write_text("\n".join(out) + "\n"); print("\n".join(out)); sys.exit(0)


if exp["scenario"] == "patterns":
    patterns_summary()
if exp["scenario"] == "rotation":
    rotation_summary()
if exp["scenario"] == "capacity":
    capacity_summary()

runs = {}
for rdir in sorted(d.glob("*.r*")):
    if (rdir / "ok").exists() and (rdir / "metrics.json").exists():
        runs.setdefault(rdir.name.rsplit(".r", 1)[0], []).append(analyse(rdir))

cols = [("client", "ClientConn max"), ("db", "DBConn max"), ("pinned", "Pinned max"), ("borrowed", "Borrowed max"),
        ("excess", "DB−Client max"), ("settle", "下限へ戻る(分)"), ("hold_avg_ms", "平均(ms)"), ("hold_p99_ms", "p99(ms)"),
        ("max_idle_closed", "idle closed"), ("failed", "失敗")]
cols = [c for c in cols if any(r.get(c[0]) is not None for rs in runs.values() for r in rs)]


def cell(rs, k):
    xs = [r[k] for r in rs if r.get(k) is not None]
    if not xs: return "–"
    f = lambda x: f"{x:g}" if abs(x) < 100 else f"{x:.0f}"
    return f(statistics.median(xs)) if len(xs) == 1 else f"{f(statistics.median(xs))} [{f(min(xs))}–{f(max(xs))}]"


out = [f"# {exp['name']} — {next((json.loads(m.read_text()).get('aurora_class') for m in d.glob('*.r*/meta.json')), '?')}", "", f"**問い**: {exp['question']}", "", f"**仮説**: {exp['hypothesis']}", "", f"**読み方**: {exp['reading']}", "",
       "値は、繰り返しの中央値 [最小–最大]。baseline 未達の run は表に含めているが、下の警告を確認すること。", "",
       "| run | n | " + " | ".join(c[1] for c in cols) + " |", "|---|---|" + "---|" * len(cols)]
for rid in [r["id"] for r in exp["runs"]]:
    rs = runs.get(rid, [])
    out.append(f"| {rid} | {len(rs)} | " + " | ".join(cell(rs, c[0]) for c in cols) + " |")
hdr = ["run", "rep", "status", "client_max", "db_max", "pinned_max", "pin_ratio", "cpu_p50", "cpu_p95", "cpu_max", "borrowlat_max_us", "hold_avg_ms", "hold_p99_ms", "wait_count", "max_idle_closed", "errors", "db_minus_client_max", "settle_min"]
keys = ["run", "rep", "status", "client", "db", "pinned", "pin_ratio", "cpu_p50", "cpu_p95", "cpu_max", "borrowlat_max", "hold_avg_ms", "hold_p99_ms", "wait_count", "max_idle_closed", "errors", "excess", "settle"]
fmt = lambda v: "" if v is None else ("%g" % round(v, 3) if isinstance(v, float) else str(v))
rows = [[fmt(r.get(k)) for k in keys] for rid in [x["id"] for x in exp["runs"]] for r in runs.get(rid, [])]
(d / "runs.csv").write_text("\n".join([",".join(hdr)] + [",".join(x) for x in rows]) + "\n")
out += ["", "## 1 run = 1 行（runs.csv と同じ。Status は lab/criteria.json の基準）", "", "| " + " | ".join(hdr) + " |", "|" + "---|" * len(hdr)] + ["| " + " | ".join(x) + " |" for x in rows]
warn = [f"- {rid}.r{r['rep']}: 開始時の DatabaseConnections が下限に戻っていない（{r['base']:g}）" for rid, rs in runs.items() for r in rs if r["unmet"]]
warn += [f"- {rid}.r{r['rep']}: 実行が異常終了（rc={r['rc']}）" for rid, rs in runs.items() for r in rs if r["rc"] not in (0,)]
verdict = []
if exp.get("pass_criteria") == "status_ok":
    for rid in [r["id"] for r in exp["runs"]]:
        rs = runs.get(rid, [])
        if rs: verdict.append("- %s: %s（%d/%d 回が Status OK）" % (rid, "合格" if all(r["status"] == "OK" for r in rs) else "不合格", sum(r["status"] == "OK" for r in rs), len(rs)))
    out += ["", "## 判定（合格基準: 全 rep が Status OK）", *verdict]
out += ["", "## 警告", *(warn or ["なし"])]
(d / "summary.md").write_text("\n".join(out) + "\n")
print("\n".join(out))
