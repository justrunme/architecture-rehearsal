// Package controller implements the Kubernetes RehearsalRun reconciler (v1.5.1).
// Trust boundary: control plane URL and token come ONLY from operator deployment.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	rehearsalv1beta1 "github.com/justrunme/architecture-rehearsal/api/v1beta1"
	"github.com/justrunme/architecture-rehearsal/internal/operator"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// RehearsalRunReconciler reconciles RehearsalRun CRs against the control plane.
// APIBase and Token MUST be set from deployment env — never from CR.
type RehearsalRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIBase is REHEARSAL_API_URL (deployment-only).
	APIBase string
	// Token is REHEARSAL_API_TOKEN (from Secret).
	Token string
	// ClientFactory builds a control-plane client (tests inject).
	ClientFactory func(base, token string) *operator.ControlPlaneClient
}

// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns/finalizers,verbs=update

func (r *RehearsalRunReconciler) cpClient(base, token string) *operator.ControlPlaneClient {
	if r.ClientFactory != nil {
		return r.ClientFactory(base, token)
	}
	return &operator.ControlPlaneClient{BaseURL: base, Token: token}
}

func (r *RehearsalRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var cr rehearsalv1beta1.RehearsalRun
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	apiURL := r.APIBase
	if apiURL == "" {
		apiURL = os.Getenv("REHEARSAL_API_URL")
	}
	if apiURL == "" {
		return r.fail(ctx, &cr, "REHEARSAL_API_URL required (set only on operator Deployment, never on CR)")
	}
	token := r.Token
	if token == "" {
		token = os.Getenv("REHEARSAL_API_TOKEN")
	}
	if token == "" {
		return r.fail(ctx, &cr, "REHEARSAL_API_TOKEN required (Secret mount)")
	}

	// Accepted: CR schema valid and operator config present.
	setCond(&cr, rehearsalv1beta1.ConditionAccepted, metav1.ConditionTrue, "Accepted", "spec accepted by operator")
	setCond(&cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionFalse, "None", "")

	digest, err := SpecDigest(cr.Spec)
	if err != nil {
		return r.fail(ctx, &cr, "spec digest: "+err.Error())
	}

	// Run ID is immutable per generation: new generation → new control-plane run.
	runID := fmt.Sprintf("%s-%s-g%d", cr.Namespace, cr.Name, cr.Generation)
	sameGen := cr.Status.ObservedGeneration == cr.Generation && cr.Status.SpecDigest == digest
	if sameGen && cr.Status.ControlPlaneRunID != "" {
		runID = cr.Status.ControlPlaneRunID
	}

	cp := r.cpClient(apiURL, token)
	rr := run.NewRun(runID, runID, run.Spec{
		BaselineRef: cr.Spec.BaselineRef,
		ChangeRef:   cr.Spec.ChangeRef,
		ObservedRef: cr.Spec.ObservedRef,
		ClusterName: cr.Spec.ClusterName,
		Scenarios:   cr.Spec.Scenarios,
	})
	rr.Labels = map[string]string{
		"k8s.namespace": cr.Namespace,
		"k8s.name":      cr.Name,
		"specDigest":    digest,
	}

	ens, err := cp.EnsureRun(rr)
	if err != nil {
		logger.Error(err, "ensure run")
		return r.fail(ctx, &cr, err.Error())
	}
	if ens.Conflict {
		if ens.Run != nil && !specMatchesRun(cr.Spec, ens.Run) {
			return r.fail(ctx, &cr, fmt.Sprintf(
				"control plane run %q exists with different refs (immutable); delete CR or bump generation via name change", runID))
		}
		// 409 with matching spec is OK (idempotent re-create).
	}

	async := true
	if cr.Spec.Async != nil {
		async = *cr.Spec.Async
	}

	// Do not re-advance if we already enqueued a job for this generation, or run is terminal.
	alreadyAdvanced := sameGen && cr.Status.JobID != ""
	existingTerminal := ens.Run != nil && ens.Run.Status.Phase.Terminal()
	needAdvance := !alreadyAdvanced && !existingTerminal &&
		(cr.Status.Phase == "" || !isTerminal(cr.Status.Phase) || !sameGen)

	if needAdvance {
		adv, err := cp.Advance(runID, async)
		if err != nil {
			logger.Error(err, "advance")
			return r.fail(ctx, &cr, err.Error())
		}
		if adv != nil && adv.JobID != "" {
			cr.Status.JobID = adv.JobID
		}
		setCond(&cr, rehearsalv1beta1.ConditionRunning, metav1.ConditionTrue, "Advancing", "control plane advance requested")
	}

	latest, err := cp.GetRun(runID)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}
	cr.Status.ControlPlaneRunID = runID
	cr.Status.Phase = string(latest.Status.Phase)
	cr.Status.Decision = latest.Status.Decision
	cr.Status.Risk = latest.Status.Risk
	cr.Status.Message = latest.Status.Message
	cr.Status.ObservedGeneration = cr.Generation
	cr.Status.SpecDigest = digest
	cr.Status.EvidenceDigest = evidenceFromRun(latest)

	if isTerminal(cr.Status.Phase) {
		setCond(&cr, rehearsalv1beta1.ConditionRunning, metav1.ConditionFalse, "Terminal", "run finished")
		if cr.Status.Phase == string(run.PhaseFailed) {
			setCond(&cr, rehearsalv1beta1.ConditionReady, metav1.ConditionFalse, "Failed", latest.Status.Message)
			setCond(&cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionTrue, "Failed", latest.Status.Message)
		} else {
			setCond(&cr, rehearsalv1beta1.ConditionReady, metav1.ConditionTrue, "Complete", "run reached terminal phase")
			setCond(&cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionFalse, "None", "")
		}
	} else {
		setCond(&cr, rehearsalv1beta1.ConditionRunning, metav1.ConditionTrue, "InProgress", "run not terminal")
		setCond(&cr, rehearsalv1beta1.ConditionReady, metav1.ConditionFalse, "InProgress", "waiting for control plane")
	}

	if err := r.Status().Update(ctx, &cr); err != nil {
		return ctrl.Result{}, err
	}
	if isTerminal(cr.Status.Phase) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// SpecDigest returns a stable sha256 of the CR spec for drift detection.
func SpecDigest(spec rehearsalv1beta1.RehearsalRunSpec) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func evidenceFromRun(rr *run.RehearsalRun) string {
	if rr == nil {
		return ""
	}
	if rr.Labels != nil {
		if d := rr.Labels["chainBlob"]; d != "" {
			return d
		}
		if d := rr.Labels["_semanticDigest"]; d != "" {
			return d
		}
	}
	if rr.Digests.ReportDigest != "" {
		return string(rr.Digests.ReportDigest)
	}
	return ""
}

func specMatchesRun(spec rehearsalv1beta1.RehearsalRunSpec, rr *run.RehearsalRun) bool {
	if rr == nil {
		return false
	}
	return rr.Spec.BaselineRef == spec.BaselineRef &&
		rr.Spec.ChangeRef == spec.ChangeRef &&
		rr.Spec.ObservedRef == spec.ObservedRef &&
		rr.Spec.ClusterName == spec.ClusterName
}

func (r *RehearsalRunReconciler) fail(ctx context.Context, cr *rehearsalv1beta1.RehearsalRun, msg string) (ctrl.Result, error) {
	cr.Status.Message = msg
	setCond(cr, rehearsalv1beta1.ConditionAccepted, metav1.ConditionTrue, "Accepted", "spec accepted")
	setCond(cr, rehearsalv1beta1.ConditionRunning, metav1.ConditionFalse, "Error", msg)
	setCond(cr, rehearsalv1beta1.ConditionReady, metav1.ConditionFalse, "Error", msg)
	setCond(cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionTrue, "Error", msg)
	_ = r.Status().Update(ctx, cr)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// setCond updates a condition. lastTransitionTime changes only when status/reason/message change.
func setCond(cr *rehearsalv1beta1.RehearsalRun, typ string, status metav1.ConditionStatus, reason, msg string) {
	now := metav1.Now()
	for i := range cr.Status.Conditions {
		c := &cr.Status.Conditions[i]
		if c.Type != typ {
			continue
		}
		if c.Status == status && c.Reason == reason && c.Message == msg {
			// no transition — keep lastTransitionTime
			return
		}
		c.Status = status
		c.Reason = reason
		c.Message = msg
		c.LastTransitionTime = now
		return
	}
	cr.Status.Conditions = append(cr.Status.Conditions, metav1.Condition{
		Type: typ, Status: status, Reason: reason, Message: msg, LastTransitionTime: now,
	})
}

func isTerminal(phase string) bool {
	switch phase {
	case string(run.PhaseCompleted), string(run.PhaseFailed), string(run.PhaseInconclusive), string(run.PhaseCancelled):
		return true
	default:
		return false
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RehearsalRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rehearsalv1beta1.RehearsalRun{}).
		Complete(r)
}

// unused import guard for time (probes may need)
var _ = time.Second
