#!/usr/bin/env bash
# Usage: ./run.sh --scenario=pool-load [--duration=10m ...]
#   SECRET=admin (default) | app
#   TARGET=proxy (default) | cluster   -> which endpoint the client connects to
set -euo pipefail
source /etc/exp.env
ADMIN_ARN="$SECRET_ARN"
ADMIN_JSON=$(aws secretsmanager get-secret-value --secret-id "$ADMIN_ARN" --query SecretString --output text)
ADMIN_USER=$(jq -r .username <<<"$ADMIN_JSON")
ADMIN_PASS=$(jq -r .password <<<"$ADMIN_JSON")
export ADMIN_USER ADMIN_PASS
export DB_DIRECT_HOST="$CLUSTER_ENDPOINT"     # used by lock-mode=direct and the Aurora sampler
# SECRET=admin (default) | app  (app = the rotated `appuser` secret; the client then re-reads it on every pool rotation)
if [ "${SECRET:-admin}" = "app" ]; then export SECRET_ARN="$APP_SECRET_ARN"; fi
SECRET_JSON=$(aws secretsmanager get-secret-value --secret-id "$SECRET_ARN" --query SecretString --output text)
DB_USER=$(jq -r .username <<<"$SECRET_JSON")
DB_PASS=$(jq -r .password <<<"$SECRET_JSON")
export DB_USER DB_PASS
export DB_PORT=3306
case "${TARGET:-proxy}" in
  proxy)   export DB_HOST="$PROXY_ENDPOINT" ;;
  cluster) export DB_HOST="$CLUSTER_ENDPOINT" ;;
  *) echo "TARGET must be proxy|cluster" >&2; exit 2 ;;
esac
cd "$(dirname "$0")"
if [[ " $* " == *" --proxy-az="* ]]; then ./netenv.sh; source net.env; fi   # PROXY_IP_SAME / PROXY_IP_CROSS for --proxy-az
exec ./lab "$@"
