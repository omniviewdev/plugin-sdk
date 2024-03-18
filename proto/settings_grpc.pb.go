// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.3.0
// - protoc             (unknown)
// source: proto/settings.proto

package proto

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

const (
	SettingsPlugin_ListSettings_FullMethodName = "/com.omniview.pluginsdk.SettingsPlugin/ListSettings"
	SettingsPlugin_GetSetting_FullMethodName   = "/com.omniview.pluginsdk.SettingsPlugin/GetSetting"
	SettingsPlugin_SetSetting_FullMethodName   = "/com.omniview.pluginsdk.SettingsPlugin/SetSetting"
	SettingsPlugin_SetSettings_FullMethodName  = "/com.omniview.pluginsdk.SettingsPlugin/SetSettings"
)

// SettingsPluginClient is the client API for SettingsPlugin service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type SettingsPluginClient interface {
	ListSettings(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*ListSettingsResponse, error)
	GetSetting(ctx context.Context, in *GetSettingRequest, opts ...grpc.CallOption) (*Setting, error)
	SetSetting(ctx context.Context, in *SetSettingRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
	SetSettings(ctx context.Context, in *SetSettingsRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
}

type settingsPluginClient struct {
	cc grpc.ClientConnInterface
}

func NewSettingsPluginClient(cc grpc.ClientConnInterface) SettingsPluginClient {
	return &settingsPluginClient{cc}
}

func (c *settingsPluginClient) ListSettings(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*ListSettingsResponse, error) {
	out := new(ListSettingsResponse)
	err := c.cc.Invoke(ctx, SettingsPlugin_ListSettings_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsPluginClient) GetSetting(ctx context.Context, in *GetSettingRequest, opts ...grpc.CallOption) (*Setting, error) {
	out := new(Setting)
	err := c.cc.Invoke(ctx, SettingsPlugin_GetSetting_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsPluginClient) SetSetting(ctx context.Context, in *SetSettingRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, SettingsPlugin_SetSetting_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *settingsPluginClient) SetSettings(ctx context.Context, in *SetSettingsRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, SettingsPlugin_SetSettings_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SettingsPluginServer is the server API for SettingsPlugin service.
// All implementations should embed UnimplementedSettingsPluginServer
// for forward compatibility
type SettingsPluginServer interface {
	ListSettings(context.Context, *emptypb.Empty) (*ListSettingsResponse, error)
	GetSetting(context.Context, *GetSettingRequest) (*Setting, error)
	SetSetting(context.Context, *SetSettingRequest) (*emptypb.Empty, error)
	SetSettings(context.Context, *SetSettingsRequest) (*emptypb.Empty, error)
}

// UnimplementedSettingsPluginServer should be embedded to have forward compatible implementations.
type UnimplementedSettingsPluginServer struct {
}

func (UnimplementedSettingsPluginServer) ListSettings(context.Context, *emptypb.Empty) (*ListSettingsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListSettings not implemented")
}
func (UnimplementedSettingsPluginServer) GetSetting(context.Context, *GetSettingRequest) (*Setting, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetSetting not implemented")
}
func (UnimplementedSettingsPluginServer) SetSetting(context.Context, *SetSettingRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetSetting not implemented")
}
func (UnimplementedSettingsPluginServer) SetSettings(context.Context, *SetSettingsRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetSettings not implemented")
}

// UnsafeSettingsPluginServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to SettingsPluginServer will
// result in compilation errors.
type UnsafeSettingsPluginServer interface {
	mustEmbedUnimplementedSettingsPluginServer()
}

func RegisterSettingsPluginServer(s grpc.ServiceRegistrar, srv SettingsPluginServer) {
	s.RegisterService(&SettingsPlugin_ServiceDesc, srv)
}

func _SettingsPlugin_ListSettings_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsPluginServer).ListSettings(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsPlugin_ListSettings_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsPluginServer).ListSettings(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsPlugin_GetSetting_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetSettingRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsPluginServer).GetSetting(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsPlugin_GetSetting_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsPluginServer).GetSetting(ctx, req.(*GetSettingRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsPlugin_SetSetting_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(SetSettingRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsPluginServer).SetSetting(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsPlugin_SetSetting_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsPluginServer).SetSetting(ctx, req.(*SetSettingRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SettingsPlugin_SetSettings_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(SetSettingsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SettingsPluginServer).SetSettings(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SettingsPlugin_SetSettings_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SettingsPluginServer).SetSettings(ctx, req.(*SetSettingsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// SettingsPlugin_ServiceDesc is the grpc.ServiceDesc for SettingsPlugin service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var SettingsPlugin_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "com.omniview.pluginsdk.SettingsPlugin",
	HandlerType: (*SettingsPluginServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "ListSettings",
			Handler:    _SettingsPlugin_ListSettings_Handler,
		},
		{
			MethodName: "GetSetting",
			Handler:    _SettingsPlugin_GetSetting_Handler,
		},
		{
			MethodName: "SetSetting",
			Handler:    _SettingsPlugin_SetSetting_Handler,
		},
		{
			MethodName: "SetSettings",
			Handler:    _SettingsPlugin_SetSettings_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "proto/settings.proto",
}