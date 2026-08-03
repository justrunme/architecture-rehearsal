package persist

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// Job kinds.
const (
	JobAdvanceRun = "run.advance"
)

// AdvancePayload is the job payload for run execution.
type AdvancePayload struct {
	WorkDir string `json:"workDir"`
	Action  string `json:"action"`
}

// Worker claims and executes jobs from the store.
type Worker struct {
	Store     *Store
	Holder    string
	WorkDir   string
	Calibrate *calibrate.Store // optional in-memory mirror; SQL cal is primary
	Interval  time.Duration
	LeaseTTL  time.Duration
}

// Run loops until ctx cancelled.
func (w *Worker) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 500 * time.Millisecond
	}
	if w.LeaseTTL <= 0 {
		w.LeaseTTL = 2 * time.Minute
	}
	if w.Holder == "" {
		w.Holder = "worker-1"
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.tick(ctx); err != nil {
				log.Printf("worker: %v", err)
			}
		}
	}
}

func (w *Worker) tick(ctx context.Context) error {
	job, err := w.Store.ClaimJob(ctx, w.Holder, w.LeaseTTL)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}
	if err := w.execute(job); err != nil {
		_ = w.Store.FailJob(job.ID, err.Error())
		return err
	}
	return w.Store.CompleteJob(job.ID)
}

func (w *Worker) execute(job *Job) error {
	switch job.Kind {
	case JobAdvanceRun:
		return w.execAdvance(job)
	default:
		return fmt.Errorf("unknown job kind %s", job.Kind)
	}
}

func (w *Worker) execAdvance(job *Job) error {
	rr, err := w.Store.GetRun(job.RunID)
	if err != nil {
		return err
	}
	if rr == nil {
		return fmt.Errorf("run %s not found", job.RunID)
	}
	var p AdvancePayload
	if job.Payload != "" {
		_ = json.Unmarshal([]byte(job.Payload), &p)
	}
	if p.Action == "cancel" {
		_ = rr.Transition(run.PhaseCancelled, "cancelled by job")
		return w.Store.PutRun(rr)
	}
	wd := p.WorkDir
	if wd == "" {
		wd = w.WorkDir
	}
	// Optional org policy file from store
	if pol, err := w.Store.GetOrgPolicy(job.Org); err == nil && pol != nil {
		// write temp policy yaml-ish via JSON for engine — engine expects path; skip if no path
		// Product: store policy payload and pass as PolicyPath when we materialize
		_ = pol
	}
	eng := &run.Engine{WorkDir: wd, Holder: w.Holder, Calibrate: w.Calibrate}
	if err := eng.Execute(rr); err != nil {
		// still persist failed state
		_ = w.Store.PutRun(rr)
		// also record calibration from engine if any
		return err
	}
	// Persist calibration outcomes from run predicted failures + verify outcome
	if w.Store != nil && len(rr.Status.PredictedFailures) > 0 {
		verified := rr.Status.VerifyOutcome != "" && rr.Status.VerifyOutcome != "inconclusive"
		for _, sc := range rr.Status.PredictedFailures {
			obsOK := rr.Status.VerifyOutcome == "verified"
			_ = w.Store.RecordCalibration(calibrate.Outcome{
				Scenario: sc, Predicted: true, Observed: obsOK, Verified: verified,
			})
		}
	}
	// Store chain blob if present
	if w.Store.Blob != nil && rr.Status.ChainPath != "" {
		if raw, err := readFile(rr.Status.ChainPath); err == nil {
			if d, _, err := w.Store.Blob.Put(raw, "application/json"); err == nil {
				if rr.Labels == nil {
					rr.Labels = map[string]string{}
				}
				rr.Labels["chainBlob"] = d
			}
		}
	}
	return w.Store.PutRun(rr)
}

func readFile(p string) ([]byte, error) {
	return osReadFile(p)
}
