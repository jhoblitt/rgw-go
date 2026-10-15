// Command citecheck checks the Ceph source citations in markdown files, such
// as a plan's or a spec's, and in Go files' comments against a clone of
// ceph/ceph at the revision each names, so that a line range that points at
// the wrong file or the wrong lines is found before work is planned on it,
// and rewrites the code spans and plain file:line citations that once cited
// Ceph into links.
//
//	go run ./hack/citecheck [--ceph DIR] [--fix | --form-only] [--markdown-plain] FILE...
//	go run ./hack/citecheck files [--ceph DIR] (--out FILE | --check FILE) REF...
//	make citecheck FILES="FILE..."
//	make citecheck-fix FILES="FILE..."
//	make citecheck-links FILES="FILE..."
//	make citecheck-go PKGS="DIR..."
//	make citecheck-go-fix PKGS="DIR..."
//	make citecheck-files
//	make citecheck-files-check
//
// A FILE whose name ends in .go is read as Go source, as Go comments below
// says; every other is read as markdown. The Go targets read every .go file
// under each DIR, at any depth, outside testdata directories. The files
// command and its targets keep the list of ceph's files, as The list of
// ceph's files below says.
//
// --ceph names the clone, /home/jhoblitt/github/ceph by default; it can also
// come from CITECHECK_CEPH, or from the config file --config (CITECHECK_CONFIG)
// names, under the key ceph. The command line wins over the environment, and
// the environment over the file. --fix (CITECHECK_FIX; make citecheck-fix)
// rewrites code span citations into links, as Rewriting below says, and
// plain citations in Go comments, as Go comments says. --form-only
// (CITECHECK_FORM_ONLY; make citecheck-links) checks only what needs no
// clone, so that CI can run it; it cannot be combined with --fix.
// --markdown-plain (CITECHECK_MARKDOWN_PLAIN) reads the plain citations in
// markdown too, as Plain citations in markdown says; without it they are not
// read at all.
// The --log-level (debug, info, warn or error; info by default;
// CITECHECK_LOG_LEVEL) and --log-format (json or text; json by default;
// CITECHECK_LOG_FORMAT) flags set the logger, which writes to stderr. The
// prefix is the tool's own, so that no RGW_GO_ setting meant for rgw-go
// applies to it.
//
// # Citations
//
// A citation has one form, a GitHub link to lines of ceph/ceph:
//
//	https://github.com/ceph/ceph/blob/<ref>/<path>#L<from>-L<to>
//	https://github.com/ceph/ceph/blob/<ref>/<path>#L<line>
//
// written bare, in angle brackets, or as a markdown link's destination, the
// link's text naming the file's base name and the lines:
//
//	[rgw_rados.cc:6847-6855](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6847-L6855)
//
// The ref is the URL's first segment after blob/. The path is
// percent-decoded, a query such as ?plain=1 is allowed, and a column GitHub's
// selection adds, #L5C3, is ignored. A link to ceph/ceph whose anchor starts
// with L but is not one of the two forms, such as #L10-12, is a malformed
// citation; a link with no anchor, or another anchor, is not a citation. A
// link has no symbol, and its text is not checked. A link a code span holds
// renders as code, not as a link, so it fails with the reason "not a link",
// in every mode, unless the code span is itself a markdown link's text.
//
// A code span is not a citation form. One written as citations once were, a
// code span citation, is only --fix's input, and fails everywhere else with
// the reason "not a link". It is a code span whose whole content, blanks
// around it aside, is
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
// Each link must hold all of these, and so must each code span citation
// under --fix, checked in this order, and the first that fails is its reason;
// --form-only checks the first three, which need no clone:
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
//   - when it names a symbol, as only a code span citation can, the cited
//     lines hold it, or lie in a block, its header or its body, that the
//     symbol heads.
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
// # Rewriting
//
// --fix checks every citation, and replaces each code span citation that
// holds, its backticks and any blanks inside them included, with the link to
// its lines at its ref:
//
//	`src/rgw/driver/rados/rgw_rados.cc:6847-6855@v19.2.7#RGWRados::Object::Read::prepare`
//	[rgw_rados.cc:6847-6855](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6847-L6855)
//
// The link's text is the file's base name and the lines, one line when the
// span's first and last are the same. A "\", "|", "*" or "~" in the text is
// escaped with a backslash, and so is an "_" that is not between two ASCII
// letters or digits, so that a table's row keeps its cells and a name such
// as __init__.py is not emphasis. Each segment of the URL's path is
// percent-encoded, as GitHub needs a "%" or a "|" to be. The link has no
// title: the span's symbol is checked, then dropped. A span that fails is
// reported and left as it is, and so is one that holds but cannot be
// rewritten where it stands: one that runs across a line break, which
// replacing would rewrap; one in a link's text, the brackets before a "(" or
// a "[", which cannot hold another link; one right after an unescaped "!",
// which would make the link an image; and one whose ref holds a "/", which
// the link's URL cannot tell from its path. Every other byte of a file
// is kept, its line endings and wrapping included, and a file is written in
// place, after every file is checked, only when a span in it was rewritten.
// A second run rewrites nothing.
//
// # Go comments
//
// In a .go file only // comments are read, as go/scanner finds them: not a
// string; not a cgo preamble, C code, whether a block comment or // lines
// right above an import of "C"; not a block comment at all; not a directive
// such as //go:build or //nolint, nor a line with no blank after its "//";
// and not a code block, the indented lines that do not start a list item.
// Each link in them is a citation, checked as a markdown link is. A plain
// citation is a file and lines, or lines alone, a continuation of a file
// named before it, either with an optional [S] or [T] marker on its lines:
//
//	rgw_op.cc:5158-5163
//	driver/rados/rgw_rados.cc [T]:6847
//	:6884-6890
//
// A file is a path whose last segment ends in .cc, .h, .hpp, .c, .py, .go,
// .yaml.in, .yaml, .yml, .sh, .txt, .json, .cmake, .proto, .gperf or .in, and
// neither it nor a continuation touches a path or identifier byte, or a bracket
// for a continuation, so that a slice expression is none. It cites ceph when
// its file, or a continuation's nearest candidate file (below), is a file in
// the clone at its ref or at v19.2.6 or v20.2.4, the releases [S] and [T] name,
// and so does a continuation that follows no file but follows a header
// convention (below). Any other, such as rgw-go's own bucketops.go or
// internal/driver/put.go, a Go standard library file, or a port written as a
// line number, is not a citation and is left alone. Without --fix a plain
// citation of ceph fails as not a link. Under --form-only, which has no clone,
// one fails so when its file is not a Go file and the rules below give it a
// ref; one with no ref is not judged.
//
// --fix resolves each into the raw link to its lines at its ref, which it
// writes in the citation's place. A comment is a run of // lines with nothing
// else on them, or a single comment after code, and a paragraph of one is
// read with its lines joined by single blanks:
//
//   - ref, the first rule that gives one: an "at", "in" or "on" and a tag after
//     it, past any citations listed with it by blanks, commas, "and" or "or",
//     or after the comma that closes them, but not past them to a citation with
//     its own tag or marker right before it; "at both tags", which is v19.2.6
//     when the cited lines are the same at v19.2.6 and v20.2.4; its own [S] or
//     [T]; the last marker or tag in its clause before it, the clause cut at
//     the last "(", ";" or ". " and an "at <tag>" that leads it left to the
//     citation before, unless the clause names another release too, by tag,
//     marker or codename, and the last is not right before it, which leaves it
//     with none; the one release, by tag, marker or codename (Squid, Tentacle),
//     the words before the "(" it opens name, back to the last citation, "(",
//     ")", ". ", ";" or ","; for a continuation, the ref of the citation it is
//     listed after, though not past that citation's own "at <tag>"; and the
//     file's header convention, the nearest comment above it saying what a bare
//     line number means. A header that names a file gives its tag only to a
//     citation that turns out to be of that file. With none it is unresolved:
//     there is no default ref.
//   - file: a continuation's candidates are, nearest first, the citations
//     and the file names without lines before it in its comment, then the
//     file the header convention names or, when it names none, those its
//     paragraph names before it.
//   - path: the one path in the clone at the ref that is the written path or
//     ends in "/" and it; of several, those under src/rgw/ and src/cls/ when
//     any is; of several still, the one whose cited lines hold an identifier
//     named beside the citation.
//   - content: when identifiers are named beside it, within 220 bytes before
//     it and 100 after, one must stand as a whole word in the cited lines or
//     the 3 either side or, for a citation that names its file and for a
//     continuation's nearest candidate, in the header of a block that
//     encloses them, as a function's does its body. A continuation takes the
//     first candidate file where one does, and with no identifier its
//     nearest, unless its clause names two files or more and its own is not
//     named right before it, which leaves it unresolved; it moves past the
//     first file it tries only on the cited lines
//     and the 3 either side, and only for a strong identifier, one with an
//     "_", a lower-case letter before a capital, or ten bytes or more, so a
//     generic name, such as a base class's, never moves it. An identifier is
//     any name in backticks, and outside them a name with an "_", a
//     lower-case letter before a capital, or two capitals and a lower-case
//     letter first, less a few stop words and less the names that are
//     rgw-go's: every identifier the Go file's own code writes, and every
//     name a package the file imports qualifies, as op.UserStore.
//
// So these read as at the refs on their right, the continuations as in
// rgw_op.cc, with no header convention:
//
//	rgw_op.cc:5158-5163 at v20.2.4    v20.2.4
//	rgw_op.cc:10 and :20 at v20.2.4   v20.2.4 for both
//	rgw_op.cc:10, :20, at v20.2.4     v20.2.4 for both
//	rgw_op.cc [T]:10                  v20.2.4
//	(rgw_op.cc:10; v20.2.4 :12)       none, then v20.2.4
//	rgw_op.cc:10 at v19.2.6, :12      v19.2.6, then none
//	[T] rgw_op.cc:10, :12             v20.2.4 for both
//	as v20.2.4 does (rgw_op.cc:10)    v20.2.4
//
// and these are header conventions:
//
//	A bare line number below is v19.2.6's.
//	A bare line number below is v19.2.6's rgw_op.cc.
//	A bare line number is rgw_op.cc's at v19.2.6.
//	A bare rgw_op.cc line is v19.2.6's.
//
// A citation is unresolved, left as it is and listed with why, when it has
// no ref, or only a header's that names another file, or only a clause's
// that names two releases; when its ref names no commit in the clone; when
// its paragraph names rook, go-ceph or s3-tests, whose files the clone does
// not hold; when only the header convention gives its ref and the words that
// lead to it, back to the last citation, ". ", ";" or ",", name another
// release, by tag, marker or codename; when it is cited at both tags and its
// lines differ between them; when it is a continuation with no candidate
// file, or with no identifier beside it in a clause that names two files;
// when its file is no file at the ref, or several that no identifier tells
// apart; when the lines hold no identifier named beside it; and when its
// link fails a check a link must pass, such as lines past the file's end.
//
// The text around a link is kept, an "at <tag>" after it included, but a
// marker that gave citations their ref, with the blanks after it, is dropped
// once each of them is a link, and so is every other citation listed after
// the first, and only blanks or a "(" stand between the marker and the
// first. A paragraph a link is written into is rewrapped to the width of
// its widest line, in runes, a word wider than that on a line of its own,
// where a link and the "at <tag>" or "at both tags" right after it are one
// word; the lines before the first link are kept, and so are those after
// the last once the new lines end where an old line did. A list item, a
// heading, a comment after code, and a paragraph whose lines' prefixes
// differ or that holds a tab or two blanks running are not rewrapped: the
// link is written in place, a citation there that runs across a line break
// is listed instead, and a marker that would run across one is kept. Nor is
// a paragraph rewrapped when a line of the rewrap would read as a list item,
// a heading or a link definition. When the file was gofmt-clean and the
// rewrapping leaves it not, its links are written in place instead, and when
// that leaves it not clean either, none is written. A second run rewrites
// nothing.
//
// # Plain citations in markdown
//
// With --markdown-plain a markdown file's plain citations are read too: a code
// span whose whole content, blanks around it aside, is a plain citation as Go
// comments defines one, `rgw_op.cc:727-758`, `rgw_op.cc [T]:10` or `:757-788`,
// and those forms in prose, outside code spans and links. A code span may also
// hold a citation in a form --fix does not rewrite: a marker before its file,
// `[T] rgw_op.cc:10`; a list of line ranges, `rgw_op.cc:10,20-30`; a symbol
// after its lines, `:10-20#RGWOp::execute`; or an open range, `:10-`, `:10-...`
// or `:10+`. Each is checked as the others are, and under --fix listed as
// unresolved. A code span that holds more than a citation is code, and a link's
// text, its brackets paired as Scan pairs them, is the link's. A code span
// whose whole content is a file, `rgw_rados.cc`, names it as a file named
// without lines does; any other code span is read for identifiers only, and
// fenced code blocks are skipped.
//
// Each is read as a Go comment's is, with these differences. A paragraph is
// one Scan pairs code spans in, the lines of a list item or of a paragraph
// joined by single blanks, and a table row's cell is a paragraph of its own
// to the rules Go comments lists. A section, from a heading to the next, is
// the comment a continuation looks back through for its file. No identifier
// named beside a citation is taken for rgw-go's.
//
// The identifiers a citation's lines are checked for are whole code tokens,
// so that no common word stands in for one. A quoted string literal in a
// code span, and a literal there of words joined by "-" or "/", such as a
// header's name, `x-amz-expected-bucket-owner`, or a path, count only
// whole, never by their words. A qualified name, A::B, counts whole, and
// each of its parts of three letters or more as any other name where it
// stands. Any other name in a code span counts when it reads as code, with
// an "_" or a digit, a lower-case letter before a capital, or two capitals
// first, or is no common English word, none of
// internal/citecheck/english.txt, a fixed list, the same whatever files a
// run is given; and when it is no generic name, such as a namespace: ceph,
// rgw, rados, librados, std, boost, http, https, amz or aws. A name in prose
// counts as Go comments reads one.
// A citation beside which a name stands that Go comments would check its
// lines for, but none that counts here, is unresolved.
//
// A citation whose name, or for a continuation its nearest candidate's, is
// the path from rgw-go's root of a file rgw-go has is rgw-go's, and stays
// plain, though ceph has the same path, as .github/dependabot.yml: rgw-go's
// root is the nearest directory above the markdown file whose go.mod declares
// rgw-go's module, which a checkout and a copy of its files both have.
// Otherwise it cites ceph when the list of ceph's files below has its file,
// or, for a continuation, its nearest candidate's; or, with a clone, when the
// clone has it at its ref, at the commit an "at <commit>" after it names, at
// the main branch an "on main", "at main" or "at origin/main" after it names,
// or at v19.2.6 or v20.2.4. A continuation that follows a link cites the link's
// file. So rgw-go's own file is cited by its path from rgw-go's root,
// hack/admin/run.sh:12, even where ceph has a file of the same base name.
// Without --fix each fails, under --form-only too and whatever its ref, as
// not a link, the reason naming ceph's paths and saying how to cite rgw-go's
// file instead. Under --form-only, a citation of a file the list lacks fails
// too when it is tied to a tag or a commit the list was not made at, the ref
// a rule or scope below gives it or an "at <commit>", unless its paragraph
// names another repository, as rook or go-ceph: nothing without a clone tells
// it is not ceph's.
//
// When the rules Go comments lists give no ref, the scopes around the
// citation may, nearest first, and the first that names any release decides:
//
//   - its sentence, from the end of the one before, a "." and a blank, with
//     the "*" or "_" that close an emphasis between them, or in a table its
//     cell;
//   - another cell of its table row, then the cell of its column in the
//     table's header row, each only when the cell's whole text, "*", "`" and
//     blanks aside, names the release, as v20.2.4, [T], Squid and
//     Squid (v19.2.6) do, and another cell only in a column whose header is
//     Release, Tag, Version or Ref;
//   - each list item it is nested in, nearest first, and the paragraph right
//     before its list or table when that paragraph ends with ":", a list or
//     table ending, at its own level, at another list or table before it;
//   - its section's heading, by a tag or a marker only, since a codename in
//     a heading names a release's behavior rather than the tag its lines
//     were read at; a heading above its section's is not read.
//
// A scope gives the one release it names, by tag, marker or codename, when
// it names it before the citation and names no other release or ref. It
// leaves the citation with no ref when it names two or more; or a ref that
// is no tag, such as main, a commit id, a codename the markers do not name,
// "both tags" or "each release"; or a release as another citation's own,
// the "at <tag>" right after one, its comma aside, a tag or a marker right
// before one, or a link's ref. A sentence or a heading whose release follows
// "fixed in", "before", "until", "since" or "pre-" gives none either, and nor
// does the clause rule or the "(" rule finding one so, since it names when a
// behavior changed, not the release the lines were read at.
//
// A lead-in that states the pair convention in the words the registries
// write it in, "each pair of lines is v19.2.6's, then v20.2.4's", "a pair of
// line numbers is ...", "unless a pair is given, ...", "lines are given as
// ..." or "cited at v19.2.6 and then v20.2.4", gives the citations of its
// list their refs two by two before any other rule: the first of each pair
// the first tag it names, the second the second. Its list is the citations
// in its own paragraph and in each it leads in to, as the scopes above read
// a lead-in, whose nearest lead-in that states the convention it is. The
// convention gives refs to every citation of a regular list and to none of
// another, whose citations the rules above then read, a lead-in inside the
// list giving none; one a scope leaves with no ref is told why the
// convention is not applied. A list is regular when all of these hold:
//
//   - the statement names two tags, and the lead-in names no other release
//     or ref, "both tags" and the like aside;
//   - no paragraph of the list or its lead-in holds a link, ties a release
//     to a citation, right before it or by an "at" after it, or names a
//     release other than the two, and none names those, or "both tags",
//     in the sentence of a citation in the list or as a lead-in of its own
//     to one;
//   - no citation in it carries a marker or an "at <ref>";
//   - its citations come in pairs: two with a ";" or a "," alone between
//     them, nothing like that joining the pair to the next, the second a
//     continuation or a citation of the same file name;
//   - under --fix, every citation in it cites ceph, resolves at its tag with
//     an identifier named beside it standing in or around its lines,
//     resolves to the same file as the other of its pair, and can be
//     written in place.
//
// And a citation is unresolved, besides as Go comments lists:
//
//   - when it is cited at, on or in a ref that is no tag: at, on or in a
//     branch or a commit id; at a version that is no tag, "all", or a ref
//     holding a "/"; or in a version or Boost;
//   - when the clause rule or the "(" rule finds a release not right before
//     it, and the words before it, back to its sentence's start, the last
//     ";", or the citation before it and that one's own "at <tag>", name
//     another, as "the same at v20.2.4, where it is unchanged from v19.2.6
//     (" does;
//   - when a lead-in names two releases or more, as "the same at v19.2.6
//     and v20.2.4" does, and its own release is named only by a codename or
//     the parenthesis around it holds a ";";
//   - when it is a continuation listed after citations whose refs differ,
//     back to the last "(", ";" or ". ";
//   - when it is a continuation with no identifier beside it and no file
//     before it in its paragraph.
//
// A continuation whose nearest candidate file is named in its own
// paragraph is taken past it only to another file named before it there.
//
// --fix writes each that resolves in its place, its backticks included, as
// the link Rewriting describes, and drops the marker that gave it its ref
// as Go comments drops one. It lists instead one that runs across a line
// break, which writing it would rewrap; one in a heading, whose anchor the
// link could change; and one right after an unescaped "!". No paragraph is
// rewrapped, and a second run rewrites nothing.
//
// # The list of ceph's files
//
// --form-only, which has no clone, tells ceph's plain markdown citations by the
// list embedded from internal/citecheck/cephfiles.txt: every path of ceph/ceph
// at each ref its header names whose base name a plain markdown citation can
// name, a file as Go comments defines one. citecheck files --out writes it from
// the clone at the refs given, and --check fails, exiting 1, when the list
// differs from what it would write: other refs, another pattern for the names,
// or other paths. make citecheck-files writes it at the Makefile's
// CITECHECK_FILES_REFS, and make citecheck-files-check, part of make check,
// checks it. A spec, which needs no clone, fails while .goreleaser.yaml pins a
// tag the list was not made at, while the markers' releases are not among its
// refs, or when the pattern that chose its names is not citecheck's.
//
// # Output
//
// citecheck prints a line for each citation that fails,
//
//	FILE:LINE: <citation>: <reason>
//
// where FILE is as given, LINE is the line the citation starts on and the
// citation is as written, then citations checked: N, failed: M, and under
// --fix rewritten: K, the code span and plain citations it rewrote. With
// --log-level debug, each plain citation --fix resolves is logged with its
// file and line, its ref and the rule or scope that gave it, its path, the
// identifiers its lines were checked for, and the files and releases its
// clause names. It exits 0 when none fails, 1 when one does or the files
// command finds the list stale, and 2 for a refused command line, a file
// it cannot read or, under --fix, write, a clone that is not a git
// repository or is a partial one, or a git that cannot answer. go run
// reports any nonzero status as 1, and make any failed recipe as 2, so a
// caller that tells the statuses apart runs a binary go build made. Every
// file is read before any is checked. git is run only as rev-parse, config,
// cat-file and ls-tree, which never check out or change a full clone, and
// each ref is resolved, each file read and each tree listed once. A partial
// clone, one whose extensions.partialClone is set or one of whose remotes
// is a promisor, is refused, since git cat-file fetches an object such a
// clone lacks from the remote and writes it into the clone.
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
