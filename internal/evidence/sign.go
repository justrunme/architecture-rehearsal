package evidence

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// SignedEnvelope is a HMAC-SHA256 signed evidence wrapper (v0.7).
// For production, replace with Sigstore/cosign or KMS-backed keys.
type SignedEnvelope struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Algorithm  string          `json:"algorithm"`
	KeyID      string          `json:"keyId"`
	SignedAt   time.Time       `json:"signedAt"`
	Payload    json.RawMessage `json:"payload"`
	Signature  string          `json:"signature"`
}

// SignReportHMAC signs a report digest with HMAC-SHA256 (legacy v0.7 envelope).
// Prefer SignEvidenceStatement for v1.1 chain-bound DSSE.
func SignReportHMAC(rep *analyze.Report, secret []byte, keyID string) (*SignedEnvelope, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("empty HMAC secret")
	}
	if keyID == "" {
		keyID = "default"
	}
	// Include content digests so legacy envelope still binds artifacts.
	body := map[string]any{
		"semanticDigest": rep.SemanticDigest,
		"changeId":       rep.ChangeID,
		"baselineId":     rep.BaselineID,
		"baselineDigest": rep.BaselineDigest,
		"changeDigest":   rep.ChangeDigest,
		"proposedDigest": rep.ProposedDigest,
		"decision":       rep.Decision,
		"risk":           rep.Risk,
		"version":        rep.Version,
		"signedAt":       time.Now().UTC().Format(time.RFC3339Nano),
		"keyId":          keyID,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	sig := hex.EncodeToString(mac.Sum(nil))
	return &SignedEnvelope{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       "SignedEvidence",
		Algorithm:  "HMAC-SHA256",
		KeyID:      keyID,
		SignedAt:   time.Now().UTC(),
		Payload:    raw,
		Signature:  sig,
	}, nil
}

// VerifyHMAC checks a signed envelope.
func VerifyHMAC(env *SignedEnvelope, secret []byte) (bool, error) {
	if env == nil {
		return false, fmt.Errorf("nil envelope")
	}
	if env.Algorithm != "HMAC-SHA256" {
		return false, fmt.Errorf("unsupported algorithm %s", env.Algorithm)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(env.Payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(env.Signature)), nil
}
