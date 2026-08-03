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
// LeaseTTL must exceed typical run duration; RenewLease heartbeats during execute.
type Worker struct {
	Store    *Store
	Holder   string
	WorkDir  string
	Interval time.Duration
	// LeaseTTL default 15m (covers 10m run timeout + margin).
	LeaseTTL time.Duration
}

// Run loops until ctx cancelled.
func (w *Worker) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 500 * time.Millisecond
	}
	if w.LeaseTTL <= 0 {
		w.LeaseTTL = 15 * time.Minute
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
	// Heartbeat lease while executing.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go w.heartbeat(hbCtx, job)

	if err := w.execute(job); err != nil {
		if failErr := w.Store.FailJob(job.ID, job.FenceToken, err.Error()); failErr != nil {
			log.Printf("worker fail job: %v (exec err: %v)", failErr, err)
		}
		return err
	}
	return w.Store.CompleteJob(job.ID, job.FenceToken)
}

func (w *Worker) heartbeat(ctx context.Context, job *Job) {
	// Renew at half TTL so long runs keep the lease.
	period := w.LeaseTTL / 3
	if period < 5*time.Second {
		period = 5 * time.Second
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Store.RenewLease(job.ID, w.Holder, job.FenceToken, w.LeaseTTL); err != nil {
				log.Printf("worker: lease renew failed for %s: %v", job.ID, err)
				return
			}
		}
	}
}

func (w *Worker) execute(job *Job) error {
	// Bail if job was cancelled while we claimed
	switch job.Kind {
	case JobAdvanceRun:
		return w.execAdvance(job)
	default:
		return fmt.Errorf("unknown job kind %s", job.Kind)
	}
}

func (w *Worker) execAdvance(job *Job) error {
	rr, err := w.Store.GetRun(job.Org, job.RunID)
	if err != nil {
		return err
	}
	if rr == nil {
		return fmt.Errorf("run %s not found in org %s", job.RunID, job.Org)
	}
	var p AdvancePayload
	if job.Payload != "" {
		_ = json.Unmarshal([]byte(job.Payload), &p)
	}
	if p.Action == "cancel" {
		_ = rr.Transition(run.PhaseCancelled, "cancelled by job")
		return w.Store.UpdateRun(rr)
	}
	// Skip if already terminal
	if rr.Status.Phase.Terminal() {
		return nil
	}
	wd := p.WorkDir
	if wd == "" {
		wd = w.WorkDir
	}
	eng := &run.Engine{WorkDir: wd, Holder: w.Holder}
	if err := eng.Execute(rr); err != nil {
		_ = w.Store.UpdateRun(rr)
		return err
	}
	org := job.Org
	if len(rr.Status.PredictedFailures) > 0 {
		verified := rr.Status.VerifyOutcome != "" && rr.Status.VerifyOutcome != "inconclusive"
		for _, sc := range rr.Status.PredictedFailures {
			obsOK := rr.Status.VerifyOutcome == "verified"
			_ = w.Store.RecordCalibration(org, calibrate.Outcome{
				Scenario: sc, Predicted: true, Observed: obsOK, Verified: verified,
			})
		}
	}
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
	return w.Store.UpdateRun(rr)
}

func readFile(p string) ([]byte, error) {
	return osReadFile(p)
}
