// pages/telemetry.js — premium card grid of balancer telemetry.
// Live via SSE (/api/telemetry/stream) with a one-shot REST fetch as the
// initial paint and a polling fallback if SSE drops.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let stream = null;
let pollTimer = null;
let lastTick = 0;

function pillTone(status) {
  switch (status) {
    case 'healthy':  return 'ok';
    case 'degraded': return 'warn';
    case 'down':     return 'danger';
    case 'unknown':
    default:         return 'muted';
  }
}

function pillText(status) {
  switch (status) {
    case 'healthy':  return 'Здоров';
    case 'degraded': return 'Деградирует';
    case 'down':     return 'Недоступен';
    case 'unknown':
    default:         return 'Без данных';
  }
}

async function load($mount) {
  let data;
  try {
    data = await api.telemetry();
  } catch (e) {
    render(html`<l-card><div style="color:var(--danger)">Telemetry unavailable: ${e.message}</div></l-card>`, $mount);
    return;
  }
  if (!data || !data.enabled) {
    render(html`
      <l-card title="Телеметрия отключена">
        <p>Включите в config.toml <code>[telemetry] enabled = true</code>, чтобы видеть метрики.</p>
      </l-card>
    `, $mount);
    return;
  }

  const entries = data.entries || [];
  const counts = entries.reduce((acc, e) => { acc[e.status] = (acc[e.status] || 0) + 1; return acc; }, {});

  const onReset = async (name) => {
    try {
      await api.post('/telemetry', { action: 'reset', balancer: name });
      toast.success('Статистика сброшена');
      load($mount);
    } catch (e) {
      toast.error(e.message);
    }
  };
  const onResetBreaker = async (name) => {
    try {
      await api.post('/telemetry', { action: 'reset_breaker', balancer: name });
      toast.success('Breaker сброшен');
      load($mount);
    } catch (e) {
      toast.error(e.message);
    }
  };

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Всего"      value=${entries.length}></l-stat>
      <l-stat accent="green"  label="Здоровые"   value=${counts.healthy || 0}></l-stat>
      <l-stat accent="amber"  label="Деградируют" value=${counts.degraded || 0}></l-stat>
      <l-stat accent="pink"   label="Недоступны" value=${counts.down || 0}></l-stat>
    </div>

    <div class="grid cols-3" style="margin-top:var(--s-5)">
      ${entries.map(e => renderCard(e, onReset, onResetBreaker))}
    </div>
  `, $mount);
}

function renderCard(e, onReset, onResetBreaker) {
  const snap = e.snapshot || {};
  const w = (snap.windows || {})['5m'] || {};
  const cs = (e.checksearch && (e.checksearch.windows || {})['5m']) || null;
  const sr = w.total > 0 ? (w.success_rate * 100).toFixed(0) + '%' : '—';
  const lastErr = (snap.recent_errors && snap.recent_errors[0]) || null;
  return html`
    <l-card hover>
      <div slot="title" style="display:flex;align-items:center;gap:8px;">
        <span style="font-weight:600;font-size:var(--fs-md)">${e.name}</span>
        ${e.quality ? html`<span class="quality q-${e.quality.toLowerCase()}">${e.quality}</span>` : ''}
        <l-pill tone=${pillTone(e.status)} ?dot=${e.status === 'down' || e.status === 'degraded'}>${pillText(e.status)}</l-pill>
      </div>
      <div slot="actions">
        ${e.breaker_open ? html`<l-button size="sm" variant="danger" @click=${() => onResetBreaker(e.name)}>♻ Breaker</l-button>` : ''}
        <l-button size="sm" variant="ghost" @click=${() => onReset(e.name)}>♻</l-button>
      </div>
      <div class="metrics">
        <div class="metric"><div class="m-l">Success</div><div class="m-v">${sr}</div></div>
        <div class="metric"><div class="m-l">Запросов</div><div class="m-v">${w.total || 0}</div></div>
        <div class="metric"><div class="m-l">P95</div><div class="m-v">${w.p95_latency_ms || 0}<small>ms</small></div></div>
        <div class="metric"><div class="m-l">P99</div><div class="m-v">${w.p99_latency_ms || 0}<small>ms</small></div></div>
      </div>
      ${cs ? html`
        <div class="cs-row" title="checksearch probes — отдельный кольцевой буфер">
          <span class="cs-l">🔍 checksearch:</span>
          <b>${cs.total > 0 ? (cs.success_rate * 100).toFixed(0) + '%' : '—'}</b>
          <span class="cs-dim">(${cs.success || 0}/${cs.total || 0}, p95 ${cs.p95_latency_ms || 0}ms)</span>
        </div>` : ''}
      ${snap.current_host ? html`<div class="host">📡 <code>${snap.current_host}</code></div>` : ''}
      ${lastErr ? html`<div class="err">[${lastErr.status || '—'}] ${lastErr.err || ''}</div>` : ''}
    </l-card>
  `;
}

// Quality badge styling — added to the global doc once.
const styleId = 'l-quality-badges';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .quality {
      display: inline-block; padding: 1px 6px; border-radius: 4px;
      font-size: 10px; font-weight: 700; letter-spacing: 0.04em;
      background: rgba(255,255,255,0.06); color: var(--text-1);
    }
    .quality.q-4k  { background: rgba(236,72,153,0.18); color: #f9a8d4; }
    .quality.q-fhd { background: rgba(96,165,250,0.18); color: #93c5fd; }
    .quality.q-hd  { background: rgba(52,211,153,0.18); color: #6ee7b7; }
    .quality.q-sd  { background: rgba(148,163,184,0.18); color: #cbd5e1; }
    .page-summary {
      display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4);
    }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }
    .metrics {
      display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-3);
      padding-top: var(--s-3);
    }
    .metric .m-l { font-size: 10px; text-transform: uppercase; color: var(--text-3); letter-spacing: 0.08em; }
    .metric .m-v { font-family: var(--font-display); font-size: var(--fs-lg); font-weight: 700; font-variant-numeric: tabular-nums; }
    .metric .m-v small { font-size: 0.6em; color: var(--text-3); margin-left: 2px; }
    .cs-row {
      margin-top: var(--s-3); padding: 6px 10px;
      background: rgba(96,165,250,0.08); border: 1px solid rgba(96,165,250,0.18);
      border-radius: var(--r-2); font-size: var(--fs-xs);
      display: flex; align-items: center; gap: 6px;
    }
    .cs-l { color: #60a5fa; font-weight: 600; }
    .cs-dim { color: var(--text-3); }
    .host { margin-top: var(--s-3); font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-2); }
    .host code { background: var(--bg-3); padding: 2px 6px; border-radius: 4px; color: var(--accent); }
    .err {
      margin-top: var(--s-2); padding: 6px 10px;
      background: var(--danger-soft); color: #fca5a5;
      border-radius: var(--r-2); font-size: var(--fs-xs); font-family: var(--font-mono);
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
  `;
  document.head.appendChild(s);
}

function startStream($mount) {
  if (stream) stream.close();
  stream = api.sse('/telemetry/stream');
  // The backend emits "event: snapshot" frames carrying the full entries
  // payload — same shape as GET /api/telemetry, so we can reuse `load()`
  // by stuffing the data into a synthetic response.
  stream.on('snapshot', (data) => {
    if (!data || !data.entries) return;
    lastTick = Date.now();
    // Render directly so we don't re-fetch.
    renderEntries($mount, data);
  });
  // Plain "message" events arrive on heartbeat; ignore.
  stream.on('message', () => { lastTick = Date.now(); });
}

function renderEntries($mount, data) {
  // Replays the same rendering path load() takes — just without the fetch.
  const entries = data.entries || [];
  const counts = entries.reduce((acc, e) => { acc[e.status] = (acc[e.status] || 0) + 1; return acc; }, {});

  const onReset = async (name) => {
    try { await api.post('/telemetry', { action: 'reset', balancer: name }); toast.success('Сброшено'); load($mount); }
    catch (e) { toast.error(e.message); }
  };
  const onResetBreaker = async (name) => {
    try { await api.post('/telemetry', { action: 'reset_breaker', balancer: name }); toast.success('Breaker сброшен'); load($mount); }
    catch (e) { toast.error(e.message); }
  };

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Всего"      value=${entries.length}></l-stat>
      <l-stat accent="green"  label="Здоровые"   value=${counts.healthy || 0}></l-stat>
      <l-stat accent="amber"  label="Деградируют" value=${counts.degraded || 0}></l-stat>
      <l-stat accent="pink"   label="Недоступны" value=${counts.down || 0}></l-stat>
    </div>
    <div class="grid cols-3" style="margin-top:var(--s-5)">
      ${entries.map(e => renderCard(e, onReset, onResetBreaker))}
    </div>
  `, $mount);
}

export async function render_($mount) {
  await load($mount);
  startStream($mount);

  // Polling fallback: if no SSE event for 12s, force a REST refresh.
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(() => {
    if (document.visibilityState !== 'visible') return;
    if (Date.now() - lastTick > 12000) load($mount);
  }, 6000);

  // Unmount cleanup.
  const guard = new MutationObserver(() => {
    if (!document.body.contains($mount)) {
      if (stream) stream.close();
      stream = null;
      if (pollTimer) clearInterval(pollTimer);
      pollTimer = null;
      guard.disconnect();
    }
  });
  guard.observe($mount.parentNode, { childList: true });
}
export { render_ as render };
