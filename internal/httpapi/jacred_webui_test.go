package httpapi

import (
	"strings"
	"testing"
)

func TestRewriteJacredHTML(t *testing.T) {
	in := `<a href="/">home</a>
<a href="/stats">stats</a>
<a href="/settings">settings</a>
<link href="./css/styles.css">
<script src="/js/app.js"></script>
<a href="//cdn.example.com/x.js">proto-relative</a>
<a href="https://github.com/jacred-fdb/jacred">ext</a>`

	got := string(rewriteJacredHTML([]byte(in)))

	for _, want := range []string{
		`href="/jacred/"`,
		`href="/jacred/stats"`,
		`href="/jacred/settings"`,
		`href="./css/styles.css"`, // relative untouched
		`src="/jacred/js/app.js"`,
		`href="//cdn.example.com/x.js"`, // protocol-relative untouched
		`href="https://github.com/jacred-fdb/jacred"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in rewritten HTML:\n%s", want, got)
		}
	}
	// Regression: root link must not be double-prefixed (order of the two
	// rewrite passes matters).
	if strings.Contains(got, "/jacred/jacred") {
		t.Fatalf("double prefix in rewritten HTML:\n%s", got)
	}
	// Idempotency safety: a second pass must not stack prefixes either.
	twice := string(rewriteJacredHTML([]byte(got)))
	if strings.Contains(twice, "/jacred/jacred") {
		t.Fatalf("double prefix after second rewrite pass:\n%s", twice)
	}
}
