default: help

help:
	@echo "Usage: make <lint|test>"

.PHONY: lint
lint:
	@echo
	@echo "==> Running linter <=="
	@ golangci-lint run ./...

.PHONY: test
test:
	@echo
	@echo "==> Running unit tests with coverage <=="
	@ ./scripts/coverage.sh