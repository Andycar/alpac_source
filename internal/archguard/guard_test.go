// Package archguard holds architecture-invariant tests. It has no runtime code.
//
// The httpapi monolith was decomposed (2026-07) into cohesive sibling packages
// under internal/ (litesrc, capihttp, userdata, …). The strangler rule from
// internal/httpapi/ARCHITECTURE.md is: extract pure logic DOWNWARD; a sub-package
// must NEVER import internal/httpapi back. Go's compiler catches a *cycle*, but a
// one-directional wrong-way import (pkg → httpapi, with httpapi not importing pkg)
// compiles fine and silently re-couples the monolith. This test fails that.
package archguard

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// extractedPkgs are the internal/ packages carved out of httpapi that must stay
// independent of it. Add new extractions here.
var extractedPkgs = []string{
	// online-source front + its leaves
	"litesrc", "litehtml", "browsergate",
	// feature / client packages
	"kpcatalog", "watchparty", "userdata", "feedback",
	// earlier extractions (admin phase + Step-4 clusters)
	"adminhttp", "transcodesvc", "sisihttp", "iptvhttp", "dlnahttp",
	"opensubs", "subvtt", "collectionshttp", "skiphttp",
	"calendarhttp",
}

const forbidden = "lampac-go/internal/httpapi"

func TestExtractedPackagesDoNotImportHttpapi(t *testing.T) {
	// internal/ is the parent of this test's dir.
	base, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve internal/ dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, pkg := range extractedPkgs {
		dir := filepath.Join(base, pkg)
		if _, err := os.Stat(dir); err != nil {
			// package renamed/removed — skip rather than fail the suite.
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("%s: read dir: %v", pkg, err)
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
			if err != nil {
				t.Errorf("%s/%s: parse: %v", pkg, name, err)
				continue
			}
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if path == forbidden {
					t.Errorf("LAYERING VIOLATION: internal/%s/%s imports %s — "+
						"extracted packages must never depend on the monolith "+
						"(see ARCHITECTURE.md). Invert the dependency: inject the "+
						"needed value via a Deps field instead.", pkg, name, forbidden)
				}
			}
		}
	}
}
