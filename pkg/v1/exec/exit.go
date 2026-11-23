package exec

import (
	"fmt"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// exitInt is the integer types an exit code may be given in: every signed type and every unsigned type that fits an
// int64. It excludes uint and uint64, whose large values would not fit, so that a code is never narrowed or wrapped
// on its way to the wire.
type exitInt interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32
}

// Exit is how a command ended, as the author reports it. Build it with ExitCode, ExitSignal, ExitFolded,
// ExitTransport or ExitLost, which set the source for you.
//
// Exactly one of three shapes is valid: a Code (the command exited), a Signal or SignalNumber (the command was
// killed by a signal, only for a target whose Capabilities.ExitReport is ExitReportCodeAndSignal), or Lost (the
// process's fate is unknown). An empty Exit is not an exit 0: the SDK reports it as a failure of the target with
// ErrorClassInternal. An Exit that mixes shapes is reported the same way.
//
// A signal needs a target that declares ExitReportCodeAndSignal. A signal returned by a target that declares less is
// never folded into an exit code: whatever its source, name and number, the SDK reports it as a failure of the target
// with ErrorClassInternal and leaves the process's fate unknown. A target that folds a death by signal itself reports the
// code, with ExitFolded. For a target that declares signals, a signal name or number the wire cannot carry is dropped,
// and the failure is reported when none is left.
type Exit struct {
	// Code is the status the target reported, exactly as reported: a POSIX status 0 to 255, a Windows DWORD
	// (0xC0000005 is 3221225477, or -1073741819 as some reporters give it) or an ssh exit-status. A nil Code means
	// the target reported none.
	Code *int64
	// Signal is the signal the target reported ("SIGKILL"): valid UTF-8 without control characters, at most 256 bytes.
	// A name that is not is dropped, and the number says which signal it was.
	Signal string
	// SignalNumber is the number of that signal, 1 to 64; zero means the target reported none. A number outside that
	// range (a negative one, one past 64, one an int32 cannot hold) is dropped, and the name says which signal it was.
	SignalNumber int
	// Source says whose status this is. SourceUnspecified, beside a Code or a Signal, is read as SourceProcess.
	// SourceHost is the SDK's own, for a bounded run it ended; an author does not set it.
	Source ExitSource
	// Lost is non-zero when the stream to the target ended with no terminal status: the process may be running,
	// may have exited with any code, may have been killed. It is never an exit 0. Code and Signal must be unset.
	Lost LostCause
}

// ExitCode reports the process's own exit status, as the target relayed it (SourceProcess). The code is carried
// exactly: ExitCode(uint32(0xC0000005)) and ExitCode(-1073741819) are two readings of one Windows status and each
// reaches the wire as it was given.
func ExitCode[N exitInt](code N) Exit {
	return Exit{Code: new(int64(code)), Source: SourceProcess}
}

// ExitSignal reports that the target said the process was killed by a signal (SourceProcess): its name ("SIGKILL") and
// its number (9), either of which may be left out (an empty name, number 0) when the target did not report it, within
// the bounds Exit gives. Only for a target whose Capabilities.ExitReport is ExitReportCodeAndSignal; what becomes of a
// signal from a target that declares none is written at Exit.
func ExitSignal(name string, number int) Exit {
	return Exit{Signal: name, SignalNumber: number, Source: SourceProcess}
}

// ExitFolded reports a code the target folded from a death by signal, 128+n (SourceSignalFolded): the code is
// reported as it is and the SDK claims no signal, because a plain exit with the same code cannot be told apart.
func ExitFolded[N exitInt](code N) Exit {
	return Exit{Code: new(int64(code)), Source: SourceSignalFolded}
}

// ExitTransport reports that the transport said the stream ended and the process's own status was not relayed
// (SourceTransport): the code, if it is not what the command exited with, says nothing about the command.
func ExitTransport[N exitInt](code N) Exit {
	return Exit{Code: new(int64(code)), Source: SourceTransport}
}

// ExitLost reports that the process's fate is unknown: the stream to the target ended with no terminal status. It
// is never exit 0.
func ExitLost(cause LostCause) Exit { return Exit{Lost: cause} }

// exitViolation describes a way an author broke the contract of Exit, for the SDK's local log.
type exitViolation struct{ what string }

func (v *exitViolation) Error() string { return "exec: invalid Exit: " + v.what }

// and returns v with one more way the author broke the contract; a nil v is no violation yet.
func (v *exitViolation) and(what string) *exitViolation {
	if v == nil {
		return &exitViolation{what}
	}
	return &exitViolation{v.what + "; " + what}
}

// end converts what the author returned into the wire End the SDK reports, for a target with the given capabilities.
// A returned violation is the contract the author broke; end has already chosen a safe End for it (a failure of the
// target for a broken shape, a downgraded source for a status the target cannot produce), so a caller only logs it.
//
// End.remote_may_survive says whether the process may still be running, and is a tri-state:
//
//	Lost                                        present, true   (the stream ended with no status: always may)
//	Exited or Signaled, source PROCESS          present, false  (the target relayed the process's own status)
//	Exited, source SIGNAL_FOLDED                present, false  (a death by signal the target folded into 128+n)
//	Exited or Signaled, source TRANSPORT        absent          (only the transport said the stream ended: not known)
//	Failed (a broken shape of Exit)             absent          (the SDK does not know)
//
// SourceHost is never an author's: it is read as the transport's word, so it leaves the field absent.
//
// A signal needs a target that declares ExitReportCodeAndSignal. From a target that declares less it is never turned
// into an exit: the SDK does not fold it into 128+n (that code is the author's to report, with ExitFolded), and
// whatever its source, name and number the end is a failure of the target with the survival absent. For a target that
// declares signals, a signal name or number the wire cannot carry (a name that is not UTF-8 or has a control character
// or is over 256 bytes, a number outside 1 to 64) is dropped, never cut or wrapped, and when neither is left the end is
// a failure of the target.
func (e Exit) end(caps Capabilities) (*execv2.End, *exitViolation) {
	hasSignal := e.Signal != "" || e.SignalNumber != 0
	switch {
	case e.Lost != LostUnspecified:
		if e.Code != nil || hasSignal {
			return failedInternal(), &exitViolation{"Lost with a code or a signal"}
		}
		return &execv2.End{
			Kind: &execv2.End_Lost{Lost: &execv2.Lost{Cause: e.Lost.toProto()}},
			// A stream that ended with no status: the process may be running, always.
			RemoteMaySurvive: new(true),
		}, nil
	case e.Code != nil && hasSignal:
		return failedInternal(), &exitViolation{"both a code and a signal"}
	case hasSignal:
		return e.signaledEnd(caps)
	case e.Code != nil:
		return e.exitedEnd(caps)
	default:
		return failedInternal(), &exitViolation{"empty Exit: neither a code, a signal nor Lost"}
	}
}

// maxSignalNumber is the largest signal number the SDK takes from an author: 64, the last real-time signal of Linux and
// the highest any platform defines. A number outside 1 to maxSignalNumber is no signal number: zero is "not reported", a
// negative or a larger number is not a signal, and every number in the range fits the wire's int32.
const maxSignalNumber = 64

// validSignalNumber reports whether n is a signal number: 1 to maxSignalNumber.
func validSignalNumber(n int) bool { return n >= 1 && n <= maxSignalNumber }

func (e Exit) signaledEnd(caps Capabilities) (*execv2.End, *exitViolation) {
	if caps.ExitReport != ExitReportCodeAndSignal {
		// The target declared that it cannot report a signal: the contract says a consumer must not expect one, so
		// whatever it returned, from whatever source, is not a status the SDK can stand behind. Folding it into 128+n
		// would make an exit code, and a process known gone, out of a signal that the target cannot have reported: that
		// code is the author's to report, with ExitFolded. The end is a failure of the target, and the fate is unknown.
		return failedInternal(), &exitViolation{"a signal from a target that does not report signals"}
	}
	source, violation := e.authorSource()
	if source == SourceSignalFolded {
		// SIGNAL_FOLDED says the target reported a folded code and the SDK claims no signal: an explicit signal with it
		// contradicts itself, and only an Exited (ExitFolded) carries that source. The signal is kept, as the transport's
		// word: the survival stays absent, never "known gone" on a status that contradicts itself.
		source = SourceTransport
		violation = violation.and("a signal with SourceSignalFolded: a folded code is no signal, read as the transport's word")
	}
	number := 0
	if validSignalNumber(e.SignalNumber) {
		number = e.SignalNumber
	}
	// The name crosses as a bounded fact does: valid text within its bound, or not at all. The number is never cast
	// before it is known to be in range.
	name := boundedFact(e.Signal)
	if e.Signal != "" && name == "" {
		violation = violation.and(fmt.Sprintf("the signal name is not text the wire carries (UTF-8 without control characters, at most %d bytes): dropped", maxFactBytes))
	}
	if e.SignalNumber != 0 && number == 0 {
		violation = violation.and(fmt.Sprintf("signal number %d is not a signal number (1 to %d): dropped", e.SignalNumber, maxSignalNumber))
	}
	if name == "" && number == 0 {
		return failedInternal(), violation.and("no usable signal name or number")
	}
	return &execv2.End{
		Kind: &execv2.End_Signaled{Signaled: &execv2.Signaled{
			Signal:       name,
			SignalNumber: int32(number), //nolint:gosec // 0, or 1 to maxSignalNumber
			Source:       source.toProto(),
		}},
		RemoteMaySurvive: survivalOf(source),
	}, violation
}

func (e Exit) exitedEnd(caps Capabilities) (*execv2.End, *exitViolation) {
	source, violation := e.authorSource()
	if caps.ExitReport == ExitReportNone && source != SourceTransport {
		// The target declared that it reports no status: a code can only be the transport's word. It stays an Exited, with
		// the source downgraded and a violation, by design: EXIT_SOURCE_TRANSPORT is defined for exactly the transport that
		// relays a code of its own and never claims it is the command's, so the code is carried as that and the survival
		// stays absent. A claim of a process status from this target is the author's contract break, logged here.
		source = SourceTransport
		if violation == nil {
			violation = &exitViolation{"a process status from a target that reports none"}
		}
	}
	return &execv2.End{
		Kind:             &execv2.End_Exited{Exited: &execv2.Exited{ExitCode: *e.Code, Source: source.toProto()}},
		RemoteMaySurvive: survivalOf(source),
	}, violation
}

// survivalOf is the remote_may_survive of an exit or a signal whose status has the given source. The wire field is
// a tri-state: absent is "not known" (a consumer reads it as "may"), true is "may" and false is "known gone". When
// the target relayed the process's own status, or a status folded from a death by signal, the process is gone: false.
// When only the transport said the stream ended, the code is not the command's and the SDK does not know: absent.
func survivalOf(source ExitSource) *bool {
	switch source {
	case SourceProcess, SourceSignalFolded:
		return new(false)
	case SourceUnspecified, SourceTransport, SourceHost:
	}
	return nil
}

// authorSource reads the source an author gave: unspecified is the process's own status; the SDK's own source is
// not an author's to claim and reads as the transport's word, the least a consumer may trust.
func (e Exit) authorSource() (ExitSource, *exitViolation) {
	switch e.Source {
	case SourceUnspecified:
		return SourceProcess, nil
	case SourceProcess, SourceSignalFolded, SourceTransport:
		return e.Source, nil
	case SourceHost:
		return SourceTransport, &exitViolation{"SourceHost is the SDK's, not an author's"}
	}
	return SourceTransport, &exitViolation{fmt.Sprintf("unknown ExitSource %d", int(e.Source))}
}

// failedInternal is the End for a command the SDK cannot account for: a failure of the target, never an exit.
func failedInternal() *execv2.End {
	return &execv2.End{Kind: &execv2.End_Failed{Failed: &execv2.Failed{
		Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL,
		Stage: execv2.FailureStage_FAILURE_STAGE_RUN,
	}}}
}
