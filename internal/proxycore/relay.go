package proxycore

import (
	"io"
	"net"
	"sync/atomic"
)

// relay copies data bidirectionally between two connections.
// Returns total bytes transferred in each direction.
func relay(a, b net.Conn) (bytesAtoB, bytesBtoA int64) {
	var ab, ba int64
	done := make(chan struct{}, 2)

	cp := func(dst, src net.Conn, counter *int64) {
		n, _ := io.Copy(dst, src)
		atomic.AddInt64(counter, n)
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}

	go cp(b, a, &ab)
	go cp(a, b, &ba)
	<-done
	// The other direction finishes shortly after (RST or FIN).

	return atomic.LoadInt64(&ab), atomic.LoadInt64(&ba)
}

// countingConn wraps a net.Conn and counts bytes read/written.
type countingConn struct {
	net.Conn
	bytesRead    int64
	bytesWritten int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	atomic.AddInt64(&c.bytesRead, int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	atomic.AddInt64(&c.bytesWritten, int64(n))
	return n, err
}
