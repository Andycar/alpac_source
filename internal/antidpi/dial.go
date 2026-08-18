package antidpi

import (
	"context"
	"net"
)

// SplitDialer returns a dial function that connects directly to the target
// (not through SOCKS5) and wraps the connection with a TLS ClientHello splitter.
//
// The returned connection intercepts the first Write (expected to be a TLS
// ClientHello record) and splits it into two fragments according to the strategy,
// with a small delay between them to ensure separate TCP segments.
//
// This is useful when the process itself needs DPI bypass without going through
// the SOCKS5 proxy (e.g., for YouTube CDN stream downloads).
func SplitDialer(st Strategy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp4", addr)
		if err != nil {
			return nil, err
		}

		// TCP_NODELAY ensures each Write creates a separate TCP segment.
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetNoDelay(true)
		}

		if st.Name == "none" {
			return conn, nil
		}

		return &splitWriter{
			Conn:     conn,
			strategy: st,
			first:    true,
		}, nil
	}
}
