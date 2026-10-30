package plugin

import (
 "testing"

 resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
 "google.golang.org/protobuf/encoding/protowire"
)

func TestRawActionRequiresExactlyOneTarget(t *testing.T) {
 descriptor := (&resourcepb.ActionInput{}).ProtoReflect().Descriptor()
 reference := protowire.AppendTag(nil, 2, protowire.BytesType)
 reference = protowire.AppendBytes(reference, nil)
 typeTarget := protowire.AppendTag(nil, 5, protowire.BytesType)
 typeTarget = protowire.AppendBytes(typeTarget, nil)
 for _, data := range [][]byte{reference, typeTarget} {
  if err := ValidateWire(data, descriptor); err != nil { t.Fatal(err) }
 }
 both := append(append([]byte{}, reference...), typeTarget...)
 for _, data := range [][]byte{nil, both} {
  if err := ValidateWire(data, descriptor); err == nil { t.Fatal("accepted ambiguous raw action target") }
 }
}
