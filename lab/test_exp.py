"""Unit tests for the run-validity logic in exp.py (no AWS needed):  python3 -m unittest test_exp  (from lab/)."""
import datetime as dt, json, pathlib, tempfile, unittest

import exp

T0 = dt.datetime(2026, 11, 16, 0, 0, 0, tzinfo=dt.timezone.utc)


def write_run(rdir, rc=0, unmet=False, series=("client", "dbconn", "cpu"), os_points=None, os_error=None):
    rdir.mkdir(parents=True, exist_ok=True)
    meta = {"rc": rc, "baseline": {"unmet": unmet}}
    (rdir / "metrics.json").write_text(json.dumps({"series": {k: {"points": [["2026-11-16T00:00:00Z", 1]]} for k in series}}))
    pts = [[(T0 + dt.timedelta(seconds=i)).strftime("%Y-%m-%dT%H:%M:%SZ"), 10.0] for i in range(os_points or 0)]
    (rdir / "os_cpu.json").write_text(json.dumps({"points": pts, "error": os_error}))
    return meta


class Judge(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = pathlib.Path(self.tmp.name)
        self.end = T0 + dt.timedelta(seconds=120)

    def tearDown(self):
        self.tmp.cleanup()

    def test_valid_run(self):
        meta = write_run(self.dir, os_points=130)
        j = exp.judge({"required_metrics": ["client", "dbconn", "cpu", "os_cpu"]}, self.dir, meta, T0, self.end)
        self.assertTrue(j["valid"], j)

    def test_nonzero_exit_is_invalid(self):
        meta = write_run(self.dir, rc=1)
        j = exp.judge({}, self.dir, meta, T0, self.end)
        self.assertFalse(j["valid"])
        self.assertIn("rc=1", j["reasons"])

    def test_target_not_reached_is_kept_but_not_valid(self):
        meta = write_run(self.dir, rc=3)
        j = exp.judge({}, self.dir, meta, T0, self.end)
        self.assertEqual(j["reasons"], ["target_not_reached"])

    def test_baseline_unmet(self):
        meta = write_run(self.dir, unmet=True)
        self.assertIn("baseline_unmet", exp.judge({}, self.dir, meta, T0, self.end)["reasons"])

    def test_missing_required_series(self):
        meta = write_run(self.dir, series=("client",))
        r = exp.judge({}, self.dir, meta, T0, self.end)["reasons"]
        self.assertIn("metric_missing:dbconn", r)
        self.assertIn("metric_missing:cpu", r)

    def test_os_cpu_required_but_empty(self):
        meta = write_run(self.dir, os_points=0, os_error="no stream")
        self.assertIn("os_cpu_missing", exp.judge({"required_metrics": ["os_cpu"]}, self.dir, meta, T0, self.end)["reasons"])

    def test_os_cpu_sparse(self):
        meta = write_run(self.dir, os_points=30)          # 30 samples for a ~100 s window: not one per second
        r = exp.judge({"required_metrics": ["os_cpu"]}, self.dir, meta, T0, self.end)["reasons"]
        self.assertTrue(any(x.startswith("os_cpu_sparse") for x in r), r)

    def test_os_cpu_not_required_is_not_checked(self):
        meta = write_run(self.dir, os_points=0, os_error="x")
        self.assertTrue(exp.judge({}, self.dir, meta, T0, self.end)["valid"])


class Manifest(unittest.TestCase):
    E = {"scenario": "patterns", "common": ["--mode=warmup"], "floor": 100}

    def test_same_inputs_same_hash(self):
        r = {"id": "a", "args": ["--warm=true"]}
        self.assertEqual(exp.manifest_of(self.E, r, "db.r6g.large", "abc", "bin1"), exp.manifest_of(self.E, r, "db.r6g.large", "abc", "bin1"))

    def test_changed_inputs_change_hash(self):
        r = {"id": "a", "args": ["--warm=true"]}
        base = exp.manifest_of(self.E, r, "db.r6g.large", "abc", "bin1")
        self.assertNotEqual(base, exp.manifest_of(self.E, {"id": "a", "args": ["--warm=false"]}, "db.r6g.large", "abc", "bin1"))   # args
        self.assertNotEqual(base, exp.manifest_of(self.E, r, "db.r6g.xlarge", "abc", "bin1"))                                         # infra
        self.assertNotEqual(base, exp.manifest_of(self.E, r, "db.r6g.large", "def", "bin1"))                                          # source version
        self.assertNotEqual(base, exp.manifest_of(self.E, r, "db.r6g.large", "abc", "bin2"))                                          # binary
        self.assertNotEqual(base, exp.manifest_of(dict(self.E, floor=6), r, "db.r6g.large", "abc", "bin1"))                           # run condition


if __name__ == "__main__":
    unittest.main()
