package plugin

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func TestRawActionExpectedDescriptorPresence(t *testing.T) {
	descriptor := (&resourcepb.ActionInput{}).ProtoReflect().Descriptor()
	target := protowire.AppendTag(nil, 5, protowire.BytesType)
	target = protowire.AppendBytes(target, nil)
	valid := []byte(`{"id":"run","scope":"type","streaming":false}`)
	frame := func(raw []byte) []byte {
		data := protowire.AppendTag(append([]byte{}, target...), 6, protowire.BytesType)
		return protowire.AppendBytes(data, raw)
	}
	if err := ValidateWire(frame(valid), descriptor); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`{}`), []byte(`{"id":"run","id":"other","scope":"type","streaming":false}`), []byte(`{"id":"run","scope":"type","streaming":false,"extra":0}`)} {
		if err := ValidateWire(frame(raw), descriptor); err == nil {
			t.Fatalf("accepted malformed expectation: %s", raw)
		}
	}
	duplicate := protowire.AppendTag(frame(valid), 6, protowire.BytesType)
	duplicate = protowire.AppendBytes(duplicate, valid)
	if err := ValidateWire(duplicate, descriptor); err == nil {
		t.Fatal("accepted duplicate expected descriptor")
	}
}
