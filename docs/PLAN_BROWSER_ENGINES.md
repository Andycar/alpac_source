# Plan: Pluggable browser engines (chromedp / go-rod / playwright)

**Status:** draft — обсуждено 2026-05-17
**Цель:** опциональный выбор браузерного движка для CDP-операций (mirage capture, kinobase player extraction, turbo, zetflix, vibix и т.д.) через админ-UI. Сохранить текущий chromedp как default. Добавить go-rod и playwright как альтернативы, выбираемые на лету.

---

## Текущая ситуация

| Файл | Движок | Что делает |
|---|---|---|
| `internal/httpapi/mirage_browser.go` (1440 строк) | chromedp | Stealth JS + `Fetch.requestPaused` на `/movies/*` и `*.m3u8`, capture `Authorizations`/`Accepts-Controls`/`Borth`; SOCKS5 proxy |
| `internal/httpapi/turbo.go` (1221 строк) | chromedp | obrut.show + FlareSolverr, SOCKS5 :40007 |
| `internal/httpapi/zetflix.go` | chromedp | obrut.show base64 player config |
| `internal/httpapi/vibix.go` | chromedp | vbx-* URL resolve |
| `internal/httpapi/kinogo_browser.go` | chromedp | Cloudflare bypass |
| `internal/httpapi/rezka_browser.go`, `rezka_browser_pool.go` | chromedp | RHS resolve |
| `internal/httpapi/zona_browser.go` | chromedp | Zona |
| `internal/httpapi/kinobase_browser.go` (320 строк) | **Node.js + puppeteer-core subprocess** | kinobase player JS extraction |

17 Go-файлов импортируют `github.com/chromedp/chromedp`. `kinobase_browser.go` — единственный gop через Node subprocess (плохая аномалия, хочется убрать).

Admin UI: вкладка "Browser pool" в `admin_tg_panel.go` (line ~6836). Конфиг — `BrowserPoolConfig` в [config.go:1032](../internal/config/config.go).

---

## Архитектура

### Этап 1 — Фасад `internal/browser/`

Новый пакет с интерфейсами и реестром. Никакой зависимости на конкретный движок.

```go
package browser

type Engine interface {
    Name() string                                  // "chromedp" | "rod" | "playwright"
    Available() error                              // nil если движок готов к работе (deps есть)
    NewSession(ctx context.Context, opts SessionOptions) (Session, error)
    Close() error
}

type SessionOptions struct {
    UserDataDir  string
    UserAgent    string
    SocksProxy   string             // "127.0.0.1:40000"
    Headless     bool
    InitScripts  []string           // mirageStealthJS и т.п.
    ExtraFlags   []string           // chromium-only флаги (rod/chromedp игнорят неподдерживаемое)
}

type Session interface {
    Navigate(url string) error
    WaitNavigation(timeout time.Duration) error
    Eval(js string, out any) error
    SetCookie(cookie *http.Cookie, domain string) error
    Cookies(urlOrDomain string) ([]*http.Cookie, error)
    Hijack(patterns []string, h HijackHandler) (cancel func(), err error)
    Screenshot() ([]byte, error)
    HTML() (string, error)
    Close() error
}

type HijackHandler func(req HijackRequest)

type HijackRequest interface {
    URL() string
    Method() string
    Headers() http.Header
    PostData() []byte
    ResourceType() string

    // Управление request side:
    Continue() error                                      // продолжить как есть
    ContinueWith(method string, url string,
                 headers http.Header, body []byte) error  // модифицировать запрос
    Abort(reason string) error
    Fulfill(status int, headers http.Header, body []byte) error

    // Доступ к ответу (LoadResponse в Rod / Fetch.continueResponse в chromedp):
    LoadResponse() (status int, headers http.Header, body []byte, err error)
    ContinueResponse(status int, headers http.Header, body []byte) error
}
```

**Реестр:**
```go
var engines = map[string]Engine{}

func Register(name string, e Engine) { engines[name] = e }
func Get(name string) (Engine, error) { ... }
func List() []string { ... }     // отсортированный список имён доступных движков
func Default() Engine             // выбран в config.BrowserPool.Engine; fallback на "chromedp"
```

### Этап 2 — Реализации

#### 2.1 `engine_chromedp.go` (без build tag, всегда включён)
Обёртка существующего кода. **Не трогает** текущие файлы — `mirage_browser.go` и пр. остаются работать как раньше. Этот movement только для новых вызовов через фасад.

#### 2.2 `engine_rod.go` под `//go:build !no_rod` (по умолчанию включён)
- Зависимость: `github.com/go-rod/rod` v0.118+
- Маппинг:
  - `Session.Hijack` → `page.HijackRequests()`
  - `Session.LoadResponse` → `ctx.LoadResponse()`
  - `Session.Navigate` → `page.Navigate(url).MustWaitLoad()` (с timeout)
  - `InitScripts` → `page.EvalOnNewDocument`
- Build tag `no_rod` чтобы при желании убрать (рантайм только Linux/Mac/Windows — Rod кросс-платформенный).

#### 2.3 `engine_playwright.go` под `//go:build playwright`
- Зависимость: `github.com/playwright-community/playwright-go`
- **Auto-install:** в `Engine.Available()` проверяем наличие `~/.cache/ms-playwright/chromium-*/`. Если нет — `playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}})`. Это качает ~150MB в `~/.cache/ms-playwright`. В Docker (read-only FS) `Available()` вернёт ошибку, UI покажет «недоступно».
- Маппинг:
  - `Session.Hijack` → `page.Route(pattern, handler)`
  - `Session.LoadResponse` → `route.Fetch()`
  - `Session.ContinueResponse` → `route.Fulfill()`
  - `InitScripts` → `page.AddInitScript()`

#### 2.4 Реестр под build tag
```go
// engine_register_default.go (no build tag)
func init() { Register("chromedp", &chromedpEngine{}) }

// engine_register_rod.go (//go:build !no_rod)
func init() { Register("rod", &rodEngine{}) }

// engine_register_playwright.go (//go:build playwright)
func init() { Register("playwright", &playwrightEngine{}) }
```

`browser.List()` возвращает то что реально вкомпилировано → UI динамически показывает доступные опции.

### Этап 3 — Конфиг + Admin UI

#### 3.1 Конфиг — `internal/config/config.go`
Расширить `BrowserPoolConfig`:
```go
type BrowserPoolConfig struct {
    MaxConcurrent   int               `toml:"max_concurrent"`
    StreamCacheMax  int               `toml:"stream_cache_max"`
    StreamCacheTTLH int               `toml:"stream_cache_ttl_h"`

    Engine          string            `toml:"engine"`            // "chromedp" (default) | "rod" | "playwright"
    BalancerEngines map[string]string `toml:"balancer_engines"`  // mirage="rod", kinobase="rod"
}
```
TOML:
```toml
[browser_pool]
engine = "chromedp"
max_concurrent = 4
stream_cache_max = 2000
stream_cache_ttl_h = 8

[browser_pool.balancer_engines]
mirage = "rod"
kinobase = "rod"
```

#### 3.2 Admin API — `internal/httpapi/admin_stats.go`
Расширить `tgAdminBrowserPoolHandler`:
- `GET /api/admin/browser-pool` теперь возвращает:
  ```json
  {
    "engine": "chromedp",
    "max_concurrent": 4,
    ...,
    "balancer_engines": {"mirage": "rod"},
    "available_engines": ["chromedp", "rod"],
    "balancers": ["mirage", "kinobase", "turbo", "zetflix", "vibix", "kinogo", "rezka"]
  }
  ```
- `POST /api/admin/browser-pool` принимает `engine`, `balancer_engines`. Валидирует что выбранный движок есть в `browser.List()`.
- Новый: `POST /api/admin/browser-pool/install` — триггерит `playwright.Install()` если выбран playwright и not available. Стримит лог. (Только для playwright.)

#### 3.3 Admin UI — `admin_tg_panel.go` (existing tab "Browser pool")
Добавить блок над текущими настройками pool:
```html
<div class="cluster-block">
  <h3>Движок браузера</h3>
  <select id="bpEngine">
    <option value="chromedp">chromedp (по умолчанию)</option>
    <option value="rod">go-rod</option>
    <option value="playwright">Playwright</option>  <!-- disabled если недоступно -->
  </select>
  <button onclick="installPlaywright()">Установить Playwright</button>
  <p class="hint">Применение требует рестарта lampac.</p>

  <h4>Переопределения по балансерам</h4>
  <table>
    <tr><td>mirage</td><td><select data-bal="mirage">...</select></td></tr>
    <tr><td>kinobase</td><td><select data-bal="kinobase">...</select></td></tr>
    ...
  </table>
</div>
```

JS:
- `loadBrowserPool()` — fetch GET, populate selects, disable opt'ы не из `available_engines`
- `saveBrowserPool()` — POST с engine + per-balancer map
- `installPlaywright()` — POST `/install`, показывает лог в textarea

### Этап 4 — Миграция балансеров

Принцип: **существующий chromedp код не трогаем**. Создаём параллельный путь через фасад, активируется только если `BrowserPool.Engine != "chromedp"` или есть override в `BalancerEngines`.

> **Stealth coverage:** наш `mirageStealthJS` покрывает ~4 evasion'а
> (webdriver, chrome stub, languages, vendor). `go-rod/stealth.JS`
> покрывает 16 (включая `chrome.csi`, `chrome.loadTimes`,
> `iframe.contentWindow`, `media.codecs`, `navigator.permissions`,
> `navigator.plugins`, `navigator.hardwareConcurrency`, `webgl.vendor`,
> `window.outerdimensions`, `sourceurl`). При миграции на rod engine
> или при включении `UseStealth=true` на chromedp инжектируем
> `stealth.JS` ПЕРЕД `mirageStealthJS` — наши специфические подмены
> остаются поверх. Ни тот ни другой Cloudflare JS-challenge не обходит.

#### 4.1 Mirage (приоритет 1, самый сложный кейс)

`mirage_browser.go` → не меняется. Новый файл `mirage_browser_facade.go`:
```go
func mirageResolveViaFacade(ctx context.Context, ...) (*mirageResolveResult, error) {
    eng := browser.ForBalancer("mirage")
    session, err := eng.NewSession(ctx, browser.SessionOptions{
        UserDataDir: mirageDataDir(),
        SocksProxy:  cfg.Mirage.SocksProxy,
        InitScripts: []string{mirageStealthJS},
        Headless:    true,
    })
    defer session.Close()

    headersByMovies := map[string]http.Header{}
    headersByM3U8   := map[string]http.Header{}
    cancel, _ := session.Hijack([]string{"*/movies/*", "*.m3u8*"}, func(req browser.HijackRequest) {
        if strings.Contains(req.URL(), "/movies/") {
            headersByMovies[req.URL()] = req.Headers()
            req.Continue()
        } else if strings.HasSuffix(req.URL(), ".m3u8") || strings.Contains(req.URL(), ".m3u8?") {
            if req.Method() == "OPTIONS" {
                req.Fulfill(204, corsHeaders, nil)
            } else {
                headersByM3U8[req.URL()] = req.Headers()
                req.Continue()
            }
        } else {
            req.Continue()
        }
    })
    defer cancel()
    ...
}
```

Точка переключения в `mirage.go`:
```go
if browser.ForBalancer("mirage").Name() == "chromedp" {
    return mirageResolveViaBrowser(...)  // существующий
}
return mirageResolveViaFacade(...)        // новый
```

#### 4.2 Kinobase (приоритет 2, убираем Node.js)

`kinobase_browser.go` сейчас — Node subprocess. Альтернатива:
- Создаём `kinobase_browser_native.go` через фасад (работает на chromedp/rod/playwright)
- Если `browser.ForBalancer("kinobase").Name() == "node"` (специальный engine для legacy subprocess) — fallback на старое
- По умолчанию — нативный путь через chromedp
- **Чистое улучшение** — Node deps больше не нужны

Файл `kinobase_browser_native.go`:
```go
func (kb *kinobaseBrowser) ExtractNative(ctx context.Context, filmURL, proxyAddr string) (string, error) {
    eng := browser.ForBalancer("kinobase")
    session, _ := eng.NewSession(ctx, browser.SessionOptions{...})
    defer session.Close()

    // Inject playerjs stub перед навигацией
    session.Eval(`window.Playerjs = function(o){ ... }`, nil)
    session.Navigate(filmURL)
    session.WaitForSelector("#playerjsfile", 30*time.Second)
    var fileData string
    session.Eval(`document.querySelector('#playerjsfile').textContent`, &fileData)
    return fileData, nil
}
```

### Этап 5 — Build / Docker

`Makefile`:
```makefile
build:                ; go build -o lampac-go ./cmd/lampac-go
build-no-rod:         ; go build -tags no_rod -o lampac-go ./cmd/lampac-go
build-playwright:     ; go build -tags playwright -o lampac-go ./cmd/lampac-go
build-full:           ; go build -tags playwright -o lampac-go ./cmd/lampac-go
```

По умолчанию: `chromedp + rod`. `playwright` — opt-in build tag, т.к. тащит ~10MB depths.

Dockerfile: не меняем default. Если юзер хочет playwright — собирает локально с `-tags playwright`, при запуске админка предложит `playwright.Install()`.

---

## Поэтапная разбивка работы (для отдельных коммитов)

1. **`internal/browser/` skeleton** — типы, реестр, тесты-моки. ~400 строк. Никаких внешних deps.
2. **chromedp wrapper** (`engine_chromedp.go`) — реализация Engine/Session через chromedp. ~600 строк.
3. **go-rod engine** (`engine_rod.go`) — Engine/Session через rod. ~500 строк. Добавляет `github.com/go-rod/rod` в go.mod.
4. **Конфиг + Admin UI** — `BrowserPoolConfig.Engine/BalancerEngines`, расширение API+UI вкладки. ~300 строк.
5. **Mirage миграция** (`mirage_browser_facade.go`) — параллельный путь через фасад, переключатель в `mirage.go`. ~400 строк.
6. **Kinobase миграция** (`kinobase_browser_native.go`) — нативный путь, замена Node subprocess для chromedp/rod. ~250 строк.
7. **Playwright engine** (отдельным коммитом, build tag) — `engine_playwright.go` + auto-install endpoint. ~700 строк. Добавляет `playwright-go` в go.mod.

Итого ~3150 строк нового кода, 0 удалений в первой итерации (старый код остаётся как fallback).

---

## Открытые вопросы / риски

1. **CDP differences:** `Fetch.requestPaused` в chromedp даёт raw access к request/response. Rod's `HijackRequests` обёртка скрывает часть; для mirage нужен `ctx.LoadResponse() + ctx.Response.Body()`. Проверить на этапе 3 что headers захватываются как сейчас.
2. **Browser pool sharing:** сейчас mirage держит долгоживущий master-browser (`bp.allocCtx`). Rod это `rod.Browser`, Playwright это `playwright.BrowserContext`. Pool логика придётся дублировать в каждом engine — это OK, унификация в фасаде сложна.
3. **Per-balancer engine swap:** требует рестарта lampac. Можно сделать live-swap (закрыть старый pool, открыть новый) — но это +200 строк сложности. **Решение:** v1 — рестарт обязателен, в UI предупреждение.
4. **Playwright в Docker:** auto-install требует RW `~/.cache/ms-playwright`. В Docker нужно прокинуть volume. Документируем.
5. **Memory:** Rod + Playwright + chromedp одновременно в одном процессе = до 3 browser pools. В UI ограничить — выбор global engine применяется ко всем балансерам которых нет в override.
