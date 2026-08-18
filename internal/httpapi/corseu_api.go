package httpapi

import (
	stdjson "encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// corsCheckHandler returns 200 OK for RCH type detection (/cors/check).
// Clients use this to determine if direct cross-origin requests are possible.
func corsCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

type corseuRequest struct {
	Browser        string            `json:"browser"`
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Data           string            `json:"data"`
	HTTPVersion    *int              `json:"httpversion"`
	Timeout        *int              `json:"timeout"`
	Encoding       string            `json:"encoding"`
	Headers        map[string]string `json:"headers"`
	DefaultHeaders *bool             `json:"defaultHeaders"`
	AutoRedirect   *bool             `json:"autoredirect"`
	Proxy          string            `json:"proxy"`
	ProxyName      string            `json:"proxy_name"`
	HeadersOnly    *bool             `json:"headersOnly"`
	AuthToken      string            `json:"auth_token"`
}

type corseuRule struct {
	Method  string
	URL     string
	Replace bool
	Headers map[string]string
}

type corseuSettings struct {
	Tokens map[string]struct{}
	Rules  []corseuRule
}

func corseuTokenGetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawURL := strings.TrimSpace(chiURLParam(r, "*"))
		if rawURL != "" && r.URL.RawQuery != "" {
			rawURL += "?" + r.URL.RawQuery
		}

		model := corseuRequest{
			URL:       rawURL,
			AuthToken: strings.TrimSpace(chiURLParam(r, "token")),
		}
		executeCorseu(w, r, model)
	}
}

func corseuGetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		model := corseuRequest{
			AuthToken: strings.TrimSpace(q.Get("auth_token")),
			Method:    strings.TrimSpace(q.Get("method")),
			URL:       strings.TrimSpace(q.Get("url")),
			Data:      q.Get("data"),
			Browser:   strings.TrimSpace(q.Get("browser")),
			Encoding:  strings.TrimSpace(q.Get("encoding")),
			Proxy:     strings.TrimSpace(q.Get("proxy")),
			ProxyName: strings.TrimSpace(q.Get("proxy_name")),
			Headers:   parseCorseuHeaders(q.Get("headers")),
		}

		if v := strings.TrimSpace(q.Get("httpversion")); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				model.HTTPVersion = &n
			}
		}
		if v := strings.TrimSpace(q.Get("timeout")); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				model.Timeout = &n
			}
		}
		if v := strings.TrimSpace(q.Get("defaultHeaders")); v != "" {
			b := parseBoolLike(v)
			model.DefaultHeaders = &b
		}
		if v := strings.TrimSpace(q.Get("autoredirect")); v != "" {
			b := parseBoolLike(v)
			model.AutoRedirect = &b
		}
		if v := strings.TrimSpace(q.Get("headersOnly")); v != "" {
			b := parseBoolLike(v)
			model.HeadersOnly = &b
		}

		executeCorseu(w, r, model)
	}
}

func corseuPostHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Invalid body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(string(body)) == "" {
			http.Error(w, "Empty body", http.StatusBadRequest)
			return
		}

		var model corseuRequest
		if err := stdjson.Unmarshal(body, &model); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		executeCorseu(w, r, model)
	}
}

func executeCorseu(w http.ResponseWriter, r *http.Request, model corseuRequest) {
	settings := loadCorseuSettings()
	if len(settings.Tokens) == 0 {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	token := strings.TrimSpace(model.AuthToken)
	if token == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if _, ok := settings.Tokens[token]; !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	urlValue := strings.TrimSpace(model.URL)
	if urlValue == "" {
		http.Error(w, "url is empty", http.StatusBadRequest)
		return
	}

	method := strings.ToUpper(strings.TrimSpace(model.Method))
	if method == "" {
		method = http.MethodGet
	}

	browser := strings.ToLower(strings.TrimSpace(model.Browser))
	if browser == "" {
		browser = "http"
	}

	// Browser-driven mode depends on legacy runtime; keep transparent fallback.
	if browser != "http" {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}

	headers := normalizeCorseuHeaders(model.Headers)
	applyCorseuRules(settings.Rules, method, urlValue, headers)

	contentType := strings.TrimSpace(headers["content-type"])
	delete(headers, "content-type")
	delete(headers, "content-length")

	timeout := 15
	if model.Timeout != nil && *model.Timeout > 5 {
		timeout = *model.Timeout
	}

	httpVersion := 1
	if model.HTTPVersion != nil && *model.HTTPVersion == 2 {
		httpVersion = 2
	}

	useDefaultHeaders := true
	if model.DefaultHeaders != nil {
		useDefaultHeaders = *model.DefaultHeaders
	}

	autoRedirect := true
	if model.AutoRedirect != nil {
		autoRedirect = *model.AutoRedirect
	}

	headersOnly := false
	if model.HeadersOnly != nil {
		headersOnly = *model.HeadersOnly
	}

	// Named proxy mappings are not ported yet, keep legacy fallback to preserve behavior.
	if strings.TrimSpace(model.ProxyName) != "" {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}

	if err := sendCorseuHTTPRequest(w, r, corseuHTTPOptions{
		Method:            method,
		URL:               urlValue,
		Data:              model.Data,
		EncodingName:      model.Encoding,
		ContentType:       contentType,
		Headers:           headers,
		TimeoutSeconds:    timeout,
		HTTPVersion:       httpVersion,
		UseDefaultHeaders: useDefaultHeaders,
		AutoRedirect:      autoRedirect,
		HeadersOnly:       headersOnly,
		ProxyRaw:          model.Proxy,
	}); err != nil {
		switch {
		case errors.Is(err, contextDeadlineExceededErr):
			w.WriteHeader(http.StatusRequestTimeout)
		default:
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	}
}

var contextDeadlineExceededErr = errors.New("timeout")

type corseuHTTPOptions struct {
	Method            string
	URL               string
	Data              string
	EncodingName      string
	ContentType       string
	Headers           map[string]string
	TimeoutSeconds    int
	HTTPVersion       int
	UseDefaultHeaders bool
	AutoRedirect      bool
	HeadersOnly       bool
	ProxyRaw          string
}

func sendCorseuHTTPRequest(w http.ResponseWriter, r *http.Request, opts corseuHTTPOptions) error {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	if proxyURL := parseCorseuProxyURL(opts.ProxyRaw); proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(maxInt(5, opts.TimeoutSeconds)) * time.Second,
	}
	if !opts.AutoRedirect {
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	var body io.Reader
	if opts.Data != "" {
		body = strings.NewReader(opts.Data)
	}

	req, err := http.NewRequestWithContext(r.Context(), opts.Method, opts.URL, body)
	if err != nil {
		return err
	}
	if opts.HTTPVersion == 2 {
		req.Proto = "HTTP/2.0"
		req.ProtoMajor = 2
		req.ProtoMinor = 0
	}

	if strings.TrimSpace(opts.ContentType) != "" && body != nil {
		req.Header.Set("Content-Type", strings.TrimSpace(opts.ContentType))
	}

	for k, v := range opts.Headers {
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		req.Header.Set(key, val)
	}

	if opts.UseDefaultHeaders && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0")
	}

	resp, err := client.Do(req)
	if err != nil {
		if isCorseuTimeout(err) {
			return contextDeadlineExceededErr
		}
		return err
	}
	defer resp.Body.Close()

	for k, values := range resp.Header {
		if corseuShouldSkipHeader(k) {
			continue
		}
		if strings.EqualFold(k, "Content-Type") {
			w.Header().Set("Content-Type", strings.Join(values, ", "))
			continue
		}
		w.Header().Set(k, strings.Join(values, ", "))
	}
	w.WriteHeader(resp.StatusCode)

	if opts.HeadersOnly {
		return nil
	}

	_, err = io.Copy(w, resp.Body)
	return err
}

func parseCorseuProxyURL(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	if u.Host == "" {
		return nil
	}
	return u
}

func corseuShouldSkipHeader(header string) bool {
	key := strings.ToLower(strings.TrimSpace(header))
	switch {
	case key == "":
		return true
	case key == "content-length":
		return true
	case key == "transfer-encoding":
		return true
	case key == "connection":
		return true
	case key == "keep-alive":
		return true
	case key == "content-disposition":
		return true
	case key == "content-encoding":
		return true
	case key == "content-security-policy":
		return true
	case key == "vary":
		return true
	case key == "alt-svc":
		return true
	case strings.HasPrefix(key, "access-control"):
		return true
	case strings.HasPrefix(key, "x-"):
		return true
	default:
		return false
	}
}

func loadCorseuSettings() corseuSettings {
	settings := corseuSettings{
		Tokens: map[string]struct{}{},
		Rules:  make([]corseuRule, 0),
	}

	data, ok := readFileAny("init.conf")
	if !ok {
		return settings
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return settings
	}

	node, ok := root["corseu"].(map[string]any)
	if !ok {
		return settings
	}

	if arr, ok := node["tokens"].([]any); ok {
		for _, item := range arr {
			token := strings.TrimSpace(toString(item))
			if token != "" {
				settings.Tokens[token] = struct{}{}
			}
		}
	}

	if arr, ok := node["rules"].([]any); ok {
		for _, item := range arr {
			raw, ok := item.(map[string]any)
			if !ok {
				continue
			}
			rule := corseuRule{
				Method:  strings.TrimSpace(toString(raw["method"])),
				URL:     strings.TrimSpace(toString(raw["url"])),
				Replace: toBool(raw["replace"]),
				Headers: map[string]string{},
			}

			if hdrRaw, ok := raw["headers"].(map[string]any); ok {
				for k, v := range hdrRaw {
					key := strings.ToLower(strings.TrimSpace(k))
					val := strings.TrimSpace(toString(v))
					if key != "" && val != "" {
						rule.Headers[key] = val
					}
				}
			}

			if len(rule.Headers) > 0 && rule.Method != "" && rule.URL != "" {
				settings.Rules = append(settings.Rules, rule)
			}
		}
	}

	return settings
}

func parseCorseuHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]string{}
	}

	var parsed map[string]string
	if err := stdjson.Unmarshal([]byte(raw), &parsed); err != nil {
		return map[string]string{}
	}
	return normalizeCorseuHeaders(parsed)
}

func normalizeCorseuHeaders(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func applyCorseuRules(rules []corseuRule, method, targetURL string, headers map[string]string) {
	for _, rule := range rules {
		if !strings.EqualFold(rule.Method, method) {
			continue
		}

		pattern := rule.URL
		if !strings.HasPrefix(pattern, "(?i)") {
			pattern = "(?i)" + pattern
		}
		matched, err := regexp.MatchString(pattern, targetURL)
		if err != nil || !matched {
			continue
		}

		if rule.Replace {
			for k := range headers {
				delete(headers, k)
			}
		}
		maps.Copy(headers, rule.Headers)
	}
}

func isCorseuTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, contextDeadlineExceededErr) {
		return true
	}

	type timeout interface {
		Timeout() bool
	}
	var te timeout
	return errors.As(err, &te) && te.Timeout()
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return parseBoolLike(t)
	default:
		return parseBoolLike(toString(v))
	}
}

func maxInt(a, b int) int {
	if a >= b {
		return a
	}
	return b
}
