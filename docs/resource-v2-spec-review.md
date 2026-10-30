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

## Review requested

Before calling plugin-sdk done: confirm which added contracts should remain public, whether admission should be consolidated, and which transport/lifecycle details should stay internal. The existing implementation is a reviewable proposal—not a settled API merely because tests passed.
