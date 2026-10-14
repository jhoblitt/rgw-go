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

# The least the data pool could take on an idle cluster of either release, in
# bytes: Tentacle's in a seam sweep of 2026-10-02.
BENCH_PLAN_IDLE ?= 8220119040

# Every setting the gateway command sizes a profile from is unset, and the
# config file that could set them, so the check judges the defaults whatever
# the caller's shell exports.
.PHONY: bench-plan-check
bench-plan-check: ## Fail when a gateway comparison profile's default dataset is more than half of BENCH_PLAN_IDLE bytes
	@out=$$(env -u BENCH_SMOKE -u BENCH_SECONDS -u BENCH_CONC -u BENCH_PUT_OBJECTS -u BENCH_GET_OBJECTS \
	  -u BENCH_LARGE_THREADS -u BENCH_LARGE_OBJECTS -u BENCH_LIST_THREADS -u BENCH_LIST_OBJECTS -u BENCH_CONFIG \
	  BENCH_PLAN=1 BENCH_IDLE_AVAIL=$(BENCH_PLAN_IDLE) go run ./hack/bench/gateway tentacle radosgw 2>&1) || \
	  { printf '%s\n' "$$out"; exit 1; }

# The parity ledgers, radosgw's baselines; hack/parity's package doc owns
# their schema.
LEDGERS = $(wildcard test/s3tests/baseline/*.json test/admin/baseline/*.json)

.PHONY: ledger-check
ledger-check: ## Fail when a parity ledger breaks its schema or the reason rule
	go run ./hack/parity validate $(LEDGERS)

# hack/citecheck's package doc owns the citation form, what is checked and
# what --fix rewrites; CITECHECK_CEPH names a clone other than its default.
.PHONY: citecheck
citecheck: ## Check the Ceph source citations in markdown FILES at the revisions they name: make citecheck FILES="docs/superpowers/plans/x.md ..."
	@test -n "$(FILES)" || { echo "set FILES to the markdown files whose citations to check" >&2; exit 1; }
	go run ./hack/citecheck $(FILES)

.PHONY: citecheck-fix
citecheck-fix: ## Rewrite the code span citations in markdown FILES that hold into links, in place: make citecheck-fix FILES="docs/x.md ..."
	@test -n "$(FILES)" || { echo "set FILES to the markdown files whose citations to rewrite" >&2; exit 1; }
	go run ./hack/citecheck --fix $(FILES)

.PHONY: citecheck-links
citecheck-links: ## Check, without a ceph clone, that every citation in markdown FILES is a well-formed link: make citecheck-links FILES="docs/x.md ..."
	@test -n "$(FILES)" || { echo "set FILES to the markdown files whose citations to check" >&2; exit 1; }
	go run ./hack/citecheck --form-only $(FILES)

.PHONY: check
check: generate-check fmt-check vet lint fix-check tidy-check release-pins-check parity-options-check bench-plan-check ledger-check test ## The local gate

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

# Every package outside test/ whose specs carry the integration label; the
# gate is make gate's. -ginkgo.fail-on-empty fails a package whose filter
# selects no spec, so a lost label or build tag cannot pass as a run.
# ./test/integration runs in a go test of its own after the others, never
# beside them: its specs link extra buckets to the populated user alice and
# sync her stats, while internal/cls/user's specs pin alice's bucket list and
# header to the population. The specs labelled rgw-go need the rgw-go make
# rgw-go-up starts, which make admin-gate runs them against; make integration
# leaves them out.
.PHONY: integration
integration: need-release ## Run the integration specs against the RELEASE cluster
	@tags="$(GO_TAGS),integration"; \
	pkgs=$$(grep -rl --include='*_test.go' 'Label("integration"' cmd hack internal | sed -e 's|/[^/]*$$||' -e 's|^|./|' | sort -u); \
	[ -n "$$pkgs" ] || { echo "no package outside test/ holds an integration spec" >&2; exit 1; }; \
	RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf \
	  go test "-tags=$$tags" -race -count=1 $$pkgs -ginkgo.label-filter=integration -ginkgo.fail-on-empty && \
	RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf \
	  go test "-tags=$$tags" -race -count=1 ./test/integration '-ginkgo.label-filter=integration && !rgw-go' -ginkgo.fail-on-empty

# The admin gate signs as rgwgo-admin, which holds every admin cap with keys
# the spec commits; the specs remove it when they end, and the trap removes
# it, with the garbage its removal leaves, when go test ends some other way.

.PHONY: admin-gate
admin-gate: need-release ## Compare the admin API of the rgw-go rgw-go-up started with the RELEASE cluster's radosgw
	trap 'ROOKET=$(ROOKET_BIN) hack/admin/remove-user.sh $(RELEASE) rgwgo-admin || echo "could not remove the user rgwgo-admin" >&2' EXIT; \
	RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf RGW_GO_TEST_ROOKET=$(ROOKET_BIN) \
	  go test "-tags=$(GO_TAGS),integration" -race -count=1 ./test/integration -ginkgo.label-filter=rgw-go -ginkgo.fail-on-empty

# The gate's read specs need an rgw-go beside the radosgw, which make
# read-gate starts; make gate leaves them out.
.PHONY: gate
gate: need-release ## Run the phase 0 gate against the populated RELEASE cluster
	RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf \
	RGW_GO_TEST_MANIFEST=$(CLUSTER_OUT)/manifest.json \
	RGW_GO_TEST_ROOKET=$(ROOKET_BIN) \
	  go test "-tags=$(GO_TAGS),integration" -race -count=1 -v ./test/gate/... -args -ginkgo.v '-ginkgo.label-filter=!read'

.PHONY: read-gate
read-gate: need-release ## Read the populated corpus through rgw-go and the RELEASE cluster's radosgw and compare
	ROOKET=$(ROOKET_BIN) GO_TAGS=$(GO_TAGS) hack/rooket/read-gate.sh $(RELEASE) $(RGW_GO_FLAGS)

# The gateway s3tests and admin-suite run against, and the run id that names
# their reports. TESTS names a file of pytest node ids, one a line, for
# s3tests to run alone, s3tests-record-tests to record and s3tests-pin to
# write.
GATEWAY ?= radosgw
RUN ?= local
TESTS ?=

.PHONY: s3tests
s3tests: need-release ## Run the phase 1 s3-tests set, or the TESTS file's tests, against GATEWAY (radosgw|rgw-go) on the RELEASE cluster; junit under hack/s3tests/out/
	ROOKET=$(ROOKET_BIN) hack/s3tests/run.sh $(RELEASE) $(GATEWAY) $(RUN) $(TESTS)

.PHONY: admin-suite
admin-suite: need-release ## Run go-ceph's rgw/admin suite at its pinned tag against GATEWAY on the RELEASE cluster; go test -json under hack/admin/_out/
	ROOKET=$(ROOKET_BIN) hack/admin/run.sh $(RELEASE) $(GATEWAY) $(RUN)

# hack/parity's package doc owns the ledger, the result file and the
# comparison rules. A record that appends to a ledger takes the reasons for
# its changed outcomes from REASONS, a file of test ids each followed by its
# reason. DRAFT, when set, makes the ledger a draft: a change REASONS does not
# cover gets the reason "unreviewed: DRAFT", which diff accepts and make
# ledger-check and the ci workflow refuse. PIN_REASON says why a full record
# at a source the ledger does not pin re-stamps every outcome there, which the
# base pin it appends must carry.
REASONS ?=
DRAFT ?=
PIN_REASON ?=
RECORD_FLAGS = $(if $(REASONS),--reasons $(REASONS)) $(if $(DRAFT),--draft "$(DRAFT)") \
  $(if $(PIN_REASON),--pin-reason "$(PIN_REASON)")

.PHONY: parity-record
parity-record: ## Append RUN-FILES to a ledger: make parity-record SUITE=s3tests FORMAT=junit LEDGER=test/s3tests/baseline/squid.json META="release=squid ceph_version=19.2.6 s3tests_commit=..." FILES="a.xml b.xml" [SUBSET=1] [REASONS=FILE] [PIN_REASON=TEXT]
	go run ./hack/parity record --format $(FORMAT) --suite $(SUITE) --ledger $(LEDGER) $(if $(SUBSET),--subset) $(RECORD_FLAGS) \
	  $(foreach kv,$(META),--meta $(kv)) $(FILES)

.PHONY: parity-check
parity-check: ## Compare a candidate result with a ledger: make parity-check BASELINE=... CANDIDATE=...
	go run ./hack/parity diff --baseline $(BASELINE) --candidate $(CANDIDATE)

# The ledger s3tests-parity compares rgw-go's run with; a record made on the
# runner passes the copy it has just appended to. hack/parity runs as a
# built binary because go run exits 1 for any failure: make's error line
# then names the diff's own status, 1 for a test that differs or whose entry
# is stale under another Ceph version, and 2 for a run at another s3-tests
# commit than the ledger's head, or for another release.
BASELINE ?= test/s3tests/baseline/$(RELEASE).json

.PHONY: s3tests-parity
s3tests-parity: need-release ## Run the phase 1 s3-tests set against rgw-go and compare with radosgw's ledger
	$(MAKE) s3tests RELEASE=$(RELEASE) GATEWAY=rgw-go RUN=parity TESTS=
	go build -o bin/parity ./hack/parity
	bin/parity record --format junit --suite s3tests --out $(CLUSTER_OUT)/s3tests-rgw-go.json \
	  --meta release=$(RELEASE) --meta ceph_version=$$(jq -r .ceph_version $(CLUSTER_OUT)/manifest.json) --meta gateway=rgw-go \
	  --meta s3tests_commit=$$(hack/s3tests/run.sh --print-commit) --meta deselect=$$(hack/s3tests/run.sh --print-deselect) \
	  hack/s3tests/out/$(RELEASE)-rgw-go-parity.xml
	bin/parity diff --baseline $(BASELINE) --candidate $(CLUSTER_OUT)/s3tests-rgw-go.json --known test/s3tests/known-differences-$(RELEASE).txt

# The ledger admin-parity compares rgw-go's run with; a record made elsewhere
# passes the copy it has just appended to.
ADMIN_BASELINE ?= test/admin/baseline/$(RELEASE).json

.PHONY: admin-parity
admin-parity: need-release ## Run go-ceph's rgw/admin suite against rgw-go and compare with radosgw's ledger
	$(MAKE) admin-suite RELEASE=$(RELEASE) GATEWAY=rgw-go RUN=parity
	go build -o bin/parity ./hack/parity
	bin/parity record --format gotest --suite admin --out $(CLUSTER_OUT)/admin-rgw-go.json \
	  --meta release=$(RELEASE) --meta ceph_version=$$(jq -er .ceph_version $(CLUSTER_OUT)/manifest.json) --meta gateway=rgw-go \
	  --meta go_ceph_tag=$$(hack/admin/run.sh --print-tag) --meta deselect=$$(hack/admin/run.sh --print-deselect) \
	  hack/admin/_out/$(RELEASE)-rgw-go-parity.jsonl
	bin/parity diff --baseline $(ADMIN_BASELINE) --candidate $(CLUSTER_OUT)/admin-rgw-go.json

# The ledger admin-record appends radosgw's runs to: OUT, else the committed
# one.
ADMIN_RECORD_OUT = $(or $(OUT),test/admin/baseline/$(RELEASE).json)

.PHONY: admin-record
admin-record: need-release ## Run go-ceph's rgw/admin suite twice against radosgw, after rgw-go-up, and append it to the ledger OUT (default test/admin/baseline/RELEASE.json)
	@test -f $(CLUSTER_OUT)/rgw-go.endpoint || { echo "run make rgw-go-up RELEASE=$(RELEASE) first: radosgw records under the parity options it sets" >&2; exit 1; }
	$(MAKE) admin-suite RELEASE=$(RELEASE) GATEWAY=radosgw RUN=base1
	$(MAKE) admin-suite RELEASE=$(RELEASE) GATEWAY=radosgw RUN=base2
	go build -o bin/parity ./hack/parity
	mkdir -p $(dir $(ADMIN_RECORD_OUT))
	ceph_version=$$(jq -er .ceph_version $(CLUSTER_OUT)/manifest.json) && \
	tag=$$(hack/admin/run.sh --print-tag) && \
	deselect=$$(hack/admin/run.sh --print-deselect) && \
	bin/parity record --format gotest --suite admin --ledger $(ADMIN_RECORD_OUT) $(RECORD_FLAGS) \
	  --meta release=$(RELEASE) --meta ceph_version=$$ceph_version --meta gateway=radosgw \
	  --meta go_ceph_tag=$$tag --meta deselect=$$deselect \
	  hack/admin/_out/$(RELEASE)-radosgw-base1.jsonl hack/admin/_out/$(RELEASE)-radosgw-base2.jsonl

# The ledger s3tests-record and s3tests-record-tests append radosgw's runs to:
# OUT, else the committed one.
S3TESTS_RECORD_OUT = $(or $(OUT),test/s3tests/baseline/$(RELEASE).json)

# s3tests_append RUN1,RUN2[,FLAGS] appends radosgw's two s3-tests runs to the
# ledger S3TESTS_RECORD_OUT.
define s3tests_append
go build -o bin/parity ./hack/parity
mkdir -p $(dir $(S3TESTS_RECORD_OUT))
ceph_version=$$(jq -er .ceph_version $(CLUSTER_OUT)/manifest.json) && \
commit=$$(hack/s3tests/run.sh --print-commit) && \
deselect=$$(hack/s3tests/run.sh --print-deselect) && \
freeze=$$(sha256sum hack/s3tests/out/$(RELEASE)-radosgw-$(2).freeze | cut -c1-12) && \
bin/parity record --format junit --suite s3tests --ledger $(S3TESTS_RECORD_OUT) --pin-file hack/s3tests/PIN $(3) $(RECORD_FLAGS) \
  --meta release=$(RELEASE) --meta ceph_version=$$ceph_version --meta gateway=radosgw \
  --meta s3tests_commit=$$commit --meta deselect=$$deselect --meta pytest_freeze=$$freeze \
  hack/s3tests/out/$(RELEASE)-radosgw-$(1).xml hack/s3tests/out/$(RELEASE)-radosgw-$(2).xml
endef

# rgw-go-up sets the parity options on radosgw's entity as well as rgw-go's,
# so a baseline recorded after it is radosgw under the options rgw-go is
# compared under; its endpoint file is the sign it has run.
.PHONY: s3tests-record
s3tests-record: need-release ## Run the phase 1 s3-tests set twice against radosgw, after rgw-go-up, and append it to the ledger OUT (default test/s3tests/baseline/RELEASE.json)
	@test -f $(CLUSTER_OUT)/rgw-go.endpoint || { echo "run make rgw-go-up RELEASE=$(RELEASE) first: radosgw records under the parity options it sets" >&2; exit 1; }
	$(MAKE) s3tests RELEASE=$(RELEASE) GATEWAY=radosgw RUN=base1 TESTS=
	$(MAKE) s3tests RELEASE=$(RELEASE) GATEWAY=radosgw RUN=base2 TESTS=
	$(call s3tests_append,base1,base2)

.PHONY: s3tests-record-tests
s3tests-record-tests: need-release ## Run the TESTS file's s3-tests twice against radosgw, after rgw-go-up, and append them alone to the ledger OUT (default test/s3tests/baseline/RELEASE.json)
	@test -n "$(TESTS)" || { echo "set TESTS to a file of pytest node ids, one a line, such as make s3tests-pin writes" >&2; exit 1; }
	@test -f $(CLUSTER_OUT)/rgw-go.endpoint || { echo "run make rgw-go-up RELEASE=$(RELEASE) first: radosgw records under the parity options it sets" >&2; exit 1; }
	$(MAKE) s3tests RELEASE=$(RELEASE) GATEWAY=radosgw RUN=tests1 TESTS=$(TESTS)
	$(MAKE) s3tests RELEASE=$(RELEASE) GATEWAY=radosgw RUN=tests2 TESTS=$(TESTS)
	$(call s3tests_append,tests1,tests2,--subset)

# A clone of Ceph that holds the s3-tests pin and NEW, the commit to pin.
CLONE ?=
NEW ?=

.PHONY: s3tests-pin
s3tests-pin: ## Move the s3-tests pin to NEW, a commit of rgw-go-s3tests proven additive or reworded in CLONE, and append it to both ledgers; an additive move writes its tests to TESTS
	@test -n "$(CLONE)" -a -n "$(NEW)" || { echo "set CLONE to a clone of Ceph and NEW to the commit to pin" >&2; exit 1; }
	go run ./hack/parity pin --clone $(CLONE) --new $(NEW) $(if $(TESTS),--tests $(TESTS))

.PHONY: bench-seam
bench-seam: need-release ## Sweep the seam microbenchmark and the rados bench floor against the RELEASE cluster; BENCH_LIMITER=1 re-runs only its over-budget cell under the in-flight limiter
	ROOKET=$(ROOKET_BIN) RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf GO_TAGS=$(GO_TAGS) go run ./hack/bench/seam $(RELEASE)

.PHONY: elbencho
elbencho: ## Build the pinned jhoblitt/elbencho fork statically into hack/bench/out/elbencho
	hack/bench/elbencho-build.sh

# Extra rgw-go serve flags for rgw-go-up and bench-gateway, such as
# --rados-completions=pipe.
RGW_GO_FLAGS ?=
# The gateway comparison's profiles; empty runs them all (hack/bench/gateway).
PROFILES ?=

.PHONY: bench-gateway
bench-gateway: need-release ## Run the gateway comparison profiles against GATEWAY (radosgw|rgw-go) on the RELEASE cluster
	ROOKET=$(ROOKET_BIN) GO_TAGS=$(GO_TAGS) RGW_GO_FLAGS="$(RGW_GO_FLAGS)" go run ./hack/bench/gateway $(RELEASE) $(GATEWAY) $(PROFILES)

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
