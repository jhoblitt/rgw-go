# rgw-go

[![ci](https://github.com/jhoblitt/rgw-go/actions/workflows/ci.yml/badge.svg)](https://github.com/jhoblitt/rgw-go/actions/workflows/ci.yml)
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

The integration suite and the phase 0 gate run against disposable one-worker Rook
clusters, one each for Squid and Tentacle, which [rooket](https://github.com/jhoblitt/rooket)
stands up on kind. The `integration` workflow runs them nightly and on demand with
`gh workflow run integration.yml`. It does not gate pull requests; locally, the same
steps are in [hack/rooket/README.md](hack/rooket/README.md).

The `s3tests` workflow runs on every pull request. When the pull request changes the
S3 path, anything under `internal/`, `cmd/`, `hack/s3tests/`, `hack/rooket/`,
`hack/parity/` or `test/s3tests/`, `go.mod`, `go.sum`, or the workflow and its
cluster action, it brings up a Squid cluster, runs the phase 1 s3-tests set against
rgw-go and compares the outcomes with radosgw's recorded baseline,
`make s3tests-parity` as it runs locally; otherwise it passes without a cluster. Its
`s3-tests` job is the check to require. `gh workflow run s3tests.yml -f release=tentacle`
runs Tentacle's comparison on demand.

Bumping the s3-tests pin (`hack/s3tests/run.sh`), the Ceph pin, or a deselect list
needs both releases' baselines re-recorded in the same pull request, since the
comparison refuses a baseline recorded under another s3-tests commit, Ceph version
or deselect lists. `gh workflow run s3tests.yml --ref <branch> -f record-baseline=true`
and the same with `-f release=tentacle` record radosgw's baseline on the runner,
upload it as the artifact `s3tests-baseline-<release>`, and compare rgw-go with it;
commit the artifact's file as `test/s3tests/baseline/<release>.json`. Locally,
`make s3tests-record RELEASE=<release>` writes the same file.

A `v*` tag runs the `release` workflow. goreleaser builds rgw-go with cgo once per Ceph
release, in a builder made from that release's Ceph image (`hack/image/cgo-build.sh`).
For each release it publishes a `linux/amd64` archive and the derived Ceph image
`ghcr.io/jhoblitt/rgw-go`, tagged `<version>-ceph-<Ceph tag>` and `<version>-<release>`.
The floating `<release>` tag follows final releases only, and a tag such as
`v0.1.0-rc.1` publishes a GitHub prerelease. Each release carries checksums, the
archives' SBOMs and keyless cosign signatures over the checksums and the images.

`.goreleaser.yaml` repeats each release's Ceph image and the build tags, and
`make release-pins-check`, part of `make check` and of CI, fails until they match the
chart values in `hack/rooket/<release>/values/` and `GO_TAGS` in the `Makefile`.
`goreleaser release --snapshot --clean --skip=publish,sign,sbom` builds everything
locally without publishing; its image tags end in `-amd64`, such as
`ghcr.io/jhoblitt/rgw-go:squid-amd64`. The workflow sets `RGW_GO_ENGINE=docker`, so
the binaries build with the engine goreleaser builds the images with.

## License

[LGPL-2.1-or-later](LICENSE)
