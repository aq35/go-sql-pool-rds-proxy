# proxy-vs-direct — db.r6g.large

**問い**: Proxy を経由すると、直結(Aurora に直接)に比べて、遅延はどれだけ増えるか。クライアント接続が多く、各接続の使用が少ないとき、Proxy は、Aurora の接続数を、どれだけ減らすか(多重化の効果)

**仮説**: Proxy 経由は、直結より、平均で、数 ms 遅い(1 ホップ分)。接続あたりの使用が少ない(各ワーカーが 500 ms に 1 回、5 ms のクエリ)負荷では、Proxy の裏の接続数(DatabaseConnections の Sum)は、クライアント接続数(30、60)より、はるかに少ない(多重化)。直結では、Aurora の接続数は、クライアント接続数に等しい。

**読み方**: proxy-N と cluster-N を、同じ N で比べる。遅延: hold_avg_ms、hold_p99_ms。接続数: Proxy 経由は proxy_dbconn_sum(Proxy→Aurora)と client_sum。直結は、Aurora 側の接続数(summary の aurora_conn、または threads_connected_max)。max_connections=100 なので、N=60 の直結は、上限の 60% を使う。直結の run は、Proxy の指標(client / dbconn)が 0 になるので、required_metrics に含めない。

値は、繰り返しの中央値 [最小–最大]。baseline 未達の run は表に含めているが、下の警告を確認すること。
接続数は **Sum(Proxy 全体)を主**とする。Max は、前回までの結果と比べるための列(Proxy の 1 ノード分しか見ない: docs/metrics.md)。

| run | n | ClientConn Sum最大 | DBConn Sum最大 | Borrowed Sum最大 | DB−Client Sum最大 | ClientConn Max | DBConn Max | Borrowed Max | DB−Client Max | 下限へ戻る(分・Proxy全体 Sum) | 平均(ms) | p99(ms) | idle closed |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| proxy-30 | 1 | 30 | 24 | 1 | 46 | 18 | 12 | 1 | 25 | 5.8 | 9.2 | 12 | 0 |
| proxy-60 | 1 | 60 | 46 | 2 | 45 | 31 | 25 | 2 | 25 | 5.7 | 9.71 | 14 | 0 |
| cluster-30 | 1 | 0 | 10 | – | 10 | 0 | 5 | – | 5 | 1 | 5.23 | 5 | 0 |
| cluster-60 | 1 | 0 | 10 | – | 24 | 0 | 5 | – | 13 | 0.4 | 5.25 | 5 | 0 |

## 1 run = 1 行（runs.csv と同じ。Status は lab/criteria.json の基準）

| run | rep | status | client_sum | db_sum | pinned_sum | db_minus_client_sum | client_max | db_max | pinned_max | pin_ratio | cpu_p50 | cpu_p95 | cpu_max | borrowlat_max_us | hold_avg_ms | hold_p99_ms | wait_count | max_idle_closed | errors | db_minus_client_max | settle_min |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| proxy-30 | 1 | OK | 30 | 24 |  | 46 | 18 | 12 |  |  | 6.983 | 7.192 | 7.192 | 1707 | 9.2 | 12 | 0 | 0 | 0 | 25 | 5.8 |
| proxy-60 | 1 | OK | 60 | 46 |  | 45 | 31 | 25 |  |  | 8.033 | 8.067 | 8.067 | 4169 | 9.71 | 14 | 0 | 0 | 0 | 25 | 5.7 |
| cluster-30 | 1 | OK | 0 | 10 |  | 10 | 0 | 5 |  |  | 6.517 | 7.508 | 7.508 |  | 5.23 | 5 | 0 | 0 | 0 | 5 | 1 |
| cluster-60 | 1 | OK | 0 | 10 |  | 24 | 0 | 5 |  |  | 7.417 | 8.3 | 8.3 |  | 5.25 | 5 | 0 | 0 | 0 | 13 | 0.4 |

## 警告
なし
