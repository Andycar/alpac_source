package adminhttp

import (
	"net/http"
	"strconv"

	"lampac-go/internal/tgauth"
)

// Admin web-log viewer: a detailed, filterable view of the client weblog forwarded to /lite/weblog
// (see weblog_collect.go) so an admin can watch ALL web/TV clients' actions + errors live, in one
// place, instead of grepping journalctl. Auth-gated like every other admin endpoint.

// GET  /{adminPath}/api/capilog?limit=&lvl=&tag=&ip=  → {logs, seq}
// POST /{adminPath}/api/capilog  {action:"clear"}     → clears the server buffer
func capiLogAPIHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Action string `json:"action"`
			}
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
			if req.Action == "clear" {
				webLogClear()
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 3000 {
			limit = 800
		}
		logs, seq := webLogSnapshot(limit, q.Get("lvl"), q.Get("tag"), q.Get("ip"))
		writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "seq": seq})
	}
}

func capiLogPageHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		writeHTML(w, http.StatusOK, capiLogPageHTML)
	}
}

const capiLogPageHTML = `<!doctype html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>capilog · web client log</title>
<style>
  :root{color-scheme:dark}
  *{box-sizing:border-box}
  body{margin:0;background:#0a0b10;color:#c3c7d6;font:13px/1.5 'SF Mono',Menlo,Consolas,monospace}
  header{position:sticky;top:0;z-index:5;display:flex;flex-wrap:wrap;gap:8px;align-items:center;
    padding:10px 14px;background:#11131a;border-bottom:1px solid #20242f}
  header b{font-size:16px;color:#fff;margin-right:6px}
  .count{color:#6b7186}
  input,select,button{background:#1a1d27;color:#dfe2ee;border:1px solid #2a2f3d;border-radius:7px;
    padding:6px 10px;font:inherit}
  button{cursor:pointer}
  button.on{background:#3a2d7a;border-color:#7c5cff;color:#fff}
  .spacer{flex:1}
  table{width:100%;border-collapse:collapse;table-layout:fixed}
  td{padding:2px 8px;vertical-align:top;border-bottom:1px solid #14161d;word-break:break-word}
  tr.error td{background:rgba(255,60,60,.08);color:#ff8a8a}
  tr.warn td{color:#ffd479}
  tr.event td{color:#8fd0ff}
  .c-t{width:96px;color:#5b6076;white-space:nowrap}
  .c-ip{width:120px;color:#7f8699}
  .c-ua{width:130px;color:#6b7186}
  .c-tag{width:78px;color:#a78bfa;font-weight:700}
  .c-data{color:#7f8699}
  tr.tagrow td{cursor:pointer}
  .empty{padding:30px;text-align:center;color:#5b6076}
</style></head><body>
<header>
  <b>capilog</b><span class="count" id="cnt">0</span>
  <select id="lvl">
    <option value="all">все уровни</option>
    <option value="error">только ошибки</option>
    <option value="warn">warn+</option>
    <option value="event">события</option>
  </select>
  <input id="tag" placeholder="tag (capi/auth/online/player…)" size="18">
  <input id="ip" placeholder="IP" size="13">
  <input id="q" placeholder="поиск по тексту" size="18">
  <button id="live" class="on">● live</button>
  <button id="clr">очистить</button>
  <span class="spacer"></span>
  <span class="count">обновл. 2с · клик по строке — фильтр по tag</span>
</header>
<table><tbody id="rows"></tbody></table>
<div class="empty" id="empty">нет данных — клиенты ещё не прислали лог (или weblog_collect=false)</div>
<script>
(function(){
  var rows=document.getElementById('rows'), empty=document.getElementById('empty'), cnt=document.getElementById('cnt');
  var lvl=document.getElementById('lvl'), tag=document.getElementById('tag'), ip=document.getElementById('ip'), q=document.getElementById('q');
  var live=document.getElementById('live'), clr=document.getElementById('clr');
  var following=true, timer=null;
  function esc(s){return (s==null?'':String(s)).replace(/[&<>]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;'}[c]})}
  function ft(ms){var d=new Date(ms);function p(n){return (n<10?'0':'')+n}return p(d.getHours())+':'+p(d.getMinutes())+':'+p(d.getSeconds())+'.'+('00'+d.getMilliseconds()).slice(-3)}
  function load(){
    var u='api/capilog?limit=800&lvl='+encodeURIComponent(lvl.value)+'&tag='+encodeURIComponent(tag.value.trim())+'&ip='+encodeURIComponent(ip.value.trim());
    fetch(u,{credentials:'include'}).then(function(r){return r.json()}).then(function(j){
      var logs=j.logs||[]; var needle=q.value.trim().toLowerCase();
      if(needle) logs=logs.filter(function(e){return (e.msg+' '+(e.data||'')+' '+e.tag).toLowerCase().indexOf(needle)>=0});
      cnt.textContent=logs.length;
      empty.style.display=logs.length?'none':'block';
      var atBottom=(window.innerHeight+window.scrollY)>=(document.body.scrollHeight-40);
      rows.innerHTML=logs.map(function(e){
        return '<tr class="'+esc(e.lvl)+'">'+
          '<td class="c-t">'+ft(e.t)+'</td>'+
          '<td class="c-ip">'+esc(e.ip)+'</td>'+
          '<td class="c-ua">'+esc(e.ua)+'</td>'+
          '<td class="c-tag tagrow" data-tag="'+esc(e.tag)+'">'+esc(e.tag)+'</td>'+
          '<td>'+esc(e.msg)+(e.data?' <span class="c-data">'+esc(e.data)+'</span>':'')+'</td>'+
        '</tr>';
      }).join('');
      if(following&&atBottom) window.scrollTo(0,document.body.scrollHeight);
    }).catch(function(){});
  }
  rows.addEventListener('click',function(ev){var t=ev.target.closest('.tagrow');if(t){tag.value=t.getAttribute('data-tag');load()}});
  function tick(){if(following)load()}
  function arm(){clearInterval(timer);timer=setInterval(tick,2000)}
  live.onclick=function(){following=!following;live.className=following?'on':'';live.textContent=following?'● live':'❚❚ пауза';if(following)load()};
  clr.onclick=function(){if(!confirm('Очистить серверный буфер логов?'))return;fetch('api/capilog',{method:'POST',credentials:'include',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'clear'})}).then(load)};
  [lvl,tag,ip,q].forEach(function(el){el.addEventListener('input',load)});
  load();arm();
})();
</script></body></html>`
