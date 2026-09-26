(function () {
    'use strict';

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
			
    if(!Lampa.Storage.get('lampac_initiale','false')) {
       // исполняется во время первого запуска лампы 
    }
	
    // исполняется при каждой загрузке лампы 
	
	
    /*
       {country} - RU, UA, etc
       {localhost} - адрес по которому лиент запросил данные / пример http://IP:9118
       {jachost} - локальный или внешний адрес JacRed
    */
	
})();