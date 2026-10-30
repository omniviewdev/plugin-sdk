package networker

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func TestResourceNetworkStrictJSONCanonical(t *testing.T) {
	input := `{"connection_type":"RESOURCE","protocol":"TCP","connection":{"plugin_id":"opaque-provider","resource_key":"opaque/type","resource_id":"opaque::id/%2F","resource_data":{"kind":"map","value":{"min":{"kind":"int64","value":"-9223372036854775808"},"max":{"kind":"uint64","value":"18446744073709551615"},"preEpoch":{"kind":"timestamp","value":"1969-12-31T23:59:59.000000123Z"},"nested":{"kind":"list","value":[{"kind":"double","value":-0}]}}},"reference":{"address":{"provider":"opaque-provider","authority":"a","type":"opaque/type","scope":[],"id":"opaque::id/%2F"}},"route":{"connection":"trusted"}}}`
	var options PortForwardSessionOptions
	if err := json.Unmarshal([]byte(input), &options); err != nil {
		t.Fatal(err)
	}
	connection, ok := resourceConnection(options.Connection)
	if !ok || connection.ResourceData.Map["min"].Int != "-9223372036854775808" ||
		connection.ResourceData.Map["max"].Uint != "18446744073709551615" ||
		!math.Signbit(connection.ResourceData.Map["nested"].List[0].Double) {
		t.Fatal("JSON scalar loss")
	}
	if connection.Reference == nil || connection.Route == nil {
		t.Fatal("reference or route lost")
	}
	if err := validateResourceContext(
		&types.PluginContext{Connection: &types.Connection{ID: "other"}},
		options,
	); resourcev2.ErrorCode(
		err,
	) != "INVALID_REQUEST" {
		t.Fatalf("trusted route conflict: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(input, `"protocol":"TCP"`, `"protocol":"TCP","protocol":"TCP"`, 1),
		strings.Replace(input, `"protocol":"TCP"`, `"protocol":"TCP","unknown":true`, 1),
		strings.Replace(input, `"value":-0`, `"value":-0,"value":0`, 1),
		strings.Replace(input, `"resource_data":{"kind":"map"`, `"resource_data":{"unknown":true,"kind":"map"`, 1),
		input + ` {}`,
	} {
		if err := json.Unmarshal([]byte(bad), &options); err == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}

func TestResourceNetworkSnapshotDeepClone(t *testing.T) {
	body := resourcev2.Value{
		Kind: "map",
		Map: map[string]resourcev2.Value{
			"nested": {Kind: "list", List: []resourcev2.Value{{Kind: "string", String: "original"}}},
		},
	}
	token := "original"
	source := PortForwardResourceConnection{
		ResourceData: &body,
		Reference: &resourcev2.ResourceReference{
			Address: resourcev2.ResourceAddress{
				Scope: []resourcev2.ScopeEntry{{Dimension: "scope", Value: "original"}},
			},
			Incarnation: &token,
		},
		Route: &resourcev2.AccessRoute{Connection: "original"},
	}
	entry := sessionEntry{session: PortForwardSession{Connection: source}}
	copied, ok := entry.snapshot().Connection.(PortForwardResourceConnection)
	if !ok {
		t.Fatal("snapshot lost resource connection type")
	}
	nested := copied.ResourceData.Map["nested"]
	nested.List[0].String = "changed"
	copied.Reference.Address.Scope[0].Value = "changed"
	*copied.Reference.Incarnation = "changed"
	copied.Route.Connection = "changed"
	if source.ResourceData.Map["nested"].List[0].String != "original" ||
		source.Reference.Address.Scope[0].Value != "original" ||
		token != "original" ||
		source.Route.Connection != "original" {
		t.Fatal("snapshot aliases resource connection")
	}
}

func TestResourceNetworkAdaptersReturnErrors(t *testing.T) {
	bad := resourcev2.Value{Kind: "uint64", Uint: "18446744073709551616"}
	connection := PortForwardResourceConnection{ResourceData: &bad}
	if _, err := connection.ToProto(); err == nil {
		t.Fatal("invalid body became empty protobuf")
	}
	options := canonicalResourceOptions(&bad)
	if _, err := options.ToProto(); err == nil {
		t.Fatal("invalid body became empty options")
	}
	session := PortForwardSession{Connection: connection, ConnectionType: PortForwardConnectionTypeResource}
	if _, err := session.ToProto(); err == nil {
		t.Fatal("invalid body became empty session")
	}
}

func TestResourceNetworkPreflightBodyAndResponseFrameBounds(t *testing.T) {
	tooLarge := resourcev2.Value{Kind: "string", String: strings.Repeat("x", resourcev2.MaxDocumentBytes+1)}
	options := canonicalResourceOptions(&tooLarge)
	if _, err := options.ToProto(); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("body preflight: %v", err)
	}
	body := resourcev2.Value{Kind: "string", String: strings.Repeat("x", 12<<20)}
	connection, _ := resourceConnection(canonicalResourceOptions(&body).Connection)
	session := &PortForwardSession{
		Connection:     connection,
		ConnectionType: PortForwardConnectionTypeResource,
		Protocol:       PortForwardProtocolTCP,
		State:          SessionStateActive,
	}
	if err := preflightSessions(
		[]*PortForwardSession{session, session, session},
		false,
	); resourcev2.ErrorCode(
		err,
	) != "RESOURCE_LIMIT" {
		t.Fatalf("response frame preflight: %v", err)
	}
}
