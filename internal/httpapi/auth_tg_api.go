package httpapi

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
	"lampac-go/internal/geoip"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// getAuthPageStyle reads auth page customization from the live config.
func getAuthPageStyle() config.AuthPageStyle {
	s := config.AuthPageStyle{}
	root := loadMergedConf()
	if tg, ok := root["TelegramAuth"].(map[string]any); ok {
		if ap, ok := tg["auth_page"].(map[string]any); ok {
			if v, ok := ap["title"].(string); ok {
				s.Title = v
			}
			if v, ok := ap["subtitle"].(string); ok {
				s.Subtitle = v
			}
			if v, ok := ap["bg_color"].(string); ok {
				s.BgColor = v
			}
			if v, ok := ap["card_color"].(string); ok {
				s.CardColor = v
			}
			if v, ok := ap["accent_color"].(string); ok {
				s.AccentColor = v
			}
			if v, ok := ap["button_color"].(string); ok {
				s.ButtonColor = v
			}
			if v, ok := ap["text_color"].(string); ok {
				s.TextColor = v
			}
			if v, ok := ap["logo_url"].(string); ok {
				s.LogoURL = v
			}
			if v, ok := ap["bg_image_url"].(string); ok {
				s.BgImageURL = v
			}
			if v, ok := ap["custom_css"].(string); ok {
				s.CustomCSS = v
			}
			if v, ok := ap["block_rows"].([]any); ok {
				for _, row := range v {
					if rowArr, ok := row.([]any); ok {
						var ids []string
						for _, item := range rowArr {
							if str, ok := item.(string); ok {
								ids = append(ids, str)
							}
						}
						if len(ids) > 0 {
							s.BlockRows = append(s.BlockRows, ids)
						}
					}
				}
			}
			if v, ok := ap["block_styles"].(map[string]any); ok {
				s.BlockStyles = make(map[string]string, len(v))
				for k, val := range v {
					if str, ok := val.(string); ok {
						s.BlockStyles[k] = str
					}
				}
			}
		}
	}
	// Fill defaults
	if s.Title == "" {
		s.Title = "Авторизация"
	}
	if s.Subtitle == "" {
		s.Subtitle = "Для доступа необходимо подтверждение"
	}
	if s.BgColor == "" {
		s.BgColor = "#1a1a2e"
	}
	if s.CardColor == "" {
		s.CardColor = "#16213e"
	}
	if s.AccentColor == "" {
		s.AccentColor = "#64ffda"
	}
	if s.ButtonColor == "" {
		s.ButtonColor = "#0088cc"
	}
	if s.TextColor == "" {
		s.TextColor = "#e0e0e0"
	}
	if len(s.BlockRows) == 0 {
		s.BlockRows = [][]string{{"logo"}, {"title"}, {"subtitle"}, {"code", "qr"}, {"steps"}, {"button"}, {"status"}, {"promo"}}
	}
	return s
}

// renderBlockRows assembles HTML blocks into rows. Blocks in the same row
// are rendered side-by-side using flexbox. blockStyles applies per-block inline CSS.
// Recognizes dynamic block IDs: spacer_N (empty space) and divider_N (horizontal line).
func renderBlockRows(rows [][]string, blocks map[string]string, blockStyles map[string]string) string {
	var out strings.Builder
	for _, row := range rows {
		type blockPart struct {
			id   string
			html string
		}
		var parts []blockPart
		for _, bid := range row {
			if h, ok := blocks[bid]; ok && h != "" {
				parts = append(parts, blockPart{bid, h})
			} else if strings.HasPrefix(bid, "spacer") {
				parts = append(parts, blockPart{bid, `<div style="height:20px"></div>`})
			} else if strings.HasPrefix(bid, "divider") {
				parts = append(parts, blockPart{bid, `<hr style="border:none;border-top:1px solid rgba(255,255,255,0.12);margin:6px 0;width:100%">`})
			}
		}
		if len(parts) == 0 {
			continue
		}
		wrapBlock := func(bp blockPart) string {
			st := blockStyles[bp.id]
			if st == "" {
				return bp.html
			}
			return fmt.Sprintf(`<div style="%s">%s</div>`, st, bp.html)
		}
		if len(parts) == 1 {
			out.WriteString(wrapBlock(parts[0]))
			out.WriteByte('\n')
		} else {
			out.WriteString(`<div style="display:flex;align-items:center;justify-content:center;gap:20px;flex-wrap:wrap">`)
			for _, p := range parts {
				out.WriteString(`<div style="flex:1;min-width:0">`)
				out.WriteString(wrapBlock(p))
				out.WriteString(`</div>`)
			}
			out.WriteString("</div>\n")
		}
	}
	return out.String()
}

// tgAuthCodeHandler returns a JSON object with a fresh auth code.
// Used by the inline gate JS in Lampa apps (WebView blocks XHR to HTML pages
// but allows JSON API calls).
func tgAuthCodeHandler(pending *tgauth.PendingStore, botName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := pending.Create(clientIP(r))
		log.Info().Str("code", req.Code).Str("ip", clientIP(r)).Msg("tgauth: auth code created (JSON)")
		writeJSON(w, http.StatusOK, map[string]any{
			"code":      req.Code,
			"bot_name":  botName,
			"deep_link": fmt.Sprintf("https://telegram.me/%s?start=%s", botName, req.Code),
		})
	}
}

// tgAuthPageHandler serves the authorization page with a unique code.
func tgAuthPageHandler(pending *tgauth.PendingStore, botName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Alternative auth modes: route the user to the correct surface
		// before we burn a pending TG code on someone who's going to log
		// in with a password.
		mode := currentAuthMode()
		switch mode {
		case config.AuthModeNone:
			// Open access — auto-issue an anon cookie and bounce home.
			if issuer := globalAnonIssuer; issuer != nil {
				token, _, _ := issuer.Issue(clientIP(r), r.Header.Get("User-Agent"))
				setAuthCookies(w, r, token)
			}
			http.Redirect(w, r, "/", http.StatusFound)
			return
		case config.AuthModePassword:
			renderPasswordAuthPage(w, r)
			return
		}

		req := pending.Create(clientIP(r))
		log.Info().Str("code", req.Code).Str("ip", clientIP(r)).Msg("tgauth: auth page created code")
		s := getAuthPageStyle()

		deepLink := fmt.Sprintf("https://telegram.me/%s?start=%s", botName, req.Code)

		bgImageCSS := ""
		if s.BgImageURL != "" {
			bgImageCSS = fmt.Sprintf("background-image:url('%s');background-size:cover;background-position:center;", s.BgImageURL)
		}
		customStyleTag := ""
		if s.CustomCSS != "" {
			customStyleTag = "<style>" + s.CustomCSS + "</style>"
		}

		logoHTML := ""
		if s.LogoURL != "" {
			logoHTML = fmt.Sprintf(`<img src="%s" alt="Logo" style="max-width:120px;max-height:80px;margin-bottom:16px;border-radius:8px">`, s.LogoURL)
		}
		qrBg := strings.TrimPrefix(s.CardColor, "#")
		qrFg := strings.TrimPrefix(s.AccentColor, "#")

		blocks := map[string]string{
			"logo":     logoHTML,
			"title":    fmt.Sprintf(`<h1>%s</h1>`, s.Title),
			"subtitle": fmt.Sprintf(`<p class="subtitle">%s</p>`, s.Subtitle),
			"code":     fmt.Sprintf(`<div class="code" id="code">%s</div>`, req.Code),
			"steps": fmt.Sprintf(`<ol class="steps">
    <li>Откройте бота <b>@%s</b> или нажмите кнопку ниже</li>
    <li>Отправьте код <b>%s</b> боту</li>
    <li>Дождитесь одобрения администратора</li>
  </ol>`, botName, req.Code),
			"button": fmt.Sprintf(`<a class="tg-link" href="%s" target="_blank">Открыть Telegram бота</a>`, deepLink),
			"qr": fmt.Sprintf(`<div id="qr"></div>
  <p style="opacity:0.5;font-size:12px;margin-top:8px">Отсканируйте QR</p>
<script>
(function(){
var dl="%s";
var img=document.createElement('img');
img.src='https://api.qrserver.com/v1/create-qr-code/?size=200x200&data='+encodeURIComponent(dl)+'&bgcolor=%s&color=%s&format=svg';
img.width=200;img.height=200;img.style.borderRadius='8px';
img.alt='QR';
img.onerror=function(){this.style.display='none'};
document.getElementById('qr').appendChild(img);
})();
</script>`, deepLink, qrBg, qrFg),
			"status": `<div class="status waiting" id="status">Ожидание отправки кода...</div>`,
			"promo": `<div style="margin-top:24px;border-top:1px solid rgba(255,255,255,0.12);padding-top:20px">
  <p style="opacity:0.6;font-size:13px;margin-bottom:10px">Или введите промокод</p>
  <div style="display:flex;gap:8px;justify-content:center">
    <input id="promo-input" type="text" placeholder="P-XXXXXX" maxlength="12"
      style="background:rgba(255,255,255,0.08);border:1px solid rgba(255,255,255,0.2);border-radius:8px;padding:10px 14px;color:#fff;font-size:16px;letter-spacing:2px;text-transform:uppercase;width:160px;text-align:center;outline:none;font-family:'Courier New',monospace">
    <button id="promo-btn" onclick="redeemPromo()"
      style="background:#10b981;color:#fff;border:none;border-radius:8px;padding:10px 20px;font-size:14px;font-weight:600;cursor:pointer;white-space:nowrap">Активировать</button>
  </div>
  <div id="promo-status" style="margin-top:10px;font-size:13px"></div>
</div>
<script>
function redeemPromo(){
  var inp=document.getElementById('promo-input');
  var code=inp.value.trim();
  if(!code)return;
  var btn=document.getElementById('promo-btn');
  var st=document.getElementById('promo-status');
  btn.disabled=true;btn.textContent='...';
  fetch('/tg/auth/promo',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:code})})
    .then(function(r){return r.json()})
    .then(function(d){
      if(d.ok&&d.token){
        st.style.color='#64ffda';st.textContent='Доступ активирован на '+d.days+' дн.!';
        try{var sas=(location.protocol==='https:'?';SameSite=Lax;Secure':';SameSite=Lax');document.cookie='lampac_token='+d.token+';path=/;max-age=31536000'+sas;document.cookie='alpac_token='+d.token+';path=/;max-age=31536000'+sas;}catch(e){}
        try{localStorage.setItem('lampac_auth_token',d.token);}catch(e){}
        setTimeout(function(){location.href='/'},1500);
      }else{
        st.style.color='#ff6b6b';st.textContent='Недействительный промокод';
        btn.disabled=false;btn.textContent='Активировать';
      }
    })
    .catch(function(){
      st.style.color='#ff6b6b';st.textContent='Ошибка сети';
      btn.disabled=false;btn.textContent='Активировать';
    });
}
document.getElementById('promo-input').addEventListener('keydown',function(e){if(e.key==='Enter')redeemPromo()});
</script>`,
		}

		cardContent := renderBlockRows(s.BlockRows, blocks, s.BlockStyles)

		html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  background: %s; color: %s;
  display: flex; justify-content: center; align-items: center;
  min-height: 100vh; %s
}
.card {
  background: %s; border-radius: 16px; padding: 40px;
  max-width: 480px; width: 90%%; text-align: center;
  box-shadow: 0 8px 32px rgba(0,0,0,0.3);
}
h1 { font-size: 24px; margin-bottom: 12px; color: #fff; }
.subtitle { color: %s; opacity: 0.7; margin-bottom: 24px; font-size: 14px; }
.code {
  font-size: 48px; font-weight: 700; letter-spacing: 8px;
  color: %s; margin: 20px 0; font-family: 'Courier New', monospace;
}
.steps { text-align: left; margin: 20px 0; }
.steps li { margin: 8px 0; color: %s; font-size: 14px; line-height: 1.6; }
.tg-link {
  display: inline-block; margin-top: 16px; padding: 12px 32px;
  background: %s; color: #fff; text-decoration: none;
  border-radius: 8px; font-size: 16px; font-weight: 500;
  transition: background 0.2s;
}
.tg-link:hover { opacity: 0.85; }
.status {
  margin-top: 20px; padding: 12px; border-radius: 8px;
  font-size: 14px; background: %s;
}
.status.waiting { color: %s; opacity: 0.7; }
.status.claimed { color: #f0c040; }
.status.approved { color: %s; }
.status.rejected { color: #ff6b6b; }
</style>
%s
</head>
<body>
<div class="card">
%s
</div>
<script>
(function() {
  var code = "%s";
  var statusEl = document.getElementById("status");
  if (!statusEl) return;
  var msgs = {
    "waiting": "Ожидание отправки кода...",
    "claimed": "Код принят. Ожидание одобрения администратора...",
    "approved": "Доступ одобрен! Перенаправление...",
    "rejected": "Запрос отклонён администратором."
  };
  function poll() {
    var x;
    try { x = new XMLHttpRequest(); } catch (e) { setTimeout(poll, 5000); return; }
    x.open("GET", "/tg/auth/check?code=" + code, true);
    x.onreadystatechange = function() {
      if (x.readyState !== 4) return; // old webOS/Tizen (Chrome 38) have NO fetch → XHR, ES5 (login was impossible there)
      if (x.status < 200 || x.status >= 300) { setTimeout(poll, 5000); return; }
      var data;
      try { data = JSON.parse(x.responseText); } catch (e) { setTimeout(poll, 5000); return; }
        statusEl.className = "status " + data.status;
        statusEl.textContent = msgs[data.status] || data.status;
        if (data.status === "approved" && data.token) {
          // SameSite tuning: cross-origin Lampa installs (lampa.mx player
          // hitting beta.example.com lampac) need SameSite=None so the
          // cookie is sent on subsequent XHRs. None mandates Secure, so
          // we only use it on HTTPS; HTTP installs fall back to Lax.
          var sas = (location.protocol === 'https:' ? "; SameSite=Lax; Secure" : "; SameSite=Lax");
          // Save token — gate JS checks localStorage on page load.
          // Dual cookie: lampac_token (legacy/external) + alpac_token
          // (our brand-scoped name) so third-party plugins that reset
          // lampac_token can't sign the user out.
          try {
            document.cookie = "lampac_token=" + data.token + "; path=/; max-age=31536000" + sas;
            document.cookie = "alpac_token="  + data.token + "; path=/; max-age=31536000" + sas;
          } catch(e) {}
          // Also try clearing old cookies with domain variants
          try {
            var d = location.hostname;
            document.cookie = "lampac_token=; path=/; max-age=0; domain=" + d;
            document.cookie = "lampac_token=; path=/; max-age=0; domain=." + d;
            document.cookie = "alpac_token=; path=/; max-age=0; domain=" + d;
            document.cookie = "alpac_token=; path=/; max-age=0; domain=." + d;
            var pts = d.split("."); if (pts.length > 2) {
              document.cookie = "lampac_token=; path=/; max-age=0; domain=." + pts.slice(-2).join(".");
              document.cookie = "alpac_token=; path=/; max-age=0; domain=." + pts.slice(-2).join(".");
            }
            // Re-set without domain (host-only)
            document.cookie = "lampac_token=" + data.token + "; path=/; max-age=31536000" + sas;
            document.cookie = "alpac_token="  + data.token + "; path=/; max-age=31536000" + sas;
          } catch(e) {}
          try { localStorage.setItem("lampac_auth_token", data.token); } catch(e) {}
          // Hand off to /tg/auth/complete: its 302 sets the auth cookies as a
          // first-party NAVIGATION response — the one cookie-delivery path that old
          // TV WebViews persist reliably. The fetch-response Set-Cookie and the
          // document.cookie writes above are silently dropped by some TV Chromium,
          // so approval "succeeds" yet the app stays logged out. Manual link + meta
          // refresh are fallbacks if the JS navigation is blocked.
          var done = "/tg/auth/complete?token=" + encodeURIComponent(data.token);
          var card = document.querySelector(".card");
          if (card) {
            card.innerHTML = '<h1 style="color:#64ffda;margin-bottom:20px">&#10003; Доступ одобрен!</h1>' +
              '<a href="' + done + '" style="display:inline-block;padding:16px 48px;background:#0088cc;color:#fff;text-decoration:none;border-radius:8px;font-size:18px;font-weight:500">Открыть приложение</a>' +
              '<p style="margin-top:16px;opacity:0.5;font-size:13px">Перенаправление...</p>';
          }
          var meta = document.createElement("meta");
          meta.httpEquiv = "refresh";
          meta.content = "1;url=" + done; // browser-level fallback
          document.head.appendChild(meta);
          location.replace(done); // immediate, reliable cookie-setting navigation
          return;
        }
        if (data.status !== "rejected" && data.status !== "expired") {
          setTimeout(poll, 3000);
        }
    };
    x.send();
  }
  setTimeout(poll, 2000);
})();
</script>
</body>
</html>`,
			s.Title,                            // <title>
			s.BgColor, s.TextColor, bgImageCSS, // body
			s.CardColor,    // .card
			s.TextColor,    // .subtitle
			s.AccentColor,  // .code
			s.TextColor,    // .steps li
			s.ButtonColor,  // .tg-link
			s.BgColor,      // .status bg
			s.TextColor,    // .status.waiting
			s.AccentColor,  // .status.approved
			customStyleTag, // custom CSS
			cardContent,    // ordered block rows
			req.Code,       // JS poll code
		)

		writeHTML(w, http.StatusOK, html)
	}
}

// tgAuthCheckHandler returns the status of a pending auth code as JSON.
// When the code is approved, consume it immediately so that FindByClientIP
// on the gate middleware cannot hand the same token to another client on
// the same IP (the root cause of the "x2 device binding" bug).
func tgAuthCheckHandler(pending *tgauth.PendingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "missing code"})
			return
		}

		req, ok := pending.FindByCode(code)
		if !ok {
			log.Debug().Str("code", code).Msg("tgauth: check poll — code expired/consumed")
			writeJSON(w, http.StatusOK, map[string]string{"status": "expired"})
			return
		}

		if req.Status != tgauth.StatusWaiting {
			log.Info().Str("code", code).Str("status", req.Status).Msg("tgauth: check poll — non-waiting status")
		}

		resp := map[string]string{"status": req.Status}
		if req.Status == tgauth.StatusApproved && req.Token != "" {
			resp["token"] = req.Token
			// Set HttpOnly cookie server-side — tamper-proof against malicious plugins.
			setAuthCookies(w, r, req.Token)
			log.Info().Str("code", code).Str("token_prefix", req.Token[:8]).Msg("tgauth: check returning approved+token")
			// Consume the pending so it cannot be reused by FindByClientIP
			// for a different device on the same IP/NAT.
			pending.Consume(code)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// tgAuthCheckJSHandler is the JSONP variant of tgAuthCheckHandler.
// Returns `window.__authPoll({status:"...",token:"..."})` as application/javascript.
// Used by Lampa WebView where XHR is blocked but <script src> works.
func tgAuthCheckJSHandler(pending *tgauth.PendingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`window.__authPoll({"status":"error"});`))
			return
		}

		status := "expired"
		token := ""

		if req, ok := pending.FindByCode(code); ok {
			status = req.Status
			if req.Status == tgauth.StatusApproved && req.Token != "" {
				token = req.Token
				setAuthCookies(w, r, req.Token)
				log.Info().Str("code", code).Str("token_prefix", req.Token[:8]).Msg("tgauth: check.js returning approved+token")
				pending.Consume(code)
			}
		}

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if token != "" {
			fmt.Fprintf(w, `window.__authPoll({"status":"%s","token":"%s"});`, status, token)
		} else {
			fmt.Fprintf(w, `window.__authPoll({"status":"%s"});`, status)
		}
	}
}

// tgAuthStatusHandler returns auth status for Lampa app (called from lampainit.js).
// For authorized users: {"authorized": true}
// For unauthorized: {"authorized": false, "code": "ABC123", "bot": "@botname"}
// The endpoint creates a pending code if none exists for the given uid/ip.
func tgAuthStatusHandler(store *tgauth.Store, pending *tgauth.PendingStore, botName string, cubVal *cubAuthValidator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := strings.TrimSpace(r.URL.Query().Get("uid"))
		fp := strings.TrimSpace(r.URL.Query().Get("fp"))
		sfp := strings.TrimSpace(r.URL.Query().Get("sfp"))
		cub := strings.TrimSpace(r.URL.Query().Get("cub"))
		// Native clients name themselves: their UA is an HTTP library string
		// ("okhttp/4.12.0"), so deriving a label from it would list every phone
		// and every box under the same meaningless name.
		label := strings.TrimSpace(r.URL.Query().Get("label"))
		uidConflict := false

		// learnCub passively links the presented CUB token to an account we
		// just authorized — builds the CUB recovery anchor with zero user
		// action (see cubAuthValidator).
		learnCub := func(ourToken string) {
			if cub != "" && cubVal != nil {
				cubVal.LearnAsync(store, ourToken, cub, clientIP(r))
			}
		}

		// detectCrossTokenConflict reports whether the given uid is bound to a
		// device on a different token (cub-backup clone case). The auth-gate JS
		// uses this signal to regenerate its UID even when its own cookie is
		// still valid, so the device record is kept consistent.
		detectCrossTokenConflict := func(currentToken string) bool {
			if uid == "" || currentToken == "" {
				return false
			}
			otherTok, _, ok := store.FindDeviceByUID(uid)
			return ok && otherTok != "" && otherTok != currentToken
		}

		// bindDeviceIfNew attaches this device to an already-valid token. The
		// authorized rungs (cookie/query-token) previously never bound —
		// invisible on our own domain where cookies cover every request, but
		// fatal on external Lampa hosts (lampa.mx): cookies don't travel
		// cross-site, /lite/* resolves the user by ?uid= alone, and an
		// unbound uid keeps the "authorize" banner up despite a valid token.
		//
		// Returns false when the account's device limit is full and the device
		// was therefore NOT bound: this rung used to bind unconditionally, which
		// is how one token ended up with 442 devices under a limit of 3.
		bindDeviceIfNew := func(tok string) bool {
			if uid == "" || store.HasDevice(tok, uid) {
				return true
			}
			if limit := effectiveDeviceLimit(store, tok, tgServerMaxDevices); limit > 0 && store.DeviceCount(tok) >= limit {
				log.Info().Str("uid", uid).Int("limit", limit).Str("ip", clientIP(r)).
					Msg("tgauth: status — device limit reached, not binding")
				return false
			}
			now := time.Now().UTC()
			_, _ = store.AddDevice(tok, tgauth.DeviceInfo{
				UID: uid, Fingerprint: fp, StableFP: sfp,
				Label:   deviceLabel(label, r.UserAgent()),
				BoundAt: now, LastSeen: now, LastIP: clientIP(r),
			})
			return true
		}

		// recoverInto is the tail shared by the recovery rungs (fingerprint,
		// stable fingerprint, CUB): bind this device to the recovered token and
		// issue cookies. Returns false — and authorizes nothing — when the
		// device is on the token's revocation list.
		recoverInto := func(tok, bindUID, via string) bool {
			if bindUID != "" {
				// Лимит устройств тут не проверялся вовсе — одна из двух веток,
				// мимо которых утекало (вторая — tgAuthBindDeviceHandler). Спрашиваем
				// его ТОЛЬКО когда появится новая запись: восстановление своей же
				// коробки, потерявшей uid, счётчик не увеличивает, и рубить его
				// на потолке значило бы отнять у человека собственное устройство.
				lbl := deviceLabel(label, r.UserAgent())
				if !store.DeviceAlreadyKnown(tok, bindUID, fp, sfp, lbl) {
					if limit := effectiveDeviceLimit(store, tok, tgServerMaxDevices); limit > 0 && store.DeviceCount(tok) >= limit {
						log.Warn().Str("uid", bindUID).Str("via", via).Int("limit", limit).
							Int("devices", store.DeviceCount(tok)).Str("ip", clientIP(r)).
							Msg("tgauth: recover — device limit reached, not binding")
						return false
					}
				}
				now := time.Now().UTC()
				_, err := store.AddDevice(tok, tgauth.DeviceInfo{
					UID: bindUID, Fingerprint: fp, StableFP: sfp,
					Label:   deviceLabel(label, r.UserAgent()),
					BoundAt: now, LastSeen: now, LastIP: clientIP(r),
				})
				if errors.Is(err, tgauth.ErrDeviceRevoked) {
					log.Warn().Str("uid", bindUID).Str("via", via).Str("ip", clientIP(r)).
						Msg("tgauth: status — recovery matched a device the user unbound, refusing")
					return false
				}
				if fp != "" {
					store.UpdateDeviceFingerprint(tok, bindUID, fp)
				}
				if sfp != "" {
					store.UpdateDeviceStableFP(tok, bindUID, sfp)
				}
			}
			setAuthCookies(w, r, tok) // restore both cookies
			learnCub(tok)
			log.Info().Str("uid", bindUID).Str("via", via).Str("ip", clientIP(r)).Msg("tgauth: status — recovered via device anchor")
			resp := map[string]any{"authorized": true, "token": tok}
			attachProofKey(resp, store, tok, bindUID)
			writeJSON(w, http.StatusOK, resp)
			return true
		}

		// Check if user has a valid token (check _lampac_auth first, then lampac_token, then ?token=)
		authTok, authVia := "", ""
		for _, cookieName := range []string{"_lampac_auth", "lampac_token"} {
			if c, err := r.Cookie(cookieName); err == nil {
				if token := strings.TrimSpace(c.Value); token != "" {
					if _, ok := store.Lookup(token); ok {
						authTok, authVia = token, "cookie:"+cookieName
						break
					}
				}
			}
		}
		if authTok == "" {
			if qToken := strings.TrimSpace(r.URL.Query().Get("token")); qToken != "" {
				if _, ok := store.Lookup(qToken); ok {
					authTok, authVia = qToken, "query"
				}
			}
		}
		revoked := false
		if authTok != "" {
			// Holding the account token is not enough for an UNBOUND uid: the
			// owner may have thrown exactly this device out through the bot.
			// The device still has the token in cookie/localStorage, so without
			// this check "unbind" was cosmetic — it re-attached on the next boot.
			if uid != "" && !store.HasDevice(authTok, uid) && store.IsDeviceRevoked(authTok, uid, fp, sfp, clientIP(r)) {
				log.Warn().Str("uid", uid).Str("ip", clientIP(r)).Str("token_prefix", authTok[:min(8, len(authTok))]).
					Msg("tgauth: status — device was unbound by the user, refusing token and clearing cookies")
				clearAuthCookies(w, r)
				revoked = true
			} else {
				setAuthCookies(w, r, authTok)
				learnCub(authTok)
				resp := map[string]any{"authorized": true, "token": authTok}
				if !bindDeviceIfNew(authTok) {
					resp["device_limit"] = true
				}
				// ПОСЛЕ привязки: у только что заведённого устройства ключа ещё
				// нет, и порядок здесь определяет, получит ли новый клиент
				// аттестацию с первого же ответа или только со второго.
				attachProofKey(resp, store, authTok, uid)
				if detectCrossTokenConflict(authTok) {
					log.Warn().Str("uid", uid).Str("token_prefix", authTok[:min(8, len(authTok))]).
						Msg("tgauth: status — UID belongs to another token, signaling regen")
					resp["uid_conflict"] = true
				}
				log.Info().Str("via", authVia).Str("ip", clientIP(r)).Msg("tgauth: status — authorized via token")
				writeJSON(w, http.StatusOK, resp)
				return
			}
		}

		// Check by device UID — allows token recovery after cache clear.
		// SECURITY: also verify fingerprint matches the stored device fp.
		// Without this, anyone who learns a UID (8-char ~2^48 entropy) gets
		// the bound token. Cub backup also clones lampac_unic_id to other
		// devices, so fp-less UID auto-auth silently shares accounts.
		if uid != "" {
			if tok, dev, ok := store.FindDeviceByUID(uid); ok {
				// Auto-auth only when the fingerprint evidence does NOT point to a
				// DIFFERENT physical device:
				//   - the stored device has no fp yet (legacy / first bind, and the
				//     fp-less web client stays here for its own uid), OR
				//   - the request's fp matches the stored one exactly.
				// ★2026-08-15: the old rule also auto-authed when `fp == ""`. That let a
				// DIFFERENT fp-less device ride a COLLIDED uid straight into someone
				// else's account (cheap Android TV boxes share ANDROID_ID → same
				// fold(uid); cloned localStorage does the same) — the user saw «страница
				// обновилась, QR нет»: the app silently logged into a stranger's account
				// instead of showing the login. A request that omits fp while the stored
				// device HAS one is provably not that device → refuse and signal
				// uid_conflict so the client regenerates its uid and lands on its own QR.
				allow, cloned, why := uidRestoreVerdict(store, dev, uid, fp, clientIP(r))
				if allow {
					setAuthCookies(w, r, tok) // restore both cookies
					learnCub(tok)
					resp := map[string]any{"authorized": true, "token": tok}
					attachProofKey(resp, store, tok, uid)
					writeJSON(w, http.StatusOK, resp)
					return
				}
				log.Warn().Str("uid", uid).Str("ip", clientIP(r)).Bool("req_has_fp", fp != "").Str("why", why).
					Msg("tgauth: status — по uid не восстанавливаем")
				// ★Перегенерацию uid просим только там, где доказано, что им
				// пользуется не одно устройство (общий uid или чужой отпечаток).
				// Раньше uid_conflict выставлялся на ЛЮБОЙ отказ, включая обычный
				// запрос без отпечатка, — и клиент выбрасывал рабочий идентификатор
				// устройства, терял привязку и шёл на QR по кругу.
				uidConflict = cloned
			}
		}

		// Check by device fingerprint — allows token recovery after full data
		// clear (localStorage + cookies both wiped, so no UID either). Try the
		// precise fp first; fall back to the coarse stable fp, which survives a
		// firmware update that shifted canvas/WebGL. Both lookups refuse
		// ambiguous cross-account matches, so a coarse collision restores
		// nobody rather than the wrong user. On a hit, re-bind this (fresh) UID
		// to the recovered device so subsequent boots recover via the cheaper
		// UID path.
		//
		// The precise fp is a hash of screen/GPU/canvas/audio — identical across
		// units of one TV or PC model, so a lone match is NOT proof of identity
		// (production: one fp on 54 accounts). It restores only with
		// corroboration: same network as the device's last visit, or a native
		// device id in the stable fp. Otherwise the device gets a QR code.
		if fp != "" {
			if tok, dev, ok := store.FindTokenByDeviceFingerprint(fp); ok && tok != "" {
				if fpRecoveryCorroborated(dev, sfp, clientIP(r)) {
					if recoverInto(tok, uid, "fingerprint") {
						return
					}
				} else {
					log.Info().Str("uid", uid).Str("ip", clientIP(r)).
						Msg("tgauth: status — fingerprint matches an account but from another network and without native id, not restoring")
				}
			}
		}
		if sfp != "" {
			if tok, _, ok := store.FindTokenByStableFP(sfp); ok && tok != "" {
				if recoverInto(tok, uid, "stable-fp") {
					return
				}
			}
		}

		// Check by CUB account — the longest-lived anchor. The client presents
		// its CUB session token (from Lampa's localStorage `account`); we
		// validate it LIVE against the CUB API (fail closed on outage) and
		// restore the account it was passively linked to. This is what turns
		// "re-enter CUB with a short site code" into a full binding recovery
		// after a total client wipe.
		if cub != "" && cubVal != nil {
			switch info, err := cubVal.Validate(r.Context(), cub); {
			case err == nil:
				if tok, ok := store.FindTokenByCubUser(info.ID); ok && tok != "" {
					// Bind the device even when the client sent no uid (old
					// cached gate JS, external Lampa without lampac_unic_id):
					// an unbound recovery is invisible in the bot's device
					// list and can't use the cheaper uid rung next boot.
					// Synthetic uid mirrors tgAuthBindDeviceHandler.
					bindUID := uid
					if bindUID == "" {
						switch {
						case fp != "":
							bindUID = "fp-" + fp[:min(8, len(fp))]
						case sfp != "":
							bindUID = "sfp-" + sfp[:min(8, len(sfp))]
						default:
							bindUID = "cub-" + info.ID[:min(8, len(info.ID))]
						}
					}
					if recoverInto(tok, bindUID, "cub:"+info.ID) {
						return
					}
					break
				}
				activeLinks, expiredLinks := store.CubLinkStats(info.ID)
				log.Info().Str("cub_uid", info.ID).Int("active_links", activeLinks).Int("expired_links", expiredLinks).Str("ip", clientIP(r)).
					Msg("tgauth: status — CUB token valid but no unique active link (0 active = not linked, 2+ = ambiguous), falling through")
			case errors.Is(err, errCubTokenInvalid):
				log.Info().Str("ip", clientIP(r)).Msg("tgauth: status — CUB token rejected by upstream")
			default:
				log.Warn().Err(err).Str("ip", clientIP(r)).Msg("tgauth: status — CUB validation unavailable, skipping anchor")
			}
		}

		// Not authorized — find or create pending code.
		// On uid_conflict, do NOT bind the pending to the (cloned) UID — the
		// client will regenerate its UID on receiving uid_conflict and retry.
		ip := clientIP(r)

		var code string
		if uid != "" && !uidConflict {
			if existing, ok := pending.FindByUID(uid); ok {
				code = existing.Code
			} else {
				req := pending.CreateForUID(uid, ip)
				code = req.Code
			}
		} else {
			if existing, ok := pending.FindByClientIP(ip); ok {
				code = existing.Code
			} else {
				req := pending.Create(ip)
				code = req.Code
			}
		}

		resp := map[string]any{
			"authorized": false,
			"code":       code,
			"bot":        "@" + botName,
		}
		if uidConflict {
			resp["uid_conflict"] = true
		}
		if revoked {
			// Tell the client to drop its stored token so it stops presenting
			// it on every boot; the QR code above is the way back in.
			resp["revoked"] = true
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// tgAuthBindDeviceHandler binds a device UID and fingerprint to an existing auth token.
// Called by auth gate after successful authorization to enable uid/fingerprint-based recovery.
// Returns {"uid_conflict": true} if the UID is already bound to another token (cub-clone case).
func tgAuthBindDeviceHandler(store *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.URL.Query().Get("token"))
		uid := strings.TrimSpace(r.URL.Query().Get("uid"))
		fp := strings.TrimSpace(r.URL.Query().Get("fp"))
		sfp := strings.TrimSpace(r.URL.Query().Get("sfp"))
		if token == "" || (uid == "" && fp == "" && sfp == "") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token and uid/fp required"})
			return
		}
		if _, ok := store.Lookup(token); !ok {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid token"})
			return
		}
		if uid == "" {
			if fp != "" {
				uid = "fp-" + fp[:min(8, len(fp))] // synthetic uid from fingerprint
			} else {
				uid = "sfp-" + sfp[:min(8, len(sfp))]
			}
		}
		// Лимит устройств. Эта ручка (GET /tg/auth/bind-device) привязывала
		// безусловно — при живом лимите в группе. Утёкший токен через неё
		// набирал устройства сколько угодно. Спрашиваем лимит только когда
		// появится НОВАЯ запись, иначе повторная привязка своей же коробки
		// упиралась бы в потолок, ничего к нему не добавляя.
		if !store.DeviceAlreadyKnown(token, uid, fp, sfp, "") {
			if limit := effectiveDeviceLimit(store, token, tgServerMaxDevices); limit > 0 && store.DeviceCount(token) >= limit {
				log.Warn().Str("uid", uid).Int("limit", limit).Int("devices", store.DeviceCount(token)).
					Str("ip", clientIP(r)).Str("token_prefix", token[:min(8, len(token))]).
					Msg("tgauth: bind-device — device limit reached, refusing")
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "device_limit": limit})
				return
			}
		}
		dev := tgauth.DeviceInfo{
			UID:         uid,
			Fingerprint: fp,
			StableFP:    sfp,
			BoundAt:     time.Now().UTC(),
			LastSeen:    time.Now().UTC(),
			LastIP:      clientIP(r),
		}
		_, err := store.AddDevice(token, dev)
		if errors.Is(err, tgauth.ErrUIDCrossToken) {
			log.Warn().Str("uid", uid).Str("token_prefix", token[:min(8, len(token))]).Str("ip", clientIP(r)).
				Msg("tgauth: bind-device — UID belongs to another token, signaling client to regen")
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "uid_conflict": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// userInfoHandler returns public user info (expiration, platform) for SURS and similar plugins.
// GET /api/user/info — reads token from cookie or query.
// Response: {"platform":"alcopac","authorized":true,"expires_at":"2026-06-01T00:00:00Z","days_left":75}
func userInfoHandler(store *tgauth.Store, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := auth.ExtractToken(r)
		// UID-based lookup fallback.
		if token == "" {
			if uid := strings.TrimSpace(r.URL.Query().Get("uid")); uid != "" && store != nil {
				token = store.FindTokenByDeviceUID(uid)
			}
		}
		resp := map[string]any{
			"platform": "alcopac",
			"version":  version,
		}
		if token == "" || store == nil {
			resp["authorized"] = false
			writeJSON(w, http.StatusOK, resp)
			return
		}
		t, ok := store.Lookup(token)
		if !ok {
			resp["authorized"] = false
			writeJSON(w, http.StatusOK, resp)
			return
		}
		daysLeft := int(time.Until(t.ExpiresAt).Hours() / 24)
		if daysLeft < 0 {
			daysLeft = 0
		}
		resp["authorized"] = true
		resp["expires_at"] = t.ExpiresAt.Format(time.RFC3339)
		resp["days_left"] = daysLeft
		addPremiumFields(resp, t.PremiumUntil)
		resp["tg_username"] = t.TGUsername
		if t.CubUserID != "" {
			resp["cub_linked"] = true
			resp["cub_email"] = t.CubEmail
			resp["cub_uid"] = t.CubUserID
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// authCookieSameSiteNone, when true, sets the auth cookies as SameSite=None;Secure (over HTTPS).
// Default false → SameSite=Lax. Wired once at server start from cfg.Auth.CookieSameSite.
var authCookieSameSiteNone bool

// authCookieSameSite picks the SameSite/Secure attributes for the auth cookies.
//
// DEFAULT is Lax — correct for a SAME-ORIGIN SPA deploy (the ALPAC client served by lampac): Lax is
// delivered on same-origin requests AND top-level navigations (exactly the /tg/auth/complete handoff)
// AND is accepted by every WebView. SameSite=None, by contrast, is REJECTED outright by old Android /
// TV WebViews (Chromium <67): approval "succeeds", the bot shows ✓, yet the cookie is never stored,
// so after a refresh the user is logged out again («успешный вход, после обновления снова авторизация»).
//
// SameSite=None is opt-in (cfg.Auth.CookieSameSite="none") and only needed when this instance backs a
// CROSS-ORIGIN Lampa fork on another domain that must send the cookie on cross-site XHRs.
func authCookieSameSite(r *http.Request) (http.SameSite, bool) {
	secure := isHTTPSRequest(r)
	if authCookieSameSiteNone && secure {
		return http.SameSiteNoneMode, true
	}
	return http.SameSiteLaxMode, secure
}

// setAuthCookies sets both lampac_token (readable by JS) and _lampac_auth
// (HttpOnly, tamper-proof). Malicious plugins can clear lampac_token via
// document.cookie, but cannot touch _lampac_auth. The auth middleware
// checks _lampac_auth first.
//
// SameSite=None when over HTTPS so the cookies survive cross-origin XHRs.
// That's the canonical Lampa-on-lampa.mx ↔ lampac-on-beta.example.com
// scenario: without SameSite=None the browser withholds the cookies on
// any subresource request, plugins see "not authenticated" even though
// the TG OAuth redirect set the cookies correctly. SameSite=None also
// requires Secure, which is why we keep Lax for plain HTTP installs
// (LAN testing, no Secure available there).
// gateToken — кандидат в токен запроса и откуда он.
type gateToken struct{ token, src string }

// gateTokenCandidates — все токены запроса без повторов, в порядке auth.ExtractToken: HttpOnly-кук
// `_lampac_auth`, куки `alpac_token`/`lampac_token`, заголовки X-Alpac-Token/X-Lampac-Token и
// последним ?token=. Гейт перебирает их, пропуская токен, отвязанный от этого устройства.
func gateTokenCandidates(r *http.Request) []gateToken {
	var out []gateToken
	seen := map[string]bool{}
	add := func(tok, src string) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] {
			return
		}
		seen[tok] = true
		out = append(out, gateToken{token: tok, src: src})
	}
	for _, name := range []string{"_lampac_auth", "alpac_token", "lampac_token"} {
		if c, err := r.Cookie(name); err == nil {
			add(c.Value, "cookie:"+name)
		}
	}
	add(r.Header.Get("X-Alpac-Token"), "header:X-Alpac-Token")
	add(r.Header.Get("X-Lampac-Token"), "header:X-Lampac-Token")
	add(r.URL.Query().Get("token"), "query")
	return out
}

func setAuthCookies(w http.ResponseWriter, r *http.Request, token string) {
	sameSite, secure := authCookieSameSite(r)
	// Two JS-readable cookies with the same value: the legacy `lampac_token`
	// (read by older plugins and external lampac-ecosystem code) and our
	// brand-scoped `alpac_token`. The dual cookie protects the auth state
	// from a third-party plugin that calls `document.cookie='lampac_token=...'`
	// (some Lampa forks reset it during their own onboarding) — our server
	// reads alpac_token first, so the user stays signed in.
	for _, name := range []string{"lampac_token", "alpac_token"} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    token,
			Path:     "/",
			MaxAge:   365 * 24 * 3600,
			SameSite: sameSite,
			Secure:   secure,
		})
	}
	// HttpOnly anchor — single cookie is enough; JS can't touch it
	// regardless of name, so namespace collisions don't apply.
	http.SetCookie(w, &http.Cookie{
		Name:     "_lampac_auth",
		Value:    token,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		SameSite: sameSite,
		Secure:   secure,
		HttpOnly: true,
	})
}

// tgAuthCompleteHandler sets auth cookies via a 302 redirect.
// This is the most reliable way to set cookies — the browser processes
// Set-Cookie headers from redirect responses before following the Location.
// The auth page JS redirects here after approval instead of setting cookies via fetch.
func tgAuthCompleteHandler(store *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.URL.Query().Get("token"))
		if token == "" {
			http.Redirect(w, r, "/tg/auth", http.StatusFound)
			return
		}
		if _, ok := store.Lookup(token); !ok {
			log.Warn().Str("token_prefix", token[:min(8, len(token))]).Msg("tgauth: complete — invalid token")
			http.Redirect(w, r, "/tg/auth", http.StatusFound)
			return
		}
		setAuthCookies(w, r, token)
		log.Info().Str("token_prefix", token[:8]).Str("ip", clientIP(r)).Msg("tgauth: complete — cookies set via redirect")
		// Carry the token in the URL too: some TV WebViews (NVIDIA Shield, restrictive Android TV)
		// drop BOTH the Set-Cookie AND localStorage across this redirect, leaving the SPA with nothing
		// to authenticate with → login loop. The SPA reads ?tgauth on load and authenticates via the
		// X-Lampac-Token header, then strips it from the URL. The token is already client-readable
		// (cookie/localStorage), so this exposes nothing new.
		http.Redirect(w, r, "/?tgauth="+url.QueryEscape(token), http.StatusFound)
	}
}

// clearAuthCookies clears all three auth cookies. SameSite/Secure mirror
// what setAuthCookies would use today — some browsers refuse to overwrite
// a SameSite=None;Secure cookie with a SameSite=Lax deletion stub,
// leaving the old value alive in storage.
func clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	sameSite, secure := authCookieSameSite(r)
	for _, name := range []string{"lampac_token", "alpac_token"} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			SameSite: sameSite, Secure: secure,
		})
	}
	http.SetCookie(w, &http.Cookie{
		Name: "_lampac_auth", Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: sameSite, Secure: secure,
	})
}

// tgAuthLogoutHandler clears the lampac_token cookie.
func tgAuthLogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clearAuthCookies(w, r)
		http.Redirect(w, r, "/tg/auth", http.StatusFound)
	}
}

// ---------- Device verification handlers ----------

// tgDeviceVerifyHandler shows a page for device verification with polling.
func tgDeviceVerifyHandler(dp *tgauth.DevicePendingStore, botName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			http.Redirect(w, r, "/tg/auth", http.StatusFound)
			return
		}
		req, ok := dp.FindByCode(code)
		if !ok {
			http.Redirect(w, r, "/tg/auth", http.StatusFound)
			return
		}

		s := getAuthPageStyle()

		bgImageCSS := ""
		if s.BgImageURL != "" {
			bgImageCSS = fmt.Sprintf(";background-image:url('%s');background-size:cover;background-position:center", s.BgImageURL)
		}
		customStyleTag := ""
		if s.CustomCSS != "" {
			customStyleTag = "<style>" + s.CustomCSS + "</style>"
		}

		// Device-specific blocks (reuse block rows layout)
		logoHTML := ""
		if s.LogoURL != "" {
			logoHTML = fmt.Sprintf(`<img src="%s" alt="Logo" style="max-width:120px;max-height:60px;margin-bottom:16px">`, s.LogoURL)
		}
		deviceBlocks := map[string]string{
			"logo":     logoHTML,
			"title":    `<h1>📱 Привязка устройства</h1>`,
			"subtitle": `<p class="subtitle">Для подключения нового устройства отправьте код боту</p>`,
			"code":     fmt.Sprintf(`<div class="device-label">%s</div><div class="code">%s</div>`, req.Label, req.Code),
			"steps":    fmt.Sprintf(`<ol class="steps"><li>Откройте бота <b>@%s</b> в Telegram</li><li>Отправьте код <b>%s</b></li></ol>`, botName, req.Code),
			"button":   "",
			"qr":       "",
			"status":   `<div class="status" id="status">Ожидание подтверждения...</div>`,
		}
		cardContent := renderBlockRows(s.BlockRows, deviceBlocks, s.BlockStyles)

		html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Привязка устройства</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:%s;color:%s;display:flex;justify-content:center;align-items:center;min-height:100vh%s}
.card{background:%s;border-radius:16px;padding:40px;max-width:420px;width:90%%;text-align:center;box-shadow:0 8px 32px rgba(0,0,0,0.3)}
h1{font-size:22px;margin-bottom:12px;color:#fff}
.subtitle{color:%s;margin-bottom:20px;font-size:14px}
.code{font-size:48px;font-weight:700;letter-spacing:8px;color:%s;margin:20px 0;font-family:'Courier New',monospace}
.device-label{color:#f0c040;font-size:14px;margin-bottom:16px}
.steps{text-align:left;margin:16px 0}
.steps li{margin:8px 0;color:%s;font-size:14px;line-height:1.6}
.status{margin-top:20px;padding:12px;border-radius:8px;font-size:14px;background:%s;color:%s}
.status.confirmed{color:%s}
</style>
%s
</head>
<body>
<div class="card">
%s
</div>
<script>
(function(){
  var code="%s";
  function poll(){
    fetch("/tg/device/check?code="+code)
      .then(function(r){return r.json()})
      .then(function(d){
        if(d.status==="confirmed"){
          document.getElementById("status").className="status confirmed";
          document.getElementById("status").textContent="Устройство привязано! Перенаправление...";
          setTimeout(function(){location.href="/"},1500);
          return;
        }
        setTimeout(poll,3000);
      })
      .catch(function(){setTimeout(poll,5000)});
  }
  setTimeout(poll,2000);
})();
</script>
</body>
</html>`,
			s.BgColor, s.TextColor, bgImageCSS, // body
			s.CardColor,    // .card
			s.TextColor,    // .subtitle
			s.AccentColor,  // .code
			s.TextColor,    // .steps li
			s.BgColor,      // .status bg
			s.TextColor,    // .status
			s.AccentColor,  // .status.confirmed
			customStyleTag, // custom CSS
			cardContent,    // ordered blocks
			req.Code,       // JS poll code
		)

		writeHTML(w, http.StatusOK, html)
	}
}

// tgDeviceCheckHandler returns the status of a device-verification code as JSON.
func tgDeviceCheckHandler(dp *tgauth.DevicePendingStore, store *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		// Check by code
		if code := strings.TrimSpace(q.Get("code")); code != "" {
			req, ok := dp.FindByCode(code)
			if !ok {
				writeJSON(w, http.StatusOK, map[string]string{"status": "expired"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": req.Status})
			return
		}

		// Check by uid (for Lampa TV polling)
		if uid := strings.TrimSpace(q.Get("uid")); uid != "" {
			// Check if uid is already bound to a token
			if token := store.FindTokenByDeviceUID(uid); token != "" {
				writeJSON(w, http.StatusOK, map[string]any{"status": "bound", "token": token})
				return
			}
			// Check if there is a pending request
			if req, ok := dp.FindByUID(uid); ok {
				resp := map[string]any{"status": "pending", "code": req.Code}
				writeJSON(w, http.StatusOK, resp)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "unknown"})
			return
		}

		writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "missing code or uid"})
	}
}

// ---------- Gate middleware ----------

// deviceLabelFromUA extracts a short label from User-Agent.
// deviceLabel prefers a label the client supplied, falling back to UA sniffing.
//
// Client-supplied text is displayed in the Telegram bot's device list, so it is
// sanitised here rather than at render time: control characters and angle
// brackets are dropped (the bot sends HTML), and the length is bounded.
func deviceLabel(supplied, ua string) string {
	supplied = strings.TrimSpace(supplied)
	if supplied == "" {
		return deviceLabelFromUA(ua)
	}
	var b strings.Builder
	for _, r := range supplied {
		switch {
		case r < 0x20 || r == 0x7f: // control chars
			continue
		case r == '<' || r == '>' || r == '&':
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return deviceLabelFromUA(ua)
	}
	return out
}

func deviceLabelFromUA(ua string) string {
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "tizen"):
		return "Samsung TV"
	case strings.Contains(ua, "webos"), strings.Contains(ua, "web0s"): // modern LG UAs spell it "Web0S" (zero)
		return "LG TV"
	case strings.Contains(ua, "vidaa"), strings.Contains(ua, "hisense"):
		return "Hisense TV"
	case strings.Contains(ua, "android tv"), strings.Contains(ua, "androidtv"):
		return "Android TV"
	case strings.Contains(ua, "firetv"), strings.Contains(ua, "fire tv"):
		return "Fire TV"
	case strings.Contains(ua, "android"):
		return "Android"
	case strings.Contains(ua, "iphone"):
		return "iPhone"
	case strings.Contains(ua, "ipad"):
		return "iPad"
	case strings.Contains(ua, "windows"):
		return "Windows"
	case strings.Contains(ua, "macintosh"), strings.Contains(ua, "mac os"):
		return "macOS"
	case strings.Contains(ua, "linux"):
		return "Linux"
	case strings.Contains(ua, "chrome"):
		return "Chrome"
	case strings.Contains(ua, "firefox"):
		return "Firefox"
	case strings.Contains(ua, "safari"):
		return "Safari"
	default:
		return "Unknown"
	}
}

// tgAuthGateMiddleware redirects unauthenticated users to /tg/auth.
// It supports device binding via uid parameter and auto-reauth for known devices.
// gatePreAuthAllowed reports whether the request is on the TG-auth gate's
// pre-authentication allowlist: paths/extensions/headers that must pass
// without a valid token (CORS preflight, cluster-trusted forwards, static
// assets, /proxy streams, bootstrap probes, self-authenticating subsystems).
//
// Extracted verbatim from tgAuthGateMiddleware so the mirror-mode delegating
// gate (mirror.go) applies the IDENTICAL allowlist before deciding whether
// to consult the origin. Pure — no store, no I/O — safe to call from both.
//
// The two token-dependent allowances stay inline in their gates because they
// need a store / request context: the /js/{token} plugin paths (store lookup)
// and the mode-aware non-TG context user. Mirror handles those separately.
func gatePreAuthAllowed(r *http.Request) bool {
	// CORS preflight — already handled by globalCORSMiddleware.
	if r.Method == http.MethodOptions {
		return true
	}

	// Cluster trust: X-Cluster-Key matching the shared secret means the
	// request comes from the cluster primary which already authenticated
	// the user. (No-op on a mirror: requires Cluster.Enable.)
	if isTrustedClusterRequest(r) {
		return true
	}

	path := r.URL.Path

	// Mirror control-plane endpoints — self-authenticate via the mirror key
	// (isMirrorOriginRequest). Must bypass the gate so mirrors can reach them.
	if path == "/api/auth/validate" || path == "/api/cluster/authgate" ||
		path == "/api/cluster/plugins.tar.gz" || path == "/api/cluster/wwwroot.tar.gz" {
		return true
	}

	// Always allow auth-related and device-verification paths.
	if strings.HasPrefix(path, "/tg/auth") || strings.HasPrefix(path, "/tg/device") {
		return true
	}

	// Tokenized on.js bundle (/on/js/<t>, /on/h/<t>, /on/<t>) — the handler
	// validates the path token itself and falls back to the gate-wrapped
	// limited bundle on a dead token. Gating it here would answer the
	// <script src> with an accsdb JSON body: the app boots empty — no
	// plugins, no gate, no recovery. The bundle body is public plugin JS;
	// sources stay gated at /lite/*.
	if strings.HasPrefix(path, "/on/") {
		return true
	}

	// Kit WebApp and API (own auth via Telegram initData / Bearer token).
	if path == "/kit" || path == "/bkit" || strings.HasPrefix(path, "/api/kit/") {
		return true
	}

	// Health/system endpoints.
	if slices.Contains([]string{
		"/ping", "/version", "/healthz", "/readyz", "/metrics",
		// Список нод для замера скорости: приложение спрашивает его до входа,
		// иначе первый же замер меряет не то, что будет отдавать видео.
		"/api/edges",
		// BlurHash постеров: публичные картинки TMDB, ответ — 28 символов на постер; без гейта,
		// иначе заглушки рейлов на экране входа и у гостей остаются серыми.
		"/api/blurhash",
		// Доклад ТВ-лаунчера: он по определению работает ДО входа — гейт отвечал ему
		// приглашением с QR-кодом, и про телевизоры мы не видели ничего.
		"/api/tvlauncher",
		// Замер по времени (/api/edges/speed?ms=…) — та же проба, что и
		// /api/edges/speed/{mb}, только без завершающего слэша: под прежний
		// префикс она не попадала и упиралась в гейт.
		"/api/edges/speed",
	}, path) || strings.HasPrefix(path, "/api/edges/speed/") {
		return true
	}

	// pprof profiling endpoints.
	if strings.HasPrefix(path, "/debug/pprof") {
		return true
	}

	// /proxy/, /proxy-dash/, /transcoding/ — already protected by AES-encrypted
	// URL tokens / HMAC stream IDs; native players and HLS.js carry no cookies.
	// /lite/pidtor/s{hash} is the PidTor stream entry (opaque infohash credential).
	if strings.HasPrefix(path, "/proxy/") || strings.HasPrefix(path, "/proxy-dash/") || strings.HasPrefix(path, "/transcoding/") ||
		strings.HasPrefix(path, "/proxyimg/") || strings.HasPrefix(path, "/proxyimg:") ||
		strings.HasPrefix(path, "/vibix_m3u8/") || strings.HasPrefix(path, "/vibix_embed/") ||
		(strings.HasPrefix(path, "/lite/pidtor/s") && !strings.HasPrefix(path, "/lite/pidtor/serial/")) {
		return true
	}

	// /api/cluster/* — межсерверный контур кластера, он аутентифицируется САМ
	// (ключ в X-Cluster-Key или Bearer, см. cluster). Гейт его глотал: на запрос
	// обновляльщика ноды (`Authorization: Bearer <api_key>`) main отвечал не
	// бинарником, а JSON «требуется авторизация» — и с кодом 200. Нода честно
	// считала sha256 этой страницы, он не сходился с ожидаемым, и обновление
	// срывалось КАЖДЫЕ 10 минут молча: обе ноды простояли на сборке от 1 сентября
	// двое суток (найдено 2026-09-04). Ровно так же они однажды отстали на месяц
	// и отвечали 501 на трети запросов.
	if strings.HasPrefix(path, "/api/cluster/") {
		return true
	}

	// /ts/* — the TorrServer subsystem authenticates ITSELF (tsExternalAuth →
	// tsAccessMiddleware → tsRequireTokenMiddleware, which resolves a cookie,
	// ?token=, or the Basic "uid:ts" credential Lampa sends). It must bypass the
	// app-wide gate, because a Lampa instance hosted on ANOTHER origin
	// (lampa.mx + our plugin) issues its TorrServer XHRs WITHOUT withCredentials
	// — so no cookie reaches us and the gate would swallow /ts/settings and
	// /ts/torrents, answering with an auth payload instead of TorrServer JSON.
	// Lampa reads that as "не удалось подключиться к TorrServer" and never
	// starts playback. Mutations stay protected downstream; only the liveness
	// probe (/ts/echo, /ts/settings {get}), the stream and the speed-test
	// download are open — exactly as a real MatriX server exposes them.
	if path == "/ts" || strings.HasPrefix(path, "/ts/") {
		return true
	}

	// /cub/ and /cubproxy/ — CUB proxy has own auth; unauth gets safe empty
	// fallbacks, not real user data.
	if strings.HasPrefix(path, "/cub/") || strings.HasPrefix(path, "/cubproxy/") {
		return true
	}

	// /api/proxystream — internal API for custom balancer subprocesses
	// (already localhost-gated).
	if path == "/api/proxystream" {
		return true
	}

	// CUB secuses endpoint — Lampa calls this during init before auth.
	if path == "/api/v1.0/events/secuses" {
		return true
	}

	// Jackett-compatible parser proxy (jacred_api.go) — pure pass-through to
	// the configured JacRed upstream, no user data. Lampa's torrent parser
	// XHRs these from WebViews that routinely lose cookies; gating them turns
	// the parser into an auth-gate JS payload instead of JSON.
	if strings.HasPrefix(path, "/api/v2.0/indexers/") || path == "/api/v1.0/torrents" ||
		path == "/api/v1.0/conf" || strings.HasPrefix(path, "/parse/") {
		return true
	}

	// Alice webhook — own pairing-based auth.
	if path == "/api/alice/webhook" {
		return true
	}

	// NWS / SignalR WebSocket endpoints — auth handled post-connect.
	if path == "/nws" || path == "/ws" {
		return true
	}

	// Web player endpoints: WS relay (must not be 302'd), health JSON,
	// optional static assets.
	if strings.HasPrefix(path, "/webplayer/ws/") ||
		strings.HasPrefix(path, "/webplayer/health/") ||
		strings.HasPrefix(path, "/webplayer/assets/") {
		return true
	}

	// Internal loopback probes (checksearch, life-checks).
	if r.Header.Get("X-Lampac-Go") == "1" {
		return true
	}

	// MSX paths — smart TV apps need them without auth.
	if strings.HasPrefix(path, "/msx/") {
		return true
	}

	// Root "/" — serves the Lampa web app; auth handled by gate JS in on.js.
	if path == "/" {
		return true
	}

	// TMDB proxy — pass-through to themoviedb.org, no user data.
	if strings.HasPrefix(path, "/tmdb/") {
		return true
	}

	// First-party client API + SPA. /capi self-authenticates; /app is public.
	if strings.HasPrefix(path, "/capi/") || path == "/app" || strings.HasPrefix(path, "/app/") {
		return true
	}

	// Bootstrap connectivity/CORS probes Lampa calls before auth.
	if path == "/cors/check" || path == "/corseu" ||
		strings.HasPrefix(path, "/corseu/") {
		return true
	}

	// Static asset extensions (needed for the auth page itself).
	// ★.wgt здесь НЕ место: единственный такой путь — /samsung.wgt, а это готовый
	// виджет с бэкенда, ровно как /webos.ipk, который закрыт входом. Из-за
	// расширения в этом списке Samsung-оболочка раздавалась анонимам (2026-09-10).
	switch filepath.Ext(path) {
	case ".js", ".css", ".png", ".jpg", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".eot", ".mp3", ".webmanifest",
		".m3u8", ".m4s", ".ts", ".mp4", ".m4a", ".lampa":
		return true
	}

	return false
}

func tgAuthGateMiddleware(store *tgauth.Store, dp *tgauth.DevicePendingStore, pending *tgauth.PendingStore, bot *tgauth.Bot, banStore *tgauth.BanStore, geoDB *geoip.DB, memberChecker *tgauth.MembershipChecker, cfg config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Pre-authentication allowlist (CORS preflight, cluster-trusted
			// requests, static assets, /proxy streams, bootstrap probes,
			// self-authenticating subsystems). Extracted to gatePreAuthAllowed
			// so the mirror-mode delegating gate (mirror.go) applies the EXACT
			// same allowlist before consulting the origin. Keep both in sync.
			if gatePreAuthAllowed(r) {
				// ★Аллоулист пропускает /capi/* — а это и есть весь обычный день
				// пользователя. Единственная запись LastSeen жила НИЖЕ по этой
				// функции, поэтому активность живых устройств не фиксировалась
				// вовсе. Отмечаем здесь, с троттлингом (device_activity.go).
				touchDeviceActivity(store, r)
				next.ServeHTTP(w, r)
				return
			}

			path := r.URL.Path

			// Allow tokenized plugin paths: /on/js/{token}, /online/js/{token}, etc.
			if strings.Contains(path, "/js/") || strings.Contains(path, "/h/") {
				parts := strings.Split(path, "/")
				if len(parts) >= 4 {
					pathToken := parts[len(parts)-1]
					if pathToken != "" {
						if _, ok := store.Lookup(pathToken); ok {
							next.ServeHTTP(w, r)
							return
						}
					}
				}
			}

			// Mode-aware allow (2026-05-28): authMiddleware runs before this
			// gate and resolves password / anon / profile / accsdb sessions
			// into the request context. The logic below only understands the
			// TG store, so without honoring the context a password-mode user
			// (or anon in open mode) gets bounced into the TG pending-code
			// flow even though they're already authenticated. TG users fall
			// through to the device-binding / ban / membership handling below.
			if u, ok := auth.UserFromContext(r.Context()); ok && u != nil && !strings.HasPrefix(u.ID, "tg:") {
				next.ServeHTTP(w, r)
				return
			}

			uid := strings.TrimSpace(r.URL.Query().Get("uid"))
			fpQ := strings.TrimSpace(r.URL.Query().Get("fp"))
			sfpQ := strings.TrimSpace(r.URL.Query().Get("sfp"))

			// Токен запроса. Кандидаты — в ТОМ ЖЕ порядке, что у middleware (auth.ExtractToken): куки,
			// заголовки, и только потом ?token=. Раньше гейт брал ?token= РАНЬШЕ заголовков, и на этом
			// ломалась Лампа, восстановленная из чужого бэкапа CUB: в адресе её плагина вшит ЧУЖОЙ токен
			// (online.js подставляет его в ?token=), а свой наша авторизация уже положила в хранилище и
			// шлёт заголовком. Владелец чужого токена отвязал этот uid — гейт выбрасывал токен, показывал
			// новый QR, человек подтверждал код, и всё повторялось («ввожу код — просит новый», LG,
			// 24.09.2026: uid 1brbgdka записан у 95 аккаунтов). Поэтому же токен, отвязанный от ЭТОГО
			// устройства, не конец разбора: берём следующий кандидат того же запроса.
			var validToken, validSrc, revokedToken string
			for _, c := range gateTokenCandidates(r) {
				if _, ok := store.Lookup(c.token); !ok {
					continue
				}
				if uid != "" && !store.HasDevice(c.token, uid) && store.IsDeviceRevoked(c.token, uid, fpQ, sfpQ, clientIP(r)) {
					if revokedToken == "" {
						revokedToken = c.token
					}
					continue
				}
				validToken, validSrc = c.token, c.src
				break
			}
			if validToken != "" && revokedToken != "" {
				log.Info().Str("uid", uid).Str("ip", clientIP(r)).Str("revoked_prefix", revokedToken[:min(8, len(revokedToken))]).
					Str("token_prefix", validToken[:min(8, len(validToken))]).Str("src", validSrc).
					Msg("tgauth-gate: skipped a token unbound from this device, using another from the same request")
			}
			// Track whether the cookie-setting paths below already wrote
			// fresh cookies. The symmetric recovery block must not re-issue
			// if we just set them via the query-token branch.
			cookiesAlreadyIssued := false
			switch validSrc {
			case "":
			case "cookie:_lampac_auth":
			case "cookie:lampac_token":
				// lampac_token valid but _lampac_auth missing —
				// set the HttpOnly cookie for tamper protection.
				http.SetCookie(w, &http.Cookie{
					Name: "_lampac_auth", Value: validToken,
					Path: "/", MaxAge: 365 * 24 * 3600,
					SameSite: http.SameSiteLaxMode, HttpOnly: true,
				})
			case "query":
				// Token from query param (post-auth redirect) — set cookies
				// so subsequent requests work without ?token= in URL.
				setAuthCookies(w, r, validToken)
				cookiesAlreadyIssued = true
				log.Info().Str("ip", clientIP(r)).Msg("tgauth-gate: token from query param, cookies set")
			default:
				// кук `alpac_token` или заголовок X-Alpac-Token/X-Lampac-Token (Android WebView не
				// переигрывает куки в XHR) — выдаём настоящие куки, чтобы следующие запросы их несли.
				setAuthCookies(w, r, validToken)
				cookiesAlreadyIssued = true
			}

			// SYMMETRIC COOKIE RECOVERY (2026-05-20).
			//
			// Lampa client (Android WebView in particular) loses cookies
			// asymmetrically — sometimes only `lampac_token` (JS-visible) is
			// dropped, sometimes only `_lampac_auth` (HttpOnly). If at least
			// one survives we still authenticate the request here, but client-
			// side JS reads `lampac_token` directly to render the "logged in"
			// state — seeing it missing flips the UI to logged-out even
			// though the server still recognises the user. That's the
			// "периодически слетает" symptom users reported.
			//
			// Re-issue both cookies whenever EITHER is missing. Equivalent to
			// a rolling session: every authenticated request extends the
			// 365-day MaxAge, so one successful request after a drop repairs
			// the state. Cheap — two Set-Cookie headers on already-authed
			// responses; no extra round-trips.
			if validToken != "" && !cookiesAlreadyIssued {
				haveLampac, haveAuth := false, false
				if c, err := r.Cookie("lampac_token"); err == nil && strings.TrimSpace(c.Value) == validToken {
					haveLampac = true
				}
				if c, err := r.Cookie("_lampac_auth"); err == nil && strings.TrimSpace(c.Value) == validToken {
					haveAuth = true
				}
				if !haveLampac || !haveAuth {
					setAuthCookies(w, r, validToken)
					log.Debug().Str("ip", clientIP(r)).
						Bool("had_lampac_token", haveLampac).
						Bool("had_auth", haveAuth).
						Msg("tgauth-gate: auto-recovered missing cookie(s)")
				}
			}

			// Ban check — block before any further processing
			if banStore != nil && validToken != "" {
				ip := clientIP(r)
				fp := r.URL.Query().Get("fp")
				var tgID int64
				if t, found := store.Lookup(validToken); found {
					tgID = t.TelegramID
				}
				var country string
				if geoDB != nil {
					country = geoDB.Country(ip)
				}
				if banned, reason := banStore.IsBanned(uid, ip, tgID, fp, country); banned {
					log.Info().Str("uid", uid).Str("ip", ip).Int64("tg_id", tgID).Str("reason", reason).Msg("tgauth-gate: request blocked by ban")
					writeJSON(w, http.StatusForbidden, map[string]any{
						"error":  "blocked",
						"reason": reason,
					})
					return
				}
			}

			// Revoked device: the owner unbound exactly this device (by uid or
			// hardware hash) through the bot, yet it still carries the shared
			// account token — and no other token in the request is usable (see the
			// candidate loop above). Forget it and fall into Case B, which ends in a
			// fresh QR code — an approval in the bot lifts the revocation. Without
			// this, unbinding was undone on the next request.
			if validToken == "" && revokedToken != "" {
				log.Warn().Str("uid", uid).Str("ip", clientIP(r)).Str("token_prefix", revokedToken[:min(8, len(revokedToken))]).
					Msg("tgauth-gate: device was unbound by the user, dropping its token")
				w.Header().Del("Set-Cookie") // undo any cookie refresh issued above
				clearAuthCookies(w, r)
			}

			// Case A: Valid token
			if validToken != "" {
				// Lazy membership check — verify user is still subscribed to required chats.
				// Uses cache (check_interval_min TTL). Stale checks run in background.
				if memberChecker != nil && memberChecker.HasRequirements() {
					if t, found := store.Lookup(validToken); found && t.TelegramID != 0 {
						if ok, msg, cached := memberChecker.CachedCheck(t.TelegramID); cached {
							if !ok {
								writeJSON(w, http.StatusOK, accsdbResponse(msg))
								return
							}
						} else {
							// Cache miss — run async check, don't block the request.
							// The sweep will catch non-subscribers eventually.
							go memberChecker.CheckMembership(t.TelegramID)
						}
					}
				}

				if uid == "" {
					// No uid — old client, just let through
					next.ServeHTTP(w, r)
					return
				}

				// Check if device is already bound
				if store.HasDevice(validToken, uid) {
					store.UpdateLastSeen(validToken, uid)
					store.UpdateDeviceIP(validToken, uid, clientIP(r))
					if fp := r.URL.Query().Get("fp"); fp != "" {
						store.UpdateDeviceFingerprint(validToken, uid, fp)
					}
					next.ServeHTTP(w, r)
					return
				}

				// ── Fingerprint-based device migration ──
				// Before creating a new device, check if the fingerprint
				// matches an existing device on this token (same physical device, new UID).
				if fp := r.URL.Query().Get("fp"); fp != "" {
					if existing := store.FindDeviceByFingerprint(validToken, fp); existing != nil {
						oldUID := existing.UID
						_ = store.MigrateDeviceUID(validToken, oldUID, uid, clientIP(r))
						store.UpdateDeviceFingerprint(validToken, uid, fp)
						log.Info().
							Str("old_uid", oldUID).Str("new_uid", uid).Str("fp", fp).
							Msg("tgauth-gate: device UID migrated (fingerprint match)")
						if bot != nil {
							if t, _ := store.Lookup(validToken); t != nil && t.TelegramID != 0 {
								label := deviceLabelFromUA(r.UserAgent())
								bot.NotifyDeviceMigrated(t.TelegramID, label, oldUID, uid)
							}
						}
						next.ServeHTTP(w, r)
						return
					}
				}

				// ── Label-based device migration (no-fingerprint clients ONLY) ──
				// Same label = same physical device is only a safe guess when NEITHER side
				// has a fingerprint to compare. When fingerprints exist and DIFFER, these
				// are provably different devices (fp is derived from hardware): merging them
				// made every same-platform pair (two Android phones) share ONE slot, and each
				// request from the "other" phone re-migrated the slot back and forth — the
				// user got a bot notification on every source switch, and the profile showed
				// «Устройства 1» no matter how many devices were active.
				{
					label := deviceLabelFromUA(r.UserAgent())
					reqFP := r.URL.Query().Get("fp")
					if label != "" {
						if existing := store.FindDeviceByLabel(validToken, label); existing != nil &&
							reqFP == "" && existing.Fingerprint == "" {
							oldUID := existing.UID
							_ = store.MigrateDeviceUID(validToken, oldUID, uid, clientIP(r))
							log.Info().
								Str("old_uid", oldUID).Str("new_uid", uid).Str("label", label).
								Msg("tgauth-gate: device UID migrated (label match)")
							if bot != nil {
								if t, _ := store.Lookup(validToken); t != nil && t.TelegramID != 0 {
									bot.NotifyDeviceMigrated(t.TelegramID, label, oldUID, uid)
								}
							}
							next.ServeHTTP(w, r)
							return
						}
					}
				}

				// New device — auto-bind if within device limit
				maxDevices := effectiveDeviceLimit(store, validToken, cfg.TelegramAuth.MaxDevicesPerUser)
				// -1 means unlimited (skip device limit check)
				if maxDevices > 0 && store.DeviceCount(validToken) >= maxDevices {
					msg := fmt.Sprintf("Лимит устройств (%d). Отвяжите старое через бота:\n/devices", maxDevices)
					writeJSON(w, http.StatusOK, accsdbResponse(msg))
					return
				}

				// Auto-bind: just add the device and let through
				label := deviceLabelFromUA(r.UserAgent())
				dev := tgauth.DeviceInfo{
					UID:      uid,
					Label:    label,
					BoundAt:  time.Now().UTC(),
					LastSeen: time.Now().UTC(),
					LastIP:   clientIP(r),
				}
				if fp := r.URL.Query().Get("fp"); fp != "" {
					dev.Fingerprint = fp
				}
				added, addErr := store.AddDevice(validToken, dev)
				if errors.Is(addErr, tgauth.ErrUIDCrossToken) {
					// UID is bound to another token (cub-backup clone scenario).
					// Don't bind, but don't break playback — auth_gate.js will
					// receive uid_conflict on the next /tg/auth/status poll
					// (cookie path checks cross-token UID) and regenerate.
					log.Warn().Str("uid", uid).Str("token_prefix", validToken[:min(8, len(validToken))]).
						Str("ip", clientIP(r)).Msg("tgauth-gate: UID belongs to another token, skipping bind")
				} else if errors.Is(addErr, tgauth.ErrDeviceRevoked) {
					// Matched a revocation the pre-check above missed (e.g. a
					// stable fp the client sent only here). Same outcome: no token.
					log.Warn().Str("uid", uid).Str("ip", clientIP(r)).Msg("tgauth-gate: device was unbound by the user, refusing bind")
					w.Header().Del("Set-Cookie")
					clearAuthCookies(w, r)
					writeJSON(w, http.StatusOK, accsdbResponse("Устройство было отвязано. Авторизуйтесь заново через бота"))
					return
				} else if added && bot != nil {
					// Notify user in TG about new device (only if actually added)
					t, _ := store.Lookup(validToken)
					if t != nil && t.TelegramID != 0 {
						bot.NotifyNewDeviceBound(t.TelegramID, label, uid)
					}
				}

				next.ServeHTTP(w, r)
				return
			}

			// Case B: No valid token
			if uid != "" {
				// Try auto-reauth: check if this uid is bound to any user.
				// SECURITY: verify fingerprint matches stored device fp before
				// auto-issuing the token. Otherwise anyone holding a UID (or
				// having restored a cub backup) silently gets the bound token.
				if tok, dev, ok := store.FindDeviceByUID(uid); ok {
					reqFP := r.URL.Query().Get("fp")
					allow, _, why := uidRestoreVerdict(store, dev, uid, reqFP, clientIP(r))
					if allow {
						setAuthCookies(w, r, tok)
						store.UpdateLastSeen(tok, uid)
						store.UpdateDeviceIP(tok, uid, clientIP(r))
						if reqFP != "" {
							store.UpdateDeviceFingerprint(tok, uid, reqFP)
						}
						next.ServeHTTP(w, r)
						return
					}
					log.Warn().Str("uid", uid).Str("ip", clientIP(r)).Str("why", why).
						Msg("tgauth-gate: по uid сессию не восстанавливаем")
					// Fall through — pending creation path will set uid_conflict.
				}

				// ── Fingerprint-based auto-reauth ──
				// UID unknown (localStorage wiped), try fingerprint match: precise
				// fp first, then the coarse stable fp (survives a firmware update
				// that shifted canvas/WebGL). Both refuse ambiguous cross-account
				// matches, so a coarse collision falls through to the QR gate
				// rather than logging the user into a stranger's account.
				reqFP := r.URL.Query().Get("fp")
				reqSFP := r.URL.Query().Get("sfp")
				var recToken, recVia string
				var recOldUID string
				if reqFP != "" {
					if fpToken, fpDev, unique := store.FindTokenByDeviceFingerprint(reqFP); unique && fpToken != "" {
						// A lone fp match is a model match, not a device match (see
						// the status handler) — require same network or native id.
						if fpRecoveryCorroborated(fpDev, reqSFP, clientIP(r)) {
							recToken, recVia, recOldUID = fpToken, "fingerprint", fpDev.UID
						} else {
							log.Info().Str("uid", uid).Str("ip", clientIP(r)).
								Msg("tgauth-gate: fingerprint matches an account but from another network and without native id, not restoring")
						}
					}
				}
				if recToken == "" && reqSFP != "" {
					if sfpToken, sfpDev, unique := store.FindTokenByStableFP(reqSFP); unique && sfpToken != "" {
						recToken, recVia, recOldUID = sfpToken, "stable-fp", sfpDev.UID
					}
				}
				if recToken != "" {
					_ = store.MigrateDeviceUID(recToken, recOldUID, uid, clientIP(r))
					if reqFP != "" {
						store.UpdateDeviceFingerprint(recToken, uid, reqFP)
					}
					if reqSFP != "" {
						store.UpdateDeviceStableFP(recToken, uid, reqSFP)
					}
					setAuthCookies(w, r, recToken)
					log.Info().
						Str("old_uid", recOldUID).Str("new_uid", uid).Str("via", recVia).
						Msg("tgauth-gate: auto-reauth via device anchor, UID migrated")
					if bot != nil {
						if t, _ := store.Lookup(recToken); t != nil && t.TelegramID != 0 {
							label := deviceLabelFromUA(r.UserAgent())
							bot.NotifyDeviceMigrated(t.TelegramID, label, recOldUID, uid)
						}
					}
					next.ServeHTTP(w, r)
					return
				}
			}

			// Not authenticated — UID-based auth for Lampa, IP-based fallback for browser.
			ip := clientIP(r)
			isLampa := isLampaRequest(r)
			log.Debug().Str("ip", ip).Str("path", path).Str("uid", uid).Bool("lampa", isLampa).Msg("tgauth-gate: no valid token")

			if pending != nil && uid != "" {
				// ── UID-based pending (primary path for Lampa) ──
				// NAT-safe: each device gets its own pending by UID, no IP collisions.
				if existing, ok := pending.FindByUID(uid); ok {
					if existing.Status == tgauth.StatusApproved && existing.Token != "" {
						log.Info().Str("uid", uid).Str("token", existing.Token[:min(8, len(existing.Token))]+"...").Msg("tgauth-gate: approved pending found by UID")
						setAuthCookies(w, r, existing.Token)
						// The owner just confirmed this very device in the bot —
						// that lifts any earlier unbind of it.
						store.ApproveDevice(existing.Token, uid)

						// ── Label-based migration before adding ──
						// Check if same device type already exists — migrate UID instead of adding.
						label := deviceLabelFromUA(r.UserAgent())
						fp := r.URL.Query().Get("fp")

						migrated := false
						// Try fingerprint match first
						if fp != "" {
							if fpDev := store.FindDeviceByFingerprint(existing.Token, fp); fpDev != nil {
								_ = store.MigrateDeviceUID(existing.Token, fpDev.UID, uid, ip)
								migrated = true
							}
						}
						// Label match — only when neither side has a fingerprint (same rule as
						// the gate above: differing fingerprints = different physical devices).
						if !migrated && label != "" && fp == "" {
							if lblDev := store.FindDeviceByLabel(existing.Token, label); lblDev != nil && lblDev.Fingerprint == "" {
								_ = store.MigrateDeviceUID(existing.Token, lblDev.UID, uid, ip)
								migrated = true
							}
						}

						if !migrated {
							// Device limit check (same as Case A) — uses
							// EFFECTIVE group (premium overlay aware).
							maxDevices := effectiveDeviceLimit(store, existing.Token, cfg.TelegramAuth.MaxDevicesPerUser)
							if maxDevices > 0 && store.DeviceCount(existing.Token) >= maxDevices {
								msg := fmt.Sprintf("Лимит устройств (%d). Отвяжите старое через бота:\n/devices", maxDevices)
								writeJSON(w, http.StatusOK, accsdbResponse(msg))
								pending.Consume(existing.Code)
								return
							}

							// Bind new device
							dev := tgauth.DeviceInfo{
								UID:      uid,
								Label:    label,
								BoundAt:  time.Now().UTC(),
								LastSeen: time.Now().UTC(),
								LastIP:   ip,
							}
							if fp != "" {
								dev.Fingerprint = fp
							}
							added, addErr := store.AddDevice(existing.Token, dev)
							if errors.Is(addErr, tgauth.ErrUIDCrossToken) {
								log.Warn().Str("uid", uid).Str("token_prefix", existing.Token[:min(8, len(existing.Token))]).
									Msg("tgauth-gate: post-approval bind — UID belongs to another token, skipping bind")
							} else if added && bot != nil {
								if t, _ := store.Lookup(existing.Token); t != nil && t.TelegramID != 0 {
									bot.NotifyNewDeviceBound(t.TelegramID, label, uid)
								}
							}
						}

						pending.Consume(existing.Code)
						next.ServeHTTP(w, r)
						return
					}

					// Still waiting/claimed — show the QR auth card with the
					// existing code; online.js polls /tg/auth/status for approval.
					writeJSON(w, http.StatusOK, accsdbAuthRequired(existing.Code, cfg.TelegramAuth.BotName))
					return
				}

				// No existing pending for this UID — create new one
				req := pending.CreateForUID(uid, ip)
				log.Info().Str("uid", uid).Str("ip", ip).Str("code", req.Code).Msg("tgauth-gate: new pending code for UID")
				writeJSON(w, http.StatusOK, accsdbAuthRequired(req.Code, cfg.TelegramAuth.BotName))
				return
			}

			if pending != nil && uid == "" && isLampa {
				// ── IP-based fallback for old Lampa clients without uid ──
				if existing, ok := pending.FindByClientIP(ip); ok {
					if existing.Status == tgauth.StatusApproved && existing.Token != "" {
						setAuthCookies(w, r, existing.Token)
						// uid-less pairing: the approval is keyed by client IP so a
						// previously unbound device from this address may re-attach.
						store.ApproveDeviceFromIP(existing.Token, ip)
						pending.Consume(existing.Code)
						next.ServeHTTP(w, r)
						return
					}
					writeJSON(w, http.StatusOK, accsdbAuthRequired(existing.Code, cfg.TelegramAuth.BotName))
					return
				}
				req := pending.Create(ip)
				log.Info().Str("ip", ip).Str("code", req.Code).Msg("tgauth-gate: new pending code for Lampa (no UID)")
				writeJSON(w, http.StatusOK, accsdbAuthRequired(req.Code, cfg.TelegramAuth.BotName))
				return
			}

			// Browser or unknown API client — redirect or JSON error
			if isLampa || isAPIRequest(r) ||
				r.Header.Get("X-Requested-With") == "XMLHttpRequest" ||
				strings.Contains(r.Header.Get("Accept"), "application/json") {
				writeJSON(w, http.StatusOK, accsdbResponse("Требуется авторизация"))
				return
			}
			http.Redirect(w, r, "/tg/auth", http.StatusFound)
		})
	}
}

// attachProofKey кладёт в ответ ПЕРСОНАЛЬНЫЙ ключ аттестации этого устройства.
// Выдаём только тому, кто уже доказал владение токеном И назвал свой uid — то
// есть по тому же каналу, по которому и так ездит токен доступа. Ключ заменяет
// общий секрет, зашитый во все клиенты: тот лежал открытым текстом в бандле и
// в APK, и подделать подпись мог кто угодно.
func attachProofKey(resp map[string]any, store *tgauth.Store, token, uid string) {
	if store == nil || token == "" || uid == "" {
		return
	}
	if key, ok := store.EnsureProofKey(token, uid); ok {
		resp["proof_key"] = key
		resp["proof_uid"] = uid
	}
}

// streamTokenOf достаёт tgauth-токен запроса тем же порядком, что и
// iptvhttp.streamAuthToken: ?token= главнее, кука lampac_token — фолбэк.
// Нужен для резолва лимита одновременных потоков на IPTV-ручках.
func streamTokenOf(r *http.Request) string {
	if t := strings.TrimSpace(r.URL.Query().Get("token")); t != "" {
		return t
	}
	return auth.ExtractToken(r)
}

// tgServerMaxDevices mirrors cfg.TelegramAuth.MaxDevicesPerUser for handlers
// that are wired without the config (the /tg/auth/status rung). Set once at
// server start; 0 falls back to the hardcoded default inside effectiveDeviceLimit.
var tgServerMaxDevices int

// effectiveDeviceLimit resolves the device cap for a token: personal limit,
// then the EFFECTIVE group's limit (premium overlay aware), then the server
// default, then 3. Returns -1 for unlimited.
func effectiveDeviceLimit(store *tgauth.Store, token string, serverDefault int) int {
	maxDevices := store.GetMaxDevices(token) // personal limit (-1 = unlimited)
	if maxDevices == 0 && groupStoreRef != nil && tgTokenStoreRef != nil {
		gid := tgTokenStoreRef.GetEffectiveGroup(token, currentPremiumGroupID())
		if g, _ := groupStoreRef.Get(gid); g.MaxDevices != 0 {
			maxDevices = g.MaxDevices
		}
	}
	if maxDevices == 0 {
		maxDevices = serverDefault
	}
	if maxDevices == 0 {
		maxDevices = 3 // hardcoded fallback
	}
	return maxDevices
}

// fpRecoveryCorroborated decides whether a precise-fingerprint match may
// restore an account. The fp hashes screen, GPU, canvas and audio — every unit
// of the same TV or PC model produces the same value, so on its own it only
// says "same model". It counts as the same device when either the request
// comes from the network the device was last seen on, or the client also
// presents a stable fp carrying a NATIVE device id that matches the record.
// uidRestoreVerdict решает, можно ли восстановить сессию по одному лишь uid, и
// возвращает причину — она уходит в лог, чтобы отказы можно было пересчитать.
//
// ★Правило «нет отпечатка — значит клон» (15.08.2026) неверно в посылке. Замер
// 20.09.2026 по логу nginx: отпечаток шлют только плагины, которые строят URL
// сами (/lite — 10 924 запроса из 10 947, /lifeevents — 9 880 из 9 893), а
// синхронизация не шлёт его ВОВСЕ: /timecode 0 из 2 499, /bookmark 0 из 359,
// /storage 0 из 571. То есть половина запросов ОДНОГО устройства выглядела
// клоном: вместо данных Лампа получала приглашение с QR-кодом, а по
// uid_conflict клиент ещё и перегенерировал свой uid. Отсюда петли в журнале —
// 881 отказ за 2,5 часа от 47 устройств.
//
// Отказываем там, где сигнал ЕСТЬ и он против нас:
//   - uid записан у нескольких токенов — по нему нельзя опознать никого;
//   - отпечаток прислан и не совпал — это другое железо;
//   - отпечатка нет, а у записанного устройства он есть — пускаем только из той
//     же сети, где устройство видели в прошлый раз (та же подпорка, что и у
//     восстановления по отпечатку: чужой uid из лога почти никогда не приходит
//     с /24 владельца).
// Второе возвращаемое значение — «доказано, что этим uid пользуется не одно
// устройство». Только по нему клиента просят ПЕРЕГЕНЕРИРОВАТЬ uid: иначе
// честное устройство выбрасывало рабочий идентификатор на каждом запросе без
// отпечатка, теряло привязку и шло на QR по кругу.
func uidRestoreVerdict(store *tgauth.Store, dev *tgauth.DeviceInfo, uid, reqFP, ip string) (allow, cloned bool, why string) {
	if dev == nil {
		return false, false, "нет записи об устройстве"
	}
	if n := store.DeviceUIDOwners(uid); n > 1 {
		return false, true, "uid записан у нескольких аккаунтов"
	}
	if reqFP != "" {
		if dev.Fingerprint == "" || dev.Fingerprint == reqFP {
			return true, false, "отпечаток совпал"
		}
		// Отпечаток прислан и он чужой — этим uid пользуется второе железо.
		// Здесь перегенерация как раз и нужна: она не даёт коллизии записаться.
		return false, true, "отпечаток не совпал"
	}
	if dev.Fingerprint == "" {
		return true, false, "отпечатка нет ни в запросе, ни в записи"
	}
	if sameNetwork(dev.LastIP, ip) {
		return true, false, "без отпечатка, но из той же сети"
	}
	return false, false, "без отпечатка и из другой сети"
}

func fpRecoveryCorroborated(dev *tgauth.DeviceInfo, reqSFP, ip string) bool {
	if dev == nil {
		return false
	}
	if reqSFP != "" && strings.HasPrefix(reqSFP, tgauth.StableFPNativePrefix) && dev.StableFP == reqSFP {
		return true
	}
	return sameNetwork(dev.LastIP, ip)
}

// sameNetwork reports whether two addresses share a /24 (IPv4) or /48 (IPv6)
// prefix — the granularity of one household or one small ISP pool.
func sameNetwork(a, b string) bool {
	ipA, ipB := net.ParseIP(strings.TrimSpace(a)), net.ParseIP(strings.TrimSpace(b))
	if ipA == nil || ipB == nil {
		return false
	}
	if a4, b4 := ipA.To4(), ipB.To4(); a4 != nil || b4 != nil {
		if a4 == nil || b4 == nil {
			return false
		}
		return a4[0] == b4[0] && a4[1] == b4[1] && a4[2] == b4[2]
	}
	return ipA.Mask(net.CIDRMask(48, 128)).Equal(ipB.Mask(net.CIDRMask(48, 128)))
}

// accsdbResponse builds the standard CUB/Lampa auth-required JSON response.
// Includes "results":[] so that client code doing response.results.forEach(...)
// doesn't crash with "Cannot read properties of undefined (reading 'forEach')".
// Lampa detects "accsdb":true and shows the auth dialog.
func accsdbResponse(msg string) map[string]any {
	return map[string]any{
		"accsdb":  true,
		"msg":     msg,
		"results": []any{},
	}
}

// accsdbAuthRequired is the source-discovery auth wall shown to an
// unauthenticated client. It's a superset of accsdbResponse: alongside the
// plain-text msg (fallback for clients that only render accsdb.msg) it carries
// the TG pending code, bot handle, deep link and a server-rendered QR (PNG
// data-URI). online.js detects accsdb+code and renders the QR auth card (scan
// QR or enter the code in the bot), then polls /tg/auth/status until approved.
func accsdbAuthRequired(code, botName string) map[string]any {
	deepLink := ""
	if botName != "" && code != "" {
		deepLink = fmt.Sprintf("https://telegram.me/%s?start=%s", botName, code)
	}
	msg := fmt.Sprintf("Для просмотра требуется авторизация. Отсканируйте QR-код или отправьте код %s боту @%s", code, botName)
	resp := accsdbResponse(msg)
	resp["code"] = code
	resp["bot"] = "@" + botName
	if deepLink != "" {
		resp["deep_link"] = deepLink
		if qr := generateQRDataURI(deepLink); qr != "" {
			resp["qr"] = qr
		}
	}
	return resp
}

// isLampaRequest detects requests coming from Lampa app (AJAX/XHR to API endpoints).
// Lampa always sends XMLHttpRequest and requests JSON from /lite/* and /lifeevents endpoints.
func isLampaRequest(r *http.Request) bool {
	// XMLHttpRequest header (Lampa uses jQuery/fetch for API calls)
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	// Accept: application/json
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		return true
	}
	// Lampa-specific paths that expect JSON (not HTML redirect)
	p := r.URL.Path
	if strings.HasPrefix(p, "/lite/") || strings.HasPrefix(p, "/lifeevents") ||
		strings.HasPrefix(p, "/externalids") || strings.HasPrefix(p, "/checksearch") ||
		strings.HasPrefix(p, "/api/") ||
		strings.HasPrefix(p, "/cub/") || strings.HasPrefix(p, "/cubproxy/") ||
		strings.HasPrefix(p, "/ts/") || strings.HasPrefix(p, "/sisi") {
		return true
	}
	// rjson parameter
	if r.URL.Query().Get("rjson") == "true" {
		return true
	}
	return false
}

// isAPIRequest detects non-browser clients (Lampa TV, curl, etc.)
func isAPIRequest(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		return true
	}
	ua := strings.ToLower(r.UserAgent())
	if strings.Contains(ua, "tizen") || strings.Contains(ua, "webos") ||
		strings.Contains(ua, "android tv") || strings.Contains(ua, "firetv") {
		return true
	}
	// Lampa client typically sends rjson=true
	if r.URL.Query().Get("rjson") == "true" {
		return true
	}
	return false
}
