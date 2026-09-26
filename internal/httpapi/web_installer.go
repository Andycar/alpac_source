package httpapi

import (
	"net/http"

	"lampac-go/internal/tgauth"
)

// installerPageHandler serves the installer SPA.
func installerPageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, http.StatusOK, installerHTML)
	}
}

// installerBlockedHandler returns 403 when setup is already done.
func installerBlockedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, http.StatusForbidden, installerBlockedHTML)
	}
}

// registerInstallerRoutes adds /web/installer routes to the router.
func registerInstallerRoutes(router interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
}, cfg interface{ SetupDone() bool }, pwStore **tgauth.PasswordAuthStore) {
	router.Get("/web/installer", installerPageHandler())
	router.Get("/web/installer/api/defaults", installerDefaultsHandler())
	router.Post("/web/installer/api/apply", installerApplyHandler(pwStore))
	router.Post("/web/installer/api/validate", installerValidateHandler())
}

const installerBlockedHTML = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Installer Disabled</title>
<style>body{background:#161b23;color:#e2e8f0;font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.box{text-align:center;padding:40px;border-radius:16px;background:#1a2332;box-shadow:0 4px 24px rgba(0,0,0,.4)}
h1{color:#06b6d4;margin-bottom:12px}p{color:#94a3b8}</style></head>
<body><div class="box"><h1>403</h1><p>Установщик уже завершен и заблокирован.</p><p>Installer has been completed and is now disabled.</p></div></body></html>`

// --- Installer SPA HTML ---

const installerHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Al(co)pac Setup</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=Montserrat:wght@400;500;600;700&display=swap');
*{margin:0;padding:0;box-sizing:border-box}
:root{--accent:#06b6d4;--accent-dark:#0d9488;--accent-hover:#0891b2;--bg:#161b23;--surface:#1a2332;--surface2:#1e2736;--surface3:#11161d;--text:#e2e8f0;--text-muted:#94a3b8;--text-dim:#64748b;--border:rgba(255,255,255,0.06);--danger:#ff6b6b;--warning:#f0c040;--success:#34d399;--radius:12px}
::-webkit-scrollbar{width:6px}::-webkit-scrollbar-track{background:transparent}::-webkit-scrollbar-thumb{background:linear-gradient(180deg,var(--accent-dark),var(--accent));border-radius:3px}
body{font-family:'Montserrat',sans-serif;background:var(--bg);color:var(--text);min-height:100vh;display:flex;flex-direction:column;align-items:center}

/* Header */
.installer-header{width:100%;padding:20px 0;text-align:center;border-bottom:1px solid var(--border);background:var(--surface)}
.installer-header h1{font-size:22px;color:var(--accent);font-weight:700;letter-spacing:-.3px}
.installer-header p{font-size:13px;color:var(--text-muted);margin-top:4px}

/* Progress bar */
.progress-bar{display:flex;align-items:center;gap:0;padding:24px 40px;width:100%;max-width:900px;margin:0 auto;overflow-x:auto}
.progress-step{display:flex;align-items:center;flex-shrink:0}
.step-circle{width:32px;height:32px;border-radius:50%;background:var(--surface2);border:2px solid var(--border);display:flex;align-items:center;justify-content:center;font-size:13px;font-weight:600;color:var(--text-dim);transition:all .3s;cursor:pointer;position:relative}
.step-circle:hover{border-color:var(--accent);color:var(--text-muted)}
.step-circle.active{background:var(--accent);border-color:var(--accent);color:#fff;box-shadow:0 0 12px rgba(6,182,212,.35)}
.step-circle.done{background:var(--accent-dark);border-color:var(--accent-dark);color:#fff}
.step-circle.done::after{content:'\2713';font-size:14px}
.step-line{width:40px;height:2px;background:var(--border);flex-shrink:0;transition:background .3s}
.step-line.done{background:var(--accent-dark)}
.step-label{position:absolute;top:38px;font-size:9px;color:var(--text-dim);white-space:nowrap;text-align:center}
.step-circle.active .step-label{color:var(--accent)}

/* Container */
.wizard-container{width:100%;max-width:800px;padding:0 20px 40px}
.step-content{display:none;animation:fadeIn .3s ease}
.step-content.active{display:block}
@keyframes fadeIn{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:translateY(0)}}

/* Cards */
.card{background:var(--surface);border:1px solid var(--border);border-radius:var(--radius);padding:24px;margin-bottom:16px}
.card h2{font-size:16px;font-weight:600;margin-bottom:4px;color:var(--text)}
.card h3{font-size:14px;font-weight:600;margin:16px 0 8px;color:var(--accent)}
.card p.desc{font-size:13px;color:var(--text-muted);margin-bottom:16px;line-height:1.5}

/* Form elements */
.form-group{margin-bottom:14px}
.form-group label{display:block;font-size:12px;font-weight:600;color:var(--text-muted);margin-bottom:5px;text-transform:uppercase;letter-spacing:.5px}
.form-group input,.form-group select,.form-group textarea{width:100%;padding:10px 14px;background:var(--surface2);border:1px solid var(--border);border-radius:8px;color:var(--text);font-size:14px;font-family:inherit;transition:border-color .2s}
.form-group input:focus,.form-group select:focus,.form-group textarea:focus{outline:none;border-color:var(--accent)}
.form-group input::placeholder{color:var(--text-dim)}
.form-group .hint{font-size:11px;color:var(--text-dim);margin-top:3px}
.form-group .error{font-size:11px;color:var(--danger);margin-top:3px;display:none}
.form-group.has-error input{border-color:var(--danger)}
.form-group.has-error .error{display:block}
.form-row{display:grid;grid-template-columns:1fr 1fr;gap:14px}
@media(max-width:600px){.form-row{grid-template-columns:1fr}}

/* Toggle */
.toggle-row{display:flex;align-items:center;gap:10px;padding:8px 0}
.toggle-row .toggle-label{font-size:13px;color:var(--text);flex:1}
.toggle-row .toggle-desc{font-size:11px;color:var(--text-dim)}
.toggle{position:relative;width:44px;height:24px;flex-shrink:0}
.toggle input{opacity:0;width:0;height:0}
.toggle .slider{position:absolute;inset:0;background:var(--surface2);border:1px solid var(--border);border-radius:12px;cursor:pointer;transition:all .2s}
.toggle .slider::before{content:'';position:absolute;width:18px;height:18px;left:2px;top:2px;background:var(--text-dim);border-radius:50%;transition:all .2s}
.toggle input:checked+.slider{background:var(--accent);border-color:var(--accent)}
.toggle input:checked+.slider::before{transform:translateX(20px);background:#fff}

/* Toggle grid */
.toggle-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:6px 16px}

/* Balancer cards */
.balancer-group{margin-bottom:20px}
.balancer-group h3{font-size:13px;color:var(--accent);text-transform:uppercase;letter-spacing:1px;margin-bottom:10px;padding-bottom:6px;border-bottom:1px solid var(--border)}
.balancer-card{background:var(--surface2);border:1px solid var(--border);border-radius:10px;padding:14px;margin-bottom:8px;transition:border-color .2s}
.balancer-card.enabled{border-color:rgba(6,182,212,.3)}
.balancer-card .bc-header{display:flex;align-items:center;gap:10px;margin-bottom:8px}
.balancer-card .bc-name{font-size:14px;font-weight:600;flex:1}
.balancer-card .bc-status{font-size:10px;padding:2px 8px;border-radius:10px;background:var(--surface3);color:var(--text-dim)}
.balancer-card .bc-status.working{background:rgba(52,211,153,.15);color:var(--success)}
.balancer-card .bc-status.dead{background:rgba(255,107,107,.15);color:var(--danger)}
.balancer-card .bc-fields{display:none;margin-top:10px}
.balancer-card.enabled .bc-fields{display:block}

/* Buttons */
.btn-row{display:flex;gap:12px;justify-content:flex-end;margin-top:24px}
.btn{padding:10px 28px;border:none;border-radius:10px;font-family:inherit;font-size:14px;font-weight:600;cursor:pointer;transition:all .2s}
.btn-primary{background:var(--accent);color:#fff}
.btn-primary:hover{background:var(--accent-hover);box-shadow:0 0 12px rgba(6,182,212,.3)}
.btn-secondary{background:var(--surface2);color:var(--text-muted);border:1px solid var(--border)}
.btn-secondary:hover{color:var(--text);border-color:var(--text-dim)}
.btn-skip{background:transparent;color:var(--text-dim);border:1px solid transparent}
.btn-skip:hover{color:var(--text-muted)}
.btn:disabled{opacity:.5;cursor:not-allowed}

/* Summary */
.summary-section{margin-bottom:16px}
.summary-section h3{font-size:13px;color:var(--accent);margin-bottom:8px}
.summary-row{display:flex;justify-content:space-between;padding:4px 0;font-size:13px;border-bottom:1px solid var(--border)}
.summary-row .sk{color:var(--text-muted)}.summary-row .sv{color:var(--text);font-weight:500}
.summary-row .sv.masked{color:var(--text-dim);font-style:italic}

/* Language picker */
.lang-picker{display:flex;gap:12px;justify-content:center;margin:16px 0}
.lang-btn{padding:10px 24px;border:2px solid var(--border);border-radius:10px;background:var(--surface2);color:var(--text);font-family:inherit;font-size:14px;font-weight:600;cursor:pointer;transition:all .2s}
.lang-btn:hover{border-color:var(--accent);color:var(--accent)}
.lang-btn.active{border-color:var(--accent);background:rgba(6,182,212,.1);color:var(--accent)}

/* Success screen */
.success-screen{text-align:center;padding:60px 20px}
.success-screen .check{font-size:64px;color:var(--success);margin-bottom:16px}
.success-screen h2{font-size:20px;margin-bottom:8px}
.success-screen p{color:var(--text-muted);font-size:14px;margin-bottom:24px}
.success-screen a{color:var(--accent);text-decoration:none;font-weight:600;font-size:16px}
.success-screen a:hover{text-decoration:underline}

/* Collapsible */
.collapsible{cursor:pointer;display:flex;align-items:center;gap:8px;padding:10px 0;user-select:none}
.collapsible::before{content:'\25B6';font-size:10px;color:var(--text-dim);transition:transform .2s}
.collapsible.open::before{transform:rotate(90deg)}
.collapsible-body{display:none;padding:0 0 12px}
.collapsible.open+.collapsible-body{display:block}

/* Spinner overlay */
.spinner-overlay{display:none;position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:1000;align-items:center;justify-content:center;backdrop-filter:blur(4px)}
.spinner-overlay.show{display:flex}
.spinner{width:48px;height:48px;border:4px solid var(--surface2);border-top-color:var(--accent);border-radius:50%;animation:spin 1s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}

/* Password strength */
.pw-strength{height:4px;border-radius:2px;margin-top:6px;background:var(--surface2);overflow:hidden}
.pw-strength .bar{height:100%;border-radius:2px;transition:width .3s,background .3s}
</style>
</head>
<body>

<div class="installer-header">
  <h1>Al(co)pac Setup</h1>
  <p id="headerDesc"></p>
</div>

<div class="progress-bar" id="progressBar"></div>

<div class="wizard-container" id="wizard"></div>

<div class="spinner-overlay" id="spinner"><div class="spinner"></div></div>

<script>
(function(){
'use strict';

// ========== i18n ==========
const L = {
  ru: {
    headerDesc: 'Настройка сервера за несколько минут',
    steps: ['Язык','Пароль','Telegram','Сервер','Источники','Прокси','TorrServer','Плагины','Дополнительно','Итог'],
    next: 'Далее', prev: 'Назад', skip: 'Пропустить', apply: 'Применить', start: 'Начать',
    welcomeTitle: 'Добро пожаловать!',
    welcomeDesc: 'Этот мастер поможет вам настроить сервер Al(co)pac. Все настройки можно будет изменить позже в админ-панели.',
    chooseLang: 'Выберите язык интерфейса:',
    // Step 2
    pwTitle: 'Пароль администратора',
    pwDesc: 'Установите пароль для доступа к админ-панели.',
    pwLabel: 'Пароль', pwConfirm: 'Подтверждение', pwMismatch: 'Пароли не совпадают', pwWeak: 'Минимум 6 символов',
    // Step 3
    tgTitle: 'Telegram бот',
    tgDesc: 'Подключите бота для авторизации пользователей и управления сервером. Создайте бота через @BotFather.',
    tgEnable: 'Включить Telegram авторизацию',
    tgToken: 'Токен бота', tgAdminId: 'Telegram ID администратора', tgBotName: 'Имя бота',
    tgMaxDevices: 'Макс. устройств', tgAutoApprove: 'Авто-подтверждение',
    tgTokenHint: 'Получите у @BotFather', tgAdminHint: 'Узнайте у @userinfobot',
    tgValidate: 'Проверить', tgValid: 'Бот найден', tgInvalid: 'Неверный токен',
    // Step 4
    srvTitle: 'Настройки сервера',
    srvDesc: 'Основные параметры работы сервера.',
    srvAddr: 'Адрес и порт', srvAddrHint: 'Например :18118 или 0.0.0.0:8080',
    srvDns: 'Использовать системный DNS', tmdbMode: 'TMDB прокси', tmdbKey: 'TMDB API ключ',
    // Step 5
    srcTitle: 'Источники контента',
    srcDesc: 'Включите нужные балансеры и укажите токены. Источники без токенов работают бесплатно.',
    srcPopular: 'Популярные', srcFree: 'Бесплатные', srcAnime: 'Аниме', srcUA: 'Украинские', srcOther: 'Другие',
    srcCheckSearch: 'Проверять доступность при поиске',
    // Step 6
    proxyTitle: 'Прокси',
    proxyDesc: 'Настройте прокси если часть источников заблокирована в вашем регионе.',
    proxyNeed: 'Нужен прокси', proxyVlessUri: 'VLESS URI', proxyBalancers: 'Балансеры (через запятую)',
    proxyAddEntry: '+ Добавить прокси',
    proxyTgTitle: 'Прокси для Telegram',
    proxyTgDesc: 'Telegram заблокирован в РФ. Настройте прокси для бота.',
    proxyTgEnable: 'Проксировать Telegram', proxyTgUri: 'VLESS / SOCKS5 URI',
    proxyTgTest: 'Проверить', proxyTgOk: 'Telegram доступен через прокси',
    proxyTgFail: 'Telegram недоступен через прокси',
    // Step 7
    tsTitle: 'TorrServer',
    tsDesc: 'Встроенный торрент-клиент для стриминга через торренты.',
    tsEnable: 'Включить TorrServer', tsPort: 'Порт', tsLogin: 'Логин', tsPassword: 'Пароль',
    tsMode: 'Режим', tsModeBuiltin: 'Встроенный', tsModeExternal: 'Внешний',
    tsCache: 'Кеширование', tsCacheRAM: 'RAM кэш (МБ)', tsCacheDisk: 'Дисковый кэш (МБ)',
    tsPreload: 'Предзагрузка (МБ)', tsDisableDHT: 'Отключить DHT', tsDisableUpload: 'Отключить раздачу',
    tsURL: 'URL TorrServer', tsURLHint: 'http://127.0.0.1:8090', tsTest: 'Проверить',
    // Step 8
    plugTitle: 'Плагины и функции',
    plugDesc: 'Выберите, какие плагины будут доступны пользователям.',
    plugCub: 'Интеграция с Cub.red', plugCubDomain: 'Домен Cub',
    plugIptv: 'IPTV плейлисты',
    // Step 9
    advTitle: 'Дополнительно',
    advDesc: 'Продвинутые настройки. Раскройте нужный раздел.',
    advYt: 'YouTube OAuth', advDlna: 'DLNA', advTranscoding: 'Транскодирование',
    advBrowser: 'Пул браузеров', advObservability: 'Мониторинг', advAntiDpi: 'AntiDPI',
    advKinopoisk: 'Кинопоиск', advSkipIntro: 'Пропуск заставок', advCalendar: 'Календарь',
    advCluster: 'Кластер', advLlm: 'AI / LLM', advCollections: 'Коллекции',
    advYtTimeout: 'Таймаут извлечения (сек)',
    advXsearch: 'Поиск по источникам', advOpensubs: 'OpenSubtitles',
    // Step 10
    sumTitle: 'Итог',
    sumDesc: 'Проверьте настройки перед применением. После сохранения установщик будет заблокирован.',
    sumPassword: 'Пароль администратора', sumSet: 'Установлен', sumNotSet: 'Не задан',
    sumTelegram: 'Telegram бот', sumEnabled: 'Включено', sumDisabled: 'Выключено',
    sumServer: 'Сервер', sumSources: 'Источники', sumProxy: 'Прокси',
    sumTorrServer: 'TorrServer', sumPlugins: 'Плагины',
    // Success
    successTitle: 'Готово!',
    successDesc: 'Сервер настроен. Переходите в админ-панель для управления.',
    successLink: 'Открыть админ-панель',
    successCountdown: 'Перенаправление через',
    successSec: 'сек.'
  },
  en: {
    headerDesc: 'Set up your server in a few minutes',
    steps: ['Language','Password','Telegram','Server','Sources','Proxy','TorrServer','Plugins','Advanced','Summary'],
    next: 'Next', prev: 'Back', skip: 'Skip', apply: 'Apply', start: 'Start',
    welcomeTitle: 'Welcome!',
    welcomeDesc: 'This wizard will help you configure your Al(co)pac server. All settings can be changed later in the admin panel.',
    chooseLang: 'Choose interface language:',
    pwTitle: 'Admin Password',
    pwDesc: 'Set a password to access the admin panel.',
    pwLabel: 'Password', pwConfirm: 'Confirm', pwMismatch: 'Passwords do not match', pwWeak: 'Minimum 6 characters',
    tgTitle: 'Telegram Bot',
    tgDesc: 'Connect a bot for user authorization and server management. Create a bot via @BotFather.',
    tgEnable: 'Enable Telegram auth',
    tgToken: 'Bot token', tgAdminId: 'Admin Telegram ID', tgBotName: 'Bot name',
    tgMaxDevices: 'Max devices', tgAutoApprove: 'Auto-approve',
    tgTokenHint: 'Get from @BotFather', tgAdminHint: 'Get from @userinfobot',
    tgValidate: 'Validate', tgValid: 'Bot found', tgInvalid: 'Invalid token',
    srvTitle: 'Server Settings',
    srvDesc: 'Core server parameters.',
    srvAddr: 'Address & Port', srvAddrHint: 'e.g. :18118 or 0.0.0.0:8080',
    srvDns: 'Use system DNS', tmdbMode: 'TMDB proxy', tmdbKey: 'TMDB API key',
    srcTitle: 'Content Sources',
    srcDesc: 'Enable the balancers you need and provide tokens. Sources without tokens are free.',
    srcPopular: 'Popular', srcFree: 'Free', srcAnime: 'Anime', srcUA: 'Ukrainian', srcOther: 'Other',
    srcCheckSearch: 'Check availability on search',
    proxyTitle: 'Proxy',
    proxyDesc: 'Set up a proxy if some sources are blocked in your region.',
    proxyNeed: 'Need proxy', proxyVlessUri: 'VLESS URI', proxyBalancers: 'Balancers (comma separated)',
    proxyAddEntry: '+ Add proxy',
    proxyTgTitle: 'Telegram Proxy',
    proxyTgDesc: 'Telegram is blocked in some regions. Set up a proxy for the bot.',
    proxyTgEnable: 'Proxy Telegram', proxyTgUri: 'VLESS / SOCKS5 URI',
    proxyTgTest: 'Test', proxyTgOk: 'Telegram reachable via proxy',
    proxyTgFail: 'Telegram unreachable via proxy',
    tsTitle: 'TorrServer',
    tsDesc: 'Built-in torrent client for streaming via torrents.',
    tsEnable: 'Enable TorrServer', tsPort: 'Port', tsLogin: 'Login', tsPassword: 'Password',
    tsMode: 'Mode', tsModeBuiltin: 'Built-in', tsModeExternal: 'External',
    tsCache: 'Caching', tsCacheRAM: 'RAM cache (MB)', tsCacheDisk: 'Disk cache (MB)',
    tsPreload: 'Preload (MB)', tsDisableDHT: 'Disable DHT', tsDisableUpload: 'Disable upload',
    tsURL: 'TorrServer URL', tsURLHint: 'http://127.0.0.1:8090', tsTest: 'Test connection',
    plugTitle: 'Plugins & Features',
    plugDesc: 'Choose which plugins will be available to users.',
    plugCub: 'Cub.red integration', plugCubDomain: 'Cub domain',
    plugIptv: 'IPTV playlists',
    advTitle: 'Advanced',
    advDesc: 'Advanced settings. Expand the section you need.',
    advYt: 'YouTube OAuth', advDlna: 'DLNA', advTranscoding: 'Transcoding',
    advBrowser: 'Browser Pool', advObservability: 'Monitoring', advAntiDpi: 'AntiDPI',
    advKinopoisk: 'Kinopoisk', advSkipIntro: 'Skip Intros', advCalendar: 'Calendar',
    advCluster: 'Cluster', advLlm: 'AI / LLM', advCollections: 'Collections',
    advYtTimeout: 'Extract timeout (sec)',
    advXsearch: 'Cross-search', advOpensubs: 'OpenSubtitles',
    sumTitle: 'Summary',
    sumDesc: 'Review your settings before applying. The installer will be disabled after saving.',
    sumPassword: 'Admin password', sumSet: 'Set', sumNotSet: 'Not set',
    sumTelegram: 'Telegram bot', sumEnabled: 'Enabled', sumDisabled: 'Disabled',
    sumServer: 'Server', sumSources: 'Sources', sumProxy: 'Proxy',
    sumTorrServer: 'TorrServer', sumPlugins: 'Plugins',
    successTitle: 'Done!',
    successDesc: 'Server is configured. Head to the admin panel to manage it.',
    successLink: 'Open Admin Panel',
    successCountdown: 'Redirecting in',
    successSec: 'sec.'
  },
  ua: {
    headerDesc: 'Налаштування сервера за кілька хвилин',
    steps: ['Мова','Пароль','Telegram','Сервер','Джерела','Проксі','TorrServer','Плагіни','Додатково','Підсумок'],
    next: 'Далі', prev: 'Назад', skip: 'Пропустити', apply: 'Застосувати', start: 'Почати',
    welcomeTitle: 'Ласкаво просимо!',
    welcomeDesc: 'Цей майстер допоможе налаштувати сервер Al(co)pac. Усі параметри можна змінити пізніше в адмін-панелі.',
    chooseLang: 'Оберіть мову інтерфейсу:',
    pwTitle: 'Пароль адміністратора',
    pwDesc: 'Встановіть пароль для доступу до адмін-панелі.',
    pwLabel: 'Пароль', pwConfirm: 'Підтвердження', pwMismatch: 'Паролі не збігаються', pwWeak: 'Мінімум 6 символів',
    tgTitle: 'Telegram бот',
    tgDesc: 'Підключіть бота для авторизації користувачів. Створіть бота через @BotFather.',
    tgEnable: 'Увімкнути Telegram авторизацію',
    tgToken: 'Токен бота', tgAdminId: 'Telegram ID адміністратора', tgBotName: "Ім'я бота",
    tgMaxDevices: 'Макс. пристроїв', tgAutoApprove: 'Авто-підтвердження',
    tgTokenHint: 'Отримайте у @BotFather', tgAdminHint: 'Дізнайтесь у @userinfobot',
    tgValidate: 'Перевірити', tgValid: 'Бот знайдений', tgInvalid: 'Невірний токен',
    srvTitle: 'Налаштування сервера',
    srvDesc: 'Основні параметри роботи сервера.',
    srvAddr: 'Адреса та порт', srvAddrHint: 'Наприклад :18118 або 0.0.0.0:8080',
    srvDns: 'Використовувати системний DNS', tmdbMode: 'TMDB проксі', tmdbKey: 'TMDB API ключ',
    srcTitle: 'Джерела контенту',
    srcDesc: 'Увімкніть потрібні балансери та вкажіть токени. Джерела без токенів працюють безкоштовно.',
    srcPopular: 'Популярні', srcFree: 'Безкоштовні', srcAnime: 'Аніме', srcUA: 'Українські', srcOther: 'Інші',
    srcCheckSearch: 'Перевіряти доступність при пошуку',
    proxyTitle: 'Проксі',
    proxyDesc: 'Налаштуйте проксі якщо частина джерел заблокована у вашому регіоні.',
    proxyNeed: 'Потрібен проксі', proxyVlessUri: 'VLESS URI', proxyBalancers: 'Балансери (через кому)',
    proxyAddEntry: '+ Додати проксі',
    proxyTgTitle: 'Проксі для Telegram',
    proxyTgDesc: 'Telegram заблоковано в деяких регіонах. Налаштуйте проксі для бота.',
    proxyTgEnable: 'Проксувати Telegram', proxyTgUri: 'VLESS / SOCKS5 URI',
    proxyTgTest: 'Перевірити', proxyTgOk: 'Telegram доступний через проксі',
    proxyTgFail: 'Telegram недоступний через проксі',
    tsTitle: 'TorrServer',
    tsDesc: 'Вбудований торрент-клієнт для стрімінгу через торренти.',
    tsEnable: 'Увімкнути TorrServer', tsPort: 'Порт', tsLogin: 'Логін', tsPassword: 'Пароль',
    tsMode: 'Режим', tsModeBuiltin: 'Вбудований', tsModeExternal: 'Зовнішній',
    tsCache: 'Кешування', tsCacheRAM: 'RAM кеш (МБ)', tsCacheDisk: 'Дисковий кеш (МБ)',
    tsPreload: 'Передзавантаження (МБ)', tsDisableDHT: 'Вимкнути DHT', tsDisableUpload: 'Вимкнути роздачу',
    tsURL: 'URL TorrServer', tsURLHint: 'http://127.0.0.1:8090', tsTest: 'Перевірити',
    plugTitle: 'Плагіни та функції',
    plugDesc: 'Оберіть, які плагіни будуть доступні користувачам.',
    plugCub: 'Інтеграція з Cub.red', plugCubDomain: 'Домен Cub',
    plugIptv: 'IPTV плейлисти',
    advTitle: 'Додатково',
    advDesc: 'Розширені налаштування. Розкрийте потрібний розділ.',
    advYt: 'YouTube OAuth', advDlna: 'DLNA', advTranscoding: 'Транскодування',
    advBrowser: 'Пул браузерів', advObservability: 'Моніторинг', advAntiDpi: 'AntiDPI',
    advKinopoisk: 'Кінопошук', advSkipIntro: 'Пропуск заставок', advCalendar: 'Календар',
    advCluster: 'Кластер', advLlm: 'AI / LLM', advCollections: 'Колекції',
    advYtTimeout: 'Таймаут витягування (сек)',
    advXsearch: 'Пошук по джерелах', advOpensubs: 'OpenSubtitles',
    sumTitle: 'Підсумок',
    sumDesc: 'Перевірте налаштування перед застосуванням. Після збереження установщик буде заблоковано.',
    sumPassword: 'Пароль адміністратора', sumSet: 'Встановлено', sumNotSet: 'Не задано',
    sumTelegram: 'Telegram бот', sumEnabled: 'Увімкнено', sumDisabled: 'Вимкнено',
    sumServer: 'Сервер', sumSources: 'Джерела', sumProxy: 'Проксі',
    sumTorrServer: 'TorrServer', sumPlugins: 'Плагіни',
    successTitle: 'Готово!',
    successDesc: 'Сервер налаштовано. Переходьте в адмін-панель для керування.',
    successLink: 'Відкрити адмін-панель',
    successCountdown: 'Перенаправлення через',
    successSec: 'сек.'
  }
};

let lang = 'ru';
let currentStep = 0;
const totalSteps = 10;
let wizardData = {};
let defaults = {};

function t(key) { return (L[lang] && L[lang][key]) || (L.ru[key]) || key; }

// ========== Init ==========
async function init() {
  try {
    const r = await fetch('/web/installer/api/defaults');
    if (r.ok) defaults = await r.json();
  } catch(e) { console.warn('defaults load failed', e); }
  // Pre-fill all fields from existing config (defaults) so tokens/hosts survive reinstall.
  var d = defaults;
  var tg = d.telegram || {};
  var srv = d.server || {};
  var tmdb = d.tmdb_proxy || {};
  var ol = d.online || {};
  var ts = d.torrserver || {};
  var pr = d.proxy || {};
  var vless = pr.vless || {};
  var wp = d.web?.plugins || {};
  var cub = d.cub || {};
  var iptv = d.iptv || {};
  var yt = d.youtube || {};
  var yto = d.youtube_oauth || {};
  var dl = d.dlna || {};
  var tr = d.transcoding || {};
  var bp = d.browser_pool || {};
  var obs = d.observability || {};
  var adpi = d.antidpi || {};
  var kp = d.kinopoisk || {};
  var si = d.skip_intro || {};
  var cal = d.calendar || {};
  var cl = d.cluster || {};
  var llm = d.llm || {};
  var os = d.opensubs || {};

  wizardData = {
    admin: { password: d.admin?.password || '' },
    telegram: {
      enable: tg.enable || false,
      bot_token: tg.bot_token || '',
      admin_id: tg.admin_id || 0,
      bot_name: tg.bot_name || '',
      max_devices_per_user: tg.max_devices_per_user || 3,
      auto_approve: tg.auto_approve || false
    },
    server: { addr: srv.addr || ':18118', system_dns: srv.system_dns || false },
    tmdb_proxy: { mode: tmdb.mode || 'self', api_key: tmdb.api_key || '' },
    online: (function() {
      var o = { check_online_search: ol.check_online_search !== false };
      // Pre-enable balancers that have config data (host/token/login).
      var allBals = [].concat.apply([], Object.values(BALANCERS));
      for (var i = 0; i < allBals.length; i++) {
        var bid = allBals[i].id;
        if (ol[bid]) {
          var src = ol[bid];
          var hasData = src.host || src.token || src.login || src.password || src.api_host;
          o[bid] = Object.assign({_enabled: !!hasData}, src);
        }
      }
      return o;
    })(),
    proxy: { vless: { entries: (vless.entries || []).map(function(e){ return {uri:e.uri||'',balancers:(e.balancers||[]).join(', ')}; }) } },
    torrserver: {
      port: ts.port || 8090, url: ts.url || '',
      login: ts.login || '', password: ts.password || '',
      cache_size_mb: ts.cache_size_mb || 64, disk_cache_mb: ts.disk_cache_mb || 1024,
      preload_mb: ts.preload_mb || 5,
      disable_dht: ts.disable_dht || false,
      disable_upload: ts.disable_upload !== false
    },
    web: { plugins: wp },
    cub: { enable: cub.enable || false, domain: cub.domain || '' },
    iptv: { enable: iptv.enable || false },
    youtube: { proxy: yt.proxy || 'auto', extract_timeout: yt.extract_timeout || 60 },
    youtube_oauth: { client_id: yto.client_id || '', client_secret: yto.client_secret || '' },
    dlna: { enable: dl.enable || false, path: dl.path || '', friendly_name: dl.friendly_name || '' },
    transcoding: { enable: tr.enable || false, ffmpeg: tr.ffmpeg || 'ffmpeg', max_concurrent_jobs: tr.max_concurrent_jobs || 5 },
    browser_pool: { max_concurrent: bp.max_concurrent || 3 },
    observability: { access_log: obs.access_log || false, log_level: obs.log_level || '' },
    antidpi: { enable: adpi.enable || false, strategy: adpi.strategy || 'auto' },
    kinopoisk: { token: kp.token || '' },
    skip_intro: { enable: si.enable || false },
    calendar: { enable: cal.enable || false },
    cluster: { enable: cl.enable || false, mode: cl.mode || 'primary', api_key: cl.api_key || '' },
    llm: { endpoint: llm.endpoint || '', api_key: llm.api_key || '', model: llm.model || '' },
    collections: d.collections || {},
    xsearch: { enable: d.xsearch?.enable !== false },
    opensubs: { enable: os.enable || false, api_key: os.api_key || '' },
    _ts_enable: !!(ts.port || ts.url),
    _ts_mode: ts.url ? 'external' : 'builtin',
    _proxy_enable: (vless.entries || []).length > 0
  };
  render();
}

// ========== Balancer definitions ==========
const BALANCERS = {
  popular: [
    { id:'filmix', name:'Filmix', fields:[{k:'token',l:'Token'},{k:'tokens',l:'Tokens',type:'text',hint:'comma separated'}], status:'working' },
    { id:'kinopub', name:'KinoPub', fields:[{k:'token',l:'Token'}], status:'working' },
    { id:'rezka', name:'PidoRezka', fields:[{k:'host',l:'Host'},{k:'login',l:'Login'},{k:'password',l:'Password',type:'password'}] },
    { id:'collaps', name:'Collaps', fields:[{k:'token',l:'Token',def:'eedefb541aeba871dcfc756e6b31c02e'}], status:'working' }
  ],
  free: [
    { id:'kinotochka', name:'Kinotochka', fields:[], status:'working' },
    { id:'ashdi', name:'Ashdi', fields:[], status:'working' },
    { id:'videoseed', name:'Videoseed', fields:[{k:'token',l:'Token'}], status:'working' },
    { id:'zetflix', name:'Zetflix', fields:[], status:'working' },
    { id:'redheadsound', name:'RedHeadSound', fields:[{k:'login',l:'Login'},{k:'password',l:'Password',type:'password'}], status:'working' },
    { id:'eneyida', name:'Eneyida', fields:[], status:'working' },
    { id:'fancdn', name:'FanCDN', fields:[], status:'working' },
    { id:'mirage', name:'Mirage (4K)', fields:[{k:'token',l:'Token'}], status:'working' },
    { id:'aladdin', name:'Aladdin', fields:[{k:'token',l:'Token'}], status:'working' }
  ],
  anime: [
    { id:'anilibria', name:'Anilibria', fields:[] },
    { id:'animego', name:'AnimeGo', fields:[] },
    { id:'animelib', name:'AnimeLib', fields:[{k:'token',l:'Token'}] },
    { id:'moonanime', name:'MoonAnime', fields:[{k:'token',l:'Token',def:'865fEF-E2e1Bc-2ca431-e6A150-780DFD-737C6B'}] },
    { id:'unimay', name:'Unimay', fields:[] },
    { id:'animeon', name:'AnimeON', fields:[] }
  ],
  ua: [
    { id:'kinoukr', name:'KinoUkr', fields:[], status:'working' },
    { id:'uaflix', name:'Uaflix', fields:[{k:'login',l:'Login'},{k:'passwd',l:'Password',type:'password'}] },
    { id:'bamboo', name:'Bamboo', fields:[] },
    { id:'starlight', name:'StarLight', fields:[] },
    { id:'mikai', name:'Mikai', fields:[] }
  ],
  other: [
    { id:'kodik', name:'Kodik', fields:[{k:'token',l:'Token',def:'41dd95f84c21719b09d6c71182237a25'}] },
    { id:'hdvb', name:'HDVB', fields:[{k:'token',l:'Token',def:'5e2fe4c70bafd9a7414c4f170ee1b192'}] },
    { id:'lumex', name:'Lumex', fields:[{k:'token',l:'Token'},{k:'client_id',l:'Client ID'}] },
    { id:'videocdn', name:'VideoCDN', fields:[{k:'token',l:'Token'}] },
    { id:'alloha', name:'Alloha', fields:[{k:'token',l:'Token'}] },
    { id:'veoveo', name:'VeoVeo', fields:[] },
    { id:'pidtor', name:'AlcoTor', fields:[{k:'enable',l:'Enable',type:'toggle'},{k:'redapi',l:'RedAPI',def:'https://jacred.stream'},{k:'apikey',l:'API Key',def:'pp'}] },
    { id:'vokino', name:'Vokino', fields:[{k:'token',l:'Token'}] },
    { id:'vibix', name:'Vibix', fields:[{k:'token',l:'Token'}] },
    { id:'iframe_video', name:'IframeVideo', fields:[{k:'token',l:'Token'}] },
    { id:'getstv', name:'GetsTV', fields:[{k:'token',l:'Token'}] }
  ]
};

const PLUGINS = [
  { id:'dlna', name:'DLNA' }, { id:'tracks', name:'Tracks' }, { id:'transcoding', name:'Transcoding' },
  { id:'tmdb_proxy', name:'TMDB Proxy' }, { id:'online', name:'Online' }, { id:'catalog', name:'Catalog' },
  { id:'sisi', name:'SISI (18+)' }, { id:'torrserver', name:'TorrServer' }, { id:'backup', name:'Backup' },
  { id:'sync', name:'Sync' }, { id:'bookmark', name:'Bookmark' }, { id:'timecode', name:'Timecode' },
  { id:'ads_free', name:'Ads Free' }, { id:'youtube_feed', name:'YouTube Feed' }, { id:'stats', name:'Stats' },
  { id:'opensubs', name:'OpenSubtitles' }, { id:'failover', name:'Failover' }, { id:'dualsubs', name:'DualSubs' },
  { id:'player_redesign', name:'Player Redesign' }, { id:'hls_tracks', name:'HLS Tracks' },
  { id:'voice_switcher', name:'Voice Switcher' }, { id:'web_player', name:'Web Player' },
  { id:'theme', name:'Theme' }, { id:'screensaver', name:'Screensaver' }, { id:'remote', name:'Remote' },
  { id:'external_player', name:'External Player' }, { id:'migrate', name:'Migrate' }
];

// ========== Render ==========
function render() {
  document.getElementById('headerDesc').textContent = t('headerDesc');
  renderProgressBar();
  renderSteps();
}

function renderProgressBar() {
  const bar = document.getElementById('progressBar');
  let html = '';
  const names = t('steps');
  for (let i = 0; i < totalSteps; i++) {
    const cls = i < currentStep ? 'done' : (i === currentStep ? 'active' : '');
    const num = cls === 'done' ? '' : (i + 1);
    html += '<div class="progress-step">';
    html += '<div class="step-circle ' + cls + '" onclick="window._goStep(' + i + ')">' + num + '<span class="step-label">' + (names[i]||'') + '</span></div>';
    if (i < totalSteps - 1) html += '<div class="step-line ' + (i < currentStep ? 'done' : '') + '"></div>';
    html += '</div>';
  }
  bar.innerHTML = html;
}

function renderSteps() {
  const w = document.getElementById('wizard');
  w.innerHTML = [
    renderStep0(), renderStep1(), renderStep2(), renderStep3(),
    renderStep4(), renderStep5(), renderStep6(), renderStep7(),
    renderStep8(), renderStep9()
  ].join('');
  bindAll();
}

// Step 0: Welcome + Language
function renderStep0() {
  return step(0, '<div class="card">' +
    '<h2>' + t('welcomeTitle') + '</h2>' +
    '<p class="desc">' + t('welcomeDesc') + '</p>' +
    '<p class="desc" style="margin-bottom:10px">' + t('chooseLang') + '</p>' +
    '<div class="lang-picker">' +
      '<button class="lang-btn' + (lang==='ru'?' active':'') + '" data-lang="ru">Русский</button>' +
      '<button class="lang-btn' + (lang==='en'?' active':'') + '" data-lang="en">English</button>' +
      '<button class="lang-btn' + (lang==='ua'?' active':'') + '" data-lang="ua">Українська</button>' +
    '</div></div>' +
    '<div class="btn-row"><button class="btn btn-primary" onclick="window._nextStep()">' + t('start') + '</button></div>'
  );
}

// Step 1: Password
function renderStep1() {
  return step(1, '<div class="card">' +
    '<h2>' + t('pwTitle') + '</h2><p class="desc">' + t('pwDesc') + '</p>' +
    fg('pw1', t('pwLabel'), 'password', '', t('pwWeak')) +
    '<div class="pw-strength"><div class="bar" id="pwBar"></div></div>' +
    fg('pw2', t('pwConfirm'), 'password', '', t('pwMismatch')) +
    '</div>' + navButtons()
  );
}

// Step 2: Telegram
function renderStep2() {
  return step(2, '<div class="card">' +
    '<h2>' + t('tgTitle') + '</h2><p class="desc">' + t('tgDesc') + '</p>' +
    toggleRow('tg_enable', t('tgEnable'), wizardData.telegram.enable) +
    '<div id="tgFields" style="' + (wizardData.telegram.enable?'':'display:none') + '">' +
    fg('tg_token', t('tgToken'), 'text', wizardData.telegram.bot_token, '', t('tgTokenHint')) +
    '<div style="text-align:right;margin:-8px 0 10px"><button class="btn btn-secondary" style="padding:5px 14px;font-size:12px" id="tgValidateBtn">' + t('tgValidate') + '</button> <span id="tgValidateResult" style="font-size:12px"></span></div>' +
    fg('tg_admin_id', t('tgAdminId'), 'number', wizardData.telegram.admin_id||'', '', t('tgAdminHint')) +
    fg('tg_bot_name', t('tgBotName'), 'text', wizardData.telegram.bot_name) +
    '<div class="form-row">' +
    fg('tg_max_devices', t('tgMaxDevices'), 'number', wizardData.telegram.max_devices_per_user) +
    '</div>' +
    toggleRow('tg_auto_approve', t('tgAutoApprove'), wizardData.telegram.auto_approve) +
    '</div></div>' + navButtons(true)
  );
}

// Step 3: Server
function renderStep3() {
  return step(3, '<div class="card">' +
    '<h2>' + t('srvTitle') + '</h2><p class="desc">' + t('srvDesc') + '</p>' +
    fg('srv_addr', t('srvAddr'), 'text', wizardData.server.addr, '', t('srvAddrHint')) +
    toggleRow('srv_dns', t('srvDns'), wizardData.server.system_dns) +
    '<h3>TMDB</h3>' +
    '<div class="form-group"><label>' + t('tmdbMode') + '</label>' +
    '<select id="tmdb_mode"><option value="self"' + (wizardData.tmdb_proxy.mode==='self'?' selected':'') + '>Self-proxy</option>' +
    '<option value="alcopa"' + (wizardData.tmdb_proxy.mode==='alcopa'?' selected':'') + '>Alcopa</option>' +
    '<option value="disabled"' + (wizardData.tmdb_proxy.mode==='disabled'?' selected':'') + '>Disabled</option></select></div>' +
    fg('tmdb_key', t('tmdbKey'), 'text', wizardData.tmdb_proxy.api_key) +
    '</div>' + navButtons(true)
  );
}

// Step 4: Sources
function renderStep4() {
  let html = '<div class="card"><h2>' + t('srcTitle') + '</h2><p class="desc">' + t('srcDesc') + '</p>' +
    toggleRow('src_check', t('srcCheckSearch'), wizardData.online.check_online_search) + '</div>';
  const groups = [
    ['popular', t('srcPopular')], ['free', t('srcFree')], ['anime', t('srcAnime')],
    ['ua', t('srcUA')], ['other', t('srcOther')]
  ];
  for (const [gid, gname] of groups) {
    html += '<div class="balancer-group"><h3>' + gname + '</h3>';
    for (const b of BALANCERS[gid]) {
      const enabled = !!(wizardData.online[b.id] && wizardData.online[b.id]._enabled);
      const statusCls = b.status === 'working' ? 'working' : (b.status === 'dead' ? 'dead' : '');
      const statusText = b.status === 'working' ? 'OK' : (b.status === 'dead' ? 'Dead' : '');
      html += '<div class="balancer-card' + (enabled?' enabled':'') + '" data-bid="' + b.id + '">' +
        '<div class="bc-header">' +
        '<span class="bc-name">' + b.name + '</span>' +
        (statusText ? '<span class="bc-status ' + statusCls + '">' + statusText + '</span>' : '') +
        mkToggle('bal_' + b.id, enabled) +
        '</div>';
      if (b.fields.length) {
        html += '<div class="bc-fields">';
        for (const f of b.fields) {
          if (f.type === 'toggle') {
            html += toggleRow('bal_' + b.id + '_' + f.k, f.l, false);
          } else {
            const val = (wizardData.online[b.id] && wizardData.online[b.id][f.k]) || (defaults.online && defaults.online[b.id] && defaults.online[b.id][f.k]) || f.def || '';
            html += fg('bal_' + b.id + '_' + f.k, f.l, f.type || 'text', val, '', f.hint || '');
          }
        }
        html += '</div>';
      }
      html += '</div>';
    }
    html += '</div>';
  }
  return step(4, html + navButtons(true));
}

// Step 5: Proxy
function renderStep5() {
  return step(5, '<div class="card">' +
    '<h2>' + t('proxyTitle') + '</h2><p class="desc">' + t('proxyDesc') + '</p>' +
    toggleRow('proxy_enable', t('proxyNeed'), wizardData._proxy_enable) +
    '<div id="proxyFields" style="' + (wizardData._proxy_enable?'':'display:none') + '">' +
    '<div id="proxyEntries"></div>' +
    '<button class="btn btn-secondary" style="margin-top:8px" id="proxyAddBtn">' + t('proxyAddEntry') + '</button>' +
    '</div></div>' +
    '<div class="card" style="margin-top:16px">' +
    '<h2>' + t('proxyTgTitle') + '</h2><p class="desc">' + t('proxyTgDesc') + '</p>' +
    toggleRow('proxy_tg_enable', t('proxyTgEnable'), wizardData._proxy_tg_enable||false) +
    '<div id="proxyTgFields" style="' + (wizardData._proxy_tg_enable?'':'display:none') + '">' +
    fg('proxy_tg_uri', t('proxyTgUri'), 'text', wizardData._proxy_tg_uri||'', '', 'vless://... / socks5://...') +
    '<div style="text-align:right;margin:-8px 0 10px"><button class="btn btn-secondary" style="padding:5px 14px;font-size:12px" id="proxyTgTestBtn">' + t('proxyTgTest') + '</button> <span id="proxyTgTestResult" style="font-size:12px"></span></div>' +
    '</div></div>' + navButtons(true)
  );
}

// Step 6: TorrServer
function renderStep6() {
  const mode = wizardData._ts_mode || 'builtin';
  return step(6, '<div class="card">' +
    '<h2>' + t('tsTitle') + '</h2><p class="desc">' + t('tsDesc') + '</p>' +
    toggleRow('ts_enable', t('tsEnable'), wizardData._ts_enable) +
    '<div id="tsFields" style="' + (wizardData._ts_enable?'':'display:none') + '">' +
    // Mode selector
    '<div class="form-group"><label>' + t('tsMode') + '</label>' +
    '<select id="ts_mode"><option value="builtin"' + (mode==='builtin'?' selected':'') + '>' + t('tsModeBuiltin') + '</option>' +
    '<option value="external"' + (mode==='external'?' selected':'') + '>' + t('tsModeExternal') + '</option></select></div>' +
    // Built-in fields
    '<div id="tsBuiltin" style="' + (mode==='builtin'?'':'display:none') + '">' +
    fg('ts_port', t('tsPort'), 'number', wizardData.torrserver.port) +
    '<div class="form-row">' +
    fg('ts_login', t('tsLogin'), 'text', wizardData.torrserver.login) +
    fg('ts_password', t('tsPassword'), 'password', wizardData.torrserver.password) +
    '</div>' +
    '<h3>' + t('tsCache') + '</h3>' +
    '<div class="form-row">' +
    fg('ts_cache_ram', t('tsCacheRAM'), 'number', wizardData.torrserver.cache_size_mb || 64) +
    fg('ts_cache_disk', t('tsCacheDisk'), 'number', wizardData.torrserver.disk_cache_mb || 1024) +
    '</div>' +
    fg('ts_preload', t('tsPreload'), 'number', wizardData.torrserver.preload_mb || 5) +
    toggleRow('ts_disable_dht', t('tsDisableDHT'), wizardData.torrserver.disable_dht || false) +
    toggleRow('ts_disable_upload', t('tsDisableUpload'), wizardData.torrserver.disable_upload !== false) +
    '</div>' +
    // External fields
    '<div id="tsExternal" style="' + (mode==='external'?'':'display:none') + '">' +
    fg('ts_url', t('tsURL'), 'text', wizardData.torrserver.url, '', t('tsURLHint')) +
    '<div style="text-align:right;margin:-8px 0 10px"><button class="btn btn-secondary" style="padding:5px 14px;font-size:12px" id="tsTestBtn">' + t('tsTest') + '</button> <span id="tsTestResult" style="font-size:12px"></span></div>' +
    '<div class="form-row">' +
    fg('ts_ext_login', t('tsLogin'), 'text', wizardData.torrserver.login) +
    fg('ts_ext_password', t('tsPassword'), 'password', wizardData.torrserver.password) +
    '</div>' +
    '</div>' +
    '</div></div>' + navButtons(true)
  );
}

// Step 7: Plugins
function renderStep7() {
  let grid = '<div class="toggle-grid">';
  for (const p of PLUGINS) {
    const checked = wizardData.web.plugins[p.id] !== undefined ? wizardData.web.plugins[p.id] : true;
    grid += toggleRow('plug_' + p.id, p.name, checked);
  }
  grid += '</div>';
  return step(7, '<div class="card"><h2>' + t('plugTitle') + '</h2><p class="desc">' + t('plugDesc') + '</p>' + grid +
    '<h3>' + t('plugCub') + '</h3>' +
    toggleRow('cub_enable', 'Cub.red', wizardData.cub.enable) +
    fg('cub_domain', t('plugCubDomain'), 'text', wizardData.cub.domain) +
    '<h3>' + t('plugIptv') + '</h3>' +
    toggleRow('iptv_enable', 'IPTV', wizardData.iptv.enable) +
    '</div>' + navButtons(true)
  );
}

// Step 8: Advanced
function renderStep8() {
  const sections = [
    ['yt', t('advYt'), fg('yt_client_id','Client ID','text',wizardData.youtube_oauth.client_id) + fg('yt_client_secret','Client Secret','password',wizardData.youtube_oauth.client_secret) + '<h3>YouTube</h3><div class="form-group"><label>Proxy</label><select id="yt_proxy"><option value="auto"' + (wizardData.youtube.proxy==='auto'||!wizardData.youtube.proxy?' selected':'') + '>Auto</option><option value="none"' + (wizardData.youtube.proxy==='none'?' selected':'') + '>None</option><option value="antidpi"' + (wizardData.youtube.proxy==='antidpi'?' selected':'') + '>AntiDPI</option><option value="vless"' + (wizardData.youtube.proxy==='vless'?' selected':'') + '>VLESS</option></select></div>' + fg('yt_timeout', t('advYtTimeout'), 'number', wizardData.youtube.extract_timeout || 60)],
    ['dlna', t('advDlna'), toggleRow('dlna_enable','Enable',wizardData.dlna.enable) + fg('dlna_path','Path','text',wizardData.dlna.path) + fg('dlna_name','Friendly Name','text',wizardData.dlna.friendly_name)],
    ['trans', t('advTranscoding'), toggleRow('trans_enable','Enable',wizardData.transcoding.enable) + fg('trans_ffmpeg','FFmpeg','text',wizardData.transcoding.ffmpeg) + fg('trans_max','Max Concurrent','number',wizardData.transcoding.max_concurrent_jobs)],
    ['browser', t('advBrowser'), fg('bp_max','Max Concurrent','number',wizardData.browser_pool.max_concurrent)],
    ['obs', t('advObservability'), toggleRow('obs_access','Access Log',wizardData.observability.access_log) + '<div class="form-group"><label>Log Level</label><select id="obs_log_level"><option value="info"' + (wizardData.observability.log_level==='info'||!wizardData.observability.log_level?' selected':'') + '>Info</option><option value="debug"' + (wizardData.observability.log_level==='debug'?' selected':'') + '>Debug</option><option value="warn"' + (wizardData.observability.log_level==='warn'?' selected':'') + '>Warn</option><option value="error"' + (wizardData.observability.log_level==='error'?' selected':'') + '>Error</option></select></div>'],
    ['adpi', t('advAntiDpi'), toggleRow('adpi_enable','Enable',wizardData.antidpi.enable) + '<div class="form-group"><label>Strategy</label><select id="adpi_strategy"><option value="auto">Auto</option><option value="split-1">Split-1</option><option value="split-2">Split-2</option><option value="split-sni">Split-SNI</option><option value="none">None</option></select></div>'],
    ['kp', t('advKinopoisk'), fg('kp_token','API Token','text',wizardData.kinopoisk.token)],
    ['skip', t('advSkipIntro'), toggleRow('skip_enable','Enable',wizardData.skip_intro.enable)],
    ['cal', t('advCalendar'), toggleRow('cal_enable','Enable',wizardData.calendar.enable)],
    ['cluster', t('advCluster'), toggleRow('cluster_enable','Enable',wizardData.cluster.enable) + '<div class="form-group"><label>Mode</label><select id="cluster_mode"><option value="primary">Primary</option><option value="node">Node</option></select></div>' + fg('cluster_key','API Key','text',wizardData.cluster.api_key)],
    ['llm', t('advLlm'), fg('llm_endpoint','Endpoint','text',wizardData.llm.endpoint) + fg('llm_key','API Key','password',wizardData.llm.api_key) + fg('llm_model','Model','text',wizardData.llm.model)],
    ['osubs', t('advOpensubs'), toggleRow('osubs_enable','Enable',wizardData.opensubs.enable) + fg('osubs_key','API Key','text',wizardData.opensubs.api_key)]
  ];
  let html = '<div class="card"><h2>' + t('advTitle') + '</h2><p class="desc">' + t('advDesc') + '</p>';
  for (const [id, title, body] of sections) {
    html += '<div class="collapsible" data-collapse="' + id + '">' + title + '</div><div class="collapsible-body" data-collapse-body="' + id + '">' + body + '</div>';
  }
  html += '</div>';
  return step(8, html + navButtons(true));
}

// Step 9: Summary
function renderStep9() {
  return step(9, '<div class="card" id="summaryCard"><h2>' + t('sumTitle') + '</h2><p class="desc">' + t('sumDesc') + '</p><div id="summaryBody"></div></div>' +
    '<div class="btn-row"><button class="btn btn-secondary" onclick="window._prevStep()">' + t('prev') + '</button><button class="btn btn-primary" onclick="window._apply()">' + t('apply') + '</button></div>'
  );
}

// ========== Helpers ==========
function step(i, inner) {
  return '<div class="step-content' + (i===currentStep?' active':'') + '" data-step="' + i + '">' + inner + '</div>';
}
function fg(id, label, type, val, errMsg, hint) {
  val = val === undefined || val === null ? '' : val;
  return '<div class="form-group"><label for="' + id + '">' + label + '</label>' +
    '<input type="' + (type||'text') + '" id="' + id + '" value="' + esc(String(val)) + '">' +
    (hint ? '<div class="hint">' + hint + '</div>' : '') +
    (errMsg ? '<div class="error">' + errMsg + '</div>' : '') + '</div>';
}
function mkToggle(id, checked) {
  return '<label class="toggle"><input type="checkbox" id="' + id + '"' + (checked?' checked':'') + '><span class="slider"></span></label>';
}
function toggleRow(id, label, checked) {
  return '<div class="toggle-row"><span class="toggle-label">' + label + '</span>' + mkToggle(id, checked) + '</div>';
}
function navButtons(showSkip) {
  return '<div class="btn-row">' +
    (currentStep > 0 ? '<button class="btn btn-secondary" onclick="window._prevStep()">' + t('prev') + '</button>' : '') +
    (showSkip ? '<button class="btn btn-skip" onclick="window._nextStep()">' + t('skip') + '</button>' : '') +
    '<button class="btn btn-primary" onclick="window._nextStep()">' + t('next') + '</button></div>';
}
function esc(s) { return s.replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;'); }

// ========== Navigation ==========
window._goStep = function(i) {
  if (i > currentStep) return; // can only go back
  collectData();
  currentStep = i;
  render();
};
window._nextStep = function() {
  if (!validateStep()) return;
  collectData();
  if (currentStep < totalSteps - 1) {
    currentStep++;
    render();
    if (currentStep === totalSteps - 1) buildSummary();
    window.scrollTo(0, 0);
  }
};
window._prevStep = function() {
  collectData();
  if (currentStep > 0) { currentStep--; render(); window.scrollTo(0, 0); }
};

// ========== Validation ==========
function validateStep() {
  if (currentStep === 1) {
    const pw = gv('pw1'), pw2 = gv('pw2');
    if (pw.length > 0 && pw.length < 6) { showError('pw1'); return false; }
    if (pw && pw !== pw2) { showError('pw2'); return false; }
  }
  return true;
}
function showError(id) {
  const el = document.getElementById(id);
  if (el) el.closest('.form-group')?.classList.add('has-error');
  setTimeout(() => el?.closest('.form-group')?.classList.remove('has-error'), 3000);
}

// ========== Collect data from form ==========
function collectData() {
  if (currentStep === 0) { /* lang already set via buttons */ }
  else if (currentStep === 1) {
    wizardData.admin.password = gv('pw1');
  }
  else if (currentStep === 2) {
    wizardData.telegram.enable = gc('tg_enable');
    wizardData.telegram.bot_token = gv('tg_token');
    wizardData.telegram.admin_id = parseInt(gv('tg_admin_id')) || 0;
    wizardData.telegram.bot_name = gv('tg_bot_name');
    wizardData.telegram.max_devices_per_user = parseInt(gv('tg_max_devices')) || 3;
    wizardData.telegram.auto_approve = gc('tg_auto_approve');
  }
  else if (currentStep === 3) {
    wizardData.server.addr = gv('srv_addr');
    wizardData.server.system_dns = gc('srv_dns');
    wizardData.tmdb_proxy.mode = gv('tmdb_mode');
    wizardData.tmdb_proxy.api_key = gv('tmdb_key');
  }
  else if (currentStep === 4) {
    wizardData.online.check_online_search = gc('src_check');
    for (const gid of Object.keys(BALANCERS)) {
      for (const b of BALANCERS[gid]) {
        const enabled = gc('bal_' + b.id);
        if (!wizardData.online[b.id]) wizardData.online[b.id] = {};
        wizardData.online[b.id]._enabled = enabled;
        for (const f of b.fields) {
          if (f.type === 'toggle') {
            wizardData.online[b.id][f.k] = gc('bal_' + b.id + '_' + f.k);
          } else {
            wizardData.online[b.id][f.k] = gv('bal_' + b.id + '_' + f.k);
          }
        }
      }
    }
  }
  else if (currentStep === 5) {
    wizardData._proxy_enable = gc('proxy_enable');
    collectProxyEntries();
    wizardData._proxy_tg_enable = gc('proxy_tg_enable');
    wizardData._proxy_tg_uri = gv('proxy_tg_uri');
  }
  else if (currentStep === 6) {
    wizardData._ts_enable = gc('ts_enable');
    wizardData._ts_mode = gv('ts_mode') || 'builtin';
    if (wizardData._ts_mode === 'builtin') {
      wizardData.torrserver.port = parseInt(gv('ts_port')) || 8090;
      wizardData.torrserver.login = gv('ts_login');
      wizardData.torrserver.password = gv('ts_password');
      wizardData.torrserver.url = '';
      wizardData.torrserver.cache_size_mb = parseInt(gv('ts_cache_ram')) || 64;
      wizardData.torrserver.disk_cache_mb = parseInt(gv('ts_cache_disk')) || 1024;
      wizardData.torrserver.preload_mb = parseInt(gv('ts_preload')) || 5;
      wizardData.torrserver.disable_dht = gc('ts_disable_dht');
      wizardData.torrserver.disable_upload = gc('ts_disable_upload');
    } else {
      wizardData.torrserver.url = gv('ts_url');
      wizardData.torrserver.login = gv('ts_ext_login');
      wizardData.torrserver.password = gv('ts_ext_password');
      wizardData.torrserver.port = 0;
    }
  }
  else if (currentStep === 7) {
    for (const p of PLUGINS) wizardData.web.plugins[p.id] = gc('plug_' + p.id);
    wizardData.cub.enable = gc('cub_enable');
    wizardData.cub.domain = gv('cub_domain');
    wizardData.iptv.enable = gc('iptv_enable');
  }
  else if (currentStep === 8) {
    wizardData.youtube_oauth.client_id = gv('yt_client_id');
    wizardData.youtube_oauth.client_secret = gv('yt_client_secret');
    wizardData.youtube.proxy = gv('yt_proxy');
    wizardData.youtube.extract_timeout = parseInt(gv('yt_timeout')) || 60;
    wizardData.dlna.enable = gc('dlna_enable');
    wizardData.dlna.path = gv('dlna_path');
    wizardData.dlna.friendly_name = gv('dlna_name');
    wizardData.transcoding.enable = gc('trans_enable');
    wizardData.transcoding.ffmpeg = gv('trans_ffmpeg');
    wizardData.transcoding.max_concurrent_jobs = parseInt(gv('trans_max')) || 5;
    wizardData.browser_pool.max_concurrent = parseInt(gv('bp_max')) || 3;
    wizardData.observability.access_log = gc('obs_access');
    wizardData.observability.log_level = gv('obs_log_level');
    wizardData.antidpi.enable = gc('adpi_enable');
    wizardData.antidpi.strategy = gv('adpi_strategy');
    wizardData.kinopoisk.token = gv('kp_token');
    wizardData.skip_intro.enable = gc('skip_enable');
    wizardData.calendar.enable = gc('cal_enable');
    wizardData.cluster.enable = gc('cluster_enable');
    wizardData.cluster.mode = gv('cluster_mode');
    wizardData.cluster.api_key = gv('cluster_key');
    wizardData.llm.endpoint = gv('llm_endpoint');
    wizardData.llm.api_key = gv('llm_key');
    wizardData.llm.model = gv('llm_model');
    wizardData.opensubs.enable = gc('osubs_enable');
    wizardData.opensubs.api_key = gv('osubs_key');
  }
}

function gv(id) { const el = document.getElementById(id); return el ? el.value.trim() : ''; }
function gc(id) { const el = document.getElementById(id); return el ? el.checked : false; }

function collectProxyEntries() {
  const entries = [];
  document.querySelectorAll('.proxy-entry').forEach(el => {
    const uri = el.querySelector('.proxy-uri')?.value.trim();
    const bals = el.querySelector('.proxy-bals')?.value.trim();
    if (uri) entries.push({ uri, balancers: bals ? bals.split(',').map(s=>s.trim()) : [] });
  });
  wizardData.proxy.vless.entries = entries;
}

// ========== Build summary ==========
function buildSummary() {
  collectData();
  const b = document.getElementById('summaryBody');
  if (!b) return;
  let html = '';
  // Password
  html += sumSection(t('sumPassword'), [[t('sumPassword'), wizardData.admin.password ? t('sumSet') : t('sumNotSet'), !wizardData.admin.password]]);
  // Telegram
  html += sumSection(t('sumTelegram'), [
    ['Status', wizardData.telegram.enable ? t('sumEnabled') : t('sumDisabled'), !wizardData.telegram.enable],
    ...(wizardData.telegram.enable ? [['Bot', wizardData.telegram.bot_name || wizardData.telegram.bot_token.substring(0,8)+'...'], ['Admin ID', String(wizardData.telegram.admin_id)]] : [])
  ]);
  // Server
  html += sumSection(t('sumServer'), [['Addr', wizardData.server.addr], ['TMDB', wizardData.tmdb_proxy.mode]]);
  // Sources
  const enabledSources = [];
  for (const gid of Object.keys(BALANCERS)) {
    for (const bal of BALANCERS[gid]) {
      if (wizardData.online[bal.id] && wizardData.online[bal.id]._enabled) enabledSources.push(bal.name);
    }
  }
  html += sumSection(t('sumSources'), [[t('sumSources'), enabledSources.length ? enabledSources.join(', ') : '-']]);
  // Proxy
  html += sumSection(t('sumProxy'), [
    [t('sumProxy'), wizardData._proxy_enable ? wizardData.proxy.vless.entries.length + ' entries' : t('sumDisabled'), !wizardData._proxy_enable],
    ['Telegram', wizardData._proxy_tg_enable ? t('sumEnabled') : t('sumDisabled'), !wizardData._proxy_tg_enable]
  ]);
  // TorrServer
  html += sumSection(t('sumTorrServer'), [['Status', wizardData._ts_enable ? t('sumEnabled') + ' :' + wizardData.torrserver.port : t('sumDisabled'), !wizardData._ts_enable]]);
  // Plugins
  const onPlugins = PLUGINS.filter(p => wizardData.web.plugins[p.id]).map(p => p.name);
  html += sumSection(t('sumPlugins'), [[t('sumPlugins'), onPlugins.length + ' / ' + PLUGINS.length]]);

  b.innerHTML = html;
}

function sumSection(title, rows) {
  let html = '<div class="summary-section"><h3>' + title + '</h3>';
  for (const [k, v, masked] of rows) {
    html += '<div class="summary-row"><span class="sk">' + k + '</span><span class="sv' + (masked?' masked':'') + '">' + esc(String(v||'')) + '</span></div>';
  }
  return html + '</div>';
}

// ========== Apply ==========
window._apply = async function() {
  collectData();
  const spinner = document.getElementById('spinner');
  spinner.classList.add('show');

  // Build the payload matching config.toml structure.
  const payload = buildPayload();

  try {
    const r = await fetch('/web/installer/api/apply', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    const data = await r.json();
    spinner.classList.remove('show');
    if (data.ok) {
      showSuccess(data.admin_path);
    } else {
      alert('Error: ' + (data.error || 'unknown'));
    }
  } catch(e) {
    spinner.classList.remove('show');
    alert('Network error: ' + e.message);
  }
};

function buildPayload() {
  const p = {};

  // Server
  p.server = { addr: wizardData.server.addr, system_dns: wizardData.server.system_dns };

  // Admin
  p.admin = { password: wizardData.admin.password };

  // Telegram
  if (wizardData.telegram.enable) {
    p.telegram = {
      enable: true,
      bot_token: wizardData.telegram.bot_token,
      admin_id: wizardData.telegram.admin_id,
      bot_name: wizardData.telegram.bot_name,
      max_devices_per_user: wizardData.telegram.max_devices_per_user,
      auto_approve: wizardData.telegram.auto_approve
    };
  }

  // TMDB
  p.tmdb_proxy = { mode: wizardData.tmdb_proxy.mode };
  if (wizardData.tmdb_proxy.api_key) p.tmdb_proxy.api_key = wizardData.tmdb_proxy.api_key;

  // Online sources
  p.online = { check_online_search: wizardData.online.check_online_search };
  for (const gid of Object.keys(BALANCERS)) {
    for (const b of BALANCERS[gid]) {
      const bd = wizardData.online[b.id];
      if (!bd || !bd._enabled) continue;
      const src = {};
      for (const f of b.fields) {
        if (bd[f.k] !== undefined && bd[f.k] !== '') {
          let val = bd[f.k];
          if (f.k === 'tokens' && typeof val === 'string') val = val.split(',').map(s=>s.trim()).filter(Boolean);
          src[f.k] = val;
        }
      }
      if (Object.keys(src).length || b.fields.length === 0) p.online[b.id] = src;
    }
  }

  // Proxy
  if (wizardData._proxy_enable && wizardData.proxy.vless.entries.length) {
    p.proxy = { vless: { entries: wizardData.proxy.vless.entries } };
  }

  // Telegram proxy via ProxyCore
  if (wizardData._proxy_tg_enable && wizardData._proxy_tg_uri) {
    if (!p.proxycore) p.proxycore = { entries: [] };
    p.proxycore.entries.push({
      uri: wizardData._proxy_tg_uri,
      label: 'Telegram Proxy',
      balancers: ['telegram']
    });
  }

  // TorrServer
  if (wizardData._ts_enable) {
    p.torrserver = {
      login: wizardData.torrserver.login,
      password: wizardData.torrserver.password
    };
    if (wizardData._ts_mode === 'external') {
      p.torrserver.url = wizardData.torrserver.url;
    } else {
      p.torrserver.port = wizardData.torrserver.port;
      p.torrserver.cache_size_mb = wizardData.torrserver.cache_size_mb;
      p.torrserver.disk_cache_mb = wizardData.torrserver.disk_cache_mb;
      p.torrserver.preload_mb = wizardData.torrserver.preload_mb;
      p.torrserver.disable_dht = wizardData.torrserver.disable_dht;
      p.torrserver.disable_upload = wizardData.torrserver.disable_upload;
    }
  }

  // Plugins
  p.web = { plugins: wizardData.web.plugins };

  // Cub
  if (wizardData.cub.enable) p.cub = { enable: true, domain: wizardData.cub.domain };
  // IPTV
  if (wizardData.iptv.enable) p.iptv = { enable: true };

  // Advanced
  if (wizardData.youtube_oauth.client_id) p.youtube_oauth = wizardData.youtube_oauth;
  if (wizardData.dlna.enable) p.dlna = { enable: true, path: wizardData.dlna.path, friendly_name: wizardData.dlna.friendly_name };
  if (wizardData.transcoding.enable) p.transcoding = { enable: true, ffmpeg: wizardData.transcoding.ffmpeg, max_concurrent_jobs: wizardData.transcoding.max_concurrent_jobs };
  p.browser_pool = { max_concurrent: wizardData.browser_pool.max_concurrent };
  p.observability = { access_log: wizardData.observability.access_log, log_level: wizardData.observability.log_level || 'info' };
  p.youtube = { proxy: wizardData.youtube.proxy, extract_timeout: wizardData.youtube.extract_timeout };
  if (wizardData.antidpi.enable) p.antidpi = { enable: true, strategy: wizardData.antidpi.strategy };
  if (wizardData.kinopoisk.token) p.kinopoisk = { token: wizardData.kinopoisk.token };
  if (wizardData.skip_intro.enable) p.skip_intro = { enable: true };
  if (wizardData.calendar.enable) p.calendar = { enable: true };
  if (wizardData.cluster.enable) p.cluster = { enable: true, mode: wizardData.cluster.mode, api_key: wizardData.cluster.api_key };
  if (wizardData.llm.endpoint) p.llm = { endpoint: wizardData.llm.endpoint, api_key: wizardData.llm.api_key, model: wizardData.llm.model };
  if (wizardData.opensubs.enable) p.opensubs = { enable: true, api_key: wizardData.opensubs.api_key };

  return p;
}

// ========== Success ==========
function showSuccess(adminPath) {
  const w = document.getElementById('wizard');
  document.getElementById('progressBar').style.display = 'none';
  let sec = 10;
  w.innerHTML = '<div class="success-screen">' +
    '<div class="check">\u2713</div>' +
    '<h2>' + t('successTitle') + '</h2>' +
    '<p>' + t('successDesc') + '</p>' +
    '<a href="' + adminPath + '" id="adminLink">' + t('successLink') + '</a>' +
    '<p style="margin-top:24px;font-size:13px;color:var(--text-dim)">' + t('successCountdown') + ' <span id="countdown">' + sec + '</span> ' + t('successSec') + '</p>' +
    '</div>';
  const cd = setInterval(() => {
    sec--;
    const el = document.getElementById('countdown');
    if (el) el.textContent = sec;
    if (sec <= 0) { clearInterval(cd); window.location.href = adminPath; }
  }, 1000);
}

// ========== Bind events ==========
function bindAll() {
  // Language buttons
  document.querySelectorAll('.lang-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      lang = btn.dataset.lang;
      render();
    });
  });
  // Toggle visibility
  bindToggleVis('tg_enable', 'tgFields');
  bindToggleVis('proxy_enable', 'proxyFields');
  bindToggleVis('proxy_tg_enable', 'proxyTgFields');
  bindToggleVis('ts_enable', 'tsFields');
  // TorrServer mode switcher
  const tsMode = document.getElementById('ts_mode');
  if (tsMode) tsMode.addEventListener('change', () => {
    const builtin = document.getElementById('tsBuiltin');
    const external = document.getElementById('tsExternal');
    if (builtin) builtin.style.display = tsMode.value === 'builtin' ? '' : 'none';
    if (external) external.style.display = tsMode.value === 'external' ? '' : 'none';
  });
  // TorrServer test connection
  const tsTestBtn = document.getElementById('tsTestBtn');
  if (tsTestBtn) tsTestBtn.addEventListener('click', async () => {
    const url = gv('ts_url');
    const res = document.getElementById('tsTestResult');
    if (!url || !res) return;
    tsTestBtn.disabled = true;
    try {
      const r = await fetch('/web/installer/api/validate', {
        method:'POST', headers:{'Content-Type':'application/json'},
        body: JSON.stringify({check:'torrserver', value:url})
      });
      const d = await r.json();
      res.textContent = d.ok ? '\u2713 ' + d.info : '\u2717 ' + d.info;
      res.style.color = d.ok ? 'var(--success)' : 'var(--danger)';
    } catch(e) { res.textContent = '\u2717 Error'; res.style.color = 'var(--danger)'; }
    tsTestBtn.disabled = false;
  });
  // Telegram proxy test
  const tgProxyTestBtn = document.getElementById('proxyTgTestBtn');
  if (tgProxyTestBtn) tgProxyTestBtn.addEventListener('click', async () => {
    const uri = gv('proxy_tg_uri');
    const res = document.getElementById('proxyTgTestResult');
    if (!uri || !res) return;
    tgProxyTestBtn.disabled = true;
    res.textContent = '';
    try {
      const r = await fetch('/web/installer/api/validate', {
        method:'POST', headers:{'Content-Type':'application/json'},
        body: JSON.stringify({check:'telegram_proxy', value:uri})
      });
      const d = await r.json();
      res.textContent = d.ok ? '\u2713 ' + t('proxyTgOk') : '\u2717 ' + (d.info || t('proxyTgFail'));
      res.style.color = d.ok ? 'var(--success)' : 'var(--danger)';
    } catch(e) { res.textContent = '\u2717 ' + t('proxyTgFail'); res.style.color = 'var(--danger)'; }
    tgProxyTestBtn.disabled = false;
  });
  // Balancer toggles
  document.querySelectorAll('.balancer-card').forEach(card => {
    const bid = card.dataset.bid;
    const cb = document.getElementById('bal_' + bid);
    if (cb) cb.addEventListener('change', () => {
      card.classList.toggle('enabled', cb.checked);
    });
  });
  // Proxy add entry
  const addBtn = document.getElementById('proxyAddBtn');
  if (addBtn) addBtn.addEventListener('click', addProxyEntry);
  // Render existing proxy entries
  const pe = document.getElementById('proxyEntries');
  if (pe && wizardData.proxy.vless.entries.length) {
    wizardData.proxy.vless.entries.forEach(e => addProxyEntryUI(pe, e.uri, e.balancers.join(', ')));
  }
  // Password strength
  const pw1 = document.getElementById('pw1');
  if (pw1) pw1.addEventListener('input', () => {
    const bar = document.getElementById('pwBar');
    if (!bar) return;
    const s = pw1.value.length;
    const pct = Math.min(s / 12 * 100, 100);
    bar.style.width = pct + '%';
    bar.style.background = s < 6 ? 'var(--danger)' : s < 10 ? 'var(--warning)' : 'var(--success)';
  });
  // Collapsible
  document.querySelectorAll('.collapsible').forEach(el => {
    el.addEventListener('click', () => el.classList.toggle('open'));
  });
  // TG validate button
  const vBtn = document.getElementById('tgValidateBtn');
  if (vBtn) vBtn.addEventListener('click', async () => {
    const token = gv('tg_token');
    const res = document.getElementById('tgValidateResult');
    if (!token || !res) return;
    vBtn.disabled = true;
    try {
      const r = await fetch('/web/installer/api/validate', {
        method:'POST', headers:{'Content-Type':'application/json'},
        body: JSON.stringify({check:'telegram', value:token})
      });
      const d = await r.json();
      res.textContent = d.ok ? '\u2713 ' + d.info : '\u2717 ' + d.info;
      res.style.color = d.ok ? 'var(--success)' : 'var(--danger)';
      // Auto-fill bot name from validation result
      if (d.ok && d.info) {
        const botName = d.info.replace(/^@/, '');
        const nameEl = document.getElementById('tg_bot_name');
        if (nameEl && !nameEl.value.trim()) nameEl.value = botName;
        wizardData.telegram.bot_name = botName;
      }
    } catch(e) { res.textContent = '\u2717 Error'; res.style.color = 'var(--danger)'; }
    vBtn.disabled = false;
  });
}

function bindToggleVis(toggleId, targetId) {
  const cb = document.getElementById(toggleId);
  const target = document.getElementById(targetId);
  if (cb && target) cb.addEventListener('change', () => { target.style.display = cb.checked ? '' : 'none'; });
}

function addProxyEntry() {
  const pe = document.getElementById('proxyEntries');
  if (pe) addProxyEntryUI(pe, '', '');
}

function addProxyEntryUI(container, uri, bals) {
  const div = document.createElement('div');
  div.className = 'proxy-entry';
  div.style.cssText = 'display:flex;gap:8px;margin-bottom:8px;align-items:center';
  div.innerHTML = '<input class="proxy-uri" placeholder="vless://..." value="' + esc(uri) + '" style="flex:2;padding:8px 12px;background:var(--surface2);border:1px solid var(--border);border-radius:8px;color:var(--text);font-size:13px">' +
    '<input class="proxy-bals" placeholder="filmix, rezka" value="' + esc(bals) + '" style="flex:1;padding:8px 12px;background:var(--surface2);border:1px solid var(--border);border-radius:8px;color:var(--text);font-size:13px">' +
    '<button style="background:var(--danger);color:#fff;border:none;border-radius:8px;padding:8px 12px;cursor:pointer;font-size:13px" onclick="this.parentElement.remove()">\u2717</button>';
  container.appendChild(div);
}

// ========== Start ==========
init();
})();
</script>
</body>
</html>`
