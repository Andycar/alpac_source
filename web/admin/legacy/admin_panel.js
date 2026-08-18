// admin_panel.js — legacy admin SPA JS extracted from admin_tg_panel.go.
// 12 inline <script> blocks concatenated in original order; each IIFE is
// self-contained so concatenation preserves behavior. Boundaries kept as
// comments so the original block can be located in admin_tg_panel.go.


// === block 1 (orig admin_tg_panel.go lines 2762-8757) ===
(function(){
var basePath=location.pathname.replace(/\/+$/,'');
var currentUser={};
function api(p,o){return fetch(basePath+p,o).then(function(r){return r.json()})}
window.api=api;window.basePath=basePath;
function post(p,d){return api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(d)})}
window.toast=function(m,t){var e=document.getElementById('toast');e.textContent=m;e.className='toast show '+(t||'success');setTimeout(function(){e.className='toast'},3000)};
function showRestart(){document.getElementById('restart-banner').classList.add('show')}
function esc(s){if(s==null)return'';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;')}
window.esc=esc;
function setH(id,html){var el=typeof id==='string'?document.getElementById(id):id;if(el&&el.innerHTML!==html)el.innerHTML=html}
function setT(id,txt){var el=typeof id==='string'?document.getElementById(id):id;if(el&&el.textContent!==txt)el.textContent=String(txt)}

// --- Init ---
api('/api/whoami').then(function(me){
  currentUser=me;
  if(me.is_super){document.getElementById('tab-tgsettings').style.display='';document.getElementById('tab-broadcast').style.display='';}
  api('/api/alice').then(function(){document.getElementById('tab-alice').style.display='';}).catch(function(){});
  api('/api/skip').then(function(){document.getElementById('tab-skipintro').style.display='';}).catch(function(){});
  api('/api/calendar').then(function(){document.getElementById('tab-calendar').style.display='';}).catch(function(){});
  api('/api/xsearch/sources').then(function(){document.getElementById('tab-xsearch').style.display='';}).catch(function(){});
  api('/api/collections/stats').then(function(){document.getElementById('tab-collections').style.display='';}).catch(function(){});
  api('/api/plugins').then(function(d){if(d&&d.iptv)document.getElementById('tab-iptv').style.display='';}).catch(function(){});
  api('/api/torrs/list').then(function(d){if(d&&d.embedded)document.getElementById('tab-torrs').style.display='';}).catch(function(){});
  api('/api/jacred/status').then(function(d){if(d&&d.enabled)document.getElementById('tab-jacred').style.display='';}).catch(function(){});
  loadDashboard();
}).catch(function(){loadDashboard()});

// --- Sidebar toggle (mobile) ---
window.toggleSidebar=function(){
  document.getElementById('sidebar').classList.toggle('open');
  document.getElementById('sidebar-overlay').classList.toggle('open');
};

// --- Nav group collapse/expand ---
window.toggleNavGroup=function(sec){
  sec.classList.toggle('open');
  var grp=sec.nextElementSibling;
  if(grp&&grp.classList.contains('nav-group'))grp.classList.toggle('open');
};

// --- Tabs ---
var tabTitles={dashboard:'Дашборд',users:'Пользователи',groups:'\u0413\u0440\u0443\u043f\u043f\u044b',promo:'\u041f\u0440\u043e\u043c\u043e\u043a\u043e\u0434\u044b',commerce:'\u041a\u043e\u043c\u043c\u0435\u0440\u0446\u0438\u044f',bans:'Блокировки',waf:'WAF',balancers:'Балансеры',modules:'Модули',plugins:'Плагины',proxy:'Прокси',proxycore:'ProxyCore',tgsettings:'TG Бот',server:'Сервер',cluster:'Кластер',constructor:'Конструктор',config:'Конфиг',lampa:'Lampa',selfupdate:'Обновления',deps:'Зависимости',inspector:'Инспектор',feedback:'Обратная связь',skipintro:'Интро/Аутро',calendar:'Расписание',xsearch:'Поиск+',collections:'Коллекции',iptv:'IPTV',broadcast:'Рассылка',logs:'Логи',alice:'Алиса',torrs:'Торренты',jacred:'Парсер jacred',appreplace:'AppReplace'};
document.querySelectorAll('.tab').forEach(function(tab){
  tab.addEventListener('click',function(){
    document.querySelectorAll('.tab').forEach(function(t){t.classList.remove('active')});
    document.querySelectorAll('.panel').forEach(function(p){p.classList.remove('active')});
    tab.classList.add('active');
    document.getElementById('panel-'+tab.dataset.tab).classList.add('active');
    // Auto-expand the parent nav-group when a tab is clicked
    var grp=tab.closest('.nav-group');
    if(grp&&!grp.classList.contains('open')){
      grp.classList.add('open');
      var sec=grp.previousElementSibling;
      if(sec&&sec.classList.contains('sidebar-section'))sec.classList.add('open');
    }
    var pt=document.getElementById('page-title');
    if(pt)pt.textContent=tabTitles[tab.dataset.tab]||tab.dataset.tab;
    // Close mobile sidebar
    document.getElementById('sidebar').classList.remove('open');
    document.getElementById('sidebar-overlay').classList.remove('open');
    var m={dashboard:loadDashboard,users:loadUsers,groups:loadGroups,promo:loadPromo,bans:loadBans,waf:loadWAF,balancers:loadBalancers,modules:function(){mMods.refresh()},'sisi-sources':function(){sisiMod.refresh()},'music-sources':function(){musicMod.refresh()},'env-presets':function(){envMod.refresh()},plugins:loadPlugins,proxy:loadProxy,proxycore:loadProxyCore,tgsettings:loadTGSettings,server:loadServerStats,cluster:function(){clusterMod.refresh()},constructor:loadConstructor,config:loadConfigTOML,lampa:loadLampa,selfupdate:loadSelfUpdate,deps:loadDeps,inspector:loadInspector,feedback:loadFeedback,skipintro:loadSkipIntro,calendar:loadCalendar,xsearch:loadXSearch,collections:loadCollections,iptv:loadIPTV,broadcast:loadBroadcast,logs:loadLogs,telemetry:openTelemetry,mediagw:openMediaGW,torrs:initTorrsTab,jacred:initJacredTab,appreplace:loadAppReplace};
    if(m[tab.dataset.tab])m[tab.dataset.tab]();
  });
});

// --- Dashboard ---
var dashTimer=null;
function fmtBytes(b){if(!b||b===0)return '0 B';var u=['B','KB','MB','GB','TB'];var i=Math.floor(Math.log(b)/Math.log(1024));if(i>=u.length)i=u.length-1;return (b/Math.pow(1024,i)).toFixed(i>0?1:0)+' '+u[i]}
function fmtUptime(s){if(!s)return '-';var d=Math.floor(s/86400),h=Math.floor((s%86400)/3600),m=Math.floor((s%3600)/60);if(d>0)return d+'д '+h+'ч';if(h>0)return h+'ч '+m+'м';return m+'м'}
function dashTCard(v,l,cls){return '<div class="dash-tcard'+(cls?' '+cls:'')+'"><div class="tc-val">'+v+'</div><div class="tc-lbl">'+l+'</div></div>'}
function dashRCard(v,l,cls){return '<div class="dash-rcard"><div class="rc-val'+(cls?' '+cls:'')+'">'+v+'</div><div class="rc-lbl">'+l+'</div></div>'}

// ===== Widget Registry =====
var WIDGETS={
  traffic:{title:'Трафик',icon:'\ud83d\udcca',api:'/api/dashboard',def:true,size:'half',
    html:function(){return '<div class="dash-section"><div class="dash-section-head"><span class="dash-section-icon">\ud83d\udcca</span><span class="dash-section-title">Трафик</span></div><div class="dash-traffic-cards-wide" id="dash-traffic"></div><div class="dash-chart-wrap" style="margin-top:12px"><div class="dash-chart" id="dash-traffic-chart"></div><div class="dash-chart-labels"><span>-10 мин</span><span>сейчас</span></div></div></div>';},
    render:function(d){
      var tr=d.traffic||{};
      var el=document.getElementById('dash-traffic');if(!el)return;
      setH(el,dashTCard(fmtBytes(tr.proxy_bytes_min),'Proxy / мин')+dashTCard(fmtBytes(tr.total_bytes_min),'Total / мин')+dashTCard(fmtBytes(tr.proxy_bytes_hour),'Proxy / час')+dashTCard(fmtBytes(tr.total_bytes_hour),'Total / час')+dashTCard(tr.active_proxy||0,'Активных потоков','accent wide'));
      var hist=tr.proxy_bytes_history||[];var chartEl=document.getElementById('dash-traffic-chart');
      var histSum=0;for(var hi=0;hi<hist.length;hi++)histSum+=hist[hi];
      if(hist.length>0&&histSum>0){var maxV=Math.max.apply(null,hist)||1;var bars='';for(var i=0;i<hist.length;i++){var pct=Math.max(2,Math.round((hist[i]/maxV)*100));var clr=hist[i]>0?'var(--accent)':'var(--border)';bars+='<div style="flex:1;background:'+clr+';height:'+pct+'%;border-radius:3px 3px 0 0;min-width:6px;transition:height .3s" title="'+(hist.length-i)+' мин назад: '+fmtBytes(hist[i])+'"></div>';}setH(chartEl,bars);}else{setH(chartEl,'<div style="color:var(--text-dim);font-size:12px;margin:auto">Нет данных за последние 10 мин</div>');}
    }
  },
  requests:{title:'Запросы',icon:'\u26a1',api:'/api/dashboard',def:true,size:'half',
    html:function(){return '<div class="dash-section"><div class="dash-section-head"><span class="dash-section-icon">\u26a1</span><span class="dash-section-title">Запросы</span></div><div class="dash-req-cards" id="dash-requests"></div><div id="dash-routes" style="margin-top:12px"></div></div>';},
    render:function(d){
      var rq=d.requests||{};
      var el=document.getElementById('dash-requests');if(!el)return;
      setH(el,dashRCard(rq.req_min||0,'Запросов / мин','good')+dashRCard(rq.req_hour||0,'Запросов / час')+dashRCard(rq.active||0,'Активных')+dashRCard(rq.latency_avg?rq.latency_avg.toFixed(0)+' ms':'-','Средн. задержка'));
      var routesHtml='';var topRoutes=rq.top_routes||[];var topPlugins=rq.top_proxy_plugins||[];
      if(topRoutes.length>0||topPlugins.length>0){
        var maxCount=1;for(var ri=0;ri<topRoutes.length;ri++){if(topRoutes[ri].count>maxCount)maxCount=topRoutes[ri].count;}
        if(topRoutes.length>0){routesHtml+='<div class="dash-routes-title">Популярные маршруты</div>';for(var ri=0;ri<Math.min(topRoutes.length,5);ri++){var r=topRoutes[ri];var w=Math.max(4,Math.round((r.count/maxCount)*100));routesHtml+='<div class="dash-route-bar"><span class="route-name">'+esc(r.route)+'</span><span class="route-bar-bg"><span class="route-bar-fill" style="width:'+w+'%"></span></span><span class="route-count">'+r.count+'</span></div>';}}
        if(topPlugins.length>0){var maxPCount=1;for(var pi=0;pi<topPlugins.length;pi++){if(topPlugins[pi].count>maxPCount)maxPCount=topPlugins[pi].count;}routesHtml+='<div class="dash-routes-title" style="margin-top:14px">Популярные прокси-плагины</div>';for(var pi=0;pi<Math.min(topPlugins.length,5);pi++){var p=topPlugins[pi];var pw=Math.max(4,Math.round((p.count/maxPCount)*100));routesHtml+='<div class="dash-route-bar"><span class="route-name">'+esc(p.plugin)+'</span><span class="route-bar-bg"><span class="route-bar-fill" style="width:'+pw+'%;background:#f0c040"></span></span><span class="route-count">'+p.count+'</span></div>';}}
      }
      setH('dash-routes',routesHtml);
    }
  },
  health:{title:'Здоровье',icon:'\ud83d\udc9a',api:'/api/dashboard',def:true,size:'full',
    html:function(){return '<div class="dash-section full"><div class="dash-section-head"><span class="dash-section-icon">\ud83d\udc9a</span><span class="dash-section-title">Здоровье балансеров</span><span class="dash-health-summary" id="dash-health-summary"></span></div><div class="dash-health-mosaic" id="dash-health"></div></div>';},
    render:function(d){
      var health=d.health||{};var hkeys=Object.keys(health);
      var hEl=document.getElementById('dash-health');var sumEl=document.getElementById('dash-health-summary');
      if(!hEl)return;
      if(hkeys.length===0){setH(hEl,'<div style="color:var(--text-dim);font-size:12px;padding:8px 0">Healthcheck ещё не запускался. Данные появятся через ~60 сек.</div>');if(sumEl)sumEl.textContent='';return;}
      var okC=0,failC=0,disC=0;hkeys.sort();var hh='';
      for(var k=0;k<hkeys.length;k++){var name=hkeys[k],st=health[name];if(st.auto_disabled)disC++;else if(st.healthy)okC++;else failC++;var dotCls=st.auto_disabled?'warn':st.healthy?'ok':'fail';var statusTxt=st.auto_disabled?'Авто-выкл':st.healthy?'OK':'Ошибка';var latTxt=st.last_check_latency_ms>0?st.last_check_latency_ms+'ms':'—';hh+='<div class="dash-hm-tile"><span class="hm-dot '+dotCls+'"></span><span class="hm-name">'+esc(name)+'</span><span class="hm-lat">'+latTxt+'</span><div class="hm-tooltip"><div class="tt-row"><span class="tt-lbl">Статус</span><span class="tt-val" style="color:'+(dotCls==='ok'?'var(--accent)':dotCls==='fail'?'var(--danger)':'var(--warning)')+'">'+statusTxt+'</span></div><div class="tt-row"><span class="tt-lbl">HTTP</span><span class="tt-val">'+(st.last_check_status||'—')+'</span></div><div class="tt-row"><span class="tt-lbl">Latency</span><span class="tt-val">'+latTxt+'</span></div>';if(st.consecutive_fails>0)hh+='<div class="tt-row"><span class="tt-lbl">Fails</span><span class="tt-val" style="color:var(--danger)">'+st.consecutive_fails+'</span></div>';if(st.last_check_error)hh+='<div style="color:var(--danger);font-size:10px;margin-top:4px;max-width:250px;word-break:break-all">'+esc(st.last_check_error.substring(0,100))+'</div>';hh+='</div></div>';}
      setH(hEl,hh);var parts=[];if(okC)parts.push(okC+' OK');if(failC)parts.push(failC+' ошибок');if(disC)parts.push(disC+' выкл');if(sumEl)setT(sumEl,parts.join(' \u00b7 '));
    }
  },
  latency:{title:'Задержки',icon:'\u23f1',api:'/api/stats',def:false,size:'half',
    html:function(){return '<div class="dash-section"><div class="dash-section-head"><span class="dash-section-icon">\u23f1</span><span class="dash-section-title">Задержки</span></div><div id="dash-w-latency-bars"></div></div>';},
    render:function(d){
      var el=document.getElementById('dash-w-latency-bars');if(!el)return;
      var req=d.requests||{},pct=req.percentiles||{};
      var vals=[['Avg',req.latency_avg||0],['P50',pct['50']||0],['P75',pct['75']||0],['P90',pct['90']||0],['P95',pct['95']||0],['P99',pct['99']||0]];
      var maxLat=Math.max.apply(null,vals.map(function(x){return x[1]}))||1;
      var h='';for(var i=0;i<vals.length;i++){
        var ms=vals[i][1];var pctV=maxLat>0?Math.min(100,(ms/maxLat)*100):0;
        var cls=ms<50?'var(--accent)':ms<200?'var(--warning)':ms<500?'#ff9800':'var(--danger)';
        h+='<div style="display:flex;align-items:center;gap:8px;margin-bottom:4px;font-size:12px"><span style="width:30px;color:var(--text-muted);text-align:right">'+vals[i][0]+'</span><div style="flex:1;height:6px;background:var(--bg);border-radius:3px;overflow:hidden"><div style="height:100%;width:'+Math.max(5,pctV)+'%;background:'+cls+';border-radius:3px"></div></div><span style="color:var(--text);width:55px;text-align:right">'+ms.toFixed(0)+' ms</span></div>';
      }
      setH(el,h);
    }
  },
  memory:{title:'Память',icon:'\ud83e\udde0',api:'/api/stats',def:false,size:'half',
    html:function(){return '<div class="dash-section"><div class="dash-section-head"><span class="dash-section-icon">\ud83e\udde0</span><span class="dash-section-title">Память</span></div><div id="dash-w-memory"></div></div>';},
    render:function(d){
      var el=document.getElementById('dash-w-memory');if(!el)return;
      var mem=d.mem||{};var pct=mem.sys_mb>0?Math.round(mem.heap_alloc_mb/mem.sys_mb*100):0;
      var color=pct>80?'var(--danger)':pct>60?'var(--warning)':'var(--accent)';
      var h='<div style="display:flex;align-items:center;gap:16px;margin-bottom:12px">';
      h+='<div style="width:64px;height:64px;position:relative"><svg viewBox="0 0 110 110" style="width:100%;height:100%"><circle cx="55" cy="55" r="45" fill="none" stroke="rgba(255,255,255,0.06)" stroke-width="8"/><circle cx="55" cy="55" r="45" fill="none" stroke="'+color+'" stroke-width="8" stroke-dasharray="'+(2*Math.PI*45).toFixed(1)+'" stroke-dashoffset="'+((2*Math.PI*45)-(pct/100)*(2*Math.PI*45)).toFixed(1)+'" stroke-linecap="round" transform="rotate(-90 55 55)"/></svg><div style="position:absolute;inset:0;display:flex;align-items:center;justify-content:center;font-size:14px;font-weight:700;color:'+color+'">'+pct+'%</div></div>';
      h+='<div style="flex:1"><div style="font-size:18px;font-weight:700;color:var(--text)">'+mem.heap_alloc_mb+' MB</div><div style="font-size:11px;color:var(--text-muted)">из '+mem.sys_mb+' MB sys</div></div></div>';
      h+='<div style="display:grid;grid-template-columns:1fr 1fr;gap:6px;font-size:11px">';
      h+='<div style="background:var(--bg);border-radius:6px;padding:6px 8px"><span style="color:var(--text-muted)">Heap InUse</span><br><span style="color:var(--text);font-weight:600">'+mem.heap_inuse_mb+' MB</span></div>';
      h+='<div style="background:var(--bg);border-radius:6px;padding:6px 8px"><span style="color:var(--text-muted)">Stack</span><br><span style="color:var(--text);font-weight:600">'+mem.stack_inuse_mb+' MB</span></div>';
      h+='<div style="background:var(--bg);border-radius:6px;padding:6px 8px"><span style="color:var(--text-muted)">GC Cycles</span><br><span style="color:var(--text);font-weight:600">'+mem.gc_count+'</span></div>';
      h+='<div style="background:var(--bg);border-radius:6px;padding:6px 8px"><span style="color:var(--text-muted)">GC Pause</span><br><span style="color:var(--text);font-weight:600">'+mem.gc_pause_ms+' ms</span></div>';
      h+='</div>';
      setH(el,h);
    }
  },
  processes:{title:'Процессы',icon:'\u2699\ufe0f',api:'/api/stats',def:false,size:'full',
    html:function(){return '<div class="dash-section full"><div class="dash-section-head"><span class="dash-section-icon">\u2699\ufe0f</span><span class="dash-section-title">Процессы</span></div><div id="dash-w-processes" style="display:flex;flex-wrap:wrap;gap:8px"></div></div>';},
    render:function(d){
      var el=document.getElementById('dash-w-processes');if(!el)return;
      var proc=d.processes||{};var h='';
      // Proxy sidecars
      var xr=proc.proxy||[];for(var i=0;i<xr.length;i++){var x=xr[i];h+=dashProcChip(x.label||'Proxy '+(i+1),x.alive?'ok':'fail',x.engine||'xray');}
      // TorrServer
      var ts=proc.torrserver||{};if(ts.configured)h+=dashProcChip('TorrServer',ts.alive?'ok':'fail',ts.inprocess?'\u0412\u0441\u0442\u0440\u043E\u0435\u043D\u043D\u044B\u0439':ts.external?'\u0412\u043D\u0435\u0448\u043D':'\u041F\u043E\u0440\u0442: '+ts.port);
      // yt-dlp
      var yt=proc.ytdlp||{};if(yt.available)h+=dashProcChip('yt-dlp','ok','v'+(yt.version||'?').substring(0,10));
      // Custom balancers
      var cbl=d.custom_balancers||[];for(var i=0;i<cbl.length;i++){var cb=cbl[i];h+=dashProcChip(cb.display_name||cb.name,cb.state==='running'?'ok':'fail','Порт: '+cb.port);}
      // Transcoding
      var trans=d.transcoding||{};if(trans.enabled){var tj=trans.jobs||[];h+=dashProcChip('FFmpeg','ok',tj.length+'/'+trans.max_jobs+' задач');}
      if(!h)h='<div style="color:var(--text-dim);font-size:12px">Нет субпроцессов</div>';
      setH(el,h);
    }
  },
  tmdb:{title:'TMDB Кеш',icon:'\ud83c\udfa5',api:'/api/tmdb-stats',def:false,size:'half',
    html:function(){return '<div class="dash-section"><div class="dash-section-head"><span class="dash-section-icon">\ud83c\udfa5</span><span class="dash-section-title">TMDB Кеш</span></div><div id="dash-w-tmdb"></div></div>';},
    render:function(d){
      var el=document.getElementById('dash-w-tmdb');if(!el)return;
      var c=d.cache||{};var ups=d.upstreams||[];
      var hr=c.total_reqs>0?((c.hits/(c.hits+c.misses+c.stale_hits))*100).toFixed(1):'—';
      var h='<div style="display:grid;grid-template-columns:1fr 1fr;gap:6px;margin-bottom:8px">';
      h+='<div style="background:var(--bg);border-radius:6px;padding:8px"><div style="color:var(--text-muted);font-size:10px">Hit Rate</div><div style="color:var(--accent);font-size:18px;font-weight:700">'+hr+'%</div></div>';
      h+='<div style="background:var(--bg);border-radius:6px;padding:8px"><div style="color:var(--text-muted);font-size:10px">Записей</div><div style="color:var(--text);font-size:18px;font-weight:700">'+(c.item_count||0).toLocaleString()+'</div></div>';
      h+='</div>';
      if(ups.length){for(var i=0;i<ups.length;i++){var u=ups[i];h+='<div style="display:flex;align-items:center;gap:6px;font-size:12px;padding:2px 0">'+(u.healthy?'\ud83d\udfe2':'\ud83d\udd34')+' <span style="color:var(--text)">'+esc(u.name)+'</span><span style="color:var(--text-muted);margin-left:auto">'+(u.avg_lat_ms>0?u.avg_lat_ms+'ms':'—')+'</span></div>';}}
      setH(el,h);
    }
  },
  logs:{title:'Ошибки (лог)',icon:'\ud83d\udccb',api:'/api/logs?level=error&limit=20',def:false,size:'full',
    html:function(){return '<div class="dash-section full"><div class="dash-section-head"><span class="dash-section-icon">\ud83d\udccb</span><span class="dash-section-title">Последние ошибки</span></div><div id="dash-w-logs" style="max-height:200px;overflow-y:auto"></div></div>';},
    render:function(d){
      var el=document.getElementById('dash-w-logs');if(!el)return;
      var entries=d.entries||[];
      if(entries.length===0){setH(el,'<div style="color:var(--accent);font-size:12px;padding:4px">\u2713 Нет ошибок</div>');return;}
      var errs=entries.slice(-15);
      var h='';for(var i=errs.length-1;i>=0;i--){
        var e=errs[i];var lc=e.level==='error'?'#ff6b6b':'#f0c040';
        var t=e.time||'';if(t.length>19)t=t.substring(11,19);
        h+='<div style="display:flex;gap:8px;padding:3px 0;font-size:11px;border-bottom:1px solid var(--surface2)"><span style="color:var(--text-dim);flex-shrink:0">'+t+'</span><span style="color:'+lc+';flex-shrink:0;width:40px">'+e.level+'</span><span style="color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">'+esc(e.message)+'</span></div>';
      }
      setH(el,h);
    }
  }
};
// Helper for process chips
function dashProcChip(name,status,sub){var color=status==='ok'?'var(--accent)':status==='fail'?'var(--danger)':'var(--text-muted)';return '<div style="display:flex;align-items:center;gap:6px;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:6px 10px;font-size:12px"><span style="width:8px;height:8px;border-radius:50%;background:'+color+';flex-shrink:0"></span><span style="color:var(--text)">'+esc(name)+'</span><span style="color:var(--text-muted);font-size:10px">'+esc(sub)+'</span></div>';}

// ===== Widget Persistence =====
var DASH_STORAGE_KEY='lampac_dash_widgets';
function getDefaultWidgets(){var ids=[];var keys=Object.keys(WIDGETS);for(var i=0;i<keys.length;i++){if(WIDGETS[keys[i]].def)ids.push(keys[i]);}return ids;}
function getActiveWidgets(){
  try{var s=localStorage.getItem(DASH_STORAGE_KEY);if(s){var arr=JSON.parse(s);if(Array.isArray(arr)&&arr.length>0){var valid=[];for(var i=0;i<arr.length;i++){if(WIDGETS[arr[i]])valid.push(arr[i]);}if(valid.length>0)return valid;}}}catch(e){}
  return getDefaultWidgets();
}
function saveActiveWidgets(ids){try{localStorage.setItem(DASH_STORAGE_KEY,JSON.stringify(ids))}catch(e){}}

// ===== Widget Picker =====
window.openWidgetPicker=function(){
  var active=getActiveWidgets();
  var allKeys=Object.keys(WIDGETS);
  // Build ordered list: active widgets first (in order), then inactive
  var ordered=[];for(var i=0;i<active.length;i++)ordered.push(active[i]);
  for(var i=0;i<allKeys.length;i++){if(active.indexOf(allKeys[i])===-1)ordered.push(allKeys[i]);}
  var h='';
  for(var i=0;i<ordered.length;i++){
    var id=ordered[i];var w=WIDGETS[id];var isActive=active.indexOf(id)!==-1;
    h+='<div class="wpick-item'+(isActive?' active':'')+'" data-wid="'+id+'">';
    h+='<label><input type="checkbox" '+(isActive?'checked':'')+' data-wid="'+id+'" onchange="wpickToggle(this)"><span class="wpick-icon">'+w.icon+'</span>'+esc(w.title)+'</label>';
    h+='<div class="wpick-arrows"><button onclick="wpickMove(\''+id+'\',-1)">\u25b2</button><button onclick="wpickMove(\''+id+'\',1)">\u25bc</button></div>';
    h+='</div>';
  }
  document.getElementById('wpick-list').innerHTML=h;
  document.getElementById('wpick-overlay').classList.add('open');
};
window.closeWidgetPicker=function(){document.getElementById('wpick-overlay').classList.remove('open');};
window.wpickToggle=function(cb){var item=cb.closest('.wpick-item');if(cb.checked)item.classList.add('active');else item.classList.remove('active');};
window.wpickMove=function(id,dir){
  var list=document.getElementById('wpick-list');var items=list.querySelectorAll('.wpick-item');
  var idx=-1;for(var i=0;i<items.length;i++){if(items[i].dataset.wid===id){idx=i;break;}}
  if(idx===-1)return;
  var newIdx=idx+dir;if(newIdx<0||newIdx>=items.length)return;
  var el=items[idx];if(dir<0)list.insertBefore(el,items[newIdx]);else list.insertBefore(items[newIdx],el);
};
window.saveWidgetPicker=function(){
  var items=document.getElementById('wpick-list').querySelectorAll('.wpick-item');
  var ids=[];
  for(var i=0;i<items.length;i++){var cb=items[i].querySelector('input[type=checkbox]');if(cb&&cb.checked)ids.push(items[i].dataset.wid);}
  if(ids.length===0)ids=getDefaultWidgets();
  saveActiveWidgets(ids);
  closeWidgetPicker();
  loadDashboard();
};
window.resetWidgetPicker=function(){
  try{localStorage.removeItem(DASH_STORAGE_KEY)}catch(e){}
  closeWidgetPicker();
  loadDashboard();
};

// ===== Dashboard Load (grouped fetch) =====
var _lastDashWidgets='';
window.loadDashboard=function(){
  if(dashTimer)clearTimeout(dashTimer);
  var widgets=getActiveWidgets();
  // Rebuild skeleton only if widgets set changed
  var wKey=widgets.join(',');
  var area=document.getElementById('dash-widget-area');
  if(wKey!==_lastDashWidgets){
    _lastDashWidgets=wKey;
    var skeletonHtml='';
    for(var i=0;i<widgets.length;i++){var w=WIDGETS[widgets[i]];if(w&&w.html)skeletonHtml+=w.html();}
    area.innerHTML=skeletonHtml;
  }
  // Group widgets by endpoint
  var endpointMap={};
  for(var i=0;i<widgets.length;i++){
    var wid=widgets[i];var w=WIDGETS[wid];if(!w)continue;
    if(!endpointMap[w.api])endpointMap[w.api]=[];
    endpointMap[w.api].push(wid);
  }
  // Fetch each unique endpoint
  var endpoints=Object.keys(endpointMap);
  var fetches=[];
  for(var i=0;i<endpoints.length;i++){
    (function(ep){
      fetches.push(api(ep).then(function(data){return{ep:ep,data:data}}).catch(function(e){return{ep:ep,data:null,err:e}}));
    })(endpoints[i]);
  }
  Promise.all(fetches).then(function(results){
    var dataMap={};
    for(var i=0;i<results.length;i++){if(results[i].data)dataMap[results[i].ep]=results[i].data;}
    // Render hero (always from /api/dashboard)
    var dd=dataMap['/api/dashboard'];
    if(dd){
      setT('dash-version',dd.version||'');
      var heroHtml='';
      heroHtml+='<div class="dash-hero-metric"><div class="hm-val">'+fmtUptime(dd.uptime_sec)+'</div><div class="hm-lbl">Аптайм</div></div>';
      heroHtml+='<div class="dash-hero-metric"><div class="hm-val">'+(dd.goroutines||0)+'</div><div class="hm-lbl">Горутины</div></div>';
      heroHtml+='<div class="dash-hero-metric"><div class="hm-val">'+(dd.mem_mb?dd.mem_mb.toFixed(0)+' MB':'-')+'</div><div class="hm-lbl">Память</div></div>';
      heroHtml+='<div class="dash-hero-metric"><div class="hm-val">'+(dd.num_cpu||'-')+'</div><div class="hm-lbl">CPU</div></div>';
      setH('dash-hero-metrics',heroHtml);
    }
    // Render each widget
    for(var i=0;i<widgets.length;i++){
      var wid=widgets[i];var w=WIDGETS[wid];if(!w||!w.render)continue;
      var wdata=dataMap[w.api];if(wdata){try{w.render(wdata)}catch(e){console.error('widget render error',wid,e);}}
    }
    // Schedule auto-refresh
    if(document.getElementById('dash-auto-refresh').checked){
      dashTimer=setTimeout(function(){
        var activeTab=document.querySelector('.tab.active');
        if(activeTab&&activeTab.dataset.tab==='dashboard')loadDashboard();
      },5000);
    }
  });
};

// --- Users ---
var usersCache=[];
var usersSearchQuery='';
// Group filter for the users table. '' = all, '_expired', '_no_group', or a group id.
// Filter matches against effective_group_id so a paid user shows under the premium pill.
var usersGroupFilter='';
// Group catalog cache for the pill row (loaded lazily).
var usersGroupCatalog=null;
var currentPanelUser=null;
var userBalGroups=[
  {key:'ru',label:'\u0420\u0443\u0441\u0441\u043a\u0438\u0435',icon:'\uD83C\uDFAC'},
  {key:'anime',label:'\u0410\u043d\u0438\u043c\u0435',icon:'\uD83C\uDF8C'},
  {key:'ua',label:'\u0423\u043a\u0440\u0430\u0438\u043d\u0441\u043a\u0438\u0435',icon:'\uD83C\uDDFA\uD83C\uDDE6'},
  {key:'video',label:'\u0412\u0438\u0434\u0435\u043e / \u0422\u0412',icon:'\uD83D\uDCFA'},
  {key:'en',label:'\u0410\u043d\u0433\u043b\u0438\u0439\u0441\u043a\u0438\u0435',icon:'\uD83C\uDF0D'},
  {key:'other',label:'\u0414\u0440\u0443\u0433\u0438\u0435',icon:'\u2699\uFE0F'}
];
window.usersPage=1;
window.loadUsers=function(){
  api('/api/users').then(function(users){
    usersCache=users||[];
    // Group catalog drives the filter-pill row. Loaded once and cached.
    if(usersGroupCatalog===null){
      api('/api/groups').then(function(d){
        usersGroupCatalog=(d&&d.groups)||[];
        renderUsers();
      }).catch(function(){usersGroupCatalog=[];renderUsers();});
    }else{
      renderUsers();
    }
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e.message,'error')});
  loadBKitSessions();
};
// renderUsersGroupPills paints the pill row above the table so the operator
// can slice users by their effective group (premium overlay wins).
function renderUsersGroupPills(){
  var el=document.getElementById('users-group-pills');
  if(!el)return;
  // Compute counts on the FULL cache (pre-search) so pill numbers don't
  // dance as the user types in the search box.
  var counts={_all:usersCache.length,_expired:0,_no_group:0};
  for(var i=0;i<usersCache.length;i++){
    var u=usersCache[i];
    if(u.expired)counts._expired++;
    var eff=u.effective_group_id||u.group_id||'';
    if(!eff&&!u.premium_active)counts._no_group++;
    if(eff)counts[eff]=(counts[eff]||0)+1;
  }
  var items=[{id:'',label:'\u0412\u0441\u0435',count:counts._all,tone:'info'}];
  var named=(usersGroupCatalog||[]).slice().map(function(g){
    return {id:g.id,label:g.name||g.id,count:counts[g.id]||0,isDefault:!!g.is_default};
  }).sort(function(a,b){return b.count-a.count;});
  for(var j=0;j<named.length;j++){
    var g=named[j];
    items.push({id:g.id,label:g.label+(g.isDefault?' (def)':''),count:g.count,tone:'accent'});
  }
  if(counts._no_group>0)items.push({id:'_no_group',label:'\u0411\u0435\u0437 \u0433\u0440\u0443\u043f\u043f\u044b',count:counts._no_group,tone:'neutral'});
  if(counts._expired>0)items.push({id:'_expired',label:'\u0418\u0441\u0442\u0451\u043a\u0448\u0438\u0435',count:counts._expired,tone:'danger'});
  var html='';
  for(var k=0;k<items.length;k++){
    var it=items[k];
    var active=(usersGroupFilter===it.id);
    // Base color depends on tone + active state.
    var bg=active?(it.tone==='danger'?'#dc2626':it.tone==='neutral'?'#475569':it.tone==='accent'?'#7c3aed':'#3b82f6'):'var(--surface2)';
    var color=active?'#fff':'var(--text)';
    var border=active?'transparent':'rgba(255,255,255,0.08)';
    html+='<button class="users-group-pill" onclick="setUsersGroupFilter(\''+esc(it.id)+'\')"'+
      ' style="display:inline-flex;align-items:center;gap:6px;padding:4px 10px;border-radius:999px;background:'+bg+';color:'+color+';border:1px solid '+border+';font-size:11px;font-weight:600;cursor:pointer">'+
      '<span>'+esc(it.label)+'</span>'+
      '<span style="background:'+(active?'rgba(255,255,255,0.18)':'var(--surface)')+';padding:0 6px;border-radius:999px;font-variant-numeric:tabular-nums">'+it.count+'</span>'+
      '</button>';
  }
  el.innerHTML=html;
}
window.setUsersGroupFilter=function(id){
  // Toggle: clicking the active pill clears the filter.
  usersGroupFilter=(usersGroupFilter===id)?'':id;
  window.usersPage=1;
  renderUsers();
};
window.renderUsers=function(){
  renderUsersGroupPills();
  var users=usersCache;
  // Group filter \u2014 applied BEFORE search so the pill counts reflect the
  // full cache (re-computed in renderUsersGroupPills from usersCache).
  if(usersGroupFilter){
    users=users.filter(function(u){
      if(usersGroupFilter==='_expired')return !!u.expired;
      if(usersGroupFilter==='_no_group')return !u.group_id&&!u.premium_active;
      var eff=u.effective_group_id||u.group_id||'';
      return eff===usersGroupFilter;
    });
  }
  // Search filter
  var sq=usersSearchQuery.toLowerCase().trim();
  if(sq){
    users=users.filter(function(u){
      if(u.tg_username&&u.tg_username.toLowerCase().indexOf(sq)!==-1)return true;
      if((''+u.telegram_id).indexOf(sq)!==-1)return true;
      if(u.devices){for(var di=0;di<u.devices.length;di++){if(u.devices[di].uid&&u.devices[di].uid.toLowerCase().indexOf(sq)!==-1)return true;if(u.devices[di].last_ip&&u.devices[di].last_ip.indexOf(sq)!==-1)return true}}
      return false;
    });
  }
  var b=document.getElementById('users-body'),e=document.getElementById('users-empty'),ctr=document.getElementById('users-counter'),pg=document.getElementById('users-pagination');
  var total=users.length,active=0,expired=0,premium=0;
  for(var i=0;i<total;i++){if(users[i].expired)expired++;else active++;if(users[i].premium_active)premium++;}
  ctr.innerHTML='<span class="users-stat">\u0412\u0441\u0435\u0433\u043e: <b>'+total+'</b> \u0410\u043a\u0442\u0438\u0432\u043d\u044b\u0445: <b>'+active+'</b> \u041F\u0440\u0435\u043C\u0438\u0443\u043C: <b style="color:#a78bfa">'+premium+'</b> <span class="expired-count">\u0418\u0441\u0442\u0451\u043a\u0448\u0438\u0445: '+expired+'</span></span>';
  if(!total){b.innerHTML='';e.style.display='';pg.innerHTML='';return}
  e.style.display='none';
  var pp=parseInt(document.getElementById('users-per-page').value)||0;
  var totalPages=pp>0?Math.ceil(total/pp):1;
  if(usersPage>totalPages)usersPage=totalPages;
  if(usersPage<1)usersPage=1;
  var start=pp>0?(usersPage-1)*pp:0;
  var end=pp>0?Math.min(start+pp,total):total;
  var slice=users.slice(start,end);
  b.innerHTML=slice.map(function(u){
    var c=u.expired?'expired':'active-user';
    var n=u.tg_username?'@'+u.tg_username:'ID:'+u.telegram_id;
    var devCount=u.device_count||0;
    var limitVal=u.max_devices||0;
    var limitLabel=limitVal>0?('<span style="color:#a78bfa;font-weight:600">'+limitVal+'</span>'):(limitVal<0?('<span style="color:#4ade80;font-weight:600">\u221E</span>'):('<span style="color:var(--text-dim);font-size:11px">def</span>'));
    // Group cell now shows the EFFECTIVE group, with a gold dot if it came
    // from the premium overlay (so admin can tell base vs overlay at a glance).
    var eff=u.effective_group_id||u.group_id||'';
    var grpLabel;
    if(!eff){
      grpLabel='<span style="color:var(--text-dim);font-size:11px">def</span>';
    }else if(u.premium_active&&u.effective_group_id&&u.effective_group_id!==u.group_id){
      grpLabel='<span style="color:#fbbf24;font-size:11px" title="\u041F\u0440\u0435\u043C\u0438\u0443\u043C-\u043e\u0432\u0435\u0440\u043B\u0435\u0439 \u0434\u043e '+esc(u.premium_until)+'">\u2605 '+esc(eff)+'</span>'+
        (u.group_id?'<span style="color:var(--text-dim);font-size:10px;margin-left:4px">\u0431\u0430\u0437\u0430: '+esc(u.group_id)+'</span>':'');
    }else{
      grpLabel='<span style="color:#a78bfa;font-size:11px">'+esc(eff)+'</span>';
    }
    // Premium column \u2014 three states.
    var premLabel;
    if(!u.premium_until){
      premLabel='<span style="color:var(--text-dim);font-size:11px">\u2014</span>';
    }else if(u.premium_active){
      premLabel='<span style="color:#34d399;font-size:11px;font-weight:600" title="'+esc(u.premium_until)+'">\u0434\u043e '+esc((u.premium_until||'').slice(0,10))+'</span>';
    }else{
      premLabel='<span style="color:var(--text-dim);font-size:11px" title="'+esc(u.premium_until)+'">\u0438\u0441\u0442\u0451\u043a</span>';
    }
    return '<tr class="user-row" onclick="openUserPanel(\''+esc(u.token)+'\')" data-token="'+esc(u.token)+'">'
      +'<td class="'+c+'">'+esc(n)+'</td>'
      +'<td>'+u.telegram_id+'</td>'
      +'<td>'+grpLabel+'</td>'
      +'<td>'+premLabel+'</td>'
      +'<td><span style="color:var(--text-muted)">\uD83D\uDCF1 '+devCount+'</span></td>'
      +'<td>'+limitLabel+'</td>'
      +'<td>'+u.created_at+'</td>'
      +'<td class="'+c+'">'+u.expires_at+(u.expired?' (\u0438\u0441\u0442\u0451\u043a)':'')+'</td>'
      +'<td><span style="color:var(--accent);font-size:12px">\u25B6</span></td></tr>';
  }).join('');
  // Pagination
  if(totalPages<=1){pg.innerHTML='';return}
  var ph='<button class="pg-btn" onclick="usersPage=1;renderUsers()" '+(usersPage===1?'disabled':'')+'>«</button>';
  ph+='<button class="pg-btn" onclick="usersPage--;renderUsers()" '+(usersPage===1?'disabled':'')+'>\u2039</button>';
  var from=Math.max(1,usersPage-2),to=Math.min(totalPages,usersPage+2);
  if(from>1)ph+='<span style="color:var(--text-dim)">...</span>';
  for(var p=from;p<=to;p++){ph+='<button class="pg-btn'+(p===usersPage?' active':'')+'" onclick="usersPage='+p+';renderUsers()">'+p+'</button>'}
  if(to<totalPages)ph+='<span style="color:var(--text-dim)">...</span>';
  ph+='<button class="pg-btn" onclick="usersPage++;renderUsers()" '+(usersPage===totalPages?'disabled':'')+'>\u203A</button>';
  ph+='<button class="pg-btn" onclick="usersPage='+totalPages+';renderUsers()" '+(usersPage===totalPages?'disabled':'')+'>\u00BB</button>';
  ph+='<span style="color:var(--text-dim);font-size:11px;margin-left:8px">'+(start+1)+'\u2013'+end+' \u0438\u0437 '+total+'</span>';
  pg.innerHTML=ph;
}
function findUser(token){for(var i=0;i<usersCache.length;i++){if(usersCache[i].token===token)return usersCache[i]}return null}
function copyText(text){
  if(navigator.clipboard)navigator.clipboard.writeText(text).then(function(){toast('\u0421\u043a\u043e\u043f\u0438\u0440\u043e\u0432\u0430\u043d\u043e')});
  else{var t=document.createElement('textarea');t.value=text;document.body.appendChild(t);t.select();document.execCommand('copy');document.body.removeChild(t);toast('\u0421\u043a\u043e\u043f\u0438\u0440\u043e\u0432\u0430\u043d\u043e')}
}
window.openUserPanel=function(token){
  var u=findUser(token);
  if(!u)return;
  currentPanelUser=u;
  var el=document.getElementById('user-panel-content');
  var n=u.tg_username?'@'+u.tg_username:'device';
  var initials=(n[0]||'?').toUpperCase();
  if(initials==='@'&&n.length>1)initials=n[1].toUpperCase();
  var isExp=u.expired;
  var statusCls=isExp?'expired':'active';
  var statusText=isExp?'\u0418\u0441\u0442\u0451\u043a':'\u0410\u043a\u0442\u0438\u0432\u0435\u043d';
  var host=window.location.origin;
  var lampaUrl=host+'/on/js/'+u.token;
  var maskedToken=u.token.substring(0,8)+'\u2026'+u.token.substring(u.token.length-4);
  // Premium chip rendered next to status — gold tint when active so the
  // operator can see at a glance that user is currently on premium tier.
  var premiumChip='';
  if(u.premium_active){
    premiumChip='<div class="up-status" style="background:rgba(168,85,247,0.15);color:#c084fc;border:1px solid rgba(168,85,247,0.35);margin-left:8px" title="Премиум до '+esc(u.premium_until)+'"><span class="dot" style="background:#c084fc"></span> Премиум до '+esc((u.premium_until||'').slice(0,10))+'</div>';
  }else if(u.premium_until){
    premiumChip='<div class="up-status" style="background:rgba(100,116,139,0.15);color:#94a3b8;border:1px solid rgba(100,116,139,0.35);margin-left:8px" title="Премиум закончился '+esc(u.premium_until)+'"><span class="dot" style="background:#94a3b8"></span> Премиум истёк</div>';
  }
  var html='<div class="up-header">'
    +'<div class="up-avatar">'+esc(initials)+'</div>'
    +'<div class="up-name">'+esc(n)+'</div>'
    +'<div class="up-tgid">TG: '+u.telegram_id+'</div>'
    +'<div class="up-status '+statusCls+'"><span class="dot"></span> '+statusText+'</div>'
    +premiumChip
    +'</div><div class="up-body">';
  // Section: Info
  html+='<div class="up-section">'
    +'<div class="up-section-head" onclick="toggleUpSection(this)"><span class="up-section-icon open">\u25B6</span><span class="up-section-title">\u0418\u043d\u0444\u043e\u0440\u043c\u0430\u0446\u0438\u044f</span></div>'
    +'<div class="up-section-body open">'
    +'<div class="up-info-row"><span class="up-info-label">Token</span><span class="up-info-val">'+esc(maskedToken)+'</span><button class="up-copy" onclick="event.stopPropagation();copyText(\''+esc(u.token)+'\')">\uD83D\uDCCB</button></div>'
    +'<div class="up-info-row"><span class="up-info-label">Lampa</span><span class="up-info-val">'+esc(lampaUrl)+'</span><button class="up-copy" onclick="event.stopPropagation();copyText(\''+esc(lampaUrl)+'\')">\uD83D\uDCCB</button></div>'
    +'<div class="up-info-row"><span class="up-info-label">\u0421\u043e\u0437\u0434\u0430\u043d</span><span class="up-info-val" style="font-family:inherit">'+esc(u.created_at)+'</span></div>'
    +'<div class="up-info-row"><span class="up-info-label">\u0418\u0441\u0442\u0435\u043a\u0430\u0435\u0442 (\u0441\u0442\u0430\u043d\u0434\u0430\u0440\u0442)</span><span class="up-info-val '+(isExp?'expired':'')+'" style="font-family:inherit">'+esc(u.expires_at)+(isExp?' (\u0438\u0441\u0442\u0451\u043a)':'')+'</span></div>'
    +'<div class="up-info-row"><span class="up-info-label">\u041f\u0440\u0435\u043c\u0438\u0443\u043c \u0434\u043e</span><span class="up-info-val" style="font-family:inherit;'+(u.premium_active?'color:#c084fc;font-weight:600':'color:var(--text-dim)')+'">'+(u.premium_until?esc(u.premium_until)+(u.premium_active?'':' (\u0438\u0441\u0442\u0451\u043a)'):'\u2014')+'</span></div>'
    +'<div class="up-info-row"><span class="up-info-label">\u0413\u0440\u0443\u043f\u043f\u0430 (\u0431\u0430\u0437\u0430)</span><span class="up-info-val"><select id="up-group-select" onchange="assignUserGroup(\''+esc(u.token)+'\',this.value)" style="background:var(--surface2);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:3px 8px;font-size:12px;cursor:pointer"><option value="">\u0417\u0430\u0433\u0440\u0443\u0437\u043a\u0430...</option></select>'+(u.premium_active&&u.effective_group_id!==u.group_id?'<span style="margin-left:8px;color:#c084fc;font-size:11px" title="\u0410\u043a\u0442\u0438\u0432\u043d\u044b\u0439 \u043f\u0440\u0435\u043c\u0438\u0443\u043c-\u043e\u0432\u0435\u0440\u043b\u0435\u0439">\u2192 '+esc(u.effective_group_id)+' (\u043e\u0432\u0435\u0440\u043b\u0435\u0439)</span>':'')+'</span></div>'
    +'<div class="up-actions">'
    +'<button class="btn btn-sm" style="background:var(--surface2);color:var(--text);border:1px solid rgba(255,255,255,0.08)" onclick="event.stopPropagation();extendUser(\''+esc(u.token)+'\',30)" title="\u041F\u0440\u043E\u0434\u043b\u0438\u0442\u044c \u0431\u0430\u0437\u043E\u0432\u044B\u0439 \u0442\u0430\u0439\u043c\u0435\u0440 (\u0441\u0442\u0430\u043D\u0434\u0430\u0440\u0442)">\u0411\u0430\u0437\u0430 +30\u0434</button>'
    +'<button class="btn btn-sm" style="background:var(--surface2);color:var(--text);border:1px solid rgba(255,255,255,0.08)" onclick="event.stopPropagation();extendUser(\''+esc(u.token)+'\',90)" title="\u041F\u0440\u043E\u0434\u043b\u0438\u0442\u044c \u0431\u0430\u0437\u043E\u0432\u044B\u0439 \u0442\u0430\u0439\u043c\u0435\u0440 (\u0441\u0442\u0430\u043D\u0434\u0430\u0440\u0442)">\u0411\u0430\u0437\u0430 +90\u0434</button>'
    +'<button class="btn btn-warning btn-sm" onclick="event.stopPropagation();setDeviceLimit(\''+esc(u.token)+'\','+(u.max_devices||0)+')">\u041b\u0438\u043c\u0438\u0442: '+(u.max_devices>0?u.max_devices:(u.max_devices<0?'\u221E':'def'))+'</button>'
    +'<button class="btn btn-sm" style="background:'+(u.torrserver_disabled?'#dc2626':'#059669')+';color:#fff" onclick="event.stopPropagation();toggleTorrServer(\''+esc(u.token)+'\','+(!u.torrserver_disabled)+')">\uD83E\uDDF2 TS: '+(u.torrserver_disabled?'\u2717 \u0412\u044B\u043A\u043b':'\u2713 \u0412\u043A\u043b')+'</button>'
    +'<button class="btn btn-danger btn-sm" onclick="event.stopPropagation();removeUser(\''+esc(u.token)+'\')">\u0423\u0434\u0430\u043b\u0438\u0442\u044c</button>'
    +(u.telegram_id?'<button class="btn btn-sm" style="background:#6b2150;color:#fff" onclick="event.stopPropagation();quickBan(\'tg_id\',\''+u.telegram_id+'\',\'user: '+esc(n)+'\')">\uD83D\uDEAB TG</button>':'')
    +'</div></div></div>';
  // Section: Premium (overlay timer)
  html+='<div class="up-section">'
    +'<div class="up-section-head" onclick="toggleUpSection(this)"><span class="up-section-icon open">\u25B6</span><span class="up-section-title">\u2605 \u041F\u0440\u0435\u043c\u0438\u0443\u043c (\u043E\u0432\u0435\u0440\u043b\u0435\u0439)</span>'
    +(u.premium_active?'<span class="up-section-badge" style="background:rgba(168,85,247,0.15);color:#c084fc">\u0434\u043E '+esc((u.premium_until||'').slice(0,10))+'</span>':'')
    +'</div>'
    +'<div class="up-section-body open">'
    +'<div style="color:var(--text-dim);font-size:11px;margin-bottom:8px;line-height:1.4">'
    +'\u041F\u0440\u0435\u043c\u0438\u0443\u043c-\u0442\u0430\u0439\u043c\u0435\u0440 \u043D\u0435 \u0442\u0440\u043E\u0433\u0430\u0435\u0442 \u0431\u0430\u0437\u043E\u0432\u044B\u0439 \u00AB\u0441\u0442\u0430\u043D\u0434\u0430\u0440\u0442\u00BB. \u041F\u043E\u043A\u0430 \u0430\u043A\u0442\u0438\u0432\u0435\u043D \u2014 \u043F\u043E\u043b\u044c\u0437\u043E\u0432\u0430\u0442\u0435\u043b\u044c \u0432 \u043F\u0440\u0435\u043c\u0438\u0443\u043c-\u0433\u0440\u0443\u043F\u043F\u0435, \u043F\u043E\u0441\u043b\u0435 \u0438\u0441\u0442\u0435\u0447\u0435\u043D\u0438\u044F \u0432\u043E\u0437\u0432\u0440\u0430\u0449\u0430\u0435\u0442\u0441\u044F \u0432 \u0431\u0430\u0437\u043E\u0432\u0443\u044E'+(u.group_id?' (<code style="color:var(--text)">'+esc(u.group_id)+'</code>)':'')+'.'
    +'</div>'
    +'<div class="up-actions" style="margin-top:4px">'
    +'<button class="btn btn-primary btn-sm" style="background:#7c3aed" onclick="event.stopPropagation();extendUserPremium(\''+esc(u.token)+'\',30)">\u2605 +30\u0434</button>'
    +'<button class="btn btn-primary btn-sm" style="background:#7c3aed" onclick="event.stopPropagation();extendUserPremium(\''+esc(u.token)+'\',90)">\u2605 +90\u0434</button>'
    +'<button class="btn btn-primary btn-sm" style="background:#7c3aed" onclick="event.stopPropagation();extendUserPremium(\''+esc(u.token)+'\',180)">\u2605 +180\u0434</button>'
    +'<button class="btn btn-primary btn-sm" style="background:#7c3aed" onclick="event.stopPropagation();extendUserPremium(\''+esc(u.token)+'\',365)">\u2605 +365\u0434</button>'
    +'</div>'
    +'<div style="margin-top:8px;display:flex;gap:6px;flex-wrap:wrap;align-items:center">'
    +'<input id="up-premium-date" type="datetime-local" value="'+esc(premiumInputValueLegacy(u))+'" style="padding:5px 8px;background:var(--surface2);color:var(--text);border:1px solid var(--border);border-radius:6px;font-size:12px">'
    +'<button class="btn btn-sm" style="background:var(--surface2);color:var(--text);border:1px solid rgba(255,255,255,0.08)" onclick="event.stopPropagation();setUserPremiumDate(\''+esc(u.token)+'\')">\u0423\u0441\u0442\u0430\u043D\u043E\u0432\u0438\u0442\u044c \u0434\u0430\u0442\u0443</button>'
    +(u.premium_until?'<button class="btn btn-danger btn-sm" onclick="event.stopPropagation();clearUserPremium(\''+esc(u.token)+'\')">\u041E\u0447\u0438\u0441\u0442\u0438\u0442\u044c \u043F\u0440\u0435\u043c\u0438\u0443\u043c</button>':'')
    +'</div>'
    +'</div></div>';
  // Section: Devices
  var devCount=u.devices?u.devices.length:0;
  html+='<div class="up-section">'
    +'<div class="up-section-head" onclick="toggleUpSection(this)"><span class="up-section-icon open">\u25B6</span><span class="up-section-title">\u0423\u0441\u0442\u0440\u043e\u0439\u0441\u0442\u0432\u0430</span><span class="up-section-badge">'+devCount+'</span></div>'
    +'<div class="up-section-body open">';
  if(devCount){
    u.devices.forEach(function(d){
      html+='<div class="up-device">'
        +'<span class="up-device-icon">\uD83D\uDCF1</span>'
        +'<div class="up-device-info">'
        +'<div class="up-device-uid">'+esc(d.uid)+'</div>'
        +'<div class="up-device-label">'+esc(d.label||'\u2014')+'</div>'
        +'<div class="up-device-meta">\u041F\u0440\u0438\u0432\u044F\u0437\u0430\u043D: '+esc(d.bound_at)+' \u00B7 \u041F\u043E\u0441\u043B\u0435\u0434\u043D\u0438\u0439: '+esc(d.last_seen)+'</div>'
        +(d.last_ip?'<div class="up-device-meta" style="color:var(--accent)">IP: '+esc(d.last_ip)+'</div>':'')
        +(d.fingerprint?'<div class="up-device-meta" style="color:#a78bfa">FP: '+esc(d.fingerprint)+'</div>':'')
        +'<div class="up-device-bans" style="display:flex;gap:4px;margin-top:4px;flex-wrap:wrap">'
        +'<button class="btn btn-sm" style="background:#6b2150;color:#fff;font-size:10px;padding:2px 6px" onclick="event.stopPropagation();quickBan(\'uid\',\''+esc(d.uid)+'\',\'device of '+esc(n)+'\')">\uD83D\uDEAB UID</button>'
        +(d.last_ip?'<button class="btn btn-sm" style="background:#6b2150;color:#fff;font-size:10px;padding:2px 6px" onclick="event.stopPropagation();quickBan(\'ip\',\''+esc(d.last_ip)+'\',\'device of '+esc(n)+'\')">\uD83D\uDEAB IP</button>':'')
        +(d.fingerprint?'<button class="btn btn-sm" style="background:#6b2150;color:#fff;font-size:10px;padding:2px 6px" onclick="event.stopPropagation();quickBan(\'fingerprint\',\''+esc(d.fingerprint)+'\',\'device of '+esc(n)+'\')">\uD83D\uDEAB FP</button>':'')
        +'</div>'
        +'</div>'
        +'<button class="up-device-rm" onclick="event.stopPropagation();removeDevice(\''+esc(u.token)+'\',\''+esc(d.uid)+'\')" title="\u041e\u0442\u0432\u044f\u0437\u0430\u0442\u044c">\u2715</button>'
        +'</div>';
    });
  }else{
    html+='<div style="color:var(--text-dim);font-size:12px;padding:8px 0">\u041d\u0435\u0442 \u043f\u0440\u0438\u0432\u044f\u0437\u0430\u043d\u043d\u044b\u0445 \u0443\u0441\u0442\u0440\u043e\u0439\u0441\u0442\u0432</div>';
  }
  html+='</div></div>';
  // Section: Balancers (loaded async)
  html+='<div class="up-section">'
    +'<div class="up-section-head" onclick="toggleUpSection(this)"><span class="up-section-icon">\u25B6</span><span class="up-section-title">\u0411\u0430\u043b\u0430\u043d\u0441\u0435\u0440\u044b</span></div>'
    +'<div class="up-section-body" id="up-bal-body"><div style="color:var(--text-dim);font-size:12px;padding:8px 0">\u0417\u0430\u0433\u0440\u0443\u0437\u043a\u0430...</div></div>'
    +'</div>';
  html+='</div>';
  el.innerHTML=html;
  document.getElementById('user-panel').classList.add('open');
  document.getElementById('user-panel-overlay').classList.add('open');
  document.body.style.overflow='hidden';
  // Load balancers
  loadUserBalancers(u.token);
  // Load groups into dropdown
  loadUserGroupSelector(u.token, u.group_id||'');
};
window.assignUserGroup=function(token,groupId){
  post('/api/users',{action:'assign_group',token:token,group_id:groupId}).then(function(d){
    if(d.ok){
      toast('\u0413\u0440\u0443\u043f\u043f\u0430 \u043d\u0430\u0437\u043d\u0430\u0447\u0435\u043d\u0430');
      var u=findUser(token);if(u)u.group_id=groupId;
    }else{toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error')}
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};
function loadUserGroupSelector(token,currentGroupId){
  var sel=document.getElementById('up-group-select');
  if(!sel)return;
  api('/api/groups',{}).then(function(d){
    var groups=d.groups||[];
    var html='';
    if(!groups.length){
      html='<option value="">\u041d\u0435\u0442 \u0433\u0440\u0443\u043f\u043f</option>';
    }else{
      for(var i=0;i<groups.length;i++){
        var g=groups[i];
        var label=esc(g.name||g.id);
        if(g.is_default)label+=' (\u043f\u043e \u0443\u043c\u043e\u043b\u0447.)';
        var selected=(g.id===currentGroupId||(currentGroupId===''&&g.is_default))?' selected':'';
        html+='<option value="'+esc(g.id)+'"'+selected+'>'+label+'</option>';
      }
    }
    sel.innerHTML=html;
  }).catch(function(){sel.innerHTML='<option value="">\u041e\u0448\u0438\u0431\u043a\u0430</option>'});
}
window.closeUserPanel=function(){
  document.getElementById('user-panel').classList.remove('open');
  document.getElementById('user-panel-overlay').classList.remove('open');
  document.body.style.overflow='';
  currentPanelUser=null;
};
window.toggleUpSection=function(head){
  var icon=head.querySelector('.up-section-icon');
  var body=head.nextElementSibling;
  if(body){body.classList.toggle('open');icon.classList.toggle('open')}
};
function loadUserBalancers(token){
  post('/api/users',{action:'get_balancers',token:token}).then(function(r){
    if(r.error){document.getElementById('up-bal-body').innerHTML='<div style="color:var(--danger);font-size:12px">'+esc(r.error)+'</div>';return}
    renderUserBalancers(token,r.balancers||[]);
  }).catch(function(){document.getElementById('up-bal-body').innerHTML='<div style="color:var(--danger);font-size:12px">\u041e\u0448\u0438\u0431\u043a\u0430 \u0437\u0430\u0433\u0440\u0443\u0437\u043a\u0438</div>'});
}
function renderUserBalancers(token,bals){
  var el=document.getElementById('up-bal-body');
  if(!el)return;
  var grouped={};
  userBalGroups.forEach(function(g){grouped[g.key]=[]});
  bals.forEach(function(b){
    var g=b.group||'other';
    if(!grouped[g])grouped[g]=[];
    grouped[g].push(b);
  });
  var html='';
  userBalGroups.forEach(function(g){
    var items=grouped[g.key];
    if(!items||!items.length)return;
    var allOn=items.every(function(b){return b.user_visible===null||b.user_visible===undefined?true:b.user_visible});
    html+='<div class="up-bal-group">'
      +'<div class="up-bal-group-head" onclick="this.nextElementSibling.classList.toggle(\'open\')">'
      +'<span class="up-bal-group-icon">'+g.icon+'</span>'
      +'<span class="up-bal-group-title">'+g.label+'</span>'
      +'<span class="up-bal-group-toggle'+(allOn?' all-on':'')+'" onclick="event.stopPropagation();toggleGroupBal(\''+esc(token)+'\',\''+g.key+'\','+(!allOn)+')">'+(allOn?'\u0412\u0441\u0435 \u0432\u043a\u043b':'\u0412\u043a\u043b \u0432\u0441\u0435')+'</span>'
      +'</div><div class="up-bal-items open">';
    items.forEach(function(b){
      var gOff=!b.global_enabled;
      var checked=gOff?false:((b.user_visible===null||b.user_visible===undefined)?true:b.user_visible);
      var qb=b.quality?'<span class="badge-quality badge-quality-'+b.quality+'" style="font-size:9px;padding:1px 5px;margin-left:4px">'+b.quality+'</span>':'';
      html+='<div class="up-bal-item'+(gOff?' globally-off':'')+'">'
        +'<label class="toggle" style="flex-shrink:0"><input type="checkbox" '+(checked?'checked':'')+(gOff?' disabled':'')+' onchange="toggleUserBal(\''+esc(token)+'\',\''+esc(b.key)+'\',this.checked)"><span class="slider"></span></label>'
        +'<span class="name">'+esc(b.name)+qb+'</span>'
        +(gOff?'<span class="off-hint">\u0432\u044b\u043a\u043b. \u0430\u0434\u043c\u0438\u043d\u043e\u043c</span>':'')
        +'</div>';
    });
    html+='</div></div>';
  });
  html+='<div class="up-bal-reset"><button class="btn btn-sm" style="color:var(--text-muted);border:1px solid rgba(255,255,255,0.06)" onclick="resetUserBal(\''+esc(token)+'\')">\u0421\u0431\u0440\u043e\u0441\u0438\u0442\u044c \u043d\u0430 \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044f</button></div>';
  el.innerHTML=html;
}
window.toggleUserBal=function(token,key,visible){
  // Collect current state from DOM
  var vis=collectBalVisibility();
  vis[key]=visible;
  post('/api/users',{action:'save_balancers',token:token,visibility:vis}).then(function(r){
    if(!r.ok)toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  });
};
window.toggleGroupBal=function(token,groupKey,enable){
  var el=document.getElementById('up-bal-body');
  if(!el)return;
  // Find all toggles in this group and set them
  var groups=el.querySelectorAll('.up-bal-group');
  var groupIdx=-1;
  userBalGroups.forEach(function(g,i){if(g.key===groupKey)groupIdx=i});
  if(groupIdx<0)return;
  var grp=groups[groupIdx];
  if(!grp)return;
  var checks=grp.querySelectorAll('input[type="checkbox"]');
  checks.forEach(function(c){c.checked=enable});
  var vis=collectBalVisibility();
  post('/api/users',{action:'save_balancers',token:token,visibility:vis}).then(function(r){
    if(r.ok){loadUserBalancers(token)}else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  });
};
window.resetUserBal=function(token){
  if(!confirm('\u0421\u0431\u0440\u043e\u0441\u0438\u0442\u044c \u043d\u0430\u0441\u0442\u0440\u043e\u0439\u043a\u0438 \u0431\u0430\u043b\u0430\u043d\u0441\u0435\u0440\u043e\u0432 \u043d\u0430 \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044f?'))return;
  post('/api/users',{action:'save_balancers',token:token}).then(function(r){
    if(r.ok){toast('\u0421\u0431\u0440\u043e\u0448\u0435\u043d\u043e');loadUserBalancers(token)}else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  });
};
function collectBalVisibility(){
  var vis={};
  var el=document.getElementById('up-bal-body');
  if(!el)return vis;
  var items=el.querySelectorAll('.up-bal-item');
  items.forEach(function(item){
    var cb=item.querySelector('input[type="checkbox"]');
    var nameEl=item.querySelector('.name');
    if(!cb||!nameEl)return;
    // Extract key from onchange attribute
    var oc=cb.getAttribute('onchange')||'';
    var m=oc.match(/toggleUserBal\('[^']+','([^']+)'/);
    if(m)vis[m[1]]=cb.checked;
  });
  return vis;
}
window.removeDevice=function(t,uid){if(!confirm('\u041e\u0442\u0432\u044f\u0437\u0430\u0442\u044c \u0443\u0441\u0442\u0440\u043e\u0439\u0441\u0442\u0432\u043e '+uid+'?'))return;post('/api/users',{action:'remove_device',token:t,uid:uid}).then(function(r){if(r.ok){toast('\u0423\u0441\u0442\u0440\u043e\u0439\u0441\u0442\u0432\u043e \u043e\u0442\u0432\u044f\u0437\u0430\u043d\u043e');loadUsers();if(currentPanelUser&&currentPanelUser.token===t)openUserPanel(t)}else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error')})};
window.removeUser=function(t){if(!confirm('\u0423\u0434\u0430\u043b\u0438\u0442\u044c \u0442\u043e\u043a\u0435\u043d?'))return;post('/api/users',{action:'remove',token:t}).then(function(){toast('\u0423\u0434\u0430\u043b\u0451\u043d');closeUserPanel();loadUsers()})};
window.extendUser=function(t,d){post('/api/users',{action:'extend',token:t,days:d}).then(function(){toast('\u041f\u0440\u043e\u0434\u043b\u0451\u043d \u043d\u0430 '+d+' \u0434\u043d\u0435\u0439');loadUsers();if(currentPanelUser&&currentPanelUser.token===t)setTimeout(function(){openUserPanel(t)},300)})};
// Premium-overlay helpers \u2014 mirror /api/users actions extend_premium /
// set_premium_until / clear_premium added in admin_panel_users.go.
window.extendUserPremium=function(t,d){
  post('/api/users',{action:'extend_premium',token:t,days:d}).then(function(r){
    if(r&&r.error){toast(r.error,'error');return}
    toast('\u2605 \u041f\u0440\u0435\u043c\u0438\u0443\u043c +'+d+' \u0434\u043d\u0435\u0439'+(r&&r.premium_until?' (\u0434\u043e '+r.premium_until+')':''));
    loadUsers();
    if(currentPanelUser&&currentPanelUser.token===t)setTimeout(function(){openUserPanel(t)},300);
  });
};
window.setUserPremiumDate=function(t){
  var inp=document.getElementById('up-premium-date');
  if(!inp||!inp.value){toast('\u0423\u043a\u0430\u0436\u0438 \u0434\u0430\u0442\u0443','error');return}
  // <input type="datetime-local"> emits "YYYY-MM-DDTHH:MM" \u2014 backend
  // accepts space-separated; convert here.
  var v=inp.value.replace('T',' ').slice(0,16);
  post('/api/users',{action:'set_premium_until',token:t,premium_until:v}).then(function(r){
    if(r&&r.error){toast(r.error,'error');return}
    toast('\u2605 \u041f\u0440\u0435\u043c\u0438\u0443\u043c \u0443\u0441\u0442\u0430\u043d\u043e\u0432\u043b\u0435\u043d'+(r&&r.premium_until?' (\u0434\u043e '+r.premium_until+')':''));
    loadUsers();
    if(currentPanelUser&&currentPanelUser.token===t)setTimeout(function(){openUserPanel(t)},300);
  });
};
window.clearUserPremium=function(t){
  if(!confirm('\u0421\u043d\u044f\u0442\u044c \u043f\u0440\u0435\u043c\u0438\u0443\u043c? \u041f\u043e\u043b\u044c\u0437\u043e\u0432\u0430\u0442\u0435\u043b\u044c \u0441\u0440\u0430\u0437\u0443 \u0432\u044b\u043f\u0430\u0434\u0435\u0442 \u0432 \u0431\u0430\u0437\u043e\u0432\u0443\u044e \u0433\u0440\u0443\u043f\u043f\u0443.'))return;
  post('/api/users',{action:'clear_premium',token:t}).then(function(r){
    if(r&&r.error){toast(r.error,'error');return}
    toast('\u2605 \u041f\u0440\u0435\u043c\u0438\u0443\u043c \u043e\u0447\u0438\u0449\u0435\u043d');
    loadUsers();
    if(currentPanelUser&&currentPanelUser.token===t)setTimeout(function(){openUserPanel(t)},300);
  });
};
// premiumInputValueLegacy \u2014 convert "YYYY-MM-DD HH:MM" or empty into the
// "YYYY-MM-DDTHH:MM" format <input type="datetime-local"> expects. When
// the user has no premium yet, suggest "now + 30 days" so the operator
// can hit "\u0423\u0441\u0442\u0430\u043d\u043e\u0432\u0438\u0442\u044c" without typing.
function premiumInputValueLegacy(u){
  if(u&&u.premium_until)return u.premium_until.replace(' ','T').slice(0,16);
  var d=new Date();d.setDate(d.getDate()+30);
  var p=function(n){return (n<10?'0':'')+n};
  return d.getFullYear()+'-'+p(d.getMonth()+1)+'-'+p(d.getDate())+'T'+p(d.getHours())+':'+p(d.getMinutes());
}
window.setDeviceLimit=function(token,current){
  var hint=current>0?'\u0422\u0435\u043a\u0443\u0449\u0438\u0439: '+current+' (\u043f\u0435\u0440\u0441\u043e\u043d\u0430\u043b\u044c\u043d\u044b\u0439)\n0 = \u0441\u0435\u0440\u0432\u0435\u0440\u043d\u044b\u0439 \u043f\u043e \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044e\n-1 = \u0431\u0435\u0437\u043b\u0438\u043c\u0438\u0442':(current<0?'\u0422\u0435\u043a\u0443\u0449\u0438\u0439: \u221E (\u0431\u0435\u0437\u043b\u0438\u043c\u0438\u0442)\n0 = \u0441\u0435\u0440\u0432\u0435\u0440\u043d\u044b\u0439 \u043f\u043e \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044e':'\u0422\u0435\u043a\u0443\u0449\u0438\u0439: \u0441\u0435\u0440\u0432\u0435\u0440\u043d\u044b\u0439 \u043f\u043e \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044e\n0 = \u0441\u0435\u0440\u0432\u0435\u0440\u043d\u044b\u0439, >0 = \u043f\u0435\u0440\u0441\u043e\u043d\u0430\u043b\u044c\u043d\u044b\u0439, -1 = \u0431\u0435\u0437\u043b\u0438\u043c\u0438\u0442');
  var val=prompt(hint,current||0);
  if(val===null)return;
  val=parseInt(val);if(isNaN(val))val=0;if(val<-1)val=-1;
  post('/api/users',{action:'set_device_limit',token:token,max_devices:val}).then(function(r){
    if(r.ok){toast(val>0?'\u041f\u0435\u0440\u0441\u043e\u043d\u0430\u043b\u044c\u043d\u044b\u0439 \u043b\u0438\u043c\u0438\u0442: '+val:(val<0?'\u0411\u0435\u0437\u043b\u0438\u043c\u0438\u0442':'\u041b\u0438\u043c\u0438\u0442 \u0441\u0431\u0440\u043e\u0448\u0435\u043d'));loadUsers();if(currentPanelUser&&currentPanelUser.token===token)setTimeout(function(){openUserPanel(token)},300)}
    else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  });
};
window.toggleTorrServer=function(token,disabled){
  post('/api/users',{action:'toggle_torrserver',token:token,torrserver_disabled:disabled}).then(function(r){
    if(r.ok){toast(disabled?'TorrServer \u0432\u044b\u043a\u043b\u044e\u0447\u0435\u043d':'TorrServer \u0432\u043a\u043b\u044e\u0447\u0451\u043d');loadUsers();if(currentPanelUser&&currentPanelUser.token===token)setTimeout(function(){openUserPanel(token)},300)}
    else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  });
};
window.createDeviceToken=function(){
  var days=prompt('\u0421\u0440\u043e\u043a \u0434\u0435\u0439\u0441\u0442\u0432\u0438\u044f (\u0434\u043d\u0435\u0439):','30');
  if(days===null)return;
  days=parseInt(days)||30;
  post('/api/users',{action:'create',days:days}).then(function(r){
    if(r.ok){
      prompt('Device-\u0442\u043e\u043a\u0435\u043d \u0441\u043e\u0437\u0434\u0430\u043d!\n\n\u0414\u043b\u044f Lampa \u0432\u0432\u0435\u0434\u0438\u0442\u0435 \u0430\u0434\u0440\u0435\u0441:',r.lampa_url);
      toast('Device-\u0442\u043e\u043a\u0435\u043d \u0441\u043e\u0437\u0434\u0430\u043d');
      loadUsers();
    } else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  });
};

// --- Bans ---
var bansCache=[];
var banTypeLabels={ip:'IP',cidr:'CIDR',uid:'Device UID',tg_id:'Telegram ID',fingerprint:'Fingerprint',country:'\u0421\u0442\u0440\u0430\u043d\u0430'};
window.loadBans=function(){
  api('/api/bans').then(function(d){
    bansCache=d.rules||[];
    var stats=d.stats||{};
    var statsEl=document.getElementById('bans-stats');
    var parts=['\u0412\u0441\u0435\u0433\u043e: <b>'+bansCache.length+'</b>'];
    for(var k in stats){if(stats[k]>0)parts.push((banTypeLabels[k]||k)+': <b>'+stats[k]+'</b>')}
    statsEl.innerHTML='<span class="users-stat">'+parts.join(' \u00B7 ')+'</span>';
    renderBans();
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e.message,'error')});
};
function renderBans(){
  var b=document.getElementById('bans-body'),e=document.getElementById('bans-empty');
  if(!bansCache.length){b.innerHTML='';e.style.display='';return}
  e.style.display='none';
  b.innerHTML=bansCache.map(function(r){
    var typeLbl=banTypeLabels[r.type]||r.type;
    return '<tr>'
      +'<td><span style="background:rgba(255,255,255,0.06);padding:2px 8px;border-radius:4px;font-size:11px;color:#a78bfa">'+esc(typeLbl)+'</span></td>'
      +'<td style="font-family:monospace;font-size:13px">'+esc(r.value)+'</td>'
      +'<td style="color:var(--text-muted);font-size:12px">'+esc(r.reason||'\u2014')+'</td>'
      +'<td style="font-size:12px;color:var(--text-dim)">'+esc(r.created_at?r.created_at.substring(0,16).replace('T',' '):'\u2014')+'</td>'
      +'<td><button class="btn btn-danger btn-sm" onclick="removeBan(\''+esc(r.id)+'\')">\u2715</button></td>'
      +'</tr>';
  }).join('');
}
window.showAddBanForm=function(){document.getElementById('ban-add-form').style.display=''};
window.addBan=function(){
  var type=document.getElementById('ban-type').value;
  var value=document.getElementById('ban-value').value.trim();
  var reason=document.getElementById('ban-reason').value.trim();
  if(!value){toast('\u0423\u043a\u0430\u0436\u0438\u0442\u0435 \u0437\u043d\u0430\u0447\u0435\u043d\u0438\u0435','error');return}
  post('/api/bans',{action:'add',type:type,value:value,reason:reason}).then(function(r){
    if(r.ok){
      toast('\u0411\u043b\u043e\u043a\u0438\u0440\u043e\u0432\u043a\u0430 \u0434\u043e\u0431\u0430\u0432\u043b\u0435\u043d\u0430');
      document.getElementById('ban-value').value='';
      document.getElementById('ban-reason').value='';
      document.getElementById('ban-add-form').style.display='none';
      loadBans();
    } else {
      toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
    }
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e.message,'error')});
};
window.removeBan=function(id){
  if(!confirm('\u0423\u0434\u0430\u043b\u0438\u0442\u044c \u0431\u043b\u043e\u043a\u0438\u0440\u043e\u0432\u043a\u0443?'))return;
  post('/api/bans',{action:'remove',id:id}).then(function(r){
    if(r.ok){toast('\u0411\u043b\u043e\u043a\u0438\u0440\u043e\u0432\u043a\u0430 \u0443\u0434\u0430\u043b\u0435\u043d\u0430');loadBans()}
    else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e.message,'error')});
};
// Quick ban helper (called from user panel)
window.quickBan=function(type,value,reason){
  if(!confirm('\u0417\u0430\u0431\u043b\u043e\u043a\u0438\u0440\u043e\u0432\u0430\u0442\u044c '+type+' = '+value+'?'))return;
  post('/api/bans',{action:'add',type:type,value:value,reason:reason||''}).then(function(r){
    if(r.ok){toast('\u0411\u043b\u043e\u043a\u0438\u0440\u043e\u0432\u043a\u0430 \u0434\u043e\u0431\u0430\u0432\u043b\u0435\u043d\u0430');if(typeof loadBans==='function')loadBans()}
    else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e.message,'error')});
};

// --- Promo Codes ---
window.loadPromo=function(){
  var el=document.getElementById('promo-list');
  el.innerHTML='<div style="color:var(--text-dim);font-size:13px;padding:8px 0">Загрузка...</div>';
  api('/api/promo').then(function(list){
    if(!list||!list.length){el.innerHTML='<div style="color:var(--text-dim);font-size:13px;padding:8px 0">Нет промокодов. Сгенерируйте первый выше.</div>';return;}
    var h='<table style="width:100%;border-collapse:collapse;font-size:13px">';
    h+='<tr style="border-bottom:1px solid rgba(255,255,255,0.1)"><th style="text-align:left;padding:6px 8px;color:var(--text-dim)">Код</th><th style="padding:6px 8px;color:var(--text-dim)">Дней</th><th style="padding:6px 8px;color:var(--text-dim)">Исп.</th><th style="padding:6px 8px;color:var(--text-dim)">Срок</th><th style="padding:6px 8px;color:var(--text-dim)">Статус</th><th style="padding:6px 8px"></th></tr>';
    for(var i=0;i<list.length;i++){
      var p=list[i];
      var maxU=p.max_uses>0?p.max_uses:'\u221E';
      var validStr=p.valid?'<span style="color:#00c864">\u2713</span>':'<span style="color:#ff6b6b">\u2717</span>';
      var expStr=p.expires_at||'\u2014';
      h+='<tr style="border-bottom:1px solid rgba(255,255,255,0.04)">';
      h+='<td style="padding:6px 8px;font-family:Courier New,monospace;font-weight:600;cursor:pointer" onclick="copyToClip(\''+esc(p.code)+'\')">'+esc(p.code)+' <span style="font-size:10px;opacity:0.4">&#x1F4CB;</span></td>';
      h+='<td style="padding:6px 8px;text-align:center">'+p.days+'</td>';
      h+='<td style="padding:6px 8px;text-align:center">'+p.used_count+'/'+maxU+'</td>';
      h+='<td style="padding:6px 8px;text-align:center;font-size:11px">'+expStr+'</td>';
      h+='<td style="padding:6px 8px;text-align:center">'+validStr+'</td>';
      h+='<td style="padding:6px 8px;text-align:center"><button class="btn btn-sm" style="background:#dc2626;font-size:11px;padding:2px 8px" onclick="deletePromo(\''+esc(p.code)+'\')">\u2717</button></td>';
      h+='</tr>';
    }
    h+='</table>';
    el.innerHTML=h;
  }).catch(function(e){el.innerHTML='<div style="color:#ff6b6b;padding:8px 0">Ошибка: '+esc(e.message)+'</div>';});
};
window.generatePromo=function(){
  var count=parseInt(document.getElementById('promo-gen-count').value)||1;
  var days=parseInt(document.getElementById('promo-gen-days').value)||30;
  var maxUses=parseInt(document.getElementById('promo-gen-uses').value)||0;
  var validHours=parseInt(document.getElementById('promo-gen-hours').value)||0;
  var res=document.getElementById('promo-gen-result');
  res.innerHTML='<span style="color:var(--text-dim)">Генерация...</span>';
  post('/api/promo',{action:'generate',count:count,days:days,max_uses:maxUses,valid_hours:validHours}).then(function(r){
    if(r.ok&&r.codes){
      res.innerHTML='<span style="color:#00c864">Сгенерировано '+r.codes.length+' кодов:</span> <code style="font-size:12px;background:var(--surface2);padding:2px 6px;border-radius:4px;cursor:pointer" onclick="copyToClip(this.textContent)">'+r.codes.join(', ')+'</code>';
      loadPromo();
    }else{res.innerHTML='<span style="color:#ff6b6b">Ошибка: '+(r.error||'unknown')+'</span>';}
  }).catch(function(e){res.innerHTML='<span style="color:#ff6b6b">Ошибка: '+esc(e.message)+'</span>';});
};
window.deletePromo=function(code){
  if(!confirm('\u0423\u0434\u0430\u043b\u0438\u0442\u044c \u043f\u0440\u043e\u043c\u043e\u043a\u043e\u0434 '+code+'?'))return;
  post('/api/promo',{action:'delete',code:code}).then(function(r){
    if(r.ok){toast('\u0423\u0434\u0430\u043b\u0451\u043d');loadPromo();}
    else toast(r.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e.message,'error');});
};
window.copyToClip=function(text){
  try{navigator.clipboard.writeText(text);toast('\u0421\u043a\u043e\u043f\u0438\u0440\u043e\u0432\u0430\u043d\u043e');}catch(e){
    var ta=document.createElement('textarea');ta.value=text;document.body.appendChild(ta);ta.select();document.execCommand('copy');ta.remove();toast('\u0421\u043a\u043e\u043f\u0438\u0440\u043e\u0432\u0430\u043d\u043e');
  }
};

// --- Balancers ---
var balancerData=[];
var balancerProxyLabels=[];
var withSearchOrder=[];
var customOrder=false;
var balGroupOrder=[
  {key:'ru',label:'\u0420\u0443\u0441\u0441\u043a\u0438\u0435',icon:'\uD83C\uDFAC'},
  {key:'anime',label:'\u0410\u043d\u0438\u043c\u0435',icon:'\uD83C\uDF8C'},
  {key:'ua',label:'\u0423\u043a\u0440\u0430\u0438\u043d\u0441\u043a\u0438\u0435',icon:'\uD83C\uDDFA\uD83C\uDDE6'},
  {key:'video',label:'\u0412\u0438\u0434\u0435\u043e / \u0422\u0412',icon:'\uD83D\uDCFA'},
  {key:'en',label:'\u0410\u043d\u0433\u043b\u0438\u0439\u0441\u043a\u0438\u0435',icon:'\uD83C\uDF0D'},
  {key:'other',label:'\u0414\u0440\u0443\u0433\u0438\u0435',icon:'\u2699\uFE0F'}
];
var qualRank={'4K':1,'FHD':2,'SD':3};
function qr(q){return qualRank[q]||4}
function renderBalCard(b,i){
  var en=b.fields&&(b.fields.enabled||b.fields.enable);
  var checked=en?'checked':'';
  var dotCls=en?'on':'off';
  var badge=b.status_tag?'<span class="badge badge-'+b.status_tag+'">'+b.status_tag.replace(/_/g,' ')+'</span>':'';
  var qb=b.quality?'<span class="badge-quality badge-quality-'+b.quality+'">'+b.quality+'</span>':'';
  var isCustom=b.fields&&b.fields.custom;
  var custBtns='';
  if(isCustom){
    custBtns='<button class="btn btn-sm" style="color:#f44;margin-left:4px" onclick="event.stopPropagation();removeCustBal(\''+esc(b.name)+'\')">Удалить</button>';
    custBtns+='<button class="btn btn-sm" style="color:#4fc3f7;margin-left:4px" onclick="event.stopPropagation();restartCustBal(\''+esc(b.name)+'\',this)">Рестарт</button>';
  }
  return '<div class="balancer-card" id="bc-'+i+'" data-name="'+esc(b.name.toLowerCase())+'"><div class="balancer-header" onclick="toggleCard('+i+')"><span class="chevron" id="chev-'+i+'">&#9654;</span><span class="status-dot '+dotCls+'"></span><span class="balancer-name">'+esc(b.name)+qb+badge+'</span><label class="toggle" onclick="event.stopPropagation()"><input type="checkbox" '+checked+' onchange="quickToggle(\''+esc(b.name)+'\',this.checked)"><span class="slider"></span></label><div class="actions" onclick="event.stopPropagation()"><button class="btn btn-warning btn-sm" onclick="testBal(\''+esc(b.name)+'\',this)">Тест</button>'+custBtns+'</div></div><div class="balancer-body" id="body-'+i+'">'+renderFields(b.name,b.fields,i)+'<div style="margin-top:12px" class="actions"><button class="btn btn-primary btn-sm" onclick="saveBal('+i+')">Сохранить</button><button class="btn btn-sm" style="color:var(--text-muted)" onclick="addField('+i+')">+ Поле</button></div></div></div>';
}
window.loadBalancers=function(){
  api('/api/balancers').then(function(resp){
    var list=resp.balancers||resp;
    balancerProxyLabels=resp.proxy_labels||[];
    withSearchOrder=resp.with_search||[];
    customOrder=!!resp.custom_order;
    balancerData=list;
    // Group balancers and sort by quality within each group.
    var groups={};
    list.forEach(function(b,i){
      b._idx=i;
      var g=b.group||'other';
      if(!groups[g])groups[g]=[];
      groups[g].push(b);
    });
    Object.keys(groups).forEach(function(g){
      groups[g].sort(function(a,b){return qr(a.quality)-qr(b.quality)});
    });
    var html='';
    balGroupOrder.forEach(function(go){
      var items=groups[go.key];
      if(!items||!items.length)return;
      delete groups[go.key];
      var enabledCount=items.filter(function(b){return b.fields&&(b.fields.enabled||b.fields.enable)}).length;
      html+='<div class="bal-group" data-group="'+go.key+'">';
      var allOn=enabledCount===items.length;
      html+='<div class="bal-group-header" onclick="toggleGroup(\''+go.key+'\')">';
      html+='<span class="bal-group-icon">'+go.icon+'</span>';
      html+='<span class="bal-group-title">'+go.label+'</span>';
      html+='<span class="bal-group-count">'+enabledCount+' / '+items.length+'</span>';
      html+='<button class="bal-group-toggle'+(allOn?' all-on':'')+'" onclick="event.stopPropagation();toggleGroupAll(\''+go.key+'\')" id="gtog-'+go.key+'">'+(allOn?'Выкл все':'Вкл все')+'</button>';
      html+='<span class="bal-group-chevron open" id="gchev-'+go.key+'">&#9654;</span>';
      html+='</div>';
      html+='<div class="bal-group-body open" id="gbody-'+go.key+'">';
      items.forEach(function(b){html+=renderBalCard(b,b._idx)});
      html+='</div></div>';
    });
    // Any remaining groups (shouldn't happen, but safety net).
    Object.keys(groups).forEach(function(g){
      var items=groups[g];if(!items||!items.length)return;
      html+='<div class="bal-group" data-group="'+g+'">';
      html+='<div class="bal-group-header" onclick="toggleGroup(\''+g+'\')"><span class="bal-group-icon">\u2699\uFE0F</span><span class="bal-group-title">'+esc(g)+'</span><span class="bal-group-count">'+items.length+'</span><span class="bal-group-chevron open" id="gchev-'+g+'">&#9654;</span></div>';
      html+='<div class="bal-group-body open" id="gbody-'+g+'">';
      items.forEach(function(b){html+=renderBalCard(b,b._idx)});
      html+='</div></div>';
    });
    document.getElementById('balancers-list').innerHTML=html;
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};
window.toggleGroup=function(key){
  var b=document.getElementById('gbody-'+key),ch=document.getElementById('gchev-'+key);
  if(b)b.classList.toggle('open');
  if(ch)ch.classList.toggle('open');
};
window.filterBalancers=function(q){
  q=q.toLowerCase().trim();
  document.querySelectorAll('.balancer-card').forEach(function(el){
    el.style.display=(!q||el.dataset.name.indexOf(q)>=0)?'':'none';
  });
  document.querySelectorAll('.bal-group').forEach(function(g){
    var vis=g.querySelectorAll('.balancer-card:not([style*="display: none"])').length;
    g.style.display=vis?'':'none';
  });
};
// --- Reorder panel ---
var reorderList=[];
window.openReorder=function(){
  reorderList=withSearchOrder.slice();
  renderReorderList();
  var coEl=document.getElementById('custom-order-cb');
  if(coEl){coEl.checked=customOrder}
  var lbl=document.getElementById('sort-label');
  if(lbl)lbl.textContent=customOrder?'свой порядок':'по качеству (4K→FHD→SD)';
  document.getElementById('reorder-panel').style.display='block';
};
window.closeReorder=function(){
  document.getElementById('reorder-panel').style.display='none';
};
function renderReorderList(){
  var el=document.getElementById('reorder-list');
  if(!reorderList.length){el.innerHTML='<div class="empty">with_search пуст — включите балансеры</div>';return}
  var html='';
  reorderList.forEach(function(name,i){
    html+='<div class="reorder-item" draggable="true" data-ri="'+i+'">';
    html+='<span class="reorder-num">'+(i+1)+'</span>';
    html+='<span class="reorder-name">'+esc(name)+'</span>';
    html+='<span class="reorder-btns">';
    html+='<button onclick="reorderMove('+i+',-1)" title="Вверх">&uarr;</button>';
    html+='<button onclick="reorderMove('+i+',1)" title="Вниз">&darr;</button>';
    html+='</span></div>';
  });
  el.innerHTML=html;
  // Drag and drop.
  var items=el.querySelectorAll('.reorder-item');
  var dragIdx=null;
  items.forEach(function(item){
    item.addEventListener('dragstart',function(e){
      dragIdx=parseInt(item.dataset.ri);
      item.classList.add('dragging');
      e.dataTransfer.effectAllowed='move';
    });
    item.addEventListener('dragend',function(){item.classList.remove('dragging');dragIdx=null});
    item.addEventListener('dragover',function(e){e.preventDefault();e.dataTransfer.dropEffect='move'});
    item.addEventListener('drop',function(e){
      e.preventDefault();
      var dropIdx=parseInt(item.dataset.ri);
      if(dragIdx!==null&&dragIdx!==dropIdx){
        var moved=reorderList.splice(dragIdx,1)[0];
        reorderList.splice(dropIdx,0,moved);
        renderReorderList();
      }
    });
  });
}
window.reorderMove=function(i,dir){
  var j=i+dir;
  if(j<0||j>=reorderList.length)return;
  var tmp=reorderList[i];reorderList[i]=reorderList[j];reorderList[j]=tmp;
  renderReorderList();
};
window.saveReorder=function(){
  // If user reordered items, auto-enable custom order so the order is actually used.
  if(reorderList.length>0){
    var orig=withSearchOrder.join(',');
    var curr=reorderList.join(',');
    if(orig!==curr&&!customOrder){
      customOrder=true;
      var cb=document.getElementById('custom-order-cb');if(cb)cb.checked=true;
      var lbl=document.getElementById('sort-label');if(lbl)lbl.textContent='свой порядок';
    }
  }
  post('/api/balancers',{action:'reorder',order:reorderList,custom_order:customOrder}).then(function(r){
    if(r.ok){toast('Порядок сохранён');withSearchOrder=reorderList.slice();closeReorder();loadBalancers()}
    else toast(r.error||'Ошибка','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};
window.toggleGroupAll=function(groupKey){
  // Use balancerData directly instead of DOM to ensure all balancers in the group are included.
  var names=[];
  var allEnabled=true;
  balancerData.forEach(function(b){
    if((b.group||'other')===groupKey){
      names.push(b.name);
      if(!(b.fields&&(b.fields.enabled||b.fields.enable)))allEnabled=false;
    }
  });
  if(!names.length)return;
  var newState=!allEnabled;
  var btn=document.getElementById('gtog-'+groupKey);
  if(btn){btn.textContent='...';btn.disabled=true}
  // Send toggle requests sequentially.
  var chain=Promise.resolve();
  names.forEach(function(n){
    chain=chain.then(function(){
      return post('/api/balancers',{action:'save',name:n,fields:{enable:newState}});
    });
  });
  chain.then(function(){
    toast(names.length+' \u0431\u0430\u043b\u0430\u043d\u0441\u0435\u0440\u043e\u0432 '+(newState?'\u0432\u043a\u043b':'\u0432\u044b\u043a\u043b'));
    showRestart();
    loadBalancers();
  }).catch(function(e){
    toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e.message,'error');
    if(btn){btn.disabled=false;btn.textContent=allEnabled?'\u0412\u043a\u043b \u0432\u0441\u0435':'\u0412\u044b\u043a\u043b \u0432\u0441\u0435'}
  });
};
function renderFields(name,fields,idx){
  if(!fields)return'<div class="empty">Нет данных</div>';
  var keys=Object.keys(fields).sort();
  var html='';
  // Proxy selector (at top if labels available).
  if(balancerProxyLabels&&balancerProxyLabels.length>0){
    var cur=fields['_proxy_label']||'';
    var opts='<option value=""'+(cur?'':' selected')+'>— нет —</option>';
    balancerProxyLabels.forEach(function(lb){opts+='<option value="'+esc(lb)+'"'+(cur===lb?' selected':'')+'>'+esc(lb)+'</option>'});
    html+='<div class="field-row"><span class="field-key">proxy</span><select class="input-sm field-val" data-bal="'+idx+'" data-key="_proxy_label" style="min-width:120px">'+opts+'</select></div>';
  }
  // Stream proxy toggle (bypass /proxy/ for direct CDN URLs).
  var sp=fields['_stream_proxy']!==false;
  html+='<div class="field-row"><span class="field-key">stream proxy</span><label class="toggle"><input type="checkbox" '+(sp?'checked':'')+' data-bal="'+idx+'" data-key="_stream_proxy"><span class="slider"></span></label><span style="color:var(--text-muted);font-size:12px;margin-left:8px">'+(sp?'через /proxy/':'прямой CDN')+'</span></div>';
  keys.forEach(function(k){
    var v=fields[k],type=typeof v;
    if(k.charAt(0)==='_')return; // skip internal fields
    if(k==='enabled')return; // duplicate of 'enable', skip to avoid confusion
    if(type==='boolean')html+='<div class="field-row"><span class="field-key">'+esc(k)+'</span><label class="toggle"><input type="checkbox" '+(v?'checked':'')+' data-bal="'+idx+'" data-key="'+esc(k)+'"><span class="slider"></span></label></div>';
    else if(type==='number')html+='<div class="field-row"><span class="field-key">'+esc(k)+'</span><input class="input-sm field-val" type="number" value="'+v+'" data-bal="'+idx+'" data-key="'+esc(k)+'"></div>';
    else if(v===null)html+='<div class="field-row"><span class="field-key">'+esc(k)+'</span><input class="input-sm field-val input-wide" value="" placeholder="null" data-bal="'+idx+'" data-key="'+esc(k)+'"></div>';
    else html+='<div class="field-row"><span class="field-key">'+esc(k)+'</span><input class="input-sm field-val input-wide" value="'+esc(v)+'" data-bal="'+idx+'" data-key="'+esc(k)+'"></div>';
  });
  return html;
}
window.toggleCard=function(i){
  var b=document.getElementById('body-'+i),ch=document.getElementById('chev-'+i);
  b.classList.toggle('open');ch.classList.toggle('open');
};
window.quickToggle=function(name,en){
  post('/api/balancers',{action:'save',name:name,fields:{enable:en}}).then(function(r){
    if(r.ok){toast(name+(en?' вкл':' выкл'));if(r.restart_needed)showRestart()}
    else toast(r.error||'Ошибка','error');
    loadBalancers();
  });
};
window.saveBal=function(idx){
  var b=balancerData[idx];if(!b)return;
  var fields={};
  document.querySelectorAll('[data-bal="'+idx+'"]').forEach(function(el){
    var k=el.dataset.key;
    if(el.type==='checkbox')fields[k]=el.checked;
    else if(el.type==='number')fields[k]=parseFloat(el.value)||0;
    else fields[k]=el.value||null;
  });
  post('/api/balancers',{action:'save',name:b.name,fields:fields}).then(function(r){
    if(r.ok){toast(b.name+' сохранён');if(r.restart_needed)showRestart()}
    else toast(r.error||'Ошибка','error');
  });
};
window.testBal=function(name,btn){
  btn.textContent='...';btn.disabled=true;
  post('/api/balancers',{action:'test',name:name}).then(function(r){
    btn.disabled=false;btn.textContent='Тест';
    if(r.ok){var c=r.latency_ms<500?'fast':r.latency_ms<2000?'slow':'bad';toast(name+': OK ('+r.latency_ms+'ms)');var ex=btn.parentElement.querySelector('.latency');if(ex)ex.remove();btn.insertAdjacentHTML('afterend','<span class="latency '+c+'">'+r.latency_ms+'ms</span>')}
    else toast(name+': '+(r.error||'Failed'),'error');
  }).catch(function(){btn.disabled=false;btn.textContent='Тест'});
};
window.addField=function(idx){
  var k=prompt('Имя поля:');if(!k)return;
  var v=prompt('Значение:');if(v===null)return;
  var body=document.getElementById('body-'+idx);
  var div=document.createElement('div');div.className='field-row';
  div.innerHTML='<span class="field-key">'+esc(k)+'</span><input class="input-sm field-val input-wide" value="'+esc(v)+'" data-bal="'+idx+'" data-key="'+esc(k)+'">';
  body.insertBefore(div,body.lastElementChild);
};
window.removeCustBal=function(name){
  if(!confirm('Удалить кастомный балансер "'+name+'"?\n\nПроцесс будет остановлен, файлы удалены.'))return;
  post('/api/custbal',{action:'remove',name:name}).then(function(r){
    if(r.ok){toast(name+' удалён');loadBalancers()}
    else toast(r.error||'Ошибка','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};
window.restartCustBal=function(name,btn){
  btn.textContent='...';btn.disabled=true;
  post('/api/custbal',{action:'restart',name:name}).then(function(r){
    btn.disabled=false;btn.textContent='Рестарт';
    if(r.ok){toast(name+' перезапущен');loadBalancers()}
    else toast(r.error||'Ошибка','error');
  }).catch(function(){btn.disabled=false;btn.textContent='Рестарт'});
};

// --- Modules (JS) ---
var mMods = (function(){
  var state = { list: [], filter: '', mode: '', active: null, logsES: null, dTab: 'info', _prefix: '/api/modules' };
  function escHTML(s){ return String(s==null?'':s).replace(/[&<>"']/g,function(c){return({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]}); }
  function toast2(m,t){ if(window.toast)toast(m,t); else console.log(m); }
  function apiMod(path, opts){ opts = opts || {}; return fetch(basePath+(state._prefix||'/api/modules')+path, opts).then(function(r){ return r.json().catch(function(){ return {}; }); }); }
  function refresh(){
    apiMod('').then(function(list){
      state.list = Array.isArray(list) ? list : [];
      render();
    }).catch(function(e){ toast2('Ошибка загрузки модулей','error'); });
  }
  function render(){
    var grid = document.getElementById('mods-grid');
    var empty = document.getElementById('mods-empty');
    if (!grid) return;
    var q = (document.getElementById('mods-search')||{}).value || '';
    var mode = (document.getElementById('mods-filter')||{}).value || '';
    var items = state.list.filter(function(m){
      if (q) {
        var hay = (m.id+' '+m.name+' '+(m.tags||[]).join(' ')+' '+(m.description||'')).toLowerCase();
        if (hay.indexOf(q.toLowerCase())<0) return false;
      }
      if (mode==='enabled') return m.enabled;
      if (mode==='disabled') return !m.enabled;
      if (mode==='error') return !!m.error;
      if (mode==='ukrainian') return m.ukrainian || (m.tags||[]).indexOf('ukrainian')>=0;
      if (mode==='anime') return m.anime || (m.tags||[]).indexOf('anime')>=0;
      return true;
    });
    if (!items.length) { grid.innerHTML=''; empty.style.display=state.list.length?'none':'block'; if(state.list.length){grid.innerHTML='<div class="mod-empty"><div class="big">&#x1F50D;</div><div>Ничего не найдено по фильтру.</div></div>'} return; }
    empty.style.display='none';
    grid.innerHTML = items.map(renderCard).join('');
  }
  function renderCard(m){
    var cls = 'mod-card' + (m.enabled?'':' disabled') + (m.error?' err':'');
    var tags = (m.tags||[]).map(function(t){ var c='mod-tag'; if(t==='ukrainian')c+=' ua'; if(t==='anime')c+=' anime'; return '<span class="'+c+'">'+escHTML(t)+'</span>'; }).join('');
    var q = m.quality ? '<span class="mod-q">'+escHTML(m.quality)+'</span>' : '';
    var badge = m.enabled ? '' : '<span class="mod-tag" style="background:#333;color:#aaa">&#x25CB; OFF</span>';
    var stats = m.stats || {};
    var statHTML = '';
    if (stats.request_count){
      statHTML = '<div class="mod-stats">'
        +'<span class="ok">&#x2714; '+stats.request_count+'</span>'
        +(stats.error_count?'<span class="fail">&#x2716; '+stats.error_count+'</span>':'')
        +(stats.avg_duration_ms?'<span>'+Math.round(stats.avg_duration_ms)+' ms</span>':'')
        +'</div>';
    }
    var err = m.error ? '<div class="mod-err">'+escHTML(m.error)+'</div>' : '';
    return '<div class="'+cls+'" data-id="'+escHTML(m.id)+'">'
      +'<div class="mod-card-head">'
        +'<div class="mod-icon">&#x1F9EA;</div>'
        +'<div class="mod-title">'
          +'<div class="mod-name">'+escHTML(m.name)+' '+q+' '+badge+'</div>'
          +'<div class="mod-meta">'
            +'<span>'+escHTML(m.author||'')+'</span>'
            +'<span>v'+escHTML(m.version||'0')+'</span>'
            +'<span style="font-family:monospace">/lite/'+escHTML(m.id)+'</span>'
          +'</div>'
        +'</div>'
      +'</div>'
      +'<div class="mod-desc">'+escHTML(m.description||'')+'</div>'
      +(tags?'<div class="mod-tags">'+tags+'</div>':'')
      +err + statHTML
      +'<div class="mod-actions">'
        +'<label class="toggle" title="Вкл/выкл"><input type="checkbox" '+(m.enabled?'checked':'')+' onchange="mMods.toggle(\''+escHTML(m.id)+'\', this.checked)"><span class="slider"></span></label>'
        +'<div class="spacer"></div>'
        +'<button class="btn" onclick="mMods.open(\''+escHTML(m.id)+'\')">&#x270E; Редактировать</button>'
        +'<button class="btn" onclick="mMods.reload(\''+escHTML(m.id)+'\')">&#x21BB;</button>'
        +'<button class="btn" style="color:#c88" onclick="mMods.remove(\''+escHTML(m.id)+'\')">&#x1F5D1;</button>'
      +'</div>'
    +'</div>';
  }
  function toggle(id, enabled){
    apiMod('/'+encodeURIComponent(id)+'/toggle', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({enabled:enabled})})
      .then(function(r){ if(r.ok){ toast2((enabled?'Включён: ':'Выключен: ')+id); refresh(); } else { toast2(r.error||'Ошибка','error'); refresh(); }});
  }
  function reload(id){
    apiMod('/'+encodeURIComponent(id)+'/reload', {method:'POST'}).then(function(r){ if(r.ok){toast2(id+' перезагружен');refresh();} else toast2(r.error||'Ошибка','error'); });
  }
  function remove(id){
    if(!confirm('Удалить модуль '+id+'?')) return;
    apiMod('/'+encodeURIComponent(id), {method:'DELETE'}).then(function(r){ if(r.ok){toast2(id+' удалён');refresh();} else toast2(r.error||'Ошибка','error'); });
  }
  function open(id){
    var m = state.list.find(function(x){return x.id===id});
    if (!m) return;
    state.active = m;
    document.getElementById('mod-d-name').innerHTML = escHTML(m.name) + (m.quality?' <span class="mod-q">'+escHTML(m.quality)+'</span>':'');
    document.getElementById('mod-d-meta').innerHTML = '<span>v'+escHTML(m.version||'0')+'</span><span>'+escHTML(m.author||'')+'</span><span style="font-family:monospace">/lite/'+escHTML(m.id)+'</span>';
    document.getElementById('mod-backdrop').classList.add('show');
    document.getElementById('mod-drawer').classList.add('open');
    drawerTab('info');
  }
  function closeDrawer(){
    document.getElementById('mod-backdrop').classList.remove('show');
    document.getElementById('mod-drawer').classList.remove('open');
    if (state.logsES) { state.logsES.close(); state.logsES=null; }
    state.active = null;
    state._prefix = '/api/modules';
  }
  // openWithPrefix allows sibling managers (e.g. sisiMod) to reuse the shared
  // drawer but with a different API prefix (e.g. /api/sisi-sources).
  function openWithPrefix(m, prefix) {
    state._prefix = prefix || '/api/modules';
    state.active = m;
    var routeBase = (prefix === '/api/sisi-sources') ? '/sisi/cust/' : '/lite/';
    document.getElementById('mod-d-name').innerHTML = escHTML(m.name) + (m.quality?' <span class="mod-q">'+escHTML(m.quality)+'</span>':'')+'';
    document.getElementById('mod-d-meta').innerHTML = '<span>v'+escHTML(m.version||'0')+'</span><span>'+escHTML(m.author||'')+'</span><span style="font-family:monospace">'+routeBase+escHTML(m.id)+'</span>';
    document.getElementById('mod-backdrop').classList.add('show');
    document.getElementById('mod-drawer').classList.add('open');
    drawerTab('info');
  }
  function drawerTab(name){
    state.dTab = name;
    document.querySelectorAll('.mod-drawer-tabs .dt').forEach(function(el){el.classList.toggle('active', el.dataset.d===name)});
    ['info','config','code','logs'].forEach(function(n){
      var p = document.getElementById('mod-pane-'+n);
      if (p) p.style.display = (n===name)?'block':'none';
    });
    var foot = document.getElementById('mod-drawer-foot');
    if (state.logsES) { state.logsES.close(); state.logsES=null; }
    if (!state.active) return;
    if (name==='info') { renderInfo(); foot.innerHTML=''; }
    if (name==='config') { renderConfig(); foot.innerHTML='<button class="btn btn-primary" onclick="mMods.saveConfig()">Сохранить</button>'; }
    if (name==='code') { loadSource(); foot.innerHTML='<button class="btn btn-primary" onclick="mMods.saveCode()">Сохранить и перезагрузить</button>'; }
    if (name==='logs') { openLogs(); foot.innerHTML='<button class="btn btn-sm" onclick="mMods.clearLogs()">Очистить</button>'; }
  }
  function renderInfo(){
    var m = state.active; if(!m)return;
    var h = '<div style="display:grid;grid-template-columns:repeat(2,1fr);gap:12px">'
      +'<div><div style="font-size:11px;color:var(--text-dim)">ID</div><div style="font-family:monospace">'+escHTML(m.id)+'</div></div>'
      +'<div><div style="font-size:11px;color:var(--text-dim)">Версия</div><div>'+escHTML(m.version||'0')+'</div></div>'
      +'<div><div style="font-size:11px;color:var(--text-dim)">Автор</div><div>'+escHTML(m.author||'-')+'</div></div>'
      +'<div><div style="font-size:11px;color:var(--text-dim)">Типы</div><div>'+(m.content_types||[]).join(', ')+'</div></div>'
      +'<div style="grid-column:1/3"><div style="font-size:11px;color:var(--text-dim)">Репозиторий</div><div>'+(m.repository?'<a href="'+escHTML(m.repository)+'" target="_blank" style="color:var(--accent)">'+escHTML(m.repository)+'</a>':'-')+'</div></div>'
      +'<div style="grid-column:1/3"><div style="font-size:11px;color:var(--text-dim)">Описание</div><div>'+escHTML(m.description||'-')+'</div></div>'
    +'</div>';
    if (m.error) h += '<div class="mod-err" style="margin-top:12px"><b>Ошибка компиляции:</b><br>'+escHTML(m.error)+'</div>';
    var s = m.stats||{};
    h += '<div style="margin-top:18px"><div style="font-size:11px;color:var(--text-dim);text-transform:uppercase;letter-spacing:.6px">Статистика</div>'
      +'<div class="mod-stats" style="margin-top:6px;font-size:12px">'
      +'<span class="ok">Запросов: '+(s.request_count||0)+'</span>'
      +(s.error_count?'<span class="fail">Ошибок: '+s.error_count+'</span>':'')
      +(s.avg_duration_ms?'<span>Среднее время: '+Math.round(s.avg_duration_ms)+' ms</span>':'')
      +(s.last_request?'<span>Последний: '+new Date(s.last_request).toLocaleString()+'</span>':'')
      +'</div></div>';
    document.getElementById('mod-pane-info').innerHTML = h;
  }
  function renderConfig(){
    var m = state.active; if(!m)return;
    var schema = m.config_schema||[];
    var cfg = m.config||{};
    if (!schema.length){ document.getElementById('mod-pane-config').innerHTML = '<div class="mod-empty"><div class="big">&#x2699;</div><div>У модуля нет настроек.</div></div>'; return; }
    var h = schema.map(function(f){
      var v = cfg[f.key];
      if (v===undefined||v===null) v = f.default===undefined?'':f.default;
      var inp = '';
      if (f.type==='bool'){
        inp = '<label class="toggle"><input type="checkbox" data-k="'+escHTML(f.key)+'" '+(v?'checked':'')+'><span class="slider"></span></label>';
      } else if (f.type==='textarea'){
        inp = '<textarea data-k="'+escHTML(f.key)+'" rows="4">'+escHTML(v)+'</textarea>';
      } else if (f.type==='enum' && Array.isArray(f.options) && f.options.length){
        var opts = f.options.map(function(o){
          var val = (typeof o==='object' && o!==null) ? (o.value!==undefined?o.value:o.key) : o;
          var lbl = (typeof o==='object' && o!==null) ? (o.label||o.name||val) : o;
          return '<option value="'+escHTML(val)+'"'+(String(v)===String(val)?' selected':'')+'>'+escHTML(lbl)+'</option>';
        }).join('');
        inp = '<select data-k="'+escHTML(f.key)+'">'+opts+'</select>';
      } else {
        var ty = (f.type==='int'||f.type==='number')?'number':((f.type==='secret'||f.secret)?'password':'text');
        inp = '<input type="'+ty+'" data-k="'+escHTML(f.key)+'" value="'+escHTML(v)+'">';
      }
      return '<div class="mod-cfg-row"><label>'+escHTML(f.label||f.key)+'</label><div>'+inp+'</div>'+(f.description?'<div class="desc">'+escHTML(f.description)+'</div>':'')+'</div>';
    }).join('');
    document.getElementById('mod-pane-config').innerHTML = h;
  }
  function saveConfig(){
    var m = state.active; if(!m)return;
    var out = {};
    document.querySelectorAll('#mod-pane-config [data-k]').forEach(function(el){
      var k = el.dataset.k;
      if (el.type==='checkbox') out[k] = el.checked;
      else if (el.type==='number') out[k] = el.value===''?null:Number(el.value);
      else out[k] = el.value;
    });
    apiMod('/'+encodeURIComponent(m.id)+'/config', {method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(out)})
      .then(function(r){ if(r.ok){toast2('Настройки сохранены');refresh();} else toast2(r.error||'Ошибка','error'); });
  }
  function loadSource(){
    var m = state.active; if(!m)return;
    var ta = document.getElementById('mod-editor'); ta.value='// Загрузка...';
    apiMod('/'+encodeURIComponent(m.id)+'/source').then(function(r){ ta.value = r.source || ''; });
  }
  function saveCode(){
    var m = state.active; if(!m)return;
    var ta = document.getElementById('mod-editor');
    apiMod('/'+encodeURIComponent(m.id)+'/source', {method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({source:ta.value})})
      .then(function(r){ if(r.ok){toast2('Код сохранён и перезагружен');refresh();} else toast2(r.error||'Ошибка компиляции','error'); });
  }
  function openLogs(){
    var m = state.active; if(!m)return;
    var box = document.getElementById('mod-logs-box'); box.innerHTML = '';
    try {
      var es = new EventSource(basePath+'/api/modules/logs/stream?module='+encodeURIComponent(m.id));
      state.logsES = es;
      es.onmessage = function(ev){
        try { var e = JSON.parse(ev.data); appendLog(box,e); } catch(err){}
      };
      es.onerror = function(){ /* keep open, reconnect handled by browser */ };
    } catch(e){}
  }
  function appendLog(box,e){
    var line = document.createElement('div');
    line.className='ll';
    var time = new Date(e.time).toLocaleTimeString();
    line.innerHTML = '<span class="lt">'+escHTML(time)+'</span><span class="lm lv-'+escHTML(e.level)+'">['+escHTML(e.level)+']</span> '+escHTML(e.msg);
    box.appendChild(line);
    box.scrollTop = box.scrollHeight;
  }
  function clearLogs(){ var box=document.getElementById('mod-logs-box'); if(box) box.innerHTML=''; }
  function showInstall(){ document.getElementById('mod-install-bd').classList.add('show'); document.getElementById('mod-install').classList.add('open'); }
  function hideInstall(){ document.getElementById('mod-install-bd').classList.remove('show'); document.getElementById('mod-install').classList.remove('open'); }
  function installFromURL(){
    var u = document.getElementById('mod-install-url').value.trim();
    if(!u){toast2('Укажите URL','error');return;}
    apiMod('/install', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:u})}).then(function(r){
      if(r.ok){toast2('Модуль '+r.id+' установлен');hideInstall();refresh();} else toast2(r.error||'Ошибка','error');
    });
  }
  function installFromFile(){
    var f = document.getElementById('mod-install-file').files[0];
    if(!f){toast2('Выберите файл','error');return;}
    var fr = new FileReader();
    fr.onload = function(){
      fetch(basePath+(state._prefix||'/api/modules')+'/install', {method:'POST',headers:{'Content-Type':'application/zip'},body:fr.result})
        .then(function(r){return r.json()})
        .then(function(r){ if(r.ok){toast2('Модуль '+r.id+' установлен');hideInstall();refresh();} else toast2(r.error||'Ошибка','error'); });
    };
    fr.readAsArrayBuffer(f);
  }
  function filter(){ render(); }

  // --- Hub catalog integration ---
  var hubCache = [];
  function showHub(){
    document.getElementById('mod-hub-bd').classList.add('show');
    document.getElementById('mod-hub').classList.add('open');
    refreshHub();
  }
  function hideHub(){
    document.getElementById('mod-hub-bd').classList.remove('show');
    document.getElementById('mod-hub').classList.remove('open');
  }
  function refreshHub(){
    var url = document.getElementById('mod-hub-url').value.trim();
    var status = document.getElementById('mod-hub-status');
    var box = document.getElementById('mod-hub-list');
    status.textContent = 'Загрузка каталога...';
    status.style.color = 'var(--text-muted)';
    box.innerHTML = '';
    fetch(url, {headers:{'Accept':'application/json'}})
      .then(function(r){ if(!r.ok) throw new Error('HTTP '+r.status); return r.json(); })
      .then(function(data){
        hubCache = (data && data.plugins) || [];
        status.textContent = 'Модулей: '+hubCache.length+' · обновлён '+(data.updated_at||'');
        renderHub();
      })
      .catch(function(e){
        status.textContent = 'Ошибка: '+e.message;
        status.style.color = 'var(--warning)';
      });
  }
  function filterHub(){ renderHub(); }
  function renderHub(){
    var q = (document.getElementById('mod-hub-search').value||'').toLowerCase();
    var box = document.getElementById('mod-hub-list');
    var installed = {};
    (state.list||[]).forEach(function(m){ installed[m.id||(m.manifest&&m.manifest.id)] = m; });
    var rows = hubCache.filter(function(p){
      if (!q) return true;
      return (p.name||'').toLowerCase().indexOf(q)>=0 ||
             (p.display_name||'').toLowerCase().indexOf(q)>=0 ||
             (p.description||'').toLowerCase().indexOf(q)>=0 ||
             (p.tags||[]).join(',').toLowerCase().indexOf(q)>=0;
    }).map(function(p){
      var mfId = (p.manifest && p.manifest.id) || p.name;
      var have = installed[mfId];
      var haveVer = have && have.version || '';
      var cmp = haveVer && p.version && (haveVer === p.version) ? 'installed' : haveVer ? 'update' : 'new';
      var badge = cmp==='installed'
        ? '<span class="tag" style="background:rgba(63,185,80,0.2);color:#7ddb8a">установлен</span>'
        : cmp==='update'
          ? '<span class="tag" style="background:rgba(210,153,34,0.18);color:#e6c767">обновление v'+esc2(haveVer)+' → v'+esc2(p.version)+'</span>'
          : '';
      var tags = (p.tags||[]).slice(0,4).map(function(t){return '<span class="tag" style="background:rgba(255,255,255,0.08);color:var(--text-muted)">'+esc2(t)+'</span>'}).join('');
      var author = p.author ? ' · <span style="color:var(--text-muted)">'+esc2(p.author)+'</span>' : '';
      var rating = p.rating_n ? ' · ★ '+(p.rating_avg||0).toFixed(1)+' ('+p.rating_n+')' : '';
      return '<div style="padding:12px 14px;border:1px solid var(--border);border-radius:10px;margin-bottom:8px;display:flex;gap:12px;align-items:center">'+
        '<div style="flex:1;min-width:0">'+
          '<div style="display:flex;gap:8px;align-items:baseline;flex-wrap:wrap">'+
            '<div style="font-weight:600">'+esc2(p.display_name||p.name)+'</div>'+
            '<code style="font-size:11px">'+esc2(mfId)+'</code>'+
            '<span style="font-size:11px;color:var(--text-muted)">v'+esc2(p.version||'?')+'</span>'+
            badge+
          '</div>'+
          '<div style="font-size:12px;color:var(--text-dim);margin:4px 0">'+esc2(p.description||'')+'</div>'+
          '<div style="font-size:11px;color:var(--text-muted)">'+tags+author+rating+' · ⬇ '+(p.downloads||0)+'</div>'+
        '</div>'+
        '<div style="display:flex;gap:6px">'+
          (p.homepage?'<a class="btn btn-sm" href="'+esc2(p.homepage)+'" target="_blank" rel="noopener">🔗</a>':'')+
          '<button class="btn btn-sm btn-primary" onclick="mMods.installFromHub('+"'"+esc2(p.module_url||p.source_url||'')+"'"+')">'+(cmp==='update'?'Обновить':'Установить')+'</button>'+
        '</div>'+
      '</div>';
    });
    box.innerHTML = rows.join('') || '<div style="padding:30px;text-align:center;color:var(--text-muted)">Ничего не найдено.</div>';
  }
  function esc2(s){ if(s==null) return ''; return String(s).replace(/[&<>"']/g,function(c){return{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]}); }
  function installFromHub(url){
    if (!url) { toast2('URL модуля пуст','error'); return; }
    if (!confirm('Установить модуль с hub?\n\n'+url)) return;
    apiMod('/install', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:url})}).then(function(r){
      if (r.ok) {
        toast2('Модуль '+r.id+' установлен');
        refresh();
        renderHub(); // обновим бадж "установлен"
      } else {
        toast2(r.error||'Ошибка','error');
      }
    });
  }

  return { refresh:refresh, render:render, filter:filter, toggle:toggle, reload:reload, remove:remove, open:open, openWithPrefix:openWithPrefix, closeDrawer:closeDrawer, drawerTab:drawerTab, saveConfig:saveConfig, saveCode:saveCode, clearLogs:clearLogs, showInstall:showInstall, hideInstall:hideInstall, installFromURL:installFromURL, installFromFile:installFromFile, showHub:showHub, hideHub:hideHub, refreshHub:refreshHub, filterHub:filterHub, installFromHub:installFromHub };
})();
// Expose globally since admin panel JS runs inside an IIFE.
window.mMods = mMods;

// --- SISI JS Sources ---
var sisiMod=(function(){
  var state={list:[],filter:''};
  function esc(s){return String(s==null?'':s).replace(/[&<>"']/g,function(c){return({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c];});}
  function api(path,opts){return fetch(basePath+'/api/sisi-sources'+path,opts||{}).then(function(r){return r.json().catch(function(){return{};});});}
  function refresh(){
    api('').then(function(list){
      state.list=Array.isArray(list)?list:[];
      render();
    }).catch(function(){if(window.toast)toast('Ошибка SISI источников','error');});
  }
  function render(){
    var grid=document.getElementById('ss-grid');
    var empty=document.getElementById('ss-empty');
    if(!grid)return;
    var q=(document.getElementById('ss-search')||{}).value||'';
    var items=state.list.filter(function(m){
      if(q){var hay=(m.id+' '+m.name+' '+(m.description||'')).toLowerCase();if(hay.indexOf(q.toLowerCase())<0)return false;}
      return true;
    });
    if(items.length===0){grid.innerHTML='';if(empty)empty.style.display='';return;}
    if(empty)empty.style.display='none';
    grid.innerHTML=items.map(function(m){
      var icon=m.icon||'🍑';
      var errBadge=m.error?'<span style="color:#f85149;font-size:10px;margin-left:6px">⚠ ошибка</span>':'';
      var stats='';
      if(m.stats&&m.stats.request_count){
        stats='<div class="mod-stats"><span class="ok">✔ '+m.stats.request_count+'</span>'+(m.stats.error_count?'<span class="fail">✘ '+m.stats.error_count+'</span>':'')+'</div>';
      }
      return '<div class="mod-card'+(m.enabled?'':' disabled')+(m.error?' err':'')+'">'+
        '<div class="mod-card-head">'+
        '<div class="mod-icon">'+esc(icon)+'</div>'+
        '<div class="mod-title">'+
        '<div class="mod-name">'+esc(m.name||m.id)+errBadge+'</div>'+
        '<div class="mod-meta"><span>'+esc(m.id)+'</span><span>v'+esc(m.version||'0.0.0')+'</span>'+(m.author?'<span>'+esc(m.author)+'</span>':'')+'</div>'+
        '</div></div>'+
        (m.description?'<div class="mod-desc">'+esc(m.description)+'</div>':'')+
        (m.error?'<div class="mod-err">'+esc(m.error)+'</div>':'')+
        '<div class="mod-actions">'+
        '<label class="toggle"><input type="checkbox"'+(m.enabled?' checked':'')+' onchange="sisiMod.toggle(\''+esc(m.id)+'\',this.checked)"></label>'+
        '<span class="spacer"></span>'+
        '<button class="btn btn-sm" onclick="sisiMod.open(\''+esc(m.id)+'\')">&#x418;&#x437;&#x43C;&#x435;&#x43D;&#x438;&#x442;&#x44C;</button>'+
        '<button class="btn btn-sm" onclick="sisiMod.reload(\''+esc(m.id)+'\')">&#x21BB;</button>'+
        '<button class="btn btn-sm" style="color:var(--danger,#c44)" onclick="sisiMod.remove(\''+esc(m.id)+'\')">&times;</button>'+
        '</div>'+stats+
        '</div>';
    }).join('');
  }
  function toggle(id,val){
    api('/'+id+'/toggle',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({enabled:val})}).then(function(r){if(r.ok)refresh();else if(window.toast)toast(r.error||'error','error');});
  }
  function reload(id){
    api('/'+id+'/reload',{method:'POST'}).then(function(r){if(r.ok)refresh();else if(window.toast)toast(r.error||'error','error');});
  }
  function remove(id){
    if(!confirm('Удалить SISI источник "'+id+'"?'))return;
    api('/'+id,{method:'DELETE'}).then(function(){refresh();});
  }
  function open(id){
    var m=state.list.find(function(x){return x.id===id;});
    if(!m)return;
    if(window.mMods)mMods.openWithPrefix(m,'/api/sisi-sources');
  }
  function showInstall(){
    document.getElementById('ss-install-bd').classList.add('show');
    document.getElementById('ss-install').classList.add('open');
  }
  function hideInstall(){
    document.getElementById('ss-install-bd').classList.remove('show');
    document.getElementById('ss-install').classList.remove('open');
  }
  function installFromURL(){
    var u=document.getElementById('ss-install-url').value.trim();
    if(!u){if(window.toast)toast('Укажите URL','error');return;}
    fetch(basePath+'/api/sisi-sources/install',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:u})})
      .then(function(r){return r.json();})
      .then(function(r){if(r.ok){if(window.toast)toast('Установлен: '+r.id);hideInstall();refresh();}else if(window.toast)toast(r.error||'Ошибка','error');});
  }
  function installFromFile(){
    var f=document.getElementById('ss-install-file').files[0];
    if(!f){if(window.toast)toast('Выберите файл','error');return;}
    var fr=new FileReader();
    fr.onload=function(){
      fetch(basePath+'/api/sisi-sources/install',{method:'POST',headers:{'Content-Type':'application/zip'},body:fr.result})
        .then(function(r){return r.json();})
        .then(function(r){if(r.ok){if(window.toast)toast('Установлен: '+r.id);hideInstall();refresh();}else if(window.toast)toast(r.error||'Ошибка','error');});
    };
    fr.readAsArrayBuffer(f);
  }
  // --- SISI Hub catalog ---
  var ssHubCache=[];
  function showHub(){
    document.getElementById('ss-hub-bd').classList.add('show');
    document.getElementById('ss-hub').classList.add('open');
    refreshHub();
  }
  function hideHub(){
    document.getElementById('ss-hub-bd').classList.remove('show');
    document.getElementById('ss-hub').classList.remove('open');
  }
  function refreshHub(){
    var url=document.getElementById('ss-hub-url').value.trim();
    var status=document.getElementById('ss-hub-status');
    var box=document.getElementById('ss-hub-list');
    status.textContent='Загрузка каталога...';
    status.style.color='var(--text-muted)';
    box.innerHTML='';
    fetch(url,{headers:{'Accept':'application/json'}})
      .then(function(r){if(!r.ok)throw new Error('HTTP '+r.status);return r.json();})
      .then(function(data){
        ssHubCache=(data&&(data.sources||data.plugins))||[];
        status.textContent='Источников: '+ssHubCache.length+' · обновлён '+(data.updated_at||'');
        renderHub();
      })
      .catch(function(e){status.textContent='Ошибка: '+e.message;status.style.color='var(--warning)';});
  }
  function filterHub(){renderHub();}
  function renderHub(){
    var q=(document.getElementById('ss-hub-search').value||'').toLowerCase();
    var box=document.getElementById('ss-hub-list');
    var installed={};
    (state.list||[]).forEach(function(m){installed[m.id]=m;});
    var rows=ssHubCache.filter(function(p){
      if(!q)return true;
      return (p.name||'').toLowerCase().indexOf(q)>=0||
             (p.display_name||'').toLowerCase().indexOf(q)>=0||
             (p.description||'').toLowerCase().indexOf(q)>=0||
             (p.tags||[]).join(',').toLowerCase().indexOf(q)>=0;
    }).map(function(p){
      var mfId=(p.manifest&&p.manifest.id)||p.id||p.name;
      var have=installed[mfId];
      var haveVer=have&&have.version||'';
      var cmp=haveVer&&p.version&&(haveVer===p.version)?'installed':haveVer?'update':'new';
      var badge=cmp==='installed'
        ?'<span class="tag" style="background:rgba(63,185,80,0.2);color:#7ddb8a">установлен</span>'
        :cmp==='update'
          ?'<span class="tag" style="background:rgba(210,153,34,0.18);color:#e6c767">обновление v'+esc(haveVer)+' → v'+esc(p.version)+'</span>'
          :'';
      var tags=(p.tags||[]).slice(0,4).map(function(t){return '<span class="tag" style="background:rgba(255,255,255,0.08);color:var(--text-muted)">'+esc(t)+'</span>';}).join('');
      var author=p.author?' · <span style="color:var(--text-muted)">'+esc(p.author)+'</span>':'';
      var rating=p.rating_n?' · ★ '+(p.rating_avg||0).toFixed(1)+' ('+p.rating_n+')':'';
      var url=p.download_url||p.zip_url||p.module_url||p.source_url||'';
      return '<div style="padding:12px 14px;border:1px solid var(--border);border-radius:10px;margin-bottom:8px;display:flex;gap:12px;align-items:center">'+
        '<div style="flex:1;min-width:0">'+
          '<div style="display:flex;gap:8px;align-items:baseline;flex-wrap:wrap">'+
            '<div style="font-weight:600">'+esc(p.display_name||p.name||mfId)+'</div>'+
            '<code style="font-size:11px">'+esc(mfId)+'</code>'+
            '<span style="font-size:11px;color:var(--text-muted)">v'+esc(p.version||'?')+'</span>'+
            badge+
          '</div>'+
          '<div style="font-size:12px;color:var(--text-dim);margin:4px 0">'+esc(p.description||'')+'</div>'+
          '<div style="font-size:11px;color:var(--text-muted)">'+tags+author+rating+' · ⬇ '+(p.downloads||0)+'</div>'+
        '</div>'+
        '<div style="display:flex;gap:6px">'+
          (p.homepage?'<a class="btn btn-sm" href="'+esc(p.homepage)+'" target="_blank" rel="noopener">🔗</a>':'')+
          '<button class="btn btn-sm btn-primary" onclick="sisiMod.installFromHub(\''+esc(url)+'\')">'+(cmp==='update'?'Обновить':'Установить')+'</button>'+
        '</div>'+
      '</div>';
    });
    box.innerHTML=rows.join('')||'<div style="padding:30px;text-align:center;color:var(--text-muted)">Ничего не найдено.</div>';
  }
  function installFromHub(url){
    if(!url){if(window.toast)toast('URL пуст','error');return;}
    if(!confirm('Установить SISI источник с hub?\n\n'+url))return;
    fetch(basePath+'/api/sisi-sources/install',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:url})})
      .then(function(r){return r.json();})
      .then(function(r){if(r.ok){if(window.toast)toast('Установлен: '+r.id);refresh();renderHub();}else if(window.toast)toast(r.error||'Ошибка','error');});
  }
  function filter(){render();}
  return{refresh:refresh,render:render,filter:filter,toggle:toggle,reload:reload,remove:remove,open:open,showInstall:showInstall,hideInstall:hideInstall,installFromURL:installFromURL,installFromFile:installFromFile,showHub:showHub,hideHub:hideHub,refreshHub:refreshHub,filterHub:filterHub,installFromHub:installFromHub};
})();
window.sisiMod=sisiMod;

// --- DrochHUB ---

// --- Cluster (clusterMod) ---------------------------------------------------
// Управление /api/cluster — каскадом lampac-go нод. CRUD, настройки,
// ручной probe. Авто-refresh каждые 5с пока вкладка открыта.
var clusterMod=(function(){
  var state={data:null};
  var timer=null;
  var editing=null; // null = add new, иначе stored node object
  function api(opts){return fetch(basePath+'/api/cluster',opts||{}).then(function(r){return r.json().catch(function(){return{};});});}
  function fmt(n){if(!n&&n!==0)return '—';return Math.round(n).toString();}
  function esc(s){return (s||'').toString().replace(/[&<>"]/g,function(c){return{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c];});}
  function refresh(){
    api().then(function(d){
      state.data=d||{};
      render();
      scheduleNext();
    }).catch(function(e){
      var b=document.getElementById('cluster-banner');
      if(b){b.style.display='block';b.textContent='Ошибка загрузки: '+e.message;}
    });
  }
  function scheduleNext(){
    if(timer){clearTimeout(timer);timer=null;}
    var auto=document.getElementById('cl-auto');
    var panel=document.getElementById('panel-cluster');
    if(auto&&auto.checked&&panel&&panel.classList.contains('active')){
      timer=setTimeout(refresh,5000);
    }
  }
  function render(){
    var d=state.data||{};
    var b=document.getElementById('cluster-banner');
    if(b){
      if(!d.enabled||d.mode!=='primary'){
        b.style.display='block';
        b.innerHTML='<b>Кластер не активен.</b> Включите <code>[cluster]</code> в config.toml: <code>enable = true</code>, <code>mode = "primary"</code>, задайте <code>api_key</code> и перезапустите сервер.';
      } else if(!d.api_key_set){
        b.style.display='block';
        b.style.background='#7f1d1d';b.style.borderColor='#f87171';
        b.innerHTML='<b>⚠️ API ключ не задан</b> — ноды не смогут отвечать на ping. Установите <code>cluster.api_key</code> в config.toml (одинаковый на primary и нодах).';
      } else if(!d.shared_secret_set){
        b.style.display='block';
        b.style.background='#7c2d12';b.style.borderColor='#fb923c';
        b.innerHTML='<b>⚠️ proxy_link.shared_secret не задан</b> — кросс-нодовые <code>/proxy/&lt;url&gt;</code> ссылки не расшифруются и стримы будут падать с 500. Добавьте в config.toml одинаковую строку:<pre style="margin:6px 0 0;background:rgba(0,0,0,0.3);padding:6px;border-radius:4px;font-size:11px">[proxy_link]\nshared_secret = "длинная-случайная-строка"</pre>';
      } else if(d.settings&&d.settings.force_node){
        b.style.display='block';
        b.style.background='#7c2d12';b.style.borderColor='#fb923c';
        b.innerHTML='<b>🧪 Тестовый режим активен:</b> все запросы принудительно форвардятся на ноды (primary игнорируется при выборе). Выключи в «⚙️ Настройки» когда закончишь проверку.';
      } else if(d.settings&&d.settings.rules&&d.settings.rules.length){
        b.style.display='block';
        b.style.background='#1e3a5f';b.style.borderColor='#3b82f6';
        var nodeNameByID={};(d.nodes||[]).forEach(function(n){nodeNameByID[n.id]=n.name;});
        var ruleSummary=d.settings.rules.map(function(r){
          var arrow=r.target==='local'?'⌂ primary':(r.target==='node-id'?'→ '+(nodeNameByID[r.node_id]||r.node_id):'→ нода');
          return '<code style="background:rgba(0,0,0,0.3);padding:1px 6px;border-radius:3px;margin:2px 4px 2px 0;display:inline-block">'+esc(r.balancer)+' '+arrow+'</code>';
        }).join('');
        b.innerHTML='<b>📋 Активные правила маршрутизации:</b><br><div style="margin-top:4px;font-size:11px;line-height:1.8">'+ruleSummary+'</div>';
      } else {
        b.style.display='block';
        b.style.background='';b.style.borderColor='';
        b.innerHTML='<div style="font-size:11px;color:var(--text-muted);line-height:1.6"><b>Модель доверия:</b> пользователи аутентифицируются только на primary (этот сервер). Ноды должны иметь тот же <code>cluster.api_key</code> и <code>proxy_link.shared_secret</code> в config.toml — primary валидирует пользователя и форвардит запросы с <code>X-Cluster-Key</code>, ноды доверяют этому заголовку и пропускают TG-auth/WAF.</div>';
      }
    }
    document.getElementById('cl-mode').textContent=(d.mode||'—')+(d.enabled?'':' (off)');
    document.getElementById('cl-apikey').textContent=d.api_key_set?'API ключ установлен':'API ключ не задан';
    document.getElementById('cl-strategy').textContent=(d.settings&&d.settings.strategy)||'—';
    var nodes=d.nodes||[];
    var alive=0;nodes.forEach(function(n){if(n.healthy&&n.enabled)alive++;});
    document.getElementById('cl-counts').textContent=alive+' / '+nodes.length;
    var lc=d.local||{};
    document.getElementById('cl-local').textContent=fmt(lc.active_conns);
    document.getElementById('cl-local-total').textContent=fmt(lc.total_served);

    var rows=document.getElementById('cl-rows');
    if(!nodes.length){rows.innerHTML='<tr><td colspan="15" style="text-align:center;color:var(--text-muted);padding:20px">Ноды не добавлены. Нажмите «Добавить ноду».</td></tr>';renderLocal(d.local);return;}
    var html='';
    nodes.forEach(function(n){
      var statusBadge;
      if(!n.enabled){statusBadge='<span style="color:#999">⏸ Disabled</span>';}
      else if(n.healthy){statusBadge='<span style="color:#4caf50">● Healthy</span>';}
      else {statusBadge='<span style="color:#f44336" title="'+esc(n.last_error||'')+'">● Down</span>';}
      var topBal=topBalancer(n.by_balancer);
      var hasDetail=(n.by_balancer&&Object.keys(n.by_balancer).length>0)||n.unique_clients_5m>0;
      var expandedKey='cl-exp-'+n.id;
      var isExpanded=window[expandedKey]||false;
      var arrow=isExpanded?'▼':'▶';
      html+='<tr data-node="'+n.id+'" style="cursor:'+(hasDetail?'pointer':'default')+'" onclick="clusterMod.toggleDetail(\''+n.id+'\')">'+
        '<td style="width:24px;text-align:center;color:var(--text-muted)">'+(hasDetail?arrow:'')+'</td>'+
        '<td><b>'+esc(n.name)+'</b></td>'+
        '<td style="font-family:monospace;font-size:11px">'+esc(n.host)+'</td>'+
        '<td>'+esc(n.region||'')+'</td>'+
        '<td>'+statusBadge+'</td>'+
        '<td>'+fmt(n.active_conns)+'</td>'+
        '<td>'+fmt(n.total_served)+'</td>'+
        '<td'+(n.total_failed>0?' style="color:#f44336"':'')+'>'+fmt(n.total_failed)+'</td>'+
        '<td><b>'+fmt(n.unique_clients_5m||0)+'</b></td>'+
        '<td>'+fmtBytes(n.bytes_out||0)+'</td>'+
        '<td style="font-size:11px;color:var(--text-muted)">'+esc(topBal)+'</td>'+
        '<td>'+fmt(n.avg_latency_ms)+'</td>'+
        '<td>'+fmt(n.last_latency_ms)+'</td>'+
        '<td>'+n.weight+'</td>'+
        '<td style="white-space:nowrap" onclick="event.stopPropagation()">'+
          '<button class="btn btn-sm" onclick="clusterMod.probe(\''+n.id+'\')" title="Probe ping">📡</button> '+
          '<button class="btn btn-sm" onclick="clusterMod.toggle(\''+n.id+'\','+(!n.enabled)+')" title="'+(n.enabled?'Отключить':'Включить')+'">'+(n.enabled?'⏸':'▶')+'</button> '+
          '<button class="btn btn-sm" onclick="clusterMod.openEdit(\''+n.id+'\')">✏️</button> '+
          '<button class="btn btn-sm" onclick="clusterMod.del(\''+n.id+'\')" style="color:#f44336">🗑</button>'+
        '</td>'+
      '</tr>';
      if(isExpanded&&hasDetail){
        html+='<tr><td></td><td colspan="14" style="background:rgba(0,0,0,0.2);padding:10px 14px">'+
          renderBalancerBreakdown(n.by_balancer,n.unique_clients_5m)+
        '</td></tr>';
      }
    });
    rows.innerHTML=html;
    renderLocal(d.local);
    renderEvents(d.recent_events);
  }
  function renderEvents(events){
    var box=document.getElementById('cl-events-block');
    if(!box)return;
    if(!events||!events.length){
      box.innerHTML='<div style="background:var(--surface2);border:1px solid var(--border);border-radius:10px;padding:14px;color:var(--text-muted);font-size:12px"><b>📋 Последние маршрутизации</b><br>Событий пока нет — запусти что-нибудь в Lampa.</div>';
      return;
    }
    var html='<div style="background:var(--surface2);border:1px solid var(--border);border-radius:10px;padding:14px">'+
      '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">'+
        '<b style="font-size:14px">📋 Последние маршрутизации (свежие сверху)</b>'+
        '<span style="color:var(--text-muted);font-size:11px">'+events.length+' событий · обновляется каждые 5с</span>'+
      '</div>'+
      '<div style="max-height:320px;overflow-y:auto;font-family:ui-monospace,Menlo,monospace;font-size:11px">';
    events.forEach(function(e){
      var t=new Date(e.time);
      var hh=String(t.getHours()).padStart(2,'0'),mm=String(t.getMinutes()).padStart(2,'0'),ss=String(t.getSeconds()).padStart(2,'0');
      var local=e.target==='local';
      var color=local?'#9ca3af':(e.failed?'#f44336':'#3b82f6');
      var arrow=local?'⌂':'→';
      html+='<div style="display:flex;gap:10px;padding:4px 0;border-bottom:1px solid rgba(255,255,255,0.04)">'+
        '<span style="color:var(--text-muted);width:60px;flex-shrink:0">'+hh+':'+mm+':'+ss+'</span>'+
        '<span style="color:'+color+';width:18px;flex-shrink:0">'+arrow+'</span>'+
        '<span style="font-weight:600;width:90px;flex-shrink:0;color:'+(local?'#9ca3af':'#22c55e')+'">'+esc(e.target||'?')+'</span>'+
        '<span style="width:90px;flex-shrink:0">'+esc(e.balancer||'-')+'</span>'+
        '<span style="color:var(--text-muted);width:120px;flex-shrink:0">'+esc(e.client_ip||'')+'</span>'+
        '<span style="color:var(--text-muted);min-width:60px;flex-shrink:0;text-align:right">'+(e.latency_ms?e.latency_ms+'ms':'')+'</span>'+
        '<span style="color:var(--text-muted);min-width:80px;text-align:right">'+(e.bytes_out?fmtBytes(e.bytes_out):'')+'</span>'+
        '<span style="color:'+(e.status>=500?'#f44336':e.status>=400?'#f59e0b':'var(--text-muted)')+';margin-left:auto">'+(e.status?e.status:'')+'</span>'+
      '</div>';
    });
    html+='</div></div>';
    box.innerHTML=html;
  }
  function fmtBytes(b){
    if(!b)return '—';
    if(b<1024)return b+' B';
    if(b<1024*1024)return (b/1024).toFixed(1)+' KB';
    if(b<1024*1024*1024)return (b/1024/1024).toFixed(1)+' MB';
    return (b/1024/1024/1024).toFixed(2)+' GB';
  }
  function topBalancer(byBal){
    if(!byBal)return '—';
    var top=null,topV=0,total=0;
    for(var k in byBal){total+=byBal[k];if(byBal[k]>topV){topV=byBal[k];top=k;}}
    if(!top)return '—';
    return top+' ('+topV+')';
  }
  function renderBalancerBreakdown(byBal,uniq){
    var html='<div style="display:flex;gap:24px;flex-wrap:wrap;font-size:12px">';
    html+='<div><b style="color:var(--text)">Уникальных клиентов:</b> <span style="color:#22c55e;font-size:14px">'+(uniq||0)+'</span> <span style="color:var(--text-muted)">за последние 5 минут</span></div>';
    html+='</div>';
    if(!byBal||!Object.keys(byBal).length){return html+'<div style="font-size:11px;color:var(--text-muted);margin-top:6px">Запросов по балансёрам пока нет.</div>';}
    var entries=Object.keys(byBal).map(function(k){return [k,byBal[k]];}).sort(function(a,b){return b[1]-a[1];});
    var total=entries.reduce(function(s,e){return s+e[1];},0);
    html+='<div style="margin-top:8px"><b style="font-size:12px">Распределение запросов по балансёрам:</b></div>';
    html+='<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:6px;margin-top:6px">';
    entries.forEach(function(e){
      var pct=total?(e[1]*100/total).toFixed(1):0;
      html+='<div style="background:var(--surface3);padding:6px 10px;border-radius:6px;font-size:11px">'+
        '<div style="display:flex;justify-content:space-between"><b>'+esc(e[0])+'</b><span>'+e[1]+'</span></div>'+
        '<div style="height:3px;background:rgba(255,255,255,0.05);border-radius:2px;margin-top:4px;overflow:hidden">'+
          '<div style="height:100%;width:'+pct+'%;background:linear-gradient(90deg,#3b82f6,#22c55e)"></div>'+
        '</div>'+
        '<div style="color:var(--text-muted);font-size:10px;margin-top:2px">'+pct+'%</div>'+
      '</div>';
    });
    html+='</div>';
    return html;
  }
  function renderLocal(local){
    var box=document.getElementById('cl-local-block');
    if(!box)return;
    if(!local){box.innerHTML='';return;}
    var topBal=topBalancer(local.by_balancer);
    var html='<div style="background:var(--surface2);border:1px solid var(--border);border-radius:10px;padding:14px">'+
      '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">'+
        '<b style="font-size:14px">🖥 Локально на primary</b>'+
        '<span style="color:var(--text-muted);font-size:11px">стратегия: '+(local.strategy||'—')+'</span>'+
      '</div>'+
      '<div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px;font-size:12px">'+
        '<div><div style="color:var(--text-muted);font-size:10px;text-transform:uppercase">Live</div><div style="font-size:18px;font-weight:600">'+fmt(local.active_conns||0)+'</div></div>'+
        '<div><div style="color:var(--text-muted);font-size:10px;text-transform:uppercase">Served</div><div style="font-size:18px;font-weight:600">'+fmt(local.total_served||0)+'</div></div>'+
        '<div><div style="color:var(--text-muted);font-size:10px;text-transform:uppercase">Fail</div><div style="font-size:18px;font-weight:600'+(local.total_failed?';color:#f44336':'')+'">'+fmt(local.total_failed||0)+'</div></div>'+
        '<div><div style="color:var(--text-muted);font-size:10px;text-transform:uppercase">Users (5м)</div><div style="font-size:18px;font-weight:600;color:#22c55e">'+fmt(local.unique_clients_5m||0)+'</div></div>'+
        '<div><div style="color:var(--text-muted);font-size:10px;text-transform:uppercase">Traffic</div><div style="font-size:18px;font-weight:600">'+fmtBytes(local.bytes_out||0)+'</div></div>'+
        '<div><div style="color:var(--text-muted);font-size:10px;text-transform:uppercase">Top балансёр</div><div style="font-size:13px;font-weight:600">'+esc(topBal)+'</div></div>'+
      '</div>';
    if(local.by_balancer&&Object.keys(local.by_balancer).length){
      html+='<div style="margin-top:12px">'+renderBalancerBreakdown(local.by_balancer,local.unique_clients_5m)+'</div>';
    }
    html+='</div>';
    box.innerHTML=html;
  }
  function toggleDetail(id){
    var key='cl-exp-'+id;
    window[key]=!window[key];
    render();
  }
  function openAdd(){editing=null;openModal('Добавить ноду','','',1,'','',true);}
  function openEdit(id){
    var n=(state.data&&state.data.nodes||[]).find(function(x){return x.id===id;});
    if(!n)return;
    editing=n;
    openModal('Редактировать ноду',n.name,n.host,n.weight,n.region||'',n.notes||'',n.enabled);
  }
  function openModal(title,name,host,weight,region,notes,enabled){
    document.getElementById('cl-edit-title').textContent=title;
    document.getElementById('cl-f-name').value=name;
    document.getElementById('cl-f-host').value=host;
    document.getElementById('cl-f-weight').value=weight;
    document.getElementById('cl-f-region').value=region;
    document.getElementById('cl-f-notes').value=notes;
    document.getElementById('cl-f-enabled').checked=!!enabled;
    document.getElementById('cl-test-out').style.display='none';
    document.getElementById('cl-edit-modal').classList.add('open');
  }
  function closeEdit(){document.getElementById('cl-edit-modal').classList.remove('open');}
  function saveEdit(){
    var payload={
      name:document.getElementById('cl-f-name').value,
      host:document.getElementById('cl-f-host').value,
      weight:parseInt(document.getElementById('cl-f-weight').value)||1,
      region:document.getElementById('cl-f-region').value,
      notes:document.getElementById('cl-f-notes').value,
      enabled:document.getElementById('cl-f-enabled').checked
    };
    if(editing){
      payload={action:'update',id:editing.id,patch:payload};
    } else {
      payload.action='add';
    }
    api({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)}).then(function(r){
      if(r&&r.ok){
        closeEdit();
        refresh();
        if(r.secrets_generated){showSecrets(r.secrets_generated,true);}
      }
      else if(r&&r.error){toast('Ошибка: '+r.error,'error');}
    });
  }
  function showSecrets(s,firstTime){
    var snippet=s.copy_to_node_config||'';
    var note=firstTime
      ? '<b>Кластер инициализирован.</b> Эти значения сохранены в config.toml на primary. Скопируйте блок ниже и вставьте в config.toml на каждой ноде:'
      : '<b>Секреты обновлены.</b> Скопируйте блок ниже и обновите config.toml на каждой ноде (старые секреты больше не работают):';
    var overlay=document.createElement('div');
    overlay.className='srv-modal-overlay open';
    overlay.style.zIndex='1500';
    overlay.innerHTML='<div class="srv-modal" style="max-width:640px;text-align:left">'+
      '<h3>&#x1F510; Секреты кластера</h3>'+
      '<div style="font-size:13px;color:var(--text-muted);margin-bottom:10px;line-height:1.5">'+note+'</div>'+
      '<pre id="cl-snip" style="background:var(--surface3);border:1px solid var(--border);border-radius:8px;padding:12px;font-size:11px;font-family:monospace;white-space:pre-wrap;word-break:break-all;max-height:240px;overflow:auto;margin:0">'+escHTML(snippet)+'</pre>'+
      '<div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap;font-size:11px;color:var(--text-muted)">'+
        '<div style="flex:1"><b>cluster.api_key:</b><br><code style="font-size:10px">'+escHTML(s.api_key||'')+'</code></div>'+
        '<div style="flex:1"><b>proxy_link.shared_secret:</b><br><code style="font-size:10px">'+escHTML(s.shared_secret||'')+'</code></div>'+
      '</div>'+
      '<div class="srv-modal-actions">'+
        '<button class="btn" onclick="(function(){var t=document.getElementById(\'cl-snip\').textContent;navigator.clipboard?navigator.clipboard.writeText(t).then(function(){toast(\'Скопировано\',\'success\')}):toast(\'Скопировать вручную\',\'info\');})()">&#x1F4CB; Скопировать</button>'+
        '<button class="btn btn-secondary" onclick="this.closest(\'.srv-modal-overlay\').remove()">Закрыть</button>'+
      '</div>'+
    '</div>';
    document.body.appendChild(overlay);
  }
  function escHTML(s){return (s==null?'':s).toString().replace(/[&<>"]/g,function(c){return{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c];});}
  function testHost(){
    var host=document.getElementById('cl-f-host').value;
    if(!host){return;}
    var out=document.getElementById('cl-test-out');
    out.style.display='block';out.textContent='Тестирую '+host+' …';
    api({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'test',host:host})}).then(function(r){
      if(r&&r.ok){
        out.textContent='✓ HTTP '+(r.info&&r.info.status)+', '+(r.info&&r.info.latency_ms)+'ms — '+JSON.stringify(r.info.response||r.info.body||'');
      } else {
        out.textContent='✗ '+(r&&r.error||'unknown error');
      }
    });
  }
  function del(id){
    if(!confirm('Удалить ноду из кластера?'))return;
    api({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'delete',id:id})}).then(function(r){
      if(r&&r.ok){refresh();}
    });
  }
  function toggle(id,enabled){
    api({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'update',id:id,patch:{enabled:enabled}})}).then(function(r){
      if(r&&r.ok){refresh();}
    });
  }
  function probe(id){
    api({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'probe',id:id})}).then(function(r){
      if(r&&r.ok){toast('Probe: '+(r.status&&r.status.last_latency_ms)+'ms','success');refresh();}
      else {toast('Probe failed: '+(r&&r.error||''),'error');}
    });
  }
  function openSettings(){
    var s=(state.data&&state.data.settings)||{};
    document.getElementById('cl-s-strategy').value=s.strategy||'hybrid';
    document.getElementById('cl-s-lw').value=s.latency_weight==null?0.4:s.latency_weight;
    document.getElementById('cl-s-lw-val').textContent=document.getElementById('cl-s-lw').value;
    document.getElementById('cl-s-pi').value=s.probe_interval_sec||30;
    document.getElementById('cl-s-mr').value=s.max_retries==null?2:s.max_retries;
    document.getElementById('cl-s-ft').value=s.fail_threshold||3;
    document.getElementById('cl-s-rt').value=s.recover_threshold||2;
    document.getElementById('cl-s-name').value=s.advertise_name||'';
    document.getElementById('cl-s-region').value=s.advertise_region||'';
    document.getElementById('cl-s-expose').checked=s.expose_public_list!==false;
    document.getElementById('cl-s-hosts').checked=!!s.public_hosts;
    document.getElementById('cl-s-force').checked=!!s.force_node;
    state.editingRules=(s.rules||[]).map(function(r){return Object.assign({},r);});
    renderRules();
    document.getElementById('cl-settings-modal').classList.add('open');
  }
  function renderRules(){
    var box=document.getElementById('cl-rules-list');
    if(!box)return;
    var rules=state.editingRules||[];
    var nodes=(state.data&&state.data.nodes)||[];
    // Suggested balancers from current stats — pick top balancers locally + on each node.
    var suggestions={};
    var local=(state.data&&state.data.local&&state.data.local.by_balancer)||{};
    Object.keys(local).forEach(function(k){suggestions[k]=true;});
    nodes.forEach(function(n){if(n.by_balancer)Object.keys(n.by_balancer).forEach(function(k){suggestions[k]=true;});});
    var suggList=Object.keys(suggestions).sort();
    var dataListID='cl-bal-suggest';
    if(!document.getElementById(dataListID)){
      var dl=document.createElement('datalist');dl.id=dataListID;document.body.appendChild(dl);
    }
    document.getElementById(dataListID).innerHTML=suggList.map(function(b){return '<option value="'+esc(b)+'">';}).join('');

    if(!rules.length){
      box.innerHTML='<div style="font-size:11px;color:var(--text-muted);background:var(--surface3);padding:8px 12px;border-radius:6px">Правил нет. Все балансёры маршрутизируются по выбранной стратегии.</div>';
      return;
    }
    var html='';
    rules.forEach(function(r,idx){
      var nodeOptions='<option value="">— любая healthy нода —</option>';
      nodes.forEach(function(n){nodeOptions+='<option value="'+esc(n.id)+'"'+(r.node_id===n.id?' selected':'')+'>'+esc(n.name)+'</option>';});
      var nodeSelDisabled=(r.target!=='node-id')?'disabled':'';
      html+='<div style="display:grid;grid-template-columns:1fr 1fr 1.4fr auto;gap:6px;align-items:center;background:var(--surface3);padding:6px 8px;border-radius:6px">'+
        '<input type="text" list="'+dataListID+'" placeholder="балансёр (kinotochka)" value="'+esc(r.balancer||'')+'" oninput="clusterMod.editRule('+idx+',\'balancer\',this.value)" style="background:var(--surface2);border:1px solid var(--border);color:var(--text);padding:6px 8px;border-radius:4px;font-size:12px">'+
        '<select onchange="clusterMod.editRule('+idx+',\'target\',this.value)" style="background:var(--surface2);border:1px solid var(--border);color:var(--text);padding:6px 8px;border-radius:4px;font-size:12px">'+
          '<option value="node"'+(r.target==='node'?' selected':'')+'>→ любая нода</option>'+
          '<option value="node-id"'+(r.target==='node-id'?' selected':'')+'>→ конкретная нода</option>'+
          '<option value="local"'+(r.target==='local'?' selected':'')+'>⌂ только primary</option>'+
        '</select>'+
        '<select onchange="clusterMod.editRule('+idx+',\'node_id\',this.value)" '+nodeSelDisabled+' style="background:var(--surface2);border:1px solid var(--border);color:var(--text);padding:6px 8px;border-radius:4px;font-size:12px'+(nodeSelDisabled?';opacity:0.4':'')+'">'+nodeOptions+'</select>'+
        '<button type="button" class="btn btn-sm" onclick="clusterMod.removeRule('+idx+')" style="color:#f44336">🗑</button>'+
      '</div>';
    });
    box.innerHTML=html;
  }
  function addRule(){
    state.editingRules=state.editingRules||[];
    state.editingRules.push({balancer:'',target:'node',node_id:''});
    renderRules();
  }
  function removeRule(idx){
    state.editingRules.splice(idx,1);
    renderRules();
  }
  function editRule(idx,field,value){
    if(!state.editingRules||!state.editingRules[idx])return;
    state.editingRules[idx][field]=value;
    // When target changes off "node-id", clear node_id (and re-render for select disabled state).
    if(field==='target'){
      if(value!=='node-id')state.editingRules[idx].node_id='';
      renderRules();
    }
  }
  function closeSettings(){document.getElementById('cl-settings-modal').classList.remove('open');}
  function saveSettings(){
    var s={
      strategy:document.getElementById('cl-s-strategy').value,
      latency_weight:parseFloat(document.getElementById('cl-s-lw').value),
      probe_interval_sec:parseInt(document.getElementById('cl-s-pi').value),
      max_retries:parseInt(document.getElementById('cl-s-mr').value),
      fail_threshold:parseInt(document.getElementById('cl-s-ft').value),
      recover_threshold:parseInt(document.getElementById('cl-s-rt').value),
      advertise_name:document.getElementById('cl-s-name').value,
      advertise_region:document.getElementById('cl-s-region').value,
      expose_public_list:document.getElementById('cl-s-expose').checked,
      public_hosts:document.getElementById('cl-s-hosts').checked,
      force_node:document.getElementById('cl-s-force').checked,
      rules:(state.editingRules||[]).filter(function(r){return r.balancer&&r.target;})
    };
    api({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'settings',settings:s})}).then(function(r){
      if(r&&r.ok){closeSettings();refresh();toast('Настройки сохранены','success');}
      else {toast('Ошибка: '+(r&&r.error||''),'error');}
    });
  }
  function showSecretsNow(){
    api({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'ensure-secrets'})}).then(function(r){
      if(r&&r.ok){
        showSecrets({api_key:r.api_key,shared_secret:r.shared_secret,copy_to_node_config:r.copy_to_node_config},(r.api_key_generated||r.secret_generated));
        refresh();
      } else {toast('Ошибка: '+(r&&r.error||''),'error');}
    });
  }
  function regenSecrets(){
    if(!confirm('Перевыпустить api_key + shared_secret?\n\nВСЕ существующие ноды перестанут работать пока их config.toml не обновишь. Продолжить?'))return;
    api({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'regenerate-secrets'})}).then(function(r){
      if(r&&r.ok){
        showSecrets({api_key:r.api_key,shared_secret:r.shared_secret,copy_to_node_config:r.copy_to_node_config},false);
        refresh();
      } else {toast('Ошибка: '+(r&&r.error||''),'error');}
    });
  }
  document.addEventListener('input',function(e){
    if(e.target&&e.target.id==='cl-s-lw'){document.getElementById('cl-s-lw-val').textContent=e.target.value;}
  });
  return {refresh:refresh,openAdd:openAdd,openEdit:openEdit,closeEdit:closeEdit,saveEdit:saveEdit,testHost:testHost,del:del,toggle:toggle,probe:probe,openSettings:openSettings,closeSettings:closeSettings,saveSettings:saveSettings,showSecretsNow:showSecretsNow,regenSecrets:regenSecrets,toggleDetail:toggleDetail,addRule:addRule,removeRule:removeRule,editRule:editRule};
})();
window.clusterMod=clusterMod;

// --- Environment presets (envMod) -------------------------------------------
// Управляет /api/env-presets — установкой/удалением/run-post-install для
// PAC-файлов, systemd-сервисов и helper-скриптов с {{key}}-параметрами.
var envMod=(function(){
  var state={presets:[],hub:[]};
  var pendingPlan=null; // план install после plan_only=true (для confirm)

  function api(path,opts){return fetch(basePath+'/api/env-presets'+path,opts||{}).then(function(r){return r.json().catch(function(){return{};});});}
  function refresh(){
    return api('/').then(function(d){
      state.presets=d.presets||[];
      render();
    });
  }
  function render(){
    var box=document.getElementById('ep-list');
    var empty=document.getElementById('ep-empty');
    if(!box)return;
    if(!state.presets.length){box.innerHTML='';empty.style.display='';return}
    empty.style.display='none';
    box.innerHTML=state.presets.map(renderCard).join('');
  }
  function renderCard(p){
    var name=esc(p.name||p.id);
    var ver=esc(p.version||'?');
    var desc=esc(p.description||'');
    var installed=p.installed?'<span class="badge">установлен</span>':'<span class="badge warn">только манифест</span>';
    var ports=(p.ports||[]).length?'<div class="ep-meta"><b>Порты:</b> '+esc((p.ports||[]).join(', '))+'</div>':'';
    var deps='';
    if(p.deps){
      if((p.deps.apt||[]).length)deps+='<div class="ep-meta"><b>apt:</b> '+esc(p.deps.apt.join(' '))+'</div>';
      if((p.deps.pip||[]).length)deps+='<div class="ep-meta"><b>pip:</b> '+esc(p.deps.pip.join(' '))+'</div>';
    }
    var files=(p.files||[]).map(function(f){return f.src+' → '+f.dst+(f.template?' [tmpl]':'')+(f.needs_root?' [root]':'')}).join('\n');
    var cmds=(p.post_install||[]).join('\n');
    var cfg=p.config||{};
    var cfgList=Object.keys(cfg).map(function(k){return '<b>'+esc(k)+':</b> '+esc(String(cfg[k]))}).join(' · ');
    var cfgRow=cfgList?'<div class="ep-meta">'+cfgList+'</div>':'';
    return '<div class="ep-card">'+
      '<div class="ep-card-head"><div class="name">'+name+'</div><div class="ver">v'+ver+'</div>'+installed+'</div>'+
      (desc?'<div class="ep-meta">'+desc+'</div>':'')+
      cfgRow+ports+deps+
      (files?'<div class="ep-meta"><b>Файлы:</b></div><div class="ep-files">'+esc(files)+'</div>':'')+
      (cmds?'<div class="ep-meta"><b>post_install:</b></div><div class="ep-cmds">'+esc(cmds)+'</div>':'')+
      '<div class="ep-actions">'+
      (p.installed?'<button class="btn btn-sm btn-primary" onclick="envMod.runPost(\''+esc(p.id)+'\')">&#x25B6; Запустить post_install</button>':'')+
      (p.installed?'<button class="btn btn-sm btn-warning" onclick="envMod.uninstall(\''+esc(p.id)+'\')">&#x1F5D1; Удалить</button>':'')+
      '</div>'+
      '</div>';
  }

  function showInstall(){
    document.getElementById('ep-install-bd').classList.add('show');
    document.getElementById('ep-install').classList.add('open');
    document.getElementById('ep-install-preview').style.display='none';
    pendingPlan=null;
  }
  function hideInstall(){
    document.getElementById('ep-install-bd').classList.remove('show');
    document.getElementById('ep-install').classList.remove('open');
  }
  // Шаг 1 — plan_only:true. Сервер вытаскивает manifest, валидирует config,
  // показывает план (какие файлы и команды) — не делая ничего на диске.
  function previewInstall(cfg){
    var url=document.getElementById('ep-install-url').value.trim();
    if(!url){toast('Введите URL ZIP-архива','error');return}
    cfg=cfg||{};
    api('/install',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:url,config:cfg,plan_only:true})}).then(function(r){
      if(r.error){toast(r.error,'error');return}
      pendingPlan={url:url,plan:r.plan};
      renderPreview(r.plan,cfg);
    });
  }
  function renderPreview(plan,cfg){
    if(!plan||!plan.manifest)return;
    var m=plan.manifest;
    var box=document.getElementById('ep-install-preview');
    var paramsHtml=(m.params||[]).map(function(p){
      var val=(cfg||{})[p.key];if(val===undefined)val=p.default;
      var inp;
      if(p.type==='enum'){
        inp='<select id="ep-pp-'+esc(p.key)+'">'+(p.options||[]).map(function(o){return '<option value="'+esc(o)+'"'+(String(o)===String(val)?' selected':'')+'>'+esc(o)+'</option>'}).join('')+'</select>';
      }else if(p.type==='bool'){
        inp='<select id="ep-pp-'+esc(p.key)+'"><option value="true"'+(val===true||val==='true'?' selected':'')+'>true</option><option value="false"'+(!(val===true||val==='true')?' selected':'')+'>false</option></select>';
      }else{
        inp='<input type="'+(p.type==='int'?'number':(p.type==='secret'?'password':'text'))+'" id="ep-pp-'+esc(p.key)+'" value="'+esc(val==null?'':String(val))+'"'+(p.min!=null?' min="'+p.min+'"':'')+(p.max!=null?' max="'+p.max+'"':'')+'>';
      }
      return '<div class="ep-form-row"><label title="'+esc(p.description||'')+'">'+esc(p.label||p.key)+'</label>'+inp+'</div>';
    }).join('');
    var filesHtml=(plan.files||[]).map(function(f){
      var skip=f.skipped?' <span style="color:#ffc107">[пропущен: '+esc(f.skip_reason||'')+']</span>':'';
      return esc(f.src)+' → '+esc(f.dst)+(f.template?' [tmpl]':'')+skip;
    }).join('\n');
    var cmds=(plan.post_install||[]).join('\n');
    box.style.display='';
    box.innerHTML=
      '<div class="ep-meta"><b>Манифест:</b> '+esc(m.name)+' v'+esc(m.version)+(m.author?' by '+esc(m.author):'')+'</div>'+
      (m.description?'<div class="ep-meta">'+esc(m.description)+'</div>':'')+
      (paramsHtml?'<div class="ep-meta" style="margin-top:10px"><b>Параметры:</b></div>'+paramsHtml:'')+
      '<div class="ep-meta" style="margin-top:10px"><b>Файлы будут записаны:</b></div><div class="ep-files">'+esc(filesHtml)+'</div>'+
      (cmds?'<div class="ep-meta"><b>После установки выполнятся:</b></div><div class="ep-cmds">'+esc(cmds)+'</div>':'')+
      '<div style="margin-top:14px;display:flex;gap:8px;flex-wrap:wrap">'+
        '<button class="btn btn-sm" onclick="envMod.refreshPreview()">&#x21BB; Обновить план</button>'+
        '<button class="btn btn-sm btn-primary" onclick="envMod.confirmInstall()">&#x2705; Установить</button>'+
        '<button class="btn btn-sm" onclick="envMod.hideInstall()">Отмена</button>'+
      '</div>';
  }
  function gatherCfg(){
    if(!pendingPlan||!pendingPlan.plan||!pendingPlan.plan.manifest)return {};
    var cfg={};
    (pendingPlan.plan.manifest.params||[]).forEach(function(p){
      var el=document.getElementById('ep-pp-'+p.key);
      if(!el)return;
      var v=el.value;
      if(p.type==='int')v=parseInt(v,10);
      else if(p.type==='bool')v=(v==='true');
      cfg[p.key]=v;
    });
    return cfg;
  }
  function refreshPreview(){previewInstall(gatherCfg())}
  function confirmInstall(){
    if(!pendingPlan){toast('Сначала превью','error');return}
    var cfg=gatherCfg();
    api('/install',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:pendingPlan.url,config:cfg,plan_only:false})}).then(function(r){
      if(r.error){toast(r.error,'error');return}
      toast('Установлен. Не забудьте «Запустить post_install».');
      hideInstall();refresh();
    });
  }
  function runPost(id){
    if(!confirm('Выполнить post_install команды для '+id+'? Они запустятся под пользователем lampac-go.'))return;
    api('/'+id+'/run',{method:'POST'}).then(function(r){
      if(r.error){toast(r.error,'error');return}
      var lines=(r.results||[]).map(function(c){return '$ '+c.cmd+'\n['+c.exit_code+'] '+c.output+(c.err?'\nERR: '+c.err:'')}).join('\n\n');
      alert(lines||'(пусто)');
      refresh();
    });
  }
  function uninstall(id){
    if(!confirm('Удалить пресет '+id+'? Файлы из .installed.json будут удалены, post_uninstall команды выполнятся.'))return;
    api('/'+id+'/uninstall',{method:'POST'}).then(function(r){
      if(r.error){toast(r.error,'error');return}
      toast('Пресет '+id+' удалён');refresh();
    });
  }

  // ── Hub catalog ───────────────────────────────────────────────────────
  function showHub(){
    document.getElementById('ep-hub-bd').classList.add('show');
    document.getElementById('ep-hub').classList.add('open');
    refreshHub();
  }
  function hideHub(){
    document.getElementById('ep-hub-bd').classList.remove('show');
    document.getElementById('ep-hub').classList.remove('open');
  }
  function refreshHub(){
    var url=document.getElementById('ep-hub-url').value.trim();
    var status=document.getElementById('ep-hub-status');
    var box=document.getElementById('ep-hub-list');
    status.textContent='Загрузка...';
    fetch(url).then(function(r){return r.json()}).then(function(d){
      var plugins=d.plugins||[];
      state.hub=plugins;
      status.textContent=plugins.length+' пресетов в каталоге';
      box.innerHTML=plugins.map(function(p){
        return '<div class="ep-card">'+
          '<div class="ep-card-head"><div class="name">'+esc(p.display_name||p.slug)+'</div><div class="ver">v'+esc(p.version||'')+'</div></div>'+
          (p.description?'<div class="ep-meta">'+esc(p.description)+'</div>':'')+
          '<div class="ep-actions">'+
            '<button class="btn btn-sm btn-primary" onclick="envMod.installFromHub(\''+esc(p.source_url||'')+'\')">&#x2795; Установить</button>'+
          '</div>'+
        '</div>';
      }).join('');
    }).catch(function(e){status.textContent='Ошибка: '+e.message});
  }
  function installFromHub(url){
    if(!url){toast('source_url отсутствует','error');return}
    document.getElementById('ep-install-url').value=url;
    hideHub();showInstall();previewInstall();
  }

  return {refresh:refresh,render:render,showInstall:showInstall,hideInstall:hideInstall,
    previewInstall:previewInstall,refreshPreview:refreshPreview,confirmInstall:confirmInstall,
    runPost:runPost,uninstall:uninstall,
    showHub:showHub,hideHub:hideHub,refreshHub:refreshHub,installFromHub:installFromHub};
})();
window.envMod=envMod;

// --- Plugins ---
var pluginNames=[
  'dlna','tracks','transcoding','tmdb_proxy','online','catalog','sisi','torrserver',
  'external_player','backup','sync','bookmark','timecode','ads_free','youtube_feed',
  'stats','opensubs','dualsubs','player_redesign','hls_tracks','voice_switcher',
  'theme','screensaver','remote','migrate'
];
var pluginMeta={
  dlna:{icon:'\uD83D\uDCFA',label:'DLNA',desc:'\u0422\u0440\u0430\u043D\u0441\u043B\u044F\u0446\u0438\u044F \u043D\u0430 Smart TV \u0447\u0435\u0440\u0435\u0437 DLNA/UPnP'},
  tracks:{icon:'\uD83C\uDFB5',label:'Tracks',desc:'\u0412\u044B\u0431\u043E\u0440 \u0430\u0443\u0434\u0438\u043E\u0434\u043E\u0440\u043E\u0436\u0435\u043A \u0438 \u0441\u0443\u0431\u0442\u0438\u0442\u0440\u043E\u0432'},
  transcoding:{icon:'\uD83D\uDD04',label:'Transcoding',desc:'FFmpeg \u043F\u0435\u0440\u0435\u043A\u043E\u0434\u0438\u0440\u043E\u0432\u0430\u043D\u0438\u0435 MKV \u2192 HLS',settings:true},
  tmdb_proxy:{icon:'\uD83C\uDFAC',label:'TMDB Proxy',desc:'\u041F\u0440\u043E\u043A\u0441\u0438\u0440\u043E\u0432\u0430\u043D\u0438\u0435 \u0437\u0430\u043F\u0440\u043E\u0441\u043E\u0432 \u043A TMDB API',settings:true},
  online:{icon:'\uD83C\uDF10',label:'Online',desc:'\u041E\u043D\u043B\u0430\u0439\u043D \u043F\u0440\u043E\u0441\u043C\u043E\u0442\u0440 \u0447\u0435\u0440\u0435\u0437 \u0431\u0430\u043B\u0430\u043D\u0441\u0435\u0440\u044B'},
  catalog:{icon:'\uD83D\uDCDA',label:'Catalog',desc:'\u041A\u0430\u0442\u0430\u043B\u043E\u0433 \u0444\u0438\u043B\u044C\u043C\u043E\u0432 \u0438 \u0441\u0435\u0440\u0438\u0430\u043B\u043E\u0432'},
  sisi:{icon:'\uD83D\uDD1E',label:'SISI 18+',desc:'\u041A\u043E\u043D\u0442\u0435\u043D\u0442 \u0434\u043B\u044F \u0432\u0437\u0440\u043E\u0441\u043B\u044B\u0445',settings:true},
  torrserver:{icon:'\uD83E\uDDF2',label:'TorrServer',desc:'\u0418\u043D\u0442\u0435\u0433\u0440\u0430\u0446\u0438\u044F \u0441 TorrServer',settings:true},
  external_player:{icon:'\uD83C\uDFAC',label:'External Player',desc:'\u0412\u043D\u0435\u0448\u043D\u0438\u0439 \u043F\u043B\u0435\u0435\u0440 (VLC, MX Player)'},
  backup:{icon:'\uD83D\uDCBE',label:'Backup',desc:'\u0420\u0435\u0437\u0435\u0440\u0432\u043D\u043E\u0435 \u043A\u043E\u043F\u0438\u0440\u043E\u0432\u0430\u043D\u0438\u0435 \u043D\u0430\u0441\u0442\u0440\u043E\u0435\u043A'},
  sync:{icon:'\uD83D\uDD01',label:'Sync',desc:'\u0421\u0438\u043D\u0445\u0440\u043E\u043D\u0438\u0437\u0430\u0446\u0438\u044F \u043C\u0435\u0436\u0434\u0443 \u0441\u0435\u0440\u0432\u0435\u0440\u0430\u043C\u0438',settings:true},
  bookmark:{icon:'\uD83D\uDD16',label:'Bookmark',desc:'\u0417\u0430\u043A\u043B\u0430\u0434\u043A\u0438 \u0438 \u0438\u0437\u0431\u0440\u0430\u043D\u043D\u043E\u0435'},
  timecode:{icon:'\u23F1\uFE0F',label:'Timecode',desc:'\u0421\u043E\u0445\u0440\u0430\u043D\u0435\u043D\u0438\u0435 \u043F\u043E\u0437\u0438\u0446\u0438\u0438 \u043F\u0440\u043E\u0441\u043C\u043E\u0442\u0440\u0430'},
  ads_free:{icon:'\uD83D\uDEAB',label:'Ads Free',desc:'\u0411\u043B\u043E\u043A\u0438\u0440\u043E\u0432\u043A\u0430 VAST \u0440\u0435\u043A\u043B\u0430\u043C\u044B'},
  youtube_feed:{icon:'\u25B6\uFE0F',label:'YouTube',desc:'YouTube \u043F\u043E\u0438\u0441\u043A, \u043F\u043E\u0434\u043F\u0438\u0441\u043A\u0438 \u0438 \u043F\u043B\u0435\u0439\u043B\u0438\u0441\u0442\u044B'},
  stats:{icon:'\uD83D\uDCCA',label:'Stats Overlay',desc:'\u041E\u0432\u0435\u0440\u043B\u0435\u0439 \u0441\u0442\u0430\u0442\u0438\u0441\u0442\u0438\u043A\u0438 \u0432 \u043F\u043B\u0435\u0435\u0440\u0435'},
  opensubs:{icon:'\uD83D\uDCAC',label:'OpenSubtitles',desc:'\u0421\u0443\u0431\u0442\u0438\u0442\u0440\u044B \u0438\u0437 OpenSubtitles'},
  dualsubs:{icon:'\uD83D\uDCAC',label:'Dual Subtitles',desc:'\u0414\u0432\u043E\u0439\u043D\u044B\u0435 \u0441\u0443\u0431\u0442\u0438\u0442\u0440\u044B'},
  player_redesign:{icon:'\uD83C\uDFA8',label:'Player Redesign',desc:'\u041E\u0431\u043D\u043E\u0432\u043B\u0451\u043D\u043D\u044B\u0439 \u0434\u0438\u0437\u0430\u0439\u043D \u043F\u043B\u0435\u0435\u0440\u0430'},
  hls_tracks:{icon:'\uD83C\uDFB5',label:'HLS Audio Tracks',desc:'\u0412\u044B\u0431\u043E\u0440 \u0430\u0443\u0434\u0438\u043E\u0434\u043E\u0440\u043E\u0436\u0435\u043A HLS'},
  voice_switcher:{icon:'\uD83D\uDD0A',label:'Voice Switcher',desc:'\u041F\u0435\u0440\u0435\u043A\u043B\u044E\u0447\u0430\u0442\u0435\u043B\u044C \u043E\u0437\u0432\u0443\u0447\u043A\u0438'},
  theme:{icon:'\uD83C\uDFA8',label:'\u041E\u0444\u043E\u0440\u043C\u043B\u0435\u043D\u0438\u0435',desc:'\u0422\u0435\u043C\u044B \u0438 \u0432\u043D\u0435\u0448\u043D\u0438\u0439 \u0432\u0438\u0434'},
  screensaver:{icon:'\uD83C\uDF05',label:'Screensaver',desc:'Apple TV Aerial \u0441\u043A\u0440\u0438\u043D\u0441\u0435\u0439\u0432\u0435\u0440'},
  remote:{icon:'\uD83D\uDCF1',label:'Remote',desc:'\u0423\u043F\u0440\u0430\u0432\u043B\u0435\u043D\u0438\u0435 \u0447\u0435\u0440\u0435\u0437 TG Mini App'},
  migrate:{icon:'\uD83D\uDCE6',label:'Migrate',desc:'\u041C\u0438\u0433\u0440\u0430\u0446\u0438\u044F \u0434\u0430\u043D\u043D\u044B\u0445 \u0438\u0437 \u0441\u0442\u0430\u0440\u043E\u0433\u043E lampac'},
  web_player:{icon:'\u25B6',label:'\u041D\u043E\u0432\u044B\u0439 \u043F\u043B\u0435\u0435\u0440',desc:'\u0412\u0441\u0442\u0440\u043E\u0435\u043D\u043D\u044B\u0439 \u043F\u043B\u0435\u0435\u0440 (Shaka + HLS.js). \u0412\u044B\u043A\u043B\u044E\u0447\u0438\u0442\u0435 \u0434\u043B\u044F \u0441\u0442\u0430\u043D\u0434\u0430\u0440\u0442\u043D\u043E\u0433\u043E \u043F\u043B\u0435\u0435\u0440\u0430 Lampa',settings:false},
  web_player_android:{icon:'\uD83D\uDCF1',label:'\u041D\u043E\u0432\u044B\u0439 \u043F\u043B\u0435\u0435\u0440 (Android)',desc:'\u0412\u043A\u043B\u044E\u0447\u0438\u0442\u044C \u043D\u043E\u0432\u044B\u0439 \u043F\u043B\u0435\u0435\u0440 \u0438 \u043D\u0430 Android WebView (Lampa \u043F\u0440\u0438\u043B\u043E\u0436\u0435\u043D\u0438\u0435)',settings:false}
};
var pluginsData={};
window.loadPlugins=function(){
  api('/api/plugins').then(function(d){
    pluginsData=d;
    var g=document.getElementById('plugins-grid');
    var h='';
    pluginNames.forEach(function(n){
      var m=pluginMeta[n]||{icon:'\uD83E\uDDE9',label:n,desc:''};
      var on=d.initPlugins&&d.initPlugins[n];
      h+='<div class="pg-card'+(on?' active':' inactive')+'" id="pgcard-'+n+'">';
      h+='<div class="pg-icon">'+m.icon+'</div>';
      h+='<div class="pg-info"><div class="pg-name">'+m.label+'</div><div class="pg-desc">'+m.desc+'</div></div>';
      h+='<div class="pg-actions">';
      if(m.settings)h+='<button class="pg-gear" onclick="openPluginSettings(\''+n+'\')" title="\u041D\u0430\u0441\u0442\u0440\u043E\u0439\u043A\u0438">\u2699</button>';
      h+='<label class="toggle"><input type="checkbox" id="plugin-'+n+'" '+(on?'checked':'')+' onchange="onPluginToggle(\''+n+'\',this.checked)"><span class="slider"></span></label>';
      h+='</div></div>';
    });
    g.innerHTML=h;
  });
  loadCustomPlugins();
  if(window.loadCommunityPlugins)window.loadCommunityPlugins();
};
window.onPluginToggle=function(name,on){
  var card=document.getElementById('pgcard-'+name);
  if(card){card.className='pg-card'+(on?' active':' inactive');}
};
// --- Plugin settings panel ---
var pgSettingsRenderers={
  sisi:function(s){
    var fields=[{k:'spider',l:'Spider',t:'bool',tip:'\u0412\u043A\u043B\u044E\u0447\u0438\u0442\u044C \u043F\u043E\u0438\u0441\u043A\u043E\u0432\u044B\u0439 \u043F\u0430\u0443\u043A'},{k:'component',l:'Component',t:'str',tip:'\u0418\u043C\u044F \u043A\u043E\u043C\u043F\u043E\u043D\u0435\u043D\u0442\u0430 (sisi)'},{k:'iconame',l:'Icon Name',t:'str',tip:'\u0418\u043C\u044F \u0438\u043A\u043E\u043D\u043A\u0438'},{k:'push_all',l:'Push All',t:'bool',tip:'\u041E\u0442\u043F\u0440\u0430\u0432\u043B\u044F\u0442\u044C \u0432\u0441\u0435 \u0438\u0441\u0442\u043E\u0447\u043D\u0438\u043A\u0438'},{k:'history.enable',l:'History',t:'bool',tip:'\u0421\u043E\u0445\u0440\u0430\u043D\u044F\u0442\u044C \u0438\u0441\u0442\u043E\u0440\u0438\u044E'},{k:'forced_checkRchtype',l:'Forced RCH Type',t:'bool',tip:'Forced checkRchtype'}];
    return fields.map(function(f){
      var v=f.k.indexOf('.')>=0?((s.history||{})[f.k.split('.')[1]]):s[f.k];
      if(f.t==='bool')return '<div class="form-row"><label>'+f.l+(f.tip?'<span class="hint" data-tip="'+esc(f.tip)+'">?</span>':'')+'</label><label class="toggle"><input type="checkbox" id="sisi-'+f.k+'" '+(v?'checked':'')+'><span class="slider"></span></label></div>';
      return '<div class="form-row"><label>'+f.l+(f.tip?'<span class="hint" data-tip="'+esc(f.tip)+'">?</span>':'')+'</label><input class="input-sm" id="sisi-'+f.k+'" value="'+esc(v||'')+'"></div>';
    }).join('');
  },
  torrserver:function(ts){
    var h='';
    var mode=ts.url?'external':'internal';
    // Mode selector
    h+='<div style="display:flex;gap:8px;margin-bottom:16px">';
    h+='<label class="tmdb-mode-card'+(mode==='internal'?' selected':'')+'" onclick="tsModeSwitch(\'internal\')" style="flex:1;cursor:pointer">';
    h+='<input type="radio" name="ts-mode" value="internal" '+(mode==='internal'?'checked':'')+' style="display:none">';
    h+='<div class="tmdb-mode-icon">\uD83D\uDDA5</div>';
    h+='<div class="tmdb-mode-info"><div class="tmdb-mode-label">\u0412\u0441\u0442\u0440\u043E\u0435\u043D\u043D\u044B\u0439</div><div class="tmdb-mode-desc">\u041B\u043E\u043A\u0430\u043B\u044C\u043D\u044B\u0439 TorrServer</div></div>';
    h+='<div class="tmdb-mode-check">'+(mode==='internal'?'\u2713':'')+'</div>';
    h+='</label>';
    h+='<label class="tmdb-mode-card'+(mode==='external'?' selected':'')+'" onclick="tsModeSwitch(\'external\')" style="flex:1;cursor:pointer">';
    h+='<input type="radio" name="ts-mode" value="external" '+(mode==='external'?'checked':'')+' style="display:none">';
    h+='<div class="tmdb-mode-icon">\uD83C\uDF10</div>';
    h+='<div class="tmdb-mode-info"><div class="tmdb-mode-label">\u0412\u043D\u0435\u0448\u043D\u0438\u0439</div><div class="tmdb-mode-desc">\u0423\u0434\u0430\u043B\u0451\u043D\u043D\u044B\u0439 TorrServer</div></div>';
    h+='<div class="tmdb-mode-check">'+(mode==='external'?'\u2713':'')+'</div>';
    h+='</label>';
    h+='</div>';
    // Embedded banner
    if(ts._embedded&&mode==='internal'){h+='<div id="ts-embedded-banner" style="padding:10px 14px;border-radius:8px;background:rgba(0,200,100,0.1);border:1px solid rgba(0,200,100,0.3);margin-bottom:12px;font-size:12px"><span style="color:#00c864;font-weight:600">\u2713 TorrServer \u0432\u0441\u0442\u0440\u043E\u0435\u043D \u0432 \u0431\u0438\u043D\u0430\u0440\u043D\u0438\u043A</span></div>';}
    // --- INTERNAL MODE ---
    h+='<div id="ts-internal" style="'+(mode==='internal'?'':'display:none')+'">';
    // Caching section
    h+='<div style="font-size:13px;font-weight:600;color:var(--text);margin-bottom:10px">\uD83D\uDCE6 \u041A\u044D\u0448\u0438\u0440\u043E\u0432\u0430\u043D\u0438\u0435</div>';
    h+='<div class="form-row"><label>\u0422\u043E\u043B\u044C\u043A\u043E RAM<span class="hint" data-tip="\u041A\u0443\u0441\u043A\u0438 \u0442\u043E\u0440\u0440\u0435\u043D\u0442\u0430 \u0434\u0435\u0440\u0436\u0430\u0442\u0441\u044F \u0442\u043E\u043B\u044C\u043A\u043E \u0432 \u043F\u0430\u043C\u044F\u0442\u0438, \u043D\u0430 \u0434\u0438\u0441\u043A \u043D\u0435 \u043F\u0438\u0448\u0435\u0442\u0441\u044F \u043D\u0438\u0447\u0435\u0433\u043E.\n\u041F\u0440\u043E\u0439\u0434\u0435\u043D\u043D\u043E\u0435 \u0432\u044B\u0442\u0435\u0441\u043D\u044F\u0435\u0442\u0441\u044F, \u043F\u043E\u044D\u0442\u043E\u043C\u0443 \u0444\u0438\u043B\u044C\u043C \u043B\u044E\u0431\u043E\u0433\u043E \u0440\u0430\u0437\u043C\u0435\u0440\u0430 \u0438\u0434\u0451\u0442 \u0447\u0435\u0440\u0435\u0437 \u043D\u0435\u0431\u043E\u043B\u044C\u0448\u043E\u0439 \u043A\u044D\u0448.\n\u041F\u0435\u0440\u0435\u043C\u043E\u0442\u043A\u0430 \u043D\u0430\u0437\u0430\u0434 \u0441\u043A\u0430\u0447\u0438\u0432\u0430\u0435\u0442 \u043A\u0443\u0441\u043A\u0438 \u0437\u0430\u043D\u043E\u0432\u043E. \u0422\u0440\u0435\u0431\u0443\u0435\u0442 \u043F\u0435\u0440\u0435\u0437\u0430\u043F\u0443\u0441\u043A\u0430.">?</span></label><label class="toggle"><input type="checkbox" id="ts-ram-only" '+(ts.ram_cache?'checked':'')+'><span class="slider"></span></label></div>';
    h+='<div class="form-row"><label>RAM \u043A\u044D\u0448<span class="hint" data-tip="\u0411\u044E\u0434\u0436\u0435\u0442 \u043F\u0430\u043C\u044F\u0442\u0438 \u0432 \u0440\u0435\u0436\u0438\u043C\u0435 \u00AB\u0422\u043E\u043B\u044C\u043A\u043E RAM\u00BB (\u043C\u0438\u043D\u0438\u043C\u0443\u043C 128 \u041C\u0411; \u0431\u0435\u0440\u0438\u0442\u0435 \u0441 \u0437\u0430\u043F\u0430\u0441\u043E\u043C \u2014 1\u20132 \u0413\u0411 \u043D\u0430 \u0437\u0440\u0438\u0442\u0435\u043B\u044F).\n\u0412 \u0434\u0438\u0441\u043A\u043E\u0432\u043E\u043C \u0440\u0435\u0436\u0438\u043C\u0435 \u043F\u043E\u043B\u0435 \u043D\u0438 \u043D\u0430 \u0447\u0442\u043E \u043D\u0435 \u0432\u043B\u0438\u044F\u0435\u0442.">?</span></label><div style="display:flex;align-items:center;gap:6px"><input class="input-sm" id="ts-cache-ram" type="number" min="16" value="'+(ts.cache_size_mb||64)+'" style="width:80px"><span style="font-size:11px;color:var(--text-dim)">\u041C\u0411</span></div></div>';
    h+='<div class="form-row"><label>\u0414\u0438\u0441\u043A\u043E\u0432\u044B\u0439 \u043A\u044D\u0448<span class="hint" data-tip="\u041F\u043E\u0440\u043E\u0433 \u0430\u0432\u0442\u043E\u043E\u0447\u0438\u0441\u0442\u043A\u0438 \u043A\u044D\u0448\u0430 \u043D\u0430 \u0434\u0438\u0441\u043A\u0435.\n0 = \u0432\u0437\u044F\u0442\u044C \u043B\u0438\u043C\u0438\u0442 \u0438\u0437 cache_cleanup_max_gb (50 \u0413\u0411), \u0430 \u041D\u0415 \u043E\u0442\u043A\u043B\u044E\u0447\u0438\u0442\u044C \u0434\u0438\u0441\u043A \u2014 \u0434\u043B\u044F \u044D\u0442\u043E\u0433\u043E \u0435\u0441\u0442\u044C \u00AB\u0422\u043E\u043B\u044C\u043A\u043E RAM\u00BB.">?</span></label><div style="display:flex;align-items:center;gap:6px"><input class="input-sm" id="ts-cache-disk" type="number" min="0" value="'+(ts.disk_cache_mb||1024)+'" style="width:80px"><span style="font-size:11px;color:var(--text-dim)">\u041C\u0411</span></div></div>';
    h+='<div class="form-row"><label>Preload<span class="hint" data-tip="\u041F\u0440\u0435\u0434\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430 \u043F\u0435\u0440\u0435\u0434 \u043D\u0430\u0447\u0430\u043B\u043E\u043C \u0432\u043E\u0441\u043F\u0440\u043E\u0438\u0437\u0432\u0435\u0434\u0435\u043D\u0438\u044F.">?</span></label><div style="display:flex;align-items:center;gap:6px"><input class="input-sm" id="ts-preload" type="number" min="1" value="'+(ts.preload_mb||5)+'" style="width:80px"><span style="font-size:11px;color:var(--text-dim)">\u041C\u0411</span></div></div>';
    // Limits section
    h+='<div style="font-size:13px;font-weight:600;color:var(--text);margin:16px 0 10px;padding-top:12px;border-top:1px solid rgba(255,255,255,0.06)">\uD83D\uDE80 \u041B\u0438\u043C\u0438\u0442\u044B</div>';
    h+='<div class="form-row"><label>\u0421\u043A\u043E\u0440\u043E\u0441\u0442\u044C \u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0438<span class="hint" data-tip="0 = \u0431\u0435\u0437 \u043E\u0433\u0440\u0430\u043D\u0438\u0447\u0435\u043D\u0438\u0439">?</span></label><div style="display:flex;align-items:center;gap:6px"><input class="input-sm" id="ts-max-dl" type="number" min="0" value="'+(ts.max_download_speed_mb||0)+'" style="width:80px"><span style="font-size:11px;color:var(--text-dim)">\u041C\u0411/\u0441</span></div></div>';
    h+='<div class="form-row"><label>\u0421\u043A\u043E\u0440\u043E\u0441\u0442\u044C \u043E\u0442\u0434\u0430\u0447\u0438<span class="hint" data-tip="0 = \u0431\u0435\u0437 \u043E\u0433\u0440\u0430\u043D\u0438\u0447\u0435\u043D\u0438\u0439">?</span></label><div style="display:flex;align-items:center;gap:6px"><input class="input-sm" id="ts-max-ul" type="number" min="0" value="'+(ts.max_upload_speed_mb||0)+'" style="width:80px"><span style="font-size:11px;color:var(--text-dim)">\u041C\u0411/\u0441</span></div></div>';
    h+='<div class="form-row"><label>\u041C\u0430\u043A\u0441. \u0442\u043E\u0440\u0440\u0435\u043D\u0442\u043E\u0432<span class="hint" data-tip="\u041E\u0433\u0440\u0430\u043D\u0438\u0447\u0435\u043D\u0438\u0435 \u0430\u043A\u0442\u0438\u0432\u043D\u044B\u0445 \u0442\u043E\u0440\u0440\u0435\u043D\u0442\u043E\u0432. 0 = \u0431\u0435\u0437 \u043B\u0438\u043C\u0438\u0442\u0430.">?</span></label><div style="display:flex;align-items:center;gap:6px"><input class="input-sm" id="ts-max-torrents" type="number" min="0" value="'+(ts.max_active_torrents||0)+'" style="width:80px"></div></div>';
    h+='<div class="form-row"><label>\u041E\u0442\u043A\u043B\u044E\u0447\u0438\u0442\u044C DHT<span class="hint" data-tip="\u041E\u0442\u043A\u043B\u044E\u0447\u0430\u0435\u0442 \u0440\u0430\u0441\u043F\u0440\u0435\u0434\u0435\u043B\u0451\u043D\u043D\u0443\u044E \u0445\u0435\u0448-\u0442\u0430\u0431\u043B\u0438\u0446\u0443.\n\u041C\u043E\u0436\u0435\u0442 \u0443\u043C\u0435\u043D\u044C\u0448\u0438\u0442\u044C \u0442\u0440\u0430\u0444\u0438\u043A, \u043D\u043E \u0441\u043D\u0438\u0437\u0438\u0442 \u0434\u043E\u0441\u0442\u0443\u043F\u043D\u043E\u0441\u0442\u044C \u043F\u0438\u0440\u043E\u0432.">?</span></label><label class="toggle"><input type="checkbox" id="ts-dht" '+(ts.disable_dht?'checked':'')+'><span class="slider"></span></label></div>';
    h+='<div class="form-row"><label>\u041E\u0442\u043A\u043B\u044E\u0447\u0438\u0442\u044C Upload<span class="hint" data-tip="\u041D\u0435 \u0440\u0430\u0437\u0434\u0430\u0432\u0430\u0442\u044C \u0434\u0430\u043D\u043D\u044B\u0435 \u0434\u0440\u0443\u0433\u0438\u043C \u043F\u0438\u0440\u0430\u043C.\n\u0420\u0435\u043A\u043E\u043C\u0435\u043D\u0434\u0443\u0435\u0442\u0441\u044F \u0434\u043B\u044F \u044D\u043A\u043E\u043D\u043E\u043C\u0438\u0438 \u0442\u0440\u0430\u0444\u0438\u043A\u0430.">?</span></label><label class="toggle"><input type="checkbox" id="ts-upload" '+(ts.disable_upload!==false?'checked':'')+'><span class="slider"></span></label></div>';
    // Cleanup section
    h+='<div style="font-size:13px;font-weight:600;color:var(--text);margin:16px 0 10px;padding-top:12px;border-top:1px solid rgba(255,255,255,0.06)">\uD83E\uDDF9 \u0410\u0432\u0442\u043E-\u043E\u0447\u0438\u0441\u0442\u043A\u0430</div>';
    h+='<div class="form-row"><label>\u0412\u043A\u043B\u044E\u0447\u0438\u0442\u044C<span class="hint" data-tip="\u0410\u0432\u0442\u043E\u043C\u0430\u0442\u0438\u0447\u0435\u0441\u043A\u0438 \u0443\u0434\u0430\u043B\u044F\u0442\u044C \u0441\u0442\u0430\u0440\u044B\u0435 \u0442\u043E\u0440\u0440\u0435\u043D\u0442\u044B \u0438 \u043A\u044D\u0448, \u0447\u0442\u043E\u0431\u044B \u0434\u0438\u0441\u043A \u043D\u0435 \u0437\u0430\u0431\u0438\u0432\u0430\u043B\u0441\u044F.">?</span></label><label class="toggle"><input type="checkbox" id="ts-cleanup" '+(ts.cache_cleanup_enable?'checked':'')+' onchange="document.getElementById(\'ts-cleanup-fields\').style.display=this.checked?\'\':\'none\'"><span class="slider"></span></label></div>';
    h+='<div id="ts-cleanup-fields" style="'+(ts.cache_cleanup_enable?'':'display:none')+'">';
    h+='<div class="form-row"><label>\u0423\u0434\u0430\u043B\u044F\u0442\u044C \u0441\u0442\u0430\u0440\u0448\u0435<span class="hint" data-tip="\u0422\u043E\u0440\u0440\u0435\u043D\u0442\u044B \u043D\u0435\u0430\u043A\u0442\u0438\u0432\u043D\u044B\u0435 \u0431\u043E\u043B\u0435\u0435 N \u0434\u043D\u0435\u0439 \u0431\u0443\u0434\u0443\u0442 \u0443\u0434\u0430\u043B\u0435\u043D\u044B.">?</span></label><div style="display:flex;align-items:center;gap:6px"><input class="input-sm" id="ts-cleanup-days" type="number" min="1" value="'+(ts.cache_cleanup_days||7)+'" style="width:80px"><span style="font-size:11px;color:var(--text-dim)">\u0434\u043D\u0435\u0439</span></div></div>';
    h+='<div class="form-row"><label>\u041C\u0430\u043A\u0441. \u0440\u0430\u0437\u043C\u0435\u0440 \u043A\u044D\u0448\u0430<span class="hint" data-tip="\u041F\u0440\u0438 \u043F\u0440\u0435\u0432\u044B\u0448\u0435\u043D\u0438\u0438 \u044D\u0442\u043E\u0433\u043E \u043E\u0431\u044A\u0451\u043C\u0430 \u0441\u0442\u0430\u0440\u044B\u0435 \u0442\u043E\u0440\u0440\u0435\u043D\u0442\u044B \u0443\u0434\u0430\u043B\u044F\u044E\u0442\u0441\u044F.">?</span></label><div style="display:flex;align-items:center;gap:6px"><input class="input-sm" id="ts-cleanup-maxgb" type="number" min="1" value="'+(ts.cache_cleanup_max_gb||50)+'" style="width:80px"><span style="font-size:11px;color:var(--text-dim)">\u0413\u0411</span></div></div>';
    h+='</div>';
    // Port & Auth
    h+='<div style="font-size:13px;font-weight:600;color:var(--text);margin:16px 0 10px;padding-top:12px;border-top:1px solid rgba(255,255,255,0.06)">\uD83D\uDD27 \u041F\u043E\u0434\u043A\u043B\u044E\u0447\u0435\u043D\u0438\u0435</div>';
    h+='<div class="form-row"><label>\u041F\u043E\u0440\u0442</label><input class="input-sm" id="ts-port" type="number" value="'+(ts.port||9080)+'" style="width:80px"></div>';
    h+='<div class="form-row"><label>Login</label><input class="input-sm input-wide" id="ts-login" value="'+esc(ts.login||'')+'" placeholder="ts"></div>';
    h+='<div class="form-row"><label>Password</label><input class="input-sm input-wide" id="ts-password" type="password" value="'+esc(ts.password||'')+'" placeholder="'+(ts._password_loaded?'\u2713 \u0437\u0430\u0433\u0440\u0443\u0436\u0435\u043D \u0438\u0437 accs.db':'\u0443\u043A\u0430\u0436\u0438\u0442\u0435 \u0432\u0440\u0443\u0447\u043D\u0443\u044E')+'"></div>';
    if(ts._password_loaded&&!ts.password){h+='<div style="font-size:11px;color:var(--accent);margin:-6px 0 6px 0;padding-left:4px">\u2713 \u041F\u0430\u0440\u043E\u043B\u044C \u0437\u0430\u0433\u0440\u0443\u0436\u0435\u043D \u0438\u0437 <code style="font-size:10px;background:var(--surface2);padding:1px 4px;border-radius:3px">accs.db</code></div>';}
    h+='</div>';
    // --- EXTERNAL MODE ---
    h+='<div id="ts-external" style="'+(mode==='external'?'':'display:none')+'">';
    h+='<div style="font-size:13px;font-weight:600;color:var(--text);margin-bottom:10px">\uD83D\uDD17 \u041F\u043E\u0434\u043A\u043B\u044E\u0447\u0435\u043D\u0438\u0435</div>';
    h+='<div class="form-row"><label>URL<span class="hint" data-tip="\u0410\u0434\u0440\u0435\u0441 \u0443\u0434\u0430\u043B\u0451\u043D\u043D\u043E\u0433\u043E TorrServer">?</span></label><input class="input-sm input-wide" id="ts-url" value="'+esc(ts.url||'')+'" placeholder="http://192.168.1.100:9080"></div>';
    h+='<div class="form-row"><label>Login</label><input class="input-sm input-wide" id="ts-ext-login" value="'+esc(ts.login||'')+'" placeholder="ts"></div>';
    h+='<div class="form-row"><label>Password</label><input class="input-sm input-wide" id="ts-ext-password" type="password" value="'+esc(ts.password||'')+'" placeholder=""></div>';
    h+='<div class="form-row"><label>\u0422\u0435\u0441\u0442</label><button type="button" class="btn btn-sm" id="ts-test-btn" onclick="testTorrServer()">\u041F\u0440\u043E\u0432\u0435\u0440\u0438\u0442\u044C</button> <span id="ts-test-result" style="margin-left:8px;font-size:13px"></span></div>';
    h+='</div>';
    return h;
  },
  sync:function(s){
    var h='';
    h+='<div class="form-row"><label>Enable<span class="hint" data-tip="\u0412\u043A\u043B\u044E\u0447\u0438\u0442\u044C \u0441\u0438\u043D\u0445\u0440\u043E\u043D\u0438\u0437\u0430\u0446\u0438\u044E \u043A\u043E\u043D\u0444\u0438\u0433\u043E\u0432">?</span></label><label class="toggle"><input type="checkbox" id="sync-enable" '+(s.enable?'checked':'')+'><span class="slider"></span></label></div>';
    h+='<div class="form-row"><label>\u0422\u0438\u043F<span class="hint" data-tip="master \u2014 \u0440\u0430\u0437\u0434\u0430\u0451\u0442 \u043A\u043E\u043D\u0444\u0438\u0433\nslave \u2014 \u043F\u043E\u043B\u0443\u0447\u0430\u0435\u0442 \u043A\u043E\u043D\u0444\u0438\u0433">?</span></label><select class="input-sm" id="sync-type"><option value="slave"'+(s.type==='slave'?' selected':'')+'>slave</option><option value="master"'+(s.type==='master'?' selected':'')+'>master</option></select></div>';
    h+='<div class="form-row"><label>API Host<span class="hint" data-tip="\u0410\u0434\u0440\u0435\u0441 master-\u0441\u0435\u0440\u0432\u0435\u0440\u0430">?</span></label><input class="input-sm input-wide" id="sync-api_host" value="'+esc(s.api_host||'')+'" placeholder="http://master:9118"></div>';
    h+='<div class="form-row"><label>API Password<span class="hint" data-tip="\u041F\u0430\u0440\u043E\u043B\u044C \u0434\u043B\u044F \u0441\u0438\u043D\u0445\u0440\u043E\u043D\u0438\u0437\u0430\u0446\u0438\u0438">?</span></label><input class="input-sm input-wide" id="sync-api_passwd" type="password" value="'+esc(s.api_passwd||'')+'"></div>';
    h+='<div class="form-row"><label>\u041F\u043E\u043B\u043D\u0430\u044F \u0441\u0438\u043D\u0445\u0440\u043E\u043D\u0438\u0437\u0430\u0446\u0438\u044F<span class="hint" data-tip="true \u2014 \u0432\u0435\u0441\u044C current.conf\nfalse \u2014 \u0442\u043E\u043B\u044C\u043A\u043E accsdb.users">?</span></label><label class="toggle"><input type="checkbox" id="sync-sync_full" '+(s.sync_full?'checked':'')+'><span class="slider"></span></label></div>';
    return h;
  },
  transcoding:function(t){
    var h='';
    h+='<div class="form-row"><label>Enable<span class="hint" data-tip="FFmpeg \u0442\u0440\u0430\u043D\u0441\u043A\u043E\u0434\u0438\u0440\u043E\u0432\u0430\u043D\u0438\u0435 \u0432\u0438\u0434\u0435\u043E \u0432 HLS">?</span></label><label class="toggle"><input type="checkbox" id="tc-enable" '+(t.enable?'checked':'')+'><span class="slider"></span></label></div>';
    h+='<div class="form-row"><label>FFmpeg<span class="hint" data-tip="\u041F\u0443\u0442\u044C \u043A ffmpeg. \u041F\u043E \u0443\u043C\u043E\u043B\u0447\u0430\u043D\u0438\u044E: ffmpeg">?</span></label><input class="input-sm input-wide" id="tc-ffmpeg" value="'+esc(t.ffmpeg||'ffmpeg')+'" placeholder="ffmpeg"></div>';
    h+='<div class="form-row"><label>\u041C\u0430\u043A\u0441. \u0437\u0430\u0434\u0430\u0447<span class="hint" data-tip="\u041C\u0430\u043A\u0441. \u043E\u0434\u043D\u043E\u0432\u0440\u0435\u043C\u0435\u043D\u043D\u044B\u0445 FFmpeg \u043F\u0440\u043E\u0446\u0435\u0441\u0441\u043E\u0432.\n\u0420\u0435\u043A\u043E\u043C\u0435\u043D\u0434\u0443\u0435\u0442\u0441\u044F 2-5.">?</span></label><input class="input-sm" id="tc-maxConcurrentJobs" type="number" min="1" max="50" value="'+(t.maxConcurrentJobs||5)+'"></div>';
    h+='<div class="form-row"><label>Temp Root<span class="hint" data-tip="\u041F\u0430\u043F\u043A\u0430 \u0434\u043B\u044F \u0432\u0440\u0435\u043C\u0435\u043D\u043D\u044B\u0445 \u0444\u0430\u0439\u043B\u043E\u0432 \u0441\u0435\u0433\u043C\u0435\u043D\u0442\u043E\u0432">?</span></label><input class="input-sm input-wide" id="tc-tempRoot" value="'+esc(t.tempRoot||'')+'" placeholder="cache/transcoding"></div>';
    h+='<div class="form-row"><label>Idle Timeout (\u0441\u0435\u043A)<span class="hint" data-tip="\u0422\u0430\u0439\u043C\u0430\u0443\u0442 \u043F\u0440\u043E\u0441\u0442\u043E\u044F VOD \u0437\u0430\u0434\u0430\u0447\u0438.\n-1 = \u043E\u0442\u043A\u043B\u044E\u0447\u0438\u0442\u044C. \u041F\u043E \u0443\u043C\u043E\u043B\u0447\u0430\u043D\u0438\u044E: 180">?</span></label><input class="input-sm" id="tc-idleTimeoutSec" type="number" value="'+(t.idleTimeoutSec||0)+'"></div>';
    h+='<div class="form-row"><label>Idle Timeout Live (\u0441\u0435\u043A)<span class="hint" data-tip="\u0422\u0430\u0439\u043C\u0430\u0443\u0442 Live \u0437\u0430\u0434\u0430\u0447\u0438.\n-1 = \u043E\u0442\u043A\u043B\u044E\u0447\u0438\u0442\u044C. \u041F\u043E \u0443\u043C\u043E\u043B\u0447\u0430\u043D\u0438\u044E: 20">?</span></label><input class="input-sm" id="tc-idleTimeoutSec_live" type="number" value="'+(t.idleTimeoutSec_live||0)+'"></div>';
    h+='<div class="form-row"><label>\u0421\u0443\u0431\u0442\u0438\u0442\u0440\u044B<span class="hint" data-tip="\u0410\u0432\u0442\u043E\u043C\u0430\u0442\u0438\u0447\u0435\u0441\u043A\u0438 \u0438\u0437\u0432\u043B\u0435\u043A\u0430\u0442\u044C \u0441\u0443\u0431\u0442\u0438\u0442\u0440\u044B \u0438\u0437 \u0432\u0438\u0434\u0435\u043E">?</span></label><label class="toggle"><input type="checkbox" id="tc-defaultSubtitles" '+(t.defaultSubtitles?'checked':'')+'><span class="slider"></span></label></div>';
    return h;
  },
  tmdb_proxy:function(t){
    var mode=t.mode||'self';
    var host=t.host||'tmdb.alcopa.cc';
    var h='';
    // Mode selection
    h+='<div class="form-row" style="margin-bottom:16px"><label style="font-size:13px;color:var(--text);font-weight:600">\u0420\u0435\u0436\u0438\u043C \u043F\u0440\u043E\u043A\u0441\u0438\u0440\u043E\u0432\u0430\u043D\u0438\u044F<span class="hint" data-tip="\u041A\u0430\u043A Lampa \u043A\u043B\u0438\u0435\u043D\u0442\u044B \u043E\u0431\u0440\u0430\u0449\u0430\u044E\u0442\u0441\u044F \u043A TMDB API">?</span></label></div>';
    var modes=[
      {id:'self',icon:'\uD83D\uDDA5',label:'\u0427\u0435\u0440\u0435\u0437 \u044D\u0442\u043E\u0442 \u0441\u0435\u0440\u0432\u0435\u0440',desc:'Lampa \u2192 /tmdb/* \u2192 \u043A\u0435\u0448 + upstream \u0446\u0435\u043F\u043E\u0447\u043A\u0430'},
      {id:'alcopa',icon:'\uD83C\uDF10',label:'\u0427\u0435\u0440\u0435\u0437 \u0432\u044B\u0434\u0435\u043B\u0435\u043D\u043D\u044B\u0439 \u0445\u043E\u0441\u0442',desc:'Lampa \u2192 tmdb.alcopa.cc \u2192 TMDB API'},
      {id:'disabled',icon:'\u26D4',label:'\u041D\u0430\u043F\u0440\u044F\u043C\u0443\u044E',desc:'Lampa \u2192 api.themoviedb.org \u043D\u0430\u043F\u0440\u044F\u043C\u0443\u044E'}
    ];
    h+='<div class="tmdb-modes">';
    modes.forEach(function(m){
      var sel=mode===m.id;
      h+='<label class="tmdb-mode-card'+(sel?' selected':'')+'" onclick="selectTmdbMode(\''+m.id+'\')">';
      h+='<input type="radio" name="tmdb-mode" value="'+m.id+'" '+(sel?'checked':'')+' style="display:none">';
      h+='<div class="tmdb-mode-icon">'+m.icon+'</div>';
      h+='<div class="tmdb-mode-info"><div class="tmdb-mode-label">'+m.label+'</div><div class="tmdb-mode-desc">'+m.desc+'</div></div>';
      h+='<div class="tmdb-mode-check">'+(sel?'\u2713':'')+'</div>';
      h+='</label>';
    });
    h+='</div>';
    h+='<div class="form-row" id="tmdb-host-row" style="margin-top:16px'+(mode!=='alcopa'?';display:none':'')+'"><label>\u0425\u043E\u0441\u0442 \u043F\u0440\u043E\u043A\u0441\u0438</label><input class="input-sm input-wide" id="tmdb-host" value="'+esc(host)+'" placeholder="tmdb.alcopa.cc"></div>';
    // Upstream settings
    h+='<div style="margin-top:20px;padding-top:16px;border-top:1px solid rgba(255,255,255,0.06)">';
    h+='<div style="font-size:13px;color:var(--text);font-weight:600;margin-bottom:12px">Upstream \u043D\u0430\u0441\u0442\u0440\u043E\u0439\u043A\u0438</div>';
    h+='<div class="form-row"><label>API Key<span class="hint" data-tip="TMDB API \u043A\u043B\u044E\u0447. \u0415\u0441\u043B\u0438 \u043A\u043B\u0438\u0435\u043D\u0442 \u043D\u0435 \u043F\u0435\u0440\u0435\u0434\u0430\u0451\u0442 \u0441\u0432\u043E\u0439, \u0441\u0435\u0440\u0432\u0435\u0440 \u043F\u043E\u0434\u0441\u0442\u0430\u0432\u0438\u0442 \u044D\u0442\u043E\u0442">?</span></label><input class="input-sm input-wide" id="tmdb-api-key" type="password" value="'+esc(t.api_key||'')+'" placeholder="\u041E\u043F\u0446\u0438\u043E\u043D\u0430\u043B\u044C\u043D\u043E"></div>';
    h+='<div class="form-row"><label>API Host<span class="hint" data-tip="\u041F\u0435\u0440\u0432\u044B\u0439 upstream \u0434\u043B\u044F API \u0437\u0430\u043F\u0440\u043E\u0441\u043E\u0432. \u0415\u0441\u043B\u0438 \u0443\u043F\u0430\u0434\u0451\u0442 \u2014 \u0430\u0432\u0442\u043E\u043C\u0430\u0442\u0438\u0447\u0435\u0441\u043A\u0438 api.themoviedb.org">?</span></label><input class="input-sm input-wide" id="tmdb-api-host" value="'+esc(t.api_host||'')+'" placeholder="apitmdb.cub.red"></div>';
    h+='<div class="form-row"><label>IMG Host<span class="hint" data-tip="\u041F\u0435\u0440\u0432\u044B\u0439 upstream \u0434\u043B\u044F \u043A\u0430\u0440\u0442\u0438\u043D\u043E\u043A. Fallback: image.tmdb.org">?</span></label><input class="input-sm input-wide" id="tmdb-img-host" value="'+esc(t.img_host||'')+'" placeholder="tmdb.cub.watch"></div>';
    h+='</div>';
    // Cache settings
    h+='<div style="margin-top:20px;padding-top:16px;border-top:1px solid rgba(255,255,255,0.06)">';
    h+='<div style="font-size:13px;color:var(--text);font-weight:600;margin-bottom:12px">\u041A\u0435\u0448 \u0438 \u0442\u0430\u0439\u043C\u0430\u0443\u0442\u044B</div>';
    h+='<div class="form-row"><label>Cache TTL (мин)<span class="hint" data-tip="Базовый TTL кеша для API ответов. Поиск = TTL/4, конфиг = TTL*12">?</span></label><input class="input-sm input-wide" id="tmdb-cache-ttl" type="number" value="'+(t.cache_ttl_min||120)+'" min="1" placeholder="120"></div>';
    h+='<div class="form-row"><label>Max Items<span class="hint" data-tip="Максимальное кол-во записей в кеше. При переполнении удаляются старые 10%">?</span></label><input class="input-sm input-wide" id="tmdb-cache-max" type="number" value="'+(t.cache_max_items||50000)+'" min="100" placeholder="50000"></div>';
    h+='<div class="form-row"><label>Upstream Timeout (сек)<span class="hint" data-tip="Таймаут запроса к upstream серверу. Healthcheck использует отдельный (12с)">?</span></label><input class="input-sm input-wide" id="tmdb-timeout" type="number" value="'+(t.upstream_timeout_sec||8)+'" min="2" max="30" placeholder="8"></div>';
    h+='</div>';
    // Live stats widget
    h+='<div style="margin-top:20px;padding-top:16px;border-top:1px solid rgba(255,255,255,0.06)">';
    h+='<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px"><div style="font-size:13px;color:var(--text);font-weight:600">\u0421\u0442\u0430\u0442\u0438\u0441\u0442\u0438\u043A\u0430 (live)</div><button class="btn btn-sm btn-primary" onclick="loadTmdbStats()" style="font-size:11px">\u041E\u0431\u043D\u043E\u0432\u0438\u0442\u044C</button></div>';
    h+='<div id="tmdb-stats-box" style="font-size:12px;color:var(--text-muted)">\u0417\u0430\u0433\u0440\u0443\u0437\u043A\u0430...</div>';
    h+='</div>';
    return h;
  }
};
window.selectTmdbMode=function(mode){
  document.querySelectorAll('.tmdb-mode-card').forEach(function(c){c.classList.remove('selected');c.querySelector('.tmdb-mode-check').textContent=''});
  var radio=document.querySelector('input[name="tmdb-mode"][value="'+mode+'"]');
  if(radio){radio.checked=true;radio.closest('.tmdb-mode-card').classList.add('selected');radio.closest('.tmdb-mode-card').querySelector('.tmdb-mode-check').textContent='\u2713';}
  var hostRow=document.getElementById('tmdb-host-row');
  if(hostRow)hostRow.style.display=mode==='alcopa'?'':'none';
};
window.openPluginSettings=function(name){
  var m=pluginMeta[name]||{icon:'\uD83E\uDDE9',label:name};
  var dataMap={sisi:pluginsData.sisi||{},torrserver:pluginsData.torrserver||{},sync:pluginsData.sync||{},transcoding:pluginsData.transcoding||{},tmdb_proxy:pluginsData.tmdb_proxy||{}};
  var renderer=pgSettingsRenderers[name];
  if(!renderer){toast('\u041D\u0435\u0442 \u043D\u0430\u0441\u0442\u0440\u043E\u0435\u043A \u0434\u043B\u044F '+m.label);return;}
  var h='<div class="pg-settings-title"><span class="pg-icon">'+m.icon+'</span> \u041D\u0430\u0441\u0442\u0440\u043E\u0439\u043A\u0438: '+m.label+'</div>';
  h+='<div class="pg-settings-section">'+renderer(dataMap[name])+'</div>';
  h+='<div style="margin-top:16px"><button class="btn btn-primary" onclick="savePlugins();closePluginSettings()">\u0421\u043E\u0445\u0440\u0430\u043D\u0438\u0442\u044C</button></div>';
  document.getElementById('pg-settings-content').innerHTML=h;
  document.getElementById('pg-settings-overlay').classList.add('open');
  document.getElementById('pg-settings-panel').classList.add('open');
  if(name==='tmdb_proxy')setTimeout(loadTmdbStats,100);
};
window.closePluginSettings=function(){
  document.getElementById('pg-settings-overlay').classList.remove('open');
  document.getElementById('pg-settings-panel').classList.remove('open');
};
// --- TMDB live stats ---
window.loadTmdbStats=function(){
  var box=document.getElementById('tmdb-stats-box');if(!box)return;
  box.innerHTML='<span style="color:var(--text-muted)">Загрузка...</span>';
  api('/api/tmdb-stats').then(function(d){
    var c=d.cache||{};var ups=d.upstreams||[];
    var hr=c.total_reqs>0?((c.hits/(c.hits+c.misses+c.stale_hits))*100).toFixed(1):'—';
    var h='<div style="display:grid;grid-template-columns:1fr 1fr;gap:8px;margin-bottom:10px">';
    h+='<div style="background:var(--surface);border-radius:6px;padding:8px"><div style="color:var(--text-muted);font-size:11px">Hit Rate</div><div style="color:var(--accent);font-size:18px;font-weight:700">'+hr+'%</div></div>';
    h+='<div style="background:var(--surface);border-radius:6px;padding:8px"><div style="color:var(--text-muted);font-size:11px">Записей</div><div style="color:var(--text);font-size:18px;font-weight:700">'+((c.item_count||0).toLocaleString())+'</div></div>';
    h+='<div style="background:var(--surface);border-radius:6px;padding:8px"><div style="color:var(--text-muted);font-size:11px">Hits / Misses</div><div style="color:var(--text);font-size:14px">'+(c.hits||0)+' / '+(c.misses||0)+'</div></div>';
    h+='<div style="background:var(--surface);border-radius:6px;padding:8px"><div style="color:var(--text-muted);font-size:11px">Stale served</div><div style="color:var(--text);font-size:14px">'+(c.stale_hits||0)+'</div></div>';
    h+='</div>';
    if(ups.length){
      h+='<div style="font-size:11px;color:var(--text-muted);margin-bottom:6px">Upstreams</div>';
      ups.forEach(function(u){
        var dot=u.healthy?'🟢':'🔴';
        var lat=u.avg_lat_ms>0?(u.avg_lat_ms+'ms'):'—';
        h+='<div style="display:flex;align-items:center;gap:6px;padding:3px 0;font-size:12px">';
        h+=dot+' <span style="color:var(--text);min-width:70px">'+esc(u.name)+'</span>';
        h+='<span style="color:var(--text-muted)">'+lat+'</span>';
        h+='<span style="color:var(--text-dim);margin-left:auto">'+u.total_reqs+' req / '+u.total_errs+' err</span>';
        h+='</div>';
      });
    }
    box.innerHTML=h;
  }).catch(function(e){box.innerHTML='<span style="color:#e74c3c">Ошибка: '+e+'</span>'});
};
// --- Save plugins ---
window.savePlugins=function(){
  var ip={};pluginNames.forEach(function(n){var el=document.getElementById('plugin-'+n);if(el)ip[n]=el.checked});
  var sisi={};
  var sisiFields=['spider','component','iconame','push_all'];
  sisiFields.forEach(function(k){var el=document.getElementById('sisi-'+k);if(!el)return;sisi[k]=el.type==='checkbox'?el.checked:el.value});
  var hEl=document.getElementById('sisi-history.enable');
  if(hEl)sisi.history={enable:hEl.checked};
  var fcEl=document.getElementById('sisi-forced_checkRchtype');
  if(fcEl)sisi.forced_checkRchtype=fcEl.checked;
  var ts={};
  var tsModeEl=document.querySelector('input[name="ts-mode"]:checked');
  var tsMode=tsModeEl?tsModeEl.value:'internal';
  if(tsMode==='external'){
    var tsUEl=document.getElementById('ts-url');if(tsUEl&&tsUEl.value.trim())ts.url=tsUEl.value.trim();
    var tsLEl=document.getElementById('ts-ext-login');if(tsLEl&&tsLEl.value.trim())ts.login=tsLEl.value.trim();
    var tsPwEl=document.getElementById('ts-ext-password');if(tsPwEl&&tsPwEl.value)ts.password=tsPwEl.value;
  }else{
    ts.url='';
    var pEl=document.getElementById('ts-port');if(pEl)ts.port=parseInt(pEl.value)||9080;
    var tsLEl=document.getElementById('ts-login');if(tsLEl&&tsLEl.value.trim())ts.login=tsLEl.value.trim();
    var tsPwEl=document.getElementById('ts-password');if(tsPwEl&&tsPwEl.value)ts.password=tsPwEl.value;
    // Cache
    var crEl=document.getElementById('ts-cache-ram');if(crEl)ts.cache_size_mb=parseInt(crEl.value)||64;
    var cdEl=document.getElementById('ts-cache-disk');if(cdEl)ts.disk_cache_mb=parseInt(cdEl.value)||1024;
    var prEl=document.getElementById('ts-preload');if(prEl)ts.preload_mb=parseInt(prEl.value)||5;
    // Limits
    var dlEl=document.getElementById('ts-max-dl');if(dlEl)ts.max_download_speed_mb=parseInt(dlEl.value)||0;
    var ulEl=document.getElementById('ts-max-ul');if(ulEl)ts.max_upload_speed_mb=parseInt(ulEl.value)||0;
    var mtEl=document.getElementById('ts-max-torrents');if(mtEl)ts.max_active_torrents=parseInt(mtEl.value)||0;
    var ramOnlyEl=document.getElementById('ts-ram-only');if(ramOnlyEl)ts.ram_cache=ramOnlyEl.checked;
    var dhtEl=document.getElementById('ts-dht');if(dhtEl)ts.disable_dht=dhtEl.checked;
    var upEl=document.getElementById('ts-upload');if(upEl)ts.disable_upload=upEl.checked;
    // Cleanup
    var clEl=document.getElementById('ts-cleanup');if(clEl)ts.cache_cleanup_enable=clEl.checked;
    var clDEl=document.getElementById('ts-cleanup-days');if(clDEl)ts.cache_cleanup_days=parseInt(clDEl.value)||7;
    var clGEl=document.getElementById('ts-cleanup-maxgb');if(clGEl)ts.cache_cleanup_max_gb=parseInt(clGEl.value)||50;
  }
  var syncData={};
  var seEl=document.getElementById('sync-enable');if(seEl)syncData.enable=seEl.checked;
  var stEl=document.getElementById('sync-type');if(stEl)syncData.type=stEl.value;
  var shEl=document.getElementById('sync-api_host');if(shEl)syncData.api_host=shEl.value;
  var spEl=document.getElementById('sync-api_passwd');if(spEl)syncData.api_passwd=spEl.value;
  var sfEl=document.getElementById('sync-sync_full');if(sfEl)syncData.sync_full=sfEl.checked;
  var tcData={};
  var tceEl=document.getElementById('tc-enable');if(tceEl)tcData.enable=tceEl.checked;
  var tcfEl=document.getElementById('tc-ffmpeg');if(tcfEl)tcData.ffmpeg=tcfEl.value;
  var tcmEl=document.getElementById('tc-maxConcurrentJobs');if(tcmEl)tcData.maxConcurrentJobs=parseInt(tcmEl.value)||5;
  var tctEl=document.getElementById('tc-tempRoot');if(tctEl&&tctEl.value)tcData.tempRoot=tctEl.value;
  var tciEl=document.getElementById('tc-idleTimeoutSec');if(tciEl)tcData.idleTimeoutSec=parseInt(tciEl.value)||0;
  var tcilEl=document.getElementById('tc-idleTimeoutSec_live');if(tcilEl)tcData.idleTimeoutSec_live=parseInt(tcilEl.value)||0;
  var tcdsEl=document.getElementById('tc-defaultSubtitles');if(tcdsEl)tcData.defaultSubtitles=tcdsEl.checked;
  var tmdbData={};
  var tmdbModeEl=document.querySelector('input[name="tmdb-mode"]:checked');if(tmdbModeEl)tmdbData.mode=tmdbModeEl.value;
  var tmdbHostEl=document.getElementById('tmdb-host');if(tmdbHostEl&&tmdbHostEl.value.trim())tmdbData.host=tmdbHostEl.value.trim();
  var tmdbAKEl=document.getElementById('tmdb-api-key');if(tmdbAKEl)tmdbData.api_key=tmdbAKEl.value.trim();
  var tmdbAHEl=document.getElementById('tmdb-api-host');if(tmdbAHEl)tmdbData.api_host=tmdbAHEl.value.trim();
  var tmdbIHEl=document.getElementById('tmdb-img-host');if(tmdbIHEl)tmdbData.img_host=tmdbIHEl.value.trim();
  var tmdbCTEl=document.getElementById('tmdb-cache-ttl');if(tmdbCTEl)tmdbData.cache_ttl_min=parseInt(tmdbCTEl.value)||120;
  var tmdbCMEl=document.getElementById('tmdb-cache-max');if(tmdbCMEl)tmdbData.cache_max_items=parseInt(tmdbCMEl.value)||50000;
  var tmdbTOEl=document.getElementById('tmdb-timeout');if(tmdbTOEl)tmdbData.upstream_timeout_sec=parseInt(tmdbTOEl.value)||8;
  post('/api/plugins',{initPlugins:ip,sisi:sisi,torrserver:ts,sync:syncData,transcoding:tcData,tmdb_proxy:tmdbData}).then(function(r){
    if(r.ok){toast('\u041F\u043B\u0430\u0433\u0438\u043D\u044B \u0441\u043E\u0445\u0440\u0430\u043D\u0435\u043D\u044B');if(r.restart_needed)showRestart();loadPlugins()}
    else toast(r.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  });
};

window.tsModeSwitch=function(mode){
  var intEl=document.getElementById('ts-internal');
  var extEl=document.getElementById('ts-external');
  if(!intEl||!extEl)return;
  intEl.style.display=mode==='internal'?'':'none';
  extEl.style.display=mode==='external'?'':'none';
  // Update mode cards
  document.querySelectorAll('input[name="ts-mode"]').forEach(function(r){
    var card=r.closest('.tmdb-mode-card');
    if(r.value===mode){r.checked=true;card.classList.add('selected');card.querySelector('.tmdb-mode-check').textContent='\u2713';}
    else{r.checked=false;card.classList.remove('selected');card.querySelector('.tmdb-mode-check').textContent='';}
  });
  // If switching to internal, clear external URL so save handler knows the mode
  if(mode==='internal'){var u=document.getElementById('ts-url');if(u)u.value='';}
};
window.testTorrServer=function(){
  var btn=document.getElementById('ts-test-btn');
  var res=document.getElementById('ts-test-result');
  if(!btn||!res)return;
  var urlEl=document.getElementById('ts-url');
  var loginEl=document.getElementById('ts-ext-login')||document.getElementById('ts-login');
  var pwEl=document.getElementById('ts-ext-password')||document.getElementById('ts-password');
  var portEl=document.getElementById('ts-port');
  var testUrl=(urlEl&&urlEl.value.trim())?urlEl.value.trim():'';
  if(!testUrl&&portEl){testUrl='http://127.0.0.1:'+(parseInt(portEl.value)||9080)}
  if(!testUrl){res.textContent='\u0423\u043A\u0430\u0436\u0438\u0442\u0435 URL \u0438\u043B\u0438 \u043F\u043E\u0440\u0442';res.style.color='#e74c3c';return}
  btn.disabled=true;res.textContent='\u041F\u0440\u043E\u0432\u0435\u0440\u043A\u0430...';res.style.color='#888';
  post('/api/torrserver/test',{url:testUrl,login:loginEl?loginEl.value.trim():'',password:pwEl?pwEl.value:''}).then(function(r){
    btn.disabled=false;
    if(r.ok){res.innerHTML='\u2705 '+esc(r.message);res.style.color='#27ae60'}
    else{
      var msg='\u274C '+(r.message||r.error||'\u041E\u0448\u0438\u0431\u043A\u0430');
      if(r.process){
        if(r.process.managed){msg+=r.process.running?'<br><span style="font-size:11px;color:#e0a030">\u26A0 Процесс запущен (PID '+r.process.pid+'), но не отвечает</span>':'<br><span style="font-size:11px;color:#e74c3c">\u274C Процесс не запущен</span>'}
        else{msg+='<br><span style="font-size:11px;color:var(--text-dim)">Бинарник не найден или используется внешний URL</span>'}
        if(r.process.recent_log){msg+='<details style="margin-top:4px"><summary style="cursor:pointer;font-size:11px;color:var(--text-dim)">Лог TorrServer</summary><pre style="font-size:10px;max-height:150px;overflow:auto;background:var(--surface);padding:6px;border-radius:4px;margin-top:4px;white-space:pre-wrap">'+esc(r.process.recent_log)+'</pre></details>'}
      }
      res.innerHTML=msg;res.style.color='#e74c3c';
    }
  }).catch(function(e){btn.disabled=false;res.innerHTML='\u274C '+esc(String(e));res.style.color='#e74c3c'});
};

// --- Custom JS Plugins ---
window.loadCustomPlugins=function(){
  api('/api/customplugins').then(function(d){
    var list=d.plugins||[];
    var el=document.getElementById('cp-grid');
    if(!list.length){el.innerHTML='<div style="color:var(--text-dim);font-size:13px;padding:8px">\u041D\u0435\u0442 \u0437\u0430\u0433\u0440\u0443\u0436\u0435\u043D\u043D\u044B\u0445 \u043F\u043B\u0430\u0433\u0438\u043D\u043E\u0432</div>';return;}
    var h='';
    list.forEach(function(p){
      var purl=location.protocol+'//'+location.hostname+(location.port?':'+location.port:'')+'/'+p.name+'.js';
      h+='<div class="pg-card'+(p.enabled?' active':' inactive')+'" style="flex-direction:column;align-items:stretch;gap:8px;padding-right:36px">';
      // header row
      h+='<div style="display:flex;align-items:center;gap:10px">';
      h+='<div class="pg-icon">\uD83E\uDDE9</div>';
      h+='<div class="pg-info" style="flex:1;min-width:0"><div class="pg-name" style="color:var(--accent)">'+esc(p.display_name||p.name)+'</div>'+(p.display_name?'<div style="font-size:11px;color:var(--text-dim)">'+esc(p.name)+'.js</div>':'')+'</div>';
      h+='<label class="toggle" style="flex-shrink:0"><input type="checkbox" '+(p.enabled?'checked':'')+' onchange="toggleCustomPlugin(\''+esc(p.name)+'\',this.checked)"><span class="slider"></span></label>';
      h+='</div>';
      // URL
      h+='<div class="pg-card-url" onclick="copyText(\''+esc(purl)+'\');toast(\'URL \u0441\u043A\u043E\u043F\u0438\u0440\u043E\u0432\u0430\u043D\')" title="\u041D\u0430\u0436\u043C\u0438\u0442\u0435 \u0447\u0442\u043E\u0431\u044B \u0441\u043A\u043E\u043F\u0438\u0440\u043E\u0432\u0430\u0442\u044C">'+esc(purl)+'</div>';
      // toggles row
      h+='<div class="pg-card-toggles">';
      h+='<label><input type="checkbox" '+(p.autoload?'checked':'')+' onchange="toggleCustomAutoload(\''+esc(p.name)+'\',this.checked)" style="accent-color:var(--accent)"> \u0410\u0432\u0442\u043E\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430</label>';
      h+='<label><input type="checkbox" id="cp-pub-'+esc(p.name)+'" '+(p["public"]?'checked':'')+' onchange="savePluginMeta(\''+esc(p.name)+'\')" style="accent-color:var(--accent)"> \u0412 \u043C\u0430\u0433\u0430\u0437\u0438\u043D</label>';
      h+='</div>';
      // meta
      h+='<div class="pg-card-meta">';
      h+='<input class="input-sm" id="cp-author-'+esc(p.name)+'" placeholder="\u0410\u0432\u0442\u043E\u0440" value="'+esc(p.author||'')+'" style="width:120px" onchange="savePluginMeta(\''+esc(p.name)+'\')">';
      h+='<input class="input-sm" id="cp-descr-'+esc(p.name)+'" placeholder="\u041E\u043F\u0438\u0441\u0430\u043D\u0438\u0435" value="'+esc(p.descr||'')+'" style="flex:1;min-width:150px" onchange="savePluginMeta(\''+esc(p.name)+'\')">';
      h+='</div>';
      // image
      h+='<div class="pg-card-img">';
      if(p.image)h+='<img src="'+esc(p.image)+'">';
      h+='<label class="btn btn-sm" style="cursor:pointer;font-size:11px;padding:3px 8px"><input type="file" accept="image/*" style="display:none" onchange="uploadPluginImage(\''+esc(p.name)+'\',this)">\uD83D\uDDBC</label>';
      if(p.image)h+='<button class="btn btn-danger btn-sm" style="font-size:11px;padding:3px 6px" onclick="deletePluginImage(\''+esc(p.name)+'\')">\u2715</button>';
      h+='</div>';
      // delete button
      h+='<button class="pg-card-delete" onclick="deleteCustomPlugin(\''+esc(p.name)+'\')" title="\u0423\u0434\u0430\u043B\u0438\u0442\u044C">\u2715</button>';
      h+='</div>';
    });
    el.innerHTML=h;
  });
};
window.savePluginMeta=function(name){
  var pub=document.getElementById('cp-pub-'+name);
  var author=document.getElementById('cp-author-'+name);
  var descr=document.getElementById('cp-descr-'+name);
  post('/api/customplugins',{action:'update_meta',name:name,public:pub?pub.checked:false,author:author?author.value:'',descr:descr?descr.value:''}).then(function(d){
    if(d.ok)toast('\u041C\u0435\u0442\u0430\u0434\u0430\u043D\u043D\u044B\u0435 \u0441\u043E\u0445\u0440\u0430\u043D\u0435\u043D\u044B');
    else toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  });
};
window.toggleCustomAutoload=function(name,on){
  post('/api/customplugins',{action:'toggle_autoload',name:name,autoload:on}).then(function(d){
    if(d.ok)toast(name+(on?' \u0430\u0432\u0442\u043E\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430 \u0432\u043A\u043B':' \u0430\u0432\u0442\u043E\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430 \u0432\u044B\u043A\u043B'));
    else toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  });
};
window.uploadPluginImage=function(name,input){
  if(!input.files.length)return;
  var fd=new FormData();
  fd.append('action','upload_image');
  fd.append('name',name);
  fd.append('image',input.files[0]);
  fetch(basePath+'/api/customplugins',{method:'POST',body:fd,credentials:'include'}).then(function(r){return r.json()}).then(function(d){
    if(d.ok){toast('\u041A\u0430\u0440\u0442\u0438\u043D\u043A\u0430 \u0437\u0430\u0433\u0440\u0443\u0436\u0435\u043D\u0430');loadCustomPlugins();}
    else toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  }).catch(function(e){toast('\u041E\u0448\u0438\u0431\u043A\u0430: '+e.message,'error')});
};
window.deletePluginImage=function(name){
  post('/api/customplugins',{action:'delete_image',name:name}).then(function(d){
    if(d.ok){toast('\u041A\u0430\u0440\u0442\u0438\u043D\u043A\u0430 \u0443\u0434\u0430\u043B\u0435\u043D\u0430');loadCustomPlugins();}
    else toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  });
};
window.uploadCustomPlugin=function(){
  var name=document.getElementById('cp-name').value.trim();
  var fileEl=document.getElementById('cp-file');
  if(!name){toast('\u0412\u0432\u0435\u0434\u0438\u0442\u0435 \u0438\u043C\u044F \u043F\u043B\u0430\u0433\u0438\u043D\u0430','error');return;}
  if(!fileEl.files.length){toast('\u0412\u044B\u0431\u0435\u0440\u0438\u0442\u0435 JS \u0444\u0430\u0439\u043B','error');return;}
  var fd=new FormData();
  fd.append('name',name);
  fd.append('file',fileEl.files[0]);
  fetch(basePath+'/api/customplugins',{method:'POST',body:fd,credentials:'include'}).then(function(r){return r.json()}).then(function(d){
    if(d.ok){var dn=d.display_name||d.name||name;toast('\u041F\u043B\u0430\u0433\u0438\u043D '+dn+' \u0437\u0430\u0433\u0440\u0443\u0436\u0435\u043D'+(d.display_name?' ('+d.name+'.js)':''));document.getElementById('cp-name').value='';fileEl.value='';loadCustomPlugins();}
    else toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  }).catch(function(e){toast('\u041E\u0448\u0438\u0431\u043A\u0430: '+e.message,'error')});
};
window.deleteCustomPlugin=function(name){
  if(!confirm('\u0423\u0434\u0430\u043B\u0438\u0442\u044C \u043F\u043B\u0430\u0433\u0438\u043D '+name+'?'))return;
  post('/api/customplugins',{action:'delete',name:name}).then(function(d){
    if(d.ok){toast('\u041F\u043B\u0430\u0433\u0438\u043D \u0443\u0434\u0430\u043B\u0451\u043D');loadCustomPlugins();}
    else toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  });
};
window.toggleCustomPlugin=function(name,on){
  post('/api/customplugins',{action:'toggle',name:name,enabled:on}).then(function(d){
    if(d.ok){toast(name+' '+(on?'\u0432\u043A\u043B\u044E\u0447\u0451\u043D':'\u0432\u044B\u043A\u043B\u044E\u0447\u0435\u043D'));loadCustomPlugins();}
    else toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  });
};

// --- TG Settings ---
window.loadTGSettings=function(){
  api('/api/tgsettings').then(function(d){
    var h='<div class="form-row"><label>Enable</label><label class="toggle"><input type="checkbox" id="tg-enable" '+(d.enable?'checked':'')+'><span class="slider"></span></label></div>';
    h+='<div class="form-row"><label>Bot Token</label><input class="input-sm input-wide" id="tg-bot_token" type="password" value="'+esc(d.bot_token||'')+'"></div>';
    h+='<div class="form-row"><label>Bot Name</label><input class="input-sm" id="tg-bot_name" value="'+esc(d.bot_name||'')+'"></div>';
    h+='<div class="form-row"><label>Admin ID</label><input class="input-sm" value="'+esc(d.admin_id||'')+'" disabled></div>';
    h+='<div class="form-row"><label>Макс. устройств</label><input class="input-sm" id="tg-max_devices" type="number" min="-1" max="50" value="'+(d.max_devices_per_user||d.max_devices||3)+'"><span style="font-size:11px;color:var(--text-dim);margin-left:8px">-1 = безлимит</span></div>';
    h+='<div class="form-row"><label>Авто-одобрение</label><label class="toggle"><input type="checkbox" id="tg-auto_approve" '+(d.auto_approve?'checked':'')+'><span class="slider"></span></label></div>';
    h+='<div class="form-row"><label>Срок (дней)</label><select class="input-sm" id="tg-auto_approve_days">';
    [1,7,30,90,365].forEach(function(v){h+='<option value="'+v+'"'+(v==(d.auto_approve_days||30)?' selected':'')+'>'+v+'</option>'});
    h+='</select></div>';
    h+='<div class="form-row"><label>Admin Path</label><span style="color:var(--accent);font-family:monospace">/'+esc(d.admin_path||'')+'</span><button class="btn btn-warning btn-sm" style="margin-left:12px" onclick="regenPath()">Перегенерировать</button></div>';
    h+='<div style="margin-top:16px"><button class="btn btn-primary" onclick="saveTGSettings()">Сохранить</button></div>';
    document.getElementById('tg-form').innerHTML=h;
    // Required chats (subscriptions)
    renderRequiredChats(d.required_chats||[],d.check_interval_min||60);
    // Kit settings
    var kit=d.kit||{};
    var kh='<div class="form-row"><label>Enable</label><label class="toggle"><input type="checkbox" id="kit-enable" '+(kit.enable?'checked':'')+'><span class="slider"></span></label></div>';
    kh+='<div class="form-row"><label>Server Host</label><input class="input-sm input-wide" id="kit-serverHost" placeholder="https://your-domain.com" value="'+esc(kit.serverHost||'')+'"></div>';
    kh+='<div class="form-row"><label>Cache TTL (сек)</label><input class="input-sm" id="kit-cacheToSeconds" type="number" min="5" max="3600" value="'+(kit.cacheToSeconds||60)+'"></div>';
    kh+='<div style="margin-top:16px"><button class="btn btn-primary" onclick="saveKitSettings()">Сохранить Kit</button></div>';
    document.getElementById('kit-form').innerHTML=kh;
    // Auth page style
    renderAuthPageForm(d.auth_page||{});
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
  loadAdmins();
  loadPasskeys();
};
window.saveTGSettings=function(){
  var md=parseInt(document.getElementById('tg-max_devices').value);if(isNaN(md))md=3;
  var d={enable:document.getElementById('tg-enable').checked,bot_token:document.getElementById('tg-bot_token').value,bot_name:document.getElementById('tg-bot_name').value,max_devices_per_user:md,auto_approve:document.getElementById('tg-auto_approve').checked,auto_approve_days:parseInt(document.getElementById('tg-auto_approve_days').value)||30};
  // Include required chats
  d.required_chats=getRequiredChatsData();
  var ci=document.getElementById('rc-interval');
  if(ci)d.check_interval_min=parseInt(ci.value)||60;
  post('/api/tgsettings',d).then(function(r){
    if(r.ok){toast('TG настройки сохранены');if(r.restart_needed)showRestart()}
    else toast(r.error||'Ошибка','error');
  });
};
window.saveKitSettings=function(){
  var cs=parseInt(document.getElementById('kit-cacheToSeconds').value)||60;
  var d={kit:{enable:document.getElementById('kit-enable').checked,serverHost:document.getElementById('kit-serverHost').value.replace(/\/+$/,''),cacheToSeconds:cs}};
  post('/api/tgsettings',d).then(function(r){
    if(r.ok){toast('Kit настройки сохранены');if(r.restart_needed)showRestart()}
    else toast(r.error||'Ошибка','error');
  });
};
// --- Required Chats (subscriptions) ---
window._rcData=[];
function renderRequiredChats(chats,interval){
  window._rcData=chats||[];
  var el=document.getElementById('required-chats-form');if(!el)return;
  var h='<div class="form-row"><label>Интервал проверки (мин)</label><select class="input-sm" id="rc-interval">';
  [15,30,60,120,360].forEach(function(v){h+='<option value="'+v+'"'+(v==interval?' selected':'')+'>'+v+'</option>'});
  h+='</select></div>';
  h+='<table style="margin-top:8px"><thead><tr><th>Chat ID</th><th>Название</th><th>Ссылка</th><th></th></tr></thead><tbody id="rc-body"></tbody></table>';
  h+='<div style="margin-top:8px;display:flex;gap:6px;align-items:center">';
  h+='<input class="input-sm" id="rc-new-id" placeholder="Chat ID (напр. -100xxx)" type="number" style="width:160px">';
  h+='<input class="input-sm" id="rc-new-title" placeholder="Название" style="width:140px">';
  h+='<input class="input-sm" id="rc-new-link" placeholder="telegram.me/channel" style="width:160px">';
  h+='<button class="btn btn-primary btn-sm" onclick="addRequiredChat()">+</button>';
  h+='</div>';
  h+='<div style="font-size:11px;color:var(--text-dim);margin-top:6px">Бот должен быть администратором в каждом канале/группе. Chat ID можно узнать через @userinfobot или API.</div>';
  el.innerHTML=h;
  renderRCRows();
}
function renderRCRows(){
  var tb=document.getElementById('rc-body');if(!tb)return;
  if(!window._rcData.length){tb.innerHTML='<tr><td colspan="4" style="color:var(--text-dim);text-align:center">Нет обязательных подписок</td></tr>';return}
  tb.innerHTML=window._rcData.map(function(c,i){
    return '<tr><td><code>'+c.chat_id+'</code></td><td>'+esc(c.title||'')+'</td><td>'+esc(c.link||'')+'</td><td><button class="btn btn-danger btn-sm" onclick="removeRequiredChat('+i+')">✕</button></td></tr>';
  }).join('');
}
window.addRequiredChat=function(){
  var id=parseInt(document.getElementById('rc-new-id').value);
  var title=document.getElementById('rc-new-title').value.trim();
  var link=document.getElementById('rc-new-link').value.trim();
  if(!id){toast('Укажите Chat ID','error');return}
  if(!title)title='Chat '+id;
  window._rcData.push({chat_id:id,title:title,link:link});
  document.getElementById('rc-new-id').value='';
  document.getElementById('rc-new-title').value='';
  document.getElementById('rc-new-link').value='';
  renderRCRows();
};
window.removeRequiredChat=function(i){
  window._rcData.splice(i,1);renderRCRows();
};
function getRequiredChatsData(){return window._rcData||[]}
// --- Auth Page Style (row-based layout + per-block styles) ---
var apDefs={title:'Авторизация',subtitle:'Для доступа к Lampac необходимо подтверждение',bg_color:'#1a1a2e',card_color:'#16213e',accent_color:'#64ffda',button_color:'#0088cc',text_color:'#e0e0e0',logo_url:'',bg_image_url:'',custom_css:''};
var apDefaultRows=[['logo'],['title'],['subtitle'],['code','qr'],['steps'],['button'],['status']];
var apBlockLabels={logo:'Лого',title:'Заголовок',subtitle:'Подзаголовок',code:'Код',steps:'Инструкция',button:'Кнопка TG',qr:'QR-код',status:'Статус'};
var apRows=JSON.parse(JSON.stringify(apDefaultRows));
var apStyles={}; // {blockId: "font-size:20px;padding:8px;..."}
var apNextSpacer=1; // counter for generating unique spacer IDs

window.renderAuthPageForm=function(ap){
  var colors=[
    {key:'bg_color',label:'Фон страницы'},{key:'card_color',label:'Фон карточки'},
    {key:'accent_color',label:'Акцент (код)'},{key:'button_color',label:'Кнопка TG'},{key:'text_color',label:'Текст'}
  ];
  var texts=[
    {key:'title',label:'Заголовок',ph:apDefs.title},{key:'subtitle',label:'Подзаголовок',ph:apDefs.subtitle},
    {key:'logo_url',label:'URL логотипа',ph:'https://...'},{key:'bg_image_url',label:'URL фона',ph:'https://...'}
  ];
  var form=document.getElementById('auth-page-form');
  var h='';
  for(var i=0;i<colors.length;i++){
    var c=colors[i],v=ap[c.key]||apDefs[c.key];
    h+='<div style="display:flex;align-items:center;gap:8px"><input type="color" id="ap-'+c.key+'" value="'+v+'" style="width:36px;height:28px;border:none;background:none;cursor:pointer" onchange="updateAuthPreview()"><label style="font-size:12px;color:var(--text-muted)">'+c.label+'</label></div>';
  }
  for(var i=0;i<texts.length;i++){
    var t=texts[i],v=ap[t.key]||'';
    h+='<div style="grid-column:span 2"><label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">'+t.label+'</label><input class="input-sm" style="width:100%" id="ap-'+t.key+'" value="'+esc(v)+'" placeholder="'+esc(t.ph)+'" oninput="updateAuthPreview()"></div>';
  }
  form.innerHTML=h;
  document.getElementById('auth-page-css').value=ap.custom_css||'';
  document.getElementById('auth-page-css').addEventListener('input',function(){updateAuthPreview()});
  if(ap.block_rows&&Array.isArray(ap.block_rows)&&ap.block_rows.length>0){apRows=JSON.parse(JSON.stringify(ap.block_rows))}
  else{apRows=JSON.parse(JSON.stringify(apDefaultRows))}
  apStyles=(ap.block_styles&&typeof ap.block_styles==='object')?JSON.parse(JSON.stringify(ap.block_styles)):{};
  // Restore labels for spacer/divider blocks and compute next counter
  apNextSpacer=1;
  for(var ri=0;ri<apRows.length;ri++){for(var ci=0;ci<apRows[ri].length;ci++){
    var bid=apRows[ri][ci];
    if(bid.indexOf('spacer')===0){if(!apBlockLabels[bid])apBlockLabels[bid]='\u2195 Отступ';var n=parseInt(bid.replace(/\D/g,''))||0;if(n>=apNextSpacer)apNextSpacer=n+1}
    if(bid.indexOf('divider')===0){if(!apBlockLabels[bid])apBlockLabels[bid]='\u2500 Разделитель';var n=parseInt(bid.replace(/\D/g,''))||0;if(n>=apNextSpacer)apNextSpacer=n+1}
  }}
  updateAuthPreview();
};

window.getAuthPageValues=function(){
  return{
    title:document.getElementById('ap-title').value||apDefs.title,
    subtitle:document.getElementById('ap-subtitle').value||apDefs.subtitle,
    bg_color:document.getElementById('ap-bg_color').value,
    card_color:document.getElementById('ap-card_color').value,
    accent_color:document.getElementById('ap-accent_color').value,
    button_color:document.getElementById('ap-button_color').value,
    text_color:document.getElementById('ap-text_color').value,
    logo_url:document.getElementById('ap-logo_url').value,
    bg_image_url:document.getElementById('ap-bg_image_url').value,
    custom_css:document.getElementById('auth-page-css').value,
    block_rows:apRows,
    block_styles:apStyles
  };
};

window.updateAuthPreview=function(){
  var s=getAuthPageValues();
  var preview=document.getElementById('auth-page-preview');
  var bgImg=s.bg_image_url?';background-image:url('+s.bg_image_url+');background-size:cover;background-position:center':'';
  var logoH=s.logo_url?'<img src="'+esc(s.logo_url)+'" style="max-width:80px;max-height:40px;margin-bottom:8px" onerror="this.style.display=\'none\'">':'';
  var blocks={
    logo:logoH,
    title:'<div style="font-size:16px;font-weight:700;color:#fff;margin-bottom:6px">'+esc(s.title)+'</div>',
    subtitle:'<div style="font-size:11px;color:'+s.text_color+';opacity:0.7;margin-bottom:10px">'+esc(s.subtitle)+'</div>',
    code:'<div style="font-size:28px;font-weight:700;letter-spacing:4px;color:'+s.accent_color+';margin:10px 0;font-family:monospace">ABC123</div>',
    steps:'<div style="text-align:left;font-size:10px;color:'+s.text_color+';opacity:0.8;margin:8px 0;line-height:1.6">1. Откройте бота @bot<br>2. Отправьте код<br>3. Дождитесь одобрения</div>',
    button:'<div style="background:'+s.button_color+';color:#fff;padding:8px 16px;border-radius:8px;font-size:12px;display:inline-block;cursor:default;margin:6px 0">Открыть Telegram</div>',
    qr:'<div style="width:80px;height:80px;background:'+s.card_color+';border:2px dashed '+s.accent_color+';border-radius:8px;display:flex;align-items:center;justify-content:center;margin:4px auto"><span style="font-size:24px;opacity:0.5">QR</span></div><div style="font-size:9px;opacity:0.5;margin-top:2px">Отсканируйте</div>',
    status:'<div style="background:'+s.bg_color+';color:'+s.text_color+';opacity:0.7;padding:6px;border-radius:6px;font-size:10px;margin-top:8px">Ожидание...</div>'
  };
  // Dynamic blocks: spacer_N, divider_N
  for(var ri=0;ri<apRows.length;ri++){for(var ci=0;ci<apRows[ri].length;ci++){var bid=apRows[ri][ci];
    if(bid.indexOf('spacer')===0&&!blocks[bid]){blocks[bid]='<div style="height:20px;border:1px dashed rgba(255,255,255,0.15);border-radius:4px;display:flex;align-items:center;justify-content:center"><span style="font-size:8px;opacity:0.3">&#9776;</span></div>';if(!apBlockLabels[bid])apBlockLabels[bid]='\u2195 Отступ'}
    if(bid.indexOf('divider')===0&&!blocks[bid]){blocks[bid]='<hr style="border:none;border-top:1px solid rgba(255,255,255,0.15);margin:4px 0;width:100%">';if(!apBlockLabels[bid])apBlockLabels[bid]='\u2500 Разделитель'}
  }}
  // Row chips editor
  var cons='<div style="margin-bottom:8px;font-size:11px;color:var(--text-dim)">Перетаскивайте чипы между рядами. \u2716 = скрыть блок. Клик на блок в макете = настроить.</div>';
  cons+='<div id="ap-rows-editor" style="margin-bottom:8px">';
  for(var ri=0;ri<apRows.length;ri++){
    var row=apRows[ri];
    cons+='<div class="ap-row-wrap" data-row="'+ri+'" style="display:flex;gap:4px;align-items:center;margin:3px 0;padding:4px;border:1px solid var(--surface2);border-radius:6px;min-height:30px">';
    for(var ci=0;ci<row.length;ci++){
      var bid=row[ci];
      cons+='<div class="ap-chip" draggable="true" data-row="'+ri+'" data-col="'+ci+'" data-bid="'+bid+'" style="background:var(--surface2);color:var(--text);padding:3px 10px;border-radius:12px;font-size:11px;cursor:grab;white-space:nowrap;user-select:none;display:flex;align-items:center;gap:4px">';
      cons+=(apBlockLabels[bid]||bid)+(apStyles[bid]?' \u2699':'');
      cons+='<span onclick="event.stopPropagation();apRemoveBlock('+ri+','+ci+')" style="cursor:pointer;opacity:0.5;font-size:9px;margin-left:2px" title="Скрыть блок">\u2716</span>';
      cons+='</div>';
    }
    cons+='<span style="margin-left:auto;display:flex;gap:2px">';
    if(ri>0)cons+='<button class="btn btn-sm" style="font-size:9px;padding:1px 4px" onclick="apMoveRow('+ri+',-1)" title="Вверх">\u2191</button>';
    if(ri<apRows.length-1)cons+='<button class="btn btn-sm" style="font-size:9px;padding:1px 4px" onclick="apMoveRow('+ri+',1)" title="Вниз">\u2193</button>';
    cons+='<button class="btn btn-sm" style="font-size:9px;padding:1px 4px;color:var(--text-dim)" onclick="apDeleteRow('+ri+')" title="Удалить ряд">\u2212</button>';
    cons+='</span></div>';
  }
  cons+='<button class="btn btn-sm" style="font-size:10px;padding:2px 8px;margin-top:4px" onclick="apAddRow()" title="Добавить пустой ряд">\u002B Ряд</button>';
  cons+='</div>';
  // Hidden blocks pool (blocks removed from layout)
  var allBids=Object.keys(apBlockLabels);
  var usedBids={};for(var ri=0;ri<apRows.length;ri++){for(var ci=0;ci<apRows[ri].length;ci++){usedBids[apRows[ri][ci]]=true}}
  var hidden=allBids.filter(function(b){return !usedBids[b]&&b.indexOf('spacer')!==0&&b.indexOf('divider')!==0});
  if(hidden.length>0){
    cons+='<div style="margin-bottom:8px"><div style="font-size:10px;color:var(--text-dim);margin-bottom:4px">Скрытые блоки (клик = вернуть):</div><div style="display:flex;gap:4px;flex-wrap:wrap">';
    for(var hi=0;hi<hidden.length;hi++){
      cons+='<div class="ap-chip" onclick="apRestoreBlock(\''+hidden[hi]+'\')" style="background:var(--surface2);color:var(--text-dim);padding:3px 10px;border-radius:12px;font-size:11px;cursor:pointer;opacity:0.6;border:1px dashed var(--text-dim)">\u002B '+(apBlockLabels[hidden[hi]]||hidden[hi])+'</div>';
    }
    cons+='</div></div>';
  }
  // Add spacer/divider buttons
  cons+='<div style="display:flex;gap:6px;margin-bottom:10px">';
  cons+='<button class="btn btn-sm" style="font-size:10px;padding:2px 8px" onclick="apAddSpacer()" title="Добавить пустое место">\u2195 Отступ</button>';
  cons+='<button class="btn btn-sm" style="font-size:10px;padding:2px 8px" onclick="apAddDivider()" title="Добавить разделитель">\u2500 Разделитель</button>';
  cons+='</div>';
  // Device tabs
  var deviceW=window._apDevice||'phone';
  cons+='<div style="display:flex;gap:4px;margin-bottom:8px">';
  ['phone','tablet','desktop'].forEach(function(d){
    var ic=d==='phone'?'\ud83d\udcf1 Телефон':d==='tablet'?'\ud83d\udcf1 Планшет':'\ud83d\udda5 Десктоп';
    var act=d===deviceW;
    cons+='<button class="btn btn-sm" onclick="setAuthPreviewDevice(\''+d+'\')" style="font-size:10px;padding:2px 8px;background:'+(act?'var(--accent)':'var(--surface2)')+';color:'+(act?'#000':'var(--text-muted)')+'">'+ic+'</button>';
  });
  cons+='</div>';
  // Preview card with draggable+clickable blocks
  var frameW=deviceW==='phone'?'280px':deviceW==='tablet'?'400px':'100%';
  var cardPad=deviceW==='phone'?'20px':deviceW==='tablet'?'30px':'40px';
  var cardMax=deviceW==='phone'?'280px':deviceW==='tablet'?'360px':'420px';
  var cardContent='';
  for(var ri=0;ri<apRows.length;ri++){
    var row=apRows[ri];
    var parts=[];
    for(var ci=0;ci<row.length;ci++){
      var bid=row[ci];
      if(!blocks[bid])continue;
      var bst=apStyles[bid]||'';
      var wrap='<div class="ap-pblock" draggable="true" data-bid="'+bid+'" data-row="'+ri+'" data-col="'+ci+'" style="cursor:grab;position:relative;border:1px dashed transparent;border-radius:4px;padding:1px;transition:border-color 0.15s;'+bst+'">';
      wrap+=blocks[bid];
      wrap+='<div class="ap-pblock-label" style="display:none;position:absolute;top:-14px;left:50%;transform:translateX(-50%);background:var(--accent);color:#000;font-size:8px;padding:1px 6px;border-radius:8px;white-space:nowrap;pointer-events:none">'+(apBlockLabels[bid]||bid)+'</div>';
      wrap+='</div>';
      parts.push(wrap);
    }
    if(!parts.length)continue;
    if(parts.length===1){cardContent+=parts[0]}
    else{cardContent+='<div style="display:flex;align-items:center;justify-content:center;gap:12px;flex-wrap:wrap">';for(var pi=0;pi<parts.length;pi++){cardContent+='<div style="flex:1;min-width:0">'+parts[pi]+'</div>'}cardContent+='</div>'}
  }
  cons+='<div style="width:'+frameW+';min-height:200px;margin:0 auto;background:'+s.bg_color+';display:flex;justify-content:center;align-items:center;border-radius:12px;overflow:hidden;padding:20px'+bgImg+'">';
  cons+='<div id="ap-card-area" style="background:'+s.card_color+';border-radius:12px;padding:'+cardPad+';max-width:'+cardMax+';width:85%;text-align:center;box-shadow:0 4px 16px rgba(0,0,0,0.3);position:relative">';
  cons+=cardContent;
  cons+='</div></div>';
  // Style editor popup placeholder
  cons+='<div id="ap-style-popup" style="display:none;margin-top:8px;padding:10px;background:var(--surface);border:1px solid var(--surface2);border-radius:8px"></div>';
  preview.innerHTML=cons;
  apSetupChipDrag();
  apSetupPreviewDrag();
};

window.setAuthPreviewDevice=function(d){window._apDevice=d;updateAuthPreview()};
window.apMoveRow=function(ri,dir){
  var t=ri+dir;if(t<0||t>=apRows.length)return;
  var tmp=apRows[ri];apRows[ri]=apRows[t];apRows[t]=tmp;
  updateAuthPreview();
};
window.apDeleteRow=function(ri){
  if(ri>=0&&ri<apRows.length){apRows.splice(ri,1);updateAuthPreview()}
};
window.apAddRow=function(){
  apRows.push([]);updateAuthPreview();
};
window.apRemoveBlock=function(ri,ci){
  if(apRows[ri]){
    var bid=apRows[ri][ci];
    apRows[ri].splice(ci,1);
    if(!apRows[ri].length)apRows.splice(ri,1);
    // Remove custom spacer/divider labels if block is fully removed
    if(bid&&(bid.indexOf('spacer')===0||bid.indexOf('divider')===0)){delete apBlockLabels[bid];delete apStyles[bid]}
    updateAuthPreview();
  }
};
window.apRestoreBlock=function(bid){
  apRows.push([bid]);updateAuthPreview();
};
window.apAddSpacer=function(){
  var id='spacer_'+apNextSpacer++;
  apBlockLabels[id]='\u2195 Отступ';
  apRows.push([id]);updateAuthPreview();
};
window.apAddDivider=function(){
  var id='divider_'+apNextSpacer++;
  apBlockLabels[id]='\u2500 Разделитель';
  apRows.push([id]);updateAuthPreview();
};

// Chip drag (rows editor)
window.apSetupChipDrag=function(){
  var chips=document.querySelectorAll('.ap-chip');
  var rowWraps=document.querySelectorAll('.ap-row-wrap');
  chips.forEach(function(chip){
    chip.addEventListener('dragstart',function(e){
      e.dataTransfer.setData('text/plain',this.getAttribute('data-bid'));
      e.dataTransfer.effectAllowed='move';
      this.style.opacity='0.4';
      window._apDragBid=this.getAttribute('data-bid');
      window._apDragRow=parseInt(this.getAttribute('data-row'));
      window._apDragCol=parseInt(this.getAttribute('data-col'));
      window._apDragSrc='chip';
    });
    chip.addEventListener('dragend',function(){this.style.opacity='1';window._apDragBid=null});
  });
  rowWraps.forEach(function(rw){
    rw.addEventListener('dragover',function(e){e.preventDefault();e.dataTransfer.dropEffect='move';this.style.borderColor='var(--accent)'});
    rw.addEventListener('dragleave',function(){this.style.borderColor='var(--surface2)'});
    rw.addEventListener('drop',function(e){
      e.preventDefault();this.style.borderColor='var(--surface2)';
      var bid=window._apDragBid;if(!bid)return;
      var fromRi=window._apDragRow,fromCi=window._apDragCol;
      var toRi=parseInt(this.getAttribute('data-row'));
      if(apRows[fromRi]){apRows[fromRi].splice(fromCi,1);if(!apRows[fromRi].length)apRows.splice(fromRi,1)}
      if(fromRi<toRi&&apRows.length<toRi+1)toRi=apRows.length-1;
      if(toRi<0)toRi=0;
      if(toRi>=apRows.length){apRows.push([bid])}else{apRows[toRi].push(bid)}
      updateAuthPreview();
    });
  });
};

// Preview block drag (in the card itself) + click to style
window.apSetupPreviewDrag=function(){
  var pblocks=document.querySelectorAll('.ap-pblock');
  pblocks.forEach(function(pb){
    // Hover label
    pb.addEventListener('mouseenter',function(){var lbl=this.querySelector('.ap-pblock-label');if(lbl)lbl.style.display='block';this.style.borderColor='rgba(255,255,255,0.3)'});
    pb.addEventListener('mouseleave',function(){var lbl=this.querySelector('.ap-pblock-label');if(lbl)lbl.style.display='none';this.style.borderColor='transparent'});
    // Click → style editor
    pb.addEventListener('click',function(e){
      e.stopPropagation();
      var bid=this.getAttribute('data-bid');
      apShowStyleEditor(bid);
    });
    // Drag in preview
    pb.addEventListener('dragstart',function(e){
      e.dataTransfer.effectAllowed='move';
      window._apDragBid=this.getAttribute('data-bid');
      window._apDragRow=parseInt(this.getAttribute('data-row'));
      window._apDragCol=parseInt(this.getAttribute('data-col'));
      window._apDragSrc='preview';
      this.style.opacity='0.4';
    });
    pb.addEventListener('dragend',function(){this.style.opacity='1';window._apDragBid=null});
    pb.addEventListener('dragover',function(e){e.preventDefault();e.dataTransfer.dropEffect='move';this.style.borderColor='var(--accent)'});
    pb.addEventListener('dragleave',function(){this.style.borderColor='transparent'});
    pb.addEventListener('drop',function(e){
      e.preventDefault();this.style.borderColor='transparent';
      var bid=window._apDragBid;if(!bid)return;
      var fromRi=window._apDragRow,fromCi=window._apDragCol;
      var toRi=parseInt(this.getAttribute('data-row'));
      var toCi=parseInt(this.getAttribute('data-col'));
      var toBid=this.getAttribute('data-bid');
      if(bid===toBid)return;
      // Swap positions
      if(apRows[fromRi])apRows[fromRi][fromCi]=toBid;
      if(apRows[toRi])apRows[toRi][toCi]=bid;
      updateAuthPreview();
    });
  });
};

// Per-block style editor popup
window.apShowStyleEditor=function(bid){
  var popup=document.getElementById('ap-style-popup');
  if(!popup)return;
  var cur=apStyles[bid]||'';
  // Parse current values
  var fs=cur.match(/font-size:\s*(\d+)/);fs=fs?parseInt(fs[1]):0;
  var sc=cur.match(/transform:\s*scale\(([^)]+)\)/);sc=sc?parseFloat(sc[1]):1;
  var pd=cur.match(/padding:\s*(\d+)/);pd=pd?parseInt(pd[1]):0;
  var h='<div style="display:flex;align-items:center;gap:8px;margin-bottom:6px"><b style="color:var(--text);font-size:12px">'+(apBlockLabels[bid]||bid)+'</b><span style="font-size:10px;color:var(--text-dim)">Настройки блока</span>';
  h+='<button class="btn btn-sm" style="margin-left:auto;font-size:9px;padding:1px 6px" onclick="apCloseStyleEditor()">✕</button></div>';
  h+='<div style="display:grid;grid-template-columns:80px 1fr 40px;gap:6px;align-items:center;font-size:11px;color:var(--text-muted)">';
  h+='<span>Размер шрифта</span><input type="range" min="0" max="60" value="'+fs+'" id="ap-se-fs" oninput="apLiveBlockStyle(\''+bid+'\')" onchange="apCommitBlockStyle(\''+bid+'\')"><span id="ap-se-fs-v">'+(fs||'авто')+'</span>';
  h+='<span>Масштаб</span><input type="range" min="50" max="200" value="'+Math.round(sc*100)+'" id="ap-se-sc" oninput="apLiveBlockStyle(\''+bid+'\')" onchange="apCommitBlockStyle(\''+bid+'\')"><span id="ap-se-sc-v">'+Math.round(sc*100)+'%</span>';
  h+='<span>Отступы</span><input type="range" min="0" max="30" value="'+pd+'" id="ap-se-pd" oninput="apLiveBlockStyle(\''+bid+'\')" onchange="apCommitBlockStyle(\''+bid+'\')"><span id="ap-se-pd-v">'+(pd||'0')+'px</span>';
  h+='</div>';
  h+='<div style="margin-top:6px"><label style="font-size:10px;color:var(--text-dim)">Свой CSS (inline)</label><input class="input-sm" style="width:100%;font-size:11px;font-family:monospace;margin-top:2px" id="ap-se-raw" value="'+esc(cur)+'" oninput="apUpdateBlockStyleRaw(\''+bid+'\')"></div>';
  if(cur){h+='<button class="btn btn-sm btn-warning" style="margin-top:6px;font-size:10px" onclick="delete apStyles[\''+bid+'\'];apCloseStyleEditor();updateAuthPreview()">Сбросить стиль</button>'}
  popup.style.display='block';
  popup.innerHTML=h;
};
window.apCloseStyleEditor=function(){var p=document.getElementById('ap-style-popup');if(p)p.style.display='none'};
// Build style string from slider values
window.apBuildStyle=function(){
  var fs=parseInt(document.getElementById('ap-se-fs').value);
  var sc=parseInt(document.getElementById('ap-se-sc').value);
  var pd=parseInt(document.getElementById('ap-se-pd').value);
  document.getElementById('ap-se-fs-v').textContent=fs||'авто';
  document.getElementById('ap-se-sc-v').textContent=sc+'%';
  document.getElementById('ap-se-pd-v').textContent=pd+'px';
  var parts=[];
  if(fs>0)parts.push('font-size:'+fs+'px');
  if(sc!==100)parts.push('transform:scale('+(sc/100)+')');
  if(pd>0)parts.push('padding:'+pd+'px');
  return parts.join(';');
};
// Live update: just patch the block style in DOM, no full re-render
window.apLiveBlockStyle=function(bid){
  var style=apBuildStyle();
  if(style)apStyles[bid]=style;else delete apStyles[bid];
  var rawEl=document.getElementById('ap-se-raw');if(rawEl)rawEl.value=style;
  // Patch the block in preview without re-render
  var el=document.querySelector('.ap-pblock[data-bid="'+bid+'"]');
  if(el){
    // Keep base styles, add user styles
    var base='cursor:grab;position:relative;border:1px dashed transparent;border-radius:4px;padding:1px;transition:border-color 0.15s;';
    el.setAttribute('style',base+style);
  }
};
// Commit: full re-render + re-open editor on slider release
window.apCommitBlockStyle=function(bid){
  var style=apBuildStyle();
  if(style)apStyles[bid]=style;else delete apStyles[bid];
  updateAuthPreview();
  setTimeout(function(){apShowStyleEditor(bid)},10);
};
window.apUpdateBlockStyleRaw=function(bid){
  var v=document.getElementById('ap-se-raw').value.trim();
  if(v)apStyles[bid]=v;else delete apStyles[bid];
  // Live patch
  var el=document.querySelector('.ap-pblock[data-bid="'+bid+'"]');
  if(el){el.setAttribute('style','cursor:grab;position:relative;border:1px dashed transparent;border-radius:4px;padding:1px;transition:border-color 0.15s;'+v)}
};

window.saveAuthPageStyle=function(){
  var v=getAuthPageValues();
  var out={};
  var defs=apDefs;
  for(var k in defs){if(v[k]&&v[k]!==defs[k])out[k]=v[k]}
  if(JSON.stringify(v.block_rows)!==JSON.stringify(apDefaultRows)){out.block_rows=v.block_rows}
  // Save block_styles only if non-empty
  var hasStyles=false;for(var k in v.block_styles){hasStyles=true;break}
  if(hasStyles)out.block_styles=v.block_styles;
  post('/api/tgsettings',{auth_page:out}).then(function(r){
    if(r.ok)toast('Стили авторизации сохранены');
    else toast(r.error||'Ошибка','error');
  });
};

window.resetAuthPageStyle=function(){
  if(!confirm('Сбросить стили авторизации на стандартные?'))return;
  apRows=JSON.parse(JSON.stringify(apDefaultRows));
  apStyles={};
  post('/api/tgsettings',{auth_page:{}}).then(function(r){
    if(r.ok){toast('Стили сброшены');loadTGSettings()}
    else toast(r.error||'Ошибка','error');
  });
};

window.regenPath=function(){
  if(!confirm('Путь админки будет изменён. Требуется перезапуск. Продолжить?'))return;
  post('/api/tgsettings/regen-path',{}).then(function(r){
    if(r.ok){toast('Новый путь: /'+r.new_path);showRestart();loadTGSettings()}
    else toast(r.error||'Ошибка','error');
  });
};

// --- Deps ---
window.loadDeps=function(){
  var el=document.getElementById('deps-content');
  el.innerHTML='<div style="color:var(--text-muted);padding:20px;text-align:center">\u041F\u0440\u043E\u0432\u0435\u0440\u043A\u0430 \u0437\u0430\u0432\u0438\u0441\u0438\u043C\u043E\u0441\u0442\u0435\u0439...</div>';
  api('/api/deps').then(function(d){renderDeps(d)}).catch(function(e){el.innerHTML='<div style="color:var(--danger)">\u041E\u0448\u0438\u0431\u043A\u0430: '+e+'</div>'});
};
window.checkDepsUpdates=function(){
  var el=document.getElementById('deps-content');
  var btn=document.getElementById('deps-check-updates-btn');
  btn.disabled=true;btn.textContent='\u041F\u0440\u043E\u0432\u0435\u0440\u043A\u0430...';
  api('/api/deps?check_latest=1').then(function(d){
    btn.disabled=false;btn.textContent='\u041F\u0440\u043E\u0432\u0435\u0440\u0438\u0442\u044C \u043E\u0431\u043D\u043E\u0432\u043B\u0435\u043D\u0438\u044F';
    renderDeps(d);
  }).catch(function(e){
    btn.disabled=false;btn.textContent='\u041F\u0440\u043E\u0432\u0435\u0440\u0438\u0442\u044C \u043E\u0431\u043D\u043E\u0432\u043B\u0435\u043D\u0438\u044F';
    el.innerHTML='<div style="color:var(--danger)">\u041E\u0448\u0438\u0431\u043A\u0430: '+e+'</div>';
  });
};
function renderDepCard(dep){
  var ok=dep.status==='ok';
  var color=ok?'var(--accent)':'var(--danger)';
  var icon=ok?'\u2713':'\u2717';
  var ver=dep.version?'<div style="color:var(--text-muted);font-size:11px;margin-top:4px">\u0412\u0435\u0440\u0441\u0438\u044F: '+esc(dep.version)+'</div>':'';
  var latest='';
  if(dep.latest){
    var isNew=dep.version&&dep.latest!==dep.version;
    latest='<div style="color:'+(isNew?'#e2b93d':'var(--text-dim)')+';font-size:11px;margin-top:2px">\u041F\u043E\u0441\u043B\u0435\u0434\u043D\u044F\u044F: '+esc(dep.latest)+(isNew?' \u2B06':'')+'</div>';
  }
  var note=dep.note?'<div style="color:var(--text-dim);font-size:11px;margin-top:2px">'+esc(dep.note)+'</div>':'';
  var badge=dep.pip_pkg?'<span style="background:rgba(59,130,246,0.15);color:#60a5fa;font-size:9px;padding:1px 6px;border-radius:3px;margin-left:6px">pip</span>':'';
  var updateBtn='';
  var btnId='dep-update-'+dep.binary.replace(/[^a-zA-Z0-9]/g,'_');
  if(dep.updatable&&ok){
    var canUpdate=dep.latest&&dep.version&&dep.latest!==dep.version;
    if(dep.updating){
      updateBtn='<div id="'+btnId+'" style="margin-top:8px"><span style="color:var(--accent);font-size:12px">\u2B6E \u041E\u0431\u043D\u043E\u0432\u043B\u0435\u043D\u0438\u0435...</span></div>';
    }else if(canUpdate){
      updateBtn='<div style="margin-top:8px"><button id="'+btnId+'" class="btn" style="font-size:11px;padding:4px 12px;background:var(--accent);color:#000;border:none;border-radius:4px;cursor:pointer" onclick="updateDep(\''+esc(dep.binary)+'\')">\u041E\u0431\u043D\u043E\u0432\u0438\u0442\u044C</button></div>';
    }else if(!dep.latest){
      updateBtn='<div style="margin-top:8px"><button id="'+btnId+'" class="btn" style="font-size:11px;padding:4px 12px;background:#333;color:var(--text-dim);border:1px solid rgba(255,255,255,0.1);border-radius:4px;cursor:pointer" onclick="updateDep(\''+esc(dep.binary)+'\')">\u041F\u0435\u0440\u0435\u0443\u0441\u0442\u0430\u043D\u043E\u0432\u0438\u0442\u044C</button></div>';
    }
  }else if(dep.updatable&&!ok){
    updateBtn='<div style="margin-top:8px"><button id="'+btnId+'" class="btn" style="font-size:11px;padding:4px 12px;background:var(--accent);color:#000;border:none;border-radius:4px;cursor:pointer" onclick="updateDep(\''+esc(dep.binary)+'\')">\u0423\u0441\u0442\u0430\u043D\u043E\u0432\u0438\u0442\u044C</button></div>';
  }
  return '<div style="background:rgba(22,27,35,0.6);border:1px solid '+(ok?'rgba(74,222,128,0.15)':'rgba(248,113,113,0.2)')+';border-radius:8px;padding:12px;min-width:0">'
    +'<div style="display:flex;align-items:center;gap:8px">'
    +'<span style="color:'+color+';font-size:15px;font-weight:bold;flex-shrink:0">'+icon+'</span>'
    +'<span style="color:var(--text);font-size:13px;font-weight:500;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">'+esc(dep.name)+badge+'</span>'
    +'<span style="color:var(--text-dim);font-size:10px;margin-left:auto;flex-shrink:0;font-family:monospace">'+esc(dep.binary)+'</span>'
    +'</div>'+ver+latest+note+updateBtn+'</div>';
}
function renderDeps(d){
  _lastDepsData=d;
  var el=document.getElementById('deps-content');
  var groups=d.groups||[];
  // Show/hide "Install All" button based on missing count.
  var missingCount=0;
  groups.forEach(function(g){(g.deps||[]).forEach(function(dep){if(dep.status!=='ok'&&dep.updatable)missingCount++})});
  var iaBtn=document.getElementById('deps-install-all-btn');
  if(iaBtn){iaBtn.style.display=missingCount>0?'':'none';iaBtn.textContent='\uD83D\uDCE5 \u0423\u0441\u0442\u0430\u043D\u043E\u0432\u0438\u0442\u044C \u0432\u0441\u0451 ('+missingCount+')';}
  if(!groups.length){el.innerHTML='<div style="color:var(--text-dim);padding:20px;text-align:center">\u041D\u0435\u0442 \u0434\u0430\u043D\u043D\u044B\u0445</div>';return;}
  var html='';
  for(var g=0;g<groups.length;g++){
    var grp=groups[g];
    var deps=grp.deps||[];
    var okCount=0;var total=deps.length;
    for(var i=0;i<deps.length;i++){if(deps[i].status==='ok')okCount++;}
    var allOk=okCount===total;
    var statusDot=allOk?'\uD83D\uDFE2':(okCount===0?'\uD83D\uDD34':'\uD83D\uDFE1');
    var statusText=okCount+'/'+total;
    html+='<div class="dep-grp" style="margin-bottom:12px">';
    html+='<div class="dep-grp-hdr" onclick="this.parentElement.classList.toggle(\'open\')" style="display:flex;align-items:center;gap:10px;padding:10px 14px;background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.06);border-radius:10px;cursor:pointer;user-select:none;transition:background .15s">';
    html+='<span style="font-size:18px">'+grp.icon+'</span>';
    html+='<span style="flex:1;font-size:14px;font-weight:600;color:var(--text)">'+esc(grp.label)+'</span>';
    html+='<span style="font-size:12px;color:var(--text-dim)">'+statusDot+' '+statusText+'</span>';
    html+='<span class="dep-chevron" style="color:var(--text-dim);font-size:11px;transition:transform .2s">\u25B6</span>';
    html+='</div>';
    html+='<div class="dep-grp-body" style="display:none;padding:8px 0 0 0">';
    html+='<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:8px">';
    for(var i=0;i<deps.length;i++){html+=renderDepCard(deps[i]);}
    html+='</div></div></div>';
  }
  el.innerHTML=html;
  // Auto-open groups with missing deps.
  var grpEls=el.querySelectorAll('.dep-grp');
  for(var i=0;i<grpEls.length;i++){
    var cards=grpEls[i].querySelectorAll('[style*="rgba(248,113,113"]');
    if(cards.length>0)grpEls[i].classList.add('open');
  }
  var sys=document.getElementById('deps-sys');
  sys.innerHTML='OS: '+esc(d.os)+' / '+esc(d.arch)+' | Go: '+esc(d.go_ver);
}
window.updateDep=function(binary){
  if(!confirm('\u041E\u0431\u043D\u043E\u0432\u0438\u0442\u044C '+binary+' \u0434\u043E \u043F\u043E\u0441\u043B\u0435\u0434\u043D\u0435\u0439 \u0432\u0435\u0440\u0441\u0438\u0438?'))return;
  var btnId='dep-update-'+binary.replace(/[^a-zA-Z0-9]/g,'_');
  var btn=document.getElementById(btnId);
  if(btn){btn.disabled=true;btn.textContent='\u041E\u0431\u043D\u043E\u0432\u043B\u0435\u043D\u0438\u0435...';}
  post('/api/deps/update',{binary:binary}).then(function(d){
    if(d.ok){
      toast('\u041E\u0431\u043D\u043E\u0432\u043B\u0435\u043D\u0438\u0435 '+binary+' \u0437\u0430\u043F\u0443\u0449\u0435\u043D\u043E');
      pollDepUpdate(binary,btnId);
    }else{
      toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
      if(btn){btn.disabled=false;btn.textContent='\u041E\u0431\u043D\u043E\u0432\u0438\u0442\u044C';}
    }
  }).catch(function(e){
    toast('\u041E\u0448\u0438\u0431\u043A\u0430: '+e,'error');
    if(btn){btn.disabled=false;btn.textContent='\u041E\u0431\u043D\u043E\u0432\u0438\u0442\u044C';}
  });
};
function pollDepUpdate(binary,btnId){
  var interval=setInterval(function(){
    api('/api/deps/update/status?binary='+encodeURIComponent(binary)).then(function(d){
      if(d.status==='done'){
        clearInterval(interval);
        var msg='\u2705 '+binary+' \u043E\u0431\u043D\u043E\u0432\u043B\u0451\u043D';
        if(d.new_version)msg+=' \u2192 '+d.new_version;
        toast(msg);
        checkDepsUpdates();
      }else if(d.status==='error'){
        clearInterval(interval);
        toast('\u274C '+binary+': '+(d.error||'\u043E\u0448\u0438\u0431\u043A\u0430'),'error');
        var btn=document.getElementById(btnId);
        if(btn){btn.disabled=false;btn.textContent='\u041E\u0431\u043D\u043E\u0432\u0438\u0442\u044C';}
      }
    }).catch(function(){clearInterval(interval)});
  },2000);
}

// Install all missing deps
var _lastDepsData=null;
window.installAllMissing=function(){
  if(!_lastDepsData)return;
  var missing=[];
  (_lastDepsData.groups||[]).forEach(function(g){
    (g.deps||[]).forEach(function(d){
      if(d.status!=='ok'&&d.updatable)missing.push(d.binary);
    });
  });
  if(!missing.length){toast('Все зависимости установлены');return}
  if(!confirm('Установить '+missing.length+' зависимост'+(missing.length===1?'ь':'ей')+'?\n\n'+missing.join(', ')))return;
  var btn=document.getElementById('deps-install-all-btn');
  if(btn){btn.disabled=true;btn.textContent='Установка...';}
  post('/api/deps/install-all',{binaries:missing}).then(function(d){
    if(d.ok){
      toast(d.count+' зависимост'+(d.count===1?'ь':'ей')+' устанавливается...');
      // Poll each one.
      (d.started||[]).forEach(function(b){
        var btnId='dep-update-'+b.replace(/[^a-zA-Z0-9]/g,'_');
        pollDepUpdate(b,btnId);
      });
      // Re-check after 15s.
      setTimeout(function(){checkDepsUpdates()},15000);
    }else{toast(d.error||'Ошибка','error');}
    if(btn){btn.disabled=false;btn.textContent='\uD83D\uDCE5 Установить всё';}
  }).catch(function(e){
    toast('Ошибка: '+e,'error');
    if(btn){btn.disabled=false;btn.textContent='\uD83D\uDCE5 Установить всё';}
  });
};
// --- Inspector ---
var inspectorCatNames={system:'\u0421\u0438\u0441\u0442\u0435\u043C\u0430',balancers:'\u0411\u0430\u043B\u0430\u043D\u0441\u0435\u0440\u044B',torrserver:'TorrServer',transcoding:'\u0422\u0440\u0430\u043D\u0441\u043A\u043E\u0434\u0438\u0440\u043E\u0432\u0430\u043D\u0438\u0435',tmdb:'TMDB Proxy',youtube:'YouTube',proxy:'\u041F\u0440\u043E\u043A\u0441\u0438'};
var inspectorCatIcons={system:'\uD83D\uDDA5',balancers:'\u2696',torrserver:'\uD83C\uDFAC',transcoding:'\uD83C\uDFA5',tmdb:'\uD83C\uDFAC',youtube:'\u25B6',proxy:'\uD83C\uDF10'};
window.loadInspector=function(){
  var el=document.getElementById('inspector-results');
  if(!el.innerHTML)el.innerHTML='<div style="color:var(--text-dim);padding:20px;text-align:center">\u041D\u0430\u0436\u043C\u0438\u0442\u0435 &laquo;\u0417\u0430\u043F\u0443\u0441\u0442\u0438\u0442\u044C \u043F\u0440\u043E\u0432\u0435\u0440\u043A\u0443&raquo; \u0434\u043B\u044F \u0430\u043D\u0430\u043B\u0438\u0437\u0430 \u0432\u0441\u0435\u0445 \u043F\u043E\u0434\u0441\u0438\u0441\u0442\u0435\u043C</div>';
};
window.runInspector=function(){
  var btn=document.getElementById('inspector-run-btn');
  var st=document.getElementById('inspector-status');
  var el=document.getElementById('inspector-results');
  var sum=document.getElementById('inspector-summary');
  btn.disabled=true;
  st.textContent='\u0410\u043D\u0430\u043B\u0438\u0437...';
  el.innerHTML='';
  sum.textContent='';
  post('/api/inspector/run',{}).then(function(d){
    btn.disabled=false;
    st.textContent='';
    var issues=d.issues||[];
    var order=d.order||['system','balancers','torrserver','transcoding','tmdb','youtube','proxy'];
    // Group by category.
    var groups={};
    order.forEach(function(c){groups[c]=[]});
    issues.forEach(function(iss){
      if(!groups[iss.category])groups[iss.category]=[];
      groups[iss.category].push(iss);
    });
    var h='';
    var totalChecks=0,critCount=0,warnCount=0;
    order.forEach(function(cat){
      var catIssues=groups[cat]||[];
      totalChecks++;
      var hasCrit=catIssues.some(function(i){return i.severity==='critical'});
      var hasWarn=catIssues.some(function(i){return i.severity==='warning'});
      catIssues.forEach(function(i){if(i.severity==='critical')critCount++;if(i.severity==='warning')warnCount++});
      var statusIcon=hasCrit?'\uD83D\uDD34':hasWarn?'\uD83D\uDFE1':'\uD83D\uDFE2';
      var statusText=hasCrit?catIssues.length+' \u043F\u0440\u043E\u0431\u043B\u0435\u043C':hasWarn?catIssues.length+' \u043F\u0440\u0435\u0434\u0443\u043F\u0440.':'\u043E\u043A';
      var catName=inspectorCatNames[cat]||cat;
      var catIcon=inspectorCatIcons[cat]||'\u2699';
      h+='<div class="insp-cat" style="margin-bottom:12px">';
      h+='<div class="insp-cat-hdr" onclick="this.parentElement.classList.toggle(\'open\')" style="display:flex;align-items:center;gap:8px;padding:10px 12px;background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.06);border-radius:8px;cursor:pointer;user-select:none">';
      h+='<span style="font-size:16px">'+catIcon+'</span>';
      h+='<span style="flex:1;font-size:14px;font-weight:500;color:var(--text)">'+catName+'</span>';
      h+='<span style="font-size:13px;color:var(--text-dim)">'+statusIcon+' '+statusText+'</span>';
      h+='<span class="insp-chevron" style="color:var(--text-dim);font-size:11px;transition:transform .2s">\u25B6</span>';
      h+='</div>';
      if(catIssues.length){
        h+='<div class="insp-cat-body" style="display:none;padding:4px 0 0 28px">';
        catIssues.forEach(function(iss){
          var sevColor=iss.severity==='critical'?'var(--danger)':iss.severity==='warning'?'#e2b93d':'var(--text-dim)';
          var sevIcon=iss.severity==='critical'?'\u274C':iss.severity==='warning'?'\u26A0':'\u2139';
          h+='<div style="display:flex;align-items:flex-start;gap:8px;padding:6px 0;border-bottom:1px solid rgba(255,255,255,0.03)">';
          h+='<span style="color:'+sevColor+';font-size:13px;flex-shrink:0">'+sevIcon+'</span>';
          h+='<div style="flex:1;min-width:0">';
          h+='<div style="font-size:13px;color:var(--text)">'+(iss.target?'<b>'+esc(iss.target)+'</b>: ':'')+esc(iss.title)+'</div>';
          if(iss.detail)h+='<div style="font-size:11px;color:var(--text-dim);margin-top:2px">'+esc(iss.detail)+'</div>';
          h+='</div>';
          if(iss.fix_id){
            h+='<button class="btn btn-sm" style="flex-shrink:0;font-size:11px;padding:3px 10px;background:var(--accent);color:#000;border:none;border-radius:4px;cursor:pointer" onclick="applyInspectorFix(\''+esc(iss.fix_id)+'\',\''+esc(iss.target||'')+'\')">'+esc(iss.fix_label||'\u0418\u0441\u043F\u0440\u0430\u0432\u0438\u0442\u044C')+'</button>';
          }
          h+='</div>';
        });
        h+='</div>';
      }
      h+='</div>';
    });
    el.innerHTML=h;
    // Auto-expand categories with issues.
    el.querySelectorAll('.insp-cat').forEach(function(cat){
      var body=cat.querySelector('.insp-cat-body');
      if(body&&body.children.length>0){cat.classList.add('open')}
    });
    sum.textContent='\u0412\u0441\u0435\u0433\u043E: '+totalChecks+' \u043A\u0430\u0442\u0435\u0433\u043E\u0440\u0438\u0439, '+critCount+' \u043E\u0448\u0438\u0431\u043E\u043A, '+warnCount+' \u043F\u0440\u0435\u0434\u0443\u043F\u0440\u0435\u0436\u0434\u0435\u043D\u0438\u0439';
  }).catch(function(e){
    btn.disabled=false;
    st.textContent='';
    el.innerHTML='<div style="color:var(--danger)">\u041E\u0448\u0438\u0431\u043A\u0430: '+e+'</div>';
  });
};
window.applyInspectorFix=function(fixId,target){
  if(!confirm('\u041F\u0440\u0438\u043C\u0435\u043D\u0438\u0442\u044C \u0438\u0441\u043F\u0440\u0430\u0432\u043B\u0435\u043D\u0438\u0435?'))return;
  post('/api/inspector/fix',{fix_id:fixId,target:target}).then(function(d){
    if(d.ok){toast(d.message||'\u0418\u0441\u043F\u0440\u0430\u0432\u043B\u0435\u043D\u043E');setTimeout(runInspector,500)}
    else toast(d.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error');
  }).catch(function(e){toast('\u041E\u0448\u0438\u0431\u043A\u0430: '+e,'error')});
};

// --- Broadcast ---
var broadcastUserCount=0;
window.loadBroadcast=function(){
  api('/api/broadcast').then(function(d){
    broadcastUserCount=d.user_count||0;
    document.getElementById('broadcast-recipients').innerHTML='\u041F\u043E\u043B\u0443\u0447\u0430\u0442\u0435\u043B\u0435\u0439: <b style="color:var(--accent)">'+broadcastUserCount+'</b> \u0430\u043A\u0442\u0438\u0432\u043D\u044B\u0445 \u043F\u043E\u043B\u044C\u0437\u043E\u0432\u0430\u0442\u0435\u043B\u0435\u0439';
    document.getElementById('broadcast-status').innerHTML='';
  }).catch(function(e){toast('\u041E\u0448\u0438\u0431\u043A\u0430: '+e.message,'error')});
};
window.sendBroadcast=function(){
  var text=document.getElementById('broadcast-text').value.trim();
  if(!text){toast('\u0412\u0432\u0435\u0434\u0438\u0442\u0435 \u0442\u0435\u043A\u0441\u0442','error');return}
  if(!confirm('\u041E\u0442\u043F\u0440\u0430\u0432\u0438\u0442\u044C \u0441\u043E\u043E\u0431\u0449\u0435\u043D\u0438\u0435 '+broadcastUserCount+' \u043F\u043E\u043B\u044C\u0437\u043E\u0432\u0430\u0442\u0435\u043B\u044F\u043C?'))return;
  var btn=document.getElementById('broadcast-btn');
  btn.disabled=true;btn.textContent='\u23F3 \u041E\u0442\u043F\u0440\u0430\u0432\u043A\u0430...';
  document.getElementById('broadcast-status').innerHTML='';
  post('/api/broadcast',{text:text}).then(function(r){
    btn.disabled=false;btn.textContent='\u041E\u0442\u043F\u0440\u0430\u0432\u0438\u0442\u044C';
    if(r.ok){
      document.getElementById('broadcast-status').innerHTML='\u2705 \u041E\u0442\u043F\u0440\u0430\u0432\u043B\u0435\u043D\u043E: <b>'+r.sent+'</b> \u0438\u0437 '+r.total;
      toast('\u0420\u0430\u0441\u0441\u044B\u043B\u043A\u0430 \u043E\u0442\u043F\u0440\u0430\u0432\u043B\u0435\u043D\u0430: '+r.sent+'/'+r.total);
    }else{toast(r.error||'\u041E\u0448\u0438\u0431\u043A\u0430','error')}
  }).catch(function(e){
    btn.disabled=false;btn.textContent='\u041E\u0442\u043F\u0440\u0430\u0432\u0438\u0442\u044C';
    toast('\u041E\u0448\u0438\u0431\u043A\u0430: '+e.message,'error');
  });
};

// --- Passkeys ---
function loadPasskeys(){
  fetch(basePath+'/api/webauthn/credentials',{credentials:'include'}).then(function(r){
    if(!r.ok)throw new Error(r.status);
    return r.json();
  }).then(function(d){
    var creds=d.credentials||[];
    var el=document.getElementById('passkeys-list');
    if(!el)return;
    if(!creds.length){el.innerHTML='<div style="color:var(--text-muted);font-size:13px;padding:8px 0">Нет зарегистрированных Passkeys</div>';return}
    var h='<table><thead><tr><th>Название</th><th>Создан</th><th>Последний вход</th><th>Действия</th></tr></thead><tbody>';
    creds.forEach(function(c){
      var created=c.created_at?new Date(c.created_at).toLocaleDateString('ru'):'—';
      var used=c.last_used_at&&c.last_used_at!=='0001-01-01T00:00:00Z'?new Date(c.last_used_at).toLocaleDateString('ru'):'—';
      h+='<tr><td>'+esc(c.display_name||'Passkey')+'</td><td>'+created+'</td><td>'+used+'</td>';
      h+='<td><button class="btn btn-danger btn-sm" onclick="deletePasskey(\''+esc(c.id)+'\')">Удалить</button></td></tr>';
    });
    h+='</tbody></table>';
    el.innerHTML=h;
  }).catch(function(){
    var el=document.getElementById('passkeys-list');
    if(el)el.innerHTML='';
  });
}
window.registerPasskey=function(){
  var nameEl=document.getElementById('passkey-name');
  var name=(nameEl&&nameEl.value.trim())||'Passkey';
  fetch(basePath+'/api/webauthn/register/begin',{method:'POST',credentials:'same-origin'}).then(function(r){return r.json()}).then(function(opts){
    if(opts.error){toast(opts.error,'error');return}
    // Decode base64url challenge
    opts.publicKey.challenge=base64urlDecode(opts.publicKey.challenge);
    opts.publicKey.user.id=base64urlDecode(opts.publicKey.user.id);
    if(opts.publicKey.excludeCredentials){
      opts.publicKey.excludeCredentials=opts.publicKey.excludeCredentials.map(function(c){c.id=base64urlDecode(c.id);return c});
    }
    return navigator.credentials.create({publicKey:opts.publicKey});
  }).then(function(cred){
    if(!cred)return;
    var body={id:cred.id,rawId:base64urlEncode(new Uint8Array(cred.rawId)),type:cred.type,response:{
      attestationObject:base64urlEncode(new Uint8Array(cred.response.attestationObject)),
      clientDataJSON:base64urlEncode(new Uint8Array(cred.response.clientDataJSON))
    }};
    return fetch(basePath+'/api/webauthn/register/complete?name='+encodeURIComponent(name),{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(function(r){return r.json()});
  }).then(function(r){
    if(!r)return;
    if(r.ok){toast('Passkey зарегистрирован');if(nameEl)nameEl.value='';loadPasskeys()}
    else toast(r.error||'Ошибка регистрации','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};
window.deletePasskey=function(id){
  if(!confirm('Удалить этот Passkey?'))return;
  fetch(basePath+'/api/webauthn/credentials?id='+encodeURIComponent(id),{method:'DELETE',credentials:'same-origin'}).then(function(r){return r.json()}).then(function(r){
    if(r.ok){toast('Passkey удалён');loadPasskeys()}
    else toast(r.error||'Ошибка','error');
  });
};
function base64urlDecode(s){s=s.replace(/-/g,'+').replace(/_/g,'/');while(s.length%4)s+='=';var bin=atob(s),arr=new Uint8Array(bin.length);for(var i=0;i<bin.length;i++)arr[i]=bin.charCodeAt(i);return arr.buffer}
function base64urlEncode(arr){var bin='';arr.forEach(function(b){bin+=String.fromCharCode(b)});return btoa(bin).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'')}

// --- Browser Kit Sessions ---
window.createBKitSessionPrompt=function(){
  var name=prompt('Имя пользователя для Browser Kit:');
  if(!name||!name.trim())return;
  post('/api/bkit/sessions',{name:name.trim()}).then(function(s){
    if(s&&s.token){
      var link=location.origin+'/bkit?token='+encodeURIComponent(s.token);
      prompt('Browser Kit создан!\n\nСсылка для пользователя:',link);
      toast('Browser Kit сессия создана');
      loadBKitSessions();
    }else{toast(s.error||'Ошибка','error')}
  });
};
function loadBKitSessions(){
  api('/api/bkit/sessions').then(function(list){
    var el=document.getElementById('bkit-sessions-list');
    if(!el)return;
    if(!list||!list.length){el.innerHTML='<div style="color:var(--text-muted);font-size:13px;padding:8px 0">Нет сессий</div>';return}
    var h='<table><thead><tr><th>Имя</th><th>Токен</th><th>Ссылка</th><th>Создан</th><th>Действия</th></tr></thead><tbody>';
    list.forEach(function(s){
      var created=s.created_at?new Date(s.created_at).toLocaleDateString('ru'):'—';
      var shortTok=s.token?s.token.substring(0,12)+'…':'—';
      var link=location.origin+'/bkit?token='+encodeURIComponent(s.token);
      h+='<tr><td>'+esc(s.name)+'</td>';
      h+='<td><code style="background:var(--surface3);padding:2px 6px;border-radius:4px;font-family:monospace;font-size:11px;cursor:pointer" onclick="copyBKit(this,\''+esc(s.token)+'\')">'+shortTok+'</code></td>';
      h+='<td><code style="background:var(--surface3);padding:2px 6px;border-radius:4px;font-family:monospace;font-size:11px;cursor:pointer;color:var(--accent)" onclick="copyBKit(this,\''+esc(link)+'\')">📋 Копировать</code></td>';
      h+='<td>'+created+'</td>';
      h+='<td><button class="btn btn-danger btn-sm" onclick="deleteBKitSession(\''+esc(s.id)+'\')">Удалить</button></td></tr>';
    });
    h+='</tbody></table>';
    el.innerHTML=h;
  }).catch(function(){});
}
window.copyBKit=function(el,text){
  navigator.clipboard.writeText(text).then(function(){toast('Скопировано')}).catch(function(){
    var ta=document.createElement('textarea');ta.value=text;document.body.appendChild(ta);ta.select();document.execCommand('copy');document.body.removeChild(ta);toast('Скопировано');
  });
};
window.createBKitSession=function(){
  var nameEl=document.getElementById('bkit-session-name');
  var name=(nameEl&&nameEl.value.trim())||'';
  if(!name){toast('Введите имя','error');return}
  post('/api/bkit/sessions',{name:name}).then(function(s){
    if(s&&s.token){
      toast('Токен создан');
      if(nameEl)nameEl.value='';
      loadBKitSessions();
    }else{toast(s.error||'Ошибка','error')}
  });
};
window.deleteBKitSession=function(id){
  if(!confirm('Удалить эту сессию? Пользователь потеряет доступ.'))return;
  fetch(basePath+'/api/bkit/sessions/'+id,{method:'DELETE',credentials:'include'}).then(function(r){return r.json()}).then(function(r){
    if(r.success){toast('Сессия удалена');loadBKitSessions()}
    else toast(r.error||'Ошибка','error');
  });
};

// --- Admins ---
window.loadAdmins=function(){
  api('/api/admins').then(function(list){
    document.getElementById('admins-body').innerHTML=(list||[]).map(function(a){
      var rm=a.role==='super'?'':'<button class="btn btn-danger btn-sm" onclick="removeAdmin('+a.telegram_id+')">Удалить</button>';
      return '<tr><td>'+a.telegram_id+'</td><td>'+esc(a.role)+'</td><td>'+esc(a.note||'')+'</td><td>'+rm+'</td></tr>';
    }).join('');
  });
};
window.addAdmin=function(){
  var id=parseInt(document.getElementById('new-admin-id').value);
  var note=document.getElementById('new-admin-note').value;
  if(!id){toast('Введите Telegram ID','error');return}
  post('/api/admins',{action:'add',telegram_id:id,note:note}).then(function(r){
    if(r.ok){toast('Админ добавлен');document.getElementById('new-admin-id').value='';document.getElementById('new-admin-note').value='';loadAdmins()}
    else toast(r.error||'Ошибка','error');
  });
};
window.removeAdmin=function(id){
  if(!confirm('Удалить админа '+id+'?'))return;
  post('/api/admins',{action:'remove',telegram_id:id}).then(function(r){
    if(r.ok){toast('Удалён');loadAdmins()}
  });
};

// --- Server Stats (redesigned) ---
var statsTimer=null;
function fmtUptime(s){var d=Math.floor(s/86400),h=Math.floor((s%86400)/3600),m=Math.floor((s%3600)/60);if(d>0)return d+'д '+h+'ч '+m+'м';if(h>0)return h+'ч '+m+'м';return m+'м'}
function sc(v,l,cls){return '<div class="stat-card'+(cls?' '+cls:'')+'"><div class="val">'+v+'</div><div class="lbl">'+l+'</div></div>'}
function srvRing(pct,val,sub,label,color){
  var r=45,c=2*Math.PI*r,off=c-(Math.min(Math.max(pct,0),100)/100)*c;
  return '<div class="srv-ring"><svg viewBox="0 0 110 110"><circle class="bg" cx="55" cy="55" r="'+r+'"/><circle class="fg" cx="55" cy="55" r="'+r+'" stroke="'+color+'" stroke-dasharray="'+c.toFixed(1)+'" stroke-dashoffset="'+off.toFixed(1)+'"/></svg><div class="srv-ring-center"><div class="rv">'+val+'</div><div class="rs">'+sub+'</div></div><div class="rlabel">'+label+'</div></div>';
}
function latBar(label,ms,maxMs){
  var pct=maxMs>0?Math.min(100,(ms/maxMs)*100):0;
  var cls=ms<50?'lgreen':ms<200?'lyellow':ms<500?'lorange':'lred';
  return '<div class="lat-bar-row"><div class="lat-bar-label">'+label+'</div><div class="lat-bar-track"><div class="lat-bar-fill '+cls+'" style="width:'+Math.max(5,pct)+'%">'+ms+' ms</div></div></div>';
}
function routeLatencyTable(rows){
  if(!rows||!rows.length)return '<div class="route-lat-muted">Нет данных за текущую минуту</div>';
  var h='<table class="route-lat-table"><thead><tr><th>Маршрут</th><th>Req</th><th>Avg</th><th>P95</th><th>P99</th><th>Max</th><th>5xx</th></tr></thead><tbody>';
  for(var i=0;i<rows.length&&i<10;i++){
    var r=rows[i]||{};
    h+='<tr>'+
      '<td class="route-lat-path" title="'+esc(r.route||'')+'">'+esc(r.route||'')+'</td>'+
      '<td>'+(r.count||0)+'</td>'+
      '<td>'+Number(r.avg_ms||0).toFixed(2)+' ms</td>'+
      '<td>'+Number(r.p95_ms||0).toFixed(2)+' ms</td>'+
      '<td>'+Number(r.p99_ms||0).toFixed(2)+' ms</td>'+
      '<td>'+Number(r.max_ms||0).toFixed(2)+' ms</td>'+
      '<td>'+(r.errors_5xx||0)+'</td>'+
    '</tr>';
  }
  h+='</tbody></table>';
  return h;
}
function proxyPLLatencyTable(rows){
  if(!rows||!rows.length)return '<div class="route-lat-muted">Нет proxy(pl) данных за текущую минуту</div>';
  var h='<table class="route-lat-table"><thead><tr><th>pl</th><th>Req</th><th>Avg</th><th>P95</th><th>P99</th><th>Max</th><th>5xx</th></tr></thead><tbody>';
  for(var i=0;i<rows.length&&i<10;i++){
    var r=rows[i]||{};
    h+='<tr>'+
      '<td class="route-lat-plugin" title="'+esc(r.plugin||'')+'">'+esc(r.plugin||'')+'</td>'+
      '<td>'+(r.count||0)+'</td>'+
      '<td>'+Number(r.avg_ms||0).toFixed(2)+' ms</td>'+
      '<td>'+Number(r.p95_ms||0).toFixed(2)+' ms</td>'+
      '<td>'+Number(r.p99_ms||0).toFixed(2)+' ms</td>'+
      '<td>'+Number(r.max_ms||0).toFixed(2)+' ms</td>'+
      '<td>'+(r.errors_5xx||0)+'</td>'+
    '</tr>';
  }
  h+='</tbody></table>';
  return h;
}
function fmtRSS(kb){if(!kb)return '—';if(kb>=1048576)return (kb/1048576).toFixed(1)+' GB';if(kb>=1024)return (kb/1024).toFixed(1)+' MB';return kb+' KB'}
function procResources(label,ps){
  if(!ps)return '';
  var os=ps[label];if(!os)return '';
  var h='<div class="proc-resources">';
  if(os.rss_kb>0)h+='<div class="proc-res-item"><div class="rv">'+fmtRSS(os.rss_kb)+'</div><div class="rl">RAM</div></div>';
  var cpu=((os.cpu_user_sec||0)+(os.cpu_sys_sec||0)).toFixed(1);
  if(cpu!=='0.0')h+='<div class="proc-res-item"><div class="rv">'+cpu+'s</div><div class="rl">CPU Time</div></div>';
  if(os.threads>0)h+='<div class="proc-res-item"><div class="rv">'+os.threads+'</div><div class="rl">Потоков</div></div>';
  h+='</div>';return h;
}
function pcard(icon,name,sub,alive,details,tags,resLabel,ps){
  var cls=alive?'alive':'dead';var stCls=alive?'on':'off';var stTxt=alive?'Активен':'Не отвечает';
  var h='<div class="proc-card '+cls+'"><div class="proc-card-head">';
  h+='<div class="proc-card-icon">'+icon+'</div>';
  h+='<div class="proc-card-title"><div class="name">'+name+'</div><div class="sub">'+sub+'</div></div>';
  h+='<div class="proc-status '+stCls+'"><span class="dot"></span>'+stTxt+'</div>';
  h+='</div><div class="proc-details">';
  for(var k=0;k<details.length;k++)h+='<div class="proc-detail"><div class="key">'+details[k][0]+'</div><div class="value">'+details[k][1]+'</div></div>';
  h+='</div>';
  if(tags&&tags.length){h+='<div class="proc-tags">';for(var t=0;t<tags.length;t++)h+='<div class="proc-tag">'+tags[t]+'</div>';h+='</div>'}
  if(resLabel&&ps)h+=procResources(resLabel,ps);
  h+='</div>';return h;
}
window.loadServerStats=function(){
  api('/api/stats').then(function(d){
    var mem=d.mem||{},req=d.requests||{},pct=req.percentiles||{},osInfo=d.os||{},ps=d.process_stats||{};
    // Header
    var hn=osInfo.hostname||'lampac-go';
    setH('srv-hostname',esc(hn)+'<span class="srv-pid">PID '+osInfo.pid+'</span>');
    setT('srv-meta',d.go_version+' \u00b7 '+osInfo.goos+'/'+osInfo.goarch+' \u00b7 '+d.num_cpu+' CPU \u00b7 ProxyLink: '+d.proxylink_entries);
    // Uptime
    setT('srv-uptime',fmtUptime(d.uptime_sec));
    // Ring charts
    var memPct=mem.sys_mb>0?Math.round(mem.heap_alloc_mb/mem.sys_mb*100):0;
    var memColor=memPct>80?'var(--danger)':memPct>60?'var(--warning)':'var(--accent)';
    var gorMax=1000,gorPct=Math.min(100,d.goroutines/gorMax*100);
    var gorColor=d.goroutines>500?'var(--danger)':d.goroutines>100?'var(--warning)':'var(--accent)';
    var reqPct=Math.min(100,req.req_min>0?Math.log10(req.req_min+1)/4*100:0);
    setH('srv-rings',
      srvRing(memPct,mem.heap_alloc_mb+' MB','/ '+mem.sys_mb+' MB','Память',memColor)+
      srvRing(gorPct,d.goroutines,'горутин','Goroutines',gorColor)+
      srvRing(reqPct,req.req_min,'req/мин','Запросы','#4fc3f7')+
      srvRing(req.active>0?Math.min(100,req.active*10):0,req.active,'активных','Active','#ba68c8'));
    // Requests
    setH('stats-requests',
      sc(req.req_min,'Req / мин')+sc(req.req_hour,'Req / час')+sc(req.active,'Active'));
    // Sparkline
    var hist=req.req_history||[0,0,0,0,0,0,0,0,0,0];
    var maxH=Math.max.apply(null,hist)||1;
    var spark='';for(var i=0;i<hist.length;i++){
      var h=Math.max(2,Math.round(hist[i]/maxH*48));
      spark+='<div class="sbar" style="height:'+h+'px" title="'+hist[i]+' req"></div>';
    }
    setH('srv-req-sparkline',spark);
    // Latency bars
    var latVals=[
      ['Avg',req.latency_avg||0],['P50',pct['50']||0],['P75',pct['75']||0],
      ['P90',pct['90']||0],['P95',pct['95']||0],['P99',pct['99']||0]
    ];
    var maxLat=Math.max.apply(null,latVals.map(function(x){return x[1]}))||1;
    var lb='';for(var i=0;i<latVals.length;i++)lb+=latBar(latVals[i][0],latVals[i][1],maxLat);
    setH('stats-latency-bars',lb);
    setH('stats-route-latency',routeLatencyTable(req.top_routes||[]));
    setH('stats-proxy-pl-latency',proxyPLLatencyTable(req.top_proxy_plugins||[]));
    // Memory detail (collapsible)
    setH('stats-mem-detail',
      sc(mem.heap_alloc_mb+' MB','Heap Alloc',mem.heap_alloc_mb>512?'danger':mem.heap_alloc_mb>256?'warn':'')+
      sc(mem.sys_mb+' MB','Sys (RSS)')+sc(mem.heap_inuse_mb+' MB','Heap InUse')+
      sc(mem.heap_idle_mb+' MB','Heap Idle')+sc(mem.stack_inuse_mb+' MB','Stack')+
      sc(mem.heap_released_mb+' MB','Released to OS')+sc(mem.gc_count,'GC Cycles')+
      sc(mem.gc_pause_ms+' ms','GC Pause Total')+sc(mem.heap_objects,'Heap Objects'));
    // Subprocesses
    var proc=d.processes||{},ph='';
    // lampac-go self
    var selfLabel='lampac-go';
    if(ps[selfLabel]){
      ph+='<div class="proc-section"><div class="proc-section-title"><span class="icon">\u2699\ufe0f</span>Основной процесс</div><div class="proc-cards">';
      ph+=pcard('\u2699\ufe0f','lampac-go','PID '+osInfo.pid,true,[
        ['PID','#'+osInfo.pid],['Goroutines',''+d.goroutines],['Uptime',fmtUptime(d.uptime_sec)]
      ],[d.go_version],selfLabel,ps);
      ph+='</div></div>';
    }
    // Proxy sidecars
    var xr=proc.proxy||[];
    if(xr.length>0){
      ph+='<div class="proc-section"><div class="proc-section-title"><span class="icon">\ud83d\udee1</span>Прокси Сайдкары</div><div class="proc-cards">';
      for(var i=0;i<xr.length;i++){
        var x=xr[i];var lbl=x.label||'Proxy '+(i+1);
        ph+=pcard('\ud83c\udf10',lbl,'SOCKS5 прокси'+(x.engine?' ['+x.engine+']':''),x.alive,[
          ['PID','#'+x.pid],['SOCKS5',x.socks_addr],['Ядро',x.engine||'xray'],['Uptime',fmtUptime(x.uptime_sec)]
        ],x.balancers||[],'proxy:'+lbl,ps);
      }
      ph+='</div></div>';
    }
    // TorrServer
    var ts=proc.torrserver||{};
    if(ts.configured){
      ph+='<div class="proc-section"><div class="proc-section-title"><span class="icon">\ud83c\udfac</span>TorrServer</div><div class="proc-cards">';
      var tsRows=[];
      if(ts.inprocess){tsRows.push(['\u0422\u0438\u043F','\u0412\u0441\u0442\u0440\u043E\u0435\u043D\u043D\u044B\u0439']);tsRows.push(['\u041A\u044D\u0448',fmtRSS(ts.cache_size/1024)]);tsRows.push(['\u041F\u0440\u0435\u043B\u043E\u0430\u0434',fmtRSS(ts.preload_size/1024)]);tsRows.push(['\u0422\u043E\u0440\u0440\u0435\u043D\u0442\u044B',''+ts.active_torrents])}
      else if(ts.external){tsRows.push(['\u0410\u0434\u0440\u0435\u0441',ts.url]);tsRows.push(['\u0422\u0438\u043F','\u0412\u043D\u0435\u0448\u043D\u0438\u0439'])}
      else{tsRows.push(['\u041F\u043E\u0440\u0442',''+ts.port]);tsRows.push(['\u0410\u0434\u0440\u0435\u0441','127.0.0.1:'+ts.port]);tsRows.push(['\u0422\u0438\u043F','\u041B\u043E\u043A\u0430\u043B\u044C\u043D\u044B\u0439'])}
      ph+=pcard('\ud83d\udce1','TorrServer','\u0422\u043E\u0440\u0440\u0435\u043D\u0442-\u0441\u0442\u0440\u0438\u043C\u0438\u043D\u0433',ts.alive,tsRows,[]);
      ph+='</div></div>';
    }
    // yt-dlp
    var yt=proc.ytdlp||{};
    if(yt.available){
      var ytTags=[];
      if(yt.js_runtime)ytTags.push('JS: '+yt.js_runtime);
      if(yt.cookies)ytTags.push('\ud83c\udf6a Cookies');
      if(yt.ffmpeg)ytTags.push('\ud83c\udf9e FFmpeg');
      ph+='<div class="proc-section"><div class="proc-section-title"><span class="icon">\u25b6</span>YouTube</div><div class="proc-cards">';
      ph+=pcard('\ud83d\udcfa','yt-dlp','Загрузка видео с YouTube',true,[
        ['Версия',yt.version||'?'],['Runtime',yt.js_runtime||'нет'],['Mux задач',''+(yt.active_mux_jobs||0)],['Mux кэш',fmtRSS((yt.mux_cache_size||0)/1024)]
      ],ytTags);
      ph+='</div></div>';
    }
    // Custom balancers
    var cbl=d.custom_balancers||[];
    if(cbl.length>0){
      ph+='<div class="proc-section"><div class="proc-section-title"><span class="icon">\ud83e\udde9</span>Кастомные балансеры</div><div class="proc-cards">';
      for(var i=0;i<cbl.length;i++){
        var cb=cbl[i];var cbAlive=cb.state==='running';
        var cbDet=[['PID',cb.pid>0?'#'+cb.pid:'\u2014'],['Порт',''+cb.port],['Состояние',cb.state],['Uptime',fmtUptime(cb.uptime_sec||0)]];
        if(cb.last_error)cbDet.push(['Ошибка',cb.last_error]);
        ph+=pcard('\ud83e\udde9',cb.display_name||cb.name,'Кастомный балансер',cbAlive,cbDet,
          [cb.quality_badge].filter(Boolean),'custbal:'+cb.name,ps);
      }
      ph+='</div></div>';
    }
    // Transcoding
    var trans=d.transcoding||{};
    if(trans.enabled){
      var tj=trans.jobs||[];
      var deadCount=0;for(var i=0;i<tj.length;i++){if(tj[i].exit_code!==-1)deadCount++}
      ph+='<div class="proc-section"><div class="proc-section-title"><span class="icon">\ud83d\udd04</span>Транскодирование ('+tj.length+'/'+trans.max_jobs+')';
      if(deadCount>0)ph+=' <button class="btn-sm btn-danger" onclick="cleanupDeadJobs()" title="Удалить все мёртвые процессы">\ud83d\uddd1 Очистить мёртвые ('+deadCount+')</button>';
      ph+=' <button class="btn-sm" onclick="runTransSelftest()" title="Запустить диагностику">\ud83e\uddea Self-test</button>';
      ph+='</div>';

      // P1/P2 status row — HW accel + probe cache + scheduler.
      var hw=trans.hw_accel||{};
      var pc=trans.probe_cache||{};
      var sch=trans.scheduler||{};
      var hits=pc.hits||0, misses=pc.misses||0, hitPct=(hits+misses>0)?Math.round(hits*100/(hits+misses)):0;
      // Inline-styled chips (no .chip-* CSS required).
      var _chipStyles={
        ok  :'background:rgba(62,207,142,.15);color:#3ecf8e;border:1px solid rgba(62,207,142,.35)',
        warn:'background:rgba(245,169,62,.15);color:#f5a93e;border:1px solid rgba(245,169,62,.35)',
        dim :'background:rgba(136,153,170,.12);color:#8899aa;border:1px solid rgba(136,153,170,.3)'
      };
      var _chip=function(kind,txt,tip){
        var base='padding:3px 8px;border-radius:10px;font-size:11px;font-weight:500;display:inline-block;';
        return '<span style="'+base+(_chipStyles[kind]||_chipStyles.dim)+'" title="'+esc(tip||'')+'">'+txt+'</span>';
      };
      var statusRow='<div style="display:flex;gap:6px;flex-wrap:wrap;padding:4px 10px 10px;font-size:12px">';
      // HW accel chip.
      var hwActive=hw.active===true, hwKind=hw.kind||'', hwDetails=hw.device?(' '+hw.device):'';
      var hwClass=hwActive?'ok':(hw.detected?'warn':'dim');
      var hwText=hwActive?('\u26a1 HW: '+hwKind.toUpperCase()+hwDetails):(hw.detected?'\u26a0 HW: '+hwKind+' (off)':'\ud83d\udda5 CPU only');
      statusRow+=_chip(hwClass,esc(hwText),'Hardware acceleration');
      // Probe cache chip.
      if(pc.enabled){
        statusRow+=_chip('ok','\ud83d\udcbe Probe: '+(pc.size||0)+' \u00b7 '+hitPct+'% hit','ffprobe LRU cache');
      }
      // Scheduler chip.
      if(sch.capacity){
        var rej=sch.rejections||0, ev=sch.disk_evicted||0;
        var schClass=(rej>0||ev>0)?'warn':'ok';
        var schText='\ud83d\udcc8 Queue: '+(sch.in_use||0)+'/'+sch.capacity;
        if(rej>0)schText+=' \u00b7 rej '+rej;
        if(ev>0)schText+=' \u00b7 evict '+ev;
        statusRow+=_chip(schClass,esc(schText),'Transcoding scheduler');
      }
      // ffmpeg version chip.
      if(trans.ffmpeg_major)statusRow+=_chip('dim','ffmpeg v'+trans.ffmpeg_major,'');
      statusRow+='</div>';
      ph+=statusRow;

      ph+='<div class="proc-cards">';
      if(tj.length>0){
        for(var i=0;i<tj.length;i++){
          var j=tj[i];var jAlive=j.exit_code===-1;
          var startDate=new Date(j.started*1000);
          var modeLabel=j.mode?j.mode:(j.live?'live':'vod');
          var details=[
            ['PID',j.pid>0?'#'+j.pid:'\u2014'],
            ['Mode',modeLabel+(j.best_effort?' (best-effort)':'')],
            ['Seg',j.last_seg_index>=0?('#'+j.last_seg_index):'\u2014'],
            ['Audio','#'+(j.selected_audio||0)],
            ['Запущен',startDate.toLocaleTimeString()],
            ['Источник',(j.source||'').substring(0,40)]
          ];
          if(typeof j.min_client_pos==='number')details.push(['Min pos',Math.round(j.min_client_pos)+'s']);
          if(j.warning)details.push(['\u26a0',j.warning.substring(0,40)]);
          var badges=[j.live?'LIVE':'VOD'];
          if(modeLabel==='remux')badges.push('REMUX');
          else if(modeLabel==='hw-transcode')badges.push('HW');
          else if(modeLabel==='sw-transcode')badges.push('SW');
          else if(modeLabel==='audio-only')badges.push('AUDIO');
          if(j.best_effort)badges.push('BE');
          var cardHtml=pcard('\ud83c\udf9e','FFmpeg #'+j.id.substring(0,6),j.live?'Live':'VOD',jAlive,details,badges,'ffmpeg:'+j.stream_id,ps);
          // Inject kill button before closing </div> of proc-card
          var killBtn='<div class="proc-card-actions">';
          if(!jAlive)killBtn+='<button class="btn-sm btn-danger" onclick="killTransJob(\''+j.id+'\')">Удалить</button>';
          else killBtn+='<button class="btn-sm btn-warn" onclick="killTransJob(\''+j.id+'\')">Остановить</button>';
          killBtn+='</div>';
          cardHtml=cardHtml.slice(0,-6)+killBtn+'</div>';
          ph+=cardHtml;
        }
      } else {
        ph+='<div style="color:var(--text-dim);font-size:12px;padding:8px">Нет активных задач</div>';
      }
      ph+='</div></div>';
    }
    setH('stats-processes',ph||'<div class="empty">Нет субпроцессов</div>');
    // Chrome section
    var chrome=d.chrome||{};
    var chromeHtml;
    if(chrome.allocator_active){
      var cPct=chrome.max_concurrent>0?(chrome.active_sessions/chrome.max_concurrent*100):0;
      chromeHtml='<div class="chrome-card"><div class="chrome-icon">\ud83c\udf10</div><div class="chrome-info"><div class="cn">Headless Chrome ('+(chrome.engine||'chromedp')+')</div><div class="cs">Сессии: '+chrome.active_sessions+' / '+chrome.max_concurrent+' \u00b7 Свободно: '+chrome.available_slots+'</div><div class="chrome-meter"><div class="chrome-fill" style="width:'+Math.max(2,cPct)+'%"></div></div></div></div>'+
      '<div style="margin-top:12px;display:flex;gap:12px;flex-wrap:wrap;align-items:flex-end">'+
      '<div><label style="font-size:11px;color:#8899aa">Макс. сессий</label><br><input type="number" id="bp-max-concurrent" value="'+chrome.max_concurrent+'" min="1" max="64" style="width:70px" class="input-sm"></div>'+
      '<div><label style="font-size:11px;color:#8899aa">Макс. кэш</label><br><input type="number" id="bp-cache-max" value="'+(chrome.stream_cache_max||2000)+'" min="100" max="50000" step="100" style="width:90px" class="input-sm"></div>'+
      '<div><label style="font-size:11px;color:#8899aa">TTL кэша (ч)</label><br><input type="number" id="bp-cache-ttl" value="'+(chrome.stream_cache_ttl_h||8)+'" min="1" max="72" style="width:60px" class="input-sm"></div>'+
      '<div><button class="btn btn-primary btn-sm" onclick="saveBrowserPool()">Применить</button></div>'+
      '</div>';
    } else {
      chromeHtml='<div style="color:var(--text-dim);font-size:12px">Chrome не инициализирован (запустится при первом запросе Mirage)</div>';
    }
    chromeHtml+='<div id="bp-engine-card" style="margin-top:16px"></div>';
    setH('stats-chrome',chromeHtml);
    loadBrowserEngineCard();
  }).catch(function(e){
    var el=document.getElementById('srv-hostname');if(el)el.textContent='Ошибка: '+e.message;
  });
  clearInterval(statsTimer);
  if(document.getElementById('auto-refresh').checked){
    statsTimer=setInterval(function(){
      if(document.getElementById('panel-server').classList.contains('active')&&document.getElementById('auto-refresh').checked)loadServerStats();
      else clearInterval(statsTimer);
    },5000);
  }
};
// Restart modal
window.confirmRestart=function(){document.getElementById('srv-restart-modal').classList.add('open')};
window.cancelRestart=function(){document.getElementById('srv-restart-modal').classList.remove('open')};
window.doRestart=function(){
  document.getElementById('srv-restart-modal').classList.remove('open');
  var btn=document.getElementById('srv-restart-btn');btn.disabled=true;btn.textContent='Перезапуск...';
  post('/api/restart',{}).then(function(r){
    if(r.ok){toast('Сервер перезапускается...');
      var attempts=0;var poll=setInterval(function(){
        attempts++;if(attempts>30){clearInterval(poll);toast('Сервер не ответил','error');btn.disabled=false;btn.textContent='\u21bb Перезапустить';return}
        api('/api/stats').then(function(){clearInterval(poll);toast('Сервер перезапущен!');btn.disabled=false;btn.textContent='\u21bb Перезапустить';loadServerStats()}).catch(function(){});
      },2000);
    } else {toast(r.error||'Ошибка','error');btn.disabled=false;btn.textContent='\u21bb Перезапустить'}
  }).catch(function(){toast('Ошибка подключения','error');btn.disabled=false;btn.textContent='\u21bb Перезапустить'});
};

// --- Transcoding job management ---
window.killTransJob=function(jobId){
  post('/api/transcoding/kill/'+jobId,{}).then(function(r){
    if(r.ok){toast('Процесс остановлен');loadServerStats()}
    else toast(r.error||'Ошибка','error');
  }).catch(function(){toast('Ошибка подключения','error')});
};
// Self-test — calls /transcoding/selftest and shows results in a modal.
window.runTransSelftest=function(){
  toast('\u0417\u0430\u043f\u0443\u0441\u043a self-test...');
  api('/transcoding/selftest').then(function(r){
    if(!r||!r.checks){toast('\u041d\u0435\u0442 \u0434\u0430\u043d\u043d\u044b\u0445','error');return}
    var html='<div style="padding:16px;max-width:520px">';
    html+='<h3 style="margin:0 0 12px">'+(r.ok?'\u2705':'\u274c')+' Self-test</h3>';
    html+='<table style="width:100%;font-size:13px;border-collapse:collapse">';
    for(var i=0;i<r.checks.length;i++){
      var c=r.checks[i];
      var icon=c.passed?'\u2705':'\u274c';
      html+='<tr><td style="padding:6px 4px;vertical-align:top">'+icon+'</td>';
      html+='<td style="padding:6px 4px;font-weight:500">'+esc(c.name)+'</td>';
      html+='<td style="padding:6px 4px;color:var(--text-dim);font-size:12px;word-break:break-word">'+esc(c.details||'')+'</td></tr>';
    }
    html+='</table>';
    html+='<div style="margin-top:16px;text-align:right"><button class="btn btn-sm" onclick="closeModal(\'selftest-modal\')">\u041a\u043e\u043f\u0438\u044f</button></div>';
    html+='</div>';
    // Render into a lightweight modal.  Reuse existing srv-restart-modal
    // styling if present; otherwise create a floating div.
    var mod=document.getElementById('selftest-modal');
    if(!mod){
      mod=document.createElement('div');
      mod.id='selftest-modal';
      mod.className='modal open';
      mod.style.cssText='position:fixed;inset:0;background:rgba(0,0,0,.6);display:flex;align-items:center;justify-content:center;z-index:9999';
      mod.onclick=function(e){if(e.target===mod)mod.remove()};
      document.body.appendChild(mod);
    }
    mod.innerHTML='<div style="background:var(--panel-bg,#1a1a1a);color:var(--text,#fff);border-radius:8px;box-shadow:0 8px 32px rgba(0,0,0,.5)">'+html+'</div>';
  }).catch(function(e){toast('Ошибка self-test: '+e.message,'error')});
};
window.closeModal=function(id){var m=document.getElementById(id);if(m)m.remove()};
window.cleanupDeadJobs=function(){
  post('/api/transcoding/cleanup',{}).then(function(r){
    if(r.ok){toast('Удалено мёртвых: '+r.removed);loadServerStats()}
    else toast(r.error||'Ошибка','error');
  }).catch(function(){toast('Ошибка подключения','error')});
};
window.saveBrowserPool=function(){
  var mc=parseInt(document.getElementById('bp-max-concurrent').value)||8;
  var cm=parseInt(document.getElementById('bp-cache-max').value)||2000;
  var ct=parseInt(document.getElementById('bp-cache-ttl').value)||8;
  post('/api/browserpool',{max_concurrent:mc,stream_cache_max:cm,stream_cache_ttl_h:ct}).then(function(r){
    if(r.ok){toast('Browser Pool обновлён: сессий='+r.max_concurrent+', кэш='+r.stream_cache_max+', TTL='+r.stream_cache_ttl_h+'ч');loadServerStats()}
    else toast(r.error||'Ошибка','error');
  }).catch(function(){toast('Ошибка подключения','error')});
};

// loadBrowserEngineCard renders the engine selector + per-balancer
// override table into #bp-engine-card. It uses the same /api/browserpool
// payload so a single network round-trip drives both blocks.
window.loadBrowserEngineCard=function(){
  api('/api/browserpool').then(function(r){
    var card=document.getElementById('bp-engine-card');
    if(!card)return;
    var avail=r.available_engines||['chromedp'];
    var cur=r.engine||'chromedp';
    var status=r.engine_status||{};
    var bals=r.balancers||[];
    var overrides=r.balancer_engines||{};

    var html='<div class="cluster-block" style="padding:14px;background:rgba(255,255,255,.03);border-radius:8px">';
    html+='<h3 style="margin:0 0 10px;font-size:14px">🧩 Движок браузера</h3>';
    html+='<div style="font-size:11px;color:var(--text-dim);margin-bottom:8px">Глобальный движок применяется ко всем балансерам, использующим Chrome. Переопределения ниже работают поверх. Изменения вступают в силу для новых сессий; для гарантии — перезапустите lampac.</div>';

    // Global dropdown
    html+='<div style="display:flex;gap:12px;align-items:center;margin-bottom:14px">';
    html+='<label style="font-size:12px">По умолчанию:</label>';
    html+='<select id="bp-engine-global" class="input-sm" style="min-width:160px">';
    for(var i=0;i<avail.length;i++){
      var name=avail[i];
      var err=status[name]||'';
      var label=name+(err?' (недоступно)':'');
      var sel=name===cur?' selected':'';
      var dis=err?' disabled':'';
      html+='<option value="'+name+'"'+sel+dis+'>'+label+'</option>';
    }
    html+='</select>';
    html+='<button class="btn btn-primary btn-sm" onclick="saveBrowserEngine()">Применить</button>';
    html+='</div>';

    // Available engine status hints. For engines that need a one-time
    // install (playwright) show an Install button next to the warning.
    var statusRows=[];
    for(var j=0;j<avail.length;j++){
      var n=avail[j];var s=status[n]||'';
      if(!s)continue;
      var row='<div style="font-size:11px;color:#dd8866;margin-bottom:4px">• '+n+': '+esc(s);
      if(n==='playwright'){
        row+=' <button type="button" class="btn btn-sm btn-primary" style="cursor:pointer;margin-left:8px" onclick="installBrowserEngine(\''+n+'\',this)">Установить</button>';
      }
      row+='</div>';
      statusRows.push(row);
    }
    if(statusRows.length){
      html+='<div style="margin-bottom:12px">'+statusRows.join('')+'</div>';
    }

    // Per-balancer overrides table
    html+='<details style="margin-top:8px"><summary style="cursor:pointer;font-size:12px;color:var(--text-dim)">Переопределения по балансерам</summary>';
    html+='<table style="width:100%;margin-top:10px;font-size:12px;border-collapse:collapse">';
    html+='<thead><tr><th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-dim)">Балансер</th><th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-dim)">Движок</th></tr></thead><tbody>';
    for(var k=0;k<bals.length;k++){
      var bal=bals[k];
      var ov=overrides[bal]||'';
      html+='<tr><td style="padding:4px 6px">'+bal+'</td><td style="padding:4px 6px"><select class="input-sm bp-bal-override" data-bal="'+bal+'" style="min-width:140px">';
      html+='<option value="">— как глобальный —</option>';
      for(var m=0;m<avail.length;m++){
        var nm=avail[m];var er=status[nm]||'';
        if(er)continue;
        html+='<option value="'+nm+'"'+(nm===ov?' selected':'')+'>'+nm+'</option>';
      }
      html+='</select></td></tr>';
    }
    html+='</tbody></table>';
    html+='<div style="margin-top:8px;text-align:right"><button type="button" class="btn btn-primary btn-sm" style="cursor:pointer" onclick="saveBrowserBalancerOverrides()">Сохранить переопределения</button></div>';
    html+='</details>';
    html+='</div>';
    card.innerHTML=html;
  }).catch(function(){});
};

window.saveBrowserEngine=function(){
  var sel=document.getElementById('bp-engine-global');
  if(!sel)return;
  post('/api/browserpool',{engine:sel.value}).then(function(r){
    if(r.ok){toast('Движок изменён: '+r.engine+' (рестарт рекомендуется)');loadBrowserEngineCard()}
    else toast(r.error||'Ошибка','error');
  }).catch(function(){toast('Ошибка подключения','error')});
};

// installBrowserEngine triggers a one-time install of an engine's
// runtime dependency (Playwright's Node driver + Chromium). Blocks
// the calling UI button for the duration of the download. On success
// the engine card is reloaded so the option becomes selectable.
window.installBrowserEngine=function(name,btn){
  if(btn){btn.disabled=true;btn.textContent='Устанавливаю…'}
  toast('Установка '+name+': скачивается ~150MB, может занять несколько минут');
  post('/api/browser/install',{engine:name}).then(function(r){
    if(r.ok){
      toast('Движок '+name+' установлен');
      loadBrowserEngineCard();
    } else {
      if(btn){btn.disabled=false;btn.textContent='Установить'}
      toast(r.error||'Ошибка установки','error');
    }
  }).catch(function(){
    if(btn){btn.disabled=false;btn.textContent='Установить'}
    toast('Ошибка подключения','error');
  });
};

window.saveBrowserBalancerOverrides=function(){
  var nodes=document.querySelectorAll('.bp-bal-override');
  var map={};
  for(var i=0;i<nodes.length;i++){
    var bal=nodes[i].getAttribute('data-bal');
    var val=nodes[i].value;
    if(bal&&val)map[bal]=val;
  }
  post('/api/browserpool',{balancer_engines:map}).then(function(r){
    if(r.ok){toast('Переопределения сохранены');loadBrowserEngineCard()}
    else toast(r.error||'Ошибка','error');
  }).catch(function(){toast('Ошибка подключения','error')});
};

// --- Proxy (multi-entry with drag & drop) ---
var _proxyState={entries:[],availableBalancers:[],dragBal:null};

function detectProtocol(uri){
  if(!uri)return '';
  var s=uri.toLowerCase();
  if(s.indexOf('vless://')===0)return 'VLESS';
  if(s.indexOf('vmess://')===0)return 'VMess';
  if(s.indexOf('trojan://')===0)return 'Trojan';
  if(s.indexOf('ss://')===0)return 'SS';
  if(s.indexOf('hysteria2://')===0||s.indexOf('hy2://')===0)return 'Hy2';
  if(s.indexOf('tuic://')===0)return 'TUIC';
  if(s.indexOf('wg://')===0||s.indexOf('wireguard://')===0)return 'WG';
  return '';
}
function protocolColor(proto){
  var m={'VLESS':'#4fc3f7','VMess':'#81c784','Trojan':'#ffb74d','SS':'#ba68c8','Hy2':'#f06292','TUIC':'#4dd0e1','WG':'#aed581'};
  return m[proto]||'#94a3b8';
}

window.loadProxy=function(){
  api('/api/proxy').then(function(d){
    _proxyState.availableBalancers=d.available_balancers||[];
    _proxyState.entries=(d.entries||[]).map(function(e){
      return {uri:e.uri||'',label:e.label||'',balancers:(e.balancers||[]).slice(),active:!!e.active,engine:e.engine||''};
    });
    renderProxy();
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
  // Load FlareSolverr settings + active sessions (silent on failure).
  if(typeof loadFlareSolverr==='function')loadFlareSolverr();
};

function renderProxy(){
  var container=document.getElementById('proxy-servers');
  // Collect assigned balancers
  var assigned={};
  _proxyState.entries.forEach(function(e){(e.balancers||[]).forEach(function(b){assigned[b]=true})});

  container.innerHTML=_proxyState.entries.map(function(entry,idx){
    var flag=entry.label.match(/[\uD83C][\uDDE6-\uDDFF][\uD83C][\uDDE6-\uDDFF]/);
    flag=flag?flag[0]:'🌐';
    var name=entry.label.replace(/[\uD83C][\uDDE6-\uDDFF][\uD83C][\uDDE6-\uDDFF]\s*/,'').trim()||'Сервер '+(idx+1);
    var hostMatch=entry.uri.match(/@([^:?#]+)/);
    var host=hostMatch?hostMatch[1]:'не настроен';
    var dotClass=entry.active?'on':'off';

    var balsHTML=(entry.balancers||[]).map(function(b){
      return '<div class="bal-chip" draggable="true" data-bal="'+esc(b)+'" data-server="'+idx+'">'+esc(b)+'<span class="remove-bal" onclick="removeBalFromServer('+idx+',\''+esc(b)+'\')" title="Убрать">&times;</span></div>';
    }).join('');
    var emptyClass=(entry.balancers||[]).length===0?' empty-hint':'';

    return '<div class="proxy-server'+(entry.active?' active':'')+'" data-idx="'+idx+'">'+
      '<div class="proxy-server-head" onclick="toggleServerBody('+idx+')">'+
        '<div class="proxy-flag">'+flag+'</div>'+
        '<div class="proxy-server-info"><div class="proxy-server-label">'+esc(name)+'</div><div class="proxy-server-ip">'+esc(host)+'</div></div>'+
        '<div class="proxy-server-status"><span class="proxy-dot '+dotClass+'"></span><span class="chevron" id="proxy-chev-'+idx+'">▶</span></div>'+
      '</div>'+
      '<div class="proxy-server-body" id="proxy-body-'+idx+'">'+
        '<div style="display:flex;gap:8px;align-items:center;margin-bottom:6px">'+
          '<label style="font-size:12px;color:var(--text-muted)">Ядро:</label>'+
          '<select class="proxy-engine-select" id="proxy-engine-'+idx+'" style="background:var(--surface2);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:4px;padding:2px 6px;font-size:12px">'+
            '<option value=""'+(entry.engine===''?' selected':'')+'>Авто</option>'+
            '<option value="xray"'+(entry.engine==='xray'?' selected':'')+'>xray</option>'+
            '<option value="mihomo"'+(entry.engine==='mihomo'?' selected':'')+'>mihomo</option>'+
          '</select>'+
          (function(){var p=detectProtocol(entry.uri);if(!p)return '';var c=protocolColor(p);return '<span id="proto-badge-'+idx+'" style="font-size:10px;font-weight:600;padding:1px 7px;border-radius:9px;background:'+c+'22;color:'+c+';border:1px solid '+c+'44">'+p+'</span>'})()+
        '</div>'+
        '<div class="proxy-uri-row"><label style="font-size:12px;color:var(--text-muted)">URI</label><input class="proxy-uri-input" id="proxy-uri-'+idx+'" value="'+escAttr(entry.uri)+'" placeholder="vless:// | vmess:// | trojan:// | ss:// | hy2:// | tuic:// | wg://" oninput="updateProtoBadge('+idx+')"></div>'+
        '<div class="form-row" style="margin-top:8px"><label style="font-size:12px;color:var(--text-muted);width:60px">Метка</label><input class="proxy-label-input" id="proxy-label-'+idx+'" value="'+escAttr(entry.label)+'" placeholder="🇷🇺 Россия"></div>'+
        '<div style="margin-top:12px"><label style="font-size:12px;color:var(--text-muted)">Привязанные балансеры</label>'+
          '<div class="proxy-balancers-zone'+emptyClass+'" id="proxy-zone-'+idx+'" data-server="'+idx+'">'+balsHTML+'</div>'+
        '</div>'+
        '<div class="proxy-test-row">'+
          '<button class="btn btn-warning btn-sm" onclick="testProxyEntry('+idx+')">Тест</button>'+
          '<button class="btn btn-danger btn-sm" onclick="removeProxyServer('+idx+')">Удалить сервер</button>'+
          '<span class="proxy-test-result" id="proxy-test-'+idx+'"></span>'+
        '</div>'+
      '</div>'+
    '</div>';
  }).join('');

  // Available (unassigned) balancers
  var availChips=document.getElementById('proxy-available-chips');
  var free=_proxyState.availableBalancers.filter(function(b){return !assigned[b]});
  availChips.innerHTML=free.map(function(b){
    return '<div class="bal-chip" draggable="true" data-bal="'+esc(b)+'" data-server="-1">'+esc(b)+'</div>';
  }).join('');
  if(free.length===0)availChips.innerHTML='<span style="color:var(--text-dim);font-size:12px;padding:4px">Все балансеры назначены</span>';

  initDragDrop();
}

function escAttr(s){return (s||'').replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;')}

window.toggleServerBody=function(idx){
  var body=document.getElementById('proxy-body-'+idx);
  var chev=document.getElementById('proxy-chev-'+idx);
  if(body.classList.contains('open')){body.classList.remove('open');chev.classList.remove('open')}
  else{body.classList.add('open');chev.classList.add('open')}
};

window.addProxyServer=function(){
  _proxyState.entries.push({uri:'',label:'',balancers:[],active:false,engine:''});
  renderProxy();
  // Open the new one
  var idx=_proxyState.entries.length-1;
  toggleServerBody(idx);
};

window.addWARPEntry=function(){
  // WARP is registered + dialed natively by ProxyCore (userspace WireGuard,
  // no sidecar binary). Route the click straight there instead of saving a
  // wg://warp entry into proxy.vless.entries, which would force the mihomo
  // sidecar engine and fail on hosts without the mihomo binary.
  if(!confirm('🌐 Cloudflare WARP будет добавлен через ProxyCore (нативный WireGuard, без сайдкара).\n\nПродолжить?'))return;
  post('/api/proxycore/entries',{action:'add',uri:'wg://warp',label:'🌐 Cloudflare WARP',balancers:[],engine:'proxycore'}).then(function(r){
    if(!r||!r.ok){toast((r&&r.error)||'Ошибка добавления','error');return}
    if(r.reload_error){toast('Добавлен, но reload упал: '+r.reload_error,'error');return}
    toast('🌐 WARP добавлен — см. вкладку ProxyCore');
  }).catch(function(e){toast(e.message,'error')});
};

window.removeProxyServer=function(idx){
  _proxyState.entries.splice(idx,1);
  renderProxy();
};

window.removeBalFromServer=function(serverIdx,bal){
  var e=_proxyState.entries[serverIdx];
  if(!e)return;
  e.balancers=e.balancers.filter(function(b){return b!==bal});
  renderProxy();
};

function syncStateFromDOM(){
  _proxyState.entries.forEach(function(entry,idx){
    var uriEl=document.getElementById('proxy-uri-'+idx);
    var labelEl=document.getElementById('proxy-label-'+idx);
    var engineEl=document.getElementById('proxy-engine-'+idx);
    if(uriEl)entry.uri=uriEl.value.trim();
    if(labelEl)entry.label=labelEl.value.trim();
    if(engineEl)entry.engine=engineEl.value;
  });
}

window.updateProtoBadge=function(idx){
  var uriEl=document.getElementById('proxy-uri-'+idx);
  var badgeEl=document.getElementById('proto-badge-'+idx);
  if(!uriEl)return;
  var p=detectProtocol(uriEl.value);
  if(!p){if(badgeEl)badgeEl.style.display='none';return}
  if(!badgeEl){
    // Badge doesn't exist yet, insert it
    var engineSel=document.getElementById('proxy-engine-'+idx);
    if(engineSel){
      var span=document.createElement('span');
      span.id='proto-badge-'+idx;
      var c=protocolColor(p);
      span.style.cssText='font-size:10px;font-weight:600;padding:1px 7px;border-radius:9px;background:'+c+'22;color:'+c+';border:1px solid '+c+'44';
      span.textContent=p;
      engineSel.parentNode.appendChild(span);
    }
    return;
  }
  var c=protocolColor(p);
  badgeEl.style.display='';
  badgeEl.style.background=c+'22';
  badgeEl.style.color=c;
  badgeEl.style.borderColor=c+'44';
  badgeEl.textContent=p;
};

function initDragDrop(){
  // Make all bal-chips draggable
  document.querySelectorAll('.bal-chip[draggable]').forEach(function(chip){
    chip.addEventListener('dragstart',function(e){
      _proxyState.dragBal=chip.dataset.bal;
      chip.classList.add('dragging');
      e.dataTransfer.effectAllowed='move';
      e.dataTransfer.setData('text/plain',chip.dataset.bal);
    });
    chip.addEventListener('dragend',function(){
      chip.classList.remove('dragging');
      _proxyState.dragBal=null;
      document.querySelectorAll('.proxy-balancers-zone').forEach(function(z){z.classList.remove('drag-over')});
    });
  });
  // Make zones droppable
  document.querySelectorAll('.proxy-balancers-zone').forEach(function(zone){
    zone.addEventListener('dragover',function(e){e.preventDefault();e.dataTransfer.dropEffect='move';zone.classList.add('drag-over')});
    zone.addEventListener('dragleave',function(){zone.classList.remove('drag-over')});
    zone.addEventListener('drop',function(e){
      e.preventDefault();zone.classList.remove('drag-over');
      var bal=e.dataTransfer.getData('text/plain')||_proxyState.dragBal;
      if(!bal)return;
      syncStateFromDOM();
      var targetServer=parseInt(zone.dataset.server);
      // Remove bal from all servers
      _proxyState.entries.forEach(function(entry){entry.balancers=entry.balancers.filter(function(b){return b!==bal})});
      // Add to target
      if(targetServer>=0&&_proxyState.entries[targetServer]){
        _proxyState.entries[targetServer].balancers.push(bal);
      }
      _proxyState.dragBal=null;
      renderProxy();
    });
  });
  // Also make the available zone droppable (to unassign)
  var availZone=document.getElementById('proxy-available-chips');
  if(availZone){
    availZone.addEventListener('dragover',function(e){e.preventDefault();e.dataTransfer.dropEffect='move';availZone.style.borderColor='var(--accent)'});
    availZone.addEventListener('dragleave',function(){availZone.style.borderColor=''});
    availZone.addEventListener('drop',function(e){
      e.preventDefault();availZone.style.borderColor='';
      var bal=e.dataTransfer.getData('text/plain')||_proxyState.dragBal;
      if(!bal)return;
      syncStateFromDOM();
      _proxyState.entries.forEach(function(entry){entry.balancers=entry.balancers.filter(function(b){return b!==bal})});
      _proxyState.dragBal=null;
      renderProxy();
    });
  }
}

window.saveProxy=function(){
  syncStateFromDOM();
  var entries=_proxyState.entries.filter(function(e){return e.uri}).map(function(e){
    return {uri:e.uri,balancers:e.balancers,label:e.label,engine:e.engine||''};
  });
  post('/api/proxy',{action:'save',entries:entries}).then(function(r){
    if(r.ok){toast('Настройки прокси сохранены');loadProxy()}
    else toast(r.error||'Ошибка','error');
  });
};

window.reloadProxy=function(){
  syncStateFromDOM();
  var entries=_proxyState.entries.filter(function(e){return e.uri}).map(function(e){
    return {uri:e.uri,balancers:e.balancers,label:e.label,engine:e.engine||''};
  });
  // Save first, then reload
  post('/api/proxy',{action:'save',entries:entries}).then(function(r){
    if(!r.ok){toast(r.error||'Ошибка сохранения','error');return}
    post('/api/proxy',{action:'reload'}).then(function(r2){
      if(r2.ok){toast('Прокси перезагружены! Изменения применены.');loadProxy()}
      else toast(r2.error||'Ошибка reload','error');
    });
  });
};

// ──────────────────────────────────────────────────────────────
// FlareSolverr (anti-bot solver) — UI on Proxy tab
// ──────────────────────────────────────────────────────────────
// _fsState.user — admin-removable entries, the list we PUT back on save.
// _fsState.system — code-required entries (turbo, etc.); shown as locked chips,
//                   never sent on PUT (the server already knows about them).
var _fsState={url:'',user:[],system:[],available:[]};

window.loadFlareSolverr=function(){
  api('/api/flaresolverr').then(function(d){
    _fsState.url=d.url||'';
    // Prefer the annotated payload (new API). Fall back to the flat balancers
    // list when serving an older backend so the panel stays usable.
    if(Array.isArray(d.balancer_entries)&&d.balancer_entries.length>=0&&Array.isArray(d.user_balancers)){
      _fsState.system=d.balancer_entries.filter(function(e){return e&&e.system}).map(function(e){return e.name});
      _fsState.user=(d.user_balancers||[]).slice();
    }else{
      _fsState.system=[];
      _fsState.user=(d.balancers||[]).slice();
    }
    _fsState.available=(d.available_balancers||[]).slice();
    document.getElementById('fs-url').value=_fsState.url;
    fsRenderBals();
    fsRenderSessions(d.sessions||[]);
  }).catch(function(e){
    // FS panel is optional; silent fail keeps Proxy tab usable when API is wonky
    console.warn('FS load failed:',e&&e.message);
  });
};

function fsRenderBals(){
  var tags=document.getElementById('fs-bals-tags');
  if(!tags)return;
  if(_fsState.user.length===0&&_fsState.system.length===0){
    tags.innerHTML='<span style="font-size:12px;color:var(--text-dim);padding:6px 4px">нет балансёров — добавьте через поиск ниже</span>';
    return;
  }
  var html='';
  // System chips first, visually distinct, no × button.
  _fsState.system.forEach(function(b){
    html+='<span class="fs-chip fs-chip-system" title="Системный балансёр — FlareSolverr обязателен для работы, удалить нельзя" style="display:inline-flex;align-items:center;gap:5px;padding:3px 10px;border-radius:14px;background:rgba(124,108,255,0.12);border:1px solid rgba(124,108,255,0.4);color:#9b8cff;font-size:11px;font-weight:600">'+esc(b)+'<span style="font-size:9px;opacity:.75;letter-spacing:.5px">SYSTEM</span></span>';
  });
  _fsState.user.forEach(function(b){
    html+='<span class="fs-chip" style="display:inline-flex;align-items:center;gap:5px;padding:3px 4px 3px 10px;border-radius:14px;background:rgba(0,212,170,0.12);border:1px solid rgba(0,212,170,0.35);color:#00d4aa;font-size:11px;font-weight:600">'+esc(b)+'<span onclick="fsRemoveBal(\''+esc(b)+'\')" style="cursor:pointer;padding:0 4px;border-radius:8px;font-size:13px;line-height:1;opacity:.8" title="Убрать">&times;</span></span>';
  });
  tags.innerHTML=html;
}

function fsRenderSessions(sessions){
  var box=document.getElementById('fs-sessions');
  var cnt=document.getElementById('fs-sessions-count');
  if(cnt)cnt.textContent=sessions.length;
  if(!box)return;
  if(sessions.length===0){
    box.innerHTML='<div style="font-size:11px;color:var(--text-dim);padding:8px 4px;font-style:italic">Нет активных сессий — challenge ещё не решался либо все истекли.</div>';
    return;
  }
  var now=Math.floor(Date.now()/1000);
  box.innerHTML=sessions.sort(function(a,b){return (b.expires_at_unix||0)-(a.expires_at_unix||0)}).map(function(s){
    var ttl=Math.max(0,(s.expires_at_unix||0)-now);
    var ttlStr=ttl<60?ttl+' c':Math.floor(ttl/60)+' мин';
    var solvedAgo=Math.max(0,now-(s.solved_at_unix||now));
    var solvedStr=solvedAgo<60?solvedAgo+' c назад':Math.floor(solvedAgo/60)+' мин назад';
    return '<div style="display:flex;align-items:center;gap:10px;padding:8px 12px;background:var(--bg);border:1px solid var(--border);border-radius:8px;font-size:12px">'+
      '<span style="font-family:\'Courier New\',monospace;color:var(--accent);font-weight:600">'+esc(s.host||'')+'</span>'+
      '<span style="color:var(--text-muted)">решено '+solvedStr+'</span>'+
      '<span style="flex:1"></span>'+
      '<span style="font-size:10px;padding:2px 7px;border-radius:8px;background:'+(ttl>300?'rgba(0,212,170,0.15);color:#00d4aa':ttl>60?'rgba(255,193,7,0.15);color:#ffc107':'rgba(255,107,107,0.15);color:#ff6b6b')+'">истекает через '+ttlStr+'</span>'+
      '<button class="btn btn-sm" onclick="fsResetSession(\''+esc(s.host||'')+'\')" style="font-size:10px;padding:3px 8px" title="Сбросить кэшированную сессию">⟳</button>'+
    '</div>';
  }).join('');
}

window.fsBalsSearch=function(){
  var inp=document.getElementById('fs-bals-input');
  var dd=document.getElementById('fs-bals-dropdown');
  if(!inp||!dd)return;
  var q=inp.value.trim().toLowerCase();
  var assigned={};
  _fsState.user.forEach(function(b){assigned[b.toLowerCase()]=true});
  _fsState.system.forEach(function(b){assigned[b.toLowerCase()]=true});
  var candidates=_fsState.available.filter(function(b){
    if(assigned[b.toLowerCase()])return false;
    if(!q)return true;
    return b.toLowerCase().indexOf(q)>=0;
  }).slice(0,30);
  if(candidates.length===0){
    dd.style.display='none';return;
  }
  dd.innerHTML=candidates.map(function(b){
    return '<div onmousedown="fsAddBal(\''+esc(b)+'\')" style="padding:8px 12px;cursor:pointer;border-bottom:1px solid var(--border);font-size:12px;transition:background .15s" onmouseover="this.style.background=\'var(--bg)\'" onmouseout="this.style.background=\'\'">'+esc(b)+'</div>';
  }).join('');
  dd.style.display='block';
};

window.fsBalsHideDropdown=function(){
  var dd=document.getElementById('fs-bals-dropdown');
  if(dd)dd.style.display='none';
};

window.fsAddBal=function(name){
  if(!name)return;
  var lc=name.toLowerCase();
  // Don't allow re-adding a name that's already in either set.
  if(_fsState.user.some(function(b){return b.toLowerCase()===lc}))return;
  if(_fsState.system.some(function(b){return b.toLowerCase()===lc}))return;
  _fsState.user.push(name);
  document.getElementById('fs-bals-input').value='';
  fsRenderBals();
  fsBalsHideDropdown();
};

window.fsRemoveBal=function(name){
  // System chips are rendered without an × — defense in depth in case someone
  // calls this from devtools, the server would refuse anyway.
  _fsState.user=_fsState.user.filter(function(b){return b!==name});
  fsRenderBals();
};

window.saveFlareSolverr=function(){
  var url=document.getElementById('fs-url').value.trim();
  fetch(basePath+'/api/flaresolverr',{
    method:'PUT',
    headers:{'Content-Type':'application/json','X-Lampac-Admin':'1'},
    credentials:'include',
    // Send only the user-managed list — system entries are kept by the server.
    body:JSON.stringify({url:url,balancers:_fsState.user})
  }).then(function(r){return r.json()}).then(function(d){
    if(d.ok){
      toast('FlareSolverr настройки сохранены');
      _fsState.url=d.url||url;
      if(Array.isArray(d.balancer_entries)&&Array.isArray(d.user_balancers)){
        _fsState.system=d.balancer_entries.filter(function(e){return e&&e.system}).map(function(e){return e.name});
        _fsState.user=(d.user_balancers||[]).slice();
      }else{
        _fsState.user=(d.balancers||[]).slice();
      }
      fsRenderBals();
    }
    else toast(d.error||'Ошибка сохранения','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

window.testFlareSolverr=function(){
  var url=document.getElementById('fs-url').value.trim();
  if(!url){toast('Укажите URL FlareSolverr','error');return}
  toast('Проверяю '+url+'…');
  // Bounce through our backend — FlareSolverr is usually on localhost/docker
  // and CORS-blocks the admin UI. Backend has a clean network path.
  api('/api/flaresolverr?test='+encodeURIComponent(url)).then(function(d){
    if(d&&d.ok)toast('✓ '+d.message);
    else toast('✗ '+(d.message||'не удалось'),'error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

window.fsResetSession=function(host){
  if(!host)return;
  if(!confirm('Сбросить кэшированную сессию для '+host+'?'))return;
  fetch(basePath+'/api/flaresolverr?host='+encodeURIComponent(host),{
    method:'DELETE',
    headers:{'X-Lampac-Admin':'1'},
    credentials:'include'
  }).then(function(r){return r.json()}).then(function(d){
    if(d.ok){toast('Сессия сброшена');loadFlareSolverr()}
    else toast(d.error||'Ошибка','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

window.testProxyEntry=function(idx){
  var res=document.getElementById('proxy-test-'+idx);
  res.innerHTML='<span style="color:var(--warning)">Тестирование...</span>';
  post('/api/proxy',{action:'test',entry_index:idx}).then(function(r){
    if(r.ok){
      var info=r.flag+' '+esc(r.country)+' — IP: <strong>'+esc(r.ip)+'</strong> ('+r.latency_ms+' ms)';
      res.innerHTML='<span style="color:var(--accent)">✓ '+info+'</span>';
      // Update label if empty
      syncStateFromDOM();
      var entry=_proxyState.entries[idx];
      if(entry&&!entry.label&&r.flag&&r.country){
        entry.label=r.flag+' '+r.country;
        var labelEl=document.getElementById('proxy-label-'+idx);
        if(labelEl)labelEl.value=entry.label;
      }
    }else{
      res.innerHTML='<span style="color:var(--danger)">✗ '+esc(r.error)+' ('+(r.latency_ms||0)+' ms)</span>';
    }
  }).catch(function(e){res.innerHTML='<span style="color:var(--danger)">✗ '+esc(e.message)+'</span>'});
};

// --- ProxyCore (Built-in Proxy Engine) ---
var _pcData=null;var _pcTimer=null;
function pcFmt(b){if(!b||b===0)return'0 B';var k=1024,s=['B','KB','MB','GB','TB'],i=Math.floor(Math.log(b)/Math.log(k));if(i>=s.length)i=s.length-1;return(b/Math.pow(k,i)).toFixed(1)+' '+s[i]}
function pcProtoColor(p){var m={vless:'#00d4aa',trojan:'#ff6b6b',ss:'#ffd93d',hysteria2:'#6c5ce7',vmess:'#a29bfe',socks5:'#74b9ff',http:'#fdcb6e',wireguard:'#55efc4'};return m[(p||'').toLowerCase()]||'var(--accent)'}
function pcProtoIcon(p){var m={vless:'\u26A1',trojan:'\uD83D\uDC0E',ss:'\uD83D\uDD12',hysteria2:'\uD83D\uDE80',vmess:'\uD83C\uDF10',socks5:'\uD83E\uDDF6',http:'\uD83C\uDF0D',wireguard:'\uD83D\uDD10'};return m[(p||'').toLowerCase()]||'\uD83C\uDF10'}

window.loadProxyCore=function(){
  var c=document.getElementById('pc-root');if(!c)return;
  if(_pcTimer)clearInterval(_pcTimer);
  c.innerHTML='<div id="pc-wrap"><div style="text-align:center;padding:40px;color:var(--text-muted)"><div style="font-size:24px;margin-bottom:8px">&#x1F6E1;</div>Загрузка ProxyCore...</div></div>';
  pcRefresh();
  _pcTimer=setInterval(pcRefresh,8000);
};
function pcRefresh(){
  api('/api/proxycore/status').then(function(data){_pcData=data;pcRender()}).catch(function(e){
    var w=document.getElementById('pc-wrap');if(w)w.innerHTML='<div style="color:var(--danger);padding:20px">Ошибка: '+esc(e.message)+'</div>';
  });
}
function pcRender(){
  var w=document.getElementById('pc-wrap');if(!w||!_pcData)return;
  var entries=_pcData.entries||[],rules=_pcData.rules||[];
  var totalUp=0,totalDown=0,totalConns=0,aliveCount=0;
  entries.forEach(function(e){totalUp+=e.bytes_up||0;totalDown+=e.bytes_down||0;totalConns+=e.active_conns||0;if(e.alive)aliveCount++});
  var h='';
  // Header with summary stats
  h+='<div style="display:flex;align-items:center;gap:12px;margin-bottom:20px;flex-wrap:wrap">';
  h+='<div style="font-size:20px;font-weight:700;display:flex;align-items:center;gap:8px">&#x1F6E1; ProxyCore</div>';
  h+='<div style="display:flex;gap:8px;margin-left:auto;flex-wrap:wrap">';
  h+='<div style="background:var(--accent-a12);padding:4px 12px;border-radius:20px;font-size:11px;font-weight:600"><span style="color:var(--accent)">'+aliveCount+'</span><span style="color:var(--text-muted)">/'+entries.length+' online</span></div>';
  h+='<div style="background:var(--accent-a06);padding:4px 12px;border-radius:20px;font-size:11px;color:var(--text-muted)">\u2191 '+pcFmt(totalUp)+' \u2193 '+pcFmt(totalDown)+'</div>';
  h+='<div style="background:var(--accent-a06);padding:4px 12px;border-radius:20px;font-size:11px;color:var(--text-muted)">\u26A1 '+totalConns+' active</div>';
  h+='</div></div>';
  // Action bar
  h+='<div style="display:flex;gap:8px;margin-bottom:16px;flex-wrap:wrap">';
  h+='<button class="btn btn-primary" onclick="pcAddEntry()" style="border-radius:8px;font-size:12px;padding:6px 14px">+ Добавить прокси</button>';
  h+='<button class="btn" onclick="pcAddWARP(this)" style="border-radius:8px;font-size:12px;padding:6px 14px;background:rgba(174,213,129,0.1);border-color:#aed581;color:#aed581">🌐 WARP</button>';
  h+='<button class="btn" onclick="pcTestURI()" style="border-radius:8px;font-size:12px;padding:6px 14px">\uD83E\uDDEA Тест URI</button>';
  h+='<button class="btn btn-warning" onclick="pcReload()" style="border-radius:8px;font-size:12px;padding:6px 14px">\u21BB Reload</button>';
  h+='</div>';
  // Proxy cards
  if(entries.length===0){
    h+='<div style="background:var(--card-bg);border:1px dashed var(--border);border-radius:12px;padding:40px;text-align:center;color:var(--text-muted)">';
    h+='<div style="font-size:32px;margin-bottom:12px">&#x1F310;</div>';
    h+='<div style="font-size:14px;margin-bottom:6px">Нет настроенных прокси</div>';
    h+='<div style="font-size:11px">Добавьте через кнопку выше или в <code style="background:var(--accent-a12);padding:2px 6px;border-radius:4px">[proxycore.entries]</code> config.toml</div>';
    h+='</div>';
  }else{
    h+='<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(340px,1fr));gap:12px;margin-bottom:24px">';
    entries.forEach(function(e,i){
      var alive=e.alive;
      var borderColor=alive?pcProtoColor(e.protocol):'var(--danger)';
      var statusDot=alive?'<span style="width:8px;height:8px;border-radius:50%;background:#00d4aa;display:inline-block;box-shadow:0 0 6px #00d4aa"></span>':'<span style="width:8px;height:8px;border-radius:50%;background:var(--danger);display:inline-block"></span>';
      var lat=e.latency_ms>0?e.latency_ms+'ms':'\u2014';
      var avg=e.avg_latency_ms>0?e.avg_latency_ms+'ms':'\u2014';
      h+='<div style="background:var(--card-bg);border:1px solid var(--border);border-left:3px solid '+borderColor+';border-radius:12px;padding:16px;transition:all .2s">';
      // Title row
      h+='<div style="display:flex;align-items:center;gap:8px;margin-bottom:10px">';
      h+='<span style="font-size:24px">'+(e.flag||pcProtoIcon(e.protocol))+'</span>';
      h+='<div style="flex:1;min-width:0"><div style="font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">'+esc(e.label||e.id)+'</div>';
      h+='<div style="font-size:10px;color:var(--text-dim);margin-top:1px">'+(e.country?esc(e.country)+' \u2022 ':'')+esc(e.server)+'</div></div>';
      h+=statusDot;
      h+='</div>';
      // Protocol badge + SOCKS addr
      h+='<div style="display:flex;gap:6px;margin-bottom:10px;flex-wrap:wrap">';
      h+='<span style="background:'+pcProtoColor(e.protocol)+'22;color:'+pcProtoColor(e.protocol)+';padding:2px 8px;border-radius:6px;font-size:10px;font-weight:700;letter-spacing:.5px">'+esc(e.protocol).toUpperCase()+'</span>';
      h+='<span style="background:var(--accent-a06);color:var(--text-muted);padding:2px 8px;border-radius:6px;font-size:10px;font-family:monospace">'+esc(e.socks_addr)+'</span>';
      h+='</div>';
      // Stats grid
      h+='<div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:8px;margin-bottom:10px">';
      h+='<div style="background:var(--bg);border-radius:8px;padding:8px;text-align:center"><div style="font-size:13px;font-weight:700;color:var(--accent)">'+lat+'</div><div style="font-size:9px;color:var(--text-dim);margin-top:2px">LATENCY</div></div>';
      h+='<div style="background:var(--bg);border-radius:8px;padding:8px;text-align:center"><div style="font-size:13px;font-weight:700">'+e.active_conns+'<span style="font-size:10px;color:var(--text-dim)">/'+e.total_conns+'</span></div><div style="font-size:9px;color:var(--text-dim);margin-top:2px">CONNS</div></div>';
      h+='<div style="background:var(--bg);border-radius:8px;padding:8px;text-align:center"><div style="font-size:13px;font-weight:700;color:'+(e.fail_count>0?'var(--danger)':'var(--text)')+'">'+e.fail_count+'</div><div style="font-size:9px;color:var(--text-dim);margin-top:2px">FAILS</div></div>';
      h+='</div>';
      // Traffic
      h+='<div style="display:flex;justify-content:space-between;font-size:10px;color:var(--text-muted);margin-bottom:8px">';
      h+='<span>\u2191 '+pcFmt(e.bytes_up)+'</span><span>\u2193 '+pcFmt(e.bytes_down)+'</span><span>avg '+avg+'</span>';
      h+='</div>';
      // Balancers
      if(e.balancers&&e.balancers.length){
        h+='<div style="display:flex;gap:4px;flex-wrap:wrap;margin-bottom:10px">';
        e.balancers.forEach(function(b){h+='<span style="background:var(--accent-a06);color:var(--text-muted);padding:1px 6px;border-radius:4px;font-size:9px">'+esc(b)+'</span>'});
        h+='</div>';
      }
      // Actions
      h+='<div style="display:flex;gap:6px;border-top:1px solid var(--border);padding-top:8px">';
      h+='<button class="btn btn-sm" id="pc-test-'+i+'" onclick="pcTestIdx('+i+',this)" style="border-radius:6px;font-size:11px">\uD83E\uDDEA Test</button>';
      h+='<button class="btn btn-sm" onclick="pcEditEntry('+i+')" style="border-radius:6px;font-size:11px">\u270F\uFE0F Edit</button>';
      h+='<button class="btn btn-sm" onclick="pcDeleteEntry('+i+')" style="border-radius:6px;font-size:11px;color:var(--danger)">\u2716 Del</button>';
      h+='</div></div>';
    });
    h+='</div>';
  }
  // Routing rules section
  h+='<div style="background:var(--card-bg);border:1px solid var(--border);border-radius:12px;padding:16px;margin-bottom:16px">';
  h+='<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px">';
  h+='<div style="font-size:14px;font-weight:600">\uD83D\uDEE3\uFE0F Маршрутизация</div>';
  h+='<button class="btn btn-sm btn-primary" onclick="pcAddRule()" style="border-radius:6px;font-size:11px">+ Правило</button>';
  h+='</div>';
  if(rules.length===0){
    h+='<div style="color:var(--text-dim);font-size:12px;text-align:center;padding:16px">Правил нет \u2014 трафик идёт по конфигу балансеров напрямую</div>';
  }else{
    rules.forEach(function(r,i){
      var modeColors={fixed:'#74b9ff',latency:'#00d4aa','round-robin':'#ffd93d'};
      var mc=modeColors[r.mode]||'var(--accent)';
      h+='<div style="display:flex;align-items:center;gap:8px;padding:8px 0;border-bottom:1px solid var(--border);font-size:12px">';
      h+='<div style="flex:1"><span style="color:var(--text-muted)">'+((r.balancers||[]).join(', ')||'\u2014')+'</span></div>';
      h+='<span style="font-weight:600">\u2192</span>';
      h+='<div style="min-width:80px;text-align:center;font-weight:600">'+esc(r.proxy_id||r.proxy||'')+'</div>';
      if(r.fallback){h+='<span style="color:var(--text-dim);font-size:10px">\u21AA '+esc(r.fallback)+'</span>';}
      h+='<span style="background:'+mc+'22;color:'+mc+';padding:2px 8px;border-radius:10px;font-size:9px;font-weight:700;text-transform:uppercase">'+esc(r.mode||'fixed')+'</span>';
      h+='<button class="btn btn-sm" onclick="pcDeleteRule('+i+')" style="padding:2px 6px;font-size:10px;color:var(--danger)">\u2716</button>';
      h+='</div>';
    });
  }
  h+='</div>';
  // Quick test URI section
  h+='<div style="background:var(--card-bg);border:1px solid var(--border);border-radius:12px;padding:16px">';
  h+='<div style="font-size:14px;font-weight:600;margin-bottom:10px">\uD83E\uDDEA Quick Test</div>';
  h+='<div style="display:flex;gap:8px"><input id="pc-test-uri" placeholder="vless://... | ss://... | trojan://..." style="flex:1;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:8px 12px;color:var(--text);font-size:12px;font-family:monospace;outline:none">';
  h+='<button class="btn btn-primary" onclick="pcTestURIInput()" style="border-radius:8px;white-space:nowrap">Test</button></div>';
  h+='<div id="pc-test-result" style="margin-top:8px;font-size:12px"></div>';
  h+='</div>';
  w.innerHTML=h;
}

window.pcReload=function(){post('/api/proxycore/reload',{}).then(function(r){if(r.ok){toast('ProxyCore перезагружен');pcRefresh()}else toast(r.error||'Ошибка','error')})};

// --- Balancer tag picker ---
var _pcAvailBals=[];  // [{name:'rezka',display:'PidoRezka'}, ...]
var _pcBalsSelected=[]; // ['rezka','mirage']
var _pcEditIndex=-1;

function pcLoadBalancers(cb){
  if(_pcAvailBals.length){if(cb)cb();return}
  api('/api/proxycore/balancers').then(function(d){
    _pcAvailBals=d.balancers||[];
    if(cb)cb();
  }).catch(function(){_pcAvailBals=[];if(cb)cb()});
}

function pcBalsRender(){
  pcLoadBalancers(function(){
    var tagsEl=document.getElementById('pc-m-bals-tags');
    var dropEl=document.getElementById('pc-m-bals-dropdown');
    if(!tagsEl||!dropEl)return;
    // Render selected chips
    var h='';
    _pcBalsSelected.forEach(function(name){
      var disp=name;
      _pcAvailBals.forEach(function(b){if(b.name===name)disp=b.display});
      h+='<span style="display:inline-flex;align-items:center;gap:4px;background:var(--accent-a12);color:var(--accent);padding:3px 8px;border-radius:6px;font-size:11px;font-weight:600;cursor:pointer" onclick="pcBalsToggle(\''+name+'\')">';
      h+=esc(disp)+' <span style="opacity:.6;font-size:9px">\u2716</span></span>';
    });
    if(_pcBalsSelected.length===0){
      h+='<span style="color:var(--text-dim);font-size:11px;padding:3px 0">Нажмите для выбора балансеров</span>';
    }
    tagsEl.innerHTML=h;
    pcBalsRenderDropdown('');
  });
}

function pcBalsRenderDropdown(filter){
  var dropEl=document.getElementById('pc-m-bals-dropdown');
  if(!dropEl)return;
  var low=filter.toLowerCase();
  var h='';
  // "telegram" special group first, then others
  var groups=[{label:'Сервисы',items:[]},{label:'Балансеры',items:[]}];
  _pcAvailBals.forEach(function(b){
    if(low&&b.name.indexOf(low)===-1&&b.display.toLowerCase().indexOf(low)===-1)return;
    if(b.name==='telegram'){groups[0].items.push(b)}else{groups[1].items.push(b)}
  });
  groups.forEach(function(g){
    if(g.items.length===0)return;
    h+='<div style="padding:4px 12px;font-size:9px;color:var(--text-dim);text-transform:uppercase;letter-spacing:.5px;margin-top:4px">'+g.label+'</div>';
    g.items.forEach(function(b){
      var sel=_pcBalsSelected.indexOf(b.name)>=0;
      var bg=sel?'var(--accent-a12)':'transparent';
      var clr=sel?'var(--accent)':'var(--text)';
      var check=sel?'\u2714 ':'';
      h+='<div onclick="pcBalsToggle(\''+b.name+'\')" style="padding:6px 12px;cursor:pointer;font-size:12px;background:'+bg+';color:'+clr+';transition:background .15s" onmouseover="this.style.background=\'var(--accent-a06)\'" onmouseout="this.style.background=\''+bg+'\'">';
      h+=check+esc(b.display)+'</div>';
    });
  });
  if(h==='')h='<div style="padding:12px;text-align:center;color:var(--text-dim);font-size:11px">Ничего не найдено</div>';
  dropEl.innerHTML=h;
}

window.pcBalsToggle=function(name){
  var idx=_pcBalsSelected.indexOf(name);
  if(idx>=0){_pcBalsSelected.splice(idx,1)}else{_pcBalsSelected.push(name)}
  pcBalsRender();
};

// Search + dropdown toggle
(function(){
  document.addEventListener('click',function(e){
    var wrap=document.getElementById('pc-m-bals-wrap');
    var drop=document.getElementById('pc-m-bals-dropdown');
    if(!wrap||!drop)return;
    if(wrap.contains(e.target)){drop.style.display='block'}else{drop.style.display='none'}
  });
  document.addEventListener('input',function(e){
    if(e.target&&e.target.id==='pc-m-bals-search'){pcBalsRenderDropdown(e.target.value)}
  });
})();

// Modal state
var _pcTestResult=null;
window.pcAddEntry=function(){
  document.getElementById('pc-modal').style.display='flex';
  document.getElementById('pc-modal-step1').style.display='block';
  document.getElementById('pc-modal-step2').style.display='none';
  document.getElementById('pc-m-uri').value='';
  document.getElementById('pc-m-test-result').innerHTML='';
  document.getElementById('pc-m-label').value='';
  _pcBalsSelected=[];pcBalsRender();
  _pcEditIndex=-1;
  _pcTestResult=null;
  setTimeout(function(){document.getElementById('pc-m-uri').focus()},100);
};
window.pcCloseModal=function(){document.getElementById('pc-modal').style.display='none'};
window.pcSetProto=function(p){
  var el=document.getElementById('pc-m-uri');
  var prefixes={vless:'vless://uuid@server:443?security=reality&sni=example.com&fp=chrome&pbk=PUBLIC_KEY&sid=SHORT_ID&flow=xtls-rprx-vision&type=tcp#label',trojan:'trojan://password@server:443?sni=example.com&type=tcp#label',ss:'ss://method:password@server:8388#label',hy2:'hy2://password@server:443?sni=example.com#label',socks5:'socks5://user:pass@server:1080',vmess:'vmess://uuid@server:443?security=tls&type=ws&path=/ws#label'};
  el.value=prefixes[p]||'';el.focus();el.select();
};
window.pcModalNext=function(){
  var uri=document.getElementById('pc-m-uri').value.trim();
  if(!uri){toast('Введите URI','error');return}
  var res=document.getElementById('pc-m-test-result');
  res.innerHTML='<div style="display:flex;align-items:center;gap:8px"><div class="pc-spinner"></div><span style="color:var(--warning)">Тестирование подключения...</span></div>';
  post('/api/proxycore/test',{uri:uri}).then(function(r){
    _pcTestResult=r;
    if(r.success||r.protocol){
      // Show step 2
      document.getElementById('pc-modal-step1').style.display='none';
      document.getElementById('pc-modal-step2').style.display='block';
      document.getElementById('pc-m-flag').textContent=r.flag||'\uD83C\uDF10';
      document.getElementById('pc-m-country').textContent=r.country||(r.success?'Connected':'Connection failed');
      document.getElementById('pc-m-server-info').textContent=esc(r.server)+' \u2022 IP: '+(r.ip||'?')+' \u2022 '+(r.latency_ms||0)+'ms';
      var badge=document.getElementById('pc-m-proto-badge');
      badge.textContent=(r.protocol||'').toUpperCase();
      badge.style.background=pcProtoColor(r.protocol)+'22';
      badge.style.color=pcProtoColor(r.protocol);
      // Auto-fill label
      if(r.flag&&r.country)document.getElementById('pc-m-label').value=r.flag+' '+r.country;
      if(!r.success)res.innerHTML='<span style="color:var(--warning)">\u26A0 '+esc(r.error||'')+'</span>';
    }else{
      res.innerHTML='<span style="color:var(--danger)">\u274C '+esc(r.error||'Failed')+'</span>';
    }
  }).catch(function(e){res.innerHTML='<span style="color:var(--danger)">\u274C '+esc(e.message)+'</span>'});
};
window.pcModalBack=function(){
  document.getElementById('pc-modal-step1').style.display='block';
  document.getElementById('pc-modal-step2').style.display='none';
};
window.pcModalSave=function(){
  var uri=document.getElementById('pc-m-uri').value.trim();
  var label=document.getElementById('pc-m-label').value.trim();
  var balArr=_pcBalsSelected.slice();
  if(_pcEditIndex>=0){
    post('/api/proxycore/entries',{action:'update',index:_pcEditIndex,label:label,balancers:balArr,uri:'_keep_'}).then(function(r){
      if(r.ok){toast('Обновлено');pcCloseModal();pcRefresh()}else toast(r.error||'Ошибка','error');
    });
  }else{
    post('/api/proxycore/entries',{action:'add',uri:uri,label:label,balancers:balArr}).then(function(r){
      if(r.ok){toast('Прокси добавлен');pcCloseModal();pcRefresh()}
      else toast(r.error||r.reload_error||'Ошибка','error');
    });
  }
};
window.pcEditEntry=function(idx){
  if(!_pcData||!_pcData.entries||!_pcData.entries[idx])return;
  var e=_pcData.entries[idx];
  _pcEditIndex=idx;
  // Open modal in edit mode (skip step 1)
  document.getElementById('pc-modal').style.display='flex';
  document.getElementById('pc-modal-step1').style.display='none';
  document.getElementById('pc-modal-step2').style.display='block';
  document.getElementById('pc-m-label').value=e.label||'';
  document.getElementById('pc-m-uri').value=e.uri||'';
  // Pre-select balancers
  _pcBalsSelected=(e.balancers||[]).map(function(b){return b.toLowerCase()});
  pcBalsRender();
  // Hide test result area, show geo if available
  var geo=document.getElementById('pc-m-geo');if(geo)geo.style.display='none';
};
window.pcAddWARP=function(btn){
  // One-click: save the sentinel wg://warp URI; the pool's startAll path
  // calls Cloudflare's /reg API, fills in keys/host/port/reserved, and runs
  // the dialer via native userspace WireGuard — no sidecar binary, no copy-
  // paste config. Reload returns the registration error verbatim if it fails.
  var orig=btn.innerHTML;btn.disabled=true;btn.innerHTML='⏳ Регистрируем WARP...';
  post('/api/proxycore/entries',{action:'add',uri:'wg://warp',label:'🌐 Cloudflare WARP',balancers:[],engine:'proxycore'}).then(function(r){
    btn.disabled=false;btn.innerHTML=orig;
    if(!r.ok){toast(r.error||'Ошибка добавления','error');return}
    if(r.reload_error){toast('Добавлен, но reload упал: '+r.reload_error,'error');pcRefresh();return}
    toast('🌐 Cloudflare WARP зарегистрирован и добавлен');
    pcRefresh();
  }).catch(function(e){btn.disabled=false;btn.innerHTML=orig;toast(e.message,'error')});
};
window.pcDeleteEntry=function(idx){if(!confirm('Удалить прокси?'))return;post('/api/proxycore/entries',{action:'delete',index:idx}).then(function(r){if(r.ok){toast('Удалён');pcRefresh()}else toast(r.error||'Ошибка','error')})};
window.pcTestIdx=function(idx,btn){
  btn.disabled=true;var orig=btn.innerHTML;btn.innerHTML='\u23F3';
  // Get URI from entries config and test it
  api('/api/proxycore/entries').then(function(d){
    var entries=d.entries||[];
    if(idx>=entries.length){btn.disabled=false;btn.innerHTML=orig;return}
    var uri=entries[idx].uri;
    return post('/api/proxycore/test',{uri:uri});
  }).then(function(r){
    btn.disabled=false;
    if(!r)return;
    if(r.success){btn.innerHTML='\u2705 '+r.latency_ms+'ms';btn.style.color='var(--accent)'}
    else{btn.innerHTML='\u274C '+(r.error||'').substring(0,30);btn.style.color='var(--danger)'}
    setTimeout(function(){btn.innerHTML=orig;btn.style.color=''},4000);
  }).catch(function(){btn.disabled=false;btn.innerHTML=orig});
};
window.pcTestURI=function(){var el=document.getElementById('pc-test-uri');if(el)el.focus()};
window.pcTestURIInput=function(){
  var uri=(document.getElementById('pc-test-uri')||{}).value;
  if(!uri){toast('Введите URI','error');return}
  var res=document.getElementById('pc-test-result');
  res.innerHTML='<span style="color:var(--warning)">\u23F3 Тестирование...</span>';
  post('/api/proxycore/test',{uri:uri}).then(function(r){
    if(r.success){res.innerHTML='<span style="color:var(--accent)">\u2705 <b>'+esc(r.protocol).toUpperCase()+'</b> \u2014 '+r.latency_ms+'ms | IP: '+esc(r.ip||'?')+' | Server: '+esc(r.server)+'</span>'}
    else{res.innerHTML='<span style="color:var(--danger)">\u274C '+esc(r.error||'Unknown error')+'</span>'}
  }).catch(function(e){res.innerHTML='<span style="color:var(--danger)">\u274C '+esc(e.message)+'</span>'});
};
window.pcAddRule=function(){
  var bals=prompt('Балансеры (через запятую):');if(!bals)return;
  var proxy=prompt('Прокси (label):');if(!proxy)return;
  var fb=prompt('Fallback (пусто = нет):')||'';
  var mode=prompt('Режим (fixed / latency / round-robin):','fixed')||'fixed';
  post('/api/proxycore/rules',{action:'add',balancers:bals.split(',').map(function(s){return s.trim()}).filter(Boolean),proxy:proxy,fallback:fb,mode:mode,id:'rule-'+Date.now()}).then(function(r){
    if(r.ok){toast('Правило добавлено');pcRefresh()}else toast(r.error||'Ошибка','error');
  });
};
window.pcDeleteRule=function(idx){if(!confirm('Удалить правило?'))return;post('/api/proxycore/rules',{action:'delete',index:idx}).then(function(r){if(r.ok){toast('Удалено');pcRefresh()}})};

// --- Constructor ---
var _ctrData=null;
window.loadConstructor=function(){};

// File upload
(function(){
  var dropzone=document.getElementById('ctr-dropzone');
  var fileInput=document.getElementById('ctr-file-input');
  if(!dropzone)return;
  dropzone.addEventListener('dragover',function(e){e.preventDefault();dropzone.classList.add('drag-over')});
  dropzone.addEventListener('dragleave',function(){dropzone.classList.remove('drag-over')});
  dropzone.addEventListener('drop',function(e){
    e.preventDefault();dropzone.classList.remove('drag-over');
    var files=e.dataTransfer.files;
    if(files.length>1)readCSFiles(files);
    else if(files.length===1)readCSFile(files[0]);
  });
  fileInput.addEventListener('change',function(){
    if(fileInput.files.length>1)readCSFiles(fileInput.files);
    else if(fileInput.files[0])readCSFile(fileInput.files[0]);
  });
})();

function readCSFile(f){
  // zip files — send as binary
  if(f.name.toLowerCase().endsWith('.zip')){
    var fd=new FormData();
    fd.append('file',f,f.name);
    sendConstructor(fd);
    return;
  }
  var reader=new FileReader();
  reader.onload=function(){
    document.getElementById('ctr-paste').value=reader.result;
    analyzeCS();
  };
  reader.readAsText(f);
}

function readCSFiles(files){
  var fd=new FormData();
  var hasZip=false;
  for(var i=0;i<files.length;i++){
    if(files[i].name.toLowerCase().endsWith('.zip')){
      fd.append('file',files[i],files[i].name);hasZip=true;break;
    }
    fd.append('files[]',files[i],files[i].name);
  }
  sendConstructor(fd);
}

function sendConstructor(fd){
  fetch(basePath+'/api/constructor',{method:'POST',body:fd}).then(function(r){return r.json()}).then(function(data){
    if(data.error){toast(data.error,'error');return}
    _ctrData=data;
    renderAnalysis(data);
    document.getElementById('ctr-result').style.display='';
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
}

window.analyzeCS=function(){
  var code=document.getElementById('ctr-paste').value.trim();
  if(!code){toast('Вставьте C# код','error');return}
  var fd=new FormData();
  fd.append('file',new Blob([code],{type:'text/plain'}),'balancer.cs');
  sendConstructor(fd);
};

window.switchCtrTab=function(el){
  document.querySelectorAll('.ctr-tab').forEach(function(t){t.classList.remove('active')});
  document.querySelectorAll('.ctr-panel').forEach(function(p){p.classList.remove('active')});
  el.classList.add('active');
  var tid=el.dataset.ctab;
  document.getElementById('ctr-'+tid).classList.add('active');
};

function renderAnalysis(d){
  var h='';
  // Header
  h+='<div class="ctr-section"><h3>&#128204; Общие сведения</h3>';
  h+='<div class="ctr-grid">';
  h+='<div class="ctr-chip"><strong>Класс:</strong> '+esc(d.class_name||'?')+'</div>';
  h+='<div class="ctr-chip"><strong>Имя:</strong> '+esc(d.name||'?')+'</div>';
  h+='<div class="ctr-chip"><strong>Настройки:</strong> '+esc(d.settings_type||'BaseSettings')+'</div>';
  h+='<div class="ctr-chip"><strong>Контент:</strong> '+esc(d.content_type||'?')+'</div>';
  h+='</div></div>';

  // Routes
  if(d.routes&&d.routes.length){
    h+='<div class="ctr-section"><h3>&#128279; Маршруты ('+d.routes.length+')</h3>';
    h+='<div class="ctr-grid">';
    d.routes.forEach(function(r){h+='<div class="ctr-chip">/'+esc(r)+'</div>'});
    h+='</div></div>';
  }

  // Parameters
  if(d.index_params&&d.index_params.length){
    h+='<div class="ctr-section"><h3>&#128203; Параметры Index ('+d.index_params.length+')</h3>';
    d.index_params.forEach(function(p){
      h+='<div class="ctr-param"><span class="pname">'+esc(p.name)+'</span><span class="ptype">'+esc(p.type)+'</span>';
      if(p.default)h+='<span class="pdefault">= '+esc(p.default)+'</span>';
      h+='</div>';
    });
    h+='</div>';
  }

  // API URLs
  if(d.api_urls&&d.api_urls.length){
    h+='<div class="ctr-section"><h3>&#127760; API эндпоинты ('+d.api_urls.length+')</h3>';
    d.api_urls.forEach(function(u){h+='<div class="ctr-chip" style="word-break:break-all">'+esc(u)+'</div>'});
    h+='</div>';
  }

  // Headers
  var hkeys=d.headers?Object.keys(d.headers):[];
  if(hkeys.length){
    h+='<div class="ctr-section"><h3>&#128221; HTTP заголовки ('+hkeys.length+')</h3>';
    h+='<div class="ctr-grid">';
    hkeys.forEach(function(k){h+='<div class="ctr-chip"><strong>'+esc(k)+':</strong> '+esc(d.headers[k])+'</div>'});
    h+='</div></div>';
  }

  // Capabilities
  h+='<div class="ctr-section"><h3>&#128295; Возможности</h3>';
  h+='<div style="display:flex;flex-wrap:wrap;gap:6px">';
  h+=capBadge('Поиск',d.has_search);
  h+=capBadge('Сериалы',d.has_seasons);
  h+=capBadge('Playwright',d.has_playwright);
  h+=capBadge('Cookies',d.has_cookies);
  h+=capBadge('Токен',d.token_fields&&d.token_fields.length>0);
  h+='</div></div>';

  // Source files (multi-file)
  if(d.source_files&&d.source_files.length>1){
    h+='<div class="ctr-section"><h3>&#128193; Файлы ('+d.source_files.length+')</h3>';
    h+='<div class="ctr-grid">';
    d.source_files.forEach(function(f){h+='<div class="ctr-chip">'+esc(f)+'</div>'});
    h+='</div></div>';
  }

  // Deploy button + host override
  if(d.standalone_go&&d.name){
    h+='<div class="ctr-section" style="text-align:center;padding:16px 0">';
    var defHost=(d.api_urls&&d.api_urls.length>0)?d.api_urls[0]:'';
    h+='<div style="margin-bottom:10px;text-align:left">';
    h+='<label style="font-size:12px;color:var(--text-muted)">Upstream Host (сайт-источник)</label>';
    h+='<input class="input-sm" id="ctr-deploy-host" style="width:100%;margin-top:4px" value="'+esc(defHost)+'" placeholder="https://example.com">';
    if(d.api_urls&&d.api_urls.length>1){h+='<div style="font-size:11px;color:var(--text-dim);margin-top:2px">Другие URL: '+d.api_urls.slice(1).map(esc).join(', ')+'</div>'}
    h+='</div>';
    h+='<button class="btn btn-primary" style="font-size:15px;padding:10px 32px" onclick="deployBalancer()">&#128640; Развернуть «'+esc(d.name)+'»</button>';
    h+='<div style="color:#888;font-size:12px;margin-top:6px">Скомпилирует Go код и запустит как subprocess на отдельном порту</div>';
    h+='</div>';
  }

  // LLM porter button — separate from deploy, only needs raw C# code
  // _llmEnabled is set either by server injection (at startup) or by config save (dynamic)
  if((window._llmEnabled||window._llmDynamic)&&d.raw_cs_code&&d.name){
    h+='<div class="ctr-section" style="text-align:center;padding:16px 0">';
    h+='<button class="btn" style="font-size:15px;padding:10px 32px;background:#6c3ed6;color:#fff;border:none;border-radius:6px;cursor:pointer" onclick="portWithLLM()">&#129302; LLM-портировать «'+esc(d.name)+'»</button>';
    h+='<div style="color:#888;font-size:12px;margin-top:6px">LLM сгенерирует полный рабочий Go код, скомпилирует и развернёт (2-5 мин)</div>';
    h+='<div id="ctr-llm-status" style="display:none;margin-top:10px;padding:10px;background:var(--surface2);border-radius:6px;font-size:13px"></div>';
    h+='</div>';
  }

  // Regex patterns
  if(d.regex_patterns&&d.regex_patterns.length){
    h+='<div class="ctr-section"><h3>&#128269; Regex паттерны ('+d.regex_patterns.length+')</h3>';
    d.regex_patterns.forEach(function(r){h+='<div class="ctr-chip" style="word-break:break-all;font-size:11px">'+esc(r)+'</div>'});
    h+='</div>';
  }

  // Templates
  if(d.template_types&&d.template_types.length){
    h+='<div class="ctr-section"><h3>&#127912; Шаблоны</h3>';
    h+='<div class="ctr-grid">';
    d.template_types.forEach(function(t){h+='<div class="ctr-chip">'+esc(t)+'</div>'});
    h+='</div></div>';
  }

  // Cache
  if(d.cache_keys&&d.cache_keys.length){
    h+='<div class="ctr-section"><h3>&#128230; Кэширование</h3>';
    d.cache_keys.forEach(function(k,i){
      h+='<div class="ctr-chip"><strong>Key:</strong> '+esc(k);
      if(d.cache_ttl&&d.cache_ttl[i])h+=' <span style="color:var(--warning)">('+esc(d.cache_ttl[i])+')</span>';
      h+='</div>';
    });
    h+='</div>';
  }

  document.getElementById('ctr-analysis').innerHTML=h;

  // Go code tab
  var goEl=document.getElementById('ctr-gocode');
  goEl.innerHTML='<div class="ctr-code-wrap"><button class="ctr-copy-btn" onclick="copyGoCode()">Копировать</button><textarea class="ctr-code" id="ctr-go-textarea" readonly>'+esc(d.generated_go||'// Не удалось сгенерировать')+'</textarea></div>';

  // Standalone tab
  var saEl=document.getElementById('ctr-standalone');
  saEl.innerHTML='<div class="ctr-code-wrap"><button class="ctr-copy-btn" onclick="copyStandaloneCode()">Копировать</button><textarea class="ctr-code" id="ctr-standalone-textarea" readonly>'+esc(d.standalone_go||'// Не удалось сгенерировать standalone код')+'</textarea></div>';

  // Original tab
  var origEl=document.getElementById('ctr-original');
  origEl.innerHTML='<div class="ctr-code-wrap"><textarea class="ctr-code" readonly>'+esc(d.raw_cs_code||'')+'</textarea></div>';
}

function capBadge(label,yes){
  return '<span class="ctr-badge '+(yes?'yes':'no')+'">'+(yes?'&#10003;':'&#10007;')+' '+esc(label)+'</span>';
}

window.copyGoCode=function(){
  var ta=document.getElementById('ctr-go-textarea');
  if(!ta)return;
  navigator.clipboard.writeText(ta.value).then(function(){toast('Go код скопирован')}).catch(function(){
    ta.select();document.execCommand('copy');toast('Go код скопирован');
  });
};

window.copyStandaloneCode=function(){
  var ta=document.getElementById('ctr-standalone-textarea');
  if(!ta)return;
  navigator.clipboard.writeText(ta.value).then(function(){toast('Standalone Go код скопирован')}).catch(function(){
    ta.select();document.execCommand('copy');toast('Standalone Go код скопирован');
  });
};

window.deployBalancer=function(){
  if(!_ctrData||!_ctrData.standalone_go||!_ctrData.name){toast('Нет данных для деплоя','error');return}
  var name=_ctrData.name;
  var hostEl=document.getElementById('ctr-deploy-host');
  var hostVal=hostEl?hostEl.value.trim():'';
  if(!confirm('Развернуть балансер "'+name+'"?\n\nЭто скомпилирует Go код и запустит subprocess на отдельном порту.'))return;
  toast('Компиляция '+name+'...');
  post('/api/custbal',{
    action:'deploy',
    name:name,
    go_code:_ctrData.standalone_go,
    config:{
      display_name:_ctrData.class_name||name,
      quality_badge:'',
      content_type:_ctrData.content_type||'both',
      host:hostVal
    }
  }).then(function(r){
    if(r.ok){
      toast('Балансер «'+name+'» развёрнут!');
      if(r.output)console.log('Deploy output:',r.output);
    }else{
      toast('Ошибка: '+(r.error||'unknown'),'error');
      if(r.output)console.error('Build output:',r.output);
    }
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

window.portWithLLM=function(){
  if(!_ctrData||!_ctrData.raw_cs_code||!_ctrData.name){toast('Нет данных для портирования','error');return}
  var name=_ctrData.name;
  if(!confirm('LLM-портирование «'+name+'»?\n\nLLM сгенерирует полный рабочий Go код, скомпилирует и развернёт.'))return;
  var statusDiv=document.getElementById('ctr-llm-status');
  if(statusDiv){statusDiv.style.display='';statusDiv.innerHTML='⏳ Запуск LLM...';}
  fetch(basePath+'/api/porter',{
    method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({cs_code:_ctrData.raw_cs_code,name:name})
  }).then(function(resp){return resp.json()}).then(function(r){
    if(r.error){
      if(statusDiv)statusDiv.innerHTML='❌ '+esc(r.error);
      toast(r.error,'error');
      return;
    }
    if(!r.job_id){
      if(statusDiv)statusDiv.innerHTML='❌ Нет job_id';
      return;
    }
    var jobId=r.job_id;
    if(statusDiv)statusDiv.innerHTML='⏳ [Шаг 1] Генерация Go кода...';
    var pollTimer=setInterval(function(){
      fetch(basePath+'/api/porter?job_id='+encodeURIComponent(jobId)).then(function(pr){return pr.json()}).then(function(s){
        if(s.status==='running'){
          if(statusDiv)statusDiv.innerHTML='⏳ [Шаг '+s.step+'] '+esc(s.message||'Работаю...');
        }else if(s.status==='done'){
          clearInterval(pollTimer);
          if(statusDiv)statusDiv.innerHTML='✅ Готово за '+esc(s.elapsed||'')+' ('+esc(String(s.attempts||1))+' попыток)';
          if(s.go_code){
            _ctrData.standalone_go=s.go_code;
            var ta=document.getElementById('ctr-standalone-textarea');
            if(ta)ta.value=s.go_code;
          }
          toast('LLM-портирование «'+name+'» завершено!');
        }else if(s.status==='error'){
          clearInterval(pollTimer);
          if(statusDiv)statusDiv.innerHTML='❌ Ошибка: '+esc(s.message||'');
          if(s.go_code){
            _ctrData.standalone_go=s.go_code;
            var ta=document.getElementById('ctr-standalone-textarea');
            if(ta)ta.value=s.go_code;
          }
          toast('Ошибка LLM: '+(s.message||'неизвестная ошибка'),'error');
        }else if(s.status==='not_found'){
          clearInterval(pollTimer);
          if(statusDiv)statusDiv.innerHTML='❌ Задача не найдена';
        }
      }).catch(function(){});
    },3000);
  }).catch(function(e){
    if(statusDiv)statusDiv.innerHTML='❌ Сетевая ошибка: '+esc(e.message);
    toast('Ошибка: '+e.message,'error');
  });
};

// --- Config (TOML) ---

var _tomlOriginal=''; // saved content for dirty detection (full TOML)
var _tomlFullContent=''; // full TOML content (always kept in sync)
var _tomlValidateTimer=null;
var _activeSection='*'; // '*' = all, or section name like 'server','online', etc.

// Section navigation: maps nav items to TOML section patterns
var _sectionMap={
  'server':['[server]'],
  'online':['[online]','[online.'],
  'web':['[web]','[web.'],
  'proxy':['[proxy]','[proxy.','[[proxy.'],
  'telegram':['[telegram]'],
  'torrserver':['[torrserver]'],
  'transcoding':['[transcoding]'],
  'cub':['[cub]','[sync]'],
  'sisi':['[sisi]'],
  'admin':['[admin]','[llm]','[legacy]'],
  'proxy_link':['[proxy_link]']
};

window.switchConfigSection=function(section){
  // If switching away from a filtered view, save edits back to full content first
  if(_activeSection!=='*'){
    _mergeBackSection();
  }
  _activeSection=section;
  // Update nav active state
  var navItems=document.querySelectorAll('.cfg-nav-item');
  for(var i=0;i<navItems.length;i++){
    navItems[i].classList.toggle('active',navItems[i].getAttribute('data-section')===section);
  }
  // Update badge
  var badge=document.getElementById('cfg-section-badge');
  if(section==='*'){
    badge.textContent='config.toml';
    badge.className='toml-badge toml-badge-toml';
  }else{
    var labels={'server':'Сервер','online':'Балансеры','web':'Плагины','proxy':'Прокси','telegram':'Telegram','torrserver':'TorrServer','transcoding':'Транскодинг','cub':'CUB / Sync','sisi':'SISI','admin':'Админ','proxy_link':'ProxyLink'};
    badge.textContent=labels[section]||section;
    badge.className='toml-badge toml-badge-toml';
  }
  // Show filtered or full content
  _displaySection(section);
};

function _extractSection(fullText,section){
  if(section==='*')return fullText;
  var patterns=_sectionMap[section];
  if(!patterns)return fullText;
  var lines=fullText.split('\n');
  var result=[];
  var inSection=false;
  var headerComment=[];
  // Collect top comment (lines before first [section])
  for(var i=0;i<lines.length;i++){
    var line=lines[i];
    var trimmed=line.trim().toLowerCase();
    // Check if this line starts a new TOML section
    if(trimmed.match(/^\[{1,2}[a-z]/)){
      // Check if this section matches our filter
      var matched=false;
      for(var p=0;p<patterns.length;p++){
        if(trimmed.indexOf(patterns[p])===0){matched=true;break;}
      }
      if(matched){
        if(!inSection&&result.length===0){
          // Add section header comment
          result.push('# ═══ '+(_sectionLabels[section]||section)+' ═══');
          result.push('');
        }
        inSection=true;
        result.push(line);
      }else{
        if(inSection){
          // We were in our section but hit a new different section
          inSection=false;
          result.push('');
        }
      }
    }else if(inSection){
      result.push(line);
    }
  }
  // Clean trailing blank lines
  while(result.length&&result[result.length-1].trim()==='')result.pop();
  if(!result.length)result.push('# Секция "'+section+'" пуста — добавьте нужные параметры');
  return result.join('\n');
}

var _sectionLabels={'server':'Сервер','online':'Балансеры (online)','web':'Плагины (web)','proxy':'VLESS Прокси','telegram':'Telegram','torrserver':'TorrServer','transcoding':'Транскодинг','cub':'CUB / Sync','sisi':'SISI','admin':'Админ / LLM','proxy_link':'ProxyLink'};

function _mergeBackSection(){
  if(_activeSection==='*'){
    // Full mode — sync directly
    _tomlFullContent=document.getElementById('toml-textarea').value;
    return;
  }
  var ta=document.getElementById('toml-textarea');
  if(!ta)return;
  var editedText=ta.value;
  var patterns=_sectionMap[_activeSection];
  if(!patterns)return;

  var fullLines=_tomlFullContent.split('\n');
  var editedLines=editedText.split('\n');

  // Remove the "# ═══ ... ═══" header we added
  if(editedLines.length&&editedLines[0].match(/^# ═══/)){
    editedLines.shift();
    if(editedLines.length&&editedLines[0].trim()==='')editedLines.shift();
  }

  // Find and replace matching sections in full content
  var newLines=[];
  var inOldSection=false;
  var insertedNew=false;

  for(var i=0;i<fullLines.length;i++){
    var line=fullLines[i];
    var trimmed=line.trim().toLowerCase();
    if(trimmed.match(/^\[{1,2}[a-z]/)){
      var matched=false;
      for(var p=0;p<patterns.length;p++){
        if(trimmed.indexOf(patterns[p])===0){matched=true;break;}
      }
      if(matched){
        if(!insertedNew){
          // Insert the edited section here
          for(var j=0;j<editedLines.length;j++)newLines.push(editedLines[j]);
          newLines.push('');
          insertedNew=true;
        }
        inOldSection=true;
        continue; // skip old line
      }else{
        inOldSection=false;
        newLines.push(line);
      }
    }else if(inOldSection){
      continue; // skip old section content
    }else{
      newLines.push(line);
    }
  }
  // If section wasn't found in original, append at end
  if(!insertedNew){
    newLines.push('');
    for(var j=0;j<editedLines.length;j++)newLines.push(editedLines[j]);
  }
  _tomlFullContent=newLines.join('\n');
}

function _displaySection(section){
  var ta=document.getElementById('toml-textarea');
  if(!ta)return;
  var content=_extractSection(_tomlFullContent,section);
  ta.value=content;
  updateLineNumbers();
  updateHighlight();
  // Count lines for nav badge
  _updateNavCounts();
}

// TOML syntax highlighter
function highlightTOML(text){
  var lines=text.split('\n');
  var out=[];
  for(var i=0;i<lines.length;i++){
    var line=lines[i];
    var escaped=esc(line);
    // Comment
    if(/^\s*#/.test(line)){
      out.push('<span class="t-comment">'+escaped+'</span>');
      continue;
    }
    // Table header [section] or [[array]]
    if(/^\s*\[{1,2}[^\]]+\]{1,2}\s*$/.test(line)){
      out.push('<span class="t-table">'+escaped+'</span>');
      continue;
    }
    // Key = Value
    var m=line.match(/^(\s*)([\w.\-]+)(\s*=\s*)(.*)/);
    if(m){
      var indent=esc(m[1]);
      var key='<span class="t-key">'+esc(m[2])+'</span>';
      var eq='<span class="t-eq">'+esc(m[3])+'</span>';
      var val=highlightTOMLValue(m[4]);
      out.push(indent+key+eq+val);
      continue;
    }
    out.push(escaped);
  }
  return out.join('\n')+'\n';
}

function highlightTOMLValue(v){
  v=v.trim();
  // Inline comment
  var comment='';
  // String value
  if(v.charAt(0)==='"'||v.charAt(0)==="'"){
    return '<span class="t-string">'+esc(v)+'</span>';
  }
  // Boolean
  if(v==='true'||v==='false'){
    return '<span class="t-bool">'+v+'</span>';
  }
  // Number
  if(/^-?[\d._]+([eE][+-]?\d+)?$/.test(v)){
    return '<span class="t-number">'+esc(v)+'</span>';
  }
  // Array
  if(v.charAt(0)==='['){
    return '<span class="t-array">'+esc(v)+'</span>';
  }
  return esc(v);
}

function updateLineNumbers(){
  var ta=document.getElementById('toml-textarea');
  var ln=document.getElementById('toml-line-numbers');
  if(!ta||!ln)return;
  var count=ta.value.split('\n').length;
  var nums=[];
  for(var i=1;i<=count;i++)nums.push(i);
  ln.textContent=nums.join('\n');
}

function updateHighlight(){
  var ta=document.getElementById('toml-textarea');
  var hl=document.getElementById('toml-highlight');
  if(!ta||!hl)return;
  hl.innerHTML=highlightTOML(ta.value);
  syncScroll();
}
function syncScroll(){
  var ta=document.getElementById('toml-textarea');
  var hl=document.getElementById('toml-highlight');
  var ln=document.getElementById('toml-line-numbers');
  if(!ta)return;
  if(hl){
    hl.scrollTop=ta.scrollTop;hl.scrollLeft=ta.scrollLeft;
    // Compensate for textarea scrollbar width so highlight wraps identically
    var sbW=ta.offsetWidth-ta.clientWidth;
    hl.style.right=sbW+'px';
  }
  if(ln)ln.scrollTop=ta.scrollTop;
}

function updateDirtyState(){
  var dot=document.getElementById('cfg-modified-dot');
  if(!dot)return;
  // Compare full content to original
  var current=_tomlFullContent;
  if(_activeSection==='*'){
    var ta=document.getElementById('toml-textarea');
    if(ta)current=ta.value;
  }
  if(current!==_tomlOriginal){
    dot.classList.add('show');
  }else{
    dot.classList.remove('show');
  }
}

function setConfigStatus(type,text){
  var el=document.getElementById('cfg-status');
  if(!el)return;
  var dotClass=type==='ok'?'green':type==='error'?'red':'yellow';
  el.innerHTML='<span class="dot '+dotClass+'"></span><span style="color:var(--text-muted);font-size:11px">'+esc(text)+'</span>';
}

function showConfigError(title,detail){
  var panel=document.getElementById('cfg-error-panel');
  document.getElementById('cfg-error-title').textContent=title;
  document.getElementById('cfg-error-detail').textContent=detail;
  panel.classList.add('show');
  document.getElementById('toml-editor-wrap').classList.add('has-error');
  document.getElementById('toml-editor-wrap').classList.remove('is-valid');
}

function hideConfigError(){
  document.getElementById('cfg-error-panel').classList.remove('show');
  document.getElementById('toml-editor-wrap').classList.remove('has-error');
}

function scheduleValidation(){
  clearTimeout(_tomlValidateTimer);
  _tomlValidateTimer=setTimeout(function(){
    // For section mode, merge back and validate full
    var toValidate;
    if(_activeSection!=='*'){
      _mergeBackSection();
      toValidate=_tomlFullContent;
    }else{
      var ta=document.getElementById('toml-textarea');
      if(!ta||!ta.value.trim())return;
      toValidate=ta.value;
      _tomlFullContent=toValidate;
    }
    if(!toValidate.trim())return;
    post('/api/config/validate',{toml:toValidate}).then(function(r){
      if(r.valid){
        hideConfigError();
        setConfigStatus('ok','TOML валиден');
        document.getElementById('toml-editor-wrap').classList.add('is-valid');
      }else{
        showConfigError('Ошибка синтаксиса TOML',r.message||'unknown');
        setConfigStatus('error','Ошибка в TOML');
      }
    }).catch(function(){});
  },800);
}

function _updateNavCounts(){
  var navItems=document.querySelectorAll('.cfg-nav-item[data-section]');
  for(var i=0;i<navItems.length;i++){
    var sec=navItems[i].getAttribute('data-section');
    if(sec==='*')continue;
    var text=_extractSection(_tomlFullContent,sec);
    var lineCount=text.split('\n').filter(function(l){return l.trim()&&!l.trim().match(/^#/)}).length;
    var countEl=navItems[i].querySelector('.nav-count');
    if(!countEl){
      countEl=document.createElement('span');
      countEl.className='nav-count';
      navItems[i].appendChild(countEl);
    }
    countEl.textContent=lineCount;
  }
}

// Load TOML config
window.loadConfigTOML=function(){
  setConfigStatus('loading','Загрузка...');
  api('/api/config/toml').then(function(d){
    var ta=document.getElementById('toml-textarea');
    if(!ta)return;
    _tomlFullContent=d.toml||'';
    _tomlOriginal=_tomlFullContent;
    _activeSection='*';
    // Reset nav
    var navItems=document.querySelectorAll('.cfg-nav-item');
    for(var i=0;i<navItems.length;i++){
      navItems[i].classList.toggle('active',navItems[i].getAttribute('data-section')==='*');
    }
    document.getElementById('cfg-section-badge').textContent='config.toml';
    ta.value=_tomlFullContent;
    updateLineNumbers();
    updateHighlight();
    updateDirtyState();
    hideConfigError();

    var src=d.source==='file'?'файл':d.source==='generated'?'сгенерирован':'пусто';
    document.getElementById('cfg-source-badge').textContent=src;
    document.getElementById('cfg-file-path').textContent=d.path||'';
    setConfigStatus('ok','Загружен');
    document.getElementById('toml-editor-wrap').classList.add('is-valid');
    _updateNavCounts();

    // Load backups too
    loadBackups();
  }).catch(function(e){
    setConfigStatus('error','Ошибка загрузки');
    toast('Ошибка загрузки TOML: '+e.message,'error');
  });
};

// Save TOML config
window.saveConfigTOML=function(){
  var ta=document.getElementById('toml-textarea');
  if(!ta)return;
  // Merge section edits back to full content before saving
  if(_activeSection!=='*'){_mergeBackSection();}
  else{_tomlFullContent=ta.value;}
  var content=_tomlFullContent.trim();
  if(!content){toast('Конфиг пуст','error');return;}

  var btn=document.getElementById('cfg-save-btn');
  btn.disabled=true;
  btn.textContent='Сохранение...';
  document.getElementById('cfg-save-status').textContent='';

  post('/api/config/toml',{toml:content}).then(function(r){
    btn.disabled=false;
    btn.textContent='Сохранить и применить';
    if(r.success){
      _tomlOriginal=_tomlFullContent;
      updateDirtyState();
      hideConfigError();
      setConfigStatus('ok','Сохранён и применён');
      var msg='Конфиг сохранён';
      if(r.backup)msg+=' (бэкап: '+r.backup+')';
      if(!r.reload_ok)msg+='. Reload ошибка: '+r.reload_error;
      if(r.need_restart)msg+='\n⚠️ '+r.restart_reason;
      toast(msg,r.need_restart?'warning':r.reload_ok?'success':'error');
      var statusEl=document.getElementById('cfg-save-status');
      if(r.need_restart){
        statusEl.textContent='⚠️ Требуется перезапуск сервера';
        statusEl.style.color='#ffd93d';
      }else{
        statusEl.textContent=r.reload_ok?'Применён '+new Date().toLocaleTimeString():'Ошибка reload!';
        statusEl.style.color=r.reload_ok?'var(--accent)':'var(--danger)';
      }
      loadBackups();
    }else{
      if(r.error==='validation'){
        showConfigError('Ошибка валидации — конфиг НЕ сохранён',r.message);
        setConfigStatus('error','Невалидный TOML');
      }else{
        toast(r.message||r.error||'Ошибка','error');
        setConfigStatus('error','Ошибка');
      }
    }
  }).catch(function(e){
    btn.disabled=false;
    btn.textContent='Сохранить и применить';
    toast('Сетевая ошибка: '+e.message,'error');
    setConfigStatus('error','Сетевая ошибка');
  });
};

// Validate without saving
window.validateConfigTOML=function(){
  if(_activeSection!=='*'){_mergeBackSection();}
  else{var ta2=document.getElementById('toml-textarea');if(ta2)_tomlFullContent=ta2.value;}
  if(!_tomlFullContent.trim()){toast('Нечего проверять','error');return;}
  post('/api/config/validate',{toml:_tomlFullContent}).then(function(r){
    if(r.valid){
      hideConfigError();
      setConfigStatus('ok','TOML валиден');
      document.getElementById('toml-editor-wrap').classList.add('is-valid');
      toast('TOML валиден','success');
    }else{
      showConfigError('Ошибка валидации',r.message);
      setConfigStatus('error','Невалидный TOML');
      toast('Ошибка валидации','error');
    }
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

// Backups
function loadBackups(){
  api('/api/config/backups').then(function(d){
    var list=d.backups||[];
    document.getElementById('cfg-backups-count').textContent=list.length;
    var body=document.getElementById('cfg-backups-body');
    if(!list.length){
      body.innerHTML='<div style="padding:16px;color:var(--text-dim);text-align:center;font-size:12px">Нет бэкапов</div>';
      return;
    }
    var html='';
    for(var i=0;i<list.length;i++){
      var b=list[i];
      var sz=b.size>1024?(b.size/1024).toFixed(1)+' KB':b.size+' B';
      html+='<div class="cfg-backup-item">';
      html+='<span class="bk-name">'+esc(b.name)+'</span>';
      html+='<span class="bk-date">'+esc(b.created)+'</span>';
      html+='<span class="bk-size">'+sz+'</span>';
      html+='<button class="btn btn-primary btn-sm" onclick="previewBackup(\''+esc(b.name)+'\')">Просмотр</button>';
      html+='<button class="btn btn-danger btn-sm" onclick="rollbackConfig(\''+esc(b.name)+'\')">Откатить</button>';
      html+='</div>';
    }
    body.innerHTML=html;
  }).catch(function(){});
}

window.toggleBackups=function(){
  var body=document.getElementById('cfg-backups-body');
  var chev=document.getElementById('cfg-backups-chevron');
  body.classList.toggle('open');
  chev.classList.toggle('open');
  if(body.classList.contains('open'))loadBackups();
};

window.previewBackup=function(name){
  if(!confirm('Откатить конфигурацию к "'+name+'"?\n\nТекущий конфиг будет забэкаплен перед откатом.'))return;
  rollbackConfig(name);
};

window.rollbackConfig=function(name){
  if(!confirm('Откатить конфигурацию к "'+name+'"?\n\nТекущий конфиг будет забэкаплен перед откатом.'))return;
  post('/api/config/rollback',{name:name}).then(function(r){
    if(r.success){
      toast('Откат к '+r.restored+' выполнен'+(r.reload_ok?'':'. Reload ошибка: '+r.reload_error),r.reload_ok?'success':'error');
      loadConfigTOML(); // reload editor
    }else{
      toast(r.error||'Ошибка отката','error');
    }
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

window.toggleLegacyJSON=function(){
  var body=document.getElementById('legacy-json-body');
  var chev=document.getElementById('legacy-json-chevron');
  var isOpen=body.style.display!=='none';
  body.style.display=isOpen?'none':'block';
  chev.classList.toggle('open',!isOpen);
  if(!isOpen)loadConfig();
};

// TOML textarea event wiring
(function(){
  var ta=document.getElementById('toml-textarea');
  if(!ta)return;

  ta.addEventListener('input',function(){
    updateLineNumbers();
    updateHighlight();
    updateDirtyState();
    scheduleValidation();
  });

  ta.addEventListener('scroll',function(){syncScroll();});

  // Tab key inserts spaces
  ta.addEventListener('keydown',function(e){
    if(e.key==='Tab'){
      e.preventDefault();
      var start=ta.selectionStart;
      var end=ta.selectionEnd;
      ta.value=ta.value.substring(0,start)+'  '+ta.value.substring(end);
      ta.selectionStart=ta.selectionEnd=start+2;
      updateLineNumbers();
      updateHighlight();
      updateDirtyState();
      scheduleValidation();
    }
    // Ctrl+S / Cmd+S = save
    if((e.ctrlKey||e.metaKey)&&e.key==='s'){
      e.preventDefault();
      saveConfigTOML();
    }
  });
})();

// --- Config (legacy JSON) ---

// Sync LLM UI fields → JSON textarea
function llmFieldsToJSON(){
  var ta=document.getElementById('config-editor');
  var cfg={};
  try{cfg=JSON.parse(ta.value)}catch(e){return}
  var chk=document.getElementById('cfg-llm-enable');
  if(chk.checked){
    var ep=document.getElementById('cfg-llm-endpoint').value.trim();
    var ak=document.getElementById('cfg-llm-apikey').value.trim();
    var mdl=document.getElementById('cfg-llm-model').value.trim();
    var tmp=parseFloat(document.getElementById('cfg-llm-temp').value);
    var ret=parseInt(document.getElementById('cfg-llm-retries').value);
    cfg.LLM={endpoint:ep||'https://openrouter.ai/api/v1'};
    if(ak)cfg.LLM.apiKey=ak;
    if(mdl)cfg.LLM.model=mdl;
    if(!isNaN(tmp))cfg.LLM.temp=tmp;
    if(!isNaN(ret)&&ret>0)cfg.LLM.maxRetries=ret;
  }else{
    delete cfg.LLM;
  }
  ta.value=JSON.stringify(cfg,null,2);
}

// Sync JSON textarea → LLM UI fields
function jsonToLLMFields(cfg){
  var chk=document.getElementById('cfg-llm-enable');
  var fields=document.getElementById('cfg-llm-fields');
  if(cfg.LLM&&cfg.LLM.endpoint){
    chk.checked=true;
    fields.style.display='';
    document.getElementById('cfg-llm-endpoint').value=cfg.LLM.endpoint||'';
    document.getElementById('cfg-llm-apikey').value=cfg.LLM.apiKey||'';
    document.getElementById('cfg-llm-model').value=cfg.LLM.model||'';
    document.getElementById('cfg-llm-temp').value=cfg.LLM.temp!=null?cfg.LLM.temp:'';
    document.getElementById('cfg-llm-retries').value=cfg.LLM.maxRetries||'';
  }else{
    chk.checked=false;
    fields.style.display='none';
    document.getElementById('cfg-llm-endpoint').value='';
    document.getElementById('cfg-llm-apikey').value='';
    document.getElementById('cfg-llm-model').value='';
    document.getElementById('cfg-llm-temp').value='';
    document.getElementById('cfg-llm-retries').value='';
  }
}

window.toggleLLMConfig=function(){
  var chk=document.getElementById('cfg-llm-enable');
  document.getElementById('cfg-llm-fields').style.display=chk.checked?'':'none';
  if(chk.checked&&!document.getElementById('cfg-llm-endpoint').value){
    document.getElementById('cfg-llm-endpoint').value='https://openrouter.ai/api/v1';
  }
  llmFieldsToJSON();
};

// Hook blur events on LLM inputs to sync back to JSON
['cfg-llm-endpoint','cfg-llm-apikey','cfg-llm-model','cfg-llm-temp','cfg-llm-retries'].forEach(function(id){
  var el=document.getElementById(id);
  if(el)el.addEventListener('change',llmFieldsToJSON);
});

window.loadConfig=function(){
  api('/api/config').then(function(d){
    document.getElementById('config-editor').value=JSON.stringify(d,null,2);
    jsonToLLMFields(d);
    if(d.LLM&&d.LLM.endpoint)window._llmDynamic=true;
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};
window.saveConfig=function(){
  llmFieldsToJSON();
  var v=document.getElementById('config-editor').value;
  try{var parsed=JSON.parse(v)}catch(e){toast('Невалидный JSON: '+e.message,'error');return}
  if(parsed.LLM&&parsed.LLM.endpoint)window._llmDynamic=true;
  else window._llmDynamic=false;
  post('/api/config',{json:v}).then(function(r){if(r.success)toast('JSON конфиг сохранён');else toast(r.ex||'Ошибка','error')});
};
})();

// === block 2 (orig admin_tg_panel.go lines 8760-8845) ===
(function(){
var basePath=location.pathname.replace(/\/+$/,'');
function api(p,o){return fetch(basePath+p,o).then(function(r){return r.json()})}

function setLampaStatus(msg,type){
  var el=document.getElementById('lampa-status');
  el.style.display='block';
  el.textContent=msg;
  if(type==='ok'){el.style.background='var(--accent-a13)';el.style.color='var(--accent)';el.style.border='1px solid var(--accent-a25)'}
  else if(type==='warn'){el.style.background='var(--warning-bg)';el.style.color='var(--warning)';el.style.border='1px solid var(--warning-border)'}
  else if(type==='error'){el.style.background='var(--danger-bg)';el.style.color='var(--danger)';el.style.border='1px solid var(--danger-border)'}
  else{el.style.background='rgba(255,255,255,0.08)';el.style.color='var(--text-muted)';el.style.border='1px solid var(--border)'}
}

window.loadLampa=function(){checkLampaVersion()};

window.checkLampaVersion=function(){
  document.getElementById('lampa-check-btn').disabled=true;
  document.getElementById('lampa-check-btn').textContent='Проверка...';
  setLampaStatus('Запрос к GitHub...','info');
  fetch(basePath+'/api/lampa/version').then(function(r){
    return r.text().then(function(t){
      try{return JSON.parse(t)}catch(e){throw new Error('HTTP '+r.status+': '+t.substring(0,200))}
    });
  }).then(function(d){
    document.getElementById('lampa-check-btn').disabled=false;
    document.getElementById('lampa-check-btn').textContent='Проверить обновления';
    if(d.error&&!d.local){setLampaStatus('Ошибка: '+d.error,'error');return}
    var loc=d.local||{};
    var rem=d.remote||{};
    document.getElementById('lampa-local-ver').textContent=loc.version||'не установлен';
    document.getElementById('lampa-local-hash').textContent=loc.hash?(loc.commit?'commit: '+loc.commit.substring(0,8):'hash: '+loc.hash):'';
    if(rem.version){
      document.getElementById('lampa-remote-ver').textContent=rem.version;
      document.getElementById('lampa-remote-hash').textContent=rem.commit?'commit: '+rem.commit.substring(0,8):'';
    }else{
      document.getElementById('lampa-remote-ver').textContent='н/д';
      document.getElementById('lampa-remote-hash').textContent=d.error||'';
    }
    if(d.update_available){
      setLampaStatus('Доступно обновление: '+loc.version+' → '+rem.version,'warn');
      document.getElementById('lampa-update-btn').style.display='';
      document.getElementById('lampa-full-btn').style.display='';
    }else if(rem.version){
      setLampaStatus('Установлена актуальная версия','ok');
      document.getElementById('lampa-update-btn').style.display='none';
      document.getElementById('lampa-full-btn').style.display='none';
    }
  }).catch(function(e){
    document.getElementById('lampa-check-btn').disabled=false;
    document.getElementById('lampa-check-btn').textContent='Проверить обновления';
    setLampaStatus('Ошибка: '+e.message,'error');
  });
};

window.updateLampa=function(full){
  var btn=full?document.getElementById('lampa-full-btn'):document.getElementById('lampa-update-btn');
  btn.disabled=true;
  btn.textContent=full?'Синхронизация...':'Обновление...';
  setLampaStatus(full?'Полная синхронизация с GitHub... (может занять 1-2 минуты)':'Скачивание обновлений...','info');
  document.getElementById('lampa-files').style.display='none';
  var url='/api/lampa/update'+(full?'?full=true':'');
  fetch(basePath+url,{method:'POST'}).then(function(r){
    return r.text().then(function(t){
      try{return JSON.parse(t)}catch(e){throw new Error('HTTP '+r.status+': '+t.substring(0,200))}
    });
  }).then(function(d){
    btn.disabled=false;
    btn.textContent=full?'Полная синхронизация':'Обновить (быстро)';
    if(d.error){setLampaStatus('Ошибка: '+d.error,'error');return}
    setLampaStatus('Обновлено: '+d.old_version+' \u2192 '+d.new_version+' ('+d.files_updated.length+' файлов)','ok');
    document.getElementById('lampa-local-ver').textContent=d.new_version;
    document.getElementById('lampa-update-btn').style.display='none';
    document.getElementById('lampa-full-btn').style.display='none';
    if(d.files_updated&&d.files_updated.length>0){
      document.getElementById('lampa-files').style.display='block';
      document.getElementById('lampa-files-list').innerHTML=d.files_updated.map(function(f){return '<div style="padding:2px 0;color:var(--text-muted)">'+f+'</div>'}).join('');
    }
    checkLampaVersion();
  }).catch(function(e){
    btn.disabled=false;
    btn.textContent=full?'Полная синхронизация':'Обновить (быстро)';
    setLampaStatus('Ошибка: '+e.message,'error');
  });
};
})();

// === block 3 (orig admin_tg_panel.go lines 8848-9065) ===
// ===== Self-update =====
(function(){
var basePath=location.pathname.replace(/\/+$/,'');
function api(p){return fetch(basePath+p).then(function(r){return r.json()})}
function post(p){return fetch(basePath+p,{method:'POST'}).then(function(r){return r.json()})}
function esc(s){if(s==null)return'';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;')}
function fmtDate(s){if(!s)return'';try{return new Date(s).toLocaleString()}catch(e){return s}}

function setStatus(msg,type){
  var el=document.getElementById('su-status');
  if(!el)return;
  el.style.display='block';el.textContent=msg;
  if(type==='ok'){el.style.background='var(--accent-a13)';el.style.color='var(--accent)';el.style.border='1px solid var(--accent-a25)'}
  else if(type==='warn'){el.style.background='var(--warning-bg)';el.style.color='var(--warning)';el.style.border='1px solid var(--warning-border)'}
  else if(type==='error'){el.style.background='var(--danger-bg)';el.style.color='var(--danger)';el.style.border='1px solid var(--danger-border)'}
  else{el.style.background='rgba(255,255,255,0.08)';el.style.color='var(--text-muted)';el.style.border='1px solid var(--border)'}
}

function render(info){
  if(!info)return;
  document.getElementById('su-current').textContent=info.current||'—';
  document.getElementById('su-commit').textContent=(info.commit?('commit '+String(info.commit).substring(0,10)):'')+(info.build_date?(' · '+info.build_date.substring(0,10)):'');
  document.getElementById('su-mode').textContent=info.mode||'—';
  document.getElementById('su-mode-sub').textContent=info.can_self_update?'самообновление доступно':'только ручное обновление';
  document.getElementById('su-asset').textContent=info.asset||'—';
  document.getElementById('su-channel').textContent='канал: '+(info.channel||'stable')+(info.token_set?' 🔒':'')+(info.auto_install?' · авто':'');
  var chIn=document.getElementById('su-ch-name');
  if(chIn&&document.activeElement!==chIn&&!chIn.value)chIn.value=info.channel||'stable';
  var chSt=document.getElementById('su-ch-status');
  if(chSt&&!chSt.textContent)chSt.textContent=info.token_set?'пароль канала задан':'';
  document.getElementById('su-interval').textContent=info.check_interval||'—';
  document.getElementById('su-window').textContent=info.maintenance_window||'любое время';
  var sigEl=document.getElementById('su-sig');
  if(info.require_signature){sigEl.textContent='обязательна (minisign)';sigEl.style.color='var(--accent)'}
  else if(info.signature_ready){sigEl.textContent='minisign настроен';sigEl.style.color='var(--accent)'}
  else{sigEl.textContent='только SHA256';sigEl.style.color='var(--warning)'}
  var sc=document.getElementById('su-streams');
  sc.textContent=info.active_streams||0;
  sc.style.color=(info.active_streams&&info.active_streams>0)?'var(--warning)':'var(--text-dim)';

  if(info.latest_version){
    document.getElementById('su-latest').textContent=info.latest_version;
    document.getElementById('su-latest-date').textContent=info.latest&&info.latest.published_at?fmtDate(info.latest.published_at):'';
  }else{
    document.getElementById('su-latest').textContent=info.last_check?'не найдено':'не проверялось';
    document.getElementById('su-latest-date').textContent='';
  }

  var applyBtn=document.getElementById('su-apply-btn');
  var rollbackBtn=document.getElementById('su-rollback-btn');
  var dockerBox=document.getElementById('su-docker-hint');
  applyBtn.style.display='none';rollbackBtn.style.display='none';dockerBox.style.display='none';

  if(info.update_available){
    if(info.can_self_update){
      applyBtn.style.display='';
      setStatus('Доступно обновление: '+info.current+' → '+info.latest_version,'warn');
    }else{
      dockerBox.style.display='';
      var cmd=(info.mode==='docker')
        ? 'docker compose pull && docker compose up -d   # образ тянется с вашего registry'
        : 'go run ./cmd/lampac-go   # dev-режим, самообновление отключено';
      document.getElementById('su-docker-cmd').textContent=cmd;
      setStatus('Доступно обновление: '+info.current+' → '+info.latest_version+' (режим '+info.mode+' — только вручную)','warn');
    }
  }else if(info.latest_version){
    setStatus('Установлена актуальная версия','ok');
  }
  if(info.has_rollback&&info.can_self_update){
    rollbackBtn.style.display='';
  }
  if(info.last_error){
    setStatus('Ошибка: '+info.last_error,'error');
  }

  // Release notes
  if(info.latest&&info.latest.body){
    document.getElementById('su-release-notes').style.display='';
    document.getElementById('su-release-notes-body').textContent=info.latest.body;
    var link=document.getElementById('su-release-link');
    if(info.latest.html_url){link.href=info.latest.html_url;link.style.display=''}else{link.style.display='none'}
  }else{
    document.getElementById('su-release-notes').style.display='none';
  }

  // History
  var hl=document.getElementById('su-history-list');
  if(info.history&&info.history.length){
    var rows=info.history.slice().reverse().map(function(h){
      var cls=h.ok?'su-hist-ok':'su-hist-err';
      var verb={check:'проверка',apply:'установка',rollback:'откат',error:'ошибка'}[h.event]||h.event;
      var from=h.from?(' '+h.from):'';
      var to=h.to?(' → '+h.to):'';
      return '<div class="su-hist-row"><span class="'+cls+'">['+verb+']</span> '+esc(fmtDate(h.time))+from+to+(h.message?' · '+esc(h.message):'')+'</div>';
    });
    hl.innerHTML=rows.join('');
  }else{
    hl.textContent='история пуста';
  }
}

window.loadSelfUpdate=function(){
  api('/api/update/status').then(function(d){render(d)}).catch(function(e){setStatus('Ошибка: '+e.message,'error')});
};

window.suCheck=function(){
  var btn=document.getElementById('su-check-btn');
  btn.disabled=true;btn.textContent='Проверка...';
  setStatus('Запрос к GitHub...','info');
  post('/api/update/check').then(function(d){
    btn.disabled=false;btn.textContent='Проверить обновления';
    if(d.error&&!d.info){setStatus('Ошибка: '+d.error,'error');return}
    render(d.info||d);
  }).catch(function(e){
    btn.disabled=false;btn.textContent='Проверить обновления';
    setStatus('Ошибка: '+e.message,'error');
  });
};

window.suApply=function(){
  var streams=parseInt(document.getElementById('su-streams').textContent,10)||0;
  var extra=streams>0?('\n\n\u26A0 Сейчас активно '+streams+' стрим(ов) — пользователи будут прерваны.'):'';
  if(!confirm('Скачать новую версию и перезапустить сервер?'+extra+'\n\nПри самообновлении lampac-go заменит свой бинарник и сделает re-exec. PID сохраняется.'))return;
  var btn=document.getElementById('su-apply-btn');
  btn.disabled=true;btn.textContent='Установка...';
  setStatus('Скачивание и установка обновления...','info');
  // Snapshot last_error before apply so we can detect a NEW error after re-exec.
  var preErr='';
  api('/api/update/status').then(function(info){if(info)preErr=info.last_error||''}).catch(function(){});
  post('/api/update/apply').then(function(d){
    if(d.error){btn.disabled=false;btn.textContent='Установить и перезапустить';setStatus('Ошибка: '+d.error,'error');return}
    setStatus('Бинарник заменён, процесс перезапускается. Подождите ~10 сек и перезагрузите страницу.','ok');
    // Poll status: either current==latest (success) or a fresh last_error
    // appears (Restart failed after a successful swap).
    var attempts=0;
    var iv=setInterval(function(){
      attempts++;
      api('/api/update/status').then(function(info){
        if(!info)return;
        if(info.current&&info.latest_version&&info.current===info.latest_version){
          clearInterval(iv);
          setStatus('Успешно обновлено до '+info.current+'. Страница перезагружается...','ok');
          setTimeout(function(){location.reload()},1000);
          return;
        }
        if(info.last_error&&info.last_error!==preErr){
          clearInterval(iv);
          btn.disabled=false;btn.textContent='Установить и перезапустить';
          setStatus('Ошибка: '+info.last_error,'error');
        }
      }).catch(function(){});
      if(attempts>30){clearInterval(iv);btn.disabled=false;btn.textContent='Установить и перезапустить';setStatus('Не дождались перезапуска. Проверьте server.log','warn')}
    },2000);
  }).catch(function(e){
    btn.disabled=false;btn.textContent='Установить и перезапустить';
    setStatus('Ошибка: '+e.message,'error');
  });
};

window.suSaveChannel=function(){
  var name=(document.getElementById('su-ch-name').value||'').trim().toLowerCase();
  var pass=document.getElementById('su-ch-pass').value||'';
  var st=document.getElementById('su-ch-status');
  var btn=document.getElementById('su-ch-save');
  btn.disabled=true;st.textContent='Сохранение и проверка…';st.style.color='var(--text-muted)';
  fetch(basePath+'/api/update/channel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({channel:name,token:pass})})
    .then(function(r){return r.json()})
    .then(function(d){
      btn.disabled=false;
      if(d.error){st.textContent='✗ '+d.error;st.style.color='var(--danger)';if(d.info)render(d.info);return}
      st.textContent='✓ Канал «'+(d.channel||'stable')+'» подключён';st.style.color='var(--accent)';
      document.getElementById('su-ch-pass').value='';
      if(d.info)render(d.info);
    })
    .catch(function(e){btn.disabled=false;st.textContent='✗ '+e.message;st.style.color='var(--danger)'});
};

window.suRollback=function(){
  if(!confirm('Откатиться к предыдущей версии?'))return;
  post('/api/update/rollback').then(function(d){
    if(d.error){setStatus('Ошибка: '+d.error,'error');return}
    setStatus('Откат запущен, сервер перезапустится...','warn');
    setTimeout(function(){location.reload()},5000);
  }).catch(function(e){setStatus('Ошибка: '+e.message,'error')});
};

// ---- Reference config ----
function rcSetStatus(msg,color){
  var el=document.getElementById('rc-status');
  el.textContent=msg||''; el.style.color=color||'var(--text-muted)';
}
window.rcFetch=function(){
  var btn=document.getElementById('rc-fetch-btn');
  btn.disabled=true; btn.textContent='Запрашиваем…';
  rcSetStatus('Обращаемся к серверу обновлений…','var(--text-muted)');
  api('/api/refconfig').then(function(d){
    btn.disabled=false; btn.textContent='Запросить с сервера';
    if(d.error){rcSetStatus('Ошибка: '+d.error,'var(--warning)');return}
    var srv=d.server||{};
    var body=srv.body||'';
    var txt=document.getElementById('rc-body');
    txt.value=body; txt.style.display='';
    var meta=document.getElementById('rc-meta');
    meta.style.display='';
    meta.innerHTML='версия сервера: <b>v'+(srv.version||0)+'</b>'+
      (srv.updated_by?' · обновил '+esc(srv.updated_by):'')+
      (srv.updated_at?' · '+esc(fmtDate(srv.updated_at)):'')+
      (d.local_path?' · локально: <code>'+esc(d.local_path)+'</code>':'');
    if(srv.note){
      var n=document.getElementById('rc-note'); n.style.display=''; n.textContent='📌 '+srv.note;
    }
    if(d.local_path){document.getElementById('rc-path').textContent=d.local_path}
    document.getElementById('rc-save-btn').style.display='';
    if(d.local_body && d.local_body===body){
      rcSetStatus('Локальная копия совпадает с сервером','var(--accent)');
    }else if(d.local_body){
      rcSetStatus('Есть отличия от локальной копии — проверьте и сохраните','var(--warning)');
    }else{
      rcSetStatus('Локальной копии нет — можно сохранить','var(--text-muted)');
    }
  }).catch(function(e){
    btn.disabled=false; btn.textContent='Запросить с сервера';
    rcSetStatus('Ошибка: '+e.message,'var(--warning)');
  });
};
window.rcSave=function(){
  var body=document.getElementById('rc-body').value;
  if(!confirm('Сохранить этот TOML в config.reference.toml? Ваш основной config.toml не будет изменён.'))return;
  var btn=document.getElementById('rc-save-btn');
  btn.disabled=true; btn.textContent='Сохраняем…';
  post('/api/refconfig/save',{body:body}).then(function(d){
    btn.disabled=false; btn.textContent='💾 Сохранить в config.reference.toml';
    if(d.error){rcSetStatus('Ошибка: '+d.error,'var(--warning)');return}
    rcSetStatus('Сохранено в '+(d.path||'config.reference.toml')+'. Примените изменения вручную.','var(--accent)');
  }).catch(function(e){
    btn.disabled=false; btn.textContent='💾 Сохранить в config.reference.toml';
    rcSetStatus('Ошибка: '+e.message,'var(--warning)');
  });
};
})();

// === block 4 (orig admin_tg_panel.go lines 9068-9835) ===
(function(){
var basePath=location.pathname.replace(/\/+$/,'');
function api(p,o){return fetch(basePath+p,o).then(function(r){return r.json()})}
function post(p,d){return api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(d)})}
function esc(s){if(s==null)return'';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;')}

var fbCache=[];
var catLabels={help:'Помощь',thanks:'Благодарность',bug:'Баг',feature:'Фича',other:'Другое'};
var priLabels={low:'Низкий',medium:'Средний',high:'Высокий',critical:'Критический'};
var stLabels={open:'Открыт',in_progress:'В работе',resolved:'Решён',closed:'Закрыт'};
var stClass={open:'s-open',in_progress:'s-progress',resolved:'s-resolved',closed:'s-closed'};

window.loadFeedback=function(){
  api('/api/feedback/stats').then(function(s){
    document.getElementById('fb-stats').innerHTML=
      '<div class="fb-stat s-open"><div class="fb-val">'+s.open+'</div><div class="fb-lbl">Открытые</div></div>'+
      '<div class="fb-stat s-progress"><div class="fb-val">'+s.in_progress+'</div><div class="fb-lbl">В работе</div></div>'+
      '<div class="fb-stat s-resolved"><div class="fb-val">'+s.resolved+'</div><div class="fb-lbl">Решённые</div></div>'+
      '<div class="fb-stat s-closed"><div class="fb-val">'+s.closed+'</div><div class="fb-lbl">Закрытые</div></div>';
  });
  fbApplyFilters();
};

window.fbApplyFilters=function(){
  var p=[];
  var s=document.getElementById('fb-f-status').value;
  var c=document.getElementById('fb-f-cat').value;
  var pr=document.getElementById('fb-f-pri').value;
  var q=document.getElementById('fb-f-search').value;
  if(s)p.push('status='+s);
  if(c)p.push('category='+c);
  if(pr)p.push('priority='+pr);
  if(q)p.push('search='+encodeURIComponent(q));
  var qs=p.length?'?'+p.join('&'):'';
  api('/api/feedback'+qs).then(function(d){
    fbCache=d.tickets||[];
    fbRender(fbCache);
  }).catch(function(e){window.toast('Ошибка: '+e.message,'error')});
};

function fbRender(list){
  var body=document.getElementById('fb-tbody');
  var empty=document.getElementById('fb-empty');
  if(!list.length){body.innerHTML='';empty.style.display='';return}
  empty.style.display='none';
  body.innerHTML=list.map(function(t){
    var replies=t.replies?t.replies.length:0;
    var cntCls=replies?'fb-cnt':'fb-cnt zero';
    return '<tr onclick="fbOpenDetail(\''+t.id+'\')">'+
      '<td><span class="fb-cat fb-cat-'+t.category+'">'+esc(catLabels[t.category]||t.category)+'</span></td>'+
      '<td><span class="fb-pri fb-pri-'+t.priority+'">'+esc(priLabels[t.priority]||t.priority)+'</span></td>'+
      '<td style="max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">'+esc(t.subject)+'</td>'+
      '<td style="color:var(--text-muted);font-size:12px">'+esc(t.user_name||t.user_id)+'</td>'+
      '<td><span class="fb-st fb-st-'+t.status+'">'+esc(stLabels[t.status]||t.status)+'</span></td>'+
      '<td style="text-align:center"><span class="'+cntCls+'">'+replies+'</span></td>'+
      '<td style="color:var(--text-dim);font-size:12px;white-space:nowrap">'+fbDate(t.created_at)+'</td>'+
      '</tr>';
  }).join('');
}

function fbDate(s){if(!s)return'';return s.substring(0,16).replace('T',' ')}

window.fbOpenDetail=function(id){
  var t=null;
  for(var i=0;i<fbCache.length;i++){if(fbCache[i].id===id){t=fbCache[i];break}}
  if(!t)return;
  var h='<div style="font-size:18px;font-weight:700;color:var(--text);margin-bottom:4px;padding-right:40px">'+esc(t.subject)+'</div>';
  h+='<div class="fb-meta-row">';
  h+='<span class="fb-cat fb-cat-'+t.category+'">'+esc(catLabels[t.category]||t.category)+'</span>';
  h+='<span class="fb-pri fb-pri-'+t.priority+'">'+esc(priLabels[t.priority]||t.priority)+'</span>';
  h+='<span class="fb-st fb-st-'+t.status+'">'+esc(stLabels[t.status]||t.status)+'</span>';
  h+='</div>';
  h+='<div class="fb-meta-row">';
  h+='<span>👤 '+esc(t.user_name||t.user_id)+'</span>';
  h+='<span>📅 '+fbDate(t.created_at)+'</span>';
  if(t.updated_at&&t.updated_at!==t.created_at)h+='<span>✏️ '+fbDate(t.updated_at)+'</span>';
  h+='</div>';
  h+='<div class="fb-msg-block">'+esc(t.message)+'</div>';

  // status control
  h+='<div style="margin:16px 0;display:flex;gap:10px;align-items:center;flex-wrap:wrap">';
  h+='<select id="fb-d-status" class="input-sm" style="width:auto">';
  ['open','in_progress','resolved','closed'].forEach(function(s){
    h+='<option value="'+s+'"'+(t.status===s?' selected':'')+'>'+esc(stLabels[s])+'</option>';
  });
  h+='</select>';
  h+='<button class="btn btn-primary btn-sm" onclick="fbSetStatus(\''+t.id+'\')">Применить</button>';

  h+='<select id="fb-d-pri" class="input-sm" style="width:auto">';
  ['low','medium','high','critical'].forEach(function(p){
    h+='<option value="'+p+'"'+(t.priority===p?' selected':'')+'>'+esc(priLabels[p])+'</option>';
  });
  h+='</select>';
  h+='<button class="btn btn-warning btn-sm" onclick="fbSetPriority(\''+t.id+'\')">Приоритет</button>';
  h+='<button class="btn btn-danger btn-sm" onclick="fbDelete(\''+t.id+'\')" style="margin-left:auto">Удалить</button>';
  h+='</div>';

  // replies thread
  if(t.replies&&t.replies.length){
    h+='<div style="font-size:12px;color:var(--text-muted);text-transform:uppercase;margin:16px 0 8px;letter-spacing:.5px">Ответы ('+t.replies.length+')</div>';
    h+='<div class="fb-thread">';
    t.replies.forEach(function(r){
      h+='<div class="fb-reply-card">';
      h+='<div class="fb-reply-meta"><span style="color:var(--accent);font-weight:600">Администратор</span><span>'+fbDate(r.created_at)+'</span></div>';
      h+='<div class="fb-reply-body">'+esc(r.message)+'</div>';
      h+='</div>';
    });
    h+='</div>';
  }

  // reply form
  h+='<div style="margin-top:20px">';
  h+='<div style="font-size:12px;color:var(--text-muted);text-transform:uppercase;margin-bottom:8px;letter-spacing:.5px">Ответить</div>';
  h+='<textarea class="fb-reply-area" id="fb-d-reply" placeholder="Введите ответ..."></textarea>';
  h+='<div style="margin-top:10px;display:flex;gap:10px">';
  h+='<button class="btn btn-primary" onclick="fbReply(\''+t.id+'\')">Отправить</button>';
  h+='</div>';
  h+='</div>';

  document.getElementById('fb-detail').innerHTML=h;
  document.getElementById('fb-overlay').classList.add('open');
  document.getElementById('fb-slide').classList.add('open');
};

window.fbCloseDetail=function(){
  document.getElementById('fb-overlay').classList.remove('open');
  document.getElementById('fb-slide').classList.remove('open');
};

window.fbSetStatus=function(id){
  var s=document.getElementById('fb-d-status').value;
  post('/api/feedback',{action:'set_status',ticket_id:id,status:s}).then(function(d){
    if(d.ok){window.toast('Статус обновлён');fbCloseDetail();loadFeedback()}
    else window.toast(d.error||'Ошибка','error');
  }).catch(function(e){window.toast('Ошибка: '+e.message,'error')});
};

window.fbSetPriority=function(id){
  var p=document.getElementById('fb-d-pri').value;
  post('/api/feedback',{action:'set_priority',ticket_id:id,priority:p}).then(function(d){
    if(d.ok){window.toast('Приоритет обновлён');fbCloseDetail();loadFeedback()}
    else window.toast(d.error||'Ошибка','error');
  }).catch(function(e){window.toast('Ошибка: '+e.message,'error')});
};

window.fbReply=function(id){
  var msg=document.getElementById('fb-d-reply').value.trim();
  if(!msg){window.toast('Введите текст ответа','error');return}
  post('/api/feedback',{action:'reply',ticket_id:id,message:msg}).then(function(d){
    if(d.ok){window.toast('Ответ отправлен');
      // reload detail
      api('/api/feedback').then(function(dd){
        fbCache=dd.tickets||[];
        fbOpenDetail(id);
        loadFeedback();
      });
    }else window.toast(d.error||'Ошибка','error');
  }).catch(function(e){window.toast('Ошибка: '+e.message,'error')});
};

window.fbDelete=function(id){
  if(!confirm('Удалить тикет? Это действие необратимо.'))return;
  post('/api/feedback',{action:'delete',ticket_id:id}).then(function(d){
    if(d.ok){window.toast('Тикет удалён');fbCloseDetail();loadFeedback()}
    else window.toast(d.error||'Ошибка','error');
  }).catch(function(e){window.toast('Ошибка: '+e.message,'error')});
};

// --- Alice ---
window.loadAlice=function(){
  api('/api/alice').then(function(d){
    var pairs=d.pairings||[];
    document.getElementById('alice-counter').textContent=pairs.length+' привязок';
    var statusEl=document.getElementById('alice-status');
    statusEl.innerHTML='<div style="display:inline-flex;align-items:center;gap:6px;background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:8px 14px;font-size:13px"><span style="width:8px;height:8px;border-radius:50%;background:var(--accent)"></span><span style="color:var(--text)">Навык активен</span><span style="color:var(--text-muted)">\u00b7 '+pairs.length+' привязок</span></div>';
    if(pairs.length===0){
      document.getElementById('alice-table').innerHTML='<div style="color:var(--text-dim);font-size:13px;padding:16px 0">Нет привязанных устройств. Пользователи привяжут Алису через навык.</div>';
      return;
    }
    var h='<table style="width:100%;border-collapse:collapse;font-size:13px">';
    h+='<thead><tr style="border-bottom:1px solid rgba(255,255,255,0.06)"><th style="text-align:left;padding:8px 12px;color:var(--text-muted);font-weight:600">Application ID</th><th style="text-align:left;padding:8px 12px;color:var(--text-muted);font-weight:600">Telegram ID</th><th style="width:80px"></th></tr></thead><tbody>';
    for(var i=0;i<pairs.length;i++){
      var p=pairs[i];
      var shortApp=p.app_id.length>16?p.app_id.substring(0,16)+'\u2026':p.app_id;
      h+='<tr style="border-bottom:1px solid var(--surface2)">';
      h+='<td style="padding:8px 12px;color:var(--text);font-family:monospace;font-size:11px" title="'+esc(p.app_id)+'">'+esc(shortApp)+'</td>';
      h+='<td style="padding:8px 12px;color:var(--text)">'+p.tg_id+'</td>';
      h+='<td style="padding:8px 12px;text-align:right"><button class="btn-sm btn-danger" onclick="deleteAlicePairing(\''+esc(p.app_id)+'\')">Удалить</button></td>';
      h+='</tr>';
    }
    h+='</tbody></table>';
    document.getElementById('alice-table').innerHTML=h;
  }).catch(function(e){
    document.getElementById('alice-status').innerHTML='<div style="display:inline-flex;align-items:center;gap:6px;background:var(--surface);border:1px solid rgba(255,255,255,0.06);border-radius:8px;padding:8px 14px;font-size:13px"><span style="width:8px;height:8px;border-radius:50%;background:var(--text-dim)"></span><span style="color:var(--text-muted)">Навык не настроен</span></div>';
    document.getElementById('alice-table').innerHTML='<div style="color:var(--text-dim);font-size:13px;padding:16px 0">Алиса не включена в config.toml ([alice] enable = true)</div>';
  });
};
window.deleteAlicePairing=function(appID){
  if(!confirm('Удалить привязку '+appID.substring(0,16)+'...?'))return;
  api('/api/alice/'+encodeURIComponent(appID),{method:'DELETE'}).then(function(d){
    if(d.ok){toast('Привязка удалена');loadAlice();}
    else toast(d.error||'Ошибка','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

// --- Skip Intro ---
window.loadSkipIntro=function(){
  api('/api/skip').then(function(d){
    document.getElementById('skip-stats').innerHTML='<span style="color:var(--accent)">'+d.total_shows+'</span> шоу, <span style="color:var(--accent)">'+d.total_episodes+'</span> эпизодов, <span style="color:var(--accent)">'+d.user_marks+'</span> маркеров';
    loadSkipMarks();
  }).catch(function(e){
    document.getElementById('skip-stats').innerHTML='<span style="color:var(--danger)">Ошибка: '+esc(e.message)+'</span>';
  });
};
window.skipSearch=function(){
  var imdb=document.getElementById('skip-imdb').value.trim();
  if(!imdb){toast('Введите IMDb ID','error');return;}
  api('/api/skip?imdb_id='+encodeURIComponent(imdb)).then(function(d){
    var show=d.show;
    var el=document.getElementById('skip-show-data');
    if(!show||Object.keys(show).length===0){
      el.innerHTML='<div style="color:var(--text-dim);font-size:13px">Нет данных для '+esc(imdb)+'</div>';
      return;
    }
    var h='<table style="width:100%;border-collapse:collapse;font-size:13px">';
    h+='<thead><tr style="border-bottom:1px solid rgba(255,255,255,0.06)"><th style="text-align:left;padding:8px;color:var(--text-muted)">Сезон</th><th style="text-align:left;padding:8px;color:var(--text-muted)">Эпизод</th><th style="text-align:left;padding:8px;color:var(--text-muted)">Сегменты</th><th style="width:80px"></th></tr></thead><tbody>';
    var seasons=Object.keys(show).sort();
    for(var si=0;si<seasons.length;si++){
      var sk=seasons[si];
      var eps=Object.keys(show[sk]).sort();
      for(var ei=0;ei<eps.length;ei++){
        var ek=eps[ei];
        var segs=show[sk][ek];
        var segStr=segs.map(function(s){return s.type+' ['+s.start.toFixed(1)+'s\u2013'+s.end.toFixed(1)+'s]'}).join(', ');
        var sNum=sk.replace('s','');
        var eNum=ek.replace('e','');
        h+='<tr style="border-bottom:1px solid var(--surface2)">';
        h+='<td style="padding:8px;color:var(--text)">'+esc(sk)+'</td>';
        h+='<td style="padding:8px;color:var(--text)">'+esc(ek)+'</td>';
        h+='<td style="padding:8px;color:var(--text-muted);font-size:12px">'+esc(segStr)+'</td>';
        h+='<td style="padding:8px;text-align:right"><button class="btn-sm btn-danger" onclick="skipDelete(\''+esc(imdb)+'\','+sNum+','+eNum+')">Удалить</button></td>';
        h+='</tr>';
      }
    }
    h+='</tbody></table>';
    el.innerHTML=h;
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};
window.skipDelete=function(imdb,s,e){
  if(!confirm('Удалить сегменты для '+imdb+' S'+s+'E'+e+'?'))return;
  api('/api/skip/'+encodeURIComponent(imdb)+'/'+s+'/'+e,{method:'DELETE'}).then(function(d){
    if(d.ok){toast('Удалено');skipSearch();}
    else toast(d.error||'Ошибка','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};
window.skipAddSegment=function(){
  var imdb=document.getElementById('skip-add-imdb').value.trim();
  var s=parseInt(document.getElementById('skip-add-s').value)||0;
  var e=parseInt(document.getElementById('skip-add-e').value)||0;
  var type=document.getElementById('skip-add-type').value;
  var start=parseFloat(document.getElementById('skip-add-start').value)||0;
  var end=parseFloat(document.getElementById('skip-add-end').value)||0;
  if(!imdb){toast('Введите IMDb ID','error');return;}
  if(end<=start){toast('End должен быть > Start','error');return;}
  post('/api/skip',{imdb_id:imdb,season:s,episode:e,segments:[{type:type,start:start,end:end}]}).then(function(d){
    if(d.ok){toast('Сегмент добавлен');document.getElementById('skip-imdb').value=imdb;skipSearch();loadSkipIntro();}
    else toast(d.error||'Ошибка','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};
function loadSkipMarks(){
  api('/api/skip/admin/marks').then(function(d){
    var marks=d.marks||[];
    var el=document.getElementById('skip-marks-list');
    if(marks.length===0){el.innerHTML='<div style="color:var(--text-dim);font-size:13px">Нет ожидающих маркеров</div>';return;}
    var h='<table style="width:100%;border-collapse:collapse;font-size:13px">';
    h+='<thead><tr style="border-bottom:1px solid rgba(255,255,255,0.06)"><th style="text-align:left;padding:8px;color:var(--text-muted)">IMDb</th><th style="padding:8px;color:var(--text-muted)">S</th><th style="padding:8px;color:var(--text-muted)">E</th><th style="padding:8px;color:var(--text-muted)">Тип</th><th style="padding:8px;color:var(--text-muted)">Начало</th><th style="padding:8px;color:var(--text-muted)">Конец</th><th style="width:120px"></th></tr></thead><tbody>';
    for(var i=0;i<marks.length;i++){
      var m=marks[i];
      h+='<tr style="border-bottom:1px solid var(--surface2)">';
      h+='<td style="padding:8px;color:var(--text);font-family:monospace;font-size:11px">'+esc(m.imdb_id)+'</td>';
      h+='<td style="padding:8px;color:var(--text);text-align:center">'+m.season+'</td>';
      h+='<td style="padding:8px;color:var(--text);text-align:center">'+m.episode+'</td>';
      h+='<td style="padding:8px;color:var(--text-muted)">'+esc(m.type)+'</td>';
      h+='<td style="padding:8px;color:var(--text-muted)">'+m.start.toFixed(1)+'s</td>';
      h+='<td style="padding:8px;color:var(--text-muted)">'+m.end.toFixed(1)+'s</td>';
      h+='<td style="padding:8px;text-align:right;white-space:nowrap"><button class="btn-sm" style="background:var(--accent);color:var(--bg);margin-right:4px" onclick="skipMarkAction('+i+',\'approve\')">&#10003;</button><button class="btn-sm btn-danger" onclick="skipMarkAction('+i+',\'reject\')">&#10005;</button></td>';
      h+='</tr>';
    }
    h+='</tbody></table>';
    el.innerHTML=h;
  }).catch(function(){});
}
window.skipMarkAction=function(idx,action){
  var label=action==='approve'?'Одобрить':'Отклонить';
  if(!confirm(label+' маркер #'+idx+'?'))return;
  post('/api/skip/marks',{index:idx,action:action}).then(function(d){
    if(d.ok){toast(label==='Одобрить'?'Маркер одобрен':'Маркер отклонён');loadSkipIntro();}
    else toast(d.error||'Ошибка','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

// --- Calendar ---
window.loadCalendar=function(){
  api('/api/calendar').then(function(d){
    document.getElementById('cal-stats').innerHTML='<span style="color:var(--accent)">'+d.users+'</span> подписчиков, <span style="color:var(--accent)">'+d.shows+'</span> сериалов';
    var popular=d.popular||[];
    var el=document.getElementById('cal-popular');
    if(popular.length===0){el.innerHTML='<div style="color:var(--text-dim);font-size:13px">Нет отслеживаемых сериалов</div>';return;}
    var h='<table style="width:100%;border-collapse:collapse;font-size:13px">';
    h+='<thead><tr style="border-bottom:1px solid rgba(255,255,255,0.06)"><th style="text-align:left;padding:8px;color:var(--text-muted)">Сериал</th><th style="text-align:left;padding:8px;color:var(--text-muted)">IMDb</th><th style="text-align:center;padding:8px;color:var(--text-muted)">TMDB</th><th style="text-align:center;padding:8px;color:var(--text-muted)">Подписчики</th></tr></thead><tbody>';
    for(var i=0;i<popular.length;i++){
      var p=popular[i];
      h+='<tr style="border-bottom:1px solid var(--surface2)">';
      h+='<td style="padding:8px;color:var(--text)">'+esc(p.title)+'</td>';
      h+='<td style="padding:8px;color:var(--text-muted);font-family:monospace;font-size:11px">'+esc(p.imdb_id)+'</td>';
      h+='<td style="padding:8px;color:var(--text-muted);text-align:center">'+p.tmdb_id+'</td>';
      h+='<td style="padding:8px;color:var(--accent);text-align:center;font-weight:600">'+p.subscribers+'</td>';
      h+='</tr>';
    }
    h+='</tbody></table>';
    el.innerHTML=h;
  }).catch(function(e){
    document.getElementById('cal-stats').innerHTML='<span style="color:var(--danger)">Ошибка: '+esc(e.message)+'</span>';
  });
};
window.calCheckNow=function(){
  post('/api/calendar/check-now',{}).then(function(d){
    if(d.ok)toast('Проверка запущена');
    else toast(d.error||'Ошибка','error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error')});
};

// --- XSearch ---
window.loadXSearch=function(){
  api('/api/xsearch/sources').then(function(sources){
    var el=document.getElementById('xs-sources');
    if(!sources||!sources.length){el.innerHTML='<span style="color:var(--text-dim)">Нет активных источников</span>';return;}
    var h='';
    for(var i=0;i<sources.length;i++){
      var s=sources[i];
      var badge=s.quality?(' <small style="opacity:0.6">'+esc(s.quality)+'</small>'):'';
      h+='<span style="background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:4px 10px;font-size:12px;color:var(--text)">'+esc(s.balancer)+badge+'</span>';
    }
    el.innerHTML=h;
    document.getElementById('xs-stats').innerHTML='<span style="color:var(--accent)">'+sources.length+'</span> источников поиска активно';
  }).catch(function(e){
    document.getElementById('xs-stats').innerHTML='<span style="color:var(--danger)">Ошибка: '+esc(e.message)+'</span>';
  });
};
window.xsTestSearch=function(){
  var q=document.getElementById('xs-test-input').value.trim();
  if(!q){toast('Введите запрос','error');return;}
  var el=document.getElementById('xs-test-results');
  el.innerHTML='<div style="color:var(--text-muted);font-size:13px">Поиск...</div>';
  api('/api/xsearch?q='+encodeURIComponent(q)).then(function(d){
    var results=d.results||[];
    if(!results.length){el.innerHTML='<div style="color:var(--text-dim);font-size:13px">Ничего не найдено</div>';return;}
    var h='<table style="width:100%;border-collapse:collapse;font-size:13px">';
    h+='<thead><tr style="border-bottom:1px solid rgba(255,255,255,0.06)"><th style="text-align:left;padding:6px;color:var(--text-muted)">Название</th><th style="text-align:center;padding:6px;color:var(--text-muted)">Год</th><th style="text-align:center;padding:6px;color:var(--text-muted)">Тип</th><th style="text-align:left;padding:6px;color:var(--text-muted)">Источники</th></tr></thead><tbody>';
    for(var i=0;i<Math.min(results.length,20);i++){
      var r=results[i];
      var sources=(r.sources||[]).filter(function(s){return s.balancer!=='tmdb'}).map(function(s){return esc(s.balancer)+(s.quality?' '+esc(s.quality):'')}).join(', ');
      h+='<tr style="border-bottom:1px solid var(--surface2)">';
      h+='<td style="padding:6px;color:var(--text)">'+esc(r.title)+(r.original_title?' <small style="opacity:0.5">'+esc(r.original_title)+'</small>':'')+'</td>';
      h+='<td style="padding:6px;color:var(--text-muted);text-align:center">'+(r.year||'—')+'</td>';
      h+='<td style="padding:6px;color:var(--text-muted);text-align:center">'+(r.type==='serial'?'сериал':'фильм')+'</td>';
      h+='<td style="padding:6px;color:var(--accent);font-size:11px">'+(sources||'<span style="color:var(--text-dim)">только TMDB</span>')+'</td>';
      h+='</tr>';
    }
    h+='</tbody></table>';
    h+='<div style="margin-top:8px;color:var(--text-dim);font-size:11px">Найдено: '+d.total+', кеш: '+(d.cached?'да':'нет')+'</div>';
    el.innerHTML=h;
  }).catch(function(e){
    el.innerHTML='<span style="color:var(--danger)">Ошибка: '+esc(e.message)+'</span>';
  });
};

// --- Collections ---
window.loadCollections=function(){
  api('/api/collections/stats').then(function(d){
    document.getElementById('coll-stats').innerHTML='Кеш: <span style="color:var(--accent)">'+d.cache_entries+'</span> записей, TTL: '+d.cache_ttl_min+' мин';
    loadCollPinned();
  }).catch(function(e){
    document.getElementById('coll-stats').innerHTML='<span style="color:var(--danger)">Ошибка: '+esc(e.message)+'</span>';
  });
};
function loadCollPinned(){
  api('/api/collections/pinned').then(function(d){
    var el=document.getElementById('coll-pinned');
    var ids=(d.directors||[]).concat(d.actors||[]);
    if(!ids.length){el.innerHTML='<span style="color:var(--text-dim);font-size:12px">Нет закреплённых</span>';return;}
    var h='';
    for(var i=0;i<ids.length;i++){
      h+='<span style="background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:4px 10px;font-size:12px;color:var(--text);display:inline-flex;align-items:center;gap:6px">ID: '+ids[i]+' <button onclick="collUnpin('+ids[i]+')" style="background:none;border:none;color:var(--danger);cursor:pointer;font-size:14px">&times;</button></span>';
    }
    el.innerHTML=h;
  }).catch(function(){});
}
window.collUnpin=function(id){
  api('/api/collections/pin/'+id,{method:'DELETE'}).then(function(){
    toast('Откреплено');loadCollPinned();
  }).catch(function(e){toast('Ошибка: '+e.message,'error');});
};
window.collSearchPerson=function(){
  var q=document.getElementById('coll-pin-input').value.trim();
  if(!q){toast('Введите имя','error');return;}
  var el=document.getElementById('coll-pin-results');
  el.innerHTML='<div style="color:var(--text-muted);font-size:13px">Поиск...</div>';
  api('/api/collections/person/search?q='+encodeURIComponent(q)).then(function(d){
    var results=d.results||[];
    if(!results.length){el.innerHTML='<div style="color:var(--text-dim);font-size:13px">Ничего не найдено</div>';return;}
    var h='';
    for(var i=0;i<Math.min(results.length,10);i++){
      var p=results[i];
      var dept=p.known_for_department==='Directing'?'director':'actor';
      h+='<div style="display:inline-flex;align-items:center;gap:8px;background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:6px 12px;margin:4px;font-size:13px;color:var(--text)">';
      h+=esc(p.name)+' <small style="opacity:0.5">('+esc(p.known_for_department||'?')+')</small> ';
      h+='<button onclick="collPin('+p.id+',\''+dept+'\')" class="btn btn-primary" style="padding:2px 8px;font-size:11px">Закрепить</button>';
      h+='</div>';
    }
    el.innerHTML=h;
  }).catch(function(e){el.innerHTML='<span style="color:var(--danger)">Ошибка: '+esc(e.message)+'</span>';});
};
window.collPin=function(id,type){
  api('/api/collections/pin',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:id,type:type})}).then(function(){
    toast('Закреплено');loadCollPinned();document.getElementById('coll-pin-results').innerHTML='';
  }).catch(function(e){toast('Ошибка: '+e.message,'error');});
};
window.collTestPerson=function(){
  var q=document.getElementById('coll-test-input').value.trim();
  if(!q){toast('Введите имя','error');return;}
  var el=document.getElementById('coll-test-results');
  el.innerHTML='<div style="color:var(--text-muted);font-size:13px">Поиск...</div>';
  api('/api/collections/person/search?q='+encodeURIComponent(q)).then(function(d){
    var results=d.results||[];
    if(!results.length){el.innerHTML='<div style="color:var(--text-dim);font-size:13px">Ничего не найдено</div>';return;}
    var pid=results[0].id;
    el.innerHTML='<div style="color:var(--text-muted);font-size:13px">'+esc(results[0].name)+' (ID: '+pid+') — загрузка фильмов...</div>';
    api('/api/collections/person/'+pid+'/movies').then(function(m){
      var cast=m.cast||[];var directed=m.directed||[];
      var h='<div style="font-size:13px;color:var(--text);margin-bottom:8px"><b>'+esc(results[0].name)+'</b>: '+cast.length+' (актёр) + '+directed.length+' (режиссёр)</div>';
      var all=directed.concat(cast).slice(0,15);
      if(all.length){
        h+='<table style="width:100%;border-collapse:collapse;font-size:12px"><thead><tr style="border-bottom:1px solid rgba(255,255,255,0.06)"><th style="text-align:left;padding:4px;color:var(--text-muted)">Фильм</th><th style="text-align:center;padding:4px;color:var(--text-muted)">Рейтинг</th><th style="text-align:left;padding:4px;color:var(--text-muted)">Роль</th></tr></thead><tbody>';
        for(var i=0;i<all.length;i++){
          var c=all[i];
          h+='<tr style="border-bottom:1px solid var(--surface2)"><td style="padding:4px;color:var(--text)">'+esc(c.title||c.name||'')+'</td><td style="padding:4px;color:var(--text-muted);text-align:center">'+(c.vote_average||'—')+'</td><td style="padding:4px;color:var(--accent);font-size:11px">'+esc(c.role||'')+'</td></tr>';
        }
        h+='</tbody></table>';
      }
      el.innerHTML=h;
    }).catch(function(e){el.innerHTML='<span style="color:var(--danger)">Ошибка: '+esc(e.message)+'</span>';});
  }).catch(function(e){el.innerHTML='<span style="color:var(--danger)">Ошибка: '+esc(e.message)+'</span>';});
};

// --- IPTV ---
window.onIPTVToggle=function(enabled){
  document.getElementById('iptv-settings').style.display=enabled?'':'none';
};
window.loadIPTV=function(){
  api('/api/plugins').then(function(d){
    var c=d.iptv||{};
    document.getElementById('iptv-enable').checked=!!c.enable;
    document.getElementById('iptv-settings').style.display=c.enable?'':'none';
    document.getElementById('iptv-epg-hours').value=c.epg_update_hours||6;
    document.getElementById('iptv-max-playlists').value=c.max_playlists||10;
    document.getElementById('iptv-default-proxy').value=c.default_proxy||'none';
    var epgUrls=(c.epg_urls||[]);
    document.getElementById('iptv-epg-urls').value=epgUrls.join('\n');
    var globalPl=(c.global_playlists||[]);
    document.getElementById('iptv-global-playlists').value=globalPl.join('\n');
    // Stats
    var stats='';
    if(epgUrls.length)stats+='EPG источников: <span style="color:var(--accent)">'+epgUrls.length+'</span>';
    if(globalPl.length)stats+=(stats?' · ':'')+'Глобальных плейлистов: <span style="color:var(--accent)">'+globalPl.length+'</span>';
    document.getElementById('iptv-stats').innerHTML=stats;
  }).catch(function(e){
    toast('Ошибка загрузки IPTV: '+e.message,'error');
  });
};
window.saveIPTVSettings=function(){
  var enable=document.getElementById('iptv-enable').checked;
  var epgHours=parseInt(document.getElementById('iptv-epg-hours').value)||6;
  var maxPl=parseInt(document.getElementById('iptv-max-playlists').value)||10;
  var proxy=document.getElementById('iptv-default-proxy').value;
  var epgUrls=document.getElementById('iptv-epg-urls').value.split('\n').map(function(s){return s.trim()}).filter(Boolean);
  var globalPl=document.getElementById('iptv-global-playlists').value.split('\n').map(function(s){return s.trim()}).filter(Boolean);
  post('/api/plugins',{iptv:{enable:enable,epg_update_hours:epgHours,max_playlists:maxPl,default_proxy:proxy,epg_urls:epgUrls,global_playlists:globalPl}}).then(function(d){
    if(d.ok){toast('IPTV настройки сохранены');if(d.restart_needed)showRestart();}
    else toast('Ошибка: '+(d.error||'unknown'),'error');
  }).catch(function(e){toast('Ошибка: '+e.message,'error');});
};

// --- Logs ---
var logsActiveCats={};
var logsSSE=null;
var logsSearchTimer=null;
var logsCatsInited=false;

var logLevelColors={error:'var(--danger)',warn:'var(--warning)',info:'var(--accent)',debug:'var(--text-muted)',trace:'#555'};
var logCatColors={
  admin:'#f0c040',auth:'#ff6b6b',transcoding:'#9b59b6',nws:'#3498db',
  dlna:'#1abc9c',kit:'#e91e63',system:'#94a3b8',browser:'#f39c12',
  sidecar:'#95a5a6',tmdb:'#01d277',alice:'#7b68ee',http:'#3498db',
  youtube:'#ff0000',rezka:'#e74c3c',rhsprem:'#c0392b',collaps:'#d35400',
  kinotochka:'#e67e22',rutubemovie:'#27ae60',vkmovie:'#4a76a8',plvideo:'#8e44ad',
  cdnvideohub:'#2c3e50',redheadsound:'#c0392b',iremux:'#16a085',remux:'#16a085',
  zetflix:'#2ecc71',videodb:'#3498db',cdnmovies:'#2980b9',vdbmovies:'#1abc9c',
  fancdn:'#e67e22',kinobase:'#c0392b',videocdn:'#9b59b6',lumex:'#f1c40f',
  vokino:'#e74c3c',iframevideo:'#34495e',hdvb:'#2980b9',vibix:'#8e44ad',
  videoseed:'#27ae60',kinopub:'#e67e22',alloha:'#1abc9c',getstv:'#2ecc71',
  hydraflix:'#3498db',vidsrc:'#95a5a6',vidlink:'#7f8c8d',videasy:'#9b59b6',
  autoembed:'#34495e',rgshows:'#d35400',kodik:'#8e44ad',mirage:'#1abc9c',
  aladdin:'#f39c12',kinogo:'#e74c3c',iptvonline:'#2980b9',filmix:'#e67e22',
  moonanime:'#9b59b6',anilibria:'#e74c3c',aniliberty:'#c0392b',animebesst:'#8e44ad',
  animedia:'#d35400',animevost:'#27ae60',animego:'#3498db',animelib:'#e91e63',
  kinoukr:'#f1c40f',ashdi:'#1abc9c',eneyida:'#2ecc71',bamboo:'#27ae60',
  unimay:'#9b59b6',starlight:'#f39c12',klonfun:'#e74c3c',uaflix:'#3498db',
  animeon:'#e91e63',mikai:'#8e44ad',veoveo:'#d35400',filmixpartner:'#e67e22',
  pidtor:'#e67e22'
};
var logBalancerCats={
  youtube:1,rezka:1,rhsprem:1,collaps:1,kinotochka:1,rutubemovie:1,vkmovie:1,
  plvideo:1,cdnvideohub:1,redheadsound:1,iremux:1,remux:1,zetflix:1,videodb:1,
  cdnmovies:1,vdbmovies:1,fancdn:1,kinobase:1,videocdn:1,lumex:1,vokino:1,
  iframevideo:1,hdvb:1,vibix:1,videoseed:1,kinopub:1,alloha:1,getstv:1,
  hydraflix:1,vidsrc:1,vidlink:1,videasy:1,autoembed:1,rgshows:1,kodik:1,
  mirage:1,aladdin:1,kinogo:1,iptvonline:1,filmix:1,moonanime:1,anilibria:1,
  aniliberty:1,animebesst:1,animedia:1,animevost:1,animego:1,animelib:1,
  kinoukr:1,ashdi:1,eneyida:1,bamboo:1,unimay:1,starlight:1,klonfun:1,
  uaflix:1,animeon:1,mikai:1,veoveo:1,filmixpartner:1,pidtor:1,
  tmdb:1,alice:1,kit:1
};
var logsBalDropdownOpen=false;

function logsSearchDebounce(){
  clearTimeout(logsSearchTimer);
  logsSearchTimer=setTimeout(loadLogs,400);
}

function logsCatParams(){
  var sel=[];
  for(var c in logsActiveCats){if(logsActiveCats[c])sel.push(c)}
  return sel.length?sel.join(','):'';
}

window.logsClearFilters=function(){
  for(var c in logsActiveCats)logsActiveCats[c]=false;
  document.querySelectorAll('#logs-cats [data-cat]').forEach(function(el){
    el.classList.remove('lcat-on');
    el.style.background='var(--surface2)';el.style.color='var(--text-muted)';el.style.borderColor='var(--border)';
  });
  document.querySelectorAll('#logs-bal-list [data-cat]').forEach(function(el){
    var cb=el.querySelector('input');if(cb)cb.checked=false;
    el.style.background='transparent';
  });
  document.getElementById('logs-level').value='';
  document.getElementById('logs-search').value='';
  logsUpdateBalCount();
  loadLogs();
};

window.logsToggleBalDropdown=function(){
  logsBalDropdownOpen=!logsBalDropdownOpen;
  document.getElementById('logs-bal-dropdown').style.display=logsBalDropdownOpen?'block':'none';
};

// Close dropdown on outside click
document.addEventListener('click',function(ev){
  if(logsBalDropdownOpen&&!document.getElementById('logs-bal-wrap').contains(ev.target)){
    logsBalDropdownOpen=false;
    document.getElementById('logs-bal-dropdown').style.display='none';
  }
});

function logsUpdateBalCount(){
  var n=0;
  for(var c in logBalancerCats){if(logsActiveCats[c])n++}
  var badge=document.getElementById('logs-bal-count');
  if(n>0){badge.textContent=n;badge.style.display='';
    document.getElementById('logs-bal-btn').style.borderColor='rgba(6,182,212,0.27)';
    document.getElementById('logs-bal-btn').style.color='var(--accent)';
  }else{badge.style.display='none';
    document.getElementById('logs-bal-btn').style.borderColor='var(--border)';
    document.getElementById('logs-bal-btn').style.color='var(--text-muted)';
  }
}

function initLogsCats(cats){
  if(logsCatsInited)return;
  logsCatsInited=true;
  var el=document.getElementById('logs-cats');
  var balList=document.getElementById('logs-bal-list');
  if(!cats||!cats.length)return;
  el.innerHTML='';balList.innerHTML='';
  var coreCats=[],balCats=[];
  cats.forEach(function(c){
    logsActiveCats[c]=false;
    if(logBalancerCats[c])balCats.push(c);
    else coreCats.push(c);
  });
  // Core category chips
  coreCats.forEach(function(c){
    var cc=logCatColors[c]||'#94a3b8';
    var chip=document.createElement('span');
    chip.dataset.cat=c;
    chip.style.cssText='display:inline-flex;align-items:center;gap:5px;padding:4px 12px;border-radius:16px;cursor:pointer;font-size:11px;font-weight:500;transition:all .2s;user-select:none;border:1px solid rgba(255,255,255,0.06);background:var(--surface2);color:var(--text-muted)';
    chip.innerHTML='<span style="width:6px;height:6px;border-radius:50%;background:'+cc+';flex-shrink:0"></span>'+esc(c);
    chip.onclick=function(){
      logsActiveCats[c]=!logsActiveCats[c];
      if(logsActiveCats[c]){
        chip.classList.add('lcat-on');
        chip.style.background=cc+'22';chip.style.color=cc;chip.style.borderColor=cc+'66';
      }else{
        chip.classList.remove('lcat-on');
        chip.style.background='var(--surface2)';chip.style.color='var(--text-muted)';chip.style.borderColor='var(--border)';
      }
      loadLogs();
    };
    el.appendChild(chip);
  });
  // Balancer dropdown items
  balCats.sort();
  balCats.forEach(function(c){
    var cc=logCatColors[c]||'#94a3b8';
    var row=document.createElement('label');
    row.dataset.cat=c;
    row.style.cssText='display:flex;align-items:center;gap:8px;padding:6px 10px;border-radius:6px;cursor:pointer;font-size:12px;color:var(--text);transition:background .15s;user-select:none';
    row.onmouseenter=function(){if(!logsActiveCats[c])row.style.background='#1e2736'};
    row.onmouseleave=function(){if(!logsActiveCats[c])row.style.background='transparent'};
    var cb=document.createElement('input');
    cb.type='checkbox';cb.style.cssText='accent-color:'+cc+';width:14px;height:14px;cursor:pointer';
    cb.onchange=function(){
      logsActiveCats[c]=cb.checked;
      row.style.background=cb.checked?cc+'15':'transparent';
      logsUpdateBalCount();
      loadLogs();
    };
    var dot=document.createElement('span');
    dot.style.cssText='width:8px;height:8px;border-radius:50%;background:'+cc+';flex-shrink:0';
    var txt=document.createElement('span');
    txt.textContent=c;txt.style.flex='1';
    row.appendChild(cb);row.appendChild(dot);row.appendChild(txt);
    balList.appendChild(row);
  });
  logsUpdateBalCount();
}

function renderLogEntry(e){
  var lc=logLevelColors[e.level]||'#94a3b8';
  var cc=logCatColors[e.category]||'#94a3b8';
  var t=e.time||'';
  if(t.length>19)t=t.substring(11,19);
  else if(t.length>10)t=t.substring(11);
  var lvlBg=lc+'18';
  var extra='';
  if(e.extra){
    var keys=Object.keys(e.extra);
    if(keys.length>0){
      var parts=[];
      for(var i=0;i<keys.length;i++){
        var k=keys[i],v=e.extra[k];
        if(v.length>200)v=v.substring(0,200)+'…';
        parts.push('<span style="color:var(--accent)">'+esc(k)+'</span>=<span style="color:#a9b1d6">'+esc(v)+'</span>');
      }
      extra='<div style="margin-top:2px;font-size:11px;color:var(--text-dim);padding-left:205px;word-break:break-all">'+parts.join(' &middot; ')+'</div>';
    }
  }
  return '<div style="padding:4px 12px;border-bottom:1px solid var(--bg);transition:background .1s" onmouseenter="this.style.background=\'#161b22\'" onmouseleave="this.style.background=\'transparent\'">'
    +'<div style="display:flex;gap:8px;align-items:baseline">'
    +'<span style="color:var(--text-dim);min-width:58px;font-size:11px">'+esc(t)+'</span>'
    +'<span style="color:'+lc+';min-width:40px;font-weight:600;text-transform:uppercase;font-size:10px;padding:1px 6px;border-radius:4px;background:'+lvlBg+';text-align:center">'+esc(e.level)+'</span>'
    +'<span style="color:'+cc+';min-width:85px;font-size:11px;font-weight:500">'+esc(e.category)+'</span>'
    +'<span style="color:var(--text);flex:1;word-break:break-all">'+esc(e.message)+'</span>'
    +'</div>'
    +extra
    +'</div>';
}

window.loadLogs=function(){
  var params='?limit=500';
  var cats=logsCatParams();
  if(cats)params+='&category='+encodeURIComponent(cats);
  var lvl=document.getElementById('logs-level').value;
  if(lvl)params+='&level='+encodeURIComponent(lvl);
  var search=document.getElementById('logs-search').value.trim();
  if(search)params+='&search='+encodeURIComponent(search);

  api('/api/logs'+params).then(function(d){
    initLogsCats(d.categories);
    var el=document.getElementById('logs-entries');
    var entries=d.entries||[];
    if(!entries.length){
      el.innerHTML='<div style="padding:30px;color:var(--text-dim);text-align:center;font-family:sans-serif;font-size:13px">Нет записей</div>';
    }else{
      el.innerHTML=entries.map(renderLogEntry).join('');
    }
    document.getElementById('logs-status').textContent='Буфер: '+(d.total||0)+' записей';
  }).catch(function(e){
    document.getElementById('logs-entries').innerHTML='<div style="padding:20px;color:var(--danger);font-family:sans-serif">Ошибка: '+esc(e.message)+'</div>';
  });
};

window.toggleLogsStream=function(){
  var on=document.getElementById('logs-realtime').checked;
  var dot=document.getElementById('logs-rt-dot');
  var lbl=document.getElementById('logs-rt-label');
  var rtStatus=document.getElementById('logs-rt-status');
  if(on){
    if(logsSSE)logsSSE.close();
    var params='?';
    var cats=logsCatParams();
    if(cats)params+='category='+encodeURIComponent(cats)+'&';
    var lvl=document.getElementById('logs-level').value;
    if(lvl)params+='level='+encodeURIComponent(lvl)+'&';
    var search=document.getElementById('logs-search').value.trim();
    if(search)params+='search='+encodeURIComponent(search)+'&';

    var tkn=(document.cookie.match(/lampac_token=([^;]+)/)||[])[1]||(document.cookie.match(/admin_session=([^;]+)/)||[])[1]||'';
    if(tkn)params+='token='+encodeURIComponent(tkn)+'&';
    logsSSE=new EventSource(basePath+'/api/logs/stream'+params);
    logsSSE.onmessage=function(ev){
      try{
        var e=JSON.parse(ev.data);
        var el=document.getElementById('logs-entries');
        el.insertAdjacentHTML('afterbegin',renderLogEntry(e));
        while(el.children.length>500)el.removeChild(el.lastChild);
      }catch(ex){}
    };
    var sseRetries=0;
    logsSSE.onerror=function(){
      sseRetries++;
      if(sseRetries>3){
        logsSSE.close();logsSSE=null;
        rtStatus.textContent='Не удалось подключиться';rtStatus.style.color='var(--danger)';
        dot.style.background='var(--danger)';dot.style.boxShadow='none';
        document.getElementById('logs-realtime').checked=false;
      } else {
        rtStatus.textContent='Переподключение ('+sseRetries+'/3)...';rtStatus.style.color='var(--warning)';
      }
    };
    logsSSE.onopen=function(){
      sseRetries=0;
      rtStatus.textContent='Подключено';rtStatus.style.color='var(--accent)';rtStatus.style.display='';
    };
    dot.style.background='var(--accent)';dot.style.boxShadow='var(--accent-glow)';
    lbl.style.borderColor='var(--accent-a25)';lbl.style.color='var(--accent)';
    rtStatus.style.display='';rtStatus.textContent='Подключение...';rtStatus.style.color='var(--warning)';
  }else{
    if(logsSSE){logsSSE.close();logsSSE=null}
    dot.style.background='#555';dot.style.boxShadow='none';
    lbl.style.borderColor='var(--border)';lbl.style.color='var(--text-muted)';
    rtStatus.style.display='none';
    loadLogs();
  }
};

window.exportLogs=function(fmt){
  var params='?format='+fmt;
  var cats=logsCatParams();
  if(cats)params+='&category='+encodeURIComponent(cats);
  var lvl=document.getElementById('logs-level').value;
  if(lvl)params+='&level='+encodeURIComponent(lvl);
  var search=document.getElementById('logs-search').value.trim();
  if(search)params+='&search='+encodeURIComponent(search);
  window.open(basePath+'/api/logs/export'+params,'_blank');
};
})();

// === block 5 (orig admin_tg_panel.go lines 9858-9979) ===
(function(){
var THEME_KEY='alpac_admin_theme';
var THEME_COLORS=[
  {key:'accent',label:'Акцент',def:'#06b6d4'},
  {key:'accent-dark',label:'Акцент 2',def:'#0d9488'},
  {key:'bg',label:'Фон',def:'#161b23'},
  {key:'surface',label:'Панели',def:'#1a2332'},
  {key:'surface2',label:'Поля',def:'#1e2736'},
  {key:'text',label:'Текст',def:'#e2e8f0'},
  {key:'text-muted',label:'Текст 2',def:'#94a3b8'},
  {key:'danger',label:'Ошибки',def:'#ff6b6b'},
  {key:'warning',label:'Предупр.',def:'#f0c040'}
];
var PRESETS=[
  {name:'Cyan',colors:{}},
  {name:'Purple',colors:{accent:'#a78bfa','accent-dark':'#7c3aed'}},
  {name:'Green',colors:{accent:'#34d399','accent-dark':'#059669'}},
  {name:'Orange',colors:{accent:'#fb923c','accent-dark':'#ea580c'}},
  {name:'Rose',colors:{accent:'#fb7185','accent-dark':'#e11d48'}},
  {name:'Blue',colors:{accent:'#60a5fa','accent-dark':'#2563eb'}},
  {name:'Amber',colors:{accent:'#fbbf24','accent-dark':'#d97706',warning:'#60a5fa'}},
  {name:'Light',colors:{bg:'#f0f2f5',surface:'#ffffff',surface2:'#e8ecf1',text:'#1a1a2e','text-muted':'#6b7280','text-dim':'#9ca3af',accent:'#0891b2','accent-dark':'#0e7490'}}
];
function hexToRgb(h){h=h.replace('#','');var r=parseInt(h.substring(0,2),16),g=parseInt(h.substring(2,4),16),b=parseInt(h.substring(4,6),16);return{r:r,g:g,b:b}}
function rgbaFromHex(h,a){var c=hexToRgb(h);return'rgba('+c.r+','+c.g+','+c.b+','+a+')'}
function computeDerived(t){
  var ac=t.accent||'#06b6d4';
  var ad=t['accent-dark']||'#0d9488';
  var r=document.documentElement.style;
  r.setProperty('--accent',ac);
  r.setProperty('--accent-dark',ad);
  r.setProperty('--accent-hover',t['accent-hover']||ad);
  r.setProperty('--accent-hover-dark',t['accent-hover-dark']||ad);
  r.setProperty('--bg',t.bg||'#161b23');
  r.setProperty('--surface',t.surface||'#1a2332');
  r.setProperty('--surface2',t.surface2||'#1e2736');
  r.setProperty('--surface3',t.surface3||'#11161d');
  r.setProperty('--text',t.text||'#e2e8f0');
  r.setProperty('--text-muted',t['text-muted']||'#94a3b8');
  r.setProperty('--text-dim',t['text-dim']||'#64748b');
  r.setProperty('--danger',t.danger||'#ff6b6b');
  r.setProperty('--warning',t.warning||'#f0c040');
  r.setProperty('--accent-a04',rgbaFromHex(ac,0.04));
  r.setProperty('--accent-a05',rgbaFromHex(ac,0.05));
  r.setProperty('--accent-a06',rgbaFromHex(ac,0.06));
  r.setProperty('--accent-a08',rgbaFromHex(ac,0.08));
  r.setProperty('--accent-a10',rgbaFromHex(ac,0.1));
  r.setProperty('--accent-a12',rgbaFromHex(ac,0.12));
  r.setProperty('--accent-a13',rgbaFromHex(ac,0.13));
  r.setProperty('--accent-a15',rgbaFromHex(ac,0.15));
  r.setProperty('--accent-a20',rgbaFromHex(ad,0.2));
  r.setProperty('--accent-a25',rgbaFromHex(ac,0.25));
  r.setProperty('--accent-a30',rgbaFromHex(ac,0.3));
  r.setProperty('--accent-a35',rgbaFromHex(ad,0.35));
  r.setProperty('--accent-glow','0 0 6px '+ac);
  r.setProperty('--danger-bg',rgbaFromHex(t.danger||'#ff6b6b',0.13));
  r.setProperty('--danger-border',rgbaFromHex(t.danger||'#ff6b6b',0.27));
  r.setProperty('--warning-bg',rgbaFromHex(t.warning||'#f0c040',0.13));
  r.setProperty('--warning-border',rgbaFromHex(t.warning||'#f0c040',0.27));
  r.setProperty('--border','rgba(255,255,255,0.06)');
}
function loadTheme(){try{var s=localStorage.getItem(THEME_KEY);if(s){var t=JSON.parse(s);computeDerived(t);return t}}catch(e){}return null}
function saveTheme(t){try{localStorage.setItem(THEME_KEY,JSON.stringify(t))}catch(e){}}
function getDefaults(){var d={};THEME_COLORS.forEach(function(c){d[c.key]=c.def});return d}
function getCurrentTheme(){var t=loadTheme();if(!t)t={};var d=getDefaults();var r={};THEME_COLORS.forEach(function(c){r[c.key]=t[c.key]||d[c.key]});return r}
// Apply on load
loadTheme();
// Modal
function renderPresets(){
  var box=document.getElementById('theme-presets');
  var cur=getCurrentTheme();
  box.innerHTML='';
  PRESETS.forEach(function(p,i){
    var btn=document.createElement('div');
    btn.className='theme-preset';
    btn.textContent=p.name;
    var isActive=true;
    var pc=Object.assign({},getDefaults(),p.colors);
    THEME_COLORS.forEach(function(c){if(c.key==='accent'||c.key==='accent-dark'||c.key==='bg'||c.key==='surface'){if(pc[c.key]!==cur[c.key])isActive=false}});
    if(isActive)btn.classList.add('active');
    btn.onclick=function(){
      var newT=Object.assign({},getDefaults(),p.colors);
      computeDerived(newT);saveTheme(newT);renderColors();renderPresets();
    };
    var swatch=document.createElement('span');
    swatch.style.cssText='display:inline-block;width:10px;height:10px;border-radius:50%;background:'+(p.colors.accent||'#06b6d4')+';margin-right:5px;vertical-align:middle';
    btn.prepend(swatch);
    box.appendChild(btn);
  });
}
function renderColors(){
  var box=document.getElementById('theme-colors');
  box.innerHTML='';
  var cur=getCurrentTheme();
  THEME_COLORS.forEach(function(c){
    var row=document.createElement('div');
    row.className='theme-color-row';
    var lbl=document.createElement('label');lbl.textContent=c.label;
    var inp=document.createElement('input');inp.type='color';inp.value=cur[c.key];
    var hex=document.createElement('span');hex.className='color-hex';hex.textContent=cur[c.key];
    inp.oninput=function(){
      hex.textContent=inp.value;
      var t=getCurrentTheme();t[c.key]=inp.value;
      computeDerived(t);saveTheme(t);renderPresets();
    };
    row.appendChild(lbl);row.appendChild(inp);row.appendChild(hex);
    box.appendChild(row);
  });
}
window.openThemeModal=function(){
  renderPresets();renderColors();
  document.getElementById('theme-overlay').classList.add('open');
};
window.closeThemeModal=function(){
  document.getElementById('theme-overlay').classList.remove('open');
};
window.resetTheme=function(){
  try{localStorage.removeItem(THEME_KEY)}catch(e){}
  computeDerived(getDefaults());renderColors();renderPresets();
  toast('Тема сброшена','success');
};
})();

// === block 6 (orig admin_tg_panel.go lines 9982-10072) ===
// === Torrs (Torrent Management) ===
(function(){
var _torrsTimer=null;
window.initTorrsTab=function(){
  clearInterval(_torrsTimer);
  torrsRefresh();
  _torrsTimer=setInterval(torrsRefresh,3000);
};
window.destroyTorrsTab=function(){clearInterval(_torrsTimer);_torrsTimer=null};

// --- jacred web UI (embedded iframe) ---
// The jacred instance may still be installing / bootstrapping its DB; poll the
// status until it's healthy, then load the /jacred/ proxy into the iframe.
var _jacredTimer=null;
window.initJacredTab=function(){
  clearInterval(_jacredTimer);
  jacredRefresh();
  _jacredTimer=setInterval(jacredRefresh,5000);
};
window.destroyJacredTab=function(){clearInterval(_jacredTimer);_jacredTimer=null};
function jacredRefresh(){
  var box=document.getElementById('jacred-status');
  var frame=document.getElementById('jacred-frame');
  if(!box||!frame)return;
  api('/api/jacred/status').then(function(d){
    d=d||{};
    var txt,healthy=!!d.healthy;
    if(d.error)txt='&#x26A0;&#xFE0F; '+d.error;
    else if(!d.installed)txt='&#x2716;&#xFE0F; не установлен — поставьте JacRed на вкладке «Зависимости»';
    else if(healthy)txt='&#x2705; работает'+(d.version?' &middot; '+d.version:'')+(d.db_size_mb?', база '+d.db_size_mb+' МБ':'');
    else txt='&#x23F3; '+(d.stage||'запускается')+'&hellip;';
    box.innerHTML=txt;
    if(healthy){
      // Load once, then stop polling — the UI lives inside the frame now.
      if(!frame.getAttribute('src'))frame.setAttribute('src','/jacred/');
      frame.style.display='';
      clearInterval(_jacredTimer);_jacredTimer=null;
    }else{
      frame.style.display='none';
    }
  }).catch(function(){box.innerHTML='&#x26A0;&#xFE0F; нет связи с сервером';});
}

function fmt(b){if(!b||b<=0)return '0 B';var u=['B','KB','MB','GB','TB'];var i=0;var v=b;while(v>=1024&&i<u.length-1){v/=1024;i++}return v.toFixed(i>0?1:0)+' '+u[i]}
function fmtSpeed(b){return fmt(b)+'/s'}

window.torrsRefresh=function(){
  api('/api/torrs/list').then(function(d){
    // Status bar
    var st=document.getElementById('torrs-status');
    if(!st)return;
    if(!d||!d.mode){st.innerHTML='<span style="color:var(--text-dim)">Недоступен</span>';document.getElementById('torrs-list').innerHTML='';return}
    var mode=d.mode==='inprocess'?'\u0412\u0441\u0442\u0440\u043E\u0435\u043D\u043D\u044B\u0439':d.mode==='proxy'?'\u041F\u0440\u043E\u043A\u0441\u0438':'—';
    var sh='<span style="color:var(--accent);font-weight:600">'+mode+'</span>';
    if(d.settings){
      sh+=' &nbsp;|&nbsp; \u041A\u044D\u0448: <b>'+fmt(d.settings.CacheSize)+'</b>';
      sh+=' &nbsp;|&nbsp; \u041F\u0440\u0435\u043B\u043E\u0430\u0434: <b>'+fmt(d.settings.PreloadSize)+'</b>';
    }
    var torrents=d.torrents||[];
    sh+=' &nbsp;|&nbsp; \u0422\u043E\u0440\u0440\u0435\u043D\u0442\u043E\u0432: <b>'+torrents.length+'</b>';
    st.innerHTML=sh;

    // List
    var el=document.getElementById('torrs-list');
    if(torrents.length===0){
      el.innerHTML='<div style="text-align:center;padding:32px;color:var(--text-dim)"><div style="font-size:36px;margin-bottom:8px">\uD83E\uDDF2</div>\u041D\u0435\u0442 \u0430\u043A\u0442\u0438\u0432\u043D\u044B\u0445 \u0442\u043E\u0440\u0440\u0435\u043D\u0442\u043E\u0432</div>';
      return;
    }
    var h='<table style="width:100%;border-collapse:collapse;font-size:13px"><thead><tr style="border-bottom:1px solid rgba(255,255,255,0.06);color:var(--text-muted);font-size:11px;text-transform:uppercase"><th style="text-align:left;padding:6px 8px">\u041D\u0430\u0437\u0432\u0430\u043D\u0438\u0435</th><th style="text-align:center;padding:6px">\u0424\u0430\u0439\u043B\u044B</th><th style="text-align:center;padding:6px">\u041F\u0438\u0440\u044B</th><th style="text-align:center;padding:6px">\u0421\u043A\u043E\u0440\u043E\u0441\u0442\u044C</th><th style="text-align:center;padding:6px">\u041F\u0440\u0435\u043B\u043E\u0430\u0434</th><th style="padding:6px"></th></tr></thead><tbody>';
    for(var i=0;i<torrents.length;i++){
      var t=torrents[i];
      var c=t.cache&&t.cache.Torrent?t.cache.Torrent:{};
      var files=t.file_stats||[];
      var title=esc(t.title||t.hash);
      var peers=(c.active_peers||0)+'/'+(c.connected_seeders||0);
      var speed=c.download_speed?fmtSpeed(c.download_speed):'—';
      var preloaded=c.preload_size>0?Math.round((c.preloaded_bytes||0)/(c.preload_size)*100)+'%':'—';
      h+='<tr style="border-bottom:1px solid rgba(255,255,255,0.04)">';
      h+='<td style="padding:8px;max-width:300px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">';
      if(t.poster)h+='<img src="'+esc(t.poster)+'" style="width:24px;height:34px;border-radius:3px;vertical-align:middle;margin-right:6px;object-fit:cover" onerror="this.style.display=\'none\'">';
      h+=title+'</td>';
      h+='<td style="text-align:center;padding:8px;color:var(--text-dim)">'+files.length+'</td>';
      h+='<td style="text-align:center;padding:8px">'+peers+'</td>';
      h+='<td style="text-align:center;padding:8px;color:var(--accent)">'+speed+'</td>';
      h+='<td style="text-align:center;padding:8px">'+preloaded+'</td>';
      h+='<td style="text-align:right;padding:8px;white-space:nowrap">';
      h+='<button class="btn btn-sm" style="padding:3px 8px;font-size:11px;background:rgba(255,152,0,0.1);color:#ff9800;border:1px solid rgba(255,152,0,0.2)" onclick="torrsAction(\'drop\',\''+t.hash+'\')">\u0421\u0442\u043E\u043F</button> ';
      h+='<button class="btn btn-sm" style="padding:3px 8px;font-size:11px;background:rgba(255,68,68,0.1);color:#ff4444;border:1px solid rgba(255,68,68,0.2)" onclick="torrsAction(\'remove\',\''+t.hash+'\')">\u0423\u0434\u0430\u043B\u0438\u0442\u044C</button>';
      h+='</td></tr>';
    }
    h+='</tbody></table>';
    el.innerHTML=h;
  }).catch(function(e){
    var st=document.getElementById('torrs-status');
    if(st)st.innerHTML='<span style="color:#ff4444">\u041E\u0448\u0438\u0431\u043A\u0430: '+esc(String(e))+'</span>';
  });
};

window.torrsAdd=function(){
  var inp=document.getElementById('torrs-add-link');
  if(!inp)return;
  var link=inp.value.trim();
  if(!link){toast('\u0412\u0432\u0435\u0434\u0438\u0442\u0435 magnet-\u0441\u0441\u044B\u043B\u043A\u0443','error');return}
  var btn=document.getElementById('torrs-add-btn');
  if(btn){btn.disabled=true;btn.textContent='\u0414\u043E\u0431\u0430\u0432\u043B\u0435\u043D\u0438\u0435...';}
  // Use direct TorrServer API
  fetch('/ts/torrents',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'add',link:link,save_to_db:true})})
  .then(function(r){return r.json()})
  .then(function(d){
    if(d.error){toast(d.error,'error')}else{toast('\u0422\u043E\u0440\u0440\u0435\u043D\u0442 \u0434\u043E\u0431\u0430\u0432\u043B\u0435\u043D');inp.value=''}
    if(btn){btn.disabled=false;btn.textContent='+ \u0414\u043E\u0431\u0430\u0432\u0438\u0442\u044C';}
    torrsRefresh();
  }).catch(function(e){toast('\u041E\u0448\u0438\u0431\u043A\u0430: '+e,'error');if(btn){btn.disabled=false;btn.textContent='+ \u0414\u043E\u0431\u0430\u0432\u0438\u0442\u044C';}});
};

window.torrsAction=function(action,hash){
  if(action==='remove'&&!confirm('\u0423\u0434\u0430\u043B\u0438\u0442\u044C \u0442\u043E\u0440\u0440\u0435\u043D\u0442 \u0438 \u0434\u0430\u043D\u043D\u044B\u0435?'))return;
  post('/api/torrs/action',{action:action,hash:hash}).then(function(d){
    toast(action==='remove'?'\u0423\u0434\u0430\u043B\u0451\u043D':'\u041E\u0441\u0442\u0430\u043D\u043E\u0432\u043B\u0435\u043D');
    torrsRefresh();
  }).catch(function(e){toast('\u041E\u0448\u0438\u0431\u043A\u0430: '+e,'error')});
};

})();

// === block 7 (orig admin_tg_panel.go lines 10075-10218) ===
// === User Groups ===
(function(){
var _grpEsc=window.esc||function(s){if(s==null)return'';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;')};
function _grpSetH(id,html){var el=typeof id==='string'?document.getElementById(id):id;if(el&&el.innerHTML!==html)el.innerHTML=html}
function _grpPost(p,d){return api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(d)})}

var _grpData={groups:[],users_count:{}};

window.loadGroups=function(){
  var el=document.getElementById('grp-list');
  if(el)_grpSetH(el,'<div style="color:var(--text-dim);padding:20px 0;text-align:center">\u0417\u0430\u0433\u0440\u0443\u0437\u043a\u0430...</div>');
  api('/api/groups',{}).then(function(d){
    _grpData=d;
    renderGroups();
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

function renderGroups(){
  var el=document.getElementById('grp-list');
  if(!el)return;
  var groups=_grpData.groups||[];
  var counts=_grpData.users_count||{};
  if(!groups.length){_grpSetH(el,'<div style="color:var(--text-dim);padding:20px 0;text-align:center">\u041d\u0435\u0442 \u0433\u0440\u0443\u043f\u043f</div>');return}
  var h='';
  for(var i=0;i<groups.length;i++){
    var g=groups[i];
    var cnt=counts[g.id]||0;
    var bals=g.balancers?Object.keys(g.balancers):[];
    var balDenied=0;bals.forEach(function(k){if(!g.balancers[k])balDenied++});
    var balText=bals.length===0?'\u0432\u0441\u0435':(bals.length-balDenied)+' \u0438\u0437 '+bals.length;
    var defBadge=g.is_default?' <span style="background:var(--accent-a15);color:var(--accent);padding:2px 8px;border-radius:8px;font-size:10px;font-weight:700">\u043f\u043e \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044e</span>':'';
    h+='<div style="background:var(--surface2);border:1px solid var(--border);border-radius:12px;padding:16px;margin-bottom:10px;cursor:pointer;transition:border-color .2s" onclick="openGroupPanel(\''+_grpEsc(g.id)+'\')" onmouseover="this.style.borderColor=\'rgba(6,182,212,0.3)\'" onmouseout="this.style.borderColor=\'var(--border)\'">';
    h+='<div style="display:flex;align-items:center;gap:10px;margin-bottom:6px">';
    h+='<span style="font-size:16px;font-weight:700;color:var(--text)">'+_grpEsc(g.name)+'</span>'+defBadge;
    h+='<span style="margin-left:auto;font-size:12px;color:var(--text-dim)">'+cnt+' \u043f\u043e\u043b\u044c\u0437.</span>';
    h+='</div>';
    h+='<div style="display:flex;gap:16px;font-size:12px;color:var(--text-muted);flex-wrap:wrap">';
    h+='<span>\u0423\u0441\u0442\u0440\u043e\u0439\u0441\u0442\u0432: '+(g.max_devices===-1?'\u221e':(g.max_devices||'\u0441\u0435\u0440\u0432\u0435\u0440'))+'</span>';
    h+='<span>TorrServer: '+(g.torrserver?'\u2714':'\u2716')+'</span>';
    h+='<span>SISI: '+(g.sisi?'\u2714':'\u2716')+'</span>';
    h+='<span>\u0411\u0430\u043b\u0430\u043d\u0441\u0435\u0440\u044b: '+balText+'</span>';
    h+='</div>';
    if(g.description)h+='<div style="font-size:11px;color:var(--text-dim);margin-top:4px">'+_grpEsc(g.description)+'</div>';
    h+='</div>';
  }
  _grpSetH(el,h);
}

window.openGroupPanel=function(id){
  var g=null;
  (_grpData.groups||[]).forEach(function(x){if(x.id===id)g=x});
  if(!g)return;
  var p=document.getElementById('grp-panel-content');
  if(!p)return;
  var h='<div style="padding:16px">';
  h+='<h3 style="color:var(--text);margin-bottom:16px">\u0420\u0435\u0434\u0430\u043a\u0442\u0438\u0440\u043e\u0432\u0430\u043d\u0438\u0435: '+_grpEsc(g.name)+'</h3>';
  h+='<div style="margin-bottom:12px"><label style="font-size:12px;color:var(--text-muted);display:block;margin-bottom:4px">\u041d\u0430\u0437\u0432\u0430\u043d\u0438\u0435</label><input id="grp-e-name" class="input-sm" value="'+_grpEsc(g.name)+'" style="width:100%"></div>';
  h+='<div style="margin-bottom:12px"><label style="font-size:12px;color:var(--text-muted);display:block;margin-bottom:4px">\u041e\u043f\u0438\u0441\u0430\u043d\u0438\u0435</label><input id="grp-e-desc" class="input-sm" value="'+_grpEsc(g.description||'')+'" style="width:100%"></div>';
  h+='<div style="margin-bottom:12px"><label style="font-size:12px;color:var(--text-muted);display:block;margin-bottom:4px">\u041b\u0438\u043c\u0438\u0442 \u0443\u0441\u0442\u0440\u043e\u0439\u0441\u0442\u0432 (0 = \u0441\u0435\u0440\u0432\u0435\u0440, -1 = \u221e)</label><input id="grp-e-devices" class="input-sm" type="number" value="'+(g.max_devices||0)+'" style="width:100px"></div>';
  h+='<div style="margin-bottom:12px;display:flex;gap:20px">';
  h+='<label style="display:flex;align-items:center;gap:6px;font-size:13px;color:var(--text);cursor:pointer"><input type="checkbox" id="grp-e-ts" '+(g.torrserver?'checked':'')+' style="accent-color:var(--accent)"> TorrServer</label>';
  h+='<label style="display:flex;align-items:center;gap:6px;font-size:13px;color:var(--text);cursor:pointer"><input type="checkbox" id="grp-e-sisi" '+(g.sisi?'checked':'')+' style="accent-color:var(--accent)"> SISI (18+)</label>';
  h+='</div>';
  h+='<div style="margin-bottom:12px"><label style="font-size:12px;color:var(--text-muted);display:block;margin-bottom:6px">\u0411\u0430\u043b\u0430\u043d\u0441\u0435\u0440\u044b (\u043f\u0443\u0441\u0442\u043e = \u0432\u0441\u0435 \u0440\u0430\u0437\u0440\u0435\u0448\u0435\u043d\u044b)</label>';
  h+='<div id="grp-e-bals" style="max-height:250px;overflow-y:auto;background:var(--surface3);border-radius:8px;padding:8px"></div>';
  h+='</div>';
  h+='<div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:16px">';
  h+='<button class="btn btn-primary" onclick="saveGroup(\''+_grpEsc(g.id)+'\')">\u0421\u043e\u0445\u0440\u0430\u043d\u0438\u0442\u044c</button>';
  if(!g.is_default)h+='<button class="btn btn-secondary" onclick="setDefaultGroup(\''+_grpEsc(g.id)+'\')">\u0421\u0434\u0435\u043b\u0430\u0442\u044c \u043f\u043e \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044e</button>';
  if(!g.is_default)h+='<button class="btn btn-danger" onclick="deleteGroup(\''+_grpEsc(g.id)+'\')">\u0423\u0434\u0430\u043b\u0438\u0442\u044c</button>';
  h+='</div></div>';
  _grpSetH(p,h);
  // Load balancer list
  api('/api/balancers',{}).then(function(d){
    var bals=d.balancers||d||[];
    var el=document.getElementById('grp-e-bals');
    if(!el)return;
    var bh='';
    if(Array.isArray(bals)){
      bals.forEach(function(b){
        var name=b.name||b;
        var checked=true;
        if(g.balancers&&typeof g.balancers==='object'&&Object.keys(g.balancers).length>0){
          var key=name.toLowerCase();
          if(g.balancers[key]===false)checked=false;
        }
        bh+='<label style="display:flex;align-items:center;gap:6px;padding:3px 4px;font-size:12px;color:var(--text);cursor:pointer"><input type="checkbox" class="grp-bal-cb" data-name="'+_grpEsc(name)+'" '+(checked?'checked':'')+' style="accent-color:var(--accent)"> '+_grpEsc(b.display_name||b.name||name)+'</label>';
      });
    }
    el.innerHTML=bh||'<span style="color:var(--text-dim);font-size:12px">\u041d\u0435\u0442 \u0431\u0430\u043b\u0430\u043d\u0441\u0435\u0440\u043e\u0432</span>';
  });
  document.getElementById('grp-overlay').classList.add('open');
  document.getElementById('grp-panel').classList.add('open');
};

window.closeGroupPanel=function(){
  document.getElementById('grp-overlay').classList.remove('open');
  document.getElementById('grp-panel').classList.remove('open');
};

window.saveGroup=function(id){
  var name=document.getElementById('grp-e-name').value.trim();
  var desc=document.getElementById('grp-e-desc').value.trim();
  var devices=parseInt(document.getElementById('grp-e-devices').value)||0;
  var ts=document.getElementById('grp-e-ts').checked;
  var sisi=document.getElementById('grp-e-sisi').checked;
  var bals={};
  document.querySelectorAll('.grp-bal-cb').forEach(function(cb){
    bals[cb.dataset.name.toLowerCase()]=cb.checked;
  });
  // If all checked, send empty (= all allowed)
  var allChecked=true;
  for(var k in bals){if(!bals[k]){allChecked=false;break}}
  if(allChecked)bals={};
  _grpPost('/api/groups',{action:'update',id:id,name:name,description:desc,max_devices:devices,torrserver:ts,sisi:sisi,balancers:bals}).then(function(d){
    if(d.ok||d.status==='ok'){toast('\u0413\u0440\u0443\u043f\u043f\u0430 \u0441\u043e\u0445\u0440\u0430\u043d\u0435\u043d\u0430');closeGroupPanel();loadGroups()}
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

window.deleteGroup=function(id){
  if(!confirm('\u0423\u0434\u0430\u043b\u0438\u0442\u044c \u0433\u0440\u0443\u043f\u043f\u0443? \u041f\u043e\u043b\u044c\u0437\u043e\u0432\u0430\u0442\u0435\u043b\u0438 \u0431\u0443\u0434\u0443\u0442 \u043f\u0435\u0440\u0435\u043d\u0435\u0441\u0435\u043d\u044b \u0432 \u0433\u0440\u0443\u043f\u043f\u0443 \u043f\u043e \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044e.'))return;
  _grpPost('/api/groups',{action:'delete',id:id}).then(function(d){
    if(d.ok){toast('\u0413\u0440\u0443\u043f\u043f\u0430 \u0443\u0434\u0430\u043b\u0435\u043d\u0430');closeGroupPanel();loadGroups()}
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

window.setDefaultGroup=function(id){
  _grpPost('/api/groups',{action:'set_default',id:id}).then(function(d){
    if(d.ok){toast('\u0413\u0440\u0443\u043f\u043f\u0430 \u043d\u0430\u0437\u043d\u0430\u0447\u0435\u043d\u0430 \u043f\u043e \u0443\u043c\u043e\u043b\u0447\u0430\u043d\u0438\u044e');closeGroupPanel();loadGroups()}
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

window.createGroupModal=function(){
  var name=prompt('\u041d\u0430\u0437\u0432\u0430\u043d\u0438\u0435 \u0433\u0440\u0443\u043f\u043f\u044b:');
  if(!name)return;
  _grpPost('/api/groups',{action:'create',name:name}).then(function(d){
    if(d.ok){toast('\u0413\u0440\u0443\u043f\u043f\u0430 \u0441\u043e\u0437\u0434\u0430\u043d\u0430');loadGroups()}
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};
})();

// === block 8 (orig admin_tg_panel.go lines 10221-10358) ===
// === Community Plugins ===
(function(){
var _commCatalog=[];
var _esc3=window.esc||function(s){if(s==null)return'';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;')};
function _post3(p,d){return api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(d)})}

function renderCommGrid(list){
  var el=document.getElementById('comm-grid');
  if(!el)return;
  if(!list||!list.length){
    el.innerHTML='<div class="comm-empty"><div class="comm-empty-icon">&#x1F310;</div>'+(api?'\u041a\u0430\u0442\u0430\u043b\u043e\u0433 \u043f\u0443\u0441\u0442 \u0438\u043b\u0438 URL \u043d\u0435 \u0443\u043a\u0430\u0437\u0430\u043d':'\u041d\u0435\u0434\u043e\u0441\u0442\u0443\u043f\u0435\u043d')+'</div>';
    return;
  }
  var h='';
  for(var i=0;i<list.length;i++){
    var p=list[i];
    h+='<div class="comm-card">';
    h+='<div class="comm-card-img">';
    if(p.image_url)h+='<img src="'+_esc3(p.image_url)+'" onerror="this.parentElement.innerHTML=\'&#x1F9E9;\'">';
    else h+='&#x1F9E9;';
    h+='</div>';
    h+='<div class="comm-card-info">';
    h+='<div class="comm-card-name">'+_esc3(p.display_name||p.name)+'</div>';
    if(p.description)h+='<div class="comm-card-desc">'+_esc3(p.description)+'</div>';
    h+='<div class="comm-card-meta">'+_esc3(p.author||'');
    if(p.version)h+=' \u00b7 v'+_esc3(p.version);
    if(p.category)h+=' \u00b7 '+_esc3(p.category);
    if(p.homepage)h+=' \u00b7 <a href="'+_esc3(p.homepage)+'" target="_blank">GitHub</a>';
    h+='</div></div>';
    h+='<div class="comm-card-actions">';
    if(p.installed&&p.update_available){
      h+='<button class="btn btn-warning btn-sm" onclick="updateCommunityPlugin(\''+_esc3(p.name)+'\')">\u2B06 \u041e\u0431\u043d\u043e\u0432\u0438\u0442\u044c</button>';
      h+='<span class="comm-ver-update">'+_esc3(p.installed_version)+' \u2192 '+_esc3(p.version)+'</span>';
    }else if(p.installed){
      h+='<button class="btn btn-danger btn-sm" onclick="uninstallCommunityPlugin(\''+_esc3(p.name)+'\')">\u2716 \u0423\u0434\u0430\u043b\u0438\u0442\u044c</button>';
      h+='<span class="comm-ver-ok">v'+_esc3(p.installed_version)+' \u2714</span>';
    }else{
      h+='<button class="btn btn-primary btn-sm" onclick="installCommunityPlugin(\''+_esc3(p.name)+'\')">\u2B07 \u0423\u0441\u0442\u0430\u043d\u043e\u0432\u0438\u0442\u044c</button>';
    }
    h+='</div></div>';
  }
  el.innerHTML=h;
}

window.loadCommunityPlugins=function(){
  var el=document.getElementById('comm-grid');
  if(el)el.innerHTML='<div class="comm-empty"><div class="comm-empty-icon">&#x23F3;</div>\u0417\u0430\u0433\u0440\u0443\u0437\u043a\u0430 \u043a\u0430\u0442\u0430\u043b\u043e\u0433\u0430...</div>';
  api('/api/communityplugins',{}).then(function(d){
    _commCatalog=d.catalog||[];
    // Badge
    var badge=document.getElementById('comm-badge');
    if(badge){if(d.pending_updates>0){badge.textContent=d.pending_updates+' \u043e\u0431\u043d.';badge.classList.add('show')}else{badge.classList.remove('show')}}
    // Settings
    var urlEl=document.getElementById('comm-url');
    var intEl=document.getElementById('comm-interval');
    if(urlEl)urlEl.value=d.catalog_url||'';
    if(intEl)intEl.value=String(d.auto_update_hours||0);
    // Last check
    var lc=document.getElementById('comm-last-check');
    if(lc){
      var txt='';
      if(d.last_check&&d.last_check!=='0001-01-01T00:00:00Z'){txt+='\u041f\u043e\u0441\u043b\u0435\u0434\u043d\u044f\u044f \u043f\u0440\u043e\u0432\u0435\u0440\u043a\u0430: '+new Date(d.last_check).toLocaleString()}
      if(d.last_error)txt+=' \u00b7 <span style="color:var(--danger)">\u041e\u0448\u0438\u0431\u043a\u0430: '+_esc3(d.last_error)+'</span>';
      lc.innerHTML=txt;
    }
    // Categories
    var cats={};_commCatalog.forEach(function(p){if(p.category)cats[p.category]=1});
    var sel=document.getElementById('comm-category');
    if(sel){sel.innerHTML='<option value="">\u0412\u0441\u0435 \u043a\u0430\u0442\u0435\u0433\u043e\u0440\u0438\u0438</option>';Object.keys(cats).sort().forEach(function(c){sel.innerHTML+='<option value="'+_esc3(c)+'">'+_esc3(c)+'</option>'})}
    renderCommGrid(_commCatalog);
  }).catch(function(e){
    var el=document.getElementById('comm-grid');
    if(el)el.innerHTML='<div class="comm-empty"><div class="comm-empty-icon">&#x26A0;</div>\u041e\u0448\u0438\u0431\u043a\u0430 \u0437\u0430\u0433\u0440\u0443\u0437\u043a\u0438 \u043a\u0430\u0442\u0430\u043b\u043e\u0433\u0430</div>';
  });
};

window.filterCommunityPlugins=function(){
  var q=(document.getElementById('comm-search').value||'').toLowerCase();
  var cat=document.getElementById('comm-category').value;
  var filtered=_commCatalog.filter(function(p){
    if(cat&&p.category!==cat)return false;
    if(q&&(p.name+' '+(p.display_name||'')+' '+(p.description||'')).toLowerCase().indexOf(q)===-1)return false;
    return true;
  });
  renderCommGrid(filtered);
};

window.installCommunityPlugin=function(name){
  toast('\u0423\u0441\u0442\u0430\u043d\u043e\u0432\u043a\u0430 '+name+'...');
  _post3('/api/communityplugins',{action:'install',name:name}).then(function(d){
    if(d.ok){toast('\u041f\u043b\u0430\u0433\u0438\u043d '+name+' \u0443\u0441\u0442\u0430\u043d\u043e\u0432\u043b\u0435\u043d');loadCommunityPlugins();if(window.loadCustomPlugins)loadCustomPlugins()}
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

window.uninstallCommunityPlugin=function(name){
  if(!confirm('\u0423\u0434\u0430\u043b\u0438\u0442\u044c \u043f\u043b\u0430\u0433\u0438\u043d '+name+'?'))return;
  _post3('/api/communityplugins',{action:'uninstall',name:name}).then(function(d){
    if(d.ok){toast('\u041f\u043b\u0430\u0433\u0438\u043d \u0443\u0434\u0430\u043b\u0451\u043d');loadCommunityPlugins();if(window.loadCustomPlugins)loadCustomPlugins()}
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

window.updateCommunityPlugin=function(name){
  toast('\u041e\u0431\u043d\u043e\u0432\u043b\u0435\u043d\u0438\u0435 '+name+'...');
  _post3('/api/communityplugins',{action:'update',name:name}).then(function(d){
    if(d.ok){toast('\u041f\u043b\u0430\u0433\u0438\u043d \u043e\u0431\u043d\u043e\u0432\u043b\u0451\u043d');loadCommunityPlugins()}
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

window.updateAllCommunity=function(){
  toast('\u041e\u0431\u043d\u043e\u0432\u043b\u0435\u043d\u0438\u0435 \u0432\u0441\u0435\u0445 \u043f\u043b\u0430\u0433\u0438\u043d\u043e\u0432...');
  _post3('/api/communityplugins',{action:'update_all'}).then(function(d){
    if(d.ok)toast('\u041e\u0431\u043d\u043e\u0432\u043b\u0435\u043d\u043e: '+(d.updated||0));
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
    loadCommunityPlugins();
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

window.checkCommunityUpdates=function(){
  toast('\u041f\u0440\u043e\u0432\u0435\u0440\u043a\u0430 \u043e\u0431\u043d\u043e\u0432\u043b\u0435\u043d\u0438\u0439...');
  _post3('/api/communityplugins',{action:'check_updates'}).then(function(d){
    if(d.ok)toast('\u0414\u043e\u0441\u0442\u0443\u043f\u043d\u043e \u043e\u0431\u043d\u043e\u0432\u043b\u0435\u043d\u0438\u0439: '+(d.pending||0));
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
    loadCommunityPlugins();
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};

window.saveCommunitySettings=function(){
  var url=(document.getElementById('comm-url').value||'').trim();
  var hours=parseInt(document.getElementById('comm-interval').value)||0;
  _post3('/api/communityplugins',{action:'save_settings',catalog_url:url,auto_update_hours:hours}).then(function(d){
    if(d.ok)toast('\u041d\u0430\u0441\u0442\u0440\u043e\u0439\u043a\u0438 \u043a\u0430\u0442\u0430\u043b\u043e\u0433\u0430 \u0441\u043e\u0445\u0440\u0430\u043d\u0435\u043d\u044b');
    else toast(d.error||'\u041e\u0448\u0438\u0431\u043a\u0430','error');
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error')});
};
})();

// === block 9 (orig admin_tg_panel.go lines 10361-10496) ===
// === AppReplace ===
(function(){
var arRules=[];
var arPresets={
  premium:{name:'Premium unlock',icon:'\ud83d\udc51',desc:'\u0412\u043a\u043b\u044e\u0447\u0430\u0435\u0442 \u0440\u0430\u0441\u0448\u0438\u0440\u0435\u043d\u043d\u044b\u0435 \u0437\u0430\u043a\u043b\u0430\u0434\u043a\u0438 \u0438 \u043f\u0440\u0435\u043c\u0438\u0443\u043c-\u0444\u0443\u043d\u043a\u0446\u0438\u0438',pattern:'Account\\$1\\.hasPremium\\(\\)',replacement:'true',enabled:true},
  hide_subscribe:{name:'Hide subscribe',icon:'\ud83d\udeab',desc:'\u0421\u043a\u0440\u044b\u0432\u0430\u0435\u0442 \u043a\u043d\u043e\u043f\u043a\u0443 \u043f\u043e\u0434\u043f\u0438\u0441\u043a\u0438 \u0432 \u043a\u0430\u0440\u0442\u043e\u0447\u043a\u0435 \u0444\u0438\u043b\u044c\u043c\u0430',pattern:'full-start__button selector button--subscribe',replacement:'full-start__button selector button--subscribe hide',enabled:true},
  disable_vast:{name:'Disable VAST ads',icon:'\ud83d\udcf5',desc:'\u041e\u0442\u043a\u043b\u044e\u0447\u0430\u0435\u0442 VAST \u0440\u0435\u043a\u043b\u0430\u043c\u043d\u044b\u0439 \u043f\u043b\u0430\u0433\u0438\u043d',pattern:'\\[[^\\n\\r]+\'\\/plugin\\/vast\'',replacement:'[\'\{localhost\}/vast.js\'',enabled:true},
  remove_comments:{name:'Remove comments',icon:'\ud83d\uddd1',desc:'\u0423\u0431\u0438\u0440\u0430\u0435\u0442 \u043a\u043d\u043e\u043f\u043a\u0443 \u043a\u043e\u043c\u043c\u0435\u043d\u0442\u0430\u0440\u0438\u0435\u0432 \u0438\u0437 \u043a\u0430\u0440\u0442\u043e\u0447\u043a\u0438',pattern:'var Add = \\{[\\s\\S]*?onCreate: function onCreate\\(\\) \\{',replacement:'var Add = { onCreate: function onCreate() { return;',enabled:true}
};
var _esc=window.esc||function(s){if(s==null)return'';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;')};
function _setH(id,html){var el=typeof id==='string'?document.getElementById(id):id;if(el&&el.innerHTML!==html)el.innerHTML=html}
function _post(p,d){return api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(d)})}

function arUpdateCount(){
  var el=document.getElementById('ar-count');
  var rules=document.querySelectorAll('.ar-rule');
  var n=rules.length;
  var active=0;rules.forEach(function(c){if(c.querySelector('.ar-enabled')&&c.querySelector('.ar-enabled').checked)active++});
  if(el)el.textContent=n===0?'0 \u043f\u0440\u0430\u0432\u0438\u043b':active+' / '+n+' \u0430\u043a\u0442\u0438\u0432\u043d\u043e';
}

function arRuleHTML(rule,idx){
  var c=rule.enabled?'checked':'';
  var dis=rule.enabled?'':'ar-disabled';
  return '<div class="ar-rule ar-card '+dis+'" data-idx="'+idx+'">'
    +'<div class="ar-card-head">'
    +'<span class="ar-drag" title="\u041f\u0440\u0430\u0432\u0438\u043b\u043e #'+(idx+1)+'">&#x2630;</span>'
    +'<input type="text" class="ar-name" value="'+_esc(rule.name)+'" placeholder="\u041d\u0430\u0437\u0432\u0430\u043d\u0438\u0435 \u043f\u0440\u0430\u0432\u0438\u043b\u0430">'
    +'<label class="ar-toggle" title="\u0412\u043a\u043b\u044e\u0447\u0438\u0442\u044c/\u0432\u044b\u043a\u043b\u044e\u0447\u0438\u0442\u044c"><input type="checkbox" class="ar-enabled" '+c+' onchange="arToggleCard(this)"><span class="ar-slider"></span></label>'
    +'<button class="ar-del-btn" onclick="deleteARRow('+idx+')" title="\u0423\u0434\u0430\u043b\u0438\u0442\u044c \u043f\u0440\u0430\u0432\u0438\u043b\u043e">&#x2716;</button>'
    +'</div>'
    +'<div class="ar-card-body">'
    +'<div class="ar-field"><div class="ar-field-label">Regex \u043f\u0430\u0442\u0442\u0435\u0440\u043d</div><textarea class="ar-pattern" rows="2" placeholder="\u0420\u0435\u0433\u0443\u043b\u044f\u0440\u043d\u043e\u0435 \u0432\u044b\u0440\u0430\u0436\u0435\u043d\u0438\u0435 \u0434\u043b\u044f \u043f\u043e\u0438\u0441\u043a\u0430 \u0432 app.min.js">'+_esc(rule.pattern)+'</textarea></div>'
    +'<div class="ar-field"><div class="ar-field-label">\u0417\u0430\u043c\u0435\u043d\u0430</div><textarea class="ar-replacement" rows="2" placeholder="\u0421\u0442\u0440\u043e\u043a\u0430 \u0437\u0430\u043c\u0435\u043d\u044b (\u043c\u043e\u0436\u0435\u0442 \u0431\u044b\u0442\u044c \u043f\u0443\u0441\u0442\u043e\u0439)">'+_esc(rule.replacement)+'</textarea></div>'
    +'</div></div>';
}

window.arToggleCard=function(cb){
  var card=cb.closest('.ar-card');
  if(card){if(cb.checked)card.classList.remove('ar-disabled');else card.classList.add('ar-disabled')}
  arUpdateCount();
};

function renderARRules(){
  var el=document.getElementById('ar-rules');
  if(!el)return;
  if(arRules.length===0){
    _setH(el,'<div class="ar-empty"><div class="ar-empty-icon">&#x1F4DD;</div>\u041d\u0435\u0442 \u043f\u0440\u0430\u0432\u0438\u043b \u0437\u0430\u043c\u0435\u043d\u044b<div class="ar-empty-hint">\u0414\u043e\u0431\u0430\u0432\u044c\u0442\u0435 \u043d\u043e\u0432\u043e\u0435 \u043f\u0440\u0430\u0432\u0438\u043b\u043e \u0432\u0440\u0443\u0447\u043d\u0443\u044e \u0438\u043b\u0438 \u0432\u044b\u0431\u0435\u0440\u0438\u0442\u0435 \u0433\u043e\u0442\u043e\u0432\u044b\u0439 \u043f\u0440\u0435\u0441\u0435\u0442 \u0432\u044b\u0448\u0435</div></div>');
    arUpdateCount();
    return;
  }
  var h='';
  for(var i=0;i<arRules.length;i++) h+=arRuleHTML(arRules[i],i);
  _setH(el,h);
  arUpdateCount();
}

function collectARRules(){
  var cards=document.querySelectorAll('.ar-rule');
  var rules=[];
  cards.forEach(function(card){
    rules.push({
      name:card.querySelector('.ar-name').value.trim(),
      pattern:card.querySelector('.ar-pattern').value,
      replacement:card.querySelector('.ar-replacement').value,
      enabled:card.querySelector('.ar-enabled').checked
    });
  });
  return rules;
}

window.loadAppReplace=function(){
  var el=document.getElementById('ar-rules');
  if(el)_setH(el,'<div class="ar-empty"><div class="ar-empty-icon">&#x23F3;</div>\u0417\u0430\u0433\u0440\u0443\u0437\u043a\u0430...</div>');
  api('/api/appreplace',{}).then(function(data){
    arRules=data||[];
    renderARRules();
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430 \u0437\u0430\u0433\u0440\u0443\u0437\u043a\u0438: '+e,'error')});
  // Also load custom CSS/JS section.
  if(window.loadCustomCode)window.loadCustomCode();
};

window.addARRow=function(){
  arRules=collectARRules();
  arRules.push({name:'',pattern:'',replacement:'',enabled:true});
  renderARRules();
  var cards=document.querySelectorAll('.ar-card');
  if(cards.length>0){var last=cards[cards.length-1];last.scrollIntoView({behavior:'smooth',block:'center'});var inp=last.querySelector('.ar-name');if(inp)setTimeout(function(){inp.focus()},300)}
};

window.deleteARRow=function(idx){
  arRules=collectARRules();
  var name=arRules[idx]?arRules[idx].name||'#'+(idx+1):'#'+(idx+1);
  if(!confirm('\u0423\u0434\u0430\u043b\u0438\u0442\u044c \u043f\u0440\u0430\u0432\u0438\u043b\u043e "'+name+'"?'))return;
  arRules.splice(idx,1);
  renderARRules();
};

window.addARPreset=function(key){
  var p=arPresets[key];
  if(!p)return;
  arRules=collectARRules();
  for(var i=0;i<arRules.length;i++){
    if(arRules[i].name===p.name){toast('\u041f\u0440\u0430\u0432\u0438\u043b\u043e "'+p.name+'" \u0443\u0436\u0435 \u0434\u043e\u0431\u0430\u0432\u043b\u0435\u043d\u043e','warning');return;}
  }
  arRules.push({name:p.name,pattern:p.pattern,replacement:p.replacement,enabled:p.enabled});
  renderARRules();
  toast(p.icon+' '+p.desc);
  var cards=document.querySelectorAll('.ar-card');
  if(cards.length>0){var last=cards[cards.length-1];last.scrollIntoView({behavior:'smooth',block:'center'});last.style.boxShadow='0 0 0 2px var(--accent)';setTimeout(function(){last.style.boxShadow=''},1500)}
};

window.saveAR=function(){
  var rules=collectARRules();
  var btn=document.getElementById('ar-save-btn');
  var statusEl=document.getElementById('ar-status');
  if(btn){btn.disabled=true;btn.textContent='\u23f3 \u0421\u043e\u0445\u0440\u0430\u043d\u0435\u043d\u0438\u0435...'}
  if(statusEl)_setH(statusEl,'');
  _post('/api/appreplace',rules).then(function(d){
    if(btn){btn.disabled=false;btn.innerHTML='&#x1F4BE; \u0421\u043e\u0445\u0440\u0430\u043d\u0438\u0442\u044c'}
    if(d.error){
      toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+d.error,'error');
      if(statusEl)_setH(statusEl,'<span style="color:var(--danger)">'+_esc(d.error)+'</span>');
      return;
    }
    toast('AppReplace \u0441\u043e\u0445\u0440\u0430\u043d\u0451\u043d');
    if(statusEl)_setH(statusEl,'<span style="color:var(--accent)">\u2714 \u0421\u043e\u0445\u0440\u0430\u043d\u0435\u043d\u043e</span>');
    arRules=rules;
    setTimeout(function(){if(statusEl)_setH(statusEl,'')},3000);
  }).catch(function(e){
    if(btn){btn.disabled=false;btn.innerHTML='&#x1F4BE; \u0421\u043e\u0445\u0440\u0430\u043d\u0438\u0442\u044c'}
    toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error');
    if(statusEl)_setH(statusEl,'<span style="color:var(--danger)">'+_esc(String(e))+'</span>');
  });
};
})();

// === block 10 (orig admin_tg_panel.go lines 10499-10568) ===
// === Custom CSS/JS ===
(function(){
var _esc2=window.esc||function(s){if(s==null)return'';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;')};
function _setH2(id,html){var el=typeof id==='string'?document.getElementById(id):id;if(el&&el.innerHTML!==html)el.innerHTML=html}
function _post2(p,d){return api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(d)})}

var ccPresets={
  hide_subscribe:'.button--subscribe,\n.full-start__button.selector.button--subscribe {\n  display: none !important;\n}',
  hide_comments:'.full-start__comments,\n.full-start__button.selector.button--comment {\n  display: none !important;\n}',
  hide_ads:'.ad-banner,\n[class*="vast"],\n[class*="adblock"] {\n  display: none !important;\n}',
  gold_accent:':root {\n  --main-color: #ffd700 !important;\n}'
};

window.ccUpdateLines=function(){
  var cssEl=document.getElementById('cc-css');
  var jsEl=document.getElementById('cc-js');
  var cssLines=document.getElementById('cc-css-lines');
  var jsLines=document.getElementById('cc-js-lines');
  if(cssEl&&cssLines){var n=cssEl.value?cssEl.value.split('\n').length:0;cssLines.textContent=n+' \u0441\u0442\u0440\u043e\u043a'}
  if(jsEl&&jsLines){var n=jsEl.value?jsEl.value.split('\n').length:0;jsLines.textContent=n+' \u0441\u0442\u0440\u043e\u043a'}
};

window.loadCustomCode=function(){
  api('/api/customcode',{}).then(function(data){
    var cssEl=document.getElementById('cc-css');
    var jsEl=document.getElementById('cc-js');
    if(cssEl)cssEl.value=data.custom_css||'';
    if(jsEl)jsEl.value=data.custom_js||'';
    ccUpdateLines();
  }).catch(function(e){toast('\u041e\u0448\u0438\u0431\u043a\u0430 \u0437\u0430\u0433\u0440\u0443\u0437\u043a\u0438 CSS/JS: '+e,'error')});
};

window.addCSSPreset=function(key){
  var p=ccPresets[key];
  if(!p)return;
  var el=document.getElementById('cc-css');
  if(!el)return;
  var v=el.value.trim();
  if(v&&v.indexOf(p.split('\n')[0])!==-1){toast('\u042d\u0442\u043e\u0442 \u043f\u0440\u0435\u0441\u0435\u0442 \u0443\u0436\u0435 \u0434\u043e\u0431\u0430\u0432\u043b\u0435\u043d','warning');return}
  el.value=v?(v+'\n\n'+p):p;
  ccUpdateLines();
  toast('\u041f\u0440\u0435\u0441\u0435\u0442 \u0434\u043e\u0431\u0430\u0432\u043b\u0435\u043d \u0432 CSS');
  el.scrollTop=el.scrollHeight;
};

window.saveCustomCode=function(){
  var cssEl=document.getElementById('cc-css');
  var jsEl=document.getElementById('cc-js');
  var btn=document.getElementById('cc-save-btn');
  var statusEl=document.getElementById('cc-status');
  var payload={custom_css:cssEl?cssEl.value:'',custom_js:jsEl?jsEl.value:''};
  if(btn){btn.disabled=true;btn.textContent='\u23f3 \u0421\u043e\u0445\u0440\u0430\u043d\u0435\u043d\u0438\u0435...'}
  if(statusEl)_setH2(statusEl,'');
  _post2('/api/customcode',payload).then(function(d){
    if(btn){btn.disabled=false;btn.innerHTML='&#x1F4BE; \u0421\u043e\u0445\u0440\u0430\u043d\u0438\u0442\u044c CSS/JS'}
    if(d.error){
      toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+d.error,'error');
      if(statusEl)_setH2(statusEl,'<span style="color:var(--danger)">'+_esc2(d.error)+'</span>');
      return;
    }
    toast('CSS/JS \u0441\u043e\u0445\u0440\u0430\u043d\u0451\u043d');
    if(statusEl)_setH2(statusEl,'<span style="color:#c084fc">\u2714 \u0421\u043e\u0445\u0440\u0430\u043d\u0435\u043d\u043e</span>');
    setTimeout(function(){if(statusEl)_setH2(statusEl,'')},3000);
  }).catch(function(e){
    if(btn){btn.disabled=false;btn.innerHTML='&#x1F4BE; \u0421\u043e\u0445\u0440\u0430\u043d\u0438\u0442\u044c CSS/JS'}
    toast('\u041e\u0448\u0438\u0431\u043a\u0430: '+e,'error');
    if(statusEl)_setH2(statusEl,'<span style="color:var(--danger)">'+_esc2(String(e))+'</span>');
  });
};
})();

// === block 11 (orig admin_tg_panel.go lines 10571-10751) ===
/* === Telemetry tab === */
(function(){
var basePath=location.pathname.replace(/\/+$/,'');
function tlmEsc(s){return String(s||'').replace(/[&<>"']/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]})}
function api(p,opts){return fetch(basePath+p,opts).then(function(r){return r.json()})}
var tlmData=null;
var tlmStream=null;
var tlmPollTimer=null; // fallback polling — на случай если SSE отвалился (nginx-таймаут, прокси-буферизация и т.п.)
var tlmLastUpdateAt=0; // timestamp последнего snapshot-а (от SSE или polling); если SSE сильно отстаёт — polling всё равно докинет
function tlmFmtPct(v){if(v==null||isNaN(v))return '\u2014';return Math.round(v*100)+'%'}
function tlmFmtTime(t){if(!t||t==='0001-01-01T00:00:00Z')return '\u2014';try{var d=new Date(t);var sec=Math.floor((Date.now()-d.getTime())/1000);if(sec<60)return sec+'\u0441 \u043d\u0430\u0437\u0430\u0434';if(sec<3600)return Math.floor(sec/60)+'\u043c \u043d\u0430\u0437\u0430\u0434';if(sec<86400)return Math.floor(sec/3600)+'\u0447 \u043d\u0430\u0437\u0430\u0434';return d.toLocaleDateString('ru-RU')}catch(e){return ''}}
window.openTelemetry=function(){tlmStartLive();tlmRefresh();tlmStartPoll()};
window.tlmRefresh=function(){
  api('/api/telemetry').then(function(d){
    if(!d||!d.enabled){
      var grid=document.getElementById('tlm-grid');if(grid)grid.innerHTML='';
      var em=document.getElementById('tlm-empty');if(em){em.style.display='block';em.textContent='\u0422\u0435\u043b\u0435\u043c\u0435\u0442\u0440\u0438\u044f \u043e\u0442\u043a\u043b\u044e\u0447\u0435\u043d\u0430 \u0432 config.toml ([telemetry] enabled=true).'}
      return;
    }
    tlmData=d;tlmLastUpdateAt=Date.now();tlmRender();
  }).catch(function(e){console.error('tlm refresh',e)});
};
window.tlmRender=function(){
  if(!tlmData||!tlmData.entries){return}
  var search=(document.getElementById('tlm-search')||{}).value||'';
  var statusF=(document.getElementById('tlm-status')||{}).value||'';
  var winK=(document.getElementById('tlm-window')||{}).value||'5m';
  var counts={down:0,degraded:0,healthy:0,unknown:0};
  var visible=[];
  tlmData.entries.forEach(function(e){counts[e.status]=(counts[e.status]||0)+1});
  tlmData.entries.forEach(function(e){
    if(search&&e.name.toLowerCase().indexOf(search.toLowerCase())<0)return;
    if(statusF&&e.status!==statusF)return;
    visible.push(e);
  });
  // Summary
  var sumEl=document.getElementById('tlm-summary');
  if(sumEl){
    sumEl.innerHTML=
      '<div class="tlm-stat"><span class="tlm-stat-label">\u0412\u0441\u0435\u0433\u043e</span><span class="tlm-stat-value">'+tlmData.entries.length+'</span></div>'+
      '<div class="tlm-stat healthy"><span class="tlm-stat-label">\u0417\u0434\u043e\u0440\u043e\u0432\u044b</span><span class="tlm-stat-value">'+counts.healthy+'</span></div>'+
      '<div class="tlm-stat degraded"><span class="tlm-stat-label">\u0414\u0435\u0433\u0440\u0430\u0434\u0438\u0440\u0443\u044e\u0442</span><span class="tlm-stat-value">'+counts.degraded+'</span></div>'+
      '<div class="tlm-stat down"><span class="tlm-stat-label">\u041d\u0435\u0434\u043e\u0441\u0442\u0443\u043f\u043d\u044b</span><span class="tlm-stat-value">'+counts.down+'</span></div>'+
      '<div class="tlm-stat unknown"><span class="tlm-stat-label">\u0411\u0435\u0437 \u0434\u0430\u043d\u043d\u044b\u0445</span><span class="tlm-stat-value">'+counts.unknown+'</span></div>'+
      (tlmData.alerts_on?'<div class="tlm-stat"><span class="tlm-stat-label">TG \u0430\u043b\u0435\u0440\u0442\u044b</span><span class="tlm-stat-value" style="color:#34d399;font-size:18px">\u0432\u043a\u043b\u044e\u0447\u0435\u043d\u044b</span></div>':'<div class="tlm-stat"><span class="tlm-stat-label">TG \u0430\u043b\u0435\u0440\u0442\u044b</span><span class="tlm-stat-value" style="color:var(--text-dim);font-size:18px">\u0432\u044b\u043a\u043b</span></div>');
  }
  var grid=document.getElementById('tlm-grid');
  var em=document.getElementById('tlm-empty');
  if(!visible.length){if(grid)grid.innerHTML='';if(em){em.style.display='block';em.textContent='\u041f\u043e\u0434 \u0444\u0438\u043b\u044c\u0442\u0440 \u043d\u0438\u0447\u0435\u0433\u043e \u043d\u0435 \u043f\u043e\u043f\u0430\u043b\u043e'}return}
  if(em)em.style.display='none';
  grid.innerHTML=visible.map(function(e){return tlmCardHTML(e,winK)}).join('');
};
function tlmCardHTML(e,winK){
  var snap=e.snapshot||{};var W=(snap.windows||{})[winK]||{};
  var rate=(W.total>0)?tlmFmtPct(W.success_rate):'\u2014';
  var totals=W.total>0?(W.success+'/'+W.total):'\u043d\u0435\u0442 \u0437\u0430\u043f\u0440\u043e\u0441\u043e\u0432';
  var bar='';
  if(W.total>0){
    var okPct=W.success_rate*100;var failPct=100-okPct;
    bar='<div class="tlm-card-bar"><div class="tlm-bar-ok" style="width:'+okPct+'%"></div><div class="tlm-bar-fail" style="width:'+failPct+'%"></div></div>';
  }
  var lat=W.total>0?
    '<div class="tlm-card-lat"><span>p50: <b>'+W.p50_latency_ms+'ms</b></span><span>p95: <b>'+W.p95_latency_ms+'ms</b></span><span>p99: <b>'+W.p99_latency_ms+'ms</b></span></div>':'';
  // Checksearch row \u2014 separate ring buffer of /lite/<bal>?checksearch=true probes.
  // Shows success rate for "can we find this film?" probes, which captures
  // upstream health even for balancers that don't yet go through balancerFetch.
  var csRow='';
  if(e.checksearch){
    var csW=(e.checksearch.windows||{})[winK]||{};
    if(csW.total>0){
      csRow='<div class="tlm-card-cs" title="Checksearch: \u043f\u0440\u043e\u0431\u044b /lite/&lt;bal&gt;?checksearch=true">'+
        '<span class="tlm-cs-label">\ud83d\udd0d checksearch '+winK+':</span> '+
        '<b>'+tlmFmtPct(csW.success_rate)+'</b> '+
        '<span class="tlm-cs-totals">('+csW.success+'/'+csW.total+', p95 '+csW.p95_latency_ms+'ms)</span>'+
      '</div>';
    }
  }
  // Circuit breaker indicator.
  var breaker='';
  if(e.breaker_open){
    var until=e.breaker_open_until?(' \u0434\u043e '+tlmFmtTime(e.breaker_open_until)):'';
    breaker='<div class="tlm-card-breaker open">\u26a0\ufe0f Breaker OPEN'+until+' '+
      '<button class="tlm-fb-edit" onclick="tlmResetBreaker(\''+tlmEsc(e.name)+'\')">\u267b \u0441\u0431\u0440\u043e\u0441\u0438\u0442\u044c</button></div>';
  }else if(e.breaker_fail_count>0){
    breaker='<div class="tlm-card-breaker pending">\u23f1 Breaker: '+e.breaker_fail_count+' fail(s)</div>';
  }
  var host='';
  if(snap.current_host){
    host='<div class="tlm-card-host">\ud83d\udce1 <code>'+tlmEsc(snap.current_host)+'</code>';
    if(e.has_fallback)host+='<span class="tlm-fbcount">+'+(e.fallbacks.length-1)+' fallback</span>';
    host+=' <button class="tlm-fb-edit" onclick="tlmEditFallbacks(\''+tlmEsc(e.name)+'\')">\u2699</button>';
    host+='</div>';
  }else if(e.has_fallback){
    host='<div class="tlm-card-host">\ud83d\udce1 <code>'+tlmEsc(e.fallbacks[0])+'</code> <span class="tlm-fbcount">+'+(e.fallbacks.length-1)+'</span> <button class="tlm-fb-edit" onclick="tlmEditFallbacks(\''+tlmEsc(e.name)+'\')">\u2699</button></div>';
  }else{
    host='<div class="tlm-card-host"><button class="tlm-fb-edit" onclick="tlmEditFallbacks(\''+tlmEsc(e.name)+'\')">\u2295 \u0437\u0430\u0434\u0430\u0442\u044c fallback</button></div>';
  }
  var err='';
  if(snap.recent_errors&&snap.recent_errors.length){
    var er=snap.recent_errors[0];
    err='<div class="tlm-card-err">'+(er.status?'['+er.status+'] ':'')+tlmEsc(er.err||'')+' <small>\u00b7 '+tlmFmtTime(er.time)+'</small></div>';
  }
  var actions=
    '<button onclick="tlmRotate(\''+tlmEsc(e.name)+'\')"'+(e.has_fallback?'':' disabled style="opacity:.4"')+'>\ud83d\udd04 \u0421\u043c\u0435\u043d\u0438\u0442\u044c \u0445\u043e\u0441\u0442</button>'+
    '<button onclick="tlmReset(\''+tlmEsc(e.name)+'\')">\u267b \u0421\u0431\u0440\u043e\u0441</button>';
  var qBadge=e.quality?'<span class="tlm-card-q q-'+e.quality+'">'+e.quality+'</span>':'';
  return ''+
    '<div class="tlm-card s-'+e.status+(e.breaker_open?' breaker-open':'')+'">'+
      '<div class="tlm-card-head">'+
        '<span class="tlm-card-dot"></span>'+
        '<span class="tlm-card-name">'+tlmEsc(e.name)+qBadge+'</span>'+
        '<span class="tlm-card-totals">'+totals+'</span>'+
      '</div>'+
      bar+
      '<div class="tlm-card-metrics"><span class="tlm-card-rate">'+rate+'</span><span class="tlm-card-rate-label">\u0443\u0441\u043f\u0435\u0448\u043d\u044b\u0445 \u0437\u0430 '+winK+'</span></div>'+
      lat+
      csRow+
      breaker+
      host+
      err+
      '<div class="tlm-card-actions">'+actions+'</div>'+
    '</div>';
}
window.tlmResetBreaker=function(name){
  fetch(basePath+'/api/telemetry',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'reset_breaker',balancer:name})}).then(function(r){return r.json()}).then(function(){toast('Breaker \u0441\u0431\u0440\u043e\u0448\u0435\u043d');tlmRefresh()});
};
window.tlmReset=function(name){
  if(!confirm(name?('\u0421\u0431\u0440\u043e\u0441\u0438\u0442\u044c \u0441\u0442\u0430\u0442\u0438\u0441\u0442\u0438\u043a\u0443 \u043f\u043e '+name+'?'):'\u0421\u0431\u0440\u043e\u0441\u0438\u0442\u044c \u0441\u0442\u0430\u0442\u0438\u0441\u0442\u0438\u043a\u0443 \u0412\u0421\u0415\u0425 \u0431\u0430\u043b\u0430\u043d\u0441\u0435\u0440\u043e\u0432?'))return;
  fetch(basePath+'/api/telemetry',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'reset',balancer:name})}).then(function(r){return r.json()}).then(function(){tlmRefresh();toast('\u0421\u0431\u0440\u043e\u0448\u0435\u043d\u043e')});
};
window.tlmRotate=function(name){
  fetch(basePath+'/api/telemetry',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'rotate_host',balancer:name})}).then(function(r){return r.json()}).then(function(d){toast('\u041f\u0435\u0440\u0435\u043a\u043b\u044e\u0447\u0435\u043d\u043e \u043d\u0430: '+(d.host||'-'));tlmRefresh()});
};
window.tlmEditFallbacks=function(name){
  var entry=null;
  if(tlmData)tlmData.entries.forEach(function(e){if(e.name===name)entry=e});
  var current=(entry&&entry.fallbacks||[]).join('\n');
  var v=prompt('\u0421\u043f\u0438\u0441\u043e\u043a fallback-\u0445\u043e\u0441\u0442\u043e\u0432 \u0434\u043b\u044f '+name+' (\u043f\u043e \u043e\u0434\u043d\u043e\u043c\u0443 \u043d\u0430 \u0441\u0442\u0440\u043e\u043a\u0443, \u043f\u0435\u0440\u0432\u044b\u0439 \u2014 \u043e\u0441\u043d\u043e\u0432\u043d\u043e\u0439):',current);
  if(v==null)return;
  var hosts=v.split(/\n+/).map(function(s){return s.trim()}).filter(Boolean);
  fetch(basePath+'/api/telemetry',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'set_fallbacks',balancer:name,hosts:hosts})}).then(function(r){return r.json()}).then(function(){toast('\u0421\u043e\u0445\u0440\u0430\u043d\u0435\u043d\u043e');tlmRefresh()});
};
function tlmStartLive(){
  if(tlmStream){return}
  var live=(document.getElementById('tlm-live')||{}).checked;
  if(!live)return;
  try{
    tlmStream=new EventSource(basePath+'/api/telemetry/stream');
    tlmStream.addEventListener('snapshot',function(ev){
      try{var d=JSON.parse(ev.data);if(d&&d.entries){tlmData=Object.assign(tlmData||{enabled:true,alerts_on:false},d);tlmLastUpdateAt=Date.now();tlmRender()}}catch(e){}
    });
    tlmStream.onerror=function(){
      // SSE рвётся часто (nginx-проксирующий, idle-cap). Закрываем и не
      // пытаемся переподключиться — fallback-polling всё равно тянет данные.
      if(tlmStream){tlmStream.close();tlmStream=null}
    };
  }catch(e){console.error('tlm stream',e)}
}
function tlmStopLive(){if(tlmStream){tlmStream.close();tlmStream=null}}
// tlmStartPoll — fallback на случай если SSE отвалился. Тянет /api/telemetry
// каждые 5с но только если последний snapshot старше 5с (если SSE приходит
// чаще — polling простаивает, не дублирует запросы).
function tlmStartPoll(){
  if(tlmPollTimer)return;
  tlmPollTimer=setInterval(function(){
    if(Date.now()-tlmLastUpdateAt<4500)return; // SSE недавно дослал — polling skip
    tlmRefresh();
  },5000);
}
function tlmStopPoll(){if(tlmPollTimer){clearInterval(tlmPollTimer);tlmPollTimer=null}}
document.addEventListener('DOMContentLoaded',function(){
  var live=document.getElementById('tlm-live');
  if(live)live.addEventListener('change',function(){if(live.checked){tlmStartLive()}else{tlmStopLive()}});
  // Stop SSE+polling when leaving the tab to save resources.
  document.querySelectorAll('.tab').forEach(function(t){
    t.addEventListener('click',function(){
      if(t.dataset.tab!=='telemetry'){tlmStopLive();tlmStopPoll()}
    });
  });
});
})();

// === block 12 (orig admin_tg_panel.go lines 10754-10852) ===
/* === Media Gateway tab === */
(function(){
var basePath=location.pathname.replace(/\/+$/,'');
function mgwEsc(s){return String(s||'').replace(/[&<>"']/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]})}
function api(p,opts){return fetch(basePath+p,opts).then(function(r){return r.json()})}
function fmtSec(n){if(!n||n<0)return '—';if(n<60)return n+'с';if(n<3600)return Math.floor(n/60)+'м '+Math.floor(n%60)+'с';return Math.floor(n/3600)+'ч '+Math.floor((n%3600)/60)+'м'}
var mgwTimer=null;
window.openMediaGW=function(){
  mgwLoadJobs();
  if(mgwTimer)clearInterval(mgwTimer);
  mgwTimer=setInterval(function(){
    var p=document.getElementById('panel-mediagw');
    if(p&&p.classList.contains('active'))mgwLoadJobs();
  },5000);
};
function mgwLoadJobs(){
  api('/api/media/jobs').then(function(d){
    var list=document.getElementById('mgw-jobs-list');
    var em=document.getElementById('mgw-jobs-empty');
    var sum=document.getElementById('mgw-jobs-summary');
    if(!d||!d.enabled){
      if(list)list.innerHTML='';
      if(em){em.style.display='block';em.textContent='Сервис транскодинга отключён ([transcoding] enable=false).'}
      if(sum)sum.innerHTML='';
      return;
    }
    var jobs=(d.jobs||[]);
    var run=jobs.filter(function(j){return j.state==='running'}).length;
    var stopped=jobs.length-run;
    if(sum){
      sum.innerHTML=
        '<div class="mgw-stat"><span class="mgw-stat-l">Активных</span><span class="mgw-stat-v run">'+run+'</span></div>'+
        '<div class="mgw-stat"><span class="mgw-stat-l">Завершено в очереди</span><span class="mgw-stat-v idle">'+stopped+'</span></div>'+
        '<div class="mgw-stat"><span class="mgw-stat-l">Всего</span><span class="mgw-stat-v">'+jobs.length+'</span></div>';
    }
    if(!jobs.length){
      if(list)list.innerHTML='';
      if(em){em.style.display='block';em.textContent='Нет активных задач транскодинга'}
      return;
    }
    if(em)em.style.display='none';
    jobs.sort(function(a,b){return (b.uptime_s||0)-(a.uptime_s||0)});
    list.innerHTML=jobs.map(function(j){return mgwJobHTML(j)}).join('');
  }).catch(function(e){console.error('mgw jobs',e)});
}
function mgwJobHTML(j){
  var src=j.original_url||j.source||'';
  var srcShort=src.length>110?src.substring(0,107)+'…':src;
  return '<div class="mgw-job s-'+(j.state||'running')+'">'+
    '<div class="mgw-job-id">'+mgwEsc(j.id.substring(0,12))+'</div>'+
    '<div class="mgw-job-info">'+
      '<div><span class="mgw-job-mode">'+mgwEsc(j.mode||'?')+'</span></div>'+
      '<div class="mgw-job-src" title="'+mgwEsc(src)+'">'+mgwEsc(srcShort)+'</div>'+
      '<div class="mgw-job-meta">'+
        '<span>uptime: <b>'+fmtSec(j.uptime_s)+'</b></span>'+
        '<span>last access: <b>'+fmtSec(j.last_access_s)+' назад</b></span>'+
        '<span>seg: <b>'+(j.last_segment||0)+'</b></span>'+
        (j.warning?'<span style="color:#fbbf24">⚠ '+mgwEsc(j.warning)+'</span>':'')+
      '</div>'+
    '</div>'+
    '<div class="mgw-job-actions">'+
      (j.state==='running'?'<button onclick="mgwKill(\''+mgwEsc(j.id)+'\')">⏹ Stop</button>':'<span style="font-size:10px;color:var(--text-dim)">exit '+(j.exit_code||0)+'</span>')+
    '</div>'+
  '</div>';
}
window.mgwKill=function(id){
  if(!confirm('Остановить задачу '+id.substring(0,12)+'?'))return;
  fetch(basePath+'/api/media/jobs/kill?id='+encodeURIComponent(id),{method:'POST'}).then(function(r){return r.json()}).then(function(){toast('Задача остановлена');setTimeout(mgwLoadJobs,500)});
};
window.mgwProbe=function(){
  var url=(document.getElementById('mgw-probe-url')||{}).value;
  if(!url){toast('Введите URL','error');return}
  var resEl=document.getElementById('mgw-probe-result');
  resEl.innerHTML='<div style="color:var(--text-muted);font-size:12px">⏳ Запуск ffprobe (до 10 сек)...</div>';
  api('/api/media/probe?url='+encodeURIComponent(url)).then(function(d){
    if(!d.ok){
      resEl.innerHTML='<div class="mgw-probe-error">'+mgwEsc(d.error||'probe failed')+'</div>';
      return;
    }
    var s=d.summary||{};
    var tags='';
    if(s.codec)tags+='<span class="mgw-probe-tag codec">📹 '+mgwEsc(s.codec)+'</span>';
    if(s.width&&s.height)tags+='<span class="mgw-probe-tag">'+s.width+'×'+s.height+'</span>';
    if(s.fps)tags+='<span class="mgw-probe-tag">'+s.fps+' fps</span>';
    if(s.audio_codec)tags+='<span class="mgw-probe-tag audio">🔊 '+mgwEsc(s.audio_codec)+'</span>';
    if(s.audio_count>1)tags+='<span class="mgw-probe-tag audio">×'+s.audio_count+' дорожки</span>';
    if(s.duration)tags+='<span class="mgw-probe-tag dur">⏱ '+fmtSec(Math.round(s.duration))+'</span>';
    if(s.bitrate)tags+='<span class="mgw-probe-tag">'+(Math.round(s.bitrate/1000))+' kbps</span>';
    if(!tags)tags='<span class="mgw-probe-tag">нет данных</span>';
    var details=
      '<div class="mgw-probe-streams">'+
        '<div>'+tags+'</div>'+
        '<div style="margin-top:8px;color:var(--text-dim)">⏱ probe занял '+(d.took_ms||0)+'ms</div>'+
        '<details><summary style="cursor:pointer;color:var(--accent);margin-top:8px">Полный JSON ответ</summary><pre>'+mgwEsc(JSON.stringify(d.probe,null,2))+'</pre></details>'+
      '</div>';
    resEl.innerHTML=details;
  }).catch(function(e){resEl.innerHTML='<div class="mgw-probe-error">'+mgwEsc(String(e))+'</div>'});
};
})();

// === block 13 (WAF tab) ===
// --- WAF (Web Application Firewall) ---
(function(){
  var basePath=(window.basePath||location.pathname.replace(/\/+$/,''));
  function api(p,o){return fetch(basePath+p,o).then(function(r){return r.json()})}
  function post(p,d){return api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(d)})}
  var esc=window.esc||function(s){if(s==null)return'';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/"/g,'&quot;')};
  var wafCache=null;
  function wafToast(msg,err){
    var el=document.getElementById('waf-toast');if(!el)return;
    el.style.display='';el.textContent=msg;
    if(err){el.style.background='rgba(220,80,80,0.15)';el.style.borderColor='rgba(220,80,80,0.4)';el.style.color='#ff8b8b'}
    else{el.style.background='rgba(64,180,120,0.15)';el.style.borderColor='rgba(64,180,120,0.4)';el.style.color='#5dd99a'}
    setTimeout(function(){if(el)el.style.display='none'},3500);
  }
  function fmtTs(unix){if(!unix||unix<=0)return '—';var d=new Date(unix*1000);return d.toLocaleString('ru-RU',{day:'2-digit',month:'2-digit',hour:'2-digit',minute:'2-digit'})}
  function fmtUntil(unix){if(!unix||unix<=0)return '<span style="color:#ff8b8b">постоянный</span>';var remain=unix-Math.floor(Date.now()/1000);if(remain<=0)return '<span style="color:var(--text-dim)">истёк</span>';return fmtTs(unix)}

  function linesToArr(txt){if(!txt)return[];return txt.split(/\r?\n/).map(function(s){return s.trim()}).filter(Boolean)}
  function arrToLines(arr){if(!arr||!arr.length)return '';return arr.join('\n')}
  function csvToArr(s){if(!s)return[];return s.split(',').map(function(x){return x.trim().toUpperCase()}).filter(Boolean)}
  function arrToCsv(a){if(!a||!a.length)return '';return a.join(', ')}

  function parseLimitMap(txt){
    var out={};
    linesToArr(txt).forEach(function(line){
      var parts=line.split('|');
      if(parts.length<3)return;
      var pat=parts[0];var lim=parseInt(parts[1],10);var sec=parseInt(parts[2],10);
      if(!pat||!lim||!sec)return;
      var rule={limit:lim,second:sec};
      for(var i=3;i<parts.length;i++){
        var p=parts[i];
        if(p==='pathId')rule.pathId=true;
        else if(p.indexOf('queryIds=')===0)rule.queryIds=p.substring(9).split(',').map(function(s){return s.trim()}).filter(Boolean);
      }
      out[pat]=rule;
    });
    return out;
  }
  function limitMapToText(m){
    if(!m)return '';
    var lines=[];
    for(var k in m){
      var r=m[k];
      var line=k+'|'+(r.limit||0)+'|'+(r.second||0);
      if(r.pathId)line+='|pathId';
      if(r.queryIds&&r.queryIds.length)line+='|queryIds='+r.queryIds.join(',');
      lines.push(line);
    }
    return lines.join('\n');
  }

  function parseHeadersDeny(txt){
    var out={};
    linesToArr(txt).forEach(function(line){
      var i=line.indexOf('|');if(i<=0)return;
      out[line.substring(0,i).trim()]=line.substring(i+1);
    });
    return out;
  }
  function headersDenyToText(m){
    if(!m)return '';
    var lines=[];
    for(var k in m)lines.push(k+'|'+m[k]);
    return lines.join('\n');
  }

  function renderBans(list){
    var body=document.getElementById('waf-bans-body');
    var empty=document.getElementById('waf-bans-empty');
    if(!body)return;
    if(!list||!list.length){body.innerHTML='';empty.style.display='';return}
    empty.style.display='none';
    body.innerHTML=list.map(function(b){
      return '<tr>'
        +'<td style="font-family:monospace;font-size:13px">'+esc(b.ip)+'</td>'
        +'<td style="color:var(--text-muted);font-size:12px">'+esc(b.reason||'—')+'</td>'
        +'<td style="font-size:12px">'+fmtUntil(b.until)+'</td>'
        +'<td style="font-size:12px;color:var(--text-dim)">'+fmtTs(b.bannedAt)+'</td>'
        +'<td><button class="btn btn-danger btn-sm" onclick="wafBanRemove(\''+esc(b.ip).replace(/'/g,"\\'")+'\')">✕</button></td>'
        +'</tr>';
    }).join('');
  }

  function renderStats(stats){
    if(!stats)stats={};
    var setT=function(id,v){var e=document.getElementById(id);if(e)e.textContent=v};
    var setH=function(id,v){var e=document.getElementById(id);if(e)e.innerHTML=v};
    setT('waf-total24h',stats.total_24h||0);
    var reasons=stats.by_reason||{};
    var rkeys=Object.keys(reasons).sort(function(a,b){return reasons[b]-reasons[a]});
    if(!rkeys.length){setH('waf-reasons','<span style="color:var(--text-dim)">пусто</span>')}
    else setH('waf-reasons',rkeys.map(function(k){return '<div style="display:flex;justify-content:space-between"><span>'+esc(k)+'</span><b>'+reasons[k]+'</b></div>'}).join(''));
    var paths=stats.top_paths||[];
    if(!paths.length)setH('waf-top-paths','<span style="color:var(--text-dim)">пусто</span>');
    else setH('waf-top-paths',paths.slice(0,7).map(function(p){return '<div style="display:flex;justify-content:space-between;gap:8px"><span style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-family:monospace">'+esc(p.key)+'</span><b>'+p.count+'</b></div>'}).join(''));
    var ips=stats.top_ips||[];
    if(!ips.length)setH('waf-top-ips','<span style="color:var(--text-dim)">пусто</span>');
    else setH('waf-top-ips',ips.slice(0,7).map(function(p){return '<div style="display:flex;justify-content:space-between;gap:8px"><span style="font-family:monospace">'+esc(p.key)+'</span><b>'+p.count+'</b></div>'}).join(''));
  }

  function fillForm(cfg){
    if(!cfg)cfg={};
    var $=function(id){return document.getElementById(id)};
    $('waf-enable').checked=!!cfg.enable;
    $('waf-bypassLocal').checked=!!cfg.bypassLocalIP;
    $('waf-brute').checked=!!cfg.bruteForceProtection;
    $('waf-bruteLimit').value=cfg.bruteForceLimit||0;
    $('waf-limitReq').value=cfg.limit_req||0;
    $('waf-limitMap').value=limitMapToText(cfg.limit_map);
    $('waf-white').value=arrToLines(cfg.whiteIps);
    $('waf-ipsDeny').value=arrToLines(cfg.ipsDeny);
    $('waf-ipsAllow').value=arrToLines(cfg.ipsAllow);
    $('waf-countryDeny').value=arrToCsv(cfg.countryDeny);
    $('waf-countryAllow').value=arrToCsv(cfg.countryAllow);
    $('waf-wlPaths').value=arrToLines(cfg.customWhitelistPaths);
    $('waf-wlPrefixes').value=arrToLines(cfg.customWhitelistPrefixes);
    $('waf-headersDeny').value=headersDenyToText(cfg.headersDeny);
  }
  function buildCfg(){
    var $=function(id){return document.getElementById(id)};
    return {
      enable:$('waf-enable').checked,
      bypassLocalIP:$('waf-bypassLocal').checked,
      bruteForceProtection:$('waf-brute').checked,
      bruteForceLimit:parseInt($('waf-bruteLimit').value,10)||0,
      whiteIps:linesToArr($('waf-white').value),
      limit_req:parseInt($('waf-limitReq').value,10)||0,
      limit_map:parseLimitMap($('waf-limitMap').value),
      ipsDeny:linesToArr($('waf-ipsDeny').value),
      ipsAllow:linesToArr($('waf-ipsAllow').value),
      countryDeny:csvToArr($('waf-countryDeny').value),
      countryAllow:csvToArr($('waf-countryAllow').value),
      headersDeny:parseHeadersDeny($('waf-headersDeny').value),
      customWhitelistPaths:linesToArr($('waf-wlPaths').value),
      customWhitelistPrefixes:linesToArr($('waf-wlPrefixes').value)
    };
  }
  function applyState(d){
    wafCache=d||{};
    fillForm(d.config);
    renderStats(d.stats);
    renderBans(d.manualBans);
  }

  window.loadWAF=function(){
    api('/api/waf/state').then(function(d){applyState(d)})
      .catch(function(e){wafToast('Ошибка: '+e.message,true)});
  };
  window.wafSave=function(){
    post('/api/waf/config',buildCfg()).then(function(d){
      if(d&&d.error){wafToast(d.error,true);return}
      applyState(d);wafToast('Конфиг сохранён и применён');
    }).catch(function(e){wafToast('Ошибка: '+e.message,true)});
  };
  window.wafReload=function(){
    post('/api/waf/reload',{}).then(function(d){
      applyState(d);wafToast('Перечитан init.conf');
    }).catch(function(e){wafToast('Ошибка: '+e.message,true)});
  };
  window.wafBanAdd=function(){
    var ip=document.getElementById('waf-ban-ip').value.trim();
    var ttl=parseInt(document.getElementById('waf-ban-ttl').value,10)||0;
    var reason=document.getElementById('waf-ban-reason').value.trim();
    if(!ip){wafToast('Укажите IP',true);return}
    post('/api/waf/ban',{ip:ip,ttlSec:ttl,reason:reason}).then(function(d){
      if(d&&d.error){wafToast(d.error,true);return}
      renderBans(d.manualBans);
      document.getElementById('waf-ban-ip').value='';
      document.getElementById('waf-ban-reason').value='';
      wafToast('Бан добавлен: '+ip);
    }).catch(function(e){wafToast('Ошибка: '+e.message,true)});
  };
  window.wafBanRemove=function(ip){
    if(!confirm('Удалить бан '+ip+'?'))return;
    // POST /waf/ban/remove with body — CIDR-safe (DELETE /waf/ban/{ip} chokes
    // on '/' in the path because chi treats %2F as a segment separator).
    post('/api/waf/ban/remove',{ip:ip}).then(function(d){
      if(d&&d.error){wafToast(d.error,true);return}
      renderBans(d.manualBans);wafToast('Бан снят');
    }).catch(function(e){wafToast('Ошибка: '+e.message,true)});
  };

  // --- Commerce (CryptoCloud + tariffs + invoices) ---
  // Lives at the bottom because it depends on shared helpers (api, post, esc,
  // copyToClip, toast) defined earlier in this file. State is module-private
  // so multiple panel re-opens don't leak.
  var cmState={cfg:null,groups:[],edit:null,invoices:[],revenue:0,total:0};

  window.loadCommerce=function(){
    // Called when the operator opens the "Коммерция" tab. Pulls config +
    // invoices in parallel, then renders.
    var statsEl=document.getElementById('cm-stats');
    if(statsEl)statsEl.innerHTML='<div style="color:var(--text-dim);font-size:13px">Загрузка...</div>';
    Promise.all([api('/api/payments/config'),fetchCmInvoices()]).then(function(arr){
      cmState.cfg=arr[0];
      cmState.groups=arr[0].available_groups||[];
      // Deep-clone editable copy. Secrets are stripped — we show masked
      // placeholders and only send a new value when the operator types one.
      cmState.edit=JSON.parse(JSON.stringify(arr[0]));
      cmState.edit.cryptocloud.api_key='';
      cmState.edit.cryptocloud.webhook_secret='';
      renderCmConfig();
      renderCmInvoices();
    }).catch(function(e){
      if(statsEl)statsEl.innerHTML='<div style="color:#ff6b6b;padding:8px 0">Ошибка: '+esc(e.message)+'</div>';
    });
  };

  function fetchCmInvoices(){
    var status=document.getElementById('cm-inv-status');
    var tgid=document.getElementById('cm-inv-tgid');
    var q=[];
    if(status&&status.value)q.push('status='+encodeURIComponent(status.value));
    if(tgid&&tgid.value)q.push('tg_id='+encodeURIComponent(tgid.value));
    q.push('limit=500');
    return api('/api/payments/invoices?'+q.join('&')).then(function(r){
      cmState.invoices=r.invoices||[];
      cmState.total=r.total||0;
      cmState.revenue=(r.revenue_by_status&&r.revenue_by_status.paid)||0;
      return r;
    });
  }

  window.loadCmInvoices=function(){
    fetchCmInvoices().then(renderCmInvoices).catch(function(e){
      toast('Ошибка: '+e.message,'error');
    });
  };

  function renderCmConfig(){
    var cfg=cmState.cfg,e=cmState.edit;
    if(!cfg||!e)return;
    // Stats
    var stats=document.getElementById('cm-stats');
    if(stats){
      var gwStatus=e.cryptocloud.enable?(cfg.cryptocloud.api_key_configured&&cfg.cryptocloud.webhook_secret_configured?'Включён':'Не настроен'):'Выключен';
      stats.innerHTML=
        '<div style="background:var(--surface2);border-radius:8px;padding:12px 16px;flex:1;min-width:140px"><div style="font-size:11px;color:var(--text-muted)">Доход (paid)</div><div style="font-size:20px;font-weight:600;color:#00c864">$'+cmState.revenue.toFixed(2)+'</div></div>'+
        '<div style="background:var(--surface2);border-radius:8px;padding:12px 16px;flex:1;min-width:140px"><div style="font-size:11px;color:var(--text-muted)">Счетов</div><div style="font-size:20px;font-weight:600">'+cmState.total+'</div></div>'+
        '<div style="background:var(--surface2);border-radius:8px;padding:12px 16px;flex:1;min-width:140px"><div style="font-size:11px;color:var(--text-muted)">Тарифов</div><div style="font-size:20px;font-weight:600">'+(e.tariffs||[]).length+'</div></div>'+
        '<div style="background:var(--surface2);border-radius:8px;padding:12px 16px;flex:1;min-width:140px"><div style="font-size:11px;color:var(--text-muted)">Гейтвей</div><div style="font-size:16px;font-weight:600">'+gwStatus+'</div></div>';
    }
    // CryptoCloud creds
    document.getElementById('cm-cc-enable').checked=!!e.cryptocloud.enable;
    document.getElementById('cm-cc-shop').value=e.cryptocloud.shop_id||'';
    document.getElementById('cm-cc-currency').value=e.cryptocloud.currency||'';
    // Always-empty inputs (we never echo back secrets) + an explicit
    // "saved/not saved" badge next to the label so operators don't have to
    // guess whether they need to type a value.
    var apiKeyEl=document.getElementById('cm-cc-apikey');
    var whEl=document.getElementById('cm-cc-webhook');
    apiKeyEl.value='';
    apiKeyEl.placeholder=cfg.cryptocloud.api_key_configured?'Оставьте пустым, чтобы сохранить текущий':'Token из кабинета CryptoCloud';
    whEl.value='';
    whEl.placeholder=cfg.cryptocloud.webhook_secret_configured?'Оставьте пустым, чтобы сохранить текущий':'Секрет из настроек проекта';
    document.getElementById('cm-cc-apikey-status').innerHTML=cfg.cryptocloud.api_key_configured
      ?'<span style="color:#00c864;font-size:11px">● сохранён</span>'
      :'<span style="color:#fbbf24;font-size:11px">● не задан</span>';
    document.getElementById('cm-cc-webhook-status').innerHTML=cfg.cryptocloud.webhook_secret_configured
      ?'<span style="color:#00c864;font-size:11px">● сохранён</span>'
      :'<span style="color:#fbbf24;font-size:11px">● не задан</span>';
    document.getElementById('cm-cc-apikey-hint').textContent=cfg.cryptocloud.api_key_configured
      ?'Введите новый ключ только если хотите изменить.':'';
    document.getElementById('cm-cc-webhook-hint').textContent=cfg.cryptocloud.webhook_secret_configured
      ?'Введите новый секрет только если хотите изменить.':'';
    document.getElementById('cm-webhook-url').textContent=window.location.origin+'/api/cryptocloud/webhook';
    // Stars block (graceful when older binary didn't ship stars block)
    if(!cmState.edit.stars){
      cmState.edit.stars={enable:false,star_rate_usd:0.013,withdrawal_fee_percent:30};
    }
    var starsEnable=document.getElementById('cm-stars-enable');
    var starsRate=document.getElementById('cm-stars-rate');
    var starsFee=document.getElementById('cm-stars-fee');
    if(starsEnable)starsEnable.checked=!!cmState.edit.stars.enable;
    if(starsRate)starsRate.value=cmState.edit.stars.star_rate_usd||0.013;
    if(starsFee)starsFee.value=cmState.edit.stars.withdrawal_fee_percent||0;
    // Live-update tariff net hints when stars rate/fee changes.
    if(starsRate)starsRate.oninput=function(){cmState.edit.stars.star_rate_usd=parseFloat(this.value)||0;renderCmTariffs();};
    if(starsFee)starsFee.oninput=function(){cmState.edit.stars.withdrawal_fee_percent=parseFloat(this.value)||0;renderCmTariffs();};
    // StreamPay block (graceful when older binary didn't ship streampay block).
    // We bill in USD (payment_type=2 / system_currency=USDT) — customer
    // picks fiat on StreamPay's checkout page.
    if(!cmState.edit.streampay){
      cmState.edit.streampay={enable:false,store_id:0,private_key_hex:'',public_key_hex:'',system_currency:'USDT',payment_type:2};
    }
    var spCfg=cfg.streampay||{};
    var spEdit=cmState.edit.streampay;
    var spEnable=document.getElementById('cm-sp-enable');
    var spStore=document.getElementById('cm-sp-store');
    var spSysCcy=document.getElementById('cm-sp-sysccy');
    var spPriv=document.getElementById('cm-sp-priv');
    var spPub=document.getElementById('cm-sp-pub');
    if(spEnable)spEnable.checked=!!spEdit.enable;
    if(spStore)spStore.value=spEdit.store_id||0;
    if(spSysCcy)spSysCcy.value=spEdit.system_currency||'USDT';
    if(spPub)spPub.value=spEdit.public_key_hex||'';
    // Private key field: always cleared on render (we never echo back
    // secrets); status pill + placeholder tell operator whether the
    // server already has one stored.
    if(spPriv){
      spPriv.value='';
      spPriv.placeholder=spCfg.private_key_configured
        ?'Оставьте пустым, чтобы сохранить текущий'
        :'Ваш ed25519 private key (128 hex)';
    }
    var spPrivStatus=document.getElementById('cm-sp-priv-status');
    if(spPrivStatus)spPrivStatus.innerHTML=spCfg.private_key_configured
      ?'<span style="color:#00c864;font-size:11px">● сохранён</span>'
      :'<span style="color:#fbbf24;font-size:11px">● не задан</span>';
    var spHint=document.getElementById('cm-sp-priv-hint');
    if(spHint)spHint.textContent=spCfg.private_key_configured
      ?'Введите новый ключ только если хотите изменить.':'';
    // Surface derived public key so operator can copy → StreamPay cabinet
    if(spCfg.derived_public_key){
      document.getElementById('cm-sp-derived-wrap').style.display='block';
      document.getElementById('cm-sp-derived').textContent=spCfg.derived_public_key;
    }else{
      document.getElementById('cm-sp-derived-wrap').style.display='none';
    }
    document.getElementById('cm-sp-webhook-url').textContent=window.location.origin+'/api/streampay/webhook';
    document.getElementById('cm-sp-redir-ok').textContent=window.location.origin+'/payment/streampay/success';
    document.getElementById('cm-sp-redir-fail').textContent=window.location.origin+'/payment/streampay/failure';
    document.getElementById('cm-sp-redir-cancel').textContent=window.location.origin+'/payment/streampay/cancel';
    // Wire StreamPay inputs to edit state.
    if(spStore)spStore.oninput=function(){spEdit.store_id=parseInt(this.value,10)||0;};
    if(spSysCcy)spSysCcy.oninput=function(){spEdit.system_currency=this.value;};
    if(spPub)spPub.oninput=function(){spEdit.public_key_hex=this.value;};
    if(spEnable)spEnable.onchange=function(){spEdit.enable=this.checked;};
    // Tariffs
    renderCmTariffs();
    // Groups
    var sel=document.getElementById('cm-group');
    var opts='<option value="">— не менять группу —</option>';
    for(var i=0;i<cmState.groups.length;i++){
      var g=cmState.groups[i];
      opts+='<option value="'+esc(g.id)+'"'+(g.id===e.premium_group_id?' selected':'')+'>'+esc(g.name)+' ('+esc(g.id)+')</option>';
    }
    sel.innerHTML=opts;
    sel.value=e.premium_group_id||'';
    // Manual-activate dialog group dropdown gets the same options
    var actSel=document.getElementById('cm-act-group');
    if(actSel)actSel.innerHTML='<option value="">— по умолчанию —</option>'+opts.replace('<option value="">— не менять группу —</option>','');
    // Poller toggle: server resolves *bool → bool with default=true.
    // Missing field (very old server) → treat as on.
    var pollerEl=document.getElementById('cm-poller-enable');
    var pollerLabel=document.getElementById('cm-poller-label');
    if(pollerEl){
      var pollerOn=(typeof e.poller_enabled==='boolean')?e.poller_enabled:true;
      pollerEl.checked=pollerOn;
      if(pollerLabel)pollerLabel.innerHTML=pollerOn?'✅ Поллер включён':'⏸ Поллер выключен';
      pollerEl.onchange=function(){
        e.poller_enabled=this.checked;
        if(pollerLabel)pollerLabel.innerHTML=this.checked?'✅ Поллер включён':'⏸ Поллер выключен';
      };
    }
  }

  // Live mirror of backend's DynamicGatewayStars.StarsNetUSD — used by
  // the per-row "≈ $X net" hint under the Stars input so the operator
  // sees the real-money preview without saving + reloading.
  function cmStarsNet(stars){
    if(!cmState.edit||!cmState.edit.stars)return 0;
    var rate=parseFloat(cmState.edit.stars.star_rate_usd)||0;
    var fee=parseFloat(cmState.edit.stars.withdrawal_fee_percent)||0;
    if(fee<0)fee=0;if(fee>100)fee=100;
    if(rate<=0||stars<=0)return 0;
    return stars*rate*(1-fee/100);
  }
  // Inverse: given a USD price already typed for a tariff, suggest a stars
  // amount that nets ≥ that USD after the fee. Rounds UP — better to
  // overcharge one star than miss the payout target.
  window.autoFillCmStars=function(idx){
    var t=cmState.edit.tariffs[idx];
    var rate=parseFloat(cmState.edit.stars.star_rate_usd)||0;
    var fee=parseFloat(cmState.edit.stars.withdrawal_fee_percent)||0;
    if(!t.amount_usd||t.amount_usd<=0||rate<=0||fee>=100){
      toast('Нужны USD-курс, комиссия < 100% и заполненная USD-цена тарифа.','error');return;
    }
    var gross=t.amount_usd/rate/(1-fee/100);
    cmState.edit.tariffs[idx].amount_stars=Math.ceil(gross);
    renderCmTariffs();
  };

  function renderCmTariffs(){
    var tbl=document.getElementById('cm-tariffs');
    if(!tbl)return;
    var tariffs=cmState.edit.tariffs||[];
    // Crypto and StreamPay share the same AmountUSD price column — no
    // per-currency StreamPay column. StreamPay's checkout page lets the
    // customer pick fiat themselves and converts on their side.
    var h='<tr style="border-bottom:1px solid rgba(255,255,255,0.1);color:var(--text-dim);font-size:12px">'+
      '<th style="padding:6px;text-align:left">Дней</th>'+
      '<th style="padding:6px;text-align:left">Цена ($)</th>'+
      '<th style="padding:6px;text-align:left">Stars ⭐</th>'+
      '<th style="padding:6px;text-align:left">Подпись</th>'+
      '<th style="padding:6px"></th>'+
      '</tr>';
    if(!tariffs.length){h+='<tr><td colspan="5" style="padding:12px;text-align:center;color:var(--text-dim)">Нет тарифов. Добавьте хотя бы один.</td></tr>';}
    for(var i=0;i<tariffs.length;i++){
      var t=tariffs[i];
      var stars=t.amount_stars||0;
      var net=stars>0?cmStarsNet(stars):0;
      var netHint=net>0?'<div style="font-size:10px;color:var(--text-dim);margin-top:2px">нетто ≈ $'+net.toFixed(2)+'</div>':'';
      h+='<tr style="border-bottom:1px solid rgba(255,255,255,0.04)">'+
        '<td style="padding:6px"><input type="number" value="'+(t.days||0)+'" data-idx="'+i+'" data-fld="days" class="cm-tariff-fld input-sm" style="width:80px;padding:4px 6px;font-size:13px"></td>'+
        '<td style="padding:6px"><input type="number" step="0.01" value="'+(t.amount_usd||0)+'" data-idx="'+i+'" data-fld="amount_usd" class="cm-tariff-fld input-sm" style="width:90px;padding:4px 6px;font-size:13px"></td>'+
        '<td style="padding:6px">'+
          '<div style="display:flex;align-items:center;gap:4px"><input type="number" value="'+stars+'" data-idx="'+i+'" data-fld="amount_stars" class="cm-tariff-fld input-sm" style="width:80px;padding:4px 6px;font-size:13px">'+
          '<button class="btn btn-sm" title="Рассчитать звёзды из USD-цены с учётом комиссии" onclick="autoFillCmStars('+i+')">≈</button></div>'+netHint+
        '</td>'+
        '<td style="padding:6px"><input type="text" value="'+esc(t.label||'')+'" data-idx="'+i+'" data-fld="label" class="cm-tariff-fld input-sm" style="width:200px;padding:4px 6px;font-size:13px" placeholder="напр. «1 месяц»"></td>'+
        '<td style="padding:6px;text-align:right"><button class="btn btn-sm" style="background:#dc2626" onclick="removeCmTariff('+i+')">&times;</button></td>'+
        '</tr>';
    }
    tbl.innerHTML=h;
    // Wire inputs
    var fields=tbl.querySelectorAll('.cm-tariff-fld');
    for(var j=0;j<fields.length;j++){
      fields[j].addEventListener('input',function(ev){
        var idx=parseInt(ev.target.getAttribute('data-idx'),10);
        var fld=ev.target.getAttribute('data-fld');
        var v=ev.target.value;
        if(fld==='days')cmState.edit.tariffs[idx].days=parseInt(v,10)||0;
        else if(fld==='amount_usd')cmState.edit.tariffs[idx].amount_usd=parseFloat(v)||0;
        else if(fld==='amount_stars'){cmState.edit.tariffs[idx].amount_stars=parseInt(v,10)||0;renderCmTariffs();}
        else cmState.edit.tariffs[idx].label=v;
      });
    }
  }

  window.addCmTariff=function(){
    if(!cmState.edit)return;
    cmState.edit.tariffs=(cmState.edit.tariffs||[]).concat([{days:30,amount_usd:3,label:''}]);
    renderCmTariffs();
  };
  window.removeCmTariff=function(idx){
    cmState.edit.tariffs.splice(idx,1);
    renderCmTariffs();
  };

  window.toggleCmReveal=function(id){
    var el=document.getElementById(id);
    if(!el)return;
    el.type=el.type==='password'?'text':'password';
  };

  function renderCmInvoices(){
    var el=document.getElementById('cm-invoices');
    if(!el)return;
    if(!cmState.invoices.length){
      el.innerHTML='<div style="padding:16px;text-align:center;color:var(--text-dim);font-size:13px">Счетов пока нет</div>';
      return;
    }
    var h='<table style="width:100%;border-collapse:collapse;font-size:13px">';
    h+='<tr style="border-bottom:1px solid rgba(255,255,255,0.1);color:var(--text-dim);font-size:11px"><th style="padding:6px;text-align:left">ID</th><th style="padding:6px;text-align:left">TG</th><th style="padding:6px;text-align:right">Дней</th><th style="padding:6px;text-align:right">$</th><th style="padding:6px">Валюта</th><th style="padding:6px">Статус</th><th style="padding:6px">Создан</th><th style="padding:6px"></th></tr>';
    for(var i=0;i<cmState.invoices.length;i++){
      var inv=cmState.invoices[i];
      var statusColor=inv.status==='paid'?'#00c864':(inv.status==='created'?'#fbbf24':'#94a3b8');
      h+='<tr style="border-bottom:1px solid rgba(255,255,255,0.04)">'+
        '<td style="padding:6px;font-family:Courier New,monospace;font-size:11px;cursor:pointer" onclick="copyToClip(\''+esc(inv.uuid)+'\')" title="Кликни чтобы скопировать UUID">'+esc(inv.short_id||inv.uuid)+'</td>'+
        '<td style="padding:6px">'+inv.tg_id+(inv.tg_username?'<br><span style="font-size:11px;color:var(--text-muted)">@'+esc(inv.tg_username)+'</span>':'')+'</td>'+
        '<td style="padding:6px;text-align:right">'+inv.days+'</td>'+
        '<td style="padding:6px;text-align:right">$'+inv.amount_usd+'</td>'+
        '<td style="padding:6px;text-align:center">'+(inv.currency||'—')+'</td>'+
        '<td style="padding:6px;text-align:center;color:'+statusColor+'">'+inv.status+'</td>'+
        '<td style="padding:6px;font-size:11px;color:var(--text-muted)">'+esc(inv.created_at)+'</td>'+
        '<td style="padding:6px;text-align:right">'+
          (inv.pay_link?'<a href="'+esc(inv.pay_link)+'" target="_blank" style="font-size:11px;color:#4fc3f7">link</a> ':'')+
          '<button class="btn btn-sm" onclick="refreshCmInvoice(\''+esc(inv.uuid)+'\',\''+esc(inv.gateway||'')+'\')" title="Перезапросить статус у '+(inv.gateway==='streampay'?'StreamPay':'CryptoCloud')+'">&#x21BB;</button>'+
        '</td>'+
        '</tr>';
    }
    h+='</table>';
    el.innerHTML=h;
  }

  // refreshCmInvoice — gateway-aware: StreamPay invoices go through their
  // own GetInvoice endpoint (signed by ed25519); CryptoCloud through the
  // legacy /payments/refresh. Surfaces the server-side hint when status
  // isn't terminal-positive (e.g. "paid" = awaiting trader confirmation).
  window.refreshCmInvoice=function(uuid,gateway){
    var endpoint=gateway==='streampay'?'/api/payments/streampay-refresh':'/api/payments/refresh';
    post(endpoint,{uuid:uuid}).then(function(r){
      if(r&&r.error){toast(r.error,'error');return;}
      if(r&&r.ok===false){toast(r.error||'Неизвестная ошибка','error');return;}
      var msg='Статус: '+r.status+(r.already_paid?' (уже обработано)':'');
      if(r.hint)msg+=' — '+r.hint;
      toast(msg);
      loadCmInvoices();
    }).catch(function(e){toast('Ошибка: '+e.message,'error');});
  };

  window.saveCmConfig=function(){
    if(!cmState.cfg)return;
    var newApiKey=document.getElementById('cm-cc-apikey').value.trim();
    var newWebhook=document.getElementById('cm-cc-webhook').value.trim();
    var enable=document.getElementById('cm-cc-enable').checked;
    var shop=document.getElementById('cm-cc-shop').value.trim();

    // StreamPay-side inputs read directly from DOM (more reliable than
    // mirrored state, especially for the password-like private key
    // where paste-and-save can race the @input handler).
    // Whitespace is stripped aggressively because pasted ed25519 keys
    // often pull trailing newlines from terminal copies.
    var spEnable=document.getElementById('cm-sp-enable').checked;
    var spStore=parseInt(document.getElementById('cm-sp-store').value,10)||0;
    var spPriv=document.getElementById('cm-sp-priv').value.replace(/\s+/g,'');
    var spPub=document.getElementById('cm-sp-pub').value.replace(/\s+/g,'');
    var spSysCcy=document.getElementById('cm-sp-sysccy').value.trim()||'USDT';
    var spCfg=cmState.cfg.streampay||{};

    // Client-side validation: catch obvious "enable without creds"
    // before round-tripping to the server. Server still validates.
    if(enable){
      if(!newApiKey&&!cmState.cfg.cryptocloud.api_key_configured){
        toast('Заполните API Key — без него CryptoCloud не примет платежи.','error');return;
      }
      if(!newWebhook&&!cmState.cfg.cryptocloud.webhook_secret_configured){
        toast('Заполните Webhook Secret — без него мы не сможем верифицировать оплаты.','error');return;
      }
      if(!shop){toast('Заполните Shop ID.','error');return;}
    }
    if(spEnable){
      if(spStore<=0){toast('StreamPay: укажите Store ID (число > 0).','error');return;}
      if(!spPriv&&!spCfg.private_key_configured){
        toast('StreamPay: заполните Private Key (ed25519, 128 hex-символов).','error');return;
      }
      if(spPriv&&(spPriv.length!==128||!/^[0-9a-fA-F]+$/.test(spPriv))){
        toast('StreamPay: Private Key должен быть ровно 128 hex-символов.','error');return;
      }
      if(!spPub||spPub.length!==64||!/^[0-9a-fA-F]+$/.test(spPub)){
        toast('StreamPay: Public Key должен быть ровно 64 hex-символа (сервер StreamPay).','error');return;
      }
    }
    var btn=document.getElementById('cm-save-btn');
    if(btn){btn.disabled=true;btn.textContent='Сохранение...';}
    var body={
      premium_group_id:document.getElementById('cm-group').value,
      tariffs:cmState.edit.tariffs.map(function(t){return{
        days:t.days|0,
        amount_usd:parseFloat(t.amount_usd)||0,
        amount_stars:parseInt(t.amount_stars,10)||0,
        label:(t.label||'').trim()
      };}),
      cryptocloud:{
        enable:enable,
        shop_id:shop,
        currency:document.getElementById('cm-cc-currency').value.trim(),
        api_key:newApiKey,
        api_key_keep:!newApiKey&&cmState.cfg.cryptocloud.api_key_configured,
        webhook_secret:newWebhook,
        webhook_secret_keep:!newWebhook&&cmState.cfg.cryptocloud.webhook_secret_configured
      },
      stars:{
        enable:document.getElementById('cm-stars-enable').checked,
        star_rate_usd:parseFloat(document.getElementById('cm-stars-rate').value)||0,
        withdrawal_fee_percent:parseFloat(document.getElementById('cm-stars-fee').value)||0
      },
      streampay:{
        enable:spEnable,
        store_id:spStore,
        private_key_hex:spPriv,
        private_key_keep:!spPriv&&!!spCfg.private_key_configured,
        public_key_hex:spPub,
        system_currency:spSysCcy||'USDT',
        // payment_type=2 → invoice in system currency (USDT ≈ USD).
        // Customer picks fiat at StreamPay's checkout.
        payment_type:2
      },
      // Background poller toggle — UI checkbox (cm-poller-enable). When
      // off, the server stops auto-refreshing pending invoices; admin
      // must use the per-row "🔄 Обновить" button.
      poller_enabled:document.getElementById('cm-poller-enable')
        ?document.getElementById('cm-poller-enable').checked
        :true
    };
    post('/api/payments/config',body).then(function(r){
      if(r&&r.error){toast(r.error,'error');return;}
      toast('Сохранено');
      loadCommerce();
    }).catch(function(e){toast('Ошибка: '+e.message,'error');})
    .finally(function(){if(btn){btn.disabled=false;btn.innerHTML='&#x1F4BE; Сохранить настройки';}});
  };

  window.testCmConnection=function(){
    // Probes CryptoCloud with a $1 throwaway invoice using saved creds.
    // Verbatim error from the API is shown — the operator can usually
    // fix it without grepping server logs.
    var btn=document.getElementById('cm-test-btn');
    if(btn){btn.disabled=true;btn.textContent='Проверка...';}
    post('/api/payments/test',{}).then(function(r){
      if(r.ok){
        toast('✅ Соединение работает!');
        // Surface the test invoice link so the operator can verify the
        // full flow end-to-end (clicking it opens CryptoCloud pay page).
        if(r.pay_link){
          var hint=document.getElementById('cm-cc-webhook-hint');
          if(hint)hint.innerHTML='Тестовый счёт: <a href="'+esc(r.pay_link)+'" target="_blank" style="color:#4fc3f7">'+esc(r.pay_link)+'</a>';
        }
      }else{
        toast('❌ '+(r.error||'Неизвестная ошибка'),'error');
      }
    }).catch(function(e){toast('❌ '+e.message,'error');})
    .finally(function(){if(btn){btn.disabled=false;btn.innerHTML='&#x1F50C; Тест CryptoCloud';}});
  };

  window.testCmStreamPay=function(){
    // Lighter than CreateInvoice probe — GetWallet has no side effects
    // (no orphan invoice in the dashboard) and validates BOTH keys at
    // once: wrong private_key → HTTP 403, wrong store_id → 406.
    var btn=document.getElementById('cm-test-sp-btn');
    if(btn){btn.disabled=true;btn.textContent='Проверка...';}
    post('/api/payments/test-streampay',{}).then(function(r){
      if(r.ok){
        var summary=(r.wallets||[]).map(function(w){return w.balance+' '+w.currency;}).join(', ');
        toast('✅ StreamPay OK'+(summary?' (баланс: '+summary+')':''));
      }else{
        toast('❌ '+(r.error||'Неизвестная ошибка'),'error');
      }
    }).catch(function(e){toast('❌ '+e.message,'error');})
    .finally(function(){if(btn){btn.disabled=false;btn.innerHTML='&#x1F50C; Тест StreamPay';}});
  };

  window.showCmActivate=function(){
    document.getElementById('cm-act-tgid').value='';
    document.getElementById('cm-act-days').value='30';
    document.getElementById('cm-act-reason').value='';
    var sel=document.getElementById('cm-act-group');
    if(sel)sel.value='';
    document.getElementById('cm-act-overlay').style.display='block';
    document.getElementById('cm-act-modal').style.display='block';
  };
  window.hideCmActivate=function(){
    document.getElementById('cm-act-overlay').style.display='none';
    document.getElementById('cm-act-modal').style.display='none';
  };
  window.doCmActivate=function(){
    var tgid=parseInt(document.getElementById('cm-act-tgid').value,10);
    var days=parseInt(document.getElementById('cm-act-days').value,10);
    if(!tgid||!days){toast('Укажите TG ID и количество дней','error');return;}
    post('/api/payments/activate',{
      tg_id:tgid,
      days:days,
      group_id:document.getElementById('cm-act-group').value||'',
      reason:document.getElementById('cm-act-reason').value||''
    }).then(function(r){
      if(r&&r.error){toast(r.error,'error');return;}
      toast('TG '+tgid+' +'+days+'д');
      hideCmActivate();
      loadCmInvoices();
    }).catch(function(e){toast('Ошибка: '+e.message,'error');});
  };
})();
