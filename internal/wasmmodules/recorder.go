package wasmmodules

import (
	"bytes"
	"net/http"
)

// recorder is a tiny in-memory ResponseWriter for capturing upstream balancer
// output without an httptest server. We need it in the middleware path; the
// stdlib's httptest is fine, but importing it from non-test code feels wrong.
type recorder struct {
	header http.Header
	Body   *bytes.Buffer
	code   int
}

func newRecorder() *recorder {
	return &recorder{header: http.Header{}, Body: new(bytes.Buffer), code: 200}
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.Body.Write(b) }
func (r *recorder) WriteHeader(code int)        { r.code = code }
