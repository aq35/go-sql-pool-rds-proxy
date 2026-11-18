# trigger-isolation — db.r6g.large

**問い**: Proxy が Aurora の接続を保持する原因は何か(ログインだけ、Ping、SELECT 1、プリペアド、GET_LOCK)

**仮説**: ログイン、Ping 1 回、SELECT 1 は保持しない(下限のまま)。プリペアドと GET_LOCK は、ピン留めされた接続数だけ保持し、RELEASE_LOCK では解除されない。

**読み方**: 各 run の dbconn と pinned の最大値を、connect(対照)と比べる。ソケットを閉じたあと下限に戻るまでの時間も見る。

値は、繰り返しの中央値 [最小–最大]。baseline 未達の run は表に含めているが、下の警告を確認すること。
接続数は **Sum(Proxy 全体)を主**とする。Max は、前回までの結果と比べるための列(Proxy の 1 ノード分しか見ない: docs/metrics.md)。

| run | n | ClientConn Sum最大 | DBConn Sum最大 | Pinned Sum最大 | Borrowed Sum最大 | DB−Client Sum最大 | ClientConn Max | DBConn Max | Pinned Max | Borrowed Max | DB−Client Max | 下限へ戻る(分・Max基準) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| connect | 1 | 10 | 0 | – | – | 0 | 6 | 0 | – | – | 0 | 0.4 |
| ping | 0 | – | – | – | – | – | – | – | – | – | – | – |
| ping-loop | 1 | 10 | 10 | – | – | 10 | 6 | 6 | – | – | 6 | 0.7 |
| select1 | 1 | 10 | 9 | – | – | 9 | 5 | 5 | – | – | 5 | 0 |
| prepared | 1 | 10 | 10 | 10 | 10 | 10 | 5 | 5 | 5 | 5 | 5 | 0.3 |
| getlock | 1 | 10 | 11 | 10 | 10 | 11 | 6 | 6 | 6 | 6 | 6 | 0.6 |
| getlock-release | 1 | 10 | 11 | 10 | 10 | 11 | 6 | 6 | 6 | 6 | 6 | 0.9 |
| getlock-close | 1 | 0 | 10 | – | – | 10 | 0 | 5 | – | – | 5 | 0.2 |

## 1 run = 1 行（runs.csv と同じ。Status は lab/criteria.json の基準）

| run | rep | status | client_sum | db_sum | pinned_sum | db_minus_client_sum | client_max | db_max | pinned_max | pin_ratio | cpu_p50 | cpu_p95 | cpu_max | borrowlat_max_us | hold_avg_ms | hold_p99_ms | wait_count | max_idle_closed | errors | db_minus_client_max | settle_min |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| connect | 1 | OK | 10 | 0 |  | 0 | 6 | 0 |  |  | 7.1 | 8.434 | 8.434 |  |  |  |  |  | 0 | 0 | 0.4 |
| ping-loop | 1 | OK | 10 | 10 |  | 10 | 6 | 6 |  |  | 7.074 | 10.192 | 10.192 | 38790 |  |  |  |  | 0 | 6 | 0.7 |
| select1 | 1 | OK | 10 | 9 |  | 9 | 5 | 5 |  |  | 6.717 | 8.116 | 8.116 | 544 |  |  |  |  | 0 | 5 | 0 |
| prepared | 1 | OK | 10 | 10 | 10 | 10 | 5 | 5 | 5 | 0.135 | 6.275 | 8.356 | 8.356 | 1960 |  |  |  |  | 0 | 5 | 0.3 |
| getlock | 1 | OK | 10 | 11 | 10 | 11 | 6 | 6 | 6 | 0.162 | 7.142 | 7.85 | 7.85 | 1486 |  |  |  |  | 0 | 6 | 0.6 |
| getlock-release | 1 | OK | 10 | 11 | 10 | 11 | 6 | 6 | 6 | 0.162 | 6.308 | 7.689 | 7.689 | 1621 |  |  |  |  | 0 | 6 | 0.9 |
| getlock-close | 1 | OK | 0 | 10 |  | 10 | 0 | 5 |  |  | 6.142 | 8.283 | 8.283 | 201 |  |  |  |  | 0 | 5 | 0.2 |

## 警告
なし
