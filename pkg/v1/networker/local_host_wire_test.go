package networker_test

import (
	"encoding/json"
	"slices"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

// localHostField is the number of PortForwardSession.local_host on the wire.
const localHostField protowire.Number = 10

func sessionStating(host string) *networker.PortForwardSession {
	return &networker.PortForwardSession{
		ID:             "session-1",
		Protocol:       networker.PortForwardProtocolTCP,
		State:          networker.SessionStateActive,
		ConnectionType: networker.PortForwardConnectionTypeStatic,
		Connection:     networker.PortForwardStaticConnection{Address: "10.0.0.1:80"},
		LocalHost:      host,
		LocalPort:      8080,
		RemotePort:     80,
	}
}

// fieldValues are the values of one field in an encoded message, in order.
func fieldValues(t *testing.T, encoded []byte, number protowire.Number) []string {
	t.Helper()
	var values []string
	for len(encoded) > 0 {
		field, kind, tagLength := protowire.ConsumeTag(encoded)
		if tagLength < 0 {
			t.Fatalf("malformed tag: %v", protowire.ParseError(tagLength))
		}
		encoded = encoded[tagLength:]
		valueLength := protowire.ConsumeFieldValue(field, kind, encoded)
		if valueLength < 0 {
			t.Fatalf("malformed value: %v", protowire.ParseError(valueLength))
		}
		if field == number && kind == protowire.BytesType {
			value, _ := protowire.ConsumeBytes(encoded)
			values = append(values, string(value))
		}
		encoded = encoded[valueLength:]
	}
	return values
}

// sessionBefore is the message as its first release had it: the descriptor of
// PortForwardSession without local_host, so that a frame can be written and read
// the way a plugin or a host that predates the field does.
func sessionBefore(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	file := protodesc.ToFileDescriptorProto(networkerpb.File_proto_v1_networker_networker_proto)
	for _, message := range file.GetMessageType() {
		if message.GetName() == "PortForwardSession" {
			message.Field = slices.DeleteFunc(message.Field, func(field *descriptorpb.FieldDescriptorProto) bool {
				return field.GetName() == "local_host"
			})
		}
	}

	built, err := protodesc.NewFile(file, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build the earlier descriptor: %v", err)
	}

	message := built.Messages().ByName("PortForwardSession")
	if message.Fields().ByNumber(protoreflect.FieldNumber(localHostField)) != nil {
		t.Fatal("the earlier descriptor still has local_host")
	}
	return message
}

// The stated host crosses the codec in both directions, and an empty one puts
// nothing on the wire: absence is how a frame says the forwarder did not say.
func TestSessionLocalHostCrossesTheProtoCodec(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "localhost", "0.0.0.0", "::", "fe80::1%en0", ""} {
		t.Run("host "+host, func(t *testing.T) {
			frame, err := sessionStating(host).ToProto()
			if err != nil {
				t.Fatalf("ToProto: %v", err)
			}
			if frame.GetLocalHost() != host {
				t.Fatalf("frame local_host = %q, want %q", frame.GetLocalHost(), host)
			}

			encoded, err := proto.Marshal(frame)
			if err != nil {
				t.Fatal(err)
			}
			wantOnWire := []string{host}
			if host == "" {
				wantOnWire = nil
			}
			if got := fieldValues(t, encoded, localHostField); !slices.Equal(got, wantOnWire) {
				t.Fatalf("local_host on the wire = %q, want %q", got, wantOnWire)
			}

			var received networkerpb.PortForwardSession
			if err := proto.Unmarshal(encoded, &received); err != nil {
				t.Fatal(err)
			}
			decoded, err := networker.NewPortForwardSessionFromProto(&received)
			if err != nil {
				t.Fatalf("NewPortForwardSessionFromProto: %v", err)
			}
			if decoded.LocalHost != host || decoded.LocalPort != 8080 || decoded.RemotePort != 80 || decoded.ID != "session-1" {
				t.Fatalf("decoded session = %+v, want local host %q, ports 8080 and 80", decoded, host)
			}
		})
	}
}

// New host, old plugin: a frame written before the field existed decodes with
// no local host, and its other fields are untouched. The host must treat it as
// unknown, never as loopback.
func TestSessionFromAPluginThatPredatesLocalHostHasNone(t *testing.T) {
	descriptor := sessionBefore(t)
	fields := descriptor.Fields()
	old := dynamicpb.NewMessage(descriptor)
	old.Set(fields.ByName("id"), protoreflect.ValueOfString("session-1"))
	old.Set(fields.ByName("local_port"), protoreflect.ValueOfInt32(8080))
	old.Set(fields.ByName("remote_port"), protoreflect.ValueOfInt32(80))

	encoded, err := proto.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}

	var received networkerpb.PortForwardSession
	if err := proto.Unmarshal(encoded, &received); err != nil {
		t.Fatalf("a new reader must accept an old frame: %v", err)
	}
	decoded, err := networker.NewPortForwardSessionFromProto(&received)
	if err != nil {
		t.Fatalf("NewPortForwardSessionFromProto: %v", err)
	}
	if decoded.LocalHost != "" || decoded.LocalPort != 8080 || decoded.RemotePort != 80 || decoded.ID != "session-1" {
		t.Fatalf("decoded session = %+v, want no local host and ports 8080 and 80", decoded)
	}
}

// Old host, new plugin: a reader that predates the field reads every field it
// knows, and the field it does not know is set aside, not an error.
func TestSessionWithALocalHostIsReadByAHostThatPredatesIt(t *testing.T) {
	frame, err := sessionStating("127.0.0.1").ToProto()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := proto.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}

	descriptor := sessionBefore(t)
	fields := descriptor.Fields()
	old := dynamicpb.NewMessage(descriptor)
	if err := proto.Unmarshal(encoded, old); err != nil {
		t.Fatalf("a reader that predates local_host must accept the frame: %v", err)
	}
	if got := old.Get(fields.ByName("id")).String(); got != "session-1" {
		t.Fatalf("id = %q, want session-1", got)
	}
	if got := old.Get(fields.ByName("local_port")).Int(); got != 8080 {
		t.Fatalf("local_port = %d, want 8080", got)
	}
	if got := old.Get(fields.ByName("remote_port")).Int(); got != 80 {
		t.Fatalf("remote_port = %d, want 80", got)
	}
	if got := fieldValues(t, old.GetUnknown(), localHostField); !slices.Equal(got, []string{"127.0.0.1"}) {
		t.Fatalf("the unknown fields of the old reader hold local_host %q, want 127.0.0.1", got)
	}
}

// JSON, which the host's own bindings read, says the host when it is stated and
// says nothing when it is not.
func TestSessionLocalHostInJSON(t *testing.T) {
	read := func(t *testing.T, host string) map[string]any {
		t.Helper()
		data, err := json.Marshal(sessionStating(host))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}

	if got := read(t, "::1")["local_host"]; got != "::1" {
		t.Fatalf("local_host = %v, want ::1", got)
	}
	fields := read(t, "")
	if _, stated := fields["local_host"]; stated || fields["local_port"] != float64(8080) {
		t.Fatalf("a session with no local host has JSON fields %v, want local_port and no local_host", fields)
	}
}
