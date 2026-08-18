/**
 * Custom Svelte transitions for premium menu & component animations.
 * Shared across all menus for visual consistency.
 */

import { cubicOut, cubicIn } from 'svelte/easing';

interface TransitionParams {
  duration?: number;
  delay?: number;
}

/**
 * Menu slide-up transition: slides from bottom with backdrop blur fade.
 * Used for QualityMenu, AudioMenu, SubtitleMenu, SettingsMenu, VoiceMenu.
 */
export function menuSlideUp(node: Element, params: TransitionParams = {}) {
  const { duration = 250, delay = 0 } = params;
  return {
    delay,
    duration,
    css: (t: number) => {
      const eased = cubicOut(t);
      return `
        opacity: ${eased};
        transform: translateY(${(1 - eased) * 12}px) scale(${0.97 + eased * 0.03});
      `;
    },
  };
}

/**
 * Menu slide-out (reverse of slide-up).
 */
export function menuSlideOut(node: Element, params: TransitionParams = {}) {
  const { duration = 180, delay = 0 } = params;
  return {
    delay,
    duration,
    css: (t: number) => {
      const eased = cubicIn(t);
      return `
        opacity: ${eased};
        transform: translateY(${(1 - eased) * 8}px);
      `;
    },
  };
}

/**
 * Drawer slide-in from right. Used for EpisodeDrawer.
 */
export function drawerSlideRight(node: Element, params: TransitionParams = {}) {
  const { duration = 300, delay = 0 } = params;
  return {
    delay,
    duration,
    css: (t: number) => {
      const eased = cubicOut(t);
      return `
        transform: translateX(${(1 - eased) * 100}%);
      `;
    },
  };
}

/**
 * Backdrop fade transition.
 */
export function backdropFade(node: Element, params: TransitionParams = {}) {
  const { duration = 200, delay = 0 } = params;
  return {
    delay,
    duration,
    css: (t: number) => `opacity: ${t};`,
  };
}

/**
 * Spring scale for checkmarks, badges, and small elements.
 * Uses a spring-like cubic-bezier effect.
 */
export function springScale(node: Element, params: TransitionParams = {}) {
  const { duration = 300, delay = 0 } = params;
  return {
    delay,
    duration,
    css: (t: number) => {
      // Spring-like overshoot
      const s = t < 0.5
        ? 4 * t * t * t
        : 1 - Math.pow(-2 * t + 2, 3) / 2;
      const scale = 0.6 + s * 0.5; // 0.6 → 1.1 → 1.0
      return `transform: scale(${scale}); opacity: ${Math.min(1, t * 2)};`;
    },
  };
}

/**
 * Pop animation for play/pause overlay icon.
 * Scale up with spring and fade out.
 */
export function popFadeOut(node: Element, params: TransitionParams = {}) {
  const { duration = 450, delay = 0 } = params;
  return {
    delay,
    duration,
    css: (t: number) => {
      // Reverse t because this is used as an "out" transition
      const progress = 1 - t;
      const scale = 0.8 + progress * 0.5; // 0.8 → 1.3
      const opacity = Math.max(0, 1 - progress * 1.5);
      return `transform: translate(-50%, -50%) scale(${scale}); opacity: ${opacity};`;
    },
  };
}
