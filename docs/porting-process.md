# Porting process registry

This registry records the problems rgw-go has had in planning and managing a large port: radosgw's behaviour at two releases, carried into Go across many parallel tasks. Each entry says what happened, what it cost, and how the process could be improved. The entries are evidence for changing the skills and instructions that drive the work (writing-plans, subagent-driven-development, the go/github/code conventions, CLAUDE.md), or for writing a new skill.

Add an entry, or update one, whenever planning or orchestration costs rework, stalls, or needs the owner to step in. Every entry starts at **open**. An entry that should be fixed before the next planning round is marked **Major**.

## Reviews

A process and planning review runs at four points:

1. **End of phase 1, before phase 2's tasks are planned.**
2. **End of phase 2, before phase 3's tasks are planned.**
3. **End of phase 3, before phase 4's tasks are planned.**
4. **End of phase 4.** This one also decides whether to change the existing skills or write a new one.

Each review reads every open entry. It applies the fixes the next planning round needs, and it marks each entry **fixed** (naming the change), **carried** (to the next review) or **dropped** (with the reason).

The end-of-phase-1 review settled the **Major** entries on 2026-10-13, before phase 2's tasks were planned.

### Review 1: end of phase 1 (2026-10-13)

Each fix below that is not this document lands on main before phase 2's tasks start.

| ID | Settlement | Change |
|---|---|---|
| PP-1 | fixed | The owner's global CLAUDE.md rule, "Implementation plans — behaviour and seams, not code". Phase 2's plan is written to it. |
| PP-2 | fixed | `hack/citecheck` (#280) resolves every `path:line@tag#symbol` citation and every ceph/ceph link at its tag. Phase 2's plan passes it before its review, and phase 2's research cites the source at both tags. The code-span form was never agreed; unit CL replaced it with links (PP-17). |
| PP-3 | fixed | The design spec's compatibility rule (§8, the owner, 2026-10-13). Compatibility means RADOS state in both directions, not reproducing radosgw's defects. Where the RADOS state stays compatible, rgw-go does the correct thing for the S3 API. It prevents corruption of shared data even where radosgw allows it. Each difference is recorded as an exclusion, and a serious defect is reported upstream once it is reproduced. Each phase 2 unit lists the radosgw defects it meets and its choice for each. |
| PP-4 | fixed | The design spec and the project CLAUDE.md state the owner's policy of 2026-10-12. A found defect is public by default and goes into the registry. Each possible security defect is also sent to the rgw-bug-reproduction session. The controller does not ask the owner about disclosure. The registry never links that session's private disclosure gists. Registry entries carry a security field and a reproduction status. |
| PP-5 | fixed | Phase 2's plan sets the file structure. fakerados gives each class method a file of its own, registered from a table, and the first phase 2 task makes that split. Any other file that two tasks touch has one owner task, or the tasks that touch it are ordered. The registries stay single files: tasks only append, and the controller's union rebase merges them. |
| PP-6 | fixed | Phase 2's plan sizes tasks: one layer per task (op, driver, emulator, handler or admin), a brief under about 250 lines, and a split wherever one reviewer could not judge the task in one pass. |
| PP-7 | fixed | The global CLAUDE.md's "Subagent budget" keeps the concurrency limit in one place, and its "Durable subagent work" has agents write skeletons and notes as they go. The missing usage view is Claude Code's to provide, and this registry drops it. |
| PP-8 | fixed; carried to review 4 | The owner granted merge authority behind `merge_gate.sh`, and `override_check.sh` checks the merged tree. Both are controller tools. Whether to ship them with the workflow is the end-of-phase-4 skill decision. |
| PP-9 | carried | It recurred on 2026-10-13: X41's merge was not counted until its ledger line read `X41: MERGED`. |
| PP-10 | carried | It recurred on 2026-10-13. Language-server errors from agents' worktrees kept reaching the controller, and two background pollers would have looped forever: one waited on a superseded commit, the other passed `gh` an option it does not take. |
| PP-11 | fixed | The global CLAUDE.md's bar for filing: a defect not yet reproduced on a running system stays in the registry, marked unreproduced. Phase 2's research records each defect's reproduction status. |
| PP-12 | fixed | The owner's rule of 2026-10-12 is a Global Constraint of phase 2's plan and a line in the project CLAUDE.md. A finding is fixed in its PR. A finding truly outside the change becomes a numbered task before the merge. Phase 2's gate requires zero open findings. On 2026-10-13, #275 through #278 each fixed every finding their reviews raised, in the same PR. |
| PP-13 | fixed | The owner's rule of 2026-10-12, a line in the project CLAUDE.md, and phase 2's plan, which states each task's PR split. X43 shipped as two PRs, #277 and #278. |
| PP-14 | fixed | New; see its entry. |
| PP-15 | fixed | New; see its entry. |
| PP-16 | fixed | New; see its entry. |

## Index

| ID | Problem | Severity | Candidate home | Status |
|---|---|---|---|---|
| PP-1 | Plan code treated as binding mandated a data-loss race | Major | writing-plans | fixed |
| PP-2 | Wrong radosgw facts in plans reached specs and reviews | Major | writing-plans; a citation checker | fixed |
| PP-3 | No up-front rule on reproducing a floor's security defects | Major | design spec; writing-plans | fixed |
| PP-4 | Disclosure policy for found defects was settled piecemeal | Major | design spec; project CLAUDE.md | fixed |
| PP-5 | Hot files shared by parallel tasks force a rebase on every merge | Major | writing-plans (file structure) | fixed |
| PP-6 | Tasks too large for one implement-and-review loop | Major | writing-plans (task sizing) | fixed |
| PP-7 | No view of usage limits; throttling discards in-flight work | Minor | subagent-driven-development; Claude Code | fixed |
| PP-8 | Merge authority and the merge gate were settled mid-execution | Minor | github-conventions; subagent-driven-development | fixed; carried to review 4 |
| PP-9 | The progress ledger has an implicit grammar | Minor | subagent-driven-development scripts | carried |
| PP-10 | Controller tooling friction and context noise | Minor | subagent-driven-development; Claude Code | carried |
| PP-11 | Defect claims from source reading alone were sometimes wrong | Minor | code-conventions bug reports | fixed |
| PP-12 | Review findings were parked instead of fixed in their PR | Major | subagent-driven-development; writing-plans; project CLAUDE.md | fixed |
| PP-13 | Unrelated changes were bundled into one PR | Major | writing-plans; subagent-driven-development; github-conventions | fixed |
| PP-14 | A regression test was hardened as a security boundary | Major | writing-plans; the task-review brief; project CLAUDE.md | fixed |
| PP-15 | Large shell scripts were written where Go commands were due | Major | global CLAUDE.md; writing-plans | fixed |
| PP-16 | An instructed action was turned into a question at its trigger | Major | subagent-driven-development; project CLAUDE.md | fixed |
| PP-17 | Phase 2 planning cited Ceph in a form the owner never agreed | Major | writing-plans; project CLAUDE.md; hack/citecheck | open |
| PP-18 | The controller stopped at a failed step of an unattended run | Major | subagent-driven-development; the benchmark run procedure | open |
| PP-19 | A change the plan called "its own" had no task, so notes piled up instead | Major | writing-plans; project CLAUDE.md | open |

## PP-1: Plan code treated as binding mandated a data-loss race

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:** W's plan, Task 5, gives the stripe writer as complete code. Each piece of a stripe is written from its own goroutine, `WriteFull` at offset 0 and `Write` at later offsets, with nothing ordering them. The implementer followed it. The task review found that a stripe's later `Write` can land before its `WriteFull`, which then truncates the stripe while the manifest records its full size. With Go limited to one CPU thread, a 4 MiB body at a 1 MiB chunk loses data every time. radosgw submits a stripe's pieces in order from one thread (`rgw_putobj_processor.cc:140-159`).
- **Cost:** one Critical finding, a fix round that overrode the plan, and a defect that W8's `putStream` would have inherited.
- **Improvement:** a plan states behaviour and the invariants between tasks, not function bodies. An ordering requirement is one of those invariants ("the pieces of one stripe reach RADOS in stream order"), so the plan says it in words, and a test pins it. Code a plan does carry is labelled as a sketch, and the stated behaviour overrides it. Phase 1's plans total about 37,800 lines, most of them code.
- **Candidate home:** writing-plans, which by default puts complete code in every step.

## PP-2: Wrong radosgw facts in plans reached specs and reviews

- **Severity:** Major. **Status:** fixed (review 1).
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

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:** W's plan (W-D3) keys conditional PUT on the release, so it reproduces v19.2.6's skip of every condition on a head with a fake tag. The task review and the defect triage later classified that skip as a security defect: an update is lost (tracker #68183), fixed upstream after v19.2.6 was tagged. The owner then ruled, during the fix round, that rgw-go applies v20.2.4's check on both releases. The same question is open for radosgw's silent drop of an unparseable role policy.
- **Cost:** an owner decision in the middle of a fix round, rework of finished code, and a security behaviour that would have shipped if the triage had not caught it.
- **Improvement:** decide the rule in the design spec before planning. A suggested rule: parity with the floor release, except that rgw-go follows the fixed release where the floor has a security or fail-open defect that upstream has fixed, and records an exclusion. Each plan lists every floor defect it reproduces, with its security class.
- **Candidate home:** the design spec, and writing-plans (the defect list).

## PP-4: Disclosure policy for found defects was settled piecemeal

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:** defects found while porting were held, released one at a time, released all at once ("All public"), and then needed a fresh ruling when a new security-class defect appeared. One commit message and its PR body were reworded and force-pushed before merge to keep a held defect out of a tracked file (#145).
- **Cost:** owner interruptions, held work, and history rewritten on a branch already pushed for review.
- **Improvement:** state the disclosure policy in the spec before phase 2: what is public by default, what a security classification changes, and who classifies. Give registry entries a security field. Then a new security finding has a rule to follow instead of a question to ask.
- **Candidate home:** the design spec and the project CLAUDE.md; code-conventions' bug-report rules for the upstream side.

## PP-5: Hot files shared by parallel tasks force a rebase on every merge

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:**
  - `internal/testutil/fakerados/cls_rgw.go` gets an addition from W4, W5, P2, M8 and M10b, both to its write-method list and to its class switch.
  - `docs/ceph-upstream-bugs.md` and `docs/exclusions.md` get an addition from nearly every task.
  - Every merge forces each branch in flight to rebase. The controller built a union-merge rebase script for the registry.
- **Cost:** rebases in every fix round, conflict risk, and controller time.
- **Improvement:** plan the file structure for parallel work. Give each emulator its own file, registered from a table, and give each registry entry its own file or an append-only region a tool assembles. Assign every shared file to one task, or order the tasks that touch it.
- **Candidate home:** writing-plans (file structure); rgw-go's own layout, which could change before phase 2.

## PP-6: Tasks too large for one implement-and-review loop

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:**
  - M's Task 10 was split mid-execution into 10a (op, #71) and 10b (driver).
  - W's Task 5 brief runs to 1,101 lines.
  - Implementer runs of 20 to 25 minutes are common, and an interruption loses the whole run.
- **Cost:** long review cycles and large fix rounds, and work lost whenever an agent stops.
- **Improvement:** cap the size of a task's brief, and split a task by layer (op, driver, emulator, handler) whenever it would cross more than one. Size each task so that one reviewer can judge it in one pass.
- **Candidate home:** writing-plans ("Task Right-Sizing").

## PP-7: No view of usage limits; throttling discards in-flight work

- **Severity:** Minor. **Status:** fixed (review 1).
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

- **Severity:** Minor. **Status:** fixed; carried to review 4 (review 1).
- **Observed:**
  - Merging stopped until the owner granted permission to merge without human review behind a self-review gate.
  - The gate (state, CI, alerts, a trial merge) and a check that builds and tests the merged tree when main has moved since CI (needed for #152) were written as ad hoc scripts.
- **Cost:** a stall on finished PRs, and controller time.
- **Improvement:** decide merge authority before execution starts, and ship the gate and the merged-tree check as tools of the workflow.
- **Candidate home:** github-conventions; subagent-driven-development's finishing step.

## PP-9: The progress ledger has an implicit grammar

- **Severity:** Minor. **Status:** carried (review 1).
- **Observed:** the progress report counts a task as done only when its ledger has a line `- Task N: MERGED`. A merge recorded as "complete" was not counted (P's Task 2, #151) until the line was rewritten.
- **Cost:** a wrong progress report, caught by eye.
- **Improvement:** give the ledger a schema, and write it through a script instead of free-form appends.
- **Candidate home:** subagent-driven-development's scripts.

## PP-10: Controller tooling friction and context noise

- **Severity:** Minor. **Status:** carried (review 1).
- **Observed:**
  - The worktree guard refuses compound git commands that name another worktree, so every such command became a script file.
  - The working directory drifted into the ledger tree, and relative pathspecs then came back empty.
  - A sandbox lock file broke git config writes.
  - Language-server diagnostics from agents' worktrees, which are outside the controller's workspace, reach the controller's context as dozens of false compile errors.
- **Cost:** controller turns and context spent on tooling, not on the work.
- **Improvement:** ship a controller script library with the workflow, and keep agents' worktrees out of the controller's language-server workspace.
- **Candidate home:** subagent-driven-development; Claude Code feedback.

## PP-11: Defect claims from source reading alone were sometimes wrong

- **Severity:** Minor. **Status:** fixed (review 1).
- **Observed:**
  - A claimed radosgw defect (an endless loop when `rgw_max_chunk_size` is 0) was disproved at registry triage, before publication (#150): the PUT gets 400 RequestTimeout instead.
  - Independent classification also changed the severity of other entries.
- **Cost:** a retraction, and triage time.
- **Improvement:** registry entries carry their reproduction status ("unreproduced" until a running release shows the defect). Plans budget a cluster reproduction step for every defect a task records.
- **Candidate home:** code-conventions' bug-report rules; the project's registry template.

## PP-12: Review findings were parked instead of fixed in their PR

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:**
  - The controller marked review findings it judged minor or out of scope as "parked": recorded as a line in a task's progress ledger, while the PR merged without them. The phase 1 ledgers mention parking 28 times across eight task streams.
  - A follow-up series, X1 to X42, collected findings and review leads after their PRs had merged.
  - On 2026-10-12, four findings parked after their PRs merged were picked up only when the owner objected: X38's Host-refusal rows checked only the status, X39's driver and gateway read the request cap separately, X40 left retry-path lost writes and an untested re-read branch. The same day a stale spec sentence found in X41's review was split into a separate PR instead of being fixed in X41.
- **Cost:** rework in later PRs, more rebases of the same files, and the owner's intervention. Nothing but a ledger line kept a parked defect from being lost.
- **Improvement:**
  - A finding is fixed in the PR whose review found it. A task's review loop ends with no open finding, at any severity.
  - A finding that truly lies outside the PR's change becomes, before that PR merges, a numbered task in the plan with its evidence, which the progress bar counts. A ledger line is never its only record.
  - The phase 2 plan states this as a Global Constraint, and phase 2's gate requires zero open findings.
- **Candidate home:** subagent-driven-development (its "park findings in the ledger" path); writing-plans (Global Constraints); the project CLAUDE.md.

## PP-13: Unrelated changes were bundled into one PR

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:**
  - The controller treated one task as one PR. A task that collected several independent fixes shipped them together, so the fastest waited for the slowest.
  - X35 (#259) carried six commits on unrelated subjects: the shared request limit, a multipart unlock, a NUL in a metadata key, and three documentation fixes.
  - X42 collected four unrelated findings. Three were finished and reviewed clean, yet waited for hours behind the fourth (a bucket rename retry) through its review rounds, until the owner objected on 2026-10-12.
- **Cost:** finished fixes held back from main, larger mixed diffs to review, and a defect in one change blocking the others.
- **Improvement:**
  - A task may be decomposed into several PRs. Each independently testable change ships as its own PR as soon as it is done and reviewed; a PR never waits on an unrelated change.
  - Unrelated changes never share a PR. Related changes that must land together share one, as a logical commit series.
  - A plan task states its PR split where it holds more than one independent change. The controller splits a branch before opening a PR, not after review.
- **Candidate home:** writing-plans (task-to-PR decomposition); subagent-driven-development (PR boundaries in the task loop); github-conventions (PR scope).

## PP-14: A regression test was hardened as a security boundary

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:**
  - X41 turned the parity baselines into append-only ledgers that are checked on every pull request.
  - Its reviews treated a pull request's author as an attacker: argument and output injection, symbolic links, trust in the base's `run.sh`, forged workflow commands. Each round found another way around the checker and added another fix.
  - On 2026-10-12 the owner ruled: "This is not a security boundary. It's a regression test." The hardening stopped and the documents were reframed. A protected-path extension to the merge gate was reverted. Test names that still spoke of a trust boundary were renamed before the merge (#275).
- **Cost:** several review and fix rounds on X41, a larger tool than its job needed, and the owner's intervention.
- **Improvement:**
  - A plan names each component's threat model: whether it defends against honest mistakes or against an adversary.
  - Reviews judge a component against its model.
  - Adversarial review is for the components the spec names as security boundaries, such as authentication, authorization and the parsers that face clients. Test and build tooling is not among them.
- **Fix:** phase 2's plan gives each unit a threat-model line. The task-review brief tells reviewers to judge against it. A line in the project CLAUDE.md states it.
- **Candidate home:** writing-plans; the task-review brief.

## PP-15: Large shell scripts were written where Go commands were due

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:**
  - T15a's limiter re-run was first written as a 737-line bash script that did its parsing and arithmetic in shell.
  - The owner rejected it: "A 737 line bash script is completely unacceptable." and "parsing output in bash is a red flag". It was redone as a Go command (#268).
  - The benchmark scripts beside it were large too, all written in phase 1: `seam.sh` (639 lines), `gateway.sh` (614) and `sample.sh` (104).
- **Cost:** a rejected change, its redo, and two ports.
- **Fix:**
  - The global CLAUDE.md rule "Shell is thin glue, never the program", approved on 2026-10-12.
  - X43 ported `seam.sh`, `gateway.sh` and `sample.sh` to Go (#277, #278).
  - `hack/rooket`'s scripts remain shell: `populate.sh` (311 lines), `rgw-go-up.sh` (194) and `lib.sh` (178). Under the rule, each is ported before it next grows.
- **Candidate home:** the global CLAUDE.md (done); writing-plans, so that no plan task specifies shell beyond glue.

## PP-16: An instructed action was turned into a question at its trigger

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:**
  - On 2026-10-13 the owner said to run the benchmarks after phase 1 completed.
  - When phase 1 completed, the controller asked the owner to confirm the quiet-run conditions (the other sessions paused, the desktop idle) instead of running them. Its quiet-run procedure listed that confirmation as a step.
  - By the time the owner answered, the host was no longer quiet: "WTF, I told you to run the benchmarks after phase 1 work ended and you didn't." The run was deferred.
- **Cost:** a lost quiet window. Phase 2's tasks now wait for the next one, since the spec runs the benchmarks before them.
- **Improvement:** an instruction to act when a condition is met is the go-ahead at that moment. The controller checks the preconditions it can observe itself (agents drained, the other sessions idle, the load average, the processes running) and acts. It asks only for what it cannot observe, and asks before the trigger, not at it.
- **Fix:** the quiet-run procedure's owner-confirmation step becomes a controller check. A line in the project CLAUDE.md states the rule.
- **Candidate home:** subagent-driven-development (its "four things stop you" list); the project CLAUDE.md.

## PP-17: Phase 2 planning cited Ceph in a form the owner never agreed

- **Severity:** Major. **Status:** open.
- **Observed:**
  - On 2026-09-29 the owner set the citation form: references to code line numbers are to be "clickable links to the line or line range in the github repo which that code lives anchored to the correct tag or ref". Phase 1's plans use such links, though five in Z's plan were written inside code spans, which renders them as code. The rule was not written into the project CLAUDE.md.
  - On 2026-10-13 the controller, fixing PP-2, introduced a second form in #280: `hack/citecheck` was built around a code span, `src/<path>:<lines>@<ref>#<symbol>`, which renders as code, not as a link. It was never agreed with the owner. Phase 2's research and plan were written in it (#281), and the plan's index made it a constraint of the set.
  - It spread the next day: the floor bump's updates of the registries, `docs/ceph-upstream-bugs.md` and `docs/exclusions.md` (#284, #286), and the plan's tasks F12 and F13 (#285) were written in it. At 89fd1fe the tracked Markdown held 3,013 such spans in 20 files: 1,838 in phase 2's research, 1,077 in its plan, and 98 in the two registries.
  - On 2026-10-14 the owner objected: "I am unhappy about seeing lines like `` (`src/rgw/rgw_op.cc:6902-6904@v19.2.7#RGWDeleteMultiObj::handle_individual_object`, `` in the docs. I thought we had agreed to use clickable links?" and "What is the citecheck format? That is completely your invention."
- **Cost:**
  - Unit CL's first two tasks: citecheck reworked so that a link is the only form and a span is rewritten into one (CL1, #297), and a sweep (CL2) that rewrites those 3,013 spans across the registries, plans and research, in a PR that needs the owner's LGTM.
  - CL1 was re-scoped mid-task: its first scope added another invented notation, a link title holding the symbol, which was dropped with the span form.
  - Every branch open during the sweep rewrites its own docs' citations before it merges, and the sweep is regenerated on main just before its own merge.
  - The owner's intervention.
- **Improvement:**
  - A notation that reaches a durable artifact, such as a citation form, is the one the owner stated, or is agreed with the owner before a tool or a plan is built on it.
  - A rule the owner states is written into the project CLAUDE.md the day it is stated, so that a later planning round reads it instead of inventing its own.
  - A checker enforces the agreed form; building one does not settle a new form.
- **Fix:** unit CL (`docs/superpowers/plans/2026-10-13-phase-2/CL-citation-links.md`). The project CLAUDE.md states the rule (#295). citecheck fails a `path:lines@ref` code span as "not a link" and rewrites one into a link (CL1, #297). CL2 rewrites every `path:lines@ref` code span citation in the Markdown into a link, and `make check` and CI run the clone-free form check over every tracked `.md`. CL3 and CL4 carry the rule, which the owner extended to code on 2026-10-14, into Go comments. CL5 (planned in #301) sweeps the docs-era plain citations in the Markdown, `<basename>:<lines>` with the tag in the prose, which citecheck's grammar does not see.
- **Candidate home:** writing-plans (a notation in a plan is the owner's agreed one); the project CLAUDE.md (done, #295); `hack/citecheck` (done, CL1).

## PP-18: The controller stopped at a failed step of an unattended run

- **Severity:** Major. **Status:** open.
- **Observed:**
  - On 2026-10-15 the owner was away for ten hours. The controller started the deferred benchmark run at 02:55
    local, in a quiet window, from a fresh checkout of main, as PP-16's fix says.
  - The first attempt failed in three seconds at Squid's gate: the fresh checkout had no population manifest,
    which `make populate` writes. Populate became a step of the run, and the second attempt started.
  - Squid's two gateway comparisons ran from 02:57 to 04:08 local. The rgw-go one measured every profile, but
    rendering its report failed: rgw-go's JSON log and the Go runtime's gctrace share its stderr, and one record
    landed in the middle of a gctrace line, which the report's parser refused.
  - The controller read that failure and ended its turn without deciding anything. Nothing ran for four hours,
    until the owner wrote "continue" at about 08:05.
  - By then the owner was back. The controller continued with the run, and the owner stopped it: "the system
    isn't currently quiet".
- **Cost:**
  - Tentacle's gateway comparisons and both releases' seam limiter cells wait for another quiet window. Phase 2's
    S1 to S3 wait behind them, since the spec runs the benchmarks before phase 2's tasks.
  - A fix task (B1, #326) so that the report renders from a torn gctrace log, and its review rounds.
- **Improvement:**
  - A failed step of an unattended run gets a decision in the turn that reads it: continue, retry, or stop with
    the reason recorded. Ending a turn on a failure read is a stall.
  - A step whose failure leaves the measurement intact, such as rendering a report, does not stop the run. The run
    script marks it soft, and the next step starts.
  - A run's prerequisites in a fresh checkout, such as the population manifest and the elbencho build, are the run
    script's own steps, not knowledge the controller must remember.
- **Candidate home:** subagent-driven-development (the controller's loop); the benchmark run procedure.

## PP-19: A change the plan called "its own" had no task, so notes piled up instead

- **Severity:** Major: it recurred, and each time it cost the owner an ack (2026-10-16). **Status:** open.
- **Observed:**
  - Phase 2's plan says every rgw-go fix carries an s3-test merged into jhoblitt/ceph's `rgw-go-s3tests` branch,
    and "the pin bump is its own change". No task owned that change.
  - Nine fork PRs merged past the pin, f6f2529. Instead of bumping it, the controller wrote a note into the plan's
    index listing the known-differences lines the next bump would need (#317), and revised the note as tests
    landed. It never covered X38's and F5's tests, which as it turned out needed no line.
  - On 2026-10-15 the owner asked: "Why are we making a note to bump the pin instead of just bumping the pin?"
  - The bump (PB1, #325) found that `make s3tests-pin` refused this move as not additive: it adds module-level
    imports and X38's helpers to test_headers.py, and T5 changes a helper in test_s3.py. So both ledgers were
    recorded in full, on both releases.
  - It recurred on 2026-10-16. Fork PRs jhoblitt/ceph#10 to #13 sat past the pin, ab228b1, and #355 again wrote the
    next bump's lines into the plan instead of bumping it: deselect lines for nine tests whose ops rgw-go does not
    route yet, known-differences lines for V10's two PutBucketVersioning tests, and BC-22's later replacement of
    both. Code PRs #353, #354 and #367 waited on #355's LGTM.
  - The owner, 2026-10-16: "I want to be clear that going forward, tasks which change s3-tests need to do their own
    version bump". The owner added that the problem had come up several times and needed too many owner acks.
- **Cost:**
  - #317 was written, revised and closed unmerged, and the owner had to step in.
  - CI's s3-tests parity did not run the 39 new tests until #325, so the fixes they cover had no regression test in
    CI in that time.
  - #355 was written and needed the owner's LGTM, three code PRs waited on it, and jhoblitt/ceph#10's test has not
    run in CI since it merged on 2026-10-15.
- **Improvement:**
  - Work that a rule calls its own change gets a task at plan time, with its trigger, such as a pin bump at the end
    of each unit. A note describing future work does not replace the work.
  - A pin move is planned as a full record unless `make s3tests-pin` proves it additive, and a full record is a
    cluster task on both releases.
  - The owner's rule of 2026-10-16: a task that adds or changes an s3-test moves the s3-tests pin itself, in its own
    rgw-go PR, with every known-differences and deselect line the move needs. The plan carries no note for a later
    bump, and no later task owns the move. Where the move is not additive, the task's full record on both releases is
    part of the task.
- **Candidate home:** writing-plans; phase 2's plan; project CLAUDE.md.
