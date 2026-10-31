package settings

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
	"github.com/omniviewdev/plugin-sdk/settings"
)

// SettingsPluginServer exposes a provider through the settings RPC service.
// Cancellation releases the persistence callback waiting for acknowledgement.
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

// CommitSettings keeps validation, persistence and publication in one provider transaction.
func (s *SettingsPluginServer) CommitSettings(stream settingspb.SettingsPlugin_CommitSettingsServer) error {
	request, err := stream.Recv()
	if err != nil {
		return err
	}
	if request.GetProposal() == nil {
		return status.Error(codes.InvalidArgument, "settings proposal required")
	}
	values, err := decodeValues(request.GetProposal())
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid settings proposal")
	}
	result, err := s.Impl.CommitSettings(stream.Context(), values, func(normalized map[string]any) error {
		if contextErr := stream.Context().Err(); contextErr != nil {
			return contextErr
		}
		prepared, encodeErr := encodeValues(normalized)
		if encodeErr != nil {
			return encodeErr
		}
		response := &settingspb.CommitSettingsResponse{
			Phase: &settingspb.CommitSettingsResponse_Prepared{Prepared: prepared},
		}
		if sendErr := stream.Send(response); sendErr != nil {
			return sendErr
		}
		ack, receiveErr := stream.Recv()
		if receiveErr != nil {
			return receiveErr
		}
		if _, ok := ack.GetPhase().(*settingspb.CommitSettingsRequest_Persisted); !ok || !ack.GetPersisted() {
			return status.Error(codes.Aborted, "settings persistence aborted")
		}
		return nil
	})
	if err != nil {
		var validationErr *settings.ValidationError
		if errors.As(err, &validationErr) {
			return status.Errorf(codes.InvalidArgument, "settings commit rejected: %s", err)
		}
		return status.Errorf(codes.FailedPrecondition, "settings commit rejected: %s", err)
	}
	return stream.Send(&settingspb.CommitSettingsResponse{
		Phase: &settingspb.CommitSettingsResponse_Result{
			Result: &settingspb.CommitSettingsResult{
				Committed: result.Committed,
				Applied:   result.Applied,
				Uncertain: result.Uncertain,
			},
		},
	})
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
