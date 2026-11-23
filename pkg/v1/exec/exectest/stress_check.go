package exectest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The checks of the stress harness are plain functions of what a reader saw and what the engine reported, so that each one
// can be tested on its own, red on a synthetic violation and green on the same input without it.

// maxFindings bounds the violations a round lists: a rule that is broken is broken for every command, and the first ones say
// what is wrong.
const maxFindings = 40

// findings collects the violations a round found, from any goroutine, up to a bound.
type findings struct {
	mu    sync.Mutex
	found []string
	total int
}

// add records a violation. The text names the command it is about.
func (f *findings) add(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.total++
	if len(f.found) < maxFindings {
		f.found = append(f.found, fmt.Sprintf(format, args...))
	}
}

// list returns the violations in the order they were found, and a last line that says how many were left out.
func (f *findings) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	listed := slices.Clone(f.found)
	if f.total > len(listed) {
		listed = append(listed, fmt.Sprintf("... and %d more violations not listed", f.total-len(listed)))
	}
	return listed
}

// knownCauses are the words with which the engine says why a command ended (exechook.Result.Cause).
var knownCauses = []string{"finished", "requested", "shutdown", "retired", "expired", "timeout", "cancelled"}

// closedByCause is the ClosedBy of the end that the SDK makes for each cause but "finished". Whatever the function returned
// afterwards, a command that the SDK ended is Closed, by the one that asked.
var closedByCause = map[string]execv2.ClosedBy{
	"requested": execv2.ClosedBy_CLOSED_BY_REQUEST,
	"shutdown":  execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN,
	"retired":   execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED,
	"expired":   execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT,
	"timeout":   execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT,
	"cancelled": execv2.ClosedBy_CLOSED_BY_CANCELLED,
}

// endArm names the arm of an End: "exited", "signaled", "closed", "failed" or "lost". ok is false when no arm is set: a nil
// End, an End with no kind, or an arm that holds nothing.
func endArm(end *execv2.End) (arm string, ok bool) {
	switch kind := end.GetKind().(type) {
	case *execv2.End_Exited:
		return "exited", kind.Exited != nil
	case *execv2.End_Signaled:
		return "signaled", kind.Signaled != nil
	case *execv2.End_Closed:
		return "closed", kind.Closed != nil
	case *execv2.End_Failed:
		return "failed", kind.Failed != nil
	case *execv2.End_Lost:
		return "lost", kind.Lost != nil
	}
	return "", false
}

// outputAudit follows what the one reader of a command has read of its output, to find what a reader that does not trust the
// engine would find: a byte that was not delivered and not reported dropped, a byte delivered twice, a byte that is not what
// the target wrote at its offset. Its position is the offset of the next byte it expects: everything before it was either read
// or reported lost.
type outputAudit struct {
	tty       bool // the command has a terminal, so its one stream is the terminal's
	patterned bool // every byte of the output is patternByte of its offset
	next      uint64
	read      uint64
	dropped   uint64
}

// take checks one Read of the command: the ranges it reports lost, which must start where the reader is, and then the bytes it
// returned (data is exactly those), which must start where the lost ranges end. eof is true when the Read returned io.EOF.
// After a violation the audit goes on from what the engine says, so that one hole is one violation and not a flood.
func (a *outputAudit) take(chunk exechook.Chunk, data []byte, eof bool) []string {
	var found []string
	for _, d := range chunk.Dropped {
		if d.From != a.next {
			found = append(found, fmt.Sprintf("a dropped range starts at %d, the reader is at %d: bytes were lost without being reported, or reported twice", d.From, a.next))
		}
		if d.Bytes == 0 {
			found = append(found, fmt.Sprintf("an empty dropped range at offset %d", d.From))
		}
		a.next = d.From + d.Bytes
		a.dropped += d.Bytes
	}
	switch {
	case chunk.N > 0:
		if chunk.Offset != a.next {
			found = append(found, fmt.Sprintf("a chunk at offset %d, the reader is at %d: a hole that was not reported as dropped, or bytes read twice", chunk.Offset, a.next))
		}
		if !streamFits(a.tty, chunk.Stream) {
			found = append(found, fmt.Sprintf("a chunk of the stream %s from a command %s", chunk.Stream, streamRule(a.tty)))
		}
		if a.patterned {
			for i := range min(chunk.N, len(data)) {
				if want := patternByte(chunk.Offset + uint64(i)); data[i] != want {
					found = append(found, fmt.Sprintf("the byte at offset %d is %d, not what was written there (%d)", chunk.Offset+uint64(i), data[i], want))
					break
				}
			}
		}
		a.next = chunk.Offset + uint64(chunk.N)
		a.read += uint64(chunk.N)
	case eof:
		if chunk.Offset != a.next {
			found = append(found, fmt.Sprintf("the end of the output is at offset %d, the reader is at %d", chunk.Offset, a.next))
		}
	case len(chunk.Dropped) > 0:
		if chunk.Offset != a.next {
			found = append(found, fmt.Sprintf("after its dropped ranges a read is at offset %d, the reader is at %d", chunk.Offset, a.next))
		}
	default:
		found = append(found, "a Read returned neither bytes nor a dropped range and was not the end of the output")
	}
	return found
}

// streamFits reports whether a command can have written to the stream: a terminal has one, and any other command has
// standard output and standard error.
func streamFits(tty bool, stream execv2.Stream) bool {
	if tty {
		return stream == execv2.Stream_STREAM_TTY
	}
	return stream == execv2.Stream_STREAM_STDOUT || stream == execv2.Stream_STREAM_STDERR
}

func streamRule(tty bool) string {
	if tty {
		return "with a terminal, whose one stream is the terminal's"
	}
	return "without a terminal, whose streams are standard output and standard error"
}

// checkResult holds the end of a command, as it is at rest, to the invariants that do not depend on what the command was:
// it has exactly one of the five arms, a cause that is one of the engine's words, and an arm that agrees with that cause; a
// COMPLETE output has no dropped byte and was read in full; and what the reader read plus what it was told was lost is
// exactly what the command wrote. a is the audit of the reader that read the output to its end.
func checkResult(res exechook.Result, a outputAudit) []string {
	var found []string
	arm, armOK := endArm(res.End)
	if !armOK {
		found = append(found, "the end has no arm: exactly one of Exited, Signaled, Closed, Failed and Lost must be set")
	}
	switch {
	case !slices.Contains(knownCauses, res.Cause):
		found = append(found, fmt.Sprintf("cause %q is not one of the engine's words (%s)", res.Cause, strings.Join(knownCauses, ", ")))
	case !armOK:
	case res.Cause == "finished" && arm == "closed":
		found = append(found, "the command finished by itself and its end is closed: only an end that the SDK made is Closed")
	case res.Cause != "finished" && arm != "closed":
		found = append(found, fmt.Sprintf("the command was ended with cause %q and its end is %s: an end that the SDK made is Closed, whatever the function returned", res.Cause, arm))
	case res.Cause != "finished":
		if got, want := res.End.GetClosed().GetBy(), closedByCause[res.Cause]; got != want {
			found = append(found, fmt.Sprintf("the command was ended with cause %q and is Closed by %s, want %s", res.Cause, got, want))
		}
	}
	if _, known := execv2.OutputCompleteness_name[int32(res.Output)]; !known {
		found = append(found, fmt.Sprintf("the output completeness %d is not a value of the contract", int32(res.Output)))
	}
	if res.Output == execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE {
		if res.DroppedBytes != 0 {
			found = append(found, fmt.Sprintf("the output is COMPLETE and %d bytes were dropped", res.DroppedBytes))
		}
		if a.read != res.BytesOut {
			found = append(found, fmt.Sprintf("the output is COMPLETE and the reader read %d of %d bytes", a.read, res.BytesOut))
		}
	}
	if a.read+a.dropped != res.BytesOut {
		found = append(found, fmt.Sprintf("the reader read %d and dropped %d, the command wrote %d: read plus dropped must be what was written, no byte twice and none missing", a.read, a.dropped, res.BytesOut))
	}
	if a.dropped != res.DroppedBytes {
		found = append(found, fmt.Sprintf("the reader was told %d bytes were lost and the result says %d", a.dropped, res.DroppedBytes))
	}
	return found
}

// sameResult says how the result of an ended command differs from the first one that was seen, or returns "" when it does
// not: the end of a command is final, and a result that changes after it is a second end.
func sameResult(first, now exechook.Result) string {
	switch {
	case !proto.Equal(first.End, now.End):
		return fmt.Sprintf("the end changed from %v to %v", first.End, now.End)
	case first.Cause != now.Cause:
		return fmt.Sprintf("the cause changed from %q to %q", first.Cause, now.Cause)
	case first.StartObserved != now.StartObserved:
		return fmt.Sprintf("start observed changed from %v to %v", first.StartObserved, now.StartObserved)
	case first.BytesOut != now.BytesOut:
		return fmt.Sprintf("bytes out changed from %d to %d", first.BytesOut, now.BytesOut)
	case first.BytesIn != now.BytesIn:
		return fmt.Sprintf("bytes in changed from %d to %d", first.BytesIn, now.BytesIn)
	case first.DroppedBytes != now.DroppedBytes:
		return fmt.Sprintf("dropped bytes changed from %d to %d", first.DroppedBytes, now.DroppedBytes)
	case first.LateWrites != now.LateWrites || first.LateBytes != now.LateBytes:
		return fmt.Sprintf("late writes changed from %d (%d bytes) to %d (%d bytes)", first.LateWrites, first.LateBytes, now.LateWrites, now.LateBytes)
	case first.Output != now.Output:
		return fmt.Sprintf("output completeness changed from %v to %v", first.Output, now.Output)
	case first.Abandoned != now.Abandoned:
		return fmt.Sprintf("abandoned changed from %v to %v", first.Abandoned, now.Abandoned)
	}
	return ""
}

// refusalName names a refusal of a start by its class and, when it has one, its reason ("LIMIT/TOO_MANY_SESSIONS"), the
// resource lane's refusals by their code, and any other error as "unknown".
func refusalName(err error) string {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		name := execErr.Class.String()
		if execErr.Reason != exec.ReasonUnspecified {
			name += "/" + execErr.Reason.String()
		}
		return name
	}
	var resourceErr *resourcev2.ResourceOperationError
	if errors.As(err, &resourceErr) {
		return "resource:" + resourceErr.Code
	}
	return "unknown"
}

// refusalClass returns the class of a refusal of a start, and false when it is not one of the exec lane's.
func refusalClass(err error) (exec.ErrorClass, bool) {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return execErr.Class, true
	}
	return exec.ClassUnspecified, false
}

// checkRefusedAfterClose holds the result of a start made after Close returned to what the engine promises: it is refused,
// as UNAVAILABLE.
func checkRefusedAfterClose(err error) string {
	if err == nil {
		return "a command was admitted after Close returned: a closed engine must refuse every start"
	}
	if class, ok := refusalClass(err); !ok || class != exec.ClassUnavailable {
		return fmt.Sprintf("a start after Close returned was refused as %s, want UNAVAILABLE", refusalName(err))
	}
	return ""
}
