// pages/selfupdate.js — check / apply / rollback the running binary.
//
// API:
//   GET  /api/update/status     → Info JSON (Current, Mode, UpdateAvail, History, ...)
//   POST /api/update/check      → re-check upstream, then return new Info
//   POST /api/update/apply      → download + swap + re-exec (BLOCKING — server restarts!)
//   POST /api/update/rollback   → swap `<exe>.old` back in
//
// Super-admin only. The Apply button shows a strong confirm because the
// process re-exec drops every active stream.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  info: null,
  loading: true,
  busy: '', // 'check' | 'apply' | 'rollback'
};

async function load() {
  state.loading = true;
  try {
    state.info = await api.get('/update/status');
  } catch (e) {
    toast.error('Не удалось получить статус: ' + e.message);
    state.info = null;
  } finally {
    state.loading = false;
  }
}

async function check() {
  state.busy = 'check'; paint();
  try {
    const r = await api.post('/update/check');
    if (r && r.ok === false) throw new Error(r.error || 'check failed');
    if (r && r.error) throw new Error(r.error);
    state.info = (r && r.info) || state.info;
    toast.success((state.info && state.info.update_available) ? 'Доступна новая версия' : 'Уже последняя версия');
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.busy = ''; paint();
  }
}

async function applyUpdate() {
  if (!confirm(
    'Применить обновление?\n\n' +
    'Сервер скачает бинарь, перезапишет и сделает re-exec.\n' +
    'ВСЕ активные стримы оборвутся. Откройте окно дашборда в другом табе — оно автоматически переподключится.\n\n' +
    'Продолжить?'
  )) return;
  state.busy = 'apply'; paint();
  try {
    const r = await api.post('/update/apply');
    if (r && r.error) throw new Error(r.error);
    toast.success(r && r.message || 'Бинарь обновлён, идёт re-exec…');
    // Server will re-exec; poll status every 3s for up to 60s to detect restart.
    pollUntilBack();
  } catch (e) {
    toast.error(e.message);
    state.busy = ''; paint();
  }
}

async function rollback() {
  if (!confirm('Откатиться на предыдущую версию?\nТекущий бинарь перейдёт в .old.')) return;
  state.busy = 'rollback'; paint();
  try {
    const r = await api.post('/update/rollback');
    if (r && r.error) throw new Error(r.error);
    toast.success('Откат выполнен, идёт re-exec…');
    pollUntilBack();
  } catch (e) {
    toast.error(e.message);
    state.busy = ''; paint();
  }
}

function pollUntilBack() {
  const start = Date.now();
  const intv = setInterval(async () => {
    if (Date.now() - start > 60000) {
      clearInterval(intv);
      state.busy = ''; paint();
      toast.warn('Сервер не вернулся за 60с — проверьте логи.');
      return;
    }
    try {
      const info = await api.get('/update/status');
      if (info) {
        clearInterval(intv);
        state.info = info;
        state.busy = '';
        toast.success('Сервер вернулся, версия: ' + (info.current || '?'));
        paint();
      }
    } catch {
      // expected during restart window
    }
  }, 2500);
}

function paint() {
  const $root = document.getElementById('su-root');
  if (!$root) return;

  if (state.loading) {
    render(html`<l-card loading title="Загрузка…"></l-card>`, $root);
    return;
  }
  const i = state.info || {};
  const canApply = !!i.can_self_update && !!i.update_available && !i.in_progress && state.busy === '';
  const canRoll  = !!i.has_rollback && !i.in_progress && state.busy === '';

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Текущая версия" value=${i.current || '—'} icon="◆"></l-stat>
      <l-stat accent=${i.update_available ? 'pink' : 'green'} label="Доступно" value=${i.latest_version || (i.update_available ? '(latest)' : 'актуально')} icon=${i.update_available ? '⇪' : '✓'}></l-stat>
      <l-stat accent="purple" label="Режим"   value=${i.mode || '?'} icon="⚙"></l-stat>
      <l-stat accent="amber"  label="Active streams" value=${i.active_streams || 0} icon="📡"></l-stat>
    </div>

    <l-card title="Управление" style="margin-top:var(--s-5)">
      <div slot="actions" style="display:flex;gap:8px;">
        <l-button variant="secondary" icon="↻" @click=${load}>Обновить статус</l-button>
        <l-button variant="primary"   icon="🔍" @click=${check} ?loading=${state.busy === 'check'}>Проверить</l-button>
        <l-button variant="success"   icon="⇪"  @click=${applyUpdate} ?disabled=${!canApply} ?loading=${state.busy === 'apply'}>Установить</l-button>
        <l-button variant="danger"    icon="↩"  @click=${rollback}   ?disabled=${!canRoll}  ?loading=${state.busy === 'rollback'}>Откатить</l-button>
      </div>

      <div class="su-info">
        <div class="su-row"><span>Канал</span><b>${i.channel || '—'}</b></div>
        <div class="su-row"><span>Asset</span><b>${i.asset || '—'}</b></div>
        <div class="su-row"><span>Auto-install</span><b>${i.auto_install ? 'да' : 'нет'}</b></div>
        <div class="su-row"><span>Check interval</span><b>${i.check_interval || '—'}</b></div>
        <div class="su-row"><span>Server URL</span><b><code>${i.server_url || '—'}</code></b></div>
        ${i.maintenance_window ? html`<div class="su-row"><span>Maintenance</span><b>${i.maintenance_window}</b></div>` : ''}
        <div class="su-row"><span>Можно самообновляться</span><b>${i.can_self_update ? 'да' : 'нет (Docker / read-only)'}</b></div>
        <div class="su-row"><span>Подпись готова</span><b>${i.signature_ready ? 'да' : 'нет'}${i.require_signature ? ' (обязательна)' : ''}</b></div>
        ${i.build_date ? html`<div class="su-row"><span>Build date</span><b>${i.build_date}</b></div>` : ''}
        ${i.commit ? html`<div class="su-row"><span>Commit</span><b><code>${i.commit}</code></b></div>` : ''}
        ${i.last_check ? html`<div class="su-row"><span>Last check</span><b>${new Date(i.last_check).toLocaleString('ru-RU')}</b></div>` : ''}
        ${i.last_apply ? html`<div class="su-row"><span>Last apply</span><b>${new Date(i.last_apply).toLocaleString('ru-RU')}</b></div>` : ''}
      </div>
      ${i.last_error ? html`<div class="su-err">⚠ ${i.last_error}</div>` : ''}
    </l-card>

    ${i.history && i.history.length ? html`
      <l-card title="История обновлений" style="margin-top:var(--s-5)">
        <l-table
          .columns=${[
            { key: 'time', label: 'Время', format: 'date', sortable: true },
            { key: 'event', label: 'Событие', cell: r => eventPill(r.event) },
            { key: 'from', label: 'От', cell: r => r.from ? html`<code style="font-family:var(--font-mono);font-size:var(--fs-xs)">${r.from}</code>` : html`<span style="color:var(--text-3)">—</span>` },
            { key: 'to', label: 'К', cell: r => r.to ? html`<code style="font-family:var(--font-mono);font-size:var(--fs-xs);color:var(--accent)">${r.to}</code>` : html`<span style="color:var(--text-3)">—</span>` },
            { key: 'error', label: 'Ошибка', cell: r => r.error ? html`<span style="color:var(--danger);font-size:var(--fs-xs)">${r.error}</span>` : html`<span style="color:var(--success)">ok</span>` },
          ]}
          .rows=${[...(i.history || [])].reverse()}
        ></l-table>
      </l-card>` : ''}
  `, $root);
}

function eventPill(ev) {
  switch (ev) {
    case 'apply':    return html`<l-pill tone="ok">apply</l-pill>`;
    case 'check':    return html`<l-pill tone="muted">check</l-pill>`;
    case 'rollback': return html`<l-pill tone="warn">rollback</l-pill>`;
    case 'error':    return html`<l-pill tone="danger" dot>error</l-pill>`;
    default:         return html`<l-pill tone="muted">${ev || '?'}</l-pill>`;
  }
}

const styleId = 'l-selfupdate-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }
    .su-info {
      display: grid; grid-template-columns: 1fr 1fr; gap: 6px 24px;
      margin-top: var(--s-2);
    }
    @media (max-width: 700px) { .su-info { grid-template-columns: 1fr; } }
    .su-row { display: flex; justify-content: space-between; padding: 6px 0; border-bottom: 1px dashed var(--border-1); font-size: var(--fs-sm); }
    .su-row span { color: var(--text-2); }
    .su-row b { color: var(--text-0); font-weight: 500; font-family: var(--font-mono); font-size: var(--fs-xs); }
    .su-row b code { color: var(--accent); }
    .su-err {
      margin-top: var(--s-3);
      padding: 10px 12px;
      color: #fca5a5;
      background: var(--danger-soft);
      border-radius: var(--r-2);
      font-family: var(--font-mono); font-size: var(--fs-sm);
      word-break: break-word;
    }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="su-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
