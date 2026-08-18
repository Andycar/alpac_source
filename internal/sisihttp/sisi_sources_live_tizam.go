package sisihttp

import (
	"context"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

var (
	tizamPaginationSplitRe = regexp.MustCompile(`(?is)id="pagination"`)
	tizamItemSplitRe       = regexp.MustCompile(`(?is)video-item`)
	tizamTitleRe           = regexp.MustCompile(`(?is)(?:-name="name"|item__title"|thumb__title")[^>]*>([^<]+)<`)
	tizamHrefRe            = regexp.MustCompile(`(?is)<a[^>]+href="([^"]+)"[^>]*(?:itemprop="url")?`)
	tizamImgRe             = regexp.MustCompile(`(?is)(?:data-srcset|srcset|data-src|src|data-original)="([^"]+)"`)
	tizamTimeRe            = regexp.MustCompile(`(?is)itemprop="duration" content="([^<]+)"`)
	tizamMP4Re             = regexp.MustCompile(`(?is)src="(https?://[^"]+\.mp4)" type="video/mp4"`)

	validTizamPlugin = map[string]struct{}{"tizam": {}}
)

type sisiTizamSource struct {
	cfg    config.Config
	client *http.Client
	host   string
}

func newSisiTizamSource(cfg config.Config) *sisiTizamSource {
	return &sisiTizamSource{
		cfg:    cfg,
		client: httpclient.NewProxied(12 * time.Second),
		host:   SisiSourceHost("Tizam", "https://in.tizam.info"),
	}
}

func (s *sisiTizamSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := validTizamPlugin[sisiSourcePluginFromPath(r.URL.Path)]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}
		if strings.TrimSpace(r.URL.Query().Get("search")) != "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		html, err := s.fetchHTML(r.Context(), s.listURL(pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"list":        parseTizamPlaylist(hostFromRequest(r), s.host, html),
			"total_pages": 1,
		})
	}
}

func (s *sisiTizamSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}
		html, err := s.fetchHTML(r.Context(), s.viewURL(uri))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}
		mp4 := strings.TrimSpace(submatch1(tizamMP4Re, html))
		if mp4 == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}
		qualitys := map[string]any{
			"auto": mp4,
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       qualitys,
			"qualitys_proxy": sisiProxyQualitys(r, qualitys),
		})
	}
}

func (s *sisiTizamSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return sisiFetchHTML(ctx, s.client, target)
}

func (s *sisiTizamSource) listURL(pg int) string {
	base := strings.TrimRight(s.host, "/") + "/fil_my_dlya_vzroslyh/s_russkim_perevodom/"
	if pg <= 1 {
		return base
	}
	return base + "?p=" + strconv.Itoa(pg-1)
}

func (s *sisiTizamSource) viewURL(uri string) string {
	return strings.TrimRight(s.host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(uri), "/")
}

func parseTizamPlaylist(host, sourceHost, html string) []map[string]any {
	if html == "" {
		return []map[string]any{}
	}
	scope := html
	if parts := tizamPaginationSplitRe.Split(html, -1); len(parts) > 0 {
		scope = parts[0]
	}
	items := tizamItemSplitRe.Split(scope, -1)
	if len(items) <= 1 {
		return []map[string]any{}
	}
	sourceHost = strings.TrimRight(strings.TrimSpace(sourceHost), "/")

	out := make([]map[string]any, 0, len(items)-1)
	for i := 1; i < len(items); i++ {
		row := items[i]
		if strings.Contains(strings.ToLower(row), "pin--premium") {
			continue
		}
		title := strings.TrimSpace(submatch1(tizamTitleRe, row))
		href := strings.TrimSpace(submatch1(tizamHrefRe, row))
		href = sisiNormalizePathFromHref(href)
		img := strings.TrimSpace(submatch1(tizamImgRe, row))
		if strings.Contains(img, ",") {
			img = firstSrcsetURL(img)
		}
		img = strings.TrimSpace(strings.SplitN(img, " ", 2)[0])
		if title == "" || href == "" || img == "" {
			continue
		}
		picture := normalizeTizamPicture(sourceHost, img)
		if picture == "" {
			continue
		}
		pictureProxy := host + "/proxy/" + url.QueryEscape(picture)
		out = append(out, map[string]any{
			"name":    title,
			"video":   host + "/tizam/vidosik?uri=" + url.QueryEscape(href),
			"picture": pictureProxy,
			"time":    strings.TrimSpace(submatch1(tizamTimeRe, row)),
			"json":    true,
			"bookmark": map[string]any{
				"site":  "tizam",
				"href":  href,
				"image": picture,
			},
		})
	}
	return out
}

func normalizeTizamPicture(sourceHost, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.ReplaceAll(raw, `\/`, "/")
	raw = strings.TrimSpace(strings.SplitN(raw, " ", 2)[0])
	raw = strings.Trim(raw, `"'`)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return strings.TrimRight(sourceHost, "/") + "/" + strings.TrimLeft(raw, "/")
}
