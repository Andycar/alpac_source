package httpapi

import (
	stdjson "encoding/json" // only for the RawMessage type; the package `json` var (jsoniter) does the (un)marshalling
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/logbuf"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// Incident reporting. The ALPAC client packages a device snapshot + its recent weblog and POSTs it
// to /lite/incident when the user taps «Сообщить о проблеме». The server stamps who/when, pulls the
// matching lines from its OWN log ring (logbuf) — correlated by the client IP and the weblog session
// id — and keeps the whole bundle so an admin can study it on cp_path/incidents without grepping.

// Incident is one report. Device + ClientLogs are pass-through JSON (built by the client), kept raw
// so the server never has to model the client's shape.
type Incident struct {
	ID         string             `json:"id"`
	RT         int64              `json:"rt"` // server receive time (ms)
	IP         string             `json:"ip"`
	UID        string             `json:"uid"`
	UA         string             `json:"ua"`     // short, derived from the device UA
	Sid        string             `json:"sid"`    // weblog session id (groups one page-load)
	UserTG     int64              `json:"-"`      // resolved Telegram id (for admin→user reply); not serialized
	Reason     string             `json:"reason"` // display | playback | sources | auth | nav | other
	Note       string             `json:"note"`
	Platform   string             `json:"platform"`
	Summary    string             `json:"summary"`              // one-line geometry digest for the list view
	Device     stdjson.RawMessage `json:"device"`               // the full device snapshot
	ClientLogs stdjson.RawMessage `json:"client_logs"`          // the client weblog at incident time
	ServerLogs []logbuf.Entry     `json:"server_logs"`          // server log lines correlated to this client
	Screenshot string             `json:"screenshot,omitempty"` // optional native-bridge screenshot (data-URL)
	Status     string             `json:"status"`               // new | answered (admin replied via TG)
	Reply      string             `json:"reply,omitempty"`      // admin's reply text, relayed to the user
	RepliedAt  int64              `json:"replied_at,omitempty"` // ms, when the admin replied
}

const incidentStoreMax = 120 // bounded ring — newest kept

var (
	incidentMu       sync.Mutex
	incidentBuf      []Incident
	incidentLoadOnce sync.Once
)

const incidentDiskKeep = 1000 // bounded archive on disk

func incidentsDir() string { return relToRuntime(filepath.Join("database", "incidents")) }

// incidentLoadFromDisk repopulates the in-memory ring from the archive on first use, so incidents
// survive a restart. Ids are timestamp-prefixed → lexically sorted = chronological.
func incidentLoadFromDisk() {
	entries, _ := os.ReadDir(incidentsDir())
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > incidentStoreMax {
		names = names[len(names)-incidentStoreMax:]
	}
	incidentMu.Lock()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(incidentsDir(), n))
		if err != nil {
			continue
		}
		var in Incident
		if json.Unmarshal(b, &in) == nil && in.ID != "" {
			incidentBuf = append(incidentBuf, in)
		}
	}
	incidentMu.Unlock()
}

// incidentPersist writes one incident to disk (best-effort) and trims the archive.
func incidentPersist(in Incident) {
	dir := incidentsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if b, err := json.Marshal(in); err == nil {
		_ = os.WriteFile(filepath.Join(dir, in.ID+".json"), b, 0o644)
	}
	entries, _ := os.ReadDir(dir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) > incidentDiskKeep {
		sort.Strings(names)
		for _, n := range names[:len(names)-incidentDiskKeep] {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

func incidentAppend(in Incident) {
	incidentLoadOnce.Do(incidentLoadFromDisk)
	incidentMu.Lock()
	incidentBuf = append(incidentBuf, in)
	if len(incidentBuf) > incidentStoreMax {
		incidentBuf = incidentBuf[len(incidentBuf)-incidentStoreMax:]
	}
	incidentMu.Unlock()
	incidentPersist(in)
}

func incidentList() []Incident {
	incidentLoadOnce.Do(incidentLoadFromDisk)
	incidentMu.Lock()
	defer incidentMu.Unlock()
	out := make([]Incident, 0, len(incidentBuf))
	// newest first, metadata only (drop the heavy raw blobs from the list)
	for i := len(incidentBuf) - 1; i >= 0; i-- {
		e := incidentBuf[i]
		out = append(out, Incident{ID: e.ID, RT: e.RT, IP: e.IP, UID: e.UID, UA: e.UA, Sid: e.Sid, Reason: e.Reason, Note: e.Note, Platform: e.Platform, Summary: e.Summary})
	}
	return out
}

func incidentGet(id string) (Incident, bool) {
	incidentLoadOnce.Do(incidentLoadFromDisk)
	incidentMu.Lock()
	defer incidentMu.Unlock()
	for i := len(incidentBuf) - 1; i >= 0; i-- {
		if incidentBuf[i].ID == id {
			return incidentBuf[i], true
		}
	}
	return Incident{}, false
}

func incidentClear() {
	incidentMu.Lock()
	incidentBuf = nil
	incidentMu.Unlock()
	_ = os.RemoveAll(incidentsDir()) // drop the on-disk archive too
}

// incidentMarkAnswered records an admin's TG reply on the matching incident (status → answered) so the
// user's «Мои обращения» list reflects it. Wired to the bot's admin→user reply relay (bot_reply.go).
func incidentMarkAnswered(id, reply string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	incidentLoadOnce.Do(incidentLoadFromDisk)
	incidentMu.Lock()
	var updated *Incident
	for i := range incidentBuf {
		if incidentBuf[i].ID == id {
			incidentBuf[i].Status = "answered"
			incidentBuf[i].Reply = reply
			incidentBuf[i].RepliedAt = time.Now().UnixMilli()
			cp := incidentBuf[i]
			updated = &cp
			break
		}
	}
	incidentMu.Unlock()
	if updated != nil {
		incidentPersist(*updated)
	}
}

// incidentStatusByIDs returns lightweight status records for the requested ids. The client proves
// ownership simply by knowing its own incident ids (stored locally) — no user matching needed, so it
// works for guests too.
func incidentStatusByIDs(ids []string) map[string]map[string]any {
	incidentLoadOnce.Do(incidentLoadFromDisk)
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	out := make(map[string]map[string]any, len(want))
	incidentMu.Lock()
	for i := range incidentBuf {
		e := incidentBuf[i]
		if !want[e.ID] {
			continue
		}
		st := e.Status
		if st == "" {
			st = "new"
		}
		rec := map[string]any{"status": st, "rt": e.RT, "reason": e.Reason}
		if e.Reply != "" {
			rec["reply"] = e.Reply
			rec["replied_at"] = e.RepliedAt
		}
		out[e.ID] = rec
	}
	incidentMu.Unlock()
	return out
}

// incidentStatusHandler: GET /lite/incident/status?ids=a,b,c — status of the caller's OWN incident ids
// (the client lists them from localStorage). Powers «Мои обращения» shown before a new report.
func incidentStatusHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Web.WeblogCollect {
			writeJSON(w, http.StatusOK, map[string]map[string]any{})
			return
		}
		ids := strings.Split(r.URL.Query().Get("ids"), ",")
		if len(ids) > 50 {
			ids = ids[:50]
		}
		writeJSON(w, http.StatusOK, incidentStatusByIDs(ids))
	}
}

// correlateServerLogs pulls recent server log lines that mention this client — by IP or by the
// weblog session id (which the forwarded weblog lines carry). Server-internal lines (balancer
// drills, transcoding) rarely tag the IP, so we also keep the most recent errors/warns for context.
func correlateServerLogs(buf *logbuf.Buffer, ip, sid string) []logbuf.Entry {
	if buf == nil {
		return nil
	}
	recent := buf.Query(logbuf.Filter{Limit: 600}) // newest-first
	out := make([]logbuf.Entry, 0, 120)
	for _, e := range recent {
		if (ip != "" && strings.Contains(e.Raw, ip)) || (sid != "" && strings.Contains(e.Raw, sid)) {
			out = append(out, e)
			if len(out) >= 120 {
				break
			}
		}
	}
	// pad with recent errors/warns for server-side context if the user-matched set is thin
	if len(out) < 40 {
		for _, e := range recent {
			if e.Level == "error" || e.Level == "warn" {
				dup := false
				for _, x := range out {
					if x.Seq == e.Seq {
						dup = true
						break
					}
				}
				if !dup {
					out = append(out, e)
				}
			}
			if len(out) >= 60 {
				break
			}
		}
	}
	return out
}

// Mark the linked incident «answered» when an admin replies to its TG notification. The bot relays the
// reply to the user (bot_reply.go) and invokes this with the notification label «инцидент #<id>».
func init() {
	tgauth.AdminReplyHook = func(label, text string) {
		if !strings.HasPrefix(label, "инцидент") {
			return // some other relay subject (e.g. a feedback ticket) — not ours
		}
		if i := strings.Index(label, "#"); i >= 0 {
			incidentMarkAnswered(strings.TrimSpace(label[i+1:]), clampStr(text, 800))
		}
	}
}

// incidentReceiveHandler handles POST /lite/incident from the client. Gated by the same flag as the
// weblog receiver (it's the same diagnostics channel). Rate-limited per IP.
func incidentReceiveHandler(cfg config.Config, buf *logbuf.Buffer, bot *tgauth.Bot, store *tgauth.Store) http.HandlerFunc {
	limiter := newWeblogLimiter() // reuse the per-IP token bucket
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Web.WeblogCollect {
			http.NotFound(w, r)
			return
		}
		if limiter.allow(clientIP(r), 6) < 1 { // ≤ a handful of incidents per burst/IP
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var p struct {
			Sid        string             `json:"sid"`
			Reason     string             `json:"reason"`
			Note       string             `json:"note"`
			Device     stdjson.RawMessage `json:"device"`
			Logs       stdjson.RawMessage `json:"logs"`
			Screenshot string             `json:"screenshot"` // optional native-bridge data-URL
		}
		// 5MB: a native screenshot (base64 data-URL) is the heavy payload; logs+device stay small.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 5*1024*1024)).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ip := clientIP(r)
		id := incidentID()
		userTG := incidentResolveUserTG(r, store)
		in := Incident{
			ID:         id,
			RT:         time.Now().UnixMilli(),
			IP:         ip,
			UID:        incidentUID(r),
			UA:         shortUA(r.UserAgent()),
			Sid:        e2sid(p.Sid),
			UserTG:     userTG,
			Reason:     clampStr(p.Reason, 24),
			Note:       clampStr(p.Note, 500),
			Platform:   incidentDeviceField(p.Device, "platform"),
			Summary:    incidentGeometrySummary(p.Device),
			Device:     p.Device,
			ClientLogs: p.Logs,
			ServerLogs: correlateServerLogs(buf, ip, e2sid(p.Sid)),
			Screenshot: sanitizeDataURLImage(p.Screenshot),
			Status:     "new",
		}
		incidentAppend(in)
		log.Warn().Str("incident", id).Str("ip", ip).Str("reason", in.Reason).Str("platform", in.Platform).
			Str("geom", in.Summary).Str("note", in.Note).Int64("user_tg", userTG).Msg("incident: client report received")
		// Notify admins in Telegram. NotifyUserContext lets the admin REPLY to the message to answer
		// the user directly (bot_reply.go). When the user isn't TG-linked, it's a plain alert.
		if bot != nil {
			bot.NotifyUserContext(incidentBotText(in), userTG, "инцидент #"+id)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	}
}

func incidentID() string {
	return time.Now().UTC().Format("0102-150405") + "-" + randomAlphaNum(3)
}

// incidentUID reads the lampac auth cookie (token) prefix as a stable user hint — full token is
// never stored. Best-effort: empty when the client is anonymous.
func incidentUID(r *http.Request) string {
	for _, name := range []string{"alpac_token", "lampac_token", "_lampac_auth"} {
		if c, err := r.Cookie(name); err == nil {
			v := strings.TrimSpace(c.Value)
			if len(v) >= 8 {
				return v[:8]
			}
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func clampStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// sanitizeDataURLImage accepts only a base64 image data-URL (png/jpeg/webp), capped, so the admin
// page can render it as <img src> without smuggling scripts or arbitrary content. Anything else → "".
func sanitizeDataURLImage(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > 4*1024*1024 { // ~3MB binary after base64
		return ""
	}
	low := strings.ToLower(s)
	ok := strings.HasPrefix(low, "data:image/png;base64,") ||
		strings.HasPrefix(low, "data:image/jpeg;base64,") ||
		strings.HasPrefix(low, "data:image/jpg;base64,") ||
		strings.HasPrefix(low, "data:image/webp;base64,")
	if !ok {
		return ""
	}
	return s
}

// incidentDeviceField pulls one string field out of the raw device snapshot without modelling it.
func incidentDeviceField(dev stdjson.RawMessage, key string) string {
	if len(dev) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(dev, &m) != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// incidentGeometrySummary builds the «innerWxH dpr zoom» digest that makes display bugs obvious in
// the list, straight from the device snapshot.
func incidentGeometrySummary(dev stdjson.RawMessage) string {
	if len(dev) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(dev, &m) != nil {
		return ""
	}
	num := func(k string) string {
		if v, ok := m[k].(float64); ok {
			if v == float64(int64(v)) {
				return strconv.FormatInt(int64(v), 10)
			}
			return strconv.FormatFloat(v, 'g', 3, 64)
		}
		return "?"
	}
	str := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	parts := []string{"inner " + num("innerW") + "x" + num("innerH"), "screen " + num("screenW") + "x" + num("screenH"), "dpr " + num("dpr")}
	if z := str("zoom"); z != "" {
		parts = append(parts, "zoom "+z)
	}
	if s := str("uiScale"); s != "" {
		parts = append(parts, "ui "+s)
	}
	return strings.Join(parts, " · ")
}

// incidentResolveUserTG maps the request's auth cookie to a Telegram id so the admin can reply to
// the user from the bot. 0 when the client is anonymous / not TG-linked.
func incidentResolveUserTG(r *http.Request, store *tgauth.Store) int64 {
	if store == nil {
		return 0
	}
	for _, tok := range collectLampacTokenCandidates(r) {
		if tid, _, ok := store.LookupAuth(tok); ok && tid != 0 {
			return tid
		}
	}
	return 0
}

func incidentReasonRU(r string) string {
	switch r {
	case "display":
		return "🖼 криво отображается"
	case "playback":
		return "▶ не воспроизводится"
	case "sources":
		return "🔌 нет источников"
	case "auth":
		return "🔑 не входит / вылетает"
	case "nav":
		return "🎮 навигация"
	case "other":
		return "❓ другое"
	}
	return r
}

func incidentBotText(in Incident) string {
	var sb strings.Builder
	sb.WriteString("🆘 <b>Инцидент #" + in.ID + "</b>\n")
	sb.WriteString("Причина: " + incidentReasonRU(in.Reason) + "\n")
	if in.Platform != "" {
		sb.WriteString("Устройство: " + in.Platform + "\n")
	}
	if in.Summary != "" {
		sb.WriteString("Гео: <code>" + in.Summary + "</code>\n")
	}
	if in.Note != "" {
		sb.WriteString("Заметка: " + in.Note + "\n")
	}
	sb.WriteString("IP: " + in.IP)
	if in.UserTG == 0 {
		sb.WriteString("\n<i>(не привязан к Telegram — ответить нельзя)</i>")
	}
	return sb.String()
}

// ── admin side ──

func incidentAdminAPIHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
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
				incidentClear()
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if id := r.URL.Query().Get("id"); id != "" {
			if in, ok := incidentGet(id); ok {
				writeJSON(w, http.StatusOK, in)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"incidents": incidentList()})
	}
}

func incidentAdminPageHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		writeHTML(w, http.StatusOK, incidentPageHTML)
	}
}

const incidentPageHTML = `<!doctype html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>incidents</title>
<style>
  :root{color-scheme:dark}*{box-sizing:border-box}
  body{margin:0;background:#0a0b10;color:#c3c7d6;font:13px/1.5 'SF Mono',Menlo,Consolas,monospace}
  header{position:sticky;top:0;z-index:5;display:flex;gap:10px;align-items:center;padding:10px 14px;background:#11131a;border-bottom:1px solid #20242f}
  header b{font-size:16px;color:#fff}.count{color:#6b7186}.spacer{flex:1}
  button{background:#1a1d27;color:#dfe2ee;border:1px solid #2a2f3d;border-radius:7px;padding:6px 10px;cursor:pointer;font:inherit}
  .wrap{display:flex;height:calc(100vh - 49px)}
  .list{width:380px;overflow:auto;border-right:1px solid #20242f;flex:0 0 auto}
  .it{padding:9px 12px;border-bottom:1px solid #14161d;cursor:pointer}
  .it:hover{background:#14161d}.it.sel{background:#1a1f2e}
  .it .r{display:flex;gap:8px;align-items:baseline}
  .badge{padding:1px 7px;border-radius:6px;font-size:11px;font-weight:700;background:#2a2f3d;color:#dfe2ee}
  .badge.display{background:rgba(255,180,60,.25);color:#ffd479}
  .badge.playback{background:rgba(124,92,255,.25);color:#c3b3ff}
  .badge.auth{background:rgba(255,60,60,.2);color:#ff8a8a}
  .it .geom{color:#7f8699;font-size:12px;margin-top:2px}
  .it .meta{color:#5b6076;font-size:11px;margin-top:2px}
  .detail{flex:1;overflow:auto;padding:14px 18px}
  .sec{margin:14px 0 6px;color:#a78bfa;font-weight:700;font-size:13px;border-bottom:1px solid #20242f;padding-bottom:4px}
  img.shot{max-width:100%;max-height:420px;border:1px solid #2a2f3d;border-radius:8px;background:#000;display:block}
  table.kv{border-collapse:collapse;width:100%}
  table.kv td{padding:2px 10px 2px 0;vertical-align:top}
  table.kv td.k{color:#6b7186;width:160px}
  .logline{padding:1px 0;word-break:break-word}
  .logline.error{color:#ff8a8a}.logline.warn{color:#ffd479}.logline.event{color:#8fd0ff}
  .lt{color:#5b6076}.empty{color:#5b6076;padding:30px}
</style></head><body>
<header><b>incidents</b><span class="count" id="cnt">0</span>
  <button id="refresh">обновить</button><button id="clr">очистить</button>
  <span class="spacer"></span><span class="count">клиентские отчёты + снимок устройства + серверные логи по клиенту</span>
</header>
<div class="wrap">
  <div class="list" id="list"></div>
  <div class="detail" id="detail"><div class="empty">выберите инцидент слева</div></div>
</div>
<script>
(function(){
  var list=document.getElementById('list'),detail=document.getElementById('detail'),cnt=document.getElementById('cnt');
  var cur=null;
  function esc(s){return (s==null?'':String(s)).replace(/[&<>]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;'}[c]})}
  function ft(ms){var d=new Date(ms);function p(n){return (n<10?'0':'')+n}return p(d.getDate())+'.'+p(d.getMonth()+1)+' '+p(d.getHours())+':'+p(d.getMinutes())+':'+p(d.getSeconds())}
  function loadList(){
    fetch('api/incidents',{credentials:'include'}).then(function(r){return r.json()}).then(function(j){
      var arr=j.incidents||[];cnt.textContent=arr.length;
      list.innerHTML=arr.map(function(e){
        return '<div class="it'+(e.id===cur?' sel':'')+'" data-id="'+esc(e.id)+'">'+
          '<div class="r"><span class="badge '+esc(e.reason)+'">'+esc(e.reason||'?')+'</span>'+
          '<span>'+esc(e.platform||'?')+'</span><span class="lt">'+ft(e.rt)+'</span></div>'+
          '<div class="geom">'+esc(e.summary||'')+'</div>'+
          '<div class="meta">'+esc(e.ip)+(e.uid?' · '+esc(e.uid):'')+(e.note?' · «'+esc(e.note)+'»':'')+'</div>'+
        '</div>';
      }).join('')||'<div class="empty">пусто</div>';
    }).catch(function(){});
  }
  function kv(obj){var r='';for(var k in obj){r+='<tr><td class="k">'+esc(k)+'</td><td>'+esc(typeof obj[k]==='object'?JSON.stringify(obj[k]):obj[k])+'</td></tr>'}return '<table class="kv">'+r+'</table>'}
  function loadDetail(id){
    cur=id;loadList();
    fetch('api/incidents?id='+encodeURIComponent(id),{credentials:'include'}).then(function(r){return r.json()}).then(function(e){
      var h='';
      h+='<div class="sec">Инцидент '+esc(e.id)+' · '+esc(e.reason)+(e.note?' · «'+esc(e.note)+'»':'')+'</div>';
      h+=kv({время:ft(e.rt),ip:e.ip,uid:e.uid,ua:e.ua,sid:e.sid});
      if(e.screenshot){h+='<div class="sec">Скриншот экрана</div><img class="shot" alt="скриншот" src="'+e.screenshot+'">';}
      h+='<div class="sec">Устройство (снимок)</div>'+(e.device?kv(e.device):'<div class="empty">нет</div>');
      h+='<div class="sec">Логи клиента ('+((e.client_logs||[]).length)+')</div>';
      h+=(e.client_logs||[]).map(function(l){return '<div class="logline '+esc(l.lvl)+'"><span class="lt">'+(l.t?ft(l.t):'')+'</span> ['+esc(l.tag)+'] '+esc(l.msg)+(l.data?' '+esc(l.data):'')+'</div>'}).join('')||'<div class="empty">нет</div>';
      h+='<div class="sec">Серверные логи по этому клиенту ('+((e.server_logs||[]).length)+')</div>';
      h+=(e.server_logs||[]).map(function(l){return '<div class="logline '+esc(l.level)+'"><span class="lt">'+esc((l.time||'').slice(11,23))+'</span> '+esc(l.message)+'</div>'}).join('')||'<div class="empty">нет совпадений по IP/sid</div>';
      detail.innerHTML=h;
    }).catch(function(){});
  }
  list.addEventListener('click',function(ev){var it=ev.target.closest('.it');if(it)loadDetail(it.getAttribute('data-id'))});
  document.getElementById('refresh').onclick=function(){loadList();if(cur)loadDetail(cur)};
  document.getElementById('clr').onclick=function(){if(confirm('Очистить все инциденты?'))fetch('api/incidents',{method:'POST',credentials:'include',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'clear'})}).then(function(){cur=null;detail.innerHTML='<div class="empty">очищено</div>';loadList()})};
  loadList();setInterval(loadList,5000);
})();
</script></body></html>`
