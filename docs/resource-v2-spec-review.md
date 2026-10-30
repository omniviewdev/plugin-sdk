# Proposed plugin-sdk versus resource specification

Status: **proposal for user review, not SDK acceptance**. SDK source/test receipts do not satisfy this review gate. SDK PR21 remains draft and must not merge before review.

## Conclusion

The canonical identity/value model matches C01. The replacement public SDK is broader than that mapping. Most complexity accumulated around generation admission, connection lifecycle, and resource-bearing utilities. Those are concrete safety problems, but their current public API shape was chosen during implementation rather than prescribed by the resource specification.

The requested code-review follow-up keeps transport framing internal, reduces unused adapter exports, separates controller/transport/provider responsibilities, and documents the candidate API in [the architecture guide](resource-v2-api.md). The canonical model and optional operations remain. Admission consolidation, lifecycle/overlay policy, utility scope, and transport ceilings still require explicit review before freezing the SDK.

## Specification baseline

Paths are relative to the managed cohort. `S` is `plugin-sdk`; `H` is `omniview`.

- Conceptual resource foundation: H/docs/superpowers/specs/2026-10-17-resource-foundation-design.md, especially lines12–50,85–175,187–230,322–340. Its diagrams and illustrative types are not frozen wire definitions.
- Concrete mapping and handoff: H/docs/superpowers/specs/2026-10-24-resource-query-contracts.md, §§1 and8–9. Later handoff/accepted P01 evidence supersedes the document's historical numeric-dependency hold; that does not authorize declaring every later engine implemented.
- Trusted invocation/error boundary: H/docs/superpowers/specs/2026-10-23-trusted-invocation-errors.md.
- History contracts remain proposals. No historical ingestion/store/query engine is accepted by this SDK work.

## Mapping and scope

|Area|Specification|Current proposal|Assessment|
|---|---|---|---|
|Identity versus access|Connection is a route, not identity.|ResourceAddress(provider,authority,type,scope,id), AccessRoute(connection), optional incarnation.|Matches C01; keep separate. See `ResourceAddress`, `ResourceReference`, and `AccessRoute` in `domain.go`.|
|Authority|Host assigns canonical authority from provider evidence.|Per-type NativeAuthorityHint; host assignment, retarget/restart fencing, conservative unknown continuity.|Separation matches; per-type hint shape and restart policy are added design choices. See `NativeAuthorityHint` in `domain.go`.|
|Scopes|Exact identity tuple differs from explicit query selection.|Repeated entries, duplicate rejection, explicit any/oneOf, separate identity/query dimensions.|Matches. Query-only repository selection must never become identity or permission proof. See scope admission in `controller_admission.go`.|
|Values|One lossless tagged value model.|Decimal int/uint strings, finite double including -0, seconds/nanos timestamp, bounded recursive maps/lists.|Matches mapping. Go/proto/TS are adapters, not independent semantic domains. See `Value` in `domain.go`.|
|Representations/schemas|Schema and representation revisions are not identity; absent/available/unavailable differ.|Explicit bindings and shared document references; unavailableReason is a safe optional machine code.|Core matches. unavailableReason is a deliberate integration extension, not an original named C01 field. See `SchemaBinding` and `Representation` in `domain.go`.|
|Provider operations|Catalog does not imply universal CRUD, enumeration, query, or watch.|Catalog-only Provider plus independently optional Getter/Enumerator/NativeQuerier/Mutator/Actioner and ancillary traits.|Matches accepted direction. Controller/options API is an implementation design choice. See provider traits in `domain.go` and construction in `provider_options.go`.|
|Capabilities|Support differs from authorization/planning.|Positive resource operation/scope declarations plus separately negotiated transport traits.|Useful split; not a complete planner capability language. TS now distinguishes ResourceCapability and PluginCapability.|
|Source pages/updates|Native acquisition is not final host query membership or result transport.|Finite SourcePage and advisory bestEffort updates.|Correct boundary. No host planner, durable source coordinator, or result lease engine is supplied.|
|Budgets|Query-profile reference policy has smaller predicate/value limits.|SDK candidate transport: 32MiB frame,16MiB body,depth64,262144 nodes.|Different policy layers, not one interchangeable budget. Explicitly approve transport bounds; P13 still owes lower query policy enforcement. S/pkg/resource/v2/validation.go14–30 versus specification§9.|

Mutation discovery now declares `create`, `mutationUpdate`, and `delete` independently, each with its own scope. `update` remains exclusively advisory watch delivery. One optional `Mutator` receives request verbs `create`, `update`, or `delete`; admission maps these to their exact catalog operations before calling native code. Missing declarations return `UNSUPPORTED`; neither a `Mutator` implementation nor a different supported verb implies support. These are positive scope-qualified declarations, not authorization or a complete planner contract. Protobuf transports the same operation strings without a coarse `mutation` alias. This implementation change does not approve the public API.


## Added API surface that deserves explicit review

|Current addition|Problem it solves|Concern / simpler proposed direction|
|---|---|---|
|Catalog revision embeds native client binding epoch; public epoch constructors/accessor.|Native config changes could otherwise return clusterB data under authorityA; metadata churn must not rekey unchanged resources.|Generation fencing is necessary. Embedding/parsing source epoch through Catalog.Revision is a new contract. Consider explicit binding generation inside one admission context.|
|Default and provider/route-qualified catalog pin helpers.|Each accepted source/relationship route needs its own observed generation; source pin cannot stand for target pin.|Two helper families and fallback precedence increase public rules. Prefer one route-qualified admission shape if direct/custom-provider use stays practical.|
|Qualified-observation framing, now SDK-internal.|Strict bounded transport of admission observations.|Public marshal/unmarshal/context-clearing helpers were removed. Domain checks and SDK transport share internal observation storage and validation; provider authors do not handle binary metadata.|
|Controller and transport both validate admission/results.|Direct Controller calls and custom remote providers both need protection.|Share primitives, not wholesale removal of checks. Preserve post-dispatch retirement fences.|
|WithClient and WithConnection / utility registry binding.|Utilities need actual started native clients and configured profiles; caller-provided data is not a grant.|Keep distinct only where native-client access versus profile access is intentional. Avoid multiple overlapping provider-facing lease facades.|
|Lifecycle/browser/overlay/readiness/context traits.|Durable desired profiles differ from native discovery; stopped versus unknown versus connected must be truthful without probes.|Useful, but transactional overlay ownership and restart behavior extend the foundation sketch and need explicit approval. CONNECTED means registered client, not network health.|
|Resource-specific exec/log/metric/networker RPCs.|Canonical values, references and generation admission must survive existing utility paths; old Struct/ordinary JSON would lose exact values or bypass admission.|Coordinated boundary migration is necessary if resource v2 ships. It is not permission to replace utility engines or add unrelated features.|
|SDK ResourceOperationError with provider presentation candidates.|Preserve native classification across transport.|Host AppError remains canonical. Narrow provider-facing presentation fields or clearly document that they are untrusted diagnostics.|
|Audited protobuf adapter exports.|Transport/domain conversion.|Unused leaf exports were removed after a caller audit. Retained codecs serve SDK transport/utilities and have ownership/error contracts. Host subscription equality now uses domain normalization rather than protobuf.|

## What I would keep versus simplify

**Keep:** canonical addresses/references/routes; exact values; identity/query dimension separation; truthful schema availability; optional operations; explicit feature negotiation; one native connection registry; fail-closed generation checks; separate source pages and host results; matching utility consumers.

**Completed organization:** one controller/registry with focused lifecycle, admission, dispatch, and inventory files; an explicit ancillary router with private handlers; cohesive Helm/native provider files; separate host lookup/enumeration views; private framing; reduced adapter exports; compiling provider examples and declaration documentation.

**Still requires review before SDK completion:** whether to replace default-plus-qualified pins with one route-qualified admission context; public epoch framing versus explicit generation observations; lifecycle/overlay scope; transport ceilings; the remaining integration helper surface; and [asynchronous log-session generation ownership](https://github.com/omniviewdev/plugin-sdk/issues/23). These decisions remain separate from passing implementation checks.

**Do not restore:** old Find/mandatory-CRUD stacks, GVK identity aliases, resource fallback through legacy utility RPCs, caller-supplied authority/grants, fake stopped-state catalogs, or compatibility shims.

## Evidence and limits

Independent scoped reviews covered the reproduced defects, mutation contracts, responsibility extractions, export reduction, and documentation. They are not exhaustive security audits or public-API approval.

| Layer | Executed checks |
| --- | --- |
| SDK | Full normal and race suites: 21 tested packages each; compiling examples and focused vet passed (artifact 1550). After the final test-only adapter cleanup, full normal tests and five resource/metric/networker race packages passed (artifact 1579). |
| Host | Full normal tests: 42 packages; resource/invocation race tests: five packages (artifact 1554). |
| Kubernetes | Full normal and race suites: 11 tested packages each (artifact 1537). |
| Frontends | Host: 76 files/1,118 tests. Kubernetes: 52 files/417 tests, including the final dashboard cutover. Runtime: 22 files/288 tests, consuming the actual Go → gRPC → host error corpus. |

The final clean managed launch attested SDK `3a27b89`, host `d14e8be7`, Kubernetes `50902e8`, and UI `11a55a53` (artifact 1649). Build, dependencies, served source, and authentication were ready. HMR connected, but source reload remains unverified because graph watching was disabled. Later evidence-only documentation revisions do not change this exercised SDK code.

Fresh owned-kind/native-UI checks observed:

- All six dashboard workload cards opened canonical source-page tables on the final build. Smoke found the missed legacy-ID callers; the fix uses the existing encoded destination builder, not compatibility aliases.
- Native resource enumeration and a four-node Pod relationship graph worked after the controller/provider/host extractions.
- Monaco computed and rendered the YAML diff from `first` to `review-verified`. Native submission preserved ConfigMap UID `b9275c94-4901-418e-93bc-88cccf97474e` and advanced revision `653` to `3424`; an independent native API read confirmed the value.
- An accepted CoreDNS TCP forward served real metrics after the initiating RPC completed. UI Stop released its listener. Generation-retirement/cancellation races have deterministic SDK regressions; this native check does not claim an additional retirement race reproduction.
- UI Drain cordoned the owned node and evicted the probe Pod; native audit recorded status `201`. After explicitly refreshing the selected node snapshot, UI Uncordon restored scheduling and the probe rollout recovered. The eviction audit was metadata-level, so it does not prove request-body grace fields; the native-option regression covers unset grace and UID preconditions.

Earlier stale-incarnation rejection, malformed-YAML rejection, live-log, exec-buffer, and paging receipts remain historical evidence in the PR discussions; they are not claimed as rerun during this follow-up. One earlier uninstrumented `INVALID_REQUEST` remains unexplained. No speculative taxonomy change or terminal WebGL pixel-rendering claim was added.

The isolated application, browser, egress proxy, and owned kind cluster were stopped after acceptance. Private evidence and review workspaces remain. Source-built SDK/plugin edits were explicitly allowed for this run; profile isolation is not a sandbox, and only private synthetic credentials/configuration were supplied.

User SDK-spec review, coordinated publication pins, and the explicit asynchronous log-generation ownership decision remain gates. Native receipts do not approve those contracts. No production historical store, ingestion, query engine, follower, or timeline is claimed.

## Lint cleanup verification — 2026-10-30

Verified SDK source: `c0b3e25` in the cleanup worktree. Documentation-only updates after this source do not change the exercised code. The established baseline was 2,496 active findings, including 777 documentation findings. Completed cleanup waves reported 73, then five, then zero active findings. Configuration, thresholds, handwritten-code coverage, managed module selection, and generated protobuf Go were not weakened or rewritten.

Formatting is isolated in commits `55dd5f6`, `d202273`, and `c0b3e25`; declaration documentation, focused correctness fixes, and package-owned structural cleanup have separate reviewable commits. Nine implementation owners used nonoverlapping package/file boundaries. Separate reviewers inspected every slice and subsequent fixes. Final integration review found no evidence-backed integration defect within its static scope; it was not an exhaustive security audit.

All Go and Make commands below ran with `GOWORK=off GOFLAGS=`.

| Executed check | Result |
| --- | --- |
| `make fmt` and `make fmt-check` | Passed; final formatting check emitted no diff. |
| `make lint-report` | Passed, zero active findings. |
| `make lint-audit` | Completed, 1,224 informational findings from only the five deferred rules. |
| `go test ./... -count=1` | Passed: 22 tested packages, 16 without tests. |
| `go test -race ./... -count=1` | Passed: 22 tested packages, 16 without tests. |
| Focused behavioral regressions across ten packages | Passed: framing width boundaries, protocol overflow, invalid context entries, unsigned setting overflow, descriptor cleanup, readiness counters, invalid RPC dispatch types, and wrapped stream completion. |
| Disposable-child conformance command below | Passed: real child startup/exit, native profiles, gRPC finite reads, authority preservation, isolation, cancellation, and generation fences. |
| Standalone `go run ./.tools/cleanup-smoke` | Passed: actual local HTTP acquisition through Controller, exact signed/unsigned values and negative zero, stopped-dispatch rejection, one native read and one client destruction. Throwaway source removed afterward. |

The focused child-process command was:

```sh
GOWORK=off GOFLAGS= go test ./internal/resourceconformance \
  -run 'TestCurrentSDK(NativeBoundary|IndependentProfiles|HostIssuedAuthority|GenerationFence|ChildIsolation)$' \
  -count=1 -v
```

Reports and their regenerated summary remain ignored under `.tools/`. The informational audit contains `paralleltest` 574, `protogetter` 518, `testpackage` 80, `nonamedreturns` 29, and `funcorder` 23 findings. These rules were not mechanically enforced.

### Narrow suppressions retained by cleanup

Each new directive names its linter and explains the local contract. No file-wide or package-wide blanket suppression was added.

| Rule | Owning boundary and reason |
| --- | --- |
| `gosec` G115 | Catalog framing casts follow shared pre-allocation and adjacent unsigned-16-bit bounds; signed protobuf varints require two's-complement reinterpretation; metric enum casts preserve the existing documented `int32` ABI. |
| `gosec` G118 | Controller cancellation transfers to `Close`; generation cancellation transfers to `stopSlot`. Constructor return is not the end of either lifetime. |
| `gosec` G703 | The host-selected workspace manifest is canonical and absolute but intentionally need not live beneath SDK state storage. |
| `gochecknoglobals` | Process-wide logger/enricher registration and existing exported binding-value slices retain their established APIs. |
| `gochecknoinits` | Strict gRPC codec registration and request-context log enrichment must precede client/server or logger use. |
| `nilnil` | Optional health/watch/session absence, disabled telemetry, an unset settings stub, and deliberately broken or unreachable test providers retain distinct nil-result semantics. |
| `recvcheck` | Settings cloning, exec JSON mutation, and network value/pointer interface membership retain their existing receiver contracts. |
| `musttag` | Connection update/overlay serialization retains the established nested `types.Connection` JSON contract. |
| `iface` | Both exported network provider interfaces retain their exact method membership and source compatibility. |
| `errcheck` | Deferred producer joins still wait after cancellation; the already-selected terminal stream error owns the return value. |
| `fatcontext` | Catalog tests deliberately accumulate independently qualified observations, including aggregate-limit rejection. |
| `usetesting` | Disposable child sockets use short temporary roots to stay within Unix-domain path limits; cleanup still removes them. |
| `revive`, `staticcheck` | Required gRPC argument order and existing exported fixture/spelling contracts remain unchanged. |

Pre-existing binding-table, zero-setting, and closed-channel test-guard directives remain local; they are not new policy exclusions. Boundary changes and the explicit unused command-interface export reduction are recorded in [cleanup contract decisions](resource-v2-contracts.md#cleanup-contract-decisions).

The first central normal run exposed two incorrect test assignments; those were fixed before the final normal/race passes. Independent review also caught a benchmark compile error, a legitimate stopped-server teardown race, and missing pre-allocation framing validation; each finding was corrected and re-reviewed. Formatter nonconvergence in a multi-value return was fixed in source without changing formatter policy.

This receipt covers SDK-local checks, not a new managed-app, browser, Kubernetes cluster, or downstream build run. User API/specification review, coordinated publication pins, and asynchronous log-generation ownership remain pending. PR21 stays draft; issue12 stays open. No merge, release, tag, API freeze, or plugin-sdk completion is claimed.

## Main integration and deferred-rule audit — 2026-10-30

Cleanup was pushed to PR21, then integrated with `main` commit `32a3358` in merge commit `bea90a0`. Independent two-parent review verified that conflict resolution retains the Tools capability and the platform-specific, pollable PTY implementation. The obsolete unsplit terminal file was removed; error-aware constructor cleanup was carried into the Unix implementation without calling `Fd()` or changing accepted-terminal first-error semantics. The integration worktree was removed after the original checkout fast-forwarded successfully.

Main integration introduced 27 active lint findings. Documentation, equivalent source cleanup, and formatting resolved them without changing policy. Verified follow-up source is `27153b9`. Existing uncommitted configuration, Makefile, README, ignore, and CI changes remain untouched; they were not silently included in publication.

Five independent reviewers assessed deferred findings by domain adapters, transport, utilities, named-return guards, and test isolation. Every reported getter site was inspected: 223 domain/test reports, 129 transport/test reports, and 166 utility/test reports. The new Tools codecs were also reviewed; they add no getter findings. All named-return reports and constructor-order reports were accounted for. Test-rule review inventoried every reported file, deeply read child/global/lifecycle groups and the three newly merged test files, and sampled remaining assertions. This is not an exhaustive security or test-suite correctness audit.

| Deferred rule | Current findings | Decision |
| --- | ---: | --- |
| `protogetter` | 518 | Keep informational. Required-message, oneof, duplicate, and optional-presence checks remain explicit. Guarded scalar accesses offer safe local style changes, not demonstrated defects. |
| `nonamedreturns` | 29 | Keep informational. All reports belong to 19 signatures with deferred recovery/classification guards or their consistently named result tuples. No wrong capture or guard-order defect was found. |
| `paralleltest` | 583 | Keep informational. Shared process state, child entrypoints, parent-deferred cleanup, and shared per-case counters prohibit blanket parallelization. Independently owned pure tests remain optional candidates. |
| `testpackage` | 82 | Keep informational. White-box watch/readiness/state tests need private access. Some public-API tests can migrate independently; mechanically renaming packages would lose coverage or require new API. |
| `funcorder` | 23 | Keep informational. These are constructor-versus-method placement preferences, including paired transport conversions, not initialization-order defects. |

No production finding required promotion to the active gate. The review did expose two existing test hygiene defects, which were fixed rather than dismissed as deferred style:

1. `TestConfigFromEnv_MissingEnv` removed two process environment variables without restoration. A throwaway nested-test probe reproduced the leak before the fix and passed afterward. Commit `9c7dee8` registers both values with `t.Setenv` before deliberately unsetting them, preserving true absence during the test and restoring original values or original absence afterward.
2. The newly merged real-PTY master-read test did not register terminal cleanup immediately after allocation. An early resize/assertion failure could leak descriptors until process exit. Commit `27153b9` registers cleanup before those failure points, tolerates the expected already-closed result after the explicit shutdown assertion, and preserves the duplicate-TTY/read-completion ordering. Independent review verified this failure-path correction; no failure-injection runtime claim is made.

A separate executable getter probe rejected seven malformed protobuf values, preserved four explicitly selected zero scalars, and rejected four missing required messages without panic. It covered nil roots, typed-nil oneofs, absent payload messages, nil map entries, and duplicate keys. Both throwaway probe sources were removed after their receipts were captured.

At `27153b9`, all of the following passed with `GOWORK=off GOFLAGS=`:

- `make fmt`, `make fmt-check`, and `make lint-report`: zero active findings and no formatting diff.
- `make lint-audit`: 1,235 informational findings, exclusively the five rules above.
- `go test ./... -count=1` and `go test -race ./... -count=1`: 23 tested packages and 17 without tests in each run.
- `go test ./pkg/v1/exec -run '^TestRealTerminal_' -count=1 -v`: actual PTY blocking-read interruption and prompt session closure passed.
- `go test ./internal/resourceconformance -run 'TestCurrentSDK(NativeBoundary|GenerationFence|ChildIsolation)$' -count=1 -v`: actual disposable-child gRPC native reads, generation fences, isolation, and teardown passed.

Safe style opportunities remain in local interceptor-chain tests, exported settings conversion tests, fresh domain-JSON fixtures, and guarded protobuf scalar reads. Their existence does not justify describing every deferred warning as unsafe, nor does it establish a correctness reason to enable whole rules. No thresholds, exclusions, or deferred-rule policy were changed.

These are SDK-local receipts. Public API/specification acceptance, asynchronous log-generation ownership, and coordinated publication decisions remain pending. PR21 remains draft; no PR merge, tag, release, API freeze, or plugin-sdk completion is authorized.

## Review requested

Before calling plugin-sdk done: confirm which added contracts should remain public, whether admission should be consolidated, and which transport/lifecycle details should stay internal. The existing implementation is a reviewable proposal—not a settled API merely because tests passed.
