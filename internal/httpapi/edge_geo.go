package httpapi

import (
	"strings"
	"sync"
	"time"

	"lampac-go/internal/geoip"
)

// Страна зрителя для гео-ограничений нод (stream_edge_geo_deny в proxyapi).
//
// Решение принимается на КАЖДЫЙ запрос манифеста, поэтому определение обязано быть
// мгновенным и никогда не задерживать поток. Отсюда два источника по порядку:
//
//  1. GeoLite2 (database/GeoLite2-Country.mmdb) — локально и без сети. На проде базы
//     сейчас нет, поэтому в одиночку она не годится;
//  2. кэш ip-api по /24, который уже наполняет netdiag (lookupASN). Читаем ТОЛЬКО
//     готовое значение; промах не блокирует запрос, а ставит фоновое дозаполнение,
//     и следующий зритель из той же подсети решается уже правильно.
//
// Неизвестная страна = ограничений нет: молчаливо запретить ноду всем — хуже, чем не
// запретить никому.
const edgeGeoFillMax = 8 // одновременных фоновых уточнений (ip-api: 45 запросов/мин)

var (
	edgeGeoFill    = make(chan struct{}, edgeGeoFillMax)
	edgeGeoPendMu  sync.Mutex
	edgeGeoPending = map[string]time.Time{}
	edgeGeoPendTTL = 10 * time.Minute
	edgeGeoDBRef   *geoip.DB
	edgeGeoDBRefMu sync.RWMutex
)

// setEdgeGeoDB запоминает базу GeoLite2 (может быть пустой — тогда работает только кэш).
func setEdgeGeoDB(db *geoip.DB) {
	edgeGeoDBRefMu.Lock()
	edgeGeoDBRef = db
	edgeGeoDBRefMu.Unlock()
}

// countryForEdge — код страны зрителя или "" (не знаем / не успели узнать).
func countryForEdge(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" || !isPublicIP(ip) {
		return ""
	}
	edgeGeoDBRefMu.RLock()
	db := edgeGeoDBRef
	edgeGeoDBRefMu.RUnlock()
	if db != nil && db.Available() {
		if c := strings.ToUpper(strings.TrimSpace(db.Country(ip))); c != "" {
			return c
		}
	}
	key := asnKey(ip)
	if key == "" {
		return ""
	}
	asnMu.Lock()
	v, ok := asnCache[key]
	fresh := ok && time.Since(v.at) < asnTTL
	asnMu.Unlock()
	if fresh && v.Country != "" {
		return strings.ToUpper(v.Country)
	}
	scheduleEdgeGeoFill(key, ip)
	return ""
}

// scheduleEdgeGeoFill уточняет подсеть в фоне, не задерживая текущий запрос.
func scheduleEdgeGeoFill(key, ip string) {
	now := time.Now()
	edgeGeoPendMu.Lock()
	if t, ok := edgeGeoPending[key]; ok && now.Sub(t) < edgeGeoPendTTL {
		edgeGeoPendMu.Unlock()
		return
	}
	edgeGeoPending[key] = now
	if len(edgeGeoPending) > 20_000 {
		for k, t := range edgeGeoPending {
			if now.Sub(t) > edgeGeoPendTTL {
				delete(edgeGeoPending, k)
			}
		}
	}
	edgeGeoPendMu.Unlock()

	select {
	case edgeGeoFill <- struct{}{}:
	default:
		return // очередь занята — уточним при следующем запросе из этой подсети
	}
	go func() {
		defer func() { <-edgeGeoFill }()
		_ = lookupASN(ip) // сам кладёт результат в asnCache по ключу /24
	}()
}
