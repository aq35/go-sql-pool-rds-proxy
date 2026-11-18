# idle-sweep — db.r6g.large

**問い**: MaxIdleConns を変えると、Proxy→Aurora の接続数(DatabaseConnections)は変わるか

**仮説**: 変わらない。MaxIdleConns が決めるのはクライアント→Proxy の接続数で、Proxy→Aurora は同時に実行中のクエリ数で決まる。

**読み方**: run id ごとに、繰り返し(r1..r3)の中央値と範囲を比べる。範囲が重なれば差なし。baseline.unmet のある run は除外して読む。client_max は MaxIdleConns とともに増えるはず。

値は、繰り返しの中央値 [最小–最大]。baseline 未達の run は表に含めているが、下の警告を確認すること。
接続数は **Sum(Proxy 全体)を主**とする。Max は、前回までの結果と比べるための列(Proxy の 1 ノード分しか見ない: docs/metrics.md)。

| run | n | ClientConn Sum最大 | DBConn Sum最大 | Borrowed Sum最大 | DB−Client Sum最大 | ClientConn Max | DBConn Max | Borrowed Max | DB−Client Max | 下限へ戻る(分・Proxy全体 Sum) | 平均(ms) | p99(ms) | idle closed |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| idle0 | 1 | 29 | 54 | 26 | 46 | 17 | 28 | 14 | 25 | 5.9 | 128 | 213 | 110298 |
| idle5 | 1 | 40 | 41 | 28 | 28 | 28 | 23 | 18 | 15 | 5.7 | 124 | 203 | 5170 |
| idle10 | 1 | 50 | 38 | 28 | 24 | 26 | 20 | 15 | 13 | 5.5 | 127 | 223 | 0 |

## 1 run = 1 行（runs.csv と同じ。Status は lab/criteria.json の基準）

| run | rep | status | client_sum | db_sum | pinned_sum | db_minus_client_sum | client_max | db_max | pinned_max | pin_ratio | cpu_p50 | cpu_p95 | cpu_max | borrowlat_max_us | hold_avg_ms | hold_p99_ms | wait_count | max_idle_closed | errors | db_minus_client_max | settle_min |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| idle0 | 1 | OK | 29 | 54 |  | 46 | 17 | 28 |  |  | 6.225 | 7.592 | 7.592 | 28304 | 127.63 | 213 | 0 | 110298 | 0 | 25 | 5.9 |
| idle5 | 1 | OK | 40 | 41 |  | 28 | 28 | 23 |  |  | 6.2 | 7.575 | 7.575 | 22700 | 124.42 | 203 | 0 | 5170 | 0 | 15 | 5.7 |
| idle10 | 1 | OK | 50 | 38 |  | 24 | 26 | 20 |  |  | 6.492 | 8.708 | 8.708 | 4646 | 126.68 | 223 | 0 | 0 | 0 | 13 | 5.5 |

## 警告
なし
