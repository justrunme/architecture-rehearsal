// rehearsal — CLI for Architecture Rehearsal.
//
// Graph and rules decide. AI (later) only explains.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/report"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "analyze":
		os.Exit(cmdAnalyze(os.Args[2:]))
	case "version":
		fmt.Println("rehearsal", analyze.Version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Architecture Rehearsal — know what breaks before you deploy

Usage:
  rehearsal analyze --baseline FILE --change FILE [flags]
  rehearsal version

Flags (analyze):
  --baseline PATH   baseline architecture snapshot (JSON)
  --change PATH     proposed change envelope (JSON)
  --out DIR         evidence output directory (default: ./out)
  --html PATH       write HTML report to PATH (optional extra copy)
  --json            print report JSON to stdout (default)
  --quiet           only print risk + decision line

Exit codes:
  0  approve / no findings
  1  warn
  2  usage / load error
  3  block (high/critical)
`)
}

func cmdAnalyze(args []string) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	baseline := fs.String("baseline", "", "baseline snapshot JSON")
	change := fs.String("change", "", "change envelope JSON")
	outDir := fs.String("out", "out", "evidence output directory")
	htmlPath := fs.String("html", "", "optional extra HTML path")
	asJSON := fs.Bool("json", true, "print JSON report to stdout")
	quiet := fs.Bool("quiet", false, "compact summary only")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *baseline == "" || *change == "" {
		fmt.Fprintln(os.Stderr, "error: --baseline and --change are required")
		return 2
	}

	base, err := loader.LoadSnapshot(*baseline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
		return 2
	}
	ch, err := loader.LoadChange(*change)
	if err != nil {
		fmt.Fprintf(os.Stderr, "change: %v\n", err)
		return 2
	}

	rep := analyze.Run(base, ch)

	dir, err := evidence.Bundle(*outDir, rep, abs(*baseline), abs(*change))
	if err != nil {
		fmt.Fprintf(os.Stderr, "evidence: %v\n", err)
		return 2
	}
	if *htmlPath != "" {
		if err := report.WriteHTML(*htmlPath, rep); err != nil {
			fmt.Fprintf(os.Stderr, "html: %v\n", err)
			return 2
		}
	}

	if *quiet {
		fmt.Printf("risk=%s decision=%s findings=%d evidence=%s\n",
			rep.Risk, rep.Decision, len(rep.Findings), dir)
	} else if *asJSON {
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
	default:
		return 0
	}
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
