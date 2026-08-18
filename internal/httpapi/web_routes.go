package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type weblogSettings struct {
	Enable bool
	Token  string
}

func extensionsHandler(cfgRoot string, customPlugins *CustomPluginRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		candidates := []string{
			filepath.Join(cfgRoot, "plugins", "extensions.json"),
			filepath.Join("plugins", "extensions.json"),
			filepath.Join("/home/plugins", "extensions.json"),
		}

		var data []byte
		for _, p := range candidates {
			raw, err := os.ReadFile(p)
			if err == nil {
				data = raw
				break
			}
		}

		if len(data) == 0 {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		host := hostFromRequest(r)
		payload := strings.ReplaceAll(string(data), "{localhost}", host)
		payload = strings.ReplaceAll(payload, "\n", "")
		payload = strings.ReplaceAll(payload, "\r", "")

		// Inject public custom plugins as a "Рекомендации" section.
		if customPlugins != nil {
			pub := customPlugins.PublicPlugins()
			if len(pub) > 0 {
				payload = injectPublicPlugins(payload, pub, host)
			}
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}
}

// injectPublicPlugins adds a "Рекомендации" section to the extensions JSON.
func injectPublicPlugins(payload string, plugins []CustomPlugin, host string) string {
	// Build the section items.
	items := make([]map[string]any, 0, len(plugins))
	for _, p := range plugins {
		author := p.Author
		if author == "" {
			author = "@admin"
		}
		descr := p.Descr
		if descr == "" {
			descr = p.Name
		}
		img := resolvePluginImageURL(p.Image, host)
		items = append(items, map[string]any{
			"name":            p.Name,
			"author":          author,
			"image":           img,
			"link":            host + "/" + p.Name + ".js",
			"descr":           descr,
			"available_lampa": 1,
		})
	}

	section := map[string]any{
		"title":   "\u0420\u0435\u043a\u043e\u043c\u0435\u043d\u0434\u0443\u0435\u0442 \u043a\u043e\u043c\u044c\u044e\u043d\u0438\u0442\u0438",
		"hpu":     "community",
		"results": items,
	}

	// Parse existing JSON, prepend section, re-serialize.
	var root map[string]any
	if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return payload
	}
	results, _ := root["results"].([]any)
	// Prepend community section as first.
	newResults := make([]any, 0, len(results)+1)
	newResults = append(newResults, section)
	newResults = append(newResults, results...)
	root["results"] = newResults

	out, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return string(out)
}

func weblogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings := loadWeblogSettings()
		if !settings.Enable {
			writePlain(w, http.StatusOK, "Включите weblog в init.conf\n\n\"weblog\": {\n   \"enable\": true\n}")
			return
		}

		token := strings.TrimSpace(r.URL.Query().Get("token"))
		if settings.Token != "" && token != settings.Token {
			writePlain(w, http.StatusOK, "Используйте /weblog?token=my_key\n\n\"weblog\": {\n   \"enable\": true,\n   \"token\": \"my_key\"\n}")
			return
		}

		receive := strings.TrimSpace(r.URL.Query().Get("receive"))
		if receive == "" {
			receive = "http"
		}
		pattern := strings.TrimSpace(r.URL.Query().Get("pattern"))

		// Keep page behavior compatible with legacy scripts/routes.
		var sb strings.Builder
		sb.WriteString("<!DOCTYPE html><html><head><meta charset='utf-8' /><title>weblog</title></head><body style='margin:0;'>")
		sb.WriteString("<div id='controls' style='margin-bottom:1em;background:#f0f0f0;padding:10px;border-bottom:1px solid #ccc;'>")
		sb.WriteString("<label style='margin-right:20px;'>Запросы:<select id='receiveSelect' style='padding:0 5px 0 0;'>")
		if receive == "request" {
			sb.WriteString("<option value='http'>Исходящие</option><option value='request' selected>Входящие</option>")
		} else {
			sb.WriteString("<option value='http' selected>Исходящие</option><option value='request'>Входящие</option>")
		}
		sb.WriteString("</select></label>")
		sb.WriteString("<label for='patternInput'>Фильтр: </label>")
		sb.WriteString("<input type='text' id='patternInput' placeholder='rezka.ag' value='" + htmlEscape(pattern) + "' style='margin-right:20px;' />")
		sb.WriteString("</div><div id='log'></div>")
		sb.WriteString("<script src='/js/nws-client-es5.js'></script><script src='/signalr-6.0.25_es5.js'></script>")
		sb.WriteString("<script>")
		sb.WriteString("let pattern=document.getElementById('patternInput').value.trim();")
		sb.WriteString("let receive=document.getElementById('receiveSelect').value;")
		sb.WriteString("document.getElementById('patternInput').addEventListener('input',e=>pattern=e.target.value.trim());")
		sb.WriteString("document.getElementById('receiveSelect').addEventListener('change',e=>receive=e.target.value);")
		sb.WriteString("function send(m){if(pattern&&m.indexOf(pattern)===-1)return;var p=document.getElementById('log');var hr=document.createElement('hr');hr.style.cssText='margin-bottom:2.5em;margin-top:2.5em;';p.insertBefore(hr,p.children[0]);var pre=document.createElement('pre');pre.style.cssText='padding:10px;background:cornsilk;white-space:pre-wrap;word-wrap:break-word;';pre.innerText=m;p.insertBefore(pre,p.children[0]);}")
		sb.WriteString("let outageReported=false;function reportOutageOnce(m){if(!outageReported){send(m);outageReported=true;}}")
		sb.WriteString("if(window.NativeWsClient){const client=new NativeWsClient('/nws',{autoReconnect:true,reconnectDelay:2000,onOpen:function(){send('WebSocket connected');outageReported=false;client.invoke('RegistryWebLog','" + jsEscape(token) + "');},onClose:function(){reportOutageOnce('Connection closed');},onError:function(err){reportOutageOnce('Connection error: '+(err&&err.message?err.message:String(err)));}});client.on('Receive',function(message,e){if(receive===e)send(message);});client.connect();}")
		sb.WriteString("else if(window.signalR){const hubConnection=new signalR.HubConnectionBuilder().withUrl('/ws').build();function start(){hubConnection.start().then(function(){hubConnection.invoke('RegistryWebLog','" + jsEscape(token) + "');}).catch(function(){setTimeout(start,2000);});}hubConnection.on('Receive',function(message,e){if(receive===e)send(message);});hubConnection.onclose(function(){setTimeout(start,2000);});start();}")
		sb.WriteString("else{send('WebSocket client is unavailable');}")
		sb.WriteString("</script></body></html>")

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sb.String()))
	}
}

func loadWeblogSettings() weblogSettings {
	data, ok := readFileAny("init.conf")
	if !ok {
		return weblogSettings{}
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return weblogSettings{}
	}

	node, ok := root["weblog"].(map[string]any)
	if !ok {
		return weblogSettings{}
	}

	return weblogSettings{
		Enable: toBool(node["enable"]),
		Token:  strings.TrimSpace(toString(node["token"])),
	}
}

func htmlEscape(v string) string {
	v = strings.ReplaceAll(v, "&", "&amp;")
	v = strings.ReplaceAll(v, "<", "&lt;")
	v = strings.ReplaceAll(v, ">", "&gt;")
	v = strings.ReplaceAll(v, `"`, "&quot;")
	v = strings.ReplaceAll(v, `'`, "&#39;")
	return v
}

func jsEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	v = strings.ReplaceAll(v, "\n", "")
	v = strings.ReplaceAll(v, "\r", "")
	return v
}
