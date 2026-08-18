/**
 * Toast notifications — transient ephemeral messages anchored to the
 * top-right of the player. Used for "preload ready", "watchparty
 * connected", "screenshot saved", etc.
 *
 * Lightweight by design: no animations beyond CSS, auto-dismiss on a
 * short timer, max 3 visible at a time.
 */

import { writable } from 'svelte/store';

export interface Toast {
  id: number;
  text: string;
  /** 'info' | 'success' | 'warn' | 'error' — drives the accent color. */
  kind: 'info' | 'success' | 'warn' | 'error';
  /** Auto-dismiss timeout in ms (default 2500). 0 = sticky. */
  ttl: number;
  createdAt: number;
}

const MAX_VISIBLE = 3;
const _toasts = writable<Toast[]>([]);
export const toasts = { subscribe: _toasts.subscribe };

let nextId = 1;

export function pushToast(text: string, opts?: Partial<Pick<Toast, 'kind' | 'ttl'>>): number {
  const id = nextId++;
  const t: Toast = {
    id,
    text,
    kind: opts?.kind ?? 'info',
    ttl: opts?.ttl ?? 2500,
    createdAt: Date.now(),
  };
  _toasts.update((list) => {
    const next = [...list, t];
    while (next.length > MAX_VISIBLE) next.shift();
    return next;
  });
  if (t.ttl > 0) {
    setTimeout(() => dismissToast(id), t.ttl);
  }
  return id;
}

export function dismissToast(id: number): void {
  _toasts.update((list) => list.filter((t) => t.id !== id));
}

export function clearToasts(): void {
  _toasts.set([]);
}
