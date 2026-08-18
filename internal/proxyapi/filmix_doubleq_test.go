package proxyapi

import "testing"

func TestFixFilmixDoubleQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://nl03.werkecdn.me/hls/hd_30_nl03/Teen.Spirit.2019.MVO.SVStudia.WEBDL.1080p_1080.mp4?vs3-origin/index.m3u8?hash=H1.H2",
			"https://nl03.werkecdn.me/hls/hd_30_nl03/Teen.Spirit.2019.MVO.SVStudia.WEBDL.1080p_1080.mp4/index.m3u8?vs3-origin&hash=H1.H2",
		},
		{ // clean path-style index.m3u8 — unchanged
			"https://nl06.cdnsqu.com/hls/lucifer/s01e05_1080.mp4/index.m3u8?hash=XYZ",
			"https://nl06.cdnsqu.com/hls/lucifer/s01e05_1080.mp4/index.m3u8?hash=XYZ",
		},
		{ // segment — unchanged
			"https://nl06.cdnsqu.com/hls/x/s01e01_1080.mp4/seg-54-v1-a1.ts?hash=AAA",
			"https://nl06.cdnsqu.com/hls/x/s01e01_1080.mp4/seg-54-v1-a1.ts?hash=AAA",
		},
	}
	for _, c := range cases {
		if got := fixFilmixDoubleQuery(c.in); got != c.want {
			t.Fatalf("in=%s\n got=%s\nwant=%s", c.in, got, c.want)
		}
		if got := fixFilmixDoubleQuery(c.want); got != c.want {
			t.Fatalf("not idempotent: %s -> %s", c.want, got)
		}
	}
}

func TestApplyPreFetchRewriteFilmix(t *testing.T) {
	in := "https://nl03.werkecdn.me/hls/hd_30_nl03/Teen.mp4?vs3-origin/index.m3u8?hash=H"
	want := "https://nl03.werkecdn.me/hls/hd_30_nl03/Teen.mp4/index.m3u8?vs3-origin&hash=H"
	if got := applyPreFetchRewrite(in, linkMeta{plugin: "filmix"}); got != want {
		t.Fatalf("filmix prefetch: got=%s want=%s", got, want)
	}
	// non-filmix plugin → untouched
	if got := applyPreFetchRewrite(in, linkMeta{plugin: "kodik"}); got != in {
		t.Fatalf("kodik must be untouched: %s", got)
	}
}
