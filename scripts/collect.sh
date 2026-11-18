#!/usr/bin/env bash
# Collect one UTC window. Empty series remain missing, including during cooldown.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
name=$1; start=$2; end=$3
writer=$(out WriterInstanceId)
[[ -n $writer && $writer != None ]] || { echo "stack has no WriterInstanceId output" >&2; exit 1; }
queries=$(jq -nc --arg writer "$writer" '
  def metric($id;$name;$dim;$value;$stat):
    {Id:$id,MetricStat:{Metric:{Namespace:"AWS/RDS",MetricName:$name,Dimensions:[{Name:$dim,Value:$value}]},Period:60,Stat:$stat},ReturnData:true};
  def proxy($id;$name): metric($id;$name;"ProxyName";"rdsproxy-exp";"Sum");
  [proxy("client";"ClientConnections"),proxy("dbconn";"DatabaseConnections"),
   proxy("clientauthfailed";"ClientConnectionsSetupFailedAuth"),proxy("dbsetupfailed";"DatabaseConnectionsSetupFailed"),
   proxy("created";"DatabaseConnectionsSetupSucceeded"),proxy("received";"ClientConnectionsReceived"),
   proxy("closed";"ClientConnectionsClosed"),proxy("borrowed";"DatabaseConnectionsCurrentlyBorrowed"),proxy("allowed";"MaxDatabaseConnectionsAllowed"),
   proxy("pinned";"DatabaseConnectionsCurrentlySessionPinned"),proxy("intx";"DatabaseConnectionsCurrentlyInTransaction"),
   metric("borrowlatency";"DatabaseConnectionsBorrowLatency";"ProxyName";"rdsproxy-exp";"Average"),
   metric("aurora";"DatabaseConnections";"DBInstanceIdentifier";$writer;"Maximum")]')
mkdir -p results
aws cloudwatch get-metric-data --start-time "$start" --end-time "$end" --scan-by TimestampDescending \
  --metric-data-queries "$queries" > "results/$name.json"
jq -r --arg n "$name" '.MetricDataResults[] | [$n,.Id,.StatusCode,(.Values|max // "missing"),(.Values|first // "missing"),(.Values|length)] | @tsv' "results/$name.json"
jq -e '(.MetricDataResults | length == 13) and all(.MetricDataResults[]; .StatusCode == "Complete") and
  all(.MetricDataResults[] | select(.Id == "client" or .Id == "dbconn" or .Id == "aurora" or .Id == "allowed"); (.Values|length)>0)' \
  "results/$name.json" >/dev/null || { echo "incomplete evidence; check metric delivery and re-collect" >&2; exit 1; }
