// Uaflix — JS port of internal/httpapi/uaflix.go (simplified).
// Full Go version has UaflixAuth (DLE cookies), AMSP bypass, zetvideo-serial
// handling, pagination probes. This port covers the common flow:
// DLE search → film page → iframe → Playerjs (ashdi single-voice / zetvideo single).

var DEFAULTS = { host: 'https://uafix.net', proxyStreams: true, dleCookie: '' };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
var UA='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36';

function baseHeaders(inv){
  var h = { 'User-Agent':UA, 'Referer': cfg(inv,'host'), 'Accept':'text/html,application/xhtml+xml,*/*' };
  var dle = (cfg(inv,'dleCookie')||'').trim();
  if (dle){
    h.Cookie = dle.indexOf('=')>=0 ? dle : ('dle_user_login='+dle);
  }
  return h;
}

function httpGet(inv, url){
  var res = http.get(url, { headers: baseHeaders(inv) });
  return res.ok ? res.text : '';
}
function httpPostForm(inv, url, form){
  var res = http.post(url, { form: form, headers: baseHeaders(inv) });
  return res.ok ? res.text : '';
}

function absURL(host, u){ u=(u||'').trim(); if(/^\/\//.test(u))return'https:'+u; if(/^\//.test(u))return host.replace(/\/+$/,'')+u; return u; }
function stripTags(s){ return String(s||'').replace(/<[^>]+>/g,'').replace(/\s+/g,' ').trim(); }
function unescapeEntities(s){ return String(s||'').replace(/&amp;/g,'&').replace(/&quot;/g,'"').replace(/&#39;/g,"'"); }

function searchDLE(inv, query){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var html = httpPostForm(inv, host+'/index.php?do=search', { do:'search', subaction:'search', story: query });
  if (!html) return [];
  var re = /<a[^>]+class="[^"]*short-(?:img|link|poster)?[^"]*"[^>]+href="([^"]+)"[\s\S]*?title="([^"]+)"/gi;
  var out = [], m;
  while ((m = re.exec(html)) !== null){
    out.push({ url: absURL(host, unescapeEntities(m[1])), title: m[2] });
    if (out.length>=10) break;
  }
  if (!out.length){
    // Fallback — generic article link.
    var re2 = /<article[\s\S]*?<a[^>]+href="([^"]+)"[^>]*>([\s\S]*?)<\/a>/gi;
    while ((m = re2.exec(html)) !== null){
      var t = stripTags(m[2]);
      if (!t) continue;
      out.push({ url: absURL(host, unescapeEntities(m[1])), title: t });
      if (out.length>=10) break;
    }
  }
  return out;
}

// Return the first playable iframe URL on a film page.
function extractIframe(html){
  var patterns = [
    /<meta[^>]*property="og:video:iframe"[^>]*content="([^"]+)"/i,
    /<div[^>]*class="[^"]*video-box[^"]*"[^>]*>[\s\S]*?<iframe[^>]*(?:src|data-src)="([^"]+)"/i,
    /<iframe[^>]*(?:src|data-src)="([^"]+)"/i
  ];
  for (var i=0;i<patterns.length;i++){
    var m = patterns[i].exec(html);
    if (m) return unescapeEntities(m[1]);
  }
  return '';
}

// Extract Playerjs stream. Ashdi serial URL → let user go to ashdi module (imdb flow). Here:
// only direct file: '...mp4/m3u8' or JSON array.
function extractPlayerfile(html){
  var a = /file\s*:\s*'([\[{][\s\S]+?[\]}])'\s*[,}]/i.exec(html);
  if (a){ try { return { serial: JSON.parse(a[1]) }; } catch(e){} }
  var m = /file\s*:\s*["']([^"']+)["']/i.exec(html);
  if (m) return { url: m[1] };
  return null;
}

function maybeProxy(inv, u){ return cfg(inv,'proxyStreams') ? proxy.url(u, 'uaflix') : u; }
function joinName(t,o){ return [t,o].filter(function(x){return x;}).join(' / '); }

// uafix.net DLE search returns article cards; we only have titles to match.
// Normalized-title equality (plus substring) keeps UI clean from noise.
function strictMatch(results, inv){
  if (!results.length) return false;
  var want1 = util.normalizeTitle(inv.query.original_title || '');
  var want2 = util.normalizeTitle(inv.query.title || '');
  for (var i=0;i<results.length;i++){
    var n = util.normalizeTitle(results[i].title || '');
    if ((want1 && n===want1) || (want2 && n===want2)) return true;
    if (want1 && n.indexOf(want1) >= 0) return true;
    if (want2 && n.indexOf(want2) >= 0) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var queries = [inv.query.original_title, inv.query.title]
    .map(function(x){ return (x||'').trim(); }).filter(function(x){ return x; });
  var results = [];
  for (var i=0;i<queries.length && !results.length;i++) results = searchDLE(inv, queries[i]);
  return strictMatch(results, inv) ? { rch:true, type:'movie', quality: manifest.quality||'FHD' } : { rch:false };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  var host = inv.host;
  var title = (inv.query.title||'').trim();
  var orig = (inv.query.original_title||'').trim();
  var imdb = (inv.query.imdb_id||'').trim();
  var href = (inv.query.href||'').trim();
  var serial = parseInt(inv.query.serial||0,10);
  var s = (inv.query.s===''||inv.query.s===undefined)?-1:parseInt(inv.query.s,10);
  var t = (inv.query.t===''||inv.query.t===undefined)?-1:parseInt(inv.query.t,10);

  if (!href){
    var results = searchDLE(inv, orig||title);
    if (!results.length) return { type:'movie', data:[] };
    if (results.length>1){
      return { type:'similar', data: results.map(function(r){
        return {
          title: r.title,
          url: host+'/lite/uaflix?title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&imdb_id='+util.urlencode(imdb)+'&serial='+serial+'&href='+util.urlencode(r.url)
        };
      })};
    }
    href = results[0].url;
  }

  var page = httpGet(inv, href);
  if (!page) return { type:'movie', data:[] };
  var iframeURL = extractIframe(page);
  if (!iframeURL) return { type:'movie', data:[] };

  // If iframe is ashdi, redirect client to ashdi module (wormhole flow).
  if (/ashdi\.vip\//.test(iframeURL) && imdb){
    return { type:'similar', data: [{
      title: 'Відкрити через ashdi (wormhole)',
      url: host+'/lite/ashdi?imdb_id='+util.urlencode(imdb)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial='+serial
    }]};
  }

  var iframeHTML = httpGet(inv, iframeURL);
  if (!iframeHTML) return { type:'movie', data:[] };
  var f = extractPlayerfile(iframeHTML);
  if (!f) return { type:'movie', data:[] };

  if (f.url){
    var u = maybeProxy(inv, f.url);
    return { type:'movie', data:[{ method:'play', url:u, stream:u, name: joinName(title,orig), title: joinName(title,orig) }]};
  }
  if (f.serial && Array.isArray(f.serial)){
    // Re-use the tree-based parser we've used in kinoukr/eneyida/klonfun.
    var voices = f.serial;
    var voiceIdx = t===-1?0:Math.max(0, Math.min(t, voices.length-1));
    var chosen = voices[voiceIdx];
    var voiceData = voices.map(function(v, i){
      return {
        name: v.title||('Дубляж '+(i+1)), active: i===voiceIdx,
        url: host+'/lite/uaflix?href='+util.urlencode(href)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&imdb_id='+util.urlencode(imdb)+'&serial=1&t='+i
      };
    });
    var seasons = Array.isArray(chosen.folder) ? chosen.folder : [];
    var seasonNums = seasons.map(function(se, i){ var m=/(\d+)/.exec(se.title||''); return m?parseInt(m[1],10):(i+1); });
    if (s===-1 && seasons.length>1){
      return { type:'season', data: seasons.map(function(se, i){
        return {
          method:'link', id: seasonNums[i],
          url: host+'/lite/uaflix?href='+util.urlencode(href)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&imdb_id='+util.urlencode(imdb)+'&serial=1&t='+voiceIdx+'&s='+seasonNums[i],
          name: se.title||('Сезон '+seasonNums[i])
        };
      })};
    }
    var pickSeason = s===-1?0:seasonNums.indexOf(s);
    if (pickSeason<0) pickSeason=0;
    var eps = Array.isArray(seasons[pickSeason] && seasons[pickSeason].folder) ? seasons[pickSeason].folder : [];
    var sNum = seasonNums[pickSeason] || 1;
    var base = joinName(title,orig);
    return { type:'episode', voice: voiceData, data: eps.map(function(ep, i){
      var em = /(\d+)/.exec(ep.title||''); var en = em?parseInt(em[1],10):(i+1);
      var stream = ep.file || '';
      if (!stream) return null;
      var u = maybeProxy(inv, stream);
      return { method:'play', url:u, stream:u, s:sNum, e:en, name: ep.title||('Епізод '+en), title: base+' ('+(ep.title||('Епізод '+en))+')' };
    }).filter(function(x){return x;})};
  }
  return { type:'movie', data:[] };
}
