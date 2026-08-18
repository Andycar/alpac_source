/**
 * Watchparty chat log — bounded ring buffer of recent messages.
 * Reset whenever a new room is joined.
 */

import { writable, get } from 'svelte/store';
import type { ChatMessage } from '../engine/core/types';

const MAX_MESSAGES = 100;

const _log = writable<ChatMessage[]>([]);
export const chatLog = { subscribe: _log.subscribe };

export function pushChat(msg: ChatMessage): void {
  _log.update((list) => {
    const next = [...list, msg];
    while (next.length > MAX_MESSAGES) next.shift();
    return next;
  });
}

export function clearChat(): void { _log.set([]); }

export function chatLength(): number { return get(_log).length; }
