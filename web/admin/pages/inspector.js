// pages/inspector.js — diagnostic scan + one-click fixes.
//
// API:
//   POST /api/inspector/run                    → {issues[], categories, order}
//   POST /api/inspector/fix {fix_id, target}   → {ok, message?, error?}
//
// Categories are colour-coded; issues are grouped by severity inside each
// category. Issues that carry a `fix_id` show a "✓ Fix" inline button.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  issues: [],
  categories: {},
  order: [],
  loading: false,
  category: '',
};

const CATEGORY_ICONS = {
  system:      '🖥',
  balancers:   '⚙',
  torrserver:  '🧲',
  transcoding: '🎞',
  tmdb:        '🎬',
  youtube:     '▶',
  proxy:       '🌐',
};

async function runChecks(category = '') {
  state.loading = true; paint();
  try {
    const q = category ? `?category=${encodeURIComponent(category)}` : '';
    const r = await api.post('/inspector/run' + q);
    if (r && r.error) throw new Error(r.error);
    state.issues     = (r && r.issues) || [];
    state.categories = (r && r.categories) || {};
    state.order      = (r && r.order) || [];
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.loading = false;
    paint();
  }
}

async function applyFix(issue) {
  if (!issue.fix_id) return;
  if (!confirm(`Применить фикс "${issue.fix_label || issue.fix_id}"?\n\nЦель: ${issue.target}`)) return;
  try {
    const r = await api.post('/inspector/fix', { fix_id: issue.fix_id, target: issue.target });
    if (r && r.ok === false) throw new Error(r.error || 'fix failed');
    if (r && r.error) throw new Error(r.error);
    toast.success(r && r.message || 'Применено');
    // Re-run the same category to refresh.
    runChecks(state.category);
  } catch (e) {
    toast.error(e.message);
  }
}

function severityTone(s) {
  switch (s) {
    case 'critical': return 'danger';
    case 'warning':  return 'warn';
    case 'info':     return 'info';
    default:         return 'muted';
  }
}
function severityRank(s) {
  return { critical: 0, warning: 1, info: 2 }[s] ?? 3;
}

function paint() {
  const $root = document.getElementById('inspector-root');
  if (!$root) return;

  const issues = state.issues;
  const critical = issues.filter(i => i.severity === 'critical').length;
  const warning  = issues.filter(i => i.severity === 'warning').length;
  const info     = issues.filter(i => i.severity === 'info').length;
  const fixable  = issues.filter(i => i.fix_id).length;

  // Group issues by category, then sort by severity rank within group.
  const byCat = {};
  for (const i of issues) {
    if (!byCat[i.category]) byCat[i.category] = [];
    byCat[i.category].push(i);
  }
  for (const k of Object.keys(byCat)) {
    byCat[k].sort((a, b) => severityRank(a.severity) - severityRank(b.severity));
  }
  const cats = state.order && state.order.length ? state.order : Object.keys(byCat);

  // Build tabs list — "Все" + per-category.
  const tabs = [{ key: '', label: 'Все' }, ...cats.map(c => ({
    key: c,
    label: (CATEGORY_ICONS[c] || '◆') + ' ' + (state.categories[c] || c),
    badge: (byCat[c] || []).length || undefined,
  }))];

  render(html`
    <div class="page-summary">
      <l-stat accent="pink"   label="Critical" value=${critical} icon="✗"></l-stat>
      <l-stat accent="amber"  label="Warning"  value=${warning}  icon="⚠"></l-stat>
      <l-stat accent="blue"   label="Info"     value=${info}     icon="i"></l-stat>
      <l-stat accent="green"  label="Auto-fix" value=${fixable}  icon="✓"></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">Диагностика</span></div>
      <div slot="actions" style="display:flex;gap:8px;align-items:center;">
        <l-tabs
          .tabs=${tabs}
          .active=${state.category}
          @tab-change=${e => { state.category = e.detail.key; runChecks(state.category); }}
        ></l-tabs>
        <l-button variant="primary" icon="🔍" @click=${() => runChecks(state.category)} ?loading=${state.loading}>Запустить</l-button>
      </div>

      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Сканирование…</div>`
        : issues.length === 0
          ? html`<div class="ins-clean">✓ Проблем не найдено</div>`
          : html`
            <div class="ins-cats">
              ${cats.filter(c => byCat[c] && byCat[c].length).map(c => html`
                <div class="ins-cat">
                  <div class="ins-cat-h">
                    <span class="ins-cat-icon">${CATEGORY_ICONS[c] || '◆'}</span>
                    <span>${state.categories[c] || c}</span>
                    <span class="ins-cat-c">${byCat[c].length}</span>
                  </div>
                  <div class="ins-list">
                    ${byCat[c].map(i => issueRow(i))}
                  </div>
                </div>
              `)}
            </div>`}
    </l-card>
  `, $root);
}

function issueRow(i) {
  return html`
    <div class="ins-row sev-${i.severity}">
      <l-pill tone=${severityTone(i.severity)} dot=${i.severity === 'critical'}>${i.severity}</l-pill>
      <div class="ins-body">
        <div class="ins-title">${i.title}</div>
        <div class="ins-target">target: <code>${i.target || '—'}</code></div>
        ${i.detail ? html`<div class="ins-detail">${i.detail}</div>` : ''}
      </div>
      ${i.fix_id ? html`<l-button size="sm" variant="success" @click=${() => applyFix(i)}>✓ ${i.fix_label || 'Fix'}</l-button>` : ''}
    </div>
  `;
}

const styleId = 'l-inspector-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }

    .ins-clean {
      padding: 48px 24px; text-align: center;
      color: var(--success); font-size: var(--fs-lg);
      background: var(--success-soft); border-radius: var(--r-2);
      margin-top: var(--s-3);
    }

    .ins-cats { display: flex; flex-direction: column; gap: var(--s-4); }
    .ins-cat {
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      overflow: hidden;
    }
    .ins-cat-h {
      display: flex; align-items: center; gap: 8px;
      padding: 10px 14px;
      background: var(--bg-3);
      font-family: var(--font-display);
      font-weight: 600; font-size: var(--fs-sm);
      color: var(--text-1);
      border-bottom: 1px solid var(--border-1);
    }
    .ins-cat-icon { font-size: 16px; }
    .ins-cat-c {
      margin-left: auto;
      font-size: var(--fs-xs); font-weight: 600;
      color: var(--text-3);
      background: var(--bg-1); padding: 1px 8px; border-radius: var(--r-pill);
    }
    .ins-list { display: flex; flex-direction: column; }
    .ins-row {
      display: flex; align-items: flex-start; gap: 10px;
      padding: 10px 14px;
      border-bottom: 1px solid var(--border-1);
    }
    .ins-row:last-child { border-bottom: none; }
    .ins-row.sev-critical { background: rgba(248,113,113,0.04); }
    .ins-row.sev-warning  { background: rgba(251,191,36,0.03); }
    .ins-body { flex: 1; min-width: 0; }
    .ins-title { font-weight: 600; font-size: var(--fs-sm); color: var(--text-0); }
    .ins-target { font-size: var(--fs-xs); color: var(--text-3); margin-top: 2px; }
    .ins-target code { color: var(--accent); background: var(--bg-3); padding: 1px 6px; border-radius: 4px; font-family: var(--font-mono); }
    .ins-detail { font-size: var(--fs-xs); color: var(--text-2); margin-top: 4px; line-height: 1.45; word-break: break-word; }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="inspector-root"></div>`;
  paint();
  await runChecks();
}
export { render_ as render };
