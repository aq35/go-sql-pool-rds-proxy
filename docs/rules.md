# Go DB 接続プール設定のルール(RDS Proxy + Aurora MySQL)

入力(インフラとアプリの構成)から、`SetMaxOpenConns` / `SetMaxIdleConns` / `SetConnMaxLifetime` / `SetConnMaxIdleTime` を**決まった式で**出す。
式は `lab/rules.py`(`make rules ARGS='...'`)。係数は実測から `scripts/derive_coeffs.py` が `lab/coeffs.json` に出す。**測っていない係数は「default/guess」と表示される。**

> **この式は「確定した公式」ではなく、「検証していく仮説」です。** 特に `by_cpu = vCPU × k` は、「CPU 負荷の高い SQL で観測した、同時実行の膝」を、そのまま `MaxOpenConns` に写しています。しかし、接続の数には、SQL を実行している間だけでなく、**トランザクション中の待機や、アイドルの接続**も含まれます(実行中の SQL 数 ≠ 接続数)。式を確定する前に、同じ時刻軸で、次の 5 つを並べて測り、どの量が、どれだけ違うのかを調べます: **実行中の SQL 数(Aurora の `Threads_running`)/ Go の `InUse` / Go の `Idle` / Proxy の `DatabaseConnectionsCurrentlyBorrowed` / Proxy の `DatabaseConnections`**(統計の取り方は [metrics.md](metrics.md))。

**状態: k だけ実測済み(2.0)。α・β・γ・δ は未測定(default/guess)。** `make campaign`(測定)→ `make derive`(係数)→ `make validation-gen` + `make campaign CAMPAIGN=day2`(検証)で確定する。

## 入力

| 記号 | 意味 | `rules.py` の引数 |
|---|---|---|
| vCPU | Aurora writer の vCPU | `--vcpu` |
| M | Aurora `max_connections` | `--max-connections` |
| P | Proxy `MaxConnectionsPercent`(既定 100、実験は 80) | `--proxy-max-pct` |
| T_p | Proxy `IdleClientTimeout`(既定 1800 秒) | `--idle-client-timeout` |
| C | コンテナ(タスク)数 | `--containers` |
| W | コンテナあたりのワーカー数(同時にクエリを流す上限) | `--workers` |
| A | 通常時の同時使用数の平均(`db.Stats().InUse` の中央値) | `--active-avg` |
| L | コンテナあたりのロック専用(GET_LOCK 保持)接続数 | `--locks-per-container` |
| TLS / 別 AZ | 再接続コストが高いか | `--tls` `--cross-az` |
| プロキシなし | Proxy を使わない場合 | `--no-proxy` |
| デプロイ重なり | ローリングデプロイ中に新旧が重なる(既定: 重なる) | `--no-deploy-overlap` |

## 式

```
cap      = M × P/100                                   # Proxy が Aurora に開ける接続の上限
overlap  = 2(デプロイで新旧が重なる) | 1
by_cpu   = vCPU × k
by_proxy = (cap × α − L × C × overlap) / overlap       # ピン留め(ロック専用)を引いた残りの予算
TotalMaxOpen = min(by_cpu, by_proxy)                   # by_proxy < C なら「実行不可」と警告
MaxOpenConns = min( ceil(TotalMaxOpen / C), W )
boost        = 1.5(TLS または別 AZ) | 1.0              # 再接続が高いほど、idle を厚く
MaxIdleConns = min( MaxOpenConns, max( ceil(A × β × boost), ceil(MaxOpenConns × γ × boost) ) )
ConnMaxIdleTime = T_p × δ                              # Proxy なしなら 300 秒
ConnMaxLifetime = lifetime_min × 60                    # 既定 30 分(Proxy の 24 時間より十分短く)
```

## 係数と、導く実験(フェーズ)

| 係数 | 意味 | 実験(フェーズ) | 導出ルール(`derive_coeffs.py`) | 現状 |
|---|---|---|---|---|
| k | 同時数の膝 / vCPU | `capacity-curve`(2 vCPU と 4 vCPU)① | クラスごとの qps_knee ÷ vCPU の最小値(CPU 律速のクエリ構成が限界を決める)。負荷生成が律速した水準は除外 | **2.0**(実測 2026-11-15。CPU 負荷の強いクエリで、膝は 2 vCPU で 4、4 vCPU で 8。軽い主キー検索では 16/vCPU だが 4 vCPU は測定範囲の上限で未確定) |
| α | Proxy 上限のうち、ピン留め + プールに使ってよい割合 | `pinning-impact`③ | 全 rep で Status OK だった最大の pin_ratio(Pinned max ÷ MaxDatabaseConnectionsAllowed) | 未測定 |
| β, γ | idle のバッファ係数、最小維持率 | `tls-latency`(churn-cost)②、`idle-sweep` | TLS・別 AZ で、p99 が最良の 1.2 倍以内になる最小の MaxIdle ÷ ワーカー数 = γ。β は idle-sweep の同時数分布から(未実装なら 1.0) | 未測定 |
| δ | ConnMaxIdleTime / IdleClientTimeout | `lifecycle-sync`④(IdleClientTimeout=60 秒に変更) | エラー 0 かつ p99 が最良の 1.2 倍以内で、比が 1 未満の最大値 | 未測定 |
| lifetime_min | ConnMaxLifetime(分) | 公式(Proxy の 24 時間) | 測定しない。フェイルオーバー後の偏りは、この実験の範囲外 | 公式 |

## 検証(再現性)

係数を出した条件(2 vCPU・4 vCPU、3 コンテナ × 10 ワーカー)と**違う条件**(コンテナ数 5・2・4、ワーカー数 8・15・12、ロック混在、TLS・別 AZ)を `scripts/gen_validation.py` が作り、式から出した設定値で流す(`rule-validation`、`campaign day2`)。
**合格基準**(`lab/criteria.json`、全 rep で Status OK): Aurora CPU が 85% 以下 / ピン留めが上限の 0.8 以下 / プールの待ち 0 / エラー 0 / 借用の遅れが 1 秒以下。
合格でなければ、立っているフラグに対応する係数(CPU_OVER→k、PIN_SATURATED→α、WAIT_SPIKE→k・MaxOpen、ERRORS→δ)を見直して、再度、導出と検証を行う。**検証に使った条件は、係数の導出には使わない**(過学習を避ける)。

## 本番で測る入力

`db.Stats()` の `InUse`(中央値 = A、最大)、`WaitCount` / `WaitDuration`、`MaxIdleClosed` の増加量。Proxy の `ClientConnections`、`DatabaseConnections`、`...SessionPinned`、`MaxDatabaseConnectionsAllowed`。

## 実験の範囲外(未検証)

フェイルオーバー時の挙動、書き込み中心のワークロードの膝、クエリが複雑に混ざる場合の k のばらつき、Aurora のメモリ(バッファプール)による差。
