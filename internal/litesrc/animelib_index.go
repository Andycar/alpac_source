package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// AnimeLib data structures for API responses
type animelibEpisode struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Number string `json:"number"`
	Season string `json:"season"`
}

type animelibEpisodesResponse struct {
	Data []animelibEpisode `json:"data"`
}

type animelibPlayer struct {
	Player string          `json:"player"`
	Team   animelibTeam    `json:"team"`
	Video  animelibVideoQL `json:"video"`
}

type animelibTeam struct {
	Name string `json:"name"`
}

type animelibVideoQL struct {
	Quality []animelibQuality `json:"quality"`
}

type animelibQuality struct {
	Href    string `json:"href"`
	Quality int    `json:"quality"`
}

type animelibEpisodeDetailResponse struct {
	Data struct {
		Players []animelibPlayer `json:"players"`
	} `json:"data"`
}

// animelibIndexLocal handles the full index for animelib in local mode.
func (a *animelibChecker) indexLocal(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	rjson := parseBoolParam(q.Get("rjson"))
	uri := strings.TrimSpace(q.Get("uri"))
	t := strings.TrimSpace(q.Get("t"))
	similar := parseBoolParam(q.Get("similar"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	if a.token == "" {
		log.Warn().Msg("animelib: no token configured")
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)

	if uri == "" {
		// Search mode
		a.animelibSearch(w, req, host, title, originalTitle, year, rjson, similar)
		return
	}

	// Episodes mode
	a.animelibEpisodes(w, req, host, title, uri, t, rjson, links)
}

func (a *animelibChecker) animelibSearch(w http.ResponseWriter, req *http.Request, host, title, originalTitle string, year int, rjson, similar bool) {
	if title == "" && originalTitle == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Try original_title first, then title
	var items []animelibSearchItem
	var ok bool
	if originalTitle != "" {
		items, ok = a.search(req, originalTitle)
	}
	if (!ok || len(items) == 0) && title != "" {
		items, ok = a.search(req, title)
	}
	if !ok || len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	stitle := normalizeSearchTitle(title)

	type searchResult struct {
		title       string
		year        string
		uri         string
		coincidence bool
	}

	results := make([]searchResult, 0, len(items))
	for _, anime := range items {
		if anime.SlugURL == "" {
			continue
		}
		name := anime.RUName
		if name != "" && anime.ENName != "" {
			name += " / " + anime.ENName
		} else if name == "" {
			name = anime.ENName
		}

		rYear := "0"
		if anime.ReleaseDate != "" {
			parts := strings.Split(anime.ReleaseDate, "-")
			if len(parts) > 0 {
				rYear = parts[0]
			}
		}

		sr := searchResult{
			title: name,
			year:  rYear,
			uri:   anime.SlugURL,
		}

		// Check coincidence
		if stitle != "" {
			ru := normalizeSearchTitle(anime.RUName)
			en := normalizeSearchTitle(anime.ENName)
			if (stitle == ru || stitle == en) && year > 0 && strings.HasPrefix(anime.ReleaseDate, strconv.Itoa(year)) {
				sr.coincidence = true
			}
		}

		results = append(results, sr)
	}

	if len(results) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// If single exact match, go directly to episodes
	if !similar {
		var exact []searchResult
		for _, r := range results {
			if r.coincidence {
				exact = append(exact, r)
			}
		}
		if len(exact) == 1 {
			a.animelibEpisodes(w, req, host, title, exact[0].uri, "", rjson, nil)
			return
		}
	}

	rows := make([]map[string]any, 0, len(results))
	for _, r := range results {
		link := fmt.Sprintf("%s/lite/animelib?rjson=%v&title=%s&uri=%s",
			host, rjson, url.QueryEscape(title), url.QueryEscape(r.uri))
		rows = append(rows, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"title":   r.title,
			"year":    r.year,
		})
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendSeasonHTML(&sb, row, fmt.Sprint(row["title"]), i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (a *animelibChecker) animelibEpisodes(w http.ResponseWriter, req *http.Request, host, title, uri, activeTranslate string, rjson bool, links *proxylink.Manager) {
	// Fetch episodes list
	episodesURL := fmt.Sprintf("%s/api/episodes?anime_id=%s", a.host, url.QueryEscape(uri))
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, episodesURL, nil)
	if err != nil {
		writeGetsTVEmpty(w, rjson)
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.token)
	httpReq.Header.Set("Accept", "application/json,text/plain,*/*")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		log.Warn().Err(err).Msg("animelib: episodes fetch failed")
		writeGetsTVEmpty(w, rjson)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var epsResp animelibEpisodesResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&epsResp); err != nil || len(epsResp.Data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Get voices from first episode
	voices := a.fetchPlayers(req, epsResp.Data[0].ID)
	if activeTranslate == "" && len(voices) > 0 {
		for _, p := range voices {
			if p.Player == "Animelib" {
				activeTranslate = p.Team.Name
				break
			}
		}
	}

	rows := make([]map[string]any, 0, len(epsResp.Data))
	for _, ep := range epsResp.Data {
		name := title
		if ep.Name != "" {
			name = title + " / " + ep.Name
		}

		link := fmt.Sprintf("%s/lite/animelib/video?id=%d&voice=%s&title=%s",
			host, ep.ID, url.QueryEscape(activeTranslate), url.QueryEscape(title))

		e, _ := strconv.Atoi(ep.Number)
		s, _ := strconv.Atoi(ep.Season)

		rows = append(rows, map[string]any{
			"method": "call",
			"url":    link,
			"name":   ep.Number + " серия",
			"title":  name,
			"s":      s,
			"e":      e,
		})
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		result := map[string]any{
			"type": "episode",
			"data": rows,
		}
		if len(voices) > 0 {
			voiceRows := make([]map[string]any, 0)
			for _, p := range voices {
				if p.Player != "Animelib" {
					continue
				}
				vLink := fmt.Sprintf("%s/lite/animelib?rjson=%v&title=%s&uri=%s&t=%s",
					host, rjson, url.QueryEscape(title), url.QueryEscape(uri), url.QueryEscape(p.Team.Name))
				voiceRows = append(voiceRows, map[string]any{
					"method":   "link",
					"url":      vLink,
					"title":    p.Team.Name,
					"selected": activeTranslate == p.Team.Name,
				})
			}
			if len(voiceRows) > 0 {
				result["voice"] = voiceRows
			}
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	var sb strings.Builder
	// Voice buttons
	if len(voices) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, p := range voices {
			if p.Player != "Animelib" {
				continue
			}
			sel := ""
			if activeTranslate == p.Team.Name {
				sel = " active"
			}
			vLink := fmt.Sprintf("%s/lite/animelib?rjson=%v&title=%s&uri=%s&t=%s",
				host, rjson, url.QueryEscape(title), url.QueryEscape(uri), url.QueryEscape(p.Team.Name))
			sb.WriteString(fmt.Sprintf(`<div class="videos__button selector%s" data-json='{"method":"link","url":"%s"}'>`, sel, vLink))
			sb.WriteString(`<div class="videos__button-text">`)
			sb.WriteString(p.Team.Name)
			sb.WriteString(`</div></div>`)
		}
		sb.WriteString(`</div>`)
	}

	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		name := fmt.Sprint(row["name"])
		s := vokinoIntFromMap(row, "s")
		e := vokinoIntFromMap(row, "e")
		getsTVAppendMovieHTML(&sb, row, name, i == 0, s, e)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (a *animelibChecker) fetchPlayers(req *http.Request, episodeID int) []animelibPlayer {
	u := fmt.Sprintf("%s/api/episodes/%d", a.host, episodeID)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.token)
	httpReq.Header.Set("Accept", "application/json,text/plain,*/*")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	var detail animelibEpisodeDetailResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&detail); err != nil {
		return nil
	}
	return detail.Data.Players
}

// AnimelibVideoHandler handles GET /lite/animelib/video?id={episodeID}&voice={voiceName}&title={title}
func AnimelibVideoHandler(a *animelibChecker, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		idStr := strings.TrimSpace(q.Get("id"))
		voice := strings.TrimSpace(q.Get("voice"))
		title := strings.TrimSpace(q.Get("title"))
		play := parseBoolParam(q.Get("play"))

		id, err := strconv.Atoi(idStr)
		if err != nil || id == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing id"})
			return
		}

		if a.token == "" {
			writeGetsTVEmpty(w, false)
			return
		}

		players := a.fetchPlayers(req, id)
		if len(players) == 0 {
			writeGetsTVEmpty(w, false)
			return
		}

		// Get streams for matching voice
		type stream struct {
			url     string
			quality string
		}
		var streams []stream

		cdnBase := "https://video1.cdnlibs.org/.%D0%B0s/"

		for _, p := range players {
			if p.Player != "Animelib" {
				continue
			}
			if voice != "" && p.Team.Name != voice {
				continue
			}
			for _, v := range p.Video.Quality {
				if v.Href == "" {
					continue
				}
				fileURL := cdnBase + v.Href
				streams = append(streams, stream{
					url:     fileURL,
					quality: fmt.Sprintf("%dp", v.Quality),
				})
			}
			if len(streams) > 0 {
				break
			}
		}

		// Fallback: try any voice
		if len(streams) == 0 {
			for _, p := range players {
				if p.Player != "Animelib" {
					continue
				}
				for _, v := range p.Video.Quality {
					if v.Href == "" {
						continue
					}
					fileURL := cdnBase + v.Href
					streams = append(streams, stream{
						url:     fileURL,
						quality: fmt.Sprintf("%dp", v.Quality),
					})
				}
				if len(streams) > 0 {
					break
				}
			}
		}

		if len(streams) == 0 {
			writeGetsTVEmpty(w, false)
			return
		}

		bestURL := streamProxyURL(req, streams[0].url, "animelib", links)

		if play {
			http.Redirect(w, req, bestURL, http.StatusFound)
			return
		}

		sq := make([]map[string]any, len(streams))
		for i, s := range streams {
			sq[i] = map[string]any{
				"quality": s.quality,
				"url":     streamProxyURL(req, s.url, "animelib", links),
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"method":        "play",
			"url":           bestURL,
			"title":         title,
			"streamquality": sq,
		})
	}
}
