package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestStoreSeedAddUpdateDelete(t *testing.T) {
	dir := t.TempDir()
	seed := config.ClusterConfig{
		Enable: true,
		Mode:   "primary",
		Nodes: []config.ClusterNode{
			{Host: "http://seed1.example.com", Weight: 2},
			{Host: "http://seed2.example.com", Weight: 1},
		},
	}
	s, err := NewStore(dir, seed)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := len(s.Snapshot()); got != 2 {
		t.Fatalf("expected 2 seeded nodes, got %d", got)
	}
	// File must have been written.
	if _, err := os.Stat(filepath.Join(dir, "database", "cluster", "nodes.json")); err != nil {
		t.Fatalf("nodes.json missing: %v", err)
	}

	// Reopen — should not re-seed.
	s2, err := NewStore(dir, seed)
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	if got := len(s2.Snapshot()); got != 2 {
		t.Fatalf("expected 2 after reopen, got %d", got)
	}

	// Add.
	n, err := s2.Add("node3", "node3.example.com", 5, true, "EU", "test")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.HasPrefix(n.Host, "http://") {
		t.Fatalf("Add should normalize host scheme, got %q", n.Host)
	}
	if n.Weight != 5 {
		t.Fatalf("Weight=%d, want 5", n.Weight)
	}

	// Duplicate host should fail.
	if _, err := s2.Add("dup", "http://node3.example.com", 1, true, "", ""); err == nil {
		t.Fatalf("expected duplicate-host error")
	}

	// Update.
	w := 10
	enabled := false
	if _, err := s2.Update(n.ID, UpdatePatch{Weight: &w, Enabled: &enabled}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	cur, ok := s2.Find(n.ID)
	if !ok || cur.Weight != 10 || cur.Enabled != false {
		t.Fatalf("Update did not stick: %+v", cur)
	}

	// Delete.
	if err := s2.Delete(n.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s2.Find(n.ID); ok {
		t.Fatalf("Delete did not remove node")
	}
}

func TestPoolReconcile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, config.ClusterConfig{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	n1, _ := s.Add("n1", "http://a.example", 1, true, "", "")
	n2, _ := s.Add("n2", "http://b.example", 1, true, "", "")

	p := NewPool(config.ClusterConfig{APIKey: "k"}, s)
	if got := len(p.Nodes()); got != 2 {
		t.Fatalf("pool nodes=%d want 2", got)
	}

	// Simulate runtime stat — ensure reconcile preserves it.
	for _, nn := range p.Nodes() {
		if nn.ID == n1.ID {
			nn.totalServed.Store(42)
		}
	}

	// Add a third node, delete n2.
	n3, _ := s.Add("n3", "http://c.example", 3, true, "", "")
	_ = s.Delete(n2.ID)
	p.Reconcile(s.Snapshot())

	nodes := p.Nodes()
	if len(nodes) != 2 {
		t.Fatalf("after reconcile nodes=%d want 2", len(nodes))
	}
	foundN1, foundN3 := false, false
	for _, nn := range nodes {
		if nn.ID == n1.ID {
			foundN1 = true
			if nn.TotalServed() != 42 {
				t.Fatalf("runtime stat lost: got %d want 42", nn.TotalServed())
			}
		}
		if nn.ID == n3.ID {
			foundN3 = true
			if nn.Weight != 3 {
				t.Fatalf("n3 weight=%d want 3", nn.Weight)
			}
		}
	}
	if !foundN1 || !foundN3 {
		t.Fatalf("expected n1+n3 after reconcile, got %+v", nodes)
	}
}

func TestSettingsNormalize(t *testing.T) {
	s := Settings{Strategy: "bogus", LatencyWeight: 2.5, FailThreshold: 0, MaxRetries: 99}
	s.normalize()
	if s.Strategy != "hybrid" {
		t.Fatalf("Strategy=%q want hybrid", s.Strategy)
	}
	if s.LatencyWeight != 1 {
		t.Fatalf("LatencyWeight=%v want 1", s.LatencyWeight)
	}
	if s.FailThreshold < 1 {
		t.Fatalf("FailThreshold=%d", s.FailThreshold)
	}
	if s.MaxRetries > 5 {
		t.Fatalf("MaxRetries=%d clamped to 5", s.MaxRetries)
	}
}
