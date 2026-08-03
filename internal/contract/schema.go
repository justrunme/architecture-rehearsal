package contract

import (
	"encoding/json"
	"fmt"
)

// Required fields per kind (lightweight schema — full JSON Schema files live in /schemas).
var requiredFields = map[string][]string{
	KindSnapshot:     {"id", "phase", "nodes"},
	KindChange:       {"id", "kind"},
	KindReport:       {"changeId", "decision", "risk"},
	KindVerification: {"changeId", "outcome"},
	KindEvidence:     {"payload", "signature"},
	KindRehearsalRun: {"id", "phase"},
	KindChain:        {"digests"},
}

// ValidateDocument checks apiVersion, kind, and required top-level fields.
func ValidateDocument(raw []byte) error {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	av, _ := m["apiVersion"].(string)
	if av != "" && !CanAccept(av) {
		return fmt.Errorf("%w: %s", ErrUnsupportedVersion, av)
	}
	kind, _ := m["kind"].(string)
	if kind == "" {
		// some change envelopes use kind as change type
		if _, ok := m["id"]; ok {
			return nil
		}
		return fmt.Errorf("missing kind")
	}
	req, ok := requiredFields[kind]
	if !ok {
		// allow extension kinds
		return nil
	}
	for _, f := range req {
		if _, ok := m[f]; !ok {
			// nested digests for chain
			if f == "digests" {
				if _, ok := m["baselineDigest"]; ok {
					continue
				}
			}
			return fmt.Errorf("schema %s: missing required field %q", kind, f)
		}
	}
	return nil
}

// SchemaDoc is human-readable schema index for /schemas.
type SchemaDoc struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Required   []string `json:"required"`
	Description string  `json:"description"`
}

// Catalog returns built-in schema catalog.
func Catalog() []SchemaDoc {
	return []SchemaDoc{
		{APIVersion: APIVersionV1Beta1, Kind: KindSnapshot, Required: requiredFields[KindSnapshot], Description: "Architecture graph snapshot"},
		{APIVersion: APIVersionV1Beta1, Kind: KindChange, Required: requiredFields[KindChange], Description: "Proposed change envelope"},
		{APIVersion: APIVersionV1Beta1, Kind: KindReport, Required: requiredFields[KindReport], Description: "Impact analysis report"},
		{APIVersion: APIVersionV1Beta1, Kind: KindVerification, Required: requiredFields[KindVerification], Description: "Post-deploy verification"},
		{APIVersion: APIVersionV1Beta1, Kind: KindEvidence, Required: requiredFields[KindEvidence], Description: "Signed evidence envelope (DSSE-style)"},
		{APIVersion: APIVersionV1Beta1, Kind: KindRehearsalRun, Required: requiredFields[KindRehearsalRun], Description: "Full rehearsal lifecycle record"},
		{APIVersion: APIVersionV1Beta1, Kind: KindChain, Required: []string{"baselineDigest", "changeDigest", "reportDigest"}, Description: "Content-addressed evidence chain"},
	}
}
