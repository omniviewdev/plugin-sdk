// Package resourcev2 defines the handwritten, provider-neutral resource API.
// This API is an implementation candidate awaiting public-contract review, not
// an approved contract. Plugin-author guides start at docs/README.md in the SDK
// repository. docs/resource-v2-api.md is the API reference; advanced ownership
// and admission rules live in docs/resource-v2-contracts.md.
//
// # Provider construction
//
// Provider requires only Catalog. Getter, Enumerator, NativeQuerier, Mutator,
// Actioner, connection lifecycle, and ancillary traits are independently optional.
// Method presence does not advertise support: the catalog qualifies each operation
// by type and scope. Create, mutation update, and delete have separate capabilities;
// OperationUpdate means advisory updates, never a write. Controller is optional.
// It supplies one native-client registry, admission, leases, and retirement fences.
// The executable examples show catalog-only and get-only composition, controller
// startup/stop, and generation issuance without Controller.
//
// # Identity and admission
//
// A connection is an explicit access route, not resource identity. Only the host
// issues authority; NativeAuthorityHint and Session labels are not grants.
// Callers pin the exact catalog observation used for admission. A matching
// provider/route-qualified pin takes precedence over the default pin, even when
// stale; duplicates fail closed. Pins never authorize work or refresh themselves.
// Custom providers own their source generation and use NewCatalogBindingEpoch
// and BindCatalogRevision; CatalogBindingEpoch is an integration accessor, not
// permission to parse opaque tokens.
//
// # Lifetimes and ownership
//
// Controller owns configured connections and active native generations. Start
// does not follow from discovery or a utility call. WithConnection supplies an
// owned configured-profile snapshot under an active lease; WithClient supplies
// the registered native client. Retirement cancels leased work, waits for it to
// finish, then destroys that client. Callbacks must honor cancellation and must
// not synchronously retire their own leased generation. Independent requests
// can invoke callbacks concurrently; shared native clients require synchronization.
// Do not mutate request/result containers concurrently with SDK use or retain
// callback containers for later mutation. Watch targets and connection snapshots
// have independent ownership; native opaque handles do not become copyable.
//
// # Public connection facts
//
// Config.ConnectionMetadata optionally observes public server facts with an
// already-started client. Use namespaced keys such as "example.server_version".
// Return success observations with UTC timestamps, unavailable observations with
// safe errors, and explicit removals. Omitted keys retain their previous state.
// Never publish credentials, raw native errors, connection configuration, resource
// inventory, or derived summaries. Sanitize endpoint userinfo, queries, and fragments.
// The SDK detaches values, validates the complete atomic update, and owns revisions,
// freshness, generation fences, and bounded snapshot streams. Get never acquires;
// Refresh never connects. Discovery invalidations must use
// Controller.InvalidateConnectionMetadata rather than independently publish cache
// observations. Concurrent refreshes share work, while invalidations coalesce one
// follow-up pass. Stop cancels acquisition and revokes publication before teardown.
// Consumers negotiate FeatureConnectionMetadata, subscribe before fetching, compare
// revisions numerically within a provider registration, and treat transport loss
// as unknown rather than current proof. Metadata does not grant authorization.
//
// # Honest results
//
// SourcePage is a finite acquisition page, not a final host query page. Unknown
// counts, partial coverage, unavailable schemas, and source consistency remain
// explicit. Empty oneOf matches nothing, not any scope. Cursors are opaque
// source continuations. Advisory updates provide neither replay continuity nor
// an atomic snapshot/watch handoff. ResourceOperationError public text must be
// safe to expose; raw diagnostics belong in Err and host redaction remains final.
// Generated messages, adapters, and wire validation belong to transport, not
// provider construction. Wire ceilings are safety bounds, not planner budgets or
// promises of history, search, schema normalization, or strong live continuity.
package resourcev2
