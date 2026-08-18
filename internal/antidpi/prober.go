package antidpi

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	probeTimeout  = 5 * time.Second  // TLS handshake timeout per strategy
	probeInterval = 30 * time.Minute // re-probe interval
	probeTarget   = "www.youtube.com:443"
)

// probeLoop runs the auto-strategy prober periodically.
func (s *Server) probeLoop(ctx context.Context) {
	defer s.wg.Done()

	// Initial probe immediately.
	s.runProbe(ctx)

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runProbe(ctx)
		}
	}
}

// runProbe tests all strategies in order and picks the first one that works.
func (s *Server) runProbe(ctx context.Context) {
	s.lastProbe.Store(time.Now())

	for _, st := range ProbeOrder {
		if ctx.Err() != nil {
			return
		}
		ok := probeStrategy(ctx, probeTarget, st)
		if ok {
			old := s.Strategy()
			s.strategy.Store(st)
			s.lastProbeOK.Store(true)
			if old.Name != st.Name {
				log.Info().
					Str("old", old.Name).
					Str("new", st.Name).
					Msg("antidpi: prober selected new strategy")
			} else {
				log.Debug().Str("strategy", st.Name).Msg("antidpi: prober confirmed strategy")
			}
			return
		}
		log.Debug().Str("strategy", st.Name).Msg("antidpi: prober strategy failed")
	}

	// All strategies failed — YouTube might not be blocked, or network is down.
	// Fall back to split-1 as the safest default.
	s.lastProbeOK.Store(false)
	log.Warn().Msg("antidpi: all probe strategies failed, falling back to split-1")
	s.strategy.Store(StrategySplit1)
}

// probeStrategy tests a single strategy by performing a TLS handshake
// to the target host using the given splitting approach.
func probeStrategy(ctx context.Context, target string, st Strategy) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}

	// Dial TCP.
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp4", target)
	if err != nil {
		return false
	}
	defer conn.Close()

	// Set TCP_NODELAY for precise segment control.
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	// Create TLS client but we'll handle the first write ourselves.
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})

	// For split strategies, we need to intercept the ClientHello.
	// Unfortunately, tls.Client.Handshake() writes the ClientHello internally.
	// We need a wrapper that splits the first write.
	if st.Name != "none" {
		// Use a splitting wrapper around the raw conn.
		splitConn := &splitWriter{
			Conn:     conn,
			strategy: st,
			first:    true,
		}
		tlsConn = tls.Client(splitConn, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
		})
	}

	// Set deadline from context.
	deadline, ok := ctx.Deadline()
	if ok {
		tlsConn.SetDeadline(deadline)
	}

	// Attempt handshake.
	if err := tlsConn.Handshake(); err != nil {
		return false
	}
	tlsConn.Close()

	return true
}

// splitWriter wraps a net.Conn and splits the first Write (TLS ClientHello)
// into two fragments according to the strategy.
type splitWriter struct {
	net.Conn
	strategy Strategy
	first    bool
}

func (sw *splitWriter) Write(data []byte) (int, error) {
	if !sw.first {
		return sw.Conn.Write(data)
	}
	sw.first = false

	if !isTLSClientHello(data) || sw.strategy.Name == "none" {
		return sw.Conn.Write(data)
	}

	// TLS Record Fragmentation: rewrite one TLS record into multiple valid
	// TLS records with the SNI split across record boundaries.
	if sw.strategy.Name == "split-tlsrec" || sw.strategy.Name == "split-tlsrec-40" {
		var fragments [][]byte
		var delay time.Duration
		if sw.strategy.Name == "split-tlsrec-40" {
			fragments = FragmentTLSRecordChunked(data, 40)
			delay = 10 * time.Millisecond
		} else {
			fragments = FragmentTLSRecord(data)
			delay = splitDelay
		}
		if fragments == nil {
			return sw.Conn.Write(data)
		}
		total := 0
		for i, frag := range fragments {
			if i > 0 {
				time.Sleep(delay)
			}
			n, err := sw.Conn.Write(frag)
			total += n
			if err != nil {
				return total, err
			}
		}
		return len(data), nil // report original size to TLS layer
	}

	pos := sw.strategy.SplitPos(data)
	if pos <= 0 || pos >= len(data) {
		return sw.Conn.Write(data)
	}

	// Write first fragment.
	n1, err := sw.Conn.Write(data[:pos])
	if err != nil {
		return n1, err
	}

	// Delay to ensure separate TCP segments.
	time.Sleep(splitDelay)

	// Write second fragment.
	n2, err := sw.Conn.Write(data[pos:])
	return n1 + n2, err
}
