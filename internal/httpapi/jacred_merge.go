package httpapi

import "strings"

// jacred_merge.go — folding a SECOND parser's answer into the primary one.
//
// Both upstreams speak the same two formats the proxy already serves:
//   v1: flat JSON array            (/api/v1.0/torrents)
//   v2: {"Results":[…]} root       (/api/v2.0/indexers/…/results)
//
// The rules mirror rutracker_merge.go: a broken primary must not lose the
// secondary's rows (and vice versa), numbers survive via UseNumber, dedup is
// info-hash first with a title+size fallback — the same release listed by both
// parsers must show once, keeping the primary's row (it is the instance the
// admin trusts more, and its /parse links resolve on the primary host).

// mergeParserV1 merges two flat /api/v1.0/torrents arrays.
func mergeParserV1(primary, secondary []byte) []byte {
	var prim []any
	if err := decodeJSONNumbers(primary, &prim); err != nil {
		prim = nil
	}
	var sec []any
	if err := decodeJSONNumbers(secondary, &sec); err != nil {
		sec = nil
	}
	if len(sec) == 0 && prim == nil {
		return primary
	}

	seen := dedupIndexV1(prim)
	for _, item := range sec {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := dedupKey(jsonStr(m["magnet"]), jsonStr(m["title"]), jsonInt(m["size"]))
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		prim = append(prim, m)
	}
	out, err := json.Marshal(prim)
	if err != nil {
		return primary
	}
	return out
}

// mergeParserV2 merges two Jackett-compatible {"Results":[…]} roots. The
// primary's root object (and its extra keys) is kept; only rows travel over.
func mergeParserV2(primary, secondary []byte) []byte {
	root := map[string]any{}
	if err := decodeJSONNumbers(primary, &root); err != nil {
		root = map[string]any{}
	}
	secRoot := map[string]any{}
	if err := decodeJSONNumbers(secondary, &secRoot); err != nil {
		secRoot = nil
	}

	resultsKey, results := v2Results(root)
	if resultsKey == "" {
		resultsKey = "Results"
	}
	_, secResults := v2Results(secRoot)
	if len(secResults) == 0 && len(root) == 0 {
		return primary
	}

	seen := dedupIndexV2(results)
	for _, item := range secResults {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		magnet := jsonStr(m["MagnetUri"])
		if magnet == "" {
			magnet = jsonStr(m["Link"])
		}
		key := dedupKey(magnet, jsonStr(m["Title"]), jsonInt(m["Size"]))
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		results = append(results, m)
	}
	root[resultsKey] = results
	out, err := json.Marshal(root)
	if err != nil {
		return primary
	}
	return out
}

// v2Results finds the Results array in a v2 root, accepting any casing (the
// same tolerance dedupIndexV2's caller in rutracker_merge.go has).
func v2Results(root map[string]any) (string, []any) {
	for k, v := range root {
		if !strings.EqualFold(k, "Results") {
			continue
		}
		arr, _ := v.([]any)
		return k, arr
	}
	return "", nil
}
