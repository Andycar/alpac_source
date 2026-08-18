// Eneyida — JS port of internal/httpapi/eneyida.go.
// Flow: search (POST /index.php?do=search) → parse article block → fetch film page
// → extract iframe → iframe → parse Playerjs 'file:' JSON → build play data.

var DEFAULTS = { host: 'https://eneyida.tv', proxyStreams: true, timeout: 15 };
function cfg(inv,k){ var v=inv.config&&inv.config[k]; return (v===undefined||v===null||v==='')?DEFAULTS[k]:v; }
var UA='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36';

function httpGet(inv, url, ref){
  var res = http.get(url, { headers:{ 'User-Agent':UA, 'Accept':'text/html,application/xhtml+xml,*/*', 'Referer': ref||'' }});
  return res.ok ? res.text : '';
}
function httpPostForm(inv, url, form, ref){
  var res = http.post(url, { form: form, headers:{ 'User-Agent':UA, 'Referer':ref||'', 'X-Lampac-Go':'1' }});
  return res.ok ? res.text : '';
}

function normalizeTitle(s){ return String(s||'').toLowerCase().replace(/[^a-z0-9а-яёіїєґ ]+/gi,'').replace(/\s+/g,' ').trim(); }

function search(inv, title){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  return httpPostForm(inv, host+'/index.php?do=search', {
    do:'search', subaction:'search', search_start:'0', result_from:'1', story: title
  }, host);
}

// Find first matching result link on the search page for title+year.
function pickFromSearch(inv, html, wantTitle, wantYear){
  if (!html) return { link:'', similars:[] };
  var want = normalizeTitle(wantTitle);
  var rows = html.split('<article ');
  var similars = [], selected = '';
  for (var i=1;i<rows.length;i++){
    var row = '<article '+rows[i];
    if (row.indexOf('>Анонс</div>')>=0 || row.indexOf('>Трейлер</div>')>=0) continue;
    var lm = /<a[^>]+class="[^"]*short-poster[^"]*"[^>]+href="([^"]+)"/i.exec(row) || /<h3[^>]*>\s*<a[^>]+href="([^"]+)"/i.exec(row);
    if (!lm) continue;
    var link = lm[1];
    var nm = /<div[^>]*class="[^"]*title[^"]*"[^>]*>([\s\S]*?)<\/div>/i.exec(row);
    var name = nm ? String(nm[1]).replace(/<[^>]+>/g,'').trim() : '';
    var ym = /(\d{4})/.exec(row);
    var year = ym ? parseInt(ym[1],10) : 0;
    var poster = '';
    var pm = /<img[^>]+(?:src|data-src)="([^"]+)"/i.exec(row);
    if (pm) poster = pm[1];
    similars.push({ url: link, title: name, year: year, poster: poster });
    if (!selected && (!want || normalizeTitle(name)===want) && (!wantYear || year===wantYear)) selected = link;
  }
  if (!selected && similars.length===1) selected = similars[0].url;
  return { link: selected, similars: similars };
}

function fetchEmbed(inv, title, year, href){
  var host = cfg(inv,'host').replace(/\/+$/,'');
  var link = (href||'').trim();
  var similars = [];
  if (!link){
    var html = search(inv, title);
    var picked = pickFromSearch(inv, html, title, year);
    link = picked.link; similars = picked.similars;
    if (!link) return { empty: similars.length===0, similars: similars };
  }
  var page = httpGet(inv, link, host);
  if (!page) return null;
  // quality
  var quality = '';
  var qm = /Якість[^<]*<[^>]*>\s*<\/?[^>]+>\s*([^<]+)/i.exec(page) || /quality["'\s:]+([0-9]+p)/i.exec(page);
  if (qm) quality = qm[1].trim();
  // iframe URI
  var iframeM = /<iframe[^>]+src="([^"]+)"/i.exec(page);
  if (!iframeM) return null;
  var iframeURI = iframeM[1];
  if (/^\/\//.test(iframeURI)) iframeURI = 'https:'+iframeURI;
  if (/^\//.test(iframeURI)) iframeURI = host+iframeURI;
  var iframeHTML = httpGet(inv, iframeURI, link);
  if (!iframeHTML || iframeHTML.indexOf('file:')<0) return null;
  // Try to parse Playerjs file JSON array
  var raw = '';
  var am = /file\s*:\s*'([\[{][\s\S]+?[\]}])'\s*[,}]/i.exec(iframeHTML);
  if (am) raw = am[1];
  var serial = null, content = '';
  if (raw){
    try { serial = JSON.parse(raw); } catch(e){ serial = null; }
  }
  if (!serial) content = iframeHTML;
  return { content: content, serial: serial, quality: quality, link: link };
}

// Extract playerjs single movie stream URL from HTML. Simple heuristic — first m3u8.
function extractMovieStream(html){
  var m = /file\s*:\s*["']([^"']+\.m3u8[^"']*)["']/i.exec(html);
  if (m) return m[1];
  m = /["']([^"']+\.m3u8[^"']*)["']/i.exec(html);
  return m ? m[1] : '';
}

function maybeProxy(inv, u){ return cfg(inv,'proxyStreams') ? proxy.url(u, 'eneyida') : u; }
function joinName(t,o){ return [t,o].filter(function(x){return x;}).join(' / '); }

// Flatten playerjs serial tree (voice → seasons → episodes) into episodes list.
function flatten(serial, selectedVoice, selectedSeason){
  // serial[].title=voice; serial[].folder[].title="N сезон"; .folder[].folder[]=episodes
  var out = [], voices = [];
  for (var vi=0;vi<serial.length;vi++){
    var v = serial[vi];
    if (!v) continue;
    voices.push({ idx: vi, name: v.title||('Дубляж '+(vi+1)) });
    if (selectedVoice!==-1 && vi!==selectedVoice) continue;
    var seasons = Array.isArray(v.folder) ? v.folder : [];
    for (var si=0;si<seasons.length;si++){
      var s = seasons[si];
      var snum = parseInt((s.title||'').match(/\d+/)||[si+1],10);
      if (selectedSeason!==-1 && snum!==selectedSeason) continue;
      var eps = Array.isArray(s.folder) ? s.folder : [];
      for (var ei=0;ei<eps.length;ei++){
        var e = eps[ei];
        var enum_ = parseInt((e.title||'').match(/\d+/)||[ei+1],10);
        out.push({ voice: vi, season: snum, episode: enum_, file: e.file, title: e.title||('Епізод '+enum_) });
      }
    }
  }
  return { voices: voices, episodes: out };
}

// Strict match by normalized title+year from parsed DLE search card.
function strictMatch(similars, inv){
  if (!similars || !similars.length) return false;
  var year = parseInt(inv.query.year || 0, 10);
  var want1 = util.normalizeTitle(inv.query.original_title || '');
  var want2 = util.normalizeTitle(inv.query.title || '');
  for (var i=0;i<similars.length;i++){
    var got = util.normalizeTitle(similars[i].title || '');
    var titleHit = (want1 && got===want1) || (want2 && got===want2);
    if (titleHit && (!year || !similars[i].year || similars[i].year === year)) return true;
  }
  return false;
}

function handleChecksearch(inv){
  var title = (inv.query.original_title || inv.query.title || '').trim();
  if (!title) return { rch:false };
  var html = search(inv, title);
  var picked = pickFromSearch(inv, html, title, parseInt(inv.query.year||0,10));
  return strictMatch(picked.similars, inv) ? { rch:true, type:'movie', quality: manifest.quality||'FHD' } : { rch:false };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);

  var host = inv.host;
  var title = (inv.query.title||'').trim();
  var orig = (inv.query.original_title||'').trim();
  var href = (inv.query.href||'').trim();
  var year = parseInt(inv.query.year||0,10);
  var similar = inv.query.similar==='true'||inv.query.similar==='1';
  var clarif = parseInt(inv.query.clarification||0,10);
  var source = (inv.query.source||'').toLowerCase();
  var sid = (inv.query.id||'').trim();
  var s = (inv.query.s===''||inv.query.s===undefined)?-1:parseInt(inv.query.s,10);
  var t = (inv.query.t===''||inv.query.t===undefined)?-1:parseInt(inv.query.t,10);

  if (!href && source==='eneyida' && sid) href = cfg(inv,'host').replace(/\/+$/,'')+'/'+sid.replace(/^\/+/,'');
  if (!href && (!orig||!year)) return { type:'movie', data:[] };

  var searchTitle = similar||clarif===1 ? title : orig;
  var emb = fetchEmbed(inv, searchTitle, year, href);
  if (!emb) return { type:'movie', data:[] };
  if (emb.empty) return { type:'movie', data:[] };
  if (emb.similars && emb.similars.length && !emb.serial && !emb.content){
    return { type:'similar', data: emb.similars.map(function(it){
      return {
        title: it.title, year: it.year, poster: it.poster,
        url: host+'/lite/eneyida?title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&year='+(year||'')+'&clarification=1&href='+util.urlencode(it.url)
      };
    })};
  }

  if (emb.content){
    var stream = extractMovieStream(emb.content);
    if (!stream) return { type:'movie', data:[] };
    var u = maybeProxy(inv, stream);
    return { type:'movie', data:[{ method:'play', url:u, stream:u, name: joinName(title,orig), title: joinName(title,orig), quality: emb.quality }]};
  }

  if (Array.isArray(emb.serial)){
    var flat = flatten(emb.serial, t, s);
    // If no season chosen, show seasons list.
    if (s===-1){
      var seasons = {};
      flat.episodes.forEach(function(e){ seasons[e.season]=true; });
      var seasonsList = Object.keys(seasons).map(function(x){return parseInt(x,10);}).sort(function(a,b){return a-b;});
      if (!seasonsList.length) return { type:'movie', data:[] };
      if (seasonsList.length===1){
        s = seasonsList[0]; // auto-drill
      } else {
        return { type:'season', data: seasonsList.map(function(sn){
          return {
            method:'link', id: sn,
            url: host+'/lite/eneyida?title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&year='+(year||'')+'&href='+util.urlencode(emb.link||href)+'&s='+sn+(t!==-1?'&t='+t:''),
            name: sn+' сезон'
          };
        })};
      }
    }
    var flat2 = flatten(emb.serial, t===-1?0:t, s);
    var voices = flat2.voices;
    var activeVoice = t===-1 ? (voices[0]?voices[0].idx:0) : t;
    var voiceData = voices.map(function(v){
      return {
        name: v.name, active: v.idx===activeVoice,
        url: host+'/lite/eneyida?title='+util.urlencode(title)+'&original_title='+util.urlencode(orig)+'&year='+(year||'')+'&href='+util.urlencode(emb.link||href)+'&s='+s+'&t='+v.idx
      };
    });
    var base = joinName(title,orig);
    var data = flat2.episodes.map(function(ep){
      var u = maybeProxy(inv, ep.file);
      return { method:'play', url:u, stream:u, s: ep.season, e: ep.episode, name: ep.title, title: base+' ('+ep.title+')' };
    });
    return { type:'episode', data: data, voice: voiceData };
  }

  return { type:'movie', data:[] };
}
