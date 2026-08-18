(function () {
    'use strict';
	
    var unic_id = Lampa.Storage.get('lampac_unic_id', '');
    if (!unic_id) {
      unic_id = Lampa.Utils.uid(8).toLowerCase();
      Lampa.Storage.set('lampac_unic_id', unic_id);
    }

    Lampa.Storage.set('torrserver_url','{localhost}/ts');
    // Lampa keeps TWO TorrServer slots and plays through whichever
    // `torrserver_use_link` selects:
    //   ip() => use_link == 'two' ? torrserver_url_two : torrserver_url
    // We only ever fill slot one, so if the user (or an older config) left the
    // switch on 'two', every playback request goes to THEIR url_two instead of
    // ours — while the settings status dot, which probes the `torrserver_url`
    // field directly, still shows green. Pin the switch so the config we push
    // is the config that actually gets used.
    Lampa.Storage.set('torrserver_use_link','one');
    Lampa.Storage.set('torrserver_auth','true');
    // Login MUST be the device uid: the server resolves Basic "uid:ts" back
    // to the user token via the device binding (requestUserTokenFromAny), so
    // clients whose WebView drops cookies still authenticate /ts mutations.
    // This is the ONLY credential that survives a cross-origin request
    // (lampa.mx → our host): Lampa sends no cookies there.
    // account_email is a CUB identity the server can't resolve — don't use it.
    Lampa.Storage.set('torrserver_login', unic_id || 'ts');
    Lampa.Storage.set('torrserver_password','ts');
	
})();