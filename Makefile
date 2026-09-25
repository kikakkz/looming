SHELL := /bin/bash

.PHONY: ci-gate validate-locks test-tools check-trailers check-adr lint-sh

# The CI-first rule: every code change lands together with its CI in the
# same PR. This target is that CI, runnable locally.
ci-gate: validate-locks test-tools check-trailers check-adr lint-sh

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
