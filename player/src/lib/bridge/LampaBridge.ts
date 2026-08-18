import { mount, unmount } from 'svelte';
import Player from '../ui/Player.svelte';
import type { PlayElement } from '../engine/PlaybackEngine';
import { mapLampaToPlayElement } from './PlayElementMapper';
import { extractDominantColor } from '../utils/colorExtract';
import { accentColor, accentColorDark, accentColorLight } from '../stores/ui';

/**
 * Bridge between Lampa's player API and our custom player.
 *
 * Strategy: late-binding interception.
 * Other plugins (ads.js, etc.) also monkey-patch Lampa.Player.play().
 * If we patch too early, they overwrite us. So we:
 *   1. Start loading shaka-player eagerly (constructor)
 *   2. Wait for appready + small delay (all plugins initialized)
 *   3. THEN apply our monkey-patch (we're the outermost wrapper)
 */
export class LampaBridge {
  private playerComponent: ReturnType<typeof mount> | null = null;
  private playerContainer: HTMLDivElement | null = null;
  private originalVideoFn: (() => HTMLVideoElement) | null = null;
  private originalPlayFn: ((element: any) => void) | null = null;
  private shakaReady: Promise<void>;
  private active = false;
  private hooked = false;

  constructor() {
    this.shakaReady = this.loadShaka();
  }

  register(): void {
    console.log('[LWP] Registering, scheduling late-bind hook...');
    this.scheduleLateHook();
  }

  /**
   * Wait until all plugins have initialized, then apply our hook last.
   * Uses multiple strategies because the 'app' event may have already fired.
   */
  private scheduleLateHook(): void {
    const apply = () => {
      if (this.hooked) return;
      const Lampa = (window as any).Lampa;
      if (!Lampa?.Player) {
        console.warn('[LWP] Lampa.Player still not found');
        return;
      }
      this.hookIntoLampa(Lampa);
    };

    // Strategy 1: If appready already set, defer briefly and apply
    if ((window as any).appready) {
      console.log('[LWP] appready already set, deferring hook 300ms');
      setTimeout(apply, 300);
      return;
    }

    // Strategy 2: Try to follow the 'app' event
    const Lampa = (window as any).Lampa;
    if (Lampa?.Listener) {
      Lampa.Listener.follow('app', () => {
        console.log('[LWP] "app" event fired, deferring hook 500ms');
        setTimeout(apply, 500);
      });
    }

    // Strategy 3: ALWAYS poll as backup — the 'app' event may have already
    // fired or may never fire for this plugin load path.
    const interval = setInterval(() => {
      if ((window as any).appready && (window as any).Lampa?.Player) {
        clearInterval(interval);
        if (!this.hooked) {
          console.log('[LWP] Detected appready via poll, applying hook');
          setTimeout(apply, 300);
        }
      }
    }, 200);
    setTimeout(() => clearInterval(interval), 30000);
  }

  private hookIntoLampa(Lampa: any): void {
    if (this.hooked) return;
    this.hooked = true;

    // Capture whatever play() is NOW — after all other plugins have wrapped it.
    // This preserves the full chain (ads.js wrapper → original play).
    this.originalPlayFn = Lampa.Player.play.bind(Lampa.Player);

    console.log('[LWP] Applying late-bind hook on Lampa.Player.play()...');
    console.log('[LWP] Wrapped function:', Lampa.Player.play.toString().substring(0, 80));

    // Our interceptor is now the outermost wrapper
    const self = this;
    Lampa.Player.play = function (element: any) {
      self.handlePlay(element, Lampa);
    };

    // Listen for player destroy to clean up
    if (Lampa.Player.listener) {
      Lampa.Player.listener.follow('destroy', () => {
        this.handleDestroy(Lampa);
      });
    }

    console.log('[LWP] Ready — next play() will use custom player');
  }

  private loadShaka(): Promise<void> {
    if ((window as any).shaka) {
      console.log('[LWP] shaka-player already available');
      return Promise.resolve();
    }

    return new Promise((resolve, reject) => {
      const script = document.createElement('script');
      script.src = '/webplayer/shaka-player.compiled.js';
      script.onload = () => {
        console.log('[LWP] shaka-player loaded');
        (window as any).shaka.polyfill.installAll();
        resolve();
      };
      script.onerror = () => {
        console.error('[LWP] Failed to load shaka-player');
        reject(new Error('Failed to load shaka-player'));
      };
      document.head.appendChild(script);
    });
  }

  private async handlePlay(rawElement: any, Lampa: any): Promise<void> {
    console.log('[LWP] handlePlay called with:', rawElement?.title, rawElement?.url?.substring(0, 60));

    const element: PlayElement = mapLampaToPlayElement(rawElement);

    if (!element.url) {
      console.warn('[LWP] No URL in play element, falling back to default');
      this.originalPlayFn?.(rawElement);
      return;
    }

    // Wait for shaka (should already be loaded by now since we started eagerly)
    try {
      await this.shakaReady;
    } catch {
      console.error('[LWP] shaka-player not available, falling back to default');
      this.originalPlayFn?.(rawElement);
      return;
    }

    console.log('[LWP] Intercepted play:', element.title, element.url.substring(0, 80));

    // Destroy any existing custom player
    this.destroyPlayer(Lampa);

    this.active = true;

    // Create container
    this.playerContainer = document.createElement('div');
    this.playerContainer.id = 'lwp-container';
    document.body.appendChild(this.playerContainer);

    // Mount Svelte player
    let closeCalled = false;
    this.playerComponent = mount(Player, {
      target: this.playerContainer,
      props: {
        element,
        onClose: () => {
          if (closeCalled) return; // prevent double-back
          closeCalled = true;
          this.destroyPlayer(Lampa);
          // We intercepted Lampa.Player.play() — Lampa never switched
          // Controller to 'player' mode. Controller is still on the
          // episodes/film page. Just destroy our player overlay — Lampa's
          // keyboard handler resumes automatically once our capture-phase
          // keydown listener is removed (in Player.svelte onDestroy).
          // Do NOT call Activity.back() or Player.close() — they would
          // navigate away from the current page (unwanted double-back).
        },
      },
    });

    // Accent color: respect manual override from settings, else extract
    // dominant color from poster (Pack 3 polish).
    let manualOverride = '';
    try {
      const raw = localStorage.getItem('lwp_settings');
      if (raw) manualOverride = JSON.parse(raw)?.accentOverride || '';
    } catch { /* ignore */ }

    if (manualOverride) {
      accentColor.set(manualOverride);
      accentColorDark.set(manualOverride);
      accentColorLight.set(manualOverride);
      if (this.playerContainer) {
        this.playerContainer.style.setProperty('--lwp-accent', manualOverride);
        this.playerContainer.style.setProperty('--lwp-accent-dark', manualOverride);
        this.playerContainer.style.setProperty('--lwp-accent-light', manualOverride);
      }
    } else if (element.poster) {
      extractDominantColor(element.poster).then((colors) => {
        accentColor.set(colors.primary);
        accentColorDark.set(colors.dark);
        accentColorLight.set(colors.accent);
        if (this.playerContainer) {
          this.playerContainer.style.setProperty('--lwp-accent', colors.primary);
          this.playerContainer.style.setProperty('--lwp-accent-dark', colors.dark);
          this.playerContainer.style.setProperty('--lwp-accent-light', colors.accent);
        }
      });
    }

    // Monkey-patch Lampa.PlayerVideo.video() to return our video element
    if (Lampa.PlayerVideo && !this.originalVideoFn) {
      this.originalVideoFn = Lampa.PlayerVideo.video;
      const self = this;
      Lampa.PlayerVideo.video = () => {
        if (self.active) {
          const vid = self.playerContainer?.querySelector('video');
          if (vid) return vid;
        }
        return self.originalVideoFn?.();
      };
    }
  }

  private handleDestroy(Lampa: any): void {
    this.destroyPlayer(Lampa);
  }

  private destroyPlayer(Lampa: any): void {
    if (!this.active) return;
    this.active = false;

    if (this.playerComponent) {
      try {
        unmount(this.playerComponent);
      } catch {
        // May already be unmounted
      }
      this.playerComponent = null;
    }

    if (this.playerContainer) {
      this.playerContainer.remove();
      this.playerContainer = null;
    }
  }
}
