# pat-driver-ab — db.r6g.large

**問い**: MySQL ドライバを v1.8.1(前回の実験)から v1.10.1(今回)に更新したことで、接続の扱い(Proxy に切られた接続への再接続、ピン留め、接続の確立)は変わったか。前回との差が、ドライバの違いか、条件の違いかの切り分け

**仮説**: プールと接続の再利用に関する大きな変化は、リリースノートにない。ピン留め(prepare の数)は、ドライバの版によらず同じ。Proxy が切った接続への再接続は、どちらの版も、透過的に行う(エラー 0)。差があるなら、v1.9.0 の「エラー処理の改善」に関係する、閉じた接続の扱いの違い。

**読み方**: 同じ条件の new(v1.10.1)と old(v1.8.1)を、run id の接尾辞で比べる。idle-gap(matrix。gap 65 秒だけ): new_conn_per_query、errors、app_reconnects、p99。pin: com_stmt_prepare_delta、proxy_pinned_sum。warmup: w1_p50_ms_med。2 回の範囲が重なる差は、結論にしない(pat-control のばらつきも参照)。binary の版は meta.json の go_deps / bin に残る。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | dedicated | queries | errors | new_connections | new_conn_per_query | app_reconnects | actual_idle_before_query_s_p50 | actual_start_interval_s_p50 | p50_success_ms | p99_success_ms | slow_queries | idle_time_closed | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_intx_max | proxy_nodes_est | aurora_cpu_p50 | aurora_cpu_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max | com_stmt_prepare_delta | com_stmt_execute_delta | connects | aurora_threads_max | go_open_max | session_time_zone | session_sql_mode_len | proxy_pinned_sum | proxy_pinned_avg | trials | warm_ms_med | w1_p50_ms_med | w1_max_ms_med | w2_p50_ms_med | proxy_intx_sum | proxy_intx_avg |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| matrix-new | gap_s=65.00 idle_time_s=0.00 workers=3 | 1 | true | 22 | 1 | 4 | 0.18 | 1 | 65 | 65 | 4.3 | 5.89 | 1 | 0 | warm | 4 | 5 | 2.5 | 8 | 15 | 7.5 | 0 | 0 | 2 | 3.5 | 35.8 | 3 | 2 | 23 | 11 | 0 | 2.2 | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – |
| matrix-new | gap_s=65.00 idle_time_s=0.00 workers=1 | 1 | false | 8 | 0 | 1 | 0.12 | 0 | 65 | 65.01 | 4.43 | 4.67 | 1 | 0 | warm | 4 | 5 | 2.5 | 8 | 15 | 7.5 | 0 | 0 | 2 | 3.5 | 35.8 | 3 | 2 | 23 | 11 | 0 | 2.2 | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – |
| matrix-new | gap_s=65.00 idle_time_s=45.00 workers=1 | 1 | false | 8 | 0 | 8 | 1 | 0 | 65 | 65.02 | 16.08 | 20.16 | 0 | 7 | warm | 4 | 5 | 2.5 | 8 | 15 | 7.5 | 0 | 0 | 2 | 3.5 | 35.8 | 3 | 2 | 23 | 11 | 0 | 2.2 | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – |
| matrix-old | gap_s=65.00 idle_time_s=0.00 workers=3 | 1 | true | 22 | 1 | 4 | 0.18 | 1 | 65 | 65.01 | 4.56 | 14.8 | 2 | 0 | warm | 3 | 5 | 2.5 | 8 | 15 | 7.5 | 0 | 0 | 2 | 3.5 | 39 | 3 | 2 | 25 | 11 | 0 | 2.7 | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – |
| matrix-old | gap_s=65.00 idle_time_s=45.00 workers=1 | 1 | false | 8 | 0 | 8 | 1 | 0 | 65 | 65.02 | 15.52 | 22.58 | 0 | 7 | warm | 3 | 5 | 2.5 | 8 | 15 | 7.5 | 0 | 0 | 2 | 3.5 | 39 | 3 | 2 | 25 | 11 | 0 | 2.7 | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – |
| matrix-old | gap_s=65.00 idle_time_s=0.00 workers=1 | 1 | false | 8 | 0 | 2 | 0.25 | 0 | 65 | 65.01 | 4.92 | 13.75 | 1 | 0 | warm | 3 | 5 | 2.5 | 8 | 15 | 7.5 | 0 | 0 | 2 | 3.5 | 39 | 3 | 2 | 25 | 11 | 0 | 2.7 | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – | – |
| pin-base-new | variant=base workers=10 | 1 | – | 32937 | 0 | – | – | – | – | – | – | – | – | – | warm | 8 | 10 | 5 | 18 | 27 | 13.5 | 0 | 0 | 2 | 4.6 | 49.5 | 3 | 2.02 | 32 | 196 | 0.5 | 2.5 | 0 | 0 | 10 | 33 | 10 | SYSTEM | 0 | 0 | 0 | – | – | – | – | – | – | – |
| pin-base-old | variant=base workers=10 | 1 | – | 33619 | 0 | – | – | – | – | – | – | – | – | – | warm | 8 | 10 | 5 | 18 | 30 | 15 | 0 | 0 | 2 | 5 | 28.6 | 3 | 2.02 | 39 | 200 | 0.5 | 2.3 | 0 | 0 | 10 | 40 | 10 | SYSTEM | 0 | 0 | 0 | – | – | – | – | – | – | – |
| pin-prep-false-new | variant=prep-false workers=10 | 1 | – | 31181 | 0 | – | – | – | – | – | – | – | – | – | warm | 6 | 10 | 6 | 18 | 30 | 15 | 6 | 0 | 2 | 5 | 28.2 | 3 | 2.01 | 42 | 187 | 0.5 | 3.2 | 31181 | 31181 | 10 | 43 | 10 | SYSTEM | 0 | 10 | 5 | – | – | – | – | – | – | – |
| pin-prep-false-old | variant=prep-false workers=10 | 1 | – | 30889 | 0 | – | – | – | – | – | – | – | – | – | warm | 5 | 10 | 5 | 15 | 30 | 15 | 5 | 0 | 2 | 5.5 | 43.2 | 3 | 2.02 | 42 | 185 | 0.5 | 4 | 30889 | 30889 | 10 | 43 | 10 | SYSTEM | 0 | 10 | 5 | – | – | – | – | – | – | – |
| pin-gorm-prepare-new | variant=gorm-prepare workers=10 | 1 | – | 33434 | 0 | – | – | – | – | – | – | – | – | – | warm | 5 | 10 | 5 | 15 | 29 | 14.5 | 5 | 0 | 2 | 4.5 | 45.5 | 3 | 2.04 | 41 | 199 | 0.4 | 3.2 | 10 | 33434 | 10 | 42 | 10 | SYSTEM | 0 | 10 | 5 | – | – | – | – | – | – | – |
| pin-gorm-prepare-old | variant=gorm-prepare workers=10 | 1 | – | 32791 | 0 | – | – | – | – | – | – | – | – | – | warm | 5 | 10 | 5 | 25 | 48 | 24 | 5 | 0 | 2 | 4 | 24.9 | 3 | 2.01 | 41 | 196 | 0.4 | 2.2 | 10 | 32791 | 10 | 42 | 10 | SYSTEM | 0 | 10 | 5 | – | – | – | – | – | – | – |
| warmup-cold-new | conns=32 pause_s=10.00 query=select1 warm=false | 1 | – | – | – | – | – | – | – | – | – | – | – | – | warm | 0 | 0 | 0 | 37 | 74 | 37 | 0 | 0 | 2 | 3.5 | 42.5 | 2 | 2 | 70 | 18 | 0.1 | 1.5 | – | – | – | – | – | – | – | 0 | 0 | 6 | 0 | 15.43 | 22.42 | 4.78 | – | – |
| warmup-cold-old | conns=32 pause_s=10.00 query=select1 warm=false | 1 | – | – | – | – | – | – | – | – | – | – | – | – | warm | 0 | 0 | 0 | 37 | 74 | 37 | 0 | 0 | 2 | 4 | 25.5 | 2 | 2 | 84 | 17 | 0.1 | 1.8 | – | – | – | – | – | – | – | 0 | 0 | 6 | 0 | 12.71 | 22.23 | 4.79 | 0 | 0 |

エラーの例: invalid_connection

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
