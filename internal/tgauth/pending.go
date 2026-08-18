package tgauth

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

const (
	StatusWaiting  = "waiting"
	StatusClaimed  = "claimed"
	StatusApproved = "approved"
	StatusRejected = "rejected"

	pendingTTL = 15 * time.Minute
)

// PendingRequest tracks a single authorization code from creation to approval.
type PendingRequest struct {
	Code       string
	UID        string // lampac_unic_id — Lampa device identifier (NAT-safe)
	ClientIP   string
	CreatedAt  time.Time
	TelegramID int64
	TGUsername string
	TGChatID   int64
	Status     string // waiting → claimed → approved / rejected
	Token      string // filled on approval
}

// PendingStore is an in-memory cache of pending auth codes.
type PendingStore struct {
	mu       sync.Mutex
	requests map[string]*PendingRequest
}

// NewPendingStore creates an empty pending store.
func NewPendingStore() *PendingStore {
	return &PendingStore{
		requests: make(map[string]*PendingRequest),
	}
}

// Create generates a random 6-char code and stores a pending request (IP-only, no UID).
// Used for browser auth flow where no device UID is available.
func (p *PendingStore) Create(clientIP string) *PendingRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clean expired while we hold the lock.
	p.cleanExpiredLocked()

	code := p.generateCode()
	req := &PendingRequest{
		Code:      code,
		ClientIP:  clientIP,
		CreatedAt: time.Now(),
		Status:    StatusWaiting,
	}
	p.requests[code] = req
	return req
}

// CreateForUID generates a code and stores a pending request keyed by UID.
// This is the primary method for Lampa app auth — NAT-safe, no IP collisions.
func (p *PendingStore) CreateForUID(uid, clientIP string) *PendingRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cleanExpiredLocked()

	code := p.generateCode()
	req := &PendingRequest{
		Code:      code,
		UID:       uid,
		ClientIP:  clientIP,
		CreatedAt: time.Now(),
		Status:    StatusWaiting,
	}
	p.requests[code] = req
	return req
}

// FindByCode returns the pending request for the given code, or nil.
func (p *PendingStore) FindByCode(code string) (*PendingRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanExpiredLocked()
	r, ok := p.requests[code]
	if !ok {
		return nil, false
	}
	return r, true
}

// FindByUID returns the most recent pending request for the given device UID.
// Priority: approved > claimed > waiting.
func (p *PendingStore) FindByUID(uid string) (*PendingRequest, bool) {
	if uid == "" {
		return nil, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanExpiredLocked()

	var bestApproved, bestClaimed, bestWaiting *PendingRequest
	for _, r := range p.requests {
		if r.UID != uid {
			continue
		}
		switch r.Status {
		case StatusApproved:
			if bestApproved == nil || r.CreatedAt.After(bestApproved.CreatedAt) {
				bestApproved = r
			}
		case StatusClaimed:
			if bestClaimed == nil || r.CreatedAt.After(bestClaimed.CreatedAt) {
				bestClaimed = r
			}
		case StatusWaiting:
			if bestWaiting == nil || r.CreatedAt.After(bestWaiting.CreatedAt) {
				bestWaiting = r
			}
		}
	}
	if bestApproved != nil {
		return bestApproved, true
	}
	if bestClaimed != nil {
		return bestClaimed, true
	}
	if bestWaiting != nil {
		return bestWaiting, true
	}
	return nil, false
}

// ClaimByCode marks a code as claimed by a Telegram user.
func (p *PendingStore) ClaimByCode(code string, telegramID int64, chatID int64, username string) (*PendingRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.requests[code]
	if !ok || r.Status != StatusWaiting {
		return nil, false
	}
	r.TelegramID = telegramID
	r.TGChatID = chatID
	r.TGUsername = username
	r.Status = StatusClaimed
	return r, true
}

// Approve sets the approval token on a pending request.
func (p *PendingStore) Approve(code, token string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.requests[code]
	if !ok {
		return false
	}
	r.Status = StatusApproved
	r.Token = token
	return true
}

// Unclaim reverts a claimed request back to waiting (e.g. when membership check fails).
func (p *PendingStore) Unclaim(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.requests[code]; ok && r.Status == StatusClaimed {
		r.Status = StatusWaiting
		r.TelegramID = 0
		r.TGChatID = 0
		r.TGUsername = ""
	}
}

// Consume removes an approved request (after the token has been picked up).
func (p *PendingStore) Consume(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.requests, code)
}

// Reject marks the request as rejected.
func (p *PendingStore) Reject(code string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.requests[code]
	if !ok {
		return false
	}
	r.Status = StatusRejected
	return true
}

// FindByClientIP returns the most recent pending request for the given IP.
// Searches in order: approved first, then claimed, then waiting.
// Skips UID-bound pendings — those must be found via FindByUID only.
func (p *PendingStore) FindByClientIP(ip string) (*PendingRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanExpiredLocked()

	var bestApproved, bestClaimed, bestWaiting *PendingRequest
	for _, r := range p.requests {
		if r.ClientIP != ip {
			continue
		}
		// UID-bound pendings belong to specific devices — never match by IP.
		if r.UID != "" {
			continue
		}
		switch r.Status {
		case StatusApproved:
			if bestApproved == nil || r.CreatedAt.After(bestApproved.CreatedAt) {
				bestApproved = r
			}
		case StatusClaimed:
			if bestClaimed == nil || r.CreatedAt.After(bestClaimed.CreatedAt) {
				bestClaimed = r
			}
		case StatusWaiting:
			if bestWaiting == nil || r.CreatedAt.After(bestWaiting.CreatedAt) {
				bestWaiting = r
			}
		}
	}
	if bestApproved != nil {
		return bestApproved, true
	}
	if bestClaimed != nil {
		return bestClaimed, true
	}
	if bestWaiting != nil {
		return bestWaiting, true
	}
	return nil, false
}

// CleanExpired removes stale entries.
func (p *PendingStore) CleanExpired() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanExpiredLocked()
}

func (p *PendingStore) cleanExpiredLocked() {
	now := time.Now()
	cutoff := now.Add(-pendingTTL)
	approvedCutoff := now.Add(-1 * time.Hour) // approved codes live up to 1 hour
	for k, r := range p.requests {
		if r.Status == StatusApproved {
			if r.CreatedAt.Before(approvedCutoff) {
				delete(p.requests, k)
			}
		} else if r.CreatedAt.Before(cutoff) {
			delete(p.requests, k)
		}
	}
}

func (p *PendingStore) generateCode() string {
	for range 100 {
		code := randomAlphaNum(6)
		if _, exists := p.requests[code]; !exists {
			return code
		}
	}
	return randomAlphaNum(8) // fallback
}

const alphaNumChars = "0123456789ABCDEFGHJKLMNPQRSTUVWXYZ" // 34 chars, no I/O to avoid confusion

func randomAlphaNum(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	out := make([]byte, n)
	for i := range b {
		out[i] = alphaNumChars[int(b[i])%len(alphaNumChars)]
	}
	return string(out)
}

// DurationLabel returns a human-readable Russian label for a day count.
func DurationLabel(days int) string {
	switch {
	case days == 1:
		return "1 день"
	case days >= 2 && days <= 4:
		return fmt.Sprintf("%d дня", days)
	case days == 30:
		return "1 месяц"
	case days == 90:
		return "3 месяца"
	case days == 365:
		return "1 год"
	default:
		return fmt.Sprintf("%d дней", days)
	}
}
