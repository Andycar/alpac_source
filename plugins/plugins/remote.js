(function () {
  'use strict';

  if (window.lampac_remote_receiver) return;
  window.lampac_remote_receiver = true;

  // ═══════════════════════════════════════════════════════════
  //  Remote Control Receiver — handles commands from TG Mini App
  //  Listens for NWS events dispatched via lwsEvent (sync_v2)
  // ═══════════════════════════════════════════════════════════

  var STATUS_INTERVAL = 3000;
  var KEYS = {
    left: 37, up: 38, right: 39, down: 40,
    enter: 13, back: 8, escape: 27, space: 32
  };

  var statusTimer = null;

  function log(msg) {
    try { console.log('[Remote]', msg); } catch(e) {}
  }

  // ─── Player Status ──────────────────────────────────────
  function getPlayerStatus() {
    var status = { type: 'status', playing: false };
    try {
      var player = Lampa.Player;
      if (!player) return status;

      var video = player.video ? player.video() : null;
      if (video && video.duration) {
        status.playing = !video.paused;
        status.currentTime = Math.floor(video.currentTime || 0);
        status.duration = Math.floor(video.duration || 0);
        status.volume = Math.round((video.volume || 0) * 100);
        status.muted = video.muted || false;
      }

      var activity = Lampa.Activity.active();
      if (activity && activity.card) {
        status.title = activity.card.title || activity.card.name || '';
      }
    } catch(e) {}
    return status;
  }

  function broadcastStatus() {
    try {
      if (!window.lwsEvent || !window.lwsEvent.send) return;

      var status = getPlayerStatus();
      var activity = Lampa.Activity.active();
      if (activity) {
        status.page = activity.component || '';
      }

      window.lwsEvent.send('remote:status', JSON.stringify(status));
    } catch(e) {}
  }

  function startStatusBroadcast() {
    if (statusTimer) return;
    statusTimer = setInterval(broadcastStatus, STATUS_INTERVAL);
    broadcastStatus();
  }

  // ─── Key Simulation ──────────────────────────────────────
  function simulateKey(keyCode) {
    try {
      var ev = new KeyboardEvent('keydown', {
        keyCode: keyCode, which: keyCode, bubbles: true
      });
      document.dispatchEvent(ev);
    } catch(e) {}
  }

  // ─── Command Handlers ──────────────────────────────────
  var handlers = {
    up:    function() { simulateKey(KEYS.up); },
    down:  function() { simulateKey(KEYS.down); },
    left:  function() { simulateKey(KEYS.left); },
    right: function() { simulateKey(KEYS.right); },
    enter: function() { simulateKey(KEYS.enter); },
    back:  function() { simulateKey(KEYS.back); },

    play: function() {
      try { if (Lampa.Player.play) Lampa.Player.play(); } catch(e) {}
    },
    pause: function() {
      try { if (Lampa.Player.pause) Lampa.Player.pause(); } catch(e) {}
    },
    toggle: function() {
      try {
        var video = Lampa.Player.video ? Lampa.Player.video() : null;
        if (video) { if (video.paused) video.play(); else video.pause(); }
      } catch(e) {}
    },
    stop: function() {
      try { if (Lampa.Player.close) Lampa.Player.close(); } catch(e) {}
    },
    forward: function(data) {
      try {
        var sec = (data && data.seconds) || 30;
        var video = Lampa.Player.video ? Lampa.Player.video() : null;
        if (video) video.currentTime = Math.min(video.currentTime + sec, video.duration);
      } catch(e) {}
    },
    rewind: function(data) {
      try {
        var sec = (data && data.seconds) || 30;
        var video = Lampa.Player.video ? Lampa.Player.video() : null;
        if (video) video.currentTime = Math.max(video.currentTime - sec, 0);
      } catch(e) {}
    },
    seek: function(data) {
      try {
        if (data && typeof data.time === 'number') {
          var video = Lampa.Player.video ? Lampa.Player.video() : null;
          if (video) video.currentTime = data.time;
        }
      } catch(e) {}
    },
    volume: function(data) {
      try {
        var video = Lampa.Player.video ? Lampa.Player.video() : null;
        if (video && data) {
          if (typeof data.level === 'number')
            video.volume = Math.max(0, Math.min(1, data.level / 100));
          if (data.delta)
            video.volume = Math.max(0, Math.min(1, video.volume + data.delta / 100));
        }
      } catch(e) {}
    },
    mute: function() {
      try {
        var video = Lampa.Player.video ? Lampa.Player.video() : null;
        if (video) video.muted = !video.muted;
      } catch(e) {}
    },

    home: function() {
      try { Lampa.Activity.push({ url: '', component: 'main' }); } catch(e) {}
    },
    search: function(data) {
      try {
        if (data && data.query) {
          Lampa.Activity.push({
            url: '', component: 'search',
            search: data.query, search_one: data.query
          });
        }
      } catch(e) {}
    },
    open: function(data) {
      try {
        if (data && data.card) {
          Lampa.Activity.push({
            url: '', component: 'full',
            id: data.card.id, card: data.card,
            source: data.card.source || 'tmdb'
          });
        }
      } catch(e) {}
    },
    settings: function() {
      try { Lampa.Activity.push({ url: '', component: 'settings' }); } catch(e) {}
    },
    fullscreen: function() {
      try {
        if (document.fullscreenElement) document.exitFullscreen();
        else document.documentElement.requestFullscreen();
      } catch(e) {}
    },
    ping: function() { broadcastStatus(); },
    getstatus: function() { broadcastStatus(); }
  };

  // ─── Event Dispatcher ──────────────────────────────────
  function handleRemoteEvent(name, data) {
    if (!name || name.indexOf('remote:') !== 0) return;
    var cmd = name.replace('remote:', '');
    if (cmd === 'status') return; // ignore our own status broadcasts

    log('cmd: ' + cmd);

    var payload = null;
    if (data) {
      try { payload = typeof data === 'string' ? JSON.parse(data) : data; }
      catch(e) { payload = data; }
    }

    var handler = handlers[cmd];
    if (handler) handler(payload);
  }

  // ─── Init ──────────────────────────────────────────────
  function initRemoteReceiver() {
    // Listen for lwsEvent (dispatched by sync_v2/invc-ws.js on NWS "event" messages)
    document.addEventListener('lwsEvent', function(e) {
      if (e.detail && e.detail.name && e.detail.name.indexOf('remote:') === 0) {
        handleRemoteEvent(e.detail.name, e.detail.data);
      }
    });

    startStatusBroadcast();
    log('receiver initialized');
  }

  if (window.appready) {
    initRemoteReceiver();
  } else {
    Lampa.Listener.follow('app', function(e) {
      if (e.type === 'ready') initRemoteReceiver();
    });
  }

})();
