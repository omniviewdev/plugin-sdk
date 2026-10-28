package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
	"google.golang.org/grpc/metadata"
	"unicode/utf8"
)

const (
	requestIDMetadata    = "omniview-request-id-bin"
	requesterIDMetadata  = "omniview-requester-id-bin"
	maxDiagnosticIDBytes = 256
)

func diagnostic(md metadata.MD, key string) (string, error) {
	values := md.Get(key)
	if len(values) > 1 {
		return "", invalid("duplicate diagnostic metadata")
	}
	if len(values) == 0 {
		return "", nil
	}
	v := values[0]
	if len(v) > maxDiagnosticIDBytes || !utf8.ValidString(v) {
		return "", invalid("invalid diagnostic metadata")
	}
	return v, nil
}

// Diagnostic identifiers are correlation data, never authenticated principal data.
func outgoingSession(ctx context.Context) (context.Context, error) {
	session, _ := resourcev2.SessionFromContext(ctx)
	md, _ := metadata.FromOutgoingContext(ctx)
	if _, err := diagnostic(md, requestIDMetadata); err != nil {
		return nil, err
	}
	if _, err := diagnostic(md, requesterIDMetadata); err != nil {
		return nil, err
	}
	if session.RequestID == "" && session.RequesterID == "" {
		return ctx, nil
	}
	md = md.Copy()
	if err := attachDiagnostic(md, requestIDMetadata, session.RequestID); err != nil {
		return nil, err
	}
	if err := attachDiagnostic(md, requesterIDMetadata, session.RequesterID); err != nil {
		return nil, err
	}
	return metadata.NewOutgoingContext(ctx, md), nil
}
func attachDiagnostic(md metadata.MD, key, value string) error {
	if value == "" {
		return nil
	}
	if len(md.Get(key)) != 0 {
		return invalid("duplicate diagnostic metadata")
	}
	if len(value) > maxDiagnosticIDBytes || !utf8.ValidString(value) {
		return invalid("invalid diagnostic metadata")
	}
	md.Set(key, value)
	return nil
}
func incomingSession(ctx context.Context, settings pkgsettings.Provider) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	requestID, err := diagnostic(md, requestIDMetadata)
	if err != nil {
		return nil, err
	}
	requesterID, err := diagnostic(md, requesterIDMetadata)
	if err != nil {
		return nil, err
	}
	session, _ := resourcev2.SessionFromContext(ctx)
	if session.RequestID == "" {
		session.RequestID = requestID
	}
	if session.RequesterID == "" {
		session.RequesterID = requesterID
	}
	if settings != nil {
		session.PluginConfig = settings
	}
	return resourcev2.WithSession(ctx, session), nil
}
