package exec

import (
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// A selection key that is refused is caller text: the refusal's local cause must not repeat it.
func TestARefusedSelectionNeverEchoesItsKeyOrValue(t *testing.T) {
	const canary = "CANARY-SELECTION-4c7e"
	tests := map[string]map[string]string{
		"key that is not a closed code":  {canary: "app"},
		"key with a control character":   {"k\n" + canary: "app"},
		"value with a control character": {"container": canary + "\n"},
		"value that is too long":         {"container": canary + strings.Repeat("v", maxSelectionValue)},
	}
	for name, selection := range tests {
		t.Run(name, func(t *testing.T) {
			refusal := validateSelection(selection)
			if refusal == nil {
				t.Fatal("the selection was accepted")
			}
			if refusal.Class != ErrorClassInvalidRequest {
				t.Errorf("class = %s, want %s", refusal.Class, ErrorClassInvalidRequest)
			}
			if strings.Contains(refusal.Error(), canary) || (refusal.Err != nil && strings.Contains(refusal.Err.Error(), canary)) {
				t.Errorf("the refusal repeats the caller's text: %v / %v", refusal, refusal.Err)
			}
		})
	}
}

// A choice is selectable only if its value is: an empty value is a value, bounded by the contract only by its
// length, so a selection of it is admitted.
func TestAnEmptySelectionValueIsAdmitted(t *testing.T) {
	if refusal := validateSelection(map[string]string{"container": ""}); refusal != nil {
		t.Errorf("an empty selection value was refused: %v", refusal)
	}
}

// nameValues returns the list of NameValue of the given name and value pairs, in order, with any name as often as it is
// given.
func nameValues(pairs ...string) []*execv2.NameValue {
	out := make([]*execv2.NameValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, &execv2.NameValue{Name: pairs[i], Value: pairs[i+1]})
	}
	return out
}

// rqtUnknownEncodings are fields a newer peer may send in one element of a list, written with protowire at the level
// of the element: every wire type, a number far beyond any message of the contract, and field 3, which is what the
// next version of a message that has two fields (an entry: name and value) adds. No message of the contract has any of
// them.
func rqtUnknownEncodings() map[string][]byte {
	return map[string][]byte{
		"a varint":                   protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1),
		"a fixed32":                  protowire.AppendFixed32(protowire.AppendTag(nil, 100, protowire.Fixed32Type), 1),
		"a fixed64":                  protowire.AppendFixed64(protowire.AppendTag(nil, 101, protowire.Fixed64Type), 1),
		"a length-delimited value":   rqtUnknownBytes("a field of a newer peer"),
		"the next field number":      protowire.AppendVarint(protowire.AppendTag(nil, 3, protowire.VarintType), 1),
		"a field number of 2^29 - 1": protowire.AppendVarint(protowire.AppendTag(nil, protowire.MaxValidNumber, protowire.VarintType), 1),
	}
}

// rqtVarint is the smallest unknown field.
var rqtVarint = rqtUnknownEncodings()["a varint"]

// rqtEntries returns n NameValue named prefix0, prefix1, ... Each of the elements in unknown carries the given
// unknown field.
func rqtEntries(prefix string, n int, unknown map[int][]byte) []*execv2.NameValue {
	out := make([]*execv2.NameValue, n)
	for i := range out {
		out[i] = &execv2.NameValue{Name: prefix + strconv.Itoa(i), Value: "v"}
		if raw, ok := unknown[i]; ok {
			out[i].ProtoReflect().SetUnknown(raw)
		}
	}
	return out
}

// rqtScopeEntries returns n ScopeEntry of distinct dimensions, the way rqtEntries returns NameValue.
func rqtScopeEntries(n int, unknown map[int][]byte) []*resourcepb.ScopeEntry {
	out := make([]*resourcepb.ScopeEntry, n)
	for i := range out {
		out[i] = &resourcepb.ScopeEntry{Dimension: "dim" + strconv.Itoa(i), Value: "v"}
		if raw, ok := unknown[i]; ok {
			out[i].ProtoReflect().SetUnknown(raw)
		}
	}
	return out
}

// wireTargetOf is the wire form of a canonical target of the given provider and type, with no scope.
func wireTargetOf(t *testing.T, provider, resourceType string, selection ...*execv2.NameValue) *execv2.TargetRef {
	t.Helper()
	target := rqtWireOfTarget(TargetRef{
		Reference: rqtGoTarget().Reference,
		Route:     rqtGoTarget().Route,
	}, nil)
	target.Reference.Address.Provider = provider
	target.Reference.Address.Type = resourceType
	target.Reference.Address.Scope = nil
	target.Selection = selection
	return target
}

// The lists of messages that a request holds, as a message that holds the list and nothing else, for the tests of the
// walk. build returns the message with n elements; the elements named in unknown carry an unknown field.
type rqtListShape struct {
	name  string
	build func(n int, unknown map[int][]byte) protoreflect.Message
}

func rqtMessageLists() []rqtListShape {
	return append(rqtExecLists(), rqtScopeLists()...)
}

// The lists of exec's own messages: the ones the exec list limit applies to.
func rqtExecLists() []rqtListShape {
	return []rqtListShape{
		{"the env of a Start", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.Start{Env: rqtEntries("E", n, u)}).ProtoReflect()
		}},
		{"the labels of a Start", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.Start{Labels: rqtEntries("k", n, u)}).ProtoReflect()
		}},
		{"the selection of a target", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.TargetRef{Selection: rqtEntries("opt", n, u)}).ProtoReflect()
		}},
		{"the env of a RunRequest", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.RunRequest{Env: rqtEntries("E", n, u)}).ProtoReflect()
		}},
		{"the labels of a RunRequest", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.RunRequest{Labels: rqtEntries("k", n, u)}).ProtoReflect()
		}},
	}
}

// The scope lists of the canonical reference of a target: the resource lane's own field, which its validators bound
// by their own policy and the exec list limit does not apply to.
func rqtScopeLists() []rqtListShape {
	return []rqtListShape{
		{"the scope of an address", func(n int, u map[int][]byte) protoreflect.Message {
			return (&resourcepb.ResourceAddress{Scope: rqtScopeEntries(n, u)}).ProtoReflect()
		}},
		// The lists of a request one, two, three and four messages deep: the scope of the address of the reference of
		// the target of a request.
		{"the scope of the reference of a Start", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.Start{Target: &execv2.TargetRef{Reference: &resourcepb.ResourceReference{
				Address: &resourcepb.ResourceAddress{Scope: rqtScopeEntries(n, u)},
			}}}).ProtoReflect()
		}},
		{"the scope of the reference of a RunRequest", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.RunRequest{Target: &execv2.TargetRef{Reference: &resourcepb.ResourceReference{
				Address: &resourcepb.ResourceAddress{Scope: rqtScopeEntries(n, u)},
			}}}).ProtoReflect()
		}},
		{"the scope of the reference of the start of an OpenRequest", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.OpenRequest{Kind: &execv2.OpenRequest_Start{Start: &execv2.Start{
				Target: &execv2.TargetRef{Reference: &resourcepb.ResourceReference{
					Address: &resourcepb.ResourceAddress{Scope: rqtScopeEntries(n, u)},
				}},
			}}}).ProtoReflect()
		}},
		{"the scope of the reference of an OptionsRequest", func(n int, u map[int][]byte) protoreflect.Message {
			return (&execv2.OptionsRequest{Target: &execv2.TargetRef{Reference: &resourcepb.ResourceReference{
				Address: &resourcepb.ResourceAddress{Scope: rqtScopeEntries(n, u)},
			}}}).ProtoReflect()
		}},
	}
}

// The walk that finds a field the build does not know must read every element of every list: an unknown field sits in
// one element, and a request is strict inside the elements of a list. Each shape is read with its unknown field in
// each position of lists of every short length, and in the first and the last element of a list at the bound of the
// longest a request may carry. A walk that does not enter a list, or enters it and stops early or starts late, passes
// none of these.
func TestHasUnknownFieldsReadsEveryElementOfEveryList(t *testing.T) {
	for _, shape := range rqtMessageLists() {
		t.Run(shape.name, func(t *testing.T) {
			for _, n := range []int{0, 1, 2, 3, 8, 1024} {
				require.False(t, hasUnknownFields(shape.build(n, nil)), "%d elements that know all their fields", n)
			}
			for n := 1; n <= 5; n++ {
				for position := range n {
					require.True(t, hasUnknownFields(shape.build(n, map[int][]byte{position: rqtVarint})),
						"a list of %d, the field in the element at %d", n, position)
				}
			}
			require.True(t, hasUnknownFields(shape.build(1024, map[int][]byte{0: rqtVarint})), "the first element of 1,024")
			require.True(t, hasUnknownFields(shape.build(1024, map[int][]byte{1023: rqtVarint})), "the last element of 1,024")
			require.True(t, hasUnknownFields(shape.build(1024, map[int][]byte{512: rqtVarint})), "the middle element of 1,024")
			require.True(t, hasUnknownFields(shape.build(3, map[int][]byte{0: rqtVarint, 1: rqtVarint, 2: rqtVarint})), "every element")
			require.True(t, hasUnknownFields(shape.build(4, map[int][]byte{1: rqtVarint, 3: rqtVarint})), "two elements")
			for encoding, raw := range rqtUnknownEncodings() {
				require.True(t, hasUnknownFields(shape.build(3, map[int][]byte{1: raw})), "%s in the middle element of 3", encoding)
			}
		})
	}
}

// The walk goes on after a list that knows all its fields, and before one: the list is one field among others.
func TestHasUnknownFieldsGoesOnAfterAndBeforeAList(t *testing.T) {
	for _, shape := range []struct {
		name string
		with func(unknown map[int][]byte, other func(*execv2.Start)) protoreflect.Message
	}{
		{"a list of entries", func(u map[int][]byte, other func(*execv2.Start)) protoreflect.Message {
			s := &execv2.Start{Env: rqtEntries("E", 3, u)}
			other(s)
			return s.ProtoReflect()
		}},
	} {
		t.Run(shape.name, func(t *testing.T) {
			// field 4 comes before the list (field 5), field 11 after it
			before := func(s *execv2.Start) { s.Size = &execv2.Size{Cols: 1, Rows: 1}; rqtSetUnknown(s.Size) }
			after := func(s *execv2.Start) { s.Integration = &execv2.IntegrationRequest{}; rqtSetUnknown(s.Integration) }
			require.True(t, hasUnknownFields(shape.with(nil, before)), "unknown before the list")
			require.True(t, hasUnknownFields(shape.with(nil, after)), "unknown after the list")
			require.True(t, hasUnknownFields(shape.with(map[int][]byte{2: rqtVarint}, after)), "unknown in the list and after it")
			require.False(t, hasUnknownFields(shape.with(nil, func(s *execv2.Start) { s.Size = &execv2.Size{Cols: 1, Rows: 1} })), "known on both sides")
		})
	}
}

// The shapes of a field that a walk must tell apart and never trip on: a message with nothing set, a list of scalars (it
// has no element to read), a list with an element that is nil in an in-process message, and a nil message.
func TestHasUnknownFieldsOfEveryShapeOfField(t *testing.T) {
	var typedNil *execv2.Start
	rows := []struct {
		name string
		m    protoreflect.Message
		want bool
	}{
		{"an empty message", (&execv2.Start{}).ProtoReflect(), false},
		{"a message that is nil", typedNil.ProtoReflect(), false},
		{"lists of scalars", (&execv2.Start{Argv: []string{"a", "b"}, Features: []execv2.Feature{1, 2, 99}}).ProtoReflect(), false},
		{"a list of one nil element", (&execv2.Start{Env: []*execv2.NameValue{nil}}).ProtoReflect(), false},
		{"nil elements around a known one", (&execv2.Start{Env: []*execv2.NameValue{nil, {Name: "A"}, nil}}).ProtoReflect(), false},
		{"nil elements around an element with an unknown field", (&execv2.Start{Env: []*execv2.NameValue{nil, rqtEntries("E", 1, map[int][]byte{0: rqtVarint})[0], nil}}).ProtoReflect(), true},
		{"a nil element of a nested list", (&resourcepb.ResourceAddress{Scope: []*resourcepb.ScopeEntry{nil}}).ProtoReflect(), false},
		{"a oneof arm that is set and known", (&execv2.OpenRequest{SessionId: "s", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: 1}}}).ProtoReflect(), false},
		{"a oneof arm with a nil message", (&execv2.OpenRequest{SessionId: "s", Kind: &execv2.OpenRequest_Start{}}).ProtoReflect(), false},
		{"an unknown field on the message itself", func() protoreflect.Message {
			s := &execv2.Start{Tty: true}
			rqtSetUnknown(s)
			return s.ProtoReflect()
		}(), true},
		{"an unknown field in a singular message", func() protoreflect.Message {
			s := &execv2.Start{Size: &execv2.Size{Cols: 1, Rows: 1}}
			rqtSetUnknown(s.Size)
			return s.ProtoReflect()
		}(), true},
		{"an unknown field in the arm of a oneof", func() protoreflect.Message {
			attach := &execv2.Attach{}
			rqtSetUnknown(attach)
			return (&execv2.OpenRequest{Kind: &execv2.OpenRequest_Attach{Attach: attach}}).ProtoReflect()
		}(), true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			require.Equal(t, row.want, hasUnknownFields(row.m))
		})
	}
}

// A map of messages is read by its values: a request has no map, and the walk is a walk of any message of the contract.
// The values of a Struct are messages.
func TestHasUnknownFieldsReadsTheValuesOfAMap(t *testing.T) {
	value := func(unknown bool) *structpb.Value {
		v := structpb.NewStringValue("v")
		if unknown {
			rqtSetUnknown(v)
		}
		return v
	}
	build := func(unknown ...bool) protoreflect.Message {
		fields := make(map[string]*structpb.Value, len(unknown))
		for i, u := range unknown {
			fields["f"+strconv.Itoa(i)] = value(u)
		}
		return (&structpb.Struct{Fields: fields}).ProtoReflect()
	}
	require.False(t, hasUnknownFields(build()), "no value")
	require.False(t, hasUnknownFields(build(false)), "one value")
	require.False(t, hasUnknownFields(build(false, false, false)), "three values that know all their fields")
	require.True(t, hasUnknownFields(build(true)), "the only value")
	for position := range 4 {
		unknown := make([]bool, 4)
		unknown[position] = true
		require.True(t, hasUnknownFields(build(unknown...)), "the value at %d of 4", position)
	}
	// A map of scalars has no message to read.
	require.False(t, hasUnknownFields((&execv2.TargetFacts{Selection: map[string]string{"a": "b", "c": "d"}}).ProtoReflect()))
	// A value that holds a list of messages holds them in turn: the walk goes through the map and then the list.
	list := &structpb.Value{Kind: &structpb.Value_ListValue{ListValue: &structpb.ListValue{Values: []*structpb.Value{value(false), value(true)}}}}
	require.True(t, hasUnknownFields((&structpb.Struct{Fields: map[string]*structpb.Value{"l": list}}).ProtoReflect()))
}

// rejectUnknownFields turns what the walk finds into the refusals of a strict request: a field this build does not
// know is unsupported, a list that no request could hold is invalid, and a refusal never quotes the request.
func TestRejectUnknownFields(t *testing.T) {
	const canary = "CANARY-UNKNOWN-7a40"
	t.Run("a message with no unknown field", func(t *testing.T) {
		require.Nil(t, rejectUnknownFields(rqtWireStart()))
		require.Nil(t, rejectUnknownFields(rqtWireRun()))
		require.Nil(t, rejectUnknownFields(&execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{}}}))
		require.Nil(t, rejectUnknownFields(&execv2.DescribeRequest{}))
	})
	t.Run("no message", func(t *testing.T) {
		require.Nil(t, rejectUnknownFields(nil))
		var typedNil *execv2.Start
		require.Nil(t, rejectUnknownFields(typedNil), "a message that is a nil pointer has nothing unknown")
	})
	t.Run("an unknown field is unsupported and the refusal never quotes the request", func(t *testing.T) {
		start := rqtWireStart()
		start.Env = []*execv2.NameValue{{Name: canary, Value: canary}}
		start.Env[0].ProtoReflect().SetUnknown(rqtUnknownBytes(canary))
		refusal := rejectUnknownFields(start)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		rqtRequireNoCanary(t, refusal, canary)
	})
	t.Run("a list longer than any request may carry is invalid, whatever else the request holds", func(t *testing.T) {
		start := rqtWireStart()
		start.Env = rqtEntries("E", 1025, map[int][]byte{0: rqtVarint})
		refusal := rejectUnknownFields(start)
		rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)
	})
	t.Run("a list at the longest a request may carry is read, and its unknown field is unsupported", func(t *testing.T) {
		start := rqtWireStart()
		start.Env = rqtEntries("E", 1024, map[int][]byte{1023: rqtVarint})
		rqtRequireRefusal(t, rejectUnknownFields(start), ErrorClassUnsupported, ErrorReasonUnspecified)
	})
	t.Run("a list at the longest a request may carry is not a refusal for its length", func(t *testing.T) {
		start := rqtWireStart()
		start.Env = rqtEntries("E", 1024, nil)
		require.Nil(t, rejectUnknownFields(start))
	})
}

// A list longer than any request may carry is refused without being read: the longest list of any request is an argv of
// 1,024 elements. The bound is the same for a list of scalars, a list of messages, a map, at any depth.
func TestHasLongListBoundsEveryListAtTheLongestARequestMayCarry(t *testing.T) {
	shapes := append([]rqtListShape{
		{"the argv of a Start", func(n int, _ map[int][]byte) protoreflect.Message {
			return (&execv2.Start{Argv: rqtRepeat(n, "a")}).ProtoReflect()
		}},
		{"the argv of a RunRequest", func(n int, _ map[int][]byte) protoreflect.Message {
			return (&execv2.RunRequest{Argv: rqtRepeat(n, "a")}).ProtoReflect()
		}},
		{"the features of a Start", func(n int, _ map[int][]byte) protoreflect.Message {
			features := make([]execv2.Feature, n)
			for i := range features {
				features[i] = execv2.Feature_FEATURE_MARKS
			}
			return (&execv2.Start{Features: features}).ProtoReflect()
		}},
		{"a map of scalars", func(n int, _ map[int][]byte) protoreflect.Message {
			selection := make(map[string]string, n)
			for i := range n {
				selection["s"+strconv.Itoa(i)] = "v"
			}
			return (&execv2.TargetFacts{Selection: selection}).ProtoReflect()
		}},
		{"a map of messages", func(n int, _ map[int][]byte) protoreflect.Message {
			fields := make(map[string]*structpb.Value, n)
			for i := range n {
				fields["f"+strconv.Itoa(i)] = structpb.NewStringValue("v")
			}
			return (&structpb.Struct{Fields: fields}).ProtoReflect()
		}},
		{"a list inside the value of a map", func(n int, _ map[int][]byte) protoreflect.Message {
			values := make([]*structpb.Value, n)
			for i := range values {
				values[i] = structpb.NewStringValue("v")
			}
			list := &structpb.Value{Kind: &structpb.Value_ListValue{ListValue: &structpb.ListValue{Values: values}}}
			return (&structpb.Struct{Fields: map[string]*structpb.Value{"l": list}}).ProtoReflect()
		}},
		{"a list inside an element of a list", func(n int, _ map[int][]byte) protoreflect.Message {
			values := make([]*structpb.Value, n)
			for i := range values {
				values[i] = structpb.NewStringValue("v")
			}
			inner := &structpb.Value{Kind: &structpb.Value_ListValue{ListValue: &structpb.ListValue{Values: values}}}
			return (&structpb.ListValue{Values: []*structpb.Value{structpb.NewNullValue(), inner}}).ProtoReflect()
		}},
	}, rqtExecLists()...)
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			require.False(t, hasLongList(shape.build(0, nil)), "no element")
			require.False(t, hasLongList(shape.build(1, nil)), "one element")
			require.False(t, hasLongList(shape.build(1023, nil)), "1,023 elements")
			require.False(t, hasLongList(shape.build(1024, nil)), "1,024 elements, the longest a request may carry")
			require.True(t, hasLongList(shape.build(1025, nil)), "1,025 elements")
			require.True(t, hasLongList(shape.build(100_000, nil)), "100,000 elements")
		})
	}

	t.Run("no message", func(t *testing.T) {
		var typedNil *execv2.Start
		require.False(t, hasLongList(typedNil.ProtoReflect()))
		require.False(t, hasLongList((&execv2.Start{}).ProtoReflect()))
		require.False(t, hasLongList((&execv2.OpenRequest{Kind: &execv2.OpenRequest_Start{}}).ProtoReflect()))
	})

	// The elements of a list that is too long are not read, and the elements of one that is not are read at most once:
	// what a request costs does not depend on the length of its lists beyond the bound.
	t.Run("a list beyond the bound costs the same whatever its length", func(t *testing.T) {
		short := (&execv2.Start{Env: rqtEntries("E", 1025, nil)}).ProtoReflect()
		long := (&execv2.Start{Env: rqtEntries("E", 100_000, nil)}).ProtoReflect()
		var found bool
		small := testing.AllocsPerRun(10, func() { found = hasLongList(short) })
		require.True(t, found)
		large := testing.AllocsPerRun(10, func() { found = hasLongList(long) })
		require.True(t, found)
		require.InDelta(t, small, large, 4, "a list of 100,000 elements was read")
	})
	t.Run("the walk stops at the first list that is too long", func(t *testing.T) {
		start := &execv2.Start{Env: rqtEntries("E", 1025, nil), Labels: rqtEntries("k", 1025, nil), Argv: rqtRepeat(1025, "a")}
		require.True(t, hasLongList(start.ProtoReflect()))
	})
}

// The exec list limit is the limit of exec's own lists. The scope of the canonical reference is the resource lane's
// field: its validators decide how many dimensions an address may have, and the exec walk neither bounds it nor reads
// it, so that a reference the resource lane accepts is not refused for the length of its scope.
func TestHasLongListLeavesTheScopeOfAReferenceToTheResourcePolicy(t *testing.T) {
	for _, shape := range rqtScopeLists() {
		t.Run(shape.name, func(t *testing.T) {
			for _, n := range []int{0, 1, 1024, 1025, 100_000} {
				require.False(t, hasLongList(shape.build(n, nil)), "%d scope dimensions", n)
			}
		})
	}

	t.Run("a long scope does not hide a long exec list", func(t *testing.T) {
		start := &execv2.Start{
			Target: &execv2.TargetRef{Reference: &resourcepb.ResourceReference{
				Address: &resourcepb.ResourceAddress{Scope: rqtScopeEntries(1025, nil)},
			}},
			Env: rqtEntries("E", 1025, nil),
		}
		require.True(t, hasLongList(start.ProtoReflect()))
		start.Env = rqtEntries("E", 1024, nil)
		require.False(t, hasLongList(start.ProtoReflect()))
		start.Argv = rqtRepeat(1025, "a")
		require.True(t, hasLongList(start.ProtoReflect()))
	})

	t.Run("the strict walk admits a reference of 1,025 scope dimensions", func(t *testing.T) {
		start := rqtWireStart()
		start.Target.Reference.Address.Scope = rqtScopeEntries(1025, nil)
		require.Nil(t, rejectUnknownFields(start))
		run := rqtWireRun()
		run.Target.Reference.Address.Scope = rqtScopeEntries(1025, nil)
		require.Nil(t, rejectUnknownFields(run))

		// The resource validators accept such a reference, and so does admission: the target is admitted whole.
		target, err := targetRefToProto(rqtGoTarget())
		require.NoError(t, err)
		target.Reference.Address.Scope = rqtScopeEntries(1025, nil)
		_, err = resourcev2.FromProtoResourceReference(target.Reference)
		require.NoError(t, err)
		ref, _, refusal := admitTargetRef(target, false)
		require.Nil(t, refusal)
		require.Len(t, ref.Reference.Address.Scope, 1025)
	})

	t.Run("an unknown field in the scope is still refused", func(t *testing.T) {
		start := rqtWireStart()
		start.Target.Reference.Address.Scope = rqtScopeEntries(2000, map[int][]byte{1999: rqtVarint})
		refusal := rejectUnknownFields(start)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
	})

	t.Run("an env of 1,025 entries is still refused before it is read, with a long scope beside it", func(t *testing.T) {
		start := rqtWireStart()
		start.Target.Reference.Address.Scope = rqtScopeEntries(1025, nil)
		start.Env = rqtEntries("E", 1025, nil)
		rqtRequireRefusal(t, rejectUnknownFields(start), ErrorClassInvalidRequest, ErrorReasonUnspecified)
	})
}

func TestNameValueMapReadsTheListAsAMapAndRefusesAnyNameTwice(t *testing.T) {
	got, refusal := nameValueMap(nameValues("A", "1", "B", "2", "C", ""), "env", 10)
	if refusal != nil || !maps.Equal(got, map[string]string{"A": "1", "B": "2", "C": ""}) {
		t.Fatalf("got %v, %v", got, refusal)
	}
	if got, refusal := nameValueMap(nil, "env", 10); got != nil || refusal != nil {
		t.Errorf("an empty list is a nil map and no refusal: %v, %v", got, refusal)
	}

	const canary = "CANARY-NAME-5d1e"
	duplicates := map[string][]*execv2.NameValue{
		"the same value":               nameValues(canary, "x", canary, "x"),
		"another value":                nameValues(canary, "first", canary, "second"),
		"not next to each other":       nameValues(canary, "x", "B", "y", canary, "z"),
		"three times":                  nameValues(canary, "x", canary, "y", canary, "z"),
		"an empty name twice":          nameValues("", "x", "", "y"),
		"two nil entries, both empty":  {nil, nil},
		"a nil entry and an empty one": {nil, {Value: "x"}},
	}
	for name, list := range duplicates {
		t.Run("refuses "+name, func(t *testing.T) {
			got, refusal := nameValueMap(list, "env", 10)
			if refusal == nil {
				t.Fatalf("a name that appears twice was admitted: %v", got)
			}
			if got != nil {
				t.Errorf("a refused list returned a map: %v", got)
			}
			if refusal.Class != ErrorClassInvalidRequest {
				t.Errorf("class = %s, want %s", refusal.Class, ErrorClassInvalidRequest)
			}
			if strings.Contains(refusal.Error(), canary) || (refusal.Err != nil && strings.Contains(refusal.Err.Error(), canary)) {
				t.Errorf("the refusal repeats the caller's name: %v / %v", refusal, refusal.Err)
			}
		})
	}

	t.Run("names that differ are two names, however alike", func(t *testing.T) {
		got, refusal := nameValueMap(nameValues("a", "1", "A", "2", "a ", "3", "b", "4"), "env", 10)
		require.Nil(t, refusal)
		require.Equal(t, map[string]string{"a": "1", "A": "2", "a ": "3", "b": "4"}, got)
	})
	t.Run("a name that is not followed by a value reads as an empty value", func(t *testing.T) {
		got, refusal := nameValueMap([]*execv2.NameValue{{Name: "A"}}, "env", 10)
		require.Nil(t, refusal)
		require.Equal(t, map[string]string{"A": ""}, got)
	})
}

// The count is read first: a list over its bound is refused without being walked, and a list at its bound is not.
func TestNameValueMapBoundsTheListBeforeReadingIt(t *testing.T) {
	list := func(n int) []*execv2.NameValue {
		out := make([]*execv2.NameValue, n)
		for i := range out {
			out[i] = &execv2.NameValue{Name: "n" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)), Value: "v"}
		}
		return out
	}
	if got, refusal := nameValueMap(list(8), "selection", 8); refusal != nil || len(got) != 8 {
		t.Errorf("a list at its bound: %d names, %v", len(got), refusal)
	}
	if got, refusal := nameValueMap(list(9), "selection", 8); refusal == nil || refusal.Class != ErrorClassInvalidRequest || got != nil {
		t.Errorf("a list over its bound: %v, %v", got, refusal)
	}
	// A list over its bound that also repeats a name is refused for its size: the bound is checked before any entry.
	// The extra entry repeats the name of the first, so that a check that reads the entries before it counts them
	// would refuse the list for the repeated name, and not for its size.
	entries := list(8)
	over := append(entries, &execv2.NameValue{Name: entries[0].Name, Value: "again"})
	_, refusal := nameValueMap(over, "selection", 8)
	if refusal == nil || refusal.Class != ErrorClassInvalidRequest || !strings.Contains(refusal.Err.Error(), "exceed") {
		t.Errorf("a list over its bound that repeats a name is refused for something else: %v", refusal)
	}
}

// admitTargetRef reads the reference and the route with the resource lane's validators and the selection with the
// bounds of the contract: at most 8 entries, each name a closed code, no name twice, each value at most 256 bytes of
// plain text. It returns nothing of a request it refuses.
func TestAdmitTargetRef(t *testing.T) {
	invalid := ErrorClassInvalidRequest
	rows := []struct {
		name     string
		target   func() *execv2.TargetRef
		noSelect bool // Options: a selection is not used
		class    ErrorClass
	}{
		{name: "a target with a selection", target: rqtHandWireTarget},
		{name: "a target with no selection", target: func() *execv2.TargetRef { return wireTargetOf(t, "k8s", "pods") }},
		{name: "no target", target: func() *execv2.TargetRef { return nil }, class: invalid},
		{name: "an empty target", target: func() *execv2.TargetRef { return &execv2.TargetRef{} }, class: invalid},
		{name: "a target with no reference", target: func() *execv2.TargetRef {
			target := rqtHandWireTarget()
			target.Reference = nil
			return target
		}, class: invalid},
		{name: "a target with no route", target: func() *execv2.TargetRef {
			target := rqtHandWireTarget()
			target.Route = nil
			return target
		}, class: invalid},
		{name: "a route with no connection", target: func() *execv2.TargetRef {
			target := rqtHandWireTarget()
			target.Route.Connection = ""
			return target
		}, class: invalid},
		{name: "a reference with no provider", target: func() *execv2.TargetRef {
			target := rqtHandWireTarget()
			target.Reference.Address.Provider = ""
			return target
		}, class: invalid},
		{name: "a reference with a dimension twice", target: func() *execv2.TargetRef {
			target := rqtHandWireTarget()
			target.Reference.Address.Scope = []*resourcepb.ScopeEntry{{Dimension: "ns", Value: "a"}, {Dimension: "ns", Value: "b"}}
			return target
		}, class: invalid},
		{name: "a reference with no address", target: func() *execv2.TargetRef {
			target := rqtHandWireTarget()
			target.Reference.Address = nil
			return target
		}, class: invalid},
		{name: "a selection of 8 entries", target: func() *execv2.TargetRef {
			return wireTargetOf(t, "k8s", "pods", rqtEntries("opt", 8, nil)...)
		}},
		{name: "a selection of 9 entries", target: func() *execv2.TargetRef {
			return wireTargetOf(t, "k8s", "pods", rqtEntries("opt", 9, nil)...)
		}, class: invalid},
		{name: "a selection name twice", target: func() *execv2.TargetRef {
			return wireTargetOf(t, "k8s", "pods", nameValues("container", "app", "container", "other")...)
		}, class: invalid},
		{name: "a selection name that is not a closed code", target: func() *execv2.TargetRef {
			return wireTargetOf(t, "k8s", "pods", nameValues("Not A Code", "x")...)
		}, class: invalid},
		{name: "a selection value of 257 bytes", target: func() *execv2.TargetRef {
			return wireTargetOf(t, "k8s", "pods", nameValues("container", rqtBytes(257))...)
		}, class: invalid},
		{name: "a selection where none is used", target: rqtHandWireTarget, noSelect: true, class: invalid},
		{name: "no selection where none is used", target: func() *execv2.TargetRef { return wireTargetOf(t, "k8s", "pods") }, noSelect: true},
		{name: "a selection that is empty where none is used", target: func() *execv2.TargetRef {
			target := wireTargetOf(t, "k8s", "pods")
			target.Selection = []*execv2.NameValue{}
			return target
		}, noSelect: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			ref, selection, refusal := admitTargetRef(row.target(), !row.noSelect)
			if row.class != ErrorClassUnspecified {
				rqtRequireRefusal(t, refusal, row.class, ErrorReasonUnspecified)
				require.Zero(t, ref, "a refusal returns nothing of the target")
				require.Nil(t, selection, "a refusal returns nothing of the selection")
				return
			}
			require.Nil(t, refusal)
			wire := row.target()
			require.Equal(t, wire.GetReference().GetAddress().GetProvider(), ref.Reference.Address.Provider)
			require.Equal(t, wire.GetReference().GetAddress().GetType(), ref.Reference.Address.Type)
			require.Equal(t, wire.GetRoute().GetConnection(), ref.Route.Connection)
			want := map[string]string{}
			for _, entry := range wire.GetSelection() {
				want[entry.GetName()] = entry.GetValue()
			}
			if len(want) == 0 {
				require.Nil(t, selection, "no selection is a nil map")
				return
			}
			require.Equal(t, want, selection)
		})
	}
}

func TestAdmitTargetRefReadsTheSelectionAsAMapAndRefusesADuplicate(t *testing.T) {
	ref, selection, refusal := admitTargetRef(wireTargetOf(t, "k8s", "pods", nameValues("container", "app", "revision", "7")...), true)
	if refusal != nil {
		t.Fatal(refusal)
	}
	if !maps.Equal(selection, map[string]string{"container": "app", "revision": "7"}) {
		t.Errorf("selection = %v", selection)
	}
	if ref.Reference.Address.Type != "pods" || ref.Route.Connection != "conn-1" {
		t.Errorf("ref = %+v", ref)
	}

	if _, selection, refusal := admitTargetRef(wireTargetOf(t, "k8s", "pods"), true); refusal != nil || selection != nil {
		t.Errorf("no selection: %v, %v", selection, refusal)
	}
	if _, _, refusal := admitTargetRef(wireTargetOf(t, "k8s", "pods", nameValues("container", "app", "container", "other")...), true); refusal == nil || refusal.Class != ErrorClassInvalidRequest {
		t.Errorf("the same option twice: %v", refusal)
	}
	// Options takes no selection: any entry is refused, a duplicate or not.
	if _, _, refusal := admitTargetRef(wireTargetOf(t, "k8s", "pods", nameValues("container", "app")...), false); refusal == nil || refusal.Class != ErrorClassInvalidRequest {
		t.Errorf("a selection where none is used: %v", refusal)
	}
	if _, selection, refusal := admitTargetRef(wireTargetOf(t, "k8s", "pods"), false); refusal != nil || selection != nil {
		t.Errorf("no selection where none is used: %v, %v", selection, refusal)
	}
	if _, _, refusal := admitTargetRef(wireTargetOf(t, "k8s", "pods", nameValues("Not A Code", "x")...), true); refusal == nil || refusal.Class != ErrorClassInvalidRequest {
		t.Errorf("a selection name that is not a closed code: %v", refusal)
	}
}

// admitTargetRef decodes the reference and the route; targetRefToProto is the way back, for a request a client builds.
// Both refuse what the resource lane's validators refuse and a route with no connection, and the wire form it gives
// admits again to the target it was given.
func TestTargetRefToProto(t *testing.T) {
	good := rqtGoTarget()
	wire, err := targetRefToProto(good)
	require.NoError(t, err)
	require.True(t, proto.Equal(rqtWireOfTarget(good, nil), wire), "got %v", wire)
	again, selection, refusal := admitTargetRef(wire, true)
	require.Nil(t, refusal)
	require.Nil(t, selection)
	require.Equal(t, good, again, "the wire form of a target admits to the target")

	noConnection := rqtGoTarget()
	noConnection.Route = resourcev2.AccessRoute{}
	for name, ref := range map[string]TargetRef{
		"a route with no connection": noConnection,
		"a reference with no provider": func() TargetRef {
			ref := rqtGoTarget()
			ref.Reference.Address.Provider = ""
			return ref
		}(),
		"a reference with no id": func() TargetRef {
			ref := rqtGoTarget()
			ref.Reference.Address.ID = ""
			return ref
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			wire, err := targetRefToProto(ref)
			require.Nil(t, wire)
			var refusal *Error
			require.ErrorAs(t, err, &refusal)
			rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)
		})
	}
}
