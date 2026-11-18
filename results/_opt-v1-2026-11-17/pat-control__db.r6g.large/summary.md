# pat-control — db.r6g.large

**問い**: 何も変えない同じ条件を繰り返すと、結果は、どれだけばらつくか(ほかの実験の「差」が、揺らぎの範囲かを判断する物差し)

**仮説**: 同じ条件の繰り返しでも、p99 は、数割ばらつく。p50 や、タイムアウトの割合は、より安定している。

**読み方**: 同じ run id の 5 回の、p50_success_ms、p99_success_ms、within_slo_pct、goodput_qps の、最小〜最大(summary の [最小–最大])を読む。ほかの実験で、2 つの条件の差が、この範囲より小さければ、差とは言えない。

値は、繰り返しの中央値 [最小–最大]。

`proxy_*` 列は CloudWatch の 1 分刻みで、前後の run を含む。`aurora_cpu_*` は Enhanced Monitoring の 1 秒値を run の区間(最初の 20 秒を除く)で切ったもの。`threads_*`、`aurora_questions_per_s` は Aurora に直接つないで 1 秒おきに取ったもの。

| run | 識別 | n | offered | launched | dropped | ok | timeouts | errors | slo_ms | within_slo_pct | p50_success_ms | p99_success_ms | issue_lag_p99_ms | issue_lag_max_ms | wait_count | wait_total_ms_all_waiters | max_in_use | connects | client_cpu | proxy_condition | proxy_client_max | proxy_client_sum | proxy_client_avg | proxy_dbconn_max | proxy_dbconn_sum | proxy_dbconn_avg | proxy_pinned_max | proxy_intx_max | proxy_nodes_est | aurora_cpu_p50 | aurora_cpu_max | threads_running_max | threads_running_avg | threads_connected_max | db_questions_per_s_all_sessions | client_cpu_avg | client_cpu_max |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| serial | max_open=20 mix=cpu queries_per_request=5 rps=55.00 variant=serial | 5 | 3284 [3235–3359] | 3284 [3235–3359] | 0 | 3284 [3235–3359] | 0 | 0 | 100 | 89.52 [81.1–90.14] | 53.37 [52.69–58.41] | 196 [166–486] | 1.05 [1.04–1.05] | 1.06 [1.06–1.11] | 113 [15–668] | 1260 [55.85–11798] | 20 | 20 | 0.77 [0.56–1.2] | warm | 12 [10–13] | 20 | 10 | 13 [11–13] | 24 [21–24] | 12 [10.5–12] | 0 | 0 | 2 | 69.1 [68.8–71.5] | 95.9 [85.7–100] | 5 [5–6] | 3.24 [3.16–3.32] | 34 [30–34] | 286 [268–290] | 0.8 [0.6–1.2] | 2.5 [1.7–6.8] |
| parallel | max_open=20 mix=cpu queries_per_request=5 rps=55.00 variant=parallel | 5 | 3382 [3274–3454] | 3382 [3274–3454] | 0 | 3382 [3274–3454] | 0 | 0 | 100 | 93.77 [93.55–96.59] | 33.68 [31.13–34.2] | 211 [160–238] | 1.05 [1.04–1.05] | 1.06 | 3069 [2384–3253] | 73638 [54531–83086] | 20 | 20 | 0.92 [0.81–1.34] | warm | 11 [10–12] | 20 | 10 | 13 [11–13] | 24 [21–24] | 12 [10.5–12] | 0 | 0 | 2 | 72.7 [69.2–75.1] | 94.5 [89–98.5] | 5 | 3.72 [3.57–4.19] | 34 [31–34] | 292 [285–299] | 0.9 [0.8–1.3] | 3.4 [2.9–7.4] |

## 無効な run(集計に含めていない。データは保存してある)

なし

`proxy_condition`: warm = 前の run の Proxy→Aurora の接続が残っている可能性がある(下限まで待っていない)、settled = 下限まで待った。両者を混ぜて比べない。
`proxy_*_max` は Maximum、`proxy_*_sum` は Sum(AWS の推奨。`docs/metrics.md`)。
