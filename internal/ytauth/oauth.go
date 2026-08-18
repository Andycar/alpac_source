package ytauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	deviceCodeURL = "https://oauth2.googleapis.com/device/code"
	ytReadScope   = "https://www.googleapis.com/auth/youtube.readonly"
)

// tokenURL is a var, not a const, so tests can point the token exchange at a
// local server — the revoked-token path deletes user credentials and must not
// be able to misfire on a transient upstream error.
var tokenURL = "https://oauth2.googleapis.com/token"

// OAuthConfig holds Google OAuth 2.0 client credentials.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
}

// DeviceCodeResponse is returned by the device/code endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// TokenResponse is returned by the token endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

var (
	ErrAccessDenied = errors.New("user denied access")
	ErrExpired      = errors.New("device code expired")
	// ErrRefreshRevoked means Google answered invalid_grant: the user revoked
	// the app on myaccount.google.com/permissions, or the refresh token aged
	// out (7 days while the OAuth app is in Testing). The stored credential is
	// dead for good, so callers must purge it rather than retry — both to keep
	// the promise the privacy policy makes about revocation and to stop dead
	// bindings from accumulating forever.
	ErrRefreshRevoked = errors.New("refresh token revoked")
)

// RequestDeviceCode initiates the Device Flow.
func RequestDeviceCode(cfg OAuthConfig) (*DeviceCodeResponse, error) {
	form := url.Values{
		"client_id": {cfg.ClientID},
		"scope":     {ytReadScope},
	}
	resp, err := http.PostForm(deviceCodeURL, form)
	if err != nil {
		return nil, fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("device code HTTP %d: %s", resp.StatusCode, body)
	}

	var dc DeviceCodeResponse
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("parse device code: %w", err)
	}
	if dc.Interval < 5 {
		dc.Interval = 5
	}
	return &dc, nil
}

// PollForToken polls the token endpoint until the user authorizes or the code expires.
func PollForToken(cfg OAuthConfig, deviceCode string, interval, expiresIn int) (*TokenResponse, error) {
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	pause := time.Duration(interval) * time.Second

	form := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"device_code":   {deviceCode},
		"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	for {
		if time.Now().After(deadline) {
			return nil, ErrExpired
		}
		time.Sleep(pause)

		resp, err := http.PostForm(tokenURL, form)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var tr TokenResponse
		if json.Unmarshal(body, &tr) != nil {
			continue
		}

		switch tr.Error {
		case "":
			if tr.AccessToken != "" {
				return &tr, nil
			}
		case "authorization_pending":
			continue
		case "slow_down":
			pause += 5 * time.Second
		case "access_denied":
			return nil, ErrAccessDenied
		case "expired_token":
			return nil, ErrExpired
		default:
			return nil, fmt.Errorf("oauth error: %s — %s", tr.Error, tr.ErrorDesc)
		}
	}
}

// RefreshAccessToken exchanges a refresh token for a new access token.
func RefreshAccessToken(cfg OAuthConfig, refreshToken string) (*TokenResponse, error) {
	form := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	resp, err := http.PostForm(tokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Google reports a revoked/expired refresh token as HTTP 400 with
	// {"error":"invalid_grant"}, so the error code has to be read before the
	// status check — otherwise revocation is indistinguishable from a
	// transient upstream failure and the dead credential is kept forever.
	var tr TokenResponse
	parseErr := json.Unmarshal(body, &tr)
	if parseErr == nil && tr.Error == "invalid_grant" {
		return nil, fmt.Errorf("%w: %s", ErrRefreshRevoked, tr.ErrorDesc)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("refresh HTTP %d: %s", resp.StatusCode, body)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parse refresh: %w", parseErr)
	}
	if tr.Error != "" {
		// Refresh token revoked or expired — user must re-auth.
		return nil, fmt.Errorf("refresh error: %s", tr.Error)
	}

	// Google refresh response doesn't include refresh_token; keep the old one.
	if tr.RefreshToken == "" {
		tr.RefreshToken = refreshToken
	}
	_ = strings.TrimSpace(tr.AccessToken) // ensure no whitespace
	return &tr, nil
}
