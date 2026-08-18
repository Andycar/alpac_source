// _lit.js — single import point for Lit. We vendor the bundle locally to
// remove the network dependency on first load. The bundle (web/admin/vendor/lit.js)
// is sourced from esm.sh's lit@3.2.1 ES2022 build and includes LitElement,
// html, css, render, nothing, svg, unsafeCSS — everything we need.
//
// classMap is provided as a tiny local implementation rather than vendoring
// the directive (which has its own internal Lit `directive()` plumbing) —
// we only need the "object → space-separated keys-where-truthy" behaviour
// which is a one-liner.

export {
  LitElement,
  html,
  css,
  render,
  nothing,
  svg,
  unsafeCSS,
} from '../vendor/lit.js';

/**
 * Minimal classMap directive replacement.
 * Usage:
 *   class="${classMap({ active: this.isActive, danger: this.bad })}"
 *
 * Returns a plain string of space-separated truthy keys. Lit binds it as
 * the `class` attribute, which is identical UX to the real directive for
 * our use cases (we never combine classMap with a static class on the same
 * attribute via mixing — we just write `class="base ${classMap(...)}"`).
 */
export function classMap(map) {
  if (!map) return '';
  const out = [];
  for (const k in map) {
    if (map[k]) out.push(k);
  }
  return out.join(' ');
}
