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

    def test_empty_zero_gauges_mean_zero_not_missing(self):
        # smoke test: CloudWatch sends no points for pinned / intx / borrowed while they were never above 0
        meta = write_run(self.dir, series=("client", "dbconn", "cpu"))
        m = json.loads((self.dir / "metrics.json").read_text())
        m["series"].update({"pinned": {"points": []}, "intx": {"points": []}})
        (self.dir / "metrics.json").write_text(json.dumps(m))
        j = exp.judge({"required_metrics": ["client", "dbconn", "cpu", "pinned", "intx"]}, self.dir, meta, T0, self.end)
        self.assertTrue(j["valid"], j)

    def test_empty_non_zero_gauge_is_still_missing(self):
        meta = write_run(self.dir, series=("client", "dbconn", "cpu"))
        m = json.loads((self.dir / "metrics.json").read_text())
        m["series"]["client"]["points"] = []                  # ClientConnections must have points while a client is connected
        (self.dir / "metrics.json").write_text(json.dumps(m))
        self.assertIn("metric_missing:client", exp.judge({}, self.dir, meta, T0, self.end)["reasons"])

    def test_failed_zero_gauge_query_is_missing(self):
        meta = write_run(self.dir, series=("client", "dbconn", "cpu"))
        m = json.loads((self.dir / "metrics.json").read_text())
        m["series"]["pinned"] = {"points": [], "error": "AccessDenied"}
        (self.dir / "metrics.json").write_text(json.dumps(m))
        self.assertIn("metric_missing:pinned", exp.judge({"required_metrics": ["pinned"]}, self.dir, meta, T0, self.end)["reasons"])

    def test_os_cpu_not_required_is_not_checked(self):
        meta = write_run(self.dir, os_points=0, os_error="x")
        self.assertTrue(exp.judge({}, self.dir, meta, T0, self.end)["valid"])


class Baseline(unittest.TestCase):
    def test_idle_pool_is_not_a_leftover(self):
        # An used Proxy keeps its idle pool (MaxIdleConnectionsPercent 10% of max_connections 100 = 10, 5 per node) for hours with no
        # client (2026-11-17). That is the baseline a run starts from, not a leftover to wait for.
        self.assertTrue(exp.baseline_met([10, 10], exp.FLOOR_DEFAULT))
        self.assertTrue(exp.baseline_met([0, 0], exp.FLOOR_DEFAULT))       # a never-used Proxy has 0
        self.assertTrue(exp.baseline_met([10, 9], exp.FLOOR_DEFAULT))

    def test_connections_above_the_idle_pool_are_a_leftover(self):
        # e.g. after a burst of 40 concurrent queries 25 backend connections stay for about 5 minutes
        self.assertFalse(exp.baseline_met([25, 25], exp.FLOOR_DEFAULT))
        self.assertFalse(exp.baseline_met([10, 25], exp.FLOOR_DEFAULT))
        self.assertFalse(exp.baseline_met([25, 10], exp.FLOOR_DEFAULT))

    def test_not_met_without_two_points(self):
        self.assertFalse(exp.baseline_met([], exp.FLOOR_DEFAULT))
        self.assertFalse(exp.baseline_met([0], exp.FLOOR_DEFAULT))

    def test_default_floor_is_the_whole_proxy_idle_pool(self):
        self.assertEqual(exp.FLOOR_DEFAULT, 10)


class Manifest(unittest.TestCase):
    E = {"scenario": "patterns", "common": ["--mode=warmup"], "floor": 100}

    def test_infrastructure_conditions_change_the_hash(self):
        r = {"id": "a", "args": ["--warm=true"]}
        import os
        old = {k: os.environ.get(k) for k in ("REQUIRE_TLS", "PROXY_AUTH")}
        try:
            os.environ["REQUIRE_TLS"], os.environ["PROXY_AUTH"] = "true", "native"
            base = exp.manifest_of(self.E, r, "db.r6g.large", "abc", "bin1")
            os.environ["REQUIRE_TLS"] = "false"
            self.assertNotEqual(base, exp.manifest_of(self.E, r, "db.r6g.large", "abc", "bin1"))      # TLS required or not
            os.environ["REQUIRE_TLS"], os.environ["PROXY_AUTH"] = "true", "sha2"
            self.assertNotEqual(base, exp.manifest_of(self.E, r, "db.r6g.large", "abc", "bin1"))      # password auth of the Proxy
        finally:
            for k, v in old.items():
                os.environ.pop(k, None) if v is None else os.environ.__setitem__(k, v)

    def test_infra_conditions_reports_the_stack(self):
        import os
        os.environ.update(REQUIRE_TLS="false", PROXY_AUTH="sha2", ENV_ID="b", PROXY_NAME="rdsproxy-b")
        try:
            self.assertEqual(exp.infra_conditions(), {"require_tls": "false", "proxy_auth": "sha2", "env_id": "b", "proxy_name": "rdsproxy-b"})
            self.assertEqual(exp.proxy_name(), "rdsproxy-b")
        finally:
            for k in ("REQUIRE_TLS", "PROXY_AUTH", "ENV_ID", "PROXY_NAME"): os.environ.pop(k, None)

    def test_proxy_defaults_follow_the_deploy(self):
        import os
        os.environ["PROXY_DEBUG_LOGGING_AT_DEPLOY"] = "true"
        try:
            self.assertTrue(exp.proxy_defaults()["DebugLogging"])
        finally:
            os.environ.pop("PROXY_DEBUG_LOGGING_AT_DEPLOY", None)
        self.assertFalse(exp.proxy_defaults()["DebugLogging"])

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
