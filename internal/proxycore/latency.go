package proxycore

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	probeInterval = 60 * time.Second
	probeTimeout  = 10 * time.Second
	probeURL      = "http://connectivitycheck.gstatic.com/generate_204"
	latencyWindow = 5 // number of samples for moving average
)

// latencyProber periodically checks proxy health and measures latency.
type latencyProber struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
}

// startProber launches background latency probes for all instances in the pool.
func (p *Pool) startProber() {
	p.prober.mu.Lock()
	defer p.prober.mu.Unlock()
	if p.prober.running {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.prober.cancel = cancel
	p.prober.running = true

	p.prober.wg.Add(1)
	go func() {
		defer p.prober.wg.Done()
		// Initial probe.
		p.probeAll()

		ticker := time.NewTicker(probeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.probeAll()
			}
		}
	}()
}

func (p *Pool) stopProber() {
	p.prober.mu.Lock()
	defer p.prober.mu.Unlock()
	if !p.prober.running {
		return
	}
	p.prober.cancel()
	p.prober.running = false
	p.prober.wg.Wait()
}

// probeAll checks all instances in parallel.
func (p *Pool) probeAll() {
	p.mu.RLock()
	instances := make([]*Instance, len(p.instances))
	copy(instances, p.instances)
	p.mu.RUnlock()

	var wg sync.WaitGroup
	for _, inst := range instances {
		wg.Add(1)
		go func(inst *Instance) {
			defer wg.Done()
			lat, err := probeInstance(inst)
			if err != nil {
				inst.updateLatency(0, true)
				log.Debug().Err(err).Str("id", inst.id).Msg("proxycore: probe failed")
			} else {
				inst.updateLatency(lat, false)
			}
		}(inst)
	}
	wg.Wait()
}

// probeInstance measures latency by making an HTTP request through the SOCKS5 proxy.
func probeInstance(inst *Instance) (time.Duration, error) {
	dialer, err := net.DialTimeout("tcp", inst.socksAddr, 2*time.Second)
	if err != nil {
		return 0, fmt.Errorf("socks connect: %w", err)
	}
	dialer.Close()

	// Use the SOCKS5 proxy for an HTTP request.
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return inst.dialer.DialProxy(ctx, addr)
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   probeTimeout,
	}
	defer client.CloseIdleConnections()

	start := time.Now()
	resp, err := client.Get(probeURL)
	elapsed := time.Since(start)

	if err != nil {
		return 0, err
	}
	resp.Body.Close()

	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return elapsed, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return elapsed, nil
}

// updateLatency updates the instance's latency stats.
func (inst *Instance) updateLatency(lat time.Duration, failed bool) {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	if failed {
		inst.latencyFail++
		return
	}

	inst.latencyLast = lat
	inst.latencySamples = append(inst.latencySamples, lat)
	if len(inst.latencySamples) > latencyWindow {
		inst.latencySamples = inst.latencySamples[1:]
	}

	// Compute average.
	var sum time.Duration
	for _, s := range inst.latencySamples {
		sum += s
	}
	inst.latencyAvg = sum / time.Duration(len(inst.latencySamples))
}
