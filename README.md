# go-sql-pool-rds-proxy

Go (`database/sql` + GORM) と RDS Proxy と Aurora MySQL の間の**接続数**を、小さな規模（Aurora `max_connections=100`）で実験するためのテンプレート。
「接続数が想定より多い」「MaxIdleConns / MaxOpenConns をいくつにするか」「ピン留め(GET_LOCK、プリペアド)は何本占有するか」といった問いに、再現できる形で答える。

最終目標は、Aurora のスペック(vCPU)に対する Go アプリの設定値を、[docs/rules.md](docs/rules.md) のルールに落とすこと。
調査の計画は [docs/EXPERIMENT_PLAN.md](docs/EXPERIMENT_PLAN.md)、実験の弱点と改善は [docs/experiment-audit.md](docs/experiment-audit.md)。

## 構成

```
infra/         CDK: VPC(NAT なし)、Aurora MySQL、RDS Proxy、EC2(SSM のみ)、ダッシュボード、アラーム
lab/           負荷クライアントと実験の実行系。EC2 に配られ、そこでビルド・実行される
  main.go        --scenario=<名前> の入口。シナリオごとにフラグが違い、未定義のフラグはエラー
  poolload.go    シナリオ pool-load: コンテナ×ワーカーの定常負荷。MaxIdle / lifetime / ロック / プール交換
  capacity.go    シナリオ capacity: 同時数を段階的に上げ、QPS・p99・CPU の膝を測る(ルール R1 の根拠)
  trigger.go     シナリオ trigger: 何が Proxy の裏接続を作る・保持するか
  exceed.go      シナリオ exceed: DatabaseConnections が ClientConnections を超える条件
  experiments/   実験の宣言(JSON): 問い・仮説・結果の読み方・条件(run)・繰り返し数
  exp.py         実験ランナー(EC2 上): 下限へ戻るまで待つ → 実行 → 保存。繰り返しで順序を回す
  rules.py       ルールの式(入力 → Go の設定値)。係数は coeffs.json、無ければ default/guess と表示
  criteria.json  合格基準と閾値(CPU 85%、ピン留め 0.8 など)。summary の Status 判定に使う
  metrics.json   収集する CloudWatch 指標と統計(Maximum / Sum)の定義 — 1 か所
scripts/       手元から EC2 を操作(SSM)、結果の集計
results/       REPORT.md と、実験ごとの結果(<実験名>/<run>.r<n>/ に meta.json, client.log, metrics.json)
```

## 使い方

前提: Node.js 18+、AWS CLI v2、Session Manager plugin、Go(`make check` 用)。認証情報は `AWS_PROFILE` などで環境に置く(チャットやコミットに書かない)。

```bash
make check                          # ビルド・構文・実験定義の検証(AWS 不要)
make deploy                         # 構築。EC2 が lab/ をビルドして待機(課金が始まる: 1 日あたり数 USD)
make start EXP=idle-sweep REPEAT=3  # 実験を EC2 上で切り離して開始(PC を閉じても止まらない)
make status EXP=idle-sweep          # 進捗
make fetch EXP=idle-sweep           # results/idle-sweep/ に取得し、summary.md を作る
make campaign                       # 1 日分の実験(2 vCPU → 4 vCPU)をまとめて開始(約 10 時間)
make finish DESTROY_AFTER=yes       # 取得・summary 生成が全部成功したときだけ削除(付けなければ削除しない)
make derive                         # 結果から係数を出す(lab/coeffs.json)
make validation-gen                 # 係数から、未知の条件の検証実験(day2)を作る
make rules ARGS='--vcpu 2 --max-connections 1000 --containers 3 --workers 10 --active-avg 4'   # 式から設定値
make destroy CONFIRM=yes            # 削除(ユーザーが求めたときだけ)
make push                           # lab/ だけ更新したいとき(再構築なし)
```

実験の一覧は `make help`(または `cd lab && python3 exp.py list`)。

## 新しい実験を足す

1. **既存シナリオの条件違い**なら、`lab/experiments/<名前>.json` を足すだけ。`name` はファイル名と同じ。`question` / `hypothesis` / `reading`(結果をどう読めば仮説が正しいか)を必ず書く。
2. **新しい種類の負荷**なら、`lab/scenario_<名前>.go` に `func(args []string) error` を作り、`lab/main.go` の `scenarios` に登録する。フラグは `newFlagSet` で定義し、`commonFlags`(duration, report-every)を使う。
3. `make check` で、実験の引数が、シナリオのフラグに存在するかを確認する。

## CI(プルリクエストで見えるチェック)

`.github/workflows/ci.yml`。PR の「Checks」に、次が別々に出る。

| チェック | 内容 | 落ちる条件 |
|---|---|---|
| Go | `gofmt`、`go vet`、`golangci-lint`、`go test -race`(カバレッジ)、ビルド、実験定義の検証 | いずれかが失敗 |
| Go vulnerabilities | `govulncheck`(最新の Go で) | **報告のみ**(警告)。既知: `golang.org/x/text` は、修正版が Go 1.25 を要求し、EC2 の Go が対応しているか未確認のため、上げていない |
| Python | `ruff`(構文、未定義・未使用)、コンパイル、単体テスト | いずれかが失敗 |
| Shell | `shellcheck`(警告以上) | 警告以上がある |
| Infra | CDK の型検査、`npm audit` | high 以上で失敗(全レベルは要約に表示) |
| No credentials | アクセスキー、秘密鍵、アカウント付きの ARN、エンドポイント名、プライベート IP | 見つかった |
| Dependency review | PR で増えた脆弱な依存 | high 以上(リポジトリで依存グラフが有効なときだけ) |

同じ内容は、手元で `make check` でも実行できる。依存の更新は Dependabot(週 1 回)が PR を作る。

## 結果の読み方で守ること

- 各 run の開始前に、Proxy の `DatabaseConnections` が下限(約 5)に戻るまで待つ(`exp.py`)。**戻らなかった run は summary.md の警告に出る。その run の接続数は比較に使わない。**
- 比較は、繰り返し(既定 3 回)の範囲が重なるかで判断する。1 回の差は結論にしない。
- 遅延の実験(tls-latency)は、クライアント側の測定値(平均、p99)を読む。接続数は読まない。
- 規模は本番の 1/10。結論は「この条件で、こう動いた」まで。本番への当てはめは、本番の指標(`ClientConnections`、`DatabaseConnections`、`SessionPinned`、Proxy の拡張ログ)で確認する。

## 前提と制約

- Aurora は r6g.large(2 vCPU)を既定とし、`make resize CLASS=db.r6g.xlarge` で 4 vCPU にする(`-c auroraClass=` でも指定可)。負荷生成は c7g.xlarge。どちらも、バースト対応でない。
- 結果は Aurora のクラスごとに `results/<実験名>__<クラス>/` に分かれる。
- Proxy は TLS 必須ではない(`requireTLS: false`)。TLS の実験は `--tls=skip-verify` を使う。
- すべてのリソースは `RemovalPolicy.DESTROY`。
