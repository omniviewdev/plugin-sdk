package settings

import (
	"encoding/json"

	grpcproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/omniviewdev/plugin-sdk/settings"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
)

// SettingTypeToProtoType maps known types and uses text for unknown values.
func SettingTypeToProtoType(t settings.SettingType) settingspb.SettingType {
	switch t {
	case settings.Text:
		return settingspb.SettingType_SETTING_TYPE_TEXT
	case settings.Select:
		return settingspb.SettingType_SETTING_TYPE_SELECT
	case settings.MultiSelect:
		return settingspb.SettingType_SETTING_TYPE_MULTI_SELECT
	case settings.Integer:
		return settingspb.SettingType_SETTING_TYPE_INTEGER
	case settings.Float:
		return settingspb.SettingType_SETTING_TYPE_FLOAT
	case settings.Toggle:
		return settingspb.SettingType_SETTING_TYPE_TOGGLE
	case settings.Color:
		return settingspb.SettingType_SETTING_TYPE_COLOR
	case settings.DateTime:
		return settingspb.SettingType_SETTING_TYPE_DATETIME
	case settings.Password:
		return settingspb.SettingType_SETTING_TYPE_PASSWORD
	}
	return settingspb.SettingType_SETTING_TYPE_TEXT
}

// ProtoTypeToSettingType maps known wire types and uses text for unknown values.
func ProtoTypeToSettingType(t settingspb.SettingType) settings.SettingType {
	switch t {
	case settingspb.SettingType_SETTING_TYPE_TEXT:
		return settings.Text
	case settingspb.SettingType_SETTING_TYPE_SELECT:
		return settings.Select
	case settingspb.SettingType_SETTING_TYPE_MULTI_SELECT:
		return settings.MultiSelect
	case settingspb.SettingType_SETTING_TYPE_INTEGER:
		return settings.Integer
	case settingspb.SettingType_SETTING_TYPE_FLOAT:
		return settings.Float
	case settingspb.SettingType_SETTING_TYPE_TOGGLE:
		return settings.Toggle
	case settingspb.SettingType_SETTING_TYPE_COLOR:
		return settings.Color
	case settingspb.SettingType_SETTING_TYPE_DATETIME:
		return settings.DateTime
	case settingspb.SettingType_SETTING_TYPE_PASSWORD:
		return settings.Password
	}
	return settings.Text
}

// ConvertInterfaceToAny wraps the JSON representation in a protobuf bytes value.
// It returns protobuf encoding errors; JSON-unsupported inputs encode empty bytes.
func ConvertInterfaceToAny(v any) (*anypb.Any, error) {
	anyValue := &anypb.Any{}
	bytes, _ := json.Marshal(v)
	bytesValue := &wrapperspb.BytesValue{
		Value: bytes,
	}
	err := anypb.MarshalFrom(anyValue, bytesValue, grpcproto.MarshalOptions{})
	return anyValue, err
}

// ConvertAnyToInterface decodes wrapped JSON; JSON numbers become float64 values.
func ConvertAnyToInterface(anyValue *anypb.Any) (any, error) {
	var value any
	bytesValue := &wrapperspb.BytesValue{}
	err := anypb.UnmarshalTo(anyValue, bytesValue, grpcproto.UnmarshalOptions{})
	if err != nil {
		return value, err
	}
	uErr := json.Unmarshal(bytesValue.GetValue(), &value)
	if uErr != nil {
		return value, uErr
	}
	return value, nil
}

// ToProtoSettingFileSelection preserves nil and shares the extension slice with its input.
func ToProtoSettingFileSelection(s *settings.SettingFileSelection) *settingspb.SettingFileSelection {
	if s == nil {
		return nil
	}

	return &settingspb.SettingFileSelection{
		Enabled:      s.Enabled,
		AllowFolders: s.AllowFolders,
		Extensions:   s.Extensions,
		Multiple:     s.Multiple,
		Relative:     s.Relative,
		DefaultPath:  s.DefaultPath,
	}
}

// FromProtoSettingFileSelection preserves nil and shares the wire extension slice.
func FromProtoSettingFileSelection(s *settingspb.SettingFileSelection) *settings.SettingFileSelection {
	if s == nil {
		return nil
	}

	return &settings.SettingFileSelection{
		Enabled:      s.GetEnabled(),
		AllowFolders: s.GetAllowFolders(),
		Extensions:   s.GetExtensions(),
		Multiple:     s.GetMultiple(),
		Relative:     s.GetRelative(),
		DefaultPath:  s.GetDefaultPath(),
	}
}

// ToProtoSetting encodes metadata without a validator and returns nil on encoding failure.
// Password types always carry sensitive metadata.
func ToProtoSetting(s settings.Setting) *settingspb.Setting {
	options := make([]*settingspb.SettingOption, 0, len(s.Options))
	for _, o := range s.Options {
		value, err := ConvertInterfaceToAny(o.Value)
		if err != nil {
			return nil
		}

		options = append(options, &settingspb.SettingOption{
			Label:       o.Label,
			Description: o.Description,
			Value:       value,
		})
	}

	value, err := ConvertInterfaceToAny(s.Value)
	if err != nil {
		return nil
	}
	defaultValue, err := ConvertInterfaceToAny(s.Default)
	if err != nil {
		return nil
	}

	return &settingspb.Setting{
		Id:            s.ID,
		Label:         s.Label,
		Description:   s.Description,
		Type:          SettingTypeToProtoType(s.Type),
		Value:         value,
		Default:       defaultValue,
		Options:       options,
		FileSelection: ToProtoSettingFileSelection(s.FileSelection),
		DevOnly:       s.DevOnly,
		Sensitive:     grpcproto.Bool(s.Sensitive || s.Type == settings.Password),
	}
}

// FromProtoSetting decodes metadata without restoring executable validators.
// Invalid values yield a sensitive zero setting; absent sensitivity metadata stays private.
func FromProtoSetting(s *settingspb.Setting) settings.Setting {
	options := make([]settings.SettingOption, 0, len(s.GetOptions()))
	for _, o := range s.GetOptions() {
		value, err := ConvertAnyToInterface(o.GetValue())
		if err != nil {
			return settings.Setting{Sensitive: true}
		}
		options = append(options, settings.SettingOption{
			Label:       o.GetLabel(),
			Description: o.GetDescription(),
			Value:       value,
		})
	}
	value, err := ConvertAnyToInterface(s.GetValue())
	if err != nil {
		return settings.Setting{Sensitive: true}
	}
	var defaultValue any
	if s.GetDefault() != nil {
		defaultValue, err = ConvertAnyToInterface(s.GetDefault())
		if err != nil {
			return settings.Setting{Sensitive: true}
		}
	}
	return settings.Setting{
		ID:            s.GetId(),
		Label:         s.GetLabel(),
		Description:   s.GetDescription(),
		Type:          ProtoTypeToSettingType(s.GetType()),
		Value:         value,
		Default:       defaultValue,
		Options:       options,
		FileSelection: FromProtoSettingFileSelection(s.GetFileSelection()),
		DevOnly:       s.GetDevOnly(),
		// Missing sensitivity metadata is legacy/unknown, never explicit public.
		Sensitive: s.Sensitive == nil || s.GetSensitive() ||
			s.GetType() == settingspb.SettingType_SETTING_TYPE_PASSWORD,
	}
}
