// Package metric provides metric queries and subscription streams.
//
// Resource inputs use the canonical resource/v2 Value without Struct conversion.
// ResourceData, Reference, or Route selects resource intent; opaque ResourceKey
// selectors and Params alone do not. Query automatically selects QueryResource
// for resource intent and never retries the legacy operation.
//
// StreamMetrics accepts only nonresource frames. StreamResourceMetrics is an
// explicit mixed resource/nonresource lane: its wire-v2 opening handshake must
// succeed before a client publishes the stream. Every subsequent subscription
// and control frame uses wire version two. Resource references and access routes
// remain optional actual inputs, never synthesized from ordinary selectors.
//
// QueryContext.Context and StreamContext.Context carry callback cancellation.
// Polling retains the complete resource input and Params for every query.
// StreamOutput.OperationError carries typed admission and transport failures;
// Error remains the native provider diagnostic field.
//
// ResourceSetProvider is the explicit finite batching capability. Configure
// PluginOpts.ResourceSetProvider, Configuration and Catalog independently.
// Resource tokens identify results; an empty resource ID never means a batch.
// Hosts admit canonical references under ResourcePluginID and recheck source
// authorization after execution. Managers lease only the provider connection;
// foreign source references never enter the provider catalog. PublicData contains
// an owner-approved projection, not source connection credentials or full objects.
//
// MetricDefinition IDs describe stable units, semantics, dimensions and shapes.
// Bindings select a connection-scoped datasource and sparse overrides; removing
// an override inherits the current global definition. Missing, denied, ambiguous,
// unsupported and invalid-context results contain no numeric value.
//
// The separate MetricResourceSet RPC service uses MetricContractVersion and
// bounded strict JSON with canonical resource/v2 values. Generate SDK transport
// using "buf generate --path proto/v1/metric/resource_set.proto". Older providers
// return explicit unsupported capability errors, never a legacy query fallback.
// ProviderConnectionInput.Credential is write-only; ProviderConnection responses
// contain only public configuration and credential presence/reference. Definition
// validation does not claim sampled execution; use admitted QueryResources for
// live identity, unit and result checks.
//
// Discovery is a separate optional capability configured through
// PluginOpts.Discovery. MetricDiscoveryRequest permits at most 100 returned
// values and exact label matchers, not caller-supplied selectors. Providers
// validate label/metric grammar and enforce response truncation. Field bindings
// support only identity, lowercase and uppercase transforms, never scripts.
// A resource binding's ResourcePath is an RFC6901 pointer. Hosts extract only
// approved scalar leaves from an admitted source read and flatten them under
// Field in PublicData. Providers never receive the complete source object.
// Declared definition dimensions distinguish series within a resource/metric
// pair; undeclared dimensions and exact duplicate dimensional identities fail.
// Saved provider ConnectionID travels independently of an optional configured
// resource Connection profile, including over RPC. Standalone metric providers
// need no resource capability or synthetic registry lease. Selected provider
// context and request source must agree before query or discovery dispatch.
package metric
