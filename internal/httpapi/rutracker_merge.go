package httpapi

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"regexp"
	"strings"

	"lampac-go/internal/rutracker"
)

// rutracker_merge.go — appending our rows to a jacred answer.
//
// Rules that shaped this code:
//   - A broken/blocked upstream must NOT lose our rows: if the body does not
//     parse (Cloudflare HTML, a 403 page, an empty body), we still answer with
//     valid JSON containing just the rutracker rows.
//   - Numbers are decoded with UseNumber so a 53 GB size survives the
//     round-trip byte-for-byte instead of going through float64.
//   - Dedup is cheap insurance: jacred structurally cannot carry rutracker
//     rows today, but a future upstream might, and a duplicated release in the
//     list looks like a bug to the user.

var reMergeBTIH = regexp.MustCompile(`(?i)urn:btih:([a-z0-9]+)`)

// mergeRutrackerV1 appends rows to the flat /api/v1.0/torrents array.
func mergeRutrackerV1(upstream []byte, rows []rutracker.Release, base string) []byte {
	if len(rows) == 0 {
		return upstream
	}
	var list []any
	if err := decodeJSONNumbers(upstream, &list); err != nil {
		list = nil
	}

	seen := dedupIndexV1(list)
	for _, r := range rows {
		row := rutrackerRowV1(r, base)
		if key := dedupKey(rutrackerLink(r, base), r.Title, r.SizeBytes); key != "" && seen[key] {
			continue
		}
		list = append(list, row)
	}
	out, err := json.Marshal(list)
	if err != nil {
		return upstream
	}
	return out
}

// mergeRutrackerV2 appends rows to the Jackett-compatible {"Results":[…]} root.
func mergeRutrackerV2(upstream []byte, rows []rutracker.Release, base string) []byte {
	if len(rows) == 0 {
		return upstream
	}
	root := map[string]any{}
	if err := decodeJSONNumbers(upstream, &root); err != nil {
		root = map[string]any{}
	}

	// The key is "Results" (JacRed/ApiController.cs:126); accept any casing
	// from a third-party upstream rather than silently dropping its rows.
	resultsKey := "Results"
	var results []any
	for k, v := range root {
		if !strings.EqualFold(k, "Results") {
			continue
		}
		resultsKey = k
		if arr, ok := v.([]any); ok {
			results = arr
		}
		break
	}

	seen := dedupIndexV2(results)
	for _, r := range rows {
		if key := dedupKey(rutrackerLink(r, base), r.Title, r.SizeBytes); key != "" && seen[key] {
			continue
		}
		results = append(results, rutrackerRowV2(r, base))
	}
	root[resultsKey] = results
	out, err := json.Marshal(root)
	if err != nil {
		return upstream
	}
	return out
}

// decodeJSONNumbers unmarshals with UseNumber so integers survive re-encoding.
func decodeJSONNumbers(data []byte, dst any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("empty body")
	}
	dec := stdjson.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	return dec.Decode(dst)
}

func dedupIndexV1(list []any) map[string]bool {
	seen := make(map[string]bool, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if key := dedupKey(jsonStr(m["magnet"]), jsonStr(m["title"]), jsonInt(m["size"])); key != "" {
			seen[key] = true
		}
	}
	return seen
}

func dedupIndexV2(list []any) map[string]bool {
	seen := make(map[string]bool, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		magnet := jsonStr(m["MagnetUri"])
		if magnet == "" {
			magnet = jsonStr(m["Link"])
		}
		if key := dedupKey(magnet, jsonStr(m["Title"]), jsonInt(m["Size"])); key != "" {
			seen[key] = true
		}
	}
	return seen
}

// dedupKey prefers the info-hash and falls back to title+size, which is what
// catches the same release re-published on two trackers.
func dedupKey(magnet, title string, size int64) string {
	if m := reMergeBTIH.FindStringSubmatch(magnet); len(m) > 1 {
		return "btih:" + strings.ToLower(m[1])
	}
	title = strings.Join(strings.Fields(strings.ToLower(title)), " ")
	if title == "" {
		return ""
	}
	return fmt.Sprintf("t:%s|%d", title, size)
}

func jsonStr(v any) string {
	s, _ := v.(string)
	return s
}

func jsonInt(v any) int64 {
	switch n := v.(type) {
	case stdjson.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return i
	case float64:
		return int64(n)
	case int64:
		return n
	}
	return 0
}
