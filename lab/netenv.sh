#!/usr/bin/env bash
# Writes net.env with PROXY_IP_SAME / PROXY_IP_CROSS (the RDS Proxy endpoint IP in this instance's AZ / in another AZ),
# needed by `--proxy-az=same|cross`. Sourced by run.sh; creates the file on first use. Needs ec2:DescribeNetworkInterfaces.
set -euo pipefail
cd "$(dirname "$0")"
[ -f net.env ] && exit 0
source /etc/exp.env
tok=$(curl -sS -X PUT http://169.254.169.254/latest/api/token -H 'X-aws-ec2-metadata-token-ttl-seconds: 60')
my_az=$(curl -sS -H "X-aws-ec2-metadata-token: $tok" http://169.254.169.254/latest/meta-data/placement/availability-zone)
same=""; cross=""
for ip in $(getent ahostsv4 "$PROXY_ENDPOINT" | awk '{print $1}' | sort -u); do
  az=$(aws ec2 describe-network-interfaces --filters "Name=addresses.private-ip-address,Values=$ip" \
        --query 'NetworkInterfaces[0].AvailabilityZone' --output text)
  if [ "$az" = "$my_az" ]; then same=$ip; else cross=$ip; fi
done
[ -n "$same" ] && [ -n "$cross" ] || { echo "could not find proxy IPs in two AZs (same='$same' cross='$cross')" >&2; exit 1; }
printf 'export PROXY_IP_SAME=%s\nexport PROXY_IP_CROSS=%s\n' "$same" "$cross" > net.env
