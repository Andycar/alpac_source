package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"lampac-go/internal/config"
)

// Payment landing pages. StreamPay (and any other future redirect-based
// gateway) wants browser-facing URLs to send the user to after success/
// failure/cancel. The actual subscription extension happens via the
// signed server-to-server callback (see streampay_api.go); these pages
// are pure UX — they tell the user what happened and how to return to
// Telegram so they don't think the bot is broken.
//
// Endpoints registered (see server.go):
//
//	GET /payment/streampay/success
//	GET /payment/streampay/failure
//	GET /payment/streampay/cancel
//
// We use a shared template renderer (renderLandingPage) so all three
// pages look identical and one stylesheet change updates all of them.

// registerPaymentLandingRoutes wires the StreamPay redirect targets.
// Kept gateway-agnostic: when StreamPay grows a second sibling we'll
// just add three more routes here, no need for a registration framework.
func registerPaymentLandingRoutes(router interface {
	Get(pattern string, h http.HandlerFunc)
}) {
	router.Get("/payment/streampay/success", paymentLandingHandler(landingSuccess))
	router.Get("/payment/streampay/failure", paymentLandingHandler(landingFailure))
	router.Get("/payment/streampay/cancel", paymentLandingHandler(landingCancel))
}

// landingKind selects which copy to render. Kept as an enum (not string)
// so a typo in registerPaymentLandingRoutes would be a compile error.
type landingKind int

const (
	landingSuccess landingKind = iota
	landingFailure
	landingCancel
)

// paymentLandingHandler returns an HTTP handler that renders one of the
// three landing pages. The handler is parameterized by kind so the
// route registration stays declarative.
//
// We avoid embedding the bot username at compile time because the same
// binary serves multiple deployments — instead we read the configured
// bot name at request time from the running config. If the bot isn't
// configured, the page still shows a useful message, just without the
// "open bot" deep link.
func paymentLandingHandler(kind landingKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Pull bot username from the running server if available — used
		// in the "Open bot" deep link. Falls back to a generic Telegram
		// link if the bot isn't wired (development / misconfigured deploy).
		// Zero config → empty BotName → TrimPrefix yields "", matching the old
		// unwired-server fallback exactly.
		botName := strings.TrimPrefix(liveConfig(config.Config{}).TelegramAuth.BotName, "@")

		// CryptoCloud and StreamPay both round-trip our external_id /
		// order id back in the query string. Surface it on the page so
		// support tickets reference the right invoice. We accept either
		// param name — StreamPay's redirects use external_id; CryptoCloud's
		// use order_id.
		invoiceRef := r.URL.Query().Get("external_id")
		if invoiceRef == "" {
			invoiceRef = r.URL.Query().Get("order_id")
		}
		if invoiceRef == "" {
			invoiceRef = r.URL.Query().Get("invoice")
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store") // never cache — payment state shouldn't be sticky
		_, _ = w.Write([]byte(renderLandingPage(kind, botName, invoiceRef)))
	}
}

// landingCopy bundles the per-kind strings — title, accent color, body
// text, primary action label. Kept as a constant table so the renderer
// stays template-y and the copy is searchable as plain strings.
type landingCopy struct {
	Emoji       string
	Title       string
	AccentColor string // CSS color — used for the accent stripe + button bg
	Body        string // HTML allowed (single sanitized source — landingCopy literals)
}

// landingCopies are the source-of-truth strings for the three pages.
// Order matters: indexed by landingKind.
var landingCopies = []landingCopy{
	{
		Emoji:       "✅",
		Title:       "Оплата прошла",
		AccentColor: "#00c864",
		Body: "Спасибо! Подписка активируется автоматически в течение 1–3 минут после подтверждения банка. " +
			"Вернитесь в Telegram — бот пришлёт уведомление, как только всё будет готово.",
	},
	{
		Emoji:       "❌",
		Title:       "Платёж не прошёл",
		AccentColor: "#ff4d4f",
		Body: "Банк отклонил операцию. Это может быть из-за лимита на карте, типа карты или защиты от мошенничества. " +
			"Попробуйте другую карту или вернитесь в Telegram и выберите другой способ оплаты в /pay.",
	},
	{
		Emoji:       "🚫",
		Title:       "Платёж отменён",
		AccentColor: "#fbbf24",
		Body: "Вы закрыли страницу оплаты — деньги <b>не списались</b>. " +
			"Если передумаете, откройте /pay в боте и попробуйте снова.",
	},
}

// renderLandingPage produces a complete, self-contained HTML page. We
// inline everything (CSS, content) so the page works in walled-garden
// browsers (e.g. SBP redirects in airline-Wi-Fi-like networks where
// external CSS may be blocked).
func renderLandingPage(kind landingKind, botName, invoiceRef string) string {
	if int(kind) < 0 || int(kind) >= len(landingCopies) {
		// Defense: a future caller passing a bad enum should still get
		// something rather than a stack trace in the user's browser.
		kind = landingFailure
	}
	c := landingCopies[kind]

	// Deep link to the bot. tg://resolve?domain=... opens the Telegram
	// app directly on iOS/Android; on desktop it's also handled by the
	// installed Telegram client. https://telegram.me/... is the fallback for
	// browsers without TG installed.
	var botLink string
	if botName != "" {
		botLink = "https://telegram.me/" + url.QueryEscape(botName)
	} else {
		botLink = "https://telegram.me/" // generic fallback
	}

	var invoiceLine string
	if invoiceRef != "" {
		// HTML-escape because invoiceRef comes from the URL query and a
		// hostile gateway (or a tampered redirect) could inject markup.
		invoiceLine = fmt.Sprintf(
			`<div class="invoice-ref">№ заказа: <code>%s</code></div>`,
			htmlEscape(invoiceRef),
		)
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s — Lampac</title>
<style>
  :root { color-scheme: dark; }
  html, body { margin:0; padding:0; height:100%%; }
  body {
    background: #0c0e12;
    color: #e7e9ee;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    display: flex; align-items: center; justify-content: center;
    padding: 16px;
  }
  .card {
    background: #161a21;
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: 16px;
    padding: 32px 28px;
    max-width: 440px;
    width: 100%%;
    text-align: center;
    box-shadow: 0 12px 40px rgba(0,0,0,0.4);
  }
  .accent { height: 4px; background: %s; border-radius: 4px; margin: -32px -28px 24px; border-radius: 16px 16px 0 0; }
  .emoji { font-size: 56px; line-height: 1; margin-bottom: 16px; }
  h1 { font-size: 22px; font-weight: 600; margin: 0 0 12px; }
  p { font-size: 14px; line-height: 1.5; color: #b4b7be; margin: 0 0 24px; }
  .btn {
    display: inline-block;
    background: %s; color: #0c0e12;
    font-weight: 600; font-size: 15px;
    padding: 12px 28px; border-radius: 10px;
    text-decoration: none;
    transition: opacity .15s;
  }
  .btn:hover { opacity: 0.9; }
  .invoice-ref {
    margin-top: 20px; font-size: 11px; color: #6b7280;
    font-family: ui-monospace, monospace;
  }
  .invoice-ref code { color: #9ca3af; }
</style>
</head>
<body>
  <main class="card">
    <div class="accent"></div>
    <div class="emoji">%s</div>
    <h1>%s</h1>
    <p>%s</p>
    <a class="btn" href="%s">Вернуться в Telegram</a>
    %s
  </main>
</body>
</html>`,
		c.Title,
		c.AccentColor,
		c.AccentColor,
		c.Emoji,
		c.Title,
		c.Body,
		botLink,
		invoiceLine,
	)
}

// (htmlEscape lives in web_routes.go — re-use the package-level helper.)
