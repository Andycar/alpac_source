package litesrc

// Полный индекс брендов «Смотрим».
//
// Первая версия источника брала названия из /sitemap — это карта сайта, 899
// брендов, и на 294 реальных карточках прода она дала ОДНО попадание. Весь
// каталог лежит в /sitemap-brand-1.xml: 14 046 брендов, но там только адреса,
// без названий.
//
// Названия берутся из <title> страницы бренда. Страница весит под полмегабайта,
// зато заголовок стоит в самом начале — поэтому читаются первые 8 КБ и
// соединение рвётся. Замер на живом сайте: 100 брендов за 8 секунд, названия у
// всех ста; полный обход — около 19 минут и ~110 МБ.
//
// Почему не через их API: у «Смотрим» есть GraphQL (apis.smotrim.ru/graphql),
// и его схема вскрывается — `body` это хеш операции из бандла, `vars` — MD5 от
// JSON переменных с отсортированными ключами. Но шлюз пропускает ТОЛЬКО
// зарегистрированные пары «операция + конкретные переменные»: тот же запрос с
// чужим vars отдаёт заглушку вместо данных. Ни поиска, ни произвольной
// пагинации там не получить, а операции поиска по каталогу у них нет вовсе —
// в реестре из 72 операций есть только поиск по статьям журнала.

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	smotrimIndexFile = "smotrim_index.json"
	// Каталог вещателя меняется медленно: недельного обновления хватает, а
	// обход стоит 14 тысяч запросов к чужому сайту.
	smotrimIndexFresh = 7 * 24 * time.Hour
	// Столько читаем со страницы бренда: <title> стоит в первых килобайтах.
	smotrimTitleBytes = 8 << 10
	// Параллелизм обхода. Замерено на 12: 100 брендов за 8 с без отказов.
	smotrimCrawlWorkers = 12
)

var smotrimSitemapBrandRe = regexp.MustCompile(`<loc>https?://[^<]*?/brand/(\d+)</loc>`)

// smotrimIndexStore — полный индекс на диске плюс его копия в памяти.
type smotrimIndexStore struct {
	path   string
	host   string
	fetch  func(ctx context.Context, target, referer string) (string, bool)
	client *http.Client

	mu       sync.RWMutex
	brands   []smotrimBrand
	built    time.Time
	building bool
}

type smotrimIndexFileFormat struct {
	Built  int64           `json:"built"`
	Brands []smotrimStored `json:"brands"`
}

type smotrimStored struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Raw   string `json:"raw"`
	Year  int    `json:"year"`
}

func newSmotrimIndexStore(repoRoot, host string, client *http.Client, fetch func(context.Context, string, string) (string, bool)) *smotrimIndexStore {
	path := smotrimIndexFile
	if repoRoot != "" {
		path = filepath.Join(repoRoot, "database", smotrimIndexFile)
	}
	s := &smotrimIndexStore{path: path, host: host, client: client, fetch: fetch}
	s.loadFromDisk()
	return s
}

// Brands отдаёт полный индекс, если он есть. Второе значение — свежий ли он;
// протухший индекс всё равно возвращается (сериалы из него никуда не делись),
// но вызывающий знает, что пора перестроить.
func (s *smotrimIndexStore) Brands() ([]smotrimBrand, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.brands, time.Since(s.built) < smotrimIndexFresh
}

// EnsureFresh запускает перестроение в фоне, если индекса нет или он устарел.
// Запросы при этом не ждут: пока обход идёт, источник работает на том, что уже
// есть (в том числе на карте сайта).
func (s *smotrimIndexStore) EnsureFresh(ctx context.Context) {
	s.mu.Lock()
	if s.building || time.Since(s.built) < smotrimIndexFresh {
		s.mu.Unlock()
		return
	}
	s.building = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.building = false
			s.mu.Unlock()
		}()
		// Обход длинный — своя граница времени, не привязанная к запросу,
		// который его случайно запустил.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Minute)
		defer cancel()
		s.rebuild(bg)
	}()
}

func (s *smotrimIndexStore) rebuild(ctx context.Context) {
	started := time.Now()
	ids, ok := s.brandIDs(ctx)
	if !ok || len(ids) == 0 {
		log.Warn().Msg("smotrim: не удалось получить карту брендов, индекс не перестроен")
		return
	}
	log.Info().Int("brands", len(ids)).Msg("smotrim: строю полный индекс")

	type result struct {
		brand smotrimBrand
		ok    bool
	}
	results := make([]result, len(ids))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < smotrimCrawlWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					return
				}
				title, ok := s.brandTitle(ctx, ids[idx])
				if !ok {
					continue
				}
				clean := smotrimCleanTitle(title)
				if clean == "" {
					continue
				}
				year, _ := parseSmotrimYear(title)
				results[idx] = result{brand: smotrimBrand{id: ids[idx], title: clean, raw: title, year: year}, ok: true}
			}
		}()
	}
	for i := range ids {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	brands := make([]smotrimBrand, 0, len(results))
	for _, r := range results {
		if r.ok {
			brands = append(brands, r.brand)
		}
	}
	if len(brands) == 0 {
		log.Warn().Msg("smotrim: обход не дал ни одного названия, старый индекс сохранён")
		return
	}
	sort.Slice(brands, func(i, j int) bool { return brands[i].id < brands[j].id })

	s.mu.Lock()
	s.brands = brands
	s.built = time.Now()
	s.mu.Unlock()

	s.saveToDisk(brands)
	log.Info().Int("brands", len(brands)).Str("took", time.Since(started).Round(time.Second).String()).
		Msg("smotrim: полный индекс построен")
}

// brandIDs забирает адреса всех брендов из карты сайта.
func (s *smotrimIndexStore) brandIDs(ctx context.Context) ([]string, bool) {
	body, ok := s.fetch(ctx, s.host+"/sitemap-brand-1.xml", s.host+"/")
	if !ok {
		return nil, false
	}
	matches := smotrimSitemapBrandRe.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		if _, dup := seen[m[1]]; dup {
			continue
		}
		seen[m[1]] = struct{}{}
		out = append(out, m[1])
	}
	return out, len(out) > 0
}

// brandTitle читает ТОЛЬКО начало страницы бренда: <title> стоит в head, а сама
// страница весит под полмегабайта — дочитывать её незачем.
func (s *smotrimIndexStore) brandTitle(ctx context.Context, id string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.host+"/brand/"+id, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", anividsUA)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.Header.Set("Referer", s.host+"/")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, smotrimTitleBytes))
	if err != nil && len(head) == 0 {
		return "", false
	}
	title := submatch1(smotrimTitleRe, string(head))
	return title, title != ""
}

var smotrimTitleRe = regexp.MustCompile(`(?s)<title>(.*?)</title>`)

// parseSmotrimYear достаёт год из заголовка. Он приходит в двух видах:
// «Салями (2011)» и «Челночницы сериал 2016 смотреть онлайн».
func parseSmotrimYear(raw string) (int, bool) {
	if m := smotrimIndexYearRe.FindStringSubmatch(raw); len(m) > 1 {
		y := 0
		for _, c := range m[1] {
			y = y*10 + int(c-'0')
		}
		return y, true
	}
	for _, m := range smotrimLooseYearRe.FindAllStringSubmatch(raw, -1) {
		y := 0
		for _, c := range m[1] {
			y = y*10 + int(c-'0')
		}
		if y >= 1930 && y <= time.Now().Year()+1 {
			return y, true
		}
	}
	return 0, false
}

var smotrimLooseYearRe = regexp.MustCompile(`\b(\d{4})\b`)

func (s *smotrimIndexStore) loadFromDisk() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var f smotrimIndexFileFormat
	if err := json.Unmarshal(data, &f); err != nil || len(f.Brands) == 0 {
		return
	}
	brands := make([]smotrimBrand, 0, len(f.Brands))
	for _, b := range f.Brands {
		brands = append(brands, smotrimBrand{id: b.ID, title: b.Title, raw: b.Raw, year: b.Year})
	}
	s.mu.Lock()
	s.brands = brands
	s.built = time.Unix(f.Built, 0)
	s.mu.Unlock()
	log.Info().Int("brands", len(brands)).Time("built", time.Unix(f.Built, 0)).
		Msg("smotrim: индекс поднят с диска")
}

func (s *smotrimIndexStore) saveToDisk(brands []smotrimBrand) {
	stored := make([]smotrimStored, 0, len(brands))
	for _, b := range brands {
		stored = append(stored, smotrimStored{ID: b.id, Title: b.title, Raw: b.raw, Year: b.year})
	}
	data, err := json.Marshal(smotrimIndexFileFormat{Built: time.Now().Unix(), Brands: stored})
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	// Пишем через временный файл: обход длинный, и оборванная запись оставила
	// бы на диске битый индекс, который подняли бы при следующем старте.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Debug().Err(err).Msg("smotrim: не смог записать индекс")
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
	}
}
