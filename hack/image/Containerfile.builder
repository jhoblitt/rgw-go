# The build stage for rgw-go: the target Ceph image itself, so the binary
# links against the librados and glibc it will run with, plus librados's
# headers, git for the version stamp and the Go toolchain. gcc is already in
# the Ceph image (packages.txt in ceph.git's container/Containerfile). The
# install adds librados-devel, git-core and less; the git package would add
# perl and some sixty other packages go does not need.
# CEPH_IMAGE has no default: a release's Ceph image is pinned only in
# hack/rooket/<release>/values/, so a build that omits it must fail.
ARG CEPH_IMAGE
FROM docker.io/library/golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS go
FROM ${CEPH_IMAGE}
# The Ceph image deletes its ceph repo file after installing (container/Containerfile
# in ceph.git, "CLEAN UP!"), so librados-devel comes from the release's repo.
# librados-devel requires librados2 at its own exact build (ceph.spec.in), so
# it is pinned to the build the image carries; any other would make dnf
# replace the librados the binary will run with.
RUN version=$(rpm -q --queryformat '%{VERSION}' librados2) && \
    build=$(rpm -q --queryformat '%{EPOCHNUM}:%{VERSION}-%{RELEASE}' librados2) && \
    rpm --import https://download.ceph.com/keys/release.asc && \
    printf '[ceph]\nname=ceph\nbaseurl=https://download.ceph.com/rpm-%s/el9/$basearch\nenabled=1\ngpgcheck=1\ngpgkey=https://download.ceph.com/keys/release.asc\n' "${version}" >/etc/yum.repos.d/ceph.repo && \
    dnf install -y --setopt=install_weak_deps=False "librados-devel-${build}" git-core && \
    dnf clean all
COPY --from=go /usr/local/go /usr/local/go
# GOTOOLCHAIN=local fails a build whose go.mod needs a newer Go than this one
# instead of downloading that toolchain.
ENV PATH=/usr/local/go/bin:${PATH} GOTOOLCHAIN=local
