package jsmodules

import (
	"sync"
	"time"
)

// LogEntry is one line of per-module log output.
type LogEntry struct {
	Time   time.Time `json:"time"`
	Level  string    `json:"level"`
	Module string    `json:"module"`
	Msg    string    `json:"msg"`
}

// logSink is a bounded in-memory ring buffer that keeps recent log lines per
// module, so the admin UI can show live feedback without a heavier logging
// pipeline. Not exported: callers use Manager.Logs().
type logSink struct {
	mu      sync.RWMutex
	cap     int
	buffers map[string][]LogEntry
	subs    map[int]chan LogEntry
	nextID  int
}

func newLogSink(capPerModule int) *logSink {
	if capPerModule <= 0 {
		capPerModule = 500
	}
	return &logSink{cap: capPerModule, buffers: map[string][]LogEntry{}, subs: map[int]chan LogEntry{}}
}

func (s *logSink) Append(module, level, msg string) {
	entry := LogEntry{Time: time.Now(), Level: level, Module: module, Msg: msg}
	s.mu.Lock()
	buf := s.buffers[module]
	if len(buf) >= s.cap {
		buf = buf[len(buf)-s.cap+1:]
	}
	buf = append(buf, entry)
	s.buffers[module] = buf
	subs := make([]chan LogEntry, 0, len(s.subs))
	for _, c := range s.subs {
		subs = append(subs, c)
	}
	s.mu.Unlock()

	for _, c := range subs {
		select {
		case c <- entry:
		default:
		}
	}
}

// Tail returns up to n most recent entries for a module (all modules if empty).
func (s *logSink) Tail(module string, n int) []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if module != "" {
		buf := s.buffers[module]
		if n > 0 && len(buf) > n {
			buf = buf[len(buf)-n:]
		}
		out := make([]LogEntry, len(buf))
		copy(out, buf)
		return out
	}
	// Merged: approximate by concatenating and truncating.
	var merged []LogEntry
	for _, buf := range s.buffers {
		merged = append(merged, buf...)
	}
	if n > 0 && len(merged) > n {
		merged = merged[len(merged)-n:]
	}
	return merged
}

// Subscribe returns a channel that receives new log entries until Unsubscribe.
func (s *logSink) Subscribe(buf int) (int, <-chan LogEntry) {
	if buf <= 0 {
		buf = 16
	}
	ch := make(chan LogEntry, buf)
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	s.mu.Unlock()
	return id, ch
}

func (s *logSink) Unsubscribe(id int) {
	s.mu.Lock()
	if c, ok := s.subs[id]; ok {
		close(c)
		delete(s.subs, id)
	}
	s.mu.Unlock()
}

// Clear wipes the ring buffer for one module.
func (s *logSink) Clear(module string) {
	s.mu.Lock()
	if module == "" {
		s.buffers = map[string][]LogEntry{}
	} else {
		delete(s.buffers, module)
	}
	s.mu.Unlock()
}
