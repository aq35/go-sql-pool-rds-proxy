# pat-gorm-pool — db.r6g.large

**問い**: MaxOpenConns を 1〜2 に絞ると、GORM のどの書き方で、アプリが固まるか(トランザクション中に別の接続が要る書き方)

**仮説**: トランザクションの中で、tx ではなく db で別のクエリを呼ぶ書き方と、フックの中で db を使う書き方は、MaxOpenConns=1 で 3 秒のタイムアウトまで固まる。Preload と、関連を含む Create は、固まらない。MaxOpenConns=2 では、どれも固まらない。

**読み方**: pat-gorm-pool の summary.md で、test ごとに max_open=1 と 2 の result(ok / blocked)を比べる。blocked が、MaxOpenConns の下限が 2 以上であることの根拠。peak_in_use は短い操作では取りこぼしがある(参考値)。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | result | ms | peak_in_use | proxy_client_max | proxy_dbconn_max | proxy_pinned_max | proxy_intx_max |
|---|---|---|---|---|---|---|---|---|---|
| gorm | max_open=2 test=tx-uses-tx | 3 | ok | 41.2 [18.37–54.13] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=2 test=tx-uses-db-inside | 3 | ok | 78.19 [59.52–175] | 2 | 0 | 0 | 0 | 0 |
| gorm | max_open=2 test=preload-two | 3 | ok | 24.29 [14–37.88] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=2 test=create-with-associations | 3 | ok | 30.92 [23.41–129] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=2 test=hook-uses-tx | 3 | ok | 17.63 [17.04–66.41] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=2 test=hook-uses-global-db | 3 | ok | 46.36 [25.24–48.2] | 2 | 0 | 0 | 0 | 0 |
| gorm | max_open=1 test=tx-uses-tx | 3 | ok | 25.24 [18.98–33.79] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=1 test=tx-uses-db-inside | 3 | blocked | 3001 [3000–3001] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=1 test=preload-two | 3 | ok | 44.7 [22.68–57.1] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=1 test=create-with-associations | 3 | ok | 41.24 [24.06–48.69] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=1 test=hook-uses-tx | 3 | ok | 23.23 [18.15–38.82] | 1 | 0 | 0 | 0 | 0 |
| gorm | max_open=1 test=hook-uses-global-db | 3 | blocked | 3001 [3000–3001] | 1 | 0 | 0 | 0 | 0 |

エラーの例: context_deadline_exceeded / context_deadline_exceeded;_sql:_transaction_has_already_been_committed_or_rolled

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
