package sdk

import (
	"context"
	"reflect"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
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
