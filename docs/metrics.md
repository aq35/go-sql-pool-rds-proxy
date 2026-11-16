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

最初の実験(2026-11-16)は、ゲージをすべて `Maximum` で取っていた。**`ClientConnections` が実ソケット数より少なく出た(10 本で 5〜8)原因は、この統計の選び方だった。** スモークテスト(同日、`pat-pin` の `base`、Go 側が数えた開いている接続 = 10)で、同じ run、同じ分を、3 つの統計で比べた:

| 統計 | `ClientConnections` | `DatabaseConnections` |
|---|---|---|
| `Maximum` | 7 | 7 |
| **`Sum`** | **10**(Go 側の数と一致) | **10** |
| `Average` | 5.0 | 5.0 |

`Average` が 5.0 で `Sum` が 10 なので、Proxy は **2 ノード**で、それぞれが約 5 を報告しているとみられる。`Maximum` は、大きいほうの 1 ノードの値だけを返す。**`Sum` が、Proxy 全体の値**。

- **確度**: 1 run、1 つの Proxy での確認(n=1)。ノード数が 2 であることは、`Average` と `Sum` の関係からの推定で、ノードごとの値は見ていない。
- **前回の結果への影響**: `DatabaseConnections`(Proxy→Aurora)、`ClientConnections`、ピン留めの数、`InTransaction` など、`Maximum` で取った接続数は、**実際より少ない**(この run では、約 3 割少ない)。`results/REPORT.md` の Proxy の接続数の値は、**下限として読む**。以後の run では、`proxy_*_sum` 列(Sum)を使う。
- **下限の 5 本(A1)の読み直し**: 「Proxy は、クライアントがいなくても Aurora への接続を 5 本は保持する」は、`Maximum` で測った値。**Proxy 全体では、約 10 本の可能性がある**(未検証。`Sum` で、アイドルの Proxy を測り直す)。

## 比べ方

- 接続数は、`Maximum` と `Sum` を同じ run、同じ分で並べて、Go 側の `db.Stats().OpenConnections`(クライアントが自分で数えた値)と照合する。
- どれが実数に近いかが決まるまで、「ClientConnections が少なく出る」「ピン留めの数」は、**断定しない**。

## Aurora(AWS/RDS、ディメンション `DBInstanceIdentifier`)

| 指標 | 粒度 | 用途 |
|---|---|---|
| `CPUUtilization` | 1 分 | 参考のみ。run の間隔が 1 分未満だと、前後の run が混ざる |
| Enhanced Monitoring の `cpuUtilization.total` | **1 秒** | run ごとの Aurora CPU(CloudWatch Logs `RDSOSMetrics`)。`os_cpu.json` に保存 |
| `Threads_running`、`Threads_connected`、`Questions` | 1 秒(自分で取得) | Aurora に直結した管理接続で、`performance_schema.global_status` を読む。**`Questions` は DB 全体のカウンタで、監視用のクエリや他のセッションも含む**(アプリが発行した QPS ではない) |
