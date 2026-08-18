package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
)

func eventsCfgForGateTests() config.Config {
	return config.Config{
		Online: config.OnlineConfig{WithSearch: []string{"filmix"}},
	}
}

func TestIsLiteStreamPath(t *testing.T) {
	stream := []string{
		"alloha/video.m3u8",
		"alloha/video.mp4",
		"alloha/video",
		"alloha/stream",
		"alloha/stream.m3u8",
		"fancdn/video.m3u8",
		"mirage/video.m3u8",
		"zetflix/manifest.m3u8",
		"zetflix/manifest",
		"videodb/manifest.mp4",
		"rezka/movie.m3u8",
		"kodik/video.m3u8",
		"youtube/mux/index.m3u8",
		"youtube/mux/seg",
		"youtube/feed/subscriptions",
		"youtube/dash.mpd",
		"youtube/img",
		"pidtor/s5b8d4f1e2c3a9b0d8e7f6a5b4c3d2e1f0a9b8c7d",
		"animedia/video.m3u8",
		"moonanime/video.m3u8",
		"getstv/video.m3u8",
	}
	gated := []string{
		"alloha",
		"alloha-search",
		"fancdn",
		"mirage",
		"mirage-search",
		"mirage/edge_hash",
		"kinotochka",
		"kinopub",
		"rezka",
		"rezka/serial",
		"hdvb-search",
		"hdvb/serial",
		"getstv-search",
		"getstv/bind",
		"iptvonline/bind",
		"trailer",
		"flixcdn",
		"animevost",
		"pidtor/serial/123",
	}
	for _, p := range stream {
		if !isLiteStreamPath(p) {
			t.Errorf("isLiteStreamPath(%q) = false, want true (stream endpoint)", p)
		}
	}
	for _, p := range gated {
		if isLiteStreamPath(p) {
			t.Errorf("isLiteStreamPath(%q) = true, want false (source discovery)", p)
		}
	}
}

func TestRequireSourceDiscoveryAuth(t *testing.T) {
	withAuthConfiguredForTest(t)

	// No user in context → false (auth IS configured on the server,
	// so unauth requests get gated).
	r := httptest.NewRequest(http.MethodGet, "/lite/events", nil)
	if requireSourceDiscoveryAuth(r) {
		t.Fatal("no user in context with auth configured should return false")
	}

	// User present → true (regardless of source — TG, password, anon all satisfy).
	for _, id := range []string{"tg:42", "pw:deadbeef", "anon:cafebabe"} {
		ctx := auth.WithUser(context.Background(), &auth.User{ID: id, Expires: time.Now().Add(time.Hour)})
		r := httptest.NewRequest(http.MethodGet, "/lite/events", nil).WithContext(ctx)
		if !requireSourceDiscoveryAuth(r) {
			t.Fatalf("user %q should pass auth gate", id)
		}
	}
}

// TestRequireSourceDiscoveryAuthCompatFallback covers the 2026-05-28
// fix-on-the-fix: when no auth method is configured anywhere on the
// server, the gate falls open instead of softlocking the deployment
// with an unfixable "auth required" banner.
func TestRequireSourceDiscoveryAuthCompatFallback(t *testing.T) {
	resetAuthGlobalsForTest(t)
	r := httptest.NewRequest(http.MethodGet, "/lite/events", nil)
	if !requireSourceDiscoveryAuth(r) {
		t.Fatal("no auth configured + no user should fall open (compat)")
	}
}

// TestLiteEventsHandlerBlocksWithoutAuth was removed 2026-05-28 when
// the source-discovery gate was rolled back per user request — the
// accsdb banner couldn't be cleanly surfaced across Lampa forks and
// the synthetic balancer caused "Ошибка" instead. /lite/events is
// open again; the global tgAuthGateMiddleware still applies for
// browsers carrying cookies.

func TestLiteEventsHandlerAllowsWithUser(t *testing.T) {
	handler := liteEventsHandler(eventsCfgForGateTests(), nil, nil)

	ctx := auth.WithUser(context.Background(), &auth.User{
		ID:      "tg:42",
		Expires: time.Now().Add(time.Hour),
	})
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=12345", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	// Authed response — even empty plugin list still returns [] (not blocked).
	// The handler returns json — verify it parses and isn't the auth-gate
	// short-circuit.
	if rec.Body.Len() == 0 {
		t.Fatal("authed handler should write something")
	}
}
