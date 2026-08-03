package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/api"
	"github.com/justrunme/architecture-rehearsal/internal/authn"
	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/chain"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/integrations/status"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/operator"
	"github.com/justrunme/architecture-rehearsal/internal/persist"
	"github.com/justrunme/architecture-rehearsal/internal/policy"
	"github.com/justrunme/architecture-rehearsal/internal/rbac"
	"github.com/justrunme/architecture-rehearsal/internal/run"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	workdir := fs.String("workdir", "", "REQUIRED workspace root for file refs (sandbox)")
	dbDSN := fs.String("db", "", "SQLite path or postgres:// URL")
	blobDir := fs.String("blob", "", "content-addressed blob root (default: <workdir>/blobs)")
	memory := fs.Bool("memory", false, "in-process store (tests/dev; no durability)")
	async := fs.Bool("async", false, "enqueue run advances as durable jobs (requires SQL backend)")
	workers := fs.Int("workers", 1, "number of background job workers when --async")
	insecureDev := fs.Bool("insecure-dev", false, "allow local-dev token (NEVER in production)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *insecureDev {
		_ = os.Setenv("REHEARSAL_ALLOW_INSECURE_DEV", "1")
	}
	// --workdir is mandatory for serve (path sandbox + policy snapshots).
	if *workdir == "" {
		fmt.Fprintln(os.Stderr, "serve refused: --workdir is required (workspace sandbox root)")
		return 2
	}
	absWD, err := filepath.Abs(*workdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "workdir: %v\n", err)
		return 2
	}
	if err := os.MkdirAll(absWD, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "workdir mkdir: %v\n", err)
		return 2
	}
	*workdir = absWD

	// REHEARSAL_DATABASE_URL always wins over --db (so Helm can pass --db for SQLite
	// while still selecting Postgres via env).
	if env := os.Getenv("REHEARSAL_DATABASE_URL"); env != "" {
		*dbDSN = env
	}
	if env := os.Getenv("REHEARSAL_BLOB_ROOT"); env != "" && *blobDir == "" {
		*blobDir = env
	}

	auth, err := authnFromEnvServe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve refused: %v\n", err)
		fmt.Fprintln(os.Stderr, "set REHEARSAL_API_TOKEN to a strong secret, or pass --insecure-dev for local only")
		return 2
	}

	var backend api.Backend
	var store *persist.Store
	// Default durable path: SQLite under workdir
	if !*memory && *dbDSN == "" {
		*dbDSN = filepath.Join(*workdir, "rehearsal.db")
	}
	if *memory {
		backend = api.NewMemoryBackend()
		fmt.Fprintln(os.Stderr, "backend: memory (non-durable)")
	} else {
		if *blobDir == "" {
			*blobDir = filepath.Join(*workdir, "blobs")
		}
		store, err = persist.Open(*dbDSN, *blobDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open store: %v\n", err)
			return 5
		}
		defer store.Close()
		// Optional MinIO/S3 path-style backend (reference; not full AWS SigV4).
		// Prefer REHEARSAL_S3_ENDPOINT for multi-replica evidence blobs.
		if ep := os.Getenv("REHEARSAL_S3_ENDPOINT"); ep != "" {
			sb, err := persist.NewS3Blob(ep, os.Getenv("REHEARSAL_S3_BUCKET"),
				os.Getenv("REHEARSAL_S3_ACCESS_KEY"), os.Getenv("REHEARSAL_S3_SECRET_KEY"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "s3 blob: %v\n", err)
				return 5
			}
			store.Blob = sb
			fmt.Fprintf(os.Stderr, "blob: s3-compatible endpoint=%s bucket=%s (MinIO path-style; not full AWS SigV4)\n",
				ep, os.Getenv("REHEARSAL_S3_BUCKET"))
		}
		backend = &api.SQLBackend{S: store}
		fmt.Fprintf(os.Stderr, "backend: sql dsn=%s blob=%s\n", redactDSN(*dbDSN), *blobDir)
	}

	srv := api.NewServerWith(api.Options{
		AuthN:          auth,
		Backend:        backend,
		WorkDirRoot:    *workdir,
		Async:          *async && store != nil,
		RequireWorkDir: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if store != nil && *async {
		n := *workers
		if n < 1 {
			n = 1
		}
		for i := 0; i < n; i++ {
			w := &persist.Worker{
				Store:    store,
				Holder:   fmt.Sprintf("worker-%d", i+1),
				WorkDir:  *workdir,
				Interval: 300 * time.Millisecond,
				LeaseTTL: 15 * time.Minute, // covers 10m run timeout + margin
			}
			go w.Run(ctx)
		}
		fmt.Fprintf(os.Stderr, "workers: %d (async advance, lease TTL 15m + heartbeat)\n", n)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		fmt.Fprintf(os.Stderr, "architecture-rehearsal control plane listening on %s (secure token required)\n", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(5)
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	cancel()
	shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shCancel()
	_ = httpSrv.Shutdown(shCtx)
	return 0
}

func redactDSN(dsn string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		// hide password
		if i := strings.Index(dsn, "@"); i > 0 {
			if j := strings.Index(dsn, "://"); j >= 0 {
				userPart := dsn[j+3 : i]
				if k := strings.Index(userPart, ":"); k >= 0 {
					return dsn[:j+3] + userPart[:k] + ":***" + dsn[i:]
				}
			}
		}
	}
	return dsn
}

func authnFromEnvServe() (authn.Authenticator, error) {
	return authn.FromEnvAuthenticator(authn.Config{RequireToken: true})
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
		OutDir:         filepath.Join("out", *id),
		TimeoutSeconds: 600,
		Gate:           run.GateSpec{BlockOn: []string{"critical", "high", "block"}},
	})
	eng := &run.Engine{Holder: rbac.ActorFromEnv(), Calibrate: calibrate.NewStore()}
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
		fmt.Fprintln(os.Stderr, "usage: rehearsal evidence chain|verify-chain|sign-dsse|verify-dsse ...")
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
		chainPath := fs.String("chain", "", "evidence-chain.json (preferred)")
		payload := fs.String("payload", "", "legacy: raw JSON file")
		out := fs.String("out", "out/evidence-dsse.json", "")
		_ = fs.Parse(args[1:])
		sec := evidence.SecretFromEnv()
		if len(sec) == 0 {
			fmt.Fprintln(os.Stderr, "REHEARSAL_HMAC_SECRET required")
			return 2
		}
		var env *evidence.DSSEEnvelope
		var err error
		if *chainPath != "" {
			raw, err := os.ReadFile(*chainPath)
			if err != nil {
				return 2
			}
			var chDoc struct {
				ChangeID string                   `json:"changeId"`
				Decision string                   `json:"decision"`
				Risk     string                   `json:"risk"`
				Digests  contract.ArtifactDigests `json:"digests"`
			}
			if err := json.Unmarshal(raw, &chDoc); err != nil {
				return 2
			}
			stmt := evidence.EvidenceStatement{
				ChangeID: chDoc.ChangeID, Decision: chDoc.Decision, Risk: chDoc.Risk,
				ChainDigests: chDoc.Digests, KeyID: "hmac-env",
			}
			env, err = evidence.SignEvidenceStatement(stmt, sec)
		} else if *payload != "" {
			raw, err := os.ReadFile(*payload)
			if err != nil {
				return 2
			}
			// Wrap raw payload as extra inside statement so digests can still be empty but body signed
			stmt := evidence.EvidenceStatement{Extra: raw, KeyID: "hmac-env"}
			env, err = evidence.SignEvidenceStatement(stmt, sec)
		} else {
			fmt.Fprintln(os.Stderr, "--chain or --payload required")
			return 2
		}
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
	case "verify-dsse":
		fs := flag.NewFlagSet("verify-dsse", flag.ContinueOnError)
		path := fs.String("envelope", "", "evidence-dsse.json")
		_ = fs.Parse(args[1:])
		if *path == "" {
			return 2
		}
		raw, err := os.ReadFile(*path)
		if err != nil {
			return 2
		}
		var env evidence.DSSEEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return 2
		}
		sec := evidence.SecretFromEnv()
		ok, err := evidence.VerifyDSSE(&env, sec, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "signature INVALID")
			return 1
		}
		stmt, err := evidence.ParseStatement(&env)
		if err != nil {
			fmt.Fprintf(os.Stderr, "payload parse: %v\n", err)
			return 1
		}
		fmt.Printf("signature OK keyId=%s changeId=%s baseline=%s change=%s\n",
			stmt.KeyID, stmt.ChangeID, stmt.ChainDigests.BaselineDigest.Short(), stmt.ChainDigests.ChangeDigest.Short())
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
	workdir := fs.String("workdir", "", "engine workdir for local reconcile")
	once := fs.Bool("once", true, "reconcile once and exit")
	apiURL := fs.String("api", "", "control plane base URL (optional; syncs CR → HTTP API)")
	token := fs.String("token", os.Getenv("REHEARSAL_API_TOKEN"), "API bearer token")
	async := fs.Bool("async", true, "async advance when using --api")
	_ = fs.Parse(args)
	_ = os.MkdirAll(*dir, 0o755)
	rec := &operator.Reconciler{WatchDir: *dir, WorkDir: *workdir, Holder: "operator", AsyncAdvance: *async}
	if *apiURL != "" {
		rec.ControlPlane = &operator.ControlPlaneClient{BaseURL: *apiURL, Token: *token}
	}
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

func cmdStatus(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal status github|gitlab --sha SHA --state success|failure|pending --description TEXT")
		return 2
	}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	sha := fs.String("sha", "", "commit sha")
	state := fs.String("state", "success", "success|failure|pending|error")
	desc := fs.String("description", "architecture-rehearsal", "status description")
	url := fs.String("url", "", "target URL")
	decision := fs.String("decision", "", "optional: map approve|warn|block")
	exitCode := fs.Int("exit-code", -1, "optional analyze exit code for mapping")
	_ = fs.Parse(args[1:])
	if *sha == "" {
		*sha = os.Getenv("GITHUB_SHA")
	}
	if *sha == "" {
		*sha = os.Getenv("CI_COMMIT_SHA")
	}
	if *decision != "" || *exitCode >= 0 {
		st, _ := status.MapDecision(*decision, *exitCode)
		*state = st
	}
	st := status.Status{State: *state, Description: *desc, TargetURL: *url}
	switch args[0] {
	case "github":
		if *sha == "" {
			fmt.Fprintln(os.Stderr, "--sha required")
			return 2
		}
		g, err := status.FromEnvGitHub()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		if err := g.PostCommitStatus(*sha, st); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		fmt.Fprintf(os.Stderr, "github status %s on %s\n", st.State, *sha)
		return 0
	case "gitlab":
		if *sha == "" {
			fmt.Fprintln(os.Stderr, "--sha required")
			return 2
		}
		g, err := status.FromEnvGitLab()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		if err := g.PostCommitStatus(*sha, st); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		fmt.Fprintf(os.Stderr, "gitlab status %s on %s\n", st.State, *sha)
		return 0
	default:
		return 2
	}
}

func cmdBackup(args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	db := fs.String("db", "data/rehearsal.db", "sqlite db path")
	out := fs.String("out", "data/rehearsal-backup.db", "backup destination")
	_ = fs.Parse(args)
	s, err := persist.Open(*db, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		return 5
	}
	defer s.Close()
	if err := s.BackupSQLite(*db, *out); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 5
	}
	fmt.Fprintf(os.Stderr, "backed up %s -> %s schema=%d\n", *db, *out, s.SchemaVersion())
	return 0
}
