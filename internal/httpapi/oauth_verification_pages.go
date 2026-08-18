package httpapi

import (
	"fmt"
	"net/http"
	"strings"
)

// Public homepage + privacy policy pages.
//
// These exist to satisfy Google's OAuth consent-screen verification for the
// YouTube `youtube.readonly` scope (see internal/ytauth). Google's reviewer
// visits the "Application home page" and "Privacy policy" URLs configured in
// the OAuth consent screen and requires that:
//
//   - the homepage is publicly reachable (NO login wall) and links to the
//     privacy policy;
//   - the privacy policy is publicly reachable, on the same domain, and
//     discloses how the app accesses/uses/stores/shares Google user data,
//     including the Google API Services User Data Policy "Limited Use"
//     commitment.
//
// The app root ("/") redirects unauthenticated users to Telegram auth
// (see lampaIndexHandler), which a Google reviewer cannot pass — hence these
// dedicated public routes. Point the consent screen at:
//
//	Application home page: https://<your-domain>/about
//	Privacy policy:        https://<your-domain>/privacy
//
// Both pages are self-contained (inline CSS) so they render even where an
// upstream proxy strips external assets, and they read the live brand name so
// the copy stays consistent with the app's actual branding (Google checks
// name/branding consistency).

// oauthVerificationContactEmail is the developer contact surfaced in the
// privacy policy. Google requires a reachable contact for data questions.
const oauthVerificationContactEmail = "dayzbanned@gmail.com"

// registerOAuthVerificationRoutes wires the public homepage + privacy policy.
func registerOAuthVerificationRoutes(router interface {
	Get(pattern string, h http.HandlerFunc)
}) {
	router.Get("/about", aboutPageHandler)
	router.Get("/privacy", privacyPageHandler)
}

// brandNameForPublicPages returns the live brand name, falling back to a
// sensible default when branding hasn't been customized.
func brandNameForPublicPages() string {
	if n := strings.TrimSpace(publicBrandName()); n != "" {
		return n
	}
	return "Alpac TV"
}

func aboutPageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write([]byte(renderAboutPage(brandNameForPublicPages())))
}

func privacyPageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write([]byte(renderPrivacyPage(brandNameForPublicPages())))
}

// publicPageCSS is shared by both pages so a single style tweak updates both.
// Kept dark to match the app; light-readable enough for a reviewer.
const publicPageCSS = `
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  html, body { margin:0; padding:0; }
  body {
    background: #0c0e12; color: #e7e9ee;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    line-height: 1.6; padding: 32px 16px;
  }
  .wrap { max-width: 760px; margin: 0 auto; }
  header.brand { display:flex; align-items:center; gap:12px; margin-bottom: 28px; }
  header.brand .logo {
    width: 44px; height: 44px; border-radius: 12px;
    background: linear-gradient(135deg,#7c5cff,#4dd0ff);
    display:flex; align-items:center; justify-content:center;
    font-size: 22px; font-weight: 700; color:#0c0e12;
  }
  header.brand .name { font-size: 20px; font-weight: 700; }
  h1 { font-size: 26px; margin: 0 0 8px; }
  h2 { font-size: 18px; margin: 28px 0 8px; color:#e7e9ee; }
  p, li { color:#c5c8d0; font-size: 15px; }
  a { color:#7cb6ff; }
  .lead { font-size: 17px; color:#d7dae1; }
  .card {
    background:#161a21; border:1px solid rgba(255,255,255,0.08);
    border-radius: 14px; padding: 20px 22px; margin: 18px 0;
  }
  .limited-use { border-left: 3px solid #7c5cff; padding-left: 14px; margin: 16px 0; }
  code { background:#20242d; padding:2px 6px; border-radius:6px; font-size: 13px; }
  footer { margin-top: 40px; padding-top: 20px; border-top:1px solid rgba(255,255,255,0.08); color:#8a8f98; font-size: 13px; }
  .cta { display:inline-block; margin-top: 8px; background:#7c5cff; color:#0c0e12; font-weight:600; text-decoration:none; padding:10px 20px; border-radius:10px; }
`

func renderAboutPage(brand string) string {
	b := htmlEscape(brand)
	initial := "A"
	if brand != "" {
		initial = htmlEscape(strings.ToUpper(brand[:1]))
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s — медиацентр</title>
<style>%s</style>
</head>
<body>
<div class="wrap">
  <header class="brand"><div class="logo">%s</div><div class="name">%s</div></header>

  <h1>%s</h1>
  <p class="lead">%s — персональный медиацентр: единый интерфейс для просмотра фильмов, сериалов, IPTV и видео с YouTube на телевизорах, приставках и в браузере.</p>

  <div class="card">
    <h2>Интеграция с YouTube</h2>
    <p>После того как вы подключите свой аккаунт Google, %s показывает <b>ваши</b> данные YouTube внутри приложения: список ваших подписок, ленту новых видео от каналов, на которые вы подписаны, и ваши плейлисты. Доступ запрашивается только на <b>чтение</b> (scope <code>youtube.readonly</code>) и используется исключительно для отображения этого контента вам. Мы не публикуем, не изменяем и не удаляем ничего в вашем аккаунте YouTube.</p>
  </div>

  <h2>Конфиденциальность</h2>
  <p>Мы бережно относимся к вашим данным. Подробно о том, какие данные Google мы получаем, как их используем и храним, — в нашей политике конфиденциальности:</p>
  <p><a class="cta" href="/privacy">Политика конфиденциальности</a></p>

  <footer>
    © %s. Связь: <a href="mailto:%s">%s</a> · <a href="/privacy">Политика конфиденциальности</a>
  </footer>
</div>
</body>
</html>`,
		b, publicPageCSS, initial, b,
		b, b, b, b,
		oauthVerificationContactEmail, oauthVerificationContactEmail,
	)
}

func renderPrivacyPage(brand string) string {
	b := htmlEscape(brand)
	initial := "A"
	if brand != "" {
		initial = htmlEscape(strings.ToUpper(brand[:1]))
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Политика конфиденциальности — %s</title>
<style>%s</style>
</head>
<body>
<div class="wrap">
  <header class="brand"><div class="logo">%s</div><div class="name">%s</div></header>

  <h1>Политика конфиденциальности</h1>
  <p class="lead">Настоящая политика описывает, какие данные приложение %s получает от сервисов Google, как использует, хранит и защищает их.</p>

  <h2>1. Какие данные Google мы получаем</h2>
  <p>Если вы решите подключить свой аккаунт Google/YouTube, приложение запрашивает доступ <b>только на чтение</b> к данным YouTube через scope <code>https://www.googleapis.com/auth/youtube.readonly</code>. В рамках этого доступа мы получаем:</p>
  <ul>
    <li>список каналов, на которые вы подписаны;</li>
    <li>ленту недавних видео от этих каналов;</li>
    <li>ваши плейлисты YouTube и их содержимое;</li>
    <li>название вашего YouTube-канала (для отображения, чей аккаунт подключён).</li>
  </ul>
  <p>Мы <b>не</b> запрашиваем доступ на запись и не получаем ваш пароль — авторизация проходит на стороне Google по протоколу OAuth 2.0.</p>

  <h2>2. Как мы используем эти данные</h2>
  <p>Полученные данные YouTube используются <b>исключительно</b> для того, чтобы показать вам ваши подписки, ленту и плейлисты внутри приложения %s на вашем устройстве. Мы не используем их для рекламы, профилирования или каких-либо иных целей.</p>

  <h2>3. Хранение и защита данных</h2>
  <p>Для поддержания подключения к YouTube мы храним на сервере OAuth-токены доступа и обновления, привязанные к вашему аккаунту в приложении. Это конфиденциальные учётные данные, и мы применяем следующие меры их защиты:</p>
  <ul>
    <li><b>Шифрование при передаче.</b> Весь обмен данными между вашим устройством, нашим сервером и API Google идёт только по защищённому соединению HTTPS (TLS). Незашифрованные соединения не используются.</li>
    <li><b>Шифрование при хранении.</b> Токены доступа и обновления хранятся на диске в зашифрованном виде, алгоритм AES-256-GCM. Ключ шифрования хранится отдельно от самих данных в файле с ограниченными правами доступа.</li>
    <li><b>Ограничение доступа.</b> Файлы с токенами и ключом шифрования доступны только той системной учётной записи, от имени которой работает сервис (права 0600 в каталоге 0700). Доступ к серверу имеет только администратор сервиса.</li>
    <li><b>Минимизация хранимых данных.</b> Содержимое ваших подписок, ленты и плейлистов <b>не сохраняется</b> в постоянную базу данных: оно запрашивается у API Google по мере необходимости и находится только в оперативной памяти в виде кратковременного кэша (5–10 минут), который исчезает при перезапуске сервиса.</li>
  </ul>
  <p><i>Security summary (EN): all data is transmitted over HTTPS/TLS only. OAuth access and refresh tokens are encrypted at rest using AES-256-GCM, with the encryption key stored separately and file access restricted to the service account. YouTube content data (subscriptions, feeds, playlists) is never written to persistent storage — it is only held in an in-memory cache for a few minutes.</i></p>

  <h2>4. Передача третьим лицам</h2>
  <p>Мы <b>не продаём, не передаём и не раскрываем</b> данные, полученные от Google API, третьим лицам. Данные не используются и не передаются для показа рекламы, а также не передаются брокерам данных.</p>

  <div class="limited-use">
    <h2>5. Ограниченное использование (Limited Use)</h2>
    <p>%s's use and transfer of information received from Google APIs to any other app will adhere to the <a href="https://developers.google.com/terms/api-services-user-data-policy" target="_blank" rel="noopener">Google API Services User Data Policy</a>, including the Limited Use requirements.</p>
    <p>То есть: использование и передача приложением %s информации, полученной от Google API, соответствует Политике Google в отношении пользовательских данных API-сервисов, включая требования Ограниченного использования (Limited Use).</p>
  </div>

  <h2>6. Срок хранения, отзыв доступа и удаление данных</h2>
  <p>Токены хранятся только до тех пор, пока подключение к YouTube активно. Мы не храним их дольше, чем это нужно для работы функции, и удаляем при любом из следующих действий:</p>
  <ul>
    <li>вы отключаете YouTube командой <code>/youtube_unbind</code> в нашем Telegram-боте — токены немедленно стираются с диска;</li>
    <li>вы отзываете доступ на странице <a href="https://myaccount.google.com/permissions" target="_blank" rel="noopener">Разрешения аккаунта Google</a> — при первой же попытке обновления Google сообщает нам об отзыве, и мы автоматически удаляем сохранённые токены;</li>
    <li>вы присылаете запрос на удаление на <a href="mailto:%s">%s</a> — мы удаляем данные вручную.</li>
  </ul>
  <p>Данные YouTube (подписки, ленты, плейлисты) отдельного удаления не требуют: они не сохраняются на диск, а кратковременный кэш в оперативной памяти очищается сам.</p>
  <p><i>Retention and deletion (EN): tokens are kept only while the YouTube connection is active. They are erased from disk when you unbind in our Telegram bot, automatically when we detect that you revoked access in your Google Account, or on request by email. YouTube content data is never persisted.</i></p>

  <h2>7. Контакты</h2>
  <p>По любым вопросам о конфиденциальности и обработке данных: <a href="mailto:%s">%s</a>.</p>

  <footer>© %s · <a href="/about">О приложении</a></footer>
</div>
</body>
</html>`,
		b, publicPageCSS, initial, b,
		b,    // lead
		b,    // section 2
		b, b, // limited use (English brand + Russian brand)
		oauthVerificationContactEmail, oauthVerificationContactEmail, // revoke
		oauthVerificationContactEmail, oauthVerificationContactEmail, // contacts
		b, // footer
	)
}
