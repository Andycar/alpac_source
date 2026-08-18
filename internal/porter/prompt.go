package porter

import (
	"embed"
	"strings"
)

//go:embed examples/*.txt
var examplesFS embed.FS

// systemPrompt is the base system prompt for Go balancer generation.
const systemPrompt = `You are an expert Go developer porting C# media streaming balancers to Go standalone binaries.

TASK: Generate a complete, compilable Go program (package main) from the provided C# source code.

ARCHITECTURE:
- Single file: package main with main() function
- HTTP server on 127.0.0.1:{port} using net/http (no external deps)
- Flags: -port (int), -main-host (string), -host (string), optionally -token
- Handle ANY path: mux.HandleFunc("/", s.handle)
- Two modes: checksearch (fast probe) and index (full content)
- defaultHost must be set to the actual upstream site URL (e.g. "https://example.com"), extracted from the C# code
- Log every request: log.Printf("%s: %s %s", balancerName, r.Method, r.URL.String())

CHECKSEARCH — the Lampa client calls this first to check if a balancer has content:
- Triggered by ?checksearch=true or ?checksearch=1
- Returns JSON: {"rch": true/false} — true if content exists for the given title
- CRITICAL: Lampa sends title, original_title, id (TMDB), imdb_id. It does NOT always send kinopoisk_id!
- For DLE sites: search by title or original_title (whichever is not empty). Do NOT require kinopoisk_id.
- For embed/API sites that use kinopoisk_id: if kinopoisk_id is empty, try to search by title instead.
- Must actually probe the upstream API/site to verify content availability
- Fast timeout (8 seconds), cache results

INDEX (main handler) — called when user selects this balancer:
- Query params: title, original_title, kinopoisk_id, imdb_id, id, s (season), t (voice/translation), similar, rjson, serial, href, play
- BOTH rjson=true (JSON) AND rjson=false (HTML widget) output MUST be supported

CRITICAL: Lampa client protocol — you MUST follow this exactly:

1. "similar" parameter handling:
   - When the user opens a film, Lampa sends similar=false. This means "don't show me a list, auto-select the best match".
   - If similar=false and search returns results → use the FIRST result automatically, do NOT return type:"similar".
   - If similar=true (or not set) and search returns multiple results → return type:"similar" list so user can pick.
   - The "similar" list uses: {"method":"link","similar":true,"id":"...","url":"...","name":"...","year":...,"img":"..."}

2. method:"play" vs method:"call":
   - method:"play" → the "url" field contains a DIRECT stream URL (m3u8/mp4) that the player opens immediately.
   - method:"call" → the "url" field contains a URL that Lampa will GET first. The response from that GET should return a JSON with method:"play" and the actual stream URL.
   - RULE: Use method:"play" ONLY when url is a direct video stream (m3u8/mp4).
   - RULE: Use method:"call" when url points back to YOUR handler (e.g. /lite/name?t=...&play=true). Lampa will GET that URL, your handler returns {"method":"play","url":"https://cdn.example.com/video.m3u8"}.
   - NEVER use method:"play" with a URL that points back to your own handler path.

3. Movie flow (non-serial):
   - First call (no play param): return the movie entry with method:"call" pointing to self with play=true param.
   - Second call (play=true): fetch the actual stream URL from upstream, return {"method":"play","url":"<stream>","title":"..."}.
   - If the upstream returns a direct HLS/mp4 URL (like embed sites), you can return method:"play" directly on first call.

4. Serial flow:
   - s=-1 or s not set → return season list: {"type":"season","data":[{"method":"link","url":"?s=1&...","name":"1 сезон"},...]}.
   - s=N (specific season) → return episode list: {"type":"episode","data":[...]}.
   - Episodes: use method:"play" with direct stream URLs, or method:"call" if stream URL needs a second fetch.

5. HTML output (rjson=false) — REQUIRED, this is the DEFAULT mode:
   - Movie: <div class="videos__line"><div class="videos__item videos__movie selector" data-json='JSON'><div class="videos__item-imgbox videos__movie-imgbox"></div><div class="videos__item-title">TITLE</div></div></div>
   - Similar list: <div class="videos__line"><div class="videos__item videos__season selector" data-json='JSON'><div class="videos__season-title">TITLE</div></div>...</div>
   - Season list: same as similar but with season names
   - Episode list: <div class="videos__line"><div class="videos__item videos__movie selector" data-json='JSON'>...
   - The data-json attribute must be valid JSON with single quotes escaped as &#39;
   - Empty result: respond with empty string (not JSON)

6. JSON output (rjson=true):
   - Movie: {"type":"movie","data":[...]}
   - Similar: {"type":"similar","data":[...]}
   - Season: {"type":"season","data":[...]}
   - Episode: {"type":"episode","data":[...]}
   - Empty: {"type":"empty","data":[]}

DLE SITES (sites using DataLife Engine CMS):
- Search: POST or GET to /index.php?do=search&subaction=search&story={query}
- Search results HTML structure: <a class="sres-wrap" href="URL">...<h2>Title</h2>...<img src="poster">...</a>
  Also possible: <div class="short-story">...<a href="URL">...<img src="poster">...</a>...<h3>Title</h3>...
- Parse search results with regexp, extract title from <h2> or <h3>, poster from <img src="...">, URL from <a href="...">
- IMPORTANT: Some DLE sites have domain aliases (e.g. uafix.net → uaflix.net). Check if link URLs contain a different domain and rewrite them to use the configured host.
- Film pages usually contain <iframe src="..."> pointing to a video CDN (e.g. zetvideo.net, ashdi.vip, hdvb.co).
- Parse the iframe URL, fetch the CDN page, extract the stream URL (usually file:"URL.m3u8" or similar pattern).

RULES:
1. ONLY use Go stdlib — no external packages (no chi, no htmlquery, no zerolog)
2. ONLY use regexp for HTML parsing — no xml/html parsers
3. Include a simple in-memory cache (simpleCache with TTL)
4. Use io.LimitReader(resp.Body, 4<<20) for all HTTP reads
5. Set proper User-Agent header on all requests
6. Handle both rjson=true (JSON) and rjson=false (HTML widget) output — HTML is the DEFAULT
7. Return ONLY valid Go source code — no markdown, no explanations, no wrapping in backticks
8. The code must compile with "go build ." without errors
9. Port ALL logic from the C# code — search, movie extraction, serial/seasons/episodes if applicable
10. Use the exact same API endpoints and URL patterns as the C# source
11. Include respondEmpty helper that returns empty HTML for rjson=false and {"type":"empty","data":[]} for rjson=true
12. Include escapeAttr helper for safe JSON in HTML data-json attributes
13. Always log incoming requests with log.Printf
14. For sites that use iframe players: the movie handler should use method:"call" for the first request, then method:"play" with the actual stream URL when play=true is set`

// BuildPortPrompt builds the full message array for porting a C# file.
func BuildPortPrompt(csCode, csFileName, category string) []Message {
	example := SelectExample(category)

	messages := []Message{
		{Role: "system", Content: systemPrompt},
	}

	// Few-shot example.
	if example != "" {
		messages = append(messages, Message{
			Role:    "user",
			Content: "Here is an example of a correctly ported Go balancer for reference:\n\n" + example,
		})
		messages = append(messages, Message{
			Role:    "assistant",
			Content: "Understood. I will follow this pattern for the new balancer.",
		})
	}

	// User prompt with C# code.
	var sb strings.Builder
	sb.WriteString("Port the following C# balancer to a standalone Go binary.\n")
	sb.WriteString("Source file: ")
	sb.WriteString(csFileName)
	sb.WriteString("\nDetected category: ")
	sb.WriteString(category)
	sb.WriteString("\n\nC# SOURCE CODE:\n```csharp\n")
	sb.WriteString(csCode)
	sb.WriteString("\n```\n\n")
	sb.WriteString("Generate the complete Go source code. Output ONLY the Go code, nothing else.")

	messages = append(messages, Message{Role: "user", Content: sb.String()})

	return messages
}

// BuildFixPrompt builds messages for fixing compilation errors.
func BuildFixPrompt(goCode, buildErrors string) []Message {
	return []Message{
		{Role: "system", Content: "You are a Go developer. Fix the compilation errors in the code below. Return ONLY the complete fixed Go source code, no explanations."},
		{Role: "user", Content: "This Go code has compilation errors:\n\n```go\n" + goCode + "\n```\n\nBuild errors:\n```\n" + buildErrors + "\n```\n\nFix ALL errors and return the complete corrected code. Output ONLY Go code."},
	}
}

// BuildTestFixPrompt builds messages for fixing runtime/logic errors.
func BuildTestFixPrompt(goCode, testOutput, expected string) []Message {
	return []Message{
		{Role: "system", Content: "You are a Go developer. The code compiles but produces wrong results. Fix the logic. Return ONLY the complete fixed Go source code."},
		{Role: "user", Content: "This Go balancer compiles but checksearch returns wrong results.\n\nCurrent code:\n```go\n" + goCode + "\n```\n\nChecksearch output: " + testOutput + "\nExpected: " + expected + "\n\nFix the logic and return the complete corrected code. Output ONLY Go code."},
	}
}

// SelectExample returns a compact example based on the detected category.
func SelectExample(category string) string {
	var filename string
	switch category {
	case "api":
		filename = "examples/filmix_example.txt"
	case "dle":
		filename = "examples/dle_example.txt"
	default:
		filename = "examples/collaps_example.txt"
	}

	data, err := examplesFS.ReadFile(filename)
	if err != nil {
		return ""
	}
	return string(data)
}

// ExtractGoCode extracts Go source from LLM response, removing markdown wrappers.
func ExtractGoCode(raw string) string {
	raw = strings.TrimSpace(raw)

	if after, ok := strings.CutPrefix(raw, "```go"); ok {
		raw = after
		if idx := strings.LastIndex(raw, "```"); idx >= 0 {
			raw = raw[:idx]
		}
		raw = strings.TrimSpace(raw)
	} else if after, ok := strings.CutPrefix(raw, "```"); ok {
		raw = after
		if idx := strings.LastIndex(raw, "```"); idx >= 0 {
			raw = raw[:idx]
		}
		raw = strings.TrimSpace(raw)
	}

	if !strings.HasPrefix(raw, "package ") {
		if idx := strings.Index(raw, "package main"); idx >= 0 {
			raw = raw[idx:]
		}
	}

	return raw
}

// DetectCategory does a simple category detection from C# code.
func DetectCategory(csCode string) string {
	scores := map[string]int{"dle": 0, "api": 0, "embed": 0, "playerjs": 0}

	if strings.Contains(csCode, "do=search") || strings.Contains(csCode, "subaction=search") {
		scores["dle"] += 3
	}
	if strings.Contains(csCode, "sres-wrap") || strings.Contains(csCode, "short-story") {
		scores["dle"] += 2
	}
	if strings.Contains(csCode, "/api/") || strings.Contains(csCode, "/API/") {
		scores["api"] += 3
	}
	if strings.Contains(csCode, "token") || strings.Contains(csCode, "apikey") {
		scores["api"] += 1
	}
	if strings.Contains(csCode, "JsonSerializer.Deserialize") || strings.Contains(csCode, "JObject") {
		scores["api"] += 1
	}
	if strings.Contains(csCode, "/embed/kp/") || strings.Contains(csCode, "/embed/imdb/") {
		scores["embed"] += 3
	}
	if strings.Contains(csCode, "makePlayer") {
		scores["embed"] += 2
	}
	if strings.Contains(csCode, "file:'[") || strings.Contains(csCode, `file:"[`) {
		scores["playerjs"] += 3
	}

	best := "dle"
	bestScore := 0
	for cat, score := range scores {
		if score > bestScore {
			best = cat
			bestScore = score
		}
	}
	return best
}

// ExtractBalancerName extracts the balancer name from C# code.
func ExtractBalancerName(csCode string) string {
	for line := range strings.SplitSeq(csCode, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "class ") && strings.Contains(line, "BaseOnlineController") {
			_, after, _ := strings.Cut(line, "class ")
			rest := after
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				name := strings.ToLower(fields[0])
				name = strings.TrimSuffix(name, "controller")
				return name
			}
		}
	}

	for line := range strings.SplitSeq(csCode, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, `[Route("lite/`) {
			start := strings.Index(line, `"lite/`) + 6
			end := strings.Index(line[start:], `"`)
			if end > 0 {
				name := line[start : start+end]
				if idx := strings.Index(name, "/"); idx >= 0 {
					name = name[:idx]
				}
				return strings.ToLower(name)
			}
		}
	}

	return ""
}
