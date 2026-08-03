package evidence_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
)

func TestHMACSignVerify(t *testing.T) {
	rep := &analyze.Report{
		ChangeID: "c", BaselineID: "b", Decision: analyze.DecisionBlock,
		Risk: analyze.RiskHigh, Version: "0.7.0", SemanticDigest: "deadbeef",
	}
	secret := []byte("test-secret-key")
	env, err := evidence.SignReportHMAC(rep, secret, "k1")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := evidence.VerifyHMAC(env, secret)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ok, _ = evidence.VerifyHMAC(env, []byte("wrong"))
	if ok {
		t.Fatal("wrong secret must fail")
	}
}
