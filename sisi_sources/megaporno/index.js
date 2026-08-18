'use strict';
// SISI JS-источник: mega-porno.me — русскоязычный tube-агрегатор.
//
// Особенности сайта:
// 1) Главная: список из class="b-lazy rthmb" (нет пагинации). Категории — отдельный
//    путь /{slug}-video (page 1) или /video/{slug}-video_page-N.html (page 2+).
// 2) Item: <a href="https://mega-porno.me/online/{ID}/">
//      <img data-src="https://thumbs.mega-porno.love/.../1-360x240.jpg"
//           alt="TITLE" class="b-lazy rthmb">
//    Заголовок в alt, картинка в data-src (lazy load).
// 3) Видео-страница online/{ID}: качества — в HTML-строке
//      "[HD 720p]./common/mp4/video.mp4?q=720&v=65843&sg=2&vurl=267863,
//       [SD 360p]./common/mp4/video.mp4?q=360&v=65843&sg=2&vurl=267863"
//    Каждый ./common/mp4/video.mp4?... 302-редирект на CDN (cdn18.vids69.com и др).
// 4) Поиск на сайте есть, но через JS — пропустить, search-параметр игнорируем.

var DEFAULT_HOST = 'mega-porno.me';
var MODULE_ID    = 'megaporno';

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
  var cat = (q.cat || '').trim();

  var url = buildListURL(base, cat, pg);
  log.info(MODULE_ID + ': list ' + url);

  var resp = http.get(url, fsOpts(inv, base, 30));
  if (!resp.ok) { log.warn(MODULE_ID + ': list http ' + resp.status + (resp.error ? ' err=' + resp.error : '')); return { list: [], total_pages: 1 }; }

  var html = resp.text || '';
  return { list: parseList(html, base, inv.host), total_pages: parseTotalPages(html) };
}

function buildListURL(base, cat, pg) {
  // Главная — без пагинации.
  if (!cat) return base + '/';
  // Категория, например "aziatskoe-porno".
  // page 1: /aziatskoe-porno-video
  // page N: /video/aziatskoe-porno-video_page-N.html
  var slug = cat;
  if (slug.indexOf('-video') < 0) slug = slug + '-video'; // если пользователь указал без -video суффикса
  if (pg <= 1) return base + '/' + slug;
  return base + '/video/' + slug + '_page-' + pg + '.html';
}

function parseList(html, base, lampacHost) {
  var items = [], seen = {};
  // <a href="https://mega-porno.me/online/{ID}/">
  //   <img ... data-src="..." alt="TITLE" class="b-lazy rthmb">
  // </a>
  var re = /<a\s+href="(https?:\/\/[^"]+\/online\/\d+\/?)"\s*>\s*<img[^>]+data-src="([^"]+)"[^>]+alt="([^"]*)"[^>]+class="b-lazy\s+rthmb"/gi;
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

function parseTotalPages(html) {
  var max = 1, re = /_page-(\d+)\.html/g, m;
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

  var resp = http.get(uri, fsOpts(inv, base, 45));
  if (dbg) dbg.steps.push('page_status=' + resp.status + ' size=' + (resp.text ? resp.text.length : 0) + ' (flaresolverr)');
  if (!resp.ok) {
    var realErr = resp.error || ('http ' + resp.status);
    log.warn(MODULE_ID + ': video fetch failed: ' + realErr);
    if (dbg) dbg.steps.push('page_error=' + realErr);
    return out(dbg, 'flaresolverr error: ' + realErr, { qualitys: {} });
  }

  var html = resp.text || '';
  var qs = extractQualities(html, base);
  if (dbg) dbg.steps.push('qualities=' + JSON.stringify(Object.keys(qs)));

  if (!hasKeys(qs)) {
    log.warn(MODULE_ID + ': no streams found in ' + uri);
    return out(dbg, 'no qualities', { qualitys: {} });
  }

  // CDN (cdn18.vids69.com и др) обычно требует Referer от mega-porno.me.
  // Кладём прокси-URL прямо в qualitys.
  var headers = { 'Referer': base + '/', 'Origin': base };
  var wrapped = {};
  Object.keys(qs).forEach(function(k){
    wrapped[k] = proxy.urlWithHeaders(qs[k], MODULE_ID, headers);
  });
  if (dbg) dbg.steps.push('proxied=' + JSON.stringify(Object.keys(wrapped)));
  return out(dbg, 'ok', { qualitys: wrapped });
}

function out(dbg, reason, body) {
  if (dbg) { dbg.reason = reason; body._debug = dbg; }
  return body;
}

// extractQualities — у mega-porno качества лежат в HTML-строке вида:
//   [HD 720p]./common/mp4/video.mp4?q=720&v=65843&sg=2&vurl=267863,
//   [SD 360p]./common/mp4/video.mp4?q=360&v=65843&sg=2&vurl=267863
// Парсим эту строку. Берём только дефолтные варианты (sg=2), второй вариант
// "or ./common/mp4/video.mp4?... &sg=3" — fallback CDN, можно игнорить.
function extractQualities(html, base) {
  var qs = {};
  // [LABEL]URL,[LABEL2]URL2,... — URL начинается с ./common/mp4/.
  // Стопаем на запятой, но пропускаем " or " — это альтернативный CDN.
  var re = /\[(SD|HD|FHD|UHD|4K)\s*(\d+p?)\](\.\/common\/mp4\/video\.mp4\?[^,\]]+)/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var qLabel = (m[2] || m[1]).toLowerCase();
    if (!/p$/.test(qLabel)) qLabel += 'p';
    var urlPath = m[3];
    // Отрезаем " or ..." — мы хотим только основной URL до пробела.
    var spaceIdx = urlPath.indexOf(' ');
    if (spaceIdx >= 0) urlPath = urlPath.substring(0, spaceIdx);
    // Превращаем относительный (./common/mp4/...) в абсолютный.
    var abs = urlPath.charAt(0) === '.' ? base + urlPath.substring(1) : urlPath;
    qs[qLabel] = abs;
  }

  // Fallback: <video src="./common/mp4/video.mp4?q=360&...">
  if (!hasKeys(qs)) {
    var v = /<video[^>]+src="(\.\/common\/mp4\/video\.mp4\?[^"]+)"/i.exec(html);
    if (v) qs.auto = base + v[1].substring(1);
  }
  return qs;
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

// fsOpts — все запросы к mega-porno идут через FlareSolverr: Go HTTP-клиент
// не доверяет CA сайта (SSL_ERROR_SYSCALL / "unable to get local issuer
// certificate"), а Chrome из FS — без проблем. flareNoProxy=true чтобы FS
// контейнер ходил напрямую (без SOCKS5).
function fsOpts(inv, base, timeoutSec) {
  return {
    headers: defaultHeaders(inv, base),
    transport: 'flaresolverr',
    flareNoProxy: true,
    timeout: timeoutSec || 30
  };
}

function hasKeys(o) { for (var k in o) if (o.hasOwnProperty(k)) return true; return false; }

function decodeEntities(s) {
  return s.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#0?39;/g, "'")
    .replace(/&laquo;/g, '«').replace(/&raquo;/g, '»').replace(/&mdash;/g, '—')
    .replace(/&ndash;/g, '–').replace(/&nbsp;/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>');
}
