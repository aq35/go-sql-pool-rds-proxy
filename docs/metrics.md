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

## Proxy が保つ接続の「下限」: 使われたあとは、アイドルの接続を 10 本(2 ノードで 5 本ずつ)保つ(2026-11-17)

前回までは、「Proxy は、クライアントがいなくても、Aurora への接続を 5 本は保持する(下限)」とした。`Sum` で測り直すと、次のとおりだった(各 n は小さい。`results/_run1-2026-11-17/`):

- **一度も使われていない Proxy は、0 本**(`pat-floor`、2 回、各 9 分。`Max`、`Sum`、`Average` のすべて 0)。**ログインだけでも 0 本**(`trigger-isolation` の `connect`)。
- **使われたあとは、クライアントが 0 でも、`Sum` = 10(`Max` = 5、`Average` = 5.0)で、安定する。** 少なくとも 1 時間以上、動かなかった(クライアントのいない、run 前の待ち 20 分 × 3 回以上で、同じ値)。
- **10 は、設定の `MaxIdleConnectionsPercent = 10`(`max_connections` 100 の 10%)と一致する。** Proxy は、使われたあと、アイドルの接続を、この上限まで保つとみられる(**推定**。`MaxIdleConnectionsPercent` を変えて、本数が変わることは、未確認)。
- したがって、前回の「5 本」は、**ノード単位の `Maximum` としては、正しい値**で、**Proxy 全体では 10 本**。「前の実験の残り」ではなく、**定常のアイドルの接続**だった。
- 残り(バースト後に、25 本が約 5 分残る現象)は、この 10 本を**超えた分**。待ちの基準は、`Sum ≤ 10`(`FLOOR_DEFAULT`)。

### 訂正(同日、私の誤り)
最初の報告では、「5 本は前の実験の残りで、待ちの基準(`Maximum ≤ 6`)は、`Sum` で 10 本が残っていても通ってしまう欠陥だった」と書き、基準を `Sum ≤ 2` に変えた。**これは誤りだった。** 10 本は残りではなく定常値で、`Sum ≤ 2` は、一度も満たされず、`trigger-isolation` の 7 run がすべて「baseline 未達」になった(約 3.5 時間の無駄)。`Maximum ≤ 6` の待ちは、デフォルトの設定では、`Sum ≤ 10〜12` と同等で、実用上は妥当だった。いまは、`Sum ≤ 10` で、全 Proxy 基準にそろえた。

前回までの接続数の結論は、`Maximum`(ノード単位)で測ったもの。`Sum`(Proxy 全体)で測り直すまで、数値は「ノード単位」として読む(Proxy 全体は、約 2 倍の可能性)。
