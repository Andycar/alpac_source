// SakhTV (sakh.tv) — JS-порт балансера, исполняется в goja.
// Полная мимикрия Android TV APK: те же 4 хедера (Authorization, X-Force-Code:1,
// X-App-Id:5, User-Agent "SakhTVAndroid/<ver>/<MAKE MODEL>/Android <release>"),
// тот же login-flow (/v2/users/login → UUID token), тот же набор эндпоинтов.
//
// Включается через admin → JS-модули → SakhTV. Когда включён, перебивает
// встроенный Go-балансер `sakhtv` (см. lite_sources.go dynRoutes.Lookup).

var DEFAULTS = {
  host:   'https://api.sakh.tv',
  login:  '',
  passwd: '',
  token:  '',
  app_id: '5'
};

// UA-профили один-в-один как формирует APK через
// "SakhTVAndroid/"+versionName+"/"+capitalize(MANUFACTURER)+" "+MODEL+"/Android "+VERSION.RELEASE
// (см. декомпиляцию BitmapFactoryDecoder$$ExternalSyntheticLambda0 case 28).
var APP_VERSION = '1.2.0';
var DEVICE_PROFILES = [
  ['Xiaomi Mi BOX 4',           '9'],
  ['Xiaomi Mi BOX S',           '9'],
  ['Xiaomi MIBOX4K',            '11'],
  ['Nvidia SHIELD Android TV',  '11'],
  ['Sony BRAVIA 4K GB',         '13'],
  ['Sony BRAVIA 4K UR1',        '12'],
  ['Onn 4K Streaming Box',      '12'],
  ['Hisense H65A6500',          '11'],
  ['Tcl TV',                    '13'],
  ['Google ADT-3',              '13']
];
function pickUserAgent(){
  // Кэшируем выбранный UA в module-cache, чтобы внутри сессии не прыгал.
  var ua = cache.get('ua');
  if (ua) return ua;
  var p = DEVICE_PROFILES[Math.floor(Math.random()*DEVICE_PROFILES.length)];
  ua = 'SakhTVAndroid/'+APP_VERSION+'/'+p[0]+'/Android '+p[1];
  cache.set('ua', ua, 24*3600);
  return ua;
}

function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
function trimRight(s,c){ while(s.length && s.charAt(s.length-1)===c) s=s.substring(0,s.length-1); return s; }

function buildHeaders(inv, token){
  var h = {
    'X-Force-Code':   '1',
    'X-App-Id':       cfg(inv,'app_id'),
    'User-Agent':     pickUserAgent(),
    'Accept':         'application/json',
    'Accept-Language':'ru-RU,ru;q=0.9,en;q=0.8'
  };
  if (token) h['Authorization'] = token;
  return h;
}

// ─── Auth ──────────────────────────────────────────────────────────────────
//
// Token lifecycle:
//   1) config.token задан → используем как есть (без логина).
//   2) cache.token свежий → используем.
//   3) login/passwd → POST /v2/users/login → cache token надолго.
// На 401/403 кэш сбрасывается и логин повторяется однократно.

function loadToken(inv){
  var t = (cfg(inv,'token')||'').trim();
  if (t) return t;
  t = cache.get('token');
  return t || '';
}

// Аккаунт ОДНОСЕССИОННЫЙ: каждый /v2/users/login выпускает новый токен и гасит
// предыдущий — вместе с ним умирают ссылки, по которым прямо сейчас смотрят
// другие. Поэтому логин здесь — деструктивная операция, а не дешёвый ретрай:
// не чаще одного раза в LOGIN_COOLDOWN. Лучше источник помолчит 3 минуты, чем
// у всех зрителей оборвётся плейбэк.
var LOGIN_COOLDOWN = 180;

function doLogin(inv){
  var login = (cfg(inv,'login')||'').trim();
  var pass  = cfg(inv,'passwd')||'';
  if (!login || !pass) return '';
  if (cache.get('login_cooldown')) { console.warn('sakhtv: login suppressed — cooldown'); return ''; }
  cache.set('login_cooldown', 1, LOGIN_COOLDOWN); // окно занимаем ДО запроса
  var host = trimRight(cfg(inv,'host'),'/');
  var res = http.post(host+'/v2/users/login', {
    headers: Object.assign({'Content-Type':'application/json'}, buildHeaders(inv, '')),
    body:    { login: login, password: pass }
  });
  if (!res.ok) { console.warn('sakhtv: login HTTP '+res.status); return ''; }
  var j; try { j = res.json(); } catch(e){ return ''; }
  if (!j || !j.auth || !j.token) return '';
  cache.set('token', j.token, 24*3600); // токен живёт долго, повторно логиниться нет нужды
  return j.token;
}

function authToken(inv){
  var t = loadToken(inv);
  if (t) return t;
  return doLogin(inv);
}

function apiGet(inv, path, allowRetry){
  var host = trimRight(cfg(inv,'host'),'/');
  var token = authToken(inv);
  if (!token) return null;
  var res = http.get(host+path, { headers: buildHeaders(inv, token) });
  // ТОЛЬКО 401 = токен мёртв. 403 у sakhtv — это контентный/тарифный гейт и
  // про сессию не говорит; релогин по нему выбивал всех остальных зрителей.
  if (res.status===401){
    if (allowRetry === false) return null;
    cache.delete('token');
    var fresh = doLogin(inv);
    if (!fresh) return null;
    res = http.get(host+path, { headers: buildHeaders(inv, fresh) });
  }
  if (!res.ok) return null;
  try { return res.json(); } catch(e){ console.warn('sakhtv: json parse', e); return null; }
}

// ─── Search & matching ─────────────────────────────────────────────────────

function searchQueries(title, originalTitle){
  var out=[], seen={};
  function add(v){ v=(v||'').trim(); if(!v) return; var k=v.toLowerCase(); if(seen[k]) return; seen[k]=true; out.push(v); }
  add(title); add(originalTitle);
  return out;
}

function search(inv, query){
  return apiGet(inv, '/v2/common/search?query='+util.urlencode(query)+'&amount=20');
}

function parseYear(date){
  if (!date) return 0;
  if (date.length<4) return 0;
  var y = parseInt(date.substring(0,4), 10);
  return isNaN(y) ? 0 : y;
}

function pickMovie(items, year, originalTitle){
  if (!items || !items.length) return null;
  var lcOrig = (originalTitle||'').trim().toLowerCase();
  var byYear=null, byOrig=null, fallback=null;
  for (var i=0;i<items.length;i++){
    var it=items[i]; if(!it.id_alpha) continue;
    if (!fallback) fallback=it;
    var iy = parseYear(it.release_date);
    if (year>0 && iy===year && !byYear) byYear=it;
    if (lcOrig && (it.origin_title||'').trim().toLowerCase()===lcOrig && !byOrig) byOrig=it;
  }
  return byYear || byOrig || fallback;
}

function pickSerial(items, year, imdb, kp){
  if (!items || !items.length) return null;
  var kpInt = parseInt(kp||0, 10);
  var imdbLC = (imdb||'').trim().toLowerCase().replace(/^tt/, '');
  var byKP=null, byImdb=null, byYear=null, fallback=null;
  for (var i=0;i<items.length;i++){
    var it=items[i]; if(!it.tvshow) continue;
    if (!fallback) fallback=it;
    if (kpInt>0 && it.kp_id===kpInt && !byKP) byKP=it;
    if (imdbLC && !byImdb){
      var u = (it.imdb_url||'').toLowerCase();
      if (u.indexOf('tt'+imdbLC)>=0 || u.indexOf('/'+imdbLC)>=0) byImdb=it;
    }
    if (year>0 && it.year===year && !byYear) byYear=it;
  }
  return byKP || byImdb || byYear || fallback;
}

// ─── Movie rendering ───────────────────────────────────────────────────────

function pickStream(movie){
  var src = movie.sources || {};
  var vars = src.variants || [];
  for (var i=0;i<vars.length;i++) if (vars[i].type==='hls' && vars[i].url) return vars[i].url;
  if (src['default']) return src['default'];
  for (var j=0;j<vars.length;j++) if (vars[j].url) return vars[j].url;
  return '';
}

function subtitleList(tracks){
  if (!tracks || !tracks.length) return null;
  var out=[];
  for (var i=0;i<tracks.length;i++){
    var t=tracks[i]; if(!t.src) continue;
    var label=(t.label||'').trim() || (t.language||'').toUpperCase();
    out.push({label:label, url:t.src});
  }
  return out.length ? out : null;
}

function joinName(ru, en){
  ru=(ru||'').trim(); en=(en||'').trim();
  if (ru && en) return ru+' / '+en;
  return ru || en || 'Untitled';
}

function streamProxy(url){ return proxy.url(url, 'sakhtv'); }

function renderMovie(inv, idAlpha, title, originalTitle){
  var movie = apiGet(inv, '/v2/movie/'+encodeURIComponent(idAlpha));
  if (!movie) return null;
  var stream = pickStream(movie);
  if (!stream) return null;
  var row = {
    method: 'play',
    url:    streamProxy(stream),
    stream: streamProxy(stream),
    name:   'По умолчанию',
    title:  joinName(movie.ru_title||title, movie.origin_title||originalTitle)
  };
  var subs = subtitleList(((movie.sources||{}).pd||{}).tracks);
  if (subs) row.subtitles = subs;
  return { type:'movie', data:[row] };
}

// ─── Serial rendering ──────────────────────────────────────────────────────

function loadSeriesDetail(inv, tvshow){
  var key='series:'+tvshow;
  var cached = cache.get(key);
  if (cached) return cached;
  var d = apiGet(inv, '/v1/serials/get?tvshow='+util.urlencode(tvshow));
  if (d) cache.set(key, d, 300);
  return d;
}

function dateToISO(s){
  s = (s||'').trim();
  if (!s || s==='0') return '';
  var p = s.split('.');
  if (p.length !== 3 || p[2].length !== 4) return '';
  return p[2]+'-'+('0'+p[1]).slice(-2)+'-'+('0'+p[0]).slice(-2);
}

function loadSeasonVoices(inv, seasonID){
  var key='voices:'+seasonID;
  var cached = cache.get(key);
  if (cached) return cached;
  var eps = apiGet(inv, '/v1/serials/get_episodes?season_id='+seasonID);
  if (!Array.isArray(eps)) return null;
  var seen={}, voices=[], previews={}, airdates={};
  for (var i=0;i<eps.length;i++){
    var ep = eps[i] || {};
    if (ep.id && ep.preview) previews[ep.id] = ep.preview;
    if (ep.id){
      var iso = dateToISO(ep.date);
      if (iso) airdates[ep.id] = iso;
    }
    var rgs=ep.rgs||[];
    for (var j=0;j<rgs.length;j++){
      var v=rgs[j]; if(!v.rg||seen[v.rg]) continue;
      seen[v.rg]=true;
      voices.push({rg:v.rg, runame:v.runame||v.rg});
    }
  }
  var result = { voices: voices, previews: previews, airdates: airdates };
  cache.set(key, result, 300);
  return result;
}

function renderSerial(inv, tvshow, title, originalTitle, seasonNum, voiceIdx){
  var detail = loadSeriesDetail(inv, tvshow);
  if (!detail || !Array.isArray(detail.seasons) || !detail.seasons.length) return null;

  var host = inv.host;
  var ids = '';
  if (inv.query.imdb_id)      ids += '&imdb_id='+util.urlencode(inv.query.imdb_id);
  if (inv.query.kinopoisk_id) ids += '&kinopoisk_id='+util.urlencode(inv.query.kinopoisk_id);
  var qt = util.urlencode(title||''), qo = util.urlencode(originalTitle||'');
  var rjson = inv.query.rjson ? 'true' : 'false';

  if (seasonNum < 1){
    var seasons = [];
    detail.seasons.forEach(function(sn){
      var num = parseInt((sn.index||'').replace(/^0+/, ''), 10);
      if (!num) return;
      seasons.push({
        num: num,
        url: host+'/lite/sakhtv?rjson='+rjson+'&serial=true'+ids+'&title='+qt+'&original_title='+qo+'&s='+num
      });
    });
    seasons.sort(function(a,b){ return a.num-b.num; });
    // SakhTV has no per-season art — reuse the series poster on every card.
    var seasonImg = (detail.poster||'').trim();
    return {
      type: 'season',
      data: seasons.map(function(s){
        var row = { method:'link', id:s.num, url:s.url, name:s.num+' сезон' };
        if (seasonImg) row.img = seasonImg;
        return row;
      })
    };
  }

  // Episode list.
  var seasonID = 0;
  for (var i=0;i<detail.seasons.length;i++){
    var n = parseInt((detail.seasons[i].index||'').replace(/^0+/, ''), 10);
    if (n === seasonNum) { seasonID = detail.seasons[i].id; break; }
  }
  if (!seasonID) return null;

  var vData = loadSeasonVoices(inv, seasonID);
  if (!vData || !vData.voices || !vData.voices.length) return null;
  var voices = vData.voices, previews = vData.previews || {}, airdates = vData.airdates || {};
  if (voiceIdx<0 || voiceIdx>=voices.length) voiceIdx=0;

  var voiceRows = voices.map(function(v,i){
    return {
      method: 'link',
      name:   (v.runame||'').trim() || v.rg,
      active: i===voiceIdx,
      url:    host+'/lite/sakhtv?rjson='+rjson+'&serial=true'+ids+'&title='+qt+'&original_title='+qo+'&s='+seasonNum+'&t='+i
    };
  });

  var pl = apiGet(inv, '/v1/serial/watch/get_playlist?season_id='+seasonID+'&rg='+util.urlencode(voices[voiceIdx].rg));
  if (!Array.isArray(pl) || !pl.length) return null;

  var baseTitle = joinName(title, originalTitle);
  var seriesPoster = (detail.poster||'').trim();
  var eps = [];
  pl.forEach(function(p){
    var s=(p.episode_playlist||'').trim(); if(!s) return;
    var epNum = parseInt((p.episode_index||'').replace(/^0+/, ''), 10) || 0;
    var label = (p.episode_name||'').trim() || (epNum+' серия');
    var stream = streamProxy(s);
    var row = {
      method:'play',
      url:    stream,
      stream: stream,
      s:      seasonNum,
      e:      epNum,
      name:   label,
      title:  baseTitle+' ('+label+')'
    };
    var img = previews[p.episode_id] || seriesPoster;
    if (img) { row.img = img; row.thumbnail = img; }
    var iso = airdates[p.episode_id];
    if (iso) { row.air_date = iso; row.release_date = iso; }
    eps.push({ epNum: epNum, row: row });
  });
  if (!eps.length) return null;
  eps.sort(function(a,b){ return a.epNum-b.epNum; });

  return { type:'episode', data: eps.map(function(x){return x.row;}), voice: voiceRows };
}

// ─── Top-level dispatch ────────────────────────────────────────────────────

function handleChecksearch(inv){
  var q=inv.query;
  var serial = (q.serial==='true'||q.serial==='1'||q.serial==='True'||q.serial===true);
  var year = parseInt(q.year||'0', 10) || 0;
  var imdb = q.imdb_id||'', kp = q.kinopoisk_id||'';
  var queries = searchQueries(q.title, q.original_title);
  for (var i=0;i<queries.length;i++){
    var res = search(inv, queries[i]);
    if (!res) continue;
    if (serial){
      if (pickSerial(res.serials, year, imdb, kp)) return { rch:true, type:'movie', quality: manifest.quality||'FHD' };
    } else {
      if (pickMovie(res.movies, year, q.original_title||'')) return { rch:true, type:'movie', quality: manifest.quality||'FHD' };
      if (pickSerial(res.serials, year, imdb, kp))           return { rch:true, type:'movie', quality: manifest.quality||'FHD' };
    }
  }
  return { rch:false };
}

function emptyResp(rjson){ return rjson ? {} : ''; }

function handleIndex(inv){
  var q = inv.query;
  var rjson = !!(q.rjson==='true'||q.rjson==='1'||q.rjson===true);
  var title = (q.title||'').trim();
  var originalTitle = (q.original_title||'').trim();
  if (!title && !originalTitle) return emptyResp(rjson);
  var serial = (q.serial==='true'||q.serial==='1'||q.serial===true);
  var year = parseInt(q.year||'0', 10) || 0;
  var imdb = q.imdb_id||'', kp = q.kinopoisk_id||'';
  var sParam = (q.s===undefined||q.s==='') ? -1 : (parseInt(q.s, 10) || -1);
  var tParam = (q.t===undefined||q.t==='') ? -1 : (parseInt(q.t, 10) || 0);

  var queries = searchQueries(title, originalTitle);
  for (var i=0;i<queries.length;i++){
    var res = search(inv, queries[i]);
    if (!res) continue;
    if (!serial){
      var m = pickMovie(res.movies, year, originalTitle);
      if (m){
        var rendered = renderMovie(inv, m.id_alpha, title, originalTitle);
        if (rendered) return rendered;
      }
    }
    var ser = pickSerial(res.serials, year, imdb, kp);
    if (ser){
      var rs = renderSerial(inv, ser.tvshow, title, originalTitle, sParam, tParam);
      if (rs) return rs;
    }
  }
  return emptyResp(rjson);
}

// manifest is injected as a global by some hosts; if not, fall back to a tag.
if (typeof manifest === 'undefined') manifest = { quality: 'FHD' };

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  return handleIndex(inv);
}

module.exports = { handle: handle };
