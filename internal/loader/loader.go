// Package loader loads architecture snapshots and change envelopes from disk.
package loader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// ChangeEnvelope describes a proposed infrastructure change (Terraform plan,
// Helm diff, manifest patch, etc.) in a tool-agnostic form for v0.1.
type ChangeEnvelope struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Kind        string         `json:"kind"` // terraform-plan | helm-diff | k8s-manifest | prometheus-rule
	Description string         `json:"description,omitempty"`
	// Seeds are node IDs primarily affected by the change.
	Seeds []string `json:"seeds,omitempty"`
	// Facts are scenario inputs (capacity deltas, rule selectors, etc.).
	Facts map[string]any `json:"facts,omitempty"`
	// Added / Removed / Updated node IDs for simple plan application.
	AddedNodes   []graph.Node `json:"addedNodes,omitempty"`
	RemovedNodes []string     `json:"removedNodes,omitempty"`
	// PatchNodes merges attributes into existing nodes by ID.
	PatchNodes []graph.Node `json:"patchNodes,omitempty"`
	AddedEdges []graph.Edge `json:"addedEdges,omitempty"`
}

// LoadSnapshot reads a Snapshot JSON file.
func LoadSnapshot(path string) (*graph.Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s graph.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse snapshot %s: %w", path, err)
	}
	if s.ID == "" {
		s.ID = filepath.Base(path)
	}
	if s.Phase == "" {
		s.Phase = graph.PhaseBaseline
	}
	return &s, nil
}

// LoadChange reads a ChangeEnvelope JSON file.
func LoadChange(path string) (*ChangeEnvelope, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c ChangeEnvelope
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse change %s: %w", path, err)
	}
	if c.ID == "" {
		c.ID = filepath.Base(path)
	}
	return &c, nil
}

// ApplyChange builds a proposed snapshot from baseline + change envelope.
// Deterministic: no live API calls.
func ApplyChange(base *graph.Snapshot, ch *ChangeEnvelope) *graph.Snapshot {
	out := &graph.Snapshot{
		ID:        base.ID + "+" + ch.ID,
		Name:      base.Name + " + " + ch.Title,
		Source:    "proposed:" + ch.Kind,
		Phase:     graph.PhaseProposed,
		CreatedAt: base.CreatedAt,
		Labels:    map[string]string{},
		Meta:      map[string]any{},
	}
	for k, v := range base.Labels {
		out.Labels[k] = v
	}
	out.Labels["change"] = ch.ID
	for k, v := range base.Meta {
		out.Meta[k] = v
	}
	// Overlay change facts into meta.
	if ch.Facts != nil {
		for k, v := range ch.Facts {
			out.Meta[k] = v
		}
	}

	removed := map[string]bool{}
	for _, id := range ch.RemovedNodes {
		removed[id] = true
	}
	patches := map[string]graph.Node{}
	for _, p := range ch.PatchNodes {
		patches[p.ID] = p
	}

	for _, n := range base.Nodes {
		if removed[n.ID] {
			continue
		}
		if p, ok := patches[n.ID]; ok {
			merged := n
			if p.Name != "" {
				merged.Name = p.Name
			}
			if p.Kind != "" {
				merged.Kind = p.Kind
			}
			if p.Namespace != "" {
				merged.Namespace = p.Namespace
			}
			if merged.Attributes == nil {
				merged.Attributes = map[string]any{}
			}
			for k, v := range p.Attributes {
				merged.Attributes[k] = v
			}
			out.Nodes = append(out.Nodes, merged)
			continue
		}
		out.Nodes = append(out.Nodes, n)
	}
	out.Nodes = append(out.Nodes, ch.AddedNodes...)

	for _, e := range base.Edges {
		if removed[e.From] || removed[e.To] {
			continue
		}
		out.Edges = append(out.Edges, e)
	}
	out.Edges = append(out.Edges, ch.AddedEdges...)
	return out
}

// FactInt reads an int from change facts or snapshot meta.
func FactInt(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return def
	}
}

// FactString reads a string fact.
func FactString(m map[string]any, key, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}
