# 2 回目のスモークテスト(2026-11-17): 残りの確認

`proxyAuth=sha2`(TLS 必須)でデプロイし、`pat-gorm-pool` を 1 回動かした。**測定の結果ではなく、動作確認**で、`results/REPORT.md` の数値には使わない。

- `pat-gorm-pool__db.r6g.large/gorm.r1`: `valid.json` は有効。`meta.json` の `infra.proxy_auth` は `sha2`。結果(固まるのは `max_open=1` の 2 つだけ)は、`native` のときと同じ。
- ほかに、実機で、`ModifyDBProxyTargetGroup`、`ModifyDBInstance`、`make push` の 2 回目、`recollect` を確認した(結果のログは、このディレクトリにはない。要約は [docs/infra-decisions.md §9](../../docs/infra-decisions.md))。
