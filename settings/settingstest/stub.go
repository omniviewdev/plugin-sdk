// Package settingstest provides test doubles for the settings.Provider interface.
package settingstest

import (
	"context"
	"sync"

	"github.com/omniviewdev/plugin-sdk/settings"
)

// Compile-time interface check.
var _ settings.Provider = (*StubProvider)(nil)

// StubProvider implements settings.Provider with canned return values.
// Getters return zero values by default. Populate the map fields
// to make specific getters return data. Commits invoke persistence and report success.
//
// Usage:
//
//	sp := &settingstest.StubProvider{
//	    StringSlices: map[string][]string{"kubeconfigs": {"/path"}},
//	}
type StubProvider struct {
	Strings      map[string]string
	StringSlices map[string][]string
	Ints         map[string]int
	IntSlices    map[string][]int
	Floats       map[string]float64
	FloatSlices  map[string][]float64
	Bools        map[string]bool
	Values_      map[string]any //nolint:revive // Preserve the exported test-fixture field name for callers.

	mu       sync.Mutex
	handlers map[string]settings.CategoryChangeFunc
}

// ListSettings returns no metadata; configure typed value maps for getter tests.
func (s *StubProvider) ListSettings() settings.Store { return nil }

// GetSetting returns empty metadata without an error.
func (s *StubProvider) GetSetting(_ string) (settings.Setting, error) {
	return settings.Setting{}, nil
}

// GetSettingValue models a successful unset preference.
//
//nolint:nilnil // The stub deliberately models a successful unset preference.
func (s *StubProvider) GetSettingValue(_ string) (any, error) {
	return nil, nil
}

// GetCategoryValues returns a detached copy of the configured unscoped values.
func (s *StubProvider) GetCategoryValues(_ string) (map[string]any, error) {
	values := make(map[string]any, len(s.Values_))
	for id, value := range s.Values_ {
		values[id] = settings.CloneValue(value)
	}
	return values, nil
}

// CommitSettings invokes persistence with detached values without retaining updates.
func (s *StubProvider) CommitSettings(
	ctx context.Context, values map[string]any, persist settings.PersistFunc,
) (settings.CommitResult, error) {
	if err := ctx.Err(); err != nil {
		return settings.CommitResult{}, err
	}
	if persist != nil {
		detached := make(map[string]any, len(values))
		for id, value := range values {
			detached[id] = settings.CloneValue(value)
		}
		if err := persist(detached); err != nil {
			return settings.CommitResult{}, err
		}
	}
	return settings.CommitResult{Committed: true, Applied: true}, nil
}

// SetSetting accepts updates without retaining them.
func (s *StubProvider) SetSetting(id string, value any) error {
	return s.SetSettings(map[string]any{id: value})
}

// SetSettings accepts bulk updates without retaining them.
func (s *StubProvider) SetSettings(values map[string]any) error {
	_, err := s.CommitSettings(context.Background(), values, nil)
	return err
}

// RegisterSetting accepts metadata without retaining it.
func (s *StubProvider) RegisterSetting(_ string, _ settings.Setting) error { return nil }

// RegisterSettings accepts metadata without retaining it.
func (s *StubProvider) RegisterSettings(_ string, _ ...settings.Setting) error { return nil }

// RegisterChangeHandler replaces the callback for a category under a lock.
func (s *StubProvider) RegisterChangeHandler(categoryID string, fn settings.CategoryChangeFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handlers == nil {
		s.handlers = make(map[string]settings.CategoryChangeFunc)
	}
	s.handlers[categoryID] = fn
}

// TriggerChange invokes the registered change handler for the given category
// with the provided values. Safe for concurrent use.
func (s *StubProvider) TriggerChange(categoryID string, vals map[string]any) {
	s.mu.Lock()
	fn := s.handlers[categoryID]
	s.mu.Unlock()
	if fn != nil {
		fn(vals)
	}
}

// GetString returns configured data or the empty string.
func (s *StubProvider) GetString(id string) (string, error) {
	if s.Strings != nil {
		if v, ok := s.Strings[id]; ok {
			return v, nil
		}
	}
	return "", nil
}

// GetStringSlice returns the configured slice without copying, or nil.
func (s *StubProvider) GetStringSlice(id string) ([]string, error) {
	if s.StringSlices != nil {
		if v, ok := s.StringSlices[id]; ok {
			return v, nil
		}
	}
	return nil, nil
}

// GetInt returns configured data or zero.
func (s *StubProvider) GetInt(id string) (int, error) {
	if s.Ints != nil {
		if v, ok := s.Ints[id]; ok {
			return v, nil
		}
	}
	return 0, nil
}

// GetIntSlice returns the configured slice without copying, or nil.
func (s *StubProvider) GetIntSlice(id string) ([]int, error) {
	if s.IntSlices != nil {
		if v, ok := s.IntSlices[id]; ok {
			return v, nil
		}
	}
	return nil, nil
}

// GetFloat returns configured data or zero.
func (s *StubProvider) GetFloat(id string) (float64, error) {
	if s.Floats != nil {
		if v, ok := s.Floats[id]; ok {
			return v, nil
		}
	}
	return 0, nil
}

// GetFloatSlice returns the configured slice without copying, or nil.
func (s *StubProvider) GetFloatSlice(id string) ([]float64, error) {
	if s.FloatSlices != nil {
		if v, ok := s.FloatSlices[id]; ok {
			return v, nil
		}
	}
	return nil, nil
}

// GetBool returns configured data or false.
func (s *StubProvider) GetBool(id string) (bool, error) {
	if s.Bools != nil {
		if v, ok := s.Bools[id]; ok {
			return v, nil
		}
	}
	return false, nil
}
