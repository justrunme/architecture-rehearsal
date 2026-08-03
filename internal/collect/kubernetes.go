// Package collect provides read-only snapshot collectors.
// Secret values are never collected.
package collect

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/validate"
	"gopkg.in/yaml.v3"
)

// K8sOptions controls offline YAML collection.
type K8sOptions struct {
	ClusterName string
	// StrictYAML if true, YAML parse errors fail the collect.
	StrictYAML bool
}

// K8sFromManifests builds a snapshot from a directory tree of Kubernetes YAML
// (including kubectl List dumps). Does not call a live API.
//
// Recommended dump:
//
//	kubectl get node,ns,deploy,sts,ds,po,svc,pvc,pv,pdb,hpa,sa,ing -A -o yaml > dump.yaml
func K8sFromManifests(ctx context.Context, dir string, opts K8sOptions) (*graph.Snapshot, error) {
	_ = ctx
	if opts.ClusterName == "" {
		opts.ClusterName = "cluster"
	}
	start := time.Now().UTC()
	snap := &graph.Snapshot{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       graph.DocKindSnapshot,
		ID:         "k8s-" + opts.ClusterName,
		Name:       opts.ClusterName,
		Source:     "kubernetes-manifests",
		Phase:      graph.PhaseBaseline,
		CreatedAt:  start,
		Labels:     map[string]string{"cluster": opts.ClusterName},
		Meta:       map[string]any{},
		Cluster: &graph.ClusterInfo{
			Name:             opts.ClusterName,
			CollectedFrom:    start,
			CollectorVersion: "0.3.0",
		},
	}
	snap.Nodes = append(snap.Nodes, graph.Node{
		ID: "cluster/" + opts.ClusterName, Kind: graph.KindCluster, Name: opts.ClusterName, Source: "kubernetes",
	})

	var parseErrors []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !isYAML(d.Name()) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, doc := range splitYAML(string(raw)) {
			doc = strings.TrimSpace(doc)
			if doc == "" {
				continue
			}
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				msg := fmt.Sprintf("%s: %v", path, err)
				parseErrors = append(parseErrors, msg)
				if opts.StrictYAML {
					return fmt.Errorf("yaml parse: %w", err)
				}
				snap.Warnings = append(snap.Warnings, "skip parse: "+msg)
				continue
			}
			if obj == nil {
				continue
			}
			if err := ingestObject(snap, obj, opts.ClusterName); err != nil {
				snap.Warnings = append(snap.Warnings, err.Error())
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if opts.StrictYAML && len(parseErrors) > 0 {
		return nil, fmt.Errorf("yaml errors: %s", strings.Join(parseErrors, "; "))
	}

	buildEdges(snap)
	end := time.Now().UTC()
	snap.Cluster.CollectedUntil = end
	snap.Warnings = append(snap.Warnings, "Secret values are never collected (references only)")

	// capacity hint from nodes
	alloc := 0
	for _, n := range snap.Nodes {
		if n.Kind == graph.KindNode {
			alloc += n.AttrInt("allocatablePods")
		}
	}
	if alloc > 0 {
		running := 0
		for _, n := range snap.Nodes {
			if n.Kind == graph.KindWorkload {
				running += n.WorkloadReplicas()
			}
		}
		avail := alloc - running
		if avail < 0 {
			avail = 0
		}
		snap.Meta["pod_ip_capacity_available"] = avail
		snap.Meta["node_allocatable_pods_total"] = alloc
	}

	if err := validate.Snapshot(snap); err != nil {
		return nil, fmt.Errorf("collected snapshot invalid: %w", err)
	}
	return snap, nil
}

// Back-compat wrapper.
func K8sFromManifestsLegacy(ctx context.Context, dir, cluster string) (*graph.Snapshot, error) {
	return K8sFromManifests(ctx, dir, K8sOptions{ClusterName: cluster})
}

func isYAML(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".yaml") || strings.HasSuffix(l, ".yml")
}

func splitYAML(s string) []string {
	return strings.Split(s, "\n---")
}

func ingestObject(snap *graph.Snapshot, obj map[string]any, cluster string) error {
	kind, _ := obj["kind"].(string)
	// Expand List / Table-less kubectl multi-doc is already split; List is special.
	if kind == "List" {
		items, _ := obj["items"].([]any)
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				if err := ingestObject(snap, m, cluster); err != nil {
					return err
				}
			}
		}
		return nil
	}
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		return nil
	}
	name, _ := meta["name"].(string)
	if name == "" {
		return nil
	}
	ns := normalizeNS(meta)
	// Never collect secret data
	if kind == "Secret" {
		refs, _ := snap.Meta["secretRefs"].([]any)
		snap.Meta["secretRefs"] = append(refs, ns+"/"+name)
		return nil
	}
	ensureNS(snap, ns)

	switch kind {
	case "Node":
		attrs := map[string]any{"schedulable": true}
		if s, ok := obj["status"].(map[string]any); ok {
			if cap, ok := s["allocatable"].(map[string]any); ok {
				if p, ok := cap["pods"]; ok {
					attrs["allocatablePods"] = asInt(p)
				}
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
		addNode(snap, graph.Node{
			ID: "node/" + name, Kind: graph.KindNode, Name: name, Attributes: attrs,
			Source: "kubernetes", SourceRef: "v1/Node/" + name,
		})
	case "Namespace":
		ensureNS(snap, name)
	case "Deployment", "StatefulSet", "DaemonSet":
		id := fmt.Sprintf("workload/%s/%s", ns, name)
		replicas := 1
		attrs := map[string]any{"type": kind}
		if kind == "DaemonSet" {
			// desiredNumberScheduled if present
			if st, ok := obj["status"].(map[string]any); ok {
				if v := asInt(st["desiredNumberScheduled"]); v > 0 {
					replicas = v
				}
			}
			attrs["daemonSet"] = true
		} else if spec, ok := obj["spec"].(map[string]any); ok {
			if v, ok := spec["replicas"]; ok {
				replicas = asInt(v)
			}
			if strat, ok := spec["strategy"].(map[string]any); ok {
				if rs, ok := strat["rollingUpdate"].(map[string]any); ok {
					if ms, ok := rs["maxSurge"]; ok {
						attrs["maxSurge"] = fmt.Sprint(ms)
					}
				}
			}
			if kind == "StatefulSet" {
				attrs["stateful"] = true
				if strat, ok := spec["updateStrategy"].(map[string]any); ok {
					if rs, ok := strat["rollingUpdate"].(map[string]any); ok {
						if p, ok := rs["partition"]; ok {
							attrs["partition"] = asInt(p)
						}
					}
				}
			}
			// selector
			if sel, ok := spec["selector"].(map[string]any); ok {
				if ml, ok := sel["matchLabels"].(map[string]any); ok {
					attrs["selector"] = stringifyMap(ml)
					attrs["matchLabels"] = ml
				}
			}
			// service account
			if tpl, ok := spec["template"].(map[string]any); ok {
				if tspec, ok := tpl["spec"].(map[string]any); ok {
					if sa, ok := tspec["serviceAccountName"].(string); ok && sa != "" {
						attrs["serviceAccountName"] = sa
					}
					// volumes -> claim names
					var claims []string
					if vols, ok := tspec["volumes"].([]any); ok {
						for _, v := range vols {
							vm, _ := v.(map[string]any)
							if vm == nil {
								continue
							}
							if pvc, ok := vm["persistentVolumeClaim"].(map[string]any); ok {
								if cn, ok := pvc["claimName"].(string); ok {
									claims = append(claims, cn)
								}
							}
						}
					}
					if len(claims) > 0 {
						attrs["volumeClaims"] = claims
					}
				}
				if tmeta, ok := tpl["metadata"].(map[string]any); ok {
					if labels, ok := tmeta["labels"].(map[string]any); ok {
						attrs["podLabels"] = labels
					}
				}
			}
		}
		attrs["replicas"] = replicas
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindWorkload, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: kind + "/" + ns + "/" + name,
		})
	case "Pod":
		id := fmt.Sprintf("pod/%s/%s", ns, name)
		attrs := map[string]any{}
		if labels, ok := meta["labels"].(map[string]any); ok {
			attrs["labels"] = labels
		}
		nodeName := ""
		phase := ""
		if spec, ok := obj["spec"].(map[string]any); ok {
			if sa, ok := spec["serviceAccountName"].(string); ok {
				attrs["serviceAccountName"] = sa
			}
			if nn, ok := spec["nodeName"].(string); ok {
				nodeName = nn
				attrs["nodeName"] = nn
			}
		}
		if st, ok := obj["status"].(map[string]any); ok {
			if p, ok := st["phase"].(string); ok {
				phase = p
				attrs["phase"] = p
			}
			if nn, ok := st["nominatedNodeName"].(string); ok && nodeName == "" {
				nodeName = nn
			}
		}
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindPod, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: "v1/Pod/" + ns + "/" + name,
		})
		if nodeName != "" {
			// edge later in buildEdges if node exists
			attrs["nodeName"] = nodeName
		}
		_ = phase
	case "Service":
		id := fmt.Sprintf("svc/%s/%s", ns, name)
		attrs := map[string]any{}
		if spec, ok := obj["spec"].(map[string]any); ok {
			if sel, ok := spec["selector"].(map[string]any); ok {
				attrs["selector"] = stringifyMap(sel)
				attrs["matchLabels"] = sel
			}
		}
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindService, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: "v1/Service/" + ns + "/" + name,
		})
	case "PersistentVolumeClaim":
		id := fmt.Sprintf("pvc/%s/%s", ns, name)
		attrs := map[string]any{}
		if spec, ok := obj["spec"].(map[string]any); ok {
			if modes, ok := spec["accessModes"].([]any); ok && len(modes) > 0 {
				attrs["accessMode"] = fmt.Sprint(modes[0])
			}
			if sc, ok := spec["storageClassName"].(string); ok {
				attrs["storageClass"] = sc
			}
			if vname, ok := spec["volumeName"].(string); ok {
				attrs["volumeName"] = vname
			}
		}
		if anns, ok := meta["annotations"].(map[string]any); ok {
			// never keep secrets; only volume.kubernetes.io/selected-node
			if bn, ok := anns["volume.kubernetes.io/selected-node"].(string); ok {
				attrs["boundNode"] = bn
			}
		}
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindPVC, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: "v1/PVC/" + ns + "/" + name,
		})
	case "PersistentVolume":
		id := "pv/" + name
		attrs := map[string]any{}
		if spec, ok := obj["spec"].(map[string]any); ok {
			if modes, ok := spec["accessModes"].([]any); ok && len(modes) > 0 {
				attrs["accessMode"] = fmt.Sprint(modes[0])
			}
			if nsel, ok := spec["nodeAffinity"].(map[string]any); ok {
				attrs["nodeAffinity"] = true
				_ = nsel
			}
			// topology from CSI / AWS EBS
			if csi, ok := spec["csi"].(map[string]any); ok {
				if va, ok := csi["volumeAttributes"].(map[string]any); ok {
					if z, ok := va["storage.kubernetes.io/csiProvisionerIdentity"]; ok {
						_ = z
					}
				}
			}
			if aws, ok := spec["awsElasticBlockStore"].(map[string]any); ok {
				_ = aws
			}
		}
		if labels, ok := meta["labels"].(map[string]any); ok {
			if z, ok := labels["topology.kubernetes.io/zone"].(string); ok {
				attrs["zone"] = z
			}
			if z, ok := labels["failure-domain.beta.kubernetes.io/zone"].(string); ok {
				attrs["zone"] = z
			}
		}
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindPV, Name: name, Attributes: attrs,
			Source: "kubernetes", SourceRef: "v1/PV/" + name,
		})
	case "PodDisruptionBudget":
		id := fmt.Sprintf("pdb/%s/%s", ns, name)
		attrs := map[string]any{}
		if spec, ok := obj["spec"].(map[string]any); ok {
			if v, ok := spec["minAvailable"]; ok {
				attrs["minAvailable"] = asInt(v)
				attrs["minAvailableRaw"] = fmt.Sprint(v)
			}
			if v, ok := spec["maxUnavailable"]; ok {
				attrs["maxUnavailable"] = asInt(v)
				attrs["maxUnavailableRaw"] = fmt.Sprint(v)
			}
			if sel, ok := spec["selector"].(map[string]any); ok {
				if ml, ok := sel["matchLabels"].(map[string]any); ok {
					attrs["matchLabels"] = ml
				}
			}
		}
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindPDB, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: "policy/PDB/" + ns + "/" + name,
		})
	case "HorizontalPodAutoscaler":
		id := fmt.Sprintf("hpa/%s/%s", ns, name)
		attrs := map[string]any{}
		if spec, ok := obj["spec"].(map[string]any); ok {
			if scale, ok := spec["scaleTargetRef"].(map[string]any); ok {
				attrs["targetKind"] = fmt.Sprint(scale["kind"])
				attrs["targetName"] = fmt.Sprint(scale["name"])
			}
		}
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindHPA, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: "autoscaling/HPA/" + ns + "/" + name,
		})
	case "ServiceAccount":
		id := fmt.Sprintf("sa/%s/%s", ns, name)
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindServiceAccount, Name: name, Namespace: ns,
			Source: "kubernetes", SourceRef: "v1/ServiceAccount/" + ns + "/" + name,
		})
	case "Ingress":
		id := fmt.Sprintf("ing/%s/%s", ns, name)
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindIngress, Name: name, Namespace: ns,
			Source: "kubernetes", SourceRef: "networking/Ingress/" + ns + "/" + name,
		})
	default:
		// ignore
	}
	_ = cluster
	return nil
}

func buildEdges(snap *graph.Snapshot) {
	idx := map[string]graph.Node{}
	for _, n := range snap.Nodes {
		idx[n.ID] = n
	}
	edgeExists := map[string]bool{}
	addEdge := func(from, to string, rel graph.Relation) {
		if from == "" || to == "" {
			return
		}
		if _, ok := idx[from]; !ok {
			return
		}
		if _, ok := idx[to]; !ok {
			return
		}
		k := from + "|" + string(rel) + "|" + to
		if edgeExists[k] {
			return
		}
		edgeExists[k] = true
		snap.Edges = append(snap.Edges, graph.Edge{From: from, To: to, Rel: rel})
	}

	// Workload → PVC, SA
	for _, n := range snap.Nodes {
		if n.Kind != graph.KindWorkload {
			continue
		}
		if claims, ok := n.Attributes["volumeClaims"].([]string); ok {
			for _, c := range claims {
				addEdge(n.ID, fmt.Sprintf("pvc/%s/%s", n.Namespace, c), graph.RelBindsVolume)
			}
		}
		// JSON round-trip may make []any
		if claims, ok := n.Attributes["volumeClaims"].([]any); ok {
			for _, c := range claims {
				addEdge(n.ID, fmt.Sprintf("pvc/%s/%s", n.Namespace, fmt.Sprint(c)), graph.RelBindsVolume)
			}
		}
		if sa := n.AttrString("serviceAccountName"); sa != "" {
			addEdge(n.ID, fmt.Sprintf("sa/%s/%s", n.Namespace, sa), graph.RelUsesIdentity)
		}
	}

	// Pod → Node, Pod labels for service matching
	for _, n := range snap.Nodes {
		if n.Kind != graph.KindPod {
			continue
		}
		if nn := n.AttrString("nodeName"); nn != "" {
			addEdge(n.ID, "node/"+nn, graph.RelRunsOn)
		}
	}

	// PVC → PV, PVC zone from PV, boundNode
	for i, n := range snap.Nodes {
		if n.Kind != graph.KindPVC {
			continue
		}
		if vn := n.AttrString("volumeName"); vn != "" {
			pvid := "pv/" + vn
			addEdge(n.ID, pvid, graph.RelBindsVolume)
			if pv, ok := idx[pvid]; ok {
				if z := pv.AttrString("zone"); z != "" {
					if snap.Nodes[i].Attributes == nil {
						snap.Nodes[i].Attributes = map[string]any{}
					}
					snap.Nodes[i].Attributes["zone"] = z
				}
			}
		}
	}

	// Service → Workload by selector vs podLabels
	for _, svc := range snap.Nodes {
		if svc.Kind != graph.KindService {
			continue
		}
		sel := mapStringAny(svc.Attributes["matchLabels"])
		if len(sel) == 0 {
			continue
		}
		for _, w := range snap.Nodes {
			if w.Kind != graph.KindWorkload || w.Namespace != svc.Namespace {
				continue
			}
			pl := mapStringAny(w.Attributes["podLabels"])
			if labelsMatch(sel, pl) {
				addEdge(svc.ID, w.ID, graph.RelRoutesTo)
			}
		}
	}

	// PDB → Workload by selector
	for _, pdb := range snap.Nodes {
		if pdb.Kind != graph.KindPDB {
			continue
		}
		sel := mapStringAny(pdb.Attributes["matchLabels"])
		if len(sel) == 0 {
			continue
		}
		for _, w := range snap.Nodes {
			if w.Kind != graph.KindWorkload || w.Namespace != pdb.Namespace {
				continue
			}
			pl := mapStringAny(w.Attributes["podLabels"])
			if labelsMatch(sel, pl) {
				addEdge(w.ID, pdb.ID, graph.RelProtectedBy)
			}
		}
	}

	// HPA → Workload
	for _, hpa := range snap.Nodes {
		if hpa.Kind != graph.KindHPA {
			continue
		}
		tn := hpa.AttrString("targetName")
		if tn == "" {
			continue
		}
		wid := fmt.Sprintf("workload/%s/%s", hpa.Namespace, tn)
		addEdge(hpa.ID, wid, graph.RelScales)
	}

	// Workload RUNS_ON via pods that share labels (simplified: pods named after workload)
	for _, pod := range snap.Nodes {
		if pod.Kind != graph.KindPod {
			continue
		}
		// owner: prefix match common generated names
		for _, w := range snap.Nodes {
			if w.Kind != graph.KindWorkload || w.Namespace != pod.Namespace {
				continue
			}
			if strings.HasPrefix(pod.Name, w.Name+"-") {
				addEdge(w.ID, pod.ID, graph.RelOwns)
				if nn := pod.AttrString("nodeName"); nn != "" {
					addEdge(w.ID, "node/"+nn, graph.RelRunsOn)
				}
			}
		}
	}
}

func addNode(snap *graph.Snapshot, n graph.Node) {
	for _, e := range snap.Nodes {
		if e.ID == n.ID {
			return
		}
	}
	snap.Nodes = append(snap.Nodes, n)
}

func ensureNS(snap *graph.Snapshot, ns string) {
	if ns == "" {
		return
	}
	id := "ns/" + ns
	for _, n := range snap.Nodes {
		if n.ID == id {
			return
		}
	}
	snap.Nodes = append(snap.Nodes, graph.Node{ID: id, Kind: graph.KindNamespace, Name: ns, Source: "kubernetes"})
}

func normalizeNS(meta map[string]any) string {
	ns, _ := meta["namespace"].(string)
	if ns == "" {
		return "default"
	}
	return ns
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	default:
		return 0
	}
}

func stringifyMap(m map[string]any) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+fmt.Sprint(v))
	}
	return strings.Join(parts, ",")
}

func mapStringAny(v any) map[string]string {
	out := map[string]string{}
	m, ok := v.(map[string]any)
	if !ok {
		// after json round-trip of yaml labels
		if m2, ok := v.(map[string]string); ok {
			return m2
		}
		return out
	}
	for k, val := range m {
		out[k] = fmt.Sprint(val)
	}
	return out
}

func labelsMatch(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
