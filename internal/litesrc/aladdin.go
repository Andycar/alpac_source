package litesrc

import (
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
)

// NewAladdinChecker creates a MirageChecker configured for the Aladdin balancer.
// Aladdin uses the same API (apbugall.org), iframe, and chromedp browser flow
// as Mirage — only the CDN linkHost differs.
func NewAladdinChecker(cfg config.Config) *MirageChecker {
	apiHost := strings.TrimSpace(strings.TrimRight(cfg.Online.Aladdin.APIHost, "/"))
	if apiHost == "" {
		apiHost = "https://api.apbugall.org"
	}
	if !strings.Contains(apiHost, "://") {
		apiHost = "https://" + apiHost
	}

	linkHost := strings.TrimSpace(strings.TrimRight(cfg.Online.Aladdin.LinkHost, "/"))
	if linkHost == "" {
		linkHost = "https://quadrillion-as.allarknow.online"
	}
	if !strings.Contains(linkHost, "://") {
		linkHost = "https://" + linkHost
	}

	m := &MirageChecker{
		client:                httpclient.NewUTLS(14 * time.Second),
		defaultPlaylistClient: httpclient.NewForBalancer("aladdin", 15*time.Second),
		defaultSegmentClient:  httpclient.NewForBalancerNoTimeout("aladdin"),
		apiHost:               apiHost,
		linkHost:              linkHost,
		token:                 strings.TrimSpace(cfg.Online.Aladdin.Token),
		prefix:                "aladdin",
		kitName:               "Aladdin",
		streamCache:           make(map[int64]*mirageStreamEntry),
		inflightResolves:      make(map[int64]chan struct{}),
	}

	// Initialize Guard client for proof-of-browser tokens.
	getOrCreateGuardClient(linkHost, "aladdin")

	return m
}
