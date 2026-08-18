// awmzone — turboserial.com (плеер videodb, CDN awmzone) → Lampa balancer.
//
// Flow:
//   1. search:  GET /mary/spotlight?search=<title> → [{name, link:/watch/{id}~slug}]
//   2. watch:   GET <link> → JSON-LD "embedUrl":".../embed-players/{N}"
//   3. embed:   GET /embed-players/{N} (Referer: watch) → inline
//               new Playerjs(JSON.parse(atob('<b64>'))) → { file, ... }
//   4. file:
//        movie  — string  "#2<obf>"          → {Озвучка}URL;{Озвучка}URL;
//        serial — [{id:"S1E1", title, file:"#2<obf>"}, ...]
//
// PlayerJS "#2" obfuscation: после '#2' — base64, в который вставлены мусорные
// сегменты "//"+base64(слово). Набор слов фиксирован (dvadolboeba/pososikloun/
// bibaiboba), лишь переставляется. Удаляем их, добиваем паддинг, base64-декод.
// CDN awmzone отдаёт мастер-HLS (240..1080) и требует Referer/Origin сайта.
//
// ВАЖНО (доступ к CDN): cdn.awmzone1.pro блокирует датацентр-IP (404 на фильмах,
// 403 на сериалах) — нужен residential CIS-IP. Токен `{hash}:{date}` может быть
// привязан к IP, который запрашивал embed, поэтому И запросы к сайту (search/
// embed), И поток через /proxy/ должны идти через ОДИН и тот же egress.
// Как в tevas: config `socks_proxy` (admin-editable) пробрасывается в
// opts.transport='socks5'/opts.proxy на каждый http.* → search/embed идут через
// residential. Для стрима — серверный [[proxy.direct.entries]] balancers=["awmzone"]
// с тем же прокси. Пусто → прямое соединение (как было).

var DEFAULTS = {
  host: 'https://turboserial.com',
  socks_proxy: '',
  proxyStreams: true,
  cacheTTL: 1200
};
function cfg(inv, k){
  var v = inv.config && inv.config[k];
  return (v === undefined || v === null || v === '') ? DEFAULTS[k] : v;
}
var UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36';

// Собрать opts для http.* с residential SOCKS5 (если задан в конфиге). Как в tevas:
// непустой socks_proxy → transport:'socks5'+proxy, иначе прямое соединение.
function reqOpts(inv, headers){
  var o = { headers: headers };
  var socks = String(cfg(inv, 'socks_proxy') || '').trim();
  if (socks){ o.transport = 'socks5'; o.proxy = socks; }
  return o;
}

// '//'+base64(слово) — мусор, который PlayerJS вырезает перед декодом.
var JUNK = ['//' + btoa('dvadolboeba'), '//' + btoa('pososikloun'), '//' + btoa('bibaiboba')];

function trim(s){ return util.trim(String(s == null ? '' : s)); }

function siteHost(inv){
  return trim(cfg(inv, 'host')).replace(/\/+$/, '') || DEFAULTS.host;
}

function httpGet(inv, url, ref){
  var res = http.get(url, reqOpts(inv, {
    'User-Agent': UA,
    'Accept': 'text/html,application/json,application/xhtml+xml',
    'Referer': ref || (siteHost(inv) + '/')
  }));
  return (res && res.ok) ? res.text : '';
}

function httpJSON(inv, url){
  var res = http.get(url, reqOpts(inv, { 'User-Agent': UA, 'Accept': 'application/json' }));
  if (!res || !res.ok) return null;
  try { return res.json(); } catch (e){ return null; }
}

// Снять "#N" маркер, вырезать мусорные сегменты, поправить паддинг, base64-декод.
function deobfFile(file){
  if (typeof file !== 'string') return '';
  var s = file;
  if (s.charAt(0) === '#'){
    s = s.substring(2); // "#2"
    for (var i = 0; i < JUNK.length; i++){
      s = s.split(JUNK[i]).join('');
    }
    s = s.replace(/=+$/, '');
    while (s.length % 4) s += '=';
    try { return atob(s); } catch (e){ return ''; }
  }
  return s;
}

// "{Озвучка}URL;{Озвучка2}URL2;" → [{name, url}]
function parseVoices(str){
  var out = [];
  if (!str) return out;
  var re = /\{([^}]*)\}\s*([^;{}]+)/g, m;
  while ((m = re.exec(str)) !== null){
    var name = trim(m[1]);
    var url = trim(m[2]);
    if (url && url.indexOf('http') === 0) out.push({ name: name, url: url });
  }
  return out;
}

function maybeProxy(inv, u){
  if (!u) return '';
  if (!cfg(inv, 'proxyStreams')) return u;
  var h = siteHost(inv);
  return proxy.urlWithHeaders(u, 'awmzone', { 'Referer': h + '/', 'Origin': h });
}

function search(inv, query){
  if (!query) return [];
  var url = siteHost(inv) + '/mary/spotlight?search=' + util.urlencode(query);
  var data = httpJSON(inv, url);
  return (Array.isArray(data)) ? data : [];
}

// Найти точное совпадение по названию (ru title или original_title).
function findLink(inv, title, orig){
  var ck = 'awmzone:link:' + title + '|' + orig;
  var hit = cache.get(ck);
  if (hit !== undefined) return hit;

  var wantT = util.normalizeTitle(title);
  var wantO = util.normalizeTitle(orig);
  var link = '';
  var queries = [];
  if (title) queries.push(title);
  if (orig && orig !== title) queries.push(orig);

  for (var qi = 0; qi < queries.length && !link; qi++){
    var results = search(inv, queries[qi]);
    for (var i = 0; i < results.length; i++){
      var n = util.normalizeTitle(results[i].name || '');
      if ((wantT && n === wantT) || (wantO && n === wantO)){
        link = results[i].link || '';
        break;
      }
    }
  }
  cache.set(ck, link, cfg(inv, 'cacheTTL'));
  return link;
}

// watch-страница → embed URL (videodb), затем embed → player file (string|array).
function fetchPlayerFile(inv, link){
  var ck = 'awmzone:pf:' + link;
  var hit = cache.get(ck);
  if (hit !== undefined) return hit;

  var host = siteHost(inv);
  var watchURL = (link.charAt(0) === '/') ? (host + link) : link;
  var watch = httpGet(inv, watchURL, host + '/');
  var result = null;
  if (watch){
    var em = /"embedUrl"\s*:\s*"([^"]+)"/.exec(watch);
    if (em){
      var embedURL = em[1].replace(/\\\//g, '/');
      var embed = httpGet(inv, embedURL, watchURL);
      var file = extractPlayerFile(embed);
      if (file !== null) result = { file: file };
    }
  }
  cache.set(ck, result, cfg(inv, 'cacheTTL'));
  return result;
}

// Из embed-HTML вытащить atob('...') (HTML-экранирован как &#039;), декодировать
// внешний JSON и вернуть его поле file. Берём первый блок, где есть file.
function extractPlayerFile(html){
  if (!html) return null;
  var re = /atob\((?:&#0?39;|&apos;|')([A-Za-z0-9+\/=]{40,})/g, m;
  while ((m = re.exec(html)) !== null){
    var raw;
    try { raw = atob(m[1]); } catch (e){ continue; }
    var obj;
    try { obj = JSON.parse(raw); } catch (e){ continue; }
    if (obj && obj.file !== undefined && obj.file !== null) return obj.file;
  }
  return null;
}

function parseSel(v){
  return (v === '' || v === undefined || v === null) ? -1 : parseInt(v, 10);
}

function handleChecksearch(inv){
  var title = trim(inv.query.title);
  var orig = trim(inv.query.original_title);
  if (!title && !orig) return { rch: false };
  var link = findLink(inv, title, orig);
  if (!link) return { rch: false };
  var serial = parseInt(inv.query.serial || 0, 10) === 1;
  return { rch: true, type: serial ? 'serial' : 'movie', quality: manifest.quality || '1080p' };
}

function renderMovie(inv, base, voices){
  if (!voices.length) return { type: 'movie', data: [] };
  var data = [];
  for (var i = 0; i < voices.length; i++){
    var u = maybeProxy(inv, voices[i].url);
    if (!u) continue;
    data.push({
      method: 'play', url: u, stream: u,
      name: voices[i].name || ('Озвучка ' + (i + 1)),
      title: base
    });
  }
  return { type: 'movie', data: data };
}

// episodes: [{id:"S1E1", title, file:"#2..."}] → season/episode навигация.
function renderSerial(inv, host, title, orig, base, episodes, s, t){
  var eps = [];
  for (var i = 0; i < episodes.length; i++){
    var ep = episodes[i] || {};
    var m = /S(\d+)\s*E(\d+)/i.exec(String(ep.id || ''));
    var sn = m ? parseInt(m[1], 10) : 1;
    var en = m ? parseInt(m[2], 10) : (i + 1);
    eps.push({ season: sn, ep: en, title: trim(ep.title), voices: parseVoices(deobfFile(ep.file)) });
  }
  if (!eps.length) return { type: 'movie', data: [] };

  // Уникальные номера сезонов.
  var seasonNums = [], seen = {};
  for (var j = 0; j < eps.length; j++){
    if (!seen[eps[j].season]){ seen[eps[j].season] = true; seasonNums.push(eps[j].season); }
  }
  seasonNums.sort(function(a, b){ return a - b; });

  var qbase = host + '/lite/awmzone?title=' + util.urlencode(title) +
              '&original_title=' + util.urlencode(orig) + '&serial=1';

  if (s === -1 && seasonNums.length > 1){
    var sdata = seasonNums.map(function(sn){
      return { method: 'link', id: sn, name: 'Сезон ' + sn, url: qbase + '&s=' + sn };
    });
    return { type: 'season', data: sdata };
  }

  var pick = (s === -1) ? seasonNums[0] : s;
  var seasonEps = [];
  for (var k = 0; k < eps.length; k++){ if (eps[k].season === pick) seasonEps.push(eps[k]); }
  seasonEps.sort(function(a, b){ return a.ep - b.ep; });
  if (!seasonEps.length) return { type: 'movie', data: [] };

  // Канонический список озвучек — из первой серии сезона.
  var canon = seasonEps[0].voices;
  var tIdx = (t === -1) ? 0 : Math.max(0, Math.min(t, canon.length - 1));

  var voiceData = canon.map(function(v, i){
    return {
      name: v.name || ('Озвучка ' + (i + 1)),
      active: i === tIdx,
      url: qbase + '&s=' + pick + '&t=' + i
    };
  });

  var data = [];
  for (var e = 0; e < seasonEps.length; e++){
    var url = pickVoiceURL(seasonEps[e].voices, canon[tIdx], tIdx);
    var u = maybeProxy(inv, url);
    if (!u) continue;
    var nm = seasonEps[e].title || ('Серия ' + seasonEps[e].ep);
    data.push({
      method: 'play', url: u, stream: u,
      s: seasonEps[e].season, e: seasonEps[e].ep,
      name: nm, title: base + ' (' + nm + ')'
    });
  }
  return { type: 'episode', voice: voiceData, data: data };
}

// Озвучки между сериями могут идти в разном порядке — матчим по имени, иначе по индексу.
function pickVoiceURL(voices, want, idx){
  if (!voices || !voices.length) return '';
  if (want && want.name){
    for (var i = 0; i < voices.length; i++){
      if (voices[i].name === want.name) return voices[i].url;
    }
  }
  var j = Math.max(0, Math.min(idx, voices.length - 1));
  return voices[j].url;
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);

  var host = inv.host;
  var title = trim(inv.query.title);
  var orig = trim(inv.query.original_title);
  var s = parseSel(inv.query.s);
  var t = parseSel(inv.query.t);

  var link = findLink(inv, title, orig);
  if (!link) return { type: 'movie', data: [] };

  var pf = fetchPlayerFile(inv, link);
  if (!pf) return { type: 'movie', data: [] };

  var base = [title, orig].filter(function(x){ return x; }).join(' / ');

  if (Array.isArray(pf.file)){
    return renderSerial(inv, host, title, orig, base, pf.file, s, t);
  }
  return renderMovie(inv, base, parseVoices(deobfFile(pf.file)));
}

module.exports = { handle: handle };
