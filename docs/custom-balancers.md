# Кастомные балансеры

Кастомные балансеры позволяют добавлять новые источники контента без изменения основного кода. Каждый балансер — это standalone Go-программа, которая запускается как subprocess и общается с основным сервером по HTTP.

## Архитектура

```
Lampa (клиент)
    │
    ▼
lampac-go (основной сервер, порт 888)
    │
    ├── /lite/source/rezka      → встроенный балансер
    ├── /lite/source/collaps     → встроенный балансер
    ├── /lite/source/uaflix      → reverse proxy → 127.0.0.1:50001 (subprocess)
    └── /lite/source/mybalancer  → reverse proxy → 127.0.0.1:50002 (subprocess)
```

Основной сервер:
1. Сканирует `custom_balancers/` при старте
2. Компилирует и запускает балансеры с `auto_start: true`
3. Проксирует запросы `/lite/source/{name}` на соответствующий порт
4. Мониторит состояние subprocess (перезапуск при падении)

## Быстрый старт

### 1. Создать директорию

```
custom_balancers/
  mybalancer/
    main.go         ← ваш код
    config.json     ← конфигурация
```

### 2. Написать config.json

```json
{
  "name": "mybalancer",
  "display_name": "My Balancer",
  "port": 0,
  "quality_badge": "FHD",
  "content_type": "both",
  "host": "https://example-source.com",
  "auto_start": true
}
```

| Поле | Тип | Описание |
|------|-----|----------|
| `name` | string | Уникальный идентификатор (`a-zA-Z0-9_-`, макс 64 символа) |
| `display_name` | string | Название в UI (если пусто, используется `name`) |
| `port` | int | TCP-порт (0 = автоматическое назначение, начиная с 50000) |
| `quality_badge` | string | Бейдж качества: `SD`, `HD`, `FHD`, `4K` |
| `content_type` | string | `movie`, `serial` или `both` |
| `host` | string | Upstream URL источника (передаётся как флаг `-host`) |
| `auto_start` | bool | Запускать при старте сервера |

### 3. Написать main.go

Минимальный шаблон:

```go
package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "net/http"
)

func main() {
    port := flag.Int("port", 50100, "listen port")
    mainHost := flag.String("main-host", "", "main server address")
    host := flag.String("host", "", "upstream source URL")
    token := flag.String("token", "", "API token")
    flag.Parse()

    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        q := r.URL.Query()

        // Проверка доступности контента
        if q.Get("checksearch") == "true" {
            handleCheckSearch(w, r, *host)
            return
        }

        // Основной поиск и выдача
        handleIndex(w, r, *host, *mainHost)
    })

    addr := fmt.Sprintf("127.0.0.1:%d", *port)
    log.Printf("mybalancer: listening on %s", addr)
    log.Fatal(http.ListenAndServe(addr, mux))
}
```

### 4. Деплой

**Через админку (рекомендуется):**

```
POST /{admin}/api/custbal
{
  "action": "deploy",
  "name": "mybalancer",
  "code": "package main\n...",
  "config": { "display_name": "My Balancer", "quality_badge": "FHD", "auto_start": true }
}
```

**Вручную:**

```bash
# Положить файлы в custom_balancers/mybalancer/
# Перезапустить сервер или вызвать через API:
POST /{admin}/api/custbal
{"action": "compile", "name": "mybalancer"}

POST /{admin}/api/custbal
{"action": "start", "name": "mybalancer"}
```

## HTTP API контракт

Каждый балансер должен реализовать единственный endpoint `GET /` с query-параметрами.

### checksearch — проверка доступности

```
GET /?checksearch=true&kinopoisk_id=123&title=Movie&original_title=Movie
```

Ответ:
```json
{"rch": true}   // контент найден
{"rch": false}  // контент не найден
```

Этот запрос используется для кросс-поиска. Должен работать быстро (< 5 сек).

### Поиск фильма

```
GET /?kinopoisk_id=123&title=Название&original_title=Original&year=2024
```

Если найден один результат — вернуть данные для воспроизведения.
Если найдено несколько — вернуть список для выбора.

### Формат ответа

Все ответы — JSON. Поле `type` определяет тип данных:

#### Фильм (одна ссылка на воспроизведение)

```json
{
  "type": "movie",
  "data": [
    {
      "method": "play",
      "url": "https://cdn.example.com/stream.m3u8",
      "stream": "https://cdn.example.com/stream.m3u8",
      "name": "Название",
      "title": "Название / Original Title"
    }
  ]
}
```

#### Список похожих результатов

```json
{
  "type": "similar",
  "data": [
    {
      "method": "link",
      "url": "/lite/source/mybalancer?kinopoisk_id=123&href=https://...",
      "name": "Вариант 1",
      "year": 2024,
      "img": "https://poster.jpg"
    }
  ]
}
```

#### Список сезонов (сериал)

```json
{
  "type": "season",
  "data": [
    {
      "method": "link",
      "url": "/lite/source/mybalancer?kinopoisk_id=123&serial=1&s=1&...",
      "name": "1 сезон"
    }
  ]
}
```

#### Список серий

```json
{
  "type": "episode",
  "data": [
    {
      "method": "play",
      "url": "https://cdn.example.com/s01e01.m3u8",
      "stream": "https://cdn.example.com/s01e01.m3u8",
      "s": 1,
      "e": 1,
      "name": "Серия 1",
      "title": "Название / Original"
    }
  ]
}
```

#### Пустой результат

```json
{"type": "empty", "data": []}
```

### Методы в data

| method | Описание |
|--------|----------|
| `play` | Прямая ссылка на поток (m3u8/mp4) — запуск плеера |
| `call` | URL для повторного запроса к балансеру (lazy-load) |
| `link` | Навигационная ссылка (переход на другой экран) |

### Query-параметры

| Параметр | Описание |
|----------|----------|
| `kinopoisk_id` | ID Кинопоиска |
| `imdb_id` | IMDB ID |
| `title` | Русское название |
| `original_title` | Оригинальное название |
| `year` | Год выхода |
| `serial` | `1` если сериал |
| `s` | Номер сезона (`-1` = список сезонов) |
| `e` | Номер серии |
| `t` | Озвучка / голос |
| `href` | URL конкретного результата (после выбора из similar) |
| `rjson` | `true` — всегда JSON (без HTML fallback) |

## Проксирование потоков

Для корректной работы CORS и шифрования ссылок, потоковые URL нужно оборачивать через proxystream основного сервера.

```go
func buildStreamUrl(link string, mainHost string, r *http.Request) string {
    if mainHost == "" {
        return link
    }

    type proxyReq struct {
        URL    string `json:"url"`
        Plugin string `json:"plugin"`
    }
    type proxyResp struct {
        ProxyURL string `json:"proxy_url"`
    }

    body, _ := json.Marshal(proxyReq{URL: link, Plugin: "mybalancer"})
    req, _ := http.NewRequest("POST", mainHost+"/api/proxystream", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")

    // Пробросить заголовки клиента для корректного построения proxy URL
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        req.Header.Set("X-Forwarded-For", xff)
    }
    if fhost := r.Header.Get("X-Forwarded-Host"); fhost != "" {
        req.Header.Set("X-Forwarded-Host", fhost)
    }
    if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
        req.Header.Set("X-Forwarded-Proto", proto)
    }

    resp, err := http.DefaultClient.Do(req)
    if err != nil || resp.StatusCode != 200 {
        return link // fallback на прямую ссылку
    }
    defer resp.Body.Close()

    var result proxyResp
    json.NewDecoder(resp.Body).Decode(&result)
    if result.ProxyURL != "" {
        return result.ProxyURL
    }
    return link
}
```

## Флаги запуска

Основной сервер передаёт subprocess следующие флаги:

| Флаг | Описание |
|------|----------|
| `-port` | TCP-порт для прослушивания |
| `-main-host` | Адрес основного сервера (для proxystream) |
| `-host` | Upstream URL из config.json |
| `-token` | API-токен (если указан в config.json, будущее) |

Балансер должен слушать на `127.0.0.1:{port}`.

## Admin API

Все запросы требуют Telegram-авторизации.

### Список балансеров

```
GET /{admin}/api/custbal
```

Ответ:
```json
[
  {
    "name": "uaflix",
    "display_name": "UAFlix",
    "state": "running",
    "port": 50001,
    "pid": 12345,
    "quality_badge": "FHD",
    "uptime_sec": 3600.5
  }
]
```

### Управление

```
POST /{admin}/api/custbal
Content-Type: application/json
```

| action | Параметры | Описание |
|--------|-----------|----------|
| `start` | `name` | Запустить subprocess |
| `stop` | `name` | Остановить (SIGINT -> SIGKILL) |
| `restart` | `name` | Перезапуск |
| `compile` | `name` | Перекомпилировать из исходников |
| `remove` | `name` | Удалить (остановить + удалить директорию) |
| `deploy` | `name`, `code`, `config` | Создать/обновить балансер |

### Deploy пример

```json
{
  "action": "deploy",
  "name": "mybalancer",
  "code": "package main\n\nimport (\n\t\"flag\"\n\t\"fmt\"\n\t\"log\"\n\t\"net/http\"\n)\n\nfunc main() {\n\tport := flag.Int(\"port\", 50100, \"\")\n\tflag.Parse()\n\thttp.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {\n\t\tw.Write([]byte(`{\"rch\":true}`))\n\t})\n\tlog.Fatal(http.ListenAndServe(fmt.Sprintf(\"127.0.0.1:%d\", *port), nil))\n}",
  "config": {
    "display_name": "My Balancer",
    "quality_badge": "FHD",
    "content_type": "both",
    "host": "https://example.com",
    "auto_start": true
  }
}
```

## Жизненный цикл

```
Deploy/ScanAndStart
    │
    ▼
[stopped] ──start──▶ [starting] ──TCP ready──▶ [running]
    ▲                                              │
    │                                          crash/stop
    │                                              │
    └──────────────────────────────────────────────┘
                                               [error]
```

- **starting**: subprocess запущен, ожидание TCP (15 сек, опрос каждые 300 мс)
- **running**: TCP-порт отвечает, трафик проксируется
- **error**: subprocess упал или TCP не ответил за 15 сек
- **stopped**: subprocess остановлен (SIGINT, 5 сек таймаут, затем SIGKILL)

## Кэширование

Рекомендуется кэшировать ответы upstream-а в памяти:

```go
type cacheItem struct {
    value     any
    expiresAt time.Time
}

type cache struct {
    mu    sync.RWMutex
    items map[string]cacheItem
}

func (c *cache) get(key string) (any, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    item, ok := c.items[key]
    if !ok || time.Now().After(item.expiresAt) {
        return nil, false
    }
    return item.value, true
}

func (c *cache) set(key string, val any, ttl time.Duration) {
    c.mu.Lock()
    c.items[key] = cacheItem{val, time.Now().Add(ttl)}
    c.mu.Unlock()
}
```

TTL рекомендации:
- checksearch: 10-20 мин
- поиск: 15-30 мин
- серии/сезоны: 30-60 мин

## Рабочий пример

Полный рабочий балансер: [`custom_balancers/uaflix/main.go`](../custom_balancers/uaflix/main.go)

Этот балансер парсит украинский источник, реализует:
- Поиск по названию
- Разрешение неоднозначности (multiple results -> similar)
- Агрегацию сериалов (сезоны, серии, озвучки)
- Проксирование потоков через proxystream
- In-memory кэш

## Конвертер C# -> Go (Porter)

Через админку можно автоматически конвертировать C# балансеры из старого Lampac в Go:

```
POST /{admin}/api/porter
{
  "name": "mybalancer",
  "csharp_code": "using System; ..."
}
```

Использует LLM (настраивается в `[llm]` секции config.toml). Результат автоматически деплоится как кастомный балансер.

## Ограничения

- Балансер должен быть standalone Go-программой (без CGO)
- Имя: `a-zA-Z0-9_-`, максимум 64 символа
- Один балансер = один TCP-порт (127.0.0.1)
- Subprocess запускается от того же пользователя, что и основной сервер
- При обновлении через `deploy.sh` / `install.sh` директория `custom_balancers/` сохраняется
