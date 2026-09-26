(function() {
  'use strict';

  window.lampac_version = { major: 0, minor: 0 };

  localStorage.setItem('cub_mirrors', '["mirror-kurwa.men"]');
  
  window.lampa_settings = window.lampa_settings || {};
  window.lampa_settings.torrents_use = true;    // Показывать кнопку торрентов
  window.lampa_settings.demo = false;           // demo off
  window.lampa_settings.read_only = false;      // Режим только для чтения, без кнопок онлайн и расширений
  window.lampa_settings.socket_use = true;      // cub - Использовать сокеты для синхронизации данных
  window.lampa_settings.socket_url = undefined; // cub - Адрес сокета, по умолчанию лампа берет адреса из манифеста
  window.lampa_settings.socket_methods = true;  // cub - Обрабатывать сообщения сокетов
  window.lampa_settings.account_use = true;   // cub - Использовать аккаунты
  window.lampa_settings.account_sync = true;  // cub - Синхронизировать закладки, таймкоды и прочее
  window.lampa_settings.plugins_store = true; // cub магазин расширений
  window.lampa_settings.feed = true;          // cub лента
  window.lampa_settings.iptv = false;         // Является ли приложение IPTV
  window.lampa_settings.white_use = false;    // Белая и пушистая лампа, для одобрения модерации
  window.lampa_settings.push_state = true;    // адрес в url /?card=1241982&media=movie 
  window.lampa_settings.lang_use = true;      // Подключить другие языки интерфейса, по умолчанию только русский и английский
  window.lampa_settings.plugins_use = true;   // Разрешить установку плагинов и расширений
  window.lampa_settings.dcma = false;         // Добавить список блокировки карточек, пример: [{"id":3566556,"cat":"movie"},...]
  window.lampa_settings.services = true;      // Различные сервисы cub в приложении
  window.lampa_settings.youtube = true;       // Подключить YouTube API
  window.lampa_settings.geo = true;           // Определять гео по IP, иначе будет RU
  window.lampa_settings.mirrors = true;       // Использовать поиск зеркал

  window.lampa_settings.disable_features = window.lampa_settings.disable_features || {};
  window.lampa_settings.disable_features.dmca = true;           // шлет нахер правообладателей - on
  window.lampa_settings.disable_features.ads = true;            // Вспомогатиленые сервисы на подписку према
  window.lampa_settings.disable_features.reactions = false;     // cub реакции
  window.lampa_settings.disable_features.discuss = false;       // cub комментарии
  window.lampa_settings.disable_features.ai = false;            // cub AI-поиск
  window.lampa_settings.disable_features.install_proxy = true;  // disable cub tmdb proxy — our tmdbproxy.js handles it
  window.lampa_settings.disable_features.subscribe = false;     // cub подписки
  window.lampa_settings.disable_features.blacklist = false;     // Черный список плагинов
  window.lampa_settings.disable_features.persons = false;       // Подписка на актеров
  window.lampa_settings.disable_features.trailers = false;      // Трейлеры

  window.lampa_settings.developer = window.lampa_settings.developer || {};
  
  
  {lampainit-invc}

  var timer = setInterval(function() {
    if (typeof Lampa !== 'undefined') {
      clearInterval(timer);
	  
      if (lampainit_invc)
        lampainit_invc.appload();

      if ({btn_priority_forced})
        Lampa.Storage.set('full_btn_priority', '{full_btn_priority_hash}');

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

      Lampa.Utils.putScriptAsync(["{localhost}/cubproxy.js", "{localhost}/privateinit.js?account_email=" + encodeURIComponent(Lampa.Storage.get('account_email', '')) + "&uid=" + encodeURIComponent(Lampa.Storage.get('lampac_unic_id', ''))], function() {});

      // ── Suppress CUB account popups ────────────────────────
      // Users authenticate via Telegram bot, not CUB.  Override
      // Account.Modal.account/premium/limited so the full-screen
      // "Еще нет аккаунта?" modal never blocks the UI.
      if (Lampa.Account && Lampa.Account.Modal) {
        Lampa.Account.Modal.account  = function () {};
        Lampa.Account.Modal.premium  = function () {};
        Lampa.Account.Modal.limited  = function () {};
      }
      if (Lampa.Account) {
        Lampa.Account.showNoAccount      = function () {};
        Lampa.Account.showCubPremium     = function () {};
        Lampa.Account.showLimitedAccount = function () {};
      }

      if (window.appready) {
        start();
      }
      else {
        Lampa.Listener.follow('app', function(e) {
          if (e.type == 'ready') {
            start();
          }
        });
      }

	  {pirate_store}
    }
  }, 200);

  function start() {
    if (lampainit_invc) lampainit_invc.appready();

    // Server-managed parser defaults, versioned by the server. Unlike the
    // one-shot lampac_initiale block below, this re-applies whenever the
    // server-side values change (new jachost, changed parser type) — so a
    // client provisioned long ago still picks up config changes, while an
    // unchanged hash leaves the user's storage untouched. Runs BEFORE the
    // plugin-sync reload below, so even the first-ever visit (which ends in
    // location.reload()) leaves the parser configured.
    var parser_defaults = '{parser_defaults_hash}';
    if (Lampa.Storage.get('lampac_parser_defaults', '') !== parser_defaults) {
      Lampa.Storage.set('parser_use', 'true');
      Lampa.Storage.set('jackett_url', '{jachost}');
      Lampa.Storage.set('jackett_key', '{jackett_key}');
      Lampa.Storage.set('parser_torrent_type', 'jackett');
      Lampa.Storage.set('lampac_parser_defaults', parser_defaults);
    }

    // Always sync plugins from server — add missing, remove disabled.
    // After a Lampa version update the client may clear localStorage,
    // wiping the plugin list.  By the time this code runs (app:ready)
    // Lampa has already initialised its plugin system with an empty list,
    // so dynamically loading the scripts is not enough — a full page
    // reload is required for plugins to register properly.
    var plugins = Lampa.Plugins.get();
    var plugins_add = [{initiale}];
    var plugins_push = [];

    // Build a set of active plugin URLs for fast lookup.
    var activeUrls = {};
    plugins_add.forEach(function(p) { activeUrls[p.url] = true; });

    // Remove server-managed plugins that admin has disabled.
    // Only touch plugins whose URL starts with this server's host
    // (user-installed plugins from other sources are left alone).
    var serverUrl = '{localhost}/';
    var toRemove = [];
    plugins.forEach(function(p) {
      if (p.url && p.url.indexOf(serverUrl) === 0 && !activeUrls[p.url]) {
        toRemove.push(p);
      }
    });
    toRemove.forEach(function(p) {
      Lampa.Plugins.remove(p);
    });

    // Add plugins that are not yet installed.
    plugins_add.forEach(function(plugin) {
      if (!plugins.find(function(a) {
          return a.url == plugin.url;
        })) {
        Lampa.Plugins.add(plugin);
        Lampa.Plugins.save();
        plugins_push.push(plugin.url);
      }
    });

    if (plugins_push.length || toRemove.length) {
      // Plugins were added or removed — reload once so the client
      // picks up the change during its normal init sequence.
      // sessionStorage flag prevents an infinite loop.
      if (!sessionStorage.getItem('lampac_plugins_reload')) {
        sessionStorage.setItem('lampac_plugins_reload', '1');
        location.reload();
        return;
      }
      // Second pass after reload — flag is set, load new scripts normally.
      sessionStorage.removeItem('lampac_plugins_reload');
      if (plugins_push.length) {
        Lampa.Utils.putScript(plugins_push, function() {}, function() {}, function() {}, true);
      }
    }

    if (Lampa.Storage.get('lampac_initiale', 'false')) return;

    Lampa.Storage.set('lampac_initiale', 'true');
    Lampa.Storage.set('source', 'cub');
    Lampa.Storage.set('video_quality_default', '2160');
    Lampa.Storage.set('full_btn_priority', '{full_btn_priority_hash}');
    Lampa.Storage.set('proxy_tmdb', '{country}' == 'RU');
    Lampa.Storage.set('poster_size', 'w300');

    if (lampainit_invc)
      lampainit_invc.first_initiale();

  }
})();