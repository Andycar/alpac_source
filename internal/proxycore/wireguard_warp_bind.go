package proxycore

import (
	"golang.zx2c4.com/wireguard/conn"
)

// warpBind wraps a regular conn.Bind to inject the 3-byte WARP "reserved"
// header field present in every WireGuard UDP packet to/from a Cloudflare
// WARP endpoint.
//
// Standard WireGuard packet layout:
//
//	byte 0:    message type (1=initiation, 2=response, 3=cookie, 4=transport)
//	bytes 1-3: reserved (zero in stock WireGuard)
//	bytes 4+:  rest
//
// WARP reuses bytes 1-3 to carry a per-client "client_id" returned during
// device registration. Stock wireguard-go ignores any non-zero bytes there
// on receive and always sends zero on send, so the WARP server drops our
// packets. We fix it by:
//
//   - On Send:    overwriting bytes 1-3 with the reserved value
//   - On Receive: zeroing bytes 1-3 so wireguard-go's parser is happy
type warpBind struct {
	inner    conn.Bind
	reserved [3]byte
}

func newWARPBind(inner conn.Bind, reserved []int) *warpBind {
	wb := &warpBind{inner: inner}
	for i := 0; i < 3 && i < len(reserved); i++ {
		wb.reserved[i] = byte(reserved[i] & 0xff)
	}
	return wb
}

func (b *warpBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actualPort, err := b.inner.Open(port)
	if err != nil {
		return nil, 0, err
	}
	wrapped := make([]conn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		innerFn := fn
		wrapped[i] = func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
			n, err := innerFn(packets, sizes, eps)
			// Zero the reserved bytes on incoming packets so wireguard-go
			// accepts them. We don't need to validate the echo — stock WG
			// only checks message type.
			for j := 0; j < n; j++ {
				if sizes[j] >= 4 {
					packets[j][1] = 0
					packets[j][2] = 0
					packets[j][3] = 0
				}
			}
			return n, err
		}
	}
	return wrapped, actualPort, nil
}

func (b *warpBind) Close() error             { return b.inner.Close() }
func (b *warpBind) SetMark(m uint32) error   { return b.inner.SetMark(m) }
func (b *warpBind) BatchSize() int           { return b.inner.BatchSize() }
func (b *warpBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	return b.inner.ParseEndpoint(s)
}

func (b *warpBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	for _, buf := range bufs {
		if len(buf) >= 4 {
			buf[1] = b.reserved[0]
			buf[2] = b.reserved[1]
			buf[3] = b.reserved[2]
		}
	}
	return b.inner.Send(bufs, ep)
}
