package resourcev2

// UnmarshalJSON rejects malformed, duplicate, and unsupported descriptor fields.
func (v *ActionDescriptor) UnmarshalJSON(data []byte) error {
	type plain ActionDescriptor
	var out plain
	object, err := decodeDomainObject(data, &out, "id", "scope", "streaming")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "paramsSchema", "outputSchema"); err != nil {
		return err
	}
	value := ActionDescriptor(out)
	if err := ValidateActionDescriptors([]ActionDescriptor{value}); err != nil {
		return err
	}
	*v = value
	return nil
}
