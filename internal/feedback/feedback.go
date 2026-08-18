package feedback

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/tgauth"
	"lampac-go/internal/userdata"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type FeedbackCategory string

const (
	FBCatHelp    FeedbackCategory = "help"
	FBCatThanks  FeedbackCategory = "thanks"
	FBCatBug     FeedbackCategory = "bug"
	FBCatFeature FeedbackCategory = "feature"
	FBCatOther   FeedbackCategory = "other"
)

var validFBCategories = map[FeedbackCategory]bool{
	FBCatHelp: true, FBCatThanks: true, FBCatBug: true,
	FBCatFeature: true, FBCatOther: true,
}

type FeedbackPriority string

const (
	FBPriLow      FeedbackPriority = "low"
	FBPriMedium   FeedbackPriority = "medium"
	FBPriHigh     FeedbackPriority = "high"
	FBPriCritical FeedbackPriority = "critical"
)

var ValidFBPriorities = map[FeedbackPriority]bool{
	FBPriLow: true, FBPriMedium: true, FBPriHigh: true, FBPriCritical: true,
}

type FeedbackStatus string

const (
	FBStatusOpen       FeedbackStatus = "open"
	FBStatusInProgress FeedbackStatus = "in_progress"
	FBStatusResolved   FeedbackStatus = "resolved"
	FBStatusClosed     FeedbackStatus = "closed"
)

var ValidFBStatuses = map[FeedbackStatus]bool{
	FBStatusOpen: true, FBStatusInProgress: true,
	FBStatusResolved: true, FBStatusClosed: true,
}

type FeedbackReply struct {
	ID        string `json:"id"`
	IsAdmin   bool   `json:"is_admin"`
	AdminID   int64  `json:"admin_id,omitempty"`
	AdminName string `json:"admin_name,omitempty"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

type FeedbackTicket struct {
	ID        string           `json:"id"`
	UserID    string           `json:"user_id"`
	UserName  string           `json:"user_name,omitempty"`
	Category  FeedbackCategory `json:"category"`
	Priority  FeedbackPriority `json:"priority"`
	Subject   string           `json:"subject"`
	Message   string           `json:"message"`
	Status    FeedbackStatus   `json:"status"`
	Replies   []FeedbackReply  `json:"replies,omitempty"`
	CreatedAt string           `json:"created_at"`
	UpdatedAt string           `json:"updated_at"`
}

type FeedbackFilter struct {
	Status   FeedbackStatus
	Category FeedbackCategory
	Priority FeedbackPriority
	Search   string
}

type FeedbackStats struct {
	Total      int            `json:"total"`
	Open       int            `json:"open"`
	InProgress int            `json:"in_progress"`
	Resolved   int            `json:"resolved"`
	Closed     int            `json:"closed"`
	ByCategory map[string]int `json:"by_category"`
	ByPriority map[string]int `json:"by_priority"`
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

type FeedbackStore struct {
	mu       sync.RWMutex
	tickets  []FeedbackTicket
	byID     map[string]int      // ticket ID → index
	byUser   map[string][]string // userID → ticket IDs
	filePath string

	// simple rate limiter: userID → timestamps
	rateMu sync.Mutex
	rates  map[string][]time.Time
}

func NewFeedbackStore() *FeedbackStore {
	s := &FeedbackStore{
		byID:     make(map[string]int),
		byUser:   make(map[string][]string),
		filePath: relToRuntime(filepath.Join("database", "feedback", "tickets.json")),
		rates:    make(map[string][]time.Time),
	}
	if err := s.load(); err != nil {
		log.Debug().Err(err).Msg("feedback: no existing tickets file, starting empty")
	}
	return s
}

// --- persistence ---

type feedbackFile struct {
	Tickets []FeedbackTicket `json:"tickets"`
}

func (s *FeedbackStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var f feedbackFile
	if err := stdjson.Unmarshal(data, &f); err != nil {
		return err
	}
	s.tickets = f.Tickets
	s.rebuildIndex()
	return nil
}

func (s *FeedbackStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return err
	}
	f := feedbackFile{Tickets: s.tickets}
	data, err := stdjson.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.filePath)
}

func (s *FeedbackStore) rebuildIndex() {
	s.byID = make(map[string]int, len(s.tickets))
	s.byUser = make(map[string][]string)
	for i, t := range s.tickets {
		s.byID[t.ID] = i
		s.byUser[t.UserID] = append(s.byUser[t.UserID], t.ID)
	}
}

// --- CRUD ---

func (s *FeedbackStore) Create(t FeedbackTicket) (FeedbackTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	t.ID = uuid.New().String()
	t.Status = FBStatusOpen
	t.CreatedAt = now
	t.UpdatedAt = now
	if t.Priority == "" {
		t.Priority = FBPriMedium
	}

	s.tickets = append(s.tickets, t)
	s.byID[t.ID] = len(s.tickets) - 1
	s.byUser[t.UserID] = append(s.byUser[t.UserID], t.ID)

	if err := s.save(); err != nil {
		log.Warn().Err(err).Msg("feedback: save failed")
	}
	return t, nil
}

func (s *FeedbackStore) Get(id string) (FeedbackTicket, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx, ok := s.byID[id]
	if !ok {
		return FeedbackTicket{}, false
	}
	return s.tickets[idx], true
}

func (s *FeedbackStore) ListByUser(userID string) []FeedbackTicket {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := s.byUser[userID]
	out := make([]FeedbackTicket, 0, len(ids))
	for _, id := range ids {
		if idx, ok := s.byID[id]; ok {
			out = append(out, s.tickets[idx])
		}
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *FeedbackStore) ListAll(f FeedbackFilter) []FeedbackTicket {
	s.mu.RLock()
	defer s.mu.RUnlock()

	search := strings.ToLower(f.Search)
	out := make([]FeedbackTicket, 0, len(s.tickets))
	for _, t := range s.tickets {
		if f.Status != "" && t.Status != f.Status {
			continue
		}
		if f.Category != "" && t.Category != f.Category {
			continue
		}
		if f.Priority != "" && t.Priority != f.Priority {
			continue
		}
		if search != "" {
			if !strings.Contains(strings.ToLower(t.Subject), search) &&
				!strings.Contains(strings.ToLower(t.Message), search) &&
				!strings.Contains(strings.ToLower(t.UserName), search) &&
				!strings.Contains(strings.ToLower(t.UserID), search) {
				continue
			}
		}
		out = append(out, t)
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *FeedbackStore) UpdateStatus(id string, status FeedbackStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.byID[id]
	if !ok {
		return errTicketNotFound
	}
	s.tickets[idx].Status = status
	s.tickets[idx].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.save()
}

func (s *FeedbackStore) UpdatePriority(id string, priority FeedbackPriority) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.byID[id]
	if !ok {
		return errTicketNotFound
	}
	s.tickets[idx].Priority = priority
	s.tickets[idx].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.save()
}

func (s *FeedbackStore) AddReply(id string, reply FeedbackReply) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.byID[id]
	if !ok {
		return errTicketNotFound
	}
	reply.ID = uuid.New().String()
	reply.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	s.tickets[idx].Replies = append(s.tickets[idx].Replies, reply)
	s.tickets[idx].UpdatedAt = reply.CreatedAt
	return s.save()
}

func (s *FeedbackStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.byID[id]
	if !ok {
		return errTicketNotFound
	}
	// remove from slice
	userID := s.tickets[idx].UserID
	s.tickets = append(s.tickets[:idx], s.tickets[idx+1:]...)
	// rebuild index
	s.rebuildIndex()
	_ = userID
	return s.save()
}

func (s *FeedbackStore) Stats() FeedbackStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := FeedbackStats{
		Total:      len(s.tickets),
		ByCategory: make(map[string]int),
		ByPriority: make(map[string]int),
	}
	for _, t := range s.tickets {
		switch t.Status {
		case FBStatusOpen:
			st.Open++
		case FBStatusInProgress:
			st.InProgress++
		case FBStatusResolved:
			st.Resolved++
		case FBStatusClosed:
			st.Closed++
		}
		st.ByCategory[string(t.Category)]++
		st.ByPriority[string(t.Priority)]++
	}
	return st
}

// --- FeedbackSubmitter interface (for tgauth.Bot) ---

// CreateTicket implements tgauth.FeedbackSubmitter.
func (s *FeedbackStore) CreateTicket(userID, userName, category, priority, subject, message string) (string, error) {
	t := FeedbackTicket{
		UserID:   userID,
		UserName: userName,
		Category: FeedbackCategory(category),
		Priority: FeedbackPriority(priority),
		Subject:  subject,
		Message:  message,
	}
	created, err := s.Create(t)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// UserTickets implements tgauth.FeedbackSubmitter.
func (s *FeedbackStore) UserTickets(userID string) []tgauth.FeedbackTicketInfo {
	tickets := s.ListByUser(userID)
	out := make([]tgauth.FeedbackTicketInfo, len(tickets))
	for i, t := range tickets {
		replies := 0
		if t.Replies != nil {
			replies = len(t.Replies)
		}
		lastAdmin := false
		if replies > 0 {
			lastAdmin = t.Replies[replies-1].IsAdmin
		}
		out[i] = tgauth.FeedbackTicketInfo{
			ID:             t.ID,
			Subject:        t.Subject,
			Status:         string(t.Status),
			Category:       string(t.Category),
			CreatedAt:      t.CreatedAt,
			Replies:        replies,
			LastReplyAdmin: lastAdmin,
			UpdatedAt:      t.UpdatedAt,
		}
	}
	return out
}

// GetTicket implements tgauth.FeedbackSubmitter.
func (s *FeedbackStore) GetTicket(id string) (tgauth.FeedbackTicketDetail, bool) {
	t, ok := s.Get(id)
	if !ok {
		return tgauth.FeedbackTicketDetail{}, false
	}
	replies := make([]tgauth.FeedbackReplyInfo, len(t.Replies))
	for i, r := range t.Replies {
		replies[i] = tgauth.FeedbackReplyInfo{
			IsAdmin:   r.IsAdmin,
			Author:    r.AdminName,
			Message:   r.Message,
			CreatedAt: r.CreatedAt,
		}
	}
	return tgauth.FeedbackTicketDetail{
		ID:        t.ID,
		UserID:    t.UserID,
		Subject:   t.Subject,
		Message:   t.Message,
		Status:    string(t.Status),
		Category:  string(t.Category),
		Priority:  string(t.Priority),
		CreatedAt: t.CreatedAt,
		Replies:   replies,
	}, true
}

// AddUserReply implements tgauth.FeedbackSubmitter — adds a reply from the user (not admin).
func (s *FeedbackStore) AddUserReply(ticketID, userID, userName, message string) error {
	// Verify ownership
	t, ok := s.Get(ticketID)
	if !ok {
		return &feedbackError{"ticket not found"}
	}
	if t.UserID != userID {
		return &feedbackError{"access denied"}
	}
	reply := FeedbackReply{
		AdminID:   0, // user, not admin
		AdminName: userName,
		Message:   message,
	}
	return s.AddReply(ticketID, reply)
}

// --- rate limiter ---

const feedbackRateLimit = 5
const feedbackRateWindow = time.Hour

func (s *FeedbackStore) rateCheck(userID string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-feedbackRateWindow)
	// clean old
	times := s.rates[userID]
	cleaned := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			cleaned = append(cleaned, t)
		}
	}
	s.rates[userID] = cleaned

	if len(cleaned) >= feedbackRateLimit {
		return false
	}
	s.rates[userID] = append(s.rates[userID], now)
	return true
}

var errTicketNotFound = stdjson.Unmarshal([]byte(`"ticket not found"`), new(string)) // sentinel

func init() {
	errTicketNotFound = &feedbackError{"ticket not found"}
}

type feedbackError struct{ msg string }

func (e *feedbackError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// Public API Handlers
// ---------------------------------------------------------------------------

// POST /api/feedback — create a new ticket.
func FeedbackCreateHandler(store *FeedbackStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := userdata.BookmarkUserID(r)
		if userID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "user_id required"})
			return
		}

		body, _ := io.ReadAll(io.LimitReader(r.Body, 32768))
		var req struct {
			Category FeedbackCategory `json:"category"`
			Priority FeedbackPriority `json:"priority"`
			Subject  string           `json:"subject"`
			Message  string           `json:"message"`
			UserName string           `json:"user_name"`
		}
		if stdjson.Unmarshal(body, &req) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}

		// validation
		if !validFBCategories[req.Category] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid category"})
			return
		}
		req.Subject = strings.TrimSpace(req.Subject)
		req.Message = strings.TrimSpace(req.Message)
		if req.Subject == "" || len(req.Subject) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "subject required (max 200 chars)"})
			return
		}
		if req.Message == "" || len(req.Message) > 5000 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message required (max 5000 chars)"})
			return
		}
		if req.Priority != "" && !ValidFBPriorities[req.Priority] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid priority"})
			return
		}

		// rate limit
		if !store.rateCheck(userID) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit: max 5 tickets per hour"})
			return
		}

		ticket := FeedbackTicket{
			UserID:   userID,
			UserName: strings.TrimSpace(req.UserName),
			Category: req.Category,
			Priority: req.Priority,
			Subject:  req.Subject,
			Message:  req.Message,
		}

		created, err := store.Create(ticket)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		log.Info().Str("id", created.ID).Str("user", userID).Str("cat", string(req.Category)).Msg("feedback: ticket created")
		writeJSON(w, http.StatusCreated, map[string]any{
			"success": true,
			"ticket": map[string]any{
				"id":         created.ID,
				"status":     created.Status,
				"created_at": created.CreatedAt,
			},
		})
	}
}

// GET /api/feedback/my — list current user's tickets.
func FeedbackMyHandler(store *FeedbackStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := userdata.BookmarkUserID(r)
		if userID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "user_id required"})
			return
		}
		tickets := store.ListByUser(userID)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"tickets": tickets,
		})
	}
}

// GET /api/feedback/{id} — get a single ticket (user can only see their own).
func FeedbackGetHandler(store *FeedbackStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := userdata.BookmarkUserID(r)
		if userID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "user_id required"})
			return
		}
		id := chi.URLParam(r, "id")
		ticket, ok := store.Get(id)
		if !ok || ticket.UserID != userID {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "ticket not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"ticket":  ticket,
		})
	}
}
