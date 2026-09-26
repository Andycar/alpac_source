package litesrc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Разметку присылают разные люди, поэтому один кусок нередко приходит дважды с
// чуть разными границами. Без склейки плеер перемотал бы дважды подряд, и зритель
// увидел бы дёрганье вместо одного пропуска.
func TestMergeSponsorSegments(t *testing.T) {
	got := mergeSponsorSegments([]SponsorSegment{
		{Category: "sponsor", Start: 100, End: 130},
		{Category: "sponsor", Start: 10, End: 25},
		{Category: "selfpromo", Start: 20, End: 40}, // пересекается с предыдущим
		{Category: "sponsor", Start: 128, End: 150}, // и этот тоже
		{Category: "sponsor", Start: 200, End: 210},
	})
	want := []SponsorSegment{{Start: 10, End: 40}, {Start: 100, End: 150}, {Start: 200, End: 210}}
	if len(got) != len(want) {
		t.Fatalf("после склейки %d отрезков, ждали %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Start != want[i].Start || got[i].End != want[i].End {
			t.Fatalf("отрезок %d: [%v, %v], ждали [%v, %v]", i, got[i].Start, got[i].End, want[i].Start, want[i].End)
		}
	}
	// Пустой и одиночный вход не должны падать.
	if len(mergeSponsorSegments(nil)) != 0 {
		t.Fatal("пустой вход дал непустой выход")
	}
}

// sponsorTestServer подменяет API: отдаёт ответ по префиксу, как настоящий, —
// включая ЧУЖИЕ ролики, которые обязаны быть отброшены.
func sponsorTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchSponsorSegmentsFiltersProperly(t *testing.T) {
	// В ответе по префиксу: чужой ролик, заминусованная разметка, разметка от
	// другой версии ролика (длительность не та), перевёрнутый отрезок — и один
	// нормальный сегмент.
	body := `[
      {"videoID":"ЧУЖОЙ","segments":[{"category":"sponsor","segment":[0,999],"votes":5,"videoDuration":300}]},
      {"videoID":"НАШ","segments":[
        {"category":"sponsor","segment":[10,25],"votes":3,"videoDuration":300},
        {"category":"sponsor","segment":[40,60],"votes":-2,"videoDuration":300},
        {"category":"selfpromo","segment":[70,80],"votes":1,"videoDuration":600},
        {"category":"sponsor","segment":[90,90],"votes":1,"videoDuration":300}
      ]}
    ]`
	srv := sponsorTestServer(t, body)
	old := sponsorAPIBase
	sponsorAPIBase = srv.URL
	defer func() { sponsorAPIBase = old }()

	y := &YoutubeChecker{sbEnable: true}
	got, err := y.fetchSponsorSegments("НАШ", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("ждали один сегмент, получили %d: %+v", len(got), got)
	}
	if got[0].Start != 10 || got[0].End != 25 {
		t.Fatalf("не тот сегмент: %+v", got[0])
	}
}

// Разметка привязана к конкретной версии ролика. Если автор перезалил его другой
// длины, отрезки съезжают и перемотка приходится на нормальную сцену — применять
// такую разметку нельзя.
func TestSponsorSegmentsRejectedOnDurationMismatch(t *testing.T) {
	body := `[{"videoID":"v","segments":[{"category":"sponsor","segment":[10,25],"votes":3,"videoDuration":300}]}]`
	srv := sponsorTestServer(t, body)
	old := sponsorAPIBase
	sponsorAPIBase = srv.URL
	defer func() { sponsorAPIBase = old }()

	y := &YoutubeChecker{sbEnable: true}
	if got, _ := y.fetchSponsorSegments("v", 600); len(got) != 0 {
		t.Fatalf("применена разметка от другой версии ролика: %+v", got)
	}
	// В пределах допуска — принимаем: клиенты присылают длительность с разным округлением.
	if got, _ := y.fetchSponsorSegments("v", 301); len(got) != 1 {
		t.Fatalf("отброшена разметка при расхождении в секунду: %+v", got)
	}
	// Длительность неизвестна — проверку пропускаем, иначе потеряли бы всё.
	if got, _ := y.fetchSponsorSegments("v", 0); len(got) != 1 {
		t.Fatalf("при неизвестной длительности разметка потеряна: %+v", got)
	}
}

// Выключенный SponsorBlock не должен ни ходить в сеть, ни отдавать сегменты.
func TestSponsorBlockOffDoesNothing(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		fmt.Fprint(w, "[]")
	}))
	defer srv.Close()
	old := sponsorAPIBase
	sponsorAPIBase = srv.URL
	defer func() { sponsorAPIBase = old }()

	y := &YoutubeChecker{sbEnable: false}
	if got := y.SponsorBlockFor("v", 300); got != nil {
		t.Fatalf("выключенный SponsorBlock отдал %+v", got)
	}
	if called {
		t.Fatal("выключенный SponsorBlock сходил в сеть")
	}
}

func TestSponsorCategoriesDefault(t *testing.T) {
	if got := (&YoutubeChecker{}).sponsorCategories(); len(got) != len(sponsorDefaultCategories) {
		t.Fatalf("по умолчанию %v", got)
	}
	custom := []string{"sponsor"}
	if got := (&YoutubeChecker{sbCategories: custom}).sponsorCategories(); len(got) != 1 {
		t.Fatalf("из конфига %v", got)
	}
}
