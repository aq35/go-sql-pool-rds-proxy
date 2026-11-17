#!/usr/bin/env bash
# Operate experiments on the EC2 instance through SSM.  Usage (normally via make):
#   lab.sh start  EXP [REPEAT]    start detached (survives the SSM call and your laptop)
#   lab.sh status EXP
#   lab.sh fetch  EXP             download results/EXP__<aurora class>/ and write summary.md for each
#   lab.sh campaign NAME          start a campaign (lab/campaigns/NAME.json) detached on the instance
#   lab.sh campaign-status NAME
#   lab.sh resize CLASS           change the writer's instance class now (e.g. db.r6g.xlarge) and wait until available
#   lab.sh push                   copy the current lab/ to the instance, build and check it in a new directory, then swap it in (atomic; refuses while an experiment runs)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); root=$(dirname "$here")
cmd=${1:?start|status|fetch|push}; exp=${2:-}
ssm() { "$here/ssm-run.sh" "$1"; }
case $cmd in
  start)
    rep=${3:-}; [ -n "$rep" ] && rep="--repeat $rep"
    ssm "cd ~/lab && mkdir -p out && (setsid nohup ./exp.py run $exp $rep > out/$exp.log 2>&1 < /dev/null &) ; sleep 2; head -3 out/$exp.log" ;;
  status)
    ssm "cd ~/lab && ./exp.py status $exp; tail -4 out/$exp.log" ;;
  fetch)
    ssm "cd ~/lab && ./exp.py pack $exp" >/dev/null
    # LAB_RESULTS=results/<name> keeps a campaign apart (e.g. the smoke test), because results are stored by experiment name.
    res="$root/${LAB_RESULTS:-results}"; mkdir -p "$res"
    tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
    "$here/ssm-run.sh" --file "lab/out/$exp.tgz" | tar xz -C "$tmp"
    # Never overwrite evidence of a DIFFERENT run (same experiment and run name, another campaign or stack). Fetching the same run
    # again is fine (identical meta.json).
    for rd in "$tmp"/*/*.r[0-9]*/; do
      rel=${rd#"$tmp"/}
      if [ -f "$res/$rel/meta.json" ] && ! cmp -s "$rd/meta.json" "$res/$rel/meta.json"; then
        echo "refusing to overwrite $res/$rel: it holds a different run. Fetch into another directory: LAB_RESULTS=results/<name> make finish ..." >&2; exit 1
      fi
    done
    cp -R "$tmp"/. "$res"/
    for d in "$res/${exp}__"*/; do python3 "$here/summarize.py" "${d%/}" || exit 1; done ;;
  campaign)
    ssm "cd ~/lab && mkdir -p out && (setsid nohup ./exp.py campaign $exp > out/campaign-$exp.log 2>&1 < /dev/null &) ; sleep 2; head -3 out/campaign-$exp.log" ;;
  campaign-status)
    ssm "cd ~/lab && ./exp.py campaign-status $exp; tail -3 out/campaign-$exp.log" ;;
  resize)
    ssm "source /etc/exp.env && aws rds modify-db-instance --db-instance-identifier \$WRITER_INSTANCE_ID --db-instance-class $exp --apply-immediately --query DBInstance.DBInstanceStatus --output text" ;;
  push)
    git -C "$root" rev-parse --short HEAD > "$root/lab/VERSION"
    # xz: the SSM command parameter is limited to ~97k characters; gzip needed ~95k for lab/ (too close), xz about 70k
    b64=$(tar cf - -C "$root" --exclude=lab/lab --exclude=lab/lab-v181 --exclude=lab/out --exclude=lab/net.env lab | xz -9 | base64 | tr -d '\n')
    [ ${#b64} -lt 90000 ] || { echo "lab/ is ${#b64} chars base64; the SSM limit is ~97k. Ship lab/ through S3 instead of SSM." >&2; exit 1; }
    # Atomic: unpack + build + `exp.py check` in ~/lab.new; only if all succeed, swap it in (the old tree stays as ~/lab.old).
    # Refuses to run while an experiment holds ~/lab/out/.lock. out/ (results) and net.env move to the new tree.
    ssm "set -e; cd ~; if [ -e lab/out/.lock ] && ! flock -n lab/out/.lock true; then echo 'an experiment is running; not pushing' >&2; exit 1; fi; rm -rf lab.new && mkdir lab.new && echo $b64 | base64 -d | tar xJ -C lab.new --strip-components=1 && cd lab.new && chmod +x *.sh exp.py && export GOTOOLCHAIN=auto GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct && go build -o lab . && go build -modfile=go.v181.mod -o lab-v181 . && python3 -I exp.py check && cd ~ && if [ -d lab/out ]; then mv lab/out lab.new/out; fi; if [ -f lab/net.env ]; then cp lab/net.env lab.new/net.env; fi; rm -rf lab.old; if [ -d lab ]; then mv lab lab.old; fi; mv lab.new lab; echo PUSHED git=\$(cat lab/VERSION) bin=\$(sha256sum lab/lab | cut -c1-12)" ;;
  *) echo "unknown command $cmd" >&2; exit 2 ;;
esac
