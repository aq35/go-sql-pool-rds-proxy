# pat-stampede — db.r6g.large

**問い**: 32 本が同時に作られ、同時に ConnMaxLifetime(60 秒)を迎えると、再接続のスパイクで遅延が悪化するか。作る時刻をずらすと、どれだけ緩和するか。ConnMaxLifetime=0(時間による入れ替えなし)を対照にする

**仮説**: sync では、60 秒ごとに、32 本が同じ秒に作り直され、その秒の p99 が、定常より悪化する。staggered では、1 秒あたりの新規接続が 1〜2 本に分散し、p99 の悪化が小さい。 lifetime=0(対照)では、定常区間の新規接続がほぼ 0 で、p99 のスパイクもない。対照にも同程度のスパイクがあるなら、寿命以外(クライアントやネットワークの揺らぎ)が原因。

**読み方**: peak_connects_per_s と steady_p99_ms を比べる。どちらも、最初の 1 回目の寿命(65 秒)が過ぎ、全ワーカーが動いている区間(steady)だけで集計する(前回は、staggered の開始の遅れが、総クエリ数の差になっていた)。3 回の繰り返しの範囲が重なれば、差は結論にしない。steady_connects は、その区間の新規接続の総数(sync と staggered で近いはず)。 peak_connects_at_s(再接続が最大の秒)と peak_p99_at_s(p99 が最悪の秒)は別の秒かもしれない。p99_at_peak_connects_ms と n_at_peak_connects(その秒の標本数)、top3_connect_secs(再接続が多い 3 つの秒の p99)で、再接続の多い秒の遅延を見る。sync-nolife は対照。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | p99_at_peak_connects_ms | n_at_peak_connects | n_at_peak_p99 | top3_connect_secs | queries | total_connects | steady_from_s | steady_connects | peak_connects_per_s | peak_connects_at_s | peak_p99_ms | peak_p99_at_s | steady_p50_ms | steady_p99_ms | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_intx_max | proxy_nodes_est | aurora_cpu_p50 | aurora_cpu_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| sync | conns=32 lifetime_s=60.00 variant=sync | 2 | 4.835 [4.83–4.84] | 587 [581–593] | 469 [438–500] | t121:c14/p99=5ms/n581|t158:c9/p99=20ms/n560|t181:c7/p99=14ms/n587×1/t121:c16/p99=5ms/n593|t181:c10/p99=5ms/n577|t76:c5/p99=5ms/n574×1 | 139002 [138846–139159] | 104 [104–105] | 65 | 51.5 [50–53] | 15 [14–16] | 121 | 113 [72.34–153] | 123 [89–157] | 4.485 [4.47–4.5] | 8.67 [8.45–8.89] | warm | 20 [18–22] | 32 | 16 | 26 [19–33] | 48 [36–60] | 24 [18–30] | 0 | 0 | 2 | 5.6 [5.2–6] | 51.45 [46.5–56.4] | 3 | 2.01 [2–2.02] | 58 [46–70] | 591 | 1.1 | 4.5 [3.9–5.1] |
| staggered | conns=32 lifetime_s=60.00 variant=staggered | 2 | 5.22 [5.08–5.36] | 582 [580–584] | 508 [467–548] | t157:c10/p99=5ms/n580|t97:c7/p99=5ms/n582|t236:c7/p99=38ms/n533×1/t218:c9/p99=5ms/n584|t100:c7/p99=5ms/n577|t186:c6/p99=5ms/n576×1 | 122334 [122233–122434] | 88 [86–90] | 65 | 65 [64–66] | 9.5 [9–10] | 188 [157–218] | 79.355 [53.61–105] | 230 [226–235] | 4.46 [4.45–4.47] | 9.275 [8.6–9.95] | warm | 18.5 [17–20] | 32 | 16 | 20.5 [20–21] | 39 | 19.5 | 0 | 0 | 2 | 5.5 | 51.05 [48.9–53.2] | 3 | 2.01 | 53.5 [49–58] | 524 [523–524] | 0.95 [0.9–1] | 3.95 [3.9–4] |
| sync-nolife | conns=32 lifetime_s=0.00 variant=sync | 2 | 64.34 [59.31–69.37] | 572 [569–576] | 536 [516–555] | t148:c0/p99=5ms/n576|t209:c0/p99=6ms/n576|t88:c0/p99=6ms/n580×1/t155:c0/p99=11ms/n576|t168:c0/p99=8ms/n576|t184:c0/p99=5ms/n576×1 | 139208 [139084–139333] | 32 | 65 | 0 | 0 | 0 | 44.005 [37.68–50.33] | 136 [132–141] | 4.51 [4.47–4.55] | 9.06 [8.84–9.28] | warm | 18.5 [17–20] | 32 | 16 | 19.5 [19–20] | 37 [36–38] | 18.5 [18–19] | 0 | 0 | 2 | 5.5 | 57.35 [47.8–66.9] | 3 | 2.005 [2–2.01] | 49 | 592 [591–592] | 1.1 | 4.8 |

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
