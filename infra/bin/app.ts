#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib';
import { ProxyExperimentStack } from '../lib/proxy-experiment-stack';

const app = new cdk.App();
new ProxyExperimentStack(app, 'RdsProxyPinningExperiment', {
  env: {
    account: process.env.CDK_DEFAULT_ACCOUNT,
    region: process.env.CDK_DEFAULT_REGION,
  },
  description: 'Experiment: why does RDS Proxy DatabaseConnections stick at MaxConnectionsPercent with few clients? (1/10 scale)',
});
