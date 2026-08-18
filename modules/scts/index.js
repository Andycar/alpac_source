// SCTS — community-каталог Sakhalin Cable TV (online.scts.tv).
//
// API сайта закрыт по IP-allowlist (только подсети SCTS, AS51004), но
// CDN dl.yama.scts.tv открыт миру и отдаёт MP4 с поддержкой Range.
// Этот балансер тянет thin-JSON по URL (~2 МБ gzip), держит индексы в
// module-cache и отдаёт клиенту прямые URL на CDN — без проксей, без
// транзита через lampac-сервер.
//
// Slim-формат каталога:
//   {
//     "v": 1, "n": 22563,
//     "items": [
//       {"i":4,"n":"Таксист","y":1976,"t":103,"m":"tt0075314",
//        "f":[["filename.mkv","http://dl.yama.scts.tv/..."]]},
//       {"i":275,"n":"Тайны Смолвиля (6 сезон)","y":2006,"t":4604,
//        "m":"tt0279600","k":"tv","s":6,"f":[...]}
//     ]
//   }
// k="tv" / s=N выставлены только для записей-сезонов сериалов.

var DEFAULTS = {
  catalog_url:   'https://hub.alcopa.cc/scts/scts-slim.json.gz',
  refresh_hours: 24,
  use_proxy:     false
};

function cfg(inv, k) {
  var v = inv.config && inv.config[k];
  if (v === undefined || v === null || v === '') return DEFAULTS[k];
  return v;
}

// ─── Загрузка каталога и построение индексов ────────────────────────────────
//
// Runtime пересоздаётся на каждый запрос, но cache переживает Runtime —
// привязан к Module. Поэтому каталог тянем один раз, индексы сохраняем
// в cache на refresh_hours.

function loadCatalog(inv) {
  var idx = cache.get('idx');
  if (idx && idx.items && idx.items.length > 0) return idx;

  var url = String(cfg(inv, 'catalog_url') || '').trim();
  if (!url) {
    console.error('scts: catalog_url не задан в настройках модуля');
    return null;
  }

  console.log('scts: fetching catalog ' + url);
  // maxBytes: 32 МБ — slim-каталог распакованным ~18 МБ. Дефолтный лимит
  // 8 МБ в jsmodules слишком мал когда hub отдаёт Content-Encoding: gzip
  // (Go HTTP-клиент распаковывает на лету, наш magic-detect не срабатывает).
  var resp = http.get(url, { timeout: 60, maxBytes: 32 * 1024 * 1024 });
  if (!resp || !resp.ok) {
    console.error('scts: catalog fetch failed status=' + (resp && resp.status) + ' err=' + (resp && resp.error));
    return null;
  }
  var data;
  try { data = resp.json(); } catch (e) {
    console.error('scts: catalog parse failed: ' + e);
    return null;
  }
  if (!data || !data.items || !data.items.length) {
    console.error('scts: catalog empty or malformed');
    return null;
  }

  var tmdbMovie = {};
  var tmdbSeries = {};
  var imdbMovie = {};
  var imdbSeries = {};
  var titleYear = {};

  for (var i = 0; i < data.items.length; i++) {
    var m = data.items[i];
    var isTV = m.k === 'tv';

    if (m.t) {
      if (isTV) {
        (tmdbSeries[m.t] = tmdbSeries[m.t] || []).push(m);
      } else if (!tmdbMovie[m.t]) {
        tmdbMovie[m.t] = m;
      }
    }
    if (m.m) {
      var imdbKey = String(m.m).toLowerCase();
      if (isTV) {
        (imdbSeries[imdbKey] = imdbSeries[imdbKey] || []).push(m);
      } else if (!imdbMovie[imdbKey]) {
        imdbMovie[imdbKey] = m;
      }
    }
    if (m.n && m.y) {
      var keys = titleYearKeys(m);
      for (var k = 0; k < keys.length; k++) {
        (titleYear[keys[k]] = titleYear[keys[k]] || []).push(m);
      }
    }
  }

  idx = {
    items:      data.items,
    n:          data.n || data.items.length,
    tmdbMovie:  tmdbMovie,
    tmdbSeries: tmdbSeries,
    imdbMovie:  imdbMovie,
    imdbSeries: imdbSeries,
    titleYear:  titleYear,
    builtAt:    Date.now()
  };

  console.log('scts: indexed n=' + idx.n + ' tmdb_movie=' + Object.keys(tmdbMovie).length +
              ' tmdb_series=' + Object.keys(tmdbSeries).length);

  var ttl = parseInt(cfg(inv, 'refresh_hours'), 10);
  if (isNaN(ttl) || ttl <= 0) ttl = 24;
  cache.set('idx', idx, ttl * 3600);
  return idx;
}

function titleYearKeys(m) {
  var out = [];
  if (!m.y) return out;
  var year = '' + m.y;
  var clean = String(m.n).replace(/\s*\(\s*\d+\s*сезон\s*\)\s*/gi, ' ');
  var seen = {};
  function add(s) {
    if (!s) return;
    var nrm = normalizeTitle(s);
    if (!nrm || seen[nrm]) return;
    seen[nrm] = true;
    out.push(nrm + '|' + year);
  }
  add(m.n);
  add(clean);
  return out;
}

function normalizeTitle(s) {
  if (!s) return '';
  return String(s).toLowerCase()
    .replace(/ё/g, 'е')
    .replace(/[^a-zа-я0-9 ]+/gi, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

// ─── Поиск ──────────────────────────────────────────────────────────────────

function parseSeasonParam(s) {
  var n = parseInt(s, 10);
  return isNaN(n) || n <= 0 ? 0 : n;
}

function pickSeason(list, season) {
  if (!list || !list.length) return null;
  if (season > 0) {
    for (var i = 0; i < list.length; i++) {
      if (list[i].s === season) return list[i];
    }
  }
  var best = list[0];
  for (var j = 0; j < list.length; j++) {
    var sj = list[j].s || 999;
    var sb = best.s || 999;
    if (sj < sb) best = list[j];
  }
  return best;
}

function findMovie(inv, idx) {
  var q = inv.query || {};
  var tmdb = String(q.tmdb_id || q.id || '').trim();
  var imdb = String(q.imdb_id || '').toLowerCase().trim();
  var title = String(q.title || '').trim();
  var original = String(q.original_title || '').trim();
  var year = String(q.year || '').trim();
  var serial = q.serial === '1' || q.serial === 'true';
  var seasonParam = parseSeasonParam(q.s);

  if (tmdb && /^\d+$/.test(tmdb)) {
    var id = parseInt(tmdb, 10);
    if (!serial && idx.tmdbMovie[id]) {
      return { movie: idx.tmdbMovie[id], seasons: null };
    }
    if (idx.tmdbSeries[id]) {
      var list = idx.tmdbSeries[id];
      return { movie: pickSeason(list, seasonParam), seasons: list };
    }
  }
  if (imdb) {
    if (!serial && idx.imdbMovie[imdb]) {
      return { movie: idx.imdbMovie[imdb], seasons: null };
    }
    if (idx.imdbSeries[imdb]) {
      var list2 = idx.imdbSeries[imdb];
      return { movie: pickSeason(list2, seasonParam), seasons: list2 };
    }
  }
  if (year) {
    var titles = [original, title];
    for (var t = 0; t < titles.length; t++) {
      if (!titles[t]) continue;
      var key = normalizeTitle(titles[t]) + '|' + year;
      var cands = idx.titleYear[key];
      if (!cands || !cands.length) continue;
      var tvs = [];
      for (var c = 0; c < cands.length; c++) {
        if (cands[c].k === 'tv') tvs.push(cands[c]);
      }
      if (tvs.length) return { movie: pickSeason(tvs, seasonParam), seasons: tvs };
      return { movie: cands[0], seasons: null };
    }
  }
  return null;
}

function hasPlayable(m) {
  return m && m.f && m.f.length > 0;
}

// ─── Парсинг номера серии из имени файла ────────────────────────────────────

var EP_PATTERNS = [
  /s\d+\.?e(\d{1,3})/i,                                          // S01.E06 / S01E06
  /\(?\s*(\d{1,3})\s*(?:[._\- ]+)?\s*(?:ser|серия|сер|seriya|s)\b/i, // (1.ser.iz.6)
  /(\d{1,3})\s+серия/,                                           // 12 серия
  /^(\d{1,3})[._\- ]/,                                           // 01.Title.mkv
  /e(\d{1,3})\b/i                                                // .E03.
];

function parseEpisodeNum(name) {
  if (!name) return 0;
  for (var i = 0; i < EP_PATTERNS.length; i++) {
    var m = EP_PATTERNS[i].exec(name);
    if (m) {
      var n = parseInt(m[1], 10);
      if (!isNaN(n) && n > 0 && n < 500) return n;
    }
  }
  return 0;
}

// ─── Сборка ответа ──────────────────────────────────────────────────────────

function joinTitle(ru, en) {
  if (ru && en && ru !== en) return ru + ' / ' + en;
  return ru || en || '';
}

// wrapStream — заворачивает CDN-ссылку через /proxy/ lampac-сервера, если
// в настройках включён use_proxy. Иначе отдаёт URL как есть.
// proxy.url(uri, plugin) даёт "<lampac-host>/proxy/<encrypted>"; если proxyBuild
// в runtime недоступен — он молча вернёт исходный uri (см. runtime.go).
function wrapStream(inv, url) {
  if (!url) return '';
  if (cfg(inv, 'use_proxy') === false) return url;
  try { return proxy.url(url, 'scts') || url; } catch (e) { return url; }
}

function buildPlay(inv, m, baseTitle) {
  var rows = [];
  for (var i = 0; i < m.f.length; i++) {
    var name = m.f[i][0];
    var url  = wrapStream(inv, m.f[i][1]);
    if (!url) continue;
    rows.push({
      method: 'play',
      url:    url,
      stream: url,
      name:   '1080p',
      title:  baseTitle || name
    });
  }
  return rows;
}

function buildEpisodes(inv, m, baseTitle) {
  var rows = [];
  for (var i = 0; i < m.f.length; i++) {
    var name = m.f[i][0];
    var url  = wrapStream(inv, m.f[i][1]);
    if (!url) continue;
    var num = parseEpisodeNum(name);
    var label = num > 0 ? (num + ' серия') : name;
    rows.push({
      method: 'play',
      url:    url,
      stream: url,
      s:      m.s || 1,
      e:      num,
      name:   label,
      title:  baseTitle + ' / ' + (m.s || 1) + ' сезон ' + num + ' серия'
    });
  }
  rows.sort(function(a, b) {
    if (!a.e) return 1;
    if (!b.e) return -1;
    return a.e - b.e;
  });
  return rows;
}

function buildSeasonList(host, query, seasons) {
  var out = [];
  // НЕ хардкодить rjson=true: online.js парсит сезоны/серии только как HTML
  // (parseJsonDate ищет .videos__item). При rjson=true сезон отдаёт JSON →
  // клиент не разбирает → «нет результатов». Проксируем входящий rjson.
  var rjson = (query.rjson === 'true' || query.rjson === '1' || query.rjson === true);
  var arr = seasons.slice().sort(function(a, b) { return (a.s || 0) - (b.s || 0); });
  for (var i = 0; i < arr.length; i++) {
    var s = arr[i].s;
    if (!s) continue;
    var params = ['serial=1', 's=' + s];
    if (rjson) params.unshift('rjson=true');
    if (query.tmdb_id) params.push('tmdb_id=' + encodeURIComponent(query.tmdb_id));
    if (query.imdb_id) params.push('imdb_id=' + encodeURIComponent(query.imdb_id));
    if (query.title) params.push('title=' + encodeURIComponent(query.title));
    if (query.original_title) params.push('original_title=' + encodeURIComponent(query.original_title));
    out.push({
      method: 'link',
      id:     s,
      url:    host + '/lite/scts?' + params.join('&'),
      name:   s + ' сезон'
    });
  }
  return out;
}

// ─── Точка входа ────────────────────────────────────────────────────────────

function handle(inv) {
  var idx = loadCatalog(inv);
  if (!idx) {
    return inv.checksearch ? { rch: false, error: 'catalog unavailable' } : { error: 'catalog unavailable' };
  }

  var found = findMovie(inv, idx);
  if (found && (!found.movie || !hasPlayable(found.movie)) && found.seasons) {
    // pickSeason мог вернуть пустую запись — берём первую играбельную
    for (var i = 0; i < found.seasons.length; i++) {
      if (hasPlayable(found.seasons[i])) {
        found.movie = found.seasons[i];
        break;
      }
    }
  }
  if (!found || !found.movie || !hasPlayable(found.movie)) {
    if (inv.checksearch) return { rch: false };
    return {};
  }

  if (inv.checksearch) {
    return {
      type:    found.seasons ? 'serial' : 'movie',
      rch:     true,
      quality: 'FHD'
    };
  }

  var q = inv.query || {};
  var baseTitle = joinTitle(q.title || '', q.original_title || '') || found.movie.n;
  var season = parseSeasonParam(q.s);

  // Сериал, сезон не выбран → список сезонов
  if (found.seasons && found.seasons.length > 1 && season === 0) {
    return { type: 'season', data: buildSeasonList(inv.host, q, found.seasons) };
  }
  // Сериал (один сезон или конкретный сезон) → эпизоды
  if (found.seasons) {
    return { type: 'episode', data: buildEpisodes(inv, found.movie, baseTitle) };
  }
  // Фильм
  return { type: 'movie', data: buildPlay(inv, found.movie, baseTitle) };
}

module.exports = { handle: handle };
