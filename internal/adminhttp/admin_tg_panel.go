package adminhttp

import (
	"net/http"
	"strings"

	"lampac-go/internal/tgauth"
)

// --- Admin Panel SPA page handler + embedded HTML ---
// All Go API handlers have been extracted to separate files:
//   admin_panel_auth.go      — auth, path gen, whoami
//   admin_panel_users.go     — users API
//   admin_panel_balancers.go — balancers data + API
//   admin_panel_plugins.go   — plugins API
//   admin_panel_telegram.go  — TG settings, broadcast, admins
//   admin_panel_proxy.go     — VLESS proxy API
//   admin_panel_config.go    — legacy JSON config API
//   admin_panel_helpers.go   — shared helpers
//   admin_config_toml.go     — TOML config editor API

func tgAdminPageHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, llmEnabled bool) http.HandlerFunc {
	// Inject _llmEnabled flag into the page so the JS knows whether to show LLM button.
	var pageHTML string
	if llmEnabled {
		pageHTML = strings.Replace(adminPanelHTML, "</head>", `<script>window._llmEnabled=true</script></head>`, 1)
	} else {
		pageHTML = adminPanelHTML
	}
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		writeHTML(w, http.StatusOK, pageHTML)
	}
}

// --- Admin SPA HTML ---

const adminPanelHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Alpac Admin</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=Montserrat:wght@400;500;600;700&display=swap');
*{margin:0;padding:0;box-sizing:border-box}
:root{--accent:#06b6d4;--accent-dark:#0d9488;--accent-hover:#0891b2;--accent-hover-dark:#0f766e;--bg:#161b23;--surface:#1a2332;--surface2:#1e2736;--surface3:#11161d;--text:#e2e8f0;--text-muted:#94a3b8;--text-dim:#64748b;--border:rgba(255,255,255,0.06);--danger:#ff6b6b;--warning:#f0c040;--accent-a06:rgba(6,182,212,0.06);--accent-a08:rgba(6,182,212,0.08);--accent-a12:rgba(6,182,212,0.12);--accent-a13:rgba(6,182,212,0.13);--accent-a15:rgba(6,182,212,0.15);--accent-a20:rgba(13,148,136,0.2);--accent-a35:rgba(13,148,136,0.35);--accent-a25:rgba(6,182,212,0.25);--accent-a30:rgba(6,182,212,0.3);--accent-a10:rgba(6,182,212,0.1);--accent-a05:rgba(6,182,212,0.05);--accent-a04:rgba(6,182,212,0.04);--accent-glow:0 0 6px var(--accent);--danger-bg:#ff6b6b22;--danger-border:#ff6b6b44;--warning-bg:#f0c04022;--warning-border:#f0c04044}
::-webkit-scrollbar{width:6px;height:6px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:linear-gradient(180deg,var(--accent-dark),var(--accent));border-radius:3px}
::-webkit-scrollbar-thumb:hover{background:linear-gradient(180deg,var(--accent-hover-dark),var(--accent-hover))}
body{font-family:'Montserrat',-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--bg);color:var(--text);min-height:100vh}
.app-layout{display:flex;min-height:100vh}
.sidebar{width:220px;background:linear-gradient(180deg,var(--surface) 0%,var(--bg) 100%);border-right:none;display:flex;flex-direction:column;flex-shrink:0;position:fixed;top:12px;left:12px;bottom:12px;z-index:100;overflow-y:auto;overflow-x:hidden;transition:transform .25s;border-radius:16px;box-shadow:0 4px 24px rgba(0,0,0,0.4),0 1px 4px rgba(0,0,0,0.2)}
.sidebar-logo{padding:20px 18px 16px;display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--border)}
.sidebar-logo h1{font-size:18px;color:var(--accent);font-weight:700;letter-spacing:-.3px}
.sidebar-logo .logo-icon{width:28px;height:28px;background:var(--accent-a12);border-radius:8px;display:flex;align-items:center;justify-content:center;font-size:15px;color:var(--accent);flex-shrink:0}
.sidebar-logo .theme-btn{margin-left:auto;width:26px;height:26px;background:var(--accent-a06);border:1px solid var(--accent-a30);border-radius:7px;display:flex;align-items:center;justify-content:center;font-size:14px;cursor:pointer;transition:all .2s;color:var(--accent);flex-shrink:0}
.sidebar-logo .theme-btn:hover{background:var(--accent-a15);transform:scale(1.1)}
.sidebar-section{padding:10px 14px 6px;font-size:10px;color:var(--text-dim);text-transform:uppercase;letter-spacing:1.2px;font-weight:600;margin-top:4px;cursor:pointer;display:flex;align-items:center;gap:6px;user-select:none;transition:color .2s ease;border-radius:6px}.sidebar-section:first-child{margin-top:0}.sidebar-section:hover{color:var(--text-muted)}.sidebar-section .sec-icon{font-size:12px;opacity:.7}.sidebar-section .sec-chevron{font-size:8px;transition:transform .2s ease;margin-left:auto;opacity:.6}.sidebar-section.open .sec-chevron{transform:rotate(90deg)}.nav-group{display:none;flex-direction:column;gap:2px}.nav-group.open{display:flex}
.tabs{display:flex;flex-direction:column;gap:2px;padding:4px 8px;flex:1}
.tab{display:flex;align-items:center;gap:10px;padding:9px 12px;cursor:pointer;color:var(--text-muted);border-radius:10px;font-size:13px;transition:all .2s ease;white-space:nowrap;border:none;border-left:3px solid transparent;user-select:none}
.tab:hover{background:var(--accent-a06);color:var(--text);transform:translateX(4px)}
.tab.active{background:linear-gradient(135deg,rgba(13,148,136,0.15),var(--accent-a08));color:var(--accent);border-left-color:var(--accent);box-shadow:inset 0 0 20px var(--accent-a05)}
.tab .tab-icon{width:20px;text-align:center;font-size:15px;flex-shrink:0;opacity:.7}
.tab.active .tab-icon{opacity:1}
.sidebar-footer{padding:12px 14px;border-top:1px solid var(--border);margin-top:auto}
.sidebar-footer .logout{color:var(--text-muted);text-decoration:none;font-size:12px;display:flex;align-items:center;gap:8px;padding:6px 8px;border-radius:6px;transition:all .2s ease}
.sidebar-footer .logout:hover{color:var(--danger);background:rgba(255,107,107,0.05)}
.main-area{margin-left:244px;flex:1;min-width:0;display:flex;flex-direction:column;min-height:100vh}
.top-bar{padding:12px 24px;display:flex;align-items:center;gap:12px;border-bottom:1px solid var(--border);background:var(--bg)}
.top-bar .page-title{font-size:15px;color:var(--text);font-weight:600}
.restart-banner{display:none;background:var(--warning-bg);color:var(--warning);border:1px solid var(--warning-border);padding:10px 24px;font-size:13px;text-align:center}
.restart-banner.show{display:block}
.mobile-toggle{display:none;background:none;border:none;color:var(--text-muted);font-size:22px;cursor:pointer;padding:4px 8px;border-radius:6px}
.mobile-toggle:hover{background:var(--surface2);color:var(--text)}
.sidebar-overlay{display:none;position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:99;backdrop-filter:blur(4px)}
@media(max-width:768px){
.sidebar{transform:translateX(-100%);width:260px;margin:0;border-radius:0 16px 16px 0;box-shadow:4px 0 24px rgba(0,0,0,0.5);top:0;bottom:0}
.sidebar.open{transform:translateX(0)}
.sidebar-overlay.open{display:block}
.main-area{margin-left:0}
.mobile-toggle{display:block}
}
/* Theme modal */
.theme-overlay{display:none;position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:1100;backdrop-filter:blur(6px);align-items:center;justify-content:center}
.theme-overlay.open{display:flex}
.theme-modal{background:var(--surface);border:1px solid var(--border);border-radius:16px;width:380px;max-width:90vw;max-height:85vh;overflow-y:auto;box-shadow:0 12px 48px rgba(0,0,0,.5);animation:tmIn .2s ease}
@keyframes tmIn{from{opacity:0;transform:scale(.95)}to{opacity:1;transform:scale(1)}}
.theme-modal-header{padding:16px 20px;border-bottom:1px solid var(--border);display:flex;align-items:center;gap:10px}
.theme-modal-header h2{font-size:15px;color:var(--text);font-weight:600;flex:1}
.theme-modal-close{background:none;border:none;color:var(--text-muted);font-size:18px;cursor:pointer;padding:4px 8px;border-radius:6px;transition:all .15s}
.theme-modal-close:hover{color:var(--danger);background:rgba(255,107,107,.08)}
.theme-modal-body{padding:16px 20px}
.theme-presets{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:16px}
.theme-preset{padding:6px 12px;border-radius:8px;font-size:12px;cursor:pointer;border:1px solid var(--border);background:var(--surface2);color:var(--text-muted);transition:all .2s;font-weight:500}
.theme-preset:hover{border-color:var(--accent);color:var(--text)}
.theme-preset.active{border-color:var(--accent);color:var(--accent);background:var(--accent-a12)}
.theme-colors{display:flex;flex-direction:column;gap:10px}
.theme-color-row{display:flex;align-items:center;gap:10px}
.theme-color-row label{font-size:12px;color:var(--text-muted);width:100px;flex-shrink:0}
.theme-color-row input[type=color]{width:36px;height:28px;border:1px solid var(--border);border-radius:6px;cursor:pointer;background:transparent;padding:0}
.theme-color-row input[type=color]::-webkit-color-swatch-wrapper{padding:2px}
.theme-color-row input[type=color]::-webkit-color-swatch{border:none;border-radius:3px}
.theme-color-row .color-hex{font-size:11px;color:var(--text-dim);font-family:'SF Mono',monospace;width:60px}
.theme-modal-footer{padding:12px 20px;border-top:1px solid var(--border);display:flex;gap:8px;justify-content:flex-end}
.theme-modal-footer .btn{padding:7px 16px;font-size:12px;font-weight:600;border-radius:8px}
.content{padding:24px;max-width:1400px;margin:0 auto;flex:1}
.panel{display:none}
.panel.active{display:block}
.insp-cat.open .insp-cat-body{display:block!important}
.insp-cat.open .insp-chevron{transform:rotate(90deg)}
.dep-grp.open .dep-grp-body{display:block!important}
.dep-grp.open .dep-chevron{transform:rotate(90deg)}
.dep-grp-hdr:hover{background:rgba(30,36,46,0.9)!important}
.insp-cat-hdr:hover{background:rgba(30,35,45,0.9)!important}
table{width:100%;border-collapse:collapse;margin-top:16px}
th,td{padding:10px 12px;text-align:left;border-bottom:1px solid var(--border);font-size:13px}
th{color:var(--text-muted);font-weight:500;font-size:12px;text-transform:uppercase}
tr:hover{background:var(--accent-a04)}
.expired{color:var(--danger)}
.active-user{color:var(--accent)}
.btn{padding:6px 14px;border:none;border-radius:6px;cursor:pointer;font-size:12px;font-weight:500;transition:all .2s}
.btn-danger{background:var(--danger-bg);color:var(--danger);border:1px solid var(--danger-border)}
.btn-danger:hover{background:var(--danger-border)}
.btn-primary{background:linear-gradient(135deg,var(--accent-a20),var(--accent-a15));color:var(--accent);border:1px solid var(--accent-a30);transition:all .2s ease}
.btn-primary:hover{background:linear-gradient(135deg,var(--accent-a35),var(--accent-a25));box-shadow:0 2px 12px var(--accent-a15)}
.btn-warning{background:var(--warning-bg);color:var(--warning);border:1px solid var(--warning-border)}
.btn-warning:hover{background:var(--warning-border)}
.btn-sm{padding:4px 10px;font-size:11px}
.actions{display:flex;gap:6px;flex-wrap:wrap}
.toggle{position:relative;width:42px;height:22px;cursor:pointer;display:inline-block}
.toggle input{display:none}
.toggle .slider{position:absolute;top:0;left:0;right:0;bottom:0;background:#2d3748;border-radius:11px;transition:.3s}
.toggle .slider:before{content:'';position:absolute;width:16px;height:16px;left:3px;bottom:3px;background:var(--text-muted);border-radius:50%;transition:.3s}
.toggle input:checked+.slider{background:linear-gradient(135deg,var(--accent-dark),var(--accent))}
.toggle input:checked+.slider:before{transform:translateX(20px);background:#fff}
.input-sm{background:var(--surface2);border:1px solid var(--border);color:var(--text);padding:6px 10px;border-radius:6px;font-size:12px;width:200px;transition:all .2s ease}
.input-sm:focus{outline:none;border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-a10)}
.input-wide{width:300px}
textarea.config-editor{width:100%;min-height:500px;background:var(--surface2);border:1px solid var(--border);color:var(--text);padding:16px;border-radius:8px;font-family:'Courier New',monospace;font-size:13px;resize:vertical;margin-top:12px}
textarea.config-editor:focus{outline:none;border-color:var(--accent)}
.config-actions{margin-top:12px;display:flex;gap:12px}
/* Config file navigator */
.cfg-layout{display:flex;gap:0;margin-top:12px;border:1px solid var(--border);border-radius:10px;overflow:hidden;background:var(--surface3)}
.cfg-nav{width:220px;flex-shrink:0;border-right:1px solid var(--border);background:var(--surface3);overflow-y:auto}
.cfg-nav-header{padding:10px 14px;font-size:11px;color:var(--text-dim);text-transform:uppercase;letter-spacing:.8px;font-weight:600;border-bottom:1px solid var(--surface2)}
.cfg-nav-item{display:flex;align-items:center;gap:8px;padding:8px 14px;cursor:pointer;color:var(--text-muted);font-size:12px;transition:all .15s;border-left:3px solid transparent;user-select:none}
.cfg-nav-item:hover{background:var(--bg);color:var(--text)}
.cfg-nav-item.active{background:var(--bg);color:var(--accent);border-left-color:var(--accent);font-weight:600}
.cfg-nav-item .nav-icon{font-size:14px;width:20px;text-align:center;flex-shrink:0}
.cfg-nav-item .nav-label{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.cfg-nav-item .nav-count{font-size:10px;color:var(--text-dim);background:var(--surface2);padding:1px 6px;border-radius:8px}
.cfg-nav-sep{height:1px;background:var(--surface2);margin:4px 0}
.cfg-editor-main{flex:1;min-width:0;display:flex;flex-direction:column}
@media(max-width:900px){.cfg-layout{flex-direction:column}.cfg-nav{width:100%;max-height:200px;border-right:none;border-bottom:1px solid var(--border)}.cfg-nav-header{display:none}.cfg-nav-item{display:inline-flex;padding:6px 12px;border-left:none;border-bottom:3px solid transparent}.cfg-nav-item.active{border-bottom-color:var(--accent);border-left-color:transparent}}
/* TOML Editor */
.toml-editor-wrap{position:relative;background:var(--surface3);flex:1}
.toml-editor-wrap.has-error{border-color:var(--danger)}
.toml-editor-wrap.is-valid{border-color:var(--accent-a30)}
.toml-toolbar{display:flex;align-items:center;gap:8px;padding:8px 12px;background:var(--bg);border-bottom:1px solid var(--border);flex-wrap:wrap}
.toml-toolbar .toml-badge{font-size:11px;padding:3px 8px;border-radius:4px;font-weight:600;letter-spacing:.5px}
.toml-badge-toml{background:var(--warning-bg);color:var(--warning)}
.toml-badge-source{background:var(--border);color:var(--text-muted);font-size:10px}
.toml-toolbar-spacer{flex:1}
.toml-status{font-size:11px;display:flex;align-items:center;gap:5px}
.toml-status .dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.toml-status .dot.green{background:var(--accent)}
.toml-status .dot.red{background:var(--danger)}
.toml-status .dot.yellow{background:var(--warning)}
.toml-editor-container{display:flex;position:relative;height:500px;overflow:hidden}
.toml-line-numbers{width:44px;padding:12px 6px 12px 8px;background:var(--surface3);color:var(--text-dim);font-family:'SF Mono','Fira Code','Courier New',monospace;font-size:13px;line-height:1.5;text-align:right;user-select:none;flex-shrink:0;border-right:1px solid var(--surface2);overflow-y:hidden}
.toml-code-area{position:relative;flex:1;min-width:0}
.toml-highlight{position:absolute;top:0;left:0;right:0;bottom:0;padding:12px;font-family:'SF Mono','Fira Code','Courier New',monospace;font-size:13px;line-height:1.5;white-space:pre-wrap;word-wrap:break-word;overflow:hidden;pointer-events:none;z-index:0}
.toml-textarea{display:block;width:100%;height:100%;padding:12px;background:transparent;caret-color:var(--text);font-family:'SF Mono','Fira Code','Courier New',monospace;font-size:13px;line-height:1.5;border:none;resize:none;outline:none;tab-size:4;-moz-tab-size:4;white-space:pre-wrap;word-wrap:break-word;overflow-y:auto;position:relative;z-index:1;color:transparent;-webkit-text-fill-color:transparent}
/* TOML syntax colors */
.toml-highlight .t-comment{color:#6a737d;font-style:italic}
.toml-highlight .t-table{color:#d2a8ff;font-weight:600}
.toml-highlight .t-key{color:#79c0ff}
.toml-highlight .t-eq{color:#8b949e}
.toml-highlight .t-string{color:#a5d6ff}
.toml-highlight .t-number{color:var(--accent)}
.toml-highlight .t-bool{color:#ff7b72;font-weight:600}
.toml-highlight .t-array{color:var(--warning)}
.toml-highlight .t-error-line{background:var(--danger-bg);display:block;margin:0 -12px;padding:0 12px;border-left:3px solid var(--danger)}
/* Config action bar */
.cfg-action-bar{display:flex;gap:10px;align-items:center;margin-top:12px;flex-wrap:wrap}
.cfg-action-bar .btn{padding:8px 18px;font-size:13px;font-weight:600}
.cfg-error-panel{background:rgba(255,107,107,0.08);border:1px solid var(--danger-border);border-radius:8px;padding:12px 16px;margin-top:12px;display:none}
.cfg-error-panel.show{display:block}
.cfg-error-panel .error-title{color:var(--danger);font-size:13px;font-weight:600;margin-bottom:4px}
.cfg-error-panel .error-detail{color:#e6828d;font-size:12px;font-family:'SF Mono',monospace;white-space:pre-wrap}
/* Backup panel */
.cfg-backups-panel{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:10px;margin-top:16px;overflow:hidden}
.cfg-backups-header{padding:12px 16px;display:flex;align-items:center;gap:10px;cursor:pointer;user-select:none}
.cfg-backups-header:hover{background:var(--surface2)}
.cfg-backups-title{flex:1;font-size:14px;font-weight:600;color:var(--text)}
.cfg-backups-count{font-size:11px;color:var(--text-muted);background:var(--surface3);padding:2px 8px;border-radius:10px}
.cfg-backups-body{display:none;border-top:1px solid var(--border);max-height:300px;overflow-y:auto}
.cfg-backups-body.open{display:block}
.cfg-backup-item{display:flex;align-items:center;gap:12px;padding:10px 16px;border-bottom:1px solid var(--surface2);font-size:12px;transition:background .15s}
.cfg-backup-item:last-child{border-bottom:none}
.cfg-backup-item:hover{background:var(--surface3)}
.cfg-backup-item .bk-name{flex:1;color:var(--text-muted);font-family:'SF Mono',monospace;font-size:11px}
.cfg-backup-item .bk-date{color:var(--text-dim);font-size:11px;width:130px}
.cfg-backup-item .bk-size{color:var(--text-dim);font-size:11px;width:60px;text-align:right}
.cfg-backup-item .btn{padding:4px 12px;font-size:11px}
/* Config diff indicator */
.cfg-modified-dot{width:8px;height:8px;border-radius:50%;background:var(--warning);display:none;margin-left:6px}
.cfg-modified-dot.show{display:inline-block}
.toast{position:fixed;bottom:24px;right:24px;padding:12px 20px;border-radius:12px;font-size:13px;opacity:0;transition:opacity .3s;z-index:1000;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);box-shadow:0 8px 32px rgba(0,0,0,0.3)}
.toast.show{opacity:1}
.toast.success{background:var(--accent-a15);color:var(--accent);border:1px solid var(--accent-a30)}
.toast.error{background:var(--danger-bg);color:var(--danger);border:1px solid var(--danger-border)}
.toast.warning{background:#ffd93d22;color:#ffd93d;border:1px solid #ffd93d44}
.section-title{font-size:16px;color:var(--text);margin-bottom:8px}
.section-desc{font-size:13px;color:var(--text-muted);margin-bottom:16px}
.empty{text-align:center;padding:40px;color:var(--text-muted)}
.badge{display:inline-block;padding:2px 8px;border-radius:4px;font-size:11px;font-weight:500;margin-left:6px}
.badge-working{background:var(--accent-a13);color:var(--accent)}
.badge-dead{background:var(--danger-bg);color:var(--danger)}
.badge-blocked{background:var(--danger-bg);color:#ff9b9b}
.badge-intermittent{background:var(--warning-bg);color:var(--warning)}
.badge-needs_token{background:#a78bfa22;color:#a78bfa}
.badge-search_only{background:#60a5fa22;color:#60a5fa}
.balancer-card{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:12px;margin:8px 0;overflow:hidden;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);transition:all .2s ease}
.balancer-header{padding:12px 16px;display:flex;align-items:center;gap:12px;cursor:pointer}
.balancer-header:hover{background:var(--surface2)}
.balancer-name{font-weight:500;flex:1}
.balancer-body{display:none;padding:16px;border-top:1px solid var(--border);background:var(--bg)}
.balancer-body.open{display:block}
.field-row{display:flex;align-items:center;gap:8px;margin:6px 0;flex-wrap:wrap}
.field-key{color:var(--text-muted);font-size:12px;width:160px;flex-shrink:0}
.field-val{flex:1}
/* Balancer groups */
.bal-group{margin-bottom:24px}
.bal-group-header{display:flex;align-items:center;gap:10px;padding:10px 4px;cursor:pointer;user-select:none;border-bottom:1px solid var(--border);margin-bottom:8px}
.bal-group-header:hover{background:var(--surface2);border-radius:8px}
.bal-group-icon{font-size:20px;line-height:1}
.bal-group-title{font-size:15px;font-weight:600;color:var(--text);flex:1}
.bal-group-count{font-size:12px;color:var(--text-muted);background:var(--surface2);border:1px solid var(--border);padding:2px 10px;border-radius:12px}
.bal-group-toggle{display:flex;align-items:center;gap:6px;padding:3px 10px;border-radius:6px;border:1px solid var(--border);background:transparent;color:var(--text-muted);font-size:11px;cursor:pointer;transition:all .2s}
.bal-group-toggle:hover{border-color:var(--accent);color:var(--accent)}
.bal-group-toggle.all-on{background:var(--accent-a13);color:var(--accent);border-color:var(--accent-a30)}
.bal-group-chevron{font-size:12px;color:var(--text-muted);transition:transform .2s}
.bal-group-chevron.open{transform:rotate(90deg)}
.bal-group-body{display:none}
.bal-group-body.open{display:block}
.badge-quality{display:inline-block;padding:2px 7px;border-radius:4px;font-size:10px;font-weight:600;margin-left:6px;letter-spacing:.5px}
.badge-quality-4K{background:linear-gradient(135deg,#f0c040,#ff8c00);color:#161b23}
.badge-quality-FHD{background:var(--accent-a20);color:var(--accent)}
.badge-quality-SD{background:rgba(148,163,184,0.2);color:var(--text-muted)}
.bal-search-wrap{margin-bottom:16px}
.bal-search{width:100%;background:var(--surface2);border:1px solid var(--border);color:var(--text);padding:10px 14px;border-radius:8px;font-size:13px}
.bal-search:focus{outline:none;border-color:var(--accent)}
.bal-search::placeholder{color:var(--text-dim)}
.reorder-item{display:flex;align-items:center;gap:8px;padding:6px 10px;margin:4px 0;background:var(--surface2);border:1px solid var(--border);border-radius:6px;font-size:13px;cursor:grab}
.reorder-item:active{cursor:grabbing}
.reorder-item .reorder-name{flex:1;color:var(--text)}
.reorder-item .reorder-btns{display:flex;gap:4px}
.reorder-item .reorder-btns button{background:transparent;border:1px solid var(--border);color:var(--text-muted);border-radius:4px;cursor:pointer;padding:2px 8px;font-size:14px;line-height:1}
.reorder-item .reorder-btns button:hover{border-color:var(--accent);color:var(--accent)}
.reorder-item .reorder-num{color:var(--text-muted);font-size:11px;min-width:20px;text-align:right}
.reorder-item.dragging{opacity:0.4;border-color:var(--accent)}
/* Plugin cards */
.pg-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:12px;margin:16px 0}
.pg-card{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:12px;padding:14px 16px;display:flex;align-items:flex-start;gap:12px;transition:all .2s ease;position:relative;cursor:default;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px)}
.pg-card:hover{border-color:var(--accent-a20);box-shadow:0 4px 20px var(--accent-a05)}
.pg-card.active{border-left:3px solid var(--accent)}
.pg-card.inactive{opacity:.55}
.pg-card .pg-icon{font-size:24px;width:40px;height:40px;display:flex;align-items:center;justify-content:center;background:var(--bg);border-radius:10px;flex-shrink:0}
.pg-card .pg-info{flex:1;min-width:0}
.pg-card .pg-name{font-size:14px;font-weight:600;color:var(--text)}
.pg-card .pg-desc{font-size:11px;color:var(--text-muted);margin-top:3px;line-height:1.4}
.pg-card .pg-actions{display:flex;align-items:center;gap:8px;flex-shrink:0;margin-top:2px}
.pg-gear{width:28px;height:28px;border-radius:6px;border:1px solid var(--border);background:transparent;color:var(--text-muted);font-size:14px;cursor:pointer;display:flex;align-items:center;justify-content:center;transition:all .2s}
.pg-gear:hover{border-color:var(--accent);color:var(--accent);background:var(--accent-a06)}
/* TMDB Proxy mode cards */
.tmdb-modes{display:flex;flex-direction:column;gap:8px}
.tmdb-mode-card{display:flex;align-items:center;gap:12px;padding:12px 14px;border-radius:10px;border:1.5px solid var(--border);background:var(--surface);cursor:pointer;transition:all .2s}
.tmdb-mode-card:hover{border-color:rgba(255,255,255,0.1);background:var(--surface2)}
.tmdb-mode-card.selected{border-color:var(--accent);background:var(--accent-a05)}
.tmdb-mode-icon{font-size:22px;flex-shrink:0;width:36px;text-align:center}
.tmdb-mode-info{flex:1;min-width:0}
.tmdb-mode-label{font-size:13px;font-weight:600;color:var(--text)}
.tmdb-mode-desc{font-size:11px;color:var(--text-muted);margin-top:2px;line-height:1.3}
.tmdb-mode-check{width:22px;height:22px;border-radius:50%;border:1.5px solid rgba(255,255,255,0.1);display:flex;align-items:center;justify-content:center;font-size:13px;color:var(--accent);flex-shrink:0;transition:all .2s}
.tmdb-mode-card.selected .tmdb-mode-check{border-color:var(--accent);background:var(--accent-a13)}
/* Custom plugin card extras */
.pg-card-url{font-size:11px;color:var(--text-dim);font-family:'SF Mono',monospace,Consolas;cursor:pointer;margin-top:4px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.pg-card-url:hover{color:var(--accent)}
.pg-card-toggles{display:flex;gap:12px;align-items:center;margin-top:8px;flex-wrap:wrap}
.pg-card-toggles label{font-size:11px;color:var(--text-muted);display:flex;align-items:center;gap:5px;cursor:pointer}
.pg-card-meta{display:flex;gap:8px;margin-top:8px;flex-wrap:wrap;align-items:center}
.pg-card-meta input{font-size:11px}
.pg-card-img{display:flex;align-items:center;gap:6px;margin-top:6px}
.pg-card-img img{height:28px;border-radius:4px;border:1px solid rgba(255,255,255,.1)}
.pg-card-delete{position:absolute;top:10px;right:10px;background:transparent;border:1px solid var(--danger-border);color:var(--danger);width:24px;height:24px;border-radius:6px;font-size:12px;cursor:pointer;display:flex;align-items:center;justify-content:center;transition:all .2s;opacity:.5}
.pg-card-delete:hover{opacity:1;background:var(--danger-bg);border-color:var(--danger)}
/* Plugin upload bar */
.pg-upload-bar{display:flex;gap:8px;align-items:flex-end;margin-bottom:16px;flex-wrap:wrap}
.pg-upload-bar label{font-size:12px;color:#aaa;display:block;margin-bottom:4px}
/* Plugin settings slide-out panel */
.pg-settings-overlay{position:fixed;top:0;left:0;right:0;bottom:0;background:rgba(0,0,0,.5);z-index:1100;display:none}
.pg-settings-overlay.open{display:block}
.pg-settings-panel{position:fixed;top:0;right:0;bottom:0;width:480px;max-width:100%;background:var(--bg);border-left:1px solid var(--border);z-index:1101;transform:translateX(100%);transition:transform .3s ease;overflow-y:auto;padding:24px}
.pg-settings-panel.open{transform:translateX(0)}
.pg-settings-panel .up-close{position:absolute;top:12px;right:16px}
.pg-settings-title{font-size:16px;font-weight:600;color:var(--text);margin-bottom:20px;display:flex;align-items:center;gap:10px}
.pg-settings-title .pg-icon{font-size:20px}
.pg-settings-section{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:8px;padding:16px;margin-bottom:16px}
.pg-settings-section h3{font-size:14px;color:var(--text);margin-bottom:12px}
@media(max-width:768px){.pg-grid{grid-template-columns:1fr}.pg-settings-panel{width:100%}}
.hint{display:inline-block;width:16px;height:16px;border-radius:50%;background:var(--border);color:var(--accent);font-size:11px;text-align:center;line-height:16px;cursor:help;margin-left:6px;position:relative}
.hint:hover::after{content:attr(data-tip);position:absolute;left:24px;top:-4px;background:var(--surface2);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:6px 10px;font-size:12px;white-space:pre-line;z-index:100;min-width:220px;max-width:360px;box-shadow:0 4px 12px rgba(0,0,0,.4)}
.form-row{display:flex;align-items:center;gap:12px;margin:8px 0}
.form-row label{width:180px;font-size:13px;color:var(--text-muted);flex-shrink:0}
.admin-form{display:flex;gap:8px;margin:16px 0;align-items:center}
.chevron{transition:transform .2s;font-size:12px;color:var(--text-muted)}
.chevron.open{transform:rotate(90deg)}
/* === Server Tab Redesign === */
.srv-header{display:flex;align-items:center;justify-content:space-between;margin-bottom:12px;flex-wrap:wrap;gap:12px}
.srv-header-info{flex:1;min-width:0}
.srv-hostname{font-size:18px;font-weight:700;color:var(--text);display:flex;align-items:center;gap:8px}
.srv-hostname .srv-pid{font-size:12px;color:var(--text-muted);font-weight:400;font-family:'SF Mono',monospace,Consolas}
.srv-meta{font-size:12px;color:var(--text-muted);margin-top:2px;font-family:'SF Mono',monospace,Consolas}
.srv-header-actions{display:flex;align-items:center;gap:8px}
.btn-danger{background:#ff6b6b22;color:var(--danger);border:1px solid #ff6b6b44;padding:6px 16px;border-radius:8px;font-size:13px;cursor:pointer;transition:all .2s;font-weight:600}
.btn-danger:hover{background:#ff6b6b33;border-color:var(--danger)}
.btn-danger:disabled{opacity:.5;cursor:not-allowed}
.btn-secondary,.btn.btn-secondary{background:var(--border);color:var(--text);border:1px solid rgba(255,255,255,0.1);padding:6px 16px;border-radius:8px;font-size:13px;cursor:pointer;transition:all .2s}
.btn-secondary:hover,.btn.btn-secondary:hover{background:rgba(255,255,255,0.1)}
.srv-uptime-hero{text-align:center;padding:8px 0 16px}
.srv-uptime-hero .uval{font-size:42px;font-weight:700;color:var(--accent);letter-spacing:1px}
.srv-uptime-hero .ulbl{font-size:11px;color:var(--text-muted);text-transform:uppercase;margin-top:4px;letter-spacing:1px}
/* Ring charts */
.srv-rings{display:flex;gap:20px;justify-content:center;flex-wrap:wrap;margin-bottom:8px}
.srv-ring{display:flex;flex-direction:column;align-items:center;gap:4px;min-width:140px}
.srv-ring svg{width:110px;height:110px;transform:rotate(-90deg)}
.srv-ring circle{fill:none;stroke-width:10;stroke-linecap:round}
.srv-ring .bg{stroke:var(--surface2)}
.srv-ring .fg{transition:stroke-dashoffset .6s ease}
.srv-ring-center{text-align:center;margin-top:-6px}
.srv-ring-center .rv{font-size:16px;font-weight:700;color:var(--text)}
.srv-ring-center .rs{font-size:10px;color:var(--text-dim)}
.srv-ring .rlabel{font-size:11px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.5px}
/* Sparkline */
.srv-sparkline-wrap{margin-top:12px;background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:8px;padding:12px 12px 24px;position:relative}
.srv-sparkline{display:flex;align-items:flex-end;gap:2px;height:48px}
.srv-sparkline .sbar{flex:1;border-radius:2px 2px 0 0;background:var(--accent);min-width:4px;transition:height .4s ease;opacity:.85}
.srv-sparkline .sbar:hover{opacity:1}
.srv-sparkline-labels{position:absolute;bottom:4px;left:12px;right:12px;font-size:10px;color:var(--text-muted);display:flex;justify-content:space-between}
/* Latency bars */
.lat-bars{margin-top:8px}
.lat-bar-row{display:flex;align-items:center;gap:12px;padding:5px 0}
.lat-bar-label{width:36px;font-size:12px;color:var(--text-muted);text-align:right;flex-shrink:0;font-weight:500}
.lat-bar-track{flex:1;height:22px;background:var(--surface2);border-radius:6px;position:relative;overflow:hidden}
.lat-bar-fill{height:100%;border-radius:6px;transition:width .4s ease;display:flex;align-items:center;padding-left:8px;font-size:11px;color:var(--bg);font-weight:600;min-width:40px}
.lat-bar-fill.lgreen{background:linear-gradient(90deg,var(--accent-dark),var(--accent))}
.lat-bar-fill.lyellow{background:linear-gradient(90deg,#f0c040,#d4a030)}
.lat-bar-fill.lorange{background:linear-gradient(90deg,#ffb347,#e08730)}
.lat-bar-fill.lred{background:linear-gradient(90deg,#ff6b6b,#cc4444)}
.route-lat-wrap{margin-top:10px;background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:8px;overflow:hidden}
.route-lat-table{width:100%;border-collapse:collapse;font-size:12px}
.route-lat-table th,.route-lat-table td{padding:8px 10px;border-bottom:1px solid var(--surface2)}
.route-lat-table th{font-size:10px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.4px;background:var(--surface)}
.route-lat-table tr:last-child td{border-bottom:none}
.route-lat-path{font-family:'SF Mono',monospace,Consolas;color:var(--text);max-width:320px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.route-lat-plugin{font-family:'SF Mono',monospace,Consolas;color:var(--text);max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.route-lat-muted{color:var(--text-muted);padding:10px 12px;font-size:12px}
/* Collapsible detail section */
.srv-collapse-body{display:none;margin-top:8px}
.srv-collapse-body.open{display:grid}
.srv-collapsible{cursor:pointer;user-select:none;display:flex;align-items:center;gap:6px}
.srv-collapsible:hover{color:var(--accent)}
/* Process resource badges */
.proc-resources{display:flex;gap:12px;margin-top:10px;padding-top:10px;border-top:1px solid rgba(255,255,255,.04)}
.proc-res-item{display:flex;flex-direction:column;align-items:center;min-width:60px}
.proc-res-item .rv{font-size:13px;font-weight:600;color:var(--text)}
.proc-res-item .rl{font-size:9px;color:var(--text-dim);text-transform:uppercase;letter-spacing:.3px}
/* Restart modal */
.srv-modal-overlay{position:fixed;top:0;left:0;right:0;bottom:0;background:rgba(0,0,0,.6);z-index:2000;display:none;align-items:center;justify-content:center}
.srv-modal-overlay.open{display:flex}
.srv-modal{background:rgba(22,27,35,0.95);border:1px solid rgba(255,255,255,0.06);border-radius:16px;padding:28px;max-width:420px;text-align:center;backdrop-filter:blur(20px);-webkit-backdrop-filter:blur(20px);box-shadow:0 8px 32px rgba(0,0,0,0.4)}
.srv-modal h3{color:var(--danger);font-size:18px;margin-bottom:12px}
.srv-modal p{color:var(--text-muted);font-size:13px;margin-bottom:20px;line-height:1.5}
.srv-modal-actions{display:flex;gap:12px;justify-content:center}
/* Chrome info card */
.chrome-card{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:10px;padding:16px;display:flex;align-items:center;gap:16px}
.chrome-card .chrome-icon{font-size:28px;width:48px;height:48px;display:flex;align-items:center;justify-content:center;background:var(--bg);border-radius:10px;flex-shrink:0}
.chrome-card .chrome-info{flex:1}
.chrome-card .chrome-info .cn{font-size:14px;font-weight:600;color:var(--text)}
.chrome-card .chrome-info .cs{font-size:11px;color:var(--text-muted);margin-top:2px}
.chrome-meter{margin-top:8px;height:6px;background:var(--surface2);border-radius:3px;overflow:hidden}
.chrome-meter .chrome-fill{height:100%;background:var(--accent);border-radius:3px;transition:width .4s}
@media(max-width:768px){.srv-rings{gap:12px}.srv-ring svg{width:90px;height:90px}.srv-uptime-hero .uval{font-size:30px}.srv-header{flex-direction:column;align-items:flex-start}.lat-bar-label{width:28px;font-size:11px}}
.stats-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:12px;margin-top:12px}
.stat-card{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:12px;padding:16px;text-align:center;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);transition:all .2s ease}
.stat-card .val{font-size:24px;font-weight:600;color:var(--accent)}
.stat-card .lbl{font-size:11px;color:var(--text-muted);margin-top:4px;text-transform:uppercase}
.stat-card.warn .val{color:var(--warning)}
.stat-card.danger .val{color:var(--danger)}
/* Process cards */
.proc-section{margin-top:8px}
.proc-section-title{font-size:13px;color:var(--text-muted);font-weight:600;margin-bottom:10px;display:flex;align-items:center;gap:8px}
.proc-section-title .icon{font-size:16px}
.proc-cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(340px,1fr));gap:12px}
.proc-card{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:12px;padding:16px;position:relative;overflow:hidden;transition:all .2s ease;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px)}
.proc-card:hover{border-color:var(--accent-a20);box-shadow:0 4px 20px var(--accent-a05)}
.proc-card.alive{border-left:3px solid var(--accent)}
.proc-card.dead{border-left:3px solid var(--danger)}
.proc-card-head{display:flex;align-items:center;gap:10px;margin-bottom:12px}
.proc-card-icon{font-size:22px;width:36px;height:36px;display:flex;align-items:center;justify-content:center;background:var(--bg);border-radius:8px;flex-shrink:0}
.proc-card-title{flex:1}
.proc-card-title .name{font-size:14px;font-weight:600;color:var(--text)}
.proc-card-title .sub{font-size:11px;color:var(--text-muted);margin-top:2px}
.proc-status{display:inline-flex;align-items:center;gap:4px;padding:3px 8px;border-radius:12px;font-size:11px;font-weight:500}
.proc-status.on{background:var(--accent-a08);color:var(--accent)}
.proc-status.off{background:rgba(255,107,107,0.09);color:var(--danger)}
.proc-status .dot{width:6px;height:6px;border-radius:50%;display:inline-block}
.proc-status.on .dot{background:var(--accent)}
.proc-status.off .dot{background:var(--danger)}
.proc-details{display:grid;grid-template-columns:1fr 1fr;gap:6px 16px}
.proc-detail{display:flex;flex-direction:column}
.proc-detail .key{font-size:10px;color:var(--text-dim);text-transform:uppercase;letter-spacing:.5px}
.proc-detail .value{font-size:13px;color:var(--text);font-weight:500;font-family:'SF Mono',monospace,Consolas;margin-top:1px}
.proc-tags{display:flex;flex-wrap:wrap;gap:4px;margin-top:10px}
.proc-tag{background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:2px 8px;font-size:11px;color:var(--text-muted)}
.proc-card-actions{display:flex;gap:6px;margin-top:10px;padding-top:10px;border-top:1px solid var(--border)}
.proc-card-actions .btn-sm{font-size:11px;padding:4px 12px;border-radius:6px;cursor:pointer;font-weight:600;transition:all .2s}
.btn-warn{background:var(--warning-bg);color:var(--warning);border:1px solid var(--warning-border)}
.btn-warn:hover{background:var(--warning-border)}
@media(max-width:768px){.proc-cards{grid-template-columns:1fr}.proc-details{grid-template-columns:1fr}}
/* Health grid */
.health-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:10px;margin-top:12px}
.health-card{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:12px;padding:12px 16px;display:flex;align-items:center;gap:12px;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);transition:all .2s ease}
.health-card.healthy{border-left:3px solid var(--accent)}
.health-card.unhealthy{border-left:3px solid var(--danger)}
.health-card.auto-disabled{border-left:3px solid var(--warning)}
.health-card.unknown{border-left:3px solid var(--text-muted)}
.health-dot{width:10px;height:10px;border-radius:50%;flex-shrink:0}
.health-dot.ok{background:var(--accent)}
.health-dot.fail{background:var(--danger)}
.health-dot.warn{background:var(--warning)}
.health-dot.unk{background:var(--text-muted)}
.health-info{flex:1;min-width:0}
.health-name{font-size:13px;font-weight:600;color:var(--text)}
.health-meta{font-size:11px;color:var(--text-muted);margin-top:2px}
.health-stats{text-align:right;font-size:11px;color:var(--text-muted)}
.health-stats .hval{font-size:13px;color:var(--text);font-weight:500}
@media(max-width:768px){.health-grid{grid-template-columns:1fr}}
/* Dashboard redesign */
.dash-hero{display:flex;align-items:center;gap:20px;padding:20px 24px;background:linear-gradient(135deg,rgba(13,148,136,0.08) 0%,rgba(6,182,212,0.04) 100%);border:1px solid var(--border);border-radius:16px;margin-bottom:20px;flex-wrap:wrap;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px)}
.dash-hero-left{flex-shrink:0}
.dash-hero-title{font-size:22px;font-weight:700;color:var(--text)}
.dash-hero-ver{font-size:12px;color:var(--accent);margin-top:2px;font-family:'SF Mono',monospace,Consolas}
.dash-hero-metrics{display:flex;gap:28px;flex:1;justify-content:center;flex-wrap:wrap}
.dash-hero-metric{text-align:center;min-width:60px}
.dash-hero-metric .hm-val{font-size:24px;font-weight:700;color:var(--text)}
.dash-hero-metric .hm-lbl{font-size:10px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.5px;margin-top:2px}
.dash-hero-right{flex-shrink:0}
.dash-auto-label{font-size:12px;color:var(--text-muted);display:flex;align-items:center;gap:6px;cursor:pointer;user-select:none}
.dash-auto-label input{accent-color:var(--accent)}
.dash-grid-2{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-bottom:16px}
.dash-section{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:12px;padding:20px;margin-bottom:16px;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px)}
.dash-grid-2 .dash-section{margin-bottom:0}
.dash-section-head{display:flex;align-items:center;gap:8px;margin-bottom:16px}
.dash-section-icon{font-size:18px;flex-shrink:0}
.dash-section-title{font-size:15px;font-weight:600;color:var(--text)}
.dash-health-summary{margin-left:auto;font-size:12px;color:var(--text-muted)}
.dash-traffic-cards-wide{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.dash-tcard{background:var(--bg);border:1px solid var(--border);border-radius:10px;padding:14px 16px}
.dash-tcard .tc-val{font-size:22px;font-weight:700;color:var(--accent)}
.dash-tcard .tc-lbl{font-size:10px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.5px;margin-top:2px}
.dash-tcard.accent .tc-val{color:var(--warning)}
.dash-tcard.wide{grid-column:1/-1}
.dash-chart-wrap{display:flex;flex-direction:column}
.dash-chart{min-height:90px;background:var(--bg);border:1px solid var(--border);border-radius:10px;display:flex;align-items:flex-end;gap:2px;padding:12px 10px 4px 10px}
.dash-chart-labels{display:flex;justify-content:space-between;font-size:10px;color:var(--text-dim);padding:4px 10px 0}
.dash-req-cards{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.dash-rcard{background:var(--bg);border:1px solid var(--border);border-radius:10px;padding:14px 16px}
.dash-rcard .rc-val{font-size:22px;font-weight:700;color:var(--text)}
.dash-rcard .rc-lbl{font-size:10px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.5px;margin-top:2px}
.dash-rcard .rc-val.good{color:var(--accent)}
.dash-routes-title{font-size:11px;color:var(--text-muted);font-weight:600;text-transform:uppercase;letter-spacing:.5px;margin-bottom:8px}
.dash-route-bar{display:flex;align-items:center;gap:8px;padding:3px 0;font-size:12px}
.dash-route-bar .route-name{flex:1;color:var(--text);min-width:0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-family:'SF Mono',monospace,Consolas;font-size:11px}
.dash-route-bar .route-bar-bg{width:80px;height:5px;background:var(--bg);border-radius:3px;overflow:hidden;flex-shrink:0}
.dash-route-bar .route-bar-fill{height:100%;border-radius:3px;background:var(--accent);transition:width .3s}
.dash-route-bar .route-count{color:var(--text-muted);font-size:11px;width:40px;text-align:right;flex-shrink:0}
/* Health mosaic */
.dash-health-mosaic{display:flex;flex-wrap:wrap;gap:6px}
.dash-hm-tile{display:flex;align-items:center;gap:6px;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:6px 10px;font-size:12px;color:var(--text);cursor:default;transition:border-color .15s;position:relative}
.dash-hm-tile:hover{border-color:rgba(255,255,255,0.1)}
.dash-hm-tile .hm-dot{width:8px;height:8px;border-radius:50%;flex-shrink:0}
.dash-hm-tile .hm-dot.ok{background:var(--accent)}
.dash-hm-tile .hm-dot.fail{background:var(--danger)}
.dash-hm-tile .hm-dot.warn{background:var(--warning)}
.dash-hm-tile .hm-dot.unk{background:var(--text-muted)}
.dash-hm-tile .hm-name{white-space:nowrap}
.dash-hm-tile .hm-lat{color:var(--text-muted);font-size:10px;margin-left:2px}
.dash-hm-tile .hm-tooltip{display:none;position:absolute;bottom:calc(100% + 6px);left:50%;transform:translateX(-50%);background:rgba(255,255,255,0.04);border:1px solid rgba(255,255,255,0.1);border-radius:8px;padding:8px 12px;font-size:11px;color:var(--text);white-space:nowrap;z-index:10;box-shadow:0 4px 12px rgba(0,0,0,.4);pointer-events:none}
.dash-hm-tile:hover .hm-tooltip{display:block}
.dash-hm-tile .hm-tooltip .tt-row{display:flex;justify-content:space-between;gap:16px;padding:1px 0}
.dash-hm-tile .hm-tooltip .tt-lbl{color:var(--text-muted)}
.dash-hm-tile .hm-tooltip .tt-val{font-weight:600}
/* Widget grid */
.dash-widget-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-bottom:16px}
.dash-widget-grid>.dash-section{margin-bottom:0}
.dash-widget-grid>.dash-section.full{grid-column:1/-1}
.dash-widget-customize{text-align:center;margin-top:4px;margin-bottom:16px}
.dash-widget-customize button{background:transparent;border:1px solid var(--border);color:var(--text-muted);padding:6px 16px;border-radius:8px;font-size:12px;cursor:pointer;transition:all .2s}
.dash-widget-customize button:hover{border-color:var(--accent);color:var(--accent)}
/* Widget picker modal */
.wpick-overlay{display:none;position:fixed;inset:0;background:rgba(0,0,0,.5);z-index:1000;align-items:center;justify-content:center}
.wpick-overlay.open{display:flex}
.wpick-panel{background:rgba(22,27,35,0.95);border:1px solid rgba(255,255,255,0.06);border-radius:16px;padding:24px;width:420px;max-width:90vw;max-height:80vh;overflow-y:auto;backdrop-filter:blur(20px);-webkit-backdrop-filter:blur(20px);box-shadow:0 8px 32px rgba(0,0,0,0.4)}
.wpick-title{font-size:16px;font-weight:700;color:var(--text);margin-bottom:16px;display:flex;align-items:center;gap:8px}
.wpick-list{display:flex;flex-direction:column;gap:6px}
.wpick-item{display:flex;align-items:center;gap:10px;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:10px 12px;transition:border-color .15s}
.wpick-item.active{border-color:var(--accent-a30)}
.wpick-item label{flex:1;display:flex;align-items:center;gap:8px;cursor:pointer;font-size:13px;color:var(--text);user-select:none}
.wpick-item input[type=checkbox]{accent-color:var(--accent)}
.wpick-item .wpick-icon{font-size:16px;flex-shrink:0}
.wpick-arrows{display:flex;flex-direction:column;gap:2px}
.wpick-arrows button{background:transparent;border:1px solid var(--border);color:var(--text-muted);width:22px;height:18px;border-radius:4px;cursor:pointer;font-size:10px;line-height:1;display:flex;align-items:center;justify-content:center;padding:0}
.wpick-arrows button:hover{border-color:var(--accent);color:var(--accent)}
.wpick-actions{display:flex;gap:8px;margin-top:16px}
.wpick-actions button{padding:8px 18px;border-radius:8px;font-size:13px;cursor:pointer;border:none}
.wpick-actions .wpick-save{background:var(--accent);color:var(--bg);font-weight:600}
.wpick-actions .wpick-reset{background:transparent;border:1px solid var(--border);color:var(--text-muted)}
.wpick-actions .wpick-cancel{background:transparent;border:1px solid var(--border);color:var(--text-muted);margin-left:auto}
@media(max-width:900px){.dash-grid-2{grid-template-columns:1fr}.dash-widget-grid{grid-template-columns:1fr}}
@media(max-width:768px){.dash-hero-metrics{justify-content:flex-start;gap:16px}.dash-traffic-cards-wide{grid-template-columns:1fr 1fr}.dash-req-cards{grid-template-columns:1fr 1fr}}
/* Proxy multi-entry */
.proxy-servers{display:flex;flex-direction:column;gap:16px;margin-bottom:24px}
.proxy-server{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:12px;overflow:hidden;transition:all .2s ease;backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px)}
.proxy-server.active{border-color:var(--accent-a30)}
.proxy-server-head{display:flex;align-items:center;gap:12px;padding:16px 20px;cursor:pointer;user-select:none}
.proxy-server-head:hover{background:var(--surface2)}
.proxy-flag{font-size:28px;line-height:1}
.proxy-server-info{flex:1;min-width:0}
.proxy-server-label{font-size:15px;font-weight:600;color:var(--text)}
.proxy-server-ip{font-size:11px;color:var(--text-muted);font-family:monospace;margin-top:2px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.proxy-server-status{display:flex;align-items:center;gap:8px}
.proxy-dot{width:8px;height:8px;border-radius:50%;flex-shrink:0}
.proxy-dot.on{background:var(--accent);box-shadow:var(--accent-glow)}
.proxy-dot.off{background:var(--danger)}
.proxy-server-body{display:none;padding:0 20px 20px;border-top:1px solid var(--border)}
.proxy-server-body.open{display:block}
.proxy-uri-row{margin:12px 0}
.proxy-uri-input{width:100%;background:var(--bg);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:6px;font-family:'Courier New',monospace;font-size:11px}
.proxy-uri-input:focus{outline:none;border-color:var(--accent)}
.proxy-label-input{background:var(--bg);border:1px solid var(--border);color:var(--text);padding:6px 10px;border-radius:6px;font-size:13px;width:220px}
.proxy-label-input:focus{outline:none;border-color:var(--accent)}
.proxy-balancers-zone{min-height:48px;background:var(--bg);border:2px dashed var(--border);border-radius:8px;padding:8px;display:flex;flex-wrap:wrap;gap:6px;transition:border-color .2s,background .2s}
.proxy-balancers-zone.drag-over{border-color:var(--accent);background:var(--accent-a04)}
.proxy-balancers-zone.empty-hint::after{content:'Перетащите балансеры сюда';color:var(--text-dim);font-size:12px;display:flex;align-items:center;justify-content:center;width:100%;min-height:32px}
.bal-chip{display:inline-flex;align-items:center;gap:4px;padding:4px 12px;background:var(--surface2);border:1px solid var(--border);border-radius:16px;font-size:12px;font-weight:500;cursor:grab;user-select:none;transition:all .15s}
.bal-chip:active{cursor:grabbing}
.bal-chip.dragging{opacity:.4}
.bal-chip .remove-bal{cursor:pointer;color:var(--text-muted);font-size:14px;margin-left:2px;line-height:1}
.bal-chip .remove-bal:hover{color:var(--danger)}
.proxy-test-row{margin-top:12px;display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.proxy-test-result{font-size:12px;margin-top:4px}
.proxy-server-actions{display:flex;gap:8px;margin-top:12px}
.proxy-available{margin-top:16px}
.proxy-available-title{font-size:13px;color:var(--text-muted);margin-bottom:8px}
.proxy-available-chips{display:flex;flex-wrap:wrap;gap:6px;min-height:36px;background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:8px;padding:8px}
.proxy-global-actions{display:flex;gap:12px;margin-top:20px;align-items:center}
.proxy-add-btn{display:flex;align-items:center;gap:6px;padding:10px 20px;background:transparent;border:2px dashed var(--border);border-radius:12px;color:var(--text-muted);cursor:pointer;font-size:13px;transition:all .2s;width:100%}
.proxy-add-btn:hover{border-color:var(--accent);color:var(--accent)}
@media(max-width:768px){.content{padding:12px}.input-sm{width:140px}.plugin-grid{grid-template-columns:1fr 1fr}.stats-grid{grid-template-columns:1fr 1fr}}
/* Constructor */
.ctr-upload{border:2px dashed var(--border);border-radius:12px;padding:40px;text-align:center;cursor:pointer;transition:all .2s;background:rgba(22,27,35,0.8);margin-bottom:20px}
.ctr-upload:hover,.ctr-upload.drag-over{border-color:var(--accent);background:var(--accent-a04)}
.ctr-upload-icon{font-size:36px;margin-bottom:8px}
.ctr-upload-text{color:var(--text-muted);font-size:13px}
.ctr-upload-text strong{color:var(--text)}
.ctr-paste-area{width:100%;min-height:200px;background:var(--bg);border:1px solid var(--border);color:var(--text);padding:16px;border-radius:8px;font-family:'Courier New',monospace;font-size:12px;resize:vertical;margin-top:12px}
.ctr-paste-area:focus{outline:none;border-color:var(--accent)}
.ctr-result{margin-top:20px}
.ctr-section{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:8px;padding:16px;margin:12px 0}
.ctr-section h3{font-size:14px;color:var(--accent);margin-bottom:12px;display:flex;align-items:center;gap:8px}
.ctr-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:10px}
.ctr-chip{background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:6px 12px;font-size:12px;color:var(--text);font-family:monospace}
.ctr-param{display:flex;align-items:center;gap:8px;padding:4px 0}
.ctr-param .pname{color:var(--accent);font-weight:500;font-family:monospace;font-size:12px}
.ctr-param .ptype{color:var(--text-muted);font-size:11px}
.ctr-param .pdefault{color:var(--warning);font-size:11px}
.ctr-badge{display:inline-flex;align-items:center;gap:4px;padding:3px 10px;border-radius:12px;font-size:11px;font-weight:500;margin:2px}
.ctr-badge.yes{background:var(--accent-a08);color:var(--accent)}
.ctr-badge.no{background:rgba(255,107,107,0.09);color:var(--danger)}
.ctr-code-wrap{position:relative}
.ctr-code{width:100%;min-height:400px;background:var(--bg);border:1px solid var(--border);color:var(--text);padding:16px;border-radius:8px;font-family:'Courier New',monospace;font-size:12px;resize:vertical;white-space:pre;tab-size:4}
.ctr-code:focus{outline:none;border-color:var(--accent)}
.ctr-copy-btn{position:absolute;top:8px;right:8px;background:var(--accent-a13);color:var(--accent);border:1px solid var(--accent-a30);padding:4px 12px;border-radius:6px;cursor:pointer;font-size:11px}
.ctr-copy-btn:hover{background:var(--accent-a30)}
.ctr-tabs{display:flex;gap:0;margin-bottom:16px;background:rgba(22,27,35,0.8);border-radius:8px;overflow:hidden;border:1px solid var(--border)}
.ctr-tab{padding:10px 20px;cursor:pointer;color:var(--text-muted);font-size:13px;transition:all .2s;border-right:1px solid var(--border)}
.ctr-tab:last-child{border-right:none}
.ctr-tab:hover{color:var(--text)}
.ctr-tab.active{color:var(--accent);background:var(--bg)}
.ctr-panel{display:none}
.ctr-panel.active{display:block}
/* User side panel */
.user-panel-overlay{position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:900;opacity:0;pointer-events:none;transition:opacity .3s}
.user-panel-overlay.open{opacity:1;pointer-events:auto}
.user-panel{position:fixed;top:0;right:0;width:500px;height:100vh;background:var(--surface);border-left:1px solid var(--border);z-index:901;transform:translateX(100%);transition:transform .3s cubic-bezier(.4,0,.2,1);overflow-y:auto;display:flex;flex-direction:column}
.user-panel.open{transform:translateX(0)}
@media(max-width:560px){.user-panel{width:100%}}
.up-close{position:absolute;top:16px;right:16px;width:32px;height:32px;border:none;background:var(--border);color:var(--text-muted);border-radius:50%;font-size:18px;cursor:pointer;display:flex;align-items:center;justify-content:center;z-index:2;transition:all .2s}
.up-close:hover{background:var(--danger-bg);color:var(--danger)}
.up-header{padding:24px 20px 16px;background:linear-gradient(135deg,rgba(13,148,136,0.06) 0%,rgba(6,182,212,0.03) 100%);border-bottom:1px solid var(--border)}
.up-avatar{width:48px;height:48px;border-radius:50%;background:var(--accent-a13);color:var(--accent);display:flex;align-items:center;justify-content:center;font-size:20px;font-weight:600;margin-bottom:12px}
.up-name{font-size:18px;font-weight:600;color:var(--text)}
.up-tgid{font-size:12px;color:var(--text-muted);margin-top:2px;font-family:monospace}
.up-status{display:inline-flex;align-items:center;gap:5px;margin-top:8px;padding:3px 10px;border-radius:12px;font-size:11px;font-weight:600}
.up-status.active{background:var(--accent-a08);color:var(--accent)}
.up-status.expired{background:rgba(255,107,107,0.09);color:var(--danger)}
.up-status .dot{width:6px;height:6px;border-radius:50%;display:inline-block}
.up-status.active .dot{background:var(--accent)}
.up-status.expired .dot{background:var(--danger)}
.up-body{padding:0 20px 24px;flex:1}
.up-section{border-bottom:1px solid rgba(255,255,255,0.04);padding:16px 0}
.up-section:last-child{border-bottom:none}
.up-section-head{display:flex;align-items:center;gap:8px;cursor:pointer;user-select:none;margin-bottom:10px}
.up-section-head:hover .up-section-title{color:var(--accent)}
.up-section-icon{font-size:14px;color:var(--text-muted);transition:transform .2s}
.up-section-icon.open{transform:rotate(90deg)}
.up-section-title{font-size:13px;font-weight:600;color:var(--text);flex:1}
.up-section-badge{font-size:11px;color:var(--text-muted);background:var(--surface2);padding:2px 8px;border-radius:10px}
.up-section-body{display:none}
.up-section-body.open{display:block}
.up-info-row{display:flex;align-items:center;gap:8px;padding:6px 0;font-size:13px}
.up-info-label{color:var(--text-muted);width:80px;flex-shrink:0;font-size:12px}
.up-info-val{color:var(--text);flex:1;font-family:monospace;font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.up-copy{background:none;border:1px solid var(--border);color:var(--text-muted);padding:2px 8px;border-radius:4px;font-size:11px;cursor:pointer;flex-shrink:0;transition:all .15s}
.up-copy:hover{border-color:var(--accent);color:var(--accent)}
.up-actions{display:flex;gap:6px;flex-wrap:wrap;margin-top:8px}
/* Devices in panel */
.up-device{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:8px;padding:10px 12px;margin:6px 0;display:flex;align-items:center;gap:10px}
.up-device-icon{font-size:18px;flex-shrink:0}
.up-device-info{flex:1;min-width:0}
.up-device-uid{font-size:12px;font-family:monospace;color:var(--text);font-weight:600}
.up-device-label{font-size:11px;color:var(--text-muted)}
.up-device-meta{font-size:10px;color:var(--text-dim);margin-top:2px}
.up-device-rm{background:none;border:none;color:var(--text-muted);cursor:pointer;font-size:14px;padding:4px;transition:color .15s}
.up-device-rm:hover{color:var(--danger)}
/* Balancers in panel */
.up-bal-group{margin:8px 0}
.up-bal-group-head{display:flex;align-items:center;gap:8px;padding:6px 0;cursor:pointer;user-select:none}
.up-bal-group-icon{font-size:16px}
.up-bal-group-title{font-size:13px;font-weight:600;color:var(--text);flex:1}
.up-bal-group-toggle{font-size:10px;color:var(--text-muted);background:var(--surface2);border:1px solid var(--border);padding:2px 8px;border-radius:10px;cursor:pointer;transition:all .15s}
.up-bal-group-toggle:hover{border-color:var(--accent);color:var(--accent)}
.up-bal-items{padding-left:4px}
.up-bal-item{display:flex;align-items:center;gap:8px;padding:5px 0;font-size:13px}
.up-bal-item.globally-off{opacity:.5}
.up-bal-item .name{flex:1;color:var(--text)}
.up-bal-item .off-hint{font-size:10px;color:rgba(255,107,107,0.53);margin-left:4px}
.up-bal-reset{margin-top:12px}
/* User table interactive rows */
.user-row{cursor:pointer;transition:background .15s}
.user-row:hover{background:rgba(22,27,35,0.8) !important}
.user-row.selected{background:rgba(22,27,35,0.8);border-left:2px solid var(--accent)}
.pg-btn{background:rgba(22,27,35,0.8);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:4px 10px;cursor:pointer;font-size:12px;transition:all .15s}
.pg-btn:hover{background:var(--surface2);border-color:var(--accent)}
.pg-btn.active{background:var(--accent);color:var(--bg);font-weight:700;border-color:var(--accent)}
.pg-btn:disabled{opacity:.3;cursor:default}
.users-stat{display:inline-flex;gap:12px;font-size:13px}
.users-stat b{color:var(--accent)}
.users-stat .expired-count{color:#f87171}
.fb-stats{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:12px;margin-bottom:20px}
.fb-stat{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:10px;padding:18px;text-align:center;transition:border-color .2s}
.fb-stat:hover{border-color:var(--accent-a30)}
.fb-stat .fb-val{font-size:32px;font-weight:700;color:var(--accent)}
.fb-stat .fb-lbl{font-size:11px;color:var(--text-muted);margin-top:6px;text-transform:uppercase;letter-spacing:.5px}
.fb-stat.s-open .fb-val{color:var(--accent)}
.fb-stat.s-progress .fb-val{color:var(--warning)}
.fb-stat.s-resolved .fb-val{color:#a78bfa}
.fb-stat.s-closed .fb-val{color:var(--text-muted)}
.fb-filters{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:16px}
.fb-filters select{background:var(--surface2);border:1px solid var(--border);color:var(--text);padding:7px 12px;border-radius:6px;font-size:12px;cursor:pointer}
.fb-filters select:focus{outline:none;border-color:var(--accent)}
.fb-cat{display:inline-block;padding:3px 10px;border-radius:5px;font-size:11px;font-weight:600}
.fb-cat-help{background:#60a5fa22;color:#60a5fa}
.fb-cat-thanks{background:var(--accent-a13);color:var(--accent)}
.fb-cat-bug{background:var(--danger-bg);color:var(--danger)}
.fb-cat-feature{background:#a78bfa22;color:#a78bfa}
.fb-cat-other{background:#94a3b822;color:var(--text-muted)}
.fb-pri{display:inline-block;padding:3px 10px;border-radius:5px;font-size:11px;font-weight:600}
.fb-pri-low{background:#94a3b822;color:var(--text-muted)}
.fb-pri-medium{background:#60a5fa22;color:#60a5fa}
.fb-pri-high{background:#f0c04022;color:var(--warning)}
.fb-pri-critical{background:#ff6b6b22;color:var(--danger);animation:pulse-glow 2s ease-in-out infinite}
@keyframes pulse-glow{0%,100%{box-shadow:0 0 0 0 rgba(255,107,107,0)}50%{box-shadow:0 0 8px 2px rgba(255,107,107,.25)}}
.fb-st{display:inline-block;padding:3px 12px;border-radius:12px;font-size:11px;font-weight:600}
.fb-st-open{background:var(--accent-a13);color:var(--accent)}
.fb-st-in_progress{background:#f0c04022;color:var(--warning)}
.fb-st-resolved{background:#a78bfa22;color:#a78bfa}
.fb-st-closed{background:#94a3b822;color:var(--text-muted)}
.fb-overlay{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:1100;display:none;backdrop-filter:blur(2px)}
.fb-overlay.open{display:block}
.fb-slide{position:fixed;top:0;right:0;bottom:0;width:580px;max-width:100%;background:var(--bg);border-left:1px solid var(--border);z-index:1101;transform:translateX(100%);transition:transform .3s cubic-bezier(.4,0,.2,1);overflow-y:auto;padding:28px}
.fb-slide.open{transform:translateX(0)}
.fb-slide .close-x{position:absolute;top:12px;right:16px;font-size:22px;color:var(--text-muted);cursor:pointer;background:none;border:none;padding:4px 8px;border-radius:6px;transition:all .2s}
.fb-slide .close-x:hover{color:var(--danger);background:rgba(255,107,107,0.09)}
.fb-thread{margin:16px 0}
.fb-reply-card{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:10px;padding:14px;margin-bottom:10px}
.fb-reply-card .fb-reply-meta{font-size:11px;color:var(--text-muted);margin-bottom:6px;display:flex;gap:8px;align-items:center}
.fb-reply-card .fb-reply-body{font-size:13px;color:var(--text);white-space:pre-wrap;line-height:1.5}
.fb-msg-block{background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:10px;padding:16px;margin:12px 0;font-size:13px;color:var(--text);white-space:pre-wrap;line-height:1.6}
.fb-meta-row{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin:12px 0;font-size:12px;color:var(--text-muted)}
.fb-reply-area{width:100%;min-height:100px;background:var(--surface2);border:1px solid var(--border);color:var(--text);padding:12px;border-radius:10px;font-size:13px;resize:vertical;font-family:inherit;line-height:1.5}
.fb-reply-area:focus{outline:none;border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-a08)}
.fb-tbl{width:100%;border-collapse:collapse;font-size:13px}
.fb-tbl th{text-align:left;padding:10px 12px;color:var(--text-muted);font-weight:500;font-size:11px;text-transform:uppercase;letter-spacing:.5px;border-bottom:1px solid var(--border)}
.fb-tbl td{padding:10px 12px;border-bottom:1px solid var(--surface2)}
.fb-tbl tr{cursor:pointer;transition:background .15s}
.fb-tbl tr:hover{background:var(--accent-a04)}
.fb-cnt{display:inline-flex;align-items:center;justify-content:center;width:22px;height:22px;border-radius:50%;font-size:11px;font-weight:700;background:var(--accent-a13);color:var(--accent)}
.fb-cnt.zero{background:var(--border);color:var(--text-dim)}

/* === Telemetry tab === */
.tlm-summary{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px;margin:14px 0}
.tlm-stat{background:linear-gradient(135deg,rgba(22,27,35,0.8),rgba(22,27,35,0.5));border:1px solid var(--border);border-radius:12px;padding:14px 16px;display:flex;flex-direction:column;gap:4px}
.tlm-stat-label{font-size:11px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.6px;font-weight:600}
.tlm-stat-value{font-size:26px;font-weight:700;color:var(--text);line-height:1.1}
.tlm-stat.down .tlm-stat-value{color:#f87171}
.tlm-stat.degraded .tlm-stat-value{color:#fbbf24}
.tlm-stat.healthy .tlm-stat-value{color:#34d399}
.tlm-stat.unknown .tlm-stat-value{color:var(--text-dim)}
.tlm-toolbar{display:flex;flex-wrap:wrap;gap:10px;align-items:center;padding:10px 14px;background:rgba(22,27,35,0.8);border:1px solid var(--border);border-radius:10px;margin-bottom:14px}
.tlm-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:12px}
.tlm-card{background:linear-gradient(135deg,rgba(22,27,35,0.85),rgba(22,27,35,0.55));border:1px solid var(--border);border-left:3px solid var(--border);border-radius:12px;padding:14px 16px;display:flex;flex-direction:column;gap:10px;transition:all .2s ease}
.tlm-card:hover{transform:translateY(-2px);box-shadow:0 6px 18px rgba(0,0,0,0.35)}
.tlm-card.s-down{border-left-color:#f87171;box-shadow:inset 0 0 30px rgba(248,113,113,0.06)}
.tlm-card.s-degraded{border-left-color:#fbbf24;box-shadow:inset 0 0 30px rgba(251,191,36,0.06)}
.tlm-card.s-healthy{border-left-color:#34d399}
.tlm-card.s-unknown{border-left-color:#475569;opacity:.78}
.tlm-card-head{display:flex;align-items:center;gap:8px}
.tlm-card-name{font-size:14px;font-weight:600;color:var(--text);flex:1;display:flex;align-items:center;gap:6px}
.tlm-card-dot{width:9px;height:9px;border-radius:50%;background:#475569;display:inline-block;flex-shrink:0}
.tlm-card.s-down .tlm-card-dot{background:#f87171;box-shadow:0 0 8px rgba(248,113,113,0.6)}
.tlm-card.s-degraded .tlm-card-dot{background:#fbbf24;box-shadow:0 0 8px rgba(251,191,36,0.6)}
.tlm-card.s-healthy .tlm-card-dot{background:#34d399;box-shadow:0 0 6px rgba(52,211,153,0.5)}
.tlm-card-q{font-size:9px;font-weight:700;padding:2px 6px;border-radius:6px;background:rgba(255,255,255,0.06);color:var(--text-muted);letter-spacing:.4px}
.tlm-card-q.q-4K{background:rgba(167,139,250,0.18);color:#a78bfa}
.tlm-card-q.q-FHD{background:rgba(96,165,250,0.18);color:#60a5fa}
.tlm-card-q.q-SD{background:rgba(148,163,184,0.18);color:#94a3b8}
.tlm-card-metrics{display:flex;gap:12px;align-items:baseline}
.tlm-card-rate{font-size:24px;font-weight:700;color:var(--text);line-height:1}
.tlm-card.s-down .tlm-card-rate{color:#f87171}
.tlm-card.s-degraded .tlm-card-rate{color:#fbbf24}
.tlm-card.s-healthy .tlm-card-rate{color:#34d399}
.tlm-card-rate-label{font-size:10px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.5px}
.tlm-card-totals{font-size:11px;color:var(--text-muted);margin-left:auto;text-align:right}
.tlm-card-lat{display:flex;gap:10px;font-size:11px;color:var(--text-muted);padding:6px 8px;background:rgba(255,255,255,0.025);border-radius:6px}
.tlm-card-lat span b{color:var(--text);font-weight:600}
.tlm-card-host{font-size:11px;color:var(--text-muted);font-family:'SF Mono','Fira Code',monospace;display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.tlm-card-host code{background:rgba(255,255,255,0.04);padding:2px 6px;border-radius:4px;color:var(--accent)}
.tlm-card-host .tlm-fbcount{font-size:10px;color:var(--text-dim)}
.tlm-card-actions{display:flex;gap:6px;flex-wrap:wrap;margin-top:2px}
.tlm-card-actions button{padding:4px 10px;font-size:11px;border-radius:6px;border:1px solid var(--border);background:transparent;color:var(--text-muted);cursor:pointer;transition:all .15s}
.tlm-card-actions button:hover{background:var(--accent-a08);color:var(--accent);border-color:var(--accent)}
.tlm-card-err{font-size:11px;color:#fca5a5;background:rgba(248,113,113,0.06);border:1px solid rgba(248,113,113,0.18);padding:6px 8px;border-radius:6px;font-family:monospace;line-height:1.4;max-height:60px;overflow:hidden;text-overflow:ellipsis;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical}
.tlm-card-err small{color:var(--text-dim);font-family:inherit}
.tlm-card-bar{height:4px;background:rgba(255,255,255,0.04);border-radius:2px;overflow:hidden;display:flex}
.tlm-card-bar .tlm-bar-ok{background:#34d399}
.tlm-card-bar .tlm-bar-fail{background:#f87171}
.tlm-card.s-unknown .tlm-card-bar{display:none}
.tlm-fb-edit{font-size:11px;border:none;background:transparent;color:var(--accent);cursor:pointer;padding:0;text-decoration:underline}
.tlm-fb-edit:hover{color:#fff}
.tlm-card-cs{font-size:11px;color:var(--text-muted);padding:5px 8px;background:rgba(96,165,250,0.06);border:1px solid rgba(96,165,250,0.16);border-radius:6px;display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.tlm-card-cs b{color:var(--text);font-weight:600}
.tlm-cs-label{font-size:10px;text-transform:uppercase;letter-spacing:.5px;color:#60a5fa}
.tlm-cs-totals{font-size:10px;color:var(--text-dim)}
.tlm-card-breaker{font-size:11px;padding:5px 8px;border-radius:6px;display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.tlm-card-breaker.open{color:#fca5a5;background:rgba(248,113,113,0.08);border:1px solid rgba(248,113,113,0.25)}
.tlm-card-breaker.pending{color:#fbbf24;background:rgba(251,191,36,0.06);border:1px solid rgba(251,191,36,0.18)}
.tlm-card.breaker-open{box-shadow:0 0 0 1px rgba(248,113,113,0.4) inset}

/* === Media Gateway tab === */
.mgw-section{background:rgba(22,27,35,0.5);border:1px solid var(--border);border-radius:12px;padding:16px;margin-bottom:14px}
.mgw-h{font-size:13px;font-weight:600;color:var(--text);margin-bottom:12px;display:flex;align-items:center;gap:8px}
.mgw-summary{display:flex;gap:18px;margin-bottom:12px;flex-wrap:wrap}
.mgw-stat{display:flex;flex-direction:column}
.mgw-stat-l{font-size:10px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.5px}
.mgw-stat-v{font-size:20px;font-weight:700;color:var(--text)}
.mgw-stat-v.run{color:#34d399}
.mgw-stat-v.idle{color:var(--text-dim)}
.mgw-jobs-list{display:flex;flex-direction:column;gap:8px}
.mgw-job{background:rgba(13,17,23,0.6);border:1px solid var(--border);border-radius:8px;padding:10px 14px;display:grid;grid-template-columns:auto 1fr auto;gap:12px;align-items:center}
.mgw-job.s-stopped{opacity:.6}
.mgw-job-id{font-family:'SF Mono','Fira Code',monospace;font-size:11px;color:var(--accent);white-space:nowrap}
.mgw-job-info{display:flex;flex-direction:column;gap:3px;min-width:0}
.mgw-job-mode{display:inline-block;font-size:9px;font-weight:700;padding:2px 6px;border-radius:4px;background:rgba(167,139,250,0.16);color:#a78bfa;text-transform:uppercase;letter-spacing:.5px;width:fit-content}
.mgw-job-src{font-size:11px;color:var(--text-muted);font-family:monospace;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:600px}
.mgw-job-meta{font-size:11px;color:var(--text-dim);display:flex;gap:10px}
.mgw-job-meta b{color:var(--text)}
.mgw-job-actions button{padding:4px 10px;font-size:11px;border-radius:6px;border:1px solid var(--border);background:transparent;color:#fca5a5;cursor:pointer}
.mgw-job-actions button:hover{background:rgba(248,113,113,0.1);border-color:#fca5a5}
.mgw-probe-bar{display:flex;gap:10px;margin-bottom:12px;flex-wrap:wrap}
.mgw-probe-result{margin-top:6px}
.mgw-probe-tag{display:inline-block;font-size:11px;padding:3px 8px;border-radius:4px;margin:2px 4px 2px 0;background:rgba(255,255,255,0.04);color:var(--text-muted);font-family:monospace}
.mgw-probe-tag.codec{background:rgba(96,165,250,0.16);color:#60a5fa}
.mgw-probe-tag.audio{background:rgba(52,211,153,0.16);color:#34d399}
.mgw-probe-tag.dur{background:rgba(251,191,36,0.16);color:#fbbf24}
.mgw-probe-error{padding:10px 14px;background:rgba(248,113,113,0.08);border:1px solid rgba(248,113,113,0.25);border-radius:8px;color:#fca5a5;font-family:monospace;font-size:12px}
.mgw-probe-streams{margin-top:10px;font-size:12px;color:var(--text-muted);font-family:monospace}
.mgw-probe-streams details{margin-top:6px}
.mgw-probe-streams pre{background:rgba(0,0,0,0.3);padding:10px;border-radius:6px;max-height:300px;overflow:auto;font-size:11px;color:var(--text)}
</style>
</head>
<body>
<div class="app-layout">
<aside class="sidebar" id="sidebar">
  <div class="sidebar-logo"><div class="logo-icon">A</div><h1>Alpac</h1><button class="theme-btn" onclick="openThemeModal()" title="Настройки темы">&#x1F3A8;</button></div>
  <nav class="tabs" id="tabs-container">
    <div class="sidebar-section open" onclick="toggleNavGroup(this)"><span class="sec-icon">&#x1F3E0;</span> Обзор <span class="sec-chevron">&#9654;</span></div>
    <div class="nav-group open">
      <div class="tab active" data-tab="dashboard"><span class="tab-icon">&#x1F4CA;</span> Дашборд</div>
      <div class="tab" data-tab="server"><span class="tab-icon">&#x1F5A5;</span> Сервер</div>
      <div class="tab" data-tab="cluster"><span class="tab-icon">&#x1F310;</span> Кластер</div>
      <div class="tab" data-tab="telemetry"><span class="tab-icon">&#x1F4E1;</span> Телеметрия</div>
      <div class="tab" data-tab="logs"><span class="tab-icon">&#x1F4DC;</span> Логи</div>
    </div>
    <div class="sidebar-section" onclick="toggleNavGroup(this)"><span class="sec-icon">&#x1F4CB;</span> Управление <span class="sec-chevron">&#9654;</span></div>
    <div class="nav-group">
      <div class="tab" data-tab="users"><span class="tab-icon">&#x1F465;</span> Пользователи</div>
      <div class="tab" data-tab="groups"><span class="tab-icon">&#x1F46B;</span> Группы</div>
      <div class="tab" data-tab="promo"><span class="tab-icon">&#x1F3AB;</span> Промокоды</div>
      <div class="tab" data-tab="bans"><span class="tab-icon">&#x1F6AB;</span> Блокировки</div>
      <div class="tab" data-tab="waf"><span class="tab-icon">&#x1F6E1;</span> WAF</div>
      <div class="tab" data-tab="broadcast" id="tab-broadcast" style="display:none"><span class="tab-icon">&#x1F4E2;</span> Рассылка</div>
      <div class="tab" data-tab="feedback"><span class="tab-icon">&#x1F4AC;</span> Обратная связь</div>
    </div>
    <div class="sidebar-section" onclick="toggleNavGroup(this)"><span class="sec-icon">&#x1F3AC;</span> Контент <span class="sec-chevron">&#9654;</span></div>
    <div class="nav-group">
      <div class="tab" data-tab="balancers"><span class="tab-icon">&#x2696;</span> Балансеры</div>
      <div class="tab" data-tab="mediagw"><span class="tab-icon">&#x1F39E;</span> Media Gateway</div>
      <div class="tab" data-tab="modules"><span class="tab-icon">&#x1F9EA;</span> Модули</div>
      <div class="tab" data-tab="sisi-sources"><span class="tab-icon">&#x1F351;</span> SISI JS</div>
      <div class="tab" data-tab="music-sources"><span class="tab-icon">&#x1F3B5;</span> Music JS</div>
      <div class="tab" data-tab="env-presets"><span class="tab-icon">&#x1F6E0;</span> Пресеты</div>
      <div class="tab" data-tab="plugins"><span class="tab-icon">&#x1F9E9;</span> Плагины</div>
      <div class="tab" data-tab="proxy"><span class="tab-icon">&#x1F310;</span> Прокси</div>
      <div class="tab" data-tab="proxycore"><span class="tab-icon">&#x1F6E1;</span> ProxyCore</div>
      <div class="tab" data-tab="torrs" id="tab-torrs" style="display:none"><span class="tab-icon">&#x1F9F2;</span> Торренты</div>
      <div class="tab" data-tab="jacred" id="tab-jacred" style="display:none"><span class="tab-icon">&#x1F50E;</span> Парсер jacred</div>
      <div class="tab" data-tab="xsearch" id="tab-xsearch" style="display:none"><span class="tab-icon">&#x1F50D;</span> Поиск+</div>
      <div class="tab" data-tab="collections" id="tab-collections" style="display:none"><span class="tab-icon">&#x1F3AC;</span> Коллекции</div>
    </div>
    <div class="sidebar-section" onclick="toggleNavGroup(this)"><span class="sec-icon">&#x2699;</span> Настройки <span class="sec-chevron">&#9654;</span></div>
    <div class="nav-group">
      <div class="tab" data-tab="config"><span class="tab-icon">&#x2699;</span> Конфиг</div>
      <div class="tab" data-tab="lampa"><span class="tab-icon">&#x1F4A1;</span> Lampa</div>
      <div class="tab" data-tab="selfupdate"><span class="tab-icon">&#x1F680;</span> Обновления</div>
      <div class="tab" data-tab="deps"><span class="tab-icon">&#x1F4E6;</span> Зависимости</div>
      <div class="tab" data-tab="inspector"><span class="tab-icon">&#x1F50D;</span> Инспектор</div>
      <div class="tab" data-tab="constructor"><span class="tab-icon">&#x1F527;</span> Конструктор</div>
      <div class="tab" data-tab="appreplace"><span class="tab-icon">&#x1F504;</span> AppReplace</div>
      <div class="tab" data-tab="tgsettings" id="tab-tgsettings" style="display:none"><span class="tab-icon">&#x1F916;</span> TG Бот</div>
      <div class="tab" data-tab="skipintro" id="tab-skipintro" style="display:none"><span class="tab-icon">&#x23ED;</span> Интро/Аутро</div>
      <div class="tab" data-tab="calendar" id="tab-calendar" style="display:none"><span class="tab-icon">&#x1F4C5;</span> Расписание</div>
      <div class="tab" data-tab="iptv" id="tab-iptv" style="display:none"><span class="tab-icon">&#x1F4FA;</span> IPTV</div>
    </div>
  </nav>
  <div class="sidebar-footer">
    <a class="logout" href="/tg/auth/logout">&#10148; Выйти</a>
  </div>
</aside>
<div class="sidebar-overlay" id="sidebar-overlay" onclick="toggleSidebar()"></div>
<main class="main-area">
  <div class="top-bar">
    <button class="mobile-toggle" onclick="toggleSidebar()">&#9776;</button>
    <span class="page-title" id="page-title">Дашборд</span>
  </div>
  <div class="restart-banner" id="restart-banner">Настройки изменены. Требуется перезапуск сервера для применения.</div>
  <div class="content">
  <!-- DASHBOARD -->
  <div class="panel active" id="panel-dashboard">
    <!-- Hero row -->
    <div class="dash-hero">
      <div class="dash-hero-left">
        <div class="dash-hero-title">Lampac-Go</div>
        <div class="dash-hero-ver" id="dash-version"></div>
      </div>
      <div class="dash-hero-metrics" id="dash-hero-metrics"></div>
      <div class="dash-hero-right">
        <label class="dash-auto-label"><input type="checkbox" id="dash-auto-refresh" checked> Авто-обновление</label>
      </div>
    </div>

    <!-- Dynamic widget area -->
    <div id="dash-widget-area" class="dash-widget-grid"></div>
    <div class="dash-widget-customize"><button onclick="openWidgetPicker()">&#x2699; Настроить виджеты</button></div>

    <!-- Widget picker modal -->
    <div class="wpick-overlay" id="wpick-overlay" onclick="if(event.target===this)closeWidgetPicker()">
      <div class="wpick-panel">
        <div class="wpick-title">&#x2699; Настройка виджетов</div>
        <div class="wpick-list" id="wpick-list"></div>
        <div class="wpick-actions">
          <button class="wpick-save" onclick="saveWidgetPicker()">Сохранить</button>
          <button class="wpick-reset" onclick="resetWidgetPicker()">Сбросить</button>
          <button class="wpick-cancel" onclick="closeWidgetPicker()">Отмена</button>
        </div>
      </div>
    </div>
  </div>
  <!-- USERS -->
  <div class="panel" id="panel-users">
    <div class="section-title">&#x1F465; Управление доступом</div>
    <div class="section-desc">Список устройств с активной TG-авторизацией. Нажмите на пользователя для настройки.</div>
    <div style="margin:12px 0;display:flex;align-items:center;gap:12px;flex-wrap:wrap">
      <button class="btn btn-primary" onclick="createDeviceToken()">📺 Device Token</button>
      <button class="btn btn-primary" onclick="createBKitSessionPrompt()" style="background:#4f46e5">🌐 Browser Kit</button>
      <span id="users-counter" style="color:var(--text-muted);font-size:13px"></span>
      <div style="margin-left:auto;display:flex;align-items:center;gap:8px">
        <input type="text" id="users-search" placeholder="Поиск: имя, TG ID, UID, IP" oninput="usersSearchQuery=this.value;window.usersPage=1;renderUsers()" style="width:200px;padding:4px 8px;font-size:12px;background:var(--surface2);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:6px;outline:none">
        <span style="color:var(--text-dim);font-size:12px">На стр:</span>
        <select class="input-sm" id="users-per-page" onchange="window.usersPage=1;renderUsers()" style="width:70px;padding:4px 6px;font-size:12px">
          <option value="10">10</option><option value="50" selected>50</option><option value="100">100</option><option value="0">Все</option>
        </select>
      </div>
    </div>
    <div id="users-group-pills" style="display:flex;flex-wrap:wrap;gap:6px;margin:4px 0 12px;padding-bottom:8px;border-bottom:1px solid rgba(255,255,255,0.06)"></div>
    <table><thead><tr><th>Пользователь</th><th>Telegram ID</th><th>Группа</th><th>Премиум</th><th>Устройства</th><th>Лимит</th><th>Создан</th><th>Истекает</th><th></th></tr></thead>
    <tbody id="users-body"></tbody></table>
    <div id="users-pagination" style="margin-top:12px;display:flex;justify-content:center;align-items:center;gap:8px"></div>
    <div class="empty" id="users-empty" style="display:none">Нет активных токенов</div>
    <div style="margin-top:24px;border-top:1px solid rgba(255,255,255,0.06);padding-top:16px">
      <div class="section-title" style="padding:0 0 8px">🌐 Browser Kit — сессии</div>
      <div class="section-desc">Токены для входа в Kit без Telegram. Каждый токен — отдельный пользователь.</div>
      <div id="bkit-sessions-list"></div>
      <div style="margin-top:8px;display:flex;gap:8px;align-items:center">
        <input class="input-sm" id="bkit-session-name" placeholder="Имя пользователя" style="flex:1">
        <button class="btn btn-primary btn-sm" onclick="createBKitSession()" style="background:#4f46e5">Создать</button>
      </div>
    </div>
  </div>
  <!-- User side panel -->
  <div class="user-panel-overlay" id="user-panel-overlay" onclick="closeUserPanel()"></div>
  <div class="user-panel" id="user-panel">
    <button class="up-close" onclick="closeUserPanel()">&times;</button>
    <div id="user-panel-content"></div>
  </div>
  <!-- PROMO CODES -->
  <!-- GROUPS -->
  <div class="panel" id="panel-groups">
    <div class="section-title">&#x1F46B; Группы пользователей</div>
    <div class="section-desc">Группировка пользователей с разграничением доступа. Каждая группа определяет лимит устройств, доступные балансеры, TorrServer и SISI. Per-user настройки имеют приоритет над группой.</div>
    <div id="grp-list"></div>
    <div style="margin-top:16px">
      <button class="btn btn-primary" onclick="createGroupModal()">+ Создать группу</button>
    </div>
  </div>
  <!-- Group edit slide-out -->
  <div class="pg-settings-overlay" id="grp-overlay" onclick="closeGroupPanel()"></div>
  <div class="pg-settings-panel" id="grp-panel" style="max-width:480px">
    <button class="up-close" onclick="closeGroupPanel()">&times;</button>
    <div id="grp-panel-content"></div>
  </div>
  <div class="panel" id="panel-promo">
    <div class="section-title">&#x1F3AB; Промокоды</div>
    <div class="section-desc">Генерация промокодов для доступа без Telegram. Пользователь вводит код в окне авторизации.</div>
    <div style="margin:12px 0;display:flex;align-items:center;gap:8px;flex-wrap:wrap">
      <label style="font-size:13px;color:var(--text-dim)">Кол-во:</label>
      <input type="number" id="promo-gen-count" class="input-sm" value="1" min="1" max="100" style="width:60px">
      <label style="font-size:13px;color:var(--text-dim)">Дней:</label>
      <input type="number" id="promo-gen-days" class="input-sm" value="30" min="1" max="3650" style="width:70px">
      <label style="font-size:13px;color:var(--text-dim)">Макс. использований:</label>
      <input type="number" id="promo-gen-uses" class="input-sm" value="1" min="0" max="10000" style="width:60px" title="0 = безлимит">
      <label style="font-size:13px;color:var(--text-dim)">Срок действия (ч):</label>
      <input type="number" id="promo-gen-hours" class="input-sm" value="0" min="0" max="87600" style="width:70px" title="0 = без ограничений">
      <button class="btn btn-primary" onclick="generatePromo()">&#x2795; Сгенерировать</button>
    </div>
    <div id="promo-gen-result" style="margin:8px 0;font-size:13px"></div>
    <div id="promo-list" style="margin-top:12px"></div>
  </div>
  <!-- BANS -->
  <div class="panel" id="panel-bans">
    <div class="section-title">&#x1F6AB; Блокировки</div>
    <div class="section-desc">Управление блокировками по IP, устройству, Telegram ID, fingerprint и стране.</div>
    <div style="margin:12px 0;display:flex;align-items:center;gap:12px;flex-wrap:wrap">
      <span id="bans-stats" style="color:var(--text-muted);font-size:13px"></span>
      <button class="btn btn-primary" onclick="showAddBanForm()" style="margin-left:auto">+ Добавить блокировку</button>
    </div>
    <div id="ban-add-form" style="display:none;background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:16px;margin-bottom:16px">
      <div style="display:flex;gap:12px;flex-wrap:wrap;align-items:end">
        <div>
          <label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px">Тип</label>
          <select id="ban-type" class="input-sm" style="width:140px;padding:6px 8px;font-size:13px">
            <option value="ip">IP</option>
            <option value="cidr">CIDR (подсеть)</option>
            <option value="uid">Device UID</option>
            <option value="tg_id">Telegram ID</option>
            <option value="fingerprint">Fingerprint</option>
            <option value="country">Страна (ISO)</option>
          </select>
        </div>
        <div style="flex:1;min-width:200px">
          <label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px">Значение</label>
          <input type="text" id="ban-value" class="input-sm" placeholder="192.168.1.1 / abc123 / RU" style="width:100%;padding:6px 8px;font-size:13px">
        </div>
        <div style="flex:1;min-width:150px">
          <label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px">Причина</label>
          <input type="text" id="ban-reason" class="input-sm" placeholder="Описание (необязательно)" style="width:100%;padding:6px 8px;font-size:13px">
        </div>
        <button class="btn btn-primary" onclick="addBan()">Добавить</button>
        <button class="btn" onclick="document.getElementById('ban-add-form').style.display='none'" style="background:rgba(255,255,255,0.08)">Отмена</button>
      </div>
    </div>
    <table>
      <thead><tr><th>Тип</th><th>Значение</th><th>Причина</th><th>Дата</th><th></th></tr></thead>
      <tbody id="bans-body"></tbody>
    </table>
    <div class="empty" id="bans-empty" style="display:none">Нет блокировок</div>
  </div>
  <!-- WAF -->
  <div class="panel" id="panel-waf">
    <div class="section-title">&#x1F6E1; WAF — сетевой брандмауэр</div>
    <div class="section-desc">IP/гео-фильтры, rate-limit, brute-force, ручные баны. Изменения применяются <b>без перезапуска</b> (hot-reload).</div>

    <div id="waf-toast" style="display:none;background:rgba(64,180,120,0.15);border:1px solid rgba(64,180,120,0.4);border-radius:8px;padding:8px 12px;margin:8px 0;color:#5dd99a;font-size:13px"></div>

    <!-- Switches + buttons -->
    <div style="display:flex;flex-wrap:wrap;gap:12px;align-items:center;margin:12px 0">
      <label class="field-row" style="margin:0"><span class="field-key" style="min-width:auto;margin-right:8px">Включен</span><label class="toggle"><input type="checkbox" id="waf-enable"><span class="slider"></span></label></label>
      <label class="field-row" style="margin:0"><span class="field-key" style="min-width:auto;margin-right:8px">Пропускать локальные IP</span><label class="toggle"><input type="checkbox" id="waf-bypassLocal"><span class="slider"></span></label></label>
      <label class="field-row" style="margin:0"><span class="field-key" style="min-width:auto;margin-right:8px">Brute-force защита</span><label class="toggle"><input type="checkbox" id="waf-brute"><span class="slider"></span></label></label>
      <button class="btn btn-primary" onclick="wafSave()" style="margin-left:auto">&#x1F4BE; Сохранить</button>
      <button class="btn" onclick="wafReload()" title="Перечитать init.conf без сохранения">&#x267B; Перечитать конфиг</button>
      <button class="btn" onclick="wafLoad()" title="Обновить статистику">&#x1F504; Обновить</button>
    </div>

    <!-- Stats cards -->
    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px;margin:16px 0">
      <div style="background:var(--surface2);border:1px solid rgba(255,255,255,0.06);border-radius:8px;padding:14px">
        <div style="color:var(--text-muted);font-size:11px;text-transform:uppercase;letter-spacing:0.5px">Всего блокировок (24ч)</div>
        <div id="waf-total24h" style="font-size:28px;font-weight:600;margin-top:6px">—</div>
      </div>
      <div style="background:var(--surface2);border:1px solid rgba(255,255,255,0.06);border-radius:8px;padding:14px">
        <div style="color:var(--text-muted);font-size:11px;text-transform:uppercase;letter-spacing:0.5px">По причинам (24ч)</div>
        <div id="waf-reasons" style="margin-top:6px;font-size:13px">—</div>
      </div>
      <div style="background:var(--surface2);border:1px solid rgba(255,255,255,0.06);border-radius:8px;padding:14px">
        <div style="color:var(--text-muted);font-size:11px;text-transform:uppercase;letter-spacing:0.5px">Топ путей (1ч)</div>
        <div id="waf-top-paths" style="margin-top:6px;font-size:12px">—</div>
      </div>
      <div style="background:var(--surface2);border:1px solid rgba(255,255,255,0.06);border-radius:8px;padding:14px">
        <div style="color:var(--text-muted);font-size:11px;text-transform:uppercase;letter-spacing:0.5px">Топ IP (1ч)</div>
        <div id="waf-top-ips" style="margin-top:6px;font-size:12px">—</div>
      </div>
    </div>

    <!-- Manual bans -->
    <div class="section-title" style="margin-top:24px;font-size:15px">&#x1F6AB; Ручные баны</div>
    <div class="section-desc">IP-уровень. Срабатывают раньше всех правил. TTL=0 — постоянный.</div>
    <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:end;margin:8px 0 12px">
      <div><label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px">IP / CIDR</label><input type="text" id="waf-ban-ip" class="input-sm" placeholder="1.2.3.4 или 1.2.3.0/24" style="padding:6px 8px;font-size:13px"></div>
      <div><label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px">TTL, сек (0 = навсегда)</label><input type="number" id="waf-ban-ttl" class="input-sm" value="3600" min="0" style="padding:6px 8px;font-size:13px;width:140px"></div>
      <div style="flex:1;min-width:160px"><label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px">Причина</label><input type="text" id="waf-ban-reason" class="input-sm" placeholder="спам, abuse..." style="padding:6px 8px;font-size:13px;width:100%"></div>
      <button class="btn btn-primary" onclick="wafBanAdd()">+ Забанить</button>
    </div>
    <table><thead><tr><th>IP</th><th>Причина</th><th>До</th><th>Создан</th><th></th></tr></thead><tbody id="waf-bans-body"></tbody></table>
    <div class="empty" id="waf-bans-empty" style="display:none">Нет ручных банов</div>

    <!-- Brute-force -->
    <div class="section-title" style="margin-top:24px;font-size:15px">&#x1F501; Brute-force</div>
    <div class="field-row"><span class="field-key">Лимит уникальных device-id с одного IP за минуту</span><input type="number" id="waf-bruteLimit" min="0" class="input-sm" style="width:120px;padding:6px 8px;font-size:13px"></div>

    <!-- Rate limits -->
    <div class="section-title" style="margin-top:24px;font-size:15px">&#x23F1; Rate-limit</div>
    <div class="field-row"><span class="field-key">Глобальный лимит запросов/мин (limit_req)</span><input type="number" id="waf-limitReq" min="0" class="input-sm" style="width:120px;padding:6px 8px;font-size:13px"></div>
    <div style="margin-top:8px">
      <div style="color:var(--text-muted);font-size:12px;margin-bottom:6px">Per-route правила (limit_map): <code>regex path → лимит за N секунд</code>. По одному в строке: <code>regex|limit|seconds[|pathId][|queryIds=a,b]</code></div>
      <textarea id="waf-limitMap" rows="5" style="width:100%;font-family:monospace;font-size:12px;background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:8px;color:var(--text)" placeholder="^/api/admin|60|60&#10;^/lite/|120|60|pathId"></textarea>
    </div>

    <!-- IP lists -->
    <div class="section-title" style="margin-top:24px;font-size:15px">&#x1F4CB; Списки IP / стран</div>
    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:12px;margin-top:8px">
      <div>
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">White-list IP (полный пропуск)</label>
        <textarea id="waf-white" rows="4" style="width:100%;font-family:monospace;font-size:12px;background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:8px;color:var(--text)" placeholder="1.2.3.4&#10;10.0.0.0/8"></textarea>
      </div>
      <div>
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">IP / CIDR — запрет</label>
        <textarea id="waf-ipsDeny" rows="4" style="width:100%;font-family:monospace;font-size:12px;background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:8px;color:var(--text)"></textarea>
      </div>
      <div>
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">IP / CIDR — разрешено</label>
        <textarea id="waf-ipsAllow" rows="4" style="width:100%;font-family:monospace;font-size:12px;background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:8px;color:var(--text)" placeholder="(если задано — пропускаются только эти)"></textarea>
      </div>
      <div>
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">Страны — запрет (ISO, через запятую)</label>
        <input type="text" id="waf-countryDeny" class="input-sm" style="width:100%;font-family:monospace;font-size:12px;padding:6px 8px" placeholder="CN, KP, IR">
        <label style="display:block;font-size:12px;color:var(--text-muted);margin:8px 0 4px">Страны — разрешено</label>
        <input type="text" id="waf-countryAllow" class="input-sm" style="width:100%;font-family:monospace;font-size:12px;padding:6px 8px" placeholder="RU, BY, KZ, UA (если задано — только они)">
      </div>
    </div>

    <!-- Whitelist paths -->
    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:12px">
      <div>
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">Whitelist путей (точные)</label>
        <textarea id="waf-wlPaths" rows="3" style="width:100%;font-family:monospace;font-size:12px;background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:8px;color:var(--text)" placeholder="/api/version&#10;/healthz"></textarea>
      </div>
      <div>
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">Whitelist префиксы путей</label>
        <textarea id="waf-wlPrefixes" rows="3" style="width:100%;font-family:monospace;font-size:12px;background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:8px;color:var(--text)" placeholder="/proxy/&#10;/static/"></textarea>
      </div>
    </div>

    <!-- Header rules -->
    <div class="section-title" style="margin-top:24px;font-size:15px">&#x1F4DC; HTTP-заголовки — deny</div>
    <div style="color:var(--text-muted);font-size:12px;margin-bottom:6px">Регекс по значению. По одному в строке: <code>HeaderName|regex</code></div>
    <textarea id="waf-headersDeny" rows="4" style="width:100%;font-family:monospace;font-size:12px;background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:8px;color:var(--text)" placeholder="User-Agent|curl|wget|python-requests&#10;Referer|evil\.com"></textarea>
  </div>
  <!-- BALANCERS -->
  <div class="panel" id="panel-balancers">
    <div class="section-title">&#x2696; Балансеры</div>
    <div class="section-desc">Полная настройка каждого балансера. Изменения сохраняются в init.conf (требуется перезапуск).</div>
    <div class="bal-search-wrap" style="display:flex;gap:8px;align-items:center"><input class="bal-search" id="bal-search" placeholder="Поиск балансера..." oninput="filterBalancers(this.value)" style="flex:1"><button class="btn btn-sm" onclick="openReorder()" style="white-space:nowrap">&#x2195; Порядок</button></div>
    <div id="reorder-panel" style="display:none;margin:12px 0;padding:12px;border-radius:8px;background:var(--card-bg);border:1px solid var(--border)">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
        <b>Порядок балансеров</b>
        <div><button class="btn btn-primary btn-sm" onclick="saveReorder()">Сохранить</button> <button class="btn btn-sm" onclick="closeReorder()">Закрыть</button></div>
      </div>
      <div class="field-row" style="margin-bottom:8px"><span class="field-key">Свой порядок</span><label class="toggle"><input type="checkbox" id="custom-order-cb" onchange="customOrder=this.checked;document.getElementById('sort-label').textContent=this.checked?'свой порядок':'по качеству (4K→FHD→SD)'"><span class="slider"></span></label><span style="color:var(--text-muted);font-size:12px;margin-left:8px" id="sort-label"></span></div>
      <div class="section-desc" style="margin-bottom:8px">Порядок определяет очерёдность в выдаче у пользователя. Перетаскивайте или используйте кнопки.</div>
      <div id="reorder-list"></div>
    </div>
    <div id="balancers-list"></div>
  </div>
  <!-- MODULES (JS-модули источников) -->
  <div class="panel" id="panel-modules">
    <style>
      .mods-head{display:flex;align-items:center;gap:10px;margin-bottom:12px;flex-wrap:wrap}
      .mods-head .section-title{margin-bottom:0}
      .mods-head-btns{margin-left:auto;display:flex;gap:6px;flex-wrap:wrap}
      .mods-toolbar{display:flex;gap:8px;align-items:center;margin-bottom:14px;flex-wrap:wrap}
      .mods-toolbar input,.mods-toolbar select{background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 12px;border-radius:8px;font-size:12px}
      .mods-toolbar input{flex:1;min-width:160px}
      .mods-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:14px}
      .mod-card{background:var(--surface2);border:1px solid var(--border);border-radius:14px;padding:16px;display:flex;flex-direction:column;gap:10px;transition:border-color .18s,transform .18s;position:relative;overflow:hidden}
      .mod-card:hover{border-color:var(--accent-a25);transform:translateY(-1px)}
      .mod-card.disabled{opacity:.6}
      .mod-card.err{border-color:var(--danger,#c44)}
      .mod-card-head{display:flex;align-items:flex-start;gap:12px}
      .mod-icon{width:42px;height:42px;border-radius:10px;flex-shrink:0;background:linear-gradient(135deg,var(--accent-a25),var(--surface3));display:flex;align-items:center;justify-content:center;font-size:22px}
      .mod-title{flex:1;min-width:0}
      .mod-name{font-size:15px;font-weight:700;color:var(--text);display:flex;align-items:center;gap:6px;flex-wrap:wrap}
      .mod-name .mod-q{background:var(--accent);color:#fff;font-size:10px;font-weight:700;padding:1px 6px;border-radius:6px;letter-spacing:.3px}
      .mod-meta{font-size:11px;color:var(--text-dim);margin-top:2px;display:flex;gap:8px;flex-wrap:wrap}
      .mod-desc{font-size:12px;color:var(--text-muted);line-height:1.5;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden;min-height:36px}
      .mod-tags{display:flex;flex-wrap:wrap;gap:4px}
      .mod-tag{font-size:10px;background:var(--surface3);color:var(--text-muted);padding:2px 8px;border-radius:10px}
      .mod-tag.ua{background:#0057b8;color:#fff}
      .mod-tag.anime{background:#e83f6f;color:#fff}
      .mod-actions{display:flex;gap:6px;flex-wrap:wrap;margin-top:auto;align-items:center}
      .mod-actions .toggle{flex:0 0 auto}
      .mod-actions .spacer{flex:1}
      .mod-actions .btn{font-size:12px;padding:5px 10px}
      .mod-err{background:#3a1a1a;border:1px solid #c44;color:#ffbcbc;font-size:11px;padding:8px 10px;border-radius:8px;word-break:break-word;font-family:monospace}
      .mod-stats{font-size:10.5px;color:var(--text-dim);display:flex;gap:10px;flex-wrap:wrap}
      .mod-stats .ok{color:#9fdbb9}
      .mod-stats .fail{color:#ff9b9b}
      /* Editor slide-out */
      .mod-drawer{position:fixed;top:0;right:-780px;width:760px;max-width:96vw;height:100vh;background:var(--surface);border-left:1px solid var(--border);box-shadow:-12px 0 36px rgba(0,0,0,.35);z-index:1500;display:flex;flex-direction:column;transition:right .25s ease}
      .mod-drawer.open{right:0}
      .mod-drawer-head{padding:14px 18px;border-bottom:1px solid var(--border);display:flex;align-items:center;gap:10px}
      .mod-drawer-head .mod-name{font-size:16px}
      .mod-drawer-tabs{display:flex;gap:4px;padding:10px 18px;border-bottom:1px solid var(--border);background:var(--surface2)}
      .mod-drawer-tabs .dt{padding:6px 14px;border-radius:8px;font-size:12px;cursor:pointer;color:var(--text-muted)}
      .mod-drawer-tabs .dt.active{background:var(--accent);color:#fff}
      .mod-drawer-body{flex:1;overflow:auto;padding:14px 18px}
      .mod-drawer-foot{padding:10px 18px;border-top:1px solid var(--border);display:flex;gap:8px;align-items:center}
      #mod-editor{width:100%;min-height:520px;border:1px solid var(--border);border-radius:10px;background:#0d1117;color:#cdd9e5;font-family:"JetBrains Mono","Consolas",monospace;font-size:13px;padding:12px;resize:vertical;line-height:1.45;tab-size:2}
      .mod-logs{background:#0d1117;color:#b9c1cc;font-family:"JetBrains Mono","Consolas",monospace;font-size:12px;height:520px;overflow:auto;border:1px solid var(--border);border-radius:10px;padding:10px}
      .mod-logs .ll{padding:2px 0;border-bottom:1px solid #161b22}
      .mod-logs .lt{color:#6e7681;margin-right:6px}
      .mod-logs .lm{color:#58a6ff}
      .mod-logs .lv-warn{color:#d29922}
      .mod-logs .lv-error{color:#f85149}
      .mod-cfg-row{display:grid;grid-template-columns:200px 1fr;gap:10px;align-items:center;padding:6px 0;border-bottom:1px solid var(--border)}
      .mod-cfg-row label{font-size:12px;color:var(--text-muted)}
      .mod-cfg-row input,.mod-cfg-row textarea,.mod-cfg-row select{background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:6px 10px;border-radius:8px;font-size:12px;width:100%}
      .mod-cfg-row .desc{grid-column:2/3;font-size:11px;color:var(--text-dim);margin-top:-2px}
      .mod-backdrop{position:fixed;inset:0;background:rgba(0,0,0,.5);z-index:1400;display:none}
      .mod-backdrop.show{display:block}
      .mod-empty{padding:50px 24px;text-align:center;color:var(--text-dim);font-size:14px}
      .mod-empty .big{font-size:48px;margin-bottom:12px;opacity:.4}
    </style>
    <div class="mods-head">
      <div class="section-title">&#x1F9EA; Модули источников</div>
      <div class="mods-head-btns">
        <button class="btn btn-sm" onclick="mMods.refresh()">&#x21BB; Обновить</button>
        <button class="btn btn-sm" onclick="mMods.showHub()">&#x1F310; Каталог Hub</button>
        <button class="btn btn-sm btn-primary" onclick="mMods.showInstall()">&#x2795; Установить</button>
      </div>
    </div>
    <div class="section-desc">JS-модули балансеров, которые можно редактировать без пересборки сервера. Достаточно одного файла <code>index.js</code> + <code>manifest.json</code> в папке <code>modules/&lt;id&gt;/</code>. Горячая перезагрузка.</div>
    <div class="mods-toolbar">
      <input id="mods-search" placeholder="Поиск по названию или тегу..." oninput="mMods.filter()">
      <select id="mods-filter" onchange="mMods.filter()">
        <option value="">Все</option>
        <option value="enabled">Включённые</option>
        <option value="disabled">Выключенные</option>
        <option value="error">С ошибкой</option>
        <option value="ukrainian">Украинские</option>
        <option value="anime">Аниме</option>
      </select>
    </div>
    <div class="mods-grid" id="mods-grid"></div>
    <div class="mod-empty" id="mods-empty" style="display:none">
      <div class="big">&#x1F9EA;</div>
      <div>Нет установленных модулей.</div>
      <div style="margin-top:16px"><button class="btn btn-primary" onclick="mMods.showInstall()">Установить первый модуль</button></div>
    </div>
  </div>
  <!-- Module editor drawer -->
  <div class="mod-backdrop" id="mod-backdrop" onclick="mMods.closeDrawer()"></div>
  <div class="mod-drawer" id="mod-drawer">
    <div class="mod-drawer-head">
      <div class="mod-icon" id="mod-d-icon">&#x1F9EA;</div>
      <div class="mod-title">
        <div class="mod-name" id="mod-d-name"></div>
        <div class="mod-meta" id="mod-d-meta"></div>
      </div>
      <button class="btn btn-sm" onclick="mMods.closeDrawer()">Закрыть</button>
    </div>
    <div class="mod-drawer-tabs">
      <div class="dt active" data-d="info" onclick="mMods.drawerTab('info')">Обзор</div>
      <div class="dt" data-d="config" onclick="mMods.drawerTab('config')">Настройки</div>
      <div class="dt" data-d="code" onclick="mMods.drawerTab('code')">Код</div>
      <div class="dt" data-d="logs" onclick="mMods.drawerTab('logs')">Логи</div>
    </div>
    <div class="mod-drawer-body">
      <div id="mod-pane-info"></div>
      <div id="mod-pane-config" style="display:none"></div>
      <div id="mod-pane-code" style="display:none"><textarea id="mod-editor" spellcheck="false"></textarea></div>
      <div id="mod-pane-logs" style="display:none"><div class="mod-logs" id="mod-logs-box"></div></div>
    </div>
    <div class="mod-drawer-foot" id="mod-drawer-foot"></div>
  </div>
  <!-- Install modal -->
  <div class="mod-backdrop" id="mod-install-bd" onclick="mMods.hideInstall()"></div>
  <div class="mod-drawer" id="mod-install" style="width:520px">
    <div class="mod-drawer-head">
      <div class="mod-icon">&#x2795;</div>
      <div class="mod-title">
        <div class="mod-name">Установить модуль</div>
        <div class="mod-meta">ZIP-архив или URL архива</div>
      </div>
      <button class="btn btn-sm" onclick="mMods.hideInstall()">Закрыть</button>
    </div>
    <div class="mod-drawer-body">
      <div style="margin-bottom:12px">
        <label style="font-size:12px;color:var(--text-muted)">URL на zip</label>
        <input id="mod-install-url" placeholder="https://example.com/module.zip" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px;font-size:12px;margin-top:4px">
        <button class="btn btn-sm btn-primary" style="margin-top:8px" onclick="mMods.installFromURL()">Установить с URL</button>
      </div>
      <div style="margin:14px 0;border-top:1px solid var(--border)"></div>
      <div>
        <label style="font-size:12px;color:var(--text-muted)">Загрузить архив</label><br>
        <input id="mod-install-file" type="file" accept=".zip" style="margin-top:8px">
        <button class="btn btn-sm btn-primary" style="margin-top:8px" onclick="mMods.installFromFile()">Загрузить</button>
      </div>
    </div>
  </div>
  <!-- Hub catalog modal -->
  <div class="mod-backdrop" id="mod-hub-bd" onclick="mMods.hideHub()"></div>
  <div class="mod-drawer" id="mod-hub" style="width:760px">
    <div class="mod-drawer-head">
      <div class="mod-icon">&#x1F310;</div>
      <div class="mod-title">
        <div class="mod-name">Каталог Hub</div>
        <div class="mod-meta" id="mod-hub-meta">hub.alcopa.cc — модули от сообщества</div>
      </div>
      <button class="btn btn-sm" onclick="mMods.refreshHub()">&#x21BB;</button>
      <button class="btn btn-sm" onclick="mMods.hideHub()">Закрыть</button>
    </div>
    <div class="mod-drawer-body">
      <div style="margin-bottom:12px;display:flex;gap:8px;align-items:center;flex-wrap:wrap">
        <label style="font-size:12px;color:var(--text-muted)">URL каталога:</label>
        <input id="mod-hub-url" style="flex:1;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 10px;border-radius:7px;font-size:12px"
          value="https://hub.alcopa.cc/hub/api/modules-catalog.json">
        <input id="mod-hub-search" placeholder="🔍 поиск" oninput="mMods.filterHub()"
          style="background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 10px;border-radius:7px;font-size:12px;width:180px">
      </div>
      <div id="mod-hub-status" style="font-size:12px;color:var(--text-muted);margin-bottom:8px"></div>
      <div id="mod-hub-list"></div>
    </div>
  </div>
  <!-- SISI JS SOURCES -->
  <div class="panel" id="panel-sisi-sources">
    <div class="mods-head">
      <div class="section-title">&#x1F351; SISI JS &#x2014; &#x43A;&#x43B;&#x443;&#x431;&#x43D;&#x438;&#x447;&#x43A;&#x430; (goja)</div>
      <div class="mods-head-btns">
        <button class="btn btn-sm" onclick="sisiMod.refresh()">&#x21BB; &#x41E;&#x431;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C;</button>
        <button class="btn btn-sm" onclick="sisiMod.showHub()">&#x1F310; &#x41A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433; Hub</button>
        <button class="btn btn-sm btn-primary" onclick="sisiMod.showInstall()">&#x2795; &#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C;</button>
      </div>
    </div>
    <div class="section-desc">JS-&#x43C;&#x43E;&#x434;&#x443;&#x43B;&#x438; 18+ &#x438;&#x441;&#x442;&#x43E;&#x447;&#x43D;&#x438;&#x43A;&#x43E;&#x432; (goja). &#x422;&#x430; &#x436;&#x435; <code>index.js</code> + <code>manifest.json</code>, &#x447;&#x442;&#x43E; &#x438; &#x43E;&#x431;&#x44B;&#x447;&#x43D;&#x44B;&#x435; &#x43C;&#x43E;&#x434;&#x443;&#x43B;&#x438;, &#x43D;&#x43E; &#x43B;&#x435;&#x436;&#x430;&#x442; &#x432; <code>sisi_sources/&lt;id&gt;/</code> &#x438; &#x43F;&#x440;&#x43E;&#x43A;&#x441;&#x438;&#x440;&#x443;&#x44E;&#x442;&#x441;&#x44F; &#x43A;&#x430;&#x43A; <code>/sisi/cust/{id}</code>.
      JS-&#x43A;&#x43E;&#x43D;&#x442;&#x440;&#x430;&#x43A;&#x442;: <code>function handle(inv)</code>, <code>inv.query.action</code> = "list" | "video",
      &#x432;&#x43E;&#x437;&#x432;&#x440;&#x430;&#x449;&#x430;&#x435;&#x442; <code>{list, total_pages, menu}</code> &#x438;&#x43B;&#x438; <code>{qualitys, title, poster, qualitys_headers}</code>.</div>
    <div class="mods-toolbar">
      <input id="ss-search" placeholder="&#x41F;&#x43E;&#x438;&#x441;&#x43A;..." oninput="sisiMod.filter()">
    </div>
    <div class="mods-grid" id="ss-grid"></div>
    <div class="mod-empty" id="ss-empty" style="display:none">
      <div class="big">&#x1F351;</div>
      <div>&#x41D;&#x435;&#x442; &#x443;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x43B;&#x435;&#x43D;&#x43D;&#x44B;&#x445; SISI JS &#x438;&#x441;&#x442;&#x43E;&#x447;&#x43D;&#x438;&#x43A;&#x43E;&#x432;.</div>
      <div style="margin-top:16px"><button class="btn btn-primary" onclick="sisiMod.showInstall()">&#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C; &#x43F;&#x435;&#x440;&#x432;&#x44B;&#x439;</button></div>
    </div>
  </div>
  <!-- SISI install modal -->
  <div class="mod-backdrop" id="ss-install-bd" onclick="sisiMod.hideInstall()"></div>
  <div class="mod-drawer" id="ss-install" style="width:520px">
    <div class="mod-drawer-head">
      <div class="mod-icon">&#x2795;</div>
      <div class="mod-title">
        <div class="mod-name">&#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C; SISI JS &#x438;&#x441;&#x442;&#x43E;&#x447;&#x43D;&#x438;&#x43A;</div>
        <div class="mod-meta">ZIP-&#x430;&#x440;&#x445;&#x438;&#x432; &#x438;&#x43B;&#x438; URL &#x430;&#x440;&#x445;&#x438;&#x432;&#x430;</div>
      </div>
      <button class="btn btn-sm" onclick="sisiMod.hideInstall()">&#x417;&#x430;&#x43A;&#x440;&#x44B;&#x442;&#x44C;</button>
    </div>
    <div class="mod-drawer-body">
      <div style="margin-bottom:12px">
        <label style="font-size:12px;color:var(--text-muted)">URL &#x43D;&#x430; zip</label>
        <input id="ss-install-url" placeholder="https://example.com/source.zip" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px;font-size:12px;margin-top:4px">
        <button class="btn btn-sm btn-primary" style="margin-top:8px" onclick="sisiMod.installFromURL()">&#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C; &#x441; URL</button>
      </div>
      <div style="margin:14px 0;border-top:1px solid var(--border)"></div>
      <div>
        <label style="font-size:12px;color:var(--text-muted)">&#x417;&#x430;&#x433;&#x440;&#x443;&#x437;&#x438;&#x442;&#x44C; &#x430;&#x440;&#x445;&#x438;&#x432;</label><br>
        <input id="ss-install-file" type="file" accept=".zip" style="margin-top:8px">
        <button class="btn btn-sm btn-primary" style="margin-top:8px" onclick="sisiMod.installFromFile()">&#x417;&#x430;&#x433;&#x440;&#x443;&#x437;&#x438;&#x442;&#x44C;</button>
      </div>
    </div>
  </div>
  <!-- SISI hub catalog modal -->
  <div class="mod-backdrop" id="ss-hub-bd" onclick="sisiMod.hideHub()"></div>
  <div class="mod-drawer" id="ss-hub" style="width:760px">
    <div class="mod-drawer-head">
      <div class="mod-icon">&#x1F310;</div>
      <div class="mod-title">
        <div class="mod-name">&#x41A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433; SISI JS</div>
        <div class="mod-meta">hub.alcopa.cc &#x2014; goja-&#x438;&#x441;&#x442;&#x43E;&#x447;&#x43D;&#x438;&#x43A;&#x438; &#x43E;&#x442; &#x441;&#x43E;&#x43E;&#x431;&#x449;&#x435;&#x441;&#x442;&#x432;&#x430;</div>
      </div>
      <button class="btn btn-sm" onclick="sisiMod.refreshHub()">&#x21BB;</button>
      <button class="btn btn-sm" onclick="sisiMod.hideHub()">&#x417;&#x430;&#x43A;&#x440;&#x44B;&#x442;&#x44C;</button>
    </div>
    <div class="mod-drawer-body">
      <div style="margin-bottom:12px;display:flex;gap:8px;align-items:center;flex-wrap:wrap">
        <label style="font-size:12px;color:var(--text-muted)">URL &#x43A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433;&#x430;:</label>
        <input id="ss-hub-url" style="flex:1;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 10px;border-radius:7px;font-size:12px"
          value="https://hub.alcopa.cc/hub/api/sisi-sources-catalog.json">
        <input id="ss-hub-search" placeholder="&#x1F50D; &#x43F;&#x43E;&#x438;&#x441;&#x43A;" oninput="sisiMod.filterHub()"
          style="background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 10px;border-radius:7px;font-size:12px;width:180px">
      </div>
      <div id="ss-hub-status" style="font-size:12px;color:var(--text-muted);margin-bottom:8px"></div>
      <div id="ss-hub-list"></div>
    </div>
  </div>
  <!-- MUSIC JS SOURCES -->
  <div class="panel" id="panel-music-sources">
    <div class="mods-head">
      <div class="section-title">&#x1F3B5; Music JS &#x2014; &#x43C;&#x443;&#x437;&#x44B;&#x43A;&#x430;&#x43B;&#x44C;&#x43D;&#x44B;&#x435; &#x431;&#x44D;&#x43A;&#x435;&#x43D;&#x434;&#x44B; (goja)</div>
      <div class="mods-head-btns">
        <button class="btn btn-sm" onclick="musicMod.refresh()">&#x21BB; &#x41E;&#x431;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C;</button>
        <button class="btn btn-sm" onclick="musicMod.showHub()">&#x1F310; &#x41A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433; Hub</button>
        <button class="btn btn-sm btn-primary" onclick="musicMod.showInstall()">&#x2795; &#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C;</button>
      </div>
    </div>
    <div class="section-desc">JS-&#x43C;&#x43E;&#x434;&#x443;&#x43B;&#x438; &#x438;&#x441;&#x442;&#x43E;&#x447;&#x43D;&#x438;&#x43A;&#x43E;&#x432; &#x43C;&#x443;&#x437;&#x44B;&#x43A;&#x438; (goja). &#x422;&#x430; &#x436;&#x435; <code>index.js</code> + <code>manifest.json</code>, &#x447;&#x442;&#x43E; &#x438; &#x43E;&#x431;&#x44B;&#x447;&#x43D;&#x44B;&#x435; &#x43C;&#x43E;&#x434;&#x443;&#x43B;&#x438;, &#x43D;&#x43E; &#x43B;&#x435;&#x436;&#x430;&#x442; &#x432; <code>music_sources/&lt;id&gt;/</code> &#x438; &#x43F;&#x440;&#x43E;&#x43A;&#x441;&#x438;&#x440;&#x443;&#x44E;&#x442;&#x441;&#x44F; &#x43A;&#x430;&#x43A; <code>/music/{id}</code>.
      JS-&#x43A;&#x43E;&#x43D;&#x442;&#x440;&#x430;&#x43A;&#x442;: <code>function handle(inv)</code>, <code>inv.query.action</code> = "list" | "video",
      &#x432;&#x43E;&#x437;&#x432;&#x440;&#x430;&#x449;&#x430;&#x435;&#x442; <code>{list, total_pages, menu}</code> &#x438;&#x43B;&#x438; <code>{tracks: [...], qualitys_headers}</code>.</div>
    <div class="mods-toolbar">
      <input id="mus-search" placeholder="&#x41F;&#x43E;&#x438;&#x441;&#x43A;..." oninput="musicMod.filter()">
    </div>
    <div class="mods-grid" id="mus-grid"></div>
    <div class="mod-empty" id="mus-empty" style="display:none">
      <div class="big">&#x1F351;</div>
      <div>&#x41D;&#x435;&#x442; &#x443;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x43B;&#x435;&#x43D;&#x43D;&#x44B;&#x445; Music JS &#x438;&#x441;&#x442;&#x43E;&#x447;&#x43D;&#x438;&#x43A;&#x43E;&#x432;.</div>
      <div style="margin-top:16px"><button class="btn btn-primary" onclick="musicMod.showInstall()">&#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C; &#x43F;&#x435;&#x440;&#x432;&#x44B;&#x439;</button></div>
    </div>
  </div>
  <!-- MUSIC install modal -->
  <div class="mod-backdrop" id="mus-install-bd" onclick="musicMod.hideInstall()"></div>
  <div class="mod-drawer" id="mus-install" style="width:520px">
    <div class="mod-drawer-head">
      <div class="mod-icon">&#x2795;</div>
      <div class="mod-title">
        <div class="mod-name">&#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C; Music JS &#x438;&#x441;&#x442;&#x43E;&#x447;&#x43D;&#x438;&#x43A;</div>
        <div class="mod-meta">ZIP-&#x430;&#x440;&#x445;&#x438;&#x432; &#x438;&#x43B;&#x438; URL &#x430;&#x440;&#x445;&#x438;&#x432;&#x430;</div>
      </div>
      <button class="btn btn-sm" onclick="musicMod.hideInstall()">&#x417;&#x430;&#x43A;&#x440;&#x44B;&#x442;&#x44C;</button>
    </div>
    <div class="mod-drawer-body">
      <div style="margin-bottom:12px">
        <label style="font-size:12px;color:var(--text-muted)">URL &#x43D;&#x430; zip</label>
        <input id="mus-install-url" placeholder="https://example.com/source.zip" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px;font-size:12px;margin-top:4px">
        <button class="btn btn-sm btn-primary" style="margin-top:8px" onclick="musicMod.installFromURL()">&#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C; &#x441; URL</button>
      </div>
      <div style="margin:14px 0;border-top:1px solid var(--border)"></div>
      <div>
        <label style="font-size:12px;color:var(--text-muted)">&#x417;&#x430;&#x433;&#x440;&#x443;&#x437;&#x438;&#x442;&#x44C; &#x430;&#x440;&#x445;&#x438;&#x432;</label><br>
        <input id="mus-install-file" type="file" accept=".zip" style="margin-top:8px">
        <button class="btn btn-sm btn-primary" style="margin-top:8px" onclick="musicMod.installFromFile()">&#x417;&#x430;&#x433;&#x440;&#x443;&#x437;&#x438;&#x442;&#x44C;</button>
      </div>
    </div>
  </div>
  <!-- MUSIC hub catalog modal -->
  <div class="mod-backdrop" id="mus-hub-bd" onclick="musicMod.hideHub()"></div>
  <div class="mod-drawer" id="mus-hub" style="width:760px">
    <div class="mod-drawer-head">
      <div class="mod-icon">&#x1F310;</div>
      <div class="mod-title">
        <div class="mod-name">&#x41A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433; Music JS</div>
        <div class="mod-meta">hub.alcopa.cc &#x2014; goja-&#x431;&#x44D;&#x43A;&#x435;&#x43D;&#x434;&#x44B; &#x43E;&#x442; &#x441;&#x43E;&#x43E;&#x431;&#x449;&#x435;&#x441;&#x442;&#x432;&#x430;</div>
      </div>
      <button class="btn btn-sm" onclick="musicMod.refreshHub()">&#x21BB;</button>
      <button class="btn btn-sm" onclick="musicMod.hideHub()">&#x417;&#x430;&#x43A;&#x440;&#x44B;&#x442;&#x44C;</button>
    </div>
    <div class="mod-drawer-body">
      <div style="margin-bottom:12px;display:flex;gap:8px;align-items:center;flex-wrap:wrap">
        <label style="font-size:12px;color:var(--text-muted)">URL &#x43A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433;&#x430;:</label>
        <input id="mus-hub-url" style="flex:1;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 10px;border-radius:7px;font-size:12px"
          value="https://hub.alcopa.cc/hub/api/music-sources-catalog.json">
        <input id="mus-hub-search" placeholder="&#x1F50D; &#x43F;&#x43E;&#x438;&#x441;&#x43A;" oninput="musicMod.filterHub()"
          style="background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 10px;border-radius:7px;font-size:12px;width:180px">
      </div>
      <div id="mus-hub-status" style="font-size:12px;color:var(--text-muted);margin-bottom:8px"></div>
      <div id="mus-hub-list"></div>
    </div>
  </div>
  <!-- ENV-PRESETS — конфиги хост-окружения (PAC, systemd, helper-скрипты) -->
  <div class="panel" id="panel-env-presets">
    <style>
      .ep-card{background:var(--surface2);border:1px solid var(--border);border-radius:10px;padding:14px;margin-bottom:10px}
      .ep-card-head{display:flex;align-items:center;gap:10px;margin-bottom:8px}
      .ep-card-head .name{font-weight:600;font-size:14px}
      .ep-card-head .ver{color:var(--text-muted);font-size:11px}
      .ep-card-head .badge{margin-left:auto;background:rgba(102,187,106,0.15);color:#aed581;border:1px solid #aed58144;font-size:10px;padding:2px 8px;border-radius:8px}
      .ep-card-head .badge.warn{background:rgba(255,193,7,0.15);color:#ffc107;border-color:#ffc10744}
      .ep-meta{font-size:12px;color:var(--text-muted);line-height:1.5;margin-bottom:8px}
      .ep-meta b{color:var(--text)}
      .ep-files,.ep-cmds{font-family:Menlo,monospace;font-size:11px;background:var(--surface3);padding:8px 10px;border-radius:6px;margin:6px 0;color:#9ccc65;white-space:pre-wrap;word-break:break-all}
      .ep-cmds{color:#fff59d}
      .ep-actions{display:flex;gap:6px;flex-wrap:wrap;margin-top:8px}
      .ep-form-row{display:flex;gap:8px;align-items:center;margin-bottom:6px}
      .ep-form-row label{flex:0 0 200px;font-size:12px;color:var(--text-muted)}
      .ep-form-row input,.ep-form-row select{flex:1;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:6px 10px;border-radius:6px;font-size:12px}
    </style>
    <div class="mods-head">
      <div class="section-title">&#x1F6E0; &#x41F;&#x440;&#x435;&#x441;&#x435;&#x442;&#x44B; &#x43E;&#x43A;&#x440;&#x443;&#x436;&#x435;&#x43D;&#x438;&#x44F;</div>
      <div class="mods-head-btns">
        <button class="btn btn-sm" onclick="envMod.refresh()">&#x21BB; &#x41E;&#x431;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C;</button>
        <button class="btn btn-sm" onclick="envMod.showHub()">&#x1F310; &#x41A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433; Hub</button>
        <button class="btn btn-sm btn-primary" onclick="envMod.showInstall()">&#x2795; &#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C; &#x441; URL</button>
      </div>
    </div>
    <div class="section-desc">PAC-&#x444;&#x430;&#x439;&#x43B;&#x44B;, systemd-&#x441;&#x435;&#x440;&#x432;&#x438;&#x441;&#x44B;, helper-&#x441;&#x43A;&#x440;&#x438;&#x43F;&#x442;&#x44B; &#x441; template-&#x43F;&#x430;&#x440;&#x430;&#x43C;&#x435;&#x442;&#x440;&#x430;&#x43C;&#x438;.
      &#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x43A;&#x430; &#x440;&#x430;&#x441;&#x43F;&#x430;&#x43A;&#x43E;&#x432;&#x44B;&#x432;&#x430;&#x435;&#x442; ZIP &#x432; <code>env_presets/{id}/</code>, &#x440;&#x435;&#x43D;&#x434;&#x435;&#x440;&#x438;&#x442; <code>{{key}}</code>-&#x437;&#x430;&#x43F;&#x43E;&#x43B;&#x43D;&#x438;&#x442;&#x435;&#x43B;&#x438; &#x432;&#x430;&#x448;&#x438;&#x43C;&#x438; &#x437;&#x43D;&#x430;&#x447;&#x435;&#x43D;&#x438;&#x44F;&#x43C;&#x438; &#x438; &#x43A;&#x43B;&#x430;&#x434;&#x451;&#x442; &#x444;&#x430;&#x439;&#x43B;&#x44B; &#x432; whitelisted &#x43F;&#x443;&#x442;&#x438; (lampac_root, /etc/systemd/, /usr/local/bin/, /etc/lampac-go/).</div>
    <div id="ep-list"></div>
    <div class="mod-empty" id="ep-empty" style="display:none">
      <div class="big">&#x1F6E0;</div>
      <div>&#x41D;&#x435;&#x442; &#x443;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x43B;&#x435;&#x43D;&#x43D;&#x44B;&#x445; &#x43F;&#x440;&#x435;&#x441;&#x435;&#x442;&#x43E;&#x432;.</div>
      <div style="margin-top:16px"><button class="btn btn-primary" onclick="envMod.showHub()">&#x41E;&#x442;&#x43A;&#x440;&#x44B;&#x442;&#x44C; &#x43A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433;</button></div>
    </div>
  </div>
  <!-- ENV install-by-URL modal -->
  <div class="mod-backdrop" id="ep-install-bd" onclick="envMod.hideInstall()"></div>
  <div class="mod-drawer" id="ep-install" style="width:680px">
    <div class="mod-drawer-head">
      <div class="mod-icon">&#x2795;</div>
      <div class="mod-title">
        <div class="mod-name">&#x423;&#x441;&#x442;&#x430;&#x43D;&#x43E;&#x432;&#x438;&#x442;&#x44C; &#x43F;&#x440;&#x435;&#x441;&#x435;&#x442;</div>
        <div class="mod-meta">URL &#x430;&#x440;&#x445;&#x438;&#x432;&#x430; (ZIP) &#x441; manifest.json + files[]</div>
      </div>
      <button class="btn btn-sm" onclick="envMod.hideInstall()">&#x417;&#x430;&#x43A;&#x440;&#x44B;&#x442;&#x44C;</button>
    </div>
    <div class="mod-drawer-body" id="ep-install-body">
      <div style="margin-bottom:12px">
        <label style="font-size:12px;color:var(--text-muted)">URL &#x430;&#x440;&#x445;&#x438;&#x432;&#x430;</label>
        <input id="ep-install-url" placeholder="https://hub.alcopa.cc/hub/dl/{slug}/{ver}/module.zip" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px;font-size:12px;margin-top:4px">
        <button class="btn btn-sm btn-primary" style="margin-top:8px" onclick="envMod.previewInstall()">&#x41F;&#x440;&#x435;&#x434;&#x432;&#x430;&#x440;&#x438;&#x442;&#x435;&#x43B;&#x44C;&#x43D;&#x44B;&#x439; &#x43F;&#x440;&#x43E;&#x441;&#x43C;&#x43E;&#x442;&#x440;</button>
      </div>
      <div id="ep-install-preview" style="display:none"></div>
    </div>
  </div>
  <!-- ENV hub catalog modal -->
  <div class="mod-backdrop" id="ep-hub-bd" onclick="envMod.hideHub()"></div>
  <div class="mod-drawer" id="ep-hub" style="width:760px">
    <div class="mod-drawer-head">
      <div class="mod-icon">&#x1F310;</div>
      <div class="mod-title">
        <div class="mod-name">&#x41A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433; &#x43F;&#x440;&#x435;&#x441;&#x435;&#x442;&#x43E;&#x432;</div>
        <div class="mod-meta">hub.alcopa.cc &#x2014; PAC, systemd, CDN-bypass-&#x441;&#x435;&#x440;&#x432;&#x438;&#x441;&#x44B;</div>
      </div>
      <button class="btn btn-sm" onclick="envMod.refreshHub()">&#x21BB;</button>
      <button class="btn btn-sm" onclick="envMod.hideHub()">&#x417;&#x430;&#x43A;&#x440;&#x44B;&#x442;&#x44C;</button>
    </div>
    <div class="mod-drawer-body">
      <div style="margin-bottom:12px;display:flex;gap:8px;align-items:center;flex-wrap:wrap">
        <label style="font-size:12px;color:var(--text-muted)">URL &#x43A;&#x430;&#x442;&#x430;&#x43B;&#x43E;&#x433;&#x430;:</label>
        <input id="ep-hub-url" style="flex:1;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 10px;border-radius:7px;font-size:12px"
          value="https://hub.alcopa.cc/hub/api/env-presets-catalog.json">
      </div>
      <div id="ep-hub-status" style="font-size:12px;color:var(--text-muted);margin-bottom:8px"></div>
      <div id="ep-hub-list"></div>
    </div>
  </div>
  <!-- PLUGINS -->
  <div class="panel" id="panel-plugins">
    <div class="section-title">🧩 Стандартные плагины</div>
    <div class="section-desc">Управление встроенными плагинами Lampa. Включённые плагины загружаются автоматически. Нажмите ⚙ для настройки.</div>
    <div class="pg-grid" id="plugins-grid"></div>
    <div style="margin-top:16px"><button class="btn btn-primary" onclick="savePlugins()">Сохранить</button></div>
    <div style="margin-top:32px;border-top:1px solid rgba(255,255,255,0.06);padding-top:24px">
      <div class="section-title">🎨 Пользовательские плагины</div>
      <div class="section-desc">JS плагины доступны по URL без перезапуска. Включите «Автозагрузка» чтобы плагин загружался в Lampa автоматически.</div>
      <div class="pg-upload-bar">
        <div><label>Имя плагина</label><input class="input-sm" id="cp-name" placeholder="Мой плагин" style="width:180px"></div>
        <div><label>JS файл</label><input type="file" id="cp-file" accept=".js" style="font-size:13px"></div>
        <button class="btn btn-primary" onclick="uploadCustomPlugin()">Загрузить</button>
      </div>
      <div class="pg-grid" id="cp-grid"></div>
    </div>
    <div style="margin-top:32px;border-top:1px solid rgba(255,255,255,0.06);padding-top:24px">
      <style>
      .comm-header{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:12px}
      .comm-header .section-title{margin-bottom:0}
      .comm-badge{background:var(--accent);color:#fff;padding:2px 10px;border-radius:12px;font-size:11px;font-weight:700;display:none}
      .comm-badge.show{display:inline-block}
      .comm-header-btns{margin-left:auto;display:flex;gap:6px}
      .comm-search-bar{display:flex;gap:8px;align-items:center;margin-bottom:12px}
      .comm-search-bar input,.comm-search-bar select{background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 12px;border-radius:8px;font-size:12px}
      .comm-search-bar input{flex:1;min-width:150px}
      .comm-card{display:flex;align-items:flex-start;gap:12px;padding:14px 16px;background:var(--surface2);border:1px solid var(--border);border-radius:12px;margin-bottom:8px;transition:border-color .2s}
      .comm-card:hover{border-color:var(--accent-a25)}
      .comm-card-img{width:40px;height:40px;border-radius:8px;flex-shrink:0;background:var(--surface3);display:flex;align-items:center;justify-content:center;font-size:20px;overflow:hidden}
      .comm-card-img img{width:100%;height:100%;object-fit:cover}
      .comm-card-info{flex:1;min-width:0}
      .comm-card-name{font-size:14px;font-weight:600;color:var(--text)}
      .comm-card-desc{font-size:12px;color:var(--text-muted);margin-top:2px;line-height:1.4;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
      .comm-card-meta{font-size:11px;color:var(--text-dim);margin-top:4px}
      .comm-card-meta a{color:var(--accent);text-decoration:none}
      .comm-card-meta a:hover{text-decoration:underline}
      .comm-card-actions{display:flex;flex-direction:column;gap:4px;align-items:flex-end;flex-shrink:0}
      .comm-card .comm-ver-update{font-size:10px;color:var(--warning);white-space:nowrap}
      .comm-card .comm-ver-ok{font-size:10px;color:var(--text-dim);white-space:nowrap}
      .comm-settings{margin-top:16px;padding:14px;background:var(--surface2);border:1px solid var(--border);border-radius:12px}
      .comm-settings-title{font-size:11px;color:var(--text-dim);text-transform:uppercase;letter-spacing:.8px;font-weight:600;margin-bottom:8px}
      .comm-settings-row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
      .comm-settings-row input{flex:1;min-width:200px;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 12px;border-radius:8px;font-size:12px}
      .comm-settings-row select{background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:7px 12px;border-radius:8px;font-size:12px}
      .comm-empty{text-align:center;padding:32px 16px;color:var(--text-dim);font-size:13px}
      .comm-empty-icon{font-size:36px;margin-bottom:8px;opacity:.4}
      </style>
      <div class="comm-header">
        <div class="section-title">&#x1F310; Community плагины</div>
        <span class="comm-badge" id="comm-badge"></span>
        <div class="comm-header-btns">
          <button class="btn btn-sm btn-primary" onclick="checkCommunityUpdates()">&#x1F50D; Проверить</button>
          <button class="btn btn-sm btn-warning" onclick="updateAllCommunity()">&#x2B06; Обновить всё</button>
        </div>
      </div>
      <div class="section-desc">Плагины из каталога сообщества. Установите и обновляйте в один клик.</div>
      <div class="comm-search-bar">
        <input id="comm-search" placeholder="Поиск плагина..." oninput="filterCommunityPlugins()">
        <select id="comm-category" onchange="filterCommunityPlugins()"><option value="">Все категории</option></select>
      </div>
      <div id="comm-grid"></div>
      <div class="comm-settings">
        <div class="comm-settings-title">&#x2699; Настройки каталога</div>
        <div class="comm-settings-row">
          <input id="comm-url" placeholder="URL каталога (JSON)">
          <select id="comm-interval">
            <option value="0">Авто-обновление: выкл</option>
            <option value="6">Каждые 6 часов</option>
            <option value="12">Каждые 12 часов</option>
            <option value="24">Каждые 24 часа</option>
          </select>
          <button class="btn btn-primary btn-sm" onclick="saveCommunitySettings()">Сохранить</button>
        </div>
        <div id="comm-last-check" style="font-size:11px;color:var(--text-dim);margin-top:6px"></div>
      </div>
    </div>
  </div>
  <!-- Plugin settings slide-out panel -->
  <div class="pg-settings-overlay" id="pg-settings-overlay" onclick="closePluginSettings()"></div>
  <div class="pg-settings-panel" id="pg-settings-panel">
    <button class="up-close" onclick="closePluginSettings()">&times;</button>
    <div id="pg-settings-content"></div>
  </div>
  <!-- PROXY -->
  <div class="panel" id="panel-proxy">
    <div class="section-title">🌐 Проксирование</div>
    <div class="section-desc">Маршрутизация запросов балансеров через прокси-сайдкары с разными exit-IP для обхода гео-блокировок. Поддержка VLESS, VMess, Trojan, Shadowsocks, Hysteria2, TUIC, WireGuard.</div>
    <div class="proxy-servers" id="proxy-servers"></div>
    <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
      <div class="proxy-add-btn" onclick="addProxyServer()">+ Добавить прокси сервер</div>
      <div class="proxy-add-btn" onclick="addWARPEntry()" style="background:rgba(174,213,129,0.1);border-color:#aed581;color:#aed581">➕ Cloudflare WARP</div>
    </div>
    <div class="proxy-available" id="proxy-available-section">
      <div class="proxy-available-title">Свободные балансеры <span style="font-size:11px;color:var(--text-dim)">(перетащите на сервер)</span></div>
      <div class="proxy-available-chips" id="proxy-available-chips"></div>
    </div>
    <div class="proxy-global-actions">
      <button class="btn btn-primary" onclick="saveProxy()">Сохранить</button>
      <button class="btn btn-warning" onclick="reloadProxy()">Применить (hot-reload)</button>
    </div>

    <!-- ============================================== -->
    <!--   FlareSolverr — anti-bot solver (Cloudflare)  -->
    <!-- ============================================== -->
    <div class="section-title" style="margin-top:32px">🛡️ FlareSolverr</div>
    <div class="section-desc">Headless Chrome (внешний docker-сервис) автоматически решает JS-челленджи: Cloudflare, DDoS-Guard, Zetflix anti-bot. Балансёры в списке ниже будут пробовать сначала FlareSolverr, fallback — прямой HTTP.</div>

    <div id="fs-card" style="background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:16px;margin-top:8px">
      <div style="display:flex;gap:8px;align-items:end;flex-wrap:wrap;margin-bottom:14px">
        <div style="flex:1;min-width:260px">
          <label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px;text-transform:uppercase;letter-spacing:.5px">URL FlareSolverr</label>
          <input id="fs-url" placeholder="http://127.0.0.1:8191" style="width:100%;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:8px 12px;color:var(--text);font-size:13px;outline:none;box-sizing:border-box;font-family:'Courier New',monospace">
        </div>
        <button class="btn btn-primary" onclick="saveFlareSolverr()" style="border-radius:8px">Сохранить</button>
        <button class="btn" onclick="testFlareSolverr()" style="border-radius:8px">Проверить</button>
      </div>

      <label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:6px;text-transform:uppercase;letter-spacing:.5px">Балансёры через FlareSolverr</label>
      <div id="fs-bals-tags" style="display:flex;flex-wrap:wrap;gap:6px;min-height:32px;margin-bottom:8px;padding:6px;background:var(--bg);border:1px dashed var(--border);border-radius:8px"></div>
      <div style="position:relative;margin-bottom:14px">
        <input id="fs-bals-input" placeholder="+ Добавить балансёр..." autocomplete="off" oninput="fsBalsSearch()" onfocus="fsBalsSearch()" onblur="setTimeout(fsBalsHideDropdown,200)" style="width:100%;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:8px 12px;color:var(--text);font-size:12px;outline:none;box-sizing:border-box">
        <div id="fs-bals-dropdown" style="display:none;position:absolute;left:0;right:0;top:100%;z-index:50;max-height:220px;overflow-y:auto;background:var(--surface2);border:1px solid var(--border);border-radius:8px;margin-top:4px;box-shadow:0 4px 12px rgba(0,0,0,.3)"></div>
      </div>

      <div style="display:flex;align-items:center;gap:8px;margin-top:8px;margin-bottom:6px">
        <span style="font-size:11px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.5px">Активные сессии</span>
        <span id="fs-sessions-count" style="font-size:10px;background:var(--surface2);padding:1px 7px;border-radius:9px;color:var(--text-muted)">0</span>
        <span style="flex:1"></span>
        <button class="btn btn-sm" onclick="loadFlareSolverr()" style="font-size:11px;padding:4px 10px">↻ Обновить</button>
      </div>
      <div id="fs-sessions" style="display:flex;flex-direction:column;gap:4px"></div>
    </div>
  </div>
  <!-- PROXYCORE -->
  <style>
  .pc-spinner{width:14px;height:14px;border:2px solid var(--border);border-top-color:var(--accent);border-radius:50%;animation:pc-spin .6s linear infinite;display:inline-block}
  @keyframes pc-spin{to{transform:rotate(360deg)}}
  #pc-modal input:focus,#pc-modal textarea:focus{border-color:var(--accent);box-shadow:0 0 0 2px var(--accent-a12)}
  </style>
  <div class="panel" id="panel-proxycore">
    <div id="pc-root"></div>
    <!-- Add Proxy Modal -->
    <div id="pc-modal" style="display:none;position:fixed;inset:0;background:rgba(0,0,0,.7);z-index:9999;backdrop-filter:blur(4px);justify-content:center;align-items:center">
      <div style="background:var(--sidebar-bg);border:1px solid var(--border);border-radius:16px;padding:24px;width:480px;max-width:92vw;max-height:85vh;overflow-y:auto;box-shadow:0 20px 60px rgba(0,0,0,.5)">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:20px">
          <div style="font-size:16px;font-weight:700">&#x1F6E1; Добавить прокси</div>
          <button onclick="pcCloseModal()" style="background:none;border:none;color:var(--text-muted);font-size:20px;cursor:pointer;padding:4px">&times;</button>
        </div>
        <!-- Step 1: URI input -->
        <div id="pc-modal-step1">
          <label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px;text-transform:uppercase;letter-spacing:.5px">Proxy URI</label>
          <textarea id="pc-m-uri" rows="3" placeholder="vless://uuid@server:443?security=reality&..." style="width:100%;background:var(--bg);border:1px solid var(--border);border-radius:10px;padding:10px 12px;color:var(--text);font-size:12px;font-family:monospace;resize:vertical;outline:none;box-sizing:border-box"></textarea>
          <div style="display:flex;gap:6px;margin:12px 0;flex-wrap:wrap">
            <span class="pc-proto-chip" onclick="pcSetProto('vless')" style="cursor:pointer;padding:4px 10px;border-radius:8px;font-size:10px;font-weight:700;background:#00d4aa22;color:#00d4aa;border:1px solid transparent;transition:all .2s">VLESS</span>
            <span class="pc-proto-chip" onclick="pcSetProto('trojan')" style="cursor:pointer;padding:4px 10px;border-radius:8px;font-size:10px;font-weight:700;background:#ff6b6b22;color:#ff6b6b;border:1px solid transparent;transition:all .2s">TROJAN</span>
            <span class="pc-proto-chip" onclick="pcSetProto('ss')" style="cursor:pointer;padding:4px 10px;border-radius:8px;font-size:10px;font-weight:700;background:#ffd93d22;color:#ffd93d;border:1px solid transparent;transition:all .2s">SS</span>
            <span class="pc-proto-chip" onclick="pcSetProto('hy2')" style="cursor:pointer;padding:4px 10px;border-radius:8px;font-size:10px;font-weight:700;background:#6c5ce722;color:#6c5ce7;border:1px solid transparent;transition:all .2s">HY2</span>
            <span class="pc-proto-chip" onclick="pcSetProto('socks5')" style="cursor:pointer;padding:4px 10px;border-radius:8px;font-size:10px;font-weight:700;background:#74b9ff22;color:#74b9ff;border:1px solid transparent;transition:all .2s">SOCKS5</span>
            <span class="pc-proto-chip" onclick="pcSetProto('vmess')" style="cursor:pointer;padding:4px 10px;border-radius:8px;font-size:10px;font-weight:700;background:#a29bfe22;color:#a29bfe;border:1px solid transparent;transition:all .2s">VMESS</span>
          </div>
          <div style="display:flex;gap:8px;margin-top:8px">
            <button class="btn btn-primary" onclick="pcModalNext()" style="border-radius:8px;flex:1">&#x1F50D; Проверить и продолжить</button>
          </div>
          <div id="pc-m-test-result" style="margin-top:10px;font-size:12px"></div>
        </div>
        <!-- Step 2: Settings (shown after test) -->
        <div id="pc-modal-step2" style="display:none">
          <div id="pc-m-geo" style="background:var(--bg);border-radius:10px;padding:12px;margin-bottom:16px;display:flex;align-items:center;gap:12px">
            <span id="pc-m-flag" style="font-size:32px"></span>
            <div>
              <div id="pc-m-country" style="font-size:14px;font-weight:600"></div>
              <div id="pc-m-server-info" style="font-size:11px;color:var(--text-muted)"></div>
            </div>
            <span id="pc-m-proto-badge" style="margin-left:auto;padding:4px 10px;border-radius:8px;font-size:10px;font-weight:700"></span>
          </div>
          <label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px;text-transform:uppercase;letter-spacing:.5px">Название</label>
          <input id="pc-m-label" placeholder="Germany VLESS" style="width:100%;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:8px 12px;color:var(--text);font-size:13px;outline:none;margin-bottom:12px;box-sizing:border-box">
          <label style="display:block;font-size:11px;color:var(--text-muted);margin-bottom:4px;text-transform:uppercase;letter-spacing:.5px">Балансеры</label>
          <div id="pc-m-bals-wrap" style="position:relative;margin-bottom:16px">
            <div id="pc-m-bals-tags" style="display:flex;flex-wrap:wrap;gap:4px;min-height:28px;margin-bottom:6px"></div>
            <input id="pc-m-bals-search" placeholder="Поиск балансера..." autocomplete="off" style="width:100%;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:8px 12px;color:var(--text);font-size:12px;outline:none;box-sizing:border-box">
            <div id="pc-m-bals-dropdown" style="display:none;position:absolute;left:0;right:0;top:100%;z-index:100;max-height:200px;overflow-y:auto;background:var(--card-bg);border:1px solid var(--border);border-radius:8px;margin-top:4px;box-shadow:0 4px 12px rgba(0,0,0,.3)"></div>
          </div>
          <div style="display:flex;gap:8px">
            <button class="btn" onclick="pcModalBack()" style="border-radius:8px">&#x2190; Назад</button>
            <button class="btn btn-primary" onclick="pcModalSave()" style="border-radius:8px;flex:1">&#x2714; Добавить прокси</button>
          </div>
        </div>
      </div>
    </div>
  </div>
  <!-- TG SETTINGS (super only) -->
  <div class="panel" id="panel-tgsettings">
    <div class="section-title">🤖 Telegram авторизация</div>
    <div class="section-desc">Настройки бота и админ-панели</div>
    <div id="tg-form"></div>
    <div style="margin-top:24px">
      <div class="section-title">🔑 Kit — персональные настройки</div>
      <div class="section-desc">Позволяет пользователям привязать свои аккаунты сервисов (Filmix, KinoPub, Rezka и т.д.) через Telegram WebApp. Требуется HTTPS для WebApp.</div>
      <div id="kit-form"></div>
    </div>
    <div style="margin-top:24px">
      <div class="section-title">🎨 Окно авторизации</div>
      <div class="section-desc">Настройте внешний вид страниц /tg/auth и /tg/device/verify</div>
      <div id="auth-page-form" style="display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:12px"></div>
      <div style="margin-top:12px">
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">Произвольный CSS</label>
        <textarea id="auth-page-css" rows="3" class="input-sm" style="width:100%;font-family:monospace;font-size:12px;resize:vertical" placeholder=".card { border: 2px solid gold; }"></textarea>
      </div>
      <div id="auth-page-preview" style="margin-top:16px;border-radius:12px;overflow:hidden;border:1px solid var(--surface2)"></div>
      <div style="margin-top:12px;display:flex;gap:8px">
        <button class="btn btn-primary btn-sm" onclick="saveAuthPageStyle()">Сохранить</button>
        <button class="btn btn-sm" onclick="resetAuthPageStyle()">Сбросить</button>
      </div>
    </div>
    <div style="margin-top:24px">
      <div class="section-title">📢 Обязательная подписка</div>
      <div class="section-desc">Пользователь должен быть подписан на указанные каналы/группы. Бот должен быть администратором в канале/группе.</div>
      <div id="required-chats-form"></div>
    </div>
    <div style="margin-top:24px">
      <div class="section-title">👤 Делегированные администраторы</div>
      <div class="section-desc">Другие пользователи с доступом к админ-панели (кроме TG настроек)</div>
      <table><thead><tr><th>Telegram ID</th><th>Роль</th><th>Заметка</th><th>Действия</th></tr></thead>
      <tbody id="admins-body"></tbody></table>
      <div class="admin-form">
        <input class="input-sm" id="new-admin-id" placeholder="Telegram ID" type="number">
        <input class="input-sm" id="new-admin-note" placeholder="Заметка">
        <button class="btn btn-primary btn-sm" onclick="addAdmin()">Добавить</button>
      </div>
    </div>
    <div style="margin-top:24px">
      <div class="section-title">Passkeys (WebAuthn)</div>
      <div class="section-desc">Вход без пароля по отпечатку, Face ID или аппаратному ключу</div>
      <div id="passkeys-list"></div>
      <div style="margin-top:12px;display:flex;gap:8px;align-items:center">
        <input class="input-sm" id="passkey-name" placeholder="Название (напр. MacBook Touch ID)" style="flex:1">
        <button class="btn btn-primary btn-sm" onclick="registerPasskey()">Добавить Passkey</button>
      </div>
    </div>
  </div>
  <!-- SERVER STATS (redesigned) -->
  <div class="panel" id="panel-server">
    <div class="srv-header">
      <div class="srv-header-info">
        <div class="srv-hostname" id="srv-hostname">---</div>
        <div class="srv-meta" id="srv-meta">---</div>
      </div>
      <div class="srv-header-actions">
        <label style="font-size:12px;color:var(--text-muted);margin-right:8px;cursor:pointer"><input type="checkbox" id="auto-refresh" checked> Авто (5с)</label>
        <button class="btn-danger" id="srv-restart-btn" onclick="confirmRestart()">&#x21bb; Перезапустить</button>
      </div>
    </div>
    <div class="srv-uptime-hero"><div class="uval" id="srv-uptime">---</div><div class="ulbl">Uptime</div></div>
    <div class="srv-rings" id="srv-rings"></div>
    <div style="margin-top:20px">
      <div class="section-title" style="font-size:14px">&#x1F4E1; Запросы</div>
      <div class="stats-grid" id="stats-requests"></div>
      <div class="srv-sparkline-wrap"><div class="srv-sparkline" id="srv-req-sparkline"></div><div class="srv-sparkline-labels"><span>-10 мин</span><span>сейчас</span></div></div>
    </div>
    <div style="margin-top:20px">
      <div class="section-title" style="font-size:14px">&#x23F1; Latency (текущая минута)</div>
      <div class="lat-bars" id="stats-latency-bars"></div>
      <div class="section-title" style="font-size:13px;margin-top:12px">&#x1F422; Топ медленные маршруты</div>
      <div class="route-lat-wrap" id="stats-route-latency"></div>
      <div class="section-title" style="font-size:13px;margin-top:12px">&#x1F310; Топ proxy(pl)</div>
      <div class="route-lat-wrap" id="stats-proxy-pl-latency"></div>
    </div>
    <div style="margin-top:20px">
      <div class="section-title srv-collapsible" onclick="this.classList.toggle('open');document.getElementById('stats-mem-detail').classList.toggle('open')"><span class="chevron">&#9654;</span> &#x1F9E0; Детали памяти Go Runtime</div>
      <div class="stats-grid srv-collapse-body" id="stats-mem-detail"></div>
    </div>
    <div style="margin-top:24px">
      <div class="section-title" style="font-size:14px">&#x2699;&#xFE0F; Субпроцессы</div>
      <div id="stats-processes"></div>
    </div>
    <div style="margin-top:20px">
      <div class="section-title" style="font-size:14px">&#x1F310; Chrome (chromedp)</div>
      <div id="stats-chrome"></div>
    </div>
  </div>
  <!-- Restart confirmation modal -->
  <div class="srv-modal-overlay" id="srv-restart-modal">
    <div class="srv-modal">
      <h3>&#x26a0; Перезапуск сервера</h3>
      <p>Сервер будет остановлен (SIGTERM).<br>Автоматический перезапуск возможен только при управлении через systemd, Docker или supervisor.</p>
      <div class="srv-modal-actions">
        <button class="btn btn-secondary" onclick="cancelRestart()">Отмена</button>
        <button class="btn-danger" onclick="doRestart()">Перезапустить</button>
      </div>
    </div>
  </div>
  <!-- CLUSTER -->
  <div class="panel" id="panel-cluster">
    <div class="section-title">&#x1F310; Кластер серверов</div>
    <div class="section-desc">Каскад lampac-go нод за этим primary. Запросы /lite/* распределяются между активными нодами по выбранной стратегии. Latency и активные соединения учитываются в реальном времени.</div>

    <div id="cluster-banner" class="ctr-info" style="display:none;margin-top:12px"></div>

    <div style="margin-top:16px;display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px">
      <div class="su-card"><div class="su-lbl">Режим</div><div id="cl-mode" class="su-val">—</div><div id="cl-apikey" class="su-sub"></div></div>
      <div class="su-card"><div class="su-lbl">Стратегия</div><div id="cl-strategy" class="su-val">—</div><div class="su-sub">алгоритм выбора ноды</div></div>
      <div class="su-card"><div class="su-lbl">Ноды (всего/живые)</div><div id="cl-counts" class="su-val">—</div><div class="su-sub">healthy / total</div></div>
      <div class="su-card"><div class="su-lbl">Локальные коннекции</div><div id="cl-local" class="su-val">—</div><div class="su-sub">обслужено всего: <span id="cl-local-total">0</span></div></div>
    </div>

    <div style="margin-top:18px;display:flex;gap:10px;flex-wrap:wrap;align-items:center">
      <button class="btn" onclick="clusterMod.openAdd()">&#x2795; Добавить ноду</button>
      <button class="btn btn-secondary" onclick="clusterMod.openSettings()">&#x2699;&#xFE0F; Настройки</button>
      <button class="btn btn-secondary" onclick="clusterMod.showSecretsNow()">&#x1F510; Секреты для нод</button>
      <button class="btn btn-secondary" onclick="clusterMod.regenSecrets()" title="Перевыпустить api_key + shared_secret. Все ноды нужно будет переконфигурировать!" style="color:#f59e0b">&#x267B;&#xFE0F; Перевыпустить</button>
      <button class="btn btn-secondary" onclick="clusterMod.refresh()">&#x21BB; Обновить</button>
      <label style="display:flex;align-items:center;gap:6px;font-size:12px;color:var(--text-muted);margin-left:auto">
        <input type="checkbox" id="cl-auto" checked> Авто-обновление (5с)
      </label>
    </div>

    <div style="margin-top:16px;overflow-x:auto">
      <table class="dt" style="width:100%">
        <thead>
          <tr>
            <th></th>
            <th>Имя</th><th>Хост</th><th>Регион</th><th>Статус</th>
            <th title="Активные соединения">Live</th>
            <th title="Обслужено всего">Served</th>
            <th title="Ошибок">Fail</th>
            <th title="Уникальных клиентов за последние 5 минут">Users</th>
            <th title="Передано байт через эту ноду">Traffic</th>
            <th title="Самый частый балансёр на этой ноде">Top балансёр</th>
            <th title="Средняя задержка (EWMA)">Avg ms</th>
            <th title="Последний пинг">Last ms</th>
            <th>Вес</th>
            <th>Действия</th>
          </tr>
        </thead>
        <tbody id="cl-rows"><tr><td colspan="15" style="text-align:center;color:var(--text-muted);padding:20px">Загрузка…</td></tr></tbody>
      </table>
    </div>

    <div id="cl-local-block" style="margin-top:18px"></div>
    <div id="cl-events-block" style="margin-top:18px"></div>
  </div>

  <!-- CLUSTER add/edit modal -->
  <div class="srv-modal-overlay" id="cl-edit-modal">
    <div class="srv-modal" style="max-width:520px;text-align:left">
      <h3 id="cl-edit-title">Добавить ноду</h3>
      <div style="display:grid;gap:10px;font-size:13px">
        <label>Имя<br><input id="cl-f-name" placeholder="EU-Frankfurt-1" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
        <label>Хост (URL с http:// или https://)<br><input id="cl-f-host" placeholder="https://node1.example.com" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:10px">
          <label>Вес<br><input id="cl-f-weight" type="number" min="1" value="1" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
          <label>Регион<br><input id="cl-f-region" placeholder="EU" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
        </div>
        <label>Заметки<br><textarea id="cl-f-notes" rows="2" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></textarea></label>
        <label style="display:flex;gap:8px;align-items:center"><input id="cl-f-enabled" type="checkbox" checked> Включена (принимает трафик)</label>
        <div id="cl-test-out" style="font-size:11px;color:var(--text-muted);font-family:monospace;background:var(--surface3);padding:6px 8px;border-radius:6px;display:none"></div>
      </div>
      <div class="srv-modal-actions">
        <button class="btn btn-secondary" onclick="clusterMod.testHost()">&#x1F4E1; Тест ping</button>
        <button class="btn btn-secondary" onclick="clusterMod.closeEdit()">Отмена</button>
        <button class="btn" onclick="clusterMod.saveEdit()">Сохранить</button>
      </div>
    </div>
  </div>

  <!-- CLUSTER settings modal -->
  <div class="srv-modal-overlay" id="cl-settings-modal">
    <div class="srv-modal" style="max-width:520px;text-align:left">
      <h3>&#x2699;&#xFE0F; Настройки кластера</h3>
      <div style="display:grid;gap:10px;font-size:13px">
        <label>Стратегия выбора ноды<br>
          <select id="cl-s-strategy" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px">
            <option value="hybrid">Гибрид (latency + load)</option>
            <option value="least-conns">Меньше всего соединений</option>
            <option value="latency">Минимальный пинг</option>
          </select>
        </label>
        <label>Вес latency в гибриде (0 = только load, 1 = только latency)<br>
          <input id="cl-s-lw" type="range" min="0" max="1" step="0.05" value="0.4" style="width:100%">
          <span id="cl-s-lw-val" style="font-family:monospace;color:var(--text-muted)">0.4</span>
        </label>
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:10px">
          <label>Probe interval (сек)<br><input id="cl-s-pi" type="number" min="5" value="30" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
          <label>Max retries при 5xx<br><input id="cl-s-mr" type="number" min="0" max="5" value="2" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
          <label>Fail threshold<br><input id="cl-s-ft" type="number" min="1" value="3" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
          <label>Recover threshold<br><input id="cl-s-rt" type="number" min="1" value="2" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
        </div>
        <hr style="border:none;border-top:1px solid var(--border);margin:6px 0">
        <label>Имя этого сервера (показывается клиентам в X-Lampac-Server)<br><input id="cl-s-name" placeholder="lampac-eu" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
        <label>Регион этого сервера<br><input id="cl-s-region" placeholder="EU" style="width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px"></label>
        <label style="display:flex;gap:8px;align-items:center"><input id="cl-s-expose" type="checkbox" checked> Публиковать список нод в /api/servers/list</label>
        <label style="display:flex;gap:8px;align-items:center"><input id="cl-s-hosts" type="checkbox"> Включить хосты нод в публичный список (для прямого ping из виджета)</label>
        <hr style="border:none;border-top:1px solid var(--border);margin:6px 0">
        <label style="display:flex;gap:8px;align-items:center;color:#f59e0b"><input id="cl-s-force" type="checkbox"> 🧪 Тестовый режим: принудительно форвардить все запросы на ноду (игнорировать primary при выборе)</label>
        <div style="font-size:11px;color:var(--text-muted);margin-top:-4px">Включи для проверки что трафик реально доходит до нод. Primary всё равно обработает запрос если все ноды down.</div>

        <hr style="border:none;border-top:1px solid var(--border);margin:6px 0">
        <div>
          <div style="display:flex;justify-content:space-between;align-items:center">
            <b>📋 Правила маршрутизации (по балансёру)</b>
            <button class="btn btn-sm" type="button" onclick="clusterMod.addRule()">+ Добавить правило</button>
          </div>
          <div style="font-size:11px;color:var(--text-muted);margin:4px 0 8px">Балансёр всегда идёт по указанному пути. Правила имеют приоритет над стратегией и force-node. Пустая таблица — поведение по умолчанию.</div>
          <div id="cl-rules-list" style="display:flex;flex-direction:column;gap:6px"></div>
        </div>
      </div>
      <div class="srv-modal-actions">
        <button class="btn btn-secondary" onclick="clusterMod.closeSettings()">Отмена</button>
        <button class="btn" onclick="clusterMod.saveSettings()">Сохранить</button>
      </div>
    </div>
  </div>

  <!-- CONSTRUCTOR -->
  <div class="panel" id="panel-constructor">
    <div class="section-title">🔧 Конструктор балансеров</div>
    <div class="section-desc">Загрузите C# файл балансера из оригинального lampac — система проанализирует код и сгенерирует Go-скелет</div>
    <div class="ctr-upload" id="ctr-dropzone" onclick="document.getElementById('ctr-file-input').click()">
      <div class="ctr-upload-icon">&#128196;</div>
      <div class="ctr-upload-text"><strong>Загрузите .cs / .zip файлы</strong> или перетащите сюда<br>Поддерживаются контроллеры из Online/Controllers/ (одиночные или zip-архив)</div>
      <input type="file" id="ctr-file-input" accept=".cs,.zip" multiple style="display:none">
    </div>
    <div style="text-align:center;color:var(--text-dim);font-size:12px;margin:-12px 0 12px">или вставьте код вручную</div>
    <textarea class="ctr-paste-area" id="ctr-paste" placeholder="Вставьте C# код балансера сюда..."></textarea>
    <div style="margin-top:12px"><button class="btn btn-primary" onclick="analyzeCS()">Анализировать</button></div>
    <div id="ctr-result" class="ctr-result" style="display:none">
      <div class="ctr-tabs">
        <div class="ctr-tab active" data-ctab="analysis" onclick="switchCtrTab(this)">Анализ</div>
        <div class="ctr-tab" data-ctab="gocode" onclick="switchCtrTab(this)">Go код (встроенный)</div>
        <div class="ctr-tab" data-ctab="standalone" onclick="switchCtrTab(this)">Go код (standalone)</div>
        <div class="ctr-tab" data-ctab="original" onclick="switchCtrTab(this)">Оригинал C#</div>
      </div>
      <div class="ctr-panel active" id="ctr-analysis"></div>
      <div class="ctr-panel" id="ctr-gocode"></div>
      <div class="ctr-panel" id="ctr-standalone"></div>
      <div class="ctr-panel" id="ctr-original"></div>
    </div>
  </div>
  <!-- APPREPLACE -->
  <div class="panel" id="panel-appreplace">
    <style>
    .ar-header{display:flex;align-items:center;gap:14px;margin-bottom:6px}
    .ar-header-icon{width:40px;height:40px;background:linear-gradient(135deg,var(--accent-a20),var(--accent-a10));border-radius:12px;display:flex;align-items:center;justify-content:center;font-size:20px;flex-shrink:0}
    .ar-header-text h2{font-size:17px;font-weight:700;color:var(--text);margin-bottom:2px}
    .ar-header-text p{font-size:12px;color:var(--text-muted);line-height:1.4}
    .ar-count{margin-left:auto;background:var(--accent-a12);color:var(--accent);font-size:11px;font-weight:700;padding:4px 10px;border-radius:20px;white-space:nowrap}
    .ar-presets{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:20px;padding:14px;background:var(--surface2);border:1px solid var(--border);border-radius:12px}
    .ar-presets-title{width:100%;font-size:11px;color:var(--text-dim);text-transform:uppercase;letter-spacing:1px;font-weight:600;margin-bottom:6px}
    .ar-preset-btn{display:inline-flex;align-items:center;gap:6px;padding:8px 14px;background:linear-gradient(135deg,var(--accent-a12),var(--accent-a06));border:1px solid var(--accent-a25);color:var(--accent);border-radius:8px;font-size:12px;font-weight:600;cursor:pointer;transition:all .25s ease;user-select:none}
    .ar-preset-btn:hover{background:linear-gradient(135deg,var(--accent-a25),var(--accent-a15));border-color:var(--accent-a35);transform:translateY(-1px);box-shadow:0 4px 12px var(--accent-a15)}
    .ar-preset-btn:active{transform:translateY(0);box-shadow:none}
    .ar-preset-btn .ar-pi{font-size:14px;opacity:.8}
    .ar-card{background:var(--surface2);border:1px solid var(--border);border-radius:14px;padding:0;margin-bottom:10px;overflow:hidden;transition:border-color .2s,box-shadow .2s}
    .ar-card:hover{border-color:var(--accent-a25);box-shadow:0 2px 16px rgba(0,0,0,0.15)}
    .ar-card.ar-disabled{opacity:.55}
    .ar-card-head{display:flex;align-items:center;gap:10px;padding:12px 16px;background:var(--surface3);border-bottom:1px solid var(--border)}
    .ar-card-head .ar-drag{color:var(--text-dim);font-size:14px;cursor:grab;padding:0 2px;user-select:none}
    .ar-card-head input[type=text]{flex:1;background:transparent;border:none;color:var(--text);font-size:13px;font-weight:600;outline:none;padding:0}
    .ar-card-head input[type=text]::placeholder{color:var(--text-dim);font-weight:400}
    .ar-toggle{position:relative;width:36px;height:20px;flex-shrink:0;cursor:pointer}
    .ar-toggle input{opacity:0;width:0;height:0;position:absolute}
    .ar-toggle .ar-slider{position:absolute;inset:0;background:var(--border);border-radius:20px;transition:all .25s}
    .ar-toggle .ar-slider:before{content:'';position:absolute;width:16px;height:16px;left:2px;top:2px;background:#fff;border-radius:50%;transition:all .25s}
    .ar-toggle input:checked+.ar-slider{background:var(--accent)}
    .ar-toggle input:checked+.ar-slider:before{transform:translateX(16px)}
    .ar-del-btn{background:none;border:none;color:var(--text-dim);font-size:16px;cursor:pointer;padding:4px 6px;border-radius:6px;transition:all .2s;line-height:1}
    .ar-del-btn:hover{color:var(--danger);background:var(--danger-bg)}
    .ar-card-body{padding:12px 16px}
    .ar-field{margin-bottom:10px}
    .ar-field:last-child{margin-bottom:0}
    .ar-field-label{font-size:10px;color:var(--text-dim);text-transform:uppercase;letter-spacing:.8px;font-weight:600;margin-bottom:4px}
    .ar-field textarea{width:100%;background:var(--surface3);border:1px solid var(--border);color:var(--text);padding:8px 12px;border-radius:8px;font-size:12px;font-family:'JetBrains Mono',Consolas,Monaco,monospace;resize:vertical;transition:border-color .2s;line-height:1.5}
    .ar-field textarea:focus{border-color:var(--accent);outline:none;box-shadow:0 0 0 2px var(--accent-a15)}
    .ar-actions{display:flex;gap:10px;margin-top:16px;align-items:center}
    .ar-btn-add{display:inline-flex;align-items:center;gap:6px;padding:10px 20px;background:var(--surface2);border:2px dashed var(--border);color:var(--text-muted);border-radius:12px;font-size:13px;font-weight:600;cursor:pointer;transition:all .25s}
    .ar-btn-add:hover{border-color:var(--accent-a30);color:var(--accent);background:var(--accent-a06)}
    .ar-btn-save{display:inline-flex;align-items:center;gap:6px;padding:10px 24px;background:linear-gradient(135deg,var(--accent-dark),var(--accent));color:#fff;border:none;border-radius:12px;font-size:13px;font-weight:700;cursor:pointer;transition:all .25s;box-shadow:0 2px 12px var(--accent-a25)}
    .ar-btn-save:hover{transform:translateY(-1px);box-shadow:0 4px 20px var(--accent-a35)}
    .ar-btn-save:active{transform:translateY(0)}
    .ar-btn-save:disabled{opacity:.5;cursor:not-allowed;transform:none;box-shadow:none}
    .ar-status-msg{font-size:12px;margin-left:8px;transition:opacity .3s}
    .ar-empty{text-align:center;padding:40px 20px;color:var(--text-dim);font-size:13px}
    .ar-empty-icon{font-size:36px;margin-bottom:10px;opacity:.4}
    .ar-empty-hint{margin-top:6px;font-size:11px;color:var(--text-dim);opacity:.7}
    .cc-divider{height:1px;background:linear-gradient(90deg,transparent,var(--border),transparent);margin:28px 0 24px}
    .cc-header{display:flex;align-items:center;gap:14px;margin-bottom:16px}
    .cc-header-icon{width:40px;height:40px;background:linear-gradient(135deg,rgba(168,85,247,0.2),rgba(168,85,247,0.1));border-radius:12px;display:flex;align-items:center;justify-content:center;font-size:20px;flex-shrink:0}
    .cc-header-text h2{font-size:17px;font-weight:700;color:var(--text);margin-bottom:2px}
    .cc-header-text p{font-size:12px;color:var(--text-muted);line-height:1.4}
    .cc-editors{display:grid;grid-template-columns:1fr 1fr;gap:16px}
    @media(max-width:900px){.cc-editors{grid-template-columns:1fr}}
    .cc-editor{background:var(--surface2);border:1px solid var(--border);border-radius:14px;overflow:hidden;transition:border-color .2s}
    .cc-editor:focus-within{border-color:rgba(168,85,247,0.4)}
    .cc-editor-head{display:flex;align-items:center;gap:8px;padding:10px 14px;background:var(--surface3);border-bottom:1px solid var(--border);font-size:12px;font-weight:600;color:var(--text-muted)}
    .cc-editor-head .cc-lang{padding:2px 8px;border-radius:4px;font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.5px}
    .cc-lang-css{background:rgba(59,130,246,0.15);color:#60a5fa}
    .cc-lang-js{background:rgba(250,204,21,0.15);color:#facc15}
    .cc-editor-head .cc-lines{margin-left:auto;font-size:10px;color:var(--text-dim);font-weight:400}
    .cc-editor textarea{width:100%;min-height:200px;background:var(--bg);border:none;color:var(--text);padding:14px;font-size:12px;font-family:'JetBrains Mono',Consolas,Monaco,monospace;resize:vertical;line-height:1.6;outline:none;tab-size:2}
    .cc-editor textarea::placeholder{color:var(--text-dim);font-style:italic}
    .cc-presets{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:16px}
    .cc-presets-title{width:100%;font-size:11px;color:var(--text-dim);text-transform:uppercase;letter-spacing:1px;font-weight:600;margin-bottom:2px}
    .cc-preset-btn{display:inline-flex;align-items:center;gap:5px;padding:6px 12px;background:rgba(168,85,247,0.08);border:1px solid rgba(168,85,247,0.2);color:#c084fc;border-radius:8px;font-size:11px;font-weight:600;cursor:pointer;transition:all .25s}
    .cc-preset-btn:hover{background:rgba(168,85,247,0.15);border-color:rgba(168,85,247,0.35);transform:translateY(-1px)}
    .cc-preset-btn:active{transform:translateY(0)}
    .cc-actions{display:flex;gap:10px;margin-top:16px;align-items:center}
    .cc-btn-save{display:inline-flex;align-items:center;gap:6px;padding:10px 24px;background:linear-gradient(135deg,rgba(168,85,247,0.35),rgba(139,92,246,0.25));color:#c084fc;border:1px solid rgba(168,85,247,0.3);border-radius:12px;font-size:13px;font-weight:700;cursor:pointer;transition:all .25s}
    .cc-btn-save:hover{background:linear-gradient(135deg,rgba(168,85,247,0.45),rgba(139,92,246,0.35));transform:translateY(-1px);box-shadow:0 4px 20px rgba(168,85,247,0.2)}
    .cc-btn-save:active{transform:translateY(0)}
    .cc-btn-save:disabled{opacity:.5;cursor:not-allowed;transform:none;box-shadow:none}
    </style>
    <div class="ar-header">
      <div class="ar-header-icon">&#x1F504;</div>
      <div class="ar-header-text">
        <h2>AppReplace</h2>
        <p>Regex-замены в app.min.js при отдаче клиенту. Включение премиума, скрытие элементов, отключение рекламы.</p>
      </div>
      <div class="ar-count" id="ar-count">0 правил</div>
    </div>
    <div class="ar-presets">
      <div class="ar-presets-title">Готовые пресеты</div>
      <button class="ar-preset-btn" onclick="addARPreset('premium')"><span class="ar-pi">&#x1F451;</span> Премиум</button>
      <button class="ar-preset-btn" onclick="addARPreset('hide_subscribe')"><span class="ar-pi">&#x1F6AB;</span> Скрыть подписку</button>
      <button class="ar-preset-btn" onclick="addARPreset('disable_vast')"><span class="ar-pi">&#x1F4F5;</span> Без рекламы</button>
      <button class="ar-preset-btn" onclick="addARPreset('remove_comments')"><span class="ar-pi">&#x1F5D1;</span> Без комментариев</button>
    </div>
    <div id="ar-rules"></div>
    <div class="ar-actions">
      <button class="ar-btn-add" onclick="addARRow()">&#x2795; Добавить правило</button>
      <button class="ar-btn-save" id="ar-save-btn" onclick="saveAR()">&#x1F4BE; Сохранить</button>
      <span class="ar-status-msg" id="ar-status"></span>
    </div>
    <!-- Custom CSS/JS -->
    <div class="cc-divider"></div>
    <div class="cc-header">
      <div class="cc-header-icon">&#x1F3A8;</div>
      <div class="cc-header-text">
        <h2>Custom CSS / JS</h2>
        <p>Произвольный код, который добавляется в конец app.css и app.min.js. Для кастомных тем, скрытия элементов, своих скриптов.</p>
      </div>
    </div>
    <div class="cc-presets">
      <div class="cc-presets-title">CSS пресеты</div>
      <button class="cc-preset-btn" onclick="addCSSPreset('hide_subscribe')">&#x1F6AB; Скрыть подписку</button>
      <button class="cc-preset-btn" onclick="addCSSPreset('hide_comments')">&#x1F5D1; Скрыть комментарии</button>
      <button class="cc-preset-btn" onclick="addCSSPreset('hide_ads')">&#x1F4F5; Скрыть рекламу</button>
      <button class="cc-preset-btn" onclick="addCSSPreset('gold_accent')">&#x2728; Золотой акцент</button>
    </div>
    <div class="cc-editors">
      <div class="cc-editor">
        <div class="cc-editor-head"><span class="cc-lang cc-lang-css">CSS</span> app.css <span class="cc-lines" id="cc-css-lines">0 строк</span></div>
        <textarea id="cc-css" placeholder="/* Свой CSS код */&#10;.button--subscribe {&#10;  display: none !important;&#10;}" oninput="ccUpdateLines()"></textarea>
      </div>
      <div class="cc-editor">
        <div class="cc-editor-head"><span class="cc-lang cc-lang-js">JS</span> app.min.js <span class="cc-lines" id="cc-js-lines">0 строк</span></div>
        <textarea id="cc-js" placeholder="// Свой JavaScript код&#10;console.log('Custom JS loaded');" oninput="ccUpdateLines()"></textarea>
      </div>
    </div>
    <div class="cc-actions">
      <button class="cc-btn-save" id="cc-save-btn" onclick="saveCustomCode()">&#x1F4BE; Сохранить CSS/JS</button>
      <span class="ar-status-msg" id="cc-status"></span>
    </div>
  </div>
  <!-- CONFIG -->
  <div class="panel" id="panel-config">
    <div style="display:flex;align-items:center;gap:12px;margin-bottom:4px">
      <div class="section-title" style="margin-bottom:0">⚙ Конфигурация</div>
      <span class="toml-badge toml-badge-toml">TOML</span>
      <span class="toml-badge toml-badge-source" id="cfg-source-badge"></span>
      <div class="cfg-modified-dot" id="cfg-modified-dot" title="Есть несохранённые изменения"></div>
    </div>
    <div class="section-desc">Редактирование config.toml — с подсветкой синтаксиса, валидацией и откатом</div>

    <!-- LLM Settings -->
    <div style="background:var(--surface2);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:16px;margin-bottom:16px">
      <div style="display:flex;align-items:center;gap:10px;margin-bottom:12px">
        <input type="checkbox" id="cfg-llm-enable" style="width:18px;height:18px;cursor:pointer" onchange="toggleLLMConfig()">
        <label for="cfg-llm-enable" style="font-size:15px;font-weight:bold;cursor:pointer">&#129302; LLM-портировщик</label>
        <span style="font-size:11px;color:#888">(Qwen2.5-Coder через llama.cpp)</span>
      </div>
      <div id="cfg-llm-fields" style="display:none">
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:10px">
          <div>
            <label style="font-size:12px;color:var(--text-muted)">Endpoint (URL API)</label>
            <input class="input-sm" id="cfg-llm-endpoint" style="width:100%;margin-top:2px" placeholder="https://openrouter.ai/api/v1">
          </div>
          <div>
            <label style="font-size:12px;color:var(--text-muted)">API Key</label>
            <input class="input-sm" id="cfg-llm-apikey" type="password" style="width:100%;margin-top:2px" placeholder="sk-or-...">
          </div>
          <div style="grid-column:1/-1">
            <label style="font-size:12px;color:var(--text-muted)">Модель</label>
            <input class="input-sm" id="cfg-llm-model" style="width:100%;margin-top:2px" placeholder="qwen/qwen-2.5-coder-32b-instruct">
          </div>
          <div>
            <label style="font-size:12px;color:var(--text-muted)">Температура (0.0 - 1.0)</label>
            <input class="input-sm" id="cfg-llm-temp" type="number" step="0.1" min="0" max="1" style="width:100%;margin-top:2px" placeholder="0.1">
          </div>
          <div>
            <label style="font-size:12px;color:var(--text-muted)">Макс. попыток исправления</label>
            <input class="input-sm" id="cfg-llm-retries" type="number" min="1" max="20" style="width:100%;margin-top:2px" placeholder="5">
          </div>
        </div>
        <div style="font-size:11px;color:#666;margin-top:8px">
          Облако: <a href="https://openrouter.ai" target="_blank" style="color:#58a6ff">openrouter.ai</a> — Endpoint: <code style="background:var(--surface3);padding:2px 6px;border-radius:3px">https://openrouter.ai/api/v1</code><br>
          Локально: <code style="background:var(--surface3);padding:2px 6px;border-radius:3px">llama-server -m model.gguf --port 8080</code> Endpoint: <code style="background:var(--surface3);padding:2px 6px;border-radius:3px">http://127.0.0.1:8080</code>
        </div>
      </div>
    </div>

    <!-- File navigator + TOML Editor -->
    <div class="cfg-layout" id="cfg-layout">
      <!-- Sidebar file tree -->
      <div class="cfg-nav" id="cfg-nav">
        <div class="cfg-nav-header">Секции конфига</div>
        <div class="cfg-nav-item active" data-section="*" onclick="switchConfigSection('*')">
          <span class="nav-icon">📄</span><span class="nav-label">Весь конфиг</span>
        </div>
        <div class="cfg-nav-sep"></div>
        <div class="cfg-nav-item" data-section="server" onclick="switchConfigSection('server')">
          <span class="nav-icon">🖥️</span><span class="nav-label">Сервер</span>
        </div>
        <div class="cfg-nav-item" data-section="online" onclick="switchConfigSection('online')">
          <span class="nav-icon">🎬</span><span class="nav-label">Балансеры</span>
        </div>
        <div class="cfg-nav-item" data-section="web" onclick="switchConfigSection('web')">
          <span class="nav-icon">🔌</span><span class="nav-label">Плагины</span>
        </div>
        <div class="cfg-nav-item" data-section="proxy" onclick="switchConfigSection('proxy')">
          <span class="nav-icon">🌐</span><span class="nav-label">Прокси / VLESS</span>
        </div>
        <div class="cfg-nav-item" data-section="telegram" onclick="switchConfigSection('telegram')">
          <span class="nav-icon">💬</span><span class="nav-label">Telegram</span>
        </div>
        <div class="cfg-nav-item" data-section="torrserver" onclick="switchConfigSection('torrserver')">
          <span class="nav-icon">🧲</span><span class="nav-label">TorrServer</span>
        </div>
        <div class="cfg-nav-item" data-section="transcoding" onclick="switchConfigSection('transcoding')">
          <span class="nav-icon">🎞️</span><span class="nav-label">Транскодинг</span>
        </div>
        <div class="cfg-nav-item" data-section="cub" onclick="switchConfigSection('cub')">
          <span class="nav-icon">🐻</span><span class="nav-label">CUB / Sync</span>
        </div>
        <div class="cfg-nav-item" data-section="sisi" onclick="switchConfigSection('sisi')">
          <span class="nav-icon">🔞</span><span class="nav-label">SISI</span>
        </div>
        <div class="cfg-nav-item" data-section="admin" onclick="switchConfigSection('admin')">
          <span class="nav-icon">🔐</span><span class="nav-label">Админ / LLM</span>
        </div>
        <div class="cfg-nav-item" data-section="proxy_link" onclick="switchConfigSection('proxy_link')">
          <span class="nav-icon">🔗</span><span class="nav-label">ProxyLink</span>
        </div>
      </div>
      <!-- Editor area -->
      <div class="cfg-editor-main">
        <div class="toml-editor-wrap" id="toml-editor-wrap">
          <div class="toml-toolbar">
            <span class="toml-badge toml-badge-toml" id="cfg-section-badge">config.toml</span>
            <span id="cfg-file-path" style="font-size:10px;color:var(--text-dim);font-family:monospace"></span>
            <div class="toml-toolbar-spacer"></div>
            <div class="toml-status" id="cfg-status">
              <span class="dot yellow"></span>
              <span style="color:var(--text-muted);font-size:11px">Загрузка...</span>
            </div>
          </div>
          <div class="toml-editor-container">
            <div class="toml-line-numbers" id="toml-line-numbers"></div>
            <div class="toml-code-area">
              <div class="toml-highlight" id="toml-highlight"></div>
              <textarea class="toml-textarea" id="toml-textarea" spellcheck="false" autocomplete="off" autocorrect="off" autocapitalize="off"></textarea>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Error panel -->
    <div class="cfg-error-panel" id="cfg-error-panel">
      <div class="error-title" id="cfg-error-title">Ошибка валидации</div>
      <div class="error-detail" id="cfg-error-detail"></div>
    </div>

    <!-- Action bar -->
    <div class="cfg-action-bar">
      <button class="btn btn-primary" id="cfg-save-btn" onclick="saveConfigTOML()">Сохранить и применить</button>
      <button class="btn btn-warning" id="cfg-validate-btn" onclick="validateConfigTOML()">Проверить</button>
      <button class="btn btn-secondary" onclick="loadConfigTOML()">Перезагрузить</button>
      <div style="flex:1"></div>
      <span id="cfg-save-status" style="font-size:12px;color:var(--text-muted)"></span>
    </div>

    <!-- Backups panel -->
    <div class="cfg-backups-panel" id="cfg-backups-panel">
      <div class="cfg-backups-header" onclick="toggleBackups()">
        <span class="chevron" id="cfg-backups-chevron">&#9654;</span>
        <span class="cfg-backups-title">История изменений (бэкапы)</span>
        <span class="cfg-backups-count" id="cfg-backups-count">0</span>
      </div>
      <div class="cfg-backups-body" id="cfg-backups-body"></div>
    </div>

    <!-- Legacy JSON editor -->
    <div style="margin-top:20px">
      <div class="srv-collapsible" onclick="toggleLegacyJSON()" style="font-size:14px;color:var(--text);gap:8px;padding:8px 0">
        <span class="chevron" id="legacy-json-chevron">&#9654;</span>
        <span>init.conf (JSON)</span>
        <span style="font-size:11px;color:var(--text-dim);margin-left:8px">legacy-формат</span>
      </div>
      <div id="legacy-json-body" style="display:none;margin-top:8px">
        <textarea class="config-editor" id="config-editor"></textarea>
        <div class="config-actions">
          <button class="btn btn-primary" onclick="saveConfig()">Сохранить JSON</button>
          <button class="btn btn-warning" onclick="loadConfig()">Перезагрузить</button>
        </div>
      </div>
    </div>
  </div>
  <!-- SKIP INTRO -->
  <div class="panel" id="panel-skipintro">
    <div class="section-title">&#9193; Интро/Аутро — Skip DB</div>
    <div class="section-desc">Управление базой пропуска заставок и титров. Данные хранятся по IMDb ID + сезон + эпизод.</div>
    <div id="skip-stats" style="margin:12px 0;color:var(--text-muted);font-size:13px"></div>
    <div style="margin:12px 0;display:flex;align-items:center;gap:12px;flex-wrap:wrap">
      <input id="skip-imdb" type="text" placeholder="IMDb ID (tt0944947)" style="background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px 12px;font-size:13px;width:200px">
      <button class="btn btn-secondary" onclick="skipSearch()">Найти</button>
      <button class="btn btn-secondary" onclick="loadSkipIntro()">Обновить</button>
    </div>
    <div id="skip-show-data" style="margin:12px 0"></div>
    <div style="margin:20px 0;border-top:1px solid rgba(255,255,255,0.08);padding-top:16px">
      <div style="font-weight:600;margin-bottom:8px;color:var(--text)">Добавить/обновить сегмент</div>
      <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">
        <input id="skip-add-imdb" type="text" placeholder="IMDb ID" style="background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px;font-size:13px;width:140px">
        <input id="skip-add-s" type="number" placeholder="S" min="0" style="background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px;font-size:13px;width:60px">
        <input id="skip-add-e" type="number" placeholder="E" min="0" style="background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px;font-size:13px;width:60px">
        <select id="skip-add-type" style="background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px;font-size:13px">
          <option value="intro">intro</option>
          <option value="outro">outro</option>
          <option value="recap">recap</option>
        </select>
        <input id="skip-add-start" type="number" placeholder="Start (s)" min="0" step="0.1" style="background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px;font-size:13px;width:100px">
        <input id="skip-add-end" type="number" placeholder="End (s)" min="0" step="0.1" style="background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px;font-size:13px;width:100px">
        <button class="btn btn-primary" onclick="skipAddSegment()">Добавить</button>
      </div>
    </div>
    <div style="margin:20px 0;border-top:1px solid rgba(255,255,255,0.08);padding-top:16px">
      <div style="font-weight:600;margin-bottom:8px;color:var(--text)">Пользовательские маркеры</div>
      <div id="skip-marks-list"></div>
    </div>
  </div>
  <!-- CALENDAR -->
  <div class="panel" id="panel-calendar">
    <div class="section-title">&#128197; Контент-календарь</div>
    <div class="section-desc">Расписание выхода новых серий отслеживаемых сериалов. Сервер опрашивает TMDB и уведомляет пользователей через TG бот.</div>
    <div id="cal-stats" style="margin:12px 0;color:var(--text-muted);font-size:13px"></div>
    <div style="margin:12px 0;display:flex;align-items:center;gap:12px">
      <button class="btn btn-secondary" onclick="loadCalendar()">Обновить</button>
      <button class="btn btn-primary" onclick="calCheckNow()">Проверить сейчас</button>
    </div>
    <div style="margin:16px 0">
      <div style="font-weight:600;margin-bottom:8px;color:var(--text)">Топ сериалов по подписчикам</div>
      <div id="cal-popular"></div>
    </div>
  </div>
  <!-- IPTV -->
  <div class="panel" id="panel-iptv">
    <div class="section-title">&#128250; IPTV</div>
    <div class="section-desc">Серверный IPTV — пользователи загружают M3U плейлисты, сервер парсит каналы, проксирует потоки и предоставляет EPG.</div>
    <div style="margin:12px 0">
      <label style="display:flex;align-items:center;gap:8px">
        <span style="font-size:13px;color:var(--text)">Включить IPTV</span>
        <label class="toggle"><input type="checkbox" id="iptv-enable" onchange="onIPTVToggle(this.checked)"><span class="slider"></span></label>
      </label>
    </div>
    <div id="iptv-settings" style="display:none;padding:12px;background:var(--surface2);border-radius:8px;border:1px solid rgba(255,255,255,0.06)">
      <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;margin:12px 0">
        <div>
          <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">Обновлять EPG каждые (часов)</label>
          <input class="input-sm" id="iptv-epg-hours" type="number" min="1" max="168" value="6" style="width:100%;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:6px 10px;font-size:13px">
        </div>
        <div>
          <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">Макс. плейлистов на юзера</label>
          <input class="input-sm" id="iptv-max-playlists" type="number" min="1" max="100" value="10" style="width:100%;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:6px 10px;font-size:13px">
        </div>
      </div>
      <div style="margin:12px 0">
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">Проксирование по умолчанию</label>
        <select id="iptv-default-proxy" style="width:100%;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:6px 10px;font-size:13px">
          <option value="none">none — прямая ссылка</option>
          <option value="all">all — через /proxy/</option>
        </select>
      </div>
      <div style="margin:12px 0">
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">EPG источники (по одному на строку)</label>
        <textarea id="iptv-epg-urls" style="width:100%;min-height:70px;font-family:monospace;font-size:12px;resize:vertical;padding:8px;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:6px" placeholder="https://epg.example.com/xmltv.xml"></textarea>
      </div>
      <div style="margin:12px 0">
        <label style="display:block;font-size:12px;color:var(--text-muted);margin-bottom:4px">Глобальные плейлисты (доступны всем, по одному на строку)</label>
        <textarea id="iptv-global-playlists" style="width:100%;min-height:70px;font-family:monospace;font-size:12px;resize:vertical;padding:8px;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:6px" placeholder="https://m3u.su/rusm&#10;https://example.com/tv.m3u"></textarea>
      </div>
      <div id="iptv-stats" style="margin:12px 0;color:var(--text-muted);font-size:13px"></div>
    </div>
    <div style="margin:16px 0">
      <button class="btn btn-primary" onclick="saveIPTVSettings()">Сохранить</button>
    </div>
  </div>
  <!-- BROADCAST -->
  <!-- XSEARCH -->
  <div class="panel" id="panel-xsearch">
    <div class="section-title">&#128269; Кросс-балансерный поиск</div>
    <div class="section-desc">Единый текстовый поиск по всем балансерам с дедупликацией и рейтингом качества.</div>
    <div id="xs-stats" style="margin:12px 0;color:var(--text-muted);font-size:13px">Загрузка...</div>
    <div style="margin:16px 0">
      <div style="font-weight:600;margin-bottom:8px;color:var(--text)">Активные источники поиска</div>
      <div id="xs-sources" style="display:flex;flex-wrap:wrap;gap:8px"></div>
    </div>
    <div style="margin:16px 0">
      <div style="font-weight:600;margin-bottom:8px;color:var(--text)">Тест поиска</div>
      <div style="display:flex;gap:8px;align-items:center">
        <input id="xs-test-input" style="flex:1;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px 12px;font-size:13px" placeholder="Название фильма или сериала..." />
        <button class="btn btn-primary" onclick="xsTestSearch()">Поиск</button>
      </div>
      <div id="xs-test-results" style="margin-top:12px"></div>
    </div>
  </div>
  <!-- COLLECTIONS -->
  <div class="panel" id="panel-collections">
    <div class="section-title">&#127912; Умные коллекции TMDB</div>
    <div class="section-desc">Автогенерация подборок по персонам, жанрам и студиям из TMDB. Trending обновляется автоматически.</div>
    <div id="coll-stats" style="margin:12px 0;color:var(--text-muted);font-size:13px">Загрузка...</div>
    <div style="margin:16px 0">
      <div style="font-weight:600;margin-bottom:8px;color:var(--text)">Закреплённые персоны</div>
      <div id="coll-pinned" style="display:flex;flex-wrap:wrap;gap:8px;margin-bottom:12px"></div>
      <div style="display:flex;gap:8px;align-items:center">
        <input id="coll-pin-input" style="flex:1;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px 12px;font-size:13px" placeholder="Имя персоны для поиска..." />
        <button class="btn btn-primary" onclick="collSearchPerson()">Найти</button>
      </div>
      <div id="coll-pin-results" style="margin-top:8px"></div>
    </div>
    <div style="margin:16px 0">
      <div style="font-weight:600;margin-bottom:8px;color:var(--text)">Тест поиска</div>
      <div style="display:flex;gap:8px;align-items:center">
        <input id="coll-test-input" style="flex:1;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:8px 12px;font-size:13px" placeholder="Имя актёра или режиссёра..." />
        <button class="btn btn-primary" onclick="collTestPerson()">Фильмы</button>
      </div>
      <div id="coll-test-results" style="margin-top:12px"></div>
    </div>
  </div>
  <div class="panel" id="panel-broadcast">
    <div class="section-title">📢 Рассылка</div>
    <div class="section-desc">Отправка HTML-сообщения всем активным пользователям через Telegram бота</div>
    <div id="broadcast-recipients" style="margin:12px 0;color:var(--text-muted);font-size:13px"></div>
    <textarea id="broadcast-text" rows="8" style="width:100%;background:var(--surface3);color:var(--text);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:12px;font-family:monospace;font-size:13px;resize:vertical" placeholder="&lt;b&gt;Заголовок&lt;/b&gt;&#10;&#10;Текст сообщения с &lt;i&gt;HTML&lt;/i&gt; разметкой.&#10;&#10;&lt;a href='https://...'&gt;Ссылка&lt;/a&gt;"></textarea>
    <div style="margin-top:8px;color:var(--text-dim);font-size:11px">Поддерживаемые теги: &lt;b&gt;, &lt;i&gt;, &lt;u&gt;, &lt;s&gt;, &lt;code&gt;, &lt;pre&gt;, &lt;a href="..."&gt;</div>
    <div style="margin-top:12px;display:flex;gap:12px;align-items:center">
      <button class="btn btn-primary" id="broadcast-btn" onclick="sendBroadcast()">Отправить</button>
      <span id="broadcast-status" style="font-size:13px"></span>
    </div>
  </div>
  <!-- LAMPA UPDATE -->
  <div class="panel" id="panel-lampa">
    <div class="section-title">💡 Обновление Lampa</div>
    <div class="section-desc">Проверка и обновление клиента Lampa из репозитория GitHub</div>
    <div style="margin-top:16px;display:flex;gap:24px;flex-wrap:wrap">
      <div style="background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.08);border-radius:12px;padding:20px;flex:1;min-width:200px">
        <div style="font-size:12px;color:var(--text-muted);text-transform:uppercase;margin-bottom:8px">Локальная версия</div>
        <div id="lampa-local-ver" style="font-size:28px;font-weight:700;color:var(--accent)">—</div>
        <div id="lampa-local-hash" style="font-size:11px;color:var(--text-dim);margin-top:4px"></div>
      </div>
      <div style="background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.08);border-radius:12px;padding:20px;flex:1;min-width:200px">
        <div style="font-size:12px;color:var(--text-muted);text-transform:uppercase;margin-bottom:8px">Upstream версия</div>
        <div id="lampa-remote-ver" style="font-size:28px;font-weight:700;color:var(--text)">—</div>
        <div id="lampa-remote-hash" style="font-size:11px;color:var(--text-dim);margin-top:4px"></div>
      </div>
    </div>
    <div id="lampa-status" style="margin-top:16px;padding:12px 16px;border-radius:8px;font-size:13px;display:none"></div>
    <div style="margin-top:20px;display:flex;gap:12px;align-items:center;flex-wrap:wrap">
      <button class="btn btn-primary" id="lampa-check-btn" onclick="checkLampaVersion()">Проверить обновления</button>
      <button class="btn btn-warning" id="lampa-update-btn" onclick="updateLampa(false)" style="display:none">Обновить (быстро)</button>
      <button class="btn btn-warning" id="lampa-full-btn" onclick="updateLampa(true)" style="display:none">Полная синхронизация</button>
    </div>
    <div id="lampa-files" style="margin-top:16px;display:none">
      <div style="font-size:12px;color:var(--text-muted);text-transform:uppercase;margin-bottom:6px">Обновлённые файлы</div>
      <div id="lampa-files-list" style="background:var(--surface3);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:12px;font-family:monospace;font-size:12px;max-height:300px;overflow-y:auto"></div>
    </div>
  </div>
  <!-- SELF UPDATE -->
  <div class="panel" id="panel-selfupdate">
    <div class="section-title">&#x1F680; Обновление lampac-go</div>
    <div class="section-desc">Проверка, скачивание и установка новой сборки сервера с сервера обновлений (alcopa-site)</div>

    <div style="margin-top:16px;display:flex;gap:16px;flex-wrap:wrap">
      <div class="su-card">
        <div class="su-lbl">Текущая версия</div>
        <div id="su-current" class="su-val" style="color:var(--accent)">—</div>
        <div id="su-commit" class="su-sub"></div>
      </div>
      <div class="su-card">
        <div class="su-lbl">Последняя на сервере</div>
        <div id="su-latest" class="su-val">—</div>
        <div id="su-latest-date" class="su-sub"></div>
      </div>
      <div class="su-card">
        <div class="su-lbl">Режим запуска</div>
        <div id="su-mode" class="su-val" style="font-size:20px">—</div>
        <div id="su-mode-sub" class="su-sub"></div>
      </div>
      <div class="su-card">
        <div class="su-lbl">Артефакт</div>
        <div id="su-asset" class="su-val" style="font-size:15px;font-family:monospace">—</div>
        <div id="su-channel" class="su-sub"></div>
      </div>
    </div>

    <div style="margin-top:12px;display:flex;gap:18px;flex-wrap:wrap;font-size:12px;color:var(--text-muted)">
      <div>&#x1F4E1; Интервал: <span id="su-interval">—</span></div>
      <div>&#x1F552; Окно: <span id="su-window">—</span></div>
      <div>&#x1F510; Подпись: <span id="su-sig">—</span></div>
      <div>&#x1F3A5; Активных стримов: <span id="su-streams" style="color:var(--accent)">0</span></div>
    </div>

    <div id="su-status" style="margin-top:16px;padding:12px 16px;border-radius:8px;font-size:13px;display:none"></div>

    <div style="margin-top:20px;display:flex;gap:12px;align-items:center;flex-wrap:wrap">
      <button class="btn btn-primary" id="su-check-btn" onclick="suCheck()">Проверить обновления</button>
      <button class="btn btn-warning" id="su-apply-btn" onclick="suApply()" style="display:none">Установить и перезапустить</button>
      <button class="btn" id="su-rollback-btn" onclick="suRollback()" style="display:none;background:#444;color:#fff">Откатить</button>
    </div>

    <div id="su-docker-hint" style="display:none;margin-top:16px;padding:12px 14px;background:rgba(100,150,255,0.08);border:1px solid rgba(100,150,255,0.25);border-radius:8px;font-size:13px;color:var(--text-muted)">
      <div style="margin-bottom:6px">&#x1F4CB; В Docker самообновление недоступно. Выполните на хосте:</div>
      <div id="su-docker-cmd" style="font-family:monospace;background:rgba(0,0,0,0.35);padding:8px 10px;border-radius:6px;color:var(--accent);word-break:break-all"></div>
    </div>

    <div style="margin-top:24px;padding-top:18px;border-top:1px solid rgba(255,255,255,0.08)">
      <div class="su-lbl" style="margin-bottom:6px">&#x1F4E1; Канал обновлений</div>
      <div class="section-desc" style="margin-bottom:10px">
        <code>stable</code> — для всех; приватный канал (создаётся на сервере обновлений с паролем) получает сборки,
        опубликованные только для него. Пароль хранится локально в <code>database/updater/settings.json</code>.
        Пустые поля — вернуться к настройкам из <code>config.toml</code>.
      </div>
      <div style="display:flex;gap:10px;flex-wrap:wrap;align-items:center">
        <input id="su-ch-name" type="text" placeholder="stable" style="width:160px;background:var(--surface3);border:1px solid rgba(255,255,255,0.12);border-radius:8px;padding:8px 10px;color:var(--text);font-size:13px">
        <input id="su-ch-pass" type="password" placeholder="пароль канала (если есть)" autocomplete="new-password" style="width:220px;background:var(--surface3);border:1px solid rgba(255,255,255,0.12);border-radius:8px;padding:8px 10px;color:var(--text);font-size:13px">
        <button class="btn btn-primary" id="su-ch-save" onclick="suSaveChannel()">Сохранить и проверить</button>
        <span id="su-ch-status" style="font-size:12px;color:var(--text-muted)"></span>
      </div>
    </div>

    <div id="su-release-notes" style="display:none;margin-top:20px">
      <div class="su-lbl" style="margin-bottom:6px">Release notes</div>
      <div id="su-release-notes-body" style="background:var(--surface3);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:12px 14px;font-size:12px;white-space:pre-wrap;max-height:280px;overflow-y:auto;color:var(--text-muted)"></div>
      <a id="su-release-link" href="#" target="_blank" rel="noopener" style="display:inline-block;margin-top:8px;font-size:12px;color:var(--accent)">&#x2197; Открыть release notes</a>
    </div>

    <div id="su-history" style="margin-top:24px">
      <div class="su-lbl" style="margin-bottom:8px">История операций</div>
      <div id="su-history-list" style="background:var(--surface3);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:10px;font-size:12px;font-family:monospace;max-height:260px;overflow-y:auto;color:var(--text-muted)">—</div>
    </div>

    <!-- Reference config block -->
    <div style="margin-top:28px;padding-top:20px;border-top:1px solid rgba(255,255,255,0.08)">
      <div class="section-title" style="font-size:16px">⚙️ Эталонный конфиг</div>
      <div class="section-desc" style="margin-bottom:10px">
        Подтянуть эталонный TOML с сервера обновлений. Это общие настройки балансеров — <b>не содержит</b>
        портов, Telegram-токенов, пароля админки, TorrServer. Скачивается в <code id="rc-path">config.reference.toml</code>
        рядом с вашим <code>config.toml</code>; применяется вручную.
      </div>
      <div style="display:flex;gap:10px;flex-wrap:wrap;margin-bottom:12px">
        <button class="btn btn-primary" id="rc-fetch-btn" onclick="rcFetch()">Запросить с сервера</button>
        <button class="btn" id="rc-save-btn" onclick="rcSave()" style="display:none;background:var(--accent);color:#000">💾 Сохранить в config.reference.toml</button>
        <span id="rc-status" style="align-self:center;font-size:12px;color:var(--text-muted)"></span>
      </div>
      <div id="rc-meta" style="font-size:11px;color:var(--text-dim);margin-bottom:8px;display:none"></div>
      <div id="rc-note" style="font-size:12px;color:var(--accent);margin-bottom:8px;display:none"></div>
      <textarea id="rc-body" rows="18" spellcheck="false"
        style="display:none;width:100%;background:var(--surface3);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:10px 12px;color:var(--text);font-family:ui-monospace,monospace;font-size:12px;resize:vertical"></textarea>
    </div>
  </div>
  <style>
    .su-card{background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.08);border-radius:12px;padding:18px 20px;flex:1;min-width:190px}
    .su-lbl{font-size:11px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.5px;margin-bottom:6px}
    .su-val{font-size:26px;font-weight:700;color:var(--text)}
    .su-sub{font-size:11px;color:var(--text-dim);margin-top:4px}
    .su-hist-row{padding:4px 0;border-bottom:1px dashed rgba(255,255,255,0.05)}
    .su-hist-row:last-child{border-bottom:none}
    .su-hist-ok{color:var(--accent)}
    .su-hist-err{color:#e74c3c}
  </style>
  <!-- FEEDBACK -->
  <!-- DEPS -->
  <div class="panel" id="panel-deps">
    <div class="section-title">📦 Системные зависимости</div>
    <div class="section-desc">Проверка компонентов, необходимых для работы балансеров и сервисов</div>
    <div style="margin:12px 0;display:flex;gap:8px;flex-wrap:wrap">
      <button class="btn btn-secondary" onclick="loadDeps()">Обновить список</button>
      <button class="btn" id="deps-check-updates-btn" onclick="checkDepsUpdates()" style="background:var(--accent);color:#000">Проверить обновления</button>
      <button class="btn" id="deps-install-all-btn" onclick="installAllMissing()" style="background:#e74c3c;color:#fff;display:none">&#x1F4E5; Установить всё</button>
    </div>
    <div id="deps-content"></div>
    <div id="deps-sys" style="margin-top:16px;color:var(--text-muted);font-size:12px"></div>
  </div>
  <!-- INSPECTOR -->
  <div class="panel" id="panel-inspector">
    <div class="section-title">&#x1F50D; Инспектор проблем</div>
    <div class="section-desc">Анализ всех подсистем сервера. Находит проблемы и предлагает решения.</div>
    <div style="margin:12px 0;display:flex;gap:8px;align-items:center">
      <button class="btn btn-primary" onclick="runInspector()" id="inspector-run-btn">&#x25B6; Запустить проверку</button>
      <span id="inspector-status" style="font-size:12px;color:var(--text-dim)"></span>
    </div>
    <div id="inspector-results"></div>
    <div id="inspector-summary" style="margin-top:16px;font-size:13px;color:var(--text-dim)"></div>
  </div>
  <div class="panel" id="panel-feedback">
    <div class="section-title">💬 Обратная связь</div>
    <div class="section-desc">Управление тикетами и обращениями пользователей</div>
    <div class="fb-stats" id="fb-stats"></div>
    <div class="fb-filters">
      <select id="fb-f-status" onchange="fbApplyFilters()">
        <option value="">Все статусы</option>
        <option value="open">Открытые</option>
        <option value="in_progress">В работе</option>
        <option value="resolved">Решённые</option>
        <option value="closed">Закрытые</option>
      </select>
      <select id="fb-f-cat" onchange="fbApplyFilters()">
        <option value="">Все категории</option>
        <option value="help">Помощь</option>
        <option value="thanks">Благодарность</option>
        <option value="bug">Баг</option>
        <option value="feature">Фича</option>
        <option value="other">Другое</option>
      </select>
      <select id="fb-f-pri" onchange="fbApplyFilters()">
        <option value="">Все приоритеты</option>
        <option value="low">Низкий</option>
        <option value="medium">Средний</option>
        <option value="high">Высокий</option>
        <option value="critical">Критический</option>
      </select>
      <input class="input-sm" id="fb-f-search" placeholder="Поиск..." oninput="fbApplyFilters()" style="width:200px">
    </div>
    <table class="fb-tbl">
      <thead><tr>
        <th>Категория</th><th>Приоритет</th><th>Тема</th><th>Пользователь</th><th>Статус</th><th style="text-align:center">Ответы</th><th>Дата</th>
      </tr></thead>
      <tbody id="fb-tbody"></tbody>
    </table>
    <div class="empty" id="fb-empty" style="display:none">Нет тикетов</div>
  </div>
  <!-- TELEMETRY -->
  <div class="panel" id="panel-telemetry">
    <div class="section-title">📡 Телеметрия балансеров</div>
    <div class="section-desc">Реальное время: успешность запросов, латентность, авто-ротация хостов и алерты в Telegram при деградации.</div>
    <div id="tlm-summary" class="tlm-summary"></div>
    <div class="tlm-toolbar">
      <input id="tlm-search" class="input-sm" placeholder="🔎 Найти балансер..." style="flex:1;max-width:280px" oninput="tlmRender()">
      <select id="tlm-status" class="input-sm" style="width:140px" onchange="tlmRender()">
        <option value="">Все статусы</option>
        <option value="down">Недоступны</option>
        <option value="degraded">Деградируют</option>
        <option value="healthy">Здоровы</option>
        <option value="unknown">Без данных</option>
      </select>
      <select id="tlm-window" class="input-sm" style="width:120px" onchange="tlmRender()">
        <option value="5m" selected>За 5 мин</option>
        <option value="1m">За 1 мин</option>
        <option value="15m">За 15 мин</option>
        <option value="1h">За 1 час</option>
      </select>
      <label class="dash-auto-label" style="margin-left:auto"><input type="checkbox" id="tlm-live" checked> Live</label>
      <button class="btn btn-sm" onclick="tlmReset('')">Сбросить статистику</button>
    </div>
    <div id="tlm-grid" class="tlm-grid"></div>
    <div class="empty" id="tlm-empty" style="display:none">Нет данных. Балансеры не были вызваны или телеметрия отключена.</div>
  </div>
  <!-- MEDIA GATEWAY -->
  <div class="panel" id="panel-mediagw">
    <div class="section-title">🎞 Media Gateway</div>
    <div class="section-desc">Активные ffmpeg-задачи транскодинга и универсальный probe — посмотреть, что отдаёт CDN/балансер по конкретному URL.</div>

    <div class="mgw-section">
      <div class="mgw-h">📊 Очередь транскодинга</div>
      <div id="mgw-jobs-summary" class="mgw-summary"></div>
      <div id="mgw-jobs-list" class="mgw-jobs-list"></div>
      <div class="empty" id="mgw-jobs-empty" style="display:none">Нет активных задач транскодинга</div>
    </div>

    <div class="mgw-section">
      <div class="mgw-h">🔍 Probe URL (ffprobe)</div>
      <div class="mgw-probe-bar">
        <input id="mgw-probe-url" class="input-sm" placeholder="https://example.com/stream.m3u8" style="flex:1;min-width:300px">
        <button class="btn btn-sm" onclick="mgwProbe()">▶ Probe</button>
      </div>
      <div id="mgw-probe-result" class="mgw-probe-result"></div>
    </div>
  </div>
  <!-- LOGS -->
  <div class="panel" id="panel-logs">
    <div class="section-title">📜 Логи сервера</div>
    <div class="section-desc">Просмотр логов в реальном времени с фильтрацией и экспортом. Токены и ключи автоматически скрыты.</div>
    <!-- Category filter grid -->
    <div style="margin:16px 0 12px">
      <div style="display:flex;align-items:center;gap:8px;margin-bottom:10px">
        <span style="font-size:12px;color:var(--text-muted);font-weight:600;text-transform:uppercase;letter-spacing:.5px">Фильтры</span>
        <span style="flex:1;height:1px;background:rgba(255,255,255,0.06)"></span>
        <button class="btn btn-sm" style="font-size:10px;padding:2px 8px;background:transparent;color:var(--text-dim);border:1px solid rgba(255,255,255,0.08)" onclick="logsClearFilters()">Сбросить</button>
      </div>
      <div style="display:flex;flex-wrap:wrap;gap:6px;align-items:center">
        <div id="logs-cats" style="display:flex;flex-wrap:wrap;gap:6px"></div>
        <!-- Balancers dropdown -->
        <div style="position:relative;display:inline-block" id="logs-bal-wrap">
          <button id="logs-bal-btn" class="btn btn-sm" style="padding:4px 12px;border-radius:16px;border:1px solid rgba(255,255,255,0.06);background:var(--surface2);color:var(--text-muted);font-size:11px;cursor:pointer;display:flex;align-items:center;gap:5px" onclick="logsToggleBalDropdown()">
            <span style="font-size:13px">&#9881;</span> Балансеры <span id="logs-bal-count" style="font-size:10px;background:rgba(255,255,255,0.06);padding:0 5px;border-radius:8px;display:none">0</span> <span style="font-size:8px">&#9660;</span>
          </button>
          <div id="logs-bal-dropdown" style="display:none;position:absolute;top:calc(100% + 4px);left:0;z-index:50;background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.06);border-radius:10px;padding:8px;min-width:200px;box-shadow:0 8px 24px rgba(0,0,0,.4);max-height:300px;overflow-y:auto">
            <div id="logs-bal-list"></div>
          </div>
        </div>
      </div>
    </div>
    <!-- Controls bar -->
    <div style="display:flex;flex-wrap:wrap;gap:10px;align-items:center;padding:10px 14px;background:rgba(22,27,35,0.8);border:1px solid rgba(255,255,255,0.06);border-radius:10px;margin-bottom:12px">
      <select id="logs-level" class="input-sm" style="width:120px;background:var(--surface3);border-color:rgba(255,255,255,0.08)" onchange="loadLogs()">
        <option value="">Все уровни</option>
        <option value="error">Error</option>
        <option value="warn">Warn</option>
        <option value="info">Info</option>
        <option value="debug">Debug</option>
        <option value="trace">Trace</option>
      </select>
      <div style="position:relative;flex:1;min-width:160px;max-width:300px">
        <input class="input-sm" id="logs-search" placeholder="Поиск по тексту..." oninput="logsSearchDebounce()" style="width:100%;background:var(--surface3);border-color:#333;padding-left:28px">
        <span style="position:absolute;left:8px;top:50%;transform:translateY(-50%);color:var(--text-dim);font-size:13px;pointer-events:none">&#128269;</span>
      </div>
      <div style="height:20px;width:1px;background:rgba(255,255,255,0.06)"></div>
      <label id="logs-rt-label" style="font-size:12px;cursor:pointer;display:flex;align-items:center;gap:6px;padding:4px 12px;border-radius:8px;border:1px solid rgba(255,255,255,0.08);background:var(--surface3);color:var(--text-muted);transition:all .2s;user-select:none">
        <span id="logs-rt-dot" style="width:8px;height:8px;border-radius:50%;background:#555;transition:background .2s"></span>
        <input type="checkbox" id="logs-realtime" onchange="toggleLogsStream()" style="display:none"> Live
      </label>
      <div style="flex:1"></div>
      <button class="btn btn-primary btn-sm" onclick="loadLogs()" style="padding:5px 14px">Обновить</button>
      <div style="display:flex;gap:4px">
        <button class="btn btn-secondary btn-sm" onclick="exportLogs('json')" style="padding:5px 10px;font-size:11px" title="Экспорт JSON">&#128230; JSON</button>
        <button class="btn btn-secondary btn-sm" onclick="exportLogs('txt')" style="padding:5px 10px;font-size:11px" title="Экспорт TXT">&#128196; TXT</button>
      </div>
    </div>
    <!-- Log entries -->
    <div id="logs-entries" style="background:var(--surface3);border:1px solid rgba(255,255,255,0.06);border-radius:10px;max-height:600px;overflow-y:auto;font-family:'SF Mono','Fira Code','Courier New',monospace;font-size:12px;line-height:1.6"></div>
    <div style="margin-top:8px;display:flex;align-items:center;gap:12px">
      <div id="logs-status" style="font-size:11px;color:var(--text-muted)"></div>
      <div id="logs-rt-status" style="font-size:11px;color:var(--accent);display:none">&#9679; Подключено</div>
    </div>
  </div>

  <!-- TORRS -->
  <div class="panel" id="panel-torrs">
    <div class="section-title">&#x1F9F2; Торренты</div>
    <div class="section-desc">Управление встроенным торрент-сервером. Добавление, просмотр и удаление торрентов.</div>
    <div id="torrs-status" style="margin:12px 0;padding:10px 14px;border-radius:8px;background:var(--surface2);border:1px solid rgba(255,255,255,0.06);font-size:13px"></div>
    <div style="margin:12px 0;display:flex;gap:8px">
      <input type="text" id="torrs-add-link" class="input-sm input-wide" placeholder="Magnet-ссылка, URL .torrent или info hash" style="flex:1" onkeydown="if(event.key==='Enter')torrsAdd()">
      <button class="btn btn-sm" id="torrs-add-btn" onclick="torrsAdd()">+ Добавить</button>
    </div>
    <div id="torrs-list" style="margin-top:12px"></div>
  </div>

  <div class="panel" id="panel-jacred">
    <div class="section-title">&#x1F50E; Парсер jacred</div>
    <div class="section-desc">Веб-интерфейс встроенного (self-hosted) торрент-парсера jacred: поиск, статистика, настройки. Работает только при <code>[parser] jacred_local = true</code>.</div>
    <div id="jacred-status" style="margin:12px 0;padding:10px 14px;border-radius:8px;background:var(--surface2);border:1px solid rgba(255,255,255,0.06);font-size:13px">Проверяем состояние&hellip;</div>
    <div style="margin:10px 0"><a href="/jacred/" target="_blank" rel="noopener" class="btn-sm">Открыть в новой вкладке &#x2197;</a></div>
    <iframe id="jacred-frame" title="jacred" style="display:none;width:100%;height:calc(100vh - 240px);min-height:420px;border:1px solid rgba(255,255,255,0.1);border-radius:10px;background:#111"></iframe>
  </div>

</div>
</main>
</div>
<div class="fb-overlay" id="fb-overlay" onclick="fbCloseDetail()"></div>
<div class="fb-slide" id="fb-slide">
  <button class="close-x" onclick="fbCloseDetail()">&times;</button>
  <div id="fb-detail"></div>
</div>
<div class="toast" id="toast"></div>
<!-- Theme customization modal -->
<div class="theme-overlay" id="theme-overlay" onclick="if(event.target===this)closeThemeModal()">
  <div class="theme-modal">
    <div class="theme-modal-header">
      <span style="font-size:18px">&#x1F3A8;</span>
      <h2>Настройки темы</h2>
      <button class="theme-modal-close" onclick="closeThemeModal()">&#x2715;</button>
    </div>
    <div class="theme-modal-body">
      <div style="font-size:12px;color:var(--text-muted);margin-bottom:12px">Пресеты</div>
      <div class="theme-presets" id="theme-presets"></div>
      <div style="font-size:12px;color:var(--text-muted);margin:16px 0 10px">Цвета</div>
      <div class="theme-colors" id="theme-colors"></div>
    </div>
    <div class="theme-modal-footer">
      <button class="btn btn-danger btn-sm" onclick="resetTheme()">Сбросить</button>
      <button class="btn btn-primary" onclick="closeThemeModal()">Готово</button>
    </div>
  </div>
</div>
<script src="/admin-legacy.js" defer></script>
</body>
</html>
`
