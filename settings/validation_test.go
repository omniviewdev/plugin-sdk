package settings

import (
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
		return ValidationResult{Code: "secret", Message: value.(string)}
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
	s := Setting{Value: "saved", Validator: func(value any) ValidationResult {
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
