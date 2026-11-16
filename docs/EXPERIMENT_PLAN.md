# 実験計画: Go の接続プール設定 × RDS Proxy × Aurora MySQL

目的: Go(`database/sql` / GORM)と RDS Proxy を組み合わせたとき、接続プールの設定値(MaxOpenConns、MaxIdleConns、ConnMaxLifetime、ConnMaxIdleTime)とドライバ・GORM の設定を、実測で決める。最後に、決めた設定(ベスト設定)を、複合の負荷で確かめる。

この計画は、貼られた計画(実験 0、A〜H、FINAL)を、先行調査の結果([EXPERIMENT_REVIEW.md](EXPERIMENT_REVIEW.md))に合わせて直したもの。直した点は「直した点」の列に書く。

規模: Aurora MySQL `max_connections=100`、RDS Proxy(MaxConnectionsPercent 80、MaxIdleConnectionsPercent 10)、Go クライアントは EC2 から東京リージョンで接続。本番の 1/10 規模。

実行結果は [results/REPORT.md](../results/REPORT.md)(2026-11-16)。FINAL、C、E は未実施。

## 守ること(全実験で共通)

- 各 run の前に、Proxy の `DatabaseConnections` が下限(約 5)に戻るまで待つ(`exp.py` が行う)。遅延だけを読む実験は、この待ちを省く(`floor` を大きくする)。
- 各条件は複数回(順序を入れ替えて)。1 回の差は結論にしない。
- 生データ(`client.log`、CloudWatch の `metrics.json`)を保存する。
- 負荷を出す側の CPU を確認する(capacity は記録済み。他は `client_cpu` を見る)。
- 合格条件は「期待する結果」ではなく、**測る量と、判定の基準**で書く。
- 認証情報、アカウント ID、ARN、エンドポイント名は、コミットしない。
- **公式の仕様と実測が食い違うときは、仕様を疑う前に、計測器・測定間隔・接続の所有権を疑う。** 例: 「`ClientConnections` が実ソケット数より少ない」は、(1) 統計が `Maximum`(AWS の推奨は `Sum`、proxy は複数インスタンスで動く)、(2) 1 分値の粒度、(3) プールを共有するワーカーの接続の再利用、の順に確かめる。
- **「実行が終わった(completed)」と「測定が有効(valid)」を分ける。** 失敗・未達の run も、学習材料として保存し、集計からは分ける(`valid.json`)。
- 結果には、**成功したものだけの値**と、**提示した全体に対する割合**(期限内成功率)を並べる。
- 各 run は、`manifest`(引数・実験定義・ソースの版・バイナリのチェックサム・インフラ設定)のハッシュを持つ。同じ run ID でも、manifest が変わったら、古い run は `_superseded/` に残して、再実行する。

## 全体の構成

| フェーズ | 実験 | 状況(この計画の実行前) |
|---|---|---|
| 1 限界点 | 0: 階段負荷で最大同時実行数 | 実施済み(`capacity-curve`) |
| 2 基本 | A: MaxIdle の役割 | 実施済み(`tls-latency`) |
| | B: MaxIdle ≠ MaxOpen | 暫定(`idle-sweep`) |
| | C: テナント分離 × 1 秒ループ | 未実施 |
| 3 運用 | D: パスワード回転 | 実施済み(`secret-rotation`) |
| | E: 多重化の圧縮率 | 未実施 |
| | F: アイドルタイムアウトの競合 | 定義のみ(`lifecycle-sync`) |
| | G-1〜G-4: ピン留め 4 種 | G-1 の一部のみ |
| | H: 一斉寿命(スタンピード) | 未実施 |
| 番外 | X1: 直列 vs 並列、X2: タイムアウトの連鎖、X3: GORM の裏側、X4: ウォームアップ | 未実施 |
| 4 統合 | FINAL: ベスト設定の総合テスト | 未実施 |

## 実験の定義

### フェーズ 1

**実験 0: 最大同時実行数**(実施済み、`capacity-curve`)
- 測る量: 同時数ごとの QPS、p50、p99、Aurora の CPU、負荷側の CPU。
- 判定: QPS が最大の 90% に達する最小の同時数(膝)。p99 が最良の 2 倍以内の最大の同時数。
- 直した点: 各段は 2 分(計画は 10 秒)。MaxOpen は、Proxy の上限(80)と Aurora の上限(100)を超えない水準まで。

### フェーズ 2

**実験 A: MaxIdle の役割**(実施済み、`tls-latency`)
- 測る量: MaxIdle ごとの平均・p99 の遅延、閉じた接続数(`MaxIdleClosed`)。
- 判定: 遅延が頭打ちになる MaxIdle。

**実験 B: MaxIdle ≠ MaxOpen**(暫定、`idle-sweep`)
- 測る量: Proxy の `DatabaseConnections`(Go 側の `OpenConnections` は仕様どおり縮むので、主な指標にしない)、バースト後の残留。
- 直した点: バースト後に、Proxy の裏の接続が約 5 分残る現象を、必ず測る。

**実験 C: テナント分離 × 1 秒ループ**(未実施)
- 測る量: 単一プールと、テナント別プール(20)で、毎秒のピーク `OpenConnections` の合計。

### フェーズ 3

**実験 D: パスワード回転**(実施済み、`secret-rotation`)
- 直した点: 合格条件は「エラー 0%」ではなく、**新規ログインが失敗する窓の長さ**と、**リトライで吸収できるか**。

**実験 E: 多重化の圧縮率**(未実施)
- 測る量: クライアント接続数(合計)と、Proxy→Aurora の接続数の比。
- 直した点: 待ち時間中は、トランザクションを持たない。

**実験 F: アイドルタイムアウトの競合**(`lifecycle-sync`)
- 測る量: `ConnMaxIdleTime` と Proxy の `IdleClientTimeout` の関係で、`invalid connection` / `EOF` が出る境界。

**実験 G: ピン留め 4 種**(`pin-patterns`)

| id | 内容 | 測る量 |
|---|---|---|
| G-1 | `interpolateParams=false`(Go の既定)でパラメータ付きクエリ | `DatabaseConnectionsCurrentlySessionPinned`、`Com_stmt_prepare` の増分 |
| G-2 | `interpolateParams=true` + GORM `PrepareStmt: true` | 同上 |
| G-3 | DSN の `time_zone`・`sql_mode`(接続のたびに `SET` が走る) | 同上 |
| G-4 | トランザクション内で外部 API 待ち(`Sleep`) | `DatabaseConnectionsCurrentlyInTransaction`、Proxy→Aurora の接続数 |
| base | 上のどれもなし(対照) | 同上 |

- 直した点: G-3 は、先行調査で `time_zone` はピン留めされなかった。実験は「どの `SET` が何本をピン留めするか」を測る。G-4 の長いトランザクションは「ピン留め」ではなく、トランザクションの間、接続が専有される現象として測る。

**実験 H: 一斉寿命(スタンピード)**(`stampede`)
- 測る量: 毎秒の新規接続数と p99。32 本が同時に作られた場合と、時間をずらして作られた場合の比較。

### 番外編

**X1: 直列 vs 並列**(`serial-parallel`)
- 1 リクエストが 5 つのクエリを、1 本の接続で順に(直列)、または 5 本で同時に(並列)実行する。
- 測る量: 到着率(リクエスト/秒)ごとの、リクエストの p50・p99、プールの待ち、Aurora の CPU。
- 判定: 並列と直列で p99 が逆転する到着率と、そのときの Aurora の CPU。

**X2: タイムアウトの連鎖**(`timeout-cascade`)
- クエリごとに `context.WithTimeout` を変える(なし、200 ms、50 ms、20 ms)。負荷は、限界の 80% と 120%。
- 測る量: 成功数、タイムアウト数、新規接続数、Aurora 側で実行中のクエリ数(クライアントが切断した後も走り続けるか)、Aurora の CPU。

**X3: GORM の裏側**(`gorm-pool`)
- `MaxOpenConns=1` で、(a) トランザクション中に `tx` で別のクエリ、(b) `db` で別のクエリ、(c) `Preload` 2 つ、(d) フックの中で `db` を使う、を実行する。
- 測る量: 完了か、3 秒でタイムアウトか。実行中の最大 `InUse`。

**X4: ウォームアップ**(`warmup`)
- 新しいプールで、32 同時のクエリを流す。`PingContext` の並列で事前に接続を作った場合と、作らない場合で比べる。
- 測る量: 最初の波のレイテンシ(p50、最大)。
- 条件: TLS あり、別 AZ。

### フェーズ 4

**FINAL**(未実施)
- 全実験の結論を統合した設定で、定常負荷 + スパイク + 外部 API 待ち + パスワード回転の複合を実行する。
- 設定値は、実験の結果から決める。**事前に書いたコード例は仮説**であり、結果で直す。

## 観測する指標

- **Go**: `db.Stats()` の `OpenConnections`、`InUse`、`Idle`、`WaitCount`、`WaitDuration`、`MaxIdleClosed`、`MaxIdleTimeClosed`、`MaxLifetimeClosed`。リクエスト/秒、p50、p95、p99、エラー率。
- **RDS Proxy**(CloudWatch): `ClientConnections`、`DatabaseConnections`、`DatabaseConnectionsCurrentlySessionPinned`、`DatabaseConnectionsCurrentlyInTransaction`、`MaxDatabaseConnectionsAllowed`。ピン留めの理由は、拡張ログ。
- **Aurora**: `CPUUtilization`、`DatabaseConnections`、`SHOW GLOBAL STATUS` の `Com_stmt_prepare`、`Threads_connected`、`Threads_running`。

指標の定義は `lab/metrics.json` にある。

## 実行方法

```bash
make check
make deploy
make campaign CAMPAIGN=patterns
make finish CAMPAIGN=patterns DESTROY_AFTER=yes   # 全部取得できたときだけ削除
```
