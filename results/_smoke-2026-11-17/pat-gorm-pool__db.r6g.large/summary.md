# pat-gorm-pool — db.r6g.large

**問い**: MaxOpenConns を 1〜2 に絞ると、GORM のどの書き方で、アプリが固まるか(トランザクション中に別の接続が要る書き方)

**仮説**: トランザクションの中で、tx ではなく db で別のクエリを呼ぶ書き方と、フックの中で db を使う書き方は、MaxOpenConns=1 で 3 秒のタイムアウトまで固まる。Preload と、関連を含む Create は、固まらない。MaxOpenConns=2 では、どれも固まらない。

**読み方**: pat-gorm-pool の summary.md で、test ごとに max_open=1 と 2 の result(ok / blocked)を比べる。blocked が、MaxOpenConns の下限が 2 以上であることの根拠。peak_in_use は短い操作では取りこぼしがある(参考値)。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | result | ms | peak_in_use | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_intx_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| gorm | max_open=2 test=tx-uses-tx | 1 | ok | 26.6 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=2 test=tx-uses-db-inside | 1 | ok | 1137 | 2 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=2 test=preload-two | 1 | ok | 18.65 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=2 test=create-with-associations | 1 | ok | 31.97 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=2 test=hook-uses-tx | 1 | ok | 29.63 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=2 test=hook-uses-global-db | 1 | ok | 29.52 | 2 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=1 test=tx-uses-tx | 1 | ok | 26.18 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=1 test=tx-uses-db-inside | 1 | blocked | 3001 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=1 test=preload-two | 1 | ok | 27.57 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=1 test=create-with-associations | 1 | ok | 31.11 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=1 test=hook-uses-tx | 1 | ok | 25.5 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |
| gorm | max_open=1 test=hook-uses-global-db | 1 | blocked | 3000 | 1 | warm | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 2.12 | 12 | 24 | 0.6 | 3.5 |

エラーの例: context_deadline_exceeded / context_deadline_exceeded;_sql:_transaction_has_already_been_committed_or_rolled

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
