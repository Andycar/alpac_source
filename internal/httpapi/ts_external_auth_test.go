package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"lampac-go/internal/config"
)

func TestIPMatchesCIDRList(t *testing.T) {
	cases := []struct {
		ip   string
		list []string
		want bool
	}{
		{"127.0.0.1", nil, true}, // empty list = open
		{"127.0.0.1", []string{"127.0.0.0/8"}, true},
		{"10.0.0.5", []string{"127.0.0.0/8"}, false},
		{"10.0.0.5", []string{"127.0.0.0/8", "10.0.0.0/8"}, true},
		{"10.0.0.5", []string{"10.0.0.5"}, true}, // bare IP match
		{"10.0.0.6", []string{"10.0.0.5"}, false},
		{"::1", []string{"::1/128"}, true},
		{"not-an-ip", []string{"127.0.0.0/8"}, false},
	}
	for _, tc := range cases {
		if got := ipMatchesCIDRList(tc.ip, tc.list); got != tc.want {
			t.Errorf("ipMatchesCIDRList(%q, %v) = %v, want %v", tc.ip, tc.list, got, tc.want)
		}
	}
}

func TestCheckBasic(t *testing.T) {
	state := &tsExternalAuthState{
		cfg: config.TorrServerExternalAccess{Enable: true, Login: "ts", Password: "hunter2"},
	}

	mk := func(user, pass string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/ts/echo", nil)
		if user != "" || pass != "" {
			cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
			r.Header.Set("Authorization", "Basic "+cred)
		}
		return r
	}

	if !state.checkBasic(mk("ts", "hunter2")) {
		t.Error("correct creds rejected")
	}
	if state.checkBasic(mk("ts", "wrong")) {
		t.Error("wrong password accepted")
	}
	if state.checkBasic(mk("admin", "hunter2")) {
		t.Error("wrong login accepted")
	}
	if state.checkBasic(mk("", "")) {
		t.Error("no header accepted")
	}

	// Empty config = no acceptance.
	stateOff := &tsExternalAuthState{}
	if stateOff.checkBasic(mk("ts", "hunter2")) {
		t.Error("disabled state accepted creds")
	}
}

func TestExternalAuthMiddlewareFlow(t *testing.T) {
	reloadTSExternalAuth(config.Config{TorrServer: config.TorrServerConfig{
		ExternalAccess: config.TorrServerExternalAccess{
			Enable: true, Login: "ts", Password: "secret",
			AllowFrom: []string{"127.0.0.0/8", "::1/128"},
		},
	}})

	handler := tsExternalAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasExternalAuth(r) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("authed"))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("anon"))
		}
	}))

	// 1) No auth header → falls through as anon.
	{
		req := httptest.NewRequest(http.MethodGet, "/ts/torrents", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 || w.Body.String() != "anon" {
			t.Errorf("no-auth case: code=%d body=%q", w.Code, w.Body.String())
		}
	}

	// 2) Valid creds → tagged.
	{
		req := httptest.NewRequest(http.MethodGet, "/ts/torrents", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("ts:secret")))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 || w.Body.String() != "authed" {
			t.Errorf("valid case: code=%d body=%q", w.Code, w.Body.String())
		}
	}

	// 3) Wrong creds → 401 + counter.
	{
		req := httptest.NewRequest(http.MethodGet, "/ts/torrents", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("ts:nope")))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("wrong creds: code=%d, want 401", w.Code)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Error("missing WWW-Authenticate challenge")
		}
	}

	// 4) IP outside allowlist → 403 without challenge.
	{
		req := httptest.NewRequest(http.MethodGet, "/ts/torrents", nil)
		req.RemoteAddr = "8.8.8.8:1234"
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("ts:secret")))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("outside allowlist: code=%d, want 403", w.Code)
		}
	}

	// 5) Throttle: 5 fails in a row → 6th request locked out.
	{
		ip := "127.0.0.99"
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, "/ts/torrents", nil)
			req.RemoteAddr = ip + ":1234"
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("ts:nope")))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
		}
		// Now even valid creds are turned away.
		req := httptest.NewRequest(http.MethodGet, "/ts/torrents", nil)
		req.RemoteAddr = ip + ":1234"
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("ts:secret")))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("throttle: code=%d, want 401", w.Code)
		}
	}
}

func TestExternalAuthDisabledIsTransparent(t *testing.T) {
	reloadTSExternalAuth(config.Config{}) // ExternalAccess.Enable=false

	called := false
	handler := tsExternalAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if hasExternalAuth(r) {
			t.Error("hasExternalAuth=true when middleware disabled")
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/ts/torrents", nil)
	req.Header.Set("Authorization", "Basic Zm9vOmJhcg==") // foo:bar
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("downstream handler not called when middleware disabled")
	}
}
