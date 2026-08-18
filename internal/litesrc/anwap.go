package litesrc

// anwap — a plain-HTTP balancer for the anwap catalogue (tv.anwap.today).
//
// No browser, no tokens, no account: search pages are ordinary HTML and the
// stream URL sits in the page as an obfuscated base64 blob:
//
//	films:    GET /films/search/?slv={title}&vid=1&toch=on  -> /films/{id}
//	          GET /films/{id}                               -> "file":"#2<blob>"
//	serials:  GET /serials/search/?t=on&word={title}&vid=1  -> /serials/{id}
//	          GET /serials/{id}                             -> /serials/s{seasonId}
//	          GET /serials/s{seasonId}                      -> /serials/down/{epId}
//	          GET /serials/down/{epId}                      -> "file":"#2<blob>"
//
// The decoded blob is a " or "-separated list of mirrors (HLS first, then a
// progressive mp4). Quality is whatever the site stores — there is no ladder and
// no master playlist, so a single "auto" entry is all we can honestly offer.
// Measured: ~576x432 on older titles, ~720 wide on newer ones.

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

const anwapDefaultHost = "https://tv.anwap.today"

const anwapUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

type anwapChecker struct {
	client *http.Client
	host   string
	links  *proxylink.Manager
}

func NewAnwapChecker(cfg config.Config) *anwapChecker {
	host := strings.TrimRight(strings.TrimSpace(cfg.Online.Anwap.Host), "/")
	if host == "" {
		host = anwapDefaultHost
	}
	return &anwapChecker{
		client: anwapHTTPClient(),
		host:   host,
	}
}

// anwapHTTPClient must NOT fall back to httpclient.SharedTransport.
//
// That transport disables HTTP/2 (a deliberate fix for cross-host h2 connection
// coalescing under InsecureSkipVerify) and caps the TLS handshake at 10s. This
// site's edge never completes an ALPN handshake that offers http/1.1 only: every
// request dies on exactly "TLS handshake timeout" after 10.16s, while the stock
// transport answers in ~430ms. Same shape as the uafilm WAF, which also had to
// be moved off the shared http/1.1 transport.
//
// A proxy explicitly mapped to this balancer still wins — only the fallback
// changes — so binding anwap to a vless/SOCKS exit keeps working.
func anwapHTTPClient() *http.Client {
	if t := httpclient.TransportForBalancer("anwap"); t != nil {
		return &http.Client{Transport: t, Timeout: 15 * time.Second}
	}
	// http.DefaultTransport может быть подменён (тесты httpapi ставят свой
	// RoundTripper) — тогда type assertion даёт nil, и Clone() роняет процесс.
	tr, _ := http.DefaultTransport.(*http.Transport)
	if tr == nil {
		tr = &http.Transport{ForceAttemptHTTP2: true}
	} else {
		tr = tr.Clone()
		tr.ForceAttemptHTTP2 = true
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second}
}

// ---------------------------------------------------------------------------
// Page parsing
// ---------------------------------------------------------------------------

var (
	anwapFileRe    = regexp.MustCompile(`"file":"#2(.*?)"`)
	anwapCardRe    = regexp.MustCompile(`(?s)href="/(films|serials)/(\d+)"(.*?)(?:</div>\s*</div>|<div class="my_razdel)`)
	anwapNameRe    = regexp.MustCompile(`(?s)<div class="namefilm">\s*(.*?)</div>`)
	anwapAltRe     = regexp.MustCompile(`alt="([^"]*)"`)
	anwapYearRe    = regexp.MustCompile(`<span class="in year">\s*(\d{4})\s*</span>`)
	anwapSeasonRe  = regexp.MustCompile(`href="(/serials/s\d+)"[^>]*>(.*?)</a>`)
	anwapEpisodeRe = regexp.MustCompile(`href="(/serials/down/\d+)"[^>]*>(.*?)</a>`)
	anwapSeasonNum = regexp.MustCompile(`(\d+)\s*[Сс]езон`)
	anwapEpNum     = regexp.MustCompile(`(\d+)\s*[Сс]ери`)
	anwapTagRe     = regexp.MustCompile(`<[^>]*>`)
)

// anwapJunk are the filler tokens the site splices into the base64 payload.
// Each one is inserted right after a "//" separator.
var anwapJunk = []string{
	"WXQ2cmpGZA==",
	"RmtpVTdoRw==",
	"RXJTdzNBc2k=",
	"UlRkM1M2NUZn",
}

// anwapDecodeFile turns the obfuscated "file" payload into its mirror list.
//
// The strip runs to a FIXED POINT on purpose. Junk tokens can be split by other
// junk tokens — a serial page carried "//Rmt//RXJTdzNBc2k=pVTdoRw==", where
// removing the inner token is what makes the outer "RmtpVTdoRw==" contiguous.
// A single pass leaves that outer token behind, it decodes as payload, and the
// URL comes out truncated (observed: ".../seriaFkiU7hG" instead of
// ".../serials/playlist_99091_h.txt"). Films happen to survive one pass, which
// is why a single-pass port of this looks correct until the first serial.
func anwapDecodeFile(html string) []string {
	m := anwapFileRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil
	}
	payload := strings.ReplaceAll(m[1], "//", "")
	for {
		before := payload
		for _, junk := range anwapJunk {
			payload = strings.ReplaceAll(payload, junk, "")
		}
		if payload == before {
			break
		}
	}
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}

	var out []string
	for _, part := range strings.Split(string(raw), " or ") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://") {
			out = append(out, part)
		}
	}
	return out
}

type anwapCard struct {
	id     string
	serial bool
	name   string
	year   int
}

// anwapParseCards pulls the result cards out of a search page.
func anwapParseCards(html string) []anwapCard {
	var out []anwapCard
	seen := map[string]bool{}
	for _, m := range anwapCardRe.FindAllStringSubmatch(html, -1) {
		kind, id, body := m[1], m[2], m[3]
		key := kind + "/" + id
		if seen[key] {
			continue
		}
		seen[key] = true

		name := ""
		if nm := anwapNameRe.FindStringSubmatch(body); len(nm) > 1 {
			name = strings.TrimSpace(anwapTagRe.ReplaceAllString(nm[1], ""))
		}
		if name == "" {
			if am := anwapAltRe.FindStringSubmatch(body); len(am) > 1 {
				name = strings.TrimSpace(am[1])
			}
		}
		year := 0
		if ym := anwapYearRe.FindStringSubmatch(body); len(ym) > 1 {
			year, _ = strconv.Atoi(ym[1])
		}
		out = append(out, anwapCard{id: id, serial: kind == "serials", name: name, year: year})
	}
	return out
}

// anwapPickCard chooses the best card for a title/year. Year is a hard filter
// when both sides know it — the catalogue is large and title collisions are
// common ("Матрица" alone returns 11 rows, including unrelated adult titles).
func anwapPickCard(cards []anwapCard, title, originalTitle string, year int) (anwapCard, bool) {
	want := normalizeSearchTitle(title)
	wantOrig := normalizeSearchTitle(originalTitle)

	var fallback anwapCard
	var haveFallback bool
	for _, c := range cards {
		if year > 0 && c.year > 0 && c.year != year {
			continue
		}
		// The card name is "Русское / Original" — compare against both sides.
		for _, part := range strings.Split(c.name, "/") {
			got := normalizeSearchTitle(part)
			if got == "" {
				continue
			}
			if (want != "" && got == want) || (wantOrig != "" && got == wantOrig) {
				return c, true
			}
		}
		if !haveFallback {
			fallback, haveFallback = c, true
		}
	}
	// Nothing matched by name: accept a year-compatible row only when the caller
	// gave a year, otherwise we would hand back an arbitrary title.
	if haveFallback && year > 0 {
		return fallback, true
	}
	return anwapCard{}, false
}

// ---------------------------------------------------------------------------
// HTTP entry point
// ---------------------------------------------------------------------------

func (a *anwapChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	a.links = links

	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			writeCheckSearchResponseNoRCH(w, a.checkSearch(req), pluginQualityBadgeGet("anwap"))
			return
		}
		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")
		switch {
		case strings.HasPrefix(raw, "anwap/play"):
			a.play(w, req)
		case strings.HasPrefix(raw, "anwap/serial"):
			a.serial(w, req)
		default:
			a.index(w, req)
		}
	}
}

func (a *anwapChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		return false
	}
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	serial := strings.TrimSpace(q.Get("serial")) == "1"

	cards, ok := a.search(req, title, serial)
	if !ok {
		return false
	}
	_, found := anwapPickCard(cards, title, strings.TrimSpace(q.Get("original_title")), year)
	return found
}

// index resolves the card and either hands back a play node (film) or the
// season list (serial).
func (a *anwapChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	if title == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	serial := strings.TrimSpace(q.Get("serial")) == "1"

	cards, ok := a.search(req, title, serial)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	card, found := anwapPickCard(cards, title, originalTitle, year)
	if !found {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	if card.serial {
		a.writeSeasons(w, req, host, card, rjson)
		return
	}

	row := map[string]any{
		"method":    "call",
		"url":       host + "/lite/anwap/play?path=" + url.QueryEscape("/films/"+card.id),
		"title":     card.name,
		"translate": "anwap",
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": []map[string]any{row}})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, card.name, true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (a *anwapChecker) writeSeasons(w http.ResponseWriter, req *http.Request, host string, card anwapCard, rjson bool) {
	page, ok := a.fetch(req, a.host+"/serials/"+card.id)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	matches := anwapSeasonRe.FindAllStringSubmatch(page, -1)
	if len(matches) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(matches))
	labels := make([]string, 0, len(matches))
	for _, m := range matches {
		text := strings.TrimSpace(anwapTagRe.ReplaceAllString(m[2], ""))
		num := 0
		if sm := anwapSeasonNum.FindStringSubmatch(text); len(sm) > 1 {
			num, _ = strconv.Atoi(sm[1])
		}
		label := text
		if num > 0 {
			label = strconv.Itoa(num) + " сезон"
		}
		link := host + "/lite/anwap/serial?path=" + url.QueryEscape(m[1]) + "&s=" + strconv.Itoa(num)
		if rjson {
			link += "&rjson=true"
		}
		data = append(data, map[string]any{"method": "link", "url": link, "name": label})
		labels = append(labels, label)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// serial lists the episodes of one season page.
func (a *anwapChecker) serial(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	path := strings.TrimSpace(q.Get("path"))
	if !strings.HasPrefix(path, "/serials/s") {
		writeGetsTVEmpty(w, rjson)
		return
	}
	season, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))

	page, ok := a.fetch(req, a.host+path)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	matches := anwapEpisodeRe.FindAllStringSubmatch(page, -1)
	if len(matches) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(matches))
	labels := make([]string, 0, len(matches))
	nums := make([]int, 0, len(matches))
	for _, m := range matches {
		text := strings.TrimSpace(anwapTagRe.ReplaceAllString(m[2], ""))
		num := 0
		if em := anwapEpNum.FindStringSubmatch(text); len(em) > 1 {
			num, _ = strconv.Atoi(em[1])
		}
		label := text
		if num > 0 {
			label = strconv.Itoa(num) + " серия"
		}
		row := map[string]any{
			"method":    "call",
			"url":       host + "/lite/anwap/play?path=" + url.QueryEscape(m[1]),
			"title":     label,
			"translate": "anwap",
			"s":         season,
			"e":         num,
		}
		data = append(data, row)
		labels = append(labels, label)
		nums = append(nums, num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": data})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, season, nums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// play fetches a film/episode page and returns its stream.
func (a *anwapChecker) play(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimSpace(req.URL.Query().Get("path"))
	if !strings.HasPrefix(path, "/films/") && !strings.HasPrefix(path, "/serials/down/") {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	page, ok := a.fetch(req, a.host+path)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	mirrors := anwapDecodeFile(page)
	if len(mirrors) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// The CDN checks Referer; without it the mirrors answer 403.
	headers := map[string]string{"Referer": a.host + "/", "User-Agent": anwapUA}
	stream := streamProxyURLWithHeaders(req, mirrors[0], "anwap", a.links, headers)

	out := map[string]any{
		"method":  "play",
		"url":     stream,
		"title":   "auto",
		"quality": map[string]string{"auto": stream},
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Fetch helpers
// ---------------------------------------------------------------------------

func (a *anwapChecker) search(req *http.Request, title string, serial bool) ([]anwapCard, bool) {
	var target string
	if serial {
		target = a.host + "/serials/search/?t=on&word=" + url.QueryEscape(title) + "&vid=1"
	} else {
		target = a.host + "/films/search/?slv=" + url.QueryEscape(title) + "&vid=1&toch=on"
	}
	page, ok := a.fetch(req, target)
	if !ok {
		return nil, false
	}
	return anwapParseCards(page), true
}

func (a *anwapChecker) fetch(req *http.Request, target string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("User-Agent", anwapUA)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	httpReq.Header.Set("Referer", a.host+"/")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}
