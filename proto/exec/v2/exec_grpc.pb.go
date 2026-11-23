// The exec lane of the Omniview plugin SDK: run a command in a target (a Pod, a container, a host, a VM)
// either interactively (Open: one stream per session) or as one bounded, non-interactive run (Run).
//
// This file is a public contract. A released message, field number, enum value or RPC never changes meaning and is
// never reused. The reviewed listing of the contract (proto/exec/v2/exec.contract.golden, which a test compares with
// this file's descriptor) and buf breaking against the last release tag in CI fail a change that is not additive.
//
// Conventions that hold for every message below:
//
//   - ABSENCE IS UNKNOWN, one rule for the whole file. A fact that a producer may not know is written so that "not
//     known" is a value of its own and never the zero value of its type: a bool fact is `optional bool`; a numeric
//     fact whose zero is a real value is `optional`; an enum fact has an UNSPECIFIED zero; a message fact is absent; a
//     list whose emptiness is a fact ("no signals") is wrapped in a message so that absent differs from empty. A field
//     that is none of these is one of: a request the host makes (its zero is the documented default); a limit or a
//     size whose zero is not a real value; an identifier or a sequence number; a counter the SDK itself observed (zero
//     is a count); or a field of an arm that exists only when the fact is known. The field's comment says which. A
//     consumer never turns "unknown" into false, zero, none or exit 0.
//   - An UNSPECIFIED enum value is "unknown", never the nearest named value. Where it has a documented safe reading
//     for a decision (CLOSE_MODE_UNSPECIFIED is read as GRACEFUL, MARK_TRUST_UNSPECIFIED as UNVERIFIED,
//     KILL_BEHAVIOR_UNSPECIFIED as "the process may survive") the enum's comment says so. A value that a reader does
//     not know (a newer peer) is read as UNSPECIFIED.
//   - Classes, reasons and facts cross this wire; free text does not. The host writes the words. The one bounded
//     exception, Failed.public_message, is optional, reviewed-classes-only and never authority.
//   - Requests (every message the host sends to ask or to start something, and the first frame of Open) are STRICT:
//     a plugin refuses a request that carries a field it does not know, at any depth and inside the elements of a
//     repeated message too (UNSUPPORTED), because a request field can change what a command may do. For that reason a
//     request carries no map: a name/value list is a repeated NameValue, whose encoding is identical to a map entry's
//     and whose unknown fields can be seen. A host therefore sends a field an older plugin may not know only after
//     DescribeResponse.features lists the feature that gates it. Frames and responses are LENIENT: a field or a
//     frame kind a reader does not know is ignored, and counted.
//   - Capabilities have two levels: the target kind's UPPER BOUND in Describe, and each session's EFFECTIVE set in its
//     facts, which may only narrow it (see Capabilities).
//   - A session has exactly one end, and a stream exactly one last frame (see ServerFrame and Ended).
//   - Names: a message or field that the Go author API also names carries the same word there (pkg/v1/exec).
//
// A REFUSAL is a gRPC status that the SDK sends itself, before it hands a command to the author's function: the answer
// to a request it did not accept, so nothing ran and a host may retry (Open, Run and every other RPC that starts or
// ends a command). It carries a google.rpc.ErrorInfo with domain "omniview.sdk.exec.v2", reason = the ErrorClass name
// without its prefix ("FORBIDDEN"), and metadata keys "reason" (the ErrorReason name without its prefix), "program",
// "selection_name", "selection_value" and "retry_after_ms" for the ErrorFacts that are set; a bounded
// google.rpc.LocalizedMessage (locale "en") carries Error.Message for a reviewed class. Options is a read, it starts no
// command: a failure of the author's callback is also a status (the classified class, INTERNAL when the callback broke
// the contract) and a host may retry it. EVERYTHING AFTER A COMMAND WAS HANDED OVER IS DATA, never a status: once the
// command was handed to the author's function, how it ended is part of the result, because the command may have run
// and may still be running. A session ends with Ended (an End: exited, signaled, closed, failed or lost); a run ends
// with RunResult (an Outcome: exited, signaled, timed out, cancelled, failed or lost), which keeps the output read so
// far and says whether the process may survive. A refusal establishes that nothing ran; whether a retry can succeed
// depends on its class and reason (CONSENT_REQUIRED, FORBIDDEN and INVALID_REQUEST stay refused until the request or
// the host's state changes; LIMIT, UNAVAILABLE and CONNECTION may pass), so a host retries a refusal only when its
// class and reason say a retry can help, and never a failed or lost end. A status that is not a refusal in this sense
// (the call was cancelled, its deadline expired, the connection broke, the plugin died, or any gRPC status without
// this ErrorInfo) can arrive after the command was handed over and says nothing about whether it ran: it never
// authorizes an automatic retry of a command.

// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.3.0
// - protoc             (unknown)
// source: proto/exec/v2/exec.proto

package execv2

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

const (
	Exec_Describe_FullMethodName     = "/omniview.sdk.exec.v2.Exec/Describe"
	Exec_Options_FullMethodName      = "/omniview.sdk.exec.v2.Exec/Options"
	Exec_Open_FullMethodName         = "/omniview.sdk.exec.v2.Exec/Open"
	Exec_Run_FullMethodName          = "/omniview.sdk.exec.v2.Exec/Run"
	Exec_RunStream_FullMethodName    = "/omniview.sdk.exec.v2.Exec/RunStream"
	Exec_ListSessions_FullMethodName = "/omniview.sdk.exec.v2.Exec/ListSessions"
	Exec_ReadOutput_FullMethodName   = "/omniview.sdk.exec.v2.Exec/ReadOutput"
	Exec_EndSession_FullMethodName   = "/omniview.sdk.exec.v2.Exec/EndSession"
)

// ExecClient is the client API for Exec service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type ExecClient interface {
	// Describe lists what this plugin can run commands in (the target kinds), what each kind can do at most and the
	// limits it enforces. Static: it needs no connection and returns the same answer for the life of the process.
	Describe(ctx context.Context, in *DescribeRequest, opts ...grpc.CallOption) (*DescribeResponse, error)
	// Options lists what a user can choose for one resource before running a command in it (for a Pod: its
	// containers, each with its state), so that a picker shows real states. Admitted like any resource utility
	// call: the caller's connection, the route and the catalog pin are checked before the plugin's callback runs.
	Options(ctx context.Context, in *OptionsRequest, opts ...grpc.CallOption) (*OptionsResponse, error)
	// Open is one interactive session: ONE stream per session at a time. The first client frame opens a new command or
	// attaches to an existing session. The last server frame of the stream is Ended (the session is over; a session has
	// at most one Ended, and none when it ended while no stream was attached: its end is the tombstone's) or Detached
	// (this stream left and the session lives on: the host asked, or another stream took the session over). Served when
	// DescribeResponse.features lists FEATURE_SESSION.
	Open(ctx context.Context, opts ...grpc.CallOption) (Exec_OpenClient, error)
	// Run is one bounded, non-interactive command with one answer. Once the command was handed to the target the SDK
	// answers with a RunResult whatever happened (an exit, a signal, a timeout, a cancel, a failure, a loss); the only
	// status the SDK itself sends is a refusal, when nothing ran. A call that is cancelled, whose deadline expires or
	// whose connection breaks before the RunResult arrives ends with a status of the transport's own, which says nothing
	// about whether the command ran and never authorizes a retry (see the file header). Served when
	// DescribeResponse.features lists FEATURE_RUN.
	Run(ctx context.Context, in *RunRequest, opts ...grpc.CallOption) (*RunResult, error)
	// RunStream is Run with its output as it happens; cancelling the call cancels the run. It ends with RunEnded under
	// the same rule. Served when DescribeResponse.features lists FEATURE_RUN_STREAM.
	RunStream(ctx context.Context, in *RunRequest, opts ...grpc.CallOption) (Exec_RunStreamClient, error)
	// ListSessions lists the plugin's sessions, including recently ended ones (tombstones). Served when
	// DescribeResponse.features lists FEATURE_SESSION_MANAGEMENT.
	ListSessions(ctx context.Context, in *ListSessionsRequest, opts ...grpc.CallOption) (*ListSessionsResponse, error)
	// ReadOutput is a cursor read of a session's replay ring. Served when DescribeResponse.features lists
	// FEATURE_SESSION_MANAGEMENT.
	ReadOutput(ctx context.Context, in *ReadOutputRequest, opts ...grpc.CallOption) (*ReadOutputResponse, error)
	// EndSession ends a session that has no attached stream. Served when DescribeResponse.features lists
	// FEATURE_SESSION_MANAGEMENT.
	EndSession(ctx context.Context, in *EndSessionRequest, opts ...grpc.CallOption) (*EndSessionResponse, error)
}

type execClient struct {
	cc grpc.ClientConnInterface
}

func NewExecClient(cc grpc.ClientConnInterface) ExecClient {
	return &execClient{cc}
}

func (c *execClient) Describe(ctx context.Context, in *DescribeRequest, opts ...grpc.CallOption) (*DescribeResponse, error) {
	out := new(DescribeResponse)
	err := c.cc.Invoke(ctx, Exec_Describe_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *execClient) Options(ctx context.Context, in *OptionsRequest, opts ...grpc.CallOption) (*OptionsResponse, error) {
	out := new(OptionsResponse)
	err := c.cc.Invoke(ctx, Exec_Options_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *execClient) Open(ctx context.Context, opts ...grpc.CallOption) (Exec_OpenClient, error) {
	stream, err := c.cc.NewStream(ctx, &Exec_ServiceDesc.Streams[0], Exec_Open_FullMethodName, opts...)
	if err != nil {
		return nil, err
	}
	x := &execOpenClient{stream}
	return x, nil
}

type Exec_OpenClient interface {
	Send(*ClientFrame) error
	Recv() (*ServerFrame, error)
	grpc.ClientStream
}

type execOpenClient struct {
	grpc.ClientStream
}

func (x *execOpenClient) Send(m *ClientFrame) error {
	return x.ClientStream.SendMsg(m)
}

func (x *execOpenClient) Recv() (*ServerFrame, error) {
	m := new(ServerFrame)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *execClient) Run(ctx context.Context, in *RunRequest, opts ...grpc.CallOption) (*RunResult, error) {
	out := new(RunResult)
	err := c.cc.Invoke(ctx, Exec_Run_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *execClient) RunStream(ctx context.Context, in *RunRequest, opts ...grpc.CallOption) (Exec_RunStreamClient, error) {
	stream, err := c.cc.NewStream(ctx, &Exec_ServiceDesc.Streams[1], Exec_RunStream_FullMethodName, opts...)
	if err != nil {
		return nil, err
	}
	x := &execRunStreamClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type Exec_RunStreamClient interface {
	Recv() (*RunEvent, error)
	grpc.ClientStream
}

type execRunStreamClient struct {
	grpc.ClientStream
}

func (x *execRunStreamClient) Recv() (*RunEvent, error) {
	m := new(RunEvent)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *execClient) ListSessions(ctx context.Context, in *ListSessionsRequest, opts ...grpc.CallOption) (*ListSessionsResponse, error) {
	out := new(ListSessionsResponse)
	err := c.cc.Invoke(ctx, Exec_ListSessions_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *execClient) ReadOutput(ctx context.Context, in *ReadOutputRequest, opts ...grpc.CallOption) (*ReadOutputResponse, error) {
	out := new(ReadOutputResponse)
	err := c.cc.Invoke(ctx, Exec_ReadOutput_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *execClient) EndSession(ctx context.Context, in *EndSessionRequest, opts ...grpc.CallOption) (*EndSessionResponse, error) {
	out := new(EndSessionResponse)
	err := c.cc.Invoke(ctx, Exec_EndSession_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExecServer is the server API for Exec service.
// All implementations should embed UnimplementedExecServer
// for forward compatibility
type ExecServer interface {
	// Describe lists what this plugin can run commands in (the target kinds), what each kind can do at most and the
	// limits it enforces. Static: it needs no connection and returns the same answer for the life of the process.
	Describe(context.Context, *DescribeRequest) (*DescribeResponse, error)
	// Options lists what a user can choose for one resource before running a command in it (for a Pod: its
	// containers, each with its state), so that a picker shows real states. Admitted like any resource utility
	// call: the caller's connection, the route and the catalog pin are checked before the plugin's callback runs.
	Options(context.Context, *OptionsRequest) (*OptionsResponse, error)
	// Open is one interactive session: ONE stream per session at a time. The first client frame opens a new command or
	// attaches to an existing session. The last server frame of the stream is Ended (the session is over; a session has
	// at most one Ended, and none when it ended while no stream was attached: its end is the tombstone's) or Detached
	// (this stream left and the session lives on: the host asked, or another stream took the session over). Served when
	// DescribeResponse.features lists FEATURE_SESSION.
	Open(Exec_OpenServer) error
	// Run is one bounded, non-interactive command with one answer. Once the command was handed to the target the SDK
	// answers with a RunResult whatever happened (an exit, a signal, a timeout, a cancel, a failure, a loss); the only
	// status the SDK itself sends is a refusal, when nothing ran. A call that is cancelled, whose deadline expires or
	// whose connection breaks before the RunResult arrives ends with a status of the transport's own, which says nothing
	// about whether the command ran and never authorizes a retry (see the file header). Served when
	// DescribeResponse.features lists FEATURE_RUN.
	Run(context.Context, *RunRequest) (*RunResult, error)
	// RunStream is Run with its output as it happens; cancelling the call cancels the run. It ends with RunEnded under
	// the same rule. Served when DescribeResponse.features lists FEATURE_RUN_STREAM.
	RunStream(*RunRequest, Exec_RunStreamServer) error
	// ListSessions lists the plugin's sessions, including recently ended ones (tombstones). Served when
	// DescribeResponse.features lists FEATURE_SESSION_MANAGEMENT.
	ListSessions(context.Context, *ListSessionsRequest) (*ListSessionsResponse, error)
	// ReadOutput is a cursor read of a session's replay ring. Served when DescribeResponse.features lists
	// FEATURE_SESSION_MANAGEMENT.
	ReadOutput(context.Context, *ReadOutputRequest) (*ReadOutputResponse, error)
	// EndSession ends a session that has no attached stream. Served when DescribeResponse.features lists
	// FEATURE_SESSION_MANAGEMENT.
	EndSession(context.Context, *EndSessionRequest) (*EndSessionResponse, error)
}

// UnimplementedExecServer should be embedded to have forward compatible implementations.
type UnimplementedExecServer struct {
}

func (UnimplementedExecServer) Describe(context.Context, *DescribeRequest) (*DescribeResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Describe not implemented")
}
func (UnimplementedExecServer) Options(context.Context, *OptionsRequest) (*OptionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Options not implemented")
}
func (UnimplementedExecServer) Open(Exec_OpenServer) error {
	return status.Errorf(codes.Unimplemented, "method Open not implemented")
}
func (UnimplementedExecServer) Run(context.Context, *RunRequest) (*RunResult, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Run not implemented")
}
func (UnimplementedExecServer) RunStream(*RunRequest, Exec_RunStreamServer) error {
	return status.Errorf(codes.Unimplemented, "method RunStream not implemented")
}
func (UnimplementedExecServer) ListSessions(context.Context, *ListSessionsRequest) (*ListSessionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListSessions not implemented")
}
func (UnimplementedExecServer) ReadOutput(context.Context, *ReadOutputRequest) (*ReadOutputResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ReadOutput not implemented")
}
func (UnimplementedExecServer) EndSession(context.Context, *EndSessionRequest) (*EndSessionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method EndSession not implemented")
}

// UnsafeExecServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to ExecServer will
// result in compilation errors.
type UnsafeExecServer interface {
	mustEmbedUnimplementedExecServer()
}

func RegisterExecServer(s grpc.ServiceRegistrar, srv ExecServer) {
	s.RegisterService(&Exec_ServiceDesc, srv)
}

func _Exec_Describe_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(DescribeRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ExecServer).Describe(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: Exec_Describe_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ExecServer).Describe(ctx, req.(*DescribeRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Exec_Options_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(OptionsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ExecServer).Options(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: Exec_Options_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ExecServer).Options(ctx, req.(*OptionsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Exec_Open_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(ExecServer).Open(&execOpenServer{stream})
}

type Exec_OpenServer interface {
	Send(*ServerFrame) error
	Recv() (*ClientFrame, error)
	grpc.ServerStream
}

type execOpenServer struct {
	grpc.ServerStream
}

func (x *execOpenServer) Send(m *ServerFrame) error {
	return x.ServerStream.SendMsg(m)
}

func (x *execOpenServer) Recv() (*ClientFrame, error) {
	m := new(ClientFrame)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func _Exec_Run_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RunRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ExecServer).Run(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: Exec_Run_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ExecServer).Run(ctx, req.(*RunRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Exec_RunStream_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(RunRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(ExecServer).RunStream(m, &execRunStreamServer{stream})
}

type Exec_RunStreamServer interface {
	Send(*RunEvent) error
	grpc.ServerStream
}

type execRunStreamServer struct {
	grpc.ServerStream
}

func (x *execRunStreamServer) Send(m *RunEvent) error {
	return x.ServerStream.SendMsg(m)
}

func _Exec_ListSessions_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListSessionsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ExecServer).ListSessions(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: Exec_ListSessions_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ExecServer).ListSessions(ctx, req.(*ListSessionsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Exec_ReadOutput_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ReadOutputRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ExecServer).ReadOutput(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: Exec_ReadOutput_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ExecServer).ReadOutput(ctx, req.(*ReadOutputRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Exec_EndSession_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(EndSessionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ExecServer).EndSession(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: Exec_EndSession_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ExecServer).EndSession(ctx, req.(*EndSessionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// Exec_ServiceDesc is the grpc.ServiceDesc for Exec service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var Exec_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "omniview.sdk.exec.v2.Exec",
	HandlerType: (*ExecServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Describe",
			Handler:    _Exec_Describe_Handler,
		},
		{
			MethodName: "Options",
			Handler:    _Exec_Options_Handler,
		},
		{
			MethodName: "Run",
			Handler:    _Exec_Run_Handler,
		},
		{
			MethodName: "ListSessions",
			Handler:    _Exec_ListSessions_Handler,
		},
		{
			MethodName: "ReadOutput",
			Handler:    _Exec_ReadOutput_Handler,
		},
		{
			MethodName: "EndSession",
			Handler:    _Exec_EndSession_Handler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Open",
			Handler:       _Exec_Open_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
		{
			StreamName:    "RunStream",
			Handler:       _Exec_RunStream_Handler,
			ServerStreams: true,
		},
	},
	Metadata: "proto/exec/v2/exec.proto",
}
