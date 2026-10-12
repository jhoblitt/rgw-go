# The Rook gate

The `rook` workflow (`.github/workflows/rook.yml`) runs Rook's own
object-store end-to-end suite, `TestCephObjectSuite`, against the derived
image (`hack/image/build.sh`), in which rgw-go is `/usr/bin/radosgw`. It runs
nightly and on demand, `gh workflow run rook.yml`, on jhoblitt/rgw-go only,
and does not gate pull requests. Each of the four jobs is one Ceph release,
Squid or Tentacle, and one of the suite's two passes, `TestWithoutTLS` or
`TestWithTLS`, run as Rook's CI runs them: Rook checked out at `ROOK_COMMIT`,
Rook's cluster setup action on kind, and Rook's `go test` invocation.

No run has passed yet. The first showed that the derived image builds, that
the operator accepts it, and that the object store and its canary user
become Ready; everything past that is checked only against Rook's sources at
the pin.

A failed or cancelled job uploads Rook's logs as the artifact
`rook-<release>-<pass>`. The artifact is public, and a failed suite leaves
its cluster up, so before the upload the job deletes the two kinds of file
`tests/scripts/collect-logs.sh` fills with cephx keys, `ceph-auth-ls.txt`
and the `secret-get--*` dumps. In every other file it redacts, in place,
each keyring entry (`key = ` and the word after it) and each whole cephx key
in the AES or AES256KRB5 encoding, which the pod descriptions and `kubectl
get all` print; the toolbox's keyring template counts as an entry. A guard
then blocks the upload if any file still holds `key = ` or a key in either
encoding, including one inside a longer base64 run, which the redaction
leaves alone.

## The phase 1 subset

`phase1.patch` removes three of the suite's tests from
`tests/integration/ceph_object_test.go`, with their imports:

| Removed | Needs | Gate |
|---|---|---|
| `bucketlifecycle.TestObjectBucketClaimLifecycle` | bucket lifecycle | phase 2 |
| `topickafka.TestBucketTopicKafka` | bucket notification topics | phase 3 |
| `notification.TestBucketNotification` | bucket notifications | phase 3 |

rgw-go does not implement those features yet, and `runObjectE2ETest` calls
every test unconditionally, so without the patch the suite would fail on them
every night and say nothing about the rest. What remains is the phase 1 gate:
`zonepools`, `bucket/{owner,policy,quota,rw}`,
`user/{caps,keys,opmask,placement,storageclass}`, `cosi` (it skips itself in
the TLS pass) and `dependents`. The full, unpatched suite is the phase 3
gate.

The patch makes two more changes so that the kept tests can run:

- `GetDockerImage` (`tests/framework/utils/k8s_helper.go`) skips its
  `docker pull` when `docker image inspect` finds the image. The installer
  pulls the Ceph image before it installs Rook, and the registry denies a
  pull of the derived image, which is never pushed.
- The shared store fixture
  (`tests/integration/object/util/sharedstore/sharedstore.go`) creates no
  SNS client. The client's sanity check calls `ListTopics`, which rgw-go
  answers with 405 until it implements bucket notifications, and only the
  Kafka topic test, removed above, uses the client. This change comes out
  when notifications land, with the topic and notification tests.

## Pointing the suite at the image

Rook's installer takes the Ceph image only from its per-release test-image
constants (`tests/framework/installer/ceph_installer.go`, `ReturnCephVersion`,
keyed by `CEPH_SUITE_VERSION`). `set-image.sh <release> <image> <rook checkout>`
rewrites the release's constant to the derived image and vets the package. It
fails unless it finds exactly one `<const> = "quay.io/ceph/ceph:vNN"`, so a
renamed or reshaped constant stops the run instead of leaving the suite on
stock Ceph.

The workflow then checks what actually ran: the suite runs with
`TEST_LOG_COLLECTION_LEVEL=all`, so Rook describes every pod in `object-ns`
before its teardown even on a passing run, and the job fails unless one of
those pods ran the derived image and none ran a `quay.io/ceph/ceph` image.

## Moving `ROOK_COMMIT`

`ROOK_COMMIT` in the workflow is a commit on rook/rook's `master`, bumped by
hand. `git apply --check` stops the job when the patch no longer applies. To
move it, in a scratch clone of rook/rook (`RGW_GO` is this checkout):

```sh
git checkout <new commit>
git apply --check "${RGW_GO}/hack/rook/phase1.patch" && git apply "${RGW_GO}/hack/rook/phase1.patch"
# When it does not apply: check out the files again, redo the changes in
# "The phase 1 subset" by hand, and regenerate the patch.
git diff >"${RGW_GO}/hack/rook/phase1.patch"
GOFLAGS=-tags=ceph_preview go vet ./tests/framework/utils/ ./tests/integration/ \
  ./tests/integration/object/util/sharedstore/
"${RGW_GO}/hack/rook/set-image.sh" squid ghcr.io/jhoblitt/rgw-go:local-v19.2.6 .
"${RGW_GO}/hack/rook/set-image.sh" tentacle ghcr.io/jhoblitt/rgw-go:local-v20.2.4 .
```

Also check, at the new commit, what the workflow copies from Rook's own
object suite jobs (`.github/workflows/integration-suites/object*/action.yaml`,
`.github/workflows/ceph-suite-integration-test.yml` and their callers): the
pass names, the `object-ns` and `object-ns-system` namespaces, the `go test`
flags, the `kubernetes-version` passed to
`.github/workflows/integration-test-setup-cluster-resources`, the runner, and
the files `tests/scripts/collect-logs.sh` writes, for any new one that holds
keys.
