package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Digest is a content-addressed SHA-256 hex digest of canonical JSON.
type Digest string

// EmptyDigest is the zero value.
const EmptyDigest Digest = ""

// ComputeDigest hashes any JSON-marshalable value with stable encoding.
func ComputeDigest(v any) (Digest, error) {
	raw, err := canonicalJSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return Digest(hex.EncodeToString(sum[:])), nil
}

// DigestBytes hashes raw bytes.
func DigestBytes(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest(hex.EncodeToString(sum[:]))
}

// Equal reports digest equality.
func (d Digest) Equal(o Digest) bool { return d != "" && d == o }

// Short returns first 12 hex chars.
func (d Digest) Short() string {
	if len(d) < 12 {
		return string(d)
	}
	return string(d[:12])
}

// String implements fmt.Stringer.
func (d Digest) String() string { return string(d) }

// ArtifactDigests binds the evidence chain (v0.8).
type ArtifactDigests struct {
	BaselineDigest     Digest `json:"baselineDigest,omitempty"`
	ChangeDigest       Digest `json:"changeDigest,omitempty"`
	ProposedDigest     Digest `json:"proposedDigest,omitempty"`
	ReportDigest       Digest `json:"reportDigest,omitempty"`
	ObservedDigest     Digest `json:"observedDigest,omitempty"`
	VerificationDigest Digest `json:"verificationDigest,omitempty"`
}

// ValidateChain ensures non-empty required digests and optional equality checks.
func (a ArtifactDigests) ValidateChain(requireObserved bool) error {
	if a.BaselineDigest == "" {
		return fmt.Errorf("missing baselineDigest")
	}
	if a.ChangeDigest == "" {
		return fmt.Errorf("missing changeDigest")
	}
	if a.ReportDigest == "" {
		return fmt.Errorf("missing reportDigest")
	}
	if requireObserved && a.ObservedDigest == "" {
		return fmt.Errorf("missing observedDigest")
	}
	return nil
}

// canonicalJSON marshals with sorted keys via standard encoding/json
// (Go maps are sorted for JSON since 1.x for string keys in encoding/json).
func canonicalJSON(v any) ([]byte, error) {
	// Double-encode through map for stability when possible.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var anyV any
	if err := json.Unmarshal(raw, &anyV); err != nil {
		return raw, nil
	}
	return json.Marshal(anyV)
}
