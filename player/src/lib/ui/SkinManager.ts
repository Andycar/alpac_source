/**
 * SkinManager — applies the user-selected visual theme + UI density to
 * the player root element (Wave 1 B3).
 *
 * Themes are pure CSS — see styles/themes.css. Each theme sets a small
 * set of CSS custom properties (--lwp-glass-bg, --lwp-radius, etc.)
 * that the rest of the player styles consume.
 *
 * Density (compact/normal/spacious) only affects spacing, not colors.
 */

export type SkinTheme = 'cinematic' | 'minimal' | 'classic' | 'oled-black';
export type SkinDensity = 'compact' | 'normal' | 'spacious';

const VALID_THEMES: ReadonlyArray<SkinTheme> = ['cinematic', 'minimal', 'classic', 'oled-black'];
const VALID_DENSITIES: ReadonlyArray<SkinDensity> = ['compact', 'normal', 'spacious'];

export class SkinManager {
  constructor(private rootEl: HTMLElement) {}

  applyTheme(name: string): void {
    const theme = (VALID_THEMES as readonly string[]).includes(name)
      ? (name as SkinTheme)
      : 'cinematic';
    this.rootEl.setAttribute('data-theme', theme);
  }

  applyDensity(density: string): void {
    const d = (VALID_DENSITIES as readonly string[]).includes(density)
      ? (density as SkinDensity)
      : 'normal';
    this.rootEl.setAttribute('data-density', d);
  }

  applyEdgeGlow(enabled: boolean): void {
    if (enabled) {
      this.rootEl.setAttribute('data-edge-glow', '');
    } else {
      this.rootEl.removeAttribute('data-edge-glow');
    }
  }
}
