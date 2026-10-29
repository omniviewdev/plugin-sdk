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

- Independent domain comparison found C01 identity/value alignment; flagged public policy/capability choices.
- Independent provider comparison flagged generation/pin/framing and overlay APIs as implementation-added contracts, not automatically approved specification text.

The review-follow-up working tree passed SDK normal and race suites (21 tested packages each), compiling examples, and focused vet (artifact 1550); host normal tests (42 packages) and resource/invocation race tests (five packages, artifact 1554); and Kubernetes normal/race tests (11 packages each, artifact 1537). These are implementation checks, not public-contract approval.

Earlier candidate receipts below predate the reproduced R1–R8 findings and their fixes. They are historical evidence, not proof that the current native/UI acceptance has been rerun:

- SDK08421dc: full normal/race21 test packages each; receipt artifact975. This is source/test settlement only.
- Host Go source `09bf38f9`: all 42 normal-test packages and 11 expanded resource/invocation/utility race-test packages passed (artifact 1251). The host frontend passed 76 files/1,118 tests; the runtime passed 22 files/288 tests, including the real Go-produced C02 error corpus.
- Kubernetes `35e2650`: native Go source `27ceda1` passed all 11 normal-test and 11 race-test packages (artifact 1336). The final frontend passed 52 files/416 tests and the existing eight-test route suite. Obsolete wording assertions were removed, not repinned.
- The final clean managed launch attested host `0ec42cfe`, Kubernetes `35e2650`, SDK `a8416f8`, and UI `11a55a53` (artifact 1425). Build, dependencies, served source, and authentication were ready. Source reload remains unverified because this run disabled graph watching.
- The owned kind cluster exercised native Get/enumeration, explicit cursor paging, schemas/YAML, computed red/green diffs, a four-node relationship graph, canonical linked-resource contributions, dynamic-resource fallback, scoped current-resource search, live logs, exec input/output and exit, and real TCP forwarding/Stop. Scoped search ranks one acquired page; it is not a host query engine.
- Exact wire/body revisions agreed on a successful contextual write. A stale-incarnation attempt returned `CONFLICT`/409, preserved the draft, and left the recreated native object untouched. Malformed YAML failed locally with no native write. One earlier uninstrumented `INVALID_REQUEST` remains unexplained; no speculative error-taxonomy change was made.
- Exec output was observed in the real terminal buffer; headless WebGL pixel rendering is not claimed. Browser-helper worker interception was isolated using an equivalent native local-only egress policy; no application workaround was added. Temporary mutation diagnostics were removed. The owned application and kind sandbox were stopped after acceptance; private evidence was retained.
- Coordinated publication pins and this user SDK-spec review remain completion/merge gates. Native integration receipts do not approve the added public admission/framing/lifecycle contracts.
- No production historical resource store/query/follower/timeline is claimed.

## Review requested

Before calling plugin-sdk done: confirm which added contracts should remain public, whether admission should be consolidated, and which transport/lifecycle details should stay internal. The existing implementation is a reviewable proposal—not a settled API merely because tests passed.
