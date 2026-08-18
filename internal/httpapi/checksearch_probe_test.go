package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An availability probe carries no user token, so the group gate would resolve
// it to the DEFAULT group. When that group denies a source (Мир Кино is denied
// in "Для олдов", which is the default), the probe got `{}` and the source
// showed up permanently greyed out — even for users whose own group allows it.
// The list is already filtered per-caller before probing, so probes must skip
// the gate, exactly like capi drills and stream paths do.
func TestChecksearchProbeIsMarkedAndDetected(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "http://x/lite/mirkino?checksearch=true", nil)
	if isChecksearchProbe(plain) {
		t.Fatal("a plain client request must not look like an internal probe")
	}

	probe := plain.WithContext(checksearchProbeContext(context.Background()))
	if !isChecksearchProbe(probe) {
		t.Fatal("probe context marker not detected")
	}

	if isChecksearchProbe(nil) {
		t.Fatal("nil request must not be treated as a probe")
	}
}

// The marker must survive the timeout wrapper the prober puts around it.
func TestChecksearchProbeMarkerSurvivesDerivedContext(t *testing.T) {
	base := checksearchProbeContext(context.Background())
	derived, cancel := context.WithCancel(base)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "http://x/lite/mirkino", nil).WithContext(derived)
	if !isChecksearchProbe(req) {
		t.Fatal("marker lost through a derived context")
	}
}
