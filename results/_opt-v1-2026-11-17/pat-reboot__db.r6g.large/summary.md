# pat-reboot — db.r6g.large

**問い**: writer を再起動(フェイルオーバーの代わり)すると、定常負荷のクライアントは、どれだけの間、どんなエラーになり、どう回復するか。Proxy 経由と直結で違うか。ConnMaxLifetime(なし、30 秒)で、回復は変わるか。障害中に、新しい接続の試行は、どれだけ増えるか

**仮説**: Proxy 経由でも直結でも、再起動の間(1〜数分)は、クエリが失敗する。直結は、接続が切れるので、接続の無効エラー(invalid connection など)が出て、プールは接続を作り直し、回復する。Proxy 経由は、Proxy が接続を保持するので、エラーの種類が違い、回復が早い(または遅い)可能性がある。ConnMaxLifetime は、再起動の回復には、ほとんど影響しない(接続は、切れた時点で破棄されるため)。障害中、ワーカーは、毎回、再接続を試すので、新しい接続の試行は、大きく増える(再接続の嵐)。

**読み方**: run(proxy-* と direct-*、life0 と life30s)ごとに、first_error_at_s、recovered_at_s、outage_s(5 秒続けて成功が続いた最初の秒までの差)、errors、error_kinds、new_connections を比べる。reboot_issued_at_s で、再起動の命令の時刻と、最初のエラーの時刻のずれも読む。1 回の再起動の所要時間は、Aurora の状態に依存するので、揺らぐ(repeat 2 の範囲)。per-second の ok / err は、client.log の PATTERN-SECOND 行。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | reboot_issued_at_s | first_error_at_s | recovered_at_s | outage_s | ok | errors | error_kinds | new_connections | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_intx_max | proxy_nodes_est | aurora_cpu_p50 | aurora_cpu_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| proxy-life0 | lifetime_s=0.00 workers=10 | 1 | 45 | 48 | 54 | 6 | 95991 | 19 | deadline-exceeded:10|invalid-connection:9 | 29 | warm | 7 | 10 | 5 | 5 | 10 | 5 | 0 | 0 | 2 | 4.7 | 64.4 | 4 | 2.02 | 60 | -16413 | 0.8 | 12.4 |
| proxy-life30s | lifetime_s=30.00 workers=10 | 1 | 45 | 56 | 62 | 6 | 95949 | 17 | deadline-exceeded:10|invalid-connection:7 | 136 | warm | 6 | 10 | 5 | 12 | 23 | 11.5 | 0 | 0 | 2 | 5 | 63.9 | 3 | 2 | 33 | -7 | 0.5 | 10.5 |
| direct-life0 | lifetime_s=0.00 workers=10 | 1 | 45 | 46 | 52 | 6 | 102620 | 1090 | connection-refused:1080|invalid-connection:10 | 1100 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | – | 5 | 62.3 | 3 | 2 | 41 | 11 | 0.3 | 12.3 |
| direct-life30s | lifetime_s=30.00 workers=10 | 1 | 45 | 55 | 62 | 7 | 102639 | 1120 | connection-refused:1120 | 1297 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | – | 5 | 67.5 | 3 | 2 | 20 | -8 | 0.3 | 9.8 |

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
