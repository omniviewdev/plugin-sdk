package resourcev2

// ValidateNativeQueryInput checks declared language constraints without changing
// canonical Value kinds. An empty discovery list preserves opaque legacy queries;
// once descriptors are advertised, only declared languages are admitted.
func ValidateNativeQueryInput(descriptor ResourceType, request NativeQueryRequest) error {
	if len(descriptor.NativeQueries) == 0 {
		return nil
	}
	for _, query := range descriptor.NativeQueries {
		if query.Language == request.Language {
			return ValidateSchemaValue(query.InputSchema, request.Query)
		}
	}
	return NewError("UNSUPPORTED", "Native query language is not declared.")
}

// UnmarshalJSON rejects duplicate fields and unsupported descriptor fields.
func (v *NativeQueryDescriptor) UnmarshalJSON(data []byte) error {
	type plain NativeQueryDescriptor
	var out plain
	if _, err := decodeDomainObject(data, &out, "language", "inputSchema"); err != nil {
		return err
	}
	if err := text(out.Language, true); err != nil {
		return err
	}
	if err := text(out.Description, false); err != nil {
		return err
	}
	if err := ValidateSchema(out.InputSchema); err != nil {
		return err
	}
	*v = NativeQueryDescriptor(out)
	return nil
}
