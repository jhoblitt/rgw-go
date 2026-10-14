package citecheck_test

import "strings"

// fixtureGo is src/rgw/rgw_gofix.cc at v19.2.6, 28 lines: RGWGoFix::prepare
// at lines 4-11, which opens the index pool at line 6, and
// RGWGoFix::complete at lines 13-28, whose retry loop of max_retries tries
// of write_meta runs at lines 17-23 and whose failure is logged at 24-26.
const fixtureGo = `// A fixture for citecheck's Go specs.
#include "rgw_gofix.h"

int RGWGoFix::prepare(optional_yield y)
{
  int r = open_bucket_index_pool(y);
  if (r < 0) {
    return r;
  }
  return 0;
}

int RGWGoFix::complete(optional_yield y)
{
  const int max_retries = 15;
  int r = 0;
  for (int i = 0; i < max_retries; i++) {
    r = write_meta(y);
    if (r >= 0) {
      break;
    }
    sleep(1);
  }
  if (r < 0) {
    log_failure(r);
  }
  return r;
}
`

// fixtureGoHelpers is what v20.2.4 inserts at line 4 of rgw_gofix.cc, which
// moves every line from there on down ten: prepare to 14-21 and complete to
// 23-38.
const fixtureGoHelpers = `static int open_quietly(optional_yield y)
{
  return open(y);
}

static int unused_helper()
{
  return 0;
}

`

// fixtureOp is src/rgw/rgw_op_fix.h at both releases: class RGWOp at lines
// 4-14, which names RGWOp on every line from 9 to 12.
const fixtureOp = `// A base op for citecheck's Go specs.
#pragma once

class RGWOp {
 public:
  virtual ~RGWOp() = default;
  virtual int verify() = 0;
  virtual const char* name() const = 0;
  RGWOp* next = nullptr;
  RGWOp* prev = nullptr;
  RGWOp* parent = nullptr;
  RGWOp* child = nullptr;
  int depth = 0;
};
`

// newGoFixture returns the ceph fixture with the two releases the [S] and
// [T] markers name: v19.2.6, which adds rgw_gofix.cc; rgw_op_fix.h;
// rgw_twin.cc, a base name two files under src/rgw/ have, twin_alpha's and
// twin_beta's; and rgw_side.cc, one under src/rgw/ and one under src/test/;
// and v20.2.4, which moves rgw_gofix.cc's code down ten lines and adds
// rgw_new.cc.
func newGoFixture() *cephFixture {
	r := newCephFixture()
	r.write("src/rgw/rgw_gofix.cc", fixtureGo)
	r.write("src/rgw/rgw_op_fix.h", fixtureOp)
	r.write("src/rgw/driver/rados/rgw_twin.cc", "// twin A\nint twin_alpha() { return 1; }\n")
	r.write("src/rgw/services/rgw_twin.cc", "// twin B\nint twin_beta() { return 2; }\n")
	r.write("src/rgw/rgw_side.cc", "// side\nint side_rgw() { return 1; }\n")
	r.write("src/test/rgw_side.cc", "// side test\nint side_test() { return 1; }\n")
	r.commit("squid")
	r.git("tag", "v19.2.6")
	lines := strings.SplitAfter(fixtureGo, "\n")
	r.write("src/rgw/rgw_gofix.cc", strings.Join(lines[:3], "")+fixtureGoHelpers+strings.Join(lines[3:], ""))
	r.write("src/rgw/rgw_new.cc", "// new\nint fresh() { return 0; }\n")
	r.commit("tentacle")
	r.git("tag", "v20.2.4")
	return r
}

// cephURL returns the link to lines of ceph/ceph at ref.
func cephURL(ref, path, lines string) string {
	return "https://github.com/ceph/ceph/blob/" + ref + "/" + path + "#" + lines
}
