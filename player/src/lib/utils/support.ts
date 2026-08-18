/**
 * Platform capability detection — single source of truth.
 *
 * Every feature that depends on a non-universal browser API checks here
 * first so we can gracefully hide / disable it on platforms that don't
 * support it (Safari, Firefox, older Chromium-based TVs).
 *
 * Memoized: probes run on first access and cache the result. Some probes
 * (mediaSession, output device picker) need to be re-checked because
 * they can change on user gesture, so they're functions not constants.
 */

let _cache: Partial<Record<string, boolean>> = {};

function once(key: string, probe: () => boolean): boolean {
  if (key in _cache) return _cache[key]!;
  let result = false;
  try { result = probe(); } catch { result = false; }
  _cache[key] = result;
  return result;
}

// ─── Audio ──────────────────────────────────────────────────

/** AudioContext available — required for boost/voice/loudness/HRTF. */
export function hasAudioContext(): boolean {
  return once('audioContext', () =>
    typeof window !== 'undefined' &&
    (typeof (window as any).AudioContext === 'function' ||
     typeof (window as any).webkitAudioContext === 'function'),
  );
}

/** HTMLMediaElement.setSinkId() — required for audio output device picker. */
export function hasOutputDevicePicker(): boolean {
  return once('outputDevicePicker', () => {
    const proto = (HTMLMediaElement as any).prototype;
    return typeof proto?.setSinkId === 'function' &&
           typeof navigator?.mediaDevices?.enumerateDevices === 'function';
  });
}

// ─── Speech recognition ─────────────────────────────────────

export function hasSpeechRecognition(): boolean {
  return once('speechRecognition', () => {
    const w: any = window;
    return typeof w?.SpeechRecognition === 'function' ||
           typeof w?.webkitSpeechRecognition === 'function';
  });
}

// ─── Video / picture-in-picture ─────────────────────────────

export function hasDocumentPiP(): boolean {
  return once('docPip', () => {
    const dpip: any = (window as any).documentPictureInPicture;
    return typeof dpip?.requestWindow === 'function';
  });
}

export function hasElementPiP(): boolean {
  return once('elementPip', () =>
    typeof (HTMLVideoElement as any).prototype?.requestPictureInPicture === 'function',
  );
}

// ─── Network / battery ──────────────────────────────────────

export function hasNetworkInformation(): boolean {
  return once('netInfo', () =>
    typeof (navigator as any)?.connection === 'object',
  );
}

export function hasBatteryAPI(): boolean {
  return once('battery', () =>
    typeof (navigator as any)?.getBattery === 'function',
  );
}

// ─── Frame timing / WebCodecs ───────────────────────────────

export function hasRVFC(): boolean {
  return once('rvfc', () =>
    typeof (HTMLVideoElement as any).prototype?.requestVideoFrameCallback === 'function',
  );
}

export function hasWebCodecs(): boolean {
  return once('webCodecs', () =>
    typeof (window as any)?.VideoDecoder === 'function' &&
    typeof (window as any)?.VideoFrame === 'function',
  );
}

// ─── Misc ───────────────────────────────────────────────────

export function hasClipboard(): boolean {
  return once('clipboard', () =>
    typeof navigator?.clipboard?.writeText === 'function',
  );
}

export function hasVibrate(): boolean {
  return once('vibrate', () =>
    typeof navigator?.vibrate === 'function',
  );
}

/**
 * Reset all probe caches. Useful for tests / when user gesture flips a
 * permission state (e.g. mic permission for SpeechRecognition).
 */
export function resetSupportCache(): void {
  _cache = {};
}
