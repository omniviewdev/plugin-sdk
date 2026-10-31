# MCP integration with resource v2

**Status: SDK direction accepted; coordinated merge and release authorized.**

The MCP relay should consume the same host resource services as the UI. Plugins should not implement a second resource engine for agents. Custom SDK tools remain an extension lane for non-resource work.

This catalog follows [Alex Mononen’s PR21 comment][alex], [SDK issue25][sdk-issue], [host issue219][host-issue], and [Kubernetes issue62][kube-issue]. Eight parallel domain audits inspected their implementation evidence and the relay review.

**Coverage:** 33 issue sections/feature rows, 16 inline review findings, and three additional review notes. These are **52 source items**, not 52 independent defects. Overlapping reports share work packages below.

> [!IMPORTANT]
> The user authorized the coordinated SDK, host, MCP and Kubernetes merge/release sequence. Publication still requires matching released dependencies, release checks and final live-product verification. Administrative merge bypass is explicitly authorized where needed; repository rules remain unchanged. Historical implementation-wave receipts below retain the authorization and acceptance state at the time they were recorded.

## Read this plan

| Task | Section |
| --- | --- |
| Understand the intended integration | [Target flow](#target-flow) |
| Assign implementation ownership | [Work packages](#work-packages) |
| Find decisions that need approval | [Contract decisions](#contract-decisions) |
| Trace every original finding | [Source checklist](#source-checklist) |
| Separate later AI work from landing | [Roadmap disposition](#roadmap-disposition) |
| Plan execution and verification | [Landing order and evidence](#landing-order-and-evidence) |

## Target flow

**Candidate integration:**

```mermaid
flowchart TB
    Agent[MCP client] --> Relay[MCP relay]
    Relay -->|host-issued agent scope| Admit[Host invocation boundary]
    UI[UI and other trusted consumers] -->|own trusted scope| Admit
    Admit --> Resource[Host resource service]
    Admit --> Custom[Custom non-resource tool]
    Resource --> Provider[Resource v2 provider]
    Provider --> Registry[One SDK native-client registry]
    Custom -->|when native access is required| Registry
```

The invocation boundary owns authorization. A catalog declaration describes support; a revision records an observation; a confirmation records consent. None substitutes for the others.

A client-free provider does not need a native client. A plugin-scoped custom tool does not need a fabricated connection or resource catalog.

### What the original audits corrected

- `Host` is invocation provenance, not a privilege ranking. The inspected baseline gave agent calls host provenance and composed the resource boundary without an authorizer; the candidate now issues agent scope through one authorizer.
- Readiness is not a lease or network-health probe. Require registered-generation admission for native work, not universal Kubernetes-style `CONNECTED` checks.
- Full custom-tool names already receive a host length/character check. Built-in names and reserved-argument collisions remain gaps.
- `Dangerous:false` does not prove read-only, non-destructive, or idempotent behavior. MCP annotations are hints, not authorization.
- Catalog CRUD does not replace label filtering or bounded logs. Existing `get_pod_logs` advertises a first-container default that its handler does not implement.
- Kubernetes resource error producers put native diagnostics in public fields. **Normal resource gRPC already strips that text.** Legacy tool errors leak directly; asynchronous logs error events need separate safe handling before MCP consumption.
- P01’s numeric contract/design-spike handoff was accepted. Older numeric holds are historical; that acceptance does not establish an installed universal query service or approve the SDK API.

## Work packages

Ownership below names repository roles, not assigned people. Source writers must own nonoverlapping files. The shared host integration owner serializes changes to `main.go`, `internal/mcprelay/server.go`, controller composition, and generated consumers.

### W1 — Agent admission, consent, and readiness

**Owner:** host invocation/security. **Files:** `backend/pkg/invocation`, relay policy/confirmation, admitted connection inventory, composition.

**Audit-baseline evidence:** [invocation origins and admission][invocation]; relay `server.go:300–315,398–469`; v2 `backend/pkg/plugin/resource/invocation.go:15–57`. The inspected composition used `NewBoundary(nil)`; the implementation now supplies the relay authorizer.

- Issue host-owned external-agent provenance after bearer authentication. Never trust MCP client names, requester strings, tool arguments, or connection IDs as identity.
- Configure one authorizer. Treat persisted relay policy as input to that boundary, not an independent permission grant. Preserve native settings-management access separately from agent-visible inventory.
- Admit the actual resource operation/target. For custom tools, use the real boundary shape: `Target.Provider`, `Target.Action`, and a separate operation argument—not the issue’s illustrative nonexistent fields.
- Filter broad inventory per permitted member. Return readiness truthfully; never implicitly start or select a connection.
- Bind confirmation to immutable arguments, caller owner, route/reference/action, catalog observation, relevant generations, and supported write preconditions.
- Recheck authorization and target validity after approval, immediately before dispatch. Define policy-update/dispatch ordering; another unlocked config snapshot alone is not a complete race contract.
- Retire pending consent and accepted work across relevant plugin, route, listener, and credential changes. Consent never transfers silently to a replacement implementation.
- Record correlated admission, consent, dispatch, and outcome evidence with deliberate argument/token redaction.

**Accept when:** denial wins even after approval; policy revocation while a prompt waits causes zero provider calls; stop/reconfigure between discovery and dispatch fails closed; client-free tools remain usable without invented native readiness.

### W2 — SDK custom-tool routes and native ownership

**Owner:** SDK tools/runtime. **Files:** `pkg/v1/tools`, `pkg/sdk/plugin.go`, `proto/v1/tools/tools.proto`, affected consumers and docs.

**Evidence:** [existing capability binding][sdk-binding]; tools `plugin.go:23–32`, `manager.go:80–99`; resource `utility_context.go:36–96` and `controller_lifecycle.go:31–82`.

- The audit baseline lacked both transport-level connection-consumer forwarding and manager-side utility admission. The replacement supplies both; forwarding alone would not fix dispatch.
- Specify explicit connection-route semantics, then migrate request types, transport, callers, and documentation together. Reject empty routes and conflicting selectors before handlers run.
- Replace caller-supplied connection data with the sole configured registry snapshot. Use the existing controller’s typed `WithClient` within the accepted callback context; a `ConnectionContextProvider` does not itself return a typed native client.
- Preserve cancellation, retirement drain, and post-dispatch result fences. Nested native access must retain the same slot rather than switch to a replacement generation.
- Keep three observations distinct: **plugin process generation**, **native-client generation**, and **resource catalog revision**. `CatalogBindingEpoch` is not all three.
- Fence custom dispatch against plugin replacement even when no native client exists. Opaque custom tools need no fabricated resource pins.
- Decide whether route-dependent custom tools need their own discovery observation. A general dynamic-tools subscription RPC is not required merely to expose resource CRDs through the host catalog.
- Preserve connection-free plugin tools, caller cancellation, and supported result contracts. Regenerate protobuf with `make proto` after an accepted wire change; never hand-edit generated Go.

**Accept when:** forged profile data cannot select credentials; unstarted native routes never run handlers; retirement cancels work and discards late success; either capability-registration order produces actual admitted native behavior; plugin-only tools work without a resource provider.

### W3 — Schemas, annotations, errors, and exact arguments

**Owner:** SDK protocol and host conversion. **Files:** tools types/protobuf/client/server, resource ancillary schemas, relay `catalog.go`, validation/error mapping.

**Evidence:** [SDK tool validation][tool-validation]; [resource error wire policy][wire-errors]; relay `catalog.go:99–166`, `server.go:370–482`; [MCP 2025-11-25 tools specification][mcp-tools].

- Validate complete input/output schema declarations before publication. Validate inputs before native work and structured outputs against any declared output schema.
- Preserve the existing restricted action schema honestly. It cannot recover absent `items`, unions, arbitrary enums, or other missing assertions. Do not silently drop constraints or invalid output schemas.
- Decide supported dialects, reference resolution, and `format` policy. Do not fetch external schema references automatically. Do not apply defaults or coerce input values during validation.
- Reject malformed/duplicate JSON independently from schema assertions. Preserve raw-wire/domain validation and protobuf presence distinctions for any accepted new fields.
- Preserve remaining argument tokens when extracting `connection_id`. The relay currently decodes through `map[string]any` and re-marshals, which can alter large integers.
- Reserve injected arguments before mutation; validate the final exposed schema. Check built-in and custom names and report collisions explicitly. MCP itself does not reserve `connection_id`.
- Preserve annotation absence versus explicit false. MCP defaults are read-only=false, destructive=true, idempotent=false, open-world=true. Choose the Go/wire presence shape explicitly; do not infer safety from `Dangerous:false`.
- Carry reviewed custom-tool error classifications with safe host presentation. Keep raw diagnostics local. Distinguish protocol failures, transport failure, cancellation/deadline, and successful RPCs carrying tool execution errors.
- Preserve existing resource gRPC sanitization. A typed error structure alone does not sanitize arbitrary text fields or asynchronous log events.

**Accept when:** malformed nested schemas and duplicate required names cannot publish; invalid inputs cause zero handler calls; declared output mismatches never escape as success; omitted/explicit-false annotations survive transport; secret-bearing failures stay out of model-visible text and structured fields.

### W4 — Catalog-derived resource tools and result contracts

**Owner:** host resource/MCP integration. **Files:** relay catalog/handlers/results, resource finite/reference/ancillary admission, logs consumer.

**Evidence:** [typed host resource service][host-service]; [catalog admission][host-finite]; relay `server.go:318–365`; resource v2 `domain.go:268–335`.

| Existing Kubernetes tool | Host path | Required replacement behavior |
| --- | --- | --- |
| `get_resource` | `ResolveReference` → `Get` | Exact route/reference/representation; scoped Get capability; intentional body projection. |
| `list_resources` | `ResolveTarget` → `Enumerate`, or an accepted host query path | Explicit source-page semantics, selectors, scope, finite budgets, continuations, coverage. |
| `delete_resource` | Resolve exact reference → `Mutate` with delete | Independent delete capability, consent/admission, incarnation and supported revision preconditions. |
| `list_namespaces` | Negotiated `ScopeValues(route, "namespace")` | Kubernetes adapter behavior, not a namespace requirement in generic SDK. |
| `get_pod_logs` | Admitted logs session → bounded headless consumption → close | Finite non-following output, cancellation, aggregate cap, safe errors, lifecycle ownership. |

- Generate only supported, admitted operations. Get/enumerate, create, mutation-update, delete, advisory update, and actions are independent.
- Retain each advertisement’s route/catalog/plugin observation through dispatch. Current host catalog admission reacquires a catalog and installs a fresh default pin; merely passing an old pin does not preserve advertisement semantics.
- Bind continuations to their original target, route, representation, and source generation. Never silently repin a stale tool or cursor.
- Treat plugin/type/action IDs as opaque. Choose bounded collision-safe names; separate resource operations from action IDs. Punctuation normalization is not identity encoding.
- Define route-specific schemas without leaking one connection’s catalog into another. Replace same-name descriptors and handlers, not only names.
- Preserve tagged `Value` kinds in resource inputs/results: signed/unsigned decimal strings, finite doubles including negative zero, nanosecond timestamps, null, and empty containers. Raw JSON bytes alone do not prove consumer fidelity.
- Specify a host-owned resource result envelope: reference, route, representation, record/page source revisions, coverage, count kind/value, cursor, consistency, and explicit output truncation. Keep these revisions distinct from incarnation and host row keys.
- Reauthorize cited resources on later graph/history traversal. Basic provenance is needed now; a full history adapter is not implied.
- Advertise actions only with a faithful schema and supported dispatch lane. Type-scoped actions currently lack a type-only request shape; never fabricate an object ID. Streaming actions must not call unary dispatch.
- Add bounded headless logs consumption through existing sessions, not a second logs engine. Resolve SDK issue23’s relevant lifetime decision before claiming retirement-safe parity.

**Accept when:** two routes with different catalogs cannot borrow capabilities; stale advertisements fail rather than refresh silently; delete-only support exposes no other writes; exact scalar kinds survive a real MCP client; incomplete pages stay incomplete; finite logs close on completion, cancellation, errors, and limits.

### W5 — Kubernetes clean cutover and parity

**Owner:** Kubernetes adapter. **Files:** `pkg/main.go`, `pkg/plugin/tools`, resource catalog/read/mutation/error adapters, logs, `plugin.yaml`.

**Evidence:** [merged five-tool implementation][kube-tools]; v2 `native_read.go:68–108`, `native_mutation.go:18–63`, `native_catalog.go:26–134`; [error producer][kube-errors].

- Preserve metrics-first registration: `retireMetrics := pluginmetric.Register(plugin)` followed by `resource.Register(plugin, retireMetrics)`. Keep the host’s metric pre-change fencing hook.
- Do not add a tools-owned client cache. Existing resource tools construct a complete Kubernetes client stack per call, outside the shared registry.
- Preserve meaningful behavior before removing all five tools: label selectors, explicit namespace rules, served API-version handling, managed-field projection, pagination, bounds, and bounded pod logs.
- Existing omitted list namespace means all namespaces; Get/delete require namespace for namespaced kinds. Neither may widen configured or authorized scope.
- Existing list cap is 500; v2 source pages cap at 256 and currently do not forward label selectors. Aggregate through bounded accepted semantics or get an explicit scope decision; do not silently drop filters or raise provider ceilings.
- Existing logs default tail is 200, clamp at 5,000 lines, and cap output at 1 MiB. Empty container is delegated to Kubernetes despite descriptor wording. Define selection and timestamp differences explicitly.
- Sanitize resource producer public fields while retaining native causes in `Err`. Also sanitize asynchronous log error events before exposing them to MCP; ordinary resource-wire sanitization already exists.
- A connection-qualified startup catalog is not proof of live CRD watching. Define refresh triggers and emitted tool-list changes honestly.
- After replacement acceptance, delete the obsolete tools implementation/tests, import/registration, and manifest capability. Keep shared utilities still used by other lanes. No empty registration shims.

**Accept when:** all five replacements preserve agreed observable behavior; stale UID/revision cannot delete a replacement object; native work uses admitted generations; per-cluster CRDs differ truthfully; metrics and utility cleanup still retire correctly. None of the existing five tools requires permanent custom-tool retention.

### W6 — Relay/controller lifecycle and persistence

**Owner:** host tools/lifecycle. **Files:** tools controller, plugin loader rollback, relay listener/resync, connection inventory publishers, existing store integration tests.

**Evidence:** relay tools `controller.go:105–169,203–235`; relay `server.go:188–255,318–365`; v2 connection publication and metric hook; inline findings in the source checklist.

- Bound startup discovery and cancel it with its owner generation. Reject late completion; clear stale providers/descriptors on replacement failure and roll back partial startup if another capability later fails.
- Fence captured calls and results across plugin replacement. Deleting a cache entry is not cancellation or a result fence.
- Bound authenticated POST body bytes and body-read time, including chunked bodies. Clear body-specific deadlines after reading; do not truncate SSE or two-minute confirmation with a blanket write/request timeout.
- Serialize whole listener transitions. Stop accepting, cancel pending work, bound session drain and HTTP shutdown, and close remaining connections after the chosen grace period.
- Serialize complete resync before snapshot through MCP publication. Replace same-name definitions using supported replacement behavior. Do not claim that a resync mutex makes all tool registrations an atomic batch.
- Publish committed connection inventory changes after relevant locks release. Cover discovery, explicit profile mutations, load/replacement/removal, and unload. The metric hook is **pre-change fencing**, not a replacement post-commit notification.
- Own subscription cleanup; stopped services must not republish from late callbacks. Emit MCP list-change notifications after effective publication, not only UI events.
- Resolve the `Listed` mismatch deliberately. Current global enablement can advertise without an accessible connection, but dispatch still rejects disallowed connections; this is not a proven authorization bypass.
- Exercise real bbolt close/reopen with nested policies and nil/explicit-false overrides. Place the integration test where it does not create a store↔relay import cycle.

**Accept when:** hanging discovery/shutdown stays bounded; failed replacement leaves no callable stale tools; concurrent edits publish latest state; old tokens/listeners retire; inventory changes refresh without unrelated settings edits; nested effective policy survives reopen.

### W7 — Author-facing settings and confirmation UI

**Owner:** host UI/runtime bindings. **Files:** MCP hook, settings page/router, confirmation provider, app/window composition, generated runtime consumers.

**Evidence:** relay `ui/App.tsx:103–109`, MCP hook `:28–68`, confirmation provider `:34–74`; seven inline UI comments and one review note below.

- Make consent reachable with only a panel window visible; choose window ownership rather than assume a hidden workspace is usable.
- Prevent stale status queries from overwriting newer events. Invalidate connection queries from the same committed inventory signal used by relay discovery.
- Distinguish failed status/config/inventory/tools queries from stopped, empty, or absent states. Do not enable configuration writes from a failed authoritative config load.
- Prevent delayed initial pending snapshots from restoring resolved requests. Preserve actionable requests after failed decision submission; expose pending-list load failure and explicit recovery.
- Reject unsupported AI Tools focus-target navigation without breaking normal navigation.
- Show real route-scoped tool rows, independent capabilities, readiness, effective inherited policy, and hint-versus-authorization distinctions.
- Display trustworthy origin/owner, exact target, and meaningful arguments for consent. A client-supplied display name is not authenticated identity.
- Preserve nullable override inheritance and exact values. Regenerate changed bindings through normal generators; no current binding mismatch was demonstrated.

**Accept when:** delayed query/event order stays truthful; failed Allow/Deny can recover; resolved/expired requests never resurrect; two scopes render different real rows; panel-only users can approve/deny; fetch failures never masquerade as an empty catalog.

### W8 — Composition, cohort, release policy, and roadmap ledger

**Owner:** one cross-repository integration owner and one CI/release-policy owner.

- Keep both host insertions: `resourceController.SetConnectionChangeHook(metricController.BeginConnectionChange)` and tools controller construction. Preserve resource-v2 and tools loader registration.
- Reconcile Kubernetes registration as W5 describes. Report historical conflict receipts as historical. Issue62’s prose baseline and command output disagree; no fresh conflict count is claimed here.
- Record both committed dependencies and effective managed replacements. Relay requires SDK `v0.8.0`/Go `1.26.0`; committed host-v2 and Kubernetes-v2 require SDK `v0.7.1`/Go `1.25.0`. Existing tags do not constitute resource-v2 acceptance.
- Decide CI trust policy explicitly. [Alex’s release-gate explanation][ci-comment] says PR218 gives up the separately reviewed isolation-core baseline in CI, replacing it with release-tag publisher trust. That is not merely a version bump.
- Reconcile resolver, both workflows, reviewed candidate path, source cohort, Kubernetes gitlink, cache fixtures, and toolchain as one reviewable integration. Candidate testing must not force publication of an unaccepted SDK.
- Only after explicit future API acceptance and release authorization: select publication versions, publish compatible artifacts, and update downstream modules/checksums/cohort. This plan selects no next tag.
- Name the relay as a consumer of existing resource/invocation seams in the roadmap. Do not retroactively mark query, history, diagnostics, or autonomous writes implemented.
- Link later P01 acceptance where historical holds remain. Do not reopen accepted foundation decisions or require an upstream CLA/PR that later acceptance made optional.

**Accept when:** an explicitly authorized candidate cohort can be tested without misleading release claims; release validation resolves the authorized published SDK and exact plugin cohort; isolation-core trust has an explicit approval record; managed workspaces retain their replacements.

## Contract decisions

The user accepted the SDK direction. The table below remains the implementation decision ledger, not a claim that every detailed MCP contract or feature is already implemented. Selected first-wave rules are recorded immediately below it.

| Decision | Resolve before exposing the affected behavior |
| --- | --- |
| D1 — Caller ownership | Shared credential versus per-session/per-client principal; confirmation ownership; token/session retirement. |
| D2 — Custom tool routes | Body route, metadata conflict/missing-context rules, plugin scope, registry-free tools, and native-only tools without a resource capability. |
| D3 — Observations and refresh | Retained resource advertisement pins; custom discovery tokens only where needed; per-route schema variants; refresh signals and publication ordering. |
| D4 — Schema policy | Honest restricted action projection versus expanded schema API; dialect/ref/format policy; absent-schema behavior; input/output validation. |
| D5 — Safety and consent | Annotation presence/defaults; trusted operation classification; consent recheck ordering; required agent-write preconditions and audit redaction. |
| D6 — List parity | Source page versus host query, selector lowering, explicit API versions, projection/coverage, overall limits, and opaque names. |
| D7 — Results | Tagged resource envelope and citations versus unchanged custom JSON results; truncation/count/cursor semantics. |
| D8 — Actions and logs | Type-only action target contract; streaming handle owner/cancel rules; finite logs selection/budgets; issue23 session-generation ownership. |
| D9 — Discovery readiness | Usable-only versus status-bearing unavailable routes; unsupported readiness for native work; `Listed` zero-connection behavior. |
| D10 — CI trust and publication | Independent reviewed isolation baseline versus tag-derived trust; approved candidate path; later publication authorization. |
| D11 — Confirmation windows | Visible workspace activation versus eligible panel ownership; truthful principal/target display. |
| D12 — Legacy tool-peer admission | Negotiate the explicit-route/native-admission tool contract and reject unsupported peers; a new protobuf field alone does not establish support. |

### Historical first-wave decisions

- **Resource versus custom work:** resource operations use host resource services; custom plugin tools remain for non-resource work. The first wave did not implement the resource-tool generator; the later implementation does.
- **Explicit custom route:** connection-scoped calls carry an optional canonical `AccessRoute` field in the body, required and nonempty for that scope. A body route can select a connection without metadata; conflicting metadata is rejected. Metadata never supplies authoritative configuration.
- **Connection-free plugin scope:** plugin-scoped calls accept neither a body route nor selected connection metadata. No default route is inferred.
- **Native ownership:** connection-scoped handlers use the existing bound connection-context provider and registry. Missing admission support fails closed; no tools-owned client registry or fabricated resource catalog is introduced. Native-client reuse remains the adapter's responsibility through its existing typed controller.
- **Lifecycle:** handler execution and accepted unary results remain fenced by the leased generation. A callback lease ends on return; this does not redefine asynchronous log/session ownership.
- **Wire cutover:** migrate SDK transport and host custom-tool callers together. Do not reconstruct missing body routes from metadata as a compatibility fallback.
- **Candidate composition:** port the MCP feature onto an isolated resource-v2 host branch, preserving the metric hook and current resource services. Keep the separately reviewed CI isolation baseline; do not import the release-tag trust replacement.
- **Historical scope:** authorization/catalog generation, annotation/schema expansion, result envelopes, UI recovery, and remaining lifecycle items were left for later implementation waves. A working candidate composition alone was not production acceptance.

Do not automatically retry writes after retirement, timeout, or post-dispatch rejection. A rejected result is not proof that a native side effect rolled back.

### Pre-merge implementation decisions

The user authorized completing the remaining integration before any PR merges. The following choices guide that work; implementation and acceptance evidence must still be recorded.

| Area | Selected contract |
| --- | --- |
| Caller ownership | Bearer possession authenticates a shared credential, not a client-supplied name. An immutable authenticated credential receipt survives MCP initialization; each RPC receives fresh host-owned external-agent scope tied to the actual server session. Session DELETE cancels admitted work before transport drain. |
| Authorization | One invocation boundary admits the actual operation and target. Policy supplies restrictions; annotations never grant permission. Custom tools require explicit enablement and conservative consent. |
| Consent | Bind immutable arguments and target observations. Recheck current policy and generation at dispatch under a defined ordering; cancel retired work and reject late results. |
| Tool protocol | Use the exclusive `omniview.sdk.tools.v2` RPC namespace and explicit contract version 2. Unsupported peers cannot execute a fallback handler. The go-plugin protocol version is separate. |
| Schemas | Validate JSON Schema 2020-12 with local references only, without coercion or defaults. Reject duplicate JSON fields separately. Preserve exact numbers and validate declared structured results. |
| Annotation and error transport | Preserve optional booleans and MCP defaults. Expose reviewed error codes with fixed safe presentation; keep diagnostic text local. |
| Resource discovery | Retain advertisement observations through admission. Names encode opaque tuple identity; schemas, handlers, and continuations remain route-specific. |
| Query and representation metadata | Describe native query languages through generic catalog metadata. Keep UI representation defaults; declare a separate optional model representation rather than putting Kubernetes branches in the host. |
| Actions | Type actions use a genuine type target, mutually exclusive with an instance reference. MCP advertises only unary actions; streaming actions remain on the existing supported UI lane. Retain the advertised descriptor through the SDK native fence rather than silently installing a fresh descriptor after approval. |
| Results | Preserve canonical tagged values, source revisions, coverage, count, consistency, and bound cursors. Collection limits do not redefine native source pages or claim complete query membership. |
| Logs | Retain the existing native lease until accepted producers exit. Separate creation-RPC cancellation from accepted-session ownership. A host-generated UUID correlates a lost creation receipt with close; it is not identity or permission to retry. Bound headless output to 5,000 lines, 1 MiB, and 30 seconds, with a default tail of 200. |
| Kubernetes parity | Preserve selectors, namespace rules, served versions, model projection, guarded deletion, and bounded 500-item collection above 256-item source pages. Reuse native clients and define catalog refresh honestly. |
| Optional action discovery | Nil means discovery unavailable; nonnil empty means an authoritative empty result. Preserve that distinction over RPC. Omit conflicting scope-specific action IDs from unscoped transport discovery rather than advertising an ambiguous descriptor. |
| Debug write outcomes | Kubernetes debug creation uses the existing public-safe `PROVIDER_ERROR` code for uncertain native outcomes. Preserve the bound Pod UID and reserved container identity for inspection; never infer rollback or automatically submit again. |
| Installation ownership | Route and retire host controllers by installation ID. Keep native handler selectors and resource references on their canonical provider and type, including alternate plugin installations. |
| Confirmation windows | Restore and focus the host-owned main window before prompting. Mount the consent provider above both workspace and panel shells; a window does not become an agent principal. Actual native visibility remains a separate acceptance check. |
| CI and publication | Preserve independent isolation review and managed replacements. Test exact candidate source revisions without publishing, selecting release tags, or treating old SDK tags as the new API. |

Native log APIs do not necessarily support atomic incarnation preconditions. Supplied reference/body validation remains mandatory, but documentation must not invent a stronger native guarantee or introduce a hidden prerequisite Get.

## Source checklist

**Implemented** means candidate code and focused regressions address the source item; final combined-cohort checks remain mandatory. **Roadmap** is explicitly outside the unary/five-tool landing contract. **Publication hold** requires later authorization, not more compatibility code. Historical checks below retain their original revision scope.

### SDK issue25

| Source item | Disposition | Work |
| --- | --- | --- |
| SDK-A1 — existing tools inheritance | Implemented: preserve non-resource tools through the negotiated v2 transport. | W2, W8 |
| SDK-A2 — release/module pins | Publication hold: exact source cohorts replace guessed release versions for candidate verification. | W8 |
| SDK-B1 — connection selection | Implemented: explicit body route, trusted profile, shared native lease. | W1, W2 |
| SDK-B2 — action/tool vocabulary and schemas | Implemented: distinct scopes, optional safety hints, independently validated restricted schemas. | W3, D4–D5 |
| SDK-B3 — errors | Implemented: classified fixed public-safe custom errors; diagnostic causes stay local. | W3 |
| SDK-B4 — catalog/generation fences | Implemented: retained catalog observations and separate process/native generation fences. | W2, W4 |
| SDK-B5 — static discovery | Implemented: route catalog/inventory refresh; no general custom subscription RPC is promised. | W2, W4, W6 |
| SDK-B6 — duplicate resource tools | Implemented: host-generated resource operations; obsolete Kubernetes tools removed after native parity proof. | W4, W5 |
| SDK-C01 — typed filters | Roadmap: typed query-field/operator metadata remains separate from native query schemas. | Roadmap/P07/P13 |
| SDK-C02 — resource description | Roadmap: optional descriptive enrichment does not block truthful capability discovery. | Roadmap |
| SDK-C03 — per-type capabilities | Implemented: consume scoped, independent create/update/delete and read capabilities. | W4 |
| SDK-C04 — action schemas/danger | Implemented: retain supported schema assertions; hints never grant authorization. | W3, D4–D5 |
| SDK-C05 — scale hints | Roadmap: no hint can override finite acquisition budgets. | Roadmap |
| SDK-C06 — headless search | Roadmap: no React callback or second authoritative query engine. | Roadmap |
| SDK-C07 — streaming/progress | Roadmap: MCP advertises unary actions only; existing streaming UI APIs remain intact. | D8, Roadmap |
| SDK-C08 — list-change notification | Implemented: committed inventory/catalog changes refresh route-specific advertisement. | W4, W6 |
| SDK-C09 — provenance/citations | Implemented: tagged resource envelope retains route, reference, source revision, and coverage; history adapter remains later work. | W4 |
| SDK-C10 — lossless numbers | Implemented: canonical tagged resource scalars and exact custom JSON arguments. | W3, W4 |
| SDK-C11 — argument validation | Implemented: validate declarations, input, and declared structured output before successful publication. | W3 |
| SDK-C12 — annotation defaults | Implemented: absent/false distinction and MCP defaults survive transport; hints are not grants. | W3 |
| SDK-C13 — reserved arguments/name length | Implemented: validate names, reserved built-ins, argument collisions, and schema injection before advertising. | W3, W4 |

Source: [full SDK audit][sdk-issue]. C numbers follow its feature-table row order.

### Host issue219 and Kubernetes issue62

| Source item | Disposition | Work |
| --- | --- | --- |
| HOST-A — composition conflict/cohort | Implemented source integration: retain main window/performance features and the resource-v2 metric hook; final immutable pins remain an integration gate. | W8 |
| HOST-B1 — invocation boundary | Implemented: host-issued agent origin, one authorizer, fresh bound consent, and correlated redacted audit events. | W1 |
| HOST-B2 — readiness | Implemented: native generation admission without fabricated universal lifecycle requirements. | W1, D9 |
| HOST-B3 — generated resource tools | Implemented: actual host service dispatch and native five-tool parity proof. | W4, W5 |
| HOST-B4 — errors/refresh | Implemented: safe errors, retained observations, coherent replacement and refresh. | W3, W4, W6 |
| HOST-B5 — roadmap ledger | Implemented: relay landing gates remain distinct from deferred query/search/history engines. | W8 |
| HOST-C — relay-local review | All 16 inline findings and three additional notes are reconciled below. | W3, W6, W7 |
| K8S-A — registration conflict/pin | Implemented source integration; publication remains separately unauthorized. | W5, W8 |
| K8S-B1 — per-call clients | Implemented: remove unmanaged resource-tool clients; use the sole native resource registry. | W2, W4, W5 |
| K8S-B2 — duplicated operations | Implemented: selectors, model/full representations, versions, bounded collection, guarded delete, namespace values, and native logs replace all five tools. | W4, W5 |
| K8S-B3 — native error text | Implemented: safe resource producers, no legacy tool leak, separately sanitized asynchronous logs. | W3, W5 |
| K8S-B4 — static list | Implemented: demand-driven catalog/OpenAPI refresh; no CRD watcher claim. | W4, W5, W6 |

Sources: [host audit][host-issue], [Kubernetes audit][kube-issue].

### Relay PR218: all 16 inline findings

Candidate source addresses every inline finding. CR-06 additionally requires actual native multi-window acceptance; source review or a synthetic preview cannot satisfy it.

| Source item | Required result | Work |
| --- | --- | --- |
| [CR-01][cr01] — discovery timeout | Implemented: bounded startup RPC and generation-owned cancellation. | W6 |
| [CR-02][cr02] — `Listed` mismatch | Implemented: advertise accessible routes truthfully; never invent a default connection or permission. | W6, D9 |
| [CR-03][cr03] — POST body limits | Implemented: bound request bytes and read duration without imposing a stream lifetime limit. | W6 |
| [CR-04][cr04] — post-confirm policy | Implemented: atomically reauthorize current policy and unchanged target before dispatch. | W1 |
| [CR-05][cr05] — resync ordering | Implemented: serialize snapshot through publication. | W6 |
| [CR-06][cr06] — panel confirmation | Implemented source: activate main window and mount consent in both shells; actual native Allow/Deny remains required before merge. | W7 |
| [CR-07][cr07] — status event race | Implemented: newer events survive delayed status responses. | W7 |
| [CR-08][cr08] — tools query errors | Implemented: explicit failure and retry, not successful empty catalog. | W7 |
| [CR-09][cr09] — resolved request resurrection | Implemented: delayed initial snapshots cannot restore resolved consent. | W7 |
| [CR-10][cr10] — failed replacement cache | Implemented: remove failed generations and roll back partial startup. | W6 |
| [CR-11][cr11] — schema keyword validation | Implemented: reject malformed or unsupported declarations before advertisement/injection. | W3 |
| [CR-12][cr12] — same-name replacement | Implemented: replace descriptor, captured handler, and provenance together. | W4, W6 |
| [CR-13][cr13] — backend inventory change | Implemented: publish after committing inventory changes and releasing locks. | W6 |
| [CR-14][cr14] — UI inventory change | Implemented: invalidate mounted scoped inventory from the same signal. | W7 |
| [CR-15][cr15] — AI Tools focus target | Implemented: reject unsupported aliases while retaining valid navigation. | W7 |
| [CR-16][cr16] — failed consent submission | Implemented: keep requests actionable until success or authoritative resolution. | W7 |

### Relay review: three additional notes

These are [review5706913275 notes][relay-review], not extra inline comments or proven production defects.

| Source item | Disposition | Work |
| --- | --- | --- |
| NOTE-01 — built-in/plugin `list_connections` collision | Implemented: preserve built-in precedence and validate collisions before advertisement. | W3, W6 |
| NOTE-02 — real nested bbolt round trip | Implemented: persisted nested nullable overrides retain nil/false/true distinctions. | W6 |
| NOTE-03 — scoped settings rows | Implemented: distinct scoped rows retain actual connection, readiness, and policy; callbacks edit that row. | W7 |

### Additional findings beyond the original checklist

| Finding | Required action | Work |
| --- | --- | --- |
| N1 — fresh host catalog pin can erase stale advertisement | Implemented: compare retained advertisement observation before normal dispatch pin installation. | W4 |
| N2 — later capability startup failure can retain tools | Implemented: roll back tools when whole plugin startup fails. | W6 |
| N3 — listener transitions/session drain are not fully bounded | Implemented: serialize transitions and bound owned cancellation/drain/close. | W6 |
| N4 — status/config/inventory errors masquerade as state | Implemented: explicit failure states; no writes from failed config load. | W7 |
| N5 — pending snapshot failure lacks recovery | Implemented: visible failure and explicit reload/retry. | W7 |
| N6 — replacement-path public errors are not uniformly safe | Implemented: preserve resource-wire sanitization and repair producers and async logs separately. | W3, W5 |
| N7 — argument extraction changes numeric values | Implemented: exact custom JSON tokens and canonical resource scalar kinds. | W3, W4 |
| N8 — CI change alters isolation trust | Implemented policy: keep the independently reviewed isolation minimum and separate immutable candidate-source approval; no publication fallback. | W8 |
| N9 — type-scoped action/request mismatch | Implemented: genuine type target and expected-descriptor native fence; no invented object identity. | W4, D8 |
| N10 — CRD discovery is not an automatic watcher | Implemented contract: eligible demand refresh reacquires discovery/OpenAPI; no watcher guarantee. | W4, W5 |
| N11 — legacy tool contract is not negotiated | Implemented: exclusive v2 RPC namespace and contract version; unsupported peers cannot execute a fallback handler. | W2, W3, D12 |
| N12 — hostile schema work can exceed finite budgets | Implemented: bounded JSON, local static references, expansion/validation work, exact numeric and count limits. | W3 |
| N13 — initialization context becomes stale | Implemented: immutable credential receipt plus fresh actual-session RPC ownership; authenticated DELETE retires work before drain. | W1, W6 |
| N14 — authority cleanup looks like caller cancellation | Implemented: inspect the original RPC context; normal provider failures return a fixed tool error without breaking the session. | W1, W6 |
| N15 — installation/provider selector confusion | Implemented: host maps retire by installation; native selectors derive from canonical reference provider/type. | W5, W8 |
| N16 — uncertain debug write looks safely retryable | Implemented: safe `PROVIDER_ERROR`/reset/cancellation outcomes retain inspection state and block duplicate submission. | W5, W7 |
| N17 — blocked-producer regression can hang on failure | Implemented: release producers before failed-test controller cleanup; retain the lifetime assertions. | SDK audit |

## Roadmap disposition

The original [plans13–19][plans] contain useful intent and historical APIs. They are not the current resource-v2 contract.

| Source | Keep for this integration | Separate work; do not claim implemented |
| --- | --- | --- |
| Plan13 — query/filter introspection | Truthful schema/capability discovery; explicit unsupported results. | Typed query-field/operator metadata, optional descriptions/scale hints; P07/P13 ownership. |
| Plan14 — MCP runtime | Host resource path, explicit routes, admission, readiness, safe errors, cancellation. | Broader transports, subscriptions, progress/polling. Replace namespace/GVK-centric and unlimited-name assumptions. |
| Plan15 — query execution | Provider-native knowledge and bounded acquisition. | Historical `Find`/universal plugin predicates are not current APIs. No silent filter dropping or unbounded post-filtering. |
| Plan16 — natural-language translation | Metadata remains data; SDK never interprets prompts. | NL authoring over accepted host query contracts; not an agent-side authoritative query engine. |
| Plan17 — cross-provider routing | Preserve provider/route provenance and partial failures. | Global query/search/indexing and `QueryAll`; no mandatory watch-everything engine. |
| Plan18 — relationship graph | Canonical references, admitted targets, bounded traversal if exposed. | MCP graph adapters remain later work. The SDK query-only `any` binding defect is fixed without weakening restricted-selector validation. |
| Plan19 — diagnostics | Honest health/event/relationship evidence if used. | Diagnose, trace-failure, blast-radius, common-cause products; shared dependency is not proof of root cause. |
| P01/P04/P13 | Accepted exact-value contracts; new agent invocation integration. | P01 acceptance is distinct from installed schema/planner/query receipts. Do not reopen its superseded numeric/CLA holds. |
| HR29 — read-only AI history | Permitted evidence citations and budgets inform results. | Headless history query/compare/traverse adapter; live resource reads are not a historical store. |

The [P01 acceptance record][p01-acceptance] supersedes earlier numeric holds. Confirm installed service receipts before advertising universal query/search tools. SDK direction is accepted; final integration checks and explicit future merge/publication authorization remain separate. The [asynchronous logs issue23][logs-issue] lifetime fix now has SDK and actual native MCP evidence.

## Landing order and evidence

1. **Review decisions:** retain the accepted SDK direction and resolve affected D1–D12 implementation choices. No publication action follows automatically.
2. **Safety wave:** W1/W2/W3 plus W6 startup/lifecycle hardening. Keep source writers package/file-owned; serialize shared files.
3. **Integration wave:** W4 resource tools/results and W6 coherent refresh. W7 can proceed on independent UI files once view/event contracts are agreed.
4. **Parity/cutover wave:** exercise all five replacements, then W5 deletes obsolete plugin tools. No silent feature loss or permanent compatibility shim.
5. **Cohort wave:** W8 reconciles composition, approved candidate CI, bindings, toolchain, and documentation. Future release pins require separate authorization.

### Evidence required from implementation

| Layer | Required proof |
| --- | --- |
| SDK | Focused native-generation/cancellation/retirement and wire-validation regressions; active lint, formatting, full normal and race suites. All Go/Make commands use `GOWORK=off GOFLAGS=`. |
| Host boundary | Real admitted agent requests, denial despite approval, policy/route/plugin retirement during consent, public-safe errors, actual audit correlation. |
| MCP transport | Actual authenticated MCP client: list/call/list-change, same-name replacement, invalid schemas/arguments, exact values, body bounds, and complete shutdown. |
| Resource parity | Point read, filtered multi-page list, scoped dimensions, delete with native UID/revision checks, two route-specific catalogs/CRD refresh. No extra Get permission merely to authorize logs/delete. |
| Logs | Real bounded one-shot stream: container/previous/tail, aggregate output cap, cancellation, close, safe async failures, issue23 retirement behavior. |
| UI | Actual visible multi-window approval plus meaningful delayed-response/error/scoped-row regressions; regenerated binding checks. |
| Integration | Separate reviewers per implementation slice and final cohort; central checks only after source writers finish. Keep formatting, behavior fixes, documentation, and structural changes reviewable separately. |

Passing SDK tests or old live-cluster receipts cannot establish the new MCP contract. Deferred `lint-audit` rules remain informational; do not turn them into extra acceptance gates.

### Evidence from this cataloging pass

- Retrieved all three primary issues: zero issue comments at capture; retained complete bodies.
- Retrieved all relay PR discussion, two reviews, and all 16 inline comments without replies; included the three embedded review notes.
- Verified 52 unique source-item IDs in an executable coverage inventory. Raw reports and domain evidence remain ignored under `.tools/mcp-audit/`.
- Eight domain agents performed read-only source audits. Two separate reviewers reconciled source coverage and integration contracts. Cross-domain review corrected producer-versus-wire error exposure, empty-container behavior, and superseded P01 holds.
- Rendered the Markdown and Mermaid locally in Chromium and exercised source-checklist navigation. This verifies documentation rendering, not MCP runtime behavior.
- No runtime code, generated code, managed replacement, lint policy, API acceptance, or release state changed. No new runtime/conformance pass is claimed by this document.

## Implementation wave 1 — API-preserving fixes

Two independent workers completed bounded fixes; separate reviewers checked each slice and the final integration. These changes do not accept D1–D11 or complete the broader work packages.

| Partial work package | Fix | Behavioral commit |
| --- | --- | --- |
| W3 — action descriptor validation | Reject duplicate `required` names in parameter/output schemas, including nested objects. Preserve nil/empty lists and required names absent from `properties`. | SDK [`6a7cecd`](https://github.com/omniviewdev/plugin-sdk/commit/6a7cecd) |
| W5 — Kubernetes error producers | Replace native API/network/certificate diagnostics in public fields with safe messages. Preserve classification, cause inspection, unknown-error fallback, and existing wire sanitization. | Kubernetes [`6d57348`](https://github.com/omniviewdev/omniview-plugin-kubernetes/commit/6d57348) |

Executed centrally after source writers finished; every Go/Make command used `GOWORK=off GOFLAGS=`:

| Check | Result |
| --- | --- |
| SDK focused `TestActionSchemaRequiredNames` | Passed; final regression covers 42 parameter/output, location, and required-list combinations. |
| Kubernetes focused `TestClassifyResourceError` | Passed, including hostile diagnostics and cause preservation. |
| SDK full normal and race suites | Both passed: 23 tested packages, 18 without tests. |
| Kubernetes full normal and race suites | Both passed: 11 tested packages, five without tests. |
| SDK `make fmt`, scoped `make fmt-check`, and active `make lint-report` | Passed; zero active findings. |
| SDK informational `make lint-audit` | 1,237 findings: paralleltest 585, protogetter 518, testpackage 82, nonamedreturns 29, funcorder 23. No policy changed. |
| SDK disposable consumer smoke | JSON round-trip rejected duplicate root/nested required lists in both schema roles and accepted unique undeclared names. |
| Kubernetes disposable native HTTP smoke | Actual client-go GET consumed a hostile 403 response; classifier retained `FORBIDDEN` and native cause while public JSON excluded diagnostics. |

Smoke source and the temporary private-classifier access hook were removed. No live cluster or MCP end-to-end pass is claimed. Full tool-schema validation, custom-tool errors, asynchronous logs, admission, discovery/generation integration, and the remaining catalog remain open. SDK acceptance, publication, and merge holds remain unchanged.

## Implementation wave 2 — explicit routes and candidate composition

The user accepted the SDK direction and authorized implementation. That direction-review hold is removed; production MCP acceptance and publication remain separate.

| Work | Delivered |
| --- | --- |
| SDK custom-tool admission | [`7bd59d1`](https://github.com/omniviewdev/plugin-sdk/commit/7bd59d1) adds canonical body-route presence, both-scope validation, configured-profile binding, shared native leases, and cancellation/retirement result fences. Metadata-only connection calls no longer satisfy admission. |
| Metadata-free RPC lifetime | Server dispatch preserves the incoming RPC context even without request-ID metadata. Real raw-gRPC cancellation/deadline regressions verify lease drain and late-result rejection. |
| Conformance publication | [`5f45aac`](https://github.com/omniviewdev/plugin-sdk/commit/5f45aac) publishes fixture commands/receipts atomically; a pipe regression proves partial writes are not observable. Native generation assertions remain intact. |
| Formatting | [`e48f95b`](https://github.com/omniviewdev/plugin-sdk/commit/e48f95b) contains formatter-only changes. |
| Host candidate | [Draft PR220](https://github.com/omniviewdev/omniview/pull/220), stacked on PR192, ports the original MCP feature onto resource v2. [`45f34533`](https://github.com/omniviewdev/omniview/commit/45f34533) preserves native services, the metric hook, and independent CI review while migrating custom-tool routes and exact argument handling. |
| Frontend integration | Regenerated bindings/events and aligned narrowed connection scopes, YAML tag types, sidebar fallback icons, and canonical editor schema types. No runtime export compatibility shim was added. |

Separate implementation and review agents covered the slices and corrections. Central checks used `GOWORK=off GOFLAGS=` and a private host modfile selecting the actual SDK source. The SDK requirement committed in the host remains the original V2 baseline; it is not a published version of this new API. The existing `SDK_SOURCE` preparation path supplies the verified candidate without modifying managed worktrees or committing absolute replacements.

| Executed evidence | Result |
| --- | --- |
| SDK full normal/race suites | Both passed: 23 tested packages, 18 without tests. |
| SDK formatting and active lint | Passed; zero active findings. Informational audit: 1,246 findings across the same five deferred rules; no policy changed. |
| Host full normal suite | Passed: 44 tested packages, 12 without tests. |
| Host integration race suites | Resource, tools, relay, and settings-store packages passed. This is not a full-host race claim. |
| Bindings/runtime/types | Warning-free Wails generation, runtime build, and host TypeScript check passed. |
| Frontend suites | Host: 78 files / 1,124 tests. Runtime: 22 files / 288 tests using a fresh actual Go/gRPC error corpus. |
| Real MCP/native smoke | MCP HTTP → actual host controller → SDK gRPC → registered native client → local HTTP. Both scopes computed `9007199254740993 + 1` exactly. Stopped dispatch caused no second native read; client creation/destruction occurred once. Missing bearer returned 401. |
| Actual settings surface | Chromium rendered the real page/providers with explicitly synthetic service data and exercised Reveal/Hide without horizontal overflow. Native Wails window/event transport was not exercised. |

Disposable programs, private-classifier hooks, visual harness, browser tab, and preview service were removed. The isolated candidate worktree and ignored verification corpus remain available for continued integration.

**Historical wave-2 remainder:** the items listed here were open at that wave's revision. The reconciled source checklist above and the later implementation evidence below supersede that implementation status. Native multi-window acceptance, final combined-cohort checks, and future publication authorization remain distinct gates. The relay stays disabled by default.

The host push also reported existing default-branch dependency alerts, including one critical alert. Those are not a PR-specific scan or a remediation claim; they remain a separate security backlog.

## Implementation wave 3 — production contracts and native parity

Separate package/file owners implemented each domain; independent agents reviewed the source and subsequent fixes. SDK behavior is in [`afd3a32`](https://github.com/omniviewdev/plugin-sdk/commit/afd3a32), bounded validation/session operations in [`dbedfc8`](https://github.com/omniviewdev/plugin-sdk/commit/dbedfc8), failure-safe blocked-producer cleanup in [`9591307`](https://github.com/omniviewdev/plugin-sdk/commit/9591307), and shared consumer descriptor validation in [`d1f7da3`](https://github.com/omniviewdev/plugin-sdk/commit/d1f7da3). Formatting has separate commits.

| Executed evidence | Result and scope |
| --- | --- |
| Final SDK normal and race commands | Both passed: 23 tested packages and 18 without tests on the final Go source. |
| Final SDK formatting and active lint | `make fmt-check` and `make lint-report` passed; zero active findings. No policy, threshold, or handwritten-code exclusion changed. |
| Informational SDK audit | 1,354 findings: paralleltest 659, protogetter 539, testpackage 102, nonamedreturns 31, funcorder 23. Separate reviewers checked presence/guards and test isolation; the blocked-producer cleanup finding was fixed, not hidden. |
| Actual SDK example | `go run ./examples/resource-plugin -smoke` read the native note and preserved `int64(9007199254740993)`. |
| Main-integrated host foundation | Full normal suite passed: 44 tested packages, 11 without tests. TypeScript passed; frontend suite passed 1,213 tests, with three existing skips. These precede the final stacked composition and later opaque-route regressions. |
| Full MCP package with actual native child | Passed, including actual HTTP provider-error/recovery and cancellation paths. A discovered authority-cleanup cancellation bug was fixed rather than accepted as an expected transport error. |
| Custom schema publication and persistence | Full MCP and store packages passed after sharing SDK descriptor validation, preserving exact schema numbers, rejecting reserved-field/output-schema failures, retaining trusted built-in precedence, and round-tripping actual nested `Config` nil/false/true overrides through bbolt close/reopen. |
| Kubernetes cutover suites | Full normal and race suites passed: 11 tested packages, six without tests. |
| Five Kubernetes replacements | Authenticated MCP HTTP → one host boundary → real resource/log controllers → actual external Kubernetes SDK child → owned kind cluster. Verified model/full-version Get, required namespace rejection, native label query, namespace values, finite native logs, single-use collection cursors, stale UID/revision delete rejection without effects, valid deletion, and retirement rejection. |
| Logs-only native permissions | Real service-account credentials permit `pods/log` Get but deny Pod Get. MCP logs returned actual container output and released the session while Pod Get remained denied. No hidden prerequisite Get was added. |
| Native selector ownership | Existing real gRPC/SDK-manager/PTY/file-I/O regression passed with different installation and canonical provider IDs, including handler retirement. |
| Main feature preservation | Resolved resource-v2 and current main architecture together. Retained workspace/panel shells, native window service, performance changes, metric hook, and canonical resource routes. |

The native proof first used the real Kubernetes entrypoint with only obsolete tool registration omitted. All five replacements passed before the old implementation, registration, and capability were deleted. The rerun using the actual cutover entrypoint also passed. Final combined-cohort checks remain separate; earlier receipts do not substitute for them.

### Final integration gate

Immutable source pins, the dedicated reviewed-candidate SDK variable, final normal/race/frontend checks, independent combined-tree review, and actual native multi-window Allow/Deny evidence must be recorded before merge on [PR220](https://github.com/omniviewdev/omniview/pull/220) and its linked foundation/plugin PRs. This SDK ledger does not claim an unobserved final native-window pass.

No merge, ready transition, API freeze, tag, release, or publication follows from these checks. The accepted direction does not make all plugin-sdk work complete. Deferred query/search/history products remain the explicit roadmap above, not hidden findings or fabricated implementations.

## Inspected revisions

| Surface | Revision |
| --- | --- |
| SDK resource-v2 candidate used for this audit | `bfbb2029bca0df64dd89574adaf4ed03f5589284` |
| Host resource-v2 candidate | `d14e8be77ec6e41827fa0617fb7f781ad7e647af` |
| Host MCP relay PR218 | `7602a6c496fcd054af8eeeb1d46318be9a213d39` |
| Host relay base/main used by source audit | `1216016a68c57e800999ce8328b5d23a20f51ecf` |
| Kubernetes resource-v2 candidate | `50902e855249fc74d9b22468f0b7a5592b4fa1d4` |
| Kubernetes merged tools PR61 | `d548b685ccd6f094ffc07b81a688ef93a07e9968` |
| Released SDK tools PR24 / v0.8.0 | `32a3358caa78f6f217d01444c0fb2926dbf8a2b0` |

Source findings are revision-scoped, not claims about future branch heads. Existing unrelated worktree changes remain owned by their authors.

[alex]: https://github.com/omniviewdev/plugin-sdk/pull/21#issuecomment-6527618354
[sdk-issue]: https://github.com/omniviewdev/plugin-sdk/issues/25
[host-issue]: https://github.com/omniviewdev/omniview/issues/219
[kube-issue]: https://github.com/omniviewdev/omniview-plugin-kubernetes/issues/62
[ci-comment]: https://github.com/omniviewdev/omniview/pull/218#issuecomment-6515110793
[relay-review]: https://github.com/omniviewdev/omniview/pull/218#pullrequestreview-5706913275
[mcp-tools]: https://modelcontextprotocol.io/specification/2025-11-25/server/tools
[logs-issue]: https://github.com/omniviewdev/plugin-sdk/issues/23
[p01-acceptance]: https://github.com/omniviewdev/omniview/issues/80#issuecomment-6476688468
[plans]: ../plans/resourcer-refactor
[sdk-binding]: https://github.com/omniviewdev/plugin-sdk/blob/bfbb2029bca0df64dd89574adaf4ed03f5589284/pkg/sdk/plugin.go#L137-L168
[tool-validation]: https://github.com/omniviewdev/plugin-sdk/blob/bfbb2029bca0df64dd89574adaf4ed03f5589284/pkg/v1/tools/manager.go#L56-L101
[wire-errors]: https://github.com/omniviewdev/plugin-sdk/blob/bfbb2029bca0df64dd89574adaf4ed03f5589284/pkg/resource/v2/plugin/codec.go#L100-L175
[invocation]: https://github.com/omniviewdev/omniview/blob/d14e8be77ec6e41827fa0617fb7f781ad7e647af/backend/pkg/invocation/invocation.go#L15-L123
[host-service]: https://github.com/omniviewdev/omniview/blob/d14e8be77ec6e41827fa0617fb7f781ad7e647af/backend/pkg/plugin/resource/service.go#L17-L66
[host-finite]: https://github.com/omniviewdev/omniview/blob/d14e8be77ec6e41827fa0617fb7f781ad7e647af/backend/pkg/plugin/resource/finite_operations.go#L87-L224
[kube-tools]: https://github.com/omniviewdev/omniview-plugin-kubernetes/blob/d548b685ccd6f094ffc07b81a688ef93a07e9968/pkg/plugin/tools/register.go#L31-L313
[kube-errors]: https://github.com/omniviewdev/omniview-plugin-kubernetes/blob/50902e855249fc74d9b22468f0b7a5592b4fa1d4/pkg/plugin/resource/errors.go#L16-L126
[cr01]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484864
[cr02]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484867
[cr03]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484870
[cr04]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484886
[cr05]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484893
[cr06]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484895
[cr07]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484904
[cr08]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484914
[cr09]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419484932
[cr10]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419490497
[cr11]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419490510
[cr12]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419490518
[cr13]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419490529
[cr14]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419490535
[cr15]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419490540
[cr16]: https://github.com/omniviewdev/omniview/pull/218#discussion_r4419490557
