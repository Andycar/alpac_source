package geoip

import (
	"net"
	"sync"

	"github.com/oschwald/maxminddb-golang"
	"github.com/rs/zerolog/log"
)

// DB wraps a MaxMind GeoLite2-Country database for IP→country lookups.
// It is safe for concurrent use.
// If the database file is not found, all methods return empty strings gracefully.
type DB struct {
	mu     sync.RWMutex
	reader *maxminddb.Reader
}

// Open loads a GeoLite2-Country.mmdb file. Returns a usable (but no-op) DB even on error.
func Open(path string) *DB {
	db := &DB{}
	if path == "" {
		return db
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		log.Warn().Err(err).Str("path", path).Msg("geoip: failed to open database")
		return db
	}
	db.reader = r
	log.Info().Str("path", path).Msg("geoip: database loaded")
	return db
}

// Close closes the underlying MMDB reader.
func (db *DB) Close() {
	if db == nil {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.reader != nil {
		_ = db.reader.Close()
		db.reader = nil
	}
}

// countryRecord is the minimal struct for GeoLite2-Country lookups.
type countryRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

// Country returns the ISO 3166-1 alpha-2 country code for the given IP string.
// Returns "" if the database is not loaded or the IP is invalid/not found.
func (db *DB) Country(ipStr string) string {
	if db == nil {
		return ""
	}
	db.mu.RLock()
	r := db.reader
	db.mu.RUnlock()
	if r == nil {
		return ""
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}

	var rec countryRecord
	if err := r.Lookup(ip, &rec); err != nil {
		return ""
	}
	return rec.Country.ISOCode
}

// Available reports whether the database is loaded and usable.
func (db *DB) Available() bool {
	if db == nil {
		return false
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.reader != nil
}
