/**
 * Apply subtitle vertical position + offset to the live shaka text container.
 * Pack 3 polish.
 */

export type SubtitlePosition = 'bottom' | 'middle' | 'top';

export function applySubtitlePosition(position: SubtitlePosition, offsetVh: number): void {
  // Shaka renders cues into <div class="shaka-text-container"> inside the
  // video container. We tweak its CSS to override the default bottom-25%.
  const root = document.querySelector<HTMLElement>('.shaka-text-container');
  if (!root) return;

  const off = isFinite(offsetVh) ? offsetVh : 0;

  switch (position) {
    case 'top':
      root.style.alignItems = 'flex-start';
      root.style.justifyContent = 'center';
      root.style.paddingTop = `${Math.max(8, 8 - off)}vh`;
      root.style.paddingBottom = '';
      break;
    case 'middle':
      root.style.alignItems = 'center';
      root.style.justifyContent = 'center';
      root.style.paddingTop = '';
      root.style.paddingBottom = '';
      root.style.transform = `translateY(${off}vh)`;
      break;
    case 'bottom':
    default:
      root.style.alignItems = 'flex-end';
      root.style.justifyContent = 'center';
      root.style.paddingBottom = `${Math.max(4, 12 + off)}vh`;
      root.style.paddingTop = '';
      root.style.transform = '';
      break;
  }
}
