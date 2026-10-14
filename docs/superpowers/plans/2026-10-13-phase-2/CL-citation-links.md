# Unit CL: Ceph citations are clickable links

The owner, 2026-09-29: references to code line numbers are to be "clickable links to the line or
line range in the github repo which that code lives anchored to the correct tag or ref". Phase 2's
planning instead introduced a code-span form, `src/<path>:<lines>@<ref>#<symbol>`, which was never
agreed and spread into the docs; the owner rejected it on 2026-10-14, extended the rule to code
comments ("I think I'd like to do code as well"), and ruled that "existing v19.2.6 citations can
stay as v19.2.6 citations".

## Decisions

| Id | Decision | Reason |
|---|---|---|
| CL-D1 | A Ceph citation in Markdown is a link, `[<basename>:<from>-<to>](https://github.com/ceph/ceph/blob/<ref>/<path>#L<from>-L<to>)`, and nothing else. | The owner's rule; the form the docs' existing links already use ([PosixStack.h:51](https://github.com/ceph/ceph/blob/v19.2.6/src/msg/async/PosixStack.h#L51) in docs/cgo-limitations.md). |
| CL-D2 | In a Go comment it is the raw URL. | Go's `[text]` doc links render only in doc comments, and many citations sit inside function bodies. |
| CL-D3 | A citation keeps the ref it was written against. | The owner, 2026-10-14; it was checked at that ref. |
| CL-D4 | rgw-go's own code stays a plain `path:line`. | It is not pinned to a tag, so a link would drift. |
| CL-D5 | citecheck checks every link, and fails a code span, a plain Ceph citation in a Go comment, or a link inside a code span as "not a link"; CI runs its clone-free form check. | So the form cannot creep back. |

## Tasks

### Task CL1: hack/citecheck, links are the only citation form

- **Layer:** hack. **Cluster:** no. **Files:** hack/citecheck and its specs; Makefile.
- **Behaviour:** a code-span citation, or a ceph link inside a code span, fails "not a link";
  `--fix` rewrites a span that passes every check into a CL-D1 link, dropping its symbol, and
  refuses (reports, leaves alone) a span across a line break, in a link's text, after `!`, or with
  a `/` in its ref; `--form-only` checks form without a clone. Targets `citecheck-fix` and
  `citecheck-links`; nothing in CI.
- **PR:** its own. **Depends on:** nothing.

### Task CL2: the Markdown sweep

- **Layer:** docs, CI. **Cluster:** no.
- **Files:** every tracked `.md` holding a Ceph citation (docs/, docs/superpowers/plans/,
  docs/superpowers/specs/); prose that describes the code-span form (docs/porting-process.md's PP-2,
  the phase 2 index's citation constraint, and any other); a new docs/porting-process.md entry for
  this drift; the CI workflow.
- **Behaviour:**
  1. `make citecheck-fix` over every tracked `.md`; the five ceph links written inside code spans
     (docs/superpowers/plans/2026-09-29-phase-1/Z-authorization.md:776-780) and any refusal are
     fixed by hand.
  2. The prose describes CL-D1 instead of the code-span form.
  3. CI runs `make citecheck-links` over every tracked `.md`, so a code span fails a PR.
  4. Each unmerged task branch runs `make citecheck-fix` over the docs it touches before it merges.
- **Gate:** the default check over every tracked `.md` reports 0 failed, and a byte diff shows only
  citations and the named prose changed.
- **PR:** one, with the owner's LGTM (it touches plans, specs and process docs). **Depends on:** CL1.

### Task CL3: hack/citecheck, Ceph citations in Go comments

- **Layer:** hack. **Cluster:** no. **Files:** hack/citecheck and its specs; Makefile.
- **Behaviour:** citecheck reads Go comments, checks every ceph link in them, and fails a plain Ceph
  citation (`<basename>:<lines>`, and the bare `:<lines>` continuing one) as "not a link". `--fix`
  resolves each into a CL-D2 URL: the basename's path at the ref (settled by content when several
  match), the ref from the clause's "at <tag>", a `[T]` or `[S]` marker, the citation a continuation
  follows, or the file's own convention for bare numbers; a named identifier must appear in the cited
  lines. It rewraps each touched paragraph to its width without breaking a URL, leaves code blocks
  alone, keeps the output gofmt-clean, and lists what it cannot resolve. It ports the rules of the
  docs-era resolver, `.superpowers/tools/linkify.py`, and adds no notation. Targets `citecheck-go-fix`
  and `citecheck-go`; nothing in CI.
- **Scale at 093c963:** 288 of 838 Go files; 2,551 named citations and 1,988 continuations.
- **PR:** its own. **Depends on:** CL1.

### Task CL4: the Go-comment sweep

- **Layer:** every package. **Cluster:** no.
- **Behaviour:** `make citecheck-go-fix`, one PR per package group, with the unresolved residue fixed
  by hand; then CI runs `make citecheck-go` over every `.go` file.
  CI's check must fail a plain Ceph citation whose ref cannot be found, not pass it, since
  CL3's `--form-only` skips those (found by CL3's review, Minor 6). It gets the file list the
  same way CL5 does.
- **Window:** after unit F's last code task merges and before the post-unit-F benchmark run, since
  it touches most packages and would conflict with every open branch.
- **PR:** one per package group. **Depends on:** CL3; unit F's code tasks merged.

### Task CL5: hack/citecheck, plain Ceph citations in Markdown

Found in CL2's work (its report's concern 2), and numbered here before CL2 merged (PP-12).

- **Layer:** hack, docs. **Cluster:** no.
- **Behaviour:**
  1. Tracked Markdown still holds the docs-era plain form: a code span naming a basename and lines,
     `rgw_op.cc:727-758`, whose ref the surrounding prose gives, and bare `:757-788` continuations.
     There are about 2,350 and 3,700 of them (docs/ceph-upstream-bugs.md 1,394, docs/exclusions.md
     465). citecheck's grammar does not see them, so CL2's sweep and form check leave them plain.
  2. citecheck applies CL3's resolver to Markdown code spans, with the same path, ref, continuation
     and content rules (CL-D3: each keeps the ref it was written against), rewriting each resolved
     span into a CL-D1 link and listing the rest by reason. rgw-go's own files stay plain (CL-D4).
  3. The sweep runs `--fix` over every tracked `.md` and fixes the residue by hand. Then a plain Ceph
     citation in Markdown fails "not a link". Telling a Ceph basename from rgw-go's needs the file
     list at the ref, so the task states how CI gets it (a ceph clone step, or a checked-in list of
     ceph paths at each pinned ref) and pins that choice with a spec.
- **PR:** the tool, then the sweep with the owner's LGTM, as CL2's. **Depends on:** CL2, CL3.

## Order

| Wave | Tasks | Waits for |
|---|---|---|
| 1 | CL1 | nothing |
| 2, in parallel | CL2, CL3 | CL1 |
| 3, in parallel | CL4, CL5 | CL3; CL4 also unit F's code tasks, CL5 also CL2 |
