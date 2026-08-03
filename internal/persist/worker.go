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
	// Job-scoped cancel: poll job status; cancel engine context if cancelled.
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()
	hbCtx, hbCancel := context.WithCancel(jobCtx)
	defer hbCancel()
	go w.heartbeat(hbCtx, job)
	go w.watchCancel(jobCtx, jobCancel, job)

	if err := w.execute(jobCtx, job); err != nil {
		if failErr := w.Store.FailJob(job.ID, job.FenceToken, err.Error()); failErr != nil {
			// cancelled/stale fence is expected
			log.Printf("worker fail job: %v (exec err: %v)", failErr, err)
		}
		return err
	}
	// Only complete if still active (not cancelled mid-flight)
	ok, _ := w.Store.JobIsActive(job.ID, w.Holder, job.FenceToken)
	if !ok {
		return ErrStaleFence
	}
	return w.Store.CompleteJob(job.ID, job.FenceToken)
}

func (w *Worker) watchCancel(ctx context.Context, cancel context.CancelFunc, job *Job) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ok, err := w.Store.JobIsActive(job.ID, w.Holder, job.FenceToken)
			if err != nil || !ok {
				cancel()
				return
			}
		}
	}
}

func (w *Worker) heartbeat(ctx context.Context, job *Job) {
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

func (w *Worker) execute(ctx context.Context, job *Job) error {
	switch job.Kind {
	case JobAdvanceRun:
		return w.execAdvance(ctx, job)
	default:
		return fmt.Errorf("unknown job kind %s", job.Kind)
	}
}

func (w *Worker) execAdvance(ctx context.Context, job *Job) error {
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
		return w.persistIfActive(job, rr)
	}
	if rr.Status.Phase.Terminal() {
		return nil
	}
	wd := p.WorkDir
	if wd == "" {
		wd = w.WorkDir
	}
	eng := &run.Engine{WorkDir: wd, Holder: w.Holder}
	if err := eng.ExecuteContext(ctx, rr); err != nil {
		// persist cancelled/failed state only if still lease holder
		_ = w.persistIfActive(job, rr)
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
				rr.Labels["chainBlobURI"] = w.Store.Blob.URI(d)
			}
		}
	}
	return w.persistIfActive(job, rr)
}

// persistIfActive writes run only when job lease/fence is still held (prevents cancelled stale write).
func (w *Worker) persistIfActive(job *Job, rr *run.RehearsalRun) error {
	ok, err := w.Store.JobIsActive(job.ID, w.Holder, job.FenceToken)
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleFence
	}
	return w.Store.UpdateRun(rr)
}

func readFile(p string) ([]byte, error) {
	return osReadFile(p)
}
