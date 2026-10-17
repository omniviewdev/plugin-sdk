package settings

import (
	"testing"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func settingWireRoundTrip(t *testing.T, setting *settingspb.Setting) *settingspb.Setting {
	t.Helper()
	wire, err := proto.Marshal(setting)
	require.NoError(t, err)
	var received settingspb.Setting
	require.NoError(t, proto.Unmarshal(wire, &received))
	return &received
}

func TestSettingWireRoundTripPreservesSensitivityClassification(t *testing.T) {
	for _, tc := range []struct {
		name          string
		typeOf        pkgsettings.SettingType
		sensitive     bool
		wantSensitive bool
	}{
		{name: "explicit-public", typeOf: pkgsettings.Text},
		{name: "sensitive-text", typeOf: pkgsettings.Text, sensitive: true, wantSensitive: true},
		{name: "password-without-flag", typeOf: pkgsettings.Password, wantSensitive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := pkgsettings.Setting{
				ID:            tc.name,
				Type:          tc.typeOf,
				Sensitive:     tc.sensitive,
				Value:         "current-value",
				Default:       "default-value",
				Options:       []pkgsettings.SettingOption{{Label: "option", Value: "option-value"}},
				FileSelection: &pkgsettings.SettingFileSelection{DefaultPath: "default-file-path"},
			}
			sent := ToProtoSetting(original)
			require.NotNil(t, sent)
			received := settingWireRoundTrip(t, sent)
			require.NotNil(t, received.Sensitive, "current SDK must transmit even explicit false classification")
			require.Equal(t, tc.wantSensitive, received.GetSensitive())
			decoded := FromProtoSetting(received)
			require.Equal(t, original.ID, decoded.ID)
			require.Equal(t, tc.wantSensitive, decoded.Sensitive)
			require.Equal(t, original.Value, decoded.Value)
			require.Equal(t, original.Default, decoded.Default)
			require.Equal(t, original.Options, decoded.Options)
			require.Equal(t, original.FileSelection, decoded.FileSelection)
			if tc.wantSensitive {
				require.Nil(t, decoded.GetValue(), "inspection accessor must not expose classified secret")
			} else {
				require.Equal(t, original.Value, decoded.GetValue())
			}
		})
	}
}

func TestLegacySettingWireWithoutClassificationFailsClosed(t *testing.T) {
	original := pkgsettings.Setting{
		ID: "legacy-unclassified", Type: pkgsettings.Text,
		Value: "legacy-current", Default: "legacy-default",
		Options:       []pkgsettings.SettingOption{{Value: "legacy-option"}},
		FileSelection: &pkgsettings.SettingFileSelection{DefaultPath: "legacy-file-path"},
	}
	legacy := ToProtoSetting(original)
	require.NotNil(t, legacy)
	legacy.Sensitive = nil // Old protobuf senders do not emit this field at all.
	received := settingWireRoundTrip(t, legacy)
	require.Nil(t, received.Sensitive)
	decoded := FromProtoSetting(received)
	require.True(t, decoded.Sensitive)
	require.Nil(t, decoded.GetValue())
	require.Equal(t, original.Default, decoded.Default)
	require.Equal(t, original.Options, decoded.Options)
	require.Equal(t, original.FileSelection, decoded.FileSelection)
	reencoded := settingWireRoundTrip(t, ToProtoSetting(decoded))
	require.NotNil(t, reencoded.Sensitive)
	require.True(t, reencoded.GetSensitive(), "legacy unknown cannot be upgraded to public by re-encoding")
}

func TestPasswordWireCannotDeclareItselfPublic(t *testing.T) {
	setting := ToProtoSetting(pkgsettings.Setting{ID: "password", Type: pkgsettings.Password, Value: "secret"})
	require.NotNil(t, setting)
	setting.Sensitive = proto.Bool(false)
	decoded := FromProtoSetting(settingWireRoundTrip(t, setting))
	require.Equal(t, pkgsettings.Password, decoded.Type)
	require.True(t, decoded.Sensitive)
	require.Nil(t, decoded.GetValue())
}

func TestMalformedSettingWireCannotBecomePublicSchema(t *testing.T) {
	for _, field := range []string{"value", "default", "option"} {
		t.Run(field, func(t *testing.T) {
			setting := ToProtoSetting(pkgsettings.Setting{ID: "broken", Type: pkgsettings.Text, Value: "public", Default: "public-default", Options: []pkgsettings.SettingOption{{Value: "public-option"}}})
			require.NotNil(t, setting)
			malformed := &anypb.Any{TypeUrl: "invalid.test/not-json", Value: []byte("invalid")}
			switch field {
			case "value":
				setting.Value = malformed
			case "default":
				setting.Default = malformed
			case "option":
				setting.Options[0].Value = malformed
			}
			decoded := FromProtoSetting(settingWireRoundTrip(t, setting))
			require.True(t, decoded.Sensitive, "decode failure is not public classification")
			require.Nil(t, decoded.GetValue())
		})
	}
}

func TestLegacySettingWithoutDefaultStillDecodesFailClosed(t *testing.T) {
	setting := ToProtoSetting(pkgsettings.Setting{ID: "legacy", Type: pkgsettings.Text, Value: "legacy-value"})
	require.NotNil(t, setting)
	setting.Default = nil
	setting.Sensitive = nil
	decoded := FromProtoSetting(settingWireRoundTrip(t, setting))
	require.Equal(t, "legacy-value", decoded.Value)
	require.Nil(t, decoded.Default)
	require.True(t, decoded.Sensitive)
}
