# rgw-go

[![workflow-lint](https://github.com/jhoblitt/rgw-go/actions/workflows/workflow-lint.yml/badge.svg)](https://github.com/jhoblitt/rgw-go/actions/workflows/workflow-lint.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/jhoblitt/rgw-go/badge)](https://scorecard.dev/viewer/?uri=github.com/jhoblitt/rgw-go)

rgw-go is an experimental reimplementation of Ceph's RADOS Gateway in Go,
built on [ceph/go-ceph](https://github.com/ceph/go-ceph). Its goal is to be a
drop-in replacement for C++ RGW in a Rook cluster running Squid or Tentacle:
bit-compatible with RGW's RADOS layout, able to coexist with radosgw on the
same zone, administered by the unmodified radosgw-admin, and benchmarked
against C++ RGW and [rgw-rs](https://github.com/jhoblitt/rgw-rs). The design
is in [docs/superpowers/specs/2026-09-25-rgw-go-design.md](docs/superpowers/specs/2026-09-25-rgw-go-design.md);
what is out of scope, and why, is in [docs/exclusions.md](docs/exclusions.md).

## Install

Nothing to install yet. rgw-go is in its foundations phase.

## Usage

Not yet usable. The first runnable milestone is the phase 1 gateway described in the design spec.

## Development

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/);
commitlint enforces this on every pull request. Every GitHub Action is pinned
to a commit SHA: run `pinact run` after editing a workflow and `actionlint`
before committing it. Where a `Makefile` is present, `make check` is the local gate,
`make tools` installs the pinned linter, and that pin lives in the `Makefile`.

## License

[LGPL-2.1-or-later](LICENSE)
