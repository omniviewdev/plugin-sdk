package settings

import (
	"errors"
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
		"path":  {ID: "path", Type: Text, Default: "default", ValidateValue: validator},
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
	s := Setting{Sensitive: true, ValidateValue: func(value any) ValidationResult {
		return ValidationResult{Code: "secret", Message: value.(string)}
	}}
	if result := s.Validation("private-input"); result.Valid || strings.Contains(result.Message, "private-input") || result.Code == "secret" {
		t.Fatalf("sensitive feedback leaked: %+v", result)
	}
	s.ValidateValue = nil
	s.Validator = func(any) error { return errors.New("private-input") }
	if result := s.Validation("private-input"); strings.Contains(result.Message, "private-input") {
		t.Fatal("legacy preflight leaked input")
	}
	if err := s.Validate("private-input"); err == nil || strings.Contains(err.Error(), "private-input") {
		t.Fatal("sensitive save leaked input")
	}
}

func TestInvalidStructuredValidationNormalizesEmptyMessage(t *testing.T) {
	for _, message := range []string{"", "Choose a working tool."} {
		t.Run(message, func(t *testing.T) {
			s := Setting{Value: "saved", ValidateValue: func(any) ValidationResult {
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

func TestLegacyValidationDraftHidesUntrustedErrorWithoutChangingSave(t *testing.T) {
	legacyErr := errors.New("credential=private-input")
	s := Setting{Validator: func(any) error { return legacyErr }}
	result := s.Validation("draft")
	if result.Valid || result.Message != "Value did not pass validation." {
		t.Fatalf("legacy draft exposed untrusted error: %+v", result)
	}
	if err := s.Validate("draft"); err != legacyErr {
		t.Fatalf("legacy save error changed: %v", err)
	}
}
