// Ashdi — JS port of internal/httpapi/ashdi.go.
// Flow: wormhole(imdb_id) → ashdi play URL → ?multivoice → Playerjs JSON array.

var DEFAULTS = { wormholeHost: 'https://wormhole.lampame.v6.rocks', proxyStreams: true };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
var UA='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36';

function httpGet(inv, url, ref){
  var res = http.get(url, { headers:{ 'User-Agent':UA, 'Accept':'text/html,application/json', 'Referer': ref||'https://ashdi.vip/' }});
  return res.ok ? res.text : '';
}
function apiGet(inv, url){
  var res = http.get(url, { headers:{ 'User-Agent':UA, 'Accept':'application/json' }});
  if (!res.ok) return null;
  try { return res.json(); } catch(e){ return null; }
}

function wormholePlayURL(inv, imdb){
  if (!imdb) return '';
  var r = apiGet(inv, cfg(inv,'wormholeHost').replace(/\/+$/,'')+'/?imdb_id='+util.urlencode(imdb));
  return (r && r.play) ? r.play : '';
}

function fixStream(u){
  // CDN typo fix seen in wormhole output.
  return String(u||'').replace(/0yql3tj/g,'oyql3tj');
}

function fetchMultivoice(inv, imdb){
  var playURL = wormholePlayURL(inv, imdb);
  if (!playURL) return null;
  var multiHTML = httpGet(inv, playURL+'?multivoice', 'https://ashdi.vip/');
  if (!multiHTML || multiHTML.indexOf('new Playerjs')<0){
    // fallback: plain URL
    var plain = httpGet(inv, playURL, 'https://ashdi.vip/');
    if (!plain || plain.indexOf('new Playerjs')<0) return null;
    return { content: plain, playURL: playURL };
  }
  // Try to parse `file:'[...JSON...]',` as array.
  var m = /file\s*:\s*'([\s\S]+?)',/i.exec(multiHTML);
  if (!m) return { content: multiHTML, playURL: playURL };
  var raw = m[1].trim();
  if (raw.charAt(0) !== '['){
    return { content: multiHTML, playURL: playURL };
  }
  try {
    var voices = JSON.parse(raw);
    return { voices: voices, playURL: playURL };
  } catch(e){
    return { content: multiHTML, playURL: playURL };
  }
}

function maybeProxy(inv, u){
  if (!u) return '';
  var fixed = fixStream(u);
  return cfg(inv,'proxyStreams') ? proxy.url(fixed, 'ashdi') : fixed;
}
function joinName(t,o){ return [t,o].filter(function(x){return x;}).join(' / '); }

// Ashdi is imdb-id keyed — wormhole API answers with `play:""` if the title
// isn't in the catalog, so the mere presence of a non-empty play URL is a
// strict match by definition.
function handleChecksearch(inv){
  var imdb = (inv.query.imdb_id||'').trim();
  if (!imdb) return { rch:false };
  var play = wormholePlayURL(inv, imdb);
  return play ? { rch:true, type:'movie', quality: manifest.quality||'FHD' } : { rch:false };
}

// Render voice→season→episode structure.
function renderSerial(inv, host, title, orig, voices, imdb, t, s){
  t = t===-1?0:t;
  if (!voices.length) return { type:'movie', data:[] };
  var chosen = voices[Math.min(t, voices.length-1)];
  var voiceData = voices.map(function(v, i){
    return {
      name: v.title||('Дубляж '+(i+1)), active: i===t,
      url: host+'/lite/ashdi?imdb_id='+util.urlencode(imdb)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial=1&t='+i
    };
  });
  var seasons = Array.isArray(chosen.folder) ? chosen.folder : [];
  var seasonNums = seasons.map(function(se, i){ var m=/(\d+)/.exec(se.title||''); return m?parseInt(m[1],10):(i+1); });
  if (s===-1 && seasons.length>1){
    return { type:'season', data: seasons.map(function(se, i){
      return {
        method:'link', id: seasonNums[i],
        url: host+'/lite/ashdi?imdb_id='+util.urlencode(imdb)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial=1&t='+t+'&s='+seasonNums[i],
        name: (se.title||('Сезон '+seasonNums[i]))
      };
    })};
  }
  var pickSeason = s===-1 ? 0 : seasonNums.indexOf(s);
  if (pickSeason<0) pickSeason = 0;
  var eps = Array.isArray(seasons[pickSeason] && seasons[pickSeason].folder) ? seasons[pickSeason].folder : [];
  var sNum = seasonNums[pickSeason] || 1;
  var base = joinName(title,orig);
  return { type:'episode', voice: voiceData, data: eps.map(function(ep, i){
    var em = /(\d+)/.exec(ep.title||''); var en = em?parseInt(em[1],10):(i+1);
    var u = maybeProxy(inv, ep.file);
    return { method:'play', url:u, stream:u, s:sNum, e:en, name: ep.title||('Серія '+en), title: base+' ('+(ep.title||('Серія '+en))+')' };
  }).filter(function(x){return x.url;})};
}

function renderMovieMultiVoice(inv, host, title, orig, voices){
  var base = joinName(title,orig);
  var data = voices.map(function(v, i){
    var stream = maybeProxy(inv, v.file);
    if (!stream) return null;
    var name = v.title || ('Озвучка '+(i+1));
    return { method:'play', url: stream, stream: stream, name: name, title: base };
  }).filter(function(x){return x;});
  return { type:'movie', data: data };
}

function renderMovieSingle(inv, host, title, orig, content){
  var m = /file\s*:\s*["']([^"']+\.m3u8[^"']*)["']/i.exec(content) || /["']([^"']+\.m3u8[^"']*)["']/i.exec(content);
  if (!m) return { type:'movie', data:[] };
  var u = maybeProxy(inv, m[1]);
  return { type:'movie', data:[{ method:'play', url:u, stream:u, name: joinName(title,orig), title: joinName(title,orig) }]};
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);
  var host = inv.host;
  var title = (inv.query.title||'').trim();
  var orig = (inv.query.original_title||'').trim();
  var imdb = (inv.query.imdb_id||'').trim();
  var serial = parseInt(inv.query.serial||0,10);
  var s = (inv.query.s===''||inv.query.s===undefined)?-1:parseInt(inv.query.s,10);
  var t = (inv.query.t===''||inv.query.t===undefined)?-1:parseInt(inv.query.t,10);

  if (!imdb) return { type:'movie', data:[] };
  var emb = fetchMultivoice(inv, imdb);
  if (!emb) return { type:'movie', data:[] };

  if (emb.voices && emb.voices.length){
    // Determine movie vs serial by presence of .folder in any voice.
    var isSerial = emb.voices.some(function(v){ return Array.isArray(v.folder) && v.folder.length; });
    if (isSerial || serial===1) return renderSerial(inv, host, title, orig, emb.voices, imdb, t, s);
    return renderMovieMultiVoice(inv, host, title, orig, emb.voices);
  }
  if (emb.content) return renderMovieSingle(inv, host, title, orig, emb.content);
  return { type:'movie', data:[] };
}
