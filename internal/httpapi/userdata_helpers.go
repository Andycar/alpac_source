package httpapi

// Shared helpers whose canonical definitions moved to internal/userdata with
// the persistence cluster (bookmark_api/storage_api). Copied back for the
// ~10 in-package callers (cmd_api/corseu_api/plugins/app_assets_api/…).

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func parseBoolLike(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return v == "1" || v == "yes" || v == "on"
}

func chiURLParam(r *http.Request, key string) string {
	val := strings.TrimSpace(chi.URLParam(r, key))
	if val != "" {
		return val
	}
	return strings.TrimSpace(r.URL.Query().Get(key))
}
