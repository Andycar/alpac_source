import type { PlaybackEngine } from '../engine/PlaybackEngine';
import {
  toggleMenu,
  resetIdleTimer,
  pokeIdleTimer,
  hideControls,
  toggleFullscreen,
  activeMenu,
  controlsVisible,
  showStats,
  showDrmInfo,
  showShortcuts,
} from '../stores/ui';
import { volume, muted, playbackRate } from '../stores/player';
import { settings, updateSetting } from '../stores/settings';
import { pushToast } from '../stores/toasts';
import { moveFocus } from './menu-focus';
import { get } from 'svelte/store';

function formatTimeShort(t: number): string {
  if (!isFinite(t)) return '0:00';
  const m = Math.floor(t / 60);
  const s = Math.floor(t % 60);
  return `${m}:${s.toString().padStart(2, '0')}`;
}

export interface KeyboardController {
  destroy: () => void;
}

// Detect TV environment:
// 1. Media query: no hover AND no fine pointer (smart TVs, Android TV)
// 2. Android TV WebView (wv + ANDROID_TV or Nexus Player or ADT-* etc.)
// 3. Tizen / webOS smart TVs
// 4. Lampa's AndroidJS on a TV-like device
const isTV =
  typeof window !== 'undefined' &&
  (
    (matchMedia('(hover: none)').matches && !matchMedia('(pointer: fine)').matches) ||
    /Android.*(?:TV|Nexus Player|ADT-|BRAVIA|MIBOX|SHIELD)/i.test(navigator.userAgent) ||
    /Tizen|Web0S|webOS/i.test(navigator.userAgent) ||
    ((window as any).AndroidJS && matchMedia('(pointer: coarse)').matches)
  );

// Track whether user is navigating with D-pad/arrows (auto-detect TV-like usage)
let _dpadMode = isTV;

// ─── Spatial navigation helpers ──────────────────────────────

/** Get all focusable elements in the bottom controls bar */
function getControlButtons(container: HTMLElement): HTMLElement[] {
  return Array.from(
    container.querySelectorAll<HTMLElement>(
      '.lwp-controls-row .lwp-btn, .lwp-controls-row .lwp-quality-badge',
    ),
  ).filter((el) => el.offsetParent !== null);
}

/** Get all focusable elements inside any open menu */
function getMenuItems(container: HTMLElement): HTMLElement[] {
  const menu = container.querySelector<HTMLElement>('.lwp-menu');
  if (!menu) return [];
  return Array.from(
    menu.querySelectorAll<HTMLElement>(
      '.lwp-menu-item, .lwp-chip, button, [tabindex="0"]',
    ),
  ).filter((el) => el.offsetParent !== null);
}

/** Navigate to the next/prev focusable element in a list */
function navigateList(
  items: HTMLElement[],
  direction: 'next' | 'prev',
): boolean {
  if (items.length === 0) return false;
  const active = document.activeElement as HTMLElement;
  const idx = items.indexOf(active);
  if (idx === -1) {
    items[0].focus();
    items[0].scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    return true;
  }
  const next =
    direction === 'next'
      ? items[Math.min(idx + 1, items.length - 1)]
      : items[Math.max(idx - 1, 0)];
  next.focus();
  // Scroll focused item into view for long menus (Settings)
  next.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  return true;
}

// Map keyCode to key name for Android TV WebViews that don't support e.key
const keyCodeMap: Record<number, string> = {
  // Standard keyboard
  8: 'Backspace',
  13: 'Enter',
  27: 'Escape',
  32: ' ',
  37: 'ArrowLeft',
  38: 'ArrowUp',
  39: 'ArrowRight',
  40: 'ArrowDown',
  // Android TV D-pad (KEYCODE_DPAD_*)
  4: 'GoBack', // KEYCODE_BACK
  19: 'ArrowUp', // KEYCODE_DPAD_UP
  20: 'ArrowDown', // KEYCODE_DPAD_DOWN
  21: 'ArrowLeft', // KEYCODE_DPAD_LEFT
  22: 'ArrowRight', // KEYCODE_DPAD_RIGHT
  23: 'Enter', // KEYCODE_DPAD_CENTER
  // Media keys
  85: 'MediaPlayPause', // KEYCODE_MEDIA_PLAY_PAUSE
  86: 'MediaStop', // KEYCODE_MEDIA_STOP
  87: 'MediaTrackNext', // KEYCODE_MEDIA_NEXT
  88: 'MediaTrackPrevious', // KEYCODE_MEDIA_PREVIOUS
  89: 'MediaRewind', // KEYCODE_MEDIA_REWIND
  90: 'MediaFastForward', // KEYCODE_MEDIA_FAST_FORWARD
  179: 'MediaPlayPause',
  227: 'MediaRewind',
  228: 'MediaFastForward',
};

function getKey(e: KeyboardEvent): string {
  if (e.key && e.key !== 'Unidentified') return e.key;
  return keyCodeMap[e.keyCode] || '';
}

/**
 * Check whether a key event's target is a text-entry control. Covers:
 *   - <input> of text-like type (not range/button/checkbox/radio/…)
 *   - <textarea>, <select>
 *   - anything with contenteditable=true (including nested spans)
 * Uses closest() as a safety net — in some browsers events bubble from
 * a span inside an input-like host.
 */
function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;

  const self: HTMLElement | null = target.closest(
    'input, textarea, select, [contenteditable=""], [contenteditable="true"]',
  );
  if (!self) return false;

  if (self.tagName === 'INPUT') {
    const t = (self as HTMLInputElement).type;
    // Let range sliders (volume boost, offsets) be handled by keyboard.
    if (t === 'range') return false;
    // Button-likes don't accept text either, treat as non-typing.
    if (t === 'button' || t === 'submit' || t === 'reset' || t === 'image' ||
        t === 'file' || t === 'checkbox' || t === 'radio' || t === 'color') {
      return false;
    }
  }
  return true;
}

export function setupKeyboard(
  engine: PlaybackEngine,
  container: HTMLElement,
  callbacks?: {
    onNext?: () => void;
    onPrev?: () => void;
    onClose?: () => void;
  },
): KeyboardController {
  const handler = (e: KeyboardEvent) => {
    // Don't capture if typing in form controls / contenteditable.
    // We also call stopPropagation() here so Lampa's own Keypad module
    // (which often hijacks Space/Enter/arrows at the bubble phase) can't
    // steal characters from our chat/room-id inputs. Without this the
    // watchparty chat input reliably drops the first few keystrokes.
    if (isTypingTarget(e.target)) {
      e.stopPropagation();
      return;
    }

    const key = getKey(e);
    if (!key) return;

    // Stop Lampa's Keypad module from also handling this event
    e.stopImmediatePropagation();

    const controlsShown = get(controlsVisible);
    const menuOpen = get(activeMenu) !== 'none';

    // Auto-detect D-pad usage: if arrow keys are pressed and no mouse was used recently
    if (
      !_dpadMode &&
      (key === 'ArrowUp' || key === 'ArrowDown' || key === 'ArrowLeft' || key === 'ArrowRight') &&
      key === getKey(e) // genuine key press, not synthetic
    ) {
      _dpadMode = true;
    }

    // --- TV/D-pad remote: when controls hidden ---
    if (_dpadMode && !controlsShown) {
      switch (key) {
        case 'ArrowLeft':
          // Seek backward AND show controls briefly
          e.preventDefault();
          engine.seek(engine.currentTime - 10);
          resetIdleTimer();
          return;
        case 'ArrowRight':
          // Seek forward AND show controls briefly
          e.preventDefault();
          engine.seek(engine.currentTime + 10);
          resetIdleTimer();
          return;
        case 'ArrowUp':
        case 'ArrowDown':
        case 'Enter':
          e.preventDefault();
          resetIdleTimer(); // shows controls for navigation
          return;
      }
    }

    // Poke timer for most keys (keeps controls alive without forcing them visible)
    pokeIdleTimer();

    // --- Menu navigation (D-pad / arrow keys move focus in 2D) ---
    // Uses bounding-rect-aware spatial navigation (Wave 1 D1) instead of
    // a flat list, so horizontal chip groups inside SettingsMenu navigate
    // naturally with Left/Right and vertical lists with Up/Down.
    if (menuOpen) {
      // Let range inputs handle arrows natively (e.g. boost slider in
      // Settings). Note: text inputs are already bailed out far above
      // via isTypingTarget — this guard is only for range sliders
      // which we still allow into the handler but pass through.
      const isRange =
        e.target instanceof HTMLInputElement &&
        (e.target as HTMLInputElement).type === 'range';

      const menuRoot = container.querySelector<HTMLElement>('.lwp-menu') ?? container;
      switch (key) {
        case 'ArrowUp':
          if (isRange) { e.stopImmediatePropagation(); return; }
          e.preventDefault();
          if (!moveFocus(menuRoot, 'up')) navigateList(getMenuItems(container), 'prev');
          return;
        case 'ArrowDown':
          if (isRange) { e.stopImmediatePropagation(); return; }
          e.preventDefault();
          if (!moveFocus(menuRoot, 'down')) navigateList(getMenuItems(container), 'next');
          return;
        case 'ArrowLeft':
          if (isRange) { e.stopImmediatePropagation(); return; }
          e.preventDefault();
          if (!moveFocus(menuRoot, 'left')) navigateList(getMenuItems(container), 'prev');
          return;
        case 'ArrowRight':
          if (isRange) { e.stopImmediatePropagation(); return; }
          e.preventDefault();
          if (!moveFocus(menuRoot, 'right')) navigateList(getMenuItems(container), 'next');
          return;
        case 'Enter':
        case ' ':
          // If a menu item is focused, click it
          if (
            document.activeElement &&
            document.activeElement !== container &&
            document.activeElement !== document.body
          ) {
            e.preventDefault();
            (document.activeElement as HTMLElement).click();
            return;
          }
          // Space with no focus in menu → ignore (don't toggle play)
          if (key === ' ') {
            e.preventDefault();
            return;
          }
          return;
        // Escape/Back closes menu (fall through to main switch)
      }
    }

    // --- Controls-bar spatial navigation (D-pad / TV mode) ---
    // Left/Right always seek (10s), Up/Down navigate controls or hide.
    if (_dpadMode && controlsShown && !menuOpen) {
      switch (key) {
        case 'ArrowLeft':
          e.preventDefault();
          engine.seek(engine.currentTime - 10);
          resetIdleTimer();
          return;
        case 'ArrowRight':
          e.preventDefault();
          engine.seek(engine.currentTime + 10);
          resetIdleTimer();
          return;
        case 'ArrowUp':
          e.preventDefault();
          navigateList(getControlButtons(container), 'prev');
          return;
        case 'ArrowDown':
          e.preventDefault();
          navigateList(getControlButtons(container), 'next');
          return;
      }
    }

    switch (key) {
      case ' ':
      case 'k':
      case 'K':
      case 'MediaPlayPause':
      case 'MediaStop':
        e.preventDefault();
        engine.paused ? engine.play() : engine.pause();
        break;

      // Media track keys (remote media buttons)
      case 'MediaTrackNext':
        e.preventDefault();
        callbacks?.onNext?.();
        break;
      case 'MediaTrackPrevious':
        e.preventDefault();
        callbacks?.onPrev?.();
        break;

      // Enter/Select on remote — activate focused button or toggle play
      case 'Enter':
        if (
          document.activeElement &&
          document.activeElement !== container &&
          document.activeElement !== document.body
        ) {
          // A button is focused — let the browser click it
          break;
        }
        // Nothing focused — toggle play/pause
        e.preventDefault();
        engine.paused ? engine.play() : engine.pause();
        break;

      case 'f':
      case 'F':
        e.preventDefault();
        toggleFullscreen(container);
        break;

      case 'm':
      case 'M':
        e.preventDefault();
        engine.muted = !engine.muted;
        muted.set(engine.muted);
        break;

      case 'ArrowLeft':
      case 'MediaRewind':
        e.preventDefault();
        engine.seek(engine.currentTime - 5);
        break;

      case 'ArrowRight':
      case 'MediaFastForward':
        e.preventDefault();
        engine.seek(engine.currentTime + 5);
        break;

      case 'j':
      case 'J':
        e.preventDefault();
        engine.seek(engine.currentTime - 10);
        break;

      case 'l':
      case 'L':
        e.preventDefault();
        engine.seek(engine.currentTime + 10);
        break;

      case 'ArrowUp':
        e.preventDefault();
        engine.volume = Math.min(1, engine.volume + 0.05);
        volume.set(engine.volume);
        break;

      case 'ArrowDown':
        e.preventDefault();
        engine.volume = Math.max(0, engine.volume - 0.05);
        volume.set(engine.volume);
        break;

      case 'n':
      case 'N':
        e.preventDefault();
        callbacks?.onNext?.();
        break;

      case 'p':
      case 'P':
        e.preventDefault();
        if (e.shiftKey) {
          callbacks?.onPrev?.();
        } else {
          engine.togglePiP();
        }
        break;

      case 's':
      case 'S':
        e.preventDefault();
        {
          const dataUrl = engine.takeScreenshot();
          if (dataUrl) {
            const a = document.createElement('a');
            a.href = dataUrl;
            a.download = `screenshot-${Date.now()}.png`;
            a.click();
            pushToast('Скриншот сохранён', { kind: 'success', ttl: 1500 });
          } else {
            pushToast('Скриншот недоступен (DRM)', { kind: 'warn' });
          }
        }
        break;

      case 'c':
      case 'C':
        e.preventDefault();
        toggleMenu('subtitles');
        break;

      case 'i':
      case 'I':
        e.preventDefault();
        showStats.update((v) => !v);
        break;

      // TV remote color keys (Wave 1 D2). Standard names + numeric fallbacks.
      // Red    → toggle stats
      // Green  → cycle ambilight off → subtle → rich
      // Yellow → toggle audio menu
      // Blue   → toggle subtitles menu
      case 'ColorF0Red':
      case 'Red':
      case 'XF86Red':
        e.preventDefault();
        showStats.update((v) => !v);
        break;

      case 'ColorF1Green':
      case 'Green':
      case 'XF86Green':
        e.preventDefault();
        {
          const order: Array<'off' | 'subtle' | 'rich'> = ['off', 'subtle', 'rich'];
          const cur = get(settings).ambilightMode;
          const next = order[(order.indexOf(cur) + 1) % order.length];
          updateSetting('ambilightMode', next);
        }
        break;

      case 'ColorF2Yellow':
      case 'Yellow':
      case 'XF86Yellow':
        e.preventDefault();
        toggleMenu('audio');
        break;

      case 'ColorF3Blue':
      case 'Blue':
      case 'XF86Blue':
        e.preventDefault();
        toggleMenu('subtitles');
        break;

      // Long-form: 'd' / 'D' → DRM diagnostics overlay
      case 'd':
      case 'D':
        e.preventDefault();
        showDrmInfo.update((v) => !v);
        break;

      // 'w' / 'W' → toggle watchparty panel (Wave 2 C2)
      case 'w':
      case 'W':
        e.preventDefault();
        toggleMenu('watchparty');
        break;

      // 'v' / 'V' → toggle voice control (Pack 4)
      case 'v':
      case 'V':
        e.preventDefault();
        {
          const fn = (window as any).__lwpToggleVoice;
          if (typeof fn === 'function') fn();
          else pushToast('Голосовое управление недоступно (Chromium-only)', { kind: 'warn' });
        }
        break;

      // 'b' / 'B' → quick add bookmark at currentTime; Shift+B opens panel.
      case 'b':
      case 'B':
        e.preventDefault();
        if (e.shiftKey) {
          toggleMenu('bookmarks');
        } else {
          import('../stores/bookmarks').then(({ addBookmark }) => {
            const b = addBookmark(engine.currentTime);
            if (b) pushToast(`Закладка: ${formatTimeShort(b.time)}`, { kind: 'success', ttl: 1500 });
          });
        }
        break;

      // '?' / 'F1' → keyboard shortcuts cheat sheet
      case '?':
      case 'F1':
        e.preventDefault();
        showShortcuts.update((v) => !v);
        break;

      // Frame step — uses WebCodecs/RVFC fps estimate when available,
      // falls back to fixed 1/30s when paused without RVFC support.
      case ',':
        if (engine.paused) {
          e.preventDefault();
          engine.frameStep('backward');
        }
        break;

      case '.':
        if (engine.paused) {
          e.preventDefault();
          engine.frameStep('forward');
        }
        break;

      // Back button: Escape, Backspace, XF86Back (Android TV remote)
      case 'Escape':
      case 'Backspace':
      case 'XF86Back':
      case 'GoBack':
        e.preventDefault();
        // Close menu first, then exit fullscreen, then close player
        if (get(activeMenu) !== 'none') {
          activeMenu.set('none');
        } else if (document.fullscreenElement) {
          document.exitFullscreen();
        } else {
          callbacks?.onClose?.();
        }
        break;

      case '[':
        e.preventDefault();
        {
          const speeds = [0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4];
          const cur = engine.playbackRate;
          const idx = speeds.findIndex((s) => s >= cur);
          const prev = speeds[Math.max(0, idx - 1)];
          engine.playbackRate = prev;
          playbackRate.set(prev);
        }
        break;

      case ']':
        e.preventDefault();
        {
          const speeds = [0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4];
          const cur = engine.playbackRate;
          const idx = speeds.findIndex((s) => s > cur);
          const next = speeds[idx >= 0 ? idx : speeds.length - 1];
          engine.playbackRate = next;
          playbackRate.set(next);
        }
        break;

      default:
        // 1-9: seek to percentage
        if (key >= '1' && key <= '9') {
          e.preventDefault();
          const pct = parseInt(key) / 10;
          engine.seek(engine.duration * pct);
        }
        break;
    }
  };

  // Mouse movement disables D-pad mode (user switched to mouse/touchpad)
  const mouseHandler = () => { _dpadMode = isTV; /* reset to auto-detected value */ };
  container.addEventListener('mousemove', mouseHandler);

  // Use capture phase to intercept keys before Lampa's Keypad handler
  document.addEventListener('keydown', handler, true);

  return {
    destroy: () => {
      document.removeEventListener('keydown', handler, true);
      container.removeEventListener('mousemove', mouseHandler);
    },
  };
}
