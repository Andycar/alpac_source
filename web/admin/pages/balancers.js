// pages/balancers.js — premium balancer management:
//   - grouped by category (working / search-only / blocked / dead / paid)
//   - enable/disable toggle per balancer
//   - quality badge with edit-on-click
//   - stream-proxy toggle
//   - quick link to telemetry card
//
// API:
//   GET  /api/balancers                  → []balancerFullInfo
//   POST /api/balancers { name, fields } → save config patch

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  rows: [],
  filter: '',
  group: '',
  loading: true,
  groups: [],
  // Edit-modal state. When editing != null, the modal is open.
  editing: null,        // {row}
  editValues: {},       // editable copy of fields
  saving: false,
  deletedKeys: new Set(), // keys explicitly removed by admin in this session
  addingField: false,    // toggled by "+ Добавить поле"
  newKey: '',
  newType: 'string',
  newValue: '',
};

// Suggested fields shown as one-click chips when adding a new field — these
// are the most common knobs across balancers (host, token, auth). The user
// can still type any arbitrary key.
const SUGGESTED_FIELDS = [
  { key: 'host',     type: 'string', hint: 'Domain or domain:port' },
  { key: 'apiHost',  type: 'string', hint: 'API endpoint host' },
  { key: 'listHost', type: 'string', hint: 'List endpoint host' },
  { key: 'linkHost', type: 'string', hint: 'CDN link host' },
  { key: 'token',    type: 'secret', hint: 'Auth token' },
  { key: 'login',    type: 'string', hint: 'Username' },
  { key: 'password', type: 'secret', hint: 'Password' },
  { key: 'cookie',   type: 'secret', hint: 'HTTP cookie' },
  { key: 'api_key',  type: 'secret', hint: 'API key' },
  { key: 'rhub',     type: 'string', hint: 'Rezka mirror' },
  { key: 'partner',  type: 'string', hint: 'Partner ID' },
  { key: 'cache_size_mb',  type: 'number', hint: 'Cache size (MB)' },
  { key: 'cache_ttl_min',  type: 'number', hint: 'Cache TTL (minutes)' },
  { key: 'quality_badge',  type: 'enum:4K,2K,FHD,HD,SD', hint: 'Quality stamp' },
  { key: 'display_name',   type: 'string', hint: 'UI label override' },
];

// Field metadata: hidden flags managed by the master toggles / status pills.
// Editing these in the modal would be redundant and error-prone.
const HIDDEN_FIELDS = new Set([
  'enable', 'enabled',     // master on/off — top-card toggle
  '_stream_proxy',          // own toggle in card
  '_proxy_label',           // pill, read-only
  '_embedded',              // server-detected, read-only
  'custom',                 // type marker, read-only
]);
// Fields known to be sensitive — render as password input (visible on focus).
const SENSITIVE_FIELDS = new Set(['token', 'password', 'api_key', 'apikey', 'cookie']);
// Fields with semantic types (override auto-detection by value).
const FIELD_HINTS = {
  port: 'number',
  cache_size_mb: 'number',
  cache_ttl_min: 'number',
  quality_badge: 'enum:4K,2K,FHD,HD,SD',
};

function fieldType(key, value) {
  if (FIELD_HINTS[key]) return FIELD_HINTS[key];
  if (typeof value === 'boolean') return 'bool';
  if (typeof value === 'number')  return 'number';
  if (Array.isArray(value))       return 'json';
  if (value && typeof value === 'object') return 'json';
  if (SENSITIVE_FIELDS.has(key.toLowerCase())) return 'secret';
  return 'string';
}

const GROUP_LABELS = {
  working:    '✓ Рабочие',
  ua:         'UA рабочие',
  search:     '🔎 Только поиск',
  paid:       '🔐 С токеном',
  blocked:    '⛔ Заблокированы CDN',
  dead:       '☠ Мёртвые',
  legacy:     'Устаревшие',
  removed:    'Удалены',
  other:      'Прочее',
};

async function load() {
  state.loading = true;
  try {
    const rows = await api.balancers();
    state.rows = Array.isArray(rows) ? rows : (rows.balancers || []);
    state.groups = [...new Set(state.rows.map(r => r.group || 'other'))];
  } catch (e) {
    toast.error('Не удалось загрузить балансеры: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function toggle(row) {
  const cur = isEnabled(row);
  const next = !cur;
  const patch = { enable: next };
  try {
    const res = await api.post('/balancers', { name: row.name, fields: patch });
    if (res && res.error) throw new Error(res.error);
    toast.success((next ? 'Включён: ' : 'Выключен: ') + row.name);
    // Optimistic local update so the toggle responds instantly.
    row.fields = { ...(row.fields || {}), enable: next };
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

function openEditor(row) {
  state.editing = row;
  // Deep-copy fields so cancel reverts cleanly.
  state.editValues = JSON.parse(JSON.stringify(row.fields || {}));
  state.deletedKeys = new Set();
  state.addingField = false;
  state.newKey = '';
  state.newType = 'string';
  state.newValue = '';
  paint();
}

function closeEditor() {
  state.editing = null;
  state.editValues = {};
  state.deletedKeys = new Set();
  state.addingField = false;
  state.saving = false;
  paint();
}

function deleteField(key) {
  delete state.editValues[key];
  state.deletedKeys.add(key);
  paint();
}

function startAddField(suggestion) {
  state.addingField = true;
  if (suggestion) {
    state.newKey = suggestion.key;
    state.newType = suggestion.type;
    state.newValue = suggestion.type === 'bool' ? false : (suggestion.type === 'number' ? 0 : '');
  } else {
    state.newKey = '';
    state.newType = 'string';
    state.newValue = '';
  }
  paint();
}

function commitAddField() {
  const key = String(state.newKey || '').trim();
  if (!key) { toast.error('Укажи имя поля'); return; }
  if (HIDDEN_FIELDS.has(key)) { toast.error('Это системное поле — управляется тумблерами карточки'); return; }
  // Coerce value to declared type.
  let v = state.newValue;
  if (state.newType === 'number') v = Number(v) || 0;
  if (state.newType === 'bool')   v = !!v;
  if (state.newType === 'json')   {
    try { v = typeof v === 'string' ? JSON.parse(v) : v; }
    catch (e) { toast.error('Невалидный JSON: ' + e.message); return; }
  }
  state.editValues[key] = v;
  // If admin had deleted this same key earlier, un-delete it.
  state.deletedKeys.delete(key);
  state.addingField = false;
  state.newKey = '';
  state.newType = 'string';
  state.newValue = '';
  paint();
}

async function saveEdit() {
  if (!state.editing) return;
  state.saving = true; paint();
  // Build patch — fields whose value differs from the original, plus any
  // keys explicitly deleted (sent as empty string so server clears them
  // when re-marshaling the TOML section).
  const original = state.editing.fields || {};
  const patch = {};
  for (const [k, v] of Object.entries(state.editValues)) {
    if (HIDDEN_FIELDS.has(k)) continue;
    if (JSON.stringify(original[k]) !== JSON.stringify(v)) {
      patch[k] = v;
    }
  }
  for (const k of state.deletedKeys) {
    if (HIDDEN_FIELDS.has(k)) continue;
    // TOML doesn't support omitting a key via patch; sending the empty
    // string is the conventional "clear" signal in this codebase.
    patch[k] = '';
  }
  if (Object.keys(patch).length === 0) {
    toast.info('Без изменений');
    closeEditor();
    return;
  }
  try {
    const res = await api.post('/balancers', { name: state.editing.name, fields: patch });
    if (res && res.error) throw new Error(res.error);
    // Patch local row so UI reflects new state without full reload.
    const nextFields = { ...(state.editing.fields || {}), ...patch };
    for (const k of state.deletedKeys) delete nextFields[k];
    state.editing.fields = nextFields;
    toast.success('Сохранено: ' + Object.keys(patch).join(', '));
    closeEditor();
  } catch (e) {
    toast.error(e.message);
    state.saving = false; paint();
  }
}

async function toggleStreamProxy(row) {
  const cur = row.fields && row.fields._stream_proxy !== false;
  const next = !cur;
  try {
    const res = await api.post('/balancers', { name: row.name, fields: { _stream_proxy: next } });
    if (res && res.error) throw new Error(res.error);
    toast.success('Stream proxy ' + (next ? 'ВКЛ' : 'ВЫКЛ') + ' для ' + row.name);
    row.fields = { ...(row.fields || {}), _stream_proxy: next };
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

function isEnabled(row) {
  const f = row.fields || {};
  // .NET admin can set both — `enable` takes precedence; fallback to `enabled`.
  if (Object.prototype.hasOwnProperty.call(f, 'enable')) return !!f.enable;
  if (Object.prototype.hasOwnProperty.call(f, 'enabled')) return !!f.enabled;
  return true;
}

function filtered() {
  const f = state.filter.trim().toLowerCase();
  return state.rows.filter(r => {
    if (state.group && (r.group || 'other') !== state.group) return false;
    if (!f) return true;
    return r.name.toLowerCase().includes(f);
  });
}

function paint() {
  const rows = filtered();
  const total = state.rows.length;
  const on = state.rows.filter(isEnabled).length;
  const off = total - on;

  // Group buckets for rendering.
  const buckets = {};
  for (const r of rows) {
    const g = r.group || 'other';
    if (!buckets[g]) buckets[g] = [];
    buckets[g].push(r);
  }
  const orderedGroups = Object.keys(buckets).sort((a, b) => {
    const order = ['working', 'ua', 'paid', 'search', 'blocked', 'dead', 'legacy', 'removed', 'other'];
    return order.indexOf(a) - order.indexOf(b);
  });

  const $root = document.getElementById('balancers-root');
  render(html`
    <div class="page-summary">
      <l-stat accent="blue"  label="Всего"     value=${total}></l-stat>
      <l-stat accent="green" label="Включены"  value=${on}></l-stat>
      <l-stat accent="pink"  label="Выключены" value=${off}></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="actions" style="display:flex;gap:8px;align-items:center;flex-wrap:wrap;">
        <l-input
          icon="🔎"
          placeholder="Найти балансер"
          .value=${state.filter}
          clearable
          @input=${e => { state.filter = e.detail.value; paint(); }}
          style="width:240px"
        ></l-input>
        <l-tabs
          .tabs=${[{ key: '', label: 'Все' }, ...state.groups.map(g => ({ key: g, label: GROUP_LABELS[g] || g }))]}
          .active=${state.group}
          @tab-change=${e => { state.group = e.detail.key; paint(); }}
        ></l-tabs>
        <l-button variant="secondary" icon="↻" @click=${async () => { await load(); paint(); }}>Обновить</l-button>
      </div>
      ${state.loading
        ? html`<div style="padding:24px;color:var(--text-2);text-align:center">Загрузка…</div>`
        : orderedGroups.length === 0
          ? html`<div style="padding:24px;color:var(--text-3);text-align:center">Ничего не найдено</div>`
          : orderedGroups.map(g => html`
            <div class="grp">
              <div class="grp-h">${GROUP_LABELS[g] || g} <span class="grp-c">${buckets[g].length}</span></div>
              <div class="grp-grid">
                ${buckets[g].map(r => balancerCard(r))}
              </div>
            </div>
          `)}
    </l-card>
    ${renderEditModal()}
  `, $root);
}

function balancerCard(r) {
  const en = isEnabled(r);
  const proxy = r.fields && r.fields._stream_proxy !== false;
  const proxyLabel = r.fields && r.fields._proxy_label;
  const quality = r.quality || '';
  const tag = r.status_tag || '';
  return html`
    <div class="b-card ${en ? '' : 'off'}">
      <div class="b-head">
        <div class="b-title">
          <span class="b-name">${r.name}</span>
          ${quality ? html`<span class="quality q-${quality.toLowerCase()}">${quality}</span>` : ''}
        </div>
        <label class="toggle">
          <input type="checkbox" ?checked=${en} @change=${() => toggle(r)}>
          <span class="track"><span class="thumb"></span></span>
        </label>
      </div>
      <div class="b-meta">
        ${tag ? html`<l-pill tone="muted">${tag}</l-pill>` : ''}
        ${proxyLabel ? html`<l-pill tone="info" title="${proxyLabel}">🌐 ${proxyLabel}</l-pill>` : ''}
      </div>
      <div class="b-row">
        <span class="b-row-l">Stream proxy</span>
        <label class="toggle small">
          <input type="checkbox" ?checked=${proxy} @change=${() => toggleStreamProxy(r)}>
          <span class="track"><span class="thumb"></span></span>
        </label>
      </div>
      <div class="b-edit-row">
        <l-button size="sm" variant="ghost" icon="⚙" @click=${() => openEditor(r)}>
          Настроить поля
        </l-button>
      </div>
    </div>
  `;
}

// ----- Field editor modal -----

function renderFieldInput(key, value, onInput) {
  const t = fieldType(key, value);
  if (t === 'bool') {
    return html`
      <label class="be-toggle">
        <input type="checkbox" ?checked=${!!value} @change=${e => onInput(e.target.checked)}>
        <span class="track"><span class="thumb"></span></span>
        <span class="be-bool-label">${value ? 'true' : 'false'}</span>
      </label>
    `;
  }
  if (t === 'number') {
    return html`<l-input type="number" .value=${String(value ?? '')} @input=${e => {
      const n = parseFloat(e.detail.value);
      onInput(Number.isFinite(n) ? n : 0);
    }}></l-input>`;
  }
  if (t === 'json') {
    let initial;
    try { initial = JSON.stringify(value, null, 2); } catch { initial = '""'; }
    return html`
      <textarea
        class="be-json"
        rows="4"
        spellcheck="false"
        .value=${initial}
        @blur=${e => {
          try {
            onInput(JSON.parse(e.target.value));
            e.target.classList.remove('bad');
          } catch (err) {
            e.target.classList.add('bad');
            toast.warn('Поле "' + key + '": невалидный JSON — ' + err.message);
          }
        }}
      ></textarea>
    `;
  }
  if (t === 'secret') {
    return html`<l-input type="password" .value=${String(value ?? '')} @input=${e => onInput(e.detail.value)}></l-input>`;
  }
  if (t.startsWith('enum:')) {
    const opts = t.slice(5).split(',').map(v => ({ value: v, label: v }));
    return html`<l-select .value=${String(value ?? '')} .options=${[{ value: '', label: '—' }, ...opts]}
                  @change=${e => onInput(e.detail.value || '')}></l-select>`;
  }
  // string default
  return html`<l-input .value=${String(value ?? '')} @input=${e => onInput(e.detail.value)}></l-input>`;
}

function renderEditModal() {
  if (!state.editing) return '';
  const row = state.editing;
  // Sort fields: lowercase alphabetical, but pinned order for known important ones.
  const PINNED = ['host', 'apiHost', 'listHost', 'linkHost', 'token', 'login', 'password',
                  'quality_badge', 'display_name', 'port', 'upstream_host'];
  const keys = Object.keys(state.editValues).filter(k => !HIDDEN_FIELDS.has(k));
  keys.sort((a, b) => {
    const ai = PINNED.indexOf(a), bi = PINNED.indexOf(b);
    if (ai !== -1 && bi !== -1) return ai - bi;
    if (ai !== -1) return -1;
    if (bi !== -1) return 1;
    return a.localeCompare(b);
  });
  // Suggestions: keys that are NOT already in editValues and weren't just deleted.
  const present = new Set(keys);
  const suggestions = SUGGESTED_FIELDS.filter(s =>
    !present.has(s.key) && !state.deletedKeys.has(s.key) && !HIDDEN_FIELDS.has(s.key)
  );
  return html`
    <l-modal ?open=${!!state.editing} title=${'Настройка: ' + (row.name || '')} @close=${closeEditor}>
      <div class="be-grid">
        ${keys.length === 0
          ? html`<div class="be-empty">У балансера нет полей в конфиге. Добавь ниже — ключ запишется в раздел <code>[${row.name}]</code> в <code>init.conf</code>.</div>`
          : keys.map(k => html`
              <div class="be-field">
                <label class="be-label">
                  <span class="be-key">${k}</span>
                  <span class="be-type">${fieldType(k, state.editValues[k])}</span>
                </label>
                <div class="be-input-row">
                  <div class="be-input-wrap">
                    ${renderFieldInput(k, state.editValues[k], v => { state.editValues[k] = v; })}
                  </div>
                  <button class="be-del" title="Удалить поле" @click=${() => deleteField(k)}>✕</button>
                </div>
              </div>
            `)}
      </div>

      ${state.addingField ? html`
        <div class="be-add-panel">
          <div class="be-add-row">
            <input class="be-add-key"
              placeholder="имя поля"
              .value=${state.newKey}
              @input=${e => { state.newKey = e.target.value; }}/>
            <select class="be-add-type" @change=${e => {
                state.newType = e.target.value;
                if (state.newType === 'bool') state.newValue = false;
                else if (state.newType === 'number') state.newValue = 0;
                else state.newValue = '';
                paint();
              }}>
              <option value="string" ?selected=${state.newType==='string'}>string</option>
              <option value="number" ?selected=${state.newType==='number'}>number</option>
              <option value="bool"   ?selected=${state.newType==='bool'}>bool</option>
              <option value="secret" ?selected=${state.newType==='secret'}>secret</option>
              <option value="json"   ?selected=${state.newType==='json'}>json</option>
            </select>
            <div class="be-add-val">
              ${state.newType === 'bool'
                ? html`<label class="be-toggle">
                    <input type="checkbox" ?checked=${!!state.newValue}
                      @change=${e => { state.newValue = e.target.checked; paint(); }}>
                    <span class="track"><span class="thumb"></span></span>
                  </label>`
                : html`<input class="be-add-input"
                    type=${state.newType === 'secret' ? 'password' : (state.newType === 'number' ? 'number' : 'text')}
                    placeholder="значение"
                    .value=${String(state.newValue ?? '')}
                    @input=${e => { state.newValue = state.newType === 'number' ? +e.target.value : e.target.value; }}/>`}
            </div>
            <button class="be-add-ok" @click=${commitAddField}>Добавить</button>
            <button class="be-add-cancel" @click=${() => { state.addingField = false; paint(); }}>✕</button>
          </div>
        </div>
      ` : html`
        <div class="be-add-zone">
          <button class="be-add-btn" @click=${() => startAddField()}>+ Добавить поле</button>
          ${suggestions.length ? html`
            <div class="be-suggestions">
              <span class="be-sug-label">или выбери из частых:</span>
              ${suggestions.slice(0, 12).map(s => html`
                <button class="be-sug" title=${s.hint} @click=${() => startAddField(s)}>
                  + ${s.key}
                </button>
              `)}
            </div>
          ` : ''}
        </div>
      `}

      ${state.deletedKeys.size > 0 ? html`
        <div class="be-deleted-note">
          К удалению: ${[...state.deletedKeys].map(k => html`<code>${k}</code> `)}
          <button class="be-undo-all" @click=${() => { state.deletedKeys = new Set(); paint(); }}>Отменить все</button>
        </div>
      ` : ''}

      <div slot="actions">
        <l-button variant="ghost" @click=${closeEditor}>Отмена</l-button>
        <l-button variant="primary" icon="💾" @click=${saveEdit} ?loading=${state.saving}>Сохранить</l-button>
      </div>
    </l-modal>
  `;
}

// Page-scoped styles, injected once.
const styleId = 'l-balancers-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr; } }
    .grp { margin-top: var(--s-4); }
    .grp-h {
      font-family: var(--font-display); font-size: var(--fs-md); font-weight: 600;
      margin-bottom: var(--s-3); color: var(--text-1);
      display: flex; align-items: center; gap: 8px;
    }
    .grp-c {
      font-size: var(--fs-xs); font-weight: 600; color: var(--text-3);
      background: var(--bg-3); padding: 1px 8px; border-radius: var(--r-pill);
    }
    .grp-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
      gap: var(--s-3);
    }
    .b-card {
      padding: var(--s-3);
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      display: flex; flex-direction: column; gap: 8px;
      transition: border-color var(--t-fast), opacity var(--t-fast);
    }
    .b-card:hover { border-color: var(--border-3); }
    .b-card.off { opacity: 0.55; }
    .b-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
    .b-title { display: flex; align-items: center; gap: 8px; min-width: 0; }
    .b-name {
      font-family: var(--font-display); font-weight: 600; font-size: var(--fs-md);
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .b-meta { display: flex; gap: 6px; flex-wrap: wrap; min-height: 22px; }
    .b-row {
      display: flex; align-items: center; justify-content: space-between;
      padding-top: 6px; border-top: 1px solid var(--border-1);
      font-size: var(--fs-xs); color: var(--text-2);
    }
    /* Apple-style switch */
    .toggle { display: inline-block; cursor: pointer; }
    .toggle input { display: none; }
    .toggle .track {
      display: inline-block; width: 40px; height: 22px;
      background: var(--bg-4); border-radius: var(--r-pill);
      position: relative; transition: background var(--t-fast) var(--ease-out);
    }
    .toggle .thumb {
      position: absolute; top: 2px; left: 2px;
      width: 18px; height: 18px; border-radius: 50%;
      background: white; box-shadow: 0 1px 3px rgba(0,0,0,0.35);
      transition: transform var(--t-fast) var(--ease-spring);
    }
    .toggle input:checked + .track {
      background: linear-gradient(135deg, var(--success), #06b6d4);
    }
    .toggle input:checked + .track .thumb { transform: translateX(18px); }
    .toggle.small .track { width: 32px; height: 18px; }
    .toggle.small .thumb { width: 14px; height: 14px; }
    .toggle.small input:checked + .track .thumb { transform: translateX(14px); }

    .quality {
      display: inline-block; padding: 1px 6px; border-radius: 4px;
      font-size: 10px; font-weight: 700; letter-spacing: 0.04em;
      background: rgba(255,255,255,0.06); color: var(--text-1);
    }
    .quality.q-4k  { background: rgba(236,72,153,0.18); color: #f9a8d4; }
    .quality.q-fhd { background: rgba(96,165,250,0.18); color: #93c5fd; }
    .quality.q-hd  { background: rgba(52,211,153,0.18); color: #6ee7b7; }
    .quality.q-sd  { background: rgba(148,163,184,0.18); color: #cbd5e1; }

    /* Per-balancer field editor (modal). */
    .b-edit-row {
      display: flex; justify-content: flex-end;
      padding-top: 6px; border-top: 1px solid var(--border-1);
    }
    .be-grid {
      display: grid; gap: var(--s-3);
      max-height: 60vh; overflow-y: auto;
      padding-right: 4px;
    }
    .be-empty { padding: 24px; text-align: center; color: var(--text-3); }
    .be-field { display: grid; gap: 4px; }
    .be-label {
      display: flex; justify-content: space-between; align-items: baseline;
      font-size: var(--fs-xs); color: var(--text-2);
    }
    .be-key {
      font-family: var(--font-mono); font-weight: 600; color: var(--text-0);
    }
    .be-type {
      font-size: 9px; text-transform: uppercase; letter-spacing: 0.06em;
      color: var(--text-3); padding: 1px 6px; border-radius: var(--r-pill);
      background: var(--bg-3);
    }
    .be-json {
      width: 100%; resize: vertical; min-height: 60px;
      background: var(--bg-0); border: 1px solid var(--border-2);
      border-radius: var(--r-2); padding: 8px 10px;
      color: var(--text-0);
      font-family: var(--font-mono); font-size: var(--fs-xs); line-height: 1.45;
      transition: border-color var(--t-fast), box-shadow var(--t-fast);
    }
    .be-json:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
    .be-json.bad { border-color: var(--danger); box-shadow: 0 0 0 3px var(--danger-soft); }

    .be-toggle { display: inline-flex; align-items: center; gap: 10px; cursor: pointer; }
    .be-toggle input { display: none; }
    .be-toggle .track {
      display: inline-block; width: 40px; height: 22px;
      background: var(--bg-4); border-radius: var(--r-pill);
      position: relative; transition: background var(--t-fast) var(--ease-out);
    }
    .be-toggle .thumb {
      position: absolute; top: 2px; left: 2px;
      width: 18px; height: 18px; border-radius: 50%;
      background: white; box-shadow: 0 1px 3px rgba(0,0,0,0.35);
      transition: transform var(--t-fast) var(--ease-spring);
    }
    .be-toggle input:checked + .track {
      background: linear-gradient(135deg, var(--success), #06b6d4);
    }
    .be-toggle input:checked + .track .thumb { transform: translateX(18px); }
    .be-bool-label {
      font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-2);
    }

    /* Field row with inline delete button */
    .be-input-row { display: flex; gap: 8px; align-items: center; }
    .be-input-wrap { flex: 1; min-width: 0; }
    .be-del {
      background: transparent; color: var(--text-3);
      border: 1px solid var(--border-2);
      width: 28px; height: 28px; flex-shrink: 0;
      border-radius: var(--r-2); cursor: pointer; font-size: 12px;
      transition: background 120ms, color 120ms, border-color 120ms;
    }
    .be-del:hover { background: var(--danger); color: white; border-color: transparent; }

    /* Add-field zone (idle state — button + suggestion chips) */
    .be-add-zone {
      margin-top: var(--s-3); padding: var(--s-3);
      border: 1px dashed var(--border-2);
      border-radius: var(--r-3);
      background: rgba(255,255,255,0.015);
    }
    .be-add-btn {
      background: var(--accent-soft); color: var(--accent);
      border: 1px solid rgba(119,145,255,0.32);
      padding: 6px 14px; border-radius: var(--r-pill);
      cursor: pointer; font-weight: var(--fw-semibold); font-size: var(--fs-sm);
      transition: background 120ms, color 120ms;
    }
    .be-add-btn:hover { background: var(--accent); color: white; }

    .be-suggestions { display: flex; flex-wrap: wrap; gap: 6px; margin-top: var(--s-2); align-items: center; }
    .be-sug-label { font-size: var(--fs-xs); color: var(--text-3); margin-right: 4px; }
    .be-sug {
      background: var(--bg-2); color: var(--text-1);
      border: 1px solid var(--border-1);
      padding: 3px 10px; font-size: var(--fs-xs); border-radius: var(--r-pill);
      font-family: var(--font-mono); cursor: pointer;
      transition: background 120ms, color 120ms, border-color 120ms;
    }
    .be-sug:hover { background: var(--accent-soft); color: var(--accent); border-color: rgba(119,145,255,0.32); }

    /* Add-field zone (editing state — composer row) */
    .be-add-panel {
      margin-top: var(--s-3); padding: var(--s-3);
      border: 1px solid var(--accent);
      border-radius: var(--r-3);
      background: rgba(119,145,255,0.05);
      box-shadow: 0 0 0 3px var(--accent-soft);
    }
    .be-add-row { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
    .be-add-key {
      flex: 1; min-width: 140px;
      background: var(--bg-1); border: 1px solid var(--border-2);
      padding: 7px 10px; border-radius: var(--r-2);
      color: var(--text-0); font-family: var(--font-mono); font-size: 13px;
    }
    .be-add-type {
      background: var(--bg-1); border: 1px solid var(--border-2);
      padding: 7px 8px; border-radius: var(--r-2);
      color: var(--text-0); font-size: var(--fs-sm); cursor: pointer;
    }
    .be-add-val { flex: 1.5; min-width: 140px; }
    .be-add-input {
      width: 100%; background: var(--bg-1); border: 1px solid var(--border-2);
      padding: 7px 10px; border-radius: var(--r-2);
      color: var(--text-0); font-size: 13px;
    }
    .be-add-input:focus, .be-add-key:focus, .be-add-type:focus {
      outline: none; border-color: var(--accent);
      box-shadow: 0 0 0 3px var(--accent-soft);
    }
    .be-add-ok {
      background: var(--g-accent); color: white; border: 0;
      padding: 7px 14px; border-radius: var(--r-pill);
      font-weight: var(--fw-semibold); cursor: pointer;
    }
    .be-add-cancel {
      background: transparent; color: var(--text-2);
      border: 1px solid var(--border-2);
      padding: 5px 10px; border-radius: var(--r-pill); cursor: pointer;
    }

    /* Deleted-keys preview row */
    .be-deleted-note {
      margin-top: var(--s-3); padding: 8px 12px;
      border-left: 3px solid var(--danger);
      background: var(--danger-soft);
      border-radius: var(--r-2);
      font-size: var(--fs-xs); color: var(--text-1);
      display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
    }
    .be-deleted-note code {
      background: rgba(255,107,122,0.18); color: #ffb3b9;
      padding: 1px 6px; border-radius: 4px; font-family: var(--font-mono);
    }
    .be-undo-all {
      margin-left: auto;
      background: transparent; color: var(--danger);
      border: 1px solid rgba(255,107,122,0.32);
      padding: 3px 10px; font-size: var(--fs-xs); border-radius: var(--r-pill);
      cursor: pointer; font-weight: var(--fw-semibold);
    }
    .be-undo-all:hover { background: var(--danger); color: white; }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="balancers-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
