package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Ключ принимается из обоих заголовков: ноды шлют X-Cluster-Key, а
// updater.DownloadAsset умеет только Bearer.
func TestClusterUpdateAuthorizedBothHeaders(t *testing.T) {
	const key = "s3cr3t-cluster-key"

	mk := func(set func(*http.Request)) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/cluster/binary", nil)
		set(r)
		return r
	}
	if !clusterUpdateAuthorized(mk(func(r *http.Request) { r.Header.Set("X-Cluster-Key", key) }), key) {
		t.Error("X-Cluster-Key должен приниматься")
	}
	if !clusterUpdateAuthorized(mk(func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }), key) {
		t.Error("Bearer должен приниматься")
	}
	if clusterUpdateAuthorized(mk(func(r *http.Request) { r.Header.Set("X-Cluster-Key", "wrong") }), key) {
		t.Error("чужой ключ принят")
	}
	if clusterUpdateAuthorized(mk(func(r *http.Request) {}), key) {
		t.Error("запрос без ключа принят")
	}
}

// Пустой ожидаемый ключ не должен открывать эндпоинты всем подряд.
func TestClusterUpdateAuthorizedRejectsEmptyExpected(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/cluster/release", nil)
	r.Header.Set("X-Cluster-Key", "")
	if clusterUpdateAuthorized(r, "") {
		t.Fatal("при незаданном ключе доступ должен быть закрыт")
	}
}

// Отпечаток собственной сборки должен считаться и быть стабильным.
func TestSelfBuildIsStable(t *testing.T) {
	a, err := selfBuild()
	if err != nil {
		t.Skipf("не удалось прочитать собственный бинарник: %v", err)
	}
	if len(a.SHA256) != 64 {
		t.Fatalf("ожидался sha256 из 64 символов, получено %q", a.SHA256)
	}
	if a.Size <= 0 {
		t.Fatalf("размер сборки %d", a.Size)
	}
	b, _ := selfBuild()
	if a.SHA256 != b.SHA256 {
		t.Fatal("повторный вызов дал другой отпечаток")
	}
	if selfBuildShort() != a.SHA256[:12] {
		t.Fatal("короткий отпечаток не совпадает с полным")
	}
}
