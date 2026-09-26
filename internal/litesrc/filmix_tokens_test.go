package litesrc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Профиль по токену решает права: мёртвый токен FI (22.09.2026) резался бы до 720p и прятал
// HEVC, а не отдавал заглушку под видом 4K.
func TestFilmixTokenHealthGatesRights(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v2/user_profile") {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("user_dev_token") {
		case "alive":
			fmt.Fprint(w, `{"user_data":{"login":"me","is_pro":false,"is_pro_plus":true,"pro_date":"2099-01-01"}}`)
		case "expired":
			fmt.Fprint(w, `{"user_data":{"login":"me","is_pro":1,"is_pro_plus":0,"pro_date":"2020-01-01"}}`)
		case "flaky":
			w.WriteHeader(http.StatusBadGateway)
		default: // мёртвый: профиль пуст
			fmt.Fprint(w, `{"user_data":{"login":null,"is_pro":null,"pro_date":null}}`)
		}
	}))
	defer srv.Close()

	filmixTokenHealthMu.Lock()
	filmixTokenHealth = map[string]filmixTokenState{}
	filmixTokenHealthMu.Unlock()
	f := &filmixChecker{client: srv.Client(), hosts: []string{srv.URL}, pro: true,
		tokens: []string{"dead", "alive", "expired", "flaky"}}
	f.checkTokens(context.Background())
	// Второй чекер того же процесса (capi) видит те же вердикты без своей пробы.
	if twin := (&filmixChecker{pro: true, tokens: f.tokens}); twin.tokenUsable("dead") || !twin.tokenUsable("alive") {
		t.Fatal("здоровье токенов должно быть общим для всех чекеров процесса")
	}

	if !f.tokenUsable("alive") || f.tokenUsable("dead") || f.tokenUsable("expired") {
		t.Fatalf("пригодность: alive=%v dead=%v expired=%v", f.tokenUsable("alive"), f.tokenUsable("dead"), f.tokenUsable("expired"))
	}
	if !f.tokenUsable("flaky") {
		t.Fatal("5xx зеркала — не вердикт: непроверенный токен остаётся пригодным")
	}
	if !f.legacyRights() {
		t.Fatal("есть живой PRO — права у контура есть")
	}
	for i := 0; i < 50; i++ {
		if tok := f.pickToken(); tok == "dead" || tok == "expired" {
			t.Fatalf("pickToken выбрал бесправный токен %q", tok)
		}
	}
	if f.allowQuality(2160, "dead") || f.allowQuality(1080, "dead") || !f.allowQuality(720, "dead") {
		t.Fatal("мёртвый токен: ≤720p настоящие, выше — заглушка")
	}
	if !f.allowQuality(2160, "alive") || !f.allowQuality(2160, "kit-unknown") {
		t.Fatal("живой и непроверенный (kit) токены дают полное качество")
	}
	if f.filmixHideUnplayable("HEVC 4K AC3", "https://x/s/h/hevc/a_[2160].mp4") {
		t.Fatal("пока есть живой PRO, HEVC-строки видны")
	}

	snap := FilmixTokens()
	if len(snap) != 3 {
		t.Fatalf("срез: ждали 3 проверенных токена, есть %d: %+v", len(snap), snap)
	}
	for _, s := range snap {
		if len(s.Token) != 8 || strings.Contains(s.Token, "alive") {
			t.Fatalf("в срез утёк сам токен: %+v", s)
		}
	}

	// Все токены мёртвы — контур без прав: HEVC прячется, качество режется, но токен выдаётся.
	g := &filmixChecker{client: srv.Client(), hosts: []string{srv.URL}, pro: true, tokens: []string{"dead", "expired"}}
	g.checkTokens(context.Background())
	if g.legacyRights() {
		t.Fatal("без живых PRO прав нет")
	}
	if !g.filmixHideUnplayable("Дубляж [HDR10+, 4K]", "https://x/s/h/hdr/a_[2160].mp4") {
		t.Fatal("без прав HEVC/HDR-строки должны прятаться")
	}
	if tok := g.pickToken(); tok == "" {
		t.Fatal("мёртвый токен всё же лучше пустого: ≤720p по нему играет")
	}
	if g.allowQuality(1080, g.pickToken()) {
		t.Fatal("без прав качество режется до 720p")
	}
}

func TestFilmixProbeTokenExpiry(t *testing.T) {
	yesterday := time.Now().Add(-48 * time.Hour).Format("2006-01-02")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"user_data":{"login":"me","is_pro_plus":"1","pro_date":"%s"}}`, yesterday)
	}))
	defer srv.Close()
	f := &filmixChecker{client: srv.Client(), hosts: []string{srv.URL}}
	st, ok := f.probeToken(context.Background(), "t")
	if !ok || !st.Alive || st.Pro {
		t.Fatalf("истёкшая PRO+ должна быть живой, но без прав: ok=%v %+v", ok, st)
	}
	if st.Account == "" || st.ProUntil.IsZero() {
		t.Fatalf("учётка и срок не разобраны: %+v", st)
	}
}
