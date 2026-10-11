// Package argo adapts Argo CD Applications into Architecture Rehearsal change envelopes.
//
// It does not re-implement Argo's diff engine. Desired manifests come from
// `argocd app manifests --revision` (or an equivalent rendered directory);
// the live cluster snapshot is the baseline. The adapter explains scheduling,
// storage, network, PDB, and blast-radius consequences via the causal graph.
package argo

import "github.com/justrunme/architecture-rehearsal/internal/change"

// Source identifies how desired manifests were obtained.
const (
	SourceArgocdCLI = "argocd-cli"
	SourceDirectory = "directory"
	SourceRepoServer = "repo-server"
)

// CompileOptions controls Application → ChangeEnvelope compilation.
type CompileOptions struct {
	AppName      string
	AppNamespace string
	Revision     string
	DesiredDir   string
	ChangeID     string
	Title        string
	Scope        change.ManifestScope
	// Source is recorded in change facts (argocd-cli | directory | repo-server).
	Source string
}

// ApplicationRef is a minimal Argo CD Application identity.
type ApplicationRef struct {
	Name      string
	Namespace string
	Revision  string
	Project   string
}
