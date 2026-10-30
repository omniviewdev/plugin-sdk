package plugin

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

func TestToolsV2RawWireAdmission(t *testing.T) {
	call := (&toolspb.CallToolRequest{}).ProtoReflect().Descriptor()
	annotations := (&toolspb.ToolAnnotations{}).ProtoReflect().Descriptor()
	if call.ParentFile().Package() != "omniview.sdk.tools.v2" {
		t.Fatal("tools namespace is not v2")
	}
	version := protowire.AppendTag(nil, 4, protowire.VarintType)
	version = protowire.AppendVarint(version, 2)
	route := protowire.AppendTag(nil, 1, protowire.BytesType)
	route = protowire.AppendString(route, "connection")
	duplicateRoute := append(append([]byte{}, route...), route...)
	nested := protowire.AppendTag(nil, 3, protowire.BytesType)
	nested = protowire.AppendBytes(nested, duplicateRoute)
	unknown := protowire.AppendTag(nil, 100, protowire.VarintType)
	unknown = protowire.AppendVarint(unknown, 0)
	invalidBool := protowire.AppendTag(nil, 1, protowire.VarintType)
	duplicateBool := protowire.AppendTag(nil, 1, protowire.VarintType)
	duplicateBool = protowire.AppendVarint(duplicateBool, 0)
	duplicateBool = append(duplicateBool, duplicateBool...)
	invalidBool = protowire.AppendVarint(invalidBool, 2)
	for _, tc := range []struct {
		name       string
		data       []byte
		descriptor protoreflect.MessageDescriptor
	}{
		{"duplicate version", append(append([]byte{}, version...), version...), call},
		{"duplicate route", nested, call},
		{"unknown field", unknown, call},
		{"invalid annotation bool", invalidBool, annotations},
		{"duplicate annotation bool", duplicateBool, annotations},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWire(tc.data, tc.descriptor); err == nil {
				t.Fatal("accepted malformed tools message")
			}
		})
	}
	for _, value := range []uint64{0, 1} {
		data := protowire.AppendTag(nil, 1, protowire.VarintType)
		data = protowire.AppendVarint(data, value)
		if err := ValidateWire(data, annotations); err != nil {
			t.Fatal(err)
		}
	}
}
