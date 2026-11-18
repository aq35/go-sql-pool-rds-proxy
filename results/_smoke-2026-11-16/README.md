# スモークテスト(2026-11-16): デプロイ → push → 2 run → 削除

インフラの変更(TLS 必須、IAM の絞り込み、`lab/` をスタックから外す、`push`、1 秒の CPU など)を、実際にデプロイして確かめたときの記録。**測定の結果ではなく、動作確認**で、`results/REPORT.md` の数値には使わない。

- `pat-pin__db.r6g.large/base.r1`: `valid.json` は「無効」(`pinned`、`intx` の系列が空だったため)。**判定の誤り**で、そのあと直した(点がない = 0)。生データは、この判定を再現できるよう、そのまま残してある。
- `tls-latency__db.r6g.large/plain-same-0.r1`: 実行されず、無効(`infra_mismatch`)。**TLS 必須のスタックでの正しい動作**。
- 結果と、見つかった不具合: [docs/infra-decisions.md §9](../../docs/infra-decisions.md)。
- 同じ run での `Maximum` / `Sum` の比較(`ClientConnections` は Maximum 7、Sum 10): [docs/metrics.md](../../docs/metrics.md)。
