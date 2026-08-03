package evidence

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/contract"
)

// DSSEEnvelope is a simplified DSSE-style signed payload (v0.8).
// Compatible in spirit with in-toto/DSSE; not full Sigstore cosign wire format.
// For production: wrap with cosign keyless or KMS via REHEARSAL_SIGNING_MODE.
type DSSEEnvelope struct {
	APIVersion  string    `json:"apiVersion"`
	Kind        string    `json:"kind"`
	PayloadType string    `json:"payloadType"`
	PayloadB64  string    `json:"payload"` // base64
	Signatures  []DSSESig `json:"signatures"`
	// Digests of the signed chain for quick lookup.
	ChainDigests contract.ArtifactDigests `json:"chainDigests,omitempty"`
	SignedAt     time.Time                `json:"signedAt"`
}

// DSSESig is one signature over PAE(payloadType, payload).
type DSSESig struct {
	KeyID string `json:"keyid,omitempty"`
	Sig   string `json:"sig"` // base64
	// Alg: hmac-sha256 | ed25519
	Alg string `json:"alg"`
}

// PAE implements DSSE Pre-Authentication Encoding.
func PAE(payloadType string, payload []byte) []byte {
	// DSSEv1 PAE: "DSSEv1" SP len(type) SP type SP len(payload) SP payload
	typeLen := len(payloadType)
	payLen := len(payload)
	s := fmt.Sprintf("DSSEv1 %d %s %d ", typeLen, payloadType, payLen)
	out := make([]byte, 0, len(s)+payLen)
	out = append(out, []byte(s)...)
	out = append(out, payload...)
	return out
}

// SignDSSEHMAC signs payload with HMAC-SHA256 (shared secret; no non-repudiation).
func SignDSSEHMAC(payloadType string, payload []byte, secret []byte, keyID string, digests contract.ArtifactDigests) (*DSSEEnvelope, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("empty secret")
	}
	if keyID == "" {
		keyID = "hmac-default"
	}
	pae := PAE(payloadType, payload)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(pae)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return &DSSEEnvelope{
		APIVersion:   contract.APIVersionV1Beta1,
		Kind:         contract.KindEvidence,
		PayloadType:  payloadType,
		PayloadB64:   base64.StdEncoding.EncodeToString(payload),
		Signatures:   []DSSESig{{KeyID: keyID, Sig: sig, Alg: "hmac-sha256"}},
		ChainDigests: digests,
		SignedAt:     time.Now().UTC(),
	}, nil
}

// SignDSSEEd25519 signs with Ed25519 private key (file or generate ephemeral).
func SignDSSEEd25519(payloadType string, payload []byte, priv ed25519.PrivateKey, keyID string, digests contract.ArtifactDigests) (*DSSEEnvelope, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid ed25519 private key")
	}
	pae := PAE(payloadType, payload)
	sig := ed25519.Sign(priv, pae)
	if keyID == "" {
		pub := priv.Public().(ed25519.PublicKey)
		keyID = "ed25519-" + hex.EncodeToString(pub)[:12]
	}
	return &DSSEEnvelope{
		APIVersion:   contract.APIVersionV1Beta1,
		Kind:         contract.KindEvidence,
		PayloadType:  payloadType,
		PayloadB64:   base64.StdEncoding.EncodeToString(payload),
		Signatures:   []DSSESig{{KeyID: keyID, Sig: base64.StdEncoding.EncodeToString(sig), Alg: "ed25519"}},
		ChainDigests: digests,
		SignedAt:     time.Now().UTC(),
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

// GenerateEd25519Keypair creates a new key pair (for self-hosted bootstrap).
func GenerateEd25519Keypair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// LoadEd25519PrivateFromEnv loads REHEARSAL_ED25519_PRIVATE (hex seed or full key).
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

// SigningModeFromEnv returns hmac|ed25519|none.
func SigningModeFromEnv() string {
	m := os.Getenv("REHEARSAL_SIGNING_MODE")
	if m == "" {
		return "hmac"
	}
	return m
}
