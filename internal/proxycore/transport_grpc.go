package proxycore

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// upgradeGRPC wraps a TLS/h2 connection with V2Ray gRPC "gun" framing.
//
// The gun protocol opens an HTTP/2 stream with a bidirectional streaming RPC:
//   POST /{serviceName}/Tun   Content-Type: application/grpc
//
// Payload framing on top of DATA frames:
//   1 byte  — compress flag (0x00 = no compression)
//   4 bytes — big-endian message length
//   N bytes — message data
//
// The TLS connection must have negotiated ALPN "h2" before this call.
func upgradeGRPC(ctx context.Context, rawConn net.Conn, host, serviceName string) (net.Conn, error) {
	if serviceName == "" {
		serviceName = "grpc"
	}

	const h2Preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	if _, err := io.WriteString(rawConn, h2Preface); err != nil {
		return nil, fmt.Errorf("grpc: write h2 preface: %w", err)
	}

	framer := http2.NewFramer(rawConn, rawConn)
	framer.SetMaxReadFrameSize(1 << 20)
	framer.AllowIllegalReads = true

	// Send empty client SETTINGS.
	if err := framer.WriteSettings(); err != nil {
		return nil, fmt.Errorf("grpc: write SETTINGS: %w", err)
	}

	// Read and acknowledge server preface (SETTINGS frames).
	if err := grpcReadServerPreface(framer); err != nil {
		return nil, fmt.Errorf("grpc: server preface: %w", err)
	}

	// Build HEADERS block for the gRPC streaming RPC.
	var hbuf bytes.Buffer
	enc := hpack.NewEncoder(&hbuf)
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: ":path", Value: "/" + serviceName + "/Tun"})
	enc.WriteField(hpack.HeaderField{Name: ":scheme", Value: "https"})
	enc.WriteField(hpack.HeaderField{Name: ":authority", Value: host})
	enc.WriteField(hpack.HeaderField{Name: "content-type", Value: "application/grpc"})
	enc.WriteField(hpack.HeaderField{Name: "te", Value: "trailers"})
	enc.WriteField(hpack.HeaderField{Name: "grpc-encoding", Value: "identity"})

	const streamID = uint32(1)
	if err := framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: hbuf.Bytes(),
		EndHeaders:    true,
	}); err != nil {
		return nil, fmt.Errorf("grpc: write HEADERS: %w", err)
	}

	gc := &grpcGunConn{
		raw:      rawConn,
		framer:   framer,
		streamID: streamID,
	}

	// Drain the server's SETTINGS ACK and initial HEADERS (200 OK) in background.
	go gc.drainHeaders()

	return gc, nil
}

// grpcReadServerPreface reads server SETTINGS and sends ACK.
func grpcReadServerPreface(framer *http2.Framer) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f, err := framer.ReadFrame()
		if err != nil {
			return fmt.Errorf("read frame: %w", err)
		}
		sf, ok := f.(*http2.SettingsFrame)
		if !ok {
			continue
		}
		if sf.IsAck() {
			return nil // received ACK to our SETTINGS
		}
		// Server SETTINGS (not ACK) → send ACK.
		if err := framer.WriteSettingsAck(); err != nil {
			return fmt.Errorf("write SETTINGS ACK: %w", err)
		}
	}
	return fmt.Errorf("timeout waiting for server SETTINGS")
}

// grpcGunConn implements net.Conn over an HTTP/2 DATA stream with gRPC message framing.
type grpcGunConn struct {
	raw      net.Conn
	framer   *http2.Framer
	streamID uint32

	wmu  sync.Mutex   // guards framer writes
	rmu  sync.Mutex   // guards reads + rbuf
	rbuf bytes.Buffer // buffered unframed payload
}

func (g *grpcGunConn) Write(b []byte) (int, error) {
	// Wrap payload in a gRPC message frame.
	frame := make([]byte, 5+len(b))
	frame[0] = 0x00 // no compression
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(b)))
	copy(frame[5:], b)

	g.wmu.Lock()
	defer g.wmu.Unlock()
	if err := g.framer.WriteData(g.streamID, false, frame); err != nil {
		return 0, fmt.Errorf("grpc write DATA: %w", err)
	}
	return len(b), nil
}

func (g *grpcGunConn) Read(b []byte) (int, error) {
	g.rmu.Lock()
	defer g.rmu.Unlock()

	for g.rbuf.Len() == 0 {
		if err := g.readNextDataFrame(); err != nil {
			return 0, err
		}
	}
	return g.rbuf.Read(b)
}

// readNextDataFrame reads H2 frames until data is available in rbuf.
func (g *grpcGunConn) readNextDataFrame() error {
	for {
		frame, err := g.framer.ReadFrame()
		if err != nil {
			return fmt.Errorf("grpc read frame: %w", err)
		}

		switch f := frame.(type) {
		case *http2.DataFrame:
			data := f.Data()
			if len(data) < 5 {
				continue // incomplete gRPC frame header, skip
			}
			msgLen := binary.BigEndian.Uint32(data[1:5])
			if int(msgLen) > len(data)-5 {
				return fmt.Errorf("grpc: message length %d exceeds frame data %d", msgLen, len(data)-5)
			}
			g.rbuf.Write(data[5 : 5+msgLen])
			return nil

		case *http2.RSTStreamFrame:
			return fmt.Errorf("grpc: RST_STREAM error=%d", f.ErrCode)

		case *http2.GoAwayFrame:
			return fmt.Errorf("grpc: GOAWAY error=%d last_stream=%d", f.ErrCode, f.LastStreamID)

		case *http2.PingFrame:
			if !f.IsAck() {
				g.wmu.Lock()
				_ = g.framer.WritePing(true, f.Data)
				g.wmu.Unlock()
			}

		case *http2.SettingsFrame:
			if !f.IsAck() {
				g.wmu.Lock()
				_ = g.framer.WriteSettingsAck()
				g.wmu.Unlock()
			}

		case *http2.WindowUpdateFrame:
			// flow control — server is allowing us to send more; nothing to do

		case *http2.HeadersFrame:
			// 200 OK response headers — already read in drainHeaders, safe to ignore here
		}
	}
}

// drainHeaders reads the initial response HEADERS (200 OK) and any ACKs
// that arrive before the first Read call.
func (g *grpcGunConn) drainHeaders() {
	g.rmu.Lock()
	defer g.rmu.Unlock()
	for {
		frame, err := g.framer.ReadFrame()
		if err != nil {
			return
		}
		switch f := frame.(type) {
		case *http2.HeadersFrame:
			_ = f
			return // response headers received, hand off to Read
		case *http2.SettingsFrame:
			if !f.IsAck() {
				g.wmu.Lock()
				_ = g.framer.WriteSettingsAck()
				g.wmu.Unlock()
			}
		case *http2.PingFrame:
			if !f.IsAck() {
				g.wmu.Lock()
				_ = g.framer.WritePing(true, f.Data)
				g.wmu.Unlock()
			}
		case *http2.WindowUpdateFrame:
			// ignore
		}
	}
}

func (g *grpcGunConn) Close() error                       { return g.raw.Close() }
func (g *grpcGunConn) LocalAddr() net.Addr                { return g.raw.LocalAddr() }
func (g *grpcGunConn) RemoteAddr() net.Addr               { return g.raw.RemoteAddr() }
func (g *grpcGunConn) SetDeadline(t time.Time) error      { return g.raw.SetDeadline(t) }
func (g *grpcGunConn) SetReadDeadline(t time.Time) error  { return g.raw.SetReadDeadline(t) }
func (g *grpcGunConn) SetWriteDeadline(t time.Time) error { return g.raw.SetWriteDeadline(t) }
