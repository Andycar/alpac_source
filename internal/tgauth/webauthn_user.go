package tgauth

import (
	"github.com/go-webauthn/webauthn/webauthn"
)

// AdminWebAuthnUser implements webauthn.User interface for the single admin account.
// go-webauthn requires a User object for registration and authentication ceremonies.
type AdminWebAuthnUser struct {
	Store *WebAuthnStore
}

// Compile-time check.
var _ webauthn.User = (*AdminWebAuthnUser)(nil)

// WebAuthnID returns a stable, opaque user handle.
// For the single admin account we use a fixed value.
func (u *AdminWebAuthnUser) WebAuthnID() []byte {
	return []byte("alpac-admin")
}

// WebAuthnName returns the human-readable username.
func (u *AdminWebAuthnUser) WebAuthnName() string {
	return "admin"
}

// WebAuthnDisplayName returns the display name shown by authenticators.
func (u *AdminWebAuthnUser) WebAuthnDisplayName() string {
	return "Alpac Admin"
}

// WebAuthnCredentials returns the list of registered credentials.
func (u *AdminWebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	return u.Store.WebAuthnCredentials()
}
