'use strict';
// SISI JS-источник: sex.pornobolt.in — русскоязычный tube-агрегатор.
//
// Особенности сайта:
// 1) Items: <a href="/video/SLUG.html" itemprop="url">
//      <div class="thumb-box"><div class="video-preview"></div>
//        <img class="tumb-img" src="https://ru.pbcdn.tv/.../huge-SLUG.jpg" alt="TITLE">
//      </div>...
//    </a>
//    Заголовок в alt у <img class="tumb-img">.
// 2) Пагинация: главная — /N (например /2, /3, ... до 764).
// 3) Поиск: /search/{URL-encoded query}.
// 4) Видео: Playerjs({id:"...", file: "/videofile/BASE64", poster:"...", ...})
//    BASE64 декодируется в ["pornhub","videoID",0] — это meta-aggregator со своим
//    CDN. /videofile/BASE64 → 302 на https://s4.ebacdn.net/.../*.mp4 (pornobolt's
//    own CDN, не оригинальный pornhub). Стрим — обычный mp4, требует Referer.

var DEFAULT_HOST = 'sex.pornobolt.in';
var MODULE_ID    = 'pbolt';

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

  var url = buildListURL(base, search, pg);
  log.info(MODULE_ID + ': list ' + url);

  var resp = http.get(url, { headers: defaultHeaders(inv, base) });
  if (!resp.ok) { log.warn(MODULE_ID + ': list http ' + resp.status); return { list: [], total_pages: 1 }; }

  var html = resp.text || '';
  return { list: parseList(html, base, inv.host), total_pages: parseTotalPages(html) };
}

function buildListURL(base, search, pg) {
  if (search) {
    // /search/{query} — пагинация через /search/{query}/N (предположение,
    // если у сайта другая схема, page=1 всё равно вернёт первые 42 элемента).
    var s = base + '/search/' + encodeURIComponent(search);
    return pg > 1 ? s + '/' + pg : s;
  }
  return pg > 1 ? base + '/' + pg : base + '/';
}

function parseList(html, base, lampacHost) {
  var items = [], seen = {};
  // <a href="/video/SLUG.html" itemprop="url"> ... <img class="tumb-img" src="..." alt="TITLE">
  var re = /<a[^>]+href="(\/video\/[^"]+\.html)"[^>]*>[\s\S]*?<img[^>]+class="tumb-img"[^>]+src="([^"]+)"[^>]*alt="([^"]*)"/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var href = m[1], img = m[2], title = decodeEntities(m[3]);
    if (!title || !img) continue;
    if (seen[href]) continue;
    seen[href] = true;

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

// Pagination: max page number из ссылок вида /N (при поиске — /search/Q/N).
function parseTotalPages(html) {
  var max = 1, re = /href="\/(?:search\/[^"\/]+\/)?(\d+)"/g, m;
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
  // Playerjs({id:"...", file: "/videofile/BASE64", ...})
  var fm = /Playerjs\s*\(\s*\{[^}]*?file\s*:\s*["']([^"']+)["']/i.exec(html);
  if (!fm) {
    log.warn(MODULE_ID + ': Playerjs file not found in ' + uri);
    if (dbg) dbg.steps.push('playerjs_match=null');
    return out(dbg, 'no playerjs', { qualitys: {} });
  }
  var filePath = fm[1];
  if (dbg) dbg.steps.push('file_path=' + filePath);

  // /videofile/{base64} — относительный, превращаем в абсолютный.
  // Это endpoint-обёртка которая 302-редиректит на CDN mp4. Прокси проследует
  // редирект и проставит наш Referer на каждый шаг.
  var streamUrl = filePath.indexOf('http') === 0 ? filePath
                : base + (filePath.charAt(0) === '/' ? '' : '/') + filePath;

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
    'User-Agent': (inv.config && inv.config.user_agent) ||
      'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36'
  };
}

function decodeEntities(s) {
  return s.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#0?39;/g, "'")
    .replace(/&laquo;/g, '«').replace(/&raquo;/g, '»').replace(/&mdash;/g, '—')
    .replace(/&ndash;/g, '–').replace(/&nbsp;/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>');
}
