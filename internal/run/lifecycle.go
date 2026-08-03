// Package run implements the RehearsalRun state machine (v0.9).
package run

import (
	"fmt"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/contract"
)

// Phase is a lifecycle state.
type Phase string

const (
	PhasePending             Phase = "Pending"
	PhaseCollecting          Phase = "Collecting"
	PhaseCompiling           Phase = "Compiling"
	PhaseRehearsing          Phase = "Rehearsing"
	PhaseGated               Phase = "Gated"
	PhaseWaitingForDeployment Phase = "WaitingForDeployment"
	PhaseObserving           Phase = "Observing"
	PhaseVerifying           Phase = "Verifying"
	PhaseCompleted           Phase = "Completed"
	PhaseFailed              Phase = "Failed"
	PhaseInconclusive        Phase = "Inconclusive"
	PhaseCancelled           Phase = "Cancelled"
)

// allowedTransitions defines the state machine.
var allowedTransitions = map[Phase][]Phase{
	PhasePending:              {PhaseCollecting, PhaseCancelled, PhaseFailed},
	PhaseCollecting:           {PhaseCompiling, PhaseFailed, PhaseCancelled},
	PhaseCompiling:            {PhaseRehearsing, PhaseFailed, PhaseCancelled},
	PhaseRehearsing:           {PhaseGated, PhaseFailed, PhaseCancelled},
	PhaseGated:                {PhaseWaitingForDeployment, PhaseCompleted, PhaseFailed, PhaseCancelled},
	PhaseWaitingForDeployment: {PhaseObserving, PhaseFailed, PhaseCancelled, PhaseInconclusive},
	PhaseObserving:            {PhaseVerifying, PhaseFailed, PhaseCancelled},
	PhaseVerifying:            {PhaseCompleted, PhaseFailed, PhaseInconclusive},
	PhaseCompleted:            {},
	PhaseFailed:               {},
	PhaseInconclusive:         {},
	PhaseCancelled:            {},
}

// RehearsalRun is the durable run record (v0.9 CRD-compatible JSON).
type RehearsalRun struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	// IdempotencyKey prevents double-apply of the same run.
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Spec           Spec              `json:"spec"`
	Status         Status            `json:"status"`
	Digests        contract.ArtifactDigests `json:"digests,omitempty"`
	// Version is an optimistic concurrency token (v1.4.1+). Incremented on each durable update.
	Version   int64     `json:"version,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Spec is the desired run configuration.
type Spec struct {
	ClusterName string   `json:"clusterName,omitempty"`
	BaselineRef string   `json:"baselineRef,omitempty"` // path or URI
	ChangeRef   string   `json:"changeRef,omitempty"`
	ObservedRef string   `json:"observedRef,omitempty"`
	Scenarios   []string `json:"scenarios,omitempty"`
	Gate        GateSpec `json:"gate,omitempty"`
	// PolicyPath optional organization policy YAML (v1.1 wired into gate).
	PolicyPath string `json:"policyPath,omitempty"`
	// OutDir stores chain/report artifacts when set.
	OutDir string `json:"outDir,omitempty"`
	// Timeout for the whole run.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// GateSpec controls blocking.
type GateSpec struct {
	BlockOn []string `json:"blockOn,omitempty"` // critical, high, unknown
	WarnOn  []string `json:"warnOn,omitempty"`
}

// Status is observed run state.
type Status struct {
	Phase            Phase      `json:"phase"`
	Decision         string     `json:"decision,omitempty"`
	Risk             string     `json:"risk,omitempty"`
	Message          string     `json:"message,omitempty"`
	Attempts         int        `json:"attempts"`
	Lease            *Lease     `json:"lease,omitempty"`
	History          []Event    `json:"history,omitempty"`
	Deadline         *time.Time `json:"deadline,omitempty"`
	// ChainPath is where evidence-chain.json was written (v1.1).
	ChainPath        string     `json:"chainPath,omitempty"`
	VerifyOutcome    string     `json:"verifyOutcome,omitempty"`
	PredictedFailures []string  `json:"predictedFailures,omitempty"`
}

// Lease is a distributed-style exclusive lock (single-node for now).
type Lease struct {
	Holder    string    `json:"holder"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Event is a state transition record.
type Event struct {
	Time    time.Time `json:"time"`
	From    Phase     `json:"from"`
	To      Phase     `json:"to"`
	Message string    `json:"message,omitempty"`
}

// NewRun creates a Pending run.
func NewRun(id, idempotencyKey string, spec Spec) *RehearsalRun {
	now := time.Now().UTC()
	var deadline *time.Time
	if spec.TimeoutSeconds > 0 {
		d := now.Add(time.Duration(spec.TimeoutSeconds) * time.Second)
		deadline = &d
	}
	return &RehearsalRun{
		APIVersion:     contract.APIVersionV1Beta1,
		Kind:           contract.KindRehearsalRun,
		ID:             id,
		IdempotencyKey: idempotencyKey,
		Spec:           spec,
		Status: Status{
			Phase:    PhasePending,
			Deadline: deadline,
			History:  []Event{{Time: now, From: "", To: PhasePending, Message: "created"}},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// Transition moves the run to next phase if allowed.
func (r *RehearsalRun) Transition(to Phase, msg string) error {
	if r == nil {
		return fmt.Errorf("nil run")
	}
	from := r.Status.Phase
	if from == to {
		return nil // idempotent
	}
	ok := false
	for _, a := range allowedTransitions[from] {
		if a == to {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("invalid transition %s → %s", from, to)
	}
	if r.Status.Deadline != nil && time.Now().UTC().After(*r.Status.Deadline) && to != PhaseFailed && to != PhaseCancelled {
		return fmt.Errorf("run deadline exceeded")
	}
	now := time.Now().UTC()
	r.Status.History = append(r.Status.History, Event{Time: now, From: from, To: to, Message: msg})
	r.Status.Phase = to
	r.Status.Message = msg
	r.UpdatedAt = now
	return nil
}

// AcquireLease sets an exclusive lease.
func (r *RehearsalRun) AcquireLease(holder string, ttl time.Duration) error {
	now := time.Now().UTC()
	if r.Status.Lease != nil && r.Status.Lease.Holder != holder && r.Status.Lease.ExpiresAt.After(now) {
		return fmt.Errorf("lease held by %s until %s", r.Status.Lease.Holder, r.Status.Lease.ExpiresAt)
	}
	r.Status.Lease = &Lease{Holder: holder, ExpiresAt: now.Add(ttl)}
	r.UpdatedAt = now
	return nil
}

// ReleaseLease clears lease if holder matches.
func (r *RehearsalRun) ReleaseLease(holder string) {
	if r.Status.Lease != nil && r.Status.Lease.Holder == holder {
		r.Status.Lease = nil
	}
}

// Terminal reports whether phase is final.
func (p Phase) Terminal() bool {
	switch p {
	case PhaseCompleted, PhaseFailed, PhaseInconclusive, PhaseCancelled:
		return true
	default:
		return false
	}
}
