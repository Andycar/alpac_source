/* Filmix «прямой CDN» для Lampa (архитектура B). Инъектится в player-inner при direct_lampa=true.
 *
 * Оборачивает Lampa.Player.play ОДИН раз: для элементов с el.fxdirect (только filmix) браузер сам
 * минтит IP-привязанный hash, берёт video-links, находит прямой m3u8 для нужной озвучки+качества,
 * дописывает hash в EXT-X-MAP (HDR fMP4) через blob и играет CDN НАПРЯМУЮ — без нашего сервера и
 * эксита, поэтому без буферизации. Любой сбой (гео-блок, старый webview, нет CORS) → тихий откат на
 * исходную (проксированную) ссылку. ES5 + XHR ради старых Tizen/webOS. */
(function () {
  try {
    if (typeof Lampa === 'undefined' || !Lampa.Player || Lampa.Player.__fxWrapped) return;
    Lampa.Player.__fxWrapped = true;
    var _play = Lampa.Player.play.bind(Lampa.Player);

    function xhr(url, headers, cb) {
      try {
        var x = new XMLHttpRequest();
        x.open('GET', url, true);
        x.timeout = 12000;
        if (headers) for (var k in headers) { try { x.setRequestHeader(k, headers[k]); } catch (e) {} }
        x.onreadystatechange = function () {
          if (x.readyState === 4) cb(x.status >= 200 && x.status < 300 ? x.responseText : null);
        };
        x.ontimeout = function () { cb(null); };
        x.onerror = function () { cb(null); };
        x.send();
      } catch (e) { cb(null); }
    }

    function sameVoice(a, b) {
      if (!a || !b) return false;
      return String(a).trim().toLowerCase() === String(b).trim().toLowerCase();
    }

    // Файлы озвучки: фильм = массив {voiceover,files}; сериал = голос→season-N→episodes.eN.files.
    function pickFiles(data, d) {
      try {
        if (Object.prototype.toString.call(data) === '[object Array]') {
          for (var i = 0; i < data.length; i++) if (sameVoice(data[i].voiceover, d.voice)) return data[i].files;
          return data[0] ? data[0].files : null;
        }
        for (var v in data) {
          if (d.voice && !sameVoice(v, d.voice)) continue;
          var seasons = data[v];
          for (var sk in seasons) {
            var so = seasons[sk];
            if (d.season && so.season != d.season) continue;
            var eps = so.episodes || {};
            var ep = eps['e' + (d.episode || 1)] || eps['' + (d.episode || 1)];
            if (ep && ep.files) return ep.files;
          }
        }
      } catch (e) {}
      return null;
    }

    // Прямой m3u8 нужного качества (want — число вроде 1080; иначе лучшее доступное).
    function pickUrl(files, want) {
      var best = null, bestQ = -1;
      try {
        for (var i = 0; i < files.length; i++) {
          var q = files[i].quality | 0, u = files[i].url;
          if (!u) continue;
          if (want && q === want) return u;
          if (q > bestQ) { bestQ = q; best = u; }
        }
      } catch (e) {}
      return best;
    }

    // Текущее выбранное качество: el.url совпадает с одной из el.quality → её ключ ("1080p").
    function currentWant(el) {
      try { if (el.quality) for (var q in el.quality) if (el.quality[q] === el.url) return parseInt(q); } catch (e) {}
      return 0;
    }

    // Дописать hash из URL плейлиста в EXT-X-MAP init (HDR), вернуть blob-URL. Сегменты в манифесте
    // абсолютные (с hash), поэтому blob играется без base-url. Не смогли — вернуть исходный m3u8.
    function toBlob(text, playlistUrl) {
      try {
        var m = playlistUrl.match(/[?&]hash=([^&]+)/);
        if (m) {
          var hash = m[1];
          text = text.replace(/(#EXT-X-MAP:[^\n]*?URI=")([^"]+)(")/g, function (_all, a, uri, c) {
            if (uri.indexOf('hash=') !== -1) return a + uri + c;
            return a + uri + (uri.indexOf('?') === -1 ? '?' : '&') + 'hash=' + hash + c;
          });
        }
        return URL.createObjectURL(new Blob([text], { type: 'application/vnd.apple.mpegurl' }));
      } catch (e) { return null; }
    }

    function resolve(el, done) {
      var d = el.fxdirect, api = d.api_host;
      xhr(api + d.request_token, null, function (tok) {
        var hash = '';
        try { hash = JSON.parse(tok || '{}').token || ''; } catch (e) {}
        if (!hash) return done();
        var h = {}; h[d.hash_header || 'hash'] = hash;
        xhr(api + d.video_links, h, function (body) {
          if (!body) return done();
          var data; try { data = JSON.parse(body); } catch (e) { return done(); }
          if (!data || data.message) return done();          // гео/РКН-блок → откат
          var files = pickFiles(data, d);
          if (!files) return done();
          var url = pickUrl(files, currentWant(el));
          if (!url) return done();
          xhr(url, null, function (man) {                    // префетч манифеста → blob (правит HDR)
            if (man) { var b = toBlob(man, url); el.url = b || url; }
            else el.url = url;                               // не префетчнули — прямая ссылка как есть
            done();
          });
        });
      });
    }

    Lampa.Player.play = function (el) {
      try {
        if (el && el.fxdirect && el.url) { resolve(el, function () { _play(el); }); return; }
      } catch (e) {}
      _play(el);
    };
  } catch (e) {}
})();
