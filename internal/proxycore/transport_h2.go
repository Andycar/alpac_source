package proxycore

// HTTP/2 transport for VLESS/Trojan.
// This is a lower-priority transport — most deployments use TCP or WebSocket.
// Placeholder for future implementation.
//
// The approach would be similar to gRPC transport:
// 1. Use existing TLS connection with ALPN "h2".
// 2. Negotiate HTTP/2 settings.
// 3. Open a stream, send HEADERS frame (pseudo-method: POST).
// 4. Frame data in HTTP/2 DATA frames.
