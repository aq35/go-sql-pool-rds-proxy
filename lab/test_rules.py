#!/usr/bin/env python3
"""Sanity tests of the rule function (run by `make check`)."""
import rules

C = rules.load_coeffs("/nonexistent")
base = dict(vcpu=2, max_connections=1000, containers=3, workers=10, active_avg=4, deploy_overlap=True)
a = rules.compute(base, C)
assert 1 <= a["SetMaxIdleConns"] <= a["SetMaxOpenConns"] <= base["workers"], a
assert a["SetConnMaxIdleTime_s"] < 1800 and a["SetConnMaxLifetime_s"] > a["SetConnMaxIdleTime_s"], a
b = rules.compute(dict(base, vcpu=4, workers=40), C)                       # more vCPU -> not smaller
assert b["SetMaxOpenConns"] >= rules.compute(dict(base, workers=40), C)["SetMaxOpenConns"], b
t = rules.compute(dict(base, tls=True, cross_az=True), C)                  # costlier reconnect -> idle not smaller
assert t["SetMaxIdleConns"] >= a["SetMaxIdleConns"], (t, a)
x = rules.compute(dict(base, max_connections=100, locks_per_container=10), C)  # 60 pinned vs a 56 budget
assert x["_feasible"] is False and x["_warning"], x
y = rules.compute(dict(base, max_connections=100, locks_per_container=2, deploy_overlap=False), C)
assert y["_feasible"], y
n = rules.compute(dict(base, proxy=False), C)
assert n["SetConnMaxIdleTime_s"] == 300, n
print("rules OK")
