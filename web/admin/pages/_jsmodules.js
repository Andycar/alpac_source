// _jsmodules.js — shared factory for the three jsmodules-shaped admin pages
// (/api/modules, /api/sisi-sources, /api/music-sources). They serve different
// directories but share the same DTO + action set:
//
//   GET    {root}                  → moduleDTO[]
//   POST   {root}/install   {url}  → install/refresh from URL
//   POST   {root}/{id}/toggle      → enable/disable
//   POST   {root}/{id}/reload      → re-eval source
//   DELETE {root}/{id}             → remove
//
// Callers pass:
//   apiRoot:  string (e.g. '/modules', '/sisi-sources', '/music-sources')
//   title:    main page title (shown in toast, "Найти X")
//   icon:     hero icon
//   emptyMsg: shown when the list is empty

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

export function createJsModulesPage({ apiRoot, title, icon = '🧱', emptyMsg, installPrompt }) {
  const state = {
    modules: [],
    filter: '',
    loading: true,
    installing: false,
    // Per-module config editor
    editing: null,        // { id, schema, draft }
    saving: false,
  };
  const containerId = 'jsmod-root-' + apiRoot.replace(/[^a-z0-9]/gi, '_');

  async function load() {
    state.loading = true;
    try {
      const list = await api.get(apiRoot);
      state.modules = Array.isArray(list) ? list : [];
    } catch (e) {
      toast.error('Не удалось загрузить ' + title + ': ' + e.message);
    } finally {
      state.loading = false;
    }
  }

  async function actionPost(id, action, success) {
    try {
      const r = await api.post(`${apiRoot}/${encodeURIComponent(id)}/${action}`);
      if (r && r.error) throw new Error(r.error);
      toast.success(success || 'OK');
      await load();
      paint();
    } catch (e) { toast.error(e.message); }
  }

  async function remove(id) {
    if (!confirm(`Удалить "${id}"?\nФайл будет стёрт.`)) return;
    try {
      const r = await api.del(`${apiRoot}/${encodeURIComponent(id)}`);
      if (r && r.error) throw new Error(r.error);
      toast.success('Удалён');
      await load();
      paint();
    } catch (e) { toast.error(e.message); }
  }

  function openConfigEditor(m) {
    if (!Array.isArray(m.config_schema) || m.config_schema.length === 0) {
      toast.info('У модуля «' + (m.name || m.id) + '» нет настраиваемых параметров');
      return;
    }
    state.editing = {
      id: m.id,
      name: m.name || m.id,
      schema: m.config_schema,
      // Deep clone current values so cancel reverts cleanly.
      draft: JSON.parse(JSON.stringify(m.config || {})),
    };
    paint();
  }

  function closeConfigEditor() {
    state.editing = null;
    state.saving = false;
    paint();
  }

  async function saveConfig() {
    if (!state.editing) return;
    const { id, schema, draft } = state.editing;
    // Coerce values according to schema type before sending.
    const payload = {};
    for (const f of schema) {
      const v = draft[f.key];
      switch (f.type) {
        case 'int':
        case 'number':
          payload[f.key] = (v === '' || v == null) ? 0 : Number(v);
          break;
        case 'bool':
          payload[f.key] = !!v;
          break;
        default:
          payload[f.key] = v == null ? '' : v;
      }
    }
    state.saving = true; paint();
    try {
      const r = await api.put(`${apiRoot}/${encodeURIComponent(id)}/config`, payload);
      if (r && r.ok === false) throw new Error(r.error || 'save failed');
      if (r && r.error) throw new Error(r.error);
      toast.success('Конфиг сохранён');
      await load();
      closeConfigEditor();
    } catch (e) {
      toast.error(e.message);
      state.saving = false; paint();
    }
  }

  async function installFromURL() {
    const url = prompt(installPrompt || 'URL манифеста (.json) или JS-источника:');
    if (!url || !url.trim()) return;
    state.installing = true; paint();
    try {
      const r = await api.post(`${apiRoot}/install`, { url: url.trim() });
      if (r && r.error) throw new Error(r.error);
      toast.success('Установлен' + (r && r.id ? ': ' + r.id : ''));
      await load();
    } catch (e) {
      toast.error('Установка не удалась: ' + e.message);
    } finally {
      state.installing = false; paint();
    }
  }

  function renderConfigInput(field, value, onChange) {
    const placeholder = field.placeholder || (field.default != null ? String(field.default) : '');
    if (field.type === 'bool') {
      return html`
        <label class="jm-toggle">
          <input type="checkbox" ?checked=${!!value} @change=${e => onChange(e.target.checked)}>
          <span class="track"><span class="thumb"></span></span>
          <span class="jm-bool-label">${value ? 'true' : 'false'}</span>
        </label>
      `;
    }
    if (field.type === 'int' || field.type === 'number') {
      return html`<l-input type="number" placeholder=${placeholder} .value=${String(value ?? '')}
                           @input=${e => onChange(e.detail.value)}></l-input>`;
    }
    if (field.type === 'enum' && Array.isArray(field.options) && field.options.length) {
      return html`<l-select .value=${String(value ?? '')}
                            .options=${[{value: '', label: '—'}, ...field.options.map(v => ({value: v, label: v}))]}
                            @change=${e => onChange(e.detail.value)}></l-select>`;
    }
    if (field.type === 'textarea') {
      return html`
        <textarea class="jm-textarea" rows="4" placeholder=${placeholder}
                  .value=${String(value ?? '')}
                  @input=${e => onChange(e.target.value)}></textarea>
      `;
    }
    if (field.type === 'secret' || field.secret) {
      return html`<l-input type="password" placeholder=${placeholder} .value=${String(value ?? '')}
                           @input=${e => onChange(e.detail.value)}></l-input>`;
    }
    return html`<l-input placeholder=${placeholder} .value=${String(value ?? '')}
                         @input=${e => onChange(e.detail.value)}></l-input>`;
  }

  function renderConfigEditor() {
    if (!state.editing) return '';
    const ed = state.editing;
    return html`
      <l-modal ?open=${!!state.editing} title=${'Настройки: ' + ed.name} @close=${closeConfigEditor}>
        <div class="jm-form">
          ${ed.schema.map(f => html`
            <div class="jm-field">
              <label class="jm-label">
                <span class="jm-key">${f.label || f.key}</span>
                <span class="jm-type">${f.type || 'string'}</span>
              </label>
              ${renderConfigInput(f, ed.draft[f.key], v => { ed.draft[f.key] = v; })}
              ${f.description ? html`<div class="jm-desc">${f.description}</div>` : ''}
            </div>
          `)}
        </div>
        <div slot="actions">
          <l-button variant="ghost" @click=${closeConfigEditor}>Отмена</l-button>
          <l-button variant="primary" icon="💾" @click=${saveConfig} ?loading=${state.saving}>Сохранить</l-button>
        </div>
      </l-modal>
    `;
  }

  function filtered() {
    const f = state.filter.trim().toLowerCase();
    if (!f) return state.modules;
    return state.modules.filter(m =>
      (m.id || '').toLowerCase().includes(f)
      || (m.name || '').toLowerCase().includes(f)
      || (m.author || '').toLowerCase().includes(f)
      || (m.description || '').toLowerCase().includes(f)
      || (m.tags || []).some(t => t.toLowerCase().includes(f))
    );
  }

  function paint() {
    const $root = document.getElementById(containerId);
    if (!$root) return;
    const rows = filtered();
    const total = state.modules.length;
    const on = state.modules.filter(m => m.enabled).length;
    const errs = state.modules.filter(m => m.error).length;

    render(html`
      <div class="page-summary">
        <l-stat accent="blue"  label="Всего"      value=${total}></l-stat>
        <l-stat accent="green" label="Включены"   value=${on}></l-stat>
        <l-stat accent="pink"  label="С ошибкой"  value=${errs}></l-stat>
      </div>

      <l-card style="margin-top:var(--s-5)">
        <div slot="actions" style="display:flex;gap:8px;align-items:center;">
          <l-input
            size="sm" icon="🔎"
            placeholder=${'Найти ' + title.toLowerCase()}
            .value=${state.filter}
            clearable
            @input=${e => { state.filter = e.detail.value; paint(); }}
            style="width:280px"
          ></l-input>
          <l-button variant="primary" icon="＋" @click=${installFromURL} ?loading=${state.installing}>Установить</l-button>
          <l-button variant="secondary" icon="↻" @click=${async () => { await load(); paint(); }}>Обновить</l-button>
        </div>

        ${state.loading
          ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
          : rows.length === 0
            ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">${state.filter ? 'Ничего не найдено' : (emptyMsg || 'Нет установленных. Нажмите «Установить».')}</div>`
            : html`<div class="m-grid">${rows.map(m => moduleCard(m, actionPost, remove, openConfigEditor))}</div>`}
      ${renderConfigEditor()}
      </l-card>
    `, $root);
  }

  return {
    async render($mount) {
      $mount.innerHTML = `<div id="${containerId}"></div>`;
      paint();
      await load();
      paint();
    },
  };
}

function moduleCard(m, actionPost, remove, openConfigEditor) {
  const hasConfig = Array.isArray(m.config_schema) && m.config_schema.length > 0;
  const stats = m.stats || {};
  const reqs = stats.requests || stats.Requests || 0;
  const errs = stats.errors || stats.Errors || 0;
  return html`
    <div class="m-card ${m.enabled ? '' : 'off'} ${m.error ? 'has-err' : ''}">
      <div class="m-head">
        <div class="m-icon">${m.icon || '◆'}</div>
        <div class="m-meta">
          <div class="m-name">${m.name || m.id}</div>
          <div class="m-sub">${m.author ? `${m.author} · ` : ''}v${m.version || '0.0.0'}</div>
        </div>
        <label class="toggle">
          <input type="checkbox" ?checked=${m.enabled} @change=${() => actionPost(m.id, 'toggle', m.enabled ? 'Выключен' : 'Включён')}>
          <span class="track"><span class="thumb"></span></span>
        </label>
      </div>
      ${m.description ? html`<div class="m-desc">${m.description}</div>` : ''}
      <div class="m-chips">
        ${m.quality ? html`<span class="quality q-${m.quality.toLowerCase()}">${m.quality}</span>` : ''}
        ${m.anime ? html`<l-pill tone="info">anime</l-pill>` : ''}
        ${m.ukrainian ? html`<l-pill tone="info">UA</l-pill>` : ''}
        ${(m.content_types || []).slice(0, 3).map(t => html`<l-pill tone="muted">${t}</l-pill>`)}
        ${(m.tags || []).slice(0, 3).map(t => html`<l-pill tone="muted">#${t}</l-pill>`)}
      </div>
      ${m.error ? html`<div class="m-err">⚠ ${m.error}</div>` : ''}
      <div class="m-foot">
        <div class="m-stats">
          ${reqs ? html`<span>${reqs} req</span>` : ''}
          ${errs ? html`<span style="color:var(--danger)">${errs} err</span>` : ''}
        </div>
        <div class="m-acts">
          ${hasConfig
            ? html`<l-button size="sm" variant="ghost" title="Настройки" @click=${() => openConfigEditor(m)}>⚙</l-button>`
            : ''}
          <l-button size="sm" variant="ghost" title="Перезагрузить" @click=${() => actionPost(m.id, 'reload', 'Перезагружен')}>↻</l-button>
          <l-button size="sm" variant="danger" @click=${() => remove(m.id)}>×</l-button>
        </div>
      </div>
    </div>
  `;
}

// Page-scoped styles — idempotent via id-check (one stylesheet for all 3 pages).
const styleId = 'l-jsmodules-shared-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr; } }
    .m-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
      gap: var(--s-3);
    }
    .m-card {
      padding: var(--s-3);
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      display: flex; flex-direction: column; gap: 8px;
      transition: border-color var(--t-fast), opacity var(--t-fast), background var(--t-fast);
    }
    .m-card:hover { border-color: var(--border-3); }
    .m-card.off { opacity: 0.55; }
    .m-card.has-err { border-color: rgba(248,113,113,0.45); background: rgba(248,113,113,0.04); }
    .m-head { display: flex; align-items: center; gap: 10px; }
    .m-icon {
      width: 40px; height: 40px; border-radius: var(--r-2);
      background: linear-gradient(135deg, rgba(108,140,255,0.22), rgba(141,107,255,0.12));
      display: grid; place-items: center;
      font-size: 18px; box-shadow: var(--shadow-sm); flex-shrink: 0;
    }
    .m-meta { flex: 1; min-width: 0; }
    .m-name {
      font-family: var(--font-display); font-weight: 600;
      font-size: var(--fs-md); letter-spacing: -0.01em;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .m-sub { font-size: var(--fs-xs); color: var(--text-3); margin-top: 1px; }
    .m-desc {
      font-size: var(--fs-sm); color: var(--text-1); line-height: 1.45;
      overflow: hidden; text-overflow: ellipsis;
      display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical;
    }
    .m-chips { display: flex; gap: 6px; flex-wrap: wrap; min-height: 22px; }
    .m-err {
      font-size: var(--fs-xs); font-family: var(--font-mono);
      color: #fca5a5; background: var(--danger-soft);
      padding: 6px 10px; border-radius: var(--r-2);
      overflow: hidden; text-overflow: ellipsis;
      display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
    }
    .m-foot {
      display: flex; align-items: center; justify-content: space-between;
      padding-top: var(--s-2); border-top: 1px solid var(--border-1);
    }
    .m-stats { display: flex; gap: 12px; font-size: var(--fs-xs); color: var(--text-2); font-variant-numeric: tabular-nums; }
    .m-acts { display: flex; gap: 4px; }
    .quality {
      display: inline-block; padding: 1px 6px; border-radius: 4px;
      font-size: 10px; font-weight: 700; letter-spacing: 0.04em;
      background: rgba(255,255,255,0.06); color: var(--text-1);
    }
    .quality.q-4k  { background: rgba(236,72,153,0.18); color: #f9a8d4; }
    .quality.q-fhd { background: rgba(96,165,250,0.18); color: #93c5fd; }
    .quality.q-hd  { background: rgba(52,211,153,0.18); color: #6ee7b7; }
    .quality.q-sd  { background: rgba(148,163,184,0.18); color: #cbd5e1; }
    .toggle { display: inline-block; cursor: pointer; flex-shrink: 0; }
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

    /* Per-module config editor (jsmodules / sisi / music). */
    .jm-form { display: flex; flex-direction: column; gap: var(--s-3); max-height: 60vh; overflow-y: auto; padding-right: 4px; }
    .jm-field { display: flex; flex-direction: column; gap: 4px; }
    .jm-label {
      display: flex; justify-content: space-between; align-items: baseline;
      font-size: var(--fs-xs); color: var(--text-2);
    }
    .jm-key { font-weight: 600; color: var(--text-0); }
    .jm-type {
      font-size: 9px; text-transform: uppercase; letter-spacing: 0.06em;
      color: var(--text-3); padding: 1px 6px; border-radius: var(--r-pill);
      background: var(--bg-3); font-family: var(--font-mono);
    }
    .jm-desc { font-size: var(--fs-xs); color: var(--text-3); line-height: 1.4; }
    .jm-textarea {
      width: 100%; resize: vertical; min-height: 80px;
      background: var(--bg-0); border: 1px solid var(--border-2);
      border-radius: var(--r-2); padding: 8px 10px;
      color: var(--text-0); font-family: var(--font-mono); font-size: var(--fs-xs);
      line-height: 1.45;
    }
    .jm-textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
    .jm-toggle { display: inline-flex; align-items: center; gap: 10px; cursor: pointer; }
    .jm-toggle input { display: none; }
    .jm-toggle .track {
      display: inline-block; width: 40px; height: 22px;
      background: var(--bg-4); border-radius: var(--r-pill);
      position: relative; transition: background var(--t-fast);
    }
    .jm-toggle .thumb {
      position: absolute; top: 2px; left: 2px; width: 18px; height: 18px; border-radius: 50%;
      background: white; box-shadow: 0 1px 3px rgba(0,0,0,0.35);
      transition: transform var(--t-fast) var(--ease-spring);
    }
    .jm-toggle input:checked + .track {
      background: linear-gradient(135deg, var(--success), #06b6d4);
    }
    .jm-toggle input:checked + .track .thumb { transform: translateX(18px); }
    .jm-bool-label { font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-2); }
  `;
  document.head.appendChild(s);
}
