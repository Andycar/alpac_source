// kbteam — kb-team.club MSX videocdn API → Lampa balancer.
//
// Movie:  vid={kp} → массив озвучек × 3 качества (FHD/480/HD).
// Serial: 3 уровня — vid={kp} → seasons (sid) → voices (sid+tid+episodes) → series.
//
// CDN-хост mpa.education1ne.in.net = CNAME cdn.zerocdn.com (GEO CIS-only).
// URL-токен включает {YYYYMMDDhh} — валидность ~1 час, поэтому кэш короткий.

var DEFAULTS = {
  apiHost: 'http://kb-team.club/msx/kinozal/videocdn.php',
  proxyStreams: true,
  cacheTTL: 1800
};
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
var UA='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';

function apiGet(inv, params){
  var url = cfg(inv,'apiHost') + '?' + params + '&device=';
  var ck  = 'kbteam:'+url;
  var hit = cache.get(ck);
  if (hit) return hit;
  var res = http.get(url, { headers:{ 'User-Agent':UA, 'Accept':'application/json' }});
  if (!res.ok) return null;
  var data = null;
  try { data = res.json(); } catch(e){ return null; }
  if (data) cache.set(ck, data, cfg(inv,'cacheTTL'));
  return data;
}

function maybeProxy(inv, u){
  if (!u) return '';
  return cfg(inv,'proxyStreams') ? proxy.url(u, 'kbteam') : u;
}

function stripVideo(action){
  return action ? String(action).replace(/^video:/i,'') : '';
}
function panelToParams(action){
  if (!action) return '';
  var q = String(action).replace(/^panel:/i,'');
  var i = q.indexOf('?');
  if (i<0) return '';
  return q.substring(i+1).replace(/&device=$/,'');
}
function cleanMSX(s){
  return String(s||'').replace(/\{[^}]+\}/g,'').replace(/\s+/g,' ').trim();
}
function qualityFromAction(act){
  // .../1080.mp4, .../480.mp4, .../360.mp4, .../240-v1-aN.mp4
  var m = /\/(\d+)(?:-[^/]+)?\.mp4/.exec(act||'');
  return m ? parseInt(m[1],10) : 0;
}
function qualityLabel(p){
  if (p>=1080) return '1080p';
  if (p>=720)  return '720p';
  if (p>=480)  return '480p';
  if (p>=360)  return '360p';
  return p+'p';
}

function flattenItems(data){
  if (!data) return [];
  if (Array.isArray(data.items)) return data.items;
  if (Array.isArray(data.pages)){
    var out=[];
    for (var i=0;i<data.pages.length;i++){
      var p = data.pages[i];
      if (p && Array.isArray(p.items)) out = out.concat(p.items);
    }
    return out;
  }
  return [];
}

function itemsLookSerial(items){
  for (var i=0;i<items.length;i++){
    var it = items[i];
    if (it && it.type==='control' && typeof it.action==='string' && /^panel:/i.test(it.action)) return true;
  }
  return false;
}

// Группирует фильмовые items: каждый `space` начинает озвучку, следующие
// control'ы (FHD/480/HD) — её качества.
function groupMovieVoices(items){
  var voices=[], cur=null;
  for (var i=0;i<items.length;i++){
    var it = items[i];
    if (!it) continue;
    if (it.type==='space'){
      if (cur && cur.urls.length) voices.push(cur);
      cur = { name: cleanMSX(it.titleHeader || it.label || ('Озвучка '+(voices.length+1))), qualities:{}, urls:[] };
      continue;
    }
    if (cur && it.action){
      var u = stripVideo(it.action);
      var q = qualityFromAction(it.action);
      if (u && q){
        cur.qualities[q] = u;
        cur.urls.push({q:q, url:u});
      }
    }
  }
  if (cur && cur.urls.length) voices.push(cur);
  return voices;
}

// vid=kp → есть ли что отдать (для checksearch и для "нет в каталоге" редиректа).
function probe(inv){
  var kp = (inv.query.kinopoisk_id||'').trim();
  if (!kp) return { ok:false };
  var data = apiGet(inv, 'act=watch&vid='+util.urlencode(kp));
  var items = flattenItems(data);
  return { ok: items.length>0, serial: itemsLookSerial(items), items: items };
}

function handleChecksearch(inv){
  var p = probe(inv);
  if (!p.ok) return { rch:false };
  return { rch:true, type: p.serial?'serial':'movie', quality: 'FHD' };
}

function renderMovie(inv, host, title, orig, voices){
  var base = [title,orig].filter(Boolean).join(' / ');
  if (!voices.length) return { type:'movie', data:[] };
  var data = voices.map(function(v){
    var qmap = {};
    var sorted = v.urls.slice().sort(function(a,b){return b.q-a.q;});
    for (var i=0;i<sorted.length;i++){
      qmap[qualityLabel(sorted[i].q)] = maybeProxy(inv, sorted[i].url);
    }
    var top = (sorted[0]||{}).url;
    var topProxied = maybeProxy(inv, top);
    return {
      method:'play',
      url: topProxied,
      stream: topProxied,
      quality: qmap,
      name: v.name || 'Озвучка',
      title: base + (v.name?' ('+v.name+')':'')
    };
  }).filter(function(x){return x.url;});
  return { type:'movie', data: data };
}

function renderSeasons(inv, host, title, orig, kp, seasonItems){
  var data = seasonItems.map(function(it, idx){
    var p = panelToParams(it.action);
    var sm = /(?:^|&)sid=(\d+)/.exec(p||'');
    var sidx = sm ? parseInt(sm[1],10) : idx;
    var label = cleanMSX(it.label) || ('Сезон '+(sidx+1));
    return {
      method:'link',
      id: sidx+1,
      name: label,
      url: host+'/lite/kbteam?kinopoisk_id='+util.urlencode(kp)+
           '&title='+util.urlencode(title)+
           '&original_title='+util.urlencode(orig)+
           '&serial=1&s='+(sidx+1)
    };
  });
  return { type:'season', data: data };
}

function renderEpisodes(inv, host, title, orig, kp, sNum, tidx, voicesItems, episodeItems){
  var base = [title,orig].filter(Boolean).join(' / ');
  var voiceData = voicesItems.map(function(it, i){
    return {
      name: cleanMSX(it.label) || ('Озвучка '+(i+1)),
      active: i===tidx,
      url: host+'/lite/kbteam?kinopoisk_id='+util.urlencode(kp)+
           '&title='+util.urlencode(title)+
           '&original_title='+util.urlencode(orig)+
           '&serial=1&s='+sNum+'&t='+i
    };
  });
  var data = episodeItems.map(function(it, i){
    var u = maybeProxy(inv, stripVideo(it.action));
    if (!u) return null;
    var num = i+1;
    var lm = /(\d+)/.exec(it.label||''); if (lm) num = parseInt(lm[1],10);
    var nm = cleanMSX(it.label) || ('Серия '+num);
    return {
      method:'play', url:u, stream:u,
      s:sNum, e:num,
      name: nm,
      title: base+' ('+nm+')'
    };
  }).filter(function(x){return x;});
  return { type:'episode', voice: voiceData, data: data };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);

  var host = inv.host;
  var kp   = (inv.query.kinopoisk_id||'').trim();
  if (!kp) return { type:'movie', data:[] };
  var title = (inv.query.title||'').trim();
  var orig  = (inv.query.original_title||'').trim();
  var s = (inv.query.s===''||inv.query.s===undefined) ? -1 : parseInt(inv.query.s,10);
  var t = (inv.query.t===''||inv.query.t===undefined) ? -1 : parseInt(inv.query.t,10);

  var p = probe(inv);
  if (!p.ok) return { type:'movie', data:[] };

  if (!p.serial){
    return renderMovie(inv, host, title, orig, groupMovieVoices(p.items));
  }

  // Сериал.
  if (s===-1){
    if (p.items.length>1) return renderSeasons(inv, host, title, orig, kp, p.items);
    s = 1; // один сезон — сразу на серии
  }
  var sidx = Math.max(0, s-1);
  if (sidx>=p.items.length) sidx = 0;

  var seasonParams = panelToParams(p.items[sidx].action);
  if (!seasonParams) return { type:'movie', data:[] };
  var voicesData = apiGet(inv, seasonParams);
  var voicesItems = flattenItems(voicesData);
  if (!voicesItems.length) return { type:'movie', data:[] };

  var tidx = (t===-1) ? 0 : Math.max(0, Math.min(t, voicesItems.length-1));
  var voiceParams = panelToParams(voicesItems[tidx].action);
  if (!voiceParams) return { type:'movie', data:[] };

  var epsData = apiGet(inv, voiceParams);
  var epsItems = flattenItems(epsData);
  return renderEpisodes(inv, host, title, orig, kp, sidx+1, tidx, voicesItems, epsItems);
}

module.exports = { handle: handle };
