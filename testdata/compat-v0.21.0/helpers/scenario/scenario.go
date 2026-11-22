// Package scenario is the legacy exec scenario of the exec v2 compatibility proof.
//
// It uses only the legacy exec API that SDK v0.21.0 and this tree share (exec.Provider, exec.SessionOptions, the
// stream types, exec.Plugin, the lifecycle client), so one source compiles against both. The host built against
// v0.21.0 and the host and tests of this tree run the very same steps and record, with gRPC client interceptors,
// what crosses the wire: the protobuf bytes of every request, response and stream frame of the legacy service
// (omniview.sdk.exec.v1.ExecPlugin). The recorded Transcripts are compared with each other and with a golden
// recorded from a real v0.21.0 build.
package scenario

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	osexec "os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/lifecycle"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

const (
	// PluginID is the id of the helper plugins: their manifest, their handshake and the hosts agree on it.
	PluginID = "compat-exec"
	// SessionID is the id the scenario gives its session.
	SessionID = "compat-session-1"

	handlerPlugin   = "compat"
	handlerResource = "echo"
	unknownSession  = "compat-missing"
	otherSession    = "compat-session-2"
	input           = "ping\n"
	reply           = "ready\r\ngot:ping\n"
)

// Transcript is what the legacy exec lane showed on the wire during one run of Run. Every byte string is the
// deterministic protobuf encoding of a message after normalization (see Message), so that two runs are equal when
// the two lanes spoke the same bytes. All slices are non-nil, so that a Transcript read back from JSON is
// reflect.DeepEqual to the one that was written.
type Transcript struct {
	// Steps lists, in order, the opening of the stream and each unary call of the scenario.
	Steps []Step `json:"steps"`
	// Sent lists the frames the host sent on the multiplexed stream, in order.
	Sent []Message `json:"sent"`
	// Data lists the output of the sessions: for each session and target stream, one StreamOutput whose data is the
	// concatenation of the data of every data frame received (the pty cuts its output into chunks of any size, so
	// the chunks are not comparable and their sequence numbers are dropped), ordered by session and target.
	Data []Message `json:"data"`
	// Signals lists, in order, the frames received that carry a signal or an error (the Close signal), as they came.
	Signals []Message `json:"signals"`
}

// Step is one call of the scenario and the legacy RPC it made.
type Step struct {
	// Name is the name of the step in the scenario.
	Name string `json:"name"`
	// Method is the full gRPC method of the RPC.
	Method string `json:"method"`
	// Request is the request message. Empty for the stream, which carries its frames in Transcript.Sent.
	Request Message `json:"request"`
	// Response is the response message, empty when the RPC failed or is the stream.
	Response Message `json:"response"`
	// Code is the gRPC status code of the RPC.
	Code string `json:"code"`
	// Message is the status message of a failed RPC.
	Message string `json:"message"`
	// Error is what the SDK's client returned to its caller for the call, empty on success.
	Error string `json:"error"`
}

// Message is a protobuf message as the transcript keeps it. Before it is encoded the volatile content is
// normalized: a google.protobuf.Timestamp field named created_at, when present, is replaced by the zero Timestamp,
// so that the time of the run is not part of the bytes and the presence of the field still is.
type Message struct {
	// Hex is the deterministic protobuf encoding of the normalized message, in hexadecimal.
	Hex string `json:"hex"`
	// Text is the same message rendered for a human reader: field names and values in declaration order. It is
	// derived from the normalized message and compared like Hex.
	Text string `json:"text"`
}

// Lifecycle is what the lifecycle service of a plugin says about it.
type Lifecycle struct {
	PluginID               string   `json:"plugin_id"`
	Version                string   `json:"version"`
	SDKProtocolVersion     int32    `json:"sdk_protocol_version"`
	HostConnectionsVersion uint32   `json:"host_connections_version"`
	Capabilities           []string `json:"capabilities"`
}

// Child is how a plugin process ended.
type Child struct {
	PID    int  `json:"pid"`
	Exited bool `json:"exited"`
}

// Report is what a host binary prints on its standard output, as one JSON document.
type Report struct {
	Transcript *Transcript `json:"transcript"`
	Lifecycle  Lifecycle   `json:"lifecycle"`
	Child      Child       `json:"child"`
}

// legacyPrefix is the prefix of every method of the legacy exec service.
var legacyPrefix = "/" + execpb.ExecPlugin_ServiceDesc.ServiceName + "/"

// ---------------------------------------------------------------------------
// The legacy handler
// ---------------------------------------------------------------------------

// EchoHandler is the one legacy handler of the helper plugins, compat/echo. Its session reads lines from the
// terminal and answers each as "got:<line>"; the first answer is preceded by "ready\r\n". The banner is written
// when the first line arrives, not when the session starts: the output of a session then never precedes the host's
// first input, which is only sent over the stream once the plugin's stream handler runs. The attach buffer of the
// first attach is empty and no output can be dropped before the stream exists, so the transcript is the same on
// every run without waiting for anything.
func EchoHandler() exec.Handler {
	return exec.Handler{
		Plugin:   handlerPlugin,
		Resource: handlerResource,
		TargetBuilder: exec.ActionTargetBuilder{
			Label:     "Echo",
			Paths:     []string{"metadata.name"},
			Selectors: map[string]string{"kind": "echo"},
		},
		DefaultCommand: []string{"echo"},
		TTYHandler:     echoSession,
	}
}

// echoSession starts the session's goroutine on the terminal and returns. The goroutine ends when the manager
// closes the terminal at the end of the session.
func echoSession(
	_ *types.PluginContext,
	_ exec.SessionOptions,
	tty *os.File,
	_ chan error,
	_ <-chan exec.SessionResizeInput,
) error {
	go func() {
		var pending []byte
		buf := make([]byte, 4096)
		banner := true
		for {
			n, readErr := tty.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				end := bytes.IndexByte(pending, '\n')
				if end < 0 {
					break
				}
				answer := append([]byte("got:"), pending[:end+1]...)
				if banner {
					answer = append([]byte("ready\r\n"), answer...)
					banner = false
				}
				if _, writeErr := tty.Write(answer); writeErr != nil {
					return
				}
				pending = pending[end+1:]
			}
			if readErr != nil {
				return
			}
		}
	}()
	return nil
}

// ---------------------------------------------------------------------------
// The recorder
// ---------------------------------------------------------------------------

// Recorder records the legacy exec RPCs of one connection. Pass DialOptions to the connection (for a go-plugin
// client, in ClientConfig.GRPCDialOptions); every other service on the connection is ignored.
type Recorder struct {
	mu      sync.Mutex
	calls   []rpc
	sent    []Message
	output  map[outputKey][]byte
	signals []Message
}

// rpc is one legacy RPC as the recorder saw it.
type rpc struct {
	method   string
	request  Message
	response Message
	code     string
	message  string
}

// outputKey identifies one output stream of a session.
type outputKey struct {
	session string
	target  execpb.StreamOutput_Target
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder {
	return &Recorder{output: map[outputKey][]byte{}}
}

// DialOptions returns the dial options that put the recorder's interceptors on a connection.
func (r *Recorder) DialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(r.unary),
		grpc.WithChainStreamInterceptor(r.stream),
	}
}

// unary records a unary call of the legacy service after it returned.
func (r *Recorder) unary(
	ctx context.Context,
	method string,
	req, resp any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	err := invoker(ctx, method, req, resp, cc, opts...)
	if !strings.HasPrefix(method, legacyPrefix) {
		return err
	}
	call := rpc{method: method, request: encodeAny(req)}
	if err == nil {
		call.response = encodeAny(resp)
	}
	st, _ := status.FromError(err)
	call.code, call.message = st.Code().String(), st.Message()
	r.record(call)
	return err
}

// stream records the opening of a stream of the legacy service and wraps it so that its frames are recorded.
func (r *Recorder) stream(
	ctx context.Context,
	desc *grpc.StreamDesc,
	cc *grpc.ClientConn,
	method string,
	streamer grpc.Streamer,
	opts ...grpc.CallOption,
) (grpc.ClientStream, error) {
	cs, err := streamer(ctx, desc, cc, method, opts...)
	if !strings.HasPrefix(method, legacyPrefix) {
		return cs, err
	}
	st, _ := status.FromError(err)
	r.record(rpc{method: method, code: st.Code().String(), message: st.Message()})
	if err != nil {
		return cs, err
	}
	return &recordedStream{ClientStream: cs, rec: r, method: method}, nil
}

// recordedStream records the frames that pass through a client stream, and how the call ended.
type recordedStream struct {
	grpc.ClientStream

	rec    *Recorder
	method string
	ended  sync.Once
}

// SendMsg sends a frame and records it when it was sent.
func (s *recordedStream) SendMsg(m any) error {
	err := s.ClientStream.SendMsg(m)
	if err == nil {
		s.rec.sentFrame(m)
	}
	return err
}

// RecvMsg receives a frame and records it when one arrived; the error that ends the call is recorded as the status
// of the call (io.EOF is the plugin ending the call with OK).
func (s *recordedStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err == nil {
		s.rec.receivedFrame(m)
		return nil
	}
	s.ended.Do(func() {
		call := rpc{method: s.method, code: codes.OK.String()}
		if !errors.Is(err, io.EOF) {
			st, _ := status.FromError(err)
			call.code, call.message = st.Code().String(), st.Message()
		}
		s.rec.record(call)
	})
	return err
}

// record keeps an RPC of the legacy service.
func (r *Recorder) record(call rpc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

// drain returns the RPCs recorded since the last call and forgets them.
func (r *Recorder) drain() []rpc {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

func (r *Recorder) sentFrame(m any) {
	frame := encodeAny(m)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, frame)
}

// receivedFrame keeps the data of a data frame, whose chunking is not comparable, and the bytes of every frame that
// carries a signal or an error.
func (r *Recorder) receivedFrame(m any) {
	out, ok := m.(*execpb.StreamOutput)
	if !ok {
		return
	}
	if out.GetSignal() != execpb.StreamSignal_STREAM_SIGNAL_NONE || out.GetError() != nil {
		frame := encode(out)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.signals = append(r.signals, frame)
		return
	}
	if len(out.GetData()) == 0 {
		return
	}
	key := outputKey{session: out.GetId(), target: out.GetTarget()}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.output[key] = append(r.output[key], out.GetData()...)
}

// frames returns the stream half of a transcript: what was sent, the output per session and target, and the
// frames that carry a signal or an error.
func (r *Recorder) frames() (sent, data, signals []Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sent = append([]Message{}, r.sent...)
	signals = append([]Message{}, r.signals...)
	keys := slices.SortedFunc(maps.Keys(r.output), func(a, b outputKey) int {
		return cmp.Or(cmp.Compare(a.session, b.session), cmp.Compare(a.target, b.target))
	})
	data = make([]Message, 0, len(keys))
	for _, key := range keys {
		data = append(data, encode(&execpb.StreamOutput{Id: key.session, Target: key.target, Data: r.output[key]}))
	}
	return sent, data, signals
}

// ---------------------------------------------------------------------------
// Encoding and normalization
// ---------------------------------------------------------------------------

// encodeAny encodes a message that a gRPC interceptor holds as an any.
func encodeAny(v any) Message {
	m, ok := v.(proto.Message)
	if !ok {
		return Message{Text: fmt.Sprintf("not a protobuf message: %T", v)}
	}
	return encode(m)
}

// encode normalizes a copy of m and returns its deterministic encoding and its text. m is not modified.
func encode(m proto.Message) Message {
	normalized := proto.Clone(m)
	pinTimestamps(normalized.ProtoReflect())
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(normalized)
	if err != nil {
		return Message{Text: "marshal error: " + err.Error()}
	}
	return Message{Hex: hex.EncodeToString(raw), Text: messageText(normalized.ProtoReflect())}
}

// pinTimestamps replaces every set google.protobuf.Timestamp field named created_at, in m and in the messages
// nested in it, by the zero Timestamp: the time of the run is not part of the transcript, and the presence of the
// field is.
func pinTimestamps(m protoreflect.Message) {
	var volatile []protoreflect.FieldDescriptor
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, entry protoreflect.Value) bool {
					pinTimestamps(entry.Message())
					return true
				})
			}
		case fd.IsList():
			if fd.Message() != nil {
				for i := range v.List().Len() {
					pinTimestamps(v.List().Get(i).Message())
				}
			}
		case fd.Message() != nil:
			if fd.Name() == "created_at" && fd.Message().FullName() == "google.protobuf.Timestamp" {
				volatile = append(volatile, fd)
			} else {
				pinTimestamps(v.Message())
			}
		}
		return true
	})
	for _, fd := range volatile {
		m.Set(fd, protoreflect.ValueOfMessage((&timestamppb.Timestamp{}).ProtoReflect()))
	}
}

// messageText renders the populated fields of m in declaration order: scalars as name: value, messages as
// name { ... }, a repeated field as one entry per element and a map as one { key value } entry per key, sorted.
func messageText(m protoreflect.Message) string {
	var parts []string
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if !m.Has(fd) {
			continue
		}
		name, v := string(fd.Name()), m.Get(fd)
		switch {
		case fd.IsList():
			for j := range v.List().Len() {
				parts = append(parts, entryText(name, fd, v.List().Get(j)))
			}
		case fd.IsMap():
			keys := make([]protoreflect.MapKey, 0, v.Map().Len())
			v.Map().Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
				keys = append(keys, k)
				return true
			})
			slices.SortFunc(keys, func(a, b protoreflect.MapKey) int {
				return cmp.Compare(fmt.Sprint(a.Interface()), fmt.Sprint(b.Interface()))
			})
			for _, k := range keys {
				parts = append(parts, name+" { key: "+scalarText(fd.MapKey(), k.Value())+" "+
					entryText("value", fd.MapValue(), v.Map().Get(k))+" }")
			}
		default:
			parts = append(parts, entryText(name, fd, v))
		}
	}
	return strings.Join(parts, " ")
}

// entryText renders one value of a field.
func entryText(name string, fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	if fd.Message() == nil {
		return name + ": " + scalarText(fd, v)
	}
	if inner := messageText(v.Message()); inner != "" {
		return name + " { " + inner + " }"
	}
	return name + " {}"
}

// scalarText renders a scalar value: strings and bytes quoted, enums by name.
func scalarText(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return strconv.Quote(v.String())
	case protoreflect.BytesKind:
		return strconv.Quote(string(v.Bytes()))
	case protoreflect.EnumKind:
		if value := fd.Enum().Values().ByNumber(v.Enum()); value != nil {
			return string(value.Name())
		}
		return strconv.Itoa(int(v.Enum()))
	default:
		return fmt.Sprint(v.Interface())
	}
}

// ---------------------------------------------------------------------------
// The scenario
// ---------------------------------------------------------------------------

// runner is the state of one run of the scenario.
type runner struct {
	ctx     context.Context
	rec     *Recorder
	steps   []Step
	in      chan exec.StreamInput
	out     chan exec.StreamOutput
	cancel  context.CancelFunc
	closeIn sync.Once
	stdout  map[string][]byte
	closed  map[string]bool
	// mu guards out and abandoned while the open of the stream may still be in flight.
	mu        sync.Mutex
	abandoned bool
}

// Run runs the legacy scenario against p, whose connection must carry rec's interceptors, and returns the
// transcript. The scenario: list the handlers; open the multiplexed stream; create a session (client-chosen id, tty,
// handler compat/echo, which the client sends as CreateResourceSession: it carries the resource selectors); attach;
// send "ping\n" and wait for the echo on the stream; resize; list; get; detach; attach again (the replay buffer holds
// the whole output); four failures (unknown session, unknown handler, a plain CreateSession that selects no
// handler, close of an unknown session); close the session and wait for its Close signal on the stream; then the
// same calls on the closed session, and the end of the stream.
//
// No step is decided by time: every wait is on the stream, and ctx is only the safety net that ends a run whose
// plugin stopped answering.
func Run(ctx context.Context, p exec.Provider, rec *Recorder) (*Transcript, error) {
	r := &runner{ctx: ctx, rec: rec, steps: []Step{}, stdout: map[string][]byte{}, closed: map[string]bool{}}
	pc := &types.PluginContext{RequestID: "compat-request", RequesterID: "compat-host"}
	session := exec.SessionOptions{
		ID:             SessionID,
		ResourcePlugin: handlerPlugin,
		ResourceKey:    handlerResource,
		Command:        []string{"echo"},
		TTY:            true,
		Params:         map[string]string{"scenario": "legacy"},
		Labels:         map[string]string{"suite": "compat"},
	}
	unknownHandler := exec.SessionOptions{
		ID:             otherSession,
		ResourcePlugin: handlerPlugin,
		ResourceKey:    "missing",
		Command:        []string{"echo"},
		TTY:            true,
	}
	plainSession := exec.SessionOptions{ID: otherSession, Command: []string{"echo"}, TTY: true}

	if err := r.must("get-supported-resources", func() error {
		if handlers := p.GetSupportedResources(pc); len(handlers) != 1 || handlers[0].ID() != handlerPlugin+"/"+handlerResource {
			return fmt.Errorf("handlers are %v, want [%s/%s]", handlers, handlerPlugin, handlerResource)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	r.in, r.cancel = make(chan exec.StreamInput), cancelStream
	if err := r.openStream(p, streamCtx); err != nil {
		return nil, err
	}
	defer func() { _ = r.endStream(true) }()

	for _, step := range []struct {
		name string
		call func() error
	}{
		{"create-session", func() (err error) { _, err = p.CreateSession(pc, session); return err }},
		{"attach-session", func() (err error) { _, _, err = p.AttachSession(pc, SessionID); return err }},
	} {
		if err := r.must(step.name, step.call); err != nil {
			return nil, err
		}
	}

	// The echo proves that the plugin's stream handler runs: the resize that follows is taken by it at once.
	select {
	case r.in <- exec.StreamInput{SessionID: SessionID, Data: []byte(input)}:
	case <-ctx.Done():
		return nil, fmt.Errorf("sending the input: %w", ctx.Err())
	}
	if err := r.until("the echo of the input", func() bool { return bytes.Contains(r.stdout[SessionID], []byte(reply)) }); err != nil {
		return nil, err
	}

	var replay []byte
	for _, step := range []struct {
		name string
		call func() error
	}{
		{"resize-session", func() error { return p.ResizeSession(pc, SessionID, 40, 120) }},
		{"list-sessions", func() (err error) { _, err = p.ListSessions(pc); return err }},
		{"get-session", func() (err error) { _, err = p.GetSession(pc, SessionID); return err }},
		{"detach-session", func() (err error) { _, err = p.DetachSession(pc, SessionID); return err }},
		{"attach-session-again", func() (err error) { _, replay, err = p.AttachSession(pc, SessionID); return err }},
	} {
		if err := r.must(step.name, step.call); err != nil {
			return nil, err
		}
	}
	// The replay buffer holds the whole output of the session so far; a reattach that lost it would still record the
	// same bytes on the wire when the client dropped them, so the buffer the client returns is checked as well.
	if string(replay) != reply {
		return nil, fmt.Errorf("step attach-session-again: the replay buffer is %q, want %q", replay, reply)
	}

	for _, step := range []struct {
		name string
		call func() error
	}{
		{"get-session-unknown", func() (err error) { _, err = p.GetSession(pc, unknownSession); return err }},
		{"create-session-unknown-handler", func() (err error) { _, err = p.CreateSession(pc, unknownHandler); return err }},
		{"create-session-plain-no-handler", func() (err error) { _, err = p.CreateSession(pc, plainSession); return err }},
		{"close-session-unknown", func() error { return p.CloseSession(pc, unknownSession) }},
	} {
		if err := r.mustFail(step.name, step.call); err != nil {
			return nil, err
		}
	}

	// A Close the stream delivered before this step is not the answer to CloseSession: the wait below takes only
	// one that is read after it.
	delete(r.closed, SessionID)
	if err := r.must("close-session", func() error { return p.CloseSession(pc, SessionID) }); err != nil {
		return nil, err
	}
	if err := r.until("the Close signal", func() bool { return r.closed[SessionID] }); err != nil {
		return nil, err
	}

	for _, step := range []struct {
		name string
		call func() error
	}{
		{"close-session-again", func() error { return p.CloseSession(pc, SessionID) }},
		{"get-session-closed", func() (err error) { _, err = p.GetSession(pc, SessionID); return err }},
	} {
		if err := r.mustFail(step.name, step.call); err != nil {
			return nil, err
		}
	}
	if err := r.must("list-sessions-closed", func() (err error) { _, err = p.ListSessions(pc); return err }); err != nil {
		return nil, err
	}

	if err := r.endStream(false); err != nil {
		return nil, err
	}
	sent, data, signals := rec.frames()
	return &Transcript{Steps: r.steps, Sent: sent, Data: data, Signals: signals}, nil
}

// openStream opens the multiplexed stream as the step open-stream. The open runs on the worker goroutine of call,
// which may outlive a run whose watchdog expired: the client starts its sender, which waits for input on r.in, when
// the stream is open, and that can be after the run gave up. A run that fails to open therefore abandons the open: it
// closes r.in, which ends the sender, and cancels the stream, and a worker that finishes after that drains the output
// it was handed instead of handing it to a run that is gone. A run whose open succeeds owns r.in and r.out, and
// ends them with endStream.
func (r *runner) openStream(p exec.Provider, streamCtx context.Context) error {
	err := r.must("open-stream", func() error {
		out, err := p.Stream(streamCtx, r.in)
		if err != nil {
			return err
		}
		r.mu.Lock()
		if r.abandoned {
			r.mu.Unlock()
			drainOutput(out)
			return errors.New("the open was abandoned")
		}
		r.out = out
		r.mu.Unlock()
		return nil
	})
	if err == nil {
		return nil
	}
	r.mu.Lock()
	r.abandoned = true
	r.mu.Unlock()
	// From here on the worker no longer writes r.out: what it has handed over by now is drained like the output of
	// any other aborted stream.
	r.closeIn.Do(func() { close(r.in) })
	r.cancel()
	if r.out != nil {
		r.drainAfterExpiry()
	}
	return err
}

// call runs fn under the watchdog and records the one legacy RPC it made as the step name. It returns the error of
// fn apart from the failures of the scenario itself (a call that never returned, a call that made no RPC).
func (r *runner) call(name string, fn func() error) (callErr, runErr error) {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case callErr = <-done:
	case <-r.ctx.Done():
		return nil, fmt.Errorf("step %s: %w", name, r.ctx.Err())
	}
	calls := r.rec.drain()
	if len(calls) != 1 {
		return nil, fmt.Errorf("step %s made %d legacy RPCs, want 1: is the recorder on the connection?", name, len(calls))
	}
	r.steps = append(r.steps, newStep(name, calls[0], callErr))
	return callErr, nil
}

// newStep builds the step of a call from the RPC the recorder saw and the error the SDK's client returned for it.
func newStep(name string, call rpc, callErr error) Step {
	step := Step{
		Name:     name,
		Method:   call.method,
		Request:  call.request,
		Response: call.response,
		Code:     call.code,
		Message:  call.message,
	}
	if callErr != nil {
		step.Error = callErr.Error()
	}
	return step
}

// must runs a step that has to succeed.
func (r *runner) must(name string, fn func() error) error {
	callErr, runErr := r.call(name, fn)
	if runErr != nil {
		return runErr
	}
	if callErr != nil {
		return fmt.Errorf("step %s: %w", name, callErr)
	}
	return nil
}

// mustFail runs a step that has to fail: the failure is what the transcript records.
func (r *runner) mustFail(name string, fn func() error) error {
	callErr, runErr := r.call(name, fn)
	if runErr != nil {
		return runErr
	}
	if callErr == nil {
		return fmt.Errorf("step %s succeeded, want an error", name)
	}
	return nil
}

// until reads the stream until done holds. Frames are only read here, so that the receiver of the client never
// holds more than one of them back.
func (r *runner) until(what string, done func() bool) error {
	for !done() {
		select {
		case o, ok := <-r.out:
			if !ok {
				return fmt.Errorf("the stream ended before %s", what)
			}
			switch {
			case o.Signal == exec.StreamSignalClose:
				r.closed[o.SessionID] = true
			case o.Signal == exec.StreamSignalNone && o.Target == exec.StreamTargetStdOut:
				r.stdout[o.SessionID] = append(r.stdout[o.SessionID], o.Data...)
			}
		case <-r.ctx.Done():
			return fmt.Errorf("waiting for %s: %w", what, r.ctx.Err())
		}
	}
	return nil
}

// endStream ends the stream and waits until the client has closed its output. Unless abort is set it does what a
// host does when it is done: it half-closes the stream and waits for the plugin to end the call, and the end of the
// call is the last step. With abort it cancels the call instead, and records nothing. It is safe to call twice.
func (r *runner) endStream(abort bool) error {
	if r.out == nil {
		return nil
	}
	r.closeIn.Do(func() { close(r.in) })
	if abort {
		r.cancel()
	}
	for {
		select {
		case _, ok := <-r.out:
			if ok {
				continue
			}
			r.out = nil
			if abort {
				return nil
			}
			calls := r.rec.drain()
			if len(calls) != 1 {
				return fmt.Errorf("the end of the stream made %d legacy RPC records, want 1", len(calls))
			}
			r.steps = append(r.steps, newStep("end-stream", calls[0], nil))
			return nil
		case <-r.ctx.Done():
			// The scenario's deadline is spent, but the client may still hold a frame it sends on its unbuffered
			// output: cancel the call and keep draining under a deadline of its own, so that the client's receiver
			// is released and closes the channel.
			r.cancel()
			r.drainAfterExpiry()
			return fmt.Errorf("waiting for the end of the stream: %w", r.ctx.Err())
		}
	}
}

// cleanupTimeout bounds the drain that follows the expiry of the scenario's context. Like the context it is a
// safety net, never the way a step is decided.
const cleanupTimeout = 10 * time.Second

// drainAfterExpiry reads the cancelled stream's output until the client closes it or the cleanup deadline passes.
func (r *runner) drainAfterExpiry() {
	if drainOutput(r.out) {
		r.out = nil
	}
}

// drainOutput reads a cancelled stream's output until the client closes it, which it reports, or the cleanup
// deadline passes.
func drainOutput(out chan exec.StreamOutput) (closed bool) {
	cleanup, stop := context.WithTimeout(context.Background(), cleanupTimeout)
	defer stop()
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return true
			}
		case <-cleanup.Done():
			return false
		}
	}
}

// ---------------------------------------------------------------------------
// Comparing transcripts
// ---------------------------------------------------------------------------

// Diff lists the differences between two transcripts, one line each. It is empty when they are equal.
func Diff(want, got *Transcript) []string {
	var d []string
	if len(want.Steps) != len(got.Steps) {
		d = append(d, fmt.Sprintf("steps: want %d, got %d", len(want.Steps), len(got.Steps)))
	}
	for i := range min(len(want.Steps), len(got.Steps)) {
		w, g := want.Steps[i], got.Steps[i]
		if w == g {
			continue
		}
		label := fmt.Sprintf("step %d (%s)", i, w.Name)
		d = diffString(d, label+" name", w.Name, g.Name)
		d = diffString(d, label+" method", w.Method, g.Method)
		d = diffMessage(d, label+" request", w.Request, g.Request)
		d = diffMessage(d, label+" response", w.Response, g.Response)
		d = diffString(d, label+" code", w.Code, g.Code)
		d = diffString(d, label+" status message", w.Message, g.Message)
		d = diffString(d, label+" error", w.Error, g.Error)
	}
	d = diffMessages(d, "sent", want.Sent, got.Sent)
	d = diffMessages(d, "data", want.Data, got.Data)
	d = diffMessages(d, "signals", want.Signals, got.Signals)
	return d
}

func diffString(d []string, what, want, got string) []string {
	if want == got {
		return d
	}
	return append(d, fmt.Sprintf("%s: want %q, got %q", what, want, got))
}

func diffMessage(d []string, what string, want, got Message) []string {
	if want == got {
		return d
	}
	return append(d, fmt.Sprintf("%s: want %s [%s], got %s [%s]", what, want.Text, want.Hex, got.Text, got.Hex))
}

func diffMessages(d []string, what string, want, got []Message) []string {
	if len(want) != len(got) {
		d = append(d, fmt.Sprintf("%s: want %d, got %d", what, len(want), len(got)))
	}
	for i := range min(len(want), len(got)) {
		d = diffMessage(d, fmt.Sprintf("%s[%d]", what, i), want[i], got[i])
	}
	return d
}

// ---------------------------------------------------------------------------
// The host
// ---------------------------------------------------------------------------

// Host is a plugin process launched with go-plugin, with its exec and lifecycle capabilities dispensed. The
// connection carries the Recorder, and the SDK's own client interceptors as a real host's does.
type Host struct {
	// Exec is the exec capability the SDK's client built for the connection: its legacy lane, and, in a build that
	// has them, its other lanes.
	Exec exec.Provider
	// Recorder records the legacy RPCs of the connection.
	Recorder *Recorder

	// StopGrace is how long Stop waits for the graceful shutdown of the plugin before it kills the process; zero
	// means a default that only a hung plugin reaches.
	StopGrace time.Duration

	client    *plugin.Client
	lifecycle *lifecycle.Client
	// cmd is the command go-plugin started: its Process is the handle of the original child, which stays that child
	// when its process id is reused after it exited.
	cmd *osexec.Cmd
}

// Launch starts the plugin binary, which must serve the helper manifest (see PluginID), and dispenses its exec and
// lifecycle capabilities. The caller must Stop the host.
func Launch(binary, id string) (*Host, error) {
	return LaunchCommand(osexec.Command(binary), id)
}

// LaunchCommand is Launch for a command that is not just a binary (a test's helper process, for instance).
func LaunchCommand(cmd *osexec.Cmd, id string) (*Host, error) {
	rec := NewRecorder()
	meta := config.PluginMeta{ID: id}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: meta.GenerateHandshakeConfig(),
		VersionedPlugins: map[int]plugin.PluginSet{
			sdk.CurrentProtocolVersion: {
				"exec":      &exec.Plugin{},
				"lifecycle": &lifecycle.Plugin{},
			},
		},
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		GRPCDialOptions:  slices.Concat(sdk.GRPCDialOptions(), rec.DialOptions()),
		Logger:           hclog.NewNullLogger(),
		StartTimeout:     time.Minute,
	})
	host := &Host{Recorder: rec, client: client, cmd: cmd}
	protocol, err := client.Client()
	if err != nil {
		host.Stop()
		return nil, fmt.Errorf("start plugin %s: %w", cmd.Path, err)
	}
	execCapability, err := protocol.Dispense("exec")
	if err != nil {
		host.Stop()
		return nil, fmt.Errorf("dispense exec: %w", err)
	}
	provider, ok := execCapability.(exec.Provider)
	if !ok {
		host.Stop()
		return nil, fmt.Errorf("the exec capability is a %T, not an exec.Provider", execCapability)
	}
	lifecycleCapability, err := protocol.Dispense("lifecycle")
	if err != nil {
		host.Stop()
		return nil, fmt.Errorf("dispense lifecycle: %w", err)
	}
	life, ok := lifecycleCapability.(*lifecycle.Client)
	if !ok {
		host.Stop()
		return nil, fmt.Errorf("the lifecycle capability is a %T, not a *lifecycle.Client", lifecycleCapability)
	}
	host.Exec, host.lifecycle = provider, life
	return host, nil
}

// Info asks the lifecycle service of the plugin who it is and what it serves.
func (h *Host) Info(ctx context.Context) (Lifecycle, error) {
	info, err := h.lifecycle.GetInfo(ctx)
	if err != nil {
		return Lifecycle{}, fmt.Errorf("lifecycle GetInfo: %w", err)
	}
	capabilities, err := h.lifecycle.GetCapabilities(ctx)
	if err != nil {
		return Lifecycle{}, fmt.Errorf("lifecycle GetCapabilities: %w", err)
	}
	return Lifecycle{
		PluginID:               info.GetPluginId(),
		Version:                info.GetVersion(),
		SDKProtocolVersion:     info.GetSdkProtocolVersion(),
		HostConnectionsVersion: info.GetHostConnectionsVersion(),
		Capabilities:           append([]string{}, capabilities.GetCapabilities()...),
	}, nil
}

// stopGrace bounds the graceful shutdown of a plugin, and killWait the wait that follows the kill. Like the context
// of a run they are safety nets that only fire on a hang: a healthy plugin exits within milliseconds of the
// shutdown request.
const (
	stopGrace = 20 * time.Second
	killWait  = 10 * time.Second
)

// PID is the process id of the plugin, or 0 when it is not running.
func (h *Host) PID() int {
	if reattach := h.client.ReattachConfig(); reattach != nil {
		return reattach.Pid
	}
	return 0
}

// Stop ends the plugin process and waits for it. It reports the process id and whether the process has exited; it
// is safe to call more than once.
//
// The plugin is asked to shut down, which is what a healthy child answers. go-plugin makes that request with the
// lifetime context of the child, which does not end while the child lives, so a child that stopped answering would
// block the request for ever: when the graceful shutdown has not finished within the grace period (StopGrace, or
// stopGrace) the host kills the child itself, which ends that context, and waits for the shutdown to return, which
// is when go-plugin has reaped the process.
func (h *Host) Stop() Child {
	var child Child
	child.PID = h.PID()

	grace := h.StopGrace
	if grace <= 0 {
		grace = stopGrace
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		h.client.Kill()
	}()
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	select {
	case <-stopped:
	case <-deadline.C:
		// The handle go-plugin started, never a lookup by PID: that finds whatever owns the id by now. Killing a
		// process that was already reaped fails with os.ErrProcessDone and touches nothing else.
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		reaped := time.NewTimer(killWait)
		defer reaped.Stop()
		select {
		case <-stopped:
		case <-reaped.C:
		}
	}
	child.Exited = h.client.Exited()
	return child
}
