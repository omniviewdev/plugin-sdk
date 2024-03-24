// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.33.0
// 	protoc        (unknown)
// source: proto/resource_informer.proto

package proto

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	_ "google.golang.org/protobuf/types/known/durationpb"
	_ "google.golang.org/protobuf/types/known/emptypb"
	structpb "google.golang.org/protobuf/types/known/structpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
	_ "google.golang.org/protobuf/types/known/wrapperspb"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type StartConnectionInformerRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Key        string `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
	Connection string `protobuf:"bytes,2,opt,name=connection,proto3" json:"connection,omitempty"`
}

func (x *StartConnectionInformerRequest) Reset() {
	*x = StartConnectionInformerRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_resource_informer_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *StartConnectionInformerRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*StartConnectionInformerRequest) ProtoMessage() {}

func (x *StartConnectionInformerRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_resource_informer_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use StartConnectionInformerRequest.ProtoReflect.Descriptor instead.
func (*StartConnectionInformerRequest) Descriptor() ([]byte, []int) {
	return file_proto_resource_informer_proto_rawDescGZIP(), []int{0}
}

func (x *StartConnectionInformerRequest) GetKey() string {
	if x != nil {
		return x.Key
	}
	return ""
}

func (x *StartConnectionInformerRequest) GetConnection() string {
	if x != nil {
		return x.Connection
	}
	return ""
}

type StopConnectionInformerRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Key        string `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
	Connection string `protobuf:"bytes,2,opt,name=connection,proto3" json:"connection,omitempty"`
}

func (x *StopConnectionInformerRequest) Reset() {
	*x = StopConnectionInformerRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_resource_informer_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *StopConnectionInformerRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*StopConnectionInformerRequest) ProtoMessage() {}

func (x *StopConnectionInformerRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_resource_informer_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use StopConnectionInformerRequest.ProtoReflect.Descriptor instead.
func (*StopConnectionInformerRequest) Descriptor() ([]byte, []int) {
	return file_proto_resource_informer_proto_rawDescGZIP(), []int{1}
}

func (x *StopConnectionInformerRequest) GetKey() string {
	if x != nil {
		return x.Key
	}
	return ""
}

func (x *StopConnectionInformerRequest) GetConnection() string {
	if x != nil {
		return x.Connection
	}
	return ""
}

type InformerEvent struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Key        string `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
	Connection string `protobuf:"bytes,2,opt,name=connection,proto3" json:"connection,omitempty"`
	Id         string `protobuf:"bytes,3,opt,name=id,proto3" json:"id,omitempty"`
	Namespace  string `protobuf:"bytes,4,opt,name=namespace,proto3" json:"namespace,omitempty"`
	// Types that are assignable to Action:
	//
	//	*InformerEvent_Add
	//	*InformerEvent_Update
	//	*InformerEvent_Delete
	Action isInformerEvent_Action `protobuf_oneof:"action"`
}

func (x *InformerEvent) Reset() {
	*x = InformerEvent{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_resource_informer_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *InformerEvent) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*InformerEvent) ProtoMessage() {}

func (x *InformerEvent) ProtoReflect() protoreflect.Message {
	mi := &file_proto_resource_informer_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use InformerEvent.ProtoReflect.Descriptor instead.
func (*InformerEvent) Descriptor() ([]byte, []int) {
	return file_proto_resource_informer_proto_rawDescGZIP(), []int{2}
}

func (x *InformerEvent) GetKey() string {
	if x != nil {
		return x.Key
	}
	return ""
}

func (x *InformerEvent) GetConnection() string {
	if x != nil {
		return x.Connection
	}
	return ""
}

func (x *InformerEvent) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *InformerEvent) GetNamespace() string {
	if x != nil {
		return x.Namespace
	}
	return ""
}

func (m *InformerEvent) GetAction() isInformerEvent_Action {
	if m != nil {
		return m.Action
	}
	return nil
}

func (x *InformerEvent) GetAdd() *InformerAddEvent {
	if x, ok := x.GetAction().(*InformerEvent_Add); ok {
		return x.Add
	}
	return nil
}

func (x *InformerEvent) GetUpdate() *InformerUpdateEvent {
	if x, ok := x.GetAction().(*InformerEvent_Update); ok {
		return x.Update
	}
	return nil
}

func (x *InformerEvent) GetDelete() *InformerDeleteEvent {
	if x, ok := x.GetAction().(*InformerEvent_Delete); ok {
		return x.Delete
	}
	return nil
}

type isInformerEvent_Action interface {
	isInformerEvent_Action()
}

type InformerEvent_Add struct {
	Add *InformerAddEvent `protobuf:"bytes,5,opt,name=add,proto3,oneof"`
}

type InformerEvent_Update struct {
	Update *InformerUpdateEvent `protobuf:"bytes,6,opt,name=update,proto3,oneof"`
}

type InformerEvent_Delete struct {
	Delete *InformerDeleteEvent `protobuf:"bytes,7,opt,name=delete,proto3,oneof"`
}

func (*InformerEvent_Add) isInformerEvent_Action() {}

func (*InformerEvent_Update) isInformerEvent_Action() {}

func (*InformerEvent_Delete) isInformerEvent_Action() {}

type InformerAddEvent struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Data *structpb.Struct `protobuf:"bytes,1,opt,name=data,proto3" json:"data,omitempty"`
}

func (x *InformerAddEvent) Reset() {
	*x = InformerAddEvent{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_resource_informer_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *InformerAddEvent) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*InformerAddEvent) ProtoMessage() {}

func (x *InformerAddEvent) ProtoReflect() protoreflect.Message {
	mi := &file_proto_resource_informer_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use InformerAddEvent.ProtoReflect.Descriptor instead.
func (*InformerAddEvent) Descriptor() ([]byte, []int) {
	return file_proto_resource_informer_proto_rawDescGZIP(), []int{3}
}

func (x *InformerAddEvent) GetData() *structpb.Struct {
	if x != nil {
		return x.Data
	}
	return nil
}

type InformerUpdateEvent struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	OldData *structpb.Struct `protobuf:"bytes,1,opt,name=old_data,json=oldData,proto3" json:"old_data,omitempty"`
	NewData *structpb.Struct `protobuf:"bytes,2,opt,name=new_data,json=newData,proto3" json:"new_data,omitempty"`
}

func (x *InformerUpdateEvent) Reset() {
	*x = InformerUpdateEvent{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_resource_informer_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *InformerUpdateEvent) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*InformerUpdateEvent) ProtoMessage() {}

func (x *InformerUpdateEvent) ProtoReflect() protoreflect.Message {
	mi := &file_proto_resource_informer_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use InformerUpdateEvent.ProtoReflect.Descriptor instead.
func (*InformerUpdateEvent) Descriptor() ([]byte, []int) {
	return file_proto_resource_informer_proto_rawDescGZIP(), []int{4}
}

func (x *InformerUpdateEvent) GetOldData() *structpb.Struct {
	if x != nil {
		return x.OldData
	}
	return nil
}

func (x *InformerUpdateEvent) GetNewData() *structpb.Struct {
	if x != nil {
		return x.NewData
	}
	return nil
}

type InformerDeleteEvent struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Data *structpb.Struct `protobuf:"bytes,1,opt,name=data,proto3" json:"data,omitempty"`
}

func (x *InformerDeleteEvent) Reset() {
	*x = InformerDeleteEvent{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_resource_informer_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *InformerDeleteEvent) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*InformerDeleteEvent) ProtoMessage() {}

func (x *InformerDeleteEvent) ProtoReflect() protoreflect.Message {
	mi := &file_proto_resource_informer_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use InformerDeleteEvent.ProtoReflect.Descriptor instead.
func (*InformerDeleteEvent) Descriptor() ([]byte, []int) {
	return file_proto_resource_informer_proto_rawDescGZIP(), []int{5}
}

func (x *InformerDeleteEvent) GetData() *structpb.Struct {
	if x != nil {
		return x.Data
	}
	return nil
}

var File_proto_resource_informer_proto protoreflect.FileDescriptor

var file_proto_resource_informer_proto_rawDesc = []byte{
	0x0a, 0x1d, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x72, 0x65, 0x73, 0x6f, 0x75, 0x72, 0x63, 0x65,
	0x5f, 0x69, 0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65, 0x72, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12,
	0x16, 0x63, 0x6f, 0x6d, 0x2e, 0x6f, 0x6d, 0x6e, 0x69, 0x76, 0x69, 0x65, 0x77, 0x2e, 0x70, 0x6c,
	0x75, 0x67, 0x69, 0x6e, 0x73, 0x64, 0x6b, 0x1a, 0x1c, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x73, 0x74, 0x72, 0x75, 0x63, 0x74, 0x2e,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x1b, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x65, 0x6d, 0x70, 0x74, 0x79, 0x2e, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x1a, 0x1e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x62, 0x75, 0x66, 0x2f, 0x64, 0x75, 0x72, 0x61, 0x74, 0x69, 0x6f, 0x6e, 0x2e, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x1a, 0x1f, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x62, 0x75, 0x66, 0x2f, 0x74, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x1a, 0x1e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x62, 0x75, 0x66, 0x2f, 0x77, 0x72, 0x61, 0x70, 0x70, 0x65, 0x72, 0x73, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x22, 0x52, 0x0a, 0x1e, 0x53, 0x74, 0x61, 0x72, 0x74, 0x43, 0x6f, 0x6e, 0x6e,
	0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x49, 0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65, 0x72, 0x52, 0x65,
	0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x10, 0x0a, 0x03, 0x6b, 0x65, 0x79, 0x18, 0x01, 0x20, 0x01,
	0x28, 0x09, 0x52, 0x03, 0x6b, 0x65, 0x79, 0x12, 0x1e, 0x0a, 0x0a, 0x63, 0x6f, 0x6e, 0x6e, 0x65,
	0x63, 0x74, 0x69, 0x6f, 0x6e, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0a, 0x63, 0x6f, 0x6e,
	0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x22, 0x51, 0x0a, 0x1d, 0x53, 0x74, 0x6f, 0x70, 0x43,
	0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x49, 0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65,
	0x72, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x10, 0x0a, 0x03, 0x6b, 0x65, 0x79, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x03, 0x6b, 0x65, 0x79, 0x12, 0x1e, 0x0a, 0x0a, 0x63, 0x6f,
	0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0a,
	0x63, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x22, 0xc5, 0x02, 0x0a, 0x0d, 0x49,
	0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65, 0x72, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x12, 0x10, 0x0a, 0x03,
	0x6b, 0x65, 0x79, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x03, 0x6b, 0x65, 0x79, 0x12, 0x1e,
	0x0a, 0x0a, 0x63, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x18, 0x02, 0x20, 0x01,
	0x28, 0x09, 0x52, 0x0a, 0x63, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x12, 0x0e,
	0x0a, 0x02, 0x69, 0x64, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x02, 0x69, 0x64, 0x12, 0x1c,
	0x0a, 0x09, 0x6e, 0x61, 0x6d, 0x65, 0x73, 0x70, 0x61, 0x63, 0x65, 0x18, 0x04, 0x20, 0x01, 0x28,
	0x09, 0x52, 0x09, 0x6e, 0x61, 0x6d, 0x65, 0x73, 0x70, 0x61, 0x63, 0x65, 0x12, 0x3c, 0x0a, 0x03,
	0x61, 0x64, 0x64, 0x18, 0x05, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x28, 0x2e, 0x63, 0x6f, 0x6d, 0x2e,
	0x6f, 0x6d, 0x6e, 0x69, 0x76, 0x69, 0x65, 0x77, 0x2e, 0x70, 0x6c, 0x75, 0x67, 0x69, 0x6e, 0x73,
	0x64, 0x6b, 0x2e, 0x49, 0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65, 0x72, 0x41, 0x64, 0x64, 0x45, 0x76,
	0x65, 0x6e, 0x74, 0x48, 0x00, 0x52, 0x03, 0x61, 0x64, 0x64, 0x12, 0x45, 0x0a, 0x06, 0x75, 0x70,
	0x64, 0x61, 0x74, 0x65, 0x18, 0x06, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x2b, 0x2e, 0x63, 0x6f, 0x6d,
	0x2e, 0x6f, 0x6d, 0x6e, 0x69, 0x76, 0x69, 0x65, 0x77, 0x2e, 0x70, 0x6c, 0x75, 0x67, 0x69, 0x6e,
	0x73, 0x64, 0x6b, 0x2e, 0x49, 0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65, 0x72, 0x55, 0x70, 0x64, 0x61,
	0x74, 0x65, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x48, 0x00, 0x52, 0x06, 0x75, 0x70, 0x64, 0x61, 0x74,
	0x65, 0x12, 0x45, 0x0a, 0x06, 0x64, 0x65, 0x6c, 0x65, 0x74, 0x65, 0x18, 0x07, 0x20, 0x01, 0x28,
	0x0b, 0x32, 0x2b, 0x2e, 0x63, 0x6f, 0x6d, 0x2e, 0x6f, 0x6d, 0x6e, 0x69, 0x76, 0x69, 0x65, 0x77,
	0x2e, 0x70, 0x6c, 0x75, 0x67, 0x69, 0x6e, 0x73, 0x64, 0x6b, 0x2e, 0x49, 0x6e, 0x66, 0x6f, 0x72,
	0x6d, 0x65, 0x72, 0x44, 0x65, 0x6c, 0x65, 0x74, 0x65, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x48, 0x00,
	0x52, 0x06, 0x64, 0x65, 0x6c, 0x65, 0x74, 0x65, 0x42, 0x08, 0x0a, 0x06, 0x61, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x22, 0x3f, 0x0a, 0x10, 0x49, 0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65, 0x72, 0x41, 0x64,
	0x64, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x12, 0x2b, 0x0a, 0x04, 0x64, 0x61, 0x74, 0x61, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x0b, 0x32, 0x17, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x53, 0x74, 0x72, 0x75, 0x63, 0x74, 0x52, 0x04, 0x64,
	0x61, 0x74, 0x61, 0x22, 0x7d, 0x0a, 0x13, 0x49, 0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65, 0x72, 0x55,
	0x70, 0x64, 0x61, 0x74, 0x65, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x12, 0x32, 0x0a, 0x08, 0x6f, 0x6c,
	0x64, 0x5f, 0x64, 0x61, 0x74, 0x61, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x17, 0x2e, 0x67,
	0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x53,
	0x74, 0x72, 0x75, 0x63, 0x74, 0x52, 0x07, 0x6f, 0x6c, 0x64, 0x44, 0x61, 0x74, 0x61, 0x12, 0x32,
	0x0a, 0x08, 0x6e, 0x65, 0x77, 0x5f, 0x64, 0x61, 0x74, 0x61, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b,
	0x32, 0x17, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62,
	0x75, 0x66, 0x2e, 0x53, 0x74, 0x72, 0x75, 0x63, 0x74, 0x52, 0x07, 0x6e, 0x65, 0x77, 0x44, 0x61,
	0x74, 0x61, 0x22, 0x42, 0x0a, 0x13, 0x49, 0x6e, 0x66, 0x6f, 0x72, 0x6d, 0x65, 0x72, 0x44, 0x65,
	0x6c, 0x65, 0x74, 0x65, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x12, 0x2b, 0x0a, 0x04, 0x64, 0x61, 0x74,
	0x61, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x17, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65,
	0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x53, 0x74, 0x72, 0x75, 0x63, 0x74,
	0x52, 0x04, 0x64, 0x61, 0x74, 0x61, 0x42, 0x09, 0x5a, 0x07, 0x2e, 0x2f, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_proto_resource_informer_proto_rawDescOnce sync.Once
	file_proto_resource_informer_proto_rawDescData = file_proto_resource_informer_proto_rawDesc
)

func file_proto_resource_informer_proto_rawDescGZIP() []byte {
	file_proto_resource_informer_proto_rawDescOnce.Do(func() {
		file_proto_resource_informer_proto_rawDescData = protoimpl.X.CompressGZIP(file_proto_resource_informer_proto_rawDescData)
	})
	return file_proto_resource_informer_proto_rawDescData
}

var file_proto_resource_informer_proto_msgTypes = make([]protoimpl.MessageInfo, 6)
var file_proto_resource_informer_proto_goTypes = []interface{}{
	(*StartConnectionInformerRequest)(nil), // 0: com.omniview.pluginsdk.StartConnectionInformerRequest
	(*StopConnectionInformerRequest)(nil),  // 1: com.omniview.pluginsdk.StopConnectionInformerRequest
	(*InformerEvent)(nil),                  // 2: com.omniview.pluginsdk.InformerEvent
	(*InformerAddEvent)(nil),               // 3: com.omniview.pluginsdk.InformerAddEvent
	(*InformerUpdateEvent)(nil),            // 4: com.omniview.pluginsdk.InformerUpdateEvent
	(*InformerDeleteEvent)(nil),            // 5: com.omniview.pluginsdk.InformerDeleteEvent
	(*structpb.Struct)(nil),                // 6: google.protobuf.Struct
}
var file_proto_resource_informer_proto_depIdxs = []int32{
	3, // 0: com.omniview.pluginsdk.InformerEvent.add:type_name -> com.omniview.pluginsdk.InformerAddEvent
	4, // 1: com.omniview.pluginsdk.InformerEvent.update:type_name -> com.omniview.pluginsdk.InformerUpdateEvent
	5, // 2: com.omniview.pluginsdk.InformerEvent.delete:type_name -> com.omniview.pluginsdk.InformerDeleteEvent
	6, // 3: com.omniview.pluginsdk.InformerAddEvent.data:type_name -> google.protobuf.Struct
	6, // 4: com.omniview.pluginsdk.InformerUpdateEvent.old_data:type_name -> google.protobuf.Struct
	6, // 5: com.omniview.pluginsdk.InformerUpdateEvent.new_data:type_name -> google.protobuf.Struct
	6, // 6: com.omniview.pluginsdk.InformerDeleteEvent.data:type_name -> google.protobuf.Struct
	7, // [7:7] is the sub-list for method output_type
	7, // [7:7] is the sub-list for method input_type
	7, // [7:7] is the sub-list for extension type_name
	7, // [7:7] is the sub-list for extension extendee
	0, // [0:7] is the sub-list for field type_name
}

func init() { file_proto_resource_informer_proto_init() }
func file_proto_resource_informer_proto_init() {
	if File_proto_resource_informer_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_proto_resource_informer_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*StartConnectionInformerRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_resource_informer_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*StopConnectionInformerRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_resource_informer_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*InformerEvent); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_resource_informer_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*InformerAddEvent); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_resource_informer_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*InformerUpdateEvent); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_resource_informer_proto_msgTypes[5].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*InformerDeleteEvent); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	file_proto_resource_informer_proto_msgTypes[2].OneofWrappers = []interface{}{
		(*InformerEvent_Add)(nil),
		(*InformerEvent_Update)(nil),
		(*InformerEvent_Delete)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_proto_resource_informer_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   6,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_proto_resource_informer_proto_goTypes,
		DependencyIndexes: file_proto_resource_informer_proto_depIdxs,
		MessageInfos:      file_proto_resource_informer_proto_msgTypes,
	}.Build()
	File_proto_resource_informer_proto = out.File
	file_proto_resource_informer_proto_rawDesc = nil
	file_proto_resource_informer_proto_goTypes = nil
	file_proto_resource_informer_proto_depIdxs = nil
}
