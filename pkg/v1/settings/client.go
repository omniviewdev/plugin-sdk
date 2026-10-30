// Package settings adapts plugin preferences and validation to the v1 gRPC transport.
package settings

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/settings"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
)

// Client forwards settings operations over an existing gRPC connection.
// Callers own the connection; methods other than validation use a background context.
type Client struct {
	client settingspb.SettingsPluginClient
}

var _ Provider = (*Client)(nil)

// ListSettings returns remote metadata, or nil when the RPC fails.
func (c *Client) ListSettings() map[string]settings.Setting {
	resp, err := c.client.ListSettings(context.Background(), &emptypb.Empty{})
	if err != nil {
		// we should never error here since the receiver never emits one
		return nil
	}

	m := make(map[string]settings.Setting)
	for k, v := range resp.GetSettings() {
		m[k] = FromProtoSetting(v)
	}

	return m
}

// GetSetting returns remote metadata or the RPC error.
func (c *Client) GetSetting(id string) (settings.Setting, error) {
	resp, err := c.client.GetSetting(context.Background(), &settingspb.GetSettingRequest{Id: id})
	if err != nil {
		return settings.Setting{}, err
	}
	return FromProtoSetting(resp), nil
}

// GetSettingValue decodes a remote value, returning RPC or conversion errors.
func (c *Client) GetSettingValue(id string) (any, error) {
	resp, err := c.client.GetSettingValue(
		context.Background(),
		&settingspb.GetSettingValueRequest{Id: id},
	)
	if err != nil {
		return nil, err
	}
	return ConvertAnyToInterface(resp.GetValue())
}

// SetSetting encodes one value before asking the remote provider to update it.
func (c *Client) SetSetting(id string, value any) error {
	val, err := ConvertInterfaceToAny(value)
	if err != nil {
		return err
	}
	_, err = c.client.SetSetting(context.Background(), &settingspb.SetSettingRequest{Id: id, Value: val})
	return err
}

// SetSettings encodes all values before submitting a single bulk update.
func (c *Client) SetSettings(settings map[string]any) error {
	req := &settingspb.SetSettingsRequest{
		Settings: make(map[string]*anypb.Any),
	}

	for k, v := range settings {
		val, err := ConvertInterfaceToAny(v)
		if err != nil {
			return err
		}
		req.Settings[k] = val
	}
	_, err := c.client.SetSettings(context.Background(), req)
	return err
}

// ValidateSetting runs remote validation without mutation and bounds the RPC lifetime.
func (c *Client) ValidateSetting(id string, value any) (settings.ValidationResult, error) {
	val, err := ConvertInterfaceToAny(value)
	if err != nil {
		return settings.ValidationResult{}, err
	}
	const validationTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), validationTimeout)
	defer cancel()
	resp, err := c.client.ValidateSetting(ctx, &settingspb.ValidateSettingRequest{Id: id, Value: val})
	if err != nil {
		return settings.ValidationResult{}, err
	}
	return settings.ValidationResult{Valid: resp.Valid, Code: resp.Code, Message: resp.Message}, nil
}
