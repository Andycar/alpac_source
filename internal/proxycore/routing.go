package proxycore

import (
	"strings"
	"sync"
	"time"
)

// RoutingMode defines how traffic is routed to a proxy.
type RoutingMode string

const (
	RouteFixed      RoutingMode = "fixed"       // always use the specified proxy
	RouteLatency    RoutingMode = "latency"     // pick proxy with lowest latency
	RouteRoundRobin RoutingMode = "round-robin" // round-robin across proxies
)

// RoutingRule maps balancers to proxy instances.
type RoutingRule struct {
	ID        string      `json:"id" toml:"id"`
	Balancers []string    `json:"balancers" toml:"balancers"` // which balancers
	ProxyID   string      `json:"proxy" toml:"proxy"`         // primary proxy (ID or label)
	Fallback  string      `json:"fallback" toml:"fallback"`   // fallback proxy (ID or label)
	Mode      RoutingMode `json:"mode" toml:"mode"`           // fixed, latency, round-robin
}

// router manages routing rules and resolves balancer→proxy mappings.
type router struct {
	mu    sync.RWMutex
	rules []RoutingRule
	rrIdx map[string]int // round-robin counters per rule ID
}

func newRouter() *router {
	return &router{
		rrIdx: make(map[string]int),
	}
}

// setRules replaces all routing rules.
func (r *router) setRules(rules []RoutingRule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules = rules
	r.rrIdx = make(map[string]int)
}

// resolve finds the best proxy instance for a given balancer name.
// Returns the instance ID (or label) that should handle this balancer's traffic.
func (r *router) resolve(balancer string, instances []*Instance) *Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()

	balLower := strings.ToLower(balancer)

	for _, rule := range r.rules {
		if !matchBalancer(rule.Balancers, balLower) {
			continue
		}

		switch rule.Mode {
		case RouteLatency:
			return r.resolveLatency(rule, instances)
		case RouteRoundRobin:
			return r.resolveRoundRobin(rule, instances)
		default: // fixed
			return r.resolveFixed(rule, instances)
		}
	}

	return nil // no rule matched
}

func (r *router) resolveFixed(rule RoutingRule, instances []*Instance) *Instance {
	primary := findInstance(instances, rule.ProxyID)
	if primary != nil && primary.Alive() {
		return primary
	}
	if rule.Fallback != "" {
		return findInstance(instances, rule.Fallback)
	}
	return primary
}

func (r *router) resolveLatency(rule RoutingRule, instances []*Instance) *Instance {
	// Collect candidates.
	candidates := r.collectCandidates(rule, instances)
	if len(candidates) == 0 {
		return findInstance(instances, rule.ProxyID)
	}

	// Pick the one with lowest average latency.
	var best *Instance
	for _, inst := range candidates {
		inst.mu.Lock()
		avg := inst.latencyAvg
		inst.mu.Unlock()

		if !inst.Alive() {
			continue
		}
		if best == nil || (avg > 0 && avg < getAvgLatency(best)) {
			best = inst
		}
	}
	if best == nil {
		return findInstance(instances, rule.ProxyID)
	}
	return best
}

func (r *router) resolveRoundRobin(rule RoutingRule, instances []*Instance) *Instance {
	candidates := r.collectCandidates(rule, instances)
	alive := make([]*Instance, 0, len(candidates))
	for _, c := range candidates {
		if c.Alive() {
			alive = append(alive, c)
		}
	}
	if len(alive) == 0 {
		return findInstance(instances, rule.ProxyID)
	}

	idx := r.rrIdx[rule.ID]
	r.rrIdx[rule.ID] = (idx + 1) % len(alive)
	return alive[idx%len(alive)]
}

func (r *router) collectCandidates(rule RoutingRule, instances []*Instance) []*Instance {
	var result []*Instance
	primary := findInstance(instances, rule.ProxyID)
	if primary != nil {
		result = append(result, primary)
	}
	if rule.Fallback != "" {
		fb := findInstance(instances, rule.Fallback)
		if fb != nil {
			result = append(result, fb)
		}
	}
	return result
}

func findInstance(instances []*Instance, idOrLabel string) *Instance {
	lower := strings.ToLower(idOrLabel)
	for _, inst := range instances {
		if strings.ToLower(inst.id) == lower || strings.ToLower(inst.label) == lower {
			return inst
		}
	}
	return nil
}

func matchBalancer(balancers []string, name string) bool {
	for _, b := range balancers {
		if strings.ToLower(b) == name {
			return true
		}
	}
	return false
}

func getAvgLatency(inst *Instance) time.Duration {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.latencyAvg
}
