package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Settings are the admin-UI overrides persisted next to the history file
// (database/updater/settings.json). They layer on top of the [updater]
// section of config.toml, so an admin can move a server onto a private
// update channel from the web panel without editing the config by hand.
type Settings struct {
	// Channel overrides [updater].channel when non-empty.
	Channel string `json:"channel,omitempty"`
	// ServerToken overrides [updater].server_token when non-empty. It is the
	// password of a protected channel on the update server.
	ServerToken string `json:"server_token,omitempty"`
}

// SettingsPath resolves the settings file location from the history path —
// both live in the same database/updater/ directory.
func SettingsPath(historyPath string) string {
	if historyPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(historyPath), "settings.json")
}

// LoadSettings reads the override file. A missing file returns zero
// Settings and no error.
func LoadSettings(path string) (Settings, error) {
	var s Settings
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	err = json.Unmarshal(data, &s)
	s.Channel = strings.ToLower(strings.TrimSpace(s.Channel))
	s.ServerToken = strings.TrimSpace(s.ServerToken)
	return s, err
}

// SaveSettings writes the override file (creating the directory), or
// removes it when the settings are empty so config.toml wins again.
func SaveSettings(path string, s Settings) error {
	if path == "" {
		return nil
	}
	s.Channel = strings.ToLower(strings.TrimSpace(s.Channel))
	s.ServerToken = strings.TrimSpace(s.ServerToken)
	if s.Channel == "" && s.ServerToken == "" {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ApplyTo layers non-empty overrides onto a runtime config.
func (s Settings) ApplyTo(cfg *Config) {
	if s.Channel != "" {
		cfg.Channel = s.Channel
	}
	if s.ServerToken != "" {
		cfg.ServerToken = s.ServerToken
	}
}
