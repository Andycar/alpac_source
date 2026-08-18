package httpapi

import (
	"context"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/rutracker"
)

// rutrackerAdminStatus is the payload behind /{adminPath}/api/rutracker/status.
// It answers the two questions the panel actually needs: is the source on, and
// if it is on but silent — why.
func rutrackerAdminStatus() map[string]any {
	cfg := liveConfig(config.Config{})
	client := liveRutracker()
	if client == nil {
		return map[string]any{"enabled": false, "reason": "server not ready"}
	}
	client.SetConfig(rutrackerConfigFrom(cfg))

	st := client.Snapshot()
	out := map[string]any{
		"enabled":           st.Enabled,
		"has_creds":         st.HasCreds,
		"host":              st.Host,
		"logged_in":         st.LoggedIn,
		"login_at":          st.LoginAt,
		"challenge":         st.Challenge,
		"flaresolverr_used": st.FlareSolverr,
		"flaresolverr_set":  httpclient.FlareSolverrURL() != "",
		"banned_until":      st.BannedUntil,
		"last_error":        st.LastError,
		"searches":          st.Searches,
		"requests":          st.Requests,
		"rows_last":         st.RowsLast,
		"magnet_cache":      st.HashCacheSize,
		"search_cache":      st.SearchCache,
		"avg_ms":            st.AvgMs,
		"only_authorized":   cfg.Parser.RuTracker.OnlyAuthorized,
	}
	if st.Enabled && !st.HasCreds {
		out["reason"] = "включено, но не заданы login/password (или cookie)"
	}
	return out
}

// rutrackerAdminTest runs the staged live probe.
func rutrackerAdminTest(ctx context.Context, query string) map[string]any {
	cfg := liveConfig(config.Config{})
	client := liveRutracker()
	if client == nil {
		return map[string]any{"ok": false, "error": "server not ready"}
	}
	client.SetConfig(rutrackerConfigFrom(cfg))

	d := client.Diagnose(ctx, query)
	stages := make([]map[string]any, 0, len(d.Stages))
	for _, s := range d.Stages {
		stages = append(stages, map[string]any{
			"name": s.Name, "status": s.Status, "detail": s.Detail, "ms": s.Ms,
		})
	}
	sample := make([]map[string]any, 0, len(d.Sample))
	for _, r := range d.Sample {
		sample = append(sample, rutrackerSampleRow(r))
	}
	return map[string]any{
		"ok":       d.OK,
		"host":     d.Host,
		"stages":   stages,
		"rows":     d.Rows,
		"resolved": d.Resolved,
		"sample":   sample,
	}
}

func rutrackerSampleRow(r rutracker.Release) map[string]any {
	return map[string]any{
		"title":     r.Title,
		"size":      r.SizeName,
		"seeders":   r.Seeders,
		"leechers":  r.Leechers,
		"topic_id":  r.TopicID,
		"types":     r.Types,
		"resolved":  r.Resolved(),
		"create_at": r.CreatedAt.Format("2006-01-02"),
	}
}
