package cmcd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParseQuery(t *testing.T) {
	// A realistic hls.js payload: quoted strings, a bare boolean, a token.
	raw := `bl=21300,br=3200,cid="tt0111161,s01e02",d=4004,mtp=25400,ot=v,sf=h,sid="sess-42",su,tb=6000`
	req := httptest.NewRequest(http.MethodGet, "/proxy/abc?CMCD="+url.QueryEscape(raw), nil)

	rep, ok := Parse(req)
	if !ok {
		t.Fatal("expected a report")
	}
	if rep.SessionID != "sess-42" {
		t.Errorf("sid = %q", rep.SessionID)
	}
	// The comma inside the quoted cid must survive: a split cid silently splits
	// one session's stats in two.
	if rep.ContentID != "tt0111161,s01e02" {
		t.Errorf("cid = %q", rep.ContentID)
	}
	if rep.BufferLengthMS != 21300 || rep.EncodedBitrateKbps != 3200 || rep.TopBitrateKbps != 6000 {
		t.Errorf("numbers: bl=%d br=%d tb=%d", rep.BufferLengthMS, rep.EncodedBitrateKbps, rep.TopBitrateKbps)
	}
	if rep.ThroughputKbps != 25400 || rep.ObjectDurationMS != 4004 {
		t.Errorf("mtp=%d d=%d", rep.ThroughputKbps, rep.ObjectDurationMS)
	}
	if rep.ObjectType != ObjVideo || rep.StreamingFormat != "h" {
		t.Errorf("ot=%q sf=%q", rep.ObjectType, rep.StreamingFormat)
	}
	if !rep.Startup {
		t.Error("su should be true when sent bare")
	}
	if rep.Starvation {
		t.Error("bs was not sent")
	}
}

func TestParseHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/proxy/abc", nil)
	req.Header.Set("CMCD-Object", "br=1200,ot=a,tb=6000")
	req.Header.Set("CMCD-Request", "bl=800,mtp=1500")
	req.Header.Set("CMCD-Session", `cid="movie",pr=1.5,sf=h,sid="s1",st=v`)
	req.Header.Set("CMCD-Status", "bs,rtp=15000")

	rep, ok := Parse(req)
	if !ok {
		t.Fatal("expected a report")
	}
	if !rep.Starvation {
		t.Error("bs should be true")
	}
	if rep.SessionID != "s1" || rep.StreamType != "v" || rep.PlaybackRate != 1.5 {
		t.Errorf("session keys: %+v", rep)
	}
	if rep.RequestedThroughput != 15000 || rep.BufferLengthMS != 800 {
		t.Errorf("rtp=%d bl=%d", rep.RequestedThroughput, rep.BufferLengthMS)
	}
}

func TestParseNoCMCD(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/proxy/abc?token=1", nil)
	if _, ok := Parse(req); ok {
		t.Error("a request without CMCD must not produce a report")
	}
}

func TestParseGarbageIsHarmless(t *testing.T) {
	// Unknown keys (CMCD v2 adds them constantly), a negative number, a value
	// where a number is expected: none of it may produce a report we'd act on.
	rep, ok := ParseString(`nope=1,bl=-5,br=abc,ec="x"`)
	if ok {
		t.Errorf("expected no usable keys, got %+v", rep)
	}
	if rep.BufferLengthMS != 0 || rep.EncodedBitrateKbps != 0 {
		t.Errorf("garbage leaked: %+v", rep)
	}
}

func TestParseEscapedQuotes(t *testing.T) {
	rep, ok := ParseString(`cid="say \"hi\"",sid="s"`)
	if !ok || rep.ContentID != `say "hi"` {
		t.Errorf("cid = %q ok=%v", rep.ContentID, ok)
	}
}

func TestPlatformFromUA(t *testing.T) {
	cases := map[string]string{
		"ExoPlayerLib/2.19":                         "android",
		"AppleCoreMedia/1.0.0.21L227 (Apple TV; U)": "apple",
		"Mozilla/5.0 (Web0S; Linux/SmartTV)":        "webos",
		"Mozilla/5.0 (SMART-TV; Linux; Tizen)":      "tizen",
		"Mozilla/5.0 (X11; Linux x86_64)":           "web",
		"":                                          "unknown",
	}
	for ua, want := range cases {
		if got := PlatformFromUA(ua); got != want {
			t.Errorf("PlatformFromUA(%q) = %q, want %q", ua, got, want)
		}
	}
}
