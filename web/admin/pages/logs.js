// pages/logs.js — live log tail with level/category/search filters + export.
//
// Backed by:
//   GET  /api/logs?level=&category=&search=&limit=&offset=    initial fetch
//   SSE  /api/logs/stream?level=&category=&search=            live tail
//   GET  /api/logs/export?format=json|txt&...                 download
//
// We keep the last LOG_BUFFER_MAX entries in-memory; new SSE events prepend
// (newest first). The 'paused' toggle stops auto-scroll while the user
// reads — re-enabled on filter change or when scrolled back to top.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

const LOG_BUFFER_MAX = 1000;

let state = {
  entries: [],
  total: 0,
  categories: [],
  filter: {
    level: '',
    category: '',
    search: '',
  },
  paused: false,
  stream: null,
};

async function loadInitial() {
  try {
    const q = new URLSearchParams();
    if (state.filter.level)    q.set('level', state.filter.level);
    if (state.filter.category) q.set('category', state.filter.category);
    if (state.filter.search)   q.set('search', state.filter.search);
    q.set('limit', '300');
    const data = await api.get('/logs?' + q.toString());
    state.entries = data.entries || [];
    state.total = data.total || 0;
    state.categories = data.categories || [];
  } catch (e) {
    toast.error('Не удалось загрузить логи: ' + e.message);
  }
}

function startStream() {
  stopStream();
  const query = {};
  if (state.filter.level)    query.level    = state.filter.level;
  if (state.filter.category) query.category = state.filter.category;
  if (state.filter.search)   query.search   = state.filter.search;

  state.stream = api.sse('/logs/stream', { query });
  state.stream.on('message', (entry) => {
    if (state.paused) return;
    state.entries.unshift(entry);
    if (state.entries.length > LOG_BUFFER_MAX) state.entries.length = LOG_BUFFER_MAX;
    state.total++;
    paint();
  });
}

function stopStream() {
  if (state.stream) {
    state.stream.close();
    state.stream = null;
  }
}

function applyFilters() {
  loadInitial().then(() => {
    paint();
    startStream();
  });
}

function exportLogs(format) {
  const q = new URLSearchParams();
  if (state.filter.level)    q.set('level', state.filter.level);
  if (state.filter.category) q.set('category', state.filter.category);
  if (state.filter.search)   q.set('search', state.filter.search);
  q.set('format', format);
  // The /export endpoint serves with Content-Disposition; let the browser
  // handle the download via a new tab.
  window.open(window.location.pathname.split('/v2/')[0] + '/api/logs/export?' + q.toString(), '_blank');
}

function levelTone(lvl) {
  switch ((lvl || '').toLowerCase()) {
    case 'error':
    case 'fatal': return 'danger';
    case 'warn':
    case 'warning': return 'warn';
    case 'info':  return 'info';
    case 'debug': return 'muted';
    default:      return 'muted';
  }
}

function paint() {
  const $root = document.getElementById('logs-root');
  if (!$root) return;
  render(html`
    <div class="page-summary">
      <l-stat accent="blue"  label="Всего в буфере" value=${state.total}></l-stat>
      <l-stat accent="green" label="Сейчас на экране" value=${state.entries.length}></l-stat>
      <l-stat accent="purple" label="Категории" value=${state.categories.length}></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="actions" class="logs-bar">
        <l-select
          size="sm"
          placeholder="Все уровни"
          .value=${state.filter.level}
          .options=${[
            { value: 'error', label: 'Error' },
            { value: 'warn',  label: 'Warn' },
            { value: 'info',  label: 'Info' },
            { value: 'debug', label: 'Debug' },
            { value: 'trace', label: 'Trace' },
          ]}
          @change=${e => { state.filter.level = e.detail.value; applyFilters(); }}
          style="width:120px"
        ></l-select>
        <l-select
          size="sm"
          placeholder="Все категории"
          .value=${state.filter.category}
          .options=${state.categories.map(c => ({ value: c, label: c }))}
          @change=${e => { state.filter.category = e.detail.value; applyFilters(); }}
          style="width:160px"
        ></l-select>
        <l-input
          size="sm"
          icon="🔎"
          placeholder="Поиск по тексту…"
          .value=${state.filter.search}
          clearable
          @input=${e => { state.filter.search = e.detail.value; debouncedApply(); }}
          style="width:240px"
        ></l-input>
        <l-pill tone=${state.paused ? 'warn' : 'ok'} dot>${state.paused ? 'Пауза' : 'Live'}</l-pill>
        <l-button size="sm" variant=${state.paused ? 'primary' : 'ghost'} @click=${() => { state.paused = !state.paused; paint(); }}>${state.paused ? '▶ Возобновить' : '⏸ Пауза'}</l-button>
        <l-button size="sm" variant="ghost" icon="📥" @click=${() => exportLogs('txt')}>TXT</l-button>
        <l-button size="sm" variant="ghost" icon="📥" @click=${() => exportLogs('json')}>JSON</l-button>
      </div>
      <div class="logs-list">
        ${state.entries.length === 0
          ? html`<div class="empty">Нет записей в буфере</div>`
          : state.entries.map(e => entryRow(e))}
      </div>
    </l-card>
  `, $root);
}

function entryRow(e) {
  return html`
    <div class="row r-${(e.level || 'info').toLowerCase()}">
      <span class="r-time">${formatTime(e.time)}</span>
      <l-pill tone=${levelTone(e.level)}>${(e.level || '').toUpperCase()}</l-pill>
      ${e.category ? html`<span class="r-cat">${e.category}</span>` : ''}
      <span class="r-msg">${e.message}</span>
      ${e.extra && Object.keys(e.extra).length ? html`
        <span class="r-extra">
          ${Object.entries(e.extra).map(([k, v]) => html`<code>${k}=${v}</code>`)}
        </span>` : ''}
    </div>
  `;
}

function formatTime(t) {
  if (!t) return '';
  // Backend sends RFC3339; strip the date for compactness, keep "HH:MM:SS.mmm".
  const m = String(t).match(/T(\d{2}:\d{2}:\d{2}(?:\.\d{3})?)/);
  return m ? m[1] : t;
}

// Debounce the search input so SSE isn't restarted on every keystroke.
let searchTimer = null;
function debouncedApply() {
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(applyFilters, 280);
  paint(); // immediate UI update for the input value
}

// Page-scoped styles.
const styleId = 'l-logs-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr; } }
    .logs-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
    .logs-list {
      font-family: var(--font-mono);
      font-size: var(--fs-xs);
      max-height: calc(100vh - 360px);
      overflow-y: auto;
      border-top: 1px solid var(--border-1);
      margin: 0 calc(-1 * var(--s-4)) calc(-1 * var(--s-4));
    }
    .logs-list .row {
      display: grid;
      grid-template-columns: 96px 70px auto 1fr auto;
      gap: 10px;
      align-items: baseline;
      padding: 6px var(--s-4);
      border-bottom: 1px solid var(--border-1);
      line-height: 1.4;
      transition: background var(--t-fast);
    }
    .logs-list .row:hover { background: var(--bg-2); }
    .r-time { color: var(--text-3); font-variant-numeric: tabular-nums; white-space: nowrap; }
    .r-cat { color: var(--text-2); font-weight: 600; }
    .r-msg { color: var(--text-0); word-break: break-word; }
    .r-extra { display: inline-flex; gap: 6px; flex-wrap: wrap; }
    .r-extra code { background: var(--bg-3); color: var(--text-1); padding: 1px 6px; border-radius: 4px; }
    .row.r-error { background: rgba(248,113,113,0.04); }
    .row.r-warn  { background: rgba(251,191,36,0.03); }
    .row.r-error .r-msg { color: #fca5a5; }
    .empty { padding: 24px; color: var(--text-3); text-align: center; }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="logs-root"></div>`;
  paint();
  await loadInitial();
  paint();
  startStream();

  // Stop the SSE when the page is unmounted (router will set new content).
  // We rely on the mount node disconnecting; a MutationObserver on the parent
  // detects the swap.
  const guard = new MutationObserver(() => {
    if (!document.body.contains($mount)) {
      stopStream();
      guard.disconnect();
    }
  });
  guard.observe($mount.parentNode, { childList: true });
}
export { render_ as render };
