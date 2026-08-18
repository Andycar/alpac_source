// pages/browser-engine.js — Chrome stealth-engine selector + browser pool
// runtime tuning. Picks between chromedp / playwright (or any registered
// engine) globally, per-balancer overrides for special cases (mirage often
// needs playwright when chromedp's TLS fingerprint gets flagged).
//
// API:
//   GET  /api/browserpool       → { engine, balancer_engines, available_engines,
//                                   engine_status, balancers, max_concurrent,
//                                   active_sessions, stream_cache_max, stream_cache_ttl_h }
//   POST /api/browserpool       → save { engine, balancer_engines, max_concurrent,
//                                       stream_cache_max, stream_cache_ttl_h }
//   POST /api/browser/install   → { engine } — triggers install (blocking)

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = null;
let installing = false;

async function fetchState() {
  state = await api.browserpool();
  return state;
}

async function paint($mount) {
  if (!state) {
    render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
    try { await fetchState(); } catch (e) {
      render(html`<l-card><div style="color:var(--danger)">Не удалось загрузить: ${e.message}</div></l-card>`, $mount);
      return;
    }
  }

  const engines = state.available_engines || [];
  const status  = state.engine_status || {};
  const current = state.engine || engines[0] || 'chromedp';
  const overrides = state.balancer_engines || {};
  const balancers = state.balancers || [];

  const engineStatusLabel = (eng) => {
    const s = status[eng];
    if (!s) return null;
    if (/installed|ready|ok/i.test(String(s))) return html`<span class="pill ok"><span class="dot"></span> ${s}</span>`;
    if (/missing|not installed|absent/i.test(String(s))) return html`<span class="pill warn"><span class="dot"></span> ${s}</span>`;
    return html`<span class="pill muted"><span class="dot"></span> ${s}</span>`;
  };

  render(html`
    <section class="hero">
      <h1>Браузерный движок</h1>
      <p>Stealth Chromium для балансеров с anti-bot защитой — Mirage, Aladdin, Ashdi, Turbo. Выбирай движок глобально или перекрывай для конкретного источника.</p>
      <div class="hero-actions">
        <span class="chip accent">текущий: ${current}</span>
        <span class="chip">движков ${engines.length}</span>
        <span class="chip">активных сессий ${state.active_sessions ?? 0}/${state.max_concurrent ?? 4}</span>
      </div>
    </section>

    <div class="section-title">Глобальный движок</div>
    <div class="grid cols-3">
      ${engines.map(eng => html`
        <l-card
          accent=${eng === current ? 'violet' : ''}
          hover
          @click=${() => pickEngine($mount, eng)}
          style=${eng === current ? 'border-color:var(--accent);box-shadow:var(--shadow-glow)' : ''}
        >
          <div class="engine-card">
            <div class="engine-icon" data-eng=${eng}>${engineGlyph(eng)}</div>
            <div class="engine-info">
              <div class="engine-name">${prettyEngine(eng)}</div>
              <div class="engine-meta">
                ${eng === current ? html`<span class="pill ok"><span class="dot"></span> активен</span>` : ''}
                ${engineStatusLabel(eng) ?? ''}
                ${needsInstall(eng, status) ? html`
                  <button class="btn-install" ?disabled=${installing} @click=${(e) => { e.stopPropagation(); installEngine($mount, eng); }}>
                    ${installing ? 'устанавливаю…' : 'установить'}
                  </button>
                ` : ''}
              </div>
              <div class="engine-desc">${engineDescription(eng)}</div>
            </div>
          </div>
        </l-card>
      `)}
    </div>

    <div class="section-title">Per-balancer overrides</div>
    <l-card title="Переопределение по балансерам" subtitle="полезно когда mirage чувствительнее к chromedp, или ashdi нужен playwright" accent="cyan">
      <div class="override-grid">
        ${balancers.map(bal => html`
          <div class="override-row">
            <div class="override-bal">
              <div class="bal-icon">${(bal || '?').charAt(0).toUpperCase()}</div>
              <div class="bal-name">${bal}</div>
            </div>
            <select class="override-sel" @change=${(e) => setOverride($mount, bal, e.target.value)}>
              <option value="">— global (${current}) —</option>
              ${engines.map(eng => html`<option value=${eng} ?selected=${overrides[bal] === eng}>${prettyEngine(eng)}</option>`)}
            </select>
          </div>
        `)}
      </div>
    </l-card>

    <div class="section-title">Пул сессий</div>
    <div class="grid cols-2">
      <l-card title="Параллелизм" subtitle="одновременных Chrome-сессий" accent="amber">
        <div class="slider-wrap">
          <input id="max-conc" type="range" min="1" max="32" step="1" value=${state.max_concurrent || 4}
                 @input=${(e) => updateSliderLabel('max-conc', e.target.value)}/>
          <div class="slider-meta">
            <span class="slider-value" id="max-conc-v">${state.max_concurrent || 4}</span>
            <span class="slider-hint">активных: ${state.active_sessions ?? 0}</span>
          </div>
        </div>
        <div style="display:flex;gap:8px;margin-top:var(--s-3)">
          <button class="btn-save" @click=${() => savePool($mount)}>Применить</button>
        </div>
      </l-card>

      <l-card title="Кэш стримов" subtitle="re-resolved URLs для Mirage" accent="mint">
        <div class="kv-grid">
          <label>
            <span>Max entries</span>
            <input id="stream-cache-max" type="number" min="10" max="5000" value=${state.stream_cache_max || 200}/>
          </label>
          <label>
            <span>TTL (часы)</span>
            <input id="stream-cache-ttl" type="number" min="1" max="168" value=${state.stream_cache_ttl_h || 6}/>
          </label>
        </div>
        <div style="display:flex;gap:8px;margin-top:var(--s-3)">
          <button class="btn-save" @click=${() => savePool($mount)}>Применить</button>
        </div>
      </l-card>
    </div>

    <style>
      .engine-card { display: flex; gap: var(--s-3); align-items: flex-start; }
      .engine-icon {
        width: 56px; height: 56px; flex-shrink: 0;
        border-radius: var(--r-3);
        background: var(--g-accent);
        display: grid; place-items: center;
        font-size: 28px;
        font-weight: var(--fw-bold);
        color: white;
        box-shadow: var(--shadow-md);
      }
      .engine-icon[data-eng="chromedp"]    { background: var(--g-cyan); }
      .engine-icon[data-eng="playwright"]  { background: var(--g-fire); }
      .engine-icon[data-eng="puppeteer"]   { background: var(--g-amber); }
      .engine-info { min-width: 0; flex: 1; }
      .engine-name { font-family: var(--font-display); font-weight: var(--fw-semibold); font-size: var(--fs-md); letter-spacing: -0.01em; }
      .engine-meta { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 4px; align-items: center; }
      .engine-desc { font-size: var(--fs-xs); color: var(--text-2); margin-top: 6px; line-height: 1.4; }
      .btn-install {
        background: var(--accent-soft); color: var(--accent);
        border: 1px solid rgba(119,145,255,0.32);
        padding: 3px 10px; font-size: var(--fs-xs); border-radius: var(--r-pill);
        cursor: pointer; font-weight: var(--fw-semibold);
      }
      .btn-install:hover { background: var(--accent); color: white; }
      .btn-install:disabled { opacity: 0.5; cursor: progress; }

      .override-grid { display: grid; gap: var(--s-2); grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); }
      .override-row {
        display: flex; align-items: center; gap: var(--s-3);
        padding: 8px 12px;
        background: var(--bg-2);
        border: 1px solid var(--border-1);
        border-radius: var(--r-2);
      }
      .override-bal { display: flex; align-items: center; gap: 10px; flex: 1; min-width: 0; }
      .bal-icon { width: 28px; height: 28px; border-radius: 8px; background: var(--g-mint); display: grid; place-items: center; font-weight: var(--fw-bold); color: #0f1024; }
      .bal-name { font-family: var(--font-mono); font-size: 12.5px; }
      .override-sel {
        background: var(--bg-1); color: var(--text-0);
        border: 1px solid var(--border-2);
        padding: 5px 8px; border-radius: var(--r-2);
        font-size: var(--fs-sm);
      }

      .slider-wrap { display: flex; flex-direction: column; gap: var(--s-2); }
      .slider-wrap input[type=range] { width: 100%; accent-color: var(--accent); }
      .slider-meta { display: flex; justify-content: space-between; font-size: var(--fs-sm); }
      .slider-value { font-family: var(--font-display); font-weight: var(--fw-bold); font-size: var(--fs-xl); background: var(--g-text-accent); -webkit-background-clip: text; background-clip: text; color: transparent; }
      .slider-hint { color: var(--text-2); }

      .kv-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--s-3); }
      .kv-grid label { display: flex; flex-direction: column; gap: 4px; font-size: var(--fs-sm); color: var(--text-2); }
      .kv-grid input { background: var(--bg-2); border: 1px solid var(--border-2); padding: 8px 10px; border-radius: var(--r-2); color: var(--text-0); font-family: var(--font-mono); }
      .kv-grid input:focus { border-color: var(--accent); outline: none; box-shadow: 0 0 0 3px var(--accent-soft); }

      .btn-save {
        background: var(--g-accent);
        color: white; border: 0;
        padding: 8px 16px; border-radius: var(--r-pill);
        font-weight: var(--fw-semibold); cursor: pointer;
        box-shadow: var(--shadow-md);
        transition: transform 120ms var(--ease-spring);
      }
      .btn-save:hover { transform: translateY(-1px); box-shadow: var(--shadow-glow); }
      .btn-save:active { transform: translateY(0); }
    </style>
  `, $mount);
}

function updateSliderLabel(id, v) {
  const el = document.getElementById(id + '-v');
  if (el) el.textContent = v;
}

function prettyEngine(name) {
  const map = { chromedp: 'ChromeDP', playwright: 'Playwright', puppeteer: 'Puppeteer' };
  return map[name] || name;
}
function engineGlyph(name) {
  return ({ chromedp: '◐', playwright: '◇', puppeteer: '◉' })[name] || '◇';
}
function engineDescription(name) {
  return ({
    chromedp:   'CDP-нативно из Go. Быстрый старт, минимум зависимостей, базовый stealth.',
    playwright: 'Stealth-флагман: bypass Cloudflare, обход TLS-фингерпринтов. Требует Node + driver.',
    puppeteer:  'Альтернатива Playwright. Менее агрессивный stealth, проще в установке.',
  })[name] || 'Browser engine';
}
function needsInstall(eng, status) {
  const s = status[eng];
  return s && /missing|not installed|absent/i.test(String(s));
}

async function pickEngine($mount, eng) {
  if (state.engine === eng) return;
  try {
    const r = await api.saveBrowserpool({ engine: eng });
    state = r;
    toast.success(`Движок переключён → ${prettyEngine(eng)}`);
    paint($mount);
  } catch (e) {
    toast.error('Не удалось: ' + e.message);
  }
}

async function setOverride($mount, balancer, eng) {
  const overrides = { ...(state.balancer_engines || {}) };
  if (eng) overrides[balancer] = eng;
  else delete overrides[balancer];
  try {
    const r = await api.saveBrowserpool({ balancer_engines: overrides });
    state = r;
    toast.success(`${balancer}: ${eng || 'global'}`);
  } catch (e) {
    toast.error(e.message);
  }
}

async function savePool($mount) {
  const body = {
    max_concurrent:     +document.getElementById('max-conc').value,
    stream_cache_max:   +document.getElementById('stream-cache-max').value,
    stream_cache_ttl_h: +document.getElementById('stream-cache-ttl').value,
  };
  try {
    const r = await api.saveBrowserpool(body);
    state = r;
    toast.success('Сохранено');
    paint($mount);
  } catch (e) {
    toast.error(e.message);
  }
}

async function installEngine($mount, eng) {
  installing = true;
  paint($mount);
  toast.info(`Устанавливаю ${prettyEngine(eng)}…`);
  try {
    await api.installBrowser(eng);
    toast.success(`${prettyEngine(eng)} установлен`);
    state = await fetchState();
  } catch (e) {
    toast.error('Не получилось: ' + e.message);
  } finally {
    installing = false;
    paint($mount);
  }
}

export async function render_($mount) {
  state = null;
  await paint($mount);
}

export { render_ as render };
