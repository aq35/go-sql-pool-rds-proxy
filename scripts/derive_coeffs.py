#!/usr/bin/env python3
"""Derive the rule coefficients from fetched results -> lab/coeffs.json (with provenance). No AI, no manual reading.

  derive_coeffs.py            (after `make finish`; reads results/*__<class>/ )

Each coefficient is derived only if its experiment has usable runs (status OK / no BASELINE_UNMET); otherwise it stays at
the default and is listed as "not derived". The derivation rules are written next to each coefficient below.
"""
import csv, json, pathlib, statistics, sys

root = pathlib.Path(__file__).resolve().parent.parent
res = root / "results"
out, notes = {}, []


def rows(exp, cls=None):
    r = []
    for d in sorted(res.glob(f"{exp}__*")):
        if cls and not d.name.endswith(cls): continue
        f = d / "runs.csv"
        if f.exists():
            r += [dict(x, _dir=d.name) for x in csv.DictReader(f.open())]
    return [x for x in r if "BASELINE_UNMET" not in x["status"] and "RUN_FAILED" not in x["status"]]


def num(x, k):
    try: return float(x[k])
    except Exception: return None


# k: knee / vCPU. knee = qps_knee (smallest N reaching 90% of best QPS), per class; take the median across classes and mixes.
ks = []
for d in sorted(res.glob("capacity-curve__*")):
    f = d / "knee.json"
    vcpu = {"large": 2, "xlarge": 4, "2xlarge": 8}.get(d.name.rsplit(".", 1)[-1])
    if f.exists() and vcpu:
        for mix, kn in json.loads(f.read_text()).items():
            if kn.get("qps_knee"): ks.append((kn["qps_knee"] / vcpu, d.name, mix))
if ks:
    # conservative: the smallest k over mixes/classes (the CPU-bound mix limits what is safe for any query mix)
    k, d, mix = min(ks)
    out["k"] = {"value": round(k, 2), "source": f"measured: min over {len(ks)} (class,mix) of qps_knee/vCPU; limiting: {d} {mix}",
                "meaning": "同時数の膝 / vCPU", "values": [round(x[0], 2) for x in ks]}
else: notes.append("k: no capacity-curve knee.json")

# alpha: largest pin_ratio (pinned / allowed) among pinning-impact levels whose Status is OK in every repetition.
pr = rows("pinning-impact")
if pr:
    byrun = {}
    for x in pr: byrun.setdefault(x["run"], []).append(x)
    ok = [(max(num(x, "pin_ratio") or 0 for x in v), r) for r, v in byrun.items() if all(x["status"] == "OK" for x in v)]
    allowed = max((r for r in pr), key=lambda x: num(x, "pin_ratio") or 0, default=None)
    if ok:
        a = max(v for v, _ in ok)
        out["alpha"] = {"value": round(min(0.9, a + 0.0), 2) if a > 0 else 0.5, "source": f"measured: highest pin_ratio with Status OK in all reps ({[r for v, r in ok]})",
                        "meaning": "Proxy 上限のうち、ピン留め + プールに割り当ててよい割合"}
else: notes.append("alpha: no pinning-impact runs")

# beta / gamma: smallest MaxIdle whose p99 is within 20% of the best p99 (tls-latency, TLS + cross-AZ), as a fraction of concurrency.
tl = [x for x in rows("tls-latency") if x["run"].startswith("tls-cross-")]
if tl:
    by = {}
    for x in tl:
        n = int(x["run"].rsplit("-", 1)[1]); p = num(x, "hold_p99_ms")
        if p is not None: by.setdefault(n, []).append(p)
    med = {n: statistics.median(v) for n, v in by.items()}
    best = min(med.values()); ok = [n for n in sorted(med) if med[n] <= 1.2 * best]
    if ok:
        conc = 10  # tls-latency: 10 workers per pool
        out["gamma"] = {"value": round(ok[0] / conc, 2), "source": f"measured: smallest MaxIdle with p99 within 20% of best = {ok[0]} (of {conc} workers); p99 by MaxIdle {med}",
                        "meaning": "MaxIdle の最小維持率"}
        out["beta"] = {"value": 1.0, "source": "default (tls-latency measures gamma; beta needs the idle-sweep active distribution)", "meaning": "MaxIdle のバッファ係数"}
else: notes.append("beta/gamma: no tls-latency runs")

# delta: largest idle_time / IdleClientTimeout ratio with no errors and p99 within 20% of best (lifecycle-sync, IdleClientTimeout=60 s).
lc = rows("lifecycle-sync")
if lc:
    pairs = {"idle-30s": 30 / 60, "idle-50s": 50 / 60, "idle-90s": 90 / 60, "idle-none": 99}
    by = {}
    for x in lc: by.setdefault(x["run"], []).append(x)
    best = min(statistics.median([num(x, "hold_p99_ms") for x in v if num(x, "hold_p99_ms") is not None] or [1e9]) for v in by.values())
    good = [pairs[r] for r, v in by.items() if r in pairs and all((num(x, "errors") or 0) == 0 for x in v)
            and statistics.median([num(x, "hold_p99_ms") or 1e9 for x in v]) <= 1.2 * best and pairs[r] < 1]
    if good:
        out["delta"] = {"value": round(max(good), 2), "source": "measured: largest idle-time / IdleClientTimeout < 1 with no errors and p99 within 20% of best",
                        "meaning": "ConnMaxIdleTime / IdleClientTimeout"}
else: notes.append("delta: no lifecycle-sync runs")

(root / "lab/coeffs.json").write_text(json.dumps(out, indent=1, ensure_ascii=False) + "\n")
print(json.dumps({k: v["value"] for k, v in out.items()}), "\nnot derived:", notes or "none")
