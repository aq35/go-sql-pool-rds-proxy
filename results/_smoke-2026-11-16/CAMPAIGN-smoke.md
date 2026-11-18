# campaign smoke

デプロイ直後の動作確認(約 15 分): Proxy の設定変更と復元(DebugLogging)、TLS 必須での接続、1 秒の Aurora CPU、valid.json、インフラ不一致の run の記録、finish の検査。pat-pin の base を 1 回と、tls-latency の plain-same-0 を 1 回(TLS 必須のスタックでは実行されず、無効として記録されるのが正しい)

- [lifecycle-sync__db.r6g.large](lifecycle-sync__db.r6g.large/summary.md)
- [pat-gorm-pool__db.r6g.large](pat-gorm-pool__db.r6g.large/summary.md)
- [pat-pin__db.r6g.large](pat-pin__db.r6g.large/summary.md)
- [pat-serial-parallel__db.r6g.large](pat-serial-parallel__db.r6g.large/summary.md)
- [pat-stampede__db.r6g.large](pat-stampede__db.r6g.large/summary.md)
- [pat-timeout__db.r6g.large](pat-timeout__db.r6g.large/summary.md)
- [pat-warmup__db.r6g.large](pat-warmup__db.r6g.large/summary.md)
- [tls-latency__db.r6g.large](tls-latency__db.r6g.large/summary.md)
