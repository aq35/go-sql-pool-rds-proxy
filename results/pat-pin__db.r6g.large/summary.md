# pat-pin — db.r6g.large

**問い**: どの Go / GORM の設定が、RDS Proxy のセッションをピン留めするか(interpolateParams=false、GORM PrepareStmt、DSN の time_zone と sql_mode、loc だけ、トランザクション内の待ち)

**仮説**: interpolateParams=false(パラメータ付きクエリがサーバ側プリペアドになる)と、GORM の PrepareStmt=true は、ピン留めされる。DSN の time_zone / sql_mode は、接続の確立で SET が走るが、ピン留めされない可能性がある(先行調査では time_zone はピン留め 0)。loc だけは、SET がないのでピン留めされない。長いトランザクションは、ピン留めではなく、InTransaction と、接続の専有として現れる。

**読み方**: variant ごとに、Proxy の pinned max と intx max、com_stmt_prepare_delta を、base と比べる。DatabaseConnections は、前の run の残りを含むので読まない(この実験は、待ち時間を省いている)。

値は、繰り返しの中央値 [最小–最大]。

| run | 識別 | n | queries | errors | com_stmt_prepare_delta | com_stmt_execute_delta | connects | aurora_threads_max | aws_cpu_max_unreliable | proxy_db_max | proxy_pinned_max | proxy_intx_max | proxy_client_max |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| base | variant=base workers=10 | 2 | 33312 [32937–33686] | 0 | 0 | 0 | 10 | 47 [35–59] | 9.20355 [7.61616–10.7909] | 19 [13–25] | 0 | 0 | 7.5 [7–8] |
| prep-false | variant=prep-false workers=10 | 2 | 30456 [30266–30646] | 0 | 30456 [30266–30646] | 30456 [30266–30646] | 10 | 46.5 [36–57] | 7.87116 [7.80833–7.93399] | 17 [13–21] | 6 | 0 | 6 |
| gorm-prepare | variant=gorm-prepare workers=10 | 2 | 33028 [32943–33113] | 0 | 10 | 33028 [32943–33113] | 10 | 35 [34–36] | 7.17888 [6.87454–7.48321] | 12 [11–13] | 6.5 [5–8] | 0 | 6.5 [5–8] |
| set-tz | variant=set-tz workers=10 | 2 | 33038 [32580–33495] | 0 | 0 | 0 | 10 | 36.5 [36–37] | 7.32888 [6.94109–7.71667] | 12.5 [12–13] | 0 | 0 | 6 [5–7] |
| set-sqlmode | variant=set-sqlmode workers=10 | 2 | 32389 [32197–32581] | 0 | 0 | 0 | 10 | 38 [35–41] | 6.55438 [6.4671–6.64167] | 14.5 [13–16] | 0 | 0 | 7 [6–8] |
| loc-only | variant=loc-only workers=10 | 2 | 33021 [32616–33426] | 0 | 0 | 0 | 10 | 46.5 [44–49] | 6.7 [6.63289–6.76712] | 18.5 [18–19] | 0 | 0 | 6.5 [5–8] |
| long-tx | variant=long-tx workers=10 | 2 | 6880 | 0 | 0 | 0 | 10 | 37 [34–40] | 6.10834 [6.00823–6.20844] | 11.5 [10–13] | 0 | 6.5 [5–8] | 6.5 [5–8] |

## 警告
なし
