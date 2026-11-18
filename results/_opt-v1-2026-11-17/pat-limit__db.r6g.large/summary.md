# pat-limit — db.r6g.large

**問い**: 上限は、どこにあるか。(1) 直結で、Aurora の max_connections(100)に達したとき、何本目で、どんなエラーか。(2) Proxy は、アイドルのクライアント接続を、何本まで受けて、その間、Aurora の接続は、増えるか。(3) 忙しいクライアントが Proxy の裏の上限(80)を超えたとき、超えた分は、どうなるか(BorrowTimeout 30 秒)

**仮説**: (1) 直結は、100 から、Proxy が保持する分(アイドル 10 本)と、監視用の接続を引いた本数のあたりで、MySQL のエラー 1040(Too many connections)になる。(2) Proxy は、400 本のアイドルクライアントを受け付け、Aurora の接続は、アイドルの 10 本のままで増えない。(3) 忙しいクライアント(SLEEP 中)が 80 を超えると、超えた分は、BorrowTimeout(30 秒)ほど待たされ、エラーになる(エラーが出るまでの時間が約 30 秒)。

**読み方**: direct-exhaust: opened、failed、first_failure_at_connection、error_kinds(mysql-1040)。idle-clients: opened / failed と、proxy_client_sum、proxy_dbconn_sum(クライアントが多くても、Aurora の接続が増えないか)。busy-clients: ok、errors、error_after_p50_s / error_after_max_s(約 30 秒なら、BorrowTimeout)、proxy_dbconn_sum が、上限(MaxDatabaseConnectionsAllowed)に達したか。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | wanted | opened | failed | first_failure_at_connection | error_kinds | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_pinned_sum | proxy_pinned_avg | proxy_intx_max | proxy_nodes_est | aurora_cpu_p50 | aurora_cpu_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max | clients | hold_s | ok | errors | error_after_p50_s | error_after_max_s |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| direct-exhaust | variant=direct-exhaust | 1 | 130 | 87 | 43 | 87 | mysql-1040:43 | settled | 0 | 0 | 0 | 5 | 10 | 5 | 0 | 0 | 0 | 0 | 2 | 3 | 8.9 | 2 | 2 | 107 | 13 | 0.1 | 0.7 | – | – | – | – | – | – |
| idle-clients-400 | variant=idle-clients | 1 | 400 | 400 | 0 | -1 | none | settled | 203 | 400 | 200 | 37 | 74 | 37 | 0 | 0 | 0 | 0 | 2 | 4 | 34.2 | 2 | 2 | 20 | 14 | 0.1 | 3 | – | – | – | – | – | – |
| busy-clients-100 | variant=busy-clients | 1 | – | – | – | – | invalid-connection:74|mysql-9501:26 | settled | 50 | 100 | 50 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | – | 4.5 | 25.3 | 76 | 72.16 | 84 | 12 | 0.1 | 0.8 | 100 | 90 | 0 | 100 | 60.01 | 60.02 |

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
