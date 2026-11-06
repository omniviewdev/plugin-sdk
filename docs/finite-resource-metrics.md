# Finite resource metrics API

`pkg/v1/metric.FiniteQueryProvider` is an optional finite execution capability. It does not embed legacy `Provider`, and a finite-only provider need not implement Query or Stream. Existing legacy consumers retain their real implementations. `metric.Plugin{Impl: provider}` accepts finite-only implementations; unsupported legacy RPCs fail explicitly.

## Registration

Native plugins normally use `metric.RegisterPlugin` and set both optional `PluginOpts.ResourceMetricsCatalogFunc` and `PluginOpts.QueryResourcesFunc`. Setting only one is an error. The manager binds the resource connection registry through existing capability setup. Registration must finish before concurrent dispatch.

```go
opts := metric.PluginOpts{
    ProviderInfo: metric.ProviderInfo{ID: "k8s-metrics", Name: "Kubernetes"},
    ResourceMetricsCatalogFunc: func(pc *types.PluginContext, key string) (*metric.ResourceMetricsCatalog, error) {
        // Return only descriptors this source implements for key.
        return nativeCatalog(pc, key)
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

`MetricSourceRef` contains `PluginID`, `ConnectionID`, `DatasourceUID`, and `DatasourceKind`. It identifies the selected metric source, not necessarily the resource owner. Native sources use `DatasourceKind: "metrics-server"` and an empty UID. The manager requires PluginID to equal the actual plugin manifest ID captured by `RegisterPlugin`, and ConnectionID to equal the selected configured connection. Legacy ProviderInfo.ID remains independent; existing provider selection IDs must not change for finite registration.

Each `ResourceMetricTarget` contains a unique nonempty `Token`, an actual `resourcev2.ResourceReference`, and an actual `resourcev2.AccessRoute`. Tokens correlate delivery and confer no authority. Host callers must authorize every reference and preserve the existing principal/session and catalog observation context. The manager validates every route against the selected connection and nests `WithUtilityConnection` for **every target**. Every lease remains live through collection; every target's catalog and connection generation are checked before and after collection. One valid first target never admits the remaining batch. Existing utility admission checks catalog observations; it does not perform a native GET or establish resource-incarnation attribution on behalf of the source.

Source context must contain a nonnil caller context and selected connection. Honor cancellation; never replace it with Background. Do not synchronously retire the generation whose lease the callback holds.

## Catalog

`GetResourceMetricsCatalog(pc, resourceKey)` returns `ResourceMetricsCatalog{Source, Metrics, MaxTargets, MaxSampleAge}`. Every descriptor has ID, Name, ResourceKey, Unit, Shapes, and Description. IDs must be unique and nonempty. ResourceKey must match the requested canonical type. Units are physical bases `cores` and `bytes`, not display scales or legacy numeric enums. Shapes are explicit strings `current` and `range`. A source must not advertise a shape it cannot execute.

Native Pod IDs are `kubernetes.pod.cpu.usage` in cores and `kubernetes.pod.memory.working_set` in bytes. Native metrics-server supports current only. It does not fabricate history from polling.

The latest source catalog is authoritative. The manager rejects unadvertised metric IDs or shapes with UNSUPPORTED before collection. Catalog MaxTargets must be 1–256; the manager enforces the catalog's lower bound as well as the protocol bound.

`MaxSampleAge` is the source's freshness policy for current samples, expressed as a nonnegative Go duration (nanoseconds in JSON). Zero means freshness is unknown, not that samples expire immediately. With a positive policy and a known `ObservedAt`, a current sample becomes delayed after `ObservedAt + MaxSampleAge`; refresh or cache reuse must not renew that deadline. Native metrics-server advertises two minutes. This policy is not a cache TTL, polling cadence, or freshness rule for historical range points. Observation snapshots carry the authoritative catalog used to interpret their samples.

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

## Transport and errors

The typed `GetFiniteCapabilities` RPC negotiates version 1. `GetResourceMetricsCatalog` and `QueryResources` use generated finite messages. Old processes or providers lacking finite support return explicit UNSUPPORTED; the client never loops through legacy per-resource calls. Session/catalog context crosses the existing resource session transport. Remote classified resource errors retain their domain codes.

INVALID_REQUEST denotes invalid source identity, duplicate tokens/IDs, domains, units, statuses, result shape, or numeric values. RESOURCE_LIMIT denotes count/metadata/sample bounds. UNSUPPORTED denotes absent finite capability or an unadvertised metric/shape. Connection retirement, catalog reset, authority, and cancellation retain existing resource registry error semantics. No compatibility adapter or synthetic sample converts these failures into successful data.

Exported `ValidateResourceMetricsQuery`, `ValidateResourceMetricsCatalog`, and `ValidateResourceMetricsResponse` allow callers to reject invalid domain data before dispatch or publication. They do not authorize references or substitute for connection leases.
