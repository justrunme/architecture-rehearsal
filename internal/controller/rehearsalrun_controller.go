// Package controller implements the Kubernetes RehearsalRun reconciler (v1.8.0).
// Trust boundary: control plane URL and token come ONLY from operator deployment.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	rehearsalv1beta1 "github.com/justrunme/architecture-rehearsal/api/v1beta1"
	"github.com/justrunme/architecture-rehearsal/internal/operator"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// FinalizerCancelRun cancels the control-plane run before CR deletion completes.
const FinalizerCancelRun = "rehearsal.io/cancel-run"

// PermanentError is a non-retryable reconcile failure (wait for generation change).
type PermanentError struct {
	Reason  string
	Message string
}

func (e *PermanentError) Error() string {
	if e == nil {
		return "permanent error"
	}
	return e.Message
}

// IsPermanent reports whether err should not be requeued.
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	var pe *PermanentError
	if errors.As(err, &pe) {
		return true
	}
	return operator.IsPermanent(err)
}

// RehearsalRunReconciler reconciles RehearsalRun CRs against the control plane.
// APIBase and Token MUST be set from deployment env — never from CR.
type RehearsalRunReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// APIBase is REHEARSAL_API_URL (deployment-only).
	APIBase string
	// Token is REHEARSAL_API_TOKEN (from Secret).
	Token string
	// ClientFactory builds a control-plane client (tests inject).
	ClientFactory func(base, token string) *operator.ControlPlaneClient
}

// Least-privilege: no create/delete of CRs — only watch + status update.
// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *RehearsalRunReconciler) cpClient(base, token string) *operator.ControlPlaneClient {
	if r.ClientFactory != nil {
		return r.ClientFactory(base, token)
	}
	return &operator.ControlPlaneClient{BaseURL: base, Token: token}
}

func (r *RehearsalRunReconciler) event(obj runtime.Object, eventtype, reason, msg string) {
	if r.Recorder == nil || obj == nil {
		return
	}
	r.Recorder.Event(obj, eventtype, reason, msg)
}

// ControlPlaneRunID builds an immutable run id including CR UID (survives delete/recreate).
// Format: {namespace}-{name}-{uid8}-g{generation}
func ControlPlaneRunID(namespace, name, uid string, generation int64) string {
	uidShort := string(uid)
	if len(uidShort) > 8 {
		uidShort = uidShort[:8]
	}
	if uidShort == "" {
		uidShort = "nouid"
	}
	// sanitize name for id safety
	name = strings.ReplaceAll(name, "/", "-")
	return fmt.Sprintf("%s-%s-%s-g%d", namespace, name, uidShort, generation)
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
	token := r.Token
	if token == "" {
		token = os.Getenv("REHEARSAL_API_TOKEN")
	}

	// Deletion: cancel control-plane run then drop finalizer (idempotent).
	if !cr.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &cr, apiURL, token)
	}

	if !controllerutil.ContainsFinalizer(&cr, FinalizerCancelRun) {
		controllerutil.AddFinalizer(&cr, FinalizerCancelRun)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if apiURL == "" {
		return r.fail(ctx, &cr, &PermanentError{
			Reason:  "InvalidConfig",
			Message: "REHEARSAL_API_URL required (set only on operator Deployment, never on CR)",
		})
	}
	if token == "" {
		return r.fail(ctx, &cr, &PermanentError{
			Reason:  "InvalidConfig",
			Message: "REHEARSAL_API_TOKEN required (Secret mount)",
		})
	}

	setCond(&cr, rehearsalv1beta1.ConditionAccepted, metav1.ConditionTrue, "Accepted", "spec accepted by operator")
	setCond(&cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionFalse, "None", "")

	digest, err := SpecDigest(cr.Spec)
	if err != nil {
		return r.fail(ctx, &cr, &PermanentError{Reason: "InvalidSpec", Message: "spec digest: " + err.Error()})
	}

	runID := ControlPlaneRunID(cr.Namespace, cr.Name, string(cr.UID), cr.Generation)
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
		"k8s.uid":       string(cr.UID),
		"specDigest":    digest,
	}

	ens, err := cp.EnsureRun(rr)
	if err != nil {
		logger.Error(err, "ensure run")
		return r.fail(ctx, &cr, err)
	}
	// Always validate existing run matches Spec (200 idempotent + 409 conflict paths).
	if ens != nil && ens.Run != nil && !specMatchesRun(cr.Spec, ens.Run) {
		return r.fail(ctx, &cr, &PermanentError{
			Reason: "SpecConflict",
			Message: fmt.Sprintf(
				"control plane run %q does not match CR spec (immutable); delete CR or use a new name", runID),
		})
	}

	async := true
	if cr.Spec.Async != nil {
		async = *cr.Spec.Async
	}

	alreadyAdvanced := sameGen && cr.Status.JobID != ""
	existingTerminal := ens.Run != nil && ens.Run.Status.Phase.Terminal()
	needAdvance := !alreadyAdvanced && !existingTerminal &&
		(cr.Status.Phase == "" || !isTerminal(cr.Status.Phase) || !sameGen)

	prevPhase := cr.Status.Phase
	if needAdvance {
		adv, err := cp.Advance(runID, async)
		if err != nil {
			logger.Error(err, "advance")
			return r.fail(ctx, &cr, err)
		}
		if adv != nil && adv.JobID != "" {
			cr.Status.JobID = adv.JobID
		}
		setCond(&cr, rehearsalv1beta1.ConditionRunning, metav1.ConditionTrue, "Advancing", "control plane advance requested")
		r.event(&cr, corev1.EventTypeNormal, "RunAdvanced", "control plane advance requested for "+runID)
	}

	latest, err := cp.GetRun(runID)
	if err != nil {
		if IsPermanent(err) {
			return r.fail(ctx, &cr, err)
		}
		return ctrl.Result{}, err
	}
	// Re-check after get (race with external mutation)
	if !specMatchesRun(cr.Spec, latest) {
		return r.fail(ctx, &cr, &PermanentError{
			Reason:  "SpecConflict",
			Message: fmt.Sprintf("control plane run %q drifted from CR spec", runID),
		})
	}

	cr.Status.ControlPlaneRunID = runID
	cr.Status.Phase = string(latest.Status.Phase)
	cr.Status.Decision = latest.Status.Decision
	cr.Status.Risk = latest.Status.Risk
	cr.Status.Message = latest.Status.Message
	cr.Status.ObservedGeneration = cr.Generation
	cr.Status.SpecDigest = digest
	cr.Status.EvidenceDigest = evidenceFromRun(latest)

	if prevPhase != cr.Status.Phase && cr.Status.Phase != "" {
		r.event(&cr, corev1.EventTypeNormal, "PhaseTransition",
			fmt.Sprintf("phase %s → %s", emptyAsNone(prevPhase), cr.Status.Phase))
	}

	if isTerminal(cr.Status.Phase) {
		setCond(&cr, rehearsalv1beta1.ConditionRunning, metav1.ConditionFalse, "Terminal", "run finished")
		switch cr.Status.Phase {
		case string(run.PhaseFailed):
			setCond(&cr, rehearsalv1beta1.ConditionReady, metav1.ConditionFalse, "Failed", latest.Status.Message)
			setCond(&cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionTrue, "Failed", latest.Status.Message)
			r.event(&cr, corev1.EventTypeWarning, "RunFailed", latest.Status.Message)
		case string(run.PhaseInconclusive):
			// Inconclusive must not report Ready=True — distinct reason for operators.
			setCond(&cr, rehearsalv1beta1.ConditionReady, metav1.ConditionFalse, "Inconclusive", latest.Status.Message)
			setCond(&cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionFalse, "Inconclusive", latest.Status.Message)
			r.event(&cr, corev1.EventTypeWarning, "RunInconclusive", latest.Status.Message)
		case string(run.PhaseCancelled):
			setCond(&cr, rehearsalv1beta1.ConditionReady, metav1.ConditionFalse, "Cancelled", latest.Status.Message)
			setCond(&cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionFalse, "Cancelled", latest.Status.Message)
			r.event(&cr, corev1.EventTypeNormal, "RunCancelled", latest.Status.Message)
		default:
			setCond(&cr, rehearsalv1beta1.ConditionReady, metav1.ConditionTrue, "Complete", "run reached terminal phase")
			setCond(&cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionFalse, "None", "")
			r.event(&cr, corev1.EventTypeNormal, "RunCompleted", "run reached terminal phase "+cr.Status.Phase)
		}
	} else {
		setCond(&cr, rehearsalv1beta1.ConditionRunning, metav1.ConditionTrue, "InProgress", "run not terminal")
		setCond(&cr, rehearsalv1beta1.ConditionReady, metav1.ConditionFalse, "InProgress", "waiting for control plane")
	}

	if err := r.patchStatus(ctx, &cr); err != nil {
		return ctrl.Result{}, err
	}
	if isTerminal(cr.Status.Phase) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *RehearsalRunReconciler) reconcileDelete(ctx context.Context, cr *rehearsalv1beta1.RehearsalRun, apiURL, token string) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, FinalizerCancelRun) {
		return ctrl.Result{}, nil
	}
	runID := cr.Status.ControlPlaneRunID
	if runID == "" {
		runID = ControlPlaneRunID(cr.Namespace, cr.Name, string(cr.UID), cr.Generation)
	}
	if apiURL != "" && token != "" && runID != "" {
		cp := r.cpClient(apiURL, token)
		if err := cp.Cancel(runID); err != nil {
			if IsPermanent(err) && !operator.IsNotFound(err) {
				// Still remove finalizer on permanent cancel failures to avoid stuck CRs;
				// log via event and continue.
				r.event(cr, corev1.EventTypeWarning, "CancelFailed", err.Error())
			} else if !IsPermanent(err) {
				return ctrl.Result{}, err
			}
		} else {
			r.event(cr, corev1.EventTypeNormal, "RunCancelled", "cancelled control plane run "+runID)
		}
	}
	controllerutil.RemoveFinalizer(cr, FinalizerCancelRun)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func emptyAsNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func (r *RehearsalRunReconciler) patchStatus(ctx context.Context, cr *rehearsalv1beta1.RehearsalRun) error {
	var latest rehearsalv1beta1.RehearsalRun
	if err := r.Get(ctx, client.ObjectKeyFromObject(cr), &latest); err != nil {
		return err
	}
	base := latest.DeepCopy()
	latest.Status = cr.Status
	return r.Status().Patch(ctx, &latest, client.MergeFrom(base))
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
	if !refMatches(spec.BaselineRef, rr.Spec.BaselineRef) ||
		!refMatches(spec.ChangeRef, rr.Spec.ChangeRef) ||
		!refMatches(spec.ObservedRef, rr.Spec.ObservedRef) ||
		rr.Spec.ClusterName != spec.ClusterName {
		return false
	}
	return equalStringSlices(rr.Spec.Scenarios, spec.Scenarios)
}

// refMatches compares a CR ref to the control-plane stored ref.
// createRun sandboxes relative paths to absolute workdir paths (e.g. baseline.json → /data/baseline.json).
func refMatches(crRef, stored string) bool {
	if crRef == stored {
		return true
	}
	if crRef == "" || stored == "" {
		return crRef == stored
	}
	cr := filepath.Clean(crRef)
	st := filepath.Clean(stored)
	if cr == st {
		return true
	}
	// Stored is workdir-absolute form of a relative CR ref.
	if !filepath.IsAbs(cr) {
		sep := string(filepath.Separator)
		if strings.HasSuffix(st, sep+cr) {
			return true
		}
	}
	return false
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *RehearsalRunReconciler) fail(ctx context.Context, cr *rehearsalv1beta1.RehearsalRun, failErr error) (ctrl.Result, error) {
	if failErr == nil {
		failErr = &PermanentError{Reason: "Error", Message: "unknown failure"}
	}
	msg := failErr.Error()
	reason := "Error"
	permanent := IsPermanent(failErr)
	var pe *PermanentError
	if errors.As(failErr, &pe) && pe.Reason != "" {
		reason = pe.Reason
	} else if permanent {
		reason = "PermanentError"
	}
	if reason == "SpecConflict" {
		r.event(cr, corev1.EventTypeWarning, "SpecConflict", msg)
	} else if permanent {
		r.event(cr, corev1.EventTypeWarning, reason, msg)
	} else {
		r.event(cr, corev1.EventTypeWarning, "TransientError", msg)
	}

	cr.Status.Message = msg
	setCond(cr, rehearsalv1beta1.ConditionAccepted, metav1.ConditionTrue, "Accepted", "spec accepted")
	setCond(cr, rehearsalv1beta1.ConditionRunning, metav1.ConditionFalse, reason, msg)
	setCond(cr, rehearsalv1beta1.ConditionReady, metav1.ConditionFalse, reason, msg)
	setCond(cr, rehearsalv1beta1.ConditionFailed, metav1.ConditionTrue, reason, msg)
	if err := r.patchStatus(ctx, cr); err != nil {
		return ctrl.Result{}, err
	}
	if permanent {
		// Wait for generation change; do not hot-loop.
		return ctrl.Result{}, nil
	}
	// Transient: return error so the workqueue applies exponential backoff.
	return ctrl.Result{}, failErr
}

// setCond updates a condition via meta.SetStatusCondition (ObservedGeneration, stable transitions).
func setCond(cr *rehearsalv1beta1.RehearsalRun, typ string, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: cr.Generation,
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

// deletionOrGenerationChanged reconciles on spec generation changes and on deletion.
func deletionOrGenerationChanged() predicate.Predicate {
	return predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				if e.ObjectNew == nil {
					return false
				}
				return !e.ObjectNew.GetDeletionTimestamp().IsZero()
			},
		},
	)
}

// SetupWithManager sets up the controller with the Manager.
func (r *RehearsalRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("rehearsalrun-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&rehearsalv1beta1.RehearsalRun{}).
		WithEventFilter(deletionOrGenerationChanged()).
		Complete(r)
}
