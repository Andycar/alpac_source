/**
 * Watchparty session store — populated by WatchpartySync (Wave 2 C2).
 * null when offline; non-null while connected to a room.
 *
 * The `watchpartySyncInstance` store holds the active WatchpartySync
 * object at module scope so it survives WatchpartyPanel mount/unmount.
 * Without this, closing the panel via × and re-opening it would leak a
 * connected socket with no way for the UI to reach it (the local `let
 * sync` would have been reset to null, so the "Выйти" button no-op'd).
 */

import { writable } from 'svelte/store';
// We keep the type as `unknown` here to avoid a circular import
// (WatchpartySync imports from this file). The panel casts to
// WatchpartySync when reading.
export const watchpartySyncInstance = writable<unknown>(null);

export interface WatchpartySession {
  roomId: string;
  nick: string;
  peers: number;
  isHost: boolean;
  connectedAt: number;
}

export const watchpartySession = writable<WatchpartySession | null>(null);
