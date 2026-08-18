// StarLight — JS port of internal/httpapi/starlight.go
// Search → project → episodes → stream via vcms-api2 player-api.

var DEFAULTS = {
  host: 'https://tp-back.starlight.digital',
  playerAPI: 'https://vcms-api2.starlight.digital/player-api',
  referer: 'https://teleportal.ua/',
  lang: 'ua',
  proxyStreams: true
};
function cfg(inv, k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }

function apiGet(inv, url, ref){
  var res = http.get(url, { headers: { 'Accept':'application/json', 'Referer': ref||cfg(inv,'host'), 'User-Agent':'Mozilla/5.0' }});
  if (!res.ok) return null;
  try { return res.json(); } catch(e){ return null; }
}

function search(inv, title, orig){
  var q = (title||orig||'').trim();
  if (!q) return [];
  var host = cfg(inv,'host').replace(/\/+$/,''), lang = cfg(inv,'lang');
  var data = apiGet(inv, host+'/'+lang+'/live-search?q='+util.urlencode(q), host);
  if (!Array.isArray(data)) return [];
  var out = [];
  for (var i=0;i<data.length;i++){
    var it=data[i];
    if (!it.typeSlug||!it.channelSlug||!it.projectSlug) continue;
    out.push({ title: it.title||q, href: host+'/'+lang+'/'+it.typeSlug+'/'+it.channelSlug+'/'+it.projectSlug, typeSlug: it.typeSlug });
  }
  return out;
}

function getProject(inv, href){
  var raw = apiGet(inv, href, cfg(inv,'host'));
  if (!raw) return null;
  var project = { title: raw.title||'', hash: raw.hash||'', typeSlug: raw.typeSlug||'', seasons: [], episodes: [] };
  var seen = {};
  function addSeason(title, slug){ if(!slug||seen[slug])return; seen[slug]=true; project.seasons.push({title:title||'', slug:slug}); }
  if (Array.isArray(raw.seasons)) raw.seasons.forEach(function(s){ if(s&&s.seasonSlug) addSeason(s.title, s.seasonSlug); });
  if (Array.isArray(raw.seasonsGallery)){
    raw.seasonsGallery.forEach(function(sg){
      if (!sg) return;
      addSeason(sg.title, sg.seasonSlug);
      (sg.items||[]).forEach(function(it){
        if (!it) return;
        project.episodes.push({
          title: it.title||'', hash: it.hash||'', seasonSlug: sg.seasonSlug||'',
          number: parseInt(it.number||0,10), date: it.date||''
        });
      });
    });
  }
  return project;
}

function resolveStream(inv, hash){
  var url = cfg(inv,'playerAPI').replace(/\/+$/,'')+'/'+hash+'?referer='+util.urlencode(cfg(inv,'referer'))+'&lang='+cfg(inv,'lang');
  var raw = apiGet(inv, url, cfg(inv,'referer'));
  if (!raw) return null;
  var stream='', name=raw.name||'';
  if (Array.isArray(raw.video) && raw.video.length){
    var v = raw.video[0]||{};
    stream = v.mediaHlsNoAdv || v.mediaHls || '';
    if (!stream && Array.isArray(v.media) && v.media.length) stream = v.media[0].url||'';
  }
  if (!stream) return null;
  return { stream: stream, name: name };
}

function maybeProxy(inv, url){
  if (!cfg(inv,'proxyStreams')) return url;
  return proxy.url(url, 'starlight');
}

function emptyResp(type){ return { type: type||'movie', data: [] }; }

// Strict match by title+year — starlight's API doesn't return imdb/tmdb IDs,
// so we fuzzy-match on normalized title. Prevents rubbish matches from
// polluting Lampa UI when the title really isn't in starlight catalog.
function strictMatch(results, inv){
  if (!results.length) return false;
  var want = util.normalizeTitle(inv.query.title || inv.query.original_title || '');
  if (!want) return results.length > 0;
  for (var i=0;i<results.length;i++){
    if (util.normalizeTitle(results[i].title) === want) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var r = search(inv, inv.query.title||'', inv.query.original_title||'');
  return strictMatch(r, inv) ? { rch:true, type:'movie', quality: manifest.quality||'FHD' } : { rch:false };
}

function handleIndex(inv){
  var title = (inv.query.title||'').trim();
  var originalTitle = (inv.query.original_title||'').trim();
  var href = (inv.query.href||'').trim();
  var serial = parseInt(inv.query.serial||0,10);
  var sRaw = inv.query.s;
  var sIdx = (sRaw===''||sRaw===undefined)?-1:parseInt(sRaw,10);
  var host = inv.host;

  if (!href){
    var results = search(inv, title, originalTitle);
    if (!results.length) return emptyResp('movie');
    if (results.length>1){
      var data = results.map(function(r){
        return {
          title: r.title,
          url: host+'/lite/starlight?title='+util.urlencode(title)+'&original_title='+util.urlencode(originalTitle)+'&serial='+(inv.query.serial||'')+'&href='+util.urlencode(r.href)
        };
      });
      return { type:'similar', data:data };
    }
    href = results[0].href;
  }
  var project = getProject(inv, href);
  if (!project) return emptyResp('movie');

  if (serial===1 && project.seasons.length){
    if (sIdx===-1){
      return { type:'season', data: project.seasons.map(function(s,i){
        return {
          method:'link', id:i,
          url: host+'/lite/starlight?title='+util.urlencode(title)+'&original_title='+util.urlencode(originalTitle)+'&serial=1&s='+i+'&href='+util.urlencode(href),
          name: s.title || ('Сезон '+(i+1))
        };
      })};
    }
    if (sIdx<0||sIdx>=project.seasons.length) return emptyResp('episode');
    var season = project.seasons[sIdx];
    var episodes = project.episodes.filter(function(e){ return e.seasonSlug===season.slug; });
    if (!episodes.length) return emptyResp('episode');
    var baseTitle = title || originalTitle || 'StarLight';
    return { type:'episode', data: episodes.map(function(ep,i){
      if (!ep.hash) return null;
      var epName = ep.title || ('Епізод '+(i+1));
      return {
        method:'call',
        url: host+'/lite/starlight/play?hash='+util.urlencode(ep.hash)+'&title='+util.urlencode(title),
        name: epName,
        title: baseTitle+' ('+epName+')',
        s: sIdx,
        e: i+1
      };
    }).filter(function(x){return x;})};
  }

  // Movie mode: one hash, one entry.
  var hash = project.hash;
  if (!hash){
    for (var i=0;i<project.episodes.length;i++) if (project.episodes[i].hash){ hash=project.episodes[i].hash; break; }
  }
  if (!hash) return emptyResp('movie');
  var mTitle = title || 'StarLight';
  return { type:'movie', data: [{
    method:'call',
    url: host+'/lite/starlight/play?hash='+util.urlencode(hash)+'&title='+util.urlencode(title),
    name: mTitle,
    title: mTitle
  }]};
}

function handlePlay(inv){
  var hash = (inv.query.hash||'').trim();
  var title = (inv.query.title||'').trim();
  if (!hash) return { rch:false };
  var result = resolveStream(inv, hash);
  if (!result||!result.stream) return { rch:false };
  var streamURL = maybeProxy(inv, result.stream);
  return {
    method:'play',
    url: streamURL,
    title: title || result.name || 'StarLight'
  };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  if (/\/play(\b|$)/.test(inv.path||'')) return handlePlay(inv);
  return handleIndex(inv);
}
