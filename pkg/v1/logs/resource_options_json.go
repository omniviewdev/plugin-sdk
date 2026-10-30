package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

// UnmarshalJSON preserves raw validation evidence and admits only exact field names.
func (o *CreateSessionOptions) UnmarshalJSON(data []byte) error {
	if o == nil {
		return resourceError("INVALID_REQUEST", "Nil logs options receiver.")
	}
	if err := resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return resourceError("INVALID_REQUEST", "Logs options must be an object.")
	}
	var out CreateSessionOptions
	for decoder.More() {
		key, e := decoder.Token()
		if e != nil {
			return resourceError("INVALID_REQUEST", "Malformed logs options.")
		}
		err = decodeSessionOption(decoder, key, &out)
		if err != nil {
			if resourcev2.ErrorCode(err) != "INTERNAL" {
				return err
			}
			return resourceError("INVALID_REQUEST", "Malformed logs option value.")
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return resourceError("INVALID_REQUEST", "Malformed logs options.")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return resourceError("INVALID_REQUEST", "Trailing logs options JSON.")
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*o = out
	return nil
}

func decodeSessionOption(decoder *json.Decoder, key any, out *CreateSessionOptions) error {
	switch key {
	case "resource_key":
		return decoder.Decode(&out.ResourceKey)
	case "resource_id":
		return decoder.Decode(&out.ResourceID)
	case "resource_data":
		out.ResourceData = new(resourcev2.Value)
		return decoder.Decode(out.ResourceData)
	case "reference":
		out.Reference = new(resourcev2.ResourceReference)
		return decoder.Decode(out.Reference)
	case "route":
		out.Route = new(resourcev2.AccessRoute)
		return decoder.Decode(out.Route)
	case "options":
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		nested := json.NewDecoder(bytes.NewReader(raw))
		nested.DisallowUnknownFields()
		return nested.Decode(&out.Options)
	default:
		return resourceError("INVALID_REQUEST", "Unknown logs option member.")
	}
}
