package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"

	_ "google.golang.org/grpc/encoding/gzip"
)

func transportSourceRequest() resourcev2.SourceOpenRequest {
	return resourcev2.SourceOpenRequest{SessionID: "session", Generation: "generation", Target: resourcev2.ReadTarget{Route: resourcev2.AccessRoute{Connection: "explicit"}, Provider: "test", Authority: "authority", Type: "opaque/type", Scope: resourcev2.QueryScope{}, Representation: "document"}, Mode: resourcev2.SourceModeSnapshot, Acquisition: resourcev2.SourceAcquisition{Kind: resourcev2.SourceAcquisitionEnumerate}, Limits: resourcev2.SourceLimits{MaxFrameBytes: 64 << 10, MaxBatchItems: 2}, Binding: resourcev2.RepresentationBinding{Representation: testRepresentation()}}
}

type transportSourceProvider struct {
	qualifiedProvider
	binding         testCatalogBinding
	callback        func(context.Context, resourcev2.SourceEmitter) error
	capabilities    []resourcev2.Capability
	features        *resourcev2.FeatureSet
	noSourceUpdates bool
}

func (p *transportSourceProvider) Features(context.Context) (resourcev2.FeatureSet, error) {
	if p.features != nil {
		return *p.features, nil
	}
	return resourcev2.FeatureSet{Traits: []resourcev2.Feature{resourcev2.FeatureSourceSessions}}, nil
}

func (p *transportSourceProvider) Catalog(ctx context.Context, r resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	c, err := p.qualifiedProvider.Catalog(ctx, r)
	c.Types[0].Capabilities = []resourcev2.Capability{{Operation: resourcev2.OperationEnumerate, Scope: resourcev2.QueryScope{}}, {Operation: resourcev2.OperationSourceUpdates, Scope: resourcev2.QueryScope{}, Guarantee: resourcev2.ContinuityBestEffort}}
	if p.noSourceUpdates {
		c.Types[0].Capabilities = c.Types[0].Capabilities[:1]
	}
	if p.capabilities != nil {
		c.Types[0].QueryDimensions = []resourcev2.Dimension{{Name: "region"}}
		c.Types[0].Capabilities = p.capabilities
	}
	if err != nil {
		return resourcev2.Catalog{}, err
	}
	return p.binding.bind(c)
}
func (p *transportSourceProvider) OpenSource(ctx context.Context, _ resourcev2.SourceOpenRequest, e resourcev2.SourceEmitter) error {
	return p.callback(ctx, e)
}

func TestSourceTransportSnapshotAndClose(t *testing.T) {
	p := &transportSourceProvider{callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
		return e.EndSnapshot(ctx, resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, resourcev2.ContinuityNotApplicable)
	}}
	server := grpc.NewServer()
	RegisterServer(server, p)
	c := NewClient(pipe(t, server))
	r := transportSourceRequest()
	ctx := acceptedCatalogContext(context.Background(), t, c, r.Target.Route)
	stream, err := c.OpenSourceStream(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := stream.Recv()
	if err != nil || begin.Begin == nil || begin.Sequence != 1 {
		t.Fatalf("begin: %#v %v", begin, err)
	}
	end, err := stream.Recv()
	if err != nil || end.SnapshotEnd == nil || end.Sequence != 2 {
		t.Fatalf("end: %#v %v", end, err)
	}
	if _, err = stream.Recv(); err != io.EOF {
		t.Fatalf("EOF: %v", err)
	}
	stream.Close()
	stream.Close()
	if _, err = stream.Recv(); resourcev2.ErrorCode(err) != "CANCELLED" {
		t.Fatalf("close: %v", err)
	}
}

func TestSourceTransportValueDepth(t *testing.T) {
	for _, shape := range []string{"map", "list", "mixed"} {
		for _, depth := range []int{resourcev2.MaxValueDepth, resourcev2.MaxValueDepth + 1} {
			t.Run(fmt.Sprintf("%s/%d", shape, depth), func(t *testing.T) {
				record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
				if err != nil {
					t.Fatal(err)
				}
				record.Body = nestedDepthValue(shape, depth)
				p := &transportSourceProvider{callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
					if err := e.Snapshot(ctx, []resourcev2.SourceObservation{{Record: resourcev2.CompactSourceRecord(record)}}); err != nil {
						return err
					}
					return e.EndSnapshot(ctx, resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, resourcev2.ContinuityNotApplicable)
				}}
				server := grpc.NewServer(ServerOptions()...)
				RegisterServer(server, p)
				c := sourceTCP(t, server)
				r := transportSourceRequest()
				ctx := acceptedCatalogContext(context.Background(), t, c, r.Target.Route)
				stream, err := c.OpenSourceStream(ctx, r)
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				var records []resourcev2.SourceRecord
				var failure string
				for {
					frame, err := stream.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if frame.Snapshot != nil {
						for _, observation := range frame.Snapshot.Observations {
							records = append(records, observation.Record)
						}
					}
					if frame.Failed != nil {
						failure = frame.Failed.Code
					}
				}
				if depth > resourcev2.MaxValueDepth {
					// Observations exceeding the value depth limit fail as RESOURCE_LIMIT.
					if failure != "RESOURCE_LIMIT" || len(records) != 0 {
						t.Fatalf("over-depth source: records=%d failure=%q", len(records), failure)
					}
					return
				}
				if failure != "" || len(records) != 1 {
					t.Fatalf("source: records=%d failure=%q", len(records), failure)
				}
				assertDepthLeaf(t, records[0].Body, depth)
			})
		}
	}
}

type hostileSourceStream struct {
	grpc.ClientStream
	frames []*resourcepb.SourceFrame
}

func (s *hostileSourceStream) RecvMsg(v any) error {
	if len(s.frames) == 0 {
		return io.EOF
	}
	e := v.(*envelope)
	proto.Merge(e.message, s.frames[0])
	s.frames = s.frames[1:]
	return nil
}
func TestSourceHostileFrames(t *testing.T) {
	r := transportSourceRequest()
	begin := func(seq uint64) *resourcepb.SourceFrame {
		return &resourcepb.SourceFrame{Version: resourcev2.WireVersion, SessionId: r.SessionID, Generation: r.Generation, Sequence: seq, Payload: &resourcepb.SourceFrame_Begin{Begin: &resourcepb.SourceBegin{Mode: r.Mode}}}
	}
	for _, tc := range []struct {
		name   string
		frames []*resourcepb.SourceFrame
		code   string
	}{
		{"sequence gap", []*resourcepb.SourceFrame{begin(2)}, "RESET_REQUIRED"},
		{"early EOF", nil, "RESET_REQUIRED"},
		{"duplicate begin", []*resourcepb.SourceFrame{begin(1), begin(2)}, "PROVIDER_ERROR"},
		{"version", []*resourcepb.SourceFrame{{Version: 1, SessionId: r.SessionID, Generation: r.Generation, Sequence: 1, Payload: &resourcepb.SourceFrame_Begin{Begin: &resourcepb.SourceBegin{Mode: r.Mode}}}}, "PROVIDER_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sourceClientStream{stream: &hostileSourceStream{frames: tc.frames}, cancel: func() {}, tracker: resourcev2.NewSourcePhaseTracker(r, "")}
			var err error
			for range len(tc.frames) {
				_, err = s.Recv()
				if err != nil {
					break
				}
			}
			if err == nil {
				_, err = s.Recv()
			}
			if resourcev2.ErrorCode(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestSourceWireRejectsOversizedBatchBeforeDecode(t *testing.T) {
	for _, m := range []proto.Message{&resourcepb.SourceSnapshot{}, &resourcepb.SourceChanges{}} {
		var raw []byte
		for range resourcev2.MaxSourceBatchItems + 1 {
			raw = protowire.AppendTag(raw, 1, protowire.BytesType)
			raw = protowire.AppendBytes(raw, nil)
		}
		if err := ValidateWire(raw, m.ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
			t.Fatalf("%T: %v", m, err)
		}
	}
}

type hostileSourceServer struct {
	resourcepb.UnimplementedResourceSourcesServer
}

func (hostileSourceServer) Open(r *resourcepb.SourceOpenRequest, stream resourcepb.ResourceSources_OpenServer) error {
	return stream.Send(&resourcepb.SourceFrame{Version: resourcev2.WireVersion, SessionId: r.GetSessionId(), Generation: r.GetGeneration(), Sequence: 2, Payload: &resourcepb.SourceFrame_Begin{Begin: &resourcepb.SourceBegin{Mode: r.GetMode()}}})
}
func TestSourceHostileServerSequence(t *testing.T) {
	server := grpc.NewServer()
	resourcepb.RegisterResourceProviderServer(server, &sourceCatalogServer{provider: &transportSourceProvider{}})
	resourcepb.RegisterResourceSourcesServer(server, hostileSourceServer{})
	client := NewClient(pipe(t, server))
	r := transportSourceRequest()
	ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
	stream, err := client.OpenSourceStream(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err = stream.Recv(); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("hostile sequence: %v", err)
	}
}

type sourceCatalogServer struct {
	resourcepb.UnimplementedResourceProviderServer
	provider *transportSourceProvider
}

func (s *sourceCatalogServer) Call(ctx context.Context, request *resourcepb.Request) (*resourcepb.Response, error) {
	if request.GetFeatures() != nil {
		features, err := s.provider.Features(ctx)
		if err != nil {
			return nil, err
		}
		traits := make([]string, len(features.Traits))
		for i, trait := range features.Traits {
			traits[i] = string(trait)
		}
		return &resourcepb.Response{Version: resourcev2.WireVersion, Result: &resourcepb.Response_Features{Features: &resourcepb.FeatureSet{Traits: traits}}}, nil
	}
	catalog, err := s.provider.Catalog(ctx, resourcev2.CatalogRequest{})
	if err != nil {
		return nil, err
	}
	p, err := resourcev2.ToProtoCatalog(catalog)
	return &resourcepb.Response{Version: resourcev2.WireVersion, Result: &resourcepb.Response_Catalog{Catalog: p}}, err
}

func sourceTCP(t *testing.T, server *grpc.Server) *Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = listener.Close() })
	return NewClient(conn)
}

func hostileTCP(t *testing.T, frames []*resourcepb.SourceFrame, compressed bool) *Client {
	t.Helper()
	server := grpc.NewServer(ServerOptions()...)
	resourcepb.RegisterResourceProviderServer(server, &sourceCatalogServer{provider: &transportSourceProvider{}})
	desc := resourcepb.ResourceSources_ServiceDesc
	desc.Streams = append([]grpc.StreamDesc(nil), desc.Streams...)
	desc.Streams[0].Handler = func(_ any, stream grpc.ServerStream) error {
		var request resourcepb.SourceOpenRequest
		if err := stream.RecvMsg(&request); err != nil {
			return err
		}
		if compressed {
			if err := grpc.SetSendCompressor(stream.Context(), "gzip"); err != nil {
				return err
			}
		}
		for _, frame := range frames {
			if err := stream.SendMsg(frame); err != nil {
				return err
			}
		}
		return nil
	}
	server.RegisterService(&desc, hostileSourceServer{})
	return sourceTCP(t, server)
}

func TestSourceHostileTCP(t *testing.T) {
	r := transportSourceRequest()
	r.Mode = resourcev2.SourceModeSnapshotAndUpdates
	makeFrame := func(seq uint64, payload any) *resourcepb.SourceFrame {
		f := &resourcepb.SourceFrame{Version: resourcev2.WireVersion, SessionId: r.SessionID, Generation: r.Generation, Sequence: seq}
		switch p := payload.(type) {
		case *resourcepb.SourceBegin:
			f.Payload = &resourcepb.SourceFrame_Begin{Begin: p}
		case *resourcepb.SourceSnapshot:
			f.Payload = &resourcepb.SourceFrame_Snapshot{Snapshot: p}
		case *resourcepb.SourceSnapshotEnd:
			f.Payload = &resourcepb.SourceFrame_SnapshotEnd{SnapshotEnd: p}
		case *resourcepb.SourceChanges:
			f.Payload = &resourcepb.SourceFrame_Changes{Changes: p}
		case *resourcepb.SourceReset:
			f.Payload = &resourcepb.SourceFrame_Reset_{Reset_: p}
		}
		return f
	}
	begin := func(n uint64) *resourcepb.SourceFrame { return makeFrame(n, &resourcepb.SourceBegin{Mode: r.Mode}) }
	end := func(n uint64, continuity string) *resourcepb.SourceFrame {
		return makeFrame(n, &resourcepb.SourceSnapshotEnd{Coverage: &resourcepb.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, Continuity: continuity})
	}
	record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
	if err != nil {
		t.Fatal(err)
	}
	record.Reference.Address.Authority = "escape"
	escaped, err := resourcev2.ToProtoSourceObservation(resourcev2.SourceObservation{Record: resourcev2.CompactSourceRecord(record)})
	if err != nil {
		t.Fatal(err)
	}
	echo := begin(1)
	echo.Generation = "wrong"
	items := make([]*resourcepb.SourceObservation, 3)
	for i := range items {
		items[i] = escaped
	}
	many := make([]*resourcepb.SourceObservation, resourcev2.MaxSourceBatchItems+1)
	for i := range many {
		many[i] = escaped
	}
	for _, tc := range []struct {
		name   string
		frames []*resourcepb.SourceFrame
		code   string
	}{
		{"invalid first", []*resourcepb.SourceFrame{makeFrame(1, &resourcepb.SourceSnapshot{})}, "PROVIDER_ERROR"},
		{"gap", []*resourcepb.SourceFrame{begin(1), end(3, resourcev2.ContinuityBestEffort)}, "RESET_REQUIRED"},
		{"duplicate", []*resourcepb.SourceFrame{begin(1), end(1, resourcev2.ContinuityBestEffort)}, "RESET_REQUIRED"},
		{"early changes", []*resourcepb.SourceFrame{begin(1), makeFrame(2, &resourcepb.SourceChanges{})}, "PROVIDER_ERROR"},
		{"late snapshot", []*resourcepb.SourceFrame{begin(1), end(2, resourcev2.ContinuityBestEffort), makeFrame(3, &resourcepb.SourceSnapshot{})}, "PROVIDER_ERROR"},
		{"after terminal", []*resourcepb.SourceFrame{begin(1), makeFrame(2, &resourcepb.SourceReset{Reason: resourcev2.SourceResetOverflow}), begin(3)}, "PROVIDER_ERROR"},
		{"echo", []*resourcepb.SourceFrame{echo}, "PROVIDER_ERROR"},
		{"scope escape", []*resourcepb.SourceFrame{begin(1), makeFrame(2, &resourcepb.SourceSnapshot{Observations: []*resourcepb.SourceObservation{escaped}})}, "PROVIDER_ERROR"},
		{"continuity", []*resourcepb.SourceFrame{begin(1), end(2, resourcev2.ContinuityGapFree)}, "PROVIDER_ERROR"},
		{"session batch", []*resourcepb.SourceFrame{begin(1), makeFrame(2, &resourcepb.SourceSnapshot{Observations: items})}, "RESOURCE_LIMIT"},
		{"wire batch", []*resourcepb.SourceFrame{begin(1), makeFrame(2, &resourcepb.SourceSnapshot{Observations: many})}, "RESOURCE_LIMIT"},
		{"following EOF", []*resourcepb.SourceFrame{begin(1), end(2, resourcev2.ContinuityBestEffort)}, "RESET_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := hostileTCP(t, tc.frames, false)
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			stream, err := client.OpenSourceStream(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for range len(tc.frames) + 1 {
				_, err = stream.Recv()
				if err != nil {
					break
				}
			}
			if resourcev2.ErrorCode(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestSourceHostileOversizedTCP(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "gzip"}[compressed], func(t *testing.T) {
			r := transportSourceRequest()
			frame := &resourcepb.SourceFrame{Version: resourcev2.WireVersion, SessionId: r.SessionID, Generation: r.Generation, Sequence: 1, Payload: &resourcepb.SourceFrame_Reset_{Reset_: &resourcepb.SourceReset{Reason: resourcev2.SourceResetOverflow, Message: strings.Repeat("x", int(r.Limits.MaxFrameBytes))}}}
			client := hostileTCP(t, []*resourcepb.SourceFrame{frame}, compressed)
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			stream, err := client.OpenSourceStream(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if _, err = stream.Recv(); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
				t.Fatalf("oversized frame: %v", err)
			}
		})
	}
}

type observedSourceServerStream struct {
	grpc.ServerStream
	sendExited chan struct{}
}

func (s *observedSourceServerStream) SendMsg(v any) error {
	err := s.ServerStream.SendMsg(v)
	if err != nil && s.Context().Err() != nil {
		close(s.sendExited)
	}
	return err
}

func TestSourceTCPStalledHost(t *testing.T) {
	old := sourceSendStallTimeout
	sourceSendStallTimeout = 200 * time.Millisecond
	defer func() { sourceSendStallTimeout = old }()
	cancelled := make(chan error, 1)
	record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
	if err != nil {
		t.Fatal(err)
	}
	record.Body = resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{"payload": {Kind: "string", String: strings.Repeat("x", 16<<10)}}}
	p := &transportSourceProvider{callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
		for {
			if err := e.Snapshot(ctx, []resourcev2.SourceObservation{{Record: resourcev2.CompactSourceRecord(record)}}); err != nil {
				cancelled <- err
				return err
			}
		}
	}}
	handlerDone := make(chan struct{})
	transportDone := make(chan struct{})
	sendExited := make(chan struct{})
	options := append(ServerOptions(), grpc.ChainStreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if info.FullMethod != resourcepb.ResourceSources_Open_FullMethodName {
			return handler(srv, stream)
		}
		go func() {
			<-stream.Context().Done()
			close(transportDone)
		}()
		defer close(handlerDone)
		return handler(srv, &observedSourceServerStream{ServerStream: stream, sendExited: sendExited})
	}))
	server := grpc.NewServer(options...)
	RegisterServer(server, p)
	client := sourceTCP(t, server)
	r := transportSourceRequest()
	defer server.Stop()
	ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
	stream, err := client.OpenSourceStream(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case err := <-cancelled:
		if resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
			t.Errorf("stalled emitter: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("stalled host did not cancel provider")
	}
	// The server must close the RPC without waiting for the host to read.
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("stalled source handler retained")
	}
	select {
	case <-transportDone:
	case <-time.After(time.Second):
		t.Fatal("stalled source transport retained")
	}
	select {
	case <-sendExited:
	case <-time.After(time.Second):
		t.Fatal("blocked SendMsg did not exit after RPC termination")
	}
	// The stalled source must not tear down other RPCs on the same connection.
	_ = acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
}

func TestSourceTCPCloseCancelsProvider(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	p := &transportSourceProvider{callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
		if err := e.Snapshot(ctx, nil); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}}
	server := grpc.NewServer(ServerOptions()...)
	RegisterServer(server, p)
	client := sourceTCP(t, server)
	r := transportSourceRequest()
	ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
	stream, err := client.OpenSourceStream(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	done := make(chan struct{})
	go func() { stream.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Close blocked")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("provider context not cancelled")
	}
}

func TestSourceTCPReceiveCancellation(t *testing.T) {
	for _, closeStream := range []bool{false, true} {
		t.Run(map[bool]string{false: "context", true: "close"}[closeStream], func(t *testing.T) {
			started := make(chan struct{})
			providerDone := make(chan struct{})
			server := grpc.NewServer(ServerOptions()...)
			RegisterServer(server, &transportSourceProvider{callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
				if err := e.Snapshot(ctx, nil); err != nil {
					return err
				}
				close(started)
				<-ctx.Done()
				close(providerDone)
				return ctx.Err()
			}})
			client := sourceTCP(t, server)
			r := transportSourceRequest()
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			stream, err := client.OpenSourceStream(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if _, err = stream.Recv(); err != nil {
				t.Fatal(err)
			}
			<-started
			received := make(chan error, 1)
			go func() { _, err := stream.Recv(); received <- err }()
			if closeStream {
				stream.Close()
			} else {
				cancel()
			}
			select {
			case err := <-received:
				if resourcev2.ErrorCode(err) != "CANCELLED" {
					t.Fatalf("receive: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("receive did not unblock")
			}
			select {
			case <-providerDone:
			case <-time.After(time.Second):
				t.Fatal("provider did not cancel")
			}
		})
	}
}

func TestSourceTCPPhaseDeadlines(t *testing.T) {
	oldBegin := sourceBeginTimeout
	sourceBeginTimeout = 250 * time.Millisecond
	const progressTimeout = 250 * time.Millisecond
	defer func() { sourceBeginTimeout = oldBegin }()
	for _, phase := range []string{"begin", "snapshot", "following"} {
		t.Run(phase, func(t *testing.T) {
			providerDone := make(chan struct{})
			server := grpc.NewServer(ServerOptions()...)
			RegisterServer(server, &transportSourceProvider{callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
				if phase != "begin" {
					if err := e.Snapshot(ctx, nil); err != nil {
						return err
					}
				}
				if phase == "following" {
					if err := e.EndSnapshot(ctx, resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, resourcev2.ContinuityBestEffort); err != nil {
						return err
					}
				}
				<-ctx.Done()
				close(providerDone)
				return ctx.Err()
			}})
			client := sourceTCP(t, server)
			r := transportSourceRequest()
			r.Mode = resourcev2.SourceModeSnapshotAndUpdates
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			ctx, err := WithSourceSnapshotProgressTimeout(ctx, progressTimeout)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := client.OpenSourceStream(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if phase != "begin" {
				if frame, err := stream.Recv(); err != nil || frame.Begin == nil {
					t.Fatalf("begin: %#v %v", frame, err)
				}
			}
			if phase == "following" {
				if frame, err := stream.Recv(); err != nil || frame.SnapshotEnd == nil {
					t.Fatalf("boundary: %#v %v", frame, err)
				}
			}
			received := make(chan error, 1)
			go func() { _, err := stream.Recv(); received <- err }()
			if phase == "following" {
				select {
				case err := <-received:
					t.Fatalf("idle following ended: %v", err)
				case <-time.After(2 * progressTimeout):
				}
				stream.Close()
			} else {
				select {
				case err := <-received:
					if resourcev2.ErrorCode(err) != "TIMEOUT" {
						t.Fatalf("deadline: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("deadline did not cancel receive")
				}
			}
			select {
			case <-providerDone:
			case <-time.After(time.Second):
				t.Fatal("deadline did not cancel provider")
			}
		})
	}
}

func TestSourceCallbackTerminationTCP(t *testing.T) {
	for _, tc := range []struct {
		name     string
		callback func(context.Context, resourcev2.SourceEmitter) error
		kind     string
	}{
		{"before boundary", func(ctx context.Context, e resourcev2.SourceEmitter) error { return e.Snapshot(ctx, nil) }, "failed"},
		{"after boundary", func(ctx context.Context, e resourcev2.SourceEmitter) error {
			return e.EndSnapshot(ctx, resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, resourcev2.ContinuityBestEffort)
		}, "reset"},
		{"after reset", func(ctx context.Context, e resourcev2.SourceEmitter) error {
			return e.Reset(ctx, resourcev2.SourceResetOverflow, "overflow")
		}, "reset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := grpc.NewServer(ServerOptions()...)
			RegisterServer(server, &transportSourceProvider{callback: tc.callback})
			client := sourceTCP(t, server)
			r := transportSourceRequest()
			r.Mode = resourcev2.SourceModeSnapshotAndUpdates
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			stream, err := client.OpenSourceStream(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for {
				frame, err := stream.Recv()
				if err != nil {
					t.Fatalf("before terminal: %v", err)
				}
				if frame.Failed != nil {
					if tc.kind != "failed" || frame.Failed.Code != "INTERNAL" || frame.Failed.Message != "source ended before snapshot boundary" {
						t.Fatalf("failed: %#v", frame.Failed)
					}
					break
				}
				if frame.Reset != nil {
					if tc.kind != "reset" {
						t.Fatalf("reset: %#v", frame.Reset)
					}
					break
				}
			}
			if _, err := stream.Recv(); err != io.EOF {
				t.Fatalf("terminal EOF: %v", err)
			}
		})
	}
}

func TestSourceEmitterLimitsAndPhases(t *testing.T) {
	record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
	if err != nil {
		t.Fatal(err)
	}
	large := record
	large.Body = resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{"payload": {Kind: "string", String: strings.Repeat("x", 64<<10)}}}
	medium := record
	medium.Body = resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{"payload": {Kind: "string", String: strings.Repeat("x", 40<<10)}}}
	coverage := resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}
	for _, tc := range []struct {
		name     string
		mode     string
		callback func(context.Context, resourcev2.SourceEmitter) error
		sizes    []int
		code     string
	}{
		{"snapshot batch splitting", resourcev2.SourceModeSnapshot, func(ctx context.Context, e resourcev2.SourceEmitter) error {
			observations := make([]resourcev2.SourceObservation, 5)
			for i := range observations {
				observations[i].Record = resourcev2.CompactSourceRecord(record)
			}
			if err := e.Snapshot(ctx, observations); err != nil {
				return err
			}
			return e.EndSnapshot(ctx, coverage, resourcev2.ContinuityNotApplicable)
		}, []int{2, 2, 1}, ""},
		{"snapshot byte splitting", resourcev2.SourceModeSnapshot, func(ctx context.Context, e resourcev2.SourceEmitter) error {
			observations := []resourcev2.SourceObservation{{Record: resourcev2.CompactSourceRecord(medium)}, {Record: resourcev2.CompactSourceRecord(medium)}}
			if err := e.Snapshot(ctx, observations); err != nil {
				return err
			}
			return e.EndSnapshot(ctx, coverage, resourcev2.ContinuityNotApplicable)
		}, []int{1, 1}, ""},
		{"oversized observation", resourcev2.SourceModeSnapshot, func(ctx context.Context, e resourcev2.SourceEmitter) error {
			return e.Snapshot(ctx, []resourcev2.SourceObservation{{Record: resourcev2.CompactSourceRecord(large)}})
		}, nil, "RESOURCE_LIMIT"},
		{"changes before boundary", resourcev2.SourceModeSnapshotAndUpdates, func(ctx context.Context, e resourcev2.SourceEmitter) error {
			return e.Changes(ctx, nil, nil)
		}, nil, "PROVIDER_ERROR"},
		{"changes exceed atomic limit", resourcev2.SourceModeSnapshotAndUpdates, func(ctx context.Context, e resourcev2.SourceEmitter) error {
			if err := e.EndSnapshot(ctx, coverage, resourcev2.ContinuityBestEffort); err != nil {
				return err
			}
			observation := resourcev2.SourceObservation{Record: resourcev2.CompactSourceRecord(record)}
			change := resourcev2.SourceChange{Kind: resourcev2.SourceChangeUpsert, Observation: &observation}
			return e.Changes(ctx, []resourcev2.SourceChange{change, change, change}, nil)
		}, nil, "RESOURCE_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := grpc.NewServer(ServerOptions()...)
			RegisterServer(server, &transportSourceProvider{callback: tc.callback})
			client := sourceTCP(t, server)
			r := transportSourceRequest()
			r.Mode = tc.mode
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			stream, err := client.OpenSourceStream(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var sizes []int
			var failed string
			for {
				frame, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if frame.Snapshot != nil {
					sizes = append(sizes, len(frame.Snapshot.Observations))
				}
				if frame.Changes != nil {
					t.Fatal("unexpected partial changes frame")
				}
				if frame.Failed != nil {
					failed = frame.Failed.Code
				}
			}
			if failed != tc.code {
				t.Fatalf("failure %q, want %q", failed, tc.code)
			}
			if len(sizes) != len(tc.sizes) {
				t.Fatalf("batch sizes %v, want %v", sizes, tc.sizes)
			}
			for i := range sizes {
				if sizes[i] != tc.sizes[i] {
					t.Fatalf("batch sizes %v, want %v", sizes, tc.sizes)
				}
			}
		})
	}
}

func TestSourceSnapshotWireNodeBudget(t *testing.T) {
	record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
	if err != nil {
		t.Fatal(err)
	}
	managedFields := make(map[string]any, 1000)
	for i := range 1000 {
		managedFields[fmt.Sprintf("field-%04d", i)] = map[string]any{"owner": "controller"}
	}
	record.Body, err = resourcev2.ValueFromNative(map[string]any{
		"metadata": map[string]any{"managedFields": []any{managedFields}},
	})
	if err != nil {
		t.Fatal(err)
	}
	observations := make([]resourcev2.SourceObservation, 191)
	for i := range observations {
		observations[i].Record = resourcev2.CompactSourceRecord(record)
	}
	request := transportSourceRequest()
	request.Limits.MaxBatchItems = 256
	request.Limits.MaxFrameBytes = resourcev2.MaxSourceFrameBytes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitter := &sourceEmitter{ctx: ctx, request: request, tracker: resourcev2.NewSourcePhaseTracker(request, ""), queue: make(chan sourceDelivery), stalled: make(chan struct{})}
	deliveries := make(chan *resourcepb.SourceFrame, 192)
	go func() {
		for {
			select {
			case delivery := <-emitter.queue:
				deliveries <- delivery.frame
				delivery.ack <- nil
			case <-ctx.Done():
				return
			}
		}
	}()
	if err := emitter.Snapshot(ctx, observations); err != nil {
		t.Fatal(err)
	}
	close(deliveries)
	var sequence uint64
	var snapshots, count int
	for frame := range deliveries {
		sequence++
		if frame.Sequence != sequence {
			t.Fatalf("sequence = %d, want %d", frame.Sequence, sequence)
		}
		data, err := proto.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		budget := wireBudget{source: true, sourceBatchLimit: resourcev2.MaxSourceBatchItems}
		if err := budget.message(data, frame.ProtoReflect().Descriptor(), 0); err != nil {
			t.Fatal(err)
		}
		nodes, fields := protoWireCost(frame.ProtoReflect())
		if nodes != budget.nodes || fields != budget.fields {
			t.Fatalf("cost = (%d, %d), receiver = (%d, %d)", nodes, fields, budget.nodes, budget.fields)
		}
		if err := ValidateWire(data, frame.ProtoReflect().Descriptor()); err != nil {
			t.Fatal(err)
		}
		if frame.GetSnapshot() != nil {
			snapshots++
			count += len(frame.GetSnapshot().Observations)
		}
	}
	if snapshots < 2 || count != len(observations) {
		t.Fatalf("got %d snapshots and %d observations", snapshots, count)
	}
}

func TestSourceEmitterRejectsWireComplexityOversize(t *testing.T) {
	record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
	if err != nil {
		t.Fatal(err)
	}
	makeObservation := func(count int) resourcev2.SourceObservation {
		values := make([]resourcev2.Value, count)
		for i := range values {
			values[i].Kind = "null"
		}
		item := record
		item.Body = resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{
			"items": {Kind: "list", List: values},
		}}
		return resourcev2.SourceObservation{Record: resourcev2.CompactSourceRecord(item)}
	}
	for _, tc := range []struct {
		name         string
		observations int
		values       int
	}{
		{"single snapshot observation", 1, 132000},
		{"atomic changes", 2, 70000},
		{"aggregate value admission", 3, 90000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation := makeObservation(tc.values)
			encoded, err := resourcev2.ToProtoSourceObservation(observation)
			if err != nil {
				t.Fatal(err)
			}
			nodes, _ := protoWireCost(encoded.ProtoReflect())
			if nodes*tc.observations <= MaxNodes {
				t.Fatalf("fixture has %d nodes, want over %d", nodes*tc.observations, MaxNodes)
			}
			request := transportSourceRequest()
			request.Limits.MaxBatchItems = 256
			request.Limits.MaxFrameBytes = resourcev2.MaxSourceFrameBytes
			request.Mode = resourcev2.SourceModeSnapshotAndUpdates
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			emitter := &sourceEmitter{ctx: ctx, request: request, tracker: resourcev2.NewSourcePhaseTracker(request, resourcev2.ContinuityBestEffort), queue: make(chan sourceDelivery), stalled: make(chan struct{})}
			deliveries := make(chan *resourcepb.SourceFrame, 4)
			go func() {
				for {
					select {
					case delivery := <-emitter.queue:
						deliveries <- delivery.frame
						delivery.ack <- nil
					case <-ctx.Done():
						return
					}
				}
			}()
			if tc.observations == 1 {
				err = emitter.Snapshot(ctx, []resourcev2.SourceObservation{observation})
			} else {
				coverage := resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}
				if err = emitter.EndSnapshot(ctx, coverage, resourcev2.ContinuityBestEffort); err != nil {
					t.Fatal(err)
				}
				changes := make([]resourcev2.SourceChange, tc.observations)
				for i := range changes {
					changes[i] = resourcev2.SourceChange{Kind: resourcev2.SourceChangeUpsert, Observation: &observation}
				}
				err = emitter.Changes(ctx, changes, nil)
			}
			if resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
				t.Fatalf("got %v, want RESOURCE_LIMIT", err)
			}
			close(deliveries)
			for frame := range deliveries {
				if frame.GetSnapshot() != nil || frame.GetChanges() != nil {
					t.Fatal("oversized payload was delivered")
				}
			}
		})
	}
	t.Run("single observation aggregate value admission", func(t *testing.T) {
		observation := makeObservation(resourcev2.MaxValueNodes + 1)
		ctx := context.Background()
		request := transportSourceRequest()
		emitter := &sourceEmitter{ctx: ctx, request: request, sequence: 1}
		err := emitter.Snapshot(ctx, []resourcev2.SourceObservation{observation})
		if resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
			t.Fatalf("got %v, want RESOURCE_LIMIT", err)
		}
	})
}

func TestFinitePageIncrementalWireCost(t *testing.T) {
	record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
	if err != nil {
		t.Fatal(err)
	}
	record.Body, err = resourcev2.ValueFromNative(map[string]any{
		"metadata": map[string]any{"managedFields": []any{map[string]any{"manager": "controller", "fields": []any{"spec", "status"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, cursorSize := range []int{0, 1, 127, 128, 16384} {
		t.Run(fmt.Sprintf("cursor-%d", cursorSize), func(t *testing.T) {
			page := resourcev2.SourcePage{Count: resourcev2.Count{Kind: "unknown"}, Coverage: resourcev2.Coverage{Kind: "complete"}, Consistency: resourcev2.ConsistencyBestEffort}
			if cursorSize > 0 {
				cursor := strings.Repeat("c", cursorSize)
				page.NextCursor = &cursor
			}
			payload, nodes, fields, err := resourcev2.SourcePageWireCost(page)
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				bytes, n, f, err := resourcev2.SourceRecordWireCost(record)
				if err != nil {
					t.Fatal(err)
				}
				payload += bytes
				nodes += n
				fields += f
				page.Records = append(page.Records, record)
			}
			wirePage, err := resourcev2.ToProtoSourcePage(page)
			if err != nil {
				t.Fatal(err)
			}
			response := &resourcepb.Response{Version: resourcev2.WireVersion, Result: &resourcepb.Response_Page{Page: wirePage}}
			data, err := proto.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			budget := wireBudget{}
			if err := budget.message(data, response.ProtoReflect().Descriptor(), 0); err != nil {
				t.Fatal(err)
			}
			if payload != proto.Size(wirePage) || resourcev2.SourcePageResponseWireSize(payload) != len(data) || nodes != budget.nodes || fields != budget.fields {
				t.Fatalf("incremental=(%d,%d,%d,%d), encoded=(%d,%d,%d,%d)", payload, resourcev2.SourcePageResponseWireSize(payload), nodes, fields, proto.Size(wirePage), len(data), budget.nodes, budget.fields)
			}
		})
	}
}

type sourceStatusStream struct {
	grpc.ClientStream
	err error
}

func (s *sourceStatusStream) RecvMsg(any) error { return s.err }

func TestSourceOpenStatusBeforeBegin(t *testing.T) {
	for _, code := range []string{"INVALID_REQUEST", "UNSUPPORTED", "RESOURCE_LIMIT"} {
		t.Run(code, func(t *testing.T) {
			r := transportSourceRequest()
			s := &sourceClientStream{stream: &sourceStatusStream{err: rpcError(resourcev2.NewError(code, "rejected"))}, cancel: func() {}, tracker: resourcev2.NewSourcePhaseTracker(r, "")}
			if _, err := s.Recv(); resourcev2.ErrorCode(err) != code {
				t.Fatalf("got %v, want %s", err, code)
			}
		})
	}
}

func TestSourceFatalFramingCause(t *testing.T) {
	statusWithCause, err := status.New(codes.Unavailable, "transport closed").WithDetails(&errdetails.ErrorInfo{Domain: grpc.ReceivePolicyErrorDomain, Reason: grpc.PartialHTTP2DataFrameTimeoutReason})
	if err != nil {
		t.Fatal(err)
	}
	request := transportSourceRequest()
	stream := &sourceClientStream{stream: &sourceStatusStream{err: statusWithCause.Err()}, cancel: func() {}, tracker: resourcev2.NewSourcePhaseTracker(request, "")}
	_, err = stream.Recv()
	if resourcev2.ErrorCode(err) != "RESET_REQUIRED" || !IsFatalSourceTransportError(err) {
		t.Fatalf("fatal framing cause lost: %v", err)
	}
	if IsFatalSourceTransportError(status.Error(codes.DeadlineExceeded, "partial message timeout")) {
		t.Fatal("ordinary per-RPC timeout classified as fatal framing")
	}
}

func TestSourceOpenFailureBeforeBeginTCP(t *testing.T) {
	for _, code := range []string{"INVALID_REQUEST", "UNSUPPORTED", "RESOURCE_LIMIT"} {
		t.Run(code, func(t *testing.T) {
			server := grpc.NewServer(ServerOptions()...)
			RegisterServer(server, &transportSourceProvider{callback: func(context.Context, resourcev2.SourceEmitter) error {
				return resourcev2.NewError(code, "rejected")
			}})
			client := sourceTCP(t, server)
			r := transportSourceRequest()
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			stream, err := client.OpenSourceStream(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if _, err := stream.Recv(); resourcev2.ErrorCode(err) != code {
				t.Fatalf("got %v, want %s", err, code)
			}
		})
	}
}

func TestSourceBindingMismatchTCP(t *testing.T) {
	server := grpc.NewServer(ServerOptions()...)
	RegisterServer(server, &transportSourceProvider{callback: func(context.Context, resourcev2.SourceEmitter) error {
		t.Error("provider called for mismatched binding")
		return nil
	}})
	client := sourceTCP(t, server)
	r := transportSourceRequest()
	r.Binding.Representation.Revision = "other-revision"
	ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
	if _, err := client.OpenSourceStream(ctx, r); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("client admission: %v", err)
	}
	wire, err := resourcev2.ToProtoSourceOpenRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = outgoingSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.conn.NewStream(ctx, &resourcepb.ResourceSources_ServiceDesc.Streams[0], resourcepb.ResourceSources_Open_FullMethodName)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.SendMsg(wire); err != nil {
		t.Fatal(err)
	}
	if err = stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	err = stream.RecvMsg(&resourcepb.SourceFrame{})
	if resourcev2.ErrorCode(domainError(err)) != "RESET_REQUIRED" {
		t.Fatalf("server admission: %v", err)
	}
}

func TestSourceFeatureAndCapabilityAdmissionTCP(t *testing.T) {
	legacy := resourcev2.FeatureSet{Traits: []resourcev2.Feature{"bestEffortUpdates"}}
	for _, tc := range []struct {
		name     string
		provider *transportSourceProvider
		mode     string
	}{
		{"legacy snapshot", &transportSourceProvider{features: &legacy}, resourcev2.SourceModeSnapshot},
		{"legacy updates", &transportSourceProvider{features: &legacy}, resourcev2.SourceModeSnapshotAndUpdates},
		{"missing update capability", &transportSourceProvider{noSourceUpdates: true}, resourcev2.SourceModeSnapshotAndUpdates},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.provider.callback = func(context.Context, resourcev2.SourceEmitter) error {
				t.Error("provider called without source admission")
				return nil
			}
			server := grpc.NewServer(ServerOptions()...)
			RegisterServer(server, tc.provider)
			client := sourceTCP(t, server)
			r := transportSourceRequest()
			r.Mode = tc.mode
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			if _, err := client.OpenSourceStream(ctx, r); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
				t.Fatalf("client admission: %v", err)
			}
			wire, err := resourcev2.ToProtoSourceOpenRequest(r)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err = outgoingSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := client.conn.NewStream(ctx, &resourcepb.ResourceSources_ServiceDesc.Streams[0], resourcepb.ResourceSources_Open_FullMethodName)
			if err != nil {
				t.Fatal(err)
			}
			if err = stream.SendMsg(wire); err != nil {
				t.Fatal(err)
			}
			if err = stream.CloseSend(); err != nil {
				t.Fatal(err)
			}
			if err = stream.RecvMsg(&resourcepb.SourceFrame{}); resourcev2.ErrorCode(domainError(err)) != "UNSUPPORTED" {
				t.Fatalf("server admission: %v", err)
			}
		})
	}
}

func TestSourceTerminalCancelsProvider(t *testing.T) {
	cancelled := make(chan struct{})
	p := &transportSourceProvider{callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
		if err := e.Reset(ctx, resourcev2.SourceResetOverflow, "overflow"); err != nil {
			return err
		}
		<-ctx.Done()
		close(cancelled)
		return nil
	}}
	server := grpc.NewServer()
	RegisterServer(server, p)
	client := NewClient(pipe(t, server))
	r := transportSourceRequest()
	r.Mode = resourcev2.SourceModeSnapshotAndUpdates
	ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
	stream, err := client.OpenSourceStream(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for range 2 {
		if _, err := stream.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("terminal EOF: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("terminal did not cancel provider")
	}
}

func TestSourceCallerCancellationAfterAdmission(t *testing.T) {
	r := transportSourceRequest()
	session, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()
	e := &sourceEmitter{ctx: session, request: r, tracker: resourcev2.NewSourcePhaseTracker(r, ""), queue: make(chan sourceDelivery), stalled: make(chan struct{})}
	caller, cancelCaller := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Snapshot(caller, nil) }()
	d := <-e.queue
	cancelCaller()
	d.ack <- nil
	if err := <-done; err != nil {
		t.Fatalf("admitted frame abandoned: %v", err)
	}
	if d.frame.Sequence != 1 {
		t.Fatalf("sequence: %d", d.frame.Sequence)
	}
}

func TestSourceInvalidProviderCoverage(t *testing.T) {
	coverage := resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort, Limitations: make([]string, resourcev2.MaxSourceLimitations+1)}
	for i := range coverage.Limitations {
		coverage.Limitations[i] = "limit"
	}
	p := &transportSourceProvider{callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
		return e.EndSnapshot(ctx, coverage, resourcev2.ContinuityNotApplicable)
	}}
	server := grpc.NewServer()
	RegisterServer(server, p)
	client := NewClient(pipe(t, server))
	r := transportSourceRequest()
	ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
	stream, err := client.OpenSourceStream(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	frame, err := stream.Recv()
	if err != nil || frame.Failed == nil || frame.Failed.Code != "PROVIDER_ERROR" {
		t.Fatalf("failure: %#v %v", frame.Failed, err)
	}
}

func BenchmarkSourceSnapshotSplit(b *testing.B) {
	record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
	if err != nil {
		b.Fatal(err)
	}
	record.Body = resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{"payload": {Kind: "string", String: strings.Repeat("x", 2<<10)}}}
	observations := make([]resourcev2.SourceObservation, 5120)
	for i := range observations {
		observations[i].Record = resourcev2.CompactSourceRecord(record)
	}
	r := transportSourceRequest()
	r.Limits.MaxBatchItems = 256
	r.Limits.MaxFrameBytes = resourcev2.MaxSourceFrameBytes
	b.ResetTimer()
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		e := &sourceEmitter{ctx: ctx, request: r, tracker: resourcev2.NewSourcePhaseTracker(r, ""), queue: make(chan sourceDelivery), stalled: make(chan struct{})}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case d := <-e.queue:
					d.ack <- nil
				case <-ctx.Done():
					return
				}
			}
		}()
		err := e.Snapshot(ctx, observations)
		cancel()
		<-done
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestSourceScopedContinuityTransport(t *testing.T) {
	west, east := []string{"west"}, []string{"east"}
	scope := func(values *[]string) resourcev2.QueryScope {
		return resourcev2.QueryScope{{Dimension: "region", Match: resourcev2.ScopeOneOf, Values: values}}
	}
	provider := &transportSourceProvider{
		capabilities: []resourcev2.Capability{
			{Operation: resourcev2.OperationEnumerate, Scope: scope(&east)},
			{Operation: resourcev2.OperationSourceUpdates, Scope: scope(&west), Guarantee: resourcev2.ContinuityGapFree},
			{Operation: resourcev2.OperationSourceUpdates, Scope: scope(&east), Guarantee: resourcev2.ContinuityBestEffort},
		},
		callback: func(ctx context.Context, e resourcev2.SourceEmitter) error {
			return e.EndSnapshot(ctx, resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, resourcev2.ContinuityGapFree)
		},
	}
	r := transportSourceRequest()
	r.Mode = resourcev2.SourceModeSnapshotAndUpdates
	r.Target.Scope = scope(&east)
	for _, hostile := range []bool{false, true} {
		t.Run(map[bool]string{false: "emitter", true: "client"}[hostile], func(t *testing.T) {
			server := grpc.NewServer()
			if hostile {
				resourcepb.RegisterResourceProviderServer(server, &sourceCatalogServer{provider: provider})
				desc := resourcepb.ResourceSources_ServiceDesc
				desc.Streams = append([]grpc.StreamDesc(nil), desc.Streams...)
				desc.Streams[0].Handler = func(_ any, stream grpc.ServerStream) error {
					var request resourcepb.SourceOpenRequest
					if err := stream.RecvMsg(&request); err != nil {
						return err
					}
					begin := &resourcepb.SourceFrame{Version: resourcev2.WireVersion, SessionId: r.SessionID, Generation: r.Generation, Sequence: 1, Payload: &resourcepb.SourceFrame_Begin{Begin: &resourcepb.SourceBegin{Mode: r.Mode}}}
					if err := stream.SendMsg(begin); err != nil {
						return err
					}
					end := &resourcepb.SourceFrame{Version: resourcev2.WireVersion, SessionId: r.SessionID, Generation: r.Generation, Sequence: 2, Payload: &resourcepb.SourceFrame_SnapshotEnd{SnapshotEnd: &resourcepb.SourceSnapshotEnd{Coverage: &resourcepb.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, Continuity: resourcev2.ContinuityGapFree}}}
					return stream.SendMsg(end)
				}
				server.RegisterService(&desc, hostileSourceServer{})
			} else {
				RegisterServer(server, provider)
			}
			client := NewClient(pipe(t, server))
			ctx := acceptedCatalogContext(context.Background(), t, client, r.Target.Route)
			stream, err := client.OpenSourceStream(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if _, err = stream.Recv(); err != nil {
				t.Fatal(err)
			}
			frame, err := stream.Recv()
			if hostile {
				if resourcev2.ErrorCode(err) != "PROVIDER_ERROR" {
					t.Fatalf("client accepted gap-free claim: %v", err)
				}
			} else if err != nil || frame.Failed == nil || frame.Failed.Code != "PROVIDER_ERROR" {
				t.Fatalf("emitter accepted gap-free claim: %#v %v", frame.Failed, err)
			}
		})
	}
}

func TestSourceTransportHostileValueDepth(t *testing.T) {
	for _, depth := range []int{21, resourcev2.MaxValueDepth - 1, resourcev2.MaxValueDepth, resourcev2.MaxValueDepth + 1} {
		t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
			value := resourcev2.Value{Kind: "bool"}
			wireValue := &resourcepb.Value{Selection: &resourcepb.Value_BoolValue{}}
			for range depth {
				value = resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{"x": value}}
				wireValue = &resourcepb.Value{Selection: &resourcepb.Value_MapValue{
					MapValue: &resourcepb.MapValue{Entries: []*resourcepb.ValueEntry{{Key: "x", Value: wireValue}}},
				}}
			}
			record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
			if err != nil {
				t.Fatal(err)
			}
			observation, err := resourcev2.ToProtoSourceObservation(resourcev2.SourceObservation{Record: resourcev2.CompactSourceRecord(record)})
			if err != nil {
				t.Fatal(err)
			}
			observation.Record.Body = wireValue
			request := transportSourceRequest()
			begin := &resourcepb.SourceFrame{
				Version: resourcev2.WireVersion, SessionId: request.SessionID, Generation: request.Generation, Sequence: 1,
				Payload: &resourcepb.SourceFrame_Begin{Begin: &resourcepb.SourceBegin{Mode: request.Mode}},
			}
			snapshot := &resourcepb.SourceFrame{
				Version: resourcev2.WireVersion, SessionId: request.SessionID, Generation: request.Generation, Sequence: 2,
				Payload: &resourcepb.SourceFrame_Snapshot{Snapshot: &resourcepb.SourceSnapshot{Observations: []*resourcepb.SourceObservation{observation}}},
			}
			raw, err := proto.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateWire(raw, snapshot.ProtoReflect().Descriptor()); err != nil {
				t.Fatalf("raw protobuf wrappers rejected semantic depth %d: %v", depth, err)
			}
			client := hostileTCP(t, []*resourcepb.SourceFrame{begin, snapshot}, false)
			ctx := acceptedCatalogContext(context.Background(), t, client, request.Target.Route)
			stream, err := client.OpenSourceStream(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if _, err = stream.Recv(); err != nil {
				t.Fatal(err)
			}
			frame, err := stream.Recv()
			if depth > resourcev2.MaxValueDepth {
				if resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
					t.Fatalf("expected semantic depth rejection, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if frame.Snapshot == nil || len(frame.Snapshot.Observations) != 1 || !reflect.DeepEqual(frame.Snapshot.Observations[0].Record.Body, value) {
				t.Fatal("valid nested source body changed in transit")
			}
		})
	}
}

func TestSourceUpdatesDoNotRequireFiniteEnumeration(t *testing.T) {
	scope := resourcev2.QueryScope{{Dimension: "region", Match: resourcev2.ScopeAny}}
	provider := &transportSourceProvider{
		capabilities: []resourcev2.Capability{{
			Operation: resourcev2.OperationSourceUpdates, Scope: scope, Guarantee: resourcev2.ContinuityBestEffort,
		}},
		callback: func(ctx context.Context, emitter resourcev2.SourceEmitter) error {
			return emitter.EndSnapshot(ctx, resourcev2.SourceCoverage{
				Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort,
			}, resourcev2.ContinuityBestEffort)
		},
	}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	request := transportSourceRequest()
	request.Mode = resourcev2.SourceModeSnapshotAndUpdates
	request.Target.Scope = scope
	ctx := acceptedCatalogContext(context.Background(), t, client, request.Target.Route)
	stream, err := client.OpenSourceStream(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if frame, err := stream.Recv(); err != nil || frame.Begin == nil {
		t.Fatalf("source-only begin: %+v %v", frame, err)
	}
	if frame, err := stream.Recv(); err != nil || frame.SnapshotEnd == nil || frame.SnapshotEnd.Continuity != resourcev2.ContinuityBestEffort {
		t.Fatalf("source-only snapshot: %+v %v", frame, err)
	}
	if _, err := client.Enumerate(ctx, resourcev2.EnumerateRequest{Target: request.Target}); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("source-only support granted finite enumeration: %v", err)
	}
}
