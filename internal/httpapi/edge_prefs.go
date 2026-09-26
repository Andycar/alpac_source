package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"lampac-go/internal/cluster"
	"lampac-go/internal/edgeprefs"
	"lampac-go/internal/proxyapi"
)

// Отключённые зрителем ноды. Источник истины один — на аккаунт, потому что правят его из двух
// мест: пикер в боте и экран «Проверка связи» на телевизоре. Параметр `edge_skip=` в ссылке
// остаётся транспортом: список подмешивается в ссылку в момент сборки и дальше едет сам.
var edgePrefsRef *edgeprefs.Store

// edgePrefsTGID — чей это запрос. Те же способы, что и везде: токен из cookie/заголовка/query,
// плюс UID устройства (приложение на телевизоре ходит именно так).
func edgePrefsTGID(r *http.Request) int64 {
	if r == nil || tgTokenStoreRef == nil {
		return 0
	}
	token := resolveOurToken(r)
	if token == "" {
		if uid := strings.TrimSpace(r.URL.Query().Get("uid")); uid != "" {
			token = tgTokenStoreRef.FindTokenByDeviceUID(uid)
		}
	}
	if token == "" {
		return 0
	}
	at, ok := tgTokenStoreRef.Lookup(token)
	if !ok || at == nil {
		return 0
	}
	return at.TelegramID
}

// edgePrefsTGIDFn — шов для тестов: подменяется, чтобы проверять дописывание запрета,
// не поднимая ради этого хранилище токенов с живым аккаунтом.
var edgePrefsTGIDFn = edgePrefsTGID

// initEdgePrefs поднимает хранилище и вешает его на proxyapi. Зовётся один раз при старте.
func initEdgePrefs(repoRoot string) {
	edgePrefsRef = edgeprefs.New(repoRoot)
	proxyapi.SetEdgeSkipResolver(func(r *http.Request) string {
		if edgePrefsRef == nil {
			return ""
		}
		id := edgePrefsTGID(r)
		if id == 0 {
			return ""
		}
		return edgePrefsRef.Join(id)
	})
}

// edgeSkippedFor — сохранённый список для выдачи в /api/edges.
//
// Разница между null и [] здесь смысловая, и она важна: null = «зрителя не опознали», пустой
// массив = «опознали, ничего не отключено». Клиент, получивший null, обязан оставить свой
// локальный выбор; получи он на неопознанном запросе пустой массив — стёр бы настройку зрителя
// при первом же походе без токена.
func edgeSkippedFor(r *http.Request) []string {
	if edgePrefsRef == nil {
		return nil
	}
	id := edgePrefsTGID(r)
	if id == 0 {
		return nil
	}
	list := edgePrefsRef.Get(id)
	if list == nil {
		return []string{}
	}
	return list
}

// edgePinnedJSON — закрепление для выдачи в /api/edges. Разница как у skipped: null = зрителя не
// опознали (клиент оставляет своё локальное), пустая строка = опознали, закрепления нет (клиент
// обязан снять своё — значит, его сняли с другого устройства).
func edgePinnedJSON(r *http.Request) any {
	if edgePrefsRef == nil || edgePrefsTGIDFn(r) == 0 {
		return nil
	}
	return edgePrefsRef.Pin(edgePrefsTGIDFn(r))
}

// edgePinnedFor — то же, но строкой: для проставления в запрос, где разница не нужна.
func edgePinnedFor(r *http.Request) string {
	if edgePrefsRef == nil {
		return ""
	}
	id := edgePrefsTGIDFn(r)
	if id == 0 {
		return ""
	}
	return edgePrefsRef.Pin(id)
}

// stampEdgePin вписывает закреплённую ноду в САМ запрос — как и запрет, до развилки «локально
// или на ноду». Подсказку `edge=` уважают оба конца: и выбор добытчика, и выдача сегментов.
//
// Клиентское значение сильнее: если приложение уже прислало `edge=`, человек только что выбрал
// ноду руками на этом устройстве, и перебивать его сохранённым было бы грубо.
func stampEdgePin(r *http.Request) {
	if edgePrefsRef == nil || r == nil {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("edge")) != "" {
		return
	}
	pin := edgePinnedFor(r)
	if pin == "" {
		return
	}
	sep := "&"
	if r.URL.RawQuery == "" {
		sep = ""
	}
	r.URL.RawQuery += sep + "edge=" + url.QueryEscape(pin)
}

// edgeSkipSaveHandler — POST /api/edges/skip, тело {"skip":["https://a", …]}.
//
// Сохраняем ТОЛЬКО адреса из своего же списка нод: иначе настройкой можно было бы засорить файл
// чужими хостами, а в ссылку уехал бы мусор. Неизвестные адреса молча отбрасываются — ответ
// показывает, что реально записано.
func edgeSkipSaveHandler(w http.ResponseWriter, r *http.Request) {
	if edgePrefsRef == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "edge prefs disabled"})
		return
	}
	id := edgePrefsTGID(r)
	if id == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not authorized"})
		return
	}
	var body struct {
		Skip []string `json:"skip"`
		// Pin приходит указателем, чтобы отличить «не трогай» (поля нет) от «сними» (пустая
		// строка): без этого любое сохранение списка отключённых сбрасывало бы закрепление.
		Pin *string `json:"pin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
		return
	}

	known := map[string]string{}
	for _, h := range proxyapi.EdgeHostsFor(clientIP(r)) {
		known[edgeprefs.Normalize(h)] = h
	}
	clean := make([]string, 0, len(body.Skip))
	for _, raw := range body.Skip {
		if h, ok := known[edgeprefs.Normalize(raw)]; ok {
			clean = append(clean, h)
		}
	}
	// Отключить ВСЁ разрешаем: это не поломка, а «отдавай сам» — main справится, просто дальше.
	edgePrefsRef.Set(id, clean)
	if body.Pin != nil {
		// Закрепить можно только свою же ноду; мусор молча отбрасывается, как и в списке.
		if pin := strings.TrimSpace(*body.Pin); pin == "" {
			edgePrefsRef.SetPin(id, "")
		} else if h, ok := known[edgeprefs.Normalize(pin)]; ok {
			edgePrefsRef.SetPin(id, h)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "skipped": edgePrefsRef.Get(id), "pinned": edgePrefsRef.Pin(id),
	})
}

// edgeOption — нода, как её видит зритель. Одна функция на всех потребителей: список в
// /api/edges, пикер в боте и проверка при сохранении обязаны совпадать, иначе в боте можно
// отключить то, чего приложение не показывает.
type edgeOption struct {
	URL   string
	Label string
}

// edgeOptions — ноды, доступные этому адресу, с городскими подписями из nodes.json.
// Пустой ip = без гео-отсева: так список запрашивает бот, у которого адреса зрителя нет.
func edgeOptions(pool *cluster.Pool, ip string) []edgeOption {
	labels := map[string]string{}
	if pool != nil {
		for _, n := range pool.Nodes() {
			if n.EdgeURL != "" && n.EdgeLabel != "" {
				labels[strings.ToLower(n.EdgeURL)] = n.EdgeLabel
			}
		}
	}
	out := []edgeOption{}
	for _, h := range proxyapi.EdgeHostsFor(ip) {
		label := labels[strings.ToLower(strings.TrimRight(h, "/"))]
		if label == "" {
			// Домен на экране телевизора человеку ничего не говорит, но пустота — хуже.
			if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
				label = u.Hostname()
			} else {
				label = h
			}
		}
		out = append(out, edgeOption{URL: h, Label: label})
	}
	return out
}

// stampEdgeSkip вписывает сохранённый запрет в САМ запрос, а не только в собираемую ссылку.
//
// Зачем: /lite main нередко форвардит на ноду, и ссылку собирает уже она — со своим пустым
// хранилищем. Настройка, сделанная в боте, до неё бы не доехала, и зритель снова попал бы на
// ноду, которую отключил. Правим запрос до развилки «локально или на ноду» — дальше он едет
// как есть, и оба пути видят одно.
//
// Дописываем в хвост RawQuery, а не пересобираем его: остальные параметры уже закодированы, и
// повторное кодирование меняло бы байты там, где их никто не просил трогать.
func stampEdgeSkip(r *http.Request) {
	if edgePrefsRef == nil || r == nil {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("edge_skip")) != "" {
		return // клиент прислал свой список — он главнее, дублировать незачем
	}
	id := edgePrefsTGIDFn(r)
	if id == 0 {
		return
	}
	list := edgePrefsRef.Join(id)
	if list == "" {
		return
	}
	sep := "&"
	if r.URL.RawQuery == "" {
		sep = ""
	}
	r.URL.RawQuery += sep + "edge_skip=" + url.QueryEscape(list)
}
