# Proposed plugin-sdk versus resource specification

Status: **proposal for user review, not SDK acceptance**. SDK source/test receipts do not satisfy this review gate. SDK PR21 remains draft and must not merge before review.

## Conclusion

The canonical identity/value model matches C01. The replacement public SDK is broader than that mapping. Most complexity accumulated around generation admission, connection lifecycle, and resource-bearing utilities. Those are concrete safety problems, but their current public API shape was chosen during implementation rather than prescribed by the resource specification.

Recommended direction before freezing the SDK: retain the canonical model and optional operations; review a smaller provider-facing admission surface; keep transport framing private; explicitly approve lifecycle/overlay and utility scope. No cleanup below has been silently applied.

## Specification baseline

Paths are relative to the managed cohort. `S` is `plugin-sdk`; `H` is `omniview`.

- Conceptual resource foundation: H/docs/superpowers/specs/2026-10-17-resource-foundation-design.md, especially lines12–50,85–175,187–230,322–340. Its diagrams and illustrative types are not frozen wire definitions.
- Concrete mapping and handoff: H/docs/superpowers/specs/2026-10-24-resource-query-contracts.md, §§1 and8–9. Later handoff/accepted P01 evidence supersedes the document's historical numeric-dependency hold; that does not authorize declaring every later engine implemented.
- Trusted invocation/error boundary: H/docs/superpowers/specs/2026-10-23-trusted-invocation-errors.md.
- History contracts remain proposals. No historical ingestion/store/query engine is accepted by this SDK work.

## Mapping and scope

|Area|Specification|Current proposal|Assessment|
|---|---|---|---|
|Identity versus access|Connection is a route, not identity.|ResourceAddress(provider,authority,type,scope,id), AccessRoute(connection), optional incarnation.|Matches C01; keep separate. S/pkg/resource/v2/domain.go16–46.|
|Authority|Host assigns canonical authority from provider evidence.|Per-type NativeAuthorityHint; host assignment, retarget/restart fencing, conservative unknown continuity.|Separation matches; per-type hint shape and restart policy are added design choices. S/pkg/resource/v2/domain.go126–137.|
|Scopes|Exact identity tuple differs from explicit query selection.|Repeated entries, duplicate rejection, explicit any/oneOf, separate identity/query dimensions.|Matches. Query-only repository selection must never become identity or permission proof. S/pkg/resource/v2/controller.go445–490.|
|Values|One lossless tagged value model.|Decimal int/uint strings, finite double including -0, seconds/nanos timestamp, bounded recursive maps/lists.|Matches mapping. Go/proto/TS are adapters, not independent semantic domains. S/pkg/resource/v2/domain.go48–62.|
|Representations/schemas|Schema and representation revisions are not identity; absent/available/unavailable differ.|Explicit bindings and shared document references; unavailableReason is a safe optional machine code.|Core matches. unavailableReason is a deliberate integration extension, not an original named C01 field. S/pkg/resource/v2/domain.go65–91.|
|Provider operations|Catalog does not imply universal CRUD, enumeration, query, or watch.|Catalog-only Provider plus independently optional Getter/Enumerator/NativeQuerier/Mutator/Actioner and ancillary traits.|Matches accepted direction. Controller/options API is an implementation design choice. S/pkg/resource/v2/domain.go210–250.|
|Capabilities|Support differs from authorization/planning.|Positive resource operation/scope declarations plus separately negotiated transport traits.|Useful split; not a complete planner capability language. TS now distinguishes ResourceCapability and PluginCapability.|
|Source pages/updates|Native acquisition is not final host query membership or result transport.|Finite SourcePage and advisory bestEffort updates.|Correct boundary. No host planner, durable source coordinator, or result lease engine is supplied.|
|Budgets|Query-profile reference policy has smaller predicate/value limits.|SDK candidate transport: 32MiB frame,16MiB body,depth64,262144 nodes.|Different policy layers, not one interchangeable budget. Explicitly approve transport bounds; P13 still owes lower query policy enforcement. S/pkg/resource/v2/validation.go14–30 versus specification§9.|

## Added API surface that deserves explicit review

|Current addition|Problem it solves|Concern / simpler proposed direction|
|---|---|---|
|Catalog revision embeds native client binding epoch; public epoch constructors/accessor.|Native config changes could otherwise return clusterB data under authorityA; metadata churn must not rekey unchanged resources.|Generation fencing is necessary. Embedding/parsing source epoch through Catalog.Revision is a new contract. Consider explicit binding generation inside one admission context.|
|Default and provider/route-qualified catalog pin helpers.|Each accepted source/relationship route needs its own observed generation; source pin cannot stand for target pin.|Two helper families and fallback precedence increase public rules. Prefer one route-qualified admission shape if direct/custom-provider use stays practical.|
|Public binary metadata framing helpers in resourcev2.|Strict bounded transport of admission observations.|Strongest layer leakage. Keep framing/aggregate header limits inside transport; expose validated domain admission data only.|
|Controller and transport both validate admission/results.|Direct Controller calls and custom remote providers both need protection.|Share primitives, not wholesale removal of checks. Preserve post-dispatch retirement fences.|
|WithClient and WithConnection / utility registry binding.|Utilities need actual started native clients and configured profiles; caller-provided data is not a grant.|Keep distinct only where native-client access versus profile access is intentional. Avoid multiple overlapping provider-facing lease facades.|
|Lifecycle/browser/overlay/readiness/context traits.|Durable desired profiles differ from native discovery; stopped versus unknown versus connected must be truthful without probes.|Useful, but transactional overlay ownership and restart behavior extend the foundation sketch and need explicit approval. CONNECTED means registered client, not network health.|
|Resource-specific exec/log/metric/networker RPCs.|Canonical values, references and generation admission must survive existing utility paths; old Struct/ordinary JSON would lose exact values or bypass admission.|Coordinated boundary migration is necessary if resource v2 ships. It is not permission to replace utility engines or add unrelated features.|
|SDK ResourceOperationError with provider presentation candidates.|Preserve native classification across transport.|Host AppError remains canonical. Narrow provider-facing presentation fields or clearly document that they are untrusted diagnostics.|
|Many exported leaf protobuf adapters.|Transport/domain conversion.|Keep only externally needed conversions public after a caller audit; avoid exposing implementation machinery as core provider API.|

## What I would keep versus simplify

**Keep:** canonical addresses/references/routes; exact values; identity/query dimension separation; truthful schema availability; optional operations; explicit feature negotiation; one native connection registry; fail-closed generation checks; separate source pages and host results; matching utility consumers.

**Review for simplification before SDK completion:** a single route-qualified admission context; private wire framing; smaller exported adapter/helper surface; clearer separation of provider-facing contracts from host/transport internals; smaller lifecycle/controller files without duplicate semantic models.

**Do not restore:** old Find/mandatory-CRUD stacks, GVK identity aliases, resource fallback through legacy utility RPCs, caller-supplied authority/grants, fake stopped-state catalogs, or compatibility shims.

## Evidence and limits

- Independent domain comparison found C01 identity/value alignment; flagged public policy/capability choices.
- Independent provider comparison flagged generation/pin/framing and overlay APIs as implementation-added contracts, not automatically approved specification text.
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
