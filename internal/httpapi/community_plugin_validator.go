package httpapi

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"lampac-go/internal/tgauth"

	"github.com/dop251/goja"
)

// ---------------------------------------------------------------------------
// Plugin sandbox validator — runs before install/update from the catalog.
//
// The goal is NOT to be a security boundary (a determined plugin can still
// do anything once loaded by the Lampa client). It's defence-in-depth:
//   - syntax errors caught before publish to admins
//   - obvious red flags surfaced (eval, raw fetch to non-allowlisted hosts,
//     attempts to read localStorage cookies, etc.)
//   - size and complexity guards (one-off DDoS scripts disguised as plugins)
//
// Severity levels:
//   "fail"  — refuses install (syntax error, exceeds hard limits)
//   "warn"  — flags as suspicious; admin can still install with confirm flag
//   "info"  — informational, no action required
// ---------------------------------------------------------------------------

// PluginValidationFinding is one issue surfaced by the validator.
type PluginValidationFinding struct {
	Severity string `json:"severity"` // "fail" | "warn" | "info"
	Code     string `json:"code"`     // short machine-readable id
	Message  string `json:"message"`  // human description
	Hint     string `json:"hint,omitempty"`
}

// PluginValidationResult bundles the verdict + findings.
type PluginValidationResult struct {
	OK          bool                      `json:"ok"` // false if any "fail" finding
	Findings    []PluginValidationFinding `json:"findings"`
	SizeBytes   int                       `json:"size_bytes"`
	SHA256      string                    `json:"sha256"`
	HasLampaAPI bool                      `json:"has_lampa_api"`
}

const (
	pluginMaxSizeBytes = 2 * 1024 * 1024 // 2 MB hard limit
	pluginWarnSize     = 512 * 1024      // > 512 KB raises a warning
)

// suspiciousPatterns is a set of regexps that flag potentially harmful code.
// Matched against the raw source after stripping JS comments. Each pattern is
// paired with a finding code and severity.
var suspiciousPatterns = []struct {
	rx       *regexp.Regexp
	severity string
	code     string
	message  string
	hint     string
}{
	{
		rx:       regexp.MustCompile(`\beval\s*\(`),
		severity: "warn",
		code:     "use-of-eval",
		message:  "Использование eval() — сложно аудитить и часто признак вредоносного кода",
		hint:     "Предпочитайте JSON.parse / структурированные вызовы",
	},
	{
		rx:       regexp.MustCompile(`new\s+Function\s*\(`),
		severity: "warn",
		code:     "function-constructor",
		message:  "new Function(...) — динамическая компиляция кода в строке",
		hint:     "Аналог eval; обходит ваш код-ревью",
	},
	{
		rx:       regexp.MustCompile(`document\.cookie\b`),
		severity: "warn",
		code:     "reads-cookies",
		message:  "Плагин читает document.cookie — может утекать сессии",
		hint:     "Если только для CSRF-токена — оставьте комментарий в плагине",
	},
	{
		rx:       regexp.MustCompile(`localStorage\.\w+\s*=`),
		severity: "info",
		code:     "writes-localstorage",
		message:  "Плагин пишет в localStorage — норма для настроек, но проверьте ключи",
	},
	{
		rx:       regexp.MustCompile(`(?i)\b(crypto|wallet|metamask|web3)\b`),
		severity: "warn",
		code:     "crypto-keywords",
		message:  "Подозрительные ключевые слова: crypto/wallet/metamask/web3",
		hint:     "Lampa-плагин не должен работать с криптокошельками",
	},
	{
		rx:       regexp.MustCompile(`(?i)mining|stratum|coinhive|hash\s*rate`),
		severity: "fail",
		code:     "mining-pattern",
		message:  "Признаки криптомайнинга в коде плагина",
	},
	{
		rx:       regexp.MustCompile(`(?i)(redirect|location\.href)\s*=\s*["']https?://[^"']+`),
		severity: "warn",
		code:     "external-redirect",
		message:  "Плагин может выполнять редирект на внешний URL",
	},
	{
		rx:       regexp.MustCompile(`<script[^>]*src=["']https?://`),
		severity: "warn",
		code:     "external-script-injection",
		message:  "Плагин внедряет внешний <script src=...> в DOM",
		hint:     "Это позволяет автору загружать произвольный код после установки",
	},
}

// jsBlockCommentRe matches /* ... */ block comments.
var jsBlockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

// jsLineCommentRe matches // comments at line start (after whitespace) only.
// We deliberately do NOT strip mid-line `//` because of URL false positives
// (e.g. `var u = "https://..."` would have everything after `//` stripped).
var jsLineCommentRe = regexp.MustCompile(`(?m)^\s*//[^\n]*`)

// stripJSComments removes block comments and line comments that are alone on
// their line. Mid-line `//` (which usually indicates URL paths inside string
// literals) is preserved — false positives there are tolerable since we only
// surface them as "warn", and the validator is defence-in-depth, not a sandbox.
func stripJSComments(src string) string {
	out := jsBlockCommentRe.ReplaceAllString(src, "")
	out = jsLineCommentRe.ReplaceAllString(out, "")
	return out
}

// ValidatePluginSource performs static checks on plugin JS without executing it.
func ValidatePluginSource(name string, content []byte) PluginValidationResult {
	res := PluginValidationResult{
		Findings:  []PluginValidationFinding{},
		SizeBytes: len(content),
		SHA256:    sha256Hex(content),
	}

	// 1. Size guard.
	if len(content) > pluginMaxSizeBytes {
		res.Findings = append(res.Findings, PluginValidationFinding{
			Severity: "fail",
			Code:     "size-too-large",
			Message:  fmt.Sprintf("Размер плагина %d байт превышает лимит %d", len(content), pluginMaxSizeBytes),
		})
		res.OK = false
		return res
	}
	if len(content) > pluginWarnSize {
		res.Findings = append(res.Findings, PluginValidationFinding{
			Severity: "warn",
			Code:     "size-large",
			Message:  fmt.Sprintf("Размер плагина %d байт — это много, проверьте, не вшит ли минифицированный фреймворк", len(content)),
		})
	}
	if len(content) == 0 {
		res.Findings = append(res.Findings, PluginValidationFinding{
			Severity: "fail",
			Code:     "empty",
			Message:  "Файл плагина пустой",
		})
		res.OK = false
		return res
	}

	// 2. Syntax check via goja.Compile (no execution).
	src := string(content)
	if _, err := goja.Compile(name, src, true); err != nil {
		res.Findings = append(res.Findings, PluginValidationFinding{
			Severity: "fail",
			Code:     "syntax-error",
			Message:  "Синтаксическая ошибка JavaScript: " + err.Error(),
		})
		res.OK = false
		return res
	}

	// 3. Lampa API surface — every legitimate community plugin should call
	//    Lampa.Plugins.* or Lampa.Listener.follow(...). If neither appears,
	//    something is off. This is "warn", not "fail" — could be a utility lib.
	stripped := stripJSComments(src)
	hasPlugins := strings.Contains(stripped, "Lampa.Plugins")
	hasListener := strings.Contains(stripped, "Lampa.Listener")
	hasComponent := strings.Contains(stripped, "Lampa.Component")
	res.HasLampaAPI = hasPlugins || hasListener || hasComponent
	if !res.HasLampaAPI {
		res.Findings = append(res.Findings, PluginValidationFinding{
			Severity: "warn",
			Code:     "no-lampa-api",
			Message:  "Не обнаружено вызовов Lampa.Plugins / Lampa.Listener / Lampa.Component",
			Hint:     "Без точки входа в Lampa плагин ничего не делает — возможно, он установится, но не появится в интерфейсе",
		})
	}

	// 4. Pattern-based suspicion checks.
	for _, p := range suspiciousPatterns {
		if p.rx.MatchString(stripped) {
			res.Findings = append(res.Findings, PluginValidationFinding{
				Severity: p.severity,
				Code:     p.code,
				Message:  p.message,
				Hint:     p.hint,
			})
		}
	}

	// 5. Verdict: OK iff no "fail" findings.
	res.OK = true
	for _, f := range res.Findings {
		if f.Severity == "fail" {
			res.OK = false
			break
		}
	}
	return res
}

// tgAdminPluginValidateHandler exposes the validator over HTTP so admins
// can sanity-check a plugin URL before installing. Accepts ?url=<sourceURL>.
// Returns the validation result plus the SHA-256 of the downloaded body.
func tgAdminPluginValidateHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		raw := strings.TrimSpace(r.URL.Query().Get("url"))
		if raw == "" {
			http.Error(w, "missing 'url' parameter", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
			http.Error(w, "url must be http:// or https://", http.StatusBadRequest)
			return
		}
		content, err := downloadPluginJS(raw)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":    false,
				"error": "Не удалось скачать плагин: " + err.Error(),
			})
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			name = "preview.js"
		}
		res := ValidatePluginSource(name, content)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         res.OK,
			"result":     res,
			"target_url": raw,
		})
	}
}
