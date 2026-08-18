/**
 * StorageSync — Save and restore video playback position.
 *
 * Uses Lampa.Timeline if available (syncs with Lampa's "Continue watching"),
 * falls back to localStorage.
 *
 * Lampa.Timeline.view(hash) returns an object: {time, duration, percent, ...}
 * Lampa.Timeline.update(obj) expects that SAME object with mutated fields.
 */

const STORAGE_PREFIX = 'lwp_timeline_';

export interface TimelineEntry {
  time: number;
  duration: number;
  updatedAt: number;
}

/** The original Lampa timeline object passed via element.timeline */
let _lampaTimelineObj: any = null;

/**
 * Store a reference to the original Lampa timeline object so we can
 * update it in-place (Lampa.Timeline.update needs the same reference).
 */
export function setLampaTimeline(tl: any): void {
  _lampaTimelineObj = tl && typeof tl === 'object' ? tl : null;
}

export function getLampaTimeline(): any {
  return _lampaTimelineObj;
}

/**
 * Save current playback position for a given URL.
 * Called periodically during playback and on destroy.
 */
export function saveTimeline(url: string, time: number, duration: number): void {
  if (!url || !duration || duration <= 0) return;
  // Don't save if at the very start or very end
  if (time < 5 || time > duration - 10) return;

  // Update Lampa.Timeline with the original object reference
  try {
    const Lampa = (window as any).Lampa;
    if (Lampa?.Timeline && _lampaTimelineObj) {
      _lampaTimelineObj.time = time;
      _lampaTimelineObj.duration = duration;
      _lampaTimelineObj.percent = duration > 0 ? (time / duration) * 100 : 0;
      Lampa.Timeline.update(_lampaTimelineObj);
    }
  } catch {
    // ignore
  }

  // Also save to localStorage as backup
  try {
    const key = timelineKey(url);
    const entry: TimelineEntry = {
      time,
      duration,
      updatedAt: Date.now(),
    };
    localStorage.setItem(key, JSON.stringify(entry));
  } catch {
    // ignore
  }
}

/**
 * Load saved playback position for a given URL.
 * Returns the time in seconds, or 0 if no saved position.
 */
export function loadTimeline(url: string): number {
  if (!url) return 0;
  const key = timelineKey(url);

  // Try localStorage
  try {
    const raw = localStorage.getItem(key);
    if (raw) {
      const entry: TimelineEntry = JSON.parse(raw);
      // Only restore if saved within last 30 days
      if (Date.now() - entry.updatedAt < 30 * 24 * 60 * 60 * 1000) {
        return entry.time;
      }
    }
  } catch {
    // ignore
  }

  return 0;
}

/**
 * Get a 0..1 watch-progress fraction for a given URL based on the saved
 * timeline (Wave polish — used by EpisodeDrawer to render progress dots).
 * Returns 0 when no entry exists or it's expired.
 *
 * Also checks Lampa.Timeline.view(hash) for cross-source progress when
 * available.
 */
export function loadTimelineProgress(url: string): number {
  if (!url) return 0;
  // Local entry
  try {
    const raw = localStorage.getItem(timelineKey(url));
    if (raw) {
      const entry: TimelineEntry = JSON.parse(raw);
      if (
        Date.now() - entry.updatedAt < 30 * 24 * 60 * 60 * 1000 &&
        entry.duration > 0
      ) {
        return Math.min(1, entry.time / entry.duration);
      }
    }
  } catch {
    /* ignore */
  }
  // Lampa.Timeline lookup (the activity hash is unknown to us, so this
  // is best-effort — only works if Lampa exposes a URL-keyed view).
  try {
    const Lampa = (window as any).Lampa;
    if (Lampa?.Timeline?.view) {
      const tl = Lampa.Timeline.view(timelineKey(url));
      if (tl?.duration > 0 && typeof tl.time === 'number') {
        return Math.min(1, tl.time / tl.duration);
      }
    }
  } catch {
    /* ignore */
  }
  return 0;
}

/**
 * Clear saved position (e.g. when video finishes).
 */
export function clearTimeline(url: string): void {
  if (!url) return;

  // Clear Lampa timeline
  try {
    const Lampa = (window as any).Lampa;
    if (Lampa?.Timeline && _lampaTimelineObj) {
      _lampaTimelineObj.time = 0;
      _lampaTimelineObj.duration = 0;
      _lampaTimelineObj.percent = 0;
      Lampa.Timeline.update(_lampaTimelineObj);
    }
  } catch {
    // ignore
  }

  try {
    localStorage.removeItem(timelineKey(url));
  } catch {
    // ignore
  }
}

function timelineKey(url: string): string {
  // Use a hash of the URL to avoid extremely long keys
  let hash = 0;
  for (let i = 0; i < url.length; i++) {
    const char = url.charCodeAt(i);
    hash = ((hash << 5) - hash + char) | 0;
  }
  return STORAGE_PREFIX + Math.abs(hash).toString(36);
}
