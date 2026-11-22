package logs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// The compatibility tests pair the current code with SDK v0.21.0, the last tag
// before replay markers, in both directions, without importing it:
//
//   - an "old plugin" is what testdata/compat-v0.21.0 recorded from a real v0.21.0
//     plugin (see gen.go there): its create reply and its stream, decoded here by
//     the current reader over a real gRPC connection;
//   - an "old reader" is the v0.21.0 logs.proto schema, rebuilt from the recorded
//     descriptor and applying the admission the v0.21.0 client applied to what the
//     current plugin sends: the strict resource wire validator on the create reply
//     and the lenient protobuf decoder on the stream.
const compatDir = "testdata/compat-v0.21.0"

const compatSession = "11111111-1111-4111-8111-111111111111"

func compatFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(compatDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// oldSchema rebuilds the v0.21.0 logs.proto in a private registry, so it can live
// beside the current one under the same names.
func oldSchema(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	var fdp descriptorpb.FileDescriptorProto
	if err := proto.Unmarshal(compatFile(t, "logs.fdp"), &fdp); err != nil {
		t.Fatal(err)
	}
	files := new(protoregistry.Files)
	var add func(path string)
	add = func(path string) {
		if _, err := files.FindFileByPath(path); err == nil {
			return
		}
		fd, err := protoregistry.GlobalFiles.FindFileByPath(path)
		if err != nil {
			t.Fatalf("dependency %s: %v", path, err)
		}
		for i := range fd.Imports().Len() {
			add(fd.Imports().Get(i).Path())
		}
		if err := files.RegisterFile(fd); err != nil {
			t.Fatal(err)
		}
	}
	for _, dep := range fdp.GetDependency() {
		add(dep)
	}
	fd, err := protodesc.NewFile(&fdp, files)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

// capture records the raw bytes of every message a client receives.
type capture struct {
	codec  encoding.CodecV2
	frames [][]byte
}

func newCapture() *capture { return &capture{codec: encoding.GetCodecV2("proto")} }

func (c *capture) Name() string                           { return "proto" }
func (c *capture) Marshal(v any) (mem.BufferSlice, error) { return c.codec.Marshal(v) }
func (c *capture) Unmarshal(data mem.BufferSlice, v any) error {
	c.frames = append(c.frames, bytes.Clone(data.Materialize()))
	return c.codec.Unmarshal(data, v)
}

// replayingPlugin is a current plugin whose handler declares Replay.
func replayingPlugin(t *testing.T) *Manager {
	t.Helper()
	manager := NewManager(ManagerConfig{Handlers: map[string]Handler{"test/pod": {
		Plugin: "test", Resource: "pod",
		Replay: func(*types.PluginContext, LogStreamRequest) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(
				"2026-01-02T03:04:05.000000001Z first\n2026-01-02T03:04:06.000000002Z second\n")), nil
		},
		Handler: func(*types.PluginContext, LogStreamRequest) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		},
	}}})
	t.Cleanup(manager.Close)
	return manager
}

func compatRequest(t *testing.T, asked bool) *logspb.CreateLogSessionRequest {
	t.Helper()
	options := LogSessionOptions{Follow: true, IncludeTimestamps: true, IncludeSourceEvents: true, TailLines: 100}
	if asked {
		options.Params = map[string]string{ParamReplayMarkers: ReplayMarkersOn}
	}
	request, err := CreateSessionOptions{
		SessionID: compatSession, ResourceKey: "pod", ResourceID: "default/web-1", Options: options,
	}.ToProto()
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// A reader that never asked, whether or not it knows replay markers, is sent the
// stream a v0.21.0 plugin would send; one that asked is sent more, in fields and
// an enum value an old decoder skips.
func TestCurrentPluginToOldReader(t *testing.T) {
	old := oldSchema(t)
	oldReply := old.Messages().ByName("ResourceLogSessionResponse")
	oldOutput := old.Messages().ByName("LogStreamOutput")

	for _, asked := range []bool{false, true} {
		name := map[bool]string{false: "reader did not ask", true: "reader asked"}[asked]
		t.Run(name, func(t *testing.T) {
			manager := replayingPlugin(t)
			connection := logsConnection(t, func(s *grpc.Server) { RegisterLogPluginServer(s, &PluginServer{Impl: manager, log: logging.NewNop()}) })
			client := logspb.NewLogPluginClient(connection)

			replyBytes := newCapture()
			if _, err := client.CreateResourceSession(t.Context(), compatRequest(t, asked), grpc.ForceCodecV2(replyBytes)); err != nil {
				t.Fatal(err)
			}
			if len(replyBytes.frames) != 1 {
				t.Fatalf("captured %d create replies", len(replyBytes.frames))
			}
			// The create reply rides the strict lane: the old client rejects any field it
			// does not know, so the acceptance must use only fields v0.21.0 has.
			if err := resourceplugin.ValidateWire(replyBytes.frames[0], oldReply); err != nil {
				t.Fatalf("a v0.21.0 client's strict admission rejects the create reply: %v", err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			frames := newCapture()
			stream, err := client.Stream(ctx, grpc.ForceCodecV2(frames))
			if err != nil {
				t.Fatal(err)
			}
			sawLines, sawEvents, sawMarker := 0, 0, false
			for {
				message, recvErr := stream.Recv()
				if recvErr != nil {
					t.Fatalf("stream ended before the session did: %v", recvErr)
				}
				if message.GetEvent().GetType() == logspb.LogStreamEventType_LOG_STREAM_EVENT_STREAM_ENDED {
					break
				}
			}
			for _, frame := range frames.frames {
				view := dynamicpb.NewMessage(oldOutput)
				if err := proto.Unmarshal(frame, view); err != nil {
					t.Fatalf("an old decoder fails on a frame: %v", err)
				}
				payload := view.Descriptor().Oneofs().ByName("payload")
				switch field := view.WhichOneof(payload); {
				case field == nil:
					t.Fatal("a frame with no payload")
				case field.Name() == "line":
					sawLines++
					line := view.Get(field).Message()
					if got := string(line.Get(line.Descriptor().Fields().ByName("content")).Bytes()); got != "first" && got != "second" {
						t.Errorf("line content = %q: the old decoder must read known fields intact", got)
					}
					if !asked && len(line.GetUnknown()) != 0 {
						t.Errorf("a reader that did not ask received new fields on a line: %x", line.GetUnknown())
					}
					if asked && len(line.GetUnknown()) == 0 {
						t.Error("the asked-for numbering did not reach the stream")
					}
				case field.Name() == "event":
					sawEvents++
					event := view.Get(field).Message()
					kind := event.Get(event.Descriptor().Fields().ByName("type")).Enum()
					if kind == 7 {
						sawMarker = true
					}
					if !asked && (kind == 7 || len(event.GetUnknown()) != 0) {
						t.Errorf("a reader that did not ask received replay metadata on an event: kind=%d unknown=%x", kind, event.GetUnknown())
					}
				}
			}
			if sawLines != 2 || sawEvents == 0 {
				t.Fatalf("decoded %d lines and %d events, want 2 lines and some events", sawLines, sawEvents)
			}
			if sawMarker != asked {
				t.Errorf("marker seen = %v, want %v", sawMarker, asked)
			}
		})
	}
}

// Why the acceptance rides in Options.Params: a new field in the create reply is
// rejected outright by the strict admission of every client that already shipped.
func TestANewCreateReplyFieldWouldBreakOldClients(t *testing.T) {
	old := oldSchema(t)
	oldReply := old.Messages().ByName("ResourceLogSessionResponse")
	reply := dynamicpb.NewMessage(oldReply)
	if err := proto.Unmarshal(compatFile(t, "create_reply.pb"), reply); err != nil {
		t.Fatal(err)
	}
	if err := resourceplugin.ValidateWire(compatFile(t, "create_reply.pb"), oldReply); err != nil {
		t.Fatalf("the recorded v0.21.0 reply must pass its own admission: %v", err)
	}
	sessionField := oldReply.Fields().ByName("session")
	session := reply.Mutable(sessionField).Message()
	const futureField = 99
	session.SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, futureField, protowire.VarintType), 1))
	forged, err := proto.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	if err := resourceplugin.ValidateWire(forged, oldReply); err == nil {
		t.Fatal("strict admission accepted an unknown field in the create reply: the premise of the params echo is gone, so revisit the acceptance carrier")
	}
}

// oldPlugin replays what a recorded v0.21.0 plugin sent.
type oldPlugin struct {
	logspb.UnimplementedLogPluginServer
	reply  *logspb.ResourceLogSessionResponse
	frames []*logspb.LogStreamOutput
}

func (p *oldPlugin) CreateResourceSession(context.Context, *logspb.CreateLogSessionRequest) (*logspb.ResourceLogSessionResponse, error) {
	return p.reply, nil
}

func (p *oldPlugin) Stream(stream logspb.LogPlugin_StreamServer) error {
	for _, frame := range p.frames {
		if err := stream.Send(frame); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	return nil
}

func recordedOldPlugin(t *testing.T) *oldPlugin {
	t.Helper()
	plugin := &oldPlugin{reply: &logspb.ResourceLogSessionResponse{}}
	if err := proto.Unmarshal(compatFile(t, "create_reply.pb"), plugin.reply); err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(compatFile(t, "stream.pb"))
	for reader.Len() > 0 {
		frame := &logspb.LogStreamOutput{}
		if err := protodelim.UnmarshalFrom(reader, frame); err != nil {
			t.Fatal(err)
		}
		plugin.frames = append(plugin.frames, frame)
	}
	return plugin
}

// A plugin that predates replay markers echoes the request and sends the old
// stream; the current reader tells from the create reply, with no timeout, that no
// marker will ever come, and reads the stream unchanged.
func TestOldPluginToCurrentReader(t *testing.T) {
	plugin := recordedOldPlugin(t)
	connection := logsConnection(t, func(s *grpc.Server) { RegisterLogPluginServer(s, plugin) })
	client := &PluginClient{client: logspb.NewLogPluginClient(connection), log: logging.NewNop()}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pc := types.NewPluginContext(ctx, "logs", nil, nil, nil)

	session, err := client.CreateResourceSession(pc, CreateSessionOptions{
		SessionID: compatSession, ResourceKey: "pod", ResourceID: "default/web-1",
		Options: LogSessionOptions{
			Follow: true, IncludeTimestamps: true, IncludeSourceEvents: true, TailLines: 100,
			Params: map[string]string{ParamReplayMarkers: ReplayMarkersOn},
		},
	})
	if err != nil {
		t.Fatalf("the current strict admission rejects a v0.21.0 create reply: %v", err)
	}
	if session.ReplayMarkers {
		t.Fatal("a plugin that echoes the request must not read as having accepted it")
	}
	if got := session.Options.Params[ParamReplayMarkers]; got != ReplayMarkersOn {
		t.Errorf("the old plugin's echo is %q, want the reader's own %q", got, ReplayMarkersOn)
	}
	if _, forged := session.Options.Params[ParamReplayMarkersAccepted]; forged {
		t.Error("an old plugin cannot have accepted")
	}

	in := make(chan StreamInput)
	out, err := client.Stream(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var lines []LogLine
	var kinds []LogStreamEventType
	for output := range out {
		if output.Line != nil {
			lines = append(lines, *output.Line)
		}
		if output.Event != nil {
			kinds = append(kinds, output.Event.Type)
			if output.Event.Type == StreamEventStreamEnded {
				break
			}
		}
	}
	if len(lines) != 2 || lines[0].Content != "first" || lines[1].Content != "second" {
		t.Fatalf("lines = %+v", lines)
	}
	for _, l := range lines {
		if l.Seq != 0 || l.Replay {
			t.Errorf("an old plugin's line reads as numbered: %+v", l)
		}
	}
	for _, kind := range kinds {
		if kind == StreamEventSourceCaughtUp || kind == StreamEventUnknown {
			t.Errorf("an old plugin's stream decoded to %v", kind)
		}
	}
}

// A newer plugin's session reports acceptance through the same path.
func TestCurrentPluginAcceptanceReachesTheReader(t *testing.T) {
	manager := replayingPlugin(t)
	connection := logsConnection(t, func(s *grpc.Server) { RegisterLogPluginServer(s, &PluginServer{Impl: manager, log: logging.NewNop()}) })
	client := &PluginClient{client: logspb.NewLogPluginClient(connection), log: logging.NewNop()}
	pc := types.NewPluginContext(t.Context(), "logs", nil, nil, nil)

	for _, asked := range []bool{false, true} {
		options := LogSessionOptions{Follow: true}
		if asked {
			options.Params = map[string]string{ParamReplayMarkers: ReplayMarkersOn}
		}
		session, err := client.CreateResourceSession(pc, CreateSessionOptions{
			ResourceKey: "pod", ResourceID: "default/web-1", Options: options,
		})
		if err != nil {
			t.Fatal(err)
		}
		if session.ReplayMarkers != asked {
			t.Errorf("asked=%v: ReplayMarkers=%v", asked, session.ReplayMarkers)
		}
		if err := client.CloseSession(pc, session.ID); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}
