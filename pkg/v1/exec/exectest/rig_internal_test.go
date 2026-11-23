package exectest

import (
	"slices"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// quietT is a testing.TB that drops what is reported to Errorf, for a test that provokes violations on purpose and looks
// at the rig's record and not at what the rig told it. Everything else goes to the real test.
type quietT struct{ testing.TB }

func (quietT) Errorf(string, ...any) {}

// The Started that the host would receive is recorded before the first byte of output is: when it is recorded, no output
// exists, whoever makes it (the author's report, the first output on either stream) and however many bytes follow. A
// report the gate refuses records nothing.
func TestTheRigRecordsTheStartedBeforeAnyByteOfOutput(t *testing.T) {
	for name, run := range map[string]func(*ProcRig){
		"the author reports before it writes": func(r *ProcRig) {
			r.Proc().Started(exec.SessionFacts{PID: 1})
			_, _ = r.Proc().Stdout.Write([]byte("abc"))
			_, _ = r.Proc().Stderr.Write([]byte("def"))
		},
		"the first output is on stdout": func(r *ProcRig) {
			_, _ = r.Proc().Stdout.Write([]byte("abc"))
			_, _ = r.Proc().Stderr.Write([]byte("def"))
		},
		"the first output is on stderr": func(r *ProcRig) {
			_, _ = r.Proc().Stderr.Write([]byte("abc"))
			_, _ = r.Proc().Stdout.Write([]byte("def"))
		},
		"a late report records nothing": func(r *ProcRig) {
			_, _ = r.Proc().Stdout.Write([]byte("abc"))
			r.Proc().Started(exec.SessionFacts{PID: 1})
		},
		"a second report records nothing": func(r *ProcRig) {
			r.Proc().Started(exec.SessionFacts{PID: 1})
			r.Proc().Started(exec.SessionFacts{PID: 2})
			_, _ = r.Proc().Stdout.Write([]byte("abc"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rig := NewProcRig(quietT{t}, exec.Request{})
			run(rig)
			if !slices.Equal(rig.startedAt, []int{0}) {
				t.Errorf("the Started was recorded after %v bytes of output, want one Started, recorded before any", rig.startedAt)
			}
		})
	}
}
