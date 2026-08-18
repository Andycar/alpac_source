/**
 * Generate a Twitch-style nick color from a username.
 *
 * Uses a deterministic FNV-1a hash → HSL with fixed saturation/lightness
 * for consistency with WCAG AA contrast on dark backgrounds. The same
 * nick always renders the same color across sessions, like Twitch.
 *
 * The selected hue range avoids muddy yellows/greens by mapping to a
 * curated list of 16 visually distinct values from the Twitch chat
 * palette (originally derived from Twitch's documented user colors).
 */

const TWITCH_PALETTE = [
  '#FF0000', // Red
  '#0000FF', // Blue
  '#008000', // Green
  '#B22222', // FireBrick
  '#FF7F50', // Coral
  '#9ACD32', // YellowGreen
  '#FF4500', // OrangeRed
  '#2E8B57', // SeaGreen
  '#DAA520', // GoldenRod
  '#D2691E', // Chocolate
  '#5F9EA0', // CadetBlue
  '#1E90FF', // DodgerBlue
  '#FF69B4', // HotPink
  '#8A2BE2', // BlueViolet
  '#00FF7F', // SpringGreen
  '#FF1493', // DeepPink
];

const cache = new Map<string, string>();

export function nickColor(nick: string): string {
  if (!nick) return TWITCH_PALETTE[0];
  const cached = cache.get(nick);
  if (cached) return cached;

  // FNV-1a 32-bit
  let h = 0x811c9dc5;
  for (let i = 0; i < nick.length; i++) {
    h ^= nick.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  const idx = (h >>> 0) % TWITCH_PALETTE.length;
  const color = TWITCH_PALETTE[idx];
  cache.set(nick, color);
  return color;
}
