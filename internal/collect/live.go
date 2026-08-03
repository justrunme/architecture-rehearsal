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
	// Resources is a kubectl resource list (comma-separated). Empty → default set.
	Resources string
	// Kubectl binary (default "kubectl").
	Kubectl string
	// Timeout for kubectl (default 120s).
	Timeout time.Duration
	AllowPartial bool
	Phase        graph.Phase
	ExtraMeta    map[string]any
}

// DefaultLiveResources matches the offline dump recommendation.
const DefaultLiveResources = "node,ns,deploy,sts,ds,rs,po,svc,pvc,pv,pdb,hpa,sa,ing,endpointslices"

// K8sFromLive runs kubectl get -A -o yaml (read-only) into a temp dir and reuses the offline collector.
func K8sFromLive(ctx context.Context, opts LiveOptions) (*graph.Snapshot, error) {
	if opts.Cluster == "" {
		opts.Cluster = "live"
	}
	if opts.Kubectl == "" {
		opts.Kubectl = "kubectl"
	}
	if opts.Resources == "" {
		opts.Resources = DefaultLiveResources
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 120 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	args := []string{"get", opts.Resources, "-A", "-o", "yaml"}
	if opts.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", opts.Kubeconfig}, args...)
	}
	if opts.Context != "" {
		args = append([]string{"--context", opts.Context}, args...)
	}

	cmd := exec.CommandContext(cctx, opts.Kubectl, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kubectl live collect (read-only): %w: %s", err, stringsTrim(stderr.String()))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("kubectl returned empty YAML")
	}

	dir, err := os.MkdirTemp("", "rehearsal-live-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	dump := filepath.Join(dir, "live-dump.yaml")
	if err := os.WriteFile(dump, stdout.Bytes(), 0o600); err != nil {
		return nil, err
	}

	snap, err := K8sFromManifests(cctx, dir, K8sOptions{
		ClusterName:  opts.Cluster,
		AllowPartial: opts.AllowPartial,
		Phase:        opts.Phase,
		ExtraMeta:    opts.ExtraMeta,
	})
	if err != nil {
		return nil, err
	}
	snap.Source = "kubernetes-live-kubectl"
	if snap.Meta == nil {
		snap.Meta = map[string]any{}
	}
	snap.Meta["live_collect"] = true
	snap.Meta["live_resources"] = opts.Resources
	if snap.Cluster != nil {
		snap.Cluster.CollectorVersion = "0.6.0-live"
	}
	return snap, nil
}

func stringsTrim(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
