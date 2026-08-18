// pages/feedback.js — user feedback ticket queue.
//
// API:
//   GET  /api/feedback?status=&category=&priority=&search=  → {tickets[], total}
//   GET  /api/feedback/stats                                → counts by status
//   POST /api/feedback  action=set_status   {ticket_id, status}
//   POST /api/feedback  action=set_priority {ticket_id, priority}
//   POST /api/feedback  action=reply        {ticket_id, message}  // notifies user via TG
//   POST /api/feedback  action=delete       {ticket_id}           // super only

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

const STATUS_OPTS = [
  { value: '',          label: 'Все' },
  { value: 'open',      label: 'Открытые' },
  { value: 'pending',   label: 'В работе' },
  { value: 'resolved',  label: 'Решены' },
  { value: 'closed',    label: 'Закрыты' },
];
const PRIORITY_OPTS = [
  { value: 'low',    label: 'low' },
  { value: 'normal', label: 'normal' },
  { value: 'high',   label: 'high' },
  { value: 'urgent', label: 'urgent' },
];

let state = {
  tickets: [],
  loading: true,
  status: 'open',
  search: '',
  selected: null,    // selected ticket ID (right pane)
  selectedData: null,
  reply: '',
};

async function load() {
  state.loading = true;
  try {
    const q = new URLSearchParams();
    if (state.status) q.set('status', state.status);
    if (state.search) q.set('search', state.search);
    const d = await api.get('/feedback?' + q.toString());
    state.tickets = (d && d.tickets) || [];
    // Preserve selection if still in the list.
    if (state.selected) {
      state.selectedData = state.tickets.find(t => t.id === state.selected) || null;
      if (!state.selectedData) state.selected = null;
    }
  } catch (e) {
    toast.error('Не удалось загрузить тикеты: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function setStatus(ticket_id, status) {
  try {
    const r = await api.post('/feedback', { action: 'set_status', ticket_id, status });
    if (r && r.error) throw new Error(r.error);
    toast.success('Статус: ' + status);
    await load();
    paint();
  } catch (e) { toast.error(e.message); }
}
async function setPriority(ticket_id, priority) {
  try {
    const r = await api.post('/feedback', { action: 'set_priority', ticket_id, priority });
    if (r && r.error) throw new Error(r.error);
    toast.success('Приоритет: ' + priority);
    await load();
    paint();
  } catch (e) { toast.error(e.message); }
}
async function sendReply() {
  const message = state.reply.trim();
  if (!message) return toast.warn('Введите ответ');
  if (!state.selected) return;
  try {
    const r = await api.post('/feedback', { action: 'reply', ticket_id: state.selected, message });
    if (r && r.error) throw new Error(r.error);
    toast.success('Ответ отправлен пользователю');
    state.reply = '';
    await load();
    paint();
  } catch (e) { toast.error(e.message); }
}
async function deleteTicket(ticket_id) {
  if (!confirm('Удалить тикет окончательно?')) return;
  try {
    const r = await api.post('/feedback', { action: 'delete', ticket_id });
    if (r && r.error) throw new Error(r.error);
    toast.success('Удалён');
    if (state.selected === ticket_id) {
      state.selected = null; state.selectedData = null;
    }
    await load();
    paint();
  } catch (e) { toast.error(e.message); }
}

function statusTone(s) {
  switch (s) {
    case 'open':     return 'danger';
    case 'pending':  return 'warn';
    case 'resolved': return 'ok';
    case 'closed':   return 'muted';
    default:         return 'muted';
  }
}
function priorityTone(p) {
  switch (p) {
    case 'urgent': return 'danger';
    case 'high':   return 'warn';
    case 'normal': return 'info';
    case 'low':    return 'muted';
    default:       return 'muted';
  }
}

function paint() {
  const $root = document.getElementById('fb-root');
  if (!$root) return;
  const counts = state.tickets.reduce((a, t) => { a[t.status] = (a[t.status] || 0) + 1; return a; }, {});

  render(html`
    <div class="page-summary">
      <l-stat accent="pink"   label="Открытые"  value=${counts.open    || 0}></l-stat>
      <l-stat accent="amber"  label="В работе"  value=${counts.pending || 0}></l-stat>
      <l-stat accent="green"  label="Решены"    value=${counts.resolved|| 0}></l-stat>
      <l-stat accent="purple" label="Закрыты"   value=${counts.closed  || 0}></l-stat>
    </div>

    <div class="fb-layout">
      <l-card class="fb-list-card">
        <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">Тикеты</span></div>
        <div slot="actions" style="display:flex;gap:6px;flex-wrap:wrap;">
          <l-select
            size="sm"
            .value=${state.status}
            .options=${STATUS_OPTS}
            @change=${e => { state.status = e.detail.value; load().then(paint); }}
            style="width:140px"
          ></l-select>
          <l-input
            size="sm" icon="🔎"
            placeholder="Поиск"
            .value=${state.search}
            clearable
            @input=${e => { state.search = e.detail.value; debouncedLoad(); }}
            style="width:200px"
          ></l-input>
        </div>
        ${state.loading
          ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
          : state.tickets.length === 0
            ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">Нет тикетов</div>`
            : html`
              <div class="fb-list">
                ${state.tickets.map(t => ticketRow(t))}
              </div>`}
      </l-card>

      <l-card class="fb-detail-card">
        ${state.selectedData ? ticketDetail(state.selectedData) : html`
          <div style="padding:64px;text-align:center;color:var(--text-3)">Выберите тикет слева</div>
        `}
      </l-card>
    </div>
  `, $root);
}

function ticketRow(t) {
  const isSelected = t.id === state.selected;
  return html`
    <button class="fb-row ${isSelected ? 'sel' : ''}" @click=${() => { state.selected = t.id; state.selectedData = t; state.reply = ''; paint(); }}>
      <div class="fb-row-top">
        <span class="fb-row-subj">${t.subject || '(без темы)'}</span>
        <l-pill tone=${statusTone(t.status)}>${t.status}</l-pill>
      </div>
      <div class="fb-row-meta">
        <span>#${t.user_id}${t.user_name ? ' · ' + t.user_name : ''}</span>
        <l-pill tone=${priorityTone(t.priority)}>${t.priority}</l-pill>
        <l-pill tone="muted">${t.category}</l-pill>
      </div>
      <div class="fb-row-msg">${(t.message || '').slice(0, 100)}${(t.message || '').length > 100 ? '…' : ''}</div>
      <div class="fb-row-time">${t.created_at || ''}${(t.replies || []).length ? ' · ' + (t.replies || []).length + ' ответов' : ''}</div>
    </button>
  `;
}

function ticketDetail(t) {
  return html`
    <div slot="title">
      <span style="font-weight:600;font-size:var(--fs-md)">${t.subject || '(без темы)'}</span>
    </div>
    <div slot="actions" style="display:flex;gap:6px;flex-wrap:wrap;">
      <l-select
        size="sm"
        .value=${t.status}
        .options=${STATUS_OPTS.filter(o => o.value)}
        @change=${e => setStatus(t.id, e.detail.value)}
        style="width:130px"
      ></l-select>
      <l-select
        size="sm"
        .value=${t.priority}
        .options=${PRIORITY_OPTS}
        @change=${e => setPriority(t.id, e.detail.value)}
        style="width:120px"
      ></l-select>
      <l-button size="sm" variant="danger" @click=${() => deleteTicket(t.id)}>×</l-button>
    </div>

    <div class="fb-detail-meta">
      <l-pill tone=${statusTone(t.status)} dot>${t.status}</l-pill>
      <l-pill tone=${priorityTone(t.priority)}>${t.priority}</l-pill>
      <l-pill tone="muted">${t.category}</l-pill>
      <span class="fb-detail-id">#${t.user_id}${t.user_name ? ' · ' + t.user_name : ''}</span>
      <span class="fb-detail-time">${t.created_at}</span>
    </div>

    <div class="fb-thread">
      <div class="fb-msg user">
        <div class="fb-msg-h">Пользователь · ${t.created_at}</div>
        <div class="fb-msg-body">${t.message}</div>
      </div>
      ${(t.replies || []).map(r => html`
        <div class="fb-msg ${r.is_admin ? 'admin' : 'user'}">
          <div class="fb-msg-h">${r.is_admin ? '👑 ' + (r.admin_name || 'admin') : '👤 user'} · ${r.created_at}</div>
          <div class="fb-msg-body">${r.message}</div>
        </div>
      `)}
    </div>

    <div class="fb-reply">
      <textarea
        placeholder="Ваш ответ (отправится пользователю через TG)"
        .value=${state.reply}
        @input=${e => { state.reply = e.target.value; }}
        rows="3"
      ></textarea>
      <l-button variant="primary" icon="📤" @click=${sendReply} ?disabled=${!state.reply.trim()}>Ответить</l-button>
    </div>
  `;
}

// Debounced search reload.
let searchTimer = null;
function debouncedLoad() {
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(() => load().then(paint), 280);
}

const styleId = 'l-fb-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }

    .fb-layout {
      display: grid; grid-template-columns: 380px 1fr; gap: var(--s-4);
      margin-top: var(--s-5);
      min-height: 540px;
    }
    @media (max-width: 1100px) { .fb-layout { grid-template-columns: 1fr; } }
    .fb-list-card, .fb-detail-card { min-width: 0; }

    .fb-list {
      max-height: 60vh; overflow-y: auto;
      display: flex; flex-direction: column; gap: 4px;
      margin: 0 calc(-1 * var(--s-4)) calc(-1 * var(--s-4));
      padding: 0 var(--s-4) var(--s-4);
    }
    .fb-row {
      text-align: left; background: var(--bg-2);
      border: 1px solid var(--border-1); border-radius: var(--r-2);
      padding: var(--s-2) var(--s-3);
      cursor: pointer; color: var(--text-1);
      transition: background var(--t-fast), border-color var(--t-fast);
      display: flex; flex-direction: column; gap: 4px;
    }
    .fb-row:hover { background: var(--bg-3); }
    .fb-row.sel { border-color: var(--accent); background: var(--accent-soft); }
    .fb-row-top { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
    .fb-row-subj { font-weight: 600; font-size: var(--fs-sm); color: var(--text-0); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .fb-row-meta { display: flex; align-items: center; gap: 6px; font-size: var(--fs-xs); color: var(--text-2); flex-wrap: wrap; }
    .fb-row-msg {
      font-size: var(--fs-xs); color: var(--text-2);
      overflow: hidden; text-overflow: ellipsis;
      display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
    }
    .fb-row-time { font-size: 10px; color: var(--text-3); }

    .fb-detail-meta { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; padding: var(--s-2) 0; }
    .fb-detail-id { font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-2); }
    .fb-detail-time { font-size: var(--fs-xs); color: var(--text-3); margin-left: auto; }

    .fb-thread {
      display: flex; flex-direction: column; gap: 10px;
      margin-top: var(--s-3);
      max-height: 50vh; overflow-y: auto;
      padding: 4px;
    }
    .fb-msg {
      padding: 10px 12px; border-radius: var(--r-3);
      max-width: 80%; line-height: 1.5;
    }
    .fb-msg.user  { align-self: flex-start; background: var(--bg-3); border: 1px solid var(--border-1); }
    .fb-msg.admin { align-self: flex-end;   background: var(--accent-soft); border: 1px solid var(--accent); }
    .fb-msg-h { font-size: 10px; color: var(--text-3); margin-bottom: 4px; }
    .fb-msg-body { font-size: var(--fs-sm); color: var(--text-0); white-space: pre-wrap; word-break: break-word; }

    .fb-reply {
      margin-top: var(--s-3); padding-top: var(--s-3);
      border-top: 1px solid var(--border-1);
      display: flex; gap: 8px; align-items: flex-end;
    }
    .fb-reply textarea {
      flex: 1; min-height: 60px; resize: vertical;
      padding: 10px 12px;
      background: var(--bg-0);
      border: 1px solid var(--border-2);
      border-radius: var(--r-2);
      color: var(--text-0);
      font-family: var(--font-text); font-size: var(--fs-sm);
      line-height: 1.5;
    }
    .fb-reply textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="fb-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
