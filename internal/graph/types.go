// Package graph defines the temporal architecture graph model.
//
// Four conceptual states:
//   Intended  — ADR / SLO / policy constraints
//   Desired   — Terraform / Helm / manifests
//   Deployed  — live Kubernetes / cloud resources
//   Observed  — metrics, incidents, cost
package graph

import "time"

// Snapshot is a frozen architecture graph at a point in time.
type Snapshot struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Source    string            `json:"source,omitempty"` // e.g. k8s-snapshot, terraform-plan
	Phase     Phase             `json:"phase"`
	CreatedAt time.Time         `json:"createdAt"`
	Labels    map[string]string `json:"labels,omitempty"`
	Nodes     []Node            `json:"nodes"`
	Edges     []Edge            `json:"edges"`
	// Meta holds scenario-specific capacity / inventory facts.
	Meta map[string]any `json:"meta,omitempty"`
}

// Phase situates the snapshot in the change lifecycle.
type Phase string

const (
	PhaseBaseline Phase = "baseline" // deployed + observed before change
	PhaseProposed Phase = "proposed" // desired after change (plan/diff applied)
	PhaseDeployed Phase = "deployed" // after apply
	PhaseObserved Phase = "observed" // post-deploy verification evidence
)

// Kind enumerates first-class architecture nodes (v0.1 subset).
type Kind string

const (
	KindCluster   Kind = "Cluster"
	KindNode      Kind = "Node"
	KindNamespace Kind = "Namespace"
	KindWorkload  Kind = "Workload"
	KindService   Kind = "Service"
	KindPVC       Kind = "PVC"
	KindVolume    Kind = "Volume"
	KindAlert     Kind = "Alert"
	KindSLO       Kind = "SLO"
	KindTeam      Kind = "Team"
	KindChange    Kind = "Change"
)

// Relation enumerates edge types.
type Relation string

const (
	RelDependsOn    Relation = "DEPENDS_ON"
	RelRunsOn       Relation = "RUNS_ON"
	RelProtectedBy  Relation = "PROTECTED_BY"
	RelObservedBy   Relation = "OBSERVED_BY"
	RelOwnedBy      Relation = "OWNED_BY"
	RelDeployedWith Relation = "DEPLOYED_WITH"
	RelBindsVolume  Relation = "BINDS_VOLUME"
	RelSchedulesOn  Relation = "SCHEDULES_ON"
)

// Node is a graph vertex.
type Node struct {
	ID         string         `json:"id"`
	Kind       Kind           `json:"kind"`
	Name       string         `json:"name"`
	Namespace  string         `json:"namespace,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Edge is a directed relation between nodes.
type Edge struct {
	ID     string         `json:"id,omitempty"`
	From   string         `json:"from"`
	To     string         `json:"to"`
	Rel    Relation       `json:"rel"`
	Attrs  map[string]any `json:"attributes,omitempty"`
}

// Index provides O(1) lookups over a Snapshot.
type Index struct {
	Snap  *Snapshot
	ByID  map[string]*Node
	Out   map[string][]Edge // from -> edges
	In    map[string][]Edge // to -> edges
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
