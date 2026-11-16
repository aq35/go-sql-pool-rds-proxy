# インフラの判断と、その根拠

`infra/lib/proxy-experiment-stack.ts` の設定のうち、**測定に影響するもの**と、**安全性・費用に関わるもの**を、理由とともに記録する。実験の結果は、ここに書いた条件のもとで得られたものである。

> **この構成は、2026-11-16 のスモークテストで、実際にデプロイして確認した**(結果と、見つかった不具合は §9)。確認していない項目も §9 にある。

## 1. デプロイ時に切り替えられる設定

`make deploy CDK_ARGS='-c key=value ...'`。**測定の条件を変えるもの**は、`/etc/exp.env` に書かれ、**各 run の `meta.json`(`infra`)と manifest に記録される**(同じ run ID でも、条件が違えば別の測定として扱う)。

| 設定 | 既定 | 何を変えるか | 記録 |
|---|---|---|---|
| `requireTls` | `true` | RDS Proxy が TLS を必須にするか | `infra.require_tls` |
| `proxyAuth` | `native` | Proxy のクライアント認証。`native` = mysql_native_password、`sha2` = caching_sha2_password | `infra.proxy_auth` |
| `debugLogging` | `false` | Proxy の詳細ログ(デプロイ時の値) | `PROXY_DEBUG_LOGGING_AT_DEPLOY`、run ごとの `proxy_settings` |
| `envId` | `exp` | Proxy、ログ、ダッシュボード、アラームの名前の接尾辞(`rdsproxy-<envId>`)。複数の環境を並べるとき | `infra.env_id`、`infra.proxy_name` |
| `ttlHours` | `24` | タグ `lab:expires-on`(作成日 + ttlHours の日付)。**思い出すための目印で、自動では削除しない** | タグ |
| `budgetEmail` | なし | 指定すると、**1 日のコスト予算(アカウント全体)の 80% で通知**する。**通知のみ。何も削除しない**。アドレスは、デプロイ時に渡し、リポジトリには入れない | – |
| `budgetUsdDaily` | `20` | 上の予算の上限(USD) | – |
| `auroraClass`、`clientClass`、`perfSchema` | 以前のとおり | Aurora / EC2 のクラス、performance_schema | – |

## 2. TLS

- **既定を「必須」にした**(AWS の推奨)。以前は `requireTLS: false` だった。
- Go の負荷クライアントは、`--tls` を指定しないと、**インフラに従う**: Proxy が TLS を必須にしているときは `skip-verify` で接続する(`lab/target.go` の `effectiveTLS`)。**`skip-verify` は暗号化するが、サーバー証明書を検証しない**。検証つきの TLS とは別の条件。
- **平文の接続を意図するときは `--tls=none`**(明示)。Proxy が TLS 必須だと拒否される。そのため、平文を比べる run(`tls-latency` の `plain-*`)は、実験の定義に **`"infra": {"require_tls": "false"}`** を持つ。スタックがこの条件を満たさないときは、**実行せず、「無効(infra_mismatch)」として記録する**(黙って条件を変えない)。平文の比較は、`make deploy CDK_ARGS='-c requireTls=false'` で行う。
- **前回の結果への影響**: 前回(2026-11-16)の実験の多くは、平文(`requireTLS: false`)で測った。TLS を使ったのは `tls-latency` の一部、`patterns` の `warmup` と `stampede` など。今回の既定(TLS 必須)での結果は、**平文の結果と直接は比べられない**。

## 3. 認証方式

- 既定は `native`(mysql_native_password)のまま。**結果の比較可能性を守るため**で、黙って変えない。
- 当時 `native` に固定した理由は、古い Go ドライバが caching_sha2 のネゴシエーションで認証エラーになったこと。**現在のドライバ(v1.10)は caching_sha2_password をサポートする**ので、`-c proxyAuth=sha2` を、**別の条件**として試せる。その場合は、TLS を有効にしておく(TLS なしでは、公開鍵の取得が絡む)。結果は `infra.proxy_auth` で区別される。
- **未検証**: `sha2` で、このラボの全シナリオが動くか。DB 側のユーザー(admin、appuser)の認証プラグインとの関係。

## 4. IAM(EC2 のロール): 最小権限

| 権限 | リソース | 使うところ | 備考 |
|---|---|---|---|
| `AmazonSSMManagedInstanceCore` | – | SSM セッション・コマンド(`make` の全操作) | 管理ポリシー |
| `secretsmanager:GetSecretValue` ほか(`grantRead`) | 管理者の secret、アプリの secret | `run.sh`、ブートストラップ | |
| `secretsmanager:RotateSecret` | **アプリの secret だけ** | `secret-rotation` 実験 | |
| `cloudwatch:GetMetricData` | `*` | 指標の収集 | リソース単位の指定ができない |
| `logs:GetLogEvents`、`FilterLogEvents` | `log-group:RDSOSMetrics:*` | 1 秒の Aurora CPU(`os_cpu`) | |
| `ec2:DescribeNetworkInterfaces` | `*` | `netenv.sh`(Proxy の IP と AZ) | リソース単位の指定ができない |
| `rds:ModifyDBProxy` | **この Proxy だけ** | `proxy` ブロックのある実験(`lifecycle-sync`、`pat-idle-gap`、`pat-pin`)。設定を変えて、戻す | |
| `rds:ModifyDBProxyTargetGroup` | この Proxy と、**このアカウント・リージョンの `target-group:*`**(絞れていない) | 同上(プールの割合を変える実験) | **絞れなかった**: ターゲットグループの ARN は `target-group:prx-tg-<ランダム>` で、デプロイ時には分からない。CloudFormation の `GetAtt(TargetGroupArn)` で取ろうとすると、スタックの作成が失敗した(`Value null at 'dBProxyName'`。**スモークテストで判明**。`cdk synth` では見つからない) |
| `rds:DescribeDBProxies`、`DescribeDBProxyTargetGroups` | `*` | 設定の読み取り、復元の確認 | 読み取りのみ。リソース単位の指定は、確認できていない |
| `rds:ModifyDBInstance`、`DescribeDBInstances` | **この書き込みインスタンスだけ** | クラスの切り替え(`cap` キャンペーン)、Aurora のリソース ID の取得 | 以前は `db:*`(アカウント内の全インスタンス)だった |
| `cloudformation:SignalResource` | このスタック | ブートストラップの成功・失敗の通知 | |

**実験によって使う権限が違う**: たとえば `RotateSecret` は `secret-rotation` だけ、`ModifyDBInstance` は、クラスを切り替えるキャンペーンだけ。それ以外の実験では、使われない。

**この絞り込みが実験を妨げないかは、未検証**(インフラは、削除済み)。権限が足りないと、最初に使う実験(`proxy` ブロックのある実験、クラスの切り替え)が、`AccessDenied` で失敗し、`status.json` の `failed` に残る。

## 5. ブートストラップ(EC2 の起動時の処理)

- **失敗の検出**: いずれかのコマンドが失敗すると、CloudFormation に `FAILURE` を即座に送る(以前は、成功の通知だけで、失敗は 20 分のタイムアウトまで気づかなかった)。出力は、`/var/log/lab-bootstrap.log` と `/var/log/cloud-init-output.log` に残る。
- **パスワード**: DB の管理者・アプリのパスワードは、コマンドラインに渡さない(`ps` に見えるため)。権限 0600 の一時ファイルを `mysql --defaults-extra-file` で読み、**終了時に削除**する。ログには出ない。
- **`lab/` は、スタックに含めない**: 以前は、`lab/` のソースの hash を、EC2 の論理 ID に含めていた。そのため、ソースを変えて `cdk deploy` すると、**EC2 が置換され、`~/lab/out/` の生データが失われた**。今は、EC2 の論理 ID は固定(`ExperimentClient`)で、`lab/` は `make push`(原子的、`out/` を保つ)で配る。`make deploy` は、デプロイの後に `make push` を実行する。
- **未検証**: EC2 で、Go 1.27.4 のツールチェーンを自動取得できること(`GOTOOLCHAIN=auto`、公開サブネットのため、取得できる見込み)。

## 6. 公開サブネット(EC2)

**現状**: クライアントの EC2 は、公開サブネットにパブリック IP を持つ。**受信は許可しない**(セキュリティグループに受信規則なし)。接続は SSM のみ。

**理由**: EC2 は、`dnf`、Go のツールチェーン、Go のモジュール、AWS の API(Secrets Manager、CloudWatch、RDS、Logs)に、インターネットまたは VPC エンドポイントで届く必要がある。DB と Proxy は、隔離されたサブネットにある。

**代替案の比較**(料金は AWS の料金ページで確認すること。ここでは、構成の違いだけを書く):

| 案 | 構成 | 費用の構造 | 複雑さ |
|---|---|---|---|
| 現状 | 公開サブネット + パブリック IP | 追加なし(パブリック IPv4 の料金のみ) | 低 |
| NAT | 非公開サブネット + NAT ゲートウェイ | 時間料金 + 転送量 | 低(構成の追加は小さい) |
| VPC エンドポイント + ビルド済みバイナリ | 非公開サブネット + SSM 用 3 つ(ssm、ssmmessages、ec2messages) + Secrets Manager(すでにある)+ CloudWatch、Logs、RDS + S3 | エンドポイント数 × AZ 数 × 時間料金 | 高(バイナリを CI でビルドし、S3 経由で配る必要がある。Go のモジュールも要らなくなる) |

**判断**: 実験は短時間(数時間)・1 人で行うので、**現状のまま**とする。複数人で使う、長期間動かす、組織のセキュリティ基準がある、のいずれかになったら、NAT か、エンドポイント + ビルド済みバイナリ(CI でビルドし、S3 で配る)に移す。

## 7. 暗号化と、削除後の残り物

- **Aurora**: `storageEncrypted: true` を明示(CDK の既定は、KMS キーを指定しない限り false)。AWS 管理キー(`aws/rds`)。暗号化が測定に与える影響は、小さいと見ているが、**測っていない**。
- **EC2 の EBS**: `encrypted: true`、`deleteOnTermination: true` を明示。**EBS は、EC2 と一緒に消える**。だから、**削除の前に、必ず `make finish` で結果を取得する**(`DESTROY_AFTER=yes` は、取得と検査が通ったときだけ削除する)。
- **Secrets Manager**: 削除すると、復旧期間(最大 30 日)がある場合がある。
- **ログ**:
  - Proxy のログ(`/aws/rds/proxy/<proxy 名>`): スタックが作り、**7 日保持、削除時に消える**。デバッグログは**既定でオフ**。AWS は、調査のときだけ一時的に使うことを推奨し、ログに機密が含まれうる。`pat-pin` だけが、実験の間、有効にする(`proxy` ブロックの `DebugLogging`)。
  - **`RDSOSMetrics`**(Enhanced Monitoring、1 秒): AWS が作る**共有のロググループ**で、このスタックの管理外。**スタックを削除しても消えない**。AWS の既定の保持は 30 日で、1 秒間隔は取り込み量が多い。`make finish` が、保持期間と保存量を表示する。**自動では削除しない**(ほかの用途と共有されうるため)。不要なら、手で `aws logs delete-log-stream` する。

## 8. 費用の見張り

失敗したキャンペーンは、証拠を守るため、自動では削除しない。そのため、**忘れると、課金が続く**。次の 3 つで備える。**どれも、削除はしない**(通知と目印だけ)。

1. タグ `lab:expires-on`(目印)。
2. `-c budgetEmail=...` による、1 日のコスト予算の通知(アカウント全体のコスト)。
3. `make finish` が、毎回「Stack is still running and billing」と表示する。

## 9. スモークテストの結果(2026-11-16)

デプロイ → `make push` → キャンペーン `smoke`(`pat-pin` の `base` を 1 回、`tls-latency` の `plain-same-0` を 1 回)→ 削除。結果は `results/_smoke-2026-11-16/`。約 1 時間、費用は少額。**修正が要った不具合が 4 つ見つかった**(下の「見つかった不具合」)。

### 確認できたこと

| 項目 | 確認の方法 |
|---|---|
| デプロイが完了する(2 回目。1 回目は不具合 1 で失敗) | `cdk deploy` が成功(1,202 秒) |
| ブートストラップが成功する。パスワードの一時ファイルが残らない | `/var/log/lab-bootstrap.log` が `Complete!`、`/tmp` に一時ファイルなし |
| `make push` が動く(`xz` の展開、原子的な入れ替え) | `PUSHED`、`~/lab/lab` のバイナリ |
| EC2 で、Go 1.27.4 のツールチェーンを取得してビルドできる | `go version -m ./lab` = go1.27.4、ドライバ v1.10.1、gorm v1.31.2(**不具合 2 を直したうえで**) |
| TLS 必須で、負荷クライアントが接続できる(`skip-verify`) | `pat-pin` の `base`: 33,148 クエリ、エラー 0 |
| Proxy の設定変更と復元(`ModifyDBProxy` の実呼び出し) | 実行中の `proxy_settings.DebugLogging` = true、終了後は false、`finish` の検査が通った |
| 1 秒の Aurora CPU(Enhanced Monitoring) | `os_cpu.json` 438 点(平均 7.9%、最大 72.5%)。`logs:GetLogEvents` の権限も実際に動いた |
| インフラが合わない run は、実行せず「無効」と記録される | `tls-latency` の `plain-same-0`: `infra_mismatch: require_tls: needs false, the stack has true` |
| IAM の絞り込み | `iam simulate-principal-policy`: この書き込みインスタンス、この Proxy、この `target-group`、`RDSOSMetrics` は許可。別のインスタンス、別の Proxy、無関係な Secret、別のロググループは拒否 |
| 暗号化・削除の設定 | EBS: `Encrypted=True`、`DeleteOnTermination=True`。Aurora: `StorageEncrypted=True`。拡張モニタリング 1 秒。Proxy: `RequireTLS=True` |
| 削除後に残るもの | RDS、EC2、**EBS**、VPC エンドポイント、Proxy のログ、Secret は、すべてなし。**`RDSOSMetrics` のストリームが 3 本**残った(スタックの外。保持 30 日) |
| `finish` の削除ゲート | 証拠が揃っていないとき(不具合 3 の状態)は、削除を拒否した。直したあとは通り、削除した |

### 見つかった不具合(いずれも、`cdk synth` と単体テストでは見つからなかった)

1. **CloudFormation が、ターゲットグループの ARN を返せない。** IAM ポリシーに `GetAtt(TargetGroupArn)` を使うと、スタックの作成が失敗した(`Value null at 'dBProxyName'`)。→ `ModifyDBProxyTargetGroup` を `target-group:*` に戻した(実際の ARN は `target-group:prx-tg-<16進>`)。**この 1 つの権限は、絞れていない**(§4)。
2. **EC2 の Go は、`go1.26.10` で、`GOTOOLCHAIN=local`、`GOSUMDB=off`、`GOPROXY=direct`**(ディストリビューションの既定)。`GOTOOLCHAIN=auto` だけでは、`go.mod` の Go 1.27.4 を取得できず、「checksum database disabled by GOSUMDB=off」で失敗した。→ `make push` で、`GOTOOLCHAIN=auto GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct` を指定。
3. **`pinned`、`intx`、`borrowed` を、必須の系列にしたのが誤り。** 値が 0 を超えないとき、CloudWatch は**点を返さない**(エラーではない)。→ 点がない = 0 として扱う。エラーがある場合だけ、欠損とする。
4. **結果の取得が、別のキャンペーンの結果を、黙って上書きした。** スモークの `pat-pin/base.r1` が、前回の本番の同名の run の生データを上書きした。→ 取得は、別の run を上書きしない(拒否する)。`LAB_RESULTS=results/<名前>` で、保存先を分けられる。**前回の本番の `pat-pin/base.r1` の生データは、失われた**(集計の `summary.md` と `runs.csv` は、Git にあり、無事)。

### まだ確認していないこと

| 項目 | 確認の方法 |
|---|---|
| `rds:ModifyDBProxyTargetGroup` の実呼び出し(プールの割合を変える実験) | `lifecycle-sync` など、割合を変える実験の最初の run |
| `rds:ModifyDBInstance`(クラスの切り替え)の実呼び出し | `cap` キャンペーン(IAM のシミュレーションでは、この 1 台だけ許可と確認済み) |
| `proxyAuth=sha2` で動く | 別のデプロイで、小さな実験 |
| `requireTls=false` での平文の比較(`plain-*` が実行される) | 別のデプロイ |
| `budgetEmail` の予算の通知 | `-c budgetEmail=...` でのデプロイ |
| 標準的なキャンペーン(数時間)の長さでの、`push` の入れ替え、`recollect` | 実際のキャンペーンの運用で |
