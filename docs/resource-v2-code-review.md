# Resource v2 implementation review

## Current status and historical verdict

The user accepted the SDK direction and authorized MCP alignment. The current
author-facing contracts are documented in [the API reference](resource-v2-api.md),
[custom tools guide](tools.md), and [capability profile](capabilities.md).
Acceptance is not merge, release, tag, publication, API-freeze, or SDK-completion
approval.

The findings below are a historical review of the explicitly identified cohort,
not a statement that every reproduced defect remains in current source. The
original verdict was to hold merge, reconcile mutation capabilities, fix the
eight reproduced defects, and review admission/lifecycle/transport choices.
Keep the dated evidence and original recommendations distinct from the current
contracts recorded in [the specification review](resource-v2-spec-review.md#implemented-candidate-contract-choices).

## Scope and evidence

Reviewed candidates:

- [SDK PR #21](https://github.com/omniviewdev/plugin-sdk/pull/21), local/published SDK head `a8d59c9179a4e021692ae97a23cc895ad22f3db0`.
- [Host PR #192](https://github.com/omniviewdev/omniview/pull/192), local head `0ec42cfe`.
- [Kubernetes PR #43](https://github.com/omniviewdev/omniview-plugin-kubernetes/pull/43), local head `35e2650`.

Paths below are relative to the cohort root; `plugin-sdk`, `omniview`, and `kubernetes` identify repositories. Line numbers refer to these review candidates. Existing host/Kubernetes worktree changes were left untouched.

Six parallel review facets covered public API/specification, Go lifecycle/concurrency, wire validation/adapters, SDK utilities, host/runtime integration, and Kubernetes/Helm integration. Parent review checked findings against source, rejected an unsupported YAML regression claim, and executed the reproductions below. The review is broad but not an exhaustive audit of every changed line. Generated code, remaining frontend callers, and complete UI behavior were not exhaustively reviewed.

Specification baseline:

1. `omniview/docs/superpowers/specs/2026-10-17-resource-foundation-design.md`, especially identity/access, scope, capabilities, and lifecycle.
2. `omniview/docs/superpowers/specs/2026-10-24-resource-query-contracts.md`, especially canonical C01 mapping and handoff.
3. `omniview/docs/superpowers/specs/2026-10-23-trusted-invocation-errors.md`.
4. SDK issue [#12](https://github.com/omniviewdev/plugin-sdk/issues/12), including later acceptance/handoff notes. Historical prerequisite-blocked labels are not treated as current blockers.

History ingestion, storage, search, timeline, the universal planner, and strong P05 continuity are explicitly outside this implementation's claimed completion. Their absence is not a regression finding.

## Behavioral findings

### R1 — P1: Preserve cancellation through forwarding creation and readiness

**Anchor:** `plugin-sdk/pkg/v1/networker/manager.go:194–196`; cancellation is detached at `213–218`.

`StartResourcePortForwardSession` holds the configured connection lease while calling `startPortForwardSession`. The latter detaches its context with `context.WithoutCancel` before invoking the forwarder. A forwarder blocked in native creation cannot observe caller cancellation or connection-generation retirement. The admission callback cannot return and release its lease; connection retirement can consequently wait indefinitely for a cooperative forwarder that never receives cancellation. The separate readiness timeout does not bound a callback blocked before returning `ForwarderResult`.

**Reproduced:** A real manager invoked a forwarder that waited on its received context. Canceling the request left creation blocked; manual probe release was needed. The forwarder context had its own `Done`, but cancellation no longer propagated from the caller.

**Fix:** Keep request and generation cancellation attached throughout native creation and readiness. Detach the accepted session's RPC lifetime only after admission succeeds. Preserve retirement cancellation and existing close/orphan cleanup behavior; do not fix this by shortening a timeout.

**Regression:** Block native creation until cancellation, cancel the request, and separately retire the configured connection. Both paths must release admission promptly and retain no session. An accepted session must still survive completion of its initiating RPC.

### R2 — P1: Prevent update data delivery before listener readiness

**Anchor:** `plugin-sdk/pkg/resource/v2/update_manager.go:203–208`; `updateListener.call` at `40–60`.

`listen` publishes the listener before `onReady` completes. State events have a replay barrier, but ADD/UPDATE/DELETE use `call`, which checks only retirement/cancellation. An already-running source can invoke the newly registered sink before readiness. The RPC update sink rejects this ordering, turning normal concurrent watch traffic into a stream failure.

**Reproduced:** Started a watch, blocked a new listener inside `onReady`, and emitted ADD. The listener received one data callback while readiness remained blocked.

**Fix:** Add an explicit readiness barrier for data callbacks as well as state handling. Preserve bounded memory and the advisory, non-replay semantics; do not invent an unbounded data replay queue or claim P05 continuity. Define how pre-ready advisory data is handled.

**Regression:** Join an active watch with readiness paused; emit ADD, UPDATE, and DELETE. No data callback may reach the transport before readiness. Rejected readiness must retire the listener and preserve existing state-replay behavior.

### R3 — P1: Align the node drain caller with grace-period validation

**Anchor:** `kubernetes/pkg/plugin/resource/resourcers/node_resourcer.go:238–243`.

The backend now accepts grace periods only in `[0, MaxInt64]`. The current node drawer still sends `gracePeriodSeconds: -1` at `kubernetes/ui/src/components/kubernetes/table/corev1/Node/actions.tsx:74–79`. Every drain initiated through that drawer fails before cordon or eviction.

**Reproduced:** Passed the drawer-equivalent canonical input to `NodeResourcer.ExecuteAction`; it returned `gracePeriodSeconds: integer exceeds native range` before using a native client.

**Fix:** Settle and migrate the sentinel contract on both sides. If `-1` means native pod defaults, translate it to absent Kubernetes grace-period options. Merely omitting it currently selects the backend's 30-second default, which may change the intended behavior.

**Regression:** Exercise the actual drawer-equivalent payload against action dispatch. Verify cordon/eviction receives the intended grace semantics, without weakening validation of other negative values.

### R4 — P2: Require a selected connection for route-bearing metric queries

**Anchor:** `plugin-sdk/pkg/v1/metric/resource_options.go:43–49`; shared fallback at `pkg/resource/v2/utility_context.go:20–24`.

Route validation only compares IDs when `PluginContext.Connection` exists. `WithUtilityConnection` invokes the callback directly when that connection and a canonical reference are absent. An explicit route-only `QueryResource` request therefore reaches native query code without registry admission or a configured connection.

**Reproduced:** Called the real metric manager with route `selected`, no configured context connection, and no registry. Its query callback ran with empty connection ID and nil configured connection; the manager returned nil error.

**Fix:** Reject explicit route intent without a selected configured connection at manager/server admission. Keep context-free payload encoding validation separate. Audit other callers of the shared helper for the same route-only fallback; do not require fabricated references/catalog pins for ordinary nonreference utilities.

**Regression:** Direct-manager and gRPC route-only queries without selected context must fail before native dispatch. A matching started connection must succeed; mismatched routes must still reject.

### R5 — P2: Own retained watch scope rather than sharing the caller's slices

**Anchor:** `plugin-sdk/pkg/resource/v2/update_manager.go:248–249`; provider invocation at `342`.

The stored `ReadTarget` shares `Scope` and `Values` with the caller. The provider callback also receives that shared target. Mutating the request after `EnsureResourceWatch` returns changes the scope used for event admission and watch reuse without a fresh admission.

**Reproduced:** Admitted an east-only watch, changed the caller's original value slice to west, and emitted a west record. The listener accepted it.

**Fix:** Deep-copy the admitted target's scope and value slices at retention. Give provider callbacks independent mutable containers, or explicitly enforce an immutable callback contract that cannot mutate retained admission. Apply the same ownership decision to retained representation/descriptor containers as appropriate.

**Regression:** Mutate both the original request and the callback's target after admission. Neither may alter retained scope, reuse decisions, or permitted event membership.

### R6 — P2: Recheck catalog admission before each native watch retry

**Anchor:** `plugin-sdk/pkg/resource/v2/update_manager.go:337–342`.

The retry loop checks context cancellation but does not recheck its retained catalog observation. Catalog metadata can change during backoff without retiring the native client binding. The next attempt invokes an obsolete registration/representation before any event-level fence can reject output.

**Reproduced:** Failed the first watch invocation, changed the catalog metadata revision during backoff, and observed a second native watch invocation under the old admission.

**Fix:** Fence the retained catalog before each native invocation, including retries. Finish the stale watch with an explicit reset-required outcome. Do not silently substitute a newer revision or reacquire under a different contract.

**Regression:** Change catalog revision during a controlled backoff. The second native callback must not run. An unchanged catalog must retain the existing bounded retry behavior.

### R7 — P2: Copy private credential containers independently of JSON visibility

**Anchor:** `plugin-sdk/pkg/resource/v2/connection_copy.go:125–130`.

Struct copying skips exported fields tagged `json:"-"`. Such fields can still contain mutable private credentials and require ownership isolation. JSON omission is not permission to share their backing slices/maps. This contradicts the documented independent ownership of private connection configuration.

**Reproduced:** Put a credential struct with `Tokens []string` tagged `json:"-"` into sensitive connection data. Mutating the result of `CloneConnection` changed the original token.

**Fix:** Separate copy policy from serialization visibility. Deep-copy exported mutable fields regardless of JSON omission while retaining the deliberate policy for opaque native handles/unexported implementation state. Do not use JSON roundtripping.

**Regression:** Exercise controller ingress, inventory egress, and callback snapshots with hidden credential containers. Snapshot edits must not alter stored configuration; an explicit meaningful credential update must still retire the old client.

### R8 — P2: Honor empty `oneOf` selections in Helm enumeration

**Anchor:** `kubernetes/pkg/plugin/helm/register.go:148–149,173–174`.

`queryScope` rejects empty repository and identity-scope `oneOf` selections. Foundation §3.1 and canonical query §1 define empty `oneOf` as matching nothing, not invalid input. SDK admission permits it; Helm enumeration then fails.

**Reproduced:** Empty repository-domain selection returned `Helm enumeration requires any scope or one exact native scope`. Empty chart-repository selection returned `repository query requires any or non-empty oneOf`. Both failed before native acquisition.

**Fix:** After validating target shape, short-circuit any valid empty selection into a successful empty source page before native acquisition. Preserve explicit coverage/consistency and no continuation; do not reinterpret empty selection as `any`.

**Regression:** Cover release namespace, repository domain, and chart repository empty selections. Assert zero records, nil continuation, and no native requests/filesystem acquisition.

## Specification reconciliation

### S1 — Mutation support is too coarse to express independent verbs

**Anchor:** `plugin-sdk/pkg/resource/v2/domain.go:103–110`.

Foundation §4 requires independently supported create/update/delete operation contracts. The candidate catalog exposes only `mutation`, while `MutationRequest.Operation` selects a separate string. Controller admission maps every mutation to the same capability (`controller.go:598–600`). Native Kubernetes advertises mutation when any native verb exists (`native_catalog.go:111–115`); Helm advertises mutation but rejects update (`helm/register.go:461–470`).

Native rejection prevents unsupported verbs from actually executing. This is a discovery/admission specification gap, not proof of unauthorized native writes. Current comments describe the coarse design, so it requires explicit reconciliation rather than treating it as an accidental typo.

**Required decision:** Represent supported mutation verbs/contracts in the catalog and admit the requested verb, or explicitly approve a narrower specification. Keep one optional `Mutator`; do not add mandatory CRUD methods or separate RPC services per verb.

**Acceptance:** A create-only provider advertises only that support. Update/delete admission returns `UNSUPPORTED` before its callback. Runtime/UI support decisions use the same declarations.

## Why the code feels difficult to read

The main problem is not comment density or file length alone. Readers must reconstruct temporal contracts across controller, context helpers, RPC middleware, utilities, and host consumers. Public provider contracts and internal framing sit in the same namespace. Large dispatchers then combine unrelated workflows, obscuring where those contracts are enforced.

A Go AST probe over handwritten `pkg/resource/v2` and its plugin subpackage, excluding tests, measured 42 files and 388 exported functions/types; 299 lacked declaration comments. This excludes exported constants and fields and is not a lint threshold. It indicates an API-surface audit is needed before adding prose everywhere.

Measured hotspots:

| Function | Lines | Branch statements |
| --- | ---: | ---: |
| `plugin/ancillary_server.go:78 call` | 286 | 81 |
| `plugin/wire.go:46 message` | 219 | 68 |
| `plugin/server.go:114 dispatch` | 163 | 45 |
| `controller.go:1458 applyInventory` | 98 | 25 |

Branch statements count `if`, `for`, `range`, `case`, and select clauses; this is not a formal cognitive-complexity or cyclomatic score. A wire grammar switch can legitimately be large. The mixed ancillary dispatcher is a better extraction candidate than blindly splitting every long function.

## Organization and API documentation recommendations

These are maintainability recommendations, not additional runtime defects. Prefer cohesive files in the existing packages before introducing more packages, exported interfaces, or generic frameworks.

### A1 — Write a normative API and structure overview before freezing the major

**Anchors:** `plugin-sdk/pkg/resource/v2/domain.go:1–3`, `README.md:159–531`, and `docs/resource-v2-spec-review.md:34–52`.

The package comment describes the domain but not its operational API. The README contains useful contracts, but spreads provider construction, utility protocol fields, catalog metadata framing, lifecycle, and host policy across one long section. The comparison document explicitly identifies unsettled additions; it is not the missing top-level contract.

Create one navigable architecture/API document, linked from a substantial Go package overview. It should specify:

- A responsibility map: provider domain, optional controller, connection registry, transport, host admission/authority, runtime consumers.
- A deliberate exported API inventory: provider-author API, host/integration API, transport internals.
- Minimal catalog-only and get-only provider examples; a controller lifecycle example; a custom-provider generation example. Examples must compile against the real API.
- One route-qualified admission model, or a documented reason to retain both default and qualified helpers. Include duplicate/precedence rules and why pins are observations, not grants.
- Connection state transitions, startup failure/retry, overlay apply/failure, generation retirement, and exact cancellation ownership.
- Lock ordering, callback concurrency/reentrancy restrictions, ownership of mutable inputs/results, and who closes channels/streams.
- Utility creation lifetime versus accepted-session lifetime. Explain `WithConnection` (configured profile) versus `WithClient` (registered native client).
- Error vocabulary and public-safe text obligations; schema state, count, coverage, cursor, and empty-selection semantics.
- Candidate wire ceilings separately from lower planner/query policy and explicitly excluded engines.

Use the overview for stable concepts and declaration comments for local contracts. Avoid restating field names or narrating implementation. Retained exported declarations should follow [Go doc comment guidance](https://go.dev/doc/comment), especially concurrency, zero/nil behavior, ownership, cancellation, and error semantics. For `ResourceOperationError`/`NewError` (`errors.go:12–17,38`), explicitly state that titles/messages/suggestions must be public-safe; raw native details belong in `Err`. Host redaction remains authoritative.

### A2 — Split the controller by responsibility without changing its state owner

**Anchor:** `plugin-sdk/pkg/resource/v2/controller.go`, 1,648 lines.

Keep one controller and registry. Move cohesive groups to focused files in `resourcev2`:

- Construction/state and classification: `Controller`, `clientSlot`, `NewController`, `guard`.
- Client leasing/lifecycle: `routeContext`, `lease`, `WithClient`, `WithConnection`, start/stop/readiness/close.
- Catalog and finite-operation admission: registration matching, scope helpers, operation description/admission, record/page checking.
- Finite dispatch: `Get`, `Enumerate`, `NativeQuery`, `Mutate`, `Action`.
- Connection inventories/overlays: snapshot/apply/update/delete.

Document registry lock order and cancellation/lease invariants beside the state owner. Keep retirement fences visible in dispatch. Do not turn each file into a new interface or create a second registry. Constructor registration/lifecycle ownership currently remains unspecified: either own snapshots, including nested scopes, or explicitly document immutable-after-construction inputs.

### A3 — Make ancillary RPC dispatch a router, not a workflow container

**Anchor:** `plugin-sdk/pkg/resource/v2/plugin/ancillary_server.go:78–363`.

Extract connection operations (`nativeConnections` through `deleteConnection`), schema/presentation operations, and relationship/health operations into private cohesive handlers. Keep `call` as an explicit operation router. Keep session setup, pre/post admission fences, classification, and response validation centralized in `ancillaryCallHandler:31–46`.

Acceptance: each operation retains its exact trait check, payload validation, response variant, and error behavior. No handler may bypass the shared boundary. Avoid a generic registration framework unless a concrete need appears.

### A4 — Keep binary framing out of the provider domain API

**Anchor:** `plugin-sdk/pkg/resource/v2/route_catalog_revision.go:69–96,144–146`.

`MarshalCatalogRevisionRoutes`, `UnmarshalCatalogRevisionRoutes`, and `ClearCatalogRevisionRoutes` are consumed in production only by SDK transport `plugin/catalog_route_session.go:19–46`. Move byte framing/context boundary clearing behind transport/internal ownership. Expose validated admission observations rather than header layout.

Do not indiscriminately hide all helpers. Host uses `CatalogBindingEpoch` and qualified pins; custom providers need generation issuance; utilities consume conversions. Host `subscriptions.go:50–58` currently uses exported protobuf adapters for fingerprinting. Migrate that host dependency to domain-owned normalization/fingerprinting before privatizing its adapters. Preserve direct/RPC parity, duplicate rejection, exact observation selection, and aggregate limits.

### A5 — Make handwritten wire contracts readable; separate adapter machinery

**Anchors:** `plugin-sdk/proto/resource/v2/domain.proto:18–45`, `ancillary.proto:8–24`; `pkg/resource/v2/adapters.go:25–90,634–661`.

Expand handwritten protobuf messages and oneofs into field-per-line blocks. Document semantic absence, duplicate-preserving repeated entries, operation/payload correspondence, reserved tags, and readiness frames. Preserve all wire numbers/types/optionality/services and regenerate through the repository's existing generation workflow. Never hand-edit generated Go.

Move generic `admitProto`, `toProto`, and `fromProto` guards into a private adapter-admission file, leaving model conversions grouped by domain. Do not delete raw-wire validation or boundary checks merely because semantic validation also exists: generated decoding can erase duplicates, and custom RPC providers/direct controller callers need independent protection. Distinguish protobuf-tree, JSON-tree, and semantic-value depth budgets.

### A6 — Separate Kubernetes/Helm composition from acquisition and mutations

**Anchors:** `kubernetes/pkg/plugin/helm/register.go:1–575`; `pkg/plugin/resource/native_provider.go:106–244`.

For Helm, keep construction/registration in `register.go`; move request/scope/paging helpers into `request.go`, catalog/representation/definitions into `catalog.go`, reads/index loading/enumeration into `read.go`, and action/mutation workflows into `operations.go`. Keep package-private helpers and the existing interface; add no new abstraction. Fix R3/R8 before moving code so regressions remain attributable.

For native provider startup, extract descriptor/snapshot assembly from `startNativeConnection`. Keep cancellation, lease ownership, load-token comparison, retirement, and final publication together. Split schema/presentation methods from relationship/event methods in `native_ancillary.go`. Preserve the single publication safety boundary rather than distributing lifecycle checks across generic callbacks.

### A7 — Align host files/components with separate responsibilities

**Anchors:** `omniview/backend/pkg/plugin/resource/finite_service.go:92–181`; `ui/components/tables/Resources/index.tsx:38–98,184–240`.

Move catalog admission capture/resolution together into the existing catalog-binding implementation, away from finite-service DTOs. Preserve epoch capture before catalog acquisition and the `pluginsMu` then `finite.mu` lock order during authority assignment.

Split get-only lookup from enumerable table mode. Keep capability selection in the parent; put lookup form/query in one component and enumeration/paging/watch in another. Lookup must not acquire enumeration/watch requirements. Preserve full target context when resetting paging.

## Suggested fix order and review acceptance

1. Add narrow failing-before/passing-after regressions for R1–R8 and fix behavior without structural churn. Combine related watch cases in the owning suite, not one huge cross-repository test.
2. Reconcile S1 and approve the top-level public API/ownership/lifecycle contract. Resolve the admission surface before freezing it; preserve all current safety fences.
3. Audit exports using real callers. Hide transport-only machinery with a clean caller migration, not aliases or compatibility shims.
4. Perform same-package controller/dispatcher/Helm extractions in separate reviewable commits. No semantic changes in extraction commits.
5. Add meaningful Go declaration docs and compiling public examples. Document decisions at their owning declarations; link rather than duplicate README prose.
6. Run focused regressions, existing conformance/normal/race suites, and the real active utility/app scenarios affected by fixes. A passing source suite alone is not new-major acceptance.

## Checks executed during this review

All commands used `GOWORK=off`.

- SDK: `go test ./pkg/resource/v2/... ./pkg/v1/exec/... ./pkg/v1/logs/... ./pkg/v1/metric/... ./pkg/v1/networker/... -count=1` — passed, nine packages.
- SDK: `go test ./internal/resourceconformance -count=1 -v` — passed. Observed child-process startup, real gRPC canonical finite reads, cancellation, and generation-fence cases; this was not only a codec fixture run.
- SDK: `go vet ./pkg/resource/v2/... ./pkg/v1/exec/... ./pkg/v1/logs/... ./pkg/v1/metric/... ./pkg/v1/networker/...` — passed without diagnostics.
- Host: `go test ./backend/pkg/plugin/resource/... ./backend/pkg/invocation/... -count=1` — passed, five packages. Linker emitted macOS deployment-target warnings.
- Kubernetes: `go test ./pkg/plugin/resource/... ./pkg/plugin/helm/... ./pkg/utils/kubeauth/... -count=1` — passed, five tested packages plus four without tests.
- Three temporary `go run` probes exercised real public managers/controller/action entry points and reproduced R1–R8. No production cluster was contacted or mutated. Watch probes observed pre-ready delivery, caller-owned scope alteration, stale retry invocation, and hidden credential aliasing directly.

Existing suites passed despite these uncovered behaviors. No fixes were applied in this review. Full race suites, UI/browser verification, and a complete managed-app/kind smoke were not rerun. Previous session receipts were read as historical evidence, not reported as checks executed here.

## Deliberately excluded claims

- YAML edited-scalar inference is a design tradeoff, not a proven regression: current host docs promise original kinds only for untouched scalars. Compatible-kind preservation or explicit tags may improve the editor, but must preserve intentional category changes.
- Large files, repeated defensive validation, string discriminants in a wire model, and generated file size are not independently correctness defects.
- No claim of hostile-plugin isolation, complete query planning, source continuity, or production historical-engine completion is made.

## Published review annotations

- [SDK review](https://github.com/omniviewdev/plugin-sdk/pull/21#pullrequestreview-5706085884): six targeted inline comments, with the three watch findings grouped into one thread.
- [Kubernetes review](https://github.com/omniviewdev/omniview-plugin-kubernetes/pull/43#pullrequestreview-5706086314): two targeted inline comments plus organization guidance.
- [Host review](https://github.com/omniviewdev/omniview/pull/192#pullrequestreview-5706086527): focused organization and integration guidance, without speculative bug annotations.

These reviews were posted as comments, not approval or formal change-request events. The historical recommendation was to hold merge. Temporary reproduction programs were removed after their observations were recorded. Subsequent SDK direction acceptance does not itself authorize merge or release.
