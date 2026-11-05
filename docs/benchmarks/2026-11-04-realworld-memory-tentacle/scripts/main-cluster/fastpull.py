#!/usr/bin/env python3
"""fastpull.py: fetches the pinned Ceph main image's manifest and blobs from quay.ceph.io over many connections
(ranged GETs, because a single connection got ~110 KB/s here and many got several times that), checks every
blob's and the manifest's sha256 against the pinned digest, and writes a skopeo `dir:` layout. The layout is
then pushed to the rgw-go-main cluster's registry with skopeo, which keeps the manifest bytes, so the image
there has the pinned digest.
"""
import concurrent.futures as cf
import hashlib
import json
import os
import subprocess
import sys

REPO = "ceph-ci/ceph"
DIGEST = "sha256:c928697602b7feffdaef0b59d95a7e94a864e4dd89d33a867e0e48bac0fe7e06"
OUT = sys.argv[1]
CHUNK = 16 * 1024 * 1024
CONNS = 24


def token():
    r = subprocess.run(["curl", "-s", f"https://quay.ceph.io/v2/auth?service=quay.ceph.io&scope=repository:{REPO}:pull"],
                       capture_output=True, text=True, check=True)
    return json.loads(r.stdout)["token"]


TOK = token()


def get(url, rng=None, accept=None, retries=8):
    global TOK
    for i in range(retries):
        cmd = ["curl", "-sSL", "--fail", "--max-time", "300", "-H", f"Authorization: Bearer {TOK}"]
        if rng:
            cmd += ["-H", f"Range: bytes={rng[0]}-{rng[1]}"]
        if accept:
            cmd += ["-H", f"Accept: {accept}"]
        cmd.append(url)
        r = subprocess.run(cmd, capture_output=True)
        if r.returncode == 0:
            return r.stdout
        print(f"retry {i} {url} {rng}: curl exit {r.returncode}", file=sys.stderr, flush=True)
        TOK = token()
    raise SystemExit(f"giving up on {url} {rng}")


os.makedirs(OUT, exist_ok=True)
manifest = get(f"https://quay.ceph.io/v2/{REPO}/manifests/{DIGEST}", accept="application/vnd.oci.image.manifest.v1+json")
got = "sha256:" + hashlib.sha256(manifest).hexdigest()
if got != DIGEST:
    raise SystemExit(f"manifest digest {got} is not the pinned {DIGEST}")
print("manifest digest ok", flush=True)
open(os.path.join(OUT, "manifest.json"), "wb").write(manifest)
open(os.path.join(OUT, "version"), "w").write("Directory Transport Version: 1.1\n")
m = json.loads(manifest)
blobs = [m["config"]] + m["layers"]
for b in blobs:
    path = os.path.join(OUT, b["digest"].split(":")[1])
    size = b["size"]
    if os.path.exists(path) and os.path.getsize(path) == size:
        h = hashlib.sha256(open(path, "rb").read()).hexdigest() if size < (1 << 30) else None
        if h == b["digest"].split(":")[1]:
            print(f"{b['digest'][:19]} already complete", flush=True)
            continue
    url = f"https://quay.ceph.io/v2/{REPO}/blobs/{b['digest']}"
    ranges = [(o, min(o + CHUNK, size) - 1) for o in range(0, size, CHUNK)]
    print(f"{b['digest'][:19]} {size} bytes in {len(ranges)} ranges", flush=True)
    with open(path, "wb") as f:
        f.truncate(size)
    done = 0
    with cf.ThreadPoolExecutor(CONNS) as ex:
        futs = {ex.submit(get, url, r): r for r in ranges}
        with open(path, "r+b") as f:
            for fut in cf.as_completed(futs):
                lo, hi = futs[fut]
                data = fut.result()
                if len(data) != hi - lo + 1:
                    raise SystemExit(f"short range {lo}-{hi}: {len(data)}")
                f.seek(lo)
                f.write(data)
                done += len(data)
                if (done // CHUNK) % 8 == 0:
                    print(f"  {done / 1e6:.0f} of {size / 1e6:.0f} MB", flush=True)
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for blk in iter(lambda: f.read(1 << 20), b""):
            h.update(blk)
    if "sha256:" + h.hexdigest() != b["digest"]:
        raise SystemExit(f"blob {b['digest']} has sha256 {h.hexdigest()}")
    print(f"{b['digest'][:19]} verified", flush=True)
print("all blobs verified", flush=True)
