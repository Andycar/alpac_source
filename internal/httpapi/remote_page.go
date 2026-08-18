package httpapi

import (
	"net/http"
	"strings"

	"lampac-go/internal/config"
)

// remotePageHandler serves the Telegram Mini App Remote Control page.
// GET /remote
func remotePageHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := hostFromRequest(r)
		// Build WebSocket URL: wss:// for https, ws:// for http.
		wsScheme := "wss"
		if strings.HasPrefix(host, "http://") {
			wsScheme = "ws"
		}
		wsURL := wsScheme + "://" + strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://") + "/nws"

		html := strings.ReplaceAll(remoteWebAppHTML, "{WS_URL}", wsURL)
		html = strings.ReplaceAll(html, "{API_BASE}", host)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(html))
	}
}

const remoteWebAppHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0,maximum-scale=1.0,user-scalable=no">
<title>Remote</title>
<script src="https://telegram.org/js/telegram-web-app.js"></script>
<style>
:root {
  --bg: #0a0a0a;
  --card: #1c1c1e;
  --card2: #2c2c2e;
  --text: #f5f5f7;
  --text2: #86868b;
  --accent: #0a84ff;
  --accent-glow: rgba(10,132,255,0.35);
  --green: #30d158;
  --red: #ff453a;
  --radius: 16px;
  --btn-size: 56px;
}
* { box-sizing: border-box; margin: 0; padding: 0; }
html, body { height: 100%; overflow: hidden; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'SF Pro Display', 'Segoe UI', Roboto, sans-serif;
  background: var(--bg);
  color: var(--text);
  -webkit-user-select: none; user-select: none;
  -webkit-tap-highlight-color: transparent;
  display: flex; flex-direction: column;
  max-height: 100vh;
}

/* ── Status Bar ───────────────────────────── */
.status-bar {
  display: flex; align-items: center; gap: 10px;
  padding: 12px 16px 8px;
  min-height: 48px;
}
.status-dot {
  width: 8px; height: 8px; border-radius: 50%;
  background: var(--red);
  transition: background 0.3s;
  flex-shrink: 0;
}
.status-dot.online { background: var(--green); }
.status-title {
  flex: 1; font-size: 14px; font-weight: 600;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.status-page {
  font-size: 12px; color: var(--text2);
  flex-shrink: 0;
}

/* ── Now Playing ──────────────────────────── */
.now-playing {
  margin: 0 16px 8px; padding: 10px 14px;
  background: var(--card); border-radius: 12px;
  display: none; align-items: center; gap: 12px;
}
.now-playing.visible { display: flex; }
.np-info { flex: 1; min-width: 0; }
.np-title {
  font-size: 13px; font-weight: 600;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.np-time { font-size: 11px; color: var(--text2); margin-top: 2px; }
.np-progress {
  width: 100%; height: 3px; background: var(--card2);
  border-radius: 2px; margin-top: 6px; overflow: hidden;
}
.np-progress-bar {
  height: 100%; background: var(--accent); border-radius: 2px;
  transition: width 0.5s linear; width: 0%;
}

/* ── Search ───────────────────────────────── */
.search-bar {
  display: flex; align-items: center; gap: 8px;
  margin: 0; padding: 0 14px;
  background: var(--card); border-radius: 12px;
  height: 44px; width: 100%;
}
.search-bar svg { flex-shrink: 0; opacity: 0.5; }
.search-bar input {
  flex: 1; background: none; border: none; outline: none;
  color: var(--text); font-size: 15px;
  font-family: inherit;
}
.search-bar input::placeholder { color: var(--text2); }
.voice-btn {
  width: 36px; height: 36px; border-radius: 50%;
  background: none; border: none; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  color: var(--text2); transition: all 0.2s;
}
.voice-btn:active, .voice-btn.listening { color: var(--red); }
.voice-btn.listening { animation: pulse 1s infinite; }
@keyframes pulse {
  0%, 100% { transform: scale(1); }
  50% { transform: scale(1.15); }
}

/* ── Main Content ────────────────────────── */
.main-content {
  flex: 1; display: flex; flex-direction: column;
  justify-content: center;
  align-items: center; gap: 20px;
  padding: 8px 16px 0;
  min-height: 0;
}

/* D-Pad + Player wrapper */
.controls {
  display: flex; flex-direction: column;
  align-items: center; gap: 20px;
}

/* D-Pad */
.dpad {
  display: grid;
  grid-template-columns: var(--btn-size) var(--btn-size) var(--btn-size);
  grid-template-rows: var(--btn-size) var(--btn-size) var(--btn-size);
  gap: 6px;
}
.dpad-btn {
  width: var(--btn-size); height: var(--btn-size);
  border-radius: 14px; border: none; cursor: pointer;
  background: var(--card); color: var(--text);
  display: flex; align-items: center; justify-content: center;
  font-size: 20px; transition: all 0.1s;
  -webkit-tap-highlight-color: transparent;
}
.dpad-btn:active {
  background: var(--card2); transform: scale(0.92);
}
.dpad-btn.center {
  background: var(--accent); border-radius: 16px;
  font-size: 13px; font-weight: 700; letter-spacing: 0.5px;
}
.dpad-btn.center:active {
  background: #0070e0; transform: scale(0.92);
}
.dpad-btn.empty { visibility: hidden; }

/* ── Player Bar ───────────────────────────── */
.player-bar {
  display: flex; align-items: center; justify-content: center;
  gap: 6px; padding: 0 16px;
}
.p-btn {
  width: 48px; height: 48px;
  border-radius: 50%; border: none; cursor: pointer;
  background: var(--card); color: var(--text);
  display: flex; align-items: center; justify-content: center;
  transition: all 0.1s;
}
.p-btn:active { background: var(--card2); transform: scale(0.9); }
.p-btn.play-pause {
  width: 56px; height: 56px;
  background: var(--accent);
}
.p-btn.play-pause:active { background: #0070e0; }

/* ── Quick Actions ────────────────────────── */
.quick-actions {
  display: flex; align-items: center; justify-content: center;
  gap: 16px; padding: 8px 16px 12px;
  flex-shrink: 0;
}
.q-btn {
  display: flex; flex-direction: column; align-items: center; gap: 4px;
  background: none; border: none; cursor: pointer;
  color: var(--text2); font-size: 10px; font-weight: 500;
  padding: 8px 12px; border-radius: 12px;
  transition: all 0.15s;
}
.q-btn:active { background: var(--card); color: var(--text); }
.q-btn svg { width: 22px; height: 22px; }

/* ── Bottom Safe Area ─────────────────────── */
.safe-bottom { height: env(safe-area-inset-bottom, 0); flex-shrink: 0; }

/* ── Haptic feedback visual ───────────────── */
@keyframes flash { 0%,100%{opacity:1}50%{opacity:0.5} }
</style>
</head>
<body>

<!-- Status bar -->
<div class="status-bar">
  <div class="status-dot" id="statusDot"></div>
  <div class="status-title" id="statusTitle">Connecting...</div>
  <div class="status-page" id="statusPage"></div>
</div>

<!-- Now playing -->
<div class="now-playing" id="nowPlaying">
  <div class="np-info">
    <div class="np-title" id="npTitle"></div>
    <div class="np-time" id="npTime"></div>
    <div class="np-progress"><div class="np-progress-bar" id="npProgress"></div></div>
  </div>
</div>

<!-- Main content (centered group) -->
<div class="main-content">

<!-- Search bar -->
<div class="search-bar">
  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.35-4.35"/></svg>
  <input type="text" id="searchInput" placeholder="Search..." enterkeyhint="search">
  <button class="voice-btn" id="voiceBtn" title="Voice search">
    <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M12 14c1.66 0 3-1.34 3-3V5c0-1.66-1.34-3-3-3S9 3.34 9 5v6c0 1.66 1.34 3 3 3zm-1-9c0-.55.45-1 1-1s1 .45 1 1v6c0 .55-.45 1-1 1s-1-.45-1-1V5z"/><path d="M17 11c0 2.76-2.24 5-5 5s-5-2.24-5-5H5c0 3.53 2.61 6.43 6 6.92V21h2v-3.08c3.39-.49 6-3.39 6-6.92h-2z"/></svg>
  </button>
</div>

<!-- Controls -->
<div class="controls">
  <!-- D-Pad -->
  <div class="dpad">
    <div class="dpad-btn empty"></div>
    <button class="dpad-btn" data-cmd="up"><svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor"><path d="M7.41 15.41L12 10.83l4.59 4.58L18 14l-6-6-6 6z"/></svg></button>
    <div class="dpad-btn empty"></div>
    <button class="dpad-btn" data-cmd="left"><svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor"><path d="M15.41 7.41L10.83 12l4.58 4.59L14 18l-6-6 6-6z"/></svg></button>
    <button class="dpad-btn center" data-cmd="enter">OK</button>
    <button class="dpad-btn" data-cmd="right"><svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor"><path d="M10 6L8.59 7.41 13.17 12l-4.58 4.59L10 18l6-6z"/></svg></button>
    <div class="dpad-btn empty"></div>
    <button class="dpad-btn" data-cmd="down"><svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor"><path d="M7.41 8.59L12 13.17l4.59-4.58L18 10l-6 6-6-6z"/></svg></button>
    <div class="dpad-btn empty"></div>
  </div>

  <!-- Player bar -->
  <div class="player-bar">
    <button class="p-btn" data-cmd="rewind" title="Rewind 30s">
      <svg width="22" height="22" viewBox="0 0 24 24" fill="currentColor"><path d="M11.99 5V1l-5 5 5 5V7c3.31 0 6 2.69 6 6s-2.69 6-6 6-6-2.69-6-6h-2c0 4.42 3.58 8 8 8s8-3.58 8-8-3.58-8-8-8z"/><text x="10" y="16" font-size="7" text-anchor="middle" fill="currentColor" font-weight="bold">30</text></svg>
    </button>
    <button class="p-btn" data-cmd="stop" title="Stop">
      <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>
    </button>
    <button class="p-btn play-pause" data-cmd="toggle" title="Play/Pause" id="playPauseBtn">
      <svg width="26" height="26" viewBox="0 0 24 24" fill="currentColor" id="playIcon"><path d="M8 5v14l11-7z"/></svg>
    </button>
    <button class="p-btn" data-cmd="back" title="Back">
      <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z"/></svg>
    </button>
    <button class="p-btn" data-cmd="forward" title="Forward 30s">
      <svg width="22" height="22" viewBox="0 0 24 24" fill="currentColor"><path d="M18 13c0 3.31-2.69 6-6 6s-6-2.69-6-6h-2c0 4.42 3.58 8 8 8s8-3.58 8-8-3.58-8-8-8V1l-5 5 5 5V7c3.31 0 6 2.69 6 6z"/><text x="14" y="16" font-size="7" text-anchor="middle" fill="currentColor" font-weight="bold">30</text></svg>
    </button>
  </div>
</div>

<!-- Quick actions -->
<div class="quick-actions">
  <button class="q-btn" data-cmd="home">
    <svg viewBox="0 0 24 24" fill="currentColor"><path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8z"/></svg>
    Home
  </button>
  <button class="q-btn" data-cmd="mute">
    <svg viewBox="0 0 24 24" fill="currentColor"><path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/></svg>
    Mute
  </button>
  <button class="q-btn" data-cmd="fullscreen">
    <svg viewBox="0 0 24 24" fill="currentColor"><path d="M7 14H5v5h5v-2H7v-3zm-2-4h2V7h3V5H5v5zm12 7h-3v2h5v-5h-2v3zM14 5v2h3v3h2V5h-5z"/></svg>
    Fullscreen
  </button>
  <button class="q-btn" data-cmd="settings">
    <svg viewBox="0 0 24 24" fill="currentColor"><path d="M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58c.18-.14.23-.41.12-.61l-1.92-3.32c-.12-.22-.37-.29-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54c-.04-.24-.24-.41-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.07.62-.07.94s.02.64.07.94l-2.03 1.58c-.18.14-.23.41-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z"/></svg>
    Settings
  </button>
</div>

</div><!-- /main-content -->

<div class="safe-bottom"></div>

<script>
(function() {
  'use strict';

  var WS_URL = '{WS_URL}';
  var API_BASE = '{API_BASE}';

  var tg = window.Telegram && window.Telegram.WebApp;
  var ws = null;
  var uid = '';
  var allUids = [];
  var connID = '';
  var reconnectTimer = null;
  var lastStatus = {};
  var deviceFound = false;
  var probeTimer = null;

  // ── TG Theme ─────────────────────────────
  function applyTGTheme() {
    if (!tg || !tg.themeParams) return;
    var p = tg.themeParams;
    if (p.bg_color) document.documentElement.style.setProperty('--bg', p.bg_color);
    if (p.secondary_bg_color) {
      document.documentElement.style.setProperty('--card', p.secondary_bg_color);
    }
    if (p.text_color) document.documentElement.style.setProperty('--text', p.text_color);
    if (p.hint_color) document.documentElement.style.setProperty('--text2', p.hint_color);
    if (p.button_color) document.documentElement.style.setProperty('--accent', p.button_color);
  }

  // ── Haptic ───────────────────────────────
  function haptic(type) {
    try {
      if (tg && tg.HapticFeedback) {
        if (type === 'light') tg.HapticFeedback.impactOccurred('light');
        else if (type === 'medium') tg.HapticFeedback.impactOccurred('medium');
        else tg.HapticFeedback.impactOccurred('rigid');
      }
    } catch(e) {}
  }

  // ── Auth ──────────────────────────────────
  function authenticate() {
    var initData = tg ? tg.initData : '';
    if (!initData) return Promise.resolve(null);

    return fetch(API_BASE + '/api/remote/auth', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Telegram-Init-Data': initData
      }
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
      return {
        uid: data.uid || '',
        uids: data.uids || [],
        connected: data.connected || 0
      };
    })
    .catch(function() { return null; });
  }

  // ── WebSocket ─────────────────────────────
  function connect() {
    if (ws && ws.readyState <= 1) return;

    ws = new WebSocket(WS_URL);

    ws.onopen = function() {
      clearReconnect();
      setOnline(true);
    };

    ws.onmessage = function(e) {
      try {
        var msg = JSON.parse(e.data);
        if (msg.method === 'Connected') {
          connID = msg.args && msg.args[0] || '';
          // Start device discovery — try each UID to find a live device
          discoverDevice();
        }
        if (msg.method === 'event') {
          var evName = msg.args && msg.args[1];
          var evData = msg.args && msg.args[2];
          if (evName === 'remote:status') {
            // Device responded — mark as found and stop probing
            if (!deviceFound) {
              deviceFound = true;
              if (probeTimer) { clearTimeout(probeTimer); probeTimer = null; }
            }
            handleStatus(typeof evData === 'string' ? JSON.parse(evData) : evData);
          }
        }
      } catch(ex) {}
    };

    ws.onclose = function() {
      setOnline(false);
      deviceFound = false;
      scheduleReconnect();
    };

    ws.onerror = function() {
      setOnline(false);
    };
  }

  // ── Device Discovery ─────────────────────
  // Try each UID sequentially. Register, send getstatus probe,
  // wait for response. First UID that responds is the right one.
  function discoverDevice() {
    if (allUids.length === 0) {
      setStatus('No devices');
      return;
    }

    deviceFound = false;
    var idx = 0;

    function tryUID() {
      if (deviceFound) return; // already found
      if (idx >= allUids.length) {
        // No device responded — retry with first UID (server may have
        // sorted connected-first), and keep periodic probes.
        uid = allUids[0];
        wsSend('RegistryEvent', uid);
        setStatus('Waiting...');
        // Retry probing every 5 seconds
        probeTimer = setTimeout(function probeLoop() {
          if (deviceFound) return;
          wsSend('events', uid, 'remote:getstatus', '{}');
          probeTimer = setTimeout(probeLoop, 5000);
        }, 5000);
        return;
      }

      uid = allUids[idx];
      wsSend('RegistryEvent', uid);
      wsSend('events', uid, 'remote:getstatus', '{}');
      setStatus('Searching... (' + (idx + 1) + '/' + allUids.length + ')');

      // Wait 2s for response before trying next UID
      probeTimer = setTimeout(function() {
        idx++;
        tryUID();
      }, 2000);
    }

    tryUID();
  }

  function wsSend(method) {
    if (!ws || ws.readyState !== 1) return;
    var args = Array.prototype.slice.call(arguments, 1);
    ws.send(JSON.stringify({ method: method, args: args }));
  }

  function scheduleReconnect() {
    if (reconnectTimer) return;
    reconnectTimer = setTimeout(function() {
      reconnectTimer = null;
      connect();
    }, 3000);
  }

  function clearReconnect() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  }

  // ── Send Command ──────────────────────────
  function sendCommand(cmd, data) {
    if (!uid) return;
    var payload = data ? JSON.stringify(data) : '{}';
    wsSend('events', uid, 'remote:' + cmd, payload);
    haptic('light');
  }

  // ── Status ────────────────────────────────
  function setStatus(text) {
    document.getElementById('statusTitle').textContent = text;
  }

  function setOnline(online) {
    var dot = document.getElementById('statusDot');
    if (online) {
      dot.classList.add('online');
      if (!deviceFound) setStatus('Searching...');
      else setStatus(lastStatus.title || 'Connected');
    } else {
      dot.classList.remove('online');
      setStatus('Reconnecting...');
    }
  }

  function handleStatus(status) {
    lastStatus = status;
    var dot = document.getElementById('statusDot');
    var page = document.getElementById('statusPage');
    var np = document.getElementById('nowPlaying');
    var npTitle = document.getElementById('npTitle');
    var npTime = document.getElementById('npTime');
    var npProgress = document.getElementById('npProgress');
    var playIcon = document.getElementById('playIcon');

    dot.classList.add('online');
    setStatus(status.title || 'Lampa');
    page.textContent = status.page || '';

    if (status.duration > 0) {
      // Позицию клиенты называют по-разному: плагин Lampa шлёт currentTime, web-клиент — current.
      // Читали только первое, поэтому у web таймер и полоса считались от undefined → NaN.
      var pos = typeof status.currentTime === 'number' ? status.currentTime
              : (typeof status.current === 'number' ? status.current : 0);
      np.classList.add('visible');
      npTitle.textContent = status.title || '';
      npTime.textContent = formatTime(pos) + ' / ' + formatTime(status.duration);
      npProgress.style.width = Math.round((pos / status.duration) * 100) + '%';
    } else {
      np.classList.remove('visible');
    }

    // Update play/pause icon
    if (status.playing) {
      playIcon.innerHTML = '<path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/>';
    } else {
      playIcon.innerHTML = '<path d="M8 5v14l11-7z"/>';
    }
  }

  function formatTime(s) {
    if (!s || s < 0) return '0:00';
    var h = Math.floor(s / 3600);
    var m = Math.floor((s % 3600) / 60);
    var sec = s % 60;
    if (h > 0) return h + ':' + pad(m) + ':' + pad(sec);
    return m + ':' + pad(sec);
  }
  function pad(n) { return n < 10 ? '0' + n : '' + n; }

  // ── Search ────────────────────────────────
  var searchInput = document.getElementById('searchInput');
  searchInput.addEventListener('keydown', function(e) {
    if (e.key === 'Enter' || e.keyCode === 13) {
      var q = searchInput.value.trim();
      if (q) {
        sendCommand('search', { query: q });
        searchInput.blur();
        searchInput.value = '';
      }
    }
  });

  // ── Voice ─────────────────────────────────
  var voiceBtn = document.getElementById('voiceBtn');
  var recognition = null;

  if ('webkitSpeechRecognition' in window || 'SpeechRecognition' in window) {
    var SpeechRecognition = window.SpeechRecognition || window.webkitSpeechRecognition;
    recognition = new SpeechRecognition();
    recognition.lang = 'ru-RU';
    recognition.interimResults = false;
    recognition.maxAlternatives = 1;

    recognition.onresult = function(e) {
      var text = e.results[0][0].transcript;
      if (text) {
        searchInput.value = text;
        sendCommand('search', { query: text });
        searchInput.value = '';
      }
      voiceBtn.classList.remove('listening');
    };
    recognition.onerror = function() { voiceBtn.classList.remove('listening'); };
    recognition.onend = function() { voiceBtn.classList.remove('listening'); };

    voiceBtn.addEventListener('click', function() {
      haptic('medium');
      if (voiceBtn.classList.contains('listening')) {
        recognition.stop();
      } else {
        voiceBtn.classList.add('listening');
        recognition.start();
      }
    });
  } else {
    voiceBtn.style.display = 'none';
  }

  // ── Button Handlers ───────────────────────
  document.querySelectorAll('[data-cmd]').forEach(function(btn) {
    btn.addEventListener('click', function() {
      sendCommand(btn.dataset.cmd);
    });
  });

  // ── Long press for repeating d-pad ────────
  var repeatTimer = null;
  var repeatCmds = ['up', 'down', 'left', 'right'];

  document.querySelectorAll('.dpad-btn[data-cmd]').forEach(function(btn) {
    var cmd = btn.dataset.cmd;
    if (repeatCmds.indexOf(cmd) === -1) return;

    function startRepeat() {
      repeatTimer = setInterval(function() {
        sendCommand(cmd);
        haptic('light');
      }, 200);
    }

    btn.addEventListener('touchstart', function(e) {
      e.preventDefault();
      sendCommand(cmd);
      haptic('light');
      clearInterval(repeatTimer);
      repeatTimer = setTimeout(function() { startRepeat(); }, 400);
    }, { passive: false });

    btn.addEventListener('touchend', function() { clearInterval(repeatTimer); clearTimeout(repeatTimer); });
    btn.addEventListener('touchcancel', function() { clearInterval(repeatTimer); clearTimeout(repeatTimer); });
  });

  // ── Init ──────────────────────────────────
  applyTGTheme();

  if (tg) {
    tg.ready();
    tg.expand();
    tg.BackButton.show();
    tg.BackButton.onClick(function() { tg.close(); });
  }

  authenticate().then(function(result) {
    if (!result || (!result.uid && result.uids.length === 0)) {
      setStatus('Auth error');
      return;
    }
    allUids = result.uids.length > 0 ? result.uids : [result.uid];
    uid = allUids[0];
    connect();
  });

})();
</script>
</body>
</html>
`
