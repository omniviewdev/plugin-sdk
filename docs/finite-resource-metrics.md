# Build a metrics provider

A metrics provider connects a resource view to a measurement source. Implement two callbacks: **describe the available metrics**, then **fetch a batch of values**.

Omniview handles refresh scheduling and sharing requests between views. Your provider performs one bounded read when called.

[Register](#1-register-your-provider) · [Describe metrics](#2-describe-your-metrics) · [Return values](#3-return-measurements) · [Reference](finite-resource-metrics-reference.md)

## 1. Register your provider

Pass your real batch collector to this helper during plugin setup:

```go
import (
    "github.com/omniviewdev/plugin-sdk/pkg/sdk"
    "github.com/omniviewdev/plugin-sdk/pkg/v1/metric"
)

func RegisterMetrics(plugin *sdk.Plugin, collect metric.QueryResourcesFunc) error {
    return metric.RegisterPlugin(plugin, metric.PluginOpts{
        ProviderInfo: metric.ProviderInfo{
            ID:   "k8s-metrics",
            Name: "Kubernetes",
        },
        ResourceMetricsCatalogFunc: PodCatalog,
        QueryResourcesFunc:        collect,
    })
}
```

Use your existing provider ID and name. Both callbacks are required together; you do not need dummy legacy query or stream methods.

| Callback | What it does |
| --- | --- |
| `GetResourceMetricsCatalog(ctx, request)` | Lists metrics for the exact source, resource type and host-resolved mappings |
| `QueryResources(ctx, query)` | Fetches the requested measurements for all supplied resources |

Here, `ctx` is a `*types.PluginContext`. Its `Context` supports cancellation; its `Connection` contains the selected source's configured connection.

## 2. Describe your metrics

A catalog tells views what they can request. This example describes native Pod CPU:

```go
import (
    "time"

    "github.com/omniviewdev/plugin-sdk/pkg/types"
    "github.com/omniviewdev/plugin-sdk/pkg/v1/metric"
)

func PodCatalog(ctx *types.PluginContext, request metric.ResourceMetricsCatalogRequest) (*metric.ResourceMetricsCatalog, error) {
    resourceKey := request.ResourceKey
    catalog := &metric.ResourceMetricsCatalog{
        Source: metric.MetricSourceRef{
            PluginID:       "kubernetes",
            ConnectionID:   ctx.Connection.ID,
            Native:         true,
        },
        MaxTargets:   256,
        MaxSampleAge: 2 * time.Minute,
    }
    if resourceKey == "k8s:/pods" {
        catalog.Metrics = []metric.FiniteMetricDescriptor{{
            ID:          "kubernetes.pod.cpu.usage",
            Name:        "CPU usage",
            ResourceKey: resourceKey,
            Unit:        "cores",
            Shapes:      []string{"current"},
        }}
    }
    return catalog, nil
}
```

| Catalog choice | Meaning |
| --- | --- |
| `Source` | Actual plugin, connection, and backend supplying the measurements |
| `Metrics` | Stable IDs, display names, units, supported query shapes, and an optional [range-reuse opt-in](#let-hosts-reuse-overlapping-history) |
| `MaxTargets` | Maximum resources your provider accepts in one batch |
| `MaxSampleAge` | How old a live sample can be before it is delayed; zero means unknown |

Use the plugin's manifest ID in `Source.PluginID`, not `ProviderInfo.ID`. Advertise only metrics and query shapes your collector actually supports.

Native providers validate `request.Source` against the leased configured source. External providers also validate the datasource UID and kind. Resource targets retain their owner's references and routes; a Grafana connection is not a Kubernetes owner connection. The host admits those targets and supplies only approved scalar projections in `PublicData`, never owner credentials or caller bodies.

The SDK holds the selected source registry generation through catalog validation, collection, and response validation. Native queries also hold and revalidate every target's canonical catalog lease. External queries do not resolve owner routes through the source registry: only the host can authorize those foreign owners, read approved fields, and revalidate identity before dispatch and delivery. This is a host-to-plugin RPC contract, not an admission API for arbitrary caller-supplied owner claims. External collectors must use the admitted identity and `PublicData`, not acquire owner credentials or treat target routes as source connections.

## 3. Return measurements

`QueryResources` receives a resource list and a metric list. Return a result for each requested resource/metric pair, including explicit results for missing measurements.

For example, a query for **24 Pods × CPU and memory** expects **48 results**. Group collection by the source's batch API, not by individual table cells.

This helper builds a CPU result from a real backend measurement:

```go
import (
    "time"

    "github.com/omniviewdev/plugin-sdk/pkg/v1/metric"
)

func CPUSample(token string, cores float64, observedAt time.Time, window time.Duration) metric.ResourceMetricResult {
    return metric.ResourceMetricResult{
        ResourceToken: token,
        MetricID:      "kubernetes.pod.cpu.usage",
        Status:        "ok",
        Value:         &cores,
        ObservedAt:    &observedAt,
        Window:        window,
        IdentityScope: "name",
    }
}
```

Pass the source timestamp and measurement window, not the request completion time. This helper uses name-based attribution; use `incarnation` only when the source actually verifies the resource UID.

### Result checklist

- Preserve each target's `Token` in `ResourceToken`.
- Return CPU in **cores** and memory in **bytes**; leave display formatting to the UI.
- A measured zero is valid. An unavailable measurement has no numeric value.
- Honor `ctx.Context` cancellation; do not start a polling loop.
- Set `Complete: true` only when every requested pair has a result. A `missing` result still counts.

| Situation | `Status` | Numeric data |
| --- | --- | --- |
| Measurement available | `ok` | Value for live queries; points for history |
| No matching measurement | `missing` | None |
| Access refused | `denied` | None |
| Collection failed | `failed` | None |

Additional statuses and exact validation rules are in the [contract reference](finite-resource-metrics-reference.md#results-time-and-completeness).

## Live values or history?

| Shape | Return | Request fields |
| --- | --- | --- |
| `current` | Latest value | No range fields |
| `range` | Time-ordered points | `StartTime`, `EndTime`, `Step` |

Native metrics-server supports live values only. Repeated refreshes must not become invented history.

### Let hosts reuse overlapping history

Dashboards often request windows that overlap one the host already holds. If your range evaluation is a pure point series, set `RangeReuse` on the descriptor so the host can fetch only the part it lacks:

```go
metric.FiniteMetricDescriptor{
    ID:          "example.pod.cpu.usage",
    Name:        "CPU usage",
    ResourceKey: resourceKey,
    Unit:        "cores",
    Shapes:      []string{"current", "range"},
    RangeReuse:  true,
}
```

Set it only when every `ok` range result meets both conditions:

- Points sit on `StartTime + n*Step`, each evaluated at its own instant.
- For the same query context and `Step`, a point's value, or its gap, does not depend on `StartTime`, `EndTime` or which other points were requested.

Leave it false, the default, when a point depends on the requested window: a rate over the whole range, a lookback derived from its length, or a step or downsampling chosen from it. The opt-in requires the `range` shape. It says nothing about current values or unchanging history, and the host's cache TTL still bounds staleness.

Hosts reuse points across different bounds only from `ok` results with no `ObservedAt` and no `Window`, because those describe one requested domain. Setting them stays valid but limits reuse to the exact domain. Owners can declare the same promise on a `MetricDefinition`; your descriptor remains the opt-in and you may decline. The [contract reference](finite-resource-metrics-reference.md#range-reuse) has the exact rules.

## Choose the producer's unit

| Quantity | `Unit` |
| --- | --- |
| CPU | `cores` |
| Memory or storage | `bytes` |
| Duration | `seconds` |
| Count | `count` |
| Fraction | `ratio` |
| Percentage | `percent` |
| Network throughput | `bytes_per_second` |
| Event rate | `count_per_second` |

Use only quantities the source actually supplies. Do not invent a denominator, convert a count into a rate, or label a fraction as a percentage.

Finite RPC negotiation uses version 2. Rebuild the host and plugin together after changing the SDK; an older finite protocol returns `UNSUPPORTED`, never a legacy polling fallback.

## Next steps

- See the [Kubernetes collector](https://github.com/omniviewdev/omniview-plugin-kubernetes/blob/66f90b5/pkg/plugin/metric/native.go) for a real bounded batch implementation.
- See the [contract reference](finite-resource-metrics-reference.md) for limits, permissions, errors, and transport details.
