package jsmodules

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/rs/zerolog"

	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

// ProxyBuilder is the minimal subset of proxylink.Manager the runtime needs;
// it is kept as an interface so tests can inject fakes.
type ProxyBuilder interface {
	EncryptURI(uri, reqip, plugin string, verifyip, forceMD5, isProxyImg bool) string
	EncryptURIWithHeaders(uri, reqip, plugin string, headers map[string]string) string
}

// HTTPClient lets tests stub HTTP. Nil falls back to http.DefaultClient.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Runtime executes a single JavaScript module. Runtime instances are not safe
// for concurrent use — the Manager keeps a pool and loans one per request.
type Runtime struct {
	module *Module
	vm     *goja.Runtime

	httpClient HTTPClient
	proxyBuild ProxyBuilder
	cache      *cacheStore
	logger     *zerolog.Logger
	logSink    *logSink

	// manager is the parent — used to access the http client cache and the
	// FlareSolverr URL, both shared across runtimes.
	manager *Manager

	// jar persists cookies for the duration of one Invoke (one js handle()
	// call). DDoS-Guard / DLE / anti-bot CDNs typically set a session cookie
	// on the first request and require it on the second; without a jar the
	// runtime would lose those cookies between http.* calls.
	jar *cookiejar.Jar

	// host + reqIP are captured from Invocation on Invoke() so proxy.url()
	// can build full "<scheme>://<host>/proxy/<enc>" URLs for the client.
	host  string
	reqIP string

	ctx     context.Context
	cancel  context.CancelFunc
	timeout time.Duration
}

// NewRuntime creates a fresh goja VM bound to the module. httpClient and
// proxyBuild may be nil (default client / no proxy). When mgr is non-nil the
// runtime gets access to the http client cache and FlareSolverr URL.
func NewRuntime(m *Module, httpClient HTTPClient, proxyBuild ProxyBuilder, logger *zerolog.Logger, sink *logSink) (*Runtime, error) {
	return NewRuntimeWithManager(m, httpClient, proxyBuild, logger, sink, nil)
}

// NewRuntimeWithManager is the same as NewRuntime but lets the caller wire a
// Manager pointer so per-call transports and FlareSolverr are available.
func NewRuntimeWithManager(m *Module, httpClient HTTPClient, proxyBuild ProxyBuilder, logger *zerolog.Logger, sink *logSink, mgr *Manager) (*Runtime, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	jar, _ := cookiejar.New(nil)
	r := &Runtime{
		module:     m,
		vm:         goja.New(),
		httpClient: httpClient,
		proxyBuild: proxyBuild,
		cache:      m.cache,
		logger:     logger,
		logSink:    sink,
		manager:    mgr,
		jar:        jar,
		timeout:    30 * time.Second,
	}
	r.vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	if err := r.installGlobals(); err != nil {
		return nil, err
	}
	if _, err := r.vm.RunProgram(m.program); err != nil {
		return nil, fmt.Errorf("module %s: %w", m.Manifest.ID, err)
	}
	return r, nil
}

// Close releases the VM. Must be called to free memory.
func (r *Runtime) Close() {
	if r.cancel != nil {
		r.cancel()
	}
	// Intentionally do NOT nil r.vm — an unsynchronized write races with any
	// goroutine still holding a reference (e.g. the Invoke watchdog). GC will
	// reclaim the VM once the Runtime itself is unreachable.
}

// Invocation is the input to a JS module handler call.
type Invocation struct {
	Query       map[string]string
	Headers     map[string]string
	Host        string
	RequestIP   string
	Path        string
	LifeMode    bool
	Checksearch bool
	UserAgent   string
}

// Response is what the JS module returns. Either Body is raw JSON bytes, or
// Value is a marshallable any that the caller will serialize.
type Response struct {
	Body        []byte
	ContentType string
}

// Invoke calls the module's handler with the given invocation context, aborting
// after the runtime timeout. Returns the JSON-marshalled response.
func (r *Runtime) Invoke(ctx context.Context, inv Invocation) (Response, error) {
	rctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	r.ctx = rctx
	r.cancel = cancel
	r.host = inv.Host
	r.reqIP = inv.RequestIP

	// Set an interrupt so a runaway script is killed. Capture vm locally and
	// wait for the watchdog to exit before returning — otherwise the goroutine
	// can outlive Invoke and race with Close.
	vm := r.vm
	done := make(chan struct{})
	watchdog := make(chan struct{})
	go func() {
		defer close(watchdog)
		select {
		case <-rctx.Done():
			if vm != nil {
				vm.Interrupt("timeout")
			}
		case <-done:
		}
	}()
	defer func() {
		close(done)
		<-watchdog
	}()

	handler := r.vm.Get("handle")
	if handler == nil || goja.IsUndefined(handler) {
		if exp := r.vm.Get("module"); exp != nil && !goja.IsUndefined(exp) {
			obj := exp.ToObject(r.vm)
			if exports := obj.Get("exports"); exports != nil && !goja.IsUndefined(exports) {
				handler = exports.ToObject(r.vm).Get("handle")
			}
		}
	}
	if handler == nil || goja.IsUndefined(handler) {
		return Response{}, fmt.Errorf("module %s: no handle() function exported", r.module.Manifest.ID)
	}

	fn, ok := goja.AssertFunction(handler)
	if !ok {
		return Response{}, fmt.Errorf("module %s: handle is not a function", r.module.Manifest.ID)
	}

	invObj := r.vm.NewObject()
	_ = invObj.Set("query", inv.Query)
	_ = invObj.Set("headers", inv.Headers)
	_ = invObj.Set("host", inv.Host)
	_ = invObj.Set("requestIP", inv.RequestIP)
	_ = invObj.Set("path", inv.Path)
	_ = invObj.Set("life", inv.LifeMode)
	_ = invObj.Set("checksearch", inv.Checksearch)
	_ = invObj.Set("userAgent", inv.UserAgent)
	_ = invObj.Set("config", r.module.ConfigSnapshot())

	result, err := fn(goja.Undefined(), invObj)
	if err != nil {
		return Response{}, fmt.Errorf("module %s handle: %w", r.module.Manifest.ID, err)
	}

	if result == nil || goja.IsUndefined(result) || goja.IsNull(result) {
		return Response{Body: []byte("{}"), ContentType: "application/json; charset=utf-8"}, nil
	}

	// If result is a string, treat as raw body (caller sets content type).
	if s, ok := result.Export().(string); ok {
		return Response{Body: []byte(s), ContentType: "application/json; charset=utf-8"}, nil
	}

	// Marshal JSON.
	exported := result.Export()
	body, err := json.Marshal(exported)
	if err != nil {
		return Response{}, fmt.Errorf("module %s: marshal result: %w", r.module.Manifest.ID, err)
	}
	return Response{Body: body, ContentType: "application/json; charset=utf-8"}, nil
}

// installGlobals wires up the JS sandbox APIs available to every module.
func (r *Runtime) installGlobals() error {
	vm := r.vm

	// console.log / warn / error
	console := vm.NewObject()
	_ = console.Set("log", func(call goja.FunctionCall) goja.Value { r.jsLog("info", call); return goja.Undefined() })
	_ = console.Set("info", func(call goja.FunctionCall) goja.Value { r.jsLog("info", call); return goja.Undefined() })
	_ = console.Set("warn", func(call goja.FunctionCall) goja.Value { r.jsLog("warn", call); return goja.Undefined() })
	_ = console.Set("error", func(call goja.FunctionCall) goja.Value { r.jsLog("error", call); return goja.Undefined() })
	_ = console.Set("debug", func(call goja.FunctionCall) goja.Value { r.jsLog("debug", call); return goja.Undefined() })
	_ = vm.Set("console", console)

	// log alias
	_ = vm.Set("log", console)

	// module.exports scaffolding so modules can use commonjs style.
	mod := vm.NewObject()
	_ = mod.Set("exports", vm.NewObject())
	_ = vm.Set("module", mod)
	_ = vm.Set("exports", mod.Get("exports"))

	// base64
	_ = vm.Set("btoa", func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) })
	_ = vm.Set("atob", func(s string) (string, error) {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil {
			// try URL-safe / no padding
			b, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
		}
		if err != nil {
			return "", err
		}
		return string(b), nil
	})

	// http API
	http := vm.NewObject()
	_ = http.Set("get", func(call goja.FunctionCall) goja.Value { return r.jsHTTP("GET", call) })
	_ = http.Set("post", func(call goja.FunctionCall) goja.Value { return r.jsHTTP("POST", call) })
	_ = http.Set("put", func(call goja.FunctionCall) goja.Value { return r.jsHTTP("PUT", call) })
	_ = http.Set("delete", func(call goja.FunctionCall) goja.Value { return r.jsHTTP("DELETE", call) })
	_ = http.Set("head", func(call goja.FunctionCall) goja.Value { return r.jsHTTP("HEAD", call) })
	_ = http.Set("request", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		method := "GET"
		if v := call.Arguments[0].String(); v != "" {
			method = strings.ToUpper(v)
		}
		call.Arguments = call.Arguments[1:]
		return r.jsHTTP(method, call)
	})
	_ = vm.Set("http", http)

	// proxy API — wraps a stream URL through /proxy/ so the client can fetch
	// it through our server (applies correct headers, bypasses Origin checks).
	// Returns "<scheme>://<host>/proxy/<enc>" or the raw URL when proxy is
	// disabled / encryption not available.
	proxy := vm.NewObject()
	_ = proxy.Set("url", func(uri, plugin string) string {
		if strings.TrimSpace(uri) == "" {
			return ""
		}
		if r.proxyBuild == nil {
			return uri
		}
		enc := r.proxyBuild.EncryptURI(uri, r.reqIP, defaultPlugin(plugin, r.module.Manifest.ID), false, false, false)
		if enc == "" {
			return uri
		}
		return r.host + "/proxy/" + enc
	})
	_ = proxy.Set("urlWithHeaders", func(uri, plugin string, headers map[string]any) string {
		if strings.TrimSpace(uri) == "" {
			return ""
		}
		if r.proxyBuild == nil {
			return uri
		}
		h := map[string]string{}
		for k, v := range headers {
			if s, ok := v.(string); ok {
				h[k] = s
			}
		}
		enc := r.proxyBuild.EncryptURIWithHeaders(uri, r.reqIP, defaultPlugin(plugin, r.module.Manifest.ID), h)
		if enc == "" {
			return uri
		}
		return r.host + "/proxy/" + enc
	})
	// img() / imgWithHeaders() wrap a poster/thumbnail URL through /proxyimg/
	// so it gets the LRU + disk cache + on-demand resize pipeline. Use these
	// (instead of url/urlWithHeaders) for any image the Lampa client renders.
	_ = proxy.Set("img", func(uri, plugin string) string {
		if strings.TrimSpace(uri) == "" {
			return ""
		}
		if r.proxyBuild == nil {
			return uri
		}
		enc := r.proxyBuild.EncryptURI(uri, r.reqIP, defaultPlugin(plugin, r.module.Manifest.ID), false, false, true)
		if enc == "" {
			return uri
		}
		return r.host + "/proxyimg/" + enc
	})
	_ = proxy.Set("imgWithHeaders", func(uri, plugin string, headers map[string]any) string {
		if strings.TrimSpace(uri) == "" {
			return ""
		}
		if r.proxyBuild == nil {
			return uri
		}
		h := map[string]string{}
		for k, v := range headers {
			if s, ok := v.(string); ok {
				h[k] = s
			}
		}
		enc := r.proxyBuild.EncryptURIWithHeaders(uri, r.reqIP, defaultPlugin(plugin, r.module.Manifest.ID), h)
		if enc == "" {
			return uri
		}
		return r.host + "/proxyimg/" + enc
	})
	_ = vm.Set("proxy", proxy)

	// cache API (per-module, TTL seconds)
	cache := vm.NewObject()
	_ = cache.Set("get", func(key string) goja.Value {
		v, ok := r.cache.Get(key)
		if !ok {
			return goja.Undefined()
		}
		return vm.ToValue(v)
	})
	_ = cache.Set("set", func(key string, value any, ttlSec int) {
		if ttlSec <= 0 {
			ttlSec = 300
		}
		r.cache.Set(key, value, time.Duration(ttlSec)*time.Second)
	})
	_ = cache.Set("delete", func(key string) { r.cache.Delete(key) })
	_ = cache.Set("clear", func() { r.cache.Clear() })
	_ = vm.Set("cache", cache)

	// util
	util := vm.NewObject()
	_ = util.Set("urlencode", url.QueryEscape)
	_ = util.Set("urldecode", func(s string) string {
		v, _ := url.QueryUnescape(s)
		return v
	})
	_ = util.Set("md5", func(s string) string {
		h := md5.Sum([]byte(s))
		return hex.EncodeToString(h[:])
	})
	_ = util.Set("sha1", func(s string) string {
		h := sha1.Sum([]byte(s))
		return hex.EncodeToString(h[:])
	})
	_ = util.Set("sha256", func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	})
	_ = util.Set("hexEncode", func(s string) string { return hex.EncodeToString([]byte(s)) })
	_ = util.Set("hexDecode", func(s string) (string, error) {
		b, err := hex.DecodeString(s)
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
	_ = util.Set("trim", strings.TrimSpace)
	_ = util.Set("sleep", func(ms int) {
		// Bounded wait that respects the runtime timeout.
		d := time.Duration(ms) * time.Millisecond
		if d > r.timeout {
			d = r.timeout
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.ctx.Done():
		}
	})
	_ = util.Set("buildEpisodeIdent", func(base, s, e, t string) string {
		// Lampa standard ident: "{base}_{s}_{e}_{t}"
		parts := []string{base}
		for _, p := range []string{s, e, t} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, "_")
	})
	// Normalize a title for fuzzy comparison: lowercase, drop punctuation and
	// diacritics, collapse whitespace. Useful for checksearch match-by-title.
	_ = util.Set("normalizeTitle", func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		var b strings.Builder
		prevSpace := false
		for _, ch := range s {
			switch {
			case (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9'):
				b.WriteRune(ch)
				prevSpace = false
			case ch >= 'а' && ch <= 'я', ch == 'ё', ch == 'і', ch == 'ї', ch == 'є', ch == 'ґ', ch == 'ы', ch == 'ъ', ch == 'э', ch == 'ю', ch == 'я':
				b.WriteRune(ch)
				prevSpace = false
			case ch == ' ' || ch == '\t' || ch == '-' || ch == '_':
				if !prevSpace && b.Len() > 0 {
					b.WriteByte(' ')
				}
				prevSpace = true
			}
		}
		return strings.TrimRight(b.String(), " ")
	})
	_ = vm.Set("util", util)

	// manifest is available to the module for self-reference
	_ = vm.Set("manifest", r.module.Manifest)

	return nil
}

func defaultPlugin(given, fallback string) string {
	if strings.TrimSpace(given) != "" {
		return given
	}
	return fallback
}

// jsLog forwards console.* into the module's log sink (ring buffer + zerolog).
func (r *Runtime) jsLog(level string, call goja.FunctionCall) {
	parts := make([]string, 0, len(call.Arguments))
	for _, a := range call.Arguments {
		if a == nil || goja.IsUndefined(a) {
			parts = append(parts, "undefined")
			continue
		}
		if goja.IsNull(a) {
			parts = append(parts, "null")
			continue
		}
		if s, ok := a.Export().(string); ok {
			parts = append(parts, s)
			continue
		}
		if b, err := json.Marshal(a.Export()); err == nil {
			parts = append(parts, string(b))
		} else {
			parts = append(parts, a.String())
		}
	}
	msg := strings.Join(parts, " ")
	if r.logSink != nil {
		r.logSink.Append(r.module.Manifest.ID, level, msg)
	}
	if r.logger != nil {
		ev := r.logger.Info()
		switch level {
		case "warn":
			ev = r.logger.Warn()
		case "error":
			ev = r.logger.Error()
		case "debug":
			ev = r.logger.Debug()
		}
		ev.Str("module", r.module.Manifest.ID).Msg(msg)
	}
}

// optsLookup извлекает числовое значение из opts (goja конвертирует JS-числа
// в float64; int / int64 встречаются у host-side кода). Возвращает 0/false если
// ключа нет или значение не число.
func optsLookup(opts map[string]any, key string) (int64, bool) {
	if opts == nil {
		return 0, false
	}
	switch v := opts[key].(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	}
	return 0, false
}

// jsHTTP implements the http.get/post/put/... bindings.
func (r *Runtime) jsHTTP(method string, call goja.FunctionCall) goja.Value {
	if len(call.Arguments) == 0 {
		panic(r.vm.ToValue("http: url is required"))
	}
	rawURL := call.Arguments[0].String()
	var opts map[string]any
	if len(call.Arguments) > 1 {
		if v := call.Arguments[1]; !goja.IsUndefined(v) && !goja.IsNull(v) {
			if m, ok := v.Export().(map[string]any); ok {
				opts = m
			}
		}
	}

	// FlareSolverr fast-path: route the entire request through a configured
	// FlareSolverr instance. The "client" doesn't apply — FlareSolverr handles
	// TLS/JS challenges itself and returns the rendered HTML.
	if r.shouldUseFlareSolverr(opts) {
		return r.jsHTTPFlareSolverr(method, rawURL, opts)
	}

	req, err := r.buildRequest(method, rawURL, opts)
	if err != nil {
		panic(r.vm.ToValue("http: " + err.Error()))
	}

	client := r.clientForOpts(opts)

	// Capture start time for logging.
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		// Return a response-shaped object with error, not panic — JS can decide.
		obj := r.vm.NewObject()
		_ = obj.Set("ok", false)
		_ = obj.Set("error", err.Error())
		_ = obj.Set("status", 0)
		_ = obj.Set("duration_ms", time.Since(start).Milliseconds())
		return obj
	}
	defer resp.Body.Close()

	// Лимит чтения тела ответа. Дефолт 8 МБ — защита от случайных гигантских
	// HTML/JSON. Модуль может переопределить через opts.maxBytes (например,
	// scts-каталог 18 МБ распакованного требует 32+ МБ лимита).
	limit := int64(8 * 1024 * 1024)
	if optMax, ok := optsLookup(opts, "maxBytes"); ok && optMax > 0 {
		limit = optMax
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	// Auto-decode gzip when sender forgot to set Content-Encoding: gzip
	// (Python http.server / некоторые CDN отдают .gz как-есть без header'а).
	// Распознаём по magic-байтам 1F 8B; в этом случае распакуем поток до того
	// же лимита.  Когда сервер прислал Content-Encoding: gzip, Go-клиент уже
	// распаковал тело сам — magic не сработает, и лимит должен быть выставлен
	// заранее достаточно большим (см. opts.maxBytes).
	if len(bodyBytes) >= 2 && bodyBytes[0] == 0x1f && bodyBytes[1] == 0x8b {
		if gz, gerr := gzip.NewReader(bytes.NewReader(bodyBytes)); gerr == nil {
			if dec, derr := io.ReadAll(io.LimitReader(gz, limit*8)); derr == nil {
				bodyBytes = dec
			}
			gz.Close()
		}
	}
	bodyStr := string(bodyBytes)

	obj := r.vm.NewObject()
	_ = obj.Set("ok", resp.StatusCode >= 200 && resp.StatusCode < 400)
	_ = obj.Set("status", resp.StatusCode)
	_ = obj.Set("url", resp.Request.URL.String())
	_ = obj.Set("duration_ms", time.Since(start).Milliseconds())
	_ = obj.Set("text", bodyStr)
	_ = obj.Set("body", bodyStr)

	// Headers: flatten to first value to match typical JS fetch shape.
	hdr := r.vm.NewObject()
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			_ = hdr.Set(strings.ToLower(k), vs[0])
		}
	}
	_ = obj.Set("headers", hdr)

	// Cookies helper.
	cookies := []map[string]string{}
	for _, c := range resp.Cookies() {
		cookies = append(cookies, map[string]string{"name": c.Name, "value": c.Value})
	}
	_ = obj.Set("cookies", cookies)

	// Lazy json parsing: attach .json() callable.
	_ = obj.Set("json", func() goja.Value {
		var out any
		if err := json.Unmarshal(bodyBytes, &out); err != nil {
			panic(r.vm.ToValue("http: json parse: " + err.Error()))
		}
		return r.vm.ToValue(out)
	})

	return obj
}

// clientForOpts picks an http.Client for a single js call, honouring the
// transport/proxy/balancer keys (and manifest defaults) and attaching the
// runtime's cookie jar so cookies persist across calls within one Invoke.
func (r *Runtime) clientForOpts(opts map[string]any) HTTPClient {
	defaults := r.moduleHTTPDefaults()
	spec := parseTransportSpec(opts, defaults)
	if r.manager != nil {
		return r.manager.pickClient(spec, r.jar, r.httpClient)
	}
	// Standalone runtime (no manager): wrap the injected client with our jar
	// when possible so cookies still stick within the call.
	if r.jar != nil {
		if base, ok := r.httpClient.(*http.Client); ok {
			return cloneClientWithJar(base, r.jar)
		}
	}
	return r.httpClient
}

// moduleHTTPDefaults builds defaults from the manifest, falling back to the
// module ID for the balancer name so a [[proxy.direct.entries]] entry that
// names the module ID just works.
func (r *Runtime) moduleHTTPDefaults() moduleHTTPDefaults {
	d := moduleHTTPDefaults{}
	if r.module != nil {
		d.Transport = strings.TrimSpace(r.module.Manifest.DefaultTransport)
		d.Proxy = strings.TrimSpace(r.module.Manifest.DefaultProxy)
		d.Balancer = strings.TrimSpace(r.module.Manifest.DefaultBalancer)
		if d.Balancer == "" {
			d.Balancer = strings.ToLower(strings.TrimSpace(r.module.Manifest.ID))
		}
	}
	return d
}

// shouldUseFlareSolverr reports whether opts.transport == "flaresolverr" (or
// the manifest's default_transport is). The manager must have a non-empty
// FlareSolverr URL or the call falls through to the regular path.
func (r *Runtime) shouldUseFlareSolverr(opts map[string]any) bool {
	t := ""
	if opts != nil {
		if v, ok := opts["transport"].(string); ok {
			t = strings.ToLower(strings.TrimSpace(v))
		}
	}
	if t == "" && r.module != nil {
		t = strings.ToLower(strings.TrimSpace(r.module.Manifest.DefaultTransport))
	}
	if t != "flaresolverr" {
		return false
	}
	if r.manager == nil || strings.TrimSpace(r.manager.FlareSolverr) == "" {
		return false
	}
	return true
}

// jsHTTPFlareSolverr runs the call through a FlareSolverr v1 endpoint and
// returns a response-shaped object compatible with the regular http.* return
// (ok, status, text, body, headers, cookies, json()).
func (r *Runtime) jsHTTPFlareSolverr(method, rawURL string, opts map[string]any) goja.Value {
	cmd := "request.get"
	if strings.EqualFold(method, "POST") {
		cmd = "request.post"
	}

	// Form / body coalescing: FlareSolverr accepts only x-www-form-urlencoded
	// for postData. JSON bodies aren't supported upstream — fall back to GET
	// or warn in the response.
	postData := ""
	if opts != nil {
		if f, ok := opts["form"].(map[string]any); ok {
			vals := url.Values{}
			for k, v := range f {
				vals.Set(k, fmt.Sprint(v))
			}
			postData = vals.Encode()
		} else if b, ok := opts["body"].(string); ok {
			postData = b
		}
	}

	// Per-call timeout (ms). Honour opts.timeout (sec) or default to 30s.
	timeoutMS := 30000
	if opts != nil {
		if v, ok := opts["timeout"].(float64); ok && v > 0 {
			timeoutMS = int(v) * 1000
		}
	}

	// Resolve upstream proxy: explicit opts.proxy wins; else look up the
	// balancer-registered SOCKS5 (manifest default_balancer / opts.balancer);
	// FlareSolverr accepts socks5://host:port.
	//
	// opts.flareNoProxy=true forces FS to solve from its own IP — useful when
	// the balancer's SOCKS5 is bound to 127.0.0.1 and unreachable from the
	// FlareSolverr docker container (we'd otherwise see ERR_PROXY_CONNECTION_FAILED).
	defaults := r.moduleHTTPDefaults()
	spec := parseTransportSpec(opts, defaults)
	noProxy := false
	if opts != nil {
		if v, ok := opts["flareNoProxy"].(bool); ok {
			noProxy = v
		}
	}
	fsProxy := ""
	if !noProxy {
		if p := strings.TrimSpace(spec.proxy); p != "" {
			if !strings.Contains(p, "://") {
				fsProxy = "socks5://" + p
			} else {
				fsProxy = p
			}
		} else if spec.balancer != "" {
			if addr := httpclient.SocksAddrForBalancer(spec.balancer); addr != "" {
				fsProxy = "socks5://" + addr
			}
		}
	}

	// Forward opts.headers into FlareSolverr so Chrome ships e.g. Referer/UA
	// matching what a regular http.get would send. Without this, the upstream
	// site sees a "browser without context" and may behave differently
	// (observed: ladoni.pro returns plain nginx 404 instead of its anti-bot
	// stub when Referer is missing).
	var fsHeaders map[string]string
	if opts != nil {
		if h, ok := opts["headers"].(map[string]any); ok {
			fsHeaders = make(map[string]string, len(h))
			for k, v := range h {
				if s, ok := v.(string); ok {
					fsHeaders[k] = s
				}
			}
		}
	}

	start := time.Now()
	res, ok := httpclient.FlareSolverrDo(r.ctx, r.manager.FlareSolverr, httpclient.FlareSolverrRequest{
		Cmd:          cmd,
		URL:          rawURL,
		PostData:     postData,
		MaxTimeoutMS: timeoutMS,
		Proxy:        fsProxy,
		Headers:      fsHeaders,
	})

	obj := r.vm.NewObject()
	_ = obj.Set("duration_ms", time.Since(start).Milliseconds())

	if !ok {
		_ = obj.Set("ok", false)
		_ = obj.Set("status", res.Solution.Status)
		_ = obj.Set("error", "flaresolverr: "+strings.TrimSpace(res.Status+" "+res.Message))
		return obj
	}

	bodyStr := res.Solution.Response
	_ = obj.Set("ok", res.Solution.Status >= 200 && res.Solution.Status < 400)
	_ = obj.Set("status", res.Solution.Status)
	_ = obj.Set("url", res.Solution.URL)
	_ = obj.Set("text", bodyStr)
	_ = obj.Set("body", bodyStr)

	hdr := r.vm.NewObject()
	for k, v := range res.Solution.Headers {
		_ = hdr.Set(strings.ToLower(k), v)
	}
	_ = obj.Set("headers", hdr)

	cookies := make([]map[string]string, 0, len(res.Solution.Cookies))
	for _, c := range res.Solution.Cookies {
		cookies = append(cookies, map[string]string{"name": c.Name, "value": c.Value})
	}
	_ = obj.Set("cookies", cookies)

	bytesCopy := []byte(bodyStr)
	_ = obj.Set("json", func() goja.Value {
		var out any
		if err := json.Unmarshal(bytesCopy, &out); err != nil {
			panic(r.vm.ToValue("http: json parse: " + err.Error()))
		}
		return r.vm.ToValue(out)
	})

	return obj
}

// buildRequest constructs an http.Request from JS-style opts.
// Supported opts keys: headers (obj), body (string|obj), form (obj), timeout (int sec), referer, userAgent
func (r *Runtime) buildRequest(method, rawURL string, opts map[string]any) (*http.Request, error) {
	var body io.Reader
	contentType := ""

	if opts != nil {
		if f, ok := opts["form"].(map[string]any); ok {
			vals := url.Values{}
			for k, v := range f {
				vals.Set(k, fmt.Sprint(v))
			}
			body = strings.NewReader(vals.Encode())
			contentType = "application/x-www-form-urlencoded"
		} else if b, ok := opts["body"]; ok {
			switch bv := b.(type) {
			case string:
				body = strings.NewReader(bv)
			case map[string]any, []any:
				data, err := json.Marshal(bv)
				if err != nil {
					return nil, fmt.Errorf("marshal body: %w", err)
				}
				body = bytes.NewReader(data)
				contentType = "application/json"
			}
		}
	}

	req, err := http.NewRequestWithContext(r.ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	if opts != nil {
		if h, ok := opts["headers"].(map[string]any); ok {
			for k, v := range h {
				req.Header.Set(k, fmt.Sprint(v))
			}
		}
		if v, ok := opts["userAgent"].(string); ok {
			req.Header.Set("User-Agent", v)
		}
		if v, ok := opts["referer"].(string); ok {
			req.Header.Set("Referer", v)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	}
	return req, nil
}

// cacheStore is a simple in-memory TTL cache, per module.
type cacheStore struct {
	mu   sync.RWMutex
	data map[string]cacheEntry
}

type cacheEntry struct {
	value any
	exp   time.Time
}

func newCacheStore() *cacheStore { return &cacheStore{data: make(map[string]cacheEntry)} }

func (c *cacheStore) Get(key string) (any, bool) {
	c.mu.RLock()
	e, ok := c.data[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.exp) {
		if ok {
			c.mu.Lock()
			delete(c.data, key)
			c.mu.Unlock()
		}
		return nil, false
	}
	return e.value, true
}

func (c *cacheStore) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	c.data[key] = cacheEntry{value: value, exp: time.Now().Add(ttl)}
	c.mu.Unlock()
}

func (c *cacheStore) Delete(key string) {
	c.mu.Lock()
	delete(c.data, key)
	c.mu.Unlock()
}

func (c *cacheStore) Clear() {
	c.mu.Lock()
	c.data = make(map[string]cacheEntry)
	c.mu.Unlock()
}

// Assert proxylink.Manager satisfies ProxyBuilder (compile-time check).
var _ ProxyBuilder = (*proxylink.Manager)(nil)
