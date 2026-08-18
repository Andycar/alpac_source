// sctsscrape — community-driven каталог-парсер для online.scts.tv.
//
// Утилита запускается абонентом сети SCTS (или с IP в их AS51004) и собирает
// JSON-каталог всех фильмов через JsHttpRequest API. Файл потом шарится в
// community для использования lampac-go без проксей (прямой стрим с открытого
// CDN dl.yama.scts.tv).
//
// Запуск:
//   go run ./cmd/sctsscrape --session=<PHPSESSID> --to=200000 --out=scts.json
//
// Подробности — README.md рядом.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		session       = flag.String("session", "", "PHPSESSID cookie value (обязательно)")
		baseURL       = flag.String("base", "http://online.scts.tv", "база сайта (без trailing slash)")
		method        = flag.String("method", "VideoCustom.getMovie", "JsHttpRequest метод")
		idParam       = flag.String("id-param", "id", "имя POST-параметра для ID фильма")
		from          = flag.Int("from", 1, "начальный movie_id")
		to            = flag.Int("to", 200000, "конечный movie_id (включительно)")
		stopAfterMiss = flag.Int("stop-after-empty", 500, "стоп после стольких подряд пустых/404 ответов (0 = не останавливаться)")
		outPath       = flag.String("out", "scts.json", "итоговый нормализованный JSON")
		rawDir        = flag.String("raw", "", "директория для сырых ответов (опционально, дамп всех 200-ответов)")
		rateMs        = flag.Int("rate-ms", 250, "пауза между запросами, мс (вежливость к серверу)")
		userAgent     = flag.String("ua", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 YaBrowser/26.4.0.0 Safari/537.36", "User-Agent")
		timeoutSec    = flag.Int("timeout", 30, "HTTP-таймаут одного запроса, секунд")
		retries       = flag.Int("retries", 3, "ретраев при сетевой ошибке / 5xx")
		flushEvery    = flag.Int("flush-every", 50, "сохранять scts.json каждые N фильмов")
		resume        = flag.Bool("resume", true, "продолжить с последнего обработанного ID если scts.json уже есть")
		verbose       = flag.Bool("v", false, "verbose логирование")
		probe         = flag.Int("probe", 0, "PROBE: сделать ОДИН запрос для указанного ID, вывести сырой ответ и выйти (для дебага API)")
		fromDump      = flag.String("from-dump", "", "обработать локальный JSON-дамп (массив фильмов из DevTools) и записать в --out, без сетевых запросов")
		fromHAR       = flag.String("from-har", "", "обработать HAR-файл (Save all as HAR with content из DevTools) и собрать каталог из всех ответов api.php")
		clean         = flag.Bool("clean", false, "режим чистки: убрать из --out мусорные записи (movie_id > 10M, без files и без name)")
		exportSlim    = flag.String("export-slim", "", "режим экспорта: построить slim-JSON для JS-балансера (имя файла; .gz сжимает)")
		cdnBase       = flag.String("cdn-base", "", "для --export-slim: подменить префикс stream-URL (например https://cdn.alcopa.cc/scts) — нужно для HTTPS-Lampa")
		enrich        = flag.Bool("enrich", false, "режим обогащения: пройтись по --out и заполнить tmdb_id/imdb_id/type/season через TMDB API")
		tmdbKey       = flag.String("tmdb-key", "", "TMDB API key (для --enrich; https://www.themoviedb.org/settings/api или env TMDB_API_KEY)")
		tmdbHost      = flag.String("tmdb-host", "https://api.themoviedb.org", "TMDB API host для --enrich")
		tmdbLang      = flag.String("tmdb-lang", "ru-RU", "TMDB language для --enrich")
		enrichWorkers = flag.Int("enrich-workers", 4, "конкурентных TMDB-запросов в --enrich")
		enrichRateMs  = flag.Int("enrich-rate-ms", 60, "пауза между TMDB-запросами одного воркера, мс")
		yearSlack     = flag.Int("year-slack", 2, "допуск года при матчинге TMDB (для сериалов / переизданий)")
		enrichForce   = flag.Bool("enrich-force", false, "переобогащать даже если enriched_at уже стоит")
	)
	flag.Parse()

	if *exportSlim != "" {
		runExportSlim(*outPath, *exportSlim, *cdnBase)
		return
	}

	if *clean {
		scraper := &Scraper{OutPath: *outPath}
		if err := scraper.LoadExisting(); err != nil {
			log.Fatalf("чтение %s: %v", *outPath, err)
		}
		before := len(scraper.Catalog.Movies)
		removed, kept := scraper.Catalog.CleanCatalog()
		if err := scraper.Flush(); err != nil {
			log.Fatalf("сохранение %s: %v", *outPath, err)
		}
		log.Printf("CLEAN ГОТОВО: было %d, удалено %d мусорных, осталось %d", before, removed, kept)
		return
	}

	if *enrich {
		runEnrich(*outPath, *tmdbKey, *tmdbHost, *tmdbLang,
			*enrichWorkers, *enrichRateMs, *timeoutSec, *retries,
			*yearSlack, *flushEvery, *enrichForce, *verbose)
		return
	}

	if *fromDump != "" {
		scraper := &Scraper{OutPath: *outPath}
		if *resume {
			_ = scraper.LoadExisting()
		}
		added, updated, err := scraper.LoadDump(*fromDump)
		if err != nil {
			log.Fatalf("дамп %s: %v", *fromDump, err)
		}
		if err := scraper.Flush(); err != nil {
			log.Fatalf("сохранение %s: %v", *outPath, err)
		}
		abs, _ := filepath.Abs(*outPath)
		log.Printf("ДАМП ОБРАБОТАН: добавлено %d, обновлено %d. Всего в каталоге: %d. Файл: %s",
			added, updated, len(scraper.Catalog.Movies), abs)
		return
	}

	if *fromHAR != "" {
		scraper := &Scraper{OutPath: *outPath}
		if *resume {
			_ = scraper.LoadExisting()
		}
		added, updated, scanned, err := scraper.LoadHAR(*fromHAR)
		if err != nil {
			log.Fatalf("HAR %s: %v", *fromHAR, err)
		}
		if err := scraper.Flush(); err != nil {
			log.Fatalf("сохранение %s: %v", *outPath, err)
		}
		abs, _ := filepath.Abs(*outPath)
		log.Printf("HAR ОБРАБОТАН: просмотрено %d ответов, добавлено %d, обновлено %d. Всего в каталоге: %d. Файл: %s",
			scanned, added, updated, len(scraper.Catalog.Movies), abs)
		return
	}

	if strings.TrimSpace(*session) == "" {
		fmt.Fprintln(os.Stderr, "ОШИБКА: --session=PHPSESSID обязателен.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Как получить PHPSESSID:")
		fmt.Fprintln(os.Stderr, "  1) Открыть http://online.scts.tv/ в браузере (нужен IP абонента SCTS).")
		fmt.Fprintln(os.Stderr, "  2) DevTools (F12) → консоль → выполнить:  document.cookie")
		fmt.Fprintln(os.Stderr, "  3) Скопировать значение PHPSESSID=... (32 символа hex).")
		fmt.Fprintln(os.Stderr, "")
		flag.Usage()
		os.Exit(2)
	}
	if *to < *from {
		log.Fatalf("--to (%d) < --from (%d)", *to, *from)
	}
	if *rawDir != "" {
		if err := os.MkdirAll(*rawDir, 0o755); err != nil {
			log.Fatalf("создать --raw=%s: %v", *rawDir, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cli := newClient(clientConfig{
		BaseURL:   strings.TrimRight(*baseURL, "/"),
		Session:   *session,
		UserAgent: *userAgent,
		Timeout:   time.Duration(*timeoutSec) * time.Second,
		Retries:   *retries,
		Verbose:   *verbose,
	})

	if *probe > 0 {
		runProbe(cli, *method, *idParam, *probe)
		return
	}

	scraper := &Scraper{
		Client:        cli,
		Method:        *method,
		IDParam:       *idParam,
		OutPath:       *outPath,
		RawDir:        *rawDir,
		FlushEvery:    *flushEvery,
		RateLimit:     time.Duration(*rateMs) * time.Millisecond,
		StopAfterMiss: *stopAfterMiss,
		Verbose:       *verbose,
	}

	if *resume {
		if err := scraper.LoadExisting(); err != nil {
			log.Printf("ПРЕДУПРЕЖДЕНИЕ: не удалось прочитать %s для resume: %v (стартую с чистого)", *outPath, err)
		} else if scraper.Catalog.LastID > 0 {
			if scraper.Catalog.LastID >= *from {
				log.Printf("RESUME: продолжаю с ID %d (в каталоге уже %d фильмов)", scraper.Catalog.LastID+1, len(scraper.Catalog.Movies))
				*from = scraper.Catalog.LastID + 1
			}
		}
	}

	abs, _ := filepath.Abs(*outPath)
	log.Printf("SCTS scraper: %s → ID %d..%d, метод=%s, rate=%v, выход=%s",
		cli.cfg.BaseURL, *from, *to, *method, scraper.RateLimit, abs)
	log.Printf("PHPSESSID: %s...%s (Go %s)", (*session)[:6], (*session)[len(*session)-4:], runtime.Version())

	if err := scraper.Run(ctx, *from, *to); err != nil {
		if ctx.Err() != nil {
			log.Printf("ОСТАНОВЛЕНО пользователем — сохраняю результат...")
		} else {
			log.Printf("ОШИБКА: %v", err)
		}
	}

	if err := scraper.Flush(); err != nil {
		log.Fatalf("финальный flush %s: %v", *outPath, err)
	}

	log.Printf("ГОТОВО. Фильмов в каталоге: %d. Файл: %s", len(scraper.Catalog.Movies), abs)
	if scraper.Stats.Errors > 0 {
		log.Printf("Ошибок при запросах: %d (см. лог выше)", scraper.Stats.Errors)
	}
}
