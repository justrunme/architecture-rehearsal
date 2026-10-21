package collect

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// LiveOptions controls read-only live cluster collection via kubectl.
// Requires kubectl on PATH and a readable kubeconfig. Never writes to the cluster.
type LiveOptions struct {
	Kubeconfig string
	Context    string
	Cluster    string
	// Namespace, when set, scopes namespaced inventory (Deployments, PDBs, …)
	// to that namespace. Nodes and Pods are ALWAYS collected cluster-wide
	// (best-effort) so scheduling-capacity sees cross-namespace pressure.
	Namespace string
	// Resources is a kubectl resource list (comma-separated). Empty → default set.
	Resources string
	// Kubectl binary (default "kubectl").
	Kubectl string
	// Timeout for kubectl (default 120s).
	Timeout      time.Duration
	AllowPartial bool
	Phase        graph.Phase
	ExtraMeta    map[string]any
}

// DefaultLiveResources matches the offline dump recommendation.
const DefaultLiveResources = "node,ns,deploy,sts,ds,rs,po,svc,pvc,pv,pdb,hpa,sa,ing,networkpolicies,endpointslices,events"

// NamespacedLiveResources is collected with -n when LiveOptions.Namespace is set.
// Nodes and Pods are fetched separately (cluster-wide) and merged.
const NamespacedLiveResources = "deploy,sts,ds,rs,svc,pvc,pdb,hpa,sa,ing,networkpolicies,endpointslices,events"

// K8sFromLive runs kubectl get (read-only) into a temp dir and reuses the offline collector.
func K8sFromLive(ctx context.Context, opts LiveOptions) (*graph.Snapshot, error) {
	if opts.Cluster == "" {
		opts.Cluster = "live"
	}
	if opts.Kubectl == "" {
		opts.Kubectl = "kubectl"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 120 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "rehearsal-live-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	var nodesErr, podsErr string
	var resourcesUsed string

	if opts.Namespace != "" {
		res := opts.Resources
		if res == "" {
			res = NamespacedLiveResources
		}
		resourcesUsed = res + "+node(best-effort)+pods-A(best-effort)"
		nsDump := filepath.Join(dir, "live-ns.yaml")
		if err := kubectlGetYAML(cctx, opts, []string{"get", res, "-n", opts.Namespace, "-o", "yaml"}, nsDump); err != nil {
			return nil, err
		}
		nodesDump := filepath.Join(dir, "live-nodes.yaml")
		if err := kubectlGetYAML(cctx, opts, []string{"get", "nodes", "-o", "yaml"}, nodesDump); err != nil {
			nodesErr = err.Error()
			_ = os.Remove(nodesDump)
		}
		podsDump := filepath.Join(dir, "live-pods.yaml")
		if err := kubectlGetYAML(cctx, opts, []string{"get", "pods", "-A", "-o", "yaml"}, podsDump); err != nil {
			podsErr = err.Error()
			_ = os.Remove(podsDump)
		}
	} else {
		res := opts.Resources
		if res == "" {
			res = DefaultLiveResources
		}
		resourcesUsed = res
		dump := filepath.Join(dir, "live-dump.yaml")
		if err := kubectlGetYAML(cctx, opts, []string{"get", res, "-A", "-o", "yaml"}, dump); err != nil {
			return nil, err
		}
	}

	extra := map[string]any{}
	for k, v := range opts.ExtraMeta {
		extra[k] = v
	}
	if nodesErr != "" {
		extra["nodes_collect_error"] = nodesErr
		extra["nodes_collect_ok"] = false
	} else if opts.Namespace != "" {
		extra["nodes_collect_ok"] = true
	}
	if opts.Namespace != "" {
		if podsErr != "" {
			extra["pods_collect_error"] = podsErr
			extra["pods_collect_ok"] = false
		} else {
			extra["pods_collect_ok"] = true
		}
	}

	snap, err := K8sFromManifests(cctx, dir, K8sOptions{
		ClusterName:  opts.Cluster,
		AllowPartial: opts.AllowPartial,
		Phase:        opts.Phase,
		ExtraMeta:    extra,
	})
	if err != nil {
		return nil, err
	}
	snap.Source = "kubernetes-live-kubectl"
	if snap.Meta == nil {
		snap.Meta = map[string]any{}
	}
	snap.Meta["live_collect"] = true
	snap.Meta["live_resources"] = resourcesUsed
	if opts.Namespace != "" {
		snap.Meta["live_collect_namespace"] = opts.Namespace
	}
	if snap.Cluster != nil {
		snap.Cluster.CollectorVersion = "0.6.0-live"
	}
	return snap, nil
}

func kubectlGetYAML(ctx context.Context, opts LiveOptions, baseArgs []string, outPath string) error {
	args := append([]string{}, baseArgs...)
	if opts.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", opts.Kubeconfig}, args...)
	}
	if opts.Context != "" {
		args = append([]string{"--context", opts.Context}, args...)
	}
	cmd := exec.CommandContext(ctx, opts.Kubectl, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl live collect (read-only): %w: %s", err, stringsTrim(stderr.String()))
	}
	if stdout.Len() == 0 {
		return fmt.Errorf("kubectl returned empty YAML")
	}
	return os.WriteFile(outPath, stdout.Bytes(), 0o600)
}

func stringsTrim(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
