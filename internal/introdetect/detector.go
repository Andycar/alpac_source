package introdetect

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Окна отпечатков: заставка — в первых introWindowSec, титры — в последних outroWindowSec.
// Больше окно — точнее, но каждая секунда окна это скачанный кусок файла (звук отдельно из
// контейнера не вытащить), так что 8 минут WEB-DL 1080p ≈ 300 МБ на серию.
const (
	introWindowSec = 480.0
	outroWindowSec = 300.0
	extractTimeout = 6 * time.Minute
	jobTimeout     = 25 * time.Minute
)

// Episode — одна серия для сравнения: номер и адрес файла (TorrServer/CDN), как его видит
// сервер. Others — соседние серии того же пака.
type Episode struct {
	Episode int    `json:"episode"`
	Src     string `json:"src"`
}

// Request — задание детектору.
type Request struct {
	ImdbID  string    `json:"imdb_id"`
	Season  int       `json:"season"`
	Episode int       `json:"episode"`
	Src     string    `json:"src"`
	Others  []Episode `json:"others"`
	UA      string    `json:"-"`
	Referer string    `json:"-"`
}

// Segment — результат для одной серии в её собственных секундах.
type Segment struct {
	Type       string // intro | outro
	Start, End float64
	Confidence float64
}

// SaveFunc получает найденные сегменты серии; вызывается для КАЖДОЙ серии, участвовавшей в
// сравнении (у соседей заставка тоже найдена — их метки достаются бесплатно).
type SaveFunc func(imdbID string, season, episode int, segs []Segment)

// Detector — очередь заданий с дедупликацией по сезону: одна серия сезона запускает разбор,
// остальные тем временем ждут результата, а не поднимают по ffmpeg каждая.
type Detector struct {
	ffmpeg  string
	ffprobe string
	save    SaveFunc
	mu      sync.Mutex
	running map[string]time.Time
	sem     chan struct{}
}

func New(ffmpeg, ffprobe string, save SaveFunc) *Detector {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	return &Detector{ffmpeg: ffmpeg, ffprobe: ffprobe, save: save, running: map[string]time.Time{}, sem: make(chan struct{}, 2)}
}

func seasonKey(imdb string, season int) string { return imdb + ":" + strconv.Itoa(season) }

// Enqueue ставит задание; false — такой сезон уже разбирается (или разбирался < 10 мин назад).
func (d *Detector) Enqueue(req Request) bool {
	if req.ImdbID == "" || req.Src == "" || len(req.Others) == 0 {
		return false
	}
	key := seasonKey(req.ImdbID, req.Season)
	d.mu.Lock()
	if t, ok := d.running[key]; ok && time.Since(t) < 10*time.Minute {
		d.mu.Unlock()
		return false
	}
	d.running[key] = time.Now()
	d.mu.Unlock()
	go d.run(req)
	return true
}

func (d *Detector) done(key string) {
	d.mu.Lock()
	// оставляем отметку времени — защита от повторного запуска сразу после провала
	d.running[key] = time.Now()
	d.mu.Unlock()
}

// duration — длительность файла: ffprobe читает только заголовки (MKV Info / MP4 moov).
func (d *Detector) duration(ctx context.Context, src, ua, referer string) float64 {
	args := []string{"-v", "error"}
	if strings.HasPrefix(src, "http") {
		if ua != "" {
			args = append(args, "-user_agent", ua)
		}
		if referer != "" {
			args = append(args, "-headers", "Referer: "+referer+"\r\n")
		}
	}
	args = append(args, "-show_entries", "format=duration", "-of", "csv=p=0", src)
	out, err := exec.CommandContext(ctx, d.ffprobe, args...).Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(string(out), ",", ".")), 64)
	return v
}

type epFP struct {
	ep       Episode
	dur      float64
	intro    Fingerprint
	outro    Fingerprint
	outroOff float64 // секунда начала окна титров
}

func (d *Detector) run(req Request) {
	key := seasonKey(req.ImdbID, req.Season)
	defer d.done(key)
	d.sem <- struct{}{}
	defer func() { <-d.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()
	start := time.Now()

	// Сама серия + до двух соседей: третья серия подтверждает совпадение.
	list := []Episode{{Episode: req.Episode, Src: req.Src}}
	for _, o := range req.Others {
		if o.Src == "" || o.Src == req.Src || len(list) >= 3 {
			continue
		}
		list = append(list, o)
	}
	if len(list) < 2 {
		return
	}
	fps := make([]*epFP, 0, len(list))
	for _, ep := range list {
		f, err := d.fingerprint(ctx, ep, req.UA, req.Referer)
		if err != nil {
			log.Debug().Err(err).Str("imdb", req.ImdbID).Int("s", req.Season).Int("e", ep.Episode).Msg("introdetect: fingerprint failed")
			continue
		}
		fps = append(fps, f)
	}
	if len(fps) < 2 {
		log.Info().Str("imdb", req.ImdbID).Int("s", req.Season).Dur("took", time.Since(start)).Msg("introdetect: not enough fingerprints")
		return
	}
	// Сравниваем запрошенную серию с каждым соседом; заставка = совпадение хотя бы с одним,
	// при двух соседях берём ту пару, где уверенность выше.
	a := fps[0]
	results := map[int][]Segment{}
	add := func(ep int, seg Segment) {
		for i, s := range results[ep] {
			if s.Type == seg.Type {
				if seg.Confidence > s.Confidence {
					results[ep][i] = seg
				}
				return
			}
		}
		results[ep] = append(results[ep], seg)
	}
	for _, b := range fps[1:] {
		if a.intro != nil && b.intro != nil {
			if sp, ok := Match(a.intro, b.intro, MinIntroSec, MaxIntroSec); ok && introPlausible(sp, a.dur, b.dur) {
				add(a.ep.Episode, Segment{Type: "intro", Start: sp.StartA, End: sp.EndA, Confidence: sp.Confidence})
				add(b.ep.Episode, Segment{Type: "intro", Start: sp.StartB, End: sp.EndB, Confidence: sp.Confidence})
			}
		}
		if a.outro != nil && b.outro != nil {
			if sp, ok := Match(a.outro, b.outro, MinOutroSec, MaxOutroSec); ok {
				// титры тянутся до конца файла: берём от начала совпадения до конца
				add(a.ep.Episode, Segment{Type: "outro", Start: a.outroOff + sp.StartA, End: a.dur, Confidence: sp.Confidence})
				add(b.ep.Episode, Segment{Type: "outro", Start: b.outroOff + sp.StartB, End: b.dur, Confidence: sp.Confidence})
			}
		}
	}
	n := 0
	for ep, segs := range results {
		if len(segs) == 0 {
			continue
		}
		d.save(req.ImdbID, req.Season, ep, segs)
		n += len(segs)
	}
	log.Info().Str("imdb", req.ImdbID).Int("s", req.Season).Int("episodes", len(fps)).Int("segments", n).
		Dur("took", time.Since(start)).Msg("introdetect: done")
}

// introPlausible — заставка в первой четверти или первых 10 минутах обеих серий (Intro Skipper).
func introPlausible(sp Span, durA, durB float64) bool {
	limit := func(d float64) float64 {
		if d <= 0 {
			return 600
		}
		return min(600, d*0.25)
	}
	return sp.StartA <= limit(durA) && sp.StartB <= limit(durB)
}

func (d *Detector) fingerprint(ctx context.Context, ep Episode, ua, referer string) (*epFP, error) {
	dur := d.duration(ctx, ep.Src, ua, referer)
	if dur > 0 && dur < 240 {
		return nil, errors.New("too short for an episode")
	}
	f := &epFP{ep: ep, dur: dur}
	ectx, cancel := context.WithTimeout(ctx, extractTimeout)
	defer cancel()
	intro, err := Extract(ectx, d.ffmpeg, ep.Src, ua, referer, 0, min(introWindowSec, max(dur*0.35, 120)))
	if err != nil {
		return nil, fmt.Errorf("intro window: %w", err)
	}
	f.intro = intro
	if dur > outroWindowSec+60 {
		octx, ocancel := context.WithTimeout(ctx, extractTimeout)
		defer ocancel()
		f.outroOff = dur - outroWindowSec
		if outro, err := Extract(octx, d.ffmpeg, ep.Src, ua, referer, f.outroOff, outroWindowSec); err == nil {
			f.outro = outro
		} else {
			log.Debug().Err(err).Int("e", ep.Episode).Msg("introdetect: outro window failed")
		}
	}
	return f, nil
}
