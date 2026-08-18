// pages/users.js — admin user management.
//
// List view: searchable token table with TG @username, group, devices,
// expiry, quick extend/delete. Group filter pills above the table let
// the operator slice users by their effective group (premium overlay
// is honoured — a paid user shows under premium, not base).
//
// Detail drawer (click a row): full per-user controls —
//   - Profile header (avatar, base+premium expiry chips, device count)
//   - Срок (стандарт) section: base ExpiresAt quick-extend buttons (this
//     is the "стандарт" timer, refreshed on TG re-pair).
//   - Премиум section: PremiumUntil quick-extend (+30/+90/+180/+365),
//     manual date setter, clear button. This is the overlay timer that
//     puts the user in the premium group while active.
//   - Devices section: list each UID with label / last seen / IP / fingerprint,
//     "revoke" button per device.
//   - Limits + Group section: device-cap slider (-1 = unlimited), TorrServer
//     toggle, base group dropdown.
//   - Balancers section: per-user visibility matrix (override the group's
//     default). null = inherit, true = visible, false = hidden.
//
// API:
//   GET  /api/users           → []userRow (includes premium_until, premium_active, effective_group_id)
//   POST /api/users           { action, token, ...fields }
//     actions: remove | extend | remove_device | set_device_limit |
//              toggle_torrserver | assign_group | create |
//              get_balancers | save_balancers |
//              extend_premium | set_premium_until | clear_premium
//   GET  /api/groups          → group catalog (for assign dropdown)

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  rows: [],
  filter: '',
  groupFilter: '',       // "" = all, "premium" / "standard" / "<custom>" / "expired"
  loading: true,
  groups: [],            // [{id, name, is_default}]
  selected: null,        // user row object (the drawer's subject)
  drawerBalancers: null, // lazy-loaded {balancers: [...], custom: {...edits}}
  drawerSaving: false,
  showCreate: false,
  createDays: 30,
  lastCreated: null,     // {token, lampa_url, expires}
};

async function loadUsers() {
  state.loading = true;
  try {
    const rows = await api.users();
    state.rows = Array.isArray(rows) ? rows : (rows.users || []);
  } catch (e) {
    toast.error('Не удалось загрузить пользователей: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function loadGroups() {
  try {
    const data = await api.groups();
    state.groups = data.groups || [];
  } catch (e) {
    // groups feature may not be configured — silent fallback
    state.groups = [];
  }
}

async function act(action, payload, success) {
  try {
    const res = await api.post('/users', { action, ...payload });
    if (res && res.error) throw new Error(res.error);
    if (success) toast.success(success);
    return res;
  } catch (e) {
    toast.error(e.message);
    throw e;
  }
}

function filtered() {
  const f = state.filter.trim().toLowerCase();
  const gf = state.groupFilter;
  return state.rows.filter(r => {
    if (gf) {
      if (gf === '_expired') {
        if (!r.expired) return false;
      } else if (gf === '_no_group') {
        // Users with no base group AND no active premium.
        if (r.group_id || r.premium_active) return false;
      } else {
        // Filter by effective group (paid user goes under premium pill).
        const eff = r.effective_group_id || r.group_id || '';
        if (eff !== gf) return false;
      }
    }
    if (!f) return true;
    return (r.tg_username || '').toLowerCase().includes(f)
      || String(r.telegram_id || '').includes(f)
      || (r.token || '').toLowerCase().includes(f)
      || (r.group_id || '').toLowerCase().includes(f);
  });
}

// groupCounts buckets all rows by their effective group, so the filter
// pills above the table can show per-group counts. "_expired" is special
// (base ExpiresAt < now), "_no_group" = neither base nor premium set.
function groupCounts() {
  const counts = { _all: state.rows.length, _expired: 0, _no_group: 0 };
  for (const r of state.rows) {
    if (r.expired) counts._expired++;
    const eff = r.effective_group_id || r.group_id || '';
    if (!eff && !r.premium_active) counts._no_group++;
    if (eff) counts[eff] = (counts[eff] || 0) + 1;
  }
  return counts;
}

// ----- LIST -----

function paint() {
  const rows = filtered();
  const expired   = state.rows.filter(r => r.expired).length;
  const active    = state.rows.length - expired;
  const totalDev  = state.rows.reduce((a, r) => a + (r.device_count || 0), 0);
  const premium   = state.rows.filter(r => r.premium_active).length;

  const $root = document.getElementById('users-root');
  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Всего токенов" value=${state.rows.length}></l-stat>
      <l-stat accent="mint"   label="Активные"      value=${active}></l-stat>
      <l-stat accent="violet" label="Премиум"       value=${premium}></l-stat>
      <l-stat accent="pink"   label="Просрочены"    value=${expired}></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="actions" style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
        <l-input
          icon="🔎"
          placeholder="Найти @username / TG ID / token / group"
          .value=${state.filter}
          clearable
          @input=${e => { state.filter = e.detail.value; paint(); }}
          style="width:300px"
        ></l-input>
        <l-button variant="secondary" icon="↻" @click=${async () => { await loadUsers(); paint(); }}>Обновить</l-button>
        <l-button variant="primary" icon="＋" @click=${() => { state.showCreate = true; state.lastCreated = null; paint(); }}>Создать токен</l-button>
      </div>
      ${renderGroupPills()}
      <l-table
        .loading=${state.loading}
        empty=${state.filter || state.groupFilter ? 'Ничего не найдено' : 'Нет пользователей'}
        .columns=${[
          { key: 'tg_username', label: '@', sortable: true, cell: r => userCell(r) },
          { key: 'effective_group_id', label: 'Группа', sortable: true, cell: r => groupCell(r) },
          { key: 'premium_until', label: 'Премиум', sortable: true, cell: r => premiumCell(r) },
          { key: 'device_count', label: 'Устройства', align: 'r', sortable: true, cell: r => devCell(r) },
          { key: 'expires_at',  label: 'Истекает', sortable: true, cell: r => expiryCell(r) },
          { key: '_act',        label: '', align: 'r', cell: r => actionsCell(r) },
        ]}
        .rows=${rows}
      ></l-table>
    </l-card>

    ${renderDrawer()}
    ${renderCreateModal()}

    <style>
      .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
      @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr 1fr; } }

      /* ----- Group filter pills ----- */
      .group-pills {
        display: flex; flex-wrap: wrap; gap: 6px;
        padding: var(--s-3) 0 var(--s-2);
        border-bottom: 1px solid var(--border-1);
        margin-bottom: var(--s-3);
      }
      .group-pill {
        display: inline-flex; align-items: center; gap: 6px;
        padding: 5px 10px 5px 12px;
        background: var(--bg-2); color: var(--text-1);
        border: 1px solid var(--border-1);
        border-radius: var(--r-pill);
        font-size: var(--fs-xs); font-weight: var(--fw-semibold);
        cursor: pointer;
        transition: background var(--t-fast), border-color var(--t-fast), transform var(--t-fast);
      }
      .group-pill:hover { background: var(--bg-3); border-color: var(--border-3); }
      .group-pill.active {
        background: var(--accent); color: white;
        border-color: var(--accent);
        box-shadow: 0 0 0 3px var(--accent-soft);
      }
      .group-pill.active.tone-danger { background: var(--danger); border-color: var(--danger); box-shadow: 0 0 0 3px var(--danger-soft); }
      .group-pill.active.tone-accent { background: var(--accent-2, var(--accent)); border-color: var(--accent-2, var(--accent)); }
      .group-pill-count {
        background: rgba(255,255,255,0.16);
        border-radius: var(--r-pill);
        padding: 0 7px;
        font-size: 11px;
        font-variant-numeric: tabular-nums;
      }
      .group-pill:not(.active) .group-pill-count {
        background: var(--bg-3); color: var(--text-2);
      }
      .group-base-hint {
        margin-left: 6px;
        font-size: 10px;
        color: var(--text-3);
        font-family: var(--font-mono);
      }


      /* ----- Drawer ----- */
      .drawer-overlay {
        position: fixed; inset: 0;
        background: var(--surface-overlay);
        backdrop-filter: blur(18px);
        z-index: var(--z-modal);
        animation: fade-in var(--t-normal) var(--ease-spring) both;
      }
      .drawer-panel {
        position: fixed; top: 0; right: 0; bottom: 0;
        width: min(720px, 100%);
        background: var(--bg-1);
        border-left: 1px solid var(--border-2);
        box-shadow: var(--shadow-xl);
        z-index: calc(var(--z-modal) + 1);
        overflow-y: auto;
        display: flex; flex-direction: column;
        animation: slide-in var(--t-slow) var(--ease-spring) both;
      }
      @keyframes slide-in {
        from { transform: translateX(100%); }
        to   { transform: none; }
      }

      .drawer-head {
        position: sticky; top: 0; z-index: 2;
        padding: var(--s-5) var(--s-5) var(--s-4);
        background: var(--bg-1);
        background-image:
          radial-gradient(80% 100% at 0% 0%, rgba(119,145,255,0.18), transparent 60%),
          radial-gradient(60% 80% at 100% 100%, rgba(178,102,255,0.14), transparent 60%);
        border-bottom: 1px solid var(--border-1);
      }
      .drawer-close {
        position: absolute; top: 14px; right: 14px;
        width: 32px; height: 32px;
        border-radius: 50%;
        background: var(--bg-2);
        border: 1px solid var(--border-2);
        color: var(--text-1);
        cursor: pointer; font-size: 14px;
        transition: background 140ms, color 140ms;
      }
      .drawer-close:hover { background: var(--danger); color: white; border-color: transparent; }

      .drawer-profile { display: flex; align-items: center; gap: var(--s-3); }
      .drawer-avatar {
        width: 56px; height: 56px;
        border-radius: 50%;
        background: var(--g-accent);
        color: white;
        display: grid; place-items: center;
        font-weight: var(--fw-bold); font-size: 22px;
        box-shadow: var(--shadow-md);
      }
      .drawer-name {
        font-family: var(--font-display);
        font-size: var(--fs-2xl);
        font-weight: var(--fw-bold);
        letter-spacing: -0.018em;
        background: var(--g-text-accent);
        -webkit-background-clip: text;
                background-clip: text;
        color: transparent;
        line-height: 1.05;
      }
      .drawer-sub { font-size: var(--fs-sm); color: var(--text-2); margin-top: 2px; }
      .drawer-chips { margin-top: var(--s-3); display: flex; gap: 6px; flex-wrap: wrap; }

      .drawer-section {
        padding: var(--s-4) var(--s-5);
        border-bottom: 1px solid var(--border-1);
      }
      .drawer-section:last-child { border-bottom: 0; }
      .drawer-section h3 {
        margin: 0 0 var(--s-3);
        font-family: var(--font-display);
        font-size: var(--fs-sm); font-weight: var(--fw-semibold);
        text-transform: uppercase; letter-spacing: 0.06em;
        color: var(--text-1);
        display: flex; align-items: center; gap: var(--s-2);
      }
      .drawer-section h3 .h-count {
        font-size: var(--fs-xs); color: var(--text-3);
        background: var(--bg-3); padding: 1px 8px; border-radius: var(--r-pill);
        font-weight: var(--fw-semibold); text-transform: none; letter-spacing: 0;
      }

      /* Devices */
      .dev-list { display: flex; flex-direction: column; gap: var(--s-2); }
      .dev-row {
        display: flex; align-items: center; gap: var(--s-3);
        padding: 10px 12px;
        background: var(--bg-2);
        border: 1px solid var(--border-1);
        border-radius: var(--r-2);
        transition: border-color var(--t-fast);
      }
      .dev-row:hover { border-color: var(--border-3); }
      .dev-icon {
        width: 36px; height: 36px;
        border-radius: 10px;
        background: var(--g-cyan);
        display: grid; place-items: center;
        font-size: 18px;
        color: #0f1024; flex-shrink: 0;
      }
      .dev-meta { flex: 1; min-width: 0; }
      .dev-label { font-family: var(--font-display); font-weight: var(--fw-semibold); font-size: var(--fs-sm); }
      .dev-uid { font-family: var(--font-mono); font-size: 11px; color: var(--text-3); overflow: hidden; text-overflow: ellipsis; }
      .dev-stats { font-size: var(--fs-xs); color: var(--text-2); margin-top: 2px; display: flex; gap: 12px; flex-wrap: wrap; }
      .dev-stats code { font-family: var(--font-mono); color: var(--text-1); background: var(--bg-1); padding: 1px 5px; border-radius: 3px; }
      .dev-revoke {
        background: transparent; color: var(--danger);
        border: 1px solid rgba(255,107,122,0.32);
        padding: 5px 12px; font-size: var(--fs-xs); border-radius: var(--r-pill);
        font-weight: var(--fw-semibold); cursor: pointer; flex-shrink: 0;
      }
      .dev-revoke:hover { background: var(--danger); color: white; border-color: transparent; }

      .empty { color: var(--text-3); font-size: var(--fs-sm); padding: var(--s-3); text-align: center; }

      /* Limits row */
      .limits-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: var(--s-3) var(--s-5);
      }
      @media (max-width: 540px) { .limits-grid { grid-template-columns: 1fr; } }
      .limits-row { display: flex; flex-direction: column; gap: 4px; }
      .limits-row label.l { font-size: var(--fs-xs); color: var(--text-2); font-weight: var(--fw-semibold); }
      .limits-row input, .limits-row select {
        background: var(--bg-2); border: 1px solid var(--border-2);
        padding: 7px 10px; border-radius: var(--r-2);
        color: var(--text-0); font-size: var(--fs-sm);
      }
      .limits-row input[type=range] { padding: 0; }
      .limits-row input:focus, .limits-row select:focus {
        outline: none; border-color: var(--accent);
        box-shadow: 0 0 0 3px var(--accent-soft);
      }

      .switch { display: inline-flex; align-items: center; gap: 10px; cursor: pointer; }
      .switch input { display: none; }
      .switch-track {
        width: 38px; height: 22px; background: var(--bg-3);
        border-radius: var(--r-pill); position: relative;
        transition: background 200ms;
      }
      .switch-track::after {
        content: ''; position: absolute; top: 2px; left: 2px;
        width: 18px; height: 18px; border-radius: 50%; background: white;
        transition: transform 200ms var(--ease-spring); box-shadow: var(--shadow-sm);
      }
      .switch input:checked + .switch-track { background: var(--g-mint); }
      .switch input:checked + .switch-track::after { transform: translateX(16px); }

      /* Balancer matrix */
      .bal-matrix {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
        gap: 6px;
        max-height: 320px;
        overflow-y: auto;
        padding: 4px 4px 4px 0;
      }
      .bal-cell {
        display: flex; align-items: center; gap: 8px;
        padding: 8px 12px;
        background: var(--bg-2);
        border: 1px solid var(--border-1);
        border-radius: var(--r-2);
        font-size: var(--fs-sm);
        cursor: pointer;
        transition: background var(--t-fast), border-color var(--t-fast);
      }
      .bal-cell:hover { background: var(--bg-3); }
      .bal-cell.visible {
        background: var(--success-soft); color: var(--success);
        border-color: rgba(52,224,161,0.32);
      }
      .bal-cell.hidden {
        background: var(--danger-soft); color: var(--danger);
        border-color: rgba(255,107,122,0.32);
      }
      .bal-cell.disabled { opacity: 0.4; }
      .bal-cell .bal-name { flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 12px; }
      .bal-cell .bal-q {
        font-size: 9px; font-weight: var(--fw-bold); letter-spacing: 0.04em;
        padding: 1px 6px; border-radius: 4px;
        background: rgba(255,255,255,0.08); color: var(--text-2);
      }

      .bal-foot {
        margin-top: var(--s-3);
        display: flex; gap: var(--s-2); flex-wrap: wrap;
      }
      .bal-foot button {
        background: var(--bg-2); color: var(--text-1);
        border: 1px solid var(--border-2);
        padding: 5px 12px; border-radius: var(--r-pill);
        font-size: var(--fs-xs); font-weight: var(--fw-semibold);
        cursor: pointer;
      }
      .bal-foot button:hover { background: var(--bg-3); }
      .bal-foot button.primary { background: var(--accent-soft); color: var(--accent); border-color: rgba(119,145,255,0.32); }
      .bal-foot button.primary:hover { background: var(--accent); color: white; }
      .bal-foot button.reset { color: var(--text-2); }

      .drawer-foot {
        position: sticky; bottom: 0; z-index: 2;
        padding: var(--s-3) var(--s-5);
        background: var(--bg-1);
        border-top: 1px solid var(--border-1);
        display: flex; gap: var(--s-2); justify-content: space-between; align-items: center;
      }
      .btn-cta { background: var(--g-accent); color: white; border: 0; padding: 8px 16px; border-radius: var(--r-pill); font-weight: var(--fw-semibold); cursor: pointer; font-size: var(--fs-sm); box-shadow: var(--shadow-md); }
      .btn-cta:hover { transform: translateY(-1px); box-shadow: var(--shadow-glow); }
      .btn-cta:disabled { opacity: 0.5; cursor: progress; }

      /* Base-timer buttons are visually subordinate to the premium .btn-cta —
         operators almost always want to extend premium, not the base timer. */
      .btn-base {
        background: var(--bg-2); color: var(--text-1);
        border: 1px solid var(--border-2);
        padding: 7px 14px; border-radius: var(--r-pill);
        font-size: var(--fs-sm); cursor: pointer; font-weight: var(--fw-semibold);
      }
      .btn-base:hover { background: var(--bg-3); border-color: var(--border-3); }

      /* Premium section: gentle gold tint so the operator can tell at a
         glance which timer they're touching. */
      .premium-section {
        background:
          radial-gradient(80% 100% at 100% 0%, rgba(178,102,255,0.07), transparent 60%),
          radial-gradient(80% 100% at 0% 100%, rgba(255,196,87,0.04), transparent 60%);
      }

      .btn-danger {
        background: transparent; color: var(--danger);
        border: 1px solid rgba(255,107,122,0.32);
        padding: 7px 14px; border-radius: var(--r-pill);
        font-size: var(--fs-sm); cursor: pointer; font-weight: var(--fw-semibold);
      }
      .btn-danger:hover { background: var(--danger); color: white; border-color: transparent; }

      /* Create modal */
      .create-overlay {
        position: fixed; inset: 0; background: var(--surface-overlay);
        backdrop-filter: blur(16px); z-index: var(--z-modal);
        display: grid; place-items: center; padding: var(--s-5);
        animation: fade-in var(--t-normal) var(--ease-spring) both;
      }
      .create-panel {
        width: min(520px, 100%);
        background: var(--bg-1); border: 1px solid var(--border-2);
        border-radius: var(--r-4); box-shadow: var(--shadow-xl);
        overflow: hidden;
      }
      .create-head { padding: var(--s-5);
        background: radial-gradient(80% 100% at 0% 0%, rgba(119,145,255,0.16), transparent 60%);
        border-bottom: 1px solid var(--border-1); }
      .create-head h2 {
        margin: 0; font-family: var(--font-display); font-size: var(--fs-xl);
        font-weight: var(--fw-bold); letter-spacing: -0.018em;
        background: var(--g-text-accent); -webkit-background-clip: text;
        background-clip: text; color: transparent;
      }
      .create-head p { margin: 4px 0 0; color: var(--text-2); font-size: var(--fs-sm); }
      .create-body { padding: var(--s-4) var(--s-5); }
      .create-foot { padding: var(--s-3) var(--s-5); display: flex; gap: var(--s-2); justify-content: flex-end; border-top: 1px solid var(--border-1); }

      .lampa-url-box {
        font-family: var(--font-mono); font-size: 12px;
        padding: 12px;
        background: var(--bg-0); border: 1px solid var(--accent);
        border-radius: var(--r-2);
        word-break: break-all;
        color: var(--accent);
        box-shadow: 0 0 0 3px var(--accent-soft);
      }
      .btn-copy {
        background: var(--accent-soft); color: var(--accent);
        border: 1px solid rgba(119,145,255,0.32);
        padding: 5px 12px; font-size: var(--fs-xs); border-radius: var(--r-pill);
        cursor: pointer; font-weight: var(--fw-semibold);
        margin-top: var(--s-2);
      }
      .btn-copy:hover { background: var(--accent); color: white; }
    </style>
  `, $root);
}

// ----- LIST cells -----

function userCell(r) {
  const name = r.tg_username ? '@' + r.tg_username : ('#' + (r.telegram_id || '?'));
  return html`
    <div style="display:flex;align-items:center;gap:10px;cursor:pointer" @click=${() => openDrawer(r)}>
      <div style="width:32px;height:32px;border-radius:50%;background:linear-gradient(135deg,var(--accent),var(--accent-2));color:#fff;display:grid;place-items:center;font-weight:700;font-size:13px;">
        ${(name.replace(/[@#]/, '').slice(0,1) || '?').toUpperCase()}
      </div>
      <div style="line-height:1.2;min-width:0">
        <div style="font-weight:600">${name}</div>
        <div style="font-size:11px;color:var(--text-3);font-family:var(--font-mono);overflow:hidden;text-overflow:ellipsis;max-width:140px;">${(r.token || '').slice(0,12)}…</div>
      </div>
    </div>
  `;
}

// renderGroupPills draws a row of clickable pills above the table that
// slice users by their effective group. "Премиум" overlay wins over base.
function renderGroupPills() {
  const counts = groupCounts();
  // Build a list of group buckets to show. Always include "Все" + an
  // "_expired" bucket. Then one pill per group that has at least 1 user.
  const items = [
    { id: '',          label: 'Все',           count: counts._all || 0,    tone: 'info' },
  ];
  // Sort named groups by user count desc so the biggest cohort is on the
  // left where the operator's eye lands first.
  const named = state.groups
    .map(g => ({ id: g.id, label: g.name || g.id, count: counts[g.id] || 0, isDefault: g.is_default }))
    .sort((a, b) => b.count - a.count);
  for (const g of named) {
    items.push({
      id: g.id,
      label: g.label,
      count: g.count,
      tone: g.isDefault ? 'info' : 'accent',
    });
  }
  if (counts._no_group > 0) {
    items.push({ id: '_no_group', label: 'Без группы', count: counts._no_group, tone: 'neutral' });
  }
  if (counts._expired > 0) {
    items.push({ id: '_expired', label: 'Истёкшие', count: counts._expired, tone: 'danger' });
  }
  return html`
    <div class="group-pills">
      ${items.map(it => html`
        <button
          class="group-pill ${state.groupFilter === it.id ? 'active' : ''} tone-${it.tone}"
          @click=${() => { state.groupFilter = state.groupFilter === it.id ? '' : it.id; paint(); }}
          title="Показать только этих пользователей"
        >
          <span class="group-pill-label">${it.label}</span>
          <span class="group-pill-count">${it.count}</span>
        </button>
      `)}
    </div>
  `;
}

function groupCell(r) {
  const eff = r.effective_group_id || r.group_id || '';
  if (!eff) return html`<span style="color:var(--text-3)">—</span>`;
  // If premium overlay is what put the user in this group, show it as a
  // gold pill so the operator can tell base vs overlay at a glance.
  if (r.premium_active && r.effective_group_id && r.effective_group_id !== r.group_id) {
    return html`
      <l-pill tone="accent" dot title="Премиум-оверлей до ${r.premium_until}">${eff}</l-pill>
      ${r.group_id ? html`<span class="group-base-hint">база: ${r.group_id}</span>` : ''}
    `;
  }
  return html`<l-pill tone="info">${eff}</l-pill>`;
}

// premiumCell renders the PremiumUntil column. Three states:
//   - active premium → green pill with date (e.g. "до 26.08")
//   - expired premium → grey pill (only if PremiumUntil is set but past)
//   - never had premium → em-dash
// premiumInputValue converts the server's "YYYY-MM-DD HH:MM" string into
// the `value` format expected by <input type="datetime-local"> which is
// "YYYY-MM-DDTHH:MM". If no premium is set, default to 30 days from now
// so the operator can just tap the input and adjust.
function premiumInputValue(r) {
  if (r.premium_until) {
    return (r.premium_until || '').replace(' ', 'T').slice(0, 16);
  }
  // Suggest 30 days from now as a starting point.
  const d = new Date();
  d.setDate(d.getDate() + 30);
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function premiumCell(r) {
  if (!r.premium_until) return html`<span style="color:var(--text-3)">—</span>`;
  if (r.premium_active) {
    // Strip seconds; keep "YYYY-MM-DD" for compact rendering.
    const short = (r.premium_until || '').slice(0, 10);
    return html`<l-pill tone="ok" dot title="${r.premium_until}">до ${short}</l-pill>`;
  }
  return html`<l-pill tone="neutral" title="Премиум закончился ${r.premium_until}">истёк</l-pill>`;
}

function devCell(r) {
  const max = r.max_devices || 0;
  const cur = r.device_count || 0;
  const over = max > 0 && cur > max;
  return html`<span style="font-variant-numeric:tabular-nums;color:${over ? 'var(--danger)' : 'var(--text-0)'}">${cur}${max > 0 ? ' / ' + max : ''}</span>`;
}

function expiryCell(r) {
  const exp = r.expires_at || '';
  if (!exp) return html`<span style="color:var(--text-3)">—</span>`;
  return r.expired
    ? html`<l-pill tone="danger" dot>Истёк</l-pill>`
    : html`<span style="font-size:var(--fs-sm);color:var(--text-1);font-family:var(--font-mono);">${exp}</span>`;
}

function actionsCell(r) {
  // Default quick-action is "+30 premium" because that's overwhelmingly
  // what operators do (manual grant / refund). Base extension is in the
  // drawer for the rarer "стандарт" timer edits.
  return html`
    <div style="display:flex;gap:6px;justify-content:flex-end;" @click=${e => e.stopPropagation()}>
      <l-button size="sm" variant="ghost" title="Премиум +30 дней"
        @click=${async () => { await act('extend_premium', { token: r.token, days: 30 }, 'Премиум: +30 дней'); await loadUsers(); paint(); }}>+30д ★</l-button>
      <l-button size="sm" variant="ghost" @click=${() => openDrawer(r)}>⋯</l-button>
    </div>
  `;
}

// ----- DRAWER -----

async function openDrawer(r) {
  state.selected = r;
  state.drawerBalancers = null;
  paint();
  // Lazy-load balancer visibility for this user
  try {
    const bals = await act('get_balancers', { token: r.token });
    if (bals && bals.balancers) {
      state.drawerBalancers = {
        items: bals.balancers,
        overrides: {}, // pending changes — applied on save
      };
    }
  } catch (e) {
    // Already toasted in act()
  }
  paint();
}

function closeDrawer() {
  state.selected = null;
  state.drawerBalancers = null;
  paint();
}

function renderDrawer() {
  if (!state.selected) return '';
  const r = state.selected;
  const name = r.tg_username ? '@' + r.tg_username : ('#' + (r.telegram_id || '?'));
  const initial = (name.replace(/[@#]/, '').slice(0, 1) || '?').toUpperCase();
  const devs = r.devices || [];

  return html`
    <div class="drawer-overlay" @click=${e => { if (e.target.classList.contains('drawer-overlay')) closeDrawer(); }}>
      <aside class="drawer-panel" @click=${e => e.stopPropagation()}>
        <button class="drawer-close" @click=${closeDrawer}>✕</button>

        <div class="drawer-head">
          <div class="drawer-profile">
            <div class="drawer-avatar">${initial}</div>
            <div>
              <div class="drawer-name">${name}</div>
              <div class="drawer-sub">
                TG ID: <code>${r.telegram_id}</code> · Token: <code>${(r.token || '').slice(0,8)}…</code>
              </div>
            </div>
          </div>
          <div class="drawer-chips">
            ${r.expired
              ? html`<l-pill tone="danger" dot>Истёк</l-pill>`
              : html`<l-pill tone="ok" dot>Активен</l-pill>`}
            ${r.premium_active
              ? html`<l-pill tone="accent" dot title="Премиум до ${r.premium_until}">Премиум</l-pill>`
              : ''}
            <span class="chip">Создан ${r.created_at || '—'}</span>
            <span class="chip">До ${r.expires_at || '—'}</span>
            ${r.premium_active ? html`<span class="chip" style="color:var(--accent-2)">Премиум до ${r.premium_until}</span>` : ''}
            ${r.effective_group_id ? html`<span class="chip accent">${r.effective_group_id}</span>` : ''}
            ${r.torrserver_disabled ? html`<span class="chip" style="color:var(--warn)">TorrServer выкл</span>` : ''}
          </div>
        </div>

        <section class="drawer-section">
          <h3>Стандарт (базовый таймер) <span class="h-count">${r.expires_at || '—'}</span></h3>
          <div style="font-size:var(--fs-xs);color:var(--text-3);margin-bottom:var(--s-2)">
            Базовая подписка. Обновляется при привязке в TG.
            После оплаты премиума остаётся неизменной — премиум-таймер ниже.
          </div>
          <div style="display:flex;gap:6px;flex-wrap:wrap">
            ${[7, 30, 90, 365].map(d => html`
              <button class="btn-base" style="padding:6px 14px;font-size:var(--fs-xs)" @click=${async () => {
                await act('extend', { token: r.token, days: d }, `+${d} дней`);
                await loadUsers(); state.selected = state.rows.find(u => u.token === r.token) || r; paint();
              }}>+${d}д</button>
            `)}
            <input id="extend-custom" type="number" min="1" max="3650" placeholder="свой срок"
              style="padding:6px 10px;background:var(--bg-2);border:1px solid var(--border-2);border-radius:var(--r-pill);color:var(--text-0);width:120px;font-size:var(--fs-xs)"/>
            <button class="btn-base" style="padding:6px 14px;font-size:var(--fs-xs)" @click=${async () => {
              const v = +document.getElementById('extend-custom').value;
              if (!v || v <= 0) { toast.error('Укажи количество дней'); return; }
              await act('extend', { token: r.token, days: v }, `+${v} дней`);
              await loadUsers(); state.selected = state.rows.find(u => u.token === r.token) || r; paint();
            }}>Применить</button>
          </div>
        </section>

        <section class="drawer-section premium-section">
          <h3>
            Премиум (оверлей-таймер)
            <span class="h-count">${r.premium_active
              ? html`<span style="color:var(--success)">до ${r.premium_until}</span>`
              : (r.premium_until ? html`<span style="color:var(--text-3)">истёк ${r.premium_until}</span>` : '—')}</span>
          </h3>
          <div style="font-size:var(--fs-xs);color:var(--text-3);margin-bottom:var(--s-2)">
            Подписка на премиум-группу. Покрывает базовую группу, пока активен.
            ${r.premium_active && r.group_id
              ? html`<br/>После истечения — пользователь вернётся в <code>${r.group_id}</code>.`
              : (r.premium_active
                ? html`<br/>После истечения — вернётся в группу по умолчанию.`
                : '')}
          </div>
          <div style="display:flex;gap:6px;flex-wrap:wrap;align-items:center">
            ${[30, 90, 180, 365].map(d => html`
              <button class="btn-cta" style="padding:6px 14px;font-size:var(--fs-xs)" @click=${async () => {
                await act('extend_premium', { token: r.token, days: d }, `Премиум: +${d} дней`);
                await loadUsers(); state.selected = state.rows.find(u => u.token === r.token) || r; paint();
              }}>+${d}д</button>
            `)}
            <input id="extend-premium-custom" type="number" min="1" max="3650" placeholder="свой срок"
              style="padding:6px 10px;background:var(--bg-2);border:1px solid var(--border-2);border-radius:var(--r-pill);color:var(--text-0);width:120px;font-size:var(--fs-xs)"/>
            <button class="btn-cta" style="padding:6px 14px;font-size:var(--fs-xs)" @click=${async () => {
              const v = +document.getElementById('extend-premium-custom').value;
              if (!v || v <= 0) { toast.error('Укажи количество дней'); return; }
              await act('extend_premium', { token: r.token, days: v }, `Премиум: +${v} дней`);
              await loadUsers(); state.selected = state.rows.find(u => u.token === r.token) || r; paint();
            }}>Применить</button>
          </div>
          <div style="display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin-top:var(--s-3)">
            <input id="set-premium-date" type="datetime-local"
              .value=${premiumInputValue(r)}
              style="padding:6px 10px;background:var(--bg-2);border:1px solid var(--border-2);border-radius:var(--r-2);color:var(--text-0);font-size:var(--fs-xs)"/>
            <button class="btn-base" style="padding:6px 14px;font-size:var(--fs-xs)" @click=${async () => {
              const v = document.getElementById('set-premium-date').value; // "YYYY-MM-DDTHH:MM"
              if (!v) { toast.error('Укажи дату'); return; }
              const formatted = v.replace('T', ' ').slice(0, 16);
              await act('set_premium_until', { token: r.token, premium_until: formatted }, 'Премиум установлен');
              await loadUsers(); state.selected = state.rows.find(u => u.token === r.token) || r; paint();
            }}>Установить дату</button>
            ${r.premium_until ? html`
              <button class="btn-danger" style="padding:6px 14px;font-size:var(--fs-xs)" @click=${async () => {
                if (!confirm('Снять премиум прямо сейчас? Пользователь сразу выпадет в базовую группу.')) return;
                await act('clear_premium', { token: r.token }, 'Премиум очищен');
                await loadUsers(); state.selected = state.rows.find(u => u.token === r.token) || r; paint();
              }}>Очистить премиум</button>
            ` : ''}
          </div>
        </section>

        <section class="drawer-section">
          <h3>Устройства <span class="h-count">${devs.length}${r.max_devices > 0 ? ' / ' + r.max_devices : ''}</span></h3>
          ${devs.length === 0
            ? html`<div class="empty">Нет привязанных устройств</div>`
            : html`<div class="dev-list">${devs.map(d => renderDevice(r, d))}</div>`}
          ${devs.length > 1 ? html`
            <div style="margin-top:var(--s-2)">
              <button class="btn-danger" @click=${async () => {
                if (!confirm('Отвязать ВСЕ устройства (' + devs.length + ') вместе с отпечатками? Аккаунт останется — устройства привяжутся заново при следующем входе.')) return;
                await act('remove_all_devices', { token: r.token }, 'Все устройства отвязаны');
                await loadUsers();
                state.selected = state.rows.find(u => u.token === r.token) || r;
                paint();
              }}>Отвязать все устройства</button>
            </div>
          ` : ''}
        </section>

        <section class="drawer-section">
          <h3>Лимиты и группа</h3>
          <div class="limits-grid">
            <div class="limits-row">
              <label class="l">Макс. устройств (-1 = безлимит)</label>
              <input id="user-max-dev" type="number" min="-1" max="100" .value=${r.max_devices}/>
            </div>
            <div class="limits-row">
              <label class="l">Группа</label>
              <select id="user-group">
                <option value="" ?selected=${!r.group_id}>— default —</option>
                ${state.groups.map(g => html`<option value=${g.id} ?selected=${g.id === r.group_id}>${g.name || g.id}${g.is_default ? ' (default)' : ''}</option>`)}
              </select>
            </div>
            <div class="limits-row">
              <label class="l">TorrServer</label>
              <label class="switch">
                <input id="user-ts" type="checkbox" ?checked=${!r.torrserver_disabled}/>
                <span class="switch-track"></span>
                <span style="font-size:var(--fs-xs);color:var(--text-2)">${r.torrserver_disabled ? 'выключен' : 'включен'}</span>
              </label>
            </div>
          </div>
          <div style="margin-top:var(--s-3);display:flex;gap:6px">
            <button class="btn-cta" @click=${() => saveLimits(r)}>Сохранить лимиты</button>
          </div>
        </section>

        <section class="drawer-section">
          <h3>Балансеры <span class="h-count">${
            state.drawerBalancers
              ? balCountSummary(state.drawerBalancers)
              : '…'
          }</span></h3>
          ${state.drawerBalancers ? renderBalMatrix(r) : html`<div class="empty">Загрузка…</div>`}
        </section>

        <div class="drawer-foot">
          <button class="btn-danger" @click=${async () => {
            if (!confirm('Удалить токен пользователя ' + name + '? Все устройства потеряют доступ.')) return;
            await act('remove', { token: r.token }, 'Удалён');
            closeDrawer();
            await loadUsers(); paint();
          }}>Удалить пользователя</button>
          <button class="btn-cta" @click=${closeDrawer}>Готово</button>
        </div>
      </aside>
    </div>
  `;
}

function renderDevice(r, d) {
  const isAndroid = /android/i.test(d.label || '');
  const isiOS     = /ios|iphone|ipad/i.test(d.label || '');
  const isWeb     = /web|browser|chrome|firefox|safari/i.test(d.label || '');
  const icon = isAndroid ? '🤖' : isiOS ? '' : isWeb ? '◐' : '◇';
  return html`
    <div class="dev-row">
      <div class="dev-icon">${icon}</div>
      <div class="dev-meta">
        <div class="dev-label">${d.label || 'Без названия'}</div>
        <div class="dev-uid">UID: ${d.uid || '—'}</div>
        <div class="dev-stats">
          ${d.bound_at ? html`<span>привязан: <code>${d.bound_at}</code></span>` : ''}
          ${d.last_seen ? html`<span>last seen: <code>${d.last_seen}</code></span>` : ''}
          ${d.last_ip ? html`<span>IP: <code>${d.last_ip}</code></span>` : ''}
          ${d.fingerprint ? html`<span>fp: <code>${(d.fingerprint || '').slice(0,12)}…</code></span>` : ''}
        </div>
      </div>
      <button class="dev-revoke" @click=${async () => {
        if (!confirm('Отвязать устройство «' + (d.label || d.uid) + '»? Оно сможет привязаться заново (если лимит позволяет).')) return;
        await act('remove_device', { token: r.token, uid: d.uid }, 'Отвязано');
        await loadUsers();
        state.selected = state.rows.find(u => u.token === r.token) || r;
        paint();
      }}>Отвязать</button>
    </div>
  `;
}

async function saveLimits(r) {
  const max = +document.getElementById('user-max-dev').value;
  const ts = !document.getElementById('user-ts').checked;
  const grp = document.getElementById('user-group').value;
  try {
    if (Number.isFinite(max) && max !== r.max_devices) {
      await act('set_device_limit', { token: r.token, max_devices: max });
    }
    if (ts !== !!r.torrserver_disabled) {
      await act('toggle_torrserver', { token: r.token, torrserver_disabled: ts });
    }
    if (grp !== (r.group_id || '')) {
      await act('assign_group', { token: r.token, group_id: grp });
    }
    toast.success('Сохранено');
    await loadUsers();
    state.selected = state.rows.find(u => u.token === r.token) || r;
    paint();
  } catch (e) {
    // already toasted
  }
}

// ----- Balancer matrix -----

function balCountSummary(d) {
  const items = d.items || [];
  const ov = d.overrides || {};
  // Merged visibility per balancer
  const counts = { visible: 0, hidden: 0, inherit: 0 };
  for (const b of items) {
    const o = ov[b.key];
    if (o === true) counts.visible++;
    else if (o === false) counts.hidden++;
    else if (b.user_visible === true) counts.visible++;
    else if (b.user_visible === false) counts.hidden++;
    else counts.inherit++;
  }
  return `${counts.visible} ✓  ${counts.hidden} ⊘  ${counts.inherit} → inherit`;
}

function renderBalMatrix(r) {
  const d = state.drawerBalancers;
  const items = d.items || [];
  return html`
    <div class="bal-matrix">
      ${items.map(b => {
        const o = d.overrides[b.key];
        let vis;
        if (Object.prototype.hasOwnProperty.call(d.overrides, b.key)) {
          vis = o; // true | false | null
        } else {
          vis = b.user_visible;  // undefined / true / false
        }
        const cellState =
          vis === true  ? 'visible' :
          vis === false ? 'hidden'  :
          'inherit';
        const cls = `bal-cell ${cellState === 'visible' ? 'visible' : (cellState === 'hidden' ? 'hidden' : '')} ${!b.global_enabled ? 'disabled' : ''}`;
        return html`
          <div class=${cls} @click=${() => toggleBalCell(b)} title="${b.global_enabled ? 'Клик: показать → скрыть → inherit' : 'Глобально выключен в админке'}">
            <span style="font-size:14px;width:18px;text-align:center">${
              cellState === 'visible' ? '✓' :
              cellState === 'hidden'  ? '⊘' :
              '↺'
            }</span>
            <span class="bal-name">${b.name}</span>
            ${b.quality ? html`<span class="bal-q">${b.quality}</span>` : ''}
          </div>
        `;
      })}
    </div>
    <div class="bal-foot">
      <button class="primary" @click=${() => saveBalancers(r)} ?disabled=${state.drawerSaving}>Сохранить балансеры</button>
      <button class="reset" @click=${() => resetBalancers(r)}>Сбросить (inherit)</button>
      <span style="margin-left:auto;font-size:var(--fs-xs);color:var(--text-3)">tri-state: ✓ visible · ⊘ hidden · ↺ inherit (default)</span>
    </div>
  `;
}

function toggleBalCell(b) {
  if (!b.global_enabled) return; // can't override globally-disabled balancer
  const d = state.drawerBalancers;
  const cur = Object.prototype.hasOwnProperty.call(d.overrides, b.key)
    ? d.overrides[b.key]
    : b.user_visible;
  // cycle: undefined → true → false → undefined
  let next;
  if (cur === true) next = false;
  else if (cur === false) next = undefined;
  else next = true;
  if (next === undefined) delete d.overrides[b.key];
  else d.overrides[b.key] = next;
  // Also explicitly null-out keys that the user reverted to inherit and which
  // were previously set on the server (so the save call sends the change).
  if (next === undefined && b.user_visible != null) {
    d.overrides[b.key] = null;
  }
  paint();
}

async function saveBalancers(r) {
  state.drawerSaving = true; paint();
  const d = state.drawerBalancers;
  // Build final visibility map. null values clear the key.
  const out = {};
  for (const b of d.items) {
    if (Object.prototype.hasOwnProperty.call(d.overrides, b.key)) {
      const v = d.overrides[b.key];
      if (v === true || v === false) out[b.key] = v;
      // null = clear (don't include in map; backend treats absence as inherit)
    } else if (b.user_visible === true || b.user_visible === false) {
      out[b.key] = b.user_visible;
    }
  }
  try {
    await act('save_balancers', { token: r.token, visibility: out }, 'Балансеры сохранены');
    // Re-load to reflect persisted state
    const bals = await act('get_balancers', { token: r.token });
    if (bals && bals.balancers) {
      state.drawerBalancers = { items: bals.balancers, overrides: {} };
    }
  } catch (e) {}
  state.drawerSaving = false; paint();
}

async function resetBalancers(r) {
  if (!confirm('Сбросить все per-user override\'ы балансеров? Пользователь будет видеть то же, что и группа по умолчанию.')) return;
  state.drawerSaving = true; paint();
  try {
    await act('save_balancers', { token: r.token, visibility: null }, 'Сброшено');
    const bals = await act('get_balancers', { token: r.token });
    if (bals && bals.balancers) {
      state.drawerBalancers = { items: bals.balancers, overrides: {} };
    }
  } catch (e) {}
  state.drawerSaving = false; paint();
}

// ----- CREATE modal -----

function renderCreateModal() {
  if (!state.showCreate) return '';
  return html`
    <div class="create-overlay" @click=${e => { if (e.target.classList.contains('create-overlay')) { state.showCreate = false; paint(); } }}>
      <div class="create-panel" @click=${e => e.stopPropagation()}>
        <div class="create-head">
          <h2>Новый device-токен</h2>
          <p>Создаёт token и URL для подключения Lampa. Привязка устройства произойдёт автоматически при первом запросе.</p>
        </div>
        <div class="create-body">
          ${state.lastCreated ? html`
            <div style="font-size:var(--fs-sm);color:var(--text-1);margin-bottom:var(--s-2)">
              Токен создан, истекает: <code style="font-family:var(--font-mono);color:var(--accent)">${state.lastCreated.expires}</code>
            </div>
            <div class="lampa-url-box">${state.lastCreated.lampa_url}</div>
            <button class="btn-copy" @click=${() => {
              navigator.clipboard.writeText(state.lastCreated.lampa_url);
              toast.success('Скопировано');
            }}>📋 Скопировать URL</button>
          ` : html`
            <div class="limits-grid">
              <div class="limits-row">
                <label class="l">Срок действия (дней)</label>
                <input id="create-days" type="number" min="1" max="3650" .value=${state.createDays} @input=${e => state.createDays = +e.target.value || 30}/>
              </div>
            </div>
          `}
        </div>
        <div class="create-foot">
          ${state.lastCreated ? html`
            <button class="btn-danger" @click=${() => { state.lastCreated = null; paint(); }}>Создать ещё</button>
            <button class="btn-cta" @click=${() => { state.showCreate = false; state.lastCreated = null; paint(); }}>Готово</button>
          ` : html`
            <button class="btn-danger" @click=${() => { state.showCreate = false; paint(); }}>Отмена</button>
            <button class="btn-cta" @click=${async () => {
              try {
                const res = await act('create', { days: state.createDays }, 'Токен создан');
                state.lastCreated = res;
                await loadUsers();
                paint();
              } catch (e) {}
            }}>Создать</button>
          `}
        </div>
      </div>
    </div>
  `;
}

// ----- ENTRY -----

export async function render_($mount) {
  $mount.innerHTML = `<div id="users-root"></div>`;
  state.selected = null;
  state.showCreate = false;
  state.lastCreated = null;
  paint();
  await Promise.all([loadUsers(), loadGroups()]);
  paint();
}
export { render_ as render };
