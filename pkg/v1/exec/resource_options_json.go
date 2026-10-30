package exec

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

// UnmarshalJSON validates raw evidence before ordinary decoding can erase
// duplicates or replace malformed Unicode. The explicit field switch also
// rejects case-folded aliases; opaque selector and identifier values are data.
// Tagged scalar types and explicit optional-field presence survive decoding.
// A decoding or validation error leaves the receiver unchanged.
func (o *SessionOptions) UnmarshalJSON(data []byte) error {
	if o == nil {
		return optionError("INVALID_REQUEST", "Nil exec option receiver.")
	}
	if err := resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return optionError("INVALID_REQUEST", "Exec options must be an object.")
	}
	var out SessionOptions
	for d.More() {
		key, tokenErr := d.Token()
		if tokenErr != nil {
			return optionError("INVALID_REQUEST", "Malformed exec option member.")
		}
		err = decodeSessionOptionMember(d, &out, key)
		if err != nil {
			if resourcev2.ErrorCode(err) != "INTERNAL" {
				return err
			}
			return optionError("INVALID_REQUEST", "Malformed exec option value.")
		}
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') {
		return optionError("INVALID_REQUEST", "Malformed exec options.")
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return optionError("INVALID_REQUEST", "Trailing exec JSON.")
	}
	if out.HasResourceIntent() {
		out, err = NormalizeResourceSessionOptions(out)
	} else {
		err = ValidateSessionOptions(out)
	}
	if err != nil {
		return err
	}
	*o = out
	return nil
}

// decodeSessionOptionMember keeps field-name admission and typed decoding in
// one place. Allocate optional values before decoding so explicit null cannot
// collapse into absent resource intent.
func decodeSessionOptionMember(d *json.Decoder, out *SessionOptions, key json.Token) error {
	switch key {
	case "id":
		return d.Decode(&out.ID)
	case "params":
		return d.Decode(&out.Params)
	case "labels":
		return d.Decode(&out.Labels)
	case "resource_plugin":
		return d.Decode(&out.ResourcePlugin)
	case "resource_key":
		return d.Decode(&out.ResourceKey)
	case "command":
		return d.Decode(&out.Command)
	case "tty":
		return d.Decode(&out.TTY)
	case "resource_data":
		out.ResourceData = new(resourcev2.Value)
		return d.Decode(out.ResourceData)
	case "reference":
		out.Reference = new(resourcev2.ResourceReference)
		return d.Decode(out.Reference)
	case "route":
		out.Route = new(resourcev2.AccessRoute)
		return d.Decode(out.Route)
	default:
		return optionError("INVALID_REQUEST", "Unknown exec option member.")
	}
}
