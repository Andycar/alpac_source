package proxycore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"time"

	"lampac-go/internal/sidecar"
)

// vmessDialer implements the VMess protocol (AEAD header, length-based stream).
type vmessDialer struct {
	server  string
	uuid    [16]byte
	alterID int
	tls     tlsConfig
	network string // "tcp", "ws"
	useTLS  bool
}

func newVMessDialer(out sidecar.ProxyOutbound) (*vmessDialer, error) {
	if out.Server == "" || out.Port == 0 || out.UUID == "" {
		return nil, fmt.Errorf("vmess: server, port, and UUID required")
	}
	uuid, err := parseUUID(out.UUID)
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}
	d := &vmessDialer{
		server:  fmt.Sprintf("%s:%d", out.Server, out.Port),
		uuid:    uuid,
		alterID: out.AlterID,
		network: out.Network,
		useTLS:  out.Security == "tls",
		tls: tlsConfig{
			Server:      fmt.Sprintf("%s:%d", out.Server, out.Port),
			SNI:         out.SNI,
			Fingerprint: out.Fingerprint,
			ALPN:        out.ALPN,
		},
	}
	return d, nil
}

func (d *vmessDialer) Protocol() string { return "vmess" }
func (d *vmessDialer) Close() error     { return nil }

func (d *vmessDialer) DialProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	host, port, err := parseTarget(targetAddr)
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}

	// 1. Connect to server.
	var conn net.Conn
	if d.useTLS {
		conn, err = dialTLS(ctx, d.tls)
	} else {
		conn, err = dialTCP(ctx, d.server)
	}
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}

	// 2. Transport layer.
	if d.network == "ws" {
		wsConn, err := upgradeWebSocket(ctx, conn, d.server, d.tls.SNI)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("vmess ws: %w", err)
		}
		conn = wsConn
	}

	// 3. VMess handshake: send AEAD header + get wrapped conn.
	vmConn, err := d.handshake(conn, host, port)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("vmess handshake: %w", err)
	}

	return vmConn, nil
}

// handshake performs the VMess AEAD handshake and returns an encrypted connection.
func (d *vmessDialer) handshake(conn net.Conn, host string, port uint16) (net.Conn, error) {
	// Generate session keys.
	var reqBodyKey [16]byte
	var reqBodyIV [16]byte
	var respV byte
	rand.Read(reqBodyKey[:])
	rand.Read(reqBodyIV[:])
	rand.Read([]byte{respV})

	// Derive response key/IV.
	respBodyKey := md5sum(reqBodyKey[:])
	respBodyIV := md5sum(reqBodyIV[:])

	// Build instruction payload.
	payload := d.buildInstruction(reqBodyKey, reqBodyIV, respV, host, port)

	// Auth: HMAC-MD5(uuid, timestamp) — the VMess "auth ID" for AEAD mode.
	ts := time.Now().Unix()
	authID := vmessAuthID(d.uuid, ts)

	// AEAD header encryption.
	headerKey := vmessHeaderKey(d.uuid, ts)
	headerIV := vmessHeaderIV(d.uuid, ts)

	// Encrypt header with AES-128-GCM.
	block, err := aes.NewCipher(headerKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Prepend 2-byte length.
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(payload)))
	lenCiphertext := aead.Seal(nil, headerIV[:aead.NonceSize()], lenBuf[:], authID[:])

	// Encrypt payload.
	payloadNonce := incrementNonce(headerIV[:aead.NonceSize()])
	payloadCiphertext := aead.Seal(nil, payloadNonce, payload, authID[:])

	// Write: authID(16) + encLen + encPayload
	var header []byte
	header = append(header, authID[:]...)
	header = append(header, lenCiphertext...)
	header = append(header, payloadCiphertext...)
	if _, err := conn.Write(header); err != nil {
		return nil, err
	}

	// Create stream cipher for body.
	encStream, err := newVMessStream(reqBodyKey[:], reqBodyIV[:])
	if err != nil {
		return nil, err
	}
	decStream, err := newVMessStream(respBodyKey, respBodyIV)
	if err != nil {
		return nil, err
	}

	return &vmessConn{
		Conn:    conn,
		enc:     encStream,
		dec:     decStream,
		respV:   respV,
		readBuf: nil,
	}, nil
}

// buildInstruction creates the VMess instruction payload.
func (d *vmessDialer) buildInstruction(reqKey [16]byte, reqIV [16]byte, respV byte, host string, port uint16) []byte {
	buf := make([]byte, 0, 128)

	// Version (1).
	buf = append(buf, 0x01)

	// Body IV (16).
	buf = append(buf, reqIV[:]...)

	// Body key (16).
	buf = append(buf, reqKey[:]...)

	// Response auth V (1).
	buf = append(buf, respV)

	// Option: chunk stream (0x01).
	buf = append(buf, 0x01)

	// Padding length (upper 4 bits) + security (lower 4 bits).
	// Security: 0x03 = AES-128-GCM.
	buf = append(buf, 0x03)

	// Reserved (1).
	buf = append(buf, 0x00)

	// Command: TCP (0x01).
	buf = append(buf, 0x01)

	// Port (2, big-endian).
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], port)
	buf = append(buf, portBuf[:]...)

	// Address type + address.
	ip := net.ParseIP(host)
	if ip == nil {
		buf = append(buf, 0x02, byte(len(host)))
		buf = append(buf, []byte(host)...)
	} else if ip4 := ip.To4(); ip4 != nil {
		buf = append(buf, 0x01)
		buf = append(buf, ip4...)
	} else {
		buf = append(buf, 0x03)
		buf = append(buf, ip.To16()...)
	}

	// FNV1a checksum of the above.
	fnvHash := fnv.New32a()
	fnvHash.Write(buf)
	buf = append(buf, fnvHash.Sum(nil)...)

	return buf
}

// vmessAuthID generates the 16-byte auth ID for AEAD header.
func vmessAuthID(uuid [16]byte, ts int64) [16]byte {
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ts))
	h := hmac.New(md5.New, uuid[:])
	h.Write(tsBuf[:])
	var authID [16]byte
	copy(authID[:], h.Sum(nil))
	return authID
}

// vmessHeaderKey derives the AES key for header encryption.
func vmessHeaderKey(uuid [16]byte, ts int64) []byte {
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ts))
	h := sha256.New()
	h.Write(uuid[:])
	h.Write(tsBuf[:])
	return h.Sum(nil)[:16]
}

// vmessHeaderIV derives the AES IV for header encryption.
func vmessHeaderIV(uuid [16]byte, ts int64) []byte {
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ts))
	h := sha256.New()
	h.Write(tsBuf[:])
	h.Write(uuid[:])
	h.Write(tsBuf[:])
	return h.Sum(nil)[:16]
}

func incrementNonce(nonce []byte) []byte {
	out := make([]byte, len(nonce))
	copy(out, nonce)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}

func md5sum(data []byte) []byte {
	h := md5.Sum(data)
	return h[:]
}

// newVMessStream creates an AES-128-GCM AEAD for VMess body encryption.
func newVMessStream(key, iv []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:16])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// vmessConn wraps a connection with VMess AEAD encryption.
type vmessConn struct {
	net.Conn
	enc     cipher.AEAD
	dec     cipher.AEAD
	respV   byte
	readBuf []byte
	readPos int
	encCtr  uint16
	decCtr  uint16
	respOK  bool
}

func (c *vmessConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > ssMaxPayload {
			chunk = chunk[:ssMaxPayload]
		}
		p = p[len(chunk):]

		// Length + tag + payload + tag.
		nonce := vmessNonce(c.encCtr)
		c.encCtr++
		enc := c.enc.Seal(nil, nonce[:c.enc.NonceSize()], chunk, nil)

		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(enc)))
		if _, err := c.Conn.Write(lenBuf[:]); err != nil {
			return written, err
		}
		if _, err := c.Conn.Write(enc); err != nil {
			return written, err
		}
		written += len(chunk)
	}
	return written, nil
}

func (c *vmessConn) Read(p []byte) (int, error) {
	if c.readPos < len(c.readBuf) {
		n := copy(p, c.readBuf[c.readPos:])
		c.readPos += n
		return n, nil
	}

	// Read response header on first read.
	if !c.respOK {
		var respHdr [4]byte
		if _, err := io.ReadFull(c.Conn, respHdr[:]); err != nil {
			return 0, fmt.Errorf("vmess resp: %w", err)
		}
		c.respOK = true
	}

	// Read chunk length.
	var lenBuf [2]byte
	if _, err := io.ReadFull(c.Conn, lenBuf[:]); err != nil {
		return 0, err
	}
	chunkLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if chunkLen == 0 {
		return 0, io.EOF
	}

	enc := make([]byte, chunkLen)
	if _, err := io.ReadFull(c.Conn, enc); err != nil {
		return 0, err
	}

	nonce := vmessNonce(c.decCtr)
	c.decCtr++
	dec, err := c.dec.Open(nil, nonce[:c.dec.NonceSize()], enc, nil)
	if err != nil {
		return 0, fmt.Errorf("vmess decrypt: %w", err)
	}

	n := copy(p, dec)
	if n < len(dec) {
		c.readBuf = dec
		c.readPos = n
	} else {
		c.readBuf = nil
		c.readPos = 0
	}
	return n, nil
}

func vmessNonce(counter uint16) [12]byte {
	var nonce [12]byte
	binary.BigEndian.PutUint16(nonce[:2], counter)
	return nonce
}
