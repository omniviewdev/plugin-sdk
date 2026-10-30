package exec

import (
	"bytes"
	"encoding/json"
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
		key, err := d.Token()
		if err != nil {
			return optionError("INVALID_REQUEST", "Malformed exec option member.")
		}
		switch key {
		case "id":
			err = d.Decode(&out.ID)
		case "params":
			err = d.Decode(&out.Params)
		case "labels":
			err = d.Decode(&out.Labels)
		case "resource_plugin":
			err = d.Decode(&out.ResourcePlugin)
		case "resource_key":
			err = d.Decode(&out.ResourceKey)
		case "command":
			err = d.Decode(&out.Command)
		case "tty":
			err = d.Decode(&out.TTY)
		case "resource_data":
			body := new(resourcev2.Value)
			err = d.Decode(body)
			out.ResourceData = body
		case "reference":
			reference := new(resourcev2.ResourceReference)
			err = d.Decode(reference)
			out.Reference = reference
		case "route":
			route := new(resourcev2.AccessRoute)
			err = d.Decode(route)
			out.Route = route
		default:
			return optionError("INVALID_REQUEST", "Unknown exec option member.")
		}
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
	if _, err = d.Token(); err != io.EOF {
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
