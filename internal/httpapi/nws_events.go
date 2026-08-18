package httpapi

import (
	stdjson "encoding/json"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
)

// nwsBroadcastWarnOnce limits the rate of "cannot fan-out" warnings so a
// long-running anonymous-only deployment doesn't flood the log. A single
// process logs at most one warning per distinct condition.
var (
	nwsWarnNoTGRef   atomic.Bool
	nwsWarnNoDevices atomic.Bool
)

// nwsBroadcast is a convenience helper for handlers that need to push
// real-time sync events (bookmark, timecode, storage) to other devices
// of the same user via the NWS WebSocket hub.
//
// It is safe to call even when no NWS hub is available (server not wired)
// or when the uid/connectionId are empty — it simply returns without action.
//
// TG-auth fan-out: storage/bookmark/timecode handlers identify the user by
// `tg:{tg_id}` (shared across devices), but NWS clients register events with
// their per-device `lampac_unic_id` (random per install). A direct broadcast
// to `tg:{tg_id}` reaches nobody. When the userID has the tg: prefix, expand
// it to every bound device UID for that TG account and send to each.
func nwsBroadcast(senderConnID, uid, eventName string, eventData any) {
	if uid == "" || eventName == "" {
		return
	}
	hub := getNwsHub()
	if hub == nil {
		return
	}

	var dataStr string
	switch v := eventData.(type) {
	case string:
		dataStr = v
	default:
		raw, err := stdjson.Marshal(v)
		if err != nil {
			return
		}
		dataStr = string(raw)
	}

	if strings.HasPrefix(uid, "tg:") {
		if tgTokenStoreRef == nil {
			// Initialization race or feature mis-config: the TG token store
			// hasn't been wired into httpapi yet. Sending to literal "tg:NN"
			// reaches no subscriber, so the broadcast would silently drop.
			if nwsWarnNoTGRef.CompareAndSwap(false, true) {
				log.Printf("[nws] broadcast: tgTokenStoreRef is nil — cross-device sync for tg:* userIDs will not work until TG auth is initialized")
			}
			return
		}
		if tgID, err := strconv.ParseInt(uid[3:], 10, 64); err == nil && tgID != 0 {
			if token := tgTokenStoreRef.FindByTelegramID(tgID); token != nil && len(token.Devices) > 0 {
				uids := make([]string, 0, len(token.Devices))
				for _, d := range token.Devices {
					if d.UID != "" {
						uids = append(uids, d.UID)
					}
				}
				if len(uids) > 0 {
					hub.SendEventsToUIDs(senderConnID, uids, eventName, dataStr)
					return
				}
			}
			// TG user has no devices bound yet — the device that just wrote
			// hasn't completed bot /start <code> binding or AddDevice failed.
			// Log once so the operator notices; the write itself succeeded,
			// only the real-time fan-out is skipped.
			if nwsWarnNoDevices.CompareAndSwap(false, true) {
				log.Printf("[nws] broadcast: no devices bound to TG account; cross-device sync will activate once a second device authenticates")
			}
		}
		// Don't fall through — sending to literal "tg:NN" hits nobody.
		return
	}

	hub.SendEvents(senderConnID, uid, eventName, dataStr)
}

// getNwsHub returns the active NWS hub from the server singleton.
// Returns nil if the server hasn't been initialized yet.
func getNwsHub() *nwsHub {
	return liveNwsHub()
}
