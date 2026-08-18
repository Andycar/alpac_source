package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// har — минимальная структура HAR-файла (HTTP Archive 1.2).
type har struct {
	Log struct {
		Entries []harEntry `json:"entries"`
	} `json:"log"`
}

type harEntry struct {
	Request struct {
		Method string `json:"method"`
		URL    string `json:"url"`
	} `json:"request"`
	Response struct {
		Status  int `json:"status"`
		Content struct {
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Encoding string `json:"encoding"`
		} `json:"content"`
	} `json:"response"`
}

// LoadHAR разбирает HAR-файл, выдёргивает из него все JSON-ответы от
// api.php (и подобных эндпоинтов SCTS) и сливает фильмы в каталог.
//
// Сценарий: пользователь открывает каталог в браузере, листает все страницы
// вручную, в DevTools → Network → правый клик → "Save all as HAR with content".
// Получает один файл со всеми сетевыми запросами и ответами. Утилита вытащит
// из него полезные JSON-payloads, без необходимости знать имя метода.
func (s *Scraper) LoadHAR(path string) (added, updated, scanned int, err error) {
	if s.Catalog.Source == "" {
		s.Catalog.Source = "online.scts.tv"
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, err
	}
	var h har
	if err := json.Unmarshal(body, &h); err != nil {
		return 0, 0, 0, fmt.Errorf("разбор HAR: %v", err)
	}

	for _, e := range h.Log.Entries {
		if e.Response.Status != 200 {
			continue
		}
		if !strings.Contains(e.Request.URL, "scts.tv") {
			continue
		}
		text := e.Response.Content.Text
		if text == "" {
			continue
		}
		if strings.EqualFold(e.Response.Content.Encoding, "base64") {
			if dec, derr := base64.StdEncoding.DecodeString(text); derr == nil {
				text = string(dec)
			}
		}
		scanned++
		movies, _, perr := parseMoviesFromBytes([]byte(text))
		if perr != nil || len(movies) == 0 {
			continue
		}
		a, u := s.Catalog.MergeMovies(movies)
		added += a
		updated += u
	}
	return
}
