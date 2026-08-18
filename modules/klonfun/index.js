// Klon.fun — JS port of internal/httpapi/klonfun.go.
// Search (POST DLE /index.php?do=search) → film page → iframe → Playerjs → stream.

var DEFAULTS = { host: 'https://klon.fun', proxyStreams: true };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
var UA='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36';

function httpGet(inv, url, ref){
  var res = http.get(url, { headers:{ 'User-Agent':UA, 'Referer':ref||cfg(inv,'host') }});
  return res.ok ? res.text : '';
}
function httpPostForm(inv, url, form, ref){
  var res = http.post(url, { form: form, headers:{ 'User-Agent':UA, 'Referer':ref||cfg(inv,'host') }});
  return res.ok ? res.text : '';
}
function stripTags(s){ return String(s||'').replace(/<[^>]+>/g,'').replace(/\s+/g,' ').trim(); }
function absURL(host, u){ u=(u||'').trim(); if(/^\/\//.test(u))return'https:'+u; if(/^\//.test(u))return host.replace(/\/+$/,'')+u; return u; }
function unescapeEntities(s){ return String(s||'').replace(/&amp;/g,'&').replace(/&quot;/g,'"').replace(/&#39;/g,"'").replace(/&lt;/g,'<').replace(/&gt;/g,'>'); }

function search(inv, q){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var html = httpPostForm(inv, host+'/index.php?do=search', { do:'search', subaction:'search', story:q }, host);
  if (!html) return [];
  var out = [];
  // Split on short-news__slide-item markers, like Go port.
  var parts = html.split('short-news__slide-item');
  for (var i=1;i<parts.length;i++){
    var block = parts[i];
    var hm = /href="([^"]+)"/.exec(block);
    if (!hm) continue;
    var href = absURL(host, unescapeEntities(hm[1]));
    var tm = /<h[23][^>]*>([\s\S]*?)<\/h[23]>/i.exec(block) || /title="([^"]+)"/.exec(block);
    var title = tm ? stripTags(tm[1]||tm[0]) : '';
    if (!title) continue;
    var poster = '';
    var pm = /<img[^>]+(?:src|data-src)="([^"]+)"/i.exec(block);
    if (pm) poster = absURL(host, pm[1]);
    var ym = /\b(19|20)\d{2}\b/.exec(block);
    out.push({ title: title, url: href, poster: poster, year: ym?parseInt(ym[0],10):0 });
  }
  return out;
}

function getItem(inv, href){
  var html = httpGet(inv, href, cfg(inv,'host'));
  if (!html) return null;
  var h1 = /<h1[^>]*>([\s\S]*?)<\/h1>/i.exec(html);
  var title = h1 ? stripTags(h1[1]) : '';
  var im = /<iframe[^>]+src="([^"]+)"/i.exec(html);
  var playerURL = im ? absURL(cfg(inv,'host'), unescapeEntities(im[1])) : '';
  var isSerial = /\/s(erial)?\//.test(playerURL) || /\/series\//.test(playerURL);
  return { title: title, playerURL: playerURL, isSerial: isSerial };
}

// Parse Playerjs "file:" in player HTML. Returns either string URL or array of voice→season→episode.
function extractPlayerfile(playerHTML){
  // Try JSON array / object (serial)
  var a = /file\s*:\s*'([\[{][\s\S]+?[\]}])'\s*[,}]/i.exec(playerHTML);
  if (a){
    try { return { serial: JSON.parse(a[1]) }; } catch(e){}
  }
  // Single movie file URL
  var m = /file\s*:\s*["']([^"']+)["']/i.exec(playerHTML);
  if (m) return { url: m[1] };
  // Multi-qualities
  var mm = /\[([\d]+p)\](https?:[^,]+\.m3u8[^,\"'\]]*)/gi;
  var multi = [], mr;
  while ((mr = mm.exec(playerHTML)) !== null) multi.push({ quality: mr[1], url: mr[2] });
  if (multi.length) return { multi: multi };
  return null;
}

function maybeProxy(inv, u){ return cfg(inv,'proxyStreams') ? proxy.url(u, 'klonfun') : u; }

// klon.fun DLE search returns year in each card — strict match by year
// and normalized title.
function strictMatch(results, inv){
  if (!results.length) return false;
  var year = parseInt(inv.query.year || 0, 10);
  var want1 = util.normalizeTitle(inv.query.original_title || '');
  var want2 = util.normalizeTitle(inv.query.title || '');
  for (var i=0;i<results.length;i++){
    var r = results[i];
    var n = util.normalizeTitle(r.title || '');
    var titleHit = (want1 && n===want1) || (want2 && n===want2);
    if (year && r.year === year && (!want1 && !want2 || titleHit)) return true;
    if (titleHit && (!year || !r.year)) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var queries = [inv.query.imdb_id, inv.query.original_title, inv.query.title]
    .map(function(x){ return (x||'').trim(); }).filter(function(x){ return x; });
  var results = [];
  for (var i=0;i<queries.length && !results.length;i++) results = search(inv, queries[i]);
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
  var t = (inv.query.t||'').trim();
  var s = (inv.query.s===''||inv.query.s===undefined)?-1:parseInt(inv.query.s,10);

  if (!href){
    var results = [];
    if (imdb) results = search(inv, imdb);
    if (!results.length) results = search(inv, orig);
    if (!results.length) results = search(inv, title);
    if (!results.length) return { type:'movie', data:[] };
    if (results.length>1){
      return { type:'similar', data: results.map(function(r){
        return {
          title: r.title, year: r.year, poster: r.poster,
          url: host+'/lite/klonfun?imdb_id='+util.urlencode(imdb)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial='+serial+'&href='+util.urlencode(r.url)
        };
      })};
    }
    href = results[0].url;
  }

  var item = getItem(inv, href);
  if (!item || !item.playerURL) return { type:'movie', data:[] };

  var playerHTML = httpGet(inv, item.playerURL, href);
  if (!playerHTML) return { type:'movie', data:[] };
  var f = extractPlayerfile(playerHTML);
  if (!f) return { type:'movie', data:[] };

  if (f.url && !item.isSerial){
    var u = maybeProxy(inv, f.url);
    return { type:'movie', data:[{ method:'play', url:u, stream:u, name:item.title||title, title:item.title||title }]};
  }
  if (f.multi && !item.isSerial){
    var q = {}, first = '';
    f.multi.forEach(function(x){ var pu=maybeProxy(inv, x.url); q[x.quality]=pu; if(!first)first=pu; });
    return { type:'movie', data:[{ method:'play', url:first, stream:first, name:item.title||title, title:item.title||title, quality:q, qualitys:q }]};
  }
  if (f.serial){
    // Voice → seasons → episodes tree similar to Playerjs.
    var voices = Array.isArray(f.serial) ? f.serial : [];
    if (!voices.length) return { type:'movie', data:[] };
    var voiceKey = t || String(0);
    var chosen = voices[parseInt(voiceKey,10)||0] || voices[0];
    var voiceData = voices.map(function(v, i){
      return {
        name: v.title||('Дубляж '+(i+1)),
        active: i===(parseInt(voiceKey,10)||0),
        url: host+'/lite/klonfun?href='+util.urlencode(href)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial=1&t='+i
      };
    });
    var seasons = Array.isArray(chosen.folder) ? chosen.folder : [];
    var seasonNums = seasons.map(function(se, i){ var m=/(\d+)/.exec(se.title||''); return m?parseInt(m[1],10):(i+1); });
    if (s===-1 && seasons.length>1){
      return { type:'season', data: seasons.map(function(se, i){
        return {
          method:'link', id: seasonNums[i],
          url: host+'/lite/klonfun?href='+util.urlencode(href)+'&title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&serial=1&t='+voiceKey+'&s='+seasonNums[i],
          name: (se.title||('Сезон '+seasonNums[i]))
        };
      })};
    }
    var pickSeason = s===-1 ? 0 : seasonNums.indexOf(s);
    if (pickSeason<0) pickSeason = 0;
    var eps = Array.isArray(seasons[pickSeason] && seasons[pickSeason].folder) ? seasons[pickSeason].folder : [];
    var sNum = seasonNums[pickSeason] || 1;
    var data = eps.map(function(ep, i){
      var em = /(\d+)/.exec(ep.title||''); var en = em?parseInt(em[1],10):(i+1);
      var stream = ep.file || '';
      if (!stream) return null;
      // If ep.file is "[720p]url,[1080p]url" treat as multi.
      var qMap = null, firstU = stream;
      var mm = /\[([\d]+p)\](https?:[^,]+)/g, mr, parsed=[];
      while ((mr = mm.exec(stream)) !== null) parsed.push({q:mr[1], u:mr[2]});
      if (parsed.length){
        qMap = {}; parsed.forEach(function(x){ qMap[x.q]=maybeProxy(inv, x.u); });
        firstU = qMap[parsed[parsed.length-1].q];
      } else firstU = maybeProxy(inv, stream);
      var base = [title,orig].filter(function(x){return x;}).join(' / ');
      var epName = ep.title || ('Епізод '+en);
      var row = { method:'play', url:firstU, stream:firstU, s:sNum, e:en, name:epName, title: base+' ('+epName+')' };
      if (qMap){ row.quality = qMap; row.qualitys = qMap; }
      return row;
    }).filter(function(x){return x;});
    return { type:'episode', data: data, voice: voiceData };
  }
  return { type:'movie', data:[] };
}
