package rutracker

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Diagnose runs the source end to end and reports each stage separately. This
// is the support tool: "rutracker finds nothing" has four very different
// causes — no credentials, blocked egress, a Cloudflare challenge, a dead
// session — and they are indistinguishable from the merged search result alone.
type Diagnosis struct {
	OK       bool        `json:"ok"`
	Host     string      `json:"host"`
	Stages   []DiagStage `json:"stages"`
	Rows     int         `json:"rows"`
	Resolved int         `json:"resolved"`
	Sample   []Release   `json:"sample,omitempty"`
}

type DiagStage struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | fail | skip
	Detail string `json:"detail,omitempty"`
	Ms     int64  `json:"ms"`
}

func (d *Diagnosis) add(name string, started time.Time, status, detail string) {
	d.Stages = append(d.Stages, DiagStage{
		Name: name, Status: status, Detail: detail,
		Ms: time.Since(started).Milliseconds(),
	})
}

// Diagnose never mutates the search cache — it always performs live requests,
// which is the point of a diagnostic.
func (c *Client) Diagnose(ctx context.Context, query string) Diagnosis {
	cfg := c.Config()
	d := Diagnosis{Host: cfg.Host}

	// 1. config
	started := time.Now()
	switch {
	case !cfg.Enable:
		d.add("config", started, "fail", "[parser.rutracker] enable = false")
		return d
	case !c.Enabled():
		d.add("config", started, "fail", "нет ни cookie, ни пары login/password")
		return d
	default:
		src := "login/password"
		if strings.TrimSpace(cfg.Cookie) != "" {
			src = "cookie"
		}
		d.add("config", started, "ok", "credentials: "+src)
	}

	if until := c.limiter.bannedUntil(); !until.IsZero() && time.Now().Before(until) {
		d.add("breaker", time.Now(), "fail", "источник на паузе до "+until.Format(time.TimeOnly))
		return d
	}

	// 2. reachability + challenge (index.php is served to guests, so this
	//    isolates "egress/DPI" from "auth")
	started = time.Now()
	resp, err := c.fetch(ctx, cfg.Host+"/forum/index.php", fetchOpts{})
	switch {
	case err == errChallenge:
		d.add("connect", started, "fail", "Cloudflare challenge (нужен FlareSolverr или готовая cookie)")
		return d
	case err != nil:
		d.add("connect", started, "fail", err.Error())
		return d
	default:
		detail := fmt.Sprintf("HTTP %d", resp.status)
		if resp.viaFS {
			detail += ", через FlareSolverr"
		}
		d.add("connect", started, "ok", detail)
	}

	// 3. session
	started = time.Now()
	if err := c.ensureSession(ctx); err != nil {
		d.add("login", started, "fail", err.Error())
		return d
	}
	d.add("login", started, "ok", "bb_session получен")

	// 4. search
	if strings.TrimSpace(query) == "" {
		query = "matrix"
	}
	started = time.Now()
	page, err := c.fetchListing(ctx, c.searchURL(cfg, query))
	if err != nil {
		detail := err.Error()
		if err == errGuest {
			detail = "страница отдана как гостю — сессия не принята (капча? cookie протухла?)"
		}
		d.add("search", started, "fail", detail)
		return d
	}
	rows := parseListing(page, cfg.Host)
	if len(rows) == 0 {
		// An empty result is legitimate; a parse regression is not. The page
		// size tells them apart at a glance.
		d.add("search", started, "fail",
			fmt.Sprintf("0 строк на странице %d байт — либо ничего не найдено, либо сменилась вёрстка", len(page)))
		d.Rows = 0
		return d
	}
	d.add("search", started, "ok", fmt.Sprintf("%d строк", len(rows)))
	d.Rows = len(rows)

	// 5. magnet resolve (the stage that costs a request per release)
	started = time.Now()
	magnet, err := c.resolveMagnet(ctx, rows[0].TopicID)
	if err != nil || magnet == "" {
		d.add("resolve", started, "fail", errString(err))
		d.Sample = sampleRows(rows)
		return d
	}
	rows[0].Magnet = magnet
	d.add("resolve", started, "ok", magnet[:minInt(len(magnet), 60)])
	d.Resolved = countResolved(rows)
	d.Sample = sampleRows(rows)
	d.OK = true
	return d
}

func sampleRows(rows []Release) []Release {
	if len(rows) > 3 {
		return rows[:3]
	}
	return rows
}

func errString(err error) string {
	if err == nil {
		return "пустой magnet"
	}
	return err.Error()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
