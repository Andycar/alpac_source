package browser

import (
	"fmt"
	"sort"
	"sync"
)

// registry is the process-wide map of registered engines. Engines
// register themselves in init() functions that may be gated by build
// tags (e.g. engine_register_rod.go has //go:build !no_rod).
var (
	regMu    sync.RWMutex
	engines  = map[string]Engine{}
	defaultE string // name selected by SetDefault; empty falls back to "chromedp"
)

// Register adds an engine to the registry. Calling Register twice with
// the same name panics — the registration is intended to happen in
// init() and a duplicate indicates a build mis-configuration.
func Register(e Engine) {
	if e == nil {
		panic("browser: Register called with nil engine")
	}
	name := e.Name()
	if name == "" {
		panic("browser: engine returned empty Name()")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, exists := engines[name]; exists {
		panic("browser: engine " + name + " already registered")
	}
	engines[name] = e
}

// Get returns the engine registered under name, or ErrUnknownEngine if
// no such engine is compiled into this binary.
func Get(name string) (Engine, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	e, ok := engines[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEngine, name)
	}
	return e, nil
}

// List returns the sorted names of all registered engines.
func List() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(engines))
	for name := range engines {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SetDefault selects the engine returned by Default(). Pass "" to clear
// and fall back to "chromedp". The name is not validated against the
// registry (config may reference an engine that is not compiled in;
// Default() will then return the fallback).
func SetDefault(name string) {
	regMu.Lock()
	defaultE = name
	regMu.Unlock()
}

// Default returns the engine selected via SetDefault, falling back to
// "chromedp" and then to any registered engine. Returns nil only if no
// engine is registered at all.
func Default() Engine {
	regMu.RLock()
	defer regMu.RUnlock()
	if defaultE != "" {
		if e, ok := engines[defaultE]; ok {
			return e
		}
	}
	if e, ok := engines["chromedp"]; ok {
		return e
	}
	for _, e := range engines {
		return e
	}
	return nil
}

// balancerOverrides is the per-balancer engine map populated from
// config (BrowserPool.BalancerEngines).
var (
	overMu     sync.RWMutex
	overrides  = map[string]string{}
)

// SetBalancerOverrides replaces the entire balancer→engine map.
func SetBalancerOverrides(m map[string]string) {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		if k == "" || v == "" {
			continue
		}
		cp[k] = v
	}
	overMu.Lock()
	overrides = cp
	overMu.Unlock()
}

// ForBalancer returns the engine selected for the named balancer
// (mirage, kinobase, …). Resolution order: balancer override → global
// default → first registered engine. Never returns nil unless no
// engine is registered.
func ForBalancer(balancer string) Engine {
	overMu.RLock()
	override := overrides[balancer]
	overMu.RUnlock()
	if override != "" {
		regMu.RLock()
		e, ok := engines[override]
		regMu.RUnlock()
		if ok {
			return e
		}
	}
	return Default()
}
