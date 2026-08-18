import type { PlaybackEngine } from '../engine/PlaybackEngine';
import { resetIdleTimer, pokeIdleTimer, controlsVisible, anyMenuOpen, activeMenu } from '../stores/ui';
import { volume } from '../stores/player';
import { get } from 'svelte/store';

export interface GestureController {
  destroy: () => void;
}

export interface GestureCallbacks {
  onDoubleTapSeek?: (side: 'left' | 'right', seconds: number) => void;
  onVolumeChange?: (vol: number | null) => void;
  onBrightnessChange?: (brightness: number | null) => void;
  onLongPressStart?: () => void;
  onLongPressEnd?: () => void;
  /** Wave 1 D5: pinch zoom factor (1.0 = neutral). Called on every move. */
  onPinch?: (scale: number) => void;
  /** Wave 1 D5: pinch ended — caller may snap or reset. */
  onPinchEnd?: () => void;
}

interface TouchState {
  startX: number;
  startY: number;
  startTime: number;
  lastTapTime: number;
  lastTapX: number;
  isDragging: boolean;
  dragType: 'none' | 'volume' | 'brightness';
  startVolume: number;
  startBrightness: number;
}

export function setupGestures(
  engine: PlaybackEngine,
  container: HTMLElement,
  callbacks?: GestureCallbacks,
): GestureController {
  const state: TouchState = {
    startX: 0,
    startY: 0,
    startTime: 0,
    lastTapTime: 0,
    lastTapX: 0,
    isDragging: false,
    dragType: 'none',
    startVolume: 1,
    startBrightness: 1,
  };

  let brightnessFilter = 1;
  let longPressTimer: ReturnType<typeof setTimeout> | null = null;
  let isLongPressing = false;
  const SWIPE_THRESHOLD = 20;
  const DOUBLE_TAP_DELAY = 300;
  const LONG_PRESS_DELAY = 500;

  // --- Wave 1 D5: pinch-zoom state ---
  // Two-finger pinch on the video tracks distance ratio between the two
  // touch points. We emit callbacks so Player.svelte can apply CSS transform.
  let pinchActive = false;
  let pinchStartDist = 0;
  let pinchStartScale = 1;
  let lastEmittedScale = 1;
  const MIN_PINCH_DELTA = 6; // px before we commit to pinch mode

  function distance(a: Touch, b: Touch): number {
    const dx = a.clientX - b.clientX;
    const dy = a.clientY - b.clientY;
    return Math.hypot(dx, dy);
  }

  function onTouchStart(e: TouchEvent) {
    // Two-finger gesture: prepare pinch (Wave 1 D5).
    if (e.touches.length === 2) {
      // Cancel any in-progress single-finger interaction
      if (longPressTimer) { clearTimeout(longPressTimer); longPressTimer = null; }
      if (isLongPressing) { callbacks?.onLongPressEnd?.(); isLongPressing = false; }
      pinchActive = false; // armed but not committed until we see distance change
      pinchStartDist = distance(e.touches[0], e.touches[1]);
      pinchStartScale = lastEmittedScale;
      return;
    }
    if (e.touches.length !== 1) return;
    pokeIdleTimer(); // keep controls alive during interaction (don't show them)
    const t = e.touches[0];
    state.startX = t.clientX;
    state.startY = t.clientY;
    state.startTime = Date.now();
    state.isDragging = false;
    state.dragType = 'none';
    state.startVolume = engine.volume;
    state.startBrightness = brightnessFilter;

    // Start long-press timer
    if (longPressTimer) clearTimeout(longPressTimer);
    isLongPressing = false;
    longPressTimer = setTimeout(() => {
      isLongPressing = true;
      callbacks?.onLongPressStart?.();
    }, LONG_PRESS_DELAY);
  }

  function onTouchMove(e: TouchEvent) {
    // Two-finger pinch: emit scale and short-circuit single-finger logic.
    if (e.touches.length === 2 && pinchStartDist > 0) {
      const d = distance(e.touches[0], e.touches[1]);
      if (!pinchActive && Math.abs(d - pinchStartDist) > MIN_PINCH_DELTA) {
        pinchActive = true;
      }
      if (pinchActive) {
        e.preventDefault();
        const ratio = d / pinchStartDist;
        const scale = clamp(pinchStartScale * ratio, 1.0, 2.5);
        lastEmittedScale = scale;
        callbacks?.onPinch?.(scale);
      }
      return;
    }

    if (e.touches.length !== 1) return;
    const t = e.touches[0];
    const dy = t.clientY - state.startY;

    if (!state.isDragging && Math.abs(dy) > SWIPE_THRESHOLD) {
      state.isDragging = true;
      // Cancel long-press on swipe
      if (longPressTimer) { clearTimeout(longPressTimer); longPressTimer = null; }
      if (isLongPressing) { callbacks?.onLongPressEnd?.(); isLongPressing = false; }
      const rect = container.getBoundingClientRect();
      const xRatio = state.startX / rect.width;
      state.dragType = xRatio > 0.5 ? 'volume' : 'brightness';
    }

    if (state.isDragging) {
      e.preventDefault();
      const rect = container.getBoundingClientRect();
      const delta = -dy / rect.height;

      if (state.dragType === 'volume') {
        const newVol = Math.max(0, Math.min(1, state.startVolume + delta));
        engine.volume = newVol;
        volume.set(newVol);
        callbacks?.onVolumeChange?.(newVol);
      } else if (state.dragType === 'brightness') {
        brightnessFilter = Math.max(
          0.2,
          Math.min(1.5, state.startBrightness + delta),
        );
        engine.video.style.filter = `brightness(${brightnessFilter})`;
        callbacks?.onBrightnessChange?.(brightnessFilter);
      }
    }
  }

  function clamp(v: number, lo: number, hi: number): number {
    return Math.max(lo, Math.min(hi, v));
  }

  function onTouchEnd(e: TouchEvent) {
    // Pinch end (when one of the two fingers lifts).
    if (pinchActive && e.touches.length < 2) {
      pinchActive = false;
      pinchStartDist = 0;
      callbacks?.onPinchEnd?.();
      return;
    }
    if (e.changedTouches.length !== 1) return;

    // End long-press
    if (longPressTimer) { clearTimeout(longPressTimer); longPressTimer = null; }
    if (isLongPressing) {
      callbacks?.onLongPressEnd?.();
      isLongPressing = false;
      return; // Don't process as tap
    }

    const t = e.changedTouches[0];
    const dx = t.clientX - state.startX;
    const dy = t.clientY - state.startY;
    const dt = Date.now() - state.startTime;

    if (state.isDragging) {
      state.isDragging = false;
      state.dragType = 'none';
      callbacks?.onVolumeChange?.(null);
      callbacks?.onBrightnessChange?.(null);
      return;
    }

    // Tap detection
    if (Math.abs(dx) < 15 && Math.abs(dy) < 15 && dt < 300) {
      const now = Date.now();
      const tapDelta = now - state.lastTapTime;

      if (
        tapDelta < DOUBLE_TAP_DELAY &&
        Math.abs(t.clientX - state.lastTapX) < 50
      ) {
        const rect = container.getBoundingClientRect();
        const xRatio = t.clientX / rect.width;

        if (xRatio < 0.35) {
          engine.seek(engine.currentTime - 10);
          callbacks?.onDoubleTapSeek?.('left', 10);
        } else if (xRatio > 0.65) {
          engine.seek(engine.currentTime + 10);
          callbacks?.onDoubleTapSeek?.('right', 10);
        } else {
          engine.paused ? engine.play() : engine.pause();
        }
        state.lastTapTime = 0;
      } else {
        state.lastTapTime = now;
        state.lastTapX = t.clientX;
        setTimeout(() => {
          if (state.lastTapTime === now) {
            const visible = get(controlsVisible);
            const menuOpen = get(anyMenuOpen);
            if (menuOpen) {
              // Menu open — tap outside closes it
              activeMenu.set('none');
              resetIdleTimer();
            } else if (visible) {
              // Controls visible — reset timer instead of hiding
              // (controls hide via idle timer, not by tap — Netflix-style)
              resetIdleTimer();
            } else {
              // Controls hidden — show them
              resetIdleTimer();
            }
          }
        }, DOUBLE_TAP_DELAY + 10);
      }
    }
  }

  container.addEventListener('touchstart', onTouchStart, { passive: true });
  container.addEventListener('touchmove', onTouchMove, { passive: false });
  container.addEventListener('touchend', onTouchEnd, { passive: true });

  return {
    destroy: () => {
      container.removeEventListener('touchstart', onTouchStart);
      container.removeEventListener('touchmove', onTouchMove);
      container.removeEventListener('touchend', onTouchEnd);
    },
  };
}
