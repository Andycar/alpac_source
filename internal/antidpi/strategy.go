// Package antidpi implements DPI bypass via TLS ClientHello splitting.
//
// Russian ISPs use DPI (TSPU) to throttle/block YouTube by inspecting
// the SNI field in TLS ClientHello. By splitting the first TCP segment
// into two fragments, the SNI becomes invisible to stateless DPI boxes.
//
// This package provides a lightweight SOCKS5 proxy that transparently
// applies the splitting to whitelisted hosts (googlevideo.com, youtube.com).
package antidpi

import "encoding/binary"

// Strategy defines how to split the TLS ClientHello.
type Strategy struct {
	Name string // "split-1", "split-2", "split-sni", "split-tlsrec", "split-tlsrec-40", "none"
}

var (
	StrategySplit1      = Strategy{Name: "split-1"}
	StrategySplit2      = Strategy{Name: "split-2"}
	StrategySplitSNI    = Strategy{Name: "split-sni"}
	StrategySplitTLSRec   = Strategy{Name: "split-tlsrec"}
	StrategySplitTLSRec40 = Strategy{Name: "split-tlsrec-40"}
	StrategyNone          = Strategy{Name: "none"}

	// ProbeOrder is the order in which strategies are tested during auto-discovery.
	// split-tlsrec is first because it defeats DPI that reassembles TCP streams:
	// instead of splitting raw TCP bytes, it rewrites the ClientHello into multiple
	// valid TLS records. DPI that only parses the first TLS record won't see the
	// full SNI. Combined with inter-fragment delays, this also defeats DPI with
	// short reassembly timeouts.
	ProbeOrder = []Strategy{StrategySplitTLSRec, StrategySplitTLSRec40, StrategySplitSNI, StrategySplit1, StrategySplit2}
)

// ParseStrategy converts a config string to a Strategy.
func ParseStrategy(s string) Strategy {
	switch s {
	case "split-1":
		return StrategySplit1
	case "split-2":
		return StrategySplit2
	case "split-sni":
		return StrategySplitSNI
	case "split-tlsrec":
		return StrategySplitTLSRec
	case "split-tlsrec-40":
		return StrategySplitTLSRec40
	case "none":
		return StrategyNone
	default:
		return StrategySplit1 // safe default
	}
}

// SplitPos returns the byte position at which to split the ClientHello.
// Returns -1 if no split should be applied.
func (s Strategy) SplitPos(data []byte) int {
	switch s.Name {
	case "split-1":
		return 1
	case "split-2":
		return 2
	case "split-sni":
		return findSNIMidpoint(data)
	default:
		return -1
	}
}

// isTLSClientHello checks if data looks like a TLS ClientHello.
// Minimum TLS record header: type(1) + version(2) + length(2) = 5 bytes.
func isTLSClientHello(data []byte) bool {
	if len(data) < 6 {
		return false
	}
	// ContentType = 0x16 (Handshake)
	if data[0] != 0x16 {
		return false
	}
	// Version: TLS 1.0 (0x0301), 1.1 (0x0302), 1.2 (0x0303), or legacy 1.0 compat in TLS 1.3
	major := data[1]
	minor := data[2]
	if major != 0x03 || minor > 0x03 {
		return false
	}
	// Handshake type = 0x01 (ClientHello) at offset 5
	if data[5] != 0x01 {
		return false
	}
	return true
}

// findSNIMidpoint parses a TLS ClientHello to locate the SNI extension,
// then returns the byte offset at the midpoint of the server name.
// Returns -1 if SNI cannot be found.
//
// TLS ClientHello structure:
//
//	[0]     ContentType (0x16)
//	[1..2]  TLS version
//	[3..4]  Record length
//	[5]     Handshake type (0x01)
//	[6..8]  Handshake length (3 bytes)
//	[9..10] Client version
//	[11..42] Random (32 bytes)
//	[43]    Session ID length (N)
//	[43+1..43+N] Session ID
//	... Cipher suites, compression, extensions ...
func findSNIMidpoint(data []byte) int {
	if len(data) < 44 {
		return -1
	}
	if data[0] != 0x16 || data[5] != 0x01 {
		return -1
	}

	// Skip TLS record header (5 bytes) + handshake header (4 bytes)
	// = offset 9 is start of ClientHello body
	pos := 9

	// Client version (2 bytes)
	pos += 2
	if pos > len(data) {
		return -1
	}

	// Random (32 bytes)
	pos += 32
	if pos >= len(data) {
		return -1
	}

	// Session ID
	sessIDLen := int(data[pos])
	pos += 1 + sessIDLen
	if pos+2 > len(data) {
		return -1
	}

	// Cipher suites
	cipherLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2 + cipherLen
	if pos+1 > len(data) {
		return -1
	}

	// Compression methods
	compLen := int(data[pos])
	pos += 1 + compLen
	if pos+2 > len(data) {
		return -1
	}

	// Extensions length
	extLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	extEnd := pos + extLen
	if extEnd > len(data) {
		extEnd = len(data)
	}

	// Walk extensions to find SNI (type 0x0000)
	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(data[pos : pos+2])
		extDataLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		pos += 4
		if pos+extDataLen > extEnd {
			break
		}

		if extType == 0x0000 { // server_name
			// SNI extension data:
			//   [0..1] server_name_list_length
			//   [2]    server_name_type (0 = host_name)
			//   [3..4] host_name_length
			//   [5..]  host_name
			if extDataLen < 5 {
				break
			}
			nameType := data[pos+2]
			if nameType != 0 { // not host_name
				break
			}
			nameLen := int(binary.BigEndian.Uint16(data[pos+3 : pos+5]))
			nameStart := pos + 5
			if nameStart+nameLen > extEnd {
				break
			}
			// Return the midpoint of the server name within the original data.
			mid := nameStart + nameLen/2
			if mid <= 0 || mid >= len(data) {
				return -1
			}
			return mid
		}
		pos += extDataLen
	}

	return -1 // SNI not found
}

// findSNIRange locates the SNI hostname within a TLS ClientHello and returns
// the byte range [nameStart, nameEnd) of the hostname.
// Returns (-1, -1) if SNI cannot be found.
func findSNIRange(data []byte) (nameStart, nameEnd int) {
	if len(data) < 44 {
		return -1, -1
	}
	if data[0] != 0x16 || data[5] != 0x01 {
		return -1, -1
	}

	pos := 9 // Skip TLS record header (5) + handshake header (4)
	pos += 2 // Client version
	if pos > len(data) {
		return -1, -1
	}
	pos += 32 // Random
	if pos >= len(data) {
		return -1, -1
	}
	sessIDLen := int(data[pos])
	pos += 1 + sessIDLen
	if pos+2 > len(data) {
		return -1, -1
	}
	cipherLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2 + cipherLen
	if pos+1 > len(data) {
		return -1, -1
	}
	compLen := int(data[pos])
	pos += 1 + compLen
	if pos+2 > len(data) {
		return -1, -1
	}
	extLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	extEnd := pos + extLen
	if extEnd > len(data) {
		extEnd = len(data)
	}

	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(data[pos : pos+2])
		extDataLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		pos += 4
		if pos+extDataLen > extEnd {
			break
		}
		if extType == 0x0000 { // server_name
			if extDataLen < 5 {
				break
			}
			if data[pos+2] != 0 { // not host_name
				break
			}
			nameLen := int(binary.BigEndian.Uint16(data[pos+3 : pos+5]))
			start := pos + 5
			if start+nameLen > extEnd {
				break
			}
			return start, start + nameLen
		}
		pos += extDataLen
	}
	return -1, -1
}

// FragmentTLSRecord rewrites a TLS ClientHello record into multiple valid TLS
// records, splitting the payload so that the SNI hostname is divided across
// record boundaries. DPI that reassembles TCP but doesn't reassemble TLS
// records at the application layer won't see the complete SNI.
//
// Per RFC 8446 §5.1: "Handshake messages MAY be coalesced into a single
// TLSPlaintext record or fragmented across several records."
//
// Returns nil if the data is not a valid TLS ClientHello or SNI cannot be found.
func FragmentTLSRecord(data []byte) [][]byte {
	if len(data) < 6 || data[0] != 0x16 {
		return nil
	}

	recordType := data[0]
	version := data[1:3]
	payload := data[5:] // everything after the 5-byte TLS record header

	nameStart, nameEnd := findSNIRange(data)
	if nameStart < 0 || nameEnd < 0 {
		return nil
	}

	// Convert nameStart/nameEnd from data offsets to payload offsets.
	payloadNameStart := nameStart - 5
	payloadNameEnd := nameEnd - 5

	if payloadNameStart <= 0 || payloadNameEnd > len(payload) {
		return nil
	}

	// Split the payload at the midpoint of the SNI hostname.
	// Fragment 1: everything up to and including the first half of the hostname.
	// Fragment 2: second half of the hostname and everything after.
	mid := payloadNameStart + (payloadNameEnd-payloadNameStart)/2
	if mid <= 0 || mid >= len(payload) {
		return nil
	}

	var fragments [][]byte
	fragments = append(fragments, makeTLSRecord(recordType, version, payload[:mid]))
	fragments = append(fragments, makeTLSRecord(recordType, version, payload[mid:]))

	return fragments
}

// FragmentTLSRecordChunked splits a TLS ClientHello into many small valid TLS
// records of at most chunkSize bytes of payload each. Combined with a short
// inter-fragment delay (10ms), this defeats DPI that has reassembly timeouts
// or buffer limits — the DPI gives up before receiving enough data to read SNI.
//
// Returns nil if the data is not a valid TLS record.
func FragmentTLSRecordChunked(data []byte, chunkSize int) [][]byte {
	if len(data) < 6 || data[0] != 0x16 {
		return nil
	}
	if chunkSize <= 0 {
		chunkSize = 40
	}

	recordType := data[0]
	version := data[1:3]
	payload := data[5:]

	var fragments [][]byte
	for len(payload) > 0 {
		end := chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		fragments = append(fragments, makeTLSRecord(recordType, version, payload[:end]))
		payload = payload[end:]
	}

	if len(fragments) <= 1 {
		return nil // no point in fragmenting if only 1 chunk
	}
	return fragments
}

// makeTLSRecord creates a TLS record: type(1) + version(2) + length(2) + payload.
func makeTLSRecord(recordType byte, version []byte, payload []byte) []byte {
	rec := make([]byte, 5+len(payload))
	rec[0] = recordType
	rec[1] = version[0]
	rec[2] = version[1]
	binary.BigEndian.PutUint16(rec[3:5], uint16(len(payload)))
	copy(rec[5:], payload)
	return rec
}

// DefaultHosts are the domains whose TLS connections should be split.
// Matching is by suffix — e.g., "googlevideo.com" matches "rr1---sn-abc.googlevideo.com".
var DefaultHosts = []string{
	"googlevideo.com",
	"youtube.com",
	"youtu.be",
	"ytimg.com",
	"ggpht.com",
	"googleapis.com",
	"googleusercontent.com",
	"gstatic.com",
	"google.com",
}

// MatchHost checks if the given host matches any entry in the whitelist (by suffix).
func MatchHost(host string, whitelist []string) bool {
	for _, w := range whitelist {
		if host == w {
			return true
		}
		// Suffix match: host ends with "."+w
		if len(host) > len(w)+1 && host[len(host)-len(w)-1] == '.' && host[len(host)-len(w):] == w {
			return true
		}
	}
	return false
}
