package proxycore

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	socks5Version     = 0x05
	socks5NoAuth      = 0x00
	socks5CmdConnect  = 0x01
	socks5AtypIPv4    = 0x01
	socks5AtypDomain  = 0x03
	socks5AtypIPv6    = 0x04
	socks5RepSuccess  = 0x00
	socks5RepFailure  = 0x01
	socks5RepNotAllow = 0x02

	handshakeTimeout = 10 * time.Second
	dialTimeout      = 15 * time.Second
)

// socks5Server is a lightweight SOCKS5 proxy that tunnels connections
// through a ProtocolDialer.
type socks5Server struct {
	dialer   ProtocolDialer
	listener net.Listener

	mu      sync.Mutex
	wg      sync.WaitGroup
	stopped bool

	// Stats.
	activeConns atomic.Int64
	totalConns  atomic.Int64
	bytesUp     atomic.Int64
	bytesDown   atomic.Int64
	failCount   atomic.Int64
}

func newSOCKS5Server(dialer ProtocolDialer) *socks5Server {
	return &socks5Server{dialer: dialer}
}

// start begins listening on the given address.
func (s *socks5Server) start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("proxycore: listen %s: %w", addr, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.stopped = false
	s.mu.Unlock()

	s.wg.Add(1)
	go s.acceptLoop(ln)
	return nil
}

// stop gracefully shuts down the server.
func (s *socks5Server) stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	ln := s.listener
	s.mu.Unlock()

	if ln != nil {
		ln.Close()
	}
	s.wg.Wait()
}

func (s *socks5Server) addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

func (s *socks5Server) alive() bool {
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return false
	}
	addr := s.addr()
	if addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (s *socks5Server) stats() InstanceStats {
	return InstanceStats{
		ActiveConns: s.activeConns.Load(),
		TotalConns:  s.totalConns.Load(),
		BytesUp:     s.bytesUp.Load(),
		BytesDown:   s.bytesDown.Load(),
		FailCount:   s.failCount.Load(),
	}
}

func (s *socks5Server) acceptLoop(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return
			}
			log.Warn().Err(err).Msg("proxycore: accept error")
			continue
		}
		s.totalConns.Add(1)
		s.activeConns.Add(1)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.activeConns.Add(-1)
			s.handleConn(conn)
		}()
	}
}

func (s *socks5Server) handleConn(clientConn net.Conn) {
	defer clientConn.Close()

	clientConn.SetDeadline(time.Now().Add(handshakeTimeout))

	// SOCKS5 method negotiation.
	var hdr [2]byte
	if _, err := io.ReadFull(clientConn, hdr[:]); err != nil {
		return
	}
	if hdr[0] != socks5Version {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(clientConn, methods); err != nil {
		return
	}
	if _, err := clientConn.Write([]byte{socks5Version, socks5NoAuth}); err != nil {
		return
	}

	// SOCKS5 request: VER CMD RSV ATYP.
	var reqHdr [4]byte
	if _, err := io.ReadFull(clientConn, reqHdr[:]); err != nil {
		return
	}
	if reqHdr[0] != socks5Version || reqHdr[1] != socks5CmdConnect {
		s.socks5Reply(clientConn, socks5RepNotAllow)
		return
	}

	// Parse target address.
	var targetHost string
	switch reqHdr[3] {
	case socks5AtypIPv4:
		var addr [4]byte
		if _, err := io.ReadFull(clientConn, addr[:]); err != nil {
			return
		}
		targetHost = net.IP(addr[:]).String()
	case socks5AtypDomain:
		var domLen [1]byte
		if _, err := io.ReadFull(clientConn, domLen[:]); err != nil {
			return
		}
		dom := make([]byte, domLen[0])
		if _, err := io.ReadFull(clientConn, dom); err != nil {
			return
		}
		targetHost = string(dom)
	case socks5AtypIPv6:
		var addr [16]byte
		if _, err := io.ReadFull(clientConn, addr[:]); err != nil {
			return
		}
		targetHost = net.IP(addr[:]).String()
	default:
		s.socks5Reply(clientConn, socks5RepFailure)
		return
	}

	var portBytes [2]byte
	if _, err := io.ReadFull(clientConn, portBytes[:]); err != nil {
		return
	}
	targetPort := binary.BigEndian.Uint16(portBytes[:])
	targetAddr := net.JoinHostPort(targetHost, fmt.Sprintf("%d", targetPort))

	// Dial through the proxy protocol.
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	proxyConn, err := s.dialer.DialProxy(ctx, targetAddr)
	if err != nil {
		log.Debug().Err(err).Str("target", targetAddr).
			Str("proto", s.dialer.Protocol()).Msg("proxycore: dial failed")
		s.socks5Reply(clientConn, socks5RepFailure)
		s.failCount.Add(1)
		return
	}
	defer proxyConn.Close()

	// Success.
	s.socks5Reply(clientConn, socks5RepSuccess)
	clientConn.SetDeadline(time.Time{})

	// Bidirectional relay.
	up, down := relay(clientConn, proxyConn)
	s.bytesUp.Add(up)
	s.bytesDown.Add(down)
}

func (s *socks5Server) socks5Reply(conn net.Conn, rep byte) {
	reply := []byte{socks5Version, rep, 0x00, socks5AtypIPv4, 0, 0, 0, 0, 0, 0}
	conn.Write(reply)
}
