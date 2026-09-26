// Account plugin for Lampa — alcopac/lampac-go user-side auth UI.
//
// Adds a "Аккаунт" entry under Settings with three actions:
//   1. Status row — current user (TG @handle / password login / anon UID),
//      subscription expiry, server-side auth mode.
//   2. "Войти / Перезайти" — opens /tg/auth in Lampa's in-app WebView via
//      Lampa.Utils.openLink. Falls back to location.href on platforms
//      where openLink isn't available.
//   3. "Выйти" — wipes all auth cookies + localStorage tokens + calls the
//      backend logout endpoints, then reloads.
//
// Also installs an Activity.push hook: when the user navigates into an
// "online" (source-picker) screen and we can't see an auth token, a
// Lampa.Noty toast nudges them toward Settings → Account. Belt-and-
// suspenders with the synthetic "_auth_required" balancer the server
// emits via /lite/events when unauthed — that one renders the blue
// banner on the picker, this one fires before the picker even loads.
(function () {
  'use strict';

  if (window.alcopac_account) return;
  window.alcopac_account = true;

  // ---------------------------------------------------------------------
  // Origin detection — copied verbatim from audiobot.js so future
  // refactors keep one source of truth (small, no dependency, robust to
  // CDN/script-tag rewriting).
  // ---------------------------------------------------------------------
  function resolveBase() {
    try {
      var scripts = document.getElementsByTagName('script');
      for (var i = scripts.length - 1; i >= 0; i--) {
        var src = scripts[i].src || '';
        var m = src.match(/^(https?:\/\/[^\/]+)\/account\.js/);
        if (m) return m[1];
      }
    } catch (e) {}
    return location.origin;
  }
  var HOST = resolveBase();

  // ---------------------------------------------------------------------
  // Token discovery
  //
  // When Lampa runs cross-origin (host on lampa.mx, our backend on
  // edge-b.example.net), the browser refuses to send our Set-Cookie back on XHR
  // unless EVERY one of: withCredentials=true, CORS allow-credentials
  // header, exact Access-Control-Allow-Origin match, SameSite=None and
  // Secure is satisfied. In practice forks-of-Lampa break one of these,
  // so /api/auth/whoami silently reports unauthenticated even though
  // the user signed in via the TG bot a moment ago.
  //
  // Fallback path: after a successful TG approval the auth-gate JS we
  // ship in /on.js stores the token in localStorage['lampac_auth_token']
  // (also Lampa.Storage). We forward it to the server as an explicit
  // X-Lampac-Token header — the middleware's resolveUser reads this
  // header alongside the cookie, so the request becomes authenticated
  // even when the cookie was eaten by CORS.
  // ---------------------------------------------------------------------
  // Mirrors syncpro.js getLampacToken — see that file's long comment
  // for why we look in every namespace and storage layer.
  function bestKnownToken() {
    // alpac_token first (our brand), lampac_token second (legacy fallback
    // some external Lampa builds populate). Each name is checked in
    // both Lampa.Storage (the per-session in-memory store our gate
    // writes into via lampac_auth_token, plus any plugin that mirrors
    // the cookie there) and document.cookie.
    var names = ['alpac_token', 'lampac_token'];
    try {
      if (window.Lampa && Lampa.Storage) {
        for (var i = 0; i < names.length; i++) {
          var v = Lampa.Storage.get(names[i], '');
          if (v) return v;
        }
        // The TG auth-gate JS (in /on.js) also stores the raw token under
        // 'lampac_auth_token' — covers the post-login state before any
        // cookie writeback reached Lampa.Storage.
        var t = Lampa.Storage.get('lampac_auth_token', '');
        if (t) return t;
      }
    } catch (e) {}
    try {
      var ls = localStorage.getItem('lampac_auth_token');
      if (ls) return ls;
    } catch (e) {}
    try {
      for (var j = 0; j < names.length; j++) {
        var re = new RegExp('(?:^|;\\s*)' + names[j] + '=([^;]*)');
        var m = document.cookie.match(re);
        if (m && m[1]) return decodeURIComponent(m[1]);
      }
    } catch (e) {}
    return '';
  }

  // Чем клиент себя называет: токен, если он ещё есть, и uid устройства —
  // он переживает потерю куки, а сервер умеет восстановить по нему сессию.
  function deviceQuery() {
    var parts = [];
    try { var tok = bestKnownToken(); if (tok) parts.push('token=' + encodeURIComponent(tok)); } catch (e) {}
    try {
      var uid = Lampa.Storage.get('lampac_unic_id', '') || '';
      if (!uid) { try { uid = localStorage.getItem('lampac_uid_backup') || ''; } catch (e) {} }
      if (uid) parts.push('uid=' + encodeURIComponent(uid));
    } catch (e) {}
    return parts.length ? '?' + parts.join('&') : '';
  }

  // ---------------------------------------------------------------------
  // HTTP — minimal XHR; we don't want to depend on fetch on old WebViews.
  // ---------------------------------------------------------------------
  function getJSON(path, cb) {
    try {
      var xhr = new XMLHttpRequest();
      xhr.open('GET', HOST + path, true);
      xhr.withCredentials = true;
      // X-Lampac-Token header is the cross-origin escape hatch; the
      // server's auth middleware accepts both cookie and header so this
      // is redundant on same-origin and life-saving cross-origin.
      var tok = bestKnownToken();
      if (tok) {
        // X-Alpac-Token is the brand-scoped header (preferred); X-Lampac-Token
        // is the legacy fallback the server also accepts. Both go on so a
        // forked Lampa that only reads one of them still works.
        try { xhr.setRequestHeader('X-Alpac-Token', tok); } catch (e) {}
        try { xhr.setRequestHeader('X-Lampac-Token', tok); } catch (e) {}
      }
      xhr.timeout = 8000;
      xhr.onload = function () {
        try { cb(null, JSON.parse(xhr.responseText)); }
        catch (e) { cb(e); }
      };
      xhr.onerror = function () { cb(new Error('network')); };
      xhr.ontimeout = function () { cb(new Error('timeout')); };
      xhr.send();
    } catch (e) { cb(e); }
  }
  function postEmpty(path) {
    try {
      var xhr = new XMLHttpRequest();
      xhr.open('POST', HOST + path, true);
      xhr.withCredentials = true;
      var tok = bestKnownToken();
      if (tok) {
        // X-Alpac-Token is the brand-scoped header (preferred); X-Lampac-Token
        // is the legacy fallback the server also accepts. Both go on so a
        // forked Lampa that only reads one of them still works.
        try { xhr.setRequestHeader('X-Alpac-Token', tok); } catch (e) {}
        try { xhr.setRequestHeader('X-Lampac-Token', tok); } catch (e) {}
      }
      xhr.timeout = 4000;
      xhr.send();
    } catch (e) {}
  }

  // ---------------------------------------------------------------------
  // Cookie / token helpers
  // ---------------------------------------------------------------------
  function hasLocalAuthToken() {
    try {
      if (/(?:^|;\s*)(lampac_token|alpac_token|_lampac_auth)=/.test(document.cookie)) return true;
      if (window.Lampa && Lampa.Storage && Lampa.Storage.get('lampac_auth_token', '')) return true;
      if (localStorage.getItem('lampac_auth_token')) return true;
    } catch (e) {}
    return false;
  }

  function wipeAuth() {
    // Server-side first — DELETE the device record so a subsequent
    // ?uid=<bound> request doesn't silently re-auth via gate's
    // UID-auto-reauth (auth_tg_api.go) or middleware UID-fallback
    // (auth.go). Without this the client clears its tokens but the
    // server still recognises the device on the next /lite/events.
    var uid = '';
    try {
      if (window.Lampa && Lampa.Storage) {
        uid = Lampa.Storage.get('lampac_unic_id', '') || '';
      }
    } catch (e) {}
    var logoutPath = '/api/auth/logout';
    if (uid) logoutPath += '?uid=' + encodeURIComponent(uid);
    postEmpty(logoutPath);                  // primary new endpoint
    postEmpty('/auth/password/logout');     // legacy fallback (password sessions)

    // Clear cookies — both apex and dotted domain variants. SameSite
    // tuning matches setAuthCookies on the server so the new no-op
    // cookie evicts the old SameSite=None pair on HTTPS deployments.
    var sas = (location.protocol === 'https:' ? ';SameSite=None;Secure' : ';SameSite=Lax');
    var names = ['lampac_token', 'alpac_token', '_lampac_auth', 'lampac_profile_session', 'alpac_profile_session'];
    var host = '';
    try { host = location.hostname || ''; } catch (e) {}
    names.forEach(function (name) {
      try { document.cookie = name + '=;path=/;max-age=0' + sas; } catch (e) {}
      if (host) {
        try { document.cookie = name + '=;path=/;max-age=0;domain=' + host; } catch (e) {}
        try { document.cookie = name + '=;path=/;max-age=0;domain=.' + host; } catch (e) {}
        // Second-level fallback (b.example.com → .example.com).
        try {
          var pts = host.split('.');
          if (pts.length > 2) {
            document.cookie = name + '=;path=/;max-age=0;domain=.' + pts.slice(-2).join('.');
          }
        } catch (e) {}
      }
    });
    try { localStorage.removeItem('lampac_auth_token'); } catch (e) {}
    try { localStorage.removeItem('bkit_token'); } catch (e) {}
    if (window.Lampa && Lampa.Storage) {
      try { Lampa.Storage.set('lampac_auth_token', ''); } catch (e) {}
      // Drop the cached alpac/lampac token mirrors syncpro.js writes
      // into Lampa.Storage. Otherwise getLampacToken in any plugin
      // would resurrect the old token on the next request.
      try { Lampa.Storage.set('alpac_token', ''); } catch (e) {}
      try { Lampa.Storage.set('lampac_token', ''); } catch (e) {}
    }
    // We deliberately KEEP lampac_unic_id — it identifies the device,
    // not the user. Rotating it on every logout would break bookmark/
    // history sync that's keyed on UID. The server-side device unbind
    // above is what severs the auth link; the UID itself stays.
  }

  function openAuthPage() {
    var url = HOST + '/tg/auth';
    try {
      if (window.Lampa && Lampa.Utils && typeof Lampa.Utils.openLink === 'function') {
        Lampa.Utils.openLink(url);
        return;
      }
    } catch (e) {}
    try {
      // openLink fallback: navigate in-place. Some Lampa builds intercept
      // window.location to push a WebView activity; if not, the user
      // ends up on /tg/auth and is redirected back after login.
      window.location.href = url;
    } catch (e) {}
  }

  function notify(msg) {
    try {
      if (window.Lampa && Lampa.Noty && typeof Lampa.Noty.show === 'function') {
        Lampa.Noty.show(msg);
        return;
      }
    } catch (e) {}
  }

  // ---------------------------------------------------------------------
  // Settings UI
  // ---------------------------------------------------------------------

  // Cache the last whoami snapshot so the description fields render
  // immediately when the user re-enters the tab (Settings calls onRender
  // synchronously for the description and then re-renders after the
  // async fetch updates the cache).
  var lastWhoami = null;
  function statusText() {
    if (lastWhoami && lastWhoami.authenticated) {
      var who = lastWhoami.username || lastWhoami.uid || '';
      var kind = ({ tg: 'Telegram', password: 'Логин', anon: 'Аноним' })[lastWhoami.type] || lastWhoami.type;
      var line = kind + ': ' + who;
      // Active premium overrides the base timer: showing the ~365d token
      // lifetime to a paying user reads as a wrong number (mirrors the bot).
      if (lastWhoami.premium_active) {
        line += ' · ⭐ Премиум';
        if (lastWhoami.premium_days_left != null) {
          line += ' · осталось ' + lastWhoami.premium_days_left + ' дн.';
        } else if (lastWhoami.premium_until) {
          line += ' · до ' + (lastWhoami.premium_until.slice(0, 10));
        }
      } else if (lastWhoami.days_left != null) {
        line += ' · осталось ' + lastWhoami.days_left + ' дн.';
      } else if (lastWhoami.expires_at) {
        line += ' · до ' + (lastWhoami.expires_at.slice(0, 10));
      }
      return line;
    }
    // whoami can miss while the session is actually alive: it races the
    // auth-gate recovery at boot, and on cross-origin Lampa builds the
    // cookie never travels. /api/user/info (queried with an explicit
    // ?token=) is the second witness — trust either.
    if (lastUserInfo && lastUserInfo.authorized) {
      var line2 = 'Telegram' + (lastUserInfo.tg_username ? ': @' + lastUserInfo.tg_username : '');
      if (lastUserInfo.premium_active) {
        line2 += ' · ⭐ Премиум';
        if (lastUserInfo.premium_days_left != null) line2 += ' · осталось ' + lastUserInfo.premium_days_left + ' дн.';
      } else if (lastUserInfo.days_left != null) {
        line2 += ' · осталось ' + lastUserInfo.days_left + ' дн.';
      }
      return line2;
    }
    if (!lastWhoami) return 'Загрузка...';
    switch (lastWhoami.mode) {
      case 'password': return 'Не авторизован — войдите по паролю';
      case 'none':     return 'Анонимный режим (доступ открыт)';
      default:         return 'Не авторизован — войдите через Telegram';
    }
  }
  function loadWhoami(done) {
    // Explicit ?token= mirrors loadUserInfo: on external Lampa hosts the
    // cookie never travels cross-site and whoami resolves the query token
    // server-side.
    // uid идёт ВСЕГДА, токен — если нашёлся. После перезапуска токена может
    // не быть вовсе (WebView теряет куку, а на части коробок и localStorage),
    // и тогда единственное, чем клиент может себя назвать, — это uid.
    var q = deviceQuery();
    getJSON('/api/auth/whoami' + q, function (err, data) {
      if (!err && data) lastWhoami = data;
      if (typeof done === 'function') done();
    });
  }

  // CUB anchor status — /api/user/info carries cub_linked/cub_email (the
  // CUB account passively linked server-side as a device-recovery anchor).
  var lastUserInfo = null;
  function cubStatusText() {
    if (!lastUserInfo) return 'Загрузка...';
    if (!lastUserInfo.authorized) return 'Требуется авторизация';
    if (!lastUserInfo.cub_linked) return 'Не привязан — войдите в CUB на любом устройстве, привязка произойдёт сама';
    var who = lastUserInfo.cub_email || 'привязан';
    return who + ' · якорь восстановления устройств';
  }
  function loadUserInfo(done) {
    var q = deviceQuery();
    getJSON('/api/user/info' + q, function (err, data) {
      if (!err && data) lastUserInfo = data;
      if (typeof done === 'function') done();
    });
  }

  function registerSettings() {
    if (!Lampa || !Lampa.SettingsApi || typeof Lampa.SettingsApi.addComponent !== 'function') return;

    Lampa.SettingsApi.addComponent({
      component: 'alcopac_account',
      name:      'Аккаунт',
      icon:      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor"><path d="M12 12c2.76 0 5-2.24 5-5s-2.24-5-5-5-5 2.24-5 5 2.24 5 5 5zm0 2c-3.33 0-10 1.67-10 5v3h20v-3c0-3.33-6.67-5-10-5z"/></svg>'
    });

    Lampa.SettingsApi.addParam({
      component: 'alcopac_account',
      param: {
        name:    'alcopac_account_status',
        type:    'static',
        default: ''
      },
      field: {
        name:        'Статус',
        description: statusText()
      },
      onRender: function (field) {
        var update = function () {
          try {
            field.find('.settings-param__descr').text(statusText());
          } catch (e) {}
        };
        // Both witnesses feed statusText (whoami + user/info fallback) —
        // refresh the row after whichever answers.
        loadWhoami(update);
        loadUserInfo(update);
        // The full plugin list may have arrived after boot (auth-gate
        // upgrade) — add any missing toggles for the next open of the tab.
        syncPluginToggles();
      }
    });

    Lampa.SettingsApi.addParam({
      component: 'alcopac_account',
      param: {
        name:    'alcopac_account_login',
        type:    'button'
      },
      field: {
        name:        'Войти / Перезайти',
        description: 'Открыть страницу авторизации'
      },
      onChange: openAuthPage
    });

    Lampa.SettingsApi.addParam({
      component: 'alcopac_account',
      param: {
        name:    'alcopac_account_logout',
        type:    'button'
      },
      field: {
        name:        'Выйти',
        description: 'Стереть текущую сессию и перезагрузить'
      },
      onChange: function () {
        wipeAuth();
        notify('Сессия очищена. Перезагрузка…');
        setTimeout(function () {
          try { location.reload(); } catch (e) {}
        }, 800);
      }
    });

    Lampa.SettingsApi.addParam({
      component: 'alcopac_account',
      param: {
        name:    'alcopac_account_cub',
        type:    'static',
        default: ''
      },
      field: {
        name:        'CUB',
        description: cubStatusText()
      },
      onRender: function (field) {
        loadUserInfo(function () {
          try {
            field.find('.settings-param__descr').text(cubStatusText());
          } catch (e) {}
        });
      }
    });

    Lampa.SettingsApi.addParam({
      component: 'alcopac_account',
      param: {
        name:    'alcopac_account_cub_toggle',
        type:    'button'
      },
      field: {
        name:        'Привязать / отвязать CUB',
        description: 'CUB-аккаунт как якорь восстановления устройств'
      },
      onChange: function () {
        var q = '';
        try { var tok = bestKnownToken(); if (tok) q = '&token=' + encodeURIComponent(tok); } catch (e) {}

        if (lastUserInfo && lastUserInfo.cub_linked) {
          // Linked → unlink (sets the server-side opt-out so the passive
          // learner doesn't re-link on the next boot).
          getJSON('/api/auth/cub/unlink?x=1' + q, function (err, data) {
            if (!err && data && data.ok) {
              notify('CUB-аккаунт отвязан');
              lastUserInfo = null;
              loadUserInfo();
            } else {
              notify('Не удалось отвязать CUB');
            }
          });
          return;
        }

        // Not linked → explicit link using the CUB token from Lampa's own
        // account storage (requires the user to be logged into CUB here).
        var cub = '';
        try {
          var acc = JSON.parse(localStorage.getItem('account') || '{}');
          if (acc && typeof acc.token === 'string') cub = acc.token;
        } catch (e) {}
        if (!cub) {
          notify('Сначала войдите в CUB (Настройки → CUB), затем повторите');
          return;
        }
        getJSON('/api/auth/cub/link?cub=' + encodeURIComponent(cub) + q, function (err, data) {
          if (!err && data && data.ok) {
            notify('CUB-аккаунт привязан' + (data.email ? ': ' + data.email : ''));
            lastUserInfo = null;
            loadUserInfo();
          } else if (data && data.error === 'cub token invalid') {
            notify('CUB-сессия недействительна — перезайдите в CUB');
          } else {
            notify('Не удалось привязать CUB');
          }
        });
      }
    });
  }

  // ---------------------------------------------------------------------
  // Optional plugin toggles
  //
  // The on.js bundle bakes every plugin as {k, o, u} and exposes the list
  // via window.alcopac_plugin_list. Mandatory entries (o:0 — online,
  // account) always load; the rest load only when the user enables them
  // here. The toggle value is a standard Lampa trigger param persisted as
  // alcopac_plug_<key> in Lampa.Storage — exactly the key the on.js
  // bootstrap checks on the next boot. Enabling loads the script into the
  // CURRENT session immediately; disabling takes effect after a restart
  // (scripts can't be unloaded).
  // ---------------------------------------------------------------------
  var PLUGIN_TITLES = {
    ts:              'Торренты (TorrServer)',
    catalog:         'Каталоги ALPAC',
    sisi:            'Контент 18+',
    startpage:       'Стартовая страница 18+',
    external_player: 'Внешний плеер',
    backup:          'Бэкап настроек',
    youtube_feed:    'YouTube-лента',
    dlna:            'DLNA',
    tracks:          'Аудиодорожки и субтитры',
    transcoding:     'Транскодинг',
    tmdbproxy:       'TMDB-прокси',
    server_widget:   'Виджет сервера'
  };

  // Plugins that ship as one product get ONE toggle: flipping it writes the
  // per-key storage flags for every member present in the bundle (on.js
  // reads alcopac_plug_<key> per member on the next boot) and loads all of
  // them into the current session. sync/timecode/bookmark are a single
  // syncpro experience for us — three separate rows would just confuse.
  var PLUGIN_GROUPS = [
    { id: 'sync', title: 'Синхронизация (профиль, таймкоды, закладки)', keys: ['sync', 'syncpro', 'timecode', 'bookmark'] }
  ];

  function loadPluginScript(url) {
    try {
      var s = document.createElement('script');
      s.src = url;
      (document.head || document.documentElement).appendChild(s);
    } catch (e) {}
  }

  // Adds a trigger row per optional plugin. Called at boot AND on every
  // open of the Аккаунт tab: after an unauthenticated boot the list holds
  // only the limited set, and the full tokenized bundle (with ts, sisi,
  // sync, ...) arrives later via the auth-gate upgrade hook — the re-call
  // picks up the newly listed plugins without an app restart.
  var addedToggles = {};
  var togglesHeaderAdded = false;

  function ensureTogglesHeader() {
    if (togglesHeaderAdded) return;
    togglesHeaderAdded = true;
    Lampa.SettingsApi.addParam({
      component: 'alcopac_account',
      param: { name: 'alcopac_plugins_header', type: 'static', default: '' },
      field: {
        name: 'Дополнительные плагины',
        description: 'Что подгружать кроме онлайн-источников'
      }
    });
  }

  // members: [{k, u}] — one or more bundle entries governed by this toggle.
  // paramKey doubles as the storage flag for its own key; extra members get
  // their alcopac_plug_<key> flags written manually so the on.js bootstrap
  // picks every one of them up on the next boot.
  function addToggle(paramKey, title, members) {
    Lampa.SettingsApi.addParam({
      component: 'alcopac_account',
      param: { name: 'alcopac_plug_' + paramKey, type: 'trigger', default: false },
      field: { name: title, description: '' },
      onChange: function (value) {
        var on = value === true || value === 'true';
        if (!window.alcopac_loaded_plugins) window.alcopac_loaded_plugins = {};
        var loadedNow = false, alreadyOn = false;
        members.forEach(function (m) {
          try { Lampa.Storage.set('alcopac_plug_' + m.k, on); } catch (e) {}
          if (on) {
            if (window.alcopac_loaded_plugins[m.k]) { alreadyOn = true; return; }
            window.alcopac_loaded_plugins[m.k] = true;
            loadPluginScript(m.u);
            loadedNow = true;
          }
        });
        if (on) {
          var msg = loadedNow ? 'Плагин включён' : (alreadyOn ? 'Плагин уже активен' : '');
          if (msg) notify(msg);
        } else if (members.some(function (m) { return window.alcopac_loaded_plugins[m.k]; })) {
          notify('Отключится после перезапуска приложения');
        }
      }
    });
  }

  function syncPluginToggles() {
    var list = window.alcopac_plugin_list;
    if (!list || !list.length) return;
    if (!Lampa || !Lampa.SettingsApi || typeof Lampa.SettingsApi.addParam !== 'function') return;

    // Optional entries only, keyed for group lookups.
    var optional = {};
    list.forEach(function (p) {
      if (p && typeof p === 'object' && p.o && p.k) optional[p.k] = p;
    });

    // Grouped toggles first (sync/timecode/bookmark → one row).
    var grouped = {};
    PLUGIN_GROUPS.forEach(function (g) {
      var members = [];
      g.keys.forEach(function (k) {
        if (optional[k]) { members.push(optional[k]); grouped[k] = true; }
      });
      if (!members.length || addedToggles[g.id]) return;
      addedToggles[g.id] = true;
      ensureTogglesHeader();
      addToggle(g.id, g.title, members);
    });

    // Individual toggles for the rest.
    list.forEach(function (p) {
      if (!p || typeof p !== 'object' || !p.o || grouped[p.k] || addedToggles[p.k]) return;
      addedToggles[p.k] = true;
      ensureTogglesHeader();
      addToggle(p.k, PLUGIN_TITLES[p.k] || p.k, [p]);
    });
  }

  // ---------------------------------------------------------------------
  // Activity hook — warn the user before they hit the source picker if
  // we already know no auth token is available.
  // ---------------------------------------------------------------------
  function installActivityHook() {
    if (!Lampa || !Lampa.Activity || typeof Lampa.Activity.push !== 'function') return;
    if (Lampa.Activity.__alcopac_account_hooked) return;
    Lampa.Activity.__alcopac_account_hooked = true;

    var orig = Lampa.Activity.push;
    Lampa.Activity.push = function (params) {
      try {
        var component = params && (params.component || params.activity || '');
        if (component && /online|search_lite|lampac/i.test(component)) {
          if (!hasLocalAuthToken()) {
            notify('Требуется авторизация — откройте Настройки → Аккаунт');
          }
        }
      } catch (e) {}
      return orig.apply(this, arguments);
    };
  }

  // ---------------------------------------------------------------------
  // Boot
  // ---------------------------------------------------------------------
  var tries = 0;
  var timer = setInterval(function () {
    tries++;
    if (typeof Lampa !== 'undefined' && Lampa.SettingsApi && Lampa.SettingsApi.addComponent) {
      clearInterval(timer);
      try { registerSettings(); } catch (e) {}
      try { syncPluginToggles(); } catch (e) {}
      try { installActivityHook(); } catch (e) {}
      // Pre-load both status sources so the first open of the tab is instant.
      loadWhoami();
      loadUserInfo();
    } else if (tries > 100) {
      // 20s — Lampa never loaded; give up silently.
      clearInterval(timer);
    }
  }, 200);
})();
