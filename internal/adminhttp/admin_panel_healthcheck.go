package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"lampac-go/internal/balancerhealth"
	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// tgAdminHealthcheckHandler serves the admin healthcheck API:
//
//	GET  /api/healthcheck                    — settings + current per-balancer
//	                                            statuses (auto-disabled, last
//	                                            check time/status/latency/etc.)
//	POST /api/healthcheck {action,…}         — actions:
//	    save_settings    body: balancerhealth.HealthSettings           apply + persist
//	    probe_now                                       run a single pass now
//	    reset            body: {balancer:""}            clear counters for one
//	                                                     balancer (or all when empty)
//	    re_enable        body: {balancer:"<name>"}      clear AutoDisabled flag
//	                                                     for one balancer
//
// Auth: regular admin. The endpoint never exposes secrets — only balancer
// names and HTTP-level probe metadata.
func tgAdminHealthcheckHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		hc := balancerhealth.GetGlobalHealthChecker()
		if hc == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "healthcheck disabled at boot",
			})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeHealthcheckSnapshot(w, hc)
		case http.MethodPost:
			handleHealthcheckAction(w, r, hc)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// healthcheckEntry is a flattened row for the admin table: balancer name +
// derived status + raw balancerhealth.HealthStatus fields. Sorted server-side so the UI
// only has to render.
type healthcheckEntry struct {
	Name             string    `json:"name"`
	Healthy          bool      `json:"healthy"`
	AutoDisabled     bool      `json:"auto_disabled"`
	Excluded         bool      `json:"excluded"`
	HasData          bool      `json:"has_data"`
	LastCheckTime    time.Time `json:"last_check_time"`
	LastCheckLatency int64     `json:"last_check_latency_ms"`
	LastCheckStatus  int       `json:"last_check_status"`
	LastCheckError   string    `json:"last_check_error,omitempty"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	ConsecutiveOK    int       `json:"consecutive_ok"`
	Group            string    `json:"group,omitempty"`
}

func writeHealthcheckSnapshot(w http.ResponseWriter, hc *balancerhealth.HealthChecker) {
	snap := hc.Snapshot()
	settings := hc.Settings()
	excluded := make(map[string]struct{}, len(settings.Excluded))
	for _, n := range settings.Excluded {
		excluded[strings.ToLower(n)] = struct{}{}
	}

	entries := make([]healthcheckEntry, 0, len(knownBalancers())+len(snap))
	seen := make(map[string]struct{}, len(snap))

	// Include every knownBalancer so the table shows "no data" rows for
	// balancers that haven't been probed yet (newly added / never reachable).
	for _, name := range knownBalancers() {
		lower := strings.ToLower(name)
		row := healthcheckEntry{Name: name}
		if _, ex := excluded[lower]; ex {
			row.Excluded = true
		}
		if s, ok := snap[lower]; ok {
			row.HasData = true
			row.Healthy = s.Healthy
			row.AutoDisabled = s.AutoDisabled
			row.LastCheckTime = s.LastCheckTime
			row.LastCheckLatency = s.LastCheckLatency
			row.LastCheckStatus = s.LastCheckStatus
			row.LastCheckError = s.LastCheckError
			row.ConsecutiveFails = s.ConsecutiveFails
			row.ConsecutiveOK = s.ConsecutiveOK
			seen[lower] = struct{}{}
		}
		if g := balancerGroupMap()[name]; g != "" {
			row.Group = g
		}
		entries = append(entries, row)
	}
	// Any recorded balancer not in knownBalancers() (custom balancers from
	// custbal/drochub, etc.) is appended at the end with its raw key.
	for k, s := range snap {
		if _, ok := seen[k]; ok {
			continue
		}
		row := healthcheckEntry{
			Name:             k,
			HasData:          true,
			Healthy:          s.Healthy,
			AutoDisabled:     s.AutoDisabled,
			LastCheckTime:    s.LastCheckTime,
			LastCheckLatency: s.LastCheckLatency,
			LastCheckStatus:  s.LastCheckStatus,
			LastCheckError:   s.LastCheckError,
			ConsecutiveFails: s.ConsecutiveFails,
			ConsecutiveOK:    s.ConsecutiveOK,
		}
		if _, ex := excluded[k]; ex {
			row.Excluded = true
		}
		entries = append(entries, row)
	}
	// Balancers in the excluded list that aren't in knownBalancers() and
	// have no snapshot yet (e.g., custom names) — still show them so the
	// operator can untick them from the UI.
	for k := range excluded {
		if _, ok := seen[k]; ok {
			continue
		}
		// Already covered by the snap loop above for entries with data.
		// This loop only adds rows for excluded-but-no-data names.
		if _, has := snap[k]; has {
			continue
		}
		// Also skip if it was a knownBalancer (those are always added).
		isKnown := false
		for _, kb := range knownBalancers() {
			if strings.EqualFold(kb, k) {
				isKnown = true
				break
			}
		}
		if isKnown {
			continue
		}
		entries = append(entries, healthcheckEntry{Name: k, Excluded: true})
	}

	sort.Slice(entries, func(i, j int) bool {
		// Auto-disabled first (operator attention), then unhealthy, then
		// no-data, then healthy. Within a tier, sort alphabetically.
		ri, rj := healthcheckSortRank(entries[i]), healthcheckSortRank(entries[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	counts := map[string]int{"total": len(entries)}
	for _, e := range entries {
		if e.Excluded {
			counts["excluded"]++
		}
		switch {
		case e.AutoDisabled:
			counts["auto_disabled"]++
		case !e.HasData:
			counts["unknown"]++
		case e.Healthy:
			counts["healthy"]++
		default:
			counts["unhealthy"]++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"settings": hc.Settings(),
		"entries":  entries,
		"counts":   counts,
		"now":      time.Now(),
	})
}

func healthcheckSortRank(e healthcheckEntry) int {
	// Excluded entries always sort last regardless of their old health
	// state — they aren't being probed anymore, so they're not actionable.
	if e.Excluded {
		return 4
	}
	switch {
	case e.AutoDisabled:
		return 0
	case e.HasData && !e.Healthy:
		return 1
	case !e.HasData:
		return 2
	default:
		return 3
	}
}

func handleHealthcheckAction(w http.ResponseWriter, r *http.Request, hc *balancerhealth.HealthChecker) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req struct {
		Action   string                         `json:"action"`
		Balancer string                         `json:"balancer"`
		Excluded bool                           `json:"excluded"`
		Settings *balancerhealth.HealthSettings `json:"settings,omitempty"`
	}
	if len(body) > 0 {
		if err := stdjson.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json: " + err.Error()})
			return
		}
	}

	switch req.Action {
	case "save_settings":
		if req.Settings == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "settings missing"})
			return
		}
		hc.ApplySettings(*req.Settings)
		// Persist so the override survives a restart. Use the live config
		// pointer's repo root since the bootstrap-time root may be relative
		// and we want consistency with how LoadFromDisk resolves paths.
		repoRoot := "."
		if serverReady() {
			repoRoot = liveConfig(config.Config{}).Compat.RepoRoot
		}
		if err := balancerhealth.SaveHealthcheckSettingsOverride(repoRoot, hc.Settings()); err != nil {
			log.Warn().Err(err).Msg("healthcheck: persist settings override failed")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed: " + err.Error()})
			return
		}
		writeHealthcheckSnapshot(w, hc)

	case "probe_now":
		// Run one pass in the background so the request returns immediately.
		// The next GET will see the freshened snapshot.
		go hc.RunOnce()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scheduled": true})

	case "reset":
		affected := hc.ResetStatus(strings.TrimSpace(req.Balancer))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected})

	case "re_enable":
		name := strings.TrimSpace(req.Balancer)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "balancer required"})
			return
		}
		ok := hc.ManualReEnable(name)
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok})

	case "set_excluded":
		// Per-row toggle from the admin UI. Apply + persist + return the
		// fresh snapshot so the UI doesn't need a follow-up GET.
		name := strings.TrimSpace(req.Balancer)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "balancer required"})
			return
		}
		hc.SetExcluded(name, req.Excluded)
		repoRoot := "."
		if serverReady() {
			repoRoot = liveConfig(config.Config{}).Compat.RepoRoot
		}
		if err := balancerhealth.SaveHealthcheckSettingsOverride(repoRoot, hc.Settings()); err != nil {
			log.Warn().Err(err).Msg("healthcheck: persist excluded toggle failed")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed: " + err.Error()})
			return
		}
		writeHealthcheckSnapshot(w, hc)

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
	}
}
