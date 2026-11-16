#!/usr/bin/env bash
# Shared helpers. Source me.  Needs: aws cli v2, jq. Region/credentials come from the environment.
STACK=RdsProxyPinningExperiment
out() { aws cloudformation describe-stacks --stack-name "$STACK" --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text; }
