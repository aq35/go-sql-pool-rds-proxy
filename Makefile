# Everything an agent (or you) needs, in one place.  `make help`
EXP ?=
REPEAT ?=

CAMPAIGN ?= day1
DESTROY_AFTER ?=
CLASS ?=

.PHONY: help check deploy start status fetch push destroy campaign campaign-status finish resize derive validation-gen rules
help:
	@echo "make check                 vet/build the Go client, validate experiments, shell/python syntax, tsc"
	@echo "make deploy                cdk deploy (ships lab/ to the EC2 instance, builds it there)"
	@echo "make push                  copy the current lab/ to the instance without redeploying"
	@echo "make start EXP=<name> [REPEAT=3]   run an experiment detached on the instance (lab/experiments/<name>.json)"
	@echo "make status EXP=<name>     progress of a running experiment"
	@echo "make fetch EXP=<name>      download results/<name>/ and write summary.md"
	@echo "make campaign [CAMPAIGN=day1]     run a whole campaign (Aurora spec phases + experiments, ~10 h) detached on the instance"
	@echo "make campaign-status [CAMPAIGN=day1]"
	@echo "make finish [CAMPAIGN=day1] [DESTROY_AFTER=yes]   fetch + summarize everything; destroy only if DESTROY_AFTER=yes and every check passes"
	@echo "make resize CLASS=db.r6g.xlarge    change the writer's instance class now"
	@echo "make derive                coefficients (k, alpha, beta, gamma, delta) from results/ -> lab/coeffs.json (no AI)"
	@echo "make validation-gen        lab/experiments/rule-validation.json + campaign day2 from the coefficients (then: make push; make campaign CAMPAIGN=day2)"
	@echo "make rules ARGS='--vcpu 2 --max-connections 1000 --containers 3 --workers 10 --active-avg 4'   settings from the rules"
	@echo "make destroy CONFIRM=yes   cdk destroy (only when the user asked for it)"
	@echo; cd lab && python3 exp.py list

check:
	cd lab && gofmt -l . | (! grep .) && go vet ./... && go test ./... >/dev/null && go build -o lab . && python3 -I exp.py check && PYTHONDONTWRITEBYTECODE=1 python3 test_rules.py && PYTHONDONTWRITEBYTECODE=1 python3 -m unittest -q test_exp; r=$$?; rm -f lab; exit $$r
	for f in lab/*.sh scripts/*.sh; do bash -n $$f || exit 1; done
	scripts/check-no-sensitive.sh
	python3 -I -m py_compile lab/exp.py lab/rules.py scripts/summarize.py scripts/finish.py scripts/derive_coeffs.py scripts/gen_validation.py; rm -rf lab/__pycache__ scripts/__pycache__
	cd infra && (test -d node_modules || npm ci --silent) && npx tsc --noEmit -p .
	@echo "check OK (cdk synth needs AWS credentials: cd infra && npx cdk synth --quiet)"

deploy:
	git rev-parse --short HEAD > lab/VERSION
	cd infra && (test -d node_modules || npm ci --silent) && npx cdk deploy --require-approval never

push:
	scripts/lab.sh push

start:
	@test -n "$(EXP)" || (echo "EXP=<name> required"; exit 2)
	scripts/lab.sh start $(EXP) $(REPEAT)

status:
	@test -n "$(EXP)" || (echo "EXP=<name> required"; exit 2)
	scripts/lab.sh status $(EXP)

fetch:
	@test -n "$(EXP)" || (echo "EXP=<name> required"; exit 2)
	scripts/lab.sh fetch $(EXP)

destroy:
	@test "$(CONFIRM)" = yes || (echo "refusing: run 'make destroy CONFIRM=yes' only when the user asked to destroy"; exit 2)
	cd infra && npx cdk destroy --force

campaign:
	scripts/lab.sh campaign $(CAMPAIGN)

campaign-status:
	scripts/lab.sh campaign-status $(CAMPAIGN)

finish:
	scripts/finish.py $(CAMPAIGN) $(if $(filter yes,$(DESTROY_AFTER)),--destroy,)

resize:
	@test -n "$(CLASS)" || (echo "CLASS=db.<family>.<size> required"; exit 2)
	scripts/lab.sh resize $(CLASS)

derive:
	python3 scripts/derive_coeffs.py

validation-gen:
	python3 scripts/gen_validation.py

rules:
	python3 lab/rules.py $(ARGS)
