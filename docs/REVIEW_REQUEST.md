# レビュー依頼: Go の接続プール × RDS Proxy × Aurora MySQL の実験

この文書は、実験の設計・実装・結論の解釈を、別の目でレビューしてもらうための依頼書です。単体で読めるように書いてあります。コードは、このリポジトリ(`go-sql-pool-rds-proxy`)にあります。

## 1. 目的

Go(`database/sql` + go-sql-driver/mysql、一部 GORM)で、RDS Proxy 経由で Aurora MySQL に接続するとき、接続プールの設定値(MaxOpenConns、MaxIdleConns、ConnMaxLifetime、ConnMaxIdleTime)と、ドライバ・GORM の設定を、**実測で決める**こと。最終的には、Aurora のスペックから設定値を算出するルール(`docs/rules.md`)にしたい。

きっかけは、本番(Aurora `max_connections=1000`)で、Aurora への接続数が約 800 に張り付く現象。この実験は、その 1/10 規模の再現です。

## 2. 環境

- Aurora MySQL 3.08.2、`max_connections=100`、**db.r6g.large(2 vCPU)**、writer 1 台。
- RDS Proxy: MaxConnectionsPercent 80、MaxIdleConnectionsPercent 10、IdleClientTimeout 既定 30 分(一部の実験だけ 60 秒に変更)。
- クライアント: EC2 c7g.xlarge(東京リージョン)。Go 1.22。負荷は、1 つのバイナリ `lab`(`--scenario=<名前> --mode=<名前>`)で出す。
- 実行: 実験の宣言(`lab/experiments/*.json`)を、EC2 上のランナー(`lab/exp.py`)が実行し、CloudWatch の指標と、クライアントのログを保存する。
- 規模は 32 本程度まで。**大きい構成への外挿はしていない。**

## 3. コードの場所

| 場所 | 内容 |
|---|---|
| `docs/EXPERIMENT_PLAN.md` | 計画(実験 0、A〜H、番外 X1〜X4、FINAL) |
| `docs/EXPERIMENT_REVIEW.md` | 計画と先行調査の突き合わせ |
| `lab/patterns.go` | 今回の主な実装(6+1 モード) |
| `lab/experiments/pat-*.json` | 実験の宣言(問い・仮説・読み方・条件) |
| `lab/campaigns/patterns.json`, `patterns-v2.json` | 実行する実験の並び |
| `lab/exp.py` | ランナー(run の前に Proxy の接続数が下限に戻るのを待つ、繰り返しで順序を回す、など) |
| `scripts/summarize.py` | 結果の集計(`patterns_summary`) |
| `infra/lib/proxy-experiment-stack.ts` | CDK(Aurora、Proxy、EC2。拡張モニタリング 1 秒を追加) |
| `results/REPORT.md` | 結果と、断定できない点の一覧(§10) |

## 4. 実験の一覧

「状況」: **実行済み**(`patterns` キャンペーン、2026-11-16)、**未実行**(`patterns-v2` 用に定義だけ。AWS では未実行)。

### 4.1 gorm-pool(実行済み、3 回)
- **問い**: `MaxOpenConns` を 1〜2 に絞ると、GORM のどの書き方で固まるか。
- **方法**: 6 つの操作を、3 秒のタイムアウトつきで実行する(トランザクション中の `tx` と `db` の使い分け、`Preload` 2 つ、関連つき `Create`、`BeforeCreate` フックで `tx` / グローバル `db`)。
- **結果**: MaxOpen=1 で固まったのは、「トランザクション中に `db` で別のクエリ」と「フックでグローバル `db`」の 2 つだけ。MaxOpen=2 ではすべて完了。
- **弱点**: 「2 でも、同じ書き方が 2 つ同時に走れば固まる」は推論で、未測定。

### 4.2 warmup(実行済み、3 回)
- **問い**: 新しいプールに 32 同時のクエリが来るとき、`PingContext` の並列で先に接続を作ると、最初の波の遅延はどれだけ縮むか。
- **方法**: 試行ごとに新しいプールを作る(10 試行、間隔 15 秒)。cold は何もしない。warm は 32 並列の Ping。どちらも 1 秒待ってから、32 同時の `SELECT 1`。TLS・別 AZ。
- **結果**: 最初の波の p50 は、cold 53.8 ms、warm 7.1 ms。温めるのに 92 ms。
- **弱点**: 試行の間隔が 15 秒で、Proxy→Aurora の接続が温まったまま(本当のコールドスタートではない)。クエリは `SELECT 1`。
- **v2(未実行)**: 主キー検索のクエリ(`--query=pk`)、Proxy も冷やした条件(`pat-warmup-proxycold`、試行の間隔 6.5 分)。

### 4.3 serial-parallel(実行済み、2 回)
- **問い**: 1 リクエストが 5 つのクエリを、1 本の接続で順に(直列)、または 5 本で同時に(並列)実行すると、どちらが速いか。
- **方法**: ポアソン到着(open-loop、遅延は到着時刻から数える)。クエリは、CPU が重い mix(先行調査で約 5 ms)。MaxOpen 20 と 6、30 / 55 / 70 req/s、各 60 秒(最初の 15 秒は除外)。
- **結果**: p50 は全条件で並列が速い。p99 は、MaxOpen=6 で 55 req/s 以上のとき、並列のほうが悪い(280 対 207 ms、957 対 621 ms)。MaxOpen=20 では、70 req/s まで並列が速い。
- **弱点**: 「限界の約 90%」は、先行調査の 386 QPS からの推定。プール待ちの合計は、全リクエストの待ち時間の和(実時間ではない)。CPU が取れていない。

### 4.4 timeout-cascade(実行済み、2 回)
- **問い**: クエリごとのタイムアウトを短くすると、限界付近で、接続の作り直しとクエリの積み残しが連鎖して、処理量が落ちるか。
- **方法**: open-loop。1 クエリ(約 5 ms の CPU)、MaxOpen=32、300 qps(限界の約 78%)と 460 qps(約 119%)、タイムアウトは なし / 200 ms / 50 ms / 20 ms。Aurora 直結の接続で、実行中のクエリ数を 0.5 秒おきに取る。
- **結果**: 新規接続数は、タイムアウト数にほぼ比例(0.75〜1.2 倍)。300 qps でも、20 ms のタイムアウトで成功が約 12% 減(302 → 266/秒)。460 qps で 217/秒(タイムアウトなしは 402/秒、ただし p50 が 1 秒超)。
- **弱点**: クエリが短く、「クライアントが切った後も Aurora で走り続けるか」が見えない。タイムアウトなしの 300 qps で p99 が 265 ms(原因未確認)。
- **v2(未実行)**: 長いクエリ(`SELECT SLEEP(0.5)`、40 件/秒、タイムアウトなし / 100 ms)の条件を追加。ローカルの MySQL では、サーバー側でクエリが走り続けることを確認したが、**RDS Proxy 経由では未確認**。

### 4.5 pin-patterns(実行済み、2 回)
- **問い**: どの Go / GORM の設定が、RDS Proxy のセッションをピン留めするか。
- **方法**: 10 ワーカーが GORM で主キー検索を 200 秒。変種: base、`interpolateParams=false`、GORM `PrepareStmt`、DSN の `time_zone`、DSN の `sql_mode`、`loc` のみ、トランザクション内で 200 ms 待つ。Aurora 側の `Com_stmt_prepare` の増分と、Proxy の `SessionPinned` の最大を見る。
- **結果**: ピン留めされたのは、`interpolateParams=false`(最大 6、prepare がクエリごと)と `PrepareStmt`(最大 6.5)だけ。`time_zone` と `sql_mode` は 0。長いトランザクションは、ピン留めではなく `InTransaction` として現れた。
- **弱点**: `ClientConnections` が実数より少なく出るので、「プール全体がピン留め」とは言えない。run の間隔が約 40 秒で、1 分値が前後の run と混ざる。`loc` がサーバーの `time_zone` を変えないことを、Aurora では確認していない(ローカルでは確認)。
- **v2(未実行)**: 変種に DSN の `SET`(`sql_select_limit`、ユーザー変数、`wait_timeout`、`max_execution_time`、`autocommit=0`)を追加。Go 側の接続数(`go_open_max`)を記録。run の間隔を 130 秒に。

### 4.6 stampede(実行済み、3 回)
- **問い**: 32 本が同時に作られ、同時に ConnMaxLifetime(60 秒)を迎えると、再接続のスパイクで遅延が悪化するか。
- **方法**: sync は全ワーカーが同時に開始。staggered は、ワーカーの開始を最大 60 秒ずらす。5 分、TLS・別 AZ。
- **結果**: 1 秒あたりの新規接続の最大は、sync 30、staggered 6。その秒の p99 は、71 ms 対 25 ms。
- **弱点**: staggered は、総クエリ数が約 9% 少ない(条件がそろっていない)。staggered の 6 本/秒は、理論値(約 0.5 本)より多く、原因未確認。
- **v2(未実行)**: 全ワーカーが動く区間(65 秒以降)だけで集計し、繰り返しを 4 回に。

### 4.7 lifecycle-sync(実行済み、**1 回のみ**。既存の `pool-load` シナリオ)
- **問い**: ConnMaxIdleTime と、Proxy の IdleClientTimeout(60 秒)の関係で、エラーや再接続の遅延が出る境界はどこか。
- **方法**: 3 コンテナ × 6 本、各ワーカーが約 60 秒おきにクエリ。ConnMaxIdleTime は なし / 30 / 50 / 90 秒。
- **結果**: どの条件でも、エラー 0。p99 は 12 / 18 / 23 / 12 ms。
- **弱点**: 1 回のみ。「ドライバが透過的に再接続している」は推測。ConnMaxIdleTime なしの p99 が低い理由が説明できない。再接続の数を数えていない。
- **v2(未実行)**: 新モード `idle-gap`(`pat-idle-gap`)。クエリの間隔を、30 / 55 / 65 / 90 秒に固定し、ConnMaxIdleTime なし / 45 秒の組み合わせで、**1 クエリあたりの新規接続数**を直接数える。

## 5. 既知の弱点(自己監査)

1. CloudWatch の 1 分値は、run の間隔が約 40 秒のため、前後の run が混ざる(CPU、ピン留め、InTransaction など)。CPU は、結論に使っていない。v2 では、拡張モニタリングの 1 秒値を run の区間で切る。**この取得処理(`exp.py` の `os_cpu`)は、AWS で動作を確認していない。**
2. 繰り返しが 2〜3 回で、範囲が重なる差がある。
3. `ClientConnections` が実数より少なく出る(原因未解明)。
4. 先行調査の値(Aurora の限界 386 QPS)を、別のインスタンスの実験に当てはめている。v2 では、同じ run で `Questions`(QPS)と `Threads_running` を記録する。
5. 2 vCPU と 32 本までの規模のみ。
6. 計画の実験 C(テナント分離)、E(多重化の圧縮率)、FINAL(複合の総合テスト)は、**未実施**。「ベスト設定」は、仮説のまま。

## 6. レビューしてほしい点

次の順に、特に見てください。

1. **統計と解釈**: 繰り返しが少ない中で、本文の結論が、データから言える範囲を超えていないか。特に、4.3(逆転の解釈)、4.4(連鎖の因果)、4.6(スパイクの効果)。
2. **測定の妥当性**: open-loop の実装(`openLoop`)に、遅延の数え方や、到着の偏りの問題がないか。Poisson の間隔を `time.Sleep` で作ることの誤差。
3. **交絡**: run の順序、前の run の残り(Proxy の裏の接続は約 5〜6 分残る)、時間帯、`floor=100`(待ちを省く設定)が、結果に影響していないか。
4. **v2 の設計**: `idle-gap`、`pat-warmup-proxycold`、`pat-pin` の追加変種が、問いに答えられる設計か。足りない対照条件や、測るべき量がないか。
5. **実装の誤り**: `lab/patterns.go` の、とくに `patStampede`(集計区間)、`patIdleGap`、`sideStats`(Aurora 側の `performance_schema.global_status` の読み方)、`startAuroraPoller`。
6. **RDS Proxy の仕様の理解**: ピン留めの条件、IdleClientTimeout の挙動、クライアント切断後のバックエンドのクエリの扱いについて、誤解がないか。公式ドキュメントとの食い違いがあれば、指摘してほしい。

## 7. 結論の信頼度(自己評価)

| 結論 | 信頼度 |
|---|---|
| `MaxOpen=1` で、トランザクション中の別の接続を要求する書き方が固まる | 高い(仕様どおりで、3 回とも同じ) |
| サーバー側プリペアド(`interpolateParams=false`、`PrepareStmt`)でピン留めされる | 高い(prepare の増分が一致) |
| DSN の `time_zone` と `sql_mode` はピン留めされない | 中(この 2 つ、2 回、1 分値) |
| ウォームアップで、最初の波が約 47 ms 縮む | 中(Proxy 側が温まったまま、`SELECT 1`) |
| 一斉寿命で、その秒の p99 が約 3 倍 | 中〜低(条件がそろっていない) |
| 小さいプールで、並列の p99 が直列より悪くなる | 中(2 回、CPU 未測定) |
| 短いタイムアウトで、成功/秒が下がる | 中(2 回。因果の過程は推定) |
| アイドルタイムアウトで、エラーが出ない | 低(1 回のみ) |

## 8. 守っていること(機密)

認証情報、アカウント ID、ARN、エンドポイント名は、リポジトリに入れていない。生ログ(エンドポイント名を含む)は、`.gitignore` で除外している。
