package plugin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	json "encoding/json/v2"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

const testWindow = "78a70d51-92ab-4aeb-a231-d30ef7b7347b"

func ownerRPCController(t *testing.T, read func(context.Context, *string, resourcev2.RelatedReadRequest) (resourcev2.RelatedReadPage, error)) *resourcev2.Controller[string] {
	t.Helper()
	p := &qualifiedProvider{allowed: true}
	c, err := resourcev2.NewController(resourcev2.Config[string]{Catalog: p.Catalog, Lifecycle: &resourcev2.Lifecycle[string]{
		LoadConnections: func(context.Context) ([]types.Connection, error) {
			return []types.Connection{{ID: "explicit", Name: "configured"}}, nil
		},
		CreateClient:  func(context.Context, types.Connection) (*string, error) { uid := "native-uid"; return &uid, nil },
		DestroyClient: func(context.Context, *string) error { return nil },
	}, Registrations: []resourcev2.Registration[string]{{Type: "opaque/type", Get: func(ctx context.Context, _ *string, in resourcev2.GetRequest) (resourcev2.Record, error) {
		return p.Get(ctx, in)
	}}}, RelatedReads: []resourcev2.RelatedReadRegistration[string]{{Operation: "installed/links@1", RootType: "opaque/type", Sources: []string{"opaque/type"}, Read: read}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.LoadConnections(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(t.Context(), "explicit"); err != nil {
		t.Fatal(err)
	}
	return c
}

// Use a freshly issued, mutually verified leaf, like the managed go-plugin
// channel. Plaintext pipes must not be able to assert these metadata keys.
func ownerRPCPipe(t *testing.T, p *GRPCPlugin) *grpc.ClientConn {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, DNSNames: []string{"owner"},
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: "owner", RootCAs: roots, ClientCAs: roots,
		ClientAuth: tls.RequireAndVerifyClientCert, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	s := grpc.NewServer(append(ServerOptions(), grpc.Creds(creds))...)
	if err := p.GRPCServer(nil, s); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(64 << 10)
	go func() { _ = s.Serve(listener) }()
	c, err := grpc.NewClient("passthrough:///owner", grpc.WithTransportCredentials(creds),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(); s.Stop(); _ = listener.Close() })
	return c
}

func TestInstalledOwnerReadRPC(t *testing.T) {
	var calls atomic.Int32
	var oversize atomic.Bool
	var stall atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	c := ownerRPCController(t, func(ctx context.Context, native *string, in resourcev2.RelatedReadRequest) (resourcev2.RelatedReadPage, error) {
		calls.Add(1)
		if stall.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		s, _ := resourcev2.SessionFromContext(ctx)
		end, bounded := ctx.Deadline()
		if s.RequesterID != testWindow || s.RequestID != "request" || s.PluginID != "installed" || s.Connection == nil ||
			s.Connection.Name != "configured" || !bounded || time.Until(end) > 5*time.Second || len(in.Sources) != 1 {
			return resourcev2.RelatedReadPage{}, resourcev2.NewError("INTERNAL", "binding lost")
		}
		if *native != *in.Root.Incarnation {
			return resourcev2.RelatedReadPage{}, resourcev2.NewError("PERMISSION_DENIED", "private uid and native error")
		}
		row := resourcev2.RelatedReadCandidate{Row: map[string]any{"text": "row"}, Evidence: []resourcev2.ResourceReference{in.Root}}
		rows := []resourcev2.RelatedReadCandidate{row}
		if oversize.Load() {
			row.Row["text"] = strings.Repeat("\x01", 4096) // each byte needs six on the wire
			rows = slices.Repeat(rows, 24)
		}
		return resourcev2.RelatedReadPage{Coverage: "complete", Sources: []string{"opaque/type"}, Rows: rows}, nil
	})
	plugin := &GRPCPlugin{Impl: c, OwnerID: "installed"}
	cc := ownerRPCPipe(t, plugin)
	catalog, err := c.Catalog(t.Context(), resourcev2.CatalogRequest{Route: testGet().Route})
	if err != nil {
		t.Fatal(err)
	}
	md := metadata.Pairs("omniview-extension-owner-bin", "installed", "omniview-extension-connection-bin", "explicit", "omniview-request-id-bin", "request",
		"omniview-requester-id-bin", testWindow, "omniview-resource-catalog-revision-bin", catalog.Revision)
	ctx := metadata.NewOutgoingContext(t.Context(), md)
	const root = `{"address":{"provider":"test","authority":"authority","type":"opaque/type","scope":[],"id":"opaque/id"},"incarnation":"native-uid"}`
	const request = `{"operation":"installed/links@1","route":{"connection":"explicit"},"root":` + root
	const target = `{"route":{"connection":"explicit"},"provider":"test","authority":"authority","type":"opaque/type","scope":[],"representation":"document"}`
	const read = request + `,"sources":[` + target + `]}`
	invoke := func(ctx context.Context, method, body string, opts ...grpc.CallOption) ([]byte, error) {
		out := new(wrapperspb.BytesValue)
		err := cc.Invoke(ctx, method, wrapperspb.Bytes([]byte(body)), out, opts...)
		return out.Value, err
	}
	ids, err := invoke(ctx, "/omniview.extension.v1.Owner/RelatedSourcesV1", request+`}`)
	if err != nil || string(ids) != `{"sources":["opaque/type"]}` || calls.Load() != 0 {
		t.Fatalf("static metadata = %s %v; inspections=%d", ids, err, calls.Load())
	}
	data, err := invoke(ctx, "/omniview.extension.v1.Owner/ReadRelatedV1", read)
	var page resourcev2.RelatedReadPage
	if err != nil || json.Unmarshal(data, &page, json.RejectUnknownMembers(true)) != nil || page.Coverage != "complete" ||
		len(page.Rows) != 1 || page.Rows[0].Row["text"] != "row" || calls.Load() != 1 {
		t.Fatalf("private page = %s %v", data, err)
	}
	for _, bad := range []string{
		request + `}`, request + `,"sources":null}`, request + `,"sources":[]}`, request + `,"Sources":[` + target + `]}`,
		request + `,"sources":[` + strings.Repeat(target+",", 8) + target + `]}`, read + `{}`,
		strings.Replace(read, `"operation":`, `"operation":"installed/other@1","operation":`, 1),
		strings.Replace(read, `"native-uid"`, `null`, 1), strings.Replace(read, `"root":`, `"extra":true,"root":`, 1),
		strings.Replace(read, `installed/links@1`, `other/links@1`, 1), strings.Replace(read, `"connection":"explicit"`, `"connection":"other"`, 1),
	} {
		if _, err := invoke(ctx, readRelatedMethod, bad); err == nil || calls.Load() != 1 {
			t.Fatal("invalid request dispatched")
		}
	}
	for _, key := range []string{relatedOwnerMD, relatedConnectionMD, requestIDMetadata, requesterIDMetadata, catalogRevisionMetadata} {
		for _, values := range [][]string{nil, {md.Get(key)[0], md.Get(key)[0]}} {
			bad := md.Copy()
			bad[key] = values
			if _, err := invoke(metadata.NewOutgoingContext(t.Context(), bad), readRelatedMethod, read); status.Code(err) != codes.PermissionDenied || calls.Load() != 1 {
				t.Fatalf("unbound metadata %s: %v", key, err)
			}
		}
	}
	stale := md.Copy()
	stale.Set(catalogRevisionMetadata, "stale")
	if _, err := invoke(metadata.NewOutgoingContext(t.Context(), stale), readRelatedMethod, read); status.Code(err) != codes.FailedPrecondition || calls.Load() != 1 {
		t.Fatalf("stale revision: %v", err)
	}
	for _, frame := range [][]byte{append(bytesField(1, []byte(read)), bytesField(1, []byte(read))...),
		append(bytesField(1, []byte(read)), bytesField(2, []byte("extra"))...)} {
		if _, err := invoke(ctx, readRelatedMethod, read, grpc.ForceCodecV2(rawCodec{data: frame})); status.Code(err) != codes.InvalidArgument || calls.Load() != 1 {
			t.Fatalf("ambiguous protobuf admitted: %v", err)
		}
	}
	maxRequest := read + strings.Repeat(" ", (16<<10)-3-len(read)) // one tag and two length bytes
	if _, err := invoke(ctx, readRelatedMethod, maxRequest); err != nil {
		t.Fatalf("exactly 16 KiB: %v", err)
	}
	if _, err := invoke(ctx, readRelatedMethod, maxRequest+" "); status.Code(err) != codes.ResourceExhausted || calls.Load() != 2 {
		t.Fatalf("oversized request materialized: %v", err)
	}
	if _, err := invoke(ctx, readRelatedMethod, strings.Replace(read, "native-uid", "stale-uid", 1)); status.Code(err) != codes.PermissionDenied || strings.Contains(err.Error(), "private") || len(status.Convert(err).Details()) != 0 {
		t.Fatalf("provider exception crossed wire: %v", err)
	}
	oversize.Store(true)
	if _, err := invoke(ctx, readRelatedMethod, read); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized response escaped: %v", err)
	}
	before := calls.Load()
	plain := grpc.NewServer(ServerOptions()...)
	if err := plugin.GRPCServer(nil, plain); err != nil {
		t.Fatal(err)
	}
	err = pipe(t, plain).Invoke(ctx, readRelatedMethod, wrapperspb.Bytes([]byte(read)), new(wrapperspb.BytesValue))
	if status.Code(err) != codes.Unauthenticated || calls.Load() != before {
		t.Fatalf("plaintext metadata accepted: %v", err)
	}

	oversize.Store(false)
	stall.Store(true)
	defer close(release)
	first, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := invoke(first, readRelatedMethod, read); finished <- err }()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("owner handler did not start: %v", err)
	}
	cancel()
	if err := <-finished; status.Code(err) != codes.Canceled {
		t.Fatalf("caller did not cancel independently of native handler: %v", err)
	}
	if _, err := invoke(ctx, readRelatedMethod, read); status.Code(err) != codes.Unavailable || calls.Load() != before+1 {
		t.Fatalf("second physical read after client cancellation: %v, calls=%d", err, calls.Load())
	}
}

func TestRelatedRegistrationIsOptionalAndEnvelopeBudgetIncludesFraming(t *testing.T) {
	s := grpc.NewServer(ServerOptions()...)
	if err := (&GRPCPlugin{Impl: &countProvider{}, OwnerID: "installed"}).GRPCServer(nil, s); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetServiceInfo()[relatedService]; ok {
		t.Fatal("unconfigured owner registered an extension service")
	}
	buf := relatedBuffer{limit: 512 << 10}
	if _, err := buf.Write(bytes.Repeat([]byte{'x'}, (512<<10)-4)); err != nil {
		t.Fatal("exactly 512 KiB refused", err)
	}
	if _, err := buf.Write([]byte{'x'}); err == nil {
		t.Fatal("protobuf envelope was not charged")
	}
}
