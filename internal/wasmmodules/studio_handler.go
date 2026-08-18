package wasmmodules

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// StudioHTTP returns the admin-panel JSON API for the WASM Studio:
//
//	POST /admin/api/wasm-studio/build    body: BuildRequest    → BuildResult
//	POST /admin/api/wasm-studio/install  body: {"id": "echo"}  → {"ok": true}
//	GET  /admin/api/wasm-studio/template?lang=tinygo|rust|as   → {"manifest","source"}
//
// The actual auth wrapper around these is the responsibility of the admin
// panel router — Studio doesn't open new attack surface beyond what jsmodules
// already exposes.
func (s *Studio) StudioHTTP() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/admin/api/wasm-studio/")
		switch path {
		case "build":
			s.handleBuild(w, r)
		case "install":
			s.handleInstall(w, r)
		case "template":
			s.handleTemplate(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *Studio) handleBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req BuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStudioErr(w, err, http.StatusBadRequest)
		return
	}
	res, err := s.Build(r.Context(), req)
	if err != nil {
		writeStudioErr(w, err, http.StatusBadRequest)
		return
	}
	writeStudioJSON(w, res)
}

func (s *Studio) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeStudioErr(w, err, http.StatusBadRequest)
		return
	}
	if body.ID == "" {
		writeStudioErr(w, errors.New("id required"), http.StatusBadRequest)
		return
	}
	if err := s.Install(body.ID); err != nil {
		writeStudioErr(w, err, http.StatusBadRequest)
		return
	}
	writeStudioJSON(w, map[string]any{"ok": true})
}

func (s *Studio) handleTemplate(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	tpl := studioTemplate(lang)
	writeStudioJSON(w, tpl)
}

type studioTemplateBundle struct {
	Manifest string `json:"manifest"`
	Source   string `json:"source"`
	Language string `json:"language"`
}

// studioTemplate returns boilerplate sources the Monaco editor pre-populates
// when the user starts a new plugin.
func studioTemplate(lang string) studioTemplateBundle {
	switch lang {
	case "rust":
		return studioTemplateBundle{
			Language: "rust",
			Manifest: `{
  "id": "my_plugin",
  "name": "My Plugin",
  "version": "0.1.0",
  "target": "server",
  "language": "rust",
  "abi_version": 1,
  "permissions": ["http:*"]
}`,
			Source: `use lampac_sdk::{handle_export, Invocation, Response};
use serde::Serialize;

handle_export!(handle_impl);

#[derive(Serialize)]
struct Item { name: String }

#[derive(Serialize)]
struct Movie { #[serde(rename = "type")] kind: &'static str, data: Vec<Item> }

fn handle_impl(_inv: Invocation) -> Response {
    lampac_sdk::info("hello from rust");
    Response::json(Movie {
        kind: "movie",
        data: vec![ Item { name: "Hello from Rust".into() } ],
    })
}
`,
		}
	case "as":
		return studioTemplateBundle{
			Language: "as",
			Manifest: `{
  "id": "my_plugin",
  "name": "My Plugin (AssemblyScript)",
  "version": "0.1.0",
  "target": "server",
  "language": "as",
  "abi_version": 1
}`,
			Source: `// Minimal AssemblyScript stub. Compiled with asc; you'll want to wire the
// host imports yourself — see wasm_sdk/tinygo/lampac.go for the ABI.
export function alloc(size: i32): i32 {
    return changetype<i32>(__alloc(size));
}

export function abi_version(): i32 { return 1; }

export function handle(ptr: i32, len: i32): i64 {
    return 0;
}
`,
		}
	default:
		return studioTemplateBundle{
			Language: "tinygo",
			Manifest: `{
  "id": "my_plugin",
  "name": "My Plugin",
  "version": "0.1.0",
  "target": "server",
  "language": "tinygo",
  "abi_version": 1,
  "permissions": ["http:*"]
}`,
			Source: `package main

import (
    "encoding/json"
    "lampac.cc/sdk/lampac"
)

func main() {}

type item struct{ Name string ` + "`json:\"name\"`" + ` }
type movie struct {
    Type string ` + "`json:\"type\"`" + `
    Data []item ` + "`json:\"data\"`" + `
}

//export handle
func handle(ptr, length uint32) uint64 {
    inv := lampac.ReadInvocation(ptr, length)
    lampac.Info("hello from tinygo, path=" + inv.Path)
    body, _ := json.Marshal(movie{
        Type: "movie",
        Data: []item{{Name: "Hello from TinyGo"}},
    })
    return lampac.WriteResponse(body)
}
`,
		}
	}
}

func writeStudioJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	body, _ := json.Marshal(v)
	_, _ = w.Write(body)
}

func writeStudioErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	body, _ := json.Marshal(map[string]string{"error": err.Error()})
	_, _ = w.Write(body)
}
