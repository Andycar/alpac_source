package httpapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/modules"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	qrcode "github.com/skip2/go-qrcode"
)

const syncInvcFallback = `var sync_invc = window.sync_invc || {};
if (!Array.isArray(sync_invc.import_keys)) sync_invc.import_keys = [''];
if (typeof sync_invc.goExport !== 'function') sync_invc.goExport = function(path, value) { return value || {}; };
if (typeof sync_invc['import\u0421ompleted'] !== 'function') sync_invc['import\u0421ompleted'] = function(path) {};
`

func liteJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		src, err := loadPluginTemplate("lite.js", cfg)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		host := hostFromRequest(r)
		out := strings.ReplaceAll(src, "{localhost}", host+"/lite")
		out = strings.ReplaceAll(out, "{token}", url.QueryEscape(token))
		out = applyPublicBrandingJS(out)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

func genericPluginJSHandler(templateFile, endpointPath string, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		src, err := loadPluginTemplate(templateFile, cfg)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		host := hostFromRequest(r)
		out := strings.ReplaceAll(src, "{localhost}", host+endpointPath)
		out = strings.ReplaceAll(out, "{token}", url.QueryEscape(token))
		out = applyPublicBrandingJS(out)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		// Plugin bodies embed per-user values like {token} and adapt to the
		// host's branding/auth flow — every byte is request-scoped. WebView
		// HTTP cache holding a stale variant after an auth or branding
		// update is a recurring class of "plugin half-broken" tickets, so
		// we always serve no-store. Lampa.Plugins.put still caches code
		// client-side once it's loaded.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

// externalPlayerJS is the client-side Lampa plugin that:
//  1. Adds "Внешний плеер" settings section with OS-specific player presets.
//  2. Adds "Запустить внешний плеер" to the TorrServer file action menu.
//  3. For Infuse: adds "Сохранить серию в Infuse" using the save?url=...&index=N API.
//  4. Normalizes stream filenames using TMDB metadata (Title.S01E01.{tmdb-id}.ext)
//     so external players show clean library entries.
//
// Filename normalization mirrors the firecore.com/api convention:
//   - Title priority: original_title → original_name → title → name → "Media"
//   - Non-alphanumeric chars collapse to a single dot, edge dots trimmed.
//   - TV: ${title}.S${season}E${episode}.{tmdb-${id}}.${ext}
//   - Movie: ${title}.{tmdb-${id}}.${ext}
//
// The server-side default (from config.toml [torrserver] external_player_protocol)
// is injected at serve time for backward compatibility.
const externalPlayerJS = `(function(){
'use strict';
var serverDefault={serverDefault};
var PRESETS={
iina:'iina://weblink?url=${url}',
vlc_mac:'vlc-x-callback://x-callback-url/stream?url=${url}',
mpv:'mpv://${furl}',
infuse:'infuse://x-callback-url/play?url=${url}',
vlc:'vlc://${furl}',
nplayer:'nplayer-${furl}',
potplayer:'potplayer://${furl}',
custom:''
};
var ICON='<svg width="36" height="36" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M15 12L9 16.5V7.5L15 12Z" fill="white"/><path d="M4 4h16v16H4V4z" stroke="white" stroke-width="2" fill="none"/><path d="M20 2l2 2M20 22l2-2M4 2L2 4M4 22l-2-2" stroke="white" stroke-width="1.5"/></svg>';
function detectPresets(){
var P=window.Lampa&&Lampa.Platform;
var o={off:'\u0412\u044b\u043a\u043b\u044e\u0447\u0438\u0442\u044c'};
if(P&&P.macOS&&P.macOS()){
o.iina='IINA';o.vlc_mac='VLC';o.mpv='mpv';o.infuse='Infuse';
}else if(P&&P.is&&(P.is('apple')||P.is('apple_tv'))){
o.vlc='VLC';o.infuse='Infuse';o.nplayer='nPlayer';
}else if(P&&P.is&&P.is('android')){
o.vlc='VLC';
}else{
o.vlc='VLC';o.potplayer='PotPlayer';
}
o.custom='\u0421\u0432\u043e\u0439 \u0448\u0430\u0431\u043b\u043e\u043d';
return o;
}
function addSettings(){
var presets=detectPresets();
Lampa.SettingsApi.addComponent({component:'ext_player',name:'\u0412\u043d\u0435\u0448\u043d\u0438\u0439 \u043f\u043b\u0435\u0435\u0440',icon:ICON});
if(serverDefault&&!Lampa.Storage.get('ext_player_inited','')){
var key='custom';
for(var k in PRESETS){if(PRESETS[k]===serverDefault){key=k;break;}}
Lampa.Storage.set('ext_player',key);
if(key==='custom')Lampa.Storage.set('ext_player_custom',serverDefault);
Lampa.Storage.set('ext_player_inited','1');
}
Lampa.SettingsApi.addParam({
component:'ext_player',
param:{name:'ext_player',type:'select',values:presets,default:'off'},
field:{name:'\u041f\u043b\u0435\u0435\u0440',description:'\u0412\u044b\u0431\u0435\u0440\u0438\u0442\u0435 \u0432\u043d\u0435\u0448\u043d\u0438\u0439 \u043f\u043b\u0435\u0435\u0440 \u0434\u043b\u044f \u0442\u043e\u0440\u0440\u0435\u043d\u0442\u043e\u0432'},
onChange:function(v){toggleCustom(v);}
});
Lampa.SettingsApi.addParam({
component:'ext_player',
param:{name:'ext_player_custom',type:'input',values:'',default:''},
field:{name:'\u0421\u0432\u043e\u0439 \u0448\u0430\u0431\u043b\u043e\u043d',description:'${url} \u2014 encoded, ${furl} \u2014 raw, \u0438\u043b\u0438 \u043f\u0440\u043e\u0441\u0442\u043e \u043f\u0440\u0435\u0444\u0438\u043a\u0441 vlc://'}
});
Lampa.Settings.listener.follow('open',function(e){
if(e.name==='ext_player')toggleCustom(Lampa.Storage.field('ext_player'));
});
}
function toggleCustom(v){
var el=$('[data-name="ext_player_custom"]');
if(el.length)el.toggleClass('hide',v!=='custom');
}
function getTemplate(){
var key=Lampa.Storage.field('ext_player');
if(!key||key==='off')return'';
if(key==='custom')return Lampa.Storage.field('ext_player_custom')||'';
return PRESETS[key]||'';
}
function getToken(){
var m=document.cookie.match(/(?:^|;\s*)lampac_token=([^;]+)/);
if(m)return decodeURIComponent(m[1]);
try{var v=localStorage.getItem('lampac_auth_token');if(v)return v;}catch(e){}
return '';
}
function addToken(u){
var t=getToken();
if(!t)return u;
return u+(u.indexOf('?')!==-1?'&':'?')+'token='+encodeURIComponent(t);
}
function buildURL(stream){
var tpl=getTemplate();
if(!tpl)return'';
var s=addToken(stream);
if(tpl.indexOf('${url}')!==-1||tpl.indexOf('${furl}')!==-1){
return tpl.replace('${url}',encodeURIComponent(s)).replace('${furl}',s);
}
return tpl+s;
}
function isInfuseActive(){
var key=Lampa.Storage.field('ext_player');
if(key==='infuse')return true;
if(key==='custom'){
var c=Lampa.Storage.field('ext_player_custom')||'';
return c.indexOf('infuse://')===0;
}
return false;
}
function getMovie(){
try{var a=Lampa.Activity.active();if(a){if(a.card)return a.card;if(a.movie)return a.movie;}}catch(e){}
return null;
}
function pad2(n){n=String(n||0);return n.length<2?'0'+n:n;}
function cleanTitle(s){
return String(s||'')
.replace(/[^a-zA-Z0-9]+/g,'.')
.replace(/\.{2,}/g,'.')
.replace(/^\.+|\.+$/g,'');
}
function pickTitle(movie){
if(!movie)return'';
var raw=movie.original_title||movie.original_name||movie.title||movie.name||'';
return cleanTitle(raw);
}
function fileExt(p){var m=/\.[a-z0-9]{2,5}$/i.exec(p||'');return m?m[0]:'.mkv';}
function buildFilename(element,movie){
var clean=pickTitle(movie);if(!clean)clean='Media';
var ext=fileExt(element.path||element.path_human||element.fname||element.title);
var idTag=(movie&&movie.id)?'.{tmdb-'+movie.id+'}':'';
if(element.season!=null&&element.episode!=null){
return clean+'.S'+pad2(element.season)+'E'+pad2(element.episode)+idTag+ext;
}
return clean+idTag+ext;
}
function rewriteFilename(url,name){
if(!url||!name)return url;
var qIdx=url.indexOf('?');
var base=qIdx>=0?url.slice(0,qIdx):url;
var qs=qIdx>=0?url.slice(qIdx):'';
var slash=base.lastIndexOf('/');
var encoded=encodeURIComponent(name);
if(slash<0)return encoded+qs;
return base.slice(0,slash+1)+encoded+qs;
}
function streamFor(element,movie){
if(!element||!element.url)return'';
var raw=element.url.replace('&preload','&play');
var name=movie?buildFilename(element,movie):'';
return name?rewriteFilename(raw,name):raw;
}
function buildInfuseSave(items,movie){
var eps=[];
for(var i=0;i<items.length;i++){
var el=items[i];
if(!el||!el.url)continue;
if(el.season==null||el.episode==null)continue;
eps.push(el);
}
if(!eps.length)return'';
eps.sort(function(a,b){
var sa=+a.season||0,sb=+b.season||0;
if(sa!==sb)return sa-sb;
return (+a.episode||0)-(+b.episode||0);
});
var parts=[];
for(var j=0;j<eps.length;j++){
var s=addToken(streamFor(eps[j],movie));
parts.push('url='+encodeURIComponent(s)+'&index='+(j+1));
}
return 'infuse://x-callback-url/save?'+parts.join('&');
}
Lampa.Listener.follow('torrent_file',function(data){
if(data.type!=='onlong')return;
if(!getTemplate())return;
data.menu.push({title:'\u0417\u0430\u043f\u0443\u0441\u0442\u0438\u0442\u044c \u0432\u043d\u0435\u0448\u043d\u0438\u0439 \u043f\u043b\u0435\u0435\u0440',onSelect:function(){
var movie=getMovie();
var stream=streamFor(data.element,movie);
var target=buildURL(stream);
if(target)window.location.assign(target);
}});
if(isInfuseActive()){
var movie=getMovie();
var items=(data.items&&data.items.length)?data.items:[data.element];
var saveURL=buildInfuseSave(items,movie);
if(saveURL){
data.menu.push({title:'\u0421\u043e\u0445\u0440\u0430\u043d\u0438\u0442\u044c \u0441\u0435\u0440\u0438\u044e \u0432 Infuse',onSelect:function(){
window.location.assign(saveURL);
try{if(Lampa.Noty)Lampa.Noty.show('Infuse: \u0434\u043e\u0431\u0430\u0432\u043b\u0435\u043d\u043e \u0432 \u043c\u0435\u0434\u0438\u0430\u0442\u0435\u043a\u0443');}catch(e){}
}});
}
}
});
if(window.appready)addSettings();
else Lampa.Listener.follow('app',function(e){if(e.type==='ready')addSettings();});
})();`

func externalPlayerJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Use live config so admin-panel changes take effect immediately.
		liveCfg := liveConfig(cfg)

		protocol := strings.TrimSpace(liveCfg.TorrServer.ExternalPlayerProtocol)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		// Always serve the full plugin JS — settings are client-side.
		// Server default is injected for backward compat (first-run migration).
		serverDefault := "''"
		if protocol != "" {
			serverDefault = "'" + jsSingleQuoted(protocol) + "'"
		}
		out := strings.ReplaceAll(externalPlayerJS, "{serverDefault}", serverDefault)
		_, _ = w.Write([]byte(out))
	}
}

func tmdbProxyJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Use live config so admin-panel changes take effect immediately.
		if serverReady() {
			cfg = liveConfig(config.Config{})
		}

		tp := cfg.TMDBProxy
		mode := tp.Mode
		if mode == "" {
			mode = "self"
		}

		token := chi.URLParam(r, "token")
		src, err := loadPluginTemplate("tmdbproxy.js", cfg)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		host := hostFromRequest(r)

		// Determine the proxy base URL for the JS plugin.
		var proxyBase string
		switch mode {
		case "alcopa":
			h := strings.TrimSpace(tp.Host)
			if h == "" {
				h = "tmdb.alcopa.cc"
			}
			h = strings.TrimRight(h, "/")
			if strings.Contains(h, "://") {
				proxyBase = h
			} else {
				proxyBase = "http://" + h
			}
		default: // "self"
			proxyBase = host
		}

		out := strings.ReplaceAll(src, "{tmdb_proxy_mode}", mode)
		out = strings.ReplaceAll(out, "{tmdb_proxy_base}", proxyBase)
		out = strings.ReplaceAll(out, "{localhost}", host)
		out = strings.ReplaceAll(out, "{token}", url.QueryEscape(token))
		out = applyPublicBrandingJS(out)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

func syncJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		lite := parseBoolParam(r.URL.Query().Get("lite"))

		template := filepath.Join("sync_v2", "sync.js")
		if lite {
			template = "sync_lite.js"
		} else if loadSyncUserVersion(cfg) == 1 {
			template = "sync.js"
		}

		src, err := loadPluginTemplate(template, cfg)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		syncInvc, err := loadPluginTemplate("sync-invc.js", cfg)
		if err != nil || strings.TrimSpace(syncInvc) == "" {
			// Keep sync.js executable even when the custom sync snippet is missing.
			syncInvc = syncInvcFallback
		}

		host := hostFromRequest(r)
		out := strings.ReplaceAll(src, "{sync-invc}", syncInvc)
		out = strings.ReplaceAll(out, "{localhost}", host)
		out = strings.ReplaceAll(out, "{token}", url.QueryEscape(token))
		out = applyPublicBrandingJS(out)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		// no-store: see comment in genericPluginJSHandler.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

func onlineJSHandler(cfg config.Config, tgPending *tgauth.PendingStore, tgStore *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Use live config so settings from admin panel take effect immediately.
		if serverReady() {
			cfg = liveConfig(config.Config{})
		}

		token := chi.URLParam(r, "token")
		src, err := loadPluginTemplate("online.js", cfg)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		host := hostFromRequest(r)
		out := src
		rchScript := ""
		invcRch := ""
		invcRchNWS := ""

		// Rename the Lampa component identifier so this plugin can coexist with
		// other online plugins (Showy, NNMTV, etc.) on the same client. We
		// rewrite component: 'lampac' literals and Lampa.Component.add('lampac',
		// ...) calls — both must change together so registration and dispatch
		// stay aligned.
		if component := strings.TrimSpace(cfg.Online.Component); component != "" && component != "lampac" {
			safeComponent := jsSingleQuoted(component)
			out = strings.ReplaceAll(out, "component: 'lampac'", "component: '"+safeComponent+"'")
			out = strings.ReplaceAll(out, "Lampa.Component.add('lampac'", "Lampa.Component.add('"+safeComponent+"'")
		}

		// Namespace the title under a private Lang key. The stock online.js
		// registers the generic 'title_online' key globally — but Showy, NNMTV
		// and other forks of this plugin register the SAME key, so whichever
		// loads last forces its title (e.g. "Alpac Онлайн") onto every other
		// online plugin's header/button. Rewriting to 'lampac_title_online'
		// keeps our title scoped to our own component only.
		out = strings.ReplaceAll(out, "'title_online'", "'lampac_title_online'")
		out = strings.ReplaceAll(out, "title_online: {", "lampac_title_online: {")
		out = strings.ReplaceAll(out, "#{title_online}", "#{lampac_title_online}")

		if strings.Contains(out, "{rch_websoket}") {
			rchScript, invcRch, invcRchNWS, err = buildRchWebsocketScript(cfg, host, token)
			if err != nil {
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
		}

		playerInner := ""
		if strings.Contains(out, "{player-inner}") {
			playerInner, _ = loadPluginTemplate("player-inner.js", cfg)
			playerInner = strings.ReplaceAll(playerInner, "{useplayer}", "false")
			playerInner = strings.ReplaceAll(playerInner, "{notUseTranscoding}", "true")
			// Filmix прямой CDN (архитектура B) — только при direct_lampa. Иначе плейсхолдер
			// пустой и online.js/player-inner ведут себя ровно как раньше (никакого хука).
			fxDirect := ""
			if cfg.Online.Filmix.DirectLampa {
				fxDirect, _ = loadPluginTemplate("filmix-direct.js", cfg)
			}
			playerInner = strings.ReplaceAll(playerInner, "{filmix-direct}", fxDirect)
		}

		// Batch transcoding JS — inject before player-inner for series pre-transcoding.
		batchTC := ""
		if cfg.Transcoding.Enable {
			if raw, err := loadPluginTemplate("batch-transcoding.js", cfg); err == nil {
				batchTC = strings.ReplaceAll(raw, "{batchTranscodingEnabled}", "true")
			}
		}
		out = strings.ReplaceAll(out, "{batch-transcoding}", batchTC)

		out = strings.ReplaceAll(out, "{rch_websoket}", rchScript)
		out = strings.ReplaceAll(out, "{invc-rch}", invcRch)
		out = strings.ReplaceAll(out, "{invc-rch_nws}", invcRchNWS)
		out = strings.ReplaceAll(out, "{player-inner}", playerInner)
		out = strings.ReplaceAll(out, "{localhost}", host)
		out = strings.ReplaceAll(out, "{token}", url.QueryEscape(token))
		out = applyPublicBrandingJS(out)

		// Prepend silent auth-recovery gate for Android/TV (they don't load index.html from server).
		if cfg.TelegramAuth.BotToken != "" {
			// Check if user already authed (cookie or approved pending)
			userAuthed := false
			if token != "" {
				userAuthed = true // has path token
			}
			if !userAuthed && tgStore != nil {
				for _, cookieName := range []string{"_lampac_auth", "lampac_token"} {
					if c, err := r.Cookie(cookieName); err == nil {
						if tok := strings.TrimSpace(c.Value); tok != "" {
							if _, ok := tgStore.Lookup(tok); ok {
								userAuthed = true
								break
							}
						}
					}
				}
			}
			if !userAuthed {
				var embeddedToken string
				if tgPending != nil {
					if existing, ok := tgPending.FindByClientIP(clientIP(r)); ok && existing.Status == tgauth.StatusApproved && existing.Token != "" {
						embeddedToken = existing.Token
					}
				}
				if embeddedToken != "" {
					out = fmt.Sprintf(`(function(){
  try{document.cookie='lampac_token=%s;path=/;max-age=31536000;SameSite=Lax';}catch(e){}
  try{localStorage.setItem('lampac_auth_token','%s');}catch(e){}
})();`, embeddedToken, embeddedToken) + "\n" + out
				} else {
					// No approved pending either — prepend the silent-recovery
					// gate (no boot UI; unauthenticated users authorize in the
					// sources window via the accsdb QR card).
					out = buildAuthGateJS(host) + "\n" + out
				}
			}
		}

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		// Prevent browser from caching on.js — it contains the auth gate IIFE
		// which changes between deployments. Stale cached gate JS causes auth loops.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

func sisiJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Use live config so sisi settings from admin panel take effect immediately.
		if serverReady() {
			cfg = liveConfig(config.Config{})
		}

		token := chi.URLParam(r, "token")
		host := hostFromRequest(r)

		if parseBoolParam(r.URL.Query().Get("lite")) {
			src, err := loadPluginTemplate("sisi.lite.js", cfg)
			if err != nil {
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}

			out := strings.ReplaceAll(src, "{localhost}", host)
			out = applyPublicBrandingJS(out)
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(out))
			return
		}

		src, err := loadPluginTemplate("sisi.js", cfg)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		out := src
		rchScript := ""
		invcRch := ""
		invcRchNWS := ""

		if strings.Contains(out, "{rch_websoket}") {
			rchScript, invcRch, invcRchNWS, err = buildRchWebsocketScript(cfg, host, token)
			if err != nil {
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
		}

		if !cfg.Sisi.Spider {
			out = strings.ReplaceAll(out, "Lampa.Search.addSource(Search);", "")
		}

		component := strings.TrimSpace(cfg.Sisi.Component)
		if component != "" && component != "sisi" {
			safeComponent := jsSingleQuoted(component)
			out = strings.ReplaceAll(out, "use_api: 'lampac'", "use_api: '"+safeComponent+"'")
			out = strings.ReplaceAll(out, "'plugin_sisi_'", "'plugin_"+safeComponent+"_'")
		}

		if icon := strings.TrimSpace(cfg.Sisi.IconName); icon != "" {
			safeIcon := jsSingleQuoted(icon)
			out = strings.ReplaceAll(out, "Defined.use_api == 'pwa'", "true")
			out = strings.ReplaceAll(out, "'<div>p</div>'", "'<div>"+safeIcon+"</div>'")
		}

		out = strings.ReplaceAll(out, "{rch_websoket}", rchScript)
		out = strings.ReplaceAll(out, "{invc-rch}", invcRch)
		out = strings.ReplaceAll(out, "{invc-rch_nws}", invcRchNWS)
		out = strings.ReplaceAll(out, "{push_all}", strconv.FormatBool(cfg.Sisi.PushAll))
		out = strings.ReplaceAll(out, "{historySave}", strconv.FormatBool(cfg.Sisi.HistoryEnable))
		out = strings.ReplaceAll(out, "{localhost}", host)
		out = strings.ReplaceAll(out, "{token}", url.QueryEscape(token))

		if cfg.Sisi.ForcedCheckRchType {
			out = strings.ReplaceAll(out, "window.rchtype", "Defined.rchtype")
		}
		out = applyPublicBrandingJS(out)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

func buildRchWebsocketScript(cfg config.Config, host, token string) (rchScript, invcRch, invcRchNWS string, err error) {
	wsType := strings.ToLower(strings.TrimSpace(cfg.WebSocket.Type))
	if wsType == "" {
		wsType = "nws"
	}

	rchScript, err = loadPluginTemplate("rch_"+wsType+".js", cfg)
	if err != nil {
		// Keep runtime usable with unexpected websocket type.
		rchScript, err = loadPluginTemplate("rch_nws.js", cfg)
	}
	if err != nil {
		rchScript, err = loadPluginTemplate("rch_signalr.js", cfg)
	}
	if err != nil {
		return "", "", "", err
	}

	if strings.Contains(rchScript, "{invc-rch}") {
		invcRch, _ = loadPluginTemplate("invc-rch.js", cfg)
	}
	if strings.Contains(rchScript, "{invc-rch_nws}") {
		invcRchNWS, _ = loadPluginTemplate("invc-rch_nws.js", cfg)
	}

	rchScript = strings.ReplaceAll(rchScript, "{invc-rch}", invcRch)
	rchScript = strings.ReplaceAll(rchScript, "{invc-rch_nws}", invcRchNWS)
	rchScript = strings.ReplaceAll(rchScript, "{localhost}", host)
	rchScript = strings.ReplaceAll(rchScript, "{token}", url.QueryEscape(token))

	return rchScript, invcRch, invcRchNWS, nil
}

func onJSHandler(cfg config.Config, manifest []modules.RootModule, customPlugins *CustomPluginRegistry, tgPending *tgauth.PendingStore, tgStore *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Use live config so plugin toggles from admin panel take effect immediately.
		liveCfg := liveConfig(cfg)

		token := chi.URLParam(r, "token")
		adult := !strings.HasPrefix(strings.ToLower(r.URL.Path), "/on/h/")

		// Validate the path token. A dead token (expired, unbound, admin-revoked)
		// baked into sub-plugin URLs means every /online/js/<dead> load is
		// swallowed by the auth gate and the app boots empty with no way to
		// recover. Serve the unauthenticated bare flow instead: limited set +
		// gate JS, which can silently recover via localStorage/uid/fp/cub.
		rawPathToken := token
		pathTokenInvalid := false
		if token != "" && liveCfg.TelegramAuth.BotToken != "" && tgStore != nil {
			if _, ok := tgStore.Lookup(token); !ok {
				pathTokenInvalid = true
				token = ""
				log.Info().Str("ip", clientIP(r)).Msg("onjs: dead path token, serving bare flow")
			} else {
				// Valid tokenized bundle — re-issue the auth cookies on this
				// very response. The bundle <script> is the FIRST request of
				// a boot; TV WebViews (LG webOS) wipe the cookie jar on every
				// relaunch, and every cookie-dependent consumer (syncpro
				// profile, /ts probe, iptv2 playlists) loads after this
				// response — so the jar is repaired before any of them fires.
				setAuthCookies(w, r, token)
			}
		}

		src, err := loadPluginTemplate("on.js", liveCfg)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		host := hostFromRequest(r)
		country := countryFromRequest(r)

		// Resolve effective token for sub-plugin URLs.
		// For /on.js (external plugin), token is empty. Try to find it from
		// cookies or approved pending so sub-plugins get tokenized URLs
		// (e.g., /online/js/{token}) and don't show their own gate.
		effectiveToken := token
		if effectiveToken == "" && liveCfg.TelegramAuth.BotToken != "" {
			// Check cookies
			if tgStore != nil {
				for _, cookieName := range []string{"_lampac_auth", "lampac_token"} {
					if c, err := r.Cookie(cookieName); err == nil {
						if tok := strings.TrimSpace(c.Value); tok != "" {
							if _, ok := tgStore.Lookup(tok); ok {
								effectiveToken = tok
								break
							}
						}
					}
				}
			}
			// Check approved pending by IP
			if effectiveToken == "" && tgPending != nil {
				if existing, ok := tgPending.FindByClientIP(clientIP(r)); ok && existing.Status == tgauth.StatusApproved && existing.Token != "" {
					effectiveToken = existing.Token
				}
			}
		}

		// When loaded as external plugin (no token, bare /on.js or its short
		// alias), only include online.js — other plugins (tmdbproxy, sync, dlna,
		// etc.) can break the host Lampa instance by overriding its TMDB API,
		// sync, etc. Compare against boot cfg (not liveCfg): the alias route is
		// registered once at startup from boot cfg, so the path must match it.
		// Dead path token but a valid session found via cookie/pending — serve
		// the full set re-tokenized with the live token, as if the client had
		// asked for /on/js/<live> (its saved registry URL may hold the dead
		// token forever, so this serving is its only path to the full set).
		externalPlugin := token == "" && (r.URL.Path == "/on.js" || r.URL.Path == cfg.Web.ShortPluginRoute() ||
			(pathTokenInvalid && effectiveToken == ""))
		plugins := buildOnPlugins(host, effectiveToken, adult, liveCfg.Web.InitPlugins, liveManifest(liveCfg, manifest), externalPlugin, customPlugins)

		// Two-stage bootstrap flags (see on.js template):
		//   full_bundle  — this serving carries the full tokenized set.
		//   self_upgrade — bare serving only: the client may swap itself for
		//                  /on/js/<token> when it holds a durable token.
		//                  Tokenized servings (valid OR dead) bake false so a
		//                  dead token can't loop the client through upgrades.
		fullBundle := !externalPlugin && effectiveToken != ""

		out := strings.ReplaceAll(src, "{plugins}", strings.Join(plugins, ","))
		out = strings.ReplaceAll(out, "{full_bundle}", strconv.FormatBool(fullBundle))
		out = strings.ReplaceAll(out, "{self_upgrade}", strconv.FormatBool(rawPathToken == ""))
		out = strings.ReplaceAll(out, "{country}", country)
		out = strings.ReplaceAll(out, "{localhost}", host)
		out = applyPublicBrandingJS(out)

		// Prepend silent auth-recovery gate for apps loading /on.js as external
		// plugin. They don't load index.html from our server, so the recovery
		// ladder must ride in the JS. No boot UI — an unauthenticated user
		// authorizes in the sources window (accsdb QR card in online.js).
		if liveCfg.TelegramAuth.BotToken != "" && effectiveToken == "" {
			out = buildAuthGateJS(host) + "\n" + out
		} else if effectiveToken != "" && token == "" {
			// User authed but loaded bare /on.js — embed token-saving script
			// so localStorage has it for account() fallback. Cookie name
			// pair (lampac_token + alpac_token) and SameSite tuning matches
			// setAuthCookies() so a cross-origin Lampa install (e.g.
			// lampa.mx loading beta.l-vid.online/on.js) replays the cookie
			// on subsequent XHRs.
			out = fmt.Sprintf(`(function(){
  try{
    var sas=(location.protocol==='https:'?';SameSite=None;Secure':';SameSite=Lax');
    document.cookie='lampac_token=%s;path=/;max-age=31536000'+sas;
    document.cookie='alpac_token=%s;path=/;max-age=31536000'+sas;
  }catch(e){}
  try{localStorage.setItem('lampac_auth_token','%s');}catch(e){}
})();`, effectiveToken, effectiveToken, effectiveToken) + "\n" + out
		}

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

// onPluginMandatory lists the plugin keys that always load. Everything else
// in the bundle is OPTIONAL: baked with "o":1 and loaded by the on.js
// bootstrap only when the user flipped its toggle in Settings → Аккаунт
// (Lampa.Storage key alcopac_plug_<key>). Default experience for a stock
// Lampa install is deliberately minimal — just the online source list plus
// the account surface that hosts the toggles themselves.
var onPluginMandatory = map[string]bool{
	"online":  true,
	"account": true,
	// ads (ad stripping) is not a user decision — always on, no toggle.
	"ads": true,
}

func buildOnPlugins(host, token string, adult bool, init config.InitPluginsConfig, manifest []modules.RootModule, externalPlugin bool, customPlugins *CustomPluginRegistry) []string {
	plugins := make([]string, 0, 12)
	enabledByModule := moduleEnabledMap(manifest)

	// Entries are JSON objects, not bare URL strings: the on.js bootstrap
	// filters optional ones by the user's toggles and hands the list to
	// account.js to render those toggles ({"k":key,"o":0|1,"u":url}).
	add := func(name string, tokenized bool) {
		u := host + "/" + name + ".js"
		if tokenized && token != "" {
			u = host + "/" + name + "/js/" + url.QueryEscape(token)
		}
		opt := 1
		if onPluginMandatory[name] {
			opt = 0
		}
		plugins = append(plugins, fmt.Sprintf(`{"k":%q,"o":%d,"u":%q}`, name, opt, u))
	}

	hasModule := func(dll string) bool {
		if len(enabledByModule) == 0 {
			return true
		}
		return enabledByModule[strings.ToLower(dll)]
	}

	// External plugin mode: only load online.js (and catalog).
	// Other plugins (tmdbproxy, cubproxy, dlna, tracks, sync, etc.)
	// override core Lampa functions (TMDB API, CUB proxy, sync)
	// and break the host Lampa instance when loaded cross-origin.
	if externalPlugin {
		if init.Online && hasModule("Online.dll") {
			add("online", true)
		}
		if init.Catalog && hasModule("Catalog.dll") {
			add("catalog", true)
		}
		// account — Settings → Аккаунт surface. Includes the "Войти /
		// Перезайти / Выйти" controls users need to recover when the
		// auth gate fails to render. Safe in external-plugin mode: it
		// only touches Lampa.SettingsApi + Lampa.Activity hook; doesn't
		// override any TMDB/CUB/sync internals.
		add("account", false)
		// server_widget — read-only cluster availability dashboard.
		// Doesn't override any core Lampa functionality so it's safe in
		// external-plugin mode (where this lampac is embedded into a
		// host Lampa that already has its own TMDB/sync/etc).
		if init.ServerWidget {
			add("server_widget", false)
		}
		return plugins
	}

	if init.DLNA && hasModule("DLNA.dll") {
		add("dlna", true)
	}

	if hasModule("Tracks.dll") {
		if init.Tracks {
			add("tracks", true)
		}
		if init.Transcoding {
			add("transcoding", true)
		}
	}

	if init.TMDBProxy {
		add("tmdbproxy", true)
	}

	if init.Online && hasModule("Online.dll") {
		add("online", true)
	}

	if init.Catalog && hasModule("Catalog.dll") {
		add("catalog", true)
	}

	if adult && init.SISI && hasModule("SISI.dll") {
		add("sisi", true)
		add("startpage", false)
	}

	if init.Sync {
		add("sync", true)
	}
	if init.Timecode {
		add("timecode", true)
	}
	if init.Bookmark {
		add("bookmark", true)
	}
	if init.TorrServer && hasModule("TorrServer.dll") {
		add("ts", true)
	}
	if init.TorrServer && hasModule("TorrServer.dll") {
		// External player is loaded via its own handler that checks the config at
		// serve time, so we always register the route but the JS becomes a no-op
		// when the protocol is empty.
		add("external_player", true)
	}
	if init.Backup {
		add("backup", true)
	}
	if init.AdsFree {
		add("ads", true)
	}
	if init.YouTubeFeed {
		add("youtube_feed", true)
	}

	// Append autoloaded custom plugins.
	if customPlugins != nil && !externalPlugin {
		for _, p := range customPlugins.AutoloadPlugins() {
			add(p.Name, true)
		}
	}

	// account — Settings → Аккаунт surface. Always loaded; gates itself
	// off /api/auth/whoami at runtime so a TG-disabled deploy doesn't
	// show a half-useful tab. Mirrored in the externalPlugin branch above.
	add("account", false)

	// Server widget — show current cluster node + picker. Cheap, optional.
	if init.ServerWidget {
		add("server_widget", false)
	}

	return plugins
}

func moduleEnabledMap(manifest []modules.RootModule) map[string]bool {
	out := make(map[string]bool, len(manifest))
	for _, mod := range manifest {
		out[strings.ToLower(mod.Dll)] = mod.Enable
	}
	return out
}

func hostFromRequest(r *http.Request) string {
	scheme := "http"
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = xf
	} else if r.TLS != nil {
		scheme = "https"
	}

	// Prefer X-Forwarded-Host when the request came through a reverse proxy
	// (nginx, caddy, cloudflare, the cluster forwarder, ...). This is the
	// canonical way to learn the public-facing host when the upstream Host
	// header carries the internal backend address. Falls back to r.Host when
	// no proxy header is present.
	var host string
	if xfh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xfh != "" {
		// XFH may contain a comma-separated chain — take the first value
		// (the original client-facing host).
		if i := strings.IndexByte(xfh, ','); i > 0 {
			xfh = strings.TrimSpace(xfh[:i])
		}
		host = xfh
	}
	if host == "" {
		host = r.Host
	}
	// Last resort: use RemoteAddr peer (strip port).
	if host == "" {
		host = r.RemoteAddr
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}

	return scheme + "://" + host
}

// streamHostFromRequest returns the host part to use when emitting URLs for
// stream-bound endpoints (`/proxy/`, `/transcoding/`, `/nws`, `/ws`). When
// the request comes in via a host listed in `[host.stream_aliases]`, the
// aliased host is returned — so video and WebSocket traffic can bypass a
// fronting CDN (e.g. CloudFlare on `lampa.li` → direct `s.lampa.li`).
// For requests on unmapped hosts (e.g. `beta.l-vid.online` direct), the
// original host is returned unchanged.
//
// Use for stream URL generation only. For API/HTML URLs that should stay on
// the fronting domain (so auth cookies and CDN protection apply), keep
// hostFromRequest.
func streamHostFromRequest(r *http.Request) string {
	host := hostFromRequest(r)
	if !serverReady() {
		return host
	}
	return liveConfig(config.Config{}).Host.StreamHostFor(host)
}

func countryFromRequest(r *http.Request) string {
	candidates := []string{
		r.Header.Get("CF-IPCountry"),
		r.Header.Get("X-Country-Code"),
		r.Header.Get("X-Geo-Country"),
	}
	for _, c := range candidates {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "" {
			return c
		}
	}
	return ""
}

// pluginTemplateCache caches plugin file contents in memory to avoid
// repeated disk reads. Entries expire after 60s so admin changes take effect.
var pluginTemplateCache = struct {
	sync.RWMutex
	items map[string]pluginTemplateCacheEntry
}{items: make(map[string]pluginTemplateCacheEntry, 16)}

type pluginTemplateCacheEntry struct {
	content   string
	expiresAt time.Time
}

func loadPluginTemplate(file string, cfg config.Config) (string, error) {
	// Cache key includes the repo root so different test harnesses (each
	// with its own t.TempDir() RepoRoot) don't see each other's files.
	// In production cfg.Compat.RepoRoot is constant per process so this is
	// effectively a one-namespace cache.
	cacheKey := file + "\x00" + cfg.Compat.RepoRoot
	// Check cache first.
	pluginTemplateCache.RLock()
	if e, ok := pluginTemplateCache.items[cacheKey]; ok && time.Now().Before(e.expiresAt) {
		pluginTemplateCache.RUnlock()
		return e.content, nil
	}
	pluginTemplateCache.RUnlock()

	// Read from disk.
	for _, p := range pluginCandidates(file, cfg) {
		for _, variant := range withMyVariant(p) {
			data, err := os.ReadFile(variant)
			if err == nil {
				content := string(data)
				pluginTemplateCache.Lock()
				pluginTemplateCache.items[cacheKey] = pluginTemplateCacheEntry{
					content:   content,
					expiresAt: time.Now().Add(60 * time.Second),
				}
				pluginTemplateCache.Unlock()
				return content, nil
			}
		}
	}
	return "", errors.New("plugin template not found")
}

func pluginCandidates(file string, cfg config.Config) []string {
	candidates := make([]string, 0, 5)
	if custom := strings.TrimSpace(os.Getenv("LAMPAC_GO_PLUGINS_DIR")); custom != "" {
		candidates = append(candidates, filepath.Join(custom, file))
	}

	candidates = append(candidates,
		filepath.Join(cfg.Compat.RepoRoot, "plugins", file),
		filepath.Join("plugins", file),
		filepath.Join("/home/plugins", file),
	)
	return candidates
}

// webplayerAssetHandler serves binary runtime assets for the web player
// (JASSUB worker + wasm for on-device ASS rendering) from
// plugins/webplayer-assets/. Names are whitelisted — this is not a generic
// file server.
func webplayerAssetHandler(cfg config.Config) http.HandlerFunc {
	allowed := map[string]string{
		"jassub.esm.js":             "application/javascript", // self-contained ESM (esbuild-bundled jassub)
		"jassub-worker.js":          "application/javascript",
		"jassub-worker.wasm":        "application/wasm",
		"jassub-worker-modern.wasm": "application/wasm",
	}
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		mime, ok := allowed[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		for _, p := range pluginCandidates(filepath.Join("webplayer-assets", name), cfg) {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				w.Header().Set("Content-Type", mime)
				w.Header().Set("Cache-Control", "public, max-age=86400")
				http.ServeFile(w, r, p)
				return
			}
		}
		http.NotFound(w, r)
	}
}

func withMyVariant(path string) []string {
	ext := filepath.Ext(path)
	if ext == "" {
		return []string{path}
	}

	base := strings.TrimSuffix(path, ext)
	my := base + ".my" + ext
	if my == path {
		return []string{path}
	}
	return []string{my, path}
}

// adsFreeJS is the client-side Lampa hook that removes VAST ads.
// Embedded directly to avoid an external template file.
const adsFreeJS = `(function () {
    function initLampaHook() {
        if (window.Lampa && Lampa.Player && Lampa.Player.play) {
            var originalPlay = Lampa.Player.play;
            Lampa.Player.play = function (object) {
                object.iptv = true;
                if (object.vast_url) delete object.vast_url;
                if (object.vast_msg) delete object.vast_msg;
                return originalPlay.apply(this, arguments);
            };
        } else {
            setTimeout(initLampaHook, 500);
        }
    }
    initLampaHook();
})();`

func adsJSHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(adsFreeJS))
	}
}

func jsSingleQuoted(value string) string {
	return strings.ReplaceAll(value, "'", "\\'")
}

// buildAuthGateJS returns a self-contained silent auth-recovery IIFE.
// It renders NO UI at boot: it walks the recovery ladder (saved token →
// uid/cub → device fingerprints), repairs cookies/localStorage on success and
// asks the on.js bootstrap to upgrade to the tokenized bundle. When every rung
// fails, an app session is simply left unauthenticated — the user authorizes
// in the sources window (accsdb QR card in online.js, a Lampa modal where the
// remote's Back is handled by Lampa itself); only a non-Lampa browser page is
// redirected to the full /tg/auth page. The old boot-time fullscreen QR
// overlay was removed deliberately: its key trap swallowed all keys but
// didn't know LG's Back (keyCode 461), so on webOS Back fell through to the
// platform default and closed the whole app instead of the gate.
func buildAuthGateJS(host string) string {
	return `(function(){
  if(window.__alcopacAuthGate) return;
  window.__alcopacAuthGate=1;
  var LS_TOK='lampac_auth_token';
  var _srvHost='` + host + `';
  var _lo=window.location.origin||'';
  var origin=(_lo&&_lo!=='null'&&_lo.indexOf('http')===0&&_lo.indexOf('127.0.0.1')<0&&_lo.indexOf('localhost')<0)?_lo:_srvHost;

  // --- Helpers ---
  function getToken(){
    // Check alpac_token first (our brand-scoped name, safe from third-party
    // plugins overwriting), then fall back to legacy lampac_token.
    try{
      var ca=document.cookie.match(/(?:^|;\s*)alpac_token=([^;]*)/);
      if(ca)return decodeURIComponent(ca[1]);
      var cl=document.cookie.match(/(?:^|;\s*)lampac_token=([^;]*)/);
      if(cl)return decodeURIComponent(cl[1]);
    }catch(e){}
    try{var v=localStorage.getItem(LS_TOK);if(v)return v;}catch(e){}
    return '';
  }
  function saveToken(tok){
    if(!tok)return;
    // SameSite=None;Secure on HTTPS so the cookie is sent on cross-origin
    // XHRs (lampa.mx → beta.l-vid.online scenario). HTTP installs fall
    // back to Lax — None requires Secure which plain-HTTP can't provide.
    var sas=(location.protocol==='https:'?';SameSite=None;Secure':';SameSite=Lax');
    try{
      document.cookie='lampac_token='+tok+';path=/;max-age=31536000'+sas;
      document.cookie='alpac_token='+tok+';path=/;max-age=31536000'+sas;
    }catch(e){}
    try{localStorage.setItem(LS_TOK,tok);}catch(e){}
  }
  function clearToken(){
    // Aggressively clear cookies for all possible domain variants — both
    // namespaces (lampac_token + alpac_token) so a partial logout
    // doesn't leave one side alive.
    try{
      ['lampac_token','alpac_token'].forEach(function(n){
        document.cookie=n+'=;path=/;max-age=0';
        var d=location.hostname;
        document.cookie=n+'=;path=/;max-age=0;domain='+d;
        document.cookie=n+'=;path=/;max-age=0;domain=.'+d;
        var pts=d.split('.');if(pts.length>2)document.cookie=n+'=;path=/;max-age=0;domain=.'+pts.slice(-2).join('.');
      });
    }catch(e){}
    try{localStorage.removeItem(LS_TOK);}catch(e){}
  }
  function getUID(){
    try{var raw=localStorage.getItem('lampac_unic_id');if(raw){try{var p=JSON.parse(raw);if(typeof p==='string'&&p)return p;}catch(e){if(typeof raw==='string'&&raw)return raw;}}}catch(e){}
    return '';
  }
  // ensureUID generates and persists a device UID when none exists yet —
  // on an external Lampa (plugin added by URL) nothing else creates
  // lampac_unic_id, and a recovery without uid authorizes the session but
  // never binds the device (invisible in the bot, no uid-based recovery).
  function ensureUID(){
    var u=getUID();
    if(u)return u;
    u='';var abc='abcdefghijklmnopqrstuvwxyz0123456789';
    for(var i=0;i<8;i++)u+=abc.charAt(Math.floor(Math.random()*abc.length));
    try{localStorage.setItem('lampac_unic_id',u);}catch(e){}
    try{localStorage.setItem('lampac_uid_backup',u);}catch(e){}
    return u;
  }
  // CUB session token from Lampa's account storage — the longest-lived
  // recovery anchor: the server links it to the account passively, and after
  // a full wipe a fresh CUB login alone restores the binding.
  function getCub(){
    try{var a=JSON.parse(localStorage.getItem('account')||'{}');if(a&&typeof a.token==='string'&&a.token)return a.token;}catch(e){}
    return '';
  }

  // --- FNV-1a hash (matches Go server) ---
  function fnv1a(str){
    var h=0x811c9dc5;
    for(var i=0;i<str.length;i++){h^=str.charCodeAt(i);h=Math.imul(h,0x01000193);}
    return (h>>>0).toString(16);
  }

  // --- Device Fingerprint (survives data clear) ---
  function getFingerprint(cb){
    var parts=[];
    try{parts.push('ua:'+navigator.userAgent);}catch(e){}
    try{parts.push('plt:'+navigator.platform);}catch(e){}
    try{parts.push('lang:'+(navigator.language||navigator.userLanguage||''));}catch(e){}
    try{parts.push('tz:'+Intl.DateTimeFormat().resolvedOptions().timeZone);}catch(e){}
    try{parts.push('scr:'+screen.width+'x'+screen.height+'x'+screen.colorDepth);}catch(e){}
    try{parts.push('dpr:'+(window.devicePixelRatio||1));}catch(e){}
    try{parts.push('cores:'+(navigator.hardwareConcurrency||0));}catch(e){}
    try{parts.push('mem:'+(navigator.deviceMemory||0));}catch(e){}
    try{parts.push('touch:'+(navigator.maxTouchPoints||0));}catch(e){}
    // WebGL renderer (GPU fingerprint — very stable)
    try{
      var c=document.createElement('canvas');var gl=c.getContext('webgl')||c.getContext('experimental-webgl');
      if(gl){
        var dbg=gl.getExtension('WEBGL_debug_renderer_info');
        if(dbg){parts.push('glv:'+gl.getParameter(dbg.UNMASKED_VENDOR_WEBGL));parts.push('glr:'+gl.getParameter(dbg.UNMASKED_RENDERER_WEBGL));}
        parts.push('glver:'+gl.getParameter(gl.VERSION));
        parts.push('glsl:'+gl.getParameter(gl.SHADING_LANGUAGE_VERSION));
      }
    }catch(e){}
    // Canvas fingerprint (rendering differences between GPUs)
    try{
      var cv=document.createElement('canvas');cv.width=240;cv.height=60;
      var cx=cv.getContext('2d');
      cx.textBaseline='alphabetic';cx.fillStyle='#f60';cx.fillRect(125,1,62,20);
      cx.fillStyle='#069';cx.font='11pt Arial';cx.fillText('Cwm fjord veg',2,15);
      cx.fillStyle='rgba(102,204,0,0.7)';cx.font='18pt Arial';cx.fillText('Cwm fjord veg',4,45);
      parts.push('cvs:'+cv.toDataURL().slice(-50));
    }catch(e){}
    // AudioContext fingerprint
    try{
      var actx=new(window.OfflineAudioContext||window.webkitOfflineAudioContext)(1,44100,44100);
      var osc=actx.createOscillator();osc.type='triangle';osc.frequency.setValueAtTime(10000,actx.currentTime);
      var comp=actx.createDynamicsCompressor();
      comp.threshold.setValueAtTime(-50,actx.currentTime);comp.knee.setValueAtTime(40,actx.currentTime);
      comp.ratio.setValueAtTime(12,actx.currentTime);comp.attack.setValueAtTime(0,actx.currentTime);comp.release.setValueAtTime(0.25,actx.currentTime);
      osc.connect(comp);comp.connect(actx.destination);osc.start(0);
      actx.startRendering().then(function(buf){
        var d=buf.getChannelData(0);var sum=0;for(var i=4500;i<5000;i++)sum+=Math.abs(d[i]);
        parts.push('audio:'+sum.toFixed(6));
        cb(fnv1a(parts.join('|')));
      }).catch(function(){cb(fnv1a(parts.join('|')));});
      setTimeout(function(){cb(fnv1a(parts.join('|')));},1000);
    }catch(e){
      cb(fnv1a(parts.join('|')));
    }
  }

  // --- Auth flow ---
  // statusReq queries /tg/auth/status on the server host baked into this
  // script FIRST — on an external Lampa (lampa.mx with our server plugged
  // in) the page origin is a foreign host where /tg/auth/status doesn't
  // exist, and some SPAs answer unknown paths with a 200 that would end the
  // ladder with a bogus non-authorized result. The page origin stays as a
  // fallback for the one case where _srvHost is garbage: a reverse proxy
  // that didn't forward the Host header.
  function statusReq(qs,done,fail){
    var hosts=[];
    if(_srvHost)hosts.push(_srvHost);
    if(origin&&origin!==_srvHost)hosts.push(origin);
    var i=0;
    function next(){i++;if(i<hosts.length)attempt();else fail();}
    function attempt(){
      var x=new XMLHttpRequest();
      x.open('GET',hosts[i]+'/tg/auth/status?'+qs,true);
      // Cross-origin fallback needs credentials so the server can set our
      // auth cookies; same-origin ignores the flag.
      try{x.withCredentials=true;}catch(e){}
      x.timeout=8000;
      x.onload=function(){
        if(x.status===200){
          var r=null;
          try{r=JSON.parse(x.responseText);}catch(e){}
          if(r){done(r);return;}
        }
        next();
      };
      x.onerror=next;
      x.ontimeout=next;
      x.send();
    }
    attempt();
  }
  // After a silent recovery, complete the CURRENT session: the plugin
  // bundle was already served in its limited unauthenticated form, so ask
  // the on.js bootstrap (window.alcopac_upgrade) to pull the tokenized
  // full bundle now instead of waiting for the next app restart.
  function upgradeBundle(tok){
    try{if(tok&&window.alcopac_upgrade)window.alcopac_upgrade(tok);}catch(e){}
  }
  function checkToken(tok){
    var cub=getCub();
    // uid rides along so the server can bind this device — on external
    // Lampa hosts cookies never travel and /lite/* resolves the user by
    // ?uid= alone; an authorized-but-unbound device still sees the
    // "authorize" banner in the source list.
    var uid=ensureUID();
    statusReq('token='+encodeURIComponent(tok)+(uid?'&uid='+encodeURIComponent(uid):'')+(cub?'&cub='+encodeURIComponent(cub):''),function(r){
      if(r&&r.authorized){saveToken(r.token||tok);upgradeBundle(r.token||tok);return;}
      clearToken();
      // After clearing invalid cookie, check if localStorage had a DIFFERENT valid token
      try{var ls=localStorage.getItem(LS_TOK);if(ls&&ls!==tok){saveToken(ls);checkToken(ls);return;}}catch(e){}
      tryRecovery();
    },function(){});
  }
  var token=getToken();
  if(token){
    checkToken(token);
  } else {
    tryRecovery();
  }

  function tryRecovery(){
    var uid=getUID();
    var cub=getCub();
    // No uid AND no cub token — nothing this rung can do, go straight to
    // fingerprints. With a cub token we still ask (and mint a uid so a
    // successful CUB recovery binds this device).
    if(uid||cub){
      if(!uid)uid=ensureUID();
      statusReq('uid='+encodeURIComponent(uid)+(cub?'&cub='+encodeURIComponent(cub):''),function(r){
        if(r&&r.authorized){saveToken(r.token||'');upgradeBundle(r.token);return;}
        tryFingerprint();
      },tryFingerprint);
    } else {
      tryFingerprint();
    }
  }

  // Coarse, drift-resistant fingerprint (stable hardware subset) — a secondary
  // anchor for the case where the precise fp shifted after a firmware update.
  function stableFP(){
    var p=[];
    try{p.push((screen.width||0)+'x'+(screen.height||0));}catch(e){}
    try{p.push(screen.colorDepth||0);}catch(e){}
    try{p.push(window.devicePixelRatio||1);}catch(e){}
    try{p.push(navigator.hardwareConcurrency||0);}catch(e){}
    try{p.push(navigator.deviceMemory||0);}catch(e){}
    try{p.push(navigator.platform||'');}catch(e){}
    try{p.push(navigator.maxTouchPoints||0);}catch(e){}
    try{p.push(Intl.DateTimeFormat().resolvedOptions().timeZone||'');}catch(e){}
    try{p.push((navigator.userAgent||'').replace(/[\d.]+/g,'').slice(0,120));}catch(e){}
    return fnv1a(p.join('|'));
  }
  function tryFingerprint(){
    getFingerprint(function(fp){
      var sfp=stableFP();
      var cub=getCub();
      if(!fp&&!sfp&&!cub){showGate();return;}
      var qs=[];
      if(fp)qs.push('fp='+encodeURIComponent(fp));
      if(sfp)qs.push('sfp='+encodeURIComponent(sfp));
      if(cub)qs.push('cub='+encodeURIComponent(cub));
      var uid=ensureUID();
      if(uid)qs.push('uid='+encodeURIComponent(uid));
      statusReq(qs.join('&'),function(r){
        if(r&&r.authorized){saveToken(r.token||'');upgradeBundle(r.token);return;}
        showGate();
      },showGate);
    });
  }

  // showGate: every silent recovery rung failed — this session is genuinely
  // unauthenticated. Inside an app we deliberately render NOTHING at boot:
  // the old fullscreen QR overlay trapped all keys but didn't know LG's
  // Back (keyCode 461), so on webOS Back fell through to the platform
  // default and closed the whole app. Auth now happens at the point of
  // use — the gate middleware answers /lite/* with accsdb+code and
  // online.js shows the QR card as a Lampa modal (Back handled by Lampa).
  function showGate(){
    // Detect if running inside Lampa app (not a plain browser).
    var isApp=!!(window.Lampa||window.appready||window.AndroidJS||typeof webOS!=='undefined'||/Tizen|WebOS|HbbTV|SMART-TV/i.test(navigator.userAgent));
    if(!isApp){
      // The auth page lives on OUR server — the page origin may be a
      // foreign Lampa host (lampa.mx) where /tg/auth is a 404.
      window.location.href=(_srvHost||origin)+'/tg/auth';
    }
  }
})();`
}

// generateQRDataURI creates a QR code PNG as a base64 data URI.
// Returns empty string on error or empty input.
func generateQRDataURI(data string) string {
	if data == "" {
		return ""
	}
	png, err := qrcode.Encode(data, qrcode.Medium, 200)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}
