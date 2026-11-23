package exec

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// The functions of the admission files are the strict half of the exec v2 wire: they decide whether a request may
// reach an author's code. Their errors are *Error values whose class and reason are the refusal the host sees; the
// detail is local text in Err and never crosses the wire.

// invalidRequest is an ErrorClassInvalidRequest refusal with local detail.
func invalidRequest(format string, args ...any) *Error {
	return &Error{Class: ErrorClassInvalidRequest, Err: fmt.Errorf(format, args...)}
}

// unsupportedRequest is an ErrorClassUnsupported refusal with local detail.
func unsupportedRequest(format string, args ...any) *Error {
	return &Error{Class: ErrorClassUnsupported, Err: fmt.Errorf(format, args...)}
}

// refusalFromResource classifies an error of the resource lane's own validators as an exec refusal.
func refusalFromResource(err error) *Error {
	return &Error{Class: classFromResourceCode(resourcev2.ErrorCode(err)), Err: err}
}

// maxRequestListLen is the longest list any message of exec's own may carry: an argv holds at most 1,024 elements, the
// most of any field of the contract. A longer list cannot be part of a valid request. The lists of the resource lane's
// messages that a request embeds (the scope of the canonical reference of a target) are not held to it: the contract
// does not declare a limit for them, and the resource validators decide what such a list may hold.
const maxRequestListLen = maxArgvElements

// resourceLanePackage is the proto package of the resource lane's messages. A list that one of its messages declares is
// the resource policy's to bound, not exec's.
var resourceLanePackage = (&resourcepb.ResourceAddress{}).ProtoReflect().Descriptor().ParentFile().Package()

// isResourceLaneField reports whether fd is declared by a message of the resource lane.
func isResourceLaneField(fd protoreflect.FieldDescriptor) bool {
	return fd.ContainingMessage().ParentFile().Package() == resourceLanePackage
}

// rejectUnknownFields enforces the strict side of the wire for a decoded request. It refuses (ErrorClassInvalidRequest) a
// request that holds a list longer than any request may carry, without reading that list, so that what a request
// costs does not depend on how long its lists are. Then it refuses (ErrorClassUnsupported) a request in which m, or any
// message nested in it (a field, an element of a list), carries a field this build's descriptor does not know.
// Protobuf keeps such a field as an unknown field when it decodes; a plugin that ignored it would run a command whose
// request asked for something it did not understand, which is why requests are strict and frames are lenient.
func rejectUnknownFields(m proto.Message) *Error {
	if m == nil {
		return nil
	}
	// This walk reads the message as decoded. A oneof keeps only its last arm on the wire: an unknown field inside an
	// earlier arm that a later arm replaced (an OpenRequest with Start, then Attach) is discarded by the decoder, and
	// there is nothing left here to inspect. Making both arms refusable is a schema change (Start and Attach as two
	// optional fields instead of a oneof, wire-identical), to be decided with the Open core before any tag.
	root := m.ProtoReflect()
	if hasLongList(root) {
		return invalidRequest("request carries a list of more than %d elements", maxRequestListLen)
	}
	if hasUnknownFields(root) {
		return unsupportedRequest("request carries a field this plugin does not know")
	}
	return nil
}

// hasUnknownFields reports whether m, or a message nested in it (a singular field, an element of a list, a value of a
// map), holds a field its descriptor does not know. It reads every element of every list it meets: the caller bounds
// the lists of exec's own first (hasLongList); the lists of the resource lane are read whole, as the resource policy
// bounds them.
func hasUnknownFields(m protoreflect.Message) bool {
	if !m.IsValid() {
		return false
	}
	if len(m.GetUnknown()) > 0 {
		return true
	}
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					found = hasUnknownFields(mv.Message())
					return !found
				})
			}
		case fd.IsList():
			if fd.Message() != nil {
				list := v.List()
				for i := range list.Len() {
					if hasUnknownFields(list.Get(i).Message()) {
						found = true
						break
					}
				}
			}
		case fd.Message() != nil:
			found = hasUnknownFields(v.Message())
		}
		return !found
	})
	return found
}

// hasLongList reports whether m, or a message nested in it, holds a list or a map of exec's own of more than
// maxRequestListLen elements. It never reads the elements of such a list, and reads at most maxRequestListLen of any
// other, so its work is bounded by the size of a valid request, whatever the request holds. It leaves the lists the
// resource lane declares (the scope of a reference) alone: the resource validators bound those.
func hasLongList(m protoreflect.Message) bool {
	scan := new(longListScan)
	scan.visit = scan.visitField
	scan.walk(m)
	return scan.found
}

// longListScan is the state of one hasLongList. Every message it meets is read with the same callback, so that a walk
// allocates a constant number of times, however many messages (the elements of a list) it meets.
type longListScan struct {
	found bool
	visit func(protoreflect.FieldDescriptor, protoreflect.Value) bool
}

func (s *longListScan) walk(m protoreflect.Message) {
	if m.IsValid() {
		m.Range(s.visit)
	}
}

// visitField reads one set field of a message: a list or a map is too long or is walked; a message is walked.
func (s *longListScan) visitField(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
	if (fd.IsMap() || fd.IsList()) && isResourceLaneField(fd) {
		return true
	}
	switch {
	case fd.IsMap():
		values := v.Map()
		switch {
		case values.Len() > maxRequestListLen:
			s.found = true
		case fd.MapValue().Message() != nil:
			values.Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				s.walk(mv.Message())
				return !s.found
			})
		}
	case fd.IsList():
		list := v.List()
		switch {
		case list.Len() > maxRequestListLen:
			s.found = true
		case fd.Message() != nil:
			for i := range list.Len() {
				s.walk(list.Get(i).Message())
				if s.found {
					break
				}
			}
		}
	case fd.Message() != nil:
		s.walk(v.Message())
	}
	return !s.found
}

// nameValueMap converts the repeated NameValue of a request (an environment, labels, a selection) to the Go map of
// the admitted request. Requests carry a list, and never a map, so that an unknown field inside an entry can be seen
// and refused; the price is that a name may appear twice, and a name that appears twice is INVALID_REQUEST, never
// "the last one wins". More than maxEntries entries are refused before any is read, so that a long list is not
// walked. No entry is nil in a decoded message; a nil one in a message built in process reads as an empty name,
// which the validators of the bounds refuse where a name must not be empty. An empty list is a nil map. The refusal
// never quotes a name or a value: both are the caller's text.
func nameValueMap(list []*execv2.NameValue, what string, maxEntries int) (map[string]string, *Error) {
	if len(list) > maxEntries {
		return nil, invalidRequest("%d %s entries exceed the limit of %d", len(list), what, maxEntries)
	}
	if len(list) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(list))
	for _, entry := range list {
		if _, dup := out[entry.GetName()]; dup {
			return nil, invalidRequest("a name appears twice in the %s", what)
		}
		out[entry.GetName()] = entry.GetValue()
	}
	return out, nil
}

// admitTargetRef validates the wire form of a target reference and returns its Go form and the selection the host
// made. The reference must be canonical (the resource lane's validators decide), the route must name a connection,
// and the selection must fit its bounds: at most 8 entries with no name twice, each name a closed code and each
// value at most 256 bytes of plain text. When allowSelection is false (Options) a non-empty selection is refused.
// The route is not compared with the caller's connection here: the connection lease does that.
func admitTargetRef(p *execv2.TargetRef, allowSelection bool) (TargetRef, map[string]string, *Error) {
	if p == nil {
		return TargetRef{}, nil, invalidRequest("target is required")
	}
	if p.GetReference() == nil || p.GetRoute() == nil {
		return TargetRef{}, nil, invalidRequest("target needs a reference and a route")
	}
	reference, err := resourcev2.FromProtoResourceReference(p.GetReference())
	if err != nil {
		return TargetRef{}, nil, refusalFromResource(err)
	}
	route, err := resourcev2.FromProtoAccessRoute(p.GetRoute())
	if err != nil {
		return TargetRef{}, nil, refusalFromResource(err)
	}
	if route.Connection == "" {
		return TargetRef{}, nil, invalidRequest("target route needs a connection")
	}
	if len(p.GetSelection()) > 0 && !allowSelection {
		return TargetRef{}, nil, invalidRequest("selection is not used here")
	}
	selection, refusal := nameValueMap(p.GetSelection(), "selection", maxSelectionEntries)
	if refusal != nil {
		return TargetRef{}, nil, refusal
	}
	if refusal := validateSelection(selection); refusal != nil {
		return TargetRef{}, nil, refusal
	}
	return TargetRef{Reference: reference, Route: route}, selection, nil
}

// validateSelection checks the entries of a selection whose length nameValueMap bounded: each key a closed code and
// each value at most 256 bytes of plain text.
func validateSelection(selection map[string]string) *Error {
	for name, value := range selection {
		if !closedCode(name) || len(value) > maxSelectionValue || !validText(value) {
			// Never the key or the value: both are the caller's text, and the cause is logged.
			return invalidRequest("a selection is not a closed code with a plain value of at most %d bytes", maxSelectionValue)
		}
	}
	return nil
}

// targetRefToProto validates a target reference and encodes it: the reference must be canonical and the route must
// name a connection. The error is an *Error with ErrorClassInvalidRequest.
func targetRefToProto(ref TargetRef) (*execv2.TargetRef, error) {
	reference, err := resourcev2.ToProtoResourceReference(ref.Reference)
	if err != nil {
		return nil, refusalFromResource(err)
	}
	route, err := resourcev2.ToProtoAccessRoute(ref.Route)
	if err != nil {
		return nil, refusalFromResource(err)
	}
	if ref.Route.Connection == "" {
		return nil, invalidRequest("target route needs a connection")
	}
	return &execv2.TargetRef{Reference: reference, Route: route}, nil
}
