"""Tests for check_pitfalls.py:  python3 scripts/test_check_pitfalls.py"""
import pathlib, sys, tempfile, unittest
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import check_pitfalls as c

GOOD = """2026/11/17 PATTERN-RESULT mode=pitfalls test=rows-not-closed max_open=2 in_use_while_open=2 third_query=blocked after_close=ok
2026/11/17 PATTERN-RESULT mode=pitfalls test=rows-fully-read max_open=2 in_use_after_loop=0 third_query=ok
2026/11/17 PATTERN-RESULT mode=pitfalls test=queryrow-without-scan max_open=1 in_use=1 second_query=blocked
2026/11/17 PATTERN-RESULT mode=pitfalls test=tx-not-finished max_open=1 second_query=blocked after_rollback=ok
2026/11/17 PATTERN-RESULT mode=pitfalls test=session-state-leak max_open=1 user_variable_seen_by_next_request=42 time_zone_seen_by_next_request=+09:00 leaked=true
2026/11/17 PATTERN-RESULT mode=pitfalls test=ctx-cancel-discards cancelled_query_failed=true next_query_ok=true connections_before=1 connections_after=2 new_connections_after_cancel=1
2026/11/17 PATTERN-RESULT mode=pitfalls test=maxidle-zero max_idle=0 sequential_queries=20 connections_opened=20 max_idle_closed=20
2026/11/17 PATTERN-RESULT mode=pitfalls test=maxidle-zero max_idle=2 sequential_queries=20 connections_opened=1 max_idle_closed=0
2026/11/17 PATTERN-RESULT mode=pitfalls test=lifetime-reconnects lifetime_ms=600 queries_every_ms=300 queries=10 connections_opened=5 max_lifetime_closed=4
2026/11/17 PATTERN-RESULT mode=gorm-pool test=tx-uses-db-inside max_open=1 result=blocked ms=3001.00
2026/11/17 PATTERN-RESULT mode=gorm-pool test=hook-uses-global-db max_open=1 result=blocked ms=3000.00
2026/11/17 PATTERN-RESULT mode=gorm-pool test=preload-two max_open=1 result=ok ms=25.00
2026/11/17 PATTERN-RESULT mode=gorm-pool test=tx-uses-db-inside max_open=2 result=ok ms=80.00
"""


def run(text):
    with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as f:
        f.write(text)
    return c.main([f.name])


class Check(unittest.TestCase):
    def test_expected_outcomes_pass(self):
        self.assertEqual(run(GOOD), 0)

    def test_a_changed_behaviour_fails(self):
        # e.g. a library change that no longer leaks the connection of an unread QueryRow
        bad = GOOD.replace("test=queryrow-without-scan max_open=1 in_use=1 second_query=blocked", "test=queryrow-without-scan max_open=1 in_use=0 second_query=ok")
        self.assertEqual(run(bad), 1)

    def test_a_missing_probe_fails(self):
        self.assertEqual(run("\n".join(l for l in GOOD.splitlines() if "tx-not-finished" not in l)), 1)

    def test_empty_output_fails(self):
        self.assertEqual(run(""), 1)


if __name__ == "__main__":
    unittest.main()
