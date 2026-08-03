package evidence_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
)

func TestDSSEHMAC(t *testing.T) {
	sec := []byte("secret")
	env, err := evidence.SignDSSEHMAC("application/json", []byte(`{"a":1}`), sec, "k", contract.ArtifactDigests{})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := evidence.VerifyDSSE(env, sec, nil)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	ok, _ = evidence.VerifyDSSE(env, []byte("wrong"), nil)
	if ok {
		t.Fatal("wrong secret")
	}
}

// contract import used by statement test in same package file set
var _ = contract.EmptyDigest

func TestEd25519(t *testing.T) {
	pub, priv, err := evidence.GenerateEd25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	env, err := evidence.SignDSSEEd25519("application/json", []byte("hi"), priv, "", contract.ArtifactDigests{})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := evidence.VerifyDSSE(env, nil, pub)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
}
