// pages/config.js — section-filtered TOML editor (the UX from the legacy
// admin, rebuilt premium).
//
// Left rail = section navigator. Clicking a section filters the editor to show
// ONLY that section's lines (header + body + any sub-tables like [online.*]).
// "Все секции" shows the whole file. Edits to a filtered view are spliced back
// into the full document by line-range, so comments + formatting everywhere
// else are preserved. Syntax-highlighted textarea, validate, save+hot-reload,
// backups rollback.
//
// API:
//   GET  /api/config/toml         → {toml, path, source}
//   POST /api/config/toml         → save + hot-reload
//   POST /api/config/validate     → validate only
//   GET  /api/config/backups      → list config_backups/*.toml
//   POST /api/config/rollback     → {name} restore backup

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';
import { icon, hasIcon } from '../icons.js';

let state = {
  toml: '',           // full document (source of truth)
  source: '',
  path: '',
  dirty: false,
  backups: [],
  validation: null,
  loading: true,
  saving: false,
  active: '*',         // '*' = whole file, or a top-level group name
  range: null,         // {start, end} line range the editor currently shows
  filterNav: '',       // section-nav search box
};

// Friendly label + icon-key for known top-level groups. Unknown groups fall
// back to the raw name + a generic icon. icon-key maps into icons.js.
const GROUP_META = {
  '(root)':       { label: 'Базовые',      iconKey: 'config' },
  server:         { label: 'Сервер',       iconKey: 'server' },
  online:         { label: 'Балансеры',    iconKey: 'balancers' },
  web:            { label: 'Web / Lampa',  iconKey: 'plugins' },
  TelegramAuth:   { label: 'Telegram',     iconKey: 'telegram' },
  telegram:       { label: 'Telegram',     iconKey: 'telegram' },
  kit:            { label: 'Kit',          iconKey: 'browser' },
  cluster:        { label: 'Кластер',      iconKey: 'cluster' },
  torrserver:     { label: 'TorrServer',   iconKey: 'transcoding' },
  transcoding:    { label: 'Транскодинг',  iconKey: 'transcoding' },
  proxy:          { label: 'Proxy (VLESS)',iconKey: 'proxy' },
  proxy_link:     { label: 'ProxyLink',    iconKey: 'proxy' },
  proxylink:      { label: 'ProxyLink',    iconKey: 'proxy' },
  proxycore:      { label: 'ProxyCore',    iconKey: 'proxycore' },
  security:       { label: 'Безопасность', iconKey: 'admins' },
  waf:            { label: 'WAF',          iconKey: 'bans' },
  admin:          { label: 'Админ',        iconKey: 'admins' },
  cub:            { label: 'CUB / Sync',   iconKey: 'selfupdate' },
  sisi:           { label: 'SISI',         iconKey: 'drochub' },
  browser_pool:   { label: 'Браузер-пул',  iconKey: 'browser' },
  calendar:       { label: 'Календарь',    iconKey: 'calendar' },
  alice:          { label: 'Алиса',        iconKey: 'alice' },
  iptv:           { label: 'IPTV',         iconKey: 'transcoding' },
  llm:            { label: 'LLM',          iconKey: 'inspector' },
  observability:  { label: 'Метрики',      iconKey: 'telemetry' },
  kinopoisk:      { label: 'Kinopoisk',    iconKey: 'balancers' },
  collections:    { label: 'Коллекции',    iconKey: 'modules' },
  antidpi:        { label: 'AntiDPI',      iconKey: 'proxy' },
  compat:         { label: 'Compat',       iconKey: 'config' },
};
function groupMeta(name) {
  return GROUP_META[name] || { label: name, iconKey: 'config' };
}

async function load() {
  state.loading = true;
  try {
    const d = await api.get('/config/toml');
    state.toml = d.toml || '';
    state.source = d.source || '';
    state.path = d.path || '';
    state.dirty = false;
    state.validation = null;
    state.active = '*';
    state.range = null;
  } catch (e) {
    toast.error('Не удалось загрузить config.toml: ' + e.message);
  }
  try {
    const b = await api.get('/config/backups');
    state.backups = (b && b.backups) || (Array.isArray(b) ? b : []);
  } catch { state.backups = []; }
  state.loading = false;
}

// ----- group parsing + range-based slice/merge -----

// Parse the full document into top-level groups. A group is a contiguous run
// of sections sharing the same top-level name (so [online] + [online.rezka] +
// [online.kinotochka] collapse into one "online" group). Leading top-level
// keys before the first [section] become the "(root)" group.
function parseGroups(toml) {
  const lines = toml.split('\n');
  const groups = [];
  let cur = null;
  const close = (end) => { if (cur) { cur.end = end; groups.push(cur); cur = null; } };

  for (let i = 0; i < lines.length; i++) {
    const h = lines[i].match(/^\s*\[\[?([^\]]+?)\]?\]\s*(#.*)?$/);
    if (h) {
      const top = h[1].split('.')[0].trim();
      if (!cur || cur.name !== top) {
        close(i);
        cur = { name: top, start: i, end: -1, keyCount: 0, subCount: 0 };
      } else {
        cur.subCount++; // another sub-table in the same group
      }
      if (h[1].includes('.')) cur.subCount++;
      continue;
    }
    if (/^\s*[A-Za-z0-9_\-.]+\s*=/.test(lines[i])) {
      if (!cur) { cur = { name: '(root)', start: 0, end: -1, keyCount: 0, subCount: 0 }; }
      cur.keyCount++;
    }
  }
  close(lines.length);
  // Trim trailing blank lines off each group's range so the slice is tidy.
  for (const g of groups) {
    let e = g.end;
    while (e > g.start && lines[e - 1].trim() === '') e--;
    g.end = e;
  }
  return groups;
}

// Merge the editor's current (possibly filtered) text back into state.toml.
function mergeBack() {
  const ta = document.getElementById('cfg-ta');
  if (!ta) return;
  if (state.active === '*' || !state.range) {
    state.toml = ta.value;
    return;
  }
  const lines = state.toml.split('\n');
  const edited = ta.value.split('\n');
  lines.splice(state.range.start, state.range.end - state.range.start, ...edited);
  state.toml = lines.join('\n');
}

// Compute the slice text + range for the active section, off the CURRENT toml.
function activeSlice() {
  if (state.active === '*') {
    state.range = null;
    return state.toml;
  }
  const groups = parseGroups(state.toml);
  const g = groups.find(x => x.name === state.active);
  if (!g) { state.range = null; return state.toml; }
  state.range = { start: g.start, end: g.end };
  return state.toml.split('\n').slice(g.start, g.end).join('\n');
}

function selectSection(name) {
  mergeBack();
  state.active = name;
  paint();
  // focus editor at top
  requestAnimationFrame(() => { const ta = document.getElementById('cfg-ta'); if (ta) ta.scrollTop = 0; });
}

// ----- actions -----

async function validate() {
  mergeBack();
  try {
    const r = await api.post('/config/validate', { toml: state.toml });
    if (r && r.error) { state.validation = { ok: false, error: r.error }; toast.warn('Ошибка валидации'); }
    else { state.validation = { ok: true }; toast.success('TOML валиден'); }
  } catch (e) { state.validation = { ok: false, error: e.message }; toast.error(e.message); }
  paint();
}

async function save() {
  mergeBack();
  state.saving = true; paint();
  try {
    const r = await api.post('/config/toml', { toml: state.toml });
    if (r && r.error && r.success === false) throw new Error(r.message || r.error);
    if (r && r.error && !r.success) throw new Error(r.message || r.error);
    state.dirty = false;
    state.validation = { ok: true };
    toast.success(r && r.reload_ok === false
      ? 'Сохранён, но hot-reload не удался: ' + (r.reload_error || '?')
      : (r && r.need_restart ? 'Сохранён — требуется рестарт' : 'Сохранён и применён'));
    await load();
  } catch (e) {
    toast.error('Сохранение не удалось: ' + e.message);
  } finally {
    state.saving = false;
    paint();
  }
}

async function rollback(name) {
  if (!confirm(`Откатить config.toml на бэкап ${name}?\nТекущий config сохранится как новый бэкап.`)) return;
  try {
    const r = await api.post('/config/rollback', { name });
    if (r && r.error) throw new Error(r.error);
    if (r && r.success === false) throw new Error(r.error || 'rollback failed');
    toast.success('Откат выполнен');
    await load();
    paint();
  } catch (e) { toast.error(e.message); }
}

// ----- highlight -----

function highlightTOML(text) {
  const esc = (s) => s.replace(/[&<>]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c]));
  return esc(text).split('\n').map(line => {
    if (/^\s*#/.test(line)) return `<span class="tk-c">${line}</span>`;
    let m = line.match(/^(\s*)(\[\[?[^\]]+\]?\])(.*)$/);
    if (m) return `${m[1]}<span class="tk-s">${m[2]}</span><span class="tk-c">${m[3]}</span>`;
    m = line.match(/^(\s*)([A-Za-z0-9_\-.]+)(\s*=\s*)(.*?)(\s*#.*)?$/);
    if (m) { const [, pad, key, eq, val, com] = m; return `${pad}<span class="tk-k">${key}</span>${eq}${highlightValue(val)}${com ? `<span class="tk-c">${com}</span>` : ''}`; }
    return line;
  }).join('\n');
}
function highlightValue(v) {
  v = v.trimEnd();
  if (!v) return v;
  if (/^(true|false)$/i.test(v)) return `<span class="tk-b">${v}</span>`;
  if (/^-?\d+(\.\d+)?$/.test(v))  return `<span class="tk-n">${v}</span>`;
  if (/^".*"$|^'.*'$/.test(v))    return `<span class="tk-str">${v}</span>`;
  if (/^\[/.test(v) && /\]$/.test(v)) return `<span class="tk-arr">${v}</span>`;
  return v;
}

function syncHighlight() {
  const ta = document.getElementById('cfg-ta');
  const hl = document.getElementById('cfg-hl');
  if (ta && hl) hl.innerHTML = highlightTOML(ta.value) + '\n';
}

// ----- paint -----

function paint() {
  const $root = document.getElementById('config-root');
  if (!$root) return;

  const groups = parseGroups(state.toml);
  const editorText = activeSlice();
  const f = state.filterNav.trim().toLowerCase();
  const navGroups = f
    ? groups.filter(g => g.name.toLowerCase().includes(f) || groupMeta(g.name).label.toLowerCase().includes(f))
    : groups;

  const activeLabel = state.active === '*' ? 'Все секции' : groupMeta(state.active).label;

  render(html`
    <section class="hero">
      <div class="hero-row">
        <div>
          <h1>Конфигурация</h1>
          <p>${state.path || 'config.toml'} · hot-reload после «Сохранить» ·
            <span class="src-tag">${state.source === 'generated' ? 'сгенерирован (файла нет)' : 'файл'}</span></p>
        </div>
        <div class="cfg-toolbar">
          ${state.dirty ? html`<span class="chip" style="color:var(--warn)">несохранено</span>` : ''}
          <l-button variant="secondary" icon="✓" @click=${validate} ?disabled=${state.loading}>Проверить</l-button>
          <l-button variant="primary" icon="💾" @click=${save} ?loading=${state.saving} ?disabled=${state.loading || state.saving}>Сохранить</l-button>
        </div>
      </div>
    </section>

    ${state.validation
      ? state.validation.ok
        ? html`<div class="cfg-valid ok">✓ TOML валиден</div>`
        : html`<div class="cfg-valid bad">✗ ${state.validation.error}</div>`
      : ''}

    ${state.loading ? html`<l-card loading title="Загрузка..."></l-card>` : html`
      <div class="cfg">
        <aside class="cfg-nav">
          <div class="cfg-nav-search">
            <l-input size="sm" icon="🔎" placeholder="Поиск секции…" .value=${state.filterNav}
                     clearable @input=${e => { mergeBack(); state.filterNav = e.detail.value; paint(); }}></l-input>
          </div>
          <div class="cfg-nav-list">
            <button class=${'cfg-nav-item all ' + (state.active === '*' ? 'active' : '')} @click=${() => selectSection('*')}>
              <span class="ni-ic">${icon('dashboard', 17)}</span>
              <span class="ni-label">Все секции</span>
              <span class="ni-count">${groups.reduce((a, g) => a + g.keyCount, 0)}</span>
            </button>
            ${navGroups.map(g => {
              const m = groupMeta(g.name);
              return html`
                <button class=${'cfg-nav-item ' + (state.active === g.name ? 'active' : '')} @click=${() => selectSection(g.name)} title=${'[' + g.name + ']'}>
                  <span class="ni-ic">${hasIcon(m.iconKey) ? icon(m.iconKey, 17) : icon('config', 17)}</span>
                  <span class="ni-label">${m.label}</span>
                  ${g.subCount > 0 ? html`<span class="ni-sub">${g.subCount}</span>` : ''}
                  <span class="ni-count">${g.keyCount}</span>
                </button>`;
            })}
          </div>

          ${state.backups && state.backups.length ? html`
            <div class="cfg-nav-h">Бэкапы</div>
            <div class="cfg-nav-list">
              ${state.backups.slice(0, 6).map(b => html`
                <button class="cfg-nav-bak" title="Откатить" @click=${() => rollback(typeof b === 'string' ? b : (b.name || b))}>
                  ${formatBackupName(typeof b === 'string' ? b : (b.name || b))}
                </button>`)}
            </div>` : ''}
        </aside>

        <l-card class="cfg-card" accent="violet">
          <div slot="title" style="display:flex;align-items:center;gap:10px">
            <span class="cfg-active-ic">${state.active === '*' ? icon('dashboard', 18) : icon(groupMeta(state.active).iconKey, 18)}</span>
            <span style="font-family:var(--font-display);font-weight:var(--fw-semibold)">${activeLabel}</span>
            ${state.active !== '*' ? html`<code class="cfg-active-name">[${state.active}]</code>` : ''}
          </div>
          <div class="cfg-editor">
            <pre class="cfg-hl" aria-hidden="true"><code id="cfg-hl" .innerHTML=${highlightTOML(editorText) + '\n'}></code></pre>
            <textarea id="cfg-ta" class="cfg-ta" spellcheck="false" autocorrect="off" autocapitalize="off"
              .value=${editorText}
              @input=${() => { state.dirty = true; state.validation = null; syncHighlight(); }}
              @scroll=${e => { const pre = document.getElementById('cfg-hl')?.parentElement; if (pre) { pre.scrollTop = e.target.scrollTop; pre.scrollLeft = e.target.scrollLeft; } }}></textarea>
          </div>
          <div class="cfg-foot">
            ${state.active === '*'
              ? html`<span class="cfg-foot-hint">Показан весь файл · ${state.toml.split('\n').length} строк</span>`
              : html`<span class="cfg-foot-hint">Секция <code>[${state.active}]</code> · правки сольются в полный конфиг при сохранении</span>`}
          </div>
        </l-card>
      </div>`}

    <style>${pageStyles}</style>
  `, $root);
}

function formatBackupName(n) {
  const m = String(n).match(/config_(\d{4})-(\d{2})-(\d{2})_(\d{2})-(\d{2})-(\d{2})/);
  if (!m) return n;
  const months = ['янв','фев','мар','апр','май','июн','июл','авг','сен','окт','ноя','дек'];
  return `${parseInt(m[3], 10)} ${months[parseInt(m[2], 10) - 1]} · ${m[4]}:${m[5]}`;
}

const pageStyles = `
  .hero { margin-bottom: var(--s-4); }
  .hero-row { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--s-4); flex-wrap: wrap; }
  .src-tag { font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-3); }
  .cfg-toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }

  .cfg-valid { margin-bottom: var(--s-3); padding: 8px 12px; border-radius: var(--r-2); font-size: var(--fs-sm); font-family: var(--font-mono); }
  .cfg-valid.ok  { background: var(--success-soft); color: var(--success); }
  .cfg-valid.bad { background: var(--danger-soft); color: var(--danger); white-space: pre-wrap; word-break: break-word; }

  .cfg { display: grid; grid-template-columns: 250px 1fr; gap: var(--s-4); align-items: start; }
  @media (max-width: 900px) { .cfg { grid-template-columns: 1fr; } }

  .cfg-nav {
    background: var(--bg-1); border: 1px solid var(--border-1); border-radius: var(--r-3);
    padding: var(--s-3); position: sticky; top: calc(var(--topbar-h) + var(--s-3));
    max-height: calc(100vh - var(--topbar-h) - var(--s-6)); overflow-y: auto;
    display: flex; flex-direction: column; gap: var(--s-2);
  }
  .cfg-nav-search { position: sticky; top: 0; }
  .cfg-nav-list { display: flex; flex-direction: column; gap: 2px; }
  .cfg-nav-h { font-size: var(--fs-xs); font-weight: 600; letter-spacing: 0.08em; text-transform: uppercase; color: var(--text-3); padding: var(--s-3) var(--s-2) 4px; border-top: 1px solid var(--border-1); margin-top: 4px; }

  .cfg-nav-item {
    display: flex; align-items: center; gap: 9px;
    text-align: left; padding: 8px 10px; background: transparent; border: 1px solid transparent;
    color: var(--text-1); font-size: var(--fs-sm); border-radius: var(--r-2); cursor: pointer;
    transition: background var(--t-fast), color var(--t-fast), border-color var(--t-fast);
  }
  .cfg-nav-item:hover { background: var(--bg-3); color: var(--text-0); }
  .cfg-nav-item.active {
    background: linear-gradient(135deg, var(--accent-soft), rgba(178,102,255,0.08));
    color: var(--text-0); border-color: rgba(119,145,255,0.28);
  }
  .cfg-nav-item.active .ni-ic { color: var(--accent); }
  .cfg-nav-item .ni-ic { color: var(--text-2); display: inline-flex; flex-shrink: 0; width: 18px; }
  .cfg-nav-item .ni-ic .lic { width: 17px; height: 17px; }
  .ni-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ni-sub { font-size: 9px; color: var(--text-3); background: var(--bg-3); padding: 1px 6px; border-radius: var(--r-pill); font-family: var(--font-mono); }
  .ni-count { font-size: var(--fs-xs); color: var(--text-3); font-family: var(--font-mono); min-width: 18px; text-align: right; }
  .cfg-nav-item.active .ni-count { color: var(--accent); }
  .cfg-nav-item.all { font-weight: var(--fw-semibold); }

  .cfg-nav-bak { text-align: left; padding: 6px 10px; background: transparent; border: none; color: var(--text-2); font-size: var(--fs-xs); font-family: var(--font-mono); border-radius: var(--r-2); cursor: pointer; display: flex; align-items: center; gap: 6px; }
  .cfg-nav-bak:hover { background: var(--warn-soft); color: var(--warn); }
  .cfg-nav-bak::before { content: '↺'; opacity: 0.6; }

  .cfg-active-ic { display: inline-flex; color: var(--accent); }
  .cfg-active-ic .lic { width: 18px; height: 18px; }
  .cfg-active-name { font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-3); background: var(--bg-2); padding: 2px 8px; border-radius: var(--r-2); }

  .cfg-editor { position: relative; background: var(--bg-0); border: 1px solid var(--border-1); border-radius: var(--r-2); overflow: hidden; margin-top: var(--s-2); }
  .cfg-ta, .cfg-hl { margin: 0; padding: 14px 16px; font-family: var(--font-mono); font-size: 13px; line-height: 1.55; tab-size: 2; white-space: pre; overflow: auto; height: calc(100vh - 340px); min-height: 380px; }
  .cfg-hl { position: absolute; inset: 0; pointer-events: none; color: var(--text-1); background: transparent; }
  .cfg-ta { position: relative; background: transparent; color: transparent; caret-color: var(--text-0); border: none; outline: none; resize: vertical; width: 100%; }
  .cfg-ta::selection { background: rgba(119,145,255,0.35); color: transparent; }

  .cfg-foot { margin-top: var(--s-2); }
  .cfg-foot-hint { font-size: var(--fs-xs); color: var(--text-3); }
  .cfg-foot-hint code { font-family: var(--font-mono); color: var(--text-2); }

  .tk-c { color: var(--text-3); font-style: italic; }
  .tk-s { color: #ec4899; font-weight: 600; }
  .tk-k { color: #93c5fd; }
  .tk-str { color: #6ee7b7; }
  .tk-n { color: #fbbf24; }
  .tk-b { color: #f472b6; font-weight: 600; }
  .tk-arr { color: #c4b5fd; }
`;

window.addEventListener('beforeunload', (e) => {
  if (state.dirty) { e.preventDefault(); e.returnValue = ''; }
});

export async function render_($mount) {
  $mount.innerHTML = `<div id="config-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
