<!-- go-conventions:begin -->
## Go conventions

This repository follows the go-conventions plugin: `/plugin marketplace add
jhoblitt/conventions-claude`, then `/plugin install
go-conventions@conventions-claude`, and load `go-conventions:go-conventions`
before writing Go here.

- Module: `github.com/jhoblitt/rgw-go`
- Binary: `rgw-go` (`cmd/rgw-go`)
- Environment prefix: `RGW_GO_`
<!-- go-conventions:end -->

## rgw-go facts

- cgo: every build and test compiles go-ceph and needs librados headers. On this machine
  `librados-devel` is absent; use `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include`
  and `CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go` where that directory holds
  `librados.so -> /usr/lib64/librados.so.2`. CI installs `librados-dev`.
- go-ceph comes from the jhoblitt/go-ceph fork through a `replace` in go.mod pinned to
  commit dd2812cd6f0a on branch rgw-go/rados-async-and-steps; Dependabot does not follow a
  replace, so it is bumped by hand. The fork's APIs sit behind the `ceph_preview` build tag,
  which every build, test and lint passes (Makefile `GO_TAGS`, .golangci.yml, CI).
- `docs/cgo-limitations.md` is the registry of go-ceph, librados and cgo limitations for
  the pure-Go question; add or update an entry whenever a task, review or benchmark finds one.
- Never use the ambient kubectl or Ceph cluster. Cluster tests use `make cluster-up-squid`
  and `make cluster-up-tentacle` (hack/cluster/).
- Implementation tasks go to Opus 5.5 code-workers in worktrees; judgment stays on the
  session model.
- `docs/exclusions.md` is canonical for rgw-rs too: announce every material change to the
  rgw-rs Claude session.
- Design: docs/superpowers/specs/2026-09-25-rgw-go-design.md. Plans: docs/superpowers/plans/.
