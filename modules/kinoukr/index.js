// KinoUKR — JS port of internal/httpapi/kinoukr.go.
// xfsearch (GET, Cloudflare-bypass) → page → iframe (ashdi preferred, then tortuga) → Playerjs.

var DEFAULTS = { host: 'https://kinoukr.tv', proxyStreams: true };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
var UA='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36';

function httpGet(inv, url, ref){
  var res = http.get(url, { headers:{ 'User-Agent':UA, 'Referer': ref||'', 'Accept':'text/html,application/xhtml+xml' }});
  return res.ok ? res.text : '';
}

function xfSearch(inv, query){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  return httpGet(inv, host+'/xfsearch/'+util.urlencode(query)+'/', host);
}

function fetchPageInfo(inv, pageURL){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var html = httpGet(inv, pageURL, host);
  if (!html) return null;
  var info = { pageURL: pageURL, title: '', original: '', year: 0, iframes: [] };
  var om = /class="foriginal">([^<]+)</.exec(html);
  if (om) info.original = om[1].trim();
  var ym = /\/xfsearch\/year\/(\d{4})\//.exec(html);
  if (ym) info.year = parseInt(ym[1],10);
  var re = /<iframe[^>]+src="(https?:\/\/(?:ashdi|tortuga)[^"]+)"/ig, m, ashdi=[], tortuga=[];
  while ((m = re.exec(html)) !== null){
    if (/ashdi/i.test(m[1])) ashdi.push(m[1]); else tortuga.push(m[1]);
  }
  info.iframes = ashdi.concat(tortuga);
  var h1 = /<h1[^>]*>([\s\S]*?)<\/h1>/i.exec(html);
  if (h1) info.title = String(h1[1]).replace(/<[^>]+>/g,'').trim();
  return info;
}

function searchKinoukr(inv, title, orig){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var queries = [];
  if (orig) queries.push(orig);
  if (title && title!==orig) queries.push(title);
  var out = [];
  for (var i=0;i<queries.length;i++){
    var html = xfSearch(inv, queries[i]);
    if (!html) continue;
    // Each result is a card with a page link.
    var re = /<a[^>]+href="(https?:\/\/[^"]*kinoukr[^"]*)"[^>]*>([\s\S]*?)<\/a>/gi, m;
    while ((m = re.exec(html)) !== null){
      var url = m[1];
      var innerText = String(m[2]).replace(/<[^>]+>/g,'').trim();
      if (!innerText||innerText.length<3) continue;
      out.push({ url: url, title: innerText });
      if (out.length>=8) break;
    }
    if (out.length) break;
  }
  // Deduplicate by URL.
  var seen = {}, dedup=[];
  out.forEach(function(r){ if(!seen[r.url]){ seen[r.url]=true; dedup.push(r); }});
  return dedup;
}

// Extract Playerjs "file:" content from an iframe HTML.
function extractPlayerfile(html){
  var a = /file\s*:\s*'([\[{][\s\S]+?[\]}])'\s*[,}]/i.exec(html);
  if (a){ try { return { serial: JSON.parse(a[1]) }; } catch(e){} }
  var m = /file\s*:\s*["']([^"']+)["']/i.exec(html);
  if (m) return { url: m[1] };
  return null;
}

function maybeProxy(inv, u){ return cfg(inv,'proxyStreams') ? proxy.url(u, 'kinoukr') : u; }
function joinName(t,o){ return [t,o].filter(function(x){return x;}).join(' / '); }

// kinoukr's xfsearch returns card with title; year is in the page url path.
// For checksearch we only have titles — fuzzy-match them to keep UI clean.
function strictMatch(results, inv){
  if (!results.length) return false;
  var want1 = util.normalizeTitle(inv.query.original_title || '');
  var want2 = util.normalizeTitle(inv.query.title || '');
  for (var i=0;i<results.length;i++){
    var n = util.normalizeTitle(results[i].title || '');
    if ((want1 && n===want1) || (want2 && n===want2)) return true;
    // Very permissive fallback — accept substring match when source site
    // decorates titles with extra words (e.g. "... (2014) укр.").
    if (want1 && n.indexOf(want1) >= 0) return true;
    if (want2 && n.indexOf(want2) >= 0) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var r = searchKinoukr(inv, (inv.query.title||'').trim(), (inv.query.original_title||'').trim());
  return strictMatch(r, inv) ? { rch:true, type:'movie', quality: manifest.quality||'FHD' } : { rch:false };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  var host = inv.host;
  var title = (inv.query.title||'').trim();
  var orig = (inv.query.original_title||'').trim();
  var year = parseInt(inv.query.year||0,10);
  var href = (inv.query.href||'').trim();
  var serial = parseInt(inv.query.serial||0,10);
  var s = (inv.query.s===''||inv.query.s===undefined)?-1:parseInt(inv.query.s,10);
  var t = (inv.query.t===''||inv.query.t===undefined)?-1:parseInt(inv.query.t,10);

  if (!href){
    var results = searchKinoukr(inv, title, orig);
    if (!results.length) return { type:'movie', data:[] };
    if (results.length>1){
      return { type:'similar', data: results.map(function(r){
        return {
          title: r.title,
          url: host+'/lite/kinoukr?title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&year='+(year||'')+'&serial='+serial+'&href='+util.urlencode(r.url)
        };
      })};
    }
    href = results[0].url;
  }

  var info = fetchPageInfo(inv, href);
  if (!info || !info.iframes.length) return { type:'movie', data:[] };
  var iframeHTML = httpGet(inv, info.iframes[0], href);
  if (!iframeHTML) return { type:'movie', data:[] };
  var f = extractPlayerfile(iframeHTML);
  if (!f) return { type:'movie', data:[] };

  if (f.url){
    var u = maybeProxy(inv, f.url);
    return { type:'movie', data:[{ method:'play', url:u, stream:u, name: info.title||joinName(title,orig), title: joinName(title,orig) }]};
  }
  if (f.serial && Array.isArray(f.serial)){
    var voices = f.serial;
    var voiceIdx = t===-1 ? 0 : Math.max(0, Math.min(t, voices.length-1));
    var chosen = voices[voiceIdx];
    var voiceData = voices.map(function(v, i){
      return {
        name: v.title||('Дубляж '+(i+1)), active: i===voiceIdx,
        url: host+'/lite/kinoukr?href='+util.urlencode(href)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&year='+(year||'')+'&serial=1&t='+i
      };
    });
    var seasons = Array.isArray(chosen.folder) ? chosen.folder : [];
    var seasonNums = seasons.map(function(se, i){ var m=/(\d+)/.exec(se.title||''); return m?parseInt(m[1],10):(i+1); });
    if (s===-1 && seasons.length>1){
      return { type:'season', data: seasons.map(function(se, i){
        return {
          method:'link', id: seasonNums[i],
          url: host+'/lite/kinoukr?href='+util.urlencode(href)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial=1&t='+voiceIdx+'&s='+seasonNums[i],
          name: (se.title||('Сезон '+seasonNums[i]))
        };
      })};
    }
    var pickSeason = s===-1 ? 0 : seasonNums.indexOf(s);
    if (pickSeason<0) pickSeason = 0;
    var eps = Array.isArray(seasons[pickSeason] && seasons[pickSeason].folder) ? seasons[pickSeason].folder : [];
    var sNum = seasonNums[pickSeason] || 1;
    var base = joinName(title,orig);
    var data = eps.map(function(ep, i){
      var em = /(\d+)/.exec(ep.title||''); var en = em?parseInt(em[1],10):(i+1);
      var stream = ep.file || '';
      if (!stream) return null;
      var u = maybeProxy(inv, stream);
      var epName = ep.title || ('Епізод '+en);
      return { method:'play', url:u, stream:u, s:sNum, e:en, name:epName, title: base+' ('+epName+')' };
    }).filter(function(x){return x;});
    return { type:'episode', data: data, voice: voiceData };
  }
  return { type:'movie', data:[] };
}
