package httpapi

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/rs/zerolog/log"
)

// DynamicRouteRegistry holds routes for custom balancer subprocesses
// that can be added/removed at runtime.
type DynamicRouteRegistry struct {
	mu     sync.RWMutex
	routes map[string]http.Handler // key: route name (e.g., "makhno")
}

// NewDynamicRouteRegistry creates an empty registry.
func NewDynamicRouteRegistry() *DynamicRouteRegistry {
	return &DynamicRouteRegistry{
		routes: make(map[string]http.Handler),
	}
}

// Register adds or replaces a route pointing to a subprocess at targetAddr.
// targetAddr is like "127.0.0.1:50001".
func (r *DynamicRouteRegistry) Register(name, targetAddr string) {
	target, err := url.Parse("http://" + targetAddr)
	if err != nil {
		log.Warn().Str("name", name).Str("addr", targetAddr).Err(err).Msg("dynroutes: bad target")
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		log.Debug().Str("name", name).Err(err).Msg("dynroutes: proxy error")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"rch":false}`))
	}

	r.mu.Lock()
	r.routes[name] = proxy
	r.mu.Unlock()
	log.Info().Str("name", name).Str("target", targetAddr).Msg("dynroutes: registered")
}

// RegisterHandler adds or replaces a route with a raw http.Handler. Used by
// in-process modules (e.g. the jsmodules package) that don't need a reverse
// proxy to a subprocess.
func (r *DynamicRouteRegistry) RegisterHandler(name string, h http.Handler) {
	if h == nil {
		return
	}
	r.mu.Lock()
	r.routes[name] = h
	r.mu.Unlock()
	log.Info().Str("name", name).Msg("dynroutes: handler registered")
}

// Unregister removes a route.
func (r *DynamicRouteRegistry) Unregister(name string) {
	r.mu.Lock()
	delete(r.routes, name)
	r.mu.Unlock()
}

// Lookup returns the handler for a route name, if registered.
func (r *DynamicRouteRegistry) Lookup(name string) (http.Handler, bool) {
	r.mu.RLock()
	h, ok := r.routes[name]
	r.mu.RUnlock()
	return h, ok
}

// Names returns all registered route names.
func (r *DynamicRouteRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.routes))
	for name := range r.routes {
		names = append(names, name)
	}
	return names
}
