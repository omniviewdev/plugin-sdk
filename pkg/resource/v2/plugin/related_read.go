package plugin

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/wrapperspb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/v2/internal/relatedwire"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
)

const (
	relatedService       = "omniview.extension.v1.Owner"
	readRelatedMethod    = "/" + relatedService + "/ReadRelatedV1"
	relatedSourcesMethod = "/" + relatedService + "/RelatedSourcesV1"
	relatedOwnerMD       = "omniview-extension-owner-bin"
	relatedConnectionMD  = "omniview-extension-connection-bin"
	relatedRequestBytes  = relatedwire.MetadataBytes
	relatedResponseBytes = 512 << 10
	relatedTimeout       = 5 * time.Second
)

var errRelatedSession = status.Error(codes.PermissionDenied, "Related session unavailable.")

type relatedOwnerRPC interface {
	session(context.Context, resourcev2.RelatedReadRequest) (context.Context, error)
	invoke(context.Context, string, resourcev2.RelatedReadRequest) (*wrapperspb.BytesValue, error)
}

type relatedOwnerServer struct {
	owner    resourcev2.RelatedReadOwner
	id       string
	settings pkgsettings.Provider
}

// The ordinary resource service neither advertises nor tunnels this protocol.
// An SDK with no configured native owner does not register either method.
func registerRelatedOwner(s *grpc.Server, p *GRPCPlugin) {
	declarer, ok := p.Impl.(interface {
		RelatedReadOwnerV1() resourcev2.RelatedReadOwner
	})
	if !ok || p.OwnerID == "" {
		return
	}
	owner := declarer.RelatedReadOwnerV1()
	if owner == nil {
		return
	}
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: relatedService,
		HandlerType: (*relatedOwnerRPC)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "RelatedSourcesV1", Handler: relatedUnary(relatedSourcesMethod)},
			{MethodName: "ReadRelatedV1", Handler: relatedUnary(readRelatedMethod)},
		},
	}, &relatedOwnerServer{owner: owner, id: p.OwnerID, settings: p.SettingsProvider})
}

func relatedUnary(method string) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, relatedTimeout)
		defer cancel()
		// The session below is a host assertion only on the mutually
		// authenticated go-plugin channel. Plain metadata proves nothing.
		p, ok := peer.FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "Related channel unavailable.")
		}
		tls, ok := p.AuthInfo.(credentials.TLSInfo)
		if !ok || len(tls.State.VerifiedChains) == 0 {
			return nil, status.Error(codes.Unauthenticated, "Related channel unavailable.")
		}
		request := new(wrapperspb.BytesValue)
		envelope := NewUtilityTransportEnvelope(request, validateRelatedFrame)
		if dec(envelope) != nil || envelope.AdmissionError() != nil {
			return nil, status.Error(codes.InvalidArgument, "Invalid related request.")
		}
		var in resourcev2.RelatedReadRequest
		if json.Unmarshal(request.Value, &in, relatedRequestOptions) != nil {
			return nil, status.Error(codes.InvalidArgument, "Invalid related request.")
		}
		owner := srv.(relatedOwnerRPC)
		// Bound metadata before ordinary middleware can interpret a profile
		// or resource selector. The handler only receives this admitted DTO.
		ctx, err := owner.session(ctx, in)
		if err != nil {
			return nil, err
		}
		handler := func(ctx context.Context, msg any) (any, error) {
			if msg != request {
				return nil, status.Error(codes.InvalidArgument, "Invalid related request.")
			}
			return owner.invoke(ctx, method, in)
		}
		if interceptor == nil {
			return handler(ctx, request)
		}
		return interceptor(ctx, request, &grpc.UnaryServerInfo{Server: srv, FullMethod: method}, handler)
	}
}

// A single canonical bytes field, no unknown fields, duplicates or alternate
// framing. The method-specific gRPC receive limit also caps decompression.
func validateRelatedFrame(b []byte) error {
	if len(b) < 3 || len(b) > relatedRequestBytes || b[0] != 0x0a {
		return invalid("Invalid related request frame.")
	}
	v, n := protowire.ConsumeBytes(b[1:])
	if len(v) == 0 || n != len(b)-1 || n != protowire.SizeBytes(len(v)) {
		return invalid("Invalid related request frame.")
	}
	return nil
}

var relatedRequestOptions = json.JoinOptions(json.RejectUnknownMembers(true), json.WithUnmarshalers(
	json.UnmarshalFromFunc(func(d *jsontext.Decoder, dst *[]resourcev2.ReadTarget) error {
		if t, err := d.ReadToken(); err != nil || t.Kind() != '[' {
			return invalid("Invalid related targets.")
		}
		for d.PeekKind() != ']' {
			if len(*dst) >= 8 || d.PeekKind() != '{' {
				return invalid("Invalid related targets.")
			}
			var target resourcev2.ReadTarget
			if err := json.UnmarshalDecode(d, &target); err != nil {
				return err
			}
			*dst = append(*dst, target)
		}
		if _, err := d.ReadToken(); err != nil || len(*dst) == 0 {
			return invalid("Invalid related targets.")
		}
		return nil
	}),
))

func (s *relatedOwnerServer) invoke(ctx context.Context, method string, in resourcev2.RelatedReadRequest) (*wrapperspb.BytesValue, error) {
	var result any
	limit := relatedResponseBytes
	switch method {
	case relatedSourcesMethod:
		sources, err := s.owner.RelatedSourcesV1(ctx, in)
		if err != nil {
			return nil, relatedStatus(err)
		}
		result, limit = relatedwire.Discovery{Sources: sources}, relatedwire.MetadataBytes
	case readRelatedMethod:
		page, err := s.owner.ReadRelatedV1(ctx, in)
		if err != nil {
			return nil, relatedStatus(err)
		}
		result = page
	default:
		return nil, status.Error(codes.Unimplemented, "Related operation unavailable.")
	}
	var out bytes.Buffer
	if err := relatedwire.WriteJSON(&out, result, limit); err != nil {
		return nil, status.Error(codes.ResourceExhausted, "Related response unavailable.")
	}
	if err := ctx.Err(); err != nil {
		return nil, rpcError(err)
	}
	return wrapperspb.Bytes(out.Bytes()), nil
}

// Only this closed status class crosses the owner boundary. Even a classified
// provider error may contain names or native exception text in its message.
func relatedStatus(err error) error {
	code := codes.Internal
	switch resourcev2.ErrorCode(err) {
	case "UNSUPPORTED":
		code = codes.Unimplemented
	case "UNAUTHORIZED", "FORBIDDEN", "PERMISSION_DENIED", "GRANT_DENIED", "DELEGATION_NOT_ACTIVE":
		code = codes.PermissionDenied
	case "NOT_FOUND", "OWNER_UNAVAILABLE", "UNAVAILABLE", "CONNECTION_NOT_STARTED", "CONNECTION_ERROR":
		code = codes.Unavailable
	case "RESET_REQUIRED":
		code = codes.FailedPrecondition
	case "INVALID_REQUEST", "INVALID_INPUT":
		code = codes.InvalidArgument
	case "RESOURCE_LIMIT":
		code = codes.ResourceExhausted
	case "CANCELLED":
		code = codes.Canceled
	case "TIMEOUT":
		code = codes.DeadlineExceeded
	}
	return status.Error(code, "Related read unavailable.")
}

func (s *relatedOwnerServer) session(ctx context.Context, in resourcev2.RelatedReadRequest) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, rpcError(err)
	}
	md, _ := metadata.FromIncomingContext(ctx)
	one := func(k string, limit int) string {
		v := md.Get(k)
		if len(v) != 1 || len(v[0]) > limit || !utf8.ValidString(v[0]) {
			return ""
		}
		return v[0]
	}
	owner, conn := one(relatedOwnerMD, 128), one(relatedConnectionMD, relatedRequestBytes)
	requester, request := one(requesterIDMetadata, maxDiagnosticIDBytes), one(requestIDMetadata, maxDiagnosticIDBytes)
	if owner == "" || owner != s.id || conn == "" || conn != in.Route.Connection || requester == "" || request == "" ||
		resourcev2.ValidateCatalogRevision(one(catalogRevisionMetadata, resourcev2.MaxCatalogRevisionBytes)) != nil {
		return nil, errRelatedSession
	}
	// No alternate route observations, client profiles, resource selectors or
	// conflicting legacy session metadata can accompany this single-route RPC.
	for k, v := range md {
		if !strings.HasPrefix(k, "omniview-") {
			continue
		}
		switch k {
		case relatedOwnerMD, relatedConnectionMD, requestIDMetadata, requesterIDMetadata, catalogRevisionMetadata:
			continue
		case "omniview-request-id":
			if len(v) == 1 && v[0] == request {
				continue
			}
		case "omniview-requester-id":
			if len(v) == 1 && v[0] == requester {
				continue
			}
		case "omniview-connection-id":
			if len(v) == 1 && v[0] == conn {
				continue
			}
		case "omniview-protocol-version":
			if len(v) == 1 && v[0] == "2" {
				continue
			}
		}
		return nil, errRelatedSession
	}
	ctx, err := incomingSession(ctx, s.settings) // clears inherited route pins
	if err != nil {
		return nil, errRelatedSession
	}
	return resourcev2.WithSession(ctx, resourcev2.Session{PluginID: owner, RequestID: request, RequesterID: requester,
		Connection: &types.Connection{ID: conn}, PluginConfig: s.settings}), nil
}
