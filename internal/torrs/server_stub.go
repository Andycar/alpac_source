//go:build !torrs

package torrs

import "io"

// BTServer is a no-op stub when built without torrs tag.
type BTServer struct{}

// ActiveTorrent is unused in stub mode.
type ActiveTorrent struct{}

// New returns nil when torrs is not enabled.
func New(_ string, _ TorrsConfig) (*BTServer, error) { return nil, nil }

// IsAvailable returns false when built without torrs tag.
func IsAvailable() bool { return false }

func (s *BTServer) Close()                                                                  {}
func (s *BTServer) Add(_, _, _, _ string, _ bool) (*TorrentInfo, error)                     { return nil, nil }
func (s *BTServer) AddWithOwner(_, _, _, _, _ string, _ bool) (*TorrentInfo, error)         { return nil, nil }
func (s *BTServer) AddFromBytes(_ []byte, _, _, _ string, _ bool) (*TorrentInfo, error)     { return nil, nil }
func (s *BTServer) Get(_ string) (*TorrentInfo, error)                                      { return nil, nil }
func (s *BTServer) List() []TorrentInfo                                                     { return nil }
func (s *BTServer) ListByOwner(_ string) []TorrentInfo                                      { return nil }
func (s *BTServer) Remove(_ string)                                                         {}
func (s *BTServer) Drop(_ string)                                                           {}
func (s *BTServer) Set(_, _, _, _ string)                                                   {}
func (s *BTServer) Stream(_ string, _ int) (io.ReadSeeker, int64, string, error)            { return nil, 0, "", nil }
func (s *BTServer) Preload(_ string, _ int)                                                 {}
func (s *BTServer) GetCacheStatus(_ string) CacheStatus                                     { return CacheStatus{} }
func (s *BTServer) GetSettings() Settings                                                   { return Settings{} }
func (s *BTServer) SetSettings(_ Settings) error                                            { return nil }
func (s *BTServer) Playlist(_, _ string) (string, error)                                    { return "", nil }
func (s *BTServer) PlaylistAll(_ string) string                                             { return "" }
func (s *BTServer) PlaylistByOwner(_, _ string) string                                      { return "" }
func (s *BTServer) Health() HealthInfo                                                      { return HealthInfo{} }
func (s *BTServer) DiskCleanupReport() CleanupReport                                        { return CleanupReport{} }
