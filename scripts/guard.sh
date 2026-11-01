#!/usr/bin/env bash
# Catches the ways an agent can make checks pass without making code better:
# deleting tests, skipping them, or loosening the gates themselves.
#
#   scripts/guard.sh <base-ref>
set -euo pipefail

base="${1:?usage: guard.sh <base-ref>}"
fail=0

deleted=$(git diff --name-only --diff-filter=D "$base"...HEAD -- '*_test.go')
if [[ -n "$deleted" ]]; then
  echo "FAIL: test files deleted:"; echo "$deleted"; fail=1
fi

skips=$(git diff "$base"...HEAD -- '*_test.go' | grep -E '^\+.*\bt\.(Skip|SkipNow)\(' || true)
if [[ -n "$skips" ]]; then
  echo "FAIL: tests skipped:"; echo "$skips"; fail=1
fi

# Not a failure on its own: a human may legitimately change these. The risk
# assessment treats any hit here as high risk.
gates=$(git diff --name-only "$base"...HEAD -- .golangci.yml Makefile scripts/ .github/ CLAUDE.md tools/go.mod steps/)
if [[ -n "$gates" ]]; then
  echo "WARN: quality gates changed (high risk, needs a human):"; echo "$gates"
fi

exit $fail
