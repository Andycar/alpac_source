//go:build torrs

package torrs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// TestRAMCacheStreamsFileLargerThanBudget is the whole point of RAM mode: play
// a file that does not fit in the cache. A real seeder feeds a real leecher
// whose storage is the RAM cache, sized well under the file, so pieces are
// evicted behind the reader while it is still streaming. The bytes that come
// out must still be byte-for-byte correct — which only holds if an evicted
// piece is re-requested (Completion → false, ReadAt → io.EOF) and the reader
// retries instead of erroring, and if the request strategy respects Capacity
// instead of trying to pull the whole file into memory.
func TestRAMCacheStreamsFileLargerThanBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 32 MB between two local clients")
	}

	const (
		fileSize    = 32 << 20
		pieceLength = 256 << 10
		budget      = 8 << 20 // a quarter of the file
	)

	seedDir := t.TempDir()
	payloadPath := filepath.Join(seedDir, "movie.bin")
	want := writeDeterministicFile(t, payloadPath, fileSize)

	// Build the torrent from the file on disk.
	info := metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(payloadPath); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}

	// Seeder: plain file storage over the directory holding the payload.
	seedCfg := torrent.NewDefaultClientConfig()
	seedCfg.DataDir = seedDir
	seedCfg.Seed = true
	seedCfg.NoDHT = true
	seedCfg.DisableTrackers = true
	seedCfg.NoDefaultPortForwarding = true
	seedCfg.ListenPort = 0 // the default 42069 collides with the leecher
	// NewFile, not NewFileByInfoHash: the payload sits at seedDir/movie.bin, the
	// layout BuildFromFilePath described.
	seedCfg.DefaultStorage = storage.NewFile(seedDir)
	seeder, err := torrent.NewClient(seedCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer seeder.Close()

	st, err := seeder.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	<-st.GotInfo()
	st.VerifyData()
	// Don't race the leecher against hashing — a seeder with nothing verified
	// has nothing to offer.
	for deadline := time.Now().Add(60 * time.Second); st.BytesCompleted() < st.Length(); {
		if time.Now().After(deadline) {
			t.Fatalf("seeder verified only %d/%d bytes", st.BytesCompleted(), st.Length())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Leecher: the RAM cache, deliberately smaller than the file. Built by hand
	// to get under ramMinBudget — the production floor exists so real playback
	// has room, but here a tight budget is the point.
	cache := &ramCache{
		budget: budget,
		tor:    map[metainfo.Hash]*ramTorrent{},
		evict:  make(chan ramEvicted, ramEvictQueue),
	}
	cache.capFn = func() (int64, bool) { return cache.Budget(), true }
	defer cache.Close()
	leechCfg := torrent.NewDefaultClientConfig()
	leechCfg.DataDir = t.TempDir()
	leechCfg.Seed = false
	leechCfg.NoDHT = true
	leechCfg.DisableTrackers = true
	leechCfg.NoDefaultPortForwarding = true
	leechCfg.ListenPort = 0
	leechCfg.DefaultStorage = cache
	leecher, err := torrent.NewClient(leechCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer leecher.Close()

	lt, err := leecher.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	<-lt.GotInfo()

	// Drain invalidations the way BTServer.ramEvictionLoop does, so evicted
	// pieces are re-requested promptly rather than only on the next failed read.
	// Started once the torrent exists, so the goroutine needs no synchronisation.
	go func() {
		for ev := range cache.Evictions() {
			if ev.Piece >= 0 && ev.Piece < lt.NumPieces() {
				lt.Piece(ev.Piece).UpdateCompletion()
			}
		}
	}()

	if n := lt.AddClientPeer(seeder); n == 0 {
		t.Fatal("no peer address from the seeder")
	}

	// Stream it exactly like Stream() does in RAM mode: reader window only, no
	// File.Download().
	f := lt.Files()[0]
	reader := f.NewReader()
	defer reader.Close()
	reader.SetReadahead(4 << 20)
	reader.SetResponsive()

	done := make(chan error, 1)
	got := sha256.New()
	go func() {
		_, err := io.Copy(got, reader)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("streaming failed: %v", err)
		}
	case <-time.After(3 * time.Minute):
		t.Fatalf("stream stalled: %d/%d bytes, cache %d MB used, %d evictions",
			lt.BytesCompleted(), lt.Length(), cache.Used()>>20, cache.evictions.Load())
	}

	if h := got.Sum(nil); string(h) != string(want) {
		t.Fatalf("streamed data differs from the source file:\n got %x\nwant %x", h, want)
	}

	// Rewind. The head of the file was evicted long ago, so this is the path
	// that actually matters: read an evicted piece → io.EOF → MarkNotComplete →
	// reader re-syncs completion and waits for the re-download. If any of that
	// is wrong the user sees a dead player on every seek backwards.
	evictedBefore := cache.evictions.Load()
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 4<<20)
	rewound := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(reader, head)
		rewound <- err
	}()
	select {
	case err := <-rewound:
		if err != nil {
			t.Fatalf("rewind read failed: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("rewind stalled — evicted pieces were not re-requested")
	}

	wantHead := make([]byte, len(head))
	pf, err := os.Open(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	if _, err := io.ReadFull(pf, wantHead); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(head, wantHead) {
		t.Error("data read after rewind does not match the source file")
	}
	t.Logf("rewind re-fetched evicted pieces (%d further evictions)", cache.evictions.Load()-evictedBefore)

	// The interesting assertions: the file went through a cache a quarter its
	// size, and memory never ran past the budget.
	if ev := cache.evictions.Load(); ev == 0 {
		t.Error("no pieces were evicted — the test did not exercise RAM mode")
	}
	if used := cache.Used(); used > budget {
		t.Errorf("cache held %d bytes, budget is %d", used, budget)
	}
	if cache.overflow.Load() > 0 {
		t.Logf("budget was briefly exceeded %d times (in-flight pieces are pinned)", cache.overflow.Load())
	}
	t.Logf("streamed %d MB through a %d MB cache: %d evictions",
		fileSize>>20, budget>>20, cache.evictions.Load())
}

// writeDeterministicFile writes n bytes of reproducible pseudo-random data and
// returns their sha256.
func writeDeterministicFile(t *testing.T, path string, n int) []byte {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 64<<10)
	var seed uint64 = 0x9E3779B97F4A7C15
	for written := 0; written < n; {
		for i := 0; i+8 <= len(buf); i += 8 {
			// xorshift64* — cheap, no crypto/rand cost, same bytes every run.
			seed ^= seed >> 12
			seed ^= seed << 25
			seed ^= seed >> 27
			binary.LittleEndian.PutUint64(buf[i:], seed*2685821657736338717)
		}
		chunk := buf
		if rem := n - written; rem < len(chunk) {
			chunk = chunk[:rem]
		}
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
		h.Write(chunk)
		written += len(chunk)
	}
	return h.Sum(nil)
}
