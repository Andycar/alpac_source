// pages/proxycore.js — built-in proxy engine pool (the "new" proxy stack
// that replaces xray/mihomo sidecars).
//
// API:
//   GET  /api/proxycore/status                       → {entries: PoolStatus[], rules}
//   GET  /api/proxycore/entries                      → {entries: PoolEntry[]}
//   POST /api/proxycore/entries  action=add|update|delete
//   GET  /api/proxycore/balancers                    → available balancers for assignment
//   POST /api/proxycore/test     {uri}               → probe a candidate URI
//
// Each entry produces a SOCKS5 sidecar on a local port; balancers are
// routed through their assigned proxy by name match. Status snapshot shows
// liveness, country/flag, latency, bytes in/out, connection counts.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  entries: [],         // editable config entries
  statuses: [],        // PoolStatus[] keyed by index
  available: [],
  loading: true,
  saving: false,
  testing: -1,
  editing: null,       // {index, draft}
};
let pollTimer = null;

async function load() {
  state.loading = true;
  try {
    const [st, ent, bals] = await Promise.all([
      api.get('/proxycore/status'),
      api.get('/proxycore/entries'),
      api.get('/proxycore/balancers').catch(() => ({balancers: []})),
    ]);
    state.statuses = (st && st.entries) || [];
    state.entries  = (ent && ent.entries) || [];
    state.available = (bals && (bals.balancers || bals.available_balancers)) || [];
  } catch (e) {
    toast.error('Не удалось загрузить ProxyCore: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function addEntry() {
  state.editing = {
    index: -1,
    draft: { uri: '', label: '', engine: '', balancers: [] },
  };
  paint();
}

function editEntry(idx) {
  const e = state.entries[idx];
  state.editing = {
    index: idx,
    draft: {
      uri: e.uri || '',
      label: e.label || '',
      engine: e.engine || '',
      balancers: [...(e.balancers || [])],
    },
  };
  paint();
}

function closeEditor() {
  state.editing = null;
  paint();
}

async function saveEntry() {
  if (!state.editing) return;
  const { index, draft } = state.editing;
  const action = index < 0 ? 'add' : 'update';
  state.saving = true; paint();
  try {
    // For "update" we let the backend keep existing URI when sentinel "_keep_"
    // is sent — handy when the user only changes balancer assignment.
    const uri = draft.uri.trim();
    const r = await api.post('/proxycore/entries', {
      action, index,
      uri: uri || '_keep_',
      label: draft.label.trim(),
      engine: draft.engine || '',
      balancers: draft.balancers,
    });
    if (r && r.error) throw new Error(r.error);
    if (r && r.reload_error) toast.warn('Сохранено, но reload не удался: ' + r.reload_error);
    else toast.success(action === 'add' ? 'Добавлен' : 'Обновлён');
    await load();
    closeEditor();
  } catch (e) {
    toast.error(e.message);
    state.saving = false; paint();
  }
}

async function deleteEntry(idx) {
  if (!confirm(`Удалить запись #${idx + 1}?`)) return;
  try {
    const r = await api.post('/proxycore/entries', { action: 'delete', index: idx });
    if (r && r.error) throw new Error(r.error);
    toast.success('Удалена');
    await load();
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

async function testURI(uri) {
  if (!uri || !uri.trim()) return toast.warn('Введите URI');
  state.testing = -2; paint();
  try {
    const r = await api.post('/proxycore/test', { uri: uri.trim() });
    if (r && r.error) throw new Error(r.error);
    const summary = r && r.ip
      ? `${r.flag || ''} ${r.country || ''} · ${r.ip}${r.ms ? ' · ' + r.ms + 'ms' : ''}`
      : 'OK';
    toast.success('✓ ' + summary);
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.testing = -1; paint();
  }
}

function toggleBalancer(name) {
  if (!state.editing) return;
  const bs = state.editing.draft.balancers;
  const i = bs.indexOf(name);
  if (i >= 0) bs.splice(i, 1); else bs.push(name);
  paint();
}

function statusFor(idx) {
  // Status array index isn't guaranteed to match entry index; match by label.
  const e = state.entries[idx];
  if (!e) return null;
  return state.statuses.find(s => s.label === e.label) || state.statuses[idx] || null;
}

function shortBytes(n) {
  if (!Number.isFinite(n) || n <= 0) return '0';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(1) + ' ' + u[i];
}

function fmtUptime(s) {
  if (!s || s < 0) return '—';
  if (s < 60)   return Math.floor(s) + 'с';
  if (s < 3600) return Math.floor(s / 60) + 'м';
  if (s < 86400) return Math.floor(s / 3600) + 'ч ' + Math.floor((s % 3600) / 60) + 'м';
  return Math.floor(s / 86400) + 'д ' + Math.floor((s % 86400) / 3600) + 'ч';
}

function paint() {
  const $root = document.getElementById('pc-root');
  if (!$root) return;

  const total = state.entries.length;
  const alive = state.statuses.filter(s => s.alive).length;
  const totalConns = state.statuses.reduce((a, s) => a + (s.active_conns || 0), 0);
  const totalBytes = state.statuses.reduce((a, s) => a + (s.bytes_up || 0) + (s.bytes_down || 0), 0);

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Всего записей" value=${total}></l-stat>
      <l-stat accent="green"  label="Активные"     value=${alive}></l-stat>
      <l-stat accent="purple" label="Соединений"   value=${totalConns}></l-stat>
      <l-stat accent="amber"  label="Трафик"       value=${shortBytes(totalBytes)}></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">Прокси-инстансы</span></div>
      <div slot="actions" style="display:flex;gap:8px;">
        <l-button variant="primary" icon="＋" @click=${addEntry}>Добавить</l-button>
        <l-button variant="secondary" icon="↻" @click=${async () => { await load(); paint(); }}>Обновить</l-button>
      </div>

      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
        : state.entries.length === 0
          ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">
              ProxyCore пуст. Нажмите «Добавить», чтобы создать первый инстанс.
            </div>`
          : html`
            <div class="pc-grid">
              ${state.entries.map((e, idx) => entryCard(e, idx, statusFor(idx)))}
            </div>`}
    </l-card>

    ${renderEditModal()}

    <l-card style="margin-top:var(--s-5)" title="Подсказка">
      <p style="color:var(--text-2);font-size:var(--fs-sm);line-height:1.6">
        Поддерживаются VLESS / VMess / Trojan / Shadowsocks / Hysteria2 / Reality / WireGuard — engine
        <code>auto</code> определяет протокол по схеме URI. Назначенные балансеры будут роутиться
        через SOCKS5 порт соответствующего инстанса (показан в метрике <b>SOCKS</b>).
      </p>
    </l-card>
  `, $root);
}

function entryCard(e, idx, st) {
  const alive = st && st.alive;
  return html`
    <div class="pc-card ${alive ? 'alive' : 'dead'}">
      <div class="pc-head">
        <div class="pc-title">
          <span class="pc-flag">${st?.flag || '🌐'}</span>
          <span class="pc-name">${e.label || '(без названия)'}</span>
          ${st?.country ? html`<span class="pc-cc">${st.country_code || st.country}</span>` : ''}
        </div>
        <l-pill tone=${alive ? 'ok' : 'danger'} dot=${alive}>${alive ? 'online' : 'down'}</l-pill>
      </div>

      <div class="pc-uri" title=${e.uri}>
        <code>${shortenURI(e.uri)}</code>
        <span class="pc-engine">${st?.engine || e.engine || 'auto'}</span>
      </div>

      <div class="pc-metrics">
        <div class="pc-m"><div class="pc-m-l">SOCKS</div><div class="pc-m-v">${st?.socks_addr || '—'}</div></div>
        <div class="pc-m"><div class="pc-m-l">Latency</div><div class="pc-m-v">${st?.avg_latency_ms || st?.latency_ms || 0}<small>ms</small></div></div>
        <div class="pc-m"><div class="pc-m-l">Uptime</div><div class="pc-m-v">${fmtUptime(st?.uptime_sec)}</div></div>
        <div class="pc-m"><div class="pc-m-l">Conn.</div><div class="pc-m-v">${st?.active_conns || 0}</div></div>
        <div class="pc-m"><div class="pc-m-l">↑/↓</div><div class="pc-m-v" style="font-size:var(--fs-sm)">${shortBytes(st?.bytes_up || 0)}<br>${shortBytes(st?.bytes_down || 0)}</div></div>
        <div class="pc-m"><div class="pc-m-l">Errors</div><div class="pc-m-v" style="color:${(st?.fail_count || 0) > 0 ? 'var(--danger)' : 'var(--text-1)'}">${st?.fail_count || 0}</div></div>
      </div>

      <div class="pc-bals">
        ${(e.balancers || []).length === 0
          ? html`<span style="color:var(--text-3);font-size:var(--fs-xs)">балансеры не назначены</span>`
          : (e.balancers || []).map(b => html`<l-pill tone="info">${b}</l-pill>`)}
      </div>

      <div class="pc-foot">
        <l-button size="sm" variant="ghost"  @click=${() => testURI(e.uri)} ?loading=${state.testing === -2}>🔍 Test</l-button>
        <l-button size="sm" variant="ghost"  @click=${() => editEntry(idx)}>⚙ Изменить</l-button>
        <l-button size="sm" variant="danger" @click=${() => deleteEntry(idx)}>×</l-button>
      </div>
    </div>
  `;
}

function shortenURI(u) {
  if (!u) return '—';
  if (u.length <= 60) return u;
  return u.slice(0, 34) + '…' + u.slice(-22);
}

function renderEditModal() {
  if (!state.editing) return '';
  const { draft, index } = state.editing;
  const isNew = index < 0;
  return html`
    <l-modal ?open=${!!state.editing} title=${(isNew ? 'Новая запись ProxyCore' : 'Запись #' + (index + 1))} @close=${closeEditor}>
      <div class="pc-form">
        <div class="pc-fld">
          <label>Label</label>
          <l-input
            placeholder="🇷🇺 Russia / 🇩🇪 Frankfurt / edge-fr"
            .value=${draft.label}
            @input=${e => { draft.label = e.detail.value; }}
          ></l-input>
        </div>
        <div class="pc-fld">
          <label>URI <span class="pc-help">vless:// vmess:// trojan:// ss:// hy2:// wg://</span></label>
          <l-input
            placeholder=${isNew ? 'vless://uuid@host:port?…' : 'оставьте пустым чтобы не менять'}
            .value=${draft.uri}
            @input=${e => { draft.uri = e.detail.value; }}
          ></l-input>
        </div>
        <div class="pc-fld">
          <label>Engine</label>
          <l-select
            .value=${draft.engine}
            .options=${[
              { value: '',          label: 'auto' },
              { value: 'proxycore', label: 'proxycore (built-in)' },
              { value: 'xray',      label: 'xray (sidecar)' },
              { value: 'mihomo',    label: 'mihomo (sidecar)' },
            ]}
            @change=${e => { draft.engine = e.detail.value; paint(); }}
          ></l-select>
        </div>
        <div class="pc-fld">
          <label>Балансеры (клик чтобы назначить/снять)</label>
          <div class="pc-bal-chips">
            ${state.available.length === 0
              ? html`<span style="color:var(--text-3);font-size:var(--fs-xs)">Список балансеров не загрузился</span>`
              : state.available.map(name => html`
                <button
                  class="pc-bal-chip ${draft.balancers.includes(name) ? 'on' : ''}"
                  @click=${() => toggleBalancer(name)}
                >${name}</button>
              `)}
          </div>
        </div>
      </div>
      <div slot="actions">
        <l-button variant="ghost" @click=${closeEditor}>Отмена</l-button>
        ${draft.uri.trim() ? html`<l-button variant="secondary" icon="🔍" @click=${() => testURI(draft.uri)}>Test</l-button>` : ''}
        <l-button variant="primary" icon="💾" @click=${saveEntry} ?loading=${state.saving}>${isNew ? 'Добавить' : 'Сохранить'}</l-button>
      </div>
    </l-modal>
  `;
}

const styleId = 'l-proxycore-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }

    .pc-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
      gap: var(--s-3);
    }
    .pc-card {
      padding: var(--s-3);
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      display: flex; flex-direction: column; gap: 10px;
      position: relative; overflow: hidden;
      transition: border-color var(--t-fast);
    }
    .pc-card::before {
      content: ''; position: absolute; top: 0; left: 0; right: 0; height: 2px;
      background: linear-gradient(90deg, var(--accent), var(--accent-2)); opacity: 0.6;
    }
    .pc-card.alive::before { background: linear-gradient(90deg, var(--success), #06b6d4); opacity: 1; }
    .pc-card.dead::before  { background: linear-gradient(90deg, var(--danger), #f43f5e);  opacity: 1; }
    .pc-card:hover { border-color: var(--border-3); }

    .pc-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
    .pc-title { display: flex; align-items: center; gap: 8px; min-width: 0; }
    .pc-flag { font-size: 18px; }
    .pc-name { font-family: var(--font-display); font-weight: 600; font-size: var(--fs-md); }
    .pc-cc { font-size: 10px; padding: 1px 6px; border-radius: 4px;
             background: var(--bg-3); color: var(--text-2); font-family: var(--font-mono); }

    .pc-uri { display: flex; align-items: center; gap: 8px; justify-content: space-between; }
    .pc-uri code {
      font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--accent);
      background: var(--bg-3); padding: 2px 6px; border-radius: 4px;
      overflow: hidden; text-overflow: ellipsis;
    }
    .pc-engine { font-size: 10px; color: var(--text-3); text-transform: uppercase;
                 letter-spacing: 0.08em; flex-shrink: 0; }

    .pc-metrics {
      display: grid; grid-template-columns: repeat(3, 1fr); gap: 6px;
    }
    .pc-m { background: var(--bg-3); padding: 6px 8px; border-radius: var(--r-2); }
    .pc-m-l { font-size: 9px; text-transform: uppercase; color: var(--text-3); letter-spacing: 0.08em; }
    .pc-m-v { font-family: var(--font-display); font-weight: 700; font-size: var(--fs-md);
              font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .pc-m-v small { font-size: 0.55em; color: var(--text-3); margin-left: 2px; }

    .pc-bals { display: flex; gap: 4px; flex-wrap: wrap; min-height: 22px; }
    .pc-foot { display: flex; gap: 4px; flex-wrap: wrap; margin-top: auto; }

    .pc-form { display: flex; flex-direction: column; gap: var(--s-3); }
    .pc-fld { display: flex; flex-direction: column; gap: 4px; }
    .pc-fld label {
      font-size: 10px; text-transform: uppercase; color: var(--text-3);
      letter-spacing: 0.08em; font-weight: 600;
      display: flex; justify-content: space-between; align-items: baseline;
    }
    .pc-help { font-size: 9px; color: var(--text-3); text-transform: none; font-weight: 400; }
    .pc-bal-chips { display: flex; gap: 4px; flex-wrap: wrap;
                    max-height: 200px; overflow-y: auto; padding: 4px;
                    background: var(--bg-0); border: 1px solid var(--border-2); border-radius: var(--r-2); }
    .pc-bal-chip {
      padding: 4px 10px; font-size: var(--fs-xs);
      background: var(--bg-3); border: 1px solid var(--border-2);
      border-radius: var(--r-pill);
      color: var(--text-2); cursor: pointer; font-family: var(--font-mono);
      transition: background var(--t-fast), color var(--t-fast), border-color var(--t-fast);
    }
    .pc-bal-chip:hover { background: var(--bg-4); color: var(--text-0); }
    .pc-bal-chip.on {
      background: var(--accent-soft); border-color: var(--accent);
      color: var(--accent); font-weight: 600;
    }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="pc-root"></div>`;
  paint();
  await load();
  paint();
  if (pollTimer) clearInterval(pollTimer);
  // Live status refresh while page visible.
  pollTimer = setInterval(() => {
    if (document.visibilityState === 'visible' && !state.editing) {
      api.get('/proxycore/status').then(r => {
        state.statuses = (r && r.entries) || [];
        paint();
      }).catch(() => {});
    }
  }, 5000);

  // Stop polling when unmounted.
  const guard = new MutationObserver(() => {
    if (!document.body.contains($mount)) {
      if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
      guard.disconnect();
    }
  });
  if ($mount.parentNode) guard.observe($mount.parentNode, { childList: true });
}
export { render_ as render };
