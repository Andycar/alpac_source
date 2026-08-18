// pages/jacred.js — embedded web UI of the self-hosted jacred torrent parser.
//
// The jacred instance binds to 127.0.0.1 only; /jacred/ is the sole way in
// (jacredWebUIHandler proxies it behind the admin auth gate). We embed that
// proxy in an iframe rather than reimplementing its search/stats screens.
//
// The route is only registered when [parser] jacred_local is on (see app.js),
// so this page assumes the feature is enabled — but the instance may still be
// installing, bootstrapping its DB, or down. We surface that state above the
// frame instead of leaving the user staring at a blank/error iframe.
//
// API: GET /{adminPath}/api/jacred/status
//      → { enabled, installed, running, healthy, stage, version, db_size_mb, url, error?, port_conflict? }
// port_conflict=true means a foreign process (e.g. an external jac.red on the
// same host) is holding our port; the server then also sets `error`, so the
// status renders as an error below rather than a false "работает".

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';

const FRAME_URL = '/jacred/';

let status = null;
let poll = null;
let mountEl = null;

// A jacred that isn't healthy yet (installing / bootstrapping the FDB dump)
// becomes healthy on its own — keep polling so the frame loads itself once
// it's up, instead of making the admin hit reload.
function schedulePoll() {
  clearTimeout(poll);
  if (status && status.healthy) return;
  poll = setTimeout(refresh, 5000);
}

async function refresh() {
  try {
    status = await api.get('/jacred/status');
  } catch (e) {
    status = { enabled: true, error: e && e.message ? e.message : 'нет связи с сервером' };
  }
  if (!mountEl || !mountEl.isConnected) { clearTimeout(poll); return; }
  draw();
  schedulePoll();
}

function stateLine() {
  if (!status) return { dot: 'wait', text: 'проверяем…' };
  if (status.error) return { dot: 'err', text: status.error };
  if (!status.installed) return { dot: 'err', text: 'не установлен — поставьте JacRed на вкладке «Зависимости»' };
  if (status.healthy) {
    const db = status.db_size_mb ? `, база ${status.db_size_mb} МБ` : '';
    return { dot: 'ok', text: `работает${status.version ? ' · ' + status.version : ''}${db}` };
  }
  return { dot: 'wait', text: status.stage || 'запускается' };
}

function draw() {
  const s = stateLine();
  render(html`
    <style>
      .jr-head { display:flex; align-items:center; gap:.75rem; margin-bottom:.75rem; flex-wrap:wrap; }
      .jr-title { font-size:1.05rem; font-weight:600; }
      .jr-state { display:flex; align-items:center; gap:.4rem; font-size:.85rem; opacity:.75; }
      .jr-dot { width:.5rem; height:.5rem; border-radius:50%; flex:0 0 auto; }
      .jr-dot.ok { background:#3ddc97; }
      .jr-dot.wait { background:#f5c451; }
      .jr-dot.err { background:#ff6b6b; }
      .jr-spacer { flex:1; }
      .jr-link { font-size:.82rem; opacity:.7; text-decoration:none; border:1px solid currentColor;
                 border-radius:.5rem; padding:.25rem .6rem; }
      .jr-link:hover { opacity:1; }
      /* Fill the viewport below the header; the jacred UI scrolls inside. */
      .jr-frame { width:100%; height:calc(100vh - 12rem); min-height:28rem; border:1px solid rgba(255,255,255,.12);
                  border-radius:.75rem; background:#111; }
      .jr-note { padding:2rem; text-align:center; opacity:.6; font-size:.9rem; line-height:1.6; }
    </style>

    <div class="jr-head">
      <div class="jr-title">Парсер jacred</div>
      <div class="jr-state"><span class="jr-dot ${s.dot}"></span>${s.text}</div>
      <div class="jr-spacer"></div>
      <a class="jr-link" href="${FRAME_URL}" target="_blank" rel="noopener">Открыть в новой вкладке ↗</a>
    </div>

    ${status && status.healthy
      ? html`<iframe class="jr-frame" src="${FRAME_URL}" title="jacred"></iframe>`
      : html`<div class="jr-note">
               Веб-интерфейс появится, когда jacred поднимется.<br>
               Состояние обновляется автоматически каждые 5 секунд.
             </div>`}
  `, mountEl);
}

export async function render_($mount) {
  mountEl = $mount;
  status = null;
  draw();       // immediate "проверяем…" placeholder
  await refresh();
}
export { render_ as render };
