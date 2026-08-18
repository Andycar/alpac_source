'use strict';
// =====================================================================
// Шаблон SISI JS-источника. Скопируйте каталог под новым id (a-z0-9_),
// поправьте имя модуля в manifest.json и две константы ниже:
//   DEFAULT_HOST — хост сайта без схемы;
//   MODULE_ID    — должен совпадать с id из manifest.json.
// При необходимости подкрутите buildListURL/parseList/extractQualities
// под особенности конкретной CMS (WordPress / DLE / самописный tube).
// =====================================================================

var DEFAULT_HOST = 'example.com';
var MODULE_ID    = '_template';

// handle(inv) — точка входа. inv.query.action одно из:
//   "list"  (по умолчанию) → каталог/поиск/категория
//   "video" / "vidosik"    → разбор страницы видео и выдача qualitys
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
  var pg     = parseInt(q.pg || '1', 10) || 1;
  var search = (q.search || '').trim();
  var cat    = (q.cat || '').trim();

  var url = buildListURL(base, search, cat, pg);
  log.info(MODULE_ID + ': list ' + url);

  var resp = http.get(url, {
    headers: {
      'Accept-Language': 'ru,en;q=0.5',
      'Referer': base + '/',
      'User-Agent': uaFromConfig(inv)
    }
  });
  if (!resp.ok) {
    log.warn(MODULE_ID + ': list http ' + resp.status);
    return { list: [], total_pages: 1 };
  }

  var html  = resp.text || '';
  var items = parseList(html, base, inv.host);
  return { list: items, total_pages: parseTotalPages(html) };
}

// Строит URL списка/поиска/категории. WordPress-дефолт.
// Для DLE-движка переопределите на /index.php?do=search&story={q}
// и /page/N или /index.php?cstart=N.
function buildListURL(base, search, cat, pg) {
  if (search) {
    return base + '/?s=' + encodeURIComponent(search) + (pg > 1 ? '&paged=' + pg : '');
  }
  if (cat) {
    return base + '/category/' + encodeURIComponent(cat) + '/' + (pg > 1 ? 'page/' + pg + '/' : '');
  }
  return pg > 1 ? base + '/page/' + pg + '/' : base + '/';
}

// Парсер списка. Универсальный паттерн: <a href title><img src/data-src/data-original></a>.
// Если у сайта свой шаблон — добавьте дополнительные регулярки до return.
function parseList(html, base, lampacHost) {
  var items = [];
  var seen  = {};

  // 1) Стандартный thumb-блок.
  var re = /<a[^>]+href="([^"]+)"[^>]*(?:title="([^"]*)")?[^>]*>[\s\S]{0,400}?<img[^>]+(?:data-original|data-src|src)="([^"]+)"[^>]*(?:alt="([^"]*)")?/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var href  = m[1];
    var title = (m[2] || m[4] || '').trim();
    var img   = m[3];
    if (!isVideoHref(href) || !title || !img) continue;
    if (seen[href]) continue;
    seen[href] = true;

    items.push(buildListItem(title, href, img, base, lampacHost));
  }
  return items;
}

// isVideoHref отсекает мусор (главная, теги, страница /page/2 и т.п.) — оставляем
// только ссылки, похожие на страницу одного видео.
function isVideoHref(href) {
  if (!href) return false;
  if (href.indexOf('#') === 0 || href === '/' || href.indexOf('javascript:') === 0) return false;
  // Простые эвристики: содержит цифры или "video" / "watch" / ".html".
  return /\/(video|watch|v|porno|ролик)[^/]*\/?[^/]*$|\d+|\.html?$/i.test(href);
}

function buildListItem(title, href, img, base, lampacHost) {
  if (href.indexOf('http') !== 0) href = base + (href.charAt(0) === '/' ? '' : '/') + href;
  if (img.indexOf('http')  !== 0) img  = base + (img.charAt(0)  === '/' ? '' : '/') + img;
  return {
    name: decodeEntities(title),
    video: lampacHost + '/sisi/cust/' + MODULE_ID + '/video?uri=' + encodeURIComponent(href),
    picture: img,
    json: true,
    bookmark: { site: MODULE_ID, href: href, image: img }
  };
}

function parseTotalPages(html) {
  var max = 1;
  var re  = /(?:\/page\/|paged=|page=|cstart=)(\d+)/g;
  var m;
  while ((m = re.exec(html)) !== null) {
    var n = parseInt(m[1], 10);
    if (n > max) max = n;
  }
  return max;
}

// ----- VIDEO ------------------------------------------------------------------

function doVideo(inv, base, q) {
  var uri = (q.uri || '').trim();
  if (!uri) return { qualitys: {} };
  if (uri.indexOf('http') !== 0) uri = base + (uri.charAt(0) === '/' ? '' : '/') + uri;

  log.info(MODULE_ID + ': video ' + uri);
  var resp = http.get(uri, {
    headers: {
      'Referer': base + '/',
      'Accept-Language': 'ru,en;q=0.5',
      'User-Agent': uaFromConfig(inv)
    }
  });
  if (!resp.ok) {
    log.warn(MODULE_ID + ': video http ' + resp.status);
    return { qualitys: {} };
  }

  var html = resp.text || '';
  var qs   = extractQualities(html);
  if (!hasKeys(qs)) {
    log.warn(MODULE_ID + ': no stream URLs found at ' + uri);
    return { qualitys: {} };
  }

  // Большинство CDN требует Referer от исходного сайта.
  var headers = { Referer: base + '/' };
  var hAll = {};
  Object.keys(qs).forEach(function(k){ hAll[k] = headers; });
  return { qualitys: qs, qualitys_headers: hAll };
}

// Извлекаем ссылки на mp4/m3u8 из плеера. По убыванию специфичности.
function extractQualities(html) {
  var qs = {};

  // 1) <source src="...mp4" label="720p" />
  var re1 = /<source[^>]+src="([^"]+\.(?:mp4|m3u8)[^"]*)"[^>]*(?:label|title|res|data-quality)="([^"]*)"/gi;
  var m;
  while ((m = re1.exec(html)) !== null) {
    qs[normalizeQ(m[2]) || 'auto'] = m[1];
  }
  if (hasKeys(qs)) return qs;

  // 2) PlayerJS multi-quality: file:"[480p]url.mp4 or url.mp4,[720p]..."
  var pjs = /["']file["']\s*:\s*["']((?:\[\d+p?\][^,"']+,?)+)["']/i.exec(html);
  if (pjs) {
    pjs[1].split(',').forEach(function(part) {
      var pp = /^\[(\d+p?)\](.+)$/.exec(part);
      if (pp) qs[normalizeQ(pp[1])] = pp[2];
    });
    if (hasKeys(qs)) return qs;
  }

  // 3) JWPlayer / generic JSON: "file":"...m3u8"
  var re3 = /["']file["']\s*:\s*["']([^"']+\.(?:mp4|m3u8)[^"']*)["']/gi;
  while ((m = re3.exec(html)) !== null) {
    qs.auto = m[1];
    if (hasKeys(qs)) break;
  }
  if (hasKeys(qs)) return qs;

  // 4) <video src="..."> прямой атрибут.
  var v = /<video[^>]+src="([^"]+\.(?:mp4|m3u8)[^"]*)"/i.exec(html);
  if (v) { qs.auto = v[1]; return qs; }

  // 5) Голый m3u8/mp4 в HTML/JS.
  var bare = /(https?:\/\/[^\s"'<>]+\.(?:m3u8|mp4)(?:\?[^\s"'<>]*)?)/i.exec(html);
  if (bare) qs.auto = bare[1];

  return qs;
}

// "720p" / "720" / "HD" → "720p". Для неизвестных — возвращаем как есть.
function normalizeQ(s) {
  s = (s || '').trim().toLowerCase();
  if (!s) return '';
  var m = /(\d{3,4})/.exec(s);
  if (m) return m[1] + 'p';
  return s;
}

// ----- helpers ----------------------------------------------------------------

function uaFromConfig(inv) {
  return (inv.config && inv.config.user_agent) ||
    'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36';
}

function hasKeys(o) { for (var k in o) if (o.hasOwnProperty(k)) return true; return false; }

function decodeEntities(s) {
  return s
    .replace(/&amp;/g, '&')
    .replace(/&quot;/g, '"')
    .replace(/&#0?39;/g, "'")
    .replace(/&laquo;/g, '«')
    .replace(/&raquo;/g, '»')
    .replace(/&mdash;/g, '—')
    .replace(/&ndash;/g, '–')
    .replace(/&nbsp;/g, ' ')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>');
}
