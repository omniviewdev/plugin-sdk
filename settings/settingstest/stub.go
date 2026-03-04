// Package settingstest provides test doubles for the settings.Provider interface.
package settingstest

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/settings"
)

// StubProvider implements settings.Provider with canned return values.
// All methods return zero values by default. Populate the map fields
// to make specific getters return data.
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
	Values_      map[string]any
}

func (s *StubProvider) Initialize(_ context.Context, _ ...settings.Category) error { return nil }
func (s *StubProvider) LoadSettings() error                                        { return nil }
func (s *StubProvider) SaveSettings() error                                        { return nil }
func (s *StubProvider) ListSettings() settings.Store                               { return nil }
func (s *StubProvider) Values() map[string]any                                     { return s.Values_ }
func (s *StubProvider) GetSetting(_ string) (settings.Setting, error) {
	return settings.Setting{}, nil
}
func (s *StubProvider) GetSettingValue(_ string) (any, error)                      { return nil, nil }
func (s *StubProvider) SetSetting(_ string, _ any) error                           { return nil }
func (s *StubProvider) SetSettings(_ map[string]any) error                         { return nil }
func (s *StubProvider) ResetSetting(_ string) error                                { return nil }
func (s *StubProvider) RegisterSetting(_ string, _ settings.Setting) error         { return nil }
func (s *StubProvider) RegisterSettings(_ string, _ ...settings.Setting) error     { return nil }
func (s *StubProvider) GetCategories() []settings.Category                         { return nil }
func (s *StubProvider) GetCategory(_ string) (settings.Category, error) {
	return settings.Category{}, nil
}
func (s *StubProvider) GetCategoryValues(_ string) (map[string]interface{}, error) { return nil, nil }

func (s *StubProvider) GetString(id string) (string, error) {
	if s.Strings != nil {
		if v, ok := s.Strings[id]; ok {
			return v, nil
		}
	}
	return "", nil
}

func (s *StubProvider) GetStringSlice(id string) ([]string, error) {
	if s.StringSlices != nil {
		if v, ok := s.StringSlices[id]; ok {
			return v, nil
		}
	}
	return nil, nil
}

func (s *StubProvider) GetInt(id string) (int, error) {
	if s.Ints != nil {
		if v, ok := s.Ints[id]; ok {
			return v, nil
		}
	}
	return 0, nil
}

func (s *StubProvider) GetIntSlice(id string) ([]int, error) {
	if s.IntSlices != nil {
		if v, ok := s.IntSlices[id]; ok {
			return v, nil
		}
	}
	return nil, nil
}

func (s *StubProvider) GetFloat(id string) (float64, error) {
	if s.Floats != nil {
		if v, ok := s.Floats[id]; ok {
			return v, nil
		}
	}
	return 0, nil
}

func (s *StubProvider) GetFloatSlice(id string) ([]float64, error) {
	if s.FloatSlices != nil {
		if v, ok := s.FloatSlices[id]; ok {
			return v, nil
		}
	}
	return nil, nil
}

func (s *StubProvider) GetBool(id string) (bool, error) {
	if s.Bools != nil {
		if v, ok := s.Bools[id]; ok {
			return v, nil
		}
	}
	return false, nil
}
