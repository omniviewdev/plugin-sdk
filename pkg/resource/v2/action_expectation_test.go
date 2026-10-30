package resourcev2

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestActionExpectedDescriptorTransport(t *testing.T) {
	request := typeActionRequest()
	request.ExpectedDescriptor = &ActionDescriptor{
		ID:           "run",
		Scope:        ActionScopeType,
		ParamsSchema: (&Schema{Properties: map[string]SchemaProperty{"n": {Type: SchemaInteger}}, Required: []string{"n"}}).Ptr(),
	}
	wire, err := ToProtoActionRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := FromProtoActionRequest(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.ExpectedDescriptor, decoded.ExpectedDescriptor) {
		t.Fatal("lost expected descriptor")
	}
	rawJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var jsonDecoded ActionRequest
	if err := json.Unmarshal(rawJSON, &jsonDecoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.ExpectedDescriptor, jsonDecoded.ExpectedDescriptor) {
		t.Fatal("lost JSON expected descriptor")
	}
	for _, raw := range [][]byte{{}, []byte(`null`), []byte(`{}`), []byte(`{"id":"run","id":"other","scope":"type","streaming":false}`), []byte(`{"id":"run","scope":"type","streaming":false,"unknown":true}`)} {
		wire.ExpectedDescriptorJson = raw
		if _, err := FromProtoActionRequest(wire); err == nil {
			t.Fatalf("accepted malformed expectation: %s", raw)
		}
	}
}

func TestActionExpectedDescriptorJSONPresence(t *testing.T) {
	request := typeActionRequest()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["expectedDescriptor"] = json.RawMessage("null")
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ActionRequest
	if err := json.Unmarshal(raw, &decoded); err == nil {
		t.Fatal("accepted present null expectation")
	}
}
