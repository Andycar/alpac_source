// auth_gate.js — silent boot auth-sync (source-level gating mode).
// Inlined into lampainit.js start() — runs after app:ready.
//
// This does NOT block the app. The catalog stays browsable; the source list
// is gated server-side (the gate returns accsdb + a TG code on /lite/*, and
// online.js renders the QR auth card there). At boot we only do the useful
// work silently:
//   - recover the session cookies from the durable localStorage token
//     (Tizen/Samsung wipes cookies on app restart but keeps localStorage),
//   - bind this device (uid + fingerprint) to the token for later recovery,
//   - regenerate the device UID if the server reports a cub-backup clone.

(function authGateRun() {
  // Comprehensive device fingerprint (FNV-1a of hardware + canvas + WebGL + audio).
  function quickFP() {
    try {
      var fp = [];
      fp.push(screen.width+'x'+screen.height+':'+screen.availWidth+'x'+screen.availHeight);
      fp.push(screen.colorDepth||0); fp.push(window.devicePixelRatio||1);
      fp.push(navigator.hardwareConcurrency||0); fp.push(navigator.deviceMemory||0);
      fp.push(navigator.maxTouchPoints||0); fp.push(navigator.platform||'');
      fp.push(navigator.language||''); fp.push(Math.tan(-1e300));
      try { fp.push(Intl.DateTimeFormat().resolvedOptions().timeZone||''); } catch(e){ fp.push(''); }
      try {
        var c = document.createElement('canvas'); c.width=200; c.height=50;
        var ctx = c.getContext('2d'); ctx.textBaseline='top'; ctx.font='14px Arial';
        ctx.fillStyle='#f60'; ctx.fillRect(125,1,62,20);
        ctx.fillStyle='#069'; ctx.fillText('Lampa,fp!',2,15);
        ctx.fillStyle='rgba(102,204,0,0.7)'; ctx.fillText('Lampa,fp!',4,17);
        fp.push(c.toDataURL().slice(-50));
      } catch(e){ fp.push('nc'); }
      try {
        var gl = document.createElement('canvas').getContext('webgl');
        if(gl){ var dbg=gl.getExtension('WEBGL_debug_renderer_info'); fp.push(dbg?gl.getParameter(dbg.UNMASKED_RENDERER_WEBGL):'nr'); fp.push(gl.getParameter(gl.MAX_TEXTURE_SIZE)); }
        else fp.push('ng');
      } catch(e){ fp.push('ng'); }
      try { var ac=new(window.AudioContext||window.webkitAudioContext)(); fp.push(ac.sampleRate); fp.push(ac.destination.maxChannelCount); ac.close(); } catch(e){ fp.push('na'); }
      var h = 0x811c9dc5; var s = fp.join('|||');
      for (var i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 0x01000193); }
      return (h >>> 0).toString(16);
    } catch(e) { return ''; }
  }
  var fp = quickFP();

  // FNV-1a — shared by the stable-fp builder below.
  function fnv1a(s){
    var h = 0x811c9dc5;
    for (var i = 0; i < s.length; i++){ h ^= s.charCodeAt(i); h = Math.imul(h, 0x01000193); }
    return (h >>> 0).toString(16);
  }

  // ── Native device id (best-effort) — the only thing that survives a FULL
  // client wipe (localStorage + cookies). webOS LGUDID (async luna), Tizen
  // DUID (sync), Android bridge. All optional; absent → ''.
  function tizenDUID(){
    try { if (window.webapis && webapis.productinfo && typeof webapis.productinfo.getDuid === 'function') return 'tz:' + webapis.productinfo.getDuid(); } catch(e){}
    return '';
  }
  function androidDID(){
    try { if (window.AndroidJS && typeof AndroidJS.getDeviceId === 'function') return 'ad:' + AndroidJS.getDeviceId(); } catch(e){}
    try { if (window.Android && typeof Android.getDeviceId === 'function') return 'ad:' + Android.getDeviceId(); } catch(e){}
    return '';
  }
  function webosLGUDID(cb){
    try {
      if (window.webOSDev && typeof webOSDev.LGUDID === 'function'){
        webOSDev.LGUDID({ onSuccess:function(r){ cb(r && r.id ? 'lg:'+r.id : ''); }, onFailure:function(){ cb(''); } });
        return true;
      }
    } catch(e){}
    try {
      if (window.PalmServiceBridge){
        var bridge = new PalmServiceBridge();
        bridge.onservicecallback = function(res){
          try { var o = JSON.parse(res); var id = o && o.idList && o.idList[0] && o.idList[0].idValue; cb(id ? 'lg:'+id : ''); }
          catch(e){ cb(''); }
        };
        bridge.call('luna://com.webos.service.sm/deviceid/getIDs', JSON.stringify({ idType:['LGUDID'] }));
        return true;
      }
    } catch(e){}
    return false;
  }
  function getNativeId(cb){
    var sync = tizenDUID() || androidDID();
    if (sync){ cb(sync); return; }
    if (webosLGUDID(cb)) return;
    cb('');
  }
  // Coarse drift-resistant fingerprint: stable hardware attrs + native id.
  function resolveStableFP(cb){
    var p = [];
    p.push((screen.width||0)+'x'+(screen.height||0));
    p.push(screen.colorDepth||0); p.push(window.devicePixelRatio||1);
    p.push(navigator.hardwareConcurrency||0); p.push(navigator.deviceMemory||0);
    p.push(navigator.platform||''); p.push(navigator.maxTouchPoints||0);
    try { p.push(Intl.DateTimeFormat().resolvedOptions().timeZone||''); } catch(e){ p.push(''); }
    p.push((navigator.userAgent||'').replace(/[\d.]+/g,'').slice(0,120));
    var base = p.join('|'), done = false;
    // Without a native id the stable fp is a MODEL signature (every unit of the
    // same TV/PC hashes alike — one value was seen on 100 accounts), so it is
    // not sent at all. With one it is prefixed "n:" — the server restores an
    // account only from prefixed values.
    var finish = function(nid){ if(done) return; done = true; cb(nid ? 'n:' + fnv1a(base + '|' + nid) : ''); };
    var t = setTimeout(function(){ finish(''); }, 1200);
    getNativeId(function(nid){ clearTimeout(t); finish(nid); });
  }

  // Generate a fresh device UID — used when server signals uid_conflict
  // (cub backup cloned the UID from another device) or on first install.
  //
  // ★Берём криптослучайные 48 бит вместо Lampa.Utils.uid(8): тот построен на
  // Math.random и даёт 8 символов, а на прод-данных 20.09.2026 нашлось 88
  // значений, поделённых 329 аккаунтами (одно — 70 аккаунтами, от iPhone до
  // Hisense). Случайным совпадением это быть не может: на 26 тысяч устройств
  // ожидаемое число пар — доли единицы. Значит, идентификатор приезжает
  // скопированным (чужой бэкап CUB, мод с зашитым значением) или Math.random
  // на части прошивок отдаёт одно и то же при холодном старте. Длина не
  // проверяется нигде — ни на сервере, ни в плагинах, поэтому удлинение
  // безопасно, а в админке такой uid просто показывается сокращённым.
  function regenUID() {
    var n = '';
    try {
      var c = window.crypto || window.msCrypto;
      if (c && c.getRandomValues) {
        var a = new Uint8Array(6);
        c.getRandomValues(a);
        for (var i = 0; i < a.length; i++) n += ('0' + a[i].toString(16)).slice(-2);
      }
    } catch(e) {}
    if (n.length < 12) {
      // Запасной путь для движков без crypto: время + два разных Math.random.
      // Хуже криптослучайного, но уже не повторяется на одинаковых прошивках.
      n = (Date.now().toString(36) + Math.random().toString(36).slice(2) +
           Math.random().toString(36).slice(2)).slice(0, 12);
    }
    n = n.toLowerCase();
    Lampa.Storage.set('lampac_unic_id', n);
    try { localStorage.setItem('lampac_uid_backup', n); } catch(e){}
    return n;
  }

  var uid = Lampa.Storage.get('lampac_unic_id', '');
  if (!uid) {
    // Try localStorage backup (Samsung/Tizen resilience)
    try {
      var backup = localStorage.getItem('lampac_uid_backup');
      if (backup) uid = backup;
    } catch(e){}
  }
  if (!uid) {
    uid = regenUID();
  } else {
    // Store in all locations for stability
    Lampa.Storage.set('lampac_unic_id', uid);
    try { localStorage.setItem('lampac_uid_backup', uid); } catch(e){}
  }

  // Bind device uid+fp+sfp to the token so it can be recovered after a
  // cache/cookie clear. Handles a uid_conflict by regenerating once.
  function bindDevice(tok, useUid, sfp) {
    if (!tok || (!fp && !sfp)) return;
    var q = function(u){ return '{localhost}/tg/auth/bind-device?token=' + encodeURIComponent(tok) + '&uid=' + encodeURIComponent(u) + (fp?'&fp=' + encodeURIComponent(fp):'') + (sfp?'&sfp=' + encodeURIComponent(sfp):''); };
    try {
      var bx = new XMLHttpRequest();
      bx.open('GET', q(useUid), true);
      bx.onload = function() {
        try {
          var rr = JSON.parse(bx.responseText);
          if (rr && rr.uid_conflict) {
            var n = regenUID();
            var bx2 = new XMLHttpRequest();
            bx2.open('GET', q(n), true);
            bx2.send();
          }
        } catch(e){}
      };
      bx.send();
    } catch(e){}
  }

  resolveStableFP(function(sfp) {
    var url = '{localhost}/tg/auth/status?uid=' + encodeURIComponent(uid);
    if (fp) url += '&fp=' + encodeURIComponent(fp);
    if (sfp) url += '&sfp=' + encodeURIComponent(sfp);

    // CUB session token (Lampa's `account` storage) — the longest-lived
    // anchor. On authorized boots the server passively links it to the
    // account; on a wiped device a fresh CUB login alone restores the binding.
    try {
      var acc = JSON.parse(localStorage.getItem('account') || '{}');
      if (acc && typeof acc.token === 'string' && acc.token) url += '&cub=' + encodeURIComponent(acc.token);
    } catch(e){}

    // Token sources, most→least durable. Tizen/Samsung WebView wipes the cookie
    // jar on app restart but keeps localStorage — a cookie-only read makes auth
    // "слетать" after every relaunch. Fall back to the localStorage anchor
    // (lampac_auth_token, written on login) and Lampa.Storage, then pass it as
    // ?token= so the server re-validates and re-issues the cookies here at boot.
    var token = '';
    try {
      var m = document.cookie.match(/(?:^|;\s*)(?:lampac_token|alpac_token)=([^;]*)/);
      if (m && m[1]) token = decodeURIComponent(m[1]);
    } catch(e) {}
    if (!token) { try { token = localStorage.getItem('lampac_auth_token') || ''; } catch(e){} }
    if (!token) { try { token = Lampa.Storage.get('lampac_token','') || Lampa.Storage.get('alpac_token','') || Lampa.Storage.get('lampac_auth_token',''); } catch(e){} }
    if (token) url += '&token=' + encodeURIComponent(token);

    var net = new Lampa.Reguest();
    net.silent(url, function(result) {
      // Server detected this UID belongs to another device's token (cub backup
      // restore cloned lampac_unic_id) — regenerate locally so the device record
      // stays unique per physical device.
      if (result && result.uid_conflict) {
        uid = regenUID();
        if (result.authorized && result.token) {
          try { localStorage.setItem('lampac_auth_token', result.token); } catch(e){}
          bindDevice(result.token, uid, sfp);
        }
        return;
      }
      if (result && result.authorized) {
        // Persist the token to the durable localStorage anchor (survives Tizen
        // cookie wipe) and bind the device (uid + both fingerprints) for recovery.
        if (result.token) { try { localStorage.setItem('lampac_auth_token', result.token); } catch(e){} }
        bindDevice(result.token, uid, sfp);
        return;
      }
      if (result && result.revoked) {
        // The owner unbound THIS device in the bot. Drop the stored token so we
        // stop presenting it on every boot; the QR card on /lite/* is the way back.
        try { localStorage.removeItem('lampac_auth_token'); } catch(e){}
        try { Lampa.Storage.set('lampac_token',''); Lampa.Storage.set('alpac_token',''); Lampa.Storage.set('lampac_auth_token',''); } catch(e){}
        return;
      }
      // Not authorized — do nothing. The catalog stays open; the source list is
      // gated server-side and online.js shows the QR auth card on /lite/*.
    }, function() {
      // Network error — ignore; nothing is blocked at boot.
    });
  });
})();
