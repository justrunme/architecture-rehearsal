// Package collect provides read-only snapshot collectors.
// Secret values are never collected.
package collect

import (
	"context"
	"encoding/json"
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
	// AllowPartial opts into skipping malformed YAML documents (default false = fail-closed).
	// When false, any YAML parse error fails the collect.
	AllowPartial bool
	// StrictYAML is deprecated: when true forces fail-closed. Prefer AllowPartial=false.
	// If StrictYAML is true, AllowPartial is ignored.
	StrictYAML bool
	// Phase overrides snapshot phase (default baseline). Use observed for post-deploy dumps.
	Phase graph.Phase
	// ExtraMeta is merged into snap.Meta after capacity derivation (e.g. observed_failures).
	ExtraMeta map[string]any
	// SnapshotID overrides default k8s-<cluster> id when non-empty.
	SnapshotID string
}

// strictMode returns true when parse errors must fail the collect (default).
func (o K8sOptions) strictMode() bool {
	if o.StrictYAML {
		return true
	}
	return !o.AllowPartial
}

// K8sFromManifests builds a snapshot from a directory tree of Kubernetes YAML
// (including kubectl List dumps). Does not call a live API.
//
// Fail-closed by default: malformed YAML aborts the collect. Pass AllowPartial
// only for deliberate partial ingestion (still recorded in warnings).
//
// Recommended dump:
//
//	kubectl get node,ns,deploy,sts,ds,po,svc,pvc,pv,pdb,hpa,sa,ing -A -o yaml > dump.yaml
func K8sFromManifests(ctx context.Context, dir string, opts K8sOptions) (*graph.Snapshot, error) {
	_ = ctx
	if opts.ClusterName == "" {
		opts.ClusterName = "cluster"
	}
	phase := opts.Phase
	if phase == "" {
		phase = graph.PhaseBaseline
	}
	strict := opts.strictMode()
	start := time.Now().UTC()
	id := "k8s-" + opts.ClusterName
	if opts.SnapshotID != "" {
		id = opts.SnapshotID
	} else if phase == graph.PhaseObserved {
		id = "k8s-" + opts.ClusterName + "-observed"
	}
	snap := &graph.Snapshot{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       graph.DocKindSnapshot,
		ID:         id,
		Name:       opts.ClusterName,
		Source:     "kubernetes-manifests",
		Phase:      phase,
		CreatedAt:  start,
		Labels:     map[string]string{"cluster": opts.ClusterName},
		Meta:       map[string]any{},
		Cluster: &graph.ClusterInfo{
			Name:             opts.ClusterName,
			CollectedFrom:    start,
			CollectorVersion: "0.7.0",
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
				if strict {
					return fmt.Errorf("yaml parse (fail-closed): %w", err)
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
	if strict && len(parseErrors) > 0 {
		return nil, fmt.Errorf("yaml errors (fail-closed): %s", strings.Join(parseErrors, "; "))
	}
	if len(parseErrors) > 0 {
		snap.Meta["yaml_parse_errors"] = len(parseErrors)
		snap.Meta["coverage_gap"] = "yaml_parse_errors_present"
	}

	buildEdges(snap)
	end := time.Now().UTC()
	snap.Cluster.CollectedUntil = end
	snap.Warnings = append(snap.Warnings, "Secret values are never collected (references only)")

	// Scheduling capacity estimate from node allocatable pods (not real CNI IP pool).
	// Kept under both keys for compatibility; prefer pod_scheduling_capacity_estimate.
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
		snap.Meta["pod_scheduling_capacity_estimate"] = avail
		snap.Meta["pod_ip_capacity_available"] = avail // compat alias until v0.6 CNI provider
		snap.Meta["node_allocatable_pods_total"] = alloc
		snap.Meta["capacity_model"] = "node_allocatable_pods_minus_desired_replicas"
	}

	// Operator/CI annotations (observed_failures, incident notes, …) after derived capacity.
	for k, v := range opts.ExtraMeta {
		snap.Meta[k] = v
	}

	// Mark Pending pods as unschedulable signals for verify (post-deploy evidence).
	if phase == graph.PhaseObserved {
		for i, n := range snap.Nodes {
			if n.Kind == graph.KindPod && n.AttrString("phase") == "Pending" {
				if snap.Nodes[i].Attributes == nil {
					snap.Nodes[i].Attributes = map[string]any{}
				}
				snap.Nodes[i].Attributes["unschedulable"] = true
			}
		}
	}

	if err := validate.Snapshot(snap); err != nil {
		return nil, fmt.Errorf("collected snapshot invalid: %w", err)
	}
	return snap, nil
}

// LoadMetaFile reads a JSON object for snapshot meta merge.
func LoadMetaFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse meta %s: %w", path, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
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
		// Live status for rollout_converged / unavailable pressure (v1.1)
		if st, ok := obj["status"].(map[string]any); ok {
			if v, ok := st["readyReplicas"]; ok {
				attrs["readyReplicas"] = asInt(v)
			}
			if v, ok := st["availableReplicas"]; ok {
				attrs["availableReplicas"] = asInt(v)
			}
			if v, ok := st["unavailableReplicas"]; ok {
				attrs["unavailableReplicas"] = asInt(v)
			}
			if v, ok := st["updatedReplicas"]; ok {
				attrs["updatedReplicas"] = asInt(v)
			}
			if v, ok := st["observedGeneration"]; ok {
				attrs["observedGeneration"] = asInt(v)
			}
		}
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindWorkload, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: kind + "/" + ns + "/" + name,
		})
	case "Event":
		// Causal evidence often lives in Events (FailedCreatePodSandBox, FailedAttachVolume).
		// Promote into meta event streams + annotate involved Pod when present.
		reason, _ := obj["reason"].(string)
		msg, _ := obj["message"].(string)
		var involvedNS, involvedName, involvedKind string
		if inv, ok := obj["involvedObject"].(map[string]any); ok {
			involvedKind, _ = inv["kind"].(string)
			involvedName, _ = inv["name"].(string)
			involvedNS, _ = inv["namespace"].(string)
			if involvedNS == "" {
				involvedNS = ns
			}
		}
		entry := map[string]any{
			"reason": reason, "message": msg,
			"kind": involvedKind, "name": involvedName, "namespace": involvedNS,
		}
		evs, _ := snap.Meta["k8s_events"].([]any)
		snap.Meta["k8s_events"] = append(evs, entry)
		// Promote CNI/RWO signals onto matching pods for verify predicates
		if strings.EqualFold(involvedKind, "Pod") && involvedName != "" {
			pid := fmt.Sprintf("pod/%s/%s", normalizeNS(map[string]any{"namespace": involvedNS}), involvedName)
			for i := range snap.Nodes {
				if snap.Nodes[i].ID != pid {
					continue
				}
				if snap.Nodes[i].Attributes == nil {
					snap.Nodes[i].Attributes = map[string]any{}
				}
				// Prefer event reason if pod status reason empty
				if snap.Nodes[i].AttrString("reason") == "" && reason != "" {
					snap.Nodes[i].Attributes["reason"] = reason
				}
				if msg != "" {
					prev := snap.Nodes[i].AttrString("message")
					if prev == "" {
						snap.Nodes[i].Attributes["message"] = msg
					} else {
						snap.Nodes[i].Attributes["message"] = prev + " | " + msg
					}
				}
				// Also stash event reasons list
				var er []any
				if raw, ok := snap.Nodes[i].Attributes["eventReasons"].([]any); ok {
					er = raw
				}
				if reason != "" {
					snap.Nodes[i].Attributes["eventReasons"] = append(er, reason)
				}
			}
		}
		// Aggregate CNI failure events for meta.cni_failure_events
		blob := strings.ToLower(reason + " " + msg)
		if strings.Contains(blob, "failedcreatepodsandbox") || strings.Contains(blob, "failed to assign") ||
			strings.Contains(blob, "networkplugin") || strings.Contains(blob, "ipamd") {
			raw, _ := snap.Meta["cni_failure_events"].([]any)
			snap.Meta["cni_failure_events"] = append(raw, reason+": "+msg)
		}
	case "ReplicaSet":
		// Record RS→Deployment mapping for ownerReferences resolution (not a graph Workload).
		if owners := ownerRefs(meta); len(owners) > 0 {
			if c := controllerOwner(owners); c != nil {
				ck := fmt.Sprint(c["kind"])
				cn := fmt.Sprint(c["name"])
				if (ck == "Deployment" || ck == "StatefulSet" || ck == "DaemonSet") && cn != "" {
					key := ns + "/" + name
					m, _ := snap.Meta["replicaset_owners"].(map[string]any)
					if m == nil {
						m = map[string]any{}
					}
					m[key] = map[string]any{"kind": ck, "name": cn, "namespace": ns}
					snap.Meta["replicaset_owners"] = m
				}
			}
		}
	case "Pod":
		id := fmt.Sprintf("pod/%s/%s", ns, name)
		attrs := map[string]any{}
		if labels, ok := meta["labels"].(map[string]any); ok {
			attrs["labels"] = labels
		}
		if owners := ownerRefs(meta); len(owners) > 0 {
			attrs["ownerReferences"] = owners
			if c := controllerOwner(owners); c != nil {
				attrs["controllerKind"] = fmt.Sprint(c["kind"])
				attrs["controllerName"] = fmt.Sprint(c["name"])
			}
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
			if r, ok := st["reason"].(string); ok && r != "" {
				attrs["reason"] = r
			}
			if m, ok := st["message"].(string); ok && m != "" {
				attrs["message"] = m
			}
			if nn, ok := st["nominatedNodeName"].(string); ok && nodeName == "" {
				nodeName = nn
			}
			// container waiting reasons (ImagePullBackOff, CreateContainerConfigError, …)
			if css, ok := st["containerStatuses"].([]any); ok {
				for _, cs := range css {
					csm, _ := cs.(map[string]any)
					if csm == nil {
						continue
					}
					if state, ok := csm["state"].(map[string]any); ok {
						if waiting, ok := state["waiting"].(map[string]any); ok {
							if r, ok := waiting["reason"].(string); ok && r != "" {
								attrs["waitingReason"] = r
							}
							if m, ok := waiting["message"].(string); ok && m != "" {
								attrs["message"] = fmt.Sprint(attrs["message"]) + " " + m
							}
						}
					}
				}
			}
		}
		if nodeName != "" {
			attrs["nodeName"] = nodeName
		}
		addNode(snap, graph.Node{
			ID: id, Kind: graph.KindPod, Name: name, Namespace: ns, Attributes: attrs,
			Source: "kubernetes", SourceRef: "v1/Pod/" + ns + "/" + name,
		})
		_ = phase
	case "EndpointSlice":
		// Annotate matching Service with ready endpoint count (actual routing).
		svcName := ""
		if labels, ok := meta["labels"].(map[string]any); ok {
			if s, ok := labels["kubernetes.io/service-name"].(string); ok {
				svcName = s
			}
		}
		ready := 0
		if endpoints, ok := obj["endpoints"].([]any); ok {
			for _, ep := range endpoints {
				em, _ := ep.(map[string]any)
				if em == nil {
					continue
				}
				if cond, ok := em["conditions"].(map[string]any); ok {
					if r, ok := cond["ready"].(bool); ok && r {
						ready++
					}
				} else {
					ready++
				}
			}
		}
		if svcName != "" {
			sid := fmt.Sprintf("svc/%s/%s", ns, svcName)
			// Ensure service shell exists so attributes can attach.
			addNode(snap, graph.Node{
				ID: sid, Kind: graph.KindService, Name: svcName, Namespace: ns,
				Attributes: map[string]any{},
				Source:     "kubernetes", SourceRef: "v1/Service/" + ns + "/" + svcName,
			})
			for i := range snap.Nodes {
				if snap.Nodes[i].ID == sid {
					if snap.Nodes[i].Attributes == nil {
						snap.Nodes[i].Attributes = map[string]any{}
					}
					// accumulate ready across slices
					prev := snap.Nodes[i].AttrInt("readyEndpoints")
					snap.Nodes[i].Attributes["readyEndpoints"] = prev + ready
					snap.Nodes[i].Attributes["hasEndpointSlice"] = true
					break
				}
			}
		}
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
				if z := zoneFromNodeAffinity(nsel); z != "" {
					attrs["zone"] = z
				}
			}
			if csi, ok := spec["csi"].(map[string]any); ok {
				if va, ok := csi["volumeAttributes"].(map[string]any); ok {
					if z, ok := va["topology.kubernetes.io/zone"].(string); ok {
						attrs["zone"] = z
					}
				}
			}
			if aws, ok := spec["awsElasticBlockStore"].(map[string]any); ok {
				if z, ok := aws["zone"].(string); ok && attrs["zone"] == nil {
					attrs["zone"] = z
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
			ID: id, Kind: graph.KindPV, Name: name, Attributes: attrs,
			Source: "kubernetes", SourceRef: "v1/PV/" + name,
		})
	case "PodDisruptionBudget":
		id := fmt.Sprintf("pdb/%s/%s", ns, name)
		attrs := map[string]any{}
		if spec, ok := obj["spec"].(map[string]any); ok {
			if v, ok := spec["minAvailable"]; ok {
				attrs["minAvailableRaw"] = fmt.Sprint(v)
				if s, ok := v.(string); ok && strings.HasSuffix(s, "%") {
					attrs["minAvailablePercent"] = asInt(strings.TrimSuffix(s, "%"))
				} else {
					attrs["minAvailable"] = asInt(v)
				}
			}
			if v, ok := spec["maxUnavailable"]; ok {
				attrs["maxUnavailableRaw"] = fmt.Sprint(v)
				if s, ok := v.(string); ok && strings.HasSuffix(s, "%") {
					attrs["maxUnavailablePercent"] = asInt(strings.TrimSuffix(s, "%"))
				} else {
					attrs["maxUnavailable"] = asInt(v)
				}
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

	// Workload ownership: prefer ownerReferences chain, fall back to name prefix.
	rsOwners, _ := snap.Meta["replicaset_owners"].(map[string]any)
	for _, pod := range snap.Nodes {
		if pod.Kind != graph.KindPod {
			continue
		}
		linked := false
		ck := pod.AttrString("controllerKind")
		cn := pod.AttrString("controllerName")
		if ck != "" && cn != "" {
			var wid string
			switch ck {
			case "ReplicaSet":
				if rsOwners != nil {
					if raw, ok := rsOwners[pod.Namespace+"/"+cn].(map[string]any); ok {
						wid = fmt.Sprintf("workload/%s/%s", pod.Namespace, fmt.Sprint(raw["name"]))
					}
				}
				if wid == "" {
					wid = resolveRSToWorkload(snap, pod.Namespace, cn)
				}
			case "StatefulSet", "DaemonSet", "Deployment", "Job", "ControllerRevision":
				wid = fmt.Sprintf("workload/%s/%s", pod.Namespace, cn)
			}
			if wid != "" {
				if _, ok := idx[wid]; ok {
					addEdge(wid, pod.ID, graph.RelOwns)
					if nn := pod.AttrString("nodeName"); nn != "" {
						addEdge(wid, "node/"+nn, graph.RelRunsOn)
					}
					linked = true
				}
			}
		}
		if linked {
			continue
		}
		// Fallback: prefix match (legacy dumps without ownerReferences)
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

func ownerRefs(meta map[string]any) []any {
	raw, ok := meta["ownerReferences"].([]any)
	if !ok {
		return nil
	}
	var out []any
	for _, o := range raw {
		m, ok := o.(map[string]any)
		if !ok {
			continue
		}
		// strip uid-heavy noise; keep kind/name/controller
		entry := map[string]any{
			"kind": m["kind"],
			"name": m["name"],
		}
		if c, ok := m["controller"].(bool); ok {
			entry["controller"] = c
		}
		out = append(out, entry)
	}
	return out
}

func controllerOwner(owners []any) map[string]any {
	var fallback map[string]any
	for _, o := range owners {
		m, ok := o.(map[string]any)
		if !ok {
			continue
		}
		fallback = m
		if c, ok := m["controller"].(bool); ok && c {
			return m
		}
	}
	return fallback
}

func resolveRSToWorkload(snap *graph.Snapshot, ns, rsName string) string {
	// ReplicaSet name: <deployment>-<pod-template-hash>
	// Find workload whose name is a prefix of rsName
	best := ""
	for _, w := range snap.Nodes {
		if w.Kind != graph.KindWorkload || w.Namespace != ns {
			continue
		}
		if strings.HasPrefix(rsName, w.Name+"-") && len(w.Name) > len(best) {
			best = w.Name
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("workload/%s/%s", ns, best)
}

func zoneFromNodeAffinity(nsel map[string]any) string {
	// spec.nodeAffinity.required.nodeSelectorTerms[].matchExpressions
	req, _ := nsel["required"].(map[string]any)
	if req == nil {
		req, _ = nsel["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)
	}
	if req == nil {
		return ""
	}
	terms, _ := req["nodeSelectorTerms"].([]any)
	for _, t := range terms {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		exprs, _ := tm["matchExpressions"].([]any)
		for _, e := range exprs {
			em, _ := e.(map[string]any)
			if em == nil {
				continue
			}
			key, _ := em["key"].(string)
			if key != "topology.kubernetes.io/zone" && key != "failure-domain.beta.kubernetes.io/zone" {
				continue
			}
			vals, _ := em["values"].([]any)
			if len(vals) > 0 {
				return fmt.Sprint(vals[0])
			}
		}
	}
	return ""
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
