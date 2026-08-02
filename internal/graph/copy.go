package graph

import "encoding/json"

// CloneMap returns a deep-ish copy of a map[string]any via JSON round-trip
// (values are JSON-serializable in our fixtures and collectors).
func CloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		// fallback shallow
		out := make(map[string]any, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		out = make(map[string]any, len(in))
		for k, v := range in {
			out[k] = v
		}
	}
	return out
}

// CloneNode deep-copies a node (including attributes).
func CloneNode(n Node) Node {
	out := n
	out.Attributes = CloneMap(n.Attributes)
	return out
}

// CloneEdge deep-copies an edge.
func CloneEdge(e Edge) Edge {
	out := e
	out.Attrs = CloneMap(e.Attrs)
	return out
}

// CloneSnapshot deep-copies a full snapshot (nodes, edges, labels, meta).
func CloneSnapshot(s *Snapshot) *Snapshot {
	if s == nil {
		return nil
	}
	out := &Snapshot{
		ID:        s.ID,
		Name:      s.Name,
		Source:    s.Source,
		Phase:     s.Phase,
		CreatedAt: s.CreatedAt,
		Labels:    map[string]string{},
		Meta:      CloneMap(s.Meta),
		APIVersion: s.APIVersion,
		Kind:       s.Kind,
	}
	for k, v := range s.Labels {
		out.Labels[k] = v
	}
	out.Nodes = make([]Node, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		out.Nodes = append(out.Nodes, CloneNode(n))
	}
	out.Edges = make([]Edge, 0, len(s.Edges))
	for _, e := range s.Edges {
		out.Edges = append(out.Edges, CloneEdge(e))
	}
	if s.Cluster != nil {
		c := *s.Cluster
		out.Cluster = &c
	}
	out.Warnings = append([]string{}, s.Warnings...)
	return out
}
