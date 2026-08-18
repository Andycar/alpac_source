import { writable, derived, get } from 'svelte/store';

// Internal store
const _controlsVisible = writable(true);
export const controlsVisible = { subscribe: _controlsVisible.subscribe };

export const isFullscreen = writable(false);
export const isPiP = writable(false);
export const showStats = writable(false);
/** Wave 2 C4 — toggles the DRM diagnostics panel. */
export const showDrmInfo = writable(false);
/** Polish — shortcuts cheat sheet overlay. */
export const showShortcuts = writable(false);

/**
 * Watchparty chat sidebar mode (Twitch-style).
 *  - 'hidden'  — chat collapsed, only a thin tab to re-open it
 *  - 'side'    — full sidebar on the right with input + message list
 *  - 'overlay' — transparent floating messages over the video, no input,
 *                pointer-events disabled (player controls underneath stay
 *                clickable). Old messages auto-fade.
 *
 * Default 'hidden' so the chat doesn't appear until the user joins a
 * room and explicitly opens it. After successful join Player.svelte
 * sets it to 'side' so the panel slides in immediately.
 */
export const chatSidebarMode = writable<'hidden' | 'side' | 'overlay'>('hidden');

// Adaptive accent color (extracted from poster)
export const accentColor = writable('#4fc3f7');
export const accentColorDark = writable('#0277bd');
export const accentColorLight = writable('#81d4fa');

// Mini player mode
export const miniPlayerMode = writable(false);
export const miniPlayerPosition = writable<'tl' | 'tr' | 'bl' | 'br'>('br');

// Active menu (only one at a time)
export type MenuType =
  | 'none'
  | 'quality'
  | 'audio'
  | 'subtitles'
  | 'settings'
  | 'episodes'
  | 'voices'
  /** Wave 2 C2 — watchparty room join/leave panel. */
  | 'watchparty'
  /** Pack 2 polish — bookmarks list/edit panel. */
  | 'bookmarks';

export const activeMenu = writable<MenuType>('none');

export const anyMenuOpen = derived(activeMenu, ($menu) => $menu !== 'none');

// Idle timer for auto-hiding controls
let idleTimer: ReturnType<typeof setTimeout> | null = null;

// Touch devices need more time to interact with menus
const isTouchDevice =
  typeof window !== 'undefined' && matchMedia('(hover: none), (pointer: coarse)').matches;
const IDLE_TIMEOUT = isTouchDevice ? 12000 : 3500;
const MENU_IDLE_TIMEOUT = 30000;

let _menuOpen = false;
activeMenu.subscribe((m) => { _menuOpen = m !== 'none'; });

export function resetIdleTimer(): void {
  _controlsVisible.set(true);
  if (idleTimer) clearTimeout(idleTimer);
  const timeout = _menuOpen ? MENU_IDLE_TIMEOUT : IDLE_TIMEOUT;
  idleTimer = setTimeout(() => {
    if (_menuOpen) {
      resetIdleTimer();
      return;
    }
    _controlsVisible.set(false);
    activeMenu.set('none');
  }, timeout);
}

export function showControlsPermanently(): void {
  if (idleTimer) clearTimeout(idleTimer);
  _controlsVisible.set(true);
}

export function hideControls(): void {
  if (idleTimer) clearTimeout(idleTimer);
  activeMenu.set('none');
  _controlsVisible.set(false);
}

export function pokeIdleTimer(): void {
  if (idleTimer) {
    clearTimeout(idleTimer);
    const timeout = _menuOpen ? MENU_IDLE_TIMEOUT : IDLE_TIMEOUT;
    idleTimer = setTimeout(() => {
      if (_menuOpen) {
        resetIdleTimer();
        return;
      }
      _controlsVisible.set(false);
      activeMenu.set('none');
    }, timeout);
  }
}

export function toggleMenu(menu: MenuType): void {
  activeMenu.update((current) => (current === menu ? 'none' : menu));
  resetIdleTimer();
}

// Fullscreen helpers
export function toggleFullscreen(container: HTMLElement): void {
  if (document.fullscreenElement) {
    document.exitFullscreen();
  } else {
    container.requestFullscreen();
  }
}

export function setupFullscreenListener(): () => void {
  const handler = () => {
    isFullscreen.set(!!document.fullscreenElement);
  };
  document.addEventListener('fullscreenchange', handler);
  return () => document.removeEventListener('fullscreenchange', handler);
}
