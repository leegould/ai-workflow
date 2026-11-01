# `make check` is the single gate: agents run it before opening a PR and CI runs
# it on every push. Tool versions are pinned in tools/go.mod.

TOOL := go tool -modfile=tools/go.mod
COVERAGE_MIN := 70
# Coverage is measured on domain code only. worker/, ticket/, ci/ and
# target/setup are thin mains that only wire things up; keep logic out of them.
COVERAGE_PKGS := ./pipeline/...,./target

.PHONY: check fmt lint workflowcheck vuln test coverage guard target

check: lint workflowcheck vuln test coverage

fmt:
	$(TOOL) golangci-lint fmt ./...

lint:
	$(TOOL) golangci-lint run ./...

# Flags non-deterministic code (time.Now, goroutines, map ranges, rand...)
# reachable from workflow functions.
workflowcheck:
	$(TOOL) workflowcheck ./...

vuln:
	$(TOOL) govulncheck ./...

test:
	go test -race -coverpkg=$(COVERAGE_PKGS) -coverprofile=coverage.out ./...

coverage: test
	@go tool cover -func=coverage.out | awk -v min=$(COVERAGE_MIN) \
		'/^total:/ { sub("%","",$$3); printf "coverage %s%% (min %s%%)\n", $$3, min; if ($$3+0 < min) { print "FAIL: coverage below minimum"; exit 1 } }'

# PR-only: fails on deleted tests, new skips, or loosened gates. Needs a base ref.
guard:
	scripts/guard.sh $(or $(BASE),origin/main)

# Local stand-in for GitHub, CI and deploy (see target/). The hook calls the CI
# binary built here, so rebuild after changing ci/ or target/.
TARGET_DIR ?= $(HOME)/.cache/ai-workflow-target
target:
	mkdir -p $(TARGET_DIR)/bin
	go build -o $(TARGET_DIR)/bin/ci ./ci/local
	go run ./target/setup -target $(TARGET_DIR) -ci $(TARGET_DIR)/bin/ci $(if $(RESET),-reset)
