(function () {
  'use strict';

  if (window.lampac_opensubs_plugin) return;
  window.lampac_opensubs_plugin = true;

  var LANGS_KEY = 'opensubs_langs';
  var defaultLangs = 'en,ru';

  // ── Helpers ──────────────────────────────────────
  function getHost() {
    var u = Lampa.Storage.get('lampac_unic_id', '');
    if (u) return u;
    return window.location.origin || '';
  }

  function getLangs() {
    return Lampa.Storage.get(LANGS_KEY, defaultLangs);
  }

  // ── IMDB ID resolution (multi-source) ───────────
  var imdbCache = {}; // tmdb_id -> imdb_id

  function getCard() {
    try {
      var activity = Lampa.Activity.active();
      if (activity && activity.card) return activity.card;
    } catch (e) {}
    return null;
  }

  // Get season/episode from player context
  function getEpisodeInfo() {
    try {
      var activity = Lampa.Activity.active();
      if (activity && activity.season !== undefined) {
        return {
          season: activity.season,
          episode: activity.episode
        };
      }
    } catch (e) {}
    return null;
  }

  /**
   * Resolve IMDB ID from card data with fallback chain:
   * 1. card.imdb_id (direct)
   * 2. card.external_ids.imdb_id
   * 3. /externalids endpoint (local mapping)
   * 4. TMDB /external_ids API via proxy
   * Caches result on the card object.
   */
  function resolveIMDB(callback) {
    var card = getCard();
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
    var host = getHost();
    var exQuery = [];
    if (tmdbID) exQuery.push('id=' + tmdbID);
    if (card.kinopoisk_id) exQuery.push('kinopoisk_id=' + card.kinopoisk_id);

    if (exQuery.length > 0) {
      $.ajax({
        url: host + '/externalids?' + exQuery.join('&'),
        type: 'GET',
        dataType: 'json',
        timeout: 5000,
        success: function (json) {
          if (json && json.imdb_id) {
            card.imdb_id = json.imdb_id;
            if (tmdbID) imdbCache[tmdbID] = json.imdb_id;
            return callback(card.imdb_id);
          }
          // 5. Fallback: TMDB API
          fetchIMDBFromTMDB(card, callback);
        },
        error: function () {
          fetchIMDBFromTMDB(card, callback);
        }
      });
    } else {
      fetchIMDBFromTMDB(card, callback);
    }
  }

  function fetchIMDBFromTMDB(card, callback) {
    if (!card.id) return callback('');

    var type = card.name ? 'tv' : 'movie'; // TV has 'name', movies have 'title'
    var url = '';
    try {
      url = Lampa.TMDB.api(type + '/' + card.id + '/external_ids');
    } catch (e) {
      return callback('');
    }

    if (!url) return callback('');

    $.ajax({
      url: url,
      type: 'GET',
      dataType: 'json',
      timeout: 5000,
      success: function (data) {
        if (data && data.imdb_id) {
          card.imdb_id = data.imdb_id;
          imdbCache[card.id] = data.imdb_id;
          callback(data.imdb_id);
        } else {
          callback('');
        }
      },
      error: function () {
        callback('');
      }
    });
  }

  // ── Subtitle Search ──────────────────────────────
  function searchSubtitles(imdbID, langs, season, episode, callback) {
    var host = getHost();
    var url = host + '/api/opensubs/search?imdb_id=' + encodeURIComponent(imdbID) +
              '&lang=' + encodeURIComponent(langs);

    if (season !== undefined && season !== null) {
      url += '&season=' + season;
    }
    if (episode !== undefined && episode !== null) {
      url += '&episode=' + episode;
    }

    Lampa.Loading.start(function () {
      // Cancelled
    });

    $.ajax({
      url: url,
      type: 'GET',
      dataType: 'json',
      timeout: 15000,
      success: function (response) {
        Lampa.Loading.stop();
        if (response && response.data && response.data.length > 0) {
          callback(null, response.data);
        } else {
          callback(null, []);
        }
      },
      error: function (xhr) {
        Lampa.Loading.stop();
        callback('Ошибка поиска субтитров');
      }
    });
  }

  // ── Download & Apply Subtitle ─────────────────────
  function downloadAndApply(fileID, label) {
    var host = getHost();
    var vttURL = host + '/api/opensubs/download?file_id=' + encodeURIComponent(fileID);

    // Create a text track on the current video element
    try {
      var video = Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null;
      if (!video) {
        Lampa.Noty.show('Видео элемент не найден');
        return;
      }

      // Remove previous OpenSubs tracks
      var existing = video.querySelectorAll ? null : null;
      for (var i = video.textTracks.length - 1; i >= 0; i--) {
        if (video.textTracks[i].label && video.textTracks[i].label.indexOf('[OS]') === 0) {
          // Cannot remove text tracks from DOM, just disable
          video.textTracks[i].mode = 'disabled';
        }
      }

      // Add new track
      var track = document.createElement('track');
      track.kind = 'subtitles';
      track.label = '[OS] ' + label;
      track.src = vttURL;
      track.default = true;
      video.appendChild(track);

      // Activate the track
      setTimeout(function () {
        for (var j = video.textTracks.length - 1; j >= 0; j--) {
          if (video.textTracks[j].label === '[OS] ' + label) {
            video.textTracks[j].mode = 'showing';
          } else {
            video.textTracks[j].mode = 'disabled';
          }
        }
      }, 500);

      Lampa.Noty.show('Субтитры загружены: ' + label);
      autoSync(video, fileID, label);
    } catch (e) {
      Lampa.Noty.show('Ошибка загрузки субтитров');
    }
  }

  // ── Auto-sync ─────────────────────────────────────
  // Скачанные субтитры почти всегда таймированы под ЧУЖОЙ рип: те же реплики, но
  // на секунду-другую в стороне. Сервер умеет измерить сдвиг по звуку самого потока
  // (/api/opensubs/sync), но измерение стоит одного прогона ffmpeg — до полуминуты.
  //
  // Поэтому сначала показываем как есть (мгновенно), а подгонку делаем фоном и
  // применяем ТОЛЬКО если сервер уверен в ответе. Ошибка синка не должна отнимать
  // у человека уже работающие субтитры.
  function streamSource(video) {
    // Нужен настоящий URL потока: при MSE/hls.js currentSrc — это blob:, из него
    // серверу нечего слушать, и синк просто не предлагается.
    var src = video && video.currentSrc ? String(video.currentSrc) : '';
    if (src.indexOf('http') !== 0) return '';
    return src;
  }

  function autoSync(video, fileID, label) {
    var src = streamSource(video);
    if (!src) return;
    var host = getHost();

    $.ajax({
      url: host + '/api/opensubs/sync?file_id=' + encodeURIComponent(fileID) + '&src=' + encodeURIComponent(src),
      dataType: 'json',
      timeout: 120000, // один ffmpeg-проход по 12 минутам звука
      success: function (res) {
        if (!res || !res.confident) return;
        var ms = res.offset_ms || 0;
        if (Math.abs(ms) < 300) return; // на глаз незаметно — не дёргаем трек

        // Результат уже в серверном кэше, так что apply=1 вернётся сразу.
        try {
          for (var i = video.textTracks.length - 1; i >= 0; i--) {
            if (video.textTracks[i].label === '[OS] ' + label) video.textTracks[i].mode = 'disabled';
          }
          var t = document.createElement('track');
          t.kind = 'subtitles';
          t.label = '[OS] ' + label;
          t.src = host + '/api/opensubs/sync?file_id=' + encodeURIComponent(fileID) +
            '&src=' + encodeURIComponent(src) + '&apply=1';
          t.default = true;
          video.appendChild(t);
          setTimeout(function () {
            for (var j = video.textTracks.length - 1; j >= 0; j--) {
              video.textTracks[j].mode = video.textTracks[j].label === '[OS] ' + label ? 'showing' : 'disabled';
            }
          }, 500);
          Lampa.Noty.show('Субтитры подогнаны по звуку: ' + (ms / 1000).toFixed(1) + ' с');
        } catch (e) {
          /* остаёмся на несинхронизированной дорожке */
        }
      },
      error: function () {
        /* синк — необязательное улучшение, молчим */
      }
    });
  }

  // ── UI: Show search results ───────────────────────
  function showResults(results) {
    if (!results || results.length === 0) {
      Lampa.Noty.show('Субтитры не найдены');
      return;
    }

    var items = results.slice(0, 30).map(function (sub) {
      var attr = sub.attributes || {};
      var lang = attr.language || '??';
      var release = attr.release || attr.feature_details && attr.feature_details.title || '';
      var downloads = attr.download_count || 0;
      var fps = attr.fps || '';
      var hearing = attr.hearing_impaired ? ' 👂' : '';
      var fileID = '';

      // Get file ID
      if (attr.files && attr.files.length > 0) {
        fileID = String(attr.files[0].file_id);
      }

      var title = lang.toUpperCase() + ' — ' + release + hearing;
      if (fps) title += ' (' + fps + 'fps)';
      title += ' [↓' + downloads + ']';

      return {
        title: title,
        file_id: fileID,
        lang: lang,
        release: release
      };
    });

    Lampa.Select.show({
      title: 'OpenSubtitles',
      items: items.map(function (item) {
        return {
          title: item.title,
          file_id: item.file_id,
          lang: item.lang,
          release: item.release
        };
      }),
      onSelect: function (selected) {
        if (selected.file_id) {
          downloadAndApply(selected.file_id, selected.lang + ' — ' + selected.release);
        }
      },
      onBack: function () {
        Lampa.Controller.toggle('player');
      }
    });
  }

  // ── Main search trigger ───────────────────────────
  function openSearch() {
    resolveIMDB(function (imdbID) {
      if (!imdbID) {
        Lampa.Noty.show('IMDB ID не найден');
        return;
      }

      var langs = getLangs();
      var epInfo = getEpisodeInfo();
      var season = epInfo ? epInfo.season : null;
      var episode = epInfo ? epInfo.episode : null;

      searchSubtitles(imdbID, langs, season, episode, function (err, results) {
        if (err) {
          Lampa.Noty.show(err);
          return;
        }
        showResults(results);
      });
    });
  }

  // ── Settings UI ───────────────────────────────────
  function openSettings() {
    var current = getLangs();

    Lampa.Input.edit({
      title: 'Языки субтитров (через запятую)',
      value: current,
      free: true
    }, function (newVal) {
      if (newVal && newVal.trim()) {
        Lampa.Storage.set(LANGS_KEY, newVal.trim());
        Lampa.Noty.show('Языки: ' + newVal.trim());
      }
    });
  }

  // ── Keyboard shortcut ─────────────────────────────
  document.addEventListener('keydown', function (e) {
    // Key "U" (sUbtitles) — open subtitle search
    if (e.keyCode === 85 && !e.ctrlKey && !e.altKey && !e.metaKey && !e.shiftKey) {
      var tag = (e.target.tagName || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea') return;

      // Only when player is active
      try {
        var video = Lampa.PlayerVideo ? Lampa.PlayerVideo.video() : null;
        if (video && !video.paused) {
          e.preventDefault();
          e.stopPropagation();
          openSearch();
        }
      } catch (ex) {}
    }
  }, true);

  // ── Player hook: Add subtitle button to panel ─────
  var isPlayerActive = false;

  function onPlayerStart() {
    isPlayerActive = true;
  }

  function onPlayerDestroy() {
    isPlayerActive = false;
  }

  // ── Settings page integration ─────────────────────
  function addSettings() {
    if (Lampa.SettingsApi) {
      Lampa.SettingsApi.addParam({
        component: 'player',
        param: {
          name: 'opensubs_langs',
          type: 'input',
          values: '',
          default: defaultLangs
        },
        field: {
          name: 'OpenSubtitles — языки',
          description: 'Языки для поиска субтитров (через запятую, напр.: en,ru,uk)'
        },
        onChange: function (val) {
          Lampa.Storage.set(LANGS_KEY, val);
        }
      });
    }
  }

  // ── Init ──────────────────────────────────────────
  function initPlugin() {
    Lampa.Player.listener.follow('start', onPlayerStart);
    Lampa.Player.listener.follow('destroy', onPlayerDestroy);

    addSettings();
  }

  if (window.appready) {
    initPlugin();
  } else {
    Lampa.Listener.follow('app', function (e) {
      if (e.type === 'ready') initPlugin();
    });
  }
})();
