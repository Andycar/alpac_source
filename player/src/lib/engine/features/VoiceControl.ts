/**
 * VoiceControl — speech recognition with a built-in command dictionary.
 *
 * Chromium-only as of 2026 (Safari and Firefox don't ship the Web Speech
 * API for recognition). Caller MUST check hasSpeechRecognition() before
 * exposing the toggle in UI.
 *
 * Command grammar is plain Russian phrases with synonym aliases. Numbers
 * are parsed by tryParseNumber so users can say "перемотай на тридцать
 * секунд" or "плюс тридцать" or "перемотать вперёд десять".
 *
 * Privacy: recognition runs in the browser — audio never leaves the
 * device on Chromium (Google Cloud is used in some platforms; we don't
 * control that).
 *
 * Pack 4 polish.
 */

import type { PlaybackEngine } from '../PlaybackEngine';
import { hasSpeechRecognition } from '../../utils/support';

export type VoiceCommand =
  | { type: 'play' }
  | { type: 'pause' }
  | { type: 'toggle' }
  | { type: 'mute' }
  | { type: 'unmute' }
  | { type: 'volume_up' }
  | { type: 'volume_down' }
  | { type: 'volume_set'; value: number }
  | { type: 'seek_forward'; seconds: number }
  | { type: 'seek_backward'; seconds: number }
  | { type: 'seek_to'; seconds: number }
  | { type: 'next' }
  | { type: 'prev' }
  | { type: 'fullscreen_toggle' }
  | { type: 'subtitles' }
  | { type: 'audio' }
  | { type: 'quality' };

export interface VoiceControlOptions {
  lang?: string;
  onCommand?: (cmd: VoiceCommand) => void;
  onTranscript?: (text: string, isFinal: boolean) => void;
  onError?: (error: string) => void;
  onEnd?: () => void;
}

export class VoiceControl {
  private recognizer: any = null;
  private active = false;
  private destroyed = false;
  private restartTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(private engine: PlaybackEngine, private opts: VoiceControlOptions = {}) {}

  static get available(): boolean { return hasSpeechRecognition(); }

  start(): boolean {
    if (this.destroyed) return false;
    if (this.active) return true;
    const Ctor: any = (window as any).SpeechRecognition
      || (window as any).webkitSpeechRecognition;
    if (!Ctor) return false;

    this.recognizer = new Ctor();
    this.recognizer.lang = this.opts.lang || 'ru-RU';
    this.recognizer.continuous = true;
    this.recognizer.interimResults = true;

    this.recognizer.onresult = (e: any) => {
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const r = e.results[i];
        const text = String(r[0]?.transcript || '').trim();
        if (!text) continue;
        this.opts.onTranscript?.(text, r.isFinal);
        if (r.isFinal) {
          const cmd = parseCommand(text);
          if (cmd) {
            this.dispatch(cmd);
            this.opts.onCommand?.(cmd);
          }
        }
      }
    };
    this.recognizer.onerror = (e: any) => {
      this.opts.onError?.(String(e?.error || 'speech-error'));
    };
    this.recognizer.onend = () => {
      // Auto-restart while active (browsers stop after silence).
      if (this.active && !this.destroyed) {
        this.restartTimer = setTimeout(() => {
          try { this.recognizer?.start(); } catch { /* ignore */ }
        }, 200);
      } else {
        this.opts.onEnd?.();
      }
    };

    try {
      this.recognizer.start();
      this.active = true;
      return true;
    } catch (err) {
      console.warn('[LWP] VoiceControl start failed:', err);
      return false;
    }
  }

  stop(): void {
    this.active = false;
    if (this.restartTimer) {
      clearTimeout(this.restartTimer);
      this.restartTimer = null;
    }
    try { this.recognizer?.stop(); } catch { /* ignore */ }
    this.recognizer = null;
  }

  toggle(): boolean {
    if (this.active) { this.stop(); return false; }
    return this.start();
  }

  destroy(): void {
    this.destroyed = true;
    this.stop();
  }

  /** Apply a command to the engine. Public so callers can dispatch synthetic. */
  dispatch(cmd: VoiceCommand): void {
    const eng = this.engine;
    if (!eng) return;
    switch (cmd.type) {
      case 'play':            eng.play(); break;
      case 'pause':           eng.pause(); break;
      case 'toggle':          eng.paused ? eng.play() : eng.pause(); break;
      case 'mute':            eng.muted = true; break;
      case 'unmute':          eng.muted = false; break;
      case 'volume_up':       eng.volume = Math.min(1, eng.volume + 0.1); break;
      case 'volume_down':     eng.volume = Math.max(0, eng.volume - 0.1); break;
      case 'volume_set':      eng.volume = Math.max(0, Math.min(1, cmd.value / 100)); break;
      case 'seek_forward':    eng.seek(eng.currentTime + cmd.seconds); break;
      case 'seek_backward':   eng.seek(eng.currentTime - cmd.seconds); break;
      case 'seek_to':         eng.seek(Math.max(0, cmd.seconds)); break;
      case 'fullscreen_toggle':
        if (document.fullscreenElement) document.exitFullscreen();
        else document.documentElement.requestFullscreen?.();
        break;
      // 'next' / 'prev' / 'subtitles' / 'audio' / 'quality' are emitted
      // for the UI layer to handle (they need access to playlist + menus).
    }
  }
}

// ─── Command parser ─────────────────────────────────────────

const NUMBER_WORDS_RU: Record<string, number> = {
  ноль: 0, один: 1, одну: 1, два: 2, две: 2, три: 3, четыре: 4,
  пять: 5, шесть: 6, семь: 7, восемь: 8, девять: 9, десять: 10,
  одиннадцать: 11, двенадцать: 12, тринадцать: 13, четырнадцать: 14,
  пятнадцать: 15, двадцать: 20, тридцать: 30, сорок: 40, пятьдесят: 50,
  шестьдесят: 60, семьдесят: 70, восемьдесят: 80, девяносто: 90, сто: 100,
};

function tryParseNumber(input: string): number | null {
  const direct = parseInt(input, 10);
  if (!isNaN(direct)) return direct;
  const lower = input.toLowerCase();
  if (NUMBER_WORDS_RU[lower] !== undefined) return NUMBER_WORDS_RU[lower];
  // Compound like "тридцать пять"
  const parts = lower.split(/\s+/);
  let total = 0; let any = false;
  for (const p of parts) {
    if (NUMBER_WORDS_RU[p] !== undefined) { total += NUMBER_WORDS_RU[p]; any = true; }
    else if (!isNaN(parseInt(p, 10))) { total += parseInt(p, 10); any = true; }
  }
  return any ? total : null;
}

export function parseCommand(rawText: string): VoiceCommand | null {
  const text = rawText.toLowerCase().trim();
  if (!text) return null;

  // ── Transport ──
  if (/(^|\s)(пауз[аы]?|стоп|останов)/.test(text)) return { type: 'pause' };
  if (/(включи|играй|плей|воспроизвед|продолж)/.test(text)) return { type: 'play' };
  if (/(переключи)\s+(воспроизведение|плей)/.test(text)) return { type: 'toggle' };

  // ── Mute / volume ──
  if (/(заглуши|без\s*звук|тиш|выключи\s*звук|немой)/.test(text)) return { type: 'mute' };
  if (/(включи\s*звук|verни\s*звук|снять\s*тиш|размют|unmute)/.test(text)) return { type: 'unmute' };
  if (/(громче|поднять?\s*громкость|увеличи?\s*громкость)/.test(text)) return { type: 'volume_up' };
  if (/(тише|снизи?\s*громкость|уменьш?и?\s*громкость)/.test(text)) return { type: 'volume_down' };

  // "громкость 50" / "громкость на 70"
  const volSet = /громкост[ьи]\s*(?:на\s*)?(\d{1,3}|[а-яё\s]+)/.exec(text);
  if (volSet) {
    const n = tryParseNumber(volSet[1]);
    if (n !== null) return { type: 'volume_set', value: Math.max(0, Math.min(100, n)) };
  }

  // ── Seek ──
  // "перемотай вперёд 30 секунд" / "+30" / "вперед на тридцать"
  const fwd = /(?:вперёд|вперед|плюс|\+|forward)\s*(?:на\s*)?(\d+|[а-яё\s]+?)\s*(?:сек|с(?:екунд)?|минут|мин)?/.exec(text);
  if (fwd && /(перемот|вперёд|вперед|плюс|\+)/.test(text)) {
    const n = tryParseNumber(fwd[1]);
    if (n !== null) {
      const isMin = /мин/.test(text);
      return { type: 'seek_forward', seconds: isMin ? n * 60 : n };
    }
  }
  const back = /(?:назад|минус|−|-|backward)\s*(?:на\s*)?(\d+|[а-яё\s]+?)\s*(?:сек|с(?:екунд)?|минут|мин)?/.exec(text);
  if (back && /(перемот|назад|минус|-)/.test(text)) {
    const n = tryParseNumber(back[1]);
    if (n !== null) {
      const isMin = /мин/.test(text);
      return { type: 'seek_backward', seconds: isMin ? n * 60 : n };
    }
  }

  // "перемотай на 1:30" → seek_to
  const toM = /(?:на|до)\s+(\d+)[:.](\d{1,2})/.exec(text);
  if (toM && /перемот/.test(text)) {
    return { type: 'seek_to', seconds: parseInt(toM[1], 10) * 60 + parseInt(toM[2], 10) };
  }

  // ── Episodes ──
  if (/(следующ|next|вперёд\s+эпизод)/.test(text)) return { type: 'next' };
  if (/(предыдущ|prev|назад\s+эпизод)/.test(text)) return { type: 'prev' };

  // ── Misc menus ──
  if (/(полн[ыо]й\s*экран|fullscreen)/.test(text)) return { type: 'fullscreen_toggle' };
  if (/(субтитр|caption|cc)/.test(text)) return { type: 'subtitles' };
  if (/(озвучк|аудио|audio)/.test(text)) return { type: 'audio' };
  if (/(качество|quality|разрешен)/.test(text)) return { type: 'quality' };

  return null;
}
