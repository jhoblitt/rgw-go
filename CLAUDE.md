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
- go-ceph comes from the jhoblitt/go-ceph fork through a `replace` in go.mod pinned to a
  commit on the fork's `rgw-go` integration branch; Dependabot does not follow a replace, so it
  is bumped by hand. Every pinned ref must be on `rgw-go`, and
  every change reaches `rgw-go` by PR into the fork, merged on green fork CI. Any PR opened
  on upstream ceph/go-ceph is merged into `rgw-go` first, unmodified, so its SHAs match. The fork's APIs sit behind the `ceph_preview` build tag,
  which every build, test and lint passes (Makefile `GO_TAGS`, .golangci.yml, CI).
- `docs/cgo-limitations.md` is the registry of go-ceph, librados and cgo limitations for
  the pure-Go question; add or update an entry whenever a task, review or benchmark finds one.
- `docs/rados-format-opportunities.md` is the registry of changes to radosgw's RADOS formats
  that would improve performance while staying S3-compliant; add an entry whenever work meets
  one. rgw-go keeps the formats byte-identical, and recording an entry never blocks a task.
- `docs/ceph-upstream-bugs.md` is the registry of defects in upstream ceph/ceph that rgw-go
  meets (radosgw, the RGW classes, librados, librbd, the OSD, the monitors); add or update an
  entry, with evidence at a named release tag, whenever work meets one, and share it with
  rgw-rs. Every entry's **Upstream** line links its tracker.ceph.com issues and upstream PRs;
  update it in the same change that files, finds or closes one.
- Every behaviour that differs from radosgw is recorded in `docs/exclusions.md` in the same
  change that introduces it.
- `docs/porting-process.md` is the registry of planning and orchestration problems in the port;
  add or update an entry whenever planning or managing the work costs rework, stalls, or needs the
  owner. A process and planning review reads it at the end of each phase, before the next phase's
  tasks are planned, and again at the end of phase 3, which also decides on skill changes.
- Never use the ambient kubectl or Ceph cluster. Cluster tests use disposable rooket clusters,
  `make cluster-up RELEASE=squid|tentacle` (hack/rooket/), reached only through `rooket k`
  with `ROOKET_NAME=rgw-go-<release>`.
- Implementation tasks go to Opus 5.5 code-workers in worktrees; judgment stays on the
  session model.
- `docs/exclusions.md` is canonical for rgw-rs too: announce every material change to the
  rgw-rs Claude session.
- Design: docs/superpowers/specs/2026-09-25-rgw-go-design.md. Plans: docs/superpowers/plans/.

## Working rules (end-of-phase-1 review, docs/porting-process.md)

- A review finding is fixed in the PR whose review found it. One truly outside the PR's change
  becomes a numbered plan task before that PR merges; a ledger line is never its only record
  (PP-12).
- Unrelated changes never share a PR, and one task may ship as several PRs (PP-13).
- Every defect found is public: it goes into `docs/ceph-upstream-bugs.md`, and each possible
  security defect is also sent to the rgw-bug-reproduction session. Never ask the owner about
  disclosure, and never link that session's private disclosure gists from the registry (PP-4).
- The parity ledgers and their tooling are a regression test, not a security boundary: harden
  them against honest mistakes only. Adversarial review is for the security boundaries the spec
  names (PP-14).
- An instruction to act once a condition holds is the go-ahead: when it holds, check what can be
  observed (agents drained, sessions idle, load, running processes) and act, without asking
  again (PP-16).
- Every rgw-go fix, where it is reasonable, carries an s3-tests test that covers it, merged into
  jhoblitt/ceph's `rgw-go-s3tests` branch as part of the task. A task that changes s3-tests also
  moves the s3-tests pin itself, with the known-differences and deselect lines the move needs; no
  note defers the move to a later bump (PP-19).
- A citation of Ceph source is a GitHub link pinned to the tag or commit it was written against:
  in Markdown a link such as
  [rgw_op.cc:6902-6904](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6902-L6904),
  in a Go comment the raw URL. rgw-go's own code is cited as a plain `path:line`. `make citecheck`
  checks them, and no other notation is used. Until unit CL's sweep (CL4) reaches a package, its
  Go comments keep their plain form, unchecked.
