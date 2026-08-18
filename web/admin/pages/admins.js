// pages/admins.js — manage the AdminIDStore (TG users who can open the admin panel).
//
// API:
//   GET  /api/admins                                          → AdminEntry[]
//   POST /api/admins  {action: 'add', telegram_id, note?}     → {ok}
//   POST /api/admins  {action: 'remove', telegram_id}         → {ok}
//
// Super-admin only on the backend.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  entries: [],
  loading: true,
  whoami: null,
  newID: '',
  newNote: '',
  error: null,
};

async function load() {
  state.loading = true;
  state.error = null;
  try {
    const list = await api.get('/admins');
    state.entries = Array.isArray(list) ? list : (list.entries || []);
  } catch (e) {
    state.error = e.message;
    toast.error('Не удалось загрузить админов: ' + e.message);
  } finally {
    state.loading = false;
  }
  try {
    state.whoami = await api.whoami();
  } catch {}
}

async function addAdmin() {
  const id = parseInt(state.newID, 10);
  if (!Number.isFinite(id) || id <= 0) return toast.warn('Telegram ID должен быть положительным числом');
  try {
    const r = await api.post('/admins', {
      action: 'add',
      telegram_id: id,
      note: state.newNote.trim(),
    });
    if (r && r.error) throw new Error(r.error);
    toast.success('Админ добавлен');
    state.newID = '';
    state.newNote = '';
    await load();
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

async function removeAdmin(entry) {
  // Защита от удаления super-admin'а
  if (entry.role === 'super') return toast.warn('Super admin удаляется через config.toml');
  if (state.whoami && entry.telegram_id === state.whoami.telegram_id) {
    return toast.warn('Нельзя удалить самого себя — попросите другого super-admin');
  }
  if (!confirm(`Удалить админа #${entry.telegram_id}${entry.note ? ' (' + entry.note + ')' : ''}?`)) return;
  try {
    const r = await api.post('/admins', { action: 'remove', telegram_id: entry.telegram_id });
    if (r && r.error) throw new Error(r.error);
    toast.success('Удалён');
    await load();
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

function paint() {
  const $root = document.getElementById('admins-root');
  if (!$root) return;
  const list = state.entries;
  const supers = list.filter(e => e.role === 'super').length;
  const regular = list.length - supers;
  const me = state.whoami && state.whoami.telegram_id;

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Всего"        value=${list.length}></l-stat>
      <l-stat accent="purple" label="Super admins" value=${supers}></l-stat>
      <l-stat accent="green"  label="Admins"       value=${regular}></l-stat>
    </div>

    <l-card title="Добавить админа" style="margin-top:var(--s-5)">
      <div class="ad-form">
        <l-input
          icon="🆔"
          placeholder="Telegram ID (число)"
          .value=${state.newID}
          @input=${e => { state.newID = e.detail.value; }}
          style="width:200px"
        ></l-input>
        <l-input
          icon="✏"
          placeholder="Заметка (для кого / роль)"
          .value=${state.newNote}
          @input=${e => { state.newNote = e.detail.value; }}
          style="flex:1;min-width:180px"
        ></l-input>
        <l-button variant="primary" icon="➕" @click=${addAdmin}>Добавить</l-button>
      </div>
      <div class="ad-hint">
        ID находится в @userinfobot или /myid. После добавления админ может открыть админ-панель
        как только пройдёт <code>/tg/auth</code> в браузере.
      </div>
    </l-card>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">Список админов</span></div>
      <div slot="actions">
        <l-button variant="secondary" icon="↻" @click=${async () => { await load(); paint(); }}>Обновить</l-button>
      </div>
      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
        : state.error
          ? html`<div class="ad-error">
              <div style="font-weight:600;margin-bottom:4px">Не удалось загрузить список</div>
              <div style="font-family:var(--font-mono);font-size:12px">${state.error}</div>
              <l-button size="sm" variant="secondary" style="margin-top:10px" @click=${async () => { await load(); paint(); }}>Повторить</l-button>
            </div>`
          : list.length === 0
            ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">Список пуст</div>`
            : html`
              <div class="ad-grid">
                ${list.map(e => adminCard(e, me))}
              </div>`}
    </l-card>
  `, $root);
}

function adminCard(e, me) {
  const isMe    = me && me === e.telegram_id;
  const isSuper = e.role === 'super';
  return html`
    <div class="ad-card ${isSuper ? 'super' : ''} ${isMe ? 'me' : ''}">
      <div class="ad-avatar">${isSuper ? '👑' : '🛡'}</div>
      <div class="ad-meta">
        <div class="ad-id">#${e.telegram_id}${isMe ? ' (это вы)' : ''}</div>
        <div class="ad-role-row">
          <l-pill tone=${isSuper ? 'warn' : 'info'}>${isSuper ? 'super' : 'admin'}</l-pill>
          ${e.note ? html`<span class="ad-note">${e.note}</span>` : ''}
        </div>
        ${e.added_at ? html`<div class="ad-when">Добавлен ${formatDate(e.added_at)}${e.added_by ? ' · от #' + e.added_by : ''}</div>` : ''}
      </div>
      <l-button
        size="sm"
        variant=${isSuper || isMe ? 'ghost' : 'danger'}
        ?disabled=${isSuper || isMe}
        @click=${() => removeAdmin(e)}
        title=${isSuper ? 'Super admin меняется через config.toml' : (isMe ? 'Нельзя удалить себя' : 'Удалить')}
      >×</l-button>
    </div>
  `;
}

function formatDate(t) {
  if (!t) return '';
  try { return new Date(t).toLocaleString('ru-RU'); } catch { return String(t); }
}

const styleId = 'l-admins-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr; } }
    .ad-form { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
    .ad-hint {
      margin-top: var(--s-3); padding: 10px 12px;
      background: var(--info-soft);
      border: 1px solid rgba(96,165,250,0.18);
      border-radius: var(--r-2);
      font-size: var(--fs-sm); color: var(--text-1);
    }
    .ad-grid {
      display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
      gap: var(--s-3);
    }
    .ad-card {
      display: flex; align-items: center; gap: 12px;
      padding: var(--s-3);
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      transition: border-color var(--t-fast);
    }
    .ad-card:hover { border-color: var(--border-3); }
    .ad-card.super { border-color: rgba(251,191,36,0.25); }
    .ad-card.super::before {
      content: ''; display: block; width: 3px;
      align-self: stretch;
      background: linear-gradient(180deg, #fbbf24, #fb923c);
      border-radius: var(--r-pill);
      margin-right: -2px;
    }
    .ad-card.me { box-shadow: 0 0 0 1px var(--accent-soft) inset; }
    .ad-avatar {
      width: 40px; height: 40px; border-radius: 50%;
      background: linear-gradient(135deg, var(--accent), var(--accent-2));
      display: grid; place-items: center; font-size: 20px;
      flex-shrink: 0;
    }
    .ad-card.super .ad-avatar {
      background: linear-gradient(135deg, #fbbf24, #fb923c);
    }
    .ad-meta { flex: 1; min-width: 0; }
    .ad-id {
      font-family: var(--font-mono);
      font-weight: 600; font-size: var(--fs-sm);
      color: var(--text-0);
    }
    .ad-role-row { display: flex; align-items: center; gap: 8px; margin-top: 4px; flex-wrap: wrap; }
    .ad-note { font-size: var(--fs-xs); color: var(--text-2); }
    .ad-when { font-size: 10px; color: var(--text-3); margin-top: 2px; }
    .ad-error {
      padding: var(--s-4);
      background: var(--danger-soft);
      border: 1px solid rgba(255,107,122,0.28);
      border-radius: var(--r-2);
      color: var(--text-1);
      text-align: center;
    }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="admins-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
