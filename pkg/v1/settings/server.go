package settings

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
)

// SettingsPluginServer exposes a provider through the settings RPC service.
// The provider owns synchronization; RPC cancellation does not cancel provider methods.
type SettingsPluginServer struct {
	// This is the real implementation
	Impl Provider
}

// ListSettings serializes provider metadata into a new response map.
func (s *SettingsPluginServer) ListSettings(
	_ context.Context,
	_ *emptypb.Empty,
) (*settingspb.ListSettingsResponse, error) {
	resp := s.Impl.ListSettings()

	settings := make(map[string]*settingspb.Setting)
	for k := range resp {
		settings[k] = ToProtoSetting(resp[k])
	}

	return &settingspb.ListSettingsResponse{
		Settings: settings,
	}, nil
}

// GetSetting returns metadata or an Internal status when the provider fails.
func (s *SettingsPluginServer) GetSetting(
	_ context.Context,
	in *settingspb.GetSettingRequest,
) (*settingspb.Setting, error) {
	resp, err := s.Impl.GetSetting(in.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get setting: %s", err.Error())
	}

	return ToProtoSetting(resp), nil
}

// GetSettingValue encodes the provider value or reports an Internal status.
func (s *SettingsPluginServer) GetSettingValue(
	_ context.Context,
	in *settingspb.GetSettingValueRequest,
) (*settingspb.GetSettingValueResponse, error) {
	resp, err := s.Impl.GetSettingValue(in.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get setting value: %s", err.Error())
	}

	value, err := ConvertInterfaceToAny(resp)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to convert setting value: %s",
			err.Error(),
		)
	}

	return &settingspb.GetSettingValueResponse{
		Value: value,
	}, nil
}

// SetSetting decodes and validates one update through the provider.
func (s *SettingsPluginServer) SetSetting(
	_ context.Context,
	in *settingspb.SetSettingRequest,
) (*emptypb.Empty, error) {
	value, err := ConvertAnyToInterface(in.GetValue())
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to convert setting value: %s",
			err.Error(),
		)
	}

	if err = s.Impl.SetSetting(in.GetId(), value); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to set setting: %s", err.Error())
	}
	return &emptypb.Empty{}, nil
}

// SetSettings decodes every input before calling the provider's bulk update method.
func (s *SettingsPluginServer) SetSettings(
	_ context.Context,
	in *settingspb.SetSettingsRequest,
) (*emptypb.Empty, error) {
	values := make(map[string]any)
	for k, v := range in.GetSettings() {
		value, err := ConvertAnyToInterface(v)
		if err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"failed to convert setting value: %s",
				err.Error(),
			)
		}
		values[k] = value
	}

	if err := s.Impl.SetSettings(values); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to set settings: %s", err.Error())
	}
	return &emptypb.Empty{}, nil
}

// ValidateSetting executes registered callbacks in the plugin, without mutating values.
func (s *SettingsPluginServer) ValidateSetting(
	_ context.Context,
	in *settingspb.ValidateSettingRequest,
) (*settingspb.ValidateSettingResponse, error) {
	value, err := ConvertAnyToInterface(in.GetValue())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid validation input")
	}
	result, err := s.Impl.ValidateSetting(in.GetId(), value)
	if err != nil {
		return nil, status.Error(codes.NotFound, "setting validation unavailable")
	}
	return &settingspb.ValidateSettingResponse{Valid: result.Valid, Code: result.Code, Message: result.Message}, nil
}
