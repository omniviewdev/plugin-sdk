package settings

import (
	"errors"
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
	p.mergeSettings(categories...)
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

// ---------------------------------------------------------------------------
// In-memory operations
// ---------------------------------------------------------------------------

func TestNewProvider_DefaultValues(t *testing.T) {
	cats := []Category{
		{
			ID:    "appearance",
			Label: "Appearance",
			Settings: map[string]Setting{
				"theme": {ID: "theme", Type: Text, Default: "dark"},
				"zoom":  {ID: "zoom", Type: Integer, Default: 100},
			},
		},
	}
	p := NewProvider(ProviderOpts{
		Logger:         zap.NewNop().Sugar(),
		PluginSettings: cats,
	})

	// Values should be populated with defaults.
	val, err := p.GetSettingValue("appearance.theme")
	require.NoError(t, err)
	assert.Equal(t, "dark", val)

	val, err = p.GetSettingValue("appearance.zoom")
	require.NoError(t, err)
	assert.Equal(t, 100, val)
}

func TestNewProvider_EmptySettings(t *testing.T) {
	p := NewProvider(ProviderOpts{
		Logger: zap.NewNop().Sugar(),
	})

	store := p.ListSettings()
	assert.Empty(t, store)
}

func TestNewProvider_NilLogger(t *testing.T) {
	// NewProvider must not panic when Logger is nil.
	assert.NotPanics(t, func() {
		p := NewProvider(ProviderOpts{
			PluginSettings: []Category{
				{
					ID:    "cat",
					Label: "Cat",
					Settings: map[string]Setting{
						"x": {ID: "x", Type: Text, Default: "v"},
					},
				},
			},
		})
		// Should still be usable.
		val, err := p.GetSettingValue("cat.x")
		require.NoError(t, err)
		assert.Equal(t, "v", val)
	})
}

func TestSetSetting_Validates(t *testing.T) {
	p := newTestProvider(t, Category{
		ID:    "net",
		Label: "Network",
		Settings: map[string]Setting{
			"port": {
				ID:      "port",
				Type:    Integer,
				Default: 8080,
				Validator: func(v interface{}) error {
					n, ok := v.(int)
					if !ok {
						return errors.New("must be int")
					}
					if n < 1 || n > 65535 {
						return errors.New("port out of range")
					}
					return nil
				},
			},
		},
	})

	// Valid value should succeed.
	require.NoError(t, p.SetSetting("net.port", 443))

	// Invalid value should fail.
	err := p.SetSetting("net.port", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port out of range")

	// Value should remain at the last valid set.
	val, _ := p.GetSettingValue("net.port")
	assert.Equal(t, 443, val)
}

func TestSetSetting_TypeCoercion(t *testing.T) {
	p := newTestProvider(t, Category{
		ID:    "misc",
		Label: "Misc",
		Settings: map[string]Setting{
			"rate": {ID: "rate", Type: Float, Default: 1.5},
			"count": {ID: "count", Type: Integer, Default: 10},
		},
	})

	// int -> float64 coercion
	require.NoError(t, p.SetSetting("misc.rate", 3))
	val, err := p.GetSettingValue("misc.rate")
	require.NoError(t, err)
	assert.Equal(t, float64(3), val)

	// float64 -> int coercion
	require.NoError(t, p.SetSetting("misc.count", float64(42)))
	val, err = p.GetSettingValue("misc.count")
	require.NoError(t, err)
	assert.Equal(t, 42, val)
}

func TestSetSettings_Bulk(t *testing.T) {
	p := newTestProvider(t, testCategory())

	err := p.SetSettings(map[string]any{
		"test.enabled": true,
		"test.name":    "bulk",
	})
	require.NoError(t, err)

	v1, _ := p.GetSettingValue("test.enabled")
	v2, _ := p.GetSettingValue("test.name")
	assert.Equal(t, true, v1)
	assert.Equal(t, "bulk", v2)
}

func TestSetSettings_PartialFailure(t *testing.T) {
	p := newTestProvider(t, Category{
		ID:    "cfg",
		Label: "Config",
		Settings: map[string]Setting{
			"ok": {ID: "ok", Type: Text, Default: "orig"},
			"guarded": {
				ID: "guarded", Type: Integer, Default: 5,
				Validator: func(v interface{}) error {
					if n, ok := v.(int); ok && n < 0 {
						return errors.New("negative")
					}
					return nil
				},
			},
		},
	})

	err := p.SetSettings(map[string]any{
		"cfg.ok":      "changed",
		"cfg.guarded": -1,
	})
	require.Error(t, err)

	// Neither value should have been applied.
	v, _ := p.GetSettingValue("cfg.ok")
	assert.Equal(t, "orig", v)
	v2, _ := p.GetSettingValue("cfg.guarded")
	assert.Equal(t, 5, v2)
}

func TestGetSetting_NotFound(t *testing.T) {
	p := newTestProvider(t, testCategory())

	_, err := p.GetSetting("test.nonexistent")
	require.ErrorIs(t, err, ErrSettingNotFound)
}

func TestGetSetting_DotNotation(t *testing.T) {
	// Core provider (no pluginID): uses "category.id" dot notation.
	p := newTestProvider(t, testCategory())
	s, err := p.GetSetting("test.enabled")
	require.NoError(t, err)
	assert.Equal(t, "enabled", s.ID)

	// Invalid format should error.
	_, err = p.GetSetting("noDot")
	require.ErrorIs(t, err, ErrInvalidSettingID)

	// Plugin provider (pluginID set): bare ID, category is always "plugin".
	pp := &provider{
		logger:         zap.NewNop().Sugar(),
		pluginID:       "my-plugin",
		changeHandlers: make(map[string]CategoryChangeFunc),
	}
	pp.mergeSettings(Category{
		ID:    "plugin",
		Label: "Plugin",
		Settings: map[string]Setting{
			"verbose": {ID: "verbose", Type: Toggle, Default: false},
		},
	})

	s, err = pp.GetSetting("verbose")
	require.NoError(t, err)
	assert.Equal(t, "verbose", s.ID)
}

func TestGetSettingValue_DefaultFallback(t *testing.T) {
	// Setting.GetValue returns Default when Value is nil.
	s := Setting{ID: "x", Type: Text, Default: "fallback", Value: nil}
	assert.Equal(t, "fallback", s.GetValue())
}

// ---------------------------------------------------------------------------
// Typed getter mismatches
// ---------------------------------------------------------------------------

func TestGetString_TypeMismatch(t *testing.T) {
	p := newTestProvider(t, Category{
		ID:    "t",
		Label: "T",
		Settings: map[string]Setting{
			"num": {ID: "num", Type: Integer, Default: 42},
		},
	})

	_, err := p.GetString("t.num")
	require.ErrorIs(t, err, ErrSettingTypeMismatch)
}

func TestGetBool_TypeMismatch(t *testing.T) {
	p := newTestProvider(t, Category{
		ID:    "t",
		Label: "T",
		Settings: map[string]Setting{
			"name": {ID: "name", Type: Text, Default: "hello"},
		},
	})

	_, err := p.GetBool("t.name")
	require.ErrorIs(t, err, ErrSettingTypeMismatch)
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func TestRegisterSetting_CreatesCategory(t *testing.T) {
	p := newTestProvider(t) // no categories

	err := p.RegisterSetting("brand-new", Setting{ID: "flag", Type: Toggle, Default: true})
	require.NoError(t, err)

	store := p.ListSettings()
	cat, ok := store["brand-new"]
	require.True(t, ok)
	assert.Contains(t, cat.Settings, "flag")
}

func TestRegisterSettings_Bulk(t *testing.T) {
	p := newTestProvider(t)

	err := p.RegisterSettings("bulk",
		Setting{ID: "a", Type: Text, Default: "A"},
		Setting{ID: "b", Type: Integer, Default: 1},
	)
	require.NoError(t, err)

	store := p.ListSettings()
	require.Contains(t, store, "bulk")
	assert.Len(t, store["bulk"].Settings, 2)
}

func TestRegisterSetting_DuplicateID(t *testing.T) {
	p := newTestProvider(t, Category{
		ID:    "dup",
		Label: "Dup",
		Settings: map[string]Setting{
			"item": {ID: "item", Type: Text, Default: "orig"},
		},
	})

	// Set a value so we can verify preservation behavior.
	require.NoError(t, p.SetSetting("dup.item", "user-set"))

	// Re-register with updated schema (label changed).
	err := p.RegisterSetting("dup", Setting{
		ID: "item", Type: Text, Default: "new-default", Label: "Updated Label",
	})
	require.NoError(t, err)

	// RegisterSetting updates the schema but preserves the existing user value.
	s, err := p.GetSetting("dup.item")
	require.NoError(t, err)
	assert.Equal(t, "Updated Label", s.Label)
	assert.Equal(t, "user-set", s.Value, "existing user value must be preserved on re-register")
}

// ---------------------------------------------------------------------------
// Change handlers (additional)
// ---------------------------------------------------------------------------

func TestChangeHandler_FiresOnSet(t *testing.T) {
	p := newTestProvider(t, testCategory())

	fired := false
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		fired = true
	})

	require.NoError(t, p.SetSetting("test.name", "x"))
	assert.True(t, fired)
}

func TestChangeHandler_FiresOnBulkSet(t *testing.T) {
	p := newTestProvider(t, testCategory())

	fired := false
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		fired = true
	})

	require.NoError(t, p.SetSettings(map[string]any{"test.name": "y"}))
	assert.True(t, fired)
}

func TestChangeHandler_ReceivesAllCategoryValues_Comprehensive(t *testing.T) {
	p := newTestProvider(t, testCategory())

	var got map[string]any
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		got = vals
	})

	// Only change "enabled", but handler should get both "enabled" and "name".
	require.NoError(t, p.SetSetting("test.enabled", true))

	require.NotNil(t, got)
	assert.Equal(t, true, got["enabled"])
	assert.Equal(t, "default", got["name"]) // unchanged, but present
}

func TestChangeHandler_PanicRecovery(t *testing.T) {
	p := newTestProvider(t, testCategory())

	p.RegisterChangeHandler("test", func(vals map[string]any) {
		panic("boom")
	})

	// Should not crash — panic is recovered internally.
	assert.NotPanics(t, func() {
		_ = p.SetSetting("test.enabled", true)
	})
}

func TestChangeHandler_MultipleCategories(t *testing.T) {
	catA := Category{
		ID: "a", Label: "A",
		Settings: map[string]Setting{
			"x": {ID: "x", Type: Text, Default: "ax"},
		},
	}
	catB := Category{
		ID: "b", Label: "B",
		Settings: map[string]Setting{
			"y": {ID: "y", Type: Text, Default: "by"},
		},
	}
	p := newTestProvider(t, catA, catB)

	firedA, firedB := false, false
	p.RegisterChangeHandler("a", func(vals map[string]any) { firedA = true })
	p.RegisterChangeHandler("b", func(vals map[string]any) { firedB = true })

	err := p.SetSettings(map[string]any{
		"a.x": "new-ax",
		"b.y": "new-by",
	})
	require.NoError(t, err)

	assert.True(t, firedA, "handler for category 'a' should have fired")
	assert.True(t, firedB, "handler for category 'b' should have fired")
}

func TestChangeHandler_FiresOnNoOpSet(t *testing.T) {
	p := newTestProvider(t, testCategory())

	callCount := 0
	p.RegisterChangeHandler("test", func(vals map[string]any) {
		callCount++
	})

	// Set same value as default — handler still fires because the provider
	// does not perform equality checks. This test documents actual behavior.
	require.NoError(t, p.SetSetting("test.enabled", false))
	// The handler IS called (the provider does not short-circuit on no-op sets).
	assert.Equal(t, 1, callCount)
}

// ---------------------------------------------------------------------------
// Merge logic
// ---------------------------------------------------------------------------

func TestMergeSettings_NewCategoryAdded(t *testing.T) {
	p := newTestProvider(t) // empty store

	p.mergeSettings(Category{
		ID: "fresh", Label: "Fresh",
		Settings: map[string]Setting{
			"flag": {ID: "flag", Type: Toggle, Default: true},
		},
	})

	store := p.ListSettings()
	require.Contains(t, store, "fresh")
	s := store["fresh"].Settings["flag"]
	assert.Equal(t, true, s.Value, "value should be set to default on new category")
}

func TestMergeSettings_ExistingCategoryUpdated(t *testing.T) {
	p := newTestProvider(t, Category{
		ID: "cat", Label: "Old Label", Description: "old desc", Icon: "old-icon",
		Settings: map[string]Setting{
			"s": {ID: "s", Type: Text, Default: "v"},
		},
	})

	p.mergeSettings(Category{
		ID: "cat", Label: "New Label", Description: "new desc", Icon: "new-icon",
		Settings: map[string]Setting{
			"s": {ID: "s", Type: Text, Default: "v"},
		},
	})

	cat := p.ListSettings()["cat"]
	assert.Equal(t, "New Label", cat.Label)
	assert.Equal(t, "new desc", cat.Description)
	assert.Equal(t, "new-icon", cat.Icon)
}

func TestMergeSettings_ExistingValuePreserved(t *testing.T) {
	p := newTestProvider(t, Category{
		ID: "pref", Label: "Pref",
		Settings: map[string]Setting{
			"color": {ID: "color", Type: Text, Default: "blue"},
		},
	})

	// User changes value.
	require.NoError(t, p.SetSetting("pref.color", "red"))

	// Merge new schema with a different default.
	p.mergeSettings(Category{
		ID: "pref", Label: "Pref",
		Settings: map[string]Setting{
			"color": {ID: "color", Type: Text, Default: "green"},
		},
	})

	val, err := p.GetSettingValue("pref.color")
	require.NoError(t, err)
	assert.Equal(t, "red", val, "user value must survive merge")
}

func TestMergeSettings_NewSettingGetsDefault(t *testing.T) {
	p := newTestProvider(t, Category{
		ID: "editor", Label: "Editor",
		Settings: map[string]Setting{
			"font": {ID: "font", Type: Text, Default: "mono"},
		},
	})

	// Merge adds a new setting "tabSize" to existing category.
	p.mergeSettings(Category{
		ID: "editor", Label: "Editor",
		Settings: map[string]Setting{
			"font":    {ID: "font", Type: Text, Default: "mono"},
			"tabSize": {ID: "tabSize", Type: Integer, Default: 4},
		},
	})

	s := p.ListSettings()["editor"].Settings["tabSize"]
	assert.Equal(t, 4, s.Value, "new setting should get its default value")
}

func TestMergeSettings_RetainsRemovedSetting(t *testing.T) {
	p := newTestProvider(t, Category{
		ID: "old", Label: "Old",
		Settings: map[string]Setting{
			"keep":   {ID: "keep", Type: Text, Default: "k"},
			"remove": {ID: "remove", Type: Text, Default: "r"},
		},
	})

	// The current mergeSettings does NOT remove settings that are absent
	// from the new schema — it only adds/updates. This test documents actual
	// behavior: removed settings are retained.
	p.mergeSettings(Category{
		ID: "old", Label: "Old",
		Settings: map[string]Setting{
			"keep": {ID: "keep", Type: Text, Default: "k"},
			// "remove" is absent
		},
	})

	store := p.ListSettings()
	// Documenting actual behavior: the removed setting is still present.
	assert.Contains(t, store["old"].Settings, "remove",
		"current merge does not drop settings absent from new schema")
}

func TestMergeSettings_TypeMismatch(t *testing.T) {
	p := newTestProvider(t, Category{
		ID: "typed", Label: "Typed",
		Settings: map[string]Setting{
			"val": {ID: "val", Type: Text, Default: "hello"},
		},
	})

	// Merge with a different value type (int vs string). The merge should
	// detect the mismatch and keep the old value, logging an error.
	p.mergeSettings(Category{
		ID: "typed", Label: "Typed",
		Settings: map[string]Setting{
			"val": {ID: "val", Type: Text, Default: 999},
		},
	})

	s := p.ListSettings()["typed"].Settings["val"]
	assert.Equal(t, "hello", s.Value, "old value should be preserved on type mismatch")
}
