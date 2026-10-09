# Managed by go-conventions (references/ci.md owns the target contract);
# converge rewrites it. `check` is the local gate; CI runs it and adds
# govulncheck (references/ci.md, "The gates").
GOLANGCI_LINT_VERSION ?= v2.14.0
# Module directories, relative to this file; a single-module repo uses ".".
MODULES ?= .
# go-ceph's async completions and op steps rgw-go uses sit behind ceph_preview.
# A comma-separated list, as go build -tags takes it.
GO_TAGS ?= ceph_preview
GOBIN ?= $(shell go env GOPATH)/bin

.DEFAULT_GOAL := help

.PHONY: help
help: ## Print this help message
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} \
	     /^[a-zA-Z0-9_-]+:.*?##/ { printf "  %-20s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: print-golangci-version
print-golangci-version: ## Print the pinned golangci-lint version (CI reads this)
	@echo $(GOLANGCI_LINT_VERSION)

.PHONY: print-go-tags
print-go-tags: ## Print the build tags every go command takes (CI reads this)
	@echo $(GO_TAGS)

.PHONY: tools
tools: ## Install the pinned golangci-lint into GOBIN
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
	  | sh -s -- -b "$(GOBIN)" $(GOLANGCI_LINT_VERSION)

.PHONY: build
build: ## Compile every package
	@for m in $(MODULES); do (cd "$$m" && go build "-tags=$(GO_TAGS)" ./...) || exit 1; done

.PHONY: generate
generate: ## Run go generate (counterfeiter fakes and the like)
	@for m in $(MODULES); do (cd "$$m" && go generate "-tags=$(GO_TAGS)" ./...) || exit 1; done

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
	@for m in $(MODULES); do (cd "$$m" && go vet "-tags=$(GO_TAGS)" ./...) || exit 1; done

.PHONY: lint
lint: ## golangci-lint run
	@for m in $(MODULES); do (cd "$$m" && golangci-lint run ./...) || exit 1; done

.PHONY: fix
fix: ## Apply the go fix modernizers
	@for m in $(MODULES); do (cd "$$m" && go fix "-tags=$(GO_TAGS)" ./...) || exit 1; done

.PHONY: fix-check
fix-check: ## Fail when go fix would change a file
	@for m in $(MODULES); do \
	  out=$$(cd "$$m" && go fix "-tags=$(GO_TAGS)" -diff ./...) || exit 1; \
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
	@for m in $(MODULES); do (cd "$$m" && go test "-tags=$(GO_TAGS)" -race -count=1 ./...) || exit 1; done

.PHONY: goldens
goldens: ## Regenerate ceph-dencoder goldens from the object corpus (needs podman)
	hack/goldens/gen.sh

.PHONY: release-pins-check
release-pins-check: ## Fail when .goreleaser.yaml's Ceph images or build tags differ from their homes
	@hack/image/release-pins-check.sh

.PHONY: parity-options-check
parity-options-check: ## Fail when a release's chart values start radosgw under other options than hack/rooket/lib.sh's parity_options
	@hack/rooket/parity-options-check.sh

.PHONY: check
check: generate-check fmt-check vet lint fix-check tidy-check release-pins-check parity-options-check test ## The local gate

# Disposable one-worker Rook clusters on kind, one per release, driven by
# rooket (hack/rooket/README.md). RELEASE is squid or tentacle; each release is
# its own cluster, rgw-go-RELEASE, with its output in hack/rooket/out/RELEASE/.
RELEASE ?=
# The rooket binary the harness scripts and the gate run. go test runs the gate
# in its package directory, so a path is made absolute; a bare name is left for
# PATH to resolve.
ROOKET ?= rooket
ROOKET_BIN = $(if $(findstring /,$(ROOKET)),$(abspath $(ROOKET)),$(ROOKET))
CLUSTER_OUT = $(CURDIR)/hack/rooket/out/$(RELEASE)

.PHONY: need-release
need-release:
	@case "$(RELEASE)" in squid | tentacle) ;; *) echo "set RELEASE=squid or RELEASE=tentacle" >&2; exit 1 ;; esac

.PHONY: cluster-up
cluster-up: need-release ## Start the RELEASE cluster and write its client config
	ROOKET=$(ROOKET_BIN) hack/rooket/up.sh $(RELEASE)

.PHONY: populate
populate: need-release ## Write the fixed data set through the RELEASE cluster's radosgw
	ROOKET=$(ROOKET_BIN) hack/rooket/populate.sh $(RELEASE)

# Every package but the gate, which make gate runs on its own. The label
# filter assumes every test package is a Ginkgo suite; a plain testing
# package would reject the flag. ./test/integration runs in a go test of its
# own after the others, never beside them: its specs link extra buckets to
# the populated user alice and sync her stats, while internal/cls/user's
# specs pin alice's bucket list and header to the population.
.PHONY: integration
integration: need-release ## Run the integration specs against the RELEASE cluster
	@tags="$(GO_TAGS),integration"; \
	pkgs=$$(go list "-tags=$$tags" ./... | grep -Ev '/test/(gate|integration)(/|$$)') || exit 1; \
	RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf \
	  go test "-tags=$$tags" -race -count=1 $$pkgs -ginkgo.label-filter=integration && \
	RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf \
	  go test "-tags=$$tags" -race -count=1 ./test/integration -ginkgo.label-filter=integration

.PHONY: gate
gate: need-release ## Run the phase 0 gate against the populated RELEASE cluster
	RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf \
	RGW_GO_TEST_MANIFEST=$(CLUSTER_OUT)/manifest.json \
	RGW_GO_TEST_ROOKET=$(ROOKET_BIN) \
	  go test "-tags=$(GO_TAGS),integration" -race -count=1 -v ./test/gate/... -args -ginkgo.v

# The gateway s3tests and admin-suite run against, and the run id that names
# their reports.
GATEWAY ?= radosgw
RUN ?= local

.PHONY: s3tests
s3tests: need-release ## Run the phase 1 s3-tests set against GATEWAY (radosgw|rgw-go) on the RELEASE cluster; junit under hack/s3tests/out/
	ROOKET=$(ROOKET_BIN) hack/s3tests/run.sh $(RELEASE) $(GATEWAY) $(RUN)

.PHONY: admin-suite
admin-suite: need-release ## Run go-ceph's rgw/admin suite at its pinned tag against GATEWAY on the RELEASE cluster; go test -json under hack/admin/_out/
	ROOKET=$(ROOKET_BIN) hack/admin/run.sh $(RELEASE) $(GATEWAY) $(RUN)

# hack/parity's package doc owns the result file and the comparison rules.
.PHONY: parity-record
parity-record: ## Record a baseline from RUN-FILES: make parity-record SUITE=s3tests FORMAT=junit OUT=test/s3tests/baseline/squid.json META="release=squid ceph_version=19.2.6 s3tests_commit=..." FILES="a.xml b.xml"
	go run ./hack/parity record --format $(FORMAT) --suite $(SUITE) --out $(OUT) $(foreach kv,$(META),--meta $(kv)) $(FILES)

.PHONY: parity-check
parity-check: ## Compare a candidate result with a baseline: make parity-check BASELINE=... CANDIDATE=...
	go run ./hack/parity diff --baseline $(BASELINE) --candidate $(CANDIDATE)

# The baseline s3tests-parity compares rgw-go's run with; a re-record passes
# the one it has just written. hack/parity runs as a built binary because go
# run exits 1 for any failure: make's error line then names the diff's own
# status, 1 for a test that differs and 2 for a baseline recorded under
# another s3-tests commit, Ceph version, release or deselect lists.
BASELINE ?= test/s3tests/baseline/$(RELEASE).json

.PHONY: s3tests-parity
s3tests-parity: need-release ## Run the phase 1 s3-tests set against rgw-go and compare with radosgw's recorded baseline
	$(MAKE) s3tests RELEASE=$(RELEASE) GATEWAY=rgw-go RUN=parity
	go build -o bin/parity ./hack/parity
	bin/parity record --format junit --suite s3tests --out $(CLUSTER_OUT)/s3tests-rgw-go.json \
	  --meta release=$(RELEASE) --meta ceph_version=$$(jq -r .ceph_version $(CLUSTER_OUT)/manifest.json) --meta gateway=rgw-go \
	  --meta s3tests_commit=$$(hack/s3tests/run.sh --print-commit) --meta deselect=$$(hack/s3tests/run.sh --print-deselect) \
	  hack/s3tests/out/$(RELEASE)-rgw-go-parity.xml
	bin/parity diff --baseline $(BASELINE) --candidate $(CLUSTER_OUT)/s3tests-rgw-go.json --known test/s3tests/known-differences-$(RELEASE).txt

.PHONY: bench-seam
bench-seam: need-release ## Sweep the seam microbenchmark and the rados bench floor against the RELEASE cluster
	ROOKET=$(ROOKET_BIN) RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf GO_TAGS=$(GO_TAGS) hack/bench/seam.sh $(RELEASE)

# Extra rgw-go serve flags for rgw-go-up, such as --rados-completions=pipe.
RGW_GO_FLAGS ?=

.PHONY: rgw-go-up
rgw-go-up: need-release ## Build rgw-go and run it on the host against the RELEASE cluster under the parity settings
	ROOKET=$(ROOKET_BIN) GO_TAGS=$(GO_TAGS) hack/rooket/rgw-go-up.sh $(RELEASE) $(RGW_GO_FLAGS)

.PHONY: rgw-go-down
rgw-go-down: need-release ## Stop the rgw-go that rgw-go-up started against the RELEASE cluster
	ROOKET=$(ROOKET_BIN) hack/rooket/rgw-go-down.sh $(RELEASE)

.PHONY: cluster-down
cluster-down: need-release ## Remove the RELEASE cluster, its disks and its output
	ROOKET=$(ROOKET_BIN) hack/rooket/down.sh $(RELEASE)

# The recipe is silent so that stdout carries the image reference alone.
.PHONY: image
image: need-release ## Build the derived Ceph image for RELEASE with rgw-go as radosgw (prints the reference)
	@hack/image/build.sh $(RELEASE)
