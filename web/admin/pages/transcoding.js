// pages/transcoding.js — live ffmpeg job queue.
//
// API:
//   GET  /api/media/jobs             → {enabled, jobs[], now}
//   POST /api/media/jobs/kill?id=…   → {ok}
//
// 3-second poll while page visible (transcoding service has no SSE yet — adding
// one would require manager.notifySubscribers wiring). Jobs are usually long
// enough that polling is fine UX-wise.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  enabled: true,
  jobs: [],
  loading: true,
  filter: '',
};
let timer = null;

async function load() {
  try {
    const d = await api.get('/media/jobs');
    state.enabled = d && d.enabled;
    state.jobs = (d && d.jobs) || [];
  } catch (e) {
    if (state.loading) toast.error('Не удалось получить задачи: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function killJob(id) {
  if (!confirm(`Остановить задачу ${id}?`)) return;
  try {
    const r = await api.post(`/media/jobs/kill?id=${encodeURIComponent(id)}`);
    if (r && r.ok === false) throw new Error('not killable');
    toast.success('Остановлена');
    await load();
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

function filtered() {
  const f = state.filter.trim().toLowerCase();
  if (!f) return state.jobs;
  return state.jobs.filter(j =>
    (j.id || '').toLowerCase().includes(f)
    || (j.stream_id || '').toLowerCase().includes(f)
    || (j.source || '').toLowerCase().includes(f)
    || (j.original_url || '').toLowerCase().includes(f)
  );
}

function stateTone(s) {
  switch ((s || '').toLowerCase()) {
    case 'running':  return 'ok';
    case 'stopped':  return 'muted';
    case 'failed':   return 'danger';
    case 'starting': return 'info';
    case 'idle':     return 'muted';
    default:         return 'muted';
  }
}

function paint() {
  const $root = document.getElementById('trans-root');
  if (!$root) return;
  const jobs = filtered();
  const running = state.jobs.filter(j => j.state === 'running').length;
  const failed  = state.jobs.filter(j => j.state === 'failed').length;
  const stopped = state.jobs.filter(j => j.state === 'stopped').length;

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"  label="Всего"   value=${state.jobs.length}></l-stat>
      <l-stat accent="green" label="Активные" value=${running} icon="▶"></l-stat>
      <l-stat accent="pink"  label="Упали"   value=${failed}  icon="✗"></l-stat>
      <l-stat accent="amber" label="Stopped" value=${stopped} icon="⏸"></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">ffmpeg задачи</span></div>
      <div slot="actions" style="display:flex;gap:8px;">
        <l-input
          size="sm" icon="🔎"
          placeholder="ID / source / URL"
          .value=${state.filter}
          clearable
          @input=${e => { state.filter = e.detail.value; paint(); }}
          style="width:240px"
        ></l-input>
        <l-button variant="secondary" icon="↻" @click=${async () => { await load(); paint(); }}>Обновить</l-button>
      </div>

      ${!state.enabled
        ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">
            Сервис транскодинга отключён. Включите <code>[transcoding] enable = true</code>.
          </div>`
        : jobs.length === 0
          ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">${state.filter ? 'Ничего не найдено' : 'Сейчас нет активных задач'}</div>`
          : html`
            <l-table
              .columns=${[
                { key: 'id', label: 'ID', cell: r => html`<code style="font-size:11px;color:var(--text-2)">${r.id.slice(0, 12)}…</code>` },
                { key: 'state', label: 'State', cell: r => html`<l-pill tone=${stateTone(r.state)} dot=${r.state === 'running'}>${r.state || '?'}</l-pill>` },
                { key: 'mode', label: 'Mode', cell: r => html`<span style="font-family:var(--font-mono);font-size:var(--fs-xs)">${r.mode || '—'}</span>` },
                { key: 'uptime_s', label: 'Uptime', align: 'r', cell: r => fmtSec(r.uptime_s) },
                { key: 'last_access_s', label: 'Last access', align: 'r', cell: r => fmtSec(r.last_access_s) + ' назад' },
                { key: 'last_segment', label: 'Seg', align: 'r', format: 'int' },
                { key: 'source', label: 'Источник', cell: r => sourceCell(r) },
                { key: '_act', label: '', align: 'r', cell: r => html`<l-button size="sm" variant="danger" @click=${() => killJob(r.id)}>×</l-button>` },
              ]}
              .rows=${jobs}
              empty="Нет задач"
            ></l-table>`}
    </l-card>

    ${state.jobs.filter(j => j.warning).length ? html`
      <l-card title="Предупреждения" style="margin-top:var(--s-5)">
        ${state.jobs.filter(j => j.warning).map(j => html`
          <div class="tr-warn">
            <l-pill tone="warn">${j.id.slice(0, 12)}</l-pill>
            <span>${j.warning}</span>
          </div>
        `)}
      </l-card>` : ''}
  `, $root);
}

function sourceCell(r) {
  if (r.source) return html`<span style="font-family:var(--font-mono);font-size:var(--fs-xs);color:var(--text-2)">${r.source}</span>`;
  if (r.original_url) return html`<span style="font-size:11px;color:var(--text-3);overflow:hidden;text-overflow:ellipsis;max-width:220px;display:inline-block;white-space:nowrap;vertical-align:bottom">${r.original_url}</span>`;
  return html`<span style="color:var(--text-3)">—</span>`;
}

function fmtSec(n) {
  if (!n || n < 0) return '—';
  if (n < 60)   return n + 'с';
  if (n < 3600) return Math.floor(n / 60) + 'м ' + Math.floor(n % 60) + 'с';
  return Math.floor(n / 3600) + 'ч ' + Math.floor((n % 3600) / 60) + 'м';
}

const styleId = 'l-trans-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }
    .tr-warn {
      display: flex; align-items: center; gap: 10px;
      padding: 8px 12px; margin-bottom: 6px;
      background: var(--warn-soft);
      border: 1px solid rgba(251,191,36,0.18);
      border-radius: var(--r-2);
      font-size: var(--fs-sm); color: var(--text-1);
    }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="trans-root"></div>`;
  paint();
  await load();
  paint();
  if (timer) clearInterval(timer);
  timer = setInterval(() => {
    if (document.visibilityState === 'visible') {
      load().then(() => paint());
    }
  }, 3000);

  const guard = new MutationObserver(() => {
    if (!document.body.contains($mount)) {
      if (timer) clearInterval(timer);
      timer = null;
      guard.disconnect();
    }
  });
  if ($mount.parentNode) guard.observe($mount.parentNode, { childList: true });
}
export { render_ as render };
