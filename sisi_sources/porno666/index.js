'use strict';
// SISI JS-источник: porno666.link — русскоязычный tube-каталог.
//
// Особенности сайта:
// 1) "/" редиректит на "/cat/" (только список категорий, без видео).
// 2) Видео живут в категориях /categories/{slug}/, пагинация /categories/{slug}/{N}/.
// 3) Item: <a href="https://porno666.link/video/{ID}/">
//      <div class="img"><img class="thumb lazy-load" src="/images/tl.png"
//        data-original=".../{ID}/533x300/5.jpg" alt="TITLE" ...></div>
//    </a>
// 4) Поиск: GET /search/{query}/ (~7-15 элементов).
//
// 5) Стрим — здесь самая бяка. На странице /video/ есть прямые URLы
//    /get_file/.../{ID}.mp4?v-acctoken=...&download=true — но они выдают 403.
//    На странице /video/ ещё есть iframe на porno666.video/embed/{ID}.
//    Внутри embed работает kt_player.js с конфигом, в котором лежат рабочие
//    URLы с параметром &embed=true:
//        video_url: '...20327_360p.mp4/?v-acctoken=...&embed=true'  // 360p
//        video_alt_url: '...20327.mp4/?v-acctoken=...&embed=true'    // 720p (HD)
//    Эти URLы 302-редиректят на реальный CDN (mega1.videofile.me/...mp4 ~62MB).
//    CRITICAL: Referer должен быть https://porno666.video/embed/{ID}.
//
//    kt_player config внутри embed-страницы — статический HTML (~12KB),
//    поэтому FlareSolverr достаточно с request.get без долгого рендера.

var DEFAULT_HOST  = 'porno666.link';
var EMBED_HOST    = 'porno666.video';
var MODULE_ID     = 'porno666';
var DEFAULT_CAT   = 'russkoe';

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
  var cat = (q.cat || '').trim() || DEFAULT_CAT;

  var url = buildListURL(base, search, cat, pg);
  log.info(MODULE_ID + ': list ' + url);

  var resp = http.get(url, { headers: defaultHeaders(inv, base) });
  if (!resp.ok) { log.warn(MODULE_ID + ': list http ' + resp.status); return { list: [], total_pages: 1 }; }

  var html = resp.text || '';
  return { list: parseList(html, base, inv.host), total_pages: parseTotalPages(html) };
}

function buildListURL(base, search, cat, pg) {
  if (search) {
    var s = base + '/search/' + encodeURIComponent(search) + '/';
    return pg > 1 ? s + pg + '/' : s;
  }
  var c = base + '/categories/' + encodeURIComponent(cat) + '/';
  return pg > 1 ? c + pg + '/' : c;
}

function parseList(html, base, lampacHost) {
  var items = [], seen = {};
  var re = /<a[^>]+href="(https?:\/\/[^"]+\/video\/(\d+)\/?)"[^>]*>[\s\S]{0,1200}?<img[^>]+data-original="([^"]+)"[^>]+alt="([^"]*)"/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var href = m[1], img = m[3], title = decodeEntities(m[4]);
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
  var max = 1, re = /href="\/(?:categories|search)\/[^"\/]+\/(\d+)\/"/g, m;
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

  // Извлекаем ID видео из URL /video/{ID}/.
  var idMatch = /\/video\/(\d+)/.exec(uri);
  if (!idMatch) return out(dbg, 'cannot parse video id from ' + uri, { qualitys: {} });
  var videoID = idMatch[1];
  var embedURL = 'https://' + EMBED_HOST + '/embed/' + videoID;

  log.info(MODULE_ID + ': video id=' + videoID + ' embed=' + embedURL);
  if (dbg) { dbg.steps.push('video_id=' + videoID); dbg.steps.push('embed_url=' + embedURL); }

  // Embed-страница содержит kt_player config с рабочими ?embed=true URLами.
  // Идём через FlareSolverr — Go HTTP-клиент тоже работает, но FS гарантирует
  // что у нас не возникнет каких-то TLS/UA-сюрпризов от тёмной CDN.
  var resp = http.get(embedURL, {
    headers: {
      'Accept-Language': 'ru,en;q=0.5',
      'Referer': base + '/',
      'User-Agent': defaultHeaders(inv, base)['User-Agent']
    },
    transport: 'flaresolverr',
    flareNoProxy: true,
    timeout: 30
  });
  if (dbg) dbg.steps.push('embed_status=' + resp.status + ' size=' + (resp.text ? resp.text.length : 0) + ' (flaresolverr)');
  if (!resp.ok) {
    log.warn(MODULE_ID + ': embed fetch failed: ' + (resp.error || resp.status));
    return out(dbg, 'embed fetch error', { qualitys: {} });
  }

  var html = resp.text || '';
  var qs = extractKTPlayerQualities(html);
  if (dbg) dbg.steps.push('qualities=' + JSON.stringify(Object.keys(qs)));

  if (!hasKeys(qs)) {
    log.warn(MODULE_ID + ': kt_player config not parseable');
    return out(dbg, 'no qualities (kt_player parse fail)', { qualitys: {} });
  }

  // CDN 302-редиректит embed-URL на mega1.videofile.me/...mp4. Если бы мы
  // передали embed-URL клиенту через прокси — наш прокси отдал бы 302 на
  // следующий /proxy/{aes} обёрнутый над mega1, и Lampa-плеер при HEAD-проверке
  // увидел бы только 302+text/html и решил что это не видео.
  //
  // Лечим это resolve-ом редиректа здесь: HEAD по embed-URL с Referer →
  // финальный CDN URL → проксируем уже его. Player увидит сразу 200 + mp4.
  var headers = {
    'Referer': embedURL,
    'Origin': 'https://' + EMBED_HOST,
    'User-Agent': defaultHeaders(inv, base)['User-Agent']
  };
  var wrapped = {};
  Object.keys(qs).forEach(function(k){
    var resolved = resolveRedirect(qs[k], headers);
    if (dbg) dbg.steps.push('resolve_' + k + '=' + (resolved !== qs[k] ? 'redirected' : 'direct'));
    wrapped[k] = proxy.urlWithHeaders(resolved, MODULE_ID, headers);
  });
  if (dbg) dbg.steps.push('proxied=' + JSON.stringify(Object.keys(wrapped)));
  return out(dbg, 'ok', { qualitys: wrapped });
}

// resolveRedirect — следуем 302 чтобы получить финальный CDN URL. Возвращаем
// resp.url (которая обновляется goja-runtime после auto-follow). Если что-то
// сломалось — возвращаем исходный URL (пусть прокси сам обрабатывает).
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

// extractKTPlayerQualities — у kt_player конфиг встроен в HTML как объект
// в стиле «var c = {video_url: '...', video_url_text: '360p', video_alt_url:
// '...', video_alt_url_text: '720p', ...}». Тащим обе ссылки.
function extractKTPlayerQualities(html) {
  var qs = {};
  // Основной поток (обычно 360p).
  var m1 = /video_url\s*:\s*['"]([^'"]+\.mp4[^'"]*)['"]/i.exec(html);
  var l1 = /video_url_text\s*:\s*['"]([^'"]+)['"]/i.exec(html);
  if (m1) qs[(l1 ? l1[1] : 'auto')] = m1[1];

  // HD-альтернатива (обычно 720p или 1080p).
  var m2 = /video_alt_url\s*:\s*['"]([^'"]+\.mp4[^'"]*)['"]/i.exec(html);
  var l2 = /video_alt_url_text\s*:\s*['"]([^'"]+)['"]/i.exec(html);
  if (m2) qs[(l2 ? l2[1] : 'hd')] = m2[1];

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

function hasKeys(o) { for (var k in o) if (o.hasOwnProperty(k)) return true; return false; }

function decodeEntities(s) {
  return s.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#0?39;/g, "'")
    .replace(/&laquo;/g, '«').replace(/&raquo;/g, '»').replace(/&mdash;/g, '—')
    .replace(/&ndash;/g, '–').replace(/&nbsp;/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>');
}
