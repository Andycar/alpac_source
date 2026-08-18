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
:root {
  --bg: #ffffff;
  --card-bg: #f7f7f7;
  --text: #1a1a1a;
  --text2: #666;
  --border: #e0e0e0;
  --accent: #2481cc;
  --accent-hover: #1c6eb0;
  --success: #4caf50;
  --danger: #e53935;
  --danger-hover: #c62828;
  --radius: 12px;
  --input-bg: #fff;
}
* { box-sizing: border-box; margin: 0; padding: 0; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  background: var(--bg);
  color: var(--text);
  padding: 0 16px 100px;
  line-height: 1.5;
  min-height: 100vh;
}
h2 { font-size: 18px; margin: 24px 0 12px; font-weight: 600; }
.section { margin-bottom: 20px; }
.card {
  background: var(--card-bg);
  border-radius: var(--radius);
  padding: 14px 16px;
  margin-bottom: 8px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.card-info { flex: 1; min-width: 0; }
.card-name { font-weight: 600; font-size: 15px; }
.card-status { font-size: 13px; color: var(--text2); margin-top: 2px; }
.card-status.bound { color: var(--success); }
.badge {
  font-size: 11px; font-weight: 600; padding: 2px 8px; border-radius: 10px;
  white-space: nowrap;
}
.badge-free { background: #e8f5e9; color: #2e7d32; }
.badge-premium { background: #fff3e0; color: #e65100; }
.profile-section{padding:16px}
.profile-hero{display:flex;align-items:center;gap:16px;background:var(--tg-theme-secondary-bg-color,#f5f5f5);border-radius:14px;padding:20px;margin-bottom:12px}
.profile-avatar{width:50px;height:50px;border-radius:50%;flex-shrink:0;background:linear-gradient(135deg,var(--tg-theme-button-color,#3390ec),#7c4dff);display:flex;align-items:center;justify-content:center;font-size:22px;font-weight:700;color:#fff}
.profile-info{flex:1;min-width:0}
.profile-name{font-size:17px;font-weight:700;line-height:1.3}
.profile-tgid{font-size:12px;opacity:.5;margin-top:2px}
.profile-card{background:var(--tg-theme-secondary-bg-color,#f5f5f5);border-radius:14px;padding:14px 18px;margin-bottom:12px}
.profile-card-title{font-size:13px;font-weight:600;opacity:.6;margin-bottom:8px;display:flex;align-items:center;gap:6px}
.profile-sub-row{display:flex;align-items:center;justify-content:space-between;gap:8px}
.profile-sub-date{font-size:15px;font-weight:600}
.profile-sub-badge{padding:3px 12px;border-radius:20px;font-size:12px;font-weight:600}
.profile-sub-badge.ok{background:rgba(52,211,153,.15);color:#34d399}
.profile-sub-badge.warn{background:rgba(251,191,36,.15);color:#fbbf24}
.profile-sub-badge.danger{background:rgba(248,113,113,.15);color:#f87171}
.profile-sub-badge.expired{background:rgba(248,113,113,.15);color:#f87171}
.profile-group-badge{display:inline-block;padding:3px 12px;border-radius:20px;font-size:12px;font-weight:600;background:rgba(51,144,236,.15);color:var(--tg-theme-button-color,#3390ec)}
.profile-device{display:flex;align-items:center;gap:12px;padding:10px 0;border-bottom:1px solid rgba(0,0,0,.05)}
.profile-device:last-child{border-bottom:none}
.profile-device-icon{font-size:18px;flex-shrink:0}
.profile-device-info{flex:1;min-width:0}
.profile-device-label{font-size:14px;font-weight:500}
.profile-device-meta{font-size:11px;opacity:.45;margin-top:1px}
.profile-empty{opacity:.45;font-size:13px;padding:8px 0}
/* Sync-profile rows (PIN-managed) inside the TG WebApp profile tab */
.sp-row{display:flex;align-items:center;gap:12px;padding:10px 0;border-bottom:1px solid rgba(0,0,0,.06)}
.sp-row:last-child{border-bottom:none}
.sp-icon{width:34px;height:34px;border-radius:10px;flex-shrink:0;display:flex;align-items:center;justify-content:center;font-size:14px;font-weight:600;background:var(--accent);color:#fff}
.sp-info{flex:1;min-width:0}
.sp-name{font-size:14px;font-weight:500;display:flex;align-items:center;gap:6px}
.sp-meta{font-size:11px;opacity:.5;margin-top:1px}
.sp-actions{display:flex;gap:6px;flex-shrink:0}
.sp-pin-tag{font-size:10px;padding:2px 8px;border-radius:10px;font-weight:600;background:rgba(0,0,0,.06);color:var(--accent)}
.sp-pin-tag.off{opacity:.5;color:inherit}
.sp-add{display:flex;align-items:center;justify-content:center;width:100%;padding:11px;border:1px dashed rgba(0,0,0,.18);background:transparent;border-radius:10px;color:var(--accent);font-weight:500;cursor:pointer;margin-top:8px;transition:background .15s}
.sp-add:active{background:rgba(0,0,0,.04)}
.sp-help{font-size:11px;opacity:.5;margin-top:10px;line-height:1.5}
.btn {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 8px 16px; border: none; border-radius: 8px;
  font-size: 14px; font-weight: 500; cursor: pointer;
  transition: all .15s;
}
.btn-primary { background: var(--accent); color: #fff; }
.btn-primary:hover { background: var(--accent-hover); }
.btn-danger { background: var(--danger); color: #fff; font-size: 12px; padding: 6px 12px; }
.btn-danger:hover { background: var(--danger-hover); }
.btn-sm { padding: 6px 12px; font-size: 13px; }

/* Tabs */
.tabs {
  display: flex; gap: 0; margin: 12px 0 0;
  border-bottom: 2px solid var(--border);
}
.tab {
  flex: 1; padding: 12px 0; text-align: center;
  font-size: 15px; font-weight: 500; cursor: pointer;
  color: var(--text2); border-bottom: 2px solid transparent;
  margin-bottom: -2px; transition: all .2s; user-select: none;
}
.tab.active { color: var(--accent); border-bottom-color: var(--accent); }

/* Toggle */
.toggle-row {
  background: var(--card-bg); border-radius: var(--radius);
  padding: 12px 16px; margin-bottom: 6px;
  display: flex; align-items: center; justify-content: space-between;
}
.toggle-label { font-size: 15px; font-weight: 500; }
.toggle-sub { font-size: 12px; color: var(--text2); }
.switch { position: relative; width: 44px; height: 24px; flex-shrink: 0; }
.switch input { opacity: 0; width: 0; height: 0; }
.switch .slider {
  position: absolute; inset: 0; background: #ccc; border-radius: 24px;
  cursor: pointer; transition: .2s;
}
.switch .slider:before {
  content: ""; position: absolute; width: 18px; height: 18px;
  left: 3px; bottom: 3px; background: #fff; border-radius: 50%;
  transition: .2s;
}
.switch input:checked + .slider { background: var(--accent); }
.switch input:checked + .slider:before { transform: translateX(20px); }
.switch input:disabled + .slider { opacity: .4; cursor: default; }

/* Token input */
.token-row { margin-top: 4px; padding: 0 16px; }
.token-input {
  width: 100%; padding: 8px 12px; border: 1px solid var(--border);
  border-radius: 8px; font-size: 13px; background: var(--input-bg);
  color: var(--text); font-family: monospace;
}
.token-input:focus { border-color: var(--accent); outline: none; }

/* Balancers tab */
.group-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 16px 8px; margin-top: 8px;
}
.group-title { font-weight: 600; font-size: 15px; display: flex; align-items: center; gap: 6px; }
.group-count { font-size: 12px; color: var(--text2); font-weight: 400; margin-left: 6px; }
.bal-row {
  background: var(--card-bg); border-radius: var(--radius);
  padding: 10px 16px; margin-bottom: 4px;
  display: flex; align-items: center; justify-content: space-between;
}
.bal-row.disabled { opacity: .45; }
.bal-name { font-size: 14px; font-weight: 500; }
.bal-quality { font-size: 11px; color: var(--accent); margin-left: 6px; }
.bal-note { font-size: 11px; color: var(--text2); display: block; }

/* Modal */
.modal-overlay {
  display: none; position: fixed; inset: 0; z-index: 100;
  background: rgba(0,0,0,.4); align-items: center; justify-content: center;
  padding: 16px;
}
.modal-overlay.active { display: flex; }
.modal {
  background: var(--bg); border-radius: 16px; padding: 24px;
  width: 100%; max-width: 400px; position: relative;
}
.modal h3 { font-size: 18px; margin-bottom: 16px; }
.modal .close {
  position: absolute; top: 12px; right: 16px; background: none;
  border: none; font-size: 24px; cursor: pointer; color: var(--text2);
}
.form-group { margin-bottom: 14px; }
.form-group label { display: block; font-size: 13px; color: var(--text2); margin-bottom: 4px; }
.form-group input {
  width: 100%; padding: 10px 12px; border: 1px solid var(--border);
  border-radius: 8px; font-size: 15px; background: var(--input-bg); color: var(--text);
}
.form-group input:focus { border-color: var(--accent); outline: none; }
.device-code {
  font-size: 28px; font-weight: 700; text-align: center;
  padding: 16px; background: var(--card-bg); border-radius: 12px;
  letter-spacing: 4px; color: var(--accent); margin: 12px 0;
  font-family: monospace;
}
.device-steps { font-size: 14px; color: var(--text2); margin: 12px 0; }
.device-steps a { color: var(--accent); }
.spinner { display: inline-block; width: 16px; height: 16px; border: 2px solid #fff;
  border-top-color: transparent; border-radius: 50%; animation: spin .6s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.toast {
  position: fixed; bottom: 80px; left: 50%; transform: translateX(-50%);
  background: #333; color: #fff; padding: 10px 20px; border-radius: 8px;
  font-size: 14px; z-index: 200; opacity: 0; transition: opacity .3s;
}
.toast.show { opacity: 1; }
.loading { text-align: center; padding: 60px 0; color: var(--text2); font-size: 15px; }
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
var activeTab = 'binds';
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
    subscription: 'Подписка',
    until: 'до',
    expired: 'Истёк',
    group: 'Группа',
    devices: 'Устройства',
    noDevices: 'Нет привязанных устройств',
    spSection: 'Профили синхронизации',
    spLoading: 'Загрузка…',
    spEmpty: 'Пока нет профилей',
    spAdd: '+ Создать профиль',
    spHelp: 'Профиль — отдельный набор закладок/таймкодов (например «Дети», «Жена»). На ТВ вводите имя + PIN в «Синхронизация → Войти по PIN».',
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
    subscription: 'Підписка',
    until: 'до',
    expired: 'Закінчився',
    group: 'Група',
    devices: 'Пристрої',
    noDevices: "Немає прив'язаних пристроїв",
    spSection: 'Профілі синхронізації',
    spLoading: 'Завантаження…',
    spEmpty: 'Поки немає профілів',
    spAdd: '+ Створити профіль',
    spHelp: "Профіль — окремий набір закладок/таймкодів (наприклад «Діти», «Дружина»). На ТВ вводьте ім'я + PIN у «Синхронізація → Увійти за PIN».",
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
    subscription: 'Subscription',
    until: 'until',
    expired: 'Expired',
    group: 'Group',
    devices: 'Devices',
    noDevices: 'No devices bound',
    spSection: 'Sync profiles',
    spLoading: 'Loading…',
    spEmpty: 'No profiles yet',
    spAdd: '+ Create profile',
    spHelp: 'A profile is a separate set of bookmarks/timecodes (e.g. "Kids", "Wife"). Sign in on the TV with name + PIN under "Sync → Sign in with PIN".',
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

// Theme
if (tg) {
  tg.expand();
  tg.ready();
  var tp = tg.themeParams || {};
  if (tp.bg_color) document.documentElement.style.setProperty('--bg', tp.bg_color);
  if (tp.secondary_bg_color) document.documentElement.style.setProperty('--card-bg', tp.secondary_bg_color);
  if (tp.text_color) document.documentElement.style.setProperty('--text', tp.text_color);
  if (tp.hint_color) document.documentElement.style.setProperty('--text2', tp.hint_color);
  if (tp.button_color) document.documentElement.style.setProperty('--accent', tp.button_color);
  if (tp.link_color) document.documentElement.style.setProperty('--accent', tp.link_color);
  // Set input background to match card background for dark theme compatibility
  if (tp.secondary_bg_color) document.documentElement.style.setProperty('--input-bg', tp.secondary_bg_color);
  tg.BackButton.show();
  tg.BackButton.onClick(function(){
    var home = bindsDisabled ? 'balancers' : 'binds';
    if (activeTab !== home) { switchTab(home); } else { tg.close(); }
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

// ---- Tab switching ----

var profileData = null;
window.switchTab = function(tab) {
  if (tab === 'binds' && bindsDisabled) tab = 'balancers'; // binds tab is gone
  activeTab = tab;
  document.querySelectorAll('.tab').forEach(function(t) {
    t.classList.toggle('active', t.dataset.tab === tab);
  });
  var bindsEl = document.getElementById('tab-binds');
  if (bindsEl) bindsEl.style.display = tab === 'binds' ? '' : 'none';
  document.getElementById('tab-balancers').style.display = tab === 'balancers' ? '' : 'none';
  var iptvEl = document.getElementById('tab-iptv');
  if (iptvEl) iptvEl.style.display = tab === 'iptv' ? '' : 'none';
  var svcEl = document.getElementById('tab-services');
  if (svcEl) svcEl.style.display = tab === 'services' ? '' : 'none';
  var nfEl = document.getElementById('tab-notify');
  if (nfEl) nfEl.style.display = tab === 'notify' ? '' : 'none';
  var profEl = document.getElementById('tab-profile');
  if (profEl) profEl.style.display = tab === 'profile' ? '' : 'none';
  if (tab === 'balancers' && !balancerData) loadBalancers();
  if (tab === 'profile' && !profileData) loadProfile();
  if (tab === 'iptv' && !iptvData) loadIptv();
  if (tab === 'services') loadServices();
  if (tab === 'notify' && !nfData) loadNotify();
};

// ---- Tab: Subscriptions / notifications ----
// Управлять подписками здесь удобнее, чем в приложении: на телевизоре список правится
// пультом, а тут — пальцем, и сразу видно, куда уходят уведомления.

var nfData = null;   // {shows, notify_tg, notify_app}
var nfUnread = 0;
var nfDelivery = {};
var nfQueued = 0;

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
  h += '<div class="sp-row"><div class="sp-icon">\u2708</div><div class="sp-info">'
    + '<div class="sp-name">' + tr('nfTg') + '</div></div>'
    + '<div class="sp-actions"><input type="checkbox" ' + (d.notify_tg ? 'checked' : '')
    + ' onchange="nfSetChannel(\'tg\', this.checked)"></div></div>';
  h += '<div class="sp-row"><div class="sp-icon">\uD83D\uDCF1</div><div class="sp-info">'
    + '<div class="sp-name">' + tr('nfApp') + '</div></div>'
    + '<div class="sp-actions"><input type="checkbox" ' + (d.notify_app ? 'checked' : '')
    + ' onchange="nfSetChannel(\'app\', this.checked)"></div></div>';
  h += '</div>';

  // «Когда» идёт сразу за «куда»: это одна мысль — как именно до вас доносить
  // новости, — и разносить её по разным местам экрана было бы странно.
  var dl = nfDelivery || {};
  var isDigest = dl.mode === 'digest';
  h += '<h2>' + tr('nfWhen') + '</h2><div class="section">';
  h += '<div class="form-group"><label><input type="radio" name="nfmode" value="instant" '
    + (isDigest ? '' : 'checked') + ' onchange="nfModeChanged(false)"> ' + tr('nfInstant') + '</label></div>';
  h += '<div class="form-group"><label><input type="radio" name="nfmode" value="digest" '
    + (isDigest ? 'checked' : '') + ' onchange="nfModeChanged(true)"> ' + tr('nfDigest') + '</label></div>';
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
  h += '<div class="sp-row"><div class="sp-info"><div class="sp-name">' + nfUnread + '</div></div>';
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
      h += '<div class="sp-row">';
      h += '<div class="sp-icon">' + (isMovie ? '\uD83C\uDFAC' : '\uD83D\uDCFA') + '</div>';
      h += '<div class="sp-info"><div class="sp-name">' + esc(sh.title || key) + '</div>';
      h += '<div class="sp-meta">' + (isMovie ? tr('nfMovie') : tr('nfSeries'));
      if (!isMovie && sh.last_season) h += ' \u00b7 S' + sh.last_season + 'E' + (sh.last_episode || 0);
      h += '</div></div>';
      // Состояние озвучек словами, а не одной галочкой: «включено» и «жду вот эти
      // две» — разные вещи, и по чекбоксу их не различить.
      var want = sh.want_voices || [];
      var vLabel = !sh.track_voices ? tr('nfVoices') + ': —'
        : want.length ? tr('nfVoices') + ': ' + esc(want.join(', '))
        : tr('nfVoices') + ': ' + tr('nfVoicesAny');
      h += '<div class="sp-actions">';
      h += '<button class="btn btn-sm" onclick="nfVoicesDialog(' + jsArg(key) + ')">' + vLabel + '</button>';
      h += '<button class="btn btn-danger" onclick="nfUnsub(' + jsArg(key) + ')">' + tr('nfUnsub') + '</button>';
      h += '</div></div>';
    }
    h += '<div class="sp-help">' + tr('nfVoicesHint') + '</div>';
  }
  h += '</div>';

  el.innerHTML = h;
}

window.nfModeChanged = function(digest) {
  var box = document.getElementById('nf-digest-at');
  if (box) box.style.display = digest ? '' : 'none';
};

window.nfSaveDelivery = function() {
  var mode = 'instant';
  var picked = document.querySelector('input[name="nfmode"]:checked');
  if (picked && picked.value === 'digest') mode = 'digest';
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

  var h = '<h2>' + tr('nfVoicesTitle') + '</h2>';
  h += '<div class="form-group"><label><input type="radio" name="vmode" value="all" '
    + (want.length ? '' : 'checked') + ' onchange="nfVoicesMode(false)"> ' + tr('nfVoicesAll') + '</label></div>';

  if (known.length) {
    h += '<div class="form-group"><label>' + tr('nfVoicesKnown') + '</label><div id="nf-known">';
    for (var j = 0; j < known.length; j++) {
      var checked = false;
      for (var q = 0; q < want.length; q++) {
        if (String(known[j]).toLowerCase() === String(want[q]).toLowerCase()) { checked = true; break; }
      }
      h += '<label class="sp-pin-tag" style="display:inline-flex;align-items:center;gap:5px;margin:0 6px 6px 0">'
        + '<input type="checkbox" class="nf-vbox" value="' + esc(known[j]) + '" ' + (checked ? 'checked' : '')
        + '> ' + esc(known[j]) + '</label>';
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
  if (!ytState) ytLoad();
  if (ytShortsMode === null) ytShortsLoad();
}

function renderServices() {
  var el = document.getElementById('tab-services');
  if (!el) return;
  var h = '';
  // Адрес общий на аккаунт и применяется на устройствах, где не введён свой локально.
  // Правится отсюда потому же, почему и плейлисты: с телефона вводить удобнее, чем с пульта.
  h += '<h2>' + tr('tsTitle') + '</h2><div class="section">';
  h += '<p class="sp-help" style="margin-top:0">' + tr('tsHint') + '</p>';
  h += '<div class="form-group"><label>' + tr('tsUrl') + '</label>'
    + '<input type="text" id="ts-url" placeholder="http://192.168.1.50:8090" value="' + esc(tsPref || '') + '"></div>';
  h += '<div style="display:flex;gap:10px">'
    + '<button class="btn btn-primary" style="flex:1" onclick="tsPrefSave()">' + tr('tsSave') + '</button>'
    + '<button class="btn" onclick="tsPrefClear()">' + tr('tsClear') + '</button></div>';
  h += '</div>';

  // «YouTube-аккаунт»: та же привязка, что и /youtube_auth в боте, но кнопкой:
  // команду набирать не надо, код не надо искать в переписке.
  h += '<h2>' + tr('ytTitle') + '</h2><div class="section" id="yt-box">';
  h += '<p class="sp-help" style="margin-top:0">' + tr('ytHint') + '</p>';
  h += '<div id="yt-body"></div>';
  h += '</div>';

  // Shorts: настройка аккаунтная и применяется НА СЕРВЕРЕ, поэтому её слушаются все клиенты
  // (Lampa, веб, Android, tvOS) без обновления самих клиентов.
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
      h += '<div class="sp-info"><div class="sp-name">' + esc(p.name || 'Playlist');
      if (p.is_global) h += ' <span class="sp-pin-tag">' + tr('iptvGlobal') + '</span>';
      h += '</div><div class="sp-meta">' + esc(meta) + '</div></div>';
      h += '<div class="sp-actions">';
      h += '<button class="btn btn-sm" onclick="iptvRefresh(' + jsArg(p.id) + ')">' + tr('iptvRefresh') + '</button>';
      // Общие плейлисты добавил админ — удалять их пользователю нечем и незачем.
      if (!p.is_global) {
        h += '<button class="btn btn-danger" onclick="iptvDelete(' + jsArg(p.id) + ')">' + tr('iptvDelete') + '</button>';
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
  var h = '<div style="display:flex;gap:10px;flex-wrap:wrap">';
  for (var i = 0; i < opts.length; i++) {
    var active = opts[i][0] === cur;
    h += '<button class="btn' + (active ? ' btn-primary' : '') + '" onclick="ytShortsSet(' + jsArg(opts[i][0]) + ')">'
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
  h += '<div class="profile-hero"><div class="profile-avatar">' + esc(initials) + '</div><div class="profile-info"><div class="profile-name">' + esc(name || 'User') + '</div>' + (p.telegram_id ? '<div class="profile-tgid">TG: ' + p.telegram_id + '</div>' : '') + '</div></div>';
  h += '<div class="profile-card"><div class="profile-card-title">\u23F3 ' + tr('subscription') + '</div><div class="profile-sub-row"><div><div class="profile-sub-date">' + (p.expired ? tr('expired') : tr('until') + ' ' + esc(p.expires_at)) + '</div></div><div class="profile-sub-badge ' + subBadgeCls + '">' + subText + '</div></div></div>';
  if (p.group_name) { h += '<div class="profile-card"><div class="profile-card-title">\uD83D\uDC65 ' + tr('group') + '</div><div class="profile-group-badge">' + esc(p.group_name) + '</div></div>'; }
  h += '<div class="profile-card"><div class="profile-card-title">\uD83D\uDCF1 ' + tr('devices') + (p.max_devices > 0 ? ' <span style="opacity:.5">' + p.device_count + '/' + p.max_devices + '</span>' : '') + '</div>';
  if (p.devices && p.devices.length > 0) { p.devices.forEach(function(d) { var uid = d.uid || ''; var masked = uid.length > 12 ? uid.substring(0,6) + '\u2026' + uid.substring(uid.length-4) : uid; h += '<div class="profile-device"><div class="profile-device-icon">\uD83D\uDCF1</div><div class="profile-device-info"><div class="profile-device-label">' + esc(d.label || masked) + '</div><div class="profile-device-meta">UID: ' + esc(masked) + ' \u00B7 ' + esc(d.last_seen) + '</div></div></div>'; }); }
  else { h += '<div class="profile-empty">' + tr('noDevices') + '</div>'; }
  h += '</div>';

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
  syncProfileLoad();
}

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
      + '<div class="sp-info"><div class="sp-name">' + esc(sp.username) + ' ' + pinTag + '</div>'
      + '<div class="sp-meta">' + esc(meta) + '</div></div>'
      + '<div class="sp-actions">'
      + '<button class="btn btn-sm" onclick="syncProfileSetPIN(\'' + sid + '\',\'' + snm + '\')">PIN</button>'
      + '<button class="btn btn-danger btn-sm" onclick="syncProfileDelete(\'' + sid + '\',\'' + snm + '\')">\u2715</button>'
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
  if (n % 10 === 1 && n % 100 !== 11) return '\u0434\u0435\u043d\u044c';
  if (n % 10 >= 2 && n % 10 <= 4 && (n % 100 < 10 || n % 100 >= 20)) return '\u0434\u043d\u044f';
  return '\u0434\u043d\u0435\u0439';
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
  }).catch(function(e) {
    document.getElementById('loading').textContent = tr('loadError') + e.message;
  });
}

// ---- Render main page ----

function render() {
  var h = '';

  // Tabs \u2014 the binds tab is omitted entirely when the admin disabled it.
  var tabCls = function(name) { return 'tab' + (activeTab === name ? ' active' : ''); };
  h += '<div class="tabs">';
  if (!bindsDisabled) h += '<div class="' + tabCls('binds') + '" data-tab="binds" onclick="switchTab(\'binds\')">' + tr('tabBinds') + '</div>';
  h += '<div class="' + tabCls('balancers') + '" data-tab="balancers" onclick="switchTab(\'balancers\')">' + tr('tabBalancers') + '</div>';
  h += '<div class="' + tabCls('iptv') + '" data-tab="iptv" onclick="switchTab(\'iptv\')">\uD83D\uDCFA ' + tr('tabIptv') + '</div>';
  h += '<div class="' + tabCls('services') + '" data-tab="services" onclick="switchTab(\'services\')">\uD83D\uDD17 ' + tr('tabServices') + '</div>';
  h += '<div class="' + tabCls('notify') + '" data-tab="notify" onclick="switchTab(\'notify\')">\uD83D\uDD14 ' + tr('tabNotify') + '</div>';
  h += '<div class="' + tabCls('profile') + '" data-tab="profile" onclick="switchTab(\'profile\')">\uD83D\uDC64 ' + tr('tabProfile') + '</div>';
  h += '</div>';

  // Tab: Binds
  if (!bindsDisabled) {
  h += '<div id="tab-binds"' + (activeTab === 'binds' ? '' : ' style="display:none"') + '>';

  // Bind section
  h += '<h2>' + tr('bindServices') + '</h2><div class="section">';
  bindServices.forEach(function(s) {
    var sec = config[sectionKey(s.id)] || {};
    var bound = sec.enable && (sec.token || sec.cookie);
    h += '<div class="card">';
    h += '<div class="card-info"><div class="card-name">' + s.name + '</div>';
    h += '<div class="card-status' + (bound ? ' bound' : '') + '">' + (bound ? tr('bound') : tr('notBound')) + '</div></div>';
    h += '<span class="badge ' + (s.badge === 'Free' ? 'badge-free' : 'badge-premium') + '">' + s.badge + '</span>';
    if (bound) {
      h += ' <button class="btn btn-danger btn-sm" onclick="unbind(\'' + s.id + '\')">✕</button>';
    } else {
      h += ' <button class="btn btn-primary btn-sm" onclick="startBind(\'' + s.id + '\')">' + tr('bind') + '</button>';
    }
    h += '</div>';
  });
  h += '</div>';

  // Sources section (token override)
  h += '<h2>' + tr('sources') + '</h2><div class="section">';
  tokenBalancers.forEach(function(b) {
    var sec = config[b.key] || {};
    var enabled = !!sec.enable;
    var token = sec.token || sec.cookie || '';
    h += '<div class="toggle-row">';
    h += '<div><div class="toggle-label">' + b.name + '</div>';
    if (token) h += '<div class="toggle-sub">' + token.substring(0, 16) + (token.length > 16 ? '...' : '') + '</div>';
    h += '</div>';
    h += '<label class="switch"><input type="checkbox" ' + (enabled ? 'checked' : '') + ' onchange="toggleTokenBalancer(\'' + b.key + '\',this.checked)"><span class="slider"></span></label>';
    h += '</div>';
    h += '<div class="token-row"><input class="token-input" placeholder="' + tr('tokenPlaceholder') + '" value="' + escHtml(token) + '" onchange="setToken(\'' + b.key + '\',this.value)"></div>';
  });
  h += '</div>';

  h += '</div>'; // end tab-binds
  } // if (!bindsDisabled)

  // Tab: Balancers / Profile — shown per activeTab (balancers is the default
  // landing tab when binds is disabled).
  h += '<div id="tab-balancers"' + (activeTab === 'balancers' ? '' : ' style="display:none"') + '><div class="loading">' + tr('loadingBalancers') + '</div></div>';
  h += '<div id="tab-iptv"' + (activeTab === 'iptv' ? '' : ' style="display:none"') + '><div class="loading">' + tr('loading') + '</div></div>';
  h += '<div id="tab-services"' + (activeTab === 'services' ? '' : ' style="display:none"') + '><div class="loading">' + tr('loading') + '</div></div>';
  h += '<div id="tab-notify"' + (activeTab === 'notify' ? '' : ' style="display:none"') + '><div class="loading">' + tr('loading') + '</div></div>';
  h += '<div id="tab-profile"' + (activeTab === 'profile' ? '' : ' style="display:none"') + '><div class="loading">' + tr('loading') + '</div></div>';

  document.getElementById('app').innerHTML = h;
  setupMainButton();

  // If balancers were already loaded, re-render them; otherwise load them now
  // when we've landed on the Балансеры tab (e.g. binds disabled).
  if (balancerData) renderBalancers();
  else if (activeTab === 'balancers') loadBalancers();
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
      hasChanges = false;
      setupMainButton();
    } else {
      toast(tr('error') + (r.error || 'unknown'));
    }
  }).catch(function() {
    toast(tr('saveError'));
  }).finally(function() {
    if (tg) tg.MainButton.hideProgress();
  });
}

function setupMainButton() {
  if (!tg) return;
  if (hasChanges) {
    tg.MainButton.setText(tr('save'));
    tg.MainButton.show();
    tg.MainButton.onClick(save);
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

function renderBalancers() {
  var el = document.getElementById('tab-balancers');
  if (!el || !balancerData) return;

  var h = '';
  balancerData.groups.forEach(function(group) {
    var bals = balancerData.balancers.filter(function(b) { return b.group === group.key; });
    if (bals.length === 0) return;

    var enabledCount = 0;
    bals.forEach(function(b) {
      var eff = b.globalEnabled || b.userBound;
      if (getVisibility(b.key, eff)) enabledCount++;
    });

    var allOn = enabledCount === bals.length;

    h += '<div class="group-header">';
    h += '<div class="group-title"><span>' + group.icon + '</span> ' + escHtml(group.label);
    h += '<span class="group-count">' + enabledCount + '/' + bals.length + '</span></div>';
    h += '<label class="switch"><input type="checkbox" ' + (allOn ? 'checked' : '') + ' onchange="toggleGroup(\'' + group.key + '\',this.checked)"><span class="slider"></span></label>';
    h += '</div>';

    bals.forEach(function(b) {
      var eff = b.globalEnabled || b.userBound;
      var vis = getVisibility(b.key, eff);
      var globalOff = !b.globalEnabled && !b.userBound;
      h += '<div class="bal-row' + (globalOff ? ' disabled' : '') + '">';
      h += '<div><span class="bal-name">' + escHtml(b.name) + '</span>';
      if (b.quality) h += '<span class="bal-quality">' + b.quality + '</span>';
      if (b.userBound && !b.globalEnabled) h += '<span class="bal-note" style="color:var(--success)">' + tr('userBound') + '</span>';
      else if (globalOff) h += '<span class="bal-note">' + tr('globallyOff') + '</span>';
      h += '</div>';
      h += '<label class="switch"><input type="checkbox" ' + (vis ? 'checked' : '') + (globalOff ? ' disabled' : '') + ' onchange="toggleBalancerVis(\'' + b.key + '\',this.checked)"><span class="slider"></span></label>';
      h += '</div>';
    });
  });

  el.innerHTML = h;
}

window.toggleBalancerVis = function(key, on) {
  balancerVisibility[key] = on;
  hasChanges = true;
  setupMainButton();
  renderBalancers();
};

window.toggleGroup = function(groupKey, on) {
  if (!balancerData) return;
  balancerData.balancers.forEach(function(b) {
    if (b.group === groupKey) {
      balancerVisibility[b.key] = on;
    }
  });
  hasChanges = true;
  setupMainButton();
  renderBalancers();
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
