package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/torrbalancer"
)

// ——— прямая отдача торрентов из /proxy ———

// torrentBackends — что нужно от пула для прямой ссылки (интерфейс — ради тестов).
type torrentBackends interface {
	Backends() []*torrbalancer.Backend
	CurrentSettings() torrbalancer.Settings
}

// torrentDirectRedirect — хук proxyapi.DirectRedirect: поток pidtor, чей
// апстрим — бэкенд TorrServer с direct_url, уходит зрителю подписанной ссылкой
// (302), как у /ts/stream. ?stat и ?m3u остаются через main — их читает наш
// код, а не плеер. Включается [proxy_link] torrent_direct.
func torrentDirectRedirect(pool torrentBackends) func(plugin, target string, r *http.Request) (string, bool) {
	return func(plugin, target string, r *http.Request) (string, bool) {
		if pool == nil || !strings.EqualFold(strings.TrimSpace(plugin), "pidtor") {
			return "", false
		}
		if !liveConfig(config.Config{}).ProxyLink.TorrentDirect {
			return "", false
		}
		u, err := url.Parse(target)
		if err != nil || u.Host == "" || !tsPathIsStream(u.Path) {
			return "", false
		}
		q := u.Query()
		if q.Has("stat") || q.Has("m3u") || !q.Has("link") {
			return "", false
		}
		origin := strings.ToLower(u.Scheme + "://" + u.Host)
		for _, b := range pool.Backends() {
			host, _ := b.Target()
			if strings.ToLower(strings.TrimRight(strings.TrimSpace(host), "/")) != origin {
				continue
			}
			base, secret := b.DirectTarget()
			if base == "" || secret == "" {
				return "", false
			}
			ttl := pool.CurrentSettings().DirectTTLSec
			if ttl < 60 {
				ttl = 6 * 3600
			}
			expires := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
			return torrbalancer.DirectLink(base, secret, u.Path, q, expires), true
		}
		return "", false
	}
}
