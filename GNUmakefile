PROVIDER := terraform-provider-railway
DEV_DIR ?= $(HOME)/.local/share/$(PROVIDER)/dev
GOLANGCI_LINT_VERSION := v2.15.0

.PHONY: build install generate fmt-check vet lint test test-race testacc docs-check examples-check check

build:
	go build ./...

install:
	mkdir -p "$(DEV_DIR)"
	go build -o "$(DEV_DIR)/$(PROVIDER)" .
	@echo "Installed $(DEV_DIR)/$(PROVIDER); point dev_overrides at $(DEV_DIR)"

generate:
	go generate ./...

fmt-check:
	test -z "$$(gofmt -l .)"

vet:
	go vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint is not installed. Install it with:" >&2; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)" >&2; \
		echo "or: mise use -g golangci-lint@$(GOLANGCI_LINT_VERSION:v%=%)" >&2; \
		exit 1; \
	fi

test:
	go test ./...

test-race:
	go test -race ./...

testacc:
	TF_ACC=1 go test ./internal/provider/... -run TestAcc -v -timeout 120m

docs-check:
	go generate ./...
	git diff --exit-code
	test -z "$$(git ls-files --others --exclude-standard docs examples)"

examples-check:
	tofu fmt -check -recursive examples
	./scripts/check-examples.sh

check: fmt-check vet lint test-race examples-check docs-check
