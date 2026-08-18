package httpapi

import (
	"strings"
	"testing"
)

func TestPatchTOMLKey_NewSection(t *testing.T) {
	src := "[server]\naddr = \":8888\"\n"
	out := patchTOMLKey(src, "cluster", "api_key", "abc123")
	if !strings.Contains(out, "[cluster]") {
		t.Fatalf("section not appended:\n%s", out)
	}
	if !strings.Contains(out, `api_key = "abc123"`) {
		t.Fatalf("key not written:\n%s", out)
	}
}

func TestPatchTOMLKey_ExistingSectionEmptyValue(t *testing.T) {
	src := `[cluster]
enable = true
api_key = ""
mode = "primary"
`
	out := patchTOMLKey(src, "cluster", "api_key", "xyz")
	if !strings.Contains(out, `api_key = "xyz"`) {
		t.Fatalf("empty value not replaced:\n%s", out)
	}
	// Original key once = once after replace.
	if strings.Count(out, "api_key") != 1 {
		t.Fatalf("api_key appears %d times, want 1:\n%s", strings.Count(out, "api_key"), out)
	}
}

func TestPatchTOMLKey_ExistingNonEmptyLeaveAlone(t *testing.T) {
	src := `[cluster]
api_key = "existing"
`
	out := patchTOMLKey(src, "cluster", "api_key", "should-not-be-used")
	if !strings.Contains(out, `api_key = "existing"`) {
		t.Fatalf("non-empty was overwritten:\n%s", out)
	}
}

func TestPatchTOMLKey_KeyMissingInSection(t *testing.T) {
	src := `[cluster]
mode = "primary"
`
	out := patchTOMLKey(src, "cluster", "api_key", "new-key")
	if !strings.Contains(out, `api_key = "new-key"`) {
		t.Fatalf("missing key not inserted:\n%s", out)
	}
	if !strings.Contains(out, `mode = "primary"`) {
		t.Fatalf("original key gone:\n%s", out)
	}
}

func TestPatchTOMLKey_PreservesComments(t *testing.T) {
	src := `# top comment
[cluster]
# enable cluster mode
enable = true

[other]
foo = 1
`
	out := patchTOMLKey(src, "cluster", "api_key", "abc")
	if !strings.Contains(out, "# enable cluster mode") {
		t.Fatalf("comment lost:\n%s", out)
	}
	if !strings.Contains(out, "[other]") {
		t.Fatalf("subsequent section lost:\n%s", out)
	}
}

func TestReplaceClusterSecretValue_AlwaysReplaces(t *testing.T) {
	src := `[cluster]
api_key = "old"
`
	out := replaceClusterSecretValue(src, "cluster", "api_key", "new-value")
	if !strings.Contains(out, `api_key = "new-value"`) {
		t.Fatalf("force-replace failed:\n%s", out)
	}
	if strings.Contains(out, `"old"`) {
		t.Fatalf("old value still present:\n%s", out)
	}
}

func TestPatchClusterSecretsTOML_BothMissing(t *testing.T) {
	src := `[server]
addr = ":8888"
`
	out, err := patchClusterSecretsTOML(src, "API", "SECRET")
	if err != nil {
		t.Fatalf("patch error: %v", err)
	}
	if !strings.Contains(out, `[cluster]`) || !strings.Contains(out, `api_key = "API"`) {
		t.Fatalf("cluster section missing:\n%s", out)
	}
	if !strings.Contains(out, `[proxy_link]`) || !strings.Contains(out, `shared_secret = "SECRET"`) {
		t.Fatalf("proxy_link section missing:\n%s", out)
	}
}

func TestBuildPeerConfigSnippet(t *testing.T) {
	snippet := buildPeerConfigSnippet("KEY", "SECRET")
	for _, want := range []string{"[cluster]", `mode = "node"`, `api_key = "KEY"`, "[proxy_link]", `shared_secret = "SECRET"`} {
		if !strings.Contains(snippet, want) {
			t.Errorf("snippet missing %q:\n%s", want, snippet)
		}
	}
}
