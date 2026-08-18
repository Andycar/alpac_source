/**
 * Extract dominant vibrant color from a poster image.
 * Uses canvas pixel sampling for speed (no external deps).
 */

interface ExtractedColors {
  primary: string;   // Dominant vibrant color
  accent: string;    // Lighter variant
  dark: string;      // Darker variant for backgrounds
}

const colorCache = new Map<string, ExtractedColors>();

const FALLBACK: ExtractedColors = {
  primary: '#4fc3f7',
  accent: '#81d4fa',
  dark: '#0277bd',
};

/**
 * Extract dominant vibrant color from an image URL.
 * Returns CSS color strings. Cached per URL.
 */
export async function extractDominantColor(imageUrl: string): Promise<ExtractedColors> {
  if (!imageUrl) return FALLBACK;
  if (colorCache.has(imageUrl)) return colorCache.get(imageUrl)!;

  try {
    const colors = await doExtract(imageUrl);
    colorCache.set(imageUrl, colors);
    return colors;
  } catch {
    return FALLBACK;
  }
}

async function doExtract(url: string): Promise<ExtractedColors> {
  const img = new Image();
  img.crossOrigin = 'anonymous';

  await new Promise<void>((resolve, reject) => {
    img.onload = () => resolve();
    img.onerror = () => reject(new Error('Image load failed'));
    img.src = url;
  });

  const canvas = document.createElement('canvas');
  const size = 8; // 8×8 grid = 64 samples
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext('2d')!;
  ctx.drawImage(img, 0, 0, size, size);
  const data = ctx.getImageData(0, 0, size, size).data;

  // Collect pixels, skip near-black and near-white
  const pixels: Array<[number, number, number]> = [];
  for (let i = 0; i < data.length; i += 4) {
    const r = data[i], g = data[i + 1], b = data[i + 2];
    const brightness = (r + g + b) / 3;
    if (brightness < 30 || brightness > 230) continue;
    // Skip very desaturated (gray) pixels
    const max = Math.max(r, g, b);
    const min = Math.min(r, g, b);
    const saturation = max === 0 ? 0 : (max - min) / max;
    if (saturation < 0.15) continue;
    pixels.push([r, g, b]);
  }

  if (pixels.length === 0) return FALLBACK;

  // Find most vibrant pixel (highest saturation × brightness)
  let bestScore = 0;
  let bestPixel: [number, number, number] = pixels[0];
  for (const [r, g, b] of pixels) {
    const max = Math.max(r, g, b);
    const min = Math.min(r, g, b);
    const saturation = max === 0 ? 0 : (max - min) / max;
    const brightness = max / 255;
    const score = saturation * brightness;
    if (score > bestScore) {
      bestScore = score;
      bestPixel = [r, g, b];
    }
  }

  const [r, g, b] = bestPixel;
  return {
    primary: `rgb(${r}, ${g}, ${b})`,
    accent: `rgb(${Math.min(255, r + 40)}, ${Math.min(255, g + 40)}, ${Math.min(255, b + 40)})`,
    dark: `rgb(${Math.max(0, r - 60)}, ${Math.max(0, g - 60)}, ${Math.max(0, b - 60)})`,
  };
}

// ---------------------------------------------------------------------------
// Edge color extraction — used by AmbilightLayer (Wave 1 B3).
// ---------------------------------------------------------------------------

export interface EdgeColors {
  top: string;
  right: string;
  bottom: string;
  left: string;
  accent: string;
}

/**
 * Sample the four edges of a playing video into a downsampled canvas
 * and return the dominant color along each edge. Designed to be cheap
 * enough to call every 500ms.
 *
 * Throws if the canvas read is CORS-tainted (DRM streams). Caller is
 * expected to catch and disable ambilight in that case.
 */
export function extractEdgeColors(
  video: HTMLVideoElement,
  scratch: HTMLCanvasElement,
): EdgeColors {
  const W = 32;
  const H = 18;
  scratch.width = W;
  scratch.height = H;
  const ctx = scratch.getContext('2d', { willReadFrequently: true });
  if (!ctx) throw new Error('canvas 2d unavailable');

  // Throws if cross-origin tainted.
  ctx.drawImage(video, 0, 0, W, H);
  const data = ctx.getImageData(0, 0, W, H).data;

  // Helper: take a row or a column and return its mean RGB.
  const idx = (x: number, y: number) => (y * W + x) * 4;

  const meanRow = (y: number): [number, number, number] => {
    let r = 0, g = 0, b = 0;
    for (let x = 0; x < W; x++) {
      const i = idx(x, y);
      r += data[i]; g += data[i + 1]; b += data[i + 2];
    }
    return [r / W, g / W, b / W];
  };
  const meanCol = (x: number): [number, number, number] => {
    let r = 0, g = 0, b = 0;
    for (let y = 0; y < H; y++) {
      const i = idx(x, y);
      r += data[i]; g += data[i + 1]; b += data[i + 2];
    }
    return [r / H, g / H, b / H];
  };

  // Sample the outermost band (2 rows/cols deep) and average — single-row
  // samples flicker on grainy footage.
  const avgBands = (
    a: [number, number, number],
    b: [number, number, number],
  ): [number, number, number] => [(a[0] + b[0]) / 2, (a[1] + b[1]) / 2, (a[2] + b[2]) / 2];

  const top = avgBands(meanRow(0), meanRow(1));
  const bottom = avgBands(meanRow(H - 1), meanRow(H - 2));
  const left = avgBands(meanCol(0), meanCol(1));
  const right = avgBands(meanCol(W - 1), meanCol(W - 2));

  // Build an "accent" color from the most saturated of the four edges.
  const candidates: Array<[number, number, number]> = [top, right, bottom, left];
  let accent = candidates[0];
  let bestSat = -1;
  for (const c of candidates) {
    const max = Math.max(c[0], c[1], c[2]);
    const min = Math.min(c[0], c[1], c[2]);
    const sat = max === 0 ? 0 : (max - min) / max;
    if (sat > bestSat) { bestSat = sat; accent = c; }
  }

  return {
    top: rgbStr(top),
    right: rgbStr(right),
    bottom: rgbStr(bottom),
    left: rgbStr(left),
    accent: rgbStr(accent),
  };
}

function rgbStr([r, g, b]: [number, number, number]): string {
  return `rgb(${Math.round(r)}, ${Math.round(g)}, ${Math.round(b)})`;
}

/**
 * Smooth a new sample toward the previous value (linear interpolation).
 * Used by AmbilightLayer to avoid jarring color jumps between samples.
 */
export function lerpEdgeColors(
  prev: EdgeColors,
  next: EdgeColors,
  alpha: number,
): EdgeColors {
  return {
    top: lerpColor(prev.top, next.top, alpha),
    right: lerpColor(prev.right, next.right, alpha),
    bottom: lerpColor(prev.bottom, next.bottom, alpha),
    left: lerpColor(prev.left, next.left, alpha),
    accent: lerpColor(prev.accent, next.accent, alpha),
  };
}

function lerpColor(prev: string, next: string, alpha: number): string {
  const p = parseRgb(prev);
  const n = parseRgb(next);
  if (!p || !n) return next;
  return rgbStr([
    p[0] + (n[0] - p[0]) * alpha,
    p[1] + (n[1] - p[1]) * alpha,
    p[2] + (n[2] - p[2]) * alpha,
  ]);
}

function parseRgb(s: string): [number, number, number] | null {
  const m = /rgb\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/.exec(s);
  if (!m) return null;
  return [parseInt(m[1], 10), parseInt(m[2], 10), parseInt(m[3], 10)];
}
