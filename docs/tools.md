# Expose a custom tool

[Docs home](README.md) · [Capabilities](capabilities.md) · [Resource guide](resources.md)

Use a custom tool for work that is not already a resource operation. The host builds resource tools from the route's catalog; do not wrap Get, enumeration, deletion, or resource logs in another plugin tool.

## Choose the right surface

| Your operation | Author it here |
| --- | --- |
| Read or enumerate resources | Resource catalog and optional provider operations |
| Create, update, or delete resources | Independent resource capabilities and `Mutator` |
| Run an operation on a resource type or instance | Action descriptor and matching action target |
| Read resource logs | Existing logs handler and source engine |
| Inspect the plugin itself or run non-resource work | Custom `tools.Tool` |

Register only capabilities the plugin implements. MCP publication and permission are host decisions, not consequences of registration.

## Register a connection-free tool

This entrypoint assumes an installed `notes` manifest, as described in the [quickstart](quickstart.md#installation-and-launch-boundary). Add `tools` to that manifest's capabilities without removing its other capabilities.

```go
package main

import (
    "encoding/json"
    "log"

    "github.com/omniviewdev/plugin-sdk/pkg/sdk"
    "github.com/omniviewdev/plugin-sdk/pkg/types"
    "github.com/omniviewdev/plugin-sdk/pkg/v1/tools"
)

func main() {
    plugin := sdk.NewPlugin(sdk.PluginOpts{ID: "notes"})
    readOnly := true

    err := tools.RegisterPlugin(plugin, tools.PluginOpts{
        Tools: []tools.Tool{{
            Descriptor: tools.Descriptor{
                Name:        "version",
                Title:       "Plugin version",
                Description: "Return this plugin's implementation version.",
                Scope:       tools.ScopePlugin,
                InputSchema: tools.ObjectSchema(nil),
                OutputSchema: tools.ObjectSchema(map[string]any{
                    "version": map[string]any{"type": "string"},
                }, "version"),
                Annotations: tools.Annotations{ReadOnly: &readOnly},
            },
            Handler: func(_ *types.PluginContext, _ json.RawMessage) (*tools.Result, error) {
                return tools.JSONResult(map[string]any{"version": "development"})
            },
        }},
    })
    if err != nil {
        log.Fatal(err)
    }
    plugin.Serve()
}
```

A plugin-scoped call has no connection. Supplying a route or selected connection metadata is an error; the SDK does not choose a default connection.

## Use an admitted native connection

Choose `tools.ScopeConnection` only when the operation needs a configured native connection.

```mermaid
sequenceDiagram
    participant Agent as MCP caller
    participant Host as Host admission
    participant SDK as Tools manager
    participant Registry as Existing native registry
    participant Handler as Your handler
    Agent->>Host: Tool name and arguments
    Host->>Host: Authorize target and obtain required consent
    Host->>SDK: CallInput with explicit AccessRoute
    SDK->>Registry: Lease configured generation
    Registry->>Handler: Admitted PluginContext
    Handler->>Registry: Borrow typed client with Controller.WithClient
    Handler-->>SDK: Result or error
    SDK->>SDK: Check cancellation and retirement
    SDK-->>Host: Accept result only while current
```

The host supplies `CallInput.Route`. Plugin authors do not reconstruct that route from user arguments or serialize their own protobuf request.

Inside the handler:

1. Use the admitted `PluginContext` and its cancellation context.
2. Borrow the client through the resource provider's existing typed `Controller.WithClient`.
3. Finish native work before returning. Do not retain the borrowed client or callback context for background work.
4. Return the native error or an object-valued result. The SDK owns final cancellation and retirement checks.

The configured registry replaces caller-supplied connection profile data. A missing, stopped, or retired connection fails before native handler work. A custom tool does not need a fabricated resource reference or catalog pin.

An accepted asynchronous utility session has a different lifetime. Use the existing session capability rather than starting an unowned goroutine in a tool callback.

## Define input and output honestly

`InputSchema` and any `OutputSchema` must describe JSON objects. `StructuredContent`, when present, must also be an object. Use text content for results that are genuinely text rather than wrapping a scalar as invalid structured content.

Schemas use the SDK's bounded JSON Schema 2020-12 profile:

| Concern | Contract |
| --- | --- |
| References | Static local pointers and anchors only; no network loading or recursive/dynamic references |
| Dialect and formats | Other dialects and `format` keywords are rejected |
| Defaults and coercion | Never applied |
| JSON | Duplicate members are rejected at every depth; numbers stay exact |
| Validation | Input before native admission; declared structured output before success |
| Resource bounds | 1 MiB per schema/value, 10,000 nodes, depth 128, plus schema-expansion and validation-work budgets |

See [the complete bounded profile](capabilities.md#choose-capabilities) for numeric and work limits. Unsupported declarations fail registration; unsupported work fails validation rather than silently dropping assertions.

Consumers inspecting tool metadata can call `tools.ValidateDescriptor(descriptor)`
before publication or schema transformation. It uses registration's same bounded
profile for both input and any declared output, requires no handler, and does not
rewrite the descriptor or execute native work. Its diagnostics are local errors,
not safe model-facing messages.

## Treat annotations as hints

Annotation booleans are optional pointers. Omission and explicit false remain distinct across the wire.

| Hint | Omitted value |
| --- | --- |
| `ReadOnly` | `false` |
| `Destructive` | `true` |
| `Idempotent` | `false` |
| `OpenWorld` | `true` |

Use `Annotations.Resolve()` when consuming hints. A read-only annotation does not authorize execution or waive host consent. MCP custom tools require explicit user policy enablement; resource-read presets rely on host-known resource operations instead.

## Return safe failures

Return cancellation and deadline errors normally. They remain transport failures, not successful tool calls containing an error message.

For other failures, the model receives a reviewed code and fixed safe presentation: `EXECUTION_FAILED`, `UNAVAILABLE`, or `PERMISSION_DENIED`. Raw handler diagnostics, failure text, and failure structured content do not cross this boundary. Keep useful diagnostic details in local plugin logs.

Retirement or timeout can reject a result after a native side effect has started. That rejection does not prove rollback. Do not automatically retry writes.

## Migrate callers together

The tools service uses only `omniview.sdk.tools.v2` and contract version 2. Existing Go utility package paths remain unchanged; the service namespace is the wire contract.

Older metadata-only peers do not receive a fallback call. Upgrade the SDK and host consumer together through the reviewed candidate cohort. Do not assume an older published SDK tag contains these APIs.
