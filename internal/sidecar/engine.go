// Package sidecar manages proxy sidecar processes (xray, mihomo) as SOCKS5 proxies.
package sidecar

// EngineType identifies a proxy engine binary.
type EngineType string

const (
	EngineXray      EngineType = "xray"
	EngineMihomo    EngineType = "mihomo"
	EngineProxyCore EngineType = "proxycore"
)

// Engine abstracts a proxy sidecar binary (xray or mihomo).
type Engine interface {
	// Type returns the engine type.
	Type() EngineType

	// EnsureBinary downloads the engine binary if needed into binDir.
	// Returns the full path to the executable.
	EnsureBinary(binDir string) (binPath string, err error)

	// GenerateConfig produces a config file for the engine.
	// Returns the config data, file extension ("json" or "yaml"), and any error.
	GenerateConfig(out ProxyOutbound, socksPort int) (data []byte, ext string, err error)

	// StartArgs returns the CLI arguments to start the process with the config file.
	StartArgs(binPath, configPath string) []string
}

// ProxyOutbound holds parsed proxy parameters regardless of protocol.
// Fields are populated based on the protocol — unused fields remain zero-valued.
type ProxyOutbound struct {
	Protocol string // "vless", "vmess", "trojan", "ss", "hysteria2", "tuic", "wireguard"

	// Common fields
	Server      string
	Port        int
	UUID        string // VLESS, VMess, TUIC
	Password    string // Trojan, SS, Hysteria2, TUIC
	Encryption  string // VLESS encryption / SS cipher
	Flow        string // VLESS xtls flow
	Security    string // "tls", "reality", "none"
	SNI         string
	Fingerprint string
	Network     string // "tcp", "ws", "grpc", "h2"
	Path        string // WS path / gRPC serviceName
	Host        string // WS host header
	ALPN        string

	// REALITY fields
	PublicKey string
	ShortID   string
	PQV       string // post-quantum value

	// VMess-specific
	AlterID int

	// Hysteria2-specific
	Obfs         string
	ObfsPassword string
	UpMbps       int
	DownMbps     int

	// TUIC-specific
	CongestionCtrl string // "bbr", "cubic"
	UDPRelayMode   string // "native", "quic"

	// WireGuard-specific
	PrivateKey    string
	PeerPublicKey string
	PreSharedKey  string
	LocalAddr     []string // e.g. ["172.16.0.2/32", "fd01::.../128"]
	MTU           int
	Reserved      []int // WARP reserved bytes
}

// DetectEngine returns the default engine for a given protocol.
func DetectEngine(protocol string) EngineType {
	switch protocol {
	case "vless", "vmess", "trojan":
		return EngineXray
	case "ss", "hysteria2", "tuic", "wireguard":
		return EngineMihomo
	case "socks5", "http":
		return EngineProxyCore
	default:
		return EngineXray
	}
}
