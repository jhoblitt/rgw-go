package citecheck_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fixtureCC is src/rgw/rgw_fixture.cc at v1.0.0, 43 lines: Read::prepare
// at lines 6-13, its body at 7-13 with a brace in a block comment;
// Write::prepare at 15-19, with a brace in a string literal; and, after the
// namespace, Store's constructor at 23-25, with braces in its member
// initializers, Store::lex at 27-38, whose body holds a brace in each of the
// constructs the lexer must leave out of the pairing, and Store::after at
// 40-43. Each brace, paired, would end lex early or run it past after.
const fixtureCC = `// A fixture for citecheck's specs.
#include "rgw_fixture.h"

namespace rgw::fixture {

int Store::Object::Read::prepare(optional_yield y)
{
  int r = open(y); /* } */
  if (r < 0) {
    return r;
  }
  return 0;
}

int Store::Object::Write::prepare(optional_yield y)
{
  const char* brace = "}";
  return write_meta(y, brace);
}

} // namespace rgw::fixture

rgw::fixture::Store::Store(int n) : shards{n}, cache{} {
  init();
}

int Store::lex()
{
#define LEX_BEGIN(x) \
  do {
  char open = '{';
  // a stray {
  const char* js = R"js(a "}" {
b)js";
  const char* u8 = u8R"x(")}")x";
  int n = 1'000; char again = '{';
  return n + open + again;
}

int Store::after()
{
  return 0;
}
`

// fixtureCCHelper is what v2.0.0 inserts at line 6 of the .cc, which moves
// Read::prepare down five lines, to 11-18, and puts open_quietly at 6-9.
const fixtureCCHelper = `static int open_quietly(optional_yield y)
{
  return open(y);
}

`

// fixtureH is src/rgw/rgw_fixture.h: the classes; Read::prepare declared at
// line 10; Read::iterate defined in its class at lines 11-20, with a brace
// in each branch of a conditional; Read::size at line 21; an overload of
// Read::prepare defined at lines 22-28, its header on two lines and an if in
// its body that calls Write::prepare; Read::retry at line 29, which calls it
// too; Write, whose head at line 31 names its base, with Write::prepare
// defined in its class at lines 32-34, a member initializer that calls
// Read::prepare at line 35, and Read's retry named in a comment at line 36 and
// in a directive at 37; and, after Store, Processor, whose constructor at
// lines 44-46, after an access label, calls Store's in its member-initializer
// list; whose name, at line 47, returns a qualified type; whose base, at
// line 48, names its type after "->"; whose default member initializer at
// line 49 calls a function; whose attach, declared at line 50, takes a Base;
// whose wait, a member template with a default template argument, is defined
// at lines 51-54; and whose items, at line 55, a template's type, are
// initialized by a call.
const fixtureH = `#pragma once

namespace rgw::fixture {

class Store {
 public:
  class Object {
   public:
    struct Read {
      int prepare(optional_yield y);
      int iterate(int ofs) {
#ifdef ITERATE_CHECKED
        if (ofs < 0) {
#else
        if (ofs <= 0) {
#endif
          return -EINVAL;
        }
        return ofs;
      }
      int size() const { return 1; }
      int prepare(optional_yield y,
                  int tries) {
        if (Write::prepare(y) < 0) {
          return tries;
        }
        return 0;
      }
      int retry(optional_yield y) { return Write::prepare(y); }
    };
    struct Write : public Base {
      int prepare(optional_yield y) {
        return flush(y);
      }
      int cached = count(Read::prepare(0));
      // retry() is Read's, not Write's
#define WRITE_RETRY(r) (r).retry()
    };
  };
};

class Processor : public Store {
 public:
  Processor(int n)
    : Store(n),
      shards(n) {}
  std::string name() const { return "processor"; }
  auto base() const -> Base;
  int total = compute(1);
  void attach(Base b);
  template <class E = int>
  int wait(E e) {
    return flush(e);
  }
  std::vector<int> items = make(1);
};

} // namespace rgw::fixture
`

// cephFixture is a git repository standing in for the ceph/ceph clone:
// v1.0.0, a lightweight tag, and v2.0.0, an annotated one, on the branch
// main, which the remote-tracking branch origin/main follows. Its dates and
// object format are fixed, so that every object id, and so every
// abbreviation a spec relies on, is the same on every run.
type cephFixture struct {
	dir string
	// v1 is v1.0.0's commit id.
	v1 string
}

func newCephFixture() *cephFixture {
	r := &cephFixture{dir: GinkgoT().TempDir()}
	r.git("init", "-q", "-b", "main", "--object-format=sha1")
	// git commit starts git maintenance run --auto --detach, whose detached
	// process works in .git/objects after the commit returns and can race the
	// spec's removal of the fixture; the repository's own config turns it off
	// for every git run there, the code under test's included.
	r.git("config", "maintenance.auto", "false")
	r.git("config", "gc.auto", "0")
	r.write("src/rgw/rgw_fixture.cc", fixtureCC)
	r.write("src/rgw/rgw_fixture.h", fixtureH)
	r.v1 = r.commit("v1")
	r.git("tag", "v1.0.0")
	lines := strings.SplitAfter(fixtureCC, "\n")
	r.write("src/rgw/rgw_fixture.cc", strings.Join(lines[:5], "")+fixtureCCHelper+strings.Join(lines[5:], ""))
	r.commit("v2")
	r.git("tag", "-a", "-m", "v2.0.0", "v2.0.0")
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")
	return r
}

// blobSharing writes a blob whose id starts with the first four hex digits of
// commit's, and returns those digits: an abbreviation unique among the
// fixture's commits that git cannot resolve without a hint that a commit is
// meant.
func (r *cephFixture) blobSharing(commit string) string {
	prefix := commit[:4]
	for i := 0; ; i++ {
		data := fmt.Sprintf("collision %d\n", i)
		sum := sha1.Sum(fmt.Appendf(nil, "blob %d\x00%s", len(data), data))
		if hex.EncodeToString(sum[:])[:4] != prefix {
			continue
		}
		file := filepath.Join(GinkgoT().TempDir(), "blob")
		Expect(os.WriteFile(file, []byte(data), 0o600)).To(Succeed())
		Expect(r.git("hash-object", "-w", file)).To(HavePrefix(prefix))
		return prefix
	}
}

// partialClone returns a clone of the fixture that holds none of its files'
// contents, as git clone --filter=blob:none makes one. Each read there fetches
// what it needs, and each such fetch would start the maintenance
// newCephFixture turns off, so the clone's config turns it off too.
func (r *cephFixture) partialClone() string {
	r.git("config", "uploadpack.allowFilter", "true")
	dst := GinkgoT().TempDir()
	r.git("clone", "-q", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "--no-checkout", "--filter=blob:none",
		"file://"+r.dir, dst)
	return dst
}

// git runs git in the repository, isolated from the caller's configuration.
// A fixture outlives the spec node that builds it, so no node's context
// bounds it.
func (r *cephFixture) git(args ...string) string {
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=citecheck", "GIT_AUTHOR_EMAIL=citecheck@rgw-go.test",
		"GIT_COMMITTER_NAME=citecheck", "GIT_COMMITTER_EMAIL=citecheck@rgw-go.test",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func (r *cephFixture) write(file, text string) {
	full := filepath.Join(r.dir, file)
	Expect(os.MkdirAll(filepath.Dir(full), 0o750)).To(Succeed())
	Expect(os.WriteFile(full, []byte(text), 0o600)).To(Succeed())
}

func (r *cephFixture) commit(msg string) string {
	r.git("add", "-A")
	r.git("commit", "-q", "--no-verify", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// markdown writes a markdown file into a fresh directory and returns its
// path.
func markdown(name, text string) string {
	p := filepath.Join(GinkgoT().TempDir(), name)
	Expect(os.WriteFile(p, []byte(text), 0o600)).To(Succeed())
	return p
}
