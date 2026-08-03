package contract_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/contract"
)

func TestDigestStable(t *testing.T) {
	a := map[string]any{"id": "x", "n": 1}
	d1, err := contract.ComputeDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := contract.ComputeDigest(a)
	if err != nil || !d1.Equal(d2) {
		t.Fatalf("%s vs %s", d1, d2)
	}
}

func TestValidateDocument(t *testing.T) {
	ok := []byte(`{"apiVersion":"rehearsal.io/v1beta1","kind":"ArchitectureSnapshot","id":"a","phase":"baseline","nodes":[]}`)
	if err := contract.ValidateDocument(ok); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"apiVersion":"rehearsal.io/v9","kind":"ArchitectureSnapshot","id":"a","phase":"baseline","nodes":[]}`)
	if err := contract.ValidateDocument(bad); err == nil {
		t.Fatal("expected unsupported version")
	}
}

func TestMigrate(t *testing.T) {
	to, err := contract.MigrateVersion(contract.APIVersionV1Alpha1, contract.APIVersionV1)
	if err != nil || to != contract.APIVersionV1 {
		t.Fatalf("%s %v", to, err)
	}
}
