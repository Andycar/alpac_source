package httpapi

import (
	"net/http"
	"strings"

	"lampac-go/internal/litesrc"
	"lampac-go/internal/proxyapi"
	"lampac-go/internal/uproxy"
)

func proxyCompatMiddleware(proxyHandler *uproxy.Handler, proxyImgHandler http.Handler, proxyAPI *proxyapi.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.ToLower(r.URL.Path)

			switch {
			case strings.HasPrefix(path, "/proxyimg:"):
				if proxyImgHandler != nil {
					proxyImgHandler.ServeHTTP(w, r)
					return
				}
				proxyHandler.ServeProxy(w, r, "/proxyimg:")
				return
			case strings.HasPrefix(path, "/proxyimg/"):
				if proxyImgHandler != nil {
					proxyImgHandler.ServeHTTP(w, r)
					return
				}
				proxyHandler.ServeProxy(w, r, "/proxyimg/")
				return
			case strings.HasPrefix(path, "/proxy/"):
				if proxyAPI != nil {
					proxyAPI.HandleProxy(w, r)
					return
				}
				if proxyHandler != nil {
					proxyHandler.ServeProxy(w, r, "/proxy/")
					return
				}
				next.ServeHTTP(w, r)
				return
			case strings.HasPrefix(path, "/vibix_m3u8/"):
				litesrc.VibixM3U8Handler(w, r)
				return
			case strings.HasPrefix(path, "/vibix_embed/"):
				litesrc.VibixEmbedHandler(w, r)
				return
			case strings.HasPrefix(path, "/proxy-dash/"):
				if proxyAPI != nil {
					proxyAPI.HandleDash(w, r)
					return
				}
				if proxyHandler != nil {
					proxyHandler.ServeProxy(w, r, "/proxy-dash/")
					return
				}
				next.ServeHTTP(w, r)
				return
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}
