package evidence_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
)

func TestEvidenceStatementSignsChainDigests(t *testing.T) {
	sec := []byte("test-secret")
	stmt := evidence.EvidenceStatement{
		ChangeID: "c1",
		Decision: "block",
		Risk:     "critical",
		KeyID:    "k1",
		ChainDigests: contract.ArtifactDigests{
			BaselineDigest: "aa",
			ChangeDigest:   "bb",
			ReportDigest:   "cc",
		},
		ReportSemanticDigest: "sem",
	}
	env, err := evidence.SignEvidenceStatement(stmt, sec)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := evidence.VerifyDSSE(env, sec, nil)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	got, err := evidence.ParseStatement(env)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChainDigests.BaselineDigest != "aa" || got.KeyID != "k1" {
		t.Fatalf("%+v", got)
	}
	// Mutating envelope outer fields is irrelevant — digests live inside payload.
	// Mutating payload breaks signature.
	env.PayloadB64 = env.PayloadB64 + "x"
	ok, _ = evidence.VerifyDSSE(env, sec, nil)
	if ok {
		t.Fatal("tampered payload must fail")
	}
}
