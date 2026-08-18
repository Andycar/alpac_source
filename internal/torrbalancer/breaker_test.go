package torrbalancer

import (
	"testing"
	"time"
)

// The stream circuit-breaker must catch a HALF-WEDGED TorrServer (/echo OK,
// /stream silent — invisible to the probe loop) while never condemning a
// healthy backend over ONE dead torrent retried in a loop.

func TestBreakerDedupesSameTorrent(t *testing.T) {
	p, _ := buildPool(t, StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1})
	b := p.Backends()[0]

	// A single dead torrent hammered 10× = ONE distinct strike → no quarantine.
	for i := 0; i < 10; i++ {
		p.RecordStreamStrike(b, "deadbeef")
	}
	if !b.Available() {
		t.Fatal("one dead torrent quarantined a healthy backend")
	}
}

func TestBreakerTripsOnDistinctTorrents(t *testing.T) {
	p, _ := buildPool(t,
		StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1},
		StoredBackend{Name: "b", Host: "http://b:9080", Weight: 1},
	)
	zombie := p.Backends()[0]
	healthy := p.Backends()[1]

	trips := make(chan bool, 1)
	p.OnZombie = func(bk *Backend, echoAlive bool) {
		if bk == zombie {
			trips <- true
		}
	}

	// FailThreshold (default 3) DISTINCT torrents fail → quarantine.
	p.RecordStreamStrike(zombie, "hash-1")
	p.RecordStreamStrike(zombie, "hash-2")
	if !zombie.Available() {
		t.Fatal("quarantined below threshold")
	}
	p.RecordStreamStrike(zombie, "hash-3")
	if zombie.Available() {
		t.Fatal("threshold reached but backend still available")
	}
	if !zombie.Quarantined() {
		t.Fatal("Quarantined() must report the tripped state")
	}
	select {
	case <-trips:
	case <-time.After(5 * time.Second):
		t.Fatal("OnZombie callback never fired")
	}

	// HRW must re-home every torrent to the surviving backend.
	for _, k := range keys(50) {
		got := p.PickForHash(k, nil)
		if got != healthy {
			t.Fatalf("key %q routed to quarantined backend", k)
		}
	}

	// The admin snapshot reflects the quarantine.
	for _, s := range p.Snapshot() {
		if s.ID == zombie.ID {
			if s.Healthy || !s.Quarantined {
				t.Fatalf("snapshot: want Healthy=false Quarantined=true, got %+v", s)
			}
		}
	}
}

func TestBreakerSuccessClearsStrikes(t *testing.T) {
	p, _ := buildPool(t, StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1})
	b := p.Backends()[0]

	p.RecordStreamStrike(b, "hash-1")
	p.RecordStreamStrike(b, "hash-2")
	p.RecordStreamOK(b) // a stream answered — evidence reset
	p.RecordStreamStrike(b, "hash-3")
	if !b.Available() {
		t.Fatal("strikes must reset after a successful stream")
	}
}

func TestBreakerQuarantineExpires(t *testing.T) {
	p, _ := buildPool(t, StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1})
	b := p.Backends()[0]

	p.OnZombie = func(*Backend, bool) {}
	p.RecordStreamStrike(b, "h1")
	p.RecordStreamStrike(b, "h2")
	p.RecordStreamStrike(b, "h3")
	if b.Available() {
		t.Fatal("expected quarantine")
	}
	// Simulate the cooldown elapsing (half-open state).
	b.quarantineUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if !b.Available() {
		t.Fatal("quarantine must expire — breaker half-opens")
	}
	// Still wedged → fresh distinct strikes re-trip it.
	p.RecordStreamStrike(b, "h4")
	p.RecordStreamStrike(b, "h5")
	p.RecordStreamStrike(b, "h6")
	if b.Available() {
		t.Fatal("expected re-trip after half-open with fresh strikes")
	}
}
