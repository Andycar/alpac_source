package proxyapi

import "strings"
import "testing"

// TestAbsolutizeMPDPaths: относительные пути сегментов становятся абсолютными
// на вещателя. Прод-случай — Первый канал: живой DASH без <BaseURL>, пути вида
// "../../../dash-live2/…$Number%09d$.mp4"; без правки плеер искал бы их на
// нашем /proxy/<hash>.
func TestAbsolutizeMPDPaths(t *testing.T) {
	manifest := "https://edge1.1internet.tv/dash-live2/streams/1tv/1tvdash.mpd"
	in := `<SegmentTemplate media="../../../dash-live2/streams/1tv/1tvdash1-TFrag-hd-5-X_$Number%09d$.mp4" initialization="1tvdash1-TFrag-hd-5-Xinit.mp4"/>`
	out := absolutizeMPDPaths(in, manifest)

	wantMedia := `media="https://edge1.1internet.tv/dash-live2/streams/1tv/1tvdash1-TFrag-hd-5-X_$Number%09d$.mp4"`
	if !strings.Contains(out, wantMedia) {
		t.Fatalf("media не абсолютизирован:\n%s", out)
	}
	// Плейсхолдер обязан уцелеть байт в байт: url.Parse съел бы %09 как таб.
	if !strings.Contains(out, `$Number%09d$`) {
		t.Fatalf("шаблон $Number%%09d$ повреждён:\n%s", out)
	}
	wantInit := `initialization="https://edge1.1internet.tv/dash-live2/streams/1tv/1tvdash1-TFrag-hd-5-Xinit.mp4"`
	if !strings.Contains(out, wantInit) {
		t.Fatalf("initialization не абсолютизирован:\n%s", out)
	}
}

// Абсолютные и протокол-относительные ссылки не трогаем.
func TestAbsolutizeMPDPathsLeavesAbsolute(t *testing.T) {
	manifest := "https://cdn.example/a/b/live.mpd"
	in := `<S media="https://other.cdn/x/$Number$.m4s" initialization="//proto.rel/i.mp4" sourceURL="http://x/y.mp4"/>`
	if got := absolutizeMPDPaths(in, manifest); got != in {
		t.Fatalf("абсолютные ссылки не должны меняться:\n%s", got)
	}
}

func TestResolveRelativeAndDir(t *testing.T) {
	dir := urlDir("https://h.tv/a/b/c/live.mpd?x=1")
	if dir != "https://h.tv/a/b/c/" {
		t.Fatalf("urlDir = %q", dir)
	}
	cases := map[string]string{
		"seg.mp4":          "https://h.tv/a/b/c/seg.mp4",
		"../seg.mp4":       "https://h.tv/a/b/seg.mp4",
		"../../../s/x.mp4": "https://h.tv/s/x.mp4",
		"./s/x.mp4":        "https://h.tv/a/b/c/s/x.mp4",
		"/root/x.mp4":      "https://h.tv/root/x.mp4",
	}
	for ref, want := range cases {
		if got := resolveRelative(dir, ref); got != want {
			t.Errorf("resolveRelative(%q) = %q, want %q", ref, got, want)
		}
	}
	// Выход выше корня не должен уводить за пределы хоста.
	if got := resolveRelative("https://h.tv/a/", "../../../../x.mp4"); got != "https://h.tv/x.mp4" {
		t.Errorf("перебор ../ = %q", got)
	}
}
