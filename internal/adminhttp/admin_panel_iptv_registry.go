package adminhttp

import (
	stdjson "encoding/json"
	"net/http"

	"lampac-go/internal/iptv"
	"lampac-go/internal/iptvhttp"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// admin_panel_iptv_registry.go — управление СВОИМ реестром IPTV-каналов
// («Мои каналы», [iptv] registry=true).
//
//	GET  /{adminPath}/api/iptv-registry            — каналы + статус источников
//	POST /{adminPath}/api/iptv-registry {action,…} — действия:
//	    upsert   body: {channel:{id?,name,group,tvg_id,logo,number,disabled,aliases,pinned}}
//	             создать/изменить канал; pinned-источники задаются целиком
//	    delete   body: {id}
//	    disable  body: {id, disabled}
//	    ingest   body: {add_new}   пересобрать источники из глобальных плейлистов
//	                               сейчас; add_new=true — добавить незнакомые каналы
//	    refresh  —                 перечитать доноров с апстрима (в фоне; ingest
//	                               сработает сам после каждого плейлиста)
//
// Auth: regular admin.

// RegisterIPTVRegistryRoutes wires the registry management API.
func RegisterIPTVRegistryRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminIPTVRegistryHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/iptv-registry", h)
	router.Post("/"+adminPath+"/api/iptv-registry", h)
}

// regSourceRow — источник + живость по последнему health-циклу.
type regSourceRow struct {
	iptv.RegSource
	Alive bool `json:"alive"` // не помечен мёртвым (не пробован = жив)
	// Frozen — отвечает 200, сегменты качаются, но окно эфира стоит. Для
	// зрителя это «канал крутит один и тот же кусок»; отличать от недоступного
	// важно, потому что чинится оно иначе (нужен другой источник, а не сеть).
	Frozen bool `json:"frozen,omitempty"`
}

type regChannelRow struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Group    string         `json:"group,omitempty"`
	TvgID    string         `json:"tvg_id,omitempty"`
	Logo     string         `json:"logo,omitempty"`
	Number   int            `json:"number,omitempty"`
	Disabled bool           `json:"disabled,omitempty"`
	Aliases  []string       `json:"aliases,omitempty"`
	Pinned   []regSourceRow `json:"pinned,omitempty"`
	Auto     []regSourceRow `json:"auto,omitempty"`
	AliveN   int            `json:"alive_sources"`
	TotalN   int            `json:"total_sources"`
}

func tgAdminIPTVRegistryHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		st := iptvhttp.ActiveStore()
		if st == nil || st.Registry() == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "registry disabled — set [iptv] enable=true and registry=true",
			})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeIPTVRegistrySnapshot(w, st)
		case http.MethodPost:
			handleIPTVRegistryAction(w, r, st)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func writeIPTVRegistrySnapshot(w http.ResponseWriter, st *iptv.Store) {
	reg := st.Registry()
	channels := reg.List()
	rows := make([]regChannelRow, 0, len(channels))
	srcRows := func(list []iptv.RegSource) ([]regSourceRow, int) {
		out := make([]regSourceRow, 0, len(list))
		alive := 0
		for _, s := range list {
			a := st.SourceAlive(s.URL)
			if a {
				alive++
			}
			out = append(out, regSourceRow{RegSource: s, Alive: a, Frozen: st.SourceFrozen(s.URL)})
		}
		return out, alive
	}
	for i := range channels {
		c := &channels[i]
		pinned, aliveP := srcRows(c.Pinned)
		auto, aliveA := srcRows(c.Auto)
		rows = append(rows, regChannelRow{
			ID: c.ID, Name: c.Name, Group: c.Group, TvgID: c.TvgID,
			Logo: c.Logo, Number: c.Number, Disabled: c.Disabled, Aliases: c.Aliases,
			Pinned: pinned, Auto: auto,
			AliveN: aliveP + aliveA, TotalN: len(pinned) + len(auto),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":     reg.Name(),
		"enabled":  reg.Len(),
		"total":    len(rows),
		"channels": rows,
	})
}

func handleIPTVRegistryAction(w http.ResponseWriter, r *http.Request, st *iptv.Store) {
	var body struct {
		Action   string          `json:"action"`
		ID       string          `json:"id"`
		Disabled bool            `json:"disabled"`
		AddNew   bool            `json:"add_new"`
		Channel  iptv.RegChannel `json:"channel"`
	}
	if err := stdjson.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
		return
	}
	reg := st.Registry()
	switch body.Action {
	case "upsert":
		ch, err := reg.Upsert(body.Channel)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": ch})
	case "delete":
		if !reg.Delete(body.ID) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "channel not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "disable":
		if !reg.SetDisabled(body.ID, body.Disabled) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "channel not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "ingest":
		stats := st.IngestRegistry(body.AddNew)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": stats})
	case "refresh":
		go st.RefreshGlobal() // ingest сработает сам после каждого глобального плейлиста
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "background": true})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
	}
}
