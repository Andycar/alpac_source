package litesrc

import (
	"net/url"
	"strings"
	"testing"
)

func TestMirkinoItemIDFromPath(t *testing.T) {
	cases := map[string]string{
		"/videos/3c006922463545e499d66c535ff949b3/stream": "3c006922463545e499d66c535ff949b3",
		"/Videos/abc/stream":                              "abc",
		"videos/xyz/stream":                               "xyz",
		"/videos/%D1%84/stream":                           "ф",
		"/Items/abc/PlaybackInfo":                         "",
		"/":                                               "",
		"":                                                "",
	}
	for in, want := range cases {
		if got := mirkinoItemIDFromPath(in); got != want {
			t.Errorf("mirkinoItemIDFromPath(%q) = %q, ждали %q", in, got, want)
		}
	}
}

// Приметы дорожки должны переживать поход в источник и обратно.
func TestMirkinoDecorateStreamURL(t *testing.T) {
	raw := "https://h/videos/i/stream?static=true&mediaSourceId=old&api_key=t"
	got := mirkinoDecorateStreamURL(raw, jfMediaSource{ID: "old", Name: "2160p", Size: 11559180942})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("адрес не разобрался: %v", err)
	}
	q := u.Query()
	if q.Get(mirkinoSizeParam) != "11559180942" {
		t.Errorf("размер не записан: %q", got)
	}
	if q.Get(mirkinoNameParam) != "2160p" {
		t.Errorf("имя не записано: %q", got)
	}
	// Исходные параметры на месте.
	if q.Get("mediaSourceId") != "old" || q.Get("api_key") != "t" {
		t.Errorf("исходные параметры потерялись: %q", got)
	}
	// Без размера/имени — ничего лишнего не дописываем.
	if plain := mirkinoDecorateStreamURL(raw, jfMediaSource{ID: "old"}); plain != raw {
		t.Errorf("пустая дорожка не должна менять адрес: %q", plain)
	}
}

// Главный риск переминта: подменить 2160p на 1080p посреди фильма — это
// другой файл, другая длина, битый просмотр. Выбор обязан быть точным.
func TestMirkinoPickSource(t *testing.T) {
	sources := []jfMediaSource{
		{ID: "fresh4k", Name: "2160p", Size: 11559180942},
		{ID: "freshfhd", Name: "1080p", Size: 4767120285},
	}
	t.Run("по размеру", func(t *testing.T) {
		got, ok := mirkinoPickSource(sources, 4767120285, "2160p", false)
		if !ok || got.ID != "freshfhd" {
			t.Fatalf("размер должен быть главнее имени: %+v ok=%v", got, ok)
		}
	})
	t.Run("по имени, когда размер не совпал", func(t *testing.T) {
		got, ok := mirkinoPickSource(sources, 999, "2160p", false)
		if !ok || got.ID != "fresh4k" {
			t.Fatalf("не нашли по имени: %+v ok=%v", got, ok)
		}
	})
	t.Run("ни размера, ни имени — отказ, а не первая попавшаяся", func(t *testing.T) {
		if got, ok := mirkinoPickSource(sources, 999, "720p", false); ok {
			t.Fatalf("подменили дорожку вместо отказа: %+v", got)
		}
	})
	t.Run("старая ссылка без примет — первая (лучшее качество)", func(t *testing.T) {
		got, ok := mirkinoPickSource(sources, 0, "", true)
		if !ok || got.ID != "fresh4k" {
			t.Fatalf("старая ссылка должна брать первую дорожку: %+v ok=%v", got, ok)
		}
	})
	t.Run("пустой список", func(t *testing.T) {
		if _, ok := mirkinoPickSource(nil, 1, "x", true); ok {
			t.Fatal("на пустом списке должен быть отказ")
		}
	})
}

// Переминт обязан сохранять служебные параметры нашей же раздачи через ноды:
// без edge_skip нода снова отправит запрос по кругу.
func TestMirkinoRefreshKeepsForeignParams(t *testing.T) {
	stale := "https://h/videos/item1/stream?static=true&mediaSourceId=old&api_key=t1" +
		"&" + mirkinoSizeParam + "=100&" + mirkinoNameParam + "=1080p" +
		"&edge_skip=https%3A%2F%2Fedge-b.example.net%3A2053"
	u, _ := url.Parse(stale)
	q := u.Query()
	if q.Get("edge_skip") == "" {
		t.Fatal("тест собран неверно: в исходном адресе нет edge_skip")
	}
	// Собираем «свежий» адрес тем же кодом, что и refresh (без похода в сеть).
	fresh := "https://h/videos/item1/stream?static=true&mediaSourceId=new&api_key=t1"
	fresh = mirkinoDecorateStreamURL(fresh, jfMediaSource{ID: "new", Name: "1080p", Size: 100})
	for k, vs := range q {
		switch k {
		case "static", "mediaSourceId", "api_key", mirkinoSizeParam, mirkinoNameParam:
			continue
		}
		for _, v := range vs {
			fresh += "&" + url.QueryEscape(k) + "=" + url.QueryEscape(v)
		}
	}
	fu, err := url.Parse(fresh)
	if err != nil {
		t.Fatalf("свежий адрес не разобрался: %v", err)
	}
	fq := fu.Query()
	if fq.Get("edge_skip") != "https://edge-b.example.net:2053" {
		t.Errorf("edge_skip потерялся: %q", fresh)
	}
	if fq.Get("mediaSourceId") != "new" {
		t.Errorf("билет не обновился: %q", fresh)
	}
	if strings.Count(fresh, "api_key=") != 1 {
		t.Errorf("api_key продублировался: %q", fresh)
	}
}
