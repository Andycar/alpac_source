package httpapi

// Live E2E harness for the Wink source. Skipped unless WINK_LIVE=1. Needs a
// Russian residential egress (WINK_SOCKS, or direct from a residential IP) and,
// for premium, a Wink account. It reuses the device UID cached at
// {WINK_REPO}/database/wink_session.json so it never re-registers a device
// (itv/devices is rate-limited — 6000002).
//
// Phone+SMS is a two-run flow:
//
//	WINK_LIVE=1 WINK_REPO=$PWD WINK_PHONE=+7... WINK_ACTION=send  go test ./internal/httpapi -run TestWinkLive -v
//	# read the SMS, then within a couple minutes:
//	WINK_LIVE=1 WINK_REPO=$PWD WINK_PHONE=+7... WINK_ACTION=login WINK_SMS=1234 go test ./internal/httpapi -run TestWinkLive -v
//
// With no WINK_ACTION it just bootstraps an anonymous session and dumps channel
// clear/DRM stats.

import (
	"context"
	"os"
	"testing"
	"time"

	"lampac-go/internal/config"
)

func TestWinkLive(t *testing.T) {
	if os.Getenv("WINK_LIVE") != "1" {
		t.Skip("set WINK_LIVE=1 to run the live Wink harness")
	}

	repo := os.Getenv("WINK_REPO")
	if repo == "" {
		repo = "../.."
	}

	var cfg config.Config
	cfg.Compat.RepoRoot = repo
	cfg.Online.Wink = config.WinkSource{
		Enable:        true,
		TV:            true,
		DiscoveryHost: "https://itv.svc.iptv.rt.ru/api/v2",
		UserAgent:     "Wink/1.38.1 (Android 9; ANDROIDTV)",
		Platform:      "ANDROID",
		DeviceType:    "ANDROIDTV",
		DeviceModel:   "Nexus 9",
		SocksProxy:    os.Getenv("WINK_SOCKS"),
		Login:         os.Getenv("WINK_PHONE"),
		Password:      os.Getenv("WINK_PASSWORD"),
	}

	w := newWinkChecker(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := w.ensureSession(ctx); err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	t.Logf("bootstrap ok: device=%.8s session=%.8s authed=%v api=%s", w.deviceUID, w.sessionID, w.authed, w.apiBase)

	switch os.Getenv("WINK_ACTION") {
	case "send":
		data, err := w.sendSmsCode(ctx, w.src.Login)
		t.Logf("send_sms_code -> %s (err=%v)", string(data), err)
		return
	case "login":
		code := os.Getenv("WINK_SMS")
		if code == "" {
			t.Fatal("WINK_ACTION=login needs WINK_SMS=<code>")
		}
		sid, err := w.createUserSession(ctx, w.src.Login, code, "")
		if err != nil {
			t.Fatalf("createUserSession: %v", err)
		}
		w.mu.Lock()
		w.sessionID, w.authed = sid, true
		w.mu.Unlock()
		w.persist()
		t.Logf("LOGIN OK: authed session=%.8s", sid)
	}

	list, err := w.getChannels(ctx)
	if err != nil {
		t.Fatalf("getChannels: %v", err)
	}
	st := winkComputeStats(list)
	t.Logf("CHANNELS total=%d clear=%d drm_only=%d no_source=%d (authed=%v)", st.Total, st.Clear, st.DRMOnly, st.NoSource, w.authed)

	n := 0
	for i := range list.Items {
		if n >= 8 {
			break
		}
		c := &list.Items[i]
		if u := c.clearStream(); u != "" {
			t.Logf("CLEAR #%d %q usage=%s -> %.90s", c.Number, c.Name, c.UsageModel, u)
			n++
		}
	}
}
