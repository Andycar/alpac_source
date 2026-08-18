// UafilmME — Lampac JS module.
// Порт з LME.UafilmME (.NET): https://github.com/lampame/lampac-ukraine/tree/main/LME.UafilmME
// Підтримує пошук, список сезонів/серій, стрім через /proxy з заголовками.

var DEFAULTS = {
  host: 'https://uafilm.me',
  userAgent: 'EchoapiRuntime/1.1.0',
  timeout: 15,
  cacheSearchMinutes: 30,
  cacheTitleMinutes: 60,
  cacheWatchMinutes: 10,
  proxyStreams: true
};

function cfg(inv, key) {
  var v = inv.config && inv.config[key];
  if (v === undefined || v === null || v === '') return DEFAULTS[key];
  return v;
}

function apiGet(inv, path) {
  var host = String(cfg(inv, 'host')).replace(/\/+$/, '');
  var url = host + path;
  var cacheKey = 'GET:' + url;
  var cached = cache.get(cacheKey);
  if (cached !== undefined) return cached;

  var res = http.get(url, {
    headers: {
      'Accept': '*/*',
      'Referer': host + '/',
      'User-Agent': cfg(inv, 'userAgent')
    }
  });
  if (!res.ok) {
    console.warn('uafilmme: GET', url, 'status', res.status, res.error || '');
    return null;
  }
  var data = null;
  try { data = res.json(); } catch (e) {
    console.warn('uafilmme: json parse failed', e.message);
    return null;
  }
  // Short-lived cache — main list endpoints get refreshed faster than watch.
  var ttl = /\/watch\//.test(path) ? cfg(inv, 'cacheWatchMinutes') : cfg(inv, 'cacheSearchMinutes');
  cache.set(cacheKey, data, ttl * 60);
  return data;
}

function search(inv, title) {
  if (!title) return [];
  var data = apiGet(inv, '/api/v1/search/' + util.urlencode(title) + '?loader=searchPage');
  if (!data || !data.results) return [];
  return data.results;
}

// Try all available titles (title, original_title) until one returns results.
// uafilm.me indexes ukrainian/english names but rarely russian — so we must
// try both since Lampa often sends Russian `title` from TMDB.
function searchAllQueries(inv) {
  var tried = {};
  var queries = [
    (inv.query.original_title || '').trim(),
    (inv.query.title || '').trim(),
    (inv.query.serial || '').trim()
  ];
  for (var i = 0; i < queries.length; i++) {
    var q = queries[i];
    if (!q || tried[q.toLowerCase()]) continue;
    tried[q.toLowerCase()] = true;
    var r = search(inv, q);
    if (r && r.length) return r;
  }
  return [];
}

function pickBest(results, want) {
  if (!results || !results.length) return null;
  var year = want.year, imdb = want.imdb_id, kp = want.kinopoisk_id;
  // Exact imdb_id match wins.
  if (imdb) {
    for (var i = 0; i < results.length; i++) {
      if ((results[i].imdb_id || '') === imdb) return results[i];
    }
  }
  if (kp) {
    for (var j = 0; j < results.length; j++) {
      if (String(results[j].tmdb_id || '') === String(kp)) return results[j];
    }
  }
  if (year) {
    for (var k = 0; k < results.length; k++) {
      if (String(results[k].year || '') === String(year)) return results[k];
    }
  }
  return results[0];
}

function fetchTitle(inv, id) {
  var data = apiGet(inv, '/api/v1/titles/' + id + '?loader=titlePage');
  return data && data.title ? data.title : null;
}

function fetchSeasons(inv, id) {
  var out = [];
  var page = 1;
  for (var guard = 0; guard < 20; guard++) {
    var data = apiGet(inv, '/api/v1/titles/' + id + '/seasons?page=' + page);
    if (!data || !data.pagination) break;
    var rows = data.pagination.data || [];
    for (var i = 0; i < rows.length; i++) out.push(rows[i]);
    if (!data.pagination.next_page) break;
    page = data.pagination.next_page;
  }
  return out;
}

function fetchEpisodes(inv, id, s) {
  var out = [];
  var page = 1;
  for (var guard = 0; guard < 30; guard++) {
    var data = apiGet(inv, '/api/v1/titles/' + id + '/seasons/' + s + '/episodes?page=' + page);
    if (!data || !data.pagination) break;
    var rows = data.pagination.data || [];
    for (var i = 0; i < rows.length; i++) out.push(rows[i]);
    if (!data.pagination.next_page) break;
    page = data.pagination.next_page;
  }
  return out;
}

function fetchWatch(inv, videoId) {
  var data = apiGet(inv, '/api/v1/watch/' + videoId);
  return data || null;
}

function normalizeSrc(inv, src) {
  if (!src) return '';
  if (src.indexOf('//') === 0) return 'https:' + src;
  if (src.indexOf('/') === 0) return String(cfg(inv, 'host')).replace(/\/+$/, '') + src;
  return src;
}

function maybeProxy(inv, rawURL) {
  if (!cfg(inv, 'proxyStreams')) return rawURL;
  return proxy.url(rawURL, 'uafilmme');
}

// ---- Handlers for each flow ----

function handleChecksearch(inv) {
  var results = searchAllQueries(inv);
  if (!results || !results.length) return { rch: false };

  // uafilm.me search endpoint returns fallback content when nothing matches,
  // so the mere presence of results is not a guarantee. Require a real match:
  // imdb_id / tmdb_id / year must line up with the requested title.
  var want = {
    year: parseInt(inv.query.year || 0, 10),
    imdb_id: (inv.query.imdb_id || '').trim(),
    kinopoisk_id: (inv.query.kinopoisk_id || '').trim()
  };
  for (var i = 0; i < results.length; i++) {
    var r = results[i];
    if (want.imdb_id && r.imdb_id === want.imdb_id) {
      return { rch: true, type: 'movie', quality: manifest.quality || 'FHD' };
    }
    if (want.kinopoisk_id && String(r.tmdb_id || '') === String(want.kinopoisk_id)) {
      return { rch: true, type: 'movie', quality: manifest.quality || 'FHD' };
    }
    if (want.year && String(r.year || '') === String(want.year)) {
      // Year-only match — weaker signal, but acceptable when IDs absent.
      if (!want.imdb_id && !want.kinopoisk_id) {
        return { rch: true, type: 'movie', quality: manifest.quality || 'FHD' };
      }
    }
  }
  return { rch: false };
}

function handleSearchList(inv) {
  var results = searchAllQueries(inv);
  if (!results || !results.length) return { type: 'movie', data: [] };

  // If we have an exact match (by imdb_id, tmdb_id or year) — skip the
  // "similar" picker and drill straight into title details, so Lampa shows
  // the player without requiring manual confirmation.
  var best = pickBest(results, {
    year: parseInt(inv.query.year || 0, 10),
    imdb_id: (inv.query.imdb_id || '').trim(),
    kinopoisk_id: (inv.query.kinopoisk_id || '').trim()
  });
  var strictMatch =
    (inv.query.imdb_id && best && best.imdb_id === inv.query.imdb_id) ||
    (inv.query.kinopoisk_id && best && String(best.tmdb_id || '') === String(inv.query.kinopoisk_id)) ||
    (results.length === 1);
  if (best && strictMatch) {
    // Shallow-clone inv with title_id injected — Goja is ES5, no Object.assign.
    var q2 = {};
    for (var k in inv.query) if (Object.prototype.hasOwnProperty.call(inv.query, k)) q2[k] = inv.query[k];
    q2.title_id = String(best.id);
    var directInv = { query: q2, host: inv.host, config: inv.config, headers: inv.headers, checksearch: inv.checksearch, path: inv.path };
    return handleTitle(directInv);
  }

  var hostUrl = inv.host;
  var data = [];
  for (var i = 0; i < results.length; i++) {
    var r = results[i];
    var detailURL = hostUrl + '/lite/uafilmme?title_id=' + r.id;
    data.push({
      method: 'link',
      url: detailURL,
      name: r.name + (r.year ? ' (' + r.year + ')' : ''),
      title: r.name + (r.year ? ' (' + r.year + ')' : ''),
      details: r.is_series ? 'серіал' : 'фільм',
      poster: r.poster || ''
    });
  }
  return { type: 'similar', data: data };
}

function handleTitle(inv) {
  var id = inv.query.title_id;
  var detail = fetchTitle(inv, id);
  if (!detail) return { type: 'movie', data: [] };

  var hostUrl = inv.host;
  if (detail.is_series) {
    var seasons = fetchSeasons(inv, id);
    var data = [];
    for (var i = 0; i < seasons.length; i++) {
      var s = seasons[i];
      data.push({
        method: 'link',
        url: hostUrl + '/lite/uafilmme?title_id=' + id + '&s=' + s.number,
        name: s.number + ' сезон' + (s.episodes_count ? ' (' + s.episodes_count + ' ep.)' : ''),
        s: s.number
      });
    }
    return { type: 'season', data: data };
  }

  // Movie → single play link.
  if (!detail.primary_video || !detail.primary_video.id) {
    return { type: 'movie', data: [] };
  }
  return {
    type: 'movie',
    data: [{
      method: 'call',
      url: hostUrl + '/lite/uafilmme?video_id=' + detail.primary_video.id,
      name: detail.name,
      title: detail.name + (detail.year ? ' (' + detail.year + ')' : '')
    }]
  };
}

function handleSeasonEpisodes(inv) {
  var id = inv.query.title_id;
  var s = parseInt(inv.query.s, 10) || 1;
  var episodes = fetchEpisodes(inv, id, s);
  if (!episodes.length) return { type: 'episode', data: [] };

  var hostUrl = inv.host;
  var data = [];
  for (var i = 0; i < episodes.length; i++) {
    var ep = episodes[i];
    if (!ep.primary_video || !ep.primary_video.id) continue;
    data.push({
      method: 'call',
      url: hostUrl + '/lite/uafilmme?video_id=' + ep.primary_video.id + '&s=' + s + '&e=' + ep.episode_number,
      name: ep.name || ('Серія ' + ep.episode_number),
      title: ep.name || ('Серія ' + ep.episode_number),
      s: s,
      e: ep.episode_number
    });
  }
  return { type: 'episode', data: data };
}

function handlePlay(inv) {
  var videoId = inv.query.video_id;
  if (!videoId) return { rch: false, error: 'video_id is required' };
  var watch = fetchWatch(inv, videoId);
  if (!watch || !watch.video) return { rch: false, error: 'video not found' };

  var primary = watch.video;
  var alts = watch.alternative_videos || [];

  function buildQualityMap(video) {
    var src = normalizeSrc(inv, video.src || '');
    if (!src) return null;
    return { quality: video.quality || 'auto', url: maybeProxy(inv, src), name: video.name || '' };
  }

  var all = [];
  var first = buildQualityMap(primary);
  if (first) all.push(first);
  for (var i = 0; i < alts.length; i++) {
    var q = buildQualityMap(alts[i]);
    if (q) all.push(q);
  }
  if (!all.length) return { rch: false };

  var quality = {};
  for (var j = 0; j < all.length; j++) quality[all[j].quality] = all[j].url;

  return {
    method: 'play',
    url: all[0].url,
    title: primary.name || 'UafilmME',
    quality: quality,
    qualitys: quality
  };
}

// Main dispatch.
function handle(inv) {
  if (inv.checksearch) return handleChecksearch(inv);
  if (inv.query.video_id) return handlePlay(inv);
  if (inv.query.title_id && inv.query.s !== undefined && inv.query.s !== '') return handleSeasonEpisodes(inv);
  if (inv.query.title_id) return handleTitle(inv);
  return handleSearchList(inv);
}
