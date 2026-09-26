# Managed by go-conventions (references/ci.md owns the target contract);
# converge rewrites it. `check` is the local gate; CI runs it and adds
# govulncheck (references/ci.md, "The gates").
GOLANGCI_LINT_VERSION ?= v2.13.2
# Module directories, relative to this file; a single-module repo uses ".".
MODULES ?= .
GOBIN ?= $(shell go env GOPATH)/bin

.DEFAULT_GOAL := help

.PHONY: help
help: ## Print this help message
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} \
	     /^[a-zA-Z_-]+:.*?##/ { printf "  %-16s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: print-golangci-version
print-golangci-version: ## Print the pinned golangci-lint version (CI reads this)
	@echo $(GOLANGCI_LINT_VERSION)

.PHONY: tools
tools: ## Install the pinned golangci-lint into GOBIN
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
	  | sh -s -- -b "$(GOBIN)" $(GOLANGCI_LINT_VERSION)

.PHONY: build
build: ## Compile every package
	@for m in $(MODULES); do (cd "$$m" && go build ./...) || exit 1; done

.PHONY: generate
generate: ## Run go generate (counterfeiter fakes and the like)
	@for m in $(MODULES); do (cd "$$m" && go generate ./...) || exit 1; done

.PHONY: generate-check
generate-check: generate ## Fail when go generate changes a tracked file
	@git diff --quiet -- $(MODULES) || { git --no-pager diff --stat -- $(MODULES); echo "go generate output is stale"; exit 1; }

.PHONY: fmt
fmt: ## Apply gofmt, gofumpt, and goimports through golangci-lint
	@for m in $(MODULES); do (cd "$$m" && golangci-lint fmt ./...) || exit 1; done

.PHONY: fmt-check
fmt-check: ## Fail when formatting would change a file
	@for m in $(MODULES); do \
	  out=$$(cd "$$m" && golangci-lint fmt --diff ./...) || exit 1; \
	  if [ -n "$$out" ]; then printf '%s\n' "$$out"; echo "formatting needed in $$m"; exit 1; fi; \
	done

.PHONY: vet
vet: ## go vet
	@for m in $(MODULES); do (cd "$$m" && go vet ./...) || exit 1; done

.PHONY: lint
lint: ## golangci-lint run
	@for m in $(MODULES); do (cd "$$m" && golangci-lint run ./...) || exit 1; done

.PHONY: fix
fix: ## Apply the go fix modernizers
	@for m in $(MODULES); do (cd "$$m" && go fix ./...) || exit 1; done

.PHONY: fix-check
fix-check: ## Fail when go fix would change a file
	@for m in $(MODULES); do \
	  out=$$(cd "$$m" && go fix -diff ./...) || exit 1; \
	  if [ -n "$$out" ]; then printf '%s\n' "$$out"; echo "go fix needed in $$m"; exit 1; fi; \
	done

.PHONY: tidy
tidy: ## go mod tidy
	@for m in $(MODULES); do (cd "$$m" && go mod tidy) || exit 1; done

.PHONY: tidy-check
tidy-check: ## Fail when go mod tidy would change go.mod or go.sum
	@for m in $(MODULES); do (cd "$$m" && go mod tidy -diff) || { echo "go mod tidy needed in $$m"; exit 1; }; done

.PHONY: test
test: ## Run every suite with the race detector
	@for m in $(MODULES); do (cd "$$m" && go test -race -count=1 ./...) || exit 1; done

.PHONY: check
check: generate-check fmt-check vet lint fix-check tidy-check test ## The local gate
