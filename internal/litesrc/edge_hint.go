package litesrc

import (
	"net/url"
	"strings"

	"lampac-go/internal/config"
)

// liteEdgeHint — «этот запрос обслужи на мне»: подсказка edge=<адрес этой ноды>
// для ссылок /lite, которые источник кладёт в СВОЙ ответ и которые плеер
// дёрнет позже отдельным запросом.
//
// Зачем. У obrut (zetflix/zetflixdb/videodb) ссылка cdn-*.obrut.show привязана
// к адресу, получившему embed. Индекс добыла нода А, а плеер зовёт
// /lite/<bal>/manifest.mp4?link=… — и правило «edge» отправляло его на ЛЮБУЮ
// edge-ноду: там резолв получал 404, код подставлял сырую ссылку и минтил токен
// с edge_url чужой ноды — у зрителя 404. Разбор 22.09.2026: 79 % всех ошибок
// /proxy за сутки, 437 из 438 мёртвых токенов zetflixdb — ровно это; ссылка
// «воскресала» при повторе, когда запрос случайно попадал на нужную ноду.
// Подсказку edge= главный сервер уважает раньше правил (findByEdge в
// PickForSkip), так что манифест придёт на добывшую ноду. Пусто, когда у сервера
// нет edge_url (main или одиночная установка) — тогда всё как прежде.
func liteEdgeHint() string {
	u := strings.TrimSpace(liveConfig(config.Config{}).Cluster.EdgeURL)
	if u == "" {
		return ""
	}
	return "&edge=" + url.QueryEscape(u)
}
