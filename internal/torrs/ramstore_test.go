//go:build torrs

package torrs

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

const testPieceLen = 1 << 20 // 1 MiB

// ramTestInfo builds a metainfo.Info with n pieces of testPieceLen.
func ramTestInfo(n int) *metainfo.Info {
	return &metainfo.Info{
		Name:        "test",
		PieceLength: testPieceLen,
		Pieces:      make([]byte, 20*n), // v1: 20 bytes of SHA-1 per piece
		Length:      int64(n) * testPieceLen,
	}
}

func ramTestHash(b byte) metainfo.Hash {
	var h metainfo.Hash
	h[0] = b
	return h
}

// writePiece fills a whole piece and marks it complete, the way anacrolix does
// after a successful hash check.
func writePiece(t *testing.T, ti storage.TorrentImpl, info *metainfo.Info, idx int, fill byte) storage.PieceImpl {
	t.Helper()
	p := ti.Piece(info.Piece(idx))
	buf := bytes.Repeat([]byte{fill}, testPieceLen)
	if _, err := p.WriteAt(buf, 0); err != nil {
		t.Fatalf("WriteAt(piece %d): %v", idx, err)
	}
	if err := p.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete(piece %d): %v", idx, err)
	}
	return p
}

func TestRAMCacheReadWriteRoundTrip(t *testing.T) {
	c := newRAMCache(ramMinBudget)
	defer c.Close()

	info := ramTestInfo(4)
	ti, err := c.OpenTorrent(context.Background(), info, ramTestHash(1))
	if err != nil {
		t.Fatal(err)
	}

	p := writePiece(t, ti, info, 0, 0xAB)
	if got := p.Completion(); !got.Ok || !got.Complete {
		t.Fatalf("Completion after MarkComplete = %+v, want complete", got)
	}

	out := make([]byte, 16)
	n, err := p.ReadAt(out, 4)
	if err != nil || n != len(out) {
		t.Fatalf("ReadAt = (%d, %v), want (%d, nil)", n, err, len(out))
	}
	if !bytes.Equal(out, bytes.Repeat([]byte{0xAB}, 16)) {
		t.Errorf("ReadAt returned %x, want AB…", out)
	}

	if used := c.Used(); used != testPieceLen {
		t.Errorf("Used = %d, want one piece (%d)", used, testPieceLen)
	}
}

// The core promise: a 40 GB film through a small cache. Pieces behind the
// reader are dropped, and a dropped piece reports itself as not-complete so
// anacrolix re-requests it instead of serving a hole.
func TestRAMCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newRAMCache(ramMinBudget)
	defer c.Close()
	budgetPieces := int(ramMinBudget / testPieceLen)

	info := ramTestInfo(budgetPieces + 8)
	ti, err := c.OpenTorrent(context.Background(), info, ramTestHash(2))
	if err != nil {
		t.Fatal(err)
	}

	pieces := make([]storage.PieceImpl, 0, budgetPieces+8)
	for i := 0; i < budgetPieces+8; i++ {
		pieces = append(pieces, writePiece(t, ti, info, i, byte(i)))
	}

	if used := c.Used(); used > ramMinBudget {
		t.Errorf("Used = %d, must stay within budget %d", used, ramMinBudget)
	}
	if ev := c.evictions.Load(); ev < 8 {
		t.Errorf("evictions = %d, want at least the 8 overflow pieces", ev)
	}

	// The oldest pieces went first…
	if got := pieces[0].Completion(); got.Complete {
		t.Error("piece 0 (least recently used) should have been evicted")
	}
	// …and an evicted piece reads as EOF, which is what makes
	// storage.Piece.ReadAt call MarkNotComplete and the reader retry.
	if n, err := pieces[0].ReadAt(make([]byte, 8), 0); n != 0 || err != io.EOF {
		t.Errorf("evicted ReadAt = (%d, %v), want (0, io.EOF)", n, err)
	}
	// The most recent piece is still resident.
	if got := pieces[len(pieces)-1].Completion(); !got.Complete {
		t.Error("most recently written piece must survive eviction")
	}
}

// A piece still receiving chunks must never be evicted: anacrolix already
// counts those chunks as delivered and would not re-request them.
func TestRAMCachePinsIncompletePieces(t *testing.T) {
	c := newRAMCache(ramMinBudget)
	defer c.Close()
	budgetPieces := int(ramMinBudget / testPieceLen)

	info := ramTestInfo(budgetPieces + 4)
	ti, err := c.OpenTorrent(context.Background(), info, ramTestHash(3))
	if err != nil {
		t.Fatal(err)
	}

	// Piece 0: written but never marked complete — in flight.
	inflight := ti.Piece(info.Piece(0))
	if _, err := inflight.WriteAt(bytes.Repeat([]byte{0x11}, 4096), 0); err != nil {
		t.Fatal(err)
	}
	// Fill well past the budget with complete pieces.
	for i := 1; i < budgetPieces+4; i++ {
		writePiece(t, ti, info, i, byte(i))
	}

	out := make([]byte, 8)
	n, err := inflight.ReadAt(out, 0)
	if err != nil || n != len(out) {
		t.Fatalf("in-flight piece was evicted: ReadAt = (%d, %v)", n, err)
	}
	if !bytes.Equal(out, bytes.Repeat([]byte{0x11}, 8)) {
		t.Errorf("in-flight data corrupted: %x", out)
	}
}

func TestRAMCacheEvictionNotifiesTorrent(t *testing.T) {
	c := newRAMCache(ramMinBudget)
	defer c.Close()
	budgetPieces := int(ramMinBudget / testPieceLen)

	hash := ramTestHash(4)
	info := ramTestInfo(budgetPieces + 2)
	ti, err := c.OpenTorrent(context.Background(), info, hash)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < budgetPieces+2; i++ {
		writePiece(t, ti, info, i, byte(i))
	}

	select {
	case ev := <-c.Evictions():
		if ev.Hash != hash {
			t.Errorf("eviction hash = %v, want %v", ev.Hash, hash)
		}
	default:
		t.Fatal("eviction produced no invalidation notice")
	}
	if c.dropped.Load() != 0 {
		t.Errorf("dropped %d notices with a %d-deep queue", c.dropped.Load(), ramEvictQueue)
	}
}

func TestRAMCacheSetBudgetShrinks(t *testing.T) {
	c := newRAMCache(4 * ramMinBudget)
	defer c.Close()

	info := ramTestInfo(200)
	ti, err := c.OpenTorrent(context.Background(), info, ramTestHash(5))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		writePiece(t, ti, info, i, byte(i))
	}
	if c.Used() <= ramMinBudget {
		t.Fatalf("precondition: want more than %d resident, got %d", ramMinBudget, c.Used())
	}

	c.SetBudget(ramMinBudget)
	if used := c.Used(); used > ramMinBudget {
		t.Errorf("after SetBudget Used = %d, want ≤ %d", used, ramMinBudget)
	}

	// Below the floor the budget is clamped, not honoured — a 1 MB cache would
	// thrash on every read.
	c.SetBudget(1 << 20)
	if got := c.Budget(); got != ramMinBudget {
		t.Errorf("Budget = %d, want the %d floor", got, int64(ramMinBudget))
	}
}

func TestRAMCacheCloseTorrentFreesMemory(t *testing.T) {
	c := newRAMCache(ramMinBudget)
	defer c.Close()

	info := ramTestInfo(8)
	ti, err := c.OpenTorrent(context.Background(), info, ramTestHash(6))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		writePiece(t, ti, info, i, byte(i))
	}
	if c.Used() == 0 {
		t.Fatal("precondition: nothing resident")
	}

	if err := ti.Close(); err != nil {
		t.Fatal(err)
	}
	if used := c.Used(); used != 0 {
		t.Errorf("Used after torrent close = %d, want 0", used)
	}
}

// Capacity is what tells anacrolix the data can vanish (torrent.hasStorageCap):
// without it a read of an evicted piece surfaces as an I/O error to the player
// instead of being retried, and the request strategy would happily queue the
// whole file into a cache that cannot hold it.
func TestRAMCacheDeclaresSharedCapacity(t *testing.T) {
	c := newRAMCache(ramMinBudget)
	defer c.Close()

	info := ramTestInfo(4)
	a, err := c.OpenTorrent(context.Background(), info, ramTestHash(7))
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.OpenTorrent(context.Background(), info, ramTestHash(8))
	if err != nil {
		t.Fatal(err)
	}
	if a.Capacity == nil || b.Capacity == nil {
		t.Fatal("Capacity must be set so anacrolix knows pieces can vanish")
	}
	if a.Capacity != b.Capacity {
		t.Error("torrents sharing one cache must share the capacity pointer")
	}
	capacity, capped := (*a.Capacity)()
	if !capped || capacity != ramMinBudget {
		t.Errorf("Capacity() = (%d, %v), want (%d, true)", capacity, capped, int64(ramMinBudget))
	}
}
