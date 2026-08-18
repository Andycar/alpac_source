package sisihttp

import (
	"context"
	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

var (
	// <div id="18491" vkey="..." class="video trailer">
	sxstItemSplitRe = regexp.MustCompile(`(?is)<div\s+id="\d+"\s+vkey="[^"]*"\s+class="video trailer"`)
	// <a href="/video/slug-12345" alt="Title">
	sxstHrefRe = regexp.MustCompile(`(?is)<a\s+href="(/video/[^"]+)"\s+alt="([^"]*)"`)
	// <img class="image" src="/images/18491.jpg?00"
	sxstImgRe = regexp.MustCompile(`(?is)<img\s+class="image"\s+src="([^"]+)"`)
	// pagination: <a href="?page=5" class="...">
	sxstPageRe = regexp.MustCompile(`(?is)\?page=(\d+)`)
	// video page: <source src="https://sex-studentki.live/link_cs/..."/>
	sxstSourceRe = regexp.MustCompile(`(?is)<source\s+src="(https?://[^"]+)"`)
	// VIDEO_DURATION = '1206';
	sxstDurationRe = regexp.MustCompile(`(?is)VIDEO_DURATION\s*=\s*'(\d+)'`)
	// search: <form ... action="/search/" or ?s=query
	sxstSearchPath = "/search/"

	validSxstPlugin = map[string]struct{}{"sxst": {}}
)

type sisiSexStudentkiSource struct {
	cfg          config.Config
	client       *http.Client
	clientDirect *http.Client
	host         string
}

func newSisiSexStudentkiSource(cfg config.Config) *sisiSexStudentkiSource {
	return &sisiSexStudentkiSource{
		cfg:          cfg,
		client:       httpclient.NewProxied(12 * time.Second),
		clientDirect: httpclient.New(12 * time.Second),
		host:         SisiSourceHost("SexStudentki", "https://sex-studentki.live"),
	}
}

func (s *sisiSexStudentkiSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := validSxstPlugin[sisiSourcePluginFromPath(r.URL.Path)]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		// Unified category filter (search fallback with Russian terms).
		if cat := r.URL.Query().Get("cat"); cat != "" {
			res := resolveSisiCategory("sxst", cat)
			if res.SearchTerm != "" {
				search = res.SearchTerm
			}
		}

		html, err := s.fetchHTML(r.Context(), s.listURL(search, pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"list":        parseSxstPlaylist(hostFromRequest(r), s.host, html),
			"total_pages": sxstMaxPage(html),
		})
	}
}

func (s *sisiSexStudentkiSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}

		pageURL := s.viewURL(uri)
		host := hostFromRequest(r)

		html, err := s.fetchHTML(r.Context(), pageURL)
		if err != nil {
			log.Warn().Err(err).Str("page", pageURL).Msg("sxst: failed to fetch video page")
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}

		// Extract <source src="..."> — filter out /system/stat (analytics).
		videoURL := sxstExtractVideoURL(html)
		if videoURL == "" {
			log.Warn().Str("page", pageURL).Msg("sxst: no video URL found in page HTML")
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}

		log.Debug().Str("video", videoURL).Msg("sxst: extracted video URL from HTML")

		// Proxy through our server — link_cs needs Referer header.
		var proxyURL string
		if pl := liveProxyLinks(); pl != nil {
			headers := map[string]string{
				"Referer": s.host + "/",
			}
			encrypted := pl.EncryptURIWithHeaders(videoURL, clientIP(r), "sxst", headers)
			proxyURL = host + "/proxy/" + encrypted
		} else {
			proxyURL = host + "/proxy/" + url.QueryEscape(videoURL)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       map[string]any{"auto": proxyURL},
			"qualitys_proxy": map[string]any{"auto": proxyURL},
		})
	}
}

func (s *sisiSexStudentkiSource) fetchHTML(ctx context.Context, target string) (string, error) {
	if html, err := sisiFetchHTML(ctx, s.client, target); err == nil && html != "" {
		return html, nil
	}
	return sisiFetchHTML(ctx, s.clientDirect, target)
}

func (s *sisiSexStudentkiSource) listURL(search string, pg int) string {
	base := strings.TrimRight(s.host, "/")
	if search != "" {
		// Use slug-based search: /search/{query}?page=N
		// The ?q= format uses Google CSE (JS-only, no server-side results).
		u := base + sxstSearchPath + url.PathEscape(search)
		if pg > 1 {
			u += "?page=" + strconv.Itoa(pg)
		}
		return u
	}
	if pg > 1 {
		return base + "/?page=" + strconv.Itoa(pg)
	}
	return base + "/"
}

func (s *sisiSexStudentkiSource) viewURL(uri string) string {
	return strings.TrimRight(s.host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(uri), "/")
}

// parseSxstPlaylist parses the video list from sex-studentki HTML.
// Structure: <div id="ID" vkey="..." class="video trailer">
//
//	<a href="/video/slug-12345" alt="Title">
//	  <img class="image" src="/images/ID.jpg?00">
func parseSxstPlaylist(host, sourceHost, html string) []map[string]any {
	if html == "" {
		return []map[string]any{}
	}

	parts := sxstItemSplitRe.Split(html, -1)
	if len(parts) <= 1 {
		return []map[string]any{}
	}

	sourceHost = strings.TrimRight(strings.TrimSpace(sourceHost), "/")
	out := make([]map[string]any, 0, len(parts)-1)

	for i := 1; i < len(parts); i++ {
		row := parts[i]

		m := sxstHrefRe.FindStringSubmatch(row)
		if len(m) < 3 {
			continue
		}
		href := strings.TrimSpace(m[1])
		title := strings.TrimSpace(m[2])
		if href == "" || title == "" {
			continue
		}

		img := strings.TrimSpace(submatch1(sxstImgRe, row))
		if img == "" {
			continue
		}

		// Normalize image URL
		picture := img
		if !strings.HasPrefix(picture, "http") {
			picture = sourceHost + "/" + strings.TrimLeft(picture, "/")
		}
		pictureProxy := host + "/proxy/" + url.QueryEscape(picture)

		out = append(out, map[string]any{
			"name":    title,
			"video":   host + "/sxst/vidosik?uri=" + url.QueryEscape(href),
			"picture": pictureProxy,
			"json":    true,
			"bookmark": map[string]any{
				"site":  "sxst",
				"href":  href,
				"image": picture,
			},
		})
	}

	return out
}

// sxstMaxPage finds the maximum page number from pagination links.
func sxstMaxPage(html string) int {
	matches := sxstPageRe.FindAllStringSubmatch(html, -1)
	maxPage := 1
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		if pg, err := strconv.Atoi(strings.TrimSpace(m[1])); err == nil && pg > maxPage {
			maxPage = pg
		}
	}
	return maxPage
}

// sxstExtractVideoURL extracts the video URL from <source src="..."> in the page HTML.
// The link_cs URL is a server-side redirect (302) to the actual CDN mp4.
// Filters out /system/stat URLs (analytics, not video).
func sxstExtractVideoURL(html string) string {
	matches := sxstSourceRe.FindAllStringSubmatch(html, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		u := strings.TrimSpace(m[1])
		if u == "" || strings.Contains(u, "/system/stat") {
			continue
		}
		return u
	}
	return ""
}

// sxstFormatDuration converts seconds string to "MM:SS" format.
func sxstFormatDuration(seconds string) string {
	sec, err := strconv.Atoi(strings.TrimSpace(seconds))
	if err != nil || sec <= 0 {
		return ""
	}
	min := sec / 60
	s := sec % 60
	return strconv.Itoa(min) + ":" + strings.Repeat("0", 1-len(strconv.Itoa(s))/2) + strconv.Itoa(s)
}
