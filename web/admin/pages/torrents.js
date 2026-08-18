// pages/torrents.js — live in-process TorrServer dashboard.
//
// Data: SSE stream from /admin/api/torrs/stream pushes a frame each second
// with {health, torrents, caches}.  REST fallback hits /torrs/health +
// /torrs/list every 5s when the stream isn't producing.
//
// API:
//   GET  /admin/api/torrs/health       → { mode, embedded, healthy, health }
//   GET  /admin/api/torrs/list         → { mode, embedded, torrents, settings }
//   POST /admin/api/torrs/action       → { action: remove|drop, hash }
//   GET  /admin/api/torrs/stream       → SSE: data: {health, torrents, caches}

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  health: null,
  torrents: [],
  caches: [],
  settings: null,
  mode: '',
  embedded: false,
  external: null,
  lastTick: 0,
};

let stream = null;
let pollTimer = null;

function fmtBytes(b) {
  if (b == null || !isFinite(b) || b <= 0) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return b.toFixed(b >= 10 || i === 0 ? 0 : 1) + ' ' + u[i];
}

function fmtSpeed(bps) {
  if (!bps || bps <= 0) return '—';
  return fmtBytes(bps) + '/s';
}

function fmtDuration(sec) {
  if (!sec || sec < 0) return '—';
  if (sec < 60) return sec + 's';
  if (sec < 3600) return Math.floor(sec / 60) + 'm';
  if (sec < 86400) return (sec / 3600).toFixed(1) + 'h';
  return (sec / 86400).toFixed(1) + 'd';
}

function progressPct(t, cache) {
  if (!t || !t.torrent_size || t.torrent_size <= 0) return 0;
  const done = t.bytes_completed || 0;
  return Math.min(100, (done / t.torrent_size) * 100);
}

async function loadOnce() {
  try {
    const [healthRes, listRes, externalRes] = await Promise.all([
      api.get('/torrs/health').catch(() => null),
      api.get('/torrs/list').catch(() => null),
      api.get('/torrs/external').catch(() => null),
    ]);
    if (externalRes) state.external = externalRes;
    if (healthRes) {
      state.health = healthRes.health;
      state.mode = healthRes.mode;
      state.embedded = !!healthRes.embedded;
    }
    if (listRes) {
      state.torrents = listRes.torrents || [];
      state.caches = state.torrents.map(t => null);
      state.settings = listRes.settings || null;
    }
    state.lastTick = Date.now();
  } catch (e) {
    toast.error('Не удалось загрузить torrs: ' + e.message);
  }
}

async function actOn(action, hash) {
  try {
    const r = await api.post('/torrs/action', { action, hash });
    if (r && r.ok === false) throw new Error(r.error || 'failed');
    toast.success(action === 'remove' ? 'Удалено' : 'Остановлено');
    await loadOnce();
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

let cleanupBusy = false;
async function runCleanup() {
  if (cleanupBusy) return;
  cleanupBusy = true;
  paint();
  try {
    const r = await api.post('/torrs/cleanup', {});
    if (r && r.ok === false) throw new Error(r.error || 'failed');
    const rep = r.report || {};
    const freed = Math.max(0, (rep.start_mb || 0) - (rep.end_mb || 0));
    toast.success(`Cleanup: -${freed} MB · удалено ${rep.age_removed + rep.size_removed} торрентов (${rep.end_mb}/${rep.limit_mb} MB)`);
    await loadOnce();
  } catch (e) {
    toast.error(e.message);
  } finally {
    cleanupBusy = false;
    paint();
  }
}

let $mountRef = null;

function copyToClipboard(text) {
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(text).then(
      () => toast.success('Скопировано'),
      () => toast.error('Не удалось скопировать'),
    );
  } else {
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
    toast.success('Скопировано');
  }
}

function renderExternalAccess() {
  const e = state.external;
  if (!e) return html``;
  if (!e.enable) {
    return html`
      <l-card title="Внешний доступ (для TorrServe / Vimu / MatriX UI)" style="margin-top:16px">
        <div style="color:var(--text-2);line-height:1.5">
          <b style="color:var(--text-3)">Отключён.</b> Чтобы открыть TorrServer
          сторонним приложениям, добавь в <code>config.toml</code>:
          <pre style="background:var(--bg-2);padding:12px;border-radius:6px;font-size:12px;overflow-x:auto;margin-top:8px">[torrserver.external_access]
enable     = true
login      = "ts"
password   = "&lt;сгенерируй надёжный пароль&gt;"
allow_from = ["127.0.0.1/8", "192.168.0.0/16"]  # опционально, CIDR-фильтр
listen_addr = ""                                  # ":9080" для отдельного порта</pre>
        </div>
      </l-card>
    `;
  }
  const samples = [
    e.sample_url,
    e.sample_curl,
  ].filter(Boolean);
  const cidrs = (e.allow_from || []).join(', ') || 'любые IP';

  return html`
    <l-card title="Внешний доступ (для TorrServe / Vimu / MatriX UI)" style="margin-top:16px">
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px;font-size:13px;color:var(--text-2)">
        <div><b style="color:var(--text-3)">Статус:</b> 🟢 Включён</div>
        <div><b style="color:var(--text-3)">Логин:</b> <code>${e.login || '—'}</code></div>
        <div><b style="color:var(--text-3)">Пароль:</b> ${e.password_set ? '🔒 задан (см. config.toml)' : '⚠ не задан'}</div>
        <div><b style="color:var(--text-3)">Доп. порт:</b> ${e.listen_addr || '—'}</div>
        <div style="grid-column:1/-1"><b style="color:var(--text-3)">Разрешённые сети:</b> ${cidrs}</div>
      </div>
      ${samples.length === 0 ? '' : html`
        <div style="margin-top:14px;display:flex;flex-direction:column;gap:8px">
          ${samples.map(line => html`
            <div style="display:flex;gap:8px;align-items:center">
              <code style="flex:1;background:var(--bg-2);padding:8px 10px;border-radius:6px;font-size:12px;overflow-x:auto;white-space:nowrap">${line}</code>
              <l-button size="sm" variant="ghost" @click=${() => copyToClipboard(line)} title="Скопировать">📋</l-button>
            </div>
          `)}
        </div>
      `}
      <div style="margin-top:12px;font-size:12px;color:var(--text-3)">
        Замени <code>&lt;password&gt;</code> на значение из <code>config.toml</code>.
        Используй любую сторонюю программу с поддержкой MatriX TorrServer (Echo вернёт <code>MatriX.API</code>).
      </div>
    </l-card>
  `;
}

function renderRow(t, cache, i) {
  const pct = progressPct(t, cache);
  const c = cache?.Torrent || cache?.torrent || {};
  const peers = c.connected_seeders ?? c.ConnectedSeeders ?? 0;
  const allPeers = c.total_peers ?? c.TotalPeers ?? 0;
  const dlSpeed = c.download_speed ?? c.DownloadSpeed ?? 0;
  const ulSpeed = c.upload_speed ?? c.UploadSpeed ?? 0;
  const remaining = (t.torrent_size || 0) - (t.bytes_completed || 0);
  const eta = dlSpeed > 0 && remaining > 0 ? remaining / dlSpeed : 0;

  return html`
    <tr>
      <td>
        <div style="font-weight:500;color:var(--text-1);max-width:340px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">
          ${t.title || t.name || t.hash.slice(0, 12)}
        </div>
        <div style="font-family:var(--font-mono);font-size:11px;color:var(--text-3);margin-top:2px">${t.hash.slice(0, 16)}…</div>
      </td>
      <td style="text-align:right;font-variant-numeric:tabular-nums">${fmtBytes(t.torrent_size)}</td>
      <td style="min-width:140px">
        <div style="display:flex;align-items:center;gap:8px">
          <div style="flex:1;height:6px;background:var(--bg-2);border-radius:3px;overflow:hidden">
            <div style="height:100%;width:${pct.toFixed(1)}%;background:linear-gradient(90deg,var(--accent),#34d399);transition:width .3s"></div>
          </div>
          <span style="font-size:12px;font-variant-numeric:tabular-nums;color:var(--text-2);min-width:38px;text-align:right">${pct.toFixed(0)}%</span>
        </div>
      </td>
      <td style="text-align:right;font-variant-numeric:tabular-nums">${peers}/${allPeers}</td>
      <td style="text-align:right;font-variant-numeric:tabular-nums;color:#34d399">${fmtSpeed(dlSpeed)}</td>
      <td style="text-align:right;font-variant-numeric:tabular-nums;color:#fbbf24">${fmtSpeed(ulSpeed)}</td>
      <td style="text-align:right;font-variant-numeric:tabular-nums;color:var(--text-3)">${fmtDuration(eta)}</td>
      <td style="text-align:right;white-space:nowrap">
        <l-button size="sm" variant="ghost" @click=${() => actOn('drop', t.hash)} title="Остановить (оставить в БД)">Drop</l-button>
        <l-button size="sm" variant="danger" @click=${() => actOn('remove', t.hash)} title="Удалить полностью">×</l-button>
      </td>
    </tr>
  `;
}

function tile(label, value, hint) {
  return html`
    <l-stat label=${label} .value=${value} ?hint=${!!hint} .hint=${hint || ''}></l-stat>
  `;
}

function paint() {
  if (!$mountRef) return;
  const h = state.health || {};

  // Proxy mode (external TorrServer): show a stub.
  if (!state.embedded) {
    render(html`
      <l-card title="Торренты">
        <div style="color:var(--text-2);line-height:1.5">
          Этот экземпляр работает в режиме <b>proxy</b> к внешнему TorrServer.
          Live-метрики и управление торрентами доступны только во встроенном режиме
          (сборка с <code>-tags torrs</code>, <code>[torrserver].url</code> пустой).
        </div>
      </l-card>
    `, $mountRef);
    return;
  }

  render(html`
    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px;margin-bottom:16px">
      ${tile('Торренты', h.num_torrents || 0)}
      ${tile('Диск', `${fmtBytes((h.disk_usage_mb||0)<<20)} / ${fmtBytes((h.disk_limit_mb||0)<<20)}`)}
      ${tile('Загружено', fmtBytes(h.bytes_completed))}
      ${tile('Скачано (сеть)', fmtBytes(h.bytes_read_useful))}
      ${tile('Отдано', fmtBytes(h.bytes_written))}
      ${tile('DHT нод', h.dht_nodes || 0)}
      ${tile('Порт', h.listen_port || '—')}
      ${tile('Uptime', fmtDuration(h.uptime_sec))}
    </div>

    <l-card title="Активные торренты" .actions=${html`
      <div style="display:flex;gap:10px;align-items:center;font-size:12px;color:var(--text-3)">
        <span title="DHT">${h.dht_enabled ? '🟢 DHT' : '⚪ DHT'}</span>
        <span title="Раздача">${h.upload_enabled ? '🟢 Upload' : '⚪ Upload'}</span>
        <span title="Auto-cleanup">${h.cleanup_enabled ? '🟢 Cleanup' : '⚪ Cleanup'}</span>
        <l-button size="sm" variant=${cleanupBusy ? 'ghost' : 'primary'} ?disabled=${cleanupBusy} @click=${runCleanup} title="Запустить cleanup-pass сейчас (age + size)">
          ${cleanupBusy ? 'Чистим…' : 'Cleanup сейчас'}
        </l-button>
      </div>
    `}>
      ${state.torrents.length === 0 ? html`
        <div style="padding:20px;text-align:center;color:var(--text-3)">Нет активных торрентов</div>
      ` : html`
        <div style="overflow-x:auto">
          <table style="width:100%;border-collapse:collapse">
            <thead>
              <tr style="text-align:left;border-bottom:1px solid var(--border-2);font-size:12px;color:var(--text-3)">
                <th style="padding:8px 6px">Название / hash</th>
                <th style="padding:8px 6px;text-align:right">Размер</th>
                <th style="padding:8px 6px">Прогресс</th>
                <th style="padding:8px 6px;text-align:right">Пиры</th>
                <th style="padding:8px 6px;text-align:right">↓</th>
                <th style="padding:8px 6px;text-align:right">↑</th>
                <th style="padding:8px 6px;text-align:right">ETA</th>
                <th style="padding:8px 6px;text-align:right"></th>
              </tr>
            </thead>
            <tbody>
              ${state.torrents.map((t, i) => renderRow(t, state.caches[i], i))}
            </tbody>
          </table>
        </div>
      `}
    </l-card>

    ${renderExternalAccess()}

    <l-card title="Настройки сервера" style="margin-top:16px">
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px;font-size:13px;color:var(--text-2)">
        <div><b style="color:var(--text-3)">Лимит активных:</b> ${h.max_active_torrents || '∞'}</div>
        <div><b style="color:var(--text-3)">Лимит скачивания:</b> ${h.max_download_speed_mbs ? h.max_download_speed_mbs + ' MB/s' : '∞'}</div>
        <div><b style="color:var(--text-3)">Лимит отдачи:</b> ${h.max_upload_speed_mbs ? h.max_upload_speed_mbs + ' MB/s' : '∞'}</div>
        <div><b style="color:var(--text-3)">Preload:</b> ${fmtBytes(state.settings?.PreloadSize)}</div>
        <div><b style="color:var(--text-3)">Read-ahead:</b> ${state.settings?.ReaderReadAHead || 0}%</div>
        <div><b style="color:var(--text-3)">Goroutines:</b> ${h.goroutines || 0}</div>
      </div>
      <div style="margin-top:12px;font-size:12px;color:var(--text-3)">
        Скоростные лимиты, лимит торрентов и расписание cleanup настраиваются в <code>config.toml</code>
        → секция <code>[torrserver]</code>. Изменения требуют рестарта.
      </div>
    </l-card>
  `, $mountRef);
}

export async function render_($mount) {
  $mountRef = $mount;
  // Lit-controlled loading skeleton so the next paint() cleanly replaces it.
  render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
  await loadOnce();
  paint();

  // SSE: backend pushes a frame every 1s while we're subscribed.
  if (stream) stream.close();
  stream = api.sse('/torrs/stream');
  stream.on('message', (raw) => {
    if (!raw) return;
    try {
      const data = typeof raw === 'string' ? JSON.parse(raw) : raw;
      state.health = data.health || state.health;
      state.torrents = data.torrents || state.torrents;
      state.caches = data.caches || state.caches;
      state.lastTick = Date.now();
      paint();
    } catch (e) {
      // Heartbeats / non-JSON frames: just bump the tick clock.
      state.lastTick = Date.now();
    }
  });

  // Polling fallback in case SSE went silent (proxy buffering, etc.).
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(async () => {
    if (document.visibilityState !== 'visible') return;
    if (Date.now() - state.lastTick < 5000) return;
    await loadOnce();
    paint();
  }, 5000);

  // Stop everything when the router swaps us out.
  const guard = new MutationObserver(() => {
    if (!document.body.contains($mount)) {
      if (stream) stream.close();
      stream = null;
      if (pollTimer) clearInterval(pollTimer);
      pollTimer = null;
      $mountRef = null;
      guard.disconnect();
    }
  });
  if ($mount.parentNode) guard.observe($mount.parentNode, { childList: true });
}

export { render_ as render };
