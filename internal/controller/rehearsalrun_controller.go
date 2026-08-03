// Package controller implements the Kubernetes RehearsalRun reconciler (v1.5).
package controller

import (
	"context"
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
type RehearsalRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// DefaultAPI is REHEARSAL_API_URL.
	DefaultAPI string
	// Token is REHEARSAL_API_TOKEN.
	Token string
}

// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rehearsal.io,resources=rehearsalruns/finalizers,verbs=update

func (r *RehearsalRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var cr rehearsalv1beta1.RehearsalRun
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	apiURL := cr.Spec.ControlPlaneURL
	if apiURL == "" {
		apiURL = r.DefaultAPI
	}
	if apiURL == "" {
		apiURL = os.Getenv("REHEARSAL_API_URL")
	}
	if apiURL == "" {
		return r.fail(&cr, "ControlPlaneURL or REHEARSAL_API_URL required")
	}
	token := r.Token
	if token == "" {
		token = os.Getenv("REHEARSAL_API_TOKEN")
	}
	if token == "" {
		return r.fail(&cr, "REHEARSAL_API_TOKEN required")
	}

	runID := cr.Status.ControlPlaneRunID
	if runID == "" {
		runID = fmt.Sprintf("%s-%s", cr.Namespace, cr.Name)
	}

	cp := &operator.ControlPlaneClient{BaseURL: apiURL, Token: token}
	rr := run.NewRun(runID, runID, run.Spec{
		BaselineRef: cr.Spec.BaselineRef,
		ChangeRef:   cr.Spec.ChangeRef,
		ObservedRef: cr.Spec.ObservedRef,
		ClusterName: cr.Spec.ClusterName,
		Scenarios:   cr.Spec.Scenarios,
	})
	if err := cp.EnsureRun(rr); err != nil {
		logger.Error(err, "ensure run")
		return r.fail(&cr, err.Error())
	}

	async := true
	if cr.Spec.Async != nil {
		async = *cr.Spec.Async
	}
	// Only advance non-terminal
	if cr.Status.Phase == "" || !isTerminal(cr.Status.Phase) {
		if err := cp.Advance(runID, async); err != nil {
			logger.Error(err, "advance")
			return r.fail(&cr, err.Error())
		}
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
	setCond(&cr, "Ready", metav1.ConditionTrue, "Synced", "synced with control plane")
	if err := r.Status().Update(ctx, &cr); err != nil {
		return ctrl.Result{}, err
	}
	if isTerminal(cr.Status.Phase) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *RehearsalRunReconciler) fail(cr *rehearsalv1beta1.RehearsalRun, msg string) (ctrl.Result, error) {
	cr.Status.Message = msg
	setCond(cr, "Ready", metav1.ConditionFalse, "Error", msg)
	_ = r.Status().Update(context.Background(), cr)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func setCond(cr *rehearsalv1beta1.RehearsalRun, typ string, status metav1.ConditionStatus, reason, msg string) {
	now := metav1.Now()
	for i := range cr.Status.Conditions {
		if cr.Status.Conditions[i].Type == typ {
			cr.Status.Conditions[i].Status = status
			cr.Status.Conditions[i].Reason = reason
			cr.Status.Conditions[i].Message = msg
			cr.Status.Conditions[i].LastTransitionTime = now
			return
		}
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
