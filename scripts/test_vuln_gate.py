"""Tests for vuln_gate.py:  python3 scripts/test_vuln_gate.py"""
import datetime as dt, json, sys, pathlib, unittest
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import vuln_gate as g

TODAY = dt.date(2026, 11, 16)


def stream(*objs): return "\n".join(json.dumps(o, indent=1) for o in objs)      # govulncheck -json prints indented, concatenated objects


CALLED = {"finding": {"osv": "GO-X-1", "fixed_version": "v9", "trace": [{"module": "m/x", "version": "v1", "package": "m/x/p", "function": "F"}]}}
IMPORTED = {"finding": {"osv": "GO-X-2", "fixed_version": "v9", "trace": [{"module": "m/y", "version": "v1", "package": "m/y/p"}]}}
MODULE_ONLY = {"finding": {"osv": "GO-X-3", "fixed_version": "v9", "trace": [{"module": "m/z", "version": "v1"}]}}


class Gate(unittest.TestCase):
    def test_empty_or_crashed_output_is_not_a_clean_scan(self):
        with self.assertRaises(ValueError): g.findings("")
        with self.assertRaises(ValueError): g.findings(stream({"progress": {"message": "x"}}))     # no config message: the tool never started

    def test_clean_scan_with_config_has_no_findings(self):
        self.assertEqual(g.findings(stream({"config": {"scanner_name": "govulncheck"}})), {})

    def test_only_called_findings_count(self):
        f = g.findings(stream({"config": {}}, {"osv": {"id": "GO-X-1"}}, CALLED, IMPORTED, MODULE_ONLY))
        self.assertEqual(list(f), ["GO-X-1"])

    def test_unknown_called_vulnerability_blocks(self):
        problems, _, _ = g.gate(g.findings(stream({"config": {}}, CALLED)), [], TODAY)
        self.assertEqual(len(problems), 1)
        self.assertTrue(problems[0].startswith("NEW"))

    def test_allowlisted_passes_but_stays_listed(self):
        problems, rows, _ = g.gate(g.findings(stream({"config": {}}, CALLED)), [{"id": "GO-X-1", "expires": "2026-12-31", "reason": "r"}], TODAY)
        self.assertEqual(problems, [])
        self.assertEqual(len(rows), 1)

    def test_expired_allowlist_entry_blocks(self):
        problems, _, _ = g.gate(g.findings(stream({"config": {}}, CALLED)), [{"id": "GO-X-1", "expires": "2026-11-15", "reason": "r"}], TODAY)
        self.assertTrue(problems[0].startswith("EXPIRED"))

    def test_stale_allowlist_entries_are_reported(self):
        _, _, stale = g.gate({}, [{"id": "GO-OLD", "expires": "2027-01-01", "reason": "r"}], TODAY)
        self.assertEqual(stale, ["GO-OLD"])


if __name__ == "__main__":
    unittest.main()
