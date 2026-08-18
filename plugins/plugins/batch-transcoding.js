// Batch Transcoding — pre-transcode series episodes for instant switching.
// Injected into online.js via {batch-transcoding} template variable.
// Works with ExoPlayer and any player — replaces episode URLs with HLS playlists.
(function(){
  if (!{batchTranscodingEnabled}) return;

  var _batchId = null;
  var _heartbeatTimer = null;

  function startBatch(element, playlist) {
    if (!element.isonline || !playlist || playlist.length < 2) return Promise.resolve(false);

    var episodes = [];
    for (var i = 0; i < Math.min(playlist.length, 20); i++) {
      var ep = playlist[i];
      var url = typeof ep.url === 'string' ? ep.url : '';
      if (!url || /\.(m3u8|mpd)(\?|$)/i.test(url)) continue;
      episodes.push({ url: url, title: ep.title || '', audioIndex: 0 });
    }

    if (episodes.length < 2) return Promise.resolve(false);

    return fetch('/transcoding/batch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ episodes: episodes, subtitles: true })
    })
    .then(function(resp) {
      if (!resp.ok) return false;
      return resp.json();
    })
    .then(function(data) {
      if (!data || !data.batchId) return false;

      _batchId = data.batchId;

      // Replace URLs in playlist with transcoded HLS playlists where available
      if (data.episodes) {
        data.episodes.forEach(function(ep) {
          if (ep.playlistUrl && ep.index < playlist.length) {
            // For ExoPlayer: replace URL with HLS playlist (method:"play")
            playlist[ep.index].url = ep.playlistUrl;
            // Remove quality map — transcoded stream is single quality
            delete playlist[ep.index].quality;
            delete playlist[ep.index].qualitys;
          }
        });

        // Also update main element if it's the first episode
        var currentIdx = 0;
        for (var j = 0; j < playlist.length; j++) {
          if (playlist[j].url === element.url || playlist[j].title === element.title) {
            currentIdx = j;
            break;
          }
        }
        if (data.episodes[currentIdx] && data.episodes[currentIdx].playlistUrl) {
          element.url = data.episodes[currentIdx].playlistUrl;
          delete element.quality;
          delete element.qualitys;
        }
      }

      // Start heartbeat
      stopBatch();
      _heartbeatTimer = setInterval(function() {
        if (_batchId) {
          fetch('/transcoding/batch/' + _batchId + '/heartbeat').catch(function(){});
        }
      }, 10000);

      // Listen for episode changes to notify server
      if (typeof Lampa !== 'undefined' && Lampa.Player && Lampa.Player.listener) {
        Lampa.Player.listener.follow('playlist', function(data) {
          if (_batchId && data && typeof data.position === 'number') {
            fetch('/transcoding/batch/' + _batchId + '/episode/' + data.position, {
              method: 'POST'
            }).then(function(resp) {
              if (!resp.ok) return;
              return resp.json();
            }).then(function(info) {
              // If episode has a playlistUrl, the batch has it ready
              if (info && info.playlistUrl) {
                console.log('[BatchTC] Episode ' + data.position + ' ready:', info.state);
              }
            }).catch(function(){});
          }
        });

        Lampa.Player.listener.follow('destroy', function() {
          stopBatch();
        });
      }

      console.log('[BatchTC] Batch started:', _batchId, 'episodes:', episodes.length);
      return true;
    })
    .catch(function(e) {
      console.warn('[BatchTC] Failed to start batch:', e);
      return false;
    });
  }

  function stopBatch() {
    if (_heartbeatTimer) {
      clearInterval(_heartbeatTimer);
      _heartbeatTimer = null;
    }
    if (_batchId) {
      fetch('/transcoding/batch/' + _batchId + '/stop').catch(function(){});
      _batchId = null;
    }
  }

  // Expose globally for player-inner.js integration
  window.__batchTranscoding = {
    start: startBatch,
    stop: stopBatch,
    active: function() { return !!_batchId; }
  };
})();
