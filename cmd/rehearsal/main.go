// rehearsal — production change-gate CLI for Architecture Rehearsal.
// Graph and rules decide. AI only explains (optional, not in risk path).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/report"
	"github.com/justrunme/architecture-rehearsal/internal/validate"
	"github.com/justrunme/architecture-rehearsal/internal/verify"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "analyze":
		code = cmdAnalyze(os.Args[2:])
	case "verify":
		code = cmdVerify(os.Args[2:])
	case "snapshot":
		code = cmdSnapshot(os.Args[2:])
	case "change":
		code = cmdChange(os.Args[2:])
	case "version":
		fmt.Println("rehearsal", analyze.Version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Architecture Rehearsal — know what breaks before you deploy

Usage:
  rehearsal analyze  --baseline FILE --change FILE [flags]
  rehearsal verify   --report FILE --observed FILE
  rehearsal snapshot k8s --dir MANIFESTS [--cluster NAME] --out FILE
  rehearsal change   manifests --baseline FILE --dir RENDERED --out FILE
  rehearsal change   terraform --plan FILE --out FILE
  rehearsal version

Exit codes:
  0  approve / verified
  1  warn / diverged
  2  usage or validation error
  3  block
  4  unknown (insufficient evidence)
  5  internal error

Graph and rules decide. Missing data never becomes false approve.
`)
}

func cmdAnalyze(args []string) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	baseline := fs.String("baseline", "", "baseline snapshot JSON")
	changePath := fs.String("change", "", "change envelope JSON")
	outDir := fs.String("out", "out", "evidence output directory")
	htmlPath := fs.String("html", "", "optional extra HTML path")
	quiet := fs.Bool("quiet", false, "compact summary only")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *baseline == "" || *changePath == "" {
		fmt.Fprintln(os.Stderr, "error: --baseline and --change are required")
		return 2
	}
	base, err := loader.LoadSnapshot(*baseline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
		return 2
	}
	ch, err := loader.LoadChange(*changePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "change: %v\n", err)
		return 2
	}
	if err := validate.ChangeAgainstBaseline(base, ch); err != nil {
		fmt.Fprintf(os.Stderr, "validate: %v\n", err)
		return 2
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analyze: %v\n", err)
		return 2
	}
	dir, err := evidence.Bundle(*outDir, rep, abs(*baseline), abs(*changePath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "evidence: %v\n", err)
		return 5
	}
	if *htmlPath != "" {
		if err := report.WriteHTML(*htmlPath, rep); err != nil {
			fmt.Fprintf(os.Stderr, "html: %v\n", err)
			return 5
		}
	}
	if *quiet {
		fmt.Printf("risk=%s decision=%s findings=%d rollback=%s digest=%s evidence=%s\n",
			rep.Risk, rep.Decision, len(rep.Findings), rep.Rollback, rep.SemanticDigest, dir)
	} else {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		fmt.Fprintf(os.Stderr, "evidence: %s\n", dir)
	}
	switch rep.Decision {
	case analyze.DecisionBlock:
		return 3
	case analyze.DecisionWarn:
		return 1
	case analyze.DecisionUnknown:
		return 4
	default:
		return 0
	}
}

func cmdVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	reportPath := fs.String("report", "", "prior analyze report.json")
	observed := fs.String("observed", "", "post-deploy snapshot JSON")
	out := fs.String("out", "", "write verification JSON")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *reportPath == "" || *observed == "" {
		fmt.Fprintln(os.Stderr, "error: --report and --observed required")
		return 2
	}
	raw, err := os.ReadFile(*reportPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return 2
	}
	var pred analyze.Report
	if err := json.Unmarshal(raw, &pred); err != nil {
		fmt.Fprintf(os.Stderr, "report parse: %v\n", err)
		return 2
	}
	obs, err := loader.LoadSnapshot(*observed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observed: %v\n", err)
		return 2
	}
	res := verify.Run(&pred, obs)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	if *out != "" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return 5
		}
	}
	switch res.Outcome {
	case verify.OutcomeVerified:
		return 0
	case verify.OutcomeInconclusive:
		return 4
	default:
		return 1
	}
}

func cmdSnapshot(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal snapshot k8s --dir DIR --out FILE")
		return 2
	}
	switch args[0] {
	case "k8s":
		fs := flag.NewFlagSet("snapshot-k8s", flag.ContinueOnError)
		dir := fs.String("dir", "", "directory of Kubernetes YAML manifests")
		cluster := fs.String("cluster", "acme-prod", "cluster name label")
		out := fs.String("out", "baseline.json", "output snapshot path")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *dir == "" {
			fmt.Fprintln(os.Stderr, "--dir required")
			return 2
		}
		snap, err := collect.K8sFromManifests(nil, *dir, *cluster)
		if err != nil {
			fmt.Fprintf(os.Stderr, "collect: %v\n", err)
			return 5
		}
		b, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			return 5
		}
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return 5
		}
		fmt.Fprintf(os.Stderr, "wrote snapshot %s (%d nodes)\n", *out, len(snap.Nodes))
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown snapshot source: %s\n", args[0])
		return 2
	}
}

func cmdChange(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal change manifests|terraform ...")
		return 2
	}
	switch args[0] {
	case "manifests":
		fs := flag.NewFlagSet("change-manifests", flag.ContinueOnError)
		base := fs.String("baseline", "", "baseline snapshot")
		dir := fs.String("dir", "", "rendered manifests directory")
		out := fs.String("out", "change.json", "output change path")
		id := fs.String("id", "change-manifests", "change id")
		title := fs.String("title", "Kubernetes manifest change", "title")
		fs.SetOutput(os.Stderr)
		_ = fs.Parse(args[1:])
		if *base == "" || *dir == "" {
			return 2
		}
		b, err := loader.LoadSnapshot(*base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		ch, err := change.FromManifestsDiff(b, *dir, *id, *title)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		raw, _ := json.MarshalIndent(ch, "", "  ")
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			return 5
		}
		fmt.Fprintf(os.Stderr, "wrote change %s\n", *out)
		return 0
	case "terraform":
		fs := flag.NewFlagSet("change-terraform", flag.ContinueOnError)
		plan := fs.String("plan", "", "terraform show -json file")
		out := fs.String("out", "change.json", "output")
		id := fs.String("id", "change-tf", "id")
		title := fs.String("title", "Terraform plan", "title")
		fs.SetOutput(os.Stderr)
		_ = fs.Parse(args[1:])
		if *plan == "" {
			return 2
		}
		ch, err := change.FromTerraformPlan(*plan, *id, *title)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		raw, _ := json.MarshalIndent(ch, "", "  ")
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			return 5
		}
		fmt.Fprintf(os.Stderr, "wrote change %s\n", *out)
		return 0
	default:
		return 2
	}
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
