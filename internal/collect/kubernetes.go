// Package collect provides read-only snapshot collectors.
package collect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"gopkg.in/yaml.v3"
)

// K8sFromManifests builds a snapshot from a directory of Kubernetes YAML
// (kind/kustomize/helm-rendered). Does not talk to a live API — safe offline.
// For live clusters, dump with: kubectl get deploy,sts,ds,svc,pvc,node,pdb,hpa -A -o yaml
func K8sFromManifests(ctx context.Context, dir string, clusterName string) (*graph.Snapshot, error) {
	_ = ctx
	if clusterName == "" {
		clusterName = "cluster"
	}
	start := time.Now().UTC()
	snap := &graph.Snapshot{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       graph.DocKindSnapshot,
		ID:         "k8s-" + clusterName,
		Name:       clusterName,
		Source:     "kubernetes-manifests",
		Phase:      graph.PhaseBaseline,
		CreatedAt:  start,
		Labels:     map[string]string{"cluster": clusterName},
		Meta:       map[string]any{},
		Cluster: &graph.ClusterInfo{
			Name:             clusterName,
			CollectedFrom:    start,
			CollectorVersion: "1.0.0",
		},
	}
	snap.Nodes = append(snap.Nodes, graph.Node{
		ID: "cluster/" + clusterName, Kind: graph.KindCluster, Name: clusterName, Source: "kubernetes",
	})

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	nsSeen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		docs := splitYAML(string(raw))
		for _, doc := range docs {
			doc = strings.TrimSpace(doc)
			if doc == "" {
				continue
			}
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				snap.Warnings = append(snap.Warnings, fmt.Sprintf("skip %s: %v", e.Name(), err))
				continue
			}
			if obj == nil {
				continue
			}
			addK8sObject(snap, obj, clusterName, nsSeen)
		}
	}
	end := time.Now().UTC()
	snap.Cluster.CollectedUntil = end
	// never collect secret values
	snap.Warnings = append(snap.Warnings, "Secret contents are never collected (references only)")
	return snap, nil
}

func isYAML(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".yaml") || strings.HasSuffix(l, ".yml")
}

func splitYAML(s string) []string {
	// simple --- splitter
	parts := strings.Split(s, "\n---")
	return parts
}

func addK8sObject(snap *graph.Snapshot, obj map[string]any, cluster string, nsSeen map[string]bool) {
	kind, _ := obj["kind"].(string)
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		return
	}
	name, _ := meta["name"].(string)
	ns, _ := meta["namespace"].(string)
	if name == "" {
		return
	}
	// strip secret data
	if kind == "Secret" {
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID: fmt.Sprintf("secret/%s/%s", ns, name), Kind: graph.KindServiceAccount, // placeholder ref only
			Name: name, Namespace: ns,
			Attributes: map[string]any{"secretRef": name, "valuesCollected": false},
			Source:     "kubernetes", SourceRef: "v1/Secret/" + ns + "/" + name,
		})
		// actually use a note in meta only — avoid wrong kind
		snap.Nodes = snap.Nodes[:len(snap.Nodes)-1]
		snap.Meta["secretRefs"] = appendStringAny(snap.Meta["secretRefs"], ns+"/"+name)
		return
	}
	if ns != "" && !nsSeen[ns] {
		nsSeen[ns] = true
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID: "ns/" + ns, Kind: graph.KindNamespace, Name: ns, Source: "kubernetes",
		})
	}
	switch kind {
	case "Node":
		id := "node/" + name
		attrs := map[string]any{"schedulable": true}
		if s, ok := obj["status"].(map[string]any); ok {
			if cap, ok := s["allocatable"].(map[string]any); ok {
				if p, ok := cap["pods"].(string); ok {
					attrs["allocatablePods"] = atoi(p)
				}
			}
		}
		if labels, ok := meta["labels"].(map[string]any); ok {
			if z, ok := labels["topology.kubernetes.io/zone"].(string); ok {
				attrs["zone"] = z
			}
		}
		snap.Nodes = append(snap.Nodes, graph.Node{ID: id, Kind: graph.KindNode, Name: name, Attributes: attrs, Source: "kubernetes"})
	case "Deployment", "StatefulSet", "DaemonSet":
		id := fmt.Sprintf("workload/%s/%s", ns, name)
		replicas := 1
		if spec, ok := obj["spec"].(map[string]any); ok {
			switch v := spec["replicas"].(type) {
			case int:
				replicas = v
			case float64:
				replicas = int(v)
			}
		}
		wtype := kind
		attrs := map[string]any{"type": wtype, "replicas": replicas}
		if kind == "StatefulSet" {
			attrs["stateful"] = true
		}
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID: id, Kind: graph.KindWorkload, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: kind + "/" + ns + "/" + name,
		})
	case "Service":
		id := fmt.Sprintf("svc/%s/%s", ns, name)
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID: id, Kind: graph.KindService, Name: name, Namespace: ns, Source: "kubernetes",
		})
	case "PersistentVolumeClaim":
		id := fmt.Sprintf("pvc/%s/%s", ns, name)
		attrs := map[string]any{}
		if spec, ok := obj["spec"].(map[string]any); ok {
			if modes, ok := spec["accessModes"].([]any); ok && len(modes) > 0 {
				if m, ok := modes[0].(string); ok {
					attrs["accessMode"] = m
				}
			}
			if sc, ok := spec["storageClassName"].(string); ok {
				attrs["storageClass"] = sc
			}
		}
		// annotations may hold bound node in some CSI — never secrets
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID: id, Kind: graph.KindPVC, Name: name, Namespace: ns, Attributes: attrs, Source: "kubernetes",
		})
	case "PodDisruptionBudget":
		id := fmt.Sprintf("pdb/%s/%s", ns, name)
		attrs := map[string]any{}
		if spec, ok := obj["spec"].(map[string]any); ok {
			if v, ok := spec["minAvailable"]; ok {
				attrs["minAvailable"] = asInt(v)
			}
		}
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID: id, Kind: graph.KindPDB, Name: name, Namespace: ns, Attributes: attrs, Source: "kubernetes",
		})
	case "HorizontalPodAutoscaler":
		id := fmt.Sprintf("hpa/%s/%s", ns, name)
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID: id, Kind: graph.KindHPA, Name: name, Namespace: ns, Source: "kubernetes",
		})
	default:
		// ignore other kinds silently for v1
		_ = cluster
	}
}

func appendStringAny(v any, s string) []any {
	var out []any
	if arr, ok := v.([]any); ok {
		out = arr
	}
	return append(out, s)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	case string:
		return atoi(t)
	default:
		return 0
	}
}
