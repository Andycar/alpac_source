package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"lampac-go/internal/config"
)

var lampaIndexWithFolderRe = regexp.MustCompile(`^[^/]+/[^./]+\.html$`)
var lampaHeadTagRe = regexp.MustCompile(`(?i)<head>`)
var lampaBodyCloseRe = regexp.MustCompile(`(?i)</body>`)

type lampaIndexSettings struct {
	Path    string
	Index   string
	BaseTag bool
}

func lampaIndexHandler(cfg config.Config) http.HandlerFunc {
	msxHandler := msxStartJSONHandler(cfg)
	return func(w http.ResponseWriter, r *http.Request) {
		// Use live config so telegram auth changes take effect immediately.
		cfg = liveConfig(cfg)

		// MSX (Media Station X) hits "/" expecting JSON.
		// Non-browser clients (no text/html in Accept) get MSX JSON.
		accept := r.Header.Get("Accept")
		if accept == "" || !strings.Contains(accept, "text/html") {
			msxHandler(w, r)
			return
		}

		settings := loadLampaIndexSettings(cfg.Compat.RepoRoot)
		index := strings.TrimSpace(settings.Index)
		if index == "" {
			index = detectDefaultLampaIndex(cfg.Compat.RepoRoot, settings)
		}
		if index == "" {
			writePlain(w, http.StatusOK, "api work")
			return
		}

		// Always serve index.html inline with <base href="/" /> so the app
		// loads from the root without a /lampa-main/ prefix in the URL.
		if lampaIndexWithFolderRe.MatchString(index) {
			html, ok := readWWWRootFile(cfg.Compat.RepoRoot, index)
			if ok {
				if lampaHeadTagRe.MatchString(html) {
					html = lampaHeadTagRe.ReplaceAllString(html, `<head><base href="/" />`)
				}
				// Inject lampainit.js for web users (native apps load it via lampa_url).
				if !strings.Contains(html, `"/lampainit.js"`) {
					html = lampaBodyCloseRe.ReplaceAllString(html, `<script src="/lampainit.js"></script></body>`)
				}
				// Inject inline auth gate script before </body>.
				// This runs on ALL platforms including Android apps that
				// intercept lampainit.js and serve their own local copy.
				if cfg.TelegramAuth.BotToken != "" {
					html = injectAuthGateInline(html, cfg)
				}
				html = applyPublicBrandingHTML(html, lampaIndexVariant(index))
				writeHTML(w, http.StatusOK, html)
				return
			}
		}

		writePlain(w, http.StatusOK, "api work")
	}
}

// injectAuthGateInline injects a lightweight inline <script> into the HTML.
// It does NOT show any UI itself — it only checks whether the user has a valid
// token (cookie OR localStorage) and, if not, redirects to /tg/auth as a
// full-page navigation. The heavy auth logic (fingerprint recovery, UID lookup)
// lives in on.js (buildAuthGateJS) which loads later.
//
// This avoids the old iframe-based gate that caused infinite auth loops because
// cookies set inside iframes don't always persist across navigations.
func injectAuthGateInline(html string, _ config.Config) string {
	script := `<script>
(function(){
  var LS_TOK='lampac_auth_token';
  function getToken(){
    try{var c=document.cookie.match(/(?:^|;\s*)lampac_token=([^;]*)/);if(c&&c[1])return decodeURIComponent(c[1]);}catch(e){}
    try{var v=localStorage.getItem(LS_TOK);if(v)return v;}catch(e){}
    return '';
  }
  function clearCookie(){
    try{
      document.cookie='lampac_token=;path=/;max-age=0';
      var d=location.hostname;
      document.cookie='lampac_token=;path=/;max-age=0;domain='+d;
      document.cookie='lampac_token=;path=/;max-age=0;domain=.'+d;
      var pts=d.split('.');if(pts.length>2)document.cookie='lampac_token=;path=/;max-age=0;domain=.'+pts.slice(-2).join('.');
    }catch(e){}
  }
  function saveToken(tok){
    if(!tok)return;
    try{document.cookie='lampac_token='+tok+';path=/;max-age=31536000;SameSite=Lax';}catch(e){}
    try{localStorage.setItem(LS_TOK,tok);}catch(e){}
  }
  var origin=window.location.origin||(window.location.protocol+'//'+window.location.host);
  var token=getToken();
  if(!token){
    // No token at all — let on.js handle recovery (UID/fingerprint).
    // Don't redirect here; if recovery also fails the user authorizes in
    // the sources window (accsdb QR card in online.js).
    return;
  }
  // Validate the token we found.
  var xhr=new XMLHttpRequest();
  xhr.open('GET',origin+'/tg/auth/status?token='+encodeURIComponent(token),true);
  xhr.timeout=8000;
  xhr.onload=function(){
    if(xhr.status===200){
      try{var r=JSON.parse(xhr.responseText);if(r&&r.authorized){saveToken(r.token||token);return;}}catch(e){}
    }
    // Token is invalid — clear it aggressively (all domain variants).
    clearCookie();
    try{localStorage.removeItem(LS_TOK);}catch(e){}
    // Check if localStorage had a DIFFERENT valid token (edge case: stale cookie
    // shadows a valid localStorage token).
    try{var ls=localStorage.getItem(LS_TOK);if(ls&&ls!==token){saveToken(ls);window.location.reload();return;}}catch(e){}
    // Don't redirect — let on.js gate handle UID/fingerprint recovery.
  };
  xhr.onerror=function(){};
  xhr.ontimeout=function(){};
  xhr.send();
})();
</script>`

	if lampaBodyCloseRe.MatchString(html) {
		return lampaBodyCloseRe.ReplaceAllString(html, script+`</body>`)
	}
	return html + script
}

func loadLampaIndexSettings(cfgRoot string) lampaIndexSettings {
	data, ok := readFileAny("init.conf")
	if !ok {
		data, _ = os.ReadFile(filepath.Join(cfgRoot, "init.conf"))
	}
	if len(data) == 0 {
		return lampaIndexSettings{}
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return lampaIndexSettings{}
	}

	node, ok := root["LampaWeb"].(map[string]any)
	if !ok {
		return lampaIndexSettings{}
	}
	return lampaIndexSettings{
		Path:    strings.TrimSpace(toString(node["path"])),
		Index:   strings.TrimSpace(toString(node["index"])),
		BaseTag: toBool(node["basetag"]),
	}
}

func detectDefaultLampaIndex(cfgRoot string, settings lampaIndexSettings) string {
	if path := sanitizeLampaType(settings.Path); path != "" {
		candidate := path + "/index.html"
		if _, ok := readWWWRootFile(cfgRoot, candidate); ok {
			return candidate
		}
	}

	candidates := []string{
		"lampa-main/index.html",
		"lampa-v2last/index.html",
		"lampa-lite/index.html",
	}
	for _, candidate := range candidates {
		if _, ok := readWWWRootFile(cfgRoot, candidate); ok {
			return candidate
		}
	}
	return ""
}

// lampaIndexVariant maps an index-html path to the branding variant used
// for HTML title substitution. lampa-lite gets its own short title;
// everything else (lampa-main, lampa-v2last, custom paths) uses the
// descriptive "v2" title.
func lampaIndexVariant(index string) string {
	if strings.Contains(index, "lampa-lite") {
		return "lite"
	}
	return "v2"
}

func readWWWRootFile(cfgRoot, rel string) (string, bool) {
	rel = filepath.Clean(strings.TrimPrefix(rel, "/"))
	candidates := []string{
		filepath.Join(cfgRoot, "wwwroot", rel),
		filepath.Join("wwwroot", rel),
		filepath.Join("/home/wwwroot", rel),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data), true
		}
	}
	return "", false
}
