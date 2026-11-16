import * as path from 'path';
import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as rds from 'aws-cdk-lib/aws-rds';
import * as sm from 'aws-cdk-lib/aws-secretsmanager';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as cw from 'aws-cdk-lib/aws-cloudwatch';
import { Asset } from 'aws-cdk-lib/aws-s3-assets';

/** Production is Aurora max_connections=1000 / Proxy 80% / idle 10%.  This stack is the 1/10 scale model. */
const AURORA_MAX_CONNECTIONS = 100;
const PROXY_MAX_CONNECTIONS_PERCENT = 80; // -> 80 backend connections (prod: 800)
const PROXY_MAX_IDLE_CONNECTIONS_PERCENT = 10; // -> 10 (prod: 100)
const PROXY_NAME = 'rdsproxy-exp';
const DB_NAME = 'exp';

export class ProxyExperimentStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

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
    // ---------------------------------------------------------------- RDS Proxy (debug logging on)
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
      debugLogging: true,
      iamAuth: false,
      requireTLS: false, // keeps the Go client simple (password auth, plain TCP inside the VPC)
      // DB users (admin/appuser) are mysql_native_password; the CDK default (caching_sha2) mismatches
      // what the Go client's auth-plugin negotiation offers, causing "Invalid credentials" at the proxy.
      clientPasswordAuthType: rds.ClientPasswordAuthType.MYSQL_NATIVE_PASSWORD,
      borrowTimeout: cdk.Duration.seconds(30),
    });
    proxy.node.addDependency(proxyLogGroup);
    proxy.applyRemovalPolicy(cdk.RemovalPolicy.DESTROY);

    // ---------------------------------------------------------------- EC2 client (SSM only, no SSH)
    const clientSrc = new Asset(this, 'ClientSource', { path: path.join(__dirname, '..', '..', 'lab'), exclude: ['lab', 'out', 'net.env', 'go.sum.bak'] });

    const role = new iam.Role(this, 'ClientRole', {
      assumedBy: new iam.ServicePrincipal('ec2.amazonaws.com'),
      managedPolicies: [iam.ManagedPolicy.fromAwsManagedPolicyName('AmazonSSMManagedInstanceCore')],
    });
    secret.grantRead(role);
    appSecret.grantRead(role);
    role.addToPolicy(new iam.PolicyStatement({ actions: ['secretsmanager:RotateSecret'], resources: [appSecret.secretArn] }));
    clientSrc.grantRead(role);
    role.addToPolicy(new iam.PolicyStatement({ actions: ['cloudwatch:GetMetricData'], resources: ['*'] }));
    // per-second OS metrics of the writer (Enhanced Monitoring)
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['logs:GetLogEvents', 'logs:FilterLogEvents'],
      resources: [`arn:aws:logs:${this.region}:${this.account}:log-group:RDSOSMetrics:*`],
    }));
    // lab/netenv.sh maps the proxy endpoint IPs to AZs for --proxy-az=same|cross
    role.addToPolicy(new iam.PolicyStatement({ actions: ['ec2:DescribeNetworkInterfaces'], resources: ['*'] }));
    // infra-knob experiments (archive/retention-cohort/infra-exp.sh) change Proxy settings mid-run and restore them
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['rds:ModifyDBProxy', 'rds:ModifyDBProxyTargetGroup'],
      resources: [proxy.dbProxyArn, `arn:aws:rds:${this.region}:${this.account}:target-group:*`],
    }));
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['rds:DescribeDBProxies', 'rds:DescribeDBProxyTargetGroups', 'rds:DescribeDBInstances'],
      resources: ['*'],
    }));
    // campaigns resize the writer between specs (2 vCPU -> 4 vCPU); CloudFormation sees this as drift, which is harmless for destroy
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['rds:ModifyDBInstance'],
      resources: [`arn:aws:rds:${this.region}:${this.account}:db:*`],
    }));

    const userData = ec2.UserData.forLinux();
    userData.addCommands(
      'set -euo pipefail',
      'dnf install -y golang mariadb105 jq unzip',
      // connection settings for run.sh / manual use
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
      'ENVEOF',
      // create the DB user whose secret gets rotated (admin creds from the master secret)
      'source /etc/exp.env',
      'ADMIN_PW=$(aws secretsmanager get-secret-value --secret-id $SECRET_ARN --query SecretString --output text | jq -r .password)',
      'APP_PW=$(aws secretsmanager get-secret-value --secret-id $APP_SECRET_ARN --query SecretString --output text | jq -r .password)',
      'created=false; for i in $(seq 1 30); do mysql -h $CLUSTER_ENDPOINT -u admin -p"$ADMIN_PW" -e "CREATE USER IF NOT EXISTS \'appuser\'@\'%\' IDENTIFIED BY \'$APP_PW\'; GRANT ALL ON ${DB_NAME}.* TO \'appuser\'@\'%\';" && { created=true; break; }; sleep 10; done; "$created"',
      // fetch + build the Go client
      'install -d -o ec2-user -g ec2-user /home/ec2-user/lab',
      `aws s3 cp ${clientSrc.s3ObjectUrl} /tmp/lab.zip --region ${this.region}`,
      'unzip -o /tmp/lab.zip -d /home/ec2-user/lab',
      'chown -R ec2-user:ec2-user /home/ec2-user/lab',
      'chmod +x /home/ec2-user/lab/*.sh /home/ec2-user/lab/exp.py',
      `sudo -u ec2-user -H bash -c 'cd /home/ec2-user/lab && export GOPATH=$HOME/go GOCACHE=$HOME/.cache/go-build && go mod tidy && go build -o lab .'`,
    );

    const client = new ec2.Instance(this, 'Client', {
      vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PUBLIC },
      associatePublicIpAddress: true,
      // load generator: 4 vCPU non-burstable so it does not become the bottleneck of the capacity runs (override: -c clientClass=t4g.micro)
      instanceType: new ec2.InstanceType(String(this.node.tryGetContext('clientClass') ?? 'c7g.xlarge')),
      machineImage: ec2.MachineImage.latestAmazonLinux2023({ cpuType: ec2.AmazonLinuxCpuType.ARM_64 }),
      securityGroup: clientSg,
      role,
      userData,
      requireImdsv2: true,
      blockDevices: [{ deviceName: '/dev/xvda', volume: ec2.BlockDeviceVolume.ebs(16) }],
    });
    client.node.addDependency(proxy); // endpoint must exist first (also implied by token use)

    const cfnClient = client.node.defaultChild as ec2.CfnInstance;
    // New client source needs a new bootstrapped instance. Use the same logical ID in its signal.
    const clientLogicalId = `ExperimentClient${clientSrc.assetHash.slice(0, 16)}`;
    cfnClient.overrideLogicalId(clientLogicalId);
    cfnClient.cfnOptions.creationPolicy = { resourceSignal: { count: 1, timeout: 'PT20M' } };
    role.addToPolicy(new iam.PolicyStatement({ actions: ['cloudformation:SignalResource'], resources: [this.stackId] }));
    userData.addCommands(`aws cloudformation signal-resource --stack-name ${this.stackName} --logical-resource-id ${clientLogicalId} --unique-id bootstrap --status SUCCESS --region ${this.region}`);
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

    const dashboard = new cw.Dashboard(this, 'Dashboard', { dashboardName: 'rds-proxy-pinning-experiment', defaultInterval: cdk.Duration.hours(3) });
    dashboard.addWidgets(
      new cw.GraphWidget({
        title: 'Entrance (ClientConnections) vs exit (DatabaseConnections)',
        width: 24,
        height: 9,
        left: [
          m('ClientConnections', 'Proxy ClientConnections (entrance)'),
          m('DatabaseConnections', 'Proxy DatabaseConnections (exit)'),
          m('DatabaseConnectionsCurrentlyBorrowed', 'Proxy borrowed'),
          m('MaxDatabaseConnectionsAllowed', 'Proxy usable limit'),
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
          { value: PROXY_MAX_CONNECTIONS_PERCENT, label: `MaxConnectionsPercent=${PROXY_MAX_CONNECTIONS_PERCENT}% (=${PROXY_MAX_CONNECTIONS_PERCENT})` },
          { value: PROXY_MAX_IDLE_CONNECTIONS_PERCENT, label: `MaxIdleConnectionsPercent=${PROXY_MAX_IDLE_CONNECTIONS_PERCENT}% (=${PROXY_MAX_IDLE_CONNECTIONS_PERCENT})` },
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
      alarmName: 'rdsproxy-exp-dbconn-high',
      alarmDescription: 'Proxy DatabaseConnections >= 56 (fixed experiment threshold)',
      metric: m('DatabaseConnections', 'DatabaseConnections'),
      threshold: 56,
      evaluationPeriods: 1,
      comparisonOperator: cw.ComparisonOperator.GREATER_THAN_OR_EQUAL_TO_THRESHOLD,
      treatMissingData: cw.TreatMissingData.NOT_BREACHING,
    });
    new cw.Alarm(this, 'PinnedHigh', {
      alarmName: 'rdsproxy-exp-pinned-high',
      alarmDescription: 'Session-pinned backend connections >= 20 (a quarter of the Proxy limit)',
      metric: m('DatabaseConnectionsCurrentlySessionPinned', 'SessionPinned'),
      threshold: 20,
      evaluationPeriods: 1,
      comparisonOperator: cw.ComparisonOperator.GREATER_THAN_OR_EQUAL_TO_THRESHOLD,
      treatMissingData: cw.TreatMissingData.NOT_BREACHING,
    });

    // ---------------------------------------------------------------- Outputs
    new cdk.CfnOutput(this, 'InstanceId', { value: client.instanceId, description: 'aws ssm start-session --target <this>' });
    new cdk.CfnOutput(this, 'WriterInstanceId', { value: writerId });
    new cdk.CfnOutput(this, 'ProxyEndpoint', { value: proxy.endpoint });
    new cdk.CfnOutput(this, 'ClusterEndpoint', { value: cluster.clusterEndpoint.hostname });
    new cdk.CfnOutput(this, 'SecretArn', { value: secret.secretArn });
    new cdk.CfnOutput(this, 'AppSecretArn', { value: appSecret.secretArn });
    new cdk.CfnOutput(this, 'ProxyLogGroup', { value: proxyLogGroup.logGroupName });
    new cdk.CfnOutput(this, 'DashboardName', { value: dashboard.dashboardName });
  }
}
