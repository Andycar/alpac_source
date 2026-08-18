package httpapi

import (
	stdjson "encoding/json"
	"testing"
)

func TestMergeParserV1(t *testing.T) {
	primary := []byte(`[
		{"title":"Passengers 2016 BDRemux","magnet":"magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":30000000000,"sid":40},
		{"title":"Passengers 2016 WEB-DL","magnet":"magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":4000000000,"sid":10}
	]`)
	secondary := []byte(`[
		{"title":"Passengers 2016 BDRemux (copy)","magnet":"magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","size":30000000000,"sid":5},
		{"title":"Passengers 2016 HDR 2160p","magnet":"magnet:?xt=urn:btih:cccccccccccccccccccccccccccccccccccccccc","size":50000000000,"sid":20}
	]`)

	var out []map[string]any
	if err := stdjson.Unmarshal(mergeParserV1(primary, secondary), &out); err != nil {
		t.Fatalf("merged body is not valid JSON: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 rows (dup by info-hash dropped, case-insensitive), got %d", len(out))
	}
	if out[2]["title"] != "Passengers 2016 HDR 2160p" {
		t.Fatalf("secondary unique row missing, got %v", out[2]["title"])
	}
	// The primary's row must win over the secondary's duplicate.
	if out[0]["title"] != "Passengers 2016 BDRemux" {
		t.Fatalf("primary row lost to the secondary duplicate: %v", out[0]["title"])
	}
}

func TestMergeParserV1DeadPrimary(t *testing.T) {
	secondary := []byte(`[{"title":"X","magnet":"magnet:?xt=urn:btih:dddddddddddddddddddddddddddddddddddddddd","size":1}]`)
	for _, primary := range [][]byte{nil, []byte("<html>403</html>"), []byte("")} {
		var out []map[string]any
		if err := stdjson.Unmarshal(mergeParserV1(primary, secondary), &out); err != nil {
			t.Fatalf("dead primary %q: merged body invalid: %v", primary, err)
		}
		if len(out) != 1 {
			t.Fatalf("dead primary %q: want the secondary row alone, got %d rows", primary, len(out))
		}
	}
}

func TestMergeParserV2(t *testing.T) {
	primary := []byte(`{"Results":[
		{"Title":"Passengers 2016 BDRemux","MagnetUri":"magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Size":30000000000,"Seeders":40}
	],"Indexers":[]}`)
	secondary := []byte(`{"Results":[
		{"Title":"Passengers 2016 BDRemux","MagnetUri":"magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Size":30000000000,"Seeders":3},
		{"Title":"Passengers 2016 Atmos","MagnetUri":"magnet:?xt=urn:btih:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","Size":60000000000,"Seeders":15}
	]}`)

	var root map[string]any
	if err := stdjson.Unmarshal(mergeParserV2(primary, secondary), &root); err != nil {
		t.Fatalf("merged body is not valid JSON: %v", err)
	}
	results, ok := root["Results"].([]any)
	if !ok {
		t.Fatalf("Results array missing: %v", root)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 rows (dup dropped), got %d", len(results))
	}
	if _, ok := root["Indexers"]; !ok {
		t.Fatalf("primary root keys must survive the merge")
	}
}

func TestMergeParserV2DeadPrimary(t *testing.T) {
	secondary := []byte(`{"Results":[{"Title":"X","MagnetUri":"magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff","Size":1}]}`)
	var root map[string]any
	if err := stdjson.Unmarshal(mergeParserV2([]byte("<html>cf</html>"), secondary), &root); err != nil {
		t.Fatalf("merged body invalid: %v", err)
	}
	if results, _ := root["Results"].([]any); len(results) != 1 {
		t.Fatalf("want the secondary row alone, got %v", root["Results"])
	}
}
