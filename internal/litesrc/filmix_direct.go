package litesrc

// Filmix «прямой CDN» (архитектура B, v1).
//
// Проблема: сегменты Filmix выше 480p CDN привязывает к IP, ПОЛУЧИВШЕМУ hash, а api.filmix.tv
// гео-блокирует датацентровые IP. Значит сервер физически не может отдать клиенту прямые ссылки,
// работающие с адреса клиента, — и проксирование через один эксит упирается в его полосу
// (буферизация). Решение: пусть КЛИЕНТ сам сминтит hash со своего адреса и качает CDN напрямую —
// тогда IP совпадают и медиатрафик минует сервер вовсе. Работает там, где api-fx не блокирует IP
// клиента (RU-домашние); иначе клиент откатывается на серверный проксирующий путь (`fallback`).
//
// Сервер здесь делает ровно две вещи: сопоставляет тайтл с postid (это остаётся у нас — матчинг
// нетривиален) и отдаёт клиенту РЕЦЕПТ прямого доступа. Всё остальное — на клиенте.
//
// Дескриптор запрашивается флагом `fxdirect=1`; без него поведение не меняется (существующие
// клиенты продолжают получать проксированные потоки), поэтому регрессий у web/android/tizen/webos нет.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// filmixDirectDescriptor — рецепт для клиента: как сминтить hash и собрать прямые CDN-ссылки.
//
// Клиент обязан повторить нашу серверную логику один-в-один (см. filmix_fx.go):
//  1. GET {api_host}/api-fx/request-token  → { "token": <hash> }  (заголовок User-Agent = browser)
//  2. GET {api_host}/api-fx/post/{postid}/video-links  с заголовком `hash: <hash>`
//     → массив озвучек (фильм) или объект голос→сезон→серии (сериал); каждый файл: {url, quality}.
//     Ответ {"message": …} = гео-блок/РКН → уйти на fallback.
//  3. Внутри плейлиста CDN кладёт ?hash= на КАЖДУЮ сегмент-строку, но НЕ на EXT-X-MAP (init fMP4/HDR).
//     Клиент ОБЯЗАН дописать ?hash из URL плейлиста в init-URI, иначе HDR 403 (наш серверный фикс
//     переезжает на клиента, раз манифест теперь тянет он).
type filmixDirectDescriptor struct {
	Method       string `json:"method"`        // "filmix-direct" — маркер для клиента
	APIHost      string `json:"api_host"`      // https://api.filmix.tv
	PostID       int    `json:"postid"`        // id тайтла на Filmix
	RequestToken string `json:"request_token"` // путь минта hash
	VideoLinks   string `json:"video_links"`   // путь получения ссылок для этого postid
	HashHeader   string `json:"hash_header"`   // имя заголовка с hash ("hash")
	UserAgent    string `json:"user_agent"`    // UA, который ждёт api-fx
	InheritHash  bool   `json:"inherit_hash"`  // дописывать ?hash в EXT-X-MAP init
	Fallback     string `json:"fallback"`      // серверный проксирующий путь (тот же /lite/filmix без fxdirect)
	Note         string `json:"note"`
}

// buildFilmixDirect собирает дескриптор для клиента.
func (f *filmixChecker) buildFilmixDirect(req *http.Request, postID int) filmixDirectDescriptor {
	return filmixDirectDescriptor{
		Method:       "filmix-direct",
		APIHost:      f.fxHost,
		PostID:       postID,
		RequestToken: "/api-fx/request-token",
		VideoLinks:   fmt.Sprintf("/api-fx/post/%d/video-links", postID),
		HashHeader:   "hash",
		UserAgent:    fxBrowserUserAgent,
		InheritHash:  true,
		Fallback:     filmixFallbackURL(req, postID),
		Note:         "Если api-fx недоступен/пусто/{message} — играть через fallback (серверный прокси).",
	}
}

// filmixFallbackURL — тот же /lite/filmix для этого postid, но БЕЗ fxdirect: сервер отдаст
// проксированные потоки (наш рабочий путь с кэшем, api-fx через эксит и легаси-фолбэком).
func filmixFallbackURL(req *http.Request, postID int) string {
	host := hostFromRequest(req)
	q := req.URL.Query()
	out := url.Values{}
	out.Set("rjson", getsTVBool(parseBoolParam(q.Get("rjson"))))
	out.Set("postid", fmt.Sprintf("%d", postID))
	if v := strings.TrimSpace(q.Get("title")); v != "" {
		out.Set("title", v)
	}
	if v := strings.TrimSpace(q.Get("original_title")); v != "" {
		out.Set("original_title", v)
	}
	if v := strings.TrimSpace(q.Get("s")); v != "" {
		out.Set("s", v)
	}
	if v := strings.TrimSpace(q.Get("t")); v != "" {
		out.Set("t", v)
	}
	return host + "/lite/filmix?" + out.Encode()
}

// filmixRowFxDirect — per-row рецепт для Lampa (архитектура B): к КАЖДОЙ строке фильма/серии
// прикладывается postid + пути api-fx + имя озвучки (и сезон/серия для сериала), чтобы браузер
// Lampa сам сминтил hash и подменил url прямой CDN-ссылкой (см. player-inner filmix-direct snippet).
// Прикладывается только при DirectLampa; иначе строки без fxdirect и snippet инертен.
func (f *filmixChecker) filmixRowFxDirect(postID int, voice string, season, episode int) map[string]any {
	m := map[string]any{
		"api_host":      f.fxHost,
		"postid":        postID,
		"request_token": "/api-fx/request-token",
		"video_links":   fmt.Sprintf("/api-fx/post/%d/video-links", postID),
		"hash_header":   "hash",
		"user_agent":    fxBrowserUserAgent,
		"voice":         voice,
	}
	if season > 0 {
		m["season"] = season
	}
	if episode > 0 {
		m["episode"] = episode
	}
	return m
}

// writeFilmixDirect отдаёт дескриптор. Для сериалов postid достаточно — сезон/серию клиент выберет
// из video-links сам (структура голос→сезон→серии там уже есть).
func (f *filmixChecker) writeFilmixDirect(w http.ResponseWriter, req *http.Request, postID int) {
	writeJSON(w, http.StatusOK, map[string]any{
		"type": "filmix-direct",
		"data": f.buildFilmixDirect(req, postID),
	})
}
