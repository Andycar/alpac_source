package iptv

import (
	"sort"
	"strings"
)

// Примерка донорского плейлиста к реестру — без единой записи на диск.
//
// Реестр каналов наш, доноры сменные: канал жив, пока хоть один донор даёт на
// него ссылку. Отсюда вопрос, ради которого написан файл: если донор уходит
// (кончилась подписка, умер хост), кто из кандидатов закрывает освободившиеся
// каналы и чего это стоит. Сопоставление здесь повторяет Ingest — tvg-id,
// затем нормализованное имя, — иначе цифры примерки разошлись бы с тем, что
// произойдёт при настоящем импорте.

// DonorRescue — канал реестра, которому кандидат даёт источник.
type DonorRescue struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Quality string `json:"quality,omitempty"`
	// Plays — сколько раз канал включали за период, если счётчик передан
	// снаружи. Без него все каналы весят одинаково, а это неправда: половина
	// реестра не включалась ни разу.
	Plays int `json:"plays,omitempty"`
}

// DonorFit — итог примерки одного плейлиста.
type DonorFit struct {
	Parsed   int `json:"parsed"`   // записей в плейлисте
	Matched  int `json:"matched"`  // записей, легших источником в известные каналы
	Channels int `json:"channels"` // различных каналов реестра, которых кандидат коснулся
	New      int `json:"new"`      // различных имён, которых в реестре нет вовсе
	// Rescued — каналы из переданного списка осиротевших, которые кандидат
	// закрывает. Ради этого поля всё и затевалось.
	Rescued  []DonorRescue `json:"rescued,omitempty"`
	NewNames []string      `json:"new_names,omitempty"`
}

// OrphansIfDropped returns enabled channels that lose their last source once
// the named donors go away. Выключенные каналы не считаем: их никто не видит,
// и они раздули бы любую оценку в несколько раз.
func (r *Registry) OrphansIfDropped(drop map[string]bool) []RegChannel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []RegChannel
	for _, c := range r.channels {
		if c.Disabled {
			continue
		}
		var left int
		var had bool
		for _, s := range c.Sources() {
			had = true
			if !drop[s.From] {
				left++
			}
		}
		if had && left == 0 {
			out = append(out, *c)
		}
	}
	return out
}

// FitDonor matches donor records against the registry the way Ingest does and
// reports what the candidate would bring. Ничего не меняет.
func (r *Registry) FitDonor(donors []Channel, orphans []RegChannel) DonorFit {
	need := make(map[string]string, len(orphans))
	for _, c := range orphans {
		need[c.ID] = c.Name
	}
	fit := DonorFit{Parsed: len(donors)}
	touched := make(map[string]bool)
	rescued := make(map[string]bool)
	newSeen := make(map[string]bool)

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, dc := range donors {
		if strings.TrimSpace(dc.URL) == "" {
			continue
		}
		var c *RegChannel
		if t := strings.TrimSpace(dc.TvgID); t != "" {
			c = r.byTvg[t]
		}
		key := normChannelName(dc.Name)
		if c == nil && key != "" {
			c = r.byKey[key]
		}
		if c == nil {
			// Считаем именно РАЗЛИЧНЫЕ имена: панель, отдающая один канал
			// пятью потоками, иначе выглядела бы впятеро щедрее.
			if key == "" || newSeen[key] {
				continue
			}
			newSeen[key] = true
			fit.New++
			name := cleanChannelName(dc.Name)
			if name == "" {
				name = dc.Name
			}
			fit.NewNames = append(fit.NewNames, name)
			continue
		}
		fit.Matched++
		touched[c.ID] = true
		if name, ok := need[c.ID]; ok && !rescued[c.ID] {
			rescued[c.ID] = true
			fit.Rescued = append(fit.Rescued, DonorRescue{
				ID: c.ID, Name: name, URL: dc.URL, Quality: dc.Quality,
			})
		}
	}
	fit.Channels = len(touched)
	sort.Strings(fit.NewNames)
	sort.Slice(fit.Rescued, func(i, j int) bool { return fit.Rescued[i].Name < fit.Rescued[j].Name })
	return fit
}
