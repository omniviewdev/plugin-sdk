package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestStructuredValidationEnforcesAtomicSave(t *testing.T) {
	validator := func(value any) ValidationResult {
		if value == "bad" {
			return ValidationResult{Code: "unusable", Message: "Choose a working tool."}
		}
		return ValidationResult{Valid: true, Code: "ready", Message: "Tool is ready."}
	}
	p := NewProvider(ProviderOpts{PluginSettings: []Category{{ID: "tools", Settings: map[string]Setting{
		"path":  {ID: "path", Type: Text, Default: "default", Validator: validator},
		"other": {ID: "other", Type: Text, Default: "original"},
	}}}})
	s, err := p.GetSetting("tools.path")
	if err != nil {
		t.Fatal(err)
	}
	if result := s.Validation("good"); !result.Valid || result.Code != "ready" {
		t.Fatalf("lost structured feedback: %+v", result)
	}
	if err := p.SetSetting("tools.path", "bad"); err == nil {
		t.Fatal("direct save accepted invalid value")
	}
	if err := p.SetSettings(map[string]any{"tools.path": "bad", "tools.other": "changed"}); err == nil {
		t.Fatal("bulk save accepted invalid value")
	}
	if got, _ := p.GetString("tools.other"); got != "original" {
		t.Fatal("bulk save partially committed")
	}
	if err := p.SetSetting("tools.path", "good"); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveValidationRedactsFeedback(t *testing.T) {
	s := Setting{Sensitive: true, Validator: func(value any) ValidationResult {
		return ValidationResult{Code: "secret", Message: fmt.Sprint(value)}
	}}
	if result := s.Validation(
		"private-input",
	); result.Valid || strings.Contains(result.Message, "private-input") ||
		result.Code == "secret" {
		t.Fatalf("sensitive feedback leaked: %+v", result)
	}
	if err := s.Validate("private-input"); err == nil || strings.Contains(err.Error(), "private-input") {
		t.Fatal("sensitive save leaked input")
	}
}

func TestInvalidStructuredValidationNormalizesEmptyMessage(t *testing.T) {
	for _, message := range []string{"", "Choose a working tool."} {
		t.Run(message, func(t *testing.T) {
			s := Setting{Value: "saved", Validator: func(any) ValidationResult {
				return ValidationResult{Code: "unusable", Message: message}
			}}
			want := message
			if want == "" {
				want = "Value did not pass validation."
			}
			result := s.Validation("draft")
			if result.Valid || result.Code != "unusable" || result.Message != want {
				t.Fatalf("unexpected draft feedback: %+v", result)
			}
			if err := s.SetValue("draft"); err == nil || err.Error() != want {
				t.Fatalf("unexpected save error: %v", err)
			}
			if s.Value != "saved" {
				t.Fatal("invalid save changed value")
			}
		})
	}
}

func TestValidationUsesOneCallbackWithoutMutation(t *testing.T) {
	calls := 0
	s := Setting{Value: "saved", Validator: func(_ any) ValidationResult {
		calls++
		return ValidationResult{Valid: true, Code: "ready", Message: "Tool is ready."}
	}}
	want := ValidationResult{Valid: true, Code: "ready", Message: "Tool is ready."}
	if got := s.Validation("draft"); got != want {
		t.Fatalf("lost contextual feedback: %+v", got)
	}
	if calls != 1 || s.Value != "saved" {
		t.Fatalf("draft evaluation changed state: calls=%d, value=%v", calls, s.Value)
	}
	if err := s.Validate("draft"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || s.Value != "saved" {
		t.Fatalf("write enforcement evaluated more than once or changed state: calls=%d, value=%v", calls, s.Value)
	}
}

func TestMergeSettingsRestoresRegisteredValidator(t *testing.T) {
	p := newTestProvider(t, Category{ID: "tools", Settings: map[string]Setting{
		"path": {ID: "path", Type: Text, Default: "saved"},
	}})
	p.mergeSettings(Category{ID: "tools", Settings: map[string]Setting{
		"path": {ID: "path", Type: Text, Default: "default", Validator: func(any) ValidationResult {
			return ValidationResult{Code: "unusable", Message: "Choose a working tool."}
		}},
	}})
	s, err := p.GetSetting("tools.path")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Validation("draft"); got.Valid || got.Code != "unusable" || got.Message != "Choose a working tool." {
		t.Fatalf("hydration lost registered validator: %+v", got)
	}
	if err := p.SetSetting("tools.path", "draft"); err == nil {
		t.Fatal("hydrated setting accepted invalid save")
	}
	if got, err := p.GetString("tools.path"); err != nil || got != "saved" {
		t.Fatalf("hydration or validation changed saved value: %q, %v", got, err)
	}
}

func TestCommitSettings_ValidatesOnceAndValidatorCanRead(t *testing.T) {
	p := newTestProvider(t, Category{ID: "tools", Settings: map[string]Setting{
		"path": {ID: "path", Type: Text, Default: "saved"},
	}})
	calls := 0
	err := p.RegisterSetting("tools", Setting{
		ID: "path", Type: Text, Default: "default",
		Validator: func(value any) ValidationResult {
			calls++
			saved, readErr := p.GetString("tools.path")
			if readErr != nil || saved != "saved" {
				t.Fatalf("validator could not read committed state: %q, %v", saved, readErr)
			}
			if value != "draft" {
				t.Fatalf("unexpected validation value: %v", value)
			}
			return ValidationResult{Valid: true}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	persistCalls := 0
	proposed := map[string]any{"tools.path": "draft"}
	result, err := p.CommitSettings(context.Background(), proposed, func(values map[string]any) error {
		persistCalls++
		if calls != 1 || values["tools.path"] != "draft" {
			t.Fatalf("persist did not receive one validated update: calls=%d, values=%v", calls, values)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || persistCalls != 1 || !result.Committed || !result.Applied || result.Uncertain {
		t.Fatalf("unexpected commit: calls=%d, persist=%d, result=%+v", calls, persistCalls, result)
	}
}

func TestCommitSettings_InvalidValuesSkipPersistence(t *testing.T) {
	p := newTestProvider(t, Category{ID: "tools", Settings: map[string]Setting{
		"path": {ID: "path", Type: Text, Default: "saved", Validator: func(any) ValidationResult {
			return ValidationResult{Code: "invalid", Message: "Choose a working tool."}
		}},
	}})
	called := false
	values := map[string]any{"tools.path": "draft"}
	result, err := p.CommitSettings(context.Background(), values, func(map[string]any) error {
		called = true
		return nil
	})
	if err == nil || called || result != (CommitResult{}) {
		t.Fatalf("invalid update reached persistence: err=%v, called=%t, result=%+v", err, called, result)
	}
	if got, readErr := p.GetString("tools.path"); readErr != nil || got != "saved" {
		t.Fatalf("invalid update changed committed state: %q, %v", got, readErr)
	}
}

func TestSettingNormalizationRejectsInvalidNumbersAndBooleans(t *testing.T) {
	for _, value := range []any{
		1.5, math.Inf(1), math.NaN(), uint64(^uint(0)),
		json.Number("9223372036854775808"), json.Number("1.000000000000000001"),
	} {
		s := Setting{Type: Integer, Default: 1, Value: 1}
		if err := s.SetValue(value); err == nil {
			t.Fatalf("integer accepted invalid value: %v", value)
		}
		if s.Value != 1 {
			t.Fatalf("failed normalization changed value: %v", s.Value)
		}
	}
	for _, value := range []any{"true", 1, nil} {
		s := Setting{Type: Toggle, Default: false}
		if result := s.Validation(value); result.Valid {
			t.Fatalf("boolean accepted invalid value: %v", value)
		}
	}
	s := Setting{Type: Float, Default: 1.0}
	if err := s.SetValue(math.Inf(-1)); err == nil {
		t.Fatal("float accepted infinity")
	}
}

func TestSettingNormalizationCanonicalSlicesAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  SettingType
		def   any
		input any
		want  any
	}{
		{"strings", Text, []string{}, []any{"a", "b"}, []string{"a", "b"}},
		{"integers", Integer, []int{}, []any{float64(2), json.Number("3")}, []int{2, 3}},
		{"floats", Float, []float64{}, []any{2, float64(3)}, []float64{2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := Setting{Type: tc.kind, Default: tc.def, Validator: func(value any) ValidationResult {
				calls++
				if fmt.Sprintf("%T", value) != fmt.Sprintf("%T", tc.want) {
					t.Fatalf("validator received noncanonical value: %T", value)
				}
				return ValidationResult{Valid: true}
			}}
			if result := s.Validation(tc.input); !result.Valid || calls != 1 {
				t.Fatalf("draft normalization failed: %+v, calls=%d", result, calls)
			}
			if err := s.SetValue(tc.input); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || fmt.Sprint(s.Value) != fmt.Sprint(tc.want) {
				t.Fatalf("save normalization failed: %v, calls=%d", s.Value, calls)
			}
		})
	}
}
