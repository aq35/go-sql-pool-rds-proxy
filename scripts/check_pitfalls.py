#!/usr/bin/env python3
"""Assert the outcomes of `lab --scenario=patterns --mode=pitfalls` and `--mode=gorm-pool` (run against any MySQL; CI uses a service container).

  check_pitfalls.py output1.txt [output2.txt ...]

These are properties of Go's database/sql and GORM, not of AWS. If one fails, either the library changed its behaviour (worth knowing) or
the probe is wrong. Exit 1 on any failed assertion or any missing probe.
"""
import sys


def parse(text):
    """{(mode, test, max_open or None): {key: value}} from PATTERN-RESULT lines."""
    out = {}
    for line in text.splitlines():
        if "PATTERN-RESULT " not in line: continue
        kv = dict(x.split("=", 1) for x in line.split("PATTERN-RESULT ", 1)[1].split() if "=" in x)
        mode, test = kv.get("mode"), kv.get("test")
        if not test: continue
        extra = kv.get("max_open") if mode == "gorm-pool" else kv.get("max_idle") if test == "maxidle-zero" else None
        out[(mode, test, extra)] = kv
    return out


def checks(r):
    """List of (description, key, predicate over the parsed results)."""
    P = lambda mode, test, extra=None: r.get((mode, test, extra))
    g = lambda key, **want: (lambda d: d is not None and all(d.get(k) == v for k, v in want.items()))
    num = lambda d, k: int(d[k])
    return [
        ("a Rows that is neither read to the end nor closed keeps its connection (the pool blocks)",
         P("pitfalls", "rows-not-closed"), lambda d: d["in_use_while_open"] == "2" and d["third_query"] == "blocked" and d["after_close"] == "ok"),
        ("reading Rows to the end releases the connection",
         P("pitfalls", "rows-fully-read"), lambda d: d["in_use_after_loop"] == "0" and d["third_query"] == "ok"),
        ("a QueryRow whose Scan is never called keeps its connection",
         P("pitfalls", "queryrow-without-scan"), lambda d: d["in_use"] == "1" and d["second_query"] == "blocked"),
        ("a Tx that is neither committed nor rolled back keeps its connection",
         P("pitfalls", "tx-not-finished"), lambda d: d["second_query"] == "blocked" and d["after_rollback"] == "ok"),
        ("SET through the pool leaks into the next request on the same connection",
         P("pitfalls", "session-state-leak"), lambda d: d["leaked"] == "true" and d["user_variable_seen_by_next_request"] == "42"
         and d["time_zone_seen_by_next_request"] == "+09:00"),
        ("a query cancelled by its context loses its connection",
         P("pitfalls", "ctx-cancel-discards"), lambda d: d["cancelled_query_failed"] == "true" and d["next_query_ok"] == "true"
         and num(d, "new_connections_after_cancel") >= 1),
        ("SetMaxIdleConns(0) reconnects for every query",
         P("pitfalls", "maxidle-zero", "0"), lambda d: num(d, "connections_opened") == num(d, "sequential_queries")),
        ("SetMaxIdleConns(2) reuses one connection",
         P("pitfalls", "maxidle-zero", "2"), lambda d: num(d, "connections_opened") == 1),
        ("SetConnMaxLifetime makes old connections reconnect",
         P("pitfalls", "lifetime-reconnects"), lambda d: num(d, "connections_opened") >= 3 and num(d, "max_lifetime_closed") >= 2),
        ("GORM: with MaxOpenConns=1, using db instead of tx inside a transaction blocks",
         P("gorm-pool", "tx-uses-db-inside", "1"), g("result", result="blocked")),
        ("GORM: with MaxOpenConns=1, a hook that uses the global db blocks",
         P("gorm-pool", "hook-uses-global-db", "1"), g("result", result="blocked")),
        ("GORM: with MaxOpenConns=1, Preload / associations / tx-using hooks do not block",
         P("gorm-pool", "preload-two", "1"), g("result", result="ok")),
        ("GORM: with MaxOpenConns=2 the nested-db patterns complete",
         P("gorm-pool", "tx-uses-db-inside", "2"), g("result", result="ok")),
    ]


def main(paths):
    r = parse("\n".join(open(p, errors="replace").read() for p in paths))
    failed = 0
    for desc, d, pred in checks(r):
        try: ok = d is not None and bool(pred(d))
        except Exception: ok = False
        print(("PASS " if ok else "FAIL ") + desc + ("" if d is not None else "   (probe missing from the output)"))
        failed += not ok
    print(f"{len(checks(r)) - failed} passed, {failed} failed")
    return 1 if failed else 0


if __name__ == "__main__":
    if len(sys.argv) < 2: sys.exit(__doc__)
    sys.exit(main(sys.argv[1:]))
