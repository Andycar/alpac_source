package litesrc

// SponsorBlock — пропуск спонсорских вставок в роликах YouTube.
//
// Данные берутся у публичного API sponsor.ajay.app (проект github.com/ajayyy/
// SponsorBlock). Сегменты размечает сообщество, поэтому они есть далеко не у всех
// роликов — отсутствие разметки это норма, а не сбой.
//
// Запрос идёт по ПРЕФИКСУ ХЕША, а не по videoID: сервер отдаёт разметку сразу для
// всех роликов, чей sha256 начинается с тех же четырёх шестнадцатеричных цифр
// (~60 роликов, 20 КБ), и нужный выбирается уже у нас. Так владелец API не узнаёт,
// что именно смотрит наш зритель. Стоит это лишних 20 КБ на запрос — при кэше
// незаметно.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// sponsorAPIBase — переменная, а не константа, чтобы тесты могли подставить
// собственный сервер вместо публичного API.
var sponsorAPIBase = "https://sponsor.ajay.app/api/skipSegments"

const (
	// Таймаут маленький: разметка — приятное дополнение, а не условие
	// воспроизведения. Лучше отдать ролик без неё, чем задержать ответ.
	sponsorTimeout  = 4 * time.Second
	sponsorCacheTTL = 6 * time.Hour
	sponsorCacheMax = 2000
	// Расхождение длительности, после которого разметка считается чужой. Сегменты
	// привязаны к конкретной версии ролика: автор мог перезалить его другой длины,
	// и тогда пропуски придутся на середину нормальной сцены. Две секунды — запас
	// на разное округление у клиентов, приславших разметку.
	sponsorDurationSlack = 2.0
)

// sponsorDefaultCategories — что режем по умолчанию. Осознанно НЕ включены
// «intro»/«outro»/«preview»/«filler»: это части самого ролика, и их вырезание —
// вкусовщина, тогда как спонсорская вставка и самореклама вставки посторонние.
var sponsorDefaultCategories = []string{"sponsor", "selfpromo", "interaction", "music_offtopic"}

// SponsorSegment — отрезок, который клиент должен перемотать.
type SponsorSegment struct {
	Category string  `json:"category"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
}

type sponsorAPIEntry struct {
	VideoID  string `json:"videoID"`
	Segments []struct {
		Category      string    `json:"category"`
		ActionType    string    `json:"actionType"`
		Segment       []float64 `json:"segment"`
		Votes         int       `json:"votes"`
		VideoDuration float64   `json:"videoDuration"`
	} `json:"segments"`
}

type sponsorCacheEntry struct {
	segments []SponsorSegment
	at       time.Time
}

var (
	sponsorMu    sync.Mutex
	sponsorCache = map[string]sponsorCacheEntry{}
)

// sponsorCategories returns the configured category list, or the default one.
func (y *YoutubeChecker) sponsorCategories() []string {
	if len(y.sbCategories) > 0 {
		return y.sbCategories
	}
	return sponsorDefaultCategories
}

// SponsorBlockFor returns the segments to skip for a video. Пустой срез — это
// нормальный ответ: разметки может просто не быть.
//
// duration — длительность ролика по данным yt-dlp; 0 = проверку версии пропустить.
func (y *YoutubeChecker) SponsorBlockFor(videoID string, duration float64) []SponsorSegment {
	if !y.sbEnable || strings.TrimSpace(videoID) == "" {
		return nil
	}

	sponsorMu.Lock()
	if e, ok := sponsorCache[videoID]; ok && time.Since(e.at) < sponsorCacheTTL {
		sponsorMu.Unlock()
		return e.segments
	}
	sponsorMu.Unlock()

	segments, err := y.fetchSponsorSegments(videoID, duration)
	if err != nil {
		// Не кэшируем ошибку: следующий зритель того же ролика попробует снова.
		log.Debug().Err(err).Str("videoID", videoID).Msg("sponsorblock: разметку получить не удалось")
		return nil
	}

	sponsorMu.Lock()
	if len(sponsorCache) >= sponsorCacheMax {
		// Кэш растёт по одному ролику на просмотр; вычищаем протухшее, а если
		// протухшего нет — сбрасываем целиком, это дешевле учёта возраста.
		for k, e := range sponsorCache {
			if time.Since(e.at) >= sponsorCacheTTL {
				delete(sponsorCache, k)
			}
		}
		if len(sponsorCache) >= sponsorCacheMax {
			sponsorCache = map[string]sponsorCacheEntry{}
		}
	}
	sponsorCache[videoID] = sponsorCacheEntry{segments: segments, at: time.Now()}
	sponsorMu.Unlock()

	if len(segments) > 0 {
		log.Debug().Str("videoID", videoID).Int("сегментов", len(segments)).Msg("sponsorblock: разметка получена")
	}
	return segments
}

func (y *YoutubeChecker) fetchSponsorSegments(videoID string, duration float64) ([]SponsorSegment, error) {
	sum := sha256.Sum256([]byte(videoID))
	prefix := hex.EncodeToString(sum[:])[:4]

	cats, _ := stdjson.Marshal(y.sponsorCategories())
	// actionTypes=skip: «mute» приглушает звук, «poi» — метка момента, «full» —
	// признак ролика целиком. Всё это требует другого поведения плеера, поэтому
	// просим у API только то, что действительно умеем — перемотку.
	q := url.Values{}
	q.Set("categories", string(cats))
	q.Set("actionTypes", `["skip"]`)
	reqURL := fmt.Sprintf("%s/%s?%s", sponsorAPIBase, prefix, q.Encode())

	ctx, cancel := context.WithTimeout(context.Background(), sponsorTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // на этот префикс разметки нет
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sponsorblock: статус %d", resp.StatusCode)
	}

	var entries []sponsorAPIEntry
	if err := stdjson.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}

	var out []SponsorSegment
	for _, e := range entries {
		if e.VideoID != videoID {
			continue // ответ по префиксу содержит и чужие ролики
		}
		for _, s := range e.Segments {
			if s.Votes < 0 || len(s.Segment) != 2 {
				continue // заминусованную разметку сообщество считает неверной
			}
			start, end := s.Segment[0], s.Segment[1]
			if end <= start {
				continue
			}
			// Разметка привязана к КОНКРЕТНОЙ версии ролика. Если автор перезалил
			// его другой длины, отрезки съедут и перемотка придётся на нормальную
			// сцену — такую разметку лучше не применять вовсе.
			if duration > 0 && s.VideoDuration > 0 &&
				math.Abs(s.VideoDuration-duration) > sponsorDurationSlack {
				continue
			}
			out = append(out, SponsorSegment{Category: s.Category, Start: start, End: end})
		}
	}
	return mergeSponsorSegments(out), nil
}

// mergeSponsorSegments сортирует отрезки и склеивает пересекающиеся. Разметку
// присылают разные люди, поэтому один и тот же кусок нередко приходит дважды с
// чуть разными границами: без склейки плеер перемотал бы дважды подряд и зритель
// увидел бы дёрганье.
func mergeSponsorSegments(in []SponsorSegment) []SponsorSegment {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Start < in[j].Start })
	out := []SponsorSegment{in[0]}
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			if s.End > last.End {
				last.End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}
