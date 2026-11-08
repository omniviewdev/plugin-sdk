# Plugin diagnostics

`pkg/diagnostics` publishes a small, bounded set of in-plugin measurements to
the host. The host pulls them over the `PluginLifecycle.GetDiagnostics` RPC
about every two seconds and merges them into its own diagnostics collector
(Performance views, captures, comparison, `/metrics` and configured OTLP).
Plugins never push, never hold an endpoint or credential, and never choose their
own identity: the host labels every point with the plugin ID from its own record.

```go
calls, _ := diagnostics.Default().Counter("omniview.plugin.self.api.calls", "1")
calls.Add(1,
    diagnostics.Attr{Key: diagnostics.KeyOperation, Value: "list"},
    diagnostics.Attr{Key: diagnostics.KeyOutcome, Value: "ok"})
```

Register instruments once at start-up. Instruments are nil-safe, so a failed
registration (`ErrInvalidMetric`, `ErrTooManyMetrics`) never needs a guard.

## Rules

| Rule | Limit |
|---|---|
| Metric names | `^omniview\.plugin\.self\.[a-z0-9_.]{1,72}$` |
| Metrics per plugin | 24 |
| Points (series) per metric | 16 |
| Attributes per point | 4 |
| Attribute keys | closed enum: operation, outcome, phase, state, reason, cache, queue, direction, event |
| Attribute values | `^[a-z][a-z0-9_]{0,31}$` |
| Histogram bounds | 1–16, finite, increasing |
| Reply size accepted by host | 64 KiB |

Violations are dropped, never clamped, and mark the reply `truncated`
(sticky until the process restarts). Use fixed vocabularies for values. Never
record resource names, namespaces, queries, error text or credentials.

An instrument with no observations is absent from the snapshot, not zero. A
plugin built with an SDK older than this one answers `Unimplemented`; hosts
show it as unavailable.
