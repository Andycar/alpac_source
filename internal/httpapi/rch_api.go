package httpapi

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type rchClientInfo struct {
	Version    int    `json:"version"`
	APKVersion int    `json:"apkVersion"`
	RchType    string `json:"rchtype"`
	NwsConnID  string `json:"-"` // WebSocket connectionId (not serialized)
	IP         string `json:"-"` // device IP, for the rch dispatch IP-match check
}

type rchPending struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
}

var (
	rchClientsMu sync.RWMutex
	rchClients   = map[string]rchClientInfo{}

	rchPendingMu sync.RWMutex
	rchPendings  = map[string]*rchPending{}
)

func rchCheckConnectedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != "" {
			rchClientsMu.RLock()
			info, ok := rchClients[id]
			rchClientsMu.RUnlock()
			if ok {
				writeJSON(w, http.StatusOK, map[string]any{
					"version":    info.Version,
					"apkVersion": info.APKVersion,
					"rchtype":    info.RchType,
				})
				return
			}
		}

		host := streamHostFromRequest(r)
		nwsProto := "ws"
		hostNoScheme := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		if strings.HasPrefix(strings.ToLower(host), "https://") {
			nwsProto = "wss"
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"rch": true,
			"ws":  host + "/ws",
			"nws": nwsProto + "://" + hostNoScheme + "/nws",
		})
	}
}

func rchResultHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		entry := rchGetPending(id)
		if entry == nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		body, err := readBodyLimited(r, 8<<20)
		if err != nil {
			rchCompletePending(entry)
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		entry.mu.Lock()
		entry.buf.Reset()
		_, _ = entry.buf.Write(body)
		entry.mu.Unlock()

		rchCompletePending(entry)
		w.WriteHeader(http.StatusOK)
	}
}

func rchGzipResultHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		entry := rchGetPending(id)
		if entry == nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			rchCompletePending(entry)
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		defer gz.Close()

		body, err := readReaderLimited(gz, 8<<20)
		if err != nil {
			rchCompletePending(entry)
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		entry.mu.Lock()
		entry.buf.Reset()
		_, _ = entry.buf.Write(body)
		entry.mu.Unlock()

		rchCompletePending(entry)
		w.WriteHeader(http.StatusOK)
	}
}

func rchRegisterPending(id string) *rchPending {
	entry := &rchPending{done: make(chan struct{})}
	rchPendingMu.Lock()
	rchPendings[id] = entry
	rchPendingMu.Unlock()
	return entry
}

func rchGetPending(id string) *rchPending {
	rchPendingMu.RLock()
	entry := rchPendings[id]
	rchPendingMu.RUnlock()
	return entry
}

func rchCompletePending(entry *rchPending) {
	select {
	case <-entry.done:
	default:
		close(entry.done)
	}
}

func rchDeletePending(id string) {
	rchPendingMu.Lock()
	delete(rchPendings, id)
	rchPendingMu.Unlock()
}

func rchPendingBytes(entry *rchPending) []byte {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	out := make([]byte, entry.buf.Len())
	copy(out, entry.buf.Bytes())
	return out
}

func rchWaitDone(entry *rchPending, timeout time.Duration) bool {
	select {
	case <-entry.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// rchRegisterClient registers a client that connected via NWS WebSocket.
func rchRegisterClient(connID, ip, host, infoJSON string) {
	var info rchClientInfo
	if infoJSON != "" {
		_ = json.Unmarshal([]byte(infoJSON), &info)
	}
	info.NwsConnID = connID
	info.IP = ip

	rchClientsMu.Lock()
	rchClients[connID] = info
	rchClientsMu.Unlock()
}

// rchLookupClient returns the registered device info for a connectionId.
func rchLookupClient(connID string) (rchClientInfo, bool) {
	rchClientsMu.RLock()
	info, ok := rchClients[connID]
	rchClientsMu.RUnlock()
	return info, ok
}

// rchOnDisconnected removes a client on WebSocket disconnect.
func rchOnDisconnected(connID string) {
	rchClientsMu.Lock()
	delete(rchClients, connID)
	rchClientsMu.Unlock()
}

func readBodyLimited(r *http.Request, limit int64) ([]byte, error) {
	return readReaderLimited(r.Body, limit)
}

func readReaderLimited(rd io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	lr := io.LimitReader(rd, limit+1)
	body, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("body too large")
	}
	return body, nil
}
