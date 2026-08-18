package main

// startSocks5Forwarder runs a tiny SOCKS5 listener on 127.0.0.1:port
// (NO_AUTH) that tunnels every CONNECT through the upstream SOCKS5
// proxy with username/password authentication.
//
// Why: Chrome's --proxy-server flag doesn't accept inline credentials,
// so we cannot point it at "socks5://user:pass@upstream". The
// forwarder bridges the auth gap — Chrome speaks plain SOCKS5 to us,
// we speak authenticated SOCKS5 to the upstream.
//
// Scope: implements just enough of RFC 1928 to handle Chromium's
// CONNECT requests. UDP/BIND not supported.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"

	"golang.org/x/net/proxy"
)

// startSocks5Forwarder picks a free localhost port, starts accepting,
// and returns the chosen "127.0.0.1:port" string. The listener runs
// forever in a background goroutine — fine for the lifetime of the
// probe.
func startSocks5Forwarder(upstreamHostPort, user, pass string) (string, error) {
	auth := &proxy.Auth{User: user, Password: pass}
	dialer, err := proxy.SOCKS5("tcp", upstreamHostPort, auth, proxy.Direct)
	if err != nil {
		return "", fmt.Errorf("upstream dialer: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSocks5Client(conn, dialer)
		}
	}()

	return ln.Addr().String(), nil
}

func handleSocks5Client(c net.Conn, upstream proxy.Dialer) {
	defer c.Close()

	// 1) Greeting: VER, NMETHODS, METHODS[]
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	if hdr[0] != 0x05 {
		return
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	// Reply NO_AUTH selected.
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// 2) Request: VER, CMD, RSV, ATYP, ADDR, PORT
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	if req[0] != 0x05 || req[1] != 0x01 {
		// Only CONNECT supported; reply COMMAND_NOT_SUPPORTED.
		_, _ = c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	host, err := readSocksAddr(c, req[3])
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(c, portBuf); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBuf)
	target := net.JoinHostPort(host, strconv.Itoa(int(port)))

	// 3) Dial upstream.
	dest, err := upstream.Dial("tcp", target)
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer dest.Close()

	// Reply success — bound addr is zero (Chrome doesn't care).
	_, _ = c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// 4) Splice. Done when either side closes.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(dest, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, dest); done <- struct{}{} }()
	<-done
}

func readSocksAddr(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x03: // Domain
		lb := make([]byte, 1)
		if _, err := io.ReadFull(r, lb); err != nil {
			return "", err
		}
		b := make([]byte, int(lb[0]))
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return string(b), nil
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	}
	return "", errors.New("unknown ATYP")
}
