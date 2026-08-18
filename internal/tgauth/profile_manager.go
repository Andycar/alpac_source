package tgauth

// ProfileSummary is the bot-facing view of a sync profile. Defined here so
// internal/tgauth doesn't have to import internal/profile (which would
// pull bcrypt + the JSON store into the bot package). The httpapi wiring
// owns the adapter — see internal/httpapi/profile_bot_adapter.go.
type ProfileSummary struct {
	ID       string
	Username string
	HasPIN   bool
}

// ProfileManager exposes the subset of profile operations that the
// Telegram bot needs to manage profiles owned by a TG user. All methods
// must be safe for concurrent invocation — Telegram updates arrive on a
// background long-poll goroutine.
//
// Implementations live outside this package; see
// internal/httpapi/profile_bot_adapter.go for the production one.
//
// All operations are scoped by `ownerTGID` so even a buggy bot handler
// can't act on profiles owned by someone else.
type ProfileManager interface {
	// OwnedList returns the profiles owned by this TG user. nil/empty is
	// a valid result when the user has no profiles yet.
	OwnedList(ownerTGID int64) []ProfileSummary

	// OwnedCreate registers a new profile under this TG owner. The pin
	// argument may be empty to defer PIN configuration. Returns the
	// profile ID and any of the sentinel errors below.
	OwnedCreate(ownerTGID int64, username, pin string) (id string, err error)

	// OwnedSetPIN rotates (or clears, when pin == "") the PIN of a profile
	// owned by this TG user.
	OwnedSetPIN(profileID string, ownerTGID int64, pin string) error

	// OwnedDelete removes a profile owned by this TG user, invalidating
	// every live session for it.
	OwnedDelete(profileID string, ownerTGID int64) error
}
