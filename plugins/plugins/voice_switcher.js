(function () {
  'use strict';

  if (window.lampac_voice_switcher) return;
  window.lampac_voice_switcher = true;

  var voices = [];
  var currentVoiceIndex = 0;
  var isPlayerActive = false;
  var currentSeason = null;
  var currentEpisode = null;
  var hlsInstance = null;
  var videoEl = null;
  var hlsPatched = false;

  // ── HLS instance capture ───────────────────────
  function captureHls() {
    if (typeof Hls === 'undefined' || hlsPatched) return;
    hlsPatched = true;

    var origAttach = Hls.prototype.attachMedia;
    Hls.prototype.attachMedia = function (media) {
      hlsInstance = this;
      videoEl = media;
      return origAttach.call(this, media);
    };
  }

  // ── Auth URL builder (same as online.js account()) ──
  function buildAuthUrl(url) {
    var u = url;
    if (u.indexOf('account_email=') === -1) {
      var email = Lampa.Storage.get('account_email');
      if (email) u += (u.indexOf('?') >= 0 ? '&' : '?') + 'account_email=' + encodeURIComponent(email);
    }
    if (u.indexOf('uid=') === -1) {
      var uid = Lampa.Storage.get('lampac_unic_id', '');
      if (uid) u += (u.indexOf('?') >= 0 ? '&' : '?') + 'uid=' + encodeURIComponent(uid);
    }
    return u;
  }

  // ── Parse video items from balancer HTML ────────
  function parseVideoItems(html) {
    var items = [];
    try {
      var $html = $('<div>' + html + '</div>');
      $html.find('.videos__item').each(function () {
        var $item = $(this);
        try {
          var data = JSON.parse($item.attr('data-json'));
          data.season = parseInt($item.attr('s')) || 0;
          data.episode = parseInt($item.attr('e')) || 0;
          data.text = $item.text().trim();
          if (data.method === 'play' || data.method === 'call') {
            items.push(data);
          }
        } catch (e) {}
      });
    } catch (e) {}
    return items;
  }

  // ── Get actual stream URL from item ─────────────
  function resolveStreamUrl(item, callback) {
    if (item.method === 'play') {
      // Direct stream URL
      var url = item.url || '';
      if (url.indexOf(' or ') !== -1) url = url.split(' or ')[0];
      callback(url);
    } else if (item.method === 'call') {
      // Need to fetch the URL to get stream
      var fetchUrl = buildAuthUrl(item.url);
      $.ajax({
        url: fetchUrl,
        type: 'GET',
        dataType: 'text',
        timeout: 10000,
        headers: { 'X-Kit-AesGcm': Lampa.Storage.get('aesgcmkey', '') },
        success: function (str) {
          try {
            var json = JSON.parse(str);
            var url = json.url || '';
            if (url.indexOf(' or ') !== -1) url = url.split(' or ')[0];
            callback(url);
          } catch (e) {
            callback('');
          }
        },
        error: function () { callback(''); }
      });
    } else {
      callback('');
    }
  }

  // ── Show voice selector UI ──────────────────────
  function showVoiceSelector() {
    if (voices.length <= 1) return;

    var items = voices.map(function (v, i) {
      var title = v.title;
      if (i === currentVoiceIndex) title = '● ' + title;
      return {
        title: title,
        voice_index: i,
        voice_url: v.url
      };
    });

    Lampa.Select.show({
      title: 'Озвучка',
      items: items,
      onSelect: function (selected) {
        if (selected.voice_index !== currentVoiceIndex) {
          switchVoice(selected.voice_index, selected.voice_url);
        } else {
          Lampa.Controller.toggle('player');
        }
      },
      onBack: function () {
        Lampa.Controller.toggle('player');
      }
    });
  }

  // ── Switch to a different voice ─────────────────
  function switchVoice(index, url) {
    Lampa.Noty.show('Загрузка озвучки...', false);

    var fetchUrl = buildAuthUrl(url);
    console.log('VoiceSwitcher', 'Fetching voice', index, ':', url);

    $.ajax({
      url: fetchUrl,
      type: 'GET',
      dataType: 'text',
      timeout: 15000,
      headers: { 'X-Kit-AesGcm': Lampa.Storage.get('aesgcmkey', '') },
      success: function (html) {
        var items = parseVideoItems(html);

        if (items.length === 0) {
          Lampa.Noty.show('Нет видео для этой озвучки');
          Lampa.Controller.toggle('player');
          return;
        }

        console.log('VoiceSwitcher', 'Parsed', items.length, 'video items from voice', index);

        // Find matching episode by season + episode
        var match = null;
        if (currentSeason && currentEpisode) {
          match = items.find(function (item) {
            return item.season == currentSeason && item.episode == currentEpisode;
          });
        }

        // Fallback: first item
        if (!match) match = items[0];

        console.log('VoiceSwitcher', 'Matched:', match.text || ('S' + match.season + 'E' + match.episode),
                    'method:', match.method);

        // Get actual stream URL
        resolveStreamUrl(match, function (streamUrl) {
          if (!streamUrl) {
            Lampa.Noty.show('Не удалось получить ссылку');
            Lampa.Controller.toggle('player');
            return;
          }

          console.log('VoiceSwitcher', 'Stream URL resolved, switching...');

          // Save current position
          var savedTime = 0;
          var video = videoEl || (Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null);
          if (video && video.currentTime > 0) {
            savedTime = video.currentTime;
          }

          currentVoiceIndex = index;

          // Switch the stream
          switchStream(streamUrl, savedTime);

          var voiceName = voices[index] ? voices[index].title : ('Voice ' + index);
          Lampa.Noty.show('🔊 ' + voiceName);
        });
      },
      error: function () {
        Lampa.Noty.show('Ошибка загрузки озвучки');
        Lampa.Controller.toggle('player');
      }
    });
  }

  // ── Switch the actual stream ────────────────────
  function switchStream(url, savedTime) {
    // Strategy 1: hls.js
    if (hlsInstance && url.indexOf('.m3u8') !== -1) {
      try {
        hlsInstance.loadSource(url);
        hlsInstance.startLoad();

        // Restore position after new stream loads
        var onManifest = function () {
          hlsInstance.off(Hls.Events.MANIFEST_PARSED, onManifest);
          if (savedTime > 0) {
            setTimeout(function () {
              var v = videoEl || (Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null);
              if (v) {
                v.currentTime = savedTime;
                console.log('VoiceSwitcher', 'Position restored to', savedTime.toFixed(1) + 's');
              }
            }, 500);
          }
        };
        hlsInstance.on(Hls.Events.MANIFEST_PARSED, onManifest);

        Lampa.Controller.toggle('player');
        return;
      } catch (e) {
        console.log('VoiceSwitcher', 'HLS switch failed, trying fallback:', e);
      }
    }

    // Strategy 2: Direct video.src change
    var video = videoEl || (Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null);
    if (video) {
      video.src = url;
      video.load();
      video.addEventListener('canplay', function onCanPlay() {
        video.removeEventListener('canplay', onCanPlay);
        if (savedTime > 0) {
          video.currentTime = savedTime;
        }
        video.play();
      });
    }

    Lampa.Controller.toggle('player');
  }

  // ── Keyboard shortcut ──────────────────────────
  document.addEventListener('keydown', function (e) {
    // Key "V" for Voice selector
    if (e.keyCode === 86 && !e.ctrlKey && !e.altKey && !e.metaKey && !e.shiftKey) {
      var tag = (e.target.tagName || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea') return;

      if (!isPlayerActive || voices.length <= 1) return;

      e.preventDefault();
      e.stopPropagation();
      showVoiceSelector();
    }
  }, true);

  // ── Player hooks ───────────────────────────────
  function onPlayerStart(data) {
    isPlayerActive = true;
    hlsInstance = null;
    videoEl = null;
    voices = [];
    currentVoiceIndex = 0;
    currentSeason = null;
    currentEpisode = null;

    captureHls();

    if (data) {
      if (data.voices && data.voices.length > 1) {
        voices = data.voices;
        currentVoiceIndex = data.voice_index || 0;

        console.log('VoiceSwitcher', voices.length, 'voices available, current:',
                    voices[currentVoiceIndex] ? voices[currentVoiceIndex].title : '?');
      }

      currentSeason = data.season || null;
      currentEpisode = data.episode || null;
    }
  }

  function onPlayerDestroy() {
    isPlayerActive = false;
    hlsInstance = null;
    videoEl = null;
    voices = [];
  }

  // ── Init ───────────────────────────────────────
  function initPlugin() {
    captureHls();

    Lampa.Player.listener.follow('start', onPlayerStart);
    Lampa.Player.listener.follow('destroy', onPlayerDestroy);

    console.log('VoiceSwitcher', 'In-player voice switcher initialized (press V)');
  }

  if (window.appready) {
    initPlugin();
  } else {
    Lampa.Listener.follow('app', function (e) {
      if (e.type === 'ready') initPlugin();
    });
  }
})();
