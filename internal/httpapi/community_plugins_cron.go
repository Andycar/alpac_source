package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// CommunityUpdateCron periodically checks for community plugin updates.
type CommunityUpdateCron struct {
	registry   *CustomPluginRegistry
	catalogURL func() string // live config getter
	interval   func() int    // hours getter

	mu            sync.RWMutex
	pending       int
	lastCheck     time.Time
	lastError     string
	cachedCatalog *CatalogResponse
	cachedAt      time.Time

	stopOnce sync.Once
	stop     chan struct{}
}

// NewCommunityUpdateCron creates a new cron instance.
// catalogURL and interval are called at each tick for live config.
func NewCommunityUpdateCron(
	registry *CustomPluginRegistry,
	catalogURL func() string,
	interval func() int,
) *CommunityUpdateCron {
	return &CommunityUpdateCron{
		registry:   registry,
		catalogURL: catalogURL,
		interval:   interval,
		stop:       make(chan struct{}),
	}
}

// Start begins the background auto-update goroutine.
func (c *CommunityUpdateCron) Start(ctx context.Context) {
	go c.run(ctx)
}

func (c *CommunityUpdateCron) run(ctx context.Context) {
	// Check every 10 minutes whether it's time to run.
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case <-ticker.C:
			hours := c.interval()
			if hours <= 0 {
				continue
			}
			c.mu.RLock()
			last := c.lastCheck
			c.mu.RUnlock()

			if time.Since(last) < time.Duration(hours)*time.Hour {
				continue
			}
			_, _ = c.AutoUpdateNow()
		}
	}
}

// Stop halts the background goroutine.
func (c *CommunityUpdateCron) Stop() {
	c.stopOnce.Do(func() { close(c.stop) })
}

// PendingCount returns the number of available updates.
func (c *CommunityUpdateCron) PendingCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pending
}

// LastCheck returns the time of the last catalog check.
func (c *CommunityUpdateCron) LastCheck() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastCheck
}

// LastError returns the last error message, if any.
func (c *CommunityUpdateCron) LastError() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastError
}

// CachedCatalog returns the catalog if cached within maxAge, otherwise fetches fresh.
func (c *CommunityUpdateCron) CachedCatalog(maxAge time.Duration) (*CatalogResponse, error) {
	c.mu.RLock()
	if c.cachedCatalog != nil && time.Since(c.cachedAt) < maxAge {
		cat := c.cachedCatalog
		c.mu.RUnlock()
		return cat, nil
	}
	c.mu.RUnlock()

	url := c.catalogURL()
	if url == "" {
		return &CatalogResponse{}, nil
	}

	catalog, err := fetchCatalog(url)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cachedCatalog = catalog
	c.cachedAt = time.Now()
	c.mu.Unlock()

	return catalog, nil
}

// CheckNow fetches the catalog and counts pending updates without applying them.
func (c *CommunityUpdateCron) CheckNow() (int, error) {
	url := c.catalogURL()
	if url == "" {
		return 0, nil
	}

	catalog, err := fetchCatalog(url)
	if err != nil {
		c.mu.Lock()
		c.lastError = err.Error()
		c.lastCheck = time.Now()
		c.mu.Unlock()
		return 0, err
	}

	c.mu.Lock()
	c.cachedCatalog = catalog
	c.cachedAt = time.Now()
	c.lastCheck = time.Now()
	c.lastError = ""
	c.mu.Unlock()

	// Count pending updates.
	statuses := buildCommunityStatus(c.registry, catalog)
	pending := 0
	for _, s := range statuses {
		if s.Installed && s.UpdateAvailable {
			pending++
		}
	}

	c.mu.Lock()
	c.pending = pending
	c.mu.Unlock()

	return pending, nil
}

// AutoUpdateNow fetches catalog and updates all outdated community plugins.
func (c *CommunityUpdateCron) AutoUpdateNow() (int, error) {
	url := c.catalogURL()
	if url == "" {
		return 0, nil
	}

	catalog, err := fetchCatalog(url)
	if err != nil {
		c.mu.Lock()
		c.lastError = err.Error()
		c.lastCheck = time.Now()
		c.mu.Unlock()
		log.Warn().Err(err).Msg("community: auto-update catalog fetch failed")
		return 0, err
	}

	c.mu.Lock()
	c.cachedCatalog = catalog
	c.cachedAt = time.Now()
	c.lastCheck = time.Now()
	c.lastError = ""
	c.mu.Unlock()

	statuses := buildCommunityStatus(c.registry, catalog)
	updated := 0
	for _, s := range statuses {
		if !s.Installed || !s.UpdateAvailable {
			continue
		}
		ok, err := updateCommunityPlugin(c.registry, s.CatalogEntry)
		if err != nil {
			log.Warn().Err(err).Str("name", s.Name).Msg("community: auto-update failed")
			continue
		}
		if ok {
			updated++
		}
	}

	// Recount pending after updates.
	c.mu.Lock()
	c.pending = 0 // after update, assume all caught up
	c.mu.Unlock()

	if updated > 0 {
		log.Info().Int("updated", updated).Msg("community: auto-update completed")
	}
	return updated, nil
}
