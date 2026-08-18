function showHeaders() {
  var network = new Lampa.Reguest();
  network.silent('{localhost}/headers?type=text', function(res) {
    Lampa.Modal.open({
      title: '',
      html: $('<pre style="padding-left: 1.2rem;">' + res + '</pre>'),
      size: 'full',
      onBack: function onBack() {
        Lampa.Modal.close();
      }
    });
  }, function() {
  }, false, {
    dataType: 'text'
  });
}

showHeaders();