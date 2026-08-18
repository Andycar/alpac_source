package balancerstats

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// AlertNotifier is implemented by anything that can deliver alerts to admins.
// In production this is *tgauth.Bot.NotifyAdmins; in tests it's a stub.
type AlertNotifier interface {
	NotifyAdmins(text string)
}

// AlertConfig controls when balancer alerts fire.
type AlertConfig struct {
	// Enabled toggles the whole alerting subsystem.
	Enabled bool

	// Window is the sliding window used to compute success rate (default 5m).
	Window time.Duration

	// MinAttempts: don't alert until we've seen at least this many attempts
	// in the window — avoids noise from one-off spikes (default 20).
	MinAttempts int

	// FailRateThreshold: success rate below this triggers an alert (default 0.30).
	FailRateThreshold float64

	// Cooldown between repeated alerts for the same balancer (default 30m).
	Cooldown time.Duration

	// CheckInterval: how often to evaluate windows (default 60s).
	CheckInterval time.Duration

	// RecoveryHysteresis: success rate must climb above this to clear the
	// "alerted" state and allow a "recovered" notification (default 0.70).
	RecoveryHysteresis float64
}

// DefaultAlertConfig returns sensible defaults.
func DefaultAlertConfig() AlertConfig {
	return AlertConfig{
		Enabled:            true,
		Window:             5 * time.Minute,
		MinAttempts:        20,
		FailRateThreshold:  0.30,
		Cooldown:           30 * time.Minute,
		CheckInterval:      60 * time.Second,
		RecoveryHysteresis: 0.70,
	}
}

// AlertEngine watches the manager and fires TG alerts on degradation.
type AlertEngine struct {
	cfg      AlertConfig
	mgr      *Manager
	notifier AlertNotifier

	mu          sync.Mutex
	alertedAt   map[string]time.Time // balancer → when alert was last sent
	alertActive map[string]bool      // balancer → currently in alerted state

	cancel context.CancelFunc
}

// NewAlertEngine wires the engine but does not start it; call Start.
func NewAlertEngine(cfg AlertConfig, mgr *Manager, notifier AlertNotifier) *AlertEngine {
	if cfg.Window == 0 {
		cfg.Window = 5 * time.Minute
	}
	if cfg.MinAttempts == 0 {
		cfg.MinAttempts = 20
	}
	if cfg.FailRateThreshold == 0 {
		cfg.FailRateThreshold = 0.30
	}
	if cfg.Cooldown == 0 {
		cfg.Cooldown = 30 * time.Minute
	}
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 60 * time.Second
	}
	if cfg.RecoveryHysteresis == 0 {
		cfg.RecoveryHysteresis = 0.70
	}
	return &AlertEngine{
		cfg:         cfg,
		mgr:         mgr,
		notifier:    notifier,
		alertedAt:   make(map[string]time.Time),
		alertActive: make(map[string]bool),
	}
}

// Start launches the background evaluation goroutine.
func (a *AlertEngine) Start(ctx context.Context) {
	if a == nil || !a.cfg.Enabled || a.notifier == nil {
		return
	}
	ctx, a.cancel = context.WithCancel(ctx)
	go a.loop(ctx)
	log.Info().Dur("interval", a.cfg.CheckInterval).Msg("balancerstats: alert engine started")
}

// Stop signals the background goroutine to exit.
func (a *AlertEngine) Stop() {
	if a == nil || a.cancel == nil {
		return
	}
	a.cancel()
}

func (a *AlertEngine) loop(ctx context.Context) {
	t := time.NewTicker(a.cfg.CheckInterval)
	defer t.Stop()
	// Run first check after a short warmup so we don't fire on cold-start.
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return
	}
	for {
		a.evaluate()
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

func (a *AlertEngine) evaluate() {
	snaps := a.mgr.Snapshot("")
	for name, snap := range snaps {
		// Pick the configured window (default 5m).
		windowKey := windowKeyFromDuration(a.cfg.Window)
		w, ok := snap.Windows[windowKey]
		if !ok || w.Total < a.cfg.MinAttempts {
			continue
		}

		a.mu.Lock()
		active := a.alertActive[name]
		lastAlert := a.alertedAt[name]
		a.mu.Unlock()

		if w.SuccessRate < a.cfg.FailRateThreshold {
			// Degraded.
			if !active && time.Since(lastAlert) >= a.cfg.Cooldown {
				a.mu.Lock()
				a.alertActive[name] = true
				a.alertedAt[name] = time.Now()
				a.mu.Unlock()
				a.fireDegraded(name, snap, w)
			}
		} else if w.SuccessRate >= a.cfg.RecoveryHysteresis && active {
			// Recovered.
			a.mu.Lock()
			a.alertActive[name] = false
			a.mu.Unlock()
			a.fireRecovered(name, w)
		}
	}
}

func (a *AlertEngine) fireDegraded(name string, snap Snapshot, w Window) {
	// Compose a compact, useful message.
	var b strings.Builder
	fmt.Fprintf(&b, "🚨 <b>Балансер деградирует</b>: <code>%s</code>\n", name)
	fmt.Fprintf(&b, "Успехов: <b>%.0f%%</b> (%d/%d за %s)\n",
		w.SuccessRate*100, w.Success, w.Total, humanDuration(a.cfg.Window))
	fmt.Fprintf(&b, "Latency p95: <b>%dms</b>\n", w.P95Latency)
	if snap.CurrentHost != "" {
		fmt.Fprintf(&b, "Текущий хост: <code>%s</code>\n", snap.CurrentHost)
	}
	if snap.LastError != "" {
		fmt.Fprintf(&b, "Последняя ошибка: <code>%s</code>", trimMessage(snap.LastError, 200))
	}
	a.notifier.NotifyAdmins(b.String())
	log.Warn().Str("balancer", name).
		Float64("success_rate", w.SuccessRate).
		Int("total", w.Total).
		Msg("balancerstats: alert fired (degraded)")
}

func (a *AlertEngine) fireRecovered(name string, w Window) {
	msg := fmt.Sprintf("✅ <b>Балансер восстановлен</b>: <code>%s</code>\nУспехов: <b>%.0f%%</b> (%d/%d)",
		name, w.SuccessRate*100, w.Success, w.Total)
	a.notifier.NotifyAdmins(msg)
	log.Info().Str("balancer", name).Float64("success_rate", w.SuccessRate).Msg("balancerstats: balancer recovered")
}

// windowKeyFromDuration maps a duration to the snapshot key used by Snapshot().
func windowKeyFromDuration(d time.Duration) string {
	switch d {
	case time.Minute:
		return "1m"
	case 15 * time.Minute:
		return "15m"
	case time.Hour:
		return "1h"
	default:
		return "5m"
	}
}

func humanDuration(d time.Duration) string {
	switch d {
	case time.Minute:
		return "1 мин"
	case 5 * time.Minute:
		return "5 мин"
	case 15 * time.Minute:
		return "15 мин"
	case time.Hour:
		return "1 час"
	default:
		return d.String()
	}
}

func trimMessage(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
