SHELL := /bin/bash

.PHONY: ci-gate check-branch validate-locks test-tools check-trailers check-adr check-skills check-index check-docs lint-sh lint-semgrep lint-go lint-arch lint-pr-body test-unit test-integration test-coverage test-e2e

# The CI-first rule: every code change lands together with its CI in the
# same PR. This target is that CI, runnable locally.
ci-gate: check-branch validate-locks test-tools check-trailers check-adr check-skills check-index check-docs lint-sh lint-semgrep lint-go lint-arch lint-pr-body test-unit test-integration test-coverage

check-skills:
	python3 .ai/tools/check_skills.py

check-index:
	python3 .ai/tools/check_index.py

check-docs:
	python3 .ai/tools/check_docs.py

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

# Go components follow the polyglot layout (AD-34): every component owns
# one go.mod at its directory root. The Go targets below loop over each
# discovered component; a missing tool skips loudly instead of failing
# (same warn-and-skip policy as shellcheck; CI installs the tools and
# turns these red). Language-specific tooling stays under the component
# it serves: every check runs from inside that component's directory.
# platform/go is the AD-34 shared kit nested one level deeper; the
# tests/e2e module is the bundle e2e layer with its own target.
GO_COMPONENTS := $(sort $(patsubst %/go.mod,%,$(wildcard */go.mod)) platform/go)

lint-go:
	@if [ -z "$(GO_COMPONENTS)" ]; then \
		echo "lint-go: no Go components (no */go.mod), skipped"; \
	elif command -v golangci-lint >/dev/null 2>&1; then \
		for c in $(GO_COMPONENTS); do \
			echo "lint-go: $$c"; \
			(cd $$c && golangci-lint run ./...) || exit 1; \
		done; \
	else \
		echo "golangci-lint: not installed, skipped (CI installs it)"; \
	fi

# PR bodies classify every issue reference (AD-30 prevention, #92).
# Locally and on push events there is no PR body — the tool skips with
# a note; the pull_request job in ci.yml feeds the body via PR_BODY.
lint-pr-body:
	@python3 .ai/tools/check_pr_body.py

# AD-23 matrix enforcement (.go-arch-lint.yml); AD-33 extended it for
# the gateway, AD-34 made it per-component. Warns and skips when the
# tool is missing locally.
lint-arch:
	@if [ -z "$(GO_COMPONENTS)" ]; then \
		echo "lint-arch: no Go components (no */go.mod), skipped"; \
	elif command -v go-arch-lint >/dev/null 2>&1; then \
		for c in $(GO_COMPONENTS); do \
			echo "lint-arch: $$c"; \
			(cd $$c && go-arch-lint check) || exit 1; \
		done; \
	else \
		echo "go-arch-lint: not installed, skipped (CI installs it)"; \
	fi

test-unit:
	@if [ -z "$(GO_COMPONENTS)" ]; then \
		echo "test-unit: no Go components (no */go.mod), skipped"; \
	elif command -v gotestsum >/dev/null 2>&1; then \
		for c in $(GO_COMPONENTS); do \
			echo "test-unit: $$c"; \
			(cd $$c && gotestsum --format testname -- -race ./...) || exit 1; \
		done; \
	else \
		for c in $(GO_COMPONENTS); do \
			echo "test-unit: $$c"; \
			(cd $$c && go test -race ./...) || exit 1; \
		done; \
	fi

# Integration layer (AD-25, `integration` build tag): testcontainers-go
# needs a Docker daemon; without one the target skips loudly, same
# warn-and-skip policy as the lint tools (CI provides Docker). Components
# without integration-tagged files skip per component. A component that
# ships .testcoverage.integration.yml also gets its integration profile
# checked against the thresholds (adapters and migrations are covered
# here, not in the unit profile).
test-integration:
	@if [ -z "$(GO_COMPONENTS)" ]; then \
		echo "test-integration: no Go components (no */go.mod), skipped"; \
	elif ! docker info >/dev/null 2>&1; then \
		echo "test-integration: docker daemon unavailable, skipped (CI provides Docker)"; \
	else \
		for c in $(GO_COMPONENTS); do \
			if grep -rqs --include='*.go' -e '^//go:build integration' $$c; then \
				echo "test-integration: $$c"; \
				if [ -f $$c/.testcoverage.integration.yml ] && command -v go-test-coverage >/dev/null 2>&1; then \
					(cd $$c && go test -tags integration -covermode=atomic -coverprofile=coverage-integration.out ./... && \
						go-test-coverage -config=.testcoverage.integration.yml) || exit 1; \
				elif [ -f $$c/.testcoverage.integration.yml ]; then \
					echo "test-integration: $$c has integration coverage config but go-test-coverage is missing, running without threshold"; \
					(cd $$c && go test -tags integration ./...) || exit 1; \
				else \
					(cd $$c && go test -tags integration ./...) || exit 1; \
				fi; \
			else \
				echo "test-integration: $$c has no integration-tagged files, skipped"; \
			fi; \
		done; \
	fi

test-coverage:
	@if [ -z "$(GO_COMPONENTS)" ]; then \
		echo "test-coverage: no Go components (no */go.mod), skipped"; \
	elif command -v go >/dev/null 2>&1; then \
		for c in $(GO_COMPONENTS); do \
			echo "test-coverage: $$c"; \
			(cd $$c && go test -covermode=atomic -coverprofile=coverage.out ./... && \
			if command -v go-test-coverage >/dev/null 2>&1; then \
				go-test-coverage -config=.testcoverage.yml; \
			else \
				go tool cover -func=coverage.out; \
			fi) || exit 1; \
		done; \
	else \
		echo "go: not installed, skipped (CI installs it)"; \
	fi

# Bundle e2e layer (AD-25, `e2e` build tag): the onboarding scenario
# driven through the real looming CLI against real component
# containers. Deliberately OUTSIDE ci-gate — AD-25 deferred this layer
# until a runner budget exists, and this target IS that layer landing:
# its cost stays visible as its own target (and its own CI job in
# .github/workflows/e2e.yml), never folded into the gate. First run
# builds every component image; expect minutes, not seconds.
test-e2e:
	@if ! docker info >/dev/null 2>&1; then \
		echo "test-e2e: docker daemon unavailable, skipped (the e2e suite drives real containers; CI provides Docker)"; \
	else \
		echo "test-e2e: building component images and running the bootstrap scenario (first run takes a while)"; \
		(cd tests/e2e && go test -tags e2e -v -count=1 -timeout 30m ./...); \
	fi
