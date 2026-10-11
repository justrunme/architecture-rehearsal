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
	var allocCPU, allocMem, reqCPU, reqMem int64
	haveCPU, haveMem := false, false
	missingRequests := false
	for _, n := range snap.Nodes {
		switch n.Kind {
		case graph.KindNode:
			alloc += n.AttrInt("allocatablePods")
			if v := n.AttrInt("allocatableCPUMillis"); v > 0 {
				allocCPU += int64(v)
				haveCPU = true
			}
			if v := n.AttrInt("allocatableMemoryBytes"); v > 0 {
				allocMem += int64(v)
				haveMem = true
			}
		case graph.KindWorkload:
			reps := n.WorkloadReplicas()
			running += reps
			cpu := n.AttrInt("cpuRequestMillis")
			mem := n.AttrInt("memoryRequestBytes")
			if reps > 0 && (haveCPU || haveMem) {
				if cpu <= 0 && haveCPU {
					missingRequests = true
				}
				if mem <= 0 && haveMem {
					missingRequests = true
				}
			}
			if cpu > 0 {
				reqCPU += int64(cpu)*int64(reps)
			}
			if mem > 0 {
				reqMem += int64(mem)*int64(reps)
			}
		}
	}
	if alloc == 0 && !haveCPU && !haveMem {
		return Result{Source: SourceUnknown, Detail: "no node allocatablePods"}, fmt.Errorf("no capacity data")
	}
	if (haveCPU || haveMem) && missingRequests {
		return Result{Source: SourceUnknown, Detail: "workloads missing cpu/memory requests while nodes expose allocatable resources"}, fmt.Errorf("missing resource requests")
	}
	avail := alloc - running
	if avail < 0 {
		avail = 0
	}
	detail := fmt.Sprintf("derived allocatable=%d desiredReplicas=%d", alloc, running)
	if haveCPU {
		cpuAvail := allocCPU - reqCPU
		if cpuAvail < 0 {
			avail = 0
			detail += fmt.Sprintf("; cpuMillis over by %d", -cpuAvail)
		} else {
			detail += fmt.Sprintf("; cpuMillis avail=%d", cpuAvail)
		}
	}
	if haveMem {
		memAvail := allocMem - reqMem
		if memAvail < 0 {
			avail = 0
			detail += fmt.Sprintf("; memoryBytes over by %d", -memAvail)
		} else {
			detail += fmt.Sprintf("; memoryBytes avail=%d", memAvail)
		}
	}
	return Result{
		Available: avail,
		Source:    SourceSchedulingEstimate,
		Detail:    detail,
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
