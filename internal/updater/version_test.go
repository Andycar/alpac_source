package updater

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// Letter suffix = post-release hotfix → newer than the plain version.
		{"0.3b", "0.3", 1},
		{"0.3", "0.3b", -1},
		{"0.4a", "0.4", 1},
		{"0.4", "0.4a", -1},
		// Lexical order across multiple suffix releases.
		{"0.3a", "0.3b", -1},
		{"0.4c", "0.4a", 1},
		// Patch level beats suffix release on the same minor.
		{"0.3.1", "0.3", 1},
		{"0.3", "0.3.1", -1},
		{"0.3.1", "0.3a", 1},
		// Equality + v-prefix normalization.
		{"1.0.0", "1.0.0", 0},
		{"v1.2.3", "1.2.3", 0},
		// Minor bump beats suffix on the previous minor.
		{"0.4", "0.3.9", 1},
		{"0.3b", "0.4", -1},
		{"0.4a", "0.5", -1},
	}
	for _, c := range cases {
		got := CompareVersions(c.a, c.b)
		if got != c.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestInMaintenanceWindow(t *testing.T) {
	if !inMaintenanceWindow("") {
		t.Error("empty window should be always-allowed")
	}
	if !inMaintenanceWindow("00:00-23:59") {
		t.Error("full-day window should be always-allowed")
	}
}
