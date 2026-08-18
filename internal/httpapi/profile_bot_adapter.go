package httpapi

import (
	"lampac-go/internal/profile"
	"lampac-go/internal/tgauth"
)

// profileBotAdapter implements tgauth.ProfileManager on top of
// internal/profile.Store. Lives in httpapi (not in tgauth) so the bot
// package stays free of profile-store imports — keeps the dependency
// graph a clean DAG.
type profileBotAdapter struct{ s *profile.Store }

// NewProfileBotAdapter returns a tgauth.ProfileManager backed by the
// given profile store. The returned adapter is safe for concurrent use.
func NewProfileBotAdapter(s *profile.Store) tgauth.ProfileManager {
	if s == nil {
		return nil
	}
	return profileBotAdapter{s: s}
}

func (a profileBotAdapter) OwnedList(ownerTGID int64) []tgauth.ProfileSummary {
	list := a.s.ListByOwnerTG(ownerTGID)
	out := make([]tgauth.ProfileSummary, 0, len(list))
	for _, p := range list {
		out = append(out, tgauth.ProfileSummary{
			ID:       p.ID,
			Username: p.Username,
			HasPIN:   p.PINHash != "",
		})
	}
	return out
}

func (a profileBotAdapter) OwnedCreate(ownerTGID int64, username, pin string) (string, error) {
	p, err := a.s.CreateForTGOwner(ownerTGID, username, pin)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func (a profileBotAdapter) OwnedSetPIN(profileID string, ownerTGID int64, pin string) error {
	return a.s.SetPIN(profileID, ownerTGID, pin)
}

func (a profileBotAdapter) OwnedDelete(profileID string, ownerTGID int64) error {
	return a.s.DeleteOwned(profileID, ownerTGID)
}
