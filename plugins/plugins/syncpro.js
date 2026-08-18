// SyncPro — unified cross-device sync for Lampa.
//
// Replaces the legacy quartet sync.js + bookmark.js + timecode.js + backup.js
// with a single plugin. All sync domains (favorites, timecodes, watch history,
// torrents, search history, plugin list, manual backup) live behind individual
// toggles in Settings → Синхронизация. The plugin also embeds a Profile section
// that lets users register / log in / generate a sync code so the same data
// follows them between devices without depending on Telegram auth.
//
// Auth model: every request relies on cookies that the auth middleware on the
// server (internal/auth/auth.go) reads — `lampac_token` (TG bot) or
// `lampac_profile_session` (this plugin's profile flow). Legacy `?uid=` and
// `?account_email=` are NOT appended because they were the source of repeated
// impersonation and NAT-collision bugs (see project memory 2026-05-25).
//
// Server endpoints exercised:
//   GET  /bookmark/list, POST /bookmark/{set,add,added,remove}
//   GET  /timecode/all,  POST /timecode/add
//   POST /storage/{set,get}?path=<domain>&pathfile=<profile_id>
//   POST /api/profile/{register,login,logout,change-password,sync-code/...}
//   GET  /api/profile/me
//
// Templated placeholders replaced by genericPluginJSHandler at serve time:
//   {localhost} — server base URL
//   {token}    — TG device token (may be empty, e.g. for anonymous installs)

(function () {
'use strict';

if (window.lampac_syncpro_plugin) return;
window.lampac_syncpro_plugin = true;

var HOST = '{localhost}';
var TOKEN = '{token}';

// ----------------------------------------------------------------------
//  i18n
// ----------------------------------------------------------------------

function loadLang() {
    Lampa.Lang.add({
        syncpro_title:               { ru: 'Синхронизация', en: 'Sync', uk: 'Синхронізація' },
        syncpro_section_domains:     { ru: 'Что синхронизировать', en: 'What to sync', uk: 'Що синхронізувати' },
        syncpro_section_profile:     { ru: 'Профиль', en: 'Profile', uk: 'Профіль' },
        syncpro_section_actions:     { ru: 'Действия', en: 'Actions', uk: 'Дії' },

        syncpro_dom_bookmarks:       { ru: 'Закладки', en: 'Bookmarks', uk: 'Закладки' },
        syncpro_dom_timecodes:       { ru: 'Таймкоды', en: 'Timecodes', uk: 'Таймкоди' },
        syncpro_dom_history:         { ru: 'История просмотров', en: 'Watch history', uk: 'Історія перегляду' },
        syncpro_dom_torrents:        { ru: 'Торренты', en: 'Torrents', uk: 'Торенти' },
        syncpro_dom_search:          { ru: 'История поиска', en: 'Search history', uk: 'Історія пошуку' },
        syncpro_dom_plugins:         { ru: 'Список плагинов', en: 'Installed plugins', uk: 'Список плагінів' },

        syncpro_action_backup_save:  { ru: 'Создать бэкап на сервере', en: 'Save backup to server', uk: 'Створити бекап' },
        syncpro_action_backup_load:  { ru: 'Восстановить из бэкапа', en: 'Restore backup', uk: 'Відновити з бекапу' },
        syncpro_action_force_pull:   { ru: 'Подтянуть данные сейчас', en: 'Pull all data now', uk: 'Завантажити зараз' },

        syncpro_profile_status_anon: { ru: 'Не вошёл', en: 'Not signed in', uk: 'Не увійшов' },
        syncpro_profile_status_tg:   { ru: 'Вход через Telegram', en: 'Signed in via Telegram', uk: 'Вхід через Telegram' },
        syncpro_profile_status_prof: { ru: 'Профиль: {name}', en: 'Profile: {name}', uk: 'Профіль: {name}' },

        syncpro_profile_login:       { ru: 'Войти в профиль', en: 'Sign in', uk: 'Увійти' },
        syncpro_profile_register:    { ru: 'Создать профиль', en: 'Create profile', uk: 'Створити профіль' },
        syncpro_profile_logout:      { ru: 'Выйти из профиля', en: 'Sign out', uk: 'Вийти' },
        syncpro_profile_chgpass:     { ru: 'Сменить пароль', en: 'Change password', uk: 'Змінити пароль' },
        syncpro_profile_sync_issue:  { ru: 'Получить код для другого устройства', en: 'Get sync code', uk: 'Отримати код синхронізації' },
        syncpro_profile_sync_redeem: { ru: 'Ввести код синхронизации', en: 'Enter sync code', uk: 'Ввести код синхронізації' },
        syncpro_profile_pin_login:   { ru: 'Войти по PIN', en: 'Sign in with PIN', uk: 'Увійти за PIN' },
        syncpro_field_pin:           { ru: 'PIN (4-8 цифр)', en: 'PIN (4-8 digits)', uk: 'PIN (4-8 цифр)' },

        syncpro_field_username:      { ru: 'Имя пользователя', en: 'Username', uk: 'Ім\'я користувача' },
        syncpro_field_password:      { ru: 'Пароль', en: 'Password', uk: 'Пароль' },
        syncpro_field_password_old:  { ru: 'Старый пароль', en: 'Old password', uk: 'Старий пароль' },
        syncpro_field_password_new:  { ru: 'Новый пароль', en: 'New password', uk: 'Новий пароль' },
        syncpro_field_code:          { ru: 'Код синхронизации', en: 'Sync code', uk: 'Код синхронізації' },

        syncpro_msg_done:            { ru: 'Готово', en: 'Done', uk: 'Готово' },
        syncpro_msg_pulled:          { ru: 'Данные подтянуты', en: 'Data pulled', uk: 'Дані завантажено' },
        syncpro_msg_backup_ok:       { ru: 'Бэкап сохранён', en: 'Backup saved', uk: 'Бекап збережено' },
        syncpro_msg_backup_restored: { ru: 'Бэкап восстановлен, перезагрузка…', en: 'Restored, reloading…', uk: 'Відновлено, перезавантаження…' },
        syncpro_action_wipe:         { ru: 'Удалить всё с серверов', en: 'Delete everything from servers', uk: 'Видалити все з серверів' },
        syncpro_wipe_confirm:        { ru: 'Удалить с серверов бэкап и все sync-данные аккаунта? Локальные настройки этого устройства останутся.', en: 'Delete the account backup and all sync data from the servers? Local settings on this device stay.', uk: 'Видалити з серверів бекап і всі sync-дані акаунта? Локальні налаштування цього пристрою залишаться.' },
        syncpro_msg_wiped:           { ru: 'Удалено файлов: {n}', en: 'Files deleted: {n}', uk: 'Видалено файлів: {n}' },
        syncpro_msg_wiped_nodes:     { ru: ' (+{m} на других серверах)', en: ' (+{m} on other servers)', uk: ' (+{m} на інших серверах)' },
        syncpro_offer_title:         { ru: 'Найден бэкап от {date}', en: 'Backup from {date} found', uk: 'Знайдено бекап від {date}' },
        syncpro_offer_text:          { ru: 'Применить сохранённые настройки аккаунта на этом устройстве?', en: 'Apply the account\'s saved settings on this device?', uk: 'Застосувати збережені налаштування акаунта на цьому пристрої?' },
        syncpro_offer_apply:         { ru: 'Применить', en: 'Apply', uk: 'Застосувати' },
        syncpro_offer_skip:          { ru: 'Не сейчас', en: 'Not now', uk: 'Не зараз' },
        syncpro_msg_login_ok:        { ru: 'Вход выполнен', en: 'Signed in', uk: 'Вхід виконано' },
        syncpro_msg_logout_ok:       { ru: 'Вы вышли', en: 'Signed out', uk: 'Ви вийшли' },
        syncpro_msg_register_ok:     { ru: 'Профиль создан', en: 'Profile created', uk: 'Профіль створено' },
        syncpro_msg_chgpass_ok:      { ru: 'Пароль изменён', en: 'Password changed', uk: 'Пароль змінено' },
        syncpro_msg_sync_code:       { ru: 'Код: {code}\nДействителен 1 час, ввести на другом устройстве', en: 'Code: {code}\nValid for 1 hour, type it on the other device', uk: 'Код: {code}' },

        syncpro_err_generic:         { ru: 'Ошибка ({code})', en: 'Error ({code})', uk: 'Помилка ({code})' },
        syncpro_err_username_taken:  { ru: 'Имя занято', en: 'Username already taken', uk: 'Ім\'я вже зайняте' },
        syncpro_err_username_inv:    { ru: 'Имя: 3-32 символа [a-z0-9_-]', en: 'Username: 3-32 chars [a-z0-9_-]', uk: 'Ім\'я: 3-32 символи' },
        syncpro_err_password_weak:   { ru: 'Пароль слишком короткий (мин. 6)', en: 'Password too short (min 6)', uk: 'Пароль закороткий (мін. 6)' },
        syncpro_err_invalid_creds:   { ru: 'Неверное имя или пароль', en: 'Invalid username or password', uk: 'Невірне ім\'я або пароль' },
        syncpro_err_sync_invalid:    { ru: 'Код недействителен', en: 'Code invalid or expired', uk: 'Код недійсний' },
        syncpro_err_sync_used:       { ru: 'Код уже использован', en: 'Code already used', uk: 'Код вже використано' },
        syncpro_err_rate_limited:    { ru: 'Слишком часто, подождите', en: 'Too many requests, wait', uk: 'Зачекайте' },
        syncpro_err_disabled:        { ru: 'Профильная синхронизация выключена на сервере', en: 'Profile sync disabled on server', uk: 'Вимкнено на сервері' },

        // -- Compact UI: section pickers --
        syncpro_open_profile_mgmt:   { ru: 'Управление профилем', en: 'Profile actions', uk: 'Керування профілем' },
        syncpro_open_domains:        { ru: 'Что синхронизировать', en: 'What to sync', uk: 'Що синхронізувати' },
        syncpro_open_actions:        { ru: 'Действия', en: 'Actions', uk: 'Дії' },
        syncpro_summary_domains:     { ru: 'Включено {n} из {total}', en: '{n} of {total} enabled', uk: 'Увімкнено {n} з {total}' },
        syncpro_msg_tg_no_code:      { ru: 'Вы уже входите через Telegram — устройства синхронизируются автоматически, отдельный код не нужен.', en: 'You are signed in via Telegram — devices already sync automatically.', uk: 'Ви вже увійшли через Telegram.' },
        syncpro_msg_tg_no_profile:   { ru: 'Вы вошли через Telegram. Чтобы использовать профиль с паролем — выйдите из TG и войдите как профиль.', en: 'Signed in via Telegram. Sign out of TG to use a profile.', uk: 'Ви увійшли через Telegram.' },
        syncpro_back:                { ru: 'Назад', en: 'Back', uk: 'Назад' },

        // -- Storage-side error slugs (msg field from writeStorageError) --
        syncpro_err_storage_disabled:{ ru: 'Хранилище на сервере отключено', en: 'Server storage is disabled', uk: 'Серверне сховище вимкнено' },
        syncpro_err_storage_max:     { ru: 'Слишком большой бэкап для сервера', en: 'Backup is larger than server limit', uk: 'Бекап більше за ліміт сервера' },
        syncpro_err_storage_path:    { ru: 'Сервер отверг путь — войдите в профиль или TG', en: 'Server rejected path — sign in to TG or profile', uk: 'Сервер відхилив шлях' },
        syncpro_err_storage_lock:    { ru: 'Сервер не смог записать файл', en: 'Server failed to write file', uk: 'Помилка запису на сервері' },
        syncpro_err_network:         { ru: 'Нет соединения с сервером', en: 'No connection to the server', uk: 'Немає з\'єднання з сервером' },
        syncpro_err_backup_empty:    { ru: 'Бэкап ещё не создан', en: 'No backup saved yet', uk: 'Бекап ще не створено' },
        syncpro_err_backup_parse:    { ru: 'Бэкап повреждён, не удалось прочитать', en: 'Backup is corrupted', uk: 'Бекап пошкоджено' },
        syncpro_err_too_large_proxy: { ru: 'Бэкап слишком большой для прокси (nginx client_max_body_size). Попросите админа увеличить лимит до 50M.', en: 'Backup exceeds the reverse-proxy body limit (nginx client_max_body_size). Ask the operator to raise it to 50M.', uk: 'Бекап більший за ліміт зворотного проксі (nginx client_max_body_size).' },

        // -- Active-profile switcher (TG users) --
        syncpro_switch_profile:      { ru: 'Сменить активный профиль', en: 'Switch active profile', uk: 'Змінити активний профіль' },
        syncpro_active_profile:      { ru: 'Активный профиль: {name}', en: 'Active profile: {name}', uk: 'Активний профіль: {name}' },
        syncpro_active_default:      { ru: 'Активный профиль: общий', en: 'Active profile: shared', uk: 'Активний профіль: спільний' },
        syncpro_profile_default:     { ru: 'Общий', en: 'Shared', uk: 'Спільний' },
        syncpro_profile_loading:     { ru: 'Загрузка списка профилей…', en: 'Loading profiles…', uk: 'Завантаження профілів…' },
        syncpro_profile_none_owned:  { ru: 'Нет своих профилей. Создайте на /bkit или /kit.', en: 'No owned profiles yet. Create one in /bkit or /kit.', uk: 'Немає профілів. Створіть у /bkit або /kit.' },
        syncpro_switched:            { ru: 'Профиль переключён: {name}', en: 'Profile switched: {name}', uk: 'Профіль перемкнено: {name}' },

        // -- Unified sub-profiles (Netflix-style, shared with alpac clients) --
        syncpro_profiles_title:      { ru: 'Профили', en: 'Profiles', uk: 'Профілі' },
        syncpro_profile_create:      { ru: 'Создать профиль', en: 'Create profile', uk: 'Створити профіль' },
        syncpro_profile_manage:      { ru: 'Управление профилями', en: 'Manage profiles', uk: 'Керування профілями' },
        syncpro_rename_balancers:    { ru: 'Переименовать балансеры', en: 'Rename balancers', uk: 'Перейменувати балансери' },
        syncpro_balancers_empty:     { ru: 'Балансеры появятся после первого поиска фильма', en: 'Balancers appear after your first title search', uk: 'Балансери з’являться після першого пошуку' },
        syncpro_balancers_reset:     { ru: 'Сбросить все переименования', en: 'Reset all renames', uk: 'Скинути всі перейменування' },
        syncpro_balancer_original:   { ru: 'Ориг', en: 'Orig', uk: 'Ориг' },
        syncpro_profile_rename:      { ru: 'Переименовать', en: 'Rename', uk: 'Перейменувати' },
        syncpro_profile_avatar:      { ru: 'Аватар (эмодзи или URL картинки)', en: 'Avatar (emoji or image URL)', uk: 'Аватар (емодзі або URL)' },
        syncpro_profile_kids_on:     { ru: 'Сделать детским', en: 'Mark as kids', uk: 'Зробити дитячим' },
        syncpro_profile_kids_off:    { ru: 'Убрать детский режим', en: 'Unmark kids', uk: 'Прибрати дитячий режим' },
        syncpro_profile_set_pin:     { ru: 'Установить PIN входа', en: 'Set entry PIN', uk: 'Встановити PIN входу' },
        syncpro_profile_clear_pin:   { ru: 'Убрать PIN', en: 'Clear PIN', uk: 'Прибрати PIN' },
        syncpro_profile_delete:      { ru: 'Удалить профиль', en: 'Delete profile', uk: 'Видалити профіль' },
        syncpro_field_profile_name:  { ru: 'Название профиля', en: 'Profile name', uk: 'Назва профілю' },
        syncpro_enter_pin:           { ru: 'PIN профиля «{name}»', en: 'PIN for "{name}"', uk: 'PIN профілю «{name}»' },
        syncpro_wrong_pin:           { ru: 'Неверный PIN', en: 'Wrong PIN', uk: 'Невірний PIN' },
        syncpro_profile_limit:       { ru: 'Максимум 5 профилей', en: 'Profile limit reached (5)', uk: 'Максимум 5 профілів' },
        syncpro_switching:           { ru: 'Переключение профиля…', en: 'Switching profile…', uk: 'Перемикання профілю…' },
        syncpro_switch_offline:      { ru: 'Сервер недоступен — профиль восстановлен из локальной копии', en: 'Server unreachable — restored from local copy', uk: 'Сервер недоступний — відновлено з локальної копії' },
        syncpro_switch_reload:       { ru: 'Полная перезагрузка при смене профиля', en: 'Full reload on profile switch', uk: 'Повне перезавантаження при зміні профілю' },
        syncpro_need_auth:           { ru: 'Войдите через Telegram или профиль, чтобы использовать профили', en: 'Sign in (Telegram or profile) to use profiles', uk: 'Увійдіть, щоб використовувати профілі' },
    });
}

function errorMessageFromSlug(slug, code) {
    var map = {
        username_taken:      'syncpro_err_username_taken',
        username_invalid:    'syncpro_err_username_inv',
        password_weak:       'syncpro_err_password_weak',
        invalid_credentials: 'syncpro_err_invalid_creds',
        sync_code_invalid:   'syncpro_err_sync_invalid',
        sync_code_used:      'syncpro_err_sync_used',
        rate_limited:        'syncpro_err_rate_limited',
        profile_store_disabled: 'syncpro_err_disabled',
        // 401 from sync-code/issue, change-password, etc. when the user
        // taps a profile-only action while TG-authed (race between UI
        // refresh and server check). Friendlier than "Error (401)".
        not_authenticated:   'syncpro_msg_tg_no_code',
        // Slugs from /storage/* (writeStorageError). The msg field on the
        // 200-with-success:false response carries one of these — surfacing
        // them in plain Russian beats showing "Ошибка (200)".
        disabled:            'syncpro_err_storage_disabled',
        max_size:            'syncpro_err_storage_max',
        outFile:             'syncpro_err_storage_path',
        fileLock:            'syncpro_err_storage_lock',
        network:             'syncpro_err_network',
        nodata:              'syncpro_err_backup_empty',
        parse:               'syncpro_err_backup_parse',
        too_large_proxy:     'syncpro_err_too_large_proxy',
    };
    if (slug && map[slug]) return Lampa.Lang.translate(map[slug]);
    return Lampa.Lang.translate('syncpro_err_generic').replace('{code}', code || slug || '?');
}

// ----------------------------------------------------------------------
//  Settings storage helpers
// ----------------------------------------------------------------------
//
// Toggles use `syncpro_<domain>` keys in Lampa.Storage. Default to true so
// new installs sync everything; users opt out per domain.

function pref(domain, def) {
    var key = 'syncpro_' + domain;
    var v = Lampa.Storage.field(key);
    if (typeof v === 'undefined' || v === null || v === '') {
        if (typeof def !== 'undefined') return def;
        return true;
    }
    return v === true || v === 'true' || v === 1 || v === '1';
}

function url(path) {
    // No legacy query params. Server resolves user from cookies
    // (lampac_token or lampac_profile_session). connectionId is appended
    // when the invc-ws hub is up so the server can skip echoing the event
    // back to the originating connection.
    var u = HOST + path;
    if (TOKEN) u = Lampa.Utils.addUrlComponent(u, 'token=' + encodeURIComponent(TOKEN));
    if (window.lwsEvent && window.lwsEvent.connectionId) {
        u = Lampa.Utils.addUrlComponent(u, 'connectionId=' + encodeURIComponent(window.lwsEvent.connectionId));
    }
    var profileSeg = Lampa.Storage.get('lampac_profile_id', '');
    if (profileSeg) {
        // bookmark / timecode read `profile_id`, /storage reads `pathfile`
        // — historical naming split. We always send both: the unused one is
        // silently dropped by each handler, and a single source of truth
        // (lampac_profile_id) means switching the active profile from the
        // settings sheet partitions ALL synced domains, not just two of
        // them. Verified server-side: storage_api.go:50 and timecode_api.go:108.
        u = Lampa.Utils.addUrlComponent(u, 'profile_id=' + encodeURIComponent(profileSeg));
        u = Lampa.Utils.addUrlComponent(u, 'pathfile=' + encodeURIComponent(profileSeg));
    }
    return u;
}

// ----------------------------------------------------------------------
//  Network primitives
// ----------------------------------------------------------------------
//
// We use raw XHR (not Lampa.Reguest) for the profile API because we need
// to inspect HTTP status codes and JSON error slugs. Lampa.Reguest swallows
// non-2xx into a generic error.

// Profile-session token kept in Lampa.Storage so it survives across the
// Lampa Android WebView, which silently drops Set-Cookie from XHR
// responses (see auth.go resolveUser for the server-side header path).
// In a normal browser the cookie path is enough; the storage shadow is
// harmless because the server treats whichever is present.
// Dual storage namespace: `alpac_*` is our brand-scoped key (preferred),
// `lampac_*` is the legacy name we still read for backward compatibility.
// We *write* the alpac key only — if a foreign plugin overwrites the
// legacy lampac key, our session survives via alpac.
function getProfileSessionToken() {
    try {
        var v = Lampa.Storage.get('alpac_profile_session_token', '');
        if (v) return v;
        v = Lampa.Storage.get('lampac_profile_session_token', '');
        if (v) return v;
    } catch (e) { /* Storage not ready */ }
    return '';
}
function setProfileSessionToken(tok) {
    try {
        Lampa.Storage.set('alpac_profile_session_token', tok || '');
        // Mirror to the legacy key so older syncpro builds running on the
        // same device continue to work after one of them logs in/out.
        Lampa.Storage.set('lampac_profile_session_token', tok || '');
    } catch (e) { /* ignore */ }
}

// getLampacToken reads the TG device token from any of its known
// hideouts. The TG bot's OAuth flow sets a `lampac_token` cookie via
// navigation response — that's the canonical store on a normal browser.
// In Lampa's Android WebView the cookie often DOES get persisted, but
// the WebView's network stack sometimes withholds it from same-host
// XHRs (varies by Android API level + how the wrapper initialised
// CookieManager). lampainit + iptv2 also keep a copy in Lampa.Storage
// for exactly this case. We accept whichever channel has a value so
// the syncpro plugin works regardless of which runtime it's loaded in.
//
// Server side reads the same value from `X-Lampac-Token` header in
// auth.go resolveUser, in addition to the cookie + ?token=.
function getLampacToken() {
    // Search both namespaces (alpac_* first as our brand, lampac_* as the
    // legacy / external fallback). Each is checked in BOTH Lampa.Storage
    // and document.cookie because the TG auth flow on the server writes
    // both cookies, and external plugins / older Lampa builds may have
    // populated Lampa.Storage with the legacy name.
    var names = ['alpac_token', 'lampac_token'];
    try {
        for (var i = 0; i < names.length; i++) {
            var v = Lampa.Storage.get(names[i], '');
            if (v) return v;
        }
    } catch (e) { /* Storage not ready */ }
    try {
        for (var j = 0; j < names.length; j++) {
            var re = new RegExp('(?:^|;\\s*)' + names[j] + '=([^;]*)');
            var m = document.cookie.match(re);
            if (m && m[1]) return decodeURIComponent(m[1]);
        }
    } catch (e) { /* document.cookie may throw in odd sandboxes */ }
    // Durable raw-localStorage anchor (lampac_auth_token, written by the
    // auth gate on login/recovery). TV WebViews — LG webOS in particular —
    // wipe the cookie jar on every app relaunch and nothing populates
    // Lampa.Storage on a stock Lampa install, so without this rung the
    // profile pane greets an authenticated user with «не вошёл» after
    // every restart.
    try {
        var ls = localStorage.getItem('lampac_auth_token');
        if (ls) return ls;
    } catch (e) { /* raw localStorage unavailable */ }
    return '';
}

function httpJSON(method, path, body, cb, errCb) {
    try {
        var xhr = new XMLHttpRequest();
        xhr.open(method, url(path), true);
        xhr.withCredentials = true;
        xhr.setRequestHeader('Content-Type', 'application/json;charset=UTF-8');
        // Use the brand-scoped X-Alpac-* headers — the server reads
        // X-Lampac-* as a backward-compat fallback for older clients.
        var sessTok = getProfileSessionToken();
        if (sessTok) xhr.setRequestHeader('X-Alpac-Profile-Session', sessTok);
        var lampTok = getLampacToken();
        if (lampTok) xhr.setRequestHeader('X-Alpac-Token', lampTok);
        xhr.onreadystatechange = function () {
            if (xhr.readyState !== 4) return;
            var parsed = null;
            try { parsed = xhr.responseText ? JSON.parse(xhr.responseText) : null; } catch (e) { /* ignore */ }
            if (xhr.status >= 200 && xhr.status < 300) {
                // Auth responses carry session_token mirroring the cookie
                // — persist it for the next request when the WebView ate
                // the Set-Cookie header.
                if (parsed && typeof parsed.session_token === 'string' && parsed.session_token) {
                    setProfileSessionToken(parsed.session_token);
                }
                if (cb) cb(parsed, xhr.status);
            } else {
                if (errCb) errCb(parsed, xhr.status);
            }
        };
        xhr.send(body ? JSON.stringify(body) : null);
    } catch (e) {
        if (errCb) errCb(null, 0);
    }
}

// ----------------------------------------------------------------------
//  Profile API (talks to /api/profile/*)
// ----------------------------------------------------------------------

var Profile = {
    me: function (cb) {
        httpJSON('GET', '/api/profile/me', null, function (j) {
            cb(j || { authenticated: false, auth_method: 'none' });
        }, function () {
            cb({ authenticated: false, auth_method: 'none' });
        });
    },
    register: function (username, password, cb, errCb) {
        httpJSON('POST', '/api/profile/register', { username: username, password: password }, cb, errCb);
    },
    login: function (username, password, cb, errCb) {
        httpJSON('POST', '/api/profile/login', { username: username, password: password }, cb, errCb);
    },
    logout: function (cb) {
        httpJSON('POST', '/api/profile/logout', null, cb, cb);
    },
    changePassword: function (oldPass, newPass, cb, errCb) {
        httpJSON('POST', '/api/profile/change-password', { old_password: oldPass, new_password: newPass }, cb, errCb);
    },
    issueCode: function (cb, errCb) {
        httpJSON('POST', '/api/profile/sync-code/issue', {}, cb, errCb);
    },
    redeemCode: function (code, cb, errCb) {
        httpJSON('POST', '/api/profile/sync-code/redeem', { code: code }, cb, errCb);
    },
    // PIN login (no TG required) — user enters {username, PIN}. The PIN is
    // set on the server side by the profile's TG-owner via /api/profile/
    // owned/set-pin or the /profiles bot command.
    pinLogin: function (username, pin, cb, errCb) {
        httpJSON('POST', '/api/profile/pin-login', { username: username, pin: pin }, cb, errCb);
    },
    // listOwned — returns profiles owned by the currently-authenticated TG
    // user. Requires the lampac_token cookie (auto-sent by Lampa wrapper).
    // Legacy fallback for servers without the unified /api/profiles API.
    listOwned: function (cb, errCb) {
        httpJSON('GET', '/api/profile/owned', null, cb, errCb);
    },
};

// ----------------------------------------------------------------------
//  Unified sub-profiles (Netflix-style) — /api/profiles/* is the SAME
//  profile set the alpac native clients see via /capi/profiles: one ID
//  space, so the per-profile buckets on the server line up across apps.
// ----------------------------------------------------------------------

var SubProfiles = {
    list:   function (cb, errCb)      { httpJSON('GET',  '/api/profiles', null, cb, errCb); },
    create: function (data, cb, errCb){ httpJSON('POST', '/api/profiles/create', data, cb, errCb); },
    update: function (data, cb, errCb){ httpJSON('POST', '/api/profiles/update', data, cb, errCb); },
    remove: function (id, cb, errCb)  { httpJSON('POST', '/api/profiles/delete', { id: id }, cb, errCb); },
    verify: function (id, pin, cb, errCb) { httpJSON('POST', '/api/profiles/verify', { id: id, pin: pin }, cb, errCb); },
};

// switching — write gate. While a profile switch is in flight every outgoing
// sync write is suppressed, so data from the OLD profile can't leak into the
// new profile's server buckets (the same job levende's profiles.js does with
// window.sync_disable + an $.ajaxPrefilter abort). Our writes all go through
// this plugin, so a module flag is enough — no XHR interception needed.
var switching = false;

// PINs verified in THIS app session, keyed by profile id. A locked profile is
// gated once per session (on switch or at boot) and not re-prompted on later
// hydrate passes within the same run.
var _pinVerifiedSession = {};

// ----------------------------------------------------------------------
//  Per-domain sync modules
// ----------------------------------------------------------------------

// Bookmarks (replaces bookmark.js)
var Bookmarks = {
    enabled: function () { return pref('bookmarks'); },
    bound: false,

    bind: function () {
        if (this.bound) return;
        this.bound = true;
        var self = this;
        var fav = Lampa.Favorite;
        if (!fav || !fav.listener) return;
        fav.listener.follow('add', function (e) {
            if (!self.enabled()) return;
            if (e.card && e.card.received) return;
            self.sendAdd(e);
        });
        fav.listener.follow('added', function (e) {
            if (!self.enabled()) return;
            if (e.card && e.card.received) return;
            self.sendAdded(e);
        });
        fav.listener.follow('remove', function (e) {
            if (!self.enabled()) return;
            if (e.card && e.card.received) return;
            self.sendRemove(e);
        });
        Lampa.Listener.follow('lampac', function (e) {
            if (e.name === 'bookmark_pullFromServer' && self.enabled()) self.pull();
            else if (e.name === 'bookmark_set' && self.enabled()) {
                self.applyServerSet(e.value);
                httpJSON('POST', '/bookmark/set', e.value);
            }
        });
        // Periodic refresh on tab visibility (devices that sleep miss WS pushes).
        var lastFocus = Date.now();
        document.addEventListener('visibilitychange', function () {
            if (Date.now() - lastFocus > 10 * 60 * 1000 && self.enabled()) self.pull();
            lastFocus = Date.now();
        });
    },

    sanitize: function (card) {
        if (!card) return null;
        if (Lampa.Utils.clearCard) return Lampa.Utils.clearCard(Lampa.Arrays.clone(card));
        return card;
    },

    sendAdd: function (e) {
        if (switching) return;
        var id = e && e.card && typeof e.card.id !== 'undefined' ? e.card.id : null;
        if (id === null) return;
        httpJSON('POST', '/bookmark/add', {
            where: e.where || '',
            method: e.method || 'card',
            card_id: id,
            id: id,
            card: this.sanitize(e.card),
        });
    },
    sendAdded: function (e) {
        if (switching) return;
        var id = e && e.card && typeof e.card.id !== 'undefined' ? e.card.id : null;
        if (id === null) return;
        httpJSON('POST', '/bookmark/added', {
            where: e.where || '',
            method: e.method || 'card',
            card_id: id,
            id: id,
            card: this.sanitize(e.card),
        });
    },
    sendRemove: function (e) {
        if (switching) return;
        var id = (e && e.card && typeof e.card.id !== 'undefined' ? e.card.id : (e && e.id));
        if (typeof id === 'undefined' || id === null) return;
        httpJSON('POST', '/bookmark/remove', {
            where: e.where || '',
            method: e.method || 'card',
            card_id: id,
            id: id,
            card: this.sanitize(e.card),
        });
    },

    pull: function (done) {
        var self = this;
        httpJSON('GET', '/bookmark/list', null, function (json) {
            if (json && !json.dbInNotInitialization) self.applyServerSet(json);
            if (done) done(true);
        }, function () {
            if (done) done(false);
        });
    },

    applyServerSet: function (data) {
        // data is { card: [...], history: [...], like: [...], ... }.
        // Translate into the 'favorite' shape used by Lampa.
        if (!data) return;
        var fav = {};
        try { fav = Lampa.Storage.get('favorite', '{}') || {}; } catch (e) { fav = {}; }
        if (typeof fav !== 'object' || Array.isArray(fav)) fav = {};

        Object.keys(data).forEach(function (k) {
            if (k === 'success' || k === 'dbInNotInitialization') return;
            fav[k] = data[k];
        });
        // MUST go through Lampa.Storage.set (not bare localStorage.setItem):
        // Storage.get serves from the in-memory `readed[]` cache, so a raw
        // write leaves Favorite.read() seeing the stale value until a full
        // page reload. This was exactly the "profile switch only syncs with
        // «полная перезагрузка»" bug. nolisten=true — a 'favorite' change
        // event would bounce back into our own push path.
        try { Lampa.Storage.set('favorite', fav, true); } catch (e) { /* quota */ }
    },
};

// Timecodes (replaces timecode.js)
var Timecodes = {
    enabled: function () { return pref('timecodes'); },
    bound: false,
    bind: function () {
        if (this.bound) return;
        this.bound = true;
        var self = this;
        if (!Lampa.Timeline || !Lampa.Timeline.listener) return;
        Lampa.Timeline.listener.follow('update', function (e) {
            if (!self.enabled()) return;
            self.add(e);
        });
        Lampa.Listener.follow('full', function (e) {
            if (e.type === 'complite' && self.enabled()) self.pullForCurrent();
        });
        Lampa.Listener.follow('lampac', function (e) {
            if (e.type === 'timecode_pullFromServer' && self.enabled()) self.pullForCurrent();
        });
    },
    cardID: function () {
        var act = Lampa.Storage.get('activity', '{}');
        var card = (act && (act.movie || act.card)) || { id: 0 };
        return (card.id || 0) + '_' + (card.name ? 'tv' : 'movie');
    },
    add: function (e) {
        if (switching) return;
        var id = e && e.data && e.data.hash;
        var payload = e && e.data && e.data.road;
        if (!id || !payload) return;
        var u = '/timecode/add?card_id=' + encodeURIComponent(this.cardID());
        // Server expects form-encoded id/data — keep classic behaviour.
        var form = 'id=' + encodeURIComponent(id) + '&data=' + encodeURIComponent(JSON.stringify(payload));
        var xhr = new XMLHttpRequest();
        xhr.open('POST', url(u), true);
        xhr.withCredentials = true;
        xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded');
        xhr.send(form);
    },
    // applyTimecodes — merge a {fileId: jsonString} map into file_view and
    // refresh Lampa's timeline cache. Shared by pullForCurrent and pullAll.
    applyTimecodes: function (json, replace) {
        if (!json) return;
        // Ask Lampa for the timeline storage key when it exposes it — its
        // filename() keys off Account.Permit.sync, while our old heuristic
        // keyed off account.profile presence. When the two disagreed we wrote
        // file_view_<id> and Timeline.read() kept loading plain file_view (or
        // vice versa) — data pulled but invisible without a full reload.
        var fname;
        try { if (Lampa.Timeline && typeof Lampa.Timeline.filename === 'function') fname = Lampa.Timeline.filename(); } catch (e) { /* older Lampa */ }
        if (!fname) {
            var account = Lampa.Storage.get('account', '{}');
            fname = 'file_view' + (account.profile ? '_' + account.profile.id : '');
        }
        var viewed = replace ? {} : Lampa.Storage.cache(fname, 10000, {});
        Object.keys(json).forEach(function (i) {
            try {
                var t = JSON.parse(json[i]);
                if (!t || typeof t !== 'object') return;
                viewed[i] = t;
                if (typeof viewed[i].duration === 'undefined') viewed[i].duration = 0;
                if (typeof viewed[i].time === 'undefined') viewed[i].time = 0;
                if (typeof viewed[i].percent === 'undefined') viewed[i].percent = 0;
                delete viewed[i].hash;
            } catch (e) { /* corrupt entry — ignore */ }
        });
        Lampa.Storage.set(fname, viewed, true);
        try { Lampa.Timeline.read(); } catch (e) { /* older Lampa */ }
    },
    pullForCurrent: function (done) {
        var self = this;
        var u = '/timecode/all?card_id=' + encodeURIComponent(this.cardID());
        httpJSON('GET', u, null, function (json) {
            self.applyTimecodes(json, false);
            if (done) done(!!json);
        }, function () {
            if (done) done(false);
        });
    },
    // pullAll — restore the ENTIRE profile's timeline (all cards) into
    // file_view. Used on a profile switch: the per-card pull can't repopulate
    // "continue watching" because no card is open, so history came back empty.
    // replace=true so a switch fully swaps the timeline rather than merging the
    // previous profile's leftovers.
    pullAll: function (done) {
        var self = this;
        httpJSON('GET', '/timecode/all?all=1', null, function (json) {
            self.applyTimecodes(json, true);
            if (done) done(!!json);
        }, function () {
            if (done) done(false);
        });
    },
};

// Generic localStorage-blob sync — for view history, torrents, search history,
// installed plugins. Each blob is one /storage/{set,get} path. We push on
// localStorage change events (Lampa.Storage.listener) and pull on app load.
function makeBlobSync(domainPref, storagePath, lsKeys) {
    return {
        enabled: function () { return pref(domainPref); },
        bound: false,
        debounce: 0,
        bind: function () {
            if (this.bound) return;
            this.bound = true;
            var self = this;
            if (Lampa.Storage.listener && Lampa.Storage.listener.follow) {
                Lampa.Storage.listener.follow('change', function (e) {
                    if (!self.enabled()) return;
                    if (lsKeys.indexOf(e.name) === -1) return;
                    self.scheduleFlush();
                });
            }
        },
        scheduleFlush: function () {
            var self = this;
            if (switching) return;
            if (self.debounce) clearTimeout(self.debounce);
            self.debounce = setTimeout(function () { self.flush(); }, 1500);
        },
        flush: function () {
            if (switching) return;
            // Bundle all lsKeys into one JSON object so we make one /storage
            // request per domain, not one per key.
            var bundle = {};
            lsKeys.forEach(function (k) {
                try {
                    var v = localStorage.getItem(k);
                    if (v !== null) bundle[k] = v;
                } catch (e) { /* ignore */ }
            });
            var body = JSON.stringify(bundle);
            // Use raw XHR; Lampa.Reguest can't send raw string body. Mirror
            // the X-Lampac-Profile-Session header from httpJSON so the
            // server can identify the user when Set-Cookie was eaten by
            // the Lampa Android WebView.
            var xhr = new XMLHttpRequest();
            xhr.open('POST', url('/storage/set?path=' + storagePath), true);
            xhr.withCredentials = true;
            var sessTok3 = getProfileSessionToken();
            if (sessTok3) xhr.setRequestHeader('X-Alpac-Profile-Session', sessTok3);
            var lampTok3 = getLampacToken();
            if (lampTok3) xhr.setRequestHeader('X-Alpac-Token', lampTok3);
            xhr.send(body);
        },
        pull: function (done) {
            var self = this;
            httpJSON('GET', '/storage/get?path=' + storagePath, null, function (j) {
                if (!j || !j.data) { if (done) done(!!j); return; }
                try {
                    var bundle = JSON.parse(j.data);
                    Object.keys(bundle).forEach(function (k) {
                        if (lsKeys.indexOf(k) === -1) return;
                        var raw = bundle[k];
                        if (typeof raw !== 'string') return;
                        // We persist values as the raw JSON-stringified
                        // strings that Lampa.Storage wrote into
                        // localStorage. To make Lampa SEE the pulled
                        // data, we must go through Storage.set so the
                        // in-memory `readed[]` cache is updated as well.
                        // localStorage.setItem alone leaves the cache
                        // stale and Storage.get keeps returning the old
                        // value (this was the "history doesn't sync" bug).
                        var parsed = raw;
                        try {
                            var c = raw.charAt(0);
                            if (c === '[' || c === '{') parsed = JSON.parse(raw);
                            else if (raw === 'true' || raw === 'false') parsed = (raw === 'true');
                        } catch (e) { /* not JSON — write as string */ }
                        try {
                            // nolisten=true — otherwise this triggers
                            // our own change-listener and starts a
                            // push-pull bounce loop.
                            if (Lampa && Lampa.Storage && typeof Lampa.Storage.set === 'function') {
                                Lampa.Storage.set(k, parsed, true);
                            } else {
                                localStorage.setItem(k, raw);
                            }
                        } catch (e) { /* quota or strange Lampa shape */ }
                    });
                } catch (e) { /* corrupt blob — ignore */ }
                if (done) done(true);
            }, function () {
                if (done) done(false);
            });
        },
    };
}

var ViewHistory = makeBlobSync('history', 'sync_view', [
    'online_view', 'online_last_balanser', 'online_watched_last',
    'recomends_list', 'recomends_list_history',
]);
var Torrents = makeBlobSync('torrents', 'sync_torrents', [
    'torrents_view', 'torrents_filter_data',
]);
var SearchHistory = makeBlobSync('search', 'search_history', [
    // Lampa's main app stores recent searches under these keys depending on
    // version. We sync both — whichever the current app uses, the other
    // is harmless dead data.
    'search_recent', 'search_history',
]);
var PluginsList = makeBlobSync('plugins', 'sync_plugins', [
    'plugins',
]);

// Full backup: dump/restore the entire localStorage. Manual, not on a timer.
var FullBackup = {
    save: function (onDone, onFail) {
        var dump = {};
        try {
            for (var i = 0; i < localStorage.length; i++) {
                var k = localStorage.key(i);
                if (!k) continue;
                // Skip our own session cookie shadows and ephemeral keys.
                if (k.indexOf('lampac_psync_') === 0) continue;
                dump[k] = localStorage.getItem(k);
            }
        } catch (e) { /* ignore */ }
        var body = JSON.stringify(dump);
        var xhr = new XMLHttpRequest();
        xhr.open('POST', url('/storage/set?path=backup'), true);
        xhr.withCredentials = true;
        // Same header dance as httpJSON: Android WebView eats Set-Cookie
        // on XHR responses, so storage requests need the profile-session
        // token as an explicit header for the user identity to follow
        // through to resolveStoragePath.
        var sessTok2 = getProfileSessionToken();
        if (sessTok2) xhr.setRequestHeader('X-Alpac-Profile-Session', sessTok2);
        var lampTok2 = getLampacToken();
        if (lampTok2) xhr.setRequestHeader('X-Alpac-Token', lampTok2);
        // No Content-Type — storage handler reads body as raw bytes and a
        // JSON content-type would mislead intermediaries (some hosts
        // intercept JSON bodies for inspection and choke on long dumps).
        xhr.onreadystatechange = function () {
            if (xhr.readyState !== 4) return;
            // Storage handler returns 200 even on logical errors (with
            // {"success":false, "msg":"<slug>"}). Distinguish the cases so
            // the toast can show *what* went wrong instead of the
            // un-actionable "Error (200)".
            var slug = '';
            var ok = false;
            try {
                var j = JSON.parse(xhr.responseText || '{}');
                ok = !!j.success;
                if (!ok) slug = j.msg || j.error || '';
            } catch (e) { /* response not JSON */ }
            if (ok) { if (onDone) onDone(); return; }
            // 413 Payload Too Large doesn't reach our handler — it's
            // emitted by the reverse proxy (nginx default
            // client_max_body_size is 1m). Surface a *specific* slug so
            // the toast can tell the admin to bump the limit instead of
            // showing "Error (413)".
            if (xhr.status === 413) {
                if (onFail) onFail('too_large_proxy');
                return;
            }
            if (onFail) {
                // Report the storage-layer reason if we have one; otherwise
                // fall back to the HTTP status. xhr.status==0 ⇒ network
                // error (offline, CORS preflight rejected, …).
                onFail(slug || xhr.status || 'network');
            }
        };
        xhr.send(body);
    },
    restore: function (onDone, onFail) {
        httpJSON('GET', '/storage/get?path=backup', null, function (j) {
            if (!j || !j.data) { if (onFail) onFail('nodata'); return; }
            try {
                var data = JSON.parse(j.data);
                Object.keys(data).forEach(function (k) {
                    try { localStorage.setItem(k, data[k]); } catch (e) { /* quota */ }
                });
                if (onDone) onDone();
            } catch (e) {
                if (onFail) onFail('parse');
            }
        }, function (_, status) { if (onFail) onFail(status); });
    },
};

// ----------------------------------------------------------------------
//  WS bridge — listen to lwsEvent for cross-device pushes
// ----------------------------------------------------------------------

// wsProfileMismatch — true when an incoming event belongs to a different
// sub-profile than the one active on this device. Events from older servers
// (no profile_id field) always apply, matching pre-profile behavior.
function wsProfileMismatch(payload) {
    if (!payload || typeof payload.profile_id === 'undefined') return false;
    return String(payload.profile_id || '') !== String(Lampa.Storage.get('lampac_profile_id', '') || '');
}

function bindWS() {
    document.addEventListener('lwsEvent', function (ev) {
        if (!ev || !ev.detail) return;
        if (switching) return; // mid-switch events may target the OLD profile
        var name = ev.detail.name;
        var data = ev.detail.data;

        if (name === 'bookmark' && Bookmarks.enabled()) {
            try {
                var ob = JSON.parse(data);
                if (wsProfileMismatch(ob)) return;
                if (ob.type === 'set' && ob.data) {
                    Bookmarks.applyServerSet(ob.data);
                } else if ((ob.type === 'add' || ob.type === 'remove') && Lampa.Favorite) {
                    var rows = Array.isArray(ob.data) ? ob.data : [ob.data];
                    rows.forEach(function (item) {
                        if (item && item.card) {
                            item.card.received = true;
                            Lampa.Favorite[ob.type](item.where, item.card);
                        }
                    });
                }
            } catch (e) { /* malformed payload — ignore */ }
        } else if (name === 'timecode' && Timecodes.enabled()) {
            // Push the new timecode into the local cache without forcing a
            // full /timecode/all roundtrip — that would amplify NWS traffic.
            try {
                var t = JSON.parse(data);
                if (!t || !t.card_id || !t.id) return;
                if (wsProfileMismatch(t)) return;
                var account = Lampa.Storage.get('account', '{}');
                var fname = 'file_view' + (account.profile ? '_' + account.profile.id : '');
                var viewed = Lampa.Storage.cache(fname, 10000, {});
                try {
                    viewed[t.id] = JSON.parse(t.data);
                    Lampa.Storage.set(fname, viewed, true);
                } catch (e) { /* invalid timecode payload */ }
            } catch (e) { /* ignore */ }
        }
    });
}

// ----------------------------------------------------------------------
//  Profile state — kept in module memory so the settings UI reflects login
// ----------------------------------------------------------------------

var profileState = { authenticated: false, auth_method: 'none', username: '' };

// Reference to the rendered status row so callbacks (login, logout,
// switch-profile, …) can repaint its description text without waiting
// for the settings screen to be reopened. We capture it in onRender of
// the status param in buildSettings.
var profileStatusItem = null;

function repaintProfileStatus() {
    if (!profileStatusItem) return;
    try {
        profileStatusItem.find('.settings-param__descr').text(profileStatusLine());
    } catch (e) { /* DOM shape changed in a Lampa update — ignore */ }
}

function refreshProfileState(cb) {
    Profile.me(function (resp) {
        profileState.authenticated = !!(resp && resp.authenticated);
        profileState.auth_method = (resp && resp.auth_method) || 'none';
        profileState.username = (resp && resp.username) || '';
        repaintProfileStatus();
        if (cb) cb(profileState);
    });
}

function profileStatusLine() {
    var base;
    if (!profileState.authenticated) base = Lampa.Lang.translate('syncpro_profile_status_anon');
    else if (profileState.auth_method === 'telegram') base = Lampa.Lang.translate('syncpro_profile_status_tg');
    else if (profileState.auth_method === 'profile')
        base = Lampa.Lang.translate('syncpro_profile_status_prof').replace('{name}', profileState.username || '?');
    else base = Lampa.Lang.translate('syncpro_profile_status_anon');

    // Append the *active* sub-profile name when one is selected. This is
    // the per-device `lampac_profile_id` — distinct from the auth identity.
    // Without surfacing it, two devices belonging to the same TG user can
    // silently drift if they happen to have different profiles selected
    // (a recurring "sync isn't working" report). The friendly name is
    // cached in lampac_profile_name when the user picks via the switch
    // sheet; we fall back to the raw ID otherwise.
    try {
        var pid = Lampa.Storage.get('lampac_profile_id', '');
        if (pid) {
            var pname = Lampa.Storage.get('lampac_profile_name', '') || pid;
            base += ' • ' + Lampa.Lang.translate('syncpro_active_profile').replace('{name}', pname);
        }
    } catch (e) { /* Storage not ready */ }
    return base;
}

// ----------------------------------------------------------------------
//  Login / register / sync-code dialogs
// ----------------------------------------------------------------------

function inputForm(title, fields, onSubmit) {
    // Two-field input via Lampa.Activity 'category_input' is overkill. Use a
    // sequence of single-field Lampa.Input prompts — that's how Lampa's own
    // CUB sign-in handles it (see app.min.js account_profile_name flow).
    var values = {};
    var idx = 0;
    function next() {
        if (idx >= fields.length) {
            onSubmit(values);
            return;
        }
        var f = fields[idx++];
        // Compose the prompt label as "Action / Field". Lampa shows `title`
        // above the on-screen keyboard, so without it the user sees an
        // empty input and has no way to know what's being asked for
        // (which was the "Появляются текстовые поля, которые не подписаны"
        // bug report).
        var promptTitle = title ? (title + ' — ' + f.label) : f.label;
        Lampa.Input.edit({
            title: promptTitle,
            value: '',
            free: true,
            nosave: true,
            nomic: true,
        }, function (v) {
            values[f.key] = v || '';
            next();
        });
    }
    next();
}

function doRegister() {
    inputForm(Lampa.Lang.translate('syncpro_profile_register'), [
        { key: 'username', label: Lampa.Lang.translate('syncpro_field_username') },
        { key: 'password', label: Lampa.Lang.translate('syncpro_field_password') },
    ], function (vals) {
        if (!vals.username || !vals.password) return;
        Lampa.Loading.start();
        Profile.register(vals.username, vals.password,
            function (resp) {
                Lampa.Loading.stop();
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_register_ok'));
                refreshProfileState();
                pullAll();
            },
            function (resp, status) {
                Lampa.Loading.stop();
                Lampa.Noty.show(errorMessageFromSlug(resp && resp.error, status));
            }
        );
    });
}

function doLogin() {
    inputForm(Lampa.Lang.translate('syncpro_profile_login'), [
        { key: 'username', label: Lampa.Lang.translate('syncpro_field_username') },
        { key: 'password', label: Lampa.Lang.translate('syncpro_field_password') },
    ], function (vals) {
        if (!vals.username || !vals.password) return;
        Lampa.Loading.start();
        Profile.login(vals.username, vals.password,
            function (resp) {
                Lampa.Loading.stop();
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_login_ok'));
                refreshProfileState();
                pullAll();
            },
            function (resp, status) {
                Lampa.Loading.stop();
                Lampa.Noty.show(errorMessageFromSlug(resp && resp.error, status));
            }
        );
    });
}

function doLogout() {
    Lampa.Loading.start();
    Profile.logout(function () {
        Lampa.Loading.stop();
        // Drop the cached session token regardless of whether the server
        // call succeeded — locally we want to be signed out either way.
        setProfileSessionToken('');
        Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_logout_ok'));
        refreshProfileState();
    });
}

function doChangePassword() {
    inputForm(Lampa.Lang.translate('syncpro_profile_chgpass'), [
        { key: 'old', label: Lampa.Lang.translate('syncpro_field_password_old') },
        { key: 'new', label: Lampa.Lang.translate('syncpro_field_password_new') },
    ], function (vals) {
        if (!vals.old || !vals.new) return;
        Lampa.Loading.start();
        Profile.changePassword(vals.old, vals.new,
            function () {
                Lampa.Loading.stop();
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_chgpass_ok'));
            },
            function (resp, status) {
                Lampa.Loading.stop();
                Lampa.Noty.show(errorMessageFromSlug(resp && resp.error, status));
            }
        );
    });
}

function doIssueSyncCode() {
    Lampa.Loading.start();
    Profile.issueCode(
        function (resp) {
            Lampa.Loading.stop();
            var msg = Lampa.Lang.translate('syncpro_msg_sync_code').replace('{code}', resp.code);
            Lampa.Noty.show(msg, 15000);
        },
        function (resp, status) {
            Lampa.Loading.stop();
            Lampa.Noty.show(errorMessageFromSlug(resp && resp.error, status));
        }
    );
}

// PIN login entrypoint — the user types their profile name and the PIN that
// was assigned in the TG bot (/profiles → Set PIN). Server returns the same
// session cookie shape as register/login. Designed for TV remotes: PIN is
// digits-only and we don't ask for the password.
function doPinLogin() {
    inputForm(Lampa.Lang.translate('syncpro_profile_pin_login'), [
        { key: 'username', label: Lampa.Lang.translate('syncpro_field_username') },
        { key: 'pin',      label: Lampa.Lang.translate('syncpro_field_pin') },
    ], function (vals) {
        if (!vals.username || !vals.pin) return;
        Lampa.Loading.start();
        Profile.pinLogin(vals.username, vals.pin,
            function () {
                Lampa.Loading.stop();
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_login_ok'));
                refreshProfileState();
                pullAll();
            },
            function (resp, status) {
                Lampa.Loading.stop();
                Lampa.Noty.show(errorMessageFromSlug(resp && resp.error, status));
            }
        );
    });
}

function doRedeemSyncCode() {
    inputForm(Lampa.Lang.translate('syncpro_profile_sync_redeem'), [
        { key: 'code', label: Lampa.Lang.translate('syncpro_field_code') },
    ], function (vals) {
        if (!vals.code) return;
        Lampa.Loading.start();
        Profile.redeemCode(vals.code,
            function () {
                Lampa.Loading.stop();
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_login_ok'));
                refreshProfileState();
                pullAll();
            },
            function (resp, status) {
                Lampa.Loading.stop();
                Lampa.Noty.show(errorMessageFromSlug(resp && resp.error, status));
            }
        );
    });
}

// ----------------------------------------------------------------------
//  Pull everything that's enabled — used after login / on app load / on
//  manual "Pull now" action.
// ----------------------------------------------------------------------

function pullAll() {
    pullAllTracked(null);
}

// pullAllTracked — pull every enabled domain and report completion. Unlike
// levende's profiles.js, which POLLS sync-timestamp keys set by XHR hooks, we
// own every pull so each one reports back directly; `done(okCount, total)`
// fires when all finish or after a 10s safety timeout (finished=−1 marks the
// timeout path so late stragglers don't double-fire).
function pullAllTracked(done) {
    var pulls = [];
    if (Bookmarks.enabled()) pulls.push(function (cb) { Bookmarks.pull(cb); });
    // pullAll (not pullForCurrent): rebuild the whole timeline. On a profile
    // switch (and initial load) no card is open, so the per-card pull restored
    // nothing — "continue watching" came back empty.
    if (Timecodes.enabled()) pulls.push(function (cb) { Timecodes.pullAll(cb); });
    if (ViewHistory.enabled()) pulls.push(function (cb) { ViewHistory.pull(cb); });
    if (Torrents.enabled()) pulls.push(function (cb) { Torrents.pull(cb); });
    if (SearchHistory.enabled()) pulls.push(function (cb) { SearchHistory.pull(cb); });
    if (PluginsList.enabled()) pulls.push(function (cb) { PluginsList.pull(cb); });

    if (!done) {
        pulls.forEach(function (p) { p(null); });
        return;
    }
    if (!pulls.length) { done(0, 0); return; }

    var finished = 0, okCount = 0;
    var timer = setTimeout(function () {
        if (finished >= 0) {
            finished = -1;
            done(okCount, pulls.length); // timeout — report with what we have
        }
    }, 10000);
    pulls.forEach(function (p) {
        p(function (ok) {
            if (finished < 0) return; // timeout already reported
            if (ok) okCount++;
            finished++;
            if (finished === pulls.length) {
                clearTimeout(timer);
                var f2 = finished;
                finished = -1;
                done(okCount, f2);
            }
        });
    });
}

// ----------------------------------------------------------------------
//  Profile switching — the levende-grade chain:
//  PIN gate → write gate → local backup → reset → pull → refresh.
// ----------------------------------------------------------------------

// Local keys that hold per-profile state. Deliberately excludes 'plugins'
// (wiping it would uninstall every plugin and force a reload loop) and
// settings/prefs (those are device-level, not profile-level).
var SYNC_KEYS = [
    'favorite', 'file_view',
    'online_view', 'online_last_balanser', 'online_watched_last',
    'recomends_list', 'recomends_list_history',
    'torrents_view', 'torrents_filter_data',
    'search_recent', 'search_history',
];

function backupKeyFor(pid, key) {
    return 'syncpro_pb_' + (pid || 'default') + '_' + key;
}

// backupLocal — snapshot the CURRENT profile's local state before wiping it.
// This is the offline fallback: when the server is unreachable during a
// switch, restoreLocal brings back whatever this device last saw for the
// target profile (levende's "offline profiles" behavior).
function backupLocal(pid) {
    SYNC_KEYS.forEach(function (k) {
        try {
            var v = localStorage.getItem(k);
            if (v !== null) localStorage.setItem(backupKeyFor(pid, k), v);
        } catch (e) { /* quota */ }
    });
}

function restoreLocal(pid) {
    var found = false;
    SYNC_KEYS.forEach(function (k) {
        try {
            var v = localStorage.getItem(backupKeyFor(pid, k));
            if (v === null) return;
            found = true;
            var parsed = v;
            try {
                var c = v.charAt(0);
                if (c === '[' || c === '{') parsed = JSON.parse(v);
            } catch (e) { /* keep string */ }
            Lampa.Storage.set(k, parsed, true);
        } catch (e) { /* ignore */ }
    });
    return found;
}

// resetLocalData — wipe the previous profile's traces so they can't bleed
// into the new one. Goes through Lampa.Storage.set (not bare localStorage)
// so Lampa's in-memory `readed` cache is invalidated too; empty values match
// each key's worker type (array vs object).
function resetLocalData() {
    var empties = {
        favorite: {}, file_view: {},
        online_view: [], online_last_balanser: {}, online_watched_last: {},
        recomends_list: [], recomends_list_history: [],
        torrents_view: [], torrents_filter_data: {},
        search_recent: [], search_history: [],
    };
    SYNC_KEYS.forEach(function (k) {
        try { Lampa.Storage.set(k, empties.hasOwnProperty(k) ? empties[k] : '', true); } catch (e) { /* ignore */ }
    });
    try { Lampa.Timeline.read(); } catch (e) { /* older Lampa */ }
    try { Lampa.Favorite.read(true); } catch (e) { /* older Lampa */ }
}

// finishSwitch — make the UI show the new profile without a restart: re-read
// Timeline/Favorite and soft-replace the current activity (levende's "мягкое
// обновление"). The full-reload path is behind a settings toggle for devices
// whose views don't survive a soft refresh.
function finishSwitch(profile) {
    if (pref('switch_reload', false)) {
        window.location.reload();
        return;
    }
    // No-arg read() → the 'state:changed' events FIRE, so listeners (the
    // continue-watching content row, bookmark counters, …) repaint themselves
    // even where Activity.replace below doesn't reach.
    try { Lampa.Timeline.read(); } catch (e) { /* ignore */ }
    try { Lampa.Favorite.read(); } catch (e) { /* ignore */ }
    try {
        var act = Lampa.Activity.active();
        if (act) {
            if (act.page) act.page = 1;
            Lampa.Activity.replace(act);
        }
    } catch (e) { /* soft refresh is best-effort */ }
    repaintProfileStatus();
    renderProfileHeaderButton();
    Lampa.Noty.show(Lampa.Lang.translate('syncpro_switched').replace('{name}', profile.name || Lampa.Lang.translate('syncpro_profile_default')));
}

// switchToProfile — the full chain. `profile` is {id,name,avatar,hasPin,...};
// id '' means the shared/legacy bucket («Общий»).
function switchToProfile(profile, pinChecked) {
    var prevId = String(Lampa.Storage.get('lampac_profile_id', '') || '');
    var newId = String(profile.id || '');
    if (prevId === newId) return;

    if (profile.hasPin && !pinChecked) {
        Lampa.Input.edit({
            title: Lampa.Lang.translate('syncpro_enter_pin').replace('{name}', profile.name || ''),
            value: '', free: true, nosave: true, nomic: true,
        }, function (pin) {
            if (!pin) return;
            SubProfiles.verify(profile.id, pin, function (resp) {
                if (resp && resp.ok) switchToProfile(profile, true);
                else Lampa.Noty.show(Lampa.Lang.translate('syncpro_wrong_pin'));
            }, function () {
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_err_network'));
            });
        });
        return;
    }

    // Remember that this locked profile passed its PIN this session, so the
    // boot gate (hydrateActiveProfile) doesn't re-prompt on the next launch's
    // hydrate within the same session.
    if (profile.hasPin && newId) _pinVerifiedSession[newId] = true;

    switching = true;
    Lampa.Loading.start();
    Lampa.Noty.show(Lampa.Lang.translate('syncpro_switching'));

    backupLocal(prevId);
    resetLocalData();
    Lampa.Storage.set('lampac_profile_id', newId);
    Lampa.Storage.set('lampac_profile_name', newId ? (profile.name || '') : '');
    Lampa.Storage.set('lampac_profile_avatar', newId ? (profile.avatar || '') : '');

    pullAllTracked(function (okCount, total) {
        switching = false;
        if (total > 0 && okCount === 0) {
            // Server unreachable — fall back to this device's last local
            // snapshot of the target profile, if any.
            if (restoreLocal(newId)) {
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_switch_offline'));
            }
        }
        Lampa.Loading.stop();
        finishSwitch(profile);
    });
}

// ----------------------------------------------------------------------
//  Settings UI
// ----------------------------------------------------------------------

var SVG_ICON = '<svg width="64" height="64" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-9-9 9 9 0 0 1 9-9c2.7 0 5.1 1.2 6.8 3" stroke="white" stroke-width="2" fill="none" stroke-linecap="round"/><polyline points="21 4 21 9 16 9" stroke="white" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>';

function addToggle(domain, label) {
    Lampa.SettingsApi.addParam({
        component: 'syncpro',
        param: { name: 'syncpro_' + domain, type: 'trigger', default: true },
        field: { name: label, description: '' },
        onChange: function () {
            // No-op — readers consult pref() at event time.
        },
    });
}

function addButton(name, onClick) {
    Lampa.SettingsApi.addParam({
        component: 'syncpro',
        param: { type: 'button' },
        field: { name: name },
        onChange: onClick,
    });
}

// _sheetBackController remembers which controller was active when a sheet was
// first opened, so Back returns focus there. Hardcoding 'settings_component'
// dead-ended the D-pad on Android TV when a sheet was opened from the HEADER
// avatar button (no settings_component controller exists on the home screen →
// toggling to it left no controller focused → the whole UI froze). Touch
// devices don't rely on controller focus, which is why mobile "worked".
var _sheetBackController = 'content';
// Real, focusable screen controllers a sheet can safely return Back to.
// Transient ones (Select's own 'select', parental/modal dialogs) must NOT be
// remembered as the origin — toggling back to them dead-ends the D-pad.
var SCREEN_CONTROLLERS = { head: 1, menu: 1, content: 1, settings_component: 1 };

// Lampa setting sheets — open a contextual Lampa.Select. Action handlers run
// on item.onSelect; the sheet auto-closes. onBack returns control to the
// remembered origin controller so the focus chain never dead-ends.
function openSheet(title, items, onBackOverride) {
    // Update the remembered origin only from a real screen controller — nested
    // sheets and async continuations (enabled='select'/modal) keep the value
    // captured when we first left a screen.
    var enabled = (Lampa.Controller.enabled() || {}).name;
    if (enabled && SCREEN_CONTROLLERS[enabled]) _sheetBackController = enabled;
    var back = _sheetBackController || 'content';
    Lampa.Select.show({
        title: title,
        items: items,
        onBack: function () {
            if (typeof onBackOverride === 'function') { onBackOverride(); return; }
            Lampa.Controller.toggle(back);
        },
        onSelect: function (a) {
            if (a && typeof a.action === 'function') a.action();
        },
    });
}

// Profile management sheet — contextual on auth state.
// - anon    → register / login / PIN / redeem code
// - tg      → friendly note "TG users don't need this"; the only real
//             action is logout-of-TG which is owned by the lampac TG plugin
// - profile → issue code / change password / logout
// currentActiveProfileLabel — short description of which Lampa profile is
// active on THIS device. The active profile_id is stored as a plain string
// in Lampa.Storage; the label shown to the user is cached in a parallel
// key so we don't have to round-trip /api/profile/owned just to render the
// "active: X" subtitle. The cache is updated whenever the user picks an
// item in openSwitchProfileSheet.
function currentActiveProfileLabel() {
    var pid = String(Lampa.Storage.get('lampac_profile_id', '') || '');
    if (!pid) return Lampa.Lang.translate('syncpro_active_default');
    var name = String(Lampa.Storage.get('lampac_profile_name', '') || pid);
    return Lampa.Lang.translate('syncpro_active_profile').replace('{name}', name);
}

// avatarIsImage — the avatar field holds either an emoji or an image URL
// (levende-style photo avatars). '{host}'-relative and absolute URLs both
// count; anything else is rendered as text.
function avatarIsImage(av) {
    if (!av) return false;
    return av.indexOf('http://') === 0 || av.indexOf('https://') === 0 || av.charAt(0) === '/';
}

function avatarImageURL(av) {
    return av.charAt(0) === '/' ? HOST + av : av;
}

// profileGlyph — short TEXT marker for a profile row: custom emoji avatar,
// kids marker, or the name's first letter (image avatars fall back to the
// letter in text-only contexts like the picker rows).
function profileGlyph(p) {
    if (p.avatar && !avatarIsImage(p.avatar)) return p.avatar;
    if (p.kids) return '🧸';
    return (p.name || '?').charAt(0).toUpperCase();
}

// openSwitchProfileSheet — the unified Netflix-style picker. Lists the
// account's sub-profiles from /api/profiles (the SAME set alpac clients see),
// plus «Общий» (empty profile_id = the legacy shared bucket every existing
// install is on today) and create/manage entries. Falls back to the legacy
// TG-owned list on servers without the unified API.
function openSwitchProfileSheet() {
    SubProfiles.list(function (resp) {
        if (!resp || !resp.profiles) { openLegacyOwnedSheet(); return; }
        var activeId = String(Lampa.Storage.get('lampac_profile_id', '') || '');
        var items = [];

        items.push({
            title: (activeId === '' ? '✓ ' : '  ') + Lampa.Lang.translate('syncpro_profile_default'),
            action: function () {
                switchToProfile({ id: '', name: '' });
            },
        });

        resp.profiles.forEach(function (p) {
            var on = String(p.id) === activeId;
            items.push({
                title: (on ? '✓ ' : '  ') + profileGlyph(p) + ' ' + p.name +
                    (p.hasPin ? ' 🔒' : '') + (p.kids ? ' 👶' : ''),
                action: function () {
                    switchToProfile(p);
                },
            });
        });

        if (resp.profiles.length < 5) {
            items.push({
                title: '➕ ' + Lampa.Lang.translate('syncpro_profile_create'),
                action: doCreateProfile,
            });
        }
        items.push({
            title: '🏷 ' + Lampa.Lang.translate('syncpro_rename_balancers'),
            action: openBalancerRenameSheet,
        });
        items.push({
            title: '⚙️ ' + Lampa.Lang.translate('syncpro_profile_manage'),
            action: function () { openManageProfilesSheet(resp.profiles); },
        });

        openSheet(Lampa.Lang.translate('syncpro_profiles_title'), items);
    }, function (resp, status) {
        if (status === 401) Lampa.Noty.show(Lampa.Lang.translate('syncpro_need_auth'));
        else openLegacyOwnedSheet();
    });
}

// openProfilePickerGated — entry point used by the header button and the
// settings row. Honors Lampa's personal parental-control scope the same way
// levende's plugin does: when 'account_profiles' is protected, the PIN
// dialog must pass before the picker opens.
var _pickerBusyUntil = 0;
function openProfilePickerGated() {
    // Debounce: the native profile button can fire this through TWO paths at
    // once (delegated hover:enter on .open--profile AND AGNative's overridden
    // Account.showProfiles fallback ~80ms later). The picker's first step is an
    // async /api/profiles fetch, so selectbox--open isn't set yet when the
    // second call lands — without this guard the sheet opens twice.
    var now = Date.now();
    if (now < _pickerBusyUntil) return;
    _pickerBusyUntil = now + 1500;
    if ($('body').hasClass('selectbox--open')) return;

    // Capture the origin controller NOW (before any async /api/profiles call or
    // parental PIN dialog toggles it away) so the sheet's Back returns focus
    // to the header / settings row it was opened from — not a dead controller.
    var controllerName = (Lampa.Controller.enabled() || {}).name;
    if (controllerName && SCREEN_CONTROLLERS[controllerName]) _sheetBackController = controllerName;

    var scopes = Lampa.Storage.get('parental_control_personal', []);
    var gated = scopes && scopes.indexOf && scopes.indexOf('account_profiles') !== -1;
    if (gated && Lampa.ParentalControl && typeof Lampa.ParentalControl.query === 'function') {
        Lampa.ParentalControl.query(openSwitchProfileSheet, function () {
            if (controllerName) Lampa.Controller.toggle(controllerName);
        });
    } else {
        openSwitchProfileSheet();
    }
}

function doCreateProfile() {
    inputForm(Lampa.Lang.translate('syncpro_profile_create'), [
        { key: 'name', label: Lampa.Lang.translate('syncpro_field_profile_name') },
    ], function (vals) {
        if (!vals.name) return;
        SubProfiles.create({ name: vals.name }, function (resp) {
            if (resp && resp.error === 'profile_limit') {
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_profile_limit'));
                return;
            }
            Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_done'));
            openSwitchProfileSheet();
        }, function (resp, status) {
            Lampa.Noty.show(errorMessageFromSlug(resp && resp.error, status));
        });
    });
}

function openManageProfilesSheet(profiles) {
    var items = profiles.map(function (p) {
        return {
            title: profileGlyph(p) + ' ' + p.name + (p.hasPin ? ' 🔒' : ''),
            action: function () { openManageOneProfileSheet(p); },
        };
    });
    openSheet(Lampa.Lang.translate('syncpro_profile_manage'), items);
}

// ----------------------------------------------------------------------
//  Per-profile balancer renaming
//  online.js records every balancer it displays into 'lampac_balansers_seen'
//  ({key: defaultName}) and reads the rename map 'lampac_balanser_names'
//  ({profile_id: {key: customName}}) when building the source list. This UI
//  edits the ACTIVE profile's submap — renames are personal to the profile.
// ----------------------------------------------------------------------

function balancerRenameMap() {
    var all = Lampa.Storage.get('lampac_balanser_names', {});
    var pid = String(Lampa.Storage.get('lampac_profile_id', '') || '');
    return { all: all, pid: pid, map: all[pid] || {} };
}

function openBalancerRenameSheet() {
    var seen = Lampa.Storage.get('lampac_balansers_seen', {});
    var keys = Object.keys(seen).sort();
    var ctx = balancerRenameMap();

    if (!keys.length) {
        Lampa.Noty.show(Lampa.Lang.translate('syncpro_balancers_empty'));
        Lampa.Controller.toggle(_sheetBackController || 'content');
        return;
    }

    var items = keys.map(function (key) {
        var custom = ctx.map[key];
        var shown = (custom && String(custom).trim()) ? custom : seen[key];
        var badge = (custom && String(custom).trim()) ? '  ✎' : '';
        return {
            title: shown + badge,
            subtitle: custom ? (Lampa.Lang.translate('syncpro_balancer_original') + ': ' + seen[key]) : key,
            key: key,
            def: seen[key],
        };
    });
    // Offer a reset-all when any override exists.
    if (Object.keys(ctx.map).length) {
        items.push({ title: '↺ ' + Lampa.Lang.translate('syncpro_balancers_reset'), reset: true });
    }

    openSheet(Lampa.Lang.translate('syncpro_rename_balancers'), items.map(function (it) {
        return {
            title: it.title,
            subtitle: it.subtitle,
            action: function () {
                if (it.reset) {
                    var c = balancerRenameMap();
                    delete c.all[c.pid];
                    Lampa.Storage.set('lampac_balanser_names', c.all);
                    Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_done'));
                    openBalancerRenameSheet();
                    return;
                }
                Lampa.Input.edit({
                    title: it.def, value: (it.key && balancerRenameMap().map[it.key]) || it.def,
                    free: true, nosave: true,
                }, function (val) {
                    var c = balancerRenameMap();
                    val = (val || '').trim();
                    if (!c.all[c.pid]) c.all[c.pid] = {};
                    if (!val || val === it.def) delete c.all[c.pid][it.key]; // back to default
                    else c.all[c.pid][it.key] = val;
                    if (!Object.keys(c.all[c.pid]).length) delete c.all[c.pid];
                    Lampa.Storage.set('lampac_balanser_names', c.all);
                    openBalancerRenameSheet();
                });
            },
        };
    }));
}

function openManageOneProfileSheet(p) {
    var refresh = function () { openSwitchProfileSheet(); };
    var fail = function (resp, status) { Lampa.Noty.show(errorMessageFromSlug(resp && resp.error, status)); };
    var items = [
        {
            title: Lampa.Lang.translate('syncpro_profile_rename'),
            action: function () {
                inputForm(Lampa.Lang.translate('syncpro_profile_rename'), [
                    { key: 'name', label: Lampa.Lang.translate('syncpro_field_profile_name') },
                ], function (vals) {
                    if (!vals.name) return;
                    SubProfiles.update({ id: p.id, name: vals.name, color: p.color, avatar: p.avatar || '', kids: !!p.kids }, refresh, fail);
                });
            },
        },
        {
            title: Lampa.Lang.translate('syncpro_profile_avatar'),
            action: function () {
                inputForm(Lampa.Lang.translate('syncpro_profile_avatar'), [
                    { key: 'avatar', label: Lampa.Lang.translate('syncpro_profile_avatar') },
                ], function (vals) {
                    SubProfiles.update({ id: p.id, name: p.name, color: p.color, avatar: vals.avatar || '', kids: !!p.kids }, refresh, fail);
                });
            },
        },
        {
            title: Lampa.Lang.translate(p.kids ? 'syncpro_profile_kids_off' : 'syncpro_profile_kids_on'),
            action: function () {
                SubProfiles.update({ id: p.id, name: p.name, color: p.color, avatar: p.avatar || '', kids: !p.kids }, refresh, fail);
            },
        },
        {
            title: Lampa.Lang.translate(p.hasPin ? 'syncpro_profile_clear_pin' : 'syncpro_profile_set_pin'),
            action: function () {
                if (p.hasPin) {
                    SubProfiles.update({ id: p.id, name: p.name, color: p.color, avatar: p.avatar || '', kids: !!p.kids, pin: '' }, refresh, fail);
                    return;
                }
                inputForm(Lampa.Lang.translate('syncpro_profile_set_pin'), [
                    { key: 'pin', label: Lampa.Lang.translate('syncpro_field_pin') },
                ], function (vals) {
                    if (!vals.pin) return;
                    SubProfiles.update({ id: p.id, name: p.name, color: p.color, avatar: p.avatar || '', kids: !!p.kids, pin: vals.pin }, refresh, fail);
                });
            },
        },
        {
            title: '🗑 ' + Lampa.Lang.translate('syncpro_profile_delete'),
            action: function () {
                Lampa.Select.show({
                    title: Lampa.Lang.translate('sure'),
                    nomark: true,
                    items: [
                        { title: Lampa.Lang.translate('confirm'), confirm: true },
                        { title: Lampa.Lang.translate('cancel'), selected: true },
                    ],
                    onSelect: function (a) {
                        if (!a.confirm) { openManageOneProfileSheet(p); return; }
                        SubProfiles.remove(p.id, function () {
                            // If the deleted profile was active locally,
                            // drop back to the shared bucket.
                            if (String(Lampa.Storage.get('lampac_profile_id', '')) === String(p.id)) {
                                Lampa.Storage.set('lampac_profile_id', '');
                                Lampa.Storage.set('lampac_profile_name', '');
                                repaintProfileStatus();
                                renderProfileHeaderButton();
                            }
                            refresh();
                        }, fail);
                    },
                    onBack: function () { openManageOneProfileSheet(p); },
                });
            },
        },
    ];
    openSheet(profileGlyph(p) + ' ' + p.name, items);
}

// openLegacyOwnedSheet — pre-unified-API behavior (owned login-profiles from
// the TG bot). Kept so the plugin still works against an older server build.
function openLegacyOwnedSheet() {
    Lampa.Noty.show(Lampa.Lang.translate('syncpro_profile_loading'));
    Profile.listOwned(function (resp) {
        var owned = (resp && resp.profiles) || [];
        var activeId = String(Lampa.Storage.get('lampac_profile_id', '') || '');

        var items = [];
        items.push({
            title: (activeId === '' ? '✓ ' : '  ') + Lampa.Lang.translate('syncpro_profile_default'),
            action: function () {
                Lampa.Storage.set('lampac_profile_id', '');
                Lampa.Storage.set('lampac_profile_name', '');
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_switched').replace('{name}', Lampa.Lang.translate('syncpro_profile_default')));
                pullAll();
            },
        });
        if (!owned.length) {
            items.push({ title: Lampa.Lang.translate('syncpro_profile_none_owned') });
        } else {
            owned.forEach(function (sp) {
                var on = sp.id === activeId;
                items.push({
                    title: (on ? '✓ ' : '  ') + sp.username + (sp.has_pin ? ' 🔑' : ''),
                    action: function () {
                        Lampa.Storage.set('lampac_profile_id', sp.id);
                        Lampa.Storage.set('lampac_profile_name', sp.username);
                        Lampa.Noty.show(Lampa.Lang.translate('syncpro_switched').replace('{name}', sp.username));
                        pullAll();
                    },
                });
            });
        }

        openSheet(Lampa.Lang.translate('syncpro_switch_profile'), items);
    }, function () {
        Lampa.Noty.show(Lampa.Lang.translate('syncpro_err_generic').replace('{code}', '401'));
    });
}

// ----------------------------------------------------------------------
//  Header avatar button — one-tap profile switching from anywhere, like
//  Netflix/levende. Rendered after the profile list is first fetched and
//  re-rendered on every switch.
// ----------------------------------------------------------------------

// profileAvatarInner — the round avatar (photo / emoji / letter) shown on a
// header profile button.
function profileAvatarInner() {
    var pid = String(Lampa.Storage.get('lampac_profile_id', '') || '');
    var name = String(Lampa.Storage.get('lampac_profile_name', '') || '');
    var avatar = String(Lampa.Storage.get('lampac_profile_avatar', '') || '');
    if (avatarIsImage(avatar)) {
        return '<img src="' + avatarImageURL(avatar) + '" style="width:1.7em;height:1.7em;border-radius:50%;object-fit:cover;display:block" onerror="this.style.display=\'none\'"/>';
    }
    var glyph = pid && name ? profileGlyph({ name: name, avatar: avatar }) : '👤';
    return '<span style="display:inline-flex;align-items:center;justify-content:center;width:1.7em;height:1.7em;border-radius:50%;background:rgba(255,255,255,.12);font-size:1em;line-height:1">' + glyph + '</span>';
}

// setupProfileButton — wire profile switching into the header.
//
// The ROBUST path is hooking the NATIVE profile button (.open--profile) rather
// than injecting our own: Lampa's own header shows it, and the Apple TV
// AGNative plugin rebuilds the header but its profile button proxies straight
// to .open--profile via $(node).trigger('hover:enter'). A custom button we add
// next to search is wiped whenever AGNative (or a Lampa re-render) rebuilds the
// header — which is exactly the "кнопка пропадает" the user hit. Delegation +
// overriding the CUB profile API survive all rebuilds. We only fall back to our
// own button on builds that have no native profile button at all.
function setupProfileButton() {
    try {
        if (!window.__syncpro_profile_hook) {
            window.__syncpro_profile_hook = true;

            // Delegated so it catches the native button no matter who (re)built
            // the header, plus AGNative's $(node).trigger('hover:enter') proxy.
            // `click` is included for desktop/mouse where Lampa doesn't relay a
            // hover:* event. Also covers our own injected button.
            $('body').on('hover:enter hover:click hover:touch click', '.open--profile, .open--syncpro-profile', function (e) {
                try { if (e && e.stopPropagation) e.stopPropagation(); } catch (_) {}
                openProfilePickerGated();
                return false;
            });

            // AGNative's fallback (openProfilesDirect) and any other caller of
            // Lampa's CUB profile selector are redirected to our picker too.
            try {
                if (Lampa.Account) {
                    Lampa.Account.showProfiles = function () { openProfilePickerGated(); };
                    if (Lampa.Account.Profile) Lampa.Account.Profile.select = function () { openProfilePickerGated(); };
                }
            } catch (_) {}

            // Keep the button alive across header re-renders. Lampa rebuilds the
            // head on activity changes and AGNative rebuilds it wholesale — a
            // one-shot inject vanishes. A cheap guarded re-check (only mutates
            // when the button is missing) survives every rebuild without a
            // heavy MutationObserver.
            setInterval(function () { try { paintProfileButtons(); } catch (_) {} }, 2000);
        }

        paintProfileButtons();
    } catch (e) { /* header not ready yet — retried on app ready / hydrate */ }
}

// agnativeActive — the Apple TV AGNative plugin replaces the whole header with
// its own topnav and owns the profile button (which proxies to the native
// .open--profile our delegation hooks). When it's present we must NOT inject
// our own button — AGNative would wipe it on every rebuild, causing a flicker,
// and its own button already routes to us.
function agnativeActive() {
    try { return !!document.querySelector('.agnative-topnav, .agnative-topnav-shell, .agnative-topnav-rightdock'); }
    catch (e) { return false; }
}

// paintProfileButtons — ensure exactly one working, reachable profile button
// and reflect the active avatar on it.
//
// Vanilla Lampa hides its native .open--profile until a CUB account is "used"
// (our users authenticate via Telegram, so it stays hidden = unreachable by
// the D-pad). Relying on it left the picker dead on the normal interface. So
// on vanilla we inject OUR OWN always-visible button (in the head controller's
// .selector collection → reachable by remote AND mouse) and hide the flaky
// native one. Under AGNative we stay hands-off; the delegation above handles it.
function paintProfileButtons() {
    if (agnativeActive()) {
        // AGNative owns the header. Just make sure the native button it proxies
        // to isn't display:none (jQuery.trigger works on hidden nodes, but keep
        // it clean) and drop any leftover button of ours.
        $('.open--profile').removeClass('hide');
        $('.open--syncpro-profile').remove();
        return;
    }

    var av = String(Lampa.Storage.get('lampac_profile_avatar', '') || '');

    // Prefer the native button when it's genuinely present in a vanilla head —
    // but only trust it if it can be made reachable. We un-hide it and also
    // keep our own as the reliable path; to avoid two buttons we hide the
    // native and drive everything through ours.
    var head = $('.head__actions');
    if (!head.length) return; // header not mounted yet

    // Our own button — always present, reachable, clickable.
    if (!$('.open--syncpro-profile').length) {
        var btn = $('<div class="head__action selector open--syncpro-profile" title="' + Lampa.Lang.translate('syncpro_profiles_title') + '">' + profileAvatarInner() + '</div>');
        var anchor = $('.head__action.open--search');
        if (anchor.length) anchor.after(btn);
        else head.append(btn);
    } else {
        // Refresh avatar on the existing button.
        $('.open--syncpro-profile').html(profileAvatarInner());
    }

    // Hide the native CUB profile button so there's no duplicate; the
    // delegation still catches it if a build shows it anyway.
    $('.open--profile').addClass('hide');
}

// Back-compat alias — call sites elsewhere still reference this name.
function renderProfileHeaderButton() { setupProfileButton(); }

// scheduleProfileButtonPaint — AGNative (and some skins) rebuild the header
// asynchronously after app:ready, so a single paint can land before the
// header exists. The delegation + API override are one-time and rebuild-proof;
// only the visible paint/fallback needs a few retries.
function scheduleProfileButtonPaint() {
    setupProfileButton();
    [300, 900, 2000].forEach(function (ms) { setTimeout(setupProfileButton, ms); });
}

// hydrateActiveProfile — refresh the cached name/avatar of the locally-active
// profile from the server (it may have been renamed / re-avatared from
// another device or the alpac app), then re-render the header button.
// bootProfileGate — when the active profile at launch is PIN-locked and hasn't
// been verified this session, require the PIN BEFORE its private data is used.
// Cancel / wrong PIN drops to «Общий» so a locked profile is never opened
// without its password (the "кидает на профиль с паролем, не спрашивая" bug).
function bootProfileGate(profileObj, done) {
    var fallbackToShared = function () {
        Lampa.Storage.set('lampac_profile_id', '');
        Lampa.Storage.set('lampac_profile_name', '');
        Lampa.Storage.set('lampac_profile_avatar', '');
        if (done) done(false);
    };
    Lampa.Input.edit({
        title: Lampa.Lang.translate('syncpro_enter_pin').replace('{name}', profileObj.name || ''),
        value: '', free: true, nosave: true, nomic: true,
    }, function (pin) {
        if (!pin) { fallbackToShared(); return; }
        SubProfiles.verify(profileObj.id, pin, function (resp) {
            if (resp && resp.ok) {
                _pinVerifiedSession[String(profileObj.id)] = true;
                if (done) done(true);
            } else {
                Lampa.Noty.show(Lampa.Lang.translate('syncpro_wrong_pin'));
                fallbackToShared();
            }
        }, function () { fallbackToShared(); });
    });
}

// hydrateActiveProfile — refresh the active profile's cached name/avatar from
// the server and enforce the boot PIN gate. `done` fires after everything
// resolves (with the FINAL active profile), so the caller can pull data with
// the correct profile_id — never the locked one before it's verified.
function hydrateActiveProfile(done) {
    var finish = function () { renderProfileHeaderButton(); if (done) done(); };
    var pid = String(Lampa.Storage.get('lampac_profile_id', '') || '');
    if (!pid) { finish(); return; }
    SubProfiles.list(function (resp) {
        var active = null;
        if (resp && resp.profiles) {
            for (var i = 0; i < resp.profiles.length; i++) {
                if (String(resp.profiles[i].id) === pid) { active = resp.profiles[i]; break; }
            }
        }
        if (active) {
            Lampa.Storage.set('lampac_profile_name', active.name || '');
            Lampa.Storage.set('lampac_profile_avatar', active.avatar || '');
            if (active.hasPin && !_pinVerifiedSession[pid]) {
                bootProfileGate(active, function () { finish(); });
                return;
            }
        }
        finish();
    }, function () {
        finish();
    });
}

function openProfileSheet() {
    refreshProfileState(function (st) {
        var items = [];
        if (st.authenticated && st.auth_method === 'profile') {
            // Password-profile accounts get the same unified sub-profiles as
            // TG users — the owner key is just profile:<uuid> instead of tg:<id>.
            items.push({
                title: Lampa.Lang.translate('syncpro_switch_profile'),
                action: openProfilePickerGated,
                subtitle: currentActiveProfileLabel(),
            });
            items.push({
                title: Lampa.Lang.translate('syncpro_profile_sync_issue'),
                action: doIssueSyncCode,
            });
            items.push({
                title: Lampa.Lang.translate('syncpro_profile_chgpass'),
                action: doChangePassword,
            });
            items.push({
                title: Lampa.Lang.translate('syncpro_profile_logout'),
                action: doLogout,
            });
        } else if (st.authenticated && st.auth_method === 'telegram') {
            // TG-authenticated users treat profiles as a *sub-identity* of
            // their TG account (Netflix-style "Дети / Жена / Я"). The
            // primary identity stays tg:<id>; each profile is just a key
            // (lampac_profile_id) that segments bookmarks/timecodes/etc.
            // server-side via the profile_id/pathfile query params.
            items.push({
                title: Lampa.Lang.translate('syncpro_switch_profile'),
                action: openProfilePickerGated,
                subtitle: currentActiveProfileLabel(),
            });
        } else {
            // Anonymous (or expired session): show all four entry points.
            items.push({ title: Lampa.Lang.translate('syncpro_profile_register'),    action: doRegister });
            items.push({ title: Lampa.Lang.translate('syncpro_profile_login'),       action: doLogin });
            items.push({ title: Lampa.Lang.translate('syncpro_profile_pin_login'),   action: doPinLogin });
            items.push({ title: Lampa.Lang.translate('syncpro_profile_sync_redeem'), action: doRedeemSyncCode });
        }
        openSheet(Lampa.Lang.translate('syncpro_open_profile_mgmt'), items);
    });
}

// Domain toggle sheet — each tap flips the storage flag and re-shows the
// sheet so the user sees the new state without leaving.
var DOMAIN_DEFS = [
    { key: 'bookmarks', label: 'syncpro_dom_bookmarks' },
    { key: 'timecodes', label: 'syncpro_dom_timecodes' },
    { key: 'history',   label: 'syncpro_dom_history' },
    { key: 'torrents',  label: 'syncpro_dom_torrents' },
    { key: 'search',    label: 'syncpro_dom_search' },
    { key: 'plugins',   label: 'syncpro_dom_plugins' },
];

function domainsSummary() {
    var on = 0;
    DOMAIN_DEFS.forEach(function (d) { if (pref(d.key, true)) on++; });
    return Lampa.Lang.translate('syncpro_summary_domains')
        .replace('{n}', on).replace('{total}', DOMAIN_DEFS.length);
}

function openDomainsSheet() {
    var items = DOMAIN_DEFS.map(function (d) {
        var on = pref(d.key, true);
        return {
            title: (on ? '✓ ' : '✗ ') + Lampa.Lang.translate(d.label),
            action: function () {
                Lampa.Storage.set('syncpro_' + d.key, !on);
                // Re-open so the user sees the new check state immediately.
                openDomainsSheet();
            },
        };
    });
    openSheet(Lampa.Lang.translate('syncpro_open_domains'), items);
}

function openActionsSheet() {
    var items = [
        {
            title: Lampa.Lang.translate('syncpro_action_force_pull'),
            action: function () {
                Lampa.Loading.start();
                pullAll();
                setTimeout(function () {
                    Lampa.Loading.stop();
                    Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_pulled'));
                }, 1500);
            },
        },
        {
            title: Lampa.Lang.translate('syncpro_action_backup_save'),
            action: function () {
                Lampa.Loading.start();
                FullBackup.save(
                    function () {
                        Lampa.Loading.stop();
                        Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_backup_ok'));
                    },
                    function (status) {
                        Lampa.Loading.stop();
                        Lampa.Noty.show(Lampa.Lang.translate('syncpro_err_generic').replace('{code}', status || '?'));
                    }
                );
            },
        },
        {
            title: Lampa.Lang.translate('syncpro_action_backup_load'),
            action: function () {
                // Confirmation step — restore overwrites the whole local
                // store, which is too destructive to do on one tap.
                Lampa.Select.show({
                    title: Lampa.Lang.translate('sure'),
                    nomark: true,
                    items: [
                        { title: Lampa.Lang.translate('confirm'), confirm: true, selected: true },
                        { title: Lampa.Lang.translate('cancel') },
                    ],
                    onSelect: function (a) {
                        if (!a.confirm) { openActionsSheet(); return; }
                        Lampa.Loading.start();
                        FullBackup.restore(
                            function () {
                                Lampa.Loading.stop();
                                Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_backup_restored'));
                                setTimeout(function () { window.location.reload(); }, 2500);
                            },
                            function (status) {
                                Lampa.Loading.stop();
                                Lampa.Noty.show(Lampa.Lang.translate('syncpro_err_generic').replace('{code}', status || '?'));
                            }
                        );
                    },
                    onBack: function () { openActionsSheet(); },
                });
            },
        },
        {
            title: Lampa.Lang.translate('syncpro_action_wipe'),
            action: function () {
                // «Одной кнопкой снести всё с серверов»: бэкап + sync-файлы аккаунта
                // (вместе с легаси-записями устройств и sub-профилями), на этом сервере
                // и на кластер-нодах. Локальное состояние устройства не трогаем —
                // sync пересоздаст свои файлы при следующем действии, это ожидаемо:
                // сценарий юзера — «почистить, настроить заново, сделать новый бэкап».
                Lampa.Select.show({
                    title: Lampa.Lang.translate('sure'),
                    subtitle: Lampa.Lang.translate('syncpro_wipe_confirm'),
                    nomark: true,
                    items: [
                        { title: Lampa.Lang.translate('confirm'), confirm: true },
                        { title: Lampa.Lang.translate('cancel'), selected: true },
                    ],
                    onSelect: function (a) {
                        if (!a.confirm) { openActionsSheet(); return; }
                        Lampa.Loading.start();
                        httpJSON('POST', '/storage/wipe', null, function (j) {
                            Lampa.Loading.stop();
                            if (!j || !j.success) {
                                Lampa.Noty.show(Lampa.Lang.translate('syncpro_err_generic').replace('{code}', (j && (j.msg || j.error)) || '?'));
                                return;
                            }
                            var msg = Lampa.Lang.translate('syncpro_msg_wiped').replace('{n}', String(j.deleted || 0));
                            if (j.deleted_remote) msg += Lampa.Lang.translate('syncpro_msg_wiped_nodes').replace('{m}', String(j.deleted_remote));
                            Lampa.Noty.show(msg);
                        }, function (_, status) {
                            Lampa.Loading.stop();
                            Lampa.Noty.show(Lampa.Lang.translate('syncpro_err_generic').replace('{code}', status || 'network'));
                        });
                    },
                    onBack: function () { openActionsSheet(); },
                });
            },
        },
    ];
    openSheet(Lampa.Lang.translate('syncpro_open_actions'), items);
}

// Build the compact Settings → Sync page. Three rows total:
//   1. Profile status (static, live-updated)
//   2. Profile management        (opens Lampa.Select)
//   3. What to sync (X of N)     (opens Lampa.Select with toggles)
//   4. Actions                   (opens Lampa.Select with pull/backup)
//
// The previous flat layout had ~19 rows; consolidating action-style entries
// behind a single sheet collapses that to four and matches the look of
// Lampa's own "Mirror / Source" pickers.
function buildSettings() {
    Lampa.SettingsApi.addComponent({
        component: 'syncpro',
        icon: SVG_ICON,
        name: Lampa.Lang.translate('syncpro_title'),
    });

    // -- profile status (live) --
    Lampa.SettingsApi.addParam({
        component: 'syncpro',
        param: { type: 'static' },
        field: {
            name: Lampa.Lang.translate('syncpro_section_profile'),
            description: profileStatusLine(),
        },
        onRender: function (item) {
            // Cache the row jQuery handle so async events (login, logout,
            // switch-profile) can call repaintProfileStatus() and update
            // the description text in-place without needing the user to
            // close-and-reopen the settings screen.
            profileStatusItem = item;
            refreshProfileState();
        },
    });

    // -- profile actions (contextual) --
    Lampa.SettingsApi.addParam({
        component: 'syncpro',
        param: { type: 'button' },
        field: {
            name: Lampa.Lang.translate('syncpro_open_profile_mgmt'),
            description: '',
        },
        onChange: openProfileSheet,
    });

    // -- what to sync (live summary in description) --
    Lampa.SettingsApi.addParam({
        component: 'syncpro',
        param: { type: 'button' },
        field: {
            name: Lampa.Lang.translate('syncpro_open_domains'),
            description: domainsSummary(),
        },
        onRender: function (item) {
            // Refresh "X of N enabled" on every render so the user sees
            // changes immediately after closing the toggle sheet.
            try { item.find('.settings-param__descr').text(domainsSummary()); } catch (e) { /* ignore */ }
        },
        onChange: openDomainsSheet,
    });

    // -- actions (pull / backup) --
    Lampa.SettingsApi.addParam({
        component: 'syncpro',
        param: { type: 'button' },
        field: {
            name: Lampa.Lang.translate('syncpro_open_actions'),
            description: '',
        },
        onChange: openActionsSheet,
    });

    // -- profile switch behavior: soft refresh (default) vs full reload --
    Lampa.SettingsApi.addParam({
        component: 'syncpro',
        param: { name: 'syncpro_switch_reload', type: 'trigger', default: false },
        field: {
            name: Lampa.Lang.translate('syncpro_switch_reload'),
            description: '',
        },
        onChange: function () { /* pref() reads live */ },
    });
}

// ----------------------------------------------------------------------
//  Fresh-device backup offer
// ----------------------------------------------------------------------
// «Сделал бэкап — пусть он станет настройками по умолчанию при входе с моего
// аккаунта»: свежая инсталляция при первом старте проверяет, лежит ли на
// сервере аккаунт-бэкап, и предлагает применить его. Один вопрос на
// устройство (маркер), и только пока локалка действительно пустая — обжитой
// инсталляции restore перезаписал бы её данные.
var BACKUP_OFFER_KEY = 'syncpro_backup_offered';

function localLooksFresh() {
    try {
        var f = Lampa.Storage.get('favorite', '{}');
        if (typeof f === 'string') f = JSON.parse(f);
        if (f && ((f.card && f.card.length) || (f.history && f.history.length) || (f.like && f.like.length))) return false;
    } catch (e) { /* повреждённый favorite = не мешаем */ }
    return true;
}

function maybeOfferBackupRestore() {
    try {
        if (Lampa.Storage.get(BACKUP_OFFER_KEY, '')) return; // уже спрашивали/пропустили
        if (!localLooksFresh()) {
            // обжитая инсталляция — вопрос не задаём и больше не проверяем
            Lampa.Storage.set(BACKUP_OFFER_KEY, 'has_local');
            return;
        }
        // responseInfo=true — только метаданные, без 5-20 МБ тела бэкапа
        httpJSON('GET', '/storage/get?path=backup&responseInfo=true', null, function (j) {
            if (!j || !j.success || !j.fileInfo || !j.fileInfo.Length) return; // бэкапа нет; маркер НЕ ставим — появится позже, предложим тогда
            Lampa.Storage.set(BACKUP_OFFER_KEY, 'asked');
            var when = '';
            try { when = new Date(j.fileInfo.changeTime).toLocaleDateString(); } catch (e) { /* ignore */ }
            Lampa.Select.show({
                title: Lampa.Lang.translate('syncpro_offer_title').replace('{date}', when || '—'),
                subtitle: Lampa.Lang.translate('syncpro_offer_text'),
                nomark: true,
                items: [
                    { title: Lampa.Lang.translate('syncpro_offer_apply'), apply: true, selected: true },
                    { title: Lampa.Lang.translate('syncpro_offer_skip') },
                ],
                onSelect: function (a) {
                    if (!a.apply) { if (Lampa.Controller) Lampa.Controller.toggle('content'); return; }
                    Lampa.Loading.start();
                    FullBackup.restore(
                        function () {
                            // маркер переживает restore? Бэкап его перезапишет тем, что было
                            // на устройстве-источнике — ставим ПОСЛЕ применения ещё раз.
                            try { Lampa.Storage.set(BACKUP_OFFER_KEY, 'applied'); } catch (e) { /* ignore */ }
                            Lampa.Loading.stop();
                            Lampa.Noty.show(Lampa.Lang.translate('syncpro_msg_backup_restored'));
                            setTimeout(function () { window.location.reload(); }, 2000);
                        },
                        function (status) {
                            Lampa.Loading.stop();
                            Lampa.Noty.show(Lampa.Lang.translate('syncpro_err_generic').replace('{code}', status || '?'));
                        }
                    );
                },
                onBack: function () { if (Lampa.Controller) Lampa.Controller.toggle('content'); },
            });
        }, function () { /* сеть/авторизация — молчим, спросим в другой раз */ });
    } catch (e) { /* ignore */ }
}

// ----------------------------------------------------------------------
//  Boot
// ----------------------------------------------------------------------

function whenReady(callback) {
    if (typeof window === 'undefined') return;
    if (window.Lampa && Lampa.Favorite && Lampa.Storage && Lampa.SettingsApi && Lampa.Listener && Lampa.Utils) {
        callback();
    } else {
        setTimeout(function () { whenReady(callback); }, 500);
    }
}

function start() {
    loadLang();
    buildSettings();
    refreshProfileState();

    Bookmarks.bind();
    Timecodes.bind();
    ViewHistory.bind();
    Torrents.bind();
    SearchHistory.bind();
    PluginsList.bind();

    bindWS();

    // Boot sequence: resolve the active profile (with the PIN gate) FIRST, then
    // pull — so a locked profile's private data is never loaded before its PIN
    // is verified. Guarded so the 'app ready' event and the window.appready
    // late-load path don't run it twice.
    var booted = false;
    var boot = function () {
        if (booted) return;
        booted = true;
        hydrateActiveProfile(function () { pullAll(); });
        scheduleProfileButtonPaint();
        // После того как интерфейс поднялся: свежему устройству — предложение
        // применить аккаунт-бэкап (задержка, чтобы не спорить за фокус с
        // профильным PIN-гейтом и стартовыми модалками Lampa).
        setTimeout(maybeOfferBackupRestore, 4000);
    };
    Lampa.Listener.follow('app', function (e) { if (e.type === 'ready') boot(); });
    if (window.appready) boot();

    // The WS hub lives in invc-ws.js (already loaded by /sync.js). When the
    // server admin disables sync.js, ensure we still attempt to load it so
    // syncpro's WS bridge has events to listen to. Safe to call twice —
    // putScript de-dupes.
    if (Lampa.Utils && Lampa.Utils.putScript) {
        try {
            Lampa.Utils.putScript([url('/invc-ws.js')], function () {}, function () {});
        } catch (e) { /* not all Lampa builds expose putScript */ }
    }
}

whenReady(start);

})();
