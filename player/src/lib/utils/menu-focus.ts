/**
 * Svelte action: auto-focus the first focusable item in a menu when it mounts.
 * Usage: <div class="lwp-menu" use:autoFocusMenu>
 */
export function autoFocusMenu(node: HTMLElement) {
  // Small delay to let items render
  requestAnimationFrame(() => {
    const first = node.querySelector<HTMLElement>(
      '.lwp-menu-item[tabindex], .lwp-chip, button',
    );
    if (first) first.focus();
  });
}

// ---------------------------------------------------------------------------
// 2D spatial navigation (Wave 1 D1) — used by D-pad menu nav in keyboard.ts.
// ---------------------------------------------------------------------------

export type SpatialDirection = 'up' | 'down' | 'left' | 'right';

const FOCUSABLE_SEL =
  '.lwp-menu-item[tabindex], .lwp-chip, button, [tabindex="0"], a[href], input:not([disabled]), select, textarea';

/**
 * Find the focusable element nearest to `from` in the given direction
 * inside `root`. Uses bounding-box geometry: among elements that lie in
 * the half-plane of the direction, picks the one with the smallest
 * weighted distance (axis-aligned distance counts double to bias toward
 * elements actually "in line" with the source).
 *
 * Falls back to a simple list-order next/prev when geometry is ambiguous
 * (all elements stacked at the same x/y) so vertical menu lists keep
 * behaving as before.
 */
export function findNeighbor(
  from: HTMLElement,
  root: HTMLElement,
  direction: SpatialDirection,
): HTMLElement | null {
  const items = Array.from(
    root.querySelectorAll<HTMLElement>(FOCUSABLE_SEL),
  ).filter((el) => el !== from && el.offsetParent !== null);
  if (items.length === 0) return null;

  const fromRect = from.getBoundingClientRect();
  const fcx = fromRect.left + fromRect.width / 2;
  const fcy = fromRect.top + fromRect.height / 2;

  let best: HTMLElement | null = null;
  let bestScore = Infinity;

  for (const el of items) {
    const r = el.getBoundingClientRect();
    const cx = r.left + r.width / 2;
    const cy = r.top + r.height / 2;
    const dx = cx - fcx;
    const dy = cy - fcy;

    // Filter to the directional half-plane.
    let primary = 0;
    let secondary = 0;
    switch (direction) {
      case 'up':    if (dy >= -1) continue; primary = -dy; secondary = Math.abs(dx); break;
      case 'down':  if (dy <= 1)  continue; primary =  dy; secondary = Math.abs(dx); break;
      case 'left':  if (dx >= -1) continue; primary = -dx; secondary = Math.abs(dy); break;
      case 'right': if (dx <= 1)  continue; primary =  dx; secondary = Math.abs(dy); break;
    }

    // Bias toward elements actually in line with the source.
    const score = primary + secondary * 2;
    if (score < bestScore) {
      bestScore = score;
      best = el;
    }
  }

  // Geometric pick succeeded.
  if (best) return best;

  // Fallback: pick by document order in the appropriate direction.
  // Useful when all menu items live in a single vertical list and the
  // strict half-plane filter rejects everything (e.g. user pressed Right).
  const allItems = items.concat(from);
  const ordered = allItems.sort((a, b) => {
    const pos = a.compareDocumentPosition(b);
    if (pos & Node.DOCUMENT_POSITION_FOLLOWING) return -1;
    if (pos & Node.DOCUMENT_POSITION_PRECEDING) return 1;
    return 0;
  });
  const idx = ordered.indexOf(from);
  if (direction === 'right' || direction === 'down') {
    return idx >= 0 && idx < ordered.length - 1 ? ordered[idx + 1] : null;
  }
  return idx > 0 ? ordered[idx - 1] : null;
}

/**
 * Move focus in the given direction inside `root`. Returns true if focus
 * was moved, false if no candidate was found.
 */
export function moveFocus(root: HTMLElement, direction: SpatialDirection): boolean {
  const active = document.activeElement as HTMLElement | null;
  if (!active || !root.contains(active)) {
    // No active focus inside root — focus the first item.
    const first = root.querySelector<HTMLElement>(FOCUSABLE_SEL);
    if (first) {
      first.focus();
      first.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
      return true;
    }
    return false;
  }
  const next = findNeighbor(active, root, direction);
  if (!next) return false;
  next.focus();
  next.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  return true;
}
