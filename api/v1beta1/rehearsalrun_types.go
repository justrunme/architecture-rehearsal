package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types for RehearsalRun status (v1.5.1).
const (
	ConditionAccepted = "Accepted"
	ConditionRunning  = "Running"
	ConditionReady    = "Ready"
	ConditionFailed   = "Failed"
)

// RehearsalRunSpec defines the desired state of RehearsalRun.
// Control plane URL is NEVER set on the CR — only via operator env REHEARSAL_API_URL (v1.5.1).
type RehearsalRunSpec struct {
	// BaselineRef is a path/URI relative to the control-plane workdir.
	BaselineRef string `json:"baselineRef"`
	// ChangeRef is the change artifact path/URI.
	ChangeRef string `json:"changeRef"`
	// ObservedRef optional post-deploy snapshot.
	ObservedRef string `json:"observedRef,omitempty"`
	// ClusterName optional registered cluster.
	ClusterName string `json:"clusterName,omitempty"`
	// Scenarios optional filter.
	Scenarios []string `json:"scenarios,omitempty"`
	// Async when true enqueues advance on the control plane (default true).
	// +optional
	Async *bool `json:"async,omitempty"`
}

// RehearsalRunStatus defines the observed state.
type RehearsalRunStatus struct {
	// ObservedGeneration is the last reconciled metadata.generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// SpecDigest is sha256 of the reconciled Spec (detects drift).
	// +optional
	SpecDigest string `json:"specDigest,omitempty"`
	// Phase mirrors control-plane phase.
	Phase string `json:"phase,omitempty"`
	// Decision approve|warn|block|unknown.
	Decision string `json:"decision,omitempty"`
	// Risk level.
	Risk string `json:"risk,omitempty"`
	// ControlPlaneRunID is the durable run id (includes generation for immutability).
	ControlPlaneRunID string `json:"controlPlaneRunId,omitempty"`
	// JobID last enqueue id when async (set once per generation).
	JobID string `json:"jobId,omitempty"`
	// EvidenceDigest is chain/report digest from control plane when available.
	// +optional
	EvidenceDigest string `json:"evidenceDigest,omitempty"`
	// Message human status.
	Message string `json:"message,omitempty"`
	// Conditions: Accepted, Running, Ready, Failed.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Decision",type=string,JSONPath=`.status.decision`
// +kubebuilder:printcolumn:name="Gen",type=integer,JSONPath=`.status.observedGeneration`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RehearsalRun is the Schema for the rehearsalruns API.
type RehearsalRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RehearsalRunSpec   `json:"spec,omitempty"`
	Status RehearsalRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RehearsalRunList contains a list of RehearsalRun.
type RehearsalRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RehearsalRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RehearsalRun{}, &RehearsalRunList{})
}
