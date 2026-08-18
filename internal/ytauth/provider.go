package ytauth

import (
	"context"
	"time"
)

// Provider implements tgauth.YouTubeAuthProvider.
type Provider struct {
	store *Store
	api   *APIClient
	oauth OAuthConfig
}

// NewProvider creates a YouTubeAuthProvider.
func NewProvider(store *Store, api *APIClient, oauth OAuthConfig) *Provider {
	return &Provider{store: store, api: api, oauth: oauth}
}

// StartAuth initiates the Google Device Flow. Returns user code, verification URL,
// and a channel that receives nil on success or an error.
func (p *Provider) StartAuth(tgID int64) (string, string, <-chan error, error) {
	dc, err := RequestDeviceCode(p.oauth)
	if err != nil {
		return "", "", nil, err
	}

	done := make(chan error, 1)
	go func() {
		tr, err := PollForToken(p.oauth, dc.DeviceCode, dc.Interval, dc.ExpiresIn)
		if err != nil {
			done <- err
			return
		}

		ut := UserToken{
			TelegramID:   tgID,
			AccessToken:  tr.AccessToken,
			RefreshToken: tr.RefreshToken,
			ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		}

		// Save token first so ensureValidToken() can find it.
		_ = p.store.Put(ut)

		// Try to get channel title and update.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		title, titleErr := p.api.GetChannelTitle(ctx, tgID)
		cancel()
		if titleErr == nil && title != "" {
			ut.ChannelTitle = title
			_ = p.store.Put(ut)
		}

		done <- nil
	}()

	return dc.UserCode, dc.VerificationURL, done, nil
}

// IsLinked returns true if the user has a YouTube token.
func (p *Provider) IsLinked(tgID int64) bool {
	return p.store.Get(tgID) != nil
}

// Unlink removes the user's YouTube token.
func (p *Provider) Unlink(tgID int64) error {
	return p.store.Delete(tgID)
}

// ChannelTitle returns the cached YouTube channel title for the user.
func (p *Provider) ChannelTitle(tgID int64) string {
	ut := p.store.Get(tgID)
	if ut == nil {
		return ""
	}
	return ut.ChannelTitle
}
