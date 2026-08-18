/**
 * Detect if running in a web browser (not Android WebView, Tizen, webOS, etc.)
 * Returns true if the custom player should activate.
 */
export function isWebBrowser(): boolean {
  if (typeof window === 'undefined') return false;

  // Android WebView: Lampa Android app injects AndroidJS global
  if ((window as any).AndroidJS) return false;

  // Tizen (Samsung Smart TV)
  if ((window as any).tizen) return false;

  // Cordova/Capacitor hybrid apps
  if ((window as any).cordova) return false;

  const ua = navigator.userAgent;

  // webOS (LG Smart TV) — check UA, not globals.
  // The webOS NPM library injects window.webOS in Electron/browser contexts too.
  if (/Web0S|webOS/.test(ua)) return false;

  // Generic WebView detection via user agent
  if (/wv\)/.test(ua)) return false;

  return true;
}

/**
 * Decide if the web player should activate.
 *
 * By default, uses isWebBrowser() — disables on native platforms.
 * However, if the server sets window.__lwpForceEnable = true (via
 * web_player_android config), the player activates even on Android
 * WebView, giving users the full premium web player experience.
 */
export function shouldActivatePlayer(): boolean {
  if (typeof window === 'undefined') return false;

  // Server explicitly opted in for this platform (e.g. Android WebView)
  if ((window as any).__lwpForceEnable === true) return true;

  return isWebBrowser();
}

/**
 * Detect Smart TVs / low-power browser environments.
 *
 * Used as a gate for "heavy" features (Ambilight, WebCodecs WASM decode,
 * detailed live stats) so they don't melt Tizen/WebOS devices. Also
 * influences BufferStrategy: low-power TV gets the 'tv-low' profile.
 *
 * Note: Tizen/WebOS are short-circuited away by shouldActivatePlayer(),
 * so isLowPowerTV() will mostly fire on (a) the __lwpForceEnable Android
 * WebView path on TV-shaped devices and (b) low-RAM web browsers.
 */
export function isLowPowerTV(): boolean {
  if (typeof window === 'undefined') return false;

  const ua = navigator.userAgent || '';

  // Explicit TV identifiers (covers Tizen/WebOS even when player is force-enabled)
  if (/Tizen|Web0S|webOS|SmartTV|HbbTV|NetCast/.test(ua)) return true;
  if (/Android.*TV|Crosswalk/.test(ua)) return true;

  // Low device memory hint (Chrome only) — < 2 GB
  const dm = (navigator as any).deviceMemory;
  if (typeof dm === 'number' && dm > 0 && dm < 2) return true;

  // Hardware concurrency hint — set-top boxes typically expose 1-2 cores
  const hc = navigator.hardwareConcurrency;
  if (typeof hc === 'number' && hc > 0 && hc <= 2) {
    // Only treat as low-power if combined with a hover-less/coarse pointer
    // (otherwise this captures legitimately weak laptops).
    if (matchMedia('(hover: none) and (pointer: coarse)').matches) return true;
  }

  return false;
}
