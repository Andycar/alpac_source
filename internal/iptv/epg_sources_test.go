package iptv

import (
	"sort"
	"testing"
)

// capEPGSources must keep EVERY operator-pinned static-config URL and cap only the user-playlist
// x-tvg-url fan-in — so a flood of user feeds can't unbound EPG memory, yet the operator's own
// configured sources ALWAYS survive (the prod incident: a count-cap dropped the RU feed).
func TestCapEPGSources_KeepsAllStaticCapsFanIn(t *testing.T) {
	static := []string{"https://cfg-a/epg.xml", "https://cfg-b/epg.xml"}
	all := map[string]struct{}{
		"https://cfg-a/epg.xml": {}, "https://cfg-b/epg.xml": {},
	}
	for i := 0; i < 20; i++ { // a flood of user feeds
		all["https://user-"+string(rune('a'+i))+"/epg.xml"] = struct{}{}
	}

	got := capEPGSources(static, all)
	// all static kept + epgMaxFanIn of the fan-in
	if len(got) != len(static)+epgMaxFanIn {
		t.Fatalf("kept %d sources, want %d (static %d + fan-in cap %d)", len(got), len(static)+epgMaxFanIn, len(static), epgMaxFanIn)
	}
	for _, s := range static {
		if _, ok := got[s]; !ok {
			t.Errorf("static config source %q was dropped — must always be kept", s)
		}
	}
}

// Under the cap, everything is returned unchanged.
func TestCapEPGSources_UnderCapPassthrough(t *testing.T) {
	all := map[string]struct{}{"https://a/epg": {}, "https://b/epg": {}}
	got := capEPGSources(nil, all)
	if len(got) != 2 {
		t.Fatalf("under-cap set changed: got %d, want 2", len(got))
	}
}

// Operator-pinned sources are NEVER capped, even when many are configured — the operator owns the
// heap tradeoff. (Old behaviour capped static too, which could strand the matching feed.)
func TestCapEPGSources_AllStaticKeptEvenOverflow(t *testing.T) {
	var static []string
	all := map[string]struct{}{}
	for i := 0; i < epgMaxFanIn+3; i++ {
		u := "https://cfg-" + string(rune('a'+i)) + "/epg"
		static = append(static, u)
		all[u] = struct{}{}
	}
	all["https://user/epg"] = struct{}{}

	got := capEPGSources(static, all)
	for _, s := range static {
		if _, ok := got[s]; !ok {
			t.Errorf("pinned source %q dropped — operator-pinned sources must never be capped", s)
		}
	}
	// the single fan-in feed fits under the budget → kept too
	if _, ok := got["https://user/epg"]; !ok {
		t.Error("a single fan-in feed (under budget) should be kept alongside all pinned sources")
	}
}

// The fan-in cap is deterministic (sorted), not random map order — so which feeds survive a restart
// is reproducible (the bug: alphabetically-late RU feed dropped while early-country feeds stayed).
func TestCapEPGSources_FanInDeterministicSorted(t *testing.T) {
	all := map[string]struct{}{}
	for _, c := range []string{"zz", "aa", "mm", "bb", "yy", "cc", "nn", "dd"} {
		all["https://feed/"+c] = struct{}{}
	}
	got := capEPGSources(nil, all)
	if len(got) != epgMaxFanIn {
		t.Fatalf("kept %d fan-in feeds, want %d", len(got), epgMaxFanIn)
	}
	// must be the sorted-first epgMaxFanIn, every run
	want := []string{"https://feed/aa", "https://feed/bb", "https://feed/cc", "https://feed/dd", "https://feed/mm", "https://feed/nn"}
	gotList := make([]string, 0, len(got))
	for u := range got {
		gotList = append(gotList, u)
	}
	sort.Strings(gotList)
	for i, w := range want[:epgMaxFanIn] {
		if i >= len(gotList) || gotList[i] != w {
			t.Fatalf("fan-in selection not the sorted-first set: got %v, want first %d of sorted", gotList, epgMaxFanIn)
		}
	}
}
