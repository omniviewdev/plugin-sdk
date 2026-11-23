# Exec: run commands in a target

[Docs home](README.md) · [Plugin anatomy and capabilities](capabilities.md) · [Manage connections](connections.md)

The exec lane runs a command in a **target** (a Pod, a container, a host, a VM) for the host: interactively (a terminal) or as one bounded run (an agent tool). You write **one function**, `ExecFunc`; once the engine steps land, the SDK owns sessions, ordering, backpressure, resize, replay, bounds, typed errors and the single end of every command.

This guide covers `omniview.sdk.exec.v2` (`proto/exec/v2/exec.proto`, `pkg/v1/exec`). The legacy lane (`exec.Handler`, `TTYHandlerFunc`) is unchanged and keeps working; a plugin may serve either or both.

> **What this SDK build serves.** `Describe`, `Options`, `Open`, `Run` and `RunStream` are live: a host can list your targets, ask for the choices of one resource (when your plugin has the resource provider described [below](#options-needs-a-resource-provider)), open an interactive session in a target, and run one bounded command in it, as one result or as events. For an `Open` the SDK runs your `ExecFunc` with a `Proc` and sends the host the frames of [What the host receives of a session](#what-the-host-receives-of-a-session): `Opened`, `Started`, the output with its offsets, and one typed `Ended`. A session lives exactly as long as its stream: this build has no lease, no replay ring and no tombstone, so a host whose stream ends ends the session. For a `Run` the SDK runs the same function for a command without a terminal, keeps a bounded part of its output with an exact account, and answers with one `RunResult` (see [Bounded runs](#bounded-runs-run-and-runstream)). `Describe` lists `FEATURE_SESSION`, `FEATURE_RUN`, `FEATURE_RUN_STREAM`, `FEATURE_ENV` and `FEATURE_CWD`. **Every other RPC** (`ListSessions`, `ReadOutput`, `EndSession`) is answered with a typed `UNSUPPORTED` refusal: gRPC code `Unimplemented`, a `google.rpc.ErrorInfo` of domain `omniview.sdk.exec.v2` with reason `UNSUPPORTED`, which the Go client reports as an `*exec.Error` with `ErrorClassUnsupported`, and `Describe` lists no feature for them, so a host that asks first never calls them. `exectest.Conformance` checks registration, `Describe` and `Options`, and runs the engine cases that this build serves, in process and over the wire; it skips each of the others with the step that fills it.

**The rules, in plain words.** (1) A failure after the command was handed to your function is data (`Ended{failed}`, `RunResult` with `OUTCOME_FAILED`); only a refusal, when nothing ran, is a status. (2) A session has exactly one end and **at most one `Ended` frame**: `Ended` goes only to the stream that is attached when the session ends, so a session that ends while no stream is attached (a lease that expires, a retirement, a shutdown) has none, and its end is the tombstone's (`SessionInfo.ended_with`). A stream that can still receive frames ends with exactly one of `Ended` (the session is over) or `Detached` (this stream left, the session lives). (3) A session may only narrow the capabilities its kind declares, never widen them. Its mode (a Kubernetes exec or attach) is declared on the `Choice` of your `Options` (`Choice.Capabilities`) and is in `Opened`; `Started` carries only what you learn at run time, is sent at most once and always before the first output, and the SDK sends one made from the facts of `Opened` when your function writes before it reports. `exectest.ProcRig` (given `exectest.RigCapabilities` and `exectest.RigOptions`) tells your test when your function widens, reports late or reports twice, as the SDK will log it. (4) An exit code is carried exactly, in any integer type up to 32 bits unsigned, and a stream that ended with no status is `Lost`, never exit 0. (5) Requests are strict (an unknown field is refused) and carry no maps: environment, labels and selection are lists of `NameValue`.

## A complete plugin

```go
// Command exec-plugin registers one exec target, an echo host. It has no resource provider, so a host can Describe
// it but cannot reach it: docs/exec.md says what a reachable plugin adds.
package main

import (
	"context"
	"io"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

func main() {
	// NewPlugin reads the installed plugin.yaml of this ID and panics without it.
	plugin := sdk.NewPlugin(sdk.PluginOpts{ID: "echo-exec"})
	if err := exec.RegisterPlugin(plugin, exec.PluginOpts{Targets: []exec.Target{echoHost()}}); err != nil {
		panic(err)
	}
	plugin.Serve()
}

// echoHost is the one kind of thing this plugin runs commands in. Its "command" copies its input back.
func echoHost() exec.Target {
	return exec.Target{
		Info: exec.TargetInfo{
			Provider: "echo", Type: "host", // the routing key: the resource provider id and type of a request's reference
			DisplayName: "Echo host",
			DefaultArgv: []string{"cat"},
			Capabilities: exec.Capabilities{ // declared, never guessed: what closing does, how it ends and stalls
				Kill: exec.KillTerminates, ExitReport: exec.ExitReportCode, Stall: exec.StallPropagates,
			},
			Session: exec.DefaultSessionLimits(), // a target with neither Session nor Run serves only Options
		},
		Exec: echo,
	}
}

// echo is the whole command: copy the input back until it ends.
func echo(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	p.Started(exec.SessionFacts{Program: exec.Program{Argv: p.Request.Argv, Name: "cat"}})
	if _, err := io.Copy(p.Stdout, p.Stdin); err != nil && ctx.Err() == nil {
		return exec.Exit{}, err // the target failed: the SDK classifies the error
	}
	return exec.ExitCode(0), nil // the command ended with its own status
}
```

The file is [`examples/exec-plugin/main.go`](../examples/exec-plugin/main.go); its tests run the conformance suite against it and keep this block identical to the file.

**This plugin needs an installed manifest.** `sdk.NewPlugin` reads `plugins/<id>/plugin.yaml` from the host's state root (`OMNIVIEW_STATE_DIR`, default `~/.omniview`) and **panics** when the file is missing or its `id` differs from the one you pass; the host installs it before it launches the plugin. The example ships this one, [`plugin.yaml`](../examples/exec-plugin/plugin.yaml), and a test installs it in a temporary state root and registers the example on it:

```yaml
id: echo-exec
name: Echo exec
version: 0.1.0
description: Run commands in a host that echoes its input back.
repository: https://github.com/omniviewdev/plugin-sdk
capabilities:
  - exec
sdk_protocol_version: 1
```

**What it is and is not.** The example is a complete provider of the exec side: it registers, `Describe` lists its target, and `exectest.Conformance` passes on it. It has **no resource provider**, so it has no connection a host could name, and it declares no `Options` callback: an example must not advertise a callback no host can reach. [Options needs a resource provider](#options-needs-a-resource-provider) says what a reachable plugin adds.

## What you write and what the SDK owns

| | You write | The SDK owns |
|---|---|---|
| `exec.Target` | `Info`, `Exec`, and optionally `Options` and `Classify` | routing, admission of the canonical reference and route against the registered type and the caller's connection, the connection lease |
| `Target.Info` (`TargetInfo`) | `Provider` and `Type`: **the routing key**, the resource provider id and resource type that a request's reference must carry to reach this target (`"k8s"`, `"pods"`). Required. Then `DisplayName`, `DefaultArgv` | the check that no two targets share a key; `Describe` |
| `Info.Capabilities` | what the target can do **at most**: the kind's upper bound, [two levels](#capabilities-two-levels) | restating it in `Describe` and in every session's facts |
| `Info.Session`, `Info.Run` | the limits you serve under: `exec.DefaultSessionLimits()` is the usual choice for a target with sessions, `exec.DefaultRunLimits()` for one with runs. **A section you leave zero is a service you do not offer: a target with neither `Session` nor `Run` serves only `Options`** (and registers without an error: `Exec` is still required and never called). A section with any field set takes the SDK's default for each field you leave at zero | default-filling, validating against the contract's ceilings, and reporting the effective, never-zero limits |
| `Exec` | `ExecFunc(ctx, *Proc) (Exit, error)`: copy between `p.Stdin`/`p.Stdout`/`p.Stderr` and the target, return how the command ended | stdio queues, frames, offsets, ordering, resize (latest wins), flow control, replay, the typed end |
| `Options` | the choices of one resource with a state (a Pod's containers) | the RPC, the lease, the bounds (at most 8 options, 256 choices each, closed codes) |
| `Classify` | a map from your API's errors to `*exec.Error{Class, Reason, Facts}` | the closed vocabulary, the bounds, never sending free text or your local cause (`Error.Err` is logged, never sent) |

`Provider` and `Type` are the only routing key (there is no second pair on `Target`). A target registered under a `Provider` that no resource provider of your plugin owns is describable and unreachable.

A target that runs commands (any `Session` or `Run`) **must** declare `Kill`, `ExitReport` and `Stall` in its capabilities. `exec.NewTargetPlugin` and `exec.RegisterPlugin` return an error at construction for anything the contract refuses (each is prefixed `exec target N ("provider/type"): `):

| You did | The error says |
|---|---|
| left `Kill`, `ExitReport` or `Stall` unset on a target with `Session` or `Run` | `a target that runs commands must declare ExitReport, Stall, Kill in Capabilities: ...` (it lists the ones you left out) |
| left `Info.Provider` or `Info.Type` empty, longer than 256 bytes or not plain text | `Info.Provider must be 1 to 256 bytes of plain text` (or `Info.Type`) |
| left `Exec` nil | `the Exec function is required` |
| registered two targets with one `Provider` and `Type` | `duplicate exec target key "provider/type"` |
| set `Integration` without `TTY` | `Capabilities.Integration needs Capabilities.TTY: ...` |
| registered more than 256 targets | `N exec targets exceed the limit of 256` |

**Limits.** The defaults of `DefaultSessionLimits()`: 32 sessions, a 64 KiB input frame, a 1 MiB replay ring (a host may ask for up to 4 MiB), a 5 minute detach lease (up to 30 minutes), terminals up to 1024 cells a side (at least 80, because a start without a size gets 80 x 24). Those of `DefaultRunLimits()`: a 30 s timeout (up to 5 minutes), 256 KiB of output per stream (up to 1 MiB), 64 KiB of stdin, 8 concurrent runs, 64 KiB of argv and of env, a 1 s post-exit drain. A value above the contract's ceiling (more than 4096 sessions, a lease over 24 hours, a run timeout over 1 hour, ...) is a registration error.

### The minimal ExecFunc

```go
func echo(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	p.Started(exec.SessionFacts{Program: exec.Program{Argv: p.Request.Argv, Name: "cat"}})
	if _, err := io.Copy(p.Stdout, p.Stdin); err != nil && ctx.Err() == nil {
		return exec.Exit{}, err // the target failed: the SDK classifies the error
	}
	return exec.ExitCode(0), nil // the command ended with its own status
}
```

Seven lines. `ExecFunc` must honor `ctx` (the SDK is to cancel it to close, kill, time out, retire the connection or shut the plugin down) and returns when the command has ended: `(Exit, nil)` for any status, a non-nil error when the target failed, `ExitLost` when the stream to the target ended with no status. `p.Started` reports what you learn once the command runs (the program that really runs, a pid, whether a terminal was granted, and the session's capabilities if they turn out narrower than the ones it was opened with): call it at most once, and before the first byte you write, because the host receives the session's `Started` before its first output. A session whose function writes without having called it gets a `Started` that the SDK makes from the facts of its opening (the requested program and the opened capabilities); a call after the first output, or a second one, is ignored and logged, and `exectest.ProcRig` tells your test. A session that neither writes nor calls it has none. `p.Stdout` fails once the session has ended, `p.Stdin` reports `io.EOF` when the host closes the input or the session ends, and `p.Resize` is closed when the command ends.

**When your function breaks the contract.** The engine runs your function on its own goroutine, once for each command that was admitted, and contains what a function can get wrong. Nothing a misbehaving function does crosses the wire as its own text, and each of these is counted (`omniview.plugin.self.sdk.exec.violations`, once for each session and kind) and logged by the SDK, so you find it in your plugin's log:

- **A panic in `ExecFunc`** is recovered. The command ends `Failed{INTERNAL}` (a command that had not started fails at `START`, one that had at `RUN`). The panic value and the stack go to the plugin's own log at `error` and nowhere else: not into the end, the metrics or the trace. A panic in a goroutine you started yourself is plain Go and ends the process.
- **Which goroutine may call `p.Started` and `p.Link`:** any goroutine of your function, while it runs. Both are safe for concurrent use. `Started` counts once, and only before the first byte of output; a second call, a call after the first output, and one with facts that are not within the bounds of the session are refused and logged (a start gate that already opened with the SDK's own `Started` stays what it was). `Link` is for a target that declared `LinkHealth`; a state that is not a state of the link is refused. A call of either after the function returned is refused.
- **A write after your function returned,** or after the session ended, fails with `io.ErrClosedPipe`, is counted (`late writes` and `late bytes` of the result, `omniview.plugin.self.sdk.exec.violations{reason=late_write}`) and is never delivered: the end is already decided. Output that you wrote and the host did not read is still there to read until the stream's last frame. A read of `p.Stdin` after the host closed the input or the session ended returns `io.EOF`.
- **A function that ignores `ctx`:** a close, a kill, a timeout, a retirement and a shutdown all cancel `ctx`. The end is typed at once from who asked (`Closed{by}`), and the SDK then waits a bounded time for the function to return: the grace (5 s by default) after a close, a retirement or a host that went away; the **kill wait, 50 ms, counted from the kill and never the grace**, after a kill (a host's kill frame, and a run's timeout); and 1 s (the shutdown grace) when the plugin is stopping, because the host stops a plugin two seconds after it asks. A function that honors `ctx` returns within its wait: it is not abandoned, and its session's place is free when the end is delivered, so a host that kills a session and starts another at once is not refused. A function that has not returned when its wait runs out is **abandoned**: the command ends without it, the violation is counted (`omniview.plugin.self.sdk.exec.violations{reason=ignored_cancel}` when the grace or the shutdown grace ran out, `did_not_return` when the kill wait did) and listed in the result, the plugin's log says so at `error` at that moment (the first time, and at most once a second for each target after that), and the gauge `omniview.plugin.self.sdk.exec.abandoned` shows it until it returns. The SDK cannot stop a goroutine, so an abandoned function keeps what it holds until it returns; `ctx` is the only way it is told. **It also keeps its session's place** (a run's, its place among `Run.MaxConcurrent`): the target's `Session.MaxSessions` bounds the running sessions and the abandoned functions together. **Functions that never return keep their places until the plugin restarts, and the target is then dead for that kind of command.** A start that the abandoned functions make too many is refused `UNAVAILABLE` with the reason `TARGET_WEDGED`, which is neither `LIMIT` (live work fills the target) nor the bare `UNAVAILABLE` of a plugin that is shutting down: it tells a host to say "restart the plugin" and not "try again", because retrying does not help until the functions return, and they may never. The refusal is counted in `omniview.plugin.self.sdk.exec.abandoned.refused` and logged (the first time, and at most once a second for each target after that), and `Describe` still lists the target, with nothing marked. What a stuck target can leak is its declared maximum, however often a host asks, and a place is free the moment its function returns.
- **An `Options` callback that ignores its context, and a `Classify` that never returns.** Both are your code, and a start, a call of `Options` or an end waits for them, so all are bounded. The context of `Options` ends at its deadline (30 s), and at once when the caller gives up, the plugin shuts down or the connection is retired; the callback then has the same grace to return (the shutdown grace, 1 s, when the plugin is stopping). One that does not is given up on: the start or the call is refused (`TIMEOUT` for the deadline; the caller's own cancellation or timeout when it gave up; the cause's own refusal otherwise: `UNAVAILABLE` for a shutdown, `CONNECTION` / `CONNECTION_RETIRED` for a retirement), the connection's lease is released (a retirement of the connection does not wait for the callback), `violations{reason=options_hung}` is counted, the plugin's log says so (the first time, and at most once a second for each target after that), and the goroutine goes on until it returns: the gauge `abandoned` shows it, and it keeps a place. For a start that place is the session's. **A call of `Options` has no session, so its place is among the callbacks of its target that are in flight: at most 32 at once, the ones given up on that have not returned included.** A call that would be one more is refused `UNAVAILABLE` and counted in `omniview.plugin.self.sdk.exec.options.refused`: with the reason `TARGET_WEDGED` when one of them was given up on and has not returned (restart the plugin), and with no reason when every one is still inside its bound (the target is busy: try again later). The cap is for each target: a target whose callbacks hang does not refuse the calls to another. It may use the client it was given after the lease was released. `Classify` has no context to honor and is given 5 s: if it has not returned, the error is classified by the SDK's own rules (`INTERNAL` for one nobody can classify), `violations{reason=classify_hung}` is counted, and the session ends all the same.
- **A function that returns before the process ended** (the context was cancelled, or it returned a status early): what it returns decides, because the SDK knows only what the function reports. A function that returns after its `ctx` was cancelled ends `Closed{by}`, not `Exited`, whatever it returned; that is the cause of the end and the status is dropped. If you need the status of a command you closed, report it through the target's own channel.
- **A function that never reads `p.Resize`** for a target that declared `Resize`: sizes wait in a queue that holds the first size and the newest (a resize storm costs two entries), and the SDK logs `resize_unread` once, after 5 s.
- **Exit 0 on a lost stream:** a status returned while the last `p.Link` report was `LinkStalled` is reported as the transport's (`TRANSPORT` source, never `PROCESS`) and logged as `stalled_status`; an error returned together with an exit status is a failure (`exit_and_error`); `ExitLost` is never turned into a code.

The queues are bounded. The input queue holds 256 KiB. The output queue of a target that declared `StallPropagates` (or declared nothing) holds 256 KiB, in at most 4,096 runs of one stream, and blocks your `Write` at that bound until the host reads. The output queue of a target that declared `StallLossy` holds what the session's replay ring is declared to hold, `Session.ReplayBytes` (1 MiB by default, and a host may ask for up to `Session.MaxReplayBytes`, 4 MiB by default; a run's is 256 KiB), so a lossy session costs up to 4 MiB of output where a blocking one costs 256 KiB: size a plugin's memory from the target's declaration, not from the number 256 KiB. It never blocks your `Write`: the oldest bytes are dropped, counted and reported to the reader as the exact range that was lost, at most 64 ranges at a time (the rest merge, and the byte counts stay exact). A session that is ending never waits for a reader, whatever its target declared, so a blocked `Write` is released at the end and what the queue could not hold is counted (`DroppedBytes`, and the end's output is then `TRUNCATED`).

## How a command ends

Return exactly one of these. An exit is never guessed.

| You return | The consumer reads | What it means |
|---|---|---|
| `ExitCode(n)` | `Exited{n, PROCESS}` | the process's own status, as the target relayed it. **`n` may be any integer type** (`int`, `int32`, `uint32`, `int64`, ...): a Windows DWORD arrives exactly, `ExitCode(uint32(0xC0000005))` is 3221225477 on the wire, and a reporter's negative 32-bit reading of the same DWORD stays negative. Never inferred from a code range |
| `ExitSignal(name, n)` | `Signaled{..., PROCESS}` | the target said a signal killed it; **only** for a target whose `Capabilities.ExitReport` is `ExitReportCodeAndSignal`. From any other target a signal is never turned into a code: the end is a failure of the target (`INTERNAL`) with unknown survival, and your bug is logged. A name that is not valid text (UTF-8, no control characters, at most 256 bytes) or a number outside 1 to 64 is dropped, and the other identifier is kept |
| `ExitFolded(n)` | `Exited{n, SIGNAL_FOLDED}` | the target itself says the code is a signal death folded into 128+n: no signal is claimed. An ordinary exit can carry the same number, so never infer this from a code; a target that gives no provenance reports `ExitCode` |
| `ExitTransport(n)` | `Exited{n, TRANSPORT}` | the transport said the stream ended and relayed a code that is not the process's own status. Use it only when the transport really hands you one; a stream that ended with **no** status is `ExitLost` |
| `ExitLost(cause)` | `Lost{cause}` | the stream to the target ended with no terminal status (`LostNoStatus`: it closed with nothing on its status channel; `LostLinkDead`: the keepalive gave up). **Lost is never exit 0**, never a failure of the command, never a reason to retry it |
| `Exit{}` (empty) or a mixed `Exit` | `Failed{INTERNAL}` | a failure of the target, never an exit 0 |

`ExitCode`, `ExitFolded` and `ExitTransport` take one of `int`, `int8`, `int16`, `int32`, `int64`, `uint8`, `uint16`, `uint32`. `uint` and `uint64` are not accepted, because a value above `math.MaxInt64` would not fit the signed 64-bit wire code. A target whose (effective) `ExitReport` is `ExitReportNone` cannot relay a process status: an `ExitCode` from it is reported with source `TRANSPORT`, and the SDK logs your bug.

**Does the process survive?** The end carries `remote_may_survive`, set from your `Exit` by the end conversion (`Exit.end`, which the engine calls): `Lost` is **true** (the process may still run); `Exited` or `Signaled` from `PROCESS` or `SIGNAL_FOLDED` is **false** (the target reported its status, so it is gone); `Exited` from `TRANSPORT` and a failure are **absent**, which means **unknown**, and a consumer treats unknown as "may survive". Absent is never "no". A `Closed` end says `false` only for a target that declared `Kill` as `KillTerminates`; for `KillBestEffort` and `KillClosesStreamOnly` it says `true`, and for a target that declared nothing it says nothing.

## What the host receives of a session

A host opens a session on one stream (`Open`), and the SDK sends frames in this order whatever your function does:

| Frame | When | What it carries |
|---|---|---|
| `Opened` | first, always | the facts the SDK knew before your function ran: the target, the requested program and size, the capabilities the session was opened with (your declared set narrowed by the choice the selection names) and the limits as you declared them. Nothing only the target can say is in it: the terminal, the pid, the TERM and the transport are absent |
| `Notice` | after `Opened` | `SIZE_DEFAULTED` when a terminal was asked for with no size and the SDK gave it 80 x 24 |
| `Started` | at most once, before the first `Output` | `Opened`'s facts refined by what you reported with `p.Started` (program, pid, terminal, size, TERM, transport, narrower capabilities), or the SDK's own from `Opened` when your function wrote output before it reported |
| `Output` | as your function writes | up to 64 KiB of one stream (`TTY`, or `STDOUT` or `STDERR`) with its `offset`, the position of its first byte in the one sequence of the session's output; offsets have no gap except across a `Dropped` range, and the bytes are untouched |
| `Dropped` | before the output that follows the range | exactly the range the queue lost: the stream, `from_offset` and `bytes`, with `EAGER_DRAIN_OVERFLOW` for a target that declared `StallLossy` and `REPLAY_FULL` for a session that was ending |
| `Link` | when the state changes | the state you reported with `p.Link`, only for a target that declared `LinkHealth` (`silent_ms` is 0: you report a state, not how long it has lasted) |
| `Ended` | last, once | how it ended (`End`), whether its start was observed, the duration, `bytes_out`, `bytes_in`, `dropped_bytes`, the output's completeness, `last_offset` (one past the last byte: equal to `bytes_out`) and `mode_reset` (present and empty for a session without a terminal; absent with one, which the host reads as "apply your own full reset") |

After `Ended` the stream ends with status `OK` and nothing follows; when `Ended` arrives, the session's id is free again. A request that is refused is a status and no frame, and your function did not run.

The host sends input (frames of at most `Session.MaxInputFrame`; the SDK stops reading frames while the function's input queue is full, so a function that never reads stops the host's `Write` at 256 KiB), resizes (latest wins; the first value of `p.Resize` is the size the session was opened with) and a close (graceful, or a kill that does not wait for the function). An input frame over the maximum ends the stream with `LIMIT` / `INPUT_TOO_LARGE` and the session with it, and so does a second `open` frame (`INVALID_REQUEST`). Frames this build does not serve (`close_input`, `signal`, `query`, `detach`) are ignored and counted (`omniview.plugin.self.sdk.exec.frames.ignored`).

A session lives exactly as long as its stream: a host that cancels, closes its side or loses its connection ends the session (the function's `ctx` is cancelled, and it has the grace to return; the id is free once the function has returned or has been given up on). One stream is not slower for another's slowness: each has its own queue, its own transport window and its own goroutines, so a host that stops reading one session stops that session's function (or, for a lossy target, makes it drop) and nobody else's.

**When the plugin stops.** A host that stops a plugin with go-plugin's `Client.Kill` first calls the controller's `Shutdown`, which stops the plugin's server at once, and then waits 2 s for the process to exit before it kills it. The SDK holds that one call: it ends every session and every run with `Closed{PLUGIN_SHUTDOWN}`, cancels each function and gives it 1 s (not the grace of a close) to return, and lets the call go on, and the server stop, once the handler of every attached stream has sent its `Ended` and every `Run` has handed over its result, or after 1.5 s, whichever comes first. So **each attached stream receives its `Ended{Closed{PLUGIN_SHUTDOWN}}`, and then its clean end, when the plugin is stopped through the controller**; a function that has not returned after its 1 s is abandoned (see above) and its command still ends. The `Ended` is handed to the transport before the server stops, and the transport writes it as the host's window allows: a host that is not reading, or is far behind on a flood, can still lose it with the connection. A host that kills the process without asking, a plugin that crashes, and a stream that was not read in time read the stream as broken (`CONNECTION`) with no `Ended`, as they always could. Once the server has stopped, however it was stopped, the SDK tells the plugin's capabilities once more, with at most 10 s: for a plugin that stopped itself, the functions are then cancelled and given the same 1 s, though no stream is left to receive an end; after a controller stop it finds nothing left to end.

## Bounded runs: `Run` and `RunStream`

A host that wants the answer of one command and not a terminal calls `Run`: one request, one `RunResult`. `RunStream` is the same run with its output as events. A target serves them when it declares `Info.Run` (`exec.DefaultRunLimits()` is the usual choice), and `Describe` lists `FEATURE_RUN` and `FEATURE_RUN_STREAM`. Your function is the same `ExecFunc` it is for a session, given a `Proc` for a command **without a terminal**: `p.Request.TTY` is false, `p.Stderr` is set (stdout and stderr are two streams), `p.Resize` never delivers a size, and `p.Stdin` is nil unless the request carried stdin.

**Safe by construction.** The request is strict, as every request is, and:

- **argv only.** `RunRequest` has no command string: `argv[0]` is a non-empty program with no NUL, and a shell is `["sh", "-c", script]`, visible as such to consent and audit.
- **Every bound is mandatory, and none is clamped.** `timeout_ms` is the target's `Run.DefaultTimeout` when zero and `INVALID_REQUEST` above `Run.MaxTimeout`; each output cap is `Run.DefaultOutputBytes` when zero and `INVALID_REQUEST` above `Run.MaxOutputBytes`; stdin is at most `Run.MaxStdinBytes` (`LIMIT` / `INPUT_TOO_LARGE`), argv and env at most `Run.MaxArgvBytes` and `Run.MaxEnvBytes`. A target that does not honor `env` or `cwd` refuses a request that carries one `UNSUPPORTED`; it is never ignored.
- **Places are bounded.** At most `Run.MaxConcurrent` runs of a target kind are in flight: one more is refused `LIMIT` / `TOO_MANY_RUNS`. A run whose function was given up on keeps its place until the function returns (see [A function that ignores `ctx`](#the-minimal-execfunc)), and while abandoned functions hold every place a new run is refused `UNAVAILABLE`, which is not a limit of concurrent runs: nothing ran, and retrying does not help until they return.
- **A refusal is a status, and nothing ran.** Everything after your function was called is a `RunResult`, whatever happened (below).

**The id.** A run is held under its `request_id` (at most 128 bytes of printable ASCII), or under `run-` and an identifier the SDK makes when the host sent none; the result echoes the request's own id, empty when there was none. Runs and sessions share **one id space**: an id that is live is refused `CONFLICT` / `SESSION_EXISTS`, for a run and for a session alike. The id is free again once the host has read the result (the call answers after the engine has released it), so a host that reads a result and runs the same id again is not refused for the run that has just ended.

**The output is kept in a window and counted exactly.** The SDK reads the output as your function writes it and keeps at most the cap of each stream, whatever the command writes: the middle is counted and never stored, and memory grows with the data up to the cap and never past it (a run that wrote ten bytes holds about ten bytes). `output.policy` says which part is kept:

| Policy | Kept |
|---|---|
| `HEAD_TAIL` (the default) | the first ceil(cap / 2) bytes and the last floor(cap / 2): an error is usually at the end |
| `HEAD` | the first cap bytes |
| `TAIL` | the last cap bytes |

`stdout` and `stderr` of the result are the kept bytes, the head and then the tail, **raw and untouched** (never decoded, repaired or cut at a character), and each stream has an account: `total_bytes` (every byte the command wrote, the ones the engine's queue lost before the SDK read them included), `kept_bytes`, `dropped_bytes` (always `total - kept`), `truncated` (some byte was dropped), `head_bytes` and `tail_bytes` (how the kept bytes split), `valid_utf8` and `completeness`. `valid_utf8` is about the bytes **as returned**: a sequence that the cap cut is not repaired, and a head and a tail joined across the elided middle are judged as joined; the host's tool boundary decides text or base64 from it. A target that declared `StallLossy` whose queue overwrote output before the SDK read it has those bytes counted as dropped too, which ends the head where the lost range falls and empties the tail, so that the head stays the beginning of the stream and the tail its end, each in one piece.

`completeness` is exactly one of: **`TRUNCATED`** when a byte of the stream was dropped, whatever else happened; **`COMPLETE`** when nothing was dropped, the run ended with a status the target relayed (`EXITED` or `SIGNALED`, and not the transport's word for a status it did not relay), the engine vouches for the output (nothing was refused after the end, nothing was left to a function that was given up on) and the output was read to its end before the post-exit drain (`Run.PostExitDrain`, 1 s by default) ran out; **`UNSPECIFIED`** for anything else (a timeout, a cancel, a loss, a failure, an abandoned function, a drain that ran out). A host never reads `UNSPECIFIED` as complete.

**How a run ends.** Each end of the command is one outcome of the result:

| The command | `outcome` | `exit_code`, `signal` | `exit_source` | `remote_may_survive` | `failed` |
|---|---|---|---|---|---|
| returned `ExitCode`, `ExitFolded` or `ExitTransport` | `EXITED` | the code, exactly as reported | the one you gave | the end's: `false` for `PROCESS` and `SIGNAL_FOLDED`, absent (unknown) for `TRANSPORT` | none |
| returned `ExitSignal` | `SIGNALED` | `signal` and `signal_number`, no code | the one you gave | as above | none |
| reached its timeout | `TIMED_OUT` | none | `HOST` | the end's: `false` only for a target that declared `KillTerminates` | none |
| was cancelled (its call, a close) | `CANCELLED` | none | `HOST` | as above | none |
| was ended because the plugin shut down | `FAILED` | none | none | as above | `UNAVAILABLE` |
| was ended because its connection was retired | `FAILED` | none | none | as above | `CONNECTION` / `CONNECTION_RETIRED` |
| returned an error, panicked, or broke the contract | `FAILED` | none | none | absent | the error's class and reason (`INTERNAL` for a panic) |
| returned `ExitLost` | `LOST` | none | none | **true** | none |

`failed` is present exactly for `FAILED`, with a `stage`: `START` when the start was not observed and `RUN` once it was (what the SDK makes of a close), or the stage the engine gave the failure (`STREAM` for a failure of the connection after the start). The start is observed when your function calls `p.Started`, writes a byte or returns a status: `start_observed` says so, and `false` is not "the command never ran". `target` and `program` are reported only for a start that was observed (the program is the one your function reported with `p.Started`, or the one that was asked). A failure after the command was handed to your function is **data, never a status**, and carries the output read so far with its account.

**Timeout and cancel.** The timeout counts from the moment the SDK accepted the request, so a slow start takes from it. When it passes, the SDK ends the run with a kill: your function's `ctx` is cancelled, the SDK waits the kill wait (50 ms) and not the grace for it to return, and the call answers `TIMED_OUT` with the output read so far. A function that honors `ctx` is not abandoned; one that does not is given up on and keeps its place (above), and the call still returns. So the call returns at its timeout plus the kill wait. A caller that cancels its call, or whose deadline passes, ends the run too, gracefully (your function has the grace), and gets the status of the transport's own, which says nothing about whether the command ran. A plugin that shuts down ends the run with `FAILED` / `UNAVAILABLE`, and a retired connection with `FAILED` / `CONNECTION_RETIRED`: both are results.

**Stdin.** The stdin of the request is written to `p.Stdin` and then closed, so the function reads it and then `io.EOF`. The writer belongs to the command and ends with it: a function that never reads its stdin does not hold the run.

**`RunStream`.** The same run, with these events in this order and no other:

| Event | When |
|---|---|
| `RunStarted` | once, as soon as the start is observed, and before the first `Output`: the target and the program (the one your function reported with `p.Started`, or the one that was asked). A command that ended with a status having neither written nor reported gets it just before `RunEnded` |
| `Output` | as your function writes: up to 64 KiB of one stream with its `offset` (the position of its first byte in the one sequence of the run's output, as for a session) and the bytes untouched |
| `RunProgress` | at the SDK's pace: at most once a second, and only when output advanced since the last one; `elapsed_ms` and `bytes_out` (every byte the command has written so far) |
| `RunEnded` | last, once: the result without the payloads (`stdout` and `stderr` are empty, they were streamed), with the outcome, the exit, the accounts and the failure |

A stream **cannot keep a head and a tail**, because it cannot take back what it has sent: it delivers the bytes of each stream, in order, up to the cap of that stream, whatever policy the request named, and counts the rest as dropped (`TRUNCATED`; the offsets then have a gap where the delivery stopped). A run that failed or was lost before its start was observed sends `RunEnded` alone, with no `RunStarted`. A refusal ends the stream with a status and no event. Nothing follows `RunEnded`, and the stream then ends with status `OK`; the id of the run is free by then. A host that goes away (cancels, loses its connection) ends the run. A host that stops taking events stops your function (its `Write` blocks, or drops for a lossy target) and does not stop the run's timeout; once the command has ended the host has `Run.PostExitDrain` to take what is queued and `RunEnded`, and a stream whose host has not by then ends with the status `DEADLINE_EXCEEDED`, which carries no exec `ErrorInfo` (it is no refusal) and says nothing about the command.

**The host's client.** `plugin.V2().Run(ctx, req)` returns the plugin's `*execv2.RunResult` as it came: every outcome, `LOST` and `FAILED` included, is a result and never an error, and an absent `exit_code` is none, never 0. An error means the command was not taken or the answer never arrived: an `*exec.Error` for a refusal of the exec lane (nothing ran), the resource lane's own error for the connection and the catalog pin, or an `*exec.Error` of the transport's own class (`CONNECTION`, `TIMEOUT` or `CANCELLED`) when the call broke before the result, which says nothing about whether the command ran and is never read as a failure of it. A call whose `ctx` has no deadline is bounded by the request's `timeout_ms` plus the 30 seconds of the call timeout (an hour plus them when `timeout_ms` is 0); cancelling `ctx` cancels the run. `plugin.V2().RunStream(ctx, req)` returns an `exec.RunConn` once the first event, or the refusal, has arrived: a command that is silent makes it wait until the command ends (the first event of a run whose start is not observed is its end), so a caller that must act meanwhile calls it on a goroutine and cancels `ctx` to stop it. `Recv` returns the `RunEvent`s in order and `io.EOF` only after `RunEnded`; a stream that ends with status `OK` and no `RunEnded` is an error of the plugin, never an end; `Close` cancels the run. A caller with no deadline gets the same bound as `Run` for the whole stream. The client sends the request as it is: the plugin refuses what it does not accept, and a host that wants to know before it asks reads `Describe`.

**What a host never retries.** A refusal is retried only when its class and reason say a retry can help (`LIMIT`, `UNAVAILABLE` and `CONNECTION` may pass; `INVALID_REQUEST`, `FORBIDDEN` and `CONSENT_REQUIRED` stay refused until the request or the host's state changes). A `RunResult` of `FAILED` or `LOST`, one of `TIMED_OUT` or `CANCELLED` whose `remote_may_survive` is not `false`, and any status that is not a refusal after the command was handed over (a cancelled call, a deadline, a broken connection, a stream ended by the drain) authorize nothing: the command may have run, and may still be running.

## Capabilities: two levels

`Capabilities` has two levels, and a plugin author writes both.

1. **The kind's upper bound.** `TargetInfo.Capabilities` is what the *most capable* session of this kind can do. You declare it at registration; it is fixed for the life of the plugin process, and a consumer can read it before any session exists (what a picker offers, which request it never sends).
2. **The session's effective set.** Each session has its own capabilities, in its facts, and they only ever narrow. `Opened` carries the upper bound narrowed by the session's **mode**: each `Choice` of your `Options` may carry `Capabilities` (`exec.Choice.Capabilities`), the complete set a session that selects it is served with, and the SDK narrows the kind's bound by the choice that each option takes in the request's selection (for an option the selection omits, its default choice). `p.Capabilities` is a deep copy of that set, so changing it affects no other session. A target whose modes differ declares them on its choices (see the illustration below), and its function reports nothing for them; it narrows a copy and reports it with `p.Started(exec.SessionFacts{Capabilities: &caps})` only for what it learns at run time. A `SessionFacts` whose `Capabilities` is nil keeps the set `Opened` carried.

**A session may only narrow.** For every one of the 16 fields a choice's value is at most the kind's, and a `Started`'s is at most the value `Opened` carried, in the order below. When a claim is above its bound the SDK keeps the bound's value and logs your bug: for a choice when it serves `Options` (live in this build: the log names the option, the choice and the field), and for a `Started` in the engine (this build does not run sessions yet, so that clamp arrives with it). `exectest.ProcRig` applies the same rules in your own tests today: build it with `exectest.RigCapabilities(yourTarget.Info.Capabilities)` and `exectest.RigOptions(options...)`, the options your `Options` callback returns (`Proc.Capabilities` is then a deep copy of the set the session is opened with, which `rig.Opened()` returns; without `RigOptions` that is the declared set, and without `RigCapabilities` a copy of a default fake target's). A choice that widens a field, and a `Proc.Started` report that widens one, are clamped and reported with `t.Errorf` at your call; `rig.Effective()` returns the session's set (the opened one until a report narrows it). Every capability can be narrowed, so one target kind can serve modes that differ. **A consumer that holds a session reads that session's facts and never `Describe`**; one that has only `Describe` reads each field as "the most it can do".

| Field | Says | Order (narrowest first) |
|---|---|---|
| `TTY`, `SeparateStderr`, `Resize`, `InitialSize`, `StdinEOF`, `Env`, `Cwd` | what the target honors (false: a request that needs it is refused `UNSUPPORTED`) | false < true |
| `LinkHealth` | the author reports the transport's health through `Proc.Link` | false < true |
| `Signals` | the signals that can be delivered; empty: only Close | inclusion: a subset |
| `Kill` | what ending a session does to the process | `KillClosesStreamOnly` < `KillBestEffort` < `KillTerminates` |
| `CwdFact`, `ForegroundFact` | how the target provides the fact; none: shown as unknown, never probed | `FactModeNone` < `FactModePull` < `FactModePush` |
| `HostRestart` | what outlives a host restart; none: never offer reattach | `SurvivalNone` < `SurvivalTargetProcess` < `SurvivalTargetSession` |
| `ExitReport` | what can ever be said about an end | `ExitReportNone` < `ExitReportCode` < `ExitReportCodeAndSignal` |
| `Stall` | what a reader that stops reading does to the target | `StallLossy` < `StallPropagates` |
| `Integration` | shell-integration modes accepted, never applied without the host's consent | inclusion: a subset |

`StallPropagates` means the SDK blocks `Write` and nothing is lost; `StallLossy` means it drains into the ring and counts what it overwrites.

### Illustration: a Kubernetes Pod, exec versus attach

One Pod kind serves two modes that differ in what can be said about them. An **exec** session starts a new process: its end carries an exit code, and nothing of it can be reached again after a host restart. An **attach** session joins the container's own process: no status is relayed at all, and the process outlives the host. The kind declares the union as its upper bound (`ExitReportCode`, `SurvivalTargetProcess`); each mode is declared on its `Choice` as the kind narrowed to what is true of it (exec: `HostRestart` none; attach: `ExitReport` none). The session is therefore **opened** with its mode: the host reads it before it opens anything, and your function never reports it.

The code below is [`examples/exec-plugin/k8s_illustration_test.go`](../examples/exec-plugin/k8s_illustration_test.go), compiled and tested in CI. It is written against a tiny stand-in for the parts of client-go it uses (`streamOptions` is `remotecommand.StreamOptions`, `codeExitError` is `utilexec.CodeExitError`, `executor` is `remotecommand.Executor`), so the example needs no Kubernetes dependency; the stand-in is defined in the same file. The mode comes from the request's selection: the target's `Options` declares a `mode` option whose choices `exec` and `attach` carry their capabilities (see [the Options snippet](#options-needs-a-resource-provider), which builds the two sets from `podCapabilities`).

```go
// podCapabilities is what a Pod can do at most: the upper bound of every session of the kind, which the two modes of a
// Pod narrow in different fields (see podOptions).
func podCapabilities() exec.Capabilities {
	return exec.Capabilities{
		TTY: true, SeparateStderr: true, Resize: true, InitialSize: true, StdinEOF: true,
		Kill: exec.KillClosesStreamOnly, ExitReport: exec.ExitReportCode, Stall: exec.StallLossy,
		HostRestart: exec.SurvivalTargetProcess, // attach reaches the container's own process again
		Integration: []exec.IntegrationMode{exec.IntegrationArgvWrapper},
	}
}

// podTarget is a Pod: one kind that serves two modes, exec (a new process) and attach (the container's own).
func podTarget() exec.Target {
	return exec.Target{
		Info: exec.TargetInfo{
			Provider: "k8s", Type: "pods", DisplayName: "Pod", DefaultArgv: []string{"/bin/sh"},
			Capabilities: podCapabilities(), // the upper bound: the most any session of this kind can do
			Session:      exec.DefaultSessionLimits(),
		},
		Exec: podExec,
	}
}

func podExec(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	// p.Capabilities is what the host read in Opened: the kind's upper bound narrowed by the mode this request chose
	// (the Capabilities of the choice, see podOptions). The mode is known before this function runs, so it is not
	// reported here, and nothing else is learned at run time, so Started is not called. client-go has no callback for
	// "the command is running"; the start this adapter observes is the first byte the command writes, and the SDK sends
	// the session's Started before it, made from the facts of Opened. A command that stays silent has none.
	attach := p.Request.Selection["mode"] == "attach" // the "mode" option of the target's Options
	run, err := executorFor(p, attach)
	if err != nil {
		return exec.Exit{}, err // Target.Classify maps "container not running", RBAC 403, "no such file"
	}
	opts := streamOptions{Stdin: p.Stdin, Stdout: p.Stdout, Stderr: p.Stderr, Tty: p.Request.TTY,
		TerminalSizeQueue: sizeQueue{p.Resize}} // a terminal has no Stderr: it is nil, which client-go reads as none
	err = run.StreamWithContext(ctx, opts)
	var code codeExitError
	switch {
	case !attach && errors.As(err, &code):
		// The API reports a code and nothing about where it came from: 137 can be a SIGKILL or a plain exit 137, so
		// no signal is inferred from the range. ExitFolded is only for a target that says a code is a folded signal.
		return exec.ExitCode(code.Code), nil
	case err == nil:
		// client-go returns nil both for a Success status and for a status channel that closed empty, so nil says
		// nothing about the exit: the stream ended and the command's status is not known. It is never exit 0.
		// (A plugin that reads the status channel itself can return ExitCode(0) for a Success status.)
		return exec.ExitLost(exec.LostNoStatus), nil
	default:
		return exec.Exit{}, err
	}
}

// sizeQueue hands Proc.Resize to client-go: Next waits for the next size and returns nil when the command has ended.
type sizeQueue struct{ resize <-chan exec.Size }

func (q sizeQueue) Next() *terminalSize {
	size, ok := <-q.resize
	if !ok {
		return nil
	}
	return &terminalSize{Width: size.Cols, Height: size.Rows}
}
```

Four things the code is honest about. A stream that ends with no status is `exec.ExitLost(exec.LostNoStatus)`, never `ExitTransport(0)` and never `ExitCode(0)`: the consumer then shows "connection lost, status unknown", not "exit 0". A real exit code is `exec.ExitCode(code)`, never inferred from a range. The mode is declared where it is known, on the choice, so it is in `Opened` before the function runs: the host can show "attach: no exit status, survives a host restart" in its picker, an attach session never offers an exit status, and the function starts from the set `Opened` carried and reports nothing for it. Last, it never calls `p.Started`: client-go has no start callback and a stream can read its stdin and still fail before the command starts, so the only start this adapter can observe is the first byte the command writes, and the SDK sends the session's `Started` before that byte, made from the facts of `Opened` (so its capabilities are the ones the mode already narrowed); a command that stays silent has none, and a stream that fails before any output reports nothing. There is no first-byte gate to write: the SDK guarantees that the host never receives output ahead of the session's `Started`, and it refuses, and logs, a `Started` that an author reports after the first output or twice.

## Absence is unknown

This is one rule for the whole contract: **a fact that a producer may not know is written so that "not known" is a value of its own, never the zero value of its type.** A bool fact is `optional bool`, a list whose emptiness is a fact (`signals`, `integration`) is wrapped in a message so that absent differs from empty, an enum has an `UNSPECIFIED` zero, a message fact is absent.

**What the SDK writes for you.** `Capabilities` is your declaration, so the SDK writes **every** field as present: an unset `bool` is a stated `false`, an empty `Signals` is a present empty set ("none"), `FactModeNone` is written `FACT_MODE_NONE`, `SurvivalNone` is `SURVIVAL_NONE`, an integration list without modes is a present empty set. It leaves a fact absent only when it does not know it: `SessionFacts.TTY` stays absent until you report it (`TTY *bool`: nil is "not said", a pointer to false is "no terminal"), a zero `Size`, empty `Program` fields and an empty `Term` or `Transport` mean "unknown", `Exit.Code` is a pointer, and `remote_may_survive` is written only when [the table above](#how-a-command-ends) says the SDK knows it. Do not state what you do not know: leave the field unset.

**What a consumer may assume.** Nothing from an absent value. An absent `Capabilities` message means nothing is promised; an absent bool or set inside a present one is not known, and `false`, an empty set and `NONE` are the producer's statement that the target cannot; an `UNSPECIFIED` enum is "unknown", never the nearest named value; a value a reader does not know (a newer peer) reads as `UNSPECIFIED`; an absent `remote_may_survive` reads as "may". Unknown is never turned into `false`, zero, none or exit 0. The rule is the "ABSENCE IS UNKNOWN" convention in the file header of [`proto/exec/v2/exec.proto`](../proto/exec/v2/exec.proto), and `TestAbsenceIsUnknown` and `TestBlocker1` to `TestBlocker5` in [`proto/exec/v2/contract_test.go`](../proto/exec/v2/contract_test.go) fail a field that breaks it.

## Requests: strict, and lists of names

A plugin **refuses** a request that carries a field it does not know (`UNSUPPORTED`), at any depth and inside the elements of a repeated message too, because a request field can change what a command may do. A host therefore sends a field an older plugin may not know only after `Describe` lists the feature that gates it. Frames and responses are lenient: an unknown field or frame kind is ignored and counted.

For that reason **a request carries no map**. The environment, the labels and the selection of a request (`Start.env`, `Start.labels`, `TargetRef.selection`, `RunRequest.env`, `RunRequest.labels`) are `repeated NameValue{name, value}`: wire-identical to a map entry, but an unknown field inside an entry can be seen and is refused. A name appears at most once in a list; a duplicate is `INVALID_REQUEST`, never "the last one wins". Your function sees them as the maps `Request.Selection`, `Request.Env` and `Request.Labels`, already validated. (`TargetFacts.selection`, a frame the plugin sends, stays a map.)

## A refusal is a status; everything after the hand-over is data

A **refusal** answers a request the SDK did not hand to your function, so nothing ran and a host may retry: a bad request, a stale connection, a limit, a reference that is not the caller's, an unsupported field. It is a gRPC status carrying `google.rpc.ErrorInfo` (domain `omniview.sdk.exec.v2`, reason = the error class, metadata `reason`, `program`, `selection_name`, `selection_value`, `retry_after_ms`) and, for a class other than `INTERNAL`, a bounded `LocalizedMessage` (`Error.Message`, at most 512 bytes). A plugin that does not serve the lane answers `Unimplemented`, which the client reports as `*exec.Error` with `ErrorClassUnsupported`.

**Once your function was called, the command may have run and may still be running, so how it ended is part of the result, never a status.** What you return from `ExecFunc` is therefore data:

- a session ends with `Ended{end}`: `exited`, `signaled`, `closed`, `failed{class, reason, facts, stage}` or `lost{cause}`;
- a run ends with `RunResult` (and `RunEnded` for a stream), whose outcome is exited, signaled, timed out, cancelled, **failed** (`OUTCOME_FAILED`, with a `Failed` message) or lost. It keeps the output read so far, says whether the start was observed, and says whether the process may survive.

A **failure after the command was handed over** (your function returns `(exec.Exit{}, err)`, or a stream breaks) is `Ended{failed}` or `RunResult{OUTCOME_FAILED}`; the same classification (`*exec.Error{Class, Reason, Facts}` or `Target.Classify`) fills it. A host retries a refusal and never a failed or lost end: a failed end may have run, may still run. `start_observed` false means the SDK did not see the start, not that the command never ran.

## One end per session, one last frame per stream

On an `Open` stream, **every stream that can still receive frames ends with exactly one of two frames**, and nothing follows it (a stream whose connection breaks gets neither: the break is an implicit detach, and the session is read from its tombstone if its lease then expires):

- **`Ended`**: the end of the **session**, sent **at most once** in its life, to the stream attached when it ends. It holds the `End` above, counts and the output completeness. A session has exactly one **end**, and `Ended` is the frame that carries it to a stream: a session that ends while no stream is attached (a lease that expires, a retirement, a shutdown) has no `Ended` at all, and its end is the tombstone's, which `ListSessions` returns as `SessionInfo.ended_with`.
- **`Detached`**: the end of **this stream** while the session lives on. It carries a `reason`: `DETACH_REASON_REQUESTED` (the host sent `Detach`; the session lives for the lease) or `DETACH_REASON_SUPERSEDED` (another stream attached and took the session over; the session continues attached to it). **A superseded stream is not an end**: there is no `Ended` for it and no `closed` value for it; a consumer keyed on `Ended` never marks a live session dead.

Your function runs once per session and returns once; the SDK turns that into the one end of the session, and into the `Ended` frame when a stream is attached to receive it.

## Options needs a resource provider

`Options` lists what a user can choose for one resource before a command runs in it, so that a picker shows real states:

```go
result, err := plugin.V2().Options(ctx, exec.TargetRef{Reference: ref, Route: route}) // ctx carries the call's types.PluginContext and the catalog pin
// result.Options[0] = {Name: "container", Required: true, Choices: [{Value: "app", State: ChoiceStateReady, IsDefault: true, Kind: "container"}, ...]}
```

A host can reach your `Options` callback only if **all** of these hold:

1. **A resource provider is registered in the same plugin** (`sdk.RegisterResourcePlugin`), and your `Info.Provider` and `Info.Type` are that provider's id and a resource type it serves. `Options` is admitted like any resource utility call: the request must name an exact reference and a route with a connection, the connection must be the caller's, and the catalog pin the host sent must be current. A request that carries a selection is refused `INVALID_REQUEST`; a `Provider`/`Type` with no registered target is refused `UNSUPPORTED`.
2. **That provider owns connection lifecycle**: its `Impl` implements `resourcev2.ConnectionContextProvider` (a `WithConnection` method), which a `*resourcev2.Controller` has and which works only when the controller is configured with a `Lifecycle` (`LoadConnections`, `CreateClient`, `DestroyClient`, ...). The SDK binds that provider to the exec capability when both are registered, in either order, and your callback then runs inside the connection lease (cancelled when the connection is retired: the caller gets `ErrorClassConnection`/`ErrorReasonConnectionRetired`).

The SDK's own [`examples/resource-plugin`](../examples/resource-plugin) provider is a controller with **no** `Lifecycle`, so on a plugin built from it `Options` is refused `UNSUPPORTED` (by the resource lane: the caller sees a `*resourcev2.ResourceOperationError`, not an `*exec.Error`); with a `Lifecycle` it works. **Every target is admitted the same way, whether or not it has an `Options` callback**: a request with no connection, no or a stale catalog pin, or another connection's route is refused by the resource lane (`RESET_REQUIRED`, `INVALID_REQUEST`) before anything else; once admitted, a target with **no** `Options` callback answers an empty list. The SDK bounds what your callback returns: at most 8 options with at most 256 choices each, option names, details and kinds as closed codes (`^[a-z0-9_.-]{1,40}$`, never prose), at most one default choice per option; a callback that breaks them is answered `INTERNAL`, and so is one that panics (recovered and logged: the plugin keeps running). `Options` is a read, so a host may retry any failure of it. [Manage connections](connections.md) shows how to wire a controller and its hooks (the runnable `ExampleLifecycle` is in [`pkg/resource/v2/example_test.go`](../pkg/resource/v2/example_test.go)). The exec example above has neither a provider nor a callback for exactly this reason.

With such a provider, the Options callback of the Pod target is the snippet below ([`examples/exec-plugin/options_snippet_test.go`](../examples/exec-plugin/options_snippet_test.go), compiled and tested in CI; `getPod` and the `pod` type stand in for your client). Set it as `target.Options`. It declares the `mode` option that `podExec` reads, with the capabilities of each mode on its choices, and shows how a container's state maps to a `Choice`: `Ready` runs a command now, `Starting` will probably be ready soon, and `Stopped` cannot run one now, with a closed-code `Detail` (`terminated`, `crash-loop`) that a host never has to parse as prose.

```go
// podOptions is the target's Options callback: the choices a picker shows for one Pod. ctx carries the connection the
// host named; use it to get your client.
func podOptions(ctx *types.PluginContext, ref resourcev2.ResourceReference) ([]exec.Option, error) {
	info, err := getPod(ctx, ref.Address.ID)
	if err != nil {
		return nil, err
	}
	containers := exec.Option{Name: "container", Required: true}
	for _, c := range info.containers {
		choice := exec.Choice{Value: c.name, Kind: c.kind, IsDefault: c.name == info.defaultContainer}
		switch c.phase {
		case "running":
			choice.State = exec.ChoiceStateReady
		case "starting":
			choice.State = exec.ChoiceStateStarting // will probably be ready soon, and is not now
		default: // "terminated", "crash-loop": cannot run a command now; Detail is a closed code, never prose
			choice.State, choice.Detail = exec.ChoiceStateStopped, c.phase
		}
		containers.Choices = append(containers.Choices, choice)
	}
	// The mode of a session is declared on its choice, before anything opens: the complete set a session that selects it
	// can do, which is the kind's upper bound (podCapabilities) narrowed. The SDK opens the session with it, so the
	// host reads it in Opened, and podExec starts from it and reports nothing.
	execMode := podCapabilities()
	execMode.HostRestart = exec.SurvivalNone // an exec'd process cannot be reached again after a host restart
	attachMode := podCapabilities()
	attachMode.ExitReport = exec.ExitReportNone // attach relays no status at all

	mode := exec.Option{Name: "mode", Choices: []exec.Choice{ // the "mode" podExec reads from the selection
		{Value: "exec", State: exec.ChoiceStateReady, IsDefault: true, Capabilities: &execMode},
		{Value: "attach", State: exec.ChoiceStateReady, Capabilities: &attachMode},
	}}
	return []exec.Option{containers, mode}, nil
}
```

The imports are `resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"`, `"github.com/omniviewdev/plugin-sdk/pkg/types"` and the `exec` package.

A choice's `Capabilities` is the **mode** of the choice: the complete set a session that selects it is served with. Copy the kind's capabilities (`TargetInfo.Capabilities`, the upper bound of every session) and narrow the copy, each field only down in the order of [the table above](#capabilities-two-levels); `nil` means the choice does not narrow. The SDK serves it with the options and applies it at admission: `Opened` carries the kind's capabilities narrowed by the choice that each option takes in the request's selection (the default choice of an option the selection omits), and `p.Capabilities` starts from that set. A field above the kind's is the author's bug, not an error: the SDK serves the kind's value for it and names the option, the choice and the field in the plugin's log. A host reads a choice's capabilities with the options, so a picker can show "attach: no exit status, survives a host restart" before it opens anything.

### Describe

`plugin.V2().Describe(ctx)` (the `Client` a host gets from `exec.PluginClient.V2()`) returns the wire `*execv2.DescribeResponse`: `contract_version`, the `features` this build serves and the `targets`, each with its upper-bound capabilities (every field present) and the limits in force, in registration order. It is static, needs no connection, and the client validates it before you see it. The limits are effective and never zero: a present `session` or `run` section carries non-zero bounds, the SDK never sends an all-zero one, a consumer refuses it as a malformed description, and an absent section means that service is not offered. A target with neither section is one that only offers `Options`. This build lists `FEATURE_SESSION`, `FEATURE_RUN`, `FEATURE_RUN_STREAM`, `FEATURE_ENV` and `FEATURE_CWD`.

## Test your target

```go
func TestConformance(t *testing.T) { exectest.Conformance(t, myTarget()) }
```

`exectest.Conformance` serves your target through the real SDK over a real transport (a unix socket) with a fake connection registry standing in for the resource provider, and runs what this build serves: that registration agrees with `Describe` and claims no capability you did not declare, that `Describe` is static, that your `Options` answer is within bounds, and the wiring of the lease and the catalog pin. If your target has an `Options` callback, pass the id it knows: `exectest.Conformance(t, target, exectest.ConformanceResource("web-0"))`.

**Some of the engine cases run the SDK's real engine, and none of them calls your `ExecFunc`.** In a subtest `over the wire` (run once the build lists `FEATURE_SESSION`): case 1 (`Opened` is first and its facts agree with `Describe`), case 2 (input and resize frames reach the function in order), case 3 (offsets are contiguous and the bytes untouched), case 4 (a slow consumer bounds memory and a blocking target drops nothing), case 5 (one session's stall does not move another's echo) and case 18 (completeness: a lossy target counts what it lost, a blocking one ends `COMPLETE`). In a subtest `in process`: case 9 (every kind of end is typed, a command has exactly one, nothing follows it), case 14 (a retired connection ends its commands), case 28 (`remote_may_survive` per end), the pair test P5 (ending twice, after the exit and during the start give the same end) and the stress harness (`exectest.Stress`). They run against a fake target that has the `Info` you declared and a script of their own, so what you declared decides what they expect: a signal only when `ExitReport` is `ExitReportCodeAndSignal`, a closed command's process gone only when `Kill` is `KillTerminates`, and a lossy case only for a target that declared `StallLossy`. The stress harness runs the SDK's own scripted targets (it checks the engine's end state, not your declaration; it runs 5 s, 1 s under `-short`, or as long as `OMNIVIEW_LIFECYCLE_HAMMER` says). A target that declares neither sessions nor runs has nothing to run, and they skip. The wire part of cases 9, 12, 14, 17, 23, 25, 26, 28 and of P5, the `Open` part of cases 13 and 16, and every case of a step that is not served yet (6 to 8, 10, 11, 19 to 22, 24 and the other pair tests), are listed under `engine` and **skipped**, each with the step that fills it. A build that lists a feature in `Describe` whose cases are not written fails instead of skipping. Meanwhile, test your function yourself: `exectest.NewProcRig(t, request, exectest.RigCapabilities(target.Info.Capabilities), exectest.RigOptions(options...))` runs an `ExecFunc` with a `Proc` whose stdio you control, opened with the mode that your options declare for the request's selection (`rig.Opened()`), records the `Started` the host would receive (yours, or the one the SDK makes at the first output) and what you report through `Link`, and holds your report to the rules described above; `exectest.NewFakeTarget` is a scriptable target. The [Kubernetes illustration's tests](../examples/exec-plugin/k8s_illustration_test.go) run an exec and an attach session this way, a quiet container and a container that writes among them, and show a session that widens, and a late and a second `Started`, being refused. The rig runs your function with a `Proc` you control; the engine that runs it for a host serves `Open` over the wire.

**The bounded run over the wire** (the same subtest `over the wire`, run once the build lists `FEATURE_RUN`, and `FEATURE_RUN_STREAM` for what `RunStream` adds; skipped for a target that declares no `Run` limits): case 15 (`Run`: an empty argv and every bound of the request refused and never clamped, the exit as your `ExitReport` declares it, a timeout and a cancel that end the run, the head and the tail that the cap and the policy keep with an exact account of each stream, `valid_utf8` of the bytes as returned, the limit on runs in flight and the id of a run), the `Run` part of case 13 (class, reason and facts of an error round-trip, and a canary placed in the local cause of an error and in a panic appears in no result, no event and no status), the `Run` part of case 16 (a stream that ends with no status ends the run `OUTCOME_LOST`, never exit 0), case 27 (a failure after the command was handed to the target is a result `OUTCOME_FAILED` with the partial output and its completeness, while a refusal before it is a status) and the pairs P1 (`Run` equals `Open` without a terminal, collected to the end) and P3 (the engine over gRPC equals the engine in process). The cases write what a result must say from what their script wrote (the bytes of each stream and where the cap cuts them, the exit and the survival your declaration implies), never from what the plugin answered. A target that declares `StallLossy` skips the check of the default cap, which a lossy queue may cut before the capture sees it.

## Names: one word per thing

| Thing | Wire (`omniview.sdk.exec.v2`) | Go (`pkg/v1/exec`) |
|---|---|---|
| a kind of thing commands run in | `TargetInfo` | `Target` (registration), `TargetInfo` |
| its routing key | `provider`, `type` | `Info.Provider`, `Info.Type` (one pair) |
| the one function | (none) | `ExecFunc`, `Proc`, `Request` |
| the command is running | frame `Started`, `RunStarted` | `Proc.Started(SessionFacts)` |
| facts the author states then | `SessionFacts` | `SessionFacts` (author subset) |
| the mode of a session | `Choice.capabilities` | `Choice.Capabilities` |
| how a command ended | `End`, `Ended`, `Exited`, `Signaled`, `Closed`, `Failed`, `Lost` | `Exit` (`ExitCode`/`ExitSignal`/`ExitFolded`/`ExitTransport`/`ExitLost`) |
| why a refusal or failure | `ErrorClass`, `ErrorReason`, `ErrorFacts` | `ErrorClass`, `ErrorReason`, `ErrorFacts`, `Error` |
| sessions that outlive a stream | `FEATURE_SESSION_MANAGEMENT` | (engine step) |
| a stream left, session lives | `Detached{reason}` | (engine step) |

## Compatibility

The v2 lane is a new gRPC service on the existing `exec` capability: additive, no protocol bump. A plugin built before it answers `Unimplemented`, the client says `ErrorClassUnsupported`, and the host stays on the legacy lane; a v0.21.0 host talking to a plugin that registers both lanes sees the legacy lane byte for byte as before.

**How the contract is pinned.** Nothing of it is released yet, so its names and shapes can still change; once a release carries it, it only grows. Two checks hold that line. The reviewed listing of the contract, [`proto/exec/v2/exec.contract.golden`](../proto/exec/v2/exec.contract.golden), is compared by a test with the compiled descriptor of `exec.proto`, and its first line, `contract_version N`, must equal `exec.ContractVersion`. While the contract is unreleased you regenerate the listing after an intended change with `go test ./proto/exec/v2 -run TestContractMatchesItsReviewedGolden -update` and review the diff; once it is released the same failure means a breaking change. The CI job `proto-breaking` runs `buf breaking` against the latest `v*` tag (and does nothing while there is no tag). The Go package exports only what an author or the live `Describe`/`Options` path uses, and each engine step adds the host types it needs.

**Before any tag.** The conditions that the independent review of the head `ed6f26f8` of this contract sets before any tag that ships `exec.v2` are kept, verbatim, in [Before any tag that ships `exec.v2`](exec-before-any-tag.md).

## What is not served yet

- `ListSessions`, `ReadOutput` and `EndSession` exist in the service and answer a typed `UNSUPPORTED` refusal; `Describe` lists no feature for them (`FEATURE_SESSION_MANAGEMENT`, ...), so a host that asks first never calls them. Each step lists its feature when it lands.
- `Run` and `RunStream` serve a bounded run (`FEATURE_RUN`, `FEATURE_RUN_STREAM`, with `FEATURE_ENV` and `FEATURE_CWD`): see [Bounded runs](#bounded-runs-run-and-runstream). A run never has a terminal, `RunProgress` is at the SDK's pace and not a timer a host can rely on, and a `RunStream` cannot keep a head and a tail (it delivers each stream's beginning up to its cap).
- `Open` serves a new session (`FEATURE_SESSION`, with `FEATURE_ENV` and `FEATURE_CWD`): `Opened`, `Notice`, `Started`, `Output`, `Dropped`, `Link` and `Ended`, and from the host input, resize and close. An attach is refused `UNSUPPORTED`. Not served yet, and ignored and counted when a host sends them: `close_input` (`FEATURE_STDIN_EOF`), `signal`, `query` and `detach`; and not sent: the `Resized` acknowledgement, the replay ring, the lease, the tombstone, marks, modes and integration. A session lives exactly as long as its stream.
- The plugin does not set the gRPC keepalive policy for a host that pings, nor a host's client parameters: a dead peer is found by the transport's own defaults until that step lands.
- `exectest.Conformance` checks registration, `Describe` and `Options`, and runs the engine cases that this build serves, in process and over the wire; the others stay skipped with the step that fills each.
- Not yet written: `Detach` and the `DETACH_TIMEOUT` clock (the cause exists, nothing produces it), `IDLE_TIMEOUT`, and an abandon rule for a connection that a function holds past its end.
