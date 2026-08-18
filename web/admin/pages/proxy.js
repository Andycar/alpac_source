// pages/proxy.js — VLESS / proxy entries editor.
//
// API:
//   GET  /api/proxy                       → {uri, balancers, available_balancers, active, entries[]}
//   POST /api/proxy  action=save          {entries[{uri,balancers,label,engine}]}
//   POST /api/proxy  action=test          {entry_index}  → {ok, ip, country, flag, ms, ...}
//
// UI: list of proxy entries — label / URI / engine select / balancers
// multi-select, with Test (probe via the proxy) and × per entry. "Добавить"
// appends a blank entry. "Сохранить" pushes all entries to backend.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  entries: [],            // editable copy
  pristine: [],
  available: [],          // all balancer keys that can be proxied
  loading: true,
  saving: false,
  testing: -1,            // index currently under test (-1 = none)
};

async function load() {
  state.loading = true;
  try {
    const d = await api.get('/proxy');
    state.entries = (d && d.entries || []).map(e => ({
      uri:       e.uri || '',
      label:     e.label || '',
      engine:    e.engine || '',
      balancers: [...(e.balancers || [])],
      active:    !!e.active,
      ip:        e.ip || '',
      country:   e.country || '',
      flag:      e.flag || '',
    }));
    state.pristine = JSON.parse(JSON.stringify(state.entries));
    state.available = (d && d.available_balancers) || [];
  } catch (e) {
    toast.error('Не удалось загрузить proxy: ' + e.message);
  } finally {
    state.loading = false;
  }
}

function isDirty() {
  return JSON.stringify(state.entries) !== JSON.stringify(state.pristine);
}

async function save() {
  state.saving = true; paint();
  try {
    const r = await api.post('/proxy', {
      action: 'save',
      entries: state.entries.map(e => ({
        uri: e.uri.trim(),
        balancers: e.balancers,
        label: e.label.trim(),
        engine: e.engine,
      })),
    });
    if (r && r.ok === false) throw new Error(r.error || 'save failed');
    if (r && r.error) throw new Error(r.error);
    toast.success(r && r.restart_needed ? 'Сохранено, требуется restart' : 'Сохранено');
    state.pristine = JSON.parse(JSON.stringify(state.entries));
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.saving = false; paint();
  }
}

async function testEntry(idx) {
  state.testing = idx; paint();
  try {
    const r = await api.post('/proxy', { action: 'test', entry_index: idx });
    if (r && r.ok === false) throw new Error(r.error || 'test failed');
    if (r && r.error) throw new Error(r.error);
    // Update the entry's measured info in-place.
    state.entries[idx].ip      = r.ip      || '';
    state.entries[idx].country = r.country || '';
    state.entries[idx].flag    = r.flag    || '';
    toast.success(`✓ ${r.flag || ''} ${r.country || ''} ${r.ip || ''} ${r.ms ? '(' + r.ms + 'ms)' : ''}`);
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.testing = -1; paint();
  }
}

function addEntry() {
  state.entries.push({ uri: '', label: '', engine: '', balancers: [], active: false });
  paint();
}
function removeEntry(idx) {
  if (!confirm(`Удалить запись #${idx + 1}?`)) return;
  state.entries.splice(idx, 1);
  paint();
}

function toggleBalancer(idx, name) {
  const e = state.entries[idx];
  const i = e.balancers.indexOf(name);
  if (i >= 0) e.balancers.splice(i, 1);
  else e.balancers.push(name);
  paint();
}

function paint() {
  const $root = document.getElementById('px-root');
  if (!$root) return;
  const dirty = isDirty();
  const total = state.entries.length;
  const active = state.entries.filter(e => e.active).length;
  const balancersUsed = new Set(state.entries.flatMap(e => e.balancers)).size;

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Записей"   value=${total}></l-stat>
      <l-stat accent="green"  label="Активные"  value=${active}></l-stat>
      <l-stat accent="purple" label="Балансеры назначены" value=${balancersUsed}></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title">
        <span style="font-weight:600;font-size:var(--fs-md)">Proxy entries</span>
        ${dirty ? html`<l-pill tone="warn">несохранено</l-pill>` : html`<l-pill tone="muted">синхр.</l-pill>`}
      </div>
      <div slot="actions" style="display:flex;gap:8px;">
        <l-button variant="ghost"   icon="↺"  @click=${() => { state.entries = JSON.parse(JSON.stringify(state.pristine)); paint(); }} ?disabled=${!dirty}>Сброс</l-button>
        <l-button variant="primary" icon="＋" @click=${addEntry}>Добавить запись</l-button>
        <l-button variant="success" icon="💾" @click=${save} ?loading=${state.saving} ?disabled=${!dirty || state.saving}>Сохранить</l-button>
      </div>

      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
        : state.entries.length === 0
          ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">Нет настроенных proxy entries. Нажмите «Добавить запись».</div>`
          : html`
            <div class="px-list">
              ${state.entries.map((e, idx) => entryCard(e, idx))}
            </div>`}
    </l-card>

    <l-card style="margin-top:var(--s-5)" title="Подсказка">
      <p style="color:var(--text-2);font-size:var(--fs-sm);line-height:1.6">
        Поддерживаются VLESS / VMess / Shadowsocks (зависит от engine).
        <code>engine=auto</code> — определяется по схеме URI.
        После сохранения новой записи запустится свой xray/mihomo сидекар; назначенные балансеры
        будут роутиться через SOCKS5 порт сидекара.
      </p>
    </l-card>
  `, $root);
}

function entryCard(e, idx) {
  const isTesting = state.testing === idx;
  return html`
    <div class="px-card ${e.active ? 'active' : ''}">
      <div class="px-row">
        <l-input
          icon="🏷"
          placeholder="Label (напр. «edge-fr»)"
          .value=${e.label}
          @input=${ev => { e.label = ev.detail.value; }}
          style="width:240px"
        ></l-input>
        <l-select
          .value=${e.engine}
          placeholder="auto"
          .options=${[
            { value: '',       label: 'auto' },
            { value: 'xray',   label: 'xray' },
            { value: 'mihomo', label: 'mihomo' },
          ]}
          @change=${ev => { e.engine = ev.detail.value; paint(); }}
          style="width:140px"
        ></l-select>
        ${e.active
          ? html`<l-pill tone="ok" dot>active</l-pill>`
          : html`<l-pill tone="muted">inactive</l-pill>`}
        ${e.flag || e.country ? html`<l-pill tone="info">${e.flag || ''} ${e.country || ''}${e.ip ? ' · ' + e.ip : ''}</l-pill>` : ''}
        <div style="flex:1"></div>
        <l-button size="sm" variant="ghost"  @click=${() => testEntry(idx)} ?loading=${isTesting}>🔍 Test</l-button>
        <l-button size="sm" variant="danger" @click=${() => removeEntry(idx)}>×</l-button>
      </div>
      <div class="px-row">
        <l-input
          icon="🔗"
          placeholder="vless://...  |  vmess://...  |  ss://..."
          .value=${e.uri}
          @input=${ev => { e.uri = ev.detail.value; }}
          style="flex:1"
        ></l-input>
      </div>
      <div class="px-bals">
        <div class="px-bals-h">Назначить балансерам:</div>
        <div class="px-bals-list">
          ${state.available.map(name => html`
            <button
              class="px-bal-chip ${e.balancers.includes(name) ? 'on' : ''}"
              @click=${() => toggleBalancer(idx, name)}
            >${name}</button>
          `)}
        </div>
      </div>
    </div>
  `;
}

const styleId = 'l-proxy-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr; } }

    .px-list { display: flex; flex-direction: column; gap: var(--s-3); }
    .px-card {
      padding: var(--s-3);
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      display: flex; flex-direction: column; gap: 10px;
      position: relative;
    }
    .px-card.active {
      border-color: var(--success);
      box-shadow: 0 0 0 1px rgba(52,211,153,0.18) inset;
    }
    .px-row {
      display: flex; gap: 8px; align-items: center; flex-wrap: wrap;
    }
    .px-bals { padding-top: 6px; border-top: 1px solid var(--border-1); }
    .px-bals-h {
      font-size: 10px; text-transform: uppercase; color: var(--text-3);
      letter-spacing: 0.08em; margin-bottom: 6px;
    }
    .px-bals-list { display: flex; gap: 4px; flex-wrap: wrap; }
    .px-bal-chip {
      padding: 4px 10px; font-size: var(--fs-xs);
      background: var(--bg-3); border: 1px solid var(--border-2);
      border-radius: var(--r-pill);
      color: var(--text-2); cursor: pointer;
      font-family: var(--font-mono);
      transition: background var(--t-fast), color var(--t-fast), border-color var(--t-fast);
    }
    .px-bal-chip:hover { background: var(--bg-4); color: var(--text-0); }
    .px-bal-chip.on {
      background: var(--accent-soft);
      border-color: var(--accent);
      color: var(--accent);
      font-weight: 600;
    }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="px-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
