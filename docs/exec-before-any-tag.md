# Before any tag that ships `exec.v2`

[Docs home](README.md) · [Exec: run commands in a target](exec.md) · [Exec v2: hand-over status](exec-status.md)

This page is the gate that must be cleared before **any** tag ships the exec v2 lane: the wire (`proto/exec/v2/exec.proto`), the Go author API (`pkg/v1/exec`) and its test kit (`pkg/v1/exec/exectest`). It is written to be read alone. Each item says what it is, why it costs nothing to fix now and is permanent after a tag, the evidence in the code, the options for the fix and the one recommended, and a check that shows it closed.

**State at `b9cedd6fc5aecbaf4fe6aab8f4a329f026dbff66` (`feature/exec-v2` after PR #92): the tag gate is NOT cleared.**

**What blocks a tag:** C3 (process), B1.4, B1.5, B1.6, B1.7, B1.10, B2, T1. **Conditional:** B1.3. **To decide:** S2 and S11.

**Why a gate.** Nothing of the lane is released. Until a release carries it, a name, a field or a rule changes for the price of a commit. After a release a plugin in the field emits its frames and an author's code compiles against its names, so a wrong shape, a value nobody can deliver, or a sentence of the guide that the code does not keep, stays. An item **blocks** when it is free to fix now and permanent after, or when the guide or the proto promises something the code does not do. Tags are the lead's to cut, and the lead clears the gate.

## Where the ids come from

- **C3, B1.1 to B1.12, B2 to B7** come from the independent review of the contract (PR #87, reviewed head `ed6f26f8445c2e756de5506882c3927e9f228b47`). Its two other conditions are closed: C1 (the text says "exactly one end, at most one `Ended` frame") and C2 (how a session reports a mode: B1.1).
- **T1, S1 to S12 and N1 to N7** come from the independent review of PRs #91 and #92. The reviewed head of #92, `3ac2568efbc5930fd00e76578af47ee9858021ec`, has the same tree as the merge commit (tree `3db0f4240058a122bfcb2d179c258ed15c5effe7`), so every `file:line` below is at `b9cedd6`. That review also re-checked every item of the first one.
- "Observed" lines say what a run showed, with its numbers. The runs of the review of PRs #91 and #92 were all made in containers limited to two CPUs ([How to verify](#how-to-verify-the-gate)). A statement marked **[INFERENCE]** was not observed.
- **gate PR** below means a PR to `feature/exec-v2` made for the item; none exists yet.

## The gate at a glance

**Blocks a tag?** `YES`: do not tag until it is closed. `conditional`: a tag may ship if the stated condition holds. `decide`: the lead decides, and either answer closes it once the answer is written and the code, the proto and the guide follow it. `no`: it does not block (already closed, or a SHOULD that is cheap now). **Status:** `CLOSED` includes "decided and the code follows it".

| Id | Item | Blocks a tag? | Status | Fixed by |
|---|---|---|---|---|
| [C3](#c3-process-what-a-stable-tag-freezes) | process: a stable tag freezes `exec.v2`; `proto-breaking` has no `exec.v2` baseline | YES | OPEN | the release process (the lead); no code |
| B1.1 | narrowing: the mode of a session is in `Opened`; `Started` before the first output | no | CLOSED | #87, #90 |
| B1.2 | end invariants: one end, at most one `Ended`, one last frame per stream | no | CLOSED | #87, #90, #92 (the guide sentence of S2 is open) |
| [B1.3](#b13-a-stream-that-ended-with-no-status-conditional) | a stream that ended with no status: `Exited{0, TRANSPORT}` | conditional | OPEN (decided) | #92 decided it; gate PR with S12; B1.6 |
| [B1.4](#b14-values-nobody-can-deliver) | values nobody can deliver: `OUTCOME_CANCELLED`, `CLOSED_BY_IDLE_TIMEOUT`; the typed end at shutdown (S1) | YES | OPEN | gate PR |
| [B1.5](#b15-redundant-fields-that-can-disagree) | redundant fields that can disagree | YES | OPEN | gate PR (S5, S9) |
| [B1.6](#b16-consumer-readers) | consumer readers: an absent fact is unknown | YES | OPEN | H1a-1 (host, proven on a `wip/` branch) or an SDK reader PR |
| [B1.7](#b17-openrequestkind-as-two-plain-fields) | `OpenRequest.kind` as two plain fields (wire-identical) | YES | OPEN | gate PR |
| B1.8 | refusal encoding: one `ErrorInfo` encoding | no | CLOSED | #87, #90, #91, #92 |
| [B1.9](#open-and-not-blocking) | `Session`/`Run` declaration | no | OPEN (additive) | a later PR |
| [B1.10](#b110-ownership-and-sizes) | ownership and sizes: no route on the session calls, `ListSessions` unbounded, `ReadOutput.ended` | YES | OPEN | gate PR (S4, S6) |
| [B1.11](#open-and-not-blocking) | `Exit` default: `SourceUnspecified` beside a code reads PROCESS | decide | OPEN | the lead, then a gate PR |
| [B1.12](#open-and-not-blocking) | vocabularies and the string guard | no | OPEN (additive) | K1a; a later PR |
| [B2](#b2-names-that-still-collide) | names that still collide | YES | OPEN | gate PR |
| [B3](#open-and-not-blocking) | exports that should not be public | no (not worse) | OPEN | decide when H1a-1 writes its first test |
| [B4](#open-and-not-blocking) | the 11 deferred SHOULDs of the contract review | no | OPEN for SHOULD 6 only | each row is another item; SHOULD 6 is additive |
| B5 | the three review threads | no | CLOSED | they follow B1.3 and B1.7 |
| B6 | a write that arrives without `Started` | no | CLOSED | #87, #90 |
| B7 | Kubernetes proof, golden, pinned limits | no | CLOSED | the spike; #87; #92 |
| [T1](#t1-per-stream-accounts-of-run-and-runstream-on-a-lossy-target) | `Run`/`RunStream` per-stream `total_bytes`/`dropped_bytes` wrong for a lossy target that writes both streams | YES | OPEN | gate PR |
| [S1](#s1-the-typed-end-at-shutdown-is-lost-by-about-7-of-reading-streams) | the typed `Ended{PLUGIN_SHUTDOWN}` is lost by about 7% of reading streams on `Client.Kill()` | YES, through B1.4 | OPEN | gate PR |
| [S2](#s2-a-cancelled-attach-can-still-supersede-the-hosts-live-attach) | a cancelled attach can still supersede the host's live attach | decide | OPEN | the lead, then a gate PR |
| [S3](#s3-a-cancelled-open-leaves-a-live-session-behind) | a cancelled `Open` leaves a live session behind | no | OPEN | gate PR |
| [S4](#s4-readoutput-says-endedtrue-on-the-first-partial-page-of-a-tombstone) | `ReadOutput` says `ended=true` on a partial page of a tombstone | YES, through B1.10 | OPEN | gate PR |
| [S5](#s5-endeddropped_bytes-and-outputcomplete-ignore-bytes-lost-to-an-unread-replay) | `Ended.dropped_bytes` and `COMPLETE` ignore bytes lost to a replay the host never read | YES, through B1.5 | OPEN | gate PR |
| [S6](#s6-listsessions-has-no-size-bound-and-fails-limit-for-everyone) | `ListSessions` has no size bound and fails `LIMIT` for everyone | YES, through B1.10 | OPEN | gate PR |
| [S7](#s7-remote_may_survivefalse-after-the-sdk-saw-the-function-not-return) | `remote_may_survive=false` after the SDK saw the function not return | no | OPEN | gate PR |
| [S8](#s8-a-run-is-not-bounded-by-its-timeout_ms-during-the-start) | a `Run` is not bounded by its `timeout_ms` during the start | no | OPEN | gate PR |
| [S9](#s9-failure_stage_run-with-start_observedfalse) | `FAILURE_STAGE_RUN` with `start_observed=false` | YES, through B1.5 | OPEN | gate PR |
| [S10](#s10-a-query-pipelined-behind-a-close-is-not-always-answered) | a query pipelined behind a `Close` is not always answered | no | OPEN | gate PR |
| [S11](#s11-the-keepalive-policy-cannot-be-discovered) | the keepalive policy cannot be discovered | decide | OPEN | the lead, then a gate PR |
| [S12](#s12-the-exited-comment-contradicts-the-transport-paragraph) | the `Exited` comment contradicts the TRANSPORT paragraph; a `Run` carries the transport's zero as a code | conditional, through B1.3 | OPEN | gate PR |
| [N1 to N7](#notes-n1-to-n7) | notes | no | open | see each |
| [Pins](#pins-and-killer-tests) | five behaviours that no test pins (mutants T05, Z03, S12, N05, N09) | no (tests) | OPEN | tests |

The S items are each a promise of the guide or of the proto that the code does not keep, or a cost that is cheap now. For every one, change the code or change the sentence: both are free before a tag.

## Blocking and conditional items

### C3. Process: what a stable tag freezes

**What.** `exec.v2` is a new gRPC service (`proto/exec/v2/exec.proto`, package `omniview.sdk.exec.v2`) and a Go author API. The CI job `proto-breaking` runs `buf breaking` against the latest *stable* tag (`vX.Y.Z`, never a pre-release). The latest stable tag is v0.22.1, which carries the legacy `proto/exec.proto` and no `proto/exec/v2`, so the job guards nothing of `exec.v2` until a stable tag carries the file. The only guard of the wire until then is the reviewed golden, `proto/exec/v2/exec.contract.golden`, compared with the compiled descriptor by `TestContractMatchesItsReviewedGolden` (`proto/exec/v2/contract_test.go:314`); `-update` regenerates it.

**Why it is free now and permanent after.** Any stable tag cut from a branch that contains `exec.v2` ships it for good, and a bug-fix tag would do it. After it, plugins in the field emit its frames, `proto-breaking` enforces the shape against that tag, and the Go names are a semver promise. A pre-release tag (`-rc.N`) is invisible to `@latest` and to that job's baseline, so it freezes nothing.

**Evidence at `b9cedd6`.**
- `.github/workflows/native-exec.yml:96-106`: the baseline is `git tag --list 'v*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n 1`; with no stable tag the job skips.
- `git ls-tree -r v0.22.1 --name-only | grep proto/exec` prints `proto/exec.pb.go`, `proto/exec.proto` and `proto/exec_grpc.pb.go`: no `proto/exec/v2`.
- `git ls-tree -r origin/main --name-only | grep -c proto/exec/v2` prints `0`: `main` has no `exec.v2`. The lead's rule of 2026-11-22 is that the lane lands on `feature/exec-v2` and not on `main` until it is tag-ready, and that `main` stays releasable for small fixes.
- `buf breaking --against ".git#tag=v0.22.1"` is clean on this tree (the proto delta of #91 and #92 is two additive lines: `ERROR_REASON_TARGET_WEDGED = 15` and `Choice.no_program = 8`).

**Options.** (a) Keep the rule: no stable tag from a branch that contains `exec.v2` until the gate is cleared; `main` is tagged for its own fixes (v0.22.x). (b) Tag a pre-release from `feature/exec-v2` when a consumer must pin something, and say in the release notes that `proto/exec/v2` is unfrozen until the first stable tag. (c) After the first stable tag that carries the file, `proto-breaking` guards `exec.v2` with no change to the workflow.

**Recommended.** (a), then (c). Use (b) only for a consumer that cannot wait; the host merges only against a released SDK tag, so a host change that needs the lane is proven on a `wip/` branch first.

**Verify.** C3 is closed by the process holding when the first tag is cut, not by a code change.
1. Before cutting a tag, `git ls-tree -r <commit-to-tag> --name-only | grep -c proto/exec/v2` is `0` for every tag cut from `main` while the gate is open, and is non-zero only for the first stable tag cut from `feature/exec-v2` after every other item here is closed.
2. After that tag, `git tag --list 'v*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n 1` is that tag. In a scratch copy, change a field number in `proto/exec/v2/exec.proto`: `buf breaking --against ".git#tag=<tag>"` must now fail (and `TestContractMatchesItsReviewedGolden` already fails). Restore the file.

### B1.3. A stream that ended with no status (conditional)

**What.** client-go's executor returns `nil` for a Success status and for a status channel that closed empty, and the plugin cannot tell them apart. After a kubelet or containerd restart the API server sends a Success for a command that did not exit 0 (8 of 8 sessions through a frame-aware proxy: 4 kubelet restarts and 4 containerd restarts; a provider that read the status channel itself returned `Exited{0, PROCESS}` with `remote_may_survive=false` for 4 of 4 that ran). An empty channel with a normal close never occurred in 228 sessions. So a Success means "the stream ended normally", never "the command exited 0". The decision, written in the proto and the guide: `Exited{0, TRANSPORT}` is the arm for that Success, with the start observed and `remote_may_survive` absent; a non-zero status is `PROCESS`; an empty channel or a break is `Lost`. `exec.ExitOfStreamError` (`pkg/v1/exec/exit_stream.go:42-61`) maps `nil` to `ExitTransport(0)`, an error with `ExitStatus()` or `ExitCode()` to `ExitCode(n)`, and anything else to the author; `exectest.CodeExitError` is the test double.

**What is left.** The review would not tag "a code on a no-status end" as it stands: on a `Run` it is `outcome=EXITED exit_code=0 exit_source=TRANSPORT`, and a host that prints the code and drops the source says "exit 0" for a command that a kubelet restart killed. It is acceptable only if the reader ships with it (B1.6), or if the code is left absent for TRANSPORT, which is free for `Run` because `RunResult.exit_code` is already `optional` (`proto/exec/v2/exec.proto:1515`).

**Why it is free now.** Leaving a field absent and fixing a comment change no deployed sender. After a tag a consumer prints the zero.

**Evidence.**
- `pkg/v1/exec/run.go:78-81`: `case *execv2.End_Exited` copies `Exited.exit_code` into `RunResult.exit_code` whatever the source (`new(kind.Exited.GetExitCode())`).
- `proto/exec/v2/exec.proto:1235-1238` (the `Exited` comment) says "exit_code is not optional because the arm itself says it is known", against the paragraph above it and `EXIT_SOURCE_TRANSPORT` (`:1193-1197`: "a code from this source, 0 included, is never the command's"): this is S12.
- `pkg/v1/exec/exit.go:185-201` and `:207-214`: a code from a target that declares `ExitReportNone` becomes TRANSPORT with a violation, and the survival of TRANSPORT is absent.

**Options.** (1) Leave `RunResult.exit_code` absent when the source is TRANSPORT (one branch at `run.go:80`), fix the `Exited` comment, and ship the reader (B1.6) so a session's `Exited{0, TRANSPORT}` reads "ended normally". (2) Replace the arm with a no-code arm (a new `ClosedBy` value or an `End` kind of its own), so no consumer can read a code at all: the wire change is the permanent part.
**Recommended.** (1): it needs no new arm, and the proof of the Pod provider found the arm sufficient once the readers say "ended normally".

**Verify.**
1. A `Run` whose function returns `ExitOfStreamError(nil)` answers `outcome=EXITED`, `exit_source=TRANSPORT`, `exit_code` absent (the test `TestARunWhoseStreamEndedNormallyIsExitedZeroFromTheTransportOverTheWire` is the one to change), `start_observed=true`, `remote_may_survive` absent.
2. A session with the same end sends `Ended{exited{exit_code: 0, source: TRANSPORT}}` with `start_observed=true` and `remote_may_survive` absent, output `UNSPECIFIED` (`TestAQuietCommandThatEndedNormallyIsExitedZeroFromTheTransportOverTheWire`; conformance cases 16 and 17).
3. The reader test of B1.6 reads that end as "ended normally", never "exit 0".
4. `proto/exec/v2/exec.proto` no longer says that the `Exited` code "is known" for every source; `buf generate` leaves no diff and `buf breaking --against ".git#tag=v0.22.1"` is clean.

### B1.4. Values nobody can deliver

**What.** A wire value that no producer can deliver forces every consumer to handle a case that never arrives. At `b9cedd6`:
- `OUTCOME_CANCELLED` (`proto/exec/v2/exec.proto:1459`, "The call was cancelled"). The engine maps a run closed by `CLOSED_BY_CANCELLED` or `CLOSED_BY_REQUEST` to it (`pkg/v1/exec/run.go:107-108`), but a host that cancels its call gets the transport's own status and never a result; no call cancels a run by id; `EndSession`, `ReadOutput`, `Open{attach}` and `ListSessions` do not reach a run (its id answers `NOT_FOUND`/`SESSION_UNKNOWN`). The guide's row "was cancelled (its call, a close)" (`docs/exec.md:284`) never happens.
- `CLOSED_BY_IDLE_TIMEOUT` (`proto/exec/v2/exec.proto:1160`, "idle for longer than its bound"). No message has a field for the bound (`SessionLimits` has no idle timeout) and nothing produces the value: outside the enum maps it appears only in `pkg/v1/exec/run.go:115` and the suite's table `pkg/v1/exec/exectest/conformance_run.go:348`. The guide says so (`docs/exec.md:617`).
- `CLOSED_BY_PLUGIN_SHUTDOWN` is produced and mostly delivered; S1 is the part that is not.
- Produced and deliverable: `CLOSED_BY_DETACH_TIMEOUT` (a lease that ran out: no `Ended` frame, the end is the tombstone's), `CLOSED_BY_RUN_TIMEOUT` (a run that reaches its timeout: the host reads `TIMED_OUT`), `CLOSED_BY_CANCELLED` (a session destroyed because its host broke the protocol or never learned that it exists: the end is the tombstone's).

**Why it is free now.** Deleting an enum value, or giving it a producer, costs a commit before a tag. After a tag deleting it is a break and keeping it is a case every host handles for ever.

**Options.** (a) Delete the two values (reserve their numbers); update the proto comments, the golden, the generated code, `closedRun` (`pkg/v1/exec/run.go:103-122`), the guide row and the suite's expectations (`exectest/conformance_run.go:341` and `:348`, `pkg/v1/exec/run_client_test.go:370`, `pkg/v1/exec/run_result_test.go:22`, `exectest/conformance_run_test.go:256-258`). (b) Define a producer for each: for `OUTCOME_CANCELLED` a way to cancel a run whose caller still reads its result (no such call exists), for `CLOSED_BY_IDLE_TIMEOUT` an idle bound that a limit can set; both are new surface with their own proofs.
**Recommended.** (a). A cancel is a status by design, and the guide already says that nothing produces the idle value. S1 is part of this item: fix it or say it.

**Verify.** `grep -rn 'OUTCOME_CANCELLED\|CLOSED_BY_IDLE_TIMEOUT' proto docs pkg --include=*.proto --include=*.md --include=*.go` shows only generated code that `buf generate` produces (or, for option (b), each value has a producer and a test that receives it from a real gRPC server); `go test ./proto/exec/v2 -run TestContractMatchesItsReviewedGolden -update` regenerates a golden whose diff is those two lines and nothing else; `buf generate` leaves no diff; and the check of S1 passes.

### B1.5. Redundant fields that can disagree

**What.** Two pairs of fields say one fact twice, so they can disagree.
- `start_observed` (`Ended.start_observed`, `proto/exec/v2/exec.proto:1321`; `RunResult.start_observed`, `:1523`) and `Failed.stage` (`:1268`): `FAILURE_STAGE_START` means "the start was not observed" (`:1174-1176`), so `START` iff `start_observed` is false.
- `OutputAccount.truncated` (`proto/exec/v2/exec.proto:1484`) against `dropped_bytes` (`:1483`) and `completeness` (`:1488`): `truncated` iff `dropped_bytes` is not zero, and `TRUNCATED` iff a byte was dropped (`:1475-1479`).

The SDK writes each pair from one predicate (`failureStage` at `pkg/v1/exec/classify.go:122-130` for the stage of a panic or an error; `truncated` as `dropped_bytes != 0`), and it makes the two exceptions itself: S9 (a broken `Exit` is stage `RUN` with `start_observed=false`: `pkg/v1/exec/exit.go:231-236`) and S5 (`Ended.output=COMPLETE`, `dropped_bytes=0`, beside a gap the host is told in `replay.start`). Nothing in this repository rejects a disagreement from another producer.

**Why it is free now.** Dropping a field, or fixing a producer, changes no deployed sender. After a tag every consumer must choose which of two fields to believe.

**Options.** (1) Drop one of each pair: `truncated` adds nothing to `dropped_bytes`; for the other pair `start_observed` also describes ends that are not failures (a `Lost` end), so the field to reduce is `stage`, to say only what `start_observed` cannot (RUN or STREAM) [INFERENCE: this reading of the pair is this page's, not the review's]. (2) Keep both; make the SDK never produce a disagreement (fix S5 and S9: no wire change); and put the rule in the reader (B1.6), which treats a disagreement as a bug of the producer.
**Recommended.** (2) now, with a property test over every end the SDK can make; take (1) for `truncated` as well if the lead wants the wire to carry each fact once, because that is free only before a tag.

**Verify.** One property test, over every end the SDK can make (session `Ended` and `RunResult`; exited, signaled, each `ClosedBy`, failed at each stage, a panic, an error, each broken `Exit` shape, lost; a blocking and a lossy target; before and after the start; a replay that was never read): for every `Failed`, `stage == START` iff `start_observed` is false; `truncated == (dropped_bytes != 0)`; `completeness == TRUNCATED` iff `dropped_bytes != 0` (a session's `dropped_bytes` counts the bytes lost to an unread replay); `COMPLETE` never beside a gap. A mutant that hard-codes `FAILURE_STAGE_RUN` in `failedInternal` (`pkg/v1/exec/exit.go:231`) makes it fail. If a field is dropped, the golden diff is reviewed and `buf generate` leaves no diff.

### B1.6. Consumer readers

**What.** What the host gets from the SDK is raw wire messages: `Client.Describe`, `Conn.Recv` (a `ServerFrame`) and `RunResult` (`pkg/v1/exec/client.go:38-100`; its comment says "the readers of the contract's optional facts live with the first consumer"). The contract's one rule, [Absence is unknown](exec.md#absence-is-unknown), needs a reader: an absent fact reads "not known"; an absent `remote_may_survive` reads "may", never "gone"; an unknown enum number reads `UNSPECIFIED`; an `End` with no arm reads "ended, how unknown", never an exit. The generated getters do the opposite for the most dangerous field: `End.GetRemoteMaySurvive()` returns `false` for an absent field (`proto/exec/v2/exec.pb.go:6196-6201`), so a host that writes `if !end.GetRemoteMaySurvive()` reads "unknown" as "gone". The Go mirror that once normalized this was deleted with the unused host-side half of the package in #87 (609 exports became 236). The pair rules of B1.2 and B1.5 (a `Lost` end with `remote_may_survive=false`; `COMPLETE` beside dropped bytes) are representable on the wire and are only text today; the reader is where they are rejected. The SDK holds the rule and its checks: `TestAbsenceIsUnknown` (`proto/exec/v2/contract_test.go:744`), `TestBlocker1FailureAfterHandOffIsAResult` to `TestBlocker5SpecificFieldsCarryPresence`, and conformance case 28, which compares the presence of `remote_may_survive` and not only its value.

**Why it is free now.** Readers are Go code, not wire. But the first host that hardens on raw getters bakes the wrong reading in, and every export added for them is a Go promise after a tag.

**Options.** (a) Write the readers in the first consumer: H1a-1 reads `Describe` and `Options` first, `Ended` and `RunResult` with H1b and H-run. (b) Write them in the SDK (`pkg/v1/exec`), next to `Client`, shared by every Go host. (b) adds exports that no consumer has exercised, which is why the SDK deleted its host-side half; (a) leaves each host to write them and the SDK to hold only the rule and conformance case 28.
**Recommended.** (a), on a host `wip/` branch if it cannot merge before the tag (the host merges only against a released SDK tag), and move the code into the SDK only when a second consumer needs it.

**Verify.** The readers exist and a test of them does each of these: an `End` with `remote_may_survive` absent reads "may"; a `Lost` with `remote_may_survive=false` reads "may" or is rejected as a bug of the producer; an unknown `ExitSource`, `ClosedBy` or `OutputCompleteness` number (99) reads `UNSPECIFIED`; an `End` with no arm reads "ended, how unknown"; `Exited{0, TRANSPORT}` reads "ended normally" (B1.3); a `Failed` with stage `START` beside `start_observed=true`, and `COMPLETE` beside `dropped_bytes > 0`, are rejected (B1.5). A mutant that returns `false` for an absent `remote_may_survive` is killed.

### B1.7. `OpenRequest.kind` as two plain fields

**What.** `message OpenRequest` has `oneof kind { Start start = 2; Attach attach = 3; }` (`proto/exec/v2/exec.proto:665-671`). A frame that carries both arms decodes, by protobuf's rule, as the later arm alone. The strict admission walks the decoded message (`rejectUnknownFields`, `pkg/v1/exec/admission_request_v2.go:320`), so it cannot see an unknown field inside the discarded arm, and "both present" cannot be refused: `admitOpenRequest` (`:316-352`) refuses "neither" (`:328`) and can never see "both". Attach is served now, so the `oneof` is live on both arms.

**Why it is free now.** Two plain message fields with the same numbers and types are wire-identical: nothing deployed changes. The generated Go API changes (the `OpenRequest_Start` and `OpenRequest_Attach` wrappers go): 61 references in 28 files of this repository, 6 of them in non-test files (all in `exectest`), and every host that builds an `OpenRequest` by then. So it is free now and a Go source break later. `buf breaking` under its FILE rules also reports a field that leaves a `oneof` once a stable tag carries the file [INFERENCE: not run].

**Options.** (a) Two plain message fields; admission refuses a frame that has both with `INVALID_REQUEST`. (b) Keep the `oneof`, and say in the proto and the guide that a frame with both arms is read as the later one.
**Recommended.** (a), before any tag.

**Verify.** The golden diff is exactly two lines (`2 start message Start` and `3 attach message Attach` lose `oneof kind`); `buf generate` leaves no diff; `go build ./...` and `go vet ./...` pass after the 61 references are migrated (`grep -rn 'OpenRequest_Start\|OpenRequest_Attach' --include=*.go .` finds none outside generated code); an admission test sends a frame with both `start` and `attach` and expects `INVALID_REQUEST` with nothing run; and a recorded frame of a start and of an attach encodes to the same bytes before and after the change.

### B1.10. Ownership and sizes

**What.** Three decisions of the contract review (its SHOULDs 14, 15 and 16).
1. *Who may attach to, read or end a session.* A session is named by its id alone. `ListSessions`, `ReadOutput` and `EndSession` take no connection and no route (`pkg/v1/exec/session_server.go:36`, `:50` and `:68` read only the incoming session context, the catalog pin), and neither does an attach; the guide says so (`docs/exec.md:250`). Any caller reads, attaches to or ends any session by id, whichever connection started it. A session does know its connection: starting one is checked against the connection lease (`pkg/v1/exec/engine.go:618`), a retirement of the connection ends its sessions, and the engine reads that cause when it concludes the command (`:921`). Open.
2. *One bound policy*, decided: a call that starts something refuses a value above its bound (`Start.detach_timeout_ms`, `Start.replay_bytes`, a size, the bounds of a run); a frame or a call that asks how much of what exists is clamped, and says what it applied (`Detach.lease_ms`, `ReadOutput.max_bytes`). Tests: `TestAReplayRingAboveTheTargetsMaximumIsRefusedAndNothingRan`, `TestTheLeaseOfADetachIsWhatTheHostAskedForTheSessionsOwnOrTheMaximumAndTheSessionEndsExactlyThen`, `TestReadOutputIsClampedToTheRingAndStartsAtItsFront`.
3. *Sizes.* An `Output` frame is at most 64 KiB (`pkg/v1/exec/open_server.go:52`); a ring chunk is 64 KiB, smaller for a ring under 256 KiB; a `ReadOutput` answers at most 1 MiB in at most 1024 chunks, and 64 KiB when `max_bytes` is zero (`pkg/v1/exec/session_server.go:20-28`; `TestReadOutputIsACursorReadOfTheRingAndHoldsToItsBounds`); the tombstones hold at most 256 sessions and 32 MiB of output (`pkg/v1/exec/tombstone.go:29-32`). A `ListSessions` answer has no bound of its own (S6), and `ReadOutput.ended` contradicts its proto (S4). Open.

**Why it is free now.** A route field on the session calls is additive today. After a tag any ownership rule changes what hosts may rely on, and a listing that fails `LIMIT` for everyone breaks the host's own recovery call.

**Options.** Ownership: (a) bind a session to the connection and route that started it, as `Options` and starting already are (`pkg/v1/exec/plugin_server_v2.go:123`): add the route to `ListSessions` (it lists only the caller's), `ReadOutput`, `EndSession` and attach; a call naming another connection answers `NOT_FOUND`/`SESSION_UNKNOWN`. (b) Keep "id alone" as the rule: write it in the proto comments of the four requests and in the guide, and leave window ownership to the host, which owns the trusted window identity (the SDK's `labels` are diagnostic and never authority). Listing and `ended`: S6 and S4.
**Recommended.** (b) now, written, unless the lead wants the plugin to separate connections; (a) separates connections only, not windows, so a host that serves several windows must enforce them itself either way (this is this page's judgment).

**Verify.**
1. If (b): the proto comments of `ListSessionsRequest`, `ReadOutputRequest`, `EndSessionRequest` and `Attach`, and `docs/exec.md:250`, say who may call and what the plugin does not check, and a test pins the guide sentence (the pattern of `examples/exec-plugin/guide_test.go`). If (a): a test with two connections where A starts a session and calls that name B's route (`ReadOutput`, `EndSession`, attach) answer `NOT_FOUND`/`SESSION_UNKNOWN` and change nothing, `ListSessions` for B lists none of A's, and A's own calls are unchanged.
2. S6's and S4's checks pass.

### B2. Names that still collide

**What.** One word with two shapes, and constants whose prefix is not their type. These are the **author API, live now**: authors write `exec.SourceTransport` and `exec.LostNoStatus`. At `b9cedd6`:
- Prefixes: `Source*` / `ExitSource` (`pkg/v1/exec/enums_v2.go:329-333`), `Lost*` / `LostCause` (`:353-355`), `Link*` / `LinkState` (`:373-378`), and `IntegrationOff` against the wire `NONE` (`:195`). (`Class*` and `Reason*` became `ErrorClass*` and `ErrorReason*` in #90.)
- Types: `SessionFacts` (the author's has 7 fields, `pkg/v1/exec/proc.go:130-156`; the wire's has 12, `proto/exec/v2/exec.proto:887-900`), `Capabilities` (`pkg/v1/exec/target.go:83` is a plain declaration; the wire's, `proto/exec/v2/exec.proto:405`, has optional facts), `TargetRef` (`pkg/v1/exec/client.go:13`; the wire's, `proto/exec/v2/exec.proto:139`), `Request` (`pkg/v1/exec/proc.go:104`) beside the wire's `RunRequest` and `OpenRequest`, and `Client` (`pkg/v1/exec/client.go:38`) beside the legacy `Provider` (`pkg/v1/exec/plugin.go:23`) and `PluginClient` (`pkg/v1/exec/plugin_client.go:26`).
- New in #92, three more doubles: `exec.Fact` (`pkg/v1/exec/facts_query.go:107`), `exec.Query` (`pkg/v1/exec/facts_query.go:121`) and `exec.Foreground` (`pkg/v1/exec/facts_query.go:96`), beside the wire's `Query` (`proto/exec/v2/exec.proto:742`), `Foreground` (`proto/exec/v2/exec.proto:1076`) and `Fact` (`proto/exec/v2/exec.proto:1091`) with other shapes. The enums added with them (`FactKind`, `ForegroundState`, `UnknownReason`) carry their type as prefix.
- Wire names: `FEATURE_SESSION` (`proto/exec/v2/exec.proto:279`) beside `FEATURE_SESSION_MANAGEMENT` (`:265`); `Close`, `EndSession`, `End`, `Ended` and `Closed`; one lease with three names (`Detach.lease_ms` `:725`, `Start.detach_timeout_ms` `:637`, `SessionLimits.detach_timeout_ms` `:464`, all in the same file); `last_offset`, which is exclusive. `Started` (the frame), `Proc.Started`, `start_observed` and `FAILURE_STAGE_START` are one story in four words, acceptable if the guide tells it once ([Names: one word per thing](exec.md#names-one-word-per-thing)).

**Why it is free now.** A rename is a mechanical commit today. After a tag it breaks every author and every host. The review accepts the deferral of names "of things no engine produces yet" but not of these, which are live.

**Cost today** (`grep -rnE '\b(SourceUnspecified|SourceProcess|SourceSignalFolded|SourceTransport|SourceHost)\b' --include=*.go pkg examples internal`, and the same for the other families): `Source*` 40 references outside tests and 24 in tests, in 8 files; `Lost*` 34 and 35, in 24 files; `Link*` 15 and 13, in 9 files; `IntegrationOff` 7 and 17, in 9 files.

**Options.** (a) Rename the three prefix families and `IntegrationOff`; rename the author's `SessionFacts`; drop or rename the Go `TargetRef`; rename `exec.Request`; pick one word for "what the host calls" (`Client`, `Provider`, `PluginClient`); give the three new doubles different names; settle the wire names (the lease's name, `FEATURE_SESSION` against `FEATURE_SESSION_MANAGEMENT`) in the same regeneration as the other wire edits. (b) Rename only the prefixes and tell the doubles once in the guide.
**Recommended.** (a), with the new names agreed with the lead first because they are the author API, in one commit, with the guide's names table, `examples/exec-plugin` and the golden updated together.

**Verify.** `go build ./...` and `go vet ./...` pass on linux, windows and darwin (the CI jobs `Exec contract` and `SDK suite`); `grep -rnE '\b(SourceProcess|SourceTransport|LostNoStatus|LinkGood|IntegrationOff)\b' --include=*.go .` finds nothing outside generated code; the table of [Names: one word per thing](exec.md#names-one-word-per-thing) matches the code; for wire names, the golden diff is reviewed and `buf generate` leaves no diff.

### T1. Per-stream accounts of `Run` and `RunStream` on a lossy target

**What.** `OutputAccount.total_bytes` is "bytes the command wrote that arrived", per stream (`proto/exec/v2/exec.proto:1471`), and the guide says the accounts are exact (`docs/exec.md:265` and `:273`). For a target that declares `StallLossy` and writes both stdout and stderr, each stream's `total_bytes` and `dropped_bytes` are wrong; their sum is exact. The `Dropped` ranges that a lossy output queue remembers for its reader are merged into the last one "whatever its stream" once 64 are pending (`maxPendingDrops`, `pkg/v1/exec/pipes.go:39`; `outputPipe.recordDrop`, `:344-350`, whose comment says that only the stream label of the merged runs is affected). `runReader.take` (`pkg/v1/exec/run_server.go:412-421`) then credits each lost range to the stream it is labelled with (`r.sink(d.Stream).lose(d.Bytes)`, `:420`), so for a run the label is the account: bytes of one stream are subtracted from it and added to the other. A run uses this queue (`pkg/v1/exec/engine.go:755-757`); a session uses the replay ring, which accounts for each attachment on its own. Kubernetes is the declared lossy target, and a lossy target is the lane an agent's `run` uses.

**Observed.** A lossy target that writes 256 MiB (268,435,456 bytes) to each of stdout and stderr in alternating writes, caps of 64 KiB, 3 runs of 3 affected every time:

| Write size | stdout `total_bytes` | stderr `total_bytes` |
|---|---|---|
| 256 B | 294,398,208 | 242,472,704 |
| 4 KiB | 258,834,432 | 278,036,480 |
| 32 KiB | 252,805,120 | 284,065,792 |
| 256 KiB | 238,288,896 | 298,582,016 |

Each stream is off by tens of MB, in opposite directions. With 3,000,000 one-byte writes to each stream: `stdout total=3034568 stderr total=2965432` in one run and `stdout total=2961949 stderr total=3038051` in a re-run; the sum is exact each time. `RunStream` is the same (2,000,000 bytes written to each: 1,966,606 and 2,033,394; kept equals delivered). Over 504 lossy runs, 214 of 1,008 stream accounts were off, at every cap from 1 to 262,144. Blocking targets are not affected: 17,260 + 42,600 + 936 runs, 0 off.

**Why it is free now.** The fix is internal. After a tag a host and an agent act on a per-stream account that is wrong.

**Options.** (1) Keep two per-stream lost counters on the pipe: the offset range may be merged, the count may not; hand them to the reader and credit the sinks from them. (2) Raise `maxPendingDrops`: it only moves the point where the merge starts.
**Recommended.** (1), as the review states it.

**Verify.** A unit test on the output pipe (lossy policy, no reader): write alternating small writes of the two streams until more than `maxPendingDrops` ranges are pending and the queue has overwritten bytes of both, then read: the lost bytes per stream equal the bytes of that stream that were overwritten (compute the expected counts by simulating the queue). A wire test through `Run` and `RunStream` with the reader held by the `beforeRead` seam (`pkg/v1/exec/run_server.go:53-55`) so the number of pending ranges is deterministic, and not by pace: for each stream `total_bytes` equals what it wrote, `kept + dropped == total`, and the two sums agree; run it with 3,000,000 one-byte writes per stream and with 256 B, 4 KiB, 32 KiB and 256 KiB writes. The sum check alone is not evidence: it held while each stream was tens of MB off. A mutant that merges by label again fails both tests.

## Decisions

### S2 and S11

The two decisions below are the lead's. Each answer closes its item once it is written and the code, the proto and the guide follow it. Their full findings (what was observed, where, the check) are [S2](#s2-a-cancelled-attach-can-still-supersede-the-hosts-live-attach) and [S11](#s11-the-keepalive-policy-cannot-be-discovered).

**Decide S2: the attach epoch.** The guide says a host that cancels an attach and attaches again at once "is never superseded by the attach it gave up on" (`docs/exec.md:240`). It is, in 0.32% of racing rounds, because grpc-go finishes a cancelled stream on a goroutine of its own, and the SDK cannot see a cancel that has not arrived. No byte is lost.
- *Option A, an additive field that carries the host's ordering.* `Attach` gains an epoch that the host increments for each attach; the ring lets only a higher epoch supersede (the check belongs under the ring's lock in `ring.attach`, `pkg/v1/exec/replay.go:259-290`), and zero (absent) keeps today's rule, so no existing host changes. It makes the sentence true whatever the transport does. It costs a wire field that is permanent after a tag, a host that must keep a counter, and a rule for a stale epoch (refuse the late attach). It orders attaches only within one host's own counter: attaches from different connections still have no order, as the guide says.
- *Option B, a weaker sentence.* Reword `docs/exec.md:240` to "holds only if the host waits until its cancel has been sent", and tell hosts that `Detached{SUPERSEDED}` on a stream that the host believes holds the session means "ask `ListSessions` and attach again from the offset you have". It costs no wire and no code, and no byte is lost either way, but the host's live stream is told it was taken over and the session is held by nobody until the host re-attaches, for the lease; every host must implement the recovery.
- *Option C, make `Conn.Close` wait until the stream is finished.* Client-side only; it does not help a host that cancels its context, which is the case that raced.
- Recommended: A if the host will own a counter anyway (H1b keeps per-session state); otherwise B. This is this page's judgment, not the review's.

**Decide S11: keepalive discoverability.** A host that applies `sdk.HostKeepaliveDialOption()` to a plugin whose SDK has no matching policy is sent `GOAWAY too_many_pings` at 120.1 s and loses its streams at 121.1 s. `Describe` has no signal for the policy (`contract_version` is 1, no feature).
- *Option A, a feature (or a version bump) that says "serves the keepalive policy".* A new value in `Feature` (`proto/exec/v2/exec.proto:258-282`) listed by `servedFeatures` (`pkg/v1/exec/describe.go:19-25`), with conformance cases written for it, because a suite that meets a listed feature without cases fails ([Test your target](exec.md#test-your-target)). It tells a host per plugin, and survives a later change of the policy. It costs an enum value that is permanent after a tag, and cases.
- *Option B, write the rule for the host.* "Apply the option only if the plugin's v2 `Describe` succeeds." A released plugin that predates the lane answers a v2 `Describe` with `Unimplemented`, so the rule is exact for every plugin built from a release; it is wrong only for a plugin built from this branch before #92, which serves v2 without the policy. No release carries the lane yet, so such builds are development builds. It costs a paragraph in [Stream health](exec.md#stream-health) and in the host's design (H1b). It is not enough if the policy later changes.
- Recommended: B for the first tag, because the first release that carries the lane also carries the policy; take A if a second policy is expected. This is this page's judgment, not the review's.

## Findings

Severity is the review's. T1 is the only finding that must be fixed before any tag by itself; S1 to S12 are SHOULDs (cheap now, expensive after), and the gate item they belong to is named in each. Where the review's file or line had drifted, the line given here is the one verified at `b9cedd6`.

### S1. The typed end at shutdown is lost by about 7% of reading streams

**Severity** SHOULD; part of B1.4 ("fix S1 or say it"). **Where** `pkg/sdk/plugin.go:376-388` (`shutdownBeforeStop` returns as soon as the handlers have *sent* their end; the hold is `capabilityPreStopBound`, 1.5 s, `:347`), `docs/exec.md:232` ("each attached stream receives its `Ended`"). go-plugin v1.8.0 `GRPCClient.Close` is the controller's `Shutdown` followed at once by `Conn.Close` (`grpc_client.go:105-109`).

**Observed.** A real go-plugin host with a reader blocked in `Recv` on every stream, then `Client.Kill()`: 5 sessions × 40 launches, **187 of 200 streams got the typed end and 13 got `CONNECTION`**; with a stuck author 237 of 240 (plugin exit at 1.03 s). Idle sessions: 25% lost with 2 sessions (15 of 60), 7.7% with 10 (23 of 300), 3.3% with 100 (66 of 2,000); 2 of 120 `Run` results lost, the ones delivered last. The plugin always exited before the host's 2 s SIGKILL: `Client.Kill()` took 2 ms idle, 1.0 s with stuck authors and 1.526 s at most (116 sessions, 4 floods, 2 stuck authors, a hung `Options`, 3 `Run`s and 3 `RunStream`s); the host never had to SIGKILL (13 scenario launches, 110 loss launches).

**Fix** (as the review states it). Settle after the last end, so the transport flushes and the host's readers take it: a 50 ms settle inside the existing 1.5 s took the loss to 0 of 360 (60 launches with 2 sessions and 300 with 10; not measured with a flood or with 100 sessions). Or say "a host must close its streams before `Kill`" in the guide. A settle adds up to its length to every stop; the sentence costs the host (H1b) an ordered close.

**Verify.** No test crosses the close of a real connection right after the controller's `Shutdown` at `b9cedd6`: the tests of the hold (`TestTheControllersShutdownEndsEverySessionBeforeTheServerStops` and `TestAStuckAuthorsSessionStillReceivesItsEndBeforeTheServerStops`, `pkg/sdk/shutdown_stop_test.go:410` and `:470`) read what the plugin sent to every stream while the server still serves, with a stand-in controller that stops the server only when the test lets it. Write one that does cross it, with the re-exec pattern of `pkg/sdk/managed_serve_test.go` (the test binary as the plugin child, `:22` and `:59`; a go-plugin client, `:62-71`; `client.Kill()`, `:86`), registering an exec target: open N sessions, keep a reader goroutine in `Recv` on each, call `Client.Kill()`, and count the streams that received `Ended{Closed{PLUGIN_SHUTDOWN}}`. Repeat for 2, 10 and 100 sessions, 30 launches each, and with a stuck author: every stream must receive it (200 of 200 in the 5 × 40 loop), the plugin must still exit before 2 s, and `Run` results in flight must arrive. If the sentence is chosen instead, `docs/exec.md:232` says it and a guide test pins it.

### S2. A cancelled attach can still supersede the host's live attach

**Severity** SHOULD; decide (see [Decisions](#s2-and-s11)). **Where** `pkg/v1/exec/replay.go:259-290` (`ring.attach` decides by `ctx.Err()` at registration, `:270-272`); `docs/exec.md:240`.

**Observed.** One connection; attach B1 is cancelled 0 to 400 µs after its `Open` returned, and attach B2 is made at once; B2 is checked before the next round. B2 was told `Detached{SUPERSEDED}`, and the session is then held by nobody for its lease. 73 events in 22,892 rounds (0.32%, under `-race`, 2 CPUs); in 64 of the 73 B1's `Open` had already failed at the client. Re-run without `-race`, 8,000 rounds: 33 events (0.41%). Cause [INFERENCE, not confirmed by the 3 ms-gap run, which did not finish]: grpc-go finishes a cancelled stream on a goroutine of its own, so B2's request can reach the server before B1's `RST_STREAM`, and B1's handler registers second with a live context. The guard works when the cancel arrives first (the mutant that removes the `ctx.Err()` guard is killed by `TestAnAttachWhoseStreamEndedBeforeTheEngineRegisteredItTakesTheSessionFromNoOneWhateverTheOrderOfTheHandlers`). No byte is lost. Two attaches on two connections have no order and exactly one holder (2 × 400 rounds), as the guide says.

**Fix** (as the review states it). An attach epoch (an additive field the client increments; the ring lets only a higher one supersede), or make `Conn.Close` wait until the stream is finished, or reword the guide ("holds only if the host waits for its cancel to have been sent") and tell hosts that `SUPERSEDED` means "check `ListSessions`".

**Verify.** Run through a real gRPC transport (`internal/exectestkit.Serve`, `internal/exectestkit/serve.go:66`), not the memory rig, which has no transport and so always delivers the cancel first: one connection, 8,000 rounds, B1 cancelled after 0 to 400 µs and B2 attached at once; B2 must never end with `Detached{SUPERSEDED}` (0 events; any event is a failure, so run it under `-race` in a two-CPU container as well). With the epoch, add a forced-order unit test on the ring: the higher epoch registers first, then a late lower one arrives with a live context and is refused. With the sentence, `docs/exec.md:240` is reworded and a guide test pins the new sentence.

### S3. A cancelled `Open` leaves a live session behind

**Severity** SHOULD. **Where** `pkg/v1/exec/open_server.go:253-295` (`run` and `leave`: a stream that breaks is an implicit detach; only a failed send of `Opened` destroys a started session, `:265-271`), against `docs/exec.md:236` (which names only "a stream that broke before the SDK could send `Opened`") and the `Client.Open` doc, "A refusal is an error and means nothing ran" (`pkg/v1/exec/client.go:51`).

**Observed.** 400 `Client.Open` calls cancelled at random times within 2.5 ms: 11 returned an error and 389 opened. Of the 11 errors, **6 left a live session** (function running, place held, id taken), and the retry of the same id was `CONFLICT`/`SESSION_EXISTS` 6 of 6 times. It follows from the design (a stream that breaks is an implicit detach), but the host never learns that its error hides a session. A user who closes a terminal tab in the 2 to 5 ms between `Open` and `Opened` leaves a running shell and a place for 5 minutes with no way to know.

**Fix** (as the review states it). Say it where a host reads it (the `Client.Open` doc), add a short `detach_timeout_ms` to the advice for terminals (a host can set it today), or have `Client.Open` end the id on a failure that was not a refusal.

**Verify.** 400 `Client.Open` calls cancelled at random times within 2.5 ms against a real transport: for every call that returned an error, `ListSessions` shows no live session for that id and a retry of the same id is accepted; or, if the doc route is chosen, the `Client.Open` doc and `docs/exec.md:236` say that an error from a cancelled `Open` may hide a live session, and a guide test pins it.

### S4. `ReadOutput` says `ended=true` on the first partial page of a tombstone

**Severity** SHOULD; part of B1.10. **Where** `pkg/v1/exec/session_server.go:172` (`Ended: ended` for every tombstone read), against `proto/exec/v2/exec.proto:1385-1387` ("ended is true when the session has ended and next_offset is its last offset").

**Observed.** A 300,000-byte tombstone read from offset 0 with `max_bytes` zero: `next_offset=65536 ended=true` while `last_offset=300000`, and the next four pages (`next_offset` 131,072, 196,608, 262,144 and 300,000) also say `ended=true`. A host that loops `for !resp.Ended` stops after 64 KiB and loses the rest silently. No test pins it.

**Fix** (as the review states it). `Ended: ended && next >= last`.

**Verify.** Read the 300,000-byte tombstone page by page with `max_bytes` zero: `ended` is false on the first four pages and true on the fifth, whose `next_offset` is 300,000; a read whose `max_bytes` covers the whole output answers `ended=true` at once; a tombstone that lost its bytes answers `ended=true` with `next_offset` at the end of the output.

### S5. `Ended.dropped_bytes` and `output=COMPLETE` ignore bytes lost to an unread replay

**Severity** SHOULD; part of B1.5. **Where** `pkg/v1/exec/replay.go:287` (`ring.attach` sets `r.delivered = r.endOff` when the snapshot is taken) and `pkg/v1/exec/ring.go:326-345` (`trimOldest` counts a chunk as dropped only if `r.delivered < end`, `:329`); `docs/exec.md:132` ("what leaves the ring while no stream is attached is counted as well").

**Observed.** Blocking target, 64 KiB ring of 16 KiB chunks: an attach hands bytes [0, 40,000) to a stream that is cancelled unread; the function writes 40,000 more bytes while nobody is attached; a new attach from 0 answers `replay.start=16384` (the host is told that 16,384 bytes are gone) and `end=80000`, but the session ends with `Ended{bytes_out=80001, dropped_bytes=0, output=COMPLETE}`. The host is never misled about the bytes (`replay.start` is exact; 0 unannounced holes in 100 random walks), it is misled by `Ended.output` and `dropped_bytes`: 28 of the 100 walks ended with `dropped_bytes=0` beside a loss announced by `replay.start`.

**Fix** (as the review states it). Count the unread remainder of a snapshot when its stream leaves, or document that `replay.start` is the host's account of what it lost.

**Verify.** The scenario above, deterministically (a blocking target, the replay held by a stream that leaves unread): `Ended.dropped_bytes` is at least 16,384, `output` is `TRUNCATED`, and `bytes read + dropped == bytes_out` for the host that read what it was given; `COMPLETE` never appears beside a `replay.start` above the offset the host asked for. The B1.5 property test covers it.

### S6. `ListSessions` has no size bound and fails `LIMIT` for everyone

**Severity** SHOULD; part of B1.10. **Where** `pkg/v1/exec/session_server.go:107-130` (`listSessions` lists every live session and tombstone with its facts, an argv of up to 64 KiB), `pkg/v1/exec/session_client.go:15-30` (the call sets no `MaxCallRecvMsgSize`, so gRPC's 4 MiB default applies; only the legacy resource-session calls at `pkg/v1/exec/plugin_client.go:139-142` and `:196-197` set one).

**Observed.** 32 live sessions and 256 tombstones whose argv is 60 KiB: `ListSessions` fails with `LIMIT`. About 70 such sessions are enough [INFERENCE: 4 MiB / 60 KiB; the threshold sweep was not run], and the failure lasts for the 60 s of a tombstone's life. `ListSessions` is the host's recovery call after a window reload.

**Fix** (as the review states it). Cap the argv in `SessionInfo.facts`, page the answer, or set the call limit; document the bound.

**Verify.** With 32 live sessions and 256 tombstones whose argv is the largest a request may carry, `ListSessions` succeeds (or returns a bounded page with a way to continue), and the size of the largest answer is a number the proto or the guide prints and a test pins.

### S7. `remote_may_survive=false` after the SDK saw the function not return

**Severity** SHOULD. **Where** `pkg/v1/exec/classify.go:160-199` (`closedEnd` and `survivalOfClose`: `KillTerminates` gives `false`), `pkg/v1/exec/engine.go:919-960` (`conclude` passes `gaveUp` to the output completeness and the result's `Abandoned`, but `decideEnd`'s inputs, `classify.go:50-57`, do not carry it); `docs/exec.md:309` (a `TIMED_OUT` result with `remote_may_survive` not `false` forbids a retry).

**Observed.** 200 of 200 `TIMED_OUT` results of a function that ignored `ctx` and was abandoned said `false` while the `abandoned` gauge was 200. Re-run: a `KillTerminates` target, a function blocked on a channel, `timeout_ms` 200: `outcome=TIMED_OUT remote_may_survive=false` after 252 ms, with the function still running; the `KillClosesStreamOnly` twin says `true`. The SDK has direct evidence (the function ignored the cancel) that nothing ended the process.

**Fix** (as the review states it). Absent or true when the function was abandoned.

**Verify.** The same two runs: for an abandoned function `remote_may_survive` is absent or true on the `TIMED_OUT` result, on a session's `Closed` end and on a run's end in every target kind; for a function that honors `ctx` on a `KillTerminates` target it stays `false`.

### S8. A `Run` is not bounded by its `timeout_ms` during the start

**Severity** SHOULD. **Where** `pkg/v1/exec/run_server.go:154-189` (`beginRun` takes `accepted` and `deadline` and starts the command) and `:266-275` (`await` starts the timer only after the start returned); `docs/exec.md:292` ("returns at its timeout plus the kill wait").

**Observed.** `timeout_ms=500` with an `Options` callback that takes 3 s: the call returned after 3.0 s (`duration_ms=3001`), `TIMED_OUT`, and the function was invoked only to be killed at once. A hung `Options` holds a run for 35 s (the 30 s deadline plus the 5 s grace) and refuses it `TIMEOUT`; a `Classify` that never returns makes the result arrive after 5.004 s (`timeout_ms=500`) and holds a place for ever (8 of 8 places, then `UNAVAILABLE`/`TARGET_WEDGED`). Bounded and typed, but 10 to 70 times the timeout.

**Fix** (as the review states it). Start the timeout's clock into the admission, or say so.

**Verify.** On the bubble clock, a `Run` with `timeout_ms=500` and an `Options` callback that takes longer returns at the timeout plus the kill wait (550 ms) with a typed result and no place held; a `Classify` that never returns holds no place past the timeout. Or `docs/exec.md:292` says that a slow start takes longer and a guide test pins it.

### S9. `FAILURE_STAGE_RUN` with `start_observed=false`

**Severity** SHOULD; part of B1.5. **Where** `pkg/v1/exec/exit.go:231-236` (`failedInternal` hard-codes `FAILURE_STAGE_RUN`), against `proto/exec/v2/exec.proto:1177` ("After the start was observed"); `docs/exec.md:290` says `RUN` also covers "the failure the SDK makes of a broken `Exit`, whether or not the start was observed".

**Observed.** An empty `Exit`, a code and a signal, a signal and `Lost`, signal number 99 and `runtime.Goexit` each end `FAILED`/`INTERNAL` with `stage=RUN` and `start_observed=false`, while a panic before any output is `stage=START`. A consumer that follows the proto reads RUN as "the command was seen to run".

**Fix** (as the review states it, with the pair of B1.5). Say it in the proto and keep the code, or make the stage follow `start_observed`.

**Verify.** The B1.5 property test: no `Failed` end the SDK makes has `stage=RUN` with `start_observed=false`, for each of the five shapes above and for a panic; or the proto's sentence at `proto/exec/v2/exec.proto:1177` says what the code does.

### S10. A query pipelined behind a `Close` is not always answered

**Severity** SHOULD. **Where** `docs/exec.md:225` ("a query is answered unknown at once"); `pkg/v1/exec/facts_query.go:282` (`watch` returns on `entry.Ended()`) and `:298-300` (when `entry.Go` fails the code does `q.claim(queryExpired)` and returns nil: no reply); `pkg/v1/exec/open_server.go:359-382` (the control frames are flushed before a stream's `Detached` and not before `Ended`; `pkg/v1/exec/open_control.go:259-262`).

**Observed.** Three queries (a working directory, the integration tier which the SDK answers itself, a working directory) sent behind a `Close`, the function returning at once, 150 runs: answers received per run `{1 answer: 142 runs, 2: 7, 3: 1}`; under `-race` `{0: 15, 1: 56, 2: 54, 3: 25}` (the SDK's own tier answer lost 15 times). With a function that takes 300 ms to return, `{1: 9, 2: 1}`. A query that was answered before the function returned: 300 of 300 delivered. The host never hangs (`Ended` arrives), so a host that treats `Ended` as "every query unknown" is safe.

**Fix** (as the review states it). Answer in `AskFact` when `entry.Go` fails and flush the control before `Ended`, or reword the guide.

**Verify.** Send `Close` and three queries back to back, the function returning at once: in every one of 150 runs, plain and under `-race`, each query receives exactly one answer (unknown for an unanswered one) before `Ended`; with a function that takes 300 ms to return, also three of three.

### S11. The keepalive policy cannot be discovered

**Severity** SHOULD; decide (see [Decisions](#s2-and-s11)). **Where** `pkg/sdk/plugin.go:249-261` (`HostKeepaliveDialOption`), `pkg/utils/keepalive.go:44-48` (the doc says to apply it "for the plugins that were built with an SDK that carries the matching policy"); `pkg/v1/exec/describe.go:19-25` lists no feature for it and `ContractVersion` stays 1 (`pkg/v1/exec/target.go:13`); [Stream health](exec.md#stream-health) gives no rule for the host.

**Observed.** A real go-plugin host that applies the option to a **v0.22.1 plugin** is sent `GOAWAY ENHANCE_YOUR_CALM "too_many_pings"` at **120.1 s** (the fourth 30 s ping), connectivity goes `READY` to `IDLE`, the open stream closes at 121.1 s, and the channel reconnects on the next RPC (`CONNECTING` to `READY` at 135 s). Without the option nothing happened in 150 s. The same option against a plugin of this branch, 150 s idle, with and without an open session: no GOAWAY, and an echo works. `GRPCDialOptions()` does not carry the option (by design). A black-holed path ends the plugin-side stream at 14 to 15 s and the host-side `Recv` at 39 to 40 s with `CONNECTION`; the session is then an implicit detach (lease about 4 m 35 s) and a reattach replays the bytes written meanwhile. A plugin built from this branch before #92 serves v2 without the policy and is hit the same.

**Fix** (as the review states it). A feature (or a version) that says "serves the keepalive policy", or write the rule for the host.

**Verify.** Option A: `Describe` of a build with the policy lists the new feature and a test applies `HostKeepaliveDialOption` only when it is listed; the new conformance cases pass. Option B: [Stream health](exec.md#stream-health) and the host's design say "apply the option only if the plugin's v2 `Describe` succeeds", and a guide test pins the sentence. In both cases a v0.22.1 plugin with the option applied still gets the GOAWAY at 120.1 s, which is why the host must not apply it there: the check is a 150 s run against a v0.22.1 plugin that does not apply it.

### S12. The `Exited` comment contradicts the TRANSPORT paragraph

**Severity** SHOULD; conditional, through B1.3. **Where** `proto/exec/v2/exec.proto:1235-1238` ("exit_code is not optional because the arm itself says it is known") against `:1193-1197` (`EXIT_SOURCE_TRANSPORT`: "a code from this source, 0 included, is never the command's") and the paragraph above it; `pkg/v1/exec/run.go:78-81`.

**Observed.** A `Run` of client-go's `nil` is `outcome=EXITED exit_code=0 exit_source=TRANSPORT remote_may_survive=absent start_observed=true`. An agent `run` that prints `exit_code` and drops `exit_source` says "exit 0" for a command that a kubelet restart killed.

**Fix** (as the review states it). `RunResult.exit_code` is already optional: leave it absent for TRANSPORT; fix the comment.

**Verify.** B1.3's checks 1 and 4.

### Notes N1 to N7

- **N1.** `Ended.bytes_in` is what the input queue accepted, not what the function read (`pkg/v1/exec/engine.go:954`, `pkg/v1/exec/pipes.go:548`; `proto/exec/v2/exec.proto:1310` says "the number the host sent"): the host sent 10 bytes, the function read 0, `bytes_in=10`; up to 227,680 accepted bytes were never read when a function returned early. Consistent with the proto; the host cannot know the consumed count.
- **N2.** Resize: a stream that attaches while another's resize waits is never told that size (the function takes it later; a UI sends its own size after an attach); the proto says a superseded resize is `IGNORED` (`proto/exec/v2/exec.proto:985`) while the guide and the engine acknowledge it `APPLIED` (`pkg/v1/exec/resize.go:13-14`); input of a stream that attaches after `close_input` is dropped without a word (`docs/exec.md:219`: "are not its violation"); the bound on answers is soft, 146 pending observed against 64.
- **N3.** A detached function never blocks, and a flooding one burns a CPU for the whole lease (up to 30 minutes; 101% of a CPU, 2,546 MiB/s into a 1 MiB ring); for `StallPropagates` a lossless wait would be cheaper. Memory bound, per target kind: `MaxSessions` × (ring + one snapshot while a stream replays) + 32 MiB of tombstones, about 288 MiB with 4 MiB rings (measured: 32 detached 4 MiB rings, 163.8 MiB of heap, unchanged after 2 GiB more was written into them; 35.6 MiB after they ended).
- **N4.** The proto comments at `proto/exec/v2/exec.proto:647-654` and `:951` still describe the ground-state boundary rule that the SDK does not apply (`start = max(from, ring front)`); the guide says so ([What the SDK does not serve](exec.md#what-the-sdk-does-not-serve)). A `replay_bytes` below 1 KiB gets a 1 KiB ring, so `pkg/v1/exec/ring.go:55-56` ("never holds more than capacity") is false there (harmless). The SDK sets no gRPC `MaxConcurrentStreams` (no occurrence in `pkg` or `internal`); what a host that opens unbounded attach streams costs was [INFERENCE: not measured].
- **N5.** The `request_id` of a run is the engine's id, shared with live sessions (`CONFLICT`/`SESSION_EXISTS` for a reused audit id), which `proto/exec/v2/exec.proto:1429` does not say; `OUTCOME_CANCELLED` never happens (B1.4); a stdin above the transport's 32 MiB (`pkg/resource/v2/validation.go:28`) is refused by gRPC with no exec `ErrorInfo`; writes between the timeout and the give-up (at most 50 ms) are delivered in the `TIMED_OUT` result.
- **N6.** `ListSessions`, `ReadOutput`, `EndSession` and attach check no connection or route; the guide says so and records it as open (B1.10).
- **N7.** Wall-clock and performance bars remain in the shipped suite, and three pins are missing: [no test decides by the clock](#no-test-decides-by-the-pace-of-the-machine) and [Pins and killer tests](#pins-and-killer-tests).

## Pins and killer tests

A mutant is a one-line change of the production code; one that no test fails ("survives") is behaviour no test pins. The review ran 87 mutants of the code that #91 and #92 added, one at a time: 79 were killed and 8 survived, with no invalid build. Three survivors are equivalent: `L05` and `L06` (two redundant guards in the lease; together, as `L07`, they are killed by `TestAStaleLeaseNeverEndsASessionAndTheCurrentOneEndsOnlyADetachedSession`) and `P02` (`len(p) >= tailCap` against `>` leaves the same bytes). The other five are gaps. The ids below are the mutants'; **the mutant `S12` and the findings S12 are unrelated.**

**Two killer tests to lift.** Both were written, green on the head and red against the mutant, and are not in the repository; the intent is below.

1. **Mutant `T05`: a start of a tombstoned id on a full target answers `CONFLICT`/`SESSION_EXISTS`, before `LIMIT`.** The early check `e.tombs.has(p.ID)` at `pkg/v1/exec/engine.go:343-349` ("the id before the limit, as for a live id") can be removed and no test fails: the target then answers `LIMIT`/`TOO_MANY_SESSIONS`, because the registry's limit refuses first (`:361`) and the tombstone check inside the admission (`:351`) is never reached. The test, in a `synctest` bubble on the memory rig (`newMemoryRig`, `pkg/v1/exec/open_memory_rig_test.go:125`) with a target made by `engTarget(blockUntilCancelled, ...)` whose `Session.MaxSessions` is 1 (`pkg/v1/exec/engine_rig_test.go:147` and `:157`): open `s1`; detach it; `EndSession(s1, KILL)`, so `s1` is a tombstone and its place is free; open `s2`, which takes the only place; start `s1` again. The handler must return `CONFLICT`/`SESSION_EXISTS` (`exec.ErrorClassConflict`, `exec.ErrorReasonSessionExists`), checked with `errors.As(exec.StatusError(err), &typed)`, and not `LIMIT`/`TOO_MANY_SESSIONS`. Then close `s2` with a kill frame and let both hosts leave. Suggested name: `TestAStartOfATombstonedIdIsConflictEvenWhenTheTargetIsFull`.
2. **Mutant `Z03`: a resize that replaces a pending size of another stream is acknowledged to its own stream.** `resizer.submit` (`pkg/v1/exec/pipes.go:615-628`) replaces the pending size and its owner (`:624`); a version that keeps the first owner is not caught, because `TestTheAckOfAResizeGoesToTheStreamThatSentItAndAStreamThatAttachesLearnsTheSizeFromTheReplay` sends the second stream's resize after the function took the first's, never while it is pending. The test, for each of the three ways a stream leaves a session that lives on (`streamLeaves`, `pkg/v1/exec/open_control_attach_test.go:51-67`: the next attach supersedes it; it detaches and the next attaches; it is cancelled, an implicit detach, and the next attaches), with a control target held at a gate (`ctlTarget`, `ctlGate`, `pkg/v1/exec/open_control_rig_test.go:197` and `:215`) that then reads `p.Resize` and records the sizes: stream A opens `s1` at 100×30 (`ctlSession`) and sends `Resize{seq 7, 120×40}` while the function is held, so that size waits and no ack is sent; stream B takes the session by the leave mode and sends `Resize{seq 8, 90×25}`, which replaces the waiting size; B has no ack yet. Open the gate. The function is handed 100×30 and then 90×25 (A's 120×40 is replaced and never delivered); B receives exactly one ack, `Resized{seq 8, 90×25, APPLIED}`; A receives none. Suggested name: `TestAResizeThatReplacesAPendingSizeOfAnotherStreamIsAckedToItsOwnStream`.

**Three pins nobody wrote.** The repository pins a constant as a number beside the sentence that prints it (`pkg/v1/exec/engine_pins_internal_test.go`; for example `TestTheQueueBoundsAre256KiBAnd4096RunsAnd64Drops` pins `maxPendingDrops`). Add these in the same style, and an effect test where the existing tests never reach the constant.

1. **Mutant `S12`: `ReadOutput` caps its answer at 1 MiB.** `maxReadOutputBytes = 1 << 20` (`pkg/v1/exec/session_server.go:24`) is an operand of `min(max, r.capacity(), maxReadOutputBytes)` (`:171`); without it a ring above 1 MiB answers more than the guide's "never more than the ring's size or 1 MiB" ([A session outlives its stream](exec.md#a-session-outlives-its-stream)). The existing test writes 200 KiB into the default 1 MiB ring (`pkg/v1/exec/tombstone_memory_test.go:264-315`), where the ring's size equals the cap and the cap can never bind. Write: a session started with `replay_bytes` of 4 MiB (the default maximum), a function that writes 3 MiB of a known pattern and then waits, a detach, and `ReadOutput{from_offset: 0, max_bytes: 4 MiB}` which must answer exactly 1,048,576 bytes with `next_offset` 1,048,576 and at most 1024 chunks; a second read from that `next_offset` answers the next 1 MiB, contiguous; `max_bytes` zero answers 64 KiB. Also pin `maxReadOutputBytes == 1<<20`, `maxReadOutputRuns == 1024` and `defaultReadOutputBytes == 64<<10`.
2. **Mutant `N05`: `maxPendingAnswers` is 64.** `const maxPendingAnswers = 64` (`pkg/v1/exec/open_control.go:33`, used at `:197`); the guide says "room in the stream's (64 answers)" (`docs/exec.md:217`). Pin the number and the guide's sentence. For the effect, a test at the `streamControl` level in package `exec`: with no reader, 64 answers are taken by `answerWait` without waiting and the 65th waits until room is made [INFERENCE: not written; the answers a stream's sender has already taken do not count, so measure how many the rig's channel holds first].
3. **Mutant `N09`: `defaultRunProgressEvery` is 1 s.** `const defaultRunProgressEvery = time.Second` (`pkg/v1/exec/run_server.go:42`, the ticker at `:536`); the existing progress test drives the ticks by hand (`progressTicks`, `tickHandled`, `:44-52`) and never reads the production pace. Pin the number, and add an effect test with no seam, on the bubble clock (a ticker in a bubble uses the fake clock, so nothing waits): a `RunStream` whose function writes a byte and then waits sends no `RunProgress` for 999 ms, exactly one at 1 s, none in an interval where output did not advance, and one more in the next interval where it did.

## Open, and not blocking

**B1.9. `Session`/`Run` declaration** (SHOULD 4 of the contract review). A target with neither a `Session` nor a `Run` section registers without an error or a log line and serves only `Options`; `Exec` is still required for it; and the SDK reads the declaration as "a section of `Info` that is not zero" (`registeredTarget.servesSessions` and `servesRuns`, `pkg/v1/exec/register_v2.go:23-28`; `SessionLimits.IsZero` and `RunLimits.IsZero`, `pkg/v1/exec/limits.go:102` and `:240`). The guide says so ("What you write and what the SDK owns"). Additive to change (an explicit declaration); do it before the registry the host reads consumes `IsZero`.

**B1.11. `Exit` default** (decide). `SourceUnspecified` beside a code or a signal reads PROCESS (`pkg/v1/exec/exit.go:40` and `:218-222`), so a hand-built `Exit{Code: &n}` claims the strongest source, "known gone". The review's option: read it as the weakest (TRANSPORT with a violation), so that `ExitCode(...)` is the only way to claim PROCESS, and consider an opaque `Exit` (its five fields are exported). `ExitCode(...)` is the constructor the guide teaches, and an `ExitCode` from a target whose `ExitReport` is `ExitReportNone` is already downgraded to TRANSPORT with a violation (conformance case 17). Verify: `Exit{Code: &n}` becomes `Exited{n, TRANSPORT}` with a violation; `ExitCode(n)` stays PROCESS.

**B1.12. Vocabularies and the string guard.** `Choice.Kind`, `Choice.Detail` and the states are still the provider's to invent (K1a publishes them). `TestAbsenceIsUnknown` audits bool, numeric, bytes, enum and message fields and skips strings (`proto/exec/v2/contract_test.go:720`); `Choice.no_program` is in its audit as a declared bool. `Mark.command_line` and `Mark.cwd` (`proto/exec/v2/exec.proto:1044` and `:1046`) cannot tell an empty command or directory from "not reported"; the SDK serves no marks, so nothing holds the rule. All additive.

**B3. Exports that should still not be public** (not worse at #92; decide when H1a-1 writes its first test).
- `NewTargetPlugin` (`pkg/v1/exec/plugin.go:80`): its non-test callers are `internal/exectestkit` and the package's own `exechook` binding; hide it behind `internal/exechook` as `narrowCapabilities` already is.
- `SourceHost` (`pkg/v1/exec/enums_v2.go:333`): no use outside the package except `exectest`, which names it (conformance case 17) to test the violation of an author who claims it; an author "may not set it".
- `SessionLimits.IsZero` and `RunLimits.IsZero` (`pkg/v1/exec/limits.go:102` and `:240`): `exectest` calls them; they publish "zero = not served", which B1.9 may change.
- Of the seven SDK-raised `ErrorReason` constants (`pkg/v1/exec/enums_v2.go:263-270`), six have a producer in the SDK (`ErrorReasonTooManySessions`, `ErrorReasonTooManyRuns`, `ErrorReasonInputTooLarge`, `ErrorReasonSessionExists`, `ErrorReasonSessionUnknown`, `ErrorReasonConsentRequired`) and `ErrorReasonInputBacklog` has none.
- `exec.Exit`'s five fields: consider opaque (B1.11).
- `exectest` had 42 identifiers at #87: 17 an author needs (`Conformance` three times, `ProcRig` fourteen) and **25 not** (`NewFakeTarget` with 8 `Fake*`, 13 `Step`s, `Gate` four times). They serve the engine's and the host's tests, so `internal/` is right unless the host repository needs a scripted target. `NewProcRig` holds a function to a *fake* target's capabilities unless given `RigCapabilities(target.Info.Capabilities)`: take the `Target`. #91 and #92 add to `exectest` only `CodeExitError` and `ProcRig.Ask`, `Facts` and `Signal`; the 25 are unchanged.
- Exports added by #91: `Client.Run`, `Client.RunStream`, `RunConn`, `ErrorReasonTargetWedged`. By #92: `Client.ListSessions`, `ReadOutput` and `EndSession`; `Proc.Signals`, `Proc.Queries` and `Proc.Fact`; the types `Fact`, `FactKind`, `Foreground`, `ForegroundState`, `Query` and `UnknownReason`; `Choice.NoProgram`; `ExitOfStreamError`; `OnFirstRead`; `ErrorFromHTTPStatus`; `sdk.HostKeepaliveDialOption`; `utils.HostKeepalive`; `utils.ServerKeepaliveOptions`.

**B4. The 11 deferred SHOULDs of the contract review.** SHOULD 4 (explicit `Session`/`Run`): B1.9. 5 (`Choice` states and vocabularies): B1.12; the mode of a choice is now published with the options (B1.1). 6 (typed `UNSUPPORTED` reasons, proposed as `LANE_ABSENT` and `NOT_SERVED`, neither of which exists, so that "stay on the legacy lane" does not depend on call order): open, additive, before H1a-1 picks lanes by call order. 7 (refusal as a typed detail): decided, B1.8. 8 (the no-status arm): B1.3. 11 (the rig fails writes after the end; five cases; the contract text of a panic in `ExecFunc` and the author's obligations): closed, [When your function breaks the contract](exec.md#the-minimal-execfunc) says them and `pkg/v1/exec/engine_author_test.go` and `pkg/v1/exec/engine_hung_callbacks_test.go` hold them. 13 (`OutputAccount.truncated`): B1.5. 14, 15, 16: B1.10. 17 (`Describe` static or per connection): closed, "static per plugin" is the answer, which the guide says and `exectest.Conformance` checks. The rest of 2 (names): B2. The Kubernetes compile proof: closed (B7).

**Found, not closed, and not gate items.**
- The resource lane has no hook for the retirement of a connection, so the options cache drops a connection's answers when a command or an `Options` call sees the retirement, and otherwise an answer lives out its 5 s. Concurrent starts of a cold request may each run the callback once (no single flight).
- An error that carries its status only in text (client-go's upgrade) is not typed by the SDK: the author's `Classify` does it with `ErrorFromHTTPStatus`.
- The container and mode combination cannot be said by per-choice capabilities: a container without stdin accepts an attach that asks for it and the input goes nowhere, and the exit code of an attach is in the Pod status, which only a provider can read.
- Not served by the SDK and needed by the same provider: `Classify` has no request, `LinkHealth` cannot be implemented over client-go, the wait for an ephemeral container that is starting has no place in the contract, and the vocabularies of `Choice.Kind` and `Choice.Detail` are the provider's to invent.
- A `Close` or a kill that follows input which the function never reads waits behind it, because the frames of a stream are applied in the order they were sent. The way out is to cancel the stream and end the session with `EndSession` and a kill (`TestACloseFrameBehindInputTheFunctionDoesNotReadWaitsAndCancellingTheStreamDetachesAndEndSessionEndsTheSession`).

## What is closed, and how each was verified

### B1.1. Narrowing: CLOSED

The condition: the contract did not say which capabilities a session may refine, and one target per (provider, type) cannot express Kubernetes' two modes (an exec has `HostRestart` none and `ExitReport` CODE; an attach has `ExitReport` NONE and runs no program). The decision, in the proto and the guide: (i) a `Choice` declares the mode of a session in `Choice.capabilities` (`proto/exec/v2/exec.proto:555-575`), and the SDK opens the session with the kind's bound narrowed by the choice that each option takes in the selection (the default choice of an option the selection omits), so `Opened` is accurate before any output; what follows a mode is what the SDK enforces after admission (resize, stdin EOF, signals, the fact modes, stall, `Kill`, `ExitReport`), while admission still holds `tty`, `env`, `cwd` and `initial_size` to the kind's bound; (ii) the host never receives output ahead of the session's `Started`; (iii) moot: `Opened` is not provisional. Two sub-points stay open and harmless: (iv) the cross-field rule "narrowing `tty` to false empties `integration`" is not written, and nothing depends on it while the SDK serves no integration; (v) `Proc.Capabilities` is a deep copy, so changing it changes nothing and only a `SessionFacts.Capabilities` reported with `Started` counts (its documentation says so).

Verified by `TestOpenedCapabilitiesAreTheKindNarrowedByTheChoicesTheSelectionNames`, `TestAChoiceCannotOpenASessionWiderThanItsKind`, `TestAChoiceCarriesItsCapabilitiesOverTheWireAndNilStaysAbsent` and conformance case 26; by the Pod provider, whose `Opened` carried exactly the two modes (exec: `HostRestart` none; attach: `ExitReport` none, no program); and by the review's runs: output never before `Started` (`[Started Output Ended]` in every run; 0 outputs before `Started` in the flood, lossy, tail and stalled runs of the earlier review).

### B1.2. End invariants: CLOSED, with S2

A session has one end and at most one `Ended` frame; every stream that can still receive frames ends with exactly one of `Ended` or `Detached`; a session that ends while no stream is attached has no `Ended`, and its end is the tombstone's (`SessionInfo.ended_with`). The SDK never writes a `Lost` end with `remote_may_survive` false (`Lost` is always true: `TestEveryEndOfASessionThatTheSDKMakesSaysWhetherTheProcessMaySurviveByTheTargetsKill`); a decoder that rejects the pair in a message from another producer is B1.6's.

Verified by `TestEveryStreamOfASessionEndsWithExactlyOneOfEndedOrDetachedAndTheSessionHasOneEnded`, `TestEveryPairOfOperationsOnASessionLeavesItInALegalStateWithAtMostOneEnd`, `TestCloseAndDetachInEitherOrderLeaveTheSessionInTheSamePlace`, `TestBlocker3SupersededIsADetachNotAnEnd`, conformance cases 9, 25 and P5; and by the review's runs: a random soak under `-race` of 100 walks × 300 moves (1,695 detaches, 1,326 cancels, 3,787 supersedes, 746 simultaneous attach pairs, 1,779 refused beyond-the-end attaches, 591 cancelled attaches, 238 resize storms, 36 `EndSession`, 58 `Close`) with 0 violations, 0 unannounced holes and at most one `Ended` per session, the tombstone equal to the `Ended` frame; attach against lease expiry (1,500 rounds with a 2 ms lease swept 0 to 4 ms: 451 attaches survived, 1,049 were refused with a `DETACH_TIMEOUT` tombstone, 0 ended and attached); attach against the end (600 rounds: 29 accepted with all bytes and one `Ended`, 571 refused with an exact tombstone); attach against `EndSession` and `Close` (500 rounds, 250 each, never both); two connections attaching at once (2 × 400 rounds: exactly one holder); and two flood-reattach runs against a real go-plugin child (8,606 and 6,843 detach, cancel, sever and supersede rounds in 30 s each, SHA-256 of what was delivered equal to the pattern). The one sentence of the guide that is false is the one S2 is about.

### B1.8. Refusal encoding: CLOSED

One encoding: the metadata strings of `google.rpc.ErrorInfo` (domain `omniview.sdk.exec.v2`, reason = the class, metadata `reason`, `program`, `selection_name`, `selection_value` and `retry_after_ms`) with a bounded `LocalizedMessage`, which the proto's header states and every refusal uses. No typed detail message was added. Verified: 42 `Run` refusals, 18 `Open` refusals and the session calls decode to class, reason and facts through that one encoding; `TestTheServedCallsThatManageSessionsAreTypedAndAnswerForTheTombstone`, `TestTheSessionCallsAreStrictAndReadTheSessionContextOfTheCall`, `TestAnAttachToASessionThatDoesNotExistIsNotFoundTypedAndNothingRan`.

### B5, B6 and B7: CLOSED

- **B5, the three review threads** on PR #87 (all answered with a reason). A `oneof` arm the decoder discards (thread 4661646400, `admission_v2.go`): acceptable, a malformed sender and not a downgrade; the wire-identical fix is B1.7. An `Exited` arm from the transport (4661646443, `exit.go`): acceptable for the merge and not for the tag; settled by B1.3. A bare `Canceled` from a catalog provider (4661646447, `plugin_server_v2.go`): acceptable, a provider that returns `context.Canceled` from a downstream operation of its own is misread as a retirement, which is a provider bug; `Options` is a read and a retry is safe; the mutant that drops the caller-alive guard is killed.
- **B6, a write that arrives without `Started`** (thread 4661646396). The SDK accepted output with no `Started`, so no illustration could refuse it; the real finding was B1.1 (the contract let output run under provisional capabilities). Closed in the SDK: the host never receives output ahead of the session's `Started`, which is the author's or one the SDK makes from the facts of `Opened` when output comes first, and a report after the first output or a second one is refused and logged. A quiet command reports its start with `exec.OnFirstRead`; one that ends with a status, having neither written nor reported, has `start_observed` true and no `Started` frame, which the proto allows ("a session that never writes output and never reports gets none"). Verified by `TestStartedIsSentBeforeTheFirstOutputWhoeverWinsTheRace`, `TestTheFirstOutputWithNoReportSendsAStartedMadeFromOpened`, `TestAReportAfterTheFirstOutputIsRefusedAndChangesNothing`, `TestAQuietCommandThatReportsItsStartOnTheFirstReadOfItsStdinHasAStartedFrame`; and by 0 outputs before `Started` in every run of the flood, lossy, tail and stalled proofs.
- **B7, guards and proof.** The Kubernetes proof against the real client-go is done (the spike; it decided B1.3 and exercised B1.1). The reviewed golden is the only wire guard until a tag carries the file (C3). `defaultMaxDimension` and `maxTargets` are pinned to the numbers the guide prints (`TestATerminalMayBe1024CellsASideByDefaultAndNoMore`, `TestAPluginMayRegister256TargetsAndNoMore`, `TestTheGuidePrintsTheNumbersTheLimitsPin`). `Exit.end` receives the session's effective set, so survival and what an exit may carry follow the session's mode (conformance cases 26 and 28).

### The ten Kubernetes-proof items: 10 of 10 CLOSED

Each was run through a real gRPC server on a unix socket (`internal/exectestkit.Serve`) with a target shaped like the Pod provider (lossy, `Kill=CLOSES_STREAM_ONLY`, `ExitReport=CODE`, an `exec` mode and an `attach` mode that runs no program), in one container, 7 s.

| # | Item | Observed |
|---|---|---|
| 1 | Success is `Exited{0, TRANSPORT}` (B1.3) | `ExitOfStreamError(nil)` gives `exited{source: TRANSPORT}`, `start_observed=true`, `remote_may_survive` absent, completeness `UNSPECIFIED`, with output (`[Started Output Ended]`) and with none (`[Ended]`); a `CodeExitError{3}` gives `exited{3, PROCESS}`, `remote_may_survive=false`, `COMPLETE`. No `Lost` and no `start_observed=false` for `exit 0`, the defect the spike found. `ExitOfStreamError` is nil-safe; `ExitStatus` is read before `ExitCode`; `Exited()==false` is no status. One proto comment contradicts it: S12 |
| 2 | `Started` for a quiet command | a function with no output that reads its stdin through `exec.OnFirstRead`: the host got `Started{transport: "k8s-ws-v5", program: [sleep]}` while the command was still quiet (3 s bound; it arrived at once) |
| 3 | the default choice is in `Request.Selection` | no selection sent: the function saw `selection=[container=main mode=exec] argv=[/bin/sh]` |
| 4 | `Options` is not run at every `Open` | the callback ran once for the picker RPC and 5 starts; twice after a second RPC (the RPC is always fresh); three times for a start 5.2 s later (the TTL is 5 s). The key is reference, incarnation and connection; a failed callback is never kept |
| 5 | typed 401 and 403 | an error with `HTTPStatus()` 403 gives `Failed{FORBIDDEN, TARGET_DENIED, stage START}`; 401 gives `UNAUTHORIZED`; 404 gives `NOT_FOUND` |
| 6 | `close_input` | `cat` sent `abc\n` and then `close_input`: output `abc\n`, EOF, the function returned, the session ended by itself; `bytes_in=4`, `bytes_out=4`, with a terminal and without |
| 7 | attach facts | `mode=attach` (`Choice.NoProgram`): `Opened.program` absent, `Started.program` absent, the function sees `argv=[]`, capabilities narrowed (`exit_report NONE`); an argv of its own is `UNSUPPORTED` |
| 8 | `replay_bytes` | served: 0 gives the default 1 MiB, 4 MiB + 1 is `INVALID_REQUEST`; exact accounting at ring sizes of 1 KiB to 4 MiB (item 9) |
| 9 | lossy drops are counted | read + dropped == `bytes_out` == `last_offset` over the wire, at ring sizes of 1 KiB to 4 MiB |
| 10 | `remote_may_survive` true for a Kubernetes close | present and **true** on a graceful close, a kill frame, `EndSession` graceful and kill, lease expiry after an implicit detach, lease expiry after `Detach{lease_ms}`, connection retirement, and plugin shutdown (an attached stream and the tombstone of a detached session); `exit 3` is `false` |

### Compatibility with the legacy lane: CLOSED

- **Legacy lane, raw HTTP/2.** For the contract (#87, last round): hosts {v0.21.0, v0.22.0, this tree} against plugins {v0.21.0, v0.22.0, this tree legacy-only, this tree with both lanes}: 12 runs, each of 17 legacy streams and 29 messages, 23 raw-identical to the v0.21.0 baseline and 6 identical after clearing `Session.created_at` (the baseline differs from itself there): **DIFFERENT=0 in all 12**. For #92: a real v0.22.1 host and the host of this tree against a v0.22.1 plugin, this tree's legacy-only plugin and its two-lane plugin, 6 rows, each compared with the v0.22.1 host × v0.22.1 plugin baseline: raw-identical 23, identical after clearing `created_at` 6, **DIFFERENT 0 in all five other rows**, and the baseline row identical to itself 29 of 29. A real v0.22.1 host drives the plugin of this tree and the host of this tree drives a v0.22.1 plugin. A plugin that serves only v2 still fails the legacy scenario by design (an old host lists it with no terminals).
- **Legacy files unchanged.** No file of the legacy lane has changed since v0.22.1 (`8011c55`): `git diff --stat 8011c55 b9cedd6 -- proto/exec.proto proto/exec.pb.go proto/exec_grpc.pb.go pkg/v1/exec/manager.go pkg/v1/exec/session.go pkg/v1/exec/buffer.go pkg/v1/exec/endmark.go pkg/v1/exec/plugin_server.go pkg/v1/exec/stream.go 'pkg/v1/exec/terminal*.go'` prints nothing; `plugin_client.go` gains one line and `client.go` only the v2 `Client` interface.
- **New calls to an old plugin.** The host's `Client` against a v0.22.1 plugin and against a legacy-only plugin of this tree: `Describe`, `Open` (start and attach), `ListSessions`, `ReadOutput`, `EndSession`, `Run` and `RunStream` are each an `*exec.Error` of class `UNSUPPORTED` (8 calls). Against a v2 plugin an empty `Open` is `INVALID_REQUEST` and an attach to an unknown id is `NOT_FOUND`/`SESSION_UNKNOWN`.
- **`buf breaking`** (against v0.22.1, against the base `8726f24` and against `abbcad0`, each under FILE, WIRE_JSON and WIRE): 0 findings in all nine runs. `buf breaking --against ".git#tag=v0.22.1"` is clean on this tree. The proto delta of #91 and #92 is two additive lines.
- gofmt is clean and `go vet ./...` is clean on linux, windows and darwin.

## How to verify the gate

### What each CI job means

The workflow `.github/workflows/native-exec.yml` runs on every PR (any base branch, no path filter) and on pushes to `main`: nine jobs.

| Job | Runs | A pass means | It does not mean |
|---|---|---|---|
| `Exec contract (ubuntu-latest)`, `(macos-latest)`, `(windows-latest)` | `go test ./pkg/v1/exec/... -count=1` on each OS | the engine, the servers, the clients, the conformance suite (`exectest`) and the proofs pass on three kernels; Windows has no pty and the lane needs none | not the race detector; not the other packages |
| `SDK suite` | `go test ./... -count=1` | the whole module passes | not under the race detector |
| `SDK suite (race)` | `go test -race ./... -count=1` | the whole module passes under the race detector (3 m 54 s on the head of #92) | not a race loop under CPU contention: use the loop below |
| `Generated exec protobuf code is up to date` | `buf generate`, then no diff under `proto/` | `exec.proto` and its generated Go agree | not that the contract is right |
| `Proto breaking changes against the last release tag` | `buf breaking` against the latest stable `vX.Y.Z` | nothing yet for `exec.v2`: the latest stable tag, v0.22.1, carries no `proto/exec/v2` (C3); it still guards the legacy proto | not the wire of `exec.v2`: the golden is its guard |
| `Exec compat with SDK v0.21.0 (ubuntu-latest)`, `(macos-latest)` | `TestCompatV0210` with `OMNIVIEW_COMPAT_V0210=1` | real v0.21.0 binaries and a plugin of this tree give the same legacy transcript four ways and the recorded one, and the new client gets a typed `UNSUPPORTED` from the old plugin | no run against v0.22.1 (made by hand, see Compatibility above); skipped on Windows |

### Heavy and timing tests: a container with two CPUs, never CPU burners on a host

A flood, a race loop or a mutant on the host competes with everything else on it and makes a timing test fail for reasons that are not the code. Run them in a container limited to two CPUs. This is the shape that the review's numbers came from (image `golang:1.27.4-bookworm`; the module cache mounted read-only, a named volume for the build cache); adjust the mounts to the machine [INFERENCE: the exact mounts differ by machine].

```sh
docker run --rm --cpus 2 -m 8g -e GOWORK=off -e GOTOOLCHAIN=local -e GOMAXPROCS=2 \
  -v "$PWD":/src:ro -v "$(go env GOMODCACHE)":/go/pkg/mod:ro -v sdk-gocache:/root/.cache/go-build \
  -w /src golang:1.27.4-bookworm \
  go test -race -count=1 ./pkg/v1/exec/ ./pkg/v1/exec/exectest/
```

- **A race round** is that command; it took 104 to 119 s (`pkg/v1/exec` about 104 s, `exectest` about 111 s under `-race`). **A race loop** is 20 rounds in a row: all 20 passed on the head of #92 (2,200 s in all, no `--- FAIL`, no panic, no `DATA RACE`), and 2 more rounds before them. Repeat the loop on the head that carries the fixes, in a container with the same limit, before clearing the gate.
- The unmutated five-package run used for mutants (`./pkg/v1/exec/`, `./pkg/v1/exec/exectest/`, `./internal/session/`, `./pkg/sdk/`, `./pkg/utils/`) takes 106 s without the race detector in that container.
- Case 21 of the suite takes about 170 s at `-cpu 1` [the author's figure; not re-measured].
- A test that needs a real connection (S1, S2, S3) runs against `internal/exectestkit.Serve` (a real gRPC server on a unix socket) or a go-plugin child process; the in-memory rig (`newMemoryRig`) has no transport and cannot show a race that lives in grpc-go.

### Run a mutant against a test

A survivor is a missing test. One mutant at a time, on a scratch copy:

1. `git worktree add ../exec-mut <sha>`; edit one line of the production code so that the old text occurs exactly once in the file (check with `grep -c`); one change, nothing else.
2. In the container above, run `go test -count=1 -timeout 300s -failfast ./pkg/v1/exec/`; only if that passes, run `go test -count=1 -timeout 300s -failfast ./pkg/v1/exec/exectest/ ./internal/session/ ./pkg/sdk/ ./pkg/utils/`.
3. **Killed** is a test that fails; note the first one. A hang to the timeout counts only if it is a detection: a `time.Sleep` inside a `synctest` bubble while another goroutine waits on a mutex never advances the fake clock, so that "kill" is an artifact of the mutant, not of a test; re-make the mutant without a sleep. **Survived** is all five packages passing: write the test, or show that the mutant is equivalent (the same bytes, or an unreachable guard, as `P02`, `L05` and `L06`).
4. Restore the file after each mutant (`git checkout -- <file>`) and check that `git status` is empty at the end.
5. A killer test is proven by a pair: green on the head and red on the mutant.

### The conformance suite and the real-process proofs

- **The suite.** `exectest.Conformance(t, target)` serves a target through the real SDK over a real unix socket and runs the numbered cases 1 to 28, the pair tests and the stress harness; the five rows that skip (19, 20, 24, P4, P6) say which step fills them. See [Test your target](exec.md#test-your-target). A suite that meets an SDK listing a feature in `Describe` whose cases are not written fails instead of skipping.
- **The proofs** in `pkg/v1/exec` run through a real gRPC socket: flood and echo (`open_flood_test.go`, `open_control_proofs_test.go`), 64 concurrent sessions (`open_concurrent_sessions_test.go`), `Run` capture (`run_proof_capture_test.go`), the wire stress (`open_wire_stress_test.go`), and the legacy lane against real v0.21.0 binaries (`compat_v0210_test.go`, which builds each fixture from `testdata/compat-v0.21.0` in its own module).
- **Real processes.** The repository has the pattern for a plugin child process (`pkg/sdk/managed_serve_test.go`, which re-executes the test binary as the plugin and drives it with a go-plugin client), and no test of shutdown delivery or of reattach across a severed connection at `b9cedd6`. The numbers of S1 and of B1.2 above came from a go-plugin host that reads every stream and can sever its connection; write that rig with the same pattern.

### No test decides by the pace of the machine

A test may wait for an event with a bound that only a hang reaches; it may not pass or fail because something was fast or slow. The new tests of #92 are almost all on `testing/synctest` (403 of the 690 clock-touching lines added are inside a bubble, where time is exact), the stress self-tests run until they see the fault, and the wire bodies wait for a place that is freed late (`mustOpenEventually`). What still decides by the clock at `b9cedd6`, to remove or to widen before the gate clears:

| Where | What decides | Can flake on 2 CPUs under `-race` |
|---|---|---|
| `pkg/v1/exec/exectest/conformance_run_test.go:28` (`testStdinBound`, 500 ms; the shipped twin `conformance_run_cases.go:152` is 3 s) | the stdin cases of `Run` give each run this as its **timeout**: a run that has not echoed and ended in time is `TIMED_OUT` and the case fails "stdin was never closed". The comment says it "decides no outcome": it does | possible at 500 ms beside a loaded neighbour; unlikely at 3 s, but the 3 s is shipped to every author's CI |
| `pkg/v1/exec/open_concurrent_sessions_test.go:444`, `:454-467`, `:498`, `:538`, `:608-613` | starvation (`Max > 4 × P50`), a 64-versus-1 throughput ratio, echo p95 floors, a held-echo ratio against a baseline measured earlier in the same run | yes, with a noisy neighbour (ratios of two measurements seconds apart) |
| `pkg/v1/exec/open_flood_test.go:248` (`floodMinMBps = 100`) and `pkg/v1/exec/run_proof_capture_test.go:313` (`capMinMBps = 100`) | an absolute MB/s floor (best of three), asserted when not `-race` | yes, at `-cpu 1` or on a loaded runner without `-race` |
| `pkg/v1/exec/open_control_proofs_test.go:50` | `under.P99 > bar` (the larger of 5 × the isolation bar and 20 × the p99 of the same echo alone, measured before) | possible: a neighbour that starts in between breaks a 20× ratio |
| `pkg/v1/exec/exectest/conformance_open_rows_streams.go:208`, `conformance_open_rows_tombstone.go:194`, `:213`, `:222`, `:228`, `conformance_replay.go:169` and `:304` | the plugin's `lease_expires_unix_ms` is compared with the **host's** wall clock read before and after the frame: exact for a monotone clock, but a wall-clock step between the two readings fails the row (the ledger harness hit it twice in 900 container runs when the VM clock stepped back, and was fixed; the **shipped** bodies still read the wall clock) | rare (needs a step) |

Latency bars with a wide margin (not a finding): the kill of a function that ignores `ctx` must end in under 2.5 s where the SDK takes 50 ms (`killBound`, `pkg/v1/exec/exectest/conformance_control.go:32`); `answerBound` 2 s for 20 echoes (`conformance_open_cases.go:629`); the stress's `CloseBound` 10 s and `stressGrace` 20 ms (`stress.go:127`). Bounds that only a hang reaches: `engineWait` 30 s (`pkg/v1/exec/engine_rig_test.go:29`), `mustHappen` 10 s (`pkg/v1/exec/pipes_test.go:26`), `caseBound` 10 s (`pkg/v1/exec/exectest/conformance_inprocess.go:27`), the stress's `until` 60 s.

### The golden and the generated code

After any wire edit: `go test ./proto/exec/v2 -run TestContractMatchesItsReviewedGolden -update` regenerates `proto/exec/v2/exec.contract.golden` (read its diff: it is the review of the contract); `buf generate` regenerates the Go (the CI job fails on any diff under `proto/`); `buf breaking --against ".git#tag=v0.22.1"` must stay clean. Make the wire edits of this page together, so that the golden and the generated code change once: B1.4, B1.5 (if a field is dropped), B1.7, B1.10 (if a route is added), S2 (if the epoch is chosen), S11 (if a feature is chosen), and the comments of S9 and S12.

### Clearing the gate

The gate is cleared when all of this is true on one head of `feature/exec-v2`, and the lead has reviewed that head:

1. C3, B1.4, B1.5, B1.6, B1.7, B1.10, B2 and T1 are CLOSED by their own checks above; B1.3's condition is met (the reader, or no code for TRANSPORT); S2 and S11 are decided, and the answer is written in the proto, the guide and the code; S4, S5, S6, S9 and S1 (through B1.4, B1.5 and B1.10) pass their checks.
2. S3, S7, S8, S10 and S12 are fixed, or the sentence they contradict is changed and pinned.
3. The five pins and the two killer tests are in the suite, and the mutants of every line the fixes touch are killed.
4. All nine CI jobs pass; a 20-round race loop of `pkg/v1/exec` and `pkg/v1/exec/exectest` passes in a two-CPU container.
5. The legacy-lane matrix against the newest stable tag is `DIFFERENT=0` in all six rows, the new calls to an old plugin are a typed `UNSUPPORTED`, and `buf breaking` is clean against the newest stable tag and the base.
6. Only then is a tag cut, from the branch the lead names, as C3 says.
