package graph

// WalkForward returns all node IDs reachable from seeds following Out edges
// (optionally filtered by allowed relations). Depth-limited BFS.
func WalkForward(idx *Index, seeds []string, allowed map[Relation]bool, maxDepth int) []string {
	if maxDepth <= 0 {
		maxDepth = 8
	}
	seen := map[string]bool{}
	var order []string
	type item struct {
		id    string
		depth int
	}
	q := make([]item, 0, len(seeds))
	for _, s := range seeds {
		if s == "" {
			continue
		}
		q = append(q, item{id: s, depth: 0})
		seen[s] = true
	}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		order = append(order, cur.id)
		if cur.depth >= maxDepth {
			continue
		}
		for _, e := range idx.Out[cur.id] {
			if allowed != nil && !allowed[e.Rel] {
				continue
			}
			if seen[e.To] {
				continue
			}
			seen[e.To] = true
			q = append(q, item{id: e.To, depth: cur.depth + 1})
		}
	}
	return order
}

// WalkReverse returns ancestors (nodes that depend on seeds) via In edges.
func WalkReverse(idx *Index, seeds []string, allowed map[Relation]bool, maxDepth int) []string {
	if maxDepth <= 0 {
		maxDepth = 8
	}
	seen := map[string]bool{}
	var order []string
	type item struct {
		id    string
		depth int
	}
	q := make([]item, 0, len(seeds))
	for _, s := range seeds {
		if s == "" {
			continue
		}
		q = append(q, item{id: s, depth: 0})
		seen[s] = true
	}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		order = append(order, cur.id)
		if cur.depth >= maxDepth {
			continue
		}
		for _, e := range idx.In[cur.id] {
			if allowed != nil && !allowed[e.Rel] {
				continue
			}
			if seen[e.From] {
				continue
			}
			seen[e.From] = true
			q = append(q, item{id: e.From, depth: cur.depth + 1})
		}
	}
	return order
}

// DependentsOf returns workloads/services that DEPENDS_ON or transitively depend on seed.
func DependentsOf(idx *Index, seed string) []string {
	allowed := map[Relation]bool{
		RelDependsOn:   true,
		RelObservedBy:  true,
		RelProtectedBy: true,
	}
	// Reverse DEPENDS_ON: who points at me?
	ids := WalkReverse(idx, []string{seed}, allowed, 6)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == seed {
			continue
		}
		out = append(out, id)
	}
	return out
}
