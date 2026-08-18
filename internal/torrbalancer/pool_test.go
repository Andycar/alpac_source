package torrbalancer

import (
	"fmt"
	"testing"

	"lampac-go/internal/config"
)

// buildPool returns a pool with the given backends, all marked healthy (the
// reconcile in NewPool sets new backends optimistically healthy, so no probe
// is needed). Returns the pool and the live backend IDs in insertion order.
func buildPool(t *testing.T, specs ...StoredBackend) (*Pool, []string) {
	t.Helper()
	s, err := NewStore(t.TempDir(), config.TorrServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range specs {
		if _, err := s.Add(sp.Name, sp.Host, sp.Login, sp.Password, sp.Weight, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	p := NewPool(s)
	ids := make([]string, 0, len(p.Backends()))
	for _, b := range p.Backends() {
		ids = append(ids, b.ID)
	}
	return p, ids
}

func keys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("infohash-%06d", i)
	}
	return out
}

func TestPickForHashDeterministic(t *testing.T) {
	p, _ := buildPool(t,
		StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1},
		StoredBackend{Name: "b", Host: "http://b:9080", Weight: 1},
		StoredBackend{Name: "c", Host: "http://c:9080", Weight: 1},
	)
	for _, k := range keys(200) {
		first := p.PickForHash(k, nil)
		if first == nil {
			t.Fatalf("key %q got nil backend", k)
		}
		for i := 0; i < 5; i++ {
			if again := p.PickForHash(k, nil); again != first {
				t.Fatalf("key %q non-deterministic: %v vs %v", k, first.ID, again.ID)
			}
		}
	}
}

func TestPickForHashStickyOnChurn(t *testing.T) {
	p, ids := buildPool(t,
		StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1},
		StoredBackend{Name: "b", Host: "http://b:9080", Weight: 1},
		StoredBackend{Name: "c", Host: "http://c:9080", Weight: 1},
	)
	ks := keys(300)
	before := map[string]string{}
	for _, k := range ks {
		before[k] = p.PickForHash(k, nil).ID
	}

	// Remove one backend and reconcile.
	removed := ids[1]
	keep := []StoredBackend{}
	for _, b := range p.Backends() {
		if b.ID == removed {
			continue
		}
		keep = append(keep, StoredBackend{ID: b.ID, Name: b.Name, Host: b.Host, Weight: b.Weight, Enabled: true})
	}
	p.Reconcile(keep)

	for _, k := range ks {
		after := p.PickForHash(k, nil)
		if after == nil {
			t.Fatalf("key %q unroutable after churn", k)
		}
		if before[k] == removed {
			if after.ID == removed {
				t.Fatalf("key %q still routed to removed backend", k)
			}
		} else if after.ID != before[k] {
			t.Fatalf("key %q moved despite its backend surviving: %s -> %s", k, before[k], after.ID)
		}
	}
}

func TestWeightedSpread(t *testing.T) {
	p, ids := buildPool(t,
		StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1},
		StoredBackend{Name: "b", Host: "http://b:9080", Weight: 1},
		StoredBackend{Name: "c", Host: "http://c:9080", Weight: 8}, // 80% expected
	)
	counts := map[string]int{}
	total := 10000
	for _, k := range keys(total) {
		counts[p.PickForHash(k, nil).ID]++
	}
	heavy := counts[ids[2]]
	// Exact expected share is 8/10 = 80%; allow a generous band for hash noise.
	if heavy < 7400 || heavy > 8500 {
		t.Fatalf("weight-8 backend got %d/%d (%.1f%%), want ~80%%", heavy, total, 100*float64(heavy)/float64(total))
	}
	if counts[ids[0]] < 600 || counts[ids[1]] < 600 {
		t.Fatalf("light backends starved: %d / %d", counts[ids[0]], counts[ids[1]])
	}
}

func TestGroupFilter(t *testing.T) {
	p, ids := buildPool(t,
		StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1},
		StoredBackend{Name: "b", Host: "http://b:9080", Weight: 1},
	)
	onlyB := func(id string) bool { return id == ids[1] }
	for _, k := range keys(100) {
		if got := p.PickForHash(k, onlyB); got == nil || got.ID != ids[1] {
			t.Fatalf("key %q: filter to b failed, got %v", k, got)
		}
	}
	none := func(string) bool { return false }
	if got := p.PickForHash("x", none); got != nil {
		t.Fatalf("empty allow-set must yield nil, got %v", got.ID)
	}
}

func TestHealthAndEnabledGating(t *testing.T) {
	p, ids := buildPool(t,
		StoredBackend{Name: "a", Host: "http://a:9080", Weight: 1},
		StoredBackend{Name: "b", Host: "http://b:9080", Weight: 1},
	)
	// Mark backend a unhealthy → never picked.
	for _, b := range p.Backends() {
		if b.ID == ids[0] {
			b.healthy.Store(false)
		}
	}
	for _, k := range keys(50) {
		if got := p.PickForHash(k, nil); got == nil || got.ID != ids[1] {
			t.Fatalf("unhealthy backend a still selected for %q: %v", k, got)
		}
	}
	// Disable b too via reconcile → nothing routable.
	p.Reconcile([]StoredBackend{
		{ID: ids[0], Name: "a", Host: "http://a:9080", Weight: 1, Enabled: false},
		{ID: ids[1], Name: "b", Host: "http://b:9080", Weight: 1, Enabled: false},
	})
	if got := p.PickForHash("x", nil); got != nil {
		t.Fatalf("disabled pool must yield nil, got %v", got.ID)
	}
	if p.HasEnabledBackends() {
		t.Fatal("HasEnabledBackends should be false")
	}
}

func TestEmptyInfohashUsesPrimary(t *testing.T) {
	p, _ := buildPool(t,
		StoredBackend{Name: "zeta", Host: "http://z:9080", Weight: 1},
		StoredBackend{Name: "alpha", Host: "http://a:9080", Weight: 1},
	)
	// PickPrimary tie-breaks by name at equal conns → "alpha".
	prim := p.PickPrimary(nil)
	viaHash := p.PickForHash("", nil)
	if prim == nil || viaHash == nil || prim.ID != viaHash.ID {
		t.Fatalf("empty infohash should defer to PickPrimary: %v vs %v", prim, viaHash)
	}
	if prim.Name != "alpha" {
		t.Fatalf("primary tie-break by name failed, got %q", prim.Name)
	}
}
