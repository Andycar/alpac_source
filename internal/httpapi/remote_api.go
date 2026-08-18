package httpapi

import (
	"net/http"
	"strconv"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// remoteAuthHandler validates TG Mini App initData and returns the user's device UIDs
// so the remote can connect to NWS with the correct event channel.
//
// The handler prioritises UIDs that have active NWS connections so the remote
// page talks to a live device rather than a stale or fallback identifier.
//
// POST /api/remote/auth
func remoteAuthHandler(tgStore *tgauth.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}

		// kitAuthFromRequest returns tgID as string (e.g. "123456789" or "bkit:xxx").
		// Parse to int64 for FindByTelegramID.
		var uids []string
		tgIDNum, parseErr := strconv.ParseInt(tgID, 10, 64)
		if parseErr == nil && tgStore != nil {
			if tok := tgStore.FindByTelegramID(tgIDNum); tok != nil {
				// Collect all device UIDs (lampac_unic_id).
				seen := make(map[string]bool, len(tok.Devices)+1)
				for _, dev := range tok.Devices {
					if dev.UID != "" && !seen[dev.UID] {
						seen[dev.UID] = true
						uids = append(uids, dev.UID)
					}
				}
				// The account token, always, as the LAST candidate. It used to be a
				// fallback for the empty-device-list case only, which quietly broke
				// every client that registers its NWS event channel under the token
				// instead of a device uid (the web SPA did exactly that): as soon as
				// the account bound a single native device, the token dropped out of
				// this list and the remote had no uid left to reach that client on.
				// Keeping it in the tail costs one extra 2s probe in the worst case
				// and makes already-deployed clients reachable again.
				if tok.Token != "" && !seen[tok.Token] {
					uids = append(uids, tok.Token)
				}
			}
		}

		// If no UIDs found, use TG ID as fallback.
		if len(uids) == 0 {
			uids = []string{tgID}
		}

		// Prioritise UIDs that have active NWS connections.
		// This ensures the remote talks to a device that is actually online.
		connectedCount := 0
		hub := getNwsHub()
		if hub != nil && len(uids) > 0 {
			hub.eventMu.RLock()
			uidOnline := make(map[string]bool, len(hub.eventClients))
			for _, u := range hub.eventClients {
				uidOnline[u] = true
			}
			hub.eventMu.RUnlock()

			var connected, offline []string
			for _, u := range uids {
				if uidOnline[u] {
					connected = append(connected, u)
				} else {
					offline = append(offline, u)
				}
			}
			connectedCount = len(connected)
			if len(connected) > 0 {
				uids = append(connected, offline...)
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"uid":       uids[0], // primary UID for event broadcasting
			"uids":      uids,    // all device UIDs (connected first)
			"connected": connectedCount,
		})
	}
}

// remoteDevicesHandler returns a list of NWS-connected devices for the user's UIDs.
//
// POST /api/remote/devices
func remoteDevicesHandler(tgStore *tgauth.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}

		hub := getNwsHub()
		if hub == nil {
			writeJSON(w, http.StatusOK, map[string]any{"devices": []any{}})
			return
		}

		// Collect all UIDs for this TG user.
		uidSet := make(map[string]bool)
		tgIDNum, parseErr := strconv.ParseInt(tgID, 10, 64)
		if parseErr == nil && tgStore != nil {
			if tok := tgStore.FindByTelegramID(tgIDNum); tok != nil {
				for _, dev := range tok.Devices {
					if dev.UID != "" {
						uidSet[dev.UID] = true
					}
				}
				uidSet[tok.Token] = true
			}
		}
		uidSet[tgID] = true

		type deviceEntry struct {
			ConnID    string `json:"conn_id"`
			UID       string `json:"uid"`
			UserAgent string `json:"user_agent"`
			IP        string `json:"ip"`
		}

		hub.eventMu.RLock()
		hub.mu.RLock()

		var devices []deviceEntry
		for cid, u := range hub.eventClients {
			if !uidSet[u] {
				continue
			}
			if c, ok := hub.connections[cid]; ok {
				devices = append(devices, deviceEntry{
					ConnID:    cid,
					UID:       u,
					UserAgent: c.userAgent,
					IP:        c.ip,
				})
			}
		}

		hub.mu.RUnlock()
		hub.eventMu.RUnlock()

		if devices == nil {
			devices = []deviceEntry{}
		}

		writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
	}
}
