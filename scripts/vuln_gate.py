#!/usr/bin/env python3
"""Gate for `govulncheck -json` output.

  govulncheck -json ./... > vuln.json; scripts/vuln_gate.py vuln.json docs/known-vulnerabilities.json

Only vulnerabilities whose vulnerable function is CALLED from this code count (the same set govulncheck's text output lists
as "affected"). Each one must be in the allowlist, and an allowlist entry has an expiry date, so a known finding cannot be
forgotten. Exit 1 on: a called vulnerability that is not allowlisted, or an allowlist entry that has expired.
Allowlisted findings are still listed (and written to the GitHub job summary) so that they stay visible.
"""
import datetime as dt, json, os, sys


def findings(text):
    """Called findings: {osv id: {module, found, fixed}} from the concatenated JSON objects of govulncheck -json."""
    dec, i, out, saw_config = json.JSONDecoder(), 0, {}, False
    while i < len(text):
        while i < len(text) and text[i].isspace(): i += 1
        if i >= len(text): break
        obj, i = dec.raw_decode(text, i)
        saw_config = saw_config or "config" in obj
        f = obj.get("finding")
        if not f: continue
        tr = f.get("trace") or [{}]
        if "function" not in tr[0]: continue          # imported or required only: not called
        out[f["osv"]] = {"module": tr[0].get("module", "?"), "found": tr[0].get("version", "?"), "fixed": f.get("fixed_version", "?")}
    if not saw_config:      # an empty or crashed run must not read as "no vulnerabilities"
        raise ValueError("no govulncheck config message in the input: the tool did not run (empty or truncated output)")
    return out


def gate(found, allow, today):
    ids = {a["id"]: a for a in allow}
    problems, rows = [], []
    for osv, d in sorted(found.items()):
        a = ids.get(osv)
        if a is None:
            problems.append(f"NEW  {osv}: {d['module']} {d['found']} (fixed in {d['fixed']}) is called and not in the allowlist")
            rows.append((osv, d, "NEW (blocks)"))
        elif dt.date.fromisoformat(a["expires"]) < today:
            problems.append(f"EXPIRED  {osv}: allowlisted until {a['expires']}; fix it or review the entry ({a['reason'][:80]})")
            rows.append((osv, d, f"expired {a['expires']} (blocks)"))
        else:
            rows.append((osv, d, f"known until {a['expires']}"))
    stale = sorted(set(ids) - set(found))
    return problems, rows, stale


def main(argv):
    try:
        found = findings(open(argv[1]).read())
    except ValueError as e:
        print("::error title=govulncheck::" + str(e) if os.environ.get("GITHUB_ACTIONS") else str(e), file=sys.stderr)
        return 1
    allow = json.load(open(argv[2]))
    problems, rows, stale = gate(found, allow, dt.date.today())
    lines = [f"### govulncheck gate: {len(found)} called vulnerabilities, {len(problems)} blocking", "",
             "| id | module | found | fixed | status |", "|---|---|---|---|---|"]
    lines += [f"| {o} | {d['module']} | {d['found']} | {d['fixed']} | {s} |" for o, d, s in rows]
    if stale: lines += ["", "Allowlist entries no longer reported (remove them): " + ", ".join(stale)]
    text = "\n".join(lines)
    print(text)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as f: f.write(text + "\n")
    for p in problems: print("::error title=govulncheck::" + p if os.environ.get("GITHUB_ACTIONS") else p, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
