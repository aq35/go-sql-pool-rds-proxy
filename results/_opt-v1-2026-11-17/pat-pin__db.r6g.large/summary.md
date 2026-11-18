# pat-pin — db.r6g.large

**問い**: どの Go / GORM の設定が、RDS Proxy のセッションをピン留めするか(interpolateParams=false、GORM PrepareStmt、DSN の time_zone と sql_mode、loc だけ、トランザクション内の待ち)

**仮説**: interpolateParams=false(パラメータ付きクエリがサーバ側プリペアドになる)と、GORM の PrepareStmt=true は、ピン留めされる。DSN の time_zone / sql_mode は、接続の確立で SET が走るが、ピン留めされない可能性がある(先行調査では time_zone はピン留め 0)。loc だけは、SET がないのでピン留めされない。長いトランザクションは、ピン留めではなく、InTransaction と、接続の専有として現れる。 DSN で接続ごとに送る `SET` のうち、サーバーが追跡できない変数(sql_select_limit、ユーザー変数、autocommit=0 など)は、ピン留めされる可能性がある。

**読み方**: variant ごとに、Proxy の pinned max と intx max、com_stmt_prepare_delta を、base と比べる。DatabaseConnections は、前の run の残りを含むので読まない(この実験は、待ち時間を省いている)。 session_time_zone は、サーバーのセッションが実際に使っている time_zone(loc だけの run で変わらないこと、set-tz の run で +09:00 になることを確認する)。 go_open_max は Go 側が数えた接続数(Proxy の ClientConnections は実数より少なく出るため、ピン留めの数は、これと比べて読む)。run の間の隙間は設けていない(Aurora 側の com_stmt_prepare_delta は run ごとの正確な値。CloudWatch の 1 分値は、前後の run と混ざりうるので、ピン留めされない変種と交互に並べて、見分ける)。set-* は、DSN の param として、接続のたびに `SET k=v` が走る。この実験の間だけ、Proxy のデバッグログを有効にする(ピン留めの理由を CloudWatch Logs の `/aws/rds/proxy/<proxy 名>` で確かめるため。AWS は、調査のときだけ一時的に使うことを推奨している。ログに SQL が含まれうるので、終わると元に戻す)。proxy_*_max(Maximum)と proxy_*_sum(Sum、AWS の推奨)を並べて読む。 変種は、ピン留めされるもの(prep-false、gorm-prepare)と、されないものを、交互に並べてある(前の run のピン留めが、次の 1 分値に混ざったとき、見分けるため)。Aurora 側の com_stmt_prepare_delta は、run ごとの正確な値で、主な根拠。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | queries | errors | com_stmt_prepare_delta | com_stmt_execute_delta | connects | aurora_threads_max | go_open_max | session_time_zone | session_sql_mode_len | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_pinned_sum | proxy_pinned_avg | proxy_intx_max | proxy_intx_sum | proxy_intx_avg | proxy_nodes_est | aurora_cpu_p50 | aurora_cpu_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| base | variant=base workers=10 | 1 | 18401 | 0 | 0 | 0 | 10 | 63 | 10 | SYSTEM | 0 | warm | 5 | 10 | 5 | 25 | 50 | 25 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | 4.5 | 25.5 | 3 | 2.02 | 62 | 197 | 0.5 | 3.5 |
| prep-false | variant=prep-false workers=10 | 1 | 17219 | 0 | 17219 | 17219 | 10 | 34 | 10 | SYSTEM | 0 | warm | 6 | 10 | 5 | 11 | 21 | 10.5 | 6 | 10 | 5 | 1 | 1 | 1 | 2 | 5.5 | 45.7 | 3 | 2.01 | 33 | 185 | 0.5 | 3 |
| loc-only | variant=loc-only workers=10 | 1 | 18509 | 0 | 0 | 0 | 10 | 34 | 10 | SYSTEM | 0 | warm | 5 | 10 | 5 | 11 | 21 | 10.5 | 5 | 10 | 5 | 0 | 0 | 0 | 2 | 5 | 27.9 | 3 | 2.02 | 33 | 198 | 0.5 | 2.3 |
| gorm-prepare | variant=gorm-prepare workers=10 | 1 | 18221 | 0 | 10 | 18221 | 10 | 34 | 10 | SYSTEM | 0 | warm | 6 | 10 | 5 | 12 | 23 | 11.5 | 5 | 10 | 5 | 0 | 0 | 0 | 2 | 5 | 49.9 | 3 | 2.03 | 33 | 196 | 0.4 | 2.2 |
| set-tz | variant=set-tz workers=10 | 1 | 18351 | 0 | 0 | 0 | 10 | 36 | 10 | +09:00 | 0 | warm | 6 | 10 | 5 | 17 | 30 | 15 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | 4.5 | 28.4 | 3 | 2.03 | 35 | 197 | 0.5 | 2.8 |
| set-sqlmode | variant=set-sqlmode workers=10 | 1 | 18254 | 0 | 0 | 0 | 10 | 43 | 10 | SYSTEM | 42 | warm | 6 | 10 | 5 | 21 | 39 | 19.5 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | 4.5 | 25.5 | 3 | 2.03 | 42 | 196 | 0.5 | 4.2 |
| long-tx | variant=long-tx workers=10 | 1 | 3730 | 0 | 0 | 0 | 10 | 52 | 10 | SYSTEM | 0 | warm | 6 | 10 | 5 | 21 | 39 | 19.5 | 6 | 10 | 5 | 6 | 10 | 5 | 2 | 4 | 24.4 | 2 | 2 | 51 | 162 | 0.2 | 3 |
| set-select-limit | variant=set-select-limit workers=10 | 1 | 18413 | 0 | 0 | 0 | 10 | 52 | 10 | SYSTEM | 0 | warm | 6 | 10 | 5 | 15 | 30 | 15 | 6 | 10 | 5 | 0 | 0 | 0 | 2 | 5 | 25 | 3 | 2.01 | 51 | 197 | 0.4 | 2.5 |
| set-user-var | variant=set-user-var workers=10 | 1 | 18141 | 0 | 0 | 0 | 10 | 44 | 10 | SYSTEM | 0 | warm | 5 | 10 | 5 | 11 | 22 | 11 | 5 | 10 | 5 | 0 | 0 | 0 | 2 | 4.5 | 25.1 | 3 | 2.01 | 43 | 195 | 0.4 | 2.7 |
| set-wait-timeout | variant=set-wait-timeout workers=10 | 1 | 18164 | 0 | 0 | 0 | 10 | 35 | 10 | SYSTEM | 0 | warm | 6 | 10 | 5 | 16 | 30 | 15 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | 5 | 26 | 3 | 2.02 | 34 | 195 | 0.3 | 1.8 |
| set-max-exec-time | variant=set-max-exec-time workers=10 | 1 | 18148 | 0 | 0 | 0 | 10 | 43 | 10 | SYSTEM | 0 | warm | 7 | 10 | 5 | 16 | 30 | 15 | 7 | 10 | 5 | 7 | 10 | 5 | 2 | 5 | 49 | 2 | 2 | 42 | 195 | 0.3 | 4.2 |
| set-autocommit-off | variant=set-autocommit-off workers=10 | 1 | 18289 | 0 | 0 | 0 | 10 | 43 | 10 | SYSTEM | 0 | warm | 7 | 10 | 5 | 16 | 30 | 15 | 0 | 0 | 0 | 7 | 10 | 5 | 2 | 5.1 | 34.6 | 3 | 2.02 | 42 | 196 | 0.4 | 1.7 |

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
