// Package logbuf provides a thread-safe ring buffer that captures zerolog
// JSON output, sanitizes sensitive tokens, and exposes entries for the admin
// panel log viewer.
package logbuf

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

// Entry is a single parsed & sanitized log line.
type Entry struct {
	Seq      uint64            `json:"seq"`
	Time     string            `json:"time"`
	Level    string            `json:"level"`
	Category string            `json:"category"`
	Message  string            `json:"message"`
	Extra    map[string]string `json:"extra,omitempty"`
	Raw      string            `json:"raw"`
}

// Filter controls which entries are returned by Query.
type Filter struct {
	Categories []string // empty = all
	Level      string   // "" = all, or "error", "warn", "info", "debug", "trace"
	Search     string   // substring match on message
	Limit      int      // 0 = default 200
	Offset     int
}

// Buffer is a fixed-capacity ring buffer of log entries.
type Buffer struct {
	mu      sync.RWMutex
	entries []Entry
	head    int // next write position
	count   int
	cap     int

	seq atomic.Uint64

	subMu sync.RWMutex
	subs  map[uint64]chan Entry
	subID uint64
}

// New creates a ring buffer with the given capacity.
func New(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = 10000
	}
	return &Buffer{
		entries: make([]Entry, capacity),
		cap:     capacity,
		subs:    make(map[uint64]chan Entry),
	}
}

// Write implements io.Writer. It receives raw zerolog JSON lines, parses them,
// sanitizes sensitive data, and stores them in the ring buffer. It always
// returns len(p), nil so zerolog never sees a write error.
func (b *Buffer) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if line == "" {
		return len(p), nil
	}

	sanitized := sanitize(line)

	var fields map[string]any
	_ = json.UnmarshalFromString(sanitized, &fields)

	e := Entry{
		Seq:      b.seq.Add(1),
		Time:     strField(fields, "time"),
		Level:    strField(fields, "level"),
		Message:  sanitize(strField(fields, "message")),
		Extra:    extractExtra(fields),
		Raw:      sanitized,
		Category: detectCategory(strField(fields, "message")),
	}

	b.mu.Lock()
	b.entries[b.head] = e
	b.head = (b.head + 1) % b.cap
	if b.count < b.cap {
		b.count++
	}
	b.mu.Unlock()

	// Fan-out to SSE subscribers (non-blocking).
	b.subMu.RLock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // slow consumer — drop
		}
	}
	b.subMu.RUnlock()

	return len(p), nil
}

// Query returns entries matching the filter, newest first.
func (b *Buffer) Query(f Filter) []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}

	catSet := make(map[string]bool, len(f.Categories))
	for _, c := range f.Categories {
		catSet[strings.ToLower(c)] = true
	}
	levelLower := strings.ToLower(f.Level)
	searchLower := strings.ToLower(f.Search)

	// Iterate from newest to oldest.
	var result []Entry
	skipped := 0
	for i := 0; i < b.count; i++ {
		idx := (b.head - 1 - i + b.cap) % b.cap
		e := b.entries[idx]

		if len(catSet) > 0 && !catSet[e.Category] {
			continue
		}
		if levelLower != "" && e.Level != levelLower {
			continue
		}
		if searchLower != "" && !strings.Contains(strings.ToLower(e.Message), searchLower) {
			continue
		}

		if skipped < f.Offset {
			skipped++
			continue
		}

		result = append(result, e)
		if len(result) >= limit {
			break
		}
	}
	return result
}

// Subscribe returns a channel that receives new log entries in real-time
// and an ID used to unsubscribe.
func (b *Buffer) Subscribe() (<-chan Entry, uint64) {
	ch := make(chan Entry, 256)
	b.subMu.Lock()
	b.subID++
	id := b.subID
	b.subs[id] = ch
	b.subMu.Unlock()
	return ch, id
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Buffer) Unsubscribe(id uint64) {
	b.subMu.Lock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
	b.subMu.Unlock()
}

// Count returns the current number of entries stored.
func (b *Buffer) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.count
}

// Categories returns the list of known category names.
func (b *Buffer) Categories() []string {
	cats := make([]string, 0, len(categoryPrefixes)+2)
	seen := map[string]bool{}
	for _, cp := range categoryPrefixes {
		if !seen[cp.category] {
			cats = append(cats, cp.category)
			seen[cp.category] = true
		}
	}
	if !seen["http"] {
		cats = append(cats, "http")
	}
	if !seen["system"] {
		cats = append(cats, "system")
	}
	return cats
}

// --- Category detection ---

type catPrefix struct {
	prefix   string
	category string
}

var categoryPrefixes = []catPrefix{
	// Core infrastructure
	{"admin:", "admin"},
	{"tgauth", "auth"},
	{"webauthn:", "auth"},
	{"password", "auth"},
	{"admin login:", "auth"},
	{"transcod", "transcoding"},
	{"nws:", "nws"},
	{"dlna:", "dlna"},
	{"kit:", "kit"},
	{"config:", "system"},
	{"browser", "browser"},
	{"sidecar:", "sidecar"},
	{"tmdb:", "tmdb"},
	{"alice:", "alice"},
	// Balancers
	{"youtube:", "youtube"},
	{"rezka", "rezka"},
	{"rhsprem", "rhsprem"},
	{"collaps", "collaps"},
	{"kinotochka", "kinotochka"},
	{"rutubemovie", "rutubemovie"},
	{"vkmovie", "vkmovie"},
	{"plvideo", "plvideo"},
	{"cdnvideohub", "cdnvideohub"},
	{"kubikvkube", "kubikvkube"},
	{"redheadsound", "redheadsound"},
	{"iremux", "iremux"},
	{"remux", "remux"},
	{"zetflix", "zetflix"},
	{"lift", "lift"},
	{"uakino", "uakino"},
	{"kinovod", "kinovod"},
	{"sakhtv", "sakhtv"},
	{"zetflixdb", "zetflixdb"},
	{"videodb", "videodb"},
	{"cdnmovies", "cdnmovies"},
	{"vdbmovies", "vdbmovies"},
	{"fancdn", "fancdn"},
	{"kinobase", "kinobase"},
	{"videocdn", "videocdn"},
	{"lumex", "lumex"},
	{"vokino", "vokino"},
	{"iframevideo", "iframevideo"},
	{"hdvb", "hdvb"},
	{"vibix", "vibix"},
	{"videoseed", "videoseed"},
	{"kinopub", "kinopub"},
	{"alloha", "alloha"},
	{"getstv", "getstv"},
	{"hydraflix", "hydraflix"},
	{"vidsrc", "vidsrc"},
	{"vidlink", "vidlink"},
	{"videasy", "videasy"},
	{"autoembed", "autoembed"},
	{"rgshows", "rgshows"},
	{"kodik", "kodik"},
	{"mirage", "mirage"},
	{"aladdin", "aladdin"},
	{"kinogo", "kinogo"},
	{"iptvonline", "iptvonline"},
	{"filmix", "filmix"},
	{"moonanime", "moonanime"},
	{"anilibria", "anilibria"},
	{"aniliberty", "aniliberty"},
	{"animebesst", "animebesst"},
	{"animedia", "animedia"},
	{"animevost", "animevost"},
	{"animego", "animego"},
	{"animelib", "animelib"},
	{"kinoukr", "kinoukr"},
	{"ashdi", "ashdi"},
	{"eneyida", "eneyida"},
	{"bamboo", "bamboo"},
	{"unimay", "unimay"},
	{"starlight", "starlight"},
	{"klonfun", "klonfun"},
	{"uaflix", "uaflix"},
	{"animeon", "animeon"},
	{"mikai", "mikai"},
	{"veoveo", "veoveo"},
	{"filmixpartner", "filmixpartner"},
	{"pidtor", "pidtor"},
}

func detectCategory(msg string) string {
	lower := strings.ToLower(msg)

	for _, cp := range categoryPrefixes {
		if strings.Contains(lower, cp.prefix) {
			return cp.category
		}
	}

	// HTTP access log detection.
	if strings.Contains(msg, "HTTP") && (strings.Contains(msg, "GET") || strings.Contains(msg, "POST") || strings.Contains(msg, "PUT") || strings.Contains(msg, "DELETE")) {
		return "http"
	}

	return "system"
}

// --- Token sanitization ---

var sensitivePatterns []*regexp.Regexp

func init() {
	sensitivePatterns = []*regexp.Regexp{
		// key=value or key: value patterns with 8+ char values
		regexp.MustCompile(`(?i)(token|key|secret|password|apikey|api_key|passw)([":\s=]+["']?)([A-Za-z0-9_\-./+=]{8,})`),
		// Bearer tokens
		regexp.MustCompile(`(?i)(Bearer\s+)([A-Za-z0-9_\-./+=]{8,})`),
		// account_email= in URLs
		regexp.MustCompile(`(?i)(account_email=)([^&\s"]+)`),
		// uid= in URLs (user identifiers)
		regexp.MustCompile(`(?i)([\?&]uid=)([^&\s"]{8,})`),
	}
}

func sanitize(line string) string {
	for _, re := range sensitivePatterns {
		line = re.ReplaceAllStringFunc(line, func(match string) string {
			loc := re.FindStringSubmatchIndex(match)
			if loc == nil {
				return match
			}
			// Replace the last capture group with ***
			groups := re.FindStringSubmatch(match)
			if len(groups) >= 3 {
				lastGroup := groups[len(groups)-1]
				if len(lastGroup) >= 8 {
					return strings.Replace(match, lastGroup, "***", 1)
				}
			}
			return match
		})
	}
	return line
}

// --- Helpers ---

func strField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

// skipFields are standard zerolog fields already captured as Entry fields.
var skipFields = map[string]bool{
	"level": true, "time": true, "message": true, "service": true,
}

// extractExtra pulls all non-standard fields from the parsed JSON log line
// into a string map for display in the admin panel.
func extractExtra(fields map[string]any) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	extra := make(map[string]string, len(fields))
	for k, v := range fields {
		if skipFields[k] {
			continue
		}
		switch val := v.(type) {
		case string:
			if val != "" {
				extra[k] = val
			}
		case float64:
			if val == float64(int64(val)) {
				extra[k] = fmt.Sprintf("%d", int64(val))
			} else {
				extra[k] = fmt.Sprintf("%g", val)
			}
		case bool:
			extra[k] = fmt.Sprintf("%t", val)
		case []any:
			if b, err := json.MarshalToString(val); err == nil {
				extra[k] = b
			}
		default:
			if val != nil {
				extra[k] = fmt.Sprintf("%v", val)
			}
		}
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

// StartCleanup runs a background goroutine that periodically trims old
// subscribers whose channels have been abandoned. Call this once on startup.
func (b *Buffer) StartCleanup() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			b.subMu.Lock()
			for id, ch := range b.subs {
				// If the channel is full for a long time, the subscriber is likely gone.
				if len(ch) == cap(ch) {
					delete(b.subs, id)
					close(ch)
				}
			}
			b.subMu.Unlock()
		}
	}()
}
