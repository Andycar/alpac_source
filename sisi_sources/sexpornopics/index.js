'use strict';
// SISI JS-источник: sexporno.pics — русскоязычный tube-агрегатор.
//
// Особенности сайта:
// 1) Vue.js SPA, первичный рендер отдаёт ~27 items, остальное подгружается JS-ом
//    (без рендера остаётся ~27, что нас устраивает на начало).
// 2) Item: <a href="/ex/{ID}-{TS}.html"> ... <img class="thumbnail__thumb"
//    src="https://ic.pckcdn.net/.../scrXX.jpg" alt="TITLE">.
// 3) Pagination: SPA-AJAX, в исходном HTML не вытащить → total_pages=1.
//    Пользователь использует поиск для другой выборки.
// 4) Поиск: GET /search/?q={query}.
// 5) Видео: на /ex/.html iframe src="https://playporn.cc/embed/{UUID}".
//    playporn.cc недоступен напрямую с datacenter-IP (timeout) — фетчим через
//    FlareSolverr. Embed-страница 1.3KB содержит <source src=".../mp4">.
//    CDN vc.pckcdn.net 302-редиректит на shard sX.pckcdn.net — обычный
//    HTTP follow работает, но HEAD CDN отказывает (405). Кладём прокси-URL
//    напрямую без pre-resolve — плеер сделает GET, прокси follow.

var DEFAULT_HOST = 'sexporno.pics';
var MODULE_ID    = 'sexpornopics';

function handle(inv) {
  var q = inv.query || {};
  var action = q.action || 'list';
  var host = (inv.config && inv.config.site_host) || DEFAULT_HOST;
  var base = 'https://' + host;

  if (action === 'video' || action === 'vidosik') return doVideo(inv, base, q);
  return doList(inv, base, q);
}

// ----- LIST -------------------------------------------------------------------

function doList(inv, base, q) {
  var pg = parseInt(q.pg || '1', 10) || 1;
  var search = (q.search || '').trim();
  var debug = q.debug === '1' || q.debug === 'true';
  var dbg = debug ? { steps: [] } : null;

  var url = buildListURL(base, search, pg);
  log.info(MODULE_ID + ': list ' + url);
  if (dbg) dbg.steps.push('list_url=' + url);

  var resp = http.get(url, { headers: defaultHeaders(inv, base) });
  if (dbg) dbg.steps.push('list_status=' + resp.status + ' size=' + (resp.text ? resp.text.length : 0));
  if (!resp.ok) {
    log.warn(MODULE_ID + ': list http ' + resp.status);
    var emptyList = { list: [], total_pages: 1 };
    if (dbg) { dbg.reason = 'list http'; emptyList._debug = dbg; }
    return emptyList;
  }

  var html = resp.text || '';
  var items = parseList(html, base, inv.host);
  if (dbg) {
    dbg.steps.push('items_count=' + items.length);
    dbg.reason = 'ok';
  }
  // total_pages всегда 1 — Vue-SPA подгружает остальное через JS, не из HTML.
  var result = { list: items, total_pages: 1 };
  if (dbg) result._debug = dbg;
  return result;
}

function buildListURL(base, search, pg) {
  if (search) {
    return base + '/search/?q=' + encodeURIComponent(search);
  }
  return base + '/';
}

function parseList(html, base, lampacHost) {
  var items = [], seen = {};
  // <a href="/ex/{ID}-{TS}.html">...<img class="thumbnail__thumb" src="..." alt="TITLE">...</a>
  var re = /<a[^>]+href="(\/ex\/[^"]+\.html)"[^>]*>([\s\S]*?)<\/a>/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var href = m[1], body = m[2];
    if (seen[href]) continue;
    seen[href] = true;

    var img = pickImg(body);
    var title = pickTitle(body);
    if (!title || !img) continue;

    var fullHref = href.indexOf('http') === 0 ? href : base + href;
    items.push({
      name: title,
      video: lampacHost + '/sisi/cust/' + MODULE_ID + '/video?uri=' + encodeURIComponent(fullHref),
      picture: img,
      json: true,
      bookmark: { site: MODULE_ID, href: fullHref, image: img }
    });
  }
  return items;
}

function pickImg(body) {
  var ds = /<img[^>]+(?:data-src|data-original)="([^"]+)"/i.exec(body);
  if (ds && ds[1].indexOf('data:image') !== 0) return ds[1];
  var s = /<img[^>]+src="([^"]+)"/i.exec(body);
  if (s && s[1].indexOf('data:image') !== 0) return s[1];
  return '';
}

function pickTitle(body) {
  var a = /<img[^>]+alt="([^"]+)"/i.exec(body);
  if (a) return decodeEntities(a[1]).trim();
  var t = /<img[^>]+title="([^"]+)"/i.exec(body);
  if (t) return decodeEntities(t[1]).trim();
  return '';
}

// ----- VIDEO ------------------------------------------------------------------

function doVideo(inv, base, q) {
  var uri = (q.uri || '').trim();
  var debug = q.debug === '1' || q.debug === 'true';
  var dbg = debug ? { steps: [] } : null;

  if (!uri) return out(dbg, 'no uri', { qualitys: {} });
  if (uri.indexOf('http') !== 0) uri = base + (uri.charAt(0) === '/' ? '' : '/') + uri;

  log.info(MODULE_ID + ': video ' + uri);
  if (dbg) dbg.steps.push('page_url=' + uri);

  // Шаг 1 — fetch /ex/.html, тянем iframe URL.
  var resp = http.get(uri, { headers: defaultHeaders(inv, base) });
  if (dbg) dbg.steps.push('page_status=' + resp.status + ' size=' + (resp.text ? resp.text.length : 0));
  if (!resp.ok) { log.warn(MODULE_ID + ': video http ' + resp.status); return out(dbg, 'page http', { qualitys: {} }); }

  var ifm = /<iframe[^>]+src="(https?:\/\/[^"]+\/embed\/[^"]+)"/i.exec(resp.text || '');
  if (!ifm) {
    log.warn(MODULE_ID + ': iframe URL not found');
    return out(dbg, 'no iframe', { qualitys: {} });
  }
  var embedURL = ifm[1];
  if (dbg) dbg.steps.push('embed_url=' + embedURL);

  // Шаг 2 — fetch playporn.cc/embed/{UUID} ЧЕРЕЗ FlareSolverr (с datacenter-IP
  // playporn.cc отдаёт timeout/блок). Embed-страница 1-2KB содержит <source src=...mp4>.
  var emb = http.get(embedURL, {
    headers: {
      'Accept-Language': 'ru,en;q=0.5',
      'Referer': base + '/',
      'User-Agent': defaultHeaders(inv, base)['User-Agent']
    },
    transport: 'flaresolverr',
    flareNoProxy: true,
    timeout: 30
  });
  if (dbg) dbg.steps.push('embed_status=' + emb.status + ' size=' + (emb.text ? emb.text.length : 0) + ' (flaresolverr)');
  if (!emb.ok) {
    log.warn(MODULE_ID + ': embed fetch failed: ' + (emb.error || emb.status));
    return out(dbg, 'embed fetch error', { qualitys: {} });
  }

  // <source src="https://vc.pckcdn.net/.../orig.mp4" type="video/mp4">
  var sm = /<source[^>]+src="([^"]+\.mp4[^"]*)"/i.exec(emb.text || '');
  if (!sm) {
    log.warn(MODULE_ID + ': <source> tag not found');
    return out(dbg, 'no <source>', { qualitys: {} });
  }
  var streamUrl = sm[1];
  if (dbg) dbg.steps.push('stream_url=' + streamUrl);

  // CDN vc.pckcdn.net 302-редиректит на shard sX.pckcdn.net — прокси сам
  // follow редирект и проставит Referer playporn.cc.
  // pre-resolve НЕ делаем — CDN не любит HEAD (405), но GET работает.
  var headers = {
    'Referer': 'https://playporn.cc/',
    'Origin': 'https://playporn.cc'
  };
  var proxied = proxy.urlWithHeaders(streamUrl, MODULE_ID, headers);
  if (dbg) dbg.steps.push('proxied=auto');
  return out(dbg, 'ok', { qualitys: { auto: proxied } });
}

function out(dbg, reason, body) {
  if (dbg) { dbg.reason = reason; body._debug = dbg; }
  return body;
}

// ----- helpers ----------------------------------------------------------------

function defaultHeaders(inv, base) {
  return {
    'Accept-Language': 'ru,en;q=0.5',
    'Referer': base + '/',
    'User-Agent': (inv.config && inv.config.user_agent) ||
      'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36'
  };
}

function decodeEntities(s) {
  return s.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#0?39;/g, "'")
    .replace(/&laquo;/g, '«').replace(/&raquo;/g, '»').replace(/&mdash;/g, '—')
    .replace(/&ndash;/g, '–').replace(/&nbsp;/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>');
}
