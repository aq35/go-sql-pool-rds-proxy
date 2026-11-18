# exceed-cases — db.r6g.large

**問い**: Proxy の DatabaseConnections が ClientConnections を上回るのは、どんなときか

**仮説**: 下限(約5本)と、クライアントが閉じたあと約5分の残留。短命なピン留め接続の繰り返しでは、残留は一定数で頭打ちになる。

**読み方**: 各 run の db_minus_client_max(summary.md)と、run 終了後に下限へ戻るまでの時間を見る。select1 を対照にする。 steady-settz(セッションごとの time_zone)は、前回の結果が明確なので、この実行からは外した。

値は、繰り返しの中央値 [最小–最大]。baseline 未達の run は表に含めているが、下の警告を確認すること。
接続数は **Sum(Proxy 全体)を主**とする。Max は、前回までの結果と比べるための列(Proxy の 1 ノード分しか見ない: docs/metrics.md)。

| run | n | ClientConn Sum最大 | DBConn Sum最大 | Pinned Sum最大 | Borrowed Sum最大 | DB−Client Sum最大 | ClientConn Max | DBConn Max | Pinned Max | Borrowed Max | DB−Client Max | 下限へ戻る(分・Proxy全体 Sum) | 失敗 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| churn-getlock | 1 | 3 | 10 | 3 | 3 | 10 | 3 | 5 | 3 | 3 | 5 | 0.6 | 0 |
| churn-prepared | 1 | 13 | 40 | 3 | 13 | 48 | 7 | 25 | 3 | 7 | 27 | – | 0 |
| burst | 1 | 40 | 54 | – | 40 | 54 | 27 | 27 | – | 27 | 27 | 5.3 | 0 |

## 1 run = 1 行（runs.csv と同じ。Status は lab/criteria.json の基準）

| run | rep | status | client_sum | db_sum | pinned_sum | db_minus_client_sum | client_max | db_max | pinned_max | pin_ratio | cpu_p50 | cpu_p95 | cpu_max | borrowlat_max_us | hold_avg_ms | hold_p99_ms | wait_count | max_idle_closed | errors | db_minus_client_max | settle_min |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| churn-getlock | 1 | OK | 3 | 10 | 3 | 10 | 3 | 5 | 3 | 0.081 | 5.992 | 7.417 | 7.417 | 3499 |  |  |  |  | 0 | 5 | 0.6 |
| churn-prepared | 1 | BORROW_SLOW | 13 | 40 | 3 | 48 | 7 | 25 | 3 | 0.081 | 6.325 | 7.592 | 7.592 | 1.3169e+06 |  |  |  |  | 0 | 27 |  |
| burst | 1 | BORROW_SLOW | 40 | 54 |  | 54 | 27 | 27 |  |  | 6.342 | 7.175 | 7.175 | 6.91176e+06 |  |  |  |  | 0 | 27 | 5.3 |

## 警告
なし
