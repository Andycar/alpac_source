package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The update server hosts several products in one release stream; the client
// must always pin product=lampac-go and present the channel password so
// private channels work end-to-end.
func TestClientLatest_SendsProductAndToken(t *testing.T) {
	var gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(Release{TagName: "v0.9"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "s3cret", "admin")
	rel, err := c.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.9" {
		t.Fatalf("tag = %s", rel.TagName)
	}
	if gotQuery != "product=lampac-go&channel=admin" {
		t.Fatalf("query = %q, want product=lampac-go&channel=admin", gotQuery)
	}
	if gotAuth != "Bearer s3cret" {
		t.Fatalf("auth = %q, want Bearer s3cret", gotAuth)
	}
}
