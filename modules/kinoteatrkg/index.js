// kinoteatrkg — kinoteatr.kg → Lampa balancer (фильмы, прямой mp4).
//
// Flow:
//   1. search: GET /search?q=<title> → карточки:
//        <a href="/site/view?id={N}"><img src="/uploads/{translit}. {YEAR}.jpg"></a>
//        <h2 class="card-title">{RU} 60 FPS/{EN}</h2>
//   2. view:   GET /site/view?id={N} → <source src="/video1/Filmu/...mp4"> (фильм;
//              /video1/Trailer/... — трейлер, пропускаем).
// Без обфускации. mp4 не требует Referer. Сайт часто гео/DNS-недоступен —
// поток проксируем через сервер. Как в tevas/awmzone: config `socks_proxy`
// пробрасывается в opts.transport='socks5'/opts.proxy на каждый http.* (search/
// view идут через него). Для mp4-стрима — серверный [[proxy.direct.entries]]
// balancers=["kinoteatrkg"] с тем же прокси. Пусто → прямое соединение.

var DEFAULTS = {
  host: 'https://kinoteatr.kg',
  socks_proxy: '',
  proxyStreams: true,
  cacheTTL: 3600
};
function cfg(inv, k){
  var v = inv.config && inv.config[k];
  return (v === undefined || v === null || v === '') ? DEFAULTS[k] : v;
}
var UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36';

function trim(s){ return util.trim(String(s == null ? '' : s)); }
function siteHost(inv){ return trim(cfg(inv, 'host')).replace(/\/+$/, '') || DEFAULTS.host; }

// Собрать opts для http.* с SOCKS5 (если задан в конфиге). Как в tevas/awmzone:
// непустой socks_proxy → transport:'socks5'+proxy, иначе прямое соединение.
function reqOpts(inv, headers){
  var o = { headers: headers };
  var socks = String(cfg(inv, 'socks_proxy') || '').trim();
  if (socks){ o.transport = 'socks5'; o.proxy = socks; }
  return o;
}

function httpGet(inv, url){
  var res = http.get(url, reqOpts(inv, {
    'User-Agent': UA,
    'Accept': 'text/html,application/xhtml+xml',
    'Referer': siteHost(inv) + '/'
  }));
  return (res && res.ok) ? res.text : '';
}

function unescapeHTML(s){
  return String(s == null ? '' : s)
    .replace(/&amp;/g, '&').replace(/&quot;/g, '"')
    .replace(/&#0?39;/g, "'").replace(/&apos;/g, "'")
    .replace(/&laquo;/g, '«').replace(/&raquo;/g, '»')
    .replace(/&lt;/g, '<').replace(/&gt;/g, '>');
}

// Убрать маркеры качества из названия ("Чужой 3 60 FPS" → "Чужой 3").
function stripQuality(s){
  return trim(String(s).replace(/\b(60\s*FPS|50\s*FPS|4K|2K|UHD|FHD|HD|1080p?|720p?)\b/ig, '').replace(/\s+/g, ' '));
}

// "Чужой 3 60 FPS/Alien 3" → { ru:"Чужой 3", en:"Alien 3" }
function splitTitle(s){
  s = unescapeHTML(s);
  var i = s.indexOf('/');
  if (i < 0) return { ru: stripQuality(s), en: '' };
  return { ru: stripQuality(s.substring(0, i)), en: stripQuality(s.substring(i + 1)) };
}

// /search?q= → [{id, ru, en, year}]
function search(inv, q){
  if (!q) return [];
  var html = httpGet(inv, siteHost(inv) + '/search?q=' + util.urlencode(q));
  if (!html) return [];
  var out = [];
  var cards = html.split('card col-lg-12');
  for (var i = 1; i < cards.length; i++){
    var c = cards[i];
    var idm = /\/site\/view\?id=(\d+)/.exec(c);
    var tm = /<h2[^>]*card-title[^>]*>([^<]+)<\/h2>/.exec(c);
    if (!idm || !tm) continue;
    var pm = /\/uploads\/[^"]*?(\d{4})\.[a-z0-9]+/i.exec(c);
    var st = splitTitle(tm[1]);
    out.push({ id: idm[1], ru: st.ru, en: st.en, year: pm ? parseInt(pm[1], 10) : 0 });
  }
  return out;
}

// Найти id по точному совпадению названия (ru/en), при наличии — уточнить годом.
function findId(inv, title, orig, year){
  var ck = 'kt:id:' + title + '|' + orig + '|' + year;
  var hit = cache.get(ck);
  if (hit !== undefined) return hit;

  var wantT = util.normalizeTitle(title);
  var wantO = util.normalizeTitle(orig);
  var queries = [];
  if (title) queries.push(title);
  if (orig && orig !== title) queries.push(orig);

  var fallback = '';
  var picked = '';
  for (var qi = 0; qi < queries.length && !picked; qi++){
    var results = search(inv, queries[qi]);
    for (var i = 0; i < results.length; i++){
      var nr = util.normalizeTitle(results[i].ru);
      var ne = util.normalizeTitle(results[i].en);
      var hitTitle = (wantT && (nr === wantT || ne === wantT)) ||
                     (wantO && (nr === wantO || ne === wantO));
      if (!hitTitle) continue;
      if (year && results[i].year && results[i].year === year){ picked = results[i].id; break; }
      if (!fallback) fallback = results[i].id;
    }
  }
  var id = picked || fallback;
  cache.set(ck, id, cfg(inv, 'cacheTTL'));
  return id;
}

// /site/view?id= → прямой mp4 (Filmu, не Trailer).
function fetchMp4(inv, id){
  var ck = 'kt:mp4:' + id;
  var hit = cache.get(ck);
  if (hit !== undefined) return hit;

  var html = httpGet(inv, siteHost(inv) + '/site/view?id=' + util.urlencode(id));
  var mp4 = '';
  if (html){
    var re = /<source[^>]*src="([^"]+\.mp4)"/ig, m, trailer = '';
    while ((m = re.exec(html)) !== null){
      var src = m[1];
      if (/\/Trailer\//i.test(src)){ if (!trailer) trailer = src; continue; }
      mp4 = src; break; // первый не-трейлер = фильм
    }
    if (!mp4) mp4 = trailer; // на крайний случай
  }
  cache.set(ck, mp4, cfg(inv, 'cacheTTL'));
  return mp4;
}

function absURL(inv, src){
  if (!src) return '';
  var abs = (src.indexOf('http') === 0) ? src : (siteHost(inv) + (src.charAt(0) === '/' ? '' : '/') + src);
  return abs.replace(/ /g, '%20'); // в путях есть пробелы
}

function maybeProxy(inv, u){
  if (!u) return '';
  return cfg(inv, 'proxyStreams') ? proxy.url(u, 'kinoteatrkg') : u;
}

function handleChecksearch(inv){
  var title = trim(inv.query.title);
  var orig = trim(inv.query.original_title);
  if (!title && !orig) return { rch: false };
  var year = parseInt(inv.query.year || 0, 10) || 0;
  var id = findId(inv, title, orig, year);
  if (!id) return { rch: false };
  return { rch: true, type: 'movie', quality: manifest.quality || '1080p' };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);

  var title = trim(inv.query.title);
  var orig = trim(inv.query.original_title);
  var year = parseInt(inv.query.year || 0, 10) || 0;

  var id = findId(inv, title, orig, year);
  if (!id) return { type: 'movie', data: [] };

  var mp4 = fetchMp4(inv, id);
  if (!mp4) return { type: 'movie', data: [] };

  var u = maybeProxy(inv, absURL(inv, mp4));
  if (!u) return { type: 'movie', data: [] };

  var base = [title, orig].filter(function(x){ return x; }).join(' / ');
  return { type: 'movie', data: [{ method: 'play', url: u, stream: u, name: base || 'Фильм', title: base }] };
}

module.exports = { handle: handle };
