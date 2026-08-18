  {invc-rch}
  window.rch[hostkey].typeInvoke('{localhost}', function() {});

  function rchInvoke(json, call) {
    if (window.hubConnection && window.hubConnection[hostkey])
      window.hubConnection[hostkey].stop();

    if (!window.hubConnection)
      window.hubConnection = {};

    window.hubConnection[hostkey] = new signalR.HubConnectionBuilder().withUrl(json.ws).build();
    window.hubConnection[hostkey].start().then(function() {
      window.rch[hostkey].Registry(window.hubConnection[hostkey], function() {
        call();
      });
    })["catch"](function(err) {
      Lampa.Noty.show(err.toString());
    });
  }

  function rchRun(json, call) {
    if (typeof signalR == 'undefined') {
      Lampa.Utils.putScript(["{localhost}/signalr-6.0.25_es5.js"], function() {}, false, function() {
        rchInvoke(json, call);
      }, true);
    } else {
      rchInvoke(json, call);
    }
  }