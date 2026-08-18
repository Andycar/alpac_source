package jacred

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// cronJob mirrors one line of jacred's shipped Data/crontab. jacred has no
// built-in scheduler — parsing is kicked over HTTP by an external cron. Since
// lampac-go is already a daemon, we play that role instead of touching the
// host crontab.
type cronJob struct {
	path     string
	interval time.Duration
}

// defaultCronJobs reproduces jacred-fdb Data/crontab (intervals simplified to
// tickers). Only used when SyncAPI is empty — with sync enabled the instance
// pulls the base from upstream and must not parse or save on its own.
var defaultCronJobs = []cronJob{
	// Persist the in-memory JSON DB to disk.
	{"/jsondb/save", 5 * time.Minute},

	{"/cron/rutor/parse", 15 * time.Minute},
	{"/cron/rutor/UpdateTasksParse", 4 * time.Hour},
	{"/cron/rutor/ParseAllTask", 5 * time.Minute},

	{"/cron/rutracker/parse", 15 * time.Minute},
	{"/cron/rutracker/UpdateTasksParse", 4 * time.Hour},
	{"/cron/rutracker/ParseAllTask", 4 * time.Hour},

	{"/cron/kinozal/parse", 15 * time.Minute},
	{"/cron/kinozal/UpdateTasksParse", 4 * time.Hour},
	{"/cron/kinozal/ParseAllTask", 5 * time.Minute},

	{"/cron/nnmclub/parse", 15 * time.Minute},
	{"/cron/nnmclub/UpdateTasksParse", 4 * time.Hour},
	{"/cron/nnmclub/ParseAllTask", 5 * time.Minute},

	{"/cron/selezen/parse", 15 * time.Minute},

	{"/cron/megapeer/parse", time.Hour},
	{"/cron/megapeer/UpdateTasksParse", 4 * time.Hour},
	{"/cron/megapeer/ParseAllTask", time.Hour},

	{"/cron/torrentby/parse", 15 * time.Minute},
	{"/cron/torrentby/UpdateTasksParse", 4 * time.Hour},
	{"/cron/torrentby/ParseAllTask", 5 * time.Minute},

	{"/cron/toloka/parse", 40 * time.Minute},
	{"/cron/mazepa/parse", 40 * time.Minute},
	{"/cron/lostfilm/parse", 15 * time.Minute},
	{"/cron/bitruapi/parse", 20 * time.Minute},
	{"/cron/knaben/parse", 20 * time.Minute},
	{"/cron/animelayer/parse", 15 * time.Minute},
	{"/cron/anidub/parse", 15 * time.Minute},
	{"/cron/aniliberty/parse", 15 * time.Minute},
	{"/cron/baibako/parse", 15 * time.Minute},
}

// cronLoop drives the parse schedule against the local instance. Jobs run in
// one goroutine, staggered by next-due time, so a slow tracker never
// stampedes the box with parallel parses.
func (m *Manager) cronLoop(ctx context.Context) {
	// Give the child time to boot and load the FDB before the first kicks.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}

	// Parse endpoints run synchronously and can take minutes on big trackers.
	client := &http.Client{Timeout: 30 * time.Minute}

	next := make([]time.Time, len(defaultCronJobs))
	now := time.Now()
	for i, j := range defaultCronJobs {
		// Stagger initial runs across the first interval.
		next[i] = now.Add(j.interval * time.Duration(i+1) / time.Duration(len(defaultCronJobs)))
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !m.healthy.Load() {
			continue
		}
		now = time.Now()
		for i, j := range defaultCronJobs {
			if now.Before(next[i]) {
				continue
			}
			next[i] = now.Add(j.interval)
			m.runCronJob(ctx, client, j.path)
		}
	}
}

func (m *Manager) runCronJob(ctx context.Context, client *http.Client, path string) {
	url := m.BaseURL() + path
	if m.cfg.APIKey != "" {
		url += "?apikey=" + m.cfg.APIKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("job", path).Msg("jacred: cron job failed")
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	log.Debug().Str("job", path).Int("status", resp.StatusCode).
		Dur("took", time.Since(started)).Msg("jacred: cron job done")
}
