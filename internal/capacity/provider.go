// Package capacity separates scheduling estimates from real CNI IP pool data.
package capacity

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// Source describes how capacity was obtained.
const (
	SourceSchedulingEstimate = "pod_scheduling_capacity_estimate"
	SourceCNIExplicit        = "cni_ip_pool"
	SourceUnknown            = "unknown"
)

// Result is a capacity reading.
type Result struct {
	Available int
	Source    string
	Detail    string
}

// Provider reads available pod/IP capacity from a snapshot.
type Provider interface {
	Read(snap *graph.Snapshot) (Result, error)
}

// SchedulingEstimate uses node allocatablePods − desired workload replicas.
// This is NOT real AWS VPC CNI free IP count.
type SchedulingEstimate struct{}

func (SchedulingEstimate) Read(snap *graph.Snapshot) (Result, error) {
	if snap == nil {
		return Result{Source: SourceUnknown}, fmt.Errorf("nil snapshot")
	}
	if snap.Meta != nil {
		if v, ok := asInt(snap.Meta["pod_scheduling_capacity_estimate"]); ok {
			return Result{Available: v, Source: SourceSchedulingEstimate, Detail: "from meta.pod_scheduling_capacity_estimate"}, nil
		}
		// compat
		if v, ok := asInt(snap.Meta["pod_ip_capacity_available"]); ok {
			return Result{Available: v, Source: SourceSchedulingEstimate, Detail: "compat alias pod_ip_capacity_available"}, nil
		}
	}
	alloc, running := 0, 0
	for _, n := range snap.Nodes {
		switch n.Kind {
		case graph.KindNode:
			alloc += n.AttrInt("allocatablePods")
		case graph.KindWorkload:
			running += n.WorkloadReplicas()
		}
	}
	if alloc == 0 {
		return Result{Source: SourceUnknown, Detail: "no node allocatablePods"}, fmt.Errorf("no capacity data")
	}
	avail := alloc - running
	if avail < 0 {
		avail = 0
	}
	return Result{
		Available: avail,
		Source:    SourceSchedulingEstimate,
		Detail:    fmt.Sprintf("derived allocatable=%d desiredReplicas=%d", alloc, running),
	}, nil
}

// CNIExplicit reads a real CNI capacity source injected into meta
// (e.g. from aws-node metrics / custom exporter). Never invents IPs.
type CNIExplicit struct{}

func (CNIExplicit) Read(snap *graph.Snapshot) (Result, error) {
	if snap == nil || snap.Meta == nil {
		return Result{Source: SourceUnknown}, fmt.Errorf("no cni meta")
	}
	if v, ok := asInt(snap.Meta["cni_ip_available"]); ok {
		return Result{
			Available: v,
			Source:    SourceCNIExplicit,
			Detail:    "from meta.cni_ip_available (external CNI provider)",
		}, nil
	}
	return Result{Source: SourceUnknown}, fmt.Errorf("meta.cni_ip_available not set — use SchedulingEstimate or inject CNI data")
}

// Best prefers explicit CNI, then scheduling estimate.
type Best struct{}

func (Best) Read(snap *graph.Snapshot) (Result, error) {
	if r, err := (CNIExplicit{}).Read(snap); err == nil {
		return r, nil
	}
	return (SchedulingEstimate{}).Read(snap)
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	default:
		return 0, false
	}
}
