import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as rds from 'aws-cdk-lib/aws-rds';
import * as sm from 'aws-cdk-lib/aws-secretsmanager';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as cw from 'aws-cdk-lib/aws-cloudwatch';
import * as budgets from 'aws-cdk-lib/aws-budgets';

/** Production is Aurora max_connections=1000 / Proxy 80% / idle 10%.  This stack is the 1/10 scale model. */
const AURORA_MAX_CONNECTIONS = 100;
const PROXY_MAX_CONNECTIONS_PERCENT = 80; // -> 80 backend connections (prod: 800)
const PROXY_MAX_IDLE_CONNECTIONS_PERCENT = 10; // -> 10 (prod: 100)
const DB_NAME = 'exp';

/**
 * Deploy-time knobs (cdk -c key=value, or `make deploy CDK_ARGS="-c key=value"`). Each one that changes what is measured is exported to
 * /etc/exp.env on the client and recorded in every run's meta.json, so a result always says which infrastructure it came from.
 *   envId        name suffix for the Proxy, log group, dashboard and alarms (default "exp"); lets several environments coexist
 *   requireTls   RDS Proxy requires TLS (default true). "false" only for experiments that compare plain TCP.
 *   proxyAuth    client password auth of the Proxy: "native" (mysql_native_password, default: comparable with earlier results) | "sha2"
 *   debugLogging RDS Proxy debug logging (default false: it adds log volume and may expose SQL; turn on only to investigate pinning)
 *   ttlHours     tag lab:expires-on = deploy day + ttlHours (default 24); nothing deletes the stack automatically
 *   budgetEmail  if set, a DAILY account-wide cost budget notifies this address at 80% (notification only; never deletes)
 *   budgetUsdDaily  limit of that budget in USD (default 20)
 *   auroraClass, clientClass, perfSchema  as before
 */
export class ProxyExperimentStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    const ctx = (k: string) => this.node.tryGetContext(k);
    const flag = (k: string, dflt: boolean) => (ctx(k) === undefined ? dflt : String(ctx(k)) === 'true');
    const envId = String(ctx('envId') ?? 'exp');
    if (!/^[a-z][a-z0-9-]{0,20}$/.test(envId)) throw new Error(`envId must match ^[a-z][a-z0-9-]{0,20}$ (got ${envId})`);
    const PROXY_NAME = `rdsproxy-${envId}`;
    const requireTls = flag('requireTls', true);
    const debugLogging = flag('debugLogging', false);
    const proxyAuth = String(ctx('proxyAuth') ?? 'native');
    if (proxyAuth !== 'native' && proxyAuth !== 'sha2') throw new Error(`proxyAuth must be native or sha2 (got ${proxyAuth})`);
    const ttlHours = Number(ctx('ttlHours') ?? 24);
    cdk.Tags.of(this).add('lab:project', 'go-sql-pool-rds-proxy');
    cdk.Tags.of(this).add('lab:env', envId);
    cdk.Tags.of(this).add('lab:expires-on', new Date(Date.now() + ttlHours * 3600 * 1000).toISOString().slice(0, 10)); // a reminder, not a deletion

    // ---------------------------------------------------------------- VPC (no NAT)
    const vpc = new ec2.Vpc(this, 'Vpc', {
      maxAzs: 2,
      natGateways: 0,
      subnetConfiguration: [
        { name: 'public', subnetType: ec2.SubnetType.PUBLIC, cidrMask: 24 },
        { name: 'isolated', subnetType: ec2.SubnetType.PRIVATE_ISOLATED, cidrMask: 24 },
      ],
    });

    // ---------------------------------------------------------------- Security groups
    const clientSg = new ec2.SecurityGroup(this, 'ClientSg', { vpc, description: 'Experiment EC2 client', allowAllOutbound: true });
    const proxySg = new ec2.SecurityGroup(this, 'ProxySg', { vpc, description: 'RDS Proxy', allowAllOutbound: true });
    const dbSg = new ec2.SecurityGroup(this, 'DbSg', { vpc, description: 'Aurora cluster', allowAllOutbound: true });
    proxySg.addIngressRule(clientSg, ec2.Port.tcp(3306), 'EC2 client to RDS Proxy');
    dbSg.addIngressRule(clientSg, ec2.Port.tcp(3306), 'EC2 client to Aurora (direct, for comparison)');
    dbSg.addIngressRule(proxySg, ec2.Port.tcp(3306), 'RDS Proxy to Aurora');
    // RDS Proxy needs to talk to itself on the same SG when targets are in it; harmless here.
    proxySg.addIngressRule(proxySg, ec2.Port.allTraffic(), 'self');

    // ---------------------------------------------------------------- Aurora MySQL 3.x, max_connections = 100
    const engine = rds.DatabaseClusterEngine.auroraMysql({ version: rds.AuroraMysqlEngineVersion.VER_3_08_2 });
    const instanceParams = new rds.ParameterGroup(this, 'InstanceParams', {
      engine,
      description: `max_connections=${AURORA_MAX_CONNECTIONS} (1/10 of production)`,
      parameters: {
        max_connections: String(AURORA_MAX_CONNECTIONS),
        // Off by default: r6g refuses performance_schema_instrument ("Invalid / Unmodifiable / Unsupported DB Parameter").
        // `-c perfSchema=true` lets the client sampler see user-level locks (GET_LOCK) in performance_schema.metadata_locks
        // (works on some classes only); otherwise the sampler reports user_locks=n/a.
        ...(this.node.tryGetContext('perfSchema') !== 'true'
          ? {}
          : { performance_schema: '1', performance_schema_instrument: 'wait/lock/metadata/sql/mdl=ON' }),
      },
    });

    const cluster = new rds.DatabaseCluster(this, 'Aurora', {
      engine,
      credentials: rds.Credentials.fromGeneratedSecret('admin'), // Secrets Manager
      defaultDatabaseName: DB_NAME,
      writer: rds.ClusterInstance.provisioned('Writer', {
        // 2 vCPU: r6g.large (default). 4 vCPU: -c auroraClass=r6g.xlarge, or `make resize CLASS=db.r6g.xlarge` on a running stack.
        // Non-burstable on purpose: a t-class CPU-credit balance would change the result between runs.
        instanceType: new ec2.InstanceType(String(this.node.tryGetContext('auroraClass') ?? 'r6g.large')),
        parameterGroup: instanceParams,
        enablePerformanceInsights: false,
      }),
      readers: [],
      // Enhanced Monitoring every second (OS CPU etc. -> CloudWatch Logs group RDSOSMetrics). The 1-minute AWS/RDS CPUUtilization
      // cannot separate runs that are less than a minute apart. CDK creates the monitoring role.
      monitoringInterval: cdk.Duration.seconds(1),
      vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_ISOLATED },
      securityGroups: [dbSg],
      backup: { retention: cdk.Duration.days(1) },
      // Explicit: the CDK default is false unless a KMS key is given. AWS-managed key (aws/rds) is enough for a lab; the cost of
      // encryption on these measurements is expected to be negligible but was not measured.
      storageEncrypted: true,
      deletionProtection: false,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });
    const secret = cluster.secret!;
    secret.applyRemovalPolicy(cdk.RemovalPolicy.DESTROY);

    // ---------------------------------------------------------------- Secrets Manager rotation (single application user)
    // Secrets Manager's Lambda must reach the Secrets Manager API from isolated subnets (no NAT).
    vpc.addInterfaceEndpoint('SecretsManagerEndpoint', { service: ec2.InterfaceVpcEndpointAwsService.SECRETS_MANAGER });

    // The rotation schedule depends on the EC2 bootstrap creating appuser.
    const appSecret = new rds.DatabaseSecret(this, 'AppSecret', {
      username: 'appuser',
      dbname: DB_NAME,
    }).attach(cluster);
    appSecret.applyRemovalPolicy(cdk.RemovalPolicy.DESTROY);
    // ---------------------------------------------------------------- RDS Proxy
    // Pre-create the log group AWS writes to, so `cdk destroy` removes it too.
    const proxyLogGroup = new logs.LogGroup(this, 'ProxyLogs', {
      logGroupName: `/aws/rds/proxy/${PROXY_NAME}`,
      retention: logs.RetentionDays.ONE_WEEK,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    const proxy = new rds.DatabaseProxy(this, 'Proxy', {
      dbProxyName: PROXY_NAME,
      proxyTarget: rds.ProxyTarget.fromCluster(cluster),
      secrets: [secret, appSecret],
      vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_ISOLATED },
      securityGroups: [proxySg],
      maxConnectionsPercent: PROXY_MAX_CONNECTIONS_PERCENT,
      maxIdleConnectionsPercent: PROXY_MAX_IDLE_CONNECTIONS_PERCENT,
      // Off by default: AWS recommends debug logging only for temporary compatibility / performance investigations (the logs can
      // contain sensitive information). pat-pin turns it on for its runs through the experiment's `proxy` block (DebugLogging).
      debugLogging,
      iamAuth: false,
      // TLS is required by default (AWS recommends it). The lab's `--tls=skip-verify` encrypts but does NOT verify the server
      // certificate, so it is a different condition from verified TLS. `-c requireTls=false` is for the plain-TCP comparison only.
      requireTLS: requireTls,
      // "native" keeps results comparable with the earlier runs (an older Go driver had trouble negotiating caching_sha2). The current
      // driver (v1.10) supports caching_sha2_password; `-c proxyAuth=sha2` runs that as a separate, recorded condition.
      clientPasswordAuthType: proxyAuth === 'sha2'
        ? rds.ClientPasswordAuthType.MYSQL_CACHING_SHA2_PASSWORD
        : rds.ClientPasswordAuthType.MYSQL_NATIVE_PASSWORD,
      borrowTimeout: cdk.Duration.seconds(30),
    });
    proxy.node.addDependency(proxyLogGroup);
    proxy.applyRemovalPolicy(cdk.RemovalPolicy.DESTROY);

    // ---------------------------------------------------------------- EC2 client (SSM only, no SSH)
    // The lab sources are NOT part of this stack: they are delivered by `make push` (atomic, keeps ~/lab/out). Putting them in the
    // instance's user data / logical id made every source change replace the instance and lose its raw results.

    const role = new iam.Role(this, 'ClientRole', {
      assumedBy: new iam.ServicePrincipal('ec2.amazonaws.com'),
      managedPolicies: [iam.ManagedPolicy.fromAwsManagedPolicyName('AmazonSSMManagedInstanceCore')],
    });
    secret.grantRead(role);
    appSecret.grantRead(role);
    role.addToPolicy(new iam.PolicyStatement({ actions: ['secretsmanager:RotateSecret'], resources: [appSecret.secretArn] }));
    role.addToPolicy(new iam.PolicyStatement({ actions: ['cloudwatch:GetMetricData'], resources: ['*'] }));
    // per-second OS metrics of the writer (Enhanced Monitoring)
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['logs:GetLogEvents', 'logs:FilterLogEvents'],
      resources: [`arn:aws:logs:${this.region}:${this.account}:log-group:RDSOSMetrics:*`],
    }));
    // lab/netenv.sh maps the proxy endpoint IPs to AZs for --proxy-az=same|cross
    role.addToPolicy(new iam.PolicyStatement({ actions: ['ec2:DescribeNetworkInterfaces'], resources: ['*'] }));
    // Experiments with a `proxy` block change Proxy settings (IdleClientTimeout, pool percentages, DebugLogging) and restore them.
    // ModifyDBProxy is scoped to THIS proxy. ModifyDBProxyTargetGroup could NOT be scoped to this proxy's target group: the target group
    // ARN (arn:...:target-group:prx-tg-<random>) is not available at deploy time. Reading it with GetAtt(TargetGroupArn) made the
    // stack fail to create ("Value null at 'dBProxyName'", found by the smoke test), so that action stays on target-group:* of this
    // account and region. docs/infra-decisions.md lists which permission each lab step uses.
    role.addToPolicy(new iam.PolicyStatement({ actions: ['rds:ModifyDBProxy'], resources: [proxy.dbProxyArn] }));
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['rds:ModifyDBProxyTargetGroup'],
      resources: [proxy.dbProxyArn, `arn:aws:rds:${this.region}:${this.account}:target-group:*`],
    }));
    // Describe* are read-only. Resource-level scoping of the Proxy describe actions was not verified, so they stay on "*".
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['rds:DescribeDBProxies', 'rds:DescribeDBProxyTargetGroups'],
      resources: ['*'],
    }));
    // campaigns resize the writer between specs (2 vCPU -> 4 vCPU); only THIS writer. CloudFormation sees the new class as drift,
    // which is harmless for destroy.
    const writerArn = `arn:aws:rds:${this.region}:${this.account}:db:${cluster.instanceIdentifiers[0]}`;
    role.addToPolicy(new iam.PolicyStatement({ actions: ['rds:ModifyDBInstance', 'rds:DescribeDBInstances'], resources: [writerArn] }));
    // pat-reboot restarts the writer under load (a stand-in for a failover: this cluster has a single instance). Only this writer.
    role.addToPolicy(new iam.PolicyStatement({ actions: ['rds:RebootDBInstance'], resources: [writerArn] }));

    const CLIENT_ID = 'ExperimentClient'; // stable: lab source changes must not replace the instance (and its raw results)
    const userData = ec2.UserData.forLinux();
    userData.addCommands(
      // fail loudly: any failing command sends FAILURE to CloudFormation at once (instead of a 20-minute timeout) and the full
      // output is kept in /var/log/lab-bootstrap.log (also /var/log/cloud-init-output.log)
      'set -Eeuo pipefail',
      'exec > >(tee -a /var/log/lab-bootstrap.log) 2>&1',
      `signal() { aws cloudformation signal-resource --stack-name ${this.stackName} --logical-resource-id ${CLIENT_ID} --unique-id bootstrap --status "$1" --region ${this.region} || true; }`,
      `trap 'rc=$?; echo "bootstrap FAILED (exit $rc) at line $LINENO: $BASH_COMMAND"; signal FAILURE; exit $rc' ERR`,
      'dnf install -y golang mariadb105 jq unzip',
      // connection settings for run.sh / manual use. The infrastructure conditions are recorded for every run (exp.py meta.json).
      `cat >/etc/exp.env <<'ENVEOF'`,
      `export AWS_REGION=${this.region}`,
      `export AWS_DEFAULT_REGION=${this.region}`,
      `export SECRET_ARN=${secret.secretArn}`,
      `export APP_SECRET_ARN=${appSecret.secretArn}`,
      `export PROXY_ENDPOINT=${proxy.endpoint}`,
      `export CLUSTER_ENDPOINT=${cluster.clusterEndpoint.hostname}`,
      `export DB_NAME=${DB_NAME}`,
      `export WRITER_INSTANCE_ID=${cluster.instanceIdentifiers[0]}`,
      `export DB_CLUSTER_ID=${cluster.clusterIdentifier}`,
      `export PROXY_NAME=${PROXY_NAME}`,
      `export ENV_ID=${envId}`,
      `export REQUIRE_TLS=${requireTls}`,
      `export PROXY_AUTH=${proxyAuth}`,
      `export PROXY_DEBUG_LOGGING_AT_DEPLOY=${debugLogging}`,
      'ENVEOF',
      // Create the DB user whose secret gets rotated. Passwords go into 0600 temp files read by the mysql client, never on a command
      // line (visible in ps) and never into the logs; the files are removed on exit.
      'source /etc/exp.env',
      'umask 077',
      'CNF=$(mktemp); SQLF=$(mktemp)',
      `trap 'rm -f "$CNF" "$SQLF"' EXIT`,
      'ADMIN_PW=$(aws secretsmanager get-secret-value --secret-id "$SECRET_ARN" --query SecretString --output text | jq -r .password)',
      'APP_PW=$(aws secretsmanager get-secret-value --secret-id "$APP_SECRET_ARN" --query SecretString --output text | jq -r .password)',
      'printf "[client]\\nuser=admin\\npassword=%s\\n" "$ADMIN_PW" > "$CNF"',
      `printf "CREATE USER IF NOT EXISTS 'appuser'@'%%' IDENTIFIED BY '%s';\\nGRANT ALL ON ${DB_NAME}.* TO 'appuser'@'%%';\\n" "$APP_PW" > "$SQLF"`,
      'unset ADMIN_PW APP_PW',
      'created=false; for i in $(seq 1 30); do if mysql --defaults-extra-file="$CNF" -h "$CLUSTER_ENDPOINT" < "$SQLF"; then created=true; break; fi; sleep 10; done; "$created"',
      'rm -f "$CNF" "$SQLF"',
      // The lab is delivered after the stack is up: `make deploy` runs `make push` (go.mod pins the Go version; the distribution's Go
      // downloads that toolchain itself, GOTOOLCHAIN=auto).
      'install -d -o ec2-user -g ec2-user /home/ec2-user/lab',
      'signal SUCCESS',
    );

    const client = new ec2.Instance(this, 'Client', {
      vpc,
      // Public subnet + public IP: the instance needs the internet for dnf, the Go toolchain and Go modules; SSM is the only way in
      // (no inbound rules). Alternatives (NAT / VPC endpoints, or a prebuilt binary) are compared in docs/infra-decisions.md.
      vpcSubnets: { subnetType: ec2.SubnetType.PUBLIC },
      associatePublicIpAddress: true,
      // load generator: 4 vCPU non-burstable so it does not become the bottleneck of the capacity runs (override: -c clientClass=t4g.micro)
      instanceType: new ec2.InstanceType(String(ctx('clientClass') ?? 'c7g.xlarge')),
      machineImage: ec2.MachineImage.latestAmazonLinux2023({ cpuType: ec2.AmazonLinuxCpuType.ARM_64 }),
      securityGroup: clientSg,
      role,
      userData,
      requireImdsv2: true,
      // Explicit: the CDK defaults are encrypted=false (unless the account enforces it) and, for EBS, deleteOnTermination=false (a
      // volume with the raw results and credentials would outlive the stack). Fetch the results BEFORE destroying.
      blockDevices: [{ deviceName: '/dev/xvda', volume: ec2.BlockDeviceVolume.ebs(16, { encrypted: true, deleteOnTermination: true }) }],
    });
    client.node.addDependency(proxy); // endpoint must exist first (also implied by token use)

    const cfnClient = client.node.defaultChild as ec2.CfnInstance;
    cfnClient.overrideLogicalId(CLIENT_ID);
    cfnClient.cfnOptions.creationPolicy = { resourceSignal: { count: 1, timeout: 'PT20M' } };
    role.addToPolicy(new iam.PolicyStatement({ actions: ['cloudformation:SignalResource'], resources: [this.stackId] }));
    const rotation = new sm.SecretRotation(this, 'AppRotation', {
      application: sm.SecretRotationApplication.MYSQL_ROTATION_SINGLE_USER,
      secret: appSecret,
      target: cluster,
      vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_ISOLATED },
      automaticallyAfter: cdk.Duration.days(365),
      rotateImmediatelyOnUpdate: false,
    });
    rotation.node.addDependency(client);

    // ---------------------------------------------------------------- CloudWatch dashboard
    const proxyDim = { ProxyName: PROXY_NAME };
    const m = (name: string, label: string, color?: string) =>
      new cw.Metric({ namespace: 'AWS/RDS', metricName: name, dimensionsMap: proxyDim, statistic: 'Sum', period: cdk.Duration.minutes(1), label, color });
    const writerId = cluster.instanceIdentifiers[0];

    const dashboard = new cw.Dashboard(this, 'Dashboard', { dashboardName: `rds-proxy-pinning-${envId}`, defaultInterval: cdk.Duration.hours(3) });
    dashboard.addWidgets(
      new cw.GraphWidget({
        title: 'Entrance (ClientConnections) vs exit (DatabaseConnections)',
        width: 24,
        height: 9,
        left: [
          m('ClientConnections', 'Proxy ClientConnections (entrance)'),
          m('DatabaseConnections', 'Proxy DatabaseConnections (exit)'),
          m('DatabaseConnectionsCurrentlyBorrowed', 'Proxy borrowed'),
          m('MaxDatabaseConnectionsAllowed', 'Proxy MaxDatabaseConnectionsAllowed (measured limit)'),
          m('DatabaseConnectionsCurrentlySessionPinned', 'Proxy ...CurrentlySessionPinned'),
          m('DatabaseConnectionsCurrentlyInTransaction', 'Proxy ...CurrentlyInTransaction'),
          new cw.Metric({
            namespace: 'AWS/RDS',
            metricName: 'DatabaseConnections',
            dimensionsMap: { DBInstanceIdentifier: writerId },
            statistic: 'Maximum',
            period: cdk.Duration.minutes(1),
            label: 'Aurora DatabaseConnections',
          }),
        ],
        leftAnnotations: [
          // Theoretical values from the settings. The Proxy reserves some connections for its own management, so the measured limit
          // (MaxDatabaseConnectionsAllowed above) can be lower than 80; and MaxIdleConnectionsPercent is an upper bound on idle
          // connections the Proxy keeps, not a number it maintains.
          { value: PROXY_MAX_CONNECTIONS_PERCENT, label: `MaxConnectionsPercent=${PROXY_MAX_CONNECTIONS_PERCENT}% of max_connections (theoretical ${PROXY_MAX_CONNECTIONS_PERCENT}; see measured limit)` },
          { value: PROXY_MAX_IDLE_CONNECTIONS_PERCENT, label: `MaxIdleConnectionsPercent=${PROXY_MAX_IDLE_CONNECTIONS_PERCENT}% (upper bound of idle connections kept)` },
        ],
        leftYAxis: { min: 0 },
      }),
    );

    dashboard.addWidgets(new cw.GraphWidget({
      title: 'Connections created or closed per minute',
      width: 24,
      left: [
        m('DatabaseConnectionsSetupSucceeded', 'DB connections established'),
        m('ClientConnectionsReceived', 'Client connections received'),
        m('ClientConnectionsClosed', 'Client connections closed'),
      ],
    }));

    // ---------------------------------------------------------------- Alarms (the "early warning" part of the fix)
    // No actions: the report checks `describe-alarm-history` to see whether/when they would have fired.
    new cw.Alarm(this, 'DbConnHigh', {
      alarmName: `${PROXY_NAME}-dbconn-high`,
      alarmDescription: 'Proxy DatabaseConnections >= 56 (fixed experiment threshold)',
      metric: m('DatabaseConnections', 'DatabaseConnections'),
      threshold: 56,
      evaluationPeriods: 1,
      comparisonOperator: cw.ComparisonOperator.GREATER_THAN_OR_EQUAL_TO_THRESHOLD,
      treatMissingData: cw.TreatMissingData.NOT_BREACHING,
    });
    new cw.Alarm(this, 'PinnedHigh', {
      alarmName: `${PROXY_NAME}-pinned-high`,
      alarmDescription: 'Session-pinned backend connections >= 20 (a quarter of the Proxy limit)',
      metric: m('DatabaseConnectionsCurrentlySessionPinned', 'SessionPinned'),
      threshold: 20,
      evaluationPeriods: 1,
      comparisonOperator: cw.ComparisonOperator.GREATER_THAN_OR_EQUAL_TO_THRESHOLD,
      treatMissingData: cw.TreatMissingData.NOT_BREACHING,
    });

    // ---------------------------------------------------------------- Cost guard (notification only: never deletes anything)
    // Failed campaigns are kept on purpose (evidence), so a forgotten stack keeps billing. This warns; deleting stays a human decision.
    // The budget covers the whole account's daily cost. The address is passed at deploy time and is not stored in the repository.
    if (ctx('budgetEmail')) {
      new budgets.CfnBudget(this, 'DailyCostBudget', {
        budget: {
          budgetName: `${this.stackName}-daily`,
          budgetType: 'COST',
          timeUnit: 'DAILY',
          budgetLimit: { amount: Number(ctx('budgetUsdDaily') ?? 20), unit: 'USD' },
        },
        notificationsWithSubscribers: [{
          notification: { notificationType: 'ACTUAL', comparisonOperator: 'GREATER_THAN', threshold: 80, thresholdType: 'PERCENTAGE' },
          subscribers: [{ subscriptionType: 'EMAIL', address: String(ctx('budgetEmail')) }],
        }],
      });
    }

    // ---------------------------------------------------------------- Outputs
    new cdk.CfnOutput(this, 'InstanceId', { value: client.instanceId, description: 'aws ssm start-session --target <this>' });
    new cdk.CfnOutput(this, 'WriterInstanceId', { value: writerId });
    new cdk.CfnOutput(this, 'ProxyEndpoint', { value: proxy.endpoint });
    new cdk.CfnOutput(this, 'ProxyName', { value: PROXY_NAME });
    new cdk.CfnOutput(this, 'EnvId', { value: envId });
    new cdk.CfnOutput(this, 'RequireTls', { value: String(requireTls) });
    new cdk.CfnOutput(this, 'ProxyAuth', { value: proxyAuth });
    new cdk.CfnOutput(this, 'DebugLoggingAtDeploy', { value: String(debugLogging) });
    new cdk.CfnOutput(this, 'ClusterEndpoint', { value: cluster.clusterEndpoint.hostname });
    new cdk.CfnOutput(this, 'SecretArn', { value: secret.secretArn });
    new cdk.CfnOutput(this, 'AppSecretArn', { value: appSecret.secretArn });
    new cdk.CfnOutput(this, 'ProxyLogGroup', { value: proxyLogGroup.logGroupName });
    new cdk.CfnOutput(this, 'DashboardName', { value: dashboard.dashboardName });
  }
}
