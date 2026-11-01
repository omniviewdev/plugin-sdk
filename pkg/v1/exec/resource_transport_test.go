package exec

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

type resourceExecFixture struct {
	Provider

	calls, oldCalls int
	seen            SessionOptions
	failure         error
}

func (p *resourceExecFixture) CreateSession(_ *types.PluginContext, o SessionOptions) (*Session, error) {
	p.oldCalls++
	return &Session{ID: o.ID}, nil
}

func (p *resourceExecFixture) CreateResourceSession(_ *types.PluginContext, o SessionOptions) (*Session, error) {
	p.calls++
	p.seen = o
	return &Session{ID: o.ID}, p.failure
}

func execPipe(t *testing.T, s *grpc.Server) *grpc.ClientConn {
	t.Helper()
	l := bufconn.Listen(1 << 20)
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(l) }()
	t.Cleanup(func() {
		s.Stop()
		_ = l.Close()
		if serveErr := <-serveDone; serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			t.Errorf("serve: %v", serveErr)
		}
	})
	c, err := grpc.NewClient(
		"passthrough:///exec-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

type execRawCodec struct{ data []byte }

func (execRawCodec) Name() string { return "proto" }
func (c execRawCodec) Marshal(any) (mem.BufferSlice, error) {
	return mem.BufferSlice{mem.SliceBuffer(c.data)}, nil
}

func (execRawCodec) Unmarshal(b mem.BufferSlice, v any) error {
	return encoding.GetCodecV2("proto").Unmarshal(b, v)
}

func execBytes(n protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, n, protowire.BytesType), v)
}

func TestResourceExecActualGRPCLosslessAndOptionalBody(t *testing.T) {
	for _, size := range []int{0, 70 << 10, 3 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			p := &resourceExecFixture{}
			s := grpc.NewServer(resourceplugin.ServerOptions()...)
			RegisterExecPluginServer(s, &PluginServer{Impl: p})
			c := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, s))}
			opts := SessionOptions{ID: "same-session", ResourcePlugin: "opaque-plugin", ResourceKey: "opaque/type"}
			var native any
			if size > 0 {
				native = map[string]any{
					"min":          int64(math.MinInt64),
					"max":          uint64(math.MaxUint64),
					"negativeZero": math.Copysign(0, -1),
					"time":         time.Unix(-1, 123).UTC(),
					"container":    []any{nil, true, []any{"nested", uint64(255), math.Copysign(0, -1)}},
					"large":        strings.Repeat("x", size),
				}
				v, err := resourcev2.ValueFromNative(native)
				if err != nil {
					t.Fatal(err)
				}
				opts.ResourceData = &v
			}
			out, err := c.CreateSession(&types.PluginContext{}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if out.ID != opts.ID || p.calls != 1 || p.oldCalls != 0 {
				t.Fatalf("wrong RPC/session: %+v %+v", out, p)
			}
			if size > 0 {
				assertExecNativeBody(t, p.seen.ResourceData, native)
			}
		})
	}
}

func TestResourceExecActualGRPCErrors(t *testing.T) {
	for _, code := range []string{"INVALID_REQUEST", "RESET_REQUIRED", "FORBIDDEN", "UNAVAILABLE"} {
		t.Run(code, func(t *testing.T) {
			p := &resourceExecFixture{
				failure: fmt.Errorf("terminal: %w", resourcev2.NewError(code, "private provider detail")),
			}
			s := grpc.NewServer()
			RegisterExecPluginServer(s, &PluginServer{Impl: p})
			c := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, s))}
			_, err := c.CreateSession(&types.PluginContext{}, SessionOptions{ResourcePlugin: "p", ResourceKey: "t"})
			if resourcev2.ErrorCode(err) != code {
				t.Fatalf("class lost: %v", err)
			}
		})
	}
}

type oldExecServer struct {
	execpb.UnimplementedExecPluginServer

	calls int
}

func (s *oldExecServer) CreateSession(context.Context, *execpb.SessionOptions) (*execpb.CreateSessionResponse, error) {
	s.calls++
	return &execpb.CreateSessionResponse{}, nil
}

func TestResourceExecOldServerNeverFallsBack(t *testing.T) {
	s := grpc.NewServer()
	old := &oldExecServer{}
	execpb.RegisterExecPluginServer(s, old)
	c := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, s))}
	_, err := c.CreateSession(&types.PluginContext{}, SessionOptions{ResourcePlugin: "p", ResourceKey: "t"})
	if resourcev2.ErrorCode(err) != "UNSUPPORTED" || old.calls != 0 {
		t.Fatalf("fallback/class: %v calls %d", err, old.calls)
	}
}

func TestResourceExecRawAdmissionBeforeProvider(t *testing.T) {
	version := []byte{80, 2}
	entry := append(execBytes(1, []byte("key")), execBytes(2, []byte("value"))...)
	dup := append(execBytes(4, entry), execBytes(4, entry)...)
	cases := []struct {
		name, method, code string
		data               []byte
	}{
		{"reserved", "CreateSession", "UNSUPPORTED", execBytes(8, nil)},
		{"selector", "CreateSession", "UNSUPPORTED", execBytes(6, []byte("p"))},
		{"body", "CreateSession", "UNSUPPORTED", execBytes(9, []byte{74, 0})},
		{"version", "CreateResourceSession", "UNSUPPORTED", nil},
		{"duplicate", "CreateResourceSession", "INVALID_REQUEST", append(append([]byte{}, version...), version...)},
		{
			"oneof",
			"CreateResourceSession",
			"INVALID_REQUEST",
			append(append([]byte{}, version...), execBytes(9, []byte{10, 0, 26, 0})...),
		},
		{
			"utf8",
			"CreateResourceSession",
			"INVALID_REQUEST",
			append(append([]byte{}, version...), execBytes(6, []byte{255})...),
		},
		{"map", "CreateResourceSession", "INVALID_REQUEST", append(append([]byte{}, version...), dup...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &resourceExecFixture{}
			s := grpc.NewServer()
			RegisterExecPluginServer(s, &PluginServer{Impl: p})
			conn := execPipe(t, s)
			var out execpb.ResourceSessionResponse
			err := conn.Invoke(
				context.Background(),
				"/"+execpb.ExecPlugin_ServiceDesc.ServiceName+"/"+tc.method,
				&execpb.SessionOptions{},
				&out,
				grpc.ForceCodecV2(execRawCodec{tc.data}),
			)
			if resourcev2.ErrorCode(resourceplugin.ResourceDomainError(err)) != tc.code || p.calls+p.oldCalls != 0 {
				t.Fatalf("admission: %v calls %d", err, p.calls+p.oldCalls)
			}
		})
	}
}

func TestResourceExecRawResponsesRejectBeforeMaterialization(t *testing.T) {
	validSession := execBytes(1, []byte("never-materialize"))
	entry := append(execBytes(1, []byte("k")), execBytes(2, []byte("v"))...)
	duplicateMap := append(execBytes(4, entry), execBytes(4, entry)...)
	cases := [][]byte{
		execBytes(2, validSession),
		append([]byte{8, 2, 8, 2}, execBytes(2, validSession)...),
		append([]byte{8, 2}, execBytes(2, append(validSession, duplicateMap...))...),
		append([]byte{8, 2}, execBytes(2, execBytes(1, []byte{255}))...),
		append([]byte{8, 2}, execBytes(2, execBytes(6, []byte{16, 128, 148, 235, 220, 3}))...),
	}
	for i, data := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			p := &resourceExecFixture{}
			s := grpc.NewServer(grpc.ForceServerCodecV2(execRawCodec{data: data}))
			RegisterExecPluginServer(s, &PluginServer{Impl: p})
			c := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, s))}
			session, err := c.CreateSession(
				&types.PluginContext{},
				SessionOptions{ResourcePlugin: "p", ResourceKey: "t"},
			)
			if err == nil || session != nil {
				t.Fatalf("bad response materialized: %+v %v", session, err)
			}
		})
	}
}

func TestExecNonresourceActualGRPCCreateRemainsUtility(t *testing.T) {
	p := &resourceExecFixture{}
	s := grpc.NewServer()
	RegisterExecPluginServer(s, &PluginServer{Impl: p})
	c := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, s))}
	out, err := c.CreateSession(
		&types.PluginContext{},
		SessionOptions{ID: "utility", Params: map[string]string{"a": "1", "b": "2"}},
	)
	if err != nil || out.ID != "utility" || p.oldCalls != 1 || p.calls != 0 {
		t.Fatalf("utility changed: %+v %v", out, err)
	}
}

func TestResourceExecActualGRPCValueLimit(t *testing.T) {
	p := &resourceExecFixture{}
	s := grpc.NewServer(resourceplugin.ServerOptions()...)
	RegisterExecPluginServer(s, &PluginServer{Impl: p})
	conn := execPipe(t, s)
	value := execBytes(3, []byte(strings.Repeat("x", resourceplugin.MaxBodyBytes+1)))
	data := append([]byte{80, 2}, execBytes(9, value)...)
	var response execpb.ResourceSessionResponse
	err := conn.Invoke(
		context.Background(),
		"/"+execpb.ExecPlugin_ServiceDesc.ServiceName+"/CreateResourceSession",
		&execpb.SessionOptions{},
		&response,
		grpc.ForceCodecV2(execRawCodec{data: data}),
		grpc.MaxCallSendMsgSize(resourceplugin.MaxWireBytes),
	)
	if resourcev2.ErrorCode(resourceplugin.ResourceDomainError(err)) != "RESOURCE_LIMIT" || p.calls != 0 {
		t.Fatalf("limit lost: %v calls %d", err, p.calls)
	}
}

func TestResourceExecTransportLossStaysUntyped(t *testing.T) {
	p := &resourceExecFixture{}
	s := grpc.NewServer()
	RegisterExecPluginServer(s, &PluginServer{Impl: p})
	c := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, s))}
	s.Stop()
	_, err := c.CreateSession(&types.PluginContext{}, SessionOptions{ResourcePlugin: "p", ResourceKey: "t"})
	var typed *resourcev2.ResourceOperationError
	if err == nil || errors.As(err, &typed) || p.calls != 0 {
		t.Fatalf("transport loss gained provider class: %v", err)
	}
}

func assertExecNativeBody(t *testing.T, body *resourcev2.Value, want any) {
	t.Helper()
	if body == nil {
		t.Fatal("native body missing")
	}
	got, err := resourcev2.NativeFromValue(*body)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("lossy native body: %v", err)
	}
	fields, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("native body is not a map: %T", got)
	}
	negativeZero, ok := fields["negativeZero"].(float64)
	if !ok || !math.Signbit(negativeZero) {
		t.Fatal("top-level negative zero lost")
	}
	container, ok := fields["container"].([]any)
	if !ok || len(container) != 3 {
		t.Fatalf("native container changed: %#v", fields["container"])
	}
	nested, ok := container[2].([]any)
	if !ok || len(nested) != 3 {
		t.Fatalf("nested native container changed: %#v", container[2])
	}
	nestedZero, ok := nested[2].(float64)
	if !ok || !math.Signbit(nestedZero) {
		t.Fatal("nested negative zero lost")
	}
}

func TestExecSessionHandlerRejectsInvalidDynamicTypes(t *testing.T) {
	for _, resource := range []bool{false, true} {
		t.Run(strconv.FormatBool(resource), func(t *testing.T) {
			intercept := func(
				ctx context.Context,
				_ any,
				_ *grpc.UnaryServerInfo,
				handler grpc.UnaryHandler,
			) (any, error) {
				return handler(ctx, "not session options")
			}
			decode := func(any) error { return nil }
			method := "CreateSession"
			if resource {
				method = "CreateResourceSession"
			}
			handler := execSessionHandler(method)
			_, serverErr := handler(struct{}{}, context.Background(), decode, nil)
			if status.Code(serverErr) != codes.Internal {
				t.Fatalf("invalid server: got %v, want Internal", serverErr)
			}
			_, requestErr := handler(&PluginServer{}, context.Background(), decode, intercept)
			if status.Code(requestErr) != codes.Internal {
				t.Fatalf("invalid request: got %v, want Internal", requestErr)
			}
		})
	}
}
