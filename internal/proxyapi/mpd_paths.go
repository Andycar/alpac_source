package proxyapi

import (
	"regexp"
	"strings"
)

// mpd_paths.go — абсолютизация относительных путей внутри DASH-манифеста.
//
// Зачем: живой DASH часто не содержит <BaseURL>, а ссылки на сегменты живут
// относительными в SegmentTemplate:
//
//	media="../../../dash-live2/streams/1tv/…-$Number%09d$.mp4"
//	initialization="…init.mp4"
//
// Манифест мы отдаём со СВОЕГО домена (у вещателя на нём нет CORS), поэтому
// плеер резолвит такие пути относительно нашего /proxy/<hash> и промахивается.
// Абсолютизируем их на вещателя: сегменты у него CORS отдают, ходить за ними
// через нас незачем — это и трафик, и лишнее звено.

var reMPDPathAttr = regexp.MustCompile(`(?i)\b(media|initialization|sourceURL)="([^"]+)"`)

// absolutizeMPDPaths rewrites relative media/initialization/sourceURL values to
// absolute URLs against the manifest's own URL. Значения с готовой схемой,
// протокол-относительные (//host/…) и шаблоны без пути не трогаются.
func absolutizeMPDPaths(mpd, manifestURL string) string {
	dir := urlDir(manifestURL)
	if dir == "" {
		return mpd
	}
	return reMPDPathAttr.ReplaceAllStringFunc(mpd, func(match string) string {
		m := reMPDPathAttr.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		val := m[2]
		if val == "" || strings.HasPrefix(val, "//") ||
			strings.HasPrefix(strings.ToLower(val), "http://") ||
			strings.HasPrefix(strings.ToLower(val), "https://") {
			return match
		}
		abs := resolveRelative(dir, val)
		if abs == "" {
			return match
		}
		return strings.Replace(match, `"`+val+`"`, `"`+abs+`"`, 1)
	})
}

// urlDir returns scheme://host/path/ with the last path element dropped.
func urlDir(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	i := strings.Index(raw, "://")
	if i < 0 {
		return ""
	}
	rest := raw[i+3:]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return raw + "/"
	}
	root := raw[:i+3+slash] // scheme://host
	path := rest[slash:]
	if j := strings.LastIndexByte(path, '/'); j >= 0 {
		path = path[:j+1]
	}
	return root + path
}

// resolveRelative joins a relative reference onto an absolute directory URL,
// resolving "." and "..". Свой резолвер вместо net/url НАРОЧНО: в путях DASH
// живут шаблоны вида $Number%09d$, а url.Parse трактует "%09" как
// percent-encoding и молча превращает его в табуляцию, ломая имя сегмента.
func resolveRelative(dirURL, ref string) string {
	if strings.HasPrefix(ref, "/") { // от корня хоста
		i := strings.Index(dirURL, "://")
		if i < 0 {
			return ""
		}
		rest := dirURL[i+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			return dirURL[:i+3+slash] + ref
		}
		return dirURL + ref
	}

	i := strings.Index(dirURL, "://")
	if i < 0 {
		return ""
	}
	rest := dirURL[i+3:]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return dirURL + "/" + ref
	}
	root := dirURL[:i+3+slash]
	segs := strings.Split(strings.Trim(rest[slash:], "/"), "/")
	if len(segs) == 1 && segs[0] == "" {
		segs = nil
	}
	for _, part := range strings.Split(ref, "/") {
		switch part {
		case "", ".":
			// пропускаем
		case "..":
			if len(segs) > 0 {
				segs = segs[:len(segs)-1]
			}
		default:
			segs = append(segs, part)
		}
	}
	return root + "/" + strings.Join(segs, "/")
}
