// ТЕВАС (pult.tevas.dev) — community-балансер для MP4-кинотеатра.
//
// Сайт отдаёт страницы фильмов и серий с инициализацией Playerjs вида:
//   Playerjs({ file: "//{v3}/kino/download/all/<File>.mp4 or //{v4}/... or //{v5}/..." })
// Плейсхолдеры {vN} — анти-блок-механика: реальные CDN-хосты выставляет
// обфусцированный JS на клиенте (player_tevas_stawru18.js плюс мутирующий
// загрузчик от vak345.com). Программно вытащить их не получилось — функция
// fplace() в плеере ожидает поля v.fpv1..fpv5, которые нигде не выставляются,
// а реальные хосты вроде "bigsgppgs.tevas.dev" / "bigjjxjjs.tevas.dev" живут
// в обфусцированных секциях плеера и ротируются раз в дни/недели.
//
// Прагматичный путь:
//   1) тянем HTML страницы фильма/серии (она стабильна, путь к файлу — там);
//   2) regex'ом достаём первое вхождение «//{vN}/<path>.mp4» — это и есть
//      относительный путь, который ровно повторяется во всех трёх или двух
//      ветках через «or»;
//   3) подставляем CDN-хост из config (admin задаёт, видно из DevTools);
//   4) отдаём прямой URL клиенту. CDN открыт миру: Range, поддерживает
//      произвольный UA / без Referer'а, CORS *.
//
// Сериалы. Поиск /search/search.php?q=… покрывает только фильмы.
// Каталог сериалов — DOM /serial/, HTML-список с парой десятков карточек.
// Тянем целиком, индексируем slug→title и кэшируем на N часов.
// Эпизод-страница та же что у фильма по форме, но с префиксом /serial/.

var DEFAULTS = {
  site_url:          'https://pult.tevas.dev',
  mirrors:           'https://pult.tevas.dev,https://tevas.team,https://tevas.tech',
  cdn_movie_host:    'bigsgppgs.tevas.dev',
  cdn_serial_host:   'bigjjxjjs.tevas.dev',
  use_proxy:         true,
  use_flaresolverr:  false,
  socks_proxy:       '',
  cookies:           '',     // cf_clearance + остальные из браузера (см. manifest)
  cookie_user_agent: '',     // UA под которым cookies выданы — иначе CF их отвергнет
  serial_cache_hours: 6,
  referer:           'https://pult.tevas.dev/'
};

// Сайт фильтрует поиск по UA: с macOS Chrome /search/search.php отдаёт «нечего
// показать» даже на легитимные запросы. Linux/X11 UA проходит без проблем.
var UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36';

function cfg(inv, k){
  var v = inv.config && inv.config[k];
  if (v === undefined || v === null || v === '') return DEFAULTS[k];
  return v;
}

// Список зеркал: сначала кэшированное «живое», потом основной site_url,
// потом всё из cfg('mirrors'). Дубликаты пропускаем.
function mirrors(inv){
  var out = [];
  var seen = {};
  function push(u){
    var s = String(u||'').trim().replace(/\/+$/, '');
    if (!s || seen[s]) return;
    seen[s] = true; out.push(s);
  }
  push(cache.get('alive_mirror') || '');
  push(cfg(inv,'site_url'));
  var raw = String(cfg(inv,'mirrors')||'');
  var parts = raw.split(/[,\s]+/);
  for (var i=0;i<parts.length;i++) push(parts[i]);
  return out;
}

function siteHost(inv){
  return cache.get('alive_mirror') || String(cfg(inv,'site_url')).replace(/\/+$/, '');
}

// Признак ad-парковки куда тевас редиректит гео-блокированные IP.
// quickresultseeker / cdn-fileserver — типовые ad-network домены, на которые
// настроена парковка tevas.pro / tevas.team. Если в теле такое — значит мы
// упёрлись в гео-блок и без residential SOCKS5 дальше делать нечего.
function isParkingPage(html){
  if (!html) return false;
  if (html.length > 65536) return false;        // полноценная страница ≫ 65КБ
  if (/quickresultseeker\.com|cdn-fileserver\.com|_ol_one_|_ol_lg_/.test(html)) return true;
  return false;
}

// Делаем запрос с fallback по зеркалам. Per-call timeout по умолчанию 6с
// (FS — 14с т.к. Chrome solver медленнее). checksearch ходит одну попытку.
//
// Логика:
//   • если use_flaresolverr=true → все запросы идут через FS (tevas.tech за
//     Cloudflare Turnstile, без FS — 403);
//   • если socks_proxy непустой → этот SOCKS5 пробрасывается:
//       — в opts.proxy для прямого HTTP (residential обход гео-блока),
//       — в opts.proxy для FS-запроса (FS делает Chrome-сессию из RU IP).
//   • при отсутствии FS и SOCKS5 — обычный HTTP по зеркалам.
//
// path должен начинаться с «/». При успехе запоминаем живое зеркало в cache.
function httpGetPath(inv, path, opts){
  opts = opts || {};
  var firstOnly = opts.firstOnly === true;
  var timeout   = opts.timeout || 6;

  var socks   = String(cfg(inv,'socks_proxy')||'').trim();
  var cookies = String(cfg(inv,'cookies')||'').trim();
  var cookieUA = String(cfg(inv,'cookie_user_agent')||'').trim();
  var useFS   = cfg(inv, 'use_flaresolverr') === true;

  // Если пользователь дал cookies — обязательно идём с тем же UA что
  // выдал клиент (CF привязывает cf_clearance к UA+fingerprint, mismatch =
  // CF её отвергнет и снова покажет challenge). Если cookies без UA —
  // используем дефолтный, но предупреждаем в логе один раз за сессию.
  var ua = UA;
  if (cookies){
    if (cookieUA) ua = cookieUA;
    else if (!cache.get('warned_no_cookie_ua')){
      console.warn('tevas: задан cookies но не задан cookie_user_agent — CF может отвергнуть clearance. Скопируйте UA из браузера откуда брали cookies.');
      cache.set('warned_no_cookie_ua', 1, 600);
    }
  }

  var hosts = mirrors(inv);
  if (firstOnly && hosts.length > 0) hosts = [hosts[0]];

  var sawParking = false;
  var sawChallenge = false;
  for (var i=0;i<hosts.length;i++){
    var url = hosts[i] + path;
    var headers = {
      'User-Agent': ua,
      'Accept':     'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
      'Accept-Language': 'ru-RU,ru;q=0.9,en;q=0.8',
      'Referer':    hosts[i] + '/'
    };
    if (cookies) headers['Cookie'] = cookies;

    var reqOpts = {
      timeout: useFS ? Math.max(timeout, 14) : timeout,
      headers: headers
    };
    if (useFS){
      reqOpts.transport = 'flaresolverr';
      if (socks) reqOpts.proxy = socks;
    } else if (socks){
      reqOpts.transport = 'socks5';
      reqOpts.proxy     = socks;
    }

    var res = http.get(url, reqOpts);
    if (res && res.ok && res.text){
      // Гео-блок: сайт отдал 200 но это ad-парковка.
      if (isParkingPage(res.text)){
        sawParking = true;
        console.warn('tevas: GET ' + url + ' → ad parking (geo-block) dur=' + res.duration_ms + 'ms');
        continue;
      }
      // CF challenge HTML — FlareSolverr вернёт сам challenge-page если не смог.
      if (/cf-mitigated|Just a moment|challenge-platform/i.test(res.text.substring(0, 4096))){
        sawChallenge = true;
        console.warn('tevas: GET ' + url + ' → CF challenge не пробит, dur=' + res.duration_ms + 'ms');
        continue;
      }
      cache.set('alive_mirror', hosts[i], 3600);
      return String(res.text);
    }
    var lastErr = (res && (res.error || ('status='+res.status))) || 'no-response';
    console.warn('tevas: GET ' + url + ' fail: ' + lastErr +
                 ' dur=' + (res && res.duration_ms) + 'ms');
  }

  cache.set('alive_mirror', '', 1);

  if (sawParking && !socks){
    console.error('tevas: все зеркала вернули ad-парковку — заполните socks_proxy в настройках модуля.');
  }
  if (sawChallenge && !useFS){
    console.error('tevas: все зеркала отдали Cloudflare challenge — включите use_flaresolverr в настройках модуля и убедитесь что FlareSolverr URL прописан в admin (Online → FlareSolverr).');
  }
  return '';
}

// Совместимый shortcut — большинство хелперов отдают абсолютный URL.
// Приводим к path и зовём httpGetPath.
function httpGet(inv, url, opts){
  var rel = String(url||'');
  var hm = /^https?:\/\/[^/]+(\/.*)?$/i.exec(rel);
  if (hm) rel = hm[1] || '/';
  if (rel.charAt(0) !== '/') rel = '/' + rel;
  return httpGetPath(inv, rel, opts);
}

// ─── Нормализация и сравнение названий ──────────────────────────────────────

// Транслитерация кириллицы в латиницу по схеме сайта: ТЕВАС именует файлы
// латинскими ASCII-словами через _ (см. Matrica, Klyk_2_gorod_volkov_2025,
// Otryad_samoubijc_missiya_navylet_TEVAS). Используем для score-сравнения.
var TRANSLIT = {
  'а':'a','б':'b','в':'v','г':'g','д':'d','е':'e','ё':'e','ж':'zh','з':'z',
  'и':'i','й':'j','к':'k','л':'l','м':'m','н':'n','о':'o','п':'p','р':'r',
  'с':'s','т':'t','у':'u','ф':'f','х':'h','ц':'c','ч':'ch','ш':'sh','щ':'sch',
  'ъ':'',  'ы':'y','ь':'',  'э':'e','ю':'yu','я':'ya'
};
function translit(s){
  s = String(s||'').toLowerCase();
  var out = '';
  for (var i=0;i<s.length;i++){
    var ch = s.charAt(i);
    out += (TRANSLIT[ch] !== undefined) ? TRANSLIT[ch] : ch;
  }
  return out;
}

function normalize(s){
  return String(s||'')
    .toLowerCase()
    .replace(/ё/g, 'е')
    .replace(/[^a-zа-я0-9 ]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

// Имя файла → канон: «Matrica_perezagruzka_2003_TEVAS.mp4» → «matrica perezagruzka»
function fileToKey(name){
  return String(name||'')
    .replace(/\.mp4$/i, '')
    .replace(/_+TEVAS_*/gi, '_')
    .replace(/_+\d{4}_*/g, '_')   // отрежем «_2024», «_2025», встроенный год
    .replace(/_+/g, ' ')
    .toLowerCase()
    .replace(/[^a-z0-9 ]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function extractYear(name){
  var m = /_(\d{4})(?:[_.]|$)/.exec(String(name||''));
  if (!m) return 0;
  var y = parseInt(m[1], 10);
  return (y >= 1900 && y <= 2100) ? y : 0;
}

// ─── Поиск фильма ───────────────────────────────────────────────────────────

function searchMovies(inv, query, opts){
  if (!query) return [];
  var url = siteHost(inv) + '/search/search.php?q=' + encodeURIComponent(query);
  var html = httpGet(inv, url, opts);
  if (!html) return [];
  var rows = [];
  // <a href="/kino/download/?f=Matrica.mp4&sea=…&big=194">
  //   <div class="v vi p"><img src="/kino/stuff/Matrica.jpg"><span>Matrica</span>
  var re = /href="(\/kino\/download\/\?f=([^"&]+)\.mp4[^"]*)"[\s\S]*?<img[^>]+src="([^"]+)"[\s\S]*?<span>([^<]+)<\/span>/g;
  var m;
  while ((m = re.exec(html)) !== null){
    var href   = m[1].replace(/&amp;/g, '&');
    var file   = m[2] + '.mp4';
    var poster = m[3];
    var label  = m[4].trim();
    rows.push({
      href:   href,
      file:   file,
      poster: poster,
      label:  label,
      year:   extractYear(file)
    });
  }
  return rows;
}

// Выбор лучшего результата. Стратегия: пробуем оба варианта (title + original_title)
// и латинскую транслитерацию русского. Если запрошен year и в имени файла он есть —
// предпочитаем точное совпадение года.
function pickMovie(inv, hits, wantTitle, wantOrig, wantYear){
  if (!hits.length) return null;
  var wantTrans = translit(wantTitle);
  var wantNorm  = normalize(wantOrig || wantTitle);
  var best = null, bestScore = -1;
  for (var i=0;i<hits.length;i++){
    var h = hits[i];
    var fileKey  = fileToKey(h.file);
    var labelKey = normalize(h.label);
    var score = 0;
    if (wantNorm && labelKey === wantNorm) score += 6;
    else if (wantNorm && labelKey.indexOf(wantNorm) >= 0) score += 3;
    if (wantTrans){
      if (fileKey === normalize(wantTrans)) score += 5;
      else if (fileKey.indexOf(normalize(wantTrans)) >= 0) score += 2;
    }
    if (wantYear){
      if (h.year === wantYear) score += 4;
      else if (h.year && Math.abs(h.year - wantYear) <= 1) score += 1;
    }
    if (score > bestScore){ bestScore = score; best = h; }
  }
  return (bestScore > 0) ? best : null;
}

// Парсим страницу фильма: достаём путь видео из Playerjs file:"//{vN}/<path>.mp4 …".
// Возвращаем относительный путь без ведущего «/», например
// "kino/download/all/Na_vershine_vershina_TEVAS_2026.mp4".
function extractFilePath(html){
  if (!html) return '';
  // file:"//{v3}/kino/download/all/Na_vershine_vershina_TEVAS_2026.mp4 or //{v4}/...
  var m = /file:"\/\/\{v\d\}\/([^"\s]+\.mp4)(?:\s|"|\\)/i.exec(html);
  return m ? m[1] : '';
}

// ─── Сборка стрима ──────────────────────────────────────────────────────────

// Оборачиваем CDN-URL через /proxy/ lampac-сервера, чтобы навесить нужные
// заголовки на upstream. Проверено на живом CDN:
//   • UA пустой / "curl/8" / стандартный stagefright → 403/timeout
//   • UA "Mozilla/5.0 (Macintosh; …) Chrome/…" без Referer → отказ
//   • UA "Mozilla/5.0 (X11; Linux …) Chrome/…" — проходит даже без Referer
//   • Origin/Referer host не валидируется (example.com — 206 OK)
// Отсюда: ставим Linux Chrome UA + Referer pult.tevas.dev. Этого достаточно
// для любого клиента, который пойдёт через /proxy/.
function wrapStream(inv, url){
  if (!url) return '';
  if (cfg(inv, 'use_proxy') !== true) return url;
  try {
    var streamUA = String(cfg(inv,'cookie_user_agent')||'').trim() || UA;
    var headers = {
      'User-Agent': streamUA,
      'Referer':    cfg(inv,'referer')
    };
    var pu = proxy.urlWithHeaders ? proxy.urlWithHeaders(url, 'tevas', headers)
                                  : proxy.url(url, 'tevas');
    return pu || url;
  } catch (e) {
    return url;
  }
}

function buildMovieUrl(inv, relPath){
  if (!relPath) return '';
  var host = String(cfg(inv,'cdn_movie_host')).replace(/^https?:\/\//,'').replace(/\/+$/,'');
  return wrapStream(inv, 'https://' + host + '/' + relPath);
}

function buildSerialUrl(inv, relPath){
  if (!relPath) return '';
  var host = String(cfg(inv,'cdn_serial_host')).replace(/^https?:\/\//,'').replace(/\/+$/,'');
  return wrapStream(inv, 'https://' + host + '/' + relPath);
}

// ТЕВАС — прямой mp4 одного качества. ВАЖНО: Lampa ждёт quality/qualitys как
// ОБЪЕКТ { "1080p": url }, и итерирует его (for q in quality; parseInt(q)).
// Если положить строку '1080p' — клиент перебирает её ПО СИМВОЛАМ и показывает
// «цифры» вместо качества. Поэтому всегда отдаём карту с одной записью.
var QUALITY_LABEL = '1080p';
function qualityMap(stream){
  var q = {};
  q[QUALITY_LABEL] = stream;
  return q;
}

// ─── Каталог сериалов ───────────────────────────────────────────────────────
//
// /serial/ — HTML-страница с фиксированным набором карточек:
//   <a href="izvne/"><div class="v vi p"><img src="stuff/izvne4.jpg"><span>Извне</span></div></a>
// или с абсолютным URL и quirk-параметрами:
//   <a href="https://tevas.dev/serial/uroven_ugrozy/?sea=…&big=195">
// Иногда slug включает префикс "top/" или "all/" — нужно сохранить как есть.

function fetchSerialIndex(inv, opts){
  var ttl = parseInt(cfg(inv,'serial_cache_hours'), 10);
  if (isNaN(ttl) || ttl < 0) ttl = 6;
  var idx = cache.get('serial_idx');
  if (idx && idx.items && idx.items.length) return idx;

  // /serial/ — 280КБ, на холодном кэше тянется ~1с по IPv6 (быстро),
  // но при выборе зеркала через timeout=6с легко уложится в бюджет.
  // checksearch вызовы получают сюда opts.firstOnly=true.
  var html = httpGet(inv, siteHost(inv) + '/serial/', opts);
  if (!html) return null;
  // Берём блок #series, иначе всю страницу
  var blockM = /<div\s+id="series">([\s\S]*?)<div\s+class="clear">/i.exec(html);
  var src = blockM ? blockM[1] : html;

  var items = [];
  var seen = {};
  // <a href="<href>"><div class="v vi p"><img …><span>NAME</span></div></a>
  var re = /<a\s+href="([^"#]+)"[^>]*>\s*<div\s+class="v\s+vi\s+p">\s*<img[^>]*src="([^"]*)"[^>]*>\s*<span>([^<]+)<\/span>/g;
  var m;
  while ((m = re.exec(src)) !== null){
    var href   = m[1].trim();
    var poster = m[2];
    var title  = String(m[3]).trim();
    // Из href вытаскиваем slug: всё, что после /serial/ до query/fragment.
    var slugRel = href;
    var hostStrip = /https?:\/\/[^/]+(\/.+)/i.exec(href);
    if (hostStrip) slugRel = hostStrip[1];
    var slugM = /^\/?serial\/([^?#]+?)\/?(?:\?|$|#)/i.exec(slugRel);
    if (!slugM){
      // относительный href от /serial/, например "izvne/" или "all/zaklyuchennyj/"
      slugM = /^([^?#]+?)\/?(?:\?|$|#)/i.exec(slugRel);
    }
    var slug = slugM ? slugM[1].replace(/\/+$/,'') : '';
    if (!slug) continue;
    if (seen[slug]) continue;
    seen[slug] = true;
    items.push({ slug: slug, title: title, poster: poster, key: normalize(title) });
  }

  idx = { items: items, builtAt: Date.now() };
  if (ttl > 0) cache.set('serial_idx', idx, ttl*3600);
  console.log('tevas: indexed serials='+items.length);
  return idx;
}

function pickSerial(idx, wantTitle, wantOrig){
  if (!idx || !idx.items.length) return null;
  var keys = [];
  if (wantTitle) keys.push(normalize(wantTitle));
  if (wantOrig && wantOrig !== wantTitle) keys.push(normalize(wantOrig));
  // Точные совпадения первыми
  for (var k=0;k<keys.length;k++){
    for (var i=0;i<idx.items.length;i++){
      if (idx.items[i].key === keys[k]) return idx.items[i];
    }
  }
  // Подстроки
  for (k=0;k<keys.length;k++){
    if (keys[k].length < 3) continue;
    for (i=0;i<idx.items.length;i++){
      if (idx.items[i].key.indexOf(keys[k]) >= 0 || keys[k].indexOf(idx.items[i].key) >= 0){
        return idx.items[i];
      }
    }
  }
  return null;
}

// ─── Сезоны и эпизоды ───────────────────────────────────────────────────────

// /serial/<slug>/ → парсим ссылки на сезоны вида «01/?big=195», «04/?big=195»,
// «05_dms/?big=195» (DMS = доп. монтаж). Возвращаем массив { season:1, dir:'01' }.
function fetchSeasons(inv, slug){
  var ck = 'seasons:'+slug;
  var c = cache.get(ck);
  if (c) return c;
  var html = httpGet(inv, siteHost(inv) + '/serial/' + slug + '/');
  if (!html) return [];
  var seen = {};
  var out  = [];
  // href="04/?big=195" / "05_dms/?big=300" — значение big варьируется по
  // сериалам (194/195/300/…), поэтому ЗАХВАТЫВАЕМ его и переиспользуем при
  // запросе страниц сезона/эпизодов. Раньше было захардкожено 194|195 — из-за
  // чего сериалы с big=300 (напр. «Пацаны»/The Boys) отдавали 0 сезонов.
  var re = /href="(\d{1,2})(?:_([a-z0-9]+))?\/\?big=(\d+)"/gi;
  var m;
  while ((m = re.exec(html)) !== null){
    var s   = parseInt(m[1], 10);
    var tag = (m[2] || '').toLowerCase();
    var dir = m[1] + (tag ? '_'+tag : '');
    var key = s + '|' + tag;
    if (seen[key]) continue;
    seen[key] = true;
    out.push({ season: s, dir: dir, tag: tag, big: m[3] });
  }
  out.sort(function(a,b){
    if (a.season !== b.season) return a.season - b.season;
    if (a.tag && !b.tag) return 1;     // обычная версия раньше «_dms»
    if (!a.tag && b.tag) return -1;
    return 0;
  });
  cache.set(ck, out, 1800);
  return out;
}

// /serial/<slug>/<dir>/ → парсим ссылки на эпизоды:
//   href="?f=tevas_izvne_01_03.mp4&big=195"
// Возвращаем массив { ep, file } в порядке возрастания.
function fetchEpisodes(inv, slug, dir, big){
  big = String(big || '195');
  var ck = 'eps:'+slug+':'+dir+':'+big;
  var c = cache.get(ck);
  if (c) return c;
  // Страница сезона требует ?big=<N> — без query сайт отдаёт 404. Значение big
  // приходит из fetchSeasons (варьируется по сериалам).
  var html = httpGet(inv, siteHost(inv) + '/serial/' + slug + '/' + dir + '/?big=' + big);
  if (!html) return [];
  var seen = {};
  var out = [];
  var re = /href="(?:\.\.?\/[^"]*)?\?f=([^"&]+)\.mp4(?:&[^"]*)?"/g;
  var m;
  while ((m = re.exec(html)) !== null){
    var file = m[1] + '.mp4';
    if (seen[file]) continue;
    seen[file] = true;
    // tevas_izvne_01_03 → ep=3
    var em = /_(\d{1,3})$/.exec(m[1]);
    var ep = em ? parseInt(em[1], 10) : 0;
    out.push({ ep: ep, file: file });
  }
  out.sort(function(a,b){
    if (!a.ep) return 1;
    if (!b.ep) return -1;
    return a.ep - b.ep;
  });
  cache.set(ck, out, 600);
  return out;
}

// На странице конкретного эпизода `?f=<file>` тоже лежит Playerjs file:"//{vN}/<path>" —
// при необходимости (если parseFile-shortcut не подойдёт) дёргаем её,
// но обычно путь предсказуем: serial/<slug>/<dir>/<file>.
function buildEpisodePath(slug, dir, file){
  return 'serial/' + slug + '/' + dir + '/' + file;
}

// ─── Сборка ответов по контракту lampac ─────────────────────────────────────

function handleMovie(inv){
  var q = inv.query || {};
  var title = String(q.title || '').trim();
  var orig  = String(q.original_title || '').trim();
  var year  = parseInt(q.year, 10) || 0;
  var queries = [];
  if (title) queries.push(title);
  if (orig && orig !== title) queries.push(orig);
  if (!queries.length) return null;

  var hit = null, hits = [];
  for (var i=0;i<queries.length && !hit;i++){
    hits = searchMovies(inv, queries[i]);
    hit = pickMovie(inv, hits, title, orig, year);
  }
  if (!hit) return null;

  // Идём на страницу фильма за реальным путём (включая префикс top/all/lat).
  var page = httpGet(inv, siteHost(inv) + hit.href);
  var rel  = extractFilePath(page);
  if (!rel) return null;

  var stream = buildMovieUrl(inv, rel);
  if (!stream) return null;
  var joined = title || orig || hit.label;
  if (orig && title && orig !== title) joined = title + ' / ' + orig;
  var qmap = qualityMap(stream);
  return {
    type: 'movie',
    data: [{
      method:   'play',
      url:      stream,
      stream:   stream,
      name:     'TEVAS',
      title:    joined,
      quality:  qmap,
      qualitys: qmap
    }]
  };
}

function buildSeasonList(host, query, seasons){
  var out = [];
  // ВАЖНО: НЕ хардкодить rjson=true. online.js парсит ответ только как HTML
  // (parseJsonDate ищет .videos__item); при rjson=true сезон отдаёт JSON-эпизоды,
  // которые клиент не разбирает → «нет результатов». Проксируем входящий rjson
  // (как нативный buildSeasonLink): Lampa ходит без rjson → сезоны/серии в HTML.
  var rjson = (query.rjson === 'true' || query.rjson === '1' || query.rjson === true);
  for (var i=0;i<seasons.length;i++){
    var s = seasons[i];
    if (!s.season) continue;
    // Если есть алт-версия «_dms» — добавим отдельной кнопкой
    var name = s.season + ' сезон' + (s.tag ? ' ('+s.tag+')' : '');
    var params = ['serial=1', 's='+s.season];
    if (rjson) params.unshift('rjson=true');
    if (s.tag) params.push('tag='+encodeURIComponent(s.tag));
    if (query.tmdb_id)       params.push('tmdb_id='+encodeURIComponent(query.tmdb_id));
    if (query.imdb_id)       params.push('imdb_id='+encodeURIComponent(query.imdb_id));
    if (query.title)         params.push('title='+encodeURIComponent(query.title));
    if (query.original_title) params.push('original_title='+encodeURIComponent(query.original_title));
    out.push({
      method: 'link',
      id:     s.season,
      url:    host + '/lite/tevas?' + params.join('&'),
      name:   name
    });
  }
  return out;
}

function buildEpisodeList(inv, slug, dirObj, episodes, baseTitle){
  var rows = [];
  var seasonNum = dirObj.season;
  for (var i=0;i<episodes.length;i++){
    var e = episodes[i];
    var rel = buildEpisodePath(slug, dirObj.dir, e.file);
    var stream = buildSerialUrl(inv, rel);
    if (!stream) continue;
    var qmap = qualityMap(stream);
    rows.push({
      method:   'play',
      url:      stream,
      stream:   stream,
      s:        seasonNum,
      e:        e.ep,
      name:     (e.ep ? (e.ep+' серия') : e.file),
      title:    baseTitle + ' / ' + seasonNum + ' сезон ' + (e.ep ? e.ep+' серия' : e.file),
      quality:  qmap,
      qualitys: qmap
    });
  }
  return rows;
}

function handleSerial(inv){
  var q = inv.query || {};
  var title = String(q.title || '').trim();
  var orig  = String(q.original_title || '').trim();
  if (!title && !orig) return null;

  var idx = fetchSerialIndex(inv);
  if (!idx) return null;
  var s = pickSerial(idx, title, orig);
  if (!s) return null;

  var seasons = fetchSeasons(inv, s.slug);
  if (!seasons.length) return null;

  var season = parseInt(q.s, 10) || 0;
  var wantTag = String(q.tag || '').toLowerCase();
  var baseTitle = title || s.title || orig || s.slug;
  if (orig && title && orig !== title) baseTitle = title + ' / ' + orig;

  // Список сезонов
  if (!season){
    return { type: 'season', data: buildSeasonList(inv.host, q, seasons) };
  }
  // Конкретный сезон → эпизоды
  var pick = null;
  for (var i=0;i<seasons.length;i++){
    if (seasons[i].season === season && (wantTag ? seasons[i].tag === wantTag : !seasons[i].tag)){
      pick = seasons[i]; break;
    }
  }
  if (!pick){
    for (i=0;i<seasons.length;i++) if (seasons[i].season === season){ pick = seasons[i]; break; }
  }
  if (!pick) return null;

  var eps = fetchEpisodes(inv, s.slug, pick.dir, pick.big);
  if (!eps.length) return null;

  return { type: 'episode', data: buildEpisodeList(inv, s.slug, pick, eps, baseTitle) };
}

// ─── Точка входа ────────────────────────────────────────────────────────────

function handle(inv){
  var q = inv.query || {};
  var serial = q.serial === '1' || q.serial === 'true' || q.serial === true;

  // checksearch: быстрый ответ rch:true если что-то нашлось.
  // Ходим только в первое (живое) зеркало с timeout=4с — нам нельзя тратить
  // 18с на checksearch, иначе кит долго рисует список балансеров.
  if (inv.checksearch){
    var fastOpts = { firstOnly: true, timeout: 4 };
    try {
      var ok = false;
      if (serial){
        var idx = fetchSerialIndex(inv, fastOpts);
        ok = !!(idx && pickSerial(idx, q.title, q.original_title));
      } else {
        var hits = searchMovies(inv, q.title || q.original_title || '', fastOpts);
        ok = !!pickMovie(inv, hits, q.title, q.original_title, parseInt(q.year,10) || 0);
      }
      return ok ? { rch: true, type: serial?'serial':'movie', quality: 'FHD' } : { rch: false };
    } catch (e){
      return { rch: false, error: String(e) };
    }
  }

  try {
    if (serial) return handleSerial(inv) || {};
    return handleMovie(inv) || {};
  } catch (e){
    console.error('tevas: handler error: ' + e);
    return { error: String(e) };
  }
}

module.exports = { handle: handle };
