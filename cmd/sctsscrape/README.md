# sctsscrape

Community-парсер каталога [online.scts.tv](http://online.scts.tv) — закрытого
видео-каталога провайдера **Sakhalin Cable Telesystems** (AS51004, Сахалин).

Каталог доступен только из подсетей абонентов SCTS (nginx allow-list блокирует
все внешние IP). Но CDN-хост `dl.yama.scts.tv` где лежат сами файлы — **открыт
миру** и отдаёт MP4 с поддержкой Range. То есть если получить список путей
к файлам, любой клиент в любой точке может стримить контент напрямую.

Идея этой утилиты — **разовый сбор каталога абонентом SCTS**. Результат —
один JSON-файл с маппингом `movie → {kp_id, imdb_id, files[].streams[*]}`,
который потом шарится в community (например, gist/GitHub) и используется
балансером lampac-go для отдачи прямых ссылок на CDN.

## Кому это надо

Утилиту имеет смысл запускать только если у вас:

- IP в сети SCTS (188.113.128.0/18 или 185.74.228.0/22), или
- VPN/прокси с выходом в эту сеть.

Проверка: откройте в браузере http://online.scts.tv/ — если открылся каталог
(а не 403 Forbidden от nginx), всё ок.

Запускать парсер с любого другого IP бесполезно — будете получать 403.

## Режимы работы

Утилита умеет три вещи:

### 1. `--probe=<ID>` — диагностика API

Делает один запрос для указанного `ID`, печатает HTTP-статус и сырое тело
ответа в консоль, и выходит. Используется чтобы убедиться что PHPSESSID
рабочий и формат ответа совпадает с ожидаемым.

```bash
./sctsscrape --session=<PHPSESSID> --probe=25500
```

### 2. `--from-dump=<file>` — обработать локальный JSON-дамп

Если автоматический брут не подходит (метод/параметры API неизвестны),
можно зайти в DevTools, посмотреть какие запросы делает сайт при скролле
каталога, скопировать ответы в файл и скормить парсеру:

```bash
./sctsscrape --from-dump=scts_dump.json --out=scts.json
```

Файл должен содержать массив фильмов (`[{id, name, year, files}, ...]`)
или один фильм (`{id, name, ...}`) — оба формата поддерживаются. Если
`--out` уже существует и `--resume=true` (по умолчанию), дамп **сольётся**
с тем что уже есть (по `movie_id`). Несколько дампов можно скармливать
подряд — каталог будет постепенно расти.

### 3. Автоматический брут по ID (по умолчанию)

```bash
./sctsscrape --session=<PHPSESSID> --to=200000 --rate-ms=300
```

Перебирает `movie_id` от `--from` до `--to`, вызывая JsHttpRequest API
(метод по умолчанию `VideoCustom.getMovie`, параметр `id`).

### 4. `--enrich` — обогатить каталог через TMDB

Заполняет `tmdb_id`, `imdb_id`, `type` (movie/tv) и `season` для каждого фильма
в `--out`. Требуется бесплатный TMDB API key
(https://www.themoviedb.org/settings/api → Request).

```bash
./sctsscrape --enrich --out=scts.json --tmdb-key=<KEY>
# или
export TMDB_API_KEY=<KEY>
./sctsscrape --enrich --out=scts.json
```

Resume встроен — фильмы с уже выставленным `enriched_at` пропускаются.
Дополнительные флаги:

| Флаг | По умолчанию | Назначение |
|---|---|---|
| `--enrich-workers` | 4 | конкурентных TMDB-запросов |
| `--enrich-rate-ms` | 60 | пауза между запросами одного воркера, мс |
| `--year-slack` | 2 | допуск года при матчинге (для сериалов / переизданий) |
| `--enrich-force` | false | переобогащать даже уже обогащённые |
| `--tmdb-lang` | ru-RU | язык поиска TMDB |

На 22k фильмов с 4 воркерами и 60 мс прогон занимает ~12-15 минут (45k запросов,
TMDB лимит ~40 req/sec на IP — попадаем в норму, редкие 429 ретраятся).

## Сборка

Нужен Go 1.22+ (в нашем проекте — 1.26).

```bash
# Из корня репозитория lampac-go
go build -o sctsscrape ./cmd/sctsscrape

# Кросс-компиляция под Windows
GOOS=windows GOARCH=amd64 go build -o sctsscrape.exe ./cmd/sctsscrape

# Под Linux
GOOS=linux GOARCH=amd64 go build -o sctsscrape-linux ./cmd/sctsscrape
```

## Использование

### Шаг 1. Получить PHPSESSID

1. Открыть http://online.scts.tv/ в браузере.
2. Нажать F12 → вкладка «Консоль» → выполнить:
   ```js
   document.cookie
   ```
3. Скопировать значение `PHPSESSID=...` (32 hex-символа).

### Шаг 2. Запустить парсер

```bash
./sctsscrape \
  --session=0304292ceccc3i51jcn1ubua11 \
  --from=1 \
  --to=200000 \
  --out=scts.json \
  --rate-ms=300
```

Параметры:

| Флаг | По умолчанию | Назначение |
|---|---|---|
| `--session` | (обязательно) | значение cookie PHPSESSID |
| `--from` | 1 | начальный movie_id |
| `--to` | 200000 | конечный movie_id |
| `--out` | scts.json | путь к итоговому JSON |
| `--raw` | (пусто) | если задано — дамп сырых ответов как `<dir>/<id>.json` для отладки |
| `--rate-ms` | 250 | пауза между запросами, мс (не быть агрессивным к серверу) |
| `--stop-after-empty` | 500 | стоп после стольких подряд пустых ответов |
| `--method` | VideoCustom.getMovie | имя JsHttpRequest-метода |
| `--id-param` | id | имя POST-параметра для ID |
| `--retries` | 3 | ретраев при 5xx / сетевой ошибке |
| `--timeout` | 30 | HTTP-таймаут одного запроса, секунд |
| `--flush-every` | 50 | сохранять JSON каждые N фильмов |
| `--resume` | true | продолжить с последнего обработанного ID (читает scts.json) |
| `-v` | false | подробный лог |

### Шаг 3. Прогресс и устойчивость

- Парсер делает один запрос на ID с паузой `rate-ms`. На каталоге ~50 тыс.
  фильмов с 300 мс это около 4 часов.
- Каждые 50 обработанных ID файл `scts.json` атомарно перезаписывается.
- Прерывание (Ctrl+C) — сохраняет состояние и выходит.
- Повторный запуск с `--resume` (по умолчанию включён) продолжит с того места,
  где остановился.

Если параметры запроса API в SCTS изменятся, метод можно подменить флагом
`--method=VideoCustom.getFullMovie` и т.п.

### Шаг 4. Поделиться результатом

Готовый `scts.json` положить в публичный gist / репозиторий / IPFS.
Балансер `scts` в lampac-go будет тянуть его раз в сутки.

## Структура итогового JSON

```json
{
  "source": "online.scts.tv",
  "updated_at": "2026-05-15T12:00:00Z",
  "last_id": 50231,
  "total_found": 28415,
  "movies": [
    {
      "movie_id": 12345,
      "name": "Готов или нет 2",
      "international_name": "Ready or Not 2: Here I Come",
      "year": 2026,
      "kp_id": "...",
      "imdb_id": "tt...",
      "genres": ["Триллер", "Комедия"],
      "files": [
        {
          "file_id": 99887,
          "name": "Ready.or.Not.2.2026.WEBRip.H264.DD51.mkv.mp4",
          "size": 5088059273,
          "active": true,
          "quality": "1080p",
          "resolution": "1920x1080",
          "duration_sec": 6543,
          "audio": ["AC3 5.1 RUS dub", "AC3 5.1 ENG"],
          "streams": {
            "720p":  "http://dl.yama.scts.tv/online/R/E/.../Ready.or.Not.2.720p.mp4",
            "1080p": "http://dl.yama.scts.tv/online/R/E/.../Ready.or.Not.2.2026.WEBRip.H264.DD51.mkv.mp4"
          },
          "download": "http://dl.yama.scts.tv/..."
        }
      ]
    }
  ]
}
```

## Известные подводные камни

- **Парсер «слепой».** На момент написания у нас не было ни одного реального
  JSON-ответа от `api.php`. Поля разобраны из исходников HTML/JS-шаблонов SPA
  (`TEMPLATES.FILM`). Если фактический формат отличается — поправить
  `normalizeMovie()` в `model.go`. Сырые ответы можно собрать через `--raw=raw/`
  и потом анализировать.

- **Метод по умолчанию** — `VideoCustom.getMovie`. Если в браузере DevTools
  видны другие методы (`VideoCustom.getFullMovie`, `VideoCustom.getMovieById`,
  и т.п.) — передать через `--method=`.

- **Имя ID-параметра.** Если API ждёт не `id`, а, например, `movie_id` — флаг
  `--id-param=movie_id`.

- **Содержимое cp1251 vs utf-8.** RSS/HTML-страницы у SCTS отдаются в cp1251,
  но JsHttpRequest-эндпоинт `api.php?format=ajax` должен возвращать JSON в
  utf-8 (это делает сам `json_encode` в PHP). Если парсер не разбирает ответ —
  посмотрите `raw/*.json`.

- **Бан за частые запросы.** Сервер старый (nginx 1.6.2, ~2014 год), ставьте
  `--rate-ms=300` или выше. Не загоняйте сервер своего же провайдера.
