package settings

import (
	"encoding/json"

	"github.com/omniviewdev/plugin-sdk/settings"
	grpcproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
)

func SettingTypeToProtoType(t settings.SettingType) settingspb.SettingType {
	switch t {
	case settings.Text:
		return settingspb.SettingType_SETTING_TYPE_TEXT
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

func ProtoTypeToSettingType(t settingspb.SettingType) settings.SettingType {
	switch t {
	case settingspb.SettingType_SETTING_TYPE_TEXT:
		return settings.Text
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

func ConvertInterfaceToAny(v interface{}) (*anypb.Any, error) {
	anyValue := &anypb.Any{}
	bytes, _ := json.Marshal(v)
	bytesValue := &wrapperspb.BytesValue{
		Value: bytes,
	}
	err := anypb.MarshalFrom(anyValue, bytesValue, grpcproto.MarshalOptions{})
	return anyValue, err
}

func ConvertAnyToInterface(anyValue *anypb.Any) (interface{}, error) {
	var value interface{}
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

	return &settingspb.Setting{
		Id:            s.ID,
		Label:         s.Label,
		Description:   s.Description,
		Type:          SettingTypeToProtoType(s.Type),
		Value:         value,
		Options:       options,
		FileSelection: ToProtoSettingFileSelection(s.FileSelection),
	}
}

func FromProtoSetting(s *settingspb.Setting) settings.Setting {
	options := make([]settings.SettingOption, 0, len(s.GetOptions()))
	for _, o := range s.GetOptions() {
		value, err := ConvertAnyToInterface(o.GetValue())
		if err != nil {
			return settings.Setting{}
		}
		options = append(options, settings.SettingOption{
			Label:       o.GetLabel(),
			Description: o.GetDescription(),
			Value:       value,
		})
	}
	value, err := ConvertAnyToInterface(s.GetValue())
	if err != nil {
		return settings.Setting{}
	}
	return settings.Setting{
		ID:            s.GetId(),
		Label:         s.GetLabel(),
		Description:   s.GetDescription(),
		Type:          ProtoTypeToSettingType(s.GetType()),
		Value:         value,
		Options:       options,
		FileSelection: FromProtoSettingFileSelection(s.GetFileSelection()),
	}
}
