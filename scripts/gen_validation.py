#!/usr/bin/env python3
"""Cross-validation: settings computed by lab/rules.py (coefficients from lab/coeffs.json) for conditions that were NOT used to
derive the coefficients. Writes lab/experiments/rule-validation.json. Run after derive_coeffs.py, then `make push` and campaign day2.
A condition passes if every repetition has Status OK (lab/criteria.json: Aurora CPU, pin saturation, waits, errors, borrow latency)."""
import json, pathlib, sys
root = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(root / "lab"))
import rules

C = rules.load_coeffs()
# unseen conditions: container counts and worker counts different from the measurement runs (3 x 10), lock mix, TLS + cross-AZ
CONDS = {"v1": dict(containers=5, workers=8, locks_per_container=4, active_avg=3, tls=False, cross_az=False),
         "v2": dict(containers=2, workers=15, locks_per_container=0, active_avg=6, tls=True, cross_az=True),
         "v3": dict(containers=4, workers=12, locks_per_container=6, active_avg=4, tls=True, cross_az=False)}
runs = []
for vc, max_conn in ((2, 100), (4, 100)):
    for name, c in CONDS.items():
        i = dict(vcpu=vc, max_connections=max_conn, proxy_max_pct=80, deploy_overlap=False, **c)    # steady state: no deploy overlap is run
        r = rules.compute(i, C)
        args = [f"--containers={c['containers']}", f"--total-conns={r['SetMaxOpenConns'] * c['containers']}", f"--workers-per-pool={c['workers']}",
                f"--max-idle={r['SetMaxIdleConns']}", f"--idle-time={r['SetConnMaxIdleTime_s']}s", f"--lifetime={r['SetConnMaxLifetime_s']}s",
                f"--lock-holders={c['locks_per_container']}", "--interpolate=true", "--lock-mode=none", "--hold=5ms", "--query-every=20ms", "--rotate-every=24h"]
        if c["tls"]: args.append("--tls=skip-verify")
        if c["cross_az"]: args.append("--proxy-az=cross")
        runs.append({"id": f"{name}-{vc}vcpu", "args": args, "rules_output": {k: v for k, v in r.items() if k.startswith("Set")}, "feasible": r["_feasible"]})
exp = {"name": "rule-validation",
       "question": "計測に使っていない条件(コンテナ数、ワーカー数、ロック数、TLS・別 AZ)に、ルールの式から出した設定値を当てると、合格基準を満たすか",
       "hypothesis": "式から出した設定値で、Aurora CPU が 85% を超えず、ピン留めの飽和・プールの待ち・エラー・借用の遅れがない。",
       "reading": "run ごとの Status が全 rep で OK なら合格。OK でなければ、そのフラグ(CPU_OVER / PIN_SATURATED / WAIT_SPIKE / ERRORS / BORROW_SLOW)に対応する係数(k / alpha / beta,gamma / delta)を見直す。feasible=false の条件は、ルールが「実行不可」と判定したもの(合否に含めない)。",
       "scenario": "pool-load", "common": ["--duration=8m"], "repeat": 2, "observe_after_s": 360, "pass_criteria": "status_ok",
       "coeff_sources": {k: v["source"] for k, v in C.items()}, "runs": runs}
(root / "lab/experiments/rule-validation.json").write_text(json.dumps(exp, indent=1, ensure_ascii=False) + "\n")
camp = {"name": "day2", "description": "検証: day1 の測定から導いた係数で、未知の条件を流す(2 vCPU → 4 vCPU)。約 3 時間",
        "steps": [{"aurora_class": "db.r6g.large"}, {"exp": "rule-validation", "repeat": 2, "only": [r["id"] for r in runs if r["id"].endswith("2vcpu") and r["feasible"]]},
                  {"aurora_class": "db.r6g.xlarge"}, {"exp": "rule-validation", "repeat": 2, "only": [r["id"] for r in runs if r["id"].endswith("4vcpu") and r["feasible"]]}]}
(root / "lab/campaigns/day2.json").write_text(json.dumps(camp, indent=1, ensure_ascii=False) + "\n")
for r in runs: print(r["id"], r["rules_output"], "feasible" if r["feasible"] else "INFEASIBLE")
