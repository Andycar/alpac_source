package ytauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withTokenServer points the token exchange at a stub returning the given
// status and body, and restores the real endpoint afterwards.
func withTokenServer(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := tokenURL
	tokenURL = srv.URL
	t.Cleanup(func() {
		tokenURL = prev
		srv.Close()
	})
}

// Google reports revocation as HTTP 400 + invalid_grant; that must be a typed
// error so the caller knows the credential is dead for good.
func TestRefreshAccessTokenFlagsRevocation(t *testing.T) {
	withTokenServer(t, http.StatusBadRequest,
		`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)

	_, err := RefreshAccessToken(OAuthConfig{ClientID: "id", ClientSecret: "secret"}, "dead-refresh-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrRefreshRevoked) {
		t.Fatalf("expected ErrRefreshRevoked, got %v", err)
	}
}

// Transient upstream failures must NOT be mistaken for revocation — that path
// erases the user's credential.
func TestRefreshAccessTokenDoesNotFlagTransientErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"server error html", http.StatusInternalServerError, `<html>502 Bad Gateway</html>`},
		{"rate limited", http.StatusTooManyRequests, `{"error":"rate_limit_exceeded"}`},
		{"other oauth error", http.StatusBadRequest, `{"error":"invalid_request"}`},
		{"empty body", http.StatusBadGateway, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTokenServer(t, tc.status, tc.body)
			_, err := RefreshAccessToken(OAuthConfig{ClientID: "id"}, "live-refresh-token")
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, ErrRefreshRevoked) {
				t.Fatalf("transient failure misreported as revocation: %v", err)
			}
		})
	}
}

// A successful refresh keeps the existing refresh token (Google omits it).
func TestRefreshAccessTokenKeepsRefreshToken(t *testing.T) {
	withTokenServer(t, http.StatusOK,
		`{"access_token":"new-access","expires_in":3599,"token_type":"Bearer"}`)

	tr, err := RefreshAccessToken(OAuthConfig{ClientID: "id"}, "keep-me")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.AccessToken != "new-access" {
		t.Fatalf("access token = %q", tr.AccessToken)
	}
	if tr.RefreshToken != "keep-me" {
		t.Fatalf("refresh token = %q, want the original to be carried over", tr.RefreshToken)
	}
}

// End-to-end: a revoked binding is purged from disk, so a user who revokes on
// Google's side genuinely has their stored credential deleted.
func TestEnsureValidTokenPurgesRevokedBinding(t *testing.T) {
	withTokenServer(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)

	dbDir := t.TempDir()
	store := NewStore(dbDir)
	if err := store.Put(UserToken{
		TelegramID:   99,
		AccessToken:  "expired-access",
		RefreshToken: "revoked-refresh",
		// Already expired, so ensureValidToken must attempt a refresh.
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	api := NewAPIClient(store, OAuthConfig{ClientID: "id"})
	if _, err := api.ensureValidToken(99); err == nil {
		t.Fatal("expected refresh to fail")
	}
	if store.Get(99) != nil {
		t.Fatal("revoked credential still stored")
	}
	if NewStore(dbDir).Get(99) != nil {
		t.Fatal("revoked credential still on disk after reload")
	}
}

// A transient refresh failure must leave the binding intact.
func TestEnsureValidTokenKeepsBindingOnTransientFailure(t *testing.T) {
	withTokenServer(t, http.StatusInternalServerError, `upstream down`)

	dbDir := t.TempDir()
	store := NewStore(dbDir)
	if err := store.Put(UserToken{TelegramID: 100, AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	api := NewAPIClient(store, OAuthConfig{ClientID: "id"})
	if _, err := api.ensureValidToken(100); err == nil {
		t.Fatal("expected refresh to fail")
	}
	if store.Get(100) == nil {
		t.Fatal("binding wrongly deleted on a transient failure")
	}
}
