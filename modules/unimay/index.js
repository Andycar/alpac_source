// Unimay — JS port of internal/httpapi/unimay.go.
// REST API at api.unimay.media/v1. Ukrainian-dubbed anime.

var DEFAULTS = { host: 'https://api.unimay.media/v1', proxyStreams: true };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }

function apiGet(inv, url){
  var res = http.get(url, { headers:{ 'Accept':'application/json','User-Agent':'Mozilla/5.0' }});
  if (!res.ok) return null;
  try { return res.json(); } catch(e){ return null; }
}

function searchAPI(inv, title, orig, serial){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var tries = [];
  if (orig) tries.push(orig);
  if (title && title!==orig) tries.push(title);
  for (var i=0;i<tries.length;i++){
    var r = apiGet(inv, host+'/release/search?page=0&page_size=10&title='+util.urlencode(tries[i]));
    if (r && Array.isArray(r.content) && r.content.length) return r;
  }
  return null;
}
function releaseAPI(inv, code){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  return apiGet(inv, host+'/release?code='+util.urlencode(code));
}

function displayName(item){
  return (item.names && (item.names.ukr || item.names.eng)) || item.title || '';
}

function maybeProxy(inv, u){ return cfg(inv,'proxyStreams') ? proxy.url(u, 'unimay') : u; }

// Strict match by year + normalized title. Unimay's API returns year field,
// so we use it together with title similarity.
function strictMatch(items, inv){
  if (!items || !items.length) return false;
  var year = parseInt(inv.query.year || 0, 10);
  var want1 = util.normalizeTitle(inv.query.original_title || '');
  var want2 = util.normalizeTitle(inv.query.title || '');
  for (var i=0;i<items.length;i++){
    var it = items[i];
    var names = [
      it.names && it.names.ukr,
      it.names && it.names.eng,
      it.title
    ].filter(function(x){return x;}).map(util.normalizeTitle);
    var titleHit = names.some(function(n){ return (want1 && n===want1) || (want2 && n===want2); });
    if (titleHit || (year && it.year === year)) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var r = searchAPI(inv, inv.query.title||'', inv.query.original_title||'', parseInt(inv.query.serial||-1,10));
  var items = (r && r.content) || [];
  return strictMatch(items, inv) ? { rch:true, type:'movie', quality:manifest.quality||'FHD' } : { rch:false };
}

function handleSearchList(inv){
  var host = inv.host;
  var title = (inv.query.title||'').trim(), orig = (inv.query.original_title||'').trim();
  var serial = parseInt(inv.query.serial||-1,10);
  var r = searchAPI(inv, title, orig, serial);
  if (!r||!r.content) return { type:'movie', data:[] };
  var data = [];
  for (var i=0;i<r.content.length;i++){
    var item = r.content[i];
    if (serial!==-1){
      var isMovie = item.type === 'Фільм';
      if ((serial===0 && !isMovie) || (serial===1 && isMovie)) continue;
    }
    var n = displayName(item);
    data.push({
      title: n, year: item.year, type: item.type,
      url: host+'/lite/unimay?code='+util.urlencode(item.code)+'&title='+util.urlencode(n)+'&original_title='+util.urlencode(orig)+'&serial='+serial
    });
  }
  return { type:'similar', data: data };
}

function handleRelease(inv){
  var host = inv.host;
  var code = (inv.query.code||'').trim();
  var title = (inv.query.title||'').trim();
  var orig = (inv.query.original_title||'').trim();
  var serial = parseInt(inv.query.serial||0,10);
  var seasonNum = parseInt(inv.query.s||0,10);
  var epNum = parseInt(inv.query.e||0,10);
  var play = inv.query.play==='true'||inv.query.play==='1';

  var r = releaseAPI(inv, code);
  if (!r || !Array.isArray(r.playlist) || !r.playlist.length) return { type:'movie', data:[] };

  if (play){
    var ep = null;
    if (r.type === 'Телесеріал'){
      if (seasonNum<=0||epNum<=0) return { rch:false };
      ep = r.playlist.find(function(e){ return e.number===epNum; });
    } else {
      ep = r.playlist[0];
    }
    if (!ep||!ep.hls||!ep.hls.master) return { rch:false };
    var stream = maybeProxy(inv, ep.hls.master);
    return { method:'play', url:stream, title: title || displayName(r) };
  }

  // List
  var isMovie = r.type !== 'Телесеріал';
  if (isMovie){
    var ep0 = r.playlist[0];
    if (!ep0||!ep0.hls||!ep0.hls.master) return { type:'movie', data:[] };
    var u = host+'/lite/unimay?code='+util.urlencode(code)+'&play=1&title='+util.urlencode(title);
    return { type:'movie', data:[{ method:'call', url:u, name: title||displayName(r), title: title||displayName(r) }]};
  }
  // Serial
  var episodes = r.playlist.slice().sort(function(a,b){ return (a.number||0)-(b.number||0); });
  var data = episodes.map(function(ep){
    var en = ep.number;
    var name = ep.title || ('Епізод '+en);
    return {
      method:'call',
      url: host+'/lite/unimay?code='+util.urlencode(code)+'&play=1&s=1&e='+en+'&title='+util.urlencode(title),
      name: name, title: name, s:1, e:en
    };
  });
  return { type:'episode', data: data };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  if ((inv.query.code||'').trim()) return handleRelease(inv);
  return handleSearchList(inv);
}
