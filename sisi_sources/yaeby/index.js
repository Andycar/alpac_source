'use strict';
// SISI JS-источник: www.yaeby.pro (русскоязычный tube).
// Каркас. При первом запуске включите модуль и проверьте /sisi/cust/yaeby —
// при пустом list или video подкрутите регулярки в parseList/extractQualities.

var DEFAULT_HOST = 'www.yaeby.pro';
var MODULE_ID    = 'yaeby';

function handle(inv) {
  var q = inv.query || {};
  var action = q.action || 'list';
  var host = (inv.config && inv.config.site_host) || DEFAULT_HOST;
  var base = 'https://' + host;

  if (action === 'video' || action === 'vidosik') return doVideo(inv, base, q);
  return doList(inv, base, q);
}

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

// yaeby — DLE-движок: поиск через /index.php?do=search, пагинация /page/N/.
function buildListURL(base, search, cat, pg) {
  if (search) {
    var body = base + '/index.php?do=search&subaction=search&story=' + encodeURIComponent(search);
    if (pg > 1) body += '&search_start=' + pg;
    return body;
  }
  if (cat) return base + '/' + encodeURIComponent(cat) + '/' + (pg > 1 ? 'page/' + pg + '/' : '');
  return pg > 1 ? base + '/page/' + pg + '/' : base + '/';
}

function parseList(html, base, lampacHost) {
  var items = [], seen = {};
  // Тумбнейл-блок DLE: <a href="/X.html" title="Y"><img src/data-src="Z"></a>
  var re = /<a[^>]+href="([^"]+\.html|\/[^"]+\/[^"]+)"[^>]*(?:title="([^"]*)")?[^>]*>[\s\S]{0,500}?<img[^>]+(?:data-original|data-src|src)="([^"]+)"[^>]*(?:alt="([^"]*)")?/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var href = m[1], title = (m[2] || m[4] || '').trim(), img = m[3];
    if (!title || !img) continue;
    if (seen[href]) continue;
    seen[href] = true;
    items.push(buildListItem(title, href, img, base, lampacHost));
  }
  return items;
}

function buildListItem(title, href, img, base, lampacHost) {
  if (href.indexOf('http') !== 0) href = base + (href.charAt(0) === '/' ? '' : '/') + href;
  if (img.indexOf('http')  !== 0) img  = base + (img.charAt(0)  === '/' ? '' : '/') + img;
  return {
    name: decodeEntities(title),
    video: lampacHost + '/sisi/cust/' + MODULE_ID + '/video?uri=' + encodeURIComponent(href),
    picture: img, json: true,
    bookmark: { site: MODULE_ID, href: href, image: img }
  };
}

function parseTotalPages(html) {
  var max = 1, re = /(?:\/page\/|paged=|page=|search_start=|cstart=)(\d+)/g, m;
  while ((m = re.exec(html)) !== null) { var n = parseInt(m[1], 10); if (n > max) max = n; }
  return max;
}

function doVideo(inv, base, q) {
  var uri = (q.uri || '').trim();
  if (!uri) return { qualitys: {} };
  if (uri.indexOf('http') !== 0) uri = base + (uri.charAt(0) === '/' ? '' : '/') + uri;

  log.info(MODULE_ID + ': video ' + uri);
  var resp = http.get(uri, { headers: defaultHeaders(inv, base) });
  if (!resp.ok) { log.warn(MODULE_ID + ': video http ' + resp.status); return { qualitys: {} }; }

  var qs = extractQualities(resp.text || '');
  if (!hasKeys(qs)) { log.warn(MODULE_ID + ': no streams'); return { qualitys: {} }; }

  var headers = { Referer: base + '/' }, hAll = {};
  Object.keys(qs).forEach(function(k){ hAll[k] = headers; });
  return { qualitys: qs, qualitys_headers: hAll };
}

function extractQualities(html) {
  var qs = {}, m;

  // 1) <source src="...mp4" label="720p">
  var re1 = /<source[^>]+src="([^"]+\.(?:mp4|m3u8)[^"]*)"[^>]*(?:label|title|res|data-quality)="([^"]*)"/gi;
  while ((m = re1.exec(html)) !== null) qs[normalizeQ(m[2]) || 'auto'] = m[1];
  if (hasKeys(qs)) return qs;

  // 2) PlayerJS multi-quality: file:"[480p]url1.mp4,[720p]url2.mp4"
  var pjs = /["']file["']\s*:\s*["']((?:\[\d+p?\][^,"']+,?)+)["']/i.exec(html);
  if (pjs) {
    pjs[1].split(',').forEach(function(part) {
      var pp = /^\[(\d+p?)\](.+)$/.exec(part);
      if (pp) qs[normalizeQ(pp[1])] = pp[2];
    });
    if (hasKeys(qs)) return qs;
  }

  // 3) "file":"...m3u8"
  var re3 = /["']file["']\s*:\s*["']([^"']+\.(?:mp4|m3u8)[^"']*)["']/gi;
  while ((m = re3.exec(html)) !== null) { qs.auto = m[1]; if (hasKeys(qs)) break; }
  if (hasKeys(qs)) return qs;

  // 4) <video src=...>
  var v = /<video[^>]+src="([^"]+\.(?:mp4|m3u8)[^"]*)"/i.exec(html);
  if (v) { qs.auto = v[1]; return qs; }

  // 5) Голый mp4/m3u8.
  var bare = /(https?:\/\/[^\s"'<>]+\.(?:m3u8|mp4)(?:\?[^\s"'<>]*)?)/i.exec(html);
  if (bare) qs.auto = bare[1];
  return qs;
}

function normalizeQ(s) {
  s = (s || '').trim().toLowerCase(); if (!s) return '';
  var m = /(\d{3,4})/.exec(s); return m ? m[1] + 'p' : s;
}

function defaultHeaders(inv, base) {
  return {
    'Accept-Language': 'ru,en;q=0.5',
    'Referer': base + '/',
    'User-Agent': (inv.config && inv.config.user_agent) ||
      'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36'
  };
}

function hasKeys(o) { for (var k in o) if (o.hasOwnProperty(k)) return true; return false; }

function decodeEntities(s) {
  return s.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#0?39;/g, "'")
    .replace(/&laquo;/g, '«').replace(/&raquo;/g, '»').replace(/&mdash;/g, '—')
    .replace(/&ndash;/g, '–').replace(/&nbsp;/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>');
}
