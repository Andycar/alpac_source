// Red Head Sound (redheadsound1.top) — JS-port via jsmodules.
//
// Flow: POST DLE search → film page → grab stloadi-iframe (Mirage CDN) →
// hand the (token_movie, token) pair to the built-in /lite/mirage handler
// via `method:"call"`. The heavy resolve (Mirage's Playwright/Chrome flow,
// 4K manifest, CDN headers) is done by the existing Mirage balancer — we
// just bridge RHS → Mirage.
//
// Why this and not ladoni: ladoni.pro hard-blocks datacenter IPs (any uTLS /
// FlareSolverr attempt returns the same 545-byte stub), so the only way RHS
// is reachable from a VPS is through stloadi.live (Mirage), which serves the
// same content via a different CDN that does NOT IP-blacklist datacenters.
//
// Requires: a working `[online.mirage]` config with a valid api.apbugall.org
// token (the same one Lampa uses for the Mirage balancer). Without it Mirage
// will return empty and the user sees no stream.

var DEFAULTS = {
  site: 'https://redheadsound1.top',
  userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0 Safari/537.36',
  yearTolerance: 1,
  miragePrefix: '/lite/mirage'
};

function cfg(inv, k){
  var v = inv && inv.config ? inv.config[k] : undefined;
  return (v === undefined || v === null || v === '') ? DEFAULTS[k] : v;
}
function siteURL(inv){ return String(cfg(inv,'site')).replace(/\/+$/,''); }
function ua(inv){ return cfg(inv,'userAgent'); }
function miragePrefix(inv){
  var p = String(cfg(inv,'miragePrefix') || DEFAULTS.miragePrefix);
  return '/' + p.replace(/^\/+|\/+$/g,'');
}
function joinName(t, o){ return [t, o].filter(function(x){ return x; }).join(' / '); }

function commonHeaders(inv, referer){
  return {
    'User-Agent': ua(inv),
    'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
    'Accept-Language': 'ru-RU,ru;q=0.9,en;q=0.8',
    'Referer': referer || (siteURL(inv) + '/')
  };
}

function htmlDecode(s){
  if (!s) return '';
  return String(s)
    .replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#039;/g, "'")
    .replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>')
    .replace(/&nbsp;/g, ' ');
}

// ---------- Search ----------

function searchPosts(inv, query){
  if (!query) return [];
  var url = siteURL(inv) + '/';
  http.get(url, { headers: commonHeaders(inv) }); // warm-up PHPSESSID
  var headers = commonHeaders(inv);
  headers['Origin'] = siteURL(inv);
  headers['X-Requested-With'] = 'XMLHttpRequest';
  var res = http.post(url, {
    headers: headers,
    form: { do: 'search', subaction: 'search', story: query }
  });
  console.log('rhs: search', { query: query, status: res && res.status, len: res && res.text ? res.text.length : 0 });
  if (!res || !res.ok) return [];
  return parseSearchResults(res.text);
}

function parseSearchResults(html){
  if (!html) return [];
  var posts = [];
  var seen = {};
  function add(href, slice){
    if (seen[href]) return;
    seen[href] = true;
    var titleMatch = /<a[^>]+class="card__title[^"]*"[^>]*>([\s\S]*?)<\/a>/i.exec(slice || '');
    var rawTitle = titleMatch ? titleMatch[1] : '';
    rawTitle = htmlDecode(rawTitle.replace(/<[^>]+>/g, '')).trim();
    if (!rawTitle){
      var hMatch = /<h[23][^>]*>([\s\S]*?)<\/h[23]>/i.exec(slice || '');
      if (hMatch) rawTitle = htmlDecode(hMatch[1].replace(/<[^>]+>/g, '')).trim();
    }
    var yearMatch = /\b(19|20)\d{2}\b/.exec(slice || '');
    posts.push({
      url: href,
      title: rawTitle || guessTitleFromURL(href),
      year: yearMatch ? parseInt(yearMatch[0], 10) : 0
    });
  }

  var cardRe = /<a[^>]+class="card__img[^"]*"[^>]+href="(https?:\/\/[^"]+\/[0-9]+-[^"]+\.html)"[^>]*>/g;
  var m;
  while ((m = cardRe.exec(html)) !== null){
    add(m[1], html.substring(m.index, Math.min(html.length, m.index + 2500)));
  }
  if (posts.length === 0){
    var anyRe = /<a([^>]*?)href="(https?:\/\/[^"]+\/[0-9]+-[^"]+\.html)"([^>]*)>/g;
    while ((m = anyRe.exec(html)) !== null){
      var tag = (m[1] || '') + (m[3] || '');
      if (/season-link|class="[^"]*\b(promo|sticker|side|widget|nav)[^"]*"/i.test(tag)) continue;
      add(m[2], html.substring(m.index, Math.min(html.length, m.index + 2500)));
    }
  }
  return posts;
}

function guessTitleFromURL(href){
  var m = /\/[0-9]+-([^.]+)\.html/.exec(href);
  return m ? m[1].replace(/-/g, ' ') : '';
}

function matchPost(posts, title, origTitle, year, tol){
  if (!posts.length) return null;
  var nT = util.normalizeTitle(title || '');
  var nO = util.normalizeTitle(origTitle || '');
  var best = null, bestYearDelta = Infinity;
  for (var i = 0; i < posts.length; i++){
    var p = posts[i];
    var nP = util.normalizeTitle(p.title || '');
    var titleHit = (nT && (nP === nT || nP.indexOf(nT) >= 0)) ||
                   (nO && (nP === nO || nP.indexOf(nO) >= 0));
    if (!titleHit) continue;
    if (year > 0){
      if (p.year > 0 && Math.abs(p.year - year) > tol) continue;
      if (p.year === year) return p;
      var d = p.year > 0 ? Math.abs(p.year - year) : tol + 1;
      if (d < bestYearDelta){ best = p; bestYearDelta = d; }
    } else if (!best){
      best = p;
    }
  }
  return best || posts[0];
}

// ---------- Page → stloadi iframe ----------

function fetchPage(inv, pageURL){
  var res = http.get(pageURL, { headers: commonHeaders(inv, siteURL(inv) + '/') });
  return res.ok ? res.text : '';
}

// stloadi iframe shape: <iframe src="https://<sub>-as.stloadi.live/?token_movie=<TM>&token=<TK>">
function pickStloadiIframe(html){
  if (!html) return null;
  var re = /<iframe[^>]+src=["'](https?:\/\/[a-z0-9.-]*stloadi\.live\/\?[^"']+)["']/i;
  var m = re.exec(html);
  if (!m) return null;
  var url = htmlDecode(m[1]);
  var tm = /[?&]token_movie=([A-Za-z0-9._-]+)/.exec(url);
  var tk = /[?&]token=([A-Za-z0-9._-]+)/.exec(url);
  return {
    url: url,
    tokenMovie: tm ? tm[1] : '',
    token:      tk ? tk[1] : ''
  };
}

// ---------- Mirage delegation ----------

function buildQS(obj){
  var parts = [];
  for (var k in obj){
    if (!obj.hasOwnProperty(k)) continue;
    var v = obj[k];
    if (v === undefined || v === null || v === '') continue;
    parts.push(util.urlencode(k) + '=' + util.urlencode(String(v)));
  }
  return parts.join('&');
}

// Build the URL Lampa should call to get the actual stream. Mirage handler
// accepts orid=<token_movie> and resolves via api.apbugall.org → CDN.
function mirageURL(inv, params){
  return inv.host + miragePrefix(inv) + '?' + buildQS(params);
}

// ---------- Lampa response ----------

function pickQuery(inv){
  var t = (inv.query.title || '').trim();
  var o = (inv.query.original_title || '').trim();
  return t || o;
}
function pickYear(inv){
  var y = parseInt(inv.query.year || inv.query.serial_year || '0', 10);
  return isNaN(y) ? 0 : y;
}

function findBestPost(inv){
  var query = pickQuery(inv);
  if (!query) return null;
  var posts = searchPosts(inv, query);
  if (!posts.length){
    var orig = (inv.query.original_title || '').trim();
    if (orig && orig !== query) posts = searchPosts(inv, orig);
  }
  var year = pickYear(inv);
  var tol = parseInt(cfg(inv, 'yearTolerance'), 10);
  if (isNaN(tol)) tol = 1;
  return matchPost(posts, query, (inv.query.original_title || '').trim(), year, tol);
}

function handleChecksearch(inv){
  // Cheap probe: search hit + presence of any /<id>-<slug>.html card means
  // RHS likely has it. Real iframe extraction happens only when the user
  // actually opens the source.
  var post = findBestPost(inv);
  if (!post) return { rch: false };
  return { rch: true, type: 'movie', quality: manifest.quality || 'FHD' };
}

function handle(inv){
  if (inv.checksearch) return handleChecksearch(inv);

  var post = findBestPost(inv);
  if (!post){ console.warn('rhs: no post for query'); return { type: 'movie', data: [] }; }

  var pageHTML = fetchPage(inv, post.url);
  if (!pageHTML){ console.warn('rhs: page fetch failed', post.url); return { type: 'movie', data: [] }; }

  var iframe = pickStloadiIframe(pageHTML);
  console.log('rhs: stloadi iframe', iframe && { tokenMovie: iframe.tokenMovie, token: iframe.token });
  if (!iframe || !iframe.tokenMovie){
    console.warn('rhs: no stloadi iframe on page', post.url);
    return { type: 'movie', data: [] };
  }

  var name = (inv.query.title || post.title || '').trim();
  var orig = (inv.query.original_title || '').trim();
  var year = pickYear(inv);
  var displayName = joinName(name, orig);
  var quality = manifest.quality || 'FHD';
  var serialQS = String(inv.query.serial || '') === '1';

  // Hand off to /lite/mirage. Mirage takes (orid|imdb_id|kinopoisk_id) and
  // resolves the playable manifest via its own browser pipeline.
  // method:"call" — Lampa fetches the URL expecting a JSON play response,
  // which is exactly what Mirage's /video endpoint returns.
  var qs = {
    orid: iframe.tokenMovie,
    title: name,
    original_title: orig,
    year: year || '',
    serial: serialQS ? 1 : 0,
    rjson: 'true'
  };
  // Forward identifiers if Lampa gave them — Mirage may use them as a
  // secondary key when its API can't find the title by orid alone.
  if (inv.query.imdb_id) qs.imdb_id = inv.query.imdb_id;
  if (inv.query.kinopoisk_id) qs.kinopoisk_id = inv.query.kinopoisk_id;

  var callURL = mirageURL(inv, qs);

  return {
    type: 'movie',
    data: [{
      method: 'call',
      url: callURL,
      stream: callURL,
      name: displayName || 'Red Head Sound',
      title: displayName,
      quality: quality,
      orid: iframe.tokenMovie,
      tokenIframe: iframe.token
    }]
  };
}
