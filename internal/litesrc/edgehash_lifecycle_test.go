package litesrc

import (
	"testing"
	"time"
)

// resetEdgeHashState clears the process-wide edge_hash registry and global
// fallback so each test starts from a known state.
func resetEdgeHashState(t *testing.T) {
	t.Helper()
	edgeHashRegistryMu.Lock()
	for k := range edgeHashRegistry {
		delete(edgeHashRegistry, k)
	}
	edgeHashRegistryMu.Unlock()
	globalEdgeHash.Store("")
	globalEdgeHashAt.Store(0)
}

// newTestWSClient builds a WS client that is never dialed — enough to exercise
// the registry/hash bookkeeping without touching the network.
func newTestWSClient(hash string) *mirageWSClient {
	c := &mirageWSClient{
		seekCh: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	if hash != "" {
		c.edgeHash.Store(hash)
	}
	return c
}

// TestGetAnyEdgeHashRejectsStaleHash: once no live WS client holds an
// edge_hash, the last globally seen value may only be used while it is fresh.
// Serving it forever meant that after the single alloha WS session died, every
// CDN request went out with a dead Accepts-Controls and came back 403 — which
// looks like "the token expired and everything got blocked".
func TestGetAnyEdgeHashRejectsStaleHash(t *testing.T) {
	resetEdgeHashState(t)

	storeGlobalEdgeHash("hash-fresh")
	if got := GetAnyEdgeHash(); got != "hash-fresh" {
		t.Fatalf("fresh global hash should be used, got %q", got)
	}

	// Backdate past the TTL: no live client, so it must no longer be offered.
	globalEdgeHashAt.Store(time.Now().Add(-globalEdgeHashTTL - time.Minute).UnixNano())
	if got := GetAnyEdgeHash(); got != "" {
		t.Fatalf("stale global hash must not be used, got %q", got)
	}

	// A live registered client wins regardless of the stale global value.
	live := newTestWSClient("hash-live")
	RegisterEdgeHashClient(live, "token-movie-1", "https://player.example")
	if got := GetAnyEdgeHash(); got != "hash-live" {
		t.Fatalf("live client hash should win, got %q", got)
	}

	// A closed client is not live: EdgeHash() returns "" and we fall back to the
	// (still stale) global, i.e. nothing.
	live.Close()
	if got := GetAnyEdgeHash(); got != "" {
		t.Fatalf("closed client must not supply a hash, got %q", got)
	}
}

// TestRegisterEdgeHashClientAliasDoesNotCloseLiveSession: linkHost is shared by
// every stream of a balancer, so a collision on it means "another stream", not
// "this stream again". Closing on it tore down the WS session of a stream that
// was still playing — no more heartbeats or edge_hash rotation for it, while its
// player kept pulling segments, which the CDN answers with 403 minutes later.
func TestRegisterEdgeHashClientAliasDoesNotCloseLiveSession(t *testing.T) {
	resetEdgeHashState(t)

	const sharedAlias = "https://player.example"

	playing := newTestWSClient("hash-a")
	RegisterEdgeHashClient(playing, "token-movie-A", sharedAlias)

	// A different title resolves and registers its own WS under the same alias.
	other := newTestWSClient("hash-b")
	RegisterEdgeHashClient(other, "token-movie-B", sharedAlias)

	select {
	case <-playing.done:
		t.Fatal("alias collision closed the WS session of a stream that was still playing")
	default:
	}
	if got := GetEdgeHash("token-movie-A"); got != "hash-a" {
		t.Fatalf("first client should still serve its hash under its own key, got %q", got)
	}

	// Identity collision DOES close the old client: same stream, re-resolved.
	replacement := newTestWSClient("hash-a2")
	RegisterEdgeHashClient(replacement, "token-movie-A", sharedAlias)
	select {
	case <-playing.done:
	default:
		t.Fatal("identity collision should close the superseded client")
	}
	if got := GetEdgeHash("token-movie-A"); got != "hash-a2" {
		t.Fatalf("replacement should serve the fresh hash, got %q", got)
	}
}

// TestRegisterEdgeHashClientPrunesClosedClients: nothing else removes registry
// entries, so dead sessions must not accumulate for the life of the process.
func TestRegisterEdgeHashClientPrunesClosedClients(t *testing.T) {
	resetEdgeHashState(t)

	dead := newTestWSClient("hash-dead")
	RegisterEdgeHashClient(dead, "token-movie-dead", "https://player.example")
	dead.Close()

	live := newTestWSClient("hash-live")
	RegisterEdgeHashClient(live, "token-movie-live", "https://player.example")

	edgeHashRegistryMu.RLock()
	_, stillThere := edgeHashRegistry["token-movie-dead"]
	edgeHashRegistryMu.RUnlock()
	if stillThere {
		t.Fatal("closed client's key should have been pruned from the registry")
	}
}
