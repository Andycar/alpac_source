/**
 * Haptic feedback utility for mobile touch interactions.
 * Uses navigator.vibrate() when supported.
 */

const supported = typeof navigator !== 'undefined' && 'vibrate' in navigator;

/** Short tap feedback (10ms) */
export function tapFeedback(): void {
  if (supported) navigator.vibrate(10);
}

/** Seek feedback (two short pulses) */
export function seekFeedback(): void {
  if (supported) navigator.vibrate([10, 30, 10]);
}

/** Volume feedback (very short) */
export function volumeFeedback(): void {
  if (supported) navigator.vibrate(5);
}
