package settings

import (
	"fmt"
	"reflect"
)

// Setting describes a typed preference, its default, and optional validation.
// Sensitive values are hidden by GetValue; validators must not disclose secrets.
//
//nolint:recvcheck // clone requires a value copy; public methods retain their pointer API.
type Setting struct {
	// ID is the unique identifier of the setting
	ID string `json:"id"`
	// Label is the human readable label of the setting
	Label string `json:"label"`
	// Description is the human readable description of the setting
	Description string `json:"description"`
	// Type is the type of the setting
	Type SettingType `json:"type"`
	// Value is the value of the setting
	Value any `json:"value"`
	// Default is the default value of the setting
	Default any `json:"default"`
	// Validator returns user-safe contextual feedback. Never include secrets in messages.
	Validator func(any) ValidationResult `json:"-"`
	// Options is an optional list of options for a select setting
	Options []SettingOption `json:"options"`
	// FileSelection is an optional setting for file selection
	FileSelection *SettingFileSelection `json:"fileSelection"`
	// Sensitive is a flag to indicate if the setting is sensitive and should not be
	// shown in the UI, nor allowed to be used by any other plugin.
	Sensitive bool `json:"sensitive"`
	// DevOnly indicates the setting should only be visible when dev mode is active.
	DevOnly bool `json:"devOnly"`
}

// clone returns a copy of the Setting with deep-copied mutable fields.
func (s Setting) clone() Setting {
	s.Value = cloneValue(s.Value)
	s.Default = cloneValue(s.Default)
	if s.Options != nil {
		opts := make([]SettingOption, len(s.Options))
		for i, opt := range s.Options {
			opts[i] = SettingOption{
				Label:       opt.Label,
				Description: opt.Description,
				Value:       cloneValue(opt.Value),
			}
		}
		s.Options = opts
	}
	if s.FileSelection != nil {
		fsCopy := *s.FileSelection
		if fsCopy.Extensions != nil {
			exts := make([]string, len(fsCopy.Extensions))
			copy(exts, fsCopy.Extensions)
			fsCopy.Extensions = exts
		}
		s.FileSelection = &fsCopy
	}
	return s
}

// Validate checks if the value of the setting is valid using an optional
// validation function. If no validation function is provided, it returns nil.
func (s *Setting) Validate(value any) error {
	result := s.Validation(value)
	if !result.Valid {
		return fmt.Errorf("%s", result.Message)
	}
	return nil
}

// ValidationResult is safe to show in settings forms. Codes are stable machine-readable identifiers.
type ValidationResult struct {
	Valid   bool   `json:"valid"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Validation evaluates the registered validator without changing the setting.
func (s *Setting) Validation(value any) ValidationResult {
	result := ValidationResult{Valid: true, Code: "valid", Message: "Value is valid."}
	if s.Validator != nil {
		result = s.Validator(value)
	}
	if !result.Valid && result.Message == "" {
		result.Message = "Value did not pass validation."
	}
	if s.Sensitive {
		if result.Valid {
			return ValidationResult{Valid: true, Code: "valid", Message: "Value is valid."}
		}
		return ValidationResult{Code: "invalid", Message: "Value did not pass validation."}
	}
	return result
}

// SetValue sets the value of the setting and validates it using the
// validator function defined.
func (s *Setting) SetValue(value any) error {
	if err := s.Validate(value); err != nil {
		return err
	}
	return s.set(value)
}

// GetValue returns the value of the setting.
func (s *Setting) GetValue() any {
	if s.Sensitive {
		// Don't expose to the UI
		return nil
	}

	if s.Value == nil {
		return s.Default
	}

	return s.Value
}

// ResetValue resets the value of the setting to its default value.
func (s *Setting) ResetValue() {
	s.Value = s.Default
}

// ========================================= PRIVATE METHODS ==================================

// tryConvertSlice attempts to convert a slice of any type to a slice of interface{}.
func tryConvertSlice(value any) ([]any, bool) {
	valReflect := reflect.ValueOf(value)
	if valReflect.Kind() != reflect.Slice {
		return nil, false
	}
	result := make([]any, valReflect.Len())
	for i := range valReflect.Len() {
		result[i] = valReflect.Index(i).Interface()
	}
	return result, true
}

// set performs the main setting logic, doing any necessary conversion to try to satisfy any easily
// convertible types. This is a private method so we can save after a bulk vs individual setting change.
func (s *Setting) set(value any) error {
	currentVal := s.Value
	currType := reflect.TypeOf(currentVal)
	valType := reflect.TypeOf(value)

	switch {
	case currType.Kind() == reflect.Slice && valType.Kind() == reflect.Slice:
		// convert slice elements one by one if the destination is []interface{}
		value, _ = tryConvertSlice(value)
	case currType.ConvertibleTo(valType):
		// convert the value to the type of the current value
		value = reflect.ValueOf(value).Convert(currType).Interface()
	case currType.Kind() == reflect.Float64 && valType.Kind() == reflect.Int:
		// convert int to float64
		value = float64(reflect.ValueOf(value).Int())
	case currType.Kind() == reflect.Int && valType.Kind() == reflect.Float64:
		// convert float64 to int
		value = int(reflect.ValueOf(value).Float())
	case !reflect.TypeOf(value).AssignableTo(currType):
		// for non-slice types, fall back to the original type check
		return fmt.Errorf(
			"setting type mismatch for setting '%s'. currently has %s, tried to assign %s",
			s.ID,
			reflect.TypeOf(currentVal).String(),
			reflect.TypeOf(value).String(),
		)
	}

	// all good, assign the value
	s.Value = value
	return nil
}
