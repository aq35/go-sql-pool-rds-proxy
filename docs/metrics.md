# 指標の定義(何を、どの統計で、どの粒度で見るか)

出典: [Monitoring RDS Proxy metrics with Amazon CloudWatch](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-proxy.monitoring.html)(2026-11-16 に確認)。

## RDS Proxy(AWS/RDS、ディメンション `ProxyName`)

| 指標 | 種類 | 粒度 | AWS が推奨する統計 | このリポジトリでの取り方 |
|---|---|---|---|---|
| `ClientConnections` | 現在値(ゲージ) | 1 分 | **Sum** | `<key>`(Maximum)と `<key>__Sum`、`<key>__Average` |
| `DatabaseConnections` | ゲージ | 1 分 | **Sum** | 同上 |
| `DatabaseConnectionsCurrentlySessionPinned` | ゲージ | 1 分 | **Sum** | 同上 |
| `DatabaseConnectionsCurrentlyInTransaction` | ゲージ | 1 分 | **Sum** | 同上 |
| `DatabaseConnectionsCurrentlyBorrowed` | ゲージ | 1 分 | **Sum** | 同上 |
| `MaxDatabaseConnectionsAllowed` | ゲージ | 1 分 | **Sum** | 同上 |
| `DatabaseConnectionsSetupSucceeded` | カウンタ | 1 分以上 | Sum | Sum |
| `DatabaseConnectionsBorrowLatency` | 遅延(µs) | 1 分以上 | Average | Average |

## 重要な注意(AWS のドキュメントより)

> 1 つの proxy は、複数の EC2 インスタンスで提供されることがある。RDS は、proxy に関連する各インスタンスについて、これらの指標を発行する。proxy 全体の値は、CloudWatch の統計で集計する。

つまり、**インスタンスごとに値が出る**。`Maximum` は、いちばん大きい 1 インスタンスの値しか見ない。一方、`Sum` は、全インスタンスの合計になる。

最初の実験(2026-11-16)は、ゲージをすべて `Maximum` で取っていた。**そのため、`ClientConnections` が実ソケット数より少なく出た(10 本で 5〜8)原因は、この統計の選び方だった可能性がある**(未検証。`Sum` と並べて取った run で確認する)。同じ理由で、`DatabaseConnections`、ピン留めの数も、実際より少ない可能性がある。

## 比べ方

- 接続数は、`Maximum` と `Sum` を同じ run、同じ分で並べて、Go 側の `db.Stats().OpenConnections`(クライアントが自分で数えた値)と照合する。
- どれが実数に近いかが決まるまで、「ClientConnections が少なく出る」「ピン留めの数」は、**断定しない**。

## Aurora(AWS/RDS、ディメンション `DBInstanceIdentifier`)

| 指標 | 粒度 | 用途 |
|---|---|---|
| `CPUUtilization` | 1 分 | 参考のみ。run の間隔が 1 分未満だと、前後の run が混ざる |
| Enhanced Monitoring の `cpuUtilization.total` | **1 秒** | run ごとの Aurora CPU(CloudWatch Logs `RDSOSMetrics`)。`os_cpu.json` に保存 |
| `Threads_running`、`Threads_connected`、`Questions` | 1 秒(自分で取得) | Aurora に直結した管理接続で、`performance_schema.global_status` を読む。**`Questions` は DB 全体のカウンタで、監視用のクエリや他のセッションも含む**(アプリが発行した QPS ではない) |
