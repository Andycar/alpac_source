package httpapi

import (
	"net/http"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
)

// kitPageHandler serves the Telegram Mini App HTML page.
// GET /kit
func kitPageHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Kit.Enable {
			http.NotFound(w, r)
			return
		}

		host := hostFromRequest(r)
		userLang := r.URL.Query().Get("lang")
		if userLang != "ru" && userLang != "uk" && userLang != "en" {
			userLang = ""
		}
		html := strings.ReplaceAll(kitWebAppHTML, "{API_BASE}", host+"/api/kit")
		html = strings.ReplaceAll(html, "{USER_LANG}", userLang)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// no-store: same rationale as bkit and the plugin handlers — the
		// inlined JS body is request-scoped (host + USER_LANG substitution)
		// and a stale cached variant has caused "Can't find variable: esc"
		// reports when the page was loaded before a renderProfile rewrite
		// shipped with the matching helper.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(html))
	}
}

const kitWebAppHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0,maximum-scale=1.0,user-scalable=no">
<title>Kit</title>
<script src="https://telegram.org/js/telegram-web-app.js"></script>
<style>
/* ─────────────────────────────────────────────────────────────────────────
   Оформление мини-аппа. Держим ДВА продукта в одном окне: плагин Lampa
   (балансеры, привязки, TorrServer) и приложение ALPAC (профиль, подписки),
   IPTV общий. Раньше это были шесть равноправных вкладок в одну строку —
   ни продукта не видно, ни что важнее. Теперь главный экран группирует, а
   вкладки стали экранами второго уровня.

   Тёмной палитры тут раньше НЕ БЫЛО ВООБЩЕ: --bg был прибит к #ffffff, а из
   Telegram подставлялись только пять переменных. Границы rgba(0,0,0,.06) на
   тёмном фоне превращались в ничто — отсюда «каша». Теперь тёмная тема
   объявлена сама по себе (prefers-color-scheme + data-scheme от
   tg.colorScheme), а тема Telegram доопределяет её сверху.
   ───────────────────────────────────────────────────────────────────────── */
:root {
  color-scheme: light dark;
  --bg: #f2f2f7;
  --surface: #ffffff;
  --surface-2: #f0f0f5;
  --surface-3: #e7e7ee;
  --text: #0b0b0f;
  --text2: #6b6b76;
  --text3: #9a9aa4;
  --line: rgba(0,0,0,.08);
  --line2: rgba(0,0,0,.16);
  --accent: #3390ec;
  --accent2: #7c5cff;
  --on-accent: #ffffff;
  --success: #34c759;
  --warn: #ff9f0a;
  --danger: #ff3b30;
  --danger-hover: #e0342a;
  --accent-hover: #2b7fd4;
  --radius: 16px;
  --radius-sm: 12px;
  --shadow: 0 1px 2px rgba(0,0,0,.05), 0 8px 24px rgba(0,0,0,.06);
  /* легаси-псевдонимы: их же переопределяет тема Telegram из JS */
  --card-bg: var(--surface);
  --border: var(--line);
  --input-bg: var(--surface-2);
}
:root[data-scheme="dark"] {
  --bg: #0e0f13;
  --surface: #17181e;
  --surface-2: #1e1f26;
  --surface-3: #262832;
  --text: #f2f3f7;
  --text2: #9d9fab;
  --text3: #6f7280;
  --line: rgba(255,255,255,.09);
  --line2: rgba(255,255,255,.18);
  --danger: #ff453a;
  --shadow: 0 1px 2px rgba(0,0,0,.4), 0 10px 30px rgba(0,0,0,.35);
}
@media (prefers-color-scheme: dark) {
  :root:not([data-scheme="light"]) {
    --bg: #0e0f13;
    --surface: #17181e;
    --surface-2: #1e1f26;
    --surface-3: #262832;
    --text: #f2f3f7;
    --text2: #9d9fab;
    --text3: #6f7280;
    --line: rgba(255,255,255,.09);
    --line2: rgba(255,255,255,.18);
    --danger: #ff453a;
    --shadow: 0 1px 2px rgba(0,0,0,.4), 0 10px 30px rgba(0,0,0,.35);
  }
}
* { box-sizing: border-box; margin: 0; padding: 0; -webkit-tap-highlight-color: transparent; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Segoe UI', Roboto, sans-serif;
  background: var(--bg);
  color: var(--text);
  padding: 0 14px calc(96px + env(safe-area-inset-bottom));
  font-size: 15px;
  line-height: 1.45;
  min-height: 100vh;
  -webkit-font-smoothing: antialiased;
}

/* ── Шапка ── */
.topbar { display: flex; align-items: center; justify-content: space-between; padding: 16px 2px 12px; }
.brand { display: flex; align-items: center; gap: 8px; font-size: 17px; font-weight: 700; letter-spacing: -.02em; }
.brand i { width: 9px; height: 9px; border-radius: 50%; background: linear-gradient(135deg, var(--accent2), #ff5ca8); flex-shrink: 0; }
.brand em { font-style: normal; font-weight: 600; color: var(--accent); }
.brand-note { font-size: 12px; color: var(--text3); }

/* ── Карточка аккаунта на главной ── */
.hero {
  position: relative; overflow: hidden; border-radius: 20px; padding: 18px;
  margin-bottom: 22px; color: #fff; box-shadow: var(--shadow);
  background: linear-gradient(135deg, #3a2b6e, #1c2340 55%, #14233a);
}
.hero::after {
  content: ''; position: absolute; right: -60px; top: -70px; width: 190px; height: 190px;
  border-radius: 50%; background: radial-gradient(circle, rgba(124,92,255,.55), transparent 68%);
}
.hero-row { display: flex; align-items: center; gap: 14px; position: relative; z-index: 1; }
.hero-ava {
  width: 52px; height: 52px; border-radius: 50%; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: 20px; font-weight: 700;
  background: linear-gradient(135deg, var(--accent), var(--accent2));
  box-shadow: 0 0 0 3px rgba(255,255,255,.14);
}
.hero-who { flex: 1; min-width: 0; }
.hero-who b { display: block; font-size: 17px; font-weight: 650; letter-spacing: -.01em; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hero-who span { font-size: 13px; opacity: .62; }
.hero-stats { display: flex; gap: 22px; margin-top: 16px; position: relative; z-index: 1; }
.hero-stats div { min-width: 0; }
.hero-stats b { display: block; font-size: 18px; font-weight: 650; letter-spacing: -.02em; }
.hero-stats b.warn { color: #ffcf70; }
.hero-stats b.danger { color: #ff9d94; }
.hero-stats span { font-size: 11px; opacity: .55; text-transform: uppercase; letter-spacing: .05em; }
.hero-skel { opacity: .35; }

/* ── Группы и строки-переходы на главной ── */
.gr { margin-bottom: 22px; }
.gr-head { display: flex; align-items: baseline; gap: 8px; padding: 0 4px 8px; }
.gr-head h2 { font-size: 12.5px; font-weight: 650; text-transform: uppercase; letter-spacing: .06em; color: var(--text3); margin: 0; }
.gr-head i { font-style: normal; font-size: 11.5px; color: var(--text3); opacity: .8; }
.list { background: var(--surface); border-radius: var(--radius); overflow: hidden; box-shadow: var(--shadow); }
.row { display: flex; align-items: center; gap: 13px; padding: 13px 15px; cursor: pointer; position: relative; }
.row + .row::before { content: ''; position: absolute; left: 57px; right: 0; top: 0; height: 1px; background: var(--line); }
.row:active { background: var(--surface-2); }
.ico { width: 31px; height: 31px; border-radius: 9px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; font-size: 15px; }
.row-t { flex: 1; min-width: 0; }
.row-t b { display: block; font-size: 15px; font-weight: 550; letter-spacing: -.01em; }
.row-t span { display: block; font-size: 12.5px; color: var(--text2); margin-top: 1px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-v { font-size: 13.5px; color: var(--text3); flex-shrink: 0; max-width: 40%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chev { width: 7px; height: 7px; border-right: 2px solid var(--text3); border-bottom: 2px solid var(--text3); transform: rotate(-45deg); flex-shrink: 0; opacity: .5; margin-left: 2px; }
.dotmark { width: 8px; height: 8px; border-radius: 50%; background: var(--danger); flex-shrink: 0; }

/* ── Экран второго уровня ── */
.scr-head { padding: 4px 2px 14px; }
.scr-head h1 { font-size: 24px; font-weight: 700; letter-spacing: -.03em; line-height: 1.15; }
.scr-head p { font-size: 13px; color: var(--text2); margin-top: 4px; }

h2 { font-size: 12.5px; font-weight: 650; text-transform: uppercase; letter-spacing: .06em; color: var(--text3); margin: 22px 0 8px; padding: 0 4px; }
.section { margin-bottom: 8px; }

/* ── Карточки сервисов (экран «Привязки») ── */
.card {
  background: var(--surface); border-radius: var(--radius-sm); padding: 12px 14px;
  margin-bottom: 6px; display: flex; align-items: center; justify-content: space-between;
  gap: 10px; box-shadow: var(--shadow);
}
.card-info { flex: 1; min-width: 0; }
.card-name { font-weight: 600; font-size: 15px; letter-spacing: -.01em; }
.card-status { font-size: 12.5px; color: var(--text2); margin-top: 1px; }
.card-status.bound { color: var(--success); }
.badge { font-size: 10.5px; font-weight: 650; padding: 3px 8px; border-radius: 999px; white-space: nowrap; letter-spacing: .02em; }
.badge-free { background: rgba(52,199,89,.16); color: #2e9e4c; }
.badge-premium { background: rgba(255,159,10,.16); color: #c77700; }
:root[data-scheme="dark"] .badge-free { color: #5fd97e; }
:root[data-scheme="dark"] .badge-premium { color: #ffbc4d; }
@media (prefers-color-scheme: dark) {
  :root:not([data-scheme="light"]) .badge-free { color: #5fd97e; }
  :root:not([data-scheme="light"]) .badge-premium { color: #ffbc4d; }
}

/* ── Профиль ── */
.profile-section { padding: 0; }
.profile-hero { display: none; } /* заменена карточкой .hero на главной */
.profile-avatar { width: 50px; height: 50px; border-radius: 50%; flex-shrink: 0; background: linear-gradient(135deg, var(--accent), var(--accent2)); display: flex; align-items: center; justify-content: center; font-size: 22px; font-weight: 700; color: #fff; }
.profile-info { flex: 1; min-width: 0; }
.profile-name { font-size: 17px; font-weight: 700; line-height: 1.3; }
.profile-tgid { font-size: 12px; color: var(--text3); margin-top: 2px; }
.profile-card { background: var(--surface); border-radius: var(--radius); padding: 14px 16px; margin-bottom: 10px; box-shadow: var(--shadow); }
.profile-card-title { font-size: 11.5px; font-weight: 650; text-transform: uppercase; letter-spacing: .05em; color: var(--text3); margin-bottom: 10px; display: flex; align-items: center; gap: 6px; }
.profile-sub-row { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.profile-sub-date { font-size: 15px; font-weight: 600; }
.profile-sub-badge { padding: 3px 12px; border-radius: 999px; font-size: 12px; font-weight: 650; }
.profile-sub-badge.ok { background: rgba(52,199,89,.16); color: #34c759; }
.profile-sub-badge.warn { background: rgba(255,159,10,.16); color: #ff9f0a; }
.profile-sub-badge.danger { background: rgba(255,59,48,.16); color: var(--danger); }
.profile-sub-badge.expired { background: rgba(255,59,48,.16); color: var(--danger); }
.profile-group-badge { display: inline-block; padding: 3px 12px; border-radius: 999px; font-size: 12px; font-weight: 650; background: rgba(51,144,236,.16); color: var(--accent); }
.profile-device { display: flex; align-items: center; gap: 12px; padding: 9px 0; border-bottom: 1px solid var(--line); }
.profile-device:last-child { border-bottom: none; }
.profile-device-icon { font-size: 18px; flex-shrink: 0; }
.profile-device-info { flex: 1; min-width: 0; }
.profile-device-label { font-size: 14px; font-weight: 500; }
.profile-device-meta { font-size: 11px; color: var(--text3); margin-top: 1px; }
.profile-empty { color: var(--text3); font-size: 13px; padding: 10px 2px; }

/* ── Строки-элементы (профили синхронизации, плейлисты IPTV) ── */
.sp-row { display: flex; align-items: center; gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--line); }
.sp-row:last-child { border-bottom: none; }
.sp-icon { width: 34px; height: 34px; border-radius: 10px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; font-size: 14px; font-weight: 650; background: linear-gradient(135deg, var(--accent), var(--accent2)); color: #fff; }
.sp-info { flex: 1; min-width: 0; }
.sp-name { font-size: 14px; font-weight: 550; display: flex; align-items: center; gap: 6px; }
.sp-meta { font-size: 11px; color: var(--text3); margin-top: 1px; }
.sp-actions { display: flex; gap: 6px; flex-shrink: 0; }
.sp-pin-tag { font-size: 10px; padding: 2px 8px; border-radius: 999px; font-weight: 650; background: rgba(51,144,236,.14); color: var(--accent); }
.sp-pin-tag.off { background: var(--surface-3); color: var(--text3); }
.sp-add { display: flex; align-items: center; justify-content: center; width: 100%; padding: 11px; border: 1px dashed var(--line2); background: transparent; border-radius: var(--radius-sm); color: var(--accent); font-weight: 550; font-size: 14px; cursor: pointer; margin-top: 10px; transition: background .15s; }
.sp-add:active { background: var(--surface-2); }
.sp-help { font-size: 11.5px; color: var(--text3); margin-top: 10px; margin-bottom: 12px; line-height: 1.5; }

/* Список найденных озвучек. Раньше это были чипы inline-flex: у длинных имён строка
   рвалась ПОСРЕДИ подписи, и чекбокс следующего оказывался в середине предыдущего —
   на телефоне выходила каша (жалоба со скриншотом 2026-09-11). Теперь строки: одна
   озвучка — одна строка, крупная зона нажатия, ничего не переносится в середине. */
/* Строка подписки. Раньше это был .sp-row: название, кнопка «Озвучки: …» и
   «Отписаться» в один ряд — на «Дюна: Пророчество» название рвалось на две
   строки, а перечень озвучек всё равно не влезал. Теперь выбор озвучек занимает
   свою строку во всю ширину, и в неё помещается то, ради чего её открывают. */
.nf-sub { display: flex; gap: 12px; padding: 12px 0; align-items: flex-start; border-bottom: 1px solid var(--line); }
.nf-sub:last-of-type { border-bottom: none; }
.nf-sub-body { flex: 1; min-width: 0; }
.nf-sub-title { font-size: 14.5px; font-weight: 600; letter-spacing: -.01em; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.nf-sub-meta { font-size: 11.5px; color: var(--text3); margin-top: 1px; }
.nf-voices { display: flex; align-items: center; gap: 8px; width: 100%; margin-top: 8px;
  background: var(--surface-2); border: none; border-radius: 10px; padding: 9px 11px;
  font-size: 12.5px; color: var(--text); cursor: pointer; text-align: left; font-family: inherit; }
.nf-voices:active { background: var(--surface-3); }
.nf-voices > span { color: var(--text3); flex-shrink: 0; }
.nf-voices > b { font-weight: 600; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.nf-x { width: 30px; height: 30px; border-radius: 50%; border: none; flex-shrink: 0; cursor: pointer;
  background: rgba(255,59,48,.14); color: var(--danger); font-size: 14px; line-height: 1; }
/* Круглые кнопки-иконки: две текстовые («Обновить»/«Удалить») отжимали название
   плейлиста в две строки на 375 px. */
.ico-btn { width: 30px; height: 30px; border-radius: 50%; border: none; flex-shrink: 0; cursor: pointer;
  background: var(--surface-3); color: var(--text2); font-size: 14px; line-height: 1; }
.ico-btn:active { background: var(--line2); }
.sp-nm { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.nfv-any { display: flex !important; align-items: center; font-size: 14px !important;
  color: var(--text) !important; background: var(--surface-2); border-radius: 10px;
  padding: 11px 12px; cursor: pointer; margin-bottom: 0 !important; }
.nfv-list { display: flex; flex-direction: column; gap: 2px; margin-top: 6px; max-height: 46vh; overflow-y: auto; -webkit-overflow-scrolling: touch; }
.nfv-item { display: flex; align-items: center; gap: 10px; padding: 11px 12px; border-radius: 10px; cursor: pointer; background: var(--surface-2); font-size: 14px; line-height: 1.25; transition: background .12s; }
.nfv-item:active { background: var(--surface-3); }
.nfv-item input { flex-shrink: 0; width: 18px; height: 18px; margin: 0; accent-color: var(--accent); }
.nfv-item span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.nfv-item.on { background: rgba(51,144,236,.14); font-weight: 600; }
.nfv-count { font-size: 11px; color: var(--text3); margin-left: 6px; font-weight: 400; }

/* ── Кнопки ── */
.btn {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 9px 16px; border: none; border-radius: 10px;
  font-size: 14px; font-weight: 550; cursor: pointer;
  background: var(--surface-3); color: var(--text);
  transition: transform .1s, background .15s, opacity .15s;
}
.btn:active { transform: scale(.97); }
.btn-primary { background: var(--accent); color: var(--on-accent); }
.btn-primary:hover { background: var(--accent-hover); }
.btn-danger { background: rgba(255,59,48,.14); color: var(--danger); font-size: 12px; padding: 7px 12px; }
.btn-danger:hover { background: rgba(255,59,48,.22); }
.btn-sm { padding: 7px 12px; font-size: 13px; }

/* ── Переключатели ── */
.toggle-row { background: var(--surface); border-radius: var(--radius-sm); padding: 12px 14px; margin-bottom: 6px; display: flex; align-items: center; justify-content: space-between; gap: 10px; box-shadow: var(--shadow); }
.toggle-label { font-size: 15px; font-weight: 550; letter-spacing: -.01em; }
.toggle-sub { font-size: 11.5px; color: var(--text3); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.switch { position: relative; width: 46px; height: 27px; flex-shrink: 0; }
.switch input { opacity: 0; width: 0; height: 0; }
.switch .slider { position: absolute; inset: 0; background: var(--surface-3); border-radius: 999px; cursor: pointer; transition: background .2s; }
.switch .slider:before { content: ""; position: absolute; width: 21px; height: 21px; left: 3px; bottom: 3px; background: #fff; border-radius: 50%; box-shadow: 0 1px 3px rgba(0,0,0,.25); transition: transform .2s; }
.switch input:checked + .slider { background: var(--success); }
.switch input:checked + .slider:before { transform: translateX(19px); }
.switch input:disabled + .slider { opacity: .4; cursor: default; }

/* ── Поля ── */
.token-row { margin: 0 0 10px; padding: 0 2px; }
.token-input { width: 100%; padding: 10px 12px; border: 1px solid var(--line); border-radius: 10px; font-size: 13px; background: var(--input-bg); color: var(--text); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.token-input:focus { border-color: var(--accent); outline: none; }

/* ── Экран балансеров ── */
.group-header { display: flex; align-items: center; justify-content: space-between; padding: 18px 4px 8px; }
.group-title { font-weight: 650; font-size: 15px; display: flex; align-items: center; gap: 7px; letter-spacing: -.01em; }
.group-count { font-size: 12px; color: var(--text3); font-weight: 400; margin-left: 4px; }
.bal-row { background: var(--surface); padding: 11px 14px; display: flex; align-items: center; justify-content: space-between; gap: 10px; position: relative; }
.bal-row + .bal-row::before { content: ''; position: absolute; left: 14px; right: 0; top: 0; height: 1px; background: var(--line); }
.bal-row:first-of-type { border-radius: var(--radius) var(--radius) 0 0; }
.bal-group { border-radius: var(--radius); overflow: hidden; box-shadow: var(--shadow); }
.bal-row.disabled { opacity: .45; }
.bal-row.locked .bal-name { opacity: .55; }
.bal-lock { font-size: 11px; color: var(--warn); display: block; margin-top: 1px; }
.bal-name { font-size: 14.5px; font-weight: 550; }
.bal-quality { font-size: 10.5px; color: var(--accent); margin-left: 6px; font-weight: 650; }
.bal-note { font-size: 11px; color: var(--text3); display: block; margin-top: 1px; }

/* ── Профили ALPAC (те же, что на ТВ): плитки и редактор ── */
.ap-count { opacity: .5; font-weight: 500; text-transform: none; letter-spacing: 0; margin-left: 2px; }
.ap-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px 6px; margin: 2px 0 2px; }
.ap-tile { display: flex; flex-direction: column; align-items: center; gap: 6px; padding: 8px 2px 6px; border: none; background: transparent; color: var(--text); cursor: pointer; border-radius: var(--radius-sm); transition: background .15s, transform .1s; min-width: 0; font: inherit; }
.ap-tile:active { transform: scale(.96); background: var(--surface-2); }
.ap-ava, .ap-big, .ap-mini { display: flex; align-items: center; justify-content: center; overflow: hidden; color: #fff; flex-shrink: 0; }
.ap-ava { width: 64px; height: 64px; border-radius: 20px; box-shadow: 0 6px 18px rgba(0,0,0,.18); }
.ap-ava img, .ap-big img, .ap-mini img { width: 100%; height: 100%; object-fit: cover; display: block; }
.ap-ava .ap-emo { font-size: 32px; line-height: 1; }
.ap-ava .ap-ltr { font-size: 26px; font-weight: 750; }
.ap-ava-add { background: var(--surface-2); color: var(--accent); font-size: 30px; font-weight: 400; box-shadow: inset 0 0 0 1.5px var(--line2); }
.ap-nm { font-size: 13.5px; font-weight: 600; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ap-tags { display: flex; gap: 4px; min-height: 16px; }
.ap-tags i { font-style: normal; font-size: 10.5px; font-weight: 650; padding: 1px 6px; border-radius: 999px; background: var(--surface-2); color: var(--text2); }
.ap-hero { display: flex; flex-direction: column; align-items: center; gap: 12px; margin: 0 0 16px; }
.ap-big { width: 112px; height: 112px; border-radius: 34px; box-shadow: 0 14px 40px rgba(0,0,0,.28); }
.ap-big .ap-emo { font-size: 58px; line-height: 1; }
.ap-big .ap-ltr { font-size: 46px; font-weight: 750; }
.ap-photo-btns { display: flex; gap: 8px; justify-content: center; flex-wrap: wrap; }
.ap-photo-pick { position: relative; overflow: hidden; }
.ap-photo-pick input { position: absolute; top: 0; left: 0; width: 100%; height: 100%; opacity: 0; cursor: pointer; }
.ap-label { font-size: 12.5px; color: var(--text2); margin: 4px 0 7px; }
.ap-colors { display: flex; gap: 10px; margin-bottom: 14px; }
.ap-sw { width: 38px; height: 38px; border-radius: 12px; border: none; cursor: pointer; transition: transform .1s; }
.ap-sw:active { transform: scale(.92); }
.ap-sw.on { box-shadow: 0 0 0 2.5px var(--bg), 0 0 0 5px var(--text); }
.ap-emojis { display: grid; grid-template-columns: repeat(auto-fill, minmax(42px, 1fr)); gap: 6px; margin-bottom: 14px; }
.ap-em { height: 42px; border: none; border-radius: 12px; background: var(--surface-2); font-size: 22px; cursor: pointer; color: var(--text); font-family: inherit; }
.ap-em-l { font-size: 15px; font-weight: 700; }
.ap-em.on { background: var(--text); color: var(--bg); }
.ap-toggle { margin-bottom: 12px; box-shadow: none; background: var(--surface-2); }
.ap-sub { font-size: 12px; color: var(--text3); margin-top: 2px; }
.ap-pin { display: flex; gap: 8px; align-items: center; }
.ap-pin input { flex: 1; min-width: 0; padding: 10px 12px; border: 1px solid var(--line); border-radius: 10px; font-size: 20px; letter-spacing: 8px; background: var(--input-bg); color: var(--text); text-align: center; }
.ap-pin input:focus { border-color: var(--accent); outline: none; }
.ap-pin input:disabled { opacity: .45; }
.ap-pin .btn.on { background: rgba(255,59,48,.14); color: var(--danger); }
.ap-pin-sub { margin: 6px 2px 16px; }
.ap-save { width: 100%; padding: 13px; font-size: 15.5px; margin-top: 4px; }
.ap-save:disabled { opacity: .6; }
.ap-del { width: 100%; margin-top: 8px; background: transparent; color: var(--danger); }
.ap-mini { width: 20px; height: 20px; border-radius: 7px; }
.ap-mini .ap-emo { font-size: 12px; line-height: 1; }
.ap-mini .ap-ltr { font-size: 10px; font-weight: 700; }
.ap-devchip { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text2); margin-top: 4px; }

/* ── Модалка ── */
.modal-overlay { display: none; position: fixed; inset: 0; z-index: 100; background: rgba(0,0,0,.55); align-items: flex-end; justify-content: center; padding: 0; backdrop-filter: blur(6px); -webkit-backdrop-filter: blur(6px); }
.modal-overlay.active { display: flex; }
.modal { background: var(--bg); border-radius: 20px 20px 0 0; padding: 22px 18px calc(26px + env(safe-area-inset-bottom)); width: 100%; max-width: 520px; position: relative; animation: sheet .22s cubic-bezier(.2,.8,.3,1); max-height: 92vh; overflow-y: auto; }
@keyframes sheet { from { transform: translateY(30px); opacity: .4; } }
.modal h3 { font-size: 19px; font-weight: 700; letter-spacing: -.02em; margin-bottom: 16px; padding-right: 32px; }
.modal .close { position: absolute; top: 14px; right: 16px; background: var(--surface-3); border: none; width: 30px; height: 30px; border-radius: 50%; font-size: 18px; line-height: 1; cursor: pointer; color: var(--text2); }
.form-group { margin-bottom: 14px; }
.form-group label { display: block; font-size: 12.5px; color: var(--text2); margin-bottom: 5px; }
.form-group input { width: 100%; padding: 11px 12px; border: 1px solid var(--line); border-radius: 10px; font-size: 15px; background: var(--input-bg); color: var(--text); }
.form-group input:focus { border-color: var(--accent); outline: none; }
/* width:100% выше писался ВСЕМ input внутри .form-group — включая radio и checkbox.
   Точка радиокнопки рисовалась по центру растянутой на всю строку коробки, и
   переключатель «Сразу по выходу / Одним сводом» выглядел оторванным от подписей. */
.form-group input[type="radio"], .form-group input[type="checkbox"] {
  width: auto; margin-right: 8px; accent-color: var(--accent); vertical-align: middle;
}
.form-group input[type="time"] { width: auto; min-width: 112px; }

/* Сегментный переключатель — вместо пары радиокнопок */
.seg { display: flex; background: var(--surface-2); border-radius: var(--radius-sm); padding: 3px; gap: 3px; }
.seg button { flex: 1; border: none; background: transparent; color: var(--text2); font-size: 14px;
  font-weight: 550; padding: 10px 6px; border-radius: 9px; cursor: pointer; transition: background .15s, color .15s; }
.seg button.on { background: var(--surface); color: var(--text); box-shadow: 0 1px 3px rgba(0,0,0,.14); }
.device-code { font-size: 28px; font-weight: 700; text-align: center; padding: 16px; background: var(--surface-2); border-radius: var(--radius-sm); letter-spacing: 4px; color: var(--accent); margin: 12px 0; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.device-steps { font-size: 14px; color: var(--text2); margin: 12px 0; }
.device-steps a { color: var(--accent); }
.spinner { display: inline-block; width: 16px; height: 16px; border: 2px solid #fff; border-top-color: transparent; border-radius: 50%; animation: spin .6s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.toast { position: fixed; bottom: calc(84px + env(safe-area-inset-bottom)); left: 50%; transform: translateX(-50%); background: rgba(28,28,32,.95); color: #fff; padding: 11px 20px; border-radius: 999px; font-size: 14px; z-index: 200; opacity: 0; transition: opacity .3s; pointer-events: none; box-shadow: 0 6px 24px rgba(0,0,0,.3); max-width: 90vw; text-align: center; }
.toast.show { opacity: 1; }
.loading { text-align: center; padding: 48px 0; color: var(--text3); font-size: 14px; }
</style>
</head>
<body>
<div id="app">
  <div class="loading" id="loading">...</div>
</div>

<div class="modal-overlay" id="modal-overlay">
  <div class="modal" id="modal-content"></div>
</div>

<div class="toast" id="toast"></div>

<script>
(function(){
'use strict';

var API = '{API_BASE}';
var tg = window.Telegram && window.Telegram.WebApp;
var initData = '';
var config = {};
var hasChanges = false;
var activeTab = 'home';
var bindsDisabled = false; // admin kill-switch for the "Привязки" tab (from _bindsDisabled)
var balancerData = null;
var balancerVisibility = {};

// ---- i18n ----

var i18n = {
  ru: {
    loading: 'Загрузка...',
    initDataEmpty: '<b>initData пуст</b><br>Telegram WebApp: {tg}<br>Откройте через кнопку в боте',
    error: 'Ошибка: ',
    loadError: 'Ошибка загрузки: ',
    tabBinds: 'Привязки',
    tabBalancers: 'Балансеры',
    tabIptv: 'IPTV',
    tabNotify: 'Подписки',
    nfWhen: 'Когда беспокоить',
    nfInstant: 'Сразу по выходу',
    nfDigest: 'Одним сводом',
    nfDigestAt: 'Время свода',
    nfQuiet: 'Не беспокоить',
    nfQuietFrom: 'с',
    nfQuietTo: 'до',
    nfQueued: 'Ждёт отправки',
    nfSendNow: 'Прислать сейчас',
    nfWhenHint: 'Отложенное не теряется — придёт, когда можно будет.',
    nfSave: 'Сохранить',
    nfVoicesAny: 'любая новая',
    nfVoicesTitle: 'Каких озвучек ждать',
    nfVoicesAll: 'Любую новую',
    nfVoicesOwn: 'Или впишите свою',
    nfVoicesSave: 'Сохранить',
    nfVoicesKnown: 'Уже найдены',
    nfVoicesNone: 'Пока не знаем, какие есть — появятся после первой проверки. Можно вписать вручную.',
    nfVoicesHintPick: 'Совпадение нестрогое: «кубик» поймает и «Кубик в Кубе (18+)».',
    nfTitle: 'Мои подписки',
    nfEmpty: 'Подписок нет. Нажмите 🔔 на карточке фильма или сериала в приложении.',
    nfWhere: 'Куда присылать',
    nfTg: 'В Telegram',
    nfApp: 'В приложение',
    nfVoices: 'Озвучки',
    nfUnsub: 'Отписаться',
    nfInbox: 'Непрочитанные',
    nfReadAll: 'Отметить прочитанными',
    nfClear: 'Очистить',
    nfDone: 'Готово',
    nfSeries: 'сериал',
    nfMovie: 'фильм',
    nfVoicesHint: 'Для сериалов и фильмов можно отдельно ждать новую озвучку.',
    iptvTitle: 'Мои плейлисты',
    iptvEmpty: 'Плейлистов нет',
    iptvAdd: 'Добавить плейлист',
    iptvUrl: 'Ссылка на M3U',
    iptvName: 'Название (необязательно)',
    iptvUa: 'User-Agent (если провайдер требует)',
    iptvEpg: 'Адрес программы EPG (иначе возьмём из плейлиста)',
    iptvProxy: 'Через сервер',
    iptvGlobal: 'общий',
    iptvChannels: 'каналов',
    iptvRefresh: 'Обновить',
    iptvDelete: 'Удалить',
    iptvSaved: 'Плейлист добавлен',
    iptvRemoved: 'Плейлист удалён',
    iptvRefreshing: 'Обновляем…',
    iptvNeedUrl: 'Укажите ссылку',
    tsTitle: 'Свой TorrServer',
    tsHint: 'Адрес применится на всех ваших устройствах, где не введён свой локально. Пусто — сервер приложения. Обычно адрес начинается с http:// (https — только если ваш TorrServer сам за TLS).',
    tsUrl: 'Адрес TorrServer',
    tsSave: 'Сохранить',
    tsClear: 'Сбросить',
    tsSaved: 'TorrServer сохранён',
    tsCleared: 'Сброшено — используется сервер приложения',
    ytTitle: 'YouTube-аккаунт',
    ytHint: 'Подключите аккаунт, чтобы в разделе YouTube появилась лента ваших подписок. Доступ только на чтение — мы ничего не публикуем и не меняем.',
    ytConnect: 'Подключить YouTube',
    ytUnlink: 'Отключить',
    ytLinked: 'Подключён',
    ytStep: 'Откройте страницу и введите код:',
    ytOpen: 'Открыть google.com/device',
    ytCopy: 'Скопировать код',
    ytCopied: 'Код скопирован',
    ytWaiting: 'Ждём подтверждения в Google…',
    ytDone: 'YouTube подключён',
    ytUnlinked: 'YouTube отключён',
    tabServices: 'Сервисы',
    ytShorts: 'Shorts в ленте',
    ytShortsHint: 'Короткие вертикальные ролики. Настройка общая для всех ваших устройств.',
    ytShortsAll: 'Показывать',
    ytShortsHide: 'Скрыть',
    ytShortsOnly: 'Только Shorts',
    ytShortsSaved: 'Настройка сохранена',
    tabProfile: 'Профиль',
    balPremium: 'доступно в Премиуме',
    balEmpty: 'Нет доступных источников',
    grSources: 'Источники',
    grSourcesSub: 'плагин Lampa',
    grShared: 'Общее',
    grSharedSub: 'и в Lampa, и в приложении',
    grApp: 'Приложение ALPAC',
    grAppSub: 'аккаунт и уведомления',
    rowBindsSub: 'Filmix, KinoPub — по токену',
    rowBalancersSub: 'какие источники искать',
    rowTsTitle: 'TorrServer',
    scrServicesSub: 'свой сервер раздач и аккаунт YouTube',
    rowTsSub: 'свой сервер раздач',
    rowYtTitle: 'YouTube',
    rowYtSub: 'аккаунт и Shorts в ленте',
    rowIptvSub: 'свои и общие списки каналов',
    rowNotifySub: 'серии, фильмы и озвучки',
    rowProfileTitle: 'Профиль и устройства',
    rowProfileSub: 'подписка, входы, профили, CUB',
    heroSignedIn: 'вход через Telegram',
    heroPlan: 'подписка',
    heroLeft: 'осталось',
    heroDevices: 'устройства',
    heroNoPlan: 'нет',
    tsDefault: 'сервер приложения',
    ofN: 'из {n}',
    nUnread: '{n} новых',
    boundN: '{n} из {t}',
    dashValue: '—',
    subscription: 'Подписка',
    until: 'до',
    expired: 'Истёк',
    group: 'Группа',
    devices: 'Устройства',
    noDevices: 'Нет привязанных устройств',
    spSection: 'Профили синхронизации Лампы',
    spLoading: 'Загрузка…',
    spEmpty: 'Пока нет профилей',
    spAdd: '+ Создать профиль',
    spHelp: 'Профиль — отдельный набор закладок/таймкодов (например «Дети», «Жена»). На ТВ вводите имя + PIN в «Синхронизация → Войти по PIN».',
    cubLinked: 'Привязан',
    cubHelp: 'Запасной ключ входа: если приставка потеряет авторизацию, Лампа восстановит её по вашему аккаунту CUB.',
    cubBind: 'Привязать по коду',
    cubUnbind: 'Отвязать',
    cubCodeTitle: 'Привязать CUB',
    cubCodeHelp: 'Откройте {url} на телефоне или компьютере, войдите в свой аккаунт CUB и введите код из 6 цифр.',
    cubCodeNote: 'В аккаунте CUB появится новое устройство — после привязки его можно удалить.',
    cubLinkedToast: 'CUB привязан',
    cubUnlinkedToast: 'CUB отвязан',
    cubErrBadCode: 'Код не подошёл — проверьте цифры или получите новый',
    cubErrUnreachable: 'CUB сейчас не отвечает — попробуйте позже',
    cubErrRate: 'Слишком много попыток — подождите несколько минут',
    cubErrNoAccount: 'Сначала войдите в ALPAC через бота',
    spPinOn: 'PIN',
    spPinOff: 'без PIN',
    spLastLogin: 'Последний вход: {when}',
    spNever: 'Ещё не использовался',
    spJustNow: 'только что',
    spMinAgo: '{n} мин назад',
    spHourAgo: '{n} ч назад',
    spDayAgo: '{n} дн. назад',
    spPromptName: 'Имя профиля (3-32, латиница/цифры/_/-):',
    spPromptPin: 'PIN для входа с ТВ (4-8 цифр), пусто — без PIN:',
    spPromptNewPin: 'Новый PIN для «{name}» (4-8 цифр), пусто — убрать PIN:',
    spConfirmDelete: 'Удалить профиль «{name}»? Войти в него после удаления будет нельзя.',
    spCreated: 'Профиль создан',
    spPinUpdated: 'PIN обновлён',
    spDeleted: 'Профиль удалён',
    spNetErr: 'Ошибка сети',
    spErr: 'Ошибка: {e}',
    apSection: 'Профили ALPAC',
    apHelp: 'Те же профили, что на телевизоре: у каждого своя история, закладки и настройки. Здесь их удобно переименовать, поставить своё фото или PIN.',
    apNew: 'Новый',
    apNewTitle: 'Новый профиль',
    apEditTitle: 'Профиль',
    apName: 'Имя',
    apNamePh: 'Например, Мама',
    apColor: 'Цвет',
    apAvatar: 'Аватар',
    apPhoto: 'Своё фото',
    apPhotoChange: 'Другое фото',
    apPhotoRemove: 'Убрать фото',
    apPhotoBusy: 'Загружаю фото…',
    apPhotoSaved: 'Фото сохранено',
    apPhotoErr: 'Не удалось загрузить фото',
    apPhotoTooMany: 'Слишком много загрузок — попробуйте через час',
    apKids: 'Детский профиль',
    apKidsSub: 'Только детские фильмы и мультфильмы',
    apKidsTag: 'Детский',
    apPinTag: 'PIN',
    apPin: 'PIN входа',
    apPinSub: '4 цифры — чтобы в профиль не заходили другие',
    apPinSet: 'PIN установлен — введите новый, чтобы сменить',
    apPinRemove: 'Убрать PIN',
    apPinWillRemove: 'PIN будет убран',
    apSave: 'Сохранить',
    apCreate: 'Создать',
    apDelete: 'Удалить профиль',
    apConfirmDelete: 'Удалить профиль «{name}»? Его история, закладки и настройки пропадут на всех устройствах.',
    apSaved: 'Сохранено',
    apCreated: 'Профиль создан',
    apDeleted: 'Профиль удалён',
    apMax: 'Можно до {n} профилей',
    apLast: 'Последний профиль удалить нельзя',
    apBadPin: 'PIN — это 4 цифры',
    apBadName: 'Имя — до 20 символов',
    apNoAccount: 'Профили появятся, когда вы войдёте в приложение ALPAC',
    apErr: 'Ошибка: {e}',
    apLoading: 'Загрузка…',
    bindServices: 'Привязка сервисов',
    bound: '✓ Привязан',
    notBound: 'Не привязан',
    bind: 'Привязать',
    sources: 'Источники',
    tokenPlaceholder: 'Токен (оставьте пустым для привязки)',
    loadingBalancers: 'Загрузка балансеров...',
    globallyOff: 'выкл. глобально',
    userBound: 'ваш токен',
    confirmUnbind: 'Удалить привязку?',
    unbound: 'Привязка удалена',
    bindTitle: 'Привязка {name}',
    requestingCode: 'Запрос кода...',
    errorTitle: 'Ошибка',
    step1: '1. Откройте <a href="{url}" target="_blank">{url}</a>',
    step2: '2. Введите код:',
    step3: '3. После ввода кода нажмите кнопку ниже',
    finishBind: 'Завершить привязку',
    boundSuccess: '{name} привязан!',
    bindFailed: 'Не удалось завершить привязку',
    bindFailedHint: 'Убедитесь, что вы ввели код на сайте сервиса.',
    serviceUnavailable: 'Сервис недоступен',
    emailLogin: 'Email / Логин',
    password: 'Пароль',
    connectionError: 'Ошибка подключения',
    dataFrom: 'Данные из',
    save: 'Сохранить',
    saved: 'Сохранено!',
    saveError: 'Ошибка сохранения',
    tgYes: 'да',
    tgNo: 'нет'
  },
  uk: {
    loading: 'Завантаження...',
    initDataEmpty: '<b>initData порожній</b><br>Telegram WebApp: {tg}<br>Відкрийте через кнопку в боті',
    error: 'Помилка: ',
    loadError: 'Помилка завантаження: ',
    tabBinds: "Прив'язки",
    tabBalancers: 'Балансери',
    tabIptv: 'IPTV',
    tabNotify: 'Підписки',
    nfWhen: 'Коли турбувати',
    nfInstant: 'Одразу після виходу',
    nfDigest: 'Одним зведенням',
    nfDigestAt: 'Час зведення',
    nfQuiet: 'Не турбувати',
    nfQuietFrom: 'з',
    nfQuietTo: 'до',
    nfQueued: 'Чекає надсилання',
    nfSendNow: 'Надіслати зараз',
    nfWhenHint: 'Відкладене не втрачається — надійде, коли буде можна.',
    nfSave: 'Зберегти',
    nfVoicesAny: 'будь-яке нове',
    nfVoicesTitle: 'Яких озвучень чекати',
    nfVoicesAll: 'Будь-яке нове',
    nfVoicesOwn: 'Або впишіть своє',
    nfVoicesSave: 'Зберегти',
    nfVoicesKnown: 'Вже знайдені',
    nfVoicesNone: 'Поки не знаємо, які є — з’являться після першої перевірки. Можна вписати вручну.',
    nfVoicesHintPick: 'Збіг нестрогий: «кубик» зловить і «Кубик в Кубе (18+)».',
    nfTitle: 'Мої підписки',
    nfEmpty: 'Підписок немає. Натисніть 🔔 на картці фільму або серіалу в застосунку.',
    nfWhere: 'Куди надсилати',
    nfTg: 'У Telegram',
    nfApp: 'У застосунок',
    nfVoices: 'Озвучення',
    nfUnsub: 'Відписатися',
    nfInbox: 'Непрочитані',
    nfReadAll: 'Позначити прочитаними',
    nfClear: 'Очистити',
    nfDone: 'Готово',
    nfSeries: 'серіал',
    nfMovie: 'фільм',
    nfVoicesHint: 'Для серіалів і фільмів можна окремо чекати нове озвучення.',
    iptvTitle: 'Мої плейлісти',
    iptvEmpty: 'Плейлістів немає',
    iptvAdd: 'Додати плейліст',
    iptvUrl: 'Посилання на M3U',
    iptvName: 'Назва (необов\'язково)',
    iptvUa: 'User-Agent (якщо провайдер вимагає)',
    iptvEpg: 'Адреса програми EPG (інакше візьмемо з плейліста)',
    iptvProxy: 'Через сервер',
    iptvGlobal: 'спільний',
    iptvChannels: 'каналів',
    iptvRefresh: 'Оновити',
    iptvDelete: 'Видалити',
    iptvSaved: 'Плейліст додано',
    iptvRemoved: 'Плейліст видалено',
    iptvRefreshing: 'Оновлюємо…',
    iptvNeedUrl: 'Вкажіть посилання',
    tsTitle: 'Свій TorrServer',
    tsHint: 'Адреса застосується на всіх ваших пристроях, де не введено свою локально. Порожньо — сервер застосунку. Зазвичай адреса починається з http:// (https — лише якщо ваш TorrServer сам за TLS).',
    tsUrl: 'Адреса TorrServer',
    tsSave: 'Зберегти',
    tsClear: 'Скинути',
    tsSaved: 'TorrServer збережено',
    tsCleared: 'Скинуто — використовується сервер застосунку',
    ytTitle: 'YouTube-акаунт',
    ytHint: 'Підключіть акаунт, щоб у розділі YouTube з\'явилася стрічка ваших підписок. Доступ лише на читання — ми нічого не публікуємо і не змінюємо.',
    ytConnect: 'Підключити YouTube',
    ytUnlink: 'Відключити',
    ytLinked: 'Підключено',
    ytStep: 'Відкрийте сторінку та введіть код:',
    ytOpen: 'Відкрити google.com/device',
    ytCopy: 'Скопіювати код',
    ytCopied: 'Код скопійовано',
    ytWaiting: 'Чекаємо на підтвердження в Google…',
    ytDone: 'YouTube підключено',
    ytUnlinked: 'YouTube відключено',
    tabServices: 'Сервіси',
    ytShorts: 'Shorts у стрічці',
    ytShortsHint: 'Короткі вертикальні ролики. Налаштування спільне для всіх ваших пристроїв.',
    ytShortsAll: 'Показувати',
    ytShortsHide: 'Сховати',
    ytShortsOnly: 'Тільки Shorts',
    ytShortsSaved: 'Налаштування збережено',
    tabProfile: 'Профіль',
    balPremium: 'доступно в Преміумі',
    balEmpty: 'Немає доступних джерел',
    grSources: 'Джерела',
    grSourcesSub: 'плагін Lampa',
    grShared: 'Спільне',
    grSharedSub: 'і в Lampa, і в застосунку',
    grApp: 'Застосунок ALPAC',
    grAppSub: 'акаунт і сповіщення',
    rowBindsSub: 'Filmix, KinoPub — за токеном',
    rowBalancersSub: 'які джерела шукати',
    rowTsTitle: 'TorrServer',
    scrServicesSub: 'свій сервер роздач і акаунт YouTube',
    rowTsSub: 'свій сервер роздач',
    rowYtTitle: 'YouTube',
    rowYtSub: 'акаунт і Shorts у стрічці',
    rowIptvSub: 'свої та спільні списки каналів',
    rowNotifySub: 'серії, фільми та озвучення',
    rowProfileTitle: 'Профіль і пристрої',
    rowProfileSub: 'підписка, входи, профілі, CUB',
    heroSignedIn: 'вхід через Telegram',
    heroPlan: 'підписка',
    heroLeft: 'залишилось',
    heroDevices: 'пристрої',
    heroNoPlan: 'немає',
    tsDefault: 'сервер застосунку',
    ofN: 'з {n}',
    nUnread: '{n} нових',
    boundN: '{n} з {t}',
    dashValue: '—',
    subscription: 'Підписка',
    until: 'до',
    expired: 'Закінчився',
    group: 'Група',
    devices: 'Пристрої',
    noDevices: "Немає прив'язаних пристроїв",
    spSection: 'Профілі синхронізації Лампи',
    spLoading: 'Завантаження…',
    spEmpty: 'Поки немає профілів',
    spAdd: '+ Створити профіль',
    spHelp: "Профіль — окремий набір закладок/таймкодів (наприклад «Діти», «Дружина»). На ТВ вводьте ім'я + PIN у «Синхронізація → Увійти за PIN».",
    cubLinked: "Прив'язано",
    cubHelp: "Запасний ключ входу: якщо приставка втратить авторизацію, Лампа відновить її за вашим акаунтом CUB.",
    cubBind: "Прив'язати за кодом",
    cubUnbind: "Відв'язати",
    cubCodeTitle: "Прив'язати CUB",
    cubCodeHelp: "Відкрийте {url} на телефоні або комп'ютері, увійдіть у свій акаунт CUB і введіть код із 6 цифр.",
    cubCodeNote: "В акаунті CUB з'явиться новий пристрій — після прив'язки його можна видалити.",
    cubLinkedToast: "CUB прив'язано",
    cubUnlinkedToast: "CUB відв'язано",
    cubErrBadCode: 'Код не підійшов — перевірте цифри або отримайте новий',
    cubErrUnreachable: 'CUB зараз не відповідає — спробуйте пізніше',
    cubErrRate: 'Забагато спроб — зачекайте кілька хвилин',
    cubErrNoAccount: 'Спершу увійдіть в ALPAC через бота',
    spPinOn: 'PIN',
    spPinOff: 'без PIN',
    spLastLogin: 'Останній вхід: {when}',
    spNever: 'Ще не використовувався',
    spJustNow: 'щойно',
    spMinAgo: '{n} хв тому',
    spHourAgo: '{n} год тому',
    spDayAgo: '{n} дн. тому',
    spPromptName: "Ім'я профілю (3-32, латиниця/цифри/_/-):",
    spPromptPin: 'PIN для входу з ТВ (4-8 цифр), порожньо — без PIN:',
    spPromptNewPin: 'Новий PIN для «{name}» (4-8 цифр), порожньо — прибрати PIN:',
    spConfirmDelete: 'Видалити профіль «{name}»? Увійти в нього після видалення буде неможливо.',
    spCreated: 'Профіль створено',
    spPinUpdated: 'PIN оновлено',
    spDeleted: 'Профіль видалено',
    spNetErr: 'Помилка мережі',
    spErr: 'Помилка: {e}',
    apSection: 'Профілі ALPAC',
    apHelp: 'Ті самі профілі, що на телевізорі: у кожного своя історія, закладки й налаштування. Тут їх зручно перейменувати, поставити своє фото чи PIN.',
    apNew: 'Новий',
    apNewTitle: 'Новий профіль',
    apEditTitle: 'Профіль',
    apName: 'Ім’я',
    apNamePh: 'Наприклад, Мама',
    apColor: 'Колір',
    apAvatar: 'Аватар',
    apPhoto: 'Своє фото',
    apPhotoChange: 'Інше фото',
    apPhotoRemove: 'Прибрати фото',
    apPhotoBusy: 'Завантажую фото…',
    apPhotoSaved: 'Фото збережено',
    apPhotoErr: 'Не вдалося завантажити фото',
    apPhotoTooMany: 'Забагато завантажень — спробуйте за годину',
    apKids: 'Дитячий профіль',
    apKidsSub: 'Лише дитячі фільми й мультфільми',
    apKidsTag: 'Дитячий',
    apPinTag: 'PIN',
    apPin: 'PIN входу',
    apPinSub: '4 цифри — щоб у профіль не заходили інші',
    apPinSet: 'PIN встановлено — введіть новий, щоб змінити',
    apPinRemove: 'Прибрати PIN',
    apPinWillRemove: 'PIN буде прибрано',
    apSave: 'Зберегти',
    apCreate: 'Створити',
    apDelete: 'Видалити профіль',
    apConfirmDelete: 'Видалити профіль «{name}»? Його історія, закладки й налаштування зникнуть на всіх пристроях.',
    apSaved: 'Збережено',
    apCreated: 'Профіль створено',
    apDeleted: 'Профіль видалено',
    apMax: 'Можна до {n} профілів',
    apLast: 'Останній профіль видалити не можна',
    apBadPin: 'PIN — це 4 цифри',
    apBadName: 'Ім’я — до 20 символів',
    apNoAccount: 'Профілі з’являться, коли ви увійдете в застосунок ALPAC',
    apErr: 'Помилка: {e}',
    apLoading: 'Завантаження…',
    bindServices: "Прив'язка сервісів",
    bound: "✓ Прив'язаний",
    notBound: "Не прив'язаний",
    bind: "Прив'язати",
    sources: 'Джерела',
    tokenPlaceholder: "Токен (залиште порожнім для прив'язки)",
    loadingBalancers: 'Завантаження балансерів...',
    globallyOff: 'вимк. глобально',
    userBound: 'ваш токен',
    confirmUnbind: "Видалити прив'язку?",
    unbound: "Прив'язку видалено",
    bindTitle: "Прив'язка {name}",
    requestingCode: 'Запит коду...',
    errorTitle: 'Помилка',
    step1: '1. Відкрийте <a href="{url}" target="_blank">{url}</a>',
    step2: '2. Введіть код:',
    step3: '3. Після введення коду натисніть кнопку нижче',
    finishBind: "Завершити прив'язку",
    boundSuccess: "{name} прив'язано!",
    bindFailed: "Не вдалося завершити прив'язку",
    bindFailedHint: 'Переконайтесь, що ви ввели код на сайті сервісу.',
    serviceUnavailable: 'Сервіс недоступний',
    emailLogin: 'Email / Логін',
    password: 'Пароль',
    connectionError: "Помилка з'єднання",
    dataFrom: 'Дані з',
    save: 'Зберегти',
    saved: 'Збережено!',
    saveError: 'Помилка збереження',
    tgYes: 'так',
    tgNo: 'ні'
  },
  en: {
    loading: 'Loading...',
    initDataEmpty: '<b>initData is empty</b><br>Telegram WebApp: {tg}<br>Open via bot button',
    error: 'Error: ',
    loadError: 'Loading error: ',
    tabBinds: 'Binds',
    tabBalancers: 'Balancers',
    tabIptv: 'IPTV',
    tabNotify: 'Subscriptions',
    nfWhen: 'When to notify',
    nfInstant: 'As soon as it is out',
    nfDigest: 'One digest',
    nfDigestAt: 'Digest time',
    nfQuiet: 'Do not disturb',
    nfQuietFrom: 'from',
    nfQuietTo: 'to',
    nfQueued: 'Waiting to be sent',
    nfSendNow: 'Send now',
    nfWhenHint: 'Nothing is lost — deferred alerts arrive once it is allowed.',
    nfSave: 'Save',
    nfVoicesAny: 'any new one',
    nfVoicesTitle: 'Which dubs to wait for',
    nfVoicesAll: 'Any new one',
    nfVoicesOwn: 'Or type your own',
    nfVoicesSave: 'Save',
    nfVoicesKnown: 'Already found',
    nfVoicesNone: 'Not known yet — they appear after the first check. You can type one in.',
    nfVoicesHintPick: 'Matching is loose: "kubik" also catches "Кубик в Кубе (18+)".',
    nfTitle: 'My subscriptions',
    nfEmpty: 'No subscriptions yet. Tap 🔔 on a film or show in the app.',
    nfWhere: 'Where to send',
    nfTg: 'To Telegram',
    nfApp: 'To the app',
    nfVoices: 'Dubs',
    nfUnsub: 'Unsubscribe',
    nfInbox: 'Unread',
    nfReadAll: 'Mark all read',
    nfClear: 'Clear',
    nfDone: 'Done',
    nfSeries: 'show',
    nfMovie: 'film',
    nfVoicesHint: 'You can wait for a new dub separately, for shows and films alike.',
    iptvTitle: 'My playlists',
    iptvEmpty: 'No playlists yet',
    iptvAdd: 'Add playlist',
    iptvUrl: 'M3U link',
    iptvName: 'Name (optional)',
    iptvUa: 'User-Agent (if your provider requires one)',
    iptvEpg: 'EPG url (otherwise taken from the playlist)',
    iptvProxy: 'Through the server',
    iptvGlobal: 'shared',
    iptvChannels: 'channels',
    iptvRefresh: 'Refresh',
    iptvDelete: 'Delete',
    iptvSaved: 'Playlist added',
    iptvRemoved: 'Playlist removed',
    iptvRefreshing: 'Refreshing…',
    iptvNeedUrl: 'A link is required',
    tsTitle: 'Own TorrServer',
    tsHint: 'Applies on all your devices that have no local address set. Empty — the app server. Usually the address starts with http:// (https only if your TorrServer itself runs TLS).',
    tsUrl: 'TorrServer address',
    tsSave: 'Save',
    tsClear: 'Reset',
    tsSaved: 'TorrServer saved',
    tsCleared: 'Reset — using the app server',
    ytTitle: 'YouTube account',
    ytHint: 'Link your account to get your subscriptions feed in the YouTube section. Read-only access — we never post or change anything.',
    ytConnect: 'Link YouTube',
    ytUnlink: 'Unlink',
    ytLinked: 'Linked',
    ytStep: 'Open the page and enter the code:',
    ytOpen: 'Open google.com/device',
    ytCopy: 'Copy code',
    ytCopied: 'Code copied',
    ytWaiting: 'Waiting for confirmation in Google…',
    ytDone: 'YouTube linked',
    ytUnlinked: 'YouTube unlinked',
    tabServices: 'Services',
    ytShorts: 'Shorts in the feed',
    ytShortsHint: 'Short vertical videos. The setting applies on all your devices.',
    ytShortsAll: 'Show',
    ytShortsHide: 'Hide',
    ytShortsOnly: 'Only Shorts',
    ytShortsSaved: 'Setting saved',
    tabProfile: 'Profile',
    balPremium: 'included with Premium',
    balEmpty: 'No sources available',
    grSources: 'Sources',
    grSourcesSub: 'Lampa plugin',
    grShared: 'Shared',
    grSharedSub: 'Lampa and the app alike',
    grApp: 'ALPAC app',
    grAppSub: 'account and alerts',
    rowBindsSub: 'Filmix, KinoPub — by token',
    rowBalancersSub: 'which sources to search',
    rowTsTitle: 'TorrServer',
    scrServicesSub: 'your own seed server and YouTube account',
    rowTsSub: 'your own seed server',
    rowYtTitle: 'YouTube',
    rowYtSub: 'account and Shorts in the feed',
    rowIptvSub: 'your own and shared channel lists',
    rowNotifySub: 'episodes, movies and dubs',
    rowProfileTitle: 'Profile and devices',
    rowProfileSub: 'plan, sign-ins, profiles, CUB',
    heroSignedIn: 'signed in with Telegram',
    heroPlan: 'plan',
    heroLeft: 'left',
    heroDevices: 'devices',
    heroNoPlan: 'none',
    tsDefault: 'app server',
    ofN: 'of {n}',
    nUnread: '{n} new',
    boundN: '{n} of {t}',
    dashValue: '—',
    subscription: 'Subscription',
    until: 'until',
    expired: 'Expired',
    group: 'Group',
    devices: 'Devices',
    noDevices: 'No devices bound',
    spSection: 'Lampa sync profiles',
    spLoading: 'Loading…',
    spEmpty: 'No profiles yet',
    spAdd: '+ Create profile',
    spHelp: 'A profile is a separate set of bookmarks/timecodes (e.g. "Kids", "Wife"). Sign in on the TV with name + PIN under "Sync → Sign in with PIN".',
    cubLinked: 'Linked',
    cubHelp: 'A backup sign-in key: if a box loses its sign-in, Lampa restores it with your CUB account.',
    cubBind: 'Link with a code',
    cubUnbind: 'Unlink',
    cubCodeTitle: 'Link CUB',
    cubCodeHelp: 'Open {url} on a phone or computer, sign in to your CUB account and enter the 6-digit code.',
    cubCodeNote: 'A new device appears in your CUB account — you can remove it after linking.',
    cubLinkedToast: 'CUB linked',
    cubUnlinkedToast: 'CUB unlinked',
    cubErrBadCode: 'The code did not work — check the digits or get a new one',
    cubErrUnreachable: 'CUB is not responding — try again later',
    cubErrRate: 'Too many attempts — wait a few minutes',
    cubErrNoAccount: 'Sign in to ALPAC through the bot first',
    spPinOn: 'PIN',
    spPinOff: 'no PIN',
    spLastLogin: 'Last login: {when}',
    spNever: 'Never used',
    spJustNow: 'just now',
    spMinAgo: '{n} min ago',
    spHourAgo: '{n} h ago',
    spDayAgo: '{n} d ago',
    spPromptName: 'Profile name (3-32, [a-z0-9_-]):',
    spPromptPin: 'PIN for TV sign-in (4-8 digits), blank — no PIN:',
    spPromptNewPin: 'New PIN for "{name}" (4-8 digits), blank — clear PIN:',
    spConfirmDelete: 'Delete profile "{name}"? Sign-in will no longer be possible.',
    spCreated: 'Profile created',
    spPinUpdated: 'PIN updated',
    spDeleted: 'Profile deleted',
    spNetErr: 'Network error',
    spErr: 'Error: {e}',
    apSection: 'ALPAC profiles',
    apHelp: 'The same profiles as on your TV: each has its own history, bookmarks and settings. Rename them, set your own photo or a PIN here.',
    apNew: 'New',
    apNewTitle: 'New profile',
    apEditTitle: 'Profile',
    apName: 'Name',
    apNamePh: 'e.g. Mom',
    apColor: 'Color',
    apAvatar: 'Avatar',
    apPhoto: 'Your photo',
    apPhotoChange: 'Another photo',
    apPhotoRemove: 'Remove photo',
    apPhotoBusy: 'Uploading photo…',
    apPhotoSaved: 'Photo saved',
    apPhotoErr: 'Could not upload the photo',
    apPhotoTooMany: 'Too many uploads — try again in an hour',
    apKids: 'Kids profile',
    apKidsSub: 'Only kids’ movies and cartoons',
    apKidsTag: 'Kids',
    apPinTag: 'PIN',
    apPin: 'Entry PIN',
    apPinSub: '4 digits — so others can’t open this profile',
    apPinSet: 'PIN is set — enter a new one to change it',
    apPinRemove: 'Remove PIN',
    apPinWillRemove: 'PIN will be removed',
    apSave: 'Save',
    apCreate: 'Create',
    apDelete: 'Delete profile',
    apConfirmDelete: 'Delete profile “{name}”? Its history, bookmarks and settings will be gone on all devices.',
    apSaved: 'Saved',
    apCreated: 'Profile created',
    apDeleted: 'Profile deleted',
    apMax: 'Up to {n} profiles',
    apLast: 'The last profile can’t be deleted',
    apBadPin: 'The PIN is 4 digits',
    apBadName: 'Name — up to 20 characters',
    apNoAccount: 'Profiles appear once you sign in to the ALPAC app',
    apErr: 'Error: {e}',
    apLoading: 'Loading…',
    bindServices: 'Service binding',
    bound: '✓ Bound',
    notBound: 'Not bound',
    bind: 'Bind',
    sources: 'Sources',
    tokenPlaceholder: 'Token (leave empty for binding)',
    loadingBalancers: 'Loading balancers...',
    globallyOff: 'off globally',
    userBound: 'your token',
    confirmUnbind: 'Remove binding?',
    unbound: 'Binding removed',
    bindTitle: 'Bind {name}',
    requestingCode: 'Requesting code...',
    errorTitle: 'Error',
    step1: '1. Open <a href="{url}" target="_blank">{url}</a>',
    step2: '2. Enter the code:',
    step3: '3. After entering the code, press the button below',
    finishBind: 'Finish binding',
    boundSuccess: '{name} bound!',
    bindFailed: 'Failed to finish binding',
    bindFailedHint: 'Make sure you entered the code on the service website.',
    serviceUnavailable: 'Service unavailable',
    emailLogin: 'Email / Login',
    password: 'Password',
    connectionError: 'Connection error',
    dataFrom: 'Data from',
    save: 'Save',
    saved: 'Saved!',
    saveError: 'Save error',
    tgYes: 'yes',
    tgNo: 'no'
  }
};

// Detect language: server-side > Telegram user > default ru
var lang = '{USER_LANG}' || 'ru';
if (lang !== 'uk' && lang !== 'en' && lang !== 'ru') {
  lang = 'ru';
  if (tg && tg.initDataUnsafe && tg.initDataUnsafe.user) {
    var lc = tg.initDataUnsafe.user.language_code || '';
    if (lc === 'uk') lang = 'uk';
    else if (lc === 'en') lang = 'en';
  }
}

var L = i18n[lang] || i18n.ru;

function tr(key, params) {
  var s = L[key] || i18n.ru[key] || key;
  if (params) {
    for (var k in params) { s = s.split('{' + k + '}').join(params[k]); }
  }
  return s;
}

// HTML-escape user-supplied strings before embedding in innerHTML. The
// renderProfile / device-list code interpolates fields that originate from
// Telegram (display name, device label set by user) — without escaping, a
// username containing markup would execute in the WebView. The bkit page
// already had this helper; for the TG-WebApp variant we added it later
// when renderProfile was extended with avatars and device rows.
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

// Try multiple sources for initData
if (tg && tg.initData) {
  initData = tg.initData;
}
if (!initData) {
  try {
    var hash = location.hash.substring(1);
    var params = new URLSearchParams(hash);
    var tgData = params.get('tgWebAppData');
    if (tgData) initData = tgData;
  } catch(e) {}
}
if (!initData) {
  try {
    var qp = new URLSearchParams(location.search);
    var tgQ = qp.get('tgWebAppData');
    if (tgQ) initData = tgQ;
  } catch(e) {}
}

// ---- Тема ----
// Раньше пять цветов Telegram клались поверх ЖЁСТКО СВЕТЛОЙ палитры: в тёмной теме
// оставались светлые разделители rgba(0,0,0,.06) — то есть их не было видно вовсе.
// Теперь схему объявляет сам CSS (data-scheme + prefers-color-scheme), а цвета
// Telegram только доопределяют её. bg_color у Telegram — цвет КАРТОЧКИ, а фон
// страницы — secondary_bg_color; так же устроены сгруппированные списки в самом
// Telegram, поэтому раскладываем их именно так, а не наоборот.
function applyTheme() {
  if (!tg) return;
  var root = document.documentElement;
  root.setAttribute('data-scheme', tg.colorScheme === 'dark' ? 'dark' : 'light');
  var tp = tg.themeParams || {};
  var set = function(name, val) { if (val) root.style.setProperty(name, val); };
  set('--bg', tp.secondary_bg_color || tp.bg_color);
  set('--surface', tp.bg_color);
  set('--card-bg', tp.bg_color);
  set('--text', tp.text_color);
  set('--text2', tp.hint_color);
  set('--accent', tp.button_color || tp.link_color);
  set('--on-accent', tp.button_text_color);
  set('--input-bg', tp.secondary_bg_color);
  set('--danger', tp.destructive_text_color);
  var top = tp.secondary_bg_color || tp.bg_color;
  try { if (tg.setHeaderColor && top) tg.setHeaderColor(top); } catch(e) {}
  try { if (tg.setBackgroundColor && top) tg.setBackgroundColor(top); } catch(e) {}
}

// Отклик на нажатие. Мини-апп открывается поверх нативного клиента, и без вибро-
// отклика переходы ощущаются как веб-страница, а не как часть Telegram.
function haptic(kind) {
  try {
    var hf = tg && tg.HapticFeedback;
    if (!hf) return;
    if (kind === 'success' || kind === 'error' || kind === 'warning') hf.notificationOccurred(kind);
    else hf.impactOccurred(kind || 'light');
  } catch(e) {}
}

if (tg) {
  tg.expand();
  tg.ready();
  applyTheme();
  try { if (tg.onEvent) tg.onEvent('themeChanged', applyTheme); } catch(e) {}
  // Кнопка «назад» уводит на главный экран, а с главного — закрывает мини-апп.
  tg.BackButton.onClick(function(){
    if (activeTab !== 'home') { switchTab('home'); } else { tg.close(); }
  });
}

// Update loading text
document.getElementById('loading').textContent = tr('loading');

// Services that support bind flows
var bindServices = [
  {id:'filmix', name:'Filmix', badge:'Free', type:'device'},
  {id:'kinopub', name:'KinoPub', badge:'Premium', type:'device'},
  {id:'rezka', name:'PidoRezka', badge:'Premium', type:'login'},
  {id:'vokino', name:'VoKino', badge:'Premium', type:'login'},
  {id:'getstv', name:'GetsTV', badge:'Premium', type:'login'},
  {id:'iptvonline', name:'iptv.online', badge:'Premium', type:'apikey'},
  {id:'pornlab', name:'PornLab', badge:'Free', type:'login'}
];

// Token override balancers (bind tab)
var tokenBalancers = [
  {key:'Filmix', name:'Filmix'},
  {key:'KinoPub', name:'KinoPub'},
  {key:'Collaps', name:'Collaps'},
  {key:'Videoseed', name:'Videoseed'},
  {key:'Mirage', name:'Mirage'},
  {key:'Rezka', name:'PidoRezka'},
  {key:'VoKino', name:'VoKino'},
  {key:'GetsTV', name:'GetsTV'}
];

function api(method, path, body) {
  var opts = {method: method, headers: {'X-Telegram-Init-Data': initData}};
  if (body) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  return fetch(API + path, opts).then(function(r){ return r.json(); });
}

function toast(msg) {
  var el = document.getElementById('toast');
  el.textContent = msg;
  el.classList.add('show');
  setTimeout(function(){ el.classList.remove('show'); }, 2500);
}

function showModal(html) {
  document.getElementById('modal-content').innerHTML = '<button class="close" onclick="closeModal()">&times;</button>' + html;
  document.getElementById('modal-overlay').classList.add('active');
}
window.closeModal = function() {
  document.getElementById('modal-overlay').classList.remove('active');
};

// ---- Переходы между экранами ----
// Было шесть равноправных вкладок в одну строку — по ним не читалось ни что важнее,
// ни какой из двух продуктов настраиваешь. Стало: главный экран с группами и экраны
// второго уровня, «назад» — системной кнопкой Telegram.

var profileData = null;
var SCREENS = ['home', 'binds', 'balancers', 'iptv', 'services', 'youtube', 'notify', 'profile'];

window.switchTab = function(tab) {
  if (tab === 'binds' && bindsDisabled) tab = 'home'; // экран привязок выключен админом
  if (SCREENS.indexOf(tab) === -1) tab = 'home';
  activeTab = tab;
  for (var i = 0; i < SCREENS.length; i++) {
    var el = document.getElementById('scr-' + SCREENS[i]);
    if (el) el.style.display = (SCREENS[i] === tab) ? '' : 'none';
  }
  // Экран всегда открывается сверху: иначе переход из середины длинного списка
  // балансеров показывал новый экран уже прокрученным на чужую позицию.
  try { window.scrollTo(0, 0); } catch(e) {}
  haptic('light');
  if (tg && tg.BackButton) {
    if (tab === 'home') tg.BackButton.hide(); else tg.BackButton.show();
  }
  if (tab === 'balancers' && !balancerData) loadBalancers();
  if (tab === 'profile' && !profileData) loadProfile();
  if (tab === 'iptv' && !iptvData) loadIptv();
  if (tab === 'services') loadServices();
  if (tab === 'youtube') loadYouTube();
  if (tab === 'notify' && !nfData) loadNotify();
};

// ---- Tab: Subscriptions / notifications ----
// Управлять подписками здесь удобнее, чем в приложении: на телевизоре список правится
// пультом, а тут — пальцем, и сразу видно, куда уходят уведомления.

var nfData = null;   // {shows, notify_tg, notify_app}
var nfUnread = 0;
var nfDelivery = {};
var nfQueued = 0;
var nfModePick = 'instant'; // 'instant' | 'digest' — выбор сегментного переключателя

function loadNotify() {
  api('GET', '/calendar/subscriptions').then(function(d) {
    nfData = d && d.shows ? d : {shows: [], notify_tg: true, notify_app: true};
    // Счётчик непрочитанных — отдельным запросом: список подписок его не содержит.
    api('GET', '/calendar/notifications').then(function(n) {
      nfUnread = (n && n.unread) || 0;
      return api('GET', '/calendar/delivery');
    }).then(function(dl) {
      nfDelivery = (dl && dl.delivery) || {};
      nfQueued = (dl && dl.queued) || 0;
      renderNotify();
      renderHome();
    }).catch(function() { renderNotify(); });
  }).catch(function() {
    nfData = {shows: [], notify_tg: true, notify_app: true};
    renderNotify();
  });
}

function renderNotify() {
  var el = document.getElementById('tab-notify');
  if (!el) return;
  var d = nfData || {shows: [], notify_tg: true, notify_app: true};
  var h = '';

  // Куда присылать — первым: если оба канала выключены, всё остальное на экране
  // не имеет смысла, и человек должен увидеть это сразу.
  h += '<h2>' + tr('nfWhere') + '</h2><div class="section">';
  // Тумблеры, а не голые чекбоксы: на остальных экранах включение выглядит именно так.
  h += '<div class="toggle-row"><div class="toggle-label">' + tr('nfTg') + '</div>'
    + '<label class="switch"><input type="checkbox" ' + (d.notify_tg ? 'checked' : '')
    + ' onchange="nfSetChannel(' + jsArg('tg') + ',this.checked)"><span class="slider"></span></label></div>';
  h += '<div class="toggle-row"><div class="toggle-label">' + tr('nfApp') + '</div>'
    + '<label class="switch"><input type="checkbox" ' + (d.notify_app ? 'checked' : '')
    + ' onchange="nfSetChannel(' + jsArg('app') + ',this.checked)"><span class="slider"></span></label></div>';
  h += '</div>';

  // «Когда» идёт сразу за «куда»: это одна мысль — как именно до вас доносить
  // новости, — и разносить её по разным местам экрана было бы странно.
  var dl = nfDelivery || {};
  var isDigest = dl.mode === 'digest';
  nfModePick = isDigest ? 'digest' : 'instant';
  h += '<h2>' + tr('nfWhen') + '</h2><div class="section">';
  h += '<div class="seg" id="nf-seg">'
    + '<button type="button" class="' + (isDigest ? '' : 'on') + '" onclick="nfModeChanged(false)">' + tr('nfInstant') + '</button>'
    + '<button type="button" class="' + (isDigest ? 'on' : '') + '" onclick="nfModeChanged(true)">' + tr('nfDigest') + '</button>'
    + '</div>';
  h += '<div class="form-group" id="nf-digest-at" ' + (isDigest ? '' : 'style="display:none"') + '>'
    + '<label>' + tr('nfDigestAt') + '</label>'
    + '<input type="time" id="nf-at" value="' + esc(dl.digest_at || '20:00') + '"></div>';
  h += '<div class="form-group"><label>' + tr('nfQuiet') + '</label>'
    + '<div style="display:flex;align-items:center;gap:8px">'
    + '<span>' + tr('nfQuietFrom') + '</span><input type="time" id="nf-qfrom" value="' + esc(dl.quiet_from || '') + '">'
    + '<span>' + tr('nfQuietTo') + '</span><input type="time" id="nf-qto" value="' + esc(dl.quiet_to || '') + '">'
    + '</div></div>';
  h += '<div class="sp-help">' + tr('nfWhenHint') + '</div>';
  if (nfQueued > 0) {
    h += '<div class="sp-row"><div class="sp-info"><div class="sp-name">' + tr('nfQueued') + ': ' + nfQueued
      + '</div></div><div class="sp-actions">'
      + '<button class="btn btn-sm" onclick="nfFlush()">' + tr('nfSendNow') + '</button></div></div>';
  }
  h += '<button class="btn btn-primary" style="width:100%;margin-top:10px" onclick="nfSaveDelivery()">'
    + tr('nfSave') + '</button>';
  h += '</div>';

  h += '<h2>' + tr('nfInbox') + '</h2><div class="section">';
  h += '<div class="sp-row"><div class="sp-info"><div class="sp-name">' + tr('nfInbox') + ': ' + nfUnread + '</div></div>';
  h += '<div class="sp-actions">';
  h += '<button class="btn btn-sm" onclick="nfReadAll()">' + tr('nfReadAll') + '</button>';
  h += '<button class="btn btn-danger" onclick="nfClear()">' + tr('nfClear') + '</button>';
  h += '</div></div></div>';

  h += '<h2>' + tr('nfTitle') + '</h2><div class="section">';
  if (!d.shows.length) {
    h += '<div class="profile-empty">' + tr('nfEmpty') + '</div>';
  } else {
    for (var i = 0; i < d.shows.length; i++) {
      var sh = d.shows[i];
      var isMovie = sh.kind === 'movie';
      var key = (isMovie ? 'movie' : 'tv') + ':' + sh.tmdb_id;
      h += '<div class="nf-sub">';
      h += '<div class="sp-icon">' + (isMovie ? '\uD83C\uDFAC' : '\uD83D\uDCFA') + '</div>';
      h += '<div class="nf-sub-body"><div class="nf-sub-title">' + esc(sh.title || key) + '</div>';
      h += '<div class="nf-sub-meta">' + (isMovie ? tr('nfMovie') : tr('nfSeries'));
      if (!isMovie && sh.last_season) h += ' \u00b7 S' + sh.last_season + 'E' + (sh.last_episode || 0);
      h += '</div>';
      // Состояние озвучек словами, а не одной галочкой: «включено» и «жду вот эти
      // две» — разные вещи, и по чекбоксу их не различить.
      var want = sh.want_voices || [];
      var vLabel = !sh.track_voices ? '—'
        : want.length ? esc(want.join(', '))
        : tr('nfVoicesAny');
      h += '<button class="nf-voices" onclick="nfVoicesDialog(' + jsArg(key) + ')">'
        + '<span>' + tr('nfVoices') + '</span><b>' + vLabel + '</b><i class="chev"></i></button>';
      h += '</div>';
      h += '<button class="nf-x" title="' + tr('nfUnsub') + '" onclick="nfUnsub(' + jsArg(key) + ')">\u2715</button>';
      h += '</div>';
    }
    h += '<div class="sp-help">' + tr('nfVoicesHint') + '</div>';
  }
  h += '</div>';

  el.innerHTML = h;
}

window.nfModeChanged = function(digest) {
  nfModePick = digest ? 'digest' : 'instant';
  var seg = document.getElementById('nf-seg');
  if (seg) {
    var bs = seg.getElementsByTagName('button');
    if (bs.length === 2) { bs[0].className = digest ? '' : 'on'; bs[1].className = digest ? 'on' : ''; }
  }
  var box = document.getElementById('nf-digest-at');
  if (box) box.style.display = digest ? '' : 'none';
  haptic('light');
};

window.nfSaveDelivery = function() {
  var mode = nfModePick === 'digest' ? 'digest' : 'instant';
  var body = {
    mode: mode,
    digest_at: (document.getElementById('nf-at') || {}).value || '',
    quiet_from: (document.getElementById('nf-qfrom') || {}).value || '',
    quiet_to: (document.getElementById('nf-qto') || {}).value || '',
    // Пояс берём у браузера: сервер живёт в UTC, и без этого «в 20:00» означало бы
    // двадцать часов UTC, а не восемь вечера у пользователя.
    tz_offset: -new Date().getTimezoneOffset()
  };
  api('POST', '/calendar/delivery', body).then(function(r) {
    if (r && r.error) { toast(String(r.error)); return; }
    nfDelivery = r.delivery || body;
    toast(tr('nfDone'));
  });
};

window.nfFlush = function() {
  api('POST', '/calendar/delivery/flush', {}).then(function() {
    nfQueued = 0;
    renderNotify();
    toast(tr('nfDone'));
  });
};

window.nfSetChannel = function(channel, on) {
  api('POST', '/calendar/notify', {channel: channel, enabled: on}).then(function(r) {
    if (r && r.error) { toast(String(r.error)); return; }
    if (nfData) { if (channel === 'app') nfData.notify_app = on; else nfData.notify_tg = on; }
    toast(tr('nfDone'));
  });
};

/**
 * Диалог «каких озвучек ждать».
 *
 * Готовые чипы — из уже найденных по этому тайтлу озвучек, но ОБЯЗАТЕЛЬНО с полем
 * ручного ввода: ждут обычно то, чего ещё НЕТ («жду Кубик в Кубе»), и в списке
 * найденного этого по определению не окажется.
 */
window.nfVoicesDialog = function(key) {
  var sh = null;
  var shows = (nfData && nfData.shows) || [];
  for (var i = 0; i < shows.length; i++) {
    var k = (shows[i].kind === 'movie' ? 'movie' : 'tv') + ':' + shows[i].tmdb_id;
    if (k === key) { sh = shows[i]; break; }
  }
  if (!sh) return;
  var want = sh.want_voices || [];
  var known = sh.voices || [];

  var h = '<h3>' + tr('nfVoicesTitle') + '</h3>';
  h += '<div class="form-group"><label class="nfv-any"><input type="radio" name="vmode" value="all" '
    + (want.length ? '' : 'checked') + ' onchange="nfVoicesMode(false)"> ' + tr('nfVoicesAll') + '</label></div>';

  if (known.length) {
    // Источники пишут одну студию по-разному: «HDRezka Studio», «HDrezka Studio.
    // 18+», «RezkaStudio»… Показывать все варианты — это и есть та каша, на которую
    // жаловались. Схлопываем по нормализованному имени (как на сервере), оставляя
    // самое читаемое написание, и показываем, сколько вариантов за ним стоит.
    var uniq = [], byNorm = {};
    for (var j = 0; j < known.length; j++) {
      var raw = String(known[j]).replace(/[\s.,;:]+$/, '').trim();
      if (!raw) continue;
      var norm = raw.toLowerCase().replace(/[^a-zа-яё0-9]+/gi, '');
      if (!norm) continue;
      if (byNorm[norm] === undefined) { byNorm[norm] = uniq.length; uniq.push({ name: raw, all: [known[j]], n: 1 }); continue; }
      var slot = uniq[byNorm[norm]];
      slot.n++;
      slot.all.push(known[j]);
      // из вариантов оставляем самый информативный, но без хвостовой пунктуации
      if (raw.length > slot.name.length) slot.name = raw;
    }
      h += '<div class="form-group"><label>' + tr('nfVoicesKnown')
      + ' <i class="nfv-count">' + uniq.length + '</i></label><div id="nf-known" class="nfv-list">';
    for (var u = 0; u < uniq.length; u++) {
      var it = uniq[u], checked = false;
      for (var q = 0; q < want.length && !checked; q++) {
        for (var a = 0; a < it.all.length; a++) {
          if (String(it.all[a]).toLowerCase() === String(want[q]).toLowerCase()) { checked = true; break; }
        }
      }
      h += '<label class="nfv-item' + (checked ? ' on' : '') + '">'
        + '<input type="checkbox" class="nf-vbox" value="' + esc(it.name) + '" ' + (checked ? 'checked' : '')
        + ' onchange="this.parentNode.classList.toggle(\'on\',this.checked)">'
        + '<span>' + esc(it.name) + (it.n > 1 ? '<i class="nfv-count">· ' + it.n + ' написания</i>' : '') + '</span></label>';
    }
    h += '</div></div>';
  } else {
    h += '<div class="form-group"><div class="sp-help">' + tr('nfVoicesNone') + '</div></div>';
  }

  // В поле — только то, чего нет среди известных: иначе одна и та же озвучка
  // оказалась бы и в чипе, и в тексте, и сохранилась бы дважды.
  var extra = [];
  for (var w = 0; w < want.length; w++) {
    var inKnown = false;
    for (var kk = 0; kk < known.length; kk++) {
      if (String(known[kk]).toLowerCase() === String(want[w]).toLowerCase()) { inKnown = true; break; }
    }
    if (!inKnown) extra.push(want[w]);
  }
  h += '<div class="form-group"><label>' + tr('nfVoicesOwn') + '</label>'
    + '<input type="text" id="nf-vextra" value="' + esc(extra.join(', ')) + '" placeholder="Кубик в Кубе, LostFilm"></div>';
  h += '<div class="sp-help">' + tr('nfVoicesHintPick') + '</div>';
  h += '<button class="btn btn-primary" style="width:100%;margin-top:12px" onclick="nfVoicesSave(' + jsArg(key) + ')">'
    + tr('nfVoicesSave') + '</button>';
  showModal(h);
};

// Радиокнопка «любую новую» снимает все конкретные выборы — иначе на экране
// остаётся противоречие: выбран режим «любая», а чипы отмечены.
window.nfVoicesMode = function(specific) {
  if (specific) return;
  document.querySelectorAll('.nf-vbox').forEach(function(b) { b.checked = false; });
  var extra = document.getElementById('nf-vextra');
  if (extra) extra.value = '';
};

window.nfVoicesSave = function(key) {
  var names = [];
  document.querySelectorAll('.nf-vbox').forEach(function(b) { if (b.checked) names.push(b.value); });
  var extra = (document.getElementById('nf-vextra') || {}).value || '';
  extra.split(',').forEach(function(x) { if (x.trim()) names.push(x.trim()); });

  // Пустой выбор = «любая новая», но отслеживание при этом должно остаться
  // включённым: человек открыл диалог, значит озвучки ему нужны.
  api('POST', '/calendar/voices', {key: key, enabled: true, names: names}).then(function(r) {
    if (r && r.error) { toast(String(r.error)); return; }
    closeModal();
    toast(tr('nfDone'));
    nfData = null;
    loadNotify();
  });
};

window.nfSetVoices = function(key, on) {
  api('POST', '/calendar/voices', {key: key, enabled: on}).then(function(r) {
    if (r && r.error) { toast(String(r.error)); nfData = null; loadNotify(); return; }
    toast(tr('nfDone'));
  });
};

window.nfUnsub = function(key) {
  api('POST', '/calendar/unsubscribe', {key: key}).then(function(r) {
    if (r && r.error) { toast(String(r.error)); return; }
    nfData = null;
    loadNotify();
  });
};

window.nfReadAll = function() {
  api('POST', '/calendar/notifications/read', {}).then(function() {
    nfUnread = 0;
    renderNotify();
    toast(tr('nfDone'));
  });
};

window.nfClear = function() {
  api('POST', '/calendar/notifications/clear', {}).then(function() {
    nfUnread = 0;
    renderNotify();
    toast(tr('nfDone'));
  });
};

// ---- Tab: IPTV ----
// Управление плейлистами живёт здесь, а не в web-клиенте: вводить длинную m3u-ссылку
// пультом на телевизоре — мучение, а в Telegram она вставляется из буфера в два тапа.

var iptvData = null;
var tsPref = ''; // аккаунтный «Свой TorrServer» ('' = сервер приложения)

function loadTsPref() {
  api('GET', '/torrserver').then(function(d) {
    tsPref = (d && d.url) || '';
    renderHome();
    if (iptvData) renderIptv(null); // список ещё грузится → его done-рендер возьмёт свежий tsPref
  }).catch(function() {});
}

window.tsPrefSave = function() {
  var u = ((document.getElementById('ts-url') || {}).value || '').trim();
  api('POST', '/torrserver', {url: u}).then(function(d) {
    if (d && d.error) { toast(String(d.error)); return; }
    tsPref = (d && d.url) || '';
    toast(tr('tsSaved'));
    if (iptvData) renderIptv(null);
  });
};

window.tsPrefClear = function() {
  api('POST', '/torrserver', {url: ''}).then(function(d) {
    if (d && d.error) { toast(String(d.error)); return; }
    tsPref = '';
    toast(tr('tsCleared'));
    if (iptvData) renderIptv(null);
  });
};

function loadIptv() {
  api('GET', '/iptv/playlists').then(function(data) {
    // Ошибку не прячем: пустой список и «не смогли загрузить» — разные вещи, и
    // молча показать «плейлистов нет» значило бы соврать.
    iptvData = data && data.playlists ? data.playlists : [];
    renderIptv(data && data.error ? data.error : null);
    renderHome();
  }).catch(function() {
    iptvData = [];
    renderIptv('network');
  });
}

// ---- Tab: Сервисы ----
// «Свой TorrServer» и привязка YouTube жили во вкладке IPTV, хотя к плейлистам
// отношения не имеют. Свой раздел: и найти проще, и вкладка IPTV снова про IPTV.
function loadServices() {
  loadTsPref();
  renderServices();
}

// Экран «TorrServer» — только свой сервер раздач. YouTube и Shorts уехали на отдельный экран
// (жалоба 2026-09-11: «ютуб и шортсы почему-то в торсервер-вкладке»): с торрентами у них общего
// ничего, кроме того, что оба когда-то свалили в один раздел «Сервисы».
function renderServices() {
  var el = document.getElementById('tab-services');
  if (!el) return;
  var h = '';
  // Адрес общий на аккаунт и применяется на устройствах, где не введён свой локально.
  // Правится отсюда потому же, почему и плейлисты: с телефона вводить удобнее, чем с пульта.
  h += '<p class="sp-help" style="margin-top:0">' + tr('tsHint') + '</p>';
  h += '<div class="form-group"><label>' + tr('tsUrl') + '</label>'
    + '<input type="text" id="ts-url" placeholder="http://192.168.1.50:8090" value="' + esc(tsPref || '') + '"></div>';
  h += '<div style="display:flex;gap:8px;margin-top:2px">'
    + '<button class="btn btn-primary" style="flex:1" onclick="tsPrefSave()">' + tr('tsSave') + '</button>'
    + '<button class="btn" onclick="tsPrefClear()">' + tr('tsClear') + '</button></div>';
  el.innerHTML = h;
}
function loadYouTube() {
  renderYouTube();
  if (!ytState) ytLoad();
  if (ytShortsMode === null) ytShortsLoad();
}
// Экран «YouTube» — привязка аккаунта и режим Shorts. Настройка аккаунтная, применяется НА СЕРВЕРЕ,
// поэтому её слушаются все клиенты (Lampa, веб, Android, tvOS) без обновления самих клиентов.
function renderYouTube() {
  var el = document.getElementById('tab-youtube');
  if (!el) return;
  var h = '';
  // «YouTube-аккаунт»: та же привязка, что и /youtube_auth в боте, но кнопкой:
  // команду набирать не надо, код не надо искать в переписке.
  h += '<h2>' + tr('ytTitle') + '</h2><div class="section" id="yt-box">';
  h += '<p class="sp-help" style="margin-top:0">' + tr('ytHint') + '</p>';
  h += '<div id="yt-body"></div>';
  h += '</div>';
  h += '<h2>' + tr('ytShorts') + '</h2><div class="section">';
  h += '<p class="sp-help" style="margin-top:0">' + tr('ytShortsHint') + '</p>';
  h += '<div id="yt-shorts-body"></div>';
  h += '</div>';
  el.innerHTML = h;
  ytRender();
  ytShortsRender();
}

function renderIptv(err) {
  var el = document.getElementById('tab-iptv');
  if (!el) return;
  var h = '<h2>' + tr('iptvTitle') + '</h2>';
  // Ошибку показываем отдельно: пустой список и «не смогли загрузить» — разные вещи,
  // и молча нарисовать «плейлистов нет» значило бы соврать.
  if (err) h += '<div class="section"><div class="profile-empty">' + esc(String(err)) + '</div></div>';

  h += '<div class="section">';
  if (!iptvData.length) {
    h += '<div class="profile-empty">' + tr('iptvEmpty') + '</div>';
  } else {
    for (var i = 0; i < iptvData.length; i++) {
      var p = iptvData[i];
      var meta = (p.channel_count || 0) + ' ' + tr('iptvChannels');
      if (p.proxy_mode === 'all') meta += ' \u00b7 ' + tr('iptvProxy');
      h += '<div class="sp-row">';
      h += '<div class="sp-icon">\uD83D\uDCFA</div>';
      h += '<div class="sp-info"><div class="sp-name"><span class="sp-nm">' + esc(p.name || 'Playlist') + '</span>';
      if (p.is_global) h += '<span class="sp-pin-tag">' + tr('iptvGlobal') + '</span>';
      h += '</div><div class="sp-meta">' + esc(meta) + '</div></div>';
      h += '<div class="sp-actions">';
      h += '<button class="ico-btn" title="' + tr('iptvRefresh') + '" onclick="iptvRefresh(' + jsArg(p.id) + ')">\u21BB</button>';
      // Общие плейлисты добавил админ — удалять их пользователю нечем и незачем.
      if (!p.is_global) {
        h += '<button class="nf-x" title="' + tr('iptvDelete') + '" onclick="iptvDelete(' + jsArg(p.id) + ')">\u2715</button>';
      }
      h += '</div></div>';
    }
  }
  h += '</div>';

  h += '<h2>' + tr('iptvAdd') + '</h2><div class="section">';
  h += '<div class="form-group"><label>' + tr('iptvUrl') + '</label>'
    + '<input type="text" id="iptv-url" placeholder="https://\u2026/list.m3u"></div>';
  h += '<div class="form-group"><label>' + tr('iptvName') + '</label><input type="text" id="iptv-name"></div>';
  // Панели фильтруют по UA («не читает плейлист» без плеерного UA), а EPG у половины
  // провайдеров лежит отдельной ссылкой — оба поля необязательные.
  h += '<div class="form-group"><label>' + tr('iptvUa') + '</label>'
    + '<input type="text" id="iptv-ua" placeholder="Mozilla/5.0 …"></div>';
  h += '<div class="form-group"><label>' + tr('iptvEpg') + '</label>'
    + '<input type="text" id="iptv-epg" placeholder="https://…/epg.xml.gz"></div>';
  h += '<div class="form-group"><label><input type="checkbox" id="iptv-proxy"> ' + tr('iptvProxy') + '</label></div>';
  h += '<button class="btn btn-primary" style="width:100%" onclick="iptvAdd()">' + tr('iptvAdd') + '</button>';
  h += '</div>';

  el.innerHTML = h;
}

// Аргумент для инлайнового onclick. esc() кавычку-апостроф НЕ экранирует, а id
// приходит с сервера строкой — одного апострофа хватило бы, чтобы выйти из
// строкового литерала прямо в исполняемый код.
function jsArg(v) {
  return "'" + String(v == null ? '' : v).replace(/\\/g, '\\\\').replace(/'/g, "\\'").replace(/"/g, '&quot;') + "'";
}

var ytShortsMode = null; // 'all' | 'hide' | 'only' — режим показа Shorts (аккаунтный)

function ytShortsRender() {
  var el = document.getElementById('yt-shorts-body');
  if (!el) return;
  var cur = ytShortsMode || 'all';
  var opts = [['all', tr('ytShortsAll')], ['hide', tr('ytShortsHide')], ['only', tr('ytShortsOnly')]];
  // Три кнопки в ряд не влезали в 375 px и переносились на вторую строку разной
  // длины — переключатель из трёх равных долей ведёт себя предсказуемо.
  var h = '<div class="seg">';
  for (var i = 0; i < opts.length; i++) {
    var active = opts[i][0] === cur;
    h += '<button type="button" class="' + (active ? 'on' : '') + '" onclick="ytShortsSet(' + jsArg(opts[i][0]) + ')">'
      + esc(opts[i][1]) + '</button>';
  }
  h += '</div>';
  el.innerHTML = h;
}

function ytShortsLoad() {
  api('GET', '/youtube/shorts').then(function(d) {
    ytShortsMode = (d && d.mode) || 'all';
    ytShortsRender();
  });
}

window.ytShortsSet = function(mode) {
  api('POST', '/youtube/shorts', {mode: mode}).then(function(d) {
    if (d && d.error) { toast(String(d.error)); return; }
    ytShortsMode = (d && d.mode) || 'all';
    ytShortsRender();
    toast(tr('ytShortsSaved'));
  });
};

var ytState = null;   // {available, linked, channel, pending, code, url, error}
var ytPollT = null;   // опрос статуса, пока пользователь подтверждает доступ в Google

function ytStopPoll() { if (ytPollT) { clearInterval(ytPollT); ytPollT = null; } }

function ytRender() {
  var el = document.getElementById('yt-body');
  if (!el) return;
  var st = ytState || {};
  // OAuth не настроен на сервере — прячем всю секцию, а не показываем мёртвую кнопку.
  if (st.available === false) {
    var box = document.getElementById('yt-box');
    if (box && box.previousElementSibling) box.previousElementSibling.style.display = 'none';
    if (box) box.style.display = 'none';
    return;
  }
  if (st.linked) {
    ytStopPoll();
    el.innerHTML = '<p style="margin:0 0 12px"><b>' + tr('ytLinked') + '</b>'
      + (st.channel ? ': ' + esc(st.channel) : '') + '</p>'
      + '<button class="btn" onclick="ytUnlink()">' + tr('ytUnlink') + '</button>';
    return;
  }
  if (st.pending && st.code) {
    el.innerHTML = '<p style="margin:0 0 8px">' + tr('ytStep') + '</p>'
      + '<div style="font-size:26px;font-weight:700;letter-spacing:3px;margin-bottom:12px">' + esc(st.code) + '</div>'
      + '<div style="display:flex;gap:10px;flex-wrap:wrap">'
      + '<button class="btn btn-primary" onclick="ytOpenPage()">' + tr('ytOpen') + '</button>'
      + '<button class="btn" onclick="ytCopyCode()">' + tr('ytCopy') + '</button></div>'
      + '<p class="sp-help">' + (st.error ? esc(st.error) : tr('ytWaiting')) + '</p>';
    return;
  }
  el.innerHTML = '<button class="btn btn-primary" style="width:100%" onclick="ytStart()">' + tr('ytConnect') + '</button>'
    + (st.error ? '<p class="sp-help">' + esc(st.error) + '</p>' : '');
}

function ytLoad() {
  api('GET', '/youtube/status').then(function(d) {
    var wasPending = ytState && ytState.pending;
    ytState = d || {};
    ytRender();
    if (ytState.linked) { ytStopPoll(); if (wasPending) toast(tr('ytDone')); }
    else if (ytState.pending) ytStartPoll();
  });
}

function ytStartPoll() {
  if (ytPollT) return;
  // Пользователь уходит в браузер и возвращается — опрос раз в 3 с довольно быстро ловит результат.
  ytPollT = setInterval(ytLoad, 3000);
}

window.ytStart = function() {
  api('POST', '/youtube/start').then(function(d) {
    if (d && d.error) { toast(String(d.error)); return; }
    if (d && d.linked) { ytLoad(); return; }
    ytState = {available: true, linked: false, pending: true, code: d && d.code, url: d && d.url};
    ytRender();
    ytStartPoll();
  });
};

window.ytOpenPage = function() {
  var u = (ytState && ytState.url) || 'https://www.google.com/device';
  if (tg && tg.openLink) tg.openLink(u); else window.open(u, '_blank');
};

window.ytCopyCode = function() {
  var code = (ytState && ytState.code) || '';
  if (!code) return;
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(code).then(function(){ toast(tr('ytCopied')); }, function(){});
    return;
  }
  // Старые WebView без Clipboard API — иначе кнопка молча ничего не делает.
  var ta = document.createElement('textarea');
  ta.value = code; document.body.appendChild(ta); ta.select();
  try { document.execCommand('copy'); toast(tr('ytCopied')); } catch (e) {}
  document.body.removeChild(ta);
};

window.ytUnlink = function() {
  api('POST', '/youtube/unlink').then(function(d) {
    if (d && d.error) { toast(String(d.error)); return; }
    ytStopPoll();
    ytState = {available: true, linked: false};
    ytRender();
    toast(tr('ytUnlinked'));
  });
};

window.iptvAdd = function() {
  var url = (document.getElementById('iptv-url') || {}).value || '';
  if (!url.trim()) { toast(tr('iptvNeedUrl')); return; }
  var name = (document.getElementById('iptv-name') || {}).value || '';
  var ua = (document.getElementById('iptv-ua') || {}).value || '';
  var epg = (document.getElementById('iptv-epg') || {}).value || '';
  var proxy = (document.getElementById('iptv-proxy') || {}).checked;
  api('POST', '/iptv/playlists', {url: url.trim(), name: name.trim(), proxy_mode: proxy ? 'all' : 'none',
    user_agent: ua.trim(), epg_url: epg.trim()})
    .then(function(r) {
      if (r && r.error) { toast(String(r.error)); return; }
      toast(tr('iptvSaved'));
      iptvData = null;
      loadIptv();
    });
};

window.iptvDelete = function(id) {
  api('DELETE', '/iptv/playlists/' + encodeURIComponent(id)).then(function(r) {
    if (r && r.error) { toast(String(r.error)); return; }
    toast(tr('iptvRemoved'));
    iptvData = null;
    loadIptv();
  });
};

window.iptvRefresh = function(id) {
  toast(tr('iptvRefreshing'));
  // Обновление на сервере асинхронное, поэтому список перечитываем не сразу:
  // мгновенный запрос вернул бы прежние счётчики и выглядел бы как «не сработало».
  api('POST', '/iptv/playlists/' + encodeURIComponent(id) + '/refresh').then(function(r) {
    if (r && r.error) { toast(String(r.error)); return; }
    setTimeout(function() { iptvData = null; loadIptv(); }, 2500);
  });
};

function loadProfile() {
  api('GET', '/profile').then(function(data) {
    if (data.error) return;
    profileData = data;
    renderProfile();
    renderHome();
  });
}

function renderProfile() {
  var p = profileData;
  if (!p) return;
  var el = document.getElementById('tab-profile');
  if (!el) return;
  var initials = '?';
  var name = p.name || '';
  if (name.length > 0) { initials = name[0].toUpperCase(); if (name[0] === '@' && name.length > 1) initials = name[1].toUpperCase(); }
  var subBadgeCls = 'ok';
  var subText = p.days_left + ' ' + pluralDays(p.days_left);
  if (p.expired) { subBadgeCls = 'expired'; subText = '\u0418\u0441\u0442\u0451\u043a'; }
  else if (p.days_left <= 7) subBadgeCls = 'danger';
  else if (p.days_left <= 30) subBadgeCls = 'warn';
  var h = '<div class="profile-section">';
  // Профили ALPAC — первыми: ради них сюда и приходят (содержимое досыпает apLoad).
  h += '<div class="profile-card" id="ap-card"><div class="profile-card-title">\uD83D\uDC64 ' + tr('apSection') + ' <span class="ap-count" id="ap-count"></span></div>'
    + '<div class="ap-grid" id="ap-grid"><div class="profile-empty">' + tr('apLoading') + '</div></div>'
    + '<div class="sp-help">' + tr('apHelp') + '</div></div>';
  h += '<div class="profile-hero"><div class="profile-avatar">' + esc(initials) + '</div><div class="profile-info"><div class="profile-name">' + esc(name || 'User') + '</div>' + (p.telegram_id ? '<div class="profile-tgid">TG: ' + p.telegram_id + '</div>' : '') + '</div></div>';
  h += '<div class="profile-card"><div class="profile-card-title">\u23F3 ' + tr('subscription') + '</div><div class="profile-sub-row"><div><div class="profile-sub-date">' + (p.expired ? tr('expired') : tr('until') + ' ' + esc(p.expires_at)) + '</div></div><div class="profile-sub-badge ' + subBadgeCls + '">' + subText + '</div></div></div>';
  if (p.group_name) { h += '<div class="profile-card"><div class="profile-card-title">\uD83D\uDC65 ' + tr('group') + '</div><div class="profile-group-badge">' + esc(p.group_name) + '</div></div>'; }
  h += '<div class="profile-card"><div class="profile-card-title">\uD83D\uDCF1 ' + tr('devices') + (p.max_devices > 0 ? ' <span style="opacity:.5">' + p.device_count + '/' + p.max_devices + '</span>' : '') + '</div>';
  if (p.devices && p.devices.length > 0) { p.devices.forEach(function(d, di) { var uid = d.uid || ''; var masked = uid.length > 12 ? uid.substring(0,6) + '\u2026' + uid.substring(uid.length-4) : uid; h += '<div class="profile-device"><div class="profile-device-icon">\uD83D\uDCF1</div><div class="profile-device-info"><div class="profile-device-label">' + esc(d.label || masked) + '</div><div class="profile-device-meta">UID: ' + esc(masked) + ' \u00B7 ' + esc(d.last_seen) + '</div><div class="ap-devchip" id="apdev-' + di + '" style="display:none"></div></div></div>'; }); }
  else { h += '<div class="profile-empty">' + tr('noDevices') + '</div>'; }
  h += '</div>';

  // CUB — запасной ключ входа (kit_cub.go). Содержимое досыпает cubLoad: статус живёт в токене.
  h += '<div class="profile-card" id="cub-card"><div class="profile-card-title">\u2601\uFE0F CUB</div>'
    + '<div id="cub-body"><div class="profile-empty">' + tr('spLoading') + '</div></div></div>';

  // Sync profiles section \u2014 only meaningful for TG-authenticated callers.
  // The TG WebApp is always TG-auth (Telegram opens it with initData), so
  // unlike bkit we don't gate on a separate tgAuth flag here.
  h += '<div class="profile-card" id="sync-profiles-card">'
    + '<div class="profile-card-title">\uD83D\uDD11 ' + tr('spSection') + '</div>'
    + '<div id="sync-profiles-list"><div class="profile-empty">' + tr('spLoading') + '</div></div>'
    + '<button class="sp-add" onclick="syncProfileCreate()">' + tr('spAdd') + '</button>'
    + '<div class="sp-help">' + tr('spHelp') + '</div>'
    + '</div>';

  h += '</div>';
  el.innerHTML = h;
  apLoad();
  syncProfileLoad();
  cubLoad();
}

// ---- Профили ALPAC (kit_app_profiles.go) ----
// Те же профили, что в приложении на телевизоре: у каждого своя история, закладки, настройки.
// Пультом их неудобно переименовывать и нельзя поставить своё фото — здесь можно всё. Не путать
// с «Профилями синхронизации Лампы» ниже (имя + PIN для входа в Лампу).

var AP_COLORS = ['linear-gradient(135deg,#7C5CFF,#FF5CA8)', 'linear-gradient(135deg,#21D4FD,#2152FF)',
  'linear-gradient(135deg,#F6C453,#FF8A3D)', 'linear-gradient(135deg,#0BAB64,#3BB78F)',
  'linear-gradient(135deg,#FF5C5C,#A8327D)'];
var apData = null;  // ответ /app-profiles: {profiles, max, avatars, device_profiles} или {error}
var apEd = null;    // редактируемый профиль (id '' = новый)
var apBusy = false;

function apGrad(c) {
  var i = parseInt(c, 10) || 0;
  return AP_COLORS[((i % AP_COLORS.length) + AP_COLORS.length) % AP_COLORS.length];
}
// Картинка: ссылка на фото (своё или CUB) или ещё не отправленное фото нового профиля.
function apIsImg(a) { return /^(https?:\/\/|data:image\/)/i.test(a || ''); }
function apFirst(name) {
  var n = String(name || '').replace(/^@/, '');
  var ch = Array.from ? Array.from(n)[0] : n.charAt(0);
  return (ch || '?').toUpperCase();
}
// Аватар: фото, эмодзи или первая буква имени на градиенте цвета профиля.
function apAvaHTML(p, cls) {
  var a = p.avatar || '';
  var inner;
  if (apIsImg(a)) inner = '<img src="' + esc(a) + '" alt="">';
  else if (a) inner = '<span class="ap-emo">' + esc(a) + '</span>';
  else inner = '<span class="ap-ltr">' + esc(apFirst(p.name)) + '</span>';
  return '<span class="' + cls + '" style="background:' + apGrad(p.color) + '">' + inner + '</span>';
}
function apMax() { return (apData && apData.max) || 5; }
function apErrText(code) {
  var map = {max_profiles: tr('apMax', {n: apMax()}), last_profile: tr('apLast'), bad_pin: tr('apBadPin'),
    bad_name: tr('apBadName'), no_account: tr('apNoAccount'), too_many: tr('apPhotoTooMany')};
  return map[code] || tr('apErr', {e: code || 'network'});
}

function apLoad() {
  api('GET', '/app-profiles').then(function(d) {
    apData = (d && !d.error) ? d : {error: (d && d.error) || 'network'};
    apRender();
  }).catch(function() { apData = {error: 'network'}; apRender(); });
}
function apApply(d) { apData = d; apRender(); }

function apRender() {
  var grid = document.getElementById('ap-grid');
  var cnt = document.getElementById('ap-count');
  if (!grid) return;
  if (!apData) { grid.innerHTML = '<div class="profile-empty">' + tr('apLoading') + '</div>'; return; }
  if (apData.error) {
    grid.innerHTML = '<div class="profile-empty">' + (apData.error === 'no_account' ? tr('apNoAccount') : tr('spNetErr')) + '</div>';
    if (cnt) cnt.textContent = '';
    return;
  }
  var list = apData.profiles || [];
  var h = '';
  list.forEach(function(p, i) {
    var tags = (p.kids ? '<i>' + tr('apKidsTag') + '</i>' : '') + (p.hasPin ? '<i>' + tr('apPinTag') + '</i>' : '');
    h += '<button class="ap-tile" onclick="apOpen(' + i + ')">' + apAvaHTML(p, 'ap-ava')
      + '<span class="ap-nm">' + esc(p.name) + '</span><span class="ap-tags">' + tags + '</span></button>';
  });
  if (list.length < apMax()) {
    h += '<button class="ap-tile" onclick="apOpen(-1)"><span class="ap-ava ap-ava-add">+</span>'
      + '<span class="ap-nm">' + tr('apNew') + '</span><span class="ap-tags"></span></button>';
  }
  grid.innerHTML = h;
  if (cnt) cnt.textContent = list.length + '/' + apMax();
  apRenderDevices();
}

// Под каждым устройством — профиль, который на нём сейчас открыт.
function apRenderDevices() {
  if (!apData || apData.error || !profileData || !profileData.devices) return;
  var byId = {};
  (apData.profiles || []).forEach(function(p) { byId[p.id] = p; });
  profileData.devices.forEach(function(d, i) {
    var el = document.getElementById('apdev-' + i);
    if (!el) return;
    var p = byId[(apData.device_profiles || {})[d.uid]];
    el.innerHTML = p ? apAvaHTML(p, 'ap-mini') + '<span>' + esc(p.name) + '</span>' : '';
    el.style.display = p ? '' : 'none';
  });
}

window.apOpen = function(i) {
  if (!apData || apData.error) return;
  var list = apData.profiles || [];
  if (i < 0 && list.length >= apMax()) { toast(tr('apMax', {n: apMax()})); return; }
  var p = i >= 0 ? list[i] : null;
  apEd = p
    ? {id: p.id, name: p.name || '', color: String(p.color || '0'), avatar: p.avatar || '', kids: !!p.kids,
       hasPin: !!p.hasPin, pin: '', pinClear: false, touched: false, pending: ''}
    : {id: '', name: '', color: String(list.length % AP_COLORS.length), avatar: '', kids: false,
       hasPin: false, pin: '', pinClear: false, touched: true, pending: ''};
  apRenderEditor();
  haptic('light');
};

function apPreview() { return {name: apEd.name, color: apEd.color, avatar: apEd.pending || apEd.avatar}; }
function apHasPhoto() { return apIsImg(apEd.pending || apEd.avatar); }

function apPhotoBtnsHTML() {
  return '<label class="btn btn-sm ap-photo-pick">📷 ' + (apHasPhoto() ? tr('apPhotoChange') : tr('apPhoto'))
    + '<input type="file" accept="image/*" onchange="apPickPhoto(this)"></label>'
    + (apHasPhoto() ? '<button class="btn btn-sm" onclick="apDropPhoto()">' + tr('apPhotoRemove') + '</button>' : '');
}
function apPinHTML() {
  var e = apEd;
  var h = '<div class="ap-pin"><input id="ap-pin" type="password" inputmode="numeric" pattern="[0-9]*" maxlength="4" autocomplete="off" placeholder="••••" value="' + esc(e.pin) + '" oninput="apSetPin(this)"' + (e.pinClear ? ' disabled' : '') + '>';
  if (e.hasPin) h += '<button class="btn btn-sm' + (e.pinClear ? ' on' : '') + '" onclick="apTogglePinClear()">' + (e.pinClear ? tr('apPinWillRemove') : tr('apPinRemove')) + '</button>';
  return h + '</div><div class="ap-sub ap-pin-sub">' + (e.hasPin ? tr('apPinSet') : tr('apPinSub')) + '</div>';
}

function apRenderEditor() {
  var e = apEd;
  var isNew = !e.id;
  var h = '<h3>' + (isNew ? tr('apNewTitle') : tr('apEditTitle')) + '</h3>';
  h += '<div class="ap-hero"><div id="ap-big-wrap">' + apAvaHTML(apPreview(), 'ap-big') + '</div>'
    + '<div class="ap-photo-btns" id="ap-photo-btns">' + apPhotoBtnsHTML() + '</div></div>';
  h += '<div class="form-group"><label for="ap-name">' + tr('apName') + '</label><input id="ap-name" maxlength="20" autocomplete="off" placeholder="' + esc(tr('apNamePh')) + '" value="' + esc(e.name) + '" oninput="apSetName(this.value)"></div>';
  h += '<div class="ap-label">' + tr('apColor') + '</div><div class="ap-colors">';
  for (var c = 0; c < AP_COLORS.length; c++) {
    h += '<button class="ap-sw' + (String(c) === e.color ? ' on' : '') + '" style="background:' + AP_COLORS[c] + '" onclick="apSetColor(' + c + ')" aria-label="' + esc(tr('apColor')) + ' ' + (c + 1) + '"></button>';
  }
  h += '</div><div class="ap-label">' + tr('apAvatar') + '</div><div class="ap-emojis">';
  h += '<button class="ap-em ap-em-l" onclick="apSetEmoji(-1)">Aa</button>';
  ((apData && apData.avatars) || []).forEach(function(a, k) {
    h += '<button class="ap-em" onclick="apSetEmoji(' + k + ')">' + esc(a) + '</button>';
  });
  h += '</div>';
  h += '<div class="toggle-row ap-toggle"><div><div class="toggle-label">' + tr('apKids') + '</div><div class="ap-sub">' + tr('apKidsSub') + '</div></div>'
    + '<label class="switch"><input type="checkbox"' + (e.kids ? ' checked' : '') + ' onchange="apSetKids(this.checked)"><span class="slider"></span></label></div>';
  h += '<div class="ap-label">' + tr('apPin') + '</div><div id="ap-pin-wrap">' + apPinHTML() + '</div>';
  h += '<button class="btn btn-primary ap-save" id="ap-save" onclick="apSave()">' + (isNew ? tr('apCreate') : tr('apSave')) + '</button>';
  if (!isNew && (apData.profiles || []).length > 1) h += '<button class="btn ap-del" onclick="apDelete()">' + tr('apDelete') + '</button>';
  showModal(h);
  apSyncEmoji();
}

// Точечные обновления: перерисовка всего листа сбрасывала бы прокрутку и фокус поля.
function apRefreshPreview() {
  var w = document.getElementById('ap-big-wrap');
  if (w) w.innerHTML = apAvaHTML(apPreview(), 'ap-big');
  var b = document.getElementById('ap-photo-btns');
  if (b) b.innerHTML = apPhotoBtnsHTML();
}
function apSyncEmoji() {
  var ems = (apData && apData.avatars) || [];
  var sel = apHasPhoto() ? -2 : (apEd.avatar ? ems.indexOf(apEd.avatar) : -1);
  var bs = document.querySelectorAll('.ap-em');
  for (var i = 0; i < bs.length; i++) bs[i].classList.toggle('on', i === sel + 1);
}
window.apSetName = function(v) { apEd.name = v; if (!apHasPhoto() && !apEd.avatar) apRefreshPreview(); };
window.apSetColor = function(c) {
  apEd.color = String(c);
  var sws = document.querySelectorAll('.ap-sw');
  for (var i = 0; i < sws.length; i++) sws[i].classList.toggle('on', i === c);
  apRefreshPreview();
  haptic('light');
};
window.apSetEmoji = function(k) {
  var ems = (apData && apData.avatars) || [];
  apEd.avatar = k < 0 ? '' : (ems[k] || '');
  apEd.pending = '';
  apEd.touched = true;
  apSyncEmoji();
  apRefreshPreview();
  haptic('light');
};
window.apSetKids = function(v) { apEd.kids = !!v; haptic('light'); };
window.apDropPhoto = function() { apEd.avatar = ''; apEd.pending = ''; apEd.touched = true; apSyncEmoji(); apRefreshPreview(); };
window.apSetPin = function(el) {
  var v = String(el.value || '').replace(/\D/g, '').slice(0, 4);
  if (el.value !== v) el.value = v;
  apEd.pin = v;
};
window.apTogglePinClear = function() {
  apEd.pinClear = !apEd.pinClear;
  apEd.pin = '';
  var w = document.getElementById('ap-pin-wrap');
  if (w) w.innerHTML = apPinHTML();
};

// Фото ужимается на телефоне: центральный квадрат до 512 px в JPEG — на сервер уходит ~50–150 КБ,
// а не 5 МБ оригинала. Сервер всё равно перекодирует его в 256×256 и выкинет метаданные.
function apShrinkPhoto(file, cb) {
  var U = window.URL || window.webkitURL;
  var url = U.createObjectURL(file);
  var img = new Image();
  img.onload = function() {
    var out = '';
    try {
      var side = Math.min(img.naturalWidth, img.naturalHeight);
      var sx = (img.naturalWidth - side) / 2, sy = (img.naturalHeight - side) / 2;
      var sizes = [512, 384];
      for (var t = 0; t < sizes.length && (!out || out.length > 700000); t++) {
        var n = Math.min(sizes[t], side);
        var cv = document.createElement('canvas');
        cv.width = n; cv.height = n;
        var ctx = cv.getContext('2d');
        ctx.fillStyle = '#222738';
        ctx.fillRect(0, 0, n, n);
        ctx.drawImage(img, sx, sy, side, side, 0, 0, n, n);
        out = cv.toDataURL('image/jpeg', t ? 0.8 : 0.88);
      }
    } catch (e) { out = ''; }
    U.revokeObjectURL(url);
    cb(out);
  };
  img.onerror = function() { U.revokeObjectURL(url); cb(''); };
  img.src = url;
}

function apUploadPhoto(id, dataURL, done) {
  apBusy = true;
  toast(tr('apPhotoBusy'));
  api('POST', '/app-profiles/photo', {id: id, image: dataURL}).then(function(d) {
    apBusy = false;
    if (!d || d.error) { toast(d && d.error === 'too_many' ? tr('apPhotoTooMany') : tr('apPhotoErr')); if (done) done(false); return; }
    apApply(d);
    if (apEd && apEd.id === id) {
      (d.profiles || []).forEach(function(p) { if (p.id === id) apEd.avatar = p.avatar || ''; });
      apEd.pending = '';
      apEd.touched = false;
      apSyncEmoji();
      apRefreshPreview();
    }
    toast(tr('apPhotoSaved'));
    haptic('success');
    if (done) done(true);
  }).catch(function() { apBusy = false; toast(tr('apPhotoErr')); if (done) done(false); });
}

window.apPickPhoto = function(input) {
  var f = input.files && input.files[0];
  if (!f || apBusy) return;
  apShrinkPhoto(f, function(dataURL) {
    input.value = '';
    if (!dataURL) { toast(tr('apPhotoErr')); return; }
    if (!apEd.id) { // новый профиль: фото уйдёт сразу после создания
      apEd.pending = dataURL;
      apEd.touched = true;
      apSyncEmoji();
      apRefreshPreview();
      return;
    }
    apUploadPhoto(apEd.id, dataURL);
  });
};

window.apSave = function() {
  if (apBusy || !apEd) return;
  var e = apEd;
  var name = String(e.name || '').trim();
  if (name.length > 20) { toast(tr('apBadName')); return; }
  if (e.pin && !/^\d{4}$/.test(e.pin)) { toast(tr('apBadPin')); return; }
  var body = {name: name, color: e.color, kids: !!e.kids};
  // своё фото уже на сервере (или уйдёт после создания) — аватар шлём, только если его сменили
  if (e.touched && !e.pending) body.avatar = apIsImg(e.avatar) ? undefined : (e.avatar || '');
  if (e.pinClear) body.pin = ''; else if (e.pin) body.pin = e.pin;
  if (e.id) body.id = e.id;
  var btn = document.getElementById('ap-save');
  if (btn) btn.disabled = true;
  apBusy = true;
  api('POST', e.id ? '/app-profiles/update' : '/app-profiles/create', body).then(function(d) {
    apBusy = false;
    if (!d || d.error) { if (btn) btn.disabled = false; toast(apErrText(d && d.error)); haptic('error'); return; }
    apApply(d);
    haptic('success');
    if (!e.id && d.created && e.pending) {
      closeModal();
      toast(tr('apCreated'));
      apUploadPhoto(d.created, e.pending);
      return;
    }
    closeModal();
    toast(e.id ? tr('apSaved') : tr('apCreated'));
  }).catch(function() { apBusy = false; if (btn) btn.disabled = false; toast(tr('spNetErr')); });
};

window.apDelete = function() {
  if (!apEd || !apEd.id || apBusy) return;
  if (!confirm(tr('apConfirmDelete', {name: apEd.name}))) return;
  apBusy = true;
  api('POST', '/app-profiles/delete', {id: apEd.id}).then(function(d) {
    apBusy = false;
    if (!d || d.error) { toast(apErrText(d && d.error)); return; }
    apApply(d);
    closeModal();
    toast(tr('apDeleted'));
    haptic('success');
  }).catch(function() { apBusy = false; toast(tr('spNetErr')); });
};

// ---- CUB: запасной ключ входа (привязка по коду с cub.red/add) ----

var cubState = null;

function cubLoad() {
  api('GET', '/cub').then(function(d) {
    cubState = d;
    cubRender();
  }).catch(function() {
    var el = document.getElementById('cub-body');
    if (el) el.innerHTML = '<div class="profile-empty">' + tr('spNetErr') + '</div>';
  });
}

function cubRender() {
  var card = document.getElementById('cub-card');
  var el = document.getElementById('cub-body');
  if (!el || !card || !cubState) return;
  // Браузерная сессия (/bkit) — не аккаунт Telegram: привязывать CUB не к чему.
  if (cubState.error === 'tg_required' || cubState.error === 'unavailable') { card.style.display = 'none'; return; }
  var h = '';
  if (cubState.error === 'no_account') {
    h = '<div class="profile-empty">' + tr('cubErrNoAccount') + '</div>';
  } else if (cubState.linked) {
    h = '<div class="toggle-row"><div><div class="toggle-label">' + tr('cubLinked') + '</div>'
      + (cubState.email ? '<div class="toggle-sub">' + esc(cubState.email) + '</div>' : '') + '</div>'
      + '<button class="btn btn-danger btn-sm" onclick="cubUnbind()">' + tr('cubUnbind') + '</button></div>'
      + '<div class="sp-help">' + tr('cubHelp') + '</div>';
  } else {
    h = '<div class="sp-help" style="margin-top:0">' + tr('cubHelp') + '</div>'
      + '<button class="sp-add" onclick="cubBind()">' + tr('cubBind') + '</button>';
  }
  el.innerHTML = h;
}

window.cubOpenAdd = function() {
  var u = 'https://cub.red/add';
  if (tg && tg.openLink) tg.openLink(u); else window.open(u, '_blank');
};

window.cubBind = function() {
  var link = '<a href="#" onclick="cubOpenAdd();return false">cub.red/add</a>';
  var html = '<h3>' + tr('cubCodeTitle') + '</h3>';
  html += '<div class="device-steps">' + tr('cubCodeHelp', {url: link}) + '</div>';
  html += '<form onsubmit="cubSubmit(event)">';
  html += '<div class="form-group"><input type="text" id="cub-code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="000000" style="font-size:22px;letter-spacing:.3em;text-align:center" required></div>';
  html += '<div id="cub-err" class="device-steps" style="color:var(--danger,#e5484d);display:none"></div>';
  html += '<button type="submit" class="btn btn-primary" style="width:100%" id="cub-submit">' + tr('bind') + '</button>';
  html += '</form>';
  html += '<div class="sp-help">' + tr('cubCodeNote') + '</div>';
  showModal(html);
  var inp = document.getElementById('cub-code');
  if (inp) inp.focus();
};

window.cubSubmit = function(e) {
  e.preventDefault();
  var code = (document.getElementById('cub-code').value || '').replace(/\D/g, '');
  var btn = document.getElementById('cub-submit');
  var err = document.getElementById('cub-err');
  var fail = function(key) {
    if (err) { err.textContent = tr(key); err.style.display = ''; }
    if (btn) btn.textContent = tr('bind');
  };
  if (code.length !== 6) { fail('cubErrBadCode'); return; }
  if (btn) btn.innerHTML = '<span class="spinner"></span>';
  api('POST', '/cub/link', {code: code}).then(function(r) {
    if (r.success) {
      closeModal();
      toast(tr('cubLinkedToast') + (r.email ? ': ' + r.email : ''));
      cubLoad();
      return;
    }
    fail({bad_code: 'cubErrBadCode', rate: 'cubErrRate', no_account: 'cubErrNoAccount'}[r.error] || 'cubErrUnreachable');
  }).catch(function() { fail('cubErrUnreachable'); });
};

window.cubUnbind = function() {
  api('POST', '/cub/unlink').then(function(r) {
    if (r.success) toast(tr('cubUnlinkedToast'));
    cubLoad();
  }).catch(function() { toast(tr('connectionError')); });
};

// ---- Sync profiles CRUD (kit-auth via X-Telegram-Init-Data) ----

var syncProfiles = [];

function formatSyncTimeAgo(ms) {
  if (!ms) return '';
  var diff = (Date.now() - ms) / 1000;
  if (diff < 60) return tr('spJustNow');
  if (diff < 3600) return tr('spMinAgo', {n: Math.floor(diff/60)});
  if (diff < 86400) return tr('spHourAgo', {n: Math.floor(diff/3600)});
  return tr('spDayAgo', {n: Math.floor(diff/86400)});
}

function syncProfileLoad() {
  api('GET', '/profile-owned').then(function(d) {
    if (!d || d.error) {
      var el = document.getElementById('sync-profiles-list');
      if (el) el.innerHTML = '<div class="profile-empty">' + tr('spEmpty') + '</div>';
      return;
    }
    syncProfiles = d.profiles || [];
    syncProfileRender();
  }).catch(function() {
    var el = document.getElementById('sync-profiles-list');
    if (el) el.innerHTML = '<div class="profile-empty">' + tr('spNetErr') + '</div>';
  });
}

function syncProfileRender() {
  var el = document.getElementById('sync-profiles-list');
  if (!el) return;
  if (!syncProfiles.length) {
    el.innerHTML = '<div class="profile-empty">' + tr('spEmpty') + '</div>';
    return;
  }
  var html = '';
  syncProfiles.forEach(function(sp) {
    var initial = (sp.username || '?').charAt(0).toUpperCase();
    var pinTag = sp.has_pin
      ? '<span class="sp-pin-tag">' + tr('spPinOn') + '</span>'
      : '<span class="sp-pin-tag off">' + tr('spPinOff') + '</span>';
    var meta = sp.last_login_at
      ? tr('spLastLogin', {when: formatSyncTimeAgo(sp.last_login_at)})
      : tr('spNever');
    // sp.id and sp.username are server-supplied; both go into onclick args
    // as string literals \u2014 escape any single quotes via the raw replace
    // because we can't rely on esc() (which is for innerHTML, not JS).
    var sid = String(sp.id || '').split("'").join("\\'");
    var snm = String(sp.username || '').split("'").join("\\'");
    html += '<div class="sp-row">'
      + '<div class="sp-icon">' + esc(initial) + '</div>'
      + '<div class="sp-info"><div class="sp-name"><span class="sp-nm">' + esc(sp.username) + '</span>' + pinTag + '</div>'
      + '<div class="sp-meta">' + esc(meta) + '</div></div>'
      + '<div class="sp-actions">'
      + '<button class="btn btn-sm" onclick="syncProfileSetPIN(\'' + sid + '\',\'' + snm + '\')">PIN</button>'
      + '<button class="nf-x" onclick="syncProfileDelete(\'' + sid + '\',\'' + snm + '\')">\u2715</button>'
      + '</div></div>';
  });
  el.innerHTML = html;
}

window.syncProfileCreate = function() {
  var name = prompt(tr('spPromptName'));
  if (!name) return;
  var pin = prompt(tr('spPromptPin'));
  if (pin === null) return;
  api('POST', '/profile-owned/create', { username: name.trim(), pin: pin.trim() }).then(function(d) {
    if (d && d.error) { toast(tr('spErr', {e: d.error})); return; }
    toast(tr('spCreated'));
    syncProfileLoad();
  }).catch(function() { toast(tr('spNetErr')); });
};

window.syncProfileSetPIN = function(id, name) {
  var pin = prompt(tr('spPromptNewPin', {name: name}));
  if (pin === null) return;
  api('POST', '/profile-owned/set-pin', { profile_id: id, pin: pin.trim() }).then(function(d) {
    if (d && d.error) { toast(tr('spErr', {e: d.error})); return; }
    toast(tr('spPinUpdated'));
    syncProfileLoad();
  }).catch(function() { toast(tr('spNetErr')); });
};

window.syncProfileDelete = function(id, name) {
  if (!confirm(tr('spConfirmDelete', {name: name}))) return;
  api('POST', '/profile-owned/delete', { profile_id: id }).then(function(d) {
    if (d && d.error) { toast(tr('spErr', {e: d.error})); return; }
    toast(tr('spDeleted'));
    syncProfileLoad();
  }).catch(function() { toast(tr('spNetErr')); });
};

function pluralDays(n) {
  if (lang === 'en') return n === 1 ? 'day' : 'days';
  var uk = lang === 'uk';
  if (n % 10 === 1 && n % 100 !== 11) return '\u0434\u0435\u043d\u044c';
  if (n % 10 >= 2 && n % 10 <= 4 && (n % 100 < 10 || n % 100 >= 20)) return uk ? '\u0434\u043d\u0456' : '\u0434\u043d\u044f';
  return uk ? '\u0434\u043d\u0456\u0432' : '\u0434\u043d\u0435\u0439';
}

// ---- Load config ----

function load() {
  if (!initData) {
    document.getElementById('loading').innerHTML = tr('initDataEmpty', {tg: tg ? tr('tgYes') : tr('tgNo')});
    return;
  }
  api('GET', '/config').then(function(data) {
    if (data.error) {
      document.getElementById('loading').textContent = tr('error') + data.error;
      return;
    }
    config = data || {};

    // Switch language from API if server-side placeholder was empty
    if (config._lang && (config._lang === 'uk' || config._lang === 'en' || config._lang === 'ru')) {
      if (config._lang !== lang) {
        lang = config._lang;
        L = i18n[lang] || i18n.ru;
      }
    }
    delete config._lang;

    // Filter hidden services from Kit UI.
    var hidden = config['_kitHiddenServices'] || [];
    if (hidden.length > 0) {
      bindServices = bindServices.filter(function(s) { return hidden.indexOf(s.id) === -1; });
      tokenBalancers = tokenBalancers.filter(function(s) { return hidden.indexOf(s.key.toLowerCase()) === -1; });
    }
    delete config['_kitHiddenServices'];

    // Admin kill-switch for the binds tab. When set, default to the Балансеры tab.
    bindsDisabled = !!config['_bindsDisabled'];
    delete config['_bindsDisabled'];
    if (bindsDisabled && activeTab === 'binds') activeTab = 'balancers';

    // Parse balancer visibility from config
    balancerVisibility = {};
    if (config['_balancerVisibility']) {
      try {
        balancerVisibility = typeof config['_balancerVisibility'] === 'string'
          ? JSON.parse(config['_balancerVisibility'])
          : config['_balancerVisibility'];
      } catch(e) {}
    }
    render();
    preloadHome();
  }).catch(function(e) {
    document.getElementById('loading').textContent = tr('loadError') + e.message;
  });
}

// ---- Главный экран и экраны второго уровня ----
// Мини-апп обслуживает ДВА продукта: плагин Lampa (балансеры и привязки) и
// приложение ALPAC (профиль, подписки). Шесть равноправных вкладок в одну строку
// этого не показывали. Теперь на главной три группы: «Источники» — про плагин,
// «Общее» — TorrServer и IPTV (одинаково работают и там, и там), «Приложение» —
// аккаунт и уведомления.

// Иконки рисуем SVG, а не символами и не эмодзи. Символы вроде ⬢ и ▤ есть не в
// каждом системном шрифте — на стенде шестиугольник выпал в кружок-заглушку, а
// эмодзи на iOS и Android выглядят по-разному, и ровного набора из них не выйдет.
function svgIcon(d) {
  return '<svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor"'
       + ' stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' + d + '</svg>';
}

var HOME_ICONS = {
  // ползунки — «какие источники искать»
  balancers: ['<path d="M3 7h4M13 7h8M3 13h11M20 13h1M3 19h6M15 19h6"/>'
              + '<circle cx="10" cy="7" r="2.2"/><circle cx="17" cy="13" r="2.2"/><circle cx="12" cy="19" r="2.2"/>',
              'rgba(124,92,255,.16)', '#8b6cff'],
  // ключ — «привязки по токену»
  binds:     ['<circle cx="8.5" cy="8.5" r="4.2"/><path d="M11.6 11.6 20 20M16.8 17.2l2-2M14.2 14.6l2-2"/>',
              'rgba(51,144,236,.16)', '#3390ec'],
  // стрелка в лоток — «раздачи»
  services:  ['<path d="M12 3v11M8 10.4l4 4 4-4M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
              'rgba(52,199,89,.16)', '#34c759'],
  // играть — «YouTube: аккаунт и Shorts»
  youtube:   ['<rect x="2.5" y="5" width="19" height="14" rx="4"/>'
              + '<path d="M10.5 9.2v5.6l4.8-2.8z" fill="currentColor" stroke="none"/>',
              'rgba(255,59,48,.16)', '#ff4d4d'],
  // экран с треугольником — «телеканалы»
  iptv:      ['<rect x="2.6" y="4.4" width="18.8" height="13" rx="2.6"/><path d="M8.5 21h7"/>'
              + '<path d="M10.6 8.9v4.6l4-2.3z" fill="currentColor" stroke="none"/>',
              'rgba(255,159,10,.16)', '#ff9f0a'],
  // колокольчик — «подписки на выход»
  notify:    ['<path d="M12 3.2a5.8 5.8 0 0 0-5.8 5.8c0 4-1.4 5.4-2 6h15.6c-.6-.6-2-2-2-6A5.8 5.8 0 0 0 12 3.2z"/>'
              + '<path d="M9.9 18.6a2.1 2.1 0 0 0 4.2 0"/>',
              'rgba(255,92,168,.16)', '#ff5ca8'],
  // человек — «профиль и устройства»
  profile:   ['<circle cx="12" cy="8" r="3.6"/><path d="M4.6 20c1.2-3.6 4-5.4 7.4-5.4s6.2 1.8 7.4 5.4"/>',
              'rgba(51,144,236,.16)', '#3390ec']
};

function homeRow(screen, title, sub, value, mark) {
  var ic = HOME_ICONS[screen];
  var h = '<div class="row" onclick="switchTab(' + jsArg(screen) + ')">';
  h += '<div class="ico" style="background:' + ic[1] + ';color:' + ic[2] + '">' + svgIcon(ic[0]) + '</div>';
  h += '<div class="row-t"><b>' + title + '</b><span>' + sub + '</span></div>';
  if (mark) h += '<span class="dotmark"></span>';
  h += '<span class="row-v">' + (value == null ? '' : value) + '</span>';
  h += '<i class="chev"></i></div>';
  return h;
}

function homeGroup(title, note, rows) {
  if (!rows) return '';
  return '<div class="gr"><div class="gr-head"><h2>' + title + '</h2><i>' + note + '</i></div>'
       + '<div class="list">' + rows + '</div></div>';
}

// Значения справа в строках. Пока соответствующий запрос не вернулся — прочерк:
// показать «0 плейлистов» до ответа значило бы соврать.
function homeValBinds() {
  var n = 0;
  bindServices.forEach(function(sv) {
    var sec = config[sectionKey(sv.id)] || {};
    if (sec.enable && (sec.token || sec.cookie)) n++;
  });
  return n ? tr('boundN', {n: n, t: bindServices.length}) : tr('dashValue');
}

function homeValBalancers() {
  if (!balancerData) return tr('dashValue');
  var total = 0, on = 0;
  balancerData.balancers.forEach(function(b) {
    if (!b.globalEnabled && !b.userBound) return; // выключен глобально — его вообще не видно
    if (b.allowed === false) return;              // закрыт группой — переключать нечего
    total++;
    if (getVisibility(b.key, true)) on++;
  });
  return total ? tr('boundN', {n: on, t: total}) : tr('dashValue');
}

// Адрес TorrServer в строке главной: схема и хвост съедали всю ширину и адрес
// обрезался ровно на цифрах, ради которых его и показывают.
function shortHost(u) {
  var v = String(u || '').replace(/^https?:\/\//, '').replace(/\/+$/, '');
  return v.length > 22 ? v.slice(0, 21) + '\u2026' : v;
}

function heroHTML() {
  var p = profileData;
  var name = '';
  if (tg && tg.initDataUnsafe && tg.initDataUnsafe.user) {
    var u = tg.initDataUnsafe.user;
    name = u.username ? '@' + u.username
                      : ((u.first_name || '') + ' ' + (u.last_name || '')).trim();
  }
  if (p && p.name) name = p.name;
  if (!name) name = 'ALPAC';
  var ini = (name.charAt(0) === '@' && name.length > 1) ? name.charAt(1) : name.charAt(0);

  var sub = tr('heroSignedIn');
  var planV = tr('dashValue'), leftV = tr('dashValue'), leftCls = '', devV = tr('dashValue');
  if (p) {
    planV = p.group_name ? esc(p.group_name) : tr('heroNoPlan');
    if (p.expired) { leftV = tr('expired'); leftCls = ' danger'; }
    else {
      leftV = p.days_left + ' ' + pluralDays(p.days_left);
      if (p.days_left <= 7) leftCls = ' danger';
      else if (p.days_left <= 30) leftCls = ' warn';
    }
    devV = String(p.device_count == null ? 0 : p.device_count);
    if (p.max_devices > 0) devV += '/' + p.max_devices;
    if (p.telegram_id) sub = 'ID ' + esc(String(p.telegram_id));
  }

  var h = '<div class="hero' + (p ? '' : ' hero-skel') + '">';
  h += '<div class="hero-row"><div class="hero-ava">' + esc(String(ini).toUpperCase()) + '</div>';
  h += '<div class="hero-who"><b>' + esc(name) + '</b><span>' + sub + '</span></div></div>';
  h += '<div class="hero-stats">';
  h += '<div><b>' + planV + '</b><span>' + tr('heroPlan') + '</span></div>';
  h += '<div><b class="' + leftCls.replace(/^ /, '') + '">' + leftV + '</b><span>' + tr('heroLeft') + '</span></div>';
  h += '<div><b>' + devV + '</b><span>' + tr('heroDevices') + '</span></div>';
  h += '</div></div>';
  return h;
}

function homeHTML() {
  var h = '<div class="topbar"><div class="brand"><i></i>ALPAC <em>kit</em></div></div>';
  h += heroHTML();

  // Источники — это настройки ПЛАГИНА Lampa.
  var rows = '';
  rows += homeRow('balancers', tr('tabBalancers'), tr('rowBalancersSub'), homeValBalancers());
  if (!bindsDisabled) rows += homeRow('binds', tr('tabBinds'), tr('rowBindsSub'), homeValBinds());
  h += homeGroup(tr('grSources'), tr('grSourcesSub'), rows);

  // Общее. Адрес TorrServer и плейлисты одинаково слушают и плагин, и приложение —
  // держать их в «источниках Lampa» было бы неправдой.
  rows = '';
  rows += homeRow('services', tr('rowTsTitle'), tr('rowTsSub'), tsPref ? esc(shortHost(tsPref)) : tr('tsDefault'));
  rows += homeRow('iptv', tr('tabIptv'), tr('rowIptvSub'), iptvData ? String(iptvData.length) : tr('dashValue'));
  h += homeGroup(tr('grShared'), tr('grSharedSub'), rows);

  // Приложение ALPAC.
  rows = '';
  rows += homeRow('youtube', tr('rowYtTitle'), tr('rowYtSub'), ytState && ytState.linked ? tr('ytLinked') : '');
  rows += homeRow('notify', tr('tabNotify'), tr('rowNotifySub'),
                  nfData ? String((nfData.shows || []).length) : tr('dashValue'), nfUnread > 0);
  rows += homeRow('profile', tr('rowProfileTitle'), tr('rowProfileSub'),
                  profileData ? String(profileData.device_count || 0) : tr('dashValue'));
  h += homeGroup(tr('grApp'), tr('grAppSub'), rows);
  return h;
}

// Перерисовать только главную. Зовётся из всех загрузчиков: цифры на главной
// дорисовываются по мере ответов, а не держат пользователя на спиннере.
function renderHome() {
  var el = document.getElementById('scr-home');
  if (el) el.innerHTML = homeHTML();
}

function screenHTML(id, title, sub, body) {
  return '<div class="scr" id="scr-' + id + '"' + (activeTab === id ? '' : ' style="display:none"') + '>'
    + '<div class="scr-head"><h1>' + title + '</h1>' + (sub ? '<p>' + sub + '</p>' : '') + '</div>'
    + body + '</div>';
}

// Экран «Привязки» строится прямо тут: он читает config, который уже загружен.
function bindsBodyHTML() {
  var h = '<div id="tab-binds">';
  h += '<h2>' + tr('bindServices') + '</h2><div class="section">';
  bindServices.forEach(function(sv) {
    var sec = config[sectionKey(sv.id)] || {};
    var bound = sec.enable && (sec.token || sec.cookie);
    h += '<div class="card">';
    h += '<div class="card-info"><div class="card-name">' + sv.name + '</div>';
    h += '<div class="card-status' + (bound ? ' bound' : '') + '">' + (bound ? tr('bound') : tr('notBound')) + '</div></div>';
    h += '<span class="badge ' + (sv.badge === 'Free' ? 'badge-free' : 'badge-premium') + '">' + sv.badge + '</span>';
    if (bound) {
      h += ' <button class="btn btn-danger btn-sm" onclick="unbind(' + jsArg(sv.id) + ')">✕</button>';
    } else {
      h += ' <button class="btn btn-primary btn-sm" onclick="startBind(' + jsArg(sv.id) + ')">' + tr('bind') + '</button>';
    }
    h += '</div>';
  });
  h += '</div>';

  h += '<h2>' + tr('sources') + '</h2><div class="section">';
  tokenBalancers.forEach(function(b) {
    var sec = config[b.key] || {};
    var enabled = !!sec.enable;
    var token = sec.token || sec.cookie || '';
    h += '<div class="toggle-row">';
    h += '<div><div class="toggle-label">' + b.name + '</div>';
    if (token) h += '<div class="toggle-sub">' + token.substring(0, 16) + (token.length > 16 ? '…' : '') + '</div>';
    h += '</div>';
    h += '<label class="switch"><input type="checkbox" ' + (enabled ? 'checked' : '') + ' onchange="toggleTokenBalancer(' + jsArg(b.key) + ',this.checked)"><span class="slider"></span></label>';
    h += '</div>';
    h += '<div class="token-row"><input class="token-input" placeholder="' + tr('tokenPlaceholder') + '" value="' + escHtml(token) + '" onchange="setToken(' + jsArg(b.key) + ',this.value)"></div>';
  });
  h += '</div></div>';
  return h;
}

function render() {
  var h = '';
  h += '<div id="scr-home"' + (activeTab === 'home' ? '' : ' style="display:none"') + '>' + homeHTML() + '</div>';
  if (!bindsDisabled) h += screenHTML('binds', tr('tabBinds'), tr('rowBindsSub'), bindsBodyHTML());
  h += screenHTML('balancers', tr('tabBalancers'), tr('rowBalancersSub'),
                  '<div id="tab-balancers"><div class="loading">' + tr('loadingBalancers') + '</div></div>');
  h += screenHTML('services', tr('rowTsTitle'), tr('rowTsSub'),
                  '<div id="tab-services"><div class="loading">' + tr('loading') + '</div></div>');
  h += screenHTML('youtube', tr('rowYtTitle'), tr('rowYtSub'),
                  '<div id="tab-youtube"><div class="loading">' + tr('loading') + '</div></div>');
  h += screenHTML('iptv', tr('tabIptv'), tr('rowIptvSub'),
                  '<div id="tab-iptv"><div class="loading">' + tr('loading') + '</div></div>');
  h += screenHTML('notify', tr('tabNotify'), tr('rowNotifySub'),
                  '<div id="tab-notify"><div class="loading">' + tr('loading') + '</div></div>');
  h += screenHTML('profile', tr('rowProfileTitle'), tr('rowProfileSub'),
                  '<div id="tab-profile"><div class="loading">' + tr('loading') + '</div></div>');

  document.getElementById('app').innerHTML = h;
  setupMainButton();
  if (tg && tg.BackButton) {
    if (activeTab === 'home') tg.BackButton.hide(); else tg.BackButton.show();
  }

  if (balancerData) renderBalancers();
  else if (activeTab === 'balancers') loadBalancers();
}

// Цифры на главной приезжают параллельно. Открывать ради них спиннер незачем:
// из шести разделов пользователь заходит в один, а видеть счётчики хочет сразу.
function preloadHome() {
  loadProfile();
  loadBalancers();
  loadTsPref();
  loadIptv();
  loadNotify();
}

function sectionKey(serviceId) {
  var map = {filmix:'Filmix',kinopub:'KinoPub',rezka:'Rezka',rezkaPrem:'RezkaPrem',vokino:'VoKino',getstv:'GetsTV',iptvonline:'IptvOnline',pornlab:'PornLab'};
  return map[serviceId] || serviceId;
}

function escHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

// ---- Token balancer toggle (binds tab) ----

window.toggleTokenBalancer = function(key, on) {
  if (!config[key]) config[key] = {};
  config[key].enable = on;
  hasChanges = true;
  setupMainButton();
};

window.setToken = function(key, val) {
  if (!config[key]) config[key] = {};
  val = val.trim();
  if (val) { config[key].token = val; config[key].enable = true; }
  else { delete config[key].token; }
  hasChanges = true;
  setupMainButton();
};

// ---- Save ----

function save() {
  if (tg) tg.MainButton.showProgress();
  // Sync visibility into config before saving
  syncVisibilityToConfig();
  api('POST', '/config', config).then(function(r) {
    if (r.success) {
      toast(tr('saved'));
      haptic('success');
      hasChanges = false;
      setupMainButton();
      renderHome();
    } else {
      haptic('error');
      toast(tr('error') + (r.error || 'unknown'));
    }
  }).catch(function() {
    toast(tr('saveError'));
  }).finally(function() {
    if (tg) tg.MainButton.hideProgress();
  });
}

var mainButtonBound = false;

function setupMainButton() {
  if (!tg) return;
  // tg.MainButton.onClick ДОБАВЛЯЕТ обработчик, а не заменяет: раньше эту строку
  // выполняли на каждое переключение галки, и один тап по «Сохранить» отправлял
  // столько же запросов, сколько галок успел тронуть пользователь.
  if (!mainButtonBound) { tg.MainButton.onClick(save); mainButtonBound = true; }
  if (hasChanges) {
    tg.MainButton.setText(tr('save'));
    tg.MainButton.show();
  } else {
    tg.MainButton.hide();
  }
}

// ---- Balancers tab ----

function loadBalancers() {
  api('GET', '/balancers').then(function(data) {
    if (data.error) {
      document.getElementById('tab-balancers').innerHTML = '<div class="loading">' + tr('error') + escHtml(data.error) + '</div>';
      return;
    }
    balancerData = data;
    renderBalancers();
    renderHome();
  }).catch(function(e) {
    document.getElementById('tab-balancers').innerHTML = '<div class="loading">' + tr('loadError') + '</div>';
  });
}

function getVisibility(key, effectiveEnabled) {
  if (balancerVisibility.hasOwnProperty(key)) return balancerVisibility[key];
  return effectiveEnabled;
}

function syncVisibilityToConfig() {
  // Only write if there are any visibility overrides
  var keys = Object.keys(balancerVisibility);
  if (keys.length > 0) {
    config['_balancerVisibility'] = balancerVisibility;
  } else {
    delete config['_balancerVisibility'];
  }
}

// Экран балансеров.
//
// Показываем только то, чем пользователь МОЖЕТ пользоваться:
//  * выключённые глобально прячем совсем — включать их бессмысленно, а строка с
//    подписью «выкл. глобально» просто удлиняла список;
//  * закрытые ГРУППОЙ (allowed=false) остаются видны с меткой Premium и
//    заблокированным тумблером: поиск (lite_events) их всё равно отфильтрует, и
//    молча включённая галка была враньём — источник просто молчал;
//  * закрытые вообще всем (не открыты даже премиум-группе) прячем — про них
//    нечего сказать пользователю.
function renderBalancers() {
  var el = document.getElementById('tab-balancers');
  if (!el || !balancerData) return;

  function visibleIn(groupKey) {
    return balancerData.balancers.filter(function(b) {
      if (b.group !== groupKey) return false;
      if (!b.globalEnabled && !b.userBound) return false;      // выключён глобально
      if (b.allowed === false && !b.premiumOnly) return false; // закрыт всем группам
      return true;
    });
  }

  var h = '';
  var shown = 0;
  balancerData.groups.forEach(function(group) {
    var bals = visibleIn(group.key);
    if (bals.length === 0) return;
    shown += bals.length;

    // «Всё включено» считаем только по тем, что пользователю вообще доступны:
    // иначе групповой тумблер никогда бы не вставал в положение «включено».
    var switchable = bals.filter(function(b) { return b.allowed !== false; });
    var enabledCount = 0;
    switchable.forEach(function(b) { if (getVisibility(b.key, true)) enabledCount++; });
    var allOn = switchable.length > 0 && enabledCount === switchable.length;

    h += '<div class="group-header">';
    h += '<div class="group-title"><span>' + group.icon + '</span> ' + escHtml(group.label);
    h += '<span class="group-count">' + enabledCount + '/' + switchable.length + '</span></div>';
    if (switchable.length > 0) {
      h += '<label class="switch"><input type="checkbox" ' + (allOn ? 'checked' : '')
         + ' onchange="toggleGroup(' + jsArg(group.key) + ',this.checked)"><span class="slider"></span></label>';
    }
    h += '</div>';

    h += '<div class="bal-group">';
    bals.forEach(function(b) {
      var locked = b.allowed === false;
      var vis = !locked && getVisibility(b.key, true);
      h += '<div class="bal-row' + (locked ? ' locked' : '') + '">';
      h += '<div><span class="bal-name">' + escHtml(b.name) + '</span>';
      if (b.quality) h += '<span class="bal-quality">' + b.quality + '</span>';
      if (locked) h += '<span class="bal-lock">' + tr('balPremium') + '</span>';
      else if (b.userBound && !b.globalEnabled) h += '<span class="bal-note" style="color:var(--success)">' + tr('userBound') + '</span>';
      h += '</div>';
      if (locked) {
        h += '<span class="badge badge-premium">Premium</span>';
      } else {
        h += '<label class="switch"><input type="checkbox" ' + (vis ? 'checked' : '')
           + ' onchange="toggleBalancerVis(' + jsArg(b.key) + ',this.checked)"><span class="slider"></span></label>';
      }
      h += '</div>';
    });
    h += '</div>';
  });

  if (!shown) h = '<div class="loading">' + tr('balEmpty') + '</div>';
  el.innerHTML = h;
}

window.toggleBalancerVis = function(key, on) {
  balancerVisibility[key] = on;
  hasChanges = true;
  haptic('light');
  setupMainButton();
  renderBalancers();
  renderHome();
};

window.toggleGroup = function(groupKey, on) {
  if (!balancerData) return;
  balancerData.balancers.forEach(function(b) {
    if (b.group === groupKey) {
      balancerVisibility[b.key] = on;
    }
  });
  hasChanges = true;
  haptic('medium');
  setupMainButton();
  renderBalancers();
  renderHome();
};

// ---- Unbind ----

window.unbind = function(serviceId) {
  if (!confirm(tr('confirmUnbind'))) return;
  api('DELETE', '/bind/' + serviceId).then(function(r) {
    if (r.success) {
      delete config[sectionKey(serviceId)];
      toast(tr('unbound'));
      render();
    }
  });
};

// ---- Bind flows ----

window.startBind = function(serviceId) {
  var svc = bindServices.find(function(s){ return s.id === serviceId; });
  if (!svc) return;
  if (svc.type === 'device') startDeviceBind(svc);
  else if (svc.type === 'login') startLoginBind(svc);
  else if (svc.type === 'apikey') startApiKeyBind(svc);
};

function startDeviceBind(svc) {
  showModal('<h3>' + tr('bindTitle', {name: svc.name}) + '</h3><div class="loading">' + tr('requestingCode') + '</div>');
  api('POST', '/bind/' + svc.id + '/start').then(function(r) {
    if (r.error) { showModal('<h3>' + tr('errorTitle') + '</h3><p>' + r.error + '</p>'); return; }
    var url = svc.id === 'filmix' ? 'https://filmix.my/consoles' : 'https://kino.watch/device';
    var html = '<h3>' + tr('bindTitle', {name: svc.name}) + '</h3>';
    html += '<div class="device-steps">' + tr('step1', {url: url}) + '</div>';
    html += '<div class="device-steps">' + tr('step2') + '</div>';
    html += '<div class="device-code">' + r.user_code + '</div>';
    html += '<div class="device-steps">' + tr('step3') + '</div>';
    html += '<button class="btn btn-primary" style="width:100%;margin-top:12px" id="finish-btn" onclick="finishDeviceBind(\'' + svc.id + '\',\'' + escHtml(r.code) + '\')">' + tr('finishBind') + '</button>';
    showModal(html);
  }).catch(function() {
    showModal('<h3>' + tr('errorTitle') + '</h3><p>' + tr('serviceUnavailable') + '</p>');
  });
}

window.finishDeviceBind = function(serviceId, code) {
  var btn = document.getElementById('finish-btn');
  if (btn) btn.innerHTML = '<span class="spinner"></span>';
  api('POST', '/bind/' + serviceId + '/finish', {code: code}).then(function(r) {
    if (r.success) {
      closeModal();
      load();
      toast(tr('boundSuccess', {name: serviceId.charAt(0).toUpperCase() + serviceId.slice(1)}));
    } else {
      showModal('<h3>' + tr('errorTitle') + '</h3><p>' + (r.error || tr('bindFailed')) + '</p><p>' + tr('bindFailedHint') + '</p>');
    }
  });
};

function startLoginBind(svc) {
  var html = '<h3>' + tr('bindTitle', {name: svc.name}) + '</h3>';
  html += '<form onsubmit="submitLoginBind(event,\'' + svc.id + '\')">';
  html += '<div class="form-group"><label>' + tr('emailLogin') + '</label><input type="text" id="bind-login" required></div>';
  html += '<div class="form-group"><label>' + tr('password') + '</label><input type="password" id="bind-pass" required></div>';
  html += '<button type="submit" class="btn btn-primary" style="width:100%" id="bind-submit-btn">' + tr('bind') + '</button>';
  html += '</form>';
  showModal(html);
}

window.submitLoginBind = function(e, serviceId) {
  e.preventDefault();
  var login = document.getElementById('bind-login').value;
  var pass = document.getElementById('bind-pass').value;
  var btn = document.getElementById('bind-submit-btn');
  if (btn) btn.innerHTML = '<span class="spinner"></span>';
  api('POST', '/bind/' + serviceId, {login: login, password: pass}).then(function(r) {
    if (r.success) {
      closeModal();
      load();
      toast(tr('boundSuccess', {name: serviceId}));
    } else {
      toast(tr('error') + (r.error || 'unknown'));
      if (btn) btn.textContent = tr('bind');
    }
  }).catch(function() {
    toast(tr('connectionError'));
    if (btn) btn.textContent = tr('bind');
  });
};

function startApiKeyBind(svc) {
  var html = '<h3>' + tr('bindTitle', {name: svc.name}) + '</h3>';
  html += '<div class="device-steps">' + tr('dataFrom') + ' <a href="https://iptv.online/ru/dealers/api" target="_blank">iptv.online/ru/dealers/api</a></div>';
  html += '<form onsubmit="submitApiKeyBind(event)">';
  html += '<div class="form-group"><label>X-API-KEY</label><input type="text" id="bind-apikey" required></div>';
  html += '<div class="form-group"><label>X-API-ID</label><input type="text" id="bind-apiid" required></div>';
  html += '<button type="submit" class="btn btn-primary" style="width:100%">' + tr('save') + '</button>';
  html += '</form>';
  showModal(html);
}

window.submitApiKeyBind = function(e) {
  e.preventDefault();
  var key = document.getElementById('bind-apikey').value;
  var id = document.getElementById('bind-apiid').value;
  api('POST', '/bind/iptvonline', {api_key: key, api_id: id}).then(function(r) {
    if (r.success) {
      closeModal();
      load();
      toast(tr('boundSuccess', {name: 'iptv.online'}));
    } else {
      toast(tr('error') + (r.error || 'unknown'));
    }
  });
};

// Init
load();

})();
</script>
</body>
</html>`
