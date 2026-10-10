// rehearsal-operator is a controller-runtime manager for RehearsalRun CRs (v1.5).
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	rehearsalv1beta1 "github.com/justrunme/architecture-rehearsal/api/v1beta1"
	"github.com/justrunme/architecture-rehearsal/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(rehearsalv1beta1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8082", "metrics")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "leader election")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "rehearsal.io",
	})
	if err != nil {
		ctrl.Log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.RehearsalRunReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		APIBase: os.Getenv("REHEARSAL_API_URL"), // deployment-only; CR cannot override
		Token:   os.Getenv("REHEARSAL_API_TOKEN"),
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to create controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "healthz")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "readyz")
		os.Exit(1)
	}

	ctrl.Log.Info("starting rehearsal-operator v1.5.3 (API URL from env only)")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "manager exit")
		os.Exit(1)
	}
}
