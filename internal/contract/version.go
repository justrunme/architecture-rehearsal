// Package contract defines versioned API types, digests, and schema validation (v0.8+).
package contract

// API versions.
const (
	APIVersionV1Alpha1 = "rehearsal.io/v1alpha1"
	APIVersionV1Beta1  = "rehearsal.io/v1beta1"
	APIVersionV1       = "rehearsal.io/v1"
)

// Document kinds.
const (
	KindSnapshot     = "ArchitectureSnapshot"
	KindChange       = "ChangeEnvelope"
	KindReport       = "ImpactReport"
	KindVerification = "VerificationResult"
	KindEvidence     = "EvidenceEnvelope"
	KindRehearsalRun = "RehearsalRun"
	KindChain        = "EvidenceChain"
	KindCalibration  = "CalibrationReport"
)

// SupportedVersions for accept/migrate.
var SupportedVersions = []string{APIVersionV1Alpha1, APIVersionV1Beta1, APIVersionV1}

// NormalizeVersion maps empty/legacy to v1alpha1; upgrades alpha→beta when requested.
func NormalizeVersion(v string) string {
	if v == "" {
		return APIVersionV1Alpha1
	}
	return v
}

// CanAccept returns true if version is known.
func CanAccept(v string) bool {
	v = NormalizeVersion(v)
	for _, s := range SupportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// MigrateVersion upgrades document apiVersion (identity transform for now).
// Future: field renames between alpha/beta/v1.
func MigrateVersion(from, to string) (string, error) {
	from = NormalizeVersion(from)
	to = NormalizeVersion(to)
	if !CanAccept(from) || !CanAccept(to) {
		return "", ErrUnsupportedVersion
	}
	// v1alpha1 → v1beta1 → v1 are currently wire-compatible.
	return to, nil
}

// ErrUnsupportedVersion is returned for unknown apiVersion.
var ErrUnsupportedVersion = errString("unsupported apiVersion")

type errString string

func (e errString) Error() string { return string(e) }
