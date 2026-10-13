// Command citecheck checks the Ceph source citations in markdown files, such
// as a plan's or a spec's, against a clone of ceph/ceph at the revision each
// names, so that a line range that points at the wrong file or the wrong
// lines is found before work is planned on it.
//
//	go run ./hack/citecheck [--ceph DIR] FILE...
//	make citecheck FILES="FILE..."
//
// --ceph names the clone, /home/jhoblitt/github/ceph by default; it can also
// come from CITECHECK_CEPH, or from the config file --config (CITECHECK_CONFIG)
// names, under the key ceph. The command line wins over the environment, and
// the environment over the file. The --log-level (debug, info, warn or error;
// info by default; CITECHECK_LOG_LEVEL) and --log-format (json or text; json
// by default; CITECHECK_LOG_FORMAT) flags set the logger, which writes to
// stderr. The prefix is the tool's own, so that no RGW_GO_ setting meant for
// rgw-go applies to it.
//
// # Citations
//
// A citation is one of two forms. The first is a code span whose whole
// content, blanks around it aside, is
//
//	<path>:<line>@<ref>
//	<path>:<from>-<to>@<ref>
//
// either optionally followed by #<symbol>, a C++ name that may be qualified:
//
//	`src/rgw/driver/rados/rgw_rados.cc:6847-6855@v19.2.6#RGWRados::Object::Read::prepare`
//
// The path is from ceph's repository root and holds no blank, control
// character, ":", "@", "#", quote, backtick, bracket, parenthesis or angle
// bracket; the ref starts with a letter or a digit and holds letters, digits
// and "._/+-". Every such span is read as a citation of ceph/ceph, so
// rgw-go's own code is cited as a plain <path>:<line>, without "@<ref>",
// which is not a citation and is not checked. A span with no path,
// :<line>@<ref>, is a malformed citation: which file it means cannot be told.
// The second form is a GitHub link to lines of ceph/ceph:
//
//	https://github.com/ceph/ceph/blob/<ref>/<path>#L<from>-L<to>
//	https://github.com/ceph/ceph/blob/<ref>/<path>#L<line>
//
// The ref is the URL's first segment after blob/. The path is
// percent-decoded, a query such as ?plain=1 is allowed, and a column GitHub's
// selection adds, #L5C3, is ignored. A link to ceph/ceph whose anchor starts
// with L but is not one of the two forms, such as #L10-12, is a malformed
// citation; a link with no anchor, or another anchor, is not a citation. A
// link has no symbol.
//
// Fenced code blocks, ``` or ~~~ at any indentation, are skipped whole, so a
// citation inside one is not checked; everything else is searched,
// indented code blocks included, since telling one from a list item's
// continuation takes a full markdown parser. Code spans are paired within a
// paragraph, as markdown pairs them, and a paragraph here ends at a blank
// line, a fence, a heading, a list item or a table row. A path:line@ref
// outside a code span is prose, not a citation.
//
// # Checks
//
// Each citation must hold all of these, checked in this order, and the
// first that fails is its reason:
//
//   - it is well formed: it has a path, its lines are numbers and its symbol
//     is a C++ name;
//   - its first line is at least 1, and not after its last;
//   - its path is clean: not absolute, and with no ".", ".." or empty
//     segment;
//   - its ref is a tag or a full or abbreviated commit id, and names a
//     commit the clone has. A branch, a remote-tracking branch such as
//     origin/main and a symbolic ref such as HEAD are refused, since what
//     they name moves, and so is any other revision expression, such as
//     v19.2.6~1. git rev-parse --symbolic-full-name tells them apart, and an
//     abbreviated id is looked up as a commit, so one that a tree's or a
//     blob's id also starts with still resolves;
//   - its path is a file at that commit, not missing and not a directory;
//   - its last line is at most the file's last; a final line with no
//     newline counts;
//   - when it names a symbol, the cited lines hold it, or lie in a block,
//     its header or its body, that the symbol heads.
//
// The symbol check is textual, a check against honest mistakes, not a C++
// parser. The braces of the file's code are paired, with comments, string,
// raw string and character literals, digit separators, preprocessor
// directives, a backslash continuing them, and every branch of a
// preprocessor conditional after its first left out. A block's header is the
// code between the ";", "{" or "}" before it and its "{", where a brace pair
// followed by ",", "{" or ")", such as a member initializer x{a}, is part of
// the header. A scope is a block whose header opens a namespace, a class, a
// struct or a union, read from the last of those keywords in the header, or
// an extern "C" block.
//
// The check searches for the symbol's last component, c in A::B::c, as a
// whole name: not part of a longer identifier, not ~c unless c is written
// so, and not a qualifier, as c is in c::d or c<T>::d. A match's qualifier
// is the one written before it, template arguments dropped and blanks around
// "::" allowed. When the match is a name a declaration or a definition gives,
// the names of the scopes around it, outermost first, come before that: a
// method defined inside its class is qualified by the class and its
// namespaces, as one defined outside it is by what is written and the
// namespaces around it. A match is such a name when it is code, not a
// comment, a literal or a directive; every block around it is a scope, not
// a function's body; it does not follow "->"; and it stands outside every
// parenthesis of its statement or header, after no "=" there but one inside
// a template's angle brackets, a default template argument's, and before any
// ":" there that neither halves a "::" nor ends an access label. So a
// trailing return type, a parameter, an argument, an initializer, and a base
// class in a base clause or a member-initializer list are uses, while a
// member template with a default template argument is still a definition. A
// match agrees with the symbol when either qualifier is empty or the shorter
// is the longer's trailing components, so B::c agrees with A::B::c, while
// A::D::c does not. Then:
//
//   - the cited lines hold the symbol when their text, comments included,
//     has a match that agrees with it;
//   - otherwise the blocks whose header starts on the first cited line or
//     before it, and whose "}" is on the last cited line or after it, are
//     searched, innermost first: the citation holds when any of their
//     headers has a match that agrees with the symbol, and otherwise fails,
//     naming the nearest match that disagrees when there is one.
//
// What it cannot see:
//
//   - a match in the cited lines is enough, whatever it is: a call, a
//     comment or a declaration of the symbol holds as well as its
//     definition, and a call written unqualified, or on an object, agrees
//     with any class;
//   - a header is any block's, so an if, a loop, a lambda or a namespace
//     whose header holds the name counts as its definition;
//   - a class whose head the header does not show, such as one a macro
//     declares, is not a scope, so its methods keep only their written
//     qualifiers and an unqualified one agrees with any class;
//   - overloads are not told apart, and neither are macros that expand to
//     a definition or to braces;
//   - code the first branch of a conditional leaves out is not paired, so a
//     citation into a later branch holds only by its own lines;
//   - in a file without braces, Python or YAML, only the cited lines count.
//
// # Output
//
// citecheck prints a line for each citation that fails,
//
//	FILE:LINE: <citation>: <reason>
//
// where FILE is as given, LINE is the markdown line the citation starts on
// and the citation is as written, then citations checked: N, failed: M. It
// exits 0 when none fails, 1 when one does, and 2 for a refused command
// line, a file it cannot read, a clone that is not a git repository or is a
// partial one, or a git that cannot answer. go run reports any nonzero
// status as 1, and make any failed recipe as 2, so a caller that tells the
// statuses apart runs a binary go build made. Every file is read before any
// is checked. git is run only as rev-parse, config and cat-file, which never
// check out or change a full clone, and each ref is resolved and each file
// read once. A partial clone, one whose extensions.partialClone is set or
// one of whose remotes is a promisor, is refused, since git cat-file fetches
// an object such a clone lacks from the remote and writes it into the clone.
//
// Managed by go-conventions (references/layout.md owns the main shape).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := citecheck.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "citecheck:", err)
	}
	return citecheck.ExitStatus(err)
}
