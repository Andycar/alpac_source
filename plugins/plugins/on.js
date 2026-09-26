(function () {
    'use strict';

    // Two-stage bootstrap.
    //
    // The plugin URL users put into Lampa is usually the bare /on.js. The
    // server can only tell who is asking by COOKIES — and TV WebViews (LG
    // webOS in particular) wipe the cookie jar on every app relaunch, so the
    // bare serving comes back unauthenticated: limited plugin set + auth
    // gate, even though a perfectly valid token sits in localStorage. The
    // durable token can't travel with a <script src> request as a header —
    // but it CAN travel as a path segment. So when this copy was served
    // without a token and the client holds one, we swap ourselves for the
    // tokenized bundle /on/js/<token> instead of loading the baked list.
    //
    // Server-baked flags (placeholders are replaced with ReplaceAll at serve
    // time — never write their brace-forms in comments, they'd be expanded):
    //   FULL         — true: served with a VALID token, the baked list is
    //                  the full set (tokenized sub-URLs); false: limited set.
    //   SELF_UPGRADE — true only on the bare /on.js serving. Tokenized
    //                  servings bake false so a dead path token can't
    //                  bounce the client back into another upgrade loop.
    var FULL = {full_bundle};
    var SELF_UPGRADE = {self_upgrade};
    var HOST = '{localhost}';

    // Disable LGBT content filter (Lampa beta feature) — prevents lgbt.forEach crash
    if (!window.lampa_settings) window.lampa_settings = {};
    if (!window.lampa_settings.disable_features) window.lampa_settings.disable_features = {};
    window.lampa_settings.disable_features.lgbt = true;

    // Durable token, most→least reliable for TV WebViews: cookies die on
    // relaunch, localStorage survives. Lampa.Storage isn't ready this early,
    // but its keys live in raw localStorage (JSON-wrapped) — read them too.
    function durableToken() {
        try {
            var m = document.cookie.match(/(?:^|;\s*)(?:alpac_token|lampac_token)=([^;]*)/);
            if (m && m[1]) return decodeURIComponent(m[1]);
        } catch (e) {}
        try { var v = localStorage.getItem('lampac_auth_token'); if (v) return v; } catch (e) {}
        var names = ['alpac_token', 'lampac_token'];
        for (var i = 0; i < names.length; i++) {
            try {
                var raw = localStorage.getItem(names[i]);
                if (!raw) continue;
                try { var p = JSON.parse(raw); if (typeof p === 'string' && p) return p; }
                catch (e) { if (raw) return raw; }
            } catch (e) {}
        }
        return '';
    }

    function putScript(src, onerror) {
        var s = document.createElement('script');
        s.src = src;
        if (onerror) s.onerror = onerror;
        (document.head || document.documentElement).appendChild(s);
    }

    // Upgrade hook: fetches the tokenized full bundle once. Called by the
    // bootstrap below (durable token on boot) and by the auth-gate IIFE /
    // online.js auth card right after a successful recovery or login — the
    // recovery lands AFTER a limited copy was already served, so without
    // this the session would run without the full plugin set until the next
    // app restart.
    if (!window.alcopac_upgrade) {
        window.alcopac_upgrade = function (tok) {
            if (!tok || window.alcopac_onjs_full) return;
            if (window.alcopac_upgrading === tok) return; // same dead token — don't loop
            window.alcopac_upgrading = tok;
            putScript(HOST + '/on/js/' + encodeURIComponent(tok), function () {
                // Network failure fetching the bundle — fall back to whatever
                // baked list the page already has via the pending loader below.
                if (window.alcopac_upgrade_fallback) window.alcopac_upgrade_fallback();
            });
        };
    }

    // Bundle entries: {k: plugin key, o: 1 = optional, u: script URL}.
    // Mandatory entries (o:0 — online, account) always load. Optional ones
    // load only when the user enabled them in Settings → Аккаунт; the
    // toggles are standard Lampa trigger params stored as
    // alcopac_plug_<key> in Lampa.Storage. account.js renders them from
    // window.alcopac_plugin_list.
    var LIST = [{plugins}];

    function optionalEnabled(key) {
        try {
            var v = Lampa.Storage.get('alcopac_plug_' + key, false);
            return v === true || v === 'true';
        } catch (e) { return false; }
    }

    function loadBundle() {
        var timer = setInterval(function () {
            if (typeof Lampa !== 'undefined') {
                clearInterval(timer);

                var unic_id = Lampa.Storage.get('lampac_unic_id', '');
                if (!unic_id) {
                    unic_id = (function(){
                      function un(raw){ if(!raw) return ''; try{ var p=JSON.parse(raw); if(typeof p==='string'&&p) return p; }catch(e){ if(typeof raw==='string'&&raw) return raw; } return ''; }
                      try{ var b=un(localStorage.getItem('lampac_uid_backup')); if(b) return b; }catch(e){}
                      try{ var m=document.cookie.match(/(?:^|;\s*)alpac_uid=([^;]*)/); if(m&&m[1]) return decodeURIComponent(m[1]); }catch(e){}
                      var u='';
                      try{ var c=window.crypto||window.msCrypto; if(c&&c.getRandomValues){ var a=new Uint8Array(6); c.getRandomValues(a); for(var i=0;i<a.length;i++) u+=('0'+a[i].toString(16)).slice(-2); } }catch(e){}
                      if(u.length<12) u=(Date.now().toString(36)+Math.random().toString(36).slice(2)+Math.random().toString(36).slice(2)).slice(0,12);
                      try{ localStorage.setItem('lampac_uid_backup',u); }catch(e){}
                      return u.toLowerCase();
                    })();
                    Lampa.Storage.set('lampac_unic_id', unic_id);
                }

                // Mirror the durable token into Lampa.Storage BEFORE any
                // plugin loads: syncpro/iptv2/account read alpac_token /
                // lampac_token from there, and on a stock Lampa install
                // nothing else writes those keys — after webOS wipes the
                // cookie jar those plugins would boot unauthenticated
                // («не вошёл») while the token sits in localStorage.
                var mtok = durableToken();
                if (mtok) {
                    try { if (Lampa.Storage.get('alpac_token', '') !== mtok) Lampa.Storage.set('alpac_token', mtok); } catch (e) {}
                    try { if (Lampa.Storage.get('lampac_token', '') !== mtok) Lampa.Storage.set('lampac_token', mtok); } catch (e) {}
                }

                window.alcopac_plugin_list = LIST;
                if (!window.alcopac_loaded_plugins) window.alcopac_loaded_plugins = {};
                var urls = [];
                for (var i = 0; i < LIST.length; i++) {
                    var p = LIST[i];
                    if (!p) continue;
                    if (typeof p === 'string') { urls.push(p); continue; } // legacy format safety
                    if (window.alcopac_loaded_plugins[p.k]) continue;      // lite copy already loaded it
                    if (p.o && !optionalEnabled(p.k)) continue;
                    window.alcopac_loaded_plugins[p.k] = true;
                    urls.push(p.u);
                }
                if (urls.length) Lampa.Utils.putScriptAsync(urls, function () {});
            }
        }, 200);
    }

    if (FULL) {
        if (window.alcopac_onjs_full) return;
        window.alcopac_onjs_full = true;
        window.alcopac_onjs = true; // block any later limited copy
        window.alcopac = true;
        loadBundle();
        return;
    }

    // ---- limited / bare copy ----
    if (window.alcopac_onjs) return;
    window.alcopac = true;

    if (SELF_UPGRADE) {
        var tok = durableToken();
        if (tok) {
            // Let the tokenized bundle take over. If the token turns out dead,
            // the server answers with a gate-wrapped limited copy (self_upgrade
            // = false) which loads the baked list and runs recovery. Keep the
            // baked list of THIS copy reachable as a network-error fallback.
            window.alcopac_upgrade_fallback = function () {
                if (window.alcopac_onjs) return;
                window.alcopac_onjs = true;
                loadBundle();
            };
            window.alcopac_upgrade(tok);
            return;
        }
    }

    window.alcopac_onjs = true;
    loadBundle();
})();
