package resourcev2

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/settings"
)

// settingNotApplied is the warning the controller logs when the plugin's stored
// acquisition timeout is not what the start used.
const settingNotApplied = "connection acquisition timeout setting not applied as configured"

// startDeadline starts "explicit" under a session whose plugin configuration is
// provider and returns the deadline CreateClient saw, measured from the start.
func startDeadline(t *testing.T, provider settings.Provider) time.Duration {
	t.Helper()
	cfg := controllerConfig()
	lifecycle := controllerLifecycle(&atomic.Int32{})
	var (
		deadline    time.Time
		sawDeadline bool
	)
	lifecycle.CreateClient = func(ctx context.Context, connection types.Connection) (*string, error) {
		deadline, sawDeadline = ctx.Deadline()
		client := connection.ID
		return &client, nil
	}
	cfg.Lifecycle = lifecycle
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := WithSession(context.Background(), Session{PluginConfig: provider})
	began := time.Now()
	if _, err := c.StartConnection(ctx, "explicit"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !sawDeadline {
		t.Fatal("CreateClient context has no deadline")
	}
	return deadline.Sub(began)
}

// assertDeadline allows a second of slack: the deadline is measured from a clock
// read taken before the start.
func assertDeadline(t *testing.T, got, want time.Duration) {
	t.Helper()
	if got > want+time.Second || got < want-time.Second {
		t.Fatalf("deadline %v from start, want about %v", got, want)
	}
}

// The bound exists to stop a start hanging forever, not to hurry it. Interactive
// credential helpers (SSO, OIDC browser logins, MFA prompts) routinely take tens
// of seconds, so the default is one minute and the user-settable range is
// 1 to 300 seconds. These are literals on purpose: the policy is pinned here.
func TestAcquisitionTimeoutDefaultsToOneMinuteWithinOneToThreeHundred(t *testing.T) {
	setting := ConnectionAcquisitionTimeoutSetting()
	if setting.Default != 60 {
		t.Fatalf("setting default = %v, want 60", setting.Default)
	}
	if got, err := ConnectionAcquisitionTimeout(context.Background()); err != nil || got != time.Minute {
		t.Fatalf("default timeout = %v, %v, want 1m0s", got, err)
	}
	for seconds, want := range map[int]bool{0: false, 1: true, 60: true, 300: true, 301: false} {
		if got := setting.Validator(seconds).Valid; got != want {
			t.Errorf("validator(%d) = %v, want %v", seconds, got, want)
		}
	}
	assertDeadline(t, startDeadline(t, nil), time.Minute)
}

// A plugin that needs longer than the default for every user sets Default on the
// setting it registers; users can then move it within 1 to 300 seconds.
func TestPluginRaisesItsOwnAcquisitionDefaultThroughTheSetting(t *testing.T) {
	setting := ConnectionAcquisitionTimeoutSetting()
	setting.Default = 120
	provider := settings.NewProvider(settings.ProviderOpts{
		Logger: zap.NewNop().Sugar(), PluginID: "example",
		PluginSettings: []settings.Category{{ID: "plugin", Settings: map[string]settings.Setting{setting.ID: setting}}},
	})
	assertDeadline(t, startDeadline(t, provider), 120*time.Second)
	if err := provider.SetSetting(setting.ID, 200); err != nil {
		t.Fatal(err)
	}
	assertDeadline(t, startDeadline(t, provider), 200*time.Second)
}

// A plugin that never registered the setting gets the default without noise.
func TestUnregisteredAcquisitionSettingUsesTheDefaultQuietly(t *testing.T) {
	h := newLifecycleObsHarness(t)
	assertDeadline(t, startDeadline(t, pluginSettings(t, false, 0)), time.Minute)
	if _, logged := h.logged(settingNotApplied); logged {
		t.Fatal("a plugin that never registered the setting got a warning")
	}
}

// The controller asks for the bound on behalf of every plugin, so a setting that
// cannot be read must not fail every start: the default applies and the failure
// is logged without the provider's own text.
func TestStartConnectionUsesDefaultAndWarnsWhenTheSettingCannotBeRead(t *testing.T) {
	h := newLifecycleObsHarness(t)
	provider := invalidTimeoutProvider{err: errors.New(lifecycleObsSecret)}
	assertDeadline(t, startDeadline(t, provider), time.Minute)
	fields, logged := h.logged(settingNotApplied)
	if !logged {
		t.Fatal("an unreadable setting was not logged")
	}
	if fields["reason"] != "read_failed" || fields["applied_seconds"] != 60 {
		t.Fatalf("warning fields = %v", fields)
	}
	h.assertNoSecret(t)
}

// A stored value outside 1 to 300 is clamped, not refused, and logged.
func TestStartConnectionClampsAnOutOfRangeAcquisitionTimeoutAndWarns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   int
		applied int
	}{
		{name: "zero", value: 0, applied: 1},
		{name: "negative", value: -5, applied: 1},
		{name: "above the cap", value: 301, applied: 300},
		{name: "far above the cap", value: 100000, applied: 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newLifecycleObsHarness(t)
			got := startDeadline(t, invalidTimeoutProvider{value: tc.value})
			assertDeadline(t, got, time.Duration(tc.applied)*time.Second)
			fields, logged := h.logged(settingNotApplied)
			if !logged {
				t.Fatal("a clamped setting was not logged")
			}
			if fields["reason"] != "out_of_range" || fields["applied_seconds"] != tc.applied ||
				fields["configured_seconds"] != tc.value {
				t.Fatalf("warning fields = %v", fields)
			}
		})
	}
}

// A stored value that is not a whole number cannot be clamped: the default applies.
func TestStartConnectionUsesDefaultAndWarnsWhenTheSettingIsNotAnInteger(t *testing.T) {
	h := newLifecycleObsHarness(t)
	assertDeadline(t, startDeadline(t, invalidTimeoutProvider{value: "10"}), time.Minute)
	fields, logged := h.logged(settingNotApplied)
	if !logged {
		t.Fatal("a setting that is not an integer was not logged")
	}
	if fields["reason"] != "not_an_integer" || fields["applied_seconds"] != 60 {
		t.Fatalf("warning fields = %v", fields)
	}
}
