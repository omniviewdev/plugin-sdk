# Resource v2 advanced contracts

**Review status:** implementation candidate, not an approved public contract. This document describes current source behavior and caller obligations. It does not approve the admission, lifecycle, overlay, or transport policies identified in [code review](resource-v2-code-review.md) and [specification comparison](resource-v2-spec-review.md).

For a plain-language comparison with the previous resource APIs, start with the [v1-to-v2 overview](sdk-v1-v2-overview.md). Concrete Kubernetes/Helm filenames below are integration landmarks, not required provider contracts; that overview also records remaining genericity limits.

[Documentation home](README.md) · [Quickstart](quickstart.md) · [Resource tasks](resources.md) · [API reference](resource-v2-api.md) · [Connection tasks](connections.md)

Use this page for detailed admission, ownership, lifetime, limits, and pending contract decisions.

## Navigation

- [Responsibilities and source map](#responsibilities-and-source-map)
- [Export inventory](#export-inventory)
- [Construction and examples](#construction-and-examples)
- [Route-qualified admission](#route-qualified-admission)
- [Connection lifecycle and overlays](#connection-lifecycle-and-overlays)
- [Concurrency and ownership](#concurrency-and-ownership)
- [Utility and stream lifetime](#utility-and-stream-lifetime)
- [Results and errors](#results-and-errors)
- [Relationship helper limitation](#relationship-helper-limitation)
- [Safety ceilings and exclusions](#safety-ceilings-and-exclusions)
- [Cleanup contract decisions](#cleanup-contract-decisions)

## Responsibilities and source map

| Owner | Responsibility | Source/caller landmarks |
| --- | --- | --- |
| Provider domain | Canonical identity, explicit routes, scoped capabilities, representations, honest source results | SDK `pkg/resource/v2/domain.go`, `ancillary.go`, `ancillary_types.go`, `updates.go` |
| Optional controller | One configured inventory and native-client registry; lifecycle, leasing, admission and result fences | SDK `Controller`, `Config`, `Registration`, `Lifecycle`; Kubernetes `pkg/plugin/resource/native_provider.go` constructs it |
| Connection registry | Own actual configured profiles and active generations; utilities share this registry rather than create another | SDK `ConnectionContextProvider`; `pkg/v1/{exec,logs,metric,networker}` consumers |
| Transport | Negotiate features/version, validate raw input before decoding, adapt domain values, preserve classification and readiness | SDK `pkg/resource/v2/plugin`, protobuf adapters, `proto/resource/v2` |
| Host | Authorize invocation, issue authority, capture accepted catalog observations, bind source epochs, redact errors, persist/replay desired overlays | Host `backend/pkg/plugin/resource/{catalog_binding,finite_operations,ancillary_service}.go` |
| Runtime/UI | Select independent catalog capabilities and maintain explicit target context; negotiate optional ancillary features | Host finite/subscription services and resource table consumers |

Kubernetes separates native catalog assembly, presentation, and relationships into `native_catalog_assembly.go`, `native_presentation.go`, and `native_relationships.go`. Helm `register.go` composes handlers; `request.go`, `catalog.go`, `read.go`, and `operations.go` own their respective workflows. These are organization boundaries, not extra registries or interface layers.

## Export inventory

This is an ownership inventory, not blanket approval of every exported declaration. Families below include their request/result values and constants. An exported transport helper is not thereby a provider-author recommendation.

| Audience | Export family | Actual use and boundary |
| --- | --- | --- |
| Provider author | `Provider`; optional `Getter`, `Enumerator`, `NativeQuerier`, `Mutator`, `Actioner`; domain identity/scope/value/catalog/page types | Kubernetes native and Helm providers construct canonical values. No mandatory CRUD, namespace, enumeration, or live source. |
| Provider author | `Config`, `Registration`, `Lifecycle`, `NewController`, `Controller.WithClient` | Kubernetes `native_provider.go` composes native/Helm registrations and the client lifecycle. Custom providers may omit this controller. |
| Provider author | Ancillary provider interfaces, `BestEffortUpdateProvider`, watch/action event types, sync policies | Optional schema, presentation, relationships, health, actions and advisory updates; negotiate feature availability remotely. |
| Provider author | `NewCatalogBindingEpoch`, `BindCatalogRevision` | Custom providers issue source/configuration generations; controller users receive binding automatically. See executable example. |
| Provider author / utility integration | `ValueFromNative`, `NativeFromValue`, `JSONToValue`, `TimestampValue`, `ScopeForAddress`, validation and bounded-size helpers | Preserve exact scalar intent, validate canonical shapes, and bound source pages without protobuf construction. These helpers do not authorize work. |
| Provider author / host integration | `ResourceOperationError`, `NewError`, `ErrorClassifier`, `ErrorCode`, `IsErrorCode` | Provider classification crosses transport; host assigns trusted provenance and final public text. |
| Host/integration | `Session`, `WithSession`, `SessionFromContext`; connection copy/config comparison and overlay contracts | Host and SDK utility composition carry diagnostic context and manage configured profiles. Session labels are not authentication. |
| Host/integration | `WithCatalogRevision`, `WithCatalogRevisionForRoute`, observation accessors/checkers, `CatalogBindingEpoch` | Host `finite_operations.go`, `ancillary_service.go`, and `catalog_binding.go` pin admitted observations and detect actual binding changes. |
| Host/integration | `AdmitCatalogOperation`, `ValidateOperationResult`, feature negotiation | Host finite/subscription services reuse domain admission and fence results; remote method sets do not prove support. |
| Utility integration | `ConnectionContextProvider`, `ConnectionContextConsumer`, `WithUtilityConnection` | Existing utility managers bind native work to the sole started registry. Ordinary nonreference utilities do not need fabricated catalogs. |
| Transport only | `ToProto*`/`FromProto*`, wire validation/version/policy helpers, generated protobuf messages | SDK plugin servers/clients and utility adapters. Host subscription equality now uses domain-owned `CanonicalReadTarget`, not protobuf adapters. |

Qualified-observation transport framing and RPC-boundary clearing belong behind SDK internal/transport ownership. Provider code uses validated observation helpers, never binary headers or token parsing. The framing cutover does not remove host epoch inspection or custom-provider generation issuance.

## Construction and examples

[Go package overview](../pkg/resource/v2/doc.go) and [executable examples](../pkg/resource/v2/example_test.go) cover four real API compositions:

1. `ExampleProvider`: catalog-only implementation with no native client or finite methods.
2. `ExampleGetter`: get-only controller with one exact registration, descriptor, host-issued example authority, and accepted revision pin. No enumeration capability is implied.
3. `ExampleLifecycle`: load configured profiles, explicitly start, lease a configured profile, stop, and close.
4. `ExampleNewCatalogBindingEpoch`: immutable custom provider without Controller; issue one actual binding epoch and bind its native metadata revision.

Register a provider through `sdk.RegisterResourcePlugin(plugin, provider)`. The examples deliberately isolate domain construction from plugin process setup. A custom mutable provider must additionally synchronize generation publication, cancel retired work, and fence dispatch/results; issuing a token alone supplies none of those safeguards.

Create/update/delete catalog declarations are independent: `OperationCreate`, `OperationMutationUpdate`, and `OperationDelete`. `MutationRequest.Operation` remains `create`, `update`, or `delete`; admission maps the requested verb to its exact declaration. `OperationUpdate` means advisory watch delivery only. Implementing `Mutator` does not imply all write verbs.

## Route-qualified admission

Connection selects access, not identity. Addresses contain provider, host-issued authority, opaque type, identity scope, and opaque ID. Incarnation and source revision remain optional opaque observations. Neither a native authority hint nor equal hints grant authority or sharing rights.

The default and qualified helpers serve different call shapes:

- `WithCatalogRevision` supplies one explicit observation for a single-route source call.
- `WithCatalogRevisionForRoute` supplies an observation keyed by exact provider and route. Use independently admitted target observations for cross-route relationships, and optionally for the source itself.
- Matching qualified observations take precedence over default observations. A wrong qualified observation cannot fall back to a correct default.
- `QualifiedCatalogRevisionFromContext` never falls back. `CatalogRevisionForRouteFromContext` does. Duplicate qualified entries fail closed, even if their revision strings match; helpers append rather than overwrite. Duplicate lookup yields a present invalid token, and admission rejects it.
- Admission validates the bounded observation set and exact current catalog equality. Missing, malformed, duplicate, or stale required observations return `RESET_REQUIRED`; aggregate excess returns `RESOURCE_LIMIT`. A new cached revision must never silently replace the accepted token.

Pins are observations, not grants. The host must first authorize provider/authority/type/scope/operation. Finite operations, resource ancillary operations and targeted advisory sources require accepted observations. Discovery, feature negotiation, connection management, and pin-free native leasing do not. Relationship targets cannot borrow an unobserved route's source pin.

A native metadata change changes the bound catalog revision but not the source epoch. Replacing/reconfiguring the actual native binding changes its epoch, including A→B→A transitions. Custom providers retain an issued epoch for one actual binding; stateless providers issue one per instance. Never invent a constant epoch or rotate it merely for schema/catalog churn.

## Connection lifecycle and overlays

Controller is the sole state owner. Configured profiles and registered clients are distinct. Discovery does not start clients, and no operation selects the first active connection implicitly.

- Configured, unstarted profiles report `DISCONNECTED`. Start publishes one pending slot and reports `PENDING` through readiness inspection while construction runs.
- `CreateClient` and optional `OnStart` must finish before the slot becomes usable. Successful startup reports `CONNECTED`, which means registered lifecycle state, not network health.
- Startup failure cancels and cleans up the slot. Every nonnil returned client is destroyed exactly once, including failed starts. Safe `FAILED` status survives client cleanup; no client/error diagnostic is retained for that status.
- Explicit retry creates a fresh slot and clears the failure marker. Stop or meaningful configuration replacement also clears it; display-only edits preserve it.
- Stop/configuration retirement/Close prevent new leases, cancel the generation, stop its advisory sources, wait for current leases, then invoke `OnStop` and `DestroyClient`. Cleanup uses a context without the retired generation's cancellation; callbacks must not depend on cancellation to end cleanup.
- Unknown profile IDs return `NOT_FOUND`. `GetConnectionStatus` does not wait, start, acquire a resource lease, or probe the network. `CheckConnection` is the separate optional health operation.

The startup creator's request cancellation cancels startup until construction completes. A concurrent waiter canceling its own request does not own the shared slot. Accepted native generation lifetime then belongs to the controller, not the completed start RPC. Request leases combine caller cancellation with generation/controller cancellation; dispatch fences reject retired results even when native callbacks ignore cancellation.

`ApplyConnectionOverlay` replaces the explicit in-memory desired overlay: upserts are manual profiles/overrides, disabled IDs are tombstones, and empty overlay restores native configuration. Native refresh cannot erase overrides or resurrect tombstones. The host owns persistence and startup replay; the SDK does not rewrite native configuration files.

Inventory application stages changed routes, blocks new starts there, cancels/drains old generations, then publishes effective inventory together. Failed teardown or canceled application keeps previous desired inventory, but already retired clients stay retired. Reapplying an earlier overlay does not resurrect old clients or epochs. Display-only changes preserve active clients; ID/UID/data/private client configuration changes retire them. Nil private override data can inherit source-owned credentials only when public client configuration remains unchanged; explicit empty private data clears it. Hosts must not persist native private credentials.

## Concurrency and ownership

Controller serializes inventory changes with `configMu`; nested acquisition order is `configMu` then registry `mu`, never the reverse. Registry `mu` protects slot publication, lease admission and retirement. Teardown waits and provider callbacks run outside `mu`. Inventory teardown still holds `configMu` across callbacks: callbacks must not synchronously reenter inventory mutation/refresh. Do not synchronously Stop/Close/reconfigure the same generation from a callback holding its lease: retirement waits for that callback.

Callbacks for different requests, watches and connections can run concurrently. Synchronize shared native clients and provider state; callback execution does not imply single-threaded access. Cancellation is cooperative. Native creation, finite work and watch loops must observe their contexts, and event sends must remain cancellation-aware. A blocked callback can block retirement.

Constructor registration slices are snapshotted shallowly; nested registration scopes/value slices, the Lifecycle object and captured callback state must remain immutable after construction unless the provider synchronizes its own callback state. Do not infer deep ownership from the top-level slice copy.

Controller copies mutable connection configuration at ingress, inventory/status egress and callback/session boundaries. Exported mutable credential containers remain owned even with `json:"-"`; opaque native handles/unexported implementation state retain their deliberate native ownership. Editing a profile snapshot does not apply configuration. Admitted watch targets and descriptor containers receive independent snapshots, including provider callback copies, so mutation cannot widen retained admission. General finite request/result containers must not be mutated concurrently with SDK use; produce independently owned results rather than reuse mutable buffers across calls.

Framework code owns its outgoing stream channels and completion. Producers send events and return; do not close framework-owned channels. Listener cancellation retires that listener; generation retirement owns its source cleanup. Advisory data arriving before a listener is ready is dropped, not queued for replay; state retains its readiness/replay barrier. This bounded advisory behavior is not gap-free continuity.

## Utility and stream lifetime

`WithConnection` requires a configured, already-started native generation and supplies its actual configured-profile snapshot. Metadata is only a selector. `WithClient` exposes the registered native client under a lease; with no lifecycle configured its client is nil. Neither helper probes, implicitly starts, chooses a default connection, or grants resource access.

Reference-bearing exec/log/metric/forwarding work carries actual canonical reference/route and accepted catalog observation, without requiring an unrelated Get. Explicit route-only intent still requires a matching selected configured connection. Ordinary opaque selectors and cluster metrics do not require fabricated references or catalog pins. Body encoding validation alone does not establish connection admission.

Creation/admission and accepted session lifetime differ by utility; do not infer one shared guarantee:

- **Resource forwarding:** caller and generation cancellation remain attached through native creation/readiness. Acceptance retains the exact session's configured-generation lease until close, native completion, or retirement. Completing the initiating RPC does not close an accepted forward.
- **Logs:** `CreateResourceSession` registers a `connecting` session and starts native acquisition asynchronously. Its connection lease covers admission only; the session context is detached from the creation RPC before native handler/resolver readiness. Registry retirement alone does not cancel or await that acquisition. Integrators must explicitly close affected log sessions when retiring a connection, or shut down the log manager. Returning a session is not proof of native readiness. Whether to enforce generation ownership inside this asynchronous engine remains a public-contract review decision.
- **Exec:** its session engine owns accepted-session close and RPC-independent lifetime. Do not infer forwarding's retained-lease implementation merely from the shared resource admission helper.
- **Metrics:** queries, polls, and streams retain their actual request/stream cancellation and owner-scoped subscriptions. Equal local subscription IDs on different connections cannot steal output.

Action streams emit exactly one `ready` only after native establishment and before output/progress/completion. Return pre-establishment errors without ready. Framework handles producer return separately from channel closure; successful return without ready is `PROVIDER_ERROR`. Remote metric streams also complete open/ready admission before exposing output. A remote client's broad static Go method set does not prove support: use `FeatureNegotiator` before optional ancillary startup.

## Results and errors

- **Schema:** `available` requires revision, dialect, and exactly one inline document or document reference. `absent` has no document binding. `unavailable` is acquisition failure, not absence; it cannot include inline document. Optional unavailable reason is a reviewed machine code, never diagnostics. `full` validation requires available schema and no unvalidated constraints; `reduced` requires available schema plus explicit unvalidated constraints; `none` makes no constraint claims. Roots are local RFC6901 URI-fragment pointers; empty root selects the whole document. No implicit external IO or normalization occurs.
- **Representation:** canonical SDK Get requests and read targets require an explicit representation. A host resolving an omitted UI selection may use only the catalog's explicit `DefaultRepresentation`; it must pass the resolved representation to the provider. Result provenance must match the admitted representation.
- **Coverage:** `complete` has no limitations; `partial` and `unknown` require a reason and retain path limitations. Failed/denied discovery is an error, not empty complete coverage.
- **Count:** `unknown` has no value; `exact`/`estimate` use canonical unsigned decimal strings. Unknown is not zero and page length is not automatically total count.
- **Cursor:** optional opaque source continuation, not a host query cursor or portable source identity. Preserve source semantics; never fabricate continuity or silently reuse a cursor across changed targets/generations.
- **Scope:** valid empty `oneOf` matches nothing, including query-only dimensions. It is neither invalid nor `any`; adapters must return an honest empty page without broad native acquisition. Query-only dimensions cannot be inferred narrow from identity alone.
- **Page:** `SourcePage` is bounded finite acquisition, with explicit coverage, consistency and optional source revision. It does not promise snapshot/watch cutover or final query planning.

`ResourceOperationError` carries reviewed C02 classification. `Title`, `Message`, and `Suggestions` must be public-safe when providers construct them. `NewError` places its detail directly in public-facing `Message`; never pass raw native error strings, credentials, URLs or private configuration. Put raw native diagnostics in `Err`, excluded from JSON and reachable through `Unwrap`. `Error()` serializes only classification/public text, not `Err`. Host redaction and canonical AppError provenance remain authoritative.

`IsErrorCode` defines accepted vocabulary; unknown errors/codes fall back to `INTERNAL`. Cancellation/deadline remain `CANCELLED`/`TIMEOUT`. Classifiers may decline with nil; registration classifier precedes controller classifier, while existing typed errors and cancellation retain classification. Denial, internal and unknown failures never become trusted host recovery instructions merely because providers supplied text.

## Relationship helper limitation

`MatchRelationshipTarget` currently matches target query constraints against returned identity scope. Every target query dimension must therefore appear in that identity scope, even for an unrestricted `any` constraint. A query-only dimension absent from identity cannot match and returns `UNSUPPORTED`.

`ValidateRelationshipResolutionResult` uses the same matcher and rejects the whole result rather than filtering unmatched links. This is a known helper limitation, not a rule that query dimensions must be identity dimensions. Do not fabricate identity coordinates, widen bindings, or report unresolved links as complete empty success.

See [relationship_validation.go](../pkg/resource/v2/relationship_validation.go) and [relationship_request.go](../pkg/resource/v2/relationship_request.go). Target bindings still require independent host authority, explicit routes and scopes, and accepted catalog observations.

## Safety ceilings and exclusions

Candidate policy `omniview.resource-wire-policy.v1` bounds frames at 32 MiB, bodies at 16 MiB, semantic value depth at 64, nodes at 262144, fields at 1048576 and source pages at 256 records. Aggregate frame/page budgets apply before generated decoding; record limits do not multiply aggregate allowance. Protobuf-tree, JSON-tree and semantic-value depth checks are separate boundaries, not interchangeable traversal counters. Qualified observations have 4096-byte individual revision and conservative 32 KiB aggregate limits; targets are bounded at 256. Metric lanes separately bound used-ID tombstones at 4096 and retained admission metadata at 32 MiB.

These are candidate safety ceilings, not latency promises, planner budgets, or approved product policy. Hosts may lower effective limits. The separate predicate/projection policy remains 64 KiB. `ValueWireSize` and `RecordWireSize` support bounded source construction without allocating transport messages.

History ingestion/storage, search, timeline, universal planning, schema normalization (P07), and strong P05 source continuity are excluded. Advisory updates never claim replay, gap-free delivery, or atomic snapshot/live handoff. Domain documentation does not turn excluded engines into implicit requirements or completion claims.

## Cleanup contract decisions

The lint cleanup preserves the candidate resource API and its pending acceptance status. It does not consolidate admission contexts, change asynchronous log-generation ownership, or approve transport ceilings. The following boundary changes are explicit:

- SDK lifecycle registration rejects a positive protocol version above `math.MaxInt32` by panicking before registration, rather than truncating its wire value. Nonpositive configured versions still select the current protocol default.
- Settings `GetIntSlice` returns `ErrSettingTypeMismatch` when a `uint64` element exceeds the platform's maximum `int`; it no longer wraps that element into a negative value.
- Internal catalog observation sizing rejects counts and individual fields that cannot fit the framing format's unsigned 16-bit lengths, even if a caller supplies wider custom limits. Rejection occurs before frame allocation. The normal resource-domain limits are unchanged.
- Fake-terminal cleanup and failed real-terminal construction attempt every owned descriptor close and return joined failures. An accepted real terminal still closes both descriptors and returns the first failure. `FindFreeTCPPort` reports failure to close its temporary listener instead of claiming that the selected port was released.
- Stream completion recognizes wrapped `io.EOF`. Invalid internal RPC dispatch types return classified errors rather than unchecked assertion panics; this does not relax wire or domain admission.

The unused `command.ProviderImpl` export was removed after reference and managed-consumer checks; its method now belongs directly to `command.Provider`. This is an explicit export reduction in the API candidate, not an approved compatibility guarantee. Both network provider interfaces remain exported with their existing method sets.

Metric enum adapters retain their existing integer-to-`int32` conversion: unknown values within the wire range survive, and wider integers truncate. Cleanup does not claim that metadata validation rejects unknown enum values. Successful absence remains distinct from an empty health result or watch summary; disabled telemetry still returns `(nil, nil)`.
