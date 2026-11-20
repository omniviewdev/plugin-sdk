package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// The sentence rides in the gRPC status, not in a resource message, so the
// strict admission of resource messages (ValidateWire, which rejects any field
// its descriptor does not know) never sees it and WireVersion does not move.
// What remains to prove is that the two readers agree across the change, in
// both directions. The "old" ones below are frozen copies of what every SDK up
// to v0.20.0 ships.

// legacyWire is the status an SDK up to v0.20.0 builds for a classified error
// (codec.go in v0.20.0): the class in an ErrorInfo and a fixed text.
func legacyWire(t *testing.T, code string, grpcCode codes.Code) error {
	t.Helper()
	detailed, err := status.New(grpcCode, "resource operation failed").WithDetails(&errdetails.ErrorInfo{Reason: code, Domain: resourceErrorDomain})
	if err != nil {
		t.Fatal(err)
	}
	return detailed.Err()
}

// legacyDomainError is the reader of every SDK up to v0.20.0 (codec.go in
// v0.20.0, without its observability calls): a host built on a released SDK runs
// it against a plugin built on this one. It keeps the ErrorInfo only.
func legacyDomainError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "resource call cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "resource call timed out")
	}
	st, ok := status.FromError(err)
	if !ok {
		return status.Error(codes.Unknown, "resource transport failed")
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == resourceErrorDomain {
			if !resourcev2.IsErrorCode(info.Reason) {
				return status.Error(codes.Internal, "invalid resource error classification")
			}
			return &resourcev2.ResourceOperationError{Err: err, Code: info.Reason, Message: "resource operation failed"}
		}
	}
	return status.Error(st.Code(), "resource transport failed")
}

func asTyped(t *testing.T, err error) *resourcev2.ResourceOperationError {
	t.Helper()
	var typed *resourcev2.ResourceOperationError
	if !errors.As(err, &typed) {
		t.Fatalf("not a classified resource error: %v", err)
	}
	return typed
}

func localizedMessages(st *status.Status) []*errdetails.LocalizedMessage {
	var out []*errdetails.LocalizedMessage
	for _, d := range st.Details() {
		if m, ok := d.(*errdetails.LocalizedMessage); ok {
			out = append(out, m)
		}
	}
	return out
}

// An older host reading a plugin built on this SDK: the extra detail is skipped
// and the class arrives exactly as before.
func TestOldReaderIgnoresTheCarriedSentence(t *testing.T) {
	for _, k := range kubernetesErrors {
		t.Run(k.code, func(t *testing.T) {
			wire := rpcErrorContext(context.Background(), "test", k.typed())
			if got := localizedMessages(status.Convert(wire)); len(got) != 1 || got[0].Message != k.message {
				t.Fatalf("the sentence is not on the wire, so this proves nothing: %v", got)
			}
			old := asTyped(t, legacyDomainError(wire))
			if old.Code != k.code || old.Message != "resource operation failed" || status.Code(wire) != k.status {
				t.Fatalf("old reader changed its reading: %+v over %s", old, status.Code(wire))
			}
			// The same through a real pair: what the old reader reads is the received status.
			received := asTyped(t, catalogError(t, k.typed())).Err
			if reread := asTyped(t, legacyDomainError(received)); reread.Code != k.code || reread.Message != "resource operation failed" {
				t.Fatalf("old reader over a real pair: %+v", reread)
			}
		})
	}
}

type legacyPlugin struct {
	resourcepb.UnimplementedResourceProviderServer
	wire error
}

func (p *legacyPlugin) Call(context.Context, *resourcepb.Request) (*resourcepb.Response, error) {
	return nil, p.wire
}

// A newer host reading a plugin built on an older SDK: the class arrives and no
// sentence is invented. The SDK's own placeholder is not a sentence.
func TestNewReaderReadsAnOldPlugin(t *testing.T) {
	for _, k := range kubernetesErrors {
		t.Run(k.code, func(t *testing.T) {
			wire := legacyWire(t, k.code, k.status)
			direct := asTyped(t, domainError(wire))
			if direct.Code != k.code || direct.Message != "" || direct.Err == nil {
				t.Fatalf("direct: %+v", direct)
			}
			s := grpc.NewServer()
			resourcepb.RegisterResourceProviderServer(s, &legacyPlugin{wire: wire})
			_, got := NewClient(pipe(t, s)).Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
			typed := asTyped(t, got)
			if typed.Code != k.code || typed.Message != "" || status.Code(got) != k.status {
				t.Fatalf("real pair: %+v over %s", typed, status.Code(got))
			}
		})
	}
}

// The classification must arrive whatever becomes of the sentence.
func TestEncoderCarriesTheSentenceOnlyWhenItCanVouchForIt(t *testing.T) {
	typed := &resourcev2.ResourceOperationError{Code: "CONFLICT", Message: "The object changed."}
	cases := []struct {
		name        string
		err         error
		code, carry string
	}{
		{"a sentence", typed, "CONFLICT", "The object changed."},
		{"a sentence behind a wrapper", fmt.Errorf("update: %w", typed), "CONFLICT", "The object changed."},
		{"no sentence", &resourcev2.ResourceOperationError{Code: "NOT_FOUND"}, "NOT_FOUND", ""},
		{"an untyped provider error", errors.New("native secret diagnostic"), "PROVIDER_ERROR", ""},
		{"text that is not UTF-8", &resourcev2.ResourceOperationError{Code: "CONFLICT", Message: "bad \xff text"}, "CONFLICT", ""},
		{"text at the bound", &resourcev2.ResourceOperationError{Code: "CONFLICT", Message: strings.Repeat("a", MaxErrorMessageBytes)}, "CONFLICT", strings.Repeat("a", MaxErrorMessageBytes)},
		{"text over the bound", &resourcev2.ResourceOperationError{Code: "CONFLICT", Message: strings.Repeat("a", MaxErrorMessageBytes+1)}, "CONFLICT", ""},
		{"multi-byte text over the bound in bytes", &resourcev2.ResourceOperationError{Code: "CONFLICT", Message: strings.Repeat("é", MaxErrorMessageBytes/2+1)}, "CONFLICT", ""},
		{"an unreviewed class", &resourcev2.ResourceOperationError{Code: "HOST_ROOT_GRANT", Message: "text of a class that does not exist"}, "INTERNAL", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := rpcErrorContext(context.Background(), "test", tc.err)
			st := status.Convert(wire)
			if st.Message() != "resource operation failed" {
				t.Fatalf("status text: %q", st.Message())
			}
			if tc.carry == "" && len(localizedMessages(st)) != 0 {
				t.Fatal("a sentence the encoder cannot vouch for was carried")
			}
			got := asTyped(t, domainError(wire))
			if got.Code != tc.code || got.Message != tc.carry {
				t.Fatalf("read back %+v, want class %s and sentence %q", got, tc.code, tc.carry)
			}
		})
	}
}

// ResourceRPCError is the encoder resource exec and host connections share. Neither
// has carried a sentence, and the callback service must not: its tests pin that an
// owner's private text never reaches the plugin that borrows the connection. The
// errors the SDK raises itself keep their fixed status too.
func TestSharedEncoderCarriesTheClassOnly(t *testing.T) {
	for name, wire := range map[string]error{
		"the exported encoder":   ResourceRPCError(&resourcev2.ResourceOperationError{Code: "GRANT_DENIED", Message: "private diagnostic"}),
		"an SDK admission error": rpcError(invalid("malformed resource request")),
	} {
		t.Run(name, func(t *testing.T) {
			if got := localizedMessages(status.Convert(wire)); len(got) != 0 {
				t.Fatalf("a sentence was carried: %v", got)
			}
			if got := asTyped(t, domainError(wire)); got.Message != "" {
				t.Fatalf("read back a sentence: %+v", got)
			}
		})
	}
}

func craftedStatus(code codes.Code, details ...*anypb.Any) error {
	return status.ErrorProto(&spb.Status{Code: int32(code), Message: "resource operation failed", Details: details})
}

func anyOf(t *testing.T, m proto.Message) *anypb.Any {
	t.Helper()
	a, err := anypb.New(m)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func info(t *testing.T, reason, domain string) *anypb.Any {
	return anyOf(t, &errdetails.ErrorInfo{Reason: reason, Domain: domain})
}

func sentence(t *testing.T, locale, message string) *anypb.Any {
	return anyOf(t, &errdetails.LocalizedMessage{Locale: locale, Message: message})
}

// A plugin is untrusted by the host that reads it: the reader keeps the class it
// knows, takes a bounded English sentence from a status that classifies, and
// takes nothing else.
func TestReaderTrustsNothingItDoesNotBound(t *testing.T) {
	// A LocalizedMessage whose text is not UTF-8, written by hand: generated code
	// refuses to marshal it, a hostile plugin does not have to.
	var notUTF8 []byte
	notUTF8 = protowire.AppendTag(notUTF8, 1, protowire.BytesType)
	notUTF8 = protowire.AppendString(notUTF8, "en")
	notUTF8 = protowire.AppendTag(notUTF8, 2, protowire.BytesType)
	notUTF8 = protowire.AppendBytes(notUTF8, []byte("bad \xff text"))
	rawNotUTF8 := &anypb.Any{TypeUrl: "type.googleapis.com/google.rpc.LocalizedMessage", Value: notUTF8}

	cases := []struct {
		name    string
		wire    error
		message string
		typed   bool
	}{
		{"an English sentence", craftedStatus(codes.Aborted, info(t, "CONFLICT", resourceErrorDomain), sentence(t, "en", "It changed.")), "It changed.", true},
		{"the sentence may come first", craftedStatus(codes.Aborted, sentence(t, "en", "It changed."), info(t, "CONFLICT", resourceErrorDomain)), "It changed.", true},
		{"another language is skipped", craftedStatus(codes.Aborted, info(t, "CONFLICT", resourceErrorDomain), sentence(t, "fr", "Il a changé.")), "", true},
		{"the first English sentence wins", craftedStatus(codes.Aborted, info(t, "CONFLICT", resourceErrorDomain), sentence(t, "en", "First."), sentence(t, "en", "Second.")), "First.", true},
		{"a dropped first sentence is not replaced by a later one", craftedStatus(codes.Aborted, info(t, "CONFLICT", resourceErrorDomain), sentence(t, "en", strings.Repeat("a", MaxErrorMessageBytes+1)), sentence(t, "en", "Second.")), "", true},
		{"an empty first sentence is not replaced by a later one", craftedStatus(codes.Aborted, info(t, "CONFLICT", resourceErrorDomain), sentence(t, "en", ""), sentence(t, "en", "Second.")), "", true},
		{"an over-long sentence is dropped, the class is not", craftedStatus(codes.Aborted, info(t, "CONFLICT", resourceErrorDomain), sentence(t, "en", strings.Repeat("a", MaxErrorMessageBytes+1))), "", true},
		{"a sentence that is not UTF-8 is dropped, the class is not", craftedStatus(codes.Aborted, info(t, "CONFLICT", resourceErrorDomain), rawNotUTF8), "", true},
		{"a sentence without a classification is nothing", craftedStatus(codes.Aborted, sentence(t, "en", "Trust me.")), "", false},
		{"a classification of another domain is nothing", craftedStatus(codes.Aborted, info(t, "CONFLICT", "someone.else"), sentence(t, "en", "Trust me.")), "", false},
		{"an unknown class takes its sentence with it", craftedStatus(codes.Aborted, info(t, "HOST_ROOT_GRANT", resourceErrorDomain), sentence(t, "en", "Trust me.")), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domainError(tc.wire)
			var typed *resourcev2.ResourceOperationError
			if errors.As(got, &typed) != tc.typed {
				t.Fatalf("typed = %v, want %v: %v", typed, tc.typed, got)
			}
			if tc.typed && (typed.Code != "CONFLICT" || typed.Message != tc.message) {
				t.Fatalf("read %+v, want CONFLICT and %q", typed, tc.message)
			}
			if !tc.typed && strings.Contains(got.Error(), "Trust me.") {
				t.Fatalf("a sentence the reader had no business reading escaped: %v", got)
			}
		})
	}
}

type hostilePlugin struct {
	resourcepb.UnimplementedResourceProviderServer
	sentence string
}

func (p *hostilePlugin) Call(context.Context, *resourcepb.Request) (*resourcepb.Response, error) {
	detailed, err := status.New(codes.Aborted, "resource operation failed").WithDetails(
		&errdetails.ErrorInfo{Reason: "CONFLICT", Domain: resourceErrorDomain},
		&errdetails.LocalizedMessage{Locale: "en", Message: p.sentence},
	)
	if err != nil {
		return nil, err
	}
	return nil, detailed.Err()
}

// The transport itself accepts a very large status; the bound is the SDK's.
func TestOversizeSentenceCrossesTheTransportButNotTheReader(t *testing.T) {
	s := grpc.NewServer()
	resourcepb.RegisterResourceProviderServer(s, &hostilePlugin{sentence: strings.Repeat("a", 1<<20)})
	_, got := NewClient(pipe(t, s)).Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
	typed := asTyped(t, got)
	if typed.Code != "CONFLICT" || typed.Message != "" {
		t.Fatalf("read %+v", typed)
	}
	if got := localizedMessages(receivedStatus(t, got)); len(got) != 1 || len(got[0].Message) != 1<<20 {
		t.Fatal("the transport did not carry the oversize sentence, so the reader's bound was not exercised")
	}
}
