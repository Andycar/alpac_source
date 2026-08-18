package wasmmodules

import (
	"sync"
	"time"
)

// LogEntry is one record streamed from a plugin (host_log or stdout/stderr).
type LogEntry struct {
	Module  string    `json:"module"`
	Level   string    `json:"level"`
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// logSink is a small ring buffer the admin panel reads from. Same approach as
// jsmodules — a real logger is overkill for a plugin's println output.
type logSink struct {
	mu      sync.Mutex
	entries []LogEntry
	cap     int

	// listeners are notified on every Append. Used to drive SSE in the admin
	// panel without polling.
	listeners []chan LogEntry
}

func newLogSink(capacity int) *logSink {
	if capacity <= 0 {
		capacity = 500
	}
	return &logSink{cap: capacity, entries: make([]LogEntry, 0, capacity)}
}

func (s *logSink) Append(e LogEntry) {
	s.mu.Lock()
	if len(s.entries) >= s.cap {
		s.entries = s.entries[1:]
	}
	s.entries = append(s.entries, e)
	listeners := append([]chan LogEntry(nil), s.listeners...)
	s.mu.Unlock()
	for _, ch := range listeners {
		select {
		case ch <- e:
		default:
			// Drop if listener is slow — don't block the runtime.
		}
	}
}

// Tail returns the last n entries (newest at end). If module is non-empty,
// entries from other modules are filtered out.
func (s *logSink) Tail(module string, n int) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogEntry, 0, n)
	start := 0
	if len(s.entries) > n && module == "" {
		start = len(s.entries) - n
	}
	for i := start; i < len(s.entries); i++ {
		if module == "" || s.entries[i].Module == module {
			out = append(out, s.entries[i])
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// Subscribe returns a channel that receives new entries. Caller must drain it
// or the sink will drop messages.
func (s *logSink) Subscribe() chan LogEntry {
	ch := make(chan LogEntry, 32)
	s.mu.Lock()
	s.listeners = append(s.listeners, ch)
	s.mu.Unlock()
	return ch
}

func (s *logSink) Unsubscribe(ch chan LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.listeners {
		if c == ch {
			s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
			close(c)
			return
		}
	}
}
