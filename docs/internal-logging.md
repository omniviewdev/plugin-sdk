# Internal Logging (`log`)

This SDK now has a dedicated internal plugin runtime logging API that is separate from `pkg/v1/logs` capability streaming.

## Why

- Context-required logging for automatic correlation across gRPC boundaries
- Structured logs for IDE filtering and operational diagnostics
- High-performance typed API with ergonomic sugar helpers
- Swappable backend model (default hclog-backed)
- Test-friendly sink pattern for asserting emitted logs

## Core API

```go
import logging "github.com/omniviewdev/plugin-sdk/log"

plugin := sdk.NewPlugin(sdk.PluginOpts{ID: "my-plugin"})

plugin.Log.Info(ctx, "connection started",
  logging.String("connection_id", connID),
)

plugin.Log.Infow(ctx, "resource synced",
  "resource_key", key,
  "count", count,
)
```

## Automatic Context Enrichment

When available in `context.Context`, the logger automatically adds:

- `request_id`
- `requester_id`
- `connection_id`
- `resource_key`
- `trace_id` / `span_id` (from gRPC metadata, including `traceparent`)

## Runtime Log Level

Use `plugin.SetLogLevel(...)` to update level dynamically:

```go
plugin.SetLogLevel(logging.LevelDebug)
```

## Testing With Sink Assertions

Use the recording sink:

```go
sink := &logtest.RecordingSink{}
log := logging.New(logging.Config{
  Name:    "test",
  Level:   logging.NewLevelController(logging.LevelTrace),
  Backend: logging.NewSinkBackend(sink),
})

log.Info(ctx, "hello", logging.String("k", "v"))
records := sink.Records()
```

## Migration Notes

This is intentionally separate from plugin log-source capability (`pkg/v1/logs`):

- `log`: internal runtime logging by plugin/SDK code
- `pkg/v1/logs`: feature-capability for end-user log streaming from infrastructure resources

Recommended migration path:

- Replace `log.Printf(...)` with `plugin.Log.Debug/Info/Warn/Error(...)`
- Replace ad-hoc zap/hclog calls in plugin code with `plugin.Log`
- Always pass request context through to log calls
