package jsmodules

import (
	stdjson "encoding/json"
	"fmt"
	stdhtml "html"
	"net/http"
	"strings"
)

// Handler builds an http.Handler that dispatches /lite/{moduleID}/... traffic
// to the module's JS handler. Returned handler also honors ?checksearch=true.
func (m *Manager) Handler(id string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mod, ok := m.Get(id)
		if !ok {
			http.Error(w, fmt.Sprintf(`{"error":"module %q not installed"}`, id), http.StatusNotFound)
			return
		}
		if !mod.Enabled {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"rch":false,"error":"disabled"}`))
			return
		}

		checksearch := strings.EqualFold(r.URL.Query().Get("checksearch"), "true") ||
			r.URL.Query().Get("checksearch") == "1"
		lifeMode := strings.EqualFold(r.URL.Query().Get("life"), "true") ||
			r.URL.Query().Get("life") == "1"

		// Flatten query string.
		qs := map[string]string{}
		for k, vs := range r.URL.Query() {
			if len(vs) > 0 {
				qs[k] = vs[0]
			}
		}
		// Flatten headers.
		hdr := map[string]string{}
		for k, vs := range r.Header {
			if len(vs) > 0 {
				hdr[strings.ToLower(k)] = vs[0]
			}
		}

		inv := Invocation{
			Query:       qs,
			Headers:     hdr,
			Host:        hostFromRequest(r),
			RequestIP:   clientIP(r),
			Path:        strings.TrimPrefix(r.URL.Path, "/lite/"),
			LifeMode:    lifeMode,
			Checksearch: checksearch,
			UserAgent:   r.Header.Get("User-Agent"),
		}

		resp, err := m.Invoke(r.Context(), id, inv)
		if err != nil {
			if m.Logger != nil {
				m.Logger.Warn().Str("module", id).Err(err).Msg("jsmodules: invoke error")
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		// Lampa expects HTML (with data-json attributes) unless ?rjson=true is
		// set — the JS-module always returns JSON, so we auto-convert for lists
		// (type+data) and keep JSON for play responses (top-level method+url).
		body := resp.Body
		ct := resp.ContentType
		if ct == "" {
			ct = "application/json; charset=utf-8"
		}
		rjson := strings.EqualFold(r.URL.Query().Get("rjson"), "true") || r.URL.Query().Get("rjson") == "1"
		if !checksearch && !rjson {
			if html, converted := convertJSONToHTML(body); converted {
				body = []byte(html)
				ct = "text/html; charset=utf-8"
			}
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

// convertJSONToHTML renders a JS-module JSON response into the HTML form
// Lampa expects when ?rjson=true is not passed. Returns (html, true) when
// conversion happened; (original, false) when input is a play-response (keep
// as JSON) or not valid JSON.
func convertJSONToHTML(body []byte) (string, bool) {
	var resp map[string]any
	if err := stdjson.Unmarshal(body, &resp); err != nil {
		return "", false
	}
	// Play response (top-level method+url) → keep JSON.
	if _, hasMethod := resp["method"]; hasMethod {
		if _, hasURL := resp["url"]; hasURL {
			if _, hasData := resp["data"]; !hasData {
				return "", false
			}
		}
	}

	respType, _ := resp["type"].(string)
	dataArr, _ := resp["data"].([]any)
	voiceArr, _ := resp["voice"].([]any)

	var sb strings.Builder

	// Voice buttons — go first, above the items grid.
	// Lampa's parseJsonDate reads JSON.parse(item.attr('data-json')) and
	// throws on plain <a href>. We must emit <div class="videos__button"
	// data-json='{"method":"link","url":"..."}'>name</div> for the filter
	// (and the auto-voice switch in online.js) to actually populate.
	if len(voiceArr) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, v := range voiceArr {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			name, _ := vm["name"].(string)
			url, _ := vm["url"].(string)
			cls := "videos__button selector"
			if active, _ := vm["active"].(bool); active {
				cls += " active"
			}
			payload, _ := stdjson.Marshal(map[string]any{
				"method": "link",
				"url":    url,
			})
			sb.WriteString(fmt.Sprintf(`<div class="%s" data-json='%s'>%s</div> `,
				cls, htmlAttrEscape(string(payload)), stdhtml.EscapeString(name)))
		}
		sb.WriteString(`</div>`)
	}

	if len(dataArr) == 0 {
		// Empty list — wrap in an empty videos__line so Lampa shows "nothing".
		sb.WriteString(`<div class="videos__line"></div>`)
		return sb.String(), true
	}

	sb.WriteString(`<div class="videos__line">`)
	for i, item := range dataArr {
		im, ok := item.(map[string]any)
		if !ok {
			continue
		}
		focused := ""
		if i == 0 {
			focused = " focused"
		}

		rawJSON, _ := stdjson.Marshal(im)
		dataAttr := htmlAttrEscape(string(rawJSON))

		switch respType {
		case "similar":
			// Picker — each item is a button
			title, _ := im["title"].(string)
			if title == "" {
				title, _ = im["name"].(string)
			}
			url, _ := im["url"].(string)
			sb.WriteString(fmt.Sprintf(`<a class="videos__button selector%s" href="%s">%s</a> `,
				focused, stdhtml.EscapeString(url), stdhtml.EscapeString(title)))

		case "season":
			name, _ := im["name"].(string)
			sb.WriteString(fmt.Sprintf(`<div class="videos__item videos__season selector%s" data-json='%s'>`,
				focused, dataAttr))
			sb.WriteString(`<div class="videos__season-layers"></div>`)
			sb.WriteString(`<div class="videos__item-imgbox videos__season-imgbox">`)
			sb.WriteString(fmt.Sprintf(`<div class="videos__item-title videos__item-season-episode">%s</div>`,
				stdhtml.EscapeString(name)))
			sb.WriteString(`</div></div>`)

		default:
			// movie / episode — card with data-json. We also need the bare
			// s="N" e="N" attributes because Lampa.parseJsonDate reads them
			// via item.attr('s') / item.attr('e') to set element.season /
			// element.episode (used to match TMDB still_path).
			name, _ := im["name"].(string)
			if name == "" {
				name, _ = im["title"].(string)
			}
			var seAttr strings.Builder
			if se, ok := jsonInt(im["s"]); ok && se > 0 {
				fmt.Fprintf(&seAttr, ` s="%d"`, se)
			}
			if ep, ok := jsonInt(im["e"]); ok && ep > 0 {
				fmt.Fprintf(&seAttr, ` e="%d"`, ep)
			}
			sb.WriteString(fmt.Sprintf(`<div class="videos__item videos__movie selector%s" media=""%s data-json='%s'>`,
				focused, seAttr.String(), dataAttr))
			sb.WriteString(`<div class="videos__item-imgbox videos__movie-imgbox"></div>`)
			sb.WriteString(fmt.Sprintf(`<div class="videos__item-title">%s</div>`,
				stdhtml.EscapeString(name)))
			sb.WriteString(`</div>`)
		}
	}
	sb.WriteString(`</div>`)
	return sb.String(), true
}

// jsonInt extracts an integer from a JS-module JSON value (which decodes
// numbers as float64). Returns (0,false) if the value is missing or not numeric.
func jsonInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	}
	return 0, false
}

// htmlAttrEscape escapes characters that break HTML single-quoted attributes.
// Go's html.EscapeString does `&` `<` `>` `"` `'` but escapes `"` as `&#34;`
// which is what Lampa-style data-json attributes already use.
func htmlAttrEscape(s string) string {
	s = strings.ReplaceAll(s, `&`, `&amp;`)
	s = strings.ReplaceAll(s, `'`, `&#39;`)
	s = strings.ReplaceAll(s, `"`, `&#34;`)
	s = strings.ReplaceAll(s, `<`, `&lt;`)
	s = strings.ReplaceAll(s, `>`, `&gt;`)
	return s
}

func hostFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if xh := r.Header.Get("X-Forwarded-Host"); xh != "" {
		host = xh
	}
	return scheme + "://" + host
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.Index(xf, ","); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	if h := r.Header.Get("X-Real-IP"); h != "" {
		return strings.TrimSpace(h)
	}
	if h, _, ok := strings.Cut(r.RemoteAddr, ":"); ok {
		return h
	}
	return r.RemoteAddr
}
