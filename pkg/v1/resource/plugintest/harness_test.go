package plugintest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
)

// stubCfg returns a minimal ResourcePluginConfig using test doubles.
func stubCfg() resource.ResourcePluginConfig[string] {
	stub := &resourcetest.StubResourcer[string]{
		GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.GetInput) (*resource.GetResult, error) {
			data, _ := json.Marshal(map[string]string{"id": input.ID})
			return &resource.GetResult{Success: true, Result: data}, nil
		},
		ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
			return &resource.ListResult{Success: true, Result: []json.RawMessage{json.RawMessage(`{"id":"a"}`)}}, nil
		},
		CreateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.CreateInput) (*resource.CreateResult, error) {
			return &resource.CreateResult{Success: true, Result: input.Input}, nil
		},
		UpdateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.UpdateInput) (*resource.UpdateResult, error) {
			return &resource.UpdateResult{Success: true, Result: input.Input}, nil
		},
		DeleteFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.DeleteInput) (*resource.DeleteResult, error) {
			return &resource.DeleteResult{Success: true, Result: json.RawMessage(`{}`)}, nil
		},
	}

	return resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{
				Meta:      resource.ResourceMeta{Group: "test", Version: "v1", Kind: "Widget"},
				Resourcer: stub,
			},
		},
	}
}

func TestHarness_Mount_StartsController(t *testing.T) {
	h := Mount(t, stubCfg())
	require.NotNil(t, h)
	assert.NotNil(t, h.Provider())
	assert.NotNil(t, h.Sink())
}

func TestHarness_CRUD_DelegatesToController(t *testing.T) {
	h := Mount(t, stubCfg())

	// We need to start a connection first so CRUD can resolve a client.
	conns := h.LoadConnections()
	require.NotEmpty(t, conns)
	h.StartConnection(conns[0].ID)

	key := "test::v1::Widget"

	// Get
	getResult := h.Get(key, resource.GetInput{ID: "foo"})
	assert.True(t, getResult.Success)

	// List
	listResult := h.List(key, resource.ListInput{})
	assert.True(t, listResult.Success)
	assert.Len(t, listResult.Result, 1)

	// Create
	createResult := h.Create(key, resource.CreateInput{Input: json.RawMessage(`{"id":"new"}`)})
	assert.True(t, createResult.Success)

	// Update
	updateResult := h.Update(key, resource.UpdateInput{ID: "foo", Input: json.RawMessage(`{"id":"upd"}`)})
	assert.True(t, updateResult.Success)

	// Delete
	deleteResult := h.Delete(key, resource.DeleteInput{ID: "foo"})
	assert.True(t, deleteResult.Success)
}

func TestHarness_WatchAssertions_Work(t *testing.T) {
	h := Mount(t, stubCfg())

	// Simulate events directly on the sink.
	h.Sink().OnAdd(resource.WatchAddPayload{Key: "test::v1::Widget", ID: "a"})
	h.Sink().OnAdd(resource.WatchAddPayload{Key: "test::v1::Widget", ID: "b"})
	h.Sink().OnUpdate(resource.WatchUpdatePayload{Key: "test::v1::Widget", ID: "a"})
	h.Sink().OnDelete(resource.WatchDeletePayload{Key: "test::v1::Widget", ID: "b"})

	h.WaitForAdds(2, time.Second)
	h.WaitForUpdates(1, time.Second)
	h.WaitForDeletes(1, time.Second)

	assert.Equal(t, 2, h.Sink().AddCount())
	assert.Equal(t, 1, h.Sink().UpdateCount())
	assert.Equal(t, 1, h.Sink().DeleteCount())
}

func TestHarness_Close_CleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := Mount(t, stubCfg(), WithContext[string](ctx))

	// Close should not panic.
	h.Close()

	// After close, the context should be done.
	select {
	case <-h.Context().Done():
		// expected
	default:
		t.Fatal("expected harness context to be done after Close()")
	}
}

func TestHarness_WithTimeout_Option(t *testing.T) {
	cfg := &harnessConfig[string]{}
	WithTimeout[string](5 * time.Second)(cfg)
	assert.Equal(t, 5*time.Second, cfg.timeout)
}
