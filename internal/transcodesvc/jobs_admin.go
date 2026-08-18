package transcodesvc

import "time"

// JobSummary is the compact, JSON-friendly form of a TranscodingJob exposed to
// admin UIs (media gateway). Lives here so it can read the job's unexported
// fields under the service mutex without exposing those internals.
type JobSummary struct {
	ID            string         `json:"id"`
	StreamID      string         `json:"stream_id"`
	Mode          string         `json:"mode"`
	State         string         `json:"state"`
	StartedAt     time.Time      `json:"started_at"`
	UptimeSeconds int64          `json:"uptime_s"`
	LastAccessAgo int64          `json:"last_access_s"`
	LastSegment   int64          `json:"last_segment"`
	OutputDir     string         `json:"output_dir,omitempty"`
	OriginalURL   string         `json:"original_url,omitempty"`
	Source        string         `json:"source,omitempty"`
	Streams       map[string]any `json:"streams,omitempty"`
	Warning       string         `json:"warning,omitempty"`
	ExitCode      int            `json:"exit_code,omitempty"`
}

// SnapshotTranscodingJobs returns a stable, JSON-friendly view of the transcoding
// service's current jobs map, read under svc.mu.
func SnapshotTranscodingJobs(svc *TranscodingService) []JobSummary {
	if svc == nil {
		return nil
	}
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	out := make([]JobSummary, 0, len(svc.jobs))
	now := time.Now()
	for id, job := range svc.jobs {
		if job == nil {
			continue
		}
		exit := int(job.exitCode) // atomic int32, but we hold svc.mu so a relaxed read is fine
		state := JobStateRunning
		if exit >= 0 {
			state = JobStateStopped
		}
		var lastAccessAgo int64
		if v := job.lastAccess.Load(); v != nil {
			if t, ok := v.(time.Time); ok && !t.IsZero() {
				lastAccessAgo = int64(now.Sub(t).Seconds())
			}
		}
		out = append(out, JobSummary{
			ID:            id,
			StreamID:      job.StreamID,
			Mode:          string(job.Mode),
			State:         state,
			StartedAt:     job.StartedUtc,
			UptimeSeconds: int64(now.Sub(job.StartedUtc).Seconds()),
			LastAccessAgo: lastAccessAgo,
			LastSegment:   job.lastSegIndex,
			OutputDir:     job.OutputDir,
			OriginalURL:   job.OriginalURL,
			Source:        job.Context.Source,
			Streams:       job.Streams,
			Warning:       job.Warning,
			ExitCode:      exit,
		})
	}
	return out
}

// KillTranscodingJobByID terminates the job with the given ID. Returns true if a
// matching job was found.
func KillTranscodingJobByID(svc *TranscodingService, jobID string) bool {
	if svc == nil || jobID == "" {
		return false
	}
	svc.mu.Lock()
	job := svc.jobs[jobID]
	svc.mu.Unlock()
	if job == nil {
		return false
	}
	// Set stopRequested so waitExit doesn't try to auto-restart.
	job.stopRequested = 1
	job.cancelOnce.Do(func() {
		select {
		case job.cancelCh <- struct{}{}:
		default:
		}
	})
	if job.Cmd != nil && job.Cmd.Process != nil {
		_ = job.Cmd.Process.Kill()
	}
	return true
}
