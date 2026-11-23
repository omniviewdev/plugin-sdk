package exec

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// A request carries its environment, its labels and its selection as lists of NameValue, so that an unknown field
// inside an entry can be seen and refused, and the admitted request holds them as maps. The reference of the target
// carries a list of its own, the scope of its address. The tests of this file put each of these lists in each place
// of the plugin that reads it (the Start of an Open or alone, the RunRequest, the OptionsRequest: five lists, four ways
// in) and pin what changes with a list: an unknown field inside an element is refused UNSUPPORTED, in any element; a
// name that appears twice is refused INVALID_REQUEST, never resolved; and the first comes before the second.
// What the functions of admission_v2.go do with a list on their own is in admission_v2_test.go.

// rqtSlots are the lists a request holds, as pointers to the fields of a message: the three lists of NameValue and the
// scope of the reference of the target. A nil pointer is a list the request does not have.
type rqtSlots struct {
	env, labels, selection *[]*execv2.NameValue
	scope                  *[]*resourcepb.ScopeEntry
}

// rqtEntryPoint is a way into the plugin for a request: a valid request of one message type, the lists it holds and
// the function that admits it.
type rqtEntryPoint struct {
	name  string
	valid func() proto.Message
	empty func() proto.Message
	slots func(proto.Message) rqtSlots
	// admit runs the entry point on the request and returns its refusal, or nil when it admits.
	admit func(t *testing.T, request proto.Message) *Error
	// keepsLocalCause is false for an entry point that answers with a status: the local cause stays in the plugin.
	keepsLocalCause bool
}

func rqtEntryPoints() []rqtEntryPoint {
	startSlots := func(s *execv2.Start) rqtSlots {
		return rqtSlots{env: &s.Env, labels: &s.Labels, selection: &s.Target.Selection, scope: &s.Target.Reference.Address.Scope}
	}
	return []rqtEntryPoint{
		{
			name:  "Start",
			valid: func() proto.Message { return rqtWireStart() },
			empty: func() proto.Message { return new(execv2.Start) },
			slots: func(m proto.Message) rqtSlots { return startSlots(m.(*execv2.Start)) },
			admit: func(t *testing.T, m proto.Message) *Error {
				_, target := rqtTarget(t, nil)
				_, refusal := admitStart(m.(*execv2.Start), target, rqtServed())
				return refusal
			},
			keepsLocalCause: true,
		},
		{
			name: "Open",
			valid: func() proto.Message {
				return &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{Start: rqtWireStart()}}
			},
			empty: func() proto.Message { return new(execv2.OpenRequest) },
			slots: func(m proto.Message) rqtSlots { return startSlots(m.(*execv2.OpenRequest).GetStart()) },
			admit: func(t *testing.T, m proto.Message) *Error {
				reg, _ := rqtTarget(t, nil)
				_, _, refusal := admitOpenRequest(m.(*execv2.OpenRequest), reg, rqtServed())
				return refusal
			},
			keepsLocalCause: true,
		},
		{
			name:  "Run",
			valid: func() proto.Message { return rqtWireRun() },
			empty: func() proto.Message { return new(execv2.RunRequest) },
			slots: func(m proto.Message) rqtSlots {
				r := m.(*execv2.RunRequest)
				return rqtSlots{env: &r.Env, labels: &r.Labels, selection: &r.Target.Selection, scope: &r.Target.Reference.Address.Scope}
			},
			admit: func(t *testing.T, m proto.Message) *Error {
				reg, _ := rqtTarget(t, nil)
				_, _, refusal := admitRunRequest(m.(*execv2.RunRequest), reg, rqtServed())
				return refusal
			},
			keepsLocalCause: true,
		},
		{
			// Options is the one request that takes no selection, and the one whose answer is a status. A request that is
			// admitted goes on to the connection lease, like every Options call whatever the target (this one has no
			// Options callback). The call here carries no connection, so the lease refuses it, in the resource lane's own
			// words (RESET_REQUIRED): that is what this entry point reads as "admitted".
			name: "Options",
			valid: func() proto.Message {
				target := rqtHandWireTarget()
				target.Selection = nil
				return &execv2.OptionsRequest{Target: target}
			},
			empty: func() proto.Message { return new(execv2.OptionsRequest) },
			slots: func(m proto.Message) rqtSlots {
				target := m.(*execv2.OptionsRequest).Target
				return rqtSlots{selection: &target.Selection, scope: &target.Reference.Address.Scope}
			},
			admit: func(t *testing.T, m proto.Message) *Error {
				reg, _ := rqtTarget(t, nil)
				connections := func() resourcev2.ConnectionContextProvider { return nil }
				_, err := newServerV2(nil, reg, connections).Options(context.Background(), m.(*execv2.OptionsRequest))
				if err == nil {
					return nil
				}
				decoded := errorFromStatus(context.Background(), err)
				var lease *resourcev2.ResourceOperationError
				if errors.As(decoded, &lease) {
					require.Equal(t, "RESET_REQUIRED", lease.Code, "the lease refused the call for a reason other than its missing connection")
					return nil
				}
				var refusal *Error
				require.ErrorAs(t, decoded, &refusal)
				return refusal
			},
		},
	}
}

// rqtList is one of the lists of a request, in the way rqtSlots names it.
type rqtList struct {
	name string
	// fill puts n elements in the list of a request, the ones named in unknown carrying the unknown field. It reports
	// false for a request that does not have the list.
	fill func(slots rqtSlots, n int, unknown map[int][]byte) bool
	// names puts the given NameValue in the list. It is nil for the scope, whose elements have no name to repeat.
	names func(slots rqtSlots, list []*execv2.NameValue) bool
	// invalid is an element that breaks the bound of the name of the list (an env name, a selection name that is not a
	// closed code), for the tests of what comes first.
	invalid *execv2.NameValue
	// overBound is the length at which the list breaks its own bound.
	overBound int
}

func rqtLists() []rqtList {
	nameValueList := func(name, prefix string, slot func(rqtSlots) *[]*execv2.NameValue, invalid *execv2.NameValue, overBound int) rqtList {
		return rqtList{
			name: name,
			fill: func(s rqtSlots, n int, unknown map[int][]byte) bool {
				p := slot(s)
				if p == nil {
					return false
				}
				*p = rqtEntries(prefix, n, unknown)
				return true
			},
			names: func(s rqtSlots, list []*execv2.NameValue) bool {
				p := slot(s)
				if p == nil {
					return false
				}
				*p = list
				return true
			},
			invalid:   invalid,
			overBound: overBound,
		}
	}
	return []rqtList{
		nameValueList("env", "E", func(s rqtSlots) *[]*execv2.NameValue { return s.env }, &execv2.NameValue{Name: "1bad", Value: "v"}, 257),
		nameValueList("labels", "k", func(s rqtSlots) *[]*execv2.NameValue { return s.labels }, &execv2.NameValue{Name: "k\x01", Value: "v"}, 17),
		nameValueList("selection", "opt", func(s rqtSlots) *[]*execv2.NameValue { return s.selection }, &execv2.NameValue{Name: "Not A Code", Value: "v"}, 9),
		{
			name: "scope",
			fill: func(s rqtSlots, n int, unknown map[int][]byte) bool {
				if s.scope == nil {
					return false
				}
				*s.scope = rqtScopeEntries(n, unknown)
				return true
			},
		},
	}
}

// rqtAdmitWith builds the valid request of an entry point, lets build change its lists, and admits it: as it is, or
// as the bytes of it that arrive and are decoded. It reports false when the request does not have the list.
func rqtAdmitWith(t *testing.T, ep rqtEntryPoint, afterTheWire bool, build func(rqtSlots) bool) (refusal *Error, has bool) {
	t.Helper()
	request := ep.valid()
	if !build(ep.slots(request)) {
		return nil, false
	}
	if afterTheWire {
		decoded := ep.empty()
		rqtThroughTheWire(t, request, decoded)
		request = decoded
	}
	return ep.admit(t, request), true
}

// rqtRequireClass requires refusal to be of the class, and, when the entry point keeps the local cause, to carry one.
func rqtRequireClass(t *testing.T, ep rqtEntryPoint, refusal *Error, class ErrorClass) {
	t.Helper()
	if class == ErrorClassUnspecified {
		require.Nil(t, refusal)
		return
	}
	require.NotNil(t, refusal, "want a %s refusal", class)
	require.Equal(t, class, refusal.Class)
	require.Equal(t, ErrorReasonUnspecified, refusal.Reason)
	if ep.keepsLocalCause {
		require.Error(t, refusal.Err, "a refusal keeps its local cause")
	}
}

// rqtCleanClass is what a list of elements that know all their fields comes to: admitted, except the selection of an
// Options request, which takes none.
func rqtCleanClass(ep rqtEntryPoint, list rqtList) ErrorClass {
	if ep.name == "Options" && list.name == "selection" {
		return ErrorClassInvalidRequest
	}
	return ErrorClassUnspecified
}

// A request is strict inside the elements of every list it carries: an unknown field in any element is refused
// UNSUPPORTED, in whichever element it sits, of whichever of the five lists, whichever way the request came in, as
// the message itself and as bytes decoded. The unknown field is built with protowire at the level of the element.
func TestEveryListOfARequestIsStrictInsideItsElements(t *testing.T) {
	for _, ep := range rqtEntryPoints() {
		for _, list := range rqtLists() {
			if !list.fill(ep.slots(ep.valid()), 1, nil) {
				continue
			}
			t.Run(ep.name+"/"+list.name, func(t *testing.T) {
				for _, afterTheWire := range []bool{false, true} {
					mode := map[bool]string{false: "in process", true: "after the wire"}[afterTheWire]
					admit := func(n int, unknown map[int][]byte) *Error {
						refusal, _ := rqtAdmitWith(t, ep, afterTheWire, func(s rqtSlots) bool { return list.fill(s, n, unknown) })
						return refusal
					}

					// the control: the same lists with every element knowing its fields
					for _, n := range []int{1, 2, 3} {
						rqtRequireClass(t, ep, admit(n, nil), rqtCleanClass(ep, list))
					}

					// an unknown field in each position of lists of one to four elements
					for n := 1; n <= 4; n++ {
						for position := range n {
							rqtRequireClass(t, ep, admit(n, map[int][]byte{position: rqtVarint}), ErrorClassUnsupported)
						}
					}
					// in every element, and in two of them
					rqtRequireClass(t, ep, admit(3, map[int][]byte{0: rqtVarint, 1: rqtVarint, 2: rqtVarint}), ErrorClassUnsupported)
					rqtRequireClass(t, ep, admit(4, map[int][]byte{0: rqtVarint, 3: rqtVarint}), ErrorClassUnsupported)
					// each kind of field, in the middle element
					for encoding, raw := range rqtUnknownEncodings() {
						refusal := admit(3, map[int][]byte{1: raw})
						require.NotNil(t, refusal, "%s, %s", encoding, mode)
						require.Equal(t, ErrorClassUnsupported, refusal.Class, "%s, %s", encoding, mode)
					}
					// the elements around it are all there: a list of the largest length the walk reads, with the field in
					// the last element and in the first
					rqtRequireClass(t, ep, admit(100, map[int][]byte{99: rqtVarint}), ErrorClassUnsupported)
					rqtRequireClass(t, ep, admit(100, map[int][]byte{0: rqtVarint}), ErrorClassUnsupported)
				}
			})
		}
	}
}

// The scope of the reference of a target is the resource lane's list: exec's list limit does not bound it, and the walk
// that refuses an unknown field reads every entry of it, not the first 1,024. The walk is called on its own here,
// because a request that goes on to the resource lane's validators is refused there for the same field too, which
// would hide a walk that stops early. A scope of 1,025 entries with no unknown field is not refused for its length.
func TestTheWalkReadsEveryEntryOfAScopeBeyondTheListLimit(t *testing.T) {
	for _, ep := range rqtEntryPoints() {
		if ep.slots(ep.valid()).scope == nil {
			continue
		}
		t.Run(ep.name, func(t *testing.T) {
			walk := func(afterTheWire bool, n int, unknown map[int][]byte) *Error {
				request := ep.valid()
				*ep.slots(request).scope = rqtScopeEntries(n, unknown)
				if afterTheWire {
					decoded := ep.empty()
					rqtThroughTheWire(t, request, decoded)
					request = decoded
				}
				return rejectUnknownFields(request)
			}
			for _, afterTheWire := range []bool{false, true} {
				require.Nil(t, walk(afterTheWire, 1025, nil), "a scope of 1,025 entries is not refused for its length")
				for _, c := range []struct {
					n        int
					position int
				}{{1025, 1024}, {1025, 1023}, {1025, 0}, {20_000, 19_999}, {20_000, 1024}} {
					refusal := walk(afterTheWire, c.n, map[int][]byte{c.position: rqtVarint})
					require.NotNil(t, refusal, "%d entries, unknown field in entry %d, after the wire %v", c.n, c.position, afterTheWire)
					require.Equal(t, ErrorClassUnsupported, refusal.Class)
				}
			}
		})
	}
}

// A name appears at most once in a list of NameValue: a repeated name is refused INVALID_REQUEST wherever the list is,
// never resolved to the first or to the last. The name is valid for every list, so that the refusal can only be for the
// repetition, and it is the caller's text: the refusal never repeats it.
func TestEveryNameInARequestListIsUnique(t *testing.T) {
	const name = "canary_5d1e"
	lists := map[string][]*execv2.NameValue{
		"adjacent, the same value":           nameValues(name, "1", name, "1"),
		"adjacent, another value":            nameValues(name, "first", name, "last"),
		"apart":                              nameValues(name, "1", "other", "x", name, "3"),
		"three times":                        nameValues(name, "1", name, "2", name, "3"),
		"two names, each twice":              nameValues(name, "1", "other", "x", name, "2", "other", "y"),
		"the first and the last of eight":    append(rqtEntries("opt", 7, nil), &execv2.NameValue{Name: "opt0", Value: "again"}),
		"an empty value and a repeated name": nameValues(name, "", name, ""),
	}
	for _, ep := range rqtEntryPoints() {
		// Options is not in this matrix: it refuses every non-empty selection before nameValueMap reads it and has no
		// other list of NameValue, so a repeated name and its deduplicated control would pass whether or not duplicates
		// are detected. Its selection is covered by the presence and unknown-field tests.
		if ep.name == "Options" {
			continue
		}
		for _, list := range rqtLists() {
			if list.names == nil || !list.fill(ep.slots(ep.valid()), 1, nil) {
				continue
			}
			t.Run(ep.name+"/"+list.name, func(t *testing.T) {
				for label, entries := range lists {
					for _, afterTheWire := range []bool{false, true} {
						refusal, _ := rqtAdmitWith(t, ep, afterTheWire, func(s rqtSlots) bool { return list.names(s, entries) })
						rqtRequireClass(t, ep, refusal, ErrorClassInvalidRequest)
						require.NotContains(t, refusal.Error(), name, "%s, after the wire %v", label, afterTheWire)
						if refusal.Err != nil {
							require.NotContains(t, refusal.Err.Error(), name, "%s, after the wire %v", label, afterTheWire)
						}
					}
				}
				// the same list with its repetition removed
				refusal, _ := rqtAdmitWith(t, ep, true, func(s rqtSlots) bool { return list.names(s, nameValues(name, "1", "other", "x")) })
				rqtRequireClass(t, ep, refusal, rqtCleanClass(ep, list))
			})
		}
	}
}

// The first thing a request is refused for decides: an unknown field inside an element comes before a name twice, before
// a name that breaks the bound of its list and before the length of a list that is beyond that bound, because it is read
// with the message; and a list too long for any request is refused for its length, before it is read at all.
func TestAnUnknownFieldInAnEntryComesBeforeEverythingAboutTheList(t *testing.T) {
	for _, ep := range rqtEntryPoints() {
		for _, list := range rqtLists() {
			if list.names == nil || !list.fill(ep.slots(ep.valid()), 1, nil) {
				continue
			}
			t.Run(ep.name+"/"+list.name, func(t *testing.T) {
				withUnknown := func(entries ...*execv2.NameValue) []*execv2.NameValue {
					entries[len(entries)-1] = proto.Clone(entries[len(entries)-1]).(*execv2.NameValue)
					entries[len(entries)-1].ProtoReflect().SetUnknown(rqtVarint)
					return entries
				}
				cases := map[string][]*execv2.NameValue{
					"a name twice":                           withUnknown(&execv2.NameValue{Name: "dup"}, &execv2.NameValue{Name: "dup"}),
					"a name twice, the field in the first":   append([]*execv2.NameValue{withUnknown(&execv2.NameValue{Name: "dup"})[0]}, &execv2.NameValue{Name: "dup"}),
					"a name that breaks the bound":           withUnknown(&execv2.NameValue{Name: "ok"}, list.invalid),
					"a list one beyond its own bound":        withUnknown(rqtEntries("o", list.overBound, nil)...),
					"a list one beyond its bound, the first": append([]*execv2.NameValue{withUnknown(&execv2.NameValue{Name: "x"})[0]}, rqtEntries("o", list.overBound, nil)...),
				}
				for label, entries := range cases {
					for _, afterTheWire := range []bool{false, true} {
						refusal, _ := rqtAdmitWith(t, ep, afterTheWire, func(s rqtSlots) bool { return list.names(s, entries) })
						rqtRequireClass(t, ep, refusal, ErrorClassUnsupported)
						require.NotNil(t, refusal, label)
					}
				}

				// Beyond the longest list any request may carry the list is refused for its length and the element is not
				// read, so the unknown field in it is not seen.
				tooLong := rqtEntries("o", 1025, map[int][]byte{0: rqtVarint})
				refusal, _ := rqtAdmitWith(t, ep, true, func(s rqtSlots) bool { return list.names(s, tooLong) })
				rqtRequireClass(t, ep, refusal, ErrorClassInvalidRequest)
			})
		}
	}

	// the same list, with every element knowing its fields, is refused for the list and not for a field
	for _, list := range rqtLists() {
		if list.names == nil {
			continue
		}
		for _, ep := range rqtEntryPoints() {
			if !list.fill(ep.slots(ep.valid()), 1, nil) {
				continue
			}
			t.Run("without the field/"+ep.name+"/"+list.name, func(t *testing.T) {
				refusal, _ := rqtAdmitWith(t, ep, true, func(s rqtSlots) bool { return list.names(s, nameValues("dup", "1", "dup", "2")) })
				rqtRequireClass(t, ep, refusal, ErrorClassInvalidRequest)
				refusal, _ = rqtAdmitWith(t, ep, true, func(s rqtSlots) bool { return list.names(s, []*execv2.NameValue{list.invalid}) })
				rqtRequireClass(t, ep, refusal, ErrorClassInvalidRequest)
				refusal, _ = rqtAdmitWith(t, ep, true, func(s rqtSlots) bool { return list.fill(s, list.overBound, nil) })
				rqtRequireClass(t, ep, refusal, ErrorClassInvalidRequest)
			})
		}
	}
}

// The lists of a Start and of a RunRequest are the maps of the request that admission returns, and an empty list is no
// map.
func TestRequestListsAreTheMapsOfTheAdmittedRequest(t *testing.T) {
	reg, registered := rqtTarget(t, nil)
	served := rqtServed()

	start := func(env, labels []*execv2.NameValue) *execv2.Start {
		s := rqtWireStart()
		s.Env, s.Labels = env, labels
		s.Target.Selection = nameValues("container", "app")
		return s
	}
	run := func(env, labels []*execv2.NameValue) *execv2.RunRequest {
		r := rqtWireRun()
		r.Env, r.Labels = env, labels
		r.Target.Selection = nameValues("container", "app")
		return r
	}

	t.Run("the lists are the maps of the admitted request", func(t *testing.T) {
		admitted, refusal := admitStart(start(nameValues("A", "1", "B", "2"), nameValues("team", "x")), registered, served)
		require.Nil(t, refusal)
		request := admitted.request(registered)
		require.True(t, maps.Equal(request.Env, map[string]string{"A": "1", "B": "2"}), "%v", request.Env)
		require.True(t, maps.Equal(request.Labels, map[string]string{"team": "x"}), "%v", request.Labels)
		require.True(t, maps.Equal(request.Selection, map[string]string{"container": "app"}), "%v", request.Selection)

		admittedRun, _, refusal := admitRunRequest(run(nameValues("A", "1"), nameValues("team", "x")), reg, served)
		require.Nil(t, refusal)
		got := admittedRun.request(registered)
		require.True(t, maps.Equal(got.Env, map[string]string{"A": "1"}), "%v", got.Env)
		require.True(t, maps.Equal(got.Labels, map[string]string{"team": "x"}), "%v", got.Labels)
		require.True(t, maps.Equal(got.Selection, map[string]string{"container": "app"}), "%v", got.Selection)
	})

	t.Run("an empty list, and a list of nothing, is no map", func(t *testing.T) {
		for _, empty := range [][]*execv2.NameValue{nil, {}} {
			admitted, refusal := admitStart(start(empty, empty), registered, served)
			require.Nil(t, refusal)
			require.Nil(t, admitted.Env)
			require.Nil(t, admitted.Labels)
			admittedRun, _, refusal := admitRunRequest(run(empty, empty), reg, served)
			require.Nil(t, refusal)
			require.Nil(t, admittedRun.Env)
			require.Nil(t, admittedRun.Labels)
		}
	})

	t.Run("an entry with a name and no value is a name with an empty value", func(t *testing.T) {
		admitted, refusal := admitStart(start([]*execv2.NameValue{{Name: "A"}}, []*execv2.NameValue{{Name: "team"}}), registered, served)
		require.Nil(t, refusal)
		require.Equal(t, map[string]string{"A": ""}, admitted.Env)
		require.Equal(t, map[string]string{"team": ""}, admitted.Labels)
	})
}
