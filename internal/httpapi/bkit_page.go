package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
)

// browserKitPageHandler serves the standalone browser Kit HTML page.
// GET /bkit
func browserKitPageHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := hostFromRequest(r)
		html := strings.ReplaceAll(browserKitHTML, "{API_BASE}", host+"/api/kit")
		html = strings.ReplaceAll(html, "{HOST}", host)

		// If user has any valid auth cookie (TG, password, or anon), inject
		// the auto-login flag so the bkit JS skips the bkit-token login
		// screen. Check stores in priority order — TG first (so existing
		// TG users still see their tg_username), then password, then anon.
		token := ""
		if cookie, err := r.Cookie("lampac_token"); err == nil && cookie.Value != "" {
			token = cookie.Value
		} else if cookie, err := r.Cookie("alpac_token"); err == nil && cookie.Value != "" {
			token = cookie.Value
		}
		if token != "" {
			authed := false
			userName := ""
			if kitTGTokenStore != nil {
				if approved, ok := kitTGTokenStore.Lookup(token); ok {
					authed = true
					userName = approved.TGUsername
				}
			}
			if !authed && globalPwUserStore != nil {
				if user, _, ok := globalPwUserStore.LookupSession(token); ok {
					authed = true
					userName = "@" + user.Username
				}
			}
			if !authed && globalAnonIssuer != nil && currentAuthMode() == "none" {
				if user, ok := globalAnonIssuer.Lookup(token); ok {
					authed = true
					userName = "anon:" + user.UID
				}
			}
			if authed {
				safeName := strings.ReplaceAll(userName, `\`, `\\`)
				safeName = strings.ReplaceAll(safeName, `'`, `\'`)
				injection := fmt.Sprintf(`<script>window.TG_AUTH=true;window.TG_USER='%s';</script>`, safeName)
				html = strings.Replace(html, "<script>", injection+"\n<script>", 1)
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// no-store: the page interpolates per-request values (TG cookie →
		// auto-login flag, host substitution, branding). A stale cached
		// variant has caused "Can't find variable: esc" errors when an
		// older renderProfile() lacked helpers added in a later release —
		// browsers held the old JS body even after the server changed.
		// See same rationale on genericPluginJSHandler.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(html))
	}
}

// bkitAdminSessionsHandler handles CRUD for browser Kit sessions.
// GET  /admin/api/bkit/sessions → list
// POST /admin/api/bkit/sessions → create (body: {"name":"..."})
func bkitAdminSessionsHandler(bkitStore *BKitSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		switch r.Method {
		case http.MethodGet:
			sessions := bkitStore.List()
			_ = json.NewEncoder(w).Encode(sessions)

		case http.MethodPost:
			var req struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
				req.Name = "Пользователь"
			}
			sess := bkitStore.Create(strings.TrimSpace(req.Name))
			_ = json.NewEncoder(w).Encode(sess)

		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

// bkitAdminDeleteSessionHandler deletes a browser Kit session.
// DELETE /admin/api/bkit/sessions/{id}
func bkitAdminDeleteSessionHandler(bkitStore *BKitSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		id := strings.TrimPrefix(r.URL.Path, "/"+adminPathGlobal+"/api/bkit/sessions/")
		id = strings.TrimRight(id, "/")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}
		if bkitStore.Delete(id) {
			_, _ = w.Write([]byte(`{"success":true}`))
		} else {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}
}

// adminPathGlobal is set from server.go to share the admin path prefix.
var adminPathGlobal string

const browserKitHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0,maximum-scale=1.0,user-scalable=no">
<title>Kit</title>
<style>
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#0f0f23;--surface:rgba(255,255,255,.05);--surface-hover:rgba(255,255,255,.08);
  --glass:rgba(255,255,255,.06);--glass-border:rgba(255,255,255,.1);
  --text:#e2e8f0;--text2:#94a3b8;--text3:#64748b;
  --accent:#818cf8;--accent-hover:#6366f1;--accent-glow:rgba(99,102,241,.25);
  --success:#34d399;--danger:#f87171;--danger-hover:#ef4444;
  --radius:16px;--radius-sm:10px;
  --font:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;
}
html{height:100%}
body{font-family:var(--font);background:var(--bg);color:var(--text);min-height:100%;line-height:1.5}
::selection{background:var(--accent);color:#fff}

/* ─── Login ─── */
.login-wrap{display:flex;align-items:center;justify-content:center;min-height:100vh;padding:24px}
.login-card{
  background:var(--glass);border:1px solid var(--glass-border);
  backdrop-filter:blur(20px);-webkit-backdrop-filter:blur(20px);
  border-radius:24px;padding:48px 40px;width:100%;max-width:420px;text-align:center;
  box-shadow:0 8px 32px rgba(0,0,0,.4);
}
.login-logo{font-size:48px;margin-bottom:8px}
.login-title{font-size:24px;font-weight:700;margin-bottom:4px}
.login-sub{color:var(--text2);font-size:14px;margin-bottom:32px}
.login-input{
  width:100%;padding:14px 18px;border-radius:var(--radius-sm);
  border:1px solid var(--glass-border);background:rgba(255,255,255,.04);
  color:var(--text);font-size:16px;font-family:monospace;letter-spacing:.5px;
  transition:border-color .2s,box-shadow .2s;outline:none;
}
.login-input:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-glow)}
.login-input::placeholder{color:var(--text3)}
.login-btn{
  width:100%;padding:14px;margin-top:16px;border:none;border-radius:var(--radius-sm);
  background:linear-gradient(135deg,var(--accent),#a78bfa);color:#fff;
  font-size:16px;font-weight:600;cursor:pointer;transition:transform .15s,box-shadow .2s;
}
.login-btn:hover{transform:translateY(-1px);box-shadow:0 4px 20px var(--accent-glow)}
.login-btn:active{transform:translateY(0)}
.login-error{color:var(--danger);font-size:13px;margin-top:12px;min-height:20px}

/* ─── App shell ─── */
.app{display:none;padding:0 0 100px}
.app.active{display:block}
.header{
  display:flex;align-items:center;justify-content:space-between;
  padding:20px 24px 12px;position:sticky;top:0;z-index:10;
  background:rgba(15,15,35,.85);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);
}
.header-left{display:flex;align-items:center;gap:12px}
.header-title{font-size:20px;font-weight:700}
.header-user{font-size:13px;color:var(--text2)}
.header-logout{
  background:none;border:1px solid var(--glass-border);color:var(--text2);
  padding:6px 14px;border-radius:8px;font-size:13px;cursor:pointer;transition:all .2s;
}
.header-logout:hover{border-color:var(--danger);color:var(--danger)}

/* ─── Tabs ─── */
.tabs{display:flex;gap:0;padding:0 24px;border-bottom:1px solid var(--glass-border);margin-bottom:4px}
.tab{
  flex:1;padding:14px 0;text-align:center;font-size:15px;font-weight:500;
  color:var(--text3);cursor:pointer;border-bottom:2px solid transparent;
  transition:all .2s;user-select:none;
}
.tab:hover{color:var(--text2)}
.tab.active{color:var(--accent);border-bottom-color:var(--accent)}

/* ─── Section headers ─── */
.section-title{font-size:16px;font-weight:600;padding:20px 24px 10px;color:var(--text)}

/* ─── Cards ─── */
.card{
  background:var(--surface);border:1px solid transparent;border-radius:var(--radius);
  padding:16px 20px;margin:0 16px 8px;
  display:flex;align-items:center;justify-content:space-between;gap:14px;
  transition:background .2s,border-color .2s;
}
.card:hover{background:var(--surface-hover);border-color:var(--glass-border)}
.card-info{flex:1;min-width:0}
.card-name{font-size:15px;font-weight:600}
.card-status{font-size:13px;color:var(--text3);margin-top:2px}
.card-status.bound{color:var(--success)}
.badge{
  font-size:11px;font-weight:600;padding:3px 10px;border-radius:20px;
  white-space:nowrap;letter-spacing:.3px;
}
.badge-free{background:rgba(52,211,153,.12);color:#34d399}
.badge-premium{background:rgba(251,191,36,.12);color:#fbbf24}

/* ─── Profile ─── */
.profile-section{padding:16px}
.profile-hero{
  display:flex;align-items:center;gap:16px;
  background:var(--glass);border:1px solid var(--glass-border);
  border-radius:var(--radius);padding:24px;margin-bottom:12px;
}
.profile-avatar{
  width:56px;height:56px;border-radius:50%;flex-shrink:0;
  background:linear-gradient(135deg,var(--accent),#a78bfa);
  display:flex;align-items:center;justify-content:center;
  font-size:24px;font-weight:700;color:#fff;
  box-shadow:0 4px 16px var(--accent-glow);
}
.profile-info{flex:1;min-width:0}
.profile-name{font-size:18px;font-weight:700;line-height:1.3}
.profile-tgid{font-size:12px;color:var(--text3);margin-top:2px}
.profile-card{
  background:var(--glass);border:1px solid var(--glass-border);
  border-radius:var(--radius);padding:16px 20px;margin-bottom:12px;
}
.profile-card-title{font-size:13px;font-weight:600;color:var(--text2);margin-bottom:10px;display:flex;align-items:center;gap:6px}
.profile-sub-row{display:flex;align-items:center;justify-content:space-between;gap:8px}
.profile-sub-date{font-size:15px;font-weight:600}
.profile-sub-badge{
  padding:3px 12px;border-radius:20px;font-size:12px;font-weight:600;white-space:nowrap;
}
.profile-sub-badge.ok{background:rgba(52,211,153,.15);color:#34d399}
.profile-sub-badge.warn{background:rgba(251,191,36,.15);color:#fbbf24}
.profile-sub-badge.danger{background:rgba(248,113,113,.15);color:#f87171}
.profile-sub-badge.expired{background:rgba(248,113,113,.15);color:#f87171}
.profile-group-badge{
  display:inline-block;padding:3px 12px;border-radius:20px;font-size:12px;font-weight:600;
  background:rgba(129,140,248,.15);color:var(--accent);
}
.profile-device{
  display:flex;align-items:center;gap:12px;padding:10px 0;
  border-bottom:1px solid rgba(255,255,255,.04);
}
.profile-device:last-child{border-bottom:none}
.profile-device-icon{font-size:20px;flex-shrink:0}
.profile-device-info{flex:1;min-width:0}
.profile-device-label{font-size:14px;font-weight:500}
.profile-device-meta{font-size:11px;color:var(--text3);margin-top:1px}
.profile-empty{color:var(--text3);font-size:13px;padding:8px 0}

/* Sync profiles (PIN-managed) — same visual language as devices */
.sp-row{
  display:flex;align-items:center;gap:12px;padding:10px 0;
  border-bottom:1px solid rgba(255,255,255,.04);
}
.sp-row:last-child{border-bottom:none}
.sp-icon{
  width:34px;height:34px;border-radius:10px;flex-shrink:0;
  display:flex;align-items:center;justify-content:center;font-size:14px;font-weight:600;
  background:linear-gradient(135deg,var(--accent),var(--accent-hover));color:#fff;
}
.sp-info{flex:1;min-width:0}
.sp-name{font-size:14px;font-weight:500;display:flex;align-items:center;gap:6px}
.sp-meta{font-size:11px;color:var(--text3);margin-top:1px}
.sp-actions{display:flex;gap:6px;flex-shrink:0}
.sp-pin-tag{
  font-size:10px;padding:2px 8px;border-radius:10px;font-weight:600;
  background:rgba(129,140,248,.15);color:var(--accent);
}
.sp-pin-tag.off{background:rgba(100,116,139,.15);color:var(--text3)}
.sp-add{
  display:flex;align-items:center;justify-content:center;gap:6px;
  width:100%;padding:11px;border:1px dashed var(--glass-border);background:transparent;
  border-radius:var(--radius-sm);color:var(--accent);font-weight:500;cursor:pointer;
  margin-top:8px;transition:all .15s;
}
.sp-add:hover{background:rgba(129,140,248,.08);border-color:var(--accent)}
.sp-help{font-size:11px;color:var(--text3);margin-top:10px;line-height:1.5}

/* ─── Buttons ─── */
.btn{
  display:inline-flex;align-items:center;justify-content:center;gap:6px;
  padding:10px 20px;border:none;border-radius:var(--radius-sm);
  font-size:14px;font-weight:500;cursor:pointer;transition:all .15s;
}
.btn-primary{
  background:linear-gradient(135deg,var(--accent),#a78bfa);color:#fff;
}
.btn-primary:hover{box-shadow:0 2px 12px var(--accent-glow);transform:translateY(-1px)}
.btn-danger{background:rgba(248,113,113,.1);color:var(--danger);font-size:12px;padding:8px 14px}
.btn-danger:hover{background:rgba(248,113,113,.2)}
.btn-sm{padding:8px 14px;font-size:13px}

/* ─── Toggle ─── */
.toggle-row{
  background:var(--surface);border-radius:var(--radius);
  padding:14px 20px;margin:0 16px 6px;
  display:flex;align-items:center;justify-content:space-between;
}
.toggle-label{font-size:15px;font-weight:500}
.toggle-sub{font-size:12px;color:var(--text3);margin-top:2px;font-family:monospace}
.switch{position:relative;width:48px;height:26px;flex-shrink:0}
.switch input{opacity:0;width:0;height:0}
.switch .slider{
  position:absolute;inset:0;background:rgba(255,255,255,.1);border-radius:26px;
  cursor:pointer;transition:.25s;
}
.switch .slider:before{
  content:"";position:absolute;width:20px;height:20px;
  left:3px;bottom:3px;background:#fff;border-radius:50%;transition:.25s;
}
.switch input:checked+.slider{background:var(--accent)}
.switch input:checked+.slider:before{transform:translateX(22px)}
.switch input:disabled+.slider{opacity:.3;cursor:default}

/* ─── Token input ─── */
.token-row{padding:0 20px 0 36px;margin:0 16px 2px}
.token-input{
  width:100%;padding:8px 12px;border:1px solid rgba(255,255,255,.08);
  border-radius:8px;font-size:13px;background:rgba(255,255,255,.03);
  color:var(--text);font-family:monospace;transition:border-color .2s;outline:none;
}
.token-input:focus{border-color:var(--accent);box-shadow:0 0 0 2px var(--accent-glow)}

/* ─── Balancers ─── */
.group-header{
  display:flex;align-items:center;justify-content:space-between;
  padding:18px 24px 8px;
}
.group-title{font-weight:600;font-size:15px;display:flex;align-items:center;gap:8px}
.group-count{font-size:12px;color:var(--text3);font-weight:400;margin-left:6px}
.bal-row{
  background:var(--surface);border-radius:var(--radius);
  padding:12px 20px;margin:0 16px 4px;
  display:flex;align-items:center;justify-content:space-between;transition:opacity .2s;
}
.bal-row.disabled{opacity:.35}
.bal-name{font-size:14px;font-weight:500}
.bal-quality{font-size:11px;color:var(--accent);margin-left:8px;font-weight:600}
.bal-note{font-size:11px;color:var(--text3);display:block;margin-top:2px}

/* ─── Modal ─── */
.modal-overlay{
  display:none;position:fixed;inset:0;z-index:100;
  background:rgba(0,0,0,.6);backdrop-filter:blur(4px);-webkit-backdrop-filter:blur(4px);
  align-items:center;justify-content:center;padding:20px;
}
.modal-overlay.active{display:flex}
.modal{
  background:var(--bg);border:1px solid var(--glass-border);
  border-radius:20px;padding:32px;width:100%;max-width:420px;position:relative;
  box-shadow:0 16px 48px rgba(0,0,0,.5);
}
.modal h3{font-size:20px;font-weight:700;margin-bottom:20px}
.modal .close{
  position:absolute;top:16px;right:20px;background:none;
  border:none;font-size:28px;cursor:pointer;color:var(--text3);transition:color .2s;
}
.modal .close:hover{color:var(--text)}
.form-group{margin-bottom:16px}
.form-group label{display:block;font-size:13px;color:var(--text2);margin-bottom:6px;font-weight:500}
.form-group input{
  width:100%;padding:12px 14px;border:1px solid var(--glass-border);
  border-radius:var(--radius-sm);font-size:15px;background:rgba(255,255,255,.04);
  color:var(--text);outline:none;transition:border-color .2s;
}
.form-group input:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-glow)}
.device-code{
  font-size:32px;font-weight:700;text-align:center;
  padding:20px;background:var(--surface);border-radius:var(--radius);
  letter-spacing:6px;color:var(--accent);margin:16px 0;
  font-family:monospace;border:1px solid var(--glass-border);
}
.device-steps{font-size:14px;color:var(--text2);margin:10px 0;line-height:1.7}
.device-steps a{color:var(--accent);text-decoration:none}
.device-steps a:hover{text-decoration:underline}

/* ─── Toast ─── */
.toast{
  position:fixed;bottom:32px;left:50%;transform:translateX(-50%) translateY(20px);
  background:var(--surface);border:1px solid var(--glass-border);
  backdrop-filter:blur(16px);-webkit-backdrop-filter:blur(16px);
  color:var(--text);padding:12px 24px;border-radius:var(--radius-sm);
  font-size:14px;z-index:200;opacity:0;transition:all .3s;pointer-events:none;
}
.toast.show{opacity:1;transform:translateX(-50%) translateY(0)}

/* ─── Save button ─── */
.save-bar{
  position:fixed;bottom:0;left:0;right:0;z-index:50;
  background:rgba(15,15,35,.9);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);
  border-top:1px solid var(--glass-border);padding:12px 24px;
  display:none;align-items:center;justify-content:center;
}
.save-bar.active{display:flex}
.save-btn{
  width:100%;max-width:400px;padding:14px;border:none;border-radius:var(--radius-sm);
  background:linear-gradient(135deg,var(--accent),#a78bfa);color:#fff;
  font-size:16px;font-weight:600;cursor:pointer;transition:all .2s;
}
.save-btn:hover{box-shadow:0 4px 20px var(--accent-glow)}

/* ─── Loading / Empty ─── */
.loading{text-align:center;padding:60px 0;color:var(--text3);font-size:15px}
.spinner{
  display:inline-block;width:18px;height:18px;border:2px solid rgba(255,255,255,.3);
  border-top-color:#fff;border-radius:50%;animation:spin .6s linear infinite;
}
@keyframes spin{to{transform:rotate(360deg)}}

/* ─── Responsive ─── */
@media(max-width:480px){
  .login-card{padding:36px 24px;border-radius:20px}
  .card,.toggle-row,.bal-row{margin:0 8px 6px}
  .token-row{margin:0 8px 2px;padding:0 12px 0 28px}
  .section-title{padding:16px 16px 8px}
  .group-header{padding:14px 16px 6px}
  .header{padding:16px 16px 10px}
}
</style>
</head>
<body>

<!-- Login Screen -->
<div class="login-wrap" id="login-screen">
  <div class="login-card">
    <div class="login-logo">⚡</div>
    <div class="login-title">Kit</div>
    <div class="login-sub" id="login-sub">Введите токен доступа</div>
    <input class="login-input" id="login-token" type="text" placeholder="Токен" autocomplete="off" spellcheck="false">
    <button class="login-btn" id="login-btn" onclick="doLogin()">Войти</button>
    <div class="login-error" id="login-error"></div>
  </div>
</div>

<!-- App -->
<div class="app" id="app">
  <div class="header">
    <div class="header-left">
      <div class="header-title">⚡ Kit</div>
      <div class="header-user" id="header-user"></div>
    </div>
    <button class="header-logout" onclick="doLogout()">Выйти</button>
  </div>

  <div class="tabs">
    <div class="tab active" data-tab="binds" onclick="switchTab('binds')">Привязки</div>
    <div class="tab" data-tab="balancers" onclick="switchTab('balancers')">Балансеры</div>
    <div class="tab" data-tab="profile" onclick="switchTab('profile')">Профиль</div>
  </div>

  <div id="tab-binds"></div>
  <div id="tab-balancers" style="display:none"><div class="loading">Загрузка...</div></div>
  <div id="tab-profile" style="display:none"><div class="loading">Загрузка...</div></div>
</div>

<div class="modal-overlay" id="modal-overlay">
  <div class="modal" id="modal-content"></div>
</div>

<div class="toast" id="toast"></div>

<div class="save-bar" id="save-bar">
  <button class="save-btn" onclick="save()">Сохранить</button>
</div>

<script>
(function(){
'use strict';

var API = '{API_BASE}';
var HOST = '{HOST}';
var token = '';
var config = {};
var hasChanges = false;
var activeTab = 'binds';
var bindsDisabled = false; // admin kill-switch for the "Привязки" tab (from _bindsDisabled)
var balancerData = null;
var balancerVisibility = {};
var userName = '';
var tgAuth = window.TG_AUTH || false;
var tgUser = window.TG_USER || '';

// ── i18n ──
var L = {
  enterToken: 'Введите токен доступа',
  login: 'Войти',
  invalidToken: 'Неверный токен',
  connectionError: 'Ошибка подключения',
  logout: 'Выйти',
  binds: 'Привязки',
  balancers: 'Балансеры',
  bindServices: 'Привязка сервисов',
  bound: '✓ Привязан',
  notBound: 'Не привязан',
  bind: 'Привязать',
  sources: 'Источники',
  tokenPlaceholder: 'Токен',
  loadingBal: 'Загрузка...',
  globallyOff: 'выкл. глобально',
  userBound: 'ваш токен',
  confirmUnbind: 'Удалить привязку?',
  unbound: 'Привязка удалена',
  bindTitle: 'Привязка',
  requesting: 'Запрос кода...',
  error: 'Ошибка',
  step1: '1. Откройте',
  step2: '2. Введите код:',
  step3: '3. Нажмите кнопку ниже',
  finish: 'Завершить привязку',
  boundOk: 'Привязано!',
  bindFail: 'Не удалось завершить привязку',
  bindHint: 'Убедитесь, что вы ввели код на сайте.',
  unavailable: 'Сервис недоступен',
  email: 'Email / Логин',
  password: 'Пароль',
  save: 'Сохранить',
  saved: 'Сохранено!',
  saveErr: 'Ошибка сохранения',
  dataFrom: 'Данные из'
};

// ── Auth ──

function getStoredToken() { try { return localStorage.getItem('bkit_token') || ''; } catch(e) { return ''; } }
function setStoredToken(t) { try { localStorage.setItem('bkit_token', t); } catch(e) {} }
function clearStoredToken() { try { localStorage.removeItem('bkit_token'); } catch(e) {} }

// Check TG cookie auth first, then URL param, then localStorage
(function() {
  if (tgAuth) {
    userName = tgUser;
    showApp();
    load();
    return;
  }
  var params = new URLSearchParams(location.search);
  var urlToken = params.get('token');
  if (urlToken) {
    setStoredToken(urlToken);
    history.replaceState(null, '', location.pathname);
  }
  token = getStoredToken();
  if (token) {
    showApp();
    load();
  }
})();

function api(method, path, body) {
  var opts = {method: method, headers: {}, credentials: 'include'};
  if (!tgAuth && token) {
    opts.headers['Authorization'] = 'Bearer ' + token;
  }
  if (body) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  return fetch(API + path, opts).then(function(r) {
    if (r.status === 401) { if (!tgAuth) doLogout(); throw new Error('unauthorized'); }
    return r.json();
  });
}

window.doLogin = function() {
  var input = document.getElementById('login-token');
  var val = input.value.trim();
  if (!val) return;

  var btn = document.getElementById('login-btn');
  btn.innerHTML = '<span class="spinner"></span>';
  btn.disabled = true;

  token = val;
  // Test token by fetching config.
  api('GET', '/config').then(function(data) {
    if (data.error) {
      showLoginError(L.invalidToken);
      token = '';
      btn.textContent = L.login;
      btn.disabled = false;
      return;
    }
    setStoredToken(token);
    config = data || {};
    processConfig();
    showApp();
    render();
  }).catch(function() {
    showLoginError(L.connectionError);
    token = '';
    btn.textContent = L.login;
    btn.disabled = false;
  });
};

window.doLogout = function() {
  if (tgAuth) {
    // TG cookie auth — redirect to main page
    location.href = '/';
    return;
  }
  clearStoredToken();
  token = '';
  config = {};
  hasChanges = false;
  balancerData = null;
  document.getElementById('app').classList.remove('active');
  document.getElementById('login-screen').style.display = '';
  document.getElementById('login-token').value = '';
  document.getElementById('login-error').textContent = '';
  document.getElementById('save-bar').classList.remove('active');
};

function showLoginError(msg) {
  document.getElementById('login-error').textContent = msg;
}

function showApp() {
  document.getElementById('login-screen').style.display = 'none';
  document.getElementById('app').classList.add('active');
}

// ── Services ──

var bindServices = [
  {id:'filmix', name:'Filmix', badge:'Free', type:'device'},
  {id:'kinopub', name:'KinoPub', badge:'Premium', type:'device'},
  {id:'rezka', name:'PidoRezka', badge:'Premium', type:'login'},
  {id:'vokino', name:'VoKino', badge:'Premium', type:'login'},
  {id:'getstv', name:'GetsTV', badge:'Premium', type:'login'},
  {id:'iptvonline', name:'iptv.online', badge:'Premium', type:'apikey'},
  {id:'pornlab', name:'PornLab', badge:'Free', type:'login'}
];

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

function sectionKey(serviceId) {
  var map = {filmix:'Filmix',kinopub:'KinoPub',rezka:'Rezka',rezkaPrem:'RezkaPrem',vokino:'VoKino',getstv:'GetsTV',iptvonline:'IptvOnline',pornlab:'PornLab'};
  return map[serviceId] || serviceId;
}

function esc(s) { return String(s).replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }

// ── Load ──

function load() {
  api('GET', '/config').then(function(data) {
    if (data.error) { toast(L.error + ': ' + data.error); return; }
    config = data || {};
    processConfig();
    render();
  }).catch(function() {});
}

function processConfig() {
  balancerVisibility = {};
  if (config['_balancerVisibility']) {
    try {
      balancerVisibility = typeof config['_balancerVisibility'] === 'string'
        ? JSON.parse(config['_balancerVisibility'])
        : config['_balancerVisibility'];
    } catch(e) {}
  }

  // Filter hidden services from Kit UI.
  var hidden = config['_kitHiddenServices'] || [];
  if (hidden.length > 0) {
    bindServices = bindServices.filter(function(s) { return hidden.indexOf(s.id) === -1; });
    tokenBalancers = tokenBalancers.filter(function(s) { return hidden.indexOf(s.key.toLowerCase()) === -1; });
  }
  delete config['_kitHiddenServices'];

  // Admin kill-switch for the binds tab: hide its button and land on Балансеры.
  bindsDisabled = !!config['_bindsDisabled'];
  delete config['_bindsDisabled'];
  var bindsTabBtn = document.querySelector('.tab[data-tab="binds"]');
  if (bindsTabBtn) bindsTabBtn.style.display = bindsDisabled ? 'none' : '';
  if (bindsDisabled && activeTab === 'binds') activeTab = 'balancers';

  if (config._userName) { userName = config._userName; delete config._userName; }
  delete config._lang;
  document.getElementById('header-user').textContent = userName;
}

// ── Tabs ──

var profileData = null;
window.switchTab = function(tab) {
  if (tab === 'binds' && bindsDisabled) tab = 'balancers'; // binds tab is hidden
  activeTab = tab;
  document.querySelectorAll('.tab').forEach(function(t) {
    t.classList.toggle('active', t.dataset.tab === tab);
  });
  document.getElementById('tab-binds').style.display = tab === 'binds' ? '' : 'none';
  document.getElementById('tab-balancers').style.display = tab === 'balancers' ? '' : 'none';
  document.getElementById('tab-profile').style.display = tab === 'profile' ? '' : 'none';
  if (tab === 'balancers' && !balancerData) loadBalancers();
  if (tab === 'profile' && !profileData) loadProfile();
};

function loadProfile() {
  api('GET', '/profile').then(function(data) {
    if (data.error) { toast(L.error + ': ' + data.error); return; }
    profileData = data;
    renderProfile();
  }).catch(function() { toast(L.connectionError); });
}

function renderProfile() {
  var p = profileData;
  if (!p) return;
  var el = document.getElementById('tab-profile');

  var initials = '?';
  var name = p.name || '';
  if (name.length > 0) {
    initials = name[0].toUpperCase();
    if (name[0] === '@' && name.length > 1) initials = name[1].toUpperCase();
  }

  // Subscription badge
  var subBadgeCls = 'ok';
  var subBadgeText = p.days_left + ' ' + pluralDays(p.days_left);
  if (p.expired) { subBadgeCls = 'expired'; subBadgeText = '\u0418\u0441\u0442\u0451\u043a'; }
  else if (p.days_left <= 7) subBadgeCls = 'danger';
  else if (p.days_left <= 30) subBadgeCls = 'warn';

  var html = '<div class="profile-section">';

  // Hero card
  html += '<div class="profile-hero">'
    + '<div class="profile-avatar">' + esc(initials) + '</div>'
    + '<div class="profile-info">'
    + '<div class="profile-name">' + esc(name || '\u041f\u043e\u043b\u044c\u0437\u043e\u0432\u0430\u0442\u0435\u043b\u044c') + '</div>'
    + (p.telegram_id ? '<div class="profile-tgid">Telegram ID: ' + p.telegram_id + '</div>' : '')
    + '</div></div>';

  // Subscription
  html += '<div class="profile-card">'
    + '<div class="profile-card-title">\u23F3 \u041f\u043e\u0434\u043f\u0438\u0441\u043a\u0430</div>'
    + '<div class="profile-sub-row">'
    + '<div><div class="profile-sub-date">' + (p.expired ? '\u0418\u0441\u0442\u0435\u043a\u043b\u0430' : '\u0434\u043e ' + esc(p.expires_at)) + '</div>'
    + '<div style="font-size:11px;color:var(--text3);margin-top:2px">\u0421\u043e\u0437\u0434\u0430\u043d: ' + esc(p.created_at) + '</div></div>'
    + '<div class="profile-sub-badge ' + subBadgeCls + '">' + subBadgeText + '</div>'
    + '</div></div>';

  // Group
  if (p.group_name) {
    html += '<div class="profile-card">'
      + '<div class="profile-card-title">\uD83D\uDC65 \u0413\u0440\u0443\u043f\u043f\u0430</div>'
      + '<div class="profile-group-badge">' + esc(p.group_name) + '</div>'
      + '</div>';
  }

  // Devices
  html += '<div class="profile-card">'
    + '<div class="profile-card-title">\uD83D\uDCF1 \u0423\u0441\u0442\u0440\u043e\u0439\u0441\u0442\u0432\u0430'
    + (p.max_devices > 0 ? ' <span style="color:var(--text3);font-weight:400">' + p.device_count + '/' + p.max_devices + '</span>' : '')
    + '</div>';
  if (p.devices && p.devices.length > 0) {
    p.devices.forEach(function(d) {
      var uid = d.uid || '';
      var masked = uid.length > 12 ? uid.substring(0,6) + '\u2026' + uid.substring(uid.length-4) : uid;
      html += '<div class="profile-device">'
        + '<div class="profile-device-icon">\uD83D\uDCF1</div>'
        + '<div class="profile-device-info">'
        + '<div class="profile-device-label">' + esc(d.label || masked) + '</div>'
        + '<div class="profile-device-meta">UID: ' + esc(masked) + ' \u00B7 ' + esc(d.last_seen) + '</div>'
        + '</div></div>';
    });
  } else {
    html += '<div class="profile-empty">\u041d\u0435\u0442 \u043f\u0440\u0438\u0432\u044f\u0437\u0430\u043d\u043d\u044b\u0445 \u0443\u0441\u0442\u0440\u043e\u0439\u0441\u0442\u0432</div>';
  }
  html += '</div>';

  // \u2500\u2500 Sync profiles (TG-only) \u2014 managed by /api/profile/owned/* \u2500\u2500
  // Only TG-authenticated users have an ownership concept; bkit users
  // who logged in with a kit-token (tgAuth=false) don't see this section.
  if (tgAuth) {
    html += '<div class="profile-card" id="sync-profiles-card">'
      + '<div class="profile-card-title">\ud83d\udd11 \u041f\u0440\u043e\u0444\u0438\u043b\u0438 \u0441\u0438\u043d\u0445\u0440\u043e\u043d\u0438\u0437\u0430\u0446\u0438\u0438</div>'
      + '<div id="sync-profiles-list"><div class="profile-empty">\u0417\u0430\u0433\u0440\u0443\u0437\u043a\u0430\u2026</div></div>'
      + '<button class="sp-add" onclick="syncProfileCreate()">+ \u0421\u043e\u0437\u0434\u0430\u0442\u044c \u043f\u0440\u043e\u0444\u0438\u043b\u044c</button>'
      + '<div class="sp-help">\u041f\u0440\u043e\u0444\u0438\u043b\u044c \u2014 \u044d\u0442\u043e \u043e\u0442\u0434\u0435\u043b\u044c\u043d\u044b\u0439 \u043d\u0430\u0431\u043e\u0440 \u0437\u0430\u043a\u043b\u0430\u0434\u043e\u043a/\u0442\u0430\u0439\u043c\u043a\u043e\u0434\u043e\u0432 (\u041d\u0430\u043f\u0440\u0438\u043c\u0435\u0440 \u00ab\u0414\u0435\u0442\u0438\u00bb, \u00ab\u0416\u0435\u043d\u0430\u00bb). \u041d\u0430 \u0422\u0412 \u0432\u0432\u043e\u0434\u0438\u0442\u0435 \u0438\u043c\u044f + PIN \u0432 \u00ab\u0421\u0438\u043d\u0445\u0440\u043e\u043d\u0438\u0437\u0430\u0446\u0438\u044f \u2192 \u0412\u043e\u0439\u0442\u0438 \u043f\u043e PIN\u00bb.</div>'
      + '</div>';
  }

  html += '</div>';

  el.innerHTML = html;

  if (tgAuth) {
    syncProfileLoad();
  }
}

// \u2500\u2500 Sync profiles (PIN-managed via TG owner) \u2500\u2500
//
// These hit /api/profile/* directly (NOT /api/kit/profile/* via the api()
// helper, which prepends API_BASE). credentials:'include' is what carries
// the lampac_token cookie \u2014 the profile-owned endpoints rely on TG auth.

var syncProfiles = [];

function profileApi(method, path, body) {
  var opts = {method: method, headers: {}, credentials: 'include'};
  if (body) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  return fetch(HOST + '/api/profile' + path, opts).then(function(r) { return r.json(); });
}

function syncProfileLoad() {
  profileApi('GET', '/owned').then(function(d) {
    if (!d || d.error) {
      var el = document.getElementById('sync-profiles-list');
      if (el) el.innerHTML = '<div class="profile-empty">\u041d\u0435\u0434\u043e\u0441\u0442\u0443\u043f\u043d\u043e (\u043d\u0443\u0436\u043d\u0430 \u0430\u0432\u0442\u043e\u0440\u0438\u0437\u0430\u0446\u0438\u044f Telegram)</div>';
      return;
    }
    syncProfiles = d.profiles || [];
    syncProfileRender();
  }).catch(function() {
    var el = document.getElementById('sync-profiles-list');
    if (el) el.innerHTML = '<div class="profile-empty">\u041e\u0448\u0438\u0431\u043a\u0430 \u0437\u0430\u0433\u0440\u0443\u0437\u043a\u0438</div>';
  });
}

function syncProfileRender() {
  var el = document.getElementById('sync-profiles-list');
  if (!el) return;
  if (!syncProfiles.length) {
    el.innerHTML = '<div class="profile-empty">\u041f\u043e\u043a\u0430 \u043d\u0435\u0442 \u043f\u0440\u043e\u0444\u0438\u043b\u0435\u0439</div>';
    return;
  }
  var html = '';
  syncProfiles.forEach(function(sp) {
    var initial = (sp.username || '?').charAt(0).toUpperCase();
    var pinTag = sp.has_pin
      ? '<span class="sp-pin-tag">PIN</span>'
      : '<span class="sp-pin-tag off">\u0431\u0435\u0437 PIN</span>';
    var meta = sp.last_login_at
      ? '\u041f\u043e\u0441\u043b\u0435\u0434\u043d\u0438\u0439 \u0432\u0445\u043e\u0434: ' + esc(formatTimeAgo(sp.last_login_at))
      : '\u0415\u0449\u0451 \u043d\u0435 \u0438\u0441\u043f\u043e\u043b\u044c\u0437\u043e\u0432\u0430\u043b\u0441\u044f';
    html += '<div class="sp-row">'
      + '<div class="sp-icon">' + esc(initial) + '</div>'
      + '<div class="sp-info"><div class="sp-name">' + esc(sp.username) + ' ' + pinTag + '</div>'
      + '<div class="sp-meta">' + meta + '</div></div>'
      + '<div class="sp-actions">'
      + '<button class="btn btn-sm" onclick="syncProfileSetPIN(\'' + esc(sp.id) + '\',\'' + esc(sp.username) + '\')">PIN</button>'
      + '<button class="btn btn-danger btn-sm" onclick="syncProfileDelete(\'' + esc(sp.id) + '\',\'' + esc(sp.username) + '\')">\u2715</button>'
      + '</div></div>';
  });
  el.innerHTML = html;
}

function formatTimeAgo(ms) {
  if (!ms) return '';
  var diff = (Date.now() - ms) / 1000;
  if (diff < 60) return '\u0442\u043e\u043b\u044c\u043a\u043e \u0447\u0442\u043e';
  if (diff < 3600) return Math.floor(diff/60) + ' \u043c\u0438\u043d \u043d\u0430\u0437\u0430\u0434';
  if (diff < 86400) return Math.floor(diff/3600) + ' \u0447 \u043d\u0430\u0437\u0430\u0434';
  return Math.floor(diff/86400) + ' \u0434\u043d. \u043d\u0430\u0437\u0430\u0434';
}

window.syncProfileCreate = function() {
  // Two-prompt sequence keeps the markup simple and avoids a custom modal
  // \u2014 the same pattern the existing kitBind* flows use.
  var name = prompt('\u0418\u043c\u044f \u043f\u0440\u043e\u0444\u0438\u043b\u044f (3-32, \u043b\u0430\u0442\u0438\u043d\u0438\u0446\u0430/\u0446\u0438\u0444\u0440\u044b/_/-):');
  if (!name) return;
  var pin = prompt('PIN \u0434\u043b\u044f \u0432\u0445\u043e\u0434\u0430 \u0441 \u0422\u0412 (4-8 \u0446\u0438\u0444\u0440), \u043f\u0443\u0441\u0442\u043e \u2014 \u0431\u0435\u0437 PIN:');
  if (pin === null) return;
  profileApi('POST', '/owned/create', { username: name.trim(), pin: pin.trim() }).then(function(d) {
    if (d && d.error) { toast('\u041e\u0448\u0438\u0431\u043a\u0430: ' + d.error); return; }
    toast('\u041f\u0440\u043e\u0444\u0438\u043b\u044c \u0441\u043e\u0437\u0434\u0430\u043d');
    syncProfileLoad();
  }).catch(function() { toast('\u041e\u0448\u0438\u0431\u043a\u0430 \u0441\u0435\u0442\u0438'); });
};

window.syncProfileSetPIN = function(id, name) {
  var pin = prompt('\u041d\u043e\u0432\u044b\u0439 PIN \u0434\u043b\u044f "' + name + '" (4-8 \u0446\u0438\u0444\u0440), \u043f\u0443\u0441\u0442\u043e \u2014 \u0443\u0431\u0440\u0430\u0442\u044c PIN:');
  if (pin === null) return;
  profileApi('POST', '/owned/set-pin', { profile_id: id, pin: pin.trim() }).then(function(d) {
    if (d && d.error) { toast('\u041e\u0448\u0438\u0431\u043a\u0430: ' + d.error); return; }
    toast('PIN \u043e\u0431\u043d\u043e\u0432\u043b\u0451\u043d');
    syncProfileLoad();
  }).catch(function() { toast('\u041e\u0448\u0438\u0431\u043a\u0430 \u0441\u0435\u0442\u0438'); });
};

window.syncProfileDelete = function(id, name) {
  if (!confirm('\u0423\u0434\u0430\u043b\u0438\u0442\u044c \u043f\u0440\u043e\u0444\u0438\u043b\u044c "' + name + '"? \u0414\u0430\u043d\u043d\u044b\u0435 \u044d\u0442\u043e\u0433\u043e \u043f\u0440\u043e\u0444\u0438\u043b\u044f \u043e\u0441\u0442\u0430\u043d\u0443\u0442\u0441\u044f \u043d\u0430 \u0441\u0435\u0440\u0432\u0435\u0440\u0435, \u043d\u043e \u0432\u043e\u0439\u0442\u0438 \u0432 \u043d\u0435\u0433\u043e \u0431\u0443\u0434\u0435\u0442 \u043d\u0435\u043b\u044c\u0437\u044f.')) return;
  profileApi('POST', '/owned/delete', { profile_id: id }).then(function(d) {
    if (d && d.error) { toast('\u041e\u0448\u0438\u0431\u043a\u0430: ' + d.error); return; }
    toast('\u041f\u0440\u043e\u0444\u0438\u043b\u044c \u0443\u0434\u0430\u043b\u0451\u043d');
    syncProfileLoad();
  }).catch(function() { toast('\u041e\u0448\u0438\u0431\u043a\u0430 \u0441\u0435\u0442\u0438'); });
};

function pluralDays(n) {
  if (n % 10 === 1 && n % 100 !== 11) return '\u0434\u0435\u043d\u044c';
  if (n % 10 >= 2 && n % 10 <= 4 && (n % 100 < 10 || n % 100 >= 20)) return '\u0434\u043d\u044f';
  return '\u0434\u043d\u0435\u0439';
}

// ── Render ──

function render() {
  // Binds tab disabled by admin: leave it empty/hidden and land on Балансеры.
  if (bindsDisabled) {
    var tb = document.getElementById('tab-binds');
    if (tb) { tb.innerHTML = ''; tb.style.display = 'none'; }
    if (activeTab === 'binds') switchTab('balancers');
    else {
      document.getElementById('tab-' + activeTab).style.display = '';
      if (activeTab === 'balancers' && !balancerData) loadBalancers();
    }
    updateSaveBar();
    if (balancerData) renderBalancers();
    return;
  }

  var h = '';

  // Bind section
  h += '<div class="section-title">' + L.bindServices + '</div>';
  bindServices.forEach(function(s) {
    var sec = config[sectionKey(s.id)] || {};
    var bound = sec.enable && (sec.token || sec.cookie);
    h += '<div class="card">';
    h += '<div class="card-info"><div class="card-name">' + s.name + '</div>';
    h += '<div class="card-status' + (bound ? ' bound' : '') + '">' + (bound ? L.bound : L.notBound) + '</div></div>';
    h += '<span class="badge ' + (s.badge === 'Free' ? 'badge-free' : 'badge-premium') + '">' + s.badge + '</span>';
    if (bound) {
      h += ' <button class="btn btn-danger btn-sm" onclick="unbind(\'' + s.id + '\')">✕</button>';
    } else {
      h += ' <button class="btn btn-primary btn-sm" onclick="startBind(\'' + s.id + '\')">' + L.bind + '</button>';
    }
    h += '</div>';
  });

  // Sources section
  h += '<div class="section-title">' + L.sources + '</div>';
  tokenBalancers.forEach(function(b) {
    var sec = config[b.key] || {};
    var enabled = !!sec.enable;
    var tok = sec.token || sec.cookie || '';
    h += '<div class="toggle-row">';
    h += '<div><div class="toggle-label">' + b.name + '</div>';
    if (tok) h += '<div class="toggle-sub">' + tok.substring(0, 16) + (tok.length > 16 ? '…' : '') + '</div>';
    h += '</div>';
    h += '<label class="switch"><input type="checkbox" ' + (enabled ? 'checked' : '') + ' onchange="toggleTokenBal(\'' + b.key + '\',this.checked)"><span class="slider"></span></label>';
    h += '</div>';
    h += '<div class="token-row"><input class="token-input" placeholder="' + L.tokenPlaceholder + '" value="' + esc(tok) + '" onchange="setTok(\'' + b.key + '\',this.value)"></div>';
  });

  document.getElementById('tab-binds').innerHTML = h;
  updateSaveBar();
  if (balancerData) renderBalancers();
}

// ── Token balancer toggles ──

window.toggleTokenBal = function(key, on) {
  if (!config[key]) config[key] = {};
  config[key].enable = on;
  hasChanges = true;
  updateSaveBar();
};

window.setTok = function(key, val) {
  if (!config[key]) config[key] = {};
  val = val.trim();
  if (val) { config[key].token = val; config[key].enable = true; }
  else { delete config[key].token; }
  hasChanges = true;
  updateSaveBar();
};

// ── Save ──

window.save = function() {
  var btn = document.querySelector('.save-btn');
  btn.innerHTML = '<span class="spinner"></span>';
  syncVisToConfig();
  api('POST', '/config', config).then(function(r) {
    if (r.success) { toast(L.saved); hasChanges = false; updateSaveBar(); }
    else toast(L.error + ': ' + (r.error || ''));
  }).catch(function() { toast(L.saveErr); }).finally(function() { btn.textContent = L.save; });
};

function updateSaveBar() {
  document.getElementById('save-bar').classList.toggle('active', hasChanges);
}

function syncVisToConfig() {
  if (Object.keys(balancerVisibility).length > 0) config['_balancerVisibility'] = balancerVisibility;
  else delete config['_balancerVisibility'];
}

// ── Balancers ──

function loadBalancers() {
  api('GET', '/balancers').then(function(data) {
    if (data.error) { document.getElementById('tab-balancers').innerHTML = '<div class="loading">' + esc(data.error) + '</div>'; return; }
    balancerData = data;
    renderBalancers();
  }).catch(function() { document.getElementById('tab-balancers').innerHTML = '<div class="loading">' + L.error + '</div>'; });
}

function getVis(key, eff) { return balancerVisibility.hasOwnProperty(key) ? balancerVisibility[key] : eff; }

function renderBalancers() {
  var el = document.getElementById('tab-balancers');
  if (!el || !balancerData) return;
  var h = '';
  balancerData.groups.forEach(function(group) {
    var bals = balancerData.balancers.filter(function(b) { return b.group === group.key; });
    if (!bals.length) return;
    var cnt = 0;
    bals.forEach(function(b) { var eff = b.globalEnabled || b.userBound; if (getVis(b.key, eff)) cnt++; });
    var allOn = cnt === bals.length;
    h += '<div class="group-header"><div class="group-title"><span>' + group.icon + '</span> ' + esc(group.label);
    h += '<span class="group-count">' + cnt + '/' + bals.length + '</span></div>';
    h += '<label class="switch"><input type="checkbox" ' + (allOn ? 'checked' : '') + ' onchange="toggleGrp(\'' + group.key + '\',this.checked)"><span class="slider"></span></label></div>';
    bals.forEach(function(b) {
      var eff = b.globalEnabled || b.userBound;
      var vis = getVis(b.key, eff);
      var off = !b.globalEnabled && !b.userBound;
      h += '<div class="bal-row' + (off ? ' disabled' : '') + '">';
      h += '<div><span class="bal-name">' + esc(b.name) + '</span>';
      if (b.quality) h += '<span class="bal-quality">' + b.quality + '</span>';
      if (b.userBound && !b.globalEnabled) h += '<span class="bal-note" style="color:var(--success)">' + L.userBound + '</span>';
      else if (off) h += '<span class="bal-note">' + L.globallyOff + '</span>';
      h += '</div>';
      h += '<label class="switch"><input type="checkbox" ' + (vis ? 'checked' : '') + (off ? ' disabled' : '') + ' onchange="toggleBalVis(\'' + b.key + '\',this.checked)"><span class="slider"></span></label></div>';
    });
  });
  el.innerHTML = h;
}

window.toggleBalVis = function(key, on) { balancerVisibility[key] = on; hasChanges = true; updateSaveBar(); renderBalancers(); };
window.toggleGrp = function(gk, on) { if (!balancerData) return; balancerData.balancers.forEach(function(b) { if (b.group === gk) balancerVisibility[b.key] = on; }); hasChanges = true; updateSaveBar(); renderBalancers(); };

// ── Unbind ──

window.unbind = function(sid) {
  if (!confirm(L.confirmUnbind)) return;
  api('DELETE', '/bind/' + sid).then(function(r) {
    if (r.success) { delete config[sectionKey(sid)]; toast(L.unbound); render(); }
  });
};

// ── Bind flows ──

window.startBind = function(sid) {
  var svc = bindServices.find(function(s) { return s.id === sid; });
  if (!svc) return;
  if (svc.type === 'device') startDeviceBind(svc);
  else if (svc.type === 'login') startLoginBind(svc);
  else if (svc.type === 'apikey') startApiKeyBind(svc);
};

function startDeviceBind(svc) {
  showModal('<h3>' + L.bindTitle + ' ' + svc.name + '</h3><div class="loading"><span class="spinner"></span> ' + L.requesting + '</div>');
  api('POST', '/bind/' + svc.id + '/start').then(function(r) {
    if (r.error) { showModal('<h3>' + L.error + '</h3><p>' + r.error + '</p>'); return; }
    var url = svc.id === 'filmix' ? 'https://filmix.my/consoles' : 'https://kino.watch/device';
    var h = '<h3>' + L.bindTitle + ' ' + svc.name + '</h3>';
    h += '<div class="device-steps">' + L.step1 + ' <a href="' + url + '" target="_blank">' + url + '</a></div>';
    h += '<div class="device-steps">' + L.step2 + '</div>';
    h += '<div class="device-code">' + r.user_code + '</div>';
    h += '<div class="device-steps">' + L.step3 + '</div>';
    h += '<button class="btn btn-primary" style="width:100%;margin-top:16px" id="finish-btn" onclick="finishDevice(\'' + svc.id + '\',\'' + esc(r.code) + '\')">' + L.finish + '</button>';
    showModal(h);
  }).catch(function() { showModal('<h3>' + L.error + '</h3><p>' + L.unavailable + '</p>'); });
}

window.finishDevice = function(sid, code) {
  var btn = document.getElementById('finish-btn');
  if (btn) btn.innerHTML = '<span class="spinner"></span>';
  api('POST', '/bind/' + sid + '/finish', {code: code}).then(function(r) {
    if (r.success) { closeModal(); load(); toast(L.boundOk); }
    else showModal('<h3>' + L.error + '</h3><p>' + (r.error || L.bindFail) + '</p><p style="color:var(--text3)">' + L.bindHint + '</p>');
  });
};

function startLoginBind(svc) {
  var h = '<h3>' + L.bindTitle + ' ' + svc.name + '</h3>';
  h += '<form onsubmit="submitLogin(event,\'' + svc.id + '\')">';
  h += '<div class="form-group"><label>' + L.email + '</label><input type="text" id="bind-login" required></div>';
  h += '<div class="form-group"><label>' + L.password + '</label><input type="password" id="bind-pass" required></div>';
  h += '<button type="submit" class="btn btn-primary" style="width:100%" id="bind-submit">' + L.bind + '</button>';
  h += '</form>';
  showModal(h);
}

window.submitLogin = function(e, sid) {
  e.preventDefault();
  var login = document.getElementById('bind-login').value;
  var pass = document.getElementById('bind-pass').value;
  var btn = document.getElementById('bind-submit');
  if (btn) btn.innerHTML = '<span class="spinner"></span>';
  api('POST', '/bind/' + sid, {login: login, password: pass}).then(function(r) {
    if (r.success) { closeModal(); load(); toast(L.boundOk); }
    else { toast(L.error + ': ' + (r.error || '')); if (btn) btn.textContent = L.bind; }
  }).catch(function() { toast(L.connectionError); if (btn) btn.textContent = L.bind; });
};

function startApiKeyBind(svc) {
  var h = '<h3>' + L.bindTitle + ' ' + svc.name + '</h3>';
  h += '<div class="device-steps">' + L.dataFrom + ' <a href="https://iptv.online/ru/dealers/api" target="_blank">iptv.online/ru/dealers/api</a></div>';
  h += '<form onsubmit="submitApiKey(event)">';
  h += '<div class="form-group"><label>X-API-KEY</label><input type="text" id="bind-apikey" required></div>';
  h += '<div class="form-group"><label>X-API-ID</label><input type="text" id="bind-apiid" required></div>';
  h += '<button type="submit" class="btn btn-primary" style="width:100%">' + L.save + '</button>';
  h += '</form>';
  showModal(h);
}

window.submitApiKey = function(e) {
  e.preventDefault();
  var key = document.getElementById('bind-apikey').value;
  var id = document.getElementById('bind-apiid').value;
  api('POST', '/bind/iptvonline', {api_key: key, api_id: id}).then(function(r) {
    if (r.success) { closeModal(); load(); toast(L.boundOk); }
    else toast(L.error + ': ' + (r.error || ''));
  });
};

// ── Modal ──

function showModal(html) {
  document.getElementById('modal-content').innerHTML = '<button class="close" onclick="closeModal()">&times;</button>' + html;
  document.getElementById('modal-overlay').classList.add('active');
}
window.closeModal = function() { document.getElementById('modal-overlay').classList.remove('active'); };

// ── Toast ──

function toast(msg) {
  var el = document.getElementById('toast');
  el.textContent = msg;
  el.classList.add('show');
  setTimeout(function() { el.classList.remove('show'); }, 2800);
}

// ── Enter key on login ──
document.getElementById('login-token').addEventListener('keydown', function(e) {
  if (e.key === 'Enter') doLogin();
});

})();
</script>
</body>
</html>`
