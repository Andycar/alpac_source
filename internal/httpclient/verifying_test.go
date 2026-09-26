package httpclient

import (
	"crypto/tls"
	"testing"
)

// VerifyingTransport обязан проверять сертификат независимо от того, что стоит в
// SharedTransport (на проде там InsecureSkipVerify=true): туда уезжает Basic-auth
// TorrServer, и TLS без проверки защищал бы только от подслушивания, не от подмены.
func TestVerifyingTransportVerifiesCerts(t *testing.T) {
	tr := VerifyingTransport()
	if tr.TLSClientConfig == nil {
		t.Fatal("нет TLS-конфига")
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("проверка сертификата выключена")
	}
	if tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("минимальная версия TLS %x, ожидалась ≥ 1.2", tr.TLSClientConfig.MinVersion)
	}
	// Копия, а не сам SharedTransport: правка не должна ослаблять/менять общий транспорт.
	if tr == SharedTransport {
		t.Fatal("вернулся сам SharedTransport вместо копии")
	}
	if SharedTransport.TLSClientConfig != nil && tr.TLSClientConfig == SharedTransport.TLSClientConfig {
		t.Fatal("TLS-конфиг разделён с SharedTransport")
	}
	// Диалер и прокси наследуются: иначе клиент к своим фронтам обошёл бы antidpi/DNS-настройки.
	if (SharedTransport.DialContext == nil) != (tr.DialContext == nil) {
		t.Fatal("диалер не унаследован от SharedTransport")
	}
}

func TestNewVerifyingClients(t *testing.T) {
	c := NewVerifying(5)
	if c.Transport == nil || c.Timeout != 5 {
		t.Fatalf("NewVerifying: %+v", c)
	}
	nr := NewVerifyingNoRedirect(7)
	if nr.CheckRedirect == nil || nr.Timeout != 7 {
		t.Fatalf("NewVerifyingNoRedirect: редиректы должны быть выключены, таймаут 7: %+v", nr)
	}
}
