package graph

import "fmt"

// MergeSnapshots combines multi-cluster baselines into one graph.
// Node IDs are prefixed with cluster/ if not already namespaced that way.
// This is the v2 multi-repository / multi-cluster foundation.
func MergeSnapshots(clusterName string, snaps ...*Snapshot) *Snapshot {
	out := &Snapshot{
		APIVersion: APIVersionV1Alpha1,
		Kind:       DocKindSnapshot,
		ID:         "multi-" + clusterName,
		Name:       "multi-cluster:" + clusterName,
		Source:     "multi-cluster-merge",
		Phase:      PhaseBaseline,
		Labels:     map[string]string{"multiCluster": "true"},
		Meta:       map[string]any{"clusters": []any{}},
	}
	clusters := []any{}
	for _, s := range snaps {
		if s == nil {
			continue
		}
		clusters = append(clusters, s.ID)
		prefix := s.ID + "::"
		idMap := map[string]string{}
		for _, n := range s.Nodes {
			nn := CloneNode(n)
			if s.Labels != nil {
				if nn.Attributes == nil {
					nn.Attributes = map[string]any{}
				}
				nn.Attributes["cluster"] = s.Labels["cluster"]
				if nn.Attributes["cluster"] == "" {
					nn.Attributes["cluster"] = s.ID
				}
			}
			newID := prefix + n.ID
			idMap[n.ID] = newID
			nn.ID = newID
			out.Nodes = append(out.Nodes, nn)
		}
		for _, e := range s.Edges {
			ee := CloneEdge(e)
			ee.From = idMap[e.From]
			ee.To = idMap[e.To]
			if ee.From == "" || ee.To == "" {
				continue
			}
			out.Edges = append(out.Edges, ee)
		}
		// merge capacity facts with prefix
		if s.Meta != nil {
			if out.Meta == nil {
				out.Meta = map[string]any{}
			}
			for k, v := range s.Meta {
				out.Meta[fmt.Sprintf("%s.%s", s.ID, k)] = v
			}
		}
	}
	out.Meta["clusters"] = clusters
	return out
}
