package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// runExportSlim строит «тонкий» JSON только с полями, необходимыми балансеру
// (id, name, year, tmdb_id, imdb_id, type, season, files[name, stream]) и
// сжимает его до размера, который влазит в HTTP-лимит JS-модулей (8 МБ).
//
// Формат — короткие ключи + массив [name, url] на файл вместо объекта:
//   {
//     "v": 1,
//     "n": 22658,
//     "items": [
//       {"i":25380,"n":"...","y":2015,"t":27205,"m":"tt...","k":"movie","f":[["filename.mkv","http://..."]]}
//     ]
//   }
//
// Поля k и s опускаются для фильмов с k=movie/s=0 чтобы экономить байты.
// sctsCDNDefaultBase — оригинальный CDN, который заменяется при cdnBase != "".
const sctsCDNDefaultBase = "http://dl.yama.scts.tv"

func runExportSlim(inPath, outPath, cdnBase string) {
	if outPath == "" {
		outPath = strings.TrimSuffix(inPath, ".json") + "-slim.json.gz"
	}
	cdnBase = strings.TrimRight(strings.TrimSpace(cdnBase), "/")

	body, err := os.ReadFile(inPath)
	if err != nil {
		log.Fatalf("чтение %s: %v", inPath, err)
	}
	var cat Catalog
	if err := json.Unmarshal(body, &cat); err != nil {
		log.Fatalf("разбор JSON: %v", err)
	}
	log.Printf("читаю %s: %d фильмов", inPath, len(cat.Movies))

	type slimFile [2]string // [name, url]
	type slimMovie struct {
		I int        `json:"i"`           // movie_id
		N string     `json:"n,omitempty"` // name (русское)
		Y int        `json:"y,omitempty"` // year
		T int        `json:"t,omitempty"` // tmdb_id
		M string     `json:"m,omitempty"` // imdb_id
		K string     `json:"k,omitempty"` // type: tv / (movie по умолчанию)
		S int        `json:"s,omitempty"` // season
		F []slimFile `json:"f,omitempty"` // files [name, stream_url]
	}
	type slimCatalog struct {
		V     int         `json:"v"`
		N     int         `json:"n"`
		Items []slimMovie `json:"items"`
	}

	slim := slimCatalog{V: 1}
	slim.Items = make([]slimMovie, 0, len(cat.Movies))
	skipped := 0

	for _, m := range cat.Movies {
		// Пропускаем фильмы без playable streams — они бесполезны балансеру.
		files := make([]slimFile, 0, len(m.Files))
		for _, f := range m.Files {
			if !f.Active {
				continue
			}
			stream := pickBestStream(f.Streams)
			if stream == "" {
				continue
			}
			if cdnBase != "" && strings.HasPrefix(stream, sctsCDNDefaultBase) {
				stream = cdnBase + strings.TrimPrefix(stream, sctsCDNDefaultBase)
			}
			files = append(files, slimFile{f.Name, stream})
		}
		if len(files) == 0 {
			skipped++
			continue
		}

		sm := slimMovie{
			I: m.MovieID,
			N: m.Name,
			Y: m.Year,
			T: m.TMDBID,
			M: m.IMDBID,
			F: files,
		}
		// type "movie" — дефолт, не сериализуем
		if strings.EqualFold(m.Type, "tv") {
			sm.K = "tv"
			sm.S = m.Season
		}
		slim.Items = append(slim.Items, sm)
	}
	slim.N = len(slim.Items)

	abs, _ := filepath.Abs(outPath)
	if err := writeSlim(outPath, &slim); err != nil {
		log.Fatalf("запись %s: %v", outPath, err)
	}
	st, _ := os.Stat(outPath)
	cdnNote := ""
	if cdnBase != "" {
		cdnNote = fmt.Sprintf(", CDN→%s", cdnBase)
	}
	log.Printf("EXPORT SLIM: %d фильмов → %s (%.1f МБ%s%s), пропущено без stream'ов: %d",
		slim.N, abs, float64(st.Size())/(1024*1024),
		gzipNote(outPath), cdnNote, skipped)
}

func pickBestStream(streams map[string]string) string {
	if len(streams) == 0 {
		return ""
	}
	for _, q := range []string{"1080p", "720p", "480p", "360p", "240p"} {
		if u := strings.TrimSpace(streams[q]); u != "" {
			return u
		}
	}
	for _, u := range streams {
		if s := strings.TrimSpace(u); s != "" {
			return s
		}
	}
	return ""
}

func writeSlim(path string, slim any) error {
	f, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	defer func() { os.Remove(path + ".tmp") }()

	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
		enc := json.NewEncoder(gz)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(slim); err != nil {
			gz.Close()
			f.Close()
			return err
		}
		if err := gz.Close(); err != nil {
			f.Close()
			return err
		}
	} else {
		enc := json.NewEncoder(f)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(slim); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func gzipNote(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		return ", gzip"
	}
	return ""
}

// ensure unused-import check (fmt) for golint-clean
var _ = fmt.Sprintf
