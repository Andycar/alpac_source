// pages/groups.js — User groups CRUD + per-group balancer access matrix.
//
// API:
//   GET  /api/groups → { groups: [...], users_count: { group_id: n } }
//   POST /api/groups → { action: 'create'|'update'|'delete'|'set_default', ...fields }
//
// Group object: { id, name, description, max_devices, torrserver, sisi,
//                 is_default, balancers: { name: true|false } }
// Balancers map semantics: empty/null = all allowed; keys = explicit list,
// false = denied.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = { groups: [], users_count: {}, balancers: [], torrservers: [] };
let editing = null;          // group being edited or null
let creating = false;        // toggled by "+ Новая" button
let confirmDelete = null;    // group ID pending delete confirm

async function fetchState() {
  const [g, b, tb] = await Promise.all([
    api.groups(),
    api.balancers().catch(() => []),
    api.torrbalancer().catch(() => ({ backends: [] })),
  ]);
  state.groups = g.groups || [];
  state.users_count = g.users_count || {};
  // balancers can be an array or {items: [...]} depending on legacy/v2 shape
  const balRows = Array.isArray(b) ? b : (b?.items || b?.balancers || []);
  state.balancers = balRows
    .map(x => x.name || x.id || x)
    .filter(Boolean)
    .sort();
  // TorrServer backends for the per-group server matrix ({id,name}).
  state.torrservers = ((tb && tb.backends) || [])
    .map(x => ({ id: x.id, name: x.name || x.host || x.id }))
    .filter(x => x.id);
}

function paint($mount) {
  const groups = state.groups;
  const defaultID = (groups.find(g => g.is_default) || {}).id;

  render(html`
    <section class="hero">
      <h1>Группы пользователей</h1>
      <p>Уровни доступа: device-лимиты, TorrServer/SISI toggles, поэлементный whitelist балансеров. По умолчанию новый пользователь попадает в default-группу.</p>
      <div class="hero-actions">
        <button class="btn-cta" @click=${() => { creating = true; editing = { id: '', name: '', description: '', max_devices: 3, torrserver: true, sisi: true, balancers: {}, torrservers: {}, strict_balancers: false }; paint($mount); }}>+ Новая группа</button>
        <span class="chip accent">${groups.length} групп</span>
        <span class="chip">пользователей всего: ${Object.values(state.users_count).reduce((a,b)=>a+(b||0),0)}</span>
      </div>
    </section>

    ${editing ? renderEditor($mount, editing) : ''}

    <div class="grid cols-3">
      ${groups.map(g => renderGroupCard($mount, g, defaultID))}
    </div>

    ${confirmDelete ? renderConfirmDelete($mount) : ''}

    <style>
      .btn-cta {
        background: var(--g-accent); color: white; border: 0;
        padding: 9px 18px; border-radius: var(--r-pill);
        font-weight: var(--fw-semibold); cursor: pointer; font-size: var(--fs-sm);
        box-shadow: var(--shadow-md);
        transition: transform 140ms var(--ease-spring), box-shadow 200ms;
      }
      .btn-cta:hover { transform: translateY(-1px); box-shadow: var(--shadow-glow); }

      .group-head { display: flex; align-items: center; gap: var(--s-3); margin-bottom: var(--s-3); }
      .group-badge {
        width: 44px; height: 44px;
        border-radius: var(--r-3);
        background: var(--g-accent);
        display: grid; place-items: center;
        font-weight: var(--fw-bold);
        color: white; font-size: 18px;
        box-shadow: var(--shadow-sm);
      }
      .group-badge.violet { background: var(--g-accent); }
      .group-badge.mint   { background: var(--g-mint); color: #0c1119; }
      .group-badge.cyan   { background: var(--g-cyan); color: #0c1119; }
      .group-badge.pink   { background: var(--g-fire); }
      .group-badge.amber  { background: var(--g-amber); color: #0c1119; }
      .group-title { font-family: var(--font-display); font-weight: var(--fw-semibold); font-size: var(--fs-lg); letter-spacing: -0.01em; line-height: 1.1; }
      .group-id    { font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-2); margin-top: 2px; }
      .group-desc  { color: var(--text-1); font-size: var(--fs-sm); line-height: 1.5; min-height: 36px; }

      .group-pills { display: flex; flex-wrap: wrap; gap: 6px; margin: var(--s-3) 0; }

      .group-stat-row { display: flex; justify-content: space-between; align-items: baseline; padding: 6px 0; border-top: 1px dashed var(--border-1); font-size: var(--fs-sm); }
      .group-stat-row:first-of-type { border-top: 0; }
      .group-stat-row .v { font-family: var(--font-mono); font-weight: var(--fw-semibold); color: var(--text-0); }
      .group-stat-row .l { color: var(--text-2); }

      .group-actions { display: flex; gap: 6px; margin-top: var(--s-3); flex-wrap: wrap; }
      .btn-small {
        background: var(--bg-2); color: var(--text-1);
        border: 1px solid var(--border-2);
        padding: 5px 12px; font-size: var(--fs-xs);
        border-radius: var(--r-pill); cursor: pointer;
        font-weight: var(--fw-semibold);
        transition: background 140ms, color 140ms, border-color 140ms;
      }
      .btn-small:hover { background: var(--bg-3); border-color: var(--border-3); }
      .btn-small.primary { background: var(--accent-soft); color: var(--accent); border-color: rgba(119,145,255,0.32); }
      .btn-small.primary:hover { background: var(--accent); color: white; }
      .btn-small.danger { color: var(--danger); border-color: rgba(255,107,122,0.32); }
      .btn-small.danger:hover { background: var(--danger); color: white; }

      .editor-overlay {
        position: fixed; inset: 0;
        background: var(--surface-overlay);
        backdrop-filter: blur(20px);
        z-index: var(--z-modal);
        display: grid; place-items: center;
        padding: var(--s-5);
        animation: fade-in var(--t-normal) var(--ease-spring) both;
      }
      .editor-panel {
        width: min(720px, 100%);
        max-height: 90vh;
        overflow-y: auto;
        background: var(--bg-1);
        border: 1px solid var(--border-2);
        border-radius: var(--r-4);
        box-shadow: var(--shadow-xl);
        position: relative;
      }
      .editor-head {
        padding: var(--s-5) var(--s-5) 0;
        background: radial-gradient(80% 100% at 0% 0%, rgba(119,145,255,0.14), transparent 70%);
      }
      .editor-title { font-family: var(--font-display); font-weight: var(--fw-bold); font-size: var(--fs-xl); letter-spacing: -0.02em; background: var(--g-text-accent); -webkit-background-clip: text; background-clip: text; color: transparent; }
      .editor-body { padding: var(--s-4) var(--s-5); }
      .editor-foot { padding: var(--s-3) var(--s-5) var(--s-5); display: flex; gap: var(--s-2); justify-content: flex-end; border-top: 1px solid var(--border-1); }

      .form-row { display: flex; flex-direction: column; gap: 4px; margin-bottom: var(--s-3); }
      .form-row label { font-size: var(--fs-sm); color: var(--text-2); }
      .form-row input[type=text], .form-row input[type=number], .form-row textarea {
        background: var(--bg-2); border: 1px solid var(--border-2);
        padding: 8px 10px; border-radius: var(--r-2);
        color: var(--text-0); font-size: var(--fs-base);
      }
      .form-row input:focus, .form-row textarea:focus { border-color: var(--accent); outline: none; box-shadow: 0 0 0 3px var(--accent-soft); }
      .form-row textarea { resize: vertical; min-height: 60px; font-family: var(--font-text); }
      .form-row.inline { flex-direction: row; align-items: center; gap: var(--s-3); }

      .switch { display: inline-flex; align-items: center; gap: 10px; cursor: pointer; user-select: none; }
      .switch input { display: none; }
      .switch-track {
        width: 38px; height: 22px;
        background: var(--bg-3);
        border-radius: var(--r-pill);
        position: relative;
        transition: background 200ms;
      }
      .switch-track::after {
        content: ''; position: absolute;
        top: 2px; left: 2px;
        width: 18px; height: 18px;
        border-radius: 50%; background: white;
        transition: transform 200ms var(--ease-spring);
        box-shadow: var(--shadow-sm);
      }
      .switch input:checked + .switch-track { background: var(--g-accent); }
      .switch input:checked + .switch-track::after { transform: translateX(16px); }

      .bal-matrix {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
        gap: 6px;
        max-height: 280px;
        overflow-y: auto;
        padding: var(--s-3);
        border: 1px solid var(--border-1);
        border-radius: var(--r-2);
        background: var(--bg-0);
      }
      .bal-toggle {
        display: flex; align-items: center; gap: 8px;
        padding: 5px 10px;
        border-radius: var(--r-2);
        background: var(--bg-2);
        font-size: var(--fs-sm);
        cursor: pointer;
        transition: background 120ms;
      }
      .bal-toggle.on { background: var(--accent-soft); color: var(--accent); }
      .bal-toggle:hover { background: var(--bg-3); }
      .bal-toggle.on:hover { background: rgba(119,145,255,0.28); }

      .confirm-overlay { position: fixed; inset: 0; background: var(--surface-overlay); backdrop-filter: blur(16px); z-index: var(--z-modal); display: grid; place-items: center; }
      .confirm-panel { width: min(420px, 90%); background: var(--bg-1); border: 1px solid var(--border-2); border-radius: var(--r-3); padding: var(--s-5); box-shadow: var(--shadow-xl); text-align: center; }
      .confirm-panel h3 { margin: 0 0 var(--s-2); font-family: var(--font-display); }
      .confirm-panel p  { margin: 0 0 var(--s-4); color: var(--text-2); }
      .confirm-foot { display: flex; gap: var(--s-2); justify-content: center; }
    </style>
  `, $mount);
}

function badgeHue(g) {
  if (g.is_default) return 'violet';
  const hues = ['cyan', 'mint', 'amber', 'pink', 'violet'];
  const hash = (g.id || '').split('').reduce((a,c) => a + c.charCodeAt(0), 0);
  return hues[hash % hues.length];
}

function renderGroupCard($mount, g, defaultID) {
  const users = state.users_count[g.id] || 0;
  const balCount = g.balancers ? Object.keys(g.balancers).filter(k => g.balancers[k]).length : 0;
  const balTotal = state.balancers.length;
  const allBalancers = !g.balancers || Object.keys(g.balancers).length === 0;
  const tsCount = g.torrservers ? Object.keys(g.torrservers).filter(k => g.torrservers[k]).length : 0;
  const tsTotal = state.torrservers.length;
  const allTS = !g.torrservers || Object.keys(g.torrservers).length === 0;
  const isDefault = g.id === defaultID;

  return html`
    <l-card accent=${isDefault ? 'violet' : ''}>
      <div class="group-head">
        <div class="group-badge ${badgeHue(g)}">${(g.name || g.id || '?').charAt(0).toUpperCase()}</div>
        <div>
          <div class="group-title">${g.name || g.id}</div>
          <div class="group-id">id: ${g.id} ${isDefault ? '· default' : ''}</div>
        </div>
      </div>
      <div class="group-desc">${g.description || html`<span style="color:var(--text-3)">Без описания</span>`}</div>
      <div class="group-pills">
        ${isDefault ? html`<span class="pill info"><span class="dot"></span> default</span>` : ''}
        <span class="pill ${g.torrserver ? 'ok' : 'muted'}"><span class="dot"></span> TorrServer</span>
        <span class="pill ${g.sisi ? 'ok' : 'muted'}"><span class="dot"></span> SISI</span>
        ${g.strict_balancers ? html`<span class="pill info"><span class="dot"></span> строгий</span>` : ''}
        <span class="pill muted">${users} польз.</span>
      </div>
      <div class="group-stat-row"><span class="l">Устройств / польз.</span><span class="v">${g.max_devices || '∞'}</span></div>
      <div class="group-stat-row"><span class="l">Балансеры</span><span class="v">${allBalancers ? 'все' : (g.strict_balancers ? `только ${balCount}` : `${balCount}/${balTotal}`)}</span></div>
      ${tsTotal > 0 ? html`<div class="group-stat-row"><span class="l">TS-серверы</span><span class="v">${allTS ? 'все' : `${tsCount}/${tsTotal}`}</span></div>` : ''}
      <div class="group-actions">
        <button class="btn-small primary" @click=${() => { editing = JSON.parse(JSON.stringify(g)); creating = false; paint($mount); }}>Редактировать</button>
        ${isDefault ? '' : html`<button class="btn-small" @click=${() => setDefault($mount, g.id)}>Сделать default</button>`}
        ${isDefault ? '' : html`<button class="btn-small danger" @click=${() => { confirmDelete = g; paint($mount); }}>Удалить</button>`}
      </div>
    </l-card>
  `;
}

function renderEditor($mount, g) {
  const balancers = state.balancers;
  const isNew = creating;
  return html`
    <div class="editor-overlay" @click=${(e) => { if (e.target.classList.contains('editor-overlay')) closeEditor($mount); }}>
      <div class="editor-panel">
        <div class="editor-head">
          <div class="editor-title">${isNew ? 'Новая группа' : `Редактирование: ${g.name || g.id}`}</div>
          <p style="color:var(--text-2);margin:4px 0 var(--s-4)">Permissions ниже определяют доступ всех пользователей в группе.</p>
        </div>
        <div class="editor-body">
          <div class="form-row">
            <label>Название</label>
            <input type="text" .value=${g.name || ''} @input=${(e) => g.name = e.target.value} placeholder="Premium"/>
          </div>
          <div class="form-row">
            <label>Описание (опционально)</label>
            <textarea @input=${(e) => g.description = e.target.value}>${g.description || ''}</textarea>
          </div>
          <div class="form-row">
            <label>Максимум устройств на пользователя</label>
            <input type="number" min="1" max="100" .value=${g.max_devices || 3} @input=${(e) => g.max_devices = +e.target.value}/>
          </div>
          <div style="display:flex;gap:var(--s-5);margin: var(--s-3) 0 var(--s-4)">
            <label class="switch">
              <input type="checkbox" ?checked=${g.torrserver !== false} @change=${(e) => g.torrserver = e.target.checked}/>
              <span class="switch-track"></span>
              <span>TorrServer</span>
            </label>
            <label class="switch">
              <input type="checkbox" ?checked=${g.sisi !== false} @change=${(e) => g.sisi = e.target.checked}/>
              <span class="switch-track"></span>
              <span>SISI</span>
            </label>
            <label class="switch" title="ON: видны ТОЛЬКО отмеченные балансеры (allowlist). OFF: видны все, кроме явно снятых — отметки не ограничивают.">
              <input type="checkbox" ?checked=${!!g.strict_balancers} @change=${(e) => { g.strict_balancers = e.target.checked; paint($mount); }}/>
              <span class="switch-track"></span>
              <span>Строгий список</span>
            </label>
          </div>
          <div class="form-row">
            <label>${g.strict_balancers
              ? 'Балансеры — доступны ТОЛЬКО отмеченные (строгий список)'
              : 'Балансеры — строгий список выкл: отметки НЕ ограничивают, доступны все'}</label>
            <div class="bal-matrix">
              ${balancers.map(b => {
                const on = !!(g.balancers && g.balancers[b]);
                return html`
                  <div class="bal-toggle ${on ? 'on' : ''}" @click=${() => toggleBal($mount, g, b)}>
                    <span style="font-size:14px">${on ? '✓' : '○'}</span>
                    <span>${b}</span>
                  </div>
                `;
              })}
            </div>
          </div>
          ${state.torrservers.length ? html`
          <div class="form-row" style=${g.torrserver === false ? 'opacity:0.5;pointer-events:none' : ''}>
            <label>ТорСервер-серверы (если ни один не выбран — доступны все)</label>
            <div class="bal-matrix">
              ${state.torrservers.map(ts => {
                const on = !!(g.torrservers && g.torrservers[ts.id]);
                return html`
                  <div class="bal-toggle ${on ? 'on' : ''}" @click=${() => toggleTS($mount, g, ts.id)}>
                    <span style="font-size:14px">${on ? '✓' : '○'}</span>
                    <span>${ts.name}</span>
                  </div>
                `;
              })}
            </div>
          </div>` : ''}
        </div>
        <div class="editor-foot">
          <button class="btn-small" @click=${() => closeEditor($mount)}>Отмена</button>
          <button class="btn-small primary" @click=${() => saveGroup($mount, g, isNew)}>${isNew ? 'Создать' : 'Сохранить'}</button>
        </div>
      </div>
    </div>
  `;
}

function renderConfirmDelete($mount) {
  const g = confirmDelete;
  return html`
    <div class="confirm-overlay" @click=${(e) => { if (e.target.classList.contains('confirm-overlay')) { confirmDelete = null; paint($mount); } }}>
      <div class="confirm-panel">
        <h3>Удалить группу «${g.name || g.id}»?</h3>
        <p>${state.users_count[g.id] || 0} пользователей будут перенесены в default-группу. Балансеры группы будут потеряны.</p>
        <div class="confirm-foot">
          <button class="btn-small" @click=${() => { confirmDelete = null; paint($mount); }}>Отмена</button>
          <button class="btn-small danger" @click=${() => doDelete($mount)}>Удалить</button>
        </div>
      </div>
    </div>
  `;
}

function closeEditor($mount) { editing = null; creating = false; paint($mount); }

function toggleBal($mount, g, b) {
  g.balancers = g.balancers || {};
  if (g.balancers[b]) delete g.balancers[b];
  else g.balancers[b] = true;
  paint($mount);
}

function toggleTS($mount, g, id) {
  g.torrservers = g.torrservers || {};
  if (g.torrservers[id]) delete g.torrservers[id];
  else g.torrservers[id] = true;
  paint($mount);
}

async function saveGroup($mount, g, isNew) {
  if (!g.name?.trim()) { toast.error('Название обязательно'); return; }
  const body = {
    action: isNew ? 'create' : 'update',
    id: g.id || undefined,
    name: g.name,
    description: g.description || '',
    max_devices: g.max_devices || 3,
    torrserver: !!g.torrserver,
    sisi: !!g.sisi,
    balancers: g.balancers || {},
    strict_balancers: !!g.strict_balancers,
    torrservers: g.torrservers || {},
  };
  try {
    await api.groupsAction(body);
    toast.success(isNew ? 'Группа создана' : 'Сохранено');
    closeEditor($mount);
    await fetchState();
    paint($mount);
  } catch (e) {
    toast.error(e.message);
  }
}

async function setDefault($mount, id) {
  try {
    await api.groupsAction({ action: 'set_default', id });
    toast.success('Default-группа обновлена');
    await fetchState();
    paint($mount);
  } catch (e) { toast.error(e.message); }
}

async function doDelete($mount) {
  const id = confirmDelete?.id;
  if (!id) return;
  try {
    await api.groupsAction({ action: 'delete', id });
    toast.success('Группа удалена');
    confirmDelete = null;
    await fetchState();
    paint($mount);
  } catch (e) { toast.error(e.message); }
}

export async function render_($mount) {
  render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
  try {
    await fetchState();
    paint($mount);
  } catch (e) {
    render(html`<l-card><div style="color:var(--danger)">Не удалось загрузить: ${e.message}</div></l-card>`, $mount);
  }
}

export { render_ as render };
