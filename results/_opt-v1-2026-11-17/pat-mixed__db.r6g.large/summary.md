# pat-mixed — db.r6g.large

**問い**: 本番に近い混在負荷(30 秒おきのワーカー 3、1 秒おきのワーカー 6、Web 100 req/s × 1 リクエスト 6 クエリ、サイトごとに GET_LOCK の専用接続 1 本)で、Go のプール設定(GORM 既定: MaxOpen 100 / MaxIdle 2 と、ルールから出した値: MaxOpen 22 / MaxIdle 10)と、Web のクエリの流し方(直列、並列)で、遅延、エラー、接続数は、どう変わるか。サイトを 10 から 40 に増やすと、何が起きるか

**仮説**: GORM の既定(MaxIdle 2)は、バーストのたびに接続を閉じて作り直す(max_idle_closed が多い)ので、Web の p99 が悪化する。ルールの値(MaxOpen 22、MaxIdle 10)は、再接続が少なく、p99 が安定する。サイト(GET_LOCK のピン留め)が 10 から 40 に増えると、Proxy の裏の接続のうち、ピン留めの分が増え、通常のクエリが使える接続が減るので、サイト 40 では、待ち(pool_wait_count)と p99 が、悪化する。並列は、プールの小さい設定で、p99 が悪化しやすい。

**読み方**: 同じ sites で、gorm-default と rule、serial と parallel を比べる。class=web の p99_ms と errors、class=pools の pool_wait_count、max_idle_closed、new_connections_pools、pool_in_use_max。Proxy 側: proxy_pinned_sum(サイトの数 + α)、proxy_dbconn_sum、aurora_cpu_*。サイト 40 の run で、ピン留めが Proxy の上限(80)に対して何割か。繰り返しが 1 回なので、pat-control のばらつきと比べて、差を判断する。 条件は、設定(GORM 既定 / ルール)× サイト数(10 / 40)を主軸にし、並列は、ルールの設定でだけ、直列と比べる(8 → 6 run に絞った)。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | class | containers | max_idle | sites | web_mode | ok | errors | error_kinds | p50_ms | p99_ms | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_pinned_sum | proxy_pinned_avg | proxy_intx_max | proxy_nodes_est | aurora_cpu_p50 | aurora_cpu_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max | site_locks_held | site_lock_failures | pool_in_use_max | pool_open_max | pool_wait_count | max_idle_closed | max_lifetime_closed | new_connections_pools |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| sites10-gorm-default-serial | max_open=100 | 4 | fast×1/pools×1/slow×1/web×1 | 3 | 2 | 10 | serial | 1704 [29–28574] | 0 | none | 1.45 [1.4–10.72] | 17.86 [13.53–42.79] | settled | 10 | 17 | 8.5 | 12 | 23 | 11.5 | 6 | 10 | 5 | 0 | 2 | 26.2 | 56.2 | 6 | 2.46 | 33 | 570 | 1 | 3.2 | 10 | 0 | 8 | 9 | 0 | 459 | 0 | 465 |
| sites10-rule-serial | max_open=22 | 4 | fast×1/pools×1/slow×1/web×1 | 3 | 10 | 10 | serial | 1703 [29–28514] | 0 | none | 4.22 [4.1–25.03] | 13.35 [10.19–50.21] | settled | 14 | 26 | 13 | 13 | 24 | 12 | 6 | 10 | 5 | 0 | 2 | 26.9 | 63.7 | 6 | 2.44 | 34 | 569 | 1 | 3.7 | 10 | 0 | 15 | 16 | 0 | 0 | 0 | 16 |
| sites10-rule-parallel | max_open=22 | 4 | fast×1/pools×1/slow×1/web×1 | 3 | 10 | 10 | parallel | 1702 [29–28543] | 0 | none | 4.33 [4.33–6.35] | 13.44 [12.85–20.47] | settled | 24 | 40 | 20 | 21 | 41 | 20.5 | 6 | 10 | 5 | 0 | 2 | 22.6 | 65 | 7 | 2.47 | 51 | 570 | 1.4 | 4.8 | 10 | 0 | 24 | 38 | 0 | 223 | 0 | 253 |
| sites40-gorm-default-serial | max_open=100 | 4 | fast×1/pools×1/slow×1/web×1 | 3 | 2 | 40 | serial | 1705 [29–28381] | 0 | none | 1.93 [1.32–10.63] | 18.42 [17.46–42.19] | settled | 28 | 46 | 23 | 29 | 52 | 26 | 22 | 40 | 20 | 0 | 2 | 25.6 | 65 | 8 | 2.53 | 62 | 567 | 1 | 4.7 | 40 | 0 | 9 | 11 | 0 | 434 | 0 | 440 |
| sites40-rule-serial | max_open=22 | 4 | fast×1/pools×1/slow×1/web×1 | 3 | 10 | 40 | serial | 1703 [29–28596] | 0 | none | 4.17 [4.14–26.57] | 14.73 [7.38–51.38] | settled | 29 | 57 | 28.5 | 28 | 56 | 28 | 20 | 40 | 20 | 0 | 2 | 26.2 | 63.4 | 5 | 2.42 | 66 | 571 | 1.1 | 4 | 40 | 0 | 11 | 17 | 0 | 0 | 0 | 17 |
| sites40-rule-parallel | max_open=22 | 4 | fast×1/pools×1/slow×1/web×1 | 3 | 10 | 40 | parallel | 1703 [29–28302] | 0 | none | 4.5 [4.33–6.17] | 13.17 [9.69–18.35] | settled | 39 | 70 | 35 | 36 | 71 | 35.5 | 23 | 40 | 20 | 0 | 2 | 22.9 | 60.9 | 9 | 2.4 | 81 | 566 | 1.3 | 4.2 | 40 | 0 | 30 | 34 | 0 | 172 | 0 | 202 |

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
