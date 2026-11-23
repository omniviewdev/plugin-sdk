package exec_test

// The legacy exec lane (omniview.sdk.exec.v1.ExecPlugin) must stay what SDK v0.21.0 made it, byte for byte, while
// the plugin also serves the exec v2 lane (omniview.sdk.exec.v2.Exec) on the same capability. These tests keep that
// promise on every run, on every platform that can open sessions:
//
//   - TestCompatFramesGoldenIsTheRecording checks that the golden file is the file the recorder wrote;
//   - TestCompatFramesReplay serves this tree's legacy lane and its v2 targets from one gRPC server, runs the legacy
//     scenario (testdata/compat-v0.21.0/helpers/scenario) through this tree's client with the recording
//     interceptors, and requires the normalized transcript to be equal to the golden.
//
// The golden, testdata/compat-v0.21.0/exec/legacy_transcript.json, is the transcript of a real v0.21.0 host talking
// to a real v0.21.0 plugin. TestCompatV0210 (compat_v0210_test.go) records it and, on every run of its CI job,
// proves that a v0.21.0 build still produces it, so that this replay compares this tree with v0.21.0 itself and not
// with a file that someone edited.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/echotarget"
	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/scenario"
)

const (
	// compatSDKModule, compatSDKTag and compatSDKCommit name the SDK build the golden was recorded from.
	compatSDKModule = "github.com/omniviewdev/plugin-sdk"
	compatSDKTag    = "v0.21.0"
	compatSDKCommit = "ae56fb095c665edc0298c7a7e19cfa8bad8aabca"

	// compatSafetyNet ends a run whose plugin stopped answering. It never decides a pass: a healthy run does not wait
	// for it.
	compatSafetyNet = time.Minute
)

// compatGolden is the golden file: the transcript of the legacy lane of the SDK build it names.
type compatGolden struct {
	SDK        string               `json:"sdk"`
	Tag        string               `json:"tag"`
	Commit     string               `json:"commit"`
	Transcript *scenario.Transcript `json:"transcript"`
}

// compatGoldenPath is the golden file, relative to the directory of this package, which is where go test runs.
func compatGoldenPath() string {
	return filepath.Join("..", "..", "..", "testdata", "compat-v0.21.0", "exec", "legacy_transcript.json")
}

// encodeCompatGolden is the one encoding of the golden file: indented JSON and a final newline.
func encodeCompatGolden(golden *compatGolden) ([]byte, error) {
	encoded, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// readCompatGolden reads the golden file strictly: it fails on a field it does not know.
func readCompatGolden(t *testing.T) (*compatGolden, []byte) {
	t.Helper()
	raw, err := os.ReadFile(compatGoldenPath())
	if err != nil {
		t.Fatalf("read the golden file: %v (record it with OMNIVIEW_COMPAT_V0210=1 OMNIVIEW_COMPAT_RECORD=1, see compat_v0210_test.go)", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var golden compatGolden
	if err := decoder.Decode(&golden); err != nil {
		t.Fatalf("decode the golden file: %v", err)
	}
	return &golden, raw
}

// assertCompatDescription checks what a host reads of the compat plugin through Describe: exactly the target
// compat/echo, the contract version of this build, and the features this build serves.
func assertCompatDescription(t *testing.T, d *execv2.DescribeResponse) {
	t.Helper()
	if d == nil {
		t.Fatal("Describe returned no description")
	}
	if d.GetContractVersion() != exec.ContractVersion {
		t.Errorf("contract version = %d, want %d", d.GetContractVersion(), exec.ContractVersion)
	}
	if want := []execv2.Feature{
		execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN, execv2.Feature_FEATURE_RUN_STREAM,
		execv2.Feature_FEATURE_ENV, execv2.Feature_FEATURE_CWD,
	}; !slices.Equal(d.GetFeatures(), want) {
		t.Errorf("features = %v, want %v", d.GetFeatures(), want)
	}
	if len(d.GetTargets()) != 1 {
		t.Fatalf("targets = %v, want exactly compat/echo", d.GetTargets())
	}
	target := d.GetTargets()[0]
	if target.GetProvider() != "compat" || target.GetType() != "echo" || target.GetDisplayName() != "Echo" ||
		!slices.Equal(target.GetDefaultArgv(), []string{"/bin/echo"}) {
		t.Errorf("target = %v, want compat/echo, DisplayName Echo, DefaultArgv [/bin/echo]", target)
	}
}

// compatInProcessHost is this tree's exec client talking to this tree's exec server over an in-memory connection.
type compatInProcessHost struct {
	client *exec.PluginClient
	rec    *scenario.Recorder
}

// newCompatInProcessHost serves the legacy handler compat/echo and the v2 target compat/echo from one gRPC server
// built as a plugin builds its own (SDK interceptors and wire limits), and connects this tree's client to it with
// the recording interceptors on the connection. Everything is stopped when the test ends.
func newCompatInProcessHost(t *testing.T) *compatInProcessHost {
	t.Helper()
	manager := exec.NewManager(exec.ManagerConfig{
		Logger:   logging.NewNop(),
		Handlers: map[string]exec.Handler{"compat/echo": scenario.EchoHandler()},
	})
	t.Cleanup(manager.Close)

	capability, err := exec.NewTargetPlugin(echotarget.Targets())
	if err != nil {
		t.Fatalf("build the exec capability: %v", err)
	}
	capability.Impl = manager
	server := sdk.GRPCServerFactoryWithLogger(logging.NewNop(), nil)
	if err := capability.GRPCServer(nil, server); err != nil {
		t.Fatalf("register the exec capability: %v", err)
	}
	listener := bufconn.Listen(1 << 20)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		if err := <-serveDone; err != nil {
			t.Errorf("serve: %v", err)
		}
	})

	rec := scenario.NewRecorder()
	options := slices.Concat(
		[]grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		},
		sdk.GRPCDialOptions(),
		rec.DialOptions(),
	)
	connection, err := grpc.NewClient("passthrough:///compat-replay", options...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	value, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, connection)
	if err != nil {
		t.Fatalf("build the exec client: %v", err)
	}
	client, ok := value.(*exec.PluginClient)
	if !ok {
		t.Fatalf("the exec client is a %T, want *exec.PluginClient", value)
	}
	return &compatInProcessHost{client: client, rec: rec}
}

// TestCompatFramesGoldenIsTheRecording requires the golden file to be the file the recorder writes: it names the
// v0.21.0 build it was recorded from, it holds a recorded run, and its bytes are the encoding of what it says, so
// that no one edited it by hand.
func TestCompatFramesGoldenIsTheRecording(t *testing.T) {
	golden, raw := readCompatGolden(t)
	if golden.SDK != compatSDKModule || golden.Tag != compatSDKTag || golden.Commit != compatSDKCommit {
		t.Errorf("golden is recorded from %s %s (%s), want %s %s (%s)",
			golden.SDK, golden.Tag, golden.Commit, compatSDKModule, compatSDKTag, compatSDKCommit)
	}
	if golden.Transcript == nil || len(golden.Transcript.Steps) == 0 {
		t.Fatal("the golden holds no recorded step: record it with OMNIVIEW_COMPAT_V0210=1 OMNIVIEW_COMPAT_RECORD=1")
	}
	encoded, err := encodeCompatGolden(golden)
	if err != nil {
		t.Fatalf("encode the golden: %v", err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Error("the golden file is not the encoding of what it says: it was edited by hand, re-record it")
	}
}

// TestCompatFramesReplay runs the legacy scenario against this tree's client and server, with the v2 target
// registered on the same capability, and requires the transcript to equal the golden.
func TestCompatFramesReplay(t *testing.T) {
	golden, _ := readCompatGolden(t)
	host := newCompatInProcessHost(t)

	t.Run("v2 lane beside the legacy lane", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), compatSafetyNet)
		defer cancel()
		description, err := host.client.V2().Describe(ctx)
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		assertCompatDescription(t, description)
	})

	t.Run("legacy scenario", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the legacy exec lane cannot open sessions on Windows")
		}
		ctx, cancel := context.WithTimeout(context.Background(), compatSafetyNet)
		defer cancel()
		got, err := scenario.Run(ctx, host.client, host.rec)
		if err != nil {
			t.Fatalf("run the legacy scenario: %v", err)
		}
		if !reflect.DeepEqual(golden.Transcript, got) {
			t.Fatalf("the legacy lane of this tree differs from the transcript recorded from SDK %s:\n%s",
				compatSDKTag, strings.Join(scenario.Diff(golden.Transcript, got), "\n"))
		}
	})
}

// compatSessionStream is the stream a scenario run gets from a wrapped Provider: the client's real stream seen
// through a goroutine of the test, which can add frames to it and take frames out of it.
type compatSessionStream struct {
	exec.Provider
	// open runs between the call of Stream and the client's open of the stream; it returns the context the stream
	// is opened with.
	open func(ctx context.Context, in chan exec.StreamInput) context.Context
	// forward decides, for every frame the client delivers, whether the scenario sees it; it may close out to end
	// the stream the scenario sees. It is called on the forwarding goroutine, which owns out.
	forward func(frame exec.StreamOutput, out chan exec.StreamOutput) (deliver, stop bool)
	// first is sent to the scenario before any frame of the client, when it is not nil.
	first *exec.StreamOutput
	// done is closed when the forwarding goroutine has read the client's channel to its end.
	done chan struct{}
}

func (s *compatSessionStream) Stream(ctx context.Context, in chan exec.StreamInput) (chan exec.StreamOutput, error) {
	if s.open != nil {
		ctx = s.open(ctx, in)
	}
	real, err := s.Provider.Stream(ctx, in)
	if err != nil {
		return nil, err
	}
	out := make(chan exec.StreamOutput)
	go func() {
		defer close(s.done)
		defer func() {
			for range real {
			}
		}()
		closed := false
		closeOut := func() {
			if !closed {
				closed = true
				close(out)
			}
		}
		defer closeOut()
		if s.first != nil {
			out <- *s.first
		}
		for frame := range real {
			if closed {
				continue
			}
			deliver, stop := true, false
			if s.forward != nil {
				deliver, stop = s.forward(frame, out)
			}
			if deliver {
				out <- frame
			}
			if stop {
				closeOut()
			}
		}
	}()
	return out, nil
}

// TestCompatScenarioOpenAbandoned requires a run that gives up on the opening of the stream (its watchdog expired)
// to release what the open worker started when it finishes later: the legacy sender of the client, which waits for
// input, and the stream's output, which the worker would otherwise leave unread.
func TestCompatScenarioOpenAbandoned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the legacy exec lane cannot open sessions on Windows")
	}
	host := newCompatInProcessHost(t)
	var (
		entered = make(chan struct{})
		release = make(chan struct{})
		input   = make(chan chan exec.StreamInput, 1)
	)
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseOpen) // a t.Fatal below must not leave the open worker blocked
	provider := &compatSessionStream{
		Provider: host.client,
		// The open worker has the stream open, as it does when it wins its race against the watchdog by a hair:
		// the stream's context is not the one the run cancels.
		open: func(ctx context.Context, in chan exec.StreamInput) context.Context {
			input <- in
			close(entered)
			<-release
			return context.WithoutCancel(ctx)
		},
		// The stream has a frame for the run that will never read it.
		first: &exec.StreamOutput{SessionID: scenario.SessionID, Signal: exec.StreamSignalNone, Target: exec.StreamTargetStdOut, Data: []byte("late")},
		done:  make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		_, err := scenario.Run(ctx, provider, host.rec)
		runDone <- err
	}()
	safety := time.After(compatSafetyNet)
	select {
	case <-entered:
	case <-safety:
		t.Fatal("the scenario did not open the stream")
	}
	cancel() // the watchdog expires while the open is in flight
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want the expiry of its context", err)
		}
	case <-safety:
		t.Fatal("Run did not return after its context was cancelled")
	}
	releaseOpen() // the open worker finishes, after Run gave up on it

	in := <-input
	select {
	case _, ok := <-in:
		if ok {
			t.Fatal("the input channel carries a frame, want it closed")
		}
	case <-safety:
		t.Fatal("the input channel of the abandoned open is still open: the client's sender waits on it for ever")
	}
	select {
	case <-provider.done:
	case <-safety:
		t.Fatal("the output of the abandoned open was not drained")
	}
}

// TestCompatScenarioCloseAfterCloseSession requires the wait for the Close signal to accept only a Close that is
// read after CloseSession: one that arrived earlier (here, before the echo) must not satisfy it.
func TestCompatScenarioCloseAfterCloseSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the legacy exec lane cannot open sessions on Windows")
	}
	host := newCompatInProcessHost(t)
	provider := &compatSessionStream{
		Provider: host.client,
		// A Close for the session before anything else, then no Close from CloseSession: the stream ends instead.
		first: &exec.StreamOutput{SessionID: scenario.SessionID, Signal: exec.StreamSignalClose},
		forward: func(frame exec.StreamOutput, _ chan exec.StreamOutput) (deliver, stop bool) {
			if frame.Signal == exec.StreamSignalClose && frame.SessionID == scenario.SessionID {
				return false, true
			}
			return true, false
		},
		done: make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), compatSafetyNet)
	defer cancel()
	_, err := scenario.Run(ctx, provider, host.rec)
	if err == nil || !strings.Contains(err.Error(), "the stream ended before the Close signal") {
		t.Fatalf("Run = %v, want an error that the stream ended before the Close signal", err)
	}
}
