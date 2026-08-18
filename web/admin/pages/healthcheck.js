// pages/healthcheck.js — admin UI for the balancer healthcheck loop.
//
// Two cards:
//   1) Настройки — Enabled toggle + interval/timeout/thresholds, "Probe now",
//                  "Reset all" actions.
//   2) Статус   — live table of balancers with auto-disable / health flags,
//                 per-row "Включить вручную" and "Сбросить" buttons.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

const RANGES = {
  interval_sec:      { min: 5,  max: 3600, hint: 'Промежуток между проверками (5–3600 сек)' },
  timeout_sec:       { min: 1,  max: 60,   hint: 'Таймаут одного запроса (1–60 сек)' },
  fail_threshold:    { min: 1,  max: 50,   hint: 'Подряд неудач до auto-disable (1–50)' },
  recover_threshold: { min: 1,  max: 50,   hint: 'Подряд успехов для восстановления (1–50)' },
};

let state = {
  data: null,               // last GET /healthcheck payload
  draft: null,              // editable copy of settings
  dirty: false,
  saving: false,
  loading: false,
  filter: '',               // table search
  pollTimer: null,
  pollEnabled: true,
};

function clamp(v, min, max) {
  v = parseInt(v, 10);
  if (!Number.isFinite(v)) return min;
  if (v < min) return min;
  if (v > max) return max;
  return v;
}

async function load() {
  state.loading = true;
  try {
    const data = await api.get('/healthcheck');
    state.data = data;
    if (!state.dirty || !state.draft) {
      state.draft = { ...data.settings };
      state.dirty = false;
    }
  } catch (e) {
    toast.error('Не удалось загрузить healthcheck: ' + e.message);
  } finally {
    state.loading = false;
    paint();
  }
}

async function saveSettings() {
  if (!state.draft) return;
  const payload = {
    enabled:           !!state.draft.enabled,
    interval_sec:      clamp(state.draft.interval_sec,      RANGES.interval_sec.min,      RANGES.interval_sec.max),
    timeout_sec:       clamp(state.draft.timeout_sec,       RANGES.timeout_sec.min,       RANGES.timeout_sec.max),
    fail_threshold:    clamp(state.draft.fail_threshold,    RANGES.fail_threshold.min,    RANGES.fail_threshold.max),
    recover_threshold: clamp(state.draft.recover_threshold, RANGES.recover_threshold.min, RANGES.recover_threshold.max),
  };
  state.saving = true; paint();
  try {
    const resp = await api.post('/healthcheck', { action: 'save_settings', settings: payload });
    state.data = resp;
    state.draft = { ...resp.settings };
    state.dirty = false;
    toast.success('Настройки применены');
  } catch (e) {
    toast.error('Сохранение не удалось: ' + e.message);
  } finally {
    state.saving = false;
    paint();
  }
}

async function probeNow() {
  try {
    await api.post('/healthcheck', { action: 'probe_now' });
    toast.success('Проверка запущена');
    // Reload after a short delay so the new probe results are visible.
    setTimeout(load, 1500);
  } catch (e) {
    toast.error(e.message);
  }
}

async function resetAll() {
  if (!confirm('Сбросить статистику ВСЕХ балансеров? Auto-disable флаги тоже снимутся.')) return;
  try {
    const r = await api.post('/healthcheck', { action: 'reset', balancer: '' });
    toast.success(`Сброшено: ${r.affected || 0}`);
    load();
  } catch (e) {
    toast.error(e.message);
  }
}

async function resetOne(name) {
  try {
    await api.post('/healthcheck', { action: 'reset', balancer: name });
    toast.success(`Сброшено: ${name}`);
    load();
  } catch (e) {
    toast.error(e.message);
  }
}

async function reEnableOne(name) {
  try {
    await api.post('/healthcheck', { action: 're_enable', balancer: name });
    toast.success(`${name} включён`);
    load();
  } catch (e) {
    toast.error(e.message);
  }
}

async function toggleExcluded(name, excluded) {
  try {
    const resp = await api.post('/healthcheck', {
      action: 'set_excluded',
      balancer: name,
      excluded,
    });
    // Server returns a full snapshot — refresh state without a second GET.
    state.data = resp;
    if (!state.dirty || !state.draft) {
      state.draft = { ...resp.settings };
      state.dirty = false;
    }
    toast.success(excluded ? `${name}: проверка отключена` : `${name}: проверка включена`);
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

function statusBadge(row) {
  if (row.excluded)      return html`<l-pill tone="muted">Не проверяется</l-pill>`;
  if (row.auto_disabled) return html`<l-pill tone="danger" dot>Auto-disabled</l-pill>`;
  if (!row.has_data)     return html`<l-pill tone="muted">Без данных</l-pill>`;
  if (!row.healthy)      return html`<l-pill tone="warn" dot>Сбой</l-pill>`;
  return html`<l-pill tone="ok">OK</l-pill>`;
}

function formatTime(t) {
  if (!t || t.startsWith('0001-')) return '—';
  const d = new Date(t);
  if (isNaN(d.getTime())) return '—';
  const ago = Math.round((Date.now() - d.getTime()) / 1000);
  if (ago < 0)         return d.toLocaleTimeString();
  if (ago < 60)        return `${ago}с назад`;
  if (ago < 3600)      return `${Math.round(ago / 60)} мин назад`;
  return d.toLocaleString();
}

function renderSettingsCard() {
  const d = state.draft || {};
  const s = state.data?.settings || {};
  const changed = state.dirty;
  return html`
    <l-card>
      <div slot="title" style="display:flex;align-items:center;gap:8px;">
        <span style="font-weight:600;font-size:var(--fs-md)">Настройки Healthcheck</span>
        ${d.enabled
          ? html`<l-pill tone="ok" dot>Включён</l-pill>`
          : html`<l-pill tone="muted" dot>Выключен</l-pill>`}
      </div>
      <div slot="actions" style="display:flex;gap:8px;flex-wrap:wrap;">
        <l-button size="sm" variant="ghost" icon="↻" @click=${load} ?loading=${state.loading}>Обновить</l-button>
        <l-button size="sm" variant="ghost" icon="🔍" @click=${probeNow} ?disabled=${!d.enabled}>Запустить проверку</l-button>
        <l-button size="sm" variant="danger" icon="♻" @click=${resetAll}>Сбросить всё</l-button>
      </div>

      <div class="settings-grid">
        ${enabledRow()}
        ${numRow('Интервал, сек',         'interval_sec')}
        ${numRow('Таймаут, сек',          'timeout_sec')}
        ${numRow('Порог сбоев',            'fail_threshold')}
        ${numRow('Порог восстановления',   'recover_threshold')}
      </div>

      <div class="settings-foot">
        <l-button variant="primary" ?disabled=${!changed} ?loading=${state.saving}
                  @click=${saveSettings}>
          ${state.saving ? 'Сохраняю…' : 'Сохранить'}
        </l-button>
        <l-button variant="ghost" ?disabled=${!changed}
                  @click=${() => { state.draft = { ...s }; state.dirty = false; paint(); }}>
          Отменить
        </l-button>
        <span class="muted">
          Изменения применяются на лету и переживают рестарт
          (override-файл <code>database/healthcheck_settings.json</code>).
        </span>
      </div>
    </l-card>
  `;
}

function enabledRow() {
  const d = state.draft;
  return html`
    <label class="hc-row hc-bool">
      <div>
        <div class="hc-l">Активен</div>
        <div class="hc-hint">Когда выключен — фоновые проверки не идут, но уже зафиксированные auto-disable остаются.</div>
      </div>
      <label class="switch">
        <input type="checkbox" ?checked=${!!d.enabled}
               @change=${e => { d.enabled = e.target.checked; state.dirty = true; paint(); }}/>
        <span class="switch-track"></span>
      </label>
    </label>
  `;
}

function numRow(label, key) {
  const d = state.draft;
  const r = RANGES[key];
  return html`
    <div class="hc-row">
      <div>
        <div class="hc-l">${label}</div>
        <div class="hc-hint">${r.hint}</div>
      </div>
      <input class="hc-num" type="number" min=${r.min} max=${r.max}
             .value=${String(d[key] ?? r.min)}
             @input=${e => { d[key] = parseInt(e.target.value, 10) || 0; state.dirty = true; paint(); }}/>
    </div>
  `;
}

function renderStatusCard() {
  const data = state.data;
  if (!data) {
    return html`<l-card><div class="muted" style="padding:24px;text-align:center;">Загрузка…</div></l-card>`;
  }
  const c = data.counts || {};
  const filter = state.filter.trim().toLowerCase();
  const rows = (data.entries || []).filter(r => !filter || r.name.toLowerCase().includes(filter));

  return html`
    <div class="hc-summary">
      <l-stat accent="blue"  label="Всего"           value=${c.total || 0}></l-stat>
      <l-stat accent="green" label="Здоровых"        value=${c.healthy || 0}></l-stat>
      <l-stat accent="amber" label="Сбоев"           value=${c.unhealthy || 0}></l-stat>
      <l-stat accent="pink"  label="Auto-disabled"   value=${c.auto_disabled || 0}></l-stat>
      <l-stat accent="slate" label="Без проверки"    value=${c.excluded || 0}></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">Состояние балансеров</span></div>
      <div slot="actions">
        <l-input type="search" placeholder="Поиск по имени…" icon="🔎" clearable
                 .value=${state.filter}
                 @input=${e => { state.filter = e.detail.value; paint(); }}></l-input>
      </div>

      <div class="hc-table-wrap">
        <table class="hc-table">
          <thead>
            <tr>
              <th>Балансер</th>
              <th>Проверка</th>
              <th>Статус</th>
              <th>Последняя проверка</th>
              <th>HTTP</th>
              <th>Латентность</th>
              <th>Подряд</th>
              <th class="hc-err-col">Ошибка</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            ${rows.length === 0
              ? html`<tr><td colspan="9" class="muted" style="padding:24px;text-align:center;">Нет данных</td></tr>`
              : rows.map(r => html`
                <tr class=${r.excluded
                              ? 'row-excluded'
                              : (r.auto_disabled
                                  ? 'row-disabled'
                                  : (!r.has_data
                                      ? 'row-empty'
                                      : (r.healthy ? '' : 'row-bad')))}>
                  <td>
                    <div class="hc-name">${r.name}</div>
                    ${r.group ? html`<div class="hc-group">${r.group}</div>` : ''}
                  </td>
                  <td>
                    <label class="switch switch-sm" title=${r.excluded ? 'Включить проверку' : 'Отключить проверку'}>
                      <input type="checkbox" ?checked=${!r.excluded}
                             @change=${e => toggleExcluded(r.name, !e.target.checked)}/>
                      <span class="switch-track"></span>
                    </label>
                  </td>
                  <td>${statusBadge(r)}</td>
                  <td>${r.excluded ? '—' : formatTime(r.last_check_time)}</td>
                  <td>${r.excluded ? '—' : (r.last_check_status ? r.last_check_status : '—')}</td>
                  <td>${r.excluded ? '—' : (r.has_data ? r.last_check_latency_ms + ' мс' : '—')}</td>
                  <td>
                    ${r.excluded
                      ? '—'
                      : (r.consecutive_fails > 0
                          ? html`<span class="bad">−${r.consecutive_fails}</span>`
                          : (r.consecutive_ok > 0
                              ? html`<span class="good">+${r.consecutive_ok}</span>`
                              : '—'))}
                  </td>
                  <td class="hc-err">${r.excluded ? '' : (r.last_check_error || '')}</td>
                  <td class="hc-actions">
                    ${!r.excluded && r.auto_disabled
                      ? html`<l-button size="sm" variant="success" @click=${() => reEnableOne(r.name)}>✓ Включить</l-button>`
                      : ''}
                    ${!r.excluded && r.has_data
                      ? html`<l-button size="sm" variant="ghost" @click=${() => resetOne(r.name)} title="Сбросить счётчики">♻</l-button>`
                      : ''}
                  </td>
                </tr>
              `)}
          </tbody>
        </table>
      </div>
    </l-card>
  `;
}

function paint() {
  const $root = document.getElementById('healthcheck-root');
  if (!$root) return;
  render(html`
    ${renderSettingsCard()}
    <div style="margin-top:var(--s-5)"></div>
    ${renderStatusCard()}
  `, $root);
}

// Inject page-local styles once.
const styleId = 'l-healthcheck-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .hc-summary {
      display: grid; grid-template-columns: repeat(5, 1fr); gap: var(--s-4);
    }
    @media (max-width: 1200px) { .hc-summary { grid-template-columns: repeat(3, 1fr); } }
    @media (max-width: 900px)  { .hc-summary { grid-template-columns: repeat(2, 1fr); } }

    .settings-grid {
      display: grid; grid-template-columns: 1fr; gap: var(--s-3);
      margin-top: var(--s-3);
    }
    .hc-row {
      display: flex; align-items: center; justify-content: space-between;
      gap: var(--s-4);
      padding: 10px 14px;
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-2);
    }
    .hc-row .hc-l { font-weight: 600; font-size: var(--fs-sm); color: var(--text-0); }
    .hc-row .hc-hint { font-size: var(--fs-xs); color: var(--text-3); margin-top: 2px; }
    .hc-num {
      width: 90px; padding: 6px 8px;
      background: var(--bg-1); color: var(--text-0);
      border: 1px solid var(--border-1); border-radius: var(--r-2);
      font-family: var(--font-mono); font-size: var(--fs-sm);
      text-align: right;
    }
    .hc-bool .switch { display: inline-block; position: relative; width: 42px; height: 24px; }
    .hc-bool .switch input { opacity: 0; width: 0; height: 0; }
    .hc-bool .switch-track {
      position: absolute; inset: 0; border-radius: 999px;
      background: var(--bg-3); border: 1px solid var(--border-1);
      transition: background .15s ease;
    }
    .hc-bool .switch-track::before {
      content: ''; position: absolute; top: 2px; left: 2px;
      width: 18px; height: 18px; border-radius: 50%;
      background: var(--text-2); transition: transform .15s ease, background .15s ease;
    }
    .hc-bool .switch input:checked + .switch-track { background: var(--accent-soft); border-color: var(--accent); }
    .hc-bool .switch input:checked + .switch-track::before { transform: translateX(18px); background: var(--accent); }

    .settings-foot {
      display: flex; align-items: center; gap: var(--s-3); flex-wrap: wrap;
      margin-top: var(--s-4); padding-top: var(--s-3);
      border-top: 1px dashed var(--border-1);
    }
    .settings-foot .muted { font-size: var(--fs-xs); color: var(--text-3); }
    .settings-foot code { color: var(--accent); background: var(--bg-3); padding: 1px 6px; border-radius: 4px; font-family: var(--font-mono); }

    .hc-table-wrap { margin-top: var(--s-3); overflow-x: auto; }
    .hc-table { width: 100%; border-collapse: collapse; font-size: var(--fs-sm); }
    .hc-table th, .hc-table td {
      padding: 8px 10px; text-align: left; vertical-align: middle;
      border-bottom: 1px solid var(--border-1);
    }
    .hc-table th {
      font-size: var(--fs-xs); text-transform: uppercase;
      color: var(--text-3); letter-spacing: 0.05em; font-weight: 600;
      background: var(--bg-2);
    }
    .hc-table tr:last-child td { border-bottom: none; }
    .hc-table .row-disabled { background: rgba(248,113,113,0.04); }
    .hc-table .row-bad      { background: rgba(251,191,36,0.03); }
    .hc-table .row-empty    { opacity: 0.55; }
    .hc-table .row-excluded { opacity: 0.45; }
    .hc-table .row-excluded .hc-name { text-decoration: line-through; text-decoration-color: var(--text-3); }

    /* Compact switch used in the table — same look as the big one in the
       settings card but at ~75% size. */
    .switch.switch-sm { display: inline-block; position: relative; width: 34px; height: 20px; vertical-align: middle; }
    .switch.switch-sm input { opacity: 0; width: 0; height: 0; }
    .switch.switch-sm .switch-track {
      position: absolute; inset: 0; border-radius: 999px;
      background: var(--bg-3); border: 1px solid var(--border-1);
      transition: background .15s ease, border-color .15s ease;
      cursor: pointer;
    }
    .switch.switch-sm .switch-track::before {
      content: ''; position: absolute; top: 2px; left: 2px;
      width: 14px; height: 14px; border-radius: 50%;
      background: var(--text-2); transition: transform .15s ease, background .15s ease;
    }
    .switch.switch-sm input:checked + .switch-track {
      background: var(--accent-soft); border-color: var(--accent);
    }
    .switch.switch-sm input:checked + .switch-track::before {
      transform: translateX(14px); background: var(--accent);
    }
    .hc-name  { font-weight: 600; color: var(--text-0); }
    .hc-group { font-size: 10px; color: var(--text-3); text-transform: uppercase; letter-spacing: 0.04em; }
    .hc-err   {
      font-family: var(--font-mono); font-size: var(--fs-xs); color: #fca5a5;
      max-width: 320px;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .hc-err-col { width: 320px; }
    .hc-actions { display: flex; gap: 6px; justify-content: flex-end; }
    .bad { color: #fca5a5; font-family: var(--font-mono); font-weight: 600; }
    .good { color: #6ee7b7; font-family: var(--font-mono); font-weight: 600; }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="healthcheck-root"></div>`;
  await load();

  // Poll every 10s while the page is visible so the table shows fresh state
  // without a stream endpoint — healthcheck data only changes every minute
  // anyway, so polling is fine.
  if (state.pollTimer) clearInterval(state.pollTimer);
  state.pollTimer = setInterval(() => {
    if (!state.pollEnabled) return;
    if (document.visibilityState !== 'visible') return;
    if (state.dirty || state.saving) return;  // don't clobber user input
    load();
  }, 10000);

  const guard = new MutationObserver(() => {
    if (!document.body.contains($mount)) {
      if (state.pollTimer) clearInterval(state.pollTimer);
      state.pollTimer = null;
      guard.disconnect();
    }
  });
  guard.observe($mount.parentNode, { childList: true });
}
export { render_ as render };
