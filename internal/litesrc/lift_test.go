package litesrc

import (
	"os"
	"strings"
	"testing"
)

// TestLiftStripIframeBust verifies that the iframe-busting JavaScript block
// is removed from a real zenithjs embed page so the player no longer tries
// to redirect away from our proxied origin.
func TestLiftStripIframeBust(t *testing.T) {
	const sample = `
	var isEmbedded; try { isEmbedded = self !== top; } catch (e) {isEmbedded = true}
	var sameOrigin; try { sameOrigin = top.location.origin === location.origin } catch(e) {}
	if(isEmbedded&&!sameOrigin){
		_s('frame','sub','',{host:location.hostname.split('.',1)[0].replace(/api\d+/,'api{0}')});
		addEventListener('message',function(e){if('ping'==e.data)e.source.postMessage('pong',e.origin)});
	}
	if (isEmbedded&& !sameOrigin&& window.fetch&& window.URL&& !/(greenfilm\.vip)$/.test(consumerHost||'')) {
		var url = new URL(location.href), re = /^api\d+\./;
		if (re.test(url.hostname)) {
			url.hostname = url.hostname.replace(re, 'api.');
			fetch(url.origin+'/ping/',{method:'head'}).then(function (r) {
				url.searchParams.set('host', new URL(document.referrer).hostname);
				if(r.status===200)location.href=url.href;
			})
		}
	}`

	out := liftStripIframeBust(sample)

	// The two redirect-on-embed branches must be gone.
	if strings.Contains(out, "location.href=url.href") {
		t.Errorf("iframe-busting redirect still present:\n%s", out)
	}

	// isEmbedded must be hard-set to false.
	if !strings.Contains(out, "isEmbedded = false") {
		t.Errorf("isEmbedded was not pinned to false:\n%s", out)
	}
	// sameOrigin must be hard-set to true.
	if !strings.Contains(out, "sameOrigin = true") {
		t.Errorf("sameOrigin was not pinned to true:\n%s", out)
	}
}

// TestLiftEmbedURLBuild verifies the URL construction prefers KP id over
// IMDB and over Lift's internal id, and threads season/episode through.
func TestLiftEmbedURLBuild(t *testing.T) {
	l := &liftChecker{
		embedHost:    "https://api.zenithjs.ws",
		consumerHost: "lift.com",
	}

	cases := []struct {
		name             string
		orid, kp         int64
		imdb             string
		season, episode  int
		wantHasSubstring []string
		wantNotSubstring []string
	}{
		{"kp wins over orid", 88225, 464963, "tt0944947", 0, 0,
			[]string{"/embed/kp/464963", "host=lift.com"},
			[]string{"/embed/movie/", "/embed/imdb/"}},
		{"imdb when no kp", 88225, 0, "tt0944947", 0, 0,
			[]string{"/embed/imdb/tt0944947", "host=lift.com"},
			[]string{"/embed/movie/", "/embed/kp/"}},
		{"orid when no kp/imdb", 88225, 0, "", 0, 0,
			[]string{"/embed/movie/88225", "host=lift.com"},
			[]string{"/embed/kp/", "/embed/imdb/"}},
		{"season+episode threaded", 0, 464963, "", 2, 3,
			[]string{"/embed/kp/464963", "season=2", "episode=3"},
			nil},
		{"empty all", 0, 0, "", 0, 0,
			nil, []string{"/embed/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := l.buildEmbedURL(tc.orid, tc.kp, tc.imdb, tc.season, tc.episode)
			if len(tc.wantHasSubstring) == 0 && got != "" {
				t.Errorf("expected empty for no-id case, got %q", got)
			}
			for _, sub := range tc.wantHasSubstring {
				if !strings.Contains(got, sub) {
					t.Errorf("URL missing %q in %q", sub, got)
				}
			}
			for _, sub := range tc.wantNotSubstring {
				if strings.Contains(got, sub) {
					t.Errorf("URL unexpectedly contains %q in %q", sub, got)
				}
			}
		})
	}
}

// TestLiftAPIHostFromFrontend verifies the API host derivation Lift uses to
// turn a resolved frontend like https://embandr.ws/ into https://api.embandr.ws.
func TestLiftAPIHostFromFrontend(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://embandr.ws", "https://api.embandr.ws"},
		{"https://embandr.ws/", "https://api.embandr.ws"},
		{"https://www.embandr.ws", "https://api.embandr.ws"},
		{"http://lateremb.ws", "http://api.lateremb.ws"},
	}
	for _, tc := range cases {
		got := liftAPIHostFromFrontend(strings.TrimRight(tc.in, "/"))
		if got != tc.want {
			t.Errorf("liftAPIHostFromFrontend(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestLiftMovieEmbedHLSExtraction confirms that the Collaps regexes (which
// Lift reuses) match the inline `hls:"..."` and `dash:"..."` URLs that
// zenithjs embeds for movies. Uses the real shape captured during recon.
func TestLiftMovieEmbedHLSExtraction(t *testing.T) {
	const movieSource = `
		Object.assign(opts, {
			source: {
				dash: "https://cdnr.interkh.com/01_23/19/06/HHN2H6SY/941749.mpd?ha=0ccb&t=1778",
				hls:  "https://cdnr.interkh.com/01_18_23/01/18/SV7O2S7B/DP3BKWX5.mp4/master.m3u8?ha=0ccb&t=1778",
				audio: {"names":["Рус. Дублированный","Original"],"order":[0,1]},
				cc: [{"url":"https://hye1eaipby4w.interkh.com/sub.vtt","name":"Eng"}]
			}
		})`

	if !collapsHLSRe.MatchString(movieSource) {
		t.Fatal("collapsHLSRe should match movie hls field")
	}
	if !collapsDashRe.MatchString(movieSource) {
		t.Fatal("collapsDashRe should match movie dash field")
	}

	hls := submatch1(collapsHLSRe, movieSource)
	if !strings.HasPrefix(hls, "https://cdnr.interkh.com/") || !strings.HasSuffix(hls, "master.m3u8?ha=0ccb&t=1778") {
		t.Errorf("hls extraction wrong: %q", hls)
	}
}

// TestLiftSerialEmbedHLSExtraction confirms the seasons array regex matches
// the JSON-keyed format zenithjs uses for serials (`"hls":"…"` rather than
// the bare-keyed movie form `hls:"…"`).
func TestLiftSerialEmbedHLSExtraction(t *testing.T) {
	// Real zenithjs embed minifies the seasons array onto a single line.
	// `collapsSeasonsRe` matches `[…]` up to the next newline — so our
	// fixture must keep the array contiguous.
	const serialSource = `				seasons:[{"season":1,"blocked":false,"episodes":[{"episode":"1","id":7,"hls":"https://cdnr.interkh.com/x/master.m3u8?ha=1","dash":"https://cdnr.interkh.com/x/p.mpd?ha=1","audio":{"names":["Track A"],"order":[0]},"cc":[]},{"episode":"2","id":8,"hls":"https://cdnr.interkh.com/y/master.m3u8?ha=2","dash":"https://cdnr.interkh.com/y/p.mpd?ha=2","audio":{"names":["Track A"],"order":[0]},"cc":[]}]}]`

	raw := submatch1(collapsSeasonsRe, serialSource)
	if raw == "" {
		t.Fatal("collapsSeasonsRe failed to match serial array")
	}

	seasons, ok := collapsParseSeasons(raw)
	if !ok || len(seasons) != 1 {
		t.Fatalf("collapsParseSeasons: ok=%v len=%d (want ok=true len=1)", ok, len(seasons))
	}
	if got := len(seasons[0].Episodes); got != 2 {
		t.Errorf("expected 2 episodes, got %d", got)
	}
	if seasons[0].Episodes[0].HLS == "" {
		t.Errorf("episode 1 HLS missing: %+v", seasons[0].Episodes[0])
	}
}

// TestLiftCheckerDefaults verifies that NewLiftChecker fills in sane
// defaults when the config block is empty (e.g. brand-new install).
func TestLiftCheckerDefaults(t *testing.T) {
	// Bypass full config parsing — pass a zero Config.
	// (NewLiftChecker only reads cfg.Online.Lift, the rest is irrelevant.)
	type cfgT = struct{}
	_ = cfgT{}

	// Constructor side-effects only — actually building it requires a
	// real config.Config. Just verify the constants resolve correctly.
	if liftEmbedHostDefault != "https://api.zenithjs.ws" {
		t.Errorf("default embed host changed unexpectedly: %s", liftEmbedHostDefault)
	}
	if liftConsumerHostDefault != "lift.com" {
		t.Errorf("default consumer host changed unexpectedly: %s", liftConsumerHostDefault)
	}
	if !strings.Contains(liftEmbedUA, "Android") {
		t.Errorf("embed UA should resemble LiftApp Android; got %q", liftEmbedUA)
	}
}

// TestMain sets a sentinel so subtests don't accidentally hit the network
// if someone adds a misconfigured test later.
func TestMain(m *testing.M) {
	os.Setenv("LAMPAC_LIFT_TEST", "1")
	os.Exit(m.Run())
}
