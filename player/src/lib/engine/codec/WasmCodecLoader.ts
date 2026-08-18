/**
 * WasmCodecLoader — capability probe + lazy fetch shims for AV1/HEVC
 * software decoders (Wave 2 C3, scoped MVP).
 *
 * Status: SCAFFOLD ONLY. Real software-decode fallback for unsupported
 * codecs requires a separate canvas-render pipeline (since shaka.Player
 * can't be told to swap in a WebCodecs decoder for a single track).
 * Building that requires:
 *   - A custom MSE shim that intercepts SourceBuffer.appendBuffer for
 *     the unsupported codec, runs WebCodecs.VideoDecoder on it, paints
 *     to a canvas, and routes audio through WebAudio.
 *   - Tight A/V sync via AudioContext.currentTime as the master clock.
 *   - Re-implementing DRM and adaptive bitrate switching for the canvas
 *     path (DRM is essentially incompatible with custom decode).
 *
 * That's a separate L+ effort. What we provide today:
 *   1. A lazy wrapper that fetches dav1d.wasm / libde265.wasm from the
 *      configured asset path WHEN AND IF the user opts in (settings.
 *      enableWasmCodecs). Returns the loaded module so future code can
 *      consume it.
 *   2. A unified `whatBrowserCannotPlay()` probe that lists codec families
 *      the host has no native support for — used as the trigger for
 *      software fallback.
 *
 * The lazy chunks are loaded by URL (NOT bundled) so the cold-start
 * bundle stays tiny. Vite's `inlineDynamicImports: true` would inline
 * them, so we use plain fetch() instead of dynamic import().
 */

import { codecProbe } from '../CodecProbe';

const DAV1D_URL = '/webplayer/assets/dav1d.wasm';
const LIBDE265_URL = '/webplayer/assets/libde265.wasm';

export interface CodecGap {
  hevc: boolean;
  av1: boolean;
}

/** Returns codec families the browser has no native support for. */
export function whatBrowserCannotPlay(): CodecGap {
  const support = codecProbe();
  return {
    hevc: !support.hevc,
    av1: !support.av1,
  };
}

/**
 * Heuristic — looks at a shaka load error and figures out if the
 * underlying cause is an unsupported codec (rather than network / DRM).
 * Used to give the user actionable feedback instead of silent failure.
 */
export function isUnsupportedCodecError(err: unknown): boolean {
  if (!err || typeof err !== 'object') return false;
  const e: any = err;
  // shaka category 4 = MEDIA, codes 4001..4032 cover codec & container issues.
  // We also accept common substring patterns from native MediaError.
  const code = Number(e.code ?? e.detail?.code);
  if (e.category === 4 || (code >= 4001 && code <= 4032)) return true;
  const msg = String(e.message ?? e.detail?.message ?? '').toLowerCase();
  return /unsupported|codec|no playable|container|mime/.test(msg);
}

/**
 * Decide whether the WASM fallback would even help in principle.
 * Currently only AV1/HEVC have reasonable WASM decoders; for a missing
 * H.264 or VP9 path, WASM rescue is unrealistic and we should just say so.
 */
export function couldWasmHelpFor(codecHint: string): boolean {
  const c = codecHint.toLowerCase();
  if (/av01|av1/.test(c)) return whatBrowserCannotPlay().av1;
  if (/hev|hvc|h265/.test(c)) return whatBrowserCannotPlay().hevc;
  return false;
}

let dav1dPromise: Promise<ArrayBuffer | null> | null = null;
let libde265Promise: Promise<ArrayBuffer | null> | null = null;

/**
 * Fetch dav1d WASM bytes. Returns null on failure (asset not deployed).
 * Cached so repeated calls don't re-fetch.
 */
export function loadDav1d(): Promise<ArrayBuffer | null> {
  if (!dav1dPromise) dav1dPromise = fetchAsset(DAV1D_URL);
  return dav1dPromise;
}

/** Fetch libde265 (HEVC) WASM bytes. */
export function loadLibde265(): Promise<ArrayBuffer | null> {
  if (!libde265Promise) libde265Promise = fetchAsset(LIBDE265_URL);
  return libde265Promise;
}

async function fetchAsset(url: string): Promise<ArrayBuffer | null> {
  try {
    const resp = await fetch(url, { signal: AbortSignal.timeout(10_000) });
    if (!resp.ok) {
      console.log(`[LWP] WASM codec asset missing: ${url} (${resp.status})`);
      return null;
    }
    const buf = await resp.arrayBuffer();
    console.log(`[LWP] WASM codec asset loaded: ${url} (${buf.byteLength} bytes)`);
    return buf;
  } catch (err) {
    console.log(`[LWP] WASM codec asset fetch failed: ${url}:`, err);
    return null;
  }
}
