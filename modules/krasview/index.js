// krasview — сеть зеркал Krasview/hlamer/smartkino/sersoap/zseek.
//
// Сервер отдаёт cp1251 в Content-Type, lampac-go-runtime читает body как UTF-8 →
// все кириллические байты ломаются в "?". Поэтому модуль НЕ ИСПОЛЬЗУЕТ
// кириллицу из HTML вообще:
//   - matching: только original_title (английский) и год (ASCII-цифры).
//   - season list: только category_id (числовое) из <li id='c-{ID}'>; имя
//     "Сезон N" формируем сами по порядку появления (учитывая что category=0
//     — это "Без категории", т.е. трейлеры/прочее, его игнорируем).
//   - episode S/E: парсим из slug `2.sezon.8.seriya` (ASCII-транслит).
//   - name/title в выводе: берём inv.query.title/original_title (Lampa
//     отдаёт чистым UTF-8) — никогда не из HTML.
//
// Для стрима используем proxy.urlWithHeaders с Referer=smartkino.ru — иначе
// CDN media*.krasview.ru режет hot-link 403.

var DEFAULTS = {
  searchHost: 'https://hlamer.ru',
  movieHost:  'https://smartkino.ru',
  serialHost: 'https://sersoap.ru',
  streamReferer: 'https://smartkino.ru/',
  preferHLS: true,
  proxyStreams: true,
  matchYearTolerance: 1,
  cacheTTL: 1800
};
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
var UA='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';

function commonHeaders(refer){
  return {
    'User-Agent': UA,
    'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
    'Accept-Language': 'ru,en;q=0.9',
    'Referer': refer || ''
  };
}

function getCached(inv, url, ref){
  var ck = 'krasview:'+url;
  var hit = cache.get(ck);
  if (hit) return hit;
  var res = http.get(url, { headers: commonHeaders(ref) });
  if (!res.ok) return '';
  var text = res.text || '';
  if (text) cache.set(ck, text, cfg(inv,'cacheTTL'));
  return text;
}

function maybeProxy(inv, u){
  if (!u) return '';
  if (!cfg(inv,'proxyStreams')) return u;
  var ref = cfg(inv,'streamReferer') || (cfg(inv,'movieHost') + '/');
  var origin = ref.replace(/\/$/,'');
  return proxy.urlWithHeaders(u, 'krasview', {
    'Referer': ref,
    'Origin': origin,
    'User-Agent': UA
  });
}

function joinName(t,o){ return [t,o].filter(function(x){return x;}).join(' / '); }

// --- SEARCH (matching через ASCII-only поля) -----------------------------

// Возвращает массив {url, host, kind, slug, en, year}. ru НЕ парсим — он
// в cp1251 поломан до символов "?" и бесполезен для matching.
function searchOnce(inv, kind, query){
  var url = cfg(inv, 'searchHost') + '/' + kind + '?mode=search&ajax&query=' + util.urlencode(query);
  var html = getCached(inv, url, cfg(inv,'searchHost')+'/');
  if (!html) return [];
  var out = [];
  var re = /<a[^>]+href='(https?:\/\/[^']+\/(?:movie|series)\/[^']+)'\s+title='([^']*)'/g;
  var m;
  while ((m = re.exec(html)) !== null){
    var href = m[1];
    var title = m[2]; // raw, может содержать ?-сурогаты для русской части
    var hostMatch = /https?:\/\/([^\/]+)/.exec(href);
    var pathMatch = /\/(movie|series)\/([^\/?#]+)/.exec(href);
    // Год — последние 4 цифры в title
    var ymatch = /(?:^|\s|\()((?:18|19|20|21)\d{2})(?:\)|\s|$)/.exec(title);
    var year = ymatch ? parseInt(ymatch[1],10) : 0;
    // Английская часть — после "/", до года. Для matching достаточно ASCII.
    var en = '';
    var afterSlash = title.split('/').slice(1).join('/').replace(/\s+\d{4}\s*$/,'').trim();
    if (afterSlash) en = afterSlash;
    out.push({
      url: href,
      host: hostMatch ? hostMatch[1] : '',
      kind: pathMatch ? pathMatch[1] : kind,
      slug: pathMatch ? pathMatch[2] : '',
      en: en, year: year
    });
  }
  return out;
}

function asciiNorm(s){
  return String(s||'').toLowerCase().replace(/[^a-z0-9]+/g,' ').replace(/\s+/g,' ').trim();
}

function pickBest(results, wantOrig, wantYear, tol){
  if (!results.length) return null;
  var nWantOrig = asciiNorm(wantOrig);
  var best = null, bestScore = -1;
  for (var i=0;i<results.length;i++){
    var r = results[i];
    var score = 0;
    var nEn = asciiNorm(r.en);
    if (nWantOrig && nEn === nWantOrig) score += 100;
    else if (nWantOrig && nEn.indexOf(nWantOrig) >= 0) score += 50;
    else if (nWantOrig && nWantOrig.indexOf(nEn) >= 0 && nEn.length>3) score += 30;
    if (wantYear && r.year){
      var diff = Math.abs(r.year - wantYear);
      if (diff === 0) score += 30;
      else if (diff <= tol) score += 10;
      else score -= 20;
    }
    if (score > bestScore){ bestScore = score; best = r; }
  }
  return bestScore >= 30 ? best : null;
}

function findMatch(inv, orig, year, kind){
  if (!orig) return null;
  var tol = parseInt(cfg(inv,'matchYearTolerance'),10) || 1;
  var rs = searchOnce(inv, kind, orig);
  return pickBest(rs, orig, year, tol);
}

// --- VIDEO PAGE → stream config (всё ASCII в base64-JSON) ----------------

function fetchVideoConfig(inv, host, href){
  var url = host + href;
  var html = getCached(inv, url, host + '/');
  if (!html) return null;
  var m = /video_Init\('([A-Za-z0-9+\/=]+)'/.exec(html);
  if (!m) return null;
  var json = '';
  try { json = atob(m[1]); } catch(e){ return null; }
  var data = null;
  try { data = JSON.parse(json); } catch(e){ return null; }
  return data;
}

function streamURL(inv, data){
  if (!data || !data.url) return '';
  var u = String(data.url);
  if (cfg(inv,'preferHLS') && /\.mpd($|\?)/i.test(u)) {
    u = u.replace(/\.mpd(\?|$)/i, '.m3u8$1');
  }
  return u;
}

function audioCount(data){
  if (!data || !data.audio_info) return 0;
  return Object.keys(data.audio_info).length;
}

// --- MOVIE PAGE → video items --------------------------------------------

// Парсит ссылки `/video/{id}-{slug}` и фильтрует через ASCII-slug. У
// "Фильма" slug кончается на ".Film" / "-Film" / "_Film". Трейлеры и обзоры
// определяем по slug-маркерам (treiyler, tizer, obzor, ...) — без HTML-label.
var SLUG_BLACKLIST = /(treiyler|tizer|obzor|review|reklam|trailer|teaser|preview|fragment|sopustvuyusch|making|behind)/i;
function parseMovieVideos(html){
  var primary = [], fallback = [];
  var re = /href='(\/video\/(\d+)-([^']+))'/g;
  var seen = {};
  var m;
  while ((m = re.exec(html)) !== null){
    var href = m[1], id = m[2], slug = m[3];
    if (seen[id]) continue;
    seen[id] = true;
    if (SLUG_BLACKLIST.test(slug)) continue;
    var item = { id: id, slug: slug, href: href };
    if (/(^|[._-])film\b|[._-]Film($|[._-])/i.test(slug)) primary.push(item);
    else fallback.push(item);
  }
  return primary.length ? primary : fallback;
}

// --- SERIAL: категории сезонов + эпизоды ---------------------------------

// На странице сериала: <li id='c-32524'><a href='/series/{slug}/?category=32524'>Сезон N</a></li>
// У category=0 — "Без категории" (трейлеры/превью), пропускаем.
function parseSeasonCategories(html){
  var out = [];
  var re = /<li[^>]+id='c-(\d+)'[^>]*>\s*<a[^>]+href='\/(?:series|movie)\/[^']+\?category=\d+/g;
  var m, seen = {};
  while ((m = re.exec(html)) !== null){
    var id = parseInt(m[1],10);
    if (id === 0) continue;
    if (seen[id]) continue;
    seen[id] = true;
    out.push({ id: id });
  }
  return out;
}

// Парсит ссылки `/video/{id}-{slug}` где slug содержит `N.sezon.M.seriya`.
function parseSeriesEpisodes(html){
  var out = [];
  var re = /href='(\/video\/(\d+)-([^']+))'/g;
  var seen = {};
  var m;
  while ((m = re.exec(html)) !== null){
    var href = m[1], id = m[2], slug = m[3];
    if (seen[id]) continue;
    seen[id] = true;
    if (SLUG_BLACKLIST.test(slug)) continue;
    var sm = /(\d+)\.sezon/i.exec(slug);
    var em = /(\d+)\.seriya/i.exec(slug);
    if (!sm || !em) continue;
    out.push({
      id: id, href: href, slug: slug,
      s: parseInt(sm[1],10), e: parseInt(em[1],10)
    });
  }
  out.sort(function(a,b){ return (a.s-b.s) || (a.e-b.e); });
  return out;
}

// Грузит весь сезон через ?category=ID, проходит пагинацию ?page=N.
function fetchSeasonEpisodes(inv, host, slug, catID){
  var all = [];
  var page = 1, maxPage = 10;
  while (page <= maxPage){
    var url = host + '/series/' + slug + '/?category=' + catID + (page>1 ? ('&page='+page) : '');
    var html = getCached(inv, url, host + '/series/' + slug);
    if (!html) break;
    var part = parseSeriesEpisodes(html);
    if (!part.length) break;
    var added = 0;
    var seenIDs = {};
    for (var i=0;i<all.length;i++) seenIDs[all[i].id] = true;
    for (var j=0;j<part.length;j++){
      if (!seenIDs[part[j].id]) { all.push(part[j]); added++; }
    }
    if (added === 0) break; // page=N+1 повторился — конец
    // Ищем следующую страницу
    var hasNext = new RegExp("href='[^']*\\?(?:category=\\d+&)?page="+(page+1)+"'").test(html);
    if (!hasNext) break;
    page++;
  }
  all.sort(function(a,b){ return (a.s-b.s) || (a.e-b.e); });
  return all;
}

// --- HANDLERS ------------------------------------------------------------

function handleChecksearch(inv){
  var orig  = (inv.query.original_title||'').trim();
  var year  = parseInt(inv.query.year||'0', 10) || 0;
  var serial = parseInt(inv.query.serial||0,10);
  if (!orig) return { rch:false };
  var kind = serial===1 ? 'series' : 'movie';
  var match = findMatch(inv, orig, year, kind);
  if (!match){
    match = findMatch(inv, orig, year, kind==='movie'?'series':'movie');
    if (match) kind = match.kind;
  }
  if (!match) return { rch:false };
  return { rch:true, type: (kind==='series'?'serial':'movie'), quality: 'FHD' };
}

function renderMovie(inv, host, title, orig, videos){
  var base = joinName(title,orig);
  if (!videos.length) return { type:'movie', data:[] };
  for (var i=0;i<videos.length;i++){
    var v = videos[i];
    var data_i = fetchVideoConfig(inv, host, v.href);
    if (!data_i) continue;
    var su = streamURL(inv, data_i);
    if (!su) continue;
    var sup = maybeProxy(inv, su);
    var ac = audioCount(data_i);
    var voiceLabel = ac>1 ? (' [озвучек: '+ac+']') : '';
    return {
      type:'movie',
      data: [{
        method:'play', url: sup, stream: sup,
        name: base, // из inv.query — UTF-8 чистый
        title: base + voiceLabel,
        duration: data_i.duration||undefined
      }]
    };
  }
  return { type:'movie', data:[] };
}

function renderSeasons(inv, host, title, orig, slug, seasons){
  var data = seasons.map(function(s, idx){
    return {
      method:'link', id: idx+1,
      name: 'Сезон '+(idx+1),
      url: inv.host+'/lite/krasview?title='+util.urlencode(title)+
           '&original_title='+util.urlencode(orig)+
           '&year='+(inv.query.year||'')+
           '&serial=1&s='+(idx+1)
    };
  });
  return { type:'season', data: data };
}

function renderEpisodes(inv, host, title, orig, eps, sNum){
  var base = joinName(title,orig);
  var filt = eps.filter(function(x){ return x.s === sNum; });
  var data = filt.map(function(ep){
    var conf = fetchVideoConfig(inv, host, ep.href);
    if (!conf) return null;
    var su = streamURL(inv, conf);
    if (!su) return null;
    var sup = maybeProxy(inv, su);
    var ac = audioCount(conf);
    var voiceLabel = ac>1 ? (' ['+ac+' озвучек]') : '';
    return {
      method:'play', url: sup, stream: sup,
      s: ep.s, e: ep.e,
      name: 'Серия '+ep.e,
      title: base+' (S'+ep.s+'E'+ep.e+')'+voiceLabel,
      duration: conf.duration||undefined
    };
  }).filter(function(x){return x;});
  return { type:'episode', data: data };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);

  var title = (inv.query.title||'').trim();
  var orig  = (inv.query.original_title||'').trim();
  var year  = parseInt(inv.query.year||'0', 10) || 0;
  var serial= parseInt(inv.query.serial||0, 10);
  var s     = (inv.query.s===''||inv.query.s===undefined) ? -1 : parseInt(inv.query.s,10);
  if (!orig && !title) return { type:'movie', data:[] };

  var kind = serial===1 ? 'series' : 'movie';
  var match = findMatch(inv, orig||title, year, kind);
  if (!match){
    match = findMatch(inv, orig||title, year, kind==='movie'?'series':'movie');
    if (match) kind = match.kind;
  }
  if (!match) return { type:'movie', data:[] };

  var host = (kind === 'series') ? cfg(inv,'serialHost') : cfg(inv,'movieHost');
  if (match.host && /(smartkino|sersoap|zseek|krasview|hlamer)\.ru$/.test(match.host)){
    host = 'https://' + match.host;
  }

  var pageURL = '/' + match.kind + '/' + match.slug;
  var pageHTML = getCached(inv, host + pageURL, host + '/');
  if (!pageHTML) return { type:'movie', data:[] };

  if (kind === 'movie'){
    var vids = parseMovieVideos(pageHTML);
    return renderMovie(inv, host, title, orig, vids);
  }

  // serial — подтянуть категории сезонов
  var seasons = parseSeasonCategories(pageHTML);
  if (!seasons.length){
    // одно-сезонный без категорий — парсим сразу эпизоды со страницы
    var eps0 = parseSeriesEpisodes(pageHTML);
    if (!eps0.length) return renderMovie(inv, host, title, orig, parseMovieVideos(pageHTML));
    return renderEpisodes(inv, host, title, orig, eps0, eps0[0].s);
  }

  if (s === -1){
    if (seasons.length === 1){
      // один сезон — сразу эпизоды
      var only = fetchSeasonEpisodes(inv, host, match.slug, seasons[0].id);
      if (only.length) return renderEpisodes(inv, host, title, orig, only, only[0].s);
      return { type:'movie', data:[] };
    }
    return renderSeasons(inv, host, title, orig, match.slug, seasons);
  }

  var idx = Math.max(0, Math.min(s-1, seasons.length-1));
  var eps = fetchSeasonEpisodes(inv, host, match.slug, seasons[idx].id);
  if (!eps.length) return { type:'movie', data:[] };
  // Номер сезона — берём из самого slug (там реальный номер). Если их там нет
  // (странный slug) — fallback на пользовательский s.
  var realS = eps[0].s || s;
  return renderEpisodes(inv, host, title, orig, eps, realS);
}

module.exports = { handle: handle };
