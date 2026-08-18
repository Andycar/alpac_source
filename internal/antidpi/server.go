package antidpi

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
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

	connectTimeout = 10 * time.Second
	splitDelay     = 50 * time.Millisecond // delay between fragments to prevent Nagle merging
)

// Server is a lightweight SOCKS5 proxy that applies TLS ClientHello splitting
// for whitelisted hosts to bypass DPI-based blocking.
type Server struct {
	listenAddr string
	hosts      []string // suffix-matched whitelist

	strategy  atomic.Value // stores Strategy
	proberCtx context.Context
	proberFn  context.CancelFunc

	mu       sync.Mutex
	listener net.Listener
	wg       sync.WaitGroup
	stopped  bool

	// Stats
	activeConns atomic.Int64
	totalConns  atomic.Int64
	splitConns  atomic.Int64

	// Prober state
	lastProbe     atomic.Value // time.Time
	lastProbeOK   atomic.Bool
	probeStrategy string // configured strategy name ("auto" or explicit)
}

// New creates a new AntiDPI SOCKS5 proxy server.
// strategyName is "auto", "split-1", "split-2", "split-sni", or "none".
func New(listenAddr, strategyName string, hosts []string) *Server {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:9898"
	}
	if len(hosts) == 0 {
		hosts = DefaultHosts
	}

	s := &Server{
		listenAddr:    listenAddr,
		hosts:         hosts,
		probeStrategy: strategyName,
	}

	// Set initial strategy (auto will be resolved by prober later).
	if strategyName == "auto" || strategyName == "" {
		s.strategy.Store(StrategySplit1) // safe default until prober runs
	} else {
		s.strategy.Store(ParseStrategy(strategyName))
	}
	s.lastProbe.Store(time.Time{})

	return s
}

// Start begins listening for SOCKS5 connections.
// If strategy is "auto", also starts the background prober.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("antidpi: listen %s: %w", s.listenAddr, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.stopped = false
	s.mu.Unlock()

	log.Info().
		Str("addr", ln.Addr().String()).
		Str("strategy", s.Strategy().Name).
		Int("hosts", len(s.hosts)).
		Msg("antidpi: SOCKS5 proxy started")

	// Start prober if auto mode.
	if s.probeStrategy == "auto" || s.probeStrategy == "" {
		s.proberCtx, s.proberFn = context.WithCancel(context.Background())
		s.wg.Add(1)
		go s.probeLoop(s.proberCtx)
	}

	s.wg.Add(1)
	go s.acceptLoop(ln)

	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	ln := s.listener
	s.mu.Unlock()

	if s.proberFn != nil {
		s.proberFn()
	}
	if ln != nil {
		ln.Close()
	}
	s.wg.Wait()
	log.Info().Msg("antidpi: stopped")
}

// Addr returns the actual listen address (useful when port is 0).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.listenAddr
}

// Strategy returns the current active strategy.
func (s *Server) Strategy() Strategy {
	if v := s.strategy.Load(); v != nil {
		return v.(Strategy)
	}
	return StrategySplit1
}

// SetStrategy changes the active strategy.
func (s *Server) SetStrategy(st Strategy) {
	s.strategy.Store(st)
	log.Info().Str("strategy", st.Name).Msg("antidpi: strategy changed")
}

// LastProbeTime returns the time of the last probe attempt.
func (s *Server) LastProbeTime() time.Time {
	if v := s.lastProbe.Load(); v != nil {
		return v.(time.Time)
	}
	return time.Time{}
}

// LastProbeOK returns true if the last probe was successful.
func (s *Server) LastProbeOK() bool {
	return s.lastProbeOK.Load()
}

// Stats returns server statistics.
func (s *Server) Stats() map[string]any {
	return map[string]any{
		"active_conns": s.activeConns.Load(),
		"total_conns":  s.totalConns.Load(),
		"split_conns":  s.splitConns.Load(),
		"strategy":     s.Strategy().Name,
		"last_probe":   s.LastProbeTime().Format(time.RFC3339),
		"last_probe_ok": s.LastProbeOK(),
	}
}

func (s *Server) acceptLoop(ln net.Listener) {
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
			log.Warn().Err(err).Msg("antidpi: accept error")
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

// handleConn processes a single SOCKS5 connection.
func (s *Server) handleConn(clientConn net.Conn) {
	defer clientConn.Close()

	// SOCKS5 handshake: method selection.
	clientConn.SetDeadline(time.Now().Add(10 * time.Second))

	// Read version + nMethods.
	var hdr [2]byte
	if _, err := io.ReadFull(clientConn, hdr[:]); err != nil {
		return
	}
	if hdr[0] != socks5Version {
		return
	}
	nMethods := int(hdr[1])
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(clientConn, methods); err != nil {
		return
	}

	// Accept no-auth.
	if _, err := clientConn.Write([]byte{socks5Version, socks5NoAuth}); err != nil {
		return
	}

	// Read SOCKS5 request.
	// VER(1) CMD(1) RSV(1) ATYP(1)
	var reqHdr [4]byte
	if _, err := io.ReadFull(clientConn, reqHdr[:]); err != nil {
		return
	}
	if reqHdr[0] != socks5Version || reqHdr[1] != socks5CmdConnect {
		s.socks5Reply(clientConn, socks5RepNotAllow)
		return
	}

	// Parse destination address.
	var targetHost string
	var targetPort uint16

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

	// Read port (2 bytes, big-endian).
	var portBytes [2]byte
	if _, err := io.ReadFull(clientConn, portBytes[:]); err != nil {
		return
	}
	targetPort = binary.BigEndian.Uint16(portBytes[:])

	targetAddr := net.JoinHostPort(targetHost, fmt.Sprintf("%d", targetPort))

	// Connect to target.
	targetConn, err := net.DialTimeout("tcp4", targetAddr, connectTimeout)
	if err != nil {
		log.Debug().Err(err).Str("target", targetAddr).Msg("antidpi: connect failed")
		s.socks5Reply(clientConn, socks5RepFailure)
		return
	}
	defer targetConn.Close()

	// Set TCP_NODELAY on target to prevent Nagle from merging our split fragments.
	if tc, ok := targetConn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	// Send success reply.
	s.socks5Reply(clientConn, socks5RepSuccess)
	clientConn.SetDeadline(time.Time{}) // clear deadline

	// Check if we should apply DPI bypass for this host.
	shouldSplit := MatchHost(strings.ToLower(targetHost), s.hosts)

	if shouldSplit {
		s.relayWithSplit(clientConn, targetConn, targetHost)
	} else {
		relay(clientConn, targetConn)
	}
}

// relayWithSplit reads the first packet from client, applies TLS splitting,
// then relays the rest bidirectionally.
func (s *Server) relayWithSplit(clientConn, targetConn net.Conn, host string) {
	// Read the first packet (expected: TLS ClientHello).
	clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 16384)
	n, err := clientConn.Read(buf)
	clientConn.SetReadDeadline(time.Time{})
	if err != nil {
		return
	}
	firstPacket := buf[:n]

	st := s.Strategy()

	if isTLSClientHello(firstPacket) && st.Name != "none" {
		// TLS Record Fragmentation: rewrite ClientHello into multiple valid
		// TLS records with SNI split across record boundaries.
		if st.Name == "split-tlsrec" || st.Name == "split-tlsrec-40" {
			var fragments [][]byte
			var delay time.Duration
			if st.Name == "split-tlsrec-40" {
				fragments = FragmentTLSRecordChunked(firstPacket, 40)
				delay = 10 * time.Millisecond
			} else {
				fragments = FragmentTLSRecord(firstPacket)
				delay = splitDelay
			}
			if fragments != nil {
				for i, frag := range fragments {
					if i > 0 {
						time.Sleep(delay)
					}
					if _, err := targetConn.Write(frag); err != nil {
						return
					}
				}
				s.splitConns.Add(1)
				log.Debug().
					Str("host", host).
					Str("strategy", st.Name).
					Int("fragments", len(fragments)).
					Int("hello_size", len(firstPacket)).
					Msg("antidpi: TLS record fragmented")
			} else {
				if _, err := targetConn.Write(firstPacket); err != nil {
					return
				}
			}
		} else {
			splitPos := st.SplitPos(firstPacket)
			if splitPos > 0 && splitPos < len(firstPacket) {
				// Send fragment 1.
				if _, err := targetConn.Write(firstPacket[:splitPos]); err != nil {
					return
				}
				// Small delay to force separate TCP segments.
				time.Sleep(splitDelay)
				// Send fragment 2.
				if _, err := targetConn.Write(firstPacket[splitPos:]); err != nil {
					return
				}
				s.splitConns.Add(1)
				log.Debug().
					Str("host", host).
					Str("strategy", st.Name).
					Int("split_pos", splitPos).
					Int("hello_size", len(firstPacket)).
					Msg("antidpi: TLS ClientHello split")
			} else {
				// Split pos invalid — send as-is.
				if _, err := targetConn.Write(firstPacket); err != nil {
					return
				}
			}
		}
	} else {
		// Not TLS or strategy is none — send as-is.
		if _, err := targetConn.Write(firstPacket); err != nil {
			return
		}
	}

	// Bidirectional relay for the rest.
	relay(clientConn, targetConn)
}

// relay copies data bidirectionally between two connections.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		// Signal that one direction is done — close write on dst
		// so the other side gets EOF.
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(b, a)
	go cp(a, b)
	<-done // wait for at least one direction to finish
	// The other direction will finish shortly after (RST or FIN).
}

// socks5Reply sends a SOCKS5 reply with the given status.
func (s *Server) socks5Reply(conn net.Conn, rep byte) {
	// VER(1) REP(1) RSV(1) ATYP(1) BND.ADDR(4) BND.PORT(2)
	reply := []byte{socks5Version, rep, 0x00, socks5AtypIPv4, 0, 0, 0, 0, 0, 0}
	conn.Write(reply)
}
