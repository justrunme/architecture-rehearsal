package evidence

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/contract"
)

// PayloadType for rehearsal evidence statements.
const PayloadTypeRehearsalEvidence = "application/vnd.rehearsal.evidence-chain+json"

// EvidenceStatement is the signed body (v1.1) — digests, timestamps, and key id are inside.
type EvidenceStatement struct {
	APIVersion   string                   `json:"apiVersion"`
	Kind         string                   `json:"kind"`
	SignedAt     time.Time                `json:"signedAt"`
	KeyID        string                   `json:"keyId"`
	ChangeID     string                   `json:"changeId,omitempty"`
	Decision     string                   `json:"decision,omitempty"`
	Risk         string                   `json:"risk,omitempty"`
	ChainDigests contract.ArtifactDigests `json:"chainDigests"`
	// SemanticDigest of the impact report (if any).
	ReportSemanticDigest string `json:"reportSemanticDigest,omitempty"`
	// Extra is optional opaque application data (e.g. report summary).
	Extra json.RawMessage `json:"extra,omitempty"`
}

// DSSEEnvelope is a simplified DSSE-style signed payload (v0.8/v1.1).
// Payload is base64(JSON(EvidenceStatement)) — all chain metadata is inside the signed payload.
type DSSEEnvelope struct {
	APIVersion  string    `json:"apiVersion"`
	Kind        string    `json:"kind"`
	PayloadType string    `json:"payloadType"`
	PayloadB64  string    `json:"payload"`
	Signatures  []DSSESig `json:"signatures"`
}

// DSSESig is one signature over PAE(payloadType, payload).
type DSSESig struct {
	KeyID string `json:"keyid,omitempty"`
	Sig   string `json:"sig"`
	Alg   string `json:"alg"`
}

// PAE implements DSSE Pre-Authentication Encoding.
func PAE(payloadType string, payload []byte) []byte {
	typeLen := len(payloadType)
	payLen := len(payload)
	s := fmt.Sprintf("DSSEv1 %d %s %d ", typeLen, payloadType, payLen)
	out := make([]byte, 0, len(s)+payLen)
	out = append(out, []byte(s)...)
	out = append(out, payload...)
	return out
}

// SignEvidenceStatement signs a complete evidence statement (preferred v1.1 API).
func SignEvidenceStatement(stmt EvidenceStatement, secret []byte) (*DSSEEnvelope, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("empty secret")
	}
	if stmt.SignedAt.IsZero() {
		stmt.SignedAt = time.Now().UTC()
	}
	if stmt.APIVersion == "" {
		stmt.APIVersion = contract.APIVersionV1Beta1
	}
	if stmt.Kind == "" {
		stmt.Kind = "EvidenceStatement"
	}
	if stmt.KeyID == "" {
		stmt.KeyID = "hmac-default"
	}
	raw, err := json.Marshal(stmt)
	if err != nil {
		return nil, err
	}
	return SignDSSEHMAC(PayloadTypeRehearsalEvidence, raw, secret, stmt.KeyID, stmt.ChainDigests)
}

// SignDSSEHMAC signs raw payload with HMAC-SHA256.
// Prefer SignEvidenceStatement so chain digests are inside the signed body.
func SignDSSEHMAC(payloadType string, payload []byte, secret []byte, keyID string, _ contract.ArtifactDigests) (*DSSEEnvelope, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("empty secret")
	}
	if keyID == "" {
		keyID = "hmac-default"
	}
	if payloadType == "" {
		payloadType = PayloadTypeRehearsalEvidence
	}
	pae := PAE(payloadType, payload)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(pae)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return &DSSEEnvelope{
		APIVersion:  contract.APIVersionV1Beta1,
		Kind:        contract.KindEvidence,
		PayloadType: payloadType,
		PayloadB64:  base64.StdEncoding.EncodeToString(payload),
		Signatures:  []DSSESig{{KeyID: keyID, Sig: sig, Alg: "hmac-sha256"}},
	}, nil
}

// SignDSSEEd25519 signs with Ed25519 private key.
func SignDSSEEd25519(payloadType string, payload []byte, priv ed25519.PrivateKey, keyID string, _ contract.ArtifactDigests) (*DSSEEnvelope, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid ed25519 private key")
	}
	if payloadType == "" {
		payloadType = PayloadTypeRehearsalEvidence
	}
	pae := PAE(payloadType, payload)
	sig := ed25519.Sign(priv, pae)
	if keyID == "" {
		pub := priv.Public().(ed25519.PublicKey)
		keyID = "ed25519-" + hex.EncodeToString(pub)[:12]
	}
	return &DSSEEnvelope{
		APIVersion:  contract.APIVersionV1Beta1,
		Kind:        contract.KindEvidence,
		PayloadType: payloadType,
		PayloadB64:  base64.StdEncoding.EncodeToString(payload),
		Signatures:  []DSSESig{{KeyID: keyID, Sig: base64.StdEncoding.EncodeToString(sig), Alg: "ed25519"}},
	}, nil
}

// VerifyDSSE verifies all signatures on the envelope.
func VerifyDSSE(env *DSSEEnvelope, hmacSecret []byte, ed25519Pub ed25519.PublicKey) (bool, error) {
	if env == nil || len(env.Signatures) == 0 {
		return false, fmt.Errorf("empty envelope")
	}
	payload, err := base64.StdEncoding.DecodeString(env.PayloadB64)
	if err != nil {
		return false, err
	}
	pae := PAE(env.PayloadType, payload)
	for _, s := range env.Signatures {
		raw, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil {
			return false, err
		}
		switch s.Alg {
		case "hmac-sha256":
			if len(hmacSecret) == 0 {
				return false, fmt.Errorf("hmac secret required")
			}
			mac := hmac.New(sha256.New, hmacSecret)
			_, _ = mac.Write(pae)
			if !hmac.Equal(mac.Sum(nil), raw) {
				return false, nil
			}
		case "ed25519":
			if len(ed25519Pub) != ed25519.PublicKeySize {
				return false, fmt.Errorf("ed25519 public key required")
			}
			if !ed25519.Verify(ed25519Pub, pae, raw) {
				return false, nil
			}
		default:
			return false, fmt.Errorf("unsupported alg %s", s.Alg)
		}
	}
	return true, nil
}

// ParseStatement extracts EvidenceStatement from a DSSE envelope.
func ParseStatement(env *DSSEEnvelope) (*EvidenceStatement, error) {
	if env == nil {
		return nil, fmt.Errorf("nil envelope")
	}
	raw, err := base64.StdEncoding.DecodeString(env.PayloadB64)
	if err != nil {
		return nil, err
	}
	var stmt EvidenceStatement
	if err := json.Unmarshal(raw, &stmt); err != nil {
		return nil, fmt.Errorf("payload is not EvidenceStatement: %w", err)
	}
	return &stmt, nil
}

// GenerateEd25519Keypair creates a new key pair.
func GenerateEd25519Keypair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// LoadEd25519PrivateFromEnv loads REHEARSAL_ED25519_PRIVATE (hex).
func LoadEd25519PrivateFromEnv() (ed25519.PrivateKey, error) {
	h := os.Getenv("REHEARSAL_ED25519_PRIVATE")
	if h == "" {
		return nil, fmt.Errorf("REHEARSAL_ED25519_PRIVATE not set")
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, err
	}
	if len(b) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(b), nil
	}
	if len(b) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(b), nil
	}
	return nil, fmt.Errorf("invalid ed25519 key length %d", len(b))
}

// SecretFromEnv loads REHEARSAL_HMAC_SECRET.
func SecretFromEnv() []byte {
	return []byte(os.Getenv("REHEARSAL_HMAC_SECRET"))
}

// SigningModeFromEnv returns hmac|ed25519|none.
func SigningModeFromEnv() string {
	m := os.Getenv("REHEARSAL_SIGNING_MODE")
	if m == "" {
		return "hmac"
	}
	return m
}
