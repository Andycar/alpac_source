//go:build torrs

package torrs

import (
	"context"
	"io"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  RAM-only piece storage (ram_cache = true)
// ---------------------------------------------------------------------------
//
// A storage.ClientImpl that keeps torrent pieces in memory and never touches
// the disk — the MatriX «RAM cache» behaviour. Pieces are evicted LRU once the
// budget (cache_size_mb) is exceeded, which is what makes streaming a 40 GB
// remux through 2 GB of RAM possible: only the window around the reader is
// resident, everything behind it is dropped.
//
// Three details make eviction safe rather than a source of corrupt reads:
//
//   - Only COMPLETE pieces are evictable. A piece still receiving chunks is
//     pinned: dropping it would lose the chunks anacrolix already counts as
//     delivered, and it would never re-request them.
//   - An evicted piece reports Completion{Complete:false} and its ReadAt
//     returns io.EOF. storage.Piece.ReadAt turns that EOF into MarkNotComplete,
//     and torrent.reader.readAt re-syncs completion and retries — but only for
//     storage that declares a capacity, which is why TorrentImpl.Capacity is
//     set (torrent.hasStorageCap: «whether we can expect data to vanish while
//     trying to read»). Without it the same read would surface as an I/O error
//     to the player.
//   - Capacity is also what keeps anacrolix from requesting the whole file into
//     a cache that cannot hold it: GetRequestablePieces walks pieces in
//     priority order and stops once the capacity is spent, so the reader window
//     always wins and nothing behind it is re-fetched only to be evicted again.
//     Stream()/Preload() additionally skip File.Download() in RAM mode, so
//     pieces outside the reader window are never even prioritised.
//
// Buffers are handed out by value (a slice header copied under the lock, the
// copy itself done outside it), so a concurrent eviction can never pull memory
// out from under an in-flight read — the GC keeps the array alive for whoever
// still references it. That rules out a buffer pool here; piece churn is a few
// allocations per second, which the GC handles comfortably.

// ramMinBudget is the floor for the RAM cache. Below this the window around the
// reader (plus in-flight incomplete pieces) does not fit and the cache thrashes:
// evict → re-request → evict.
const ramMinBudget = 128 << 20

// ramEvictQueue is the depth of the completion-invalidation queue. Overflow is
// harmless (the io.EOF → MarkNotComplete path in storage.Piece.ReadAt still
// corrects the torrent's view lazily), so a full queue drops rather than blocks
// — blocking here would stall a chunk write inside anacrolix.
const ramEvictQueue = 4096

// ramEvicted identifies a piece whose data was dropped, so the owning torrent
// can be told to re-read its completion state.
type ramEvicted struct {
	Hash  metainfo.Hash
	Piece int
}

type ramPiece struct {
	data     []byte
	complete bool
	lastUse  uint64 // monotonic tick, not wall clock — strictly ordered and syscall-free
}

type ramTorrent struct {
	c      *ramCache
	hash   metainfo.Hash
	pieces []ramPiece
}

// ramCache is the shared budget across every torrent it opens.
type ramCache struct {
	mu     sync.Mutex
	used   int64
	budget int64
	clock  uint64
	tor    map[metainfo.Hash]*ramTorrent

	// capFn is handed to anacrolix by pointer; every torrent from this cache
	// shares the pointer, which is how the request strategy knows they draw
	// from one pool.
	capFn func() (int64, bool)

	evict     chan ramEvicted
	closeOnce sync.Once

	evictions atomic.Int64
	dropped   atomic.Int64 // eviction notices lost to a full queue
	overflow  atomic.Int64 // times the budget was exceeded with nothing evictable
}

func newRAMCache(budget int64) *ramCache {
	if budget < ramMinBudget {
		budget = ramMinBudget
	}
	c := &ramCache{
		budget: budget,
		tor:    make(map[metainfo.Hash]*ramTorrent),
		evict:  make(chan ramEvicted, ramEvictQueue),
	}
	c.capFn = func() (int64, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.budget, true
	}
	return c
}

// Budget returns the current cap in bytes.
func (c *ramCache) Budget() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.budget
}

// SetBudget re-caps the cache at runtime (MatriX clients POST /settings with a
// new CacheSize) and immediately evicts down to the new size.
func (c *ramCache) SetBudget(b int64) {
	if b < ramMinBudget {
		b = ramMinBudget
	}
	c.mu.Lock()
	c.budget = b
	c.evictLocked(nil, 0)
	used := c.used
	c.mu.Unlock()
	log.Info().Int64("budget_mb", b>>20).Int64("used_mb", used>>20).Msg("torrs: RAM cache budget updated")
}

// Used returns resident bytes.
func (c *ramCache) Used() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// Evictions is drained by BTServer to invalidate piece completion.
func (c *ramCache) Evictions() <-chan ramEvicted { return c.evict }

func (c *ramCache) Close() error {
	c.closeOnce.Do(func() { close(c.evict) })
	return nil
}

// Stats reports cache occupancy for the admin health endpoint.
func (c *ramCache) Stats() (usedMB, budgetMB, evictions int64) {
	c.mu.Lock()
	usedMB, budgetMB = c.used>>20, c.budget>>20
	c.mu.Unlock()
	return usedMB, budgetMB, c.evictions.Load()
}

// OpenTorrent implements storage.ClientImpl.
func (c *ramCache) OpenTorrent(_ context.Context, info *metainfo.Info, hash metainfo.Hash) (storage.TorrentImpl, error) {
	t := &ramTorrent{c: c, hash: hash, pieces: make([]ramPiece, info.NumPieces())}

	c.mu.Lock()
	c.tor[hash] = t
	c.mu.Unlock()

	capacity := storage.TorrentCapacity(&c.capFn)
	return storage.TorrentImpl{
		Piece:    func(p metainfo.Piece) storage.PieceImpl { return ramPieceRef{t: t, idx: p.Index(), size: p.Length()} },
		Capacity: capacity,
		Close:    t.close,
	}, nil
}

// close frees every piece of a dropped torrent.
func (t *ramTorrent) close() error {
	c := t.c
	c.mu.Lock()
	var freed int64
	for i := range t.pieces {
		if t.pieces[i].data != nil {
			freed += int64(len(t.pieces[i].data))
			t.pieces[i].data = nil
			t.pieces[i].complete = false
		}
	}
	c.used -= freed
	if c.used < 0 {
		c.used = 0
	}
	delete(c.tor, t.hash)
	c.mu.Unlock()
	if freed > 0 {
		log.Debug().Str("hash", t.hash.HexString()).Int64("freed_mb", freed>>20).Msg("torrs: RAM cache released torrent")
	}
	return nil
}

// touchLocked marks a piece as most-recently-used. Caller holds c.mu.
func (c *ramCache) touchLocked(p *ramPiece) {
	c.clock++
	p.lastUse = c.clock
}

type ramVictim struct {
	t       *ramTorrent
	idx     int
	lastUse uint64
	size    int64
}

// ramEvictTargetNum/Den set the low-water mark: an eviction round frees down to
// 90% of the budget rather than to exactly the budget. Each round costs a scan
// and sort of every resident piece, so freeing in batches keeps that off the
// hot path — with 256 KB pieces a "free exactly one piece" policy would re-scan
// thousands of entries for every piece downloaded.
const (
	ramEvictTargetNum = 9
	ramEvictTargetDen = 10
)

// evictLocked frees complete pieces, least-recently-used first, until the cache
// fits its budget. The piece identified by (pinT, pinIdx) is never evicted — it
// is the one the caller is writing to. Caller holds c.mu.
func (c *ramCache) evictLocked(pinT *ramTorrent, pinIdx int) {
	if c.used <= c.budget {
		return
	}
	target := c.budget * ramEvictTargetNum / ramEvictTargetDen

	victims := make([]ramVictim, 0, 64)
	for _, t := range c.tor {
		for i := range t.pieces {
			p := &t.pieces[i]
			// Incomplete pieces are pinned: their chunks are already accounted
			// for by anacrolix and would never be re-requested.
			if p.data == nil || !p.complete {
				continue
			}
			if t == pinT && i == pinIdx {
				continue
			}
			victims = append(victims, ramVictim{t: t, idx: i, lastUse: p.lastUse, size: int64(len(p.data))})
		}
	}
	if len(victims) == 0 {
		c.overflow.Add(1)
		return
	}
	sort.Slice(victims, func(i, j int) bool { return victims[i].lastUse < victims[j].lastUse })

	for _, v := range victims {
		if c.used <= target {
			break
		}
		p := &v.t.pieces[v.idx]
		if p.data == nil {
			continue
		}
		p.data = nil
		p.complete = false
		c.used -= v.size
		c.evictions.Add(1)
		// Non-blocking: a full queue only costs one lazy re-sync later.
		select {
		case c.evict <- ramEvicted{Hash: v.t.hash, Piece: v.idx}:
		default:
			c.dropped.Add(1)
		}
	}
	if c.used < 0 {
		c.used = 0
	}
}

// ---------------------------------------------------------------------------
//  PieceImpl
// ---------------------------------------------------------------------------

type ramPieceRef struct {
	t    *ramTorrent
	idx  int
	size int64
}

var _ storage.PieceImpl = ramPieceRef{}

func (r ramPieceRef) ReadAt(b []byte, off int64) (int, error) {
	c := r.t.c
	c.mu.Lock()
	p := &r.t.pieces[r.idx]
	buf := p.data
	if buf != nil {
		c.touchLocked(p)
	}
	c.mu.Unlock()

	if buf == nil {
		// Evicted (or never written). io.EOF is the contract that makes
		// storage.Piece.ReadAt call MarkNotComplete and the reader retry.
		return 0, io.EOF
	}
	if off < 0 || off >= int64(len(buf)) {
		return 0, io.EOF
	}
	n := copy(b, buf[off:])
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (r ramPieceRef) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(b)) > r.size {
		return 0, io.ErrShortWrite
	}
	c := r.t.c
	c.mu.Lock()
	p := &r.t.pieces[r.idx]
	if p.data == nil {
		p.data = make([]byte, r.size)
		c.used += r.size
		c.touchLocked(p)
		// Free space for what we just allocated, never the piece being written.
		c.evictLocked(r.t, r.idx)
	} else {
		c.touchLocked(p)
	}
	buf := p.data
	c.mu.Unlock()

	// Copied outside the lock: even if this piece is evicted concurrently the
	// array stays alive for us, and the piece will simply be re-fetched.
	return copy(buf[off:], b), nil
}

func (r ramPieceRef) MarkComplete() error {
	c := r.t.c
	c.mu.Lock()
	p := &r.t.pieces[r.idx]
	if p.data != nil {
		p.complete = true
		c.touchLocked(p)
	}
	c.mu.Unlock()
	return nil
}

func (r ramPieceRef) MarkNotComplete() error {
	c := r.t.c
	c.mu.Lock()
	r.t.pieces[r.idx].complete = false
	c.mu.Unlock()
	return nil
}

func (r ramPieceRef) Completion() storage.Completion {
	c := r.t.c
	c.mu.Lock()
	p := &r.t.pieces[r.idx]
	done := p.complete && p.data != nil
	c.mu.Unlock()
	return storage.Completion{Complete: done, Ok: true}
}
