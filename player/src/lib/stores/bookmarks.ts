/**
 * Bookmarks — per-URL named timestamps. Stored in localStorage so they
 * survive across sessions. Rendered as dots on the timeline + a list
 * panel reachable from SettingsMenu / 'B' shortcut.
 *
 * Schema: Map<urlHash, BookmarkEntry[]>. Cap at 50 bookmarks per URL.
 */

import { writable, get } from 'svelte/store';

export interface Bookmark {
  /** Seconds into the video. */
  time: number;
  /** Optional user note. */
  note: string;
  /** Auto-generated label like "Закладка 1" if note is empty. */
  label: string;
  /** ms timestamp of creation — used as stable id. */
  createdAt: number;
}

const STORAGE_KEY = 'lwp_bookmarks_v1';
const MAX_PER_URL = 50;
const MAX_URLS = 200;

type Store = Record<string, Bookmark[]>;

function read(): Store {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    return typeof parsed === 'object' && parsed ? parsed : {};
  } catch {
    return {};
  }
}

function write(s: Store): void {
  try {
    // Cap total URLs by dropping the oldest first.
    const keys = Object.keys(s);
    if (keys.length > MAX_URLS) {
      const toDrop = keys.length - MAX_URLS;
      const sortedByLastUpdate = keys.sort((a, b) => {
        const la = (s[a][s[a].length - 1]?.createdAt ?? 0);
        const lb = (s[b][s[b].length - 1]?.createdAt ?? 0);
        return la - lb;
      });
      for (let i = 0; i < toDrop; i++) delete s[sortedByLastUpdate[i]];
    }
    localStorage.setItem(STORAGE_KEY, JSON.stringify(s));
  } catch {
    /* ignore quota errors */
  }
}

function urlKey(url: string): string {
  // FNV-1a 32-bit, hex.
  let h = 0x811c9dc5;
  for (let i = 0; i < url.length; i++) {
    h ^= url.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return ('00000000' + (h >>> 0).toString(16)).slice(-8);
}

/** Live store of bookmarks for the currently-loaded URL. */
export const currentBookmarks = writable<Bookmark[]>([]);

let _activeKey = '';
let _store: Store = read();

/** Switch the bookmarks store to a new content URL. */
export function loadBookmarksFor(url: string): void {
  _activeKey = url ? urlKey(url) : '';
  _store = read();
  currentBookmarks.set(_activeKey ? (_store[_activeKey] ?? []) : []);
}

/** Add a bookmark at the given time for the active URL. Returns the new entry. */
export function addBookmark(time: number, note = ''): Bookmark | null {
  if (!_activeKey || !isFinite(time) || time < 0) return null;
  const list = _store[_activeKey] ?? [];
  const next: Bookmark = {
    time,
    note: note.trim(),
    label: note.trim() || `Закладка ${list.length + 1}`,
    createdAt: Date.now(),
  };
  list.push(next);
  list.sort((a, b) => a.time - b.time);
  if (list.length > MAX_PER_URL) list.shift();
  _store[_activeKey] = list;
  write(_store);
  currentBookmarks.set([...list]);
  return next;
}

export function removeBookmark(createdAt: number): void {
  if (!_activeKey) return;
  const list = (_store[_activeKey] ?? []).filter((b) => b.createdAt !== createdAt);
  if (list.length === 0) delete _store[_activeKey];
  else _store[_activeKey] = list;
  write(_store);
  currentBookmarks.set([...list]);
}

export function updateBookmarkNote(createdAt: number, note: string): void {
  if (!_activeKey) return;
  const list = _store[_activeKey] ?? [];
  const e = list.find((b) => b.createdAt === createdAt);
  if (!e) return;
  e.note = note.trim();
  e.label = e.note || e.label;
  write(_store);
  currentBookmarks.set([...list]);
}

/** Clear all bookmarks for the active URL. */
export function clearBookmarks(): void {
  if (!_activeKey) return;
  delete _store[_activeKey];
  write(_store);
  currentBookmarks.set([]);
}

/** Find the next/previous bookmark relative to a time. */
export function nextBookmark(time: number): Bookmark | null {
  const list = get(currentBookmarks);
  return list.find((b) => b.time > time + 0.5) ?? null;
}

export function prevBookmark(time: number): Bookmark | null {
  const list = get(currentBookmarks);
  let candidate: Bookmark | null = null;
  for (const b of list) if (b.time < time - 1.5) candidate = b;
  return candidate;
}
