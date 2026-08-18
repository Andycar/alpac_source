(function () {
  'use strict';

  if (window.lampac_dualsubs_plugin) return;
  window.lampac_dualsubs_plugin = true;

  var PRIMARY_KEY = 'dualsubs_primary';
  var SECONDARY_KEY = 'dualsubs_secondary';
  var ENABLED_KEY = 'dualsubs_enabled';

  var overlay = null;
  var secondaryCues = [];
  var syncTimer = null;
  var isPlayerActive = false;
  var videoEl = null;

  // ── IMDB ID resolution (multi-source) ───────────
  var imdbCache = {};

  function resolveIMDB(callback) {
    var card = null;
    try {
      var activity = Lampa.Activity.active();
      if (activity && activity.card) card = activity.card;
    } catch (e) {}

    if (!card) return callback('');

    // 1. Direct field
    if (card.imdb_id) return callback(card.imdb_id);

    // 2. External IDs sub-object
    if (card.external_ids && card.external_ids.imdb_id) {
      card.imdb_id = card.external_ids.imdb_id;
      return callback(card.imdb_id);
    }

    // 3. Cache hit
    var tmdbID = card.id;
    if (tmdbID && imdbCache[tmdbID]) {
      card.imdb_id = imdbCache[tmdbID];
      return callback(card.imdb_id);
    }

    // 4. Local /externalids endpoint
    var host = window.location.origin || '';
    try {
      var u = Lampa.Storage.get('lampac_unic_id', '');
      if (u) host = u;
    } catch (e) {}

    var exQuery = [];
    if (tmdbID) exQuery.push('id=' + tmdbID);
    if (card.kinopoisk_id) exQuery.push('kinopoisk_id=' + card.kinopoisk_id);

    if (exQuery.length > 0) {
      $.ajax({
        url: host + '/externalids?' + exQuery.join('&'),
        type: 'GET', dataType: 'json', timeout: 5000,
        success: function (json) {
          if (json && json.imdb_id) {
            card.imdb_id = json.imdb_id;
            if (tmdbID) imdbCache[tmdbID] = json.imdb_id;
            return callback(card.imdb_id);
          }
          fetchIMDBViaTMDB(card, callback);
        },
        error: function () { fetchIMDBViaTMDB(card, callback); }
      });
    } else {
      fetchIMDBViaTMDB(card, callback);
    }
  }

  function fetchIMDBViaTMDB(card, callback) {
    if (!card.id) return callback('');
    var type = card.name ? 'tv' : 'movie';
    var url = '';
    try { url = Lampa.TMDB.api(type + '/' + card.id + '/external_ids'); } catch (e) { return callback(''); }
    if (!url) return callback('');
    $.ajax({
      url: url, type: 'GET', dataType: 'json', timeout: 5000,
      success: function (data) {
        if (data && data.imdb_id) {
          card.imdb_id = data.imdb_id;
          imdbCache[card.id] = data.imdb_id;
          callback(data.imdb_id);
        } else { callback(''); }
      },
      error: function () { callback(''); }
    });
  }

  // ── CSS ──────────────────────────────────────────
  var style = document.createElement('style');
  style.textContent = [
    '.dualsubs-secondary {',
    '  position: fixed; top: 5%; left: 50%;',
    '  transform: translateX(-50%); z-index: 999998;',
    '  text-align: center; pointer-events: none;',
    '  max-width: 80%; padding: 4px 12px;',
    '  font-size: 1.6em; line-height: 1.4;',
    '  color: #ffeb3b; opacity: 0.9;',
    '  font-family: inherit; font-weight: 500;',
    '  text-shadow: 1px 1px 2px rgba(0,0,0,0.9),',
    '               -1px -1px 2px rgba(0,0,0,0.9),',
    '               2px 2px 4px rgba(0,0,0,0.7);',
    '  transition: opacity 0.15s;',
    '}',
    '.dualsubs-secondary.hidden { opacity: 0; }',
    '.dualsubs-secondary span {',
    '  background: rgba(0,0,0,0.5);',
    '  border-radius: 4px; padding: 2px 8px;',
    '  display: inline-block;',
    '}'
  ].join('\n');
  document.head.appendChild(style);

  // ── Create overlay ───────────────────────────────
  function createOverlay() {
    if (overlay) return;
    overlay = document.createElement('div');
    overlay.className = 'dualsubs-secondary hidden';
    document.body.appendChild(overlay);
  }

  function removeOverlay() {
    if (overlay) {
      overlay.remove();
      overlay = null;
    }
  }

  // ── VTT Parser ───────────────────────────────────
  function parseVTT(vttText) {
    var cues = [];
    if (!vttText) return cues;

    // Remove BOM
    vttText = vttText.replace(/^\uFEFF/, '');

    // Split into blocks
    var blocks = vttText.split(/\r?\n\r?\n/);
    var tcRe = /(\d{2}:\d{2}:\d{2}[.,]\d{3})\s*-->\s*(\d{2}:\d{2}:\d{2}[.,]\d{3})/;

    for (var i = 0; i < blocks.length; i++) {
      var block = blocks[i].trim();
      if (!block) continue;

      var lines = block.split(/\r?\n/);
      var tcIdx = -1;

      for (var j = 0; j < lines.length; j++) {
        if (tcRe.test(lines[j])) {
          tcIdx = j;
          break;
        }
      }

      if (tcIdx < 0) continue;

      var match = lines[tcIdx].match(tcRe);
      if (!match) continue;

      var startTime = parseTimestamp(match[1]);
      var endTime = parseTimestamp(match[2]);

      var text = [];
      for (var k = tcIdx + 1; k < lines.length; k++) {
        // Strip VTT tags like <b>, <i>, <c.color>, etc.
        var line = lines[k].replace(/<[^>]+>/g, '').trim();
        if (line) text.push(line);
      }

      if (text.length > 0) {
        cues.push({
          start: startTime,
          end: endTime,
          text: text.join('\n')
        });
      }
    }

    return cues;
  }

  function parseTimestamp(ts) {
    // 00:01:23.456 or 00:01:23,456
    ts = ts.replace(',', '.');
    var parts = ts.split(':');
    if (parts.length === 3) {
      return parseInt(parts[0]) * 3600 +
             parseInt(parts[1]) * 60 +
             parseFloat(parts[2]);
    }
    return 0;
  }

  // ── SRT Parser (converts to cues) ────────────────
  function parseSRT(srtText) {
    if (!srtText) return [];
    // Convert SRT to VTT-like format by replacing commas in timestamps
    var vtt = 'WEBVTT\n\n' + srtText.replace(/(\d{2}:\d{2}:\d{2}),(\d{3})/g, '$1.$2');
    return parseVTT(vtt);
  }

  // ── Find active cue ──────────────────────────────
  function findActiveCue(cues, currentTime) {
    for (var i = 0; i < cues.length; i++) {
      if (currentTime >= cues[i].start && currentTime <= cues[i].end) {
        return cues[i];
      }
    }
    return null;
  }

  // ── Sync secondary subtitles ─────────────────────
  function syncSubtitles() {
    if (!overlay || !videoEl || secondaryCues.length === 0) {
      if (overlay) overlay.classList.add('hidden');
      return;
    }

    var ct = videoEl.currentTime;
    var activeCue = findActiveCue(secondaryCues, ct);

    if (activeCue) {
      overlay.innerHTML = '<span>' + escapeHTML(activeCue.text).replace(/\n/g, '<br>') + '</span>';
      overlay.classList.remove('hidden');
    } else {
      overlay.classList.add('hidden');
    }
  }

  function escapeHTML(str) {
    var div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
  }

  // ── Start/stop sync ──────────────────────────────
  function startSync() {
    stopSync();
    syncTimer = setInterval(syncSubtitles, 200);
  }

  function stopSync() {
    if (syncTimer) {
      clearInterval(syncTimer);
      syncTimer = null;
    }
  }

  // ── Load secondary subtitles ─────────────────────
  function loadSecondarySubtitle(url) {
    secondaryCues = [];

    $.ajax({
      url: url,
      type: 'GET',
      dataType: 'text',
      timeout: 15000,
      success: function (data) {
        if (!data) return;

        // Detect format
        if (data.trim().indexOf('WEBVTT') === 0) {
          secondaryCues = parseVTT(data);
        } else {
          secondaryCues = parseSRT(data);
        }

        if (secondaryCues.length > 0) {
          console.log('DualSubs', 'Loaded', secondaryCues.length, 'secondary cues');
          createOverlay();
          startSync();
          Lampa.Noty.show('Вторые субтитры: ' + secondaryCues.length + ' фраз');
        }
      },
      error: function () {
        console.log('DualSubs', 'Failed to load secondary subtitles');
      }
    });
  }

  // ── Load from OpenSubtitles ──────────────────────
  function loadFromOpenSubs(imdbID, lang, season, episode) {
    var host = window.location.origin || '';
    try {
      var u = Lampa.Storage.get('lampac_unic_id', '');
      if (u) host = u;
    } catch (e) {}

    var url = host + '/api/opensubs/search?imdb_id=' + encodeURIComponent(imdbID) +
              '&lang=' + encodeURIComponent(lang);

    if (season !== undefined && season !== null) {
      url += '&season=' + season;
    }
    if (episode !== undefined && episode !== null) {
      url += '&episode=' + episode;
    }

    $.ajax({
      url: url,
      type: 'GET',
      dataType: 'json',
      timeout: 15000,
      success: function (response) {
        if (response && response.data && response.data.length > 0) {
          // Get first result's file ID
          var first = response.data[0];
          var attr = first.attributes || {};
          if (attr.files && attr.files.length > 0) {
            var fileID = attr.files[0].file_id;
            var vttURL = host + '/api/opensubs/download?file_id=' + fileID;
            loadSecondarySubtitle(vttURL);
          }
        }
      },
      error: function () {
        console.log('DualSubs', 'OpenSubs search failed for secondary lang');
      }
    });
  }

  // ── UI: Choose secondary subtitle ────────────────
  function showSubtitlePicker() {
    if (!isPlayerActive) return;

    var video = videoEl || (Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null);
    if (!video) return;

    var items = [];

    // Add available text tracks from video
    for (var i = 0; i < video.textTracks.length; i++) {
      var track = video.textTracks[i];
      items.push({
        title: (track.label || track.language || 'Track ' + i),
        trackIndex: i,
        type: 'track'
      });
    }

    // Add OpenSubtitles option
    items.push({
      title: '🔍 Поиск в OpenSubtitles...',
      type: 'opensubs'
    });

    // Add "Disable" option
    items.push({
      title: '❌ Отключить вторые субтитры',
      type: 'disable'
    });

    Lampa.Select.show({
      title: 'Вторые субтитры',
      items: items,
      onSelect: function (selected) {
        if (selected.type === 'disable') {
          secondaryCues = [];
          stopSync();
          if (overlay) overlay.classList.add('hidden');
          Lampa.Noty.show('Вторые субтитры отключены');
        } else if (selected.type === 'opensubs') {
          showOpenSubsLangPicker();
        } else if (selected.type === 'track') {
          // Load from existing text track
          loadFromTextTrack(selected.trackIndex);
        }
        Lampa.Controller.toggle('player');
      },
      onBack: function () {
        Lampa.Controller.toggle('player');
      }
    });
  }

  function showOpenSubsLangPicker() {
    var langs = [
      { title: 'English', code: 'en' },
      { title: 'Русский', code: 'ru' },
      { title: 'Українська', code: 'uk' },
      { title: 'Deutsch', code: 'de' },
      { title: 'Français', code: 'fr' },
      { title: 'Español', code: 'es' },
      { title: 'Italiano', code: 'it' },
      { title: 'Português', code: 'pt-br' },
      { title: '日本語', code: 'ja' },
      { title: '中文', code: 'zh-cn' },
      { title: 'العربية', code: 'ar' },
      { title: 'Polski', code: 'pl' }
    ];

    Lampa.Select.show({
      title: 'Язык вторых субтитров',
      items: langs.map(function (l) {
        return { title: l.title, code: l.code };
      }),
      onSelect: function (selected) {
        Lampa.Storage.set(SECONDARY_KEY, selected.code);

        resolveIMDB(function (imdbID) {
          if (!imdbID) {
            Lampa.Noty.show('IMDB ID не найден');
            Lampa.Controller.toggle('player');
            return;
          }

          var epInfo = null;
          try {
            var act = Lampa.Activity.active();
            if (act && act.season !== undefined) {
              epInfo = { season: act.season, episode: act.episode };
            }
          } catch (e) {}

          loadFromOpenSubs(
            imdbID,
            selected.code,
            epInfo ? epInfo.season : null,
            epInfo ? epInfo.episode : null
          );

          Lampa.Controller.toggle('player');
        });
      },
      onBack: function () {
        Lampa.Controller.toggle('player');
      }
    });
  }

  // ── Load from existing text track ────────────────
  function loadFromTextTrack(trackIndex) {
    var video = videoEl || (Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null);
    if (!video || trackIndex < 0 || trackIndex >= video.textTracks.length) return;

    var track = video.textTracks[trackIndex];

    // Read cues from the track
    secondaryCues = [];

    // Need to activate track temporarily to read cues
    var prevMode = track.mode;
    track.mode = 'hidden';

    var waitForCues = function () {
      if (track.cues && track.cues.length > 0) {
        for (var i = 0; i < track.cues.length; i++) {
          var cue = track.cues[i];
          secondaryCues.push({
            start: cue.startTime,
            end: cue.endTime,
            text: cue.text
          });
        }
        console.log('DualSubs', 'Loaded', secondaryCues.length, 'cues from track', trackIndex);
        createOverlay();
        startSync();
        Lampa.Noty.show('Вторые субтитры: ' + (track.label || track.language || 'Track ' + trackIndex));
      } else {
        // Cues might not be loaded yet
        setTimeout(waitForCues, 500);
      }
    };

    setTimeout(waitForCues, 300);
  }

  // ── Keyboard shortcut ────────────────────────────
  document.addEventListener('keydown', function (e) {
    // Key "D" (Dual) — open dual subtitle picker
    if (e.keyCode === 68 && !e.ctrlKey && !e.altKey && !e.metaKey && !e.shiftKey) {
      var tag = (e.target.tagName || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea') return;

      if (isPlayerActive) {
        e.preventDefault();
        e.stopPropagation();
        showSubtitlePicker();
      }
    }
  }, true);

  // ── Player hooks ─────────────────────────────────
  function onPlayerStart() {
    isPlayerActive = true;
    videoEl = null;
    secondaryCues = [];

    setTimeout(function () {
      videoEl = Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null;
    }, 500);
  }

  function onCanPlay() {
    videoEl = Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null;
  }

  function onPlayerDestroy() {
    isPlayerActive = false;
    secondaryCues = [];
    stopSync();
    removeOverlay();
    videoEl = null;
  }

  // ── Settings ─────────────────────────────────────
  function addSettings() {
    if (Lampa.SettingsApi) {
      Lampa.SettingsApi.addParam({
        component: 'player',
        param: {
          name: 'dualsubs_secondary_lang',
          type: 'select',
          values: {
            '': 'Отключено',
            en: 'English',
            ru: 'Русский',
            uk: 'Українська',
            de: 'Deutsch',
            fr: 'Français',
            es: 'Español'
          },
          default: ''
        },
        field: {
          name: 'Вторые субтитры — язык',
          description: 'Автоматически загружать вторые субтитры при воспроизведении'
        },
        onChange: function (val) {
          Lampa.Storage.set(SECONDARY_KEY, val);
        }
      });
    }
  }

  // ── Init ─────────────────────────────────────────
  function initPlugin() {
    Lampa.Player.listener.follow('start', onPlayerStart);
    Lampa.Player.listener.follow('destroy', onPlayerDestroy);
    Lampa.PlayerVideo.listener.follow('canplay', onCanPlay);

    addSettings();

    console.log('DualSubs', 'Dual subtitles plugin initialized');
  }

  if (window.appready) {
    initPlugin();
  } else {
    Lampa.Listener.follow('app', function (e) {
      if (e.type === 'ready') initPlugin();
    });
  }
})();
