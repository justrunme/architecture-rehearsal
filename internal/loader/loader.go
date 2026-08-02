// Package loader loads architecture snapshots and change envelopes from disk.
package loader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/validate"
)

// ChangeEnvelope describes a proposed infrastructure change.
// Kind is the change type (node-failure, helm-upgrade, prometheus-rule, …).
type ChangeEnvelope struct {
	APIVersion   string         `json:"apiVersion,omitempty"`
	Kind         string         `json:"kind"` // change type (v0.1+)
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Description  string         `json:"description,omitempty"`
	Seeds        []string       `json:"seeds,omitempty"`
	Facts        map[string]any `json:"facts,omitempty"`
	AddedNodes   []graph.Node   `json:"addedNodes,omitempty"`
	RemovedNodes []string       `json:"removedNodes,omitempty"`
	PatchNodes   []graph.Node   `json:"patchNodes,omitempty"`
	AddedEdges   []graph.Edge   `json:"addedEdges,omitempty"`
}

// EffectiveKind returns the change type.
func (c *ChangeEnvelope) EffectiveKind() string {
	if c == nil {
		return ""
	}
	return c.Kind
}

func (c *ChangeEnvelope) GetID() string              { return c.ID }
func (c *ChangeEnvelope) GetTitle() string           { return c.Title }
func (c *ChangeEnvelope) GetSeeds() []string         { return c.Seeds }
func (c *ChangeEnvelope) GetRemovedNodes() []string  { return c.RemovedNodes }
func (c *ChangeEnvelope) GetPatchNodes() []graph.Node { return c.PatchNodes }
func (c *ChangeEnvelope) GetAddedNodes() []graph.Node { return c.AddedNodes }
func (c *ChangeEnvelope) GetAddedEdges() []graph.Edge { return c.AddedEdges }

// LoadSnapshot reads a Snapshot JSON file and validates it.
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
	if s.APIVersion == "" {
		s.APIVersion = graph.APIVersionV1Alpha1
	}
	if s.Kind == "" {
		s.Kind = graph.DocKindSnapshot
	}
	if err := validate.Snapshot(&s); err != nil {
		return nil, fmt.Errorf("validate snapshot %s: %w", path, err)
	}
	return &s, nil
}

// LoadChange reads a ChangeEnvelope JSON file and validates it.
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
	if c.APIVersion == "" {
		c.APIVersion = graph.APIVersionV1Alpha1
	}
	if err := validate.Change(&c); err != nil {
		return nil, fmt.Errorf("validate change %s: %w", path, err)
	}
	// Full baseline-relative checks happen in analyze after both load.
	return &c, nil
}

// ApplyChange builds a proposed snapshot from baseline + change without mutating baseline.
func ApplyChange(base *graph.Snapshot, ch *ChangeEnvelope) *graph.Snapshot {
	// Work on deep copy of baseline first so base is never mutated.
	src := graph.CloneSnapshot(base)
	out := &graph.Snapshot{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       graph.DocKindSnapshot,
		ID:         src.ID + "+" + ch.ID,
		Name:       src.Name + " + " + ch.Title,
		Source:     "proposed:" + ch.EffectiveKind(),
		Phase:      graph.PhaseProposed,
		CreatedAt:  src.CreatedAt,
		Labels:     map[string]string{},
		Meta:       graph.CloneMap(src.Meta),
		Cluster:    src.Cluster,
		Warnings:   append([]string{}, src.Warnings...),
	}
	for k, v := range src.Labels {
		out.Labels[k] = v
	}
	out.Labels["change"] = ch.ID
	if ch.Facts != nil {
		if out.Meta == nil {
			out.Meta = map[string]any{}
		}
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

	for _, n := range src.Nodes {
		if removed[n.ID] {
			continue
		}
		if p, ok := patches[n.ID]; ok {
			merged := graph.CloneNode(n)
			if p.Name != "" {
				merged.Name = p.Name
			}
			// Kind identity must not change via patch (validated separately).
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
		out.Nodes = append(out.Nodes, graph.CloneNode(n))
	}
	for _, n := range ch.AddedNodes {
		out.Nodes = append(out.Nodes, graph.CloneNode(n))
	}

	for _, e := range src.Edges {
		if removed[e.From] || removed[e.To] {
			continue
		}
		out.Edges = append(out.Edges, graph.CloneEdge(e))
	}
	for _, e := range ch.AddedEdges {
		out.Edges = append(out.Edges, graph.CloneEdge(e))
	}

	// Stable ordering for deterministic digests.
	sort.SliceStable(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	sort.SliceStable(out.Edges, func(i, j int) bool {
		if out.Edges[i].From != out.Edges[j].From {
			return out.Edges[i].From < out.Edges[j].From
		}
		if out.Edges[i].To != out.Edges[j].To {
			return out.Edges[i].To < out.Edges[j].To
		}
		return string(out.Edges[i].Rel) < string(out.Edges[j].Rel)
	})
	return out
}

// FactInt reads an int from a map.
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

// FactBool reads a bool fact.
func FactBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}
