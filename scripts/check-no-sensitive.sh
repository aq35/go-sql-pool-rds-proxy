#!/usr/bin/env bash
# Fails if a tracked file contains something that must never be published: AWS access keys, private keys, account-scoped ARNs,
# RDS / Proxy endpoint host names, or private IPs. (Raw run logs, which contain endpoints, are git-ignored.)
# A 12-digit number is only flagged inside an ARN. Run in CI on every PR; also usable locally.
set -euo pipefail
cd "$(dirname "$0")/.."
pat='AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|arn:aws[a-z-]*:[a-z0-9-]+:[a-z0-9-]*:[0-9]{12}:|[a-z0-9-]+\.(cluster-|proxy-)[a-z0-9]+\.[a-z0-9-]+\.rds\.amazonaws\.com|\b10\.0\.[0-9]+\.[0-9]+\b|aws_secret_access_key'
# this script and the docs that quote the patterns are excluded
if hits=$(git ls-files -z | xargs -0 grep -InE "$pat" -- 2>/dev/null | grep -vE '^(scripts/check-no-sensitive\.sh|infra/package-lock\.json):'); then
  echo "Sensitive-looking content found in tracked files:" >&2
  echo "$hits" >&2
  exit 1
fi
echo "no sensitive content found in $(git ls-files | wc -l | tr -d ' ') tracked files"
