'use strict';
// SISI JS-источник: pornokaef.live — русскоязычный tube-агрегатор.
// Близнец pornobolt.in (тот же CMS), отличия только в путях и mirror-домене.
//
// Особенности сайта:
// 1) pornokaef.live 302-редиректит на mirror xx.pornokaef.love. Подменяем base
//    автоматически по resp.url, чтобы относительные ссылки строились правильно.
// 2) Item: <a href="/videotape/SLUG.html"> <img src="..." title="TITLE">
//    title в атрибуте title= у <img>.
// 3) Pagination: /N (e.g. /2, /3, ..., /462).
// 4) Поиск: GET /videos/{query} (form action = "/videos").
// 5) Видео: Playerjs({file: "/broadkast/BASE64"}). BASE64 → ["pornhub","ID",0].
//    /broadkast/{base64} → 302 на CDN ru.kaefcdn.net (свой кэш). Стрим — mp4.

var DEFAULT_HOST = 'pornokaef.live';
var MODULE_ID    = 'pornokaef';

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
  if (dbg) dbg.steps.push('list_status=' + resp.status + ' size=' + (resp.text ? resp.text.length : 0) + ' final_url=' + (resp.url || ''));
  if (!resp.ok) {
    log.warn(MODULE_ID + ': list http ' + resp.status);
    var emptyList = { list: [], total_pages: 1 };
    if (dbg) { dbg.reason = 'list http'; emptyList._debug = dbg; }
    return emptyList;
  }

  var finalBase = resolveBase(resp.url, base);
  var html = resp.text || '';
  var items = parseList(html, finalBase, inv.host);
  var pages = parseTotalPages(html);
  if (dbg) {
    dbg.steps.push('items_count=' + items.length);
    dbg.steps.push('total_pages=' + pages);
    dbg.reason = 'ok';
  }
  var result = { list: items, total_pages: pages };
  if (dbg) result._debug = dbg;
  return result;
}

function resolveBase(respUrl, fallback) {
  if (!respUrl) return fallback;
  var m = /^(https?:\/\/[^\/]+)/i.exec(respUrl);
  return m ? m[1] : fallback;
}

function buildListURL(base, search, pg) {
  if (search) {
    // Form action = /videos, data-url = /videos/{query} — пагинация /videos/{q}/{N}.
    var s = base + '/videos/' + encodeURIComponent(search);
    return pg > 1 ? s + '/' + pg : s;
  }
  return pg > 1 ? base + '/' + pg : base + '/';
}

function parseList(html, base, lampacHost) {
  var items = [], seen = {};
  // Захватываем целиком блок <a href=/videotape/...>...</a>, потом отдельно
  // вытаскиваем title и реальный image URL (предпочитая data-src над src,
  // фильтруя data:image-плейсхолдеры lazy-load).
  var re = /<a[^>]+href="(\/videotape\/[^"]+\.html)"[^>]*>([\s\S]*?)<\/a>/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var href = m[1], body = m[2];
    if (seen[href]) continue;
    seen[href] = true;

    var title = pickTitle(body);
    var img = pickImg(body);
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

// pickImg — предпочитаем data-src, потом любой не-плейсхолдер src.
function pickImg(body) {
  var ds = /<img[^>]+(?:data-src|data-original)="([^"]+)"/i.exec(body);
  if (ds && ds[1].indexOf('data:image') !== 0) return ds[1];
  var s = /<img[^>]+src="([^"]+)"/i.exec(body);
  if (s && s[1].indexOf('data:image') !== 0) return s[1];
  return '';
}

// pickTitle — title= у img, alt= у img, либо первый текст ≥3 символа в блоке.
function pickTitle(body) {
  var t = /<img[^>]+title="([^"]+)"/i.exec(body);
  if (t) return decodeEntities(t[1]);
  var a = /<img[^>]+alt="([^"]+)"/i.exec(body);
  if (a) return decodeEntities(a[1]);
  return '';
}

// Pagination: max page из /N (или /videos/{q}/N для поиска).
function parseTotalPages(html) {
  var max = 1, re = /href="\/(?:videos\/[^"\/]+\/)?(\d+)"/g, m;
  while ((m = re.exec(html)) !== null) {
    var n = parseInt(m[1], 10);
    if (n > max) max = n;
  }
  return max;
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

  var resp = http.get(uri, { headers: defaultHeaders(inv, base) });
  if (dbg) dbg.steps.push('page_status=' + resp.status + ' size=' + (resp.text ? resp.text.length : 0));
  if (!resp.ok) { log.warn(MODULE_ID + ': video http ' + resp.status); return out(dbg, 'page http', { qualitys: {} }); }

  // Origin может смениться на mirror — используем resp.url для правильного Referer.
  var finalBase = resolveBase(resp.url, base);
  var finalUri = resp.url || uri;

  // Playerjs({id:"...", file: "/broadkast/BASE64", ...})
  var fm = /Playerjs\s*\(\s*\{[^}]*?file\s*:\s*["']([^"']+)["']/i.exec(resp.text || '');
  if (!fm) {
    log.warn(MODULE_ID + ': Playerjs file not found in ' + uri);
    if (dbg) dbg.steps.push('playerjs_match=null');
    return out(dbg, 'no playerjs', { qualitys: {} });
  }
  var filePath = fm[1];
  if (dbg) dbg.steps.push('file_path=' + filePath);

  // /broadkast/{base64} — endpoint-обёртка с 302 на CDN. Превращаем в абсолют.
  var streamUrl = filePath.indexOf('http') === 0 ? filePath
                : finalBase + (filePath.charAt(0) === '/' ? '' : '/') + filePath;

  // Pre-resolve 302: тот же приём что у porno666 — иначе Lampa-плеер при HEAD
  // увидит только 302+text/html и решит что это не видео. Idempotent — если
  // редирект не выявлен (2xx сразу), возвращаем исходный URL.
  var headers = { 'Referer': finalUri, 'Origin': finalBase };
  var resolved = resolveRedirect(streamUrl, headers);
  if (dbg) dbg.steps.push('resolve=' + (resolved !== streamUrl ? 'redirected' : 'direct'));

  var proxied = proxy.urlWithHeaders(resolved, MODULE_ID, headers);
  if (dbg) dbg.steps.push('proxied=auto');
  return out(dbg, 'ok', { qualitys: { auto: proxied } });
}

// resolveRedirect — следуем 302 чтобы получить финальный CDN URL.
function resolveRedirect(url, headers) {
  try {
    var resp = http.head(url, { headers: headers, timeout: 15 });
    if (resp && resp.ok && resp.url && /\.(?:mp4|m3u8)/i.test(resp.url)) {
      return resp.url;
    }
  } catch (e) { /* ignore */ }
  return url;
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
