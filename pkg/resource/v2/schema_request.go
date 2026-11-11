package resourcev2

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SchemaTarget selects exactly one current representation or auxiliary resource
// schema. Callers resolve default representations before constructing a target.
// Nested references and pointers are borrowed for the duration of resolution.
type SchemaTarget struct {
	Representation *Representation        `json:"representation,omitempty"`
	Auxiliary      *AuxiliarySchemaTarget `json:"auxiliary,omitempty"`
}

// AuxiliarySchemaTarget identifies one schema role on one exact resource.
// SourceRevision, when present, is a nonempty opaque equality observation.
// Neither the key nor a document reference grants access to another resource.
type AuxiliarySchemaTarget struct {
	Key            string            `json:"key"`
	Reference      ResourceReference `json:"reference"`
	SourceRevision *string           `json:"sourceRevision,omitempty"`
}

// SchemaRequest asks for one schema under an explicitly admitted route and type.
// The auxiliary reference must have exactly the requested type. A representation
// target requires an explicit format ID and revision, never an inferred default.
// Successful resolution does not grant access to the referenced document.
type SchemaRequest struct {
	Route  AccessRoute  `json:"route"`
	Type   string       `json:"type"`
	Target SchemaTarget `json:"target"`
}

// SchemaDocumentRequest qualifies an exact document reference with its demanded
// target. Consumers independently admit current access and the resolved binding;
// document identity or shared content alone is not an authorization grant.
type SchemaDocumentRequest struct {
	Request   SchemaRequest           `json:"request"`
	Reference SchemaDocumentReference `json:"reference"`
}

const maxAuxiliarySchemaKeyBytes = 256

func validateAuxiliarySchemaTarget(v AuxiliarySchemaTarget) error {
	if len(v.Key) == 0 || len(v.Key) > maxAuxiliarySchemaKeyBytes || !utf8.ValidString(v.Key) ||
		strings.IndexFunc(v.Key, unicode.IsControl) >= 0 {
		return invalid("auxiliary schema key must be 1-256 UTF-8 bytes without control characters")
	}
	if err := validateReference(v.Reference); err != nil {
		return err
	}
	return token(v.SourceRevision)
}

func validateSchemaTarget(v SchemaTarget) error {
	if (v.Representation == nil) == (v.Auxiliary == nil) {
		return invalid("schema target requires exactly one selection")
	}
	if v.Representation != nil {
		return validateRepresentation(*v.Representation)
	}
	return validateAuxiliarySchemaTarget(*v.Auxiliary)
}

// ValidateSchemaRequest validates a bounded, explicitly selected schema target.
// It does not authorize access or resolve a current catalog/default selection.
func ValidateSchemaRequest(v SchemaRequest) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if err := text(v.Type, true); err != nil {
		return err
	}
	if err := validateSchemaTarget(v.Target); err != nil {
		return err
	}
	if v.Target.Auxiliary != nil && v.Target.Auxiliary.Reference.Address.Type != v.Type {
		return invalid("auxiliary schema reference type does not match requested type")
	}
	return nil
}

// ValidateSchemaDocumentRequest validates a target-qualified exact document
// lookup. Current access and the target's resolved reference remain caller-owned.
func ValidateSchemaDocumentRequest(v SchemaDocumentRequest) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := ValidateSchemaRequest(v.Request); err != nil {
		return err
	}
	return validateSchemaDocumentReference(v.Reference)
}
