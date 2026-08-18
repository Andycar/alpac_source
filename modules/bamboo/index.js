// Bamboo — JS port of internal/httpapi/bamboo.go.
// Scrapes bambooua.com HTML for search, episodes (sub/dub), and movie streams.

var DEFAULTS = { host: 'https://bambooua.com', proxyStreams: true };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }

function stripTags(s){ return String(s||'').replace(/<[^>]+>/g,'').replace(/\s+/g,' ').trim(); }
function normURL(host, u){
  if (!u) return '';
  u = u.trim();
  if (/^\/\//.test(u)) return 'https:'+u;
  if (/^\//.test(u)) return host.replace(/\/+$/,'')+u;
  return u;
}
function stripLampac(u){
  // Mimic bambooStripLampacArgs — remove ?lampac_token= and similar trackers.
  return String(u||'').replace(/[?&](lampac_[a-z_]+|lampac-)=[^&]*/gi,'').replace(/[?&]$/,'');
}

function siteGet(inv, url){
  var res = http.get(url, { headers:{
    'Accept':'text/html,application/xhtml+xml,application/xml;q=0.9',
    'Referer': cfg(inv,'host'),
    'User-Agent':'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36'
  }});
  return res.ok ? res.text : '';
}

function search(inv, title, orig){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var tries = [];
  if (orig) tries.push(orig);
  if (title && title!==orig) tries.push(title);
  for (var i=0;i<tries.length;i++){
    var r = searchOne(inv, host, tries[i]);
    if (r.length) return r;
  }
  return [];
}
function searchOne(inv, host, q){
  var url = host+'/index.php?do=search&subaction=search&story='+util.urlencode(q);
  var html = siteGet(inv, url);
  if (!html) return [];
  var re = /<li[^>]*class="[^"]*slide-item[^"]*"[^>]*>([\s\S]*?)<\/li>/gi;
  var out = [], m;
  while ((m = re.exec(html)) !== null){
    var block = m[1];
    var tm = /<h6[^>]*>([\s\S]*?)<\/h6>/i.exec(block);
    if (!tm) continue;
    var title = stripTags(tm[1]);
    var hrefM = /<a[^>]*href="([^"]+)"/i.exec(block);
    if (!hrefM) continue;
    var href = normURL(host, hrefM[1]);
    var poster = '';
    var pm = /<img[^>]*(?:src|data-src)="([^"]+)"/i.exec(block);
    if (pm) poster = normURL(host, pm[1]);
    out.push({ title: title, url: href, poster: poster });
  }
  return out;
}

// Parse <span data-file=".." data-type="sub|dub" data-title="..">...</span> blocks from a page.
function parseSpans(inv, host, html){
  var re = /<span([^>]*data-file\s*=\s*"([^"]+)"[^>]*)>([\s\S]*?)<\/span>/gi;
  var out = [], m;
  while ((m = re.exec(html)) !== null){
    var attrs = m[1], file = (m[2]||'').trim(), inner = m[3];
    if (!file) continue;
    var title = '';
    var tm = /data-title\s*=\s*"([^"]*)"/i.exec(attrs);
    if (tm) title = tm[1].trim();
    if (!title) title = stripTags(inner);
    if (!title) title = 'Episode';
    var n = /(\d+)/.exec(title);
    var ep = n ? parseInt(n[1],10) : 0;
    var type = '';
    var dt = /data-type\s*=\s*"([^"]*)"/i.exec(attrs);
    if (dt) type = (dt[1]||'').trim().toLowerCase();
    out.push({ title: title, url: normURL(host, file), episode: ep, type: type });
  }
  return out;
}

function getSeriesEpisodes(inv, href){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var html = siteGet(inv, href);
  if (!html) return null;
  var all = parseSpans(inv, host, html);
  var sub = all.filter(function(e){ return e.type==='sub'; });
  var dub = all.filter(function(e){ return e.type!=='sub'; });
  if (!sub.length && !dub.length) return null;
  return { sub: sub, dub: dub };
}

function getMovieStreams(inv, href){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var html = siteGet(inv, href);
  if (!html) return [];
  return parseSpans(inv, host, html);
}

function maybeProxy(inv, u){ return cfg(inv,'proxyStreams') ? proxy.url(u, 'bamboo') : u; }
function joinName(t,o){ return [t,o].filter(function(x){return x;}).join(' / '); }

// Strict match: bambooua.com HTML card lacks imdb_id, so we fuzzy-compare
// normalized titles. This keeps the UI clean when Lampa asks about a film
// that simply isn't in the catalog.
function strictMatch(results, inv){
  if (!results.length) return false;
  var want1 = util.normalizeTitle(inv.query.original_title || '');
  var want2 = util.normalizeTitle(inv.query.title || '');
  for (var i=0;i<results.length;i++){
    var got = util.normalizeTitle(results[i].title);
    if ((want1 && got === want1) || (want2 && got === want2)) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var r = search(inv, inv.query.title||'', inv.query.original_title||'');
  return strictMatch(r, inv) ? { rch:true, type:'movie', quality: manifest.quality||'FHD' } : { rch:false };
}

function handleIndex(inv){
  var host = inv.host;
  var title = (inv.query.title||'').trim();
  var origTitle = (inv.query.original_title||'').trim();
  var href = (inv.query.href||'').trim();
  var serial = parseInt(inv.query.serial||0,10);
  var t = (inv.query.t||'').trim();

  if (!href){
    var res = search(inv, title, origTitle);
    if (!res.length) return { type:'movie', data: [] };
    if (res.length>1){
      return { type:'similar', data: res.map(function(r){
        return {
          title: r.title,
          poster: r.poster,
          url: host+'/lite/bamboo?title='+util.urlencode(title)+'&original_title='+util.urlencode(origTitle)+'&year='+(inv.query.year||'')+'&serial='+(inv.query.serial||'')+'&href='+util.urlencode(r.url)
        };
      })};
    }
    href = res[0].url;
  }

  if (serial===1) return renderSerial(inv, host, title, origTitle, href, t);
  return renderMovie(inv, host, title, origTitle, href);
}

function renderSerial(inv, host, title, orig, href, chosenType){
  var s = getSeriesEpisodes(inv, href);
  if (!s) return { type:'episode', data:[] };
  var voices = [];
  if (s.sub.length) voices.push({ type:'sub', name:'Субтитри' });
  if (s.dub.length) voices.push({ type:'dub', name:'Озвучка' });
  if (!voices.length) return { type:'episode', data:[] };
  if (!chosenType) chosenType = voices[0].type;
  var voiceData = voices.map(function(v){
    return {
      name: v.name,
      active: v.type===chosenType,
      url: host+'/lite/bamboo?title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial=1&href='+util.urlencode(href)+'&t='+v.type
    };
  });
  var selected = chosenType==='sub' ? s.sub : s.dub;
  selected.sort(function(a,b){ return (a.episode||999999)-(b.episode||999999); });
  var base = joinName(title,orig);
  var data = selected.map(function(ep,i){
    var epNum = ep.episode || (i+1);
    var epName = (!ep.title||ep.title==='Episode') ? ('Епізод '+epNum) : ep.title;
    var u = maybeProxy(inv, stripLampac(ep.url));
    return { method:'play', url:u, stream:u, s:1, e:epNum, name:epName, title:base+' ('+epName+')' };
  });
  return { type:'episode', data: data, voice: voiceData };
}

function renderMovie(inv, host, title, orig, href){
  var streams = getMovieStreams(inv, href);
  if (!streams.length) return { type:'movie', data:[] };
  var mTitle = joinName(title,orig);
  return { type:'movie', data: streams.map(function(s,i){
    var label = s.title || ('Варіант '+(i+1));
    var u = maybeProxy(inv, stripLampac(s.url));
    return { method:'play', url:u, stream:u, name:label, title:mTitle };
  })};
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  return handleIndex(inv);
}
