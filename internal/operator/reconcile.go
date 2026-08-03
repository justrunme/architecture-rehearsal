// Package operator provides a RehearsalRun reconciler loop (v0.9).
// Works on JSON-serialized CRD-compatible objects without requiring a live apiserver.
package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// Reconciler drives RehearsalRun objects from a watch directory.
type Reconciler struct {
	WatchDir string
	WorkDir  string
	Holder   string
}

// ReconcileOnce loads all *.json runs and advances non-terminal ones.
func (r *Reconciler) ReconcileOnce() (int, error) {
	if r.WatchDir == "" {
		return 0, fmt.Errorf("WatchDir required")
	}
	ents, err := os.ReadDir(r.WatchDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(r.WatchDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rr run.RehearsalRun
		if err := json.Unmarshal(raw, &rr); err != nil {
			continue
		}
		if rr.Status.Phase.Terminal() {
			continue
		}
		eng := &run.Engine{WorkDir: r.WorkDir, Holder: r.Holder}
		if eng.Holder == "" {
			eng.Holder = "operator"
		}
		_ = eng.Execute(&rr)
		out, _ := json.MarshalIndent(rr, "", "  ")
		_ = os.WriteFile(path, out, 0o644)
		n++
	}
	return n, nil
}

// RunLoop reconciles until stop channel closed.
func (r *Reconciler) RunLoop(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			_, _ = r.ReconcileOnce()
		}
	}
}
