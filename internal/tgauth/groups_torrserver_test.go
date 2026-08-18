package tgauth

import "testing"

func TestTorrServerAllowed(t *testing.T) {
	cases := []struct {
		name    string
		servers map[string]bool
		id      string
		want    bool
	}{
		{"nil allows all", nil, "any", true},
		{"empty allows all", map[string]bool{}, "any", true},
		{"selected is allowed", map[string]bool{"a": true, "b": true}, "a", true},
		{"unselected is denied", map[string]bool{"a": true, "b": true}, "c", false},
		{"explicit false is denied", map[string]bool{"a": false}, "a", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := UserGroup{TorrServers: c.servers}
			if got := g.TorrServerAllowed(c.id); got != c.want {
				t.Fatalf("TorrServerAllowed(%q) with %v = %v, want %v", c.id, c.servers, got, c.want)
			}
		})
	}
}
