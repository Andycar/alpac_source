package wasmmodules

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// UpstreamResolver maps a balancer ID ("collaps", "kinotochka") to an
// http.Handler that produces its /lite/{id} JSON response. The httpapi
// package wires this to its DynamicRouteRegistry so middleware modules can
// transparently invoke other balancers — including JS modules and built-in
// Go ones.
type UpstreamResolver func(balancer string) (http.Handler, bool)

// SetUpstreamResolver wires the resolver. Safe to call multiple times.
func (m *Manager) SetUpstreamResolver(r UpstreamResolver) { m.upstreams = r }

// MiddlewareInvocation is the payload handed to a middleware plugin's
// handle(). It contains the original Lampa request plus the upstream results
// already gathered by the host. The plugin returns a single Response whose
// JSON body replaces what would have gone to the client.
type MiddlewareInvocation struct {
	Invocation
	Upstreams []UpstreamResult `json:"upstreams"`
}

// UpstreamResult is one balancer's contribution. Body is whatever the
// upstream returned (usually a JSON object with "data":[...]).
type UpstreamResult struct {
	Balancer string          `json:"balancer"`
	Status   int             `json:"status"`
	Body     json.RawMessage `json:"body"`
	Error    string          `json:"error,omitempty"`
}

// MiddlewareHandler returns an http.Handler that:
//  1. Calls each upstream balancer concurrently (or sequentially — see below).
//  2. Bundles their responses into a MiddlewareInvocation.
//  3. Hands the bundle to the guest's handle() for filtering/merging.
//  4. Writes the guest's response to the client.
//
// We call upstreams sequentially on purpose: Lampa-side balancers often share
// session state via cookies and concurrent calls have produced 403s on real
// CDNs (see Mirage CDN cooldown notes). 99% of middleware use cases have ≤3
// upstreams, so the latency loss is small.
func (m *Manager) MiddlewareHandler(id string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mod, ok := m.Get(id)
		if !ok {
			http.Error(w, fmt.Sprintf(`{"error":"middleware %q not installed"}`, id), http.StatusNotFound)
			return
		}
		if !mod.Manifest.IsMiddleware() {
			http.Error(w, `{"error":"not a middleware module"}`, http.StatusBadRequest)
			return
		}
		if !mod.Enabled {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{"rch":false,"error":"disabled"}`))
			return
		}
		if m.upstreams == nil {
			http.Error(w, `{"error":"no upstream resolver wired"}`, http.StatusInternalServerError)
			return
		}

		results := m.callUpstreams(r, mod.Manifest.Upstreams)

		qs := map[string]string{}
		for k, vs := range r.URL.Query() {
			if len(vs) > 0 {
				qs[k] = vs[0]
			}
		}
		hdr := map[string]string{}
		for k, vs := range r.Header {
			if len(vs) > 0 {
				hdr[strings.ToLower(k)] = vs[0]
			}
		}
		mw := MiddlewareInvocation{
			Invocation: Invocation{
				Query:       qs,
				Headers:     hdr,
				Host:        hostFromRequest(r),
				RequestIP:   clientIP(r),
				Path:        strings.TrimPrefix(r.URL.Path, "/lite/"),
				LifeMode:    equalish(r.URL.Query().Get("life")),
				Checksearch: equalish(r.URL.Query().Get("checksearch")),
				UserAgent:   r.Header.Get("User-Agent"),
				Config:      mod.ConfigSnapshot(),
			},
			Upstreams: results,
		}
		// Stuff the upstream array into Invocation.Config under a reserved
		// key so the existing Invoke() path can carry it without an ABI bump.
		// The TinyGo SDK looks for inv.config.__upstreams.
		injectedCfg := injectUpstreams(mw.Invocation.Config, results)
		mw.Invocation.Config = injectedCfg

		resp, err := m.Invoke(r.Context(), id, mw.Invocation)
		if err != nil {
			if m.Logger != nil {
				m.Logger.Warn().Str("middleware", id).Err(err).Msg("wasmmodules: middleware invoke error")
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}
		ct := resp.ContentType
		if ct == "" {
			ct = "application/json; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(resp.Body)
	})
}

// callUpstreams invokes each upstream balancer with the same query params
// the user sent, captures the JSON body, and returns one result per name. A
// "*" entry expands to every balancer the resolver knows; ideally only used
// in test plugins.
func (m *Manager) callUpstreams(r *http.Request, names []string) []UpstreamResult {
	expanded := names
	if len(names) == 1 && names[0] == "*" {
		// Wildcard isn't safe in production (would call every balancer per
		// request) — we leave the expansion to the caller's resolver to keep
		// the policy decision out of this package.
		expanded = []string{"*"}
	}
	out := make([]UpstreamResult, 0, len(expanded))
	for _, name := range expanded {
		h, ok := m.upstreams(name)
		if !ok {
			out = append(out, UpstreamResult{Balancer: name, Error: "no such balancer"})
			continue
		}
		body, status, err := captureUpstream(r, h, name)
		if err != nil {
			out = append(out, UpstreamResult{Balancer: name, Status: status, Error: err.Error()})
			continue
		}
		out = append(out, UpstreamResult{Balancer: name, Status: status, Body: body})
	}
	return out
}

// captureUpstream forwards the request to `h` (rewriting /lite/{mwID} →
// /lite/{name}) and captures the response body. Uses an internal
// httptest-style writer to avoid spinning up a full HTTP round trip.
func captureUpstream(r *http.Request, h http.Handler, name string) ([]byte, int, error) {
	rew := r.Clone(r.Context())
	// Rewrite the path so the upstream sees its own balancer ID.
	rew.URL = &url.URL{
		Path:     "/lite/" + name,
		RawQuery: r.URL.RawQuery,
	}
	rew.RequestURI = "" // not allowed in client requests
	rec := newRecorder()
	// Bound the upstream call so a stuck balancer doesn't pin the goroutine.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	h.ServeHTTP(rec, rew.WithContext(ctx))
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		return nil, rec.code, err
	}
	return body, rec.code, nil
}

// injectUpstreams merges the upstream array into a config JSON blob under
// the reserved key "__upstreams". Plugins read it via inv.Config.
func injectUpstreams(cfg json.RawMessage, results []UpstreamResult) json.RawMessage {
	merged := map[string]any{}
	if len(cfg) > 0 {
		_ = json.Unmarshal(cfg, &merged)
	}
	merged["__upstreams"] = results
	out, err := json.Marshal(merged)
	if err != nil {
		return cfg
	}
	return out
}
