package proxycore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"lampac-go/internal/sidecar"
)

const ssMaxPayload = 0x3FFF // 16383 bytes per chunk

// ssDialer implements the Shadowsocks AEAD protocol.
type ssDialer struct {
	server string
	cipher string
	key    []byte
}

func newShadowsocksDialer(out sidecar.ProxyOutbound) (*ssDialer, error) {
	if out.Server == "" || out.Port == 0 || out.Password == "" {
		return nil, fmt.Errorf("shadowsocks: server, port, and password required")
	}

	cipherName := out.Encryption
	if cipherName == "" {
		cipherName = "aes-256-gcm"
	}

	keySize := ssKeySize(cipherName)
	if keySize == 0 {
		return nil, fmt.Errorf("shadowsocks: unsupported cipher %q", cipherName)
	}

	key := ssKDF(out.Password, keySize)

	return &ssDialer{
		server: fmt.Sprintf("%s:%d", out.Server, out.Port),
		cipher: cipherName,
		key:    key,
	}, nil
}

func (d *ssDialer) Protocol() string { return "ss" }
func (d *ssDialer) Close() error     { return nil }

func (d *ssDialer) DialProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	host, port, err := parseTarget(targetAddr)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}

	conn, err := dialTCP(ctx, d.server)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}

	// Wrap with AEAD cipher stream.
	ssConn, err := newSSAEADConn(conn, d.key, d.cipher)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ss cipher: %w", err)
	}

	// Send target address as first payload.
	addrBuf := ssEncodeAddr(host, port)
	if _, err := ssConn.Write(addrBuf); err != nil {
		ssConn.Close()
		return nil, fmt.Errorf("ss addr: %w", err)
	}

	return ssConn, nil
}

// ssAEADConn wraps a net.Conn with Shadowsocks AEAD encryption.
type ssAEADConn struct {
	net.Conn
	encAEAD  cipher.AEAD
	decAEAD  cipher.AEAD
	encNonce []byte
	decNonce []byte

	// Read buffer for decrypted data.
	readBuf []byte
	readPos int

	// Write key for lazy initialization.
	key       []byte
	cipherStr string
	encInited bool
	decInited bool
}

func newSSAEADConn(conn net.Conn, key []byte, cipherName string) (*ssAEADConn, error) {
	return &ssAEADConn{
		Conn:      conn,
		key:       key,
		cipherStr: cipherName,
	}, nil
}

func (c *ssAEADConn) Write(p []byte) (int, error) {
	if !c.encInited {
		// Generate salt and derive subkey.
		saltSize := len(c.key)
		salt := make([]byte, saltSize)
		if _, err := rand.Read(salt); err != nil {
			return 0, err
		}
		subkey := ssHKDFSHA1(c.key, salt)
		aead, err := ssNewAEAD(c.cipherStr, subkey)
		if err != nil {
			return 0, err
		}
		c.encAEAD = aead
		c.encNonce = make([]byte, aead.NonceSize())
		c.encInited = true

		// Write salt first.
		if _, err := c.Conn.Write(salt); err != nil {
			return 0, err
		}
	}

	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > ssMaxPayload {
			chunk = chunk[:ssMaxPayload]
		}
		p = p[len(chunk):]

		// Encrypt length prefix [2 bytes].
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(chunk)))
		encLen := c.encAEAD.Seal(nil, c.encNonce, lenBuf[:], nil)
		increment(c.encNonce)

		// Encrypt payload.
		encPayload := c.encAEAD.Seal(nil, c.encNonce, chunk, nil)
		increment(c.encNonce)

		// Write both.
		if _, err := c.Conn.Write(encLen); err != nil {
			return written, err
		}
		if _, err := c.Conn.Write(encPayload); err != nil {
			return written, err
		}
		written += len(chunk)
	}
	return written, nil
}

func (c *ssAEADConn) Read(p []byte) (int, error) {
	// Return buffered data.
	if c.readPos < len(c.readBuf) {
		n := copy(p, c.readBuf[c.readPos:])
		c.readPos += n
		return n, nil
	}

	if !c.decInited {
		// Read salt.
		saltSize := len(c.key)
		salt := make([]byte, saltSize)
		if _, err := io.ReadFull(c.Conn, salt); err != nil {
			return 0, err
		}
		subkey := ssHKDFSHA1(c.key, salt)
		aead, err := ssNewAEAD(c.cipherStr, subkey)
		if err != nil {
			return 0, err
		}
		c.decAEAD = aead
		c.decNonce = make([]byte, aead.NonceSize())
		c.decInited = true
	}

	// Read encrypted length.
	encLenBuf := make([]byte, 2+c.decAEAD.Overhead())
	if _, err := io.ReadFull(c.Conn, encLenBuf); err != nil {
		return 0, err
	}
	lenBuf, err := c.decAEAD.Open(nil, c.decNonce, encLenBuf, nil)
	if err != nil {
		return 0, fmt.Errorf("ss decrypt len: %w", err)
	}
	increment(c.decNonce)

	payloadLen := int(binary.BigEndian.Uint16(lenBuf))
	if payloadLen > ssMaxPayload {
		return 0, fmt.Errorf("ss payload too large: %d", payloadLen)
	}

	// Read encrypted payload.
	encPayload := make([]byte, payloadLen+c.decAEAD.Overhead())
	if _, err := io.ReadFull(c.Conn, encPayload); err != nil {
		return 0, err
	}
	payload, err := c.decAEAD.Open(nil, c.decNonce, encPayload, nil)
	if err != nil {
		return 0, fmt.Errorf("ss decrypt payload: %w", err)
	}
	increment(c.decNonce)

	n := copy(p, payload)
	if n < len(payload) {
		c.readBuf = payload
		c.readPos = n
	} else {
		c.readBuf = nil
		c.readPos = 0
	}
	return n, nil
}

// ssEncodeAddr encodes a target address in Shadowsocks format (same as SOCKS5).
func ssEncodeAddr(host string, port uint16) []byte {
	return appendSOCKSAddr(nil, host, port)
}

// ssKDF derives a key from password using EVP_BytesToKey (OpenSSL compatible).
func ssKDF(password string, keyLen int) []byte {
	var b, prev []byte
	h := sha256.New()
	for len(b) < keyLen {
		h.Reset()
		h.Write(prev)
		h.Write([]byte(password))
		prev = h.Sum(nil)
		b = append(b, prev...)
	}
	return b[:keyLen]
}

// ssHKDFSHA1 derives a subkey from master key and salt using HKDF-SHA1.
func ssHKDFSHA1(key, salt []byte) []byte {
	r := hkdf.New(sha256.New, key, salt, []byte("ss-subkey"))
	subkey := make([]byte, len(key))
	io.ReadFull(r, subkey)
	return subkey
}

// ssNewAEAD creates an AEAD cipher from cipher name and key.
func ssNewAEAD(cipherName string, key []byte) (cipher.AEAD, error) {
	switch cipherName {
	case "aes-128-gcm", "aes-256-gcm":
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(block)
	case "chacha20-ietf-poly1305":
		return chacha20poly1305.New(key)
	default:
		return nil, fmt.Errorf("unsupported cipher: %s", cipherName)
	}
}

// ssKeySize returns the key size for a given cipher.
func ssKeySize(cipher string) int {
	switch cipher {
	case "aes-128-gcm":
		return 16
	case "aes-256-gcm":
		return 32
	case "chacha20-ietf-poly1305":
		return 32
	default:
		return 0
	}
}

// increment increments a nonce (little-endian byte counter).
func increment(nonce []byte) {
	for i := range nonce {
		nonce[i]++
		if nonce[i] != 0 {
			break
		}
	}
}
