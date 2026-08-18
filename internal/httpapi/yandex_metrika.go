package httpapi

import "strings"

// sanitizeYandexMetrikaID keeps only digits from the operator-supplied counter
// ID. The value is interpolated directly into the app.min.js JS stream, so
// stripping everything but digits prevents an accidental (or malicious) config
// value from injecting arbitrary script into every client's bundle.
func sanitizeYandexMetrikaID(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// yandexMetrikaTemplate is a self-contained Yandex.Metrika loader appended to
// app.min.js. It is idempotent (guards on window.__lampacYM and on a duplicate
// tag.js <script>), detects which client channel it is running on (web /
// android-app / android / tizen / webos / apple / smarttv) and reports that as
// a visit + user parameter so a single counter segments every platform. Lampa
// is an SPA, so we also fire an explicit hit on each screen change once
// Lampa.Listener is available, and re-check the channel then (the hybrid
// Android bridge can attach to window after first paint).
const yandexMetrikaTemplate = `;(function(){
'use strict';
var COUNTER=__COUNTER__;
if(!COUNTER||window.__lampacYM)return;window.__lampacYM=true;
function chan(){try{
var ua=(navigator.userAgent||'').toLowerCase();
var P=window.Lampa&&Lampa.Platform;
if(window.AndroidJS&&(AndroidJS.getLampaURL||AndroidJS.openYoutube||AndroidJS.voiceStart||AndroidJS.openPlayer))return 'android-app';
if(P&&P.is){if(P.is('tizen'))return 'tizen';if(P.is('webos'))return 'webos';if(P.is('apple_tv'))return 'apple_tv';if(P.is('android'))return window.AndroidJS?'android-app':'android';if(P.is('apple'))return 'apple';}
if(ua.indexOf('tizen')>-1)return 'tizen';
if(ua.indexOf('web0s')>-1||ua.indexOf('webos')>-1)return 'webos';
if(ua.indexOf('android')>-1)return window.AndroidJS?'android-app':'android';
if(ua.indexOf('smart-tv')>-1||ua.indexOf('smarttv')>-1||ua.indexOf('crkey')>-1)return 'smarttv';
if(/iphone|ipad|ipod/.test(ua))return 'apple';
return 'web';
}catch(e){return 'web';}}
var channel=chan();
(function(m,e,t,r,i,k,a){m[i]=m[i]||function(){(m[i].a=m[i].a||[]).push(arguments)};m[i].l=1*new Date();for(var j=0;j<e.scripts.length;j++){if(e.scripts[j].src===r){return;}}k=e.createElement(t),a=e.getElementsByTagName(t)[0],k.async=1,k.src=r,a.parentNode.insertBefore(k,a)})(window,document,'script','https://mc.yandex.ru/metrika/tag.js?id='+COUNTER,'ym');
ym(COUNTER,'init',{ssr:true,webvisor:true,clickmap:true,ecommerce:'dataLayer',accurateTrackBounce:true,trackLinks:true,params:{channel:channel,app:'lampac'}});
try{ym(COUNTER,'params',{channel:channel});}catch(e){}
try{ym(COUNTER,'userParams',{channel:channel});}catch(e){}
function refine(){try{var c=chan();if(c!==channel){channel=c;ym(COUNTER,'params',{channel:channel});ym(COUNTER,'userParams',{channel:channel});}}catch(e){}}
function hit(){refine();try{ym(COUNTER,'hit',location.href,{params:{channel:channel}});}catch(e){}}
function bind(){if(!(window.Lampa&&Lampa.Listener))return false;try{Lampa.Listener.follow('activity',function(e){if(e&&e.type==='start')hit();});}catch(e){}refine();return true;}
if(!bind()){var n=0,iv=setInterval(function(){if(bind()||++n>40)clearInterval(iv);},500);}
})();`

// yandexMetrikaSnippet returns the ready-to-append JS for the given counter ID,
// or "" when the ID is empty/invalid (feature disabled).
func yandexMetrikaSnippet(id string) string {
	id = sanitizeYandexMetrikaID(id)
	if id == "" {
		return ""
	}
	return strings.ReplaceAll(yandexMetrikaTemplate, "__COUNTER__", id)
}
