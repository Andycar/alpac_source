/**
 * AssRenderer — on-device ASS/SSA subtitle rendering via JASSUB (libass wasm).
 *
 * The transcoding server historically burned ASS into the video (forcing a
 * re-encode) or stripped it to WebVTT (losing all styling). It now emits the
 * raw track as a `subs_<idx>.ass` sibling plus the MKV's embedded fonts at
 * `/transcoding/{id}/fonts` (see lampac-go transcoding_fonts.go). When the
 * user picks such a track we overlay a JASSUB canvas over the video and let
 * libass rasterise the dialogue with the release's own typefaces — full
 * styling, zero server re-encode.
 *
 * Nothing jassub-related lives in the webplayer bundle: the library itself is
 * an esbuild-prebundled ESM asset (jassub.esm.js — see player/package.json
 * "build:assets") loaded via a vite-ignored dynamic import, and the worker +
 * wasm (~4MB) are fetched by it at runtime. All served from
 * /webplayer/assets/ (lampac-go webplayerAssetHandler, files under
 * plugins/webplayer-assets/), so players that never see an ASS track download
 * nothing extra — and vite never has to bundle jassub's internal workers.
 */

const ASSET_BASE = '/webplayer/assets';

export class AssRenderer {
  private instance: any = null;
  private currentUrl: string | null = null;

  static isSupported(): boolean {
    return typeof WebAssembly !== 'undefined' && typeof Worker !== 'undefined';
  }

  get active(): boolean {
    return this.instance != null;
  }

  get activeUrl(): string | null {
    return this.currentUrl;
  }

  /**
   * Start rendering `assUrl` over `video`. `fontUrls` are the embedded fonts
   * extracted from the source container — callers should only pick the JASSUB
   * path when at least one is available (jassub v2 bundles no fallback font,
   * so a fontless render risks blank glyphs; the WebVTT twin is safer then).
   */
  async start(video: HTMLVideoElement, assUrl: string, fontUrls: string[]): Promise<void> {
    this.stop();
    // Runtime ESM import — @vite-ignore keeps the bundler away (see header).
    const mod = await import(/* @vite-ignore */ `${ASSET_BASE}/jassub.esm.js`);
    const JASSUB = mod.default;
    this.instance = new JASSUB({
      video,
      subUrl: assUrl,
      fonts: fontUrls,
      workerUrl: `${ASSET_BASE}/jassub-worker.js`,
      wasmUrl: `${ASSET_BASE}/jassub-worker.wasm`,
      modernWasmUrl: `${ASSET_BASE}/jassub-worker-modern.wasm`,
      // Budget-TV guardrails: cap the render height so a 4K canvas doesn't
      // melt weak GPUs; prescale keeps typical 1080p subs crisp.
      prescaleFactor: 0.8,
      prescaleHeightLimit: 1080,
      maxRenderHeight: 1440,
    });
    this.currentUrl = assUrl;
  }

  stop(): void {
    if (this.instance) {
      try {
        this.instance.destroy();
      } catch {
        /* ignore */
      }
      this.instance = null;
    }
    this.currentUrl = null;
  }
}
