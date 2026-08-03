package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RehearsalRunSpec defines the desired state of RehearsalRun.
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
	// ControlPlaneURL overrides default REHEARSAL_API_URL.
	// +optional
	ControlPlaneURL string `json:"controlPlaneURL,omitempty"`
}

// RehearsalRunStatus defines the observed state.
type RehearsalRunStatus struct {
	// Phase mirrors control-plane phase.
	Phase string `json:"phase,omitempty"`
	// Decision approve|warn|block|unknown.
	Decision string `json:"decision,omitempty"`
	// Risk level.
	Risk string `json:"risk,omitempty"`
	// ControlPlaneRunID is the durable run id (namespace/name by default).
	ControlPlaneRunID string `json:"controlPlaneRunId,omitempty"`
	// JobID last enqueue id when async.
	JobID string `json:"jobId,omitempty"`
	// Message human status.
	Message string `json:"message,omitempty"`
	// Conditions standard k8s conditions.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Decision",type=string,JSONPath=`.status.decision`
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
