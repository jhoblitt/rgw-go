# Phase 0 Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build everything rgw-go needs before it can serve a request: the Go scaffold, Ceph's wire encoding, RGW's stored types verified against the object corpus, the RADOS client seam over a go-ceph fork with three completion modes, five object-class packages, disposable Squid and Tentacle clusters with a radosgw oracle, and the phase 0 gate that every metadata object a radosgw wrote decodes and re-encodes byte-identically.

**Architecture:** Leaf packages first. `internal/denc` implements the encoding; `internal/meta` and `internal/acl` hold RGW's stored types on top of it; `internal/radosclient` is the seam whose only implementation is `internal/radosclient/goceph`; `internal/cls/<class>` packages marshal object-class calls over the seam's exec step and depend on nothing above them. Every encoder is proven by committed goldens generated once from ceph-dencoder, and the gate proves the whole stack against objects a real radosgw wrote.

**Tech Stack:** Go 1.27, cgo, ceph/go-ceph through the jhoblitt/go-ceph fork (librados C API), cobra, viper, log/slog, Ginkgo v2 and Gomega, counterfeiter, golangci-lint v2, podman or docker with quay.io/ceph/ceph images, ceph-dencoder from those images.

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md`, with `docs/exclusions.md` for scope, coexistence obligations and parity settings.

## Global Constraints

- Module `github.com/jhoblitt/rgw-go`; `go 1.27` in `go.mod`, minor only, no `toolchain` line.
- Layout per the Go canon: `cmd/rgw-go/main.go`, everything else under `internal/`; no `pkg/`, no `util`, `common` or `helpers` packages; package names one lowercase word.
- Tests are Ginkgo v2 and Gomega only, one `<pkg>_suite_test.go` per package with `RandomizeAllSpecs` and `FailOnPending`; test doubles are counterfeiter fakes generated beside the consumer; integration specs carry `//go:build integration` and `Label("integration")`.
- Logging is `log/slog`, JSON to stderr, static lowercase messages, attributes only. Errors wrap with `%w`, lowercase, unpunctuated. Context is the first parameter. Every goroutine has a stop condition.
- Dependency direction is one way: `denc` imports only the standard library; `cls/*` import `denc` and `radosclient` and each other, never `meta`, `acl` or `driver`; `meta` and `acl` import `denc`; `radosclient` imports nothing of ours; `radosclient/goceph` is the only package importing go-ceph.
- Encoding rule, from spec section 8: driver-level types decode every struct version the C++ decoders accept and encode at the version the cluster's release writes, selected by a `denc.Release` value; class requests encode at that release's version; class replies decode from the Squid version up. Byte-exactness with radosgw is the acceptance test for every encoder.
- Class packages call only methods registered by Squid's classes (v19.2.3).
- go-ceph is consumed through a `replace` to `github.com/jhoblitt/go-ceph` at a pinned commit, bumped by hand.
- Fork work follows go-ceph's conventions: new API in files with `//go:build ceph_preview`, one topic per file, tests in go-ceph's testify suite, commits `rados: <subject>` with a `Signed-off-by` trailer, `make api-update` before the PR.
- Commits are Conventional Commits with the two trailers used in this repository (`Co-Authored-By` and `Claude-Session`); every PR opens as a draft assigned to the author, CI is watched, and merges on green with a merge commit under the user's standing authorization.
- Every GitHub Action `uses:` is pinned to a 40-hex SHA with a `# vX.Y.Z` comment; `pinact run` and `actionlint` before committing a workflow.
- Never touch the ambient Kubernetes or Ceph cluster. All cluster work uses the disposable clusters this plan creates (`hack/cluster/up.sh`).
- Any material change to `docs/exclusions.md` is announced to the rgw-rs session.
- The librados headers on this machine are absent; until `librados-devel` is installed, cgo builds use `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include` and `CGO_LDFLAGS=-L<dir>` where `<dir>` holds a `librados.so` symlink to `/usr/lib64/librados.so.2`. Task 1 records this in the repository CLAUDE.md.

## Review Focus

1. **Object keys that start with `_`.** radosgw escapes them by prepending another `_` in both the head object name and the index key, and sets a locator equal to the oid. A name builder that treats `_` naively misplaces the object. Pinned in Task 4 (`ObjKey` tests for `_foo`).
2. **Zero-length values.** go-ceph's read and write steps take `&b[0]` and panic on an empty slice, and an empty xattr or a zero-byte object is legal in RGW. Pinned in Task 12 (seam integration tests write and read a zero-length object and an empty xattr).
3. **A stored struct at a version below its compat floor.** The 32-bit legacy framing has no length field for old versions, so a decoder that assumes one skips into the next field. Pinned in Task 2 (framing tests with a synthetic old-format buffer) and by the 15.2 and 16.2 corpus archives in Tasks 4 to 9.
4. **A cluster whose required OSD release is newer than the plan knows.** Release detection must map an unknown newer name to the newest known release with a warning, not fail closed. Pinned in Task 2 (`ParseRelease("umbrella")`) and Task 12 (detection from the OSD map).
5. **Bucket index headers at both Squid and main versions on one cluster.** The header gained a field at v8, and a Tentacle OSD re-encodes it at v8 while Squid corpus objects are v7. Pinned in Task 14 (decode both versions from goldens).

---

## File structure

```
cmd/rgw-go/main.go                       canon main
internal/cli/root.go                     cobra tree: serve (stub), version
internal/version/version.go              build info
internal/denc/                           encoder, decoder, framing, containers, time, release
internal/denc/goldentest/                shared golden-file test helper
internal/meta/                           RGW stored types: primitives, user, account, bucket, layout, zone, manifest, compression, cache notify
internal/acl/                            ACL policy types, encoding only in phase 0
internal/radosclient/                    seam: Cluster, Pool, ReadOp, WriteOp, results, errors, flags
internal/radosclient/goceph/             seam over the go-ceph fork, three completion modes
internal/cls/version/                    cls_version client
internal/cls/refcount/                   cls_refcount client
internal/cls/user/                       cls_user client
internal/cls/rgw/                        cls_rgw client (index, obj, usage, gc omap-era)
internal/cls/gc/                         cls_rgw_gc client (queue-era)
hack/cluster/entrypoint.sh               per-role container entrypoint (mon, mgr, osd, rgw)
hack/cluster/populate.sh                 writes users, buckets, objects; emits a manifest
hack/cluster/up.sh                       disposable Squid or Tentacle cluster with radosgw
hack/goldens/gen.sh                      regenerates goldens with ceph-dencoder from the Ceph image
hack/goldens/types.txt                   the types and destination packages gen.sh handles
test/gate/phase0_test.go                 the phase 0 gate, integration-tagged
.github/workflows/integration.yml        nightly and on-demand integration workflow
```

## Task index

| # | Task | Depends on |
|---|---|---|
| 1 | Go scaffold and repository hygiene | none |
| 2 | `denc`: encoding primitives and framing | 1 |
| 3 | Golden generation tooling and the shared golden test helper | 2 |
| 4 | `meta` primitives | 3 |
| 5 | `meta` user and account types | 4 |
| 6 | `meta` bucket types and layout | 4 |
| 7 | `meta` zone configuration types | 4 |
| 8 | `meta` object attribute types | 4 |
| 9 | `acl` types, encoding only | 4 |
| 10 | `radosclient` seam | 1 |
| 11 | go-ceph fork bindings | none (separate repository) |
| 12 | `radosclient/goceph` over the fork | 10, 11, 13 |
| 13 | Disposable clusters and the population script | 1 |
| 14 | Class packages: version, refcount, user, rgw, gc | 3, 10 |
| 15 | Phase 0 gate | 5 to 9, 12, 13, 14 |
| 16 | Integration workflow | 13, 15 |

Tasks 4 to 9 are independent of each other and of 10, 11 and 13, so they run in parallel. Task 11 runs in the go-ceph checkout and can start immediately.

---
### Task 1: Go scaffold and repository hygiene

**Files:**
- Create: `cmd/rgw-go/main.go`, `internal/cli/root.go`, `internal/cli/cli_suite_test.go`, `internal/cli/root_test.go`, `internal/version/version.go`, `go.mod`, `go.sum`, `.golangci.yml`, `Makefile`, `.goreleaser.yaml`, `.gitignore`, `CLAUDE.md`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.github/workflows/codeql.yml`
- Modify: `.github/dependabot.yml` (append the `gomod` entry; add a commit-message prefix), `README.md` (CI badge from `workflow-lint` to `ci`)

**Interfaces:**
- Produces: the module path `github.com/jhoblitt/rgw-go`; `internal/version.String()`; `internal/cli.Run(ctx, args, stdin, stdout, stderr) error` with subcommands `serve` (returns `errors.New("serve: not implemented in phase 0")`) and `version`.

The scaffold is the go-conventions `go-new-project` template table rendered by hand onto a branch, because the repository already exists and that skill's own flow assumes an empty tree. Templates live under `/home/jhoblitt/.claude-personal/plugins/cache/conventions-claude/go-conventions/1.2.1/skills/go-conventions/templates/`. Placeholders: `{{MODULE}}` = `github.com/jhoblitt/rgw-go`, `{{BINARY}}` = `rgw-go`, `{{DESCRIPTION}}` = `Ceph RADOS Gateway reimplemented in Go`, `{{ENV_PREFIX}}` = `RGW_GO`, `{{MODULES}}` = `.`, `{{OWNER}}` = `jhoblitt`, `{{REPO}}` = `rgw-go`, `{{PACKAGE}}` = `cli`, `{{PACKAGE_TITLE}}` = `Cli`.

- [ ] **Step 1: Initialize the module and render the templates**

```sh
go mod init github.com/jhoblitt/rgw-go
sed -i 's/^go .*/go 1.27/' go.mod && sed -i '/^toolchain /d' go.mod
T=/home/jhoblitt/.claude-personal/plugins/cache/conventions-claude/go-conventions/1.2.1/skills/go-conventions/templates
render() { sed -e 's|{{MODULE}}|github.com/jhoblitt/rgw-go|g' -e 's|{{BINARY}}|rgw-go|g' \
  -e 's|{{DESCRIPTION}}|Ceph RADOS Gateway reimplemented in Go|g' -e 's|{{ENV_PREFIX}}|RGW_GO|g' \
  -e 's|{{MODULES}}|.|g' -e 's|{{OWNER}}|jhoblitt|g' -e 's|{{REPO}}|rgw-go|g' \
  -e 's|{{PACKAGE_TITLE}}|Cli|g' -e 's|{{PACKAGE}}|cli|g' "$1" > "$2"; }
mkdir -p cmd/rgw-go internal/cli internal/version .github/workflows
render $T/main.go cmd/rgw-go/main.go
render $T/root.go internal/cli/root.go
render $T/version.go internal/version/version.go
render $T/suite_test.go internal/cli/cli_suite_test.go
render $T/.golangci.yml .golangci.yml
render $T/Makefile Makefile
render $T/.goreleaser.yaml .goreleaser.yaml
render $T/ci.yml .github/workflows/ci.yml
render $T/release.yml .github/workflows/release.yml
render $T/.gitignore .gitignore
render $T/CLAUDE-pointer.md CLAUDE.md
```

- [ ] **Step 2: Replace the template's greeting command with `serve` and `version`**

In `internal/cli/root.go`, delete the root `RunE` and the `--name` flag, and add two subcommands:

```go
func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the gateway (not implemented in phase 0)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("serve: not implemented in phase 0")
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return nil
		},
	}
}
```

Register both with `cmd.AddCommand(newServeCmd(), newVersionCmd())` in `newRootCmd`. Keep the persistent flags `--config`, `--log-level`, `--log-format` and `configure` exactly as the template has them.

- [ ] **Step 3: Write the first spec**

`internal/cli/root_test.go`, package `cli_test`:

```go
var _ = Describe("Run", func() {
	It("prints the version", func() {
		var out bytes.Buffer
		err := cli.Run(context.Background(), []string{"version"}, strings.NewReader(""), &out, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).NotTo(BeEmpty(), "version output")
	})
	It("refuses to serve in phase 0", func() {
		err := cli.Run(context.Background(), []string{"serve"}, strings.NewReader(""), io.Discard, io.Discard)
		Expect(err).To(MatchError(ContainSubstring("not implemented")))
	})
})
```

- [ ] **Step 4: Dependencies**

```sh
go get github.com/spf13/cobra@v1.10.2 github.com/spf13/viper@v1.21.0
go get github.com/onsi/ginkgo/v2@v2.32.1 github.com/onsi/gomega@v1.43.0
go get -tool github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2
go mod tidy
```

Module downloads need the sandbox disabled, because the module cache is read-only inside it.

- [ ] **Step 5: CI adjustments for cgo, CodeQL and Dependabot**

In `.github/workflows/ci.yml`, add to the `test`, `lint` and `checks` jobs, right after checkout, a step:

```yaml
      - name: Install librados headers
        run: |
          sudo apt-get update
          sudo apt-get install -y --no-install-recommends librados-dev
```

Create `.github/workflows/codeql.yml` from the github-conventions template at `/home/jhoblitt/.claude-personal/plugins/cache/conventions-claude/github-conventions/1.2.1/skills/github-conventions/templates/codeql.yml` with `{{CODEQL_LANGUAGES}}` = `go` and `{{CODEQL_BUILD_MODE}}` = `autobuild`, and add the same librados-dev step before the analysis step, since autobuild compiles cgo packages.

In `.github/dependabot.yml`, append the go-conventions `dependabot-gomod.yml` entry under `updates:`, and add to both entries:

```yaml
    commit-message:
      prefix: "chore(deps)"
```

This exists because Dependabot's first PR here used a sentence-case subject that commitlint rejects.

Then run `GITHUB_TOKEN=$(gh auth token) pinact run .github/workflows/*.yml` and `actionlint .github/workflows/*.yml`. If pinact cannot resolve a floating major tag, resolve the latest release with `gh api repos/<owner>/<repo>/releases/latest --jq .tag_name` and its commit with `gh api repos/<owner>/<repo>/commits/<tag> --jq .sha`, and pin by hand.

Change the README's first badge from `workflow-lint` to `ci`, both the image URL and the link.

- [ ] **Step 6: Repository CLAUDE.md**

Append below the go-conventions pointer block:

```markdown
## rgw-go facts

- cgo: every build and test compiles go-ceph and needs librados headers. On this machine
  `librados-devel` is absent; use `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include`
  and `CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go` where that directory holds
  `librados.so -> /usr/lib64/librados.so.2`. CI installs `librados-dev`.
- go-ceph comes from the jhoblitt/go-ceph fork through a `replace` in go.mod, pinned to a
  commit; Dependabot does not follow it, bump by hand.
- Never use the ambient kubectl or Ceph cluster. Cluster tests use `make cluster-up-squid`
  and `make cluster-up-tentacle` (hack/cluster/).
- Implementation tasks go to Opus 5.5 code-workers in worktrees; judgment stays on the
  session model.
- `docs/exclusions.md` is canonical for rgw-rs too: announce every material change to the
  rgw-rs Claude session.
- Design: docs/superpowers/specs/2026-09-25-rgw-go-design.md. Plans: docs/superpowers/plans/.
```

- [ ] **Step 7: Gate**

```sh
make tools
make check
```

Expected: every gate green. If `go test` fails to link because `-lrados` is not found, create the symlink directory named in the CLAUDE.md and export the two CGO variables.

- [ ] **Step 8: Commit as a series and open the PR**

Three commits: `feat: scaffold the Go module and CLI` (cmd, internal/cli, internal/version, go.mod, go.sum, .golangci.yml, Makefile, .goreleaser.yaml, .gitignore); `ci: add the Go CI, release and CodeQL workflows` (workflows, dependabot, README badge); `docs: record the repository facts in CLAUDE.md`. Push, open the draft PR assigned to the author, watch CI, merge on green.

---

### Task 2: `denc`, encoding primitives and framing

**Files:**
- Create: `internal/denc/doc.go`, `internal/denc/release.go`, `internal/denc/encoder.go`, `internal/denc/decoder.go`, `internal/denc/frame.go`, `internal/denc/containers.go`, `internal/denc/time.go`, `internal/denc/errors.go`, `internal/denc/denc_suite_test.go`, `internal/denc/encoder_test.go`, `internal/denc/decoder_test.go`, `internal/denc/frame_test.go`, `internal/denc/containers_test.go`, `internal/denc/release_test.go`

**Interfaces:**
- Produces, used by every later task:

```go
package denc

// Release selects which struct versions an encoder emits. Decoders accept every version.
type Release uint8

const (
	Squid Release = iota
	Tentacle
)

// ParseRelease maps a Ceph release name ("squid", "tentacle", "umbrella", ...) to a Release.
// A name newer than Tentacle maps to Tentacle with ok true; an unknown or older name
// returns ok false.
func ParseRelease(name string) (r Release, ok bool)

type Encoder struct{ /* buf []byte */ }

func NewEncoder() *Encoder
func (e *Encoder) Bytes() []byte
func (e *Encoder) Len() int
func (e *Encoder) U8(v uint8)
func (e *Encoder) U16(v uint16)
func (e *Encoder) U32(v uint32)
func (e *Encoder) U64(v uint64)
func (e *Encoder) I32(v int32)
func (e *Encoder) I64(v int64)
func (e *Encoder) Bool(v bool)
func (e *Encoder) String(s string)   // u32 length + bytes
func (e *Encoder) Bytes32(b []byte)  // bufferlist: u32 length + bytes
func (e *Encoder) Raw(b []byte)      // bytes, no prefix
func (e *Encoder) Time(t time.Time)  // u32 seconds + u32 nanoseconds (utime_t and real_time)
func (e *Encoder) BeginStruct(version, compat uint8) Frame
func (e *Encoder) EndStruct(f Frame)

type Frame struct{ /* start int */ }

// Decoder reads Ceph-encoded values. It records the first failure and returns zero
// values from every later call; callers check Err once at the end.
type Decoder struct{ /* buf []byte; off int; err error */ }

func NewDecoder(b []byte) *Decoder
func (d *Decoder) Err() error
func (d *Decoder) Remaining() int
func (d *Decoder) Offset() int
func (d *Decoder) U8() uint8
func (d *Decoder) U16() uint16
func (d *Decoder) U32() uint32
func (d *Decoder) U64() uint64
func (d *Decoder) I32() int32
func (d *Decoder) I64() int64
func (d *Decoder) Bool() bool
func (d *Decoder) String() string
func (d *Decoder) Bytes32() []byte   // a copy
func (d *Decoder) Raw(n int) []byte  // a copy
func (d *Decoder) Time() time.Time
func (d *Decoder) BeginStruct(maxVersion uint8) Header
func (d *Decoder) BeginStructLegacy(maxVersion, compatVersion, lenVersion uint8, skip int) Header
func (d *Decoder) EndStruct(h Header)
func (d *Decoder) Fail(err error)    // record an error raised by a caller, e.g. a bad enum value

type Header struct {
	Version uint8
	Compat  uint8
	// end: the offset where the struct ends, or 0 when the encoding carried no length.
}

var (
	ErrShortBuffer  = errors.New("denc: short buffer")
	ErrIncompatible = errors.New("denc: struct compat version newer than decoder")
	ErrOverread     = errors.New("denc: decoded past struct end")
)

func EncodeSlice[T any](e *Encoder, xs []T, enc func(*Encoder, T))
func DecodeSlice[T any](d *Decoder, dec func(*Decoder) T) []T
func EncodeMap[K cmp.Ordered, V any](e *Encoder, m map[K]V, encK func(*Encoder, K), encV func(*Encoder, V)) // keys in sorted order
func DecodeMap[K comparable, V any](d *Decoder, decK func(*Decoder) K, decV func(*Decoder) V) map[K]V   // first duplicate wins
func EncodeStringMap(e *Encoder, m map[string][]byte)   // the attrs shape: sorted keys, Bytes32 values
func DecodeStringMap(d *Decoder) map[string][]byte
func EncodeOptional[T any](e *Encoder, v *T, enc func(*Encoder, T))  // u8 presence then value
func DecodeOptional[T any](d *Decoder, dec func(*Decoder) T) *T
```

Wire facts this task implements, all verified against `src/include/encoding.h` on Ceph main:

- Every integer is little-endian. `bool` is one byte, 0 or 1; the decoder accepts any non-zero as true.
- `string` and bufferlist are a u32 length then the bytes, no terminator. Containers are a u32 count then the elements; `std::map` in key order; `std::optional` is a u8 flag then the value when present.
- `utime_t` and `ceph::real_time` are u32 seconds then u32 nanoseconds. `std::chrono::duration` is i32 seconds then i32 nanoseconds and appears nowhere in scope.
- Standard framing: `[u8 struct_v][u8 struct_compat][u32 struct_len][payload]`, `struct_len` counting payload bytes after the six-byte header. Decoding fails with `ErrIncompatible` when `struct_compat > maxVersion`, with `ErrShortBuffer` when `struct_len` exceeds the remaining bytes; `EndStruct` skips forward to the end when the decoder read less and fails with `ErrOverread` when it read more; an end of 0 means no length was present and `EndStruct` does nothing.
- Legacy compat-length framing, `DECODE_START_LEGACY_COMPAT_LEN(v, compatv, lenv)`: read u8 `struct_v`; if `compatv <= struct_v` read u8 `struct_compat` and apply the compat check; if `skip != 0 && compatv > struct_v` skip `skip` bytes (3 for the `_32` variant, whose old format wrote a u32 version); if `lenv > struct_v` there is no length and end is 0; otherwise read u32 `struct_len` and set end. Encoders always emit the standard six-byte header regardless of which decoder macro the type uses.
- Nothing in scope uses feature-dependent encoding, `encode_nohead`, or DENC framing.

- [ ] **Step 1: Write failing encoder and decoder primitive tests**

`internal/denc/encoder_test.go`, package `denc_test`, dot-importing Ginkgo and Gomega:

```go
var _ = Describe("Encoder primitives", func() {
	DescribeTable("encode to Ceph's little-endian layout",
		func(write func(*denc.Encoder), want []byte) {
			e := denc.NewEncoder()
			write(e)
			Expect(e.Bytes()).To(Equal(want))
		},
		Entry("u8", func(e *denc.Encoder) { e.U8(0xAB) }, []byte{0xAB}),
		Entry("u16", func(e *denc.Encoder) { e.U16(0x1234) }, []byte{0x34, 0x12}),
		Entry("u32", func(e *denc.Encoder) { e.U32(0x01020304) }, []byte{4, 3, 2, 1}),
		Entry("u64", func(e *denc.Encoder) { e.U64(0x0102030405060708) }, []byte{8, 7, 6, 5, 4, 3, 2, 1}),
		Entry("bool true", func(e *denc.Encoder) { e.Bool(true) }, []byte{1}),
		Entry("string", func(e *denc.Encoder) { e.String("ab") }, []byte{2, 0, 0, 0, 'a', 'b'}),
		Entry("empty string", func(e *denc.Encoder) { e.String("") }, []byte{0, 0, 0, 0}),
		Entry("bufferlist", func(e *denc.Encoder) { e.Bytes32([]byte{9}) }, []byte{1, 0, 0, 0, 9}),
		Entry("time", func(e *denc.Encoder) { e.Time(time.Unix(0x01020304, 0x05060708).UTC()) }, []byte{4, 3, 2, 1, 8, 7, 6, 5}),
	)
})
```

`internal/denc/decoder_test.go` mirrors each entry through `NewDecoder(want)` and asserts the value and that `Err()` is nil, plus:

```go
	It("records a short buffer once and returns zero values afterwards", func() {
		d := denc.NewDecoder([]byte{1, 0})
		Expect(d.U32()).To(BeZero())
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		Expect(d.U8()).To(BeZero(), "calls after the first error return zero values")
	})
	It("accepts any non-zero byte as true", func() {
		Expect(denc.NewDecoder([]byte{7}).Bool()).To(BeTrue())
	})
```

`internal/denc/release_test.go`:

```go
var _ = DescribeTable("ParseRelease",
	func(name string, want denc.Release, wantOK bool) {
		got, ok := denc.ParseRelease(name)
		Expect(ok).To(Equal(wantOK), "name %q", name)
		Expect(got).To(Equal(want), "name %q", name)
	},
	Entry("squid", "squid", denc.Squid, true),
	Entry("tentacle", "tentacle", denc.Tentacle, true),
	Entry("newer than known maps to the newest known", "umbrella", denc.Tentacle, true),
	Entry("uppercase", "Squid", denc.Squid, true),
	Entry("older than the floor", "reef", denc.Squid, false),
	Entry("unknown", "banana", denc.Squid, false),
)
```

- [ ] **Step 2: Run to verify the suite fails to compile**

```sh
go test ./internal/denc/
```
Expected: FAIL, undefined `denc.NewEncoder` and friends.

- [ ] **Step 3: Implement `encoder.go`, `decoder.go`, `errors.go`, `time.go`, `release.go`**

The encoder appends with `binary.LittleEndian.AppendUint32` and friends. The decoder checks `d.off+n <= len(d.buf)` before every read and sets `d.err = fmt.Errorf("%w: need %d bytes at offset %d", ErrShortBuffer, n, d.off)` on the first failure. `Time` decodes seconds as u32 into `time.Unix(int64(sec), int64(nsec)).UTC()`. `ParseRelease` lowercases the name and looks it up in an ordered table of Ceph release names from `reef` through `tentacle` plus the known newer names (`umbrella`); a name at or after `squid` maps to itself or, if newer than Tentacle, to Tentacle; `reef` and earlier return ok false; unknown names return ok false.

- [ ] **Step 4: Run the primitive tests**

```sh
go test ./internal/denc/ -v 2>&1 | tail -20
```
Expected: PASS for every primitive and release entry.

- [ ] **Step 5: Write failing framing tests**

`internal/denc/frame_test.go`:

```go
var _ = Describe("struct framing", func() {
	It("writes the six-byte header with the payload length patched in", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(3, 1)
		e.U32(0xAABBCCDD)
		e.EndStruct(f)
		Expect(e.Bytes()).To(Equal([]byte{3, 1, 4, 0, 0, 0, 0xDD, 0xCC, 0xBB, 0xAA}))
	})
	It("skips unknown trailing fields written by a newer encoder", func() {
		d := denc.NewDecoder([]byte{5, 1, 8, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0})
		h := d.BeginStruct(3)
		Expect(h.Version).To(Equal(uint8(5)))
		Expect(d.U32()).To(Equal(uint32(1)))
		d.EndStruct(h)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.Remaining()).To(BeZero(), "the second u32 was skipped")
	})
	It("refuses a compat version newer than the decoder", func() {
		d := denc.NewDecoder([]byte{9, 7, 0, 0, 0, 0})
		d.BeginStruct(3)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
	It("fails when the decoder reads past the struct end", func() {
		d := denc.NewDecoder([]byte{1, 1, 1, 0, 0, 0, 0xFF, 0xEE})
		h := d.BeginStruct(1)
		d.U16()
		d.EndStruct(h)
		Expect(d.Err()).To(MatchError(denc.ErrOverread))
	})
	Describe("legacy compat-length framing", func() {
		It("reads the compat byte and length when struct_v is at or above compatv", func() {
			d := denc.NewDecoder([]byte{10, 3, 1, 0, 0, 0, 0x42})
			h := d.BeginStructLegacy(10, 3, 3, 0)
			Expect(h.Version).To(Equal(uint8(10)))
			Expect(d.U8()).To(Equal(uint8(0x42)))
			d.EndStruct(h)
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("reads neither compat nor length for an old struct_v", func() {
			d := denc.NewDecoder([]byte{2, 0x42})
			h := d.BeginStructLegacy(10, 3, 3, 0)
			Expect(h.Version).To(Equal(uint8(2)))
			Expect(d.U8()).To(Equal(uint8(0x42)), "payload follows the version byte directly")
			d.EndStruct(h)
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("skips the three high bytes of an old 32-bit version", func() {
			d := denc.NewDecoder([]byte{2, 0, 0, 0, 0x42})
			h := d.BeginStructLegacy(23, 9, 9, 3)
			Expect(h.Version).To(Equal(uint8(2)))
			Expect(d.U8()).To(Equal(uint8(0x42)))
			Expect(d.Err()).NotTo(HaveOccurred())
		})
	})
})
```

- [ ] **Step 6: Run to verify they fail, implement `frame.go`, run to verify they pass**

```sh
go test ./internal/denc/ -v 2>&1 | grep -E 'framing|PASS|FAIL' | tail -20
```

- [ ] **Step 7: Containers and the attrs map**

Tests in `containers_test.go`: a slice of strings encodes as count then elements; a `map[string]uint32` encodes keys in sorted order; `DecodeMap` keeps the first value for a duplicated key; `EncodeOptional` with nil writes a single 0 byte and with a value writes 1 then the value; `EncodeStringMap` of `{"b": {1}, "a": {}}` yields `02 00 00 00 | 01 00 00 00 'a' | 00 00 00 00 | 01 00 00 00 'b' | 01 00 00 00 01`. Implement `containers.go` with Go 1.27 generics. Run, expect PASS.

- [ ] **Step 8: Lint and commit**

Run `make check`, then commit `internal/denc` as `feat(denc): add Ceph wire encoding primitives and struct framing`.

---

### Task 3: Golden generation tooling and the shared golden test helper

**Files:**
- Create: `hack/goldens/gen.sh`, `hack/goldens/types.txt`, `internal/denc/goldentest/goldentest.go`, `internal/denc/goldentest/goldentest_suite_test.go`, `internal/denc/goldentest/goldentest_test.go`
- Modify: `Makefile` (add a `goldens` target that runs `hack/goldens/gen.sh`)

**Interfaces:**
- Produces:

```go
package goldentest

// Case is one corpus object of one type: its stored bytes, ceph-dencoder's JSON dump of it,
// and ceph-dencoder's re-encoding of it at the dencoder's own version.
type Case struct {
	Type    string
	Archive string // corpus archive directory name, e.g. "19.2.0-404-g78ddc7f9027"
	Name    string // corpus object file name
	Bin     []byte
	JSON    []byte
	ReEnc   []byte
}

// Load returns every case under dir/goldens/<Type>/<Archive>/<Name>.{bin,json,reenc}.
func Load(dir, typ string) ([]Case, error)

// Options tune a round-trip check for one type.
type Options struct {
	// Release is the release whose version the encoder emits; it must match the dencoder
	// that produced ReEnc (Squid for goldens generated from the v19 image).
	Release denc.Release
	// Nondeterministic marks a type whose C++ encoding is not byte-stable (unordered_map);
	// ReEnc is then compared by decoding both sides instead of by bytes.
	Nondeterministic bool
	// SkipJSON skips the JSON comparison for types whose Go JSON form need not match.
	SkipJSON bool
}

// RoundTrip decodes every case with decode, re-encodes with encode, and asserts the bytes
// equal ReEnc; when the case's JSON is present and not skipped, marshals the decoded value
// and asserts it equals the dencoder JSON after both are canonicalized. It uses Gomega
// assertions and is called from inside an It.
func RoundTrip[T any](dir, typ string, o Options, decode func(*denc.Decoder) T, encode func(*denc.Encoder, T, denc.Release))
```

- [ ] **Step 1: Determine where ceph-dencoder lives**

```sh
podman run --rm quay.io/ceph/ceph:v19.2.6 sh -c 'command -v ceph-dencoder || dnf -q provides "*/ceph-dencoder" 2>/dev/null | head'
```

Record the outcome in the header comment of `hack/goldens/gen.sh`. If the binary is absent from the image, the script builds a derived image `localhost/rgw-go-dencoder:v19.2.6` from `quay.io/ceph/ceph:v19.2.6` that installs the package the query named, and uses that image.

- [ ] **Step 2: Write `hack/goldens/gen.sh`**

```sh
#!/usr/bin/env bash
# Regenerates golden files from the ceph-object-corpus with ceph-dencoder run inside the
# Ceph image, so `go test` never needs the binary. Usage: hack/goldens/gen.sh [types.txt]
set -euo pipefail
CORPUS=${CORPUS:-/home/jhoblitt/github/ceph/ceph-object-corpus/archive}
IMAGE=${IMAGE:-quay.io/ceph/ceph:v19.2.6}
TYPES=${1:-hack/goldens/types.txt}
while read -r typ pkgdir; do
  [ -z "$typ" ] && continue
  for archive in "$CORPUS"/*/; do
    a=$(basename "$archive")
    src="$archive/objects/$typ"
    [ -d "$src" ] || continue
    dst="$pkgdir/testdata/goldens/$typ/$a"; mkdir -p "$dst"
    for f in "$src"/*; do
      n=$(basename "$f")
      cp "$f" "$dst/$n.bin"
      podman run --rm -v "$src:/in:ro,Z" "$IMAGE" ceph-dencoder type "$typ" import "/in/$n" decode dump_json > "$dst/$n.json"
      podman run --rm -v "$src:/in:ro,Z" -v "$dst:/out:Z" "$IMAGE" ceph-dencoder type "$typ" import "/in/$n" decode encode export "/out/$n.reenc"
    done
  done
done < "$TYPES"
```

`hack/goldens/types.txt` lists `<C++ type> <package dir>` pairs, one per line, for every type Tasks 4 to 9 and 14 decode, for example `RGWUserInfo internal/meta` and `rgw_bucket_dir_entry internal/cls/rgw`. Goldens are committed; the script runs by hand and through `make goldens`.

- [ ] **Step 3: Write the helper's failing test, then the helper**

`goldentest_test.go` creates a temporary `goldens/T/arch/` with `a.bin` = `{1,1,1,0,0,0,7}`, `a.reenc` identical and `a.json` = `{"v":7}`, defines `type T struct{ V uint8 }` with JSON tag `v` and decode and encode functions using `BeginStruct(1)` and `BeginStruct(1,1)`, runs `RoundTrip` and expects no failure; then rewrites `a.reenc` to end in 8 and expects a failure through `InterceptGomegaFailures`. Implement `Load` and `RoundTrip`; JSON canonicalization is `json.Unmarshal` into `any` on both sides followed by `Equal`.

- [ ] **Step 4: Generate the first goldens and commit**

Run the script for `rgw_user`, `rgw_bucket` and `RGWUserInfo` only, confirm the files appear under `internal/meta/testdata/goldens/`, run `make check`, and commit `hack/goldens`, `internal/denc/goldentest`, the Makefile change and the goldens as `feat(goldens): generate and check ceph-dencoder goldens from the object corpus`.

---
### Task 4: `meta` primitives

**Files:**
- Create: `internal/meta/doc.go`, `internal/meta/time.go`, `internal/meta/user_id.go`, `internal/meta/bucket_id.go`, `internal/meta/pool.go`, `internal/meta/placement.go`, `internal/meta/objkey.go`, `internal/meta/rawobj.go`, `internal/meta/quota.go`, `internal/meta/meta_suite_test.go`, `internal/meta/primitives_test.go`, `internal/meta/goldens_test.go`
- Goldens: run `hack/goldens/gen.sh` with `rgw_user`, `rgw_bucket`, `rgw_pool`, `rgw_placement_rule`, `rgw_obj_key`, `rgw_obj`, `rgw_raw_obj`, `RGWQuotaInfo` mapped to `internal/meta`

**Interfaces:**
- Produces:

```go
package meta

// Time is a time.Time whose JSON form is RGW's dump format, "2006-01-02T15:04:05.000000Z".
type Time struct{ time.Time }
func (t Time) MarshalJSON() ([]byte, error)
func (t *Time) UnmarshalJSON(b []byte) error
func (t Time) Encode(e *denc.Encoder)
func DecodeTime(d *denc.Decoder) Time

// UserID is rgw_user: tenant, id and namespace.
type UserID struct {
	Tenant string `json:"tenant"`
	ID     string `json:"id"`
	NS     string `json:"ns"`
}
func ParseUserID(s string) UserID            // "id", "tenant$id", "tenant$ns$id", "$ns$id"
func (u UserID) String() string              // the inverse, rgw_user::to_str
func (u UserID) Encode(e *denc.Encoder, r denc.Release)
func DecodeUserID(d *denc.Decoder) UserID

// BucketID is rgw_bucket.
type BucketID struct {
	Tenant            string
	Name              string
	Marker            string
	ID                string // bucket_id
	ExplicitPlacement DataPlacement
}
type DataPlacement struct{ DataPool, DataExtraPool, IndexPool Pool }
func (b BucketID) Encode(e *denc.Encoder, r denc.Release)
func DecodeBucketID(d *denc.Decoder) BucketID
func (b BucketID) EntryPointOID() string     // "<name>" or "<tenant>/<name>"
func (b BucketID) InstanceOID() string       // ".bucket.meta.<name>:<id>" or ".bucket.meta.<tenant>:<name>:<id>"

// Pool is rgw_pool: a pool name and a RADOS namespace, "name" or "name:ns".
type Pool struct{ Name, NS string }
func ParsePool(s string) Pool
func (p Pool) String() string
func (p Pool) Encode(e *denc.Encoder, r denc.Release)
func DecodePool(d *denc.Decoder) Pool

// PlacementRule is rgw_placement_rule: "name" or "name/storage_class", STANDARD omitted.
type PlacementRule struct{ Name, StorageClass string }
func ParsePlacementRule(s string) PlacementRule
func (p PlacementRule) String() string
func (p PlacementRule) Encode(e *denc.Encoder, r denc.Release)   // a single string, no header
func DecodePlacementRule(d *denc.Decoder) PlacementRule

// ObjKey is rgw_obj_key: name, version instance and namespace.
type ObjKey struct{ Name, Instance, NS string }
func (k ObjKey) Encode(e *denc.Encoder, r denc.Release)
func DecodeObjKey(d *denc.Decoder) ObjKey
func (k ObjKey) IndexKeyName() string   // rgw_obj_key::get_index_key_name
func (k ObjKey) OID() string            // rgw_obj_key::get_oid
func (k ObjKey) Locator() string        // rgw_obj_key::get_loc; "" when none

// Obj is rgw_obj: a bucket and a key.
type Obj struct{ Bucket BucketID; Key ObjKey }
func (o Obj) Encode(e *denc.Encoder, r denc.Release)
func DecodeObj(d *denc.Decoder) Obj

// RawObj is rgw_raw_obj: pool, oid, locator.
type RawObj struct{ Pool Pool; OID, Loc string }
func (o RawObj) Encode(e *denc.Encoder, r denc.Release)
func DecodeRawObj(d *denc.Decoder) RawObj

// Quota is RGWQuotaInfo.
type Quota struct {
	MaxSize    int64 `json:"max_size"`
	MaxObjects int64 `json:"max_objects"`
	Enabled    bool  `json:"enabled"`
	CheckOnRaw bool  `json:"check_on_raw"`
}
func (q Quota) Encode(e *denc.Encoder, r denc.Release)
func DecodeQuota(d *denc.Decoder) Quota
```

C++ sources, field order to mirror exactly (read the `encode` body and the version-conditional `decode` body of each; the plan names the file and the framing, the worker transcribes the fields):

| Type | Encode | Decode framing | Source |
|---|---|---|---|
| rgw_user | (2,1) | standard | `src/rgw/rgw_user_types.h:60` |
| rgw_bucket | (10,10) | legacy compat-len (10,3,3) | `src/rgw/rgw_bucket_types.h:84` and `:100` |
| rgw_pool | (10,10) | legacy compat-len (10,3,3) | `src/rgw/rgw_pool_types.h:64` and `:73` |
| rgw_placement_rule | none | none | `src/rgw/rgw_placement_types.h:79-83` |
| rgw_obj_key | (2,1) | standard | `src/rgw/rgw_obj_types.h:367` |
| rgw_obj | (6,6) | legacy compat-len (6,3,3) | `src/rgw/rgw_obj_types.h:553` and `:563` |
| rgw_raw_obj | (6,6) | standard | `src/rgw/rgw_obj_types.h:435` |
| RGWQuotaInfo | (3,1) | legacy compat-len (3,1,1) | `src/rgw/rgw_quota_types.h:46` and `:59` |

Name rules, verified in `src/rgw/rgw_obj_types.h:192-255` and `src/rgw/rgw_common.h:2285-2306`: `IndexKeyName` is the name unchanged unless it starts with `_`, in which case one `_` is prepended; a namespaced key is `_<ns>_<name>`. `OID` is the plain name, `_` + name when it starts with `_`, or `_<ns>[:<instance>]_<name>` when namespaced or versioned, with the null instance omitted. `Locator` is `<marker>_<name>` only when the name starts with `_` and the namespace is empty; the head object name is `<marker>_<OID>` and is built by the driver in phase 1.

- [ ] **Step 1: Write failing tests for the name rules and string forms**

`internal/meta/primitives_test.go`:

```go
var _ = Describe("ObjKey names", func() {
	DescribeTable("index key, oid and locator",
		func(k meta.ObjKey, indexKey, oid string) {
			Expect(k.IndexKeyName()).To(Equal(indexKey), "index key of %+v", k)
			Expect(k.OID()).To(Equal(oid), "oid of %+v", k)
		},
		Entry("plain", meta.ObjKey{Name: "foo"}, "foo", "foo"),
		Entry("leading underscore is escaped", meta.ObjKey{Name: "_foo"}, "__foo", "__foo"),
		Entry("namespaced", meta.ObjKey{Name: "foo", NS: "multipart"}, "_multipart_foo", "_multipart_foo"),
		Entry("versioned", meta.ObjKey{Name: "foo", Instance: "v1"}, "foo", "_:v1_foo"),
		Entry("namespaced and versioned", meta.ObjKey{Name: "foo", NS: "shadow", Instance: "v1"}, "_shadow_foo", "_shadow:v1_foo"),
	)
	It("sets a locator only for an escaped name outside a namespace", func() {
		Expect(meta.ObjKey{Name: "_foo"}.Locator()).To(Equal("_foo"))
		Expect(meta.ObjKey{Name: "foo"}.Locator()).To(BeEmpty())
		Expect(meta.ObjKey{Name: "_foo", NS: "shadow"}.Locator()).To(BeEmpty())
	})
})

var _ = DescribeTable("UserID string forms round-trip",
	func(s string, want meta.UserID) {
		got := meta.ParseUserID(s)
		Expect(got).To(Equal(want), "parse %q", s)
		Expect(got.String()).To(Equal(s), "format %q", s)
	},
	Entry("bare id", "alice", meta.UserID{ID: "alice"}),
	Entry("tenant and id", "t1$alice", meta.UserID{Tenant: "t1", ID: "alice"}),
	Entry("tenant, namespace and id", "t1$oidc$alice", meta.UserID{Tenant: "t1", NS: "oidc", ID: "alice"}),
	Entry("namespace without tenant", "$oidc$alice", meta.UserID{NS: "oidc", ID: "alice"}),
)

var _ = DescribeTable("PlacementRule string forms",
	func(s string, want meta.PlacementRule) {
		Expect(meta.ParsePlacementRule(s)).To(Equal(want))
		Expect(want.String()).To(Equal(s))
	},
	Entry("name only", "default-placement", meta.PlacementRule{Name: "default-placement"}),
	Entry("name and class", "default-placement/COLD", meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}),
)

var _ = It("Pool splits name and namespace at the colon", func() {
	Expect(meta.ParsePool("default.rgw.meta:root")).To(Equal(meta.Pool{Name: "default.rgw.meta", NS: "root"}))
	Expect(meta.Pool{Name: "default.rgw.control"}.String()).To(Equal("default.rgw.control"))
})

var _ = It("Time marshals in RGW's dump format", func() {
	t := meta.Time{Time: time.Date(2026, 9, 25, 20, 1, 2, 345678000, time.UTC)}
	b, err := json.Marshal(t)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(b)).To(Equal(`"2026-09-25T20:01:02.345678Z"`))
})
```

- [ ] **Step 2: Write the golden round-trip spec**

`internal/meta/goldens_test.go`:

```go
var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("rgw_user", func() {
		goldentest.RoundTrip(dir, "rgw_user", goldentest.Options{Release: denc.Squid}, meta.DecodeUserID,
			func(e *denc.Encoder, v meta.UserID, r denc.Release) { v.Encode(e, r) })
	})
	It("rgw_bucket", func() {
		goldentest.RoundTrip(dir, "rgw_bucket", goldentest.Options{Release: denc.Squid, SkipJSON: true}, meta.DecodeBucketID,
			func(e *denc.Encoder, v meta.BucketID, r denc.Release) { v.Encode(e, r) })
	})
	// one It per type in this task's golden list, same shape
})
```

JSON is compared for the types whose JSON form the admin API exposes (`rgw_user` in isolation is compared; `rgw_bucket` is skipped here because the dencoder dumps it through a container the Go type does not mirror one to one; compare it in Task 6 inside RGWBucketInfo instead).

- [ ] **Step 3: Run to verify failure, implement, run to verify pass**

Implement each type by transcribing the C++ encode order. For the legacy-framed types call `d.BeginStructLegacy(v, compatv, lenv, 0)` with the table's arguments and gate each field on `h.Version` exactly as the C++ `decode` does (for example `rgw_bucket` reads `data_pool`, `marker`, `bucket_id`, `index_pool`, `data_extra_pool` at the old versions before `explicit_placement` took over). Where the C++ discards a legacy field, decode and drop it. Encoders write the current version's fields only.

```sh
go test ./internal/meta/ -v 2>&1 | tail -30
```

- [ ] **Step 4: Commit**

`feat(meta): add RGW identity and placement primitives with corpus goldens`.

---

### Task 5: `meta` user and account types

**Files:**
- Create: `internal/meta/user.go`, `internal/meta/account.go`, `internal/meta/user_test.go`
- Goldens: `RGWAccessKey`, `RGWSubUser`, `RGWUserCaps`, `RGWUserInfo`, `RGWUID`, `RGWAccountInfo` mapped to `internal/meta`

**Interfaces:**
- Consumes: Task 4's `UserID`, `Quota`, `Time`, `PlacementRule`.
- Produces:

```go
type AccessKey struct {
	ID        string `json:"access_key"`
	Secret    string `json:"secret_key"`
	Subuser   string `json:"subuser"`
	Active    bool   `json:"active"`
	CreatedAt Time   `json:"create_date"`
}
type SubUser struct{ Name string; Perm uint32 }
type Caps map[string]uint32                     // RGWUserCaps: type -> perm bits
type UserInfo struct { /* every RGWUserInfo field, JSON tags from RGWUserInfo::dump */ }
type UID string                                 // RGWUID: the bare string form of a user id
type AccountInfo struct { /* every RGWAccountInfo field */ }

func (k AccessKey) Encode(e *denc.Encoder, r denc.Release)
func DecodeAccessKey(d *denc.Decoder) AccessKey
func (s SubUser) Encode(e *denc.Encoder, r denc.Release)
func DecodeSubUser(d *denc.Decoder) SubUser
func (c Caps) Encode(e *denc.Encoder, r denc.Release)
func DecodeCaps(d *denc.Decoder) Caps
func (u UserInfo) Encode(e *denc.Encoder, r denc.Release)
func DecodeUserInfo(d *denc.Decoder) UserInfo
func (u UID) Encode(e *denc.Encoder, r denc.Release)     // u32 length + bytes, no struct header
func DecodeUID(d *denc.Decoder) UID
func (a AccountInfo) Encode(e *denc.Encoder, r denc.Release)
func DecodeAccountInfo(d *denc.Decoder) AccountInfo

// UserObject is the payload of the users.uid object: RGWUID followed by RGWUserInfo.
type UserObject struct{ UID UID; Info UserInfo }
func (o UserObject) Encode(e *denc.Encoder, r denc.Release)
func DecodeUserObject(d *denc.Decoder) UserObject
```

| Type | Encode | Decode framing | Source |
|---|---|---|---|
| RGWAccessKey | (4,2) | legacy 32-bit (4,2,2) | `src/rgw/rgw_acl_types.h:56` and `:66` |
| RGWSubUser | (2,2) | legacy 32-bit (2,2,2) | `:94` and `:101` |
| RGWUserCaps | (1,1) | standard | `:127` |
| RGWUserInfo | (23,9) | legacy 32-bit (23,9,9) | `src/rgw/rgw_common.h:678` and `:733` |
| RGWUID | none | none | `src/rgw/driver/rados/rgw_user.h:49-73` |
| RGWAccountInfo | (2,1) | standard | `src/rgw/rgw_common.h:863` |

`RGWUserInfo` has more than twenty version-gated fields including a `multimap<string,string>` of tags (u32 count then pairs in key order, equal keys in insertion order), embedded `RGWQuotaInfo` twice, the access key and swift key maps keyed by id, the subuser map, caps, and the account id and path added at v22 and v23. The JSON field names are `RGWUserInfo::dump` in `src/rgw/rgw_json_enc.cc`; the golden JSON check enforces them. The users.uid object stores `RGWUID` then `RGWUserInfo` back to back (`src/rgw/services/svc_user_rados.cc:281-289`).

- [ ] **Step 1: Goldens** for the six types. **Step 2:** write the round-trip specs, one `It` per type, with JSON compared for `RGWUserInfo`, `RGWAccessKey`, `RGWAccountInfo`. **Step 3:** run, expect failure. **Step 4:** implement by transcription. **Step 5:** run to pass; add a unit spec that `UserObject` of a fresh `UserInfo` encodes to `UID` bytes followed by the info bytes. **Step 6:** commit `feat(meta): add user, key, caps and account types with corpus goldens`.

---

### Task 6: `meta` bucket types and layout

**Files:**
- Create: `internal/meta/bucket_entrypoint.go`, `internal/meta/bucket_info.go`, `internal/meta/bucket_layout.go`, `internal/meta/owner.go`, `internal/meta/bucket_ent.go`, `internal/meta/bucket_test.go`
- Goldens: `RGWBucketEntryPoint`, `RGWBucketInfo`, `RGWBucketEnt` mapped to `internal/meta`

**Interfaces:**
- Produces:

```go
// Owner is rgw_owner: a user or an account id.
type Owner struct{ User *UserID; Account string }
func (o Owner) String() string                                   // to_string(rgw_owner)
// EncodeConverted writes the converted_variant form used by RGWBucketEntryPoint: a user is
// the bare rgw_user encoding, an account is a u8 129 marker then the string.
func (o Owner) EncodeConverted(e *denc.Encoder, r denc.Release)
func DecodeOwnerConverted(d *denc.Decoder) Owner
// EncodeVersioned writes the versioned_variant form used by RGWBucketInfo: a struct header
// whose version and compat are the variant index, then the alternative's encoding.
func (o Owner) EncodeVersioned(e *denc.Encoder, r denc.Release)
func DecodeOwnerVersioned(d *denc.Decoder) Owner

type BucketEntryPoint struct {
	Bucket        BucketID
	Owner         Owner
	CreationTime  Time
	Linked        bool
	HasBucketInfo bool
	// OldBucketInfo is decoded and dropped for versions that embedded it.
}

type IndexLayoutGen struct{ Gen uint64; Layout IndexLayout }
type IndexLayout struct{ Type uint8; Normal IndexNormalLayout }
type IndexNormalLayout struct{ NumShards uint32; HashType uint8; MinNumShards uint32 } // MinNumShards is v2, main only
type LogLayoutGen struct{ Gen uint64; Layout LogLayout }
type LogLayout struct{ Type uint8; InIndex IndexLayoutGen; FIFO FIFOLogLayout }
type FIFOLogLayout struct{ NumShards uint32 }
type BucketLayout struct {
	Resharding          uint8
	Current             IndexLayoutGen
	Target              *IndexLayoutGen
	Logs                []LogLayoutGen
	JudgeReshardLockTime Time // v3, main only
}

type BucketInfo struct { /* every RGWBucketInfo field: Bucket, Owner, Flags, Zonegroup, CreationTime, PlacementRule, HasInstanceObj, Quota, RequesterPays, HasWebsite, Website, SwiftVersioning, SwiftVerLocation, MDSearchConfig, Reshard, NewBucketInstanceID, ObjLock, SyncPolicy, Layout, ... */ }
type BucketEnt struct { /* RGWBucketEnt: Bucket, Size, SizeRounded, CreationTime, Count, PlacementRule */ }

func (b BucketEntryPoint) Encode(e *denc.Encoder, r denc.Release)
func DecodeBucketEntryPoint(d *denc.Decoder) BucketEntryPoint
func (l BucketLayout) Encode(e *denc.Encoder, r denc.Release)    // (2,1) on Squid, (3,1) on Tentacle
func DecodeBucketLayout(d *denc.Decoder) BucketLayout
func (b BucketInfo) Encode(e *denc.Encoder, r denc.Release)
func DecodeBucketInfo(d *denc.Decoder) BucketInfo
func (b BucketEnt) Encode(e *denc.Encoder, r denc.Release)
func DecodeBucketEnt(d *denc.Decoder) BucketEnt

// IndexShardOID names one index shard: ".dir.<id>" when the layout has no shards,
// ".dir.<id>.<shard>" for generation 0, ".dir.<id>.<gen>.<shard>" otherwise.
func (b BucketInfo) IndexShardOID(gen IndexLayoutGen, shard uint32) string
// IndexShard selects the shard for an index key name: ceph_str_hash_linux, then
// h ^= (h & 0xFF) << 24, then (h % 7877) % num_shards (65521 replaces 7877 above 7877 shards).
func IndexShard(indexKeyName string, numShards uint32) uint32
```

| Type | Encode | Decode framing | Source |
|---|---|---|---|
| RGWBucketEntryPoint | (10,8) | legacy 32-bit (10,4,4) | `src/rgw/rgw_common.h:1193` and `:1210` |
| RGWBucketInfo | (24,4) | legacy 32-bit (24,4,4) | `src/rgw/rgw_common.cc:2305` and `:2357` |
| RGWBucketEnt | (7,5) | legacy compat-len (7,5,5) | `src/rgw/rgw_common.h:1530` and `:1548` |
| bucket_index_normal_layout | (2,1) main, (1,1) Squid | standard | `src/rgw/rgw_bucket_layout.cc:84` |
| bucket_index_layout | (1,1) | standard | `:120` |
| bucket_index_layout_generation | (1,1) | standard | `:160` |
| bucket_index_log_layout | (1,1) | standard | `:225` |
| bucket_fifo_log_layout | (1,1), main only | standard | `:253` |
| bucket_log_layout | (1,1) | standard | `:281` |
| bucket_log_layout_generation | (1,1) | standard | `:338` |
| rgw::BucketLayout | (3,1) main, (2,1) Squid | standard | `:404` |
| converted_variant and versioned_variant | see header | see header | `src/common/versioned_variant.h` |

Release-dependent encoders: `BucketLayout.Encode` emits version 2 and omits `JudgeReshardLockTime` when `r == denc.Squid`; `IndexNormalLayout.Encode` emits version 1 and omits `MinNumShards` on Squid. The shard naming and selection come from `src/rgw/services/svc_bi_rados.cc:27,97-98,130-176` and `svc_bi_rados.h:98-119`, and `ceph_str_hash_linux` from `src/common/ceph_hash.cc`; port that hash into `bucket_layout.go` with a test vector taken from the populated Squid cluster: the shard whose listing contains `small.bin` (Task 13's manifest names the bucket and its eleven shards), recorded in the test together with the `rados listomapkeys` command that located it.

- [ ] **Step 1: Goldens** for the three types. **Step 2:** round-trip specs with JSON compared for all three (`RGWBucketInfo::dump` names in `rgw_json_enc.cc`). **Step 3:** unit specs: `IndexShardOID` for the three shapes; `IndexShard` against the recorded vector; `Owner` converted and versioned forms encode a user owner as the bare user and as a v0 header respectively. **Step 4:** run to fail, implement, run to pass. **Step 5:** commit `feat(meta): add bucket entry point, bucket info and index layout with corpus goldens`.

---

### Task 7: `meta` zone configuration types

**Files:**
- Create: `internal/meta/zone_params.go`, `internal/meta/zonegroup.go`, `internal/meta/realm.go`, `internal/meta/period.go`, `internal/meta/sysobj.go`, `internal/meta/jsonformattable.go`, `internal/meta/zone_test.go`
- Goldens: `RGWZoneParams`, `RGWZoneGroup`, `RGWZone`, `RGWZonePlacementInfo`, `RGWZoneGroupPlacementTarget`, `RGWZoneGroupPlacementTier`, `RGWZoneStorageClasses`, `RGWZoneStorageClass`, `RGWRealm`, `RGWPeriod`, `RGWPeriodLatestEpochInfo`, `RGWDefaultSystemMetaObjInfo`, `RGWNameToId` mapped to `internal/meta`

**Interfaces:**
- Produces `ZoneParams`, `ZoneGroup`, `Zone`, `ZonePlacementInfo`, `ZoneGroupPlacementTarget`, `ZoneGroupPlacementTier`, `ZoneStorageClasses`, `Realm`, `Period`, `PeriodMap`, `PeriodConfig`, `RateLimitInfo`, `PeriodLatestEpochInfo`, `DefaultSystemMetaObjInfo`, `NameToID`, and `JSONFormattable`, each with `Encode(e, r)` and `Decode<Type>(d)`, plus the root-pool object names:

```go
const RootPool = ".rgw.root"
func ZoneInfoOID(zoneID string) string        // "zone_info.<id>"
func ZoneNameOID(name string) string          // "zone_names.<name>"
func DefaultZoneOID(realmID string) string    // "default.zone.<realmID>" (a trailing dot when empty)
func ZoneGroupInfoOID(id string) string       // "zonegroup_info.<id>"
func ZoneGroupNameOID(name string) string     // "zonegroups_names.<name>"
func DefaultZoneGroupOID(realmID string) string
func RealmOID(id string) string               // "realms.<id>"
func RealmNameOID(name string) string         // "realms_names.<name>"
func DefaultRealmOID() string                 // "default.realm"
func PeriodOID(id string, epoch uint32) string           // "periods.<id>.<epoch>"
func PeriodLatestEpochOID(id string) string              // "periods.<id>.latest_epoch"
// ZoneParams.PoolFor returns the pool and namespace for a zone-params pool field.
```

| Type | Encode | Source |
|---|---|---|
| RGWZoneParams | (19,1) main, (15,1) Squid; identical bytes through group_pool | `src/rgw/rgw_zone.h:67`, nested `RGWSystemMetaObj` (1,1) at `:81` |
| RGWZoneGroup | (6,1) | `src/rgw/rgw_zone.h:324` |
| RGWZone | (8,1) | `src/rgw/rgw_zone_types.h:351` |
| RGWZonePlacementInfo | (8,1) | `:228` |
| RGWZoneGroupPlacementTarget | (3,1) | `:732` |
| RGWZoneGroupPlacementTier | (5,1) main, (1,1) Squid | `:636` |
| RGWZoneGroupPlacementTierS3 | (3,1) main, (1,1) Squid | `:518` |
| RGWRealm | (1,1) | `src/rgw/rgw_zone.h:494` |
| RGWPeriod | (1,1) | `:690` |
| RGWPeriodMap | (2,1) | `src/rgw/rgw_zone.cc:866` |
| RGWPeriodConfig | (2,1) | `src/rgw/rgw_zone.h:443` |
| RGWRateLimitInfo | (2,1) main, (1,1) Squid | `src/rgw/rgw_common.h:596` |
| RGWPeriodLatestEpochInfo | (1,1) | `src/rgw/rgw_zone.h:557` |
| RGWDefaultSystemMetaObjInfo, RGWNameToId | (1,1) | `src/rgw/rgw_zone_types.h:90`, `:69` |
| JSONFormattable | read `encode` in `src/common/ceph_json.h` | tier_config inside RGWZoneParams and the placement tier |

Every root-pool object is binary, none is JSON, verified in `src/rgw/driver/rados/config/impl.cc:204-231`. Name prefixes come from `src/rgw/driver/rados/config/{realm,period,zone,zonegroup}.cc`. The release-dependent encoders emit the Squid version and omit the later fields when `r == denc.Squid`.

- [ ] **Step 1: Goldens** for every type listed. **Step 2:** round-trip specs; JSON compared for `RGWZoneParams`, `RGWZoneGroup`, `RGWRealm`, `RGWPeriod`, since `radosgw-admin zone get` and friends print them. **Step 3:** unit specs for the object-name functions including the trailing-dot case. **Step 4:** run to fail, implement, run to pass. **Step 5:** commit `feat(meta): add realm, zonegroup, zone and period types with corpus goldens`.

---

### Task 8: `meta` object attribute types

**Files:**
- Create: `internal/meta/manifest.go`, `internal/meta/compression.go`, `internal/meta/cachenotify.go`, `internal/meta/attrs.go`, `internal/meta/manifest_test.go`
- Goldens: `RGWObjManifest`, `RGWObjManifestPart`, `RGWObjManifestRule`, `RGWObjTier`, `RGWCompressionInfo`, `RGWCacheNotifyInfo` mapped to `internal/meta`

**Interfaces:**
- Produces:

```go
type Manifest struct { /* every RGWObjManifest field: ObjSize, Objs (legacy map), ExplicitObjs, HeadSize, MaxHeadSize, Prefix, Rules, TailInstance, TailPlacement, HeadPlacementRule, Begin/End iterators' state, TierType, TierConfig */ }
type ManifestRule struct{ StartPartNum, StartOfs, PartSize, StripeMaxSize uint64; OverridePrefix string }
type ManifestPart struct{ Loc Obj; LocOfs, Size uint64 }
type ObjTier struct{ Name string; Tier ZoneGroupPlacementTier; IsMultipartUpload bool }

func (m Manifest) Encode(e *denc.Encoder, r denc.Release)
func DecodeManifest(d *denc.Decoder) Manifest

// Stripe describes one RADOS object of an object's data as the manifest lays it out.
type Stripe struct{ Ofs, Size uint64; Key ObjKey; Placement PlacementRule; InHead bool }
// Stripes walks the manifest exactly as RGWObjManifest::obj_iterator does.
func (m Manifest) Stripes() []Stripe

type CompressionInfo struct{ Type string; OrigSize uint64; CompressorMessage *int32; Blocks []CompressionBlock }
type CompressionBlock struct{ OldOfs, NewOfs, Len uint64 }
func (c CompressionInfo) Encode(e *denc.Encoder, r denc.Release)
func DecodeCompressionInfo(d *denc.Decoder) CompressionInfo

type CacheNotifyOp uint32
const ( CacheUpdateObj CacheNotifyOp = 0; CacheInvalidateObj CacheNotifyOp = 1 )
type CacheNotifyInfo struct{ Op CacheNotifyOp; Obj RawObj; ObjInfo ObjectCacheInfo; Ofs int64; NS string }
type ObjectCacheInfo struct{ Status int32; Flags uint32; Epoch uint64; Data []byte; Xattrs map[string][]byte; RMXattrs map[string][]byte; Meta ObjectMetaInfo; Version ObjVersion; TimeAdded Time }
type ObjectMetaInfo struct{ Size uint64; Mtime Time }
type ObjVersion struct{ Ver uint64; Tag string }    // obj_version from cls_version, duplicated here so meta does not import cls
func (n CacheNotifyInfo) Encode(e *denc.Encoder, r denc.Release)
func DecodeCacheNotifyInfo(d *denc.Decoder) CacheNotifyInfo

// Attr names radosgw writes on heads and buckets, from src/rgw/rgw_common.h:74-210.
const (
	AttrPrefix      = "user.rgw."
	AttrACL         = "user.rgw.acl"
	AttrETag        = "user.rgw.etag"
	AttrIDTag       = "user.rgw.idtag"
	AttrTailTag     = "user.rgw.tail_tag"
	AttrManifest    = "user.rgw.manifest"
	AttrPGVer       = "user.rgw.pg_ver"
	AttrSourceZone  = "user.rgw.source_zone"
	AttrContentType = "user.rgw.content_type"
	AttrStorageClass = "user.rgw.storage_class"
	AttrCompression = "user.rgw.compression"
	AttrMetaPrefix  = "user.rgw.x-amz-meta-"
	AttrObjVersion  = "ceph.objclass.version"  // cls_version's xattr, not user.rgw.*
)
```

| Type | Encode | Decode framing | Source |
|---|---|---|---|
| RGWObjManifest | (8,6) | legacy 32-bit (8,2,2) | `src/rgw/driver/rados/rgw_obj_manifest.h:281` and `:308` |
| RGWObjManifestPart | (2,2) | legacy 32-bit (2,2,2) | `:98` and `:106` |
| RGWObjManifestRule | (2,1) | standard | `:146` |
| RGWObjTier | (2,2) | legacy compat-len (2,2,2) | `:178` and `:186` |
| RGWCompressionInfo, compression_block | (2,1), (1,1) | standard | `src/rgw/rgw_compression_types.h:64`, `:33` |
| RGWCacheNotifyInfo | (2,2) | legacy compat-len (2,2,2) | `src/rgw/rgw_cache.h:107` and `:116` |
| ObjectCacheInfo | (5,3) | legacy compat-len (5,3,3) | `:66` and `:78` |
| ObjectMetaInfo | (2,2) | legacy compat-len (2,2,2) | `:36` and `:42` |

The manifest's tier config embeds `RGWZoneGroupPlacementTier`, whose version differs by release (Task 7), so `Manifest.Encode` passes `r` through. Tail naming, from `src/rgw/rgw_obj_manifest.cc:209-256` and `src/rgw/driver/rados/rgw_obj_manifest.cc:239-248`: stripes beyond the head are namespace `shadow`, name `<prefix><n>` with `n` starting at 1 and the prefix `.` + 32 random alphanumerics + `_`; multipart parts are namespace `multipart`, name `<obj>.<upload>.<part>` for the first stripe and `shadow` `<obj>.<upload>.<part>_<n>` after. `Stripes()` must reproduce `RGWObjManifest::obj_iterator` across explicit-objs and rule-based manifests; test it against a manifest decoded from a corpus golden by asserting the sum of stripe sizes equals `ObjSize` and the first stripe is the head when `HeadSize > 0`.

- [ ] **Step 1: Goldens.** **Step 2:** round-trip specs (JSON compared for `RGWObjManifest`, since `radosgw-admin object stat` prints it). **Step 3:** `Stripes()` specs. **Step 4:** run to fail, implement, run to pass. **Step 5:** commit `feat(meta): add manifest, compression and cache-notify types with corpus goldens`.

---

### Task 9: `acl` types, encoding only

**Files:**
- Create: `internal/acl/doc.go`, `internal/acl/policy.go`, `internal/acl/grant.go`, `internal/acl/acl_suite_test.go`, `internal/acl/goldens_test.go`
- Goldens: `RGWAccessControlPolicy`, `RGWAccessControlList` mapped to `internal/acl`

**Interfaces:**
- Consumes: `meta.Owner`, `meta.UserID`.
- Produces:

```go
package acl

type Permission uint32   // ACLPermission flags: READ=1, WRITE=2, READ_ACP=4, WRITE_ACP=8, FULL_CONTROL=15
type GranteeType uint32  // ACLGranteeType: user, email, group, unknown, referer
type Grant struct{ Type GranteeType; ID string; Email string; Permission Permission; Name string; Group uint32; URLSpec string }
type Referer struct{ URLSpec string; Perm uint32 }
type List struct {
	UserMap    map[string]int32
	GroupMap   map[uint32]int32
	RefererList []Referer
	Grants     []GrantEntry   // the multimap in wire order
}
type GrantEntry struct{ Key string; Grant Grant }
type Owner struct{ ID string; DisplayName string }   // ACLOwner: id is to_string(rgw_owner)
type Policy struct{ Owner Owner; ACL List }

func (p Policy) Encode(e *denc.Encoder, r denc.Release)
func DecodePolicy(d *denc.Decoder) Policy
// and Encode/Decode for List, Grant, Owner, Permission, GranteeType, Referer
```

| Type | Encode | Decode framing | Source |
|---|---|---|---|
| RGWAccessControlPolicy | (2,2) | legacy compat-len (2,2,2) | `src/rgw/rgw_acl.h:405` and `:411` |
| RGWAccessControlList | (4,3) | legacy compat-len (4,3,3) | `:302` and `:312` |
| ACLOwner | (3,2) | legacy compat-len (3,2,2) | `:361` and `:368` |
| ACLGrant | (5,3) | legacy compat-len (5,3,3) | `:88` and `:129` |
| ACLReferer | (1,1) | legacy compat-len (1,1,1) | `:242` |
| ACLPermission, ACLGranteeType | (2,2) | legacy compat-len (2,2,2) | `src/rgw/rgw_acl_types.h:172`, `:203` |

`RGWAccessControlList::encode` writes a bool `maps_initialized` (always true) before the four containers (`rgw_acl.h:301-309`). The grant map is a `std::multimap<string, ACLGrant>`: u32 count then key and value pairs in key order with equal keys in insertion order, which is why `Grants` is a slice, not a map. Grant evaluation, canned ACLs and XML arrive in phase 1; this task is the on-disk form only.

- [ ] **Step 1: Goldens.** **Step 2:** round-trip specs with JSON compared for both (`radosgw-admin` prints ACLs through `dump`). **Step 3:** run to fail, implement, run to pass. **Step 4:** commit `feat(acl): add ACL policy on-disk types with corpus goldens`.

---
### Task 10: `radosclient` seam

**Files:**
- Create: `internal/radosclient/doc.go`, `internal/radosclient/cluster.go`, `internal/radosclient/ops.go`, `internal/radosclient/steps.go`, `internal/radosclient/results.go`, `internal/radosclient/flags.go`, `internal/radosclient/errors.go`, `internal/radosclient/radosclient_suite_test.go`, `internal/radosclient/ops_test.go`

**Interfaces:**
- Produces the contract every class package and the driver consume, and the goceph package implements:

```go
package radosclient

// Cluster is one connected RADOS client.
type Cluster interface {
	// Pool opens an I/O context on pool within namespace ("" for the default namespace).
	Pool(ctx context.Context, pool, namespace string) (Pool, error)
	// MonCommand runs a JSON mon command and returns its output and status line.
	MonCommand(ctx context.Context, cmd []byte) (out []byte, status string, err error)
	// ConfigGet reads any Ceph configuration option by name through librados.
	ConfigGet(name string) (string, error)
	// RequiredOSDRelease returns the OSD map's require_osd_release name, e.g. "squid".
	RequiredOSDRelease(ctx context.Context) (string, error)
	Close() error
}

// Pool is an I/O context: one pool and one namespace, with an optional object locator.
type Pool interface {
	Name() string
	Namespace() string
	// WithLocator returns a Pool whose operations set the given object locator key.
	WithLocator(loc string) Pool
	// Read runs a read op. Results are available on the op's steps afterwards.
	Read(ctx context.Context, oid string, op *ReadOp, flags OpFlags) error
	// Write runs a write op and returns the object version librados reports for it.
	Write(ctx context.Context, oid string, op *WriteOp, flags OpFlags) (version uint64, err error)
	// ListObjects calls fn for every object in the namespace until fn returns an error.
	ListObjects(ctx context.Context, fn func(oid, locator string) error) error
	Watch(ctx context.Context, oid string, fn func(notifyID, notifierID uint64, payload []byte)) (Watch, error)
	Notify(ctx context.Context, oid string, payload []byte, timeout time.Duration) ([]NotifyAck, error)
	LockExclusive(ctx context.Context, oid, name, cookie, desc string, duration time.Duration, flags LockFlags) error
	LockShared(ctx context.Context, oid, name, cookie, tag, desc string, duration time.Duration, flags LockFlags) error
	Unlock(ctx context.Context, oid, name, cookie string) error
	BreakLock(ctx context.Context, oid, name, client, cookie string) error
	ListLockers(ctx context.Context, oid, name string) ([]Locker, error)
	Close() error
}

type Watch interface{ Close() error }
type NotifyAck struct{ NotifierID, Cookie uint64; Payload []byte }
type Locker struct{ Client, Cookie, Address string }
type LockFlags uint8
const LockRenew LockFlags = 1

type OpFlags uint32
const (
	OpFlagNone      OpFlags = 0
	OpFlagBalanceReads OpFlags = 1 << 0
	OpFlagLocalizeReads OpFlags = 1 << 1
	OpFlagIgnoreCache OpFlags = 1 << 3
	OpFlagReturnVec OpFlags = 1 << 10  // LIBRADOS_OPERATION_RETURNVEC
)

type CmpOp uint8
const ( CmpEQ CmpOp = 1; CmpNE CmpOp = 2; CmpGT CmpOp = 3; CmpGTE CmpOp = 4; CmpLT CmpOp = 5; CmpLTE CmpOp = 6 )

// Execer is what a class package needs: a place to add an exec step.
type Execer interface{ Exec(class, method string, in []byte) *ExecResult }

// ReadOp is a compound read operation, built then run once through Pool.Read.
type ReadOp struct{ /* steps []Step */ }
func NewReadOp() *ReadOp
func (o *ReadOp) Steps() []Step
func (o *ReadOp) AssertExists()
func (o *ReadOp) AssertVersion(ver uint64)
func (o *ReadOp) CmpXattr(name string, op CmpOp, value []byte)
func (o *ReadOp) Read(offset uint64, length uint64) *ReadResult
func (o *ReadOp) Stat() *StatResult
func (o *ReadOp) GetXattrs() *XattrsResult
func (o *ReadOp) OmapGetVals(startAfter, filterPrefix string, max uint64) *OmapResult
func (o *ReadOp) OmapGetValsByKeys(keys []string) *OmapResult
func (o *ReadOp) OmapGetKeys(startAfter string, max uint64) *OmapKeysResult
func (o *ReadOp) Exec(class, method string, in []byte) *ExecResult

// WriteOp is a compound write operation, built then run once through Pool.Write.
type WriteOp struct{ /* steps []Step; mtime *time.Time */ }
func NewWriteOp() *WriteOp
func (o *WriteOp) Steps() []Step
func (o *WriteOp) SetMtime(t time.Time)
func (o *WriteOp) AssertExists()
func (o *WriteOp) AssertVersion(ver uint64)
func (o *WriteOp) CmpXattr(name string, op CmpOp, value []byte)
func (o *WriteOp) Create(exclusive bool)
func (o *WriteOp) Remove()
func (o *WriteOp) WriteFull(data []byte)
func (o *WriteOp) Write(data []byte, offset uint64)
func (o *WriteOp) Append(data []byte)
func (o *WriteOp) Zero(offset, length uint64)
func (o *WriteOp) Truncate(offset uint64)
func (o *WriteOp) SetXattr(name string, value []byte)
func (o *WriteOp) RmXattr(name string)
func (o *WriteOp) OmapSet(kv map[string][]byte)
func (o *WriteOp) OmapRmKeys(keys []string)
func (o *WriteOp) OmapClear()
func (o *WriteOp) OmapCmp(key string, op CmpOp, value []byte)
func (o *WriteOp) SetAllocHint(expectedObjectSize, expectedWriteSize uint64, flags AllocHintFlags)
func (o *WriteOp) Exec(class, method string, in []byte) *ExecResult

// Step is one entry of an op, exported so implementations translate it and tests inspect it.
type Step interface{ isStep() }
type ExecStep struct{ Class, Method string; In []byte; Result *ExecResult }
type ReadStep struct{ Offset, Length uint64; Result *ReadResult }
type StatStep struct{ Result *StatResult }
// ... one exported struct per builder method above

// Results are filled by the implementation after the op runs.
type ExecResult struct{ /* out []byte; rval int32; done bool */ }
func (r *ExecResult) Bytes() ([]byte, error)   // ErrIncomplete before the op ran; the step's error if rval < 0
func (r *ExecResult) Set(out []byte, rval int32) // for implementations and tests
type ReadResult struct{ Data []byte; N int; Err error }
type StatResult struct{ Size uint64; ModTime time.Time; Err error }
type XattrsResult struct{ Xattrs map[string][]byte; Err error }
type OmapResult struct{ Values map[string][]byte; More bool; Err error }
type OmapKeysResult struct{ Keys []string; More bool; Err error }

// Errors: every RADOS failure is an *Error wrapping the errno, and the common ones
// have sentinels so callers use errors.Is.
type Error struct{ Errno int32; Op string }
func (e *Error) Error() string
func (e *Error) Is(target error) bool
var (
	ErrNotFound       = errors.New("rados: not found")             // ENOENT
	ErrExists         = errors.New("rados: exists")                // EEXIST
	ErrCanceled       = errors.New("rados: canceled")              // ECANCELED
	ErrNoSpace        = errors.New("rados: no space")              // ENOSPC
	ErrPermission     = errors.New("rados: operation not permitted") // EPERM
	ErrNoData         = errors.New("rados: no data")               // ENODATA
	ErrRange          = errors.New("rados: range")                 // ERANGE
	ErrTooBig         = errors.New("rados: too big")               // EFBIG
	ErrInvalid        = errors.New("rados: invalid argument")      // EINVAL
	ErrTimedOut       = errors.New("rados: timed out")             // ETIMEDOUT
	ErrBusyResharding = errors.New("rados: bucket is resharding")  // 2300, cls_rgw's ERR_BUSY_RESHARDING
	ErrIncomplete     = errors.New("rados: operation has not run")
	ErrNotSupported   = errors.New("rados: operation not supported") // EOPNOTSUPP
)
```

The seam is a leaf package with concrete builders and interfaces for the connected objects. Builders record steps in order; an implementation translates them at run time. `Pool.Write` returns the version because radosgw's index protocol needs the head write's epoch. Cancellation: `Read` and `Write` return `ctx.Err()` when the context ends before the operation completes, but the operation itself cannot be cancelled in librados, so the implementation owns the step buffers until the completion fires.

- [ ] **Step 1: Write failing builder tests**

`ops_test.go`: building a `ReadOp` with `Stat`, `GetXattrs`, `Read(0, 4<<20)` yields three steps in that order with the returned results attached; `Exec` on both ops returns an `ExecResult` whose `Bytes()` returns `ErrIncomplete` until `Set` is called and returns the bytes afterwards, or the mapped error when `rval` is `-2`; `(&Error{Errno: 2}).Is(ErrNotFound)` is true and `Is(ErrExists)` false; `Error{Errno: 2300}` matches `ErrBusyResharding`.

- [ ] **Step 2: Run to fail, implement, run to pass, `make check`, commit** `feat(radosclient): define the RADOS client seam`.

---

### Task 11: go-ceph fork bindings

This task runs in `/home/jhoblitt/github/go-ceph`, not in rgw-go. Start from upstream: `git fetch origin && git switch -c rgw-go/rados-async-and-steps origin/master`. The local checkout is 108 commits behind upstream master, which has the first `ceph_preview` rados files, a gitlint job that requires `Signed-off-by`, and Squid as the default test version.

**Files (all new, all `//go:build ceph_preview`, one C function per file, each with a `_test.go`):**
- `rados/read_op_getxattrs.go`, `rados/read_op_stat2.go`, `rados/read_op_cmpxattr.go`, `rados/read_op_omap_get_keys.go`
- `rados/write_op_cmpxattr.go`, `rados/write_op_rmxattr.go`, `rados/write_op_append.go`, `rados/write_op_zero.go`, `rados/write_op_truncate.go`, `rados/write_op_set_flags.go`, `rados/write_op_omap_cmp.go`
- `rados/operation_flags.go` (modify: add `OperationReturnVec OperationFlags = C.LIBRADOS_OPERATION_RETURNVEC` if absent)
- `rados/aio_completion.go`, `rados/aio_notifier.go`, `rados/read_op_operate_async.go`, `rados/write_op_operate_async.go`, and their tests
- `docs/api-status.json`, `docs/api-status.md` via `make api-update`

**Interfaces:**
- Produces, in package `rados`:

```go
// Read-op steps
func (r *ReadOp) GetXattrs() *ReadOpGetXattrsStep          // rados_read_op_getxattrs
func (s *ReadOpGetXattrsStep) Xattrs() (map[string][]byte, error)
func (r *ReadOp) Stat() *ReadOpStatStep                    // rados_read_op_stat2
func (s *ReadOpStatStep) Size() uint64; func (s *ReadOpStatStep) ModTime() time.Time
func (r *ReadOp) CmpXattr(name string, op CmpXattrOp, value []byte)   // rados_read_op_cmpxattr
func (r *ReadOp) GetOmapKeys(startAfter string, maxReturn uint64) *ReadOpOmapGetKeysStep // rados_read_op_omap_get_keys2
// Write-op steps
func (w *WriteOp) CmpXattr(name string, op CmpXattrOp, value []byte)  // rados_write_op_cmpxattr
func (w *WriteOp) RmXattr(name string)                                // rados_write_op_rmxattr
func (w *WriteOp) Append(b []byte)                                    // rados_write_op_append
func (w *WriteOp) Zero(offset, length uint64)                         // rados_write_op_zero
func (w *WriteOp) Truncate(offset uint64)                             // rados_write_op_truncate
func (w *WriteOp) SetFlags(flags OperationFlags)                      // rados_write_op_set_flags
func (w *WriteOp) OmapCmp(key string, op CmpXattrOp, value []byte)    // rados_write_op_omap_cmp
type CmpXattrOp int  // LIBRADOS_CMPXATTR_OP_EQ .. LTE

// Asynchronous operate
type AioCompletion struct{ /* c C.rados_completion_t; done chan struct{}; keep []unsafe.Pointer; pinner runtime.Pinner */ }
func (r *ReadOp) OperateAsync(ioctx *IOContext, oid string, flags OperationFlags) (*AioCompletion, error)
func (w *WriteOp) OperateAsync(ioctx *IOContext, oid string, flags OperationFlags) (*AioCompletion, error)
func (c *AioCompletion) Done() <-chan struct{}    // closed when librados reports completion
func (c *AioCompletion) ReturnValue() int         // valid after Done is closed
func (c *AioCompletion) Version() uint64          // rados_aio_get_version, valid after Done
func (c *AioCompletion) Release()                 // frees the C completion and unpins buffers; steps' update() runs first

// Notifier selection, process-wide, set before the first OperateAsync.
type AioMode int
const ( AioModeCallback AioMode = iota; AioModePipe )
func SetAioMode(m AioMode)
```

Binding conventions, from the go-ceph tree: a step is a struct embedding `withoutUpdate`/`withoutFree` as needed and implementing `update() error` and `free()`; out-parameters are C-allocated (`C.malloc`) so their addresses stay valid; the action method appends the step and calls the C function; the godoc ends with `// Implements:` and the C prototype; tests are methods on `RadosTestSuite` using `suite.SetupConnection()` and testify's `assert`. See `rados/read_op_read.go`, `rados/read_op_exec.go`, `rados/write_op_cmpext.go` and `rados/operation.go` for the exact shapes.

Asynchronous specifics:
- `OperateAsync` creates the completion with `rados_aio_create_completion2(arg, cb)` where `cb` is a C function in the preamble and `arg` is an integer id from `internal/callbacks` (the pattern in `rbd/diff_iterate.go`), never a Go pointer. It then calls `rados_aio_read_op_operate` or `rados_aio_write_op_operate`.
- Buffers: every Go slice a step handed to C (`readStep.cBuffer`, `writeStep.cBuffer`) is pinned with `runtime.Pinner.Pin(&b[0])` in the completion before the async call and unpinned in `Release`; `ReadOpExecStep.prval` and the other Go-embedded out-parameters are moved to C memory in the async path. A zero-length slice is never dereferenced.
- Callback mode: the exported Go function looks up the id, records the return value, runs each step's `update()`, and closes `done`. Pipe mode: the C callback writes the 8-byte id to a pipe created by `SetAioMode`; one goroutine, started by the first async operate, drains the pipe and performs the same completion work. `Done` and `Release` behave identically in both modes.
- The completion, not the op, owns buffer lifetime: `ReadOp.Release` and `WriteOp.Release` after `OperateAsync` free the C op object but not the pinned buffers, which live until `AioCompletion.Release`.

- [ ] **Step 1: Sync with upstream and read the conventions**

```sh
cd /home/jhoblitt/github/go-ceph && git fetch origin && git switch -c rgw-go/rados-async-and-steps origin/master
sed -n 1,80p docs/development.md; sed -n 1,60p rados/read_op_read.go; sed -n 1,140p rados/operation.go
```

- [ ] **Step 2: Read-op steps, test first, one commit each**

For `GetXattrs`: the test writes two xattrs with `SetXattr`, builds a `ReadOp` with `GetXattrs()`, operates, and asserts `Xattrs()` returns both; then implement with `rados_read_op_getxattrs(op, &iter, &prval)` and drain `rados_getxattrs_next` in `update()`, ending with `rados_getxattrs_end`. Same rhythm for `Stat` (`rados_read_op_stat2` with `uint64_t *psize, struct timespec *pmtime, int *prval`), `CmpXattr` (assert a mismatched value makes `Operate` return an error), `GetOmapKeys`. Commit each as `rados: add ReadOp.<Method> (rados_read_op_<cfunc>)` with `Signed-off-by`.

- [ ] **Step 3: Write-op steps, same rhythm**

`CmpXattr` (mismatch fails the whole op), `RmXattr` (attr gone afterwards), `Append` (content grows), `Zero`, `Truncate` (size after stat), `SetFlags`, `OmapCmp` (mismatch fails). One commit each.

- [ ] **Step 4: Return-vector flag**

If `OperationReturnVec` is absent from `rados/operation_flags.go`, add it, and add a test that a `ReadOp.Exec` of a modifying class method returns its output when the read is operated with `OperationReturnVec`. Use the `version` class: `set` then `read` in one read op with the flag; assert the `read` output decodes to the version just set. Commit `rados: add OperationReturnVec`.

- [ ] **Step 5: AioCompletion, callback mode first**

Test: build a `WriteOp` with `WriteFull`, `OperateAsync`, `<-c.Done()`, assert `ReturnValue() == 0` and a following synchronous read sees the data; build a `ReadOp` with `Read`, `OperateAsync`, `<-Done()`, assert the step's `BytesRead`. Then a concurrency test: 256 async writes in flight, all complete, `runtime.NumGoroutine()` returns to baseline after `Release`. Implement. Commit `rados: add asynchronous ReadOp and WriteOp operate with AioCompletion`.

- [ ] **Step 6: Pipe mode**

Same tests parameterized by `SetAioMode(AioModePipe)`. Implement the pipe, the drain goroutine and its stop condition (closed on `SetAioMode` change or process exit is acceptable for a process-wide singleton, documented). Commit `rados: add a pipe-based completion notifier for AioCompletion`.

- [ ] **Step 7: API status, tests in the container, push**

```sh
make api-update
make test-container CEPH_VERSION=squid ENTRYPOINT_ARGS="--test-pkg=rados --test-run=TestRadosTestSuite"
git push jhoblitt rgw-go/rados-async-and-steps
```

Record the branch's head commit; rgw-go's `go.mod` `replace` pins it in Task 12. Do not open the upstream PR in this task; that waits until rgw-go has exercised the bindings.

---

### Task 12: `radosclient/goceph` over the fork

**Files:**
- Create: `internal/radosclient/goceph/doc.go`, `internal/radosclient/goceph/cluster.go`, `internal/radosclient/goceph/pool.go`, `internal/radosclient/goceph/translate.go`, `internal/radosclient/goceph/completion.go`, `internal/radosclient/goceph/release.go`, `internal/radosclient/goceph/goceph_suite_test.go`, `internal/radosclient/goceph/translate_test.go`, `internal/radosclient/goceph/goceph_integration_test.go`
- Modify: `go.mod` (`require github.com/ceph/go-ceph` and `replace github.com/ceph/go-ceph => github.com/jhoblitt/go-ceph <pseudo-version of the Task 11 head>`)

**Interfaces:**
- Consumes: Task 10's seam; Task 11's bindings.
- Produces:

```go
package goceph

// Mode selects how a completion wakes the waiting goroutine.
type Mode string
const (
	ModeSync     Mode = "sync"      // stock blocking Operate, one OS thread parked per operation
	ModeCallback Mode = "callback"  // AioCompletion with the C-to-Go callback
	ModePipe     Mode = "pipe"      // AioCompletion with the pipe notifier
)

type Config struct {
	Cluster    string   // ceph cluster name, default "ceph"
	Name       string   // entity name, e.g. "client.rgw.a"
	ConfigFile string   // "" reads the default config file
	Args       []string // ceph-style argv handed to librados after the early arguments
	Mode       Mode
}

// Connect creates the librados connection, reads the config file and CEPH_ARGS, parses Args,
// connects, and returns the seam's Cluster.
func Connect(ctx context.Context, cfg Config) (radosclient.Cluster, error)
```

Behaviors:
- `Pool` opens an `IOContext`, sets the namespace, and caches one `IOContext` per locator behind `WithLocator`, since librados keeps the locator on the context.
- `translate.go` turns seam steps into go-ceph steps in order and copies results back into the seam's result structs after the op completes; a seam `Error` wraps the errno from go-ceph's `radosError`, and a class error of 2300 maps to `ErrBusyResharding`.
- `Read` and `Write` in `ModeSync` call `Operate`; in the two async modes they call `OperateAsync`, then `select` on `Done()` and `ctx.Done()`; on context cancellation they return `ctx.Err()` and hand the completion to a background reaper goroutine that waits for `Done` and then `Release`s it, so buffers stay pinned until librados is finished with them.
- `RequiredOSDRelease` runs `{"prefix":"osd dump","format":"json"}` and returns the `require_osd_release` field.

- [ ] **Step 1: Pin the fork**

```sh
go get github.com/ceph/go-ceph@v0.39.0
go mod edit -replace github.com/ceph/go-ceph=github.com/jhoblitt/go-ceph@<Task 11 head commit>
go mod tidy
```

- [ ] **Step 2: Unit specs that need no cluster**

`translate_test.go`: a seam `ReadOp` with `Stat`, `GetXattrs`, `Read` translates to three go-ceph steps in the same order (assert through a small interface the translator exposes for tests); errno mapping table: 2 to `ErrNotFound`, 17 to `ErrExists`, 125 to `ErrCanceled`, 2300 to `ErrBusyResharding`; `Config{}` defaults to cluster `ceph` and `ModeCallback`.

- [ ] **Step 3: Integration specs**

`goceph_integration_test.go`, `//go:build integration`, `Label("integration")`, reading `RGW_GO_TEST_CEPH_CONF` for the disposable cluster's config (Task 13) and running the whole suite once per mode:

```go
var _ = Describe("goceph against a cluster", Label("integration"), func() {
	for _, mode := range []goceph.Mode{goceph.ModeSync, goceph.ModeCallback, goceph.ModePipe} {
		Context(string(mode), func() {
			var pool radosclient.Pool
			BeforeEach(func() { /* Connect with mode; Pool("rgw-go-test", "") on a pool the cluster tooling created */ })
			It("writes then reads a zero-length object", func() { /* WriteOp.Create(true); Read op Stat: Size 0 */ })
			It("sets and reads an empty xattr", func() { /* SetXattr("user.rgw.etag", nil); GetXattrs has the key with empty value */ })
			It("composes stat, xattrs and read in one op", func() { /* one Read op; all three results filled */ })
			It("fails a guarded write with ErrCanceled on a tag mismatch", func() { /* CmpXattr then WriteFull */ })
			It("returns the version after a write", func() { /* version > 0 and increases on a second write */ })
			It("returns a class method's output through a write exec with ReturnVec", func() { /* version set + read */ })
			It("keeps OS thread count flat with 512 reads in flight", func() { /* count threads from /proc/self/status before, during, after; sync mode is allowed to grow, async modes must not exceed baseline+16 */ })
			It("reports the required OSD release", func() { /* "squid" on the Squid cluster */ })
		})
	}
})
```

- [ ] **Step 4: Implement, run the unit specs, then the integration specs against the Squid disposable cluster**

```sh
make cluster-up-squid
RGW_GO_TEST_CEPH_CONF=hack/cluster/out/squid/ceph.conf go test -tags integration -race ./internal/radosclient/goceph/ -v
```

- [ ] **Step 5: Commit**

`feat(goceph): implement the RADOS seam over the go-ceph fork with sync, callback and pipe completions`.

---
### Task 13: Disposable clusters and the population script

**Files:**
- Create: `hack/cluster/up.sh`, `hack/cluster/down.sh`, `hack/cluster/populate.sh`, `hack/cluster/entrypoint.sh`, `hack/cluster/README.md`
- Modify: `Makefile` (targets `cluster-up-squid`, `cluster-up-tentacle`, `cluster-down`, `populate`)

**Interfaces:**
- Produces: a running single-node cluster reachable from the host with its `ceph.conf` and admin keyring under `hack/cluster/out/<release>/`, a radosgw on `127.0.0.1:7480`, and after `populate.sh` a manifest file `hack/cluster/out/<release>/manifest.json`:

```json
{
  "release": "squid",
  "zone": "default",
  "pools": {"root": ".rgw.root", "meta": "default.rgw.meta", "control": "default.rgw.control", "log": "default.rgw.log", "index": "default.rgw.buckets.index", "data": "default.rgw.buckets.data", "nonec": "default.rgw.buckets.non-ec"},
  "users": [{"uid": "alice", "tenant": "", "access_key": "...", "secret_key": "..."}, {"uid": "bob", "tenant": "t1"}],
  "buckets": [{"name": "plain", "owner": "alice", "id": "<bucket_id>", "marker": "<marker>", "num_shards": 11}, {"name": "tenanted", "owner": "t1$bob"}],
  "objects": [{"bucket": "plain", "key": "small.bin", "size": 1024}, {"bucket": "plain", "key": "head-full.bin", "size": 4194304}, {"bucket": "plain", "key": "large.bin", "size": 10485760}, {"bucket": "plain", "key": "_underscore.bin", "size": 16}, {"bucket": "plain", "key": "multipart.bin", "size": 20971520, "multipart": true}, {"bucket": "plain", "key": "empty.bin", "size": 0}, {"bucket": "plain", "key": "meta.bin", "size": 64, "content_type": "text/plain", "metadata": {"x-amz-meta-color": "blue"}}]
}
```

`up.sh` starts four podman containers on host networking, named `rgw-go-mon`, `rgw-go-mgr`, `rgw-go-osd` and `rgw-go-rgw` (the mon on 127.0.0.1:3300, daemons on ports 7100-7199, so the cluster coexists with other local Ceph clusters): mon, mgr and one memstore OSD from `quay.io/ceph/ceph:v19.2.6` for Squid, plus a fourth service running `radosgw` with `--rgw-frontends="beast endpoint=127.0.0.1:7480"` against the same config volume, and a `rgw-go-test` pool created by `up.sh` for the goceph integration suite. Tentacle uses the latest `v20.2.*` tag; `up.sh` resolves it once with `skopeo list-tags docker://quay.io/ceph/ceph` (or `podman search --list-tags`) and records it in `hack/cluster/out/tentacle/image`. `up.sh` copies `ceph.conf` and the admin keyring out of the mon volume so host processes can connect, waits for `ceph -s` to report `HEALTH_OK` or `HEALTH_WARN` with the OSD up, and waits for the radosgw to answer on port 7480. On any failure `up.sh` removes the containers, volumes and output it created.

`populate.sh` uses the radosgw's own tools so every object is written by the oracle: `radosgw-admin user create` for `alice` and for `bob` with `--tenant t1`; `aws s3api create-bucket` and `put-object` with the aws CLI on the host against `http://127.0.0.1:7480`, including a multipart upload through `aws s3 cp` of a 20 MiB file (the CLI switches to multipart at 8 MiB); then `radosgw-admin bucket stats` to learn each bucket's id and marker for the manifest.

- [ ] **Step 1: Write `up.sh` and `entrypoint.sh`, bring Squid up, assert `ceph -s` and a radosgw response**

```sh
make cluster-up-squid
podman exec rgw-go-mon ceph -s | head -5
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:7480/   # 403 with an S3 error body is success here
```

- [ ] **Step 2: Write `populate.sh`, run it, check the manifest and the objects**

```sh
make populate RELEASE=squid
jq '.buckets[0].id' hack/cluster/out/squid/manifest.json
podman exec rgw-go-mon rados -p default.rgw.meta -N users.uid ls
podman exec rgw-go-mon rados -p default.rgw.buckets.index ls | head
```

Expected: `alice`, `t1$bob`, `alice.buckets`, `t1$bob.buckets` in `users.uid`; eleven `.dir.<id>.<n>` shards per bucket.

- [ ] **Step 3: Tentacle**

`make cluster-up-tentacle && make populate RELEASE=tentacle`; the same checks.

- [ ] **Step 4: Commit**

`feat(hack): add disposable Squid and Tentacle clusters with a radosgw and a population script`. `hack/cluster/out/` is ignored by `.gitignore`.

---

### Task 14: Class packages: version, refcount, user, rgw, gc

**Files:**
- Create: `internal/cls/version/{doc,ops,types}.go`, `internal/cls/refcount/{doc,ops,types}.go`, `internal/cls/user/{doc,ops,types}.go`, `internal/cls/rgw/{doc,const,ops_index,ops_obj,ops_usage,ops_gc,types_index,types_key,types_usage,types_gc}.go`, `internal/cls/gc/{doc,ops,types}.go`, one `<pkg>_suite_test.go`, `goldens_test.go` and `ops_test.go` per package, and `internal/cls/rgw/rgw_integration_test.go`
- Goldens (`hack/goldens/types.txt`): `obj_version`, `cls_version_check_op`, `cls_version_set_op`, `cls_version_inc_op`, `cls_version_read_ret` to `internal/cls/version`; `obj_refcount`, `cls_refcount_get_op`, `cls_refcount_put_op`, `cls_refcount_set_op`, `cls_refcount_read_op`, `cls_refcount_read_ret` to `internal/cls/refcount`; `cls_user_header`, `cls_user_stats`, `cls_user_bucket`, `cls_user_bucket_entry`, `cls_user_set_buckets_op`, `cls_user_remove_bucket_op`, `cls_user_list_buckets_op`, `cls_user_list_buckets_ret`, `cls_user_get_header_op`, `cls_user_get_header_ret`, `cls_user_complete_stats_sync_op` to `internal/cls/user`; `cls_rgw_obj_key`, `rgw_bucket_dir_header`, `rgw_bucket_dir_entry`, `rgw_bucket_dir_entry_meta`, `rgw_bucket_category_stats`, `rgw_bucket_pending_info`, `rgw_bucket_entry_ver`, `rgw_bucket_dir`, `cls_rgw_bucket_instance_entry`, `rgw_cls_obj_prepare_op`, `rgw_cls_obj_complete_op`, `rgw_cls_list_op`, `rgw_cls_list_ret`, `rgw_cls_check_index_ret`, `rgw_cls_obj_remove_op`, `rgw_cls_obj_store_pg_ver_op`, `rgw_cls_obj_check_attrs_prefix`, `rgw_cls_obj_check_mtime`, `cls_rgw_guard_bucket_resharding_op`, `rgw_cls_tag_timeout_op`, `rgw_cls_usage_log_add_op`, `rgw_cls_usage_log_read_op`, `rgw_cls_usage_log_read_ret`, `rgw_cls_usage_log_trim_op`, `rgw_usage_log_entry`, `rgw_usage_log_info`, `rgw_usage_data`, `cls_rgw_gc_set_entry_op`, `cls_rgw_gc_obj_info`, `cls_rgw_obj_chain`, `cls_rgw_obj` to `internal/cls/rgw`; `cls_rgw_gc_queue_init_op`, `cls_rgw_gc_urgent_data`, `cls_rgw_gc_queue_remove_entries_op`, `cls_rgw_gc_queue_defer_entry_op`, `cls_rgw_gc_list_op`, `cls_rgw_gc_list_ret` to `internal/cls/gc`. (`cls_rgw_gc_urgent_data` uses `Nondeterministic: true`.)

**Interfaces:**
- Consumes: `denc`, `radosclient.Execer`, `radosclient.ReadOp`, `radosclient.WriteOp`, `radosclient.ExecResult`, `radosclient.OpFlagReturnVec`.
- Produces, following one pattern: request functions take an `Execer` (or the concrete op when a result is returned) and the release; reply types have a `Decode` reading from the `ExecResult`:

```go
package version   // internal/cls/version, class "version"
type ObjVersion struct{ Ver uint64; Tag string }                 // obj_version (1,1)
type Cond uint32
const ( CondNone Cond = 0; CondEQ Cond = 1; CondGT Cond = 2; CondGE Cond = 3; CondLT Cond = 4; CondLE Cond = 5; CondTagEQ Cond = 6; CondTagNE Cond = 7 )
func Set(op radosclient.Execer, v ObjVersion, r denc.Release)     // "set"
func Inc(op radosclient.Execer, r denc.Release)                    // "inc"
func IncConds(op radosclient.Execer, v ObjVersion, cond Cond, r denc.Release) // "inc_conds"
func Check(op radosclient.Execer, v ObjVersion, cond Cond, r denc.Release)    // "check_conds"; fails the op with ErrCanceled
func Read(op *radosclient.ReadOp, r denc.Release) *ReadResult      // "read"
func (res *ReadResult) Version() (ObjVersion, error)
const XattrName = "ceph.objclass.version"

package refcount  // class "refcount"
type Refcount struct{ Refs map[string]bool; RetiredRefs []string } // obj_refcount (2,1)
func Get(op radosclient.Execer, tag string, implicitRef bool, r denc.Release)
func Put(op radosclient.Execer, tag string, implicitRef bool, r denc.Release)
func Set(op radosclient.Execer, refs []string, r denc.Release)
func Read(op *radosclient.ReadOp, implicitRef bool, r denc.Release) *ReadResult
// NUL-terminated tags are the caller's business: radosgw appends "\x00" to the write tag
// before using it as a ref tag and as the idtag/tail_tag xattr value.

package user      // class "user"
type Bucket struct{ Name, Marker, BucketID, PlacementID string; DataPool, IndexPool, DataExtraPool string } // cls_user_bucket; (9,8) when PlacementID != "", else (7,3)
type BucketEntry struct{ Bucket Bucket; Size, SizeRounded, Count uint64; Mtime time.Time; UserStatsSync bool; CreationTime time.Time } // (9,5)
type Stats struct{ TotalEntries, TotalBytes, TotalBytesRounded uint64 }   // (1,1)
type Header struct{ Stats Stats; LastStatsSync, LastStatsUpdate time.Time } // (1,1)
func SetBucketsInfo(op radosclient.Execer, entries []BucketEntry, add bool, t time.Time, r denc.Release)
func RemoveBucket(op radosclient.Execer, b Bucket, r denc.Release)
func ListBuckets(op *radosclient.ReadOp, marker, endMarker string, max int32, r denc.Release) *ListResult
func (res *ListResult) Entries() ([]BucketEntry, string, bool, error)   // entries, next marker, truncated
func GetHeader(op *radosclient.ReadOp, r denc.Release) *HeaderResult
func CompleteStatsSync(op radosclient.Execer, t time.Time, r denc.Release)
func ResetStats2(op *radosclient.ReadOp, t time.Time, marker string, acc Stats, r denc.Release) *ResetResult // must run with OpFlagReturnVec

package rgw       // class "rgw"
type ObjKey struct{ Name, Instance string }                        // cls_rgw_obj_key (1,1); its own copy, not meta.ObjKey
type DirEntryMeta struct{ Category uint8; Size uint64; Mtime time.Time; ETag, Owner, OwnerDisplayName, ContentType string; AccountedSize uint64; UserData string; StorageClass string; AppendableValue bool; RestoreStatus uint8; RestoreExpiryDate time.Time } // (8,3); RestoreStatus and RestoreExpiryDate are v8 (main); decode gates on version
type DirEntry struct{ Key ObjKey; Ver EntryVer; Exists bool; Meta DirEntryMeta; PendingMap map[string]PendingInfo; Locator string; IndexVer uint64; Tag string; Flags uint16; VersionedEpoch uint64 } // (8,3), field order: key.name, ver.epoch, exists, meta, pending_map, locator, ver, index_ver, tag, key.instance, flags, versioned_epoch
type EntryVer struct{ Pool int64; Epoch uint64 }                  // rgw_bucket_entry_ver (1,1), packed varints (cls_rgw_types.h:230-311)
type PendingInfo struct{ State uint8; Timestamp time.Time; Op uint8 } // (2,2)
type CategoryStats struct{ TotalSize, TotalSizeRounded, NumEntries, ActualSize uint64 } // (3,2)
type DirHeader struct{ Ver, MasterVer uint64; Stats map[uint8]CategoryStats; Tag string; TagTimeout uint64; NewInstance InstanceEntry; SyncStopped bool; ReshardLogEntries uint32 } // (8,2) main, (7,2) Squid; ReshardLogEntries is v8
type InstanceEntry struct{ ReshardStatus uint8; NewBucketInstanceID string; NumShards int32 } // (3,1); status 0 not resharding, 1 in progress, 2 done, 3 in log record (main)
type Dir struct{ Header DirHeader; Entries map[string]DirEntry }  // rgw_bucket_dir (2,2)

const ErrBusyResharding = 2300
func GuardBucketResharding(op radosclient.Execer, retErr int32, r denc.Release)   // "guard_bucket_resharding"
func BucketInitIndex(op radosclient.Execer)                                       // "bucket_init_index", empty input
func BucketPrepareOp(op radosclient.Execer, p PrepareOp, r denc.Release)          // "bucket_prepare_op"; rgw_cls_obj_prepare_op (7,5): u8 op, tag, locator, bool log_op, key, u16 bilog_flags, zones_trace
func BucketCompleteOp(op radosclient.Execer, c CompleteOp, r denc.Release)        // "bucket_complete_op"; (9,7): u8 op, u64 ver.epoch, meta, op_tag, locator, remove_objs, ver, log_op, key, bilog_flags, zones_trace
type ModifyOp uint8
const ( OpAdd ModifyOp = 0; OpDel ModifyOp = 1; OpCancel ModifyOp = 2 )
func BucketList(op *radosclient.ReadOp, l ListOp, r denc.Release) *ListResult    // "bucket_list"; rgw_cls_list_op (6,4): u32 num_entries, filter_prefix, start_obj, bool list_versions, delimiter
func (res *ListResult) Result() (ListRet, error)                                   // rgw_cls_list_ret (4,2): dir, is_truncated, marker
func GetDirHeader(op *radosclient.ReadOp, r denc.Release) *ListResult             // bucket_list with num_entries 0
func SuggestChanges(op radosclient.Execer, changes []Suggestion, r denc.Release)  // "dir_suggest_changes": raw [u8 op, DirEntry] repeated; op 'r' (114) or 'u' (117), |0x80 to log
func BucketCheckIndex(op *radosclient.ReadOp, r denc.Release) *CheckIndexResult   // "bucket_check_index" -> rgw_cls_check_index_ret (1,1)
func BucketRebuildIndex(op radosclient.Execer)                                     // "bucket_rebuild_index"
func ObjRemove(op radosclient.Execer, keepAttrPrefixes []string, r denc.Release)  // "obj_remove" (1,1)
func ObjStorePGVer(op radosclient.Execer, attr string, r denc.Release)            // "obj_store_pg_ver" (1,1)
func ObjCheckAttrsPrefix(op radosclient.Execer, prefix string, failIfExist bool, r denc.Release) // (1,1)
func ObjCheckMtime(op radosclient.Execer, mtime time.Time, typ MtimeCheck, highPrecision bool, r denc.Release) // (2,1)
func UsageLogAdd(op radosclient.Execer, info UsageLogInfo, r denc.Release)         // "user_usage_log_add" (2,1)
func UsageLogRead(op *radosclient.ReadOp, q UsageReadOp, r denc.Release) *UsageReadResult // (2,1) -> ret (1,1)
func UsageLogTrim(op radosclient.Execer, t UsageTrimOp, r denc.Release)            // (3,2)
func UsageLogClear(op radosclient.Execer)                                          // "usage_log_clear", empty
func GCSetEntry(op radosclient.Execer, expirationSecs uint32, info GCObjInfo, r denc.Release) // "gc_set_entry" (1,1), the omap-era enqueue
type GCObjInfo struct{ Tag string; Chain []GCObj; Time time.Time }                // cls_rgw_gc_obj_info (1,1); chain is cls_rgw_obj_chain (1,1) of cls_rgw_obj (2,1): pool, key.name, loc, key

package gc        // class "rgw_gc"
func QueueInit(op radosclient.Execer, size, numDeferredEntries uint64, r denc.Release)  // "rgw_gc_queue_init"; cls_rgw_gc_queue_init_op (1,1)
func QueueEnqueue(op radosclient.Execer, expirationSecs uint32, info rgw.GCObjInfo, r denc.Release) // "rgw_gc_queue_enqueue"; reuses cls_rgw_gc_set_entry_op
func QueueList(op *radosclient.ReadOp, marker string, max uint32, expiredOnly bool, r denc.Release) *ListResult // "rgw_gc_queue_list_entries"
func QueueRemoveEntries(op radosclient.Execer, numEntries uint32, r denc.Release)
func QueueUpdateEntry(op radosclient.Execer, expirationSecs uint32, info rgw.GCObjInfo, r denc.Release)
```

Method name constants come from `src/cls/rgw/cls_rgw_const.h`, `src/cls/user/cls_user_ops.h`, `src/cls/version/cls_version_ops.h`, `src/cls/refcount/cls_refcount_ops.h` and `src/cls/rgw_gc/cls_rgw_gc_ops.h` on the v19.2.3 tag; request and reply structs from the matching `*_ops.h` and `*_types.h`. Every method listed is registered by Squid's classes. The only request struct whose version differs by release in this set is none in phase 0 (`rgw_cls_bucket_update_stats_op` is phase 1); reply and stored types with two versions (`rgw_bucket_dir_header` 7 and 8, `rgw_bucket_dir_entry_meta` 7 and 8) decode both and encode at `r`.

The user-class `Bucket` uses the (7,3) branch when `PlacementID` is empty, which is what `rgw_bucket::convert` produces, so a fresh radosgw's `.buckets` entries decode through that branch; the golden for `cls_user_bucket` exercises both.

- [ ] **Step 1: Goldens for every listed type; round-trip specs in each package** (JSON compared only for `rgw_bucket_dir_entry`, `rgw_bucket_dir_header`, `cls_user_bucket_entry`, `cls_user_header`, which `radosgw-admin bi list` and `user stats` print).
- [ ] **Step 2: Request-marshalling specs without a cluster.** For each request function, build a `radosclient.NewWriteOp()`, call the function, and assert `Steps()` holds one `ExecStep` with the right class and method and with `In` equal to the bytes of the corresponding golden case where one exists (for example `rgw_cls_obj_prepare_op`), decoding the golden into the Go request type first and re-encoding through the function.
- [ ] **Step 3: Run to fail, implement, run to pass.**
- [ ] **Step 4: Integration specs** in `internal/cls/rgw/rgw_integration_test.go` against the populated Squid cluster: `GetDirHeader` on shard 0 of the `plain` bucket decodes a header whose `Ver` is at least 1; `BucketList` across all eleven shards, merged by key, returns exactly the object keys the manifest lists, with `Exists` true, `Meta.Size` equal to the manifest size, and `Meta.ETag` a 32-hex string or a multipart etag with a dash; `version.Read` on the bucket instance object returns `Ver` 1 with a 24-character tag; `refcount.Read` on one tail object of `large.bin` returns a single ref tag ending in a NUL byte; `user.ListBuckets` on `alice.buckets` returns `plain`; `user.GetHeader` stats match the manifest's object count and size for that user.
- [ ] **Step 5: Commit per package** (`feat(cls/version): ...`, `feat(cls/refcount): ...`, `feat(cls/user): ...`, `feat(cls/rgw): ...`, `feat(cls/gc): ...`).

---

### Task 15: Phase 0 gate

**Files:**
- Create: `test/gate/gate_suite_test.go`, `test/gate/phase0_test.go`, `test/gate/manifest.go`
- Modify: `Makefile` (target `gate RELEASE=squid|tentacle`)

**Interfaces:**
- Consumes everything above. `manifest.go` decodes `hack/cluster/out/<release>/manifest.json` into a struct mirroring Task 13's JSON.

The gate is the spec's phase 0 acceptance: every metadata object the populated radosgw wrote decodes and re-encodes byte-identically, and the decoded forms agree with what `radosgw-admin` reports. It runs against both clusters; the Tentacle run uses `denc.Tentacle` from `RequiredOSDRelease`.

- [ ] **Step 1: Write the gate specs**

```go
var _ = Describe("phase 0 gate", Label("integration"), func() {
	var (
		cluster radosclient.Cluster
		release denc.Release
		m       Manifest
	)
	BeforeEach(func() { /* Connect via goceph; release from RequiredOSDRelease; m from RGW_GO_TEST_MANIFEST */ })

	roundTrip := func(pool radosclient.Pool, oid string, decode func(*denc.Decoder) any, encode func(*denc.Encoder, any)) {
		op := radosclient.NewReadOp()
		data := op.Read(0, 64<<20)
		Expect(pool.Read(ctx, oid, op, 0)).To(Succeed(), oid)
		d := denc.NewDecoder(data.Data[:data.N])
		v := decode(d)
		Expect(d.Err()).NotTo(HaveOccurred(), oid)
		Expect(d.Remaining()).To(BeZero(), "trailing bytes in %s", oid)
		e := denc.NewEncoder()
		encode(e, v)
		Expect(e.Bytes()).To(Equal(data.Data[:data.N]), "re-encoding %s", oid)
	}

	It("round-trips every root pool object", func() { /* zone_info.<id>, zone_names.default, default.zone.<realm>, zonegroup_info.<id>, zonegroups_names.default, default.zonegroup.<realm>; realm and period objects when present */ })
	It("round-trips every user object and its xattrs", func() { /* users.uid/<uid> as UserObject; ceph.objclass.version xattr as version.ObjVersion; users.keys/<ak> as UID; users.email when set */ })
	It("round-trips bucket entry points and instances with their xattrs", func() { /* root/<name> as BucketEntryPoint; root/.bucket.meta.<...> as BucketInfo; user.rgw.acl as acl.Policy; objv xattr */ })
	It("decodes every index shard header and entry", func() { /* for each bucket and shard: GetDirHeader, BucketList; every entry re-encodes byte-identically to what a second BucketList returns for it (the OSD re-encodes, so compare decoded forms against the manifest instead of bytes) */ })
	It("round-trips head object xattrs for every populated object", func() { /* GetXattrs on <marker>_<oid>: manifest, acl, etag (32 hex or multipart form), idtag ends in NUL, tail_tag equals idtag when present, pg_ver decodes as u64, source_zone as u32 */ })
	It("finds every tail object the manifest names", func() { /* for large.bin and multipart.bin: Manifest.Stripes() names exist in the data pool and their sizes sum to the object size */ })
	It("agrees with radosgw-admin", func() { /* podman exec rgw-go-mon radosgw-admin metadata get user:alice; compare the "data" object to json.Marshal(UserInfo) after canonicalization; same for bucket:plain and bucket.instance:plain:<id> */ })
})
```

- [ ] **Step 2: Run against Squid, fix what fails, run against Tentacle**

```sh
make gate RELEASE=squid
make gate RELEASE=tentacle
```

Expected: green on both. A byte mismatch here is a real finding about an encoder; fix the encoder, regenerate nothing.

- [ ] **Step 3: Commit** `test(gate): add the phase 0 round-trip gate against populated Squid and Tentacle clusters`.

---

### Task 16: Integration workflow

**Files:**
- Create: `.github/workflows/integration.yml`

**Interfaces:** none new.

On `workflow_dispatch` and a nightly `schedule`, one job per release in a matrix of `squid` and `tentacle`, `runs-on: ubuntu-latest`, `timeout-minutes: 45`, `permissions: contents: read`, `concurrency` keyed on `${{ github.workflow }}-${{ github.ref }}` with `cancel-in-progress: true`. Steps: checkout with `persist-credentials: false`; setup-go with `go-version-file: go.mod`; install `librados-dev`, `podman`, `awscli` and `jq`; `make cluster-up-<release>`; `make populate RELEASE=<release>`; `go test -tags integration -race ./... -ginkgo.label-filter=integration`; `make gate RELEASE=<release>`; always `make cluster-down`. Pin every action with pinact, run actionlint, and add the workflow to the README's Development section as the on-demand integration run. It does not gate pull requests.

- [ ] **Step 1: Write the workflow, pin, lint.** **Step 2:** trigger it once with `gh workflow run integration.yml`, watch it, fix what the runner environment breaks (rootless podman and `network_mode: host` are the likely spots). **Step 3:** commit `ci: add the nightly and on-demand integration workflow`.

---

## Self-review

- **Spec coverage.** Spec section 8 (`denc`, version rules, class packages, layout as constraints) maps to Tasks 2 to 9 and 14; section 4's seam and its goceph implementation to Tasks 10 and 12; section 7's three completion modes to Tasks 11 and 12; section 13's fork items, all eleven bindings plus the return-vector path, to Task 11; section 9's phase 0 gate to Task 15; section 10's disposable clusters and integration layer to Tasks 13 and 16; section 12's scaffold, CI additions and CLAUDE.md to Task 1. The seam microbenchmark from section 11 is deliberately phase 1, since it needs the driver's op shapes to be meaningful; Task 12's thread-count spec covers the phase 0 form of that question.
- **Types across tasks.** `denc.Release`, `denc.Squid`, `denc.Tentacle`, `radosclient.Execer`, `radosclient.ExecResult`, `radosclient.OpFlagReturnVec`, `goldentest.RoundTrip`, `meta.Owner`, `meta.UserID`, `rgw.GCObjInfo` are spelled the same wherever they appear.
- **Review Focus.** Each of the five lines names the task whose tests pin it.
