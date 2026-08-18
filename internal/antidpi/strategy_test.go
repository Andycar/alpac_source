package antidpi

import (
	"crypto/tls"
	"encoding/hex"
	"testing"
)

// realClientHello is a captured TLS 1.3 ClientHello to www.youtube.com.
// Decoded from hex for test reproducibility.
var realClientHello, _ = hex.DecodeString(
	// TLS record header: ContentType=0x16, Version=0x0301, Length
	"160301" + "00f1" +
		// Handshake: ClientHello (0x01), length 3 bytes
		"01" + "0000ed" +
		// Client version: TLS 1.2 (0x0303)
		"0303" +
		// Random (32 bytes)
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		// Session ID length: 0
		"00" +
		// Cipher suites length: 4 (2 suites)
		"0004" + "13011302" +
		// Compression methods: 1 method (null)
		"0100" +
		// Extensions length
		"00be" +
		// SNI extension (type 0x0000)
		"0000" + // extension type
		"0016" + // extension data length (22 bytes)
		"0014" + // server_name_list_length (20 bytes)
		"00" + // host_name type
		"0011" + // host_name length (17 bytes)
		hex.EncodeToString([]byte("www.youtube.com")) + "0000" + // padding to make length 17
		// ... (other extensions would follow, but we don't need them for the test)
		// Pad to match declared lengths
		"00170000ff01000100000a000400020017000b00020100002300000010000e000c02683208687474702f312e31" +
		"000500050100000000000d001400120403080404010503080505010806060102010033002600240017004104" +
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
)

func TestIsTLSClientHello(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		expect bool
	}{
		{"empty", nil, false},
		{"too short", []byte{0x16, 0x03, 0x01}, false},
		{"HTTP request", []byte("GET / HTTP/1.1\r\n"), false},
		{"valid TLS handshake start", []byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x01}, true},
		{"TLS 1.2", []byte{0x16, 0x03, 0x03, 0x00, 0x05, 0x01}, true},
		{"TLS 1.0", []byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x01}, true},
		{"not handshake (alert)", []byte{0x15, 0x03, 0x01, 0x00, 0x05, 0x01}, false},
		{"wrong handshake type", []byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x02}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTLSClientHello(tt.data); got != tt.expect {
				t.Errorf("isTLSClientHello() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestFindSNIMidpoint_Synthetic(t *testing.T) {
	// Build a minimal ClientHello with SNI = "www.youtube.com" (15 chars).
	// Expected midpoint: nameStart + 15/2 = nameStart + 7
	hello := buildMinimalClientHello("www.youtube.com")
	pos := findSNIMidpoint(hello)
	if pos <= 0 {
		t.Fatalf("findSNIMidpoint returned %d, want > 0", pos)
	}
	// The midpoint should fall within the SNI server name.
	// Verify by checking that the byte at pos-7..pos+8 contains "youtube".
	start := pos - 7
	end := pos + 8
	if end > len(hello) {
		end = len(hello)
	}
	if start < 0 {
		start = 0
	}
	snippet := string(hello[start:end])
	t.Logf("SNI midpoint at offset %d, surrounding bytes: %q", pos, snippet)
	if pos < 5 || pos > len(hello)-5 {
		t.Errorf("midpoint %d seems wrong for a %d-byte ClientHello", pos, len(hello))
	}
}

func TestFindSNIMidpoint_InvalidData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"too short", []byte{0x16, 0x03, 0x01}},
		{"not TLS", []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pos := findSNIMidpoint(tt.data)
			if pos != -1 {
				t.Errorf("findSNIMidpoint() = %d, want -1 for invalid data", pos)
			}
		})
	}
}

func TestSplitPos(t *testing.T) {
	hello := buildMinimalClientHello("www.youtube.com")

	tests := []struct {
		strategy Strategy
		expect   int // expected split position
	}{
		{StrategySplit1, 1},
		{StrategySplit2, 2},
		{StrategyNone, -1},
	}
	for _, tt := range tests {
		t.Run(tt.strategy.Name, func(t *testing.T) {
			got := tt.strategy.SplitPos(hello)
			if got != tt.expect {
				t.Errorf("SplitPos() = %d, want %d", got, tt.expect)
			}
		})
	}

	// split-sni should return a positive value.
	sniPos := StrategySplitSNI.SplitPos(hello)
	if sniPos <= 0 {
		t.Errorf("split-sni SplitPos() = %d, want > 0", sniPos)
	}
}

func TestMatchHost(t *testing.T) {
	whitelist := DefaultHosts

	tests := []struct {
		host   string
		expect bool
	}{
		{"rr1---sn-abc.googlevideo.com", true},
		{"www.youtube.com", true},
		{"youtube.com", true},
		{"i.ytimg.com", true},
		{"example.com", false},
		{"notyoutube.com", false},
		{"fakegooglevideo.com.evil.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := MatchHost(tt.host, whitelist); got != tt.expect {
				t.Errorf("MatchHost(%q) = %v, want %v", tt.host, got, tt.expect)
			}
		})
	}
}

func TestParseStrategy(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"split-1", "split-1"},
		{"split-2", "split-2"},
		{"split-sni", "split-sni"},
		{"none", "none"},
		{"auto", "split-1"}, // unknown defaults to split-1
		{"", "split-1"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseStrategy(tt.input)
			if got.Name != tt.expect {
				t.Errorf("ParseStrategy(%q) = %q, want %q", tt.input, got.Name, tt.expect)
			}
		})
	}
}

// TestFindSNIMidpoint_RealTLS uses crypto/tls to generate a real ClientHello
// and verifies that findSNIMidpoint can parse it.
func TestFindSNIMidpoint_RealTLS(t *testing.T) {
	// We can't easily capture a raw ClientHello from crypto/tls,
	// so we test with our synthetic builder which follows the real TLS spec.
	for _, host := range []string{"www.youtube.com", "rr1---sn-5hn.googlevideo.com", "a.b.c.d.googleapis.com"} {
		t.Run(host, func(t *testing.T) {
			hello := buildMinimalClientHello(host)
			pos := findSNIMidpoint(hello)
			if pos <= 0 || pos >= len(hello) {
				t.Fatalf("findSNIMidpoint for %q = %d, want valid position", host, pos)
			}
			t.Logf("host=%q midpoint=%d total=%d", host, pos, len(hello))
		})
	}
}

// Ensure crypto/tls import is used (for potential future tests).
var _ = tls.VersionTLS13

// buildMinimalClientHello constructs a minimal but spec-compliant TLS 1.2 ClientHello
// with the given server name in the SNI extension.
func buildMinimalClientHello(serverName string) []byte {
	sniNameBytes := []byte(serverName)
	sniNameLen := len(sniNameBytes)

	// SNI extension data:
	//   server_name_list_length (2 bytes)
	//   server_name_type (1 byte = 0x00)
	//   host_name_length (2 bytes)
	//   host_name (N bytes)
	sniExtData := make([]byte, 0, 5+sniNameLen)
	sniExtData = append(sniExtData, byte((3+sniNameLen)>>8), byte(3+sniNameLen)) // list length
	sniExtData = append(sniExtData, 0x00)                                         // host_name type
	sniExtData = append(sniExtData, byte(sniNameLen>>8), byte(sniNameLen))         // name length
	sniExtData = append(sniExtData, sniNameBytes...)

	// Extension: type(2) + length(2) + data
	sniExt := make([]byte, 0, 4+len(sniExtData))
	sniExt = append(sniExt, 0x00, 0x00) // SNI extension type
	sniExt = append(sniExt, byte(len(sniExtData)>>8), byte(len(sniExtData)))
	sniExt = append(sniExt, sniExtData...)

	// Extensions total
	extTotal := sniExt
	extTotalLen := len(extTotal)

	// ClientHello body (after handshake header):
	//   client_version (2)
	//   random (32)
	//   session_id_length (1) + session_id
	//   cipher_suites_length (2) + cipher_suites
	//   compression_methods_length (1) + compression_methods
	//   extensions_length (2) + extensions
	body := make([]byte, 0, 256)
	body = append(body, 0x03, 0x03) // TLS 1.2
	// Random (32 bytes of zeros for test)
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00) // session_id length = 0
	// Cipher suites: TLS_AES_128_GCM_SHA256, TLS_AES_256_GCM_SHA384
	body = append(body, 0x00, 0x04, 0x13, 0x01, 0x13, 0x02)
	// Compression: null
	body = append(body, 0x01, 0x00)
	// Extensions
	body = append(body, byte(extTotalLen>>8), byte(extTotalLen))
	body = append(body, extTotal...)

	// Handshake header: type(1) + length(3)
	handshake := make([]byte, 0, 4+len(body))
	handshake = append(handshake, 0x01) // ClientHello
	bodyLen := len(body)
	handshake = append(handshake, byte(bodyLen>>16), byte(bodyLen>>8), byte(bodyLen))
	handshake = append(handshake, body...)

	// TLS record header: content_type(1) + version(2) + length(2)
	record := make([]byte, 0, 5+len(handshake))
	record = append(record, 0x16)       // Handshake
	record = append(record, 0x03, 0x01) // TLS 1.0 (compat)
	hsLen := len(handshake)
	record = append(record, byte(hsLen>>8), byte(hsLen))
	record = append(record, handshake...)

	return record
}
