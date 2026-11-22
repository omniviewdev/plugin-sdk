package exec

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

func TestExitConstructorsSetTheirSource(t *testing.T) {
	tests := []struct {
		name string
		exit Exit
		want Exit
	}{
		{"code", ExitCode(3), Exit{Code: new(int64(3)), Source: SourceProcess}},
		{"signal", ExitSignal("SIGKILL", 9), Exit{Signal: "SIGKILL", SignalNumber: 9, Source: SourceProcess}},
		{"folded", ExitFolded(137), Exit{Code: new(int64(137)), Source: SourceSignalFolded}},
		{"transport", ExitTransport(0), Exit{Code: new(int64(0)), Source: SourceTransport}},
		{"lost", ExitLost(LostLinkDead), Exit{Lost: LostLinkDead}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, want := tc.exit, tc.want
			if got.Signal != want.Signal || got.SignalNumber != want.SignalNumber || got.Source != want.Source ||
				got.Lost != want.Lost || (got.Code == nil) != (want.Code == nil) || (got.Code != nil && *got.Code != *want.Code) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

// exitStatus is a named integer type: an exit code is given in whatever integer type the target's own client returns.
type exitStatus int32

// An exit code is carried exactly. A Windows process status is a DWORD: 0xC0000005 is 3221225477, which one reporter
// gives as that uint32 and another (kubelet) as the negative int32 -1073741819. The SDK never narrows, folds or
// saturates either, and never converts one into the other: each reaches the wire as it was given.
func TestExitCodeCarriesTheStatusExactly(t *testing.T) {
	tests := []struct {
		name string
		exit Exit
		want int64
	}{
		{"a Windows DWORD as the uint32 the reporter gave", ExitCode(uint32(0xC0000005)), 3221225477},
		{"the same status as the negative int32 kubelet reports", ExitCode(int32(-1073741819)), -1073741819},
		{"the same status as a plain int", ExitCode(-1073741819), -1073741819},
		{"a POSIX status", ExitCode(255), 255},
		{"a status of a byte", ExitCode(uint8(255)), 255},
		{"a status of a uint16", ExitCode(uint16(65535)), 65535},
		{"a small negative status", ExitCode(int8(-1)), -1},
		{"the largest int64", ExitCode(int64(math.MaxInt64)), math.MaxInt64},
		{"the smallest int64", ExitCode(int64(math.MinInt64)), math.MinInt64},
		{"the largest uint32", ExitCode(uint32(math.MaxUint32)), math.MaxUint32},
		{"a named integer type", ExitCode(exitStatus(-2)), -2},
		{"a folded code", ExitFolded(uint32(0xC0000005)), 3221225477},
		{"the transport's code", ExitTransport(uint32(0xC0000005)), 3221225477},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.exit.Code == nil {
				t.Fatalf("Exit.Code is nil, want %d", tc.want)
			}
			if *tc.exit.Code != tc.want {
				t.Fatalf("Exit.Code = %d, want %d", *tc.exit.Code, tc.want)
			}
			end, _ := tc.exit.end(Capabilities{ExitReport: ExitReportCodeAndSignal})
			if got := end.GetExited().GetExitCode(); got != tc.want {
				t.Errorf("the end carries code %d, want %d: %v", got, tc.want, end)
			}
		})
	}
}

// The wire states of remote_may_survive of an End: absent (not known), true (may) and false (known gone).
var (
	mayRun  = new(true)
	goneNow = new(false)
)

func wireExited(code int64, source execv2.ExitSource, survives *bool) *execv2.End {
	return &execv2.End{
		Kind:             &execv2.End_Exited{Exited: &execv2.Exited{ExitCode: code, Source: source}},
		RemoteMaySurvive: survives,
	}
}

func wireSignaled(name string, number int32, source execv2.ExitSource, survives *bool) *execv2.End {
	return &execv2.End{
		Kind:             &execv2.End_Signaled{Signaled: &execv2.Signaled{Signal: name, SignalNumber: number, Source: source}},
		RemoteMaySurvive: survives,
	}
}

func wireLost(cause execv2.LostCause, survives *bool) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Lost{Lost: &execv2.Lost{Cause: cause}}, RemoteMaySurvive: survives}
}

// exitEndCase is one row of TestExitEnd: what an author returned, the capabilities of its target, the End the SDK
// builds and the violation it records.
type exitEndCase struct {
	name      string
	exit      Exit
	caps      Capabilities
	want      *execv2.End
	violation string // substring; empty means none
}

// beyondInt32 is a signal number that does not fit the wire's int32: 1<<32 + 9, which a cast to int32 would carry as 9
// (SIGKILL). An int of 32 bits cannot hold it, so a build with one has no such number and the cases that name it run
// only where IntSize is 64.
func beyondInt32() int {
	wide := int64(1) << 32
	return int(wide + 9)
}

func TestExitEnd(t *testing.T) {
	both := Capabilities{ExitReport: ExitReportCodeAndSignal}
	codeOnly := Capabilities{ExitReport: ExitReportCode}
	none := Capabilities{ExitReport: ExitReportNone}
	const (
		process = execv2.ExitSource_EXIT_SOURCE_PROCESS
		folded  = execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED
		carrier = execv2.ExitSource_EXIT_SOURCE_TRANSPORT
	)
	notUTF8 := string([]byte{0xff})
	longName := strings.Repeat("S", maxFactBytes)
	tests := []exitEndCase{
		{"a process code", ExitCode(0), both, wireExited(0, process, goneNow), ""},
		{"a non-zero code", ExitCode(3), codeOnly, wireExited(3, process, goneNow), ""},
		{"a code with no source is the process's", Exit{Code: new(int64(7))}, codeOnly, wireExited(7, process, goneNow), ""},
		{"a folded code", ExitFolded(137), codeOnly, wireExited(137, folded, goneNow), ""},
		{"the transport's word leaves the fate unknown", ExitTransport(0), codeOnly, wireExited(0, carrier, nil), ""},
		{"a signal from a target that reports them", ExitSignal("SIGKILL", 9), both, wireSignaled("SIGKILL", 9, process, goneNow), ""},
		{"a signal whose source is the transport's leaves the fate unknown",
			Exit{Signal: "SIGKILL", SignalNumber: 9, Source: SourceTransport}, both, wireSignaled("SIGKILL", 9, carrier, nil), ""},
		{"an explicit signal reported as a folded code contradicts itself: read as the transport's word",
			Exit{Signal: "SIGKILL", SignalNumber: 9, Source: SourceSignalFolded}, both, wireSignaled("SIGKILL", 9, carrier, nil), "SourceSignalFolded"},
		{"a signal with a name and no number is a signal", Exit{Signal: "SIGKILL"}, both, wireSignaled("SIGKILL", 0, process, goneNow), ""},
		{"a signal with a number and no name is a signal", Exit{SignalNumber: 9}, both, wireSignaled("", 9, process, goneNow), ""},
		{"the last signal number the SDK accepts", ExitSignal("SIGRTMAX", 64), both, wireSignaled("SIGRTMAX", 64, process, goneNow), ""},
		{"a name of the longest length is carried", ExitSignal(longName, 9), both, wireSignaled(longName, 9, process, goneNow), ""},

		// What the wire cannot carry, or the SDK does not accept, is dropped when the other identifier still says which
		// signal it was, and is a failure of the target when nothing does: protobuf refuses to marshal a name that is not
		// UTF-8, and a number the wire cannot hold is never cut to fit.
		{"a name that is not UTF-8 is dropped", ExitSignal(notUTF8, 9), both, wireSignaled("", 9, process, goneNow), "signal name"},
		{"a name with a control character is dropped", ExitSignal("SIG\x00KILL", 9), both, wireSignaled("", 9, process, goneNow), "signal name"},
		{"a name past its bound is dropped, never cut", ExitSignal(longName+"S", 9), both, wireSignaled("", 9, process, goneNow), "signal name"},
		{"a signal number below 1 is dropped", ExitSignal("SIGKILL", -9), both, wireSignaled("SIGKILL", 0, process, goneNow), "signal number"},
		{"a signal number past 64 is dropped", ExitSignal("SIGKILL", 65), both, wireSignaled("SIGKILL", 0, process, goneNow), "signal number"},
		{"a name that cannot cross and no number is a failure of the target", Exit{Signal: notUTF8}, both, failedInternal(), "signal name"},
		{"a number that is no signal number and no name is a failure of the target", Exit{SignalNumber: 65}, both, failedInternal(), "signal number"},
		{"neither identifier usable is a failure of the target", ExitSignal(notUTF8, -1), both, failedInternal(), "signal name"},
		{"a windows status is carried exactly", ExitCode(uint32(0xC0000005)), codeOnly, wireExited(3221225477, process, goneNow), ""},
		{"lost: no status", ExitLost(LostNoStatus), codeOnly, wireLost(execv2.LostCause_LOST_CAUSE_NO_STATUS, mayRun), ""},
		{"lost: the link is dead", ExitLost(LostLinkDead), both, wireLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD, mayRun), ""},
		{"lost ignores the source", Exit{Lost: LostNoStatus, Source: SourceProcess}, codeOnly, wireLost(execv2.LostCause_LOST_CAUSE_NO_STATUS, mayRun), ""},

		{"an empty Exit is a failure of the target, never exit 0", Exit{}, both, failedInternal(), "empty Exit"},
		{"a code and Lost", Exit{Code: new(int64(0)), Lost: LostNoStatus}, both, failedInternal(), "Lost with a code"},
		{"a signal and Lost", Exit{Signal: "SIGKILL", Lost: LostNoStatus}, both, failedInternal(), "Lost with a code or a signal"},
		{"a code and a signal", Exit{Code: new(int64(0)), Signal: "SIGTERM", SignalNumber: 15}, both, failedInternal(), "both a code and a signal"},

		// A target that declares no signals cannot be the source of one, and no source makes a signal from it the
		// process's own status: whatever its source, name and number, a signal it returns is a failure of the target (never
		// a signal arm, never an exit code, never a process known gone) and its survival stays unknown. The SDK does not
		// fold a signal into 128+n: that is a code only the author can report, with ExitFolded.
		{"a signal from a code-only target is a failure of the target", ExitSignal("SIGTERM", 15), codeOnly, failedInternal(), "a signal from a target that does not report signals"},
		{"a signal from a target that reports none is a failure of the target", ExitSignal("SIGKILL", 9), none, failedInternal(), "a signal from a target that does not report signals"},
		{"a signal with no source is not folded", Exit{Signal: "SIGTERM", SignalNumber: 15}, codeOnly, failedInternal(), "does not report signals"},
		{"a name alone is no more", Exit{Signal: "SIGTERM"}, codeOnly, failedInternal(), "does not report signals"},
		{"a number alone is no more", Exit{SignalNumber: 15}, codeOnly, failedInternal(), "does not report signals"},
		{"a name the wire cannot carry", ExitSignal(notUTF8, 15), codeOnly, failedInternal(), "does not report signals"},
		{"a number that is no signal number", Exit{Signal: "SIGWHAT", SignalNumber: 65}, codeOnly, failedInternal(), "does not report signals"},
		{"a negative signal number", Exit{Signal: "SIGWHAT", SignalNumber: -9}, codeOnly, failedInternal(), "does not report signals"},
		{"a signal from the transport", Exit{Signal: "SIGKILL", SignalNumber: 9, Source: SourceTransport}, codeOnly, failedInternal(), "does not report signals"},
		{"a signal from the transport of a target that reports none", Exit{Signal: "SIGKILL", SignalNumber: 9, Source: SourceTransport}, none, failedInternal(), "does not report signals"},
		{"a signal of the SDK's own source", Exit{Signal: "SIGKILL", SignalNumber: 9, Source: SourceHost}, codeOnly, failedInternal(), "does not report signals"},
		{"a signal of a source nobody knows", Exit{Signal: "SIGKILL", SignalNumber: 9, Source: ExitSource(99)}, codeOnly, failedInternal(), "does not report signals"},
		{"a signal reported as a status already folded", Exit{Signal: "SIGKILL", SignalNumber: 9, Source: SourceSignalFolded}, codeOnly, failedInternal(), "does not report signals"},

		// What the author folds is a code, and a code is carried as it is from every target.
		{"a folded code from a target that reports signals is carried as it is", ExitFolded(143), both, wireExited(143, folded, goneNow), ""},

		{"a process status from a target that reports none is the transport's", ExitCode(0), none, wireExited(0, carrier, nil), "reports none"},
		{"a folded code from a target that reports none is the transport's", ExitFolded(137), none, wireExited(137, carrier, nil), "reports none"},
		{"the transport's word from a target that reports none is fine", ExitTransport(0), none, wireExited(0, carrier, nil), ""},

		{"the SDK's own source is not an author's", Exit{Code: new(int64(1)), Source: SourceHost}, codeOnly, wireExited(1, carrier, nil), "SourceHost"},
		{"an unknown source reads as the transport's", Exit{Code: new(int64(1)), Source: ExitSource(99)}, codeOnly, wireExited(1, carrier, nil), "unknown ExitSource"},
	}
	if strconv.IntSize == 64 {
		beyond := beyondInt32()
		tests = append(tests,
			exitEndCase{"a signal number beyond int32 is dropped, not cut to its low bits", ExitSignal("SIGKILL", beyond), both, wireSignaled("SIGKILL", 0, process, goneNow), "signal number"},
			exitEndCase{"a signal number beyond int32 and no name is a failure of the target", ExitSignal("", beyond), both, failedInternal(), "signal number"},
			exitEndCase{"a signal number beyond int32 from a code-only target is a failure of the target", ExitSignal("SIGKILL", beyond), codeOnly, failedInternal(), "does not report signals"},
		)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, violation := tc.exit.end(tc.caps)
			if !proto.Equal(got, tc.want) {
				t.Errorf("end = %v, want %v", got, tc.want)
			}
			switch {
			case tc.violation == "" && violation != nil:
				t.Errorf("unexpected violation %q", violation)
			case tc.violation != "" && (violation == nil || !strings.Contains(violation.Error(), tc.violation)):
				t.Errorf("violation = %v, want one containing %q", violation, tc.violation)
			}
		})
	}
}

// exitShapes is every combination of what an author can return, with every capability that decides how it is read.
// Each case calls fn once.
func exitShapes(fn func(exit Exit, report ExitReport, end *execv2.End)) {
	codes := []*int64{nil, new(int64(0)), new(int64(2))}
	signals := []struct {
		name   string
		number int
	}{
		{"", 0}, {"SIGTERM", 15}, {"", 9},
		// What the wire cannot carry or the SDK does not accept: a name that is not UTF-8, numbers outside 1 to 64 and one
		// that does not fit int32.
		{string([]byte{0xff}), 9}, {string([]byte{0xff}), 0}, {"SIGKILL", -9}, {"SIGKILL", 65}, {"SIGKILL", beyondInt32()},
	}
	losts := []LostCause{LostUnspecified, LostNoStatus, LostLinkDead, LostCause(99)}
	sources := []ExitSource{SourceUnspecified, SourceProcess, SourceSignalFolded, SourceTransport, SourceHost, ExitSource(99)}
	reports := []ExitReport{ExitReportUnspecified, ExitReportNone, ExitReportCode, ExitReportCodeAndSignal}
	for _, code := range codes {
		for _, sig := range signals {
			for _, lost := range losts {
				for _, source := range sources {
					for _, report := range reports {
						exit := Exit{Code: code, Signal: sig.name, SignalNumber: sig.number, Source: source, Lost: lost}
						end, _ := exit.end(Capabilities{ExitReport: report})
						fn(exit, report, end)
					}
				}
			}
		}
	}
}

// TestNoExitShapeIsReportedAsExitZeroByMistake is the invariant the lost end exists for, over every combination of
// what an author can return: an end that is an exit with code 0 is one only when the author reported code 0 and
// nothing else, and every combination ends in exactly one arm.
func TestNoExitShapeIsReportedAsExitZeroByMistake(t *testing.T) {
	exitShapes(func(exit Exit, report ExitReport, end *execv2.End) {
		if end.GetKind() == nil {
			t.Fatalf("%+v with %v ended in no arm: %v", exit, report, end)
		}
		hasSignal := exit.Signal != "" || exit.SignalNumber != 0
		exactlyZero := exit.Code != nil && *exit.Code == 0 && !hasSignal && exit.Lost == LostUnspecified
		if end.GetExited() != nil && end.GetExited().GetExitCode() == 0 && !exactlyZero {
			t.Fatalf("%+v with %v was reported as an exit with code 0", exit, report)
		}
		if exit.Lost != LostUnspecified && end.GetExited() != nil {
			t.Fatalf("%+v with %v was lost and reported as an exit: %v", exit, report, end)
		}
		if exit.Code == nil && !hasSignal && exit.Lost == LostUnspecified && end.GetExited() != nil {
			t.Fatalf("an empty %+v with %v was reported as an exit: %v", exit, report, end)
		}
	})
}

// processStatus reports whether source, as an author gave it, says the status is the process's own: no source (read as
// the process's), the process's, or a death by signal the target folded. It is the only word the SDK takes for "the
// process is gone"; the transport's word, the SDK's own source and a source nobody knows are not.
func processStatus(source ExitSource) bool {
	return source == SourceUnspecified || source == SourceProcess || source == SourceSignalFolded
}

// signalShaped reports whether what the author returned is a signal and nothing else: a Signal or a SignalNumber, and
// no code and no Lost.
func signalShaped(exit Exit) bool {
	return (exit.Signal != "" || exit.SignalNumber != 0) && exit.Code == nil && exit.Lost == LostUnspecified
}

// remote_may_survive is a tri-state on the wire: absent is "not known" and reads as "may", true is "may", false is
// "known gone". Over every shape of Exit: a lost end always says it may (it is never absent and never false); an exit
// or a signal that the target's own status backs (the process's, or a death by signal the author reports as folded into
// 128+n) says the process is gone, and only the author's own word that the status was the process's can make it so:
// the transport's word, the SDK's own source and a source nobody knows never do; an exit from the transport's word,
// whose code is not the command's, leaves it absent; and an end that is a failure of the target leaves it absent,
// because the SDK does not know. A signal from a target that declares none is such a failure from every source: it is
// never turned into an exit with the process known gone.
func TestRemoteMaySurviveIsATriStateForEveryExitShape(t *testing.T) {
	var lost, gone, unknown int
	exitShapes(func(exit Exit, report ExitReport, end *execv2.End) {
		survives := end.RemoteMaySurvive
		if signalShaped(exit) && report != ExitReportCodeAndSignal {
			if _, failed := end.GetKind().(*execv2.End_Failed); !failed || survives != nil {
				t.Fatalf("%+v with %v: a signal from a target that declares none must be a failure of the target with the survival absent, got %v",
					exit, report, end)
			}
		}
		knownGone := func(what string, source execv2.ExitSource) {
			gone++
			if survives == nil || *survives {
				t.Fatalf("%+v with %v: %s with the status of %v must say the process is gone, got %v", exit, report, what, source, end)
			}
			if !processStatus(exit.Source) {
				t.Fatalf("%+v with %v: %s says the process is gone, but the author's source %v is not the process's own status: %v",
					exit, report, what, exit.Source, end)
			}
		}
		switch kind := end.GetKind().(type) {
		case *execv2.End_Lost:
			lost++
			if survives == nil || !*survives {
				t.Fatalf("%+v with %v: a lost end must say the process may survive, got %v", exit, report, end)
			}
		case *execv2.End_Exited:
			source := kind.Exited.GetSource()
			switch source {
			case execv2.ExitSource_EXIT_SOURCE_PROCESS, execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED:
				knownGone("an exit", source)
			default:
				unknown++
				if survives != nil {
					t.Fatalf("%+v with %v: an exit from %v leaves the fate unknown, got %v", exit, report, source, end)
				}
			}
		case *execv2.End_Signaled:
			source := kind.Signaled.GetSource()
			switch source {
			case execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED:
				t.Fatalf("%+v with %v: SIGNAL_FOLDED is a folded code and no signal; only an exit carries it: %v", exit, report, end)
			case execv2.ExitSource_EXIT_SOURCE_PROCESS:
				knownGone("a signal", source)
			default:
				unknown++
				if survives != nil {
					t.Fatalf("%+v with %v: a signal from %v leaves the fate unknown, got %v", exit, report, source, end)
				}
			}
		case *execv2.End_Failed:
			unknown++
			if survives != nil {
				t.Fatalf("%+v with %v: a failed end leaves the fate unknown, got %v", exit, report, end)
			}
		default:
			t.Fatalf("%+v with %v ended in an arm Exit never produces: %v", exit, report, end)
		}
	})
	// The three states must all occur, or the loop above proved nothing about one of them.
	if lost == 0 || gone == 0 || unknown == 0 {
		t.Fatalf("the shapes reached lost=%d gone=%d unknown=%d: every state must be reached", lost, gone, unknown)
	}
}

// Whatever an author returns, the End the SDK builds is one the wire can carry and says no more than the author said.
// Protobuf must marshal it (a string field holds UTF-8: a name that is not text is never copied in). A signal arm
// repeats the author's name and number exactly or leaves one out as "not reported": a number is never cut to fit the
// wire's int32, wrapped or invented, and a signal names at least one of the two. A target that declares no signals
// never answers with a signal arm and never turns a signal into a code: a signal it returns, from any source, is a
// failure of the target (INTERNAL, at RUN). An exit code is always the author's own, exactly: the SDK never makes one up.
func TestEveryExitShapeEndsInAnEndTheWireCanCarryAndSaysOnlyWhatTheAuthorSaid(t *testing.T) {
	exitShapes(func(exit Exit, report ExitReport, end *execv2.End) {
		if _, err := proto.Marshal(end); err != nil {
			t.Fatalf("%+v with %v ended in an End the wire cannot carry (%v): %v", exit, report, err, end)
		}
		if signaled := end.GetSignaled(); signaled != nil {
			name, number := signaled.GetSignal(), int(signaled.GetSignalNumber())
			switch {
			case report != ExitReportCodeAndSignal:
				t.Fatalf("%+v with %v: a target that declares no signal ended in one: %v", exit, report, end)
			case name == "" && number == 0:
				t.Fatalf("%+v with %v: a signal that names no signal: %v", exit, report, end)
			case name != "" && name != exit.Signal:
				t.Fatalf("%+v with %v: the signal name %q is not the author's: %v", exit, report, name, end)
			case number != 0 && number != exit.SignalNumber:
				t.Fatalf("%+v with %v: the signal number %d is not the author's: %v", exit, report, number, end)
			}
		}
		if exited := end.GetExited(); exited != nil && (exit.Code == nil || exited.GetExitCode() != *exit.Code) {
			t.Fatalf("%+v with %v: an exit code that is not the author's own, made up by the SDK: %v", exit, report, end)
		}
		if signalShaped(exit) && report != ExitReportCodeAndSignal {
			failed := end.GetFailed()
			if failed == nil || failed.GetClass() != execv2.ErrorClass_ERROR_CLASS_INTERNAL || failed.GetStage() != execv2.FailureStage_FAILURE_STAGE_RUN {
				t.Fatalf("%+v with %v: a signal from a target that declares none must be a failure of the target (INTERNAL, at RUN), got %v", exit, report, end)
			}
		}
	})
}
