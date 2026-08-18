package httpapi

import (
	"net/url"
	"strings"

	"lampac-go/internal/config"
)

func buildLocalSpiderMap(cfg config.Config, host, title string, anime bool) map[string]string {
	type item struct {
		name string
		path string
	}

	var ordered []item
	if anime {
		ordered = []item{
			{name: "kodik", path: "kodik"},
			{name: "animelib", path: "animelib"},
			{name: "anilibria", path: "anilibria"},
			{name: "animevost", path: "animevost"},
			{name: "animebesst", path: "animebesst"},
			{name: "moonanime", path: "moonanime"},
			{name: "animego", path: "animego"},
		}
	} else {
		ordered = []item{
			{name: "filmix", path: "filmix"},
			{name: "filmixtv", path: "filmixtv"},
			{name: "fxapi", path: "fxapi"},
			{name: "rezka", path: "rezka"},
			{name: "rhsprem", path: "rhsprem"},
			{name: "kinopub", path: "kinopub"},
			{name: "kinogo", path: "kinogo"},
			{name: "getstv", path: "getstv-search"},
			{name: "kinobase", path: "kinobase"},
			{name: "alloha", path: "alloha-search"},
			{name: "collaps", path: "collaps-search"},
			{name: "veoveo", path: "veoveo-spider"},
			{name: "videocdn", path: "videocdn"},
			{name: "lumex", path: "lumex"},
			{name: "vdbmovies", path: "vdbmovies"},
			{name: "hdvb", path: "hdvb-search"},
			{name: "videodb", path: "videodb"},
			{name: "youtube", path: "youtube"},
		}
	}

	enabled := map[string]bool{}
	for _, raw := range cfg.Online.WithSearch {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		enabled[p] = true
	}
	if len(enabled) == 0 {
		for _, it := range ordered {
			enabled[it.name] = true
		}
	}

	out := make(map[string]string, len(ordered))
	qTitle := url.QueryEscape(strings.TrimSpace(title))
	for _, it := range ordered {
		// keep deterministic and include entries when plugin is in with_search
		if !enabled[it.name] {
			continue
		}
		out[it.name] = host + "/lite/" + it.path + "?title=" + qTitle + "&clarification=1&rjson=true&similar=true"
	}
	return out
}
