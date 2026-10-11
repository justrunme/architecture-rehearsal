package argo

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"gopkg.in/yaml.v3"
)

// Compile builds a ChangeEnvelope from desired rendered manifests vs a live/baseline snapshot.
// Workload diffs reuse change.FromManifestsDiff; PVC, PDB, and NetworkPolicy diffs are layered on.
func Compile(base *graph.Snapshot, opts CompileOptions) (*loader.ChangeEnvelope, error) {
	if base == nil {
		return nil, fmt.Errorf("argo: baseline snapshot required")
	}
	if opts.DesiredDir == "" {
		return nil, fmt.Errorf("argo: desired manifest directory required")
	}
	id := opts.ChangeID
	if id == "" {
		id = "argo-" + sanitizeID(opts.AppName)
		if id == "argo-" {
			id = "argo-sync"
		}
	}
	title := opts.Title
	if title == "" {
		if opts.AppName != "" {
			title = "Argo CD sync impact: " + opts.AppName
		} else {
			title = "Argo CD sync impact"
		}
	}
	scope := opts.Scope
	ch, err := change.FromManifestsDiff(base, opts.DesiredDir, id, title, scope)
	if err != nil {
		return nil, err
	}
	// Reclassify as argo-sync; keep capacity scenario applicable via kind + facts.
	ch.Kind = "argo-sync"
	if ch.Facts == nil {
		ch.Facts = map[string]any{}
	}
	ch.Facts["source"] = firstNonEmpty(opts.Source, SourceDirectory)
	ch.Facts["scenario"] = "argo-impact"
	if opts.AppName != "" {
		ch.Facts["argo.app"] = opts.AppName
	}
	if opts.AppNamespace != "" {
		ch.Facts["argo.appNamespace"] = opts.AppNamespace
	}
	if opts.Revision != "" {
		ch.Facts["argo.revision"] = opts.Revision
	}
	// Enrich workload patches with scheduling attributes from desired YAML.
	if err := enrichWorkloads(base, opts.DesiredDir, ch, scope); err != nil {
		return nil, err
	}
	if err := compileExtraResources(base, opts.DesiredDir, ch, scope); err != nil {
		return nil, err
	}
	return ch, nil
}

// FetchAndCompile obtains desired manifests then compiles the change envelope.
func FetchAndCompile(base *graph.Snapshot, fetcher ManifestFetcher, app ApplicationRef, workDir string, opts CompileOptions) (*loader.ChangeEnvelope, error) {
	if fetcher == nil {
		return nil, fmt.Errorf("argo: manifest fetcher required")
	}
	dest := filepath.Join(workDir, "desired")
	if err := fetcher.Fetch(app, dest); err != nil {
		return nil, err
	}
	opts.DesiredDir = dest
	if opts.AppName == "" {
		opts.AppName = app.Name
	}
	if opts.AppNamespace == "" {
		opts.AppNamespace = app.Namespace
	}
	if opts.Revision == "" {
		opts.Revision = app.Revision
	}
	if opts.Source == "" {
		opts.Source = SourceArgocdCLI
	}
	return Compile(base, opts)
}

func enrichWorkloads(base *graph.Snapshot, dir string, ch *loader.ChangeEnvelope, scope change.ManifestScope) error {
	baseWL := map[string]*graph.Node{}
	for i := range base.Nodes {
		n := &base.Nodes[i]
		if n.Kind != graph.KindWorkload {
			continue
		}
		key := normalizeNS(n.Namespace) + "/" + n.Name
		baseWL[key] = n
	}
	return walkYAML(dir, func(obj map[string]any) error {
		kind, _ := obj["kind"].(string)
		if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
			return nil
		}
		meta, _ := obj["metadata"].(map[string]any)
		if meta == nil {
			return nil
		}
		name, _ := meta["name"].(string)
		ns := normalizeNS(str(meta["namespace"]))
		tmp := graph.Node{Name: name, Namespace: ns, Kind: graph.KindWorkload, Attributes: map[string]any{}}
		if labels, ok := meta["labels"].(map[string]any); ok {
			tmp.Attributes["podLabels"] = labels
		}
		if scopeActive(scope) && !inScope(tmp, scope) {
			return nil
		}
		spec, _ := obj["spec"].(map[string]any)
		attrs := schedulingAttrs(spec)
		if len(attrs) == 0 {
			return nil
		}
		id := fmt.Sprintf("workload/%s/%s", ns, name)
		// Merge into existing patch/add or create patch when only scheduling changed.
		merged := false
		for i := range ch.PatchNodes {
			if ch.PatchNodes[i].ID == id {
				if ch.PatchNodes[i].Attributes == nil {
					ch.PatchNodes[i].Attributes = map[string]any{}
				}
				for k, v := range attrs {
					ch.PatchNodes[i].Attributes[k] = v
				}
				merged = true
				break
			}
		}
		for i := range ch.AddedNodes {
			if ch.AddedNodes[i].ID == id {
				if ch.AddedNodes[i].Attributes == nil {
					ch.AddedNodes[i].Attributes = map[string]any{}
				}
				for k, v := range attrs {
					ch.AddedNodes[i].Attributes[k] = v
				}
				merged = true
				break
			}
		}
		if merged {
			return nil
		}
		bn := baseWL[ns+"/"+name]
		if bn == nil {
			return nil
		}
		changed := false
		for k, v := range attrs {
			if fmt.Sprint(bn.Attributes[k]) != fmt.Sprint(v) {
				changed = true
				break
			}
		}
		if !changed {
			return nil
		}
		patch := graph.Node{ID: id, Attributes: attrs}
		ch.PatchNodes = append(ch.PatchNodes, patch)
		ch.Seeds = appendUnique(ch.Seeds, id)
		return nil
	})
}

func compileExtraResources(base *graph.Snapshot, dir string, ch *loader.ChangeEnvelope, scope change.ManifestScope) error {
	baseByID := map[string]graph.Node{}
	for _, n := range base.Nodes {
		baseByID[n.ID] = n
	}
	return walkYAML(dir, func(obj map[string]any) error {
		kind, _ := obj["kind"].(string)
		meta, _ := obj["metadata"].(map[string]any)
		if meta == nil {
			return nil
		}
		name, _ := meta["name"].(string)
		ns := normalizeNS(str(meta["namespace"]))
		switch kind {
		case "PersistentVolumeClaim":
			id := fmt.Sprintf("pvc/%s/%s", ns, name)
			attrs := map[string]any{}
			if spec, ok := obj["spec"].(map[string]any); ok {
				if sc, ok := spec["storageClassName"].(string); ok {
					attrs["storageClass"] = sc
				}
				if am, ok := spec["accessModes"].([]any); ok && len(am) > 0 {
					attrs["accessMode"] = fmt.Sprint(am[0])
				}
			}
			if labels, ok := meta["labels"].(map[string]any); ok {
				if z, ok := labels["topology.kubernetes.io/zone"].(string); ok {
					attrs["zone"] = z
				}
				if z, ok := labels["failure-domain.beta.kubernetes.io/zone"].(string); ok && attrs["zone"] == nil {
					attrs["zone"] = z
				}
			}
			upsertNode(ch, baseByID, graph.Node{
				ID: id, Kind: graph.KindPVC, Name: name, Namespace: ns, Attributes: attrs,
			})
		case "PodDisruptionBudget":
			id := fmt.Sprintf("pdb/%s/%s", ns, name)
			attrs := map[string]any{}
			if spec, ok := obj["spec"].(map[string]any); ok {
				if v, ok := spec["minAvailable"]; ok {
					attrs["minAvailableRaw"] = fmt.Sprint(v)
					switch t := v.(type) {
					case int:
						attrs["minAvailable"] = t
					case float64:
						attrs["minAvailable"] = int(t)
					case string:
						if strings.HasSuffix(t, "%") {
							var pct int
							fmt.Sscanf(strings.TrimSuffix(t, "%"), "%d", &pct)
							attrs["minAvailablePercent"] = pct
						} else {
							var n int
							fmt.Sscanf(t, "%d", &n)
							attrs["minAvailable"] = n
						}
					}
				}
				if v, ok := spec["maxUnavailable"]; ok {
					attrs["maxUnavailableRaw"] = fmt.Sprint(v)
					switch t := v.(type) {
					case int:
						attrs["maxUnavailable"] = t
					case float64:
						attrs["maxUnavailable"] = int(t)
					case string:
						if strings.HasSuffix(t, "%") {
							var pct int
							fmt.Sscanf(strings.TrimSuffix(t, "%"), "%d", &pct)
							attrs["maxUnavailablePercent"] = pct
						} else {
							var n int
							fmt.Sscanf(t, "%d", &n)
							attrs["maxUnavailable"] = n
						}
					}
				}
				if sel, ok := spec["selector"].(map[string]any); ok {
					if ml, ok := sel["matchLabels"].(map[string]any); ok {
						attrs["matchLabels"] = ml
					}
				}
			}
			upsertNode(ch, baseByID, graph.Node{
				ID: id, Kind: graph.KindPDB, Name: name, Namespace: ns, Attributes: attrs,
			})
		case "NetworkPolicy":
			id := fmt.Sprintf("netpol/%s/%s", ns, name)
			attrs := map[string]any{"policyTypes": []any{}}
			if spec, ok := obj["spec"].(map[string]any); ok {
				if pt, ok := spec["policyTypes"]; ok {
					attrs["policyTypes"] = pt
				}
				if podSel, ok := spec["podSelector"].(map[string]any); ok {
					if ml, ok := podSel["matchLabels"].(map[string]any); ok {
						attrs["podSelector"] = ml
					}
				}
				if ing, ok := spec["ingress"]; ok {
					attrs["ingress"] = ing
				}
				if eg, ok := spec["egress"]; ok {
					attrs["egress"] = eg
				}
				// Explicit deny-all markers for detectors.
				ingList, _ := spec["ingress"].([]any)
				egList, _ := spec["egress"].([]any)
				attrs["ingressEmpty"] = len(ingList) == 0 && hasPolicyType(spec, "Ingress")
				attrs["egressEmpty"] = len(egList) == 0 && hasPolicyType(spec, "Egress")
				if _, hasIng := spec["ingress"]; hasIng && len(ingList) == 0 {
					attrs["ingressEmpty"] = true
				}
				if _, hasEg := spec["egress"]; hasEg && len(egList) == 0 {
					attrs["egressEmpty"] = true
				}
			}
			_ = scope
			upsertNode(ch, baseByID, graph.Node{
				ID: id, Kind: graph.KindNetworkPolicy, Name: name, Namespace: ns, Attributes: attrs,
			})
		}
		return nil
	})
}

func upsertNode(ch *loader.ChangeEnvelope, baseByID map[string]graph.Node, n graph.Node) {
	if bn, ok := baseByID[n.ID]; ok {
		same := true
		for k, v := range n.Attributes {
			if fmt.Sprint(bn.Attributes[k]) != fmt.Sprint(v) {
				same = false
				break
			}
		}
		if same {
			return
		}
		ch.PatchNodes = append(ch.PatchNodes, graph.Node{ID: n.ID, Attributes: n.Attributes})
		ch.Seeds = appendUnique(ch.Seeds, n.ID)
		return
	}
	ch.AddedNodes = append(ch.AddedNodes, n)
	ch.Seeds = appendUnique(ch.Seeds, n.ID)
}

func schedulingAttrs(spec map[string]any) map[string]any {
	if spec == nil {
		return nil
	}
	attrs := map[string]any{}
	tpl, _ := spec["template"].(map[string]any)
	if tpl == nil {
		return attrs
	}
	tspec, _ := tpl["spec"].(map[string]any)
	if tspec == nil {
		return attrs
	}
	if aff, ok := tspec["affinity"]; ok {
		attrs["affinity"] = aff
		attrs["hasAffinity"] = true
	}
	if tsc, ok := tspec["topologySpreadConstraints"]; ok {
		attrs["topologySpreadConstraints"] = tsc
		attrs["hasTopologySpread"] = true
	}
	if taints := tspec["tolerations"]; taints != nil {
		attrs["tolerations"] = taints
	}
	if ns, ok := tspec["nodeSelector"].(map[string]any); ok && len(ns) > 0 {
		attrs["nodeSelector"] = ns
	}
	return attrs
}

func hasPolicyType(spec map[string]any, want string) bool {
	pt, _ := spec["policyTypes"].([]any)
	if len(pt) == 0 {
		// Default: if ingress key present → Ingress; egress key → Egress.
		if want == "Ingress" {
			_, ok := spec["ingress"]
			return ok
		}
		if want == "Egress" {
			_, ok := spec["egress"]
			return ok
		}
		return false
	}
	for _, p := range pt {
		if fmt.Sprint(p) == want {
			return true
		}
	}
	return false
}

func walkYAML(dir string, fn func(map[string]any) error) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, doc := range strings.Split(string(raw), "\n---") {
			doc = strings.TrimSpace(doc)
			if doc == "" {
				continue
			}
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				return fmt.Errorf("yaml parse %s: %w", path, err)
			}
			if obj == nil {
				continue
			}
			if k, _ := obj["kind"].(string); k == "List" {
				items, _ := obj["items"].([]any)
				for _, it := range items {
					if m, ok := it.(map[string]any); ok {
						if err := fn(m); err != nil {
							return err
						}
					}
				}
				continue
			}
			if err := fn(obj); err != nil {
				return err
			}
		}
		return nil
	})
}

func normalizeNS(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == '/' {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func appendUnique(in []string, v string) []string {
	for _, x := range in {
		if x == v {
			return in
		}
	}
	return append(in, v)
}

func scopeActive(s change.ManifestScope) bool {
	return len(s.Namespaces) > 0 || s.NamePrefix != "" || s.LabelKey != ""
}

func inScope(n graph.Node, s change.ManifestScope) bool {
	if !scopeActive(s) {
		return true
	}
	ns := normalizeNS(n.Namespace)
	if len(s.Namespaces) > 0 {
		ok := false
		for _, x := range s.Namespaces {
			if normalizeNS(x) == ns {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if s.NamePrefix != "" && !strings.HasPrefix(n.Name, s.NamePrefix) {
		return false
	}
	return true
}
