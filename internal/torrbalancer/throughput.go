package torrbalancer

import (
	"math"
	"time"
)

// Measured streaming throughput per backend.
//
// Selection used to look only at active connections (PickPrimary) and weighted
// rendezvous hashing (PickForHash). Neither can see how fast a backend actually
// delivers, and the /echo health probe answers in milliseconds whatever the
// pipe behind it is. Production 2026-09-01: one backend returned ~170 KB/s on
// every torrent tried — a throttled ~1.4 Mbit link, unusable for video — while
// answering /echo in 258 ms and taking 495 requests, and another that streamed
// at 13 MB/s received two.
//
// So throughput is measured where the bytes actually move (the stream proxy's
// io.Copy) and folded into the weight the picker already understands.

const (
	// thrEWMAAlpha weights the newest sample. Kept low: throughput swings with
	// the swarm behind each torrent, and the goal is the backend's sustained
	// capability, not this minute's seeders.
	thrEWMAAlpha = 0.2

	// thrMinBytes rejects samples too small to mean anything: a 200-byte error
	// body served in 3 ms is not "66 KB/s". The byte count is what makes a
	// sample meaningful, NOT how long it took — a first cut of this code also
	// required 300 ms and thereby threw away every sample from the fastest
	// backends, which deliver 2 MB in ~200 ms. That biased the measurement
	// towards exactly the slow backends it was meant to demote.
	thrMinBytes = 256 << 10 // 256 KB
	// thrMinDuration only guards the division; anything above a few
	// milliseconds carries a usable rate.
	thrMinDuration = 20 * time.Millisecond
)

// speedTier maps measured bytes/sec onto a coarse multiplier for the routing
// weight. Coarse on purpose: PickForHash pins a torrent to a backend through
// rendezvous hashing, and every weight change reshuffles some hashes onto a
// different backend, throwing away the cache warmed there. Four buckets change
// rarely, and the hysteresis below keeps a backend hovering on a boundary from
// flapping between them.
//
// The spread (1→8) is deliberate: a backend delivering 10 MB/s can carry
// roughly eight times what a 170 KB/s one can, so proportional routing is what
// "fair" actually means here.
func speedTier(bytesPerSec float64) int {
	switch {
	case bytesPerSec <= 0:
		return 2 // нулевая скорость измерена (не «нет данных» — для того см. unmeasuredTier)
	case bytesPerSec < 500<<10: // < 500 KB/s — cannot carry video
		return 1
	case bytesPerSec < 2<<20: // < 2 MB/s
		return 2
	case bytesPerSec < 8<<20: // < 8 MB/s
		return 4
	default:
		return 8
	}
}

// tierHysteresis is the fraction a backend must move PAST a boundary before its
// tier changes, so a backend sitting exactly on 2 MB/s does not oscillate.
const tierHysteresis = 0.15

// RecordThroughput folds one streaming sample into the backend's rate.
// Samples too small to be meaningful are ignored.
func (p *Pool) RecordThroughput(b *Backend, bytes int64, dur time.Duration) {
	if b == nil || bytes < thrMinBytes || dur < thrMinDuration {
		return
	}
	rate := float64(bytes) / dur.Seconds()
	if rate <= 0 {
		return
	}
	b.observeRate(rate)
}

func (b *Backend) observeRate(rate float64) {
	prev := b.rateBits.Load()
	var next float64
	if b.rateSamples.Add(1) == 1 || prev == 0 {
		next = rate
	} else {
		next = (1-thrEWMAAlpha)*float64FromBits(prev) + thrEWMAAlpha*rate
	}
	b.rateBits.Store(bitsFromFloat64(next))
	b.retier(next)
}

// retier updates the cached tier, applying hysteresis so a rate drifting around
// a bucket boundary does not reshuffle torrents on every sample.
func (b *Backend) retier(rate float64) {
	cur := int(b.tier.Load())
	want := speedTier(rate)
	if cur == 0 {
		b.tier.Store(int64(want))
		return
	}
	if want == cur {
		return
	}
	// Moving up needs the rate to clear the boundary by the margin; moving down
	// needs it to fall below by the same margin.
	adjusted := rate * (1 - tierHysteresis)
	if want < cur {
		adjusted = rate * (1 + tierHysteresis)
	}
	if speedTier(adjusted) == want {
		b.tier.Store(int64(want))
	}
}

// RateBytesPerSec reports the smoothed streaming rate, 0 when never measured.
func (b *Backend) RateBytesPerSec() float64 {
	return float64FromBits(b.rateBits.Load())
}

// unmeasuredTier — ступень сервера, который ещё НИ РАЗУ не отдавал поток.
//
// Раньше здесь была нейтральная двойка, и это создавало замкнутый круг: обстрелянный сервер
// поднимался до восьмёрки, неизмеренный оставался на двойке, получал вчетверо меньше раздач и
// потому никогда не набирал замеров, чтобы себя показать. Замер на проде 2026-09-09: два сервера
// (138.16.226.171 с кэшем 512 МБ и 82.192.72.85 с 768 МБ) стояли с нулём и двумя раздачами
// в базе, тогда как две трёхгигабайтные коробки держали 98 раздач из 133 — при том, что каналы
// у обделённых оказались нормальными, просто их некому было измерить.
//
// Четвёрка — это разведочная доля, а не аванс: сервер получает заметный, но не подавляющий
// кусок (против восьмёрки у измеренных быстрых), первый же реальный поток даёт замер, и дальше
// решает измеренная скорость. Ставить сразу восемь нельзя — тогда неизвестный сервер обгонял бы
// проверенных, а половина смысла ступеней в том, чтобы этого не случалось.
const unmeasuredTier = 4

// SpeedTier is the routing multiplier derived from the measured rate.
func (b *Backend) SpeedTier() int {
	if t := int(b.tier.Load()); t > 0 {
		return t
	}
	return unmeasuredTier
}

// effectiveWeight is the operator's weight scaled by measured capability. This
// is what the pickers use, so a throttled backend keeps its configured weight
// on paper while receiving traffic in proportion to what it can actually serve.
func (b *Backend) effectiveWeight() int {
	w := b.Weight
	if w < 1 {
		w = 1
	}
	return w * b.SpeedTier()
}

// float64 <-> uint64 bit helpers, so a rate can live in an atomic.Uint64.
func bitsFromFloat64(f float64) uint64 { return math.Float64bits(f) }
func float64FromBits(u uint64) float64 { return math.Float64frombits(u) }
