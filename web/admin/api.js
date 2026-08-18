// api.js — single source of truth for /api calls. Computes the admin base
// path from window.location (we live under /<adminPath>/v2/ or /tg-admin/)
// and exposes high-level helpers per resource.
//
// All requests are JSON; non-2xx is thrown as an Error with .status + .body.
// On 401/403 the page redirects to login (for cookie auth) or, in TG mode,
// re-runs initData exchange.

import { toast } from './components/l-toast.js';

const cookieMode = !window.location.pathname.startsWith('/tg-admin/');

// Compute the API root. For cookie mode we're at /<adminPath>/v2/ so the
// existing admin API lives at /<adminPath>/api/. For TG mode we're at
// /tg-admin/ and the same API is reachable via the auth-issued cookie.
function apiRoot() {
  if (cookieMode) {
    const segs = window.location.pathname.split('/').filter(Boolean);
    // segs = ["<adminPath>", "v2", ...]
    const adminPath = segs[0] || '';
    return '/' + adminPath + '/api';
  }
  // TG mode: we still need an admin path to hit; resolve from /api/tg-admin/whoami
  // at startup, cached for the session.
  return window.__adminApiBase || '/api';
}

export async function call(method, path, body) {
  const url = apiRoot() + path;
  const opts = {
    method,
    credentials: 'include',
    headers: { 'Accept': 'application/json' },
  };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = typeof body === 'string' ? body : JSON.stringify(body);
  }
  let resp;
  try {
    resp = await fetch(url, opts);
  } catch (e) {
    toast.error('Network error: ' + e.message);
    throw e;
  }
  const text = await resp.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!resp.ok) {
    const err = new Error('HTTP ' + resp.status + (data && data.error ? ': ' + data.error : ''));
    err.status = resp.status;
    err.body = data;
    // Reload ONLY on 401 (session expired / no cookie) — the admin auth flow
    // redirects to /tg/auth or /login on the next request. NEVER reload on 403:
    // that's a *permission* error (e.g. endpoint is super-admin-only and the
    // caller is a regular admin). Reloading on 403 just bounces the SPA in a
    // loop and hides the real reason — let the page surface the error instead.
    if (resp.status === 401 && cookieMode) {
      window.location.reload();
    }
    throw err;
  }
  return data;
}

/**
 * Open an SSE stream rooted at the admin API. Returns an object with:
 *   - close()              terminate the stream
 *   - on(event, handler)   subscribe to a named SSE event
 *
 * Reconnect-on-error is built in (exponential backoff capped at 30s). The
 * caller can stop reconnects by calling close().
 *
 * Usage:
 *   const sse = api.sse('/telemetry/stream', { query: { window: '5m' } });
 *   sse.on('snapshot', payload => render(payload));
 *   ... later ...
 *   sse.close();
 */
export function sse(path, opts = {}) {
  const handlers = new Map(); // event name → Set<fn>
  let es = null;
  let closed = false;
  let attempt = 0;
  let reconnectTimer = null;

  const fullURL = () => {
    let url = apiRoot() + path;
    if (opts.query) {
      const sp = new URLSearchParams(opts.query);
      url += (url.includes('?') ? '&' : '?') + sp.toString();
    }
    return url;
  };

  function connect() {
    if (closed) return;
    es = new EventSource(fullURL(), { withCredentials: true });
    es.onopen = () => { attempt = 0; };
    es.onerror = () => {
      if (closed) return;
      try { es.close(); } catch {}
      es = null;
      attempt = Math.min(attempt + 1, 6);
      const delay = Math.min(30000, 1000 * Math.pow(1.7, attempt));
      reconnectTimer = setTimeout(connect, delay);
    };
    es.onmessage = (ev) => {
      dispatch('message', ev);
    };
    // The Telegram-aware logs/telemetry streams emit named events; bind
    // listeners as handlers are registered.
    for (const [name] of handlers) {
      if (name === 'message') continue;
      es.addEventListener(name, (ev) => dispatch(name, ev));
    }
  }

  function dispatch(name, ev) {
    const set = handlers.get(name);
    if (!set) return;
    let data = ev.data;
    try { data = JSON.parse(ev.data); } catch {}
    set.forEach(fn => { try { fn(data, ev); } catch (e) { console.error(e); } });
  }

  return {
    on(name, fn) {
      if (!handlers.has(name)) handlers.set(name, new Set());
      handlers.get(name).add(fn);
      if (es && name !== 'message') {
        es.addEventListener(name, (ev) => dispatch(name, ev));
      }
      // First listener triggers initial connect.
      if (!es) connect();
      return () => handlers.get(name)?.delete(fn);
    },
    close() {
      closed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (es) try { es.close(); } catch {}
      es = null;
      handlers.clear();
    },
  };
}

export const api = {
  get:  (path)        => call('GET',  path),
  post: (path, body)  => call('POST', path, body),
  put:  (path, body)  => call('PUT',  path, body),
  del:  (path)        => call('DELETE', path),
  sse,

  // Resource-shaped helpers — fill in as new pages need them.
  whoami:    () => api.get('/whoami'),
  stats:     () => api.get('/stats'),
  users:     () => api.get('/users'),
  balancers: () => api.get('/balancers'),
  telemetry: () => api.get('/telemetry'),
  logs:      (level) => api.get('/logs' + (level ? `?level=${encodeURIComponent(level)}` : '')),

  // Browser engine + per-balancer overrides
  browserpool:        () => api.get('/browserpool'),
  saveBrowserpool:    (body) => api.post('/browserpool', body),
  installBrowser:     (engine) => api.post('/browser/install', { engine }),

  // Groups
  groups:            () => api.get('/groups'),
  groupsAction:      (body) => api.post('/groups', body),

  // TorrServer balancer (pool of backend TorrServers)
  torrbalancer:       () => api.get('/torrbalancer'),
  torrbalancerAction: (body) => api.post('/torrbalancer', body),

  // TG bot settings
  tgsettings:        () => api.get('/tgsettings'),
  saveTGSettings:    (body) => api.post('/tgsettings', body),
  regenAdminPath:    () => api.post('/tgsettings/regen-path', {}),

  // External dependencies
  deps:              (checkLatest) => api.get('/deps' + (checkLatest ? '?check_latest=1' : '')),
  updateDep:         (binary) => api.post('/deps/update', { binary }),
  depUpdateStatus:   (binary) => api.get('/deps/update/status?binary=' + encodeURIComponent(binary)),

  // AppReplace string-replacement rules — POST takes a bare array
  appreplace:        () => api.get('/appreplace'),
  saveAppreplace:    (rules) => api.post('/appreplace', rules),

  // Password users (alternative auth)
  passwordUsers:       () => api.get('/password-users'),
  passwordUsersAction: (body) => api.post('/password-users', body),

  // Auth mode (tg / password / none)
  authMode:        () => api.get('/auth-mode'),
  saveAuthMode:    (mode) => api.post('/auth-mode', { mode }),
};
