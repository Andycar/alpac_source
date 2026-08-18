package adminhttp

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"io"
	"net/http"
	"time"

	"lampac-go/internal/config"
)

// FanoutStorageDelete повторяет удаление на всех enabled кластер-нодах. Владельцы и
// профили посчитаны на этом сервере и передаются явно — у ноды может не быть своей
// tgauth-записи юзера, а раскладка storage у всех одинаковая. Экспортировано: тем же
// фан-аутом пользуется юзерский self-service `/storage/wipe` (httpapi).
func FanoutStorageDelete(ctx context.Context, owners, profiles, paths []string) []map[string]any {
	cs := liveClusterStore()
	if cs == nil {
		return nil
	}
	cfg := liveConfig(config.Config{})
	apiKey := cfg.Cluster.APIKey
	payload, _ := stdjson.Marshal(map[string]any{
		"owner_uids":  owners,
		"profile_ids": profiles,
		"paths":       paths,
	})
	out := []map[string]any{}
	for _, n := range cs.Snapshot() {
		if !n.Enabled || n.Host == "" {
			continue
		}
		row := map[string]any{"node": n.Name, "host": n.Host}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(cctx, http.MethodPost, n.Host+"/api/cluster/userdata/delete", bytes.NewReader(payload))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if apiKey != "" {
				req.Header.Set("X-Cluster-Key", apiKey)
			}
			resp, rerr := http.DefaultClient.Do(req)
			if rerr != nil {
				row["error"] = rerr.Error()
			} else {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
				resp.Body.Close()
				row["status"] = resp.StatusCode
				var parsed map[string]any
				if stdjson.Unmarshal(b, &parsed) == nil {
					row["response"] = parsed
				}
			}
		} else {
			row["error"] = err.Error()
		}
		cancel()
		out = append(out, row)
	}
	return out
}
