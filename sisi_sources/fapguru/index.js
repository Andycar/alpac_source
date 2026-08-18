'use strict';
// SISI JS-источник: x.fap-guru.pro — русскоязычный tube-каталог.
//
// Особенности сайта:
// 1) Возрастная заглушка (age gate). Без cookie is_age_verified=1 каталог
//    отдаёт 15KB-страницу с iframe-логином. Cookie ставим на каждый запрос.
// 2) Списки по адресу /videos?page=N, поиск /search?q=...&page=N,
//    категории /categories/{slug}?page=N (либо /categories/popular).
// 3) Элементы списка: <div id="N" vkey="..." class="video trailer">
//      <a href="/video/SLUG" alt="TITLE">
//        <img class="image" src="/images/N.jpg?00" alt="TITLE">
//      </a>
//    Тот же шаблон что у sex-studentki.live (общий движок).
// 4) Стрим лежит за server-side redirect: <source src="/link_cs/{base64}?{key}"> —
//    302 на CDN mp4. CDN требует Referer: https://x.fap-guru.pro/.

var DEFAULT_HOST = 'x.fap-guru.pro';
var MODULE_ID    = 'fapguru';
var AGE_COOKIE   = 'is_age_verified=1';

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
  var cat = (q.cat || '').trim();

  var url = buildListURL(base, search, cat, pg);
  log.info(MODULE_ID + ': list ' + url);

  var resp = http.get(url, { headers: defaultHeaders(inv, base) });
  if (!resp.ok) { log.warn(MODULE_ID + ': list http ' + resp.status); return { list: [], total_pages: 1 }; }

  var html = resp.text || '';
  return { list: parseList(html, base, inv.host), total_pages: parseTotalPages(html) };
}

function buildListURL(base, search, cat, pg) {
  if (search) {
    return base + '/search?q=' + encodeURIComponent(search) + (pg > 1 ? '&page=' + pg : '');
  }
  if (cat) {
    return base + '/categories/' + encodeURIComponent(cat) + (pg > 1 ? '?page=' + pg : '');
  }
  return base + '/videos' + (pg > 1 ? '?page=' + pg : '');
}

function parseList(html, base, lampacHost) {
  var items = [], seen = {};
  // <div id="ID" vkey="..." class="video trailer"> <a href="/video/SLUG" alt="TITLE">
  //   <img class="image" src="/images/ID.jpg?00" alt="TITLE" ...>
  var re = /<div[^>]+class="video\s+trailer"[\s\S]*?<a\s+href="(\/video\/[^"]+)"[^>]*alt="([^"]*)"[\s\S]*?<img[^>]+class="image"[^>]+src="([^"]+)"/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var href = m[1], title = decodeEntities(m[2]), img = m[3];
    if (!title || !img) continue;
    if (seen[href]) continue;
    seen[href] = true;

    if (img.indexOf('http') !== 0) img = base + (img.charAt(0) === '/' ? '' : '/') + img;

    items.push({
      name: title,
      video: lampacHost + '/sisi/cust/' + MODULE_ID + '/video?uri=' + encodeURIComponent(href),
      picture: img,
      json: true,
      bookmark: { site: MODULE_ID, href: href, image: img }
    });
  }
  return items;
}

function parseTotalPages(html) {
  var max = 1, re = /[?&]page=(\d+)/g, m;
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

  var html = resp.text || '';
  // <source src="https://x.fap-guru.pro/link_cs/{base64}?{key}"> — отсекаем
  // /system/stat (метрика) если попадётся. Возвращает 302 → CDN mp4.
  var streamUrl = '';
  var re = /<source\s+src="(https?:\/\/[^"]+)"/gi, m;
  while ((m = re.exec(html)) !== null) {
    if (m[1].indexOf('/system/stat') >= 0) continue;
    streamUrl = m[1];
    break;
  }
  if (dbg) dbg.steps.push('stream_url=' + (streamUrl || '<not found>'));
  if (!streamUrl) {
    log.warn(MODULE_ID + ': stream URL not found at ' + uri);
    return out(dbg, 'no stream', { qualitys: {} });
  }

  // CDN требует Referer от сайта. Кладём прокси-URL прямо в qualitys —
  // injectDrochubProxyURLs продублирует в qualitys_proxy как fallback.
  var headers = { 'Referer': base + '/', 'Origin': base };
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
    'Cookie': AGE_COOKIE,
    'User-Agent': (inv.config && inv.config.user_agent) ||
      'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36'
  };
}

function decodeEntities(s) {
  return s.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#0?39;/g, "'")
    .replace(/&laquo;/g, '«').replace(/&raquo;/g, '»').replace(/&mdash;/g, '—')
    .replace(/&ndash;/g, '–').replace(/&nbsp;/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>');
}
