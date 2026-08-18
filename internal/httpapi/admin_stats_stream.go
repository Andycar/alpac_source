package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"
)

// tgAdminStatsStreamHandler is the SSE companion to tgAdminStatsHandler.
// It pulls the same JSON snapshot every `tickInterval` and emits it as a
// named `snapshot` event so the v2 dashboard can render live without
// polling.
//
// Implementation note: rather than duplicate the (large) stats assembly
// logic, we invoke the existing handler via httptest.ResponseRecorder. This
// keeps the two endpoints byte-for-byte compatible — any field added to
// /api/stats automatically appears in the stream.
func tgAdminStatsStreamHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, pl *proxylink.Manager) http.HandlerFunc {
	inner := tgAdminStatsHandler(store, adminStore, pl)
	return func(w http.ResponseWriter, r *http.Request) {
		// Re-auth so we get the same 401/redirect behaviour as the JSON endpoint.
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		// Emit initial frame immediately so the client paints without waiting
		// for the first tick.
		emit := func() bool {
			body := buildStatsBody(inner, r)
			if body == nil {
				return true
			}
			if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", body); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		if !emit() {
			return
		}

		// Tick. 3s gives sub-real-time feel without thrashing CPU. The dashboard
		// charts buffer 60 points → 3-min window per chart.
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		heart := time.NewTicker(20 * time.Second)
		defer heart.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !emit() {
					return
				}
			case <-heart.C:
				if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// buildStatsBody runs the existing /api/stats handler against a recorder and
// returns its response body. Skips emission on non-2xx (keeps the SSE quiet
// if something went wrong server-side).
func buildStatsBody(inner http.HandlerFunc, r *http.Request) []byte {
	rec := httptest.NewRecorder()
	// Strip query / body just in case the inner handler reads them — stats
	// doesn't, but defending against future changes is free.
	clone := r.Clone(r.Context())
	clone.Body = io.NopCloser(bytes.NewReader(nil))
	inner.ServeHTTP(rec, clone)
	if rec.Code < 200 || rec.Code >= 300 {
		return nil
	}
	return rec.Body.Bytes()
}
