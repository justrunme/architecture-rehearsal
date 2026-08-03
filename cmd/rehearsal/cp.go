package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/api"
	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/chain"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/operator"
	"github.com/justrunme/architecture-rehearsal/internal/policy"
	"github.com/justrunme/architecture-rehearsal/internal/rbac"
	"github.com/justrunme/architecture-rehearsal/internal/run"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	srv := api.NewServer()
	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler()}
	go func() {
		fmt.Fprintf(os.Stderr, "architecture-rehearsal control plane listening on %s\n", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(5)
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	return 0
}

func cmdRun(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal run execute --baseline F --change F [--observed F] --out F")
		return 2
	}
	if args[0] != "execute" {
		return 2
	}
	if code := requireAction(rbac.ActionAnalyze); code != 0 {
		return code
	}
	fs := flag.NewFlagSet("run-execute", flag.ContinueOnError)
	id := fs.String("id", "local-run", "run id")
	base := fs.String("baseline", "", "baseline.json")
	ch := fs.String("change", "", "change.json")
	obs := fs.String("observed", "", "optional observed.json")
	out := fs.String("out", "out/run.json", "write RehearsalRun JSON")
	fs.SetOutput(os.Stderr)
	_ = fs.Parse(args[1:])
	if *base == "" || *ch == "" {
		fmt.Fprintln(os.Stderr, "--baseline and --change required")
		return 2
	}
	rr := run.NewRun(*id, *id, run.Spec{
		BaselineRef:    *base,
		ChangeRef:      *ch,
		ObservedRef:    *obs,
		TimeoutSeconds: 600,
		Gate:           run.GateSpec{BlockOn: []string{"critical", "high", "block"}},
	})
	eng := &run.Engine{Holder: rbac.ActorFromEnv()}
	_ = eng.Execute(rr)
	raw, _ := json.MarshalIndent(rr, "", "  ")
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		return 5
	}
	fmt.Fprintf(os.Stderr, "wrote %s phase=%s decision=%s\n", *out, rr.Status.Phase, rr.Status.Decision)
	if rr.Status.Phase == run.PhaseFailed {
		return 1
	}
	if rr.Status.Phase == run.PhaseInconclusive {
		return 4
	}
	if rr.Status.Decision == analyze.DecisionBlock {
		return 3
	}
	return 0
}

func cmdEvidence(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal evidence chain|verify-chain|sign-dsse ...")
		return 2
	}
	switch args[0] {
	case "chain":
		fs := flag.NewFlagSet("evidence-chain", flag.ContinueOnError)
		base := fs.String("baseline", "", "")
		chp := fs.String("change", "", "")
		rep := fs.String("report", "", "")
		obs := fs.String("observed", "", "")
		out := fs.String("out", "out/chain.json", "")
		_ = fs.Parse(args[1:])
		if *base == "" || *chp == "" || *rep == "" {
			return 2
		}
		chainObj, err := buildChainFiles(*base, *chp, *rep, *obs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		rawOut, _ := json.MarshalIndent(chainObj, "", "  ")
		if err := os.WriteFile(*out, rawOut, 0o644); err != nil {
			return 5
		}
		fmt.Fprintf(os.Stderr, "wrote evidence chain %s digests baseline=%s change=%s report=%s\n",
			*out, chainObj.Digests.BaselineDigest.Short(), chainObj.Digests.ChangeDigest.Short(), chainObj.Digests.ReportDigest.Short())
		return 0
	case "verify-chain":
		fs := flag.NewFlagSet("verify-chain", flag.ContinueOnError)
		chainPath := fs.String("chain", "", "")
		base := fs.String("baseline", "", "")
		chp := fs.String("change", "", "")
		rep := fs.String("report", "", "")
		obs := fs.String("observed", "", "")
		_ = fs.Parse(args[1:])
		raw, err := os.ReadFile(*chainPath)
		if err != nil {
			return 2
		}
		var c chain.EvidenceChain
		if err := json.Unmarshal(raw, &c); err != nil {
			return 2
		}
		b, err := loader.LoadSnapshot(*base)
		if err != nil {
			return 2
		}
		change, err := loader.LoadChange(*chp)
		if err != nil {
			return 2
		}
		rb, err := os.ReadFile(*rep)
		if err != nil {
			return 2
		}
		var report analyze.Report
		if err := json.Unmarshal(rb, &report); err != nil {
			return 2
		}
		obsSnap, err := optionalSnapshot(*obs)
		if err != nil {
			return 2
		}
		if err := chain.VerifyChain(&c, b, change, &report, obsSnap); err != nil {
			fmt.Fprintf(os.Stderr, "INVALID: %v\n", err)
			return 1
		}
		fmt.Println("chain OK")
		return 0
	case "sign-dsse":
		if code := requireAction(rbac.ActionSign); code != 0 {
			return code
		}
		fs := flag.NewFlagSet("sign-dsse", flag.ContinueOnError)
		payload := fs.String("payload", "", "JSON file to sign")
		out := fs.String("out", "out/evidence-dsse.json", "")
		_ = fs.Parse(args[1:])
		raw, err := os.ReadFile(*payload)
		if err != nil {
			return 2
		}
		sec := evidence.SecretFromEnv()
		if len(sec) == 0 {
			fmt.Fprintln(os.Stderr, "REHEARSAL_HMAC_SECRET required")
			return 2
		}
		env, err := evidence.SignDSSEHMAC("application/vnd.rehearsal.report+json", raw, sec, "hmac", contract.ArtifactDigests{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		b, _ := json.MarshalIndent(env, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return 5
		}
		ok, _ := evidence.VerifyDSSE(env, sec, nil)
		fmt.Fprintf(os.Stderr, "wrote %s verified=%v\n", *out, ok)
		return 0
	default:
		return 2
	}
}

func buildChainFiles(basePath, changePath, reportPath, obsPath string) (*chain.EvidenceChain, error) {
	b, err := loader.LoadSnapshot(basePath)
	if err != nil {
		return nil, err
	}
	c, err := loader.LoadChange(changePath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, err
	}
	var rep analyze.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, err
	}
	obs, err := optionalSnapshot(obsPath)
	if err != nil {
		return nil, err
	}
	return chain.Build(b, c, nil, &rep, obs, nil)
}

func optionalSnapshot(path string) (*graph.Snapshot, error) {
	if path == "" {
		return nil, nil
	}
	return loader.LoadSnapshot(path)
}

func cmdPolicy(args []string) int {
	fs := flag.NewFlagSet("policy", flag.ContinueOnError)
	path := fs.String("file", "", "policy YAML")
	risk := fs.String("risk", "high", "")
	decision := fs.String("decision", "block", "")
	rollback := fs.String("rollback", "unknown", "")
	missing := fs.Int("required-missing", 0, "")
	_ = fs.Parse(args)
	doc, err := policy.Load(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	res := policy.Evaluate(doc, policy.Input{
		Risk: *risk, Decision: *decision, Rollback: *rollback, RequiredMissing: *missing,
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	switch res.Decision {
	case "block":
		return 3
	case "warn":
		return 1
	case "unknown":
		return 4
	default:
		return 0
	}
}

func cmdCalibrate(args []string) int {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	demo := fs.Bool("demo", false, "record demo outcomes")
	_ = fs.Parse(args)
	st := calibrate.NewStore()
	if *demo {
		st.Record(calibrate.Outcome{Scenario: "rwo-node-loss", Predicted: true, Observed: true, Verified: true})
		st.Record(calibrate.Outcome{Scenario: "rwo-node-loss", Predicted: true, Observed: false, Verified: true})
		st.Record(calibrate.Outcome{Scenario: "cni-ip-capacity", Predicted: true, Observed: true, Verified: true})
		st.Record(calibrate.Outcome{Scenario: "cni-ip-capacity", Predicted: false, Observed: false, Verified: true})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(st.Report())
	return 0
}

func cmdSchemas(args []string) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"catalog":  contract.Catalog(),
		"packages": scenario.BuiltinPackages(),
	})
	return 0
}

func cmdOperator(args []string) int {
	fs := flag.NewFlagSet("operator", flag.ContinueOnError)
	dir := fs.String("watch", "out/runs-cr", "directory of RehearsalRun JSON")
	once := fs.Bool("once", true, "reconcile once and exit")
	_ = fs.Parse(args)
	_ = os.MkdirAll(*dir, 0o755)
	rec := &operator.Reconciler{WatchDir: *dir, Holder: "operator"}
	if *once {
		n, err := rec.ReconcileOnce()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		fmt.Fprintf(os.Stderr, "reconciled %d runs\n", n)
		return 0
	}
	stop := make(chan struct{})
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		close(stop)
	}()
	rec.RunLoop(stop, 5*time.Second)
	return 0
}
