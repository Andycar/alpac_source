package httpapi

import (
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/config"
)

// publicServerInfoHandler serves /api/servers/info — a single JSON describing
// THIS lampac-go instance (name, region, version, healthy nodes count, etc).
// Used by the Lampa widget plugin to show the user which server they're on.
//
// Safe to call without auth — exposes only non-sensitive fields.
func publicServerInfoHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		out := map[string]any{
			"name":      "lampac-go",
			"region":    "",
			"version":   "",
			"server_ts": time.Now().UTC().Unix(),
		}
		if serverReady() {
			cfg := liveConfig(config.Config{})
			out["version"] = liveVersion()
			out["mode"] = cfg.Cluster.Mode
			out["cluster"] = cfg.Cluster.Enable
			if cp := liveClusterPool(); cp != nil {
				st := cp.CurrentSettings()
				if st.AdvertiseName != "" {
					out["name"] = st.AdvertiseName
				}
				if st.AdvertiseRegion != "" {
					out["region"] = st.AdvertiseRegion
				}
				out["strategy"] = cp.Strategy()
				healthy, total := 0, 0
				for _, n := range cp.Snapshot() {
					total++
					if n.Healthy && n.Enabled {
						healthy++
					}
				}
				out["nodes_total"] = total
				out["nodes_healthy"] = healthy
				out["local_active_conns"] = cp.LocalActiveConns()
			}
			if name, _ := out["name"].(string); name == "lampac-go" {
				// Try to derive a name from the Host header if no explicit advertise_name.
				if h := strings.TrimSpace(r.Host); h != "" {
					out["name"] = stripPort(h)
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// publicServerListHandler serves /api/servers/list — list of cluster nodes
// suitable for showing in the user widget. Requires Settings.ExposePublicList.
// Hosts are included only when Settings.PublicHosts is true, otherwise just
// name + region + healthy + avg latency are returned.
func publicServerListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		out := map[string]any{"servers": []any{}}
		cp := liveClusterPool()
		if cp == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		st := cp.CurrentSettings()
		if !st.ExposePublicList {
			writeJSON(w, http.StatusOK, out)
			return
		}
		// Include "this server" entry first. Primary is always considered 100%
		// available from its own perspective (it serves the response).
		self := map[string]any{
			"self":       true,
			"name":       firstNonEmpty(st.AdvertiseName, stripPort(r.Host), "lampac-go"),
			"region":     st.AdvertiseRegion,
			"healthy":    true,
			"uptime_pct": 100.0,
		}
		if st.PublicHosts {
			self["host"] = guessSelfHost(r)
		}
		list := []any{self}
		for _, n := range cp.Snapshot() {
			if !n.Enabled {
				continue
			}
			entry := map[string]any{
				"self":           false,
				"name":           n.Name,
				"region":         n.Region,
				"healthy":        n.Healthy,
				"avg_latency_ms": n.AvgLatencyMs,
				"uptime_pct":     n.UptimePct,
			}
			if st.PublicHosts {
				entry["host"] = n.Host
			}
			list = append(list, entry)
		}
		out["servers"] = list
		out["strategy"] = cp.Strategy()
		writeJSON(w, http.StatusOK, out)
	}
}

// xLampacServerMiddleware annotates outgoing responses with X-Lampac-Server
// and X-Lampac-Server-Region headers so clients always know which server
// handled their request (even when no cluster forwarding happened).
//
// Only adds the headers if not already present (the forwarder sets them for
// proxied requests; this middleware covers the local-handled case).
func xLampacServerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't touch the /proxy/ stream path — adding headers after WriteHeader
		// (which streamed responses may have already done) is a no-op and we
		// don't want any chance of breaking range responses.
		if !strings.HasPrefix(r.URL.Path, "/proxy/") {
			if h := w.Header(); h.Get("X-Lampac-Server") == "" {
				name, region := selfAdvertise(r)
				h.Set("X-Lampac-Server", name)
				if region != "" {
					h.Set("X-Lampac-Server-Region", region)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func selfAdvertise(r *http.Request) (name, region string) {
	if cp := liveClusterPool(); cp != nil {
		st := cp.CurrentSettings()
		if st.AdvertiseName != "" {
			return st.AdvertiseName, st.AdvertiseRegion
		}
	}
	if h := strings.TrimSpace(r.Host); h != "" {
		return stripPort(h), ""
	}
	return "lampac-go", ""
}

func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}
	return host
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// guessSelfHost returns the most accurate URL we can reconstruct for the
// current server (used in the public list when PublicHosts is on).
func guessSelfHost(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + strings.TrimSpace(r.Host)
}

// publicServersPageHandler serves /servers — a tiny HTML dashboard showing
// the same data as /api/servers/list, suitable for plain browser viewing.
func publicServersPageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(publicServersPageHTML))
	}
}

const publicServersPageHTML = `<!doctype html>
<html lang="ru"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Lampac Cluster — серверы</title>
<style>
:root{--bg:#0f1419;--card:#1a1f24;--border:#2a3036;--text:#e5e7eb;--muted:#9ca3af;--ok:#22c55e;--bad:#ef4444;--accent:#3b82f6;}
*{box-sizing:border-box;font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif}
body{margin:0;background:var(--bg);color:var(--text);min-height:100vh}
.wrap{max-width:960px;margin:0 auto;padding:24px}
h1{font-size:22px;margin:0 0 4px;font-weight:600}
.sub{color:var(--muted);font-size:13px;margin-bottom:18px}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:12px}
.card{background:var(--card);border:1px solid var(--border);border-radius:10px;padding:14px}
.card h3{margin:0 0 6px;font-size:15px;display:flex;align-items:center;gap:8px}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block;background:var(--muted);flex-shrink:0}
.dot.ok{background:var(--ok)} .dot.bad{background:var(--bad)}
.meta{font-size:12px;color:var(--muted);font-family:ui-monospace,Menlo,monospace;line-height:1.5}
.meta b{color:var(--text);font-weight:500}
.tag{display:inline-block;padding:2px 8px;background:var(--border);border-radius:99px;font-size:11px;margin-right:4px}
.tag.self{background:var(--accent);color:#fff}
.refresh{float:right;background:transparent;border:1px solid var(--border);color:var(--text);padding:4px 10px;border-radius:6px;cursor:pointer;font-size:12px}
.refresh:hover{border-color:var(--accent)}
.foot{text-align:center;color:var(--muted);font-size:11px;margin-top:24px}
</style>
</head><body>
<div class="wrap">
  <h1>Lampac Cluster</h1>
  <div class="sub" id="sub">Загрузка…</div>
  <div class="grid" id="grid"></div>
  <div class="foot">Auto-refresh каждые 10 секунд · <button class="refresh" onclick="load()">Обновить</button></div>
</div>
<script>
function fmt(n){return (n==null||isNaN(n))?'—':Math.round(n);}
function pingClient(host){var t=Date.now();return fetch(host+'/api/servers/info',{cache:'no-store'}).then(function(r){return r.ok?Date.now()-t:null;}).catch(function(){return null;});}
var clientPings={};
function render(d){
  var s=d.servers||[];
  document.getElementById('sub').textContent='Стратегия: '+(d.strategy||'—')+' · Серверов: '+s.length;
  var html='';
  s.forEach(function(srv){
    var dot=srv.healthy?'ok':'bad';
    var tags=[];
    if(srv.self)tags.push('<span class="tag self">этот сервер</span>');
    if(srv.region)tags.push('<span class="tag">'+srv.region+'</span>');
    var cp=clientPings[srv.host||srv.name];
    html+='<div class="card"><h3><span class="dot '+dot+'"></span>'+(srv.name||'—')+'</h3>'+
      '<div>'+tags.join('')+'</div>'+
      '<div class="meta" style="margin-top:8px">'+
        (srv.host?'<b>host:</b> '+srv.host+'<br>':'')+
        (srv.avg_latency_ms!=null?'<b>latency (с сервера):</b> ~'+fmt(srv.avg_latency_ms)+'ms<br>':'')+
        (srv.active_conns!=null?'<b>live conns:</b> '+fmt(srv.active_conns)+'<br>':'')+
        (cp!=null?'<b>ваш пинг:</b> '+fmt(cp)+'ms':'')+
      '</div></div>';
  });
  document.getElementById('grid').innerHTML=html||'<div class="card">Список серверов пуст.</div>';
}
function load(){
  fetch('/api/servers/list',{cache:'no-store'}).then(function(r){return r.json();}).then(function(d){
    render(d);
    (d.servers||[]).forEach(function(srv){
      if(!srv.host)return;
      pingClient(srv.host).then(function(ms){
        if(ms==null)return;
        clientPings[srv.host]=ms;
        render(d);
      });
    });
  });
}
load();setInterval(load,10000);
</script>
</body></html>`
