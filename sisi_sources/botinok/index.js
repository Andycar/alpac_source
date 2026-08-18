'use strict';
// SISI JS-источник: botinok.porn — русскоязычный каталог-агрегатор над разными
// tube-сайтами (xvideos, pornhub, iceporn, mylust, …). Сам botinok видео не
// стримит, на странице /rolik/* стоит iframe с javascript-redirect-ом на
// один из upstream-плееров. Поддержка экстракции добавлена для xvideos и
// pornhub (~70% каталога). Прочие провайдеры → пустой qualitys + понятная
// причина в _debug.reason.
//
// Особенности сайта:
// 1) Главная страница содержит ТОЛЬКО список категорий (<li class="tmb">), не видео.
// 2) Видео живут в категориях: /c/{slug}/, пагинация через ?p=N (infinite scroll).
// 3) Поиск на сайте отсутствует — параметр search игнорируется.
// 4) Заголовки в листинге пустые (alt=""), подменяем slug-ом из URL.

var DEFAULT_HOST = 'botinok.porn';
var MODULE_ID    = 'botinok';

// Дефолтная "стартовая" категория, когда не задан ни search, ни cat.
// /cool/ — это раздел "горяченькое" с видео (а не список категорий, как / ).
var DEFAULT_CAT  = 'cool';

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
  var pg  = parseInt(q.pg || '1', 10) || 1;
  var cat = (q.cat || '').trim() || DEFAULT_CAT;

  // /c/{cat}/?p=N — page=1 без параметра.
  var url = base + (cat === 'cool' ? '/cool/' : '/c/' + encodeURIComponent(cat) + '/');
  if (pg > 1) url += '?p=' + pg;

  log.info(MODULE_ID + ': list ' + url);
  var resp = http.get(url, { headers: defaultHeaders(inv, base) });
  if (!resp.ok) { log.warn(MODULE_ID + ': list http ' + resp.status); return { list: [], total_pages: 1 }; }

  var html = resp.text || '';
  var items = parseList(html, base, inv.host);
  // Сайт использует infinite scroll и маркер __INFINITE_END__ на последней странице.
  // Если маркера нет — даём клиенту следующую страницу (минимум +1 от текущей).
  var pages = html.indexOf('__INFINITE_END__') >= 0 ? pg : pg + 1;
  return { list: items, total_pages: pages };
}

function parseList(html, base, lampacHost) {
  var items = [], seen = {};
  // <li class="tmb use-data-id"> ... <a href="/rolik/{id}/{slug}.php"> ... <img src/data-src="...">
  var re = /<li[^>]+class="tmb\s+use-data-id"[^>]*>[\s\S]*?<a[^>]+href="(\/rolik\/[^"]+\.php)"[\s\S]*?<img[^>]+(?:data-src|src)="([^"]+)"/gi;
  var m;
  while ((m = re.exec(html)) !== null) {
    var href = m[1], img = m[2];
    if (seen[href]) continue;
    seen[href] = true;

    // Заголовок не лежит в листинге — берём slug из URL: /rolik/35227785/natural.php → "Natural"
    var slug = (/\/rolik\/\d+\/([^.]+)\.php/.exec(href) || [, ''])[1];
    var title = humanizeSlug(slug) || ('Видео #' + (slug || (items.length + 1)));

    if (img.indexOf('http') !== 0) img = base + (img.charAt(0) === '/' ? '' : '/') + img;

    items.push({
      name: title,
      video: lampacHost + '/sisi/cust/' + MODULE_ID + '/video?uri=' + encodeURIComponent(base + href),
      picture: img,
      json: true,
      bookmark: { site: MODULE_ID, href: href, image: img }
    });
  }
  return items;
}

// "natural" → "Natural", "big-tits-anal" → "Big Tits Anal".
function humanizeSlug(s) {
  if (!s) return '';
  return s.replace(/[-_]+/g, ' ').replace(/\s+/g, ' ').trim()
    .replace(/\b\w/g, function(c) { return c.toUpperCase(); });
}

// ----- VIDEO ------------------------------------------------------------------

function doVideo(inv, base, q) {
  var uri = (q.uri || '').trim();
  // ?debug=1 — добавит в ответ поле _debug, чтобы видеть где модуль "ломается".
  var debug = q.debug === '1' || q.debug === 'true';
  var dbg = debug ? { steps: [] } : null;

  if (!uri) return out(dbg, 'no uri', { qualitys: {} });
  if (uri.indexOf('http') !== 0) uri = base + (uri.charAt(0) === '/' ? '' : '/') + uri;

  log.info(MODULE_ID + ': video ' + uri);
  if (dbg) dbg.steps.push('rolik_url=' + uri);

  // Шаг 1 — rolik-страница: достаём URL upstream-плеера из iframe.
  var resp = http.get(uri, { headers: defaultHeaders(inv, base) });
  if (dbg) dbg.steps.push('rolik_status=' + resp.status + ' size=' + (resp.text ? resp.text.length : 0));
  if (!resp.ok) { log.warn(MODULE_ID + ': rolik http ' + resp.status); return out(dbg, 'rolik http', { qualitys: {} }); }

  var embedUrl = extractEmbed(resp.text || '');
  if (!embedUrl) {
    log.warn(MODULE_ID + ': embed URL not found in ' + uri);
    if (dbg) dbg.steps.push('embed_extract=null');
    return out(dbg, 'no embed iframe', { qualitys: {} });
  }
  var provider = detectProvider(embedUrl);
  log.info(MODULE_ID + ': embed ' + provider + ' ' + embedUrl);
  if (dbg) { dbg.steps.push('provider=' + provider); dbg.steps.push('embed_url=' + embedUrl); }

  // Шаг 2 — фетчим upstream embed. iceporn/mylust/pornhub требуют рендера JS-плеера
  // (signed URL генерится на клиенте) и/или обхода Cloudflare → через FlareSolverr.
  // xvideos отдаёт данные сразу в HTML — обычный HTTP-клиент быстрее.
  var useFS = (provider === 'iceporn' || provider === 'mylust' || provider === 'pornhub');
  var embedOpts = {
    headers: {
      'Referer': base + '/',
      'Accept-Language': 'en-US,en;q=0.9',
      'User-Agent': defaultHeaders(inv, base)['User-Agent']
    }
  };
  if (useFS) {
    embedOpts.transport = 'flaresolverr';
    embedOpts.flareNoProxy = true; // FS-контейнер дёргает напрямую, без SOCKS5
    embedOpts.timeout = 45;        // дольше — Chrome грузит JS
  }
  var embedResp = http.get(embedUrl, embedOpts);
  // Heuristic: размер iceporn-embed без JS-рендера ≈ 8KB. Если useFS просили,
  // но получили <12KB — FS, скорее всего, не настроен и запрос ушёл по обычному HTTP.
  var fsLikelyMissed = useFS && embedResp.ok && embedResp.text && embedResp.text.length < 12000;
  var fsLabel = useFS ? (fsLikelyMissed ? ' (flaresolverr REQUESTED but appears to have fallen through — check config.toml [online] flaresolverr URL)' : ' (flaresolverr)') : '';
  if (dbg) dbg.steps.push('embed_status=' + embedResp.status + ' size=' + (embedResp.text ? embedResp.text.length : 0) + fsLabel);
  if (!embedResp.ok) {
    var realErr = embedResp.error || ('http ' + embedResp.status);
    log.warn(MODULE_ID + ': embed fetch failed: ' + realErr);
    if (dbg) dbg.steps.push('embed_error=' + realErr);
    return out(dbg, useFS ? 'flaresolverr error: ' + realErr : 'embed http: ' + realErr, { qualitys: {} });
  }

  var embedHtml = embedResp.text || '';

  // Pornhub отдаёт ~4КБ страницу-заглушку для удалённых видео — детектим по размеру.
  if (provider === 'pornhub' && embedHtml.length < 8000 && /unavailable|This video/i.test(embedHtml)) {
    log.warn(MODULE_ID + ': pornhub video unavailable (removed)');
    return out(dbg, 'pornhub video removed', { qualitys: {} });
  }

  // Шаг 3 — провайдер-специфичный экстрактор.
  var qs = {}, refOrigin = '';
  if (provider === 'xvideos') {
    qs = extractXvideosQualities(embedHtml);
    refOrigin = 'https://www.xvideos.com/';
  } else if (provider === 'pornhub') {
    qs = extractPornhubQualities(embedHtml);
    refOrigin = 'https://www.pornhub.com/';
  } else if (provider === 'iceporn') {
    qs = extractGenericPlayerQualities(embedHtml);
    refOrigin = 'https://www.iceporn.com/';
  } else if (provider === 'mylust') {
    qs = extractGenericPlayerQualities(embedHtml);
    refOrigin = 'https://mylust.com/';
  } else {
    // Generic fallback: попробуем дёрнуть голый mp4/m3u8 из любого embed.
    var bare = /(https?:\/\/[^\s"'<>]+\.(?:m3u8|mp4)(?:\?[^\s"'<>]*)?)/i.exec(embedHtml);
    if (bare) { qs.auto = bare[1]; refOrigin = providerOrigin(provider, embedUrl); }
  }

  if (dbg) dbg.steps.push('qualities=' + JSON.stringify(Object.keys(qs)));
  if (!hasKeys(qs)) {
    log.warn(MODULE_ID + ': no streams extracted from ' + provider);
    return out(dbg, 'unsupported provider: ' + provider, { qualitys: {} });
  }

  // CDN большинства tube-сайтов требует Referer/Origin своего домена. Браузер
  // Lampa их не пошлёт, поэтому СРАЗУ кладём в qualitys прокси-URL — Lampa
  // берёт первый отсюда как основной источник; injectDrochubProxyURLs продублирует
  // в qualitys_proxy как fallback url_reserve.
  var headers = refOrigin
    ? { 'Referer': refOrigin, 'Origin': refOrigin.replace(/\/$/, '') }
    : {};
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

// extractEmbed — общая функция: ищет любой iframe-redirect или прямой src
// в iframe (у botinok всегда javascript:window.location.replace, но fallback
// оставляем для устойчивости).
function extractEmbed(html) {
  var m = /window\.location\.replace\(\s*['"](https?:\/\/[^'"]+)['"]/i.exec(html);
  if (m) return m[1];
  m = /<iframe[^>]+id="extvidx"[^>]+src=["'](https?:\/\/[^"']+)["']/i.exec(html);
  if (m) return m[1];
  m = /<iframe[^>]+src=["'](https?:\/\/[^"']+\/(?:embed(?:frame)?|player)\/[^"']+)["']/i.exec(html);
  return m ? m[1] : '';
}

function detectProvider(url) {
  var hm = /^https?:\/\/(?:www\.)?([a-z0-9.-]+)/i.exec(url);
  if (!hm) return 'unknown';
  var host = hm[1].toLowerCase();
  if (host.indexOf('xvideos.com') >= 0)  return 'xvideos';
  if (host.indexOf('pornhub.com') >= 0)  return 'pornhub';
  if (host.indexOf('xnxx.com') >= 0)     return 'xnxx';
  if (host.indexOf('iceporn.com') >= 0)  return 'iceporn';
  if (host.indexOf('mylust.com') >= 0)   return 'mylust';
  if (host.indexOf('spankbang.com') >= 0) return 'spankbang';
  if (host.indexOf('xhamster.') >= 0)    return 'xhamster';
  return host;
}

function providerOrigin(provider, url) {
  var hm = /^(https?:\/\/[^\/]+)/i.exec(url);
  return hm ? hm[1] + '/' : '';
}

// xvideos: setVideoUrlLow=240p, setVideoUrlHigh=360p, setVideoHLS=full.
function extractXvideosQualities(html) {
  var qs = {}, m;
  var mp4Patterns = [
    /html5player\.setVideoUrlLow\(\s*['"]([^'"]+\.mp4[^'"]*)['"]/i,
    /html5player\.setVideoUrlHigh\(\s*['"]([^'"]+\.mp4[^'"]*)['"]/i
  ];
  for (var i = 0; i < mp4Patterns.length; i++) {
    m = mp4Patterns[i].exec(html);
    if (!m) continue;
    var url = m[1];
    var rm = /video_(\d{3,4})p\.mp4/i.exec(url) || /_(\d{3,4})p[\.?]/i.exec(url);
    qs[rm ? rm[1] + 'p' : (i === 0 ? '240p' : '360p')] = url;
  }
  m = /html5player\.setVideoHLS\(\s*['"]([^'"]+\.m3u8[^'"]*)['"]/i.exec(html);
  if (m) qs.auto = m[1];
  return qs;
}

// extractGenericPlayerQualities — для FS-рендеренного embed-а (iceporn/mylust и
// прочие сайты с JS-плеером). После того как Chrome выполнил скрипты, в HTML
// уже стоят <video>/<source> теги или JSON-конфиги плееров с прямыми ссылками.
// Пробуем по убыванию специфичности.
function extractGenericPlayerQualities(html) {
  var qs = {}, m;

  // Многие сайты используют HTML entities (&amp;) в атрибутах URL — раскодируем.
  function decodeURL(u) { return u.replace(/&amp;/g, '&'); }

  // 1a) <source src="...mp4|m3u8" ... data-quality="lq|hq|720p" / label / title>
  var re1 = /<source[^>]+src=["']([^"']+\.(?:mp4|m3u8)[^"']*)["'][^>]*?(?:label|data-quality|data-type|res|title)=["']([^"']*)["']/gi;
  while ((m = re1.exec(html)) !== null) {
    qs[mapQualityLabel(m[2])] = decodeURL(m[1]);
  }
  if (hasKeys(qs)) return qs;

  // 1b) <source src="..."> без явного label — кладём как auto.
  var re1b = /<source[^>]+src=["']([^"']+\.(?:mp4|m3u8)[^"']*)["']/gi;
  while ((m = re1b.exec(html)) !== null) {
    qs.auto = decodeURL(m[1]);
    break;
  }
  if (hasKeys(qs)) return qs;

  // 2) <video src="..."> прямой src — отсекаем preview-thumbs (jpg/webp).
  m = /<video[^>]+src=["']([^"']+\.(?:mp4|m3u8)[^"']*)["']/i.exec(html);
  if (m && !/\/preview\//i.test(m[1])) qs.auto = decodeURL(m[1]);
  if (hasKeys(qs)) return qs;

  // 3) JWPlayer / generic player setup: file:"..." или sources:[{file:"..."}]
  var re3 = /["']file["']\s*:\s*["']([^"']+\.(?:mp4|m3u8)[^"']*)["']/gi;
  while ((m = re3.exec(html)) !== null) {
    qs.auto = m[1].replace(/\\\//g, '/');
    if (hasKeys(qs)) break;
  }
  if (hasKeys(qs)) return qs;

  // 4) PlayerJS multi-quality: file:"[480p]url1.mp4,[720p]url2.mp4"
  m = /["']file["']\s*:\s*["']((?:\[\d+p?\][^,"']+,?)+)["']/i.exec(html);
  if (m) {
    m[1].split(',').forEach(function(part) {
      var pp = /^\[(\d+p?)\](.+)$/.exec(part);
      if (pp) qs[normalizeQ(pp[1])] = pp[2].replace(/\\\//g, '/');
    });
    if (hasKeys(qs)) return qs;
  }

  // 5) Raw m3u8/mp4 в HTML (после JS-инжекта).
  var bare = /(https?:\/\/[^\s"'<>]+\.(?:m3u8|mp4)(?:\?[^\s"'<>]*)?)/i.exec(html);
  if (bare) qs.auto = bare[1];
  return qs;
}

function normalizeQ(s) {
  s = (s || '').trim().toLowerCase(); if (!s) return '';
  var m = /(\d{3,4})/.exec(s); return m ? m[1] + 'p' : s;
}

// mapQualityLabel переводит сайт-специфичные обозначения качества в стандартные.
// iceporn: lq=320p, hq=720p, 4k=2160p. Прочее — пропускаем через normalizeQ.
function mapQualityLabel(s) {
  s = (s || '').trim().toLowerCase();
  if (!s) return 'auto';
  if (s === 'lq') return '320p';
  if (s === 'hq') return '720p';
  if (s === '4k') return '2160p';
  if (s === 'sd') return '480p';
  if (s === 'hd') return '720p';
  if (s === 'fhd' || s === 'fullhd') return '1080p';
  return normalizeQ(s) || 'auto';
}

// pornhub: после рендера через FlareSolverr в HTML лежит mediaDefinitions массив
// с записями {format, quality, videoUrl}. videoUrl — JSON-escaped (\/) HLS или mp4.
// Параллельно может встретиться запись с videoUrl=https://www.pornhub.com/video/get_media?s=...
// — это API-эндпоинт для повторного запроса, не прямой стрим, его пропускаем.
//
// Старый формат flashvars_VIDEOID={...} (до 2026) больше не используется — оставляем
// fallback на случай если PH вернёт старый layout.
function extractPornhubQualities(html) {
  var qs = {}, m;

  // 1) Новый формат: ищем все "videoUrl":"..." непосредственно. HLS master.m3u8 →
  // под ключом "auto" (приоритет — даже если в имени файла встречается _480P_).
  // Иначе качество берём из URL пути: video_480p.mp4 или _480P_2000K_ → 480p.
  var re = /"videoUrl"\s*:\s*"([^"]+)"/g;
  while ((m = re.exec(html)) !== null) {
    var url = m[1].replace(/\\\//g, '/');
    if (!url || url.indexOf('/get_media') >= 0) continue; // API-эндпоинт, не стрим
    if (/master\.m3u8/i.test(url) || /\.m3u8/i.test(url)) {
      qs.auto = url;
      continue;
    }
    var qm = /_(\d{3,4})P_/i.exec(url) || /\/(\d{3,4})p\//i.exec(url) || /(\d{3,4})p\.mp4/i.exec(url);
    qs[qm ? qm[1] + 'p' : 'auto'] = url;
  }
  if (hasKeys(qs)) return qs;

  // 2) Старый формат (flashvars_*).
  var fv = /flashvars_\d+\s*=\s*(\{[\s\S]*?\});/i.exec(html);
  if (fv) {
    try {
      var data = JSON.parse(fv[1]);
      var mds = (data && data.mediaDefinitions) || [];
      mds.forEach(function(md){
        if (!md || !md.videoUrl) return;
        var url = String(md.videoUrl).replace(/\\\//g, '/');
        if (!url || url.indexOf('/get_media') >= 0) return;
        if (md.format === 'hls' || /\.m3u8/i.test(url)) qs.auto = url;
        else if (md.quality) qs[String(md.quality) + 'p'] = url;
      });
    } catch (e) { /* fall through */ }
  }
  if (hasKeys(qs)) return qs;

  // 3) Последний шанс — голый m3u8 в HTML.
  var bare = /(https?:\/\/[^\s"'<>]+\.(?:m3u8|mp4)(?:\?[^\s"'<>]*)?)/i.exec(html);
  if (bare) qs.auto = bare[1];
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
