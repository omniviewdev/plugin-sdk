package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func TestActualGRPCFeaturesAreProviderTruth(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider resourcev2.Provider
		want     []resourcev2.Feature
	}{{"catalogOnly", &countProvider{}, nil}, {"browser", &browserProvider{}, []resourcev2.Feature{resourcev2.FeatureConnectionBrowser, resourcev2.FeatureScopeValues}}, {"streamingAction", &streamProvider{}, []resourcev2.Feature{resourcev2.FeatureStreamingActions}}, {"updates", &updateProvider{}, []resourcev2.Feature{resourcev2.FeatureBestEffortUpdates}}} {
		t.Run(tc.name, func(t *testing.T) {
			s := grpc.NewServer()
			RegisterServer(s, tc.provider)
			features, err := NewClient(pipe(t, s)).Features(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if features.Has(resourcev2.FeatureConnections) {
				t.Fatal("unimplemented connection lifecycle advertised")
			}
			if len(features.Traits) != len(tc.want) {
				t.Fatalf("traits %v want %v", features.Traits, tc.want)
			}
			for _, trait := range tc.want {
				if !features.Has(trait) {
					t.Fatalf("missing trait %s", trait)
				}
			}
		})
	}
}

type sessionProvider struct {
	countProvider
	seen chan resourcev2.Session
	wait bool
}

func (p *sessionProvider) Catalog(ctx context.Context, in resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	session, _ := resourcev2.SessionFromContext(ctx)
	p.seen <- session
	if p.wait {
		<-ctx.Done()
		return resourcev2.Catalog{}, ctx.Err()
	}
	return p.countProvider.Catalog(ctx, in)
}

func TestActualGRPCDiagnosticContext(t *testing.T) {
	p := &sessionProvider{seen: make(chan resourcev2.Session, 1)}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx := resourcev2.WithSession(
		context.Background(),
		resourcev2.Session{RequestID: "req/日本語", RequesterID: "diagnostic-only"},
	)
	if _, err := c.Catalog(
		ctx,
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}},
	); err != nil {
		t.Fatal(err)
	}
	got := <-p.seen
	if got.RequestID != "req/日本語" || got.RequesterID != "diagnostic-only" {
		t.Fatalf("diagnostic context lost: %+v", got)
	}
}

func TestIncomingDiagnosticsPreserveTrustedSession(t *testing.T) {
	p := &sessionProvider{seen: make(chan resourcev2.Session, 1)}
	s := grpc.NewServer(
		grpc.UnaryInterceptor(
			func(ctx context.Context, r any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
				return next(
					resourcev2.WithSession(
						ctx,
						resourcev2.Session{
							RequestID:   "trusted-request",
							RequesterID: "trusted-diagnostic",
							PluginID:    "trusted-plugin",
							Values:      map[string]any{"principal": "host-owned"},
						},
					),
					r,
				)
			},
		),
	)
	RegisterServer(s, p)
	ctx := resourcev2.WithSession(
		context.Background(),
		resourcev2.Session{RequestID: "untrusted-request", RequesterID: "untrusted-diagnostic"},
	)
	_, err := NewClient(
		pipe(t, s),
	).Catalog(ctx, resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}})
	if err != nil {
		t.Fatal(err)
	}
	got := <-p.seen
	if got.RequestID != "trusted-request" || got.RequesterID != "trusted-diagnostic" ||
		got.PluginID != "trusted-plugin" ||
		got.Values["principal"] != "host-owned" {
		t.Fatal("wire diagnostics replaced trusted context")
	}
}

func TestRawDiagnosticMetadataRejectsDuplicatesAndMalformedValues(t *testing.T) {
	for _, values := range [][]string{{"same", "same"}, {strings.Repeat("é", 129)}, {string([]byte{255})}} {
		p := &countProvider{}
		s := grpc.NewServer()
		RegisterServer(s, p)
		conn := pipe(t, s)
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.MD{requestIDMetadata: values})
		var out resourcepb.Response
		err := conn.Invoke(
			ctx,
			resourcepb.ResourceProvider_Call_FullMethodName,
			&resourcepb.Request{
				Version:   2,
				Operation: &resourcepb.Request_Catalog{Catalog: &resourcepb.AccessRoute{Connection: "explicit"}},
			},
			&out,
		)
		if resourcev2.ErrorCode(domainError(err)) != "INVALID_REQUEST" {
			t.Fatalf("metadata class: %v", err)
		}
		if p.calls.Load() != 0 {
			t.Fatal("malformed metadata dispatched")
		}
	}
}

func TestActualGRPCCancellationReachesProvider(t *testing.T) {
	p := &sessionProvider{seen: make(chan resourcev2.Session, 1), wait: true}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx, cancel := context.WithCancel(
		resourcev2.WithSession(context.Background(), resourcev2.Session{RequestID: "cancel-request"}),
	)
	done := make(chan error, 1)
	go func() {
		_, err := c.Catalog(ctx, resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}})
		done <- err
	}()
	select {
	case <-p.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive request")
	}
	cancel()
	select {
	case err := <-done:
		if testErrorClass(err) != "CANCELLED" {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled call did not return")
	}
}

type classifierPanicProvider struct{ countProvider }

func (*classifierPanicProvider) Catalog(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	return resourcev2.Catalog{}, errors.New("native failure")
}
func (*classifierPanicProvider) ClassifyError(error) error { panic("provider classifier failed") }
func TestClassifierPanicIsSafeTypedInternal(t *testing.T) {
	s := grpc.NewServer()
	RegisterServer(s, &classifierPanicProvider{})
	_, err := NewClient(
		pipe(t, s),
	).Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}})
	if resourcev2.ErrorCode(err) != "INTERNAL" {
		t.Fatalf("classifier panic class: %v", err)
	}
}
