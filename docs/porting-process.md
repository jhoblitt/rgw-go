# Porting process registry

This registry records the problems rgw-go has had in planning and managing a large port: radosgw's behaviour at two releases, carried into Go across many parallel tasks. Each entry says what happened, what it cost, and how the process could be improved. The entries are evidence for changing the skills and instructions that drive the work (writing-plans, subagent-driven-development, the go/github/code conventions, CLAUDE.md), or for writing a new skill.

Add an entry, or update one, whenever planning or orchestration costs rework, stalls, or needs the owner to step in. Every entry starts at **open**. An entry that should be fixed before the next planning round is marked **Major**.

## Reviews

A process and planning review runs at three points:

1. **End of phase 1, before phase 2's tasks are planned.**
2. **End of phase 2, before phase 3's tasks are planned.**
3. **End of phase 3.** This one also decides whether to change the existing skills or write a new one.

Each review reads every open entry. It applies the fixes the next planning round needs, and it marks each entry **fixed** (naming the change), **carried** (to the next review) or **dropped** (with the reason).

The **Major** entries below must be settled at the end-of-phase-1 review, before phase 2's tasks are planned.

## Index

| ID | Problem | Severity | Candidate home | Status |
|---|---|---|---|---|
| PP-1 | Plan code treated as binding mandated a data-loss race | Major | writing-plans | open |
| PP-2 | Wrong radosgw facts in plans reached specs and reviews | Major | writing-plans; a citation checker | open |
| PP-3 | No up-front rule on reproducing a floor's security defects | Major | design spec; writing-plans | open |
| PP-4 | Disclosure policy for found defects was settled piecemeal | Major | design spec; project CLAUDE.md | open |
| PP-5 | Hot files shared by parallel tasks force a rebase on every merge | Major | writing-plans (file structure) | open |
| PP-6 | Tasks too large for one implement-and-review loop | Major | writing-plans (task sizing) | open |
| PP-7 | No view of usage limits; throttling discards in-flight work | Minor | subagent-driven-development; Claude Code | open |
| PP-8 | Merge authority and the merge gate were settled mid-execution | Minor | github-conventions; subagent-driven-development | open |
| PP-9 | The progress ledger has an implicit grammar | Minor | subagent-driven-development scripts | open |
| PP-10 | Controller tooling friction and context noise | Minor | subagent-driven-development; Claude Code | open |
| PP-11 | Defect claims from source reading alone were sometimes wrong | Minor | code-conventions bug reports | open |

## PP-1: Plan code treated as binding mandated a data-loss race

- **Severity:** Major. **Status:** open.
- **Observed:** W's plan, Task 5, gives the stripe writer as complete code. Each piece of a stripe is written from its own goroutine, `WriteFull` at offset 0 and `Write` at later offsets, with nothing ordering them. The implementer followed it. The task review found that a stripe's later `Write` can land before its `WriteFull`, which then truncates the stripe while the manifest records its full size. With Go limited to one CPU thread, a 4 MiB body at a 1 MiB chunk loses data every time. radosgw submits a stripe's pieces in order from one thread (`rgw_putobj_processor.cc:140-159`).
- **Cost:** one Critical finding, a fix round that overrode the plan, and a defect that W8's `putStream` would have inherited.
- **Improvement:** a plan states behaviour and the invariants between tasks, not function bodies. An ordering requirement is one of those invariants ("the pieces of one stripe reach RADOS in stream order"), so the plan says it in words, and a test pins it. Code a plan does carry is labelled as a sketch, and the stated behaviour overrides it. Phase 1's plans total about 37,800 lines, most of them code.
- **Candidate home:** writing-plans, which by default puts complete code in every step.

## PP-2: Wrong radosgw facts in plans reached specs and reviews

- **Severity:** Major. **Status:** open.
- **Observed:**
  - W's plan, Task 5's condition table, expects Squid to answer If-None-Match with another ETag on a missing key with ENOENT. v19.2.6 answers 412 (`rgw_rados.cc:6536-6542`).
  - The same plan links `src/rgw/rgw_op.cc#L6493-L6512` for code that lives in `src/rgw/driver/rados/rgw_rados.cc`.
  - Task reviews in other units corrected further citations and claims, each costing a fix round.
- **Cost:** every wrong fact costs a reviewer's verification and often a fix round. A wrong expected value in a spec table can make a defect pass as correct.
- **Improvement:**
  - Run a mechanical citation check at plan time: each link resolves at its tag, and the cited range contains the symbol the text names.
  - Expected values that decide a spec come from the source at both tags, or from running the release, and are never written from memory.
- **Candidate home:** writing-plans (a verification step), and a script under `hack/`.

## PP-3: No up-front rule on reproducing a floor's security defects

- **Severity:** Major. **Status:** open.
- **Observed:** W's plan (W-D3) keys conditional PUT on the release, so it reproduces v19.2.6's skip of every condition on a head with a fake tag. The task review and the defect triage later classified that skip as a security defect: an update is lost (tracker #68183), fixed upstream after v19.2.6 was tagged. The owner then ruled, during the fix round, that rgw-go applies v20.2.4's check on both releases. The same question is open for radosgw's silent drop of an unparseable role policy.
- **Cost:** an owner decision in the middle of a fix round, rework of finished code, and a security behaviour that would have shipped if the triage had not caught it.
- **Improvement:** decide the rule in the design spec before planning. A suggested rule: parity with the floor release, except that rgw-go follows the fixed release where the floor has a security or fail-open defect that upstream has fixed, and records an exclusion. Each plan lists every floor defect it reproduces, with its security class.
- **Candidate home:** the design spec, and writing-plans (the defect list).

## PP-4: Disclosure policy for found defects was settled piecemeal

- **Severity:** Major. **Status:** open.
- **Observed:** defects found while porting were held, released one at a time, released all at once ("All public"), and then needed a fresh ruling when a new security-class defect appeared. One commit message and its PR body were reworded and force-pushed before merge to keep a held defect out of a tracked file (#145).
- **Cost:** owner interruptions, held work, and history rewritten on a branch already pushed for review.
- **Improvement:** state the disclosure policy in the spec before phase 2: what is public by default, what a security classification changes, and who classifies. Give registry entries a security field. Then a new security finding has a rule to follow instead of a question to ask.
- **Candidate home:** the design spec and the project CLAUDE.md; code-conventions' bug-report rules for the upstream side.

## PP-5: Hot files shared by parallel tasks force a rebase on every merge

- **Severity:** Major. **Status:** open.
- **Observed:**
  - `internal/testutil/fakerados/cls_rgw.go` gets an addition from W4, W5, P2, M8 and M10b, both to its write-method list and to its class switch.
  - `docs/ceph-upstream-bugs.md` and `docs/exclusions.md` get an addition from nearly every task.
  - Every merge forces each branch in flight to rebase. The controller built a union-merge rebase script for the registry.
- **Cost:** rebases in every fix round, conflict risk, and controller time.
- **Improvement:** plan the file structure for parallel work. Give each emulator its own file, registered from a table, and give each registry entry its own file or an append-only region a tool assembles. Assign every shared file to one task, or order the tasks that touch it.
- **Candidate home:** writing-plans (file structure); rgw-go's own layout, which could change before phase 2.

## PP-6: Tasks too large for one implement-and-review loop

- **Severity:** Major. **Status:** open.
- **Observed:**
  - M's Task 10 was split mid-execution into 10a (op, #71) and 10b (driver).
  - W's Task 5 brief runs to 1,101 lines.
  - Implementer runs of 20 to 25 minutes are common, and an interruption loses the whole run.
- **Cost:** long review cycles and large fix rounds, and work lost whenever an agent stops.
- **Improvement:** cap the size of a task's brief, and split a task by layer (op, driver, emulator, handler) whenever it would cross more than one. Size each task so that one reviewer can judge it in one pass.
- **Candidate home:** writing-plans ("Task Right-Sizing").

## PP-7: No view of usage limits; throttling discards in-flight work

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - No tool in the session exposes the 5-hour or weekly usage figures.
  - A parallel session reported usage percentages it had no source for, and later retracted them.
  - The owner throttled the work to one subagent, then paused it, then resumed it at three. Two implementers that had spent their run reading had written no code, so their worktrees were removed and the reading was lost.
- **Cost:** lost agent runs, and decisions made on numbers that had no source.
- **Improvement:**
  - Dispatches require a report skeleton and early checkpoint commits.
  - A concurrency limit lives in one place the controller reads before every dispatch.
  - No session states a usage figure it cannot trace to a source.
- **Candidate home:** subagent-driven-development; a feature request for Claude Code to expose usage.

## PP-8: Merge authority and the merge gate were settled mid-execution

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - Merging stopped until the owner granted permission to merge without human review behind a self-review gate.
  - The gate (state, CI, alerts, a trial merge) and a check that builds and tests the merged tree when main has moved since CI (needed for #152) were written as ad hoc scripts.
- **Cost:** a stall on finished PRs, and controller time.
- **Improvement:** decide merge authority before execution starts, and ship the gate and the merged-tree check as tools of the workflow.
- **Candidate home:** github-conventions; subagent-driven-development's finishing step.

## PP-9: The progress ledger has an implicit grammar

- **Severity:** Minor. **Status:** open.
- **Observed:** the progress report counts a task as done only when its ledger has a line `- Task N: MERGED`. A merge recorded as "complete" was not counted (P's Task 2, #151) until the line was rewritten.
- **Cost:** a wrong progress report, caught by eye.
- **Improvement:** give the ledger a schema, and write it through a script instead of free-form appends.
- **Candidate home:** subagent-driven-development's scripts.

## PP-10: Controller tooling friction and context noise

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - The worktree guard refuses compound git commands that name another worktree, so every such command became a script file.
  - The working directory drifted into the ledger tree, and relative pathspecs then came back empty.
  - A sandbox lock file broke git config writes.
  - Language-server diagnostics from agents' worktrees, which are outside the controller's workspace, reach the controller's context as dozens of false compile errors.
- **Cost:** controller turns and context spent on tooling, not on the work.
- **Improvement:** ship a controller script library with the workflow, and keep agents' worktrees out of the controller's language-server workspace.
- **Candidate home:** subagent-driven-development; Claude Code feedback.

## PP-11: Defect claims from source reading alone were sometimes wrong

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - A claimed radosgw defect (an endless loop when `rgw_max_chunk_size` is 0) was disproved at registry triage, before publication (#150): the PUT gets 400 RequestTimeout instead.
  - Independent classification also changed the severity of other entries.
- **Cost:** a retraction, and triage time.
- **Improvement:** registry entries carry their reproduction status ("unreproduced" until a running release shows the defect). Plans budget a cluster reproduction step for every defect a task records.
- **Candidate home:** code-conventions' bug-report rules; the project's registry template.
