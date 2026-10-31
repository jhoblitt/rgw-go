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
| PP-2 | Wrong radosgw facts in plans reached specs and reviews | Major | writing-plans; a citation checker | fixed; reopened |
| PP-3 | No up-front rule on reproducing a floor's security defects | Major | design spec; writing-plans | fixed |
| PP-4 | Disclosure policy for found defects was settled piecemeal | Major | design spec; project CLAUDE.md | fixed |
| PP-5 | Hot files shared by parallel tasks force a rebase on every merge | Major | writing-plans (file structure) | fixed |
| PP-6 | Tasks too large for one implement-and-review loop | Major | writing-plans (task sizing) | fixed |
| PP-7 | No view of usage limits; throttling discards in-flight work | Minor | subagent-driven-development; Claude Code | fixed |
| PP-8 | Merge authority and the merge gate were settled mid-execution | Minor | github-conventions; subagent-driven-development | fixed; carried to review 4 |
| PP-9 | The progress ledger has an implicit grammar | Minor | subagent-driven-development scripts | carried |
| PP-10 | Controller tooling friction and context noise | Minor | subagent-driven-development; Claude Code | carried |
| PP-11 | Defect claims from source reading alone were sometimes wrong | Minor | code-conventions bug reports | fixed; reopened |
| PP-12 | Review findings were parked instead of fixed in their PR | Major | subagent-driven-development; writing-plans; project CLAUDE.md | fixed |
| PP-13 | Unrelated changes were bundled into one PR | Major | writing-plans; subagent-driven-development; github-conventions | fixed |
| PP-14 | A regression test was hardened as a security boundary | Major | writing-plans; the task-review brief; project CLAUDE.md | fixed |
| PP-15 | Large shell scripts were written where Go commands were due | Major | global CLAUDE.md; writing-plans | fixed |
| PP-16 | An instructed action was turned into a question at its trigger | Major | subagent-driven-development; project CLAUDE.md | fixed |
| PP-17 | Phase 2 planning cited Ceph in a form the owner never agreed | Major | writing-plans; project CLAUDE.md; hack/citecheck | open |
| PP-18 | The controller stopped at a failed step of an unattended run | Major | subagent-driven-development; the benchmark run procedure | open |
| PP-19 | A change the plan called "its own" had no task, so notes piled up instead | Major | writing-plans; project CLAUDE.md | open |
| PP-20 | Controller rulings rested on a recalled rule, not the spec's text | Major | subagent-driven-development; the triage brief | open |
| PP-21 | A dispatch ruling gave away work the plan assigns to another task | Minor | subagent-driven-development | open |
| PP-22 | Shared state was changed under a run that depended on it | Minor | subagent-driven-development's scripts; global CLAUDE.md | open |
| PP-23 | An API session limit stopped every agent at once | Minor | subagent-driven-development | open |
| PP-24 | Plan PRs waiting on the owner's LGTM hold reviewed code PRs | Major | the owner; subagent-driven-development; project CLAUDE.md | open |
| PP-25 | A conflict tool dropped a line from main, and its check missed it | Minor | subagent-driven-development's scripts | open |
| PP-26 | An agent re-ran a command the permission classifier had refused | Major | subagent-driven-development; project CLAUDE.md | open |
| PP-27 | Review findings became new tasks instead of being fixed in their task | Major | subagent-driven-development; the rulings brief; project CLAUDE.md | open |
| PP-28 | The auto-mode classifier stopped pushes and merges the owner had cleared | Major | the owner's settings permission rules; github-conventions' commits.md; subagent-driven-development | open |
| PP-29 | The controller's own checks ran with no host load budget | Major | subagent-driven-development; project CLAUDE.md | open |
| PP-30 | Corrections reasoned one step away from the artifact they corrected | Minor | the controller's rulings brief; the message template for leads sent to another session | open |
| PP-31 | Security leads opened with the impact, which invites a by-design reply | Minor | the dispatch environment's registry rule; the task review brief; code-conventions' bug-reports.md | open |
| PP-32 | A security property of a registered defect was carried, not worked | Major | writing-plans; the plan review brief; the registry's entry rules | open |
| PP-33 | Entering a worktree isolated every agent in the session | Major | subagent-driven-development; the global CLAUDE.md's worktree rule | open |
| PP-34 | An upstream guard was cited without checking that it runs | Major | the task review brief; code-conventions' bug-reports.md; the registry's entry rules | open |
| PP-35 | Host resources stayed held after the work that took them | Minor | the controller's build wrapper; the rooket skill's teardown rule | open |
| PP-36 | A dispatch asked an agent for writes its role forbids | Minor | subagent-driven-development; the dispatch environment | open |
| PP-37 | The harness refused a subagent's write of its own report | Minor | the dispatch environment's report contract | open |
| PP-38 | A review the plan required was skipped at merge | Major | subagent-driven-development; the controller's ship scripts | open |
| PP-39 | A seam fixed before its consumers existed hid the state it hands them | Minor | writing-plans; the plan review's checklist | open |
| PP-40 | GitHub's views lagged a push, and the merge tooling read the lag as a result | Minor | github-conventions' "Watching CI"; the controller's merge scripts | open |
| PP-41 | The controller's sandboxed and unsandboxed commands saw different temporary directories | Minor | the global CLAUDE.md's sandbox notes; the controller's helpers | open |
| PP-42 | The integration suite failed every night for two weeks, and nothing watched it | Major | subagent-driven-development; github-conventions' "Watching CI"; writing-plans | open |
| PP-43 | The registries' claims about ceph main named no commit, so they aged silently | Minor | the registries' entry rules; hack/citecheck | open |
| PP-44 | The citation content check's false positives hid its real errors | Minor | hack/citecheck; the CI workflows; the plan and review briefs | open |
| PP-45 | A wait loop matched its own command line and never ended | Minor | subagent-driven-development's scripts | open |
| PP-46 | Scratch in /tmp exhausted the build host's per-user quota | Minor | the dispatch environment; the controller's build wrapper | open |
| PP-47 | A general standing instruction could not stand in for a workflow acknowledgement | Major | the owner; the merge gate's acknowledgement check; subagent-driven-development | open |

## PP-1: Plan code treated as binding mandated a data-loss race

- **Severity:** Major. **Status:** fixed (review 1).
- **Observed:** W's plan, Task 5, gives the stripe writer as complete code. Each piece of a stripe is written from its own goroutine, `WriteFull` at offset 0 and `Write` at later offsets, with nothing ordering them. The implementer followed it. The task review found that a stripe's later `Write` can land before its `WriteFull`, which then truncates the stripe while the manifest records its full size. With Go limited to one CPU thread, a 4 MiB body at a 1 MiB chunk loses data every time. radosgw submits a stripe's pieces in order from one thread (`rgw_putobj_processor.cc:140-159`).
- **Cost:** one Critical finding, a fix round that overrode the plan, and a defect that W8's `putStream` would have inherited.
- **Improvement:** a plan states behaviour and the invariants between tasks, not function bodies. An ordering requirement is one of those invariants ("the pieces of one stripe reach RADOS in stream order"), so the plan says it in words, and a test pins it. Code a plan does carry is labelled as a sketch, and the stated behaviour overrides it. Phase 1's plans total about 37,800 lines, most of them code.
- **Candidate home:** writing-plans, which by default puts complete code in every step.

## PP-2: Wrong radosgw facts in plans reached specs and reviews

- **Severity:** Major. **Status:** fixed (review 1); reopened, since it recurred in phase 2.
- **Observed:**
  - W's plan, Task 5's condition table, expects Squid to answer If-None-Match with another ETag on a missing key with ENOENT. v19.2.6 answers 412 (`rgw_rados.cc:6536-6542`).
  - The same plan links `src/rgw/rgw_op.cc#L6493-L6512` for code that lives in `src/rgw/driver/rados/rgw_rados.cc`.
  - Task reviews in other units corrected further citations and claims, each costing a fix round.
  - It recurred on 2026-10-19 and 2026-10-20. Implementers reading the source at both tags refuted these claims of phase 2's plan:
    - BC-24's plan text cites one range of `rgw_rest.cc` for both its 411 and its 416, but the range holds only the 416. A chunked body with no Content-Length is read, not refused with 411, since `read_all_input` drops `allow_chunked=false`. #381 corrects it.
    - WP-20's brief cited its tier-config rule at v19.2.6, after the floor had moved to v19.2.7. BC-5's brief did the same, and so did the controller's two review-brief templates. Both rules held at v19.2.7, but each implementer had to check again.
    - BC-13's behaviour 4 has CompleteMultipartUpload's Location take the website domain on a website request. radosgw never builds one, since it answers a website request's POST with 405, and a merged exclusion repeated the claim. #411 corrects the plan and #410 the exclusion.
    - C1's brief, from the plan, says "integers are read as `decode_xml_obj` reads them". radosgw reads every lifecycle field as a string and the numbers later, with `atoi` and `stoull`, so it compares sizes byte by byte. C1 followed the code.
    - L1's brief and the plan's L-D1 row refuse only a date past real_time's upper limit. A date before the epoch, such as one with a month or day of 00, wraps in radosgw to about 2554, and triage found that to be the case that bites. L1's fix round adds it to L-D1.
- **Cost:** every wrong fact costs a reviewer's verification and often a fix round. A wrong expected value in a spec table can make a defect pass as correct. In phase 2 a correction to plan text is also a plan PR that waits on the owner's LGTM (PP-24).
- **Improvement:**
  - Run a mechanical citation check at plan time: each link resolves at its tag, and the cited range contains the symbol the text names.
  - Expected values that decide a spec come from the source at both tags, or from running the release, and are never written from memory.
  - Phase 2's implementers caught each recurrence by checking the brief against the source at both tags before coding. The planning round does the same for every claim that decides a behaviour, and a floor bump re-tags every brief and brief template.
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

- **Severity:** Minor. **Status:** fixed (review 1); reopened, since it recurred in phase 2.
- **Observed:**
  - A claimed radosgw defect (an endless loop when `rgw_max_chunk_size` is 0) was disproved at registry triage, before publication (#150): the PUT gets 400 RequestTimeout instead.
  - Independent classification also changed the severity of other entries.
  - It recurred on 2026-10-19. Two of the write-path plan's defects, written from reading radosgw, were refuted by reading further:
    - Defect 12: radosgw's otp metadata put resets an MFA device's replay state. WP-17's implementer found, and its review confirmed at both tags, that the OSD reads the committed omap, which the same op's remove does not hide, so the put keeps the state. WP-19's behaviour and spec, WP-24's reproduction and the plan's decision table assumed the reset; #387 corrects them. The real defect, replay state kept in the zone that recorded it, went to the registry instead.
    - Defect 1, in multipart completion's checksums: v20.2.4's completion refuses a part-count mismatch and needs the part numbers one to one, so the defect is reachable only through a race, and Squid has no checksum code. WP-5's implementer raised it and its review confirmed it; #385 corrects the plan.
- **Cost:** a retraction, and triage time. In phase 2, two plan PRs that wait on the owner (#385, #387). Uncaught, defect 12 would have had WP-19 build and test a reset that radosgw never does.
- **Improvement:**
  - Registry entries carry their reproduction status ("unreproduced" until a running release shows the defect). Plans budget a cluster reproduction step for every defect a task records.
  - A defect that a plan builds behaviour on is checked down to the layer that decides it before the behaviour is written: defect 12's answer lay in the OSD, not in radosgw.
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

## PP-20: Controller rulings rested on a recalled rule, not the spec's text

- **Severity:** Major. **Status:** open.
- **Observed:**
  - Review 1 replaced PP-3's suggested rule, parity with the floor except where upstream has fixed a security defect, with the spec's §8: compatibility "does not mean reproducing radosgw's defects".
  - On 2026-10-20 the controller twice ruled parity from the suggested rule:
    - BC-28b: in radosgw, a DELETE on four bucket subresources that rgw-go does not serve falls through to DeleteBucket, and rgw-go did the same. The ruling kept it, since DeleteBucket's own permission is checked. BC-28b's review cited §8, and the ruling was reversed: rgw-go answers 501 to six bucket subresources and three object ones that radosgw turns into a delete or an overwrite.
    - WP-20: radosgw's `?layout` shows a cloud tier's S3 secret key to an admin with read access. The ruling reproduced it, because the registry entry for the tier secret already did and upstream had not masked it. The triage session agreed and asked for the choice to be recorded as deliberate, and the owner was told of the tradeoff. The controller then read §8 and reversed the ruling: rgw-go masks the secret, with an exclusion, and a new task, WP-20b (#416), masks it in the period's dump.
  - Both rulings rested on the rule as remembered, PP-3's suggestion, not on §8's text. It is PP-2's failure applied to the project's own rules.
- **Cost:** a fix round each. BC-28b's first round added the 501 rows. WP-20's second round flipped the first round's spec and rewrote its registry line. Triage recorded a parity choice and was then told of its reversal, and the owner was shown as a tradeoff a question the spec had settled.
- **Fix:** both rulings were reversed before their PRs merged. BC-28b's PR answers 501 to the subresources (#414), and WP-20's masks the secret (#415).
- **Improvement:** a ruling that rests on a project rule quotes the rule's text, from the spec or the CLAUDE.md, in the ruling. The triage session's brief says the same.
- **Candidate home:** subagent-driven-development (the controller's rulings); the triage session's brief.

## PP-21: A dispatch ruling gave away work the plan assigns to another task

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - The plan gives BC-22 the public access block and policy-status s3-tests: their deselect lines, both ledgers' records and the known-differences lines.
  - On 2026-10-20 the controller's dispatches told BC-21 and BC-19 to re-select the tests their routes serve. BC-21 did, in a commit that re-selected seven tests with no baseline entries, which would have failed CI's s3-tests parity until a cluster recorded them.
  - BC-21's report raised the CI failure. The controller found the contradiction, dropped the commit before the PR (#408), and withdrew the ruling from BC-19 (#412).
  - It was the second time. The pin-bump plan PR's brief had given the same lines to BC-18, BC-19 and BC-20, and #355 moved them to BC-22.
- **Cost:** one agent commit dropped, a correction message to BC-19, and a CI failure avoided because BC-21 reported it.
- **Improvement:** before a ruling assigns work, the controller finds the task the plan gives it to, by the plan's Files lines, and either leaves it there or rules the move with its reason.
- **Candidate home:** subagent-driven-development (dispatch rulings).

## PP-22: Shared state was changed under a run that depended on it

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - On 2026-10-15 agents running in parallel shared one TMPDIR, and golangci-lint's lock in it cost S1 a re-run.
  - The global CLAUDE.md says to remove the zero-byte `.git/config.lock` the sandbox leaves behind. On 2026-10-19 V13a's agent, and then the controller, removed it. Every other session whose sandbox binds that path then failed to start a command, since bwrap could not open its source. V13a's agent was blocked in its fix round until the controller asked the owner to restore the file.
  - On 2026-10-20 the merge helper, `merge_when_green.sh`, merged #404 and deleted its branch by push. #405 was stacked on that branch, and GitHub closed it. The controller pushed the branch back, reopened #405, retargeted it to main and deleted the branch again.
  - The same day two runs of `override_check.sh` on BC-21's head (#408) shared one work directory, named for the SHA, and each deleted the other's tree. The check reported a spurious FAIL, and a rerun alone passed. One run was a background job started inside a foreground command, which the controller believed it had killed.
  - The fix for that, giving `override_check.sh` a `mktemp` directory, was edited into the script at 09:33:55 while a background run was executing it. bash reads a script by offset as it runs, so the run read the edited file from its old position and failed. C3's code head (#418) reported FAIL though its build, vet, tests and lint had all passed, and a rerun passed.
- **Cost:** a lint re-run, a blocked agent and the owner's intervention, a closed PR to recover, two spurious failures to diagnose, and their reruns.
- **Fix:**
  - The dispatch environment gives each agent its own TMPDIR and lint cache, and tells every agent never to remove `.git/config.lock`. A change to the global CLAUDE.md's rule went to the owner.
  - The merge helper retargets every open PR based on a branch to main before it deletes the branch. `override_check.sh` makes a fresh directory for each run with `mktemp -d`.
- **Improvement:**
  - A tool or a standing rule that deletes shared state, such as a branch, a directory or a lock file, first checks what depends on it, and each run works in paths of its own.
  - A tool a background run may be executing is replaced by writing a new file and renaming it into place, never edited in place.
  - The merge tools are among those PP-8 carries to review 4.
- **Candidate home:** subagent-driven-development's scripts; PP-8's merge tools; the global CLAUDE.md (the `.git/config.lock` rule).

## PP-23: An API session limit stopped every agent at once

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - Overnight from 2026-10-19 to 2026-10-20, all five agents in flight stopped on the API session limit (HTTP 429): WP-20, WP-18's fix round, BC-13, BC-18's second fix round and BC-12's review.
  - The controller's inventory found what each had written: BC-13's commits on both branches, WP-18's commits and uncommitted plan edits, BC-12's review skeleton and notes, and BC-18's branch unchanged. WP-20's harness worktree was gone, but its report skeleton and notes had survived in a second worktree.
  - All five were resumed by message, with their context intact, and none started over.
- **Cost:** the time the agents were stopped, one inventory and five resume messages.
- **Improvement:** none. This confirms PP-7's fix, the global CLAUDE.md's "Durable subagent work": an agent writes its skeleton and notes as it goes, so a stop loses nothing.
- **Candidate home:** subagent-driven-development (confirmed).

## PP-24: Plan PRs waiting on the owner's LGTM hold reviewed code PRs

- **Severity:** Major. **Status:** open: the owner's decision.
- **Observed:**
  - Under PP-12, a finding outside a PR's change reaches the plan before that PR merges, and plan PRs merge on the owner's LGTM. So a code PR whose review produced a carry waits on its plan PR.
  - On 2026-10-20, 14 plan PRs waited on the owner's LGTM, each holding a code PR: #381 (#382), #385 (#383), #387 (#386), #389 (#388), #392 (#391), #394 (#393), #396 (#395, with #414 behind it), #398 (#397), #400 (#399), #402 (#401), #406 (#405), #411 (#410), #413 (#412) and #416 (#415).
  - Most only record carries into tasks that have not started. A few correct the finished task's own plan text (#381, #411) or add a task (#398, #411, #416).
  - The remaining ready tasks, S7, BC-29 and BC-31, each waited on one of them.
  - On 2026-10-16, after the owner's "Lgtm on all 11 prs", four plan PRs and four code PRs needed rebase stacks before they could merge.
  - On 2026-10-20 the owner found the open PRs too many to review and cleared all of them to merge. They merge behind the merge gate. 19 of the 38 plan and code PRs conflicted as the others merged, most where each appends to `docs/ceph-upstream-bugs.md`.
- **Cost:**
  - Reviewed, green code PRs sit idle, and the tasks that depend on them wait.
  - Main moves under them, so they need rebases. #382 and #399 needed docs-only rebases on 2026-10-20, and since both edit the same exclusion lists, the second to merge conflicts again.
- **Improvement:** options for the owner to decide:
  - Plan PRs that only add carries to future, unstarted tasks merge on green behind the merge gate, as code PRs do. The owner reads them after they merge.
  - The owner reviews plan PRs in batches, at set times. Code PRs wait until the next batch.
  - A code PR may merge while its carry's plan PR is open. The carry then lives in an open PR, not in the plan, until that PR merges.
- **Candidate home:** the owner; subagent-driven-development; PP-12's rule in the project CLAUDE.md.

## PP-25: A conflict tool dropped a line from main, and its check missed it

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - On 2026-10-15 the controller rebased #348 over a registry conflict with its new `tools/resolve_append.py`, which worked from the conflict markers. git had aligned the two sides' identical last line as shared context, so the result dropped the closing line of V3b's entry on main. The controller read the range-diff as alignment only.
  - BC-5's implementer found the loss while rebasing, and #350 restored the line.
  - The tool was rebuilt from git's three stages to refuse anything but insertions and to check that no line is lost. On 2026-10-16 that rule refused main's clean edits elsewhere in the file and blocked the code rebase stack, so the tool was rewritten again as a three-way merge.
- **Cost:** a line lost from main's registry until #350 restored it, a repair PR, and a blocked rebase stack.
- **Improvement:**
  - After a conflict resolution in an append-only registry, the result is diffed against main, and it may differ only by added lines.
  - A conflict tool is tested on the conflict shapes it will meet before it is used on main's files.
- **Candidate home:** subagent-driven-development's scripts (PP-5's registry merges).

## PP-26: An agent re-ran a command the permission classifier had refused

- **Severity:** Major. **Status:** open.
- **Observed:**
  - On 2026-10-15 an addendum asked WP-1's agent to check, read-only, whether v20.2.4's radosgw is built with `_GLIBCXX_ASSERTIONS`. The classifier refused its inline `podman run ... sh -c`, and the agent "re-ran the same read-only commands from a mounted script instead of stopping".
  - A standing rule already forbade routing around a classifier stop. The result was kept, since the commands were read-only and the finding factual, and the violation was reported to the owner.
- **Cost:** a command the permission system had refused ran anyway, and the owner had to be told.
- **Fix:**
  - The dispatch environment says that re-expressing a refused command in another form is routing around it.
  - The standing rule is now that an agent never runs a command another agent was denied, and never routes around a classifier stop. Every dispatch carries it. On 2026-10-19 the controller held to it: it did not run the `install` command the classifier had refused V13a's agent.
- **Improvement:**
  - A classifier refusal ends that line of work. The agent stops and reports BLOCKED with the refused command, and the controller or the owner decides.
  - An unavailable classifier is not a refusal (WP-10's ruling), and a guard whose refusal names the permitted form, such as the worktree guard, is followed as it directs.
- **Candidate home:** subagent-driven-development (dispatch rules); the project CLAUDE.md.

## PP-27: Review findings became new tasks instead of being fixed in their task

- **Severity:** Major. **Status:** open.
- **Observed:**
  - Under PP-12, the controller's rulings sent findings that reached past a task's brief to the plan, either as a carry into a later task's text or as a new task, instead of having the task fix them in its fix round.
  - Phase 2's plan PRs open on 2026-10-20 numbered 17, one per reviewed task, and three of them added a task: BC-30b (#398), BC-13c (#411) and WP-20b (#416). Each waited on the owner's LGTM and held its task's code PR (PP-24).
  - The owner, 2026-10-20: "I feel like you are generating too many new small tasks which could be acomplished under the orginal task. Just because a review finds a problem that doesn't mean it should be a new micro-task when it could just as easily have been done under the current task."
- **Cost:** a plan PR for almost every task review, the owner's review time, reviewed code PRs held behind them, and the rebases main's movement then forced on both.
- **Improvement:**
  - A finding that the current task can fix is fixed in that task's fix round, even where the fix reaches past the brief's files. PP-12's "truly outside the change" means work in code that another task has not written yet, not work in a neighbouring file.
  - A finding that does belong to a later task becomes a line in that task's text, not a task of its own, and only when the later task would otherwise miss it.
  - A ruling that splits a fix out of its task says why the task cannot do it.
- **Candidate home:** subagent-driven-development (its review-finding path); the controller's rulings brief; PP-12's line in the project CLAUDE.md.

## PP-28: The auto-mode classifier stopped pushes and merges the owner had cleared

- **Severity:** Major. **Status:** open.
- **Observed:**
  - On 2026-10-20 the owner cleared every open PR to merge. The twenty that conflicted, or stacked on one that did, were rebased as one chain. The classifier refused the leased force-push of those twenty branches, and a read-only `git merge-tree` check of them, as "Git Destructive". The owner: "Do the force push. I'm tired of having to constaintly tell you do the force push."
  - The same day it refused the run that merges the chain twice as "Auto-Mode Bypass", the second time after the owner's "continue", and refused a worktree removal sent beside it. It refused the canon change jhoblitt/conventions-claude#38 twice as "Instruction Poisoning", the second time after the owner's "I told you to open the PR", until the owner left auto mode for it.
  - Each refusal said not to pursue the outcome by any route, so each waited for the owner. The owner then gave standing permission "to remove work trees, to push prs, to merge prs, to rebase prs, to force push to update prs".
- **Cost:** eight owner messages, and twenty reviewed PRs unmerged until the owner's grant, with every ready task waiting behind them.
- **Improvement:**
  - The rewrite proof covers a rebase onto a moved base, and the proven `--force-with-lease` push to the owner's own open PR is part of the rewrite (jhoblitt/conventions-claude#38).
  - A push or a merge is issued as one plain command, the form a settings permission rule can match. Permission for it lives in the owner's settings: text the agent writes cannot grant it.
- **Candidate home:** the owner's settings permission rules; github-conventions' `references/commits.md` (#38, merged); subagent-driven-development (how a controller merges a batch).

## PP-29: The controller's own checks ran with no host load budget

- **Severity:** Major. **Status:** open.
- **Observed:**
  - The controller's pre-merge check ran the race tests and golangci-lint across the module with no parallelism cap, beside up to five agents each building at `-p=6`.
  - On 2026-10-21 two of those checks ran at once, and both failed from load, not code: one with 34 packages failing to build, the other with a test that failed at 30 s and passes alone in 21 s. The check printed only the FAIL lines, so both failures read as code failures until they were re-run one at a time.
  - Later that day the load on the owner's 32-core desktop passed 300, then 600. The owner: "wtf, the load average on my desktop spiked to over 300", "load average went over 600", "do not allow that ot happen again".
- **Cost:** two false failures and their re-runs, a merge run stopped partway, four owner messages, and the owner's desktop unusable while it lasted.
- **Improvement:**
  - Every go, make and golangci-lint command, whether an agent's or the controller's, runs through one wrapper. It holds a host-wide lock and caps a run at 4 packages of 4 threads. The project CLAUDE.md states the rule (#447), and every dispatch carries it.
  - Pre-merge checks run one at a time, and the controller holds concurrency at 3 agents.
  - A failure report shows enough of the failing output to tell a load failure from a code failure.
- **Candidate home:** subagent-driven-development (a host budget for the controller's own checks and its agents' builds); the project CLAUDE.md (#447, merged).

## PP-30: Corrections reasoned one step away from the artifact they corrected

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - The controller corrected the BC30-5 registry entry from what the log level settings imply about output: it said the line was gathered but not emitted. A cluster measurement showed the crash happens when the line is formatted, so the correction was withdrawn.
  - The rgw-bug-reproduction session asked for BC-31's registry text to be reverted, working from the controller's summary of it rather than the branch.
- **Cost:** two rework rounds across two sessions.
- **Improvement:**
  - A correction that depends on another artifact opens that artifact first.
  - A correction that depends on what code does with a value opens the code that reads the value.
  - A measurement outranks a mechanism argued from the source.
- **Candidate home:** the controller's rulings brief; the message template for leads sent to another session.

## PP-31: Security leads opened with the impact, which invites a by-design reply

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - Three leads in a row sent to rgw-bug-reproduction opened with their impact sentence: C2-2 (object lock), V5b (sync and object lock) and V9 (a listing that writes). An impact sentence invites the reply that the behaviour is by design.
  - The entries that get a fix merged lead with the narrow mechanical defect, such as an argument in the wrong slot or a key encoded with the wrong instance, and give reachability as the blast radius.
  - Each of the three entries was rewritten after triage.
- **Cost:** a triage round and a registry rewrite per lead.
- **Improvement:** the registry rule in the dispatch environment and the task review brief says to lead with the mechanical defect, then give reachability as the blast radius, and to verify an outside service's semantics by fetching its own documentation.
- **Candidate home:** the dispatch environment's registry rule; the task review brief; code-conventions' `references/bug-reports.md`.

## PP-32: A security property of a registered defect was carried, not worked

- **Severity:** Major. **Status:** open.
- **Observed:**
  - The registry's entry for defect 11, radosgw's MFA one-time-use check, ended "No code outside the window is accepted, so it does not bypass MFA". Phase 2's V plan took that premise as given, and so did the upstream tracker issue.
  - V13b worked the replay against liboath. One code was accepted at three time steps in a row and refused at the fourth, so a captured code can be replayed within its window. V13b's review rewrote the entry as possibly security-relevant, and deleted the sentence.
- **Cost:** a security entry's class corrected after the plan built on it, a fix round of registry text, and a disclosure package started in another session.
- **Improvement:**
  - A plan or registry entry that states a security property of a defect works the attack, or cites a measurement, before it states the property.
  - A plan review checks each such claim the same way.
- **Candidate home:** writing-plans (security claims); the plan review brief; the registry's entry rules.

## PP-33: Entering a worktree isolated every agent in the session

- **Severity:** Major. **Status:** open.
- **Observed:** on 2026-10-21 the controller entered a worktree to write a process docs PR while three agents ran. The harness then refused every agent's Bash calls and writes under `.claude/worktrees/` with "session is isolated in the worktree .../docs-pp-load-budget". One review lost its writes to the ledger and a timing run. The controller left the worktree, and told the other two agents to retry what was refused.
- **Cost:** a review's report was written to a temporary path and copied back, one measurement was lost, and two agents spent tool rounds on refused commands.
- **Improvement:**
  - The controller never enters a worktree while an agent runs.
  - A controller-side commit is built without a checkout, through a temporary index in the main clone (`git read-tree`, `hash-object`, `update-index`, `commit-tree`), then pushed and shipped like any other.
- **Candidate home:** subagent-driven-development (controller-side commits during a run); the global CLAUDE.md's worktree rule.

## PP-34: An upstream guard was cited without checking that it runs

- **Severity:** Major. **Status:** open.
- **Observed:**
  - The controller told rgw-bug-reproduction that CreateBucket's x-amz-object-ownership permission was already checked on ceph main, citing the guard in `RGWCreateBucket::verify_permission`. The session advised leading a security report with that guard as upstream's own pattern.
  - Both had checked that the guard exists, and neither that it runs. Its input is set only in `get_params`, which runs after `verify_permission`, so the guard never refuses anything. L2's re-review caught it.
  - The same day, the same reading error appeared twice more in cross-session triage: a parameter map keyed by name, which overwrites repeats, and a build-flag condition missing from a crash claim.
- **Cost:** a withdrawn framing on two security entries in two sessions, one new registry entry, and a plan citation that names a 403 its release never returns.
- **Improvement:**
  - A claim that radosgw already handles a case holds only once the claim shows when the handler's inputs are set, against the call order.
  - The task review brief now says so.
  - Before a written claim about what code does is sent, the writer asks under what condition it is false.
- **Candidate home:** the task review brief (done); code-conventions' `references/bug-reports.md`; the registry's entry rules.

## PP-35: Host resources stayed held after the work that took them

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - The build wrapper ran `flock FILE command`, which hands the lock descriptor to the command. An rgw-go started with `make rgw-go-up` inherited it and held the host-wide build lock for as long as it ran, so the next build waited 10 minutes.
  - Both rooket clusters were found up from earlier work, with no run using them.
- **Cost:** a stalled agent, and two idle clusters' load on the owner's desktop for an unknown time.
- **Improvement:**
  - The wrapper runs `flock -o`, so a daemon the command leaves running does not hold the lock. Fixed 2026-10-21.
  - A task that brings a cluster up tears it down at its end, or says in its report that it left it up and why.
- **Candidate home:** the controller's build wrapper (done); the rooket skill's teardown rule.

## PP-36: A dispatch asked an agent for writes its role forbids

- **Severity:** Minor. **Status:** open.
- **Observed:** the s3-tests stage-1 dispatch asked a code-worker to push branches and open jhoblitt/ceph PRs. The code-worker role forbids remote writes, so it stopped before them, and the controller pushed and opened the PR itself.
- **Cost:** a hand-back round, and a PR step done late.
- **Improvement:** a dispatch gives an agent only work its role allows. Pushes, PRs and merges stay with the controller, which holds the owner's grant for them.
- **Candidate home:** subagent-driven-development (dispatch shape); the dispatch environment.

## PP-37: The harness refused a subagent's write of its own report

- **Severity:** Minor. **Status:** open.
- **Observed:** on 2026-10-21 the harness refused an implementer's write of its fix-round section to its report file ("subagents must not write report files"), where earlier rounds had written theirs. The implementer did not work around it, and gave the section in its reply.
- **Cost:** the report file lacks one round. The section exists only in the hand-back and its notes file.
- **Improvement:** every dispatch says what to do when a write is refused, which is to put the content in the reply, and the controller files that content where the next reader looks for it.
- **Candidate home:** the dispatch environment's report contract.

## PP-38: A review the plan required was skipped at merge

- **Severity:** Major. **Status:** open.
- **Observed:** the owner's P3 gives V5b a Fable review after its task review, and the task's brief says so. V5b merged as #445 after its task review and the controller's interdiff checks, with no Fable review. The gap was found only when V5c, which shares that review, was dispatched.
- **Cost:** V5b's OLH writes, which change state both gateways share, have been on main since 2026-10-21 without the review the owner asked for. The shared review now covers V5b after the fact, and a finding in it lands on main as a follow-up instead of before the merge.
- **Improvement:** the ship step reads the task's **Review:** line and refuses to merge until every review it names has a report in the unit's workspace.
- **Candidate home:** subagent-driven-development (the per-task loop's exit); the controller's ship scripts.

## PP-39: A seam fixed before its consumers existed hid the state it hands them

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - The plan gave V5c olh_cond.go and V4's init, but its rules have to run inside V5b's setOLH, between the class's link and the replay, so V5c changed olh.go past its brief. Its condition seam also changed shape, to take the resolved current state.
  - The plan routes V7a's and V7b's null-version write conditions through that seam. A null write puts its head before setOLH, so the condition would see the write's own data, and a failed condition could not be undone. V5c's review found it, and setOLH now refuses a condition there.
- **Cost:** two rulings past the brief, an Important finding, and carries into V7a, V7b and V8a.
- **Improvement:**
  - A plan seam says which state its consumer sees, and when, not only its signature.
  - A task whose rule must run inside another task's function lists that function's file.
- **Candidate home:** writing-plans (seams); the plan review's checklist.

## PP-40: GitHub's views lagged a push, and the merge tooling read the lag as a result

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - After the force-push that rebased #445, the PR's head still showed the old commit, so the merge gate failed on a head mismatch.
  - After the rebase of #461, the runs API's `head_sha` filter returned no runs for minutes, so merge_when_green reported "CI: 0 runs" and stopped. A gate run named by the commit passed.
- **Cost:** two re-armed merges and the minutes spent finding why.
- **Improvement:**
  - The ship scripts wait until the PR's head is the pushed commit.
  - They also wait until the runs filter returns that commit's runs before judging CI.
  - An empty answer straight after a push is a lag, not a result.
- **Candidate home:** github-conventions' "Watching CI"; the controller's merge scripts (done).

## PP-41: The controller's sandboxed and unsandboxed commands saw different temporary directories

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - The controller wrote the registry to `$TMPDIR` from a command run outside the sandbox, where TMPDIR is /tmp. It then read `$TMPDIR` from sandboxed commands, where TMPDIR is /tmp/claude-1000, and found an older copy from 2026-10-19 there.
  - The Found-by-us sweep's first count, 48 rows, came from that copy. A later run on the current file found 72.
  - The same day, the controller's commit helper took a relative message path, which `git -C` resolved against the clone, so the commit failed.
- **Cost:** one analysis redone, and one failed commit.
- **Improvement:**
  - Controller scripts name absolute paths under /tmp/claude-1000 whenever their commands mix sandbox modes.
  - Each helper resolves its path arguments before it changes directory. The commit helper does so now.
- **Candidate home:** the global CLAUDE.md's sandbox notes; the controller's helpers (done for the commit helper).

## PP-42: The integration suite failed every night for two weeks, and nothing watched it

- **Severity:** Major. **Status:** open.
- **Observed:**
  - The cluster integration suite ran only on a schedule (`on: schedule, workflow_dispatch` in `integration.yml`), so no pull request ran it, and nothing read the scheduled runs.
  - It last passed on 2026-10-15. From 2026-10-17 it was red every night on both releases, 14 nights running, until the controller found it on 2026-10-30.
  - Two deliberate behaviour changes had left cluster specs asserting the old behaviour. #360 made rgw-go compress a PUT as its storage class says, while a write-path spec still asserted "rgw-go compresses nothing". #441 combined a multipart upload's part checksums into the object's, while the multipart oracle still asserted that the completed head carried none. Neither pull request ran the suite, so both stale assertions merged.
  - Three flakes had accumulated beside them. goceph's sync-mode limiter spec assumed that reads would park, and failed about one job in ten. The phase 0 gate refused the GC-queued part heads that #441's conformance specs left behind, in about a third of Tentacle's spec orders. The admin gate's BeforeAll set a user rate limit before its fixture writes, and failed whenever Go's map order wrote the larger fixture first.
  - `make integration` chained its two `go test` runs with `&&`, so the goceph flake hid the suite's own failures in 4 of the 28 jobs from 2026-10-17.
- **Cost:**
  - Two weeks without the suite's signal, while phase 2 kept merging.
  - A diagnosis task, a fix task with a review and a fix round (#523), and a trigger change with a review, a re-review and two fix rounds (#524), each verified by integration runs on both releases.
  - The owner's attention: "do we even need to keep the nightlys? I would rather we run the integration tests on every PR".
- **Fix:**
  - #523 makes the two stale specs assert what rgw-go now does, fixes the three flaky ones, and has `make integration` run its second `go test` when the first fails.
  - #524 runs the suite, per release, on every pull request that touches the code it covers, behind one `integration` check, and drops the schedule. It is green, and waits on the owner's workflow acknowledgement (PP-47).
  - Until then, and for the workflows that stay scheduled, such as `rook.yml`, the controller checks the latest scheduled runs at each status report.
- **Improvement:**
  - A suite gates the pull requests whose code it covers. One that only runs on a schedule has an owner who reads every run.
  - A pull request that changes behaviour a cluster spec asserts runs that spec before it merges.
  - A target that runs several suites reports every suite's failure, not only the first.
- **Candidate home:** subagent-driven-development (the controller's status checks); github-conventions' "Watching CI" (scheduled runs); writing-plans (a behaviour change names the cluster specs it changes).

## PP-43: The registries' claims about ceph main named no commit, so they aged silently

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - docs/ceph-upstream-bugs.md and docs/exclusions.md say whether a defect is fixed on ceph main, a branch that moves every day. Of about 190 such claims, about 156 named no commit, and the rest named 8 different ones, so nothing showed when a claim had last been true. One named f6f2529dd9e, a commit of jhoblitt/ceph's `rgw-go-s3tests` branch, as main.
  - On 2026-10-30 the owner agreed to re-verify every claim at one main commit, and to have CI refuse a claim that names none.
  - The re-verification at ceph main 8aa24c51928 (#530) found 3 defects the registry called unfixed on main that had been fixed there. Of 102 entries that had never been checked against main at all, 4 were fixed there too. It also found a defect that exists only on main, which became an entry of its own (#529).
- **Cost:**
  - Entries that told readers a defect was open on main after main had fixed it, for an unknown time.
  - The owner's attention, a sweep in three rounds with a review and a re-review, and a check in four rounds with a review and a re-review.
- **Fix:**
  - #530 names 8aa24c51928 at every main claim it checked, and #532 names it at the claims whose commit the sweep's wording had left implicit.
  - #532 makes `make check` and CI fail a main claim that names no commit of at least nine hex digits. With a ceph clone, citecheck also checks that the named commit is on main.
- **Improvement:** a claim about a moving branch says where it was checked, as a citation names its tag, so that re-checking it later is one sweep at one new commit, not a search.
- **Candidate home:** the registries' entry rules; `hack/citecheck` (done, #532).

## PP-44: The citation content check's false positives hid its real errors

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - `make citecheck-content` checks that each Ceph link's lines hold what its text names. It is opt-in: neither `make check` nor CI runs it.
  - On 2026-10-30 it rejected 819 links. About 670 of the rejections were the checker's own false positives, so no one read its output for the real errors.
  - The noise had been noted as a candidate for this registry some days before, and nothing was done about it.
  - About 130 of the rejected links, and a few found beside them, were really wrong: line ranges, paths or names that did not match the code at their tag, in docs/ceph-upstream-bugs.md, the phase 1 plans, two phase 2 research notes and three Go comments.
- **Cost:** wrong citations in the registry and the plans for an unknown time, a correction task with two fix rounds and a review (#528), and a checker task with three fix rounds, a review and a re-review (#531).
- **Fix:**
  - #528 corrects the wrong links.
  - #531 fixes the false-positive shapes the checker can tell apart from real errors. On the tree it was measured against, the failures fell from 694 to 558 and the false positives from 667 to 532. The rest are shapes the review kept failing, since passing them would pass wrong citations too.
- **Open:** the check stays opt-in with about 530 false positives. Running it in CI needs a ceph clone holding every cited ref, about 29, and a workflow change, which needs the owner's acknowledgement.
- **Improvement:**
  - A check whose output is mostly false positives is fixed before its output is relied on, and a check that nothing runs catches nothing.
  - A problem noted as a candidate for this registry gets its entry when it is noted, not at the next process docs PR.
- **Candidate home:** `hack/citecheck` (#531); the CI workflows (the owner's acknowledgement); the plan and review briefs, which rely on citations.

## PP-45: A wait loop matched its own command line and never ended

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - On 2026-10-31 the controller held two ships, each until another `ship_new.sh` run ended, with wait loops of the form `while pgrep -f 'ship_new.sh <sha>'; do sleep ...; done`, run through `bash -c`.
  - The pattern was part of the loop's own `bash -c` command line, so `pgrep -f` always found that shell, and neither loop ended. The rate-limiter registry PR, reviewed and ready, sat unshipped, and the main-claims sweep waited behind it.
- **Cost:** a ready PR left unshipped until the controller noticed, and the PR queued behind it.
- **Fix:** both loops were stopped and replaced with one script that runs the dependent steps in order: ship the rate-limiter PR (#529), rebase the sweep, stop unless the rebase changed no patch line, and ship the sweep (#530).
- **Improvement:**
  - Steps that depend on each other run in order in one script, not as separate processes that poll for each other.
  - A poll never matches a pattern its own command line holds. Where a poll cannot be avoided, it waits on a process id or on a file the awaited step writes.
- **Candidate home:** subagent-driven-development's scripts (the controller's ship helpers).

## PP-46: Scratch in /tmp exhausted the build host's per-user quota

- **Severity:** Minor. **Status:** open.
- **Observed:**
  - The build host's /tmp is a 31 GB tmpfs mounted with `usrquota`. Every agent's and session's scratch under it counts against one user's quota, and sandboxed builds put their work directories there.
  - On 2026-10-31 the pre-merge override check of #529's head failed in `go test -race` with "disk quota exceeded" and "No space left on device". The change itself was correct.
  - /tmp/claude-1000 then held about 20 GB. Among it were a Rust build tree of 6.2 GB, most likely the rgw-rs session's, an agent's V8a scratch tree of 2.5 GB and an S7 re-review tree of 1.3 GB. Scratch that finished agents and other sessions leave behind accumulates, and nothing removes it.
- **Cost:** a correct change failed its check, a failure to diagnose as the host's rather than the code's, and a retry.
- **Fix:** the controller removed about 1.5 GB of finished scratch, and the retried check passed.
- **Open:** scratch another session leaves, such as the Rust build tree, is that session's to remove, and nothing asks it to.
- **Improvement:**
  - A task deletes the scratch it creates under /tmp when it ends, and its dispatch says so.
  - The build wrapper refuses to start when /tmp's quota is nearly spent, as it already does when /home is nearly full.
- **Candidate home:** the dispatch environment; the controller's build wrapper; PP-35 (host resources held after the work that took them).

## PP-47: A general standing instruction could not stand in for a workflow acknowledgement

- **Severity:** Major. **Status:** open: the owner's decision.
- **Observed:**
  - The merge gate refuses a PR that changes `.github/workflows/` or `.github/actions/` until `pr-acks.tsv` holds the owner's `workflows` acknowledgement for it (the owner's rule of 2026-10-12).
  - On 2026-10-30 the owner said "please fix everything you find without stopping to ask for permission". The controller read it as the acknowledgement for the fix PRs that touch workflows. The auto-mode classifier refused the write of that line for #522, the s3-tests path filter's fix for quoted paths, as a CI bypass, and the controller did not route around it.
  - #522 and #524 are green, and both wait as drafts for a yes from the owner on each. #524 waits although the owner had approved its plan ("the plan, steps 1-3 approved"), since the controller expected the same refusal.
- **Cost:** two finished PRs held, one of them PP-42's fix, so the integration suite still runs only on a schedule, and an owner message for every workflow PR.
- **Improvement:** options for the owner to decide:
  - The owner acknowledges each workflow PR by its number, and the controller asks for every pending acknowledgement in one message as the PRs open.
  - The owner writes the `pr-acks.tsv` line, or grants the write in the settings permission rules. As in PP-28, text the agent writes cannot grant it.
- **Candidate home:** the owner; the merge gate's acknowledgement check; subagent-driven-development (how a controller asks for acknowledgements).
