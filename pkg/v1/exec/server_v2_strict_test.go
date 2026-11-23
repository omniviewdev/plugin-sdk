package exec_test

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// An Options request is strict inside the elements of the lists it carries: an unknown field inside an entry of the
// selection or inside an entry of the scope of the reference is refused UNSUPPORTED, and the author's callback never
// runs. The requests of this file are written byte by byte with protowire, so that the unknown field is where the test
// says it is, inside the element, and the plugin reads it from the wire as a host's request arrives: what a message
// built in process and marshaled would give is not assumed.

// strictSafety bounds a call that the test expects to end: it never decides an outcome, it only turns a hang into a
// failure.
const strictSafety = 60 * time.Second

// strictDomain is the google.rpc.ErrorInfo domain of every exec v2 refusal, as the contract spells it.
const strictDomain = "omniview.sdk.exec.v2"

// strictCanary is in every name and value of the requests that are refused: no refusal may repeat it.
const strictCanary = "CANARY-STRICT-0b6f"

// strictPlugin is a plugin that serves one target whose Options callback counts its calls, with a connection registry
// the host's calls are admitted against.
type strictPlugin struct {
	connections *exectestkit.Connections
	raw         execv2.ExecClient
	calls       atomic.Int32
}

func newStrictPlugin(t *testing.T) *strictPlugin {
	t.Helper()
	p := new(strictPlugin)
	target := exectest.NewFakeTarget(
		exectest.FakeRouting("k8s", "pods"),
		exectest.FakeOptions(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
			p.calls.Add(1)
			return nil, nil
		}),
	)
	p.connections = exectestkit.NewConnectionsT(t, target.Info.Provider)
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: []exec.Target{target}, Connections: p.connections})
	p.raw = server.Raw()
	return p
}

// context is the context of a call as the SDK's client sends it: the call's connection and the catalog pin, serialized
// into the outgoing metadata, for a test that calls the generated client directly.
func (p *strictPlugin) context(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), strictSafety)
	t.Cleanup(cancel)
	ctx, err := resourceplugin.OutgoingSessionContext(p.connections.Context(ctx))
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// send sends the bytes as an OptionsRequest, exactly: the bytes are the unknown fields of an empty request, which the
// generated client marshals as they are.
func (p *strictPlugin) send(t *testing.T, raw []byte) (*execv2.OptionsResponse, error) {
	t.Helper()
	request := new(execv2.OptionsRequest)
	request.ProtoReflect().SetUnknown(raw)
	return p.raw.Options(p.context(t), request)
}

// strictField appends a length-delimited field to raw: how a message holds a field that is a message, and one element
// of a list of messages.
func strictField(raw []byte, number protowire.Number, value []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(raw, number, protowire.BytesType), value)
}

// strictEntry is one element of a list of entries with two fields (a NameValue, a ScopeEntry): field 1 and field 2, each
// a string, and the given unknown field when there is one.
func strictEntry(first, second string, unknown []byte) []byte {
	raw := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), first)
	raw = protowire.AppendString(protowire.AppendTag(raw, 2, protowire.BytesType), second)
	return append(raw, unknown...)
}

// strictOptionsRequest is the bytes of an OptionsRequest for the Pod "pod-1" of the plugin's connection, written with
// protowire: the target (field 1) holds the reference (field 1) with the given scope entries in its address, the route
// (field 2) and the given selection entries (field 3). The identity of the Pod is encoded by the SDK's own encoder, and
// everything else of the list is here.
func strictOptionsRequest(t *testing.T, p *strictPlugin, scope, selection [][]byte) []byte {
	t.Helper()
	target := p.connections.WireTarget(t, "pods", "pod-1")
	target.Reference.Address.Scope = nil
	address, err := proto.Marshal(target.Reference.Address)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range scope {
		address = strictField(address, 4, entry) // ResourceAddress.scope
	}
	reference := strictField(nil, 1, address) // ResourceReference.address
	route, err := proto.Marshal(target.Route)
	if err != nil {
		t.Fatal(err)
	}
	targetBytes := strictField(nil, 1, reference)    // TargetRef.reference
	targetBytes = strictField(targetBytes, 2, route) // TargetRef.route
	for _, entry := range selection {
		targetBytes = strictField(targetBytes, 3, entry) // TargetRef.selection
	}
	return strictField(nil, 1, targetBytes) // OptionsRequest.target
}

// strictUnknown are the fields of a newer peer that the elements of a list are given: a varint, a fixed32, a string, and
// field 3, which is what the next version of an entry of two fields adds.
func strictUnknown() map[string][]byte {
	return map[string][]byte{
		"a varint":              protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1),
		"a fixed32":             protowire.AppendFixed32(protowire.AppendTag(nil, 100, protowire.Fixed32Type), 1),
		"a string":              protowire.AppendString(protowire.AppendTag(nil, 1000, protowire.BytesType), strictCanary),
		"the next field number": protowire.AppendVarint(protowire.AppendTag(nil, 3, protowire.VarintType), 1),
	}
}

// strictErrorInfo is the exec-domain ErrorInfo of a refusal, or nil.
func strictErrorInfo(st *status.Status) *errdetails.ErrorInfo {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == strictDomain {
			return info
		}
	}
	return nil
}

// strictRequireRefusal requires err to be the typed refusal of an exec v2 call: the gRPC code and the class and reason
// that the contract gives it, and nothing of the request in the status.
func strictRequireRefusal(t *testing.T, err error, code codes.Code, class string) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || st.Code() != code {
		t.Fatalf("err = %v, want a status with code %s", err, code)
	}
	info := strictErrorInfo(st)
	if info == nil || info.GetReason() != class {
		t.Fatalf("the status is not the typed %s of the exec domain: %v", class, st.Details())
	}
	encoded, marshalErr := proto.Marshal(st.Proto())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), strictCanary) || strings.Contains(st.Message(), strictCanary) {
		t.Errorf("the refusal repeats the request: %q", encoded)
	}
}

func TestOptionsIsStrictInsideTheElementsOfAListOfTheRequest(t *testing.T) {
	p := newStrictPlugin(t)

	scope := func(unknownAt int, unknown []byte, n int) [][]byte {
		entries := make([][]byte, n)
		for i := range entries {
			var field []byte
			if i == unknownAt {
				field = unknown
			}
			entries[i] = strictEntry("dim"+strconv.Itoa(i), strictCanary, field)
		}
		return entries
	}
	selection := func(unknownAt int, unknown []byte, n int) [][]byte {
		entries := make([][]byte, n)
		for i := range entries {
			var field []byte
			if i == unknownAt {
				field = unknown
			}
			entries[i] = strictEntry("opt"+strconv.Itoa(i), strictCanary, field)
		}
		return entries
	}

	// The control: the request of a host, with the same two lists in it and every element knowing its fields. The scope
	// is admitted and the callback runs; the selection is refused for what it is, a selection where none is used.
	t.Run("a scope whose entries know all their fields is admitted and the callback runs", func(t *testing.T) {
		before := p.calls.Load()
		if _, err := p.send(t, strictOptionsRequest(t, p, scope(-1, nil, 3), nil)); err != nil {
			t.Fatalf("the control request was refused: %v", err)
		}
		if got := p.calls.Load(); got != before+1 {
			t.Fatalf("the callback ran %d times for the control request", got-before)
		}
	})
	// A scope beyond the exec list limit is the resource policy's to bound, not exec's: 1,025 entries with distinct short
	// dimensions and no unknown field are not refused for their number, and reach the author's callback.
	t.Run("a scope of 1,025 valid entries is admitted and the callback runs", func(t *testing.T) {
		before := p.calls.Load()
		if _, err := p.send(t, strictOptionsRequest(t, p, scope(-1, nil, 1025), nil)); err != nil {
			t.Fatalf("the request was refused: %v", err)
		}
		if got := p.calls.Load(); got != before+1 {
			t.Fatalf("the callback ran %d times for the request", got-before)
		}
	})
	before := p.calls.Load()

	// The bytes of every row below put the field where the test says: it decodes into the unknown fields of the entry.
	t.Run("the request decodes with the field inside the element", func(t *testing.T) {
		decoded := new(execv2.OptionsRequest)
		raw := strictOptionsRequest(t, p, scope(1, strictUnknown()["a varint"], 3), selection(2, strictUnknown()["a varint"], 3))
		if err := proto.Unmarshal(raw, decoded); err != nil {
			t.Fatal(err)
		}
		scopeEntries := decoded.GetTarget().GetReference().GetAddress().GetScope()
		selectionEntries := decoded.GetTarget().GetSelection()
		if len(scopeEntries) != 3 || len(selectionEntries) != 3 {
			t.Fatalf("scope %d, selection %d", len(scopeEntries), len(selectionEntries))
		}
		for i, entry := range scopeEntries {
			if got, want := len(entry.ProtoReflect().GetUnknown()) > 0, i == 1; got != want {
				t.Errorf("scope entry %d carries an unknown field: %v", i, got)
			}
		}
		for i, entry := range selectionEntries {
			if got, want := len(entry.ProtoReflect().GetUnknown()) > 0, i == 2; got != want {
				t.Errorf("selection entry %d carries an unknown field: %v", i, got)
			}
		}
		if len(decoded.GetTarget().ProtoReflect().GetUnknown()) > 0 || len(decoded.ProtoReflect().GetUnknown()) > 0 {
			t.Error("the field is not inside the element")
		}
	})

	for encoding, unknown := range strictUnknown() {
		for position := range 3 {
			t.Run("an unknown field inside an entry of the scope of the reference: "+encoding+", entry "+string(rune('0'+position)), func(t *testing.T) {
				_, err := p.send(t, strictOptionsRequest(t, p, scope(position, unknown, 3), nil))
				strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
			})
			t.Run("an unknown field inside an entry of the selection: "+encoding+", entry "+string(rune('0'+position)), func(t *testing.T) {
				_, err := p.send(t, strictOptionsRequest(t, p, nil, selection(position, unknown, 3)))
				strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
			})
		}
	}
	t.Run("an unknown field inside the only entry of the scope", func(t *testing.T) {
		_, err := p.send(t, strictOptionsRequest(t, p, scope(0, strictUnknown()["a varint"], 1), nil))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
	})
	t.Run("an unknown field inside the only entry of the selection", func(t *testing.T) {
		_, err := p.send(t, strictOptionsRequest(t, p, nil, selection(0, strictUnknown()["a varint"], 1)))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
	})
	t.Run("an unknown field in an entry of each", func(t *testing.T) {
		_, err := p.send(t, strictOptionsRequest(t, p, scope(2, strictUnknown()["a varint"], 3), selection(0, strictUnknown()["a varint"], 2)))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
	})

	// What the request is refused for when the field is not there: the same selection with every entry knowing its
	// fields is refused as what it is, so that it is the field that makes the refusal unsupported.
	t.Run("the same selection without the field is a selection where none is used", func(t *testing.T) {
		_, err := p.send(t, strictOptionsRequest(t, p, nil, selection(-1, nil, 3)))
		strictRequireRefusal(t, err, codes.InvalidArgument, "INVALID_REQUEST")
	})
	t.Run("a name twice in the selection is invalid, and an unknown field beside it is unsupported", func(t *testing.T) {
		twice := [][]byte{strictEntry("dup", strictCanary, nil), strictEntry("dup", strictCanary, nil)}
		_, err := p.send(t, strictOptionsRequest(t, p, nil, twice))
		strictRequireRefusal(t, err, codes.InvalidArgument, "INVALID_REQUEST")
		twice[1] = strictEntry("dup", strictCanary, strictUnknown()["a varint"])
		_, err = p.send(t, strictOptionsRequest(t, p, nil, twice))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
	})
	t.Run("a list longer than any request may carry is invalid, and is not read", func(t *testing.T) {
		// 20,000 entries, the first with an unknown field: if the list were read the refusal would be unsupported.
		_, err := p.send(t, strictOptionsRequest(t, p, nil, selection(0, strictUnknown()["a varint"], 20_000)))
		strictRequireRefusal(t, err, codes.InvalidArgument, "INVALID_REQUEST")
		// The scope of the reference is the resource lane's list, not bounded by the exec list limit: the long scope is
		// read like any other, and its unknown field is refused as one.
		_, err = p.send(t, strictOptionsRequest(t, p, scope(0, strictUnknown()["a varint"], 20_000), nil))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
		// at the longest the walk reads, 1,024 entries, the unknown field in the last is read and refused
		_, err = p.send(t, strictOptionsRequest(t, p, scope(1023, strictUnknown()["a varint"], 1024), nil))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
		_, err = p.send(t, strictOptionsRequest(t, p, nil, selection(1023, strictUnknown()["a varint"], 1024)))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
		// beyond the longest list exec reads, an unknown field in the last entry of 1,025, and in the last of 20,000, is
		// refused. The resource lane's own admission refuses it too, so a walk of exec that stops early is not seen
		// here: TestTheWalkReadsEveryEntryOfAScopeBeyondTheListLimit calls the walk on its own.
		_, err = p.send(t, strictOptionsRequest(t, p, scope(1024, strictUnknown()["a varint"], 1025), nil))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
		_, err = p.send(t, strictOptionsRequest(t, p, scope(19_999, strictUnknown()["a varint"], 20_000), nil))
		strictRequireRefusal(t, err, codes.Unimplemented, "UNSUPPORTED")
	})

	if got := p.calls.Load(); got != before {
		t.Fatalf("the author's callback ran %d times for requests that were refused before it", got-before)
	}
}
