/**
 * WatchpartySync — keep multiple players in lock-step over a WebSocket
 * relay (Wave 2 C2).
 *
 * Protocol: peers send small JSON messages to a per-room WS endpoint
 * (`/webplayer/ws/watchparty/:roomId`). The server fans them out to all
 * other peers. We tag every message with our peer id so we can ignore
 * echoes.
 *
 * Drift correction: only apply remote `time` if we are off by more than
 * settings.watchpartyDriftThresholdMs. Apply latency compensation:
 * (now - sentAt) is the one-way delay estimate.
 *
 * Throttle: outgoing 'time' broadcasts are limited to one every 3s while
 * playing. Play/pause/seek/episode events go out immediately.
 */

import type { PlaybackEngine } from '../PlaybackEngine';
import type { PartyCmd, ChatMessage } from '../core/types';
import { watchpartySession } from '../../stores/watchparty';
import { settings } from '../../stores/settings';
import { pushChat, clearChat } from '../../stores/chat';
import { get } from 'svelte/store';

export interface WatchpartyOptions {
  wsUrl: string;       // ws://host/webplayer/ws/watchparty/:roomId
  roomId: string;
  nick?: string;
}

export type EpisodeChangeHandler = (cmd: Extract<PartyCmd, { type: 'episode' }>) => void;

const TIME_BROADCAST_INTERVAL_MS = 3000;

export class WatchpartySync {
  private ws: WebSocket | null = null;
  private peerId: string;
  private suppressNextLocalEvent = false;
  private lastTimeBroadcast = 0;
  private timeTimer: ReturnType<typeof setInterval> | null = null;
  private peers = 0;
  private connected = false;
  private destroyed = false;
  private onEpisodeChange: EpisodeChangeHandler | null = null;

  constructor(
    private engine: PlaybackEngine,
    private opts: WatchpartyOptions,
  ) {
    this.peerId = makePeerId();
  }

  /** Provide a callback for remote 'episode' commands (caller decides UX). */
  setEpisodeChangeHandler(cb: EpisodeChangeHandler | null): void {
    this.onEpisodeChange = cb;
  }

  async connect(): Promise<void> {
    if (this.ws) return;
    let socket: WebSocket;
    try {
      socket = new WebSocket(this.opts.wsUrl);
    } catch (err: any) {
      // SecurityError on mixed-content / invalid URL — surface immediately.
      throw new Error(`Не удалось открыть WS (${this.opts.wsUrl}): ${err?.message || err}`);
    }
    this.ws = socket;
    await new Promise<void>((resolve, reject) => {
      const onOpen = () => { cleanup(); resolve(); };
      const onErr = () => {
        cleanup();
        // The browser doesn't expose much detail on WS failure — only
        // "Connection failed". Annotate with the URL we tried so it's
        // obvious whether origin / port / path is wrong.
        reject(new Error(`WS не подключился: ${this.opts.wsUrl}`));
      };
      const onClose = (ev: CloseEvent) => {
        cleanup();
        reject(new Error(`WS закрыт сразу (code=${ev.code} ${ev.reason || 'без причины'})`));
      };
      const cleanup = () => {
        socket.removeEventListener('open', onOpen);
        socket.removeEventListener('error', onErr);
        socket.removeEventListener('close', onClose);
      };
      socket.addEventListener('open', onOpen);
      socket.addEventListener('error', onErr);
      socket.addEventListener('close', onClose);
    });
    this.connected = true;
    this.ws.addEventListener('message', (ev) => this.onMessage(ev));
    this.ws.addEventListener('close', () => this.handleClose());

    // Announce ourselves.
    this.send({
      type: 'hello',
      nick: this.opts.nick || this.peerId.substring(0, 6),
      peers: 0,
      src: this.peerId,
    });

    // Wire engine event broadcasts.
    this.attachEngineHooks();

    clearChat();
    watchpartySession.set({
      roomId: this.opts.roomId,
      nick: this.opts.nick || this.peerId.substring(0, 6),
      peers: this.peers,
      isHost: false,
      connectedAt: Date.now(),
    });

    // Periodic time broadcast while playing.
    this.timeTimer = setInterval(() => {
      if (!this.engine || this.engine.paused) return;
      const now = performance.now();
      if (now - this.lastTimeBroadcast >= TIME_BROADCAST_INTERVAL_MS) {
        this.lastTimeBroadcast = now;
        this.send({
          type: 'time',
          t: this.engine.currentTime,
          sentAt: Date.now(),
          src: this.peerId,
        });
      }
    }, 1000);
  }

  async leave(): Promise<void> {
    this.send({
      type: 'bye',
      nick: this.opts.nick || this.peerId.substring(0, 6),
      peers: this.peers,
      src: this.peerId,
    });
    this.handleClose();
  }

  /** Manually broadcast an episode change (called by Player.svelte playItem). */
  broadcastEpisode(index: number, url: string): void {
    if (!this.connected) return;
    this.send({
      type: 'episode',
      index,
      url,
      sentAt: Date.now(),
      src: this.peerId,
    });
  }

  /** Send a chat message and append it to local log immediately. */
  sendChat(text: string): void {
    const trimmed = text.trim();
    if (!trimmed || !this.connected) return;
    const nick = this.opts.nick || this.peerId.substring(0, 6);
    this.send({
      type: 'chat',
      text: trimmed.substring(0, 280),
      nick,
      sentAt: Date.now(),
      src: this.peerId,
    });
    // Local echo (server doesn't bounce our own message back).
    pushChat({
      id: `${this.peerId}:${Date.now()}`,
      text: trimmed,
      nick,
      self: true,
      at: Date.now(),
    });
  }

  destroy(): void {
    this.destroyed = true;
    this.handleClose();
  }

  // ------- private -------

  private send(cmd: PartyCmd): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    try {
      this.ws.send(JSON.stringify(cmd));
    } catch { /* ignore */ }
  }

  private onMessage(ev: MessageEvent): void {
    let cmd: PartyCmd;
    try {
      cmd = JSON.parse(typeof ev.data === 'string' ? ev.data : '');
    } catch {
      return;
    }
    if (!cmd?.type || cmd.src === this.peerId) return; // echo
    this.applyRemote(cmd);
  }

  private applyRemote(cmd: PartyCmd): void {
    const s = get(settings);
    // Some events don't carry sync-affecting timing.
    switch (cmd.type) {
      case 'hello':
      case 'bye':
        this.peers = cmd.peers;
        watchpartySession.update((sess) => sess ? { ...sess, peers: this.peers } : sess);
        return;
      case 'episode':
        this.onEpisodeChange?.(cmd);
        return;
      case 'chat':
        pushChat({
          id: `${cmd.src}:${cmd.sentAt}`,
          text: cmd.text,
          nick: cmd.nick || cmd.src.substring(0, 6),
          self: false,
          at: cmd.sentAt || Date.now(),
        });
        return;
      case 'play':
      case 'pause':
      case 'seek':
      case 'time':
        if (!s.watchpartyAutoFollowHost) return;
        this.applyTimeOrTransport(cmd);
        return;
    }
  }

  private applyTimeOrTransport(cmd: Extract<PartyCmd, { type: 'time' | 'play' | 'pause' | 'seek' }>): void {
    const s = get(settings);
    const driftMs = s.watchpartyDriftThresholdMs ?? 2000;

    // Latency compensation
    const latency = Math.max(0, (Date.now() - cmd.sentAt) / 1000);
    const remoteT = cmd.t + latency;
    const drift = Math.abs(this.engine.currentTime - remoteT);

    if (cmd.type === 'time') {
      if (drift * 1000 > driftMs) {
        this.suppressNext();
        this.engine.seek(remoteT);
      }
      return;
    }
    if (cmd.type === 'seek') {
      this.suppressNext();
      this.engine.seek(remoteT);
      return;
    }
    if (cmd.type === 'play') {
      if (drift * 1000 > driftMs) {
        this.suppressNext();
        this.engine.seek(remoteT);
      }
      this.suppressNext();
      this.engine.play();
      return;
    }
    if (cmd.type === 'pause') {
      this.suppressNext();
      this.engine.pause();
    }
  }

  /** Mark the next local play/pause/seek as remote-driven so we don't echo. */
  private suppressNext(): void {
    this.suppressNextLocalEvent = true;
    // Auto-clear after a short delay in case the expected event doesn't fire.
    setTimeout(() => { this.suppressNextLocalEvent = false; }, 500);
  }

  private attachEngineHooks(): void {
    const onPlay = () => {
      if (this.consumeSuppress()) return;
      this.send({
        type: 'play',
        t: this.engine.currentTime,
        sentAt: Date.now(),
        src: this.peerId,
      });
    };
    const onPause = () => {
      if (this.consumeSuppress()) return;
      this.send({
        type: 'pause',
        t: this.engine.currentTime,
        sentAt: Date.now(),
        src: this.peerId,
      });
    };
    const onSeeked = () => {
      if (this.consumeSuppress()) return;
      this.send({
        type: 'seek',
        t: this.engine.currentTime,
        sentAt: Date.now(),
        src: this.peerId,
      });
    };
    this.engine.on('playing', onPlay);
    this.engine.on('paused', onPause);
    this.engine.on('seeked', onSeeked);
  }

  private consumeSuppress(): boolean {
    if (this.suppressNextLocalEvent) {
      this.suppressNextLocalEvent = false;
      return true;
    }
    return false;
  }

  private handleClose(): void {
    if (this.timeTimer) {
      clearInterval(this.timeTimer);
      this.timeTimer = null;
    }
    if (this.ws) {
      try { this.ws.close(); } catch { /* ignore */ }
      this.ws = null;
    }
    this.connected = false;
    if (!this.destroyed) {
      watchpartySession.set(null);
    }
  }
}

function makePeerId(): string {
  const c: any = (globalThis as any).crypto;
  if (c?.randomUUID) return c.randomUUID();
  return Math.random().toString(36).substring(2, 10) + Date.now().toString(36);
}
