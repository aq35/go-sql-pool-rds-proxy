# 既知の脆弱性(govulncheck)と、対応の計画

最終確認: 2026-11-16。機械可読の許可リストは [known-vulnerabilities.json](known-vulnerabilities.json)(CI の `scripts/vuln_gate.py` が読む)。

## 1. 現在の状態

CI の `Go vulnerabilities` ジョブは、**許可リストにない、呼ばれている脆弱性があれば失敗する**(ブロッキング)。許可リストの項目には**期限**があり、過ぎると、そのジョブが失敗する。許可した項目も、ジョブの要約に毎回表示される。

| ID | モジュール | 見つかった版 | 修正版 | 状態 |
|---|---|---|---|---|
| [GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970) | `golang.org/x/text` | v0.14.0 | **v0.39.0** | 許可(期限 2026-12-31)。**未修正** |

標準ライブラリの脆弱性(21 件)は、ローカルの Go 1.25.0 が古いための結果で、CI(Go 1.25 の最新パッチ)には出ない。ただし、**CI の Go と、EC2 でビルドする Go は別物**(§3)。

> **訂正(2026-11-16)**: この PR の最初の CI は、`govulncheck` が「exit 2」を返したのを、脆弱性の検出として扱っていた。**exit 2 は、ツールの異常終了**(検出は exit 3)。実際は、`govulncheck` v1.1.4 が、CI の Go 1.27.4 で `panic: unexpected expr: *ast.KeyValueExpr` とクラッシュしており、**CI 上では、一度もスキャンできていなかった**(上の数値は、ローカルの実行による)。CI は、(1) スキャンの Go を 1.25 の最新パッチに固定し、(2) 終了コードが 0 と 3 以外なら失敗にし、(3) ゲートは、`config` のない(ツールが動いていない)入力を拒否するようにした。

## 2. GO-2026-5970(`golang.org/x/text`、入力が不正なときの無限ループ)

- **到達可能性**: `govulncheck` のトレースでは、呼ばれている。経路: `gorm.io/gorm/schema/naming.go` → `golang.org/x/text/cases.Caser.String` → `transform` → `cases/map.go` → `unicode/norm.Form.Properties`(`gorm` のスキーマ初期化)。
- **入力の出どころ**: `gorm` が大文字化するのは、構造体名・列名のような、**アプリ側が書いた固定の文字列**。外部からの入力は通らない。
- **影響の見立て(推定)**: 悪用は考えにくい。実験用の閉じたネットワークの中の負荷試験ツールで、信頼できない入力を受けない。それでも、「到達する」ことは事実なので、直す前提で期限を付けて許可している。
- **修正の障害**: v0.39.0 は、`go.mod` の `go` 行を **1.22 → 1.25.0** に上げる(ローカルで確認: `go get golang.org/x/text@v0.39.0` で上がる。ビルド、`go vet`、`go test` は通った。`govulncheck` で指摘が消えることは、アップグレード時に確認する)。EC2 の Go が 1.25 未満だと、ビルドできない(EC2 は閉じたネットワークで、Go ツールチェーンを自動で取得できない)。

## 3. EC2 の Go のバージョン

- **構成**: `ec2.MachineImage.latestAmazonLinux2023(ARM_64)` + `dnf install -y golang`(`infra/lib/proxy-experiment-stack.ts`)。つまり、デプロイ時点の AL2023 の最新の `golang` パッケージが入る。
- **推定**: AL2023 のリリースノートでは、2026 年 5〜6 月の時点で、`golang` は 1.25.10〜1.25.11([2023.11.20260526](https://docs.amazonaws.cn/en_us/linux/al2023/release-notes/relnotes-2023.11.20260526.md)、[2023.12.20260622](https://docs.amazonaws.cn/en_us/linux/al2023/release-notes/relnotes-2023.12.20260622.md))。
- **未確認**: 2026-11-16 に実際にデプロイしたインスタンスの `go version` は、**記録していなかった**(インスタンスは削除済み)。上は、リリースノートからの推定で、実機の確認ではない。
- **今後**: `exp.py` が、run ごとの `meta.json` に、EC2 の `go version`(`go_version`)を記録する(この PR で追加)。`make push` と `make deploy` の後に、`scripts/ssm-run.sh 'go version'` でも確認できる。
- **CI との違い**: CI の `govulncheck`(Go 1.25 の最新パッチ)は、(a)`go.mod` / `go.sum` の**依存モジュール**(EC2 でも同じ版が使われるので、判定が一致する)と、(b)**CI の Go の標準ライブラリ**(EC2 の Go とは別)を調べる。(b) は、EC2 の Go の脆弱性を保証しない。EC2 の標準ライブラリの脆弱性は、`go_version` を、`govulncheck` が示す「Fixed in」の版と見比べて、手で確認する(§4 の手順 1)。

## 4. アップグレードの計画と検証

1. **次のデプロイで、EC2 の Go を確認する**: `scripts/ssm-run.sh 'go version'`。`meta.json` の `go_version` にも残る。**1.25.0 以上**なら、手順 2 へ。未満なら、`dnf` のリポジトリの更新を待つか、Go を AMI かユーザーデータで明示的に入れる(閉じたネットワークで取得できる経路が要る)。
2. **ローカルで上げる**: `cd lab && go get golang.org/x/text@v0.39.0 && go mod tidy`(`go` 行が 1.25.0 になる)。`make check`、`govulncheck ./...` で、指摘が消えたことを確認する。
3. **EC2 で、原子的にビルドする**: `make push`(新しいディレクトリで、ビルドと `exp.py check` に成功したときだけ入れ替わる)。失敗しても、実験中のツリーは壊れない。
4. **回帰の確認**: `gorm` を使う `pat-gorm-pool`(全 6 テスト)と、`pat-pin` の `base` を実行し、前回(`results/REPORT.md` §2、§4)と同じ結果(MaxOpen=1 で固まるのは 2 つだけ、base のピン留めは 0)になることを確認する。
5. **許可リストの項目を消す**: `docs/known-vulnerabilities.json` から GO-2026-5970 を削除する。ゲートは、すでにブロッキングなので、以後、同種の脆弱性は、許可なしでは通らない。

期限(2026-12-31)までに、手順 1〜5 を終えられないときは、期限を延ばす理由を、`reason` に書く(期限切れで CI が失敗するので、放置できない)。

## 5. 同じ区分の、ほかの指摘

- `npm audit`: **moderate が 2 件**(`aws-cdk-lib` が依存する `minimatch`)。CI は high 以上で失敗する。moderate は、ジョブの要約に表示する。CDK は、手元で `cdk deploy` するときだけ使うビルド用の道具で、EC2 には載らない。`aws-cdk-lib` の更新(Dependabot が週 1 回 PR を作る)で解消を待つ。
- Dependency review(PR で増えた脆弱な依存の検査): リポジトリの Dependency graph が無効のため、**入れていない**(README 参照)。
