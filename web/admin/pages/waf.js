// pages/waf.js — Web Application Firewall: hot-reloadable config, IP/geo
// rules, brute-force, manual bans, 24h stats.
//
// API:
//   GET    /api/waf/state            → {config, stats, manualBans}
//   POST   /api/waf/config (body)    → save + hot-reload + return new state
//   POST   /api/waf/reload           → re-read init.conf
//   POST   /api/waf/ban   {ip,ttlSec,reason}
//   DELETE /api/waf/ban/:ip

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  loading: true,
  cfg: null,
  stats: null,
  bans: [],
  newBan: { ip: '', ttl: 3600, reason: '' },
};

async function load() {
  state.loading = true;
  paint();
  try {
    const d = await api.get('/waf/state');
    state.cfg = (d && d.config) || emptyCfg();
    state.stats = (d && d.stats) || {};
    state.bans = (d && d.manualBans) || [];
  } catch (e) {
    toast.error('Не удалось загрузить: ' + e.message);
    state.cfg = state.cfg || emptyCfg();
  } finally {
    state.loading = false;
    paint();
  }
}

function emptyCfg() {
  return {
    enable: false, bypassLocalIP: false, bruteForceProtection: false, bruteForceLimit: 0,
    whiteIps: [], limit_req: 0, limit_map: {}, ipsDeny: [], ipsAllow: [],
    countryDeny: [], countryAllow: [], headersDeny: {},
    customWhitelistPaths: [], customWhitelistPrefixes: [],
  };
}

async function save() {
  try {
    const d = await api.post('/waf/config', state.cfg);
    state.cfg = (d && d.config) || state.cfg;
    state.stats = (d && d.stats) || state.stats;
    state.bans = (d && d.manualBans) || state.bans;
    toast.success('Конфиг сохранён и применён');
    paint();
  } catch (e) { toast.error(e.message); }
}

async function reloadFromDisk() {
  try {
    const d = await api.post('/waf/reload', {});
    state.cfg = (d && d.config) || state.cfg;
    state.stats = (d && d.stats) || state.stats;
    state.bans = (d && d.manualBans) || state.bans;
    toast.info('Перечитан init.conf');
    paint();
  } catch (e) { toast.error(e.message); }
}

async function banAdd() {
  const ip = (state.newBan.ip || '').trim();
  if (!ip) return toast.warn('Укажите IP');
  try {
    const d = await api.post('/waf/ban', {
      ip,
      ttlSec: parseInt(state.newBan.ttl, 10) || 0,
      reason: (state.newBan.reason || '').trim(),
    });
    if (d && d.error) throw new Error(d.error);
    state.bans = (d && d.manualBans) || [];
    state.newBan = { ip: '', ttl: 3600, reason: '' };
    toast.success('Забанен: ' + ip);
    paint();
  } catch (e) { toast.error(e.message); }
}

async function banRemove(ip) {
  if (!confirm('Снять бан ' + ip + '?')) return;
  try {
    // POST /waf/ban/remove with body. DELETE /waf/ban/{ip} is also valid but
    // breaks on CIDR IPs ('/' in the path) because chi treats '%2F' as a
    // segment separator — the body form sidesteps that entirely.
    const d = await api.post('/waf/ban/remove', { ip });
    if (d && d.error) throw new Error(d.error);
    state.bans = (d && d.manualBans) || [];
    toast.success('Бан снят');
    paint();
  } catch (e) { toast.error(e.message); }
}

// --- parse/format helpers ---
const linesToArr  = (txt) => (txt || '').split(/\r?\n/).map(s => s.trim()).filter(Boolean);
const arrToLines  = (a)   => (a && a.length) ? a.join('\n') : '';
const csvToArr    = (s)   => (s || '').split(',').map(x => x.trim().toUpperCase()).filter(Boolean);
const arrToCsv    = (a)   => (a && a.length) ? a.join(', ') : '';

function parseLimitMap(txt) {
  const out = {};
  linesToArr(txt).forEach(line => {
    const parts = line.split('|');
    if (parts.length < 3) return;
    const pat = parts[0], lim = parseInt(parts[1], 10), sec = parseInt(parts[2], 10);
    if (!pat || !lim || !sec) return;
    const rule = { limit: lim, second: sec };
    for (let i = 3; i < parts.length; i++) {
      const p = parts[i];
      if (p === 'pathId') rule.pathId = true;
      else if (p.startsWith('queryIds=')) rule.queryIds = p.substring(9).split(',').map(s => s.trim()).filter(Boolean);
    }
    out[pat] = rule;
  });
  return out;
}
function limitMapToText(m) {
  if (!m) return '';
  return Object.entries(m).map(([k, r]) => {
    let line = k + '|' + (r.limit || 0) + '|' + (r.second || 0);
    if (r.pathId) line += '|pathId';
    if (r.queryIds && r.queryIds.length) line += '|queryIds=' + r.queryIds.join(',');
    return line;
  }).join('\n');
}
function parseHeadersDeny(txt) {
  const out = {};
  linesToArr(txt).forEach(line => {
    const i = line.indexOf('|');
    if (i <= 0) return;
    out[line.substring(0, i).trim()] = line.substring(i + 1);
  });
  return out;
}
function headersDenyToText(m) {
  if (!m) return '';
  return Object.entries(m).map(([k, v]) => k + '|' + v).join('\n');
}

function fmtTs(unix) {
  if (!unix || unix <= 0) return '—';
  return new Date(unix * 1000).toLocaleString('ru-RU', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' });
}
function fmtUntil(unix) {
  if (!unix || unix <= 0) return html`<l-pill tone="danger">постоянный</l-pill>`;
  const remain = unix - Math.floor(Date.now() / 1000);
  if (remain <= 0) return html`<span style="color:var(--text-3)">истёк</span>`;
  return fmtTs(unix);
}

function paint() {
  const $root = document.getElementById('waf-root');
  if (!$root) return;
  const c = state.cfg || emptyCfg();
  const s = state.stats || {};
  const total = s.total_24h || 0;
  const byReason = s.by_reason || {};
  const topPaths = s.top_paths || [];
  const topIps   = s.top_ips || [];
  const bansCount = (state.bans || []).length;

  render(html`
    <div class="page-summary">
      <l-stat accent="pink"   label="Блок 24ч"   value=${total}></l-stat>
      <l-stat accent="purple" label="Ручных банов" value=${bansCount}></l-stat>
      <l-stat accent="blue"   label="IP в deny"  value=${(c.ipsDeny || []).length}></l-stat>
      <l-stat accent="amber"  label="Странах deny" value=${(c.countryDeny || []).length}></l-stat>
    </div>

    <l-card title="Глобальные переключатели" style="margin-top:var(--s-5)">
      <div slot="actions" style="display:flex;gap:8px;">
        <l-button variant="secondary" icon="↻" @click=${async () => { await load(); }}>Обновить</l-button>
        <l-button variant="secondary" icon="⇣"
          title="Перечитать init.conf без сохранения"
          @click=${reloadFromDisk}>Из init.conf</l-button>
        <l-button variant="primary" icon="✓" @click=${save}>Сохранить</l-button>
      </div>
      <div class="waf-switches">
        <label class="waf-switch">
          <input type="checkbox" .checked=${!!c.enable}
            @change=${e => { c.enable = e.target.checked; paint(); }}>
          <span><b>Включить WAF</b><small>Без галочки — middleware passthrough</small></span>
        </label>
        <label class="waf-switch">
          <input type="checkbox" .checked=${!!c.bypassLocalIP}
            @change=${e => { c.bypassLocalIP = e.target.checked; paint(); }}>
          <span><b>Пропускать локальные IP</b><small>127.0.0.0/8, 10/8, 172.16/12, 192.168/16</small></span>
        </label>
        <label class="waf-switch">
          <input type="checkbox" .checked=${!!c.bruteForceProtection}
            @change=${e => { c.bruteForceProtection = e.target.checked; paint(); }}>
          <span><b>Brute-force защита</b><small>Лимит уникальных device-id с одного IP</small></span>
        </label>
      </div>
    </l-card>

    <l-card title="Статистика (24ч / 1ч)" style="margin-top:var(--s-5)">
      <div class="waf-stats-grid">
        <div>
          <div class="waf-stats-h">По причинам</div>
          ${Object.keys(byReason).length === 0
            ? html`<div style="color:var(--text-3)">Нет блокировок за последние 24ч</div>`
            : Object.entries(byReason).sort((a, b) => b[1] - a[1]).map(([k, v]) =>
                html`<div class="waf-stats-row"><span>${k}</span><b>${v}</b></div>`)}
        </div>
        <div>
          <div class="waf-stats-h">Топ путей (1ч)</div>
          ${topPaths.length === 0
            ? html`<div style="color:var(--text-3)">пусто</div>`
            : topPaths.slice(0, 8).map(p =>
                html`<div class="waf-stats-row"><code>${p.key}</code><b>${p.count}</b></div>`)}
        </div>
        <div>
          <div class="waf-stats-h">Топ IP (1ч)</div>
          ${topIps.length === 0
            ? html`<div style="color:var(--text-3)">пусто</div>`
            : topIps.slice(0, 8).map(p =>
                html`<div class="waf-stats-row"><code>${p.key}</code><b>${p.count}</b></div>`)}
        </div>
      </div>
    </l-card>

    <l-card title="Ручные баны (IP-уровень)" style="margin-top:var(--s-5)">
      <div class="waf-ban-form">
        <l-input placeholder="IP или CIDR (1.2.3.4 / 1.2.3.0/24)"
          .value=${state.newBan.ip}
          @input=${e => { state.newBan.ip = e.detail.value; }}
          style="flex:1;min-width:200px"></l-input>
        <l-input type="number" placeholder="TTL, сек (0 = ∞)"
          .value=${String(state.newBan.ttl)}
          @input=${e => { state.newBan.ttl = e.detail.value; }}
          style="width:160px"></l-input>
        <l-input placeholder="Причина"
          .value=${state.newBan.reason}
          @input=${e => { state.newBan.reason = e.detail.value; }}
          style="flex:1;min-width:160px"></l-input>
        <l-button variant="danger" icon="🚫" @click=${banAdd}>Забанить</l-button>
      </div>
      <l-table
        style="margin-top:var(--s-4)"
        .columns=${[
          { key: 'ip', label: 'IP / CIDR',
            cell: r => html`<code style="font-family:var(--font-mono);font-size:var(--fs-xs)">${r.ip}</code>` },
          { key: 'reason', label: 'Причина',
            cell: r => r.reason || html`<span style="color:var(--text-3)">—</span>` },
          { key: 'until', label: 'Истекает', cell: r => fmtUntil(r.until) },
          { key: 'bannedAt', label: 'Создан', cell: r => fmtTs(r.bannedAt) },
          { key: '_act', label: '', align: 'r',
            cell: r => html`<l-button size="sm" variant="danger" @click=${() => banRemove(r.ip)}>×</l-button>` },
        ]}
        .rows=${state.bans}
        empty="Нет ручных банов"></l-table>
    </l-card>

    <l-card title="Brute-force и rate-limit" style="margin-top:var(--s-5)">
      <div class="waf-row">
        <label class="waf-fld">
          <span>Лимит device-id с одного IP за минуту</span>
          <input type="number" class="waf-num" .value=${String(c.bruteForceLimit || 0)}
            @input=${e => { c.bruteForceLimit = parseInt(e.target.value, 10) || 0; }}>
        </label>
        <label class="waf-fld">
          <span>Глобальный limit_req (запросов/мин)</span>
          <input type="number" class="waf-num" .value=${String(c.limit_req || 0)}
            @input=${e => { c.limit_req = parseInt(e.target.value, 10) || 0; }}>
        </label>
      </div>
      <label class="waf-fld" style="margin-top:var(--s-4)">
        <span>Per-route правила (limit_map). Формат: <code>regex|limit|seconds[|pathId][|queryIds=a,b]</code></span>
        <textarea class="waf-ta" rows="5"
          .value=${limitMapToText(c.limit_map)}
          @input=${e => { c.limit_map = parseLimitMap(e.target.value); }}
          placeholder="^/api/admin|60|60&#10;^/lite/|120|60|pathId"></textarea>
      </label>
    </l-card>

    <l-card title="Списки IP / стран" style="margin-top:var(--s-5)">
      <div class="waf-grid2">
        <label class="waf-fld">
          <span>White-list IP (полный пропуск)</span>
          <textarea class="waf-ta" rows="4"
            .value=${arrToLines(c.whiteIps)}
            @input=${e => { c.whiteIps = linesToArr(e.target.value); }}
            placeholder="1.2.3.4&#10;10.0.0.0/8"></textarea>
        </label>
        <label class="waf-fld">
          <span>Whitelist путей (точные)</span>
          <textarea class="waf-ta" rows="4"
            .value=${arrToLines(c.customWhitelistPaths)}
            @input=${e => { c.customWhitelistPaths = linesToArr(e.target.value); }}
            placeholder="/healthz&#10;/api/version"></textarea>
        </label>
        <label class="waf-fld">
          <span>IP / CIDR — deny</span>
          <textarea class="waf-ta" rows="4"
            .value=${arrToLines(c.ipsDeny)}
            @input=${e => { c.ipsDeny = linesToArr(e.target.value); }}></textarea>
        </label>
        <label class="waf-fld">
          <span>IP / CIDR — allow (если задано — только эти)</span>
          <textarea class="waf-ta" rows="4"
            .value=${arrToLines(c.ipsAllow)}
            @input=${e => { c.ipsAllow = linesToArr(e.target.value); }}></textarea>
        </label>
        <label class="waf-fld">
          <span>Whitelist префиксы путей</span>
          <textarea class="waf-ta" rows="4"
            .value=${arrToLines(c.customWhitelistPrefixes)}
            @input=${e => { c.customWhitelistPrefixes = linesToArr(e.target.value); }}
            placeholder="/proxy/&#10;/static/"></textarea>
        </label>
        <div>
          <label class="waf-fld">
            <span>Страны — deny (ISO-2, через запятую)</span>
            <input type="text" class="waf-txt" .value=${arrToCsv(c.countryDeny)}
              @input=${e => { c.countryDeny = csvToArr(e.target.value); }}
              placeholder="CN, KP, IR">
          </label>
          <label class="waf-fld" style="margin-top:var(--s-3)">
            <span>Страны — allow (если задано — только эти)</span>
            <input type="text" class="waf-txt" .value=${arrToCsv(c.countryAllow)}
              @input=${e => { c.countryAllow = csvToArr(e.target.value); }}
              placeholder="RU, BY, KZ, UA">
          </label>
        </div>
      </div>
    </l-card>

    <l-card title="HTTP-заголовки — deny" style="margin-top:var(--s-5);margin-bottom:var(--s-5)">
      <label class="waf-fld">
        <span>По одному правилу в строке: <code>HeaderName|regex</code> (case-insensitive)</span>
        <textarea class="waf-ta" rows="4"
          .value=${headersDenyToText(c.headersDeny)}
          @input=${e => { c.headersDeny = parseHeadersDeny(e.target.value); }}
          placeholder="User-Agent|curl|wget|python-requests&#10;Referer|evil\\.com"></textarea>
      </label>
    </l-card>
  `, $root);
}

const styleId = 'l-waf-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }

    .waf-switches { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .waf-switches { grid-template-columns: 1fr; } }
    .waf-switch { display: flex; align-items: flex-start; gap: 10px; cursor: pointer; padding: 10px 12px; border: 1px solid var(--border-2); border-radius: var(--r-2); background: var(--bg-1); transition: background .15s; }
    .waf-switch:hover { background: var(--bg-2); }
    .waf-switch input { margin-top: 4px; }
    .waf-switch span { display: flex; flex-direction: column; gap: 2px; line-height: 1.3; font-size: var(--fs-sm); }
    .waf-switch small { color: var(--text-2); font-size: var(--fs-xs); }

    .waf-stats-grid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: var(--s-4); }
    @media (max-width: 900px) { .waf-stats-grid { grid-template-columns: 1fr; } }
    .waf-stats-h { font-size: var(--fs-xs); color: var(--text-2); text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 6px; font-weight: var(--fw-semibold); }
    .waf-stats-row { display: flex; justify-content: space-between; gap: 8px; padding: 4px 0; font-size: var(--fs-sm); border-bottom: 1px solid var(--border-3); }
    .waf-stats-row code { font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--text-1); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 240px; }
    .waf-stats-row b { color: var(--text-0); }

    .waf-ban-form { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }

    .waf-row { display: grid; grid-template-columns: 1fr 1fr; gap: var(--s-4); }
    @media (max-width: 900px) { .waf-row { grid-template-columns: 1fr; } }
    .waf-grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: var(--s-4); }
    @media (max-width: 900px) { .waf-grid2 { grid-template-columns: 1fr; } }
    .waf-fld { display: flex; flex-direction: column; gap: 6px; font-size: var(--fs-sm); }
    .waf-fld > span { color: var(--text-2); font-size: var(--fs-xs); }
    .waf-fld > span code { font-family: var(--font-mono); background: var(--bg-2); padding: 1px 5px; border-radius: 4px; color: var(--text-1); }
    .waf-num, .waf-txt, .waf-ta {
      background: var(--bg-0); border: 1px solid var(--border-2); border-radius: var(--r-2);
      padding: 8px 10px; color: var(--text-0); font-size: var(--fs-sm);
      font-family: inherit; transition: border-color .15s, box-shadow .15s;
    }
    .waf-ta { font-family: var(--font-mono); font-size: var(--fs-xs); resize: vertical; min-height: 80px; }
    .waf-num { width: 160px; }
    .waf-num:focus, .waf-txt:focus, .waf-ta:focus { border-color: var(--accent); outline: none; box-shadow: 0 0 0 3px var(--accent-soft); }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="waf-root"></div>`;
  paint();
  await load();
}
export { render_ as render };
