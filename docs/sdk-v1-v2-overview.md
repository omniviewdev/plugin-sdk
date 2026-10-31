# Migrate a resource plugin from v1

[Docs home](README.md) · [Quickstart](quickstart.md) · [API reference](resource-v2-api.md)

> [!IMPORTANT]
> Resource v2 is the SDK v0.9 resource API, not a compatibility layer. Rebuild and migrate host/plugin consumers together using the coordinated release versions.

This guide covers the two removed resource stacks: `pkg/resource` and `pkg/v1/resource`. Utility packages such as `pkg/v1/exec` and `pkg/v1/metric` remain current.

## What changes?

| Before | Now | Author action |
| --- | --- | --- |
| Six-method Resourcer: Get/List/Find/Create/Update/Delete | Required catalog; independent optional operations | Remove fake implementations for unsupported operations. |
| Core operations advertised for every registered v1 resourcer | Explicit type/scope-qualified catalog support | Declare only operations your callback implements. |
| GVK-shaped type metadata and namespace-shaped inputs | Opaque type IDs and provider-defined dimensions | Map native identities without inventing group/version/kind or namespace. |
| Connection appears in older reference/session models | Identity and explicit access route are separate | Preserve host authority; do not make connection ID part of canonical identity. |
| Generic maps or raw JSON, depending on the old stack | Tagged `Value` domain | Preserve native numeric and timestamp intent before transport. |
| List/Find and filtering extensions | Finite `Enumerate` and optional provider-language `NativeQuery` | Return honest source pages, not a replacement host query engine. |
| SDK-managed watches/informers | Explicit best-effort advisory updates | Do not claim replay or snapshot/live continuity. |

Old extensions such as watches, actions, schemas, and relationships were already optional. The old framework was Kubernetes-shaped, not Kubernetes-exclusive. See [historical v1 source](https://github.com/omniviewdev/plugin-sdk/blob/32a3358/pkg/v1/resource/resourcer.go) for the actual six-method core and extensions.

## 1. Replace resource registration

Build a controller with only the callbacks you need, or implement `Provider` directly. Register the resulting provider once:

```go
plugin := sdk.NewPlugin(sdk.PluginOpts{ID: pluginID})
sdk.RegisterResourcePlugin(plugin, provider)
plugin.Serve()
```

This contextual excerpt is from the complete [example entrypoint](../examples/resource-plugin/main.go). `pluginID` is `"notes"` and `provider` comes from its `newProvider` constructor. Do not register a separate plugin capability for each resource type.

**Try first:** [run the read-only example](quickstart.md) before connecting your existing native client.

## 2. Write an honest catalog

A get-only type advertises Get, not the former six-method contract. Inside its `ResourceType` declaration:

```go
Capabilities: []resourcev2.Capability{{
    Operation: resourcev2.OperationGet,
    Scope:     resourcev2.QueryScope{},
}},
```

This excerpt matches [the example catalog](../examples/resource-plugin/provider.go). Add scopes and representations for your real backend, then register the matching callback.

Create, mutation-update, and delete need separate declarations. `OperationUpdate` is advisory watch delivery; it is not the capability for a write.

## 3. Map identity separately from access

| Question | Put it in |
| --- | --- |
| Which provider/type/native object is this? | `ResourceAddress.Provider`, `Type`, `ID` |
| Which native partition identifies it? | Address `Scope`, using declared identity dimensions |
| Which authority does the host assign? | Address `Authority`; do not synthesize it in the native adapter |
| Which connection reaches it? | `AccessRoute.Connection` |
| Which recreation was observed? | Optional `ResourceReference.Incarnation` |
| Which content revision was observed? | Optional record `SourceRevision` |

No first-active-client fallback exists. If you use SDK lifecycle management, start the selected configured connection before native work. See [manage connections](connections.md).

## 4. Convert values before precision is lost

For native JSON object bytes, replace a default `map[string]any` JSON decode with `JSONToValue`:

```go
body, err := resourcev2.JSONToValue(data)
if err != nil {
    return resourcev2.Record{}, &resourcev2.ResourceOperationError{
        Code: "INTERNAL", Message: "The source document could not be decoded.", Err: err,
    }
}
```

This is a contextual Get-callback excerpt: `data` is the native document just acquired. A conversion cannot recover an integer already rounded through `float64`. Native JSON timestamp-looking strings remain strings; use explicit native timestamp conversion when appropriate.

## 5. Reconnect utilities, then verify

- Keep the existing exec/log/metric/forwarding engine that owns each utility's sessions.
- Carry canonical values and optional reference/route through resource RPC lanes.
- Reference-bearing work needs an accepted catalog observation; body-only work must not fabricate one.
- Bind compatible utilities to the resource provider's sole connection registry.
- Preserve each utility's cancellation and accepted-session lifetime. Asynchronous logs do not currently share forwarding's retained-generation lease contract.

Use the [quickstart checkpoints](quickstart.md), existing native-boundary conformance, and your backend's real workflows. Resource-v1 RPC fallback is not a migration path.

### Version names are different things

| Name | Current meaning |
| --- | --- |
| Resource wire version `2` | Canonical resource contract negotiated by resource RPCs |
| Shared SDK/go-plugin protocol `1` | Process/lifecycle transport version; not bumped merely by resource v2 |
| `pkg/v1/exec`, `logs`, `metric`, … | Retained utility package paths |
| Go module release tag | Coordinated publication decision; not inferred from the numbers above |

## Known limitations

> [!WARNING]
> Relationship matching currently rejects query-only dimensions absent from returned identity, including an `any` constraint. Do not invent identity coordinates to bypass it. See the [recorded limitation](resource-v2-contracts.md#relationship-helper-limitation).

Resource bodies still require a map root. Older metric `ResourceNamespace` fields and common GVK/namespaced protobuf structures remain outside the new resource core. Their existence is not a requirement to use Kubernetes identities, but SDK-wide removal is not claimed.

Admission/lifecycle helper breadth, create request/result semantics, candidate wire ceilings, and asynchronous log-generation ownership remain review decisions. Detailed findings and proof limits belong in the [specification review](resource-v2-spec-review.md), not in the authoring path.

**Next:** [Resource tasks](resources.md) · [Connection guide](connections.md) · [API lookup](resource-v2-api.md)
