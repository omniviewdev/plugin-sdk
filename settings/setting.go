package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
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
	// RequiresRestart declares that changes apply after restart.
	RequiresRestart bool `json:"requiresRestart"`
	// PendingRestart is host snapshot projection, not provider-owned state.
	PendingRestart bool `json:"pendingRestart"`
}

// clone returns a copy of the Setting with deep-copied mutable fields.
func (s Setting) clone() Setting {
	s.Value = CloneValue(s.Value)
	s.Default = CloneValue(s.Default)
	if s.Options != nil {
		opts := make([]SettingOption, len(s.Options))
		for i, opt := range s.Options {
			opts[i] = SettingOption{
				Label:       opt.Label,
				Description: opt.Description,
				Value:       CloneValue(opt.Value),
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
		return errors.New(result.Message)
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
	normalized, err := s.NormalizeValue(value)
	if err != nil {
		return s.validationResult(ValidationResult{Code: "invalid_type", Message: "Value has an invalid type."})
	}
	return s.validateNormalized(normalized)
}

func (s *Setting) validateNormalized(value any) ValidationResult {
	result := ValidationResult{Valid: true, Code: "valid", Message: "Value is valid."}
	if s.Validator != nil {
		result = s.Validator(value)
	}
	return s.validationResult(result)
}

func (s *Setting) validationResult(result ValidationResult) ValidationResult {
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
	normalized, err := s.NormalizeValue(value)
	if err != nil {
		return fmt.Errorf("setting type mismatch for setting '%s'", s.ID)
	}
	if result := s.validateNormalized(CloneValue(normalized)); !result.Valid {
		return errors.New(result.Message)
	}
	s.Value = normalized
	return nil
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
	s.Value = CloneValue(s.Default)
}

// NormalizeValue converts supported JSON and typed values to the setting's canonical shape.
// It does not invoke custom validation or mutate the setting.
func (s *Setting) NormalizeValue(value any) (any, error) {
	schema := s.Default
	if schema == nil {
		schema = s.Value
	}
	kind, array := s.normalizationShape(schema)
	if array {
		return normalizeArray(kind, value)
	}
	if kind == Select || kind == "" {
		kind = scalarKind(schema, kind)
		if kind == "" {
			return CloneValue(value), nil
		}
	}
	return normalizeScalar(kind, value)
}

func (s *Setting) normalizationShape(schema any) (SettingType, bool) {
	kind := s.Type
	array := s.Type == MultiSelect || s.FileSelection != nil && s.FileSelection.Multiple
	switch schema.(type) {
	case []string, []int, []float64, []bool, []any:
		array = true
	}
	if kind == "" || kind == Select || kind == MultiSelect {
		if values, ok := schema.([]any); ok && len(values) != 0 {
			kind = scalarKind(values[0], kind)
		}
	}
	if !array {
		return kind, false
	}
	switch schema.(type) {
	case []int:
		kind = Integer
	case []float64:
		kind = Float
	case []bool:
		kind = Toggle
	case []string:
		kind = Text
	}
	if kind == MultiSelect || kind == Select {
		kind = Text
	}
	return kind, true
}

func scalarKind(value any, fallback SettingType) SettingType {
	switch value.(type) {
	case int:
		return Integer
	case float64:
		return Float
	case bool:
		return Toggle
	case string:
		return Text
	default:
		if fallback == Select {
			return Text
		}
		return fallback
	}
}

func normalizeArray(kind SettingType, value any) (any, error) {
	switch v := value.(type) {
	case []string:
		if kind != Integer && kind != Float && kind != Toggle {
			return CloneValue(v), nil
		}
	case []int:
		if kind == Integer {
			return CloneValue(v), nil
		}
	case []float64:
		if kind == Float {
			for _, n := range v {
				if math.IsNaN(n) || math.IsInf(n, 0) {
					return nil, errors.New("expected finite number")
				}
			}
			return CloneValue(v), nil
		}
	case []bool:
		if kind == Toggle {
			return CloneValue(v), nil
		}
	}
	switch values := value.(type) {
	case []any:
		return normalizeArrayElements(kind, values)
	case []string:
		return normalizeArrayElements(kind, values)
	case []int:
		return normalizeArrayElements(kind, values)
	case []float64:
		return normalizeArrayElements(kind, values)
	case []bool:
		return normalizeArrayElements(kind, values)
	default:
		return nil, errors.New("expected array")
	}
}

func normalizeArrayElements[T any](kind SettingType, values []T) (any, error) {
	switch kind {
	case Integer:
		return normalizeElements[T, int](Integer, values)
	case Float:
		return normalizeElements[T, float64](Float, values)
	case Toggle:
		return normalizeElements[T, bool](Toggle, values)
	case Text, Select, MultiSelect, Color, DateTime, Password:
		return normalizeElements[T, string](Text, values)
	default:
		return normalizeElements[T, string](Text, values)
	}
}

func normalizeElements[T, U any](kind SettingType, values []T) ([]U, error) {
	out := make([]U, len(values))
	for i, value := range values {
		normalized, err := normalizeScalar(kind, value)
		if err != nil {
			return nil, err
		}
		typed, ok := normalized.(U)
		if !ok {
			return nil, errors.New("unexpected normalized element type")
		}
		out[i] = typed
	}
	return out, nil
}

func normalizeScalar(kind SettingType, value any) (any, error) {
	if kind == Toggle {
		if v, ok := value.(bool); ok {
			return v, nil
		}
		return nil, errors.New("expected boolean")
	}
	if kind != Integer && kind != Float {
		if v, ok := value.(string); ok {
			return v, nil
		}
		return nil, errors.New("expected string")
	}
	return normalizeNumber(kind, value)
}

func normalizeNumber(kind SettingType, value any) (any, error) {
	var number float64
	switch v := value.(type) {
	case int:
		if kind == Integer {
			return v, nil
		}
		number = float64(v)
	case int8:
		number = float64(v)
	case int16:
		number = float64(v)
	case int32:
		number = float64(v)
	case int64:
		if kind == Integer {
			n := int(v)
			if int64(n) != v {
				return nil, errors.New("integer overflow")
			}
			return n, nil
		}
		number = float64(v)
	case uint:
		return normalizeUnsigned(kind, uint64(v))
	case uint8:
		return normalizeUnsigned(kind, uint64(v))
	case uint16:
		return normalizeUnsigned(kind, uint64(v))
	case uint32:
		return normalizeUnsigned(kind, uint64(v))
	case uint64:
		return normalizeUnsigned(kind, v)
	case float32:
		number = float64(v)
	case float64:
		number = v
	case json.Number:
		return normalizeJSONNumber(kind, v)
	default:
		return nil, errors.New("expected number")
	}
	return normalizeFloat(kind, number)
}

func normalizeFloat(kind SettingType, number float64) (any, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return nil, errors.New("expected finite number")
	}
	if kind == Float {
		return number, nil
	}
	bound := math.Ldexp(1, strconv.IntSize-1)
	if math.Trunc(number) != number || number < -bound || number >= bound {
		return nil, errors.New("expected in-range integer")
	}
	return int(number), nil
}

func normalizeJSONNumber(kind SettingType, value json.Number) (any, error) {
	if !json.Valid([]byte(value)) {
		return nil, errors.New("invalid number")
	}
	if kind != Integer {
		number, err := strconv.ParseFloat(string(value), 64)
		if err != nil {
			return nil, errors.New("invalid number")
		}
		return normalizeFloat(kind, number)
	}
	if number, err := strconv.ParseInt(string(value), 10, strconv.IntSize); err == nil {
		return int(number), nil
	}
	number, ok := new(big.Rat).SetString(string(value))
	if !ok || !number.IsInt() || !number.Num().IsInt64() {
		return nil, errors.New("expected in-range integer")
	}
	integer := number.Num().Int64()
	if int64(int(integer)) != integer {
		return nil, errors.New("integer overflow")
	}
	return int(integer), nil
}

func normalizeUnsigned(kind SettingType, value uint64) (any, error) {
	if kind == Float {
		return float64(value), nil
	}
	if value > uint64(^uint(0)>>1) {
		return nil, errors.New("integer overflow")
	}
	return int(value), nil
}
