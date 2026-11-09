# Metrics provider contract reference

Start with [Build a metrics provider](finite-resource-metrics.md). This reference covers validation, permissions, timestamps, and transport behavior.

`pkg/v1/metric.FiniteQueryProvider` is an optional finite execution capability. It does not embed legacy `Provider`, and a finite-only provider need not implement Query or Stream. Existing legacy consumers retain their real implementations. `metric.Plugin{Impl: provider}` accepts finite-only implementations; unsupported legacy RPCs fail explicitly.

## Registration

Native plugins normally use `metric.RegisterPlugin` and set both optional `PluginOpts.ResourceMetricsCatalogFunc` and `PluginOpts.QueryResourcesFunc`. Setting only one is an error. The manager binds the resource connection registry through existing capability setup. Registration must finish before concurrent dispatch.

```go
opts := metric.PluginOpts{
    ProviderInfo: metric.ProviderInfo{ID: "k8s-metrics", Name: "Kubernetes"},
    ResourceMetricsCatalogFunc: func(pc *types.PluginContext, request metric.ResourceMetricsCatalogRequest) (*metric.ResourceMetricsCatalog, error) {
        // Source names the exact datasource; Context contains host-resolved mappings.
        return nativeCatalog(pc, request)
    },
    QueryResourcesFunc: func(pc *types.PluginContext, query metric.ResourceMetricsQuery) (*metric.ResourceMetricsResponse, error) {
        // pc.Connection is the configured source profile, not request metadata.
        // Honor pc.Context cancellation and use actual source timestamps.
        return collectNativeMetrics(pc, query)
    },
}
err := metric.RegisterPlugin(plugin, opts)
```

The example delegates collection to the plugin's real native collector; it is not a fallback implementation. Both callback signatures receive `*types.PluginContext`. Catalog additionally receives the canonical resource key; query receives the entire finite batch. No resource body is accepted.

## Authority and source identity

`MetricSourceRef` contains `PluginID`, `ConnectionID`, `Native`, `DatasourceUID`, and `DatasourceKind`. Native sources set `Native: true`, use the resource owner's plugin and connection, and leave datasource kind and UID empty. External sources set `Native: false` and require both datasource kind and UID. The manager requires PluginID to equal the actual plugin manifest ID captured by `RegisterPlugin`, and ConnectionID to equal the selected configured connection. Legacy ProviderInfo.ID remains independent.

Each `ResourceMetricTarget` contains a unique nonempty `Token`, an actual `resourcev2.ResourceReference`, and an actual `resourcev2.AccessRoute`. Tokens correlate delivery and confer no authority. Host callers must authorize every reference and preserve the existing principal/session and catalog observation context. The manager validates every route against the selected connection and nests `WithUtilityConnection` for **every target**. Every lease remains live through collection; every target's catalog and connection generation are checked before and after collection. One valid first target never admits the remaining batch. Existing utility admission checks catalog observations; it does not perform a native GET or establish resource-incarnation attribution on behalf of the source.

Source context must contain a nonnil caller context and selected connection. Honor cancellation; never replace it with Background. Do not synchronously retire the generation whose lease the callback holds.

## Catalog
`MetricOwnerCatalogProvider.GetMetricOwnerCatalog(*types.PluginContext)` is an optional owner capability independent of finite execution. Register it with `PluginOpts.GetMetricOwnerCatalogFunc`. It returns `MetricOwnerCatalog{Definitions, ScopeFields}` under the selected owner connection lease. `MetricScopeField` declares `Key`, `Name`, and `Description`; keys must be unique and nonempty. Catalogs allow at most 32 scope fields. Source executors need not implement this trait. `PluginClient.GetMetricOwnerCapabilities(context.Context)` discovers it explicitly through `GetFiniteCapabilities.owner_catalog`, including owner-only providers with no finite execution version.


`GetResourceMetricsCatalog(pc, ResourceMetricsCatalogRequest{Source, ResourceKey, Context})` returns `ResourceMetricsCatalog{Source, Metrics, MaxTargets, MaxSampleAge}` for the exact selected source. Every descriptor has ID, Name, ResourceKey, Unit, Shapes, Description, and an optional Source override for a host-composed mixed-source catalog. IDs must be unique and nonempty. ResourceKey must match the requested canonical type. Valid units are `cores`, `bytes`, `seconds`, `count`, `ratio`, `percent`, `bytes_per_second` and `count_per_second`, never display scales or legacy numeric enums. These units describe real producer quantities; the host never guesses a conversion or denominator. Shapes are explicit strings `current` and `range`. A source must not advertise a shape it cannot execute.

A descriptor may also set `RangeReuse`, an optional opt-in to coverage-aware range reuse. It requires the `range` shape; see [Range reuse](#range-reuse).

For example, the Kubernetes plugin defines `kubernetes.pod.cpu.usage` in cores and `kubernetes.pod.memory.working_set` in bytes. Its metrics-server source supports current values only; it does not fabricate history from polling.

The latest source catalog is authoritative. The manager rejects unadvertised metric IDs or shapes with UNSUPPORTED before collection. Catalog MaxTargets must be 1–256; the manager enforces the catalog's lower bound as well as the protocol bound.

`MaxSampleAge` is the source's freshness policy for current samples, expressed as a nonnegative Go duration (nanoseconds in JSON). Zero means freshness is unknown, not that samples expire immediately. With a positive policy and a known `ObservedAt`, a current sample becomes delayed after `ObservedAt + MaxSampleAge`; refresh or cache reuse must not renew that deadline. Native metrics-server advertises two minutes. This policy is not a cache TTL, polling cadence, or freshness rule for historical range points. Observation snapshots carry the authoritative catalog used to interpret their samples.

## Source configuration and approved projections

The host resolves source precedence from view selection, connection/per-metric binding, profile default, then native inheritance. A failed explicit selection is an error, never permission to fall back to native. `MetricSourceRef` identifies the source plugin, source connection, datasource UID and kind independently of every target's resource owner.

`MetricViewSourceSelection` stores a durable choice for `(OwnerPluginID, OwnerConnectionID, ResourceKey)`. `Mode` is `inherit`, `native`, or `external`; only `external` carries a `Source`. `Revision` supports conflict-checked writes. Inheritance removes the view override without deleting the connection binding or profile default. These models contain no credentials.

`MetricQueryContext` carries host-resolved semantic definitions, approved public scope, `MappingRevision` and `ProjectionRevision`. Scope is generic: at most 32 nonempty keys, with values at most 1024 bytes. Owner scope declarations approve keys; required inputs come from required scope bindings in effective applicable mappings. Resource bindings require an explicit RFC6901 `ResourcePath`. `ResourceMetricTarget.PublicData` carries only schema-approved scalar fields extracted from current owner observations. The host discards caller projections and retains owner admission, schema body guards, and source-generation guards. Sources read approved projections, not owner bodies, and must not infer owner authority from datasource credentials. No resource GET is introduced for projection.

`ProviderConnectionInput.Credential` is write-only: nil preserves the stored secret; an empty string clears it. `ProviderConnection.HasCredentials` exposes presence only. Credentials and authenticated transports stay inside the trusted provider's private store. Public metric configuration and presentation preferences use separate stores.

Sparse `MetricDefinitionOverride` fields inherit when nil. Removing an override resets it to the latest global definition. `MetricSourceDefaults.Source == nil` resets source inheritance. Host configuration revisions fence active collection and delivery; changing credentials retires the old transport generation and cancels its work.

## Query domains and bounds

`ResourceMetricsQuery` contains Source, Targets, MetricIDs, Shape, StartTime, EndTime, and Step. JSON names use snake_case; Go durations encode as nanoseconds. The protobuf transport uses typed Timestamp and Duration messages and presence-aware optional doubles.

- Targets: 1–256; tokens must be unique.
- MetricIDs: 1–32 unique nonempty IDs.
- Current: StartTime and EndTime must be zero; Step must be zero.
- Range: nonzero ordered times, duration at most 31 days, positive Step at most 31 days, and at most 10,000 implied samples.
- Response: at most one result per requested pair and at most 10,000 total points.
- Existing resource metadata byte/depth/node admission applies to all domain messages. These limits supplement transport message limits; limit overflow is explicit, never silently truncated.

A query evaluates the Cartesian product of its own targets and metric IDs. A coordinator combining requests must preserve each observer's requested pair set, not accidentally widen that product.

## Results, time, and completeness

`ResourceMetricsResponse` contains Results, Complete, and Warnings. Each `ResourceMetricResult` names exactly one ResourceToken/MetricID pair. Unknown pairs and duplicate pairs are invalid. `Complete: true` requires every requested pair, including explicit missing/error results. `Complete: false` may omit pairs; it does not change any individual result status.

Statuses are `ok`, `missing`, `unsupported`, `denied`, `invalid_context`, `invalid_mapping`, `ambiguous`, and `failed`. Non-ok results carry no Value or Points. Missing is never zero. Current ok results require a finite Value; zero remains a real sample. Range points contain Time and nullable Value; nil is a gap. NaN and infinity are invalid. Range point times must be ordered, unique, and inside the requested inclusive domain.

ObservedAt is the source observation time, when known. It is optional even for ok/current: an evaluation timestamp from a future query backend is not necessarily a raw sample timestamp. Never replace missing observation time with fetch completion, receipt time, or evaluation time. Native metrics-server collectors specifically use the actual metrics-server timestamp. Window is a nonnegative source measurement duration, not a UI polling interval. Negative windows and explicitly zero observation timestamps are invalid.

IdentityScope is source provenance: `incarnation` means the source actually verified the resource UID; `name` means the source attributed by name only. Empty means unspecified. This field is not an aggregation dimension. Metrics-server often omits UID/creation metadata, so native collection normally uses `name`; do not invent incarnation proof or add per-target GETs. The host still fences cache and delivery by the authoritative resource incarnation. Host cache fencing does not strengthen backend attribution.

Dimensions are source labels, not authority or source selectors. Message and Warnings contain safe diagnostics, never credentials. Permission-denied delivery must remove sensitive prior data at the host boundary.

## Range reuse

`FiniteMetricDescriptor.RangeReuse` (`range_reuse`) is an optional provider opt-in. It lets a host answer a range query from points it already holds for an overlapping window and fetch only the uncovered part. It defaults to false and is valid only when `Shapes` includes `range`; the SDK rejects any other combination with `INVALID_REQUEST` on both sides of the RPC.

Setting it promises that every `ok` range result for the metric is a **pure point series**:

- `Points` are pointwise evaluations on the grid `StartTime + n*Step`.
- For the same `MetricQueryContext` and `Step`, a point's value and presence (a nil value is a gap) do not depend on `StartTime`, `EndTime` or which other points were requested. Do not opt in when a result changes with the requested window: a rate or aggregate computed over the whole range, a lookback or interval derived from its length, or a step or downsampling chosen from it.
- `Value` and `ObservedAt` are unset and `Window` is zero. Range-wide sample metadata cannot be transplanted into another requested domain.

The promise does not say that a range point equals the current sample, or that source history never changes. Retained points remain subject to the host's existing cache TTL; the contract carries no per-point maturity or settled-history signal.

Cross-bound reuse is deliberately conservative. The host combines only complete `ok` results while `IdentityScope`, `Source` and `Dimensions` stay compatible. An opted-in `ok` response carrying aggregate sample metadata violates the declaration and may be rejected. Without the flag, range results may still carry `ObservedAt` and `Window`, but do not establish cross-bound coverage. Non-`ok` and incomplete results never establish cross-bound coverage.

`MetricDefinition.RangeReuse` is an owner's declaration that its authored definition meets the same promise. It also requires `range` in `SupportedShapes`, and `ValidateMetricDefinitionContract` rejects it otherwise. It is not an opt-in: the provider's descriptor decides, and a provider may decline an owner's declaration by leaving its own flag false. A host clears the declaration unless the effective definition is exactly the owner-authored one, so a user edit, override or other remapping never inherits it.

Absent and false mean the same thing in JSON and protobuf, and neither permits cross-bound reuse. Existing exact-domain caching is independent of this capability.

## Range-to-current reuse

`FiniteMetricDescriptor.RangeToCurrent` (`range_to_current`) is a separate provider opt-in requiring both `current` and `range` shapes. It promises that the latest range point can represent a cached current sample with the same value semantics, physical unit, identity, source, dimensions and sampling window, independent of range bounds and step. This is not implied by `RangeReuse`, and there is no inherited owner-definition opt-in.

The range result's `ObservedAt` must identify the actual source time associated with its terminal point, and `Window` that point's sampling window. Neither may describe the whole requested range. A host can promote only a complete `ok` result whose terminal point is numeric, whose cache entry has not expired, and whose source declares a positive known `MaxSampleAge`. Source time must be fresh and no later than the point's nonfuture evaluation time. An absent source timestamp or age policy prevents reuse; `Point.Time` never substitutes for source time. A terminal null remains a gap: never search backward for an older numeric value.

The host preserves actual source metadata and every existing authority, generation, configuration and projection fence. Exact current results, including known absence, take precedence. Among eligible range results, prefer the newest physical sample, not the most recently fetched historical query. Reuse reads the existing completed cache without adding a promoted-sample cache or extending TTL. Manual refresh bypasses completed reuse.

## Transport and errors

The typed `GetFiniteCapabilities` RPC negotiates version 2. `GetResourceMetricsCatalog` and `QueryResources` carry generated typed source, mapping and approved-projection messages. Old processes or providers lacking finite support return explicit UNSUPPORTED; the client never loops through legacy per-resource calls. Session/catalog context crosses the existing resource session transport. Remote classified resource errors retain their domain codes.

`range_reuse` (descriptor field 8, definition field 16) and `range_to_current` (descriptor field 9) are additive, so the finite wire version stays 2. Older providers remain opted out and older hosts ignore the fields. False fields are omitted from stored JSON.

INVALID_REQUEST denotes invalid source identity, duplicate tokens/IDs, domains, units, statuses, result shape, or numeric values. RESOURCE_LIMIT denotes count/metadata/sample bounds. UNSUPPORTED denotes absent finite capability or an unadvertised metric/shape. Connection retirement, catalog reset, authority, and cancellation retain existing resource registry error semantics. No compatibility adapter or synthetic sample converts these failures into successful data.

Exported `ValidateResourceMetricsQuery`, `ValidateResourceMetricsCatalog`, and `ValidateResourceMetricsResponse` allow callers to reject invalid domain data before dispatch or publication. They do not authorize references or substitute for connection leases.
