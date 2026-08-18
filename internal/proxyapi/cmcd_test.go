package proxyapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/cmcd"
	"lampac-go/internal/config"
)

// The whole feature rests on two properties: we see the telemetry, and the CDN
// does not. The second one matters more — an extra query argument on a signed
// upstream URL is how a working stream turns into a 403.
func TestHandleProxyRecordsCMCDAndKeepsItOffUpstream(t *testing.T) {
	var upstreamQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write([]byte("segment-bytes"))
	}))
	defer upstream.Close()

	var got cmcd.Report
	var gotPlugin, gotUA string
	CMCDRecorder = func(plugin, ua string, rep cmcd.Report) {
		gotPlugin, gotUA, got = plugin, ua, rep
	}
	defer func() { CMCDRecorder = nil }()

	h := New(config.Config{}, nil)
	enc := url.PathEscape(upstream.URL + "/seg1.ts?token=signed")
	payload := url.QueryEscape(`bl=2100,br=800,bs,mtp=900,ot=v,sf=h,sid="s-1",tb=6000`)
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/proxy/"+enc+"?CMCD="+payload+"&pl=filmix", nil)
	req.Header.Set("User-Agent", "ExoPlayerLib/2.19")
	rec := httptest.NewRecorder()

	h.HandleProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !got.Any || got.SessionID != "s-1" || !got.Starvation || got.BufferLengthMS != 2100 {
		t.Errorf("report = %+v", got)
	}
	if gotPlugin != "filmix" {
		t.Errorf("plugin = %q, want filmix", gotPlugin)
	}
	if gotUA != "ExoPlayerLib/2.19" {
		t.Errorf("ua = %q", gotUA)
	}
	if strings.Contains(strings.ToLower(upstreamQuery), "cmcd") {
		t.Errorf("CMCD leaked upstream: %q", upstreamQuery)
	}
	if !strings.Contains(upstreamQuery, "token=signed") {
		t.Errorf("upstream lost its own query: %q", upstreamQuery)
	}
}

// Header mode is what Shaka and ExoPlayer default to, so the same rule has to
// hold there: collected by us, invisible to the CDN.
func TestHandleProxyKeepsCMCDHeadersOffUpstream(t *testing.T) {
	var upstreamHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders = r.Header.Clone()
		_, _ = w.Write([]byte("segment-bytes"))
	}))
	defer upstream.Close()

	var got cmcd.Report
	CMCDRecorder = func(_, _ string, rep cmcd.Report) { got = rep }
	defer func() { CMCDRecorder = nil }()

	h := New(config.Config{}, nil)
	enc := url.PathEscape(upstream.URL + "/seg1.ts")
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/"+enc, nil)
	req.Header.Set("CMCD-Request", "bl=1500,mtp=800")
	req.Header.Set("CMCD-Session", `sid="s-2",sf=h`)
	req.Header.Set("CMCD-Status", "bs")
	rec := httptest.NewRecorder()

	h.HandleProxy(rec, req)

	if !got.Any || got.SessionID != "s-2" || !got.Starvation {
		t.Errorf("report = %+v", got)
	}
	for k := range upstreamHeaders {
		if strings.HasPrefix(strings.ToLower(k), "cmcd-") {
			t.Errorf("CMCD header leaked upstream: %s", k)
		}
	}
}

func TestStripCMCDQueryLeavesEverythingElseAlone(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"a=1&b=2":                 "a=1&b=2",
		"CMCD=bl%3D1":             "",
		"a=1&CMCD=bl%3D1&b=2":     "a=1&b=2",
		"cmcd=x&keep=%2Fslash%2F": "keep=%2Fslash%2F",
		"hash=abc&e=1&CMCD=su":    "hash=abc&e=1",
	}
	for in, want := range cases {
		if got := stripCMCDQuery(in); got != want {
			t.Errorf("stripCMCDQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
