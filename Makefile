# BLP Digital integrations challenge.
#
# The native path is the primary one: no Docker, no network, no module downloads.
# Every target below works offline. Everything that builds, tests, seeds or grades
# needs nothing but a Go toolchain; the convenience targets that talk to a running
# pair of services (up, seed, reset, creds, the smoke targets) use curl, and
# validate-reports uses python3 to read your JSON.
#
# Run `make` or `make help` for the list.

SHELL := /bin/sh
GO ?= go
BIN := bin
RUN := .run
SCENARIO ?= S0
SEED ?= 20260416
S ?= S0
CONNECTOR ?=
SUBMISSION ?= .
ERP_PORT ?= 8082
TWIN_PORT ?= 8081

# Flags a target passes through. NOCHAOS=1 disables fault injection, which
# changes nothing about the resulting state and is there for local debugging.
CHAOS_FLAG := $(if $(NOCHAOS),--no-chaos,)
CONNECTOR_FLAG := $(if $(CONNECTOR),--connector "$(CONNECTOR)",)

# The hidden assertion kinds live behind a build tag, and the file that defines
# them is stripped from the candidate bundle. Detecting the file rather than
# asking for a flag means the reviewer's grader always has them and the bundle's
# always compiles: with the file gone, -tags hidden would exclude both halves of
# the constant and fail to build. Nobody has to remember which they are holding.
GRADE_TAGS := $(if $(wildcard internal/grader/hidden.go),-tags hidden,)

# Internal targets that need what the candidate bundle does not ship. The file is
# stripped from it, and a hyphen-include means its absence is silence rather than
# an error, so there is nothing here to be curious about.
-include Makefile.internal

.DEFAULT_GOAL := help

## help: list every target
help:
	@echo "BLP integrations challenge"
	@echo
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | awk -F': ' '{printf "  \033[1m%-22s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "variables: SCENARIO=$(SCENARIO) SEED=$(SEED) S=$(S) NOCHAOS= STRETCH= CONNECTOR= SUBMISSION=."

## build: compile every command into bin/
build:
	@mkdir -p $(BIN)
	@$(GO) build -o $(BIN)/erp ./cmd/erp
	@$(GO) build -o $(BIN)/miniblp ./cmd/miniblp
	@$(GO) build $(GRADE_TAGS) -o $(BIN)/grade ./cmd/grade
	@$(GO) build -o $(BIN)/seed ./cmd/seed
	@echo "built: $$(ls $(BIN) | tr '\n' ' ')$(if $(GRADE_TAGS), (grade with the hidden assertion kinds),)"

## up: start both services in the background and print their URLs
up: build down
	@mkdir -p $(RUN) var/erp/export var/miniblp
	@$(BIN)/erp --listen 127.0.0.1:$(ERP_PORT) --scenario $(SCENARIO) --seed $(SEED) \
		--export-dir var/erp/export --admin-token dev-erp-admin $(CHAOS_FLAG) \
		> $(RUN)/erp.log 2>&1 & echo $$! > $(RUN)/erp.pid
	@TWIN_CLIENT_ID=blp-connector TWIN_CLIENT_SECRET=dev-twin-secret \
		$(BIN)/miniblp --listen 127.0.0.1:$(TWIN_PORT) --scenario $(SCENARIO) --seed $(SEED) \
		--data-dir var/miniblp --inbox-dir var/miniblp/inbox --outbox-dir var/miniblp/outbox \
		--admin-token dev-twin-admin $(CHAOS_FLAG) \
		> $(RUN)/miniblp.log 2>&1 & echo $$! > $(RUN)/miniblp.pid
	@sleep 2
	@printf '  ERP        http://127.0.0.1:%s          UI http://127.0.0.1:%s/ui\n' $(ERP_PORT) $(ERP_PORT)
	@printf '  digital twin  http://127.0.0.1:%s       UI http://127.0.0.1:%s/ui\n' $(TWIN_PORT) $(TWIN_PORT)
	@printf '  logs in %s/, state in var/. Stop with: make down\n' $(RUN)
	@printf '  credentials for both services: make creds\n'
	@$(MAKE) --no-print-directory seed

## down: stop both services
down:
	@for f in $(RUN)/erp.pid $(RUN)/miniblp.pid; do \
		if [ -f "$$f" ]; then kill "$$(cat $$f)" 2>/dev/null || true; rm -f "$$f"; fi; \
	done
	@echo "stopped"

## seed: load SCENARIO into both running services
seed:
	@curl -fsS -X POST -H 'X-Admin-Token: dev-erp-admin' -H 'Content-Type: application/json' \
		-d '{"scenario":"$(SCENARIO)","seed":$(SEED)}' \
		http://127.0.0.1:$(ERP_PORT)/erp-admin/v1/seed > /dev/null \
		|| { echo "the ERP is not running: make up"; exit 1; }
	@curl -fsS -X POST -H 'X-Admin-Token: dev-twin-admin' -H 'Content-Type: application/json' \
		-d '{"scenario":"$(SCENARIO)","seed":$(SEED)}' \
		http://127.0.0.1:$(TWIN_PORT)/admin/v1/seed > /dev/null \
		|| { echo "the twin is not running: make up"; exit 1; }
	@echo "seeded $(SCENARIO) at seed $(SEED) into both services"

## reset: clear both services back to an empty run, keeping the dataset
reset:
	@curl -fsS -X POST -H 'X-Admin-Token: dev-erp-admin' http://127.0.0.1:$(ERP_PORT)/erp-admin/v1/reset > /dev/null
	@curl -fsS -X POST -H 'X-Admin-Token: dev-twin-admin' http://127.0.0.1:$(TWIN_PORT)/admin/v1/reset > /dev/null
	@echo "reset"

## ui: print both UI URLs
ui:
	@printf '  digital twin  http://127.0.0.1:%s/ui\n  ERP           http://127.0.0.1:%s/ui\n' $(TWIN_PORT) $(ERP_PORT)

# The two services, their libraries and the reference connector must never read
# the wall clock: every graded outcome depends on the virtual clock in
# internal/simclock, and a real timestamp anywhere in that path makes a run
# unreproducible. internal/grader is deliberately out of scope - it supervises
# real OS processes, so its health deadlines, port-release waits and elapsed-time
# measurements are wall clock by necessity, and none of them reaches a service.
# Comment lines are filtered out: prose about math/rand is not a use of it.
# Makefile.internal appends the packages that only exist in our tree.
# += rather than =: Makefile.internal is -included ABOVE and appends the packages
# that exist only in our tree, and a plain assignment here silently discarded
# them, which left the reference connector outside the guard it is named in.
WALLCLOCK_SCOPE += cmd internal/model internal/store internal/httpx internal/miniblp \
	internal/erp internal/importer internal/seed

## creds: print the credentials of the running dev services, as env exports
#
# In a graded run the harness passes these to your connector in the environment,
# so this target prints them in exactly that shape: eval it and your connector
# sees what the grader would give it. The ERP mints its own per process and
# publishes them on its admin surface; the twin takes its pair from the
# environment make up started it with.
creds:
	@if ! curl -fsS -o /dev/null "http://127.0.0.1:$(ERP_PORT)/healthz" 2>/dev/null; then \
		echo "the ERP is not running on port $(ERP_PORT); start it with: make up" >&2; exit 1; fi
	@curl -fsS -H 'X-Admin-Token: dev-erp-admin' \
		"http://127.0.0.1:$(ERP_PORT)/erp-admin/v1/credentials" \
		| tr ',' '\n' | sed -e 's/[{}"]//g' \
		    -e 's/^client_id:/export ERP_CLIENT_ID=/' \
		    -e 's/^client_secret:/export ERP_CLIENT_SECRET=/' \
		    -e 's/^soap_username:/export SOAP_USERNAME=/' \
		    -e 's/^soap_password:/export SOAP_PASSWORD=/' \
		    -e '/^admin_token:/d'
	@echo 'export TWIN_CLIENT_ID=blp-connector'
	@echo 'export TWIN_CLIENT_SECRET=dev-twin-secret'
	@echo '# the admin tokens are ours, not your connector'"'"'s: dev-erp-admin, dev-twin-admin'

## check: gofmt, vet, tests, the import audit, the determinism grep and the protected tree
check:
	@echo "== gofmt"
	@out=$$(gofmt -l . | grep -v '^vendor/' || true); \
		if [ -n "$$out" ]; then echo "not gofmt clean:"; echo "$$out"; exit 1; fi
	@echo "== go vet"
	@$(GO) vet ./...
	@echo "== tests"
	@$(GO) test ./...
	@echo "== standard library only"
	@$(GO) list -deps ./... | grep -E '^[^/]+\.[^/]+/' | grep -v '^github.com/fatjonblp/' \
		| sort -u > /tmp/blp-nonstd.txt || true; \
		if [ -s /tmp/blp-nonstd.txt ]; then \
			echo "non-standard-library dependencies:"; cat /tmp/blp-nonstd.txt; exit 1; fi
	@if [ -f go.sum ]; then echo "go.sum exists; this module has no dependencies"; exit 1; fi
	@echo "== no wall clock outside simclock"
	@tools/no-wallclock.sh $(WALLCLOCK_SCOPE)
	@if [ -x tools/verify-pristine.sh ]; then echo "== protected tree"; tools/verify-pristine.sh; fi
	@echo "check: OK"

## smoke-rest: authenticate and read one page from each service
smoke-rest:
	@tools/smoke-rest.sh $(ERP_PORT) $(TWIN_PORT)

## smoke-file: drop the example batch into the twin and print its receipt
smoke-file:
	@tools/smoke-file.sh $(TWIN_PORT)

## soap-smoke: call the one SOAP operation and print what came back
soap-smoke:
	@tools/soap-smoke.sh $(ERP_PORT)

## scenario: run one scenario, e.g. make scenario S=S1 [NOCHAOS=1]
scenario: build
	@$(BIN)/grade run --scenario $(S) $(CONNECTOR_FLAG) $(CHAOS_FLAG)

## selfcheck: run every public scenario with per-assertion output (STRETCH=1 adds X1 and X2)
selfcheck: build
	@$(BIN)/grade selfcheck $(CONNECTOR_FLAG) $(CHAOS_FLAG) $(if $(STRETCH),--stretch,)

## golden-diff: diff one Go-task fixture against its expectation
golden-diff: build
	@if [ -z "$(FIXTURE)" ]; then \
		for f in testdata/kredexp/*.txt; do echo "== $$f"; $(BIN)/grade golden-diff --fixture "$$f" || true; done; \
	else $(BIN)/grade golden-diff --fixture $(FIXTURE); fi

## validate-reports: check three report files, e.g. make validate-reports DIR=examples/reports
validate-reports: build
	@tools/validate-reports.sh $(DIR)

## score: score a submission, e.g. make score SUBMISSION=../candidate
score: build
	@$(BIN)/grade score --submission $(SUBMISSION) $(CONNECTOR_FLAG)

## scenarios: list the scenarios present in this tree
scenarios: build
	@$(BIN)/grade scenarios

## regen-protected: regenerate PROTECTED.sha256 after a deliberate change of ours
regen-protected:
	@tools/regen-protected.sh

## clean: remove build output and runtime state
clean: down
	@rm -rf $(BIN) $(RUN) var grading/out dist
	@echo "cleaned"

.PHONY: help build up down seed reset ui check smoke-rest smoke-file soap-smoke \
	scenario selfcheck golden-diff validate-reports score scenarios creds \
	candidate-bundle regen-protected clean
