package httpapi

import (
	"net/http"
	"strings"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
	"lampac-go/internal/kit"
)

// kitMiddleware loads the authenticated TG user's kit config into the request context
// for subsequent balancer handlers. Only triggers on /lite/ paths to minimize disk I/O.
func kitMiddleware(store *kit.Store, cfg config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Kit.Enable {
				next.ServeHTTP(w, r)
				return
			}

			// Only load kit for balancer and sisi request paths.
			path := r.URL.Path
			if !strings.HasPrefix(path, "/lite/") && !strings.HasPrefix(path, "/plab") {
				next.ServeHTTP(w, r)
				return
			}

			user, ok := auth.UserFromContext(r.Context())
			if !ok || user == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Extract kit store key from user ID.
			// TG users: "tg:123456" → "123456"
			// Browser Kit users: "bkit:abc123" → "bkit:abc123" (full ID as key)
			var tgID string
			switch {
			case strings.HasPrefix(user.ID, "tg:"):
				tgID = strings.TrimPrefix(user.ID, "tg:")
			case strings.HasPrefix(user.ID, "bkit:"):
				tgID = user.ID // use full "bkit:xxx" as kit store key
			default:
				next.ServeHTTP(w, r)
				return
			}
			kitCfg, err := store.Load(tgID)
			if err != nil || kitCfg == nil {
				next.ServeHTTP(w, r)
				return
			}

			ctx := kit.WithConfig(r.Context(), kitCfg)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
