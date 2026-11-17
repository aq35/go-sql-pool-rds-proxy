# pat-floor — db.r6g.large

**問い**: クライアントが 1 本もいないとき、Proxy 全体は、Aurora へ何本の接続を保持するか。前回の「5 本」は、Maximum(最も大きい 1 ノードの値)で測っていた。Sum(Proxy 全体)では何本か。Proxy は何ノードか

**仮説**: Proxy は 2 ノードで、それぞれが 5 本ほどを保持しているので、DatabaseConnections の Sum は約 10、Maximum は約 5、Average は約 5 になる(スモークテストで、ClientConnections の Sum = 10、Maximum = 7、Average = 5.0 だったことからの推定)。

**読み方**: proxy_dbconn_sum、proxy_dbconn_max、proxy_dbconn_avg を並べて読む。Sum ÷ Average が、報告しているノード数の推定(proxy_nodes_est)。前回の A1「5 本」が、Maximum の値で、Proxy 全体は、その約 2 倍なら、仮説どおり。クライアントはいない(go_open_max などは出ない)ので、この run は、Proxy の裏の接続だけを見る。run の前に、DatabaseConnections(Maximum)が下限へ戻るのを待ってから始まる(floor は既定の 6)。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | note | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_intx_max | aurora_cpu_p50 | aurora_cpu_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| no-clients |  | 2 | no_client_connections | settled | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 4.3 [4.1–4.5] | 56.75 [56.3–57.2] | 3 | 2 | 10 | 11 | 0 | 4.3 [2.3–6.3] |

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
