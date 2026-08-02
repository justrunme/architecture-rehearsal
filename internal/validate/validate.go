// Package validate enforces fail-closed graph and change contracts.
package validate

import (
	"fmt"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// Error is a validation failure (input unusable — never approve).
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Snapshot validates a graph snapshot.
func Snapshot(s *graph.Snapshot) error {
	if s == nil {
		return &Error{Code: "snapshot.nil", Message: "snapshot is nil"}
	}
	if strings.TrimSpace(s.ID) == "" {
		return &Error{Code: "snapshot.id", Message: "id is required"}
	}
	switch s.Phase {
	case "", graph.PhaseBaseline, graph.PhaseProposed, graph.PhaseDeployed, graph.PhaseObserved:
	default:
		return &Error{Code: "snapshot.phase", Message: fmt.Sprintf("invalid phase %q", s.Phase)}
	}
	seen := map[string]bool{}
	for _, n := range s.Nodes {
		if n.ID == "" {
			return &Error{Code: "node.id", Message: "node with empty id"}
		}
		if seen[n.ID] {
			return &Error{Code: "node.duplicate", Message: "duplicate node id: " + n.ID}
		}
		seen[n.ID] = true
		if n.Kind == "" {
			return &Error{Code: "node.kind", Message: "node " + n.ID + " missing kind"}
		}
		if !graph.KnownKinds[n.Kind] {
			return &Error{Code: "node.kind.unknown", Message: fmt.Sprintf("node %s unknown kind %q", n.ID, n.Kind)}
		}
	}
	edgeKey := map[string]bool{}
	for _, e := range s.Edges {
		if e.From == "" || e.To == "" {
			return &Error{Code: "edge.endpoints", Message: "edge missing from/to"}
		}
		if !seen[e.From] {
			return &Error{Code: "edge.dangling", Message: "edge from unknown node: " + e.From}
		}
		if !seen[e.To] {
			return &Error{Code: "edge.dangling", Message: "edge to unknown node: " + e.To}
		}
		if !graph.KnownRelations[e.Rel] {
			return &Error{Code: "edge.rel.unknown", Message: fmt.Sprintf("unknown relation %q", e.Rel)}
		}
		k := e.From + "|" + string(e.Rel) + "|" + e.To
		if edgeKey[k] {
			return &Error{Code: "edge.duplicate", Message: "duplicate edge: " + k}
		}
		edgeKey[k] = true
	}
	return nil
}

// Change validates a change envelope against optional baseline.
// Pass base=nil to skip seed/patch existence checks (loaded change alone).
func Change(ch changeLike) error {
	if ch == nil {
		return &Error{Code: "change.nil", Message: "change is nil"}
	}
	if strings.TrimSpace(ch.GetID()) == "" {
		return &Error{Code: "change.id", Message: "id is required"}
	}
	if strings.TrimSpace(ch.GetTitle()) == "" {
		return &Error{Code: "change.title", Message: "title is required"}
	}
	return nil
}

// ChangeAgainstBaseline validates change relative to a baseline graph.
func ChangeAgainstBaseline(base *graph.Snapshot, ch changeLike) error {
	if err := Change(ch); err != nil {
		return err
	}
	if base == nil {
		return nil
	}
	if err := Snapshot(base); err != nil {
		return err
	}
	idx := map[string]graph.Node{}
	for _, n := range base.Nodes {
		idx[n.ID] = n
	}
	for _, id := range ch.GetRemovedNodes() {
		if _, ok := idx[id]; !ok {
			return &Error{Code: "change.remove.missing", Message: "removed node not in baseline: " + id}
		}
	}
	for _, p := range ch.GetPatchNodes() {
		bn, ok := idx[p.ID]
		if !ok {
			return &Error{Code: "change.patch.missing", Message: "patch node not in baseline: " + p.ID}
		}
		if p.Kind != "" && p.Kind != bn.Kind {
			return &Error{Code: "change.patch.kind", Message: "patch cannot change node kind for " + p.ID}
		}
	}
	for _, seed := range ch.GetSeeds() {
		if seed == "" {
			continue
		}
		if _, ok := idx[seed]; !ok {
			// seed may be newly added
			found := false
			for _, a := range ch.GetAddedNodes() {
				if a.ID == seed {
					found = true
					break
				}
			}
			if !found {
				return &Error{Code: "change.seed.missing", Message: "seed not in baseline or addedNodes: " + seed}
			}
		}
	}
	for _, e := range ch.GetAddedEdges() {
		if !graph.KnownRelations[e.Rel] {
			return &Error{Code: "change.edge.rel", Message: fmt.Sprintf("unknown relation %q", e.Rel)}
		}
	}
	return nil
}

// changeLike abstracts ChangeEnvelope without import cycle.
type changeLike interface {
	GetID() string
	GetTitle() string
	GetSeeds() []string
	GetRemovedNodes() []string
	GetPatchNodes() []graph.Node
	GetAddedNodes() []graph.Node
	GetAddedEdges() []graph.Edge
}
