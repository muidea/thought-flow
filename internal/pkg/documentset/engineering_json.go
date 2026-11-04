package documentset

import (
	"bytes"
	"encoding/json"
	"errors"
)

// UnmarshalJSON accepts the empty applicability placeholder some planners copy
// from acceptance contracts. It carries no information and grants no exemption
// from the required artifact fields. Nonempty declarations must be expressed in
// those fields or as unresolved questions, never silently discarded.
func (a *ArtifactContract) UnmarshalJSON(raw []byte) error {
	type fields ArtifactContract
	decoded := fields(*a)
	wire := struct {
		*fields
		NotApplicable emptyArtifactApplicability `json:"not_applicable"`
	}{fields: &decoded}
	if err := StrictJSON(raw, &wire); err != nil {
		return err
	}
	*a = ArtifactContract(decoded)
	return nil
}

type emptyArtifactApplicability struct{}

// Validate every occurrence, so a later empty duplicate cannot erase an earlier
// declaration containing substantive information.
func (*emptyArtifactApplicability) UnmarshalJSON(raw []byte) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return err
	}
	if compact.String() != "{}" {
		return errors.New("artifact.not_applicable must be omitted or an empty object; preserve applicability reasons in the artifact fields or open questions")
	}
	return nil
}
