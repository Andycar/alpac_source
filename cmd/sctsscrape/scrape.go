package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Scraper struct {
	Client        *Client
	Method        string
	IDParam       string
	OutPath       string
	RawDir        string
	FlushEvery    int
	RateLimit     time.Duration
	StopAfterMiss int
	Verbose       bool

	Catalog Catalog
	Stats   struct {
		Processed int
		Found     int
		Empty     int
		Errors    int
	}

	lastFlushAt int
}

func (s *Scraper) LoadExisting() error {
	b, err := os.ReadFile(s.OutPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	s.Catalog = c
	return nil
}

func (s *Scraper) Run(ctx context.Context, from, to int) error {
	if s.Catalog.Source == "" {
		s.Catalog.Source = "online.scts.tv"
	}

	consecutiveMiss := 0
	lastLog := time.Now()

	for id := from; id <= to; id++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		movies, missed, err := s.fetchOne(id)
		s.Stats.Processed++

		if err != nil {
			s.Stats.Errors++
			log.Printf("ID=%d ОШИБКА: %v", id, err)
		} else if missed {
			s.Stats.Empty++
			consecutiveMiss++
		} else if len(movies) > 0 {
			s.Stats.Found += len(movies)
			consecutiveMiss = 0
			s.Catalog.MergeMovies(movies)
		}
		s.Catalog.LastID = id

		if time.Since(lastLog) > 5*time.Second {
			log.Printf("прогресс: ID=%d processed=%d found=%d empty=%d errors=%d (подряд пусто: %d)",
				id, s.Stats.Processed, s.Stats.Found, s.Stats.Empty, s.Stats.Errors, consecutiveMiss)
			lastLog = time.Now()
		}

		if s.FlushEvery > 0 && s.Stats.Processed-s.lastFlushAt >= s.FlushEvery {
			if err := s.Flush(); err != nil {
				log.Printf("ПРЕДУПРЕЖДЕНИЕ: периодический flush: %v", err)
			}
			s.lastFlushAt = s.Stats.Processed
		}

		if s.StopAfterMiss > 0 && consecutiveMiss >= s.StopAfterMiss {
			log.Printf("СТОП: %d пустых ответов подряд (конец каталога?), останавливаюсь на ID=%d", consecutiveMiss, id)
			return nil
		}

		if s.RateLimit > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(s.RateLimit):
			}
		}
	}
	return nil
}

func (s *Scraper) fetchOne(id int) ([]Movie, bool, error) {
	params := url.Values{}
	params.Set(s.IDParam, strconv.Itoa(id))

	status, body, err := s.Client.Call(s.Method, params)
	if err != nil {
		return nil, false, err
	}
	if status == 403 {
		return nil, false, fmt.Errorf("403 Forbidden — PHPSESSID невалиден или IP не в allowlist SCTS")
	}
	if status == 404 {
		return nil, true, nil
	}
	if status != 200 {
		return nil, false, fmt.Errorf("HTTP %d: %s", status, snippet(body, 200))
	}

	if s.RawDir != "" {
		_ = os.WriteFile(filepath.Join(s.RawDir, fmt.Sprintf("%d.json", id)), body, 0o644)
	}

	movies, empty, err := parseMoviesFromBytes(body)
	if err != nil {
		return nil, false, fmt.Errorf("parse: %v (head: %s)", err, snippet(body, 200))
	}
	return movies, empty, nil
}

// LoadDump читает локальный JSON-дамп (массив фильмов или один фильм) и
// мерджит в каталог. Используется когда у пользователя есть выгрузка из
// DevTools, но автоматический scrape не доступен или не настроен.
func (s *Scraper) LoadDump(path string) (added, updated int, err error) {
	if s.Catalog.Source == "" {
		s.Catalog.Source = "online.scts.tv"
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	movies, _, err := parseMoviesFromBytes(body)
	if err != nil {
		return 0, 0, err
	}
	added, updated = s.Catalog.MergeMovies(movies)
	return
}

func (s *Scraper) Flush() error {
	s.Catalog.UpdatedAt = nowISO()
	s.Catalog.SortByID()

	tmp := s.OutPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&s.Catalog); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.OutPath)
}

func snippet(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
