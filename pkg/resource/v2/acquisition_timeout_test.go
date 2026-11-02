package resourcev2

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/settings"
	"go.uber.org/zap"
)

func TestConnectionAcquisitionTimeoutSettingsBoundaries(t *testing.T) {
	setting := ConnectionAcquisitionTimeoutSetting()
	provider := settings.NewProvider(settings.ProviderOpts{
		Logger: zap.NewNop().Sugar(), PluginID: "example",
		PluginSettings: []settings.Category{{ID: "plugin", Settings: map[string]settings.Setting{setting.ID: setting}}},
	})
	ctx := WithSession(context.Background(), Session{PluginConfig: provider})
	for _, seconds := range []int{1, 45, 300} {
		// JSON settings writes arrive as floating-point numbers and normalize
		// through the same Provider transaction used by plugin settings.
		if err := provider.SetSetting(setting.ID, float64(seconds)); err != nil {
			t.Fatal(err)
		}
		got, err := ConnectionAcquisitionTimeout(ctx)
		if err != nil || got != time.Duration(seconds)*time.Second {
			t.Fatalf("timeout = %v, %v for %d seconds", got, err, seconds)
		}
	}
	for _, value := range []any{0, -1, 301, 1.5, "45"} {
		if err := provider.SetSetting(setting.ID, value); err == nil {
			t.Fatalf("invalid timeout %v was accepted", value)
		}
		got, err := ConnectionAcquisitionTimeout(ctx)
		if err != nil || got != 300*time.Second {
			t.Fatalf("rejected write changed effective timeout: %v, %v", got, err)
		}
	}
}

type invalidTimeoutProvider struct {
	settings.Provider
	value any
	err   error
}

func (p invalidTimeoutProvider) GetSettingValue(string) (any, error) { return p.value, p.err }

func TestConnectionAcquisitionTimeoutRejectsInvalidRuntimePolicy(t *testing.T) {
	for _, provider := range []invalidTimeoutProvider{
		{value: 0}, {value: 301}, {value: "10"},
		{err: errors.New("private-native-diagnostic")},
	} {
		ctx := WithSession(context.Background(), Session{PluginConfig: provider})
		_, err := ConnectionAcquisitionTimeout(ctx)
		if ErrorCode(err) != "INVALID_REQUEST" || strings.Contains(err.Error(), "private-native-diagnostic") {
			t.Fatalf("invalid policy did not produce a safe error: %v", err)
		}
	}
}
