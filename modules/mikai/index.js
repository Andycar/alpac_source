// Mikai — JS port of internal/httpapi/mikai.go.
// REST API at api.mikai.me/v1. Ukrainian anime dubbing.

var DEFAULTS = { host: 'https://api.mikai.me/v1', proxyStreams: true };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }

function apiGet(inv, url){
  var res = http.get(url, { headers:{ 'User-Agent':'Mozilla/5.0', 'Accept':'application/json', 'Referer': cfg(inv,'host') }});
  if (!res.ok) return null;
  try { return res.json(); } catch(e){ return null; }
}

function searchAPI(inv, query){
  if (!query) return [];
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var d = apiGet(inv, host+'/anime/search?page=1&limit=24&sort=year&order=desc&name='+util.urlencode(query));
  return (d && Array.isArray(d.result)) ? d.result : [];
}
function getAnime(inv, id){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var d = apiGet(inv, host+'/anime/'+id);
  return (d && d.result) ? d.result : null;
}
function displayName(anime){
  if (anime.details && anime.details.names){
    var n = anime.details.names;
    return n.nameEnglish || n.name || n.nameNative || '';
  }
  return '';
}

function search(inv, title, orig, year){
  var tries = [];
  if (orig) tries.push(orig);
  if (title && title!==orig) tries.push(title);
  for (var i=0;i<tries.length;i++){
    var r = searchAPI(inv, tries[i]);
    if (r.length){
      if (year){
        var m = r.filter(function(x){ return x.year===year; });
        if (m.length) return m;
      }
      return r;
    }
  }
  return [];
}

function collectVoices(inv, anime){
  // API uses camelCase (isSubs/playLink). For each player group, build voices.
  var voices = {}, seasonCounter = 1;
  (anime.players||[]).forEach(function(p){
    var voiceName = (p.team && p.team.name) ? p.team.name : 'Unknown';
    var isSubs = p.isSubs === true || p.is_subs === true;
    if (isSubs) voiceName += ' [SUB]';
    if (!voices[voiceName]) voices[voiceName] = { displayName: voiceName, isSubs: isSubs, seasons: {} };
    (p.providers||[]).forEach(function(prov){
      voices[voiceName].seasons[seasonCounter] = (prov.episodes||[]).map(function(e){
        return { number: e.number, url: e.playLink||e.play_link, title: 'Епізод '+e.number };
      });
      seasonCounter++;
    });
  });
  return voices;
}

function maybeProxy(inv, u){ return cfg(inv,'proxyStreams') ? proxy.url(u, 'mikai') : u; }

// Mikai returns year in anime metadata. Strict match by year + normalized
// title. The API itself already filters by name, so any result with
// matching year is considered a real hit.
function strictMatch(results, inv){
  if (!results.length) return false;
  var year = parseInt(inv.query.year || 0, 10);
  var want1 = util.normalizeTitle(inv.query.original_title || '');
  var want2 = util.normalizeTitle(inv.query.title || '');
  for (var i=0;i<results.length;i++){
    var it = results[i];
    if (year && it.year === year) return true;
    var n = util.normalizeTitle(displayName(it));
    if ((want1 && n===want1) || (want2 && n===want2)) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var r = search(inv, (inv.query.title||'').trim(), (inv.query.original_title||'').trim(), parseInt(inv.query.year||0,10));
  return strictMatch(r, inv) ? { rch:true, type:'movie', quality: manifest.quality||'FHD' } : { rch:false };
}

function handleIndex(inv){
  var host = inv.host;
  var title = (inv.query.title||'').trim();
  var orig = (inv.query.original_title||'').trim();
  var year = parseInt(inv.query.year||0,10);
  var id = parseInt(inv.query.id||0,10);
  var t = (inv.query.t===''||inv.query.t===undefined)?'':inv.query.t;
  var s = (inv.query.s===''||inv.query.s===undefined)?-1:parseInt(inv.query.s,10);

  if (!id){
    var picked = search(inv, title, orig, year);
    if (!picked.length) return { type:'movie', data:[] };
    if (picked.length>1){
      return { type:'similar', data: picked.map(function(r){
        var n = displayName(r);
        return {
          title: n||('Anime '+r.id),
          year: r.year,
          url: host+'/lite/mikai?id='+r.id+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)
        };
      })};
    }
    id = picked[0].id;
  }

  var anime = getAnime(inv, id);
  if (!anime) return { type:'movie', data:[] };
  var name = displayName(anime);

  var voices = collectVoices(inv, anime);
  var voiceKeys = Object.keys(voices);
  if (!voiceKeys.length) return { type:'movie', data:[] };
  if (!t) t = voiceKeys[0];
  var v = voices[t] || voices[voiceKeys[0]];
  var voiceData = voiceKeys.map(function(k){
    return {
      name: k, active: k===t,
      url: host+'/lite/mikai?id='+id+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&t='+util.urlencode(k)
    };
  });

  // Movie format — just one season, one episode.
  if (anime.format === 'movie'){
    var seasonsArr = Object.keys(v.seasons);
    if (!seasonsArr.length) return { type:'movie', data:[] };
    var firstEp = (v.seasons[seasonsArr[0]]||[])[0];
    if (!firstEp) return { type:'movie', data:[] };
    var mu = maybeProxy(inv, firstEp.url);
    return { type:'movie', data: [{ method:'play', url:mu, stream:mu, name:name||'Mikai', title: name }], voice: voiceData };
  }

  // Serial — show episodes of selected season.
  var seasonNums = Object.keys(v.seasons).map(function(x){return parseInt(x,10);}).sort(function(a,b){return a-b;});
  if (!seasonNums.length) return { type:'movie', data:[] };
  if (s===-1 && seasonNums.length>1){
    return { type:'season', data: seasonNums.map(function(sn){
      return {
        method:'link', id: sn,
        url: host+'/lite/mikai?id='+id+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&t='+util.urlencode(t)+'&s='+sn,
        name: sn+' сезон'
      };
    })};
  }
  if (s===-1) s = seasonNums[0];
  var eps = v.seasons[s] || [];
  var data = eps.map(function(ep){
    var u = maybeProxy(inv, ep.url);
    return { method:'play', url:u, stream:u, s:s, e:ep.number, name:ep.title, title:(name||'')+' ('+ep.title+')' };
  });
  return { type:'episode', data: data, voice: voiceData };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  return handleIndex(inv);
}
