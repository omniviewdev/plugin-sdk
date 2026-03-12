package settings

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestProvider(t *testing.T, categories ...Category) *provider {
	t.Helper()
	p := &provider{
		logger:         zap.NewNop().Sugar(),
		changeHandlers: make(map[string]CategoryChangeFunc),
	}
	require.NoError(t, p.Initialize(context.Background(), categories...))
	return p
}

func testCategory() Category {
	return Category{
		ID:    "test",
		Label: "Test",
		Settings: map[string]Setting{
			"enabled": {ID: "enabled", Type: Toggle, Default: false},
			"name":    {ID: "name", Type: Text, Default: "default"},
		},
	}
}

func TestRegisterChangeHandler_CalledOnSetSettings(t *testing.T) {
	p := newTestProvider(t, testCategory())

	var called bool
	var receivedVals map[string]any
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		called = true
		receivedVals = vals
	})

	err := p.SetSettings(map[string]any{"test.enabled": true})
	require.NoError(t, err)

	assert.True(t, called)
	assert.Equal(t, true, receivedVals["enabled"])
}

func TestRegisterChangeHandler_CalledOnSetSetting(t *testing.T) {
	p := newTestProvider(t, testCategory())

	var called bool
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		called = true
	})

	err := p.SetSetting("test.name", "updated")
	require.NoError(t, err)

	assert.True(t, called)
}

func TestRegisterChangeHandler_CalledOnResetSetting(t *testing.T) {
	p := newTestProvider(t, testCategory())

	// Set a non-default value first.
	require.NoError(t, p.SetSetting("test.enabled", true))

	var called bool
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		called = true
	})

	err := p.ResetSetting("test.enabled")
	require.NoError(t, err)

	assert.True(t, called)
}

func TestRegisterChangeHandler_NotCalledForOtherCategory(t *testing.T) {
	other := Category{
		ID:    "other",
		Label: "Other",
		Settings: map[string]Setting{
			"flag": {ID: "flag", Type: Toggle, Default: false},
		},
	}
	p := newTestProvider(t, testCategory(), other)

	var called bool
	p.RegisterChangeHandler("other", func(vals map[string]any) {
		called = true
	})

	// Change "test" category — "other" handler should NOT fire.
	err := p.SetSetting("test.enabled", true)
	require.NoError(t, err)

	assert.False(t, called)
}

func TestRegisterChangeHandler_NoHandlerNoPanic(t *testing.T) {
	p := newTestProvider(t, testCategory())

	// No handler registered — should not panic.
	assert.NotPanics(t, func() {
		_ = p.SetSetting("test.enabled", true)
	})
}

func TestRegisterChangeHandler_ReceivesAllCategoryValues(t *testing.T) {
	p := newTestProvider(t, testCategory())

	var receivedVals map[string]any
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		receivedVals = vals
	})

	// Change only one setting — handler should still receive ALL category values.
	err := p.SetSetting("test.enabled", true)
	require.NoError(t, err)

	assert.Equal(t, true, receivedVals["enabled"])
	// "name" key must be present (handler receives all category values, not just changed ones).
	assert.Contains(t, receivedVals, "name")
}

func TestRegisterChangeHandler_SetSettingsBulkCallsOncePerCategory(t *testing.T) {
	p := newTestProvider(t, testCategory())

	callCount := 0
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		callCount++
	})

	// Bulk set two settings in the same category — handler fires once.
	err := p.SetSettings(map[string]any{
		"test.enabled": true,
		"test.name":    "new",
	})
	require.NoError(t, err)

	assert.Equal(t, 1, callCount)
}
