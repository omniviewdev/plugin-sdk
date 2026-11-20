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

const defaultConnectionAcquisitionTimeoutSeconds = 10
const maxConnectionAcquisitionTimeoutSeconds = 300

// ConnectionAcquisitionTimeoutSetting exposes the common acquisition policy in a
// plugin's settings. Changes apply to the next acquisition; they never change an
// operation already in flight. The controller reads it for each connection start;
// providers register it to expose the bound, and resolve it themselves only for
// acquisition work of their own (such as catalog refresh).
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
// default. Other provider read failures and invalid values remain errors. Callers apply
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
