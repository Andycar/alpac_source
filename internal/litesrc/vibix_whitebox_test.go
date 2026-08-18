package litesrc

import (
	"encoding/base64"
	stdjson "encoding/json"
	"strings"
	"testing"
)

// vibixTestDirectTransport routes the "vibix" balancer through the shared
// (direct) transport so the source reaches the httptest mock instead of the
// hardcoded residential SOCKS5 (127.0.0.1:40008) that newVibixChecker would
// otherwise register. Must run before the handler builds the checker.
// vibixEncryptPayload encrypts a JSON payload using the same v=1 scheme.
func vibixEncryptPayload(plainJSON string) string {
	key := []byte(vibixDecoderKey)
	data := []byte(plainJSON)
	for i := range data {
		data[i] ^= key[i%len(key)]
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	// Reverse for useReverse=true
	runes := []rune(encoded)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func TestVibixDecodeAPIResponse(t *testing.T) {
	plainJSON := `{"data":{"playlist":[{"title":"Test Voice","file":"[1080p]https://cdn.example/test_1080.m3u8,[720p]https://cdn.example/test_720.m3u8"}]}}`
	encrypted := vibixEncryptPayload(plainJSON)

	// Build the API response
	apiResp := `{"p":"` + encrypted + `","v":1}`

	decoded, ok := vibixDecodeAPIResponse([]byte(apiResp))
	if !ok {
		t.Fatal("expected decode to succeed")
	}

	var root vibixPlaylistResponse
	if err := stdjson.Unmarshal(decoded, &root); err != nil {
		t.Fatalf("unmarshal decoded: %v", err)
	}
	if len(root.Data.Playlist) != 1 {
		t.Fatalf("expected 1 playlist item, got %d", len(root.Data.Playlist))
	}
	if root.Data.Playlist[0].Title != "Test Voice" {
		t.Fatalf("unexpected title: %s", root.Data.Playlist[0].Title)
	}
	if !strings.Contains(root.Data.Playlist[0].File, "cdn.example") {
		t.Fatalf("unexpected file: %s", root.Data.Playlist[0].File)
	}
}

func TestVibixDecodeV0(t *testing.T) {
	plainJSON := `{"data":{"playlist":[{"title":"V0","file":"[720p]https://cdn.example/v0.m3u8"}]}}`
	// v=0 passes p as-is, but p must be a string
	apiResp2 := `{"p":"` + strings.ReplaceAll(plainJSON, `"`, `\"`) + `","v":0}`

	decoded, ok := vibixDecodeAPIResponse([]byte(apiResp2))
	if !ok {
		t.Fatal("expected v=0 decode to succeed")
	}
	if !strings.Contains(string(decoded), "V0") {
		t.Fatalf("unexpected decoded: %s", string(decoded))
	}
}
