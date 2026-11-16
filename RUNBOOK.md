# RUNBOOK — 実験の手順

読む順: README.md → AGENTS.md → この手順。認証情報は環境(`AWS_PROFILE` など)にあるものを使い、出力しない。

## 0. 準備(Mac)
- `brew install node awscli jq go` と `brew install --cask session-manager-plugin`。システムの bash は 3.2。スクリプトは 3.2 で動く書き方なので、bash 4 の機能を足さない。BSD の `date` / `sed` に注意(`sed -i ''`、`date -u +%Y-%m-%dT%H:%M:%SZ`)。
- 長い待ちがある。Mac のスリープで手元の待機が止まっても、EC2 上の実験は止まらない(再開は `make status`)。
- `make check` が通ること。

## 1. 構築(課金が始まる)
1. `aws sts get-caller-identity` でアカウントとリージョンを確認し、**ユーザーの了承を得てから** `make deploy`。
2. 初回のみ `cd infra && npx cdk bootstrap`。
3. 構築後の確認: `scripts/ssm-run.sh 'cloud-init status --wait; ls ~/lab/lab'`(バイナリがあること)。無ければ `sudo tail -50 /var/log/cloud-init-output.log`。
4. 手動で削除する場合のコマンドを、ユーザーに伝える: `make destroy CONFIRM=yes`(同じアカウント・リージョンの認証情報が必要)。

## 2. 実験
```bash
make start EXP=<名前> REPEAT=3     # 例: idle-sweep(約 1 時間 × 繰り返し)。EC2 上で切り離して動く
make status EXP=<名前>             # 10〜15 分おき
make fetch EXP=<名前>              # 終わったら取得。results/<名前>/summary.md
```
- 実験の中身(問い、仮説、読み方)は `lab/experiments/<名前>.json`。
- 各 run は、開始前に `DatabaseConnections` が下限へ戻るまで待つ(最大 20 分)。戻らなかった run は、summary.md の警告に出る。再実験するか、その run を比較から外す。
- `lab/` を直したら `make check` → `make push`(再構築なし)。バイナリは EC2 上で再ビルドされる。
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
- Proxy の拡張ログ(ロググループ `/aws/rds/proxy/rdsproxy-exp`): ピン留めの理由の集計。メッセージの文言は事前にわからないので、まず種類を列挙して `results/proxy-log-messages.txt` に残す。
- ローテーション(`--rotate-secret-every`): CloudTrail の `RotateSecret` / `PutSecretValue` と時刻を合わせる(`SECRET=app` が要る)。

## 4. レポート(`results/REPORT.md`、日本語)
1. 結論(3〜5 行): どの仮説を、どのデータが支持するか。
2. 実験ごとの表: summary.md の表と、範囲(繰り返し)。baseline 未達の run は明示。
3. 注意点: 規模(1/10)、繰り返し数、固定順か、確かめていないこと。
4. 本番で確認する指標(`ClientConnections` と `DatabaseConnections` の重ね合わせ、`SessionPinned`、`processlist` の接続元別集計、拡張ログ)。
`results/` を、秘密情報なしでコミットする。

## 5. 削除
**ユーザーが削除を求めたときだけ**(「終わったら閉じて」という明示の指示も含む)。
1. 結果が `results/` に取得済みであること、REPORT.md が書けていること。
2. Proxy の設定が既定に戻っていること: `aws rds describe-db-proxies --db-proxy-name rdsproxy-exp`(IdleClientTimeout 1800)、`describe-db-proxy-target-groups`(80 / 10)。
3. `make destroy CONFIRM=yes` → `aws cloudformation describe-stacks --stack-name RdsProxyPinningExperiment`(存在しないこと)。
4. 残っていないことを確認して報告する: RDS クラスタ・Proxy、EC2、VPC エンドポイント、ロググループ `/aws/rds/proxy/rdsproxy-exp`、Secrets(削除待ちの期間がある場合は、そう伝える)。`CDKToolkit` は共有なので残す。削除に失敗したら、数分待って再実行(Proxy の ENI が原因のことが多い)。
