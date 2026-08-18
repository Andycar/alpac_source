(function() {
  'use strict';

    /* Слушать события
      document.addEventListener('lwsEvent', function(evnt) {
        console.log(evnt.detail);
      }); 
	*/

  {invc-rch}

  var hubConnection;
  window.lwsEvent = {
    uid: '', 
	connectionId: '',
	init: false
  };
  
  window.lwsEvent.send = function hubEvnt(name, data) {
    if(hubConnection.state === 'Connected')
        hubConnection.invoke("events", window.lwsEvent.uid, name, data);
  };
  
  window.lwsEvent.sendId = function hubEvnt(connectionId, name, data) {
    if(hubConnection.state === 'Connected')
        hubConnection.invoke("eventsId", connectionId, window.lwsEvent.uid, name, data);
  };

  function sendEvent(name, data) {
    var hubEvents = document.createEvent('CustomEvent');
    hubEvents.initCustomEvent('lwsEvent', true, true, {
      uid: window.lwsEvent.uid,
      name: name,
      data: data
    });

    document.dispatchEvent(hubEvents);
  }


  function account(url) {
    url = url + '';
    if (url.indexOf('account_email=') == -1) {
      var email = Lampa.Storage.get('account_email');
      if (email) url = Lampa.Utils.addUrlComponent(url, 'account_email=' + encodeURIComponent(email));
    }
    if (url.indexOf('uid=') == -1) {
      var uid = Lampa.Storage.get('lampac_unic_id', '');
      if (uid) url = Lampa.Utils.addUrlComponent(url, 'uid=' + encodeURIComponent(uid));
    }
    if (url.indexOf('token=') == -1) {
      var token = '{token}';
      if (token != '') url = Lampa.Utils.addUrlComponent(url, 'token={token}');
    }
    return url;
  }


  function waitEvent() {
    hubConnection = new signalR.HubConnectionBuilder().withUrl('{localhost}/ws').build();

    function tryConnect() {
      hubConnection.start().then(function() {
        window.lwsEvent.connectionId = hubConnection.connectionId;
        sendEvent('system', 'connected');
        hubConnection.invoke("RegistryEvent", window.lwsEvent.uid).then(function() {
          hubConnection.on("event", function(uid, name, data) {
            sendEvent(name, data);
          });
        });

        window.rch[hostkey].Registry(hubConnection, function() {});

        hubConnection.onclose(function() {
          sendEvent('system', 'onclose');
          waitEvent();
        });
      }).catch(function(err) {
        setTimeout(function() {
          if(hubConnection.state !== 'Connected')
            sendEvent('system', 'reconnected');
          tryConnect();
        }, 5000);
      });
    }

    tryConnect();
  }


  function start(j) {
    window.reqinfo = j;
    window.lwsEvent.init = true;
    window.lwsEvent.uid = j.user_uid;
    if (typeof signalR == 'undefined') {
      Lampa.Utils.putScript(["{localhost}/signalr-6.0.25_es5.js"], function() {}, false, function() {
        waitEvent();
      }, true);
    } else waitEvent();
  }
  
  
  if (!window.lwsEvent.init) {
    if (!window.reqinfo) {
      var network = new Lampa.Reguest();
      network.silent(account('{localhost}/reqinfo'), function(j) {
        if (j.user_uid)
          start(j);
      });
    } 
    else
      start(window.reqinfo);
  }

})();