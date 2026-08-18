(function () {
  'use strict';

  if (window.plugin_iptv2_ready) return;

  var COMPONENT = 'iptv2';

  // ---------------------------------------------------------------------------
  //  CSS
  // ---------------------------------------------------------------------------

  var css = [
    '.iptv2-wrap { padding: 1.5em; }',

    // ---- Playlist screen ----
    '.iptv2-head { display: flex; align-items: center; margin-bottom: 1.5em; }',
    '.iptv2-head__title { font-size: 2.4em; font-weight: 300; flex: 1; }',
    '.iptv2-head__add { display: flex; align-items: center; padding: 0.55em 1.3em; border-radius: 2em; background: rgba(255,255,255,0.08); border: 0.07em solid rgba(255,255,255,0.12); font-size: 1.1em; cursor: pointer; }',
    '.iptv2-head__add.focus { background: #fff; color: #000; border-color: #fff; }',
    '.iptv2-head__add svg { width: 1.2em; height: 1.2em; margin-right: 0.5em; }',

    '.iptv2-pl { display: flex; align-items: center; padding: 1.2em 1.4em; background: linear-gradient(150deg, rgba(255,255,255,0.09) 0%, rgba(255,255,255,0.03) 100%); border: 0.07em solid rgba(255,255,255,0.07); border-radius: 1.2em; margin-bottom: 0.8em; cursor: pointer; position: relative; -webkit-transition: -webkit-transform 0.15s ease; transition: transform 0.15s ease, box-shadow 0.15s ease; }',
    '.iptv2-pl.focus { -webkit-transform: scale(1.015); transform: scale(1.015); background: rgba(255,255,255,0.14); box-shadow: 0 0 0 0.18em rgba(255,255,255,0.9), 0 0.8em 2em rgba(0,0,0,0.45); }',
    '.iptv2-pl__ico { width: 3.6em; height: 3.6em; border-radius: 0.9em; display: flex; align-items: center; justify-content: center; margin-right: 1.2em; flex-shrink: 0; font-size: 1.5em; font-weight: 800; color: #fff; text-shadow: 0 0.05em 0.2em rgba(0,0,0,0.35); }',
    '.iptv2-pl__ico--0 { background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); }',
    '.iptv2-pl__ico--1 { background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%); }',
    '.iptv2-pl__ico--2 { background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%); }',
    '.iptv2-pl__ico--3 { background: linear-gradient(135deg, #43e97b 0%, #38f9d7 100%); }',
    '.iptv2-pl__ico--4 { background: linear-gradient(135deg, #fa709a 0%, #fee140 100%); }',
    '.iptv2-pl__ico--5 { background: linear-gradient(135deg, #a18cd1 0%, #fbc2eb 100%); }',
    '.iptv2-pl__body { flex: 1; overflow: hidden; }',
    '.iptv2-pl__name { font-size: 1.4em; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }',
    '.iptv2-pl__meta { margin-top: 0.35em; font-size: 0.9em; opacity: 0.45; }',
    '.iptv2-pl__meta span { margin-right: 1.2em; }',
    '.iptv2-pl__arrow { flex-shrink: 0; opacity: 0.3; font-size: 1.4em; margin-left: 0.5em; }',
    '.iptv2-pl.focus .iptv2-pl__arrow { opacity: 0.9; }',

    '.iptv2-empty { display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 4em 2em; }',
    '.iptv2-empty__text { font-size: 1.3em; opacity: 0.35; text-align: center; line-height: 1.5; }',

    // ---- Channels: hero/info row (top) ----
    '.iptv2-info { display: flex; align-items: center; margin-bottom: 1.2em; min-height: 5.4em; padding: 0.9em 1.2em; background: linear-gradient(150deg, rgba(255,255,255,0.07) 0%, rgba(255,255,255,0.02) 100%); border: 0.07em solid rgba(255,255,255,0.06); border-radius: 1.2em; }',
    '.iptv2-info__logo { width: 5.2em; height: 3.6em; flex-shrink: 0; margin-right: 1.1em; border-radius: 0.7em; background: rgba(255,255,255,0.07); position: relative; overflow: hidden; display: none; }',
    '.iptv2-info__logo img { position: absolute; top: 50%; left: 50%; -webkit-transform: translate(-50%,-50%); transform: translate(-50%,-50%); max-width: 86%; max-height: 82%; object-fit: contain; }',
    '.iptv2-info__left { flex: 1; overflow: hidden; }',
    '.iptv2-info__group { font-size: 0.95em; opacity: 0.45; letter-spacing: 0.04em; text-transform: uppercase; }',
    '.iptv2-info__title { font-size: 1.8em; font-weight: 700; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 0.05em; }',
    '.iptv2-info__epg { font-size: 1em; opacity: 0.65; margin-top: 0.25em; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }',
    '.iptv2-info__bar { height: 0.22em; border-radius: 0.11em; background: rgba(255,255,255,0.14); margin-top: 0.5em; max-width: 26em; overflow: hidden; display: none; }',
    '.iptv2-info__bar-fill { height: 100%; width: 0; background: #fff; border-radius: 0.11em; }',
    '.iptv2-info__right { flex-shrink: 0; display: flex; margin-left: 1em; }',
    '.iptv2-btn { display: flex; align-items: center; padding: 0.55em 1.2em; border-radius: 2em; background: rgba(255,255,255,0.08); border: 0.07em solid rgba(255,255,255,0.12); font-size: 1.05em; cursor: pointer; white-space: nowrap; margin-left: 0.6em; }',
    '.iptv2-btn.focus { background: #fff; color: #000; border-color: #fff; }',
    '.iptv2-btn svg { width: 1.15em; height: 1.15em; flex-shrink: 0; margin-right: 0.5em; }',

    // ---- Channel grid ----
    '.iptv2-body { display: flex; }',
    '.iptv2-grid-scroll { flex: 1; min-width: 0; }',
    '.iptv2-grid { display: flex; flex-wrap: wrap; }',
    '.iptv2-card { width: 14.28%; padding: 0.55em; box-sizing: border-box; cursor: pointer; position: relative; visibility: hidden; }',
    '.iptv2-card.iptv2-vis { visibility: visible; }',
    '@media screen and (max-width: 1000px) { .iptv2-card { width: 20%; } }',
    '@media screen and (max-width: 700px)  { .iptv2-card { width: 25%; } }',
    '@media screen and (max-width: 480px)  { .iptv2-card { width: 33.3%; } }',
    '.iptv2-epgpanel--on .iptv2-card { width: 20%; }',
    '.iptv2-card__view { width: 100%; padding-bottom: 62%; background: linear-gradient(155deg, rgba(255,255,255,0.10) 0%, rgba(255,255,255,0.03) 60%, rgba(0,0,0,0.12) 100%); border-radius: 1.1em; position: relative; overflow: hidden; -webkit-transition: -webkit-transform 0.15s ease; transition: transform 0.15s ease, box-shadow 0.15s ease; }',
    '.iptv2-card.focus .iptv2-card__view { -webkit-transform: scale(1.07); transform: scale(1.07); box-shadow: 0 0 0 0.22em #fff, 0 0.9em 2.2em rgba(0,0,0,0.55); z-index: 2; }',
    '.iptv2-card__logo { position: absolute; top: 46%; left: 50%; -webkit-transform: translate(-50%,-50%); transform: translate(-50%,-50%); max-width: 78%; max-height: 62%; object-fit: contain; -webkit-filter: drop-shadow(0 0.15em 0.4em rgba(0,0,0,0.4)); filter: drop-shadow(0 0.15em 0.4em rgba(0,0,0,0.4)); }',
    '.iptv2-card__letter { position: absolute; top: 44%; left: 50%; -webkit-transform: translate(-50%,-50%); transform: translate(-50%,-50%); font-size: 2.1em; font-weight: 900; line-height: 1; letter-spacing: 0.02em; }',
    '.iptv2-card__num { position: absolute; top: 0.5em; left: 0.65em; font-size: 0.78em; font-weight: 700; opacity: 0.5; z-index: 1; text-shadow: 0 0.1em 0.3em rgba(0,0,0,0.8); }',
    '.iptv2-card__fav { position: absolute; top: 0.45em; right: 0.55em; font-size: 0.85em; z-index: 3; color: #ffd54f; text-shadow: 0 0.1em 0.3em rgba(0,0,0,0.8); }',
    '.iptv2-card__badge { position: absolute; top: 0.5em; right: 0.55em; padding: 0.16em 0.5em; border-radius: 0.9em; font-size: 0.62em; font-weight: 800; letter-spacing: 0.04em; z-index: 1; box-shadow: 0 0.15em 0.5em rgba(0,0,0,0.4); }',
    '.iptv2-card__fav + .iptv2-card__badge, .iptv2-card__badge--faved { top: 2em; }',
    '.iptv2-card__badge--4K { background: linear-gradient(135deg, #e91e63, #ad1457); }',
    '.iptv2-card__badge--FHD { background: linear-gradient(135deg, #2196f3, #1565c0); }',
    '.iptv2-card__badge--HD { background: linear-gradient(135deg, #4caf50, #2e7d32); }',
    // now-playing overlay pinned to the card bottom (Netflix-style shade)
    '.iptv2-card__epg { position: absolute; left: 0; right: 0; bottom: 0; padding: 1.4em 0.7em 0.45em; background: linear-gradient(to top, rgba(0,0,0,0.82) 0%, rgba(0,0,0,0.45) 55%, rgba(0,0,0,0) 100%); z-index: 1; display: none; }',
    '.iptv2-card__epg-title { font-size: 0.74em; opacity: 0.92; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; text-align: left; text-shadow: 0 0.1em 0.25em rgba(0,0,0,0.7); }',
    '.iptv2-card__epg-progress { height: 0.22em; width: 0; margin-top: 0.35em; background: #fff; border-radius: 0.11em; box-shadow: 0 0 0.4em rgba(255,255,255,0.35); }',
    '.iptv2-card__title { margin-top: 0.55em; font-size: 1em; line-height: 1.25; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; text-align: center; opacity: 0.85; }',
    '.iptv2-card.focus .iptv2-card__title { opacity: 1; font-weight: 600; }',

    // ---- EPG side panel ----
    '.iptv2-epg { display: none; width: 30%; flex-shrink: 0; margin-left: 1.2em; box-sizing: border-box; }',
    '.iptv2-epgpanel--on .iptv2-epg { display: block; }',
    '.iptv2-epg__inner { background: linear-gradient(160deg, rgba(255,255,255,0.07) 0%, rgba(255,255,255,0.02) 100%); border: 0.07em solid rgba(255,255,255,0.07); border-radius: 1.1em; padding: 1.1em 1.2em; }',
    '.iptv2-epg__ch { font-size: 1.35em; font-weight: 700; margin-bottom: 0.4em; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }',
    '.iptv2-epg__label { font-size: 0.85em; opacity: 0.45; margin: 1em 0 0.4em; letter-spacing: 0.06em; text-transform: uppercase; }',
    '.iptv2-epg__now-title { font-size: 1.08em; font-weight: 600; }',
    '.iptv2-epg__bar { height: 0.28em; border-radius: 0.14em; background: rgba(255,255,255,0.14); margin: 0.5em 0; overflow: hidden; }',
    '.iptv2-epg__bar-fill { height: 100%; background: #fff; width: 0; border-radius: 0.14em; }',
    '.iptv2-epg__desc { font-size: 0.85em; opacity: 0.55; margin-top: 0.45em; max-height: 9em; overflow: hidden; line-height: 1.45; }',
    '.iptv2-epg__item { display: flex; font-size: 0.98em; margin-top: 0.5em; }',
    '.iptv2-epg__item-time { width: 4em; flex-shrink: 0; opacity: 0.55; }',
    '.iptv2-epg__item-title { flex: 1; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }',

    // ---- Player: channel number OSD + zap helper (glassy) ----
    '.iptv2-osd { position: fixed; top: 3.5em; left: 3.5em; z-index: 5000; background: rgba(12,12,16,0.82); border: 0.07em solid rgba(255,255,255,0.14); border-radius: 1em; padding: 0.7em 1.3em; font-size: 1.5em; display: none; box-shadow: 0 0.6em 2em rgba(0,0,0,0.5); }',
    '.iptv2-osd__num { font-weight: 800; margin-right: 0.6em; font-size: 1.25em; }',
    '.iptv2-osd__title { opacity: 0.85; }',
    '.iptv2-osd__list { position: fixed; top: 9.5em; left: 3.5em; z-index: 5000; background: rgba(12,12,16,0.82); border: 0.07em solid rgba(255,255,255,0.14); border-radius: 1em; padding: 0.8em 1.3em; font-size: 1.05em; line-height: 1.7; display: none; box-shadow: 0 0.6em 2em rgba(0,0,0,0.5); opacity: 0.9; }'
  ].join('\n');

  // ---------------------------------------------------------------------------
  //  API helpers
  // ---------------------------------------------------------------------------

  function apiUrl(path, params) {
    var host = (window.lampa_settings && window.lampa_settings.host) ? window.lampa_settings.host : '';
    var url = host + path;
    var q = [];
    if (params) for (var k in params) if (params[k] !== undefined && params[k] !== '') q.push(encodeURIComponent(k) + '=' + encodeURIComponent(params[k]));
    // Token discovery, most→least fresh: Lampa.Storage mirrors (written by
    // lampainit / the on.js bootstrap), cookies, then the durable raw
    // localStorage anchor — the only store TV WebViews (LG webOS) keep
    // across relaunches. Cookie-only sessions die on webOS restart, and
    // without the anchor the playlists silently come back empty.
    var t = '';
    try { t = Lampa.Storage.get('alpac_token', '') || Lampa.Storage.get('lampac_token', ''); } catch (e) {}
    if (!t) {
      try {
        var m = document.cookie.match(/(?:^|;\s*)(?:alpac_token|lampac_token)=([^;]*)/);
        if (m && m[1]) t = decodeURIComponent(m[1]);
      } catch (e) {}
    }
    if (!t) { try { t = localStorage.getItem('lampac_auth_token') || ''; } catch (e) {} }
    if (t) q.push('token=' + encodeURIComponent(t));
    if (q.length) url += '?' + q.join('&');
    return url;
  }

  function apiGet(path, params, ok, err) {
    var n = new Lampa.Reguest();
    n.timeout(15000);
    n.silent(apiUrl(path, params), ok || function () {}, err || function () {});
    return n;
  }

  function apiPost(path, body, ok, err) {
    $.ajax({ url: apiUrl(path), type: 'POST', contentType: 'application/json', data: JSON.stringify(body), timeout: 15000, success: ok, error: function (x, s, e) { if (err) err(e || s); } });
  }

  function apiDelete(path, ok, err) {
    $.ajax({ url: apiUrl(path), type: 'DELETE', timeout: 15000, success: ok, error: function (x, s, e) { if (err) err(e || s); } });
  }

  function lang(key) { return Lampa.Lang.translate('iptv2_' + key); }

  function getStore(name, def) { return Lampa.Storage.get('iptv2_' + name, def); }
  function setStore(name, val) { return Lampa.Storage.set('iptv2_' + name, val); }

  // ---------------------------------------------------------------------------
  //  Small utils
  // ---------------------------------------------------------------------------

  // bulkWrapper — render heavy card lists in chunks so weak TVs don't freeze.
  // Ported from rootu's tv.js: queue calls, flush `bulk` per tick.
  function bulkWrapper(fn, bulk, onEnd) {
    var queue = [], interval = null;
    var runner = function () {
      if (queue.length && !interval) {
        interval = setInterval(function () {
          var i = 0;
          while (queue.length && ++i <= bulk) fn.apply(null, queue.shift());
          if (!queue.length) {
            clearInterval(interval);
            interval = null;
            if (onEnd) onEnd();
          }
        }, 1);
      }
    };
    var wrapped = function () { queue.push(arguments); runner(); };
    wrapped.stop = function () { if (interval) clearInterval(interval); interval = null; queue = []; };
    return wrapped;
  }

  // logoFallback — deterministic colored tile with channel initials, so
  // channels without (or with broken) logos still look intentional. Rootu-style.
  function logoInitials(name) {
    var n = (name || 'TV').replace(/\s+\(([+-]?\d+)\)/, ' $1').replace(/[-.()\s]+/g, ' ').replace(/(^|\s+)(TV|ТВ)(\s+|$)/i, '$3').trim();
    var fl = n.replace(/\s+/g, '').length > 5
      ? n.split(/\s+/).map(function (v) { return v.match(/^(\+?\d+|[UF]?HD|4K)$/i) ? v : v.substring(0, 1).toUpperCase(); }).join('').substring(0, 6)
      : n.replace(/\s+/g, '');
    return fl || 'TV';
  }

  function logoColor(name) {
    var hex = (Lampa.Utils.hash(name || 'TV') * 1).toString(16);
    while (hex.length < 6) hex += hex;
    hex = hex.substring(0, 6);
    var r = parseInt(hex.slice(0, 2), 16), g = parseInt(hex.slice(2, 4), 16), b = parseInt(hex.slice(4, 6), 16);
    return { bg: '#' + hex, fg: (r * 0.299 + g * 0.587 + b * 0.114) > 186 ? '#000' : '#FFF' };
  }

  function fmtTime(unix) {
    var d = new Date(unix * 1000);
    return ('0' + d.getHours()).substr(-2) + ':' + ('0' + d.getMinutes()).substr(-2);
  }

  function fmtDate(unix) {
    return new Date(unix * 1000).toLocaleDateString();
  }

  function escapeHtml(s) { return $('<i/>').text(s == null ? '' : s).html(); }

  function epgKey(ch) { return ch.tvg_id || ch.id; }

  function qualityBadge(q) {
    if (!q || q === 'SD') return '';
    return '<div class="iptv2-card__badge iptv2-card__badge--' + q + '">' + q + '</div>';
  }

  // ---------------------------------------------------------------------------
  //  Player zapping — the rootu experience:
  //   • digits type a channel number (OSD + prediction list, 3s commit)
  //   • ←/→ = prev/next channel (looped) when no player controls are focused
  //   • PgUp/PgDn = prev/next everywhere
  //  Works because every playlist item carries a REAL play_url now.
  // ---------------------------------------------------------------------------

  var zap = {
    playlist: null,   // [{title,url,iptv:true,...}]
    active: false,
    number: '',
    timer: null,
    osd: null,
    osdList: null,

    ensureOSD: function () {
      if (!this.osd || !$.contains(document.body, this.osd[0])) {
        this.osd = $('<div class="iptv2-osd"><span class="iptv2-osd__num"></span><span class="iptv2-osd__title"></span></div>').appendTo('body');
        this.osdList = $('<div class="iptv2-osd__list"></div>').appendTo('body');
      }
    },

    start: function (playlist) {
      this.playlist = playlist;
      this.active = true;
      this.number = '';
    },

    stop: function () {
      this.active = false;
      this.playlist = null;
      this.number = '';
      if (this.timer) clearTimeout(this.timer);
      if (this.osd) this.osd.hide();
      if (this.osdList) this.osdList.hide();
    },

    position: function () {
      try {
        var cur = Lampa.PlayerPlaylist.position();
        if (typeof cur === 'number' && cur >= 0) return cur;
      } catch (e) { /* older Lampa */ }
      return 0;
    },

    switchTo: function (pos) {
      var pl = this.playlist;
      if (!pl || !pl[pos]) return;
      if (!pl[pos].url) { Lampa.Noty.show(lang('no_stream')); return; }
      try {
        Lampa.PlayerPlaylist.listener.send('select', { playlist: pl, position: pos, item: pl[pos] });
      } catch (e) {
        Lampa.Player.play(pl[pos]);
      }
      var runas = Lampa.Storage.field('player_iptv');
      if (Lampa.Player.runas && runas) Lampa.Player.runas(runas);
    },

    showOSD: function (num, title, help) {
      this.ensureOSD();
      this.osd.find('.iptv2-osd__num').text(num);
      this.osd.find('.iptv2-osd__title').text(title || '');
      this.osd.show();
      if (help && help.length) {
        this.osdList.html(help.map(escapeHtml).join('<br>')).show();
      } else this.osdList.hide();
    },

    hideOSDSoon: function () {
      var self = this;
      setTimeout(function () { if (self.osd) self.osd.fadeOut(300); if (self.osdList) self.osdList.fadeOut(300); }, 1200);
    },

    step: function (dir) {
      var pl = this.playlist;
      if (!pl || !pl.length) return false;
      var pos = this.position() + dir;
      if (pos < 0) pos = pl.length - 1;
      if (pos >= pl.length) pos = 0;
      this.showOSD(pos + 1, pl[pos].chName || pl[pos].title);
      this.hideOSDSoon();
      this.switchTo(pos);
      return true;
    },

    digit: function (d) {
      var pl = this.playlist;
      if (!pl || !pl.length) return false;
      var self = this;
      var next = this.number + d;
      var num = parseInt(next, 10);
      if (!num || num > pl.length) return true; // swallow the key, keep current entry
      this.number = next;
      if (this.timer) clearTimeout(this.timer);

      // Prediction list: channels reachable by typing one more digit.
      var help = [];
      var start = num * 10;
      for (var i = start; i <= pl.length && help.length < 9; i++) help.push(i + '. ' + (pl[i - 1].chName || pl[i - 1].title));
      this.showOSD(this.number, pl[num - 1].chName || pl[num - 1].title, help);

      var commit = function () {
        self.number = '';
        self.hideOSDSoon();
        self.switchTo(num - 1);
      };
      if (start > pl.length) commit(); // no further digit possible
      else this.timer = setTimeout(commit, 3000);
      return true;
    },

    keydown: function (e) {
      if (!this.active) return;
      if (!Lampa.Player.opened()) return;
      if ($('body.selectbox--open').length) return;
      var code = e.code;
      var handled = false;

      // ←/→ zap only when the player panel/footer has no focused control —
      // otherwise arrows must keep navigating the controls (feedback rule).
      var controlsFocused = $('.player .panel--visible .focus').length || $('.player .player-footer.open .focus').length;

      if (code === 34 || code === 428 || ((code === 37 || code === 4) && !controlsFocused)) handled = this.step(-1);
      else if (code === 33 || code === 427 || ((code === 39 || code === 5) && !controlsFocused)) handled = this.step(1);
      else if (code >= 48 && code <= 57) handled = this.digit(code - 48);
      else if (code >= 96 && code <= 105) handled = this.digit(code - 96);

      if (handled && e.event) {
        e.event.preventDefault();
        e.event.stopPropagation();
      }
    }
  };

  // One global keydown hook; zap.active gates it.
  Lampa.Keypad.listener.follow('keydown', function (e) { zap.keydown(e); });
  // Re-assert the chosen IPTV player on every playlist select (rootu does this).
  Lampa.PlayerPlaylist.listener.follow('select', function (e) {
    if (e.item && e.item.iptv2 && Lampa.Player.runas) {
      var runas = Lampa.Storage.field('player_iptv');
      if (runas) Lampa.Player.runas(runas);
    }
  });

  // ---------------------------------------------------------------------------
  //  Component
  //  object params: { component:'iptv2', view:'playlists'|'channels',
  //                   playlist:{id,name}, group:'<name>'|''('' = favorites)|null }
  // ---------------------------------------------------------------------------

  var FAV_GROUP = '::fav::';

  function Component(object) {
    var comp = this;
    var network = new Lampa.Reguest();
    var scroll = new Lampa.Scroll({ mask: true, over: true, step: 250 });
    var html = $('<div></div>');
    var view = object.view || 'playlists';
    var playlist = object.playlist || null;
    var curGroup = (typeof object.group === 'string') ? object.group : null;

    var playlists = [];
    var groups = [];
    var channels = [];
    var favorites = [];      // array of channel ids (per playlist)
    var last = false;        // last focused DOM element — restored on toggle
    var infoEl = null;
    var epgPanelEl = null;
    var cards = [];          // [{el, ch, idx}] for the visibility window
    var focusIdx = 0;
    var visTimer = null;
    var epgTimer = null;
    var epgCache = {};       // epgKey -> {now:{start,stop,title,desc}, next:{...}}
    var epgFocusReq = null;
    var bulkRender = null;
    var searchQuery = '';

    // -- lifecycle ------------------------------------------------------------

    this.create = function () { return this.render(); };
    this.render = function () { return html; };

    this.start = function () {
      if (Lampa.Activity.active() && Lampa.Activity.active().activity !== this.activity) return;
      Lampa.Background.immediately('');
      Lampa.Controller.add('content', {
        invisible: true,
        toggle: function () {
          Lampa.Controller.collectionSet(scroll.render());
          Lampa.Controller.collectionFocus(last || false, scroll.render());
        },
        left: function () {
          if (Navigator.canmove('left')) Navigator.move('left');
          else Lampa.Controller.toggle('menu');
        },
        right: function () {
          if (Navigator.canmove('right')) Navigator.move('right');
          else if (view === 'channels') selectGroup();
        },
        up: function () {
          if (Navigator.canmove('up')) Navigator.move('up');
          else Lampa.Controller.toggle('head');
        },
        down: function () {
          if (Navigator.canmove('down')) Navigator.move('down');
        },
        back: function () {
          Lampa.Activity.backward();
        }
      });
      Lampa.Controller.toggle('content');

      if (!this._initialized) {
        this._initialized = true;
        if (!$('#iptv2-css').length) $('head').append('<style id="iptv2-css">' + css + '</style>');
        html.append(scroll.render());
        scroll.minus();
        if (view === 'channels') loadChannels();
        else loadPlaylists();
      }
    };

    this.pause = function () {};
    this.stop = function () {};
    this.destroy = function () {
      zap.stop();
      if (bulkRender) bulkRender.stop();
      if (visTimer) clearInterval(visTimer);
      if (epgTimer) clearInterval(epgTimer);
      network.clear();
      scroll.destroy();
      html.remove();
      cards = [];
      channels = [];
      last = false;
    };

    // ===================== Playlists screen =====================

    function loadPlaylists() {
      comp.activity.loader(true);
      apiGet('/api/iptv/playlists', {}, function (data) {
        comp.activity.loader(false);
        playlists = data.playlists || [];
        // Single playlist → skip the chooser, go straight to channels.
        if (playlists.length === 1 && !object.stay) {
          openChannels(playlists[0], true);
          return;
        }
        renderPlaylists();
      }, function () {
        comp.activity.loader(false);
        playlists = [];
        renderPlaylists();
      });
    }

    function openChannels(pl, replace) {
      var act = {
        title: pl.name || 'IPTV',
        component: COMPONENT,
        view: 'channels',
        playlist: { id: pl.id, name: pl.name },
        group: getStore('last_group_' + pl.id, null),
        page: 1
      };
      if (replace) Lampa.Activity.replace(act);
      else Lampa.Activity.push(act);
    }

    function renderPlaylists() {
      scroll.clear();
      var w = $('<div class="iptv2-wrap"></div>');

      var head = $('<div class="iptv2-head"></div>');
      head.append('<div class="iptv2-head__title">IPTV</div>');
      var addBtn = $('<div class="iptv2-head__add selector"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg><span>' + lang('add') + '</span></div>');
      addBtn.on('hover:enter', promptAdd);
      addBtn.on('hover:focus', function () { last = this; });
      head.append(addBtn);
      w.append(head);

      if (!playlists.length) {
        w.append('<div class="iptv2-empty"><div class="iptv2-empty__text">' + lang('empty') + '</div></div>');
      }

      playlists.forEach(function (pl, i) {
        var row = $('<div class="iptv2-pl selector"></div>');
        row.append('<div class="iptv2-pl__ico iptv2-pl__ico--' + (i % 6) + '">' + escapeHtml((pl.name || 'P').charAt(0).toUpperCase()) + '</div>');
        var body = $('<div class="iptv2-pl__body"></div>');
        body.append($('<div class="iptv2-pl__name"></div>').text(pl.name || 'Playlist'));
        var meta = $('<div class="iptv2-pl__meta"></div>');
        meta.append('<span>' + (pl.channel_count || 0) + ' ' + lang('ch') + '</span>');
        if (pl.is_global) meta.append('<span>global</span>');
        body.append(meta);
        row.append(body);
        row.append('<div class="iptv2-pl__arrow">›</div>');

        row.on('hover:focus', function () { last = this; });
        row.on('hover:enter', function () { openChannels(pl); });
        row.on('hover:long', function () {
          var items = [{ title: lang('open'), value: 'open' }];
          if (!pl.is_global) {
            items.push({ title: lang('refresh'), value: 'refresh' });
            items.push({ title: lang('delete'), value: 'delete' });
          }
          Lampa.Select.show({
            title: pl.name,
            items: items,
            onSelect: function (a) {
              if (a.value === 'open') { openChannels(pl); return; }
              if (a.value === 'delete') apiDelete('/api/iptv/playlists/' + pl.id, function () { Lampa.Noty.show(lang('deleted')); comp._initialized = true; loadPlaylists(); });
              else apiPost('/api/iptv/playlists/' + pl.id + '/refresh', {}, function () { Lampa.Noty.show(lang('refreshed')); loadPlaylists(); });
              Lampa.Controller.toggle('content');
            },
            onBack: function () { Lampa.Controller.toggle('content'); }
          });
        });

        w.append(row);
      });

      scroll.append(w);
      scroll.reset();
      Lampa.Controller.toggle('content');
      comp.activity.toggle();
    }

    function promptAdd() {
      Lampa.Input.edit({ title: lang('url_title'), value: '', free: true, nosave: true }, function (url) {
        if (!url || !url.trim()) { Lampa.Controller.toggle('content'); return; }
        Lampa.Input.edit({ title: lang('name_title'), value: 'My Playlist', free: true, nosave: true }, function (name) {
          comp.activity.loader(true);
          apiPost('/api/iptv/playlists', { name: name || 'My Playlist', url: url.trim() }, function () {
            loadPlaylists();
          }, function () {
            comp.activity.loader(false);
            Lampa.Noty.show(lang('error'));
          });
        });
      });
    }

    // ===================== Channels screen =====================

    function favStoreKey() { return 'fav_' + playlist.id; }
    function loadFavorites() { favorites = getStore(favStoreKey(), []); if (!Array.isArray(favorites)) favorites = []; }
    function saveFavorites() { setStore(favStoreKey(), favorites); }

    function loadChannels() {
      loadFavorites();
      comp.activity.loader(true);
      apiGet('/api/iptv/groups', { playlist_id: playlist.id }, function (data) {
        groups = data.groups || [];
        // Resolve which group to show: explicit param → stored → first group → all.
        if (curGroup === null) {
          curGroup = favorites.length ? FAV_GROUP : (groups.length ? groups[0].name : '');
        }
        fetchChannels();
      }, function () {
        groups = [];
        if (curGroup === null) curGroup = '';
        fetchChannels();
      });
    }

    function fetchChannels() {
      var params = { playlist_id: playlist.id, limit: 2000, play: 1 };
      if (searchQuery) params.search = searchQuery;
      else if (curGroup && curGroup !== FAV_GROUP) params.group = curGroup;
      apiGet('/api/iptv/channels', params, function (data) {
        comp.activity.loader(false);
        channels = data.channels || [];
        if (!searchQuery && curGroup === FAV_GROUP) {
          // Favorites view: fetched everything, keep only favorites in saved order.
          var byId = {};
          channels.forEach(function (c) { byId[c.id] = c; });
          channels = favorites.map(function (id) { return byId[id]; }).filter(Boolean);
        }
        if (!searchQuery) setStore('last_group_' + playlist.id, curGroup);
        renderChannels();
      }, function () {
        comp.activity.loader(false);
        channels = [];
        renderChannels();
      });
    }

    function groupTitle() {
      if (searchQuery) return lang('search') + ': ' + searchQuery;
      if (curGroup === FAV_GROUP) return lang('favorites');
      return curGroup || lang('all');
    }

    // -- group selector (Lampa.Select — native remote UX, rootu-style) --------

    function selectGroup() {
      var items = [];
      items.push({ title: lang('favorites') + ' [' + favorites.length + ']', key: FAV_GROUP, selected: curGroup === FAV_GROUP });
      items.push({ title: lang('all'), key: '', selected: curGroup === '' && !searchQuery });
      groups.forEach(function (g) {
        items.push({ title: g.name + ' [' + g.count + ']', key: g.name, selected: curGroup === g.name });
      });
      items.push({ title: '🔍 ' + lang('search'), search: true });
      Lampa.Select.show({
        title: lang('categories'),
        items: items,
        onSelect: function (a) {
          if (a.search) { openSearch(); return; }
          if (curGroup !== a.key || searchQuery) {
            Lampa.Activity.replace({
              title: playlist.name, component: COMPONENT, view: 'channels',
              playlist: playlist, group: a.key, page: 1
            });
          } else {
            Lampa.Controller.toggle('content');
          }
        },
        onBack: function () { Lampa.Controller.toggle('content'); }
      });
    }

    function openSearch() {
      Lampa.Input.edit({ title: lang('search_title'), value: '', free: true, nosave: true }, function (val) {
        val = (val || '').trim();
        if (!val) { Lampa.Controller.toggle('content'); return; }
        searchQuery = val;
        last = false;
        comp.activity.loader(true);
        fetchChannels();
      });
    }

    // -- render ----------------------------------------------------------------

    function renderChannels() {
      scroll.clear();
      if (bulkRender) bulkRender.stop();
      cards = [];
      focusIdx = 0;
      epgCache = {};

      var w = $('<div class="iptv2-wrap' + (getStore('epg_panel', false) ? ' iptv2-epgpanel--on' : '') + '"></div>');

      // ---- info row ----
      infoEl = $('<div class="iptv2-info">' +
        '<div class="iptv2-info__logo"><img alt=""></div>' +
        '<div class="iptv2-info__left">' +
        '<div class="iptv2-info__group"></div>' +
        '<div class="iptv2-info__title"></div>' +
        '<div class="iptv2-info__epg"></div>' +
        '<div class="iptv2-info__bar"><div class="iptv2-info__bar-fill"></div></div>' +
        '</div>' +
        '<div class="iptv2-info__right"></div>' +
        '</div>');
      infoEl.find('.iptv2-info__group').text(playlist.name + ' — ' + groupTitle() + ' [' + channels.length + ']');

      var catBtn = $('<div class="iptv2-btn selector"><svg viewBox="0 0 24 24" fill="currentColor"><path d="M20,10H4c-1.1,0-2,0.9-2,2c0,1.1,0.9,2,2,2h16c1.1,0,2-0.9,2-2C22,10.9,21.1,10,20,10z"/><path d="M4,8h12c1.1,0,2-0.9,2-2c0-1.1-0.9-2-2-2H4C2.9,4,2,4.9,2,6C2,7.1,2.9,8,4,8z"/><path d="M16,16H4c-1.1,0-2,0.9-2,2c0,1.1,0.9,2,2,2h12c1.1,0,2-0.9,2-2C18,16.9,17.1,16,16,16z"/></svg><span>' + lang('categories') + '</span></div>');
      catBtn.on('hover:enter hover:click', function () { selectGroup(); });
      catBtn.on('hover:focus', function () { last = this; });
      infoEl.find('.iptv2-info__right').append(catBtn);
      w.append(infoEl);

      // ---- body: grid + EPG panel ----
      var bodyRow = $('<div class="iptv2-body"></div>');
      var gridWrap = $('<div class="iptv2-grid-scroll"></div>');
      var grid = $('<div class="iptv2-grid"></div>');
      gridWrap.append(grid);
      bodyRow.append(gridWrap);

      epgPanelEl = $('<div class="iptv2-epg"><div class="iptv2-epg__inner">' +
        '<div class="iptv2-epg__ch"></div>' +
        '<div class="iptv2-epg__now" style="display:none">' +
        '<div class="iptv2-epg__label">' + lang('now') + '</div>' +
        '<div class="iptv2-epg__now-title"></div>' +
        '<div class="iptv2-epg__bar"><div class="iptv2-epg__bar-fill"></div></div>' +
        '<div class="iptv2-epg__desc"></div>' +
        '</div>' +
        '<div class="iptv2-epg__after" style="display:none">' +
        '<div class="iptv2-epg__label">' + lang('later') + '</div>' +
        '<div class="iptv2-epg__list"></div>' +
        '</div>' +
        '</div></div>');
      bodyRow.append(epgPanelEl);
      w.append(bodyRow);

      if (!channels.length) {
        grid.append('<div class="iptv2-empty" style="width:100%"><div class="iptv2-empty__text">' + lang('no_ch') + '</div></div>');
      }

      // ---- cards (bulk-rendered) ----
      bulkRender = bulkWrapper(function (ch, idx) {
        var card = buildCard(ch, idx);
        cards.push({ el: card, ch: ch, idx: idx });
        grid.append(card);
      }, 18, function () {
        comp.activity.loader(false);
        comp.activity.toggle();
        updateVisibility();
        fetchEpgForWindow();
      });
      channels.forEach(function (ch, idx) { bulkRender(ch, idx); });

      scroll.append(w);
      scroll.reset();
      Lampa.Controller.toggle('content');
      if (!channels.length) comp.activity.toggle();

      // Visibility window + EPG progress refresh tickers.
      if (visTimer) clearInterval(visTimer);
      visTimer = setInterval(updateVisibility, 200);
      if (epgTimer) clearInterval(epgTimer);
      epgTimer = setInterval(function () {
        renderCardsEpg();
        fetchEpgForWindow();
      }, 30000);
    }

    // Visibility window (rootu technique): only ±N cards around the focus are
    // visible — keeps huge playlists smooth on weak devices.
    var VIS_WINDOW = 60;
    function updateVisibility() {
      if (!cards.length) return;
      var min = Math.max(focusIdx - VIS_WINDOW, 0);
      var max = Math.min(focusIdx + VIS_WINDOW, cards.length - 1);
      for (var i = 0; i < cards.length; i++) {
        var vis = i >= min && i <= max;
        if (vis !== cards[i].vis) {
          cards[i].vis = vis;
          cards[i].el.toggleClass('iptv2-vis', vis);
        }
      }
    }

    function buildCard(ch, idx) {
      var card = $('<div class="iptv2-card selector"></div>');
      var viewEl = $('<div class="iptv2-card__view"></div>');

      // number + fav marker + quality
      viewEl.append('<div class="iptv2-card__num">' + (idx + 1) + '</div>');
      var faved = curGroup !== FAV_GROUP && favorites.indexOf(ch.id) !== -1;
      if (faved) viewEl.append('<div class="iptv2-card__fav">★</div>');
      var badge = qualityBadge(ch.quality);
      if (badge) {
        badge = $(badge);
        if (faved) badge.addClass('iptv2-card__badge--faved');
        viewEl.append(badge);
      }

      // logo (lazy) with colored-initials fallback
      var setFallback = function () {
        var c = logoColor(ch.name);
        viewEl.css({ 'background': c.bg, color: c.fg });
        viewEl.append('<div class="iptv2-card__letter">' + escapeHtml(logoInitials(ch.name)) + '</div>');
      };
      if (ch.logo) {
        var img = $('<img class="iptv2-card__logo" alt="">');
        img[0].loading = 'lazy';
        img.on('error', function () { img.remove(); setFallback(); });
        img.attr('src', ch.logo);
        viewEl.append(img);
      } else setFallback();

      // now-playing shade overlay pinned to the card bottom
      viewEl.append('<div class="iptv2-card__epg"><div class="iptv2-card__epg-title"></div><div class="iptv2-card__epg-progress"></div></div>');

      card.append(viewEl);
      card.append($('<div class="iptv2-card__title"></div>').text(ch.name));

      card.on('hover:focus hover:hover touchstart', function (ev) {
        focusIdx = idx;
        last = card[0];
        if (ev.type === 'hover:focus') scroll.update(card, true);
        updateInfo(ch);
      });
      card.on('hover:enter', function () { playChannel(ch, idx); });
      card.on('hover:long', function () { channelMenu(ch, idx, card); });

      return card;
    }

    // -- info row + EPG panel for the focused channel ---------------------------

    function updateInfo(ch) {
      infoEl.find('.iptv2-info__title').text(ch.name);

      // focused channel logo in the hero row
      var logoBox = infoEl.find('.iptv2-info__logo');
      var logoImg = logoBox.find('img');
      if (ch.logo) {
        if (logoImg.attr('src') !== ch.logo) logoImg.attr('src', ch.logo);
        logoBox.css('display', 'block');
      } else {
        logoBox.css('display', 'none');
      }

      var e = epgCache[epgKey(ch)];
      var line = '';
      var bar = infoEl.find('.iptv2-info__bar');
      var now = Math.floor(Date.now() / 1000);
      if (e && e.now) {
        line = fmtTime(e.now.start) + '–' + fmtTime(e.now.stop) + ' • ' + e.now.title;
        if (e.next) line += '  →  ' + fmtTime(e.next.start) + ' ' + e.next.title;
        if (e.now.start <= now && now < e.now.stop) {
          var pct = Math.round((now - e.now.start) * 100 / Math.max(e.now.stop - e.now.start, 1));
          bar.find('.iptv2-info__bar-fill').css('width', pct + '%');
          bar.css('display', 'block');
        } else bar.css('display', 'none');
      } else bar.css('display', 'none');
      infoEl.find('.iptv2-info__epg').text(line);
      renderEpgPanel(ch);
    }

    function renderEpgPanel(ch) {
      if (!getStore('epg_panel', false) || !epgPanelEl) return;
      epgPanelEl.find('.iptv2-epg__ch').text(ch.name);
      var nowBox = epgPanelEl.find('.iptv2-epg__now').hide();
      var afterBox = epgPanelEl.find('.iptv2-epg__after').hide();

      if (epgFocusReq) epgFocusReq.clear();
      var key = epgKey(ch);
      if (!key) return;
      var from = Math.floor(Date.now() / 1000) - 3600;
      var to = from + 12 * 3600;
      epgFocusReq = apiGet('/api/iptv/epg/timeline', { channel_id: key, from: from, to: to }, function (data) {
        var progs = (data && data.programs) || [];
        var now = Math.floor(Date.now() / 1000);
        var cur = null, rest = [];
        progs.forEach(function (p) {
          if (p.start <= now && now < p.stop) cur = p;
          else if (p.start > now) rest.push(p);
        });
        if (cur) {
          nowBox.find('.iptv2-epg__now-title').text(fmtTime(cur.start) + '–' + fmtTime(cur.stop) + ' • ' + cur.title);
          var pct = Math.round((now - cur.start) * 100 / Math.max(cur.stop - cur.start, 1));
          nowBox.find('.iptv2-epg__bar-fill').css('width', pct + '%');
          nowBox.find('.iptv2-epg__desc').text(cur.desc || '');
          nowBox.show();
        }
        if (rest.length) {
          var list = afterBox.find('.iptv2-epg__list').empty();
          rest.slice(0, 8).forEach(function (p) {
            list.append('<div class="iptv2-epg__item"><div class="iptv2-epg__item-time">' + fmtTime(p.start) + '</div><div class="iptv2-epg__item-title">' + escapeHtml(p.title) + '</div></div>');
          });
          afterBox.show();
        }
      });
    }

    // -- card EPG lines (batched now/next for the visible window) ---------------

    var epgFetched = {};
    function fetchEpgForWindow() {
      if (!cards.length) return;
      var min = Math.max(focusIdx - VIS_WINDOW, 0);
      var max = Math.min(focusIdx + VIS_WINDOW, cards.length - 1);
      var ids = [];
      for (var i = min; i <= max && ids.length < 200; i++) {
        var key = epgKey(cards[i].ch);
        if (key && !epgFetched[key]) { epgFetched[key] = true; ids.push(key); }
      }
      if (!ids.length) { renderCardsEpg(); return; }
      apiGet('/api/iptv/epg/now', { channel_ids: ids.join(',') }, function (data) {
        (data.epg || []).forEach(function (nn) {
          epgCache[nn.channel_id] = nn;
        });
        renderCardsEpg();
      }, function () {
        // allow retry on next tick
        ids.forEach(function (id) { delete epgFetched[id]; });
      });
    }

    function renderCardsEpg() {
      var now = Math.floor(Date.now() / 1000);
      var min = Math.max(focusIdx - VIS_WINDOW, 0);
      var max = Math.min(focusIdx + VIS_WINDOW, cards.length - 1);
      for (var i = min; i <= max; i++) {
        var c = cards[i];
        var e = epgCache[epgKey(c.ch)];
        var box = c.el.find('.iptv2-card__epg');
        if (e && e.now && e.now.start <= now && now < e.now.stop) {
          var pct = Math.round((now - e.now.start) * 100 / Math.max(e.now.stop - e.now.start, 1));
          box.find('.iptv2-card__epg-title').text(e.now.title);
          box.find('.iptv2-card__epg-progress').css('width', pct + '%');
          box.css('display', 'block');
        } else {
          box.css('display', 'none');
        }
        // stale "now" → refetch on next window pass
        if (e && e.now && now >= e.now.stop) delete epgFetched[epgKey(c.ch)];
      }
      // refresh focused info line too
      if (cards[focusIdx]) {
        var ch = cards[focusIdx].ch;
        if (infoEl && infoEl.find('.iptv2-info__title').text() === ch.name) updateInfo(ch);
      }
    }

    // -- play --------------------------------------------------------------------

    function buildPlayerPlaylist() {
      return channels.map(function (c, i) {
        return {
          title: (i + 1) + '. ' + c.name,
          chName: c.name,
          url: c.play_url || '',
          thumbnail: c.logo,
          iptv: true,
          iptv2: true,
          tv: true
        };
      });
    }

    function playChannel(ch, idx) {
      var startPlay = function (url) {
        var pl = buildPlayerPlaylist();
        pl[idx].url = url;
        var runas = Lampa.Storage.field('player_iptv');
        if (Lampa.Player.runas && runas) Lampa.Player.runas(runas);
        Lampa.Player.play(pl[idx]);
        if (Lampa.Player.runas && runas) Lampa.Player.runas(runas);
        Lampa.Player.playlist(pl);
        zap.start(pl);
      };
      if (ch.play_url) startPlay(ch.play_url);
      else {
        comp.activity.loader(true);
        apiGet('/api/iptv/play', { channel_id: ch.id }, function (data) {
          comp.activity.loader(false);
          if (!data || !data.url) { Lampa.Noty.show(lang('no_stream')); return; }
          ch.play_url = data.url;
          startPlay(data.url);
        }, function () {
          comp.activity.loader(false);
          Lampa.Noty.show(lang('no_stream'));
        });
      }
    }

    function playArchive(ch, program, dayPlaylist) {
      comp.activity.loader(true);
      apiGet('/api/iptv/play', { channel_id: ch.id, start: program.start, duration: Math.max(program.stop - program.start, 60) }, function (data) {
        comp.activity.loader(false);
        if (!data || !data.url) { Lampa.Noty.show(lang('no_stream')); return; }
        var runas = Lampa.Storage.field('player_iptv');
        if (Lampa.Player.runas && runas) Lampa.Player.runas(runas);
        Lampa.Player.play({
          title: ch.name + ' • ' + program.title,
          url: data.url,
          iptv: true,
          tv: false,
          timeline: { time: 0, percent: 0, duration: (program.stop - program.start), handler: function () {} }
        });
        if (Lampa.Player.runas && runas) Lampa.Player.runas(runas);
        if (dayPlaylist) Lampa.Player.playlist(dayPlaylist);
      }, function () {
        comp.activity.loader(false);
        Lampa.Noty.show(lang('no_stream'));
      });
    }

    // -- long-press context menu ---------------------------------------------------

    function channelMenu(ch, idx, card) {
      var favI = favorites.indexOf(ch.id);
      var inFavView = curGroup === FAV_GROUP && !searchQuery;
      var catchupDays = (ch.catchup && ch.catchup.days) || 0;
      var e = epgCache[epgKey(ch)];
      var items = [];

      items.push({ title: lang('play'), doPlay: true });
      if (catchupDays > 0) {
        if (e && e.now) items.push({ title: lang('restart'), doRestart: true });
        items.push({ title: lang('archive'), doArchive: true });
      }
      if (e && e.now) items.push({ title: Lampa.Lang.translate('search_start') + ': ' + e.now.title, doSearch: e.now.title });
      items.push({ title: favI === -1 ? lang('fav_add') : lang('fav_del'), doFav: true });
      if (inFavView && favorites.length > 1 && favI !== -1) {
        if (favI !== 0) items.push({ title: lang('fav_top'), doMove: 0 });
        if (favI > 0) items.push({ title: lang('fav_up'), doMove: favI - 1 });
        if (favI < favorites.length - 1) items.push({ title: lang('fav_down'), doMove: favI + 1 });
      }
      items.push({ title: getStore('epg_panel', false) ? lang('epg_off') : lang('epg_on'), doEpgToggle: true });

      Lampa.Select.show({
        title: ch.name,
        items: items,
        onSelect: function (a) {
          if (a.doPlay) { Lampa.Controller.toggle('content'); playChannel(ch, idx); }
          else if (a.doRestart) {
            Lampa.Controller.toggle('content');
            playArchive(ch, { start: e.now.start, stop: e.now.stop, title: e.now.title });
          }
          else if (a.doArchive) openArchive(ch, catchupDays);
          else if (a.doSearch) { Lampa.Search.open({ input: cleanupProgramTitle(a.doSearch) }); }
          else if (a.doFav) {
            if (favI === -1) favorites.push(ch.id);
            else favorites.splice(favI, 1);
            saveFavorites();
            if (inFavView) {
              Lampa.Activity.replace({ title: playlist.name, component: COMPONENT, view: 'channels', playlist: playlist, group: FAV_GROUP, page: 1 });
            } else {
              var mark = card.find('.iptv2-card__fav');
              var nowFaved = favorites.indexOf(ch.id) !== -1;
              if (nowFaved && !mark.length) card.find('.iptv2-card__view').append('<div class="iptv2-card__fav">★</div>');
              else if (!nowFaved) mark.remove();
              card.find('.iptv2-card__badge').toggleClass('iptv2-card__badge--faved', nowFaved);
              Lampa.Controller.toggle('content');
            }
          }
          else if (typeof a.doMove === 'number') {
            favorites.splice(favI, 1);
            favorites.splice(a.doMove, 0, ch.id);
            saveFavorites();
            Lampa.Activity.replace({ title: playlist.name, component: COMPONENT, view: 'channels', playlist: playlist, group: FAV_GROUP, page: 1 });
          }
          else if (a.doEpgToggle) {
            var on = !getStore('epg_panel', false);
            setStore('epg_panel', on);
            scroll.render().find('.iptv2-wrap').toggleClass('iptv2-epgpanel--on', on);
            if (on && cards[focusIdx]) renderEpgPanel(cards[focusIdx].ch);
            Lampa.Controller.toggle('content');
          }
        },
        onBack: function () { Lampa.Controller.toggle('content'); }
      });
    }

    // strip episode/season junk from an EPG title before feeding global search
    function cleanupProgramTitle(t) {
      return (t || '')
        .replace(/^«([^»]+)».*$/, '$1')
        .replace(/^"([^"]+)".*$/, '$1')
        .replace(/[.,]?\s*\d+\s*(-я)?\s*(сери[яи]|эпизод|с\.|ep?\.?)\s*\d*\.?\s*$/i, '')
        .replace(/\s*\((19|20)\d\d\)\s*$/, '')
        .trim();
    }

    // -- archive (catchup) browser ---------------------------------------------------

    function openArchive(ch, days) {
      var key = epgKey(ch);
      if (!key) { Lampa.Noty.show(lang('no_epg')); Lampa.Controller.toggle('content'); return; }
      var dayItems = [];
      var now = Math.floor(Date.now() / 1000);
      var names = [lang('today'), lang('yesterday')];
      for (var d = 0; d <= days; d++) {
        var dayStart = now - d * 86400;
        dayItems.push({ title: names[d] || fmtDate(dayStart), day: d });
      }
      Lampa.Select.show({
        title: lang('archive') + ' — ' + ch.name,
        items: dayItems,
        onSelect: function (sel) { openArchiveDay(ch, sel.day); },
        onBack: function () { Lampa.Controller.toggle('content'); }
      });
    }

    function openArchiveDay(ch, day) {
      var key = epgKey(ch);
      var now = Math.floor(Date.now() / 1000);
      // midnight-align the requested day in local time
      var d0 = new Date((now - day * 86400) * 1000);
      d0.setHours(0, 0, 0, 0);
      var from = Math.floor(d0.getTime() / 1000);
      var to = Math.min(from + 86400, now);
      comp.activity.loader(true);
      apiGet('/api/iptv/epg/timeline', { channel_id: key, from: from, to: to }, function (data) {
        comp.activity.loader(false);
        var progs = ((data && data.programs) || []).filter(function (p) { return p.stop <= now; });
        if (!progs.length) { Lampa.Noty.show(lang('no_epg')); Lampa.Controller.toggle('content'); return; }
        progs.reverse(); // freshest first
        var items = progs.map(function (p) {
          return { title: fmtTime(p.start) + '–' + fmtTime(p.stop) + '  ' + p.title, prog: p };
        });
        Lampa.Select.show({
          title: fmtDate(from) + ' — ' + ch.name,
          items: items,
          onSelect: function (sel) {
            Lampa.Controller.toggle('content');
            playArchive(ch, sel.prog);
          },
          onBack: function () { openArchive(ch, (ch.catchup && ch.catchup.days) || 1); }
        });
      }, function () {
        comp.activity.loader(false);
        Lampa.Noty.show(lang('error'));
        Lampa.Controller.toggle('content');
      });
    }
  }

  // ---------------------------------------------------------------------------
  //  Translations
  // ---------------------------------------------------------------------------

  Lampa.Lang.add({
    iptv2_add: { ru: 'Добавить', en: 'Add', uk: 'Додати' },
    iptv2_open: { ru: 'Открыть', en: 'Open', uk: 'Відкрити' },
    iptv2_empty: { ru: 'Нет плейлистов<br>Добавьте M3U ссылку', en: 'No playlists<br>Add an M3U URL', uk: 'Немає плейлистів<br>Додайте M3U посилання' },
    iptv2_url_title: { ru: 'URL плейлиста (M3U)', en: 'Playlist URL (M3U)', uk: 'URL плейлиста (M3U)' },
    iptv2_name_title: { ru: 'Название плейлиста', en: 'Playlist name', uk: 'Назва плейлиста' },
    iptv2_ch: { ru: 'каналов', en: 'channels', uk: 'каналів' },
    iptv2_all: { ru: 'Все каналы', en: 'All channels', uk: 'Усі канали' },
    iptv2_categories: { ru: 'Категории', en: 'Categories', uk: 'Категорії' },
    iptv2_favorites: { ru: 'Избранное', en: 'Favorites', uk: 'Обране' },
    iptv2_no_ch: { ru: 'Нет каналов', en: 'No channels', uk: 'Немає каналів' },
    iptv2_no_stream: { ru: 'Не удалось получить поток', en: 'Failed to get stream', uk: 'Не вдалося отримати потік' },
    iptv2_no_epg: { ru: 'Нет программы передач', en: 'No EPG data', uk: 'Немає телепрограми' },
    iptv2_refresh: { ru: 'Обновить', en: 'Refresh', uk: 'Оновити' },
    iptv2_delete: { ru: 'Удалить', en: 'Delete', uk: 'Видалити' },
    iptv2_deleted: { ru: 'Удалён', en: 'Deleted', uk: 'Видалено' },
    iptv2_refreshed: { ru: 'Обновлён', en: 'Refreshed', uk: 'Оновлено' },
    iptv2_error: { ru: 'Ошибка', en: 'Error', uk: 'Помилка' },
    iptv2_search: { ru: 'Поиск', en: 'Search', uk: 'Пошук' },
    iptv2_search_title: { ru: 'Поиск каналов', en: 'Search channels', uk: 'Пошук каналів' },
    iptv2_play: { ru: 'Смотреть', en: 'Watch', uk: 'Дивитися' },
    iptv2_restart: { ru: 'Смотреть сначала', en: 'Watch from start', uk: 'Дивитися спочатку' },
    iptv2_archive: { ru: 'Архив передач', en: 'Archive', uk: 'Архів передач' },
    iptv2_fav_add: { ru: 'В избранное', en: 'Add to favorites', uk: 'До обраного' },
    iptv2_fav_del: { ru: 'Убрать из избранного', en: 'Remove from favorites', uk: 'Прибрати з обраного' },
    iptv2_fav_top: { ru: 'В начало списка', en: 'Move to top', uk: 'На початок' },
    iptv2_fav_up: { ru: 'Выше', en: 'Move up', uk: 'Вище' },
    iptv2_fav_down: { ru: 'Ниже', en: 'Move down', uk: 'Нижче' },
    iptv2_epg_on: { ru: 'Показать панель передач', en: 'Show EPG panel', uk: 'Показати панель передач' },
    iptv2_epg_off: { ru: 'Скрыть панель передач', en: 'Hide EPG panel', uk: 'Сховати панель передач' },
    iptv2_now: { ru: 'Сейчас', en: 'Now', uk: 'Зараз' },
    iptv2_later: { ru: 'Далее', en: 'Next', uk: 'Далі' },
    iptv2_today: { ru: 'Сегодня', en: 'Today', uk: 'Сьогодні' },
    iptv2_yesterday: { ru: 'Вчера', en: 'Yesterday', uk: 'Вчора' }
  });

  // ---------------------------------------------------------------------------
  //  Menu + init
  // ---------------------------------------------------------------------------

  function addToMenu() {
    var btn = $('<li class="menu__item selector"><div class="menu__ico"><svg height="36" viewBox="0 0 38 36" fill="none" xmlns="http://www.w3.org/2000/svg"><rect x="2" y="8" width="34" height="21" rx="3" stroke="currentColor" stroke-width="3"/><line x1="13.09" y1="2.35" x2="16.35" y2="6.91" stroke="currentColor" stroke-width="3" stroke-linecap="round"/><line x1="1.5" y1="-1.5" x2="9.32" y2="-1.5" transform="matrix(-0.758 0.652 0.652 0.758 26.2 2)" stroke="currentColor" stroke-width="3" stroke-linecap="round"/><line x1="9.5" y1="34.5" x2="29.5" y2="34.5" stroke="currentColor" stroke-width="3" stroke-linecap="round"/></svg></div><div class="menu__text">IPTV</div></li>');
    btn.on('hover:enter', function () {
      Lampa.Activity.push({ url: '', title: 'IPTV', component: COMPONENT, view: 'playlists', page: 1 });
    });
    $('.menu .menu__list').eq(0).append(btn);
  }

  function startPlugin() {
    window.plugin_iptv2_ready = true;
    Lampa.Component.add(COMPONENT, Component);
    if (window.appready) addToMenu();
    else Lampa.Listener.follow('app', function (e) { if (e.type === 'ready') addToMenu(); });
  }

  if (!window.plugin_iptv2_ready) startPlugin();
})();
