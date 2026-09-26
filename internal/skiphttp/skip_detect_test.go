package skiphttp

import "testing"

func TestStreamURLAllowed(t *testing.T) {
	ok := []string{"https://tv.example.com/ts/stream/a.mkv?link=x&index=1&play", "http://93.184.216.34:8090/stream/a.mkv"}
	bad := []string{"file:///etc/passwd", "http://127.0.0.1:8090/x", "http://10.0.0.5/x", "http://localhost/x", "http://[::1]/x", "ftp://a/b", "http://169.254.169.254/latest"}
	for _, u := range ok {
		if !streamURLAllowed(u) {
			t.Errorf("должен пройти: %s", u)
		}
	}
	for _, u := range bad {
		if streamURLAllowed(u) {
			t.Errorf("должен отсеяться: %s", u)
		}
	}
}
