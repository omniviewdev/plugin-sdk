package utils

import (
	"encoding/json"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ConvertInterfaceToAny encodes v as JSON inside a protobuf BytesValue envelope.
// Unsupported JSON values retain the legacy empty-body behavior; this is not a resource-value codec.
func ConvertInterfaceToAny(v any) (*anypb.Any, error) {
	anyValue := &anypb.Any{}
	bytes, _ := json.Marshal(v)
	bytesValue := &wrapperspb.BytesValue{
		Value: bytes,
	}
	err := anypb.MarshalFrom(anyValue, bytesValue, proto.MarshalOptions{})
	return anyValue, err
}

// ConvertAnyToInterface decodes a JSON BytesValue envelope into ordinary Go JSON values.
// Malformed envelopes or JSON return an error; JSON null produces a nil value without error.
func ConvertAnyToInterface(anyValue *anypb.Any) (any, error) {
	var value any
	bytesValue := &wrapperspb.BytesValue{}
	err := anypb.UnmarshalTo(anyValue, bytesValue, proto.UnmarshalOptions{})
	if err != nil {
		return value, err
	}
	uErr := json.Unmarshal(bytesValue.GetValue(), &value)
	if uErr != nil {
		return value, uErr
	}
	return value, nil
}
