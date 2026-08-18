package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// runEnrich — режим `--enrich`: проходится по существующему scts.json,
// для каждого фильма ищет совпадение в TMDB и заполняет tmdb_id/imdb_id/
// type/season. Resume встроен — фильмы с enriched_at пропускаются.
func runEnrich(outPath, tmdbKey, tmdbHost, lang string,
	workers, rateMs, timeoutSec, retries, yearSlack, flushEvery int,
	force, verbose bool) {

	key := strings.TrimSpace(tmdbKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("TMDB_API_KEY"))
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "ОШИБКА: для --enrich нужен TMDB API key.")
		fmt.Fprintln(os.Stderr, "Использовать: --tmdb-key=<KEY> или env TMDB_API_KEY=<KEY>")
		fmt.Fprintln(os.Stderr, "Получить: https://www.themoviedb.org/settings/api (бесплатно).")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("читаю %s...", outPath)
	body, err := os.ReadFile(outPath)
	if err != nil {
		log.Fatalf("чтение %s: %v", outPath, err)
	}
	var cat Catalog
	if err := json.Unmarshal(body, &cat); err != nil {
		log.Fatalf("разбор JSON: %v", err)
	}
	log.Printf("каталог: %d фильмов, источник=%s", len(cat.Movies), cat.Source)

	tmdb := newTMDB(tmdbHost, key, lang, time.Duration(timeoutSec)*time.Second, retries, verbose)

	toEnrich := 0
	for i := range cat.Movies {
		if force || cat.Movies[i].EnrichedAt == "" {
			toEnrich++
		}
	}
	log.Printf("к обогащению: %d (skip %d уже обогащённых)", toEnrich, len(cat.Movies)-toEnrich)
	if toEnrich == 0 {
		return
	}

	jobs := make(chan int, len(cat.Movies))
	var (
		mu        sync.Mutex
		processed int
		matched   int
		missed    int
		errors    int
	)

	go func() {
		for i := range cat.Movies {
			if !force && cat.Movies[i].EnrichedAt != "" {
				continue
			}
			select {
			case <-ctx.Done():
				close(jobs)
				return
			case jobs <- i:
			}
		}
		close(jobs)
	}()

	rateLimit := time.Duration(rateMs) * time.Millisecond
	var wg sync.WaitGroup
	lastLog := time.Now()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				start := time.Now()
				m := &cat.Movies[i]
				ok, eerr := enrichOne(ctx, tmdb, m, yearSlack)

				mu.Lock()
				processed++
				switch {
				case eerr != nil:
					errors++
					if verbose {
						log.Printf("[%d/%d] err id=%d %q: %v", processed, toEnrich, m.MovieID, m.Name, eerr)
					}
				case ok:
					matched++
				default:
					missed++
				}
				if processed > 0 && flushEvery > 0 && processed%flushEvery == 0 {
					_ = saveCatalog(outPath, &cat)
					log.Printf("[%d/%d] flush — matched=%d missed=%d err=%d", processed, toEnrich, matched, missed, errors)
				}
				if time.Since(lastLog) > 10*time.Second {
					log.Printf("[%d/%d] progress: matched=%d missed=%d err=%d", processed, toEnrich, matched, missed, errors)
					lastLog = time.Now()
				}
				mu.Unlock()

				if rem := rateLimit - time.Since(start); rem > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(rem):
					}
				}
			}
		}()
	}

	wg.Wait()

	if err := saveCatalog(outPath, &cat); err != nil {
		log.Fatalf("сохранение %s: %v", outPath, err)
	}
	abs, _ := filepath.Abs(outPath)
	log.Printf("ENRICH ГОТОВО: processed=%d matched=%d missed=%d errors=%d → %s",
		processed, matched, missed, errors, abs)
}

func saveCatalog(path string, cat *Catalog) error {
	cat.UpdatedAt = nowISO()
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cat); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
