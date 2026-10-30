# plugin-sdk

The SDK for creating plugins in Omniview, the Integrated DevOps Environment

## Plugin author documentation

Start with the [plugin author guides](docs/README.md), or
[run the read-only plugin quickstart](docs/quickstart.md).
Use the [resource API reference](docs/resource-v2-api.md) for signatures and contracts,
and the [migration guide](docs/sdk-v1-v2-overview.md) when moving from resource v1.

## Internal Logging

Use `log` for plugin runtime/internal logging. This is separate from the log capability API in `pkg/v1/logs`.

See [docs/internal-logging.md](docs/internal-logging.md).

## Settings validation

Register `settings.Setting.Validator` with a
`func(interface{}) settings.ValidationResult` to return contextual validation
feedback. Set `Valid` for accepted values and provide a stable `Code` plus a
user-safe `Message`. Validators must not mutate settings and should bound external
work.

This is a breaking source migration: replace error-returning `Validator` callbacks
with callbacks returning `settings.ValidationResult`, then rebuild plugins.
There is only one callback; no legacy callback adapter is provided.

`Setting.Validation` and `Provider.ValidateSetting` check drafts without saving.
Writes validate again and reject invalid values; bulk writes validate all values
before changing any. Sensitive settings always return generic feedback.
Invalid structured results with an empty message receive generic user-safe feedback;
nonempty contextual messages remain unchanged. `Setting.Validate` enforces writes
by calling the same `Setting.Validation` evaluation and returning rejection messages
as errors.

The settings capability exposes draft validation through gRPC using a dedicated
`ValidateSettingRequest`, executing callbacks inside the plugin rather than on
serialized host copies. Rebuild plugins and the
host against the updated SDK to use this RPC. Older plugin processes report an
unsupported RPC instead of approving unvalidated drafts.

## Application state and workspace isolation

The SDK resolves all owned plugin configuration and capability stores through
`pkg/state.ResolveRoot`. The host must pass its
resolved absolute root as `OMNIVIEW_STATE_DIR`, including when the host selected
that root programmatically. A non-workspace process without this variable retains
the production default `~/.omniview`; nightly hosts must explicitly pass their
nightly root. The SDK never changes `HOME`.

When `OMNIVIEW_WORKSPACE_FILE` is nonempty, it must be an absolute readable v1
workspace manifest with a canonical nonzero `workspaceID` UUID and
`profile.kind: development`. The SDK reads identity/path-boundary metadata,
leaving full manifest validation and mutation to the host.
`OMNIVIEW_STATE_DIR` is mandatory and must agree with the selected profile.
Without a machine-local binding, the default remains the exact literal
`<canonical-shared-profiles>/<workspaceID>/state`; UUID-specific profile/state
symlink remaps are rejected, while HOME/shared profiles-container aliases are
canonicalized.

A host-bound custom root is accepted only when all three v1 machine-local
records match: `<workspace>/.local/profile.json`,
`<custom-parent>/.omniview-profile.json`, and a claim in
`<HOME>/.omniview-dev/custom-profiles.json`. Identity records contain
`schemaVersion`, `workspaceID`, and canonical absolute `stateRoot`; the claims
registry contains `schemaVersion` and `claims`. State must be the literal `state`
child of its dedicated canonical parent. Missing/foreign/malformed or redirected
records, duplicate/nested claims, environment disagreement, and overlap with
production/nightly/default profiles or workspace/registered source storage fail
closed. Absent binding permits only the default, never an unclaimed custom
override. The SDK reads these records but never creates claims, binds/rebinds a
profile, or initializes state during resolution. The host keeps runtime storage
in the default private UUID namespace independently of custom application state.

A moved workspace may retain a durable interrupted sibling-rename journal before
the host rewrites its root. The SDK accepts only the exact recorded rename phase
and current target, clean absolute sibling old root, matching v1 UUID/development
identity, and valid target slug. The old root remains a protected source boundary.
Unrelated root mismatches remain errors; the host still performs full manifest
validation and owns rename recovery.
The native-independent `pkg/profilecontract` package owns these v1 binding,
claim, and ownership-journal schemas and shared read-only identity validation.
Both host and SDK consume that contract directly; only the host validates the
complete portable manifest and publishes or repairs ownership metadata. Metadata
decode failures use fixed diagnostics without including private JSON values.

Plugin IDs and capability filenames must be single nonempty ASCII
letter/digit/dot/underscore/hyphen components, not `.`/`..`, trailing-dot names,
or Windows reserved device names. SDK file operations use `os.Root` handles and
reject symlink aliases in owned plugin/store paths. Returned path
strings such as `GetPluginStorePath` are advisory: external consumers must not
replace handle-based SDK operations with unchecked path-based access.

Development plugins use the same go-plugin handshake/lifecycle RPC transport as
other plugins. The host owns process supervision, readiness, rebuilds, and
shutdown through its managed lifecycle/control client. The SDK does not inspect
`OMNIVIEW_DEV`, intercept stdout, write `.devinfo`, or clean up old discovery
records. The removed standalone runner/discovery APIs have no compatibility shim.
Legacy `.devinfo*` files remain excluded from baseline exports and are not proof
of attachment/readiness or permission to start an imported executable.

### Pre-launch artifact compatibility

`config.PluginMeta.StateIsolation` is optional, additive YAML/JSON metadata under
`state_isolation`. Missing metadata means isolation support is **unverified**.
Its v1 fields are `schema_version: 1`, `capability: state-root-v1`,
`sdk_module: github.com/omniviewdev/plugin-sdk`, a fully resolved lowercase
`sdk_revision` known-fixed capability-base commit hash, `binary_sha256`, and
optional `sdk_source_sha256` identifying the exact selected SDK source snapshot.

A trusted host-owned builder calls
`config.BuildStateIsolationProvenance(binaryPath, sdkRevision, sdkSourceSHA256)`
**after** validating the selected SDK module/path, known-fixed reviewed baseline,
and actual build inputs. For live editable SDK worktrees, it computes a stable
lowercase SHA-256 source snapshot before/after the build and rejects a changing
snapshot. Dirty feature edits are supported without requiring a commit per edit;
the reviewed commit establishes the capability base and the source digest
identifies the exact editable build. The SDK does not define a second source-tree
hashing algorithm: the trusted host builder owns that snapshot contract.

The host stores the attestation and independent expected source digest in its
owned artifact/install record, or accepts independently authenticated registry
provenance. Before launch, it calls
`provenance.VerifyArtifact(binaryPath, expectedSDKRevision, expectedSDKSourceSHA256)`
to check capability, schema, module, trusted capability base, source snapshot,
and exact artifact bytes without executing the plugin. A supplied snapshot must
be lowercase 64-hex and match the trusted expectation exactly. Empty source
digests are permitted only when both expectation and attestation are empty for a
trusted immutable clean-commit build; a source-bound record cannot downgrade to
commit-only verification. Trusted expectations never come from arbitrary artifact
self-assertions. Protect verified artifact bytes from replacement before execution.

These helpers hash and compare attestations; they do not prove source inclusion
or authenticate metadata. Arbitrary `plugin.yaml` claims are not sufficient
authorization. No lifecycle RPC, protobuf, or protocol-version change is needed:
starting an unknown binary to query isolation support is already unsafe.
Existing marketplace executables are **not** retroactively fixed. Baseline-imported
binaries remain inactive until rebuilt with the fixed SDK or verified through an
appropriate trusted artifact pipeline.

Application-state isolation is not a process, credential, or remote-resource
sandbox. Plugin-authored filesystem operations, user-selected exports, external
tool caches, and cluster/account mutations require their own policies.

## Plugin settings sensitivity transport

The v1 settings protobuf carries `optional bool sensitive` with field presence.
Current SDK schema conversion explicitly transmits either public (`false`) or
sensitive (`true`) classification; Password settings are always classified
sensitive, even if a sender supplies `false`. Settings received without this
field are legacy/unclassified and decode conservatively as `Sensitive: true`.
Re-encoding such a setting cannot upgrade it to public. Rebuild legacy plugins
with the corrected SDK before expecting nonsecret settings to remain visible in
secret-safe inspection; absent metadata is not evidence that a value is public.

Conversion preserves setting values/defaults, options, and file-selection
metadata for trusted runtime consumers. The inspecting host must use sensitivity
classification to redact every value-bearing field, including defaults, options,
and file-selection defaults, rather than exposing the raw RPC schema. Malformed
value/default/option payloads fail closed as sensitive metadata. This transport
fix does not change the provider's runtime accessors or itself authorize access
to secret settings.

## Resource v2 boundary

Resource v2 is an **implementation candidate awaiting public API review**, not
an approved contract. Providers use handwritten `pkg/resource/v2` values and
`sdk.RegisterResourcePlugin(plugin, provider)`. Only catalog discovery is required;
finite operations, lifecycle, ancillary traits and advisory updates are optional.
Catalog declarations qualify each operation independently, including create,
mutation update and delete. Advisory `OperationUpdate` never authorizes writes.

Start with the [plugin author guides](docs/README.md) for a runnable quickstart,
resource recipes, connection management, and capability registration.
Use the [API reference](docs/resource-v2-api.md) for lookup and the
[advanced contracts](docs/resource-v2-contracts.md) for host integration details.
The [Go package overview](pkg/resource/v2/doc.go) links those concepts to the
[compiling public examples](pkg/resource/v2/example_test.go): catalog-only,
get-only, controller lifecycle and custom-provider generation issuance.

Read the [code review](docs/resource-v2-code-review.md) and
[specification comparison](docs/resource-v2-spec-review.md) before freezing or
claiming approval. Connection routes and catalog pins are not permission grants;
the host owns authority and public error policy. Generated messages and binary
framing are transport details, not provider construction APIs.

Providers must rebuild for the resource v2 contract. Existing nonresource utility
engines remain; resource-bearing operations use canonical values and strict
versioned admission without legacy-method fallback. Resource wire version is
`2`; the shared go-plugin transport remains `1`. Local replacements alone are
not a consumable release receipt.

`buf.gen.yaml` pins protoc-gen-go `v1.36.11` and protoc-gen-go-grpc `v1.3.0`.
Use `make proto` for regeneration; remote Buf generators require no ambient
native `protoc` binary.

## Settings value reads

The v1 RPC client's `GetSettingValue` returns the decoded setting value, not a
protobuf `Any` envelope. It uses the existing JSON-backed settings codec:
numbers decode as `float64`, objects as `map[string]any`, arrays as `[]any`, and
JSON null as `nil`. Malformed or unsupported wire payloads return decoding errors;
RPC status errors propagate unchanged.


## Releasing the Go module

The module is `github.com/omniviewdev/plugin-sdk` at the repository root.
Publish a new, previously unused root tag of the form `v0.MINOR.PATCH` on the
reviewed commit after it has merged into `main`. The module has no source-code
release-version constant, nested module, or publishing workflow; a pushed Git tag
is the Go module release. A GitHub release can accompany that tag but is not
required for Go module resolution. Never move or reuse an existing release tag:
Go proxies and the checksum database treat published versions as immutable.

Before tagging, run the SDK tests with `GOWORK=off GOFLAGS= go test ./... -count=1`
and finish the coordinated host/plugin compatibility checks. After pushing the
tag, confirm the version's `.info` and `.mod` endpoints at
`https://proxy.golang.org/github.com/omniviewdev/plugin-sdk/@v/`, then resolve it
with `GOWORK=off GOFLAGS= GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org go mod download -json github.com/omniviewdev/plugin-sdk@<release-tag>`.
Consumers must pin the new version, update `go.sum`, and pass their own checks
without a local SDK replacement before their releases are published.

The workspace-isolation release is `v0.6.0`: it removes the standalone `.devinfo`
APIs and requires rebuilding plugins against this SDK. It must not reuse the
already published `v0.5.0`, `v0.5.1`, or `v0.5.2` tags. The added exec sequence and optional
settings-sensitivity fields preserve their protobuf field numbers and wire
compatibility. Their generated Go files are committed alongside the schemas;
regenerate them with `make proto` only when changing those schemas. The module's
release tag is independent of `sdk.CurrentProtocolVersion`, which remains `1`;
these additive fields and the host-owned isolation metadata do not require a
handshake/lifecycle protocol bump.
