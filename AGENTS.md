# AGENTS.md — このリポジトリで作業するときの規約

目的: RDS Proxy + Aurora の接続数の実験を、再現できる形で行い、結論と注意点を `results/REPORT.md` に書く。README.md が全体、RUNBOOK.md が手順。

## 最初に読む順
1. README.md(構成・コマンド)  2. RUNBOOK.md(手順と、確認が要る操作)  3. docs/EXPERIMENT_PLAN.md(計画)と results/REPORT.md(結論と弱点)  4. docs/experiment-audit.md(実験の弱点)

## 守ること
- **実験は `make start EXP=<名前>`**。`lab/experiments/<名前>.json` が定義。`--step` や自作の起動スクリプトは使わない(使えない)。
- **長い処理は切り離して実行する**(`make start` がそうする)。SSM の呼び出しは 2 分前後で切れる。標準出力は 24,000 文字まで。
- **結果は `make fetch` で取得して `results/<実験名>/` に置く。インフラを削除する前に、取得済みを確認する**(EC2 上のデータは削除で消える)。
- **接続数の比較は、`summary.md` の警告(baseline 未達)を確認してから**。未達の run は、前の実験の残留(約 5〜6 分)を含む。
- 統計の取り方は `lab/metrics.json` に固定。ゲージ(接続数)は Maximum、カウンターは Sum。勝手に変えない。
- 結論には、条件(規模、繰り返し数、固定順か)と、確かめていないことを併記する。単発の差を結論にしない。
- コミット前に `make check`。実験定義を足したら、必ず question / hypothesis / reading を書く。

## 確認が要る操作(ユーザーが明示したときだけ)
- `make deploy`(課金が始まる)、`make destroy CONFIRM=yes`(削除)、`git push`。
- 「全部終わったら閉じて」のような指示は、その会話での明示なので、実験の完了、結果の取得、レポートの作成を確認してから削除してよい。
- 認証情報・DB のパスワードを、出力・ログ・コミットに出さない。

## キャンペーン(1 日分の実験)
- `make campaign` で開始し、`make campaign-status` で見る。EC2 上で動くので、手元を閉じてよい。
- 終わったら `make finish`(取得と summary 生成)。**`DESTROY_AFTER=yes` を付けたときだけ**、全部の確認(完了状態、全 run の保存、メトリクスに欠損なし、summary 生成)が通れば削除する。1 つでも欠ければ削除せず、理由を出す。
- 削除前に、ユーザーが「閉じてよい」と言っているか確認する。言っていなければ `DESTROY_AFTER` を付けない。

## 流れ(測定 → 係数 → 検証)と、AI を使う範囲
1. `make campaign`(day1: 測定)。**AI は要らない。** 手元から離れてよい。
2. `make finish` → `make derive`(係数)→ `make validation-gen` → `make push` → `make campaign CAMPAIGN=day2`(検証)→ `make finish DESTROY_AFTER=yes`。
3. **AI に読ませるのは `results/<実験>__<クラス>/summary.md` と `runs.csv` だけ**(1 run = 1 行、Status 付き)。`metrics.json`、`client.log`、CloudWatch の時系列、`cdk` / `aws` の出力は、直接読まない。調べるときは、`grep -c`、`tail -20` などで、必要な行だけを見る。
4. 失敗したときは、ログの全体ではなく、`grep -B2 -A5 -i error` の数行だけを見る。
5. ルール(docs/rules.md)の更新は、`summary.md`、`lab/coeffs.json`、`lab/experiments/*.json` だけを読んで行う。

## 判断に迷ったとき
- 仮説と結果が食い違う → 実験の弱点(docs/experiment-audit.md)に当てはまらないか、まず確認する。
- 新しい負荷を足す → 既存シナリオのフラグで表せないか、先に確認する(`lab --scenario=<名前> -h`)。表せないときだけ、新しいシナリオを作る。
- 本番の値は、実験では決まらない。実験は、傾向と、確認すべき指標を示す。
