package sisihttp

import (
	stdjson "encoding/json"
	"strings"
	"testing"

	"lampac-go/internal/proxylink"
)

// TestInjectSisiPosterProxy verifies the list-payload poster rewrite:
//   - list[].picture and list[].bookmark.image become /proxyimg/{aes} URLs
//   - our own host and relative URLs pass through untouched
//   - video payloads (no "list") and non-JSON bodies are unchanged
func TestInjectSisiPosterProxy(t *testing.T) {
	pl, err := proxylink.New(proxylink.Options{
		EncryptAES: true,
		CacheDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("init proxylink: %v", err)
	}
	host := "http://127.0.0.1:9118"

	t.Run("wraps picture and bookmark.image", func(t *testing.T) {
		body := []byte(`{
			"list": [
				{"title":"a","picture":"https://cdn.example.com/p1.jpg","bookmark":{"site":"x","href":"/v/1","image":"https://cdn.example.com/p1.jpg"}},
				{"title":"b","picture":"https://cdn.example.com/p2.jpg"}
			],
			"total_pages": 3
		}`)
		out := injectSisiPosterProxy(body, pl, "sisi_test", "127.0.0.1", host)

		var raw map[string]any
		if err := stdjson.Unmarshal(out, &raw); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out)
		}
		list := raw["list"].([]any)
		first := list[0].(map[string]any)
		pic := first["picture"].(string)
		if !strings.HasPrefix(pic, host+"/proxyimg/") {
			t.Errorf("picture not wrapped: %s", pic)
		}
		bm := first["bookmark"].(map[string]any)
		if img := bm["image"].(string); !strings.HasPrefix(img, host+"/proxyimg/") {
			t.Errorf("bookmark.image not wrapped: %s", img)
		}
		second := list[1].(map[string]any)
		if p2 := second["picture"].(string); !strings.HasPrefix(p2, host+"/proxyimg/") {
			t.Errorf("second picture not wrapped: %s", p2)
		}
		if raw["total_pages"].(float64) != 3 {
			t.Errorf("total_pages mangled: %v", raw["total_pages"])
		}
	})

	t.Run("own-host and relative URLs untouched", func(t *testing.T) {
		body := []byte(`{"list":[
			{"picture":"` + host + `/img/sisi/torrent.png"},
			{"picture":"/local/p.jpg"}
		]}`)
		out := injectSisiPosterProxy(body, pl, "sisi_test", "127.0.0.1", host)
		var raw map[string]any
		_ = stdjson.Unmarshal(out, &raw)
		list := raw["list"].([]any)
		if p := list[0].(map[string]any)["picture"].(string); p != host+"/img/sisi/torrent.png" {
			t.Errorf("own-host URL rewritten: %s", p)
		}
		if p := list[1].(map[string]any)["picture"].(string); p != "/local/p.jpg" {
			t.Errorf("relative URL rewritten: %s", p)
		}
	})

	t.Run("video payload and junk pass through", func(t *testing.T) {
		video := []byte(`{"qualitys":{"720p":"https://cdn.example.com/v.m3u8"}}`)
		if out := injectSisiPosterProxy(video, pl, "sisi_test", "127.0.0.1", host); string(out) != string(video) {
			t.Errorf("video payload modified: %s", out)
		}
		junk := []byte(`not json at all`)
		if out := injectSisiPosterProxy(junk, pl, "sisi_test", "127.0.0.1", host); string(out) != string(junk) {
			t.Errorf("junk modified: %s", out)
		}
	})
}
