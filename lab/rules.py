#!/usr/bin/env python3
"""Deterministic rules: infrastructure + application inputs -> Go pool settings. stdlib only.

  rules.py --vcpu 2 --max-connections 100 --containers 3 --workers 10 --active-avg 4 [--tls] [--cross-az] [--locks-per-container 10]
           [--coeffs coeffs.json]  [--json]

Coefficients (k, alpha, beta, gamma, delta, lifetime) come from coeffs.json, written by scripts/derive_coeffs.py from measured
results. Without a measured value the DEFAULTS below are used and the output says so (provenance = "default/guess").
"""
import argparse, json, math, pathlib, sys

HERE = pathlib.Path(__file__).resolve().parent
DEFAULTS = {  # value, provenance. Replace by measurement: scripts/derive_coeffs.py
    "k":      {"value": 4.0,  "source": "default/guess", "meaning": "同時数の膝 / vCPU (capacity-curve)"},
    "alpha":  {"value": 0.7,  "source": "default/guess", "meaning": "Proxy 上限のうち、ピン留め + プールに割り当ててよい割合 (pinning-impact; 公式の余裕 30%)"},
    "beta":   {"value": 1.0,  "source": "default/guess", "meaning": "MaxIdle のバッファ係数 (churn-cost, idle-sweep)"},
    "gamma":  {"value": 0.3,  "source": "default/guess", "meaning": "MaxIdle の最小維持率 = MaxOpen に対する割合 (churn-cost)"},
    "delta":  {"value": 0.5,  "source": "default/guess", "meaning": "ConnMaxIdleTime / Proxy IdleClientTimeout (lifecycle-sync)"},
    "lifetime_min": {"value": 30, "source": "default/guess", "meaning": "ConnMaxLifetime(分)。Proxy の 24 時間より十分短く (lifecycle-sync)"},
    "reconnect_cost_boost": {"value": 1.5, "source": "default/guess", "meaning": "TLS / クロス AZ のとき beta, gamma に掛ける (churn-cost)"},
}


def load_coeffs(path=None):
    c = {k: dict(v) for k, v in DEFAULTS.items()}
    p = pathlib.Path(path) if path else HERE / "coeffs.json"
    if p.exists():
        for k, v in json.loads(p.read_text()).items():
            if k in c: c[k] = v
    return c


def compute(i, c):
    """i: vcpu, max_connections, containers, workers, active_avg, [proxy_max_pct=80, proxy=True, idle_client_timeout_s=1800,
    tls=False, cross_az=False, locks_per_container=0, deploy_overlap=True]. Returns settings + the intermediate numbers."""
    v = lambda k: c[k]["value"]
    proxy_cap = i["max_connections"] * i.get("proxy_max_pct", 80) / 100
    pinned = i.get("locks_per_container", 0) * i["containers"]
    # 1. total pool size: the smaller of "what the DB can use" and "what the Proxy may hand out after pinned connections".
    containers_live = i["containers"] * (2 if i.get("deploy_overlap", True) else 1)       # rolling deploy: old + new
    by_cpu = i["vcpu"] * v("k")
    overlap = containers_live / i["containers"]
    by_proxy_raw = (proxy_cap * v("alpha") - pinned * overlap) / overlap       # steady-state pool budget after the pinned (lock) connections
    feasible = by_proxy_raw >= i["containers"]                                  # at least one connection per container
    by_proxy = max(1, by_proxy_raw)
    total_open = max(1, math.floor(min(by_cpu, by_proxy)))
    # 2. per container, but never beyond the workers that can use connections
    max_open = min(math.ceil(total_open / i["containers"]), i["workers"])
    # 3. idle: cover the usual concurrency (+buffer) and a minimum share; costlier reconnects -> more idle
    boost = v("reconnect_cost_boost") if (i.get("tls") or i.get("cross_az")) else 1.0
    max_idle = min(max_open, max(math.ceil(i["active_avg"] * v("beta") * boost), math.ceil(max_open * v("gamma") * boost)))
    # 4./5. times
    idle_to = i.get("idle_client_timeout_s", 1800) if i.get("proxy", True) else 900
    conn_idle = int(idle_to * v("delta")) if i.get("proxy", True) else 300
    conn_life = int(v("lifetime_min") * 60)
    return {"SetMaxOpenConns": max_open, "SetMaxIdleConns": max_idle, "SetConnMaxIdleTime_s": conn_idle, "SetConnMaxLifetime_s": conn_life,
            "_feasible": feasible,
            "_warning": None if feasible else "INFEASIBLE: the pinned (lock) connections alone use the Proxy budget (alpha x cap). Reduce locks, raise max_connections, or accept a higher alpha.",
            "_intermediate": {"proxy_cap": proxy_cap, "pinned_total": pinned, "total_open": total_open, "by_cpu": by_cpu, "by_proxy": round(by_proxy, 1),
                              "binding": "cpu" if by_cpu <= by_proxy else "proxy", "total_open_cap_check": max_open * i["containers"]},
            "_provenance": {k: c[k]["source"] for k in c}}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--vcpu", type=int, required=True); ap.add_argument("--max-connections", type=int, required=True)
    ap.add_argument("--containers", type=int, required=True); ap.add_argument("--workers", type=int, required=True)
    ap.add_argument("--active-avg", type=float, required=True)
    ap.add_argument("--proxy-max-pct", type=int, default=80); ap.add_argument("--no-proxy", action="store_true")
    ap.add_argument("--idle-client-timeout", type=int, default=1800)
    ap.add_argument("--tls", action="store_true"); ap.add_argument("--cross-az", action="store_true")
    ap.add_argument("--locks-per-container", type=int, default=0); ap.add_argument("--no-deploy-overlap", action="store_true")
    ap.add_argument("--coeffs"); ap.add_argument("--json", action="store_true")
    a = ap.parse_args()
    i = dict(vcpu=a.vcpu, max_connections=a.max_connections, containers=a.containers, workers=a.workers, active_avg=a.active_avg,
             proxy_max_pct=a.proxy_max_pct, proxy=not a.no_proxy, idle_client_timeout_s=a.idle_client_timeout, tls=a.tls, cross_az=a.cross_az,
             locks_per_container=a.locks_per_container, deploy_overlap=not a.no_deploy_overlap)
    out = compute(i, load_coeffs(a.coeffs)); out["_inputs"] = i
    if a.json: print(json.dumps(out, indent=1)); return
    if out["_warning"]: print(out["_warning"], file=sys.stderr)
    for k in ("SetMaxOpenConns", "SetMaxIdleConns", "SetConnMaxIdleTime_s", "SetConnMaxLifetime_s"): print(f"{k:<22}{out[k]}")
    print("intermediate:", json.dumps(out["_intermediate"]))
    guess = [k for k, s in out["_provenance"].items() if s.startswith("default")]
    if guess: print("NOTE: coefficients not measured (default/guess):", ", ".join(guess), file=sys.stderr)


if __name__ == "__main__":
    main()
