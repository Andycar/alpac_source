package tgauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AdminRole distinguishes the original config admin from delegated admins.
type AdminRole string

const (
	RoleSuperAdmin AdminRole = "super"
	RoleAdmin      AdminRole = "admin"
)

// AdminEntry represents a user with admin panel access.
type AdminEntry struct {
	TelegramID int64     `json:"telegram_id"`
	Role       AdminRole `json:"role"`
	AddedBy    int64     `json:"added_by"`
	AddedAt    time.Time `json:"added_at"`
	Note       string    `json:"note,omitempty"`
}

// AdminIDStore manages the list of admin panel users,
// persisted to database/tgauth/admin_ids.json.
type AdminIDStore struct {
	mu         sync.RWMutex
	entries    []AdminEntry
	filePath   string
	superAdmin int64
}

// NewAdminIDStore creates an admin store backed by JSON file.
// The superAdminID (from config) is always in the list as RoleSuperAdmin.
func NewAdminIDStore(dbDir string, superAdminID int64) *AdminIDStore {
	s := &AdminIDStore{
		filePath:   filepath.Join(dbDir, "database", "tgauth", "admin_ids.json"),
		superAdmin: superAdminID,
	}
	_ = s.load()
	s.ensureSuper()
	return s
}

// IsAdmin returns true if the given ID has any admin role.
func (s *AdminIDStore) IsAdmin(telegramID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.TelegramID == telegramID {
			return true
		}
	}
	return false
}

// IsSuperAdmin returns true only for the config-defined admin.
func (s *AdminIDStore) IsSuperAdmin(telegramID int64) bool {
	return telegramID == s.superAdmin
}

// SuperAdminID returns the configured super admin Telegram ID.
func (s *AdminIDStore) SuperAdminID() int64 {
	return s.superAdmin
}

// RoleOf returns the role string for a given ID.
func (s *AdminIDStore) RoleOf(telegramID int64) AdminRole {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.TelegramID == telegramID {
			return e.Role
		}
	}
	return ""
}

// List returns a copy of all admin entries.
func (s *AdminIDStore) List() []AdminEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AdminEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// AdminTelegramIDs returns the telegram IDs of all admins, satisfying the
// AdminIDLister interface used by Bot.NotifyAdmins.
func (s *AdminIDStore) AdminTelegramIDs() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]int64, 0, len(s.entries))
	for _, e := range s.entries {
		if e.TelegramID != 0 {
			ids = append(ids, e.TelegramID)
		}
	}
	return ids
}

// Add adds a delegated admin. Cannot add if already exists.
func (s *AdminIDStore) Add(entry AdminEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.TelegramID == entry.TelegramID {
			return nil // already exists
		}
	}
	if entry.Role == "" {
		entry.Role = RoleAdmin
	}
	if entry.AddedAt.IsZero() {
		entry.AddedAt = time.Now().UTC()
	}
	s.entries = append(s.entries, entry)
	return s.save()
}

// Remove removes an admin by telegram ID. Cannot remove the super admin.
func (s *AdminIDStore) Remove(telegramID int64) error {
	if telegramID == s.superAdmin {
		return nil // refuse to remove super
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].TelegramID == telegramID {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return s.save()
		}
	}
	return nil
}

func (s *AdminIDStore) ensureSuper() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.TelegramID == s.superAdmin {
			return
		}
	}
	s.entries = append([]AdminEntry{{
		TelegramID: s.superAdmin,
		Role:       RoleSuperAdmin,
		AddedAt:    time.Now().UTC(),
	}}, s.entries...)
	_ = s.save()
}

func (s *AdminIDStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var entries []AdminEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	s.entries = entries
	return nil
}

func (s *AdminIDStore) save() error {
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0644)
}
