SHELL := /bin/bash

.PHONY: ci-gate check-branch validate-locks test-tools check-trailers check-adr lint-sh lint-semgrep lint-go test-unit test-coverage

# The CI-first rule: every code change lands together with its CI in the
# same PR. This target is that CI, runnable locally.
ci-gate: check-branch validate-locks test-tools check-trailers check-adr lint-sh lint-semgrep lint-go test-unit

# Branch names are cheapest to fix before push: a rename after a PR exists
# forces close-and-reopen (GitHub cannot retarget a PR).
check-branch:
	python3 .ai/tools/check_branch_name.py

validate-locks:
	python3 .ai/tools/validate_locks.py

test-tools:
	python3 -m unittest discover -s .ai/tools/tests -p 'test_*.py' -v

check-trailers:
	bash .ai/tools/check-trailers.sh

check-adr:
	python3 .ai/tools/adr_manager.py check

lint-sh:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck .ai/tools/*.sh; \
		echo "shellcheck: OK"; \
	else \
		echo "shellcheck: not installed, skipped (CI installs it)"; \
	fi

# Custom semgrep rule pack (.ai/semgrep/rules): first validate the rule
# fixtures, then scan the repository scripts the rules protect.
lint-semgrep:
	@if command -v semgrep >/dev/null 2>&1; then \
		semgrep --config .ai/semgrep/rules --metrics=off --test .ai/semgrep/rules && \
		semgrep --config .ai/semgrep/rules --metrics=off --error .ai/tools && \
		echo "semgrep: OK"; \
	else \
		echo "semgrep: not installed, skipped (CI installs it)"; \
	fi

# Go targets follow the same warn-and-skip policy as shellcheck: missing
# tools or a missing go.mod skip loudly instead of failing. The CI job
# that turns these red lands with the first Go component (CI-first rule).
lint-go:
	@if [ ! -f go.mod ]; then \
		echo "lint-go: no go.mod yet, skipped (lands with the first Go component)"; \
	elif command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint: not installed, skipped (CI installs it)"; \
	fi

test-unit:
	@if [ ! -f go.mod ]; then \
		echo "test-unit: no go.mod yet, skipped (lands with the first Go component)"; \
	elif command -v gotestsum >/dev/null 2>&1; then \
		gotestsum --format testname -- -race ./...; \
	else \
		go test -race ./...; \
	fi

test-coverage:
	@if [ ! -f go.mod ]; then \
		echo "test-coverage: no go.mod yet, skipped (lands with the first Go component)"; \
	elif command -v go >/dev/null 2>&1; then \
		go test -covermode=atomic -coverprofile=coverage.out ./... && \
		if command -v go-test-coverage >/dev/null 2>&1; then \
			go-test-coverage -config=.testcoverage.yml; \
		else \
			go tool cover -func=coverage.out; \
		fi; \
	else \
		echo "go: not installed, skipped (CI installs it)"; \
	fi
