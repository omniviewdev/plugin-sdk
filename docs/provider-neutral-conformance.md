# P44 provider-native conformance corpus

Status: implemented and locally verified after the coordinator's settled-slice barrier; independent acceptance review remains required. Scope is [plugin-sdk#11](https://github.com/omniviewdev/plugin-sdk/issues/11), selected SDK source `1e92fb518b117814acb504aaae1772ff39abcdef` (v0.6.0). This is a verification deliverable, not a released provider or approval of any replacement API.

## Acceptance shape and implementation

The accepted plan is deliberately confined to one internal package:

1. Define deterministic native records, finite page membership, independent identity cases and native host/VM/disk links.
2. Supply transport-independent snapshot, page, update, link, coverage and freshness oracles; include negative controls for payload corruption, scope escape, duplicate/missing membership and guarantee upgrades.
3. Exercise the current SDK runtime in a disposable go-plugin process through its real resource protobuf client/server and `BuildResourceController`, without `TestProvider` forwarding/default successes.
4. Record supported behavior separately from current representability limitations and hand the transport port to P45.

`internal/resourceconformance/{corpus,native,oracles}.go` imports only the Go standard library. Its exported names are fixture entrypoints inside the repository's `internal` boundary, not new public SDK contracts. `NativeCorpus()` returns fresh data. `SameAddress`, `CheckSnapshot`, `CheckPages`, `CheckChanges`, `CheckLinks` and `Validate` are finite test oracles, not a second query/identity/graph engine. Test-only `current_*_test.go` owns every legacy transport dependency and serving entrypoint. There is no `main` executable, production registration, cloud SDK dependency, frontend fixture row or product fallback.

| Native profile | Cases |
| --- | --- |
| Cloud | Flat ARN/OCID-like opaque IDs, delimiters/slashes/percent/Unicode, account/region tuples and collisions, tag arrays with same-element-correlation counterexample, null versus missing, quoted map keys/injection-shaped strings, exact large integers and nanosecond timestamp text; two native cursor pages; bundled/absent schema and explicit partial/eventual listing |
| Container | Engine-local colliding IDs, label maps, optional incarnation and opaque revision, bundled event profile plus absent-schema snapshot profile; one native `running` to `paused` update with revision `event:z9` to `event:a1` (never lexical recency) |
| Hypervisor | Native numeric VM/disk IDs (`101` and `7`) collide across nodes; host/VM/disk body links resolve within exact node; bundled/absent VM schema, paged and preselected single-node reads, snapshot-only without fake watch/poller |

Every scenario explicitly records operation support, effective finite native selection, coverage and freshness. All mutation/server-filter modes are unsupported. Exhausted means only the selected accessible listing was exhausted, not unrestricted inventory or point-in-time consistency. Partial last-page completion remains partial. Snapshot-only remains snapshot-only. Incarnation/revision absence stays absent; neither is filled with a UID.

The server-filter/create/update/delete boundary checks consume each scenario's declared support expectation, rather than pinning all declarations to a hard-coded default. A disposable negative control changed the parent oracle to claim supported server filtering while the real child still rejected it; all affected scenarios failed at the operation-result assertion. The mutation was removed before verification. Unsupported machine-code loss remains separately logged, not treated as correct semantics.

## Actual current boundary and its limits

The subprocess runs `sdk.NewPlugin(...).Serve()`, dispensing `resourceplugin.GRPCPlugin` backed by the real resource controller, connection manager and watch manager. The finite fixture source implements mandatory current `Resourcer` methods, returning explicit errors for unsupported operations; it is not a released product resourcer. Exact native type keys are registered as current pattern keys (`compute/instance`, `container`, `vm`, `host`, `disk`). No invented group/version/kind tuple is introduced. Consequently metadata is empty and no catalog types are advertised: that is a current SDK limitation, not successful neutral catalog support.

Selected source routes choose finite scenarios. Their connection IDs are access provenance, not added to identity. Native account/region/engine/node scope is never encoded as `Namespace`. Unambiguous current Get works; colliding local IDs without an exact scope slot are explicitly rejected rather than merged, rewritten into compound IDs or namespace-encoded. Single-node source routes permit each VM `101` to be read honestly under their preselected native scope. They do not prove the current Get request can encode a general scope tuple.

Boundary probes cover native JSON byte preservation (including large numeric lexemes), cursor traversal, bundled/absent schema bytes, native IDs, source scope collisions, invalid cursor/namespace/unknown type and not-found errors, no-server-filter and read-only errors, optional container streaming, cancellation observed inside the plugin, connection destruction, stopped-source rejection and child process exit. Hypervisor link checking consumes bodies actually transported by these reads. Schema transport is not schema admission, normalization or CEL validation.

Current-wire limitation observations are logged with `LIMIT` and are **not** asserted as correct target semantics:

- `ListResult.TotalCount = -1` is clamped to zero by the current server. Zero must not be treated as inventory size or an exact known count.
- Current List has no coverage/freshness/consistency envelope. The source-side finite oracles remain independently available, not falsely described as transported guarantees.
- Current mandatory Find/CRUD interface produces true-by-default capability claims despite read-only/no-filter operation errors. Context-qualified absent schema/update capability discovery is also not established by structural type assertions.
- `UNSUPPORTED` has no current error-code mapping; its message survives but its machine code is lost. This is neither a uniform-error acceptance receipt nor an approved future error spelling.
- Exact native pattern keys have no neutral catalog/metadata representation. No synthetic GVK is used to conceal that gap.
- Current Get and event envelopes lack a native exact identity-scope tuple. Event body engine/incarnation/revision data survives; event metadata UID/namespace stays empty. A native body is not an admitted canonical resource reference.
- The optional event test uses a test-side file barrier, released only after receiving the real listener's buffered ready state. It proves actual event delivery, not an atomic snapshot/event cutover, replay, continuity, source sequence, gap-free ordering or reliable recreation removal.

No host resource API, query evaluator, UI, permission admission, production infrastructure or replacement protocol is certified by this packet. The clean environment is source/profile isolation, not network or hostile-plugin sandboxing.

## Reproduction and disposable state

After the coordinator's settled-slice barrier, from the SDK worktree:

```sh
GOWORK=off go test ./internal/resourceconformance -count=1 -v
GOWORK=off go test -race ./internal/resourceconformance -count=1
```

For the isolated frontier run, launch each command through the coordinator-provided `baseline-env` and set `P44_CLEAN_ENV_LAUNCHER` to that launcher using `env`. The subprocess also re-enters that launcher, then supplies an explicit minimal environment, magic cookie and negotiated current protocol version. `SkipHostEnv: true` prevents go-plugin from appending the parent's environment in both launcher and ordinary reproduction paths. The child receives temporary HOME, USERPROFILE, XDG and `OMNIVIEW_STATE_DIR` roots, not cloud credentials, kubeconfig, ambient workspace selection or telemetry settings.

Each test run creates unique `t.TempDir` roots for plugin metadata/state, home and lifecycle receipts. Plugin registration YAML, event-release barrier and waiting/cancelled/closed receipts are verification artifacts inside those roots. `StopConnection`, event cancellation and `client.Kill()` retire runtime resources; testing cleanup removes the directories. No checked-in binary, generated binding, `.devinfo`, standalone fixture provider, persistent receipt output, global link or profile seed is needed. Preserve the command output externally when attaching acceptance evidence.

Independent review found that an explicit `cmd.Env` alone did not prevent the pinned go-plugin from appending `os.Environ()`. A real-subprocess regression disables the optional clean-child launcher, supplies harmless ambient credential/kubeconfig/workspace sentinels, rejects those variable names in the child before SDK initialization, and performs actual connection discovery. It failed before `SkipHostEnv` was enabled and passed afterward; the full conformance race run also passed. No credential values are asserted or printed. Earlier clean-launcher receipts alone did not establish the ordinary path's isolation.

## Explicit P45 port ownership

P44 can be accepted after P00 without waiting for P01/P45. P45 owns replacing/deleting `current_*_test.go` with probes for its **actually reviewed new-major boundary**, and the real host adapter smoke. Do not guess a replacement RPC, source-open signature, DTO package, capability flag or canonical identity encoding here.

Retain the native records and finite semantic expectations independently. P45 must carry all three profiles through real new-wire/host reads and demonstrate exact scope/local-ID behavior, optional incarnation/revision, honest unavailable/absent schemas, unknown totals, explicit partial/eventual/snapshot freshness, independently unsupported mutation/filter/update modes and cancellation/cleanup. It must replace limitation observations with real supported behavior or explicit unsupported results, not assert today's lossy encodings as compatibility requirements. P05 then owns the separately reviewed snapshot-plus-update acquisition/cutover contract; P07 schema admission/catalog; P03 canonical identity. No legacy adapter in this verification package is a production compatibility shim or an approval to retain one at release.

## Narrow progress receipt

Coordinator authorized scoped formatting and sequential verification. All 13 real-process scenarios passed, including actual container update delivery, cancellation receipts, source destruction, stopped-source rejection, transported hypervisor link checks and plugin exit. The full conformance suite passed with `-race`; existing resource/controller/transport/plugintest suites and the complete SDK `go test ./... -count=1` passed under the isolated launcher. No race detector report occurred for this workload; no claim covers every schedule.

Two initial harness failures were diagnosed and corrected before those passing receipts: nested testing TMPDIR exceeded Darwin's Unix-socket path limit (now a short unique cleanup-owned socket directory); remote `StartConnectionWatch` has no current RPC (the real `StartConnection` already initializes the manager, so the probe uses supported `EnsureResourceWatch`). Neither correction changed SDK/product code, masked a native semantic failure or invented a replacement API.

The real run observed unknown-total transport `-1` to `0`, unsupported code `UNKNOWN_0`, and true legacy Find/CRUD flags; these remain documented losses, not accepted semantics. Cancellation/shutdown can log current SDK gRPC `Canceled` diagnostics even though the explicit lifecycle checks pass. Packaging/reference inspection found the fixture package/plugin ID only in this internal test package and this document. No issue closure, public publication, commit, push, PR or merge is claimed here. The integration coordinator owns independent review, retained command receipts, issue acceptance/Project readiness and central host ledger updates.
