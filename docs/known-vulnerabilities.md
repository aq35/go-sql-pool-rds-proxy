# 脆弱性(govulncheck)の状態と、対応の記録

最終確認: 2026-11-16。機械可読の許可リストは [known-vulnerabilities.json](known-vulnerabilities.json)(CI の `scripts/vuln_gate.py` が読む)。

## 1. 現在の状態: 許可リストは空

Go を **1.27.4**、依存を最新にした(`golang.org/x/text` v0.44.0、`go-sql-driver/mysql` v1.10.1、`gorm.io/gorm` v1.31.2 など)。ローカルで、`govulncheck` v1.9.0 を Go 1.27.4 で実行し、**「No vulnerabilities found」**を確認した。

CI の `Go vulnerabilities` ジョブは、**許可リストにない、呼ばれている脆弱性があれば失敗する**。許可リストは今は空なので、**どの新しい脆弱性も、そのまま CI を止める**。許可リストに入れるときは、項目に**期限**と理由を付ける(期限が過ぎると失敗する)。許可した項目も、毎回の要約に表示される。

> **EC2 での確認(2026-11-16 のスモークテスト、`docs/infra-decisions.md` §9)**: EC2 の `dnf` の Go は、**1.25 系ではなく `go1.26.10`**(推定は外れた)。ディストリビューションの既定は `GOTOOLCHAIN=local`、`GOSUMDB=off`、`GOPROXY=direct` で、そのままでは `go.mod` の `go 1.27.4` を取得できず、「checksum database disabled by GOSUMDB=off」で失敗した。`make push` で `GOTOOLCHAIN=auto GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct` を指定したところ、**Go 1.27.4 で取得・ビルドできた**(`go version -m ./lab` = go1.27.4、ドライバ v1.10.1、gorm v1.31.2)。実行ごとの `meta.json` の `go_version`、`go_deps` にも残る。

## 2. 経緯(同じ誤りを繰り返さないための記録)

この PR で、脆弱性の扱いには、3 つの誤りがあった。

1. **「exit 2 は脆弱性の検出」は誤り。** `govulncheck` は、検出時は exit **3**。exit 2 は、ツールの異常終了。最初の CI では、`govulncheck` v1.1.4 が、Go 1.27.4 で `panic: unexpected expr: *ast.KeyValueExpr` とクラッシュしていたが、警告に丸めたため、**一度もスキャンできていなかった**。→ 終了コードが 0 と 3 以外なら失敗にし、ゲートは、`config` のない(ツールが動いていない)入力を拒否する。
2. **「最新パッチで標準ライブラリの指摘は消える」は、一部で誤り。** Go 1.25 の最新パッチ(1.25.14)でも、`crypto/tls`(GO-2026-6607)と `crypto/x509`(GO-2026-6809)の 2 件が残った。修正は、Go 1.26.9 / 1.26.10 以降(1.27 では 1.27.2 / 1.27.3)だけだった。→ Go 1.25 は、上流のサポートが切れている可能性が高い(Go の方針と、`stable` が 1.27.4 であることからの推定)。**Go を 1.27 に上げて解消した。**
3. **「EC2 は閉じたネットワークで、Go ツールチェーンを取得できない」は誤り。** クライアントの EC2 は、**公開サブネット**(パブリック IP あり)にある(閉じているのは、DB 側)。→ Go を上げるのに、バイナリの配布方式の変更は要らなかった。

### 解消した 3 件

| ID | 内容 | 解消の方法 |
|---|---|---|
| [GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970) | `golang.org/x/text` v0.14.0: 不正な入力での無限ループ。`gorm` のスキーマ命名(固定の構造体名・列名)から到達 | v0.44.0 に更新(修正版は v0.39.0) |
| [GO-2026-6607](https://pkg.go.dev/vuln/GO-2026-6607)(CVE-2026-97031) | `crypto/tls`: 細工された ECH メッセージで、TLS **サーバー**のメモリが尽きる。このコードは TLS クライアントのみなので、実害は考えにくかった | Go 1.27.4(修正は 1.27.2) |
| [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809)(CVE-2026-111943) | `crypto/x509`: 信頼できない中間証明書の拒否リスト。このコードは `tls=skip-verify`(検証なし)で接続するので、実害は考えにくかった | Go 1.27.4(修正は 1.27.3) |

## 3. 残っている指摘

- `npm audit`: **moderate が 2 件**(`aws-cdk-lib` が同梱する `minimatch`)。最新の `aws-cdk-lib`(2.277.0)でも残る。上流の更新待ち。CI は high 以上で失敗する。moderate は、ジョブの要約に表示する。CDK は、手元で `cdk deploy` するときだけ使う道具で、EC2 には載らない。
- Dependency review(PR で増えた脆弱な依存の検査): リポジトリの Dependency graph が無効のため、**入れていない**(README 参照)。

## 4. 最新にしたことの注意(実験結果の比較)

前回の実験(2026-11-16、`results/REPORT.md` §2〜§8)は、**MySQL ドライバ v1.8.1、gorm v1.25.12、EC2 の `dnf` の Go(版は未記録)**で測った。今回の更新で、**ドライバは v1.10.1、gorm は v1.31.2、Go は 1.27.4** になった。リリースノート上、プールや接続の再利用を直接変える記述は見当たらなかったが、v1.9.0 の「エラー処理の改善(busy buffer エラーの減少)」は、閉じた接続の扱いに触れる可能性がある。**`patterns-v2` の結果は、前回と条件が違う**ので、次の点に気をつける。

- 前回との比較は、**傾向**にとどめる。ドライバ・Go の違いが、結果の差に混ざる。
- 特に、`idle-gap`(Proxy が切った接続への再接続)と、`pin-patterns`(ドライバが送る `SET`、プリペアド)は、ドライバの挙動に依存する。
- 各 run の `meta.json` に、`go_version` と `go_deps`(ドライバ、gorm の版)が残るので、どの版で測ったかを、あとから確認できる。
