# trigger-isolation — db.r6g.large

**問い**: Proxy が Aurora の接続を保持する原因は何か(ログインだけ、Ping、SELECT 1、プリペアド、GET_LOCK)

**仮説**: ログイン、Ping 1 回、SELECT 1 は保持しない(下限のまま)。プリペアドと GET_LOCK は、ピン留めされた接続数だけ保持し、RELEASE_LOCK では解除されない。

**読み方**: 各 run の dbconn と pinned の最大値を、connect(対照)と比べる。ソケットを閉じたあと下限に戻るまでの時間も見る。

値は、繰り返しの中央値 [最小–最大]。baseline 未達の run は表に含めているが、下の警告を確認すること。
接続数は **Sum(Proxy 全体)を主**とする。Max は、前回までの結果と比べるための列(Proxy の 1 ノード分しか見ない: docs/metrics.md)。

| run | n | ClientConn Sum最大 | DBConn Sum最大 | Pinned Sum最大 | Borrowed Sum最大 | DB−Client Sum最大 | ClientConn Max | DBConn Max | Pinned Max | Borrowed Max | DB−Client Max | 下限へ戻る(分・Proxy全体 Sum) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| connect | 1 | 10 | 10 | – | – | 10 | 8 | 5 | – | – | 5 | 0.8 |
| ping | 0 | – | – | – | – | – | – | – | – | – | – | – |
| ping-loop | 1 | 10 | 11 | – | – | 11 | 6 | 6 | – | – | 6 | – |
| select1 | 1 | 10 | 10 | – | – | 10 | 7 | 5 | – | – | 5 | 0.4 |
| prepared | 1 | 10 | 11 | 10 | 10 | 11 | 6 | 6 | 6 | 6 | 6 | 0.7 |
| getlock | 1 | 10 | 12 | 10 | 10 | 12 | 7 | 7 | 7 | 7 | 7 | 1 |
| getlock-release | 1 | 10 | 11 | 10 | 10 | 11 | 6 | 6 | 6 | 6 | 6 | 0.2 |
| getlock-close | 1 | 0 | 12 | – | – | 12 | 0 | 7 | – | – | 7 | 0.5 |

## 1 run = 1 行（runs.csv と同じ。Status は lab/criteria.json の基準）

| run | rep | status | client_sum | db_sum | pinned_sum | db_minus_client_sum | client_max | db_max | pinned_max | pin_ratio | cpu_p50 | cpu_p95 | cpu_max | borrowlat_max_us | hold_avg_ms | hold_p99_ms | wait_count | max_idle_closed | errors | db_minus_client_max | settle_min |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| connect | 1 | OK | 10 | 10 |  | 10 | 8 | 5 |  |  | 6.558 | 8.225 | 8.225 |  |  |  |  |  | 0 | 5 | 0.8 |
| ping-loop | 1 | OK | 10 | 11 |  | 11 | 6 | 6 |  |  | 6.467 | 7.767 | 7.767 | 1982 |  |  |  |  | 0 | 6 |  |
| select1 | 1 | OK | 10 | 10 |  | 10 | 7 | 5 |  |  | 6.783 | 8.233 | 8.233 | 254 |  |  |  |  | 0 | 5 | 0.4 |
| prepared | 1 | OK | 10 | 11 | 10 | 11 | 6 | 6 | 6 | 0.162 | 6.767 | 8.55 | 8.55 | 1610 |  |  |  |  | 0 | 6 | 0.7 |
| getlock | 1 | OK | 10 | 12 | 10 | 12 | 7 | 7 | 7 | 0.189 | 6.383 | 7.717 | 7.717 | 3095 |  |  |  |  | 0 | 7 | 1 |
| getlock-release | 1 | OK | 10 | 11 | 10 | 11 | 6 | 6 | 6 | 0.162 | 6.467 | 7.542 | 7.542 | 521 |  |  |  |  | 0 | 6 | 0.2 |
| getlock-close | 1 | OK | 0 | 12 |  | 12 | 0 | 7 |  |  | 6.5 | 7.917 | 7.917 | 2782 |  |  |  |  | 0 | 7 | 0.5 |

## 警告
なし
