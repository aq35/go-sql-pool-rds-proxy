# RUNBOOK — 実験の手順

読む順: README.md → AGENTS.md → この手順。認証情報は環境(`AWS_PROFILE` など)にあるものを使い、出力しない。

## 0. 準備(Mac)
- `brew install node awscli jq go` と `brew install --cask session-manager-plugin`。システムの bash は 3.2。スクリプトは 3.2 で動く書き方なので、bash 4 の機能を足さない。BSD の `date` / `sed` に注意(`sed -i ''`、`date -u +%Y-%m-%dT%H:%M:%SZ`)。
- 長い待ちがある。Mac のスリープで手元の待機が止まっても、EC2 上の実験は止まらない(再開は `make status`)。
- `make check` が通ること。

## 1. 構築(課金が始まる)
1. `aws sts get-caller-identity` でアカウントとリージョンを確認し、**ユーザーの了承を得てから** `make deploy`。測定の条件を変えるときは `make deploy CDK_ARGS='-c requireTls=false ...'`(設定の一覧と意味は [docs/infra-decisions.md](docs/infra-decisions.md) §1)。**既定は、TLS 必須・デバッグログなし**。費用の通知がほしいときは、`-c budgetEmail=<自分のアドレス>`(通知のみ。アドレスは、リポジトリに書かない)。
2. 初回のみ `cd infra && npx cdk bootstrap`。
3. `make deploy` は、スタックを作った後、`make push` で `lab/` を EC2 に配る(`lab/` はスタックに含めない。ソースを直しても、EC2 は置換されない)。構築後の確認: `scripts/ssm-run.sh 'cat /var/log/lab-bootstrap.log | tail -20; ls ~/lab/lab; go version'`(バイナリがあること、実際に使われた Go のバージョン)。ブートストラップが失敗した場合は、CloudFormation にすぐ `FAILURE` が届く。原因は `/var/log/lab-bootstrap.log` と `/var/log/cloud-init-output.log`。
4. 手動で削除する場合のコマンドを、ユーザーに伝える: `make destroy CONFIRM=yes`(同じアカウント・リージョンの認証情報が必要)。

## 2. 実験
```bash
make start EXP=<名前> REPEAT=3     # 例: idle-sweep(約 1 時間 × 繰り返し)。EC2 上で切り離して動く
make status EXP=<名前>             # 10〜15 分おき
make fetch EXP=<名前>              # 終わったら取得。results/<名前>/summary.md
```
- **EC2 の Go は、ディストリビューションの既定(`GOTOOLCHAIN=local`、`GOSUMDB=off`、`GOPROXY=direct`)**。`go.mod` の Go を取得させるため、`make push` は、環境変数で上書きしてビルドする。手でビルドするときも同じ(`docs/infra-decisions.md` §9)。
- 実験の中身(問い、仮説、読み方)は `lab/experiments/<名前>.json`。
- 各 run は、開始前に `DatabaseConnections` が下限へ戻るまで待つ(最大 20 分)。戻らなかった run は、summary.md の警告に出る。再実験するか、その run を比較から外す。
- 結果は、実験名ごとのディレクトリに取得される(`results/<実験名>__<クラス>/`)。**別のキャンペーン(スモークテストなど)の結果を、同じ名前の本番の結果に混ぜない**: `LAB_RESULTS=results/<名前> make finish CAMPAIGN=...` で、保存先を分ける。取得は、同名で別の run の証拠を、上書きしない(拒否する)。
- `lab/` を直したら `make check` → `make push`(再構築なし)。新しいディレクトリに展開してビルドと `exp.py check` を行い、成功したときだけ入れ替わる(実験の実行中は拒否される。結果の `~/lab/out/` は引き継がれる)。
- 各 run の `meta.json` には、使った Go と依存のバージョン(`go_version`、`go_deps`)、インフラの条件(`infra`: TLS 必須か、認証方式)、実行中の Proxy の設定(`proxy_settings`)が残る。平文を比べる run(`infra.require_tls=false` を要求)は、スタックが合わないときは、**実行されず、「無効」として記録される**。
- 途中で異常終了した run があれば、`results/<名前>/<run>/client.log` を読む(`Access denied`、`Too many connections`、借用タイムアウトも結果の一部)。

### 2b. 1 日分(キャンペーン)
```bash
make campaign                  # lab/campaigns/day1.json: 2 vCPU で実験 → 4 vCPU に変更して膝を測る(約 10 時間)
make campaign-status           # 10〜30 分おき。失敗したら、原因を直して同じコマンドで再開できる(完了した run は飛ばす)
make finish                    # 取得・summary 生成・確認。削除はしない
make finish DESTROY_AFTER=yes  # 上に加えて、確認がすべて通れば削除
```
スペックの変更(resize)は writer の再起動を伴い、10〜15 分かかる。CloudFormation にはドリフトとして見えるが、削除には影響しない。

## 3. 追加の証拠(必要なとき)
- Proxy の拡張ログ(ロググループ `/aws/rds/proxy/<Proxy 名>`。Proxy 名は、スタックの出力 `ProxyName`。既定の `rdsproxy-exp`): ピン留めの理由の集計。**デバッグログは既定でオフ**。`pat-pin` の間だけ有効になる。それ以外で調べるときは、一時的に有効にし、終わったら戻す(ログに SQL が含まれうる)。メッセージの文言は事前にわからないので、まず種類を列挙して `results/proxy-log-messages.txt` に残す。
- ローテーション(`--rotate-secret-every`): CloudTrail の `RotateSecret` / `PutSecretValue` と時刻を合わせる(`SECRET=app` が要る)。

## 4. レポート(`results/REPORT.md`、日本語)
1. 結論(3〜5 行): どの仮説を、どのデータが支持するか。
2. 実験ごとの表: summary.md の表と、範囲(繰り返し)。baseline 未達の run は明示。
3. 注意点: 規模(1/10)、繰り返し数、固定順か、確かめていないこと。
4. 本番で確認する指標(`ClientConnections` と `DatabaseConnections` の重ね合わせ、`SessionPinned`、`processlist` の接続元別集計、拡張ログ)。
`results/` を、秘密情報なしでコミットする。

## 5. 削除
**ユーザーが削除を求めたときだけ**(「終わったら閉じて」という明示の指示も含む)。
1. **結果が `results/` に取得済みであること**、REPORT.md が書けていること。**EC2 の EBS は、EC2 と一緒に消える**(`deleteOnTermination`)ので、`~/lab/out/` の生データは、削除の後に取り戻せない。`make finish` の確認(必須の系列、`valid.json`、`os_cpu`)が通っていること。
2. Proxy の設定が、デプロイ時の値に戻っていること。`make finish` が、スタックの出力(`ProxyName`、`DebugLoggingAtDeploy`)を使って確かめる(手で見るなら `aws rds describe-db-proxies --db-proxy-name <Proxy 名>`: IdleClientTimeout 1800、`describe-db-proxy-target-groups`: 80 / 10)。
3. `make destroy CONFIRM=yes` → `aws cloudformation describe-stacks --stack-name RdsProxyPinningExperiment`(存在しないこと)。
4. 残っていないことを確認して報告する: RDS クラスタ・Proxy、EC2、**EBS のボリューム**、VPC エンドポイント、ロググループ `/aws/rds/proxy/<Proxy 名>`、Secrets(削除待ちの期間がある場合は、そう伝える)。`CDKToolkit` は共有なので残す。削除に失敗したら、数分待って再実行(Proxy の ENI が原因のことが多い)。
5. **スタックの外に残るもの**: `RDSOSMetrics`(Enhanced Monitoring、1 秒間隔のログ)。AWS が作る共有のロググループで、**スタックを削除しても消えない**。保持期間(AWS の既定は 30 日)と保存量を確認して報告する(`make finish` が表示する)。**自動では削除しない**(ほかの用途と共有されうるため)。不要なら、このインスタンスのストリームだけを、手で削除する(`aws logs delete-log-stream --log-group-name RDSOSMetrics --log-stream-name <DbiResourceId>`)。
6. 費用: 忘れると課金が続く。タグ `lab:expires-on` で、期限を確認できる。`budgetEmail` を指定していれば、1 日のコスト予算の 80% で通知が来る(通知のみ)。
