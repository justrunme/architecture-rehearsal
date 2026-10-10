// Package graph defines the architecture graph model.
//
// States: Intended · Desired · Deployed · Observed
// Phases: baseline · proposed · deployed · observed
package graph

import "time"

// API versions for versioned contracts.
const (
	APIVersionV1Alpha1 = "rehearsal.io/v1alpha1"
	DocKindSnapshot    = "ArchitectureSnapshot"
	DocKindChange      = "ChangeEnvelope"
	DocKindReport      = "ImpactReport"
	DocKindEvidence    = "EvidenceManifest"
)

// Snapshot is a frozen architecture graph at a point in time.
type Snapshot struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`

	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Source    string            `json:"source,omitempty"`
	Phase     Phase             `json:"phase"`
	CreatedAt time.Time         `json:"createdAt"`
	Labels    map[string]string `json:"labels,omitempty"`
	Nodes     []Node            `json:"nodes"`
	Edges     []Edge            `json:"edges"`
	Meta      map[string]any    `json:"meta,omitempty"`

	// Collection provenance (v0.3+).
	Cluster  *ClusterInfo `json:"cluster,omitempty"`
	Warnings []string     `json:"warnings,omitempty"`
}

// ClusterInfo describes collection context.
type ClusterInfo struct {
	Name            string    `json:"name,omitempty"`
	UID             string    `json:"uid,omitempty"`
	ServerVersion   string    `json:"serverVersion,omitempty"`
	CollectedFrom   time.Time `json:"collectedFrom,omitempty"`
	CollectedUntil  time.Time `json:"collectedUntil,omitempty"`
	CollectorVersion string   `json:"collectorVersion,omitempty"`
}

// Phase situates the snapshot in the change lifecycle.
type Phase string

const (
	PhaseBaseline Phase = "baseline"
	PhaseProposed Phase = "proposed"
	PhaseDeployed Phase = "deployed"
	PhaseObserved Phase = "observed"
)

// Kind enumerates architecture nodes.
type Kind string

const (
	KindCluster        Kind = "Cluster"
	KindNode           Kind = "Node"
	KindNamespace      Kind = "Namespace"
	KindWorkload       Kind = "Workload"
	KindPod            Kind = "Pod"
	KindService        Kind = "Service"
	KindIngress        Kind = "Ingress"
	KindPVC            Kind = "PVC"
	KindPV             Kind = "PV"
	KindPDB            Kind = "PDB"
	KindHPA            Kind = "HPA"
	KindServiceAccount Kind = "ServiceAccount"
	KindAlert          Kind = "Alert"
	KindSLO            Kind = "SLO"
	KindTeam           Kind = "Team"
	KindChange         Kind = "Change"
	KindIAMRole        Kind = "IAMRole"
)

// KnownKinds for validation.
var KnownKinds = map[Kind]bool{
	KindCluster: true, KindNode: true, KindNamespace: true, KindWorkload: true,
	KindPod: true, KindService: true, KindIngress: true, KindPVC: true, KindPV: true,
	KindPDB: true, KindHPA: true, KindServiceAccount: true, KindAlert: true,
	KindSLO: true, KindTeam: true, KindChange: true, KindIAMRole: true,
}

// Relation enumerates edge types.
type Relation string

const (
	RelDependsOn     Relation = "DEPENDS_ON"
	RelRunsOn        Relation = "RUNS_ON"
	RelProtectedBy   Relation = "PROTECTED_BY"
	RelObservedBy    Relation = "OBSERVED_BY"
	RelOwnedBy       Relation = "OWNED_BY"
	RelDeployedWith  Relation = "DEPLOYED_WITH"
	RelBindsVolume   Relation = "BINDS_VOLUME"
	RelSchedulesOn   Relation = "SCHEDULES_ON"
	RelRoutesTo      Relation = "ROUTES_TO"
	RelScales        Relation = "SCALES"
	RelUsesIdentity  Relation = "USES_IDENTITY"
	RelOwns          Relation = "OWNS"
)

// KnownRelations for validation.
var KnownRelations = map[Relation]bool{
	RelDependsOn: true, RelRunsOn: true, RelProtectedBy: true, RelObservedBy: true,
	RelOwnedBy: true, RelDeployedWith: true, RelBindsVolume: true, RelSchedulesOn: true,
	RelRoutesTo: true, RelScales: true, RelUsesIdentity: true, RelOwns: true,
}

// Node is a graph vertex.
type Node struct {
	ID         string         `json:"id"`
	Kind       Kind           `json:"kind"`
	Name       string         `json:"name"`
	Namespace  string         `json:"namespace,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	// Provenance (optional).
	Source    string `json:"source,omitempty"`
	SourceRef string `json:"sourceRef,omitempty"`
}

// Edge is a directed relation between nodes.
type Edge struct {
	ID    string         `json:"id,omitempty"`
	From  string         `json:"from"`
	To    string         `json:"to"`
	Rel   Relation       `json:"rel"`
	Attrs map[string]any `json:"attributes,omitempty"`
}

// Index provides O(1) lookups over a Snapshot.
type Index struct {
	Snap   *Snapshot
	ByID   map[string]*Node
	Out    map[string][]Edge
	In     map[string][]Edge
	ByKind map[Kind][]*Node
}

// BuildIndex indexes a snapshot for traversal.
func BuildIndex(s *Snapshot) *Index {
	idx := &Index{
		Snap:   s,
		ByID:   make(map[string]*Node, len(s.Nodes)),
		Out:    make(map[string][]Edge),
		In:     make(map[string][]Edge),
		ByKind: make(map[Kind][]*Node),
	}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		idx.ByID[n.ID] = n
		idx.ByKind[n.Kind] = append(idx.ByKind[n.Kind], n)
	}
	for _, e := range s.Edges {
		idx.Out[e.From] = append(idx.Out[e.From], e)
		idx.In[e.To] = append(idx.In[e.To], e)
	}
	return idx
}

// AttrString reads a string attribute.
func (n *Node) AttrString(key string) string {
	if n == nil || n.Attributes == nil {
		return ""
	}
	v, _ := n.Attributes[key].(string)
	return v
}

// AttrBool reads a bool attribute.
func (n *Node) AttrBool(key string) bool {
	if n == nil || n.Attributes == nil {
		return false
	}
	v, _ := n.Attributes[key].(bool)
	return v
}

// AttrInt reads a numeric attribute as int.
func (n *Node) AttrInt(key string) int {
	if n == nil || n.Attributes == nil {
		return 0
	}
	switch v := n.Attributes[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

// WorkloadReplicas returns replica count for a workload node.
func (n *Node) WorkloadReplicas() int {
	if n == nil {
		return 0
	}
	if r := n.AttrInt("replicas"); r > 0 {
		return r
	}
	if r := n.AttrInt("desiredReplicas"); r > 0 {
		return r
	}
	return 0
}
