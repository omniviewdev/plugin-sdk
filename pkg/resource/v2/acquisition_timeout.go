package resourcev2

import (
	"context"
	"errors"
	"time"

	"github.com/omniviewdev/plugin-sdk/settings"
)

// ConnectionAcquisitionTimeoutSettingID is a provider setting. It bounds
// initial connection acquisition and explicit catalog refresh, not live clients,
// watches, or unrelated resource operations. The controller applies it to every
// connection start (CreateClient and OnStart together); a provider that does not
// register the setting gets the default. Registering the setting is how a
// provider makes the bound user-adjustable.
const ConnectionAcquisitionTimeoutSettingID = "connection_acquisition_timeout_seconds"

// defaultConnectionAcquisitionTimeoutSeconds bounds a connection start when its
// user has not chosen a value. The bound exists to stop a start hanging forever,
// not to hurry it. Interactive credential helpers (exec plugins such as
// `aws eks get-token` behind SSO, OIDC browser logins, MFA prompts) routinely take
// tens of seconds, and client-go's exec authenticator ignores its context, so a
// short default would fail legitimate logins and leave the helper running.
const defaultConnectionAcquisitionTimeoutSeconds = 60
const maxConnectionAcquisitionTimeoutSeconds = 300

// ConnectionAcquisitionTimeoutSetting exposes the common acquisition policy in a
// plugin's settings. Changes apply to the next acquisition; they never change an
// operation already in flight. The controller reads it for each connection start.
// Registering it lets users choose the bound, from 1 to 300 seconds; until they do,
// the value is the setting's Default, which is 60 seconds. A plugin whose starts
// legitimately take longer sets Default on the returned setting before it
// registers it. A provider resolves it with ConnectionAcquisitionTimeout only for
// acquisition work of its own, such as catalog refresh.
func ConnectionAcquisitionTimeoutSetting() settings.Setting {
	return settings.Setting{
		ID:          ConnectionAcquisitionTimeoutSettingID,
		Label:       "Connection acquisition timeout (seconds)",
		Description: "Maximum time for initial connection acquisition or catalog refresh. Changes apply to the next acquisition, not existing operations or live connections.",
		Type:        settings.Integer,
		Default:     defaultConnectionAcquisitionTimeoutSeconds,
		Validator: func(value any) settings.ValidationResult {
			seconds, ok := value.(int)
			if !ok || seconds < 1 || seconds > maxConnectionAcquisitionTimeoutSeconds {
				return settings.ValidationResult{Code: "acquisition_timeout_range", Message: "Choose a whole number of seconds from 1 to 300."}
			}
			return settings.ValidationResult{Valid: true}
		},
	}
}

// ConnectionAcquisitionTimeout resolves an acquisition's policy from its plugin
// session. Missing session/configuration, a provider that has not registered the
// setting (settings.ErrSettingNotFound), or an unset value uses the documented
// default. Other provider read failures and invalid values remain errors, which
// suits a provider that asks for the value for work of its own; the controller's
// connection start is more forgiving (see startAcquisitionTimeout). Callers apply
// this duration to acquisition work only, never to a retained client lifetime.
// context.WithTimeout preserves any earlier caller deadline or cancellation.
func ConnectionAcquisitionTimeout(ctx context.Context) (time.Duration, error) {
	seconds := defaultConnectionAcquisitionTimeoutSeconds
	if session, ok := SessionFromContext(ctx); ok && session.PluginConfig != nil {
		value, err := session.PluginConfig.GetSettingValue(ConnectionAcquisitionTimeoutSettingID)
		if errors.Is(err, settings.ErrSettingNotFound) {
			value, err = nil, nil
		}
		if err != nil {
			return 0, &ResourceOperationError{Code: "INVALID_REQUEST", Message: "Connection acquisition timeout could not be read.", Err: err}
		}
		if value != nil {
			var valid bool
			seconds, valid = value.(int)
			if !valid || seconds < 1 || seconds > maxConnectionAcquisitionTimeoutSeconds {
				return 0, NewError("INVALID_REQUEST", "Connection acquisition timeout must be a whole number of seconds from 1 to 300.")
			}
		}
	}
	return time.Duration(seconds) * time.Second, nil
}

// acquisitionTimeoutNotApplied is logged when the controller starts a connection
// with a bound other than the stored setting.
const acquisitionTimeoutNotApplied = "connection acquisition timeout setting not applied as configured"

// startAcquisitionTimeout resolves the bound the controller applies to one
// connection start. Unlike ConnectionAcquisitionTimeout it never fails the start:
// the controller asks on behalf of every plugin, including those that never
// registered the setting, so a setting that cannot be read, or is not a whole
// number, gives the default, and a number outside 1 to 300 is clamped into that
// range. Each case is logged with its reason and the numbers, never with the
// text of the provider's error.
func startAcquisitionTimeout(ctx context.Context) time.Duration {
	fallback := time.Duration(defaultConnectionAcquisitionTimeoutSeconds) * time.Second
	session, ok := SessionFromContext(ctx)
	if !ok || session.PluginConfig == nil {
		return fallback
	}
	value, err := session.PluginConfig.GetSettingValue(ConnectionAcquisitionTimeoutSettingID)
	switch {
	case errors.Is(err, settings.ErrSettingNotFound), err == nil && value == nil:
		return fallback
	case err != nil:
		warnAcquisitionTimeoutNotApplied(ctx, "read_failed", defaultConnectionAcquisitionTimeoutSeconds)
		return fallback
	}
	seconds, isInt := value.(int)
	if !isInt {
		warnAcquisitionTimeoutNotApplied(ctx, "not_an_integer", defaultConnectionAcquisitionTimeoutSeconds)
		return fallback
	}
	applied := min(max(seconds, 1), maxConnectionAcquisitionTimeoutSeconds)
	if applied != seconds {
		warnAcquisitionTimeoutNotApplied(ctx, "out_of_range", applied, "configured_seconds", seconds)
	}
	return time.Duration(applied) * time.Second
}

func warnAcquisitionTimeoutNotApplied(ctx context.Context, reason string, applied int, extra ...any) {
	fields := []any{"reason", reason, "setting", ConnectionAcquisitionTimeoutSettingID, "applied_seconds", applied}
	lifecycleLogger().Warnw(ctx, acquisitionTimeoutNotApplied, append(fields, extra...)...)
}
