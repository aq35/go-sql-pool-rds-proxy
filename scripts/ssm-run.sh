#!/usr/bin/env bash
# Run a command as ec2-user, or stream an entire completed evidence file with --file PATH.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
iid=$(out InstanceId)
run() {
  local cmd id st result i
  cmd=$(jq -cn --arg c "sudo -u ec2-user -H bash -lc $(printf '%q' "$1")" '[$c]')
  id=$(aws ssm send-command --instance-ids "$iid" --document-name AWS-RunShellScript \
    --parameters "commands=$cmd" --timeout-seconds 3600 --query Command.CommandId --output text)
  for ((i=0; i<720; i++)); do
    if result=$(aws ssm get-command-invocation --command-id "$id" --instance-id "$iid" --output json 2>/dev/null); then
      st=$(jq -r .Status <<<"$result")
      case "$st" in
        Success) jq -jr .StandardOutputContent <<<"$result"; return ;;
        Failed|Cancelled|TimedOut|Cancelling)
          jq -r '.Status, .StandardErrorContent' <<<"$result" >&2; return 1 ;;
      esac
    fi
    sleep 5
  done
  echo "SSM command $id did not finish" >&2; return 1
}
if [[ $1 != --file ]]; then run "$1"; exit; fi
# SSM caps stdout at 24,000 characters. Retrieve chunks under the cap and verify the byte stream.
file=$(printf '%q' "$2")
meta=$(run "cd && wc -c < $file && sha256sum < $file")
size=$(awk 'NR==1 {print $1}' <<<"$meta")
digest=$(awk 'NR==2 {print $1}' <<<"$meta")
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
for ((block=0; block*15000<size; block++)); do
  run "cd && set -o pipefail && dd if=$file bs=15000 skip=$block count=1 status=none | base64 -w0" \
    | python3 -c 'import sys,base64; sys.stdout.buffer.write(base64.b64decode(sys.stdin.buffer.read(), validate=True))' >> "$tmp"
done
[[ $(shasum -a 256 "$tmp" | cut -d' ' -f1) == "$digest" ]] || { echo "file changed or incomplete: $2" >&2; exit 1; }
cat "$tmp"
