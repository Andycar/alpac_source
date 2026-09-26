package httpapi

import (
	"bufio"
	stdjson "encoding/json" // RawMessage; (де)сериализует пакетный json (jsoniter)
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// Матрица диагностики сети.
//
// Клиент (Android TV; веб и tvOS подключатся тем же контрактом) по событию — столл плеера,
// отвал основного хоста, «Сообщить о проблеме», раз в несколько часов фоном — прогоняет по
// нашим хостам (API-зеркала, ноды отдачи, хост текущего потока, пара эталонов) ОТДЕЛЬНЫЕ шаги
// DNS → TCP → TLS → HTTP и присылает результат сюда. Сервер дописывает провайдера (ASN по IP,
// с кэшем по /24) и складывает всё в кольцо + суточные jsonl; админка cp_path/netdiag сводит
// это в таблицу «провайдер × хост × шаг».
//
// Зачем именно шаги, а не «работает/не работает»: провайдеры режут по-разному — RST после
// ClientHello (упадёт TLS при живом TCP), DPI по SNI на отдельные хосты, чёрная дыра по IP
// (TCP-таймаут), подмена DNS. Каждый способ лечится своим приёмом, и без матрицы мы выбираем
// лекарство вслепую. Плюс к столлам плеер прикладывает сводку StreamDiag — ленту просадок с
// вердиктом «балансер / торрент / сеть / декодер», чтобы «в 12:41 всё упало» имело причину.

// NetDiagRow — одна строка матрицы: один хост, четыре шага. Мс ≥ 0 — шаг прошёл; -1 — упал;
// -2 — не выполнялся (предыдущий упал или шаг неприменим, например TLS у http://).
type NetDiagRow struct {
	Host string `json:"host"`           // как пробовали: "https://tv.example.com", "https://edge-a.example.org"
	Role string `json:"role"`           // api | edge | stream | ref
	DNS  int    `json:"dns"`            // мс до адреса
	TCP  int    `json:"tcp"`            // мс до connect
	TLS  int    `json:"tls"`            // мс рукопожатия (RST по SNI ловится именно тут)
	HTTP int    `json:"http"`           // мс до первого байта ответа
	Code int    `json:"code,omitempty"` // HTTP-статус (0 = ответа не было)
	Kbps int    `json:"kbps,omitempty"` // короткий замер полосы (0 = не мерили)
	IP   string `json:"ip,omitempty"`   // куда резолвилось
	Err  string `json:"err,omitempty"`  // ошибка первого упавшего шага, коротко
}

// NetDiagReport — один прогон матрицы с одного устройства.
type NetDiagReport struct {
	ID       string             `json:"id"`
	RT       int64              `json:"rt"` // приём сервером, мс
	IP       string             `json:"ip"`
	ASN      string             `json:"asn"` // "AS12389 PJSC Rostelecom" — по ip-api, с кэшем
	ISP      string             `json:"isp"`
	Country  string             `json:"country"` // код страны
	City     string             `json:"city"`
	UA       string             `json:"ua"`
	Sid      string             `json:"sid"`
	UID      string             `json:"uid"`
	Platform string             `json:"platform"`
	Reason   string             `json:"reason"`  // stall | failover | periodic | manual | http_error
	Net      string             `json:"net"`     // wifi | ethernet | cellular | vpn | ?
	Source   string             `json:"source"`  // balancer | torrent | iptv | youtube… (при столле)
	Context  string             `json:"context"` // свободная строка клиента: тайтл, нода, вердикт
	Rows     []NetDiagRow       `json:"rows"`
	Stream   stdjson.RawMessage `json:"stream,omitempty"` // сводка StreamDiag плеера, как есть
}

// netDiagPayload — что присылает клиент (всё остальное сервер ставит сам).
type netDiagPayload struct {
	Sid      string             `json:"sid"`
	UA       string             `json:"ua"`
	UID      string             `json:"uid"`
	Platform string             `json:"platform"`
	Reason   string             `json:"reason"`
	Net      string             `json:"net"`
	Source   string             `json:"source"`
	Context  string             `json:"context"`
	Rows     []NetDiagRow       `json:"rows"`
	Stream   stdjson.RawMessage `json:"stream"`
}

const (
	netDiagRingMax   = 3000      // отчётов в памяти (≈ неделя при типичном потоке)
	netDiagDiskDays  = 14        // суточных jsonl на диске
	netDiagRowsMax   = 40        // строк в одном отчёте
	netDiagStreamMax = 24 * 1024 // сводка плеера, байт
	netDiagAPIMax    = 1500      // отчётов в ответе админского API
	asnTTL           = 7 * 24 * time.Hour
)

var (
	netDiagMu        sync.Mutex
	netDiagBuf       []NetDiagReport
	netDiagOnce      sync.Once
	netDiagPrunedDay string // YYYY-MM-DD, когда последний раз чистили диск
)

func netDiagDir() string { return relToRuntime(filepath.Join("database", "netdiag")) }

// netDiagLoad поднимает кольцо из суточных файлов на старте: свежие файлы первыми, пока не
// наберём netDiagRingMax. Порядок в кольце — хронологический (старые в начале).
func netDiagLoad() {
	entries, _ := os.ReadDir(netDiagDir())
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var all []NetDiagReport
	for i := len(names) - 1; i >= 0 && len(all) < netDiagRingMax; i-- {
		f, err := os.Open(filepath.Join(netDiagDir(), names[i]))
		if err != nil {
			continue
		}
		var day []NetDiagReport
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 512*1024)
		for sc.Scan() {
			var rep NetDiagReport
			if json.Unmarshal(sc.Bytes(), &rep) == nil && rep.ID != "" {
				day = append(day, rep)
			}
		}
		f.Close()
		all = append(day, all...) // файл старше — вперёд
	}
	if len(all) > netDiagRingMax {
		all = all[len(all)-netDiagRingMax:]
	}
	netDiagMu.Lock()
	netDiagBuf = all
	netDiagMu.Unlock()
}

// netDiagAppend кладёт отчёт в кольцо и дописывает строкой в файл текущего дня; раз в сутки
// подчищает файлы старше netDiagDiskDays.
func netDiagAppend(rep NetDiagReport) {
	netDiagOnce.Do(netDiagLoad)
	netDiagMu.Lock()
	netDiagBuf = append(netDiagBuf, rep)
	if len(netDiagBuf) > netDiagRingMax {
		netDiagBuf = netDiagBuf[len(netDiagBuf)-netDiagRingMax:]
	}
	day := time.UnixMilli(rep.RT).UTC().Format("2006-01-02")
	prune := netDiagPrunedDay != day
	netDiagPrunedDay = day
	netDiagMu.Unlock()

	dir := netDiagDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if b, err := json.Marshal(rep); err == nil {
		if f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.Write(append(b, '\n'))
			f.Close()
		}
	}
	if prune {
		cut := time.Now().UTC().AddDate(0, 0, -netDiagDiskDays).Format("2006-01-02")
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".jsonl") && strings.TrimSuffix(n, ".jsonl") < cut {
				_ = os.Remove(filepath.Join(dir, n))
			}
		}
	}
}

// netDiagList — отчёты не старше since (мс), свежие первыми, не больше max.
func netDiagList(since int64, max int) []NetDiagReport {
	netDiagOnce.Do(netDiagLoad)
	netDiagMu.Lock()
	defer netDiagMu.Unlock()
	out := make([]NetDiagReport, 0, 64)
	for i := len(netDiagBuf) - 1; i >= 0 && len(out) < max; i-- {
		if netDiagBuf[i].RT < since {
			break // кольцо хронологическое — дальше только старее
		}
		out = append(out, netDiagBuf[i])
	}
	return out
}

func netDiagClear() {
	netDiagOnce.Do(netDiagLoad)
	netDiagMu.Lock()
	netDiagBuf = nil
	netDiagMu.Unlock()
	entries, _ := os.ReadDir(netDiagDir())
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			_ = os.Remove(filepath.Join(netDiagDir(), e.Name()))
		}
	}
}

// ── провайдер по IP ──

type asnInfo struct {
	ASN, ISP, Country, City string
	at                      time.Time
}

var (
	asnMu    sync.Mutex
	asnCache = map[string]asnInfo{} // ключ — /24 (v4) или /48 (v6): один провайдер на префикс
)

func asnKey(ip string) string {
	p := net.ParseIP(ip)
	if p == nil {
		return ""
	}
	if v4 := p.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String()
	}
	return p.Mask(net.CIDRMask(48, 128)).String()
}

func isPublicIP(ip string) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	return !(p.IsLoopback() || p.IsPrivate() || p.IsLinkLocalUnicast() || p.IsUnspecified())
}

// lookupASN — провайдер по ip-api.com (бесплатный тариф: 45 запросов/мин; отчёты идут реже,
// и кэш по префиксу режет повторы). Пусто — если приватный адрес или сервис не ответил.
func lookupASN(ip string) asnInfo {
	key := asnKey(ip)
	if key == "" || !isPublicIP(ip) {
		return asnInfo{}
	}
	asnMu.Lock()
	if v, ok := asnCache[key]; ok && time.Since(v.at) < asnTTL {
		asnMu.Unlock()
		return v
	}
	asnMu.Unlock()

	client := httpclient.New(4 * time.Second)
	resp, err := client.Get("http://ip-api.com/json/" + ip + "?fields=status,countryCode,city,isp,as")
	if err != nil {
		return asnInfo{}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var g struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
		City        string `json:"city"`
		ISP         string `json:"isp"`
		AS          string `json:"as"`
	}
	if stdjson.Unmarshal(body, &g) != nil || g.Status != "success" {
		return asnInfo{}
	}
	v := asnInfo{ASN: g.AS, ISP: g.ISP, Country: g.CountryCode, City: g.City, at: time.Now()}
	asnMu.Lock()
	if len(asnCache) > 50_000 {
		asnCache = map[string]asnInfo{}
	}
	asnCache[key] = v
	asnMu.Unlock()
	return v
}

// ── приём ──

// netDiagFailSummary — «tcp:edge-a.example.org tls:edge-b.example.net» для лога: чтобы journalctl отвечал на вопрос
// «что у него упало» без похода в админку.
func netDiagFailSummary(rows []NetDiagRow) string {
	parts := make([]string, 0, 4)
	for _, r := range rows {
		step := ""
		switch {
		case r.DNS == -1:
			step = "dns"
		case r.TCP == -1:
			step = "tcp"
		case r.TLS == -1:
			step = "tls"
		case r.HTTP == -1:
			step = "http"
		case r.Code >= 500:
			step = "http" + strconv.Itoa(r.Code)
		}
		if step == "" {
			continue
		}
		host := r.Host
		if u := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"); u != "" {
			host = u
		}
		parts = append(parts, step+":"+host)
		if len(parts) >= 6 {
			break
		}
	}
	if len(parts) == 0 {
		return "ok"
	}
	return strings.Join(parts, " ")
}

// netDiagReceiveHandler — POST /lite/netdiag. Тот же гейт и лимитер, что у weblog: матрица —
// это телеметрия, не пользовательское действие, и включается/выключается вместе с ней.
func netDiagReceiveHandler(cfg config.Config) http.HandlerFunc {
	limiter := newWeblogLimiter()
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Web.WeblogCollect {
			http.NotFound(w, r)
			return
		}
		ip := clientIP(r)
		if limiter.allow(ip, 4) < 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var p netDiagPayload
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(p.Rows) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(p.Rows) > netDiagRowsMax {
			p.Rows = p.Rows[:netDiagRowsMax]
		}
		for i := range p.Rows {
			p.Rows[i].Host = clampStr(p.Rows[i].Host, 120)
			p.Rows[i].Role = clampStr(p.Rows[i].Role, 12)
			p.Rows[i].IP = clampStr(p.Rows[i].IP, 64)
			p.Rows[i].Err = clampStr(p.Rows[i].Err, 160)
		}
		var stream stdjson.RawMessage
		if len(p.Stream) > 0 && len(p.Stream) <= netDiagStreamMax && stdjson.Valid(p.Stream) {
			stream = p.Stream
		}
		uid := clampStr(p.UID, 40)
		if uid == "" {
			uid = incidentUID(r)
		}
		rep := NetDiagReport{
			ID:       incidentID(),
			RT:       time.Now().UnixMilli(),
			IP:       ip,
			UA:       shortUA(p.UA),
			Sid:      e2sid(p.Sid),
			UID:      uid,
			Platform: clampStr(p.Platform, 24),
			Reason:   clampStr(p.Reason, 24),
			Net:      clampStr(p.Net, 16),
			Source:   clampStr(p.Source, 24),
			Context:  clampStr(p.Context, 300),
			Rows:     p.Rows,
			Stream:   stream,
		}
		a := lookupASN(ip)
		rep.ASN, rep.ISP, rep.Country, rep.City = a.ASN, a.ISP, a.Country, a.City
		netDiagAppend(rep)
		// В общий лог — по ip/sid его подберёт correlateServerLogs к инциденту того же зрителя.
		log.Info().Str("src", "netdiag").Str("ip", ip).Str("sid", rep.Sid).Str("asn", rep.ASN).
			Str("city", rep.City).Str("reason", rep.Reason).Str("net", rep.Net).Str("source", rep.Source).
			Str("fails", netDiagFailSummary(rep.Rows)).Msg("netdiag: матрица " + rep.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": rep.ID})
	}
}

// ── админка ──

// netDiagAdminAPIHandler: GET ?hours=24 — отчёты за окно (свежие первыми, ≤ netDiagAPIMax),
// сводку по провайдерам/хостам собирает страница; POST {"action":"clear"} — очистить.
func netDiagAdminAPIHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
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
				netDiagClear()
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
			return
		}
		hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
		if hours <= 0 {
			hours = 24
		}
		if hours > 24*30 {
			hours = 24 * 30
		}
		since := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
		writeJSON(w, http.StatusOK, map[string]any{
			"since":   since,
			"reports": netDiagList(since, netDiagAPIMax),
		})
	}
}

func netDiagAdminPageHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		writeHTML(w, http.StatusOK, netDiagPageHTML)
	}
}

const netDiagPageHTML = `<!doctype html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>netdiag</title>
<style>
  :root{color-scheme:dark}*{box-sizing:border-box}
  body{margin:0;background:#0a0b10;color:#c3c7d6;font:13px/1.5 'SF Mono',Menlo,Consolas,monospace}
  header{position:sticky;top:0;z-index:5;display:flex;gap:10px;align-items:center;flex-wrap:wrap;padding:10px 14px;background:#11131a;border-bottom:1px solid #20242f}
  header b{font-size:16px;color:#fff}.count{color:#6b7186}.spacer{flex:1}
  button,select{background:#1a1d27;color:#dfe2ee;border:1px solid #2a2f3d;border-radius:7px;padding:6px 10px;cursor:pointer;font:inherit}
  button.on{background:#2b2f5a;border-color:#5b62c8}
  .wrap{padding:14px 18px}
  .sec{margin:16px 0 8px;color:#a78bfa;font-weight:700;font-size:13px;border-bottom:1px solid #20242f;padding-bottom:4px}
  table{border-collapse:collapse}
  table.m td,table.m th{border:1px solid #1c202b;padding:5px 8px;text-align:center;white-space:nowrap}
  table.m th{background:#11131a;color:#9aa0b4;font-weight:600}
  table.m th.h{writing-mode:vertical-rl;transform:rotate(180deg);max-height:180px;padding:8px 4px;font-weight:500}
  table.m td.g{text-align:left;color:#dfe2ee;background:#11131a}
  table.m td.c{cursor:pointer;color:#fff;text-shadow:0 1px 2px #000;min-width:74px}
  table.m td.c:hover{outline:2px solid #8ab4ff}
  table.m td.c.sel{outline:2px solid #ffd479}
  table.m td.e{color:#3a3f52}
  .legend{color:#6b7186;margin:6px 0 0}
  .hosts{display:flex;flex-wrap:wrap;gap:8px}
  .host{background:#11131a;border:1px solid #20242f;border-radius:8px;padding:8px 10px;min-width:220px}
  .host b{color:#fff}.host .r{color:#6b7186;font-size:11px}
  .bar{display:flex;height:6px;border-radius:3px;overflow:hidden;background:#1c202b;margin:6px 0}
  .bar i{display:block;height:100%}
  .dns{background:#ff8a8a}.tcp{background:#ffb47a}.tls{background:#ffd479}.http{background:#c3b3ff}.ok{background:#4caf7d}
  .rep{border:1px solid #20242f;border-radius:8px;margin:8px 0;background:#0e1016}
  .rep .hd{display:flex;gap:10px;align-items:baseline;padding:8px 12px;cursor:pointer;flex-wrap:wrap}
  .rep .hd:hover{background:#14161d}
  .badge{padding:1px 7px;border-radius:6px;font-size:11px;font-weight:700;background:#2a2f3d;color:#dfe2ee}
  .badge.stall{background:rgba(255,60,60,.2);color:#ff8a8a}
  .badge.failover{background:rgba(255,180,60,.25);color:#ffd479}
  .badge.periodic{background:rgba(76,175,125,.2);color:#8fd8b0}
  .badge.manual{background:rgba(124,92,255,.25);color:#c3b3ff}
  .badge.http_error{background:rgba(255,120,60,.25);color:#ffb47a}
  .lt{color:#5b6076}.ctx{color:#9aa0b4}
  .rep .body{display:none;padding:4px 12px 12px;border-top:1px solid #20242f}
  .rep.open .body{display:block}
  table.rows td,table.rows th{padding:2px 10px 2px 0;text-align:left}
  table.rows th{color:#6b7186;font-weight:500}
  td.f{color:#ff8a8a;font-weight:700}td.k{color:#7f8699}
  .ev{padding:1px 0;word-break:break-word}.ev.balancer{color:#ffb47a}.ev.torrent{color:#c3b3ff}.ev.network{color:#ff8a8a}.ev.decoder{color:#ffd479}.ev.seek,.ev.info{color:#7f8699}
  .empty{color:#5b6076;padding:20px}
</style></head><body>
<header><b>netdiag</b><span class="count" id="cnt">0</span>
  <select id="hours"><option value="6">6 ч</option><option value="24" selected>24 ч</option><option value="72">3 дня</option><option value="168">7 дней</option><option value="720">30 дней</option></select>
  <select id="group"><option value="asn" selected>по провайдеру (ASN)</option><option value="city">по городу</option><option value="country">по стране</option><option value="platform">по платформе</option><option value="reason">по причине пробы</option><option value="net">по типу сети</option><option value="source">по источнику</option></select>
  <select id="country" title="показывать только отчёты из выбранной страны"><option value="">все страны</option></select>
  <select id="step"><option value="any" selected>любой шаг</option><option value="dns">DNS</option><option value="tcp">TCP</option><option value="tls">TLS</option><option value="http">HTTP</option></select>
  <button id="only" title="скрыть пробы, где всё прошло">только с провалами</button>
  <button id="refresh">обновить</button><button id="clr">очистить</button>
  <span class="spacer"></span><span class="count">DNS → TCP → TLS → HTTP по нашим хостам с устройств зрителей; цвет = доля проваленных проб</span>
</header>
<div class="wrap">
  <div class="sec">Матрица: строки — <span id="gname">провайдер</span>, столбцы — хосты · клик по ячейке фильтрует отчёты ниже</div>
  <div id="matrix"></div>
  <div class="legend">в ячейке: доля провалов · число проб · какой шаг падал чаще (d=DNS t=TCP s=TLS h=HTTP) · p50 TTFB</div>
  <div class="sec">По хостам за окно</div>
  <div class="hosts" id="hosts"></div>
  <div class="sec">Отчёты <span class="count" id="fcnt"></span> <button id="unsel" style="display:none">снять фильтр</button></div>
  <div id="list"></div>
</div>
<script>
(function(){
  var $=function(id){return document.getElementById(id)};
  var reports=[],sel=null,onlyFail=false;
  function esc(s){return (s==null?'':String(s)).replace(/[&<>"]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]})}
  function ft(ms){var d=new Date(ms);function p(n){return (n<10?'0':'')+n}return p(d.getDate())+'.'+p(d.getMonth()+1)+' '+p(d.getHours())+':'+p(d.getMinutes())+':'+p(d.getSeconds())}
  function hostName(h){return (h||'').replace(/^https?:\/\//,'').replace(/\/$/,'')}
  function failStep(r){if(r.dns===-1)return 'dns';if(r.tcp===-1)return 'tcp';if(r.tls===-1)return 'tls';if(r.http===-1)return 'http';if(r.code>=500)return 'http';return null}
  function p50(a){if(!a.length)return null;a=a.slice().sort(function(x,y){return x-y});return a[Math.floor(a.length/2)]}
  function groupKey(rep){var g=$('group').value;var v=rep[g];if(g==='asn')v=rep.asn||rep.isp;return v||'?'}
  // Фильтр по стране РАЗВЯЗАН с группировкой: «по стране» в select#group отвечает на вопрос
  // «где хуже», а этот — «а что внутри выбранной страны». Раньше их приходилось выбирать
  // одно вместо другого, и посмотреть провайдеров ОДНОЙ страны было нельзя вовсе.
  function countryOk(rep){var c=$('country').value;return !c||(rep.country||'?')===c}
  // Список стран строится из самих данных и сортируется по числу отчётов: сверху то, где
  // зрителей больше всего, а не алфавит. Выбор переживает перезагрузку (она идёт раз в минуту),
  // иначе фильтр сбрасывался бы у админа под руками.
  function fillCountries(){
    var cnt={};
    reports.forEach(function(rep){var c=rep.country||'?';cnt[c]=(cnt[c]||0)+1});
    var keep=$('country').value;
    var list=Object.keys(cnt).sort(function(a,b){return cnt[b]-cnt[a]||a.localeCompare(b)});
    var h='<option value="">все страны ('+reports.length+')</option>';
    list.forEach(function(c){h+='<option value="'+esc(c)+'">'+esc(c)+' ('+cnt[c]+')</option>'});
    $('country').innerHTML=h;
    // Страна могла исчезнуть из окна (сменили «24 ч» на «6 ч») — тогда честнее показать всё,
    // чем молча оставить фильтр, под который нет ни одного отчёта.
    $('country').value=(keep&&cnt[keep])?keep:'';
  }
  function roleOrder(r){return {api:0,edge:1,stream:2,ref:3}[r]!==undefined?{api:0,edge:1,stream:2,ref:3}[r]:4}
  function stepOk(step){var f=$('step').value;return f==='any'||f===step}
  function color(p){var h=Math.round(120*(1-p));return 'hsl('+h+',55%,'+(p>0?'28%':'22%')+')'}
  function build(){
    var groups={},hosts={};
    reports.forEach(function(rep){
      if(!countryOk(rep))return;
      var rows=rep.rows||[];
      var anyFail=rows.some(function(r){return failStep(r)});
      if(onlyFail&&!anyFail)return;
      var g=groupKey(rep);var G=groups[g]||(groups[g]={name:g,n:0,cells:{}});G.n++;
      rows.forEach(function(r){
        var h=hostName(r.host);var H=hosts[h]||(hosts[h]={name:h,role:r.role,n:0,f:{dns:0,tcp:0,tls:0,http:0},http:[],tcp:[],kbps:[]});
        var c=G.cells[h]||(G.cells[h]={n:0,fail:0,steps:{dns:0,tcp:0,tls:0,http:0},http:[],kbps:[]});
        var s=failStep(r);
        if(s&&!stepOk(s))s=null;
        c.n++;H.n++;
        if(s){c.fail++;c.steps[s]++;H.f[s]++}
        if(r.http>=0){c.http.push(r.http);H.http.push(r.http)}
        if(r.tcp>=0)H.tcp.push(r.tcp);
        if(r.kbps>0){c.kbps.push(r.kbps);H.kbps.push(r.kbps)}
      });
    });
    var hostList=Object.keys(hosts).map(function(k){return hosts[k]}).sort(function(a,b){return roleOrder(a.role)-roleOrder(b.role)||a.name.localeCompare(b.name)});
    var groupList=Object.keys(groups).map(function(k){return groups[k]}).sort(function(a,b){return b.n-a.n});
    var h='<table class="m"><tr><th>'+esc($('group').selectedOptions[0].textContent)+'</th><th>проб</th>';
    hostList.forEach(function(H){h+='<th class="h" title="'+esc(H.role)+'">'+esc(H.name)+'</th>'});
    h+='</tr>';
    groupList.forEach(function(G){
      h+='<tr><td class="g">'+esc(G.name)+'</td><td>'+G.n+'</td>';
      hostList.forEach(function(H){
        var c=G.cells[H.name];
        if(!c){h+='<td class="e">·</td>';return}
        var p=c.fail/c.n;var dom='',dm=0;for(var k in c.steps){if(c.steps[k]>dm){dm=c.steps[k];dom=k}}
        var lbl={dns:'d',tcp:'t',tls:'s',http:'h'}[dom]||'';
        var t=p50(c.http);var kb=p50(c.kbps);
        var isSel=sel&&sel.g===G.name&&sel.h===H.name;
        h+='<td class="c'+(isSel?' sel':'')+'" data-g="'+esc(G.name)+'" data-h="'+esc(H.name)+'" style="background:'+color(p)+'" title="'+esc('провалов '+c.fail+' из '+c.n+(t!=null?' · p50 TTFB '+t+' мс':'')+(kb?' · p50 '+kb+' кбит/с':''))+'">'+
          Math.round(p*100)+'% · '+c.n+(lbl?' · '+lbl:'')+(t!=null?' · '+t+'мс':'')+'</td>';
      });
      h+='</tr>';
    });
    h+='</table>';
    $('matrix').innerHTML=groupList.length?h:'<div class="empty">за окно проб нет</div>';
    $('hosts').innerHTML=hostList.map(function(H){
      var fails=H.f.dns+H.f.tcp+H.f.tls+H.f.http;var ok=H.n-fails;
      function seg(cls,n){return n?'<i class="'+cls+'" style="width:'+(100*n/H.n)+'%" title="'+cls+' '+n+'"></i>':''}
      return '<div class="host"><b>'+esc(H.name)+'</b> <span class="r">'+esc(H.role)+' · '+H.n+' проб</span>'+
        '<div class="bar">'+seg('dns',H.f.dns)+seg('tcp',H.f.tcp)+seg('tls',H.f.tls)+seg('http',H.f.http)+seg('ok',ok)+'</div>'+
        '<div class="r">DNS '+H.f.dns+' · TCP '+H.f.tcp+' · TLS '+H.f.tls+' · HTTP '+H.f.http+' · ok '+ok+
        (H.tcp.length?' · p50 tcp '+p50(H.tcp)+'мс':'')+(H.http.length?' · p50 ttfb '+p50(H.http)+'мс':'')+(H.kbps.length?' · p50 '+Math.round(p50(H.kbps)/1000*10)/10+' Мбит/с':'')+'</div></div>';
    }).join('')||'<div class="empty">—</div>';
    Array.prototype.forEach.call(document.querySelectorAll('td.c'),function(td){td.onclick=function(){
      var g=td.getAttribute('data-g'),hh=td.getAttribute('data-h');
      sel=(sel&&sel.g===g&&sel.h===hh)?null:{g:g,h:hh};build();
    }});
    $('unsel').style.display=sel?'':'none';
    buildList();
  }
  function ms(v){return v===-1?'<td class="f">✕</td>':v===-2?'<td class="k">–</td>':'<td>'+v+'</td>'}
  function repHTML(rep){
    var rows=rep.rows||[];var fails=rows.filter(function(r){return failStep(r)}).length;
    var st=null;try{st=rep.stream?(typeof rep.stream==='string'?JSON.parse(rep.stream):rep.stream):null}catch(e){}
    var h='<div class="rep" data-id="'+esc(rep.id)+'"><div class="hd">'+
      '<span class="badge '+esc(rep.reason)+'">'+esc(rep.reason||'?')+'</span>'+
      '<span class="lt">'+ft(rep.rt)+'</span>'+
      '<span>'+esc(rep.asn||rep.isp||'?')+(rep.city?' · '+esc(rep.city):'')+(rep.country?' '+esc(rep.country):'')+'</span>'+
      '<span class="lt">'+esc(rep.platform||'')+(rep.net?' · '+esc(rep.net):'')+(rep.source?' · '+esc(rep.source):'')+'</span>'+
      '<span class="'+(fails?'f':'')+'">'+(fails?'провалов '+fails+'/'+rows.length:'все шаги ок')+'</span>'+
      (rep.context?'<span class="ctx">«'+esc(rep.context)+'»</span>':'')+
      '<span class="lt">'+esc(rep.ip)+(rep.uid?' · '+esc(rep.uid):'')+' · '+esc(rep.ua||'')+'</span>'+
      '</div><div class="body">';
    h+='<table class="rows"><tr><th>хост</th><th>роль</th><th>DNS</th><th>TCP</th><th>TLS</th><th>HTTP</th><th>код</th><th>кбит/с</th><th>ip</th><th>ошибка</th></tr>';
    rows.forEach(function(r){h+='<tr><td>'+esc(hostName(r.host))+'</td><td class="k">'+esc(r.role)+'</td>'+ms(r.dns)+ms(r.tcp)+ms(r.tls)+ms(r.http)+'<td>'+(r.code||'')+'</td><td>'+(r.kbps||'')+'</td><td class="k">'+esc(r.ip||'')+'</td><td class="k">'+esc(r.err||'')+'</td></tr>'});
    h+='</table>';
    if(st){
      h+='<div class="sec">Плеер: '+esc(st.summary||'')+'</div>';
      (st.events||[]).forEach(function(e){h+='<div class="ev '+esc(e.cause||'info')+'"><span class="lt">'+(e.t?ft(e.t):'')+(e.pos!=null?' @'+Math.round(e.pos)+'s':'')+'</span> ['+esc(e.kind)+(e.cause?'/'+esc(e.cause):'')+'] '+esc(e.detail||'')+(e.dur_ms?' · '+Math.round(e.dur_ms/100)/10+'с':'')+'</div>'});
    }
    h+='</div></div>';
    return h;
  }
  function buildList(){
    var list=reports.filter(function(rep){
      if(!countryOk(rep))return false;
      var rows=rep.rows||[];
      if(onlyFail&&!rows.some(function(r){return failStep(r)}))return false;
      if(!sel)return true;
      if(groupKey(rep)!==sel.g)return false;
      return rows.some(function(r){return hostName(r.host)===sel.h&&(function(s){return !!s&&stepOk(s)})(failStep(r))||hostName(r.host)===sel.h&&$('step').value==='any'});
    }).slice(0,300);
    $('fcnt').textContent=list.length+(sel?' · '+sel.g+' × '+sel.h:'');
    $('list').innerHTML=list.map(repHTML).join('')||'<div class="empty">пусто</div>';
    Array.prototype.forEach.call(document.querySelectorAll('.rep .hd'),function(hd){hd.onclick=function(){hd.parentNode.classList.toggle('open')}});
  }
  function load(){
    fetch('api/netdiag?hours='+$('hours').value,{credentials:'include'}).then(function(r){return r.json()}).then(function(j){
      reports=j.reports||[];$('cnt').textContent=reports.length;fillCountries();build();
    }).catch(function(){});
  }
  $('hours').onchange=load;$('group').onchange=function(){sel=null;$('gname').textContent=$('group').selectedOptions[0].textContent;build()};$('step').onchange=function(){sel=null;build()};
  $('country').onchange=function(){sel=null;build()};
  $('only').onclick=function(){onlyFail=!onlyFail;$('only').classList.toggle('on',onlyFail);build()};
  $('unsel').onclick=function(){sel=null;build()};
  $('refresh').onclick=load;
  $('clr').onclick=function(){if(!confirm('Удалить все отчёты netdiag?'))return;fetch('api/netdiag',{method:'POST',credentials:'include',headers:{'Content-Type':'application/json'},body:'{"action":"clear"}'}).then(load)};
  load();setInterval(load,60000);
})();
</script></body></html>`
