# Provider-native conformance corpus

## P45 canonical boundary

`internal/resourceconformance/{corpus,native,oracles}.go` retains the P44 credential-free native records and independent finite semantic oracles unchanged. The `current_*_test.go` probes now target the new-major handwritten `pkg/resource/v2` domain through an actual disposable go-plugin child and `pkg/resource/v2/plugin.GRPCPlugin{Impl: provider}`. Generated protobuf remains transport-only. The fixture implements `Provider` directly, without the legacy resource controller, mandatory CRUD methods, GVK metadata or v1 imports.

The settled integration barrier caught two fixture mismatches: undeclared route-catalog types return `UNSUPPORTED`, and advertised update support requires an explicit `bestEffort` capability guarantee. Corrected expectations passed in the normal/race suites recorded below. Earlier P44 verification receipts apply only to the old boundary and do not certify this port.

## Native profiles

| Profile | Preserved evidence |
| --- | --- |
| Cloud | Opaque ARN/OCID-like IDs; slash, percent, delimiter and Unicode bytes; account/region scope entries and collisions; tag arrays; same-element-correlation counterexample; null versus missing; quoted map keys and injection-shaped strings; exact large signed/unsigned integers; native timestamp; cursor pages; bundled/absent schema; explicit partial/eventual listing |
| Container | Engine-local colliding IDs; label maps; optional incarnation and opaque source revision; bundled/absent schema; actual optional best-effort update transport probe, separately scoped below |
| Hypervisor | VM/disk IDs `101` and `7` collide across node scope; host/VM/disk links checked from transported bodies; bundled/absent VM schema; paged and preselected single-node reads; snapshot-only without invented watches |

All 13 native scenarios use explicit `AccessRoute` and canonical `ScopeEntry` identity tuples. Get sends the full reference, so colliding local IDs remain unchanged and resolvable. Connection IDs stay access provenance, not address components. Optional incarnation and source revision remain absent when unknown. Revisions remain opaque equality tokens, never lexical clocks.

The caller supplies the host-issued canonical authority in the admitted target/reference. The fixture validates native provider/type/scope and preserves that authority in Get, Enumerate, NativeQuery and best-effort updates; corpus authority strings are oracle labels, not provider authority issuance. A separate child gRPC probe uses a different host-issued authority to prevent accidental dependence on native corpus labels. Each native corpus type descriptor leaves its optional authority hint absent.

Native document conversion uses `json.Decoder.UseNumber` and an explicit fixture policy: integer tokens become exact signed or unsigned values; `createdAt` is a declared native timestamp. No integer passes through `float64`, and no rounded JSON number is claimed to regain precision. Resource bodies cross gRPC as recursive tagged map values, not raw JSON bytes. The independent body and link oracles consume reconstructed native JSON only after transport, retaining exact numeric tokens.

Each page asserts unknown count with no count value, source coverage, best-effort consistency and opaque cursor preservation. Catalog discovery separately reports complete coverage; absent native authority hints remain unknown, never synthesized from route or endpoint. A partial final page stays partial. Exhaustion means the selected accessible listing ended, not unrestricted inventory or an atomic snapshot. Schema binding transports available document/revision provenance or explicit absence with validation `none`; schema transport does not establish P07 schema admission. Snapshot-only freshness remains a native corpus expectation: finite acquisition makes no live freshness or gap-free guarantee.

## Independent optional operations

Catalog is the sole required provider method. Test implementations genuinely omit optional methods:

- Catalog-only has no Getter, Enumerator or NativeQuerier.
- Get-only has Getter but no Enumerator or NativeQuerier.
- Enumerate-only has Enumerator but no Getter or NativeQuerier.
- Native-query-only has NativeQuerier but no Getter or Enumerator.
- Read-only has Getter and Enumerator but no mutation, action or native query implementation.

The tests check these native method sets and then exercise each profile over real child gRPC. Route-qualified catalog capabilities are support authority; a transport client's broad method set is not capability discovery. Unsupported remote operations must return the existing typed error class rather than successful empty results. No fake mandatory Find/Create/Update/Delete implementations remain.

A structurally valid undeclared type, representation or operation is `UNSUPPORTED` at its qualified route catalog. Malformed identity/scope/cursor requests remain `INVALID_REQUEST`; provider mismatch is also invalid request. The fixture follows this catalog admission distinction rather than requiring dispatch to a native implementation for unsupported targets.

The native-query-only fixture accepts `fixture.compute.v1` with tagged int64 `9007199254740993`, computes addition by one in the child and returns a map containing int64 `9007199254740994`, pre-epoch timestamp `1969-12-31T23:59:59.999999999Z`, and a heterogeneous list containing uint64 maximum, null and true. Assertions consume actual gRPC results and tagged JSON serialization. This is computed result codec evidence, not input echo or CEL evaluation. The SDK has no CEL dependency. Host-owned selected native CEL evaluation and Node result proof remain separate acceptance work; the fixture recipe is intentionally simple enough to reproduce there without publishing a fixture API.

## Cancellation, isolation and teardown

A test-only file receipt signals that a child request reached its cancellation wait. The parent cancels that actual RPC, checks cancellation, waits for the child cancellation receipt and makes a successful independent request. Connection stop writes a close receipt and rejects subsequent acquisition. Explicit process teardown asserts child exit; test cleanup also kills children after failures.

The child receives temporary HOME, USERPROFILE, XDG, state and short Unix socket roots. `SkipHostEnv: true` prevents go-plugin from appending ambient credentials, kubeconfig or workspace selection. A subprocess isolation probe sets harmless ambient sentinels and rejects their variable names in the child before serving. The optional existing `P44_CLEAN_ENV_LAUNCHER` hook remains a test-launch mechanism, not a legacy resource API.

This is credential/profile isolation, not hostile-plugin sandboxing or network isolation. Temporary plugin metadata and waiting/cancelled/closed receipts are test artifacts, not production state.

## Best-effort updates and P05 scope

The retained native container update changes `running` to `paused` and revision `event:z9` to `event:a1`. The optional `BestEffortUpdateProvider` fixture traverses the actual new-major child gRPC update transport. The probe waits for listener readiness before starting the native source, waits for its advisory Synced state, then releases the finite native change through a test-side file barrier. It checks transported canonical references, exact engine scope, stable incarnation, tagged bodies, opaque source revisions and explicit `bestEffort` guarantees against `CheckChanges`. It stops the native watch and cancels the listener before source teardown.

These probes passed in the normal and race SDK suites recorded below. No v1 import or fixture-specific event RPC remains. The approved advisory contract preserves useful live workflow without claiming atomic snapshot/event cutover, replay continuity, gap detection, gap-free ordering or reliable recreation removal. Synced means native readiness, not a point-in-time snapshot. P05 owns the separate stronger continuity contract; this receipt does not certify that contract.

## SDK verification receipt — 2026-10-28

The selected SDK worktree passed:

```sh
GOWORK=off go test ./... -count=1
GOWORK=off go test -race ./... -count=1
GOWORK=off go test ./pkg/resource/v2/plugin -run '^TestActualGRPCMultiMiBResourceBodies$' -count=1 -v
```

Both full suites reported 20 packages passing and 17 packages without tests.
They include the real child-process profiles, optional finite-operation variants,
canonical authority binding, best-effort update readiness, cancellation and
teardown, strict JSON/protobuf negatives, typed C02 errors, and actual gRPC
connection overlay/native snapshot workflows. Independent review identified
listener retirement, replacement-watch admission, connection response binding,
and unknown remote error-code defects; fixes and regression tests are included.

The dedicated actual-gRPC body probe admitted a 2,097,152-byte Kubernetes-shaped
payload in a 2,097,530-byte response (2.810917 ms), and an 8,388,608-byte payload in
an 8,388,986-byte response (8.142708 ms). These single local observations are not
latency guarantees or production policy approval. Over-body, aggregate-frame,
node, depth and field limits have separate negative probes.

The host read adapter, selected native CEL/Node computed-result proof, permission
admission, actual app/utility smoke, schema normalization and cross-repository
independent acceptance require their own receipts. No release or issue closure
is claimed here.

## Reviewed SDK follow-up receipt — 2026-10-28

The settled follow-up source passed both `GOWORK=off go test ./... -count=1`
and `GOWORK=off go test -race ./... -count=1`: 20 packages passing and 17
packages without tests in each run. This receipt includes the corrections below;
the earlier payload timings remain observations of the preceding source.

### Type-qualified authority hints

Optional `NativeAuthorityHint` now belongs to each exact `ResourceType`
(`nativeAuthorityHint` in JSON; protobuf field 8). `Catalog` has no shared hint
or compatibility fallback; its former protobuf field 4 is reserved. Resource
addresses and identity dimensions are unchanged. The actual-gRPC mixed-catalog
probe preserves distinct fixture hints and an unhinted descriptor without
inheritance. This proves transport qualification, not approval of a local Helm
identity scheme. Local Helm types remain unhinted absent separate review.
Raw gRPC rejects retired catalog field 4 as `UNSUPPORTED`.

### Owned connection configuration

The new native-HTTP regression was exercised before the fix: nine returned or
retained-input boundaries mutated stored configuration before explicit apply,
failed to cancel the old native request, destroyed no client, and continued
reading the old source. After the fix, the same probes cancel the actual old
HTTP request, destroy its client exactly once, and read the replacement source.

Native/overlay/update ingress, inventory/status egress, and native callback/session
configuration now have independent mutable ownership. Internal immutable
inventories share values without repeated copies. Tests preserve nested typed
containers, raw bytes, signed/unsigned integer boundaries, `json.Number`, signed
double zero, nil containers, lifecycle pointers/triggers, and private credentials
without a JSON roundtrip. Provider-retained and per-request context mutations
cannot alter inventory or subsequent client leases.

### Explicit relationship target bindings

`RelationshipResolutionRequest` carries the source and at most 256 explicit
host-admitted `ReadTarget` bindings. Both server and client validate returned
provider/authority/type/route/scope against those bindings. Extra identity
dimensions survive projection onto declared query dimensions. Ambiguous,
missing or out-of-scope bindings fail explicitly rather than fabricating an
address, borrowing source authority, repairing identities, or dropping links.
Bindings are not permissions and do not authorize inventory scans.

Actual default-protobuf gRPC probes admit 256 valid bindings and reject the
257th with `RESOURCE_LIMIT` and zero dispatch before generated decoding.
Direct handwritten adapter callers also reject excess cardinality before
conversion/allocation. Strict JSON, malformed RPC, authority/scope escape and
large-oneOf overlap regressions passed in both full suites.

Independent provider-lifecycle and wire-admission reviewers accepted these
source corrections with no remaining findings in their bounded re-reviews.
They did not run checks. Host/provider integration, native completeness,
authorization, app smoke and final cross-repository acceptance remain parent
gates; this SDK receipt does not certify them.

## Resource exec correction receipt — 2026-10-28

Resource exec now uses the same exec service's explicit `CreateResourceSession`
RPC and session engine. Canonical Value field 9 and exact resource wire version
`2` field 10 replace reserved Struct field 8. Any resource intent selects this
operation, including body-free selectors. Optional actual C01 reference/route
fields retain their canonical meaning; utility selectors remain opaque inputs
and neither invent identity nor require an unrelated Get.

Actual gRPC probes transport signed/unsigned boundaries, nested values,
pre-epoch timestamps, positive and negative zero, and 70 KiB/3 MiB bodies.
Adversarial raw request and response probes cover malformed tagged values,
duplicate fields, invalid UTF-8/version, legacy field 8, and wire/body bounds.
An actual old server returns unsupported without an old native-method call.
Typed native C02 classes survive the new RPC; transport loss remains distinct.
Independent SDK-server and malicious-generated-server probes reject missing or
changed response IDs while accepting an actual generated ID when none was
requested.

Manager regressions exercise native admission cancellation/deadline, no orphan
on rejection, and detached lifetime only after acceptance. A real Manager-backed
nonresource gRPC probe performs ordinary creation, attachment, output streaming,
closure and close-signal forwarding; resource admission does not replace that
terminal lifecycle.

The final settled SDK barrier passed both `GOWORK=off go test ./... -count=1`
and `GOWORK=off go test -race ./... -count=1`: each reported 20 passing packages
and 17 packages without tests. The first exec barrier exposed incorrect
`int`/`uint` test fixture tags; canonical `int64`/`uint64` tags are corrected.
Independent wire-admission and lifecycle source re-reviews reported no remaining
findings; reviewers did not execute checks. Host/TypeScript bindings, native
Kubernetes/app smoke, publication and coordinated acceptance remain parent gates.

## Catalog/source generation and remaining utilities — 2026-10-28

Catalog admission now pins an explicit accepted observation through bounded
binary metadata; SDK clients do not substitute a current catalog cache.
`CatalogBindingEpoch` distinguishes actual native client/configuration binding
from ordinary catalog metadata churn. Controller and custom-provider framing use
the same SDK implementation; custom fixtures issue an epoch per actual source
instance. Direct controller and RPC checks fence admission and publication.
Cross-route relationships carry independently qualified accepted observations,
with a 32 KiB aggregate catalog-header ceiling and no new identity DTO.

`current_generation_test.go` uses a disposable canonical SDK child and real
native HTTP clients. Independent child-side refresh controls invoke actual
`ApplyConnections`/`StartConnection`; file receipts prove native entry,
cancellation, ignored-cancellation return and refresh completion. Cases cover
A→B before a request, in-flight retirement, current-generation success and
A→B→A rejection of the retired nonce. Successful bodies are tagged maps, not a
scalar fixture bypassing record admission.

Logs, metric query/subscription and resource forwarding bodies now traverse
explicit new resource operations as canonical Value. Native builders,
resolvers, callbacks and forwarding snapshots retain exact integers,
timestamps and nested negative zero without Struct. Reference-bearing
operations require the accepted catalog; opaque nonreference utility inputs
and ordinary cluster metrics do not fabricate one or acquire a Get permission.

Metric tests bind the sole started resource registry to real local HTTP
endpoints. Two simultaneous actual connections share the same local
subscription ID without stealing output or cancellation. A blocked native poll
observes cancellation after source-side profile refresh; stopped admission does
not auto-start, explicit restart samples the replacement endpoint, and stale/
retired reference observations reject. Custom streams receive actual configured
profiles and request contexts. Completion releases the real connection lease.

Immutable per-lane admission and compact used-ID tombstones prevent queued
retired reference output from inheriting a later nonreference subscription.
Actual RPC tests reject ID reuse and enforce the candidate 4096-used-ID and
32 MiB retained-metadata bounds before native work. Hosts use fresh opaque wire
IDs; these are utility routing IDs, not resource identity.

Wire-admission and lifecycle re-reviews closed the reported source findings.
They ran no checks. The SDK verification receipt below is separate from host
authority/restart admission, TypeScript bindings, Kubernetes/native integration,
app smoke and coordinated publication acceptance.

### Final settled SDK verification receipt

Both `GOWORK=off go test ./... -count=1` and
`GOWORK=off go test -race ./... -count=1` passed: **21 packages passing and
16 packages without tests in each suite**. This includes the actual child
generation controls, two-route relationship RPCs, native HTTP metric isolation,
all utility/raw-wire regressions, and live authenticated HTTP survival across a
display-only rename. The latter preserves the active client, endpoint,
credentials, accepted catalog revision and source epoch while exposing the
updated configured name.

Earlier settled normal barriers exposed invalid scalar-root/native fixture
bodies, unpinned callsites, incidental name-only teardown assumptions, metadata
overwrite of accepted pins, and utility fixture/context mismatches. Corrections
retain native consumer assertions rather than bypassing admission. The final
normal barrier passed before the full race suite ran. Independent source reviews
closed the identified wire/provenance/watch-reuse and lease-completion findings.
Neither these SDK results nor source reviews certify parent host/provider/app
integration or authorize publication.
