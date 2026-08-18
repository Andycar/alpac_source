// AnimeON — JS port of internal/httpapi/animeon.go.
// REST API: /api/anime/search, /api/player/{id}/translations, /api/player/{id}/episodes, /api/player/{id}/episode.

var DEFAULTS = { host: 'https://animeon.club', proxyStreams: true };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
var UA='Mozilla/5.0';

function apiGet(inv, url){
  var res = http.get(url, { headers:{ 'User-Agent':UA, 'Referer': cfg(inv,'host'), 'Accept':'application/json' }});
  if (!res.ok) return null;
  try { return res.json(); } catch(e){ return null; }
}

function search(inv, query){
  if (!query) return [];
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var data = apiGet(inv, host+'/api/anime/search?text='+util.urlencode(query));
  return (data && Array.isArray(data.result)) ? data.result : [];
}

function pickBest(inv, title, orig, imdb, serial){
  var results = search(inv, title);
  if (!results.length) results = search(inv, orig);
  if (!results.length) return { list: [], seasons: [] };
  // API uses camelCase (titleUa/titleEn/imdbId). Handle either case.
  var titleEn = results[0] ? (results[0].titleEn || results[0].title_en) : '';
  if (serial===1 && titleEn){
    var extra = search(inv, titleEn);
    var seen = {}; results.forEach(function(r){ seen[r.id]=true; });
    extra.forEach(function(e){ if(!seen[e.id]){ results.push(e); seen[e.id]=true; }});
  }
  var filtered = results;
  if (imdb){
    var m = results.filter(function(r){ return (r.imdbId||r.imdb_id)===imdb; });
    if (m.length) filtered = m;
  }
  return { list: filtered, seasons: filtered };
}

function getTranslations(inv, animeID){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var d = apiGet(inv, host+'/api/player/'+animeID+'/translations');
  return (d && Array.isArray(d.translations)) ? d.translations : [];
}
function getEpisodes(inv, animeID, playerID, fundubID){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var d = apiGet(inv, host+'/api/player/'+animeID+'/episodes?take=100&skip=-1&playerId='+playerID+'&translationId='+fundubID);
  if (!d) return [];
  // API may nest under d.episodes or return array directly.
  if (Array.isArray(d)) return d;
  return Array.isArray(d.episodes) ? d.episodes : [];
}
function getEpisodeStream(inv, episodeID){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var d = apiGet(inv, host+'/api/player/'+episodeID+'/episode');
  // response shape varies; return raw
  return d;
}

function pickFundub(translations){
  // Flatten to list of voice variants: {name, playerID, fundubID, episodesCount}
  var out = [];
  translations.forEach(function(t){
    var fundub = t.translation || {};
    (t.player||[]).forEach(function(p){
      out.push({
        fundubID: fundub.id, fundubName: fundub.name||'',
        playerID: p.id, playerName: p.name||'',
        episodesCount: p.episodes_count||0,
        displayName: (fundub.name||'') + (p.name? ' ['+p.name+']':'')
      });
    });
  });
  return out;
}

function maybeProxy(inv, u){ return cfg(inv,'proxyStreams') ? proxy.url(u, 'animeon') : u; }

// AnimeON returns imdb_id/year — use them for strict match. Fall back to
// normalized title equality to catch legit matches where metadata is missing.
function strictMatch(results, inv){
  if (!results.length) return false;
  var imdb = (inv.query.imdb_id || '').trim();
  var year = parseInt(inv.query.year || 0, 10);
  var want1 = util.normalizeTitle(inv.query.original_title || '');
  var want2 = util.normalizeTitle(inv.query.title || '');
  for (var i=0;i<results.length;i++){
    var r = results[i];
    var rimdb = r.imdbId || r.imdb_id || '';
    if (imdb && rimdb === imdb) return true;
    if (year && r.year === year) return true;
    var n1 = util.normalizeTitle(r.titleUa || r.title_ua || '');
    var n2 = util.normalizeTitle(r.titleEn || r.title_en || '');
    if ((want1 && (n1===want1 || n2===want1)) || (want2 && (n1===want2 || n2===want2))) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var title = (inv.query.title || inv.query.original_title || '').trim();
  if (!title) return { rch:false };
  var r = search(inv, title);
  if (!r.length) r = search(inv, (inv.query.original_title||'').trim());
  return strictMatch(r, inv) ? { rch:true, type:'movie', quality: manifest.quality||'FHD' } : { rch:false };
}

function handleIndex(inv){
  var host = inv.host;
  var title = (inv.query.title||'').trim();
  var orig = (inv.query.original_title||'').trim();
  var imdb = (inv.query.imdb_id||'').trim();
  var serial = parseInt(inv.query.serial||0,10);
  var animeID = parseInt(inv.query.anime_id||0,10);
  var t = (inv.query.t===''||inv.query.t===undefined)?-1:parseInt(inv.query.t,10);

  if (!animeID){
    var picked = pickBest(inv, title, orig, imdb, serial);
    if (!picked.list.length) return { type:'movie', data:[] };
    if (picked.list.length>1 && serial!==1){
      return { type:'similar', data: picked.list.map(function(r){
        return {
          title: r.titleUa||r.title_ua||r.titleEn||r.title_en||'',
          year: r.year,
          url: host+'/lite/animeon?anime_id='+r.id+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial='+serial
        };
      })};
    }
    animeID = picked.list[0].id;
  }

  var fundubs = pickFundub(getTranslations(inv, animeID));
  if (!fundubs.length) return { type:'movie', data:[] };

  var voice = t===-1 ? 0 : Math.min(t, fundubs.length-1);
  var chosen = fundubs[voice];
  var voiceData = fundubs.map(function(f, i){
    return {
      name: f.displayName, active: i===voice,
      url: host+'/lite/animeon?anime_id='+animeID+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial='+serial+'&t='+i
    };
  });

  var episodes = getEpisodes(inv, animeID, chosen.playerID, chosen.fundubID);
  if (!episodes.length) return { type:'movie', data:[] };

  var data = episodes.sort(function(a,b){ return ((a.episodeNum||a.episode_num)||0)-((b.episodeNum||b.episode_num)||0); }).map(function(ep, i){
    var en = ep.episodeNum || ep.episode_num || (i+1);
    var stream = ep.hls || ep.videoUrl || ep.video_url || '';
    if (!stream) return null;
    var u = maybeProxy(inv, stream);
    var name = ep.name || ('Епізод '+en);
    return { method:'play', url:u, stream:u, s:1, e:en, name:name, title: (title||orig)+' ('+name+')' };
  }).filter(function(x){return x;});

  return { type:'episode', data: data, voice: voiceData };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  if (/\/play(\b|$)/.test(inv.path||'')) {
    // direct call from older clients; stream URL already in data, return rch
    return { rch:false };
  }
  return handleIndex(inv);
}
