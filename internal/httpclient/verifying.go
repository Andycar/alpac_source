package httpclient

import (
	"crypto/tls"
	"net/http"
	"time"
)

// VerifyingTransport — копия SharedTransport с НАСТОЯЩЕЙ проверкой сертификата,
// независимо от глобального [security] strict_balancer_tls (на проде он выключен:
// у половины чужих источников битые цепочки).
//
// Нужен для СВОИХ машин с честными Let's Encrypt-сертификатами — TLS-фронтов
// TorrServer-бэкенды пула. Туда в каждом запросе уезжает Basic-auth TorrServer;
// с InsecureSkipVerify TLS защищал бы только от подслушивания, но не от подмены
// сервера на пути — а это ровно тот случай, ради которого API уводится с голого
// HTTP. Диалер, DNS и прочие настройки SharedTransport сохраняются.
func VerifyingTransport() *http.Transport {
	t := SharedTransport.Clone()
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return t
}

// NewVerifying — клиент на VerifyingTransport с таймаутом.
func NewVerifying(timeout time.Duration) *http.Client {
	return &http.Client{Transport: VerifyingTransport(), Timeout: timeout}
}

// NewVerifyingNoRedirect — то же, но без следования редиректам (как NewNoRedirect).
func NewVerifyingNoRedirect(timeout time.Duration) *http.Client {
	return &http.Client{Transport: VerifyingTransport(), Timeout: timeout, CheckRedirect: noRedirect}
}
