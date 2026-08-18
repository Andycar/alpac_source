package iptv

import (
	"strings"
	"testing"
	"time"
)

const testXMLTV = `<?xml version="1.0" encoding="UTF-8"?>
<tv generator-info-name="test">
  <channel id="ch1">
    <display-name>Channel One</display-name>
    <icon src="http://logo.com/ch1.png"/>
  </channel>
  <channel id="ch2">
    <display-name>ESPN</display-name>
  </channel>
  <programme start="20260308060000 +0000" stop="20260308070000 +0000" channel="ch1">
    <title>Morning News</title>
    <desc>Latest headlines</desc>
    <category>News</category>
  </programme>
  <programme start="20260308070000 +0000" stop="20260308080000 +0000" channel="ch1">
    <title>Weather Report</title>
  </programme>
  <programme start="20260308060000 +0000" stop="20260308090000 +0000" channel="ch2">
    <title>Live Football</title>
    <category>Sports</category>
    <icon src="http://img.com/football.png"/>
  </programme>
</tv>`

func TestParseXMLTV_Basic(t *testing.T) {
	var channels []EPGChannel
	var programs []EPGProgram

	err := ParseXMLTV(strings.NewReader(testXMLTV),
		func(ch EPGChannel) { channels = append(channels, ch) },
		func(prog EPGProgram) { programs = append(programs, prog) },
	)
	if err != nil {
		t.Fatalf("ParseXMLTV error: %v", err)
	}

	// Channels.
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	if channels[0].ID != "ch1" || channels[0].Name != "Channel One" {
		t.Errorf("ch1: id=%q name=%q", channels[0].ID, channels[0].Name)
	}
	if channels[0].Icon != "http://logo.com/ch1.png" {
		t.Errorf("ch1 icon=%q", channels[0].Icon)
	}

	// Programs.
	if len(programs) != 3 {
		t.Fatalf("expected 3 programs, got %d", len(programs))
	}

	p := programs[0]
	if p.Title != "Morning News" {
		t.Errorf("prog0 title=%q", p.Title)
	}
	if p.Description != "Latest headlines" {
		t.Errorf("prog0 desc=%q", p.Description)
	}
	if p.Category != "News" {
		t.Errorf("prog0 category=%q", p.Category)
	}
	if p.ChannelID != "ch1" {
		t.Errorf("prog0 channel=%q", p.ChannelID)
	}
	expectedStart := time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC)
	if !p.Start.Equal(expectedStart) {
		t.Errorf("prog0 start=%v, want %v", p.Start, expectedStart)
	}
	expectedStop := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)
	if !p.Stop.Equal(expectedStop) {
		t.Errorf("prog0 stop=%v, want %v", p.Stop, expectedStop)
	}

	// Program with icon.
	p = programs[2]
	if p.Icon != "http://img.com/football.png" {
		t.Errorf("prog2 icon=%q", p.Icon)
	}
}

func TestParseXMLTV_Empty(t *testing.T) {
	xmltv := `<?xml version="1.0"?><tv></tv>`
	var channels []EPGChannel
	var programs []EPGProgram

	err := ParseXMLTV(strings.NewReader(xmltv),
		func(ch EPGChannel) { channels = append(channels, ch) },
		func(prog EPGProgram) { programs = append(programs, prog) },
	)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(channels) != 0 || len(programs) != 0 {
		t.Errorf("expected empty, got %d channels %d programs", len(channels), len(programs))
	}
}

func TestParseXMLTVFiltered_SkipsRejectedProgrammeBody(t *testing.T) {
	xmltv := `<?xml version="1.0"?><tv>
  <programme start="20260308060000 +0000" stop="20260308070000 +0000" channel="keep">
    <title>Keep Me</title>
  </programme>
  <programme start="20260308060000 +0000" stop="20260308070000 +0000" channel="drop">
    <title>Drop Me</title>
    <desc>` + strings.Repeat("x", 2048) + `</desc>
  </programme>
</tv>`
	var programs []EPGProgram
	err := ParseXMLTVFiltered(strings.NewReader(xmltv), nil,
		func(prog EPGProgram) bool { return prog.ChannelID == "keep" },
		func(prog EPGProgram) { programs = append(programs, prog) },
	)
	if err != nil {
		t.Fatalf("ParseXMLTVFiltered error: %v", err)
	}
	if len(programs) != 1 || programs[0].Title != "Keep Me" {
		t.Fatalf("got programs=%+v", programs)
	}
}

func TestParseXMLTV_TruncatesHugeDescription(t *testing.T) {
	longDesc := strings.Repeat("аб", 600)
	xmltv := `<?xml version="1.0"?><tv>
  <programme start="20260308060000 +0000" stop="20260308070000 +0000" channel="ch1">
    <title>Title</title>
    <desc>` + longDesc + `</desc>
  </programme>
</tv>`
	var programs []EPGProgram
	err := ParseXMLTV(strings.NewReader(xmltv), nil, func(prog EPGProgram) {
		programs = append(programs, prog)
	})
	if err != nil {
		t.Fatalf("ParseXMLTV error: %v", err)
	}
	if len(programs) != 1 {
		t.Fatalf("expected 1 program, got %d", len(programs))
	}
	if len(programs[0].Description) > epgMaxDescriptionBytes {
		t.Fatalf("description len=%d, max=%d", len(programs[0].Description), epgMaxDescriptionBytes)
	}
	if !strings.HasPrefix(longDesc, programs[0].Description) {
		t.Fatalf("description is not a prefix of source")
	}
}

func TestParseXMLTVTime(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"20260308150000 +0300", time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)},
		{"20260308150000 +0000", time.Date(2026, 3, 8, 15, 0, 0, 0, time.UTC)},
		{"20260308150000", time.Date(2026, 3, 8, 15, 0, 0, 0, time.UTC)},
		{"202603081500 +0000", time.Date(2026, 3, 8, 15, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"invalid", time.Time{}},
	}
	for _, tc := range tests {
		got := parseXMLTVTime(tc.input)
		if !got.Equal(tc.want) {
			t.Errorf("parseXMLTVTime(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestEPGEngine_NowNext(t *testing.T) {
	engine := NewEPGEngine(nil, EPGConfig{})

	// Manually inject programs.
	now := time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC)
	engine.programs["ch1"] = []EPGProgram{
		{Start: time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC), Stop: time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), ChannelID: "ch1", Title: "Morning News"},
		{Start: time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), Stop: time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC), ChannelID: "ch1", Title: "Weather"},
		{Start: time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC), Stop: time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC), ChannelID: "ch1", Title: "Talk Show"},
	}

	result := engine.NowNext([]string{"ch1", "ch_unknown"}, now)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}

	// ch1: 06:30 is during "Morning News", next is "Weather".
	nn := result[0]
	if nn.Now == nil {
		t.Fatal("ch1 Now is nil")
	}
	if nn.Now.Title != "Morning News" {
		t.Errorf("ch1 Now title=%q", nn.Now.Title)
	}
	if nn.Next == nil || nn.Next.Title != "Weather" {
		t.Errorf("ch1 Next=%v", nn.Next)
	}

	// Unknown channel: both nil.
	nn = result[1]
	if nn.Now != nil || nn.Next != nil {
		t.Errorf("unknown channel should have nil now/next")
	}
}

func TestEPGEngine_Timeline(t *testing.T) {
	engine := NewEPGEngine(nil, EPGConfig{})

	engine.programs["ch1"] = []EPGProgram{
		{Start: time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC), Stop: time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), ChannelID: "ch1", Title: "P1"},
		{Start: time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), Stop: time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC), ChannelID: "ch1", Title: "P2"},
		{Start: time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC), Stop: time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC), ChannelID: "ch1", Title: "P3"},
		{Start: time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC), Stop: time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC), ChannelID: "ch1", Title: "P4"},
	}

	// Query 07:00 to 09:00 — should get P2 and P3.
	from := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC)
	result := engine.Timeline("ch1", from, to)
	if len(result) != 2 {
		t.Fatalf("expected 2 programs, got %d", len(result))
	}
	if result[0].Title != "P2" || result[1].Title != "P3" {
		t.Errorf("got titles: %q, %q", result[0].Title, result[1].Title)
	}

	// Query that overlaps P1 (P1 stops at 7:00, from=6:30 → P1.Stop > from).
	from2 := time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC)
	to2 := time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC)
	result2 := engine.Timeline("ch1", from2, to2)
	if len(result2) != 2 {
		t.Fatalf("expected 2 programs (P1+P2), got %d", len(result2))
	}
	if result2[0].Title != "P1" || result2[1].Title != "P2" {
		t.Errorf("got titles: %q, %q", result2[0].Title, result2[1].Title)
	}
}

func TestEPGEngine_Search(t *testing.T) {
	engine := NewEPGEngine(nil, EPGConfig{})

	future := time.Now().UTC().Add(1 * time.Hour)
	engine.programs["ch1"] = []EPGProgram{
		{Start: future, Stop: future.Add(1 * time.Hour), ChannelID: "ch1", Title: "Football Match"},
		{Start: future, Stop: future.Add(1 * time.Hour), ChannelID: "ch1", Title: "Tennis Final"},
	}
	engine.programs["ch2"] = []EPGProgram{
		{Start: future, Stop: future.Add(1 * time.Hour), ChannelID: "ch2", Title: "Football Highlights"},
	}

	results := engine.Search("football", 10)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}
