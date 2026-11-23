package exec_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// echoFunction copies its input to its output until the input ends or the session is closed.
func echoFunction(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	_, _ = io.Copy(p.Stdout, p.Stdin)
	if ctx.Err() != nil {
		return exec.Exit{}, ctx.Err()
	}
	return exec.ExitCode(0), nil
}

func TestDescribeListsTheFeaturesOfTheSessionsAndRunsItServes(t *testing.T) {
	rig := newWireRig(t, engTarget(echoFunction))
	description, err := rig.client.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []execv2.Feature{
		execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN, execv2.Feature_FEATURE_RUN_STREAM,
		execv2.Feature_FEATURE_ENV, execv2.Feature_FEATURE_CWD,
	}
	got := description.GetFeatures()
	if len(got) != len(want) {
		t.Fatalf("Describe lists %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Describe lists %v, want %v", got, want)
		}
	}
}

// The first frame of a stream is Opened, with the facts the SDK knew before the author ran; the session echoes what the host
// sends with offsets that count the bytes, and ends with one Ended that closes the stream.
func TestASessionOverTheWireOpensEchoesAndEndsOnce(t *testing.T) {
	rig := newWireRig(t, engTarget(echoFunction))
	conn := rig.mustOpen("s1", withoutTTY)

	facts := conn.Opened().GetFacts()
	if facts.GetSessionId() != "s1" || facts.GetTarget().GetProvider() != "fake" || facts.GetTarget().GetType() != "thing" || facts.GetTarget().GetId() != "thing-1" {
		t.Errorf("Opened facts %v: wrong session or target", facts)
	}
	if facts.GetCapabilities() == nil || facts.GetLimits().GetMaxSessions() == 0 {
		t.Errorf("Opened facts %v: want the capabilities the session was opened with and its limits", facts)
	}
	if r := conn.Opened().GetReplay(); r == nil || r.GetStart() != 0 || r.GetEnd() != 0 || r.GetBytes() != 0 {
		t.Errorf("Opened replay %v: a new session has start = end = bytes = 0", r)
	}

	sendInput(t, conn, "hello ")
	sendInput(t, conn, "world")
	var got []byte
	var next uint64
	var startedBeforeOutput bool
	for len(got) < len("hello world") {
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		switch f := frame.GetFrame().(type) {
		case *execv2.ServerFrame_Started:
			startedBeforeOutput = len(got) == 0
		case *execv2.ServerFrame_Output:
			if f.Output.GetOffset() != next {
				t.Fatalf("an output frame at offset %d, want %d: offsets count the bytes", f.Output.GetOffset(), next)
			}
			next += uint64(len(f.Output.GetData()))
			got = append(got, f.Output.GetData()...)
		default:
			t.Fatalf("unexpected %s frame before the output was complete", frameKind(frame))
		}
	}
	if !startedBeforeOutput {
		t.Error("no Started came before the first Output")
	}
	if !bytes.Equal(got, []byte("hello world")) {
		t.Errorf("output %q, want the input echoed", got)
	}

	sendClose(t, conn, execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	_, ended := readUntilEnded(t, conn)
	if ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
		t.Errorf("the session ended %v, want closed by request", ended.GetEnd())
	}
	if ended.GetBytesOut() != uint64(len(got)) || ended.GetBytesIn() != uint64(len(got)) || ended.GetLastOffset() != ended.GetBytesOut() {
		t.Errorf("accounts out %d in %d last offset %d, want %d for each", ended.GetBytesOut(), ended.GetBytesIn(), ended.GetLastOffset(), len(got))
	}
}
