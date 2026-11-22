package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// catalogFunc is a provider that only answers Catalog.
type catalogFunc func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error)

func (f catalogFunc) Catalog(ctx context.Context, r resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	return f(ctx, r)
}

func answer(err error) catalogFunc {
	return func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
		return resourcev2.Catalog{}, err
	}
}

// TestSourceCatalogCheckClassifiesPeerDeadline: the periodic fence classifies a
// timeout from the provider's answer too, not only from its own context. A peer
// that reports DeadlineExceeded before the local timer has fired is still TIMEOUT.
func TestSourceCatalogCheckClassifiesPeerDeadline(t *testing.T) {
	request := transportSourceRequest()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"peer deadline status", status.Error(codes.DeadlineExceeded, "resource transport failed"), "TIMEOUT"},
		{"wrapped peer deadline status", fmt.Errorf("catalog: %w", status.Error(codes.DeadlineExceeded, "late")), "TIMEOUT"},
		{"typed timeout", resourcev2.NewError("TIMEOUT", "slow"), "TIMEOUT"},
		{"raw context deadline", context.DeadlineExceeded, "TIMEOUT"},
		{"peer unavailable", status.Error(codes.Unavailable, "resource transport failed"), "INTERNAL"},
		{"peer canceled", status.Error(codes.Canceled, "resource transport failed"), "INTERNAL"},
		{"typed not found", resourcev2.NewError("NOT_FOUND", "gone"), "NOT_FOUND"},
		{"typed unavailable", resourcev2.NewError("UNAVAILABLE", "down"), "UNAVAILABLE"},
		{"typed classification wins over wrapped deadline", &resourcev2.ResourceOperationError{Code: "GRANT_DENIED", Message: "no", Err: status.Error(codes.DeadlineExceeded, "late")}, "GRANT_DENIED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := sourceCatalogCheck(context.Background(), answer(tc.err), request, resourcev2.ResourceType{})
			if got := resourcev2.ErrorCode(err); got != tc.want {
				t.Fatalf("code %q, want %q: %v", got, tc.want, err)
			}
			if tc.want != "TIMEOUT" && !errors.Is(err, tc.err) {
				t.Fatalf("a non-timeout answer must reach the caller unchanged: %v", err)
			}
		})
	}
}

// The caller's own cancellation wins over a deadline the peer reports after it.
func TestSourceCatalogCheckCallerCancelBeatsPeerDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	provider := catalogFunc(func(ctx context.Context, _ resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
		close(entered)
		<-ctx.Done()
		return resourcev2.Catalog{}, status.Error(codes.DeadlineExceeded, "resource transport failed")
	})
	done := make(chan error, 1)
	go func() {
		done <- sourceCatalogCheck(ctx, provider, transportSourceRequest(), resourcev2.ResourceType{})
	}()
	<-entered
	cancel()
	if err := <-done; resourcev2.ErrorCode(err) != "CANCELLED" {
		t.Fatalf("caller cancel reported as %q: %v", resourcev2.ErrorCode(err), err)
	}
}

// A context whose own deadline has expired is TIMEOUT whatever the peer says.
func TestSourceCatalogCheckExpiredContextIsTimeout(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, peer := range []error{nil, status.Error(codes.Unavailable, "resource transport failed"), status.Error(codes.DeadlineExceeded, "resource transport failed")} {
		err := sourceCatalogCheck(ctx, answer(peer), transportSourceRequest(), resourcev2.ResourceType{})
		if resourcev2.ErrorCode(err) != "TIMEOUT" {
			t.Fatalf("expired context with peer answer %v reported %q: %v", peer, resourcev2.ErrorCode(err), err)
		}
	}
}

// recheckAnswerProvider serves a normal catalog until checking is set, then
// answers every Catalog call from answer.
type recheckAnswerProvider struct {
	transportSourceProvider
	checking atomic.Bool
	answer   func(context.Context) error
}

func (p *recheckAnswerProvider) Catalog(ctx context.Context, r resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	catalog, err := p.transportSourceProvider.Catalog(ctx, r)
	if err != nil || !p.checking.Load() {
		return catalog, err
	}
	return resourcev2.Catalog{}, p.answer(ctx)
}

// recheckStream returns a following-mode source stream, one change frame away
// from a catalog recheck, wired to provider over a real gRPC transport.
func recheckStream(t *testing.T, provider *recheckAnswerProvider) (*sourceClientStream, context.CancelFunc) {
	t.Helper()
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	request := transportSourceRequest()
	request.Mode = resourcev2.SourceModeSnapshotAndUpdates
	catalog, err := client.Catalog(context.Background(), resourcev2.CatalogRequest{Route: request.Target.Route})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(resourcev2.WithCatalogRevision(context.Background(), catalog.Revision))
	tracker := resourcev2.NewCatalogBoundSourcePhaseTracker(request, catalog.Types[0])
	for _, frame := range []resourcev2.SourceFrame{
		{SessionID: request.SessionID, Generation: request.Generation, Sequence: 1, Begin: &resourcev2.SourceBegin{Mode: request.Mode}},
		{SessionID: request.SessionID, Generation: request.Generation, Sequence: 2, SnapshotEnd: &resourcev2.SourceSnapshotEnd{Coverage: resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, Continuity: resourcev2.ContinuityBestEffort}},
	} {
		if err := tracker.Admit(frame); err != nil {
			t.Fatal(err)
		}
	}
	record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
	if err != nil {
		t.Fatal(err)
	}
	observation := resourcev2.SourceObservation{Record: resourcev2.CompactSourceRecord(record)}
	frame, err := resourcev2.ToProtoSourceFrame(resourcev2.SourceFrame{
		SessionID: request.SessionID, Generation: request.Generation, Sequence: 3,
		Changes: &resourcev2.SourceChanges{Changes: []resourcev2.SourceChange{{Kind: resourcev2.SourceChangeUpsert, Observation: &observation}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := &sourceClientStream{
		stream: &hostileSourceStream{frames: []*resourcepb.SourceFrame{frame}},
		cancel: cancel, ctx: ctx, maxBatchItems: request.Limits.MaxBatchItems,
		tracker: tracker, client: client, request: request, descriptor: catalog.Types[0],
	}
	t.Cleanup(func() { stream.Close(); cancel() })
	provider.checking.Store(true)
	return stream, cancel
}

// A provider that reports DeadlineExceeded at once, while the client's own
// five-second timer is far from firing, still gives the host a TIMEOUT.
func TestSourceClientRecheckClassifiesPeerAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer error
		want   string
	}{
		{"peer deadline", status.Error(codes.DeadlineExceeded, "provider timer"), "TIMEOUT"},
		{"provider context deadline", context.DeadlineExceeded, "TIMEOUT"},
		{"typed timeout", resourcev2.NewError("TIMEOUT", "slow"), "TIMEOUT"},
		{"peer unavailable", status.Error(codes.Unavailable, "down"), "INTERNAL"},
		{"peer canceled", status.Error(codes.Canceled, "gone"), "INTERNAL"},
		{"typed not found", resourcev2.NewError("NOT_FOUND", "gone"), "NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, _ := recheckStream(t, &recheckAnswerProvider{answer: func(context.Context) error { return tc.answer }})
			started := time.Now()
			_, err := stream.Recv()
			if got := resourcev2.ErrorCode(err); got != tc.want {
				t.Fatalf("code %q, want %q: %v", got, tc.want, err)
			}
			if elapsed := time.Since(started); elapsed >= sourceCatalogCheckTimeout {
				t.Fatalf("answer was immediate, yet Recv took %s: the local timer decided", elapsed)
			}
			if stream.ctx.Err() == nil {
				t.Fatal("failed client recheck left source stream open")
			}
		})
	}
}

// The caller cancels; the provider then answers DeadlineExceeded. CANCELLED stays.
func TestSourceClientRecheckCallerCancelBeatsPeerDeadline(t *testing.T) {
	entered := make(chan struct{})
	provider := &recheckAnswerProvider{answer: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return status.Error(codes.DeadlineExceeded, "provider timer")
	}}
	stream, cancel := recheckStream(t, provider)
	received := make(chan error, 1)
	go func() { _, err := stream.Recv(); received <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("client catalog recheck did not reach the provider")
	}
	cancel()
	select {
	case err := <-received:
		if got := resourcev2.ErrorCode(err); got != "CANCELLED" {
			t.Fatalf("caller cancel reported as %q: %v", got, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client catalog recheck did not unblock")
	}
}
