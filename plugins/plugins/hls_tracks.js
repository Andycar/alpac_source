(function () {
  'use strict';

  if (window.lampac_hls_tracks_plugin) return;
  window.lampac_hls_tracks_plugin = true;

  var hlsInstance = null;
  var videoEl = null;
  var isPlayerActive = false;
  var tracksApplied = false;
  var hlsPatched = false;

  // ── Capture HLS instance ───────────────────────
  function captureHls() {
    if (typeof Hls === 'undefined' || hlsPatched) return;
    hlsPatched = true;

    var origAttach = Hls.prototype.attachMedia;
    Hls.prototype.attachMedia = function (media) {
      hlsInstance = this;
      videoEl = media;
      bindHlsEvents(this);
      return origAttach.call(this, media);
    };
  }

  // ── Bind HLS events for audio track discovery ──
  function bindHlsEvents(hls) {
    if (!hls || hls.__hls_tracks_bound) return;
    hls.__hls_tracks_bound = true;

    hls.on(Hls.Events.AUDIO_TRACKS_UPDATED, function () {
      if (!isPlayerActive) return;
      applyAudioTracks(hls);
    });

    hls.on(Hls.Events.MANIFEST_PARSED, function () {
      if (!isPlayerActive) return;
      setTimeout(function () {
        applyAudioTracks(hls);
      }, 800);
    });
  }

  // ── Format channel info ────────────────────────
  function formatChannels(track) {
    if (!track.attrs) return '';
    var ch = track.attrs.CHANNELS;
    if (!ch) return '';
    if (ch === '6') return '5.1';
    if (ch === '8') return '7.1';
    if (ch === '2') return 'stereo';
    if (ch === '1') return 'mono';
    return ch;
  }

  // ── Build track label ──────────────────────────
  function buildLabel(track, index) {
    var parts = [];

    // Language code
    var lang = (track.lang || '').toUpperCase();
    if (lang) parts.push(lang);

    // Track name
    var name = track.name || '';
    if (name && name.toUpperCase() !== lang) {
      parts.push(name);
    }

    // If nothing yet, use generic label
    if (parts.length === 0) {
      parts.push('Track ' + (index + 1));
    }

    // Channel info
    var channels = formatChannels(track);
    if (channels) parts.push('(' + channels + ')');

    return parts.join(' ');
  }

  // ── Apply audio tracks to Lampa UI ─────────────
  function applyAudioTracks(hls) {
    if (!hls || !hls.audioTracks || hls.audioTracks.length <= 1) return;
    if (tracksApplied) return;
    tracksApplied = true;

    var tracks = hls.audioTracks;
    var currentIndex = hls.audioTrack;
    var lampaTrackList = [];

    console.log('HLS Tracks', 'Found', tracks.length, 'audio tracks');

    tracks.forEach(function (track, i) {
      var label = buildLabel(track, i);

      console.log('HLS Tracks', 'Track', i + ':', label,
                  'lang=' + (track.lang || '?'),
                  'group=' + (track.groupId || '?'),
                  'default=' + (track['default'] || false));

      var elem = {
        index: i,
        language: track.lang || '',
        label: label,
        ghost: false,
        selected: i === currentIndex
      };

      // Define enabled property for Lampa PlayerPanel integration
      Object.defineProperty(elem, 'enabled', {
        set: function (v) {
          if (v && hlsInstance) {
            console.log('HLS Tracks', 'Switching to track', elem.index, ':', elem.label);
            hlsInstance.audioTrack = elem.index;

            // Update selected states
            lampaTrackList.forEach(function (t) {
              t.selected = t.index === elem.index;
            });

            Lampa.Noty.show('🔊 ' + elem.label);
          }
        },
        get: function () {
          return hlsInstance ? hlsInstance.audioTrack === elem.index : false;
        }
      });

      lampaTrackList.push(elem);
    });

    if (lampaTrackList.length > 1) {
      console.log('HLS Tracks', 'Registering', lampaTrackList.length, 'audio tracks in PlayerPanel');
      try {
        Lampa.PlayerPanel.setTracks(lampaTrackList);
      } catch (e) {
        console.log('HLS Tracks', 'Failed to set tracks:', e);
      }
    }
  }

  // ── Player hooks ───────────────────────────────
  function onPlayerStart() {
    isPlayerActive = true;
    tracksApplied = false;
    hlsInstance = null;
    videoEl = null;

    captureHls();

    // Retry audio track detection after delay (some HLS manifests load slowly)
    setTimeout(function () {
      if (!isPlayerActive || tracksApplied) return;
      if (hlsInstance) {
        applyAudioTracks(hlsInstance);
      }
    }, 4000);
  }

  function onPlayerDestroy() {
    isPlayerActive = false;
    tracksApplied = false;
    hlsInstance = null;
    videoEl = null;
  }

  // ── Init ───────────────────────────────────────
  function initPlugin() {
    captureHls();

    Lampa.Player.listener.follow('start', onPlayerStart);
    Lampa.Player.listener.follow('destroy', onPlayerDestroy);

    console.log('HLS Tracks', 'HLS audio track switcher initialized');
  }

  if (window.appready) {
    initPlugin();
  } else {
    Lampa.Listener.follow('app', function (e) {
      if (e.type === 'ready') initPlugin();
    });
  }
})();
