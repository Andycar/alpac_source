// pages/deps.js — external system dependencies (ffmpeg, yt-dlp, TorrServer,
// Chromium, pip packages, etc.) with version + latest checks and
// install/update buttons.
//
// API:
//   GET  /api/deps[?check_latest=1] → { groups: { youtube: [...], balancers,
//                                       torrents, media, system }, os, arch, go_ver }
//   POST /api/deps/update           → { binary } — triggers update
//   GET  /api/deps/update/status?binary=X → { status: idle|in_progress|done|error,
//                                             old_version, new_version, error }

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = null;
let updating = new Set();   // binaries currently updating in UI
let pollTimers = new Map(); // binary → timer

// Per-group accent colour. The server is the source of truth for id/icon/label,
// we only override the accent (the server doesn't have a "premium colour scheme"
// concept). Falls back to violet for unknown group ids.
const GROUP_ACCENT = {
  youtube:   'pink',
  balancers: 'violet',
  torrents:  'cyan',
  media:     'amber',
  system:    'mint',
};

// Flatten the depGroup[] structure returned by /api/deps into a single deps
// array — used for top-level counters (total/ok/missing/updatable).
function flattenGroups(groups) {
  if (!Array.isArray(groups)) return [];
  const out = [];
  for (const g of groups) {
    if (!g) continue;
    const deps = Array.isArray(g.deps) ? g.deps : [];
    for (const d of deps) out.push(d);
  }
  return out;
}

async function fetchState(checkLatest) {
  state = await api.deps(checkLatest);
  // Initialize updating set from server-side `updating` flag.
  flattenGroups(state && state.groups).forEach(d => {
    if (d && d.updating) updating.add(d.binary);
  });
}

function paint($mount) {
  if (!state) {
    render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
    return;
  }

  const groups = Array.isArray(state.groups) ? state.groups : [];
  const allDeps = flattenGroups(groups);
  const total = allDeps.length;
  const ok = allDeps.filter(d => d.status === 'ok').length;
  const missing = allDeps.filter(d => d.status === 'missing').length;
  const updatable = allDeps.filter(d => d.updatable && d.latest && d.latest !== d.version && d.status === 'ok').length;

  render(html`
    <section class="hero">
      <h1>Зависимости</h1>
      <p>Внешние бинарники и пакеты: yt-dlp, ffmpeg, Chromium, TorrServer, pip-пакеты. Кнопка обновления использует встроенные recipe-ы для своей платформы (${state.os}/${state.arch}).</p>
      <div class="hero-actions">
        <span class="chip accent">${ok}/${total} установлены</span>
        ${missing > 0 ? html`<span class="chip" style="color:var(--warn)">${missing} отсутствуют</span>` : ''}
        ${updatable > 0 ? html`<span class="chip" style="color:var(--info)">${updatable} c обновлением</span>` : ''}
        <button class="btn-refresh" @click=${() => refresh($mount, true)}>Проверить обновления</button>
      </div>
    </section>

    ${groups.map(g => {
      const deps = Array.isArray(g.deps) ? g.deps : [];
      const accent = GROUP_ACCENT[g.id] || 'violet';
      const icon = g.icon || '◉';
      const label = g.label || g.id || 'Прочее';
      return html`
        <div class="section-title"><span style="font-size:18px">${icon}</span> ${label} <span style="color:var(--text-3);font-size:var(--fs-xs);font-weight:var(--fw-medium);text-transform:none;letter-spacing:0">${deps.length}</span></div>
        <div class="grid cols-3">
          ${deps.map(d => renderDep($mount, d, accent))}
        </div>
      `;
    })}

    <style>
      .btn-refresh {
        background: var(--bg-2); color: var(--accent);
        border: 1px solid rgba(119,145,255,0.32);
        padding: 6px 16px; font-size: var(--fs-xs);
        border-radius: var(--r-pill); cursor: pointer;
        font-weight: var(--fw-semibold);
        transition: background 120ms, color 120ms;
      }
      .btn-refresh:hover { background: var(--accent); color: white; }

      .dep-card { display: flex; flex-direction: column; gap: var(--s-2); }
      .dep-head { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--s-2); }
      .dep-name { font-family: var(--font-display); font-weight: var(--fw-semibold); font-size: var(--fs-md); letter-spacing: -0.01em; }
      .dep-binary { font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-2); margin-top: 2px; }
      .dep-note { font-size: var(--fs-xs); color: var(--text-2); line-height: 1.5; min-height: 30px; }
      .dep-version { font-family: var(--font-mono); font-size: 12.5px; padding: 4px 10px; background: var(--bg-2); border-radius: var(--r-2); border: 1px solid var(--border-1); display: inline-block; max-width: 100%; word-break: break-word; }
      .dep-actions { display: flex; gap: 6px; margin-top: 4px; flex-wrap: wrap; }
      .dep-meta { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 4px; }
      .dep-update-info { font-size: var(--fs-xs); color: var(--info); margin-top: 4px; }

      .btn-small { background: var(--bg-2); color: var(--text-1); border: 1px solid var(--border-2); padding: 4px 10px; font-size: var(--fs-xs); border-radius: var(--r-pill); cursor: pointer; font-weight: var(--fw-semibold); }
      .btn-small:hover { background: var(--bg-3); }
      .btn-small.primary { background: var(--accent-soft); color: var(--accent); border-color: rgba(119,145,255,0.32); }
      .btn-small.primary:hover { background: var(--accent); color: white; }
      .btn-small.success { background: var(--success-soft); color: var(--success); border-color: rgba(52,224,161,0.32); }
      .btn-small.success:hover { background: var(--success); color: #0f1024; }
      .btn-small:disabled { opacity: 0.6; cursor: progress; }

      .dep-spinner { display: inline-block; width: 12px; height: 12px; border: 2px solid var(--border-2); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.8s linear infinite; vertical-align: middle; margin-right: 4px; }
    </style>
  `, $mount);
}

function renderDep($mount, d, accent) {
  const isUpdating = updating.has(d.binary) || d.updating;
  const hasUpdate = d.updatable && d.latest && d.latest !== d.version && d.status === 'ok';
  const statusPill = d.status === 'ok'
    ? html`<span class="pill ok"><span class="dot"></span> установлен</span>`
    : html`<span class="pill warn"><span class="dot"></span> отсутствует</span>`;

  return html`
    <l-card accent=${d.status === 'missing' ? 'amber' : (hasUpdate ? 'cyan' : accent)}>
      <div class="dep-card">
        <div class="dep-head">
          <div>
            <div class="dep-name">${d.name || d.binary}</div>
            <div class="dep-binary">${d.binary}</div>
          </div>
          ${statusPill}
        </div>

        <div class="dep-note">${d.note || ''}</div>

        ${d.version ? html`<div class="dep-version">${d.version}</div>` : ''}

        ${hasUpdate ? html`<div class="dep-update-info">↗ доступна версия: <strong>${d.latest}</strong></div>` : ''}

        <div class="dep-meta">
          ${d.pip_pkg ? html`<span class="pill muted">pip</span>` : ''}
          ${d.group ? html`<span class="pill muted">${d.group}</span>` : ''}
        </div>

        ${d.updatable ? html`
          <div class="dep-actions">
            ${isUpdating ? html`
              <button class="btn-small" disabled><span class="dep-spinner"></span> обновляю…</button>
            ` : (d.status === 'missing' ? html`
              <button class="btn-small primary" @click=${() => triggerUpdate($mount, d.binary)}>Установить</button>
            ` : (hasUpdate ? html`
              <button class="btn-small success" @click=${() => triggerUpdate($mount, d.binary)}>Обновить → ${d.latest}</button>
            ` : html`
              <button class="btn-small" @click=${() => triggerUpdate($mount, d.binary)}>Переустановить</button>
            `))}
          </div>
        ` : ''}
      </div>
    </l-card>
  `;
}

async function triggerUpdate($mount, binary) {
  updating.add(binary);
  paint($mount);
  try {
    await api.updateDep(binary);
    toast.info(`Обновление ${binary} запущено`);
    startPolling($mount, binary);
  } catch (e) {
    updating.delete(binary);
    toast.error(e.message);
    paint($mount);
  }
}

function startPolling($mount, binary) {
  if (pollTimers.has(binary)) clearInterval(pollTimers.get(binary));
  const tick = async () => {
    try {
      const s = await api.depUpdateStatus(binary);
      if (s.status === 'done') {
        clearInterval(pollTimers.get(binary));
        pollTimers.delete(binary);
        updating.delete(binary);
        toast.success(`${binary}: ${s.old_version || '—'} → ${s.new_version || 'updated'}`);
        await refresh($mount, false);
      } else if (s.status === 'error') {
        clearInterval(pollTimers.get(binary));
        pollTimers.delete(binary);
        updating.delete(binary);
        toast.error(`${binary}: ${s.error || 'ошибка'}`);
        paint($mount);
      } else if (s.status === 'idle') {
        clearInterval(pollTimers.get(binary));
        pollTimers.delete(binary);
        updating.delete(binary);
        paint($mount);
      }
    } catch (e) {
      // transient errors are OK — keep polling
    }
  };
  pollTimers.set(binary, setInterval(tick, 2000));
}

async function refresh($mount, checkLatest) {
  $mount.querySelector('.btn-refresh')?.setAttribute('disabled', '');
  try {
    await fetchState(checkLatest);
    paint($mount);
  } catch (e) {
    toast.error(e.message);
  }
}

export async function render_($mount) {
  render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
  updating = new Set();
  pollTimers.forEach(t => clearInterval(t));
  pollTimers = new Map();
  try {
    await fetchState(true);
    paint($mount);
  } catch (e) {
    render(html`<l-card><div style="color:var(--danger)">Не загрузилось: ${e.message}</div></l-card>`, $mount);
  }
  // Cleanup poll timers on page unmount
  const guard = new MutationObserver(() => {
    if (!document.body.contains($mount)) {
      pollTimers.forEach(t => clearInterval(t));
      pollTimers.clear();
      guard.disconnect();
    }
  });
  if ($mount.parentNode) guard.observe($mount.parentNode, { childList: true });
}

export { render_ as render };
