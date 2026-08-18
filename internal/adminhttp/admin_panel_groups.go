package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"strings"

	"lampac-go/internal/tgauth"
)

// --- User Groups Admin API ---

func tgAdminGroupsHandler(
	store *tgauth.Store,
	adminStore *tgauth.AdminIDStore,
	groupStore *tgauth.GroupStore,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			groups := groupStore.List()
			userCounts := map[string]int{}
			if store != nil {
				userCounts = tgauth.CountByGroup(store.List())
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"groups":      groups,
				"users_count": userCounts,
			})

		case http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			var req groupActionRequest
			if err := stdjson.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
				return
			}
			handleGroupAction(w, req, store, groupStore)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

type groupActionRequest struct {
	Action          string          `json:"action"`
	ID              string          `json:"id,omitempty"`
	Name            string          `json:"name,omitempty"`
	Description     string          `json:"description,omitempty"`
	MaxDevices      int             `json:"max_devices,omitempty"`
	TorrServer      *bool           `json:"torrserver,omitempty"`
	SISI            *bool           `json:"sisi,omitempty"`
	Balancers       map[string]bool `json:"balancers,omitempty"`
	StrictBalancers *bool           `json:"strict_balancers,omitempty"`
	TorrServers     map[string]bool `json:"torrservers,omitempty"`
	Token           string          `json:"token,omitempty"`
	GroupID         string          `json:"group_id,omitempty"`
	Tokens          []string        `json:"tokens,omitempty"`
}

func handleGroupAction(w http.ResponseWriter, req groupActionRequest, tokenStore *tgauth.Store, groupStore *tgauth.GroupStore) {
	switch req.Action {
	case "create":
		id := slugify(req.Name)
		if id == "" {
			id = slugify(req.ID)
		}
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		ts := true
		if req.TorrServer != nil {
			ts = *req.TorrServer
		}
		sisi := true
		if req.SISI != nil {
			sisi = *req.SISI
		}
		strict := false
		if req.StrictBalancers != nil {
			strict = *req.StrictBalancers
		}
		g := tgauth.UserGroup{
			ID:              id,
			Name:            req.Name,
			MaxDevices:      req.MaxDevices,
			TorrServer:      ts,
			SISI:            sisi,
			Balancers:       req.Balancers,
			StrictBalancers: strict,
			TorrServers:     req.TorrServers,
			Description:     req.Description,
		}
		if err := groupStore.Create(g); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})

	case "update":
		if req.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
			return
		}
		existing, found := groupStore.Get(req.ID)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
			return
		}
		if req.Name != "" {
			existing.Name = req.Name
		}
		if req.Description != "" {
			existing.Description = req.Description
		}
		existing.MaxDevices = req.MaxDevices
		if req.TorrServer != nil {
			existing.TorrServer = *req.TorrServer
		}
		if req.SISI != nil {
			existing.SISI = *req.SISI
		}
		if req.Balancers != nil {
			existing.Balancers = req.Balancers
		}
		if req.StrictBalancers != nil {
			existing.StrictBalancers = *req.StrictBalancers
		}
		if req.TorrServers != nil {
			existing.TorrServers = req.TorrServers
		}
		if err := groupStore.Update(existing); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "delete":
		if err := groupStore.Delete(req.ID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// Move users from deleted group to default.
		if tokenStore != nil {
			def := groupStore.GetDefault()
			for _, t := range tokenStore.List() {
				if t.GroupID == req.ID {
					tokenStore.SetGroupID(t.Token, def.ID)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "set_default":
		if err := groupStore.SetDefault(req.ID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "assign_user":
		if req.Token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token is required"})
			return
		}
		if tokenStore != nil {
			tokenStore.SetGroupID(req.Token, req.GroupID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "bulk_assign":
		if tokenStore != nil {
			for _, t := range req.Tokens {
				tokenStore.SetGroupID(t, req.GroupID)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(req.Tokens)})

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
	}
}

// slugify creates a lowercase slug from a name.
func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		if r == ' ' {
			return '_'
		}
		return -1
	}, name)
	if len(name) > 32 {
		name = name[:32]
	}
	return name
}
