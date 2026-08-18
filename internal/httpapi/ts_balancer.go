package httpapi

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/torrbalancer"
)

// tsBalancerPoolRef is the process-wide TorrServer balancer pool, set once in
// server.go at startup. The pool object identity is stable across config
// hot-reloads (settings/backends are applied via Reconcile, never by replacing
// the pool), so a plain pointer is safe. nil → no balancer; the proxy uses its
// single legacy upstream.
var tsBalancerPoolRef *torrbalancer.Pool

// tsPoolBackendFor picks the HRW-sticky torrent-balancer backend for infohash,
// or nil when the pool is off/empty or TorrServer runs in-process. Mirrors
// transcodesvc.TSPoolBackendFor over httpapi's OWN pool handle (the two are the
// same object in production) so pidtor and the transcoder agree on the backend.
func tsPoolBackendFor(infohash string) *torrbalancer.Backend {
	if torrsIsInProcess() || tsBalancerPoolRef == nil || !tsBalancerPoolRef.HasEnabledBackends() {
		return nil
	}
	return tsBalancerPoolRef.PickForHash(strings.ToLower(strings.TrimSpace(infohash)), nil)
}

// tsZombieHandler reacts to a stream circuit-breaker trip (see
// Pool.RecordStreamStrike): alerts the TG admins, and — when the operator
// enabled auto_restart_zombie AND /echo still answers (the half-wedged
// signature: process alive, streams dead; prod 2026-07-16 the operator fixed
// it by hand-restarting TorrServer) — sends the backend /shutdown so systemd
// Restart=always brings it back clean. Runs async off the request path.
func tsZombieHandler(pool *torrbalancer.Pool) func(*torrbalancer.Backend, bool) {
	return func(b *torrbalancer.Backend, echoAlive bool) {
		host, _ := b.Target()
		name := b.Name
		if name == "" {
			name = host
		}

		state := "и /echo тоже молчит — бэкенд лежит целиком"
		if echoAlive {
			state = "но /echo отвечает — процесс завис наполовину (зомби)"
		}
		autoheal := echoAlive && pool.CurrentSettings().AutoRestartZombie
		action := "торренты переехали на здоровые бэкенды; перезапустите TorrServer вручную"
		if autoheal {
			action = "отправляю /shutdown — systemd перезапустит его автоматически"
		} else if echoAlive {
			action += " (или включите автолечение в TS Балансере)"
		}

		if bot := liveTGBot(); bot != nil {
			bot.NotifyAdminsNow(fmt.Sprintf(
				"⚠️ <b>TorrServer «%s»</b>\n<code>%s</code>\nСтримы не отдают данные, %s.\nКарантин на 2 мин, %s.",
				name, host, state, action))
		}

		if autoheal {
			err := pool.ShutdownBackend(b)
			if bot := liveTGBot(); bot != nil {
				if err != nil {
					bot.NotifyAdminsNow(fmt.Sprintf("❌ <b>TorrServer «%s»</b>: /shutdown не прошёл: %s", name, err.Error()))
				} else {
					bot.NotifyAdminsNow(fmt.Sprintf("✅ <b>TorrServer «%s»</b>: /shutdown отправлен — жду возвращения по health-пробе", name))
				}
			}
			// Эскалация: /shutdown перезапускает процесс, но НЕ лечит заклиненную
			// торрент-подсистему (прод 2026-08-13, Selectel: после рестарта /echo и
			// /settings живы, /torrents так и висит). Если через 45с torrents-API
			// всё ещё мёртв и у бэкенда настроен SSH — рестартуем машиной.
			go func() {
				time.Sleep(45 * time.Second)
				if pool.TorrentsAPIAlive(b) {
					return
				}
				if !b.HasSSH() {
					if bot := liveTGBot(); bot != nil {
						bot.NotifyAdminsNow(fmt.Sprintf("⚠️ <b>TorrServer «%s»</b>: и после /shutdown torrents-API мёртв. SSH не настроен — нужен ручной рестарт машины.", name))
					}
					return
				}
				out, sshErr := pool.SSHRestartBackend(b)
				time.Sleep(20 * time.Second)
				alive := pool.TorrentsAPIAlive(b)
				if bot := liveTGBot(); bot != nil {
					switch {
					case sshErr != nil:
						bot.NotifyAdminsNow(fmt.Sprintf("❌ <b>TorrServer «%s»</b>: SSH-рестарт не прошёл: %s", name, sshErr.Error()))
					case alive:
						bot.NotifyAdminsNow(fmt.Sprintf("✅ <b>TorrServer «%s»</b>: SSH-рестарт помог, torrents-API снова отвечает.", name))
					default:
						msg := fmt.Sprintf("⚠️ <b>TorrServer «%s»</b>: SSH-рестарт выполнен, но torrents-API так и молчит — бэкенд в карантине, разбирайтесь руками.", name)
						if out != "" {
							msg += "\n<code>" + out + "</code>"
						}
						bot.NotifyAdminsNow(msg)
					}
				}
			}()
		}
	}
}

// tsRequestBackendFilter builds the "is this backend allowed for the requesting
// user's group" predicate. It reuses resolveUserGroup (which already applies
// the premium overlay and falls back to the default group), keeping the
// torrbalancer package free of any tgauth dependency.
func tsRequestBackendFilter(r *http.Request) func(backendID string) bool {
	if g := resolveUserGroup(r); g != nil {
		return g.TorrServerAllowed
	}
	return func(string) bool { return true }
}

// resolveTSBackend chooses the backend for this request. infohash may be "" for
// hashless endpoints (web UI, echo, settings), which route to the pool primary.
//
// ok=false means the pool is active but no healthy+allowed backend exists — the
// caller must return 503 and never silently leak to a forbidden/legacy host.
// When the pool has no enabled backends at all, it falls back to the single
// legacy upstream so existing single-server deployments behave unchanged.
func (p *tsProxy) resolveTSBackend(r *http.Request, infohash string) (host, authHdr string, backend *torrbalancer.Backend, ok bool) {
	if p.pool != nil && p.pool.HasEnabledBackends() {
		b := p.pool.PickForHash(infohash, tsRequestBackendFilter(r))
		if b == nil {
			return "", "", nil, false
		}
		host, authHdr = b.Target()
		return host, authHdr, b, true
	}
	return p.upstream, p.authHdr, nil, true
}

// fanoutList queries every backend in `backends` for the same path and merges
// the responses. Used for endpoints whose data spans the pool: the torrents
// list (JSON array) and playlistall (m3u). Partial failures are tolerated — a
// backend that errors or returns non-200 simply contributes nothing.
func (p *tsProxy) fanoutList(w http.ResponseWriter, r *http.Request, path string, backends []*torrbalancer.Backend, isM3U bool) {
	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	_ = r.Body.Close()

	parts := make([][]byte, len(backends))
	var wg sync.WaitGroup
	for i, b := range backends {
		host, authHdr := b.Target()
		wg.Add(1)
		go func(i int, host, authHdr string) {
			defer wg.Done()
			target := host + path
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			var body io.Reader
			if len(bodyBytes) > 0 {
				body = bytes.NewReader(bodyBytes)
			}
			req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
			if err != nil {
				return
			}
			for k, vals := range r.Header {
				switch strings.ToLower(k) {
				case "authorization", "host", "connection", "content-length":
					continue
				}
				for _, v := range vals {
					req.Header.Add(k, v)
				}
			}
			req.Host = req.URL.Host
			if authHdr != "" {
				req.Header.Set("Authorization", authHdr)
			}
			resp, err := p.client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
			if resp.StatusCode == http.StatusOK {
				parts[i] = data
			}
		}(i, host, authHdr)
	}
	wg.Wait()

	if isM3U {
		w.Header().Set("Content-Type", "audio/x-mpegurl")
		_, _ = w.Write([]byte(mergeM3U(parts)))
	} else {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mergeJSONArrays(parts))
	}
}

// mergeM3U concatenates m3u playlists, keeping a single #EXTM3U header and
// rewriting backend /stream/ paths to the proxy's /ts/stream/ prefix (matching
// handleM3U).
func mergeM3U(parts [][]byte) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		text := strings.ReplaceAll(string(part), "/stream/", "/ts/stream/")
		for _, line := range strings.Split(text, "\n") {
			t := strings.TrimRight(line, "\r")
			trimmed := strings.TrimSpace(t)
			if trimmed == "" || strings.HasPrefix(trimmed, "#EXTM3U") {
				continue
			}
			sb.WriteString(t)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// mergeJSONArrays concatenates the JSON arrays returned by each backend into a
// single array. Non-array or empty parts are skipped. Always returns valid JSON.
func mergeJSONArrays(parts [][]byte) []byte {
	merged := make([]stdjson.RawMessage, 0, 16)
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		var arr []stdjson.RawMessage
		if err := stdjson.Unmarshal(part, &arr); err != nil {
			continue
		}
		merged = append(merged, arr...)
	}
	if len(merged) == 0 {
		return []byte("[]")
	}
	out, err := stdjson.Marshal(merged)
	if err != nil {
		return []byte("[]")
	}
	return out
}
