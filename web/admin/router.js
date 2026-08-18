// router.js — hash-based SPA router. Hash routes keep things simple: no
// server-side fall-through config needed, deep links work out of the box,
// and TG WebApp tolerates them well.
//
// API:
//   register({ key, title, loader })   loader: () => Promise<{render($mount, ctx)}>
//   setActive(key)
//   onChange(cb)                       cb({key, title}) when route changes

const routes = new Map();
const listeners = new Set();
let _current = '';

export function register(route) {
  if (!route || !route.key) throw new Error('route missing .key');
  routes.set(route.key, route);
}
export function list() { return Array.from(routes.values()); }
export function getRoute(key) { return routes.get(key); }
export function getActiveKey() { return _current; }

export function setActive(key) {
  if (!routes.has(key)) {
    // Default to the first registered route when the hash is unknown.
    const first = routes.keys().next().value;
    if (!first) return;
    key = first;
  }
  if (window.location.hash !== '#/' + key) {
    window.location.hash = '#/' + key;
    return; // hashchange listener will pick it up
  }
  if (key === _current) return;
  _current = key;
  const route = routes.get(key);
  listeners.forEach(cb => cb({ key, title: route.title }));
}

export function onChange(cb) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function start() {
  const parse = () => {
    const m = (window.location.hash || '').match(/^#\/([\w-]+)/);
    return m ? m[1] : '';
  };
  window.addEventListener('hashchange', () => {
    const key = parse();
    if (key) setActive(key);
  });
  const initial = parse() || (routes.keys().next().value || '');
  setActive(initial);
}

// Helper: lazy-load a page module and mount it. Pages export `render($mount, ctx)`.
//
// Each page gets a FRESH child element. We can't just `$mount.innerHTML = ''`
// — that strips Lit's marker-comment anchors but leaves its WeakMap-cached
// ChildPart pointing at the (now removed) marker nodes. Next page renders
// into the same container and Lit calls `parentNode.insertBefore` on a node
// whose parent is null. The error users hit on page switch:
//
//   Cannot read properties of null (reading 'insertBefore')
//     at lit.js (render → _$AI → insertBefore)
//     at <page>.js (its first render(html`...`, $mount) call)
//
// Giving each page its own fresh container scopes Lit's state per-page so
// inter-page navigation can't poison the renderer.
export async function mountPage(key, $mount, ctx) {
  const route = routes.get(key);
  if (!route) return;
  try {
    const mod = await route.loader();
    const fresh = document.createElement('div');
    $mount.replaceChildren(fresh);
    await mod.render(fresh, ctx);
  } catch (e) {
    $mount.innerHTML = `<div style="padding:24px;color:#f87171">Failed to load page: ${e.message}</div>`;
    console.error(e);
  }
}
