# lifecycle-sync — db.r6g.large

**問い**: アプリの ConnMaxIdleTime と、Proxy の IdleClientTimeout の関係で、接続エラー(invalid / bad connection、EOF)や再接続による遅延が出る境界はどこか

**仮説**: ConnMaxIdleTime が IdleClientTimeout 未満なら、エラーも再接続の遅延スパイクも出ない。IdleClientTimeout を超える(または未設定)と、Proxy に切られた接続の再接続が起き、p99 が悪化する。go-sql-driver は idle 接続の切断を検知して透過的に再接続するため、エラーは 0 のままで、遅延だけに出る可能性もある。

**読み方**: run ごとに、エラー数と p99(summary.md)を比べる。ConnMaxIdleTime / IdleClientTimeout の比が 1 未満の run と、1 以上の run を分けて読む。エラーが 0 でも p99 が悪化するなら、ルールは「比を 0.5〜0.8 にする」を支持。フェイルオーバーは、この実験では扱わない。

値は、繰り返しの中央値 [最小–最大]。baseline 未達の run は表に含めているが、下の警告を確認すること。

| run | n | ClientConn max | DBConn max | DB−Client max | 下限へ戻る(分) | 平均(ms) | p99(ms) | idle closed |
|---|---|---|---|---|---|---|---|---|
| idle-none | 1 | 10 | 9 | 7 | 0.6 | 9.3 | 12 | 0 |
| idle-30s | 1 | 13 | 11 | 8 | 0.4 | 9.9 | 18 | 0 |
| idle-50s | 1 | 11 | 11 | 9 | 0.3 | 9.64 | 23 | 0 |
| idle-90s | 1 | 11 | 6 | 5 | 0.1 | 10.09 | 12 | 0 |

## 1 run = 1 行（runs.csv と同じ。Status は lab/criteria.json の基準）

| run | rep | status | client_max | db_max | pinned_max | pin_ratio | cpu_p50 | cpu_p95 | cpu_max | borrowlat_max_us | hold_avg_ms | hold_p99_ms | wait_count | max_idle_closed | errors | db_minus_client_max | settle_min |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| idle-none | 1 | OK | 10 | 9 |  |  | 5.592 | 5.867 | 5.867 | 3574 | 9.3 | 12 | 0 | 0 | 0 | 7 | 0.6 |
| idle-30s | 1 | OK | 13 | 11 |  |  | 5.817 | 6.683 | 6.683 | 10916 | 9.9 | 18 | 0 | 0 | 0 | 8 | 0.4 |
| idle-50s | 1 | OK | 11 | 11 |  |  | 5.867 | 6.033 | 6.033 | 3117 | 9.64 | 23 | 0 | 0 | 0 | 9 | 0.3 |
| idle-90s | 1 | OK | 11 | 6 |  |  | 5.433 | 6.383 | 6.383 | 123 | 10.09 | 12 | 0 | 0 | 0 | 5 | 0.1 |

## 警告
なし
