// Package settings adapts plugin preferences and validation to the v1 gRPC transport.
package settings

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
	"github.com/omniviewdev/plugin-sdk/settings"
)

// Client forwards settings operations over an existing gRPC connection.
// Callers own the connection; commits honor caller cancellation and a bounded deadline.
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

// CommitSettings persists normalized remote values before acknowledging publication.
// A lost response after persistence is durable success with uncertain application.
func (c *Client) CommitSettings(
	ctx context.Context,
	values map[string]any,
	persist settings.PersistFunc,
) (settings.CommitResult, error) {
	const commitTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(ctx, commitTimeout)
	defer cancel()
	encoded, err := encodeValues(values)
	if err != nil {
		return settings.CommitResult{}, err
	}
	stream, err := c.client.CommitSettings(ctx)
	if err != nil {
		return settings.CommitResult{}, err
	}
	proposal := &settingspb.CommitSettingsRequest{
		Phase: &settingspb.CommitSettingsRequest_Proposal{Proposal: encoded},
	}
	if err = stream.Send(proposal); err != nil {
		return settings.CommitResult{}, err
	}
	response, err := stream.Recv()
	if err != nil {
		return settings.CommitResult{}, err
	}
	if response.GetPrepared() == nil {
		return settings.CommitResult{}, errors.New("settings commit did not prepare values")
	}
	normalized, err := decodeValues(response.GetPrepared())
	if err != nil {
		return settings.CommitResult{}, err
	}
	if persist != nil {
		if err = persist(normalized); err != nil {
			_ = stream.Send(&settingspb.CommitSettingsRequest{
				Phase: &settingspb.CommitSettingsRequest_Persisted{Persisted: false},
			})
			return settings.CommitResult{}, err
		}
	}
	uncertain := settings.CommitResult{Committed: true, Uncertain: true}
	ack := &settingspb.CommitSettingsRequest{
		Phase: &settingspb.CommitSettingsRequest_Persisted{Persisted: true},
	}
	if err = stream.Send(ack); err != nil {
		return uncertain, nil
	}
	response, err = stream.Recv()
	if err != nil || response.GetResult() == nil {
		return uncertain, nil
	}
	result := response.GetResult()
	if !result.GetCommitted() {
		return uncertain, nil
	}
	return settings.CommitResult{
		Committed: true,
		Applied:   result.GetApplied(),
		Uncertain: result.GetUncertain(),
	}, nil
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
