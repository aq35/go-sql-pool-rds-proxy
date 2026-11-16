# tls-latency — db.r6g.large

**問い**: TLS と別 AZ のとき、MaxIdleConns=0 は遅延をどれだけ悪化させるか。いくつから頭打ちか

**仮説**: 0 では接続の作り直しで遅延が数倍になり、2〜5 で頭打ちになる。

**読み方**: hold_avg_ms と hold_p99_ms(client.log の最終行)を run id ごとに比べる。DatabaseConnections は読まない(遅延の実験)。

値は、繰り返しの中央値 [最小–最大]。baseline 未達の run は表に含めているが、下の警告を確認すること。

| run | n |  |
|---|---|
| plain-same-0 | 1 |  |
| plain-same-5 | 0 |  |
| tls-same-0 | 0 |  |
| tls-same-5 | 0 |  |
| tls-cross-0 | 0 |  |
| tls-cross-1 | 0 |  |
| tls-cross-2 | 0 |  |
| tls-cross-5 | 0 |  |
| tls-cross-10 | 0 |  |

## 1 run = 1 行（runs.csv と同じ。Status は lab/criteria.json の基準）

| run | rep | status | client_max | db_max | pinned_max | pin_ratio | cpu_p50 | cpu_p95 | cpu_max | borrowlat_max_us | hold_avg_ms | hold_p99_ms | wait_count | max_idle_closed | errors | db_minus_client_max | settle_min |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| plain-same-0 | 1 | RUN_FAILED |  |  |  |  |  |  |  |  |  |  |  |  | 0 |  |  |

## 警告
- plain-same-0.r1: 実行が異常終了（rc=None）
