package wasmmodules

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"lampac-go/internal/proxylink"
)

// ProxyBuilder is the minimal subset of proxylink.Manager the runtime needs.
// Same interface as jsmodules.ProxyBuilder so the same wiring works for both.
type ProxyBuilder interface {
	EncryptURI(uri, reqip, plugin string, verifyip, forceMD5, isProxyImg bool) string
	EncryptURIWithHeaders(uri, reqip, plugin string, headers map[string]string) string
}

// HTTPClient lets tests stub HTTP. Nil falls back to http.DefaultClient.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Invocation is the input to a guest's handle() — same fields the JS modules
// receive so a plugin authored for either runtime sees the same shape.
type Invocation struct {
	Query       map[string]string `json:"query"`
	Headers     map[string]string `json:"headers"`
	Host        string            `json:"host"`
	RequestIP   string            `json:"requestIP"`
	Path        string            `json:"path"`
	LifeMode    bool              `json:"life"`
	Checksearch bool              `json:"checksearch"`
	UserAgent   string            `json:"userAgent"`
	Config      json.RawMessage   `json:"config"`
}

// Response is what handle() returns: a JSON body that the HTTP handler will
// either pass through or convert to Lampa-flavoured HTML.
type Response struct {
	Body        []byte
	ContentType string
}

// Runtime owns one wazero instance for one .wasm module. Runtime is NOT safe
// for concurrent use — Manager keeps one per loaded module and serializes
// Invoke calls (cheap; .wasm modules are tiny and hot in memory).
type Runtime struct {
	module       *Module
	wzRuntime    wazero.Runtime
	instance     api.Module
	allocFn      api.Function
	handleFn     api.Function
	abiVersionFn api.Function

	httpClient HTTPClient
	proxy      ProxyBuilder
	cache      *cacheStore
	logger     *zerolog.Logger
	logSink    *logSink

	host    string
	reqIP   string
	timeout time.Duration

	mu sync.Mutex
}

// NewRuntime instantiates `wasmBytes` against a fresh wazero runtime. The
// wasi_snapshot_preview1 host is wired so TinyGo binaries can call _start /
// fd_write etc. — but stdout/stderr are routed to our logSink.
func NewRuntime(
	ctx context.Context,
	mod *Module,
	wasmBytes []byte,
	httpClient HTTPClient,
	proxy ProxyBuilder,
	logger *zerolog.Logger,
	sink *logSink,
) (*Runtime, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	wzCfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	wz := wazero.NewRuntimeWithConfig(ctx, wzCfg)

	r := &Runtime{
		module:     mod,
		wzRuntime:  wz,
		httpClient: httpClient,
		proxy:      proxy,
		cache:      mod.cache,
		logger:     logger,
		logSink:    sink,
		timeout:    30 * time.Second,
	}

	// WASI for TinyGo / Rust (wasm32-wasi) targets — gives them stdout, env,
	// time, exit codes. We capture stdout/stderr in the log sink so plugins'
	// println goes to admin logs.
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, wz); err != nil {
		_ = wz.Close(ctx)
		return nil, fmt.Errorf("wasi instantiate: %w", err)
	}

	if err := r.installHostImports(ctx); err != nil {
		_ = wz.Close(ctx)
		return nil, err
	}

	// Reactor-mode instantiation: skip _start (which would exit the module)
	// and run _initialize ourselves if the guest exported it. This keeps the
	// WASI runtime initialised across many handle() calls.
	cfg := wazero.NewModuleConfig().
		WithName(mod.Manifest.ID).
		WithStdout(&logWriter{sink: sink, level: "info", module: mod.Manifest.ID}).
		WithStderr(&logWriter{sink: sink, level: "warn", module: mod.Manifest.ID}).
		WithSysWalltime().
		WithSysNanotime().
		WithStartFunctions() // empty list — don't auto-call _start

	inst, err := wz.InstantiateWithConfig(ctx, wasmBytes, cfg)
	if err != nil {
		_ = wz.Close(ctx)
		return nil, fmt.Errorf("instantiate %s: %w", mod.Manifest.ID, err)
	}
	r.instance = inst

	if init := inst.ExportedFunction("_initialize"); init != nil {
		if _, err := init.Call(ctx); err != nil {
			_ = wz.Close(ctx)
			return nil, fmt.Errorf("module %s _initialize: %w", mod.Manifest.ID, err)
		}
	}

	r.allocFn = inst.ExportedFunction("alloc")
	r.handleFn = inst.ExportedFunction("handle")
	r.abiVersionFn = inst.ExportedFunction("abi_version")
	if r.allocFn == nil {
		_ = wz.Close(ctx)
		return nil, fmt.Errorf("module %s: missing exported alloc(size)->i32", mod.Manifest.ID)
	}
	if r.handleFn == nil {
		_ = wz.Close(ctx)
		return nil, fmt.Errorf("module %s: missing exported handle(ptr,len)->i64", mod.Manifest.ID)
	}

	return r, nil
}

// Close releases the wazero runtime. Must be called to free memory.
func (r *Runtime) Close(ctx context.Context) {
	if r.wzRuntime != nil {
		_ = r.wzRuntime.Close(ctx)
	}
}

// Invoke serializes inv to JSON, hands it to the guest's handle(), and reads
// the JSON response back from guest memory.
func (r *Runtime) Invoke(ctx context.Context, inv Invocation) (Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if inv.Config == nil {
		inv.Config = r.module.ConfigSnapshot()
	}
	r.host = inv.Host
	r.reqIP = inv.RequestIP

	rctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	payload, err := json.Marshal(inv)
	if err != nil {
		return Response{}, fmt.Errorf("marshal invocation: %w", err)
	}

	ptr, err := r.copyToGuest(rctx, payload)
	if err != nil {
		return Response{}, err
	}

	results, err := r.handleFn.Call(rctx, uint64(ptr), uint64(len(payload)))
	if err != nil {
		return Response{}, fmt.Errorf("module %s handle: %w", r.module.Manifest.ID, err)
	}
	if len(results) == 0 {
		return Response{}, fmt.Errorf("module %s: handle returned no value", r.module.Manifest.ID)
	}
	respPtr, respLen := UnpackPtrLen(results[0])
	if respLen == 0 {
		return Response{Body: []byte("{}"), ContentType: "application/json; charset=utf-8"}, nil
	}
	body, ok := r.instance.Memory().Read(respPtr, respLen)
	if !ok {
		return Response{}, fmt.Errorf("module %s: response ptr/len out of bounds", r.module.Manifest.ID)
	}
	out := make([]byte, len(body))
	copy(out, body)
	return Response{Body: out, ContentType: "application/json; charset=utf-8"}, nil
}

// copyToGuest reserves a buffer in guest memory via the exported alloc() and
// writes b into it.
func (r *Runtime) copyToGuest(ctx context.Context, b []byte) (uint32, error) {
	if len(b) == 0 {
		return 0, nil
	}
	res, err := r.allocFn.Call(ctx, uint64(len(b)))
	if err != nil {
		return 0, fmt.Errorf("guest alloc: %w", err)
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("guest alloc returned no value")
	}
	ptr := uint32(res[0])
	if !r.instance.Memory().Write(ptr, b) {
		return 0, fmt.Errorf("guest alloc returned out-of-bounds ptr=%d len=%d", ptr, len(b))
	}
	return ptr, nil
}

// readGuest reads len bytes from guest memory at ptr.
func (r *Runtime) readGuest(ptr, length uint32) ([]byte, error) {
	if length == 0 {
		return nil, nil
	}
	b, ok := r.instance.Memory().Read(ptr, length)
	if !ok {
		return nil, fmt.Errorf("guest read out of bounds ptr=%d len=%d", ptr, length)
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

// installHostImports registers the "lampac" host module with the host ABI
// functions: log, http, proxy_url, cache_get/set, config.
func (r *Runtime) installHostImports(ctx context.Context) error {
	b := r.wzRuntime.NewHostModuleBuilder(HostModuleName)

	b.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(r.hostLog),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			nil,
		).
		Export("host_log")

	b.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(r.hostHTTP),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI64},
		).
		Export("host_http")

	b.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(r.hostProxyURL),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI64},
		).
		Export("host_proxy_url")

	b.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(r.hostCacheGet),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI64},
		).
		Export("host_cache_get")

	b.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(r.hostCacheSet),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			nil,
		).
		Export("host_cache_set")

	b.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(r.hostConfig),
			nil,
			[]api.ValueType{api.ValueTypeI64},
		).
		Export("host_config")

	if _, err := b.Instantiate(ctx); err != nil {
		return fmt.Errorf("host imports: %w", err)
	}
	return nil
}

// --- host import implementations ---

func (r *Runtime) hostLog(ctx context.Context, mod api.Module, stack []uint64) {
	level := uint32(stack[0])
	ptr := uint32(stack[1])
	length := uint32(stack[2])
	msg, ok := mod.Memory().Read(ptr, length)
	if !ok {
		return
	}
	text := string(msg)
	if r.logSink != nil {
		lvlName := "info"
		switch level {
		case LogLevelDebug:
			lvlName = "debug"
		case LogLevelWarn:
			lvlName = "warn"
		case LogLevelError:
			lvlName = "error"
		}
		r.logSink.Append(LogEntry{Module: r.module.Manifest.ID, Level: lvlName, Time: time.Now(), Message: text})
	}
	if r.logger != nil {
		switch level {
		case LogLevelDebug:
			r.logger.Debug().Str("module", r.module.Manifest.ID).Msg(text)
		case LogLevelWarn:
			r.logger.Warn().Str("module", r.module.Manifest.ID).Msg(text)
		case LogLevelError:
			r.logger.Error().Str("module", r.module.Manifest.ID).Msg(text)
		default:
			r.logger.Info().Str("module", r.module.Manifest.ID).Msg(text)
		}
	}
}

type httpRequest struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	BodyB64   string            `json:"body_b64"`
	TimeoutMS int               `json:"timeout_ms"`
	Transport string            `json:"transport"`
}

type httpResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
	Error   string            `json:"error,omitempty"`
}

func (r *Runtime) hostHTTP(ctx context.Context, mod api.Module, stack []uint64) {
	reqPtr := uint32(stack[0])
	reqLen := uint32(stack[1])
	rawReq, _ := mod.Memory().Read(reqPtr, reqLen)

	r.module.stats.HTTPRequests.Add(1)
	r.module.stats.HTTPBytesOut.Add(int64(len(rawReq)))

	resp := r.doHTTP(ctx, rawReq)
	body, _ := json.Marshal(resp)
	if len(resp.BodyB64) > 0 {
		// BodyB64 is base64-encoded; the wire body is ~3/4 the b64 length.
		r.module.stats.HTTPBytesIn.Add(int64(len(resp.BodyB64) * 3 / 4))
	}
	stack[0] = r.writeBack(ctx, body)
}

func (r *Runtime) doHTTP(ctx context.Context, raw []byte) httpResponse {
	var req httpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return httpResponse{Error: "bad request: " + err.Error()}
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = "GET"
	}
	if strings.TrimSpace(req.URL) == "" {
		return httpResponse{Error: "url required"}
	}
	if r.module.perms != nil {
		if ok, why := r.module.perms.allowHTTP(req.URL); !ok {
			return httpResponse{Error: "permission denied: " + why}
		}
	}

	var bodyReader io.Reader
	if req.BodyB64 != "" {
		dec, err := base64.StdEncoding.DecodeString(req.BodyB64)
		if err != nil {
			return httpResponse{Error: "bad body_b64: " + err.Error()}
		}
		bodyReader = bytes.NewReader(dec)
	} else if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(rctx, method, req.URL, bodyReader)
	if err != nil {
		return httpResponse{Error: err.Error()}
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return httpResponse{Error: err.Error()}
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 16<<20)) // 16 MiB cap
	if err != nil {
		return httpResponse{Status: httpResp.StatusCode, Error: err.Error()}
	}
	hdrs := map[string]string{}
	for k, vv := range httpResp.Header {
		if len(vv) > 0 {
			hdrs[strings.ToLower(k)] = vv[0]
		}
	}
	return httpResponse{
		Status:  httpResp.StatusCode,
		Headers: hdrs,
		BodyB64: base64.StdEncoding.EncodeToString(body),
	}
}

type proxyURLRequest struct {
	URI     string            `json:"uri"`
	Plugin  string            `json:"plugin"`
	Headers map[string]string `json:"headers"`
}

type proxyURLResponse struct {
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

func (r *Runtime) hostProxyURL(ctx context.Context, mod api.Module, stack []uint64) {
	reqPtr := uint32(stack[0])
	reqLen := uint32(stack[1])
	raw, _ := mod.Memory().Read(reqPtr, reqLen)

	var req proxyURLRequest
	resp := proxyURLResponse{}
	if r.module.perms != nil && !r.module.perms.allowProxy() {
		resp.Error = "permission denied: proxy not granted"
		body, _ := json.Marshal(resp)
		stack[0] = r.writeBack(ctx, body)
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		resp.Error = "bad request: " + err.Error()
	} else if strings.TrimSpace(req.URI) == "" {
		resp.Error = "uri required"
	} else if r.proxy == nil {
		// Proxy disabled — return raw URI; the plugin can detect this and
		// decide whether that's acceptable for the user.
		resp.URL = req.URI
	} else {
		plugin := req.Plugin
		if plugin == "" {
			plugin = r.module.Manifest.ID
		}
		var enc string
		if len(req.Headers) > 0 {
			enc = r.proxy.EncryptURIWithHeaders(req.URI, r.reqIP, plugin, req.Headers)
		} else {
			enc = r.proxy.EncryptURI(req.URI, r.reqIP, plugin, false, false, false)
		}
		if enc == "" {
			resp.URL = req.URI
		} else {
			resp.URL = r.host + "/proxy/" + enc
		}
	}
	body, _ := json.Marshal(resp)
	stack[0] = r.writeBack(ctx, body)
}

func (r *Runtime) hostCacheGet(ctx context.Context, mod api.Module, stack []uint64) {
	if r.module.perms != nil && !r.module.perms.allowCache() {
		stack[0] = 0
		return
	}
	keyPtr := uint32(stack[0])
	keyLen := uint32(stack[1])
	key, _ := mod.Memory().Read(keyPtr, keyLen)
	v, ok := r.cache.Get(string(key))
	if !ok {
		r.module.stats.CacheMisses.Add(1)
		stack[0] = 0
		return
	}
	r.module.stats.CacheHits.Add(1)
	// Cache stores raw bytes — return them verbatim. The plugin owns the
	// (de)serialization; the host doesn't reinterpret the value.
	body, ok := v.([]byte)
	if !ok {
		// Fall back to JSON marshalling for non-byte values (e.g. anything a
		// future caller may put through CacheSet from inside Go).
		var err error
		body, err = json.Marshal(v)
		if err != nil {
			stack[0] = 0
			return
		}
	}
	stack[0] = r.writeBack(ctx, body)
}

func (r *Runtime) hostCacheSet(ctx context.Context, mod api.Module, stack []uint64) {
	if r.module.perms != nil && !r.module.perms.allowCache() {
		return
	}
	keyPtr := uint32(stack[0])
	keyLen := uint32(stack[1])
	valPtr := uint32(stack[2])
	valLen := uint32(stack[3])
	ttl := uint32(stack[4])

	key, _ := mod.Memory().Read(keyPtr, keyLen)
	val, _ := mod.Memory().Read(valPtr, valLen)
	if ttl == 0 {
		ttl = 300
	}
	// Copy because Memory().Read returns a slice into the live linear-memory
	// buffer; without copying, a future guest write would corrupt the cache.
	stored := make([]byte, len(val))
	copy(stored, val)
	r.cache.Set(string(key), stored, time.Duration(ttl)*time.Second)
	r.module.stats.CacheSets.Add(1)
}

func (r *Runtime) hostConfig(ctx context.Context, _ api.Module, stack []uint64) {
	body := []byte(r.module.ConfigSnapshot())
	if len(body) == 0 {
		body = []byte("{}")
	}
	stack[0] = r.writeBack(ctx, body)
}

// writeBack writes b into guest memory via alloc() and returns the packed
// (ptr, len) i64. Used by host imports that produce a JSON response.
func (r *Runtime) writeBack(ctx context.Context, b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	ptr, err := r.copyToGuest(ctx, b)
	if err != nil {
		// Best-effort log and return zero; guests should treat 0 as "no data".
		if r.logger != nil {
			r.logger.Warn().Str("module", r.module.Manifest.ID).Err(err).Msg("wasm: writeBack failed")
		}
		return 0
	}
	return PackPtrLen(ptr, uint32(len(b)))
}

// logWriter pipes guest stdout/stderr into the per-module log sink so admin
// users can see panic traces and println output.
type logWriter struct {
	sink   *logSink
	level  string
	module string
}

func (lw *logWriter) Write(p []byte) (int, error) {
	if lw.sink != nil && len(p) > 0 {
		text := strings.TrimRight(string(p), "\n")
		if text != "" {
			lw.sink.Append(LogEntry{Module: lw.module, Level: lw.level, Time: time.Now(), Message: text})
		}
	}
	return len(p), nil
}

// Compile-time check: proxylink.Manager satisfies ProxyBuilder. Done here so
// `go vet` catches API drift if proxylink ever changes its signature.
var _ ProxyBuilder = (*proxylink.Manager)(nil)
