// auth_gate_plugin.js — standalone Lampa plugin for silent boot auth-sync.
// Loaded as a regular plugin via plugins_add — works on Android/TV/Web.
//
// Source-level gating mode: this does NOT block the app. The catalog stays
// browsable; the source list is gated server-side (online.js renders the QR
// auth card on /lite/*). Here we silently recover the session from the durable
// localStorage token, bind the device, and handle cub-clone uid conflicts.
(function(){
  'use strict';

  var LS_KEY = 'lampac_unic_id';
  var ORIGIN = '{localhost}';

  var _cachedUID = '';
  function getUID(){
    if(_cachedUID) return _cachedUID;
    // 1. Try Lampa.Storage
    try {
      var v = Lampa.Storage.get(LS_KEY, '');
      if(v){ _cachedUID = v; ensureUIDStored(v); return v; }
    } catch(e){}
    // 2. Try raw localStorage (Samsung/Tizen fallback)
    try {
      var raw = localStorage.getItem(LS_KEY);
      if(raw){
        try { var parsed = JSON.parse(raw); if(typeof parsed==='string' && parsed){ _cachedUID = parsed; ensureUIDStored(parsed); return parsed; } }
        catch(e){ if(typeof raw==='string' && raw){ _cachedUID = raw; ensureUIDStored(raw); return raw; } }
      }
    } catch(e){}
    // 3. Try dedicated backup key (Samsung double-safety)
    try {
      var backup = localStorage.getItem('lampac_uid_backup');
      if(backup){ _cachedUID = backup; ensureUIDStored(backup); return backup; }
    } catch(e){}
    // 4. Generate new
    var uid = Math.random().toString(36).substr(2,8);
    _cachedUID = uid;
    ensureUIDStored(uid);
    return uid;
  }
  function ensureUIDStored(uid){
    try { Lampa.Storage.set(LS_KEY, uid); } catch(e){}
    try { localStorage.setItem(LS_KEY, JSON.stringify(uid)); } catch(e){}
    try { localStorage.setItem('lampac_uid_backup', uid); } catch(e){}
  }
  // Force-regenerate the device UID. Used when the server signals
  // uid_conflict — typically after a cub-backup restore cloned this UID
  // from another device's account.
  function regenUID(){
    var n = Math.random().toString(36).substr(2,8);
    _cachedUID = n;
    ensureUIDStored(n);
    return n;
  }

  function getCookie(name){
    try {
      var m = document.cookie.match(new RegExp('(?:^|;\\s*)'+name+'=([^;]*)'));
      return m ? decodeURIComponent(m[1]) : '';
    } catch(e){ return ''; }
  }

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

  // FNV-1a — shared by the stable-fp builder below.
  function fnv1a(s){
    var h = 0x811c9dc5;
    for (var i = 0; i < s.length; i++){ h ^= s.charCodeAt(i); h = Math.imul(h, 0x01000193); }
    return (h >>> 0).toString(16);
  }

  // ── Native device id (best-effort) ─────────────────────────────────────
  // The single thing that survives a FULL client wipe (localStorage + cookies,
  // which kills UID and the token anchor) is the physical device identity.
  // webOS exposes LGUDID via a luna service (async), Tizen exposes DUID (sync),
  // hybrid Android builds may expose a bridge. All optional; absent → ''.
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
    if (webosLGUDID(cb)) return; // async — cb fires from luna callback
    cb('');
  }

  // Coarse, drift-resistant fingerprint: only attributes that DON'T change on
  // a fixed TV (geometry, cores, platform, timezone, UA sans version numbers),
  // folded with the native device id. Async because the native id may be.
  function resolveStableFP(cb){
    var p = [];
    p.push((screen.width||0)+'x'+(screen.height||0));
    p.push(screen.colorDepth||0);
    p.push(window.devicePixelRatio||1);
    p.push(navigator.hardwareConcurrency||0);
    p.push(navigator.deviceMemory||0);
    p.push(navigator.platform||'');
    p.push(navigator.maxTouchPoints||0);
    try { p.push(Intl.DateTimeFormat().resolvedOptions().timeZone||''); } catch(e){ p.push(''); }
    p.push((navigator.userAgent||'').replace(/[\d.]+/g,'').slice(0,120)); // strip version numbers
    var base = p.join('|');
    var done = false;
    var finish = function(nid){ if(done) return; done = true; cb(fnv1a(base + '|' + (nid||''))); };
    var t = setTimeout(function(){ finish(''); }, 1200); // don't hang boot on a slow luna call
    getNativeId(function(nid){ clearTimeout(t); finish(nid); });
  }

  // Bind device uid+fp+sfp to the token; regenerate once on a uid_conflict.
  function bindDevice(tok, useUid, fp, sfp){
    if(!tok || (!fp && !sfp)) return;
    var q = function(u){ return ORIGIN+'/tg/auth/bind-device?token='+encodeURIComponent(tok)+'&uid='+encodeURIComponent(u)+(fp?'&fp='+encodeURIComponent(fp):'')+(sfp?'&sfp='+encodeURIComponent(sfp):''); };
    try {
      var bx = new XMLHttpRequest();
      bx.open('GET', q(useUid), true);
      bx.onload = function(){
        try {
          var rr = JSON.parse(bx.responseText);
          if(rr && rr.uid_conflict){
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

  function run(){
    var uid = getUID();
    var fp = quickFP();
    resolveStableFP(function(sfp){
      var url = ORIGIN + '/tg/auth/status?uid=' + encodeURIComponent(uid);
      if(fp) url += '&fp=' + encodeURIComponent(fp);
      if(sfp) url += '&sfp=' + encodeURIComponent(sfp);

      // CUB session token (Lampa's `account` storage) — the longest-lived
      // anchor. On authorized boots the server passively links it to the
      // account; on a wiped device a fresh CUB login alone restores the binding.
      try {
        var acc = JSON.parse(localStorage.getItem('account') || '{}');
        if(acc && typeof acc.token === 'string' && acc.token) url += '&cub=' + encodeURIComponent(acc.token);
      } catch(e){}

      // Cookie jar is wiped on Tizen/Samsung app restart while localStorage
      // survives — fall back to the durable token anchor so the boot sync can
      // re-issue cookies. Server re-issues cookies when it gets a valid ?token=.
      var token = getCookie('lampac_token') || getCookie('alpac_token');
      if(!token){ try { token = localStorage.getItem('lampac_auth_token') || ''; } catch(e){} }
      if(!token){ try { token = Lampa.Storage.get('lampac_token','') || Lampa.Storage.get('alpac_token','') || Lampa.Storage.get('lampac_auth_token',''); } catch(e){} }
      if(token) url += '&token=' + encodeURIComponent(token);

      var xhr = new XMLHttpRequest();
      xhr.open('GET', url, true);
      xhr.timeout = 8000;
      xhr.onload = function(){
        if(xhr.status !== 200) return;
        try {
          var r = JSON.parse(xhr.responseText);
          if(r && r.uid_conflict){
            // UID was cloned (cub backup or otherwise) — generate fresh.
            uid = regenUID();
            if(r.authorized && r.token){
              try { localStorage.setItem('lampac_auth_token', r.token); } catch(e){}
              bindDevice(r.token, uid, fp, sfp);
            }
            return;
          }
          if(r && r.authorized){
            // Persist token to the durable anchor (survives Tizen cookie wipe)
            // and bind the device (uid + both fingerprints) for recovery.
            if(r.token){ try { localStorage.setItem('lampac_auth_token', r.token); } catch(e){} }
            bindDevice(r.token, uid, fp, sfp);
          }
          // Not authorized — do nothing. Source list is gated server-side and
          // online.js shows the QR auth card on /lite/*.
        } catch(e){}
      };
      xhr.onerror = function(){};
      xhr.ontimeout = function(){};
      xhr.send();
    });
  }

  // Run immediately when script loads — don't wait for app:ready
  // because plugin scripts load AFTER app:ready.
  if(document.body){
    run();
  } else {
    document.addEventListener('DOMContentLoaded', run);
  }
})();
