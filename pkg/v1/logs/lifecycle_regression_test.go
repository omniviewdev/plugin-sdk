package logs_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs/logtest"
)

func TestSilentFollowReadyAndCancellationClosesReader(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Handler: func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
				return reader, nil
			}},
		},
		Sink: output,
	})
	// This reader deliberately ignores the handler context. Manager must close it.
	defer func() {
		_ = reader.Close()
		mgr.Close()
	}()
	ctx := types.NewPluginContext(context.Background(), "log", nil, nil, nil)
	session, err := mgr.CreateResourceSession(ctx, logs.CreateSessionOptions{
		ResourceKey: "pod", ResourceID: "silent", Options: logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.WaitForEvent(logs.StreamEventSessionReady, time.Second) == nil {
		t.Fatal("silent open stream never became ready")
	}
	got, err := mgr.GetSession(ctx, session.ID)
	if err != nil || got.Status != logs.LogSessionStatusActive {
		t.Fatalf("session = %+v, error = %v; want ACTIVE", got, err)
	}
	closed := make(chan error, 1)
	go func() { closed <- mgr.CloseSession(ctx, session.ID) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		_ = reader.Close()
		t.Fatal("CloseSession blocked on silent reader")
	}
}

func TestFiniteSessionEmitsEndAfterAllOutput(t *testing.T) {
	for _, content := range []string{"", "first\nsecond\n"} {
		t.Run(map[bool]string{true: "empty", false: "lines"}[content == ""], func(t *testing.T) {
			output := logtest.NewRecordingOutput()
			mgr := logs.NewManager(logs.ManagerConfig{
				Handlers: map[string]logs.Handler{
					"test/pod": {Handler: func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
						return io.NopCloser(strings.NewReader(content)), nil
					}},
				},
				Sink: output,
			})
			defer mgr.Close()
			ctx := types.NewPluginContext(context.Background(), "log", nil, nil, nil)
			session, err := mgr.CreateResourceSession(ctx, logs.CreateSessionOptions{ResourceKey: "pod", ResourceID: "finite"})
			if err != nil {
				t.Fatal(err)
			}
			if output.WaitForEvent(logs.StreamEventStreamEnded, time.Second) == nil {
				t.Fatal("finite stream never emitted STREAM_ENDED")
			}
			mgr.Wait()
			got, err := mgr.GetSession(ctx, session.ID)
			if err != nil || got.Status != logs.LogSessionStatusClosed {
				t.Fatalf("session = %+v, error = %v; want CLOSED", got, err)
			}
			want := strings.Count(content, "\n")
			if lines := output.Lines(); len(lines) != want {
				t.Fatalf("received %d lines, want %d", len(lines), want)
			}
		})
	}
}

type failedLogReader struct{}

func (failedLogReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failedLogReader) Close() error           { return nil }

func TestExhaustedLogRetriesSetErrorStatus(t *testing.T) {
	for _, mode := range []string{"open", "read", "nil"} {
		t.Run(mode, func(t *testing.T) {
			output := logtest.NewRecordingOutput()
			mgr := logs.NewManager(logs.ManagerConfig{
				Handlers: map[string]logs.Handler{
					"test/pod": {Handler: func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
						switch mode {
						case "open":
							return nil, errors.New("open failed")
						case "nil":
							return nil, nil
						default:
							return failedLogReader{}, nil
						}
					}},
				},
				Sink: output, Clock: timeutil.NewInstantFakeClock(),
			})
			defer mgr.Close()
			ctx := types.NewPluginContext(context.Background(), "log", nil, nil, nil)
			session, err := mgr.CreateResourceSession(ctx, logs.CreateSessionOptions{
				ResourceKey: "pod", ResourceID: "failed", Options: logs.LogSessionOptions{Follow: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			if output.WaitForEvent(logs.StreamEventStreamError, time.Second) == nil {
				t.Fatal("retry exhaustion did not emit STREAM_ERROR")
			}
			mgr.Wait()
			got, err := mgr.GetSession(ctx, session.ID)
			if err != nil || got.Status != logs.LogSessionStatusError {
				t.Fatalf("session = %+v, error = %v; want ERROR", got, err)
			}
			if mode != "read" {
				for _, event := range output.Events() {
					if event.Type == logs.StreamEventSessionReady {
						t.Fatal("failed acquisitions must not report readiness")
					}
				}
			}
		})
	}
}

func TestSourceFailureRemainsErrorWhenSiblingOpens(t *testing.T) {
	for _, mode := range []string{"open", "read"} {
		t.Run(mode, func(t *testing.T) {
			output := logtest.NewRecordingOutput()
			releaseSibling := make(chan struct{})
			mgr := logs.NewManager(logs.ManagerConfig{
				Handlers: map[string]logs.Handler{
					"test/pod": {
						SourceBuilder: logtest.MultiSourceBuilder("failed", "healthy"),
						Handler: func(ctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
							if req.SourceID == "failed" {
								if mode == "open" {
									return nil, errors.New("open failed")
								}
								return failedLogReader{}, nil
							}
							select {
							case <-releaseSibling:
								return io.NopCloser(strings.NewReader("healthy output\n")), nil
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						},
					},
				},
				Sink: output, Clock: timeutil.NewInstantFakeClock(),
			})
			defer mgr.Close()
			ctx := types.NewPluginContext(context.Background(), "log", nil, nil, nil)
			session, err := mgr.CreateResourceSession(ctx, logs.CreateSessionOptions{
				ResourceKey: "pod", ResourceID: "mixed",
			})
			if err != nil {
				t.Fatal(err)
			}
			if output.WaitForEvent(logs.StreamEventStreamError, time.Second) == nil {
				t.Fatal("failed source did not emit STREAM_ERROR")
			}
			got, err := mgr.GetSession(ctx, session.ID)
			if err != nil || got.Status != logs.LogSessionStatusError {
				t.Fatalf("session = %+v, error = %v; want ERROR while sibling is pending", got, err)
			}
			close(releaseSibling)
			mgr.Wait()
			got, err = mgr.GetSession(ctx, session.ID)
			if err != nil || got.Status != logs.LogSessionStatusError {
				t.Fatalf("session = %+v, error = %v; want ERROR after sibling completes", got, err)
			}
			for _, event := range output.Events() {
				if event.Type == logs.StreamEventSessionReady {
					t.Fatal("failed session must not report readiness when sibling opens")
				}
			}
			if lines := output.Lines(); len(lines) != 1 || lines[0].Content != "healthy output" {
				t.Fatalf("healthy sibling output = %+v; want one line", lines)
			}
		})
	}
}
