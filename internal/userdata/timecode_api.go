package userdata

import (
	"crypto/md5"
	"encoding/hex"
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"lampac-go/internal/auth"
)

var timecodeMu sync.Mutex

type timecodeUserData map[string]map[string]string

func TimecodeAllHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cardID := strings.TrimSpace(r.URL.Query().Get("card_id"))
		// all=1 → flat dump of EVERY card's timecodes for this profile, in the
		// same {fileId: jsonString} shape the client writes into file_view.
		// Needed on profile switch: file_view is wiped, and the per-card pull
		// (card_id=…) only restores the card you're currently viewing, so the
		// "continue watching" timeline came back empty. fileIds are globally
		// unique (hash of the file path) so flattening across cards is safe.
		all := r.URL.Query().Get("all") == "1"
		if cardID == "" && !all {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		userID := timecodeUserID(r)
		if userID == "" {
			// Anonymous request — no cross-device identity available.
			// Return empty so client falls back to its local file_view cache;
			// avoids cross-NAT pollution from sharing an IP-keyed bucket.
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		store, err := loadTimecodeUser(userID)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		if all {
			flat := make(map[string]string)
			for _, cardData := range store {
				for fileID, tc := range cardData {
					flat[fileID] = tc
				}
			}
			writeJSON(w, http.StatusOK, flat)
			return
		}

		cardData, ok := store[cardID]
		if !ok || len(cardData) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		writeJSON(w, http.StatusOK, cardData)
	}
}

func TimecodeAddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cardID := strings.TrimSpace(r.URL.Query().Get("card_id"))
		id := strings.TrimSpace(r.FormValue("id"))
		data := strings.TrimSpace(r.FormValue("data"))
		if id == "" || data == "" || cardID == "" {
			writeJSON(w, http.StatusOK, map[string]any{"success": false})
			return
		}

		userID := timecodeUserID(r)
		if userID == "" {
			// Anonymous — no persistence, but report success so the client
			// keeps the local file_view in localStorage and doesn't retry.
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
			return
		}

		timecodeMu.Lock()
		defer timecodeMu.Unlock()

		store, _ := loadTimecodeUser(userID)
		if store == nil {
			store = make(timecodeUserData)
		}
		if store[cardID] == nil {
			store[cardID] = make(map[string]string)
		}
		store[cardID][id] = data

		if err := saveTimecodeUser(userID, store); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"success": false})
			return
		}

		// Broadcast timecode change to other devices of the same user via NWS.
		// Owner uid (no profile suffix) — NWS resolves device connections by
		// the bare identity; profile_id rides the payload so receivers apply
		// it only when the same sub-profile is active.
		connID := r.URL.Query().Get("connectionId")
		deps.NwsBroadcast(connID, timecodeOwnerOnly(r), "timecode", map[string]any{
			"card_id":    cardID,
			"id":         id,
			"data":       data,
			"profile_id": requestProfileID(r),
		})

		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

// timecodeUserID resolves the cross-device identity for a timecode request.
// Returns empty string for anonymous callers (no auth context, no ?uid=, no
// X-User-Uid header, no `uid` cookie). The previous client-IP fallback
// caused two devices behind the same NAT to overwrite each other's timecodes
// and mobile users on changing IPs to lose them silently — see
// `storageUserUID` for the underlying resolution priority.
func timecodeUserID(r *http.Request) string {
	userID := timecodeOwnerOnly(r)

	profileID := strings.TrimSpace(r.URL.Query().Get("profile_id"))
	if profileID != "" && profileID != "0" && userID != "" {
		return userID + "_" + profileID
	}
	return userID
}

// timecodeOwnerOnly mirrors storageUserUID without the client-IP fallback.
// Kept as a thin local helper so the storage endpoint (where IP fallback is
// historically expected for backup/restore flows) is unaffected.
func timecodeOwnerOnly(r *http.Request) string {
	if user, ok := auth.UserFromContext(r.Context()); ok && user != nil && user.ID != "" {
		return user.ID
	}
	q := r.URL.Query()
	candidates := []string{
		q.Get("uid"),
		q.Get("user_uid"),
		q.Get("id"),
		r.Header.Get("X-User-Uid"),
		cookieValue(r, "uid"),
	}
	for _, v := range candidates {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func loadTimecodeUser(userID string) (timecodeUserData, error) {
	path := timecodeUserPath(userID)
	data, err := os.ReadFile(path)
	if err != nil {
		return make(timecodeUserData), err
	}

	var out timecodeUserData
	if err := stdjson.Unmarshal(data, &out); err != nil {
		return make(timecodeUserData), err
	}
	if out == nil {
		out = make(timecodeUserData)
	}
	return out, nil
}

func saveTimecodeUser(userID string, store timecodeUserData) error {
	path := timecodeUserPath(userID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := stdjson.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func timecodeUserPath(userID string) string {
	sum := md5.Sum([]byte(userID))
	name := hex.EncodeToString(sum[:]) + ".json"
	return relToRuntime(filepath.Join("database", "timecode", name))
}
