package scenario

import "github.com/justrunme/architecture-rehearsal/internal/graph"

// Metadata describes a scenario package (v0.14 SDK / v1.0).
type Metadata struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Domain      string   `json:"domain"` // kubernetes, storage, networking, observability, gitops, …
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
}

// VerificationInput is prediction + observation for scenario-specific verify.
type VerificationInput struct {
	Prediction Finding
	Baseline   *graph.Snapshot
	Observed   *graph.Snapshot
	Components []string
}

// VerificationOutcome is scenario-local verify result.
type VerificationOutcome struct {
	Passed  bool
	Unknown bool
	Detail  string
	// Evidence is causal structured evidence.
	Evidence map[string]string
}

// ScenarioSDK is the full scenario contract (prediction + verification).
// Built-in runners implement Runner; packages may implement ScenarioSDK.
type ScenarioSDK interface {
	Runner
	Metadata() Metadata
	// VerifyObservation checks post-deploy evidence for this scenario's prediction.
	VerifyObservation(in VerificationInput) VerificationOutcome
}

// PackageRegistry lists available scenario packages.
type PackageRegistry struct {
	Packages []Metadata
}

// BuiltinPackages returns metadata for built-in scenarios.
func BuiltinPackages() PackageRegistry {
	return PackageRegistry{Packages: []Metadata{
		{Name: "rwo-node-loss", Version: "1.0.0", Domain: "storage", Description: "RWO volume stuck after node loss", Tags: []string{"pvc", "stateful"}},
		{Name: "cni-ip-capacity", Version: "1.0.0", Domain: "networking", Description: "Pod IP / scheduling capacity exhaustion", Tags: []string{"cni", "scale"}},
		{Name: "prom-zero-match", Version: "1.0.0", Domain: "observability", Description: "Prometheus rule matches zero series", Tags: []string{"prometheus"}},
		{Name: "pdb-disruption", Version: "1.0.0", Domain: "kubernetes", Description: "PDB blocks or violates disruption", Tags: []string{"pdb"}},
		{Name: "service-routing", Version: "1.0.0", Domain: "networking", Description: "Service loses all backends", Tags: []string{"service", "endpoints"}},
		{Name: "volume-az", Version: "1.0.0", Domain: "storage", Description: "PVC zone has no remaining nodes", Tags: []string{"az", "pvc"}},
	}}
}
