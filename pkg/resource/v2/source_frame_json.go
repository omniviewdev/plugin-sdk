package resourcev2

import (
	"encoding/json"
	"strconv"
)

// MarshalJSON emits source sequence as decimal text, never a JavaScript number.
func (f SourceFrame) MarshalJSON() ([]byte, error) {
	if f.Sequence == 0 { return nil, invalid("invalid source sequence") }
	type frame SourceFrame
	return json.Marshal(struct {
		frame
		Sequence string `json:"sequence"`
	}{frame: frame(f), Sequence: strconv.FormatUint(f.Sequence, 10)})
}

// UnmarshalJSON rejects noncanonical or out-of-range source sequence text.
func (f *SourceFrame) UnmarshalJSON(data []byte) error {
	type frame SourceFrame
	var decoded struct {
		frame
		Sequence string `json:"sequence"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil { return err }
	sequence, err := strconv.ParseUint(decoded.Sequence, 10, 64)
	if err != nil || sequence == 0 || strconv.FormatUint(sequence, 10) != decoded.Sequence { return invalid("invalid source sequence") }
	*f = SourceFrame(decoded.frame)
	f.Sequence = sequence
	return nil
}
