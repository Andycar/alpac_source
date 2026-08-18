package tgauth

import (
	"sync"
	"time"
)

const devicePendingTTL = 15 * time.Minute

// DevicePendingRequest tracks a device-verification code for an already-authenticated user.
type DevicePendingRequest struct {
	Code       string
	UID        string // lampac_unic_id
	TelegramID int64  // already known (user is authenticated)
	Token      string // existing token of the user
	CreatedAt  time.Time
	Status     string // "waiting" | "confirmed"
	Label      string // device label from User-Agent
}

// DevicePendingStore is an in-memory cache of pending device verification codes.
type DevicePendingStore struct {
	mu       sync.Mutex
	requests map[string]*DevicePendingRequest // key: code
}

// NewDevicePendingStore creates an empty device-pending store.
func NewDevicePendingStore() *DevicePendingStore {
	return &DevicePendingStore{
		requests: make(map[string]*DevicePendingRequest),
	}
}

// Create generates a 6-digit code for verifying a new device.
func (dp *DevicePendingStore) Create(tgID int64, token, uid, label string) *DevicePendingRequest {
	dp.mu.Lock()
	defer dp.mu.Unlock()

	dp.cleanExpiredLocked()

	// Reuse existing pending for same uid (avoid flooding bot with multiple codes)
	for _, r := range dp.requests {
		if r.UID == uid && r.TelegramID == tgID && r.Status == "waiting" {
			return r
		}
	}

	code := dp.generateCode()
	req := &DevicePendingRequest{
		Code:       code,
		UID:        uid,
		TelegramID: tgID,
		Token:      token,
		CreatedAt:  time.Now(),
		Status:     "waiting",
		Label:      label,
	}
	dp.requests[code] = req
	return req
}

// FindByCode returns the pending request for the given code, or nil.
func (dp *DevicePendingStore) FindByCode(code string) (*DevicePendingRequest, bool) {
	dp.mu.Lock()
	defer dp.mu.Unlock()
	dp.cleanExpiredLocked()
	r, ok := dp.requests[code]
	if !ok {
		return nil, false
	}
	return r, true
}

// Confirm marks a device-verification code as confirmed.
func (dp *DevicePendingStore) Confirm(code string) bool {
	dp.mu.Lock()
	defer dp.mu.Unlock()
	r, ok := dp.requests[code]
	if !ok {
		return false
	}
	r.Status = "confirmed"
	return true
}

// FindByUID returns the pending request for the given uid, or nil.
// Used for polling from the client.
func (dp *DevicePendingStore) FindByUID(uid string) (*DevicePendingRequest, bool) {
	if uid == "" {
		return nil, false
	}
	dp.mu.Lock()
	defer dp.mu.Unlock()
	dp.cleanExpiredLocked()
	for _, r := range dp.requests {
		if r.UID == uid {
			return r, true
		}
	}
	return nil, false
}

func (dp *DevicePendingStore) cleanExpiredLocked() {
	cutoff := time.Now().Add(-devicePendingTTL)
	for k, r := range dp.requests {
		if r.CreatedAt.Before(cutoff) && r.Status != "confirmed" {
			delete(dp.requests, k)
		}
	}
}

func (dp *DevicePendingStore) generateCode() string {
	for range 100 {
		code := randomAlphaNum(6)
		if _, exists := dp.requests[code]; !exists {
			return code
		}
	}
	return randomAlphaNum(8) // fallback
}
