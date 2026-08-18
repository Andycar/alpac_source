(function () {
  'use strict';

  if (window.lampac_stats_overlay) return;
  window.lampac_stats_overlay = true;

  var STORAGE_KEY = 'stats_overlay_visible';
  var overlay = null;
  var updateTimer = null;
  var autoHideTimer = null;
  var hlsInstance = null;
  var videoEl = null;
  var isPlayerActive = false;

  // ═══════════════════════════════════════════════════
  //  CSS
  // ═══════════════════════════════════════════════════

  var CSS_TEXT = [

    '@keyframes so-slide-in {',
    '  from { opacity: 0; transform: translateX(30px) scale(0.95); }',
    '  to { opacity: 1; transform: translateX(0) scale(1); }',
    '}',

    '@keyframes so-pulse-dot {',
    '  0%, 100% { opacity: 0.4; }',
    '  50% { opacity: 1; }',
    '}',

    '@keyframes so-bar-fill {',
    '  from { width: 0%; }',
    '}',

    // ── Container ────────────────────────────────
    '.stats-overlay {',
    '  position: fixed; top: 1.2em; right: 1.2em; z-index: 999999;',
    '  background: linear-gradient(135deg, rgba(12,12,25,0.72) 0%, rgba(20,10,30,0.68) 100%);',
    '  backdrop-filter: blur(24px) saturate(1.8);',
    '  -webkit-backdrop-filter: blur(24px) saturate(1.8);',
    '  color: #e0e0e0;',
    '  font-family: "SF Mono", "Consolas", "Menlo", "Liberation Mono", monospace;',
    '  font-size: 11.5px; line-height: 1.5;',
    '  padding: 14px 18px 12px;',
    '  border-radius: 1em;',
    '  border: 1px solid rgba(255,255,255,0.1);',
    '  box-shadow: 0 8px 32px rgba(0,0,0,0.45),',
    '              inset 0 1px 0 rgba(255,255,255,0.08);',
    '  pointer-events: none;',
    '  min-width: 280px; max-width: 320px;',
    '  animation: so-slide-in 0.35s cubic-bezier(0.16, 1, 0.3, 1) forwards;',
    '  transition: opacity 0.3s ease;',
    '}',

    '.stats-overlay.hidden {',
    '  opacity: 0; pointer-events: none;',
    '  animation: none;',
    '}',

    // ── Header ────────────────────────────────────
    '.so-header {',
    '  display: flex; align-items: center; justify-content: space-between;',
    '  margin-bottom: 10px; padding-bottom: 8px;',
    '  border-bottom: 1px solid rgba(255,255,255,0.08);',
    '}',

    '.so-header__title {',
    '  font-size: 10.5px; font-weight: 700;',
    '  text-transform: uppercase; letter-spacing: 1.2px;',
    '  color: rgba(255,255,255,0.45);',
    '}',

    '.so-header__live {',
    '  display: flex; align-items: center; gap: 5px;',
    '  font-size: 9.5px; font-weight: 600;',
    '  text-transform: uppercase; letter-spacing: 0.8px;',
    '  color: rgba(255,255,255,0.35);',
    '}',

    '.so-header__dot {',
    '  width: 6px; height: 6px;',
    '  border-radius: 50%;',
    '  background: #30d158;',
    '  box-shadow: 0 0 6px rgba(48,209,88,0.5);',
    '  animation: so-pulse-dot 1.5s ease-in-out infinite;',
    '}',

    // ── Section ────────────────────────────────────
    '.so-section {',
    '  margin-bottom: 8px;',
    '}',

    '.so-section:last-child {',
    '  margin-bottom: 0;',
    '}',

    '.so-section__label {',
    '  font-size: 9px; font-weight: 600;',
    '  text-transform: uppercase; letter-spacing: 1px;',
    '  color: rgba(255,255,255,0.3);',
    '  margin-bottom: 4px;',
    '}',

    // ── Row ────────────────────────────────────────
    '.so-row {',
    '  display: flex; justify-content: space-between; align-items: center;',
    '  padding: 1px 0;',
    '}',

    '.so-label {',
    '  color: rgba(255,255,255,0.5);',
    '  font-size: 11px;',
    '}',

    '.so-value {',
    '  color: #fff; font-weight: 500; text-align: right;',
    '  font-size: 11px;',
    '}',

    // ── Badges ─────────────────────────────────────
    '.so-badge {',
    '  display: inline-block;',
    '  padding: 1px 6px;',
    '  border-radius: 3px;',
    '  font-size: 10px; font-weight: 700;',
    '  letter-spacing: 0.5px;',
    '}',

    '.so-badge--4k {',
    '  background: linear-gradient(135deg, rgba(255,180,100,0.25), rgba(255,120,50,0.2));',
    '  color: #ffb464;',
    '  border: 1px solid rgba(255,180,100,0.3);',
    '}',

    '.so-badge--fhd {',
    '  background: linear-gradient(135deg, rgba(100,200,255,0.2), rgba(60,150,255,0.15));',
    '  color: #7cc8ff;',
    '  border: 1px solid rgba(100,200,255,0.25);',
    '}',

    '.so-badge--hd {',
    '  background: rgba(255,255,255,0.08);',
    '  color: rgba(255,255,255,0.7);',
    '  border: 1px solid rgba(255,255,255,0.12);',
    '}',

    '.so-badge--sd {',
    '  background: rgba(255,255,255,0.05);',
    '  color: rgba(255,255,255,0.45);',
    '  border: 1px solid rgba(255,255,255,0.08);',
    '}',

    '.so-badge--codec {',
    '  background: rgba(255,255,255,0.06);',
    '  color: rgba(255,255,255,0.65);',
    '  border: 1px solid rgba(255,255,255,0.1);',
    '  font-weight: 600;',
    '}',

    '.so-badges {',
    '  display: flex; gap: 5px; align-items: center;',
    '}',

    // ── Visual bar ─────────────────────────────────
    '.so-bar-wrap {',
    '  margin-top: 3px;',
    '}',

    '.so-bar {',
    '  height: 3px;',
    '  background: rgba(255,255,255,0.08);',
    '  border-radius: 2px;',
    '  overflow: hidden;',
    '}',

    '.so-bar__fill {',
    '  height: 100%;',
    '  border-radius: 2px;',
    '  transition: width 0.8s ease, background 0.5s ease;',
    '  animation: so-bar-fill 0.6s ease-out;',
    '}',

    '.so-bar__fill--good {',
    '  background: linear-gradient(90deg, #30d158, #34c759);',
    '  box-shadow: 0 0 6px rgba(48,209,88,0.3);',
    '}',

    '.so-bar__fill--warn {',
    '  background: linear-gradient(90deg, #ffd60a, #ff9f0a);',
    '  box-shadow: 0 0 6px rgba(255,214,10,0.3);',
    '}',

    '.so-bar__fill--bad {',
    '  background: linear-gradient(90deg, #ff453a, #ff6961);',
    '  box-shadow: 0 0 6px rgba(255,69,58,0.3);',
    '}',

    // ── Status colors ──────────────────────────────
    '.so-value.good { color: #30d158; }',
    '.so-value.warn { color: #ffd60a; }',
    '.so-value.bad  { color: #ff453a; }',

    // ── Separator ──────────────────────────────────
    '.so-sep {',
    '  height: 1px;',
    '  background: rgba(255,255,255,0.06);',
    '  margin: 6px 0;',
    '}',

    // ── Footer ──────────────────────────────────────
    '.so-footer {',
    '  display: flex; justify-content: space-between; align-items: center;',
    '  margin-top: 6px; padding-top: 6px;',
    '  border-top: 1px solid rgba(255,255,255,0.06);',
    '  font-size: 10px;',
    '  color: rgba(255,255,255,0.3);',
    '}',

  ].join('\n');

  var styleEl = document.createElement('style');
  styleEl.textContent = CSS_TEXT;
  document.head.appendChild(styleEl);

  // ═══════════════════════════════════════════════════
  //  DOM
  // ═══════════════════════════════════════════════════

  function createOverlay() {
    if (overlay) return;
    overlay = document.createElement('div');
    overlay.className = 'stats-overlay hidden';
    overlay.innerHTML = [
      // Header
      '<div class="so-header">',
      '  <span class="so-header__title">Stats for Nerds</span>',
      '  <span class="so-header__live"><span class="so-header__dot"></span>LIVE</span>',
      '</div>',

      // Video section
      '<div class="so-section">',
      '  <div class="so-section__label">Video</div>',
      '  <div class="so-row">',
      '    <span class="so-label">Resolution</span>',
      '    <span class="so-badges" id="so-res-badges"></span>',
      '  </div>',
      '  <div class="so-row">',
      '    <span class="so-label">Codec</span>',
      '    <span class="so-badges" id="so-codec-badges"></span>',
      '  </div>',
      '  <div class="so-row">',
      '    <span class="so-label">Bitrate</span>',
      '    <span class="so-value" id="so-bitrate">—</span>',
      '  </div>',
      '  <div class="so-bar-wrap" id="so-bitrate-bar-wrap">',
      '    <div class="so-bar"><div class="so-bar__fill so-bar__fill--good" id="so-bitrate-bar" style="width:0%"></div></div>',
      '  </div>',
      '</div>',

      '<div class="so-sep"></div>',

      // Audio section
      '<div class="so-section">',
      '  <div class="so-section__label">Audio</div>',
      '  <div class="so-row">',
      '    <span class="so-label">Track</span>',
      '    <span class="so-value" id="so-audio">—</span>',
      '  </div>',
      '</div>',

      '<div class="so-sep"></div>',

      // Network section
      '<div class="so-section">',
      '  <div class="so-section__label">Network</div>',
      '  <div class="so-row">',
      '    <span class="so-label">Buffer</span>',
      '    <span class="so-value" id="so-buffer">—</span>',
      '  </div>',
      '  <div class="so-bar-wrap" id="so-buffer-bar-wrap">',
      '    <div class="so-bar"><div class="so-bar__fill so-bar__fill--good" id="so-buffer-bar" style="width:0%"></div></div>',
      '  </div>',
      '  <div class="so-row" style="margin-top:3px">',
      '    <span class="so-label">Bandwidth</span>',
      '    <span class="so-value" id="so-bw">—</span>',
      '  </div>',
      '</div>',

      '<div class="so-sep"></div>',

      // Performance section
      '<div class="so-section">',
      '  <div class="so-section__label">Performance</div>',
      '  <div class="so-row">',
      '    <span class="so-label">Frames dropped</span>',
      '    <span class="so-value" id="so-frames">—</span>',
      '  </div>',
      '  <div class="so-bar-wrap" id="so-frames-bar-wrap">',
      '    <div class="so-bar"><div class="so-bar__fill so-bar__fill--good" id="so-frames-bar" style="width:0%"></div></div>',
      '  </div>',
      '</div>',

      // Footer
      '<div class="so-footer">',
      '  <span id="so-player">hls.js</span>',
      '  <span id="so-uptime">0:00</span>',
      '</div>',

    ].join('\n');
    document.body.appendChild(overlay);
  }

  function removeOverlay() {
    if (overlay) {
      overlay.remove();
      overlay = null;
    }
  }

  // ═══════════════════════════════════════════════════
  //  HLS Capture
  // ═══════════════════════════════════════════════════

  function captureHls() {
    if (typeof Hls === 'undefined') return;
    var origAttach = Hls.prototype.attachMedia;
    if (origAttach.__stats_patched) return;

    Hls.prototype.attachMedia = function (media) {
      hlsInstance = this;
      return origAttach.call(this, media);
    };
    Hls.prototype.attachMedia.__stats_patched = true;
  }

  // ═══════════════════════════════════════════════════
  //  Format Helpers
  // ═══════════════════════════════════════════════════

  function fmtBitrate(bps) {
    if (!bps || bps <= 0) return '—';
    if (bps >= 1000000) return (bps / 1000000).toFixed(1) + ' Mbps';
    return (bps / 1000).toFixed(0) + ' kbps';
  }

  function getBufferAhead(video) {
    if (!video || !video.buffered || video.buffered.length === 0) return -1;
    try {
      var ct = video.currentTime;
      for (var i = 0; i < video.buffered.length; i++) {
        if (video.buffered.start(i) <= ct && ct <= video.buffered.end(i)) {
          return video.buffered.end(i) - ct;
        }
      }
    } catch (e) {}
    return -1;
  }

  function qualityBadge(h) {
    if (h >= 2160) return { text: '4K', cls: 'so-badge--4k' };
    if (h >= 1080) return { text: 'FHD', cls: 'so-badge--fhd' };
    if (h >= 720)  return { text: 'HD', cls: 'so-badge--hd' };
    return { text: 'SD', cls: 'so-badge--sd' };
  }

  var startTime = 0;

  function fmtUptime() {
    if (!startTime) return '0:00';
    var s = Math.floor((Date.now() - startTime) / 1000);
    var m = Math.floor(s / 60);
    s = s % 60;
    if (m >= 60) {
      var h = Math.floor(m / 60);
      m = m % 60;
      return h + ':' + (m < 10 ? '0' : '') + m + ':' + (s < 10 ? '0' : '') + s;
    }
    return m + ':' + (s < 10 ? '0' : '') + s;
  }

  // ═══════════════════════════════════════════════════
  //  Update Stats
  // ═══════════════════════════════════════════════════

  function updateStats() {
    if (!overlay || overlay.classList.contains('hidden')) return;
    var video = videoEl || (Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null);
    if (!video) return;

    var hls = hlsInstance;
    var level = null;
    var audioTrack = null;

    if (hls && hls.levels && hls.currentLevel >= 0) {
      level = hls.levels[hls.currentLevel];
    }
    if (hls && hls.audioTracks && hls.audioTrack >= 0) {
      audioTrack = hls.audioTracks[hls.audioTrack];
    }

    // ── Resolution + quality badge ───────────────
    var resBadges = document.getElementById('so-res-badges');
    if (resBadges) {
      var resW = 0, resH = 0;
      if (level && level.width && level.height) {
        resW = level.width; resH = level.height;
      } else if (video.videoWidth && video.videoHeight) {
        resW = video.videoWidth; resH = video.videoHeight;
      }

      if (resW && resH) {
        var qb = qualityBadge(resH);
        resBadges.innerHTML =
          '<span class="so-badge ' + qb.cls + '">' + qb.text + '</span>' +
          '<span class="so-value" style="margin-left:4px">' + resW + '×' + resH + '</span>';
      } else {
        resBadges.innerHTML = '<span class="so-value">—</span>';
      }
    }

    // ── Codec badge ──────────────────────────────
    var codecBadges = document.getElementById('so-codec-badges');
    if (codecBadges) {
      var videoCodec = '';
      if (level && level.codecSet) videoCodec = level.codecSet;
      else if (level && level.attrs && level.attrs.CODECS) videoCodec = level.attrs.CODECS;
      else if (level && level.codecs) videoCodec = level.codecs;

      if (videoCodec) {
        // Split codecs, show each as badge
        var codecs = videoCodec.split(',');
        var html = '';
        for (var ci = 0; ci < codecs.length; ci++) {
          var c = codecs[ci].trim();
          // Shorten common codec names
          var short = c;
          if (c.indexOf('avc1') === 0) short = 'H.264';
          else if (c.indexOf('hvc1') === 0 || c.indexOf('hev1') === 0) short = 'H.265';
          else if (c.indexOf('vp09') === 0 || c.indexOf('vp9') === 0) short = 'VP9';
          else if (c.indexOf('av01') === 0) short = 'AV1';
          else if (c.indexOf('mp4a') === 0) short = 'AAC';
          else if (c.indexOf('ac-3') === 0 || c.indexOf('ec-3') === 0) short = c.toUpperCase();
          else if (c.indexOf('opus') === 0) short = 'Opus';
          html += '<span class="so-badge so-badge--codec">' + short + '</span>';
        }
        codecBadges.innerHTML = html;
      } else {
        codecBadges.innerHTML = '<span class="so-value">—</span>';
      }
    }

    // ── Bitrate + bar ────────────────────────────
    var bitrate = level ? level.bitrate : 0;
    setText('so-bitrate', fmtBitrate(bitrate));

    var bitrateBar = document.getElementById('so-bitrate-bar');
    if (bitrateBar && bitrate > 0) {
      // Scale: 0-20 Mbps
      var pct = Math.min((bitrate / 20000000) * 100, 100);
      bitrateBar.style.width = pct.toFixed(1) + '%';
      bitrateBar.className = 'so-bar__fill' +
        (bitrate >= 8000000 ? ' so-bar__fill--fhd' : bitrate >= 3000000 ? ' so-bar__fill--good' : ' so-bar__fill--warn');
      // FHD bar gets blue tint
      if (bitrate >= 8000000) {
        bitrateBar.style.background = 'linear-gradient(90deg, #64b5f6, #42a5f5)';
        bitrateBar.style.boxShadow = '0 0 6px rgba(100,181,246,0.3)';
      } else if (bitrate >= 3000000) {
        bitrateBar.style.background = '';
        bitrateBar.style.boxShadow = '';
      } else {
        bitrateBar.style.background = '';
        bitrateBar.style.boxShadow = '';
      }
    }

    // ── Audio ─────────────────────────────────────
    var audioInfo = '—';
    if (audioTrack) {
      audioInfo = (audioTrack.name || audioTrack.lang || 'Track ' + hls.audioTrack);
      if (audioTrack.attrs && audioTrack.attrs.CHANNELS) {
        audioInfo += ' · ' + audioTrack.attrs.CHANNELS + 'ch';
      }
    }
    setText('so-audio', audioInfo);

    // ── Buffer + bar ─────────────────────────────
    var ahead = getBufferAhead(video);
    var bufferBar = document.getElementById('so-buffer-bar');

    if (ahead >= 0) {
      var bufStr = ahead.toFixed(1) + 's';
      var bufCls = ahead > 10 ? 'good' : ahead > 3 ? 'warn' : 'bad';
      setTextCls('so-buffer', bufStr, bufCls);

      if (bufferBar) {
        // Scale: 0-30s
        var bufPct = Math.min((ahead / 30) * 100, 100);
        bufferBar.style.width = bufPct.toFixed(1) + '%';
        bufferBar.className = 'so-bar__fill so-bar__fill--' + bufCls;
      }
    } else {
      setText('so-buffer', '—');
      if (bufferBar) bufferBar.style.width = '0%';
    }

    // ── Bandwidth ─────────────────────────────────
    var bw = hls ? hls.bandwidthEstimate : 0;
    setText('so-bw', bw > 0 ? (bw / 1000000).toFixed(1) + ' Mbps' : '—');

    // ── Frames + bar ──────────────────────────────
    var framesBar = document.getElementById('so-frames-bar');

    if (video.getVideoPlaybackQuality) {
      var q = video.getVideoPlaybackQuality();
      var total = q.totalVideoFrames;
      var dropped = q.droppedVideoFrames;

      if (total > 0) {
        var ratio = dropped / total;
        var pctDrop = (ratio * 100).toFixed(2);
        var frameCls = ratio > 0.05 ? 'bad' : ratio > 0.01 ? 'warn' : 'good';
        setTextCls('so-frames', dropped + ' (' + pctDrop + '%)', frameCls);

        if (framesBar) {
          // Inverse: good = full bar
          var healthPct = Math.max(0, (1 - ratio) * 100);
          framesBar.style.width = healthPct.toFixed(1) + '%';
          framesBar.className = 'so-bar__fill so-bar__fill--' + frameCls;
        }
      } else {
        setText('so-frames', '0');
        if (framesBar) { framesBar.style.width = '100%'; framesBar.className = 'so-bar__fill so-bar__fill--good'; }
      }
    } else {
      setText('so-frames', '—');
    }

    // ── Player + uptime ──────────────────────────
    var playerInfo = 'hls.js';
    if (typeof Hls !== 'undefined' && Hls.version) {
      playerInfo = 'hls.js v' + Hls.version;
    }
    setText('so-player', playerInfo);
    setText('so-uptime', fmtUptime());
  }

  function setText(id, text) {
    var el = document.getElementById(id);
    if (el) el.textContent = text;
  }

  function setTextCls(id, text, cls) {
    var el = document.getElementById(id);
    if (el) {
      el.textContent = text;
      el.className = 'so-value' + (cls ? ' ' + cls : '');
    }
  }

  // ═══════════════════════════════════════════════════
  //  Toggle
  // ═══════════════════════════════════════════════════

  function show() {
    if (!overlay) createOverlay();
    overlay.classList.remove('hidden');
    // Re-trigger entrance animation
    overlay.style.animation = 'none';
    overlay.offsetHeight; // reflow
    overlay.style.animation = '';
    startUpdating();
    resetAutoHide();
  }

  function hide() {
    if (overlay) overlay.classList.add('hidden');
    stopUpdating();
    clearAutoHide();
  }

  function toggle() {
    if (!isPlayerActive) return;
    if (overlay && !overlay.classList.contains('hidden')) {
      hide();
    } else {
      show();
    }
  }

  function startUpdating() {
    stopUpdating();
    updateStats();
    updateTimer = setInterval(updateStats, 1000);
  }

  function stopUpdating() {
    if (updateTimer) {
      clearInterval(updateTimer);
      updateTimer = null;
    }
  }

  function resetAutoHide() {
    clearAutoHide();
    autoHideTimer = setTimeout(function () {
      hide();
    }, 20000);
  }

  function clearAutoHide() {
    if (autoHideTimer) {
      clearTimeout(autoHideTimer);
      autoHideTimer = null;
    }
  }

  // ═══════════════════════════════════════════════════
  //  Keyboard
  // ═══════════════════════════════════════════════════

  document.addEventListener('keydown', function (e) {
    // Key "I" (info) — toggle stats overlay
    if (e.keyCode === 73 && !e.ctrlKey && !e.altKey && !e.metaKey && !e.shiftKey) {
      var tag = (e.target.tagName || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea') return;
      if (isPlayerActive) {
        e.preventDefault();
        e.stopPropagation();
        toggle();
      }
    }
  }, true);

  // ═══════════════════════════════════════════════════
  //  Player Hooks
  // ═══════════════════════════════════════════════════

  function onPlayerStart() {
    isPlayerActive = true;
    videoEl = null;
    hlsInstance = null;
    startTime = Date.now();
    captureHls();
  }

  function onCanPlay() {
    videoEl = Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null;
  }

  function onPlayerDestroy() {
    isPlayerActive = false;
    startTime = 0;
    hide();
    removeOverlay();
    videoEl = null;
    hlsInstance = null;
  }

  // ═══════════════════════════════════════════════════
  //  Init
  // ═══════════════════════════════════════════════════

  function initPlugin() {
    captureHls();
    Lampa.Player.listener.follow('start', onPlayerStart);
    Lampa.Player.listener.follow('destroy', onPlayerDestroy);
    Lampa.PlayerVideo.listener.follow('canplay', onCanPlay);

    console.log('StatsOverlay', 'Cinematic stats initialized');
  }

  if (window.appready) {
    initPlugin();
  } else {
    Lampa.Listener.follow('app', function (e) {
      if (e.type === 'ready') initPlugin();
    });
  }
})();
