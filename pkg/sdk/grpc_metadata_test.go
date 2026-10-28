package sdk

import (
	"context"
	"reflect"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestClientPluginContextPreservesAdmissionMetadata(t *testing.T) {
	original := metadata.MD{"omniview-resource-catalog-revision-bin": {"exact-accepted-catalog"}, "diagnostic-header": {"keep"}}
	ctx := metadata.NewOutgoingContext(context.Background(), original)
	pc := &types.PluginContext{RequestID: "request", RequesterID: "diagnostic", Connection: &types.Connection{ID: "selected"}}
	ctx = types.WithPluginContext(ctx, pc)
	sent, err := UseClientPluginContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := metadata.FromOutgoingContext(sent)
	if !reflect.DeepEqual(got.Get("omniview-resource-catalog-revision-bin"), []string{"exact-accepted-catalog"}) || got.Get("diagnostic-header")[0] != "keep" || got.Get(MDKeyConnectionID)[0] != "selected" {
		t.Fatalf("admission metadata replaced: %#v", got)
	}
	if len(original) != 2 {
		t.Fatal("interceptor mutated caller metadata")
	}
}

func TestStreamClientPluginContextPreservesSelectionAndAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "omniview-resource-catalog-revision-bin", "exact-observation")
	pc := &types.PluginContext{Context: ctx, RequestID: "actual-request", RequesterID: "diagnostic-only", Connection: &types.Connection{ID: "selected", Data: map[string]any{"endpoint": "configured"}}}
	ctx = types.WithPluginContext(ctx, pc)
	called := false
	_, err := StreamClientPluginContextInterceptor(nil)(ctx, &grpc.StreamDesc{}, nil, "/actual/Stream", func(sent context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
		called = true
		md, _ := metadata.FromOutgoingContext(sent)
		if !reflect.DeepEqual(md.Get(MDKeyConnectionID), []string{"selected"}) || !reflect.DeepEqual(md.Get(MDKeyRequestID), []string{"actual-request"}) || !reflect.DeepEqual(md.Get("omniview-resource-catalog-revision-bin"), []string{"exact-observation"}) {
			t.Fatalf("stream selection/admission lost: %#v", md)
		}
		if sent.Done() != ctx.Done() {
			t.Fatal("stream interceptor replaced caller cancellation")
		}
		return nil, nil
	})
	if err != nil || !called || pc.Context == nil {
		t.Fatalf("stream metadata transport failed: called=%v err=%v", called, err)
	}
}
