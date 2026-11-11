package resourcev2

import "encoding/json"

// UnmarshalJSON rejects unknown, duplicate, null, and ambiguous target fields.
func (v *SchemaTarget) UnmarshalJSON(data []byte) error {
	type plain SchemaTarget
	var out plain
	if _, err := decodeDomainObject(data, &out); err != nil {
		return err
	}
	value := SchemaTarget(out)
	if err := validateSchemaTarget(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// MarshalJSON requires exactly one explicit, valid target.
func (v SchemaTarget) MarshalJSON() ([]byte, error) {
	if err := admission(v); err != nil {
		return nil, err
	}
	if err := validateSchemaTarget(v); err != nil {
		return nil, err
	}
	type plain SchemaTarget
	return json.Marshal(plain(v))
}

// UnmarshalJSON admits one exact auxiliary reference and optional source revision.
func (v *AuxiliarySchemaTarget) UnmarshalJSON(data []byte) error {
	type plain AuxiliarySchemaTarget
	var out plain
	if _, err := decodeDomainObject(data, &out, "key", "reference"); err != nil {
		return err
	}
	value := AuxiliarySchemaTarget(out)
	if err := validateAuxiliarySchemaTarget(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// MarshalJSON validates the auxiliary identity without changing its observation.
func (v AuxiliarySchemaTarget) MarshalJSON() ([]byte, error) {
	if err := admission(v); err != nil {
		return nil, err
	}
	if err := validateAuxiliarySchemaTarget(v); err != nil {
		return nil, err
	}
	type plain AuxiliarySchemaTarget
	return json.Marshal(plain(v))
}

// UnmarshalJSON rejects unknown, duplicate, null, and missing request fields.
func (v *SchemaRequest) UnmarshalJSON(data []byte) error {
	type plain SchemaRequest
	var out plain
	if _, err := decodeDomainObject(data, &out, "route", "type", "target"); err != nil {
		return err
	}
	value := SchemaRequest(out)
	if err := ValidateSchemaRequest(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// MarshalJSON validates the route, type, and selected target before encoding.
func (v SchemaRequest) MarshalJSON() ([]byte, error) {
	if err := ValidateSchemaRequest(v); err != nil {
		return nil, err
	}
	type plain SchemaRequest
	return json.Marshal(plain(v))
}

// UnmarshalJSON requires both the admitted target and exact document reference.
func (v *SchemaDocumentRequest) UnmarshalJSON(data []byte) error {
	type plain SchemaDocumentRequest
	var out plain
	if _, err := decodeDomainObject(data, &out, "request", "reference"); err != nil {
		return err
	}
	value := SchemaDocumentRequest(out)
	if err := ValidateSchemaDocumentRequest(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// MarshalJSON validates the complete target-qualified document request.
func (v SchemaDocumentRequest) MarshalJSON() ([]byte, error) {
	if err := ValidateSchemaDocumentRequest(v); err != nil {
		return nil, err
	}
	type plain SchemaDocumentRequest
	return json.Marshal(plain(v))
}
